// 代理视图（模型广场按查看者分组出模型与价格）的单元测试。
//
// 意图（Why）：
//
//	代理生意依赖三件事同时成立，任何一条错了都会直接伤到钱：
//	  1) 代理在广场看到的必须是【自己那一档的模型与价格】，而不是公开清单；
//	  2) 折后价必须与计费口径一致（基础额度 × 分组倍率 ÷ 100），
//	     否则用户按广场价估算的花费与实际扣费对不上——最直接的投诉来源；
//	  3) 普通用户 / 匿名访客【完全看不到】批发分组的存在（价格体系红线，
//	     与 TestModelPlaza_隐藏仅后台分组 同一条底线，此处再覆盖"带普通会话"的情形）。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run AgentPlaza → httptest 调用 /api/models（带 / 不带会话）
//
// 扩展（Extend）：
//
//	日后代理支持"一人多分组"时，在本文件补"取第一个生效授权"的断言即可。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

type agentPlazaFixture struct {
	srv         *Server
	groups      model.ModelGroupRepository
	prices      model.ModelPriceRepository
	channels    model.ChannelRepository
	agentTok    string
	normalTok   string
	agentUserID uint64
}

// newAgentPlazaFixture 构造含"代理分组 + 两档价格 + 两套渠道"的最小可用服务。
func newAgentPlazaFixture(t *testing.T) *agentPlazaFixture {
	t.Helper()
	gin.DefaultWriter = io.Discard

	dsn := filepath.Join(t.TempDir(), "agent_plaza_test.db")
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

	ctx := context.Background()
	users := store.NewUserRepository(st.DB())
	sessions := store.NewSessionRepository(st.DB())
	groups := store.NewModelGroupRepository(st.DB())
	prices := store.NewModelPriceRepository(st.DB())
	channels := store.NewChannelRepository(st.DB(), cipher)

	// 战略代理分组：倍率 60 = 拿货 6 折；admin_only 表示不对公众开放。
	if err := groups.Create(ctx, &model.ModelGroup{
		Name: "agent", DisplayName: "战略代理", Ratio: 60,
		AdminOnly: true, Enabled: true, Description: "代理拿货价",
	}); err != nil {
		t.Fatalf("创建代理分组失败: %v", err)
	}

	// 价格：同一模型在公开档与代理档各有一条规则，金额相同——折扣只由分组倍率表达。
	priceRows := []*model.ModelPrice{
		{Model: "m-shared", Group: model.DefaultGroupName, PromptPrice: 1_000_000, Enabled: true},
		{Model: "m-shared", Group: "agent", PromptPrice: 1_000_000, Enabled: true},
		{Model: "m-public-only", Group: model.DefaultGroupName, PromptPrice: 3_000_000, Enabled: true},
		{Model: "m-wholesale-only", Group: "agent", PromptPrice: 2_000_000, Enabled: true},
	}
	for _, row := range priceRows {
		if err := prices.Create(ctx, row); err != nil {
			t.Fatalf("创建价格规则失败: %v", err)
		}
	}

	// 渠道：公开档服务两个模型，代理档只服务其中一个（另一个用于验证"不可用"标记）。
	if err := channels.Create(ctx, &model.Channel{
		Name: "公开渠道", Type: 1, BaseURL: "https://api.example.com", APIKey: "sk-public",
		Models: []string{"m-shared", "m-public-only"}, Group: model.DefaultGroupName,
		Groups:   []string{model.DefaultGroupName},
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}); err != nil {
		t.Fatalf("创建公开渠道失败: %v", err)
	}
	if err := channels.Create(ctx, &model.Channel{
		Name: "代理渠道", Type: 1, BaseURL: "https://api.example.com", APIKey: "sk-agent",
		Models: []string{"m-shared"}, Group: "agent",
		Groups:   []string{"agent"},
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}); err != nil {
		t.Fatalf("创建代理渠道失败: %v", err)
	}

	agentUser := &model.User{
		Username: "agent-user", PasswordHash: "test-hash",
		Role: model.UserRoleUser, Status: model.UserStatusEnabled,
		Quota: model.QuotaUnlimited, AgentGroup: "agent",
	}
	normalUser := &model.User{
		Username: "normal-user", PasswordHash: "test-hash",
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: model.QuotaUnlimited,
	}
	for _, u := range []*model.User{agentUser, normalUser} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatalf("创建测试用户失败: %v", err)
		}
	}

	fx := &agentPlazaFixture{
		groups:      groups,
		prices:      prices,
		channels:    channels,
		agentUserID: agentUser.ID,
		agentTok:    createAgentPlazaSession(t, sessions, agentUser.ID, "agent"),
		normalTok:   createAgentPlazaSession(t, sessions, normalUser.ID, "normal"),
	}
	fx.srv = New(Deps{
		Config:      cfg,
		Store:       st,
		Channels:    channels,
		Groups:      groups,
		Users:       users,
		Sessions:    sessions,
		Settings:    store.NewSettingRepository(st.DB(), st.Dialect()),
		ModelPrices: prices,
	})
	return fx
}

