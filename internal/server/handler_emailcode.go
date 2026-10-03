// 本文件实现「邮箱验证码」的三种用途：注册、邮箱验证码登录、重置密码。
//
// 意图（Why）：
//
//	开放注册的站点最大的运营风险是脚本批量注册（垃圾账号、刷光免费额度、
//	发信通道被投诉）。邮箱验证码把"拥有一个真实可收信的邮箱"变成门槛，
//	使批量注册的成本显著高于收益。
//
//	同样的能力顺手还能解决两个真实的用户痛点：
//	  · 忘记密码 —— 老用户找回账号的唯一自助通道；
//	  · 记不住用户名 —— 用邮箱收码直接登录，不必先想起自己当初注册叫什么。
//
//	三种用途共用一个发码接口，但**用途串严格区分**（register / login / reset）：
//	验证码按用途哈希与查找，一条注册码无法被拿去登录，一条登录码也无法改密。
//
// 流转（Flow）：
//
//	POST /api/auth/email-code       申请验证码（body 里带 purpose）
//	  → 校验开关与邮件配置 → 冷却/小时上限 → 生成码 → 落库（只存摘要）
//	  → 按用途选择邮件模板 → 发信（失败回滚记录）→ 返回有效期与冷却
//
//	POST /api/auth/register         注册（见 handler_auth.go，用途 register）
//	POST /api/auth/email-login      邮箱验证码登录（用途 login）
//	POST /api/auth/password-reset   重置密码（用途 reset）
//
// 扩展（Extend）：
//
//	新增用途：在 model 里加 EmailCodePurpose* 常量 → 在 normalizeEmailCodePurpose
//	白名单里登记 → 在 mailer 里加一封模板 → 写一个消费它的处理器即可。
//	务必保证"发送"与"校验"两侧的用途字符串一致，否则永远校验不过。
package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/mailer"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

// emailCodeRequest 是申请验证码的请求体。
type emailCodeRequest struct {
	Email string `json:"email"`
	// Purpose 是用途：register / login / reset；留空按 register 处理（兼容旧前端）。
	Purpose string `json:"purpose"`
}

// emailCodeResponse 是申请验证码的响应体。
//
// 把 expires_in 与 cooldown 一并返回，前端即可显示"剩余有效时间"与
// "多少秒后可重发"的倒计时，而不必把这两个数值硬编码在前端。
type emailCodeResponse struct {
	OK        bool   `json:"ok"`
	ExpiresIn int    `json:"expires_in"` // 验证码有效期（秒）
	Cooldown  int    `json:"cooldown"`   // 重发冷却时间（秒）
	Message   string `json:"message"`    // 给用户看的提示文案
}

// normalizeEmailCodePurpose 归一化验证码用途，返回 false 表示取值非法。
//
// 白名单而非透传：用途串会被写进库并参与哈希，若允许任意取值，
// 攻击者可以伪造一串自己的用途去刷验证码（绕开按用途的冷却计数）。
func normalizeEmailCodePurpose(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", model.EmailCodePurposeRegister:
		return model.EmailCodePurposeRegister, true
	case model.EmailCodePurposeLogin:
		return model.EmailCodePurposeLogin, true
	case model.EmailCodePurposeReset:
		return model.EmailCodePurposeReset, true
	default:
		return "", false
	}
}

