// 本文件实现「使用 QIU 科技账号登录」。
//
// 意图（Why）：
//
//	让用户不必在本站再注册一套账号密码：点一次按钮 → 去 QIU 的页面确认 →
//	本站自动登录（首次自动建号，之后每次回到同一个号）。
//	关键的可靠性在于"之后每次回到同一个号"：靠 user_external_accounts 表
//	把 QIU 的 user.id 与本站 uid 绑起来，而不是每次都新建一个空账号。
//
// 流程（三段式，与 QIU 开放接口一一对应）：
//
//	① POST /api/auth/qiu/start       → 调 /startlogin?app=XXX 取 {task_id, url}
//	   （前端把 url 开给用户在 QIU 侧点确认）
//	② GET  /api/auth/qiu/status/:id  → 轮询 /readlogin/{task_id}
//	   pending 等、denied/expired 如实回、ok 则签发本站会话
//	③ POST /api/auth/qiu/bind        → 已登录用户把当前账号与某个 task 的身份绑定
//	   （给"早已在本站有号"的人一条归并路径，不必开新号）
//
// 安全约定（三条，都不是可选的美化）：
//
//  1. 本地账号的匹配【只认 external_id】。昵称、外部用户名都可能被对方平台的用户
//     随意修改，拿它们去猜本地账号等于把账号接管权交出去
//     （把自己的昵称改成别人的本站用户名即可登录其账号）。
//  2. task_id 必须过白名单正则再拼进 URL：它直接参与构造请求路径，
//     放行含斜杠或 ../ 的值就等于把请求路径的控制权交给调用方。
//  3. 出站请求统一走 netguard 护栏 + 独立超时：第三方地址由配置提供，
//     护栏防止它被指向云厂商元数据地址；超时防止对方缓慢拖垮本进程。
//
// 扩展（Extend）：
//
//	接入第二个第三方平台：在 model 加一个 provider 常量，并把本文件的接口地址
//	拼装部分抽出即可（绑定表与 README 都不用改）。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/netguard"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
	"github.com/LTZY-ACU/aqua-api/internal/server/middleware"
)

// qiuPollLimit 是同一来源 IP 在 5 分钟窗口内允许的轮询次数。
//
// 取值 240（≈ 每 1.25 秒一次）：轮询的推荐节奏是每 2 秒一次，
// 一次授权最坏要等几分钟才有人点确认，配额必须显著高于这个量；
// 但它终究要有个上限——每一次轮询都会向第三方平台发出一次真实请求，
// 没有上限的接口可以直接被用来放大流量到别人家服务器。
const qiuPollLimit = 240

// taskIDPattern 是 task_id 的白名单。
//
// 取值依据：QIU 的任务号是它自己生成的短标识（字母数字与少量符号）。
// 这里收紧到 [A-Za-z0-9_-]{1,64}，既能容纳常见的 UUID/随机串形态，
// 又天然排除了斜杠、点号与百分号——它们正是拼进 URL 路径时最危险的三类字符。
var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// qiuMaxResponseBody 是读取 QIU 响应的上限。
//
// 第三方响应不由我们控制，读之前必须设限：一个"忘了关 BufferedReader"的对方接口
// 若返回几 GB 内容，会在我们的进程里完整展开。
const qiuMaxResponseBody = 1 << 20 // 1 MiB

// qiuStartResponse 是创建登录任务的响应。
type qiuStartResponse struct {
	TaskID string `json:"task_id"`
	URL    string `json:"url"`
}

