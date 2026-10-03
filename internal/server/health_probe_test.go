// 渠道健康巡检（自动延迟刷新）的单元测试。
//
// 意图（Why）：
//
//	这项功能的价值全在于"数据是不是真的在自动刷新"，而它恰恰是最容易悄悄失效的一类代码：
//	协程没起来、参数归一写错导致间隔变成 0、或者部分列更新写错了字段——
//	这些都不会让服务报错，只会让后台的延迟永远停在某个陈旧值上，
//	管理员还以为"系统说它就是这么快"。本文件把三条链路钉住：
//
//	  1. 配置归一：非正值必须回退到默认值（否则 time.NewTicker(0) 会 panic 级失控）；
//	  2. 落库：巡检跑完之后渠道行上必须同时出现延迟、状态码与命中模型；
//	  3. 范围：没有可用模型的渠道不参与巡检（探测它们没有意义，还会污染计数）。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run HealthProbe
//
// 扩展（Extend）：
//
//	新增巡检项（例如"连续失败自动禁用渠道"）时，在本文件按同样的三段式补用例：
//	配置归一 → 结果落库 → 边界不误伤。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestHealthProbeConfigFrom_非正值回退默认 覆盖配置归一（含防御 ticker 失控）。
func TestHealthProbeConfigFrom_非正值回退默认(t *testing.T) {
	c := healthProbeConfigFrom(config.HealthConfig{Enabled: true})
	if c.Interval != config.DefaultHealthCheckIntervalMinutes*60*1e9 {
		t.Fatalf("间隔应回退到 %d 分钟，实际 %v", config.DefaultHealthCheckIntervalMinutes, c.Interval)
	}
	if c.Concurrency != config.DefaultHealthCheckConcurrency {
		t.Errorf("并发应回退到 %d，实际 %d", config.DefaultHealthCheckConcurrency, c.Concurrency)
	}
	if c.Timeout <= 0 {
		t.Errorf("超时必须为正，实际 %v", c.Timeout)
	}

	// 全零配置（连 Enabled 都没设置）同样要能算出一套可安全运行的参数：
	// StartHealthProbe 在 Enabled=false 时会直接返回，不会走到这里，
	// 但把它算出来能证明"任何输入下都不会出现 0 间隔的 ticker"。
	zero := healthProbeConfigFrom(config.HealthConfig{})
	if zero.Interval <= 0 || zero.Concurrency <= 0 || zero.Timeout <= 0 {
		t.Fatalf("零值配置未正确归一：%+v", zero)
	}
}

// newHealthProbeFixture 造一个带本地上游的服务，返回 fixture。
//
// 上游刻意用 httptest 起本地服务：巡检会真的发出 HTTP 请求，
// 若指向真实外网，用例的成败就取决于当时的网络状况。
func newHealthProbeFixture(t *testing.T, upstream http.Handler) (*Server, *httptest.Server) {
	t.Helper()

	srv, _ := newTestServer(t)
	ts := httptest.NewServer(upstream)
	t.Cleanup(ts.Close)
	return srv, ts
}

// TestHealthProbe_巡检结果落库 覆盖"跑完一轮，渠道行上就有新鲜延迟"。
func TestHealthProbe_巡检结果落库(t *testing.T) {
	srv, upstream := newHealthProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-probe","choices":[{"message":{"content":"ok"}}]}`))
	}))

	ctx := context.Background()
	ch := &model.Channel{
		Name: "延迟巡检渠道", Type: 1, BaseURL: upstream.URL, APIKey: "sk-probe",
		Models: []string{"probe-model"}, Group: model.DefaultGroupName,
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}
	if err := srv.deps.Channels.Create(ctx, ch); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	got := srv.ProbeAllChannels(ctx, healthProbeConfigFrom(config.HealthConfig{Enabled: true}))
	if got != 1 {
		t.Fatalf("本轮应巡检 1 个渠道，实际 %d（返回 -1 表示连列表都没查出来）", got)
	}

	reloaded, err := srv.deps.Channels.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	if !reloaded.LastTestOK {
		t.Errorf("本地上游返回 200，巡检结果应为可用：%q", reloaded.LastTestModel)
	}
	if reloaded.LastTestAt.IsZero() {
		t.Error("巡检后 last_test_at 未写入（后台无法判断这个延迟是否新鲜）")
	}
	if reloaded.LastTestCode != http.StatusOK {
		t.Errorf("last_test_code = %d，期望 200（运维靠它区分 401/404/429）", reloaded.LastTestCode)
	}
	if reloaded.LastTestModel != "probe-model" {
		t.Errorf("last_test_model = %q，期望记录命中的模型", reloaded.LastTestModel)
	}
	if reloaded.LatencyMS < 0 {
		t.Errorf("latency_ms = %d，不应为负", reloaded.LatencyMS)
	}
}

// TestHealthProbe_无模型的渠道不参与巡检 覆盖边界不误伤。
func TestHealthProbe_无模型的渠道不参与巡检(t *testing.T) {
	srv, upstream := newHealthProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))

	ctx := context.Background()
	// 无模型渠道：探测它没有意义（不知道该发给上游哪个名字），必须跳过。
	if err := srv.deps.Channels.Create(ctx, &model.Channel{
		Name: "没有配模型的渠道", Type: 1, BaseURL: upstream.URL, APIKey: "sk-x",
		Group: model.DefaultGroupName, Priority: 1, Weight: 1,
		Status: model.ChannelStatusEnabled,
	}); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	// 停用渠道：同样不该出现在巡检名单里。
	if err := srv.deps.Channels.Create(ctx, &model.Channel{
		Name: "已停用渠道", Type: 1, BaseURL: upstream.URL, APIKey: "sk-x",
		Models: []string{"probe-model"}, Group: model.DefaultGroupName,
		Priority: 1, Weight: 1, Status: model.ChannelStatusDisabled,
	}); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	if got := srv.ProbeAllChannels(ctx, healthProbeConfigFrom(config.HealthConfig{Enabled: true})); got != 0 {
		t.Fatalf("无可用渠道时应返回 0，实际 %d", got)
	}
}

// TestHealthProbe_上游失败也要记录不健康 覆盖"巡检能发现故障"这一核心价值。
func TestHealthProbe_上游失败也要记录不健康(t *testing.T) {
	srv, upstream := newHealthProbeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))

	ctx := context.Background()
	ch := &model.Channel{
		Name: "密钥已失效的渠道", Type: 1, BaseURL: upstream.URL, APIKey: "sk-expired",
		Models: []string{"probe-model"}, Group: model.DefaultGroupName,
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}
	if err := srv.deps.Channels.Create(ctx, ch); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	srv.ProbeAllChannels(ctx, healthProbeConfigFrom(config.HealthConfig{Enabled: true}))

	reloaded, err := srv.deps.Channels.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("读取渠道失败: %v", err)
	}
	if reloaded.LastTestOK {
		t.Error("上游返回 401，巡检结果不应为可用")
	}
	if reloaded.LastTestCode != http.StatusUnauthorized {
		t.Errorf("last_test_code = %d，期望 401 —— 管理员据此才知道该去换密钥而不是删模型", reloaded.LastTestCode)
	}
	if reloaded.LastTestAt.IsZero() {
		t.Error("失败也必须更新时间：否则后台会显示一个不知何時的旧结论")
	}
}
