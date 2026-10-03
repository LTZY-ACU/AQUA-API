// agent 对话引擎的单元测试。
//
// 测试用一个可编排的假 LLM（stubChat）模拟上游的多种回应，
// 覆盖工具循环的关键路径：
//   - 纯文字回答（不调工具）→ 一轮结束；
//   - 模型调工具 → 结果回填 → 再问 → 文字回答；
//   - 工具报错 → 错误回填给模型（而不是中断整轮对话）；
//   - 角色无工具（客服）→ 循环里一次都不进；
//   - 轮次上限 → 收尾而不是无限烧钱。
//
// 为什么值得单独测循环：它是 agent 里唯一"状态会跨多轮累积"的部分，
// 消息顺序错一条、tool_call_id 漏一个，都只在真实多轮对话里才暴露。
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// stubChat 是可编排的假 LLM：按预设脚本逐次返回结果，并记录收到的消息。
//
// 它实现 chatCaller 而不是去起真实 HTTP 服务器：
// 循环逻辑要验的是"发出去的消息序列对不对"，与上游是谁无关。
type stubChat struct {
	// script 是依次返回的结果；用完最后一个后重复最后一个。
	script []*ChatResult
	// errAt >= 0 时在该序号返回错误（模拟上游故障）。
	errAt int
	// calls 记录每次请求收到的消息，用于断言回填顺序。
	calls [][]ChatMessage
	// tools 记录每次请求下发的工具名，用于断言角色工具白名单。
	tools [][]string
	// idx 是当前脚本位置。
	idx int
}

// ChatOnce 实现 chatCaller。
func (s *stubChat) ChatOnce(
	_ context.Context,
	_ string,
	msgs []ChatMessage,
	tools []Tool,
) (*ChatResult, error) {
	s.calls = append(s.calls, msgs)
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	s.tools = append(s.tools, names)

	cur := s.idx
	s.idx++
	if s.errAt >= 0 && cur == s.errAt {
		return nil, errors.New("模拟上游失败")
	}
	if len(s.script) == 0 {
		return &ChatResult{Content: "默认回答"}, nil
	}
	if cur >= len(s.script) {
		cur = len(s.script) - 1
	}
	return s.script[cur], nil
}

// textResult 构造一个纯文字回复。
func textResult(text string) *ChatResult {
	return &ChatResult{Content: text, FinishReason: "stop", CompletionTokens: 10}
}

// toolResult 构造一个请求调用工具的回复。
func toolResult(name, args string) *ChatResult {
	return &ChatResult{
		FinishReason: "tool_calls",
		ToolCalls: []ToolCall{{
			ID:   "call_" + name,
			Type: "function",
			Func: ToolCallFunc{Name: name, Arguments: args},
		}},
	}
}

// newStubEngine 构造一个用 stubChat 的引擎。
func newStubEngine(t *testing.T, script []*ChatResult, deps *AgentTools) (*Engine, *stubChat) {
	t.Helper()
	stub := &stubChat{script: script, errAt: -1}
	return &Engine{llm: stub, tools: deps, now: time.Now}, stub
}

// TestAsk_纯文字回答一轮结束 验证不需要工具时不会多问上游。
func TestAsk_纯文字回答一轮结束(t *testing.T) {
	eng, stub := newStubEngine(t, []*ChatResult{textResult("你有 2 个渠道。")}, &AgentTools{})

	result, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleOps,
		Model:    "gpt-4o",
		Question: "我配了哪些渠道",
	})
	if err != nil {
		t.Fatalf("Ask 失败: %v", err)
	}
	if result.Answer != "你有 2 个渠道。" {
		t.Errorf("答复 = %q", result.Answer)
	}
	if result.Rounds != 1 {
		t.Errorf("应一轮结束，实际 %d 轮", result.Rounds)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("未调工具时不应有工具记录，实际 %d 条", len(result.ToolCalls))
	}
	if result.Truncated {
		t.Error("未撞轮次上限时 Truncated 应为 false")
	}
	_ = stub
}