// qiuUser 是 QIU 返回的用户身份片段。
type qiuUser struct {
	ID       any    `json:"id"` // 可能是数字也可能是字符串，两种形态都必须能接住
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

// qiuReadResponse 是轮询任务的响应。
//
// Status 取值：pending（等待用户确认）/ ok / denied / expired。
// Token 只在 ok 时有意义（调用对方业务接口的凭证，本站不需要保存它 ——
// 少存一份凭证就少一份泄露面）。
type qiuReadResponse struct {
	Status string  `json:"status"`
	User   qiuUser `json:"user"`
	Token  string  `json:"token"`
}

// ── 公共 guard ──────────────────────────────────────────────────────────────

// qiuReady 检查第三方登录是否可用，不可用则写出响应并返回 false。
//
// 关闭时返回 404 而不是 403：这是一个"本站点不存在的功能"，
// 403 反而会告诉探测者"这个功能存在，只是被关了"。
func (s *Server) qiuReady(c *gin.Context) bool {
	if !s.deps.Config.QIU.Enabled || s.deps.ExternalAccounts == nil {
		oai.WriteError(c.Writer, http.StatusNotFound, "该站点未开放此登录方式",
			oai.TypeInvalidRequest, "oauth_not_found")
		return false
	}
	return true
}

// qiuClient 构造用于访问 QIU 接口的 HTTP 客户端。
//
// 每次新建而非复用长连接：第三方登录是低频操作（一次登录两三个请求），
// 短连接避免了连接池长期空闲带来的各种中间设备断连问题。
// 护栏 DialControl 与转发链路同源：哪怕配置被改成指向云元数据地址也连不出去。
func qiuClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: netguard.DialControl}
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
			// 第三方登录一次只要一个连接，连接池刻意开到最小
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       15 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// qiuTimeout 取配置的超时（非正值回退到默认值）。
func (s *Server) qiuTimeout() time.Duration {
	if sec := s.deps.Config.QIU.TimeoutSeconds; sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return time.Duration(config.DefaultQIUTimeoutSeconds) * time.Second
}

// qiuBaseURL 取并检查配置里的根地址。
//
// 为什么要求 http(s) 且不带路径片段：根地址要和 /startlogin 做字符串拼接，
// 一旦允许任意 scheme（file://、gopher://）或路径，就从"调一个 HTTPS 接口"
// 变成了"可以被配置操控的出站请求面"。
func (s *Server) qiuBaseURL() (string, error) {
	raw := strings.TrimSpace(s.deps.Config.QIU.BaseURL)
	if raw == "" {
		raw = config.DefaultQIUBaseURL
	}
	raw = strings.TrimRight(raw, "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("配置的 QIU 接口地址无效: %q", raw)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("配置的 QIU 接口地址 scheme 必须是 http 或 https: %q", raw)
	}
	return parsed.String(), nil
}

// qiuAppName 取对外展示的身份名称，留空时回退到站点名。
func (s *Server) qiuAppName(ctx context.Context) string {
	if name := strings.TrimSpace(s.deps.Config.QIU.AppName); name != "" {
		return name
	}
	if settings, err := model.LoadSiteSettings(ctx, s.deps.Settings); err == nil && settings.SiteName != "" {
		return settings.SiteName
	}
	return fallbackSiteName
}

