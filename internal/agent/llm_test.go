// agent LLM 客户端的纯逻辑测试（不发真实网络请求）。
//
// 测试重点：
//   - joinUpstreamURL 的版本段去重（/v1/v1 是转发链路踩过的坑，agent 不能重犯）；
//   - 请求体构造：tools 为空时不下发该字段（部分上游见到空数组会 400）；
//   - 响应解析：tool_calls 完整取出、200 携带错误体时被识别。
package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestJoinUpstreamURL_版本段去重 是最重要的一条。
//
// 管理员常把 base_url 填成 https://host/v1（各平台文档都这么写），
// 朴素拼接会得到 /v1/v1/chat/completions 而 404。
// relay 曾经踩过这个坑，agent 走的是另一条拼接路径，必须单独验证。
func TestJoinUpstreamURL_版本段去重(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{
			name: "基础地址不带版本段",
			base: "https://api.openai.com",
			path: chatCompletionsPath,
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "基础地址已带 /v1（最常见的老坑）",
			base: "https://api.openai.com/v1",
			path: chatCompletionsPath,
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "基础地址末尾有斜杠",
			base: "https://api.openai.com/v1/",
			path: chatCompletionsPath,
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "基础地址有空格",
			base: "  https://api.openai.com/v1  ",
			path: chatCompletionsPath,
			want: "https://api.openai.com/v1/chat/completions",
		},
		{
			name: "非 v1 版本段（智谱形态）",
			base: "https://open.bigmodel.cn/api/paas/v4",
			path: chatCompletionsPath,
			want: "https://open.bigmodel.cn/api/paas/v4/v1/chat/completions",
		},
		{
			name: "自建反代根路径",
			base: "http://127.0.0.1:3000",
			path: chatCompletionsPath,
			want: "http://127.0.0.1:3000/v1/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinUpstreamURL(tc.base, tc.path)
			if got != tc.want {
				t.Errorf("joinUpstreamURL(%q) = %q，期望 %q", tc.base, got, tc.want)
			}
			// 无论怎么拼，都不该出现 /v1/v1/ 这种重复版本段。
			if strings.Contains(got, "/v1/v1/") {
				t.Errorf("拼接结果含重复版本段：%q", got)
			}
		})
	}
}

// TestChatOnce_工具为空时不下发tools 验证请求体里没有 tools 字段。
//
// 部分上游（尤其自建的兼容层）见到 "tools": [] 会直接 400，
// 表现为"agent 一点都用不了"，而原因在错误信息里完全看不出来。
func TestChatOnce_工具为空时不下发tools(t *testing.T) {
	var captured map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-test")
	if _, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "你好"}}, nil); err != nil {
		t.Fatalf("ChatOnce 失败: %v", err)
	}

	if _, exists := captured["tools"]; exists {
		t.Error("tools 为空时不应下发该字段")
	}
	if _, exists := captured["tool_choice"]; exists {
		t.Error("tools 为空时不应下发 tool_choice")
	}
	if got, _ := captured["stream"].(bool); got {
		t.Error("agent 用非流式请求上游，stream 应为 false")
	}
}

// TestChatOnce_有工具时下发tools与auto 验证工具确实传给了上游。
func TestChatOnce_有工具时下发tools与auto(t *testing.T) {
	var captured map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"好"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-test")
	tools := ToolsForRole(model.AgentRoleOps, clientToolsDeps())
	if _, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "有哪些渠道"}}, tools); err != nil {
		t.Fatalf("ChatOnce 失败: %v", err)
	}

	if got, _ := captured["tool_choice"].(string); got != "auto" {
		t.Errorf("tool_choice = %q，期望 auto", got)
	}
	toolsField, ok := captured["tools"].([]any)
	if !ok || len(toolsField) == 0 {
		t.Fatal("有工具时应下发非空 tools 数组")
	}
	// 每个工具都要有 name/description/parameters，
	// 缺 parameters 的工具声明会被部分上游拒收。
	for _, raw := range toolsField {
		spec, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("工具声明不是对象: %#v", raw)
		}
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			t.Fatalf("工具声明缺少 function: %#v", spec)
		}
		if fn["name"] == nil || fn["description"] == nil || fn["parameters"] == nil {
			t.Errorf("工具声明字段不全: %#v", fn)
		}
	}
}

