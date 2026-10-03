// 本文件覆盖「relay 路由层」本批次的三项能力的单元测试：
//   - C1 渠道专用价：渠道专用价优先、无专用价回退分组默认价；
//   - C4 渠道级余额熔断：整条渠道凭据余额耗尽时被跳过并改走其他渠道；
//   - B9 路由可观测响应头：X-Routed-Via / X-Fallback-Attempts / X-Upstream。
//
// 意图（Why）：这三项都属于"配了/接了却没生效"极难靠人工点测发现的能力，
// 必须用自动化用例固化其行为分支（尤其"回退"与"跳过"这类负向分支）。
//
// 流转（Flow）：
//
//	go test ./internal/relay/
//	  ├─ C1：内存计价仓储 + Billing 取价分支断言
//	  ├─ C4：真实密钥池仓储（临时 SQLite）+ 假上游，断言改走备用渠道
//	  └─ B9：httptest 端到端，断言响应头
package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// ---- C1：渠道专用价取值优先级 ----

// TestBilling_QuoteForChannel_专用价优先与回退 覆盖 C1 的两个分支：
//   - 命中渠道专用价 → 用专用价；
//   - 无专用价（渠道不匹配）→ 回退分组默认价。
func TestBilling_QuoteForChannel_专用价优先与回退(t *testing.T) {
	ctx := context.Background()

	repo := &fakePriceRepo{prices: []*model.ModelPrice{
		{ID: 1, Model: "gpt-4o", PromptPrice: 1000, Group: "default", Enabled: true},               // 分组默认价
		{ID: 2, Model: "gpt-4o", PromptPrice: 2000, Group: "default", Enabled: true, ChannelID: 7}, // 渠道 7 专用价
		{ID: 3, Model: "gpt-4o", PromptPrice: 5000, Group: "default", Enabled: true, ChannelID: 9}, // 渠道 9 专用价
	}}
	billing := NewBilling(repo, newFakeGroupRepo(100), nil, nil, "default")

	// 1) 命中渠道 7 的专用价 → 用 2000（而不是默认价 1000）
	if got := billing.QuoteForChannel(ctx, "default", "gpt-4o", 1_000_000, 0, 0, 7); got != 2000 {
		t.Fatalf("渠道 7 应命中专用价 2000，实际 %d", got)
	}
	// 2) 渠道 8 无专用价 → 回退分组默认价 1000
	if got := billing.QuoteForChannel(ctx, "default", "gpt-4o", 1_000_000, 0, 0, 8); got != 1000 {
		t.Fatalf("渠道 8 无专用价应回退默认价 1000，实际 %d", got)
	}
	// 3) 渠道 9 的专用价不得泄漏给渠道 7（缓存按分组+渠道隔离）
	if got := billing.QuoteForChannel(ctx, "default", "gpt-4o", 1_000_000, 0, 0, 9); got != 5000 {
		t.Fatalf("渠道 9 应命中自身专用价 5000，实际 %d", got)
	}
	// 4) 不带渠道上下文（channelID=0）时等价于旧行为：只用默认价
	if got := billing.Quote(ctx, "default", "gpt-4o", 1_000_000, 0, 0); got != 1000 {
		t.Fatalf("未指定渠道应使用默认价 1000，实际 %d", got)
	}
}

