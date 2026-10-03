// AI Agent 与上游模型通信的客户端。
//
// 意图（Why）：
//
//	agent 要做两件事：把用户问题转成模型请求，以及在模型要求调用工具时
//	把工具结果回填后再次请求。这两件事都需要【拿到结构化响应】，
//	而 relay 层的转发入口（ServeOpenAIChatCompletions 等）直接写 HTTP 响应，
//	拿不到中间结果。
//
// 为什么不复用 relay：
//  1. relay 的入口签名是 Serve*(w, req)，它的职责是"把上游响应搬给客户端"，
//     而 agent 需要的是"解析后再决定下一步"——强行套用要构造一个假 ResponseWriter，
//     那种代码读起来像hack，跑起来也容易在错误处理上出错；
//  2. agent 的重试语义与 relay 不同（见 maxToolRounds），复用反而要加开关。
//     因此这里独立实现，直��对齐 OpenAI 兼容协议。
//
// 为什么不直接拿渠道行上的 APIKey：
//
//	渠道可能配了密钥池（多把凭据轮换），也可能只配了单密钥。
//	统一走 ListUsable 拿一把可用凭据，两种配置都能工作，
//	且与 relay 的凭据选择口径一致（都跳过被自动移除的）。
//
// 流转（Flow）：
//
//	Engine.ChatOnce(ctx, req) → 选渠道 → 取凭据 → POST {base}/v1/chat/completions
//	  → 解析响应 → 返回 ChatCompletion（含 tool_calls）
//	agent 主循环（见 engine.go）拿到 tool_calls → 执行工具 → 再调一次
//
// 扩展（Extend）：
//
//	要支持 Anthropic / Gemini 原生协议：在此加 provider 分支，
//	在 buildRequestBody 里按渠道类型产出对应格式，并在 parseResponse 里
//	统一转回内部的 ToolCall 结构。内部结构刻意保持中立，
//	就是为了让这一步不必改动上层循环。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

const (
	// chatCompletionsPath 是 OpenAI 兼容的对话端点。
	//
	// 自带 /v1 前缀；上游 base_url 也带版本段时由 joinUpstreamURL 去重
	//（否则 base_url=https://host/v1 会拼出 /v1/v1/chat/completions 而 404，
	// 这与转发链路曾经踩过的坑是同一类问题）。
	chatCompletionsPath = "/v1/chat/completions"

	// agentUpstreamTimeout 是单次上游请求的整体超时。
	//
	// 取 120 秒而非转发链路的 300 秒：agent 的一次对话可能包含
	// 多轮"请求 → 工具 → 再请求"，用户是在等一个网页回答，
	// 而不是转发一个可能长跑几分钟的补全任务。宁可超时让用户重试，
	// 也不让一条对话长时间挂着。
	agentUpstreamTimeout = 120 * time.Second

	// maxUpstreamErrorBytes 是读取上游错误体的上限。
	//
	// 上游出错时经常返回一整页 HTML，不限量读取会让内存被拖爆，
	// 而错误信息里真正有用的通常只有第一行。
	maxUpstreamErrorBytes = 4 << 10

	// maxResponseBytes 是读取成功响应体的上限。
	//
	// 取 8MB：足够装下带工具定义的长对话与完整回答，
	// 又能挡住"上游返回了个几百 MB 的异常响应"这类情况。
	maxResponseBytes = 8 << 20
)

// joinUpstreamURL 拼接上游地址并去掉重复的版本段。
//
// 为什么要去重：管理员常把 base_url 填成 https://host/v1（多数平台的习惯），
// 直接拼路径会得到 /v1/v1/chat/completions。判定方式是看 base 是否已以
// 该路径的版本段结尾——只去最外层的一段，不做更激进的路径重写
// （有些上游路径形态特殊，猜错反而更糟）。
func joinUpstreamURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	// path 形如 /v1/chat/completions，取出首段作为版本段。
	segments := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	if len(segments) > 0 && segments[0] != "" {
		version := "/" + segments[0]
		if strings.HasSuffix(base, version) {
			return base + "/" + strings.Join(segments[1:], "/")
		}
	}
	return base + path
}

// ChatMessage 是发给上游的一条消息。
//
// 刻意用单一结构承载全部角色，而不是为 system / user / assistant 各建一个类型：
// 上游协议里它们是同一个数组，只是 role 不同；
// 拆成多个类型后转换代码会到处散落，而漏转换一个 role 的后果是
// 上游返回 400（"messages 格式不对"），报错信息完全指不到具体哪条消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls 仅 assistant 有：模型请求调用工具时带上来。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 仅 tool 消息有：把工具结果与对应的请求对上号。
	//
	// 没有它，模型收到多条工具结果时无法判断哪条属于哪个调用，
	// 表现出来是"模型把工具结果张冠李戴"，且不会报错。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name 仅 tool 消息有：被调用的工具名（部分上游要求）。
	Name string `json:"name,omitempty"`
}

