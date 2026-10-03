// AI Agent 的后台管理接口：配置读写与密钥管理。
//
// 意图（Why）：
//
//	站长要能自己完成两件事，不必求助他人：
//	  1) 决定"助手用什么模型、说什么话"（配置）；
//	  2) 给外部人发一把只能问客服的钥匙（密钥）。
//
// 【密钥明文只返回一次，这是本文件最重要的约束】
//
//	创建密钥时响应里带明文，之后无论谁再调接口都只能拿到数据库里的摘要。
//	这不是偷懒——摘要不可逆是整个设计的意义所在：
//	若后台列表能随时取回明文，那么数据库被读走（备份泄漏、日志误记、
//	一台被入侵的机器）就直接等于所有客服密钥同时失守，
//	而站长对"我这把 key 还安全吗"将完全没有判断依据。
//
// 流转（Flow）：
//
//	GET    /api/admin/agent/settings  → 读配置（含"是否用内置提示词"的标记）
//	PUT    /api/admin/agent/settings  → 整份保存（局部更新语义见 handleUpdateAgentSettings）
//	GET    /api/admin/agent/keys      → 列表（永不返回明文与摘要）
//	POST   /api/admin/agent/keys      → 创建（响应含一次性明文）
//	PUT    /api/admin/agent/keys/:id  → 启用 / 禁用
//	DELETE /api/admin/agent/keys/:id  → 永久删除（立即失效）
//
// 扩展（Extend）：
//
//	要加"密钥使用统计"：给 agent_keys 加 last_used_at 列 + 一个 0056 迁移，
//	在 AgentKeyAuth 鉴权成功时顺带更新——
//	不要在列表接口里现算，那会把"看列表"变成一次写操作。
package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

const (
	// agentKeyIDParamMaxDigits 是密钥 ID 的长度上限。
	//
	// 存在的理由很实际：id 从查询参数来，直接 Atoi 一个超长字符串
	// 会因溢出返回错误，虽然不会出错，但错误信息（"数字格式错误"）
	// 会把站长引向"是不是我哪里填错了"，而实际只是有人扫接口。
	// 显式限长后返回的是明确的 400。
	agentKeyIDParamMaxDigits = 19

	// agentKeyListMaxLimit 是密钥列表的行数上限。
	agentKeyListMaxLimit = 200
)

// agentSettingsRequest 是配置保存请求。
//
// 刻意【不用指针字段】表示"未传"：本站的设置页是整份保存语义
// （见 SiteSettings 的 SetMany 事务化写入），指针会让"留空即不改"
// 与"清空该项"两种意图混在一起，而这两者对提示词而言含义完全相反。
type agentSettingsRequest struct {
	Enabled        *bool   `json:"enabled"`
	DefaultModel   *string `json:"default_model"`
	OpsModel       *string `json:"ops_model"`
	SupportModel   *string `json:"support_model"`
	OpsPrompt      *string `json:"ops_system_prompt"`
	SupportPrompt  *string `json:"support_system_prompt"`
	HistoryEnabled *bool   `json:"history_enabled"`
}

// agentSettingsResponse 是配置响应。
type agentSettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	DefaultModel   string `json:"default_model"`
	OpsModel       string `json:"ops_model"`
	SupportModel   string `json:"support_model"`
	OpsPrompt      string `json:"ops_system_prompt"`
	SupportPrompt  string `json:"support_system_prompt"`
	HistoryEnabled bool   `json:"history_enabled"`

	// OpsPromptIsDefault / SupportPromptIsDefault 告知前端"这一栏留空会用内置底稿"。
	//
	// 为什么必须由后端给而不是前端自己判断空串：内置底稿的存在与否是后端的决定
	// （可能随版本变化），前端若自己硬编码"空 = 有默认"，将来后端改版时
	// 前端会显示一个已经不存在的默认提示词，用户照着填半天毫无意义。
	OpsPromptIsDefault     bool `json:"ops_prompt_is_default"`
	SupportPromptIsDefault bool `json:"support_prompt_is_default"`
	// PromptPlaceholder 是提示词输入框的占位文案。
	PromptPlaceholder string `json:"prompt_placeholder"`
}

