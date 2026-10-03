// 公开定价试算接口（GET /api/models/quote）的单元测试。
//
// 意图（Why）：
//
//	试算接口是模型广场的"费用预估"入口，用户据此判断"这个模型要花多少钱"。
//	它必须：① 公开可访问；② 与实际计费（Billing.Quote）同口径，不能各算各的；
//	③ 不泄露任何上游成本信息。本用例覆盖"按量计费"与"按次计费"两种口径。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run ModelQuote → httptest 直接调用 Handler（不带令牌）
//
// 扩展（Extend）：
//
//	新增计价维度（如缓存折扣、分级定价）时，在本文件补充对应断言。
package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/payment"
	"github.com/LTZY-ACU/aqua-api/internal/relay"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// newQuoteFixture 构造带计费组件（Billing）的最小服务，用于试算接口测试。
func newQuoteFixture(t *testing.T) (*Server, model.ModelPriceRepository, model.ModelGroupRepository) {
	t.Helper()
	gin.DefaultWriter = io.Discard

	dsn := filepath.Join(t.TempDir(), "quote_test.db")
	st, err := store.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	cipher, err := crypto.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}

	cfg := config.Default()
	cfg.Server.Mode = "test"

	channels := store.NewChannelRepository(st.DB(), cipher)
	groups := store.NewModelGroupRepository(st.DB())
	prices := store.NewModelPriceRepository(st.DB())
	billing := relay.NewBilling(prices, groups, nil, nil, model.DefaultGroupName)

	srv := New(Deps{
		Config:      cfg,
		Store:       st,
		Channels:    channels,
		Groups:      groups,
		ModelPrices: prices,
		Settings:    store.NewSettingRepository(st.DB(), st.Dialect()),
		Billing:     billing,
		Relay:       relay.New(channels, relay.Options{}),
		Payment:     payment.NewRegistry(payment.Options{}),
	})
	return srv, prices, groups
}