// 对话角色。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolCall 是模型请求调用一个工具的结构（OpenAI 格式）。
type ToolCall struct {
	// ID 由模型生成，回填结果时必须原样带回。
	ID   string       `json:"id"`
	Type string       `json:"type"`
	Func ToolCallFunc `json:"function"`
}

// ToolCallFunc 是 tool call 里的函数名与参数。
//
// 参数是 string 而非结构体：模型的输出是 JSON 文本，
// 提前反序列化会把"参数不是合法 JSON"变成解析阶段错误，
// 而那时已经无法告诉模型"你的参数写错了"——它会一直重试同样的错参数。
// 保持字符串，由工具层自己解析并返回可据以改问的错误。
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatRequest 是 chat/completions 的请求体。
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	// Tools 为空时不下发该字段：部分上游见到空数组会报错。
	Tools []toolSpec `json:"tools,omitempty"`
	// ToolChoice 固定 "auto"：让模型自己决定要不要调工具。
	//
	// 不支持 "required" / "none" 的上游会直接 400，而"每次都强制调工具"
	// 本身也是错的——用户问站点叫什么时候并不需要查数据库。
	ToolChoice string `json:"tool_choice,omitempty"`
	Stream     bool   `json:"stream"`
}

// toolSpec 是一个可调用工具的声明（下发给模型的部分）。
type toolSpec struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters 已是 json.RawMessage，直接内联为对象。
	Parameters json.RawMessage `json:"parameters"`
}