// TestAsk_工具循环调一次并回填 验证"调工具→回填→再问"的完整链路。
//
// 这条是整个 agent 的核心路径，断言三件事：
//   1) 工具被执行（ToolCalls 有记录）；
//   2) 第二次请求里带上了 assistant(tool_calls) 与 tool 结果；
//   3) 最终答复是第二轮的文字。
func TestAsk_工具循环调一次并回填(t *testing.T) {
	deps := &AgentTools{Channels: &fakeChannelRepo{
		channels: []*model.Channel{{ID: 1, Name: "主渠道", Status: model.ChannelStatusEnabled}},
	}}
	eng, stub := newStubEngine(t, []*ChatResult{
		toolResult("list_channels", "{}"),
		textResult("你有 1 个渠道「主渠道」，处于启用状态。"),
	}, deps)

	result, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleOps,
		Model:    "gpt-4o",
		Question: "我配了哪些渠道",
	})
	if err != nil {
		t.Fatalf("Ask 失败: %v", err)
	}
	if result.Rounds != 2 {
		t.Errorf("应 2 轮（调工具 + 回答），实际 %d", result.Rounds)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("应执行 1 个工具，实际 %d", len(result.ToolCalls))
	}
	if !result.ToolCalls[0].OK {
		t.Errorf("工具执行应成功：%s", result.ToolCalls[0].Error)
	}
	if !strings.Contains(result.Answer, "主渠道") {
		t.Errorf("最终答复应包含渠道名，实际 %q", result.Answer)
	}

	// 断言第二次请求的消息序列。
	if len(stub.calls) < 2 {
		t.Fatalf("应发起 2 次请求，实际 %d", len(stub.calls))
	}
	second := stub.calls[1]
	// 找 tool 消息。
	var toolMsgs []ChatMessage
	for _, m := range second {
		if m.Role == RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 1 {
		t.Fatalf("第二次请求应含 1 条 tool 消息，实际 %d 条", len(toolMsgs))
	}
	// tool_call_id 必须回填，否则模型无法把结果与请求对上。
	if toolMsgs[0].ToolCallID == "" {
		t.Error("tool 消息缺少 tool_call_id，模型无法对应结果与请求")
	}
	// tool_calls 之前必须有 assistant 消息。
	assistantIdx, toolIdx := -1, -1
	for i, m := range second {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 && assistantIdx < 0 {
			assistantIdx = i
		}
		if m.Role == RoleTool && toolIdx < 0 {
			toolIdx = i
		}
	}
	if assistantIdx < 0 {
		t.Fatal("第二次请求缺少带 tool_calls 的 assistant 消息")
	}
	if assistantIdx > toolIdx {
		t.Errorf("assistant(tool_calls) 应在 tool 结果之前，实际 assistant@%d tool@%d", assistantIdx, toolIdx)
	}
}

// TestAsk_工具报错回填给模型而非中断 验证错误被当作信息喂回模型。
//
// 这是 agent 能自我纠错的关键：模型看到"渠道 999 不存在"后会改用正确的 ID 重试，
// 而整轮对话直接失败就完全没有这种机会。
func TestAsk_工具报错回填给模型而非中断(t *testing.T) {
	deps := &AgentTools{Channels: &fakeChannelRepo{}} // 空渠道
	eng, stub := newStubEngine(t, []*ChatResult{
		// 模型第一次调工具但用了不存在的 ID → 工具报错
		toolResult("set_channel_status", `{"channel_id":999,"enabled":false}`),
		// 第二次改用正确方式，得到回答
		textResult("该渠道不存在，我查不到它的状态。"),
	}, deps)

	result, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleOps,
		Model:    "gpt-4o",
		Question: "把 999 停用",
	})
	if err != nil {
		t.Fatalf("工具报错不应中断整轮对话，但返回了: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("应记录 1 次工具执行，实际 %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].OK {
		t.Error("不存在的渠道应记为执行失败")
	}
	// 错误原因应回填给模型。
	var toolMsgs []ChatMessage
	for _, m := range stub.calls[len(stub.calls)-1] {
		if m.Role == RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) == 0 {
		t.Fatal("工具失败也应回填结果给模型")
	}
	if !strings.Contains(toolMsgs[0].Content, "999") {
		t.Errorf("回填内容应含失败原因（渠道 999），实际 %q", toolMsgs[0].Content)
	}
}