// createAgentPlazaSession 为用户建立一条有效会话并返回明文令牌。
func createAgentPlazaSession(t *testing.T, sessions model.SessionRepository, userID uint64, tag string) string {
	t.Helper()
	token := "session-" + tag + "-" + strconv.FormatUint(userID, 10)
	if err := sessions.Create(context.Background(), &model.Session{
		UserID:    userID,
		TokenHash: crypto.SHA256Hex(token),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	return token
}

// getPlaza 请求 /api/models；token 为空表示匿名访问。
func getPlaza(t *testing.T, srv *Server, token string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	body := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return body
}

// priceOf 从卡片里取出 prices[0]。
func priceOf(t *testing.T, card map[string]any) map[string]any {
	t.Helper()
	list, ok := card["prices"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("卡片缺少价格：%v", card)
	}
	price, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("价格结构异常：%v", list[0])
	}
	return price
}

// TestAgentPlaza_代理只看本档模型且给出折后价与划线原价 锁住代理视图的核心行为。
func TestAgentPlaza_代理只看本档模型且给出折后价与划线原价(t *testing.T) {
	fx := newAgentPlazaFixture(t)
	body := getPlaza(t, fx.srv, fx.agentTok)

	viewer, ok := body["viewer"].(map[string]any)
	if !ok {
		t.Fatalf("代理访问应下发 viewer，实际：%v", body)
	}
	if viewer["agent_group"] != "agent" || viewer["label"] != "战略代理" {
		t.Fatalf("viewer 身份不符：%v", viewer)
	}
	if ratio, _ := viewer["ratio"].(float64); ratio != 60 {
		t.Fatalf("viewer.ratio 应为 60（拿货 6 折），实际 %v", viewer["ratio"])
	}

	items := plazaItems(t, body)
	if len(items) != 2 {
		t.Fatalf("代理清单应只含本档有价格的模型（m-shared / m-wholesale-only），实际 %d 个：%v", len(items), items)
	}
	if _, exists := items["m-public-only"]; exists {
		t.Fatalf("仅公开档有价格的模型不应出现在代理清单里：%v", items)
	}

	// m-shared：规则 1_000_000，代理价 = 1_000_000 × 60 ÷ 100 = 600_000，原价 = 1_000_000
	shared := items["m-shared"]
	if price, _ := priceOf(t, shared)["prompt_price"].(float64); price != 600_000 {
		t.Fatalf("m-shared 代理价应为 600000，实际 %v", priceOf(t, shared)["prompt_price"])
	}
	list, ok := shared["list_price"].(map[string]any)
	if !ok {
		t.Fatalf("代理视图必须下发划线原价：%v", shared)
	}
	if price, _ := list["prompt_price"].(float64); price != 1_000_000 {
		t.Fatalf("m-shared 原价应为 1000000，实际 %v", list["prompt_price"])
	}
	if available, _ := shared["available"].(bool); !available {
		t.Fatalf("m-shared 有代理档渠道，应标记为可用：%v", shared)
	}

	// m-wholesale-only：有代理价但代理档没有渠道 → 出现在清单里但标记不可用
	agentOnly := items["m-wholesale-only"]
	if price, _ := priceOf(t, agentOnly)["prompt_price"].(float64); price != 1_200_000 {
		t.Fatalf("m-wholesale-only 代理价应为 1200000，实际 %v", priceOf(t, agentOnly)["prompt_price"])
	}
	if available, _ := agentOnly["available"].(bool); available {
		t.Fatalf("m-wholesale-only 在代理档没有渠道，应标记为不可用：%v", agentOnly)
	}

	// 分组视图：代理只应看到自己那一档
	groupsRaw, _ := body["groups"].([]any)
	if len(groupsRaw) != 1 {
		t.Fatalf("代理视图的分组列表应只有 1 项，实际 %v", groupsRaw)
	}
	if g, _ := groupsRaw[0].(map[string]any); g["name"] != "agent" {
		t.Fatalf("代理视图的分组应为 agent，实际 %v", groupsRaw[0])
	}
}

// TestAgentPlaza_普通用户与匿名访客都看不到批发分组 锁住价格体系的红线。
func TestAgentPlaza_普通用户与匿名访客都看不到批发分组(t *testing.T) {
	fx := newAgentPlazaFixture(t)

	for name, token := range map[string]string{"匿名": "", "普通用户": fx.normalTok} {
		body := getPlaza(t, fx.srv, token)
		if _, exists := body["viewer"]; exists {
			t.Fatalf("%s不应下发 viewer：%v", name, body["viewer"])
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化响应失败: %v", err)
		}
		if strings.Contains(string(raw), "agent") || strings.Contains(string(raw), "战略代理") {
			t.Fatalf("%s视图泄露了批发分组信息：%s", name, raw)
		}
	}
}

// TestAgentPlaza_分组停用后降级为公开视图 保证配置异常不会把广场打成 5xx。
func TestAgentPlaza_分组停用后降级为公开视图(t *testing.T) {
	fx := newAgentPlazaFixture(t)

	group, err := fx.groups.GetByName(context.Background(), "agent")
	if err != nil {
		t.Fatalf("读取代理分组失败: %v", err)
	}
	group.Enabled = false
	if err := fx.groups.Update(context.Background(), group); err != nil {
		t.Fatalf("停用代理分组失败: %v", err)
	}

	body := getPlaza(t, fx.srv, fx.agentTok)
	if _, exists := body["viewer"]; exists {
		t.Fatalf("分组停用后应按公开视图处理，不应下发 viewer：%v", body["viewer"])
	}
	if _, exists := plazaItems(t, body)["m-public-only"]; !exists {
		t.Fatalf("降级为公开视图后应能正常看到公开模型")
	}
}
