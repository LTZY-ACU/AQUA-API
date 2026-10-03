// AI Agent 的运行配置（模型选择与系统提示词）。
//
// 意图（Why）：
//
//	站长原话是"客服会被注入严格的提示词，提示词可以授权工具调用"。
//	这意味着提示词是【产品配置】而不是代码常量——站长要能在后台改它，
//	否则"调整客服话术"就得改代码重新部署，对小白站长是不可能的。
//
// 安全性上的取舍（必须清楚）：
//
//	提示词可改 = 站长自己就能把客服变成"什么都答"或"什么都说"。
//	这不是缺陷而是既定设计：客服的口径本来就是站长说了算。
//	本站负责保证的是【边界】——客服无论怎么改提示词都拿不到站内数据，
//	因为工具授权由 agent.ToolsForRole 单点决定，与提示词无关。
//	换句话说：提示词可被改越权，但【数据边界不可被提示词跨越】。
//	若哪天把客服提示词改成"忽略之前的指令"，它能做的也只是胡言乱语，
//	读不到渠道、用户、订单——这正是把两者分开设计的原因。
//
// 流转（Flow）：
//
//	后台保存 → SettingRepository.SetMany（键值对）
//	→ LoadAgentSettings 读出并与默认值合并 → handleAgentChat 使用
//
// 扩展（Extend）：
//
//	要按【分组】给客服配不同人设：在此结构加 map 形态的子配置，
//	并让 ModelFor 同时接收分组参数；不要在 agent 包里读设置——
//	agent 是纯领域层，不该知道"设置"这种存储概念。
package model

import (
	"context"
	"strconv"
	"strings"
)

// Agent 配置的设置键。
const (
	// SettingKeyAgentEnabled 是 agent 总开关（"true" / "false"），默认关闭。
	//
	// 为什么默认关闭：agent 会调用上游模型，是真金白银的支出项，
	// 且客服入口一旦开放就在公网可达。默认开启等于让站长在没意识到的情况下
	// 承担费用并暴露一个问答接口。
	SettingKeyAgentEnabled = "agent_enabled"

	// SettingKeyAgentDefaultModel 是未指定角色时的兜底模型。
	SettingKeyAgentDefaultModel = "agent_default_model"

	// SettingKeyAgentOpsModel 是运维 agent 使用的模型。
	SettingKeyAgentOpsModel = "agent_ops_model"

	// SettingKeyAgentSupportModel 是在线客服 agent 使用的模型。
	SettingKeyAgentSupportModel = "agent_support_model"

	// SettingKeyAgentOpsSystemPrompt 是运维 agent 的系统提示词。
	SettingKeyAgentOpsSystemPrompt = "agent_ops_system_prompt"

	// SettingKeyAgentSupportSystemPrompt 是在线客服 agent 的系统提示词。
	SettingKeyAgentSupportSystemPrompt = "agent_support_system_prompt"

	// SettingKeyAgentHistoryEnabled 是否携带历史对话，默认开启。
	//
	// 提供开关是为了排障：站长怀疑"多轮上下文让模型答歪了"时，
	// 能一键切成单轮问答验证，不必改代码。
	SettingKeyAgentHistoryEnabled = "agent_history_enabled"
)

// Agent 配置的长度上限（按 rune 计）。
//
// 提示词上限取 8000 字：足够写详尽的人设与边界说明，
// 又不至于让每次请求都拖着几万 token 的固定开销。
const (
	agentPromptMaxRunes   = 8000
	agentModelNameMaxLen  = 200
	agentHistoryMaxTurnsR = 20
)

// AgentSettings 是 agent 的运行配置。
type AgentSettings struct {
	// Enabled 是总开关。关闭时两个入口都返回"未启用"。
	Enabled bool
	// DefaultModel 是兜底模型名；角色未单独配置时用它。
	DefaultModel string
	// OpsModel / SupportModel 是按角色的模型名覆盖。
	OpsModel     string
	SupportModel string
	// OpsSystemPrompt / SupportSystemPrompt 是按角色的系统提示词。
	OpsSystemPrompt     string
	SupportSystemPrompt string
	// HistoryEnabled 是否携带历史对话。
	HistoryEnabled bool
}

