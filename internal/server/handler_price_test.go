// 计价规则管理接口（/api/admin/prices）的单元测试，聚焦「渠道专属价」维度。
//
// 意图（Why）：
//
//	渠道专属价是本次新增的录入能力，最容易出错的三处必须锁死：
//	  1) 带 channel_id 的规则要能落库并原样读回（含 channel_name 回显）；
//	  2) channel_id 指向不存在的渠道时必须 400——否则会写出一条
//	     永远命不中的"僵尸价"，表现为"配了价却不生效"；
//	  3) (model, group_name, channel_id) 唯一约束冲突要翻译成可读的 400，
//	     而不是 500/409，管理员才知道该改哪个维度。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run Price → httptest 带管理员会话调用 Handler
//
// 扩展（Extend）：
//
//	新增计价维度或过滤条件时，按"正常路径 + 校验失败路径"补充用例。
package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// priceFixture 汇总计价规则接口测试所需的仓储与管理员会话。
type priceFixture struct {
	srv      *Server
	channels model.ChannelRepository
	prices   model.ModelPriceRepository
	adminTok string
}

// newPriceFixture 构造含渠道与计价仓储、带管理员会话的最小服务。
func newPriceFixture(t *testing.T) *priceFixture {
	t.Helper()
	gin.DefaultWriter = io.Discard

	dsn := filepath.Join(t.TempDir(), "price_server_test.db")
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
	cfg.Server.Listen = "127.0.0.1:0"

	channels := store.NewChannelRepository(st.DB(), cipher)
	prices := store.NewModelPriceRepository(st.DB())
	users := store.NewUserRepository(st.DB())
	sessions := store.NewSessionRepository(st.DB())

	admin := &model.User{
		Username: "price-admin", PasswordHash: "test-hash",
		Role: model.UserRoleAdmin, Status: model.UserStatusEnabled,
		Quota: model.QuotaUnlimited,
	}
	if err := users.Create(context.Background(), admin); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	srv := New(Deps{
		Config:      cfg,
		Store:       st,
		Channels:    channels,
		ModelPrices: prices,
		Users:       users,
		Sessions:    sessions,
		Settings:    store.NewSettingRepository(st.DB(), st.Dialect()),
	})

	return &priceFixture{
		srv:      srv,
		channels: channels,
		prices:   prices,
		adminTok: createPriceTestSession(t, sessions, admin.ID),
	}
}

