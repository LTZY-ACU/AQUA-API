// 本文件验证「模型广场测速数据按查看者身份下发」的规则。
//
// 意图（Why）：
//
//	「广场公示」开关只约束普通用户；管理员（后台内嵌广场 / 登录态）
//	必须始终能看到测速数据——否则站长关掉公示的同时失去了核对测速结果的入口，
//	只能再开回去看一眼，这个开关就变成了"自伤"设计。
//
// 流转（Flow）：
//
//	relay 测速 → ModelSpeedRepository.Upsert
//	  → GET /api/models（匿名 / 普通用户 / 管理员三种身份）
//	    → plazaSpeedByModel 按 viewerIsAdmin 决定是否豁免公示开关
//
// 扩展（Extend）：
//
//	日后增加"按分组公示延迟"之类的细粒度开关时，在本文件补对应身份的断言。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// TestModelPlaza_测速公示关闭时仅管理员可见 锁住豁免行为：
// 同一份测速数据，公示关闭后匿名看不到、管理员能看到。
func TestModelPlaza_测速公示关闭时仅管理员可见(t *testing.T) {
	gin.DefaultWriter = io.Discard

	dsn := filepath.Join(t.TempDir(), "plaza_speed_test.db")
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
	channels := store.NewChannelRepository(st.DB(), cipher)
	settings := store.NewSettingRepository(st.DB(), st.Dialect())
	speeds := store.NewModelSpeedRepository(st.DB())

	// 公示关闭（测速总开关保持默认开启）：这是本用例的核心前提。
	if err := settings.Set(ctx, model.SettingKeySpeedTestPublic, "false"); err != nil {
		t.Fatalf("关闭公示开关失败: %v", err)
	}

	// 一个启用渠道 + 一个模型，并写入一条成功测速结果。
	if err := channels.Create(ctx, &model.Channel{
		Name: "测速渠道", Type: 1, BaseURL: "https://api.example.com", APIKey: "sk-test",
		Models: []string{"m-fast"}, Group: model.DefaultGroupName,
		Groups:   []string{model.DefaultGroupName},
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	if err := speeds.Upsert(ctx, &model.ModelSpeedResult{
		ChannelID: 1, Model: "m-fast", UpstreamModel: "m-fast",
		OK: true, StatusCode: 200, TTFBMS: 320, TotalMS: 320,
		TestedAt: time.Now(),
	}); err != nil {
		t.Fatalf("写入测速结果失败: %v", err)
	}

	admin := &model.User{
		Username: "admin-user", PasswordHash: "test-hash",
		Role: model.UserRoleAdmin, Status: model.UserStatusEnabled, Quota: model.QuotaUnlimited,
	}
	if err := users.Create(ctx, admin); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	adminTok := "session-admin-" + time.Now().Format("150405.000000000")
	if err := sessions.Create(ctx, &model.Session{
		UserID: admin.ID, TokenHash: crypto.SHA256Hex(adminTok), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("创建管理员会话失败: %v", err)
	}

	srv := New(Deps{
		Config: cfg, Store: st, Channels: channels, Users: users, Sessions: sessions,
		Settings: settings, ModelSpeeds: speeds,
	})

	// 匿名：公示关闭 → 不下发延迟字段。
	if _, card := plazaItemOf(t, srv, ""); card["speed_ttfb_ms"] != nil {
		t.Errorf("公示关闭时匿名请求不应下发 speed_ttfb_ms，实际 %v", card["speed_ttfb_ms"])
	}
	// 管理员：豁免公示开关 → 延迟可见且数值正确。
	if _, card := plazaItemOf(t, srv, adminTok); card["speed_ttfb_ms"] == nil {
		t.Fatal("管理员应豁免公示开关看到 speed_ttfb_ms，实际未下发")
	} else if got := int(card["speed_ttfb_ms"].(float64)); got != 320 {
		t.Errorf("speed_ttfb_ms 应为 320，实际 %d", got)
	}
}

// plazaItemOf 请求 /api/models 并返回唯一模型卡片；token 为空表示匿名。
func plazaItemOf(t *testing.T, srv *Server, token string) (string, map[string]any) {
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
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("应返回 1 个模型，实际 %v", body["items"])
	}
	card, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("卡片结构异常：%v", items[0])
	}
	return "m-fast", card
}