// handleGetAgentSettings 处理 GET /api/admin/agent/settings。
func (s *Server) handleGetAgentSettings(c *gin.Context) {
	settings, err := model.LoadAgentSettings(c.Request.Context(), s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取 agent 配置失败", err)
		return
	}
	c.JSON(http.StatusOK, buildAgentSettingsResponse(settings))
}

// handleUpdateAgentSettings 处理 PUT /api/admin/agent/settings。
//
// 语义：指针为 nil 的项【保持原值】。本站设置页是整份表单提交，
// 而提示词这类长文本一旦被某次"只改模型名"的提交顺手清空，
// 站长精心写的客服话术就没了且无从恢复——这个默认值必须是保守的。
func (s *Server) handleUpdateAgentSettings(c *gin.Context) {
	var req agentSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	current, err := model.LoadAgentSettings(c.Request.Context(), s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取 agent 配置失败", err)
		return
	}

	next := current
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.HistoryEnabled != nil {
		next.HistoryEnabled = *req.HistoryEnabled
	}
	if req.DefaultModel != nil {
		next.DefaultModel = model.TrimAgentPrompt(*req.DefaultModel)
	}
	if req.OpsModel != nil {
		next.OpsModel = model.TrimAgentPrompt(*req.OpsModel)
	}
	if req.SupportModel != nil {
		next.SupportModel = model.TrimAgentPrompt(*req.SupportModel)
	}
	if req.OpsPrompt != nil {
		next.OpsSystemPrompt = model.TrimAgentPrompt(*req.OpsPrompt)
	}
	if req.SupportPrompt != nil {
		next.SupportSystemPrompt = model.TrimAgentPrompt(*req.SupportPrompt)
	}

	// 校验：开启就必须有模型可用，否则站长会得到一个"开了但不能用"的入口。
	if next.Enabled && next.DefaultModel == "" && next.OpsModel == "" && next.SupportModel == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"开启 agent 前请至少填写一个模型（默认模型、运维模型、客服模型任选其一）",
			oai.TypeInvalidRequest, "agent_model_required")
		return
	}

	values := map[string]string{
		model.SettingKeyAgentEnabled:             strconv.FormatBool(next.Enabled),
		model.SettingKeyAgentDefaultModel:        next.DefaultModel,
		model.SettingKeyAgentOpsModel:            next.OpsModel,
		model.SettingKeyAgentSupportModel:        next.SupportModel,
		model.SettingKeyAgentOpsSystemPrompt:     next.OpsSystemPrompt,
		model.SettingKeyAgentSupportSystemPrompt: next.SupportSystemPrompt,
		model.SettingKeyAgentHistoryEnabled:      strconv.FormatBool(next.HistoryEnabled),
	}
	if err := s.deps.Settings.SetMany(c.Request.Context(), values); err != nil {
		s.respondInternalError(c, "保存 agent 配置失败", err)
		return
	}
	c.JSON(http.StatusOK, buildAgentSettingsResponse(next))
}

// buildAgentSettingsResponse 组装配置响应。
func buildAgentSettingsResponse(s model.AgentSettings) agentSettingsResponse {
	return agentSettingsResponse{
		Enabled:                s.Enabled,
		DefaultModel:           s.DefaultModel,
		OpsModel:               s.OpsModel,
		SupportModel:           s.SupportModel,
		OpsPrompt:              s.OpsSystemPrompt,
		SupportPrompt:          s.SupportSystemPrompt,
		HistoryEnabled:         s.HistoryEnabled,
		OpsPromptIsDefault:     strings.TrimSpace(s.OpsSystemPrompt) == "",
		SupportPromptIsDefault: strings.TrimSpace(s.SupportSystemPrompt) == "",
		PromptPlaceholder:      model.SiteAgentPromptPlaceholder,
	}
}