// handleSendEmailCode 处理验证码申请（三种用途共用）。
//
// 挂载在登录限流中间件之后：即使没有邮箱维度限流，也能挡住高频刷接口。
func (s *Server) handleSendEmailCode(c *gin.Context) {
	var req emailCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"request.invalid_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	purpose, validPurpose := normalizeEmailCodePurpose(req.Purpose)
	if !validPurpose {
		// 正常前端只会传三个合法用途之一，走到这里几乎都是直接调接口，
		// 因此用一句直白的中文即可，不必为它新增 6 种语言的词条。
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"验证码用途取值非法", oai.TypeInvalidRequest, "email_code_purpose_invalid")
		return
	}

	ctx := c.Request.Context()

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败")
		return
	}

	// 注册用途才受"注册开关"与"注册是否要求验证码"约束；
	// 登录与找回密码是既有用户的刚需，关掉注册也不该连找回密码一起关掉。
	if purpose == model.EmailCodePurposeRegister {
		if !settings.RegistrationEnabled {
			writeUserError(c, http.StatusForbidden,
				"auth.registration_closed", oai.TypePermission, "registration_disabled")
			return
		}
		if !settings.RegistrationRequireEmailCode {
			// 管理员已关闭"验证码校验"，此时不应再消耗邮件配额
			writeUserError(c, http.StatusBadRequest,
				"email.code_not_required", oai.TypeInvalidRequest, "email_code_not_required")
			return
		}
	}

	email := model.NormalizeEmail(req.Email)
	if err := model.ValidateEmailFormat(email); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"auth.invalid_email", oai.TypeInvalidRequest, "invalid_email")
		return
	}

	// 邮件通道未就绪时给出可操作的提示，而不是让用户以为"验证码已发出但没收到"
	if s.deps.Mailer == nil || !s.deps.Mailer.Configured() {
		writeUserError(c, http.StatusServiceUnavailable,
			"email.service_unavailable", oai.TypeServer, "email_service_unavailable")
		return
	}

	now := time.Now()

	// ── 冷却检查：同一邮箱同一用途两次申请之间必须间隔 60 秒 ──────
	// 目的：防止把本站当作"邮件轰炸机"（反复给同一个人发信）。
	// 注意这里用最新一条记录（含已过期但未消费的），因为"刚发过"这个事实
	// 不应因验证码过期而消失。
	if latest, err := s.deps.EmailCodes.LatestActive(ctx, email, purpose); err == nil {
		if elapsed := now.Sub(latest.CreatedAt); elapsed < model.EmailCodeResendCooldown {
			wait := int((model.EmailCodeResendCooldown - elapsed).Seconds()) + 1
			c.Header("Retry-After", strconv.Itoa(wait))
			writeUserError(c, http.StatusTooManyRequests,
				"email.cooldown", oai.TypeRateLimit, "email_code_cooldown", wait)
			return
		}
	} else if !errors.Is(err, model.ErrEmailCodeNotFound) {
		s.respondInternalError(c, "查询验证码记录失败")
		return
	}

	// ── 邮箱维度小时上限 ─────────────────────────────────────────
	since := now.Add(-time.Hour)
	emailCount, err := s.deps.EmailCodes.CountByEmailSince(ctx, email, since)
	if err != nil {
		s.respondInternalError(c, "统计验证码申请次数失败")
		return
	}
	if emailCount >= model.EmailCodeMaxPerEmailPerHour {
		writeUserError(c, http.StatusTooManyRequests,
			"email.rate_email", oai.TypeRateLimit, "email_code_email_limit")
		return
	}

	// ── 来源 IP 维度小时上限 ─────────────────────────────────────
	// 这一层是为了防止"换邮箱绕过邮箱维度限流"。
	clientIP := middleware.ClientIP(c)
	ipCount, err := s.deps.EmailCodes.CountByIPSince(ctx, clientIP, since)
	if err != nil {
		s.respondInternalError(c, "统计验证码申请次数失败")
		return
	}
	if ipCount >= model.EmailCodeMaxPerIPPerHour {
		writeUserError(c, http.StatusTooManyRequests,
			"email.rate_ip", oai.TypeRateLimit, "email_code_ip_limit")
		return
	}

	// ── 该不该真的发这封信 ──────────────────────────────────────
	//
	// 登录与重置只对"本站已注册且启用"的邮箱有意义。但对不存在的邮箱
	// **不能回一个不同的错误**，否则接口会变成"批量探测哪些邮箱在你站注册过"
	// 的枚举器。做法是：一切照旧（含冷却与限流计数），只是不发信，
	// 响应与真实发送完全一致。
	shouldSend := true
	if purpose != model.EmailCodePurposeRegister {
		user, err := s.deps.Users.GetByEmail(ctx, email)
		switch {
		case err == nil && user.IsActive():
			shouldSend = true
		case err == nil || errors.Is(err, model.ErrUserNotFound):
			// 邮箱未注册、或账号被禁用：不发信，但对外表现与发送成功一致
			shouldSend = false
		default:
			// 查询异常同样按"不发"处理：宁可不发，也不能因此泄露内部状态。
			// 但必须留痕：对外表现为"发送成功却永远收不到"，没有这条日志
			// 站长只会以为是垃圾箱问题，永远想不到是查库挂了。
			slog.Error("查询邮箱归属用户失败，跳过发信（对外仍按成功响应）",
				"error", err, "purpose", purpose, "client_ip", clientIP)
			shouldSend = false
		}
	}

	// ── 生成并落库（只存摘要，明文不落库）───────────────────────
	code, err := model.GenerateEmailCode()
	if err != nil {
		s.respondInternalError(c, "生成验证码失败")
		return
	}

	record := &model.EmailCode{
		Email:     email,
		Purpose:   purpose,
		CodeHash:  model.HashEmailCode(email, purpose, code),
		ExpiresAt: now.Add(model.EmailCodeTTL),
		RequestIP: clientIP,
	}
	if err := s.deps.EmailCodes.Create(ctx, record); err != nil {
		s.respondInternalError(c, "保存验证码失败")
		return
	}

	if shouldSend {
		subject, body := emailCodeMessage(settings.SiteName, code, purpose)
		if err := s.deps.Mailer.Send(ctx, email, subject, body); err != nil {
			// 发信失败的根因（SMTP 认证失败/域名错/被拒收）只在这里有：
			// 此前它既不回给用户也不进日志，站长面对"收不到验证码"的投诉零信息。
			// 邮箱地址不进日志（避免日志本身成为泄露面），只记用途与摘要。
			slog.Error("发送验证码邮件失败", "error", err,
				"purpose", purpose, "email_sha256", crypto.SHA256Hex(email),
				"client_ip", clientIP)
			// 关键：发信失败必须回滚记录，否则用户会因这条"从未收到"的记录
			// 被冷却 60 秒，表现为"点了重发却一直提示过于频繁"。
			// 回滚失败同样要留痕——否则用户被锁 60 秒却无因可查。
			if delErr := s.deps.EmailCodes.DeleteByID(ctx, record.ID); delErr != nil {
				slog.Error("回滚未发出的验证码记录失败（用户可能被冷却锁住）",
					"error", delErr, "purpose", purpose, "record_id", record.ID)
			}
			writeUserError(c, http.StatusBadGateway,
				"email.send_failed", oai.TypeServer, "email_send_failed")
			return
		}
	}

	c.JSON(http.StatusOK, emailCodeResponse{
		OK:        true,
		ExpiresIn: int(model.EmailCodeTTL.Seconds()),
		Cooldown:  int(model.EmailCodeResendCooldown.Seconds()),
		Message:   "验证码已发送，请查收邮件（含垃圾箱）",
	})
}

