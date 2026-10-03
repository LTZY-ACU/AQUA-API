// 模型实时指标接口的测试。
//
// 意图（Why）：
//   模型详情页的 tokens/s、平均耗时、TTFB 直接来自 usage_logs 聚合，
//   口径必须与概览页一致（否则用户在两个页面看到不同数字会困惑）。
//   本测试锁定：
//     1) 只统计成功请求（失败请求不参与 TPS/耗时平均）；
//     2) TPS 用有速率样本的请求做分母（与 relay 落库口径一致）。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// TestModelStats_AggregatesOnlySuccess 验证：
//   - 失败请求不进入 TPS/耗时统计（只统计成功请求，与 Summary 口径一致）；
//   - 平均耗时只按成功请求计算。
func TestModelStats_AggregatesOnlySuccess(t *testing.T) {
	st := openTestModelStatsStore(t)
	repo := store.NewUsageLogRepository(st.DB(), st.Dialect())
	now := time.Now()

	// 成功请求 2 条：耗时 50 与 100 → 平均 75；TPS 10 与 30 → 平均 20
	createStatsLog(t, repo, "gpt-4o", 200, 10, 50, now.Add(-time.Minute))
	createStatsLog(t, repo, "gpt-4o", 200, 30, 100, now.Add(-2*time.Minute))
	// 失败请求 1 条：不参与统计
	createStatsLog(t, repo, "gpt-4o", 500, 999, 999, now.Add(-3*time.Minute))
	// 其它模型 1 条：不参与
	createStatsLog(t, repo, "claude-3", 200, 7, 7, now.Add(-time.Minute))

	srv := newModelStatsServer(t, st)

	req := httptest.NewRequest(http.MethodGet, "/api/user/models/stats?model=gpt-4o&minutes=30", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}

	var dto modelStatsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if dto.Requests != 2 {
		t.Errorf("requests = %d，期望 2（失败请求不计入）", dto.Requests)
	}
	if dto.AvgTokensPerSecond != 20 {
		t.Errorf("avg_tokens_per_second = %v，期望 20（只按成功请求平均）", dto.AvgTokensPerSecond)
	}
	if dto.AvgLatencyMS != 75 {
		// (50+100)/2 = 75
		t.Errorf("avg_latency_ms = %v，期望 75", dto.AvgLatencyMS)
	}
}

// TestModelStats_OfflineModel 验证无启用渠道声明的模型返回 available=false。
func TestModelStats_OfflineModel(t *testing.T) {
	st := openTestModelStatsStore(t)
	srv := newModelStatsServer(t, st)

	req := httptest.NewRequest(http.MethodGet, "/api/user/models/stats?model=nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var dto modelStatsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if dto.Available {
		t.Error("不存在的模型应返回 available=false")
	}
	if dto.ChannelCount != 0 {
		t.Errorf("channel_count = %d，期望 0", dto.ChannelCount)
	}
}

// TestModelStats_模型名带斜杠也能查询 是回归测试：本项目对外模型名普遍带斜杠
// （如 LTZY-CALL/deepseek-v4.1-flash），早期把它放进路径段（/models/:model/stats）
// 会被 gin 的路由树拆成多段而 404 —— 本用例钉住"模型名走查询参数"这一约定，
// 防止后人再改回路径参数。
func TestModelStats_模型名带斜杠也能查询(t *testing.T) {
	st := openTestModelStatsStore(t)
	repo := store.NewUsageLogRepository(st.DB(), st.Dialect())
	now := time.Now()

	const slashed = "LTZY-CALL/deepseek-v4.1-flash"
	createStatsLog(t, repo, slashed, 200, 42, 80, now.Add(-time.Minute))

	srv := newModelStatsServer(t, st)

	// 走查询参数（注意 model 值里含斜杠，模拟真实模型名）
	req := httptest.NewRequest(http.MethodGet,
		"/api/user/models/stats?model="+url.QueryEscape(slashed), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("带斜杠的模型名应能查询（状态码 = %d，期望 200）；body=%s", rec.Code, rec.Body.String())
	}
	var dto modelStatsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if dto.Requests != 1 {
		t.Errorf("requests = %d，期望 1（该模型名的成功请求）", dto.Requests)
	}
}

// ---------- 测试基础设施 ----------

// openTestModelStatsStore 打开内存 SQLite 并迁移（与 store 测试同构）。
func openTestModelStatsStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "stats_test.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate 失败: %v", err)
	}
	return st
}

// newModelStatsServer 构造仅含 stats 路由的 Server（Channels 用真实仓储，测试库无渠道 → 离线）。
func newModelStatsServer(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	// 渠道仓储需要加密器（测试库无渠道，密钥随意但需合法）
	cipher, err := crypto.New("test-key-material-for-stats-test")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}

	s := &Server{
		engine: engine,
		deps: Deps{
			UsageLogs: store.NewUsageLogRepository(st.DB(), st.Dialect()),
			Channels:  store.NewChannelRepository(st.DB(), cipher),
		},
	}
	engine.GET("/api/user/models/stats",
		func(c *gin.Context) {
			// 注入登录用户（模拟 SessionAuth 成功后的上下文，绕过中间件）
			c.Set("aqua.context.user", &model.User{
				ID: 1, Username: "stats-test-user",
				Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: model.QuotaUnlimited,
			})
			c.Next()
		},
		s.handleModelStats)
	return engine
}

// createStatsLog 写入一条带 TPS 与耗时的日志。
func createStatsLog(t *testing.T, repo model.UsageLogRepository, modelName string, status, tps, latency int, at time.Time) {
	t.Helper()
	entry := &model.UsageLog{
		UserID:          1,
		ChannelID:       1,
		Model:           modelName,
		TotalTokens:     100,
		StatusCode:      status,
		LatencyMS:       latency,
		TokensPerSecond: float64(tps),
		CreatedAt:       at,
	}
	if err := repo.Create(context.Background(), entry); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}
}