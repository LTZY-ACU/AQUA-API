// AI Agent 的 HTTP 对话接口（SSE 流式）。
//
// 意图（Why）：
//
//	站长原话是"如果你不懂可以直接叫他帮你去干"。要让这句话成立，
//	接口必须满足三件事：
//	  1) 提问后立刻看到字在动（流式），而不是干等十几秒后突然出现全文；
//	  2) 能看到它"正在查什么"（工具调用可视化），否则用户无法判断
//	     它是真在查库还是在编；
//	  3) 客服（外部人）与运维（站长）走【不同的路由】，
//	     而不是同一个接口里判角色——路由级隔离让越权在装配期就能看出来。
//
// 流转（Flow）：
//
//	POST /api/agent/chat        （公开客服，support 密钥）
//	  → AgentKeyAuth → 角色必须是 support，否则 403
//	  → handleAgentChat（流式）
//
//	POST /api/admin/agent/chat  （后台运维，管理员会话，无需 agent key）
//	  → RequireAdmin → handleAgentChat（流式，角色固定 ops）
//
//	两条路由最终调用同一个处理器：共用一段流式与事件拼装逻辑，
//	避免"客服的流式行为与运维的不一致"这种只有细看才发现的差异。
//
// 【为什么客服路由不接受 ops 密钥】
//
//	反过来（ops 密钥访问客服路由）看起来无害——它只是拿到更少的能力。
//	但它会诱使前端把两个入口当成同一个：前端一旦发现"一把 key 到处能用"，
//	就会把它硬编码进公开页面，于是运维助手被暴露给所有访客。
//	这里宁可让运维在前台用不了，也不能让"一把 key 走天下"成为默认解法。
//
// 扩展（Extend）：
//
//	要加"对话历史持久化"：在 handleAgentChat 落库 messages，
//	并把 req.History 从客户端上报改为服务端按 key 读取——
//	客户端上报的历史等于让调用方决定模型看到什么，那是提示注入的现成入口。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/agent"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

const (
	// maxAgentQuestionRunes 是单个问题的长度上限（按 rune 计）。
	//
	// 按 rune 而不是字节：中文一个字是 3 字节，按字节算会让中文用户
	// 只能用三分之一的长度，而他们恰恰是最主要的使用者。
	maxAgentQuestionRunes = 2000

	// maxAgentHistoryTurns 是允许携带的历史轮数上限。
	//
	// 上限的作用是控制 token 成本与延迟：历史每多一轮就多一次全量重发，
	// 而聊天界面本身只保留最近若干轮，客户端传超长历史多半是拼装 bug。
	maxAgentHistoryTurns = 20

	// agentSSEKeepAlive 是流式过程中的注释心跳间隔。
	//
	// 为什么需要：反向代理（Nginx / Cloudflare）通常有 60s 空闲超时，
	// 而模型思考 + 工具执行期间本端确实可能长时间无字节输出。
	// 不发心跳就会表现为"接口随机挂掉"，且难以复现（取决于网络路径）。
	agentSSEKeepAlive = 15 * time.Second

	// agentStreamTimeout 是整次流式响应的总时限。
	//
	// 必须大于 agent 内部的上游超时（llm.go 的 agentUpstreamTimeout = 120s），
	// 因为工具循环最多 8 轮，每轮都可能各用满一次上游超时。
	// 这里给足余量后由 http.Server 的整体超时兜底，
	// 目的是"不让一个卡死的连接一直占着 goroutine"，而不是掐掉正常的长回答。
	agentStreamTimeout = 15 * time.Minute
)

// agentChatRequest 是对话请求体。
type agentChatRequest struct {
	// Question 是本轮提问。必填。
	Question string `json:"question"`
	// Model 可选，为空时用后台配置的默认模型。
	//
	// 允许覆盖是刻意的：站长排查"换个大模型会不会答得好"时需要临时切换。
	// 但它【不接受外部人传入的任意模型名】——见 handleAgentChat 的
	// allowModelOverride 说明，公开客服路由会把它强制忽略。
	Model string `json:"model"`
	// History 是此前的对话（不含本轮），按时间正序。
	//
	// 由客户端上报而非服务端读取，因此【不可信】：
	// 客户端可以伪造 assistant 消息让模型"以为"某件事已经谈妥，
	// 也可以塞入假 system 消息试图提权。安全处置见 buildMessages：
	// system 消息一律丢弃，且只接受 user/assistant 两种角色。
	History []agentChatTurn `json:"history"`
}