// emailCodeMessage 按用途选择验证码邮件的主题与正文。
func emailCodeMessage(siteName, code, purpose string) (subject, htmlBody string) {
	switch purpose {
	case model.EmailCodePurposeLogin:
		return mailer.LoginCodeEmail(siteName, code, model.EmailCodeTTL)
	case model.EmailCodePurposeReset:
		return mailer.ResetPasswordCodeEmail(siteName, code, model.EmailCodeTTL)
	default:
		return mailer.RegisterCodeEmail(siteName, code, model.EmailCodeTTL)
	}
}

// verifyAndConsumeRegisterEmailCode 校验注册验证码并一次性消费。
//
// 返回值：true 表示校验通过（调用方可继续注册）；false 表示已写出错误响应，
// 调用方必须立即 return —— 这样把"错误响应只写一次"的责任收敛在本函数内，
// 避免调用方重复写响应导致响应体拼接错乱。
func (s *Server) verifyAndConsumeRegisterEmailCode(c *gin.Context, email, code string) bool {
	return s.verifyAndConsumeEmailCode(c, email, code, model.EmailCodePurposeRegister)
}

// verifyAndConsumeEmailCode 校验并一次性消费指定用途的验证码。
//
// 三步闸门（顺序不能改）：
//  1. 取该邮箱该用途下最新且未消费的记录（不存在即失败）；
//  2. 原子累加尝试次数（条件自增，把"最多 5 次"变成并发安全的硬上限）；
//  3. 比对摘要，成功后 Consume 一次性消费。
func (s *Server) verifyAndConsumeEmailCode(c *gin.Context, email, code, purpose string) bool {
	ctx := c.Request.Context()

	if email == "" {
		writeUserError(c, http.StatusBadRequest,
			"email.required", oai.TypeInvalidRequest, "email_required")
		return false
	}
	if code == "" {
		writeUserError(c, http.StatusBadRequest,
			"email.code_required", oai.TypeInvalidRequest, "email_code_required")
		return false
	}

	record, err := s.deps.EmailCodes.LatestActive(ctx, email, purpose)
	if err != nil {
		if errors.Is(err, model.ErrEmailCodeNotFound) {
			writeUserError(c, http.StatusBadRequest,
				"email.code_invalid", oai.TypeInvalidRequest, "email_code_invalid")
			return false
		}
		s.respondInternalError(c, "查询验证码失败")
		return false
	}

	now := time.Now()
	if record.IsExpired(now) {
		writeUserError(c, http.StatusBadRequest,
			"email.code_expired", oai.TypeInvalidRequest, "email_code_expired")
		return false
	}

	// 尝试次数上限：6 位数字共 100 万种组合，限次后在线穷举不可行。
	//
	// 这里的自增刻意【先于】验证码比对：把"用掉一次机会"变成一次原子的条件自增，
	// 读阈值与自增之间就不再存在窗口——并发提交同一验证码时，条件自增本身就是闸门，
	// 多出来的请求会拿到 false 被直接拒绝，而不是"各自读到旧值后都放行"。
	// 比对成功时代码随即被 Consume 消费，所以多算的那一次尝试没有副作用。
	counted, err := s.deps.EmailCodes.IncreaseAttemptsWithin(ctx, record.ID, model.EmailCodeMaxAttempts)
	if err != nil {
		s.respondInternalError(c, "累加验证码尝试次数失败")
		return false
	}
	if !counted {
		writeUserError(c, http.StatusTooManyRequests,
			"email.code_attempts_exceeded", oai.TypeRateLimit, "email_code_attempts_exceeded")
		return false
	}

	if !model.VerifyEmailCode(record, code) {
		remaining := model.EmailCodeMaxAttempts - record.Attempts - 1
		if remaining < 0 {
			remaining = 0
		}
		writeUserError(c, http.StatusBadRequest,
			"email.code_mismatch", oai.TypeInvalidRequest, "email_code_mismatch", remaining)
		return false
	}

	// 一次性消费：并发提交同一验证码时，只有一条 UPDATE 会生效
	if err := s.deps.EmailCodes.Consume(ctx, record.ID, now); err != nil {
		if errors.Is(err, model.ErrEmailCodeNotFound) {
			writeUserError(c, http.StatusBadRequest,
				"email.code_used", oai.TypeInvalidRequest, "email_code_used")
			return false
		}
		s.respondInternalError(c, "消费验证码失败")
		return false
	}

	return true
}

