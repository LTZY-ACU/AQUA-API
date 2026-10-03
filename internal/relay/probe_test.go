// 本文件覆盖「按渠道类型构造探测请求」这一修复点。
//
// 为什么必须有这个测试：测活此前硬编码 OpenAI 协议（固定拼 /v1/chat/completions、
// 固定用 Authorization: Bearer），因此 Azure / Anthropic / Gemini 等渠道测活必然失败，
// 管理员会去改本来就正确的配置。这里用真实上游桩断言"测活与转发走同一套组装"。
package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// TestProbeChannel_Anthropic类型走Messages协议 验证测活会按类型改写路径与鉴权头。
func TestProbeChannel_Anthropic类型走Messages协议(t *testing.T) {
	var (
		mu      sync.Mutex
		gotPath string
		gotKey  string
		gotAuth string
		gotBody string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotBody = string(buf[:n])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-x"}`))
	}))
	defer upstream.Close()

	r := New(nil, Options{})
	ch := &model.Channel{
		Name: "anthropic 渠道", Type: 1, TypeKey: "anthropic",
		BaseURL: upstream.URL, APIKey: "sk-anthropic", Models: []string{"claude-x"},
	}

	res := r.ProbeChannel(context.Background(), ch, "sk-anthropic", "claude-x")
	if res.Err != nil {
		t.Fatalf("探测失败: %v", res.Err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（体：%s）", res.StatusCode, res.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	// 走 Anthropic 的 Messages 协议，而不是 OpenAI 的 /v1/chat/completions
	if !strings.HasSuffix(gotPath, "/messages") {
		t.Errorf("上游路径 = %q，期望以 /messages 结尾（Anthropic 协议）", gotPath)
	}
	if strings.Contains(gotPath, "chat/completions") {
		t.Errorf("不应走 OpenAI 的 chat/completions，实际 %q", gotPath)
	}
	// 鉴权走 x-api-key（Anthropic 系），而不是 Authorization: Bearer
	if gotKey != "sk-anthropic" {
		t.Errorf("x-api-key = %q，期望 sk-anthropic", gotKey)
	}
	if gotAuth != "" {
		t.Errorf("Anthropic 渠道不应带 Authorization 头，实际 %q", gotAuth)
	}
	// 请求体必须已转成 Anthropic 格式（含 messages 与 max_tokens）
	if !strings.Contains(gotBody, "messages") || !strings.Contains(gotBody, "max_tokens") {
		t.Errorf("请求体未按 Anthropic 协议转换：%s", gotBody)
	}
}

// TestProbeChannel_OpenAI兼容渠道保持Bearer鉴权 验证默认路径未被改坏。
func TestProbeChannel_OpenAI兼容渠道保持Bearer鉴权(t *testing.T) {
	var (
		mu      sync.Mutex
		gotPath string
		gotAuth string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	r := New(nil, Options{})
	ch := &model.Channel{
		Name: "openai 渠道", Type: 1, TypeKey: "openai",
		BaseURL: upstream.URL, APIKey: "sk-openai", Models: []string{"gpt-4o"},
	}

	res := r.ProbeChannel(context.Background(), ch, "sk-openai", "gpt-4o")
	if res.Err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("探测失败: err=%v status=%d", res.Err, res.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Errorf("上游路径 = %q，期望 /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-openai" {
		t.Errorf("Authorization = %q，期望 Bearer sk-openai", gotAuth)
	}
}

// TestProbeChannel_先套用模型映射再探测 验证测活与真实转发的模型名口径一致。
//
// 这是线上实测暴露过的缺陷：测活漏掉模型映射时，会把「平台模型 ID」原样发给上游，
// 得到一个与真实调用无关的 404（带前缀的平台名 → 上游回 model_not_found），
// 而真实转发其实完全正常——管理员会因此去改本来正确的配置。
func TestProbeChannel_先套用模型映射再探测(t *testing.T) {
	var (
		mu       sync.Mutex
		gotBody  string
		gotPath  string
		gotModel string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 2048)
		n, _ := r.Body.Read(buf)
		var parsed map[string]any
		_ = json.Unmarshal(buf[:n], &parsed)
		modelName, _ := parsed["model"].(string)
		mu.Lock()
		gotBody = string(buf[:n])
		gotPath = r.URL.Path
		gotModel = modelName
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	// 渠道 ID=7 配了映射：平台名 vendor/model-a → 上游名 model-a
	mappings := &fakeMappingRepo{byChannel: map[uint64][]*model.ChannelModelMapping{
		7: {{ChannelID: 7, PublicModel: "vendor/model-a", UpstreamModel: "model-a", Enabled: true}},
	}}
	r := New(nil, Options{ChannelModelMappings: mappings})
	ch := &model.Channel{
		ID: 7, Name: "自营渠道", Type: 1, TypeKey: "openai",
		BaseURL: upstream.URL, APIKey: "sk-aqua", Models: []string{"vendor/model-a"},
	}

	res := r.ProbeChannel(context.Background(), ch, "sk-aqua", "vendor/model-a")
	if res.Err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("探测失败: err=%v status=%d body=%s", res.Err, res.StatusCode, res.Body)
	}
	if res.UpstreamModel != "model-a" {
		t.Fatalf("结果应回传上游实际收到的模型名，实际 %q", res.UpstreamModel)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotModel != "model-a" {
		t.Fatalf("上游收到的 model 应为 model-a，实际 %q（体：%s）", gotModel, gotBody)
	}
	if strings.Contains(gotBody, "vendor/") {
		t.Fatalf("不应把平台模型 ID 发给上游，实际 %s", gotBody)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("路径不应被映射改写：%q", gotPath)
	}
}

// TestProbeChannel_失败时透出上游原话 验证错误原因可被管理员看到。
func TestProbeChannel_失败时透出上游原话(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer upstream.Close()

	r := New(nil, Options{})
	ch := &model.Channel{
		Name: "openai 渠道", Type: 1, TypeKey: "openai",
		BaseURL: upstream.URL, APIKey: "sk-openai", Models: []string{"nope"},
	}

	res := r.ProbeChannel(context.Background(), ch, "sk-openai", "nope")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", res.StatusCode)
	}
	if !strings.Contains(res.Body, "model not found") {
		t.Fatalf("应透出上游原话，实际 %q", res.Body)
	}
}
