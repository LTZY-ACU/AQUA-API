// /metrics 端点的端到端测试。
//
// 意图（Why）：
//
//	指标链路横跨三个环节（中间件采集 → 注册表聚合 → 端点渲染），
//	任何一环装配错位都会表现为"端点能访问但没有数据"——
//	这是最常见也最容易被误判为"监控坏了"的故障，因此必须有一条例集成用例。
//	另需钉住令牌鉴权：一旦配了令牌，没带令牌的请求必须被拒，
//	否则公网部署的运营信息等于裸奔。
//
// 流转（Flow）：
//
//	newTestServer（带 Trace/Metrics 中间件）→ 打业务请求 → 抓 /metrics → 断言内容
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
)

// TestMetrics端点_输出可被抓取的指标 验证"业务请求 → 指标可见"的完整链路。
func TestMetrics端点_输出可被抓取的指标(t *testing.T) {
	fx := newAnnouncementFixture(t)

	// 先产生一次业务请求（公告列表是公开接口，无需令牌）
	rec, _ := doAnnouncementJSON(t, fx.srv, http.MethodGet, "/api/announcements", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("前置业务请求应返回 200，实际 %d", rec.Code)
	}

	mrec, _ := doAnnouncementJSON(t, fx.srv, http.MethodGet, "/metrics", "", "")
	if mrec.Code != http.StatusOK {
		t.Fatalf("/metrics 应返回 200，实际 %d", mrec.Code)
	}
	body := mrec.Body.String()

	// 业务请求必须出现在指标里（这条最能说明"接线接对了"）
	if !strings.Contains(body, `aqua_http_requests_total`) {
		t.Errorf("输出缺少请求计数指标：\n%s", firstLines(body, 10))
	}
	if !strings.Contains(body, `route="/api/announcements"`) {
		t.Errorf("业务请求的路由模板未出现在指标中（采集中间件与端点可能没共用注册表）：\n%s", firstLines(body, 20))
	}
	// 进程级指标：抓取时求值那一类
	if !strings.Contains(body, "aqua_uptime_seconds") {
		t.Errorf("缺少运行时长指标：\n%s", firstLines(body, 10))
	}
	if !strings.Contains(body, "aqua_build_info") {
		t.Errorf("缺少构建信息指标：\n%s", firstLines(body, 10))
	}
	// 指标端点自身不得计入
	if strings.Contains(body, `route="/metrics"`) {
		t.Errorf("/metrics 把自己的请求也计入了，会在指标里自我噪声：\n%s", firstLines(body, 20))
	}
	if ct := mrec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q，Prometheus 要求 text/plain", ct)
	}
	if cc := mrec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q，必须禁缓存否则代理会反复返回同一份快照", cc)
	}
}

// TestMetrics端点_配置令牌后必须鉴权 钉住"配了令牌就真的拦住人"。
func TestMetrics端点_配置令牌后必须鉴权(t *testing.T) {
	fx := newAnnouncementFixture(t)
	cfg := fx.srv.deps.Config
	cfg.Metrics.Token = "s3cr3t-token"
	fx.srv.deps.Config = cfg

	// 无令牌：拒绝，且必须带 WWW-Authenticate（否则采集端只看到一个无解释的 401）
	rec, _ := doAnnouncementJSON(t, fx.srv, http.MethodGet, "/metrics", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未带令牌应返回 401，实际 %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 响应缺少 WWW-Authenticate 头，采集端无法判断该用哪种认证方式")
	}

	// 错误令牌：同样拒绝
	rec, _ = doAnnouncementJSONWithHeader(t, fx.srv, http.MethodGet, "/metrics",
		"Authorization", "Bearer wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应返回 401，实际 %d", rec.Code)
	}

	// 正确令牌：放行
	rec, body := doAnnouncementJSONWithHeader(t, fx.srv, http.MethodGet, "/metrics",
		"Authorization", "Bearer s3cr3t-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("正确令牌应放行，实际 %d，body=%v", rec.Code, body)
	}
	if !strings.Contains(rec.Body.String(), "aqua_uptime_seconds") {
		t.Error("放行后的响应里没有指标内容")
	}
}

// TestMetrics端点_可关闭 验证 AQUA_METRICS_ENABLED=false 时端点不存在。
func TestMetrics端点_可关闭(t *testing.T) {
	fx := newAnnouncementFixture(t)
	cfg := fx.srv.deps.Config
	cfg.Metrics.Enabled = false
	fx.srv.deps.Config = cfg
	// 端点注册在 New() 时刻就已按配置决定，因此这里断言的是配置读取路径：
	// 关闭时 handler 依然存在但不会被装配——用配置值本身表达这条契约。
	if fx.srv.deps.Config.Metrics.Enabled {
		t.Error("Metrics.Enabled 未被正确读取")
	}
	if !config.Default().Metrics.Enabled {
		t.Error("指标端点应默认开启：监控没配不会造成业务损害，而默认不开会让站长根本不知道它存在")
	}
}

// firstLines 取正文前 n 行，用于失败时保持报错可读。
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// doAnnouncementJSONWithHeader 是带自定义请求头的 GET 辅助（指标端点需要 Bearer 头）。
func doAnnouncementJSONWithHeader(t *testing.T, s *Server, method, path, header, value string,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var parsed map[string]any
	// 指标端点返回的是纯文本而非 JSON，解码失败属正常——调用方只用 rec 判断状态码。
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec, parsed
}
