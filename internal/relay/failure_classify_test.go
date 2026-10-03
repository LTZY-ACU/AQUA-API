// 失败分类驱动重试 + (凭据×模型) 冷却 + retry-after 的测试。
//
// 意图（Why）：
//
//	这三项能力都直接决定"重试率 r"与"故障半径"，任何一处判定错误都会在线上被放大：
//	  · 429 若误换渠道 → 限流被扩散到其他上游；
//	  · 内容审核若误重试 → 白烧上游额度；
//	  · 冷却若仍是"整把" → 模型 A 的失败拖垮同渠道的模型 B/C；
//	  · retry-after 若解析失败就不冷却 → 被限流的凭据被反复推向上游。
//	因此这里把每类判据与方向固化成测试。
//
// 流转（Flow）：
//
//	go test ./internal/relay/
//	  ├─ 单元：classifyUpstreamFailure / retryAfterCooldown / isEmptyCompletion / 冷却表
//	  └─ 集成：httptest 假上游按状态码与模型返回，断言"是否重试、往哪个方向重试"
//
// 扩展（Extend）：
//
//	新增失败语义或冷却维度时，仿照本文件的"场景_预期"命名补充用例。
package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestClassifyUpstreamFailure_各类失败判据 覆盖语义分类表。
func TestClassifyUpstreamFailure_各类失败判据(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   upstreamFailureClass
	}{
		{name: "429判为限流", status: http.StatusTooManyRequests, want: failureClassRateLimited},
		{name: "401判为鉴权失败", status: http.StatusUnauthorized, want: failureClassCredential},
		{name: "403判为鉴权失败", status: http.StatusForbidden, want: failureClassCredential},
		{name: "402判为欠费", status: http.StatusPaymentRequired, want: failureClassCredential},
		{name: "500判为渠道故障", status: http.StatusInternalServerError, want: failureClassChannel},
		{name: "503判为渠道故障", status: http.StatusServiceUnavailable, want: failureClassChannel},
		{name: "529过载判为渠道故障", status: 529, want: failureClassChannel},
		{name: "审核拦截优先于状态码", status: http.StatusBadRequest,
			body: `{"error":{"message":"content_policy_violation"}}`, want: failureClassContentFilter},
		{name: "审核拦截403", status: http.StatusForbidden,
			body: `{"error":{"message":"blocked by content filter"}}`, want: failureClassContentFilter},
		{name: "普通400无关", status: http.StatusBadRequest,
			body: `{"error":{"message":"bad request"}}`, want: failureClassNone},
		{name: "200无关", status: http.StatusOK, want: failureClassNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status}
			got, _ := classifyUpstreamFailure(resp, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("分类 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestRetryAfterCooldown_解析秒数与HTTP日期 覆盖两种规范格式。
func TestRetryAfterCooldown_解析秒数与HTTP日期(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	h := http.Header{}
	h.Set("Retry-After", "120")
	if got := retryAfterCooldown(h, now); got != 120*time.Second {
		t.Fatalf("秒数格式应解析为 120s，实际 %v", got)
	}

	h.Set("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
	if got := retryAfterCooldown(h, now); got != 90*time.Second {
		t.Fatalf("HTTP 日期格式应解析为 90s，实际 %v", got)
	}
}

// TestRetryAfterCooldown_非法值回退不冷却 保证"解析失败不会导致不冷却"这条红线。
//
// 返回值 0 的语义是"不采信该提示"，调用方据此回退到默认退避——仍然会冷却。
func TestRetryAfterCooldown_非法值回退不冷却(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		raw  string
	}{
		{name: "缺失", raw: ""},
		{name: "非数字", raw: "soon"},
		{name: "负数", raw: "-5"},
		{name: "零", raw: "0"},
		{name: "过去的日期", raw: now.Add(-time.Hour).Format(http.TimeFormat)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.raw != "" {
				h.Set("Retry-After", tc.raw)
			}
			if got := retryAfterCooldown(h, now); got != 0 {
				t.Fatalf("非法值应返回 0（回退默认退避），实际 %v", got)
			}
		})
	}

	// 超大值夹到上限，而不是无限冷冻凭据
	h := http.Header{}
	h.Set("Retry-After", "999999999")
	if got := retryAfterCooldown(h, now); got != retryAfterMax {
		t.Fatalf("超大 retry-after 应夹到 %v，实际 %v", retryAfterMax, got)
	}
}

// TestIsEmptyCompletion_判据 覆盖"200 但空内容"的识别。
func TestIsEmptyCompletion_判据(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "choices为空数组", body: `{"id":"x","choices":[]}`, want: true},
		{name: "content为空", body: `{"choices":[{"message":{"content":""}}]}`, want: true},
		{name: "有内容", body: `{"choices":[{"message":{"content":"你好"}}]}`, want: false},
		{name: "空内容但有工具调用", body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"a"}]}}]}`, want: false},
		{name: "非本协议形态", body: `{"object":"list","data":[{"embedding":[0.1]}]}`, want: false},
		{name: "非法JSON", body: `not-json`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}
			if got := isEmptyCompletion(resp); got != tc.want {
				t.Fatalf("判定 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestCredentialModelCooldownBook_只影响该组合 覆盖冷却表的登记 / 查询 / 过期。
func TestCredentialModelCooldownBook_只影响该组合(t *testing.T) {
	now := time.Now()
	book := newCredentialModelCooldownBook()
	book.now = func() time.Time { return now }

	book.mark(1, "model-a", now.Add(10*time.Minute))

	if !book.cooling(1, "model-a") {
		t.Fatal("(1, model-a) 应处于冷却中")
	}
	if book.cooling(1, "model-b") {
		t.Fatal("同一凭据的 model-b 不应受 model-a 冷却影响")
	}
	if !book.hasLiveEntry(1) {
		t.Fatal("凭据 1 应存在有效的模型级冷却记录")
	}
	if book.hasLiveEntry(2) {
		t.Fatal("凭据 2 不应有冷却记录")
	}

	// 过期即自动失效，无需任何清理动作
	now = now.Add(11 * time.Minute)
	if book.cooling(1, "model-a") {
		t.Fatal("冷却过期后应自动恢复")
	}
	if book.hasLiveEntry(1) {
		t.Fatal("冷却过期后不应再有有效记录")
	}
	if book.len() != 0 {
		t.Fatalf("过期条目应被清理，实际剩余 %d 条", book.len())
	}
}

// TestFilterUsableKeysForRequest_模型级冷却 验证冷却表在选凭据时被真正查询。
func TestFilterUsableKeysForRequest_模型级冷却(t *testing.T) {
	now := time.Now()
	book := newCredentialModelCooldownBook()
	book.now = func() time.Time { return now }

	keys := []*model.ChannelKey{
		{ID: 1, Status: model.ChannelKeyStatusEnabled, Balance: model.BalanceUnknown},                                           // 无冷却
		{ID: 2, Status: model.ChannelKeyStatusEnabled, Balance: model.BalanceUnknown, CooldownUntil: now.Add(10 * time.Minute)}, // 整把冷却
		{ID: 3, Status: model.ChannelKeyStatusEnabled, Balance: model.BalanceUnknown, CooldownUntil: now.Add(10 * time.Minute)}, // 整把冷却 + 模型级记录
	}
	// 凭据 1 在 model-a 上失败；凭据 3 在 model-a 上失败（同时也有整把冷却）
	book.mark(1, "model-a", now.Add(10*time.Minute))
	book.mark(3, "model-a", now.Add(10*time.Minute))

	// 对 model-a：三者都应被排除（1/3 命中模型级冷却，2 命中整把冷却）
	if got := filterUsableKeysForRequest(keys, now, credentialScope{Model: "model-a"}, book); len(got) != 0 {
		t.Fatalf("model-a 上不应有可用凭据，实际 %d 把", len(got))
	}

	// 对 model-b：1 可用；2 整把冷却且无模型级记录 → 排除；
	// 3 整把冷却但有模型级记录（说明失败是模型维度的）→ 其他模型可用。
	got := filterUsableKeysForRequest(keys, now, credentialScope{Model: "model-b"}, book)
	ids := map[uint64]bool{}
	for _, k := range got {
		ids[k.ID] = true
	}
	if !ids[1] {
		t.Fatal("凭据 1 在 model-b 上应可用")
	}
	if ids[2] {
		t.Fatal("凭据 2 处于整把冷却且无模型级记录，model-b 上应被排除")
	}
	if !ids[3] {
		t.Fatal("凭据 3 的失败是模型维度（model-a），model-b 上应仍可用")
	}

	// 无模型上下文：沿用整把冷却语义（凭据 2/3 排除，凭据 1 可用）
	got = filterUsableKeysForRequest(keys, now, credentialScope{}, book)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("无模型上下文时应只剩凭据 1，实际 %v", got)
	}
}

// okCompletion 是一段带内容的合法成功响应。
const okCompletion = `{"id":"chatcmpl-ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`

// statusUpstream 启动一个"固定返回某状态码与响应体"的假上游，并记录调用次数。
func statusUpstream(t *testing.T, status int, body string, header map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// modelAwareUpstream 启动一个"按请求体中的模型名返回不同结果"的假上游，并记录调用次数。
//
// modelA 命中时返回 429（模拟"该模型在此凭据上被限流"），其余返回 200 内容。
func modelAwareUpstream(t *testing.T, modelA string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), modelA) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okCompletion)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestForward_429限流_只冷却不换渠道 是「不换渠道」的核心用例。
//
// 场景：高优先级渠道持续 429，低优先级渠道完全可用。
// 期望：客户端拿到 429（本站语义），且低优先级渠道【没有被调用】——
// 因为换渠道对限流无意义，只会把限流扩散到其他上游。
func TestForward_429限流_只冷却不换渠道(t *testing.T) {
	limited, limitedCalls := statusUpstream(t, http.StatusTooManyRequests,
		`{"error":{"message":"rate limit exceeded"}}`, nil)
	healthy, healthyCalls := statusUpstream(t, http.StatusOK, okCompletion, nil)

	repo := newTestRepo(t)
	addChannel(t, repo, limited.URL, "sk-limited", nil, 100)
	addChannel(t, repo, healthy.URL, "sk-healthy", nil, 10)

	gateway := newGateway(t, newRelay(repo))
	resp := postChat(t, gateway.URL, `{"model":"cl-429-model"}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("429 应回本站限流语义（429），实际 %d：%s", resp.StatusCode, body)
	}
	if got := atomic.LoadInt32(healthyCalls); got != 0 {
		t.Fatalf("429 不应换渠道，但低优先级渠道被调用了 %d 次", got)
	}
	if got := atomic.LoadInt32(limitedCalls); got == 0 {
		t.Fatal("高优先级渠道应至少被调用一次")
	}
}

// TestForward_5xx渠道故障_换渠道 保留"5xx 属于渠道级故障，换渠道"的行为。
func TestForward_5xx渠道故障_换渠道(t *testing.T) {
	broken, _ := statusUpstream(t, http.StatusServiceUnavailable,
		`{"error":{"message":"upstream down"}}`, nil)
	healthy, _ := statusUpstream(t, http.StatusOK, okCompletion, nil)

	repo := newTestRepo(t)
	addChannel(t, repo, broken.URL, "sk-broken", nil, 100)
	addChannel(t, repo, healthy.URL, "sk-healthy", nil, 10)

	gateway := newGateway(t, newRelay(repo))
	resp := postChat(t, gateway.URL, `{"model":"cl-5xx-model"}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("5xx 应换渠道并最终成功，实际 %d：%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "chatcmpl-ok") {
		t.Fatalf("应返回健康渠道的内容，实际：%s", body)
	}
}

// TestForward_内容审核拦截_不重试不换渠道 验证审核拦截既不重试也不换渠道。
func TestForward_内容审核拦截_不重试不换渠道(t *testing.T) {
	filtered, filteredCalls := statusUpstream(t, http.StatusBadRequest,
		`{"error":{"message":"blocked by content filter","code":"content_filter"}}`, nil)
	healthy, healthyCalls := statusUpstream(t, http.StatusOK, okCompletion, nil)

	repo := newTestRepo(t)
	addChannel(t, repo, filtered.URL, "sk-filtered", nil, 100)
	addChannel(t, repo, healthy.URL, "sk-healthy", nil, 10)

	gateway := newGateway(t, newRelay(repo))
	resp := postChat(t, gateway.URL, `{"model":"cl-filter-model"}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 400 脱敏为本站 502 / upstream_request_failed
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("审核拦截应脱敏为 502，实际 %d：%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "upstream_request_failed") {
		t.Fatalf("应采用本站错误码 upstream_request_failed，实际：%s", body)
	}
	if strings.Contains(string(body), "content filter") {
		t.Fatalf("响应体绝不能出现上游原文，实际：%s", body)
	}
	if got := atomic.LoadInt32(filteredCalls); got != 1 {
		t.Fatalf("审核拦截不应重试，期望 1 次上游调用，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(healthyCalls); got != 0 {
		t.Fatalf("审核拦截不应换渠道，但另一渠道被调用了 %d 次", got)
	}
}

// TestForward_空200_触发渠道级重试 验证"200 但空内容"走渠道级重试。
func TestForward_空200_触发渠道级重试(t *testing.T) {
	empty, _ := statusUpstream(t, http.StatusOK, `{"id":"chatcmpl-empty","choices":[]}`, nil)
	healthy, _ := statusUpstream(t, http.StatusOK, okCompletion, nil)

	repo := newTestRepo(t)
	addChannel(t, repo, empty.URL, "sk-empty", nil, 100)
	addChannel(t, repo, healthy.URL, "sk-healthy", nil, 10)

	gateway := newGateway(t, newRelay(repo))
	resp := postChat(t, gateway.URL, `{"model":"cl-empty-model"}`)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("空 200 应降级重试到健康渠道并成功，实际 %d：%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "chatcmpl-ok") {
		t.Fatalf("应返回健康渠道的内容（而非空响应），实际：%s", body)
	}
}

// TestForward_渠道模型冷却_同渠道其他模型仍可用 是「渠道×模型冷却」的集成验证。
//
// 场景：单渠道 + 单凭据；模型 cl-model-a 被上游 429，模型 cl-model-b 正常。
// 期望：请求 cl-model-a 失败后，请求 cl-model-b 仍能用【同一把凭据】成功——
// 若冷却还是"整把"语义，cl-model-b 会被一并冷却而失败。
func TestForward_渠道模型冷却_同渠道其他模型仍可用(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	upstream, _ := modelAwareUpstream(t, "cl-model-a")

	ch := addChannel(t, channels, upstream.URL, "", []string{"cl-model-a", "cl-model-b"}, 10)
	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"cl-key-1"}, nil); err != nil {
		t.Fatalf("导入凭据失败: %v", err)
	}

	r := New(channels, Options{Keys: keys})
	gateway := newGateway(t, r)

	// 模型 A：429（该凭据在这个模型上被限流）
	respA := postChat(t, gateway.URL, `{"model":"cl-model-a","messages":[{"role":"user","content":"hi"}]}`)
	bodyA, _ := io.ReadAll(respA.Body)
	_ = respA.Body.Close()
	if respA.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("cl-model-a 应返回 429，实际 %d：%s", respA.StatusCode, bodyA)
	}

	// 模型 B：同一把凭据，仍应可用
	respB := postChat(t, gateway.URL, `{"model":"cl-model-b","messages":[{"role":"user","content":"hi"}]}`)
	bodyB, _ := io.ReadAll(respB.Body)
	_ = respB.Body.Close()
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("cl-model-b 应与模型 A 的失败隔离、仍可用，实际 %d：%s", respB.StatusCode, bodyB)
	}
	if !strings.Contains(string(bodyB), "chatcmpl-ok") {
		t.Fatalf("cl-model-b 应返回上游内容，实际：%s", bodyB)
	}
}

// TestForward_429带retryAfter_冷却时长尊重上游 验证 retry-after 端到端生效。
func TestForward_429带retryAfter_冷却时长尊重上游(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	upstream, _ := statusUpstream(t, http.StatusTooManyRequests,
		`{"error":{"message":"rate limit exceeded"}}`, map[string]string{"Retry-After": "120"})

	ch := addChannel(t, channels, upstream.URL, "", []string{"cl-retryafter-model"}, 10)
	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"cl-key-ra"}, nil); err != nil {
		t.Fatalf("导入凭据失败: %v", err)
	}

	r := New(channels, Options{Keys: keys})
	gateway := newGateway(t, r)

	resp := postChat(t, gateway.URL, `{"model":"cl-retryafter-model","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()

	pool, err := keys.ListByChannel(ctx, ch.ID)
	if err != nil {
		t.Fatalf("查询凭据池失败: %v", err)
	}
	if len(pool) != 1 {
		t.Fatalf("池内应有 1 把凭据，实际 %d", len(pool))
	}
	delta := time.Until(pool[0].CooldownUntil)
	// retry-after=120s：默认 429 退避只有 30s，若解析生效应接近 120s
	if delta < 110*time.Second || delta > 130*time.Second {
		t.Fatalf("冷却时长应约 120s（尊重 retry-after），实际 %v", delta)
	}
}
