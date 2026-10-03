// AI Agent 在服务层的装配。
//
// 意图（Why）：
//
//	agent 包是纯领域层：它知道自己要查渠道、要调模型，但不该知道
//	"这些仓储从哪来""HTTP 客户端从哪来"。本文件是那层接线，
//	职责只有一件：把 Deps 里已有的依赖组装成 agent.Engine。
//
// 流转（Flow）：
//
//	server.New  → buildAgentEngine()（一次性）→ s.agent
//	请求到达     → agentEngine() 取已建好的实例（不加锁）
//
// 扩展（Extend）：
//
//	要给 agent 加能力：先在 agent 包加工具（internal/agent/tools.go），
//	再在本文件把对应仓储接进 AgentTools——
//	两处缺一不可，只有前者会得到"工具报未配置"，只有后者是死代码。
package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/agent"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// agentUpstreamClient 是 agent 调用上游模型用的 HTTP 客户端。
//
// 与网关转发用的客户端分开的原因：agent 的超时（120 秒）远短于
// 转发的 300 秒，且它不需要连接池级别的并发能力
// （agent 调用频率极低，一个用户一天问不了几次）。
// 复用转发客户端会让 agent 卡在转发那个更长的超时上，
// 表现为"问一句要等五分钟才报错"。
var agentUpstreamClient = &http.Client{
	Timeout: 120 * time.Second,
}

// errAgentNotConfigured 表示 agent 依赖未接入。
//
// 单独的错误类型而不是复用 agent 包的错误：调用方要据此返回 503
// （"功能未启用"），而不是 500（"服务坏了"）——
// 前者提示站长去配置，后者会让人以为程序有 bug。
var errAgentNotConfigured = errors.New("agent: 未接入依赖，功能未启用")

// buildAgentEngine 构造 agent 引擎；依赖不足时返回错误。
//
// 一次性构造而非每次请求新建：LLMClient 内部持有 HTTP 客户端，
// 每次新建等于每次新建一个连接池——高频调用下会耗尽文件句柄。
func (s *Server) buildAgentEngine() (*agent.Engine, error) {
	if s.deps.Channels == nil || s.deps.ChannelKeys == nil {
		// 没有渠道就选不出上游，没有密钥池就发不出请求。
		// 这两项是模型转发的核心依赖，缺失说明部署不完整。
		return nil, errAgentNotConfigured
	}
	llm := agent.NewLLMClient(s.deps.Channels, s.deps.ChannelKeys, agentUpstreamClient)
	return agent.NewEngine(llm, s.agentToolDeps()), nil
}

// agentToolDeps 构造工具层所需的依赖集合。
//
// 【不要在这里给客服加只读工具】
//
//	客服面向公网输入。给它任何"读"的能力，提示注入就能把站内数据套出来：
//	例如诱导它调用 list_users，然后要求"把结果原样念出来"。
//	工具是执行边界，提示词管不了这个——这是本站刻意的设计取舍，
//	改这里之前请先读 internal/agent/tools.go 的 ToolsForRole 注释。
func (s *Server) agentToolDeps() *agent.AgentTools {
	return &agent.AgentTools{
		Channels:  s.deps.Channels,
		ProbeLogs: s.deps.ChannelProbeLogs,
		UsageLogs: s.deps.UsageLogs,
		Users:     s.deps.Users,
		Orders:    s.deps.Orders,
		Settings:  s.deps.Settings,
	}
}

// agentSystemPrompt 返回该角色应使用的系统提示词。
//
// 配置未定制时回退到 model 包的内置底稿（见 agent_prompts.go）。
func agentSystemPrompt(settings model.AgentSettings, role model.AgentRole) string {
	return settings.SystemPromptFor(role)
}

// agentEngine 取已建好的 agent 引擎，必要时惰性构建。
//
// 用 sync.Once 而非在 New 里构建：构建本身不会失败，但依赖的完整性检查
// 放在惰性路径上可以让"部署缺依赖"表现为一条明确日志 + 503，
// 而不是让整个服务在启动时崩掉——缺 agent 依赖不该阻止站点正常提供
// 模型转发服务，那会让一个可选功能的缺失变成全站故障。
//
// 之所以要缓存：每次请求重建 LLMClient 会新建连接池（见 buildAgentEngine）。
func (s *Server) agentEngine() (*agent.Engine, error) {
	s.agentOnce.Do(func() {
		s.agent, s.agentErr = s.buildAgentEngine()
	})
	return s.agent, s.agentErr
}