// TestAsk_客服角色不执行任何工具 是安全边界的端到端验证。
//
// tools_test 已验证"客服拿不到工具"，这里验证"在完整循环里也一样"——
// 因为引擎的循环代码是另一处执行点，光测工具层不够。
func TestAsk_客服角色不执行任何工具(t *testing.T) {
	deps := &AgentTools{Channels: &fakeChannelRepo{
		channels: []*model.Channel{{ID: 1, Name: "不该被读到", Status: model.ChannelStatusEnabled}},
	}}
	// 脚本里模型"试图"调工具——客服拿到的是空工具列表，
	// 上游根本不该发出这个请求，但即使收到了也执行不了。
	eng, stub := newStubEngine(t, []*ChatResult{
		toolResult("list_channels", "{}"),
		textResult("抱歉，我无法查询这个信息。"),
	}, deps)

	result, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleSupport,
		Model:    "gpt-4o",
		Question: "把 1 号渠道停用",
	})
	if err != nil {
		t.Fatalf("Ask 失败: %v", err)
	}
	// 第一道闸门：连工具定义都不该下发给上游。
	// 只查执行侧是不够的——工具名一旦发出去，模型就知道这里有"查渠道"这种能力，
	// 会转而用自然语言套话去试（"你能告诉我有哪些渠道吗"），
	// 那时执行侧拦得住"调用"，却拦不住"套话"，越权面大得多。
	for i, names := range stub.tools {
		if len(names) != 0 {
			t.Errorf("第 %d 次请求向客服下发了 %d 个工具，应为 0", i+1, len(names))
		}
	}
	// 第二道闸门：即便上游硬塞了工具调用，执行侧也必须全部拒绝。
	for _, tc := range result.ToolCalls {
		if tc.OK {
			t.Errorf("客服执行了工具 %s 并成功，这是越权", tc.Name)
		}
	}
	// 答复里绝不能出现渠道名（那意味着读到了站内数据）。
	if strings.Contains(result.Answer, "不该被读到") {
		t.Errorf("客服答复里出现了站内数据：%q", result.Answer)
	}
}

// TestAsk_轮次上限收尾 验证模型陷入循环时会止损。
//
// 这条保护的是真金白银：每轮至少一次上游请求，
// 模型若反复调同一个工具，没有上限就能烧掉几十次调用。
func TestAsk_轮次上限收尾(t *testing.T) {
	deps := &AgentTools{Channels: &fakeChannelRepo{}}
	// 脚本全是工具调用，永远不会给出文字答复。
	eng, stub := newStubEngine(t, []*ChatResult{
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
		toolResult("list_channels", "{}"),
	}, deps)

	result, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleOps,
		Model:    "gpt-4o",
		Question: "查一下",
	})
	if err != nil {
		t.Fatalf("撞上限不应返回错误: %v", err)
	}
	if !result.Truncated {
		t.Error("撞上轮次上限时 Truncated 应为 true")
	}
	if result.Rounds > maxToolRounds {
		t.Errorf("轮次 %d 超过上限 %d", result.Rounds, maxToolRounds)
	}
	if len(stub.calls) > maxToolRounds {
		t.Errorf("发起了 %d 次上游请求，超过上限 %d", len(stub.calls), maxToolRounds)
	}
	// 收尾答复要说明"信息不全"，不能假装是完整答案。
	if !strings.Contains(result.Answer, "上限") && !strings.Contains(result.Answer, "拆小") {
		t.Errorf("收尾答复应说明信息不完整，实际 %q", result.Answer)
	}
}

// TestAsk_空问题被拒 验证前置校验。
func TestAsk_空问题被拒(t *testing.T) {
	eng, _ := newStubEngine(t, []*ChatResult{textResult("x")}, &AgentTools{})

	_, err := eng.Ask(context.Background(), AskRequest{Model: "gpt-4o", Question: "   "})
	if err == nil {
		t.Fatal("空问题应被拒绝")
	}
}