// createPriceTestSession 为管理员建立一条有效会话并返回明文令牌。
func createPriceTestSession(t *testing.T, sessions model.SessionRepository, userID uint64) string {
	t.Helper()
	token := "session-" + strconv.FormatUint(userID, 10) + "-price-test-token"
	if err := sessions.Create(context.Background(), &model.Session{
		UserID:    userID,
		TokenHash: crypto.SHA256Hex(token),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	return token
}

// toUint64 把 JSON 解析出的数字转成 uint64，供断言使用。
func toUint64(value any) uint64 {
	number, _ := value.(float64)
	return uint64(number)
}

// createPriceTestChannel 通过管理接口创建一个渠道并返回其 ID。
func createPriceTestChannel(t *testing.T, fx *priceFixture, name string) uint64 {
	t.Helper()
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/channels", fx.adminTok,
		`{"name":"`+name+`","type":1,"base_url":"https://api.example.com","group":"default","priority":1,"weight":1,"status":1,"api_key":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建渠道失败：%d %s", rec.Code, rec.Body.String())
	}
	id, ok := body["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("创建渠道响应缺少有效 id：%v", body)
	}
	return uint64(id)
}

// TestPrice_创建渠道专属价并读回 覆盖「带 channel_id 落库 → 回显 channel_name → 按渠道过滤可查」。
func TestPrice_创建渠道专属价并读回(t *testing.T) {
	fx := newPriceFixture(t)
	const channelName = "专属价渠道-A"
	channelID := createPriceTestChannel(t, fx, channelName)

	body := `{"model":"gpt-4o","group":"default","channel_id":` + strconv.FormatUint(channelID, 10) +
		`,"prompt_price":1000000,"completion_price":2000000,"billing_mode":"token"}`
	rec, created := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/prices", fx.adminTok, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建渠道专属价应返回 200，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if got := toUint64(created["channel_id"]); got != channelID {
		t.Fatalf("响应 channel_id = %d，期望 %d", got, channelID)
	}
	if created["channel_name"] != channelName {
		t.Fatalf("响应 channel_name = %v，期望 %q", created["channel_name"], channelName)
	}

	// 直接查库校验：该规则确属指定渠道
	stored, err := fx.prices.ListForPricing(context.Background(), "default", channelID, false)
	if err != nil {
		t.Fatalf("查询计价规则失败：%v", err)
	}
	found := false
	for _, p := range stored {
		if p.Model == "gpt-4o" && p.ChannelID == channelID {
			found = true
		}
	}
	if !found {
		t.Fatalf("库中未找到 channel_id=%d 的专属价规则", channelID)
	}

	// 按渠道过滤：应能查到（默认价未配置，故只有这一条）
	listPath := "/api/admin/prices?channel_id=" + strconv.FormatUint(channelID, 10)
	rec, list := doBearerJSON(t, fx.srv, http.MethodGet, listPath, fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("按渠道查询应返回 200，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("按渠道过滤应返回 1 条，实际 %d（%v）", len(items), list)
	}
	first, _ := items[0].(map[string]any)
	if toUint64(first["channel_id"]) != channelID || first["channel_name"] != channelName {
		t.Fatalf("过滤结果未回显渠道信息：%v", first)
	}
}

// TestPrice_渠道不存在返回400 覆盖渠道归属校验（防止写入永不命中的僵尸价）。
func TestPrice_渠道不存在返回400(t *testing.T) {
	fx := newPriceFixture(t)

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/prices", fx.adminTok,
		`{"model":"gpt-4o","group":"default","channel_id":999999,"prompt_price":1,"completion_price":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("渠道不存在应返回 400，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if code := redeemErrorCode(body); code != "channel_not_found" {
		t.Fatalf("错误码应为 channel_not_found，实际 %q", code)
	}
	if msg := redeemErrorMessage(body); msg == "" {
		t.Fatal("错误信息不应为空，需给管理员可读提示")
	}
}

// TestPrice_唯一冲突返回400 覆盖「同一 模型 + 分组 + 渠道」的唯一约束冲突。
func TestPrice_唯一冲突返回400(t *testing.T) {
	fx := newPriceFixture(t)

	// 1) 创建失败：同一 (模型, 分组, 不限渠道) 重复创建
	base := `{"model":"dup-model","group":"default","prompt_price":1,"completion_price":1}`
	rec, _ := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/prices", fx.adminTok, base)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次创建应返回 200，实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/prices", fx.adminTok, base)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重复创建应返回 400（而非 500），实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if code := redeemErrorCode(body); code != "price_duplicated" {
		t.Fatalf("错误码应为 price_duplicated，实际 %q", code)
	}

	// 2) 更新失败：把渠道 B 的规则改到渠道 A 已占用的组合上
	channelA := createPriceTestChannel(t, fx, "冲突渠道-A")
	channelB := createPriceTestChannel(t, fx, "冲突渠道-B")
	for _, cid := range []uint64{channelA, channelB} {
		payload := `{"model":"dup-scoped","group":"default","channel_id":` + strconv.FormatUint(cid, 10) +
			`,"prompt_price":1,"completion_price":1}`
		rec, _ = doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/prices", fx.adminTok, payload)
		if rec.Code != http.StatusOK {
			t.Fatalf("创建渠道 %d 的专属价应返回 200，实际 %d", cid, rec.Code)
		}
	}

	// 取出渠道 B 那条规则的 ID
	scoped, err := fx.prices.ListForPricing(context.Background(), "default", channelB, false)
	if err != nil {
		t.Fatalf("查询计价规则失败：%v", err)
	}
	var ruleBID uint64
	for _, p := range scoped {
		if p.ChannelID == channelB {
			ruleBID = p.ID
		}
	}
	if ruleBID == 0 {
		t.Fatal("未找到渠道 B 的专属价规则")
	}

	updatePath := "/api/admin/prices/" + strconv.FormatUint(ruleBID, 10)
	updateBody := `{"channel_id":` + strconv.FormatUint(channelA, 10) + `}`
	rec, body = doBearerJSON(t, fx.srv, http.MethodPut, updatePath, fx.adminTok, updateBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("改到已占用组合应返回 400（而非 500），实际 %d，响应：%s", rec.Code, rec.Body.String())
	}
	if code := redeemErrorCode(body); code != "price_duplicated" {
		t.Fatalf("错误码应为 price_duplicated，实际 %q", code)
	}
}