// callQIU 向 QIU 发起一次 GET 请求并解析 JSON 响应。
func callQIU(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s 失败: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, qiuMaxResponseBody))
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("对方返回 HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}

// ── ① 创建登录任务 ──────────────────────────────────────────────────────────

// handleQIULoginStart 处理 POST /api/auth/qiu/start。
//
// 响应里的 url 需要由前端开给用户在 QIU 侧点确认；本站不代跳，
// 因为跨域跳转会把"用户在哪点确认"这件事的可见性从浏览器地址栏里抹掉。
func (s *Server) handleQIULoginStart(c *gin.Context) {
	if !s.qiuReady(c) {
		return
	}
	ctx := c.Request.Context()

	base, err := s.qiuBaseURL()
	if err != nil {
		slog.Error("QIU 登录：接口地址配置非法", "error", err)
		s.respondInternalError(c, "第三方登录配置有误", err)
		return
	}

	endpoint := fmt.Sprintf("%s/startlogin?app=%s", base,
		url.QueryEscape(s.qiuAppName(ctx)))

	var payload struct {
		TaskID string `json:"task_id"`
		URL    string `json:"url"`
	}
	if err := callQIU(ctx, qiuClient(s.qiuTimeout()), endpoint, &payload); err != nil {
		slog.Error("QIU 登录：创建登录任务失败", "error", err)
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务暂时不可用，请稍后重试",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}
	if payload.TaskID == "" || payload.URL == "" {
		slog.Error("QIU 登录：对方返回的响应缺少 task_id 或 url")
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务返回了不完整的数据",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}
	// 只放行 http/https 绝对地址：这个 URL 会被前端直接交给用户的浏览器打开，
	// 放行 javascript: 之类伪协议等于让第三方在我们的登录页上执行脚本。
	if u, err := url.Parse(payload.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		slog.Error("QIU 登录：对方返回的确认页地址不合法", "url_scheme", payload.URL)
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务返回了不合法的地址",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}

	c.JSON(http.StatusOK, qiuStartResponse{TaskID: payload.TaskID, URL: payload.URL})
}

// ── ② 轮询任务结果 ──────────────────────────────────────────────────────────

// handleQIULoginStatus 处理 GET /api/auth/qiu/status/:task_id。
//
// 轮询由前端驱动（每 2 秒一次），本站只做转发与结论处理：
// 不在服务端起轮询任务，是因为"用户是否愿意等多久"只有浏览器那边知道，
// 服务端轮询会变成一堆没人认领的 goroutine。
//
// 同一条链接服务两种场景，靠"调用方是否已登录"区分（路由挂了可选会话中间件）：
//   - 未登录：登录流程 —— 解析身份、按需建号、下发会话；
//   - 已登录：绑定流程 —— 只回报"对方已确认"，不碰任何账号与会话。
func (s *Server) handleQIULoginStatus(c *gin.Context) {
	if !s.qiuReady(c) {
		return
	}
	ctx := c.Request.Context()

	taskID := strings.TrimSpace(c.Param("task_id"))
	if !taskIDPattern.MatchString(taskID) {
		oai.WriteError(c.Writer, http.StatusBadRequest, "任务编号格式不正确",
			oai.TypeInvalidRequest, "invalid_task_id")
		return
	}

	base, err := s.qiuBaseURL()
	if err != nil {
		s.respondInternalError(c, "第三方登录配置有误", err)
		return
	}

	var payload qiuReadResponse
	endpoint := fmt.Sprintf("%s/readlogin/%s", base, taskID)
	if err := callQIU(ctx, qiuClient(s.qiuTimeout()), endpoint, &payload); err != nil {
		slog.Error("QIU 登录：查询登录任务失败", "error", err, "task_id", taskID)
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务暂时不可用，请稍后重试",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}

	switch payload.Status {
	case "pending":
		c.JSON(http.StatusOK, gin.H{"status": "pending"})
		return
	case "denied":
		c.JSON(http.StatusOK, gin.H{"status": "denied"})
		return
	case "expired":
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
		return
	case "ok":
		// 落到下面统一处理：拿到身份后要么登录已有账号，要么新建账号。
	default:
		// 未知状态按"还在等待"处理而不是报错：对方平台将来加一个新状态时，
		// 用户会卡在一个永远转圈的页面上；而等待是最安全的默认动作。
		slog.Warn("QIU 登录：对方返回未知状态", "status", payload.Status, "task_id", taskID)
		c.JSON(http.StatusOK, gin.H{"status": "pending"})
		return
	}

	externalID := qiuExternalID(payload.User.ID)
	if externalID == "" {
		slog.Error("QIU 登录：对方确认后未返回用户 id", "task_id", taskID)
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务未返回用户标识",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}

	// 已登录的人查的是"我这次授权点完没有"（绑定流程），不是"我要登录"。
	//
	// 这里绝不能往下走建号 / 发会话：一旦发了，点「绑定」的用户会被静默切到
	// 另一个账号（甚至是一个刚被凭空建出来的空号），而他自己的账号并未真正
	// 完成绑定。绑定关系由 POST /auth/qiu/bind 凭【已登录会话】来建立，
	// 本分支只回报"对方已确认"与用于展示的身份信息。
	if _, authed := middleware.CurrentUser(c); authed {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"profile": gin.H{
				"username": truncateRunes(payload.User.Username, 64),
				"nickname": truncateRunes(payload.User.Nickname, 64),
			},
		})
		return
	}

	user, err := s.resolveQIUUser(c, externalID, payload.User)
	if err != nil {
		// resolveQIUUser 已经写好响应；这里不再重复写，避免二次 c.JSON。
		return
	}

	token, expiresAt, ok := s.createSession(c, user)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"session_token": token,
		"expires_at":    expiresAt.Unix(),
		"user":          toUserDTO(user),
	})
}

// resolveQIUUser 按 QIU 身份找到（或建立）本地账号。
//
// 返回 (user, nil) 表示可以继续签发会话；返回错误表示已经写好响应，
// 调用方必须直接 return（不写第二次响应）。
func (s *Server) resolveQIUUser(c *gin.Context, externalID string, profile qiuUser) (*model.User, error) {
	ctx := c.Request.Context()

	bound, err := s.deps.ExternalAccounts.GetByExternalID(ctx, model.ExternalProviderQIU, externalID)
	switch {
	case err == nil:
		user, getErr := s.deps.Users.GetByID(ctx, bound.UserID)
		if getErr != nil {
			s.respondInternalError(c, "查询账号失败", getErr)
			return nil, getErr
		}
		if !user.IsActive() {
			writeUserError(c, http.StatusForbidden,
				"auth.account_disabled", oai.TypePermission, oai.CodeTokenDisabled)
			return nil, errors.New("账号已停用")
		}
		return user, nil
	case errors.Is(err, model.ErrExternalAccountNotFound):
		// 首次用这个第三方账号登录本站：走下面的建号流程。
	default:
		s.respondInternalError(c, "查询第三方账号绑定失败", err)
		return nil, err
	}

	return s.createQIUUser(c, externalID, profile)
}

// createQIUUser 为首次登录的第三方账号建立本站账号并落绑定关系。
func (s *Server) createQIUUser(c *gin.Context, externalID string, profile qiuUser) (*model.User, error) {
	ctx := c.Request.Context()

	settings, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败", err)
		return nil, err
	}
	// 关闭注册时不允许新建账号，但已绑定的老账号照常登录：
	// 否则第三方登录会变成绕过"关闭注册"的后门。
	if !settings.RegistrationEnabled {
		writeUserError(c, http.StatusForbidden,
			"auth.register_disabled", oai.TypePermission, "registration_disabled")
		return nil, errors.New("站点未开放注册")
	}

	// 第三方账号本身不需要口令，但本站账号必须有口令：给它一个随机且无人知道的值，
	// 避免"空口令登录"这类影子通道存在（将来若有人误开放口令登录入口也进不去）。
	hash, err := crypto.HashPassword(randomPasswordForExternal())
	if err != nil {
		s.respondInternalError(c, "生成账号凭据失败", err)
		return nil, err
	}

	baseName := suggestQIUUsername(profile.Username, profile.Nickname, externalID)
	user, err := s.insertQIUUserWithRetry(ctx, baseName, hash, settings.DefaultUserQuota)
	if err != nil {
		s.respondInternalError(c, "创建账号失败", err)
		return nil, err
	}

	item := &model.ExternalAccount{
		UserID:           user.ID,
		Provider:         model.ExternalProviderQIU,
		ExternalID:       externalID,
		ExternalUsername: truncateRunes(profile.Username, 64),
		Nickname:         truncateRunes(profile.Nickname, 64),
	}
	if err := s.deps.ExternalAccounts.Create(ctx, item); err != nil {
		// 绑定失败必须显式失败而不是"登录成功但下次又不认识他了"：
		// 后者会让用户第二次登录拿到一个新号，额度与令牌全部对不上。
		slog.Error("QIU 登录：写入第三方账号绑定失败", "error", err, "user_id", user.ID)
		s.respondInternalError(c, "完成第三方账号绑定失败", err)
		return nil, err
	}

	slog.Info("QIU 账号首次登录，已自动建号并绑定",
		"user_id", user.ID, "username", user.Username)
	return user, nil
}

// insertQIUUserWithRetry 创建账号；用户名冲突时换一个后缀重试。
//
// 为什么必须重试：第三方昵称与用户名完全可能重名（大量用户叫同一个名字），
// 直接返回失败会让"同名用户根本无法使用第三方登录"。
func (s *Server) insertQIUUserWithRetry(ctx context.Context, baseName, passwordHash string,
	quota int64) (*model.User, error) {
	const maxAttempts = 10
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		name := baseName
		if i > 0 {
			name = fmt.Sprintf("%s%d", baseName, i+1)
		}
		user := &model.User{
			Username:     name,
			PasswordHash: passwordHash,
			Role:         model.UserRoleUser,
			Status:       model.UserStatusEnabled,
			Quota:        quota,
		}
		if err := s.deps.Users.Create(ctx, user); err != nil {
			lastErr = err
			if errors.Is(err, model.ErrUsernameTaken) {
				continue
			}
			return nil, err
		}
		return user, nil
	}
	return nil, lastErr
}

