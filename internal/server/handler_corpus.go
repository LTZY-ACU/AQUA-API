// 本文件实现「语料共建计划」的后台接口：清单维护、福利账户、样本查看与导出。
//
// 意图（Why）：
//
//	采集本身在转发链路上完成（见 relay 与 corpus 包），本文件只解决"人怎么管它"：
//	  1) 清单要能随时增删，且**改完立刻生效**（不必重启、不必等快照周期）；
//	  2) 样本列表只给预览，看全文与导出必须走独立入口——这两个入口都写审计日志。
//
//	第 2 条是刻意的约束：语料是用户对话原文，是本站最敏感的数据。
//	如果列表页就能直接读到全文，"顺手翻一翻用户聊了什么"在流程上就成立了；
//	把它变成"必须点开单条、且留下记录"，绝大多数顺手行为自然消失。
//
// 流转（Flow）：
//
//	后台页面 → GET/POST /api/admin/corpus/* → 仓储读写 → 刷新内存快照 → 立即生效
//	导出      → GET /api/admin/corpus/export → 边读边写 JSONL → 站长本地脱敏
//
// 扩展（Extend）：
//
//	新增筛选维度（如按渠道）时：在 store 的 corpusSampleFilter 里加一条，
//	列表与导出会同时生效（两条路径共用同一套筛选语义）。
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
)

// corpusPreviewRunes 是样本列表里正文预览的字数。
const corpusPreviewRunes = 200

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// corpusModelDTO 是语料清单项。
type corpusModelDTO struct {
	Model     string `json:"model"`
	Enabled   bool   `json:"enabled"`
	Remark    string `json:"remark"`
	UpdatedAt int64  `json:"updated_at"`
}

