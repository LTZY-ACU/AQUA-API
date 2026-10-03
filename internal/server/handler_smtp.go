// 本文件实现「出站邮件（SMTP）通道」的后台配置接口。
//
// 意图（Why）：
//
//	注册邮箱验证码要靠邮件发出去，而邮件必须用站长自己的服务商账号。
//	此前这些参数只能通过环境变量注入（改完还要登录服务器重启），
//	而每个站长的服务商、发信域名、授权码都不一样——要求人人改环境变量不现实。
//	本文件提供三个接口，让站长在后台就能把邮件通道配好并当场验证：
//	  GET  /api/admin/smtp      读取当前配置（口令永不回传，只回"是否已配置"）
//	  PUT  /api/admin/smtp      保存并【立即热加载】到发送器（无需重启进程）
//	  POST /api/admin/smtp/test 向指定地址发一封测试邮件（"配完能不能发"当场验证）
//
// 安全约束（三条，务必保持）：
//  1. 口令【只进不出】：请求可以带 password 写入，但任何响应都不会包含它，
//     连密文也不回传；界面只能"重填"，不能"查看"。
//  2. 口令落库加密：由 store 层用 AQUA_APP_KEY 加密（见 0017 迁移与 smtp_repo.go）。
//     数据库备份流出时没有 APP_KEY 依然解不开。
//  3. 留空即不改：编辑时 password 留空表示"沿用已保存的口令"，
//     避免管理员只想改个端口却把口令清空（会导致发信全部失败）。
//
// 优先级规则（与 0023 迁移的注释一致）：
//
//	后台 enabled=true  → 以后台配置为准；
//	后台 enabled=false → 回退环境变量（AQUA_SMTP_*），这也是首次部署的预设。
//
// 流转（Flow）：
//
//	SettingsView（后台系统设置页）
//	  ├─ GET  → readStoredSMTP → 抹掉口令 → DTO（含当前生效端点与就绪状态）
//	  ├─ PUT  → 校验 → Save（加密落库）→ applySMTPConfig（热加载 mailer）→ 返回最新状态
//	  └─ POST /test → mailer.Send → 成功/失败原样反馈（不吞错误，便于站长自查）
//
// 扩展（Extend）：
//
//	新增 SMTP 参数时：在 model.SMTPSettings 加字段 + 建迁移加列
//	+ 同步 store/smtp_repo.go + 本文件的 DTO 与 applySMTPConfig（四处同步）。
package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// smtpConfigSource 表示当前生效的配置来自哪里，用于界面提示"现在用的是哪一套"。
const (
	smtpSourceDatabase = "database" // 后台配置（站长在界面里填的）
	smtpSourceEnv      = "env"      // 环境变量 / 内置默认值
	smtpSourceNone     = "none"     // 两处都没有，邮件功能不可用
)

// smtpSettingsDTO 是 SMTP 配置的对外表示。
//
// 注意：这里【没有】password 字段，也没有 password_cipher——
// 口令不对外输出是本接口的硬约束（见文件头安全约束）。
type smtpSettingsDTO struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	From      string `json:"from"`
	FromName  string `json:"from_name"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt int64  `json:"updated_at"`
	// PasswordSet 表示库里是否已保存口令（用于界面提示"留空则沿用已保存的口令"）。
	PasswordSet bool `json:"password_set"`
	// ValuesFromEnv 表示上面这组 host/port/username/from/from_name 是【从环境变量回填】的，
	// 而不是后台保存过的配置。
	//
	// 为什么必须回填：站长已经通过环境变量把邮件通道配好了，打开这个页面却看到一片空白，
	// 会直接得出"配置丢了/没生效"的结论（这正是修复前的实际投诉）。
	// 回填后他看到的就是"现在真正在用的那套参数"，只需要决定要不要搬到后台配置里。
	ValuesFromEnv bool `json:"values_from_env"`
	// EnvPasswordSet 表示环境变量里提供了口令。
	//
	// 口令无法（也不应）被读取回显，所以当 ValuesFromEnv 为真时，
	// 界面要提示"口令由环境变量提供，无法显示；若改用后台配置需重新填写"。
	EnvPasswordSet bool `json:"env_password_set"`

	// Source 是当前实际生效的配置来源：database / env / none。
	Source string `json:"source"`
	// Ready 表示"此刻真的能发信"（发送器认为配置完整）。
	//
	// 它不等于 Enabled：站长可能启用了后台配置但口令还没填，
	// 此时 Enabled=true 而 Ready=false，界面据此提示"还差什么"。
	Ready bool `json:"ready"`
	// Effective* 是当前生效的端点（不含口令），供界面显示"实际在用哪一套"。
	// 有了它，站长不必猜"我改了到底生效没有"。
	EffectiveHost   string `json:"effective_host"`
	EffectivePort   int    `json:"effective_port"`
	EffectiveFrom   string `json:"effective_from"`
	EffectiveSender string `json:"effective_sender"`
}

// smtpUpdateRequest 是保存 SMTP 配置的请求体。
type smtpUpdateRequest struct {
	Host     string `json:"host"`
	Port     *int   `json:"port"` // 指针：缺省时保留原值/默认值，避免被写成 0
	Username string `json:"username"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
	Enabled  *bool  `json:"enabled"`
	// Password 留空表示"沿用已保存的口令"（见文件头安全约束第 3 条）。
	Password string `json:"password"`
}

