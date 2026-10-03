// 本文件承载登录路径的账号级防护：口令失败的连续计数、到阈值的定时锁定，
// 以及登录成功时记录来源（IP / UA）并对疑似异地登录发出提醒。
//
// 意图（Why）：
//
//	此前登录安全只有"进程内的 IP / 账号限流"三道闸，它们共同的问题是
//	【状态不落库】：重启即清零、多实例不共享、换个账号就能继续试。
//	本文件配合迁移 0043 把防护升级成账号级、持久化的形态——
//	连续 5 次口令错误即锁定 15 分钟（阈值与时长见 model 常量），
//	无论攻击者从多少出口 IP 试、进程重启多少次，计数都跟着账号走。
//
//	一处刻意的设计：【锁定只在"口令错误"时才生效】。
//	若把锁定判定放在"校验口令之前"，攻击者只要知道用户名就能
//	无限期把受害者锁在门外（每次 15 分钟的拒绝服务）；
//	放在"口令已确认错误"之后，则受害者本人用正确口令依然进得来，
//	被挡住的只有猜口令的人——可用性攻击面由此消失，而防爆破强度分毫未减。
//
// 流转（Flow）：
//
//	handleLogin / handleAdminLogin
//	  → 口令错误：rejectLoginIfLocked（锁定期则 429）→ noteLoginFailure（计数 +1，到阈值锁定）
//	  → 口令正确：issueSession → noteLoginSuccess（清零计数 + 记录来源 + 判定异地）
//	handleEmailLogin（验证码登录，无口令可错）：issueSession → noteLoginSuccess
//
// 扩展（Extend）：
//
//	调整阈值/锁定时长：改 model.LoginFailureLockThreshold / LoginFailureLockDuration 即可；
//	新增"锁定即通知站长"：在 noteLoginFailure 的锁定分支挂一条告警即可，本文件结构无需改。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/mailer"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/notify"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
	"github.com/LTZY-ACU/aqua-api/internal/server/middleware"
)

// loginAlertSendTimeout 是发送异地登录提醒邮件的超时上限。
//
// 为什么不用请求本身的 ctx：登录响应返回后请求 ctx 立即取消，
// 协程里那封信会被半路掐断；而这份提醒再晚几秒到也不影响登录本身，
// 因此它配自己独立的截止时间，与用户等待时间彻底解耦。
const loginAlertSendTimeout = 15 * time.Second

// rejectLoginIfLocked 在账号处于锁定期时拒绝本次登录尝试并写出响应。
//
// 调用位置必须是"口令已确认错误之后"，理由见本文件头部的"为什么这么放"。
// 返回 true 表示已写出响应，调用方应立即返回。
//
// 为什么不复用 401：锁定期的语义是"请不要再试"（429 Too Many Requests），
// 它告诉客户端退避而不是换口令；统一用 401 会让前端把用户锁在外面还提示"密码错误"，
// 用户只会越试越挫败。
func (s *Server) rejectLoginIfLocked(c *gin.Context, user *model.User, now time.Time) bool {
	if !user.IsLocked(now) {
		return false
	}
	// 剩余分钟向上取整：显示"请 15 分钟后重试"时不会出现"还剩 0 分钟"。
	minutes := int(user.LockRemain(now).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	c.Header("Retry-After", strconv.Itoa(int(user.LockRemain(now).Seconds())+1))
	writeUserError(c, http.StatusTooManyRequests,
		"auth.login_locked", oai.TypeRateLimit, "login_locked", minutes)
	return true
}

// noteLoginFailure 登记一次登录失败，并在达到阈值的地方留下告警日志。
//
// 为什么不返回教条的文案而直接走仓库：阈值与锁定时长都定义在 model 里，
// server 层只负责把"这一次失败"如实上报，判定逻辑保持单一出处。
func (s *Server) noteLoginFailure(c *gin.Context, user *model.User) {
	ctx := c.Request.Context()
	failed, lockedUntil, err := s.deps.Users.RegisterLoginFailure(ctx, user.ID, time.Now())
	if err != nil {
		// 失败登记的写失败不能改变响应语义（依然是"口令错误"），
		// 但必须留痕：否则"计数一直不涨、账号永远不锁"会变成看不见的洞。
		slog.Error("记录登录失败次数出错了（防爆破计数可能失效）",
			"error", err, "user_id", user.ID, "client_ip", middleware.ClientIP(c))
		return
	}
	if !lockedUntil.After(time.Now()) {
		return
	}
	// 同一账号每被锁一次只在这一次（刚好达阈值）打点：日志里能数出撞库频次，
	// 又因为后续请求都被锁挡在门外，不会把日志刷爆。
	slog.Warn("账号因连续登录失败被临时锁定",
		"user_id", user.ID, "username", user.Username,
		"attempts", failed, "locked_until", lockedUntil.Format(time.RFC3339),
		"client_ip", middleware.ClientIP(c))

	// 外发告警：账号被锁是"有人在撞这个账号"的直接信号。
	// 逐条发而不是只发一次：锁定期 15 分钟，同一个账号在窗口内被反复尝试
	// 恰恰说明攻击仍在继续——去重窗口（notify 内部按级别统一控制）不会掩盖它。
	s.deps.Notifier.Alert(notify.Alert{
		Key:   model.EventLoginLocked,
		Level: notify.LevelWarning,
		Title: fmt.Sprintf("账号「%s」因连续登录失败被临时锁定", user.Username),
		Detail: fmt.Sprintf("连续 %d 次登录失败，账号已锁定至 %s（北京时间）。",
			failed, lockedUntil.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")),
		DedupKey: fmt.Sprintf("user/%d", user.ID),
		Fields: []notify.Field{
			{Label: "账号", Value: user.Username},
			{Label: "来源 IP", Value: middleware.ClientIP(c)},
			{Label: "失败次数", Value: strconv.Itoa(int(failed))},
		},
	})
}

// noteLoginSuccess 记录一次成功登录的来源，并在来源发生明显变化时提醒用户。
//
// 异地判定刻意做成"只提醒、不阻断"：移动网络与家庭宽带的出口 IP 本来就会变，
// 硬绑 IP 会让用户在换个基站后被自己的账号踢下线；而一封提醒邮件既保留了
// "别人在用我的号"的可察觉性，又不会误伤正常用户。
func (s *Server) noteLoginSuccess(c *gin.Context, user *model.User) {
	ctx := c.Request.Context()
	ip := middleware.ClientIP(c)

	// 判据是"曾经成功登录过，且上一次的来源 IP 与本次不同"。
	// 首次登录（LastLoginIP 为空）不发信——那封邮件只会让人以为账号被盗。
	unfamiliar := !user.LastLoginAt.IsZero() && user.LastLoginIP != "" && user.LastLoginIP != ip

	if err := s.deps.Users.RecordLoginSuccess(ctx, user.ID, ip, time.Now()); err != nil {
		// 写失败不影响本次登录结果，但要留痕：来源记录缺失会让下一次异地判定失准。
		slog.Error("记录登录来源失败（异地提醒可能失准）",
			"error", err, "user_id", user.ID, "client_ip", ip)
		return
	}

	if unfamiliar {
		slog.Warn("检测到异地登录（已发提醒邮件）",
			"user_id", user.ID, "username", user.Username,
			"last_ip", user.LastLoginIP, "current_ip", ip)
		go s.sendLoginAlert(user, ip, c.Request.UserAgent())
	}
}

// sendLoginAlert 异步发送异地登录提醒邮件。
//
// 为什么必须异步：SMTP 会话最坏要走到 25 秒的截止时间（见 mailer 的 sendTimeout），
// 把它挂在登录响应里等于让"别人在试我密码"这件事拖垮受害者的登录体验。
func (s *Server) sendLoginAlert(user *model.User, ip, userAgent string) {
	if s.deps.Mailer == nil || !s.deps.Mailer.Configured() || user.Email == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginAlertSendTimeout)
	defer cancel()

	// 站点名拿不到时用固定兜底文案：这封信的价值在"提醒有人换了地方登录"，
	// 绝不能因为读不到设置就干脆不发。
	siteName := fallbackSiteName
	if settings, err := model.LoadSiteSettings(ctx, s.deps.Settings); err == nil && settings.SiteName != "" {
		siteName = settings.SiteName
	}

	subject, body := mailer.NewLocationEmail(siteName, user.Username, ip, userAgent, time.Now())
	if err := s.deps.Mailer.Send(ctx, user.Email, subject, body); err != nil {
		// 邮箱不进日志（防止日志成为另一份明文名单），用户名与 IP 足够定位。
		slog.Error("发送异地登录提醒邮件失败", "error", err,
			"user_id", user.ID, "username", user.Username, "client_ip", ip)
	}
}

// fallbackSiteName 是读不到站点设置时的兜底落款。
const fallbackSiteName = "AQUA-API"

// requestUserAgent 取本次请求的 User-Agent（供会话留痕）。
//
// 直接用标准取值方法而不是框架封装：UA 只是可选的留痕字段，
// 取不到就是空串，不值得为它引入额外的取值策略。
func requestUserAgent(c *gin.Context) string {
	return c.GetHeader("User-Agent")
}