// ── ③ 已登录用户主动绑定 ────────────────────────────────────────────────────

// bindQIURequest 是绑定请求体。
type bindQIURequest struct {
	TaskID string `json:"task_id"`
}

// handleQIUBind 处理 POST /api/auth/qiu/bind（已登录用户绑定第三方账号）。
//
// 存在的意义：早已在本站有账号的人不该被迫再多一个新号。
// 由【已登录会话】证明本地账号归属，因此不存在"抢绑别人账号"的可能——
// 这正是"只能靠 verified 会话而不能靠昵称匹配"的原因。
func (s *Server) handleQIUBind(c *gin.Context) {
	if !s.qiuReady(c) {
		return
	}
	ctx := c.Request.Context()

	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}

	var req bindQIURequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeUserError(c, http.StatusBadRequest,
			"request.invalid_json", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	taskID := strings.TrimSpace(req.TaskID)
	if !taskIDPattern.MatchString(taskID) {
		oai.WriteError(c.Writer, http.StatusBadRequest, "任务编号格式不正确",
			oai.TypeInvalidRequest, "invalid_task_id")
		return
	}

	base, err := s.qiuBaseURL()
	if err != nil {
		s.respondInternalError(c, "第三方登录配置有误", err)
		return
	}

	var payload qiuReadResponse
	if err := callQIU(ctx, qiuClient(s.qiuTimeout()),
		fmt.Sprintf("%s/readlogin/%s", base, taskID), &payload); err != nil {
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务暂时不可用，请稍后重试",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}
	if payload.Status != "ok" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "该次授权尚未完成，请先在对方页面确认",
			oai.TypeInvalidRequest, "oauth_not_confirmed")
		return
	}
	externalID := qiuExternalID(payload.User.ID)
	if externalID == "" {
		oai.WriteError(c.Writer, http.StatusBadGateway, "第三方登录服务未返回用户标识",
			oai.TypeServer, "oauth_upstream_unavailable")
		return
	}

	item := &model.ExternalAccount{
		UserID:           user.ID,
		Provider:         model.ExternalProviderQIU,
		ExternalID:       externalID,
		ExternalUsername: truncateRunes(payload.User.Username, 64),
		Nickname:         truncateRunes(payload.User.Nickname, 64),
	}
	if err := s.deps.ExternalAccounts.Create(ctx, item); err != nil {
		if errors.Is(err, model.ErrExternalAccountTaken) {
			oai.WriteError(c.Writer, http.StatusConflict, "该第三方账号已绑定到别的本站账号",
				oai.TypeInvalidRequest, "oauth_already_bound")
			return
		}
		s.respondInternalError(c, "绑定第三方账号失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "username": item.ExternalUsername})
}

// ── 辅助 ────────────────────────────────────────────────────────────────────

// qiuExternalID 把对方返回的 user.id 统一成字符串。
//
// 必须兼容数字与字符串两种形态：JSON 里 id 常被写成数字，
// 而 SQLite 里的 external_id 是文本；若只按字符串解，数字形态会丢成空串，
// 于是"所有数字 id 的用户"都被判成同一个（空 id）——这是最危险的串号。
func qiuExternalID(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case float32:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

// suggestQIUUsername 生成一个可用的本站用户名。
//
// 优先级：昵称/用户名 → 加 QIU 前缀兜底（qiu_<external_id>）。
// 只保留字母、数字与下划线：这三类在任何一处用户名校验规则里都是安全的，
// 中文与 emoji 昵称（很常见）在只认 ASCII 的规则下会被拒掉，故先行剔除。
func suggestQIUUsername(username, nickname, externalID string) string {
	base := sanitizeQIUUsername(username)
	if base == "" {
		base = sanitizeQIUUsername(nickname)
	}
	if base == "" {
		base = "qiu_" + sanitizeQIUUsername(externalID)
	}
	if base == "" {
		base = fallbackQIUUsername
	}
	// 过短的用户名在"查找用户""邀请关系"等地方会显得像垃圾账号，
	// 统一补齐到合理长度。
	for len(base) < 3 {
		base += "0"
	}
	return truncateRunes(base, 32)
}

const fallbackQIUUsername = "qiu_user"

// sanitizeQIUUsername 只保留字母、数字与下划线。
func sanitizeQIUUsername(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// randomPasswordForExternal 生成一个无人知晓的高强度随机口令。
//
// 为什么不用固定串或空口令：本站账号行的 password_hash 是必填列，
// 若给一个可预测的固定值，等于给所有第三方账号留了一条统一的口令入口
// （将来任何一处口令校验的疏漏都能一次穿透全部账号）。
//
// 生成失败时也不 panic：随机数不可用是极少数运行环境才会遇到的问题，
// 用时间戳兜底虽然熵值不够，但经过一次 HashPassword 之后依然无人可知，
// 比直接让登录流程崩掉要好得多。
func randomPasswordForExternal() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "aqua-external-fallback-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "aqua-external-" + hex.EncodeToString(raw)
}

// ── ④ 查看自己绑了哪些第三方账号 ────────────────────────────────────────────

// externalAccountDTO 是"我的第三方账号"列表项。
//
// 只下发展示所需字段：external_id 是内部匹配用的主键，
// 不该出现在门户页的响应里（它一旦与其它数据拼在一起就可能成为关联线索）。
type externalAccountDTO struct {
	Provider     string `json:"provider"`
	ProviderText string `json:"provider_text"`
	AccountName  string `json:"account_name"`
	Nickname     string `json:"nickname"`
	BoundAt      int64  `json:"bound_at"`
	// LoginEnabled 表示该第三方登录当前是否开放。
	//
	// 前端据此决定"还能不能用它登录"——总开关关掉后，
	// 已绑定的用户会看到"该登录方式已停用"，而不是点了没反应。
	LoginEnabled bool `json:"login_enabled"`
}

// handleMyExternalAccounts 处理 GET /api/user/external-accounts。
//
// 刻意【不提供解绑接口】：第三方自动建号的账号没有已知口令（随机生成、无人知晓），
// 一旦解绑又退出登录，这个账号就再也进不来了——那是比"绑错了"严重得多的事故。
// 真要支持解绑，必须先保证账号至少还有一种可用的登录方式。
func (s *Server) handleMyExternalAccounts(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	if s.deps.ExternalAccounts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"第三方账号模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	items, err := s.deps.ExternalAccounts.ListByUser(c.Request.Context(), user.ID)
	if err != nil {
		s.respondInternalError(c, "查询第三方账号绑定失败", err)
		return
	}

	list := make([]externalAccountDTO, 0, len(items))
	for _, item := range items {
		name := item.Nickname
		if name == "" {
			name = item.ExternalUsername
		}
		list = append(list, externalAccountDTO{
			Provider:     item.Provider.String(),
			ProviderText: item.Provider.DisplayName(),
			AccountName:  item.ExternalUsername,
			Nickname:     name,
			BoundAt:      unixOrZero(item.CreatedAt),
			LoginEnabled: s.deps.Config.QIU.Enabled,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": list})
}