// ModelFor 返回该角色应使用的模型名；未配置时回退到 DefaultModel。
//
// 回退放在这里而不是调用处：两个入口（运维 / 客服）都会用到，
// 分散判断必然有一处漏掉，表现为"客服接口报未配置模型"。
func (s AgentSettings) ModelFor(role AgentRole) string {
	var specific string
	switch role {
	case AgentRoleOps:
		specific = s.OpsModel
	case AgentRoleSupport:
		specific = s.SupportModel
	}
	if specific = strings.TrimSpace(specific); specific != "" {
		return specific
	}
	return strings.TrimSpace(s.DefaultModel)
}

// SystemPromptFor 返回该角色的系统提示词；未配置时回退到内置底稿。
//
// 回退到内置底稿而不是空串：空提示词意味着模型按训练时的默认人格回答，
// 对"在线客服"这种角色会说出"我是通用 AI 助手，请找人工"这类破坏沉浸的话。
func (s AgentSettings) SystemPromptFor(role AgentRole) string {
	var specific string
	switch role {
	case AgentRoleOps:
		specific = s.OpsSystemPrompt
	case AgentRoleSupport:
		specific = s.SupportSystemPrompt
	}
	if specific = strings.TrimSpace(specific); specific != "" {
		return specific
	}
	return DefaultAgentSystemPrompt(role)
}

// DefaultAgentSettings 返回 agent 配置的默认值。
func DefaultAgentSettings() AgentSettings {
	return AgentSettings{
		// 默认关闭：见 SettingKeyAgentEnabled 的说明。
		Enabled:        false,
		DefaultModel:   "",
		HistoryEnabled: true,
		// 提示词默认留空，由 SystemPromptFor 回退到内置底稿。
		// 刻意不给一个"看似完整"的默认值：那会被站长当成推荐配置保存下来，
		// 而通用模板话术对这个站点毫无意义。
		OpsSystemPrompt:     "",
		SupportSystemPrompt: "",
	}
}

// LoadAgentSettings 从 KV 仓储读取 agent 配置并与默认值合并。
//
// 与 LoadSiteSettings 同一套语义：只在"存在且解析成功"时覆盖，
// 脏值一律回退默认。这样手工写坏一个值不会让整个 agent 入口瘫痪。
func LoadAgentSettings(ctx context.Context, repo SettingRepository) (AgentSettings, error) {
	settings := DefaultAgentSettings()
	if repo == nil {
		// 没有设置仓储时返回"关闭"的默认值而不是报错：
		// 调用方（agent 入口）据此明确告诉用户"未启用"，
		// 比抛出一个含糊的 500 更容易定位。
		return settings, nil
	}

	values, err := repo.GetAll(ctx)
	if err != nil {
		return settings, err
	}
	if v, ok := values[SettingKeyAgentEnabled]; ok {
		if parsed, err := parseBoolLenient(v); err == nil {
			settings.Enabled = parsed
		}
	}
	if v, ok := values[SettingKeyAgentHistoryEnabled]; ok {
		if parsed, err := parseBoolLenient(v); err == nil {
			settings.HistoryEnabled = parsed
		}
	}
	if v, ok := values[SettingKeyAgentDefaultModel]; ok {
		settings.DefaultModel = clampText(v, agentModelNameMaxLen)
	}
	if v, ok := values[SettingKeyAgentOpsModel]; ok {
		settings.OpsModel = clampText(v, agentModelNameMaxLen)
	}
	if v, ok := values[SettingKeyAgentSupportModel]; ok {
		settings.SupportModel = clampText(v, agentModelNameMaxLen)
	}
	if v, ok := values[SettingKeyAgentOpsSystemPrompt]; ok {
		settings.OpsSystemPrompt = clampText(v, agentPromptMaxRunes)
	}
	if v, ok := values[SettingKeyAgentSupportSystemPrompt]; ok {
		settings.SupportSystemPrompt = clampText(v, agentPromptMaxRunes)
	}
	return settings, nil
}

// clampText 去除首尾空白并按 rune 截断。
//
// 截断而不是报错：提示词被截短只是效果变差，
// 而拒绝保存会让站长看着"提示词明明填了却说不成功"无从排查。
func clampText(s string, limit int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// parseBoolLenient 宽松解析布尔值：空串与非布尔文本都算解析失败。
//
// 单独一个函数而不是让每个调用点写 `if parsed, err := strconv.ParseBool(v); err == nil`：
// 那个写法会在每个调用点重复一遍，而漏掉 err 判断的那一处会把默认值悄悄改成 false
// （对"总开关"来说那等于把 agent 静默关掉，用户只会看到"助手没反应"）。
func parseBoolLenient(v string) (bool, error) {
	return strconv.ParseBool(strings.TrimSpace(v))
}