// agentChatTurn 是历史里的一轮。
type agentChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// sseEvent 是流式响应中的一个事件。
//
// 用命名事件（event:）而非只发 data：前端可以只监听关心的事件类型，
// 而不必在每个 data 上做字符串匹配来判断"这是正文还是工具状态"。
type sseEvent struct {
	// Type 取值：delta（正文增量）/ tool（工具执行）/ done（结束）/ error（出错）。
	Type string `json:"type"`
	// Text 仅 delta 事件使用。
	Text string `json:"text,omitempty"`
	// Tool 仅 tool 事件使用。
	Tool *sseToolEvent `json:"tool,omitempty"`
	// Result 仅 done / error 事件使用。
	Result *agentAskResultDTO `json:"result,omitempty"`
	// Message 仅 error 事件使用（面向人的可读原因）。
	Message string `json:"message,omitempty"`
}

// sseToolEvent 是"正在执行某个工具"的事件负载。
type sseToolEvent struct {
	Name string `json:"name"`
	// Mutating 标记这是写操作，前端据此提示"这一步会改动站点配置"。
	//
	// 这个标记必须在【执行前】发出去才有意义：等到执行完再告诉用户
	// "我刚才改了"，用户既无法阻止也无从知晓发生了什么。
	Mutating bool `json:"mutating"`
}

// agentAskResultDTO 是结束时回传的结果。
type agentAskResultDTO struct {
	Answer           string                    `json:"answer"`
	ToolCalls        []agentExecutedToolDTO    `json:"tool_calls"`
	Rounds           int                       `json:"rounds"`
	Truncated        bool                      `json:"truncated"`
	PromptTokens     int                       `json:"prompt_tokens"`
	CompletionTokens int                       `json:"completion_tokens"`
	Role             string                    `json:"role"`
	Tools            []agentToolBriefDTO       `json:"tools"`
}