// corpusGrantDTO 是福利资格项。
type corpusGrantDTO struct {
	UserID     uint64 `json:"user_id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	Model      string `json:"model"`
	FreeAccess bool   `json:"free_access"`
	Active     bool   `json:"active"`
	Remark     string `json:"remark"`
	UpdatedAt  int64  `json:"updated_at"`
}

// corpusSampleDTO 是样本的列表项（正文只给预览）。
type corpusSampleDTO struct {
	ID              uint64 `json:"id"`
	RequestID       string `json:"request_id"`
	UserID          uint64 `json:"user_id"`
	Model           string `json:"model"`
	UpstreamModel   string `json:"upstream_model"`
	ChannelID       uint64 `json:"channel_id"`
	IsStream        bool   `json:"is_stream"`
	StatusCode      int    `json:"status_code"`
	RequestBytes    int64  `json:"request_bytes"`
	ResponseBytes   int64  `json:"response_bytes"`
	Truncated       bool   `json:"truncated"`
	Incomplete      bool   `json:"incomplete"`
	RequestPreview  string `json:"request_preview"`
	ResponsePreview string `json:"response_preview"`
	CreatedAt       int64  `json:"created_at"`
}

// corpusSampleDetailDTO 是样本详情（含全文）。
type corpusSampleDetailDTO struct {
	corpusSampleDTO
	RequestBody  string `json:"request_body"`
	ResponseBody string `json:"response_body"`
}

// corpusStatDTO 是语料库规模统计。
type corpusStatDTO struct {
	Samples       int64 `json:"samples"`
	Users         int64 `json:"users"`
	RequestBytes  int64 `json:"request_bytes"`
	ResponseBytes int64 `json:"response_bytes"`
	EarliestAt    int64 `json:"earliest_at"`
	LatestAt      int64 `json:"latest_at"`
	// Models 是内存快照里"正在采集"的模型数量，FreeUsers 是享有免计费的用户数。
	Models    int `json:"models"`
	FreeUsers int `json:"free_users"`
}

// corpusModelRequest 是清单项的写入请求。
type corpusModelRequest struct {
	Model   string `json:"model"`
	Enabled *bool  `json:"enabled"`
	Remark  string `json:"remark"`
}

// corpusGrantRequest 是福利资格的发放请求。
type corpusGrantRequest struct {
	// Email 与 UserID 二选一；优先用邮箱（避免抄错 ID）。
	Email string `json:"email"`
	// UserID 仅在邮箱无法定位时使用。
	UserID uint64 `json:"user_id"`
	Model  string `json:"model"`
	// FreeAccess 省略时按"免计费"处理（本功能的默认语义）。
	FreeAccess *bool  `json:"free_access"`
	Remark     string `json:"remark"`
}

// corpusDeleteRequest 是"按名字删除"的请求体。
//
// 为什么不用 DELETE /xxx/:name 这种路径参数：模型名里带斜杠
// （如 LTZY-CALL/deepseek-v4.1-flash），放进路径会被路由拆成多段。
// 用请求体传名字既避免歧义，也不用做二次转义。
type corpusDeleteRequest struct {
	Model  string `json:"model"`
	UserID uint64 `json:"user_id"`
}

// ---------------------------------------------------------------------------
// 清单
// ---------------------------------------------------------------------------

// handleListCorpusModels 处理 GET /api/admin/corpus/models。
func (s *Server) handleListCorpusModels(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	items, err := repo.ListCorpusModels(c.Request.Context())
	if err != nil {
		s.respondInternalError(c, "查询语料模型清单失败")
		return
	}
	views := make([]corpusModelDTO, 0, len(items))
	for _, item := range items {
		views = append(views, corpusModelDTO{
			Model:     item.Model,
			Enabled:   item.Enabled,
			Remark:    item.Remark,
			UpdatedAt: unixOrZero(item.UpdatedAt),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

// handleUpsertCorpusModel 处理 POST /api/admin/corpus/models（新增或修改）。
func (s *Server) handleUpsertCorpusModel(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	var req corpusModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	item := &model.CorpusModel{
		Model:   strings.TrimSpace(req.Model),
		Enabled: enabled,
		Remark:  strings.TrimSpace(req.Remark),
	}
	if err := item.Validate(); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "模型名不能为空",
			oai.TypeInvalidRequest, "corpus_model_invalid")
		return
	}
	if err := repo.UpsertCorpusModel(c.Request.Context(), item); err != nil {
		s.respondInternalError(c, "写入语料模型失败")
		return
	}
	s.refreshCorpusGuard(c)
	c.JSON(http.StatusOK, gin.H{"ok": true, "model": item.Model, "enabled": item.Enabled})
}

// handleDeleteCorpusModel 处理 POST /api/admin/corpus/models/delete。
func (s *Server) handleDeleteCorpusModel(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	var req corpusDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Model) == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请提供要移除的模型名",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	if err := repo.DeleteCorpusModel(c.Request.Context(), req.Model); err != nil {
		s.respondInternalError(c, "移除语料模型失败")
		return
	}
	s.refreshCorpusGuard(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 福利资格
// ---------------------------------------------------------------------------

// handleListCorpusGrants 处理 GET /api/admin/corpus/grants。
func (s *Server) handleListCorpusGrants(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	items, err := repo.ListCorpusGrants(ctx)
	if err != nil {
		s.respondInternalError(c, "查询福利资格失败")
		return
	}
	views := make([]corpusGrantDTO, 0, len(items))
	for _, item := range items {
		view := corpusGrantDTO{
			UserID:     item.UserID,
			Model:      item.Model,
			FreeAccess: item.FreeAccess,
			Active:     item.IsActive(),
			Remark:     item.Remark,
			UpdatedAt:  unixOrZero(item.UpdatedAt),
		}
		// 带上用户名与邮箱：后台要看的是"谁"，只有数字 ID 无法核对。
		if user, err := s.deps.Users.GetByID(ctx, item.UserID); err == nil && user != nil {
			view.Username = user.Username
			view.Email = user.Email
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

// handleUpsertCorpusGrant 处理 POST /api/admin/corpus/grants。
func (s *Server) handleUpsertCorpusGrant(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	var req corpusGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	userID := req.UserID
	if email := model.NormalizeEmail(req.Email); email != "" {
		user, err := s.deps.Users.GetByEmail(ctx, email)
		if err != nil {
			// 用邮箱发放就是为了避免抄错 ID，因此"查不到"必须直接拒绝，
			// 而不是退化成"按 user_id 硬发"——那等于把抄错的风险留在系统里。
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"该邮箱没有对应的用户，请确认后再发放",
				oai.TypeInvalidRequest, "corpus_grant_user_not_found")
			return
		}
		userID = user.ID
	}
	if userID == 0 || strings.TrimSpace(req.Model) == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"需要同时提供用户（邮箱或 user_id）与模型名",
			oai.TypeInvalidRequest, "corpus_grant_invalid")
		return
	}

	freeAccess := true
	if req.FreeAccess != nil {
		freeAccess = *req.FreeAccess
	}
	grant := &model.CorpusGrant{
		UserID:     userID,
		Model:      strings.TrimSpace(req.Model),
		FreeAccess: freeAccess,
		Status:     model.CorpusGrantActive,
		Remark:     strings.TrimSpace(req.Remark),
	}
	if err := repo.UpsertCorpusGrant(ctx, grant); err != nil {
		s.respondInternalError(c, "写入福利资格失败")
		return
	}
	s.refreshCorpusGuard(c)
	c.JSON(http.StatusOK, gin.H{"ok": true, "user_id": userID, "model": grant.Model})
}

// handleDeleteCorpusGrant 处理 POST /api/admin/corpus/grants/delete（撤销资格）。
func (s *Server) handleDeleteCorpusGrant(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	var req corpusDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserID == 0 || strings.TrimSpace(req.Model) == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "需要同时提供 user_id 与模型名",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	if err := repo.DeleteCorpusGrant(c.Request.Context(), req.UserID, req.Model); err != nil {
		s.respondInternalError(c, "撤销福利资格失败")
		return
	}
	// 撤销后立刻刷新快照：让"停止免计费"在下一次请求就生效，
	// 而不是等最多 30 秒的刷新周期。
	s.refreshCorpusGuard(c)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 样本查看
// ---------------------------------------------------------------------------

// handleListCorpusSamples 处理 GET /api/admin/corpus/samples。
func (s *Server) handleListCorpusSamples(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	page, size, offset := parsePagination(c)
	query := corpusSampleQueryFromContext(c)
	query.Limit = size
	query.Offset = offset

	items, total, err := repo.ListCorpusSamples(c.Request.Context(), query)
	if err != nil {
		s.respondInternalError(c, "查询语料样本失败")
		return
	}
	views := make([]corpusSampleDTO, 0, len(items))
	for _, item := range items {
		views = append(views, toCorpusSampleDTO(item))
	}
	c.JSON(http.StatusOK, newPagedResponse(views, total, page, size))
}

// handleGetCorpusSample 处理 GET /api/admin/corpus/samples/:id（含全文）。
//
// 这是"看用户对话原文"的唯一入口，因此它会被审计中间件记录下来
// （后台写操作审计只覆盖写方法，所以这里额外写一条查询审计，见下方说明）。
func (s *Server) handleGetCorpusSample(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	item, err := repo.GetCorpusSample(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrCorpusSampleNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "语料样本不存在",
				oai.TypeInvalidRequest, "corpus_sample_not_found")
			return
		}
		s.respondInternalError(c, "查询语料样本失败")
		return
	}

	// 审计留痕：先记一笔"谁在什么时候看了哪一条"，再返回内容。
	s.recordCorpusAccess(c, "查看语料样本全文", map[string]any{
		"sample_id":  item.ID,
		"request_id": item.RequestID,
		"model":      item.Model,
		"user_id":    item.UserID,
	})

	view := corpusSampleDetailDTO{
		corpusSampleDTO: toCorpusSampleDTO(item),
		RequestBody:     item.RequestBody,
		ResponseBody:    item.ResponseBody,
	}
	c.JSON(http.StatusOK, view)
}

// handleExportCorpusSamples 处理 GET /api/admin/corpus/export。
//
// 以 JSONL 流式导出（一行一条），边读边写、不在内存里攒整批：
// 语料可能几十万条、每条几十 KB，一次性读进内存会把进程撑爆。
func (s *Server) handleExportCorpusSamples(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	query := corpusSampleQueryFromContext(c)

	s.recordCorpusAccess(c, "导出语料样本", map[string]any{
		"model":   query.Model,
		"user_id": query.UserID,
		"from":    unixOrZero(query.From),
		"to":      unixOrZero(query.To),
	})

	filename := "aqua-corpus-" + time.Now().Format("20060102-150405") + ".jsonl"
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Header("Cache-Control", "no-store")

	encoder := json.NewEncoder(c.Writer)
	flusher, canFlush := c.Writer.(http.Flusher)

	if err := repo.IterateCorpusSamples(c.Request.Context(), query, func(item *model.CorpusSample) error {
		if err := encoder.Encode(corpusExportLine(item)); err != nil {
			return err
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}); err != nil {
		// 响应头可能已经发出，此时无法再改状态码；只能中断并把原因记进日志。
		// 调用方会拿到一个"提前结束"的 JSONL，条数不足，可据此判断导出不完整。
		oai.WriteError(c.Writer, http.StatusBadGateway, "导出中断："+err.Error(),
			oai.TypeServer, oai.CodeInternal)
		return
	}
}

// handleCorpusStats 处理 GET /api/admin/corpus/stats。
func (s *Server) handleCorpusStats(c *gin.Context) {
	repo, ok := s.corpusRepoReady(c)
	if !ok {
		return
	}
	stat, err := repo.StatCorpusSamples(c.Request.Context())
	if err != nil {
		s.respondInternalError(c, "统计语料库失败")
		return
	}
	view := corpusStatDTO{
		Samples:       stat.Samples,
		Users:         stat.Users,
		RequestBytes:  stat.RequestBytes,
		ResponseBytes: stat.ResponseBytes,
		EarliestAt:    unixOrZero(stat.EarliestAt),
		LatestAt:      unixOrZero(stat.LatestAt),
	}
	if s.deps.Corpus != nil {
		view.Models = s.deps.Corpus.ModelCount()
		view.FreeUsers = s.deps.Corpus.FreeUserCount()
	}
	c.JSON(http.StatusOK, view)
}

// ---------------------------------------------------------------------------
// 公共辅助
// ---------------------------------------------------------------------------

// corpusRepoReady 取出语料仓储；未装配时已写出响应。
func (s *Server) corpusRepoReady(c *gin.Context) (model.CorpusRepository, bool) {
	if s.deps.CorpusSamples == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"语料共建模块未启用", oai.TypeServer, oai.CodeInternal)
		return nil, false
	}
	return s.deps.CorpusSamples, true
}

// refreshCorpusGuard 让清单/资格的改动立即生效。
//
// 为什么不等定时刷新：站长点了"撤销"，却还要等最多 30 秒才生效，
// 会让人怀疑"是不是没生效"，从而反复操作。刷新失败只告警——
// 最多是延迟一个周期生效，不该让这次管理操作失败。
func (s *Server) refreshCorpusGuard(c *gin.Context) {
	if s.deps.Corpus == nil {
		return
	}
	_ = s.deps.Corpus.Refresh(c.Request.Context())
}

// recordCorpusAccess 写一条语料访问审计。
//
// 为什么单独写：后台的写操作审计只覆盖 POST/PUT/PATCH/DELETE，
// 而"看全文""导出"都是 GET——若不做这一步，最敏感的两种访问反而没有痕迹。
// 写失败不阻断：审计失败不该让站长拿不到数据。
func (s *Server) recordCorpusAccess(c *gin.Context, action string, detail map[string]any) {
	if s.deps.Audit == nil {
		return
	}
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return
	}
	blob, err := json.Marshal(detail)
	if err != nil {
		blob = nil
	}
	_ = s.deps.Audit.Create(c.Request.Context(), &model.AuditLog{
		AdminID:       user.ID,
		AdminUsername: user.Username,
		Method:        c.Request.Method,
		Path:          c.Request.URL.Path,
		Action:        action,
		Detail:        string(blob),
		StatusCode:    http.StatusOK,
		ClientIP:      c.ClientIP(),
		UserAgent:     truncateForAudit(c.Request.UserAgent()),
		CreatedAt:     time.Now(),
	})
}

// truncateForAudit 截断 User-Agent（审计表不该被超长头撑大）。
func truncateForAudit(text string) string {
	const limit = 200
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// corpusSampleQueryFromContext 解析样本筛选条件（列表与导出共用）。
func corpusSampleQueryFromContext(c *gin.Context) model.CorpusSampleQuery {
	query := model.CorpusSampleQuery{
		Model: strings.TrimSpace(c.Query("model")),
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
			query.UserID = id
		}
	}
	// from/to 接受 unix 秒；前端用时间选择器时自行换算。
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
			query.From = time.Unix(sec, 0)
		}
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
			query.To = time.Unix(sec, 0)
		}
	}
	return query
}

// toCorpusSampleDTO 把样本转成列表项（正文截成预览）。
func toCorpusSampleDTO(item *model.CorpusSample) corpusSampleDTO {
	return corpusSampleDTO{
		ID:              item.ID,
		RequestID:       item.RequestID,
		UserID:          item.UserID,
		Model:           item.Model,
		UpstreamModel:   item.UpstreamModel,
		ChannelID:       item.ChannelID,
		IsStream:        item.IsStream,
		StatusCode:      item.StatusCode,
		RequestBytes:    item.RequestBytes,
		ResponseBytes:   item.ResponseBytes,
		Truncated:       item.Truncated,
		Incomplete:      item.Incomplete,
		RequestPreview:  previewRunes(item.RequestBody, corpusPreviewRunes),
		ResponsePreview: previewRunes(item.ResponseBody, corpusPreviewRunes),
		CreatedAt:       unixOrZero(item.CreatedAt),
	}
}

// corpusExportLine 组装导出的一行。
//
// 与库表列一一对应，另外把 request 解析成**结构化 JSON**：
// 站长本地要按字段脱敏（例如只取 messages），把整段请求体当字符串会很难处理。
// 解析失败时退化为原始字符串，保证"导得出来"永远优先于"格式漂亮"。
func corpusExportLine(item *model.CorpusSample) map[string]any {
	line := map[string]any{
		"id":             item.ID,
		"request_id":     item.RequestID,
		"user_id":        item.UserID,
		"token_id":       item.TokenID,
		"model":          item.Model,
		"upstream_model": item.UpstreamModel,
		"channel_id":     item.ChannelID,
		"channel_key_id": item.ChannelKeyID,
		"is_stream":      item.IsStream,
		"status_code":    item.StatusCode,
		"request_bytes":  item.RequestBytes,
		"response_bytes": item.ResponseBytes,
		"truncated":      item.Truncated,
		"incomplete":     item.Incomplete,
		"created_at":     unixOrZero(item.CreatedAt),
		"response":       item.ResponseBody,
	}
	var request any
	if err := json.Unmarshal([]byte(item.RequestBody), &request); err == nil {
		line["request"] = request
	} else {
		line["request"] = item.RequestBody
	}
	return line
}

// previewRunes 截取前 n 个字符作为预览（按 rune 截，避免把中文切半个）。
func previewRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}
