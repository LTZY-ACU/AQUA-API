// AI Agent 入口的鉴权中间件。
//
// 意图（Why）：
//
//	agent 入口与模型接口（/v1）权限边界完全不同：
//	后者是"拿令牌换上游额度"，前者是"拿密钥问助手"。
//	更关键的是，密钥自带角色——运维助手带工具能读站内数据，
//	在线客服不带任何工具。若这两类入口共用一套鉴权，
//	迟早会出现"客服密钥被当成令牌用去调 /v1"这类越权。
//	因此这里做【独立鉴权】，并把角色作为一等结果写进上下文，
//	由下游的 agent.ToolsForRole 决定它能拿到哪些工具。
//
// 流转（Flow）：
//
//	请求 → AgentKeyAuth
//	  ├─ extractAPIKey        从 Authorization / X-API-Key 提取明文
//	  ├─ 格式前缀校验          非 ak- 前缀直接拒（省一次查库，也避免误用令牌）
//	  ├─ HashAgentKey         算摘要
//	  ├─ AgentKeys.GetByHash  走 key_hash 唯一索引
//	  ├─ IsActive             同时判状态与过期
//	  └─ SetAgentKey → c.Next()
//
// 顺序为什么这样：
//
//	前缀校验放在查库【之前】。它不提供安全性，但能把"拿令牌来调 agent"
//	这类误用在最便宜的位置挡掉；更重要的是，若不校验前缀，
//	同一个摘要会同时命中 tokens 与 agent_keys 两张表，
//	届时"查到的是哪一张"就取决于本文件的某一行代码——
//	那种设计下，任何一次后续改动都可能变成越权。
//
// 扩展（Extend）：
//
//	要加"每把密钥独立的调用配额"：在本文件查库成功后、
//	放行之前计数（参照 TokenAuth 的预留做法）。
//	不要放到处理器里——那是鉴权该做的事，处理器漏一次就是无限额。
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// agentKeyDisabledCode 是"密钥已禁用/已过期"专用的错误码。
//
// 刻意与 invalid_api_key 分开：站长停用了一把 key 后只想让它失效，
// 却在客户端看到"密钥无效"会怀疑是配错了、反复重建同一把。
// 分开之后，客户端可以据此明确停止重试。
const agentKeyDisabledCode = "agent_key_disabled"

// agentNotEnabledCode 是"本站未部署 agent"专用的错误码。
//
// 单一 code 而非复用 disabled：两者的处置完全不同——
// disabled 是"稍后管理员可能会开"，客户端应当退避重试；
// not_enabled 是"这个站点压根没有 agent"，重试多少次都一样。
// 混用会让客户端对着一个永远 503 的接口无限退避重试。
const agentNotEnabledCode = "agent_not_enabled"

// AgentKeyAuth 返回校验 agent 密钥的 gin 中间件。
//
// keys 为 nil 时【一律拒绝】而不是放行：未接入密钥仓储说明部署未完成，
// 此时若默认放行，等于开了一个不需要任何凭据的公网接口。
func AgentKeyAuth(keys model.AgentKeyRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		if keys == nil {
			abortWithErrorKey(c, http.StatusServiceUnavailable,
				"agent.not_enabled", oai.TypeServer, agentNotEnabledCode)
			return
		}

		rawKey := extractAPIKey(c.Request)
		if rawKey == "" {
			abortWithErrorKey(c, http.StatusUnauthorized,
				"auth.missing_token", oai.TypeAuthentication, oai.CodeMissingAPIKey)
			return
		}

		// 前缀校验放在查库之前：既是廉价过滤，也把"两类密钥混淆"这件事
		// 在最外层就变成一次明确的失败，而不是一条查不到记录的模糊 401。
		if !strings.HasPrefix(rawKey, model.AgentKeyPrefix) {
			abortWithErrorKey(c, http.StatusUnauthorized,
				"agent.invalid_key", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
			return
		}

		key, err := keys.GetByHash(c.Request.Context(), model.HashAgentKey(rawKey))
		if err != nil {
			if errors.Is(err, model.ErrAgentKeyNotFound) {
				// 查不到与密钥被篡改无法区分，也【不应】区分——
				// 告诉攻击者"这条不存在但那条存在"等于给出枚举接口。
				abortWithErrorKey(c, http.StatusUnauthorized,
					"agent.invalid_key", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
				return
			}
			abortWithErrorKey(c, http.StatusInternalServerError,
				"agent.auth_failed", oai.TypeServer, oai.CodeInternal)
			return
		}

		if !key.IsActive() {
			// 禁用与过期共用一个错误码：两者对调用方都是"这把 key 现在不能用"，
			// 且都不该继续重试。区分它们对客户端毫无用处，
			// 却会泄漏"这把 key 曾经有效"这一信息。
			abortWithErrorKey(c, http.StatusUnauthorized,
				"agent.key_disabled", oai.TypeAuthentication, agentKeyDisabledCode)
			return
		}

		SetAgentKey(c, key)
		c.Next()
	}
}
