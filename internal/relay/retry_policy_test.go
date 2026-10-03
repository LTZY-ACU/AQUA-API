// retry_policy_test.go 校验「渠道/模型级重试开关」在转发链路上的真实效果。
//
// 意图（Why）：
//
//	这套开关的价值全在"实际向上游打了几次"上，而单元测试里最容易漏掉的正是这一点——
//	策略解析对了、但转发层没真正采纳，代码看起来仍然是对的。
//	因此这里一律用 httptest 上游【数请求次数】来断言，而不是断言内部变量。
//
// 流转（Flow）：
//
//	后台配置（channels.retry_* / model_retry_rules）
//	  → Channel.RetryPolicyFor → forwardWithFallback 的尝试预算 → 上游实际收到的请求数
//
// 扩展（Extend）：
//
//	新增"按分组/按令牌"的重试层级时，在 TestRetryPolicy_* 之后补一组同名结构的用例：
//	建渠道 → 配策略 → 数上游请求次数。
package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// countingStatusUpstream 启动一个"总是返回指定状态码"的上游，并统计被请求次数。
func countingStatusUpstream(t *testing.T, status int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream boom","type":"server_error"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestRetryPolicy_关闭重试时不换渠道 验证渠道级总开关的"只尝试一次"语义。
//
// 场景：优先级更高的渠道持续 500，另有一个健康渠道可切换。
// 期望：因为首渠道关闭了重试，只打 1 次上游，且下游收到本站脱敏错误（502）。
func TestRetryPolicy_关闭重试时不换渠道(t *testing.T) {
	channels, _ := newTestRepos(t)
	ctx := context.Background()

	failing, failHits := countingStatusUpstream(t, http.StatusInternalServerError)
	healthy, healthyHits := countingStatusUpstream(t, http.StatusOK)

	primary := addChannel(t, channels, failing.URL, "sk-1", []string{"test-model"}, 100)
	addChannel(t, channels, healthy.URL, "sk-2", []string{"test-model"}, 10)

	// 关闭重试：期望"一次就够"
	primary.RetryMode = model.RetryModeOff
	if err := channels.Update(ctx, primary); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{}))
	resp := postChat(t, gateway.URL, `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望本站 503（上游 5xx 的语义化归码），实际 %d：%s", resp.StatusCode, body)
	}
	if got := atomic.LoadInt32(failHits); got != 1 {
		t.Fatalf("关闭重试后应只请求首渠道 1 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(healthyHits); got != 0 {
		t.Fatalf("关闭重试后不应切换到健康渠道，实际被请求 %d 次", got)
	}
}

// TestRetryPolicy_模型级关闭覆盖渠道级 验证模型级规则真的改变了行为。
func TestRetryPolicy_模型级关闭覆盖渠道级(t *testing.T) {
	channels, _ := newTestRepos(t)
	ctx := context.Background()

	failing, failHits := countingStatusUpstream(t, http.StatusInternalServerError)
	healthy, healthyHits := countingStatusUpstream(t, http.StatusOK)

	primary := addChannel(t, channels, failing.URL, "sk-1", []string{"test-model"}, 100)
	addChannel(t, channels, healthy.URL, "sk-2", []string{"test-model"}, 10)

	// 渠道级保持默认（开启），仅对该模型关闭
	primary.ModelRetryRules = []model.ModelRetryRule{{Model: "test-model", Enabled: false}}
	if err := channels.Update(ctx, primary); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{}))
	resp := postChat(t, gateway.URL, `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望本站 503（上游 5xx 的语义化归码），实际 %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(failHits); got != 1 {
		t.Fatalf("模型级关闭后应只请求 1 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(healthyHits); got != 0 {
		t.Fatalf("模型级关闭后不应切换渠道，实际被请求 %d 次", got)
	}
}

// TestRetryPolicy_按配置次数重试 验证次数真的生效（不是只认开关）。
func TestRetryPolicy_按配置次数重试(t *testing.T) {
	channels, _ := newTestRepos(t)
	ctx := context.Background()

	// 三个渠道全部 500：预算为几次，上游就该收到几次
	first, firstHits := countingStatusUpstream(t, http.StatusInternalServerError)
	second, secondHits := countingStatusUpstream(t, http.StatusInternalServerError)
	third, thirdHits := countingStatusUpstream(t, http.StatusInternalServerError)

	ch1 := addChannel(t, channels, first.URL, "sk-1", []string{"test-model"}, 30)
	addChannel(t, channels, second.URL, "sk-2", []string{"test-model"}, 20)
	addChannel(t, channels, third.URL, "sk-3", []string{"test-model"}, 10)

	// 只给首渠道设置"重试 2 次"（含首次共 2 次尝试）
	ch1.RetryMode = model.RetryModeOn
	ch1.RetryMaxAttempts = 2
	if err := channels.Update(ctx, ch1); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{}))
	resp := postChat(t, gateway.URL, `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望本站 503（上游 5xx 的语义化归码），实际 %d", resp.StatusCode)
	}
	total := atomic.LoadInt32(firstHits) + atomic.LoadInt32(secondHits) + atomic.LoadInt32(thirdHits)
	if total != 2 {
		t.Fatalf("重试次数设为 2 时上游应只收到 2 次请求，实际 %d 次", total)
	}
}

// TestRetryPolicy_关闭重试时不换密钥 验证总开关对密钥级重试同样有效。
//
// 场景：单渠道挂两把密钥，上游一律 401。
// 期望：只打 1 次（不换第二把钥匙），并回本站脱敏错误。
func TestRetryPolicy_关闭重试时不换密钥(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	upstream, hits := countingStatusUpstream(t, http.StatusUnauthorized)

	ch := addChannel(t, channels, upstream.URL, "", []string{"test-model"}, 10)
	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"nvapi-bad1", "nvapi-bad2"}, nil); err != nil {
		t.Fatalf("导入密钥池失败: %v", err)
	}

	ch.RetryMode = model.RetryModeOff
	if err := channels.Update(ctx, ch); err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{Keys: keys}))
	resp := postChat(t, gateway.URL, `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("期望本站 502（上游 401 的语义化归码），实际 %d：%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "upstream_request_failed") {
		t.Fatalf("应采用本站错误码，实际：%s", body)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("关闭重试后不应换第二把密钥，上游应只被请求 1 次，实际 %d 次", got)
	}
}
