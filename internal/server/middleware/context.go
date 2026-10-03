// 本文件定义中间件在 gin.Context 中存取「已认证令牌」的约定。
//
// 意图（Why）：
//
//	鉴权中间件认证成功后，后续处理器往往需要知道"是哪个令牌在调用"
//	（例如记录用量、按令牌限流、审计日志）。用一个集中定义的键与访问函数，
//	可以避免各处用字符串字面量读取上下文——那种写法拼错时不会编译报错，
//	只会在运行时拿到 nil，属于典型的"静默失败"。
//
// 流转（Flow）：
//
//	middleware.TokenAuth 认证成功 → SetToken(c, tk)
//	  └─ 后续处理器 → TokenFromContext(c) 取出令牌
//
// 扩展（Extend）：
//
//	需要新增上下文信息（如用户、请求 ID）时，在本文件按同样方式
//	添加私有键常量与访问函数，保持风格一致。
package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// contextKeyToken 是令牌在 gin 上下文中的键。
//
// 使用私有常量而非字符串字面量：外部无法直接拼写该键，
// 只能通过本文件的访问函数读写，从而避免键名不一致的问题。
const contextKeyToken = "aqua.context.token"

// contextKeyAgentKey 是 agent 密钥在 gin 上下文中的键。
//
// 与 contextKeyToken 刻意分开：两者的角色语义完全不同
// （令牌决定"能调哪些模型"，agent 密钥决定"能拿到哪些工具"），
// 共用一个键会让"拿到哪个值"变成隐式约定，处理器读错时不会报错，
// 只会安静地按另一个身份继续执行。
const contextKeyAgentKey = "aqua.context.agent_key"

// SetToken 把已认证的令牌写入上下文（仅供本包的中间件调用）。
func SetToken(c *gin.Context, token *model.Token) {
	c.Set(contextKeyToken, token)
}

// TokenFromContext 取出当前请求已认证的令牌。
//
// 第二个返回值为 false 表示上下文中没有令牌——通常意味着该处理器
// 没有挂在鉴权中间件之后，属于装配错误，应在开发阶段暴露。
func TokenFromContext(c *gin.Context) (*model.Token, bool) {
	value, exists := c.Get(contextKeyToken)
	if !exists {
		return nil, false
	}
	token, ok := value.(*model.Token)
	return token, ok
}

// SetAgentKey 把已认证的 agent 密钥写入上下文（仅供本包的中间件调用）。
func SetAgentKey(c *gin.Context, key *model.AgentKey) {
	c.Set(contextKeyAgentKey, key)
}

// AgentKeyFromContext 取出当前请求已认证的 agent 密钥（含其角色）。
//
// 处理器必须经由它拿角色，而不要自己按别处传来的字符串判断：
// 角色决定工具授权，绕过这个读取就等于绕过角色校验。
func AgentKeyFromContext(c *gin.Context) (*model.AgentKey, bool) {
	value, exists := c.Get(contextKeyAgentKey)
	if !exists {
		return nil, false
	}
	key, ok := value.(*model.AgentKey)
	return key, ok
}
