// 本文件测试「模型测速」的转发侧实现（ProbeChannelLatency）。
//
// 覆盖四条路径：
//  1. 成功：SSE 首分片到达 → TTFB 被记录、请求体带 max_tokens=1 与 stream=true；
//  2. 非 2xx：状态码与上游错误片段被带回，TTFB 为 0；
//  3. 网络错误：Err 非 nil，TTFB 为 0；
//  4. 空 SSE 流：2xx 但没有任何 data 分片 → 视为失败（TTFB=0）并说明原因。
//
// 流转（Flow）：
//
//	httptest 模拟上游 → newRelay(repo) → ProbeChannelLatency → 断言结果
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestProbeChannelLatency_Success 验证成功路径：TTFB 被记录、请求体最小化、
// 并且拿到首分片后客户端立即断开（不再读后续分片）。
func TestProbeChannelLatency_Success(t *testing.T) {
	var (
		gotBody    map[string]any
		gotAuth    string
		wroteExtra atomic.Bool // 服务端是否在首分片后又写了内容
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if raw, err := readAllForProbe(r); err == nil {
			_ = json.Unmarshal(raw, &gotBody)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 首分片立即写出（触发 TTFB），随后再慢慢补充分片——
		// 客户端应在首分片后断开，这些补充内容只会被部分写出。
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n")
		wroteExtra.Store(true)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-upstream", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	result := rl.ProbeChannelLatency(context.Background(), ch, ch.APIKey, "gpt-4o")

	if result.Err != nil {
		t.Fatalf("测速不应报错: %v", result.Err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", result.StatusCode)
	}
	if result.TTFBMS <= 0 {
		t.Fatalf("TTFB 应被记录为正数，实际 %d", result.TTFBMS)
	}
	if result.TotalMS <= 0 {
		t.Fatalf("总耗时应为正数，实际 %d", result.TotalMS)
	}
	// 拿到首分片即断开：不应等 300ms 后的补充内容，总耗时必须远小于它。
	if result.TotalMS >= 300 {
		t.Errorf("总耗时 %dms ≥ 300ms：说明没有在首分片后立即断开", result.TotalMS)
	}

	// 请求体必须是最小 token 消耗形态。
	if gotBody["max_tokens"] != float64(1) {
		t.Errorf("max_tokens = %v，期望 1（输出必须截断在 1 token）", gotBody["max_tokens"])
	}
	if gotBody["stream"] != true {
		t.Errorf("stream = %v，期望 true（必须流式才能测首字延迟）", gotBody["stream"])
	}
	messages, _ := gotBody["messages"].([]any)
	if len(messages) != 1 {
		t.Errorf("messages 长度 = %d，期望 1（最小提示词）", len(messages))
	}
	if gotAuth != "Bearer sk-upstream" {
		t.Errorf("上游收到的 Authorization = %q，期望渠道密钥", gotAuth)
	}
	// 服务端补充内容不应被读到（客户端已断开）；此断言宽松处理——
	// 写补充内容时连接可能已断，wroteExtra 只证明服务端尝试过写。
	if wroteExtra.Load() {
		t.Log("服务端在断开后尝试写过补充内容（预期行为：客户端不再读取）")
	}
}

// TestProbeChannelLatency_UpstreamError 验证非 2xx：TTFB 为 0，错误片段被带回。
func TestProbeChannelLatency_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-bad", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	result := rl.ProbeChannelLatency(context.Background(), ch, ch.APIKey, "gpt-4o")
	if result.Err != nil {
		t.Fatalf("非 2xx 不应产生网络错误: %v", result.Err)
	}
	if result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", result.StatusCode)
	}
	if result.TTFBMS != 0 {
		t.Errorf("失败时 TTFB 应为 0，实际 %d", result.TTFBMS)
	}
	if !strings.Contains(result.Body, "invalid api key") {
		t.Errorf("上游错误片段应被带回，实际 %q", result.Body)
	}
}

// TestProbeChannelLatency_NetworkError 验证连接失败：Err 非 nil 且 TTFB 为 0。
func TestProbeChannelLatency_NetworkError(t *testing.T) {
	// 先起一个服务拿 URL，再关掉它——得到一个必然连接失败的地址。
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := upstream.URL
	upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, url, "sk-any", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	result := rl.ProbeChannelLatency(context.Background(), ch, ch.APIKey, "gpt-4o")
	if result.Err == nil {
		t.Fatal("连接失败时 Err 应非 nil")
	}
	if result.StatusCode != 0 {
		t.Errorf("无响应时状态码应为 0，实际 %d", result.StatusCode)
	}
	if result.TTFBMS != 0 {
		t.Errorf("无响应时 TTFB 应为 0，实际 %d", result.TTFBMS)
	}
}

// TestProbeChannelLatency_EmptyStream 验证 2xx 但无任何 data 分片的异常上游：
// 视为失败（TTFB=0）并给出可读原因，而不是误报成功。
func TestProbeChannelLatency_EmptyStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 只有空行与注释，没有任何 data 分片。
		_, _ = w.Write([]byte("\n: keep-alive\n\n"))
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-any", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	result := rl.ProbeChannelLatency(context.Background(), ch, ch.APIKey, "gpt-4o")
	if result.Err != nil {
		t.Fatalf("不应报网络错误: %v", result.Err)
	}
	if result.TTFBMS != 0 {
		t.Errorf("空流的 TTFB 应为 0，实际 %d", result.TTFBMS)
	}
	if !strings.Contains(result.Body, "空流") {
		t.Errorf("应说明空流原因，实际 %q", result.Body)
	}
}

// TestProbeChannelLatency_NonSSEBody 验证防御分支：上游声明了流式
// 却直接回了普通 JSON——首个非空行按"首字"记录 TTFB，而不是误报空流。
func TestProbeChannelLatency_NonSSEBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	ch := addChannel(t, repo, upstream.URL, "sk-any", []string{"gpt-4o"}, 10)
	rl := newRelay(repo)

	result := rl.ProbeChannelLatency(context.Background(), ch, ch.APIKey, "gpt-4o")
	if result.Err != nil {
		t.Fatalf("不应报网络错误: %v", result.Err)
	}
	if result.TTFBMS <= 0 {
		t.Fatalf("非流式响应也应记录 TTFB（首字节即首字），实际 %d", result.TTFBMS)
	}
	if !strings.Contains(result.Body, "chatcmpl-1") {
		t.Errorf("应附上响应片段，实际 %q", result.Body)
	}
}

// readAllForProbe 读取测速请求的请求体（测试助手）。
func readAllForProbe(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	return io.ReadAll(r.Body)
}

// 编译期断言：确保本测试文件引用的类型没有被意外破坏。
var _ = model.ChannelStatusEnabled