// chatResponse 是 chat/completions 的响应体。
type chatResponse struct {
	Choices []struct {
		Index        int         `json:"index"`
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
	// Error 字段用于上游以 200 返回错误体的兼容情况。
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// ChatResult 是一次上游请求的结果。
type ChatResult struct {
	// Content 是模型的文字回复（可能为空：模型只请求了工具调用时）。
	Content string
	// ToolCalls 是模型请求调用的工具。
	ToolCalls []ToolCall
	// FinishReason 结束原因；"tool_calls" 表示模型在等工具结果。
	FinishReason string
	// PromptTokens / CompletionTokens 用于调用量统计。
	PromptTokens     int
	CompletionTokens int
}

// HasToolCalls 判断模型是否在等工具结果。
func (r *ChatResult) HasToolCalls() bool {
	return r != nil && len(r.ToolCalls) > 0
}

// LLMClient 是 agent 侧的上游调用器。
type LLMClient struct {
	channels model.ChannelRepository
	keys     model.ChannelKeyRepository
	client   *http.Client
}

// NewLLMClient 构造 LLM 客户端。
//
// http 为 nil 时内部用默认客户端。
func NewLLMClient(channels model.ChannelRepository, keys model.ChannelKeyRepository, client *http.Client) *LLMClient {
	if client == nil {
		client = &http.Client{Timeout: agentUpstreamTimeout}
	}
	return &LLMClient{channels: channels, keys: keys, client: client}
}

// 选渠道失败与取凭据失败的错误：分开定义以便上层给出不同提示。
var (
	// ErrNoUsableChannel 表示没有可用于 agent 的渠道。
	ErrNoUsableChannel = errors.New("agent: 没有可用的渠道")
	// ErrNoUsableKey 表示渠道里没有可用凭据。
	ErrNoUsableKey = errors.New("agent: 渠道内没有可用密钥")
	// ErrModelRequired 表示未指定模型。
	ErrModelRequired = errors.New("agent: 未指定模型")
)

// ChatOnce 向模型发一次请求，返回结构化结果（不做工具循环）。
//
// model 必须是该渠道支持的名字——agent 不做模型名改写，
// 因为"猜一个差不多的名字"会得到 404，而 404 的报错完全指不出真实原因。
func (c *LLMClient) ChatOnce(
	ctx context.Context,
	modelName string,
	messages []ChatMessage,
	tools []Tool,
) (*ChatResult, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, ErrModelRequired
	}
	if len(messages) == 0 {
		return nil, errors.New("agent: 消息不能为空")
	}

	channel, apiKey, err := c.pickChannel(ctx, modelName)
	if err != nil {
		return nil, err
	}

	// tools 为空时不下发：部分上游见到空数组或 "auto" 会直接 400。
	reqBody := chatRequest{
		Model:    modelName,
		Messages: messages,
		Stream:   false, // agent 用非流式请求上游，再自己向前端流式输出。
	}
	// 为什么不让上游直接流式：agent 要在拿到完整 tool_calls 参数后才能
	// 执行工具，而流式响应里的 tool_call 参数是分片到达的，
	// 边收边执行等于要实现一套增量 JSON 解析器——复杂度远高于收益。
	// 代价是用户要等第一个完整回答才看到字，但换来了可靠的工具循环。
	if len(tools) > 0 {
		specs := make([]toolSpec, 0, len(tools))
		for _, tool := range tools {
			params := tool.Parameters
			if len(params) == 0 {
				// schema 缺失时给一个空对象：部分上游会拒绝缺 parameters 的工具声明，
				// 而一个"没有任何参数"的工具用空 schema 表达是正确的。
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			specs = append(specs, toolSpec{
				Type: "function",
				Function: toolFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
				},
			})
		}
		reqBody.Tools = specs
		reqBody.ToolChoice = "auto"
	}

	return c.doRequest(ctx, channel, apiKey, reqBody)
}

// pickChannel 选一个支持该模型且启用的渠道，并取一把可用凭据。
//
// 为什么只挑一个而不做多渠道故障转移：agent 的调用频率极低（一次对话几轮），
// 而多渠道重试会让"这次对话花了多少钱"变得不可预测。
// 拿不到凭据就直接报错，让站长去后台看渠道配置——那比自动重试到成功更有用。
func (c *LLMClient) pickChannel(ctx context.Context, modelName string) (*model.Channel, string, error) {
	if c.channels == nil {
		return nil, "", errors.New("agent: 未接入渠道数据")
	}

	status := model.ChannelStatusEnabled
	list, err := c.channels.List(ctx, model.ChannelQuery{Status: &status, Limit: 100})
	if err != nil {
		return nil, "", fmt.Errorf("agent: 读取渠道失败: %w", err)
	}
	if len(list) == 0 {
		return nil, "", ErrNoUsableChannel
	}

	// 优先选"明确列出了该模型"的渠道；都不列时退回第一条启用渠道
	//（模型列表为空在本站语义里就是"支持全部模型"）。
	var fallback *model.Channel
	for _, ch := range list {
		if len(ch.Models) == 0 {
			if fallback == nil {
				fallback = ch
			}
			continue
		}
		for _, m := range ch.Models {
			if strings.EqualFold(strings.TrimSpace(m), modelName) {
				fallback = ch
				break
			}
		}
		if fallback != nil && len(fallback.Models) > 0 {
			break
		}
	}
	if fallback == nil {
		return nil, "", fmt.Errorf("%w：没有任何启用中的渠道支持模型 %s", ErrNoUsableChannel, modelName)
	}

	if c.keys == nil {
		// 未接入密钥池时退回渠道行上的单一密钥。
		if fallback.APIKey == "" {
			return nil, "", ErrNoUsableKey
		}
		return fallback, fallback.APIKey, nil
	}

	keys, err := c.keys.ListUsable(ctx, fallback.ID)
	if err != nil {
		return nil, "", fmt.Errorf("agent: 读取渠道密钥失败: %w", err)
	}
	for _, k := range keys {
		// 只要 api_key 类型：oauth 类型的 Key 为空，
		// agent 暂不支持以订阅账号凭据调用（见渠道密钥的 Kind 字段说明）。
		if k.Kind == model.CredentialKindAPIKey && k.Key != "" {
			return fallback, k.Key, nil
		}
	}
	return nil, "", fmt.Errorf("%w：渠道「%s」里没有可用的 API 密钥", ErrNoUsableKey, fallback.Name)
}

// doRequest 发一次 HTTP 请求并解析响应。
func (c *LLMClient) doRequest(
	ctx context.Context,
	channel *model.Channel,
	apiKey string,
	body chatRequest,
) (*ChatResult, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("agent: 序列化请求失败: %w", err)
	}

	requestURL := joinUpstreamURL(channel.BaseURL, chatCompletionsPath)
	reqCtx, cancel := context.WithTimeout(ctx, agentUpstreamTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, requestURL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("agent: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// 与 relay.FetchModels 同样的取舍：不把底层 err 的细节（可能含内网地址）
		// 透传给调用方，只保留"连不上"这个事实。
		return nil, errors.New("agent: 连接上游失败（请检查渠道地址与网络）")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBytes))
		message := strings.TrimSpace(string(detail))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("上游返回 HTTP %d：%s", resp.StatusCode, truncateText(message, 300))
	}

	respRaw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("agent: 读取上游响应失败: %w", err)
	}

	var parsed chatResponse
	if err := json.Unmarshal(respRaw, &parsed); err != nil {
		return nil, fmt.Errorf("agent: 解析上游响应失败: %w", err)
	}
	// 有些上游用 200 携带错误体（如网关层的鉴权失败），必须单独判。
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("上游返回错误：%s", truncateText(parsed.Error.Message, 300))
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("agent: 上游没有返回任何回复（choices 为空）")
	}

	choice := parsed.Choices[0]
	return &ChatResult{
		Content:          choice.Message.Content,
		ToolCalls:        choice.Message.ToolCalls,
		FinishReason:     choice.FinishReason,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}, nil
}