// TestModelPriceRepo_渠道专用价_落库与列表 覆盖 C1 的存储层：
// 同一分组同一模型可同时存在"默认价"与"渠道专用价"（唯一索引已含 channel_id），
// 且 List 只返回默认价、ListForPricing 返回默认价 + 本渠道专用价。
func TestModelPriceRepo_渠道专用价_落库与列表(t *testing.T) {
	ctx := context.Background()

	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "price_channel.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	repo := store.NewModelPriceRepository(st.DB())
	if err := repo.Create(ctx, &model.ModelPrice{
		Model: "gpt-4o", Group: "default", PromptPrice: 1000, Enabled: true,
	}); err != nil {
		t.Fatalf("写入默认价失败: %v", err)
	}
	// 同分组同模型的渠道专用价：旧唯一索引会冲突，新索引允许共存。
	if err := repo.Create(ctx, &model.ModelPrice{
		Model: "gpt-4o", Group: "default", PromptPrice: 2000, Enabled: true, ChannelID: 7,
	}); err != nil {
		t.Fatalf("写入渠道专用价失败（唯一索引可能未包含 channel_id）: %v", err)
	}

	defaults, err := repo.List(ctx, "default", true)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(defaults) != 1 || defaults[0].ChannelID != model.ChannelScopeAll {
		t.Fatalf("List 应只返回默认价（1 条, channel_id=0），实际 %d 条", len(defaults))
	}

	scoped, err := repo.ListForPricing(ctx, "default", 7, true)
	if err != nil {
		t.Fatalf("ListForPricing 失败: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("ListForPricing 应返回默认价 + 渠道 7 专用价（2 条），实际 %d 条", len(scoped))
	}
	if got := model.MatchModelPriceForChannel(scoped, "gpt-4o", 7); got == nil || got.PromptPrice != 2000 {
		t.Fatalf("渠道 7 应匹配到专用价 2000，实际 %+v", got)
	}
}

// ---- C4：渠道级余额熔断 ----

// createChannelWithName 写入一个启用渠道并返回它（名称可定制，便于断言路由结果）。
func createChannelWithName(t *testing.T, repo model.ChannelRepository, name, baseURL, apiKey string, models []string, priority int) *model.Channel {
	t.Helper()
	ch := &model.Channel{
		Name: name, Type: 1, BaseURL: baseURL, APIKey: apiKey,
		Models: models, Group: "default", Priority: priority, Weight: 1,
		Status: model.ChannelStatusEnabled,
	}
	if err := repo.Create(context.Background(), ch); err != nil {
		t.Fatalf("写入渠道 %q 失败: %v", name, err)
	}
	return ch
}

// TestRouting_渠道凭据余额全部耗尽_应跳过并改走备用渠道 覆盖 C4：
// 高优先级渠道的凭据余额全部耗尽 → 被主动跳过，请求成功落到低优先级渠道。
func TestRouting_渠道凭据余额全部耗尽_应跳过并改走备用渠道(t *testing.T) {
	ctx := context.Background()
	channels, keys := newTestRepos(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	// 高优先级渠道：唯一凭据余额为 0（已耗尽）→ 应被跳过。
	exhausted := createChannelWithName(t, channels, "余额耗尽渠道", upstream.URL, "sk-exhausted", []string{"m"}, 20)
	if _, _, err := keys.ReplaceAllWithBalance(ctx, exhausted.ID, []string{"sk-exhausted"}, nil, []int64{0}); err != nil {
		t.Fatalf("写入耗尽凭据失败: %v", err)
	}
	// 低优先级渠道：凭据余额充足 → 应承接请求。
	healthy := createChannelWithName(t, channels, "健康渠道", upstream.URL, "sk-healthy", []string{"m"}, 10)
	if _, _, err := keys.ReplaceAllWithBalance(ctx, healthy.ID, []string{"sk-healthy"}, nil, []int64{100}); err != nil {
		t.Fatalf("写入健康凭据失败: %v", err)
	}

	rl := New(channels, Options{Keys: keys})

	// 渠道级判定：耗尽渠道应被识别为"凭据余额全部耗尽"。
	if !rl.channelKeysAllBalanceExhausted(ctx, exhausted.ID) {
		t.Fatal("余额全为 0 的渠道应被判为凭据余额全部耗尽")
	}
	if rl.channelKeysAllBalanceExhausted(ctx, healthy.ID) {
		t.Fatal("存在余额充足凭据的渠道不应被判为耗尽")
	}

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeChatCompletions))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（应改走健康渠道）", resp.StatusCode)
	}
	via := resp.Header.Get(headerRoutedVia)
	if !strings.Contains(via, "健康渠道") {
		t.Fatalf("应路由到健康渠道，实际 X-Routed-Via = %q", via)
	}
}

// ---- B9：路由可观测响应头 ----

// TestServeChatCompletions_路由可观测响应头_非流式 覆盖 B9 的普通 JSON 响应。
func TestServeChatCompletions_路由可观测响应头_非流式(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-x", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeChatCompletions))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	assertRoutingHeaders(t, resp, ch, "gpt-4o", "1")
}

// TestServeChatCompletions_路由可观测响应头_流式 覆盖 B9 的 SSE 流式响应：
// 响应头必须在写状态行之前注入，流式下同样可见。
func TestServeChatCompletions_路由可观测响应头_流式(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-x", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeChatCompletions))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	assertRoutingHeaders(t, resp, ch, "gpt-4o", "1")
}

// assertRoutingHeaders 断言 B9 的三个响应头取值。
func assertRoutingHeaders(t *testing.T, resp *http.Response, ch *model.Channel, upstreamModel, attempts string) {
	t.Helper()
	via := resp.Header.Get(headerRoutedVia)
	if !strings.Contains(via, ch.Name) || !strings.Contains(via, "#") {
		t.Errorf("X-Routed-Via = %q，应含渠道名 %q 与 #ID", via, ch.Name)
	}
	if got := resp.Header.Get(headerFallbackAttempts); got != attempts {
		t.Errorf("X-Fallback-Attempts = %q，期望 %q", got, attempts)
	}
	if got := resp.Header.Get(headerUpstreamModel); got != upstreamModel {
		t.Errorf("X-Upstream = %q，期望 %q", got, upstreamModel)
	}
}
