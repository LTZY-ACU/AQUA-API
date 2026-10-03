// 本文件实现「全站通知邮件（群发）」的后台接口。
//
// 意图（Why）：
//
//	群发有三个不可逆的后果，接口设计上必须比普通 CRUD 更保守：
//	  1) 发出即到达 —— 内容错了只能再发一封更正，等于第二次打扰全体用户；
//	  2) 重复即投诉 —— 同一批人收到两封相同邮件，直接推高垃圾邮件评分；
//	  3) 量大门槛 —— 能力越顺手越要拦得住，因此创建接口要求显式 confirm，
//	     并提供"先发给自己预览"与"中途停止"两条安全通道。
//
//	三个入口的分工：
//	  preview：把渲染好的同一份内容发给【当前管理员自己】——先看看长什么样；
//	  create ：创建批次 + 入队收件人 + 后台开始发送（要求 confirm=true）；
//	  cancel ：停止一个正在发送的批次，未发的人不会再发（已发的人不撤回）。
//
// 流转（Flow）：
//
//	后台页面 → preview（发给管理员自己，验证观感）
//	        → create（写快照 → 入队 → broadcast.Sender.Start）
//	        → list / recipients（看进度与失败明细）
//	        → cancel（需要时中止）
//
// 扩展（Extend）：
//
//	新增通知模板：只在 internal/mailer/template.go 的目录里加一条 + 写渲染函数，
//	本文件的 preview / create 会自动支持（它们按模板键走同一套渲染入口）。
package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/mailer"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
	"github.com/LTZY-ACU/aqua-api/internal/server/middleware"
)

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// broadcastTemplateDTO 是可选通知模板的目录项。
type broadcastTemplateDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// emailBroadcastDTO 是群发批次的对外表示。
//
// 注意这里【不含正文】：后台列表要展示的是进度与结果，
// 正文是几 KB 的 HTML，放进列表响应只会让页面变慢且毫无用处。
type emailBroadcastDTO struct {
	ID        uint64 `json:"id"`
	Template  string `json:"template"`
	Subject   string `json:"subject"`
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Sent      int    `json:"sent"`
	Failed    int    `json:"failed"`
	Pending   int    `json:"pending"`
	CreatedBy uint64 `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	StartedAt int64  `json:"started_at"`
	// FinishedAt 为 0 表示尚未结束（进行中或从未开始）。
	FinishedAt int64 `json:"finished_at"`
}

// toEmailBroadcastDTO 把领域模型转为对外 DTO。
func toEmailBroadcastDTO(item *model.EmailBroadcast) emailBroadcastDTO {
	if item == nil {
		return emailBroadcastDTO{}
	}
	pending := item.Total - item.Sent - item.Failed
	if pending < 0 {
		pending = 0
	}
	return emailBroadcastDTO{
		ID:         item.ID,
		Template:   item.Template,
		Subject:    item.Subject,
		Status:     string(item.Status),
		Total:      item.Total,
		Sent:       item.Sent,
		Failed:     item.Failed,
		Pending:    pending,
		CreatedBy:  item.CreatedBy,
		CreatedAt:  unixOrZero(item.CreatedAt),
		UpdatedAt:  unixOrZero(item.UpdatedAt),
		StartedAt:  unixOrZero(item.StartedAt),
		FinishedAt: unixOrZero(item.FinishedAt),
	}
}

// broadcastRecipientDTO 是收件人明细分项（失败明细靠它核对）。
type broadcastRecipientDTO struct {
	ID     uint64 `json:"id"`
	UserID uint64 `json:"user_id"`
	Email  string `json:"email"`
	Status string `json:"status"`
	Error  string `json:"error"`
	SentAt int64  `json:"sent_at"`
}

// broadcastRequest 是创建批次的请求体。
type broadcastRequest struct {
	// Template 是模板键（见 GET /api/admin/broadcast-templates）。
	Template string `json:"template"`
	// Confirm 必须显式为 true：全站群发不可撤回，要求调用方明确表达意图，
	// 避免前端某个"顺手复用"的提交按钮把邮件发给全体用户。
	Confirm bool `json:"confirm"`
}

// broadcastPreviewRequest 是预览请求体。
type broadcastPreviewRequest struct {
	Template string `json:"template"`
}

// ---------------------------------------------------------------------------
// 模板目录
// ---------------------------------------------------------------------------

// handleListBroadcastTemplates 处理 GET /api/admin/broadcast-templates。
//
// 为什么单独一个端点：后台下拉据此渲染，新增模板时前端无需改动
// （与 channel-types / key-strategies 一致的做法）。
func (s *Server) handleListBroadcastTemplates(c *gin.Context) {
	items := make([]broadcastTemplateDTO, 0, 4)
	for _, tpl := range mailer.BroadcastTemplates() {
		items = append(items, broadcastTemplateDTO{Key: tpl.Key, Label: tpl.Label})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ---------------------------------------------------------------------------
// 预览
// ---------------------------------------------------------------------------

// handlePreviewBroadcast 处理 POST /api/admin/broadcasts/preview。
//
// 只发给当前管理员自己：预览的目的是"自己先看一眼"。
// 刻意不支持任意收件人——那会让这个接口变成"用什么地址都能发的发信器"。
func (s *Server) handlePreviewBroadcast(c *gin.Context) {
	var req broadcastPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	to := model.NormalizeEmail(user.Email)
	if to == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"你的账号还没有填写邮箱，请先在「用户管理」里补上邮箱再预览",
			oai.TypeInvalidRequest, "broadcast_admin_email_missing")
		return
	}
	if !s.broadcastMailerReady(c) {
		return
	}

	subject, body, ok := s.renderBroadcast(c, req.Template)
	if !ok {
		return
	}
	if err := s.deps.Mailer.Send(c.Request.Context(), to, subject, body); err != nil {
		// 把发信失败原文回给管理员：他正在排查"为什么发不出去"，隐藏细节毫无帮助。
		oai.WriteError(c.Writer, http.StatusBadGateway,
			"预览邮件发送失败："+err.Error(), oai.TypeServer, "broadcast_preview_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"subject": subject, "to": to})
}

// ---------------------------------------------------------------------------
// 创建并开始发送
// ---------------------------------------------------------------------------

// handleCreateBroadcast 处理 POST /api/admin/broadcasts。
func (s *Server) handleCreateBroadcast(c *gin.Context) {
	// 二次验证：全站群发一旦点下去就发到所有绑定邮箱，既不可撤回，
	// 又直接消耗发信额度与服务商信誉，是最需要"确认是本人亲手点的"的操作之一。
	if !s.requireFreshReauth(c) {
		return
	}

	var req broadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	if !req.Confirm {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"全站群发不可撤回，请先预览确认内容后再显式确认发送",
			oai.TypeInvalidRequest, "broadcast_confirm_required")
		return
	}

	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	if !s.broadcastMailerReady(c) {
		return
	}
	if s.deps.Broadcasts == nil || s.deps.Broadcast == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件群发模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	subject, body, ok := s.renderBroadcast(c, req.Template)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	// 主题与正文在此【快照】落库：预览看到的就是发出去的，模板后续改动不影响本次发送。
	bc := &model.EmailBroadcast{
		Template:  req.Template,
		Subject:   subject,
		BodyHTML:  body,
		Status:    model.BroadcastStatusPending,
		CreatedBy: user.ID,
	}
	if err := s.deps.Broadcasts.Create(ctx, bc); err != nil {
		s.respondInternalError(c, "创建群发批次失败")
		return
	}

	// 收件人：仅启用用户，邮箱为空的跳过（由 Sender.Enqueue 负责过滤与去重）。
	enabled := model.UserStatusEnabled
	total, err := s.deps.Broadcast.Enqueue(ctx, bc, model.UserQuery{Status: &enabled})
	if err != nil {
		s.respondInternalError(c, "生成收件人名单失败")
		return
	}
	if total == 0 {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"没有任何用户填写了邮箱，本次未发送任何邮件",
			oai.TypeInvalidRequest, "broadcast_no_recipients")
		return
	}
	if err := s.deps.Broadcasts.UpdateProgress(ctx, bc.ID, model.BroadcastStatusPending,
		total, 0, 0, time.Time{}, time.Time{}); err != nil {
		s.respondInternalError(c, "写入群发进度失败")
		return
	}
	bc.Total = total

	// 后台开始发送：接口立即返回，避免 HTTP 请求挂着等几十分钟。
	if !s.deps.Broadcast.Start(bc.ID) {
		oai.WriteError(c.Writer, http.StatusConflict,
			"该批次已在发送中", oai.TypeInvalidRequest, "broadcast_already_running")
		return
	}
	c.JSON(http.StatusOK, toEmailBroadcastDTO(bc))
}

// ---------------------------------------------------------------------------
// 列表 / 明细 / 停止
// ---------------------------------------------------------------------------

// handleListBroadcasts 处理 GET /api/admin/broadcasts。
func (s *Server) handleListBroadcasts(c *gin.Context) {
	if s.deps.Broadcasts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件群发模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	page, size, offset := parsePagination(c)
	items, total, err := s.deps.Broadcasts.List(c.Request.Context(), size, offset)
	if err != nil {
		s.respondInternalError(c, "查询群发记录失败")
		return
	}

	views := make([]emailBroadcastDTO, 0, len(items))
	for _, item := range items {
		views = append(views, toEmailBroadcastDTO(item))
	}
	c.JSON(http.StatusOK, newPagedResponse(views, total, page, size))
}

// handleListBroadcastRecipients 处理 GET /api/admin/broadcasts/{id}/recipients。
//
// 支持按状态过滤：验收"发了多少、谁失败了"时只看 failed 即可。
func (s *Server) handleListBroadcastRecipients(c *gin.Context) {
	if s.deps.Broadcasts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件群发模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != model.RecipientStatusPending &&
		status != model.RecipientStatusSent && status != model.RecipientStatusFailed {
		oai.WriteError(c.Writer, http.StatusBadRequest, "收件人状态取值非法",
			oai.TypeInvalidRequest, "invalid_recipient_status")
		return
	}

	page, size, offset := parsePagination(c)
	items, total, err := s.deps.Broadcasts.ListRecipients(c.Request.Context(), id, status, size, offset)
	if err != nil {
		s.respondInternalError(c, "查询收件人明细失败")
		return
	}

	views := make([]broadcastRecipientDTO, 0, len(items))
	for _, item := range items {
		views = append(views, broadcastRecipientDTO{
			ID:     item.ID,
			UserID: item.UserID,
			Email:  item.Email,
			Status: item.Status,
			Error:  item.Error,
			SentAt: unixOrZero(item.SentAt),
		})
	}
	c.JSON(http.StatusOK, newPagedResponse(views, total, page, size))
}

// handleCancelBroadcast 处理 POST /api/admin/broadcasts/{id}/cancel。
//
// 语义：停止后续发送。已发出的邮件无法撤回（这是"发送前必须预览"的根本原因）。
func (s *Server) handleCancelBroadcast(c *gin.Context) {
	if s.deps.Broadcasts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件群发模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	bc, err := s.deps.Broadcasts.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrEmailBroadcastNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "群发批次不存在",
				oai.TypeInvalidRequest, "broadcast_not_found")
			return
		}
		s.respondInternalError(c, "查询群发批次失败")
		return
	}
	if bc.IsFinished() {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"该批次已结束，无需停止", oai.TypeInvalidRequest, "broadcast_finished")
		return
	}

	// 发送器每封之前都会重读状态，因此这里写入 canceled 后最多再发一封就停。
	if err := s.deps.Broadcasts.UpdateProgress(ctx, id, model.BroadcastStatusCanceled,
		bc.Total, bc.Sent, bc.Failed, time.Time{}, time.Time{}); err != nil {
		s.respondInternalError(c, "停止群发失败")
		return
	}
	updated, err := s.deps.Broadcasts.GetByID(ctx, id)
	if err != nil {
		s.respondInternalError(c, "读取群发批次失败")
		return
	}
	c.JSON(http.StatusOK, toEmailBroadcastDTO(updated))
}

// ---------------------------------------------------------------------------
// 公共辅助
// ---------------------------------------------------------------------------

// renderBroadcast 校验模板键并渲染主题与正文；失败时已写出响应。
func (s *Server) renderBroadcast(c *gin.Context, templateKey string) (subject, body string, ok bool) {
	key := strings.TrimSpace(templateKey)
	if key == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请选择要发送的通知模板",
			oai.TypeInvalidRequest, "broadcast_template_required")
		return "", "", false
	}

	settings, err := model.LoadSiteSettings(c.Request.Context(), s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取站点设置失败")
		return "", "", false
	}

	subject, body, rendered := mailer.RenderBroadcast(key, settings.SiteName)
	if !rendered {
		oai.WriteError(c.Writer, http.StatusBadRequest, "通知模板不存在",
			oai.TypeInvalidRequest, "broadcast_template_not_found")
		return "", "", false
	}
	return subject, body, true
}

// broadcastMailerReady 检查邮件通道是否可用；不可用时已写出响应。
//
// 为什么单独抽出来：预览与发送都要先过这一关，而"没配 SMTP 却点了发送"
// 若不在入口拦住，会得到一个空名单的批次或一堆失败明细，排查成本高得多。
func (s *Server) broadcastMailerReady(c *gin.Context) bool {
	if s.deps.Mailer == nil || !s.deps.Mailer.Configured() {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"邮件通道未配置：请先在「系统设置 → 邮件通道」完成配置并测试发信",
			oai.TypeServer, "email_service_unavailable")
		return false
	}
	return true
}