// smtpTestRequest 是发送测试邮件的请求体。
type smtpTestRequest struct {
	// To 是测试收件地址；留空时回落到发件地址（给自己发一封，最省事）。
	To string `json:"to"`
}

// readStoredSMTP 读取已保存的 SMTP 配置；从未保存过时返回 nil（不是错误）。
func (s *Server) readStoredSMTP(ctx context.Context) (*model.SMTPSettings, error) {
	if s.deps.SMTP == nil {
		return nil, nil
	}
	return s.deps.SMTP.Get(ctx)
}

// effectiveSMTPConfig 合成"当前应当生效"的 SMTP 发送配置。
//
// 规则（唯一真相，改动时必须同步 0023 迁移与本文件头注释）：
//   - 后台配置已启用且参数完整 → 用它；
//   - 否则 → 回退到环境变量/默认值（s.deps.SMTPBase）。
//
// 为什么以"后台启用"为分界，而不是"环境变量优先"：
// 站长在后台点"启用并保存"是一个明确意图；若被环境变量静默覆盖，
// 就会出现"我明明配好了却不生效"这类最难排查的问题。
func (s *Server) effectiveSMTPConfig(stored *model.SMTPSettings) config.SMTPConfig {
	if stored != nil && stored.Configured() {
		return config.SMTPConfig{
			Host:     strings.TrimSpace(stored.Host),
			Port:     stored.Port,
			Username: strings.TrimSpace(stored.Username),
			From:     strings.TrimSpace(stored.From),
			FromName: strings.TrimSpace(stored.FromName),
			Password: stored.Password,
		}
	}
	return s.deps.SMTPBase
}

// applySMTPConfig 把合成后的配置热加载到发送器。
//
// 为什么要在每次保存后调用：站长改完参数必须立刻生效。
// 若要求重启进程，他填完表单还得登录服务器操作，等于没解决原来的痛点。
func (s *Server) applySMTPConfig(stored *model.SMTPSettings) {
	if s.deps.Mailer == nil {
		return
	}
	s.deps.Mailer.Update(s.effectiveSMTPConfig(stored))
}

// buildSMTPDTO 组装给界面看的 SMTP 状态。
func (s *Server) buildSMTPDTO(stored *model.SMTPSettings) smtpSettingsDTO {
	effective := s.effectiveSMTPConfig(stored)

	source := smtpSourceNone
	switch {
	case stored != nil && stored.Configured():
		source = smtpSourceDatabase
	case effective.Configured():
		source = smtpSourceEnv
	}

	dto := smtpSettingsDTO{
		Source:          source,
		EffectiveHost:   effective.Host,
		EffectivePort:   effective.Port,
		EffectiveFrom:   effective.From,
		EffectiveSender: effective.FromName,
		// 环境变量的口令只暴露"有没有"，绝不回显内容
		EnvPasswordSet: strings.TrimSpace(s.deps.SMTPBase.Password) != "",
	}
	if s.deps.Mailer != nil {
		dto.Ready = s.deps.Mailer.Configured()
	}
	if stored != nil {
		dto.Host = stored.Host
		dto.Port = stored.Port
		dto.Username = stored.Username
		dto.From = stored.From
		dto.FromName = stored.FromName
		dto.Enabled = stored.Enabled
		dto.PasswordSet = strings.TrimSpace(stored.Password) != ""
		dto.UpdatedAt = stored.UpdatedAt.Unix()
		if dto.UpdatedAt < 0 {
			dto.UpdatedAt = 0
		}
	}
	// 后台没有保存过任何配置时，用"当前生效的参数"回填表单：
	// 站长因此能看到自己通过环境变量配的那套值，而不是一片空白。
	// 注意这只回填非口令字段——口令在任何路径下都不会被回显。
	if stored == nil {
		dto.Host = effective.Host
		dto.Port = effective.Port
		dto.Username = effective.Username
		dto.From = effective.From
		dto.FromName = effective.FromName
		dto.ValuesFromEnv = dto.Host != "" || dto.Username != "" || dto.From != ""
	}
	// 未保存过时给出一份"可直接编辑的初值"：端口用协议默认值，
	// 避免界面显示 0 让站长以为端口坏了。
	if dto.Port == 0 {
		dto.Port = config.DefaultSMTPPort
	}
	if dto.FromName == "" && stored == nil {
		dto.FromName = config.DefaultSMTPFromName
	}
	return dto
}

// handleGetSMTP 处理 GET /api/admin/smtp。
func (s *Server) handleGetSMTP(c *gin.Context) {
	stored, err := s.readStoredSMTP(c.Request.Context())
	if err != nil {
		s.respondInternalError(c, "读取邮件通道配置失败")
		return
	}
	c.JSON(http.StatusOK, s.buildSMTPDTO(stored))
}

