// 本文件实现敏感操作的二次验证（STEP-UP AUTH）：
// 做高危动作前必须重新输一次密码，验证只在当前会话生效一段时间后过期。
//
// 意图（Why）：
//
//	会话被劫持的现实场景是：攻击者拿到了一台已登录设备上的令牌
//	（恶意扩展、共用电脑、流量嗅探），此后他能做用户能做的任何事。
//	而"登录时认证过密码"这件事随时间衰减——一周前的登录不足以证明此刻
//	坐在电脑前的是本人。二次验证把"证明此刻是本人"与"曾经登录过"切开。
//
// 设计取舍：
//   - 【挂在会话而不是用户上】：验证时效属于"这台设备这一次登录"。
//     挂在用户上会让一次输密码放行所有设备，等于没有验证。
//   - 【窗口制而非一次有效】窗口过长会让被劫持会话长期畅通；
//     过短会让管理员连续操作时反复输密码。15 分钟是常见取值。
//   - 【只保护不可撤回/涉及资产的写操作】：全给每一个接口都加上会让后台无法使用，
//     保护面越大、用户越倾向于想办法绕过。
//
// 流转（Flow）：
//
//	POST /api/auth/reauth {password} → 校验口令 → Sessions.UpdateReauth(sessionID, now)
//	 → 之后的敏感操作由 s.requireFreshReauth(c) 判定，过期则 403 auth.reauth_required
//
// 扩展（Extend）：
//
//	想给某个操作加保护：在处理器开头调用 s.requireFreshReauth(c)，
//	返回 true 才继续执行（见 handleGrantTrial / handleCreateBroadcast / handleAdminMarkOrderPaid）。
package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

// ReauthWindow 是一次二次验证的有效时长。
//
// 取 15 分钟：足够完成"一批连续的后台操作"，
// 又短到"攻击者不可能指望一次不小心看到的密码能用一整天"。
const ReauthWindow = 15 * time.Minute

// reauthRequest 是二次验证的请求体。
type reauthRequest struct {
	Password string `json:"password"`
}

// handleReauth 处理 POST /api/auth/reauth：重新验证密码并刷新当前会话的验证时刻。
//
// 返回 reauth_until：前端据此在页面上提示"免密窗口还剩多久"，
// 而不是让用户对着一个看不见的时间边界困惑。
func (s *Server) handleReauth(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}

	var req reauthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"request.invalid_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// 口令错误时不区分"输错"与"什么都没输"，统一一个提示：
	// 这是登录之后的操作，没必要为它保留枚举语义。
	if !crypto.VerifyPassword(req.Password, user.PasswordHash) {
		writeUserError(c, http.StatusUnauthorized,
			"auth.invalid_credentials", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
		return
	}

	session, ok := middleware.CurrentSession(c)
	if !ok || session.ID == 0 {
		// 理论不可达：本路由挂在 SessionAuth 之后。真出现了说明装配有误，
		// 明确报错好过"静默当成已验证"。
		slog.Error("二次验证：上下文中缺少会话信息（路由装配是否有误？）", "user_id", user.ID)
		s.respondInternalError(c, "无法定位当前会话")
		return
	}

	now := time.Now()
	if err := s.deps.Sessions.UpdateReauth(c.Request.Context(), session.ID, now); err != nil {
		s.respondInternalError(c, "记录二次验证结果失败", err)
		return
	}
	// 同步更新上下文里的会话快照：同一个请求里若紧接着还要判定
	// （例如将来把二次验证做成中间件），不必等下次请求才生效。
	session.ReauthAt = now

	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"reauth_until": now.Add(ReauthWindow).Unix(),
	})
}

// requireFreshReauth 判断当前会话是否在二次验证的有效窗口内。
//
// 返回 true 表示放行；false 表示已经写好响应，调用方必须立即 return。
//
// 为什么默认【不放行】找不到会话的情况：宁可让管理员多输一次密码，
// 也不能让"查不到会话"变成一条绕过二次验证的路径。
func (s *Server) requireFreshReauth(c *gin.Context) bool {
	session, ok := middleware.CurrentSession(c)
	if !ok {
		slog.Warn("敏感操作被拦下：上下文中没有会话信息", "path", c.Request.URL.Path)
		writeUserError(c, http.StatusForbidden,
			"auth.reauth_required", oai.TypePermission, "reauth_required")
		return false
	}
	if !session.IsReauthFresh(time.Now(), ReauthWindow) {
		writeUserError(c, http.StatusForbidden,
			"auth.reauth_required", oai.TypePermission, "reauth_required")
		return false
	}
	return true
}
