// 本文件实现「网站登录态」的鉴权中间件与管理权限校验。
//
// 意图（Why）：
//
//	网站有另一套与模型接口完全不同的鉴权需求：
//	  1) 管理者登录后台、使用者登录门户，持有的是「会话令牌」（不透明随机串）；
//	  2) 会话需要可即时吊销（用户被禁用或改密后必须立刻失效）；
//	  3) 需要区分「已登录」与「是管理员」两级权限。
//	因此与 TokenAuth（校验 sk- 访问令牌）分开实现，避免两套语义混在一起。
//
// 流转（Flow）：
//
//	SessionAuth：取 Bearer → 查会话摘要 → 校验过期 → 载入用户 → 校验启用状态 → 注入上下文
//	RequireAdmin：从上下文取用户 → 判断角色 → 放行或 403
//
// 扩展（Extend）：
//
//	新增权限级别（如"渠道管理员"）时：在 UserRole 增加取值，并在此新增
//	RequireRole(roles...) 一类的中间件，而不是在各个处理器里散布角色判断。
package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// contextKeyUser 是登录用户在 gin 上下文中的键。
const contextKeyUser = "aqua.context.user"

// contextKeySession 是当前会话在 gin 上下文中的键。
const contextKeySession = "aqua.context.session"

// SetUser 把已认证用户写入上下文（仅供本包中间件调用）。
func SetUser(c *gin.Context, user *model.User) {
	c.Set(contextKeyUser, user)
}

// SetSession 把当前会话写入上下文（仅供本包中间件调用）。
//
// 为什么要把会话对象也放进上下文：会话上挂着"这次登录的来源与最近一次
// 二次验证的时刻"这类【会话级】事实，属于每个请求都要用到的安全上下文，
// 让处理器各自按令牌摘要再查一次既浪费一次查询，也让"有没有查"变成隐性约定。
func SetSession(c *gin.Context, session *model.Session) {
	c.Set(contextKeySession, session)
}

// CurrentSession 取出当前会话。
//
// 第二个返回值为 false 表示没有会话信息（路由未挂 SessionAuth，
// 或走的是 API 令牌鉴权而不是网站会话）。
func CurrentSession(c *gin.Context) (*model.Session, bool) {
	value, exists := c.Get(contextKeySession)
	if !exists {
		return nil, false
	}
	session, ok := value.(*model.Session)
	return session, ok
}

// CurrentUser 取出当前登录用户。
//
// 第二个返回值为 false 表示未登录——通常意味着该路由没有挂在 SessionAuth 之后。
func CurrentUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get(contextKeyUser)
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok
}

// SessionAuth 返回校验网站会话令牌的中间件。
//
// 参数：
//   - sessions：会话仓储（按令牌摘要查找）
//   - users：用户仓储（载入用户，校验是否被禁用）
//
// 校验顺序经过刻意设计：先验会话有效性（廉价），再载入用户（昂贵），
// 避免为无效会话付出额外的数据库查询。
func SessionAuth(sessions model.SessionRepository, users model.UserRepository) gin.HandlerFunc {
	return sessionAuth(sessions, users, true)
}

// SessionAuthOptional 返回「可选登录」中间件。
//
// 用途：公开接口在【已登录】时要能给出个性化结果（模型广场据查看者的代理分组
// 返回对应模型与价格），而在【未登录 / 会话无效】时必须照常返回公开结果——
// 因此不能像 SessionAuth 那样直接 401。
//
// 与 SessionAuth 共用同一段解析逻辑，唯一差别是失败时是否中断请求：
// 「会话怎样才算有效」只有一处实现，不会出现两套判定随时间长歪。
//
// 安全约定：本中间件【不构成任何鉴权】——它只负责「能认出就认出」，
// 需要"必须登录"的路由仍须挂 SessionAuth。
func SessionAuthOptional(sessions model.SessionRepository, users model.UserRepository) gin.HandlerFunc {
	return sessionAuth(sessions, users, false)
}

// sessionAuth 是两种会话中间件的共同实现。
//
// required 为 false 时，任何校验失败都只是「本次按匿名处理」：不写响应、
// 不中断链路，直接返回让 gin 继续走后续处理器。
func sessionAuth(sessions model.SessionRepository, users model.UserRepository, required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		fail := func(status int, key, typ, code string) {
			if !required {
				return
			}
			abortWithErrorKey(c, status, key, typ, code)
		}

		rawToken := extractAPIKey(c.Request)
		if rawToken == "" {
			fail(http.StatusUnauthorized,
				"auth.credentials_missing", oai.TypeAuthentication, oai.CodeMissingAPIKey)
			return
		}

		// 按摘要查找会话：数据库里没有明文令牌，泄露也无法直接使用
		session, err := sessions.GetByTokenHash(c.Request.Context(), crypto.SHA256Hex(rawToken))
		if err != nil {
			if errors.Is(err, model.ErrSessionNotFound) {
				fail(http.StatusUnauthorized,
					"auth.session_invalid", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
				return
			}
			// 根因只在这里有（fail 只带语义键），必须当场记下：
			// DB 故障时每个请求都会 500，没有这条日志就只剩"全线 500 却查无原因"。
			slog.Error("查询会话失败", "error", err, "client_ip", c.ClientIP())
			fail(http.StatusInternalServerError,
				"网关内部错误", oai.TypeServer, oai.CodeInternal)
			return
		}

		// 过期校验：会话表虽有过期时间，但清理是异步的，
		// 因此这里必须实时判断，不能假设"表里存在即有效"。
		if session.IsExpired(time.Now()) {
			// 顺手清理这条过期会话，避免无效数据长期堆积
			_ = sessions.DeleteByTokenHash(c.Request.Context(), session.TokenHash)
			fail(http.StatusUnauthorized,
				"auth.session_expired", oai.TypeAuthentication, oai.CodeTokenExpired)
			return
		}

		user, err := users.GetByID(c.Request.Context(), session.UserID)
		if err != nil {
			if errors.Is(err, model.ErrUserNotFound) {
				// 用户已被删除，但其会话仍在：清理掉并拒绝
				_ = sessions.DeleteByUserID(c.Request.Context(), session.UserID)
				fail(http.StatusUnauthorized,
					"auth.account_missing", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
				return
			}
			slog.Error("查询会话所属用户失败", "error", err,
				"user_id", session.UserID, "client_ip", c.ClientIP())
			fail(http.StatusInternalServerError,
				"网关内部错误", oai.TypeServer, oai.CodeInternal)
			return
		}

		// 被禁用的账号不得继续使用：这是"禁用"这一操作能否真正生效的关键。
		// 若只靠登录时校验，已登录的会话仍可长期访问，禁用形同虚设。
		if !user.IsActive() {
			_ = sessions.DeleteByUserID(c.Request.Context(), user.ID)
			fail(http.StatusForbidden,
				"auth.account_disabled", oai.TypePermission, oai.CodeTokenDisabled)
			return
		}

		SetUser(c, user)
		SetSession(c, session)
		c.Next()
	}
}

// RequireAdmin 返回校验管理员权限的中间件，必须挂在 SessionAuth 之后。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := CurrentUser(c)
		if !ok {
			// 走到这里说明路由装配有误（未挂 SessionAuth），按未登录处理而非放行
			abortWithErrorKey(c, http.StatusUnauthorized,
				"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
			return
		}
		if !user.IsAdmin() {
			abortWithErrorKey(c, http.StatusForbidden,
				"auth.admin_required", oai.TypePermission, "insufficient_privileges")
			return
		}
		c.Next()
	}
}