// TestChatOnce_解析工具调用 验证 tool_calls 能被完整取出。
//
// 工具循环完全依赖这一步：解析不出来就等于 agent 没有工具。
func TestChatOnce_解析工具调用(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"",
				"tool_calls":[{"id":"call_1","type":"function",
					"function":{"name":"list_channels","arguments":"{\"only_enabled\":true}"}}]},
				"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}
		}`))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-test")
	tools := ToolsForRole(model.AgentRoleOps, clientToolsDeps())

	result, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "我配了哪些渠道"}}, tools)
	if err != nil {
		t.Fatalf("ChatOnce 失败: %v", err)
	}

	if !result.HasToolCalls() {
		t.Fatal("应识别出 tool_calls")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("应解析出 1 个工具调用，实际 %d 个", len(result.ToolCalls))
	}
	call := result.ToolCalls[0]
	if call.ID != "call_1" {
		t.Errorf("工具调用 ID = %q，期望 call_1（回填结果时要用它对上号）", call.ID)
	}
	if call.Func.Name != "list_channels" {
		t.Errorf("工具名 = %q，期望 list_channels", call.Func.Name)
	}
	// 参数保持字符串形态：解析交给工具层，那里才能给出"参数错了怎么改"的提示。
	if !strings.Contains(call.Func.Arguments, "only_enabled") {
		t.Errorf("参数应原样保留为 JSON 字符串，实际 %q", call.Func.Arguments)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q，期望 tool_calls", result.FinishReason)
	}
	if result.PromptTokens != 100 || result.CompletionTokens != 20 {
		t.Errorf("token 统计 = %d/%d，期望 100/20", result.PromptTokens, result.CompletionTokens)
	}
}

// TestChatOnce_上游200携带错误体 验证这种情况被识别为错误。
//
// 有些网关层用 200 携带错误 JSON。不判的话，模型会拿到一个
// "没有 choices 的成功响应"，上层报"choices 为空"——
// 真实原因（鉴权失败）被完全掩盖，站长会去看错的方向。
func TestChatOnce_上游200携带错误体(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth"}}`))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-wrong")
	_, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("200 携带错误体时应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("错误信息应包含上游原文（便于站长判断是密钥问题），实际：%v", err)
	}
}

// TestChatOnce_非2xx时截断错误体 验证超长错误页不会把内存拖爆。
func TestChatOnce_非2xx时截断错误体(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		// 返回 1MB 的错误页，模拟异常上游
		_, _ = w.Write([]byte(strings.Repeat("A", 1<<20)))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-test")
	_, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("非 2xx 时应返回错误")
	}
	// 错误信息本身也被截断到 300 字符（外层），
	// 所以完整报错不会把 1MB 内容带出来。
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长（%d 字符），应已截断", len(err.Error()))
	}
}

// TestChatOnce_空choices 验证上游返回空结果时明确报错。
func TestChatOnce_空choices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	client := newTestLLMClient(t, upstream.URL, "sk-test")
	_, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "choices") {
		t.Errorf("空 choices 应明确报错，实际：%v", err)
	}
}

// TestChatOnce_未指定模型 验证前置校验先于网络请求。
func TestChatOnce_未指定模型(t *testing.T) {
	client := newTestLLMClient(t, "http://127.0.0.1:1", "sk-test")
	_, err := client.ChatOnce(t.Context(), "  ",
		[]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if err != ErrModelRequired {
		t.Errorf("err = %v，期望 ErrModelRequired", err)
	}
}

// TestChatOnce_无可用渠道 验证渠道缺失时给出可据以排查的错误。
func TestChatOnce_无可用渠道(t *testing.T) {
	client := NewLLMClient(&fakeChannelRepo{}, nil, nil)
	_, err := client.ChatOnce(t.Context(), "gpt-4o",
		[]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("没有渠道时应报错")
	}
	// 错误必须指向"渠道"而不是"网络"——站长该去配渠道，不是去查网线。
	if !strings.Contains(err.Error(), "渠道") {
		t.Errorf("错误信息应指向渠道配置，实际：%v", err)
	}
}

// newTestLLMClient 构造一个指向测试服务器的客户端。
func newTestLLMClient(t *testing.T, baseURL, apiKey string) *LLMClient {
	t.Helper()
	ch := &fakeChannelRepo{
		channels: []*model.Channel{
			{
				ID: 1, Name: "测试渠道", BaseURL: baseURL, APIKey: apiKey,
				TypeKey: "openai", Status: model.ChannelStatusEnabled,
			},
		},
	}
	return NewLLMClient(ch, nil, &http.Client{})
}

// clientToolsDeps 给工具集一个空依赖，只为拿到工具定义用于测试请求体。
func clientToolsDeps() *AgentTools {
	return &AgentTools{}
}