// agentKeyResponse 是密钥列表项。
//
// 刻意不含 KeyHash：连摘要都不该下发。
// 摘要虽然没有明文那么致命，但它是"校验值"——
// 泄露后攻击者可以离线比对大量猜测，且后台没有任何界面需要它。
type agentKeyResponse struct {
	ID        uint64 `json:"id"`
	Role      string `json:"role"`
	RoleLabel string `json:"role_label"`
	Name      string `json:"name"`
	Status    int    `json:"status"`
	// Expired 单独下发而不是让前端比较时间：
	// 时间比较在前端要处理时区与"零值"两种情况，
	// 而"永不过期"在前端表现为 1970 年的时间戳，很容易被误判成"已过期"。
	Expired bool `json:"expired"`
	Active  bool `json:"active"`
	// ExpiresAt / CreatedAt 是 Unix 秒，与全站其余接口一致。
	//
	// 为什么不用 RFC3339 字符串：前端的 formatDateTime 只接受 Unix 秒
	// （i18n/format.ts 的 DateInput），而全站已有二十多个页面按这个契约写。
	// 让 agent 单独用另一种格式，就要在前端开一个特例函数——
	// 而那个特例函数的存在本身会成为"这里有个不一致"的长期提示。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	CreatedAt int64 `json:"created_at"`
}

// createAgentKeyRequest 是创建密钥请求。
type createAgentKeyRequest struct {
	// Role 决定这把密钥的权限边界。必填且不接受空值：
	// 默认成 ops 会让"忘了填角色"直接产出一把能读站内数据的公网密钥。
	Role model.AgentRole `json:"role"`
	// Name 是站长备注，必填（见 model 的长度校验）。
	Name string `json:"name"`
	// ExpiresInDays 是有效天数；0 或负值表示永不过期。
	//
	// 默认给 0（永不过期）而不是某个天数：站长发出去的是给别人用的凭据，
	// 到期突然失效会让对方直接报错。是否设期限应当由站长明确决定。
	ExpiresInDays int `json:"expires_in_days"`
}

// createAgentKeyResponse 是创建响应（含一次性明文）。
type createAgentKeyResponse struct {
	agentKeyResponse
	// Key 是明文，**仅本次响应返回**。
	Key string `json:"key"`
	// Warning 是给前端的提示文案，引导它明确告知用户"只显示这一次"。
	Warning string `json:"warning"`
}