func TestModelQuote_按量计费口径(t *testing.T) {
	srv, prices, _ := newQuoteFixture(t)
	ctx := context.Background()

	// 每 1M token：输入 1_000_000 额度、输出 2_000_000 额度；默认分组倍率 100（1.0 倍）。
	if err := prices.Create(ctx, &model.ModelPrice{
		Model:           "chat-model",
		PromptPrice:     1_000_000,
		CompletionPrice: 2_000_000,
		Group:           model.DefaultGroupName,
		Enabled:         true,
	}); err != nil {
		t.Fatalf("创建计价规则失败: %v", err)
	}

	rec, body := doRequest(t, srv, http.MethodGet,
		"/api/models/quote?model=chat-model&prompt_tokens=1000000&completion_tokens=1000000")
	if rec.Code != http.StatusOK {
		t.Fatalf("公开试算应返回 200（无需登录），实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if mode, _ := body["billing_mode"].(string); mode != model.BillingModeToken {
		t.Fatalf("计费方式应为 token，实际 %v", body["billing_mode"])
	}
	if currency, _ := body["currency"].(string); currency != "CNY" {
		t.Fatalf("币种应为 CNY，实际 %v", body["currency"])
	}
	// 1M 输入(1e6) + 1M 输出(2e6) = 3e6 额度（默认分组倍率 100，不折算）
	if total, _ := body["total_cost"].(float64); total != 3e6 {
		t.Fatalf("总费用应为 3e6 额度，实际 %v", body["total_cost"])
	}
	if input, _ := body["input_cost"].(float64); input != 1e6 {
		t.Fatalf("输入费用应为 1e6 额度，实际 %v", body["input_cost"])
	}
	if output, _ := body["output_cost"].(float64); output != 2e6 {
		t.Fatalf("输出费用应为 2e6 额度，实际 %v", body["output_cost"])
	}
	if cached, _ := body["cached_cost"].(float64); cached != 0 {
		t.Fatalf("未指定缓存命中时缓存费用应为 0，实际 %v", body["cached_cost"])
	}
	// 单价为"每 100 万 token"：输入 1e6 额度 → 1e6
	if unit, _ := body["input_unit_price"].(float64); unit != 1e6 {
		t.Fatalf("输入单价应为 1e6 额度/1M token，实际 %v", body["input_unit_price"])
	}
	if unit, _ := body["output_unit_price"].(float64); unit != 2e6 {
		t.Fatalf("输出单价应为 2e6 额度/1M token，实际 %v", body["output_unit_price"])
	}
}

func TestModelQuote_按次计费口径(t *testing.T) {
	srv, prices, _ := newQuoteFixture(t)
	ctx := context.Background()

	// 只填按次价 → EffectiveBillingMode 自动判定为 per_call。
	if err := prices.Create(ctx, &model.ModelPrice{
		Model:        "image-model",
		PerCallPrice: 5000,
		Group:        model.DefaultGroupName,
		Enabled:      true,
	}); err != nil {
		t.Fatalf("创建计价规则失败: %v", err)
	}

	rec, body := doRequest(t, srv, http.MethodGet,
		"/api/models/quote?model=image-model&prompt_tokens=1000000&completion_tokens=1000000")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if mode, _ := body["billing_mode"].(string); mode != model.BillingModePerCall {
		t.Fatalf("计费方式应为 per_call，实际 %v", body["billing_mode"])
	}
	// 按次：5000 额度（默认分组倍率 100，不折算）；token 分量不参与
	if total, _ := body["total_cost"].(float64); total != 5000 {
		t.Fatalf("按次总费用应为 5000 额度，实际 %v", body["total_cost"])
	}
	for _, key := range []string{"input_cost", "output_cost", "cached_cost"} {
		if v, _ := body[key].(float64); v != 0 {
			t.Fatalf("按次计费时 %s 应为 0，实际 %v", key, body[key])
		}
	}
}

func TestModelQuote_未定价与缺参(t *testing.T) {
	srv, _, _ := newQuoteFixture(t)

	// 未定价模型：返回 200 但金额全 0、billing_mode 为空（区别于"免费"）
	rec, body := doRequest(t, srv, http.MethodGet, "/api/models/quote?model=not-priced")
	if rec.Code != http.StatusOK {
		t.Fatalf("未定价模型应返回 200，实际 %d", rec.Code)
	}
	if mode, _ := body["billing_mode"].(string); mode != "" {
		t.Fatalf("未定价时 billing_mode 应为空，实际 %v", body["billing_mode"])
	}
	if total, _ := body["total_cost"].(float64); total != 0 {
		t.Fatalf("未定价时总费用应为 0，实际 %v", body["total_cost"])
	}

	// 缺 model 参数：400
	rec, _ = doRequest(t, srv, http.MethodGet, "/api/models/quote")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少 model 参数应返回 400，实际 %d", rec.Code)
	}
}

// TestGroupRPM_挂载到转发链路 验证 RPM 中间件确实挂在 /v1 上并生效。
//
// 借助模型列表接口（GET /v1/models）触发转发链路的中间件：
// 把默认分组的 rpm_limit 设为 2 后，前两次放行、第三次必须 429。
func TestGroupRPM_挂载到转发链路(t *testing.T) {
	fx := newPlazaFixture(t)
	ctx := context.Background()

	group, err := fx.groups.GetByName(ctx, model.DefaultGroupName)
	if err != nil {
		t.Fatalf("读取默认分组失败: %v", err)
	}
	group.RpmLimit = 2
	if err := fx.groups.Update(ctx, group); err != nil {
		t.Fatalf("更新默认分组 RPM 失败: %v", err)
	}

	for i := 1; i <= 2; i++ {
		rec, _ := doAuthRequest(t, fx.server, http.MethodGet, "/v1/models", fx.plainToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际 %d，响应：%s", i, rec.Code, rec.Body.String())
		}
	}
	rec, body := doAuthRequest(t, fx.server, http.MethodGet, "/v1/models", fx.plainToken)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次请求应返回 429，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "quota.group_rpm_exceeded" {
		t.Fatalf("错误 code 应为 quota.group_rpm_exceeded，实际 %v", errObj["code"])
	}
}