// handleUpdateSMTP 处理 PUT /api/admin/smtp。
func (s *Server) handleUpdateSMTP(c *gin.Context) {
	if s.deps.SMTP == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件配置模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	var req smtpUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	existing, err := s.readStoredSMTP(ctx)
	if err != nil {
		s.respondInternalError(c, "读取邮件通道配置失败")
		return
	}

	settings := &model.SMTPSettings{
		Host:     strings.TrimSpace(req.Host),
		Port:     config.DefaultSMTPPort,
		Username: strings.TrimSpace(req.Username),
		From:     strings.TrimSpace(req.From),
		FromName: strings.TrimSpace(req.FromName),
		Enabled:  true,
	}
	if req.Port != nil {
		settings.Port = *req.Port
	}
	if req.Enabled != nil {
		settings.Enabled = *req.Enabled
	}
	// 口令留空 = 沿用已保存的。这是本接口最容易被误用的地方：
	// 界面出于安全不回传口令，因此"编辑后直接保存"必然带来空口令，
	// 若不沿用就会把原本好好的通道改成不可用。
	settings.Password = strings.TrimSpace(req.Password)
	if settings.Password == "" && existing != nil {
		settings.Password = existing.Password
	}
	// 库里没有口令、请求也没带口令：这是"想启用后台配置但没填口令"。
	// 单独给出可操作的提示，并说明环境变量里的口令无法被读取复用——
	// 否则站长会以为"环境变量里明明有口令，为什么这里说没有"。
	if settings.Enabled && settings.Password == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"请填写 SMTP 登录口令（授权码）。为安全起见，环境变量中的口令不会被读取或复用，"+
				"若要改用后台配置，需要把口令重新填写一次。",
			oai.TypeInvalidRequest, "smtp_password_required")
		return
	}

	if err := settings.Validate(); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_smtp_settings")
		return
	}

	if err := s.deps.SMTP.Save(ctx, settings); err != nil {
		s.respondInternalError(c, "保存邮件通道配置失败")
		return
	}

	// 保存成功后立刻热加载，让"改完立即生效"成立。
	s.applySMTPConfig(settings)

	saved, err := s.readStoredSMTP(ctx)
	if err != nil {
		s.respondInternalError(c, "读取保存后的邮件通道配置失败")
		return
	}
	c.JSON(http.StatusOK, s.buildSMTPDTO(saved))
}

// handleTestSMTP 处理 POST /api/admin/smtp/test：向指定地址发一封测试邮件。
//
// 为什么需要它：邮件通道的失败模式很多（端口被云厂商封、授权码错、
// 发信地址未在服务商备案…），这些只有真的发一次才暴露。
// 让站长在配置页当场验证，比"注册个账号试试收不收得到验证码"高效得多。
func (s *Server) handleTestSMTP(c *gin.Context) {
	if s.deps.Mailer == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件发送器未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	var req smtpTestRequest
	// 允许空请求体：此时默认给自己（发件地址）发一封。
	_ = c.ShouldBindJSON(&req)

	to := strings.TrimSpace(req.To)
	if to == "" {
		to = strings.TrimSpace(s.deps.Mailer.From())
	}
	if to == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"请填写测试收件地址（当前邮件通道还没有发件地址）",
			oai.TypeInvalidRequest, "invalid_recipient")
		return
	}
	if !strings.Contains(to, "@") {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"收件地址格式不正确（缺少 @）", oai.TypeInvalidRequest, "invalid_recipient")
		return
	}

	if !s.deps.Mailer.Configured() {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"邮件通道尚未配置完整（需要服务器地址 / 账号 / 口令 / 发件地址）",
			oai.TypeInvalidRequest, "smtp_not_configured")
		return
	}

	subject := "AQUA-API 邮件通道测试"
	body := buildSMTPTestHTML(time.Now())

	// 用独立超时而非请求 context：SMTP 会话最长 25 秒，
	// 若沿用请求 context，客户端提前断开会让这次测试"看起来失败但其实发出了"。
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	if err := s.deps.Mailer.Send(ctx, to, subject, body); err != nil {
		// 原样回传失败原因：这里正是需要细节的场景（错误信息不含口令，
		// mailer 内部已把认证失败等归类为可读文案）。
		oai.WriteError(c.Writer, http.StatusBadGateway,
			"发送测试邮件失败："+err.Error(), oai.TypeServer, "smtp_send_failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "to": to})
}

// buildSMTPTestHTML 构造测试邮件的正文。
//
// 刻意保持极简：它的唯一用途是证明"这条通道能把信送到"，
// 因此内容只需要让收信人一眼确认"这是我自己的站点发的"。
func buildSMTPTestHTML(now time.Time) string {
	timestamp := now.Format("2006-01-02 15:04:05")
	return `<!DOCTYPE html><html><body style="font-family:system-ui,-apple-system,'Segoe UI',sans-serif;line-height:1.7;color:#1f2937">
<p>这是一封来自 <strong>AQUA-API</strong> 的测试邮件。</p>
<p>你收到它，说明站点的邮件通道配置正确，注册邮箱验证码可以正常发出。</p>
<p style="color:#6b7280;font-size:13px">发送时间：` + timestamp + `</p>
</body></html>`
}