// handleListAgentKeys 处理 GET /api/admin/agent/keys。
func (s *Server) handleListAgentKeys(c *gin.Context) {
	if s.deps.AgentKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "agent 密钥功能未启用",
			oai.TypeServer, "agent_not_enabled")
		return
	}

	limit := agentKeyListMaxLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < limit {
			limit = parsed
		}
		// 解析失败或超界一律按上限处理，而不是报错：
		// 列表接口的参数来自前端展示逻辑，不该因为它坏了就整个页面打不开。
	}

	query := model.AgentKeyQuery{Limit: limit}
	if raw := strings.TrimSpace(c.Query("role")); raw != "" {
		if err := model.ValidateAgentKeyRole(model.AgentRole(raw)); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, "角色参数不合法",
				oai.TypeInvalidRequest, "invalid_role")
			return
		}
		query.Role = model.AgentRole(raw)
	}

	keys, err := s.deps.AgentKeys.List(c.Request.Context(), query)
	if err != nil {
		s.respondInternalError(c, "读取 agent 密钥列表失败", err)
		return
	}

	items := make([]agentKeyResponse, 0, len(keys))
	for _, key := range keys {
		items = append(items, buildAgentKeyResponse(key))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// handleCreateAgentKey 处理 POST /api/admin/agent/keys。
func (s *Server) handleCreateAgentKey(c *gin.Context) {
	if s.deps.AgentKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "agent 密钥功能未启用",
			oai.TypeServer, "agent_not_enabled")
		return
	}

	var req createAgentKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// 角色必填且严格校验：见结构体字段注释。
	if err := model.ValidateAgentKeyRole(req.Role); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"请选择密钥角色（运维助手或在线客服）",
			oai.TypeInvalidRequest, "invalid_role")
		return
	}

	plain, err := model.GenerateAgentKey()
	if err != nil {
		s.respondInternalError(c, "生成 agent 密钥失败", err)
		return
	}

	now := time.Now()
	key := &model.AgentKey{
		Role:      req.Role,
		Name:      req.Name,
		KeyHash:   model.HashAgentKey(plain),
		Status:    model.AgentKeyStatusEnabled,
		CreatedAt: now,
	}
	if req.ExpiresInDays > 0 {
		key.ExpiresAt = now.AddDate(0, 0, req.ExpiresInDays)
	}
	key.NormalizeForCreate(now)
	if err := key.Validate(); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_agent_key")
		return
	}

	if err := s.deps.AgentKeys.Create(c.Request.Context(), key); err != nil {
		s.respondInternalError(c, "保存 agent 密钥失败", err)
		return
	}

	c.JSON(http.StatusCreated, createAgentKeyResponse{
		agentKeyResponse: buildAgentKeyResponse(key),
		Key:              plain,
		Warning:          "请立即复制并保存该密钥，它只会显示这一次。",
	})
}

// updateAgentKeyRequest 是启用 / 禁用请求。
//
// 用 *bool 而非 bool：见 model.AgentKey 里 enabled 的同类讨论——
// "没传"与"传 false"必须能区分，否则一次只改备注的请求会把密钥停用掉。
type updateAgentKeyRequest struct {
	Status *int    `json:"status"`
	Name   *string `json:"name"`
}

// handleUpdateAgentKey 处理 PUT /api/admin/agent/keys/:id。
func (s *Server) handleUpdateAgentKey(c *gin.Context) {
	if s.deps.AgentKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "agent 密钥功能未启用",
			oai.TypeServer, "agent_not_enabled")
		return
	}

	id, ok := parseAgentKeyID(c)
	if !ok {
		return
	}

	var req updateAgentKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	if req.Status == nil && req.Name == nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "没有需要修改的内容",
			oai.TypeInvalidRequest, "empty_update")
		return
	}
	if req.Status != nil &&
		*req.Status != model.AgentKeyStatusEnabled &&
		*req.Status != model.AgentKeyStatusDisabled {
		oai.WriteError(c.Writer, http.StatusBadRequest, "状态参数不合法",
			oai.TypeInvalidRequest, "invalid_status")
		return
	}

	// 改名与改状态分开执行：两者是独立的仓储操作，
	// 合成一步（先读出整条记录再写回）会让"改备注"顺带有了改权限的能力。
	// 顺序上先改名后改状态，因为改名失败（备注非法）应当整条请求失败，
	// 而状态变更通常是站长更在意的那一项，不该被一次备注错误连累。
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		// 借用模型层的完整校验：构造一个临时对象只验名字字段。
		// 不另写一份长度/空值判断——两份校验迟早会漂移，
		// 而漂移的表现是"创建时能过、改名时被拒"这种无法解释的不一致。
		probe := &model.AgentKey{Role: model.AgentRoleSupport, Name: name}
		if err := probe.ValidateName(); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_agent_key")
			return
		}
		if err := s.deps.AgentKeys.UpdateName(c.Request.Context(), id, name); err != nil {
			if errors.Is(err, model.ErrAgentKeyNotFound) {
				oai.WriteError(c.Writer, http.StatusNotFound, "密钥不存在",
					oai.TypeInvalidRequest, "agent_key_not_found")
				return
			}
			s.respondInternalError(c, "修改 agent 密钥备注失败", err)
			return
		}
	}

	if req.Status != nil {
		if err := s.deps.AgentKeys.SetStatus(c.Request.Context(), id, *req.Status); err != nil {
			if errors.Is(err, model.ErrAgentKeyNotFound) {
				oai.WriteError(c.Writer, http.StatusNotFound, "密钥不存在",
					oai.TypeInvalidRequest, "agent_key_not_found")
				return
			}
			s.respondInternalError(c, "修改 agent 密钥状态失败", err)
			return
		}
	}

	// 回读以返回最新状态：避免前端拿乐观值渲染出一个与实际不符的徽标。
	keys, err := s.deps.AgentKeys.List(c.Request.Context(),
		model.AgentKeyQuery{Limit: agentKeyListMaxLimit})
	if err != nil {
		s.respondInternalError(c, "读取 agent 密钥失败", err)
		return
	}
	found := findAgentKeyByID(keys, id)
	if found == nil {
		oai.WriteError(c.Writer, http.StatusNotFound, "密钥不存在",
			oai.TypeInvalidRequest, "agent_key_not_found")
		return
	}
	c.JSON(http.StatusOK, buildAgentKeyResponse(found))
}