// ---------------------------------------------------------------------------
// POST /api/auth/email-login
// ---------------------------------------------------------------------------

// emailLoginRequest 是邮箱验证码登录的请求体。
type emailLoginRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// handleEmailLogin 处理"用邮箱验证码登录"。
//
// 为什么值得做：本站用户的登录名是注册时自己填的，隔一段时间很容易忘；
// 而邮箱是唯一被验证过的身份凭据。用邮箱收码登录把"找回登录名 + 找回密码"
// 两步合并成一步，是流失用户回得来与否的关键。
func (s *Server) handleEmailLogin(c *gin.Context) {
	var req emailLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"request.invalid_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	email := model.NormalizeEmail(req.Email)
	if !s.verifyAndConsumeEmailCode(c, email, req.Code, model.EmailCodePurposeLogin) {
		return
	}

	ctx := c.Request.Context()
	user, err := s.deps.Users.GetByEmail(ctx, email)
	if err != nil {
		// 走到这里说明"码是对的但账号不见了"（发码时已确认存在），属极小概率竞态。
		// 仍按"凭据无效"回应，不泄露账号状态。
		writeUserError(c, http.StatusUnauthorized,
			"auth.invalid_credentials", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
		return
	}
	if !user.IsActive() {
		writeUserError(c, http.StatusForbidden,
			"auth.account_disabled", oai.TypePermission, oai.CodeTokenDisabled)
		return
	}

	s.issueSession(c, user, http.StatusOK)
}

// ---------------------------------------------------------------------------
// POST /api/auth/password-reset
// ---------------------------------------------------------------------------

// passwordResetRequest 是邮箱验证码重置密码的请求体。
type passwordResetRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// handleResetPassword 处理"用邮箱验证码重置密码"。
//
// 安全性取舍：
//   - 口令强度先于验证码校验（不消耗验证码的尝试次数），避免用户把 5 次
//     机会浪费在"新密码太短"这种一眼可见的问题上；
//   - 重置成功后**吊销该账号全部会话**：否则"改了密码"并不等于"把可能的
//     盗号者踢下线"，改密就失去了一半意义；
//   - 全程只认邮箱验证码，不暴露账号是否存在。
func (s *Server) handleResetPassword(c *gin.Context) {
	var req passwordResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"request.invalid_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	if err := crypto.ValidatePasswordStrength(req.Password); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_password")
		return
	}

	email := model.NormalizeEmail(req.Email)
	if !s.verifyAndConsumeEmailCode(c, email, req.Code, model.EmailCodePurposeReset) {
		return
	}

	ctx := c.Request.Context()
	user, err := s.deps.Users.GetByEmail(ctx, email)
	if err != nil {
		writeUserError(c, http.StatusUnauthorized,
			"auth.invalid_credentials", oai.TypeAuthentication, oai.CodeInvalidAPIKey)
		return
	}
	if !user.IsActive() {
		writeUserError(c, http.StatusForbidden,
			"auth.account_disabled", oai.TypePermission, oai.CodeTokenDisabled)
		return
	}

	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		s.respondInternalError(c, "计算口令哈希失败")
		return
	}
	if err := s.deps.Users.UpdatePassword(ctx, user.ID, hash); err != nil {
		s.respondInternalError(c, "更新口令失败")
		return
	}

	// 吊销全部会话。失败也不向用户报错（口令确已改成功，报错只会让人以为没改），
	// 但必须留下告警——它意味着"旧登录态可能仍然有效"。
	if err := s.deps.Sessions.DeleteByUserID(ctx, user.ID); err != nil {
		slog.Warn("重置密码后吊销会话失败，旧登录态可能仍然有效",
			"error", err, "user_id", user.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"message": "密码已重置，请使用新密码登录",
	})
}