// agentExecutedToolDTO 是一次工具执行的结果（回传给前端用于展示过程）。
type agentExecutedToolDTO struct {
	Name      string `json:"name"`
	Mutating  bool   `json:"mutating"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Result    string `json:"result,omitempty"`
}

// agentToolBriefDTO 是"这次给了模型哪些工具"的清单。
//
// 单独下发而不是让前端自己猜：客服与运维拿到的工具集不同，
// 前端界面应当据此决定要不要显示"正在查询…"这类过程提示。
type agentToolBriefDTO struct {
	Name string `json:"name"`
}

// handleAgentChat 是对话接口的实现（两条路由共用）。
//
// role 由【路由】决定而非由请求体传入：角色决定工具授权，
// 让它出现在请求体里就等于让调用方给自己授权。
func (s *Server) handleAgentChat(role model.AgentRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		engine, err := s.agentEngine()
		if err != nil {
			s.respondInternalError(c, err.Error())
			return
		}

		var req agentChatRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
				oai.TypeInvalidRequest, oai.CodeInvalidJSON)
			return
		}

		question := strings.TrimSpace(req.Question)
		if question == "" {
			oai.WriteError(c.Writer, http.StatusBadRequest, "问题不能为空",
				oai.TypeInvalidRequest, "empty_question")
			return
		}
		if len([]rune(question)) > maxAgentQuestionRunes {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				fmt.Sprintf("问题过长（最多 %d 字）", maxAgentQuestionRunes),
				oai.TypeInvalidRequest, "question_too_long")
			return
		}

		settings, err := model.LoadAgentSettings(c.Request.Context(), s.deps.Settings)
		if err != nil {
			s.respondInternalError(c, "读取 agent 配置失败")
			return
		}

		modelName := s.resolveAgentModel(c, role, req.Model, settings)
		if modelName == "" {
			// 没配模型就直接说清楚，而不是发一个必然失败的上游请求：
			// 后者会在站长日志里留下一堆 400，掩盖真正的原因。
			oai.WriteError(c.Writer, http.StatusServiceUnavailable,
				"站点管理员还没有配置 agent 使用的模型，请稍后再试",
				oai.TypeServer, "agent_model_not_configured")
			return
		}

		s.streamAgentAnswer(c, engine, agent.AskRequest{
			Role:         role,
			Model:        modelName,
			SystemPrompt: agentSystemPrompt(settings, role),
			History:      sanitizeAgentHistory(req.History),
			Question:     question,
		}, settings, role)
	}
}

// streamAgentAnswer 以 SSE 形式输出一次对话。
//
// 【为什么先写响应头再干活】
//
//	SSE 一旦写出第一个字节，HTTP 状态码就锁死为 200，之后任何错误
//	都只能塞进事件流里。因此校验全部前置（见 handleAgentChat），
//	剩下的错误一律以 error 事件告知前端——前端据此显示"出错了"而不是
//	把一段残缺的正文当成完整答案。
func (s *Server) streamAgentAnswer(
	c *gin.Context,
	engine *agent.Engine,
	req agent.AskRequest,
	settings model.AgentSettings,
	role model.AgentRole,
) {
	flusher, canFlush := c.Writer.(http.Flusher)
	if !canFlush {
		// 拿不到 Flusher 就无法流式。宁可明确报错，也不要"降级成一次性返回"：
		// 前端会以为自己在读流，实际拿到的是一个大 JSON，
		// 表现成"界面卡住不动"这种极难定位的现象。
		oai.WriteError(c.Writer, http.StatusInternalServerError,
			"当前服务端不支持流式响应",
			oai.TypeServer, oai.CodeInternal)
		return
	}

	writer := newSSEWriter(c.Writer, flusher)
	writer.writeHeaders()

	// 请求上下文加上总时限：连接断开时上游请求也随之取消，
	// 否则用户关掉页面后，上游还在替他烧钱跑到超时。
	ctx, cancel := context.WithTimeout(c.Request.Context(), agentStreamTimeout)
	defer cancel()

	stopKeepAlive := startAgentKeepAlive(writer)
	defer stopKeepAlive()

	tools := agent.ToolsForRole(role, s.agentToolDeps())

	req.OnDelta = func(text string) {
		writer.send(sseEvent{Type: "delta", Text: text})
	}
	// 工具事件在【执行前】发出：前端据此显示"正在查渠道…"。
	// 刻意不回传工具结果原文：那是模型与站点之间的中间产物，
	// 里面可能有渠道名、用户邮箱等站内细节，前端展示它反而扩大信息面。
	req.OnToolCall = func(name string, mutating bool) {
		writer.send(sseEvent{Type: "tool", Tool: &sseToolEvent{Name: name, Mutating: mutating}})
	}

	result, err := engine.Ask(ctx, req)
	if err != nil {
		slog.Warn("agent 对话失败", "error", err, "role", string(role))
		writer.send(sseEvent{
			Type:    "error",
			Message: "助手暂时无法回答这个问题，请稍后重试。",
		})
		writer.close()
		return
	}

	writer.send(sseEvent{Type: "done", Result: buildAgentResultDTO(result, role, tools)})
	writer.close()
}

// resolveAgentModel 决定本次调用用哪个模型。
//
// 【为什么公开客服路由忽略客户端传来的 model】
//
//	运维可以用它临时切换模型排查问题；客服不行——
//	若允许外部人指定模型，他就能指定一个贵得离谱的模型让站长付账，
//	或者指定一个被下架/未配权限的模型把错误信息当成探测信道。
//	因此客服路由一律用管理员配置的模型，请求里的 model 直接丢弃。
func (s *Server) resolveAgentModel(
	c *gin.Context,
	role model.AgentRole,
	requested string,
	settings model.AgentSettings,
) string {
	switch role {
	case model.AgentRoleOps:
		if name := strings.TrimSpace(requested); name != "" {
			return name
		}
	case model.AgentRoleSupport:
		// 刻意不使用 requested：见上方注释。
	}
	if name := strings.TrimSpace(settings.ModelFor(role)); name != "" {
		return name
	}
	return strings.TrimSpace(settings.DefaultModel)
}

// sanitizeAgentHistory 清洗客户端上报的历史对话。
//
// 这是提示注入的主要入口，必须在这里收口：
//   - 只保留 user / assistant 两种角色。放开 tool 角色等于让调用方
//     伪造工具结果（"系统已确认该渠道正常"），放开 system 则等于提权；
//   - 丢弃所有 system 消息：真正的系统提示词由服务端按角色注入，
//     客户端塞进来的任何 system 都只能是伪造品；
//   - 截断条数与长度：历史是重发的全文，成本与延迟都随它线性增长，
//     而超长历史几乎没有正当来源（界面只保留最近若干轮）。
func sanitizeAgentHistory(turns []agentChatTurn) []agent.ChatMessage {
	if len(turns) == 0 {
		return nil
	}
	if len(turns) > maxAgentHistoryTurns {
		// 从【尾部】保留最近的若干轮：对话的上下文连续性在最近几轮。
		turns = turns[len(turns)-maxAgentHistoryTurns:]
	}

	out := make([]agent.ChatMessage, 0, len(turns))
	for _, turn := range turns {
		content := strings.TrimSpace(turn.Content)
		if content == "" {
			continue
		}
		if len([]rune(content)) > maxAgentQuestionRunes {
			content = string([]rune(content)[:maxAgentQuestionRunes])
		}
		switch turn.Role {
		case agent.RoleUser:
			out = append(out, agent.ChatMessage{Role: agent.RoleUser, Content: content})
		case agent.RoleAssistant:
			out = append(out, agent.ChatMessage{Role: agent.RoleAssistant, Content: content})
		default:
			// 其余角色（含 system / tool / 拼错的）一律丢弃。
			// 不做"忽略大小写"的宽容处理：宽松匹配正是这类伪造的入口。
		}
	}
	return out
}

// buildAgentResultDTO 把引擎结果转成前端 DTO。
func buildAgentResultDTO(
	result *agent.AskResult,
	role model.AgentRole,
	tools []agent.Tool,
) *agentAskResultDTO {
	dto := &agentAskResultDTO{
		Answer:           result.Answer,
		Rounds:           result.Rounds,
		Truncated:        result.Truncated,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		Role:             string(role),
		Tools:            make([]agentToolBriefDTO, 0, len(tools)),
	}
	for _, t := range tools {
		dto.Tools = append(dto.Tools, agentToolBriefDTO{Name: t.Name})
	}
	dto.ToolCalls = make([]agentExecutedToolDTO, 0, len(result.ToolCalls))
	for _, t := range result.ToolCalls {
		dto.ToolCalls = append(dto.ToolCalls, agentExecutedToolDTO{
			Name:     t.Name,
			Mutating: t.Mutating,
			OK:       t.OK,
			Error:    t.Error,
			// 只给前 200 字：前端用它做"我查到了什么"的摘要展示，
			// 全量回传既占带宽又会把这批数据长期留在浏览器里。
			Result: truncateAgentText(t.Result, 200),
		})
	}
	return dto
}

// handleAdminAgentChat 处理 POST /api/admin/agent/chat。
//
// 走管理员会话而非 agent key：站长本人已经在后台登录了，
// 再要求他配一把额外的密钥只会让人以为"助手还没配好"。
// 角色固定 ops（站长本人，带工具）。
func (s *Server) handleAdminAgentChat(c *gin.Context) {
	s.handleAgentChat(model.AgentRoleOps)(c)
}

// handlePublicAgentChat 处理 POST /api/agent/chat（面向外部人的在线客服）。
//
// 鉴权在路由上（AgentKeyAuth），此处再判一次角色：
// 两处都判不是冗余——路由负责"有没有 key"，这里负责"这种 key 能不能用这个入口"。
// 少任何一处都会出现一种越权：只有前者，运维密钥可访问客服入口；
// 只有后者，客服密钥可访问运维入口。
func (s *Server) handlePublicAgentChat(c *gin.Context) {
	key, ok := middleware.AgentKeyFromContext(c)
	if !ok {
		// 走到这里说明路由装配漏挂了 AgentKeyAuth。
		// 按未鉴权处理而不是放行：这类"理论上不可能"的分支一旦真的发生，
		// 放行的代价是一个无需凭据的公网接口。
		oai.WriteError(c.Writer, http.StatusUnauthorized, "缺少 agent 密钥",
			oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	if key.Role != model.AgentRoleSupport {
		oai.WriteError(c.Writer, http.StatusForbidden,
			"该密钥无权访问在线客服入口",
			oai.TypePermission, "agent_role_not_allowed")
		return
	}
	s.handleAgentChat(key.Role)(c)
}

// ── SSE 输出 ──────────────────────────────────────────────────────

// sseWriter 封装 SSE 写出的细节（事件拼装、立即刷新）。
type sseWriter struct {
	w       gin.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w gin.ResponseWriter, flusher http.Flusher) *sseWriter {
	return &sseWriter{w: w, flusher: flusher}
}

// writeHeaders 写出 SSE 响应头。
//
// 三个头都必要：
//   - Content-Type: text/event-stream  告诉浏览器按事件流解析；
//   - Cache-Control: no-cache          否则代理会缓存整段回复；
//   - X-Accel-Buffering: no            告诉 Nginx 不要缓冲——
//     不少反代默认缓冲响应，开了它前端才会看到逐字输出而不是"憋到最后一起出"。
func (w *sseWriter) writeHeaders() {
	header := w.w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.w.WriteHeader(http.StatusOK)
	w.flusher.Flush()
}

// send 发出一条事件并立即刷新。
func (w *sseWriter) send(ev sseEvent) {
	payload, err := json.Marshal(ev)
	if err != nil {
		// 事件序列化失败只记日志、不中断流：正文已经写出去一半，
		// 此时中断会让用户看到"回答到一半没了"，比少一个事件糟得多。
		slog.Error("agent SSE 事件序列化失败", "error", err, "type", ev.Type)
		return
	}
	// 【下面这行替换是防御性的，正常路径下不会命中任何字符】
	//
	// SSE 规范里 data 中的裸换行会把一个事件拆成两个，因此这里替换成空格。
	// 但 json.Marshal 早已把字符串里的换行转义成反斜杠 + n 两个字符，
	// 序列化结果中不存在裸换行——所以这行现在跑的是个空操作。
	//
	// 为什么仍然保留：它守的是"将来有人改用手动拼 JSON"或"某天换了序列化器"
	// 这类改动。那种改动一旦发生，缺了这行的表现是前端随机少显示半句话，
	// 而且极难定位（后端日志一切正常）。留着它没有运行成本，
	// 删掉它则把一次潜在的静默故障换成了"防御代码看起来多余"。
	//
	// 如果哪天确认它真的不再需要，请连同本段注释一起删——只留一行无解释的
	// 替换更糟：读者会以为它在解决某个已知问题，进而不敢动它。
	body := strings.ReplaceAll(string(payload), "\n", " ")
	_, _ = fmt.Fprintf(w.w, "data: %s\n\n", body)
	w.flusher.Flush()
}

// close 发出流结束的哨兵事件。
//
// 用一个显式的 done 事件而不是"连接关闭"来表示结束：
// 客户端在 HTTP/1.1 下无法从读取结束里区分"正常结束"与"连接被掐"，
// 少这个哨兵时前端会在网络波动后一直显示"思考中"。
func (w *sseWriter) close() {
	_, _ = fmt.Fprint(w.w, "data: {\"type\":\"end\"}\n\n")
	w.flusher.Flush()
}

// startAgentKeepAlive 周期性发送 SSE 注释行作为心跳，返回停止函数。
//
// 注释行（以 ":" 开头）在 SSE 规范里被客户端忽略，
// 正好用来"占位"——保活连接而不干扰前端的事件解析。
func startAgentKeepAlive(w *sseWriter) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(agentSSEKeepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_, _ = fmt.Fprint(w.w, ": keep-alive\n\n")
				w.flusher.Flush()
			}
		}
	}()
	return func() { close(done) }
}

// truncateAgentText 按 rune 截断文本并追加省略标记。
func truncateAgentText(s string, limit int) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