// handleDeleteAgentKey 处理 DELETE /api/admin/agent/keys/:id。
//
// 永久删除而非停用：撤销必须是"立刻不可用"，
// 留一条"已删除但记录还在"的行只会让人误以为还能用。
func (s *Server) handleDeleteAgentKey(c *gin.Context) {
	if s.deps.AgentKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "agent 密钥功能未启用",
			oai.TypeServer, "agent_not_enabled")
		return
	}
	id, ok := parseAgentKeyID(c)
	if !ok {
		return
	}
	if err := s.deps.AgentKeys.Delete(c.Request.Context(), id); err != nil {
		if strings.Contains(err.Error(), model.ErrAgentKeyNotFound.Error()) {
			oai.WriteError(c.Writer, http.StatusNotFound, "密钥不存在",
				oai.TypeInvalidRequest, "agent_key_not_found")
			return
		}
		s.respondInternalError(c, "删除 agent 密钥失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// parseAgentKeyID 解析路径中的密钥 ID；不合法时已写好错误响应。
func parseAgentKeyID(c *gin.Context) (uint64, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	if raw == "" || len(raw) > agentKeyIDParamMaxDigits {
		oai.WriteError(c.Writer, http.StatusBadRequest, "密钥 ID 不合法",
			oai.TypeInvalidRequest, "invalid_id")
		return 0, false
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		oai.WriteError(c.Writer, http.StatusBadRequest, "密钥 ID 不合法",
			oai.TypeInvalidRequest, "invalid_id")
		return 0, false
	}
	return id, true
}

// findAgentKeyByID 在列表里找指定 ID 的密钥。
func findAgentKeyByID(keys []*model.AgentKey, id uint64) *model.AgentKey {
	for _, key := range keys {
		if key != nil && key.ID == id {
			return key
		}
	}
	return nil
}

// buildAgentKeyResponse 组装密钥列表项。
func buildAgentKeyResponse(key *model.AgentKey) agentKeyResponse {
	resp := agentKeyResponse{
		ID:        key.ID,
		Role:      string(key.Role),
		RoleLabel: key.Role.Label(),
		Name:      key.Name,
		Status:    key.Status,
		Expired:   key.Expired(),
		Active:    key.IsActive(),
		CreatedAt: key.CreatedAt.UTC().Unix(),
	}
	// 永不过期时不下发 expires_at：前端拿到 undefined 就知道"没有期限"，
	// 而拿到 0 会被多数日期控件当成"1970 年已过期"显示。
	if !key.ExpiresAt.IsZero() {
		resp.ExpiresAt = key.ExpiresAt.UTC().Unix()
	}
	return resp
}