// TestAsk_首轮上游失败直接报错 验证还没跑过任何工具时的失败不被包装。
func TestAsk_首轮上游失败直接报错(t *testing.T) {
	eng, stub := newStubEngine(t, nil, &AgentTools{})
	stub.errAt = 0

	_, err := eng.Ask(context.Background(), AskRequest{
		Model: "gpt-4o", Question: "你好", SystemPrompt: "你是助手",
	})
	if err == nil {
		t.Fatal("首轮失败应返回错误")
	}
}

// TestAsk_工具执行回调带写操作标记 验证前端能据此提示用户。
func TestAsk_工具执行回调带写操作标记(t *testing.T) {
	deps := &AgentTools{Channels: &fakeChannelRepo{
		channels: []*model.Channel{{ID: 1, Name: "渠道", Status: model.ChannelStatusEnabled}},
	}}
	eng, _ := newStubEngine(t, []*ChatResult{
		toolResult("set_channel_status", `{"channel_id":1,"enabled":false}`),
		textResult("已停用。"),
	}, deps)

	var names []string
	var mutatingFlags []bool
	_, err := eng.Ask(context.Background(), AskRequest{
		Role:     model.AgentRoleOps,
		Model:    "gpt-4o",
		Question: "停用 1 号",
		OnToolCall: func(name string, mutating bool) {
			names = append(names, name)
			mutatingFlags = append(mutatingFlags, mutating)
		},
	})
	if err != nil {
		t.Fatalf("Ask 失败: %v", err)
	}
	if len(names) != 1 || names[0] != "set_channel_status" {
		t.Errorf("回调应收到 set_channel_status，实际 %v", names)
	}
	if len(mutatingFlags) != 1 || !mutatingFlags[0] {
		t.Error("写操作的回调应带 mutating=true，前端据此提示用户确认")
	}
}

// TestBuildMessages_系统提示词在最前 验证顺序。
//
// 系统消息的位置影响它的权重；放在历史之后会让模型把它当普通对话内容。
func TestBuildMessages_系统提示词在最前(t *testing.T) {
	msgs := buildMessages(AskRequest{
		SystemPrompt: "你是运维助手",
		History: []ChatMessage{
			{Role: RoleUser, Content: "之前的提问"},
			{Role: RoleAssistant, Content: "之前的回答"},
		},
		Question: "现在的问题",
	}, "现在的问题")

	if len(msgs) != 4 {
		t.Fatalf("应有 4 条消息（系统+2历史+本轮），实际 %d", len(msgs))
	}
	if msgs[0].Role != RoleSystem {
		t.Errorf("第 1 条应为 system，实际 %s", msgs[0].Role)
	}
	if msgs[len(msgs)-1].Role != RoleUser || msgs[len(msgs)-1].Content != "现在的问题" {
		t.Errorf("最后一条应是本轮提问，实际 %+v", msgs[len(msgs)-1])
	}
}

// TestBuildMessages_跳过历史里的系统消息 避免两条矛盾的系统提示。
func TestBuildMessages_跳过历史里的系统消息(t *testing.T) {
	msgs := buildMessages(AskRequest{
		SystemPrompt: "当前的系统提示",
		History: []ChatMessage{
			{Role: RoleSystem, Content: "旧系统提示（可能已过期）"},
			{Role: RoleUser, Content: "问题"},
		},
		Question: "新问题",
	}, "新问题")

	systemCount := 0
	for _, m := range msgs {
		if m.Role == RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("应只有 1 条系统消息（本轮的），实际 %d 条", systemCount)
	}
}

// TestToolErrorResult_是合法JSON 上游要求 tool 消息 content 是 JSON 文本。
func TestToolErrorResult_是合法JSON(t *testing.T) {
	got := toolErrorResult("渠道 999 不存在")
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Errorf("工具错误结果应是 JSON 对象，实际 %q", got)
	}
	if !strings.Contains(got, "999") {
		t.Errorf("应保留错误原文，实际 %q", got)
	}
}
