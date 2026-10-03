// 本文件实现管理后台接口：仪表盘、渠道、令牌、用户、日志、系统设置。
//
// 意图（Why）：
//
//	管理后台是站长日常使用的主界面：配置上游渠道、给用户发令牌、看用量与故障。
//	这些接口全部需要管理员权限（路由层用 RequireAdmin 统一守卫），
//	且必须遵守两条安全铁律：
//	  1) 任何响应都不得包含明文密钥（渠道密钥只输出掩码）；
//	  2) 不得因"方便"而允许删除最后一个管理员，否则系统将无人可管理。
//
// 流转（Flow）：
//
//	/api/admin/* → SessionAuth → RequireAdmin → 本文件各处理器 → DTO → JSON
//
// 扩展（Extend）：
//
//	新增管理功能时：在本文件加处理器，在 router.go 的 admin 分组注册，
//	并同步更新 docs/06-前后端接口契约.md。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/channeltype"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/mailer"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
	"github.com/LTZY-ACU/aqua-api/internal/payment"
	"github.com/LTZY-ACU/aqua-api/internal/relay"
	"github.com/LTZY-ACU/aqua-api/internal/server/middleware"
)

// 分页默认值。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// nameLookupLimit 是批量解析名称时一次加载的最大记录数。
//
// 取舍说明：日志列表需要展示"用户名/渠道名"，而日志表只存 ID。
// 逐条查询会造成 N+1（一页 20 条就是 40 次额外查询），因此改为一次性加载映射。
// 对自托管网关（渠道与用户规模通常在数十级别）完全够用；
// 若将来规模显著增长，再改为带条件查询或 SQL JOIN。
const nameLookupLimit = 1000

// ---------------------------------------------------------------------------
// 仪表盘
// ---------------------------------------------------------------------------

// dashboardSectionCount 是仪表盘中"计数"分组的响应结构。
type dashboardSectionCount struct {
	Total        int `json:"total"`
	Enabled      int `json:"enabled"`
	AutoDisabled int `json:"auto_disabled,omitempty"`
}

// handleDashboard 返回仪表盘汇总数据。
func (s *Server) handleDashboard(c *gin.Context) {
	ctx := c.Request.Context()

	// 渠道
	channelCounts, err := s.deps.Channels.StatusCounts(ctx)
	if err != nil {
		s.respondInternalError(c, "统计渠道失败")
		return
	}
	channels := dashboardSectionCount{
		Total:        channelCounts[model.ChannelStatusEnabled] + channelCounts[model.ChannelStatusDisabled] + channelCounts[model.ChannelStatusAutoDisabled],
		Enabled:      channelCounts[model.ChannelStatusEnabled],
		AutoDisabled: channelCounts[model.ChannelStatusAutoDisabled],
	}

	// 令牌
	tokenCounts, err := s.deps.Tokens.StatusCounts(ctx)
	if err != nil {
		s.respondInternalError(c, "统计令牌失败")
		return
	}
	tokens := dashboardSectionCount{
		Total:   tokenCounts[model.TokenStatusEnabled] + tokenCounts[model.TokenStatusDisabled] + tokenCounts[model.TokenStatusExpired] + tokenCounts[model.TokenStatusExhausted],
		Enabled: tokenCounts[model.TokenStatusEnabled],
	}

	// 用户
	enabledStatus := model.UserStatusEnabled
	userTotal, err := s.deps.Users.Count(ctx, model.UserQuery{})
	if err != nil {
		s.respondInternalError(c, "统计用户失败")
		return
	}
	userActive, err := s.deps.Users.Count(ctx, model.UserQuery{Status: &enabledStatus})
	if err != nil {
		s.respondInternalError(c, "统计用户失败")
		return
	}

	// 今日用量与近 7 天趋势
	todayStart := truncateToDay(time.Now())
	recentSince := todayStart.AddDate(0, 0, -6)

	todaySummary, err := s.deps.UsageLogs.Summary(ctx, model.UsageLogQuery{Since: &todayStart})
	if err != nil {
		s.respondInternalError(c, "统计今日用量失败")
		return
	}

	series, err := s.deps.UsageLogs.DailySeries(ctx, model.UsageLogQuery{Since: &recentSince})
	if err != nil {
		s.respondInternalError(c, "统计用量趋势失败")
		return
	}

	topModels, err := s.deps.UsageLogs.TopModels(ctx, model.UsageLogQuery{Since: &recentSince}, 5)
	if err != nil {
		s.respondInternalError(c, "统计模型排行失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channels": channels,
		"users": gin.H{
			"total":  userTotal,
			"active": userActive,
		},
		"tokens": tokens,
		"today": gin.H{
			"requests":     todaySummary.Requests,
			"tokens":       todaySummary.Tokens,
			"quota":        todaySummary.Quota,
			"success_rate": todaySummary.SuccessRate(),
			// 用量细节：把总量拆成输入/输出/缓存/推理，站长才能回答
			// "成本涨在输入还是输出""缓存到底省了多少"。
			"prompt_tokens":         todaySummary.PromptTokens,
			"completion_tokens":     todaySummary.CompletionTokens,
			"cached_tokens":         todaySummary.CachedTokens,
			"reasoning_tokens":      todaySummary.ReasoningTokens,
			"cache_hit_rate":        todaySummary.CacheHitRate(),
			"avg_latency_ms":        todaySummary.AvgLatencyMS(),
			"avg_first_token_ms":    todaySummary.AvgFirstTokenMS(),
			"avg_tokens_per_second": todaySummary.AvgTokensPerSecond(),
		},
		"recent_days": toDailyUsageDTOList(series),
		"top_models":  toModelUsageDTOList(topModels),
	})
}

// ---------------------------------------------------------------------------
// 渠道管理
// ---------------------------------------------------------------------------

// channelUpsertRequest 是创建/更新渠道的请求体。
//
// 关键设计：APIKey 使用指针类型。
//   - nil  表示"未提交该字段" → 更新时保留原密钥（前端编辑时留空即为这种情况）；
//   - ""   表示"显式清空密钥"；
//   - 其他 表示"设置新密钥"。
//
// 若用普通 string，就无法区分"没传"与"传了空串"，
// 结果是管理员只改个名字就把渠道密钥清空了——这类事故很难排查。
type channelUpsertRequest struct {
	Name    string   `json:"name"`
	Type    int      `json:"type"`
	BaseURL string   `json:"base_url"`
	APIKey  *string  `json:"api_key"`
	Models  []string `json:"models"`
	Group   string   `json:"group"`
	// Groups 是本渠道可服务的分组清单（多选）。
	//
	// 与 Group 的关系：Groups 是权威值，主分组恒等于 Groups[0]。
	// 未提交 Groups 时按单分组 Group 处理，兼容只认识 Group 的旧客户端。
	// 之所以提供多选：同一上游常需同时服务多个用户分组，
	// 只能选一个分组会让"没有候选渠道"变成 0ms 的 503。
	Groups   []string `json:"groups"`
	Priority int      `json:"priority"`
	Weight   int      `json:"weight"`
	Status   int      `json:"status"`
	// TypeKey 是渠道类型标识（与 channeltype 目录的 Key 对应，如 azure_openai）。
	//
	// 留空表示"不指定类型"：创建时为空即按 OpenAI 兼容处理；
	// 更新时留空表示"不修改"（见 handleUpdateChannel），避免旧版前端只提交
	// 部分字段时把已配置的类型标识清空，从而让专用渠道静默退化为 OpenAI 兼容。
	TypeKey string `json:"type_key"`
	// ExtraConfig 是类型专属参数（如 Azure 的 deployment / api_version）。
	//
	// 更新时以 nil（字段缺失）表示"不修改"；显式传 {} 表示"清空"。
	ExtraConfig map[string]string `json:"extra_config"`
	// KeyStrategy 是渠道凭据池的调度策略标识。
	//
	// 留空表示"不修改"：更新接口据此刻意不覆盖已有策略，
	// 否则前端只提交部分字段（如仅切换状态）就会把策略重置回默认值。
	KeyStrategy string `json:"key_strategy"`
	// KeyFailurePolicy 是密钥失败处置策略标识（cooldown_only / auto_remove）。
	//
	// 留空表示"不修改"：与 KeyStrategy 同样的保护理由——
	// 前端只提交部分字段（如仅切换启停）时不应把策略重置回默认值。
	KeyFailurePolicy string `json:"key_failure_policy"`
	// KeyCooldownSeconds 是密钥失败后的统一冷却时长（秒）。
	//
	// 用指针区分"未提交"（nil，不修改）与"显式提交 0"（改回内置分级退避）：
	// 站长把自定义时长改回 0 是一个合法且常见的操作，若用零值表示"不修改"，
	// 这个操作就永远做不成。
	KeyCooldownSeconds *int `json:"key_cooldown_seconds"`
	// RetryEnabled 是上游错误重试的总开关。
	//
	// 用指针区分"未提交"（nil，不修改）与"显式提交 false"（关闭重试）：
	// 关闭重试是一个明确诉求（如按次计费的上游怕重复扣费），
	// 若用零值表示"不修改"，这个操作就永远做不成。
	RetryEnabled *bool `json:"retry_enabled"`
	// RetryMaxAttempts 是渠道级重试次数上限（含首次尝试）。
	//
	// 同样用指针：显式提交 0 表示"恢复内置默认次数"（3 次），
	// 这与"未提交、保持原值"是两件事。
	RetryMaxAttempts *int `json:"retry_max_attempts"`
	// ModelRetryRules 是模型级重试覆盖规则。
	//
	// nil（字段缺失）表示"不修改"；显式提交 [] 表示"清空所有模型级规则"。
	ModelRetryRules []model.ModelRetryRule `json:"model_retry_rules"`
	// KeysText 是"批量密钥"文本框内容：每行一把密钥，行内可用空格或逗号附加备注。
	//
	// 为什么用文本而不是 []string：
	//   - 使用者通常从表格/记事本里直接粘几百行，文本是最自然的输入形态；
	//   - 后端解析（model.ParseKeyList）能统一处理空行、注释、备注与去重，
	//     前端只需原样提交，不必自己实现一遍解析规则。
	//
	// 语义：非空时把该渠道的密钥池整体替换为这批密钥（差集增删，幂等）；
	// 为空时不动密钥池（避免"只改个名字却把 500 把密钥清空"）。
	KeysText string `json:"keys_text"`

	// OAuthTokensText 是"批量订阅账号"文本框内容：每行一条 refresh_token，
	// 行内可用空格或逗号附加账号标识（如邮箱）。
	//
	// 与 KeysText 分开的原因：两类凭据的去重标识不同
	// （API Key 用密钥本身，订阅账号用 refresh_token），
	// 混在一个框里会让使用者无法预知"这次导入会替换掉什么"。
	OAuthTokensText string `json:"oauth_tokens_text"`
	// OAuthProvider 是这批订阅账号对应的 OAuth 提供方名称（如 claude）。
	OAuthProvider string `json:"oauth_provider"`
}

// handleListChannels 返回渠道列表（统一分页格式）。
func (s *Server) handleListChannels(c *gin.Context) {
	page, size, offset := parsePagination(c)

	query := model.ChannelQuery{Limit: size, Offset: offset}
	if group := c.Query("group"); group != "" {
		query.Group = group
	}
	if statusRaw := c.Query("status"); statusRaw != "" {
		if parsed, err := strconv.Atoi(statusRaw); err == nil {
			status := model.ChannelStatus(parsed)
			query.Status = &status
		}
	}

	ctx := c.Request.Context()
	channels, err := s.deps.Channels.List(ctx, query)
	if err != nil {
		s.respondInternalError(c, "查询渠道列表失败")
		return
	}
	total, err := s.deps.Channels.Count(ctx, query)
	if err != nil {
		s.respondInternalError(c, "统计渠道总数失败")
		return
	}

	dtos := toChannelDTOList(channels)
	s.attachKeyPoolSummaries(ctx, channels, dtos)

	c.JSON(http.StatusOK, newPagedResponse(dtos, total, page, size))
}

// attachKeyPoolSummaries 为一批渠道补充密钥池概览。
//
// 用一次 GROUP BY 查询覆盖全部渠道，避免"每个渠道查一次"的 N+1 问题
// （渠道列表页通常有几十行，N+1 会让响应时间随渠道数线性增长）。
//
// 查询失败时不阻断列表：密钥池只是展示信息，缺少它不应导致管理页面打不开。
func (s *Server) attachKeyPoolSummaries(ctx context.Context, channels []*model.Channel, dtos []channelDTO) {
	if s.deps.ChannelKeys == nil || len(channels) == 0 {
		return
	}

	ids := make([]uint64, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
	}
	summaries, err := s.deps.ChannelKeys.Summary(ctx, ids)
	if err != nil {
		return
	}
	for i, ch := range channels {
		if summary, ok := summaries[ch.ID]; ok {
			applyKeyPool(&dtos[i], summary)
		}
	}
}

// handleGetChannel 返回单个渠道详情。
func (s *Server) handleGetChannel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	channel, err := s.deps.Channels.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	dto := toChannelDTO(channel)

	// 近 24 小时模型失败统计（"失效模型体检"）。
	//
	// 用途：让站长在渠道详情页直接看到哪些模型最近持续 403/404/410——
	// 403 意味着凭据对该模型无授权，404/410 意味着模型在上游已下线，
	// 这两类模型都该从渠道模型清单里清理。列表接口不跑该查询，
	// 避免几十个渠道各自多一次聚合 SQL（详情页才有必要）。
	if s.deps.UsageLogs != nil {
		if failures, err := s.deps.UsageLogs.ModelFailureStats(c.Request.Context(), id, time.Now().Add(-24*time.Hour)); err == nil {
			dto.ModelFailures = failures
		}
		// 查询失败不回显错误：失败统计是体检信息，缺它不应阻断详情页打开。
	}

	c.JSON(http.StatusOK, dto)
}

// handleCreateChannel 新建渠道。
func (s *Server) handleCreateChannel(c *gin.Context) {
	var req channelUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	// 先解析批量密钥：解析失败时直接返回，避免"渠道建好了但密钥没进去"的半成品状态
	keys, labels, balances, err := parseKeysText(req.KeysText)
	if err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_keys")
		return
	}

	// 调度策略：空值取默认；非法值直接 400 并告知可选值（不静默兜底，
	// 否则管理员填错也"保存成功"，却在转发时按另一套策略调度）。
	strategy, err := parseKeyStrategy(req.KeyStrategy)
	if err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_strategy")
		return
	}

	// 密钥失败策略：同上，非法值直接 400 而不静默兜底——
	// 它决定"密钥失败后是进冷却池还是被摘除"，与站长的运营预期直接相关。
	failurePolicy, err := parseKeyFailurePolicy(req.KeyFailurePolicy)
	if err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_failure_policy")
		return
	}

	channel := &model.Channel{
		Name:        strings.TrimSpace(req.Name),
		Type:        req.Type,
		TypeKey:     strings.TrimSpace(req.TypeKey),
		ExtraConfig: req.ExtraConfig,
		BaseURL:     strings.TrimSpace(req.BaseURL),
		Models:      req.Models,
		Priority:    req.Priority,
		Weight:      defaultIfZero(req.Weight, 1),
		Status:      model.ChannelStatus(defaultIfZero(req.Status, int(model.ChannelStatusEnabled))),
		KeyStrategy: strategy,

		KeyFailurePolicy: failurePolicy,
	}
	if req.KeyCooldownSeconds != nil {
		if err := validateKeyCooldownSeconds(*req.KeyCooldownSeconds); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_cooldown_seconds")
			return
		}
		channel.KeyCooldownSeconds = *req.KeyCooldownSeconds
	}
	// 分组：多选清单为准，主分组恒等于清单第一项（避免两个字段互相矛盾）。
	channel.Groups, channel.Group = resolveChannelGroups(req.Groups, req.Group)
	// 重试策略：创建时未提交的字段保持零值（未配置 = 启用重试，与旧行为一致）。
	applyRetryPolicy(channel, req)
	if req.APIKey != nil {
		channel.APIKey = *req.APIKey
	}

	ctx := c.Request.Context()
	if err := s.deps.Channels.Create(ctx, channel); err != nil {
		// 领域校验的错误信息（如"base_url 必须以 http:// 开头"）对使用者有直接帮助，
		// 且不包含内部细节，因此原样回传，便于管理员自助修正。
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_channel")
		return
	}

	if err := s.replaceChannelKeys(ctx, channel.ID, keys, labels, balances); err != nil {
		s.respondInternalError(c, "导入渠道密钥失败")
		return
	}

	if err := s.replaceChannelOAuthCredentials(ctx, channel, req.OAuthTokensText, req.OAuthProvider); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_oauth_tokens")
		return
	}

	dto, err := s.channelDTOWithPool(ctx, channel)
	if err != nil {
		s.respondInternalError(c, "读取渠道信息失败")
		return
	}
	c.JSON(http.StatusOK, dto)
}

// parseKeysText 预解析"批量密钥"文本。
//
// 返回 (明文密钥列表, 备注列表, 余额列表, 错误)。文本为空时返回 (nil, nil, nil, nil)，
// 表示"本次不修改密钥池"。
//
// 解析走 model.ParseKeyListWithBalance：除"每行一把密钥（可带备注）"外，
// 还支持"余额标记行"（如 `49余额`），为【其后】的密钥批量设定余额，
// 详见该函数的文档。
func parseKeysText(keysText string) ([]string, []string, []int64, error) {
	if strings.TrimSpace(keysText) == "" {
		return nil, nil, nil, nil
	}
	keys, labels, balances := model.ParseKeyListWithBalance(keysText)
	if len(keys) == 0 {
		return nil, nil, nil, errors.New("未能从提交内容中解析出任何密钥，请检查格式（每行一把）")
	}
	return keys, labels, balances, nil
}

// replaceChannelKeys 用给定密钥整体替换渠道密钥池（差集增删，幂等），并录入余额。
func (s *Server) replaceChannelKeys(ctx context.Context, channelID uint64, keys, labels []string, balances []int64) error {
	if s.deps.ChannelKeys == nil || len(keys) == 0 {
		return nil
	}
	_, _, err := s.deps.ChannelKeys.ReplaceAllWithBalance(ctx, channelID, keys, labels, balances)
	return err
}

// replaceChannelOAuthCredentials 按提交的文本同步订阅账号（OAuth）凭据。
//
// 语义与 replaceChannelKeys 一致：文本为空表示"不修改"，
// 避免管理员只改渠道名却把订阅账号清空。
//
// 两类凭据分开导入的额外好处：ReplaceCredentials 只会增删"本次涉及的类型"，
// 因此导入 API Key 不会影响已有的订阅账号，反之亦然。
//
// 参数 channel 用于判断该渠道是否为订阅类协议（Codex）：是的话会顺带
// 确保内置 OAuth 提供方存在，并把凭据挂到它下面——站长因此不必知道
// token_url / client_id 这些参数。
func (s *Server) replaceChannelOAuthCredentials(ctx context.Context, channel *model.Channel, tokensText, provider string) error {
	if strings.TrimSpace(tokensText) == "" {
		return nil
	}
	if s.deps.ChannelKeys == nil {
		return errors.New("凭据池功能未启用")
	}
	if channel == nil {
		return errors.New("渠道不存在")
	}

	provider = strings.TrimSpace(provider)
	if provider == "" && isCodexChannel(channel) {
		// 订阅账号的刷新配置由程序内置，不需要站长填写；
		// 首次导入时自动创建，后续导入复用同一条。
		name, err := s.ensureCodexOAuthProvider(ctx)
		if err != nil {
			return err
		}
		provider = name
	}

	credentials := model.ParseCredentialList(tokensText, provider)
	if len(credentials) == 0 {
		return errors.New("未能从提交内容中解析出任何订阅账号凭据；" +
			"可直接粘贴 Codex 的 auth.json，或每行一条 refresh_token")
	}
	_, _, err := s.deps.ChannelKeys.ReplaceCredentials(ctx, channel.ID, credentials)
	return err
}

// isCodexChannel 判断渠道是否使用 Codex 订阅协议。
//
// 判据取渠道类型目录里的 Protocol，而不是硬编码 type_key 字符串：
// 目录是"哪种类型走哪套协议"的唯一事实来源，硬编码会在目录调整后静默失配。
func isCodexChannel(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	spec, ok := channeltype.Find(strings.TrimSpace(channel.TypeKey))
	return ok && spec.Protocol == channeltype.ProtocolCodex
}

// ensureCodexOAuthProvider 确保内置的 Codex OAuth 提供方存在，并返回其名称。
//
// 幂等：已存在则直接返回（不覆盖管理员可能做过的调整）。
// 仓储未注入时返回内置名称而不报错——那种部署下不会走到刷新，
// 让导入先成功、把问题留到真正需要刷新时暴露会更友好。
func (s *Server) ensureCodexOAuthProvider(ctx context.Context) (string, error) {
	preset := model.CodexOAuthProviderPreset()
	if s.deps.OAuthProviders == nil {
		return preset.Name, nil
	}

	existing, err := s.deps.OAuthProviders.GetByName(ctx, preset.Name)
	if err == nil && existing != nil {
		return existing.Name, nil
	}
	if !errors.Is(err, model.ErrOAuthProviderNotFound) {
		return "", fmt.Errorf("查询内置 OAuth 提供方失败: %w", err)
	}

	// 不存在则创建。并发导入时可能撞上唯一索引，此时按"已存在"处理即可。
	if err := s.deps.OAuthProviders.Create(ctx, preset); err != nil {
		if fallback, getErr := s.deps.OAuthProviders.GetByName(ctx, preset.Name); getErr == nil && fallback != nil {
			return fallback.Name, nil
		}
		return "", fmt.Errorf("创建内置 OAuth 提供方失败: %w", err)
	}
	return preset.Name, nil
}

// channelDTOWithPool 组装带密钥池概览的渠道 DTO。
func (s *Server) channelDTOWithPool(ctx context.Context, channel *model.Channel) (channelDTO, error) {
	dto := toChannelDTO(channel)
	if s.deps.ChannelKeys == nil {
		return dto, nil
	}
	summaries, err := s.deps.ChannelKeys.Summary(ctx, []uint64{channel.ID})
	if err != nil {
		return dto, err
	}
	if summary, ok := summaries[channel.ID]; ok {
		applyKeyPool(&dto, summary)
	}
	return dto, nil
}

// handleUpdateChannel 更新渠道。
func (s *Server) handleUpdateChannel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req channelUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	channel, err := s.deps.Channels.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	// 先解析批量密钥（与创建一致：解析失败直接返回，不留半成品状态）
	keys, labels, balances, err := parseKeysText(req.KeysText)
	if err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_keys")
		return
	}

	// 逐字段覆盖。注意 api_key 只在请求显式提供时才替换，
	// 这样前端"编辑时留空"不会清空已配置的密钥。
	channel.Name = strings.TrimSpace(req.Name)
	channel.Type = req.Type
	channel.BaseURL = strings.TrimSpace(req.BaseURL)
	channel.Models = req.Models
	channel.Groups, channel.Group = resolveChannelGroups(req.Groups, req.Group)
	channel.Priority = req.Priority
	channel.Weight = defaultIfZero(req.Weight, 1)
	channel.Status = model.ChannelStatus(defaultIfZero(req.Status, int(model.ChannelStatusEnabled)))
	// 类型标识留空表示"不修改"：与 KeyStrategy 同理，避免旧版前端只提交
	// 部分字段时把专用渠道的类型清空（那会让 Azure/Anthropic 渠道静默退化为
	// OpenAI 兼容，请求全部打错地址）。非空时由领域层校验是否为已登记类型。
	if strings.TrimSpace(req.TypeKey) != "" {
		channel.TypeKey = strings.TrimSpace(req.TypeKey)
	}
	// 扩展配置：字段缺失（nil）表示不修改，显式传 {} 表示清空。
	if req.ExtraConfig != nil {
		channel.ExtraConfig = req.ExtraConfig
	}
	// 策略留空表示"不修改"：这样只提交部分字段（如启停）也不会把策略重置。
	// 非空时校验合法性，非法值直接 400。
	if strings.TrimSpace(req.KeyStrategy) != "" {
		strategy, err := parseKeyStrategy(req.KeyStrategy)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_strategy")
			return
		}
		channel.KeyStrategy = strategy
	}
	// 密钥失败策略同样"留空即不修改"；显式提交则校验合法性。
	if strings.TrimSpace(req.KeyFailurePolicy) != "" {
		policy, err := parseKeyFailurePolicy(req.KeyFailurePolicy)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_failure_policy")
			return
		}
		channel.KeyFailurePolicy = policy
	}
	// 冷却时长：nil 表示不修改；显式提交（含 0）则按提交值生效。
	if req.KeyCooldownSeconds != nil {
		if err := validateKeyCooldownSeconds(*req.KeyCooldownSeconds); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_key_cooldown_seconds")
			return
		}
		channel.KeyCooldownSeconds = *req.KeyCooldownSeconds
	}
	// 重试策略：nil / 字段缺失一律表示"不修改"，保持该渠道已有的配置。
	applyRetryPolicy(channel, req)
	if req.APIKey != nil {
		channel.APIKey = *req.APIKey
	}

	if err := s.deps.Channels.Update(ctx, channel); err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_channel")
		return
	}

	// 密钥池为空文本时不动（见 parseKeysText 的语义），避免误清空
	if err := s.replaceChannelKeys(ctx, channel.ID, keys, labels, balances); err != nil {
		s.respondInternalError(c, "导入渠道密钥失败")
		return
	}

	if err := s.replaceChannelOAuthCredentials(ctx, channel, req.OAuthTokensText, req.OAuthProvider); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_oauth_tokens")
		return
	}

	dto, err := s.channelDTOWithPool(ctx, channel)
	if err != nil {
		s.respondInternalError(c, "读取渠道信息失败")
		return
	}
	c.JSON(http.StatusOK, dto)
}

// handleDeleteChannel 删除渠道。
func (s *Server) handleDeleteChannel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if err := s.deps.Channels.Delete(ctx, id); err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		s.respondInternalError(c, "删除渠道失败")
		return
	}

	// 级联清理密钥池：渠道已不存在，残留的密钥既无用又属于敏感数据，不应留在库里
	if s.deps.ChannelKeys != nil {
		_ = s.deps.ChannelKeys.DeleteByChannel(ctx, id)
	}

	// 级联清理上游进价：成本规则以渠道为归属，留下只会成为永远不会被读到的孤儿数据
	//（它的模型名还可能与其他渠道重名，将来排查成本问题时极易误导）。
	if s.deps.ChannelModelCosts != nil {
		_ = s.deps.ChannelModelCosts.DeleteByChannel(ctx, id)
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// channelTestResponse 是测活结果。
type channelTestResponse struct {
	OK         bool   `json:"ok"`
	LatencyMS  int    `json:"latency_ms"`
	Model      string `json:"model"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
	// UpstreamBody 是上游响应片段（已截断）。
	//
	// 透出它是为了让管理员看到上游的原话："model not found" 与
	// "insufficient permissions" 指向完全不同的处置动作。
	UpstreamBody string `json:"upstream_body"`
	// UpstreamModel 是本次真正发给上游的模型名（可能被渠道级映射改写）。
	//
	// 测活报 404 时，第一件要确认的就是"上游到底收到了哪个名字"：
	// 映射没生效与上游真没有该模型，处置动作完全不同。
	UpstreamModel string `json:"upstream_model"`

	// 以下字段回答"这次测活用了什么、池里还剩多少"——
	// 没有它们，测活结论无法解释：测活用的是哪把凭据？池是不是已经空了？
	KeyMasked     string `json:"key_masked"`
	KeySource     string `json:"key_source"`
	PoolTotal     int    `json:"pool_total"`
	PoolAvailable int    `json:"pool_available"`
	PoolCooling   int    `json:"pool_cooling"`
	PoolDisabled  int    `json:"pool_disabled"`
	PoolRemoved   int    `json:"pool_removed"`
	PoolExhausted int    `json:"pool_exhausted"`
}

// handleTestChannel 对渠道发起一次真实请求以验证连通性。
func (s *Server) handleTestChannel(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	channel, err := s.deps.Channels.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	// 若渠道配置了密钥池，测活要用池中的一把密钥。
	//
	// 为什么必须这样做：密钥池渠道通常【不填】单密钥（api_key 为空），
	// 若测活仍用 channel.APIKey，这类渠道会永远显示"测活失败"，
	// 管理员会误以为渠道配错了。
	result := s.probeChannel(ctx, channel)

	// 记录测活结果（失败不影响本次响应：测活结果本身就是"可能失败"的信息）
	//
	// 与后台巡检共用 RecordProbeResult：手动点出来的结论和自动巡检的结论落在同一组字段上，
	// 页面才不会同时存在两个互相矛盾的延迟值。
	_ = s.deps.Channels.RecordProbeResult(ctx, model.ChannelProbeResult{
		ID:         channel.ID,
		At:         time.Now(),
		OK:         result.OK,
		LatencyMS:  result.LatencyMS,
		StatusCode: result.StatusCode,
		Model:      result.Model,
	})

	c.JSON(http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// 上游模型列表
// ---------------------------------------------------------------------------

// fetchModelsRequest 是拉取上游模型列表的请求体。
//
// 两种用法：
//   - ChannelID > 0：用该渠道已保存的 base_url 与密钥。适用于"渠道已配好，只是想同步模型清单"；
//   - ChannelID = 0：用请求体里的 base_url 与 api_key。
//     适用于"还没保存渠道，先看看这个上游有哪些模型可勾选"。
type fetchModelsRequest struct {
	ChannelID uint64 `json:"channel_id"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
}

// fetchModelsResponse 是模型列表响应。
type fetchModelsResponse struct {
	Models []string `json:"models"`
	Count  int      `json:"count"`
}

// handleFetchModels 向上游拉取可用模型列表。
//
// 存在的意义：NIM 这类平台上架了几百个模型，人工抄写模型名必然出错
// （名字形如 "meta/llama-3.1-70b-instruct"，错一个字符整条路由就失效）。
func (s *Server) handleFetchModels(c *gin.Context) {
	var req fetchModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	if s.deps.Relay == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "转发引擎未就绪", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)

	if req.ChannelID > 0 {
		channel, err := s.deps.Channels.GetByID(ctx, req.ChannelID)
		if err != nil {
			if errors.Is(err, model.ErrChannelNotFound) {
				oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
				return
			}
			s.respondInternalError(c, "查询渠道失败")
			return
		}
		// 以库中数据为准：避免"编辑现有渠道时用错地址/密钥"，导致拉回来的清单
		// 与实际渠道配置不一致——那比不拉取更危险（会配错模型）
		baseURL = channel.BaseURL
		if apiKey == "" {
			apiKey = channel.APIKey
		}
		// 渠道"只有密钥池、没有单密钥"是常态，此时从池里取一把可用密钥
		if apiKey == "" && s.deps.ChannelKeys != nil {
			if pool, err := s.deps.ChannelKeys.ListUsable(ctx, channel.ID); err == nil && len(pool) > 0 {
				apiKey = model.PickKey(pool).Key
			}
		}
	}

	if baseURL == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "缺少上游地址（base_url）", oai.TypeInvalidRequest, "missing_base_url")
		return
	}

	models, err := s.deps.Relay.FetchModels(ctx, baseURL, apiKey)
	if err != nil {
		// 上游返回的错误信息（如 "invalid api key"）对管理员排查有直接价值，
		// 且不包含网关内部细节，因此原样回传。
		oai.WriteError(c.Writer, http.StatusBadGateway, err.Error(), oai.TypeServer, "fetch_models_failed")
		return
	}

	c.JSON(http.StatusOK, fetchModelsResponse{Models: models, Count: len(models)})
}

// ---------------------------------------------------------------------------
// 渠道密钥池
// ---------------------------------------------------------------------------

// handleListChannelKeys 返回某渠道的密钥池明细（只含掩码，绝不返回明文）。
func (s *Server) handleListChannelKeys(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	if s.deps.ChannelKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "密钥池功能未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	// reveal=1 表示管理员显式要求查看凭据原文（后台「显示明文」开关）。
	//
	// 为什么不默认返回原文：一个渠道可能有几百把密钥，默认返回等于
	// 把整池凭据明文灌进浏览器内存、前端日志与可能的截图里。
	// 默认只给掩码，需要原文时必须显式请求——并且这次读取会被记入操作审计。
	reveal := c.Query("reveal") == "1"

	keys, err := s.deps.ChannelKeys.ListByChannel(c.Request.Context(), id)
	if err != nil {
		s.respondInternalError(c, "查询渠道密钥失败")
		return
	}

	items := toChannelKeyDTOList(keys, reveal)
	if reveal {
		s.recordSecretReveal(c, id, len(items))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items), "revealed": reveal})
}

// recordSecretReveal 记录一次"查看凭据原文"的操作审计。
//
// 为什么读取也要审计：这是唯一会把上游密钥明文带出服务端的接口，
// 一旦发生泄露（或站长怀疑泄露），审计是唯一能回答"谁在什么时候看过"的依据。
// AdminAudit 中间件只记录写操作，因此这里显式补一条。
func (s *Server) recordSecretReveal(c *gin.Context, channelID uint64, count int) {
	if s.deps.Audit == nil {
		return
	}
	entry := &model.AuditLog{
		Method:     http.MethodGet,
		Path:       c.Request.URL.Path,
		Action:     "查看渠道密钥原文",
		Target:     fmt.Sprintf("channel_id=%d", channelID),
		Detail:     fmt.Sprintf("reveal=1，返回 %d 条凭据原文", count),
		StatusCode: http.StatusOK,
		// 用 middleware.ClientIP 而非 gin 的 c.ClientIP()：审计记录里的来源 IP
		// 只有在"直连对端是自家反向代理"时才采信代理头，否则取 TCP 对端地址。
		ClientIP:  middleware.ClientIP(c),
		UserAgent: c.Request.UserAgent(),
	}
	if user, ok := middleware.CurrentUser(c); ok {
		entry.AdminID = user.ID
		entry.AdminUsername = user.Username
	}
	// 审计失败不阻断本次读取：审计是旁路能力，不应影响管理员的正常操作。
	_ = s.deps.Audit.Create(c.Request.Context(), entry)
}

// channelKeyUpdateRequest 是修改单把凭据的请求体。
//
// 路径保持 PUT /api/admin/keys/:keyId，语义是"改状态、调度参数与余额"：
//   - Status 为空表示不改状态；
//   - weight / priority / rpm_limit 为调度参数，三者需同时提供
//     （仓储的 UpdateScheduling 是整组覆盖，缺项会把它误写成 0）；
//   - Balance 为空（nil）表示不改余额；-1 表示置为"未知"（未录入）；
//     >=0 表示设为该余额（0 即视为已用尽，该凭据会退出调度）。
type channelKeyUpdateRequest struct {
	Status   *int   `json:"status"`
	Weight   *int   `json:"weight"`
	Priority *int   `json:"priority"`
	RPMLimit *int   `json:"rpm_limit"`
	Balance  *int64 `json:"balance"`
	// Groups / Models 是本凭据可服务的分组与模型（迁移 0038，凭据级分叉）。
	//
	// 用指针区分"未提交"（nil，不修改）与"显式提交空数组"（清空限制 = 不限）：
	// 把某把凭据从"只服务自营组"改回"不限分组"是常见操作，
	// 若用空数组兼任"不修改"，这个操作就永远做不成。
	//
	// 两者需同时提供（UpdateRouting 是整组覆盖，缺项会被误写成"不限"）。
	Groups *[]string `json:"groups"`
	Models *[]string `json:"models"`
}

// channelKeyQuotaResponse 是额度探测的响应。
//
// 为什么同时回传"已落库的字段"：前端据此立即刷新那一行，不必再整表拉一次。
//
// 主 / 次两个窗口都回传：上游对订阅账号给出"5 小时"与"每周"两条独立的额度线，
// 前端要同时渲染两条进度条，只给主窗口会让周额度的预警信息在探测后凭空消失。
type channelKeyQuotaResponse struct {
	KeyID    uint64 `json:"key_id"`
	PlanType string `json:"plan_type"`
	Email    string `json:"email"`

	// 主窗口（5 小时）。
	UsedPercent          int   `json:"used_percent"`
	ResetAt              int64 `json:"reset_at"`
	PrimaryWindowSeconds int   `json:"primary_window_seconds"`

	// 次窗口（每周）；used_percent 为 -1 表示上游未提供。
	SecondaryUsedPercent   int   `json:"secondary_used_percent"`
	SecondaryResetAt       int64 `json:"secondary_reset_at"`
	SecondaryWindowSeconds int   `json:"secondary_window_seconds"`

	LimitReached bool   `json:"limit_reached"`
	Note         string `json:"note"`
}

// handleProbeChannelKeyQuota 查询某把订阅账号凭据的额度快照并落库。
//
// 存在的意义：订阅账号的额度只有上游知道，不主动问就只能等"被限流"才发现，
// 而那时用户已经拿到一次失败。站长在后台点一下即可看到每个账号的
// 套餐、已用百分比与重置时间；结果落库后调度会自动跳过已用满的账号。
func (s *Server) handleProbeChannelKeyQuota(c *gin.Context) {
	channelID, ok := parseIDParam(c)
	if !ok {
		return
	}
	keyID, err := strconv.ParseUint(c.Param("keyId"), 10, 64)
	if err != nil || keyID == 0 {
		oai.WriteError(c.Writer, http.StatusBadRequest, "密钥 ID 非法", oai.TypeInvalidRequest, "invalid_id")
		return
	}
	if s.deps.Relay == nil || s.deps.ChannelKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "转发引擎或凭据池未就绪",
			oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	channel, err := s.deps.Channels.GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return
		}
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	// 逐条找目标凭据：凭据池接口只提供"按渠道列出"，
	// 额外加一个 GetByID 只会为这一处需求扩大仓储接口。
	keys, err := s.deps.ChannelKeys.ListByChannel(ctx, channelID)
	if err != nil {
		s.respondInternalError(c, "读取凭据池失败")
		return
	}
	var target *model.ChannelKey
	for _, item := range keys {
		if item.ID == keyID {
			target = item
			break
		}
	}
	if target == nil {
		oai.WriteError(c.Writer, http.StatusNotFound, "凭据不存在", oai.TypeInvalidRequest, "key_not_found")
		return
	}

	quota, err := s.deps.Relay.QueryCodexQuota(ctx, channel, target)
	if err != nil {
		// 探测失败要落"未知"而不是保留旧值：旧快照会继续影响调度判断，
		// 让一个可能已经恢复的账号被继续跳过。主 / 次两个窗口一起置回未知，
		// 避免界面出现"主窗口未查询、次窗口还是旧百分比"的矛盾状态。
		_ = s.deps.ChannelKeys.UpdateQuota(ctx, keyID, model.QuotaWindows{
			PrimaryUsedPercent:   model.QuotaUsedPercentUnknown,
			SecondaryUsedPercent: model.QuotaUsedPercentUnknown,
			CheckedAt:            time.Now(),
		})
		oai.WriteError(c.Writer, http.StatusBadGateway, err.Error(),
			oai.TypeServer, "quota_probe_failed")
		return
	}

	now := time.Now()
	if err := s.deps.ChannelKeys.UpdateQuota(ctx, keyID, model.QuotaWindows{
		PrimaryUsedPercent:     quota.UsedPercent,
		PrimaryResetAt:         quota.ResetAt,
		PrimaryWindowSeconds:   quota.PrimaryWindowSeconds,
		SecondaryUsedPercent:   quota.SecondaryUsedPercent,
		SecondaryResetAt:       quota.SecondaryResetAt,
		SecondaryWindowSeconds: quota.SecondaryWindowSeconds,
		CheckedAt:              now,
	}); err != nil {
		s.respondInternalError(c, "保存额度快照失败")
		return
	}
	// 顺带补齐套餐：探测是"免费"的额外信息，没必要让站长再手工维护一遍。
	// 只写非空值（空串表示本次没拿到，不该把已知套餐抹掉）。
	if strings.TrimSpace(quota.PlanType) != "" {
		_ = s.deps.ChannelKeys.UpdateAccountMeta(ctx, keyID, "", quota.PlanType)
	}

	note := "额度快照已更新；已用满的账号会自动跳过，窗口重置后自动回到池中"
	if quota.UsedPercent == model.QuotaUsedPercentUnknown {
		note = "上游未返回额度窗口（该账号可能不按窗口计费，或套餐不支持查询）"
	}
	c.JSON(http.StatusOK, channelKeyQuotaResponse{
		KeyID:    keyID,
		PlanType: quota.PlanType,
		Email:    quota.Email,

		UsedPercent:          quota.UsedPercent,
		ResetAt:              unixOrZeroTime(quota.ResetAt),
		PrimaryWindowSeconds: quota.PrimaryWindowSeconds,

		SecondaryUsedPercent:   quota.SecondaryUsedPercent,
		SecondaryResetAt:       unixOrZeroTime(quota.SecondaryResetAt),
		SecondaryWindowSeconds: quota.SecondaryWindowSeconds,

		LimitReached: quota.LimitReached,
		Note:         note,
	})
}

// unixOrZeroTime 把时间转成 Unix 秒；零值时间返回 0。
func unixOrZeroTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

// handleUpdateChannelKeyStatus 更新某把密钥的状态、调度参数与余额。
//
// 典型场景：
//   - 恢复被误杀（连续失败自动摘除）的密钥，或临时禁用正在被限流的密钥；
//   - 调整该凭据的权重 / 优先级 / 每分钟上限（配合渠道级的调度策略）；
//   - 录入 / 更新该凭据的余额（余额耗尽会自动退出调度，补录后自动恢复）。
func (s *Server) handleUpdateChannelKeyStatus(c *gin.Context) {
	keyID, err := strconv.ParseUint(c.Param("keyId"), 10, 64)
	if err != nil || keyID == 0 {
		oai.WriteError(c.Writer, http.StatusBadRequest, "密钥 ID 非法", oai.TypeInvalidRequest, "invalid_id")
		return
	}

	var req channelKeyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}
	if req.Status == nil && req.Weight == nil && req.Priority == nil && req.RPMLimit == nil &&
		req.Balance == nil && req.Groups == nil && req.Models == nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "未提供任何可更新字段", oai.TypeInvalidRequest, "empty_update")
		return
	}
	if s.deps.ChannelKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable, "密钥池功能未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	resp := gin.H{"ok": true}

	// 1) 状态（可选）
	if req.Status != nil {
		status := model.ChannelKeyStatus(*req.Status)
		if !status.IsValid() {
			oai.WriteError(c.Writer, http.StatusBadRequest, "密钥状态非法", oai.TypeInvalidRequest, "invalid_status")
			return
		}
		if err := s.deps.ChannelKeys.UpdateStatus(ctx, keyID, status); err != nil {
			s.respondKeyUpdateError(c, err)
			return
		}
		resp["status"] = int(status)
		resp["status_text"] = status.String()
	}

	// 2) 调度参数（可选）：三项必须同时给，否则未给项会被写 0 造成意外停用
	if req.Weight != nil || req.Priority != nil || req.RPMLimit != nil {
		if req.Weight == nil || req.Priority == nil || req.RPMLimit == nil {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"更新调度参数时需同时提供 weight / priority / rpm_limit 三项",
				oai.TypeInvalidRequest, "incomplete_scheduling")
			return
		}
		if *req.Weight < 0 || *req.Priority < 0 || *req.RPMLimit < 0 {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"权重 / 优先级 / 每分钟上限不能为负数", oai.TypeInvalidRequest, "invalid_scheduling")
			return
		}
		if err := s.deps.ChannelKeys.UpdateScheduling(ctx, keyID, *req.Weight, *req.Priority, *req.RPMLimit); err != nil {
			s.respondKeyUpdateError(c, err)
			return
		}
		resp["weight"] = *req.Weight
		resp["priority"] = *req.Priority
		resp["rpm_limit"] = *req.RPMLimit
	}

	// 3) 余额（可选）：nil 不修改；-1 置为未知；>=0 设为该值（0 表示已用尽）。
	//
	// 余额是运营数据、不参与计费；它只影响"是否还把这把凭据纳入调度"。
	if req.Balance != nil {
		if *req.Balance < model.BalanceUnknown {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				fmt.Sprintf("余额不能小于 %d（%d 表示未知，0 表示已用尽）", model.BalanceUnknown, model.BalanceUnknown),
				oai.TypeInvalidRequest, "invalid_balance")
			return
		}
		if err := s.deps.ChannelKeys.UpdateBalance(ctx, keyID, *req.Balance); err != nil {
			s.respondKeyUpdateError(c, err)
			return
		}
		resp["balance"] = *req.Balance
		resp["balance_unknown"] = *req.Balance == model.BalanceUnknown
		resp["balance_exhausted"] = *req.Balance >= 0 && *req.Balance <= 0
	}

	// 4) 分组 / 模型分叉（可选，迁移 0038）：决定"这把凭据服务哪些分组与模型"。
	//
	// 两者必须同时提供：UpdateRouting 是整组覆盖，只给一项会把另一项误写成"不限"，
	// 而"不限"意味着这把凭据会重新参与所有请求——一个不易察觉的越权分叉。
	if req.Groups != nil || req.Models != nil {
		if req.Groups == nil || req.Models == nil {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"更新分组/模型限制时需同时提供 groups 与 models 两项",
				oai.TypeInvalidRequest, "incomplete_routing")
			return
		}
		groups := model.NormalizeGroupNames(*req.Groups)
		models := model.NormalizeGroupNames(*req.Models)
		if err := validateKeyRoutingModels(models); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_routing")
			return
		}
		if err := s.deps.ChannelKeys.UpdateRouting(ctx, keyID, groups, models); err != nil {
			s.respondKeyUpdateError(c, err)
			return
		}
		resp["groups"] = groups
		resp["models"] = models
	}

	c.JSON(http.StatusOK, resp)
}

// validateKeyRoutingModels 校验凭据级"可服务模型"清单。
//
// 只做两件必要的事：不接受空项（空项等于一条永不命中的规则，纯属配置噪声），
// 不接受含空白的模型名（它永远匹配不上任何模型，站长却会以为规则生效了）。
func validateKeyRoutingModels(models []string) error {
	for _, model := range models {
		name := strings.TrimSpace(model)
		if name == "" {
			return errors.New("模型清单里存在空项")
		}
		if strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("模型名 %q 不能包含空白字符（否则永远匹配不上）", name)
		}
	}
	return nil
}

// respondKeyUpdateError 统一翻译密钥更新的领域错误。
func (s *Server) respondKeyUpdateError(c *gin.Context, err error) {
	if errors.Is(err, model.ErrChannelKeyNotFound) {
		oai.WriteError(c.Writer, http.StatusNotFound, "密钥不存在", oai.TypeInvalidRequest, "key_not_found")
		return
	}
	s.respondInternalError(c, "更新密钥失败")
}

// ---------------------------------------------------------------------------
// 凭据调度策略目录
// ---------------------------------------------------------------------------

// keyStrategyDTO 描述一种凭据调度策略（供后台渲染下拉与帮助文案）。
//
// 说明文案由后端下发（取自领域层），前端不硬编码：
// 这样新增策略时只需改后端一处，界面自动跟随。
type keyStrategyDTO struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// handleListKeyStrategies 返回全部凭据调度策略。
//
// 只读接口：策略取值是稳定枚举，站长只能"选用"，不能自定义，
// 因此这里不做鉴权以外的任何副作用，也不需要分页。
func (s *Server) handleListKeyStrategies(c *gin.Context) {
	items := make([]keyStrategyDTO, 0, len(model.KeyStrategyAll()))
	for _, strategy := range model.KeyStrategyAll() {
		items = append(items, keyStrategyDTO{
			Key:         string(strategy),
			Label:       strategy.String(),
			Description: strategy.Description(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// parseKeyStrategy 解析并校验渠道凭据调度策略。
//
// 空值取默认策略（least_in_flight）；非法值返回带可选值的错误，
// 由调用方转成 400 反馈给管理员。
func parseKeyStrategy(raw string) (model.KeyStrategy, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return model.DefaultKeyStrategy(), nil
	}
	strategy := model.KeyStrategy(trimmed)
	if !strategy.IsValid() {
		return "", fmt.Errorf("凭据调度策略非法: %q（可选：%s）", trimmed, model.KeyStrategyOptionText())
	}
	return strategy, nil
}

// ---------------------------------------------------------------------------
// 密钥失败处置策略目录
// ---------------------------------------------------------------------------

// handleListKeyFailurePolicies 返回全部密钥失败处置策略。
//
// 与调度策略目录同理：文案由后端领域层下发，前端不硬编码，
// 新增策略时只改后端一处即可让界面跟随。
func (s *Server) handleListKeyFailurePolicies(c *gin.Context) {
	policies := model.KeyFailurePolicyAll()
	items := make([]keyStrategyDTO, 0, len(policies))
	for _, policy := range policies {
		items = append(items, keyStrategyDTO{
			Key:         string(policy),
			Label:       policy.String(),
			Description: policy.Description(),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		// 明确下发上限与默认值：前端据此限制输入并给出"留 0 用系统默认"的提示，
		// 避免站长填一个 999999 秒然后困惑"密钥怎么再也不回来了"。
		"max_cooldown_seconds":   model.MaxKeyCooldownSeconds,
		"default_failure_policy": string(model.DefaultKeyFailurePolicy()),
	})
}

// parseKeyFailurePolicy 解析并校验密钥失败处置策略。
//
// 空值取默认策略（只冷却不摘除）；非法值返回带可选值的错误，
// 由调用方转成 400 反馈给管理员。
func parseKeyFailurePolicy(raw string) (model.KeyFailurePolicy, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return model.DefaultKeyFailurePolicy(), nil
	}
	policy := model.KeyFailurePolicy(trimmed)
	if !policy.IsValid() {
		return "", fmt.Errorf("密钥失败策略非法: %q（可选：%s）", trimmed, model.KeyFailurePolicyOptionText())
	}
	return policy, nil
}

// validateKeyCooldownSeconds 校验管理员显式提交的冷却时长。
//
// 为什么这里是 400 而不是"夹到上限"：仓储层的归一（NormalizeKeyCooldownSeconds）
// 是给历史脏数据兜底的，属于"读取时的容错"；而管理员显式提交一个越界值
// 是输入错误，静默改成别的数字会让他以为设置生效了，实际却不是他要的值。
// 两者目的不同，因此读取夹取、写入报错。
func validateKeyCooldownSeconds(seconds int) error {
	if seconds < 0 || seconds > model.MaxKeyCooldownSeconds {
		return fmt.Errorf("密钥冷却时长必须在 0 ~ %d 秒之间（0 表示使用系统内置的分级退避）",
			model.MaxKeyCooldownSeconds)
	}
	return nil
}

// applyRetryPolicy 把请求里的重试配置应用到渠道实体（创建与更新共用）。
//
// 抽成一个函数的原因：两条写路径对同一组字段的解析口径必须完全一致，
// 否则会出现"创建时归一、更新时没归一"这种不对称——同一份输入在两条路径上
// 落库结果不同，排查起来极为费劲。
//
// 只做归一化（去空白/去重/去空项）；取值是否合法交给 model.Channel.Validate——
// 领域校验是唯一权威，在这里再写一套判断只会多出一处可能走偏的规则。
func applyRetryPolicy(channel *model.Channel, req channelUpsertRequest) {
	if req.RetryEnabled != nil {
		channel.RetryMode = model.RetryModeOn
		if !*req.RetryEnabled {
			channel.RetryMode = model.RetryModeOff
		}
	}
	if req.RetryMaxAttempts != nil {
		channel.RetryMaxAttempts = *req.RetryMaxAttempts
	}
	if req.ModelRetryRules != nil {
		channel.ModelRetryRules = model.NormalizeModelRetryRules(req.ModelRetryRules)
	}
}

// resolveChannelGroups 解析渠道的分组清单，并给出主分组（清单第一项）。
//
// 规则（唯一真相，前端与文档据此对齐）：
//   - 提交了 groups（多选）→ 以它为准，去空白去重、保持顺序；
//   - 只提交 group（旧客户端）→ 等价于 groups = [group]；
//   - 两者都没提交 → 落到默认分组 default（与改动前一致）。
//
// 返回值恒非空：一个不属于任何分组的渠道既不会被任何请求选中，
// 也不会出现在任何分组页面里，属于"配了但永远不会生效"的静默失效，必须避免。
func resolveChannelGroups(groups []string, single string) (list []string, primary string) {
	normalized := model.NormalizeGroupNames(groups)
	if len(normalized) == 0 {
		normalized = []string{defaultIfEmpty(strings.TrimSpace(single), defaultChannelGroup)}
	}
	return normalized, normalized[0]
}

// probeChannel 向渠道发起一次最小请求，用于验证连通、协议与凭据有效性。
//
// 实现要点（本次全面重做，前三条是修复项）：
//  1. 【按渠道类型构造请求】由 relay.ProbeChannel 完成，与真实转发复用同一套
//     路径模板与鉴权逻辑，因此 Azure / Anthropic / Gemini / Bedrock / Vertex
//     等非 OpenAI 协议渠道也能被正确测活（旧实现硬编码 OpenAI 协议，这些渠道必然误报失败）；
//  2. 【从密钥池挑可用凭据】跳过正在冷却与余额已耗尽的密钥，且按 ID 取第一把可用的，
//     保证同一状态下的测活结论可复现、可解释（旧实现随机挑，还可能挑到冷却中的密钥）；
//  3. 【上游原话透出】返回响应片段，让管理员看到 "model not found" 还是
//     "insufficient permissions"，而不是一个无法行动的"测活失败"；
//  4. 只发 max_tokens=1 的最小请求，尽量少消耗上游额度；
//  5. 超时与转发链路一致（relay.UpstreamTimeout）：部分平台排队本身就久，
//     短超时会给出"渠道坏了"的错误结论；
//  6. 测活【不】修改凭据的失败计数与冷却状态——它是只读探测，不能因为一次
//     后台点击就把正在服务的密钥打进冷却。
func (s *Server) probeChannel(ctx context.Context, channel *model.Channel) channelTestResponse {
	if len(channel.Models) == 0 {
		// 未声明模型时无法构造有效请求：明确告知管理员先补模型列表，
		// 而不是随便挑一个模型去撞（那会产生"测活失败但渠道其实正常"的误导）。
		return channelTestResponse{
			OK:      false,
			Model:   "",
			Message: "该渠道未声明任何模型，请先在渠道配置中填写模型列表后再测活",
		}
	}

	// ── 选本次探测使用的凭据（池优先，与转发链路的取用顺序一致）──────────
	apiKey, cred, credErr := s.pickProbeCredential(ctx, channel)
	result := channelTestResponse{KeyMasked: cred.masked, KeySource: cred.source}
	result.PoolTotal = cred.poolTotal
	result.PoolAvailable = cred.poolAvailable
	result.PoolCooling = cred.poolCooling
	result.PoolDisabled = cred.poolDisabled
	result.PoolRemoved = cred.poolRemoved
	result.PoolExhausted = cred.poolExhausted
	if credErr != "" {
		result.Message = credErr
		return result
	}

	if s.deps.Relay == nil {
		result.Message = "转发引擎未就绪，无法测活"
		return result
	}

	// 超时与转发链路保持一致（见本函数说明第 5 条）。
	probeCtx, cancel := context.WithTimeout(ctx, relay.UpstreamTimeout)
	defer cancel()

	// ── 逐个探测直到命中一个可用模型（2026-09-30 修复线上测活误报）─────
	//
	// 此前只测 Models[0]，一旦清单里第一个模型在上游已不存在（404/410 EOL），
	// 渠道就会一直显示"测活失败"，即使其余 20+ 个模型全部正常——
	// 线上实测：NVIDIA 渠道第一个模型 starcoder2-15b 已下架，
	// 渠道因此常年 last_test_ok=0，掩盖了真实可用性。
	//
	// 新策略：按清单顺序逐个探测，命中 2xx 立即判定成功；
	// 全部失败时取第一个失败的详情回传（管理员可据此清理失效模型）。
	var (
		probe          relay.ProbeResult
		firstFailure   relay.ProbeResult
		skipped        []string
		probedModels   []string
		skippedUnknown []string
	)
	for _, raw := range channel.Models {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if len(skippedUnknown) > 0 {
			skipped = append(skipped, name)
			continue
		}
		probe = s.deps.Relay.ProbeChannel(probeCtx, channel, apiKey, name)
		probedModels = append(probedModels, name)
		if probe.StatusCode >= 200 && probe.StatusCode < 300 {
			// 命中可用模型：测活成功，附带本次实际探测到的模型名
			result.Model = name
			result.LatencyMS = probe.LatencyMS
			result.StatusCode = probe.StatusCode
			result.UpstreamBody = probe.Body
			result.UpstreamModel = probe.UpstreamModel
			note := keySourceText(cred)
			if probe.UpstreamModel != "" && probe.UpstreamModel != name {
				note = fmt.Sprintf("模型映射：%s → %s", name, probe.UpstreamModel)
			}
			skipped = append(skipped, channel.Models[len(probedModels):]...)
			skipped = append(skipped, skippedUnknown...)
			result.Message = describeProbeStatus(probe.StatusCode, probe.Body, note)
			if len(skipped) > 0 {
				result.Message += fmt.Sprintf("　（其余 %d 个模型未逐一探测：%s）",
					len(skipped), strings.Join(skipped, ", "))
			}
			result.OK = true
			return result
		}
		// 记录首个失败详情（含网络错误），供全部失败时汇报
		if probe.Err != nil && firstFailure.Err == nil {
			firstFailure = probe
			if probe.TimedOut() {
				// 超时不代表模型失效，可能只是上游慢——跳过它继续测下一个
				skippedUnknown = append(skippedUnknown, name)
			}
		}
		if probe.StatusCode >= 400 && firstFailure.Err == nil {
			firstFailure = probe
		}
	}

	// 全部失败：回传第一个失败模型的详情
	result.Model = firstFailure.UpstreamModel
	result.LatencyMS = firstFailure.LatencyMS
	result.StatusCode = firstFailure.StatusCode
	result.UpstreamBody = firstFailure.Body
	if firstFailure.Err != nil {
		if firstFailure.TimedOut() {
			result.Message = fmt.Sprintf("全部模型均等待超时（超过 %d 秒）。"+
				"这不代表渠道不可用（部分平台排队时间很长），建议稍后再测或直接交给实际调用验证。",
				int(relay.UpstreamTimeout.Seconds()))
			return result
		}
		result.Message = "无法连接到上游（网络不通或地址有误）：" + firstFailure.Err.Error()
		return result
	}
	result.Message = describeProbeStatus(firstFailure.StatusCode, firstFailure.Body,
		keySourceText(cred))
	return result
}

// probeCredential 描述本次探测所用凭据的来路与池内状况。
type probeCredential struct {
	masked        string
	source        string // pool / single
	poolTotal     int
	poolAvailable int
	poolCooling   int
	poolDisabled  int
	poolRemoved   int
	poolExhausted int
}

// keySourceText 返回凭据来源的中文说明，用于拼进测活结论。
func keySourceText(cred probeCredential) string {
	if cred.poolTotal == 0 {
		return "渠道单密钥"
	}
	if cred.masked == "" {
		return ""
	}
	return fmt.Sprintf("密钥池（池内可用 %d/%d，本次使用 %s）", cred.poolAvailable, cred.poolTotal, cred.masked)
}

// pickProbeCredential 选出一把用于测活的凭据，并给出池内统计。
//
// 选择规则（确定性，便于解释与复现）：
//   - 渠道配了密钥池 → 只从池里挑：跳过"非启用 / 冷却中 / 余额已耗尽"，
//     在剩下的里取 ID 最小的一把；
//   - 没有池 → 用渠道的单密钥；
//   - 都没有 → 返回可操作的错误说明（区分"池空了"与"从未配置"）。
//
// 池内没有可用凭据时【不】退回到单密钥：那会让"池已耗尽"这一关键事实被掩盖，
// 管理员会以为渠道还能用，实际流量早已无处可去。
func (s *Server) pickProbeCredential(ctx context.Context, channel *model.Channel) (string, probeCredential, string) {
	var cred probeCredential

	if s.deps.ChannelKeys == nil {
		cred.source = "single"
		return strings.TrimSpace(channel.APIKey), cred, probeEmptySingleKey(channel)
	}

	keys, err := s.deps.ChannelKeys.ListByChannel(ctx, channel.ID)
	if err != nil {
		// 读池失败时退回单密钥：至少还能给一个结论，而不是直接把测活判为失败。
		cred.source = "single"
		return strings.TrimSpace(channel.APIKey), cred, probeEmptySingleKey(channel)
	}
	if len(keys) == 0 {
		cred.source = "single"
		return strings.TrimSpace(channel.APIKey), cred, probeEmptySingleKey(channel)
	}

	cred.source = "pool"
	cred.poolTotal = len(keys)
	now := time.Now()
	var picked *model.ChannelKey
	for _, key := range keys {
		switch {
		case key.Status == model.ChannelKeyStatusDisabled:
			cred.poolDisabled++
			continue
		case key.Status == model.ChannelKeyStatusAutoRemoved:
			cred.poolRemoved++
			continue
		case key.CoolingDown(now):
			// 冷却中的密钥此刻本就不可用：选它只会得到"限流/鉴权失败"这类
			// 与渠道无关的结论，是对管理员最有误导性的一种结果。
			cred.poolCooling++
			continue
		case key.BalanceExhausted():
			cred.poolExhausted++
			continue
		}
		cred.poolAvailable++
		if picked == nil { // ListByChannel 已按 ID 升序，故第一把即 ID 最小
			picked = key
		}
	}

	if picked == nil {
		return "", cred, fmt.Sprintf(
			"密钥池当前没有可用密钥（共 %d 把：冷却中 %d、已禁用 %d、已摘除 %d、余额已耗尽 %d）。"+
				"请到「密钥池明细」查看，或等待冷却到期后重试。",
			cred.poolTotal, cred.poolCooling, cred.poolDisabled, cred.poolRemoved, cred.poolExhausted)
	}
	cred.masked = picked.Masked()
	return picked.CredentialValue(), cred, ""
}

// probeEmptySingleKey 在"没有可用单密钥"时给出可操作的提示。
func probeEmptySingleKey(channel *model.Channel) string {
	if strings.TrimSpace(channel.APIKey) != "" {
		return ""
	}
	return "该渠道没有任何可用凭据：既未配置密钥池，单密钥也是空的。请填写密钥或导入批量密钥。"
}

// describeProbeStatus 把上游状态码与响应片段翻译成管理员能直接行动的结论。
//
// 参数 body 是上游原话（可能为空）；把它拼进结论里，管理员无需再去抓包。
func describeProbeStatus(status int, body, source string) string {
	suffix := ""
	if trimmed := strings.TrimSpace(body); trimmed != "" {
		suffix = "　上游返回：" + trimmed
	}
	where := ""
	if source != "" {
		where = "（" + source + "）"
	}

	switch {
	case status >= 200 && status < 300:
		return "连通正常，上游已正常应答" + where + suffix
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "上游鉴权失败：该凭据可能无效、无权限，或类型/鉴权方式选错" + where + suffix
	case status == http.StatusTooManyRequests:
		return "上游返回限流：凭据有效但当前受限（可降低并发或等待冷却）" + where + suffix
	case status == http.StatusNotFound:
		// 404 有三种成因，必须区分开，否则管理员会一直去改 BaseURL：
		//   1) 该密钥/账号无权访问这个模型（NVIDIA 等平台按模型逐个授权）；
		//   2) 模型名在上游不存在（映射配错或上游改名）；
		//   3) BaseURL 填错（注意不要带 /v1）。
		return "上游返回 404：可能是该凭据无权访问此模型、模型名在上游不存在，也可能是 BaseURL 填错（不要带 /v1）" +
			where + suffix
	case status == http.StatusPaymentRequired:
		return "上游返回 402：该账号额度/账单异常" + where + suffix
	case status >= 500:
		return fmt.Sprintf("上游返回 %d：上游服务端异常（通常是上游临时故障，可稍后重试）", status) + where + suffix
	default:
		return fmt.Sprintf("上游返回异常状态码 %d", status) + where + suffix
	}
}

// ---------------------------------------------------------------------------
// 令牌管理（管理员可操作全部令牌）
// ---------------------------------------------------------------------------

// adminTokenCreateRequest 是管理员创建令牌的请求体。
//
// 该结构体同时被用户门户复用（门户会忽略 user_id 并强制归属当前用户）。
type adminTokenCreateRequest struct {
	UserID         uint64   `json:"user_id"`
	Name           string   `json:"name"`
	ExpiresInDays  int      `json:"expires_in_days"`
	Models         []string `json:"models"`
	UnlimitedQuota bool     `json:"unlimited_quota"`
	RemainQuota    int64    `json:"remain_quota"`
	// GroupName 是令牌所属分组标识；留空表示使用网关默认分组。
	// 非空时必须指向一个已存在的分组（handler 会校验）。
	GroupName string `json:"group_name"`
}

// handleAdminListTokens 返回全部令牌。
func (s *Server) handleAdminListTokens(c *gin.Context) {
	page, size, offset := parsePagination(c)

	query := model.TokenQuery{Limit: size, Offset: offset}
	if ownerRaw := c.Query("user_id"); ownerRaw != "" {
		if parsed, err := strconv.ParseUint(ownerRaw, 10, 64); err == nil {
			query.OwnerID = &parsed
		}
	}
	if statusRaw := c.Query("status"); statusRaw != "" {
		if parsed, err := strconv.Atoi(statusRaw); err == nil {
			status := model.TokenStatus(parsed)
			query.Status = &status
		}
	}
	// 按分组筛选令牌（后台"看看某分组下有多少把密钥"的场景）。
	// 需注意：这里按落库原值精确匹配，传 default 只筛出显式设为 default 的令牌，
	// 不含"未设置分组、运行期回退默认"的令牌。
	if groupRaw := c.Query("group"); groupRaw != "" {
		group := groupRaw
		query.GroupName = &group
	}

	ctx := c.Request.Context()
	tokens, err := s.deps.Tokens.List(ctx, query)
	if err != nil {
		s.respondInternalError(c, "查询令牌列表失败")
		return
	}
	total, err := s.deps.Tokens.Count(ctx, query)
	if err != nil {
		s.respondInternalError(c, "统计令牌总数失败")
		return
	}

	// 补齐归属用户名（日志与列表都需要展示"谁的令牌"）
	usernames := s.loadUsernames(ctx)
	now := time.Now()

	items := make([]tokenDTO, 0, len(tokens))
	for _, token := range tokens {
		dto := toTokenDTO(token, token.EffectiveStatus(now))
		dto.Username = usernames[token.OwnerID]
		items = append(items, dto)
	}

	c.JSON(http.StatusOK, newPagedResponse(items, total, page, size))
}

// handleAdminCreateToken 为指定用户创建令牌。
func (s *Server) handleAdminCreateToken(c *gin.Context) {
	var req adminTokenCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()

	// 归属用户必须存在，否则令牌会变成"无主令牌"，用量无法归属到任何人
	if _, err := s.deps.Users.GetByID(ctx, req.UserID); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "归属用户不存在", oai.TypeInvalidRequest, "user_not_found")
		return
	}

	s.createTokenAndRespond(c, req.UserID, req.Name, req.ExpiresInDays, req.Models, req.UnlimitedQuota, req.RemainQuota, req.GroupName)
}

// handleAdminUpdateToken 更新任意令牌。
func (s *Server) handleAdminUpdateToken(c *gin.Context) {
	s.updateToken(c, 0) // 0 表示不限制归属（管理员权限）
}

// handleAdminDeleteToken 删除任意令牌。
func (s *Server) handleAdminDeleteToken(c *gin.Context) {
	s.deleteToken(c, 0)
}

// ---------------------------------------------------------------------------
// 用户管理
// ---------------------------------------------------------------------------

// adminUserUpsertRequest 是创建/更新用户的请求体。
type adminUserUpsertRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Role     int    `json:"role"`
	Status   int    `json:"status"`
	Quota    *int64 `json:"quota"`
	// AgentGroup 是代理分组名（空串 = 普通用户）。
	//
	// 用指针而非字符串：更新时"没传这个字段"必须与"传空串（取消代理资格）"
	// 区分开——前者是别的客户端（如旧版后台）在改别的东西，不该顺手把代理资格抹掉。
	AgentGroup *string `json:"agent_group"`
}

// validateAgentGroup 校验代理分组名是否可用。
//
// 空串合法（= 普通用户）。非空时必须能在分组表里查到，否则一律拒绝——
// 分组名写错的话，用户会在广场上看不到任何模型，而界面上完全看不出原因，
// 这种"静默失效"比直接报错难排查得多。
func (s *Server) validateAgentGroup(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if s.deps.Groups == nil {
		return "", errors.New("分组模块未启用，无法设置代理分组")
	}
	group, err := s.deps.Groups.GetByName(ctx, name)
	if err != nil || group == nil {
		return "", fmt.Errorf("代理分组 %q 不存在", name)
	}
	return group.Name, nil
}

// handleListUsers 返回用户列表。
func (s *Server) handleListUsers(c *gin.Context) {
	page, size, offset := parsePagination(c)

	query := model.UserQuery{Limit: size, Offset: offset, Keyword: c.Query("keyword")}
	if roleRaw := c.Query("role"); roleRaw != "" {
		if parsed, err := strconv.Atoi(roleRaw); err == nil {
			role := model.UserRole(parsed)
			query.Role = &role
		}
	}
	if statusRaw := c.Query("status"); statusRaw != "" {
		if parsed, err := strconv.Atoi(statusRaw); err == nil {
			status := model.UserStatus(parsed)
			query.Status = &status
		}
	}

	ctx := c.Request.Context()
	users, err := s.deps.Users.List(ctx, query)
	if err != nil {
		s.respondInternalError(c, "查询用户列表失败")
		return
	}
	total, err := s.deps.Users.Count(ctx, query)
	if err != nil {
		s.respondInternalError(c, "统计用户总数失败")
		return
	}

	items := make([]userDTO, 0, len(users))
	for _, user := range users {
		items = append(items, toUserDTO(user))
	}
	c.JSON(http.StatusOK, newPagedResponse(items, total, page, size))
}

// handleCreateUser 新建用户。
func (s *Server) handleCreateUser(c *gin.Context) {
	var req adminUserUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	if err := crypto.ValidatePasswordStrength(req.Password); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_password")
		return
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		s.respondInternalError(c, "计算口令哈希失败")
		return
	}

	quota := int64(0)
	if req.Quota != nil {
		quota = *req.Quota
	}

	agentGroup := ""
	if req.AgentGroup != nil {
		validated, err := s.validateAgentGroup(c.Request.Context(), *req.AgentGroup)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_agent_group")
			return
		}
		agentGroup = validated
	}

	user := &model.User{
		Username:     strings.TrimSpace(req.Username),
		PasswordHash: hash,
		// 邮箱统一走 NormalizeEmail（去空白 + 转小写）：唯一索引是按原文匹配的，
		// 若这里存入大小写不一的写法，就会绕过"一个邮箱只能绑定一个账号"的约束。
		Email:      model.NormalizeEmail(req.Email),
		Role:       model.UserRole(defaultIfZero(req.Role, int(model.UserRoleUser))),
		Status:     model.UserStatus(defaultIfZero(req.Status, int(model.UserStatusEnabled))),
		Quota:      quota,
		AgentGroup: agentGroup,
	}
	if err := s.deps.Users.Create(c.Request.Context(), user); err != nil {
		if errors.Is(err, model.ErrUsernameTaken) {
			oai.WriteError(c.Writer, http.StatusConflict, "用户名已被占用", oai.TypeInvalidRequest, "username_taken")
			return
		}
		if errors.Is(err, model.ErrEmailTaken) {
			oai.WriteError(c.Writer, http.StatusConflict, "邮箱已被占用（一个邮箱只能绑定一个账号）", oai.TypeInvalidRequest, "email_taken")
			return
		}
		oai.WriteError(c.Writer, http.StatusBadRequest, "创建用户失败："+err.Error(), oai.TypeInvalidRequest, "invalid_user")
		return
	}
	c.JSON(http.StatusOK, toUserDTO(user))
}

// handleUpdateUser 更新用户。
func (s *Server) handleUpdateUser(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req adminUserUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	user, err := s.deps.Users.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "用户不存在", oai.TypeInvalidRequest, "user_not_found")
			return
		}
		s.respondInternalError(c, "查询用户失败")
		return
	}

	newRole := model.UserRole(defaultIfZero(req.Role, int(user.Role)))
	newStatus := model.UserStatus(defaultIfZero(req.Status, int(user.Status)))

	// 保护性校验：不允许把最后一个管理员降级或禁用，
	// 否则系统将再无人能进入后台（这类不可逆的运维事故必须提前拦截）。
	if user.IsAdmin() && (newRole != model.UserRoleAdmin || newStatus != model.UserStatusEnabled) {
		adminCount, err := s.deps.Users.CountAdmins(ctx)
		if err != nil {
			s.respondInternalError(c, "统计管理员数量失败")
			return
		}
		if adminCount <= 1 {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"系统必须保留至少一名启用的管理员，无法降级或禁用最后一个管理员",
				oai.TypeInvalidRequest, "last_admin_protected")
			return
		}
	}

	user.Username = strings.TrimSpace(defaultIfEmpty(req.Username, user.Username))
	// 邮箱同样归一化，保证唯一索引能真正拦住"大小写变体绕过"（见 handleCreateUser 说明）。
	// 注意这里是【全量覆盖】语义：传空字符串即清空邮箱（清空后不参与唯一约束）。
	user.Email = model.NormalizeEmail(req.Email)
	user.Role = newRole
	user.Status = newStatus
	if req.Quota != nil {
		user.Quota = *req.Quota
	}
	// 代理分组：传空串 = 取消代理资格（回到普通用户）；不传 = 保持不变。
	if req.AgentGroup != nil {
		validated, err := s.validateAgentGroup(ctx, *req.AgentGroup)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_agent_group")
			return
		}
		user.AgentGroup = validated
	}
	// 口令留空表示不修改（避免管理员只想改额度却意外重置了用户密码）
	if req.Password != "" {
		if err := crypto.ValidatePasswordStrength(req.Password); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_password")
			return
		}
		hash, err := crypto.HashPassword(req.Password)
		if err != nil {
			s.respondInternalError(c, "计算口令哈希失败")
			return
		}
		user.PasswordHash = hash
	}

	if err := s.deps.Users.Update(ctx, user); err != nil {
		if errors.Is(err, model.ErrUsernameTaken) {
			oai.WriteError(c.Writer, http.StatusConflict, "用户名已被占用", oai.TypeInvalidRequest, "username_taken")
			return
		}
		if errors.Is(err, model.ErrEmailTaken) {
			oai.WriteError(c.Writer, http.StatusConflict, "邮箱已被占用（一个邮箱只能绑定一个账号）", oai.TypeInvalidRequest, "email_taken")
			return
		}
		oai.WriteError(c.Writer, http.StatusBadRequest, "更新用户失败："+err.Error(), oai.TypeInvalidRequest, "invalid_user")
		return
	}

	// 被禁用或改密后强制下线：否则已登录的会话仍可继续访问，使该操作形同虚设。
	// 改密场景尤其重要——若旧会话仍有效，"改密"就不能起到"踢出可疑登录"的作用。
	if newStatus == model.UserStatusDisabled || req.Password != "" {
		_ = s.deps.Sessions.DeleteByUserID(ctx, user.ID)
	}

	c.JSON(http.StatusOK, toUserDTO(user))
}

// handleDeleteUser 删除用户。
func (s *Server) handleDeleteUser(c *gin.Context) {
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	user, err := s.deps.Users.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "用户不存在", oai.TypeInvalidRequest, "user_not_found")
			return
		}
		s.respondInternalError(c, "查询用户失败")
		return
	}

	// 同样保护最后一个管理员
	if user.IsAdmin() {
		adminCount, err := s.deps.Users.CountAdmins(ctx)
		if err != nil {
			s.respondInternalError(c, "统计管理员数量失败")
			return
		}
		if adminCount <= 1 {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"系统必须保留至少一名管理员，无法删除最后一个管理员",
				oai.TypeInvalidRequest, "last_admin_protected")
			return
		}
	}

	// 先删会话再删用户：避免出现"用户已删、会话仍在"的残留状态
	if err := s.deps.Sessions.DeleteByUserID(ctx, user.ID); err != nil {
		s.respondInternalError(c, "清理用户会话失败")
		return
	}
	if err := s.deps.Users.Delete(ctx, user.ID); err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "用户不存在", oai.TypeInvalidRequest, "user_not_found")
			return
		}
		s.respondInternalError(c, "删除用户失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------------------------
// 调用日志（管理员可见全站）
// ---------------------------------------------------------------------------

// handleAdminListLogs 返回全站调用日志。
func (s *Server) handleAdminListLogs(c *gin.Context) {
	query, page, size, ok := s.buildLogQuery(c, 0)
	if !ok {
		return
	}
	s.respondLogs(c, query, page, size)
}

// ---------------------------------------------------------------------------
// 系统设置
// ---------------------------------------------------------------------------

// handleGetSettings 返回系统设置。
func (s *Server) handleGetSettings(c *gin.Context) {
	settings, err := model.LoadSiteSettings(c.Request.Context(), s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取系统设置失败")
		return
	}

	// SEO 基址：优先用配置的站点地址，未配置时按请求推导（与 publicBaseURL 同一逻辑）。
	// 用它拼出可点击/可复制的 sitemap 与 robots 地址回显给后台。
	seoBase := strings.TrimRight(resolveBaseURL(settings, c), "/")

	c.JSON(http.StatusOK, gin.H{
		"site_name":                       settings.SiteName,
		"site_description":                settings.SiteDescription,
		"registration_enabled":            settings.RegistrationEnabled,
		"registration_require_email_code": settings.RegistrationRequireEmailCode,
		"default_user_quota":              settings.DefaultUserQuota,
		"default_group":                   settings.DefaultGroup,
		// 邮件通道是否就绪：让管理员在开关"注册邮箱验证码"前就知道
		// 当前是否具备发信能力，避免开启后用户全部收不到验证码。
		"email_service_ready": s.deps.Mailer != nil && s.deps.Mailer.Configured(),
		"email_from":          mailerFromAddress(s.deps.Mailer),

		// 充值 / 支付参数（非密钥，可在此修改并即时生效）
		"payment": toPaymentSettingsDTO(settings.Payment),
		// 支付通道清单：字段定义 + 当前已填值 + 密钥是否就绪。
		// 前端据此做「勾选启用哪个通道，才展开该通道的配置项」的触发式渲染，
		// 因此新增支付通道不需要改前端代码。
		"payment_channels": buildPaymentChannels(settings.Payment, os.LookupEnv),
		// 各支付通道的密钥是否已通过环境变量就绪。
		// 只暴露布尔值，绝不回传密钥本身——密钥一旦出过服务端就等于泄露。
		"payment_secrets": gin.H{
			model.PaymentMethodEPay: s.deps.Config.Payment.EPayKey != "",
			model.PaymentMethodStripe: s.deps.Config.Payment.StripeSecretKey != "" &&
				s.deps.Config.Payment.StripeWebhookSecret != "",
			model.PaymentMethodManual: true,
		},

		// SEO / 搜索引擎优化参数（非密钥，可在此修改并即时生效）
		"seo": gin.H{
			"site_url":            settings.SEO.SiteURL,
			"keywords":            nonNilStrings(settings.SEO.Keywords),
			"bing_verification":   settings.SEO.BingVerification,
			"google_verification": settings.SEO.GoogleVerification,
			"baidu_verification":  settings.SEO.BaiduVerification,
			"geo_region":          settings.SEO.GeoRegion,
			"geo_placename":       settings.SEO.GeoPlacename,
			"geo_position":        settings.SEO.GeoPosition,
			"sitemap_enabled":     settings.SEO.SitemapEnabled,
			"sitemap_paths":       nonNilStrings(settings.SEO.SitemapPaths),
			// 便于后台直接给出可点击/可复制的两个地址
			"sitemap_url": seoBase + "/sitemap.xml",
			"robots_url":  seoBase + "/robots.txt",
		},

		// 邀请返利 / 每日签到参数（非密钥，可在此修改并即时生效）
		"referral": gin.H{
			"enabled":             settings.Referral.Enabled,
			"register_bonus":      settings.Referral.RegisterBonus,
			"recharge_ratio":      settings.Referral.RechargeRatio,
			"checkin_enabled":     settings.Referral.CheckinEnabled,
			"checkin_daily_quota": settings.Referral.CheckinDailyQuota,
		},

		// 内容安全参数（敏感词过滤）：开关在此，词表在 /api/admin/sensitive-words。
		"safeguard": gin.H{
			"sensitive_filter_enabled": settings.Safeguard.SensitiveFilterEnabled,
		},

		// 合规信息（对用户公示）：页脚、协议页、举报入口都用这一组。
		// 单独成块便于后台表单集中维护，也让"哪些是必须公示的信息"一目了然。
		"compliance": gin.H{
			"operator_name":  settings.Compliance.OperatorName,
			"icp_license":    settings.Compliance.ICPLicense,
			"police_license": settings.Compliance.PoliceLicense,
			"contact_email":  settings.Compliance.ContactEmail,
		},
	})
}

// paymentSettingsDTO 是支付运营参数的对外表示。
type paymentSettingsDTO struct {
	Enabled         bool     `json:"enabled"`
	Methods         []string `json:"methods"`
	ExchangeRate    int64    `json:"exchange_rate"`
	Currency        string   `json:"currency"`
	MinCents        int64    `json:"min_cents"`
	MaxCents        int64    `json:"max_cents"`
	OrderTTLMinutes int      `json:"order_ttl_minutes"`
	NotifyBase      string   `json:"notify_base"`
	// Params 是各支付通道的通道级参数，键形如 "<通道>.<字段>"（如 "epay.pid"）。
	//
	// 这是支持"任意多种支付通道"的承载：通道与字段由支付通道注册表声明，
	// 前端按注册表渲染表单并原样回传，后端不再为每个通道定义专用字段。
	Params map[string]string `json:"params"`
	// 以下四个字段是旧版专用字段，仅为兼容老前端保留，新前端请用 Params。
	EPayGateway string   `json:"epay_gateway"`
	EPayPID     string   `json:"epay_pid"`
	EPayTypes   []string `json:"epay_types"`
	StripeNote  string   `json:"stripe_note"`
}

// paymentChannelFieldDTO 描述支付通道的一个配置字段（供前端触发式渲染）。
type paymentChannelFieldDTO struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Source      string  `json:"source"`
	EnvVar      string  `json:"env_var"`
	Placeholder string  `json:"placeholder"`
	Help        string  `json:"help"`
	Default     string  `json:"default"`
	Required    bool    `json:"required"`
	Options     []gin.H `json:"options,omitempty"`
	Value       string  `json:"value"`
	Ready       bool    `json:"ready"`
	SettingKey  string  `json:"setting_key"`
}

// paymentChannelDTO 描述一个支付通道（含字段、密钥就绪状态与回调地址）。
type paymentChannelDTO struct {
	Key         string                   `json:"key"`
	Label       string                   `json:"label"`
	Description string                   `json:"description"`
	Available   bool                     `json:"available"`
	Enabled     bool                     `json:"enabled"`
	NotifyPath  string                   `json:"notify_path"`
	MissingEnv  []string                 `json:"missing_env"`
	Fields      []paymentChannelFieldDTO `json:"fields"`
}

// buildPaymentChannels 组装支付通道清单：字段定义 + 当前已填值 + 密钥是否就绪。
//
// 为什么要一次返回"字段定义 + 当前值 + 就绪状态"三样：
//   - 字段定义让前端做**触发式渲染**（勾选哪个通道才展开它的字段）；
//   - 当前值让界面能回显站长已配好的内容；
//   - 就绪状态让界面能明确提示"密钥还没注入、这个通道开了也用不了"。
//
// 密钥字段只返回"是否就绪"，绝不返回内容。
func buildPaymentChannels(settings model.PaymentSettings, lookup func(string) (string, bool)) []paymentChannelDTO {
	channels := payment.Channels()
	result := make([]paymentChannelDTO, 0, len(channels))

	for _, channel := range channels {
		missing := channel.MissingSecrets(lookup)

		fields := make([]paymentChannelFieldDTO, 0, len(channel.Fields))
		for _, field := range channel.Fields {
			item := paymentChannelFieldDTO{
				Key:         field.Key,
				Label:       field.Label,
				Kind:        string(field.Kind),
				Source:      string(field.Source),
				EnvVar:      field.EnvVar,
				Placeholder: field.Placeholder,
				Help:        field.Help,
				Default:     field.Default,
				Required:    field.Required,
				SettingKey:  channel.SettingKey(field.Key),
			}
			for _, option := range field.Options {
				item.Options = append(item.Options, gin.H{"value": option.Value, "label": option.Label})
			}

			if field.Source == payment.SourceSecret {
				// 密钥：只告诉前端"是否已就绪"，值永不出服务端
				item.Ready = true
				if field.EnvVar != "" {
					if value, ok := lookup(field.EnvVar); !ok || strings.TrimSpace(value) == "" {
						item.Ready = false
					}
				}
			} else {
				item.Value = settings.Param(channel.Key, field.Key)
				if item.Value == "" {
					item.Value = field.Default
				}
				item.Ready = item.Value != ""
			}
			fields = append(fields, item)
		}

		result = append(result, paymentChannelDTO{
			Key:         channel.Key,
			Label:       channel.Label,
			Description: channel.Description,
			Available:   channel.Available,
			Enabled:     settings.MethodEnabled(channel.Key),
			NotifyPath:  channel.NotifyPath,
			MissingEnv:  nonNilStrings(missing),
			Fields:      fields,
		})
	}
	return result
}

// toPaymentSettingsDTO 把支付设置转为对外 DTO。
func toPaymentSettingsDTO(settings model.PaymentSettings) paymentSettingsDTO {
	params := make(map[string]string, len(settings.Params))
	for key, value := range settings.Params {
		params[key] = value
	}
	return paymentSettingsDTO{
		Enabled:         settings.Enabled,
		Methods:         nonNilStrings(settings.Methods),
		ExchangeRate:    settings.ExchangeRate,
		Currency:        settings.Currency,
		MinCents:        settings.MinCents,
		MaxCents:        settings.MaxCents,
		OrderTTLMinutes: settings.OrderTTLMinutes,
		NotifyBase:      settings.NotifyBase,
		Params:          params,
		EPayGateway:     settings.Param(model.PaymentMethodEPay, "gateway"),
		EPayPID:         settings.Param(model.PaymentMethodEPay, "pid"),
		EPayTypes:       nonNilStrings(settings.ParamList(model.PaymentMethodEPay, "types")),
		StripeNote:      settings.Param(model.PaymentMethodStripe, "note"),
	}
}

// nonNilStrings 保证 JSON 序列化出 [] 而不是 null。
//
// 前端对数组做 .length / .map 时，null 会导致渲染报错，
// 因此在边界上统一成空数组。
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// mailerFromAddress 返回发件人地址用于界面展示；未配置时返回空字符串。
//
// 只暴露地址不暴露口令：地址本身对管理员没有秘密，且能帮助定位
// "验证码发不出去"这类问题（例如发件地址写错）。
func mailerFromAddress(sender *mailer.Sender) string {
	if sender == nil {
		return ""
	}
	return sender.From()
}

// settingsUpdateRequest 是更新设置的请求体。
//
// 说明：字段用指针，未提交的项保持原值，避免前端只改一项却把其他项清空。
type settingsUpdateRequest struct {
	SiteName                     *string `json:"site_name"`
	SiteDescription              *string `json:"site_description"`
	RegistrationEnabled          *bool   `json:"registration_enabled"`
	RegistrationRequireEmailCode *bool   `json:"registration_require_email_code"`
	DefaultUserQuota             *int64  `json:"default_user_quota"`
	DefaultGroup                 *string `json:"default_group"`

	// 支付 / 充值参数（整体替换，见下方处理逻辑）
	Payment *paymentSettingsDTO `json:"payment"`

	// SEO / 搜索引擎优化参数（逐项覆盖，见 mergeSEOSettings）
	SEO *seoSettingsDTO `json:"seo"`

	// 邀请返利 / 每日签到参数（逐项覆盖，见 mergeReferralSettings）
	Referral *referralSettingsDTO `json:"referral"`

	// 内容安全参数（逐项覆盖，见 mergeSafeguardSettings）
	Safeguard *safeguardSettingsDTO `json:"safeguard"`

	// 合规信息（逐项覆盖，见 mergeComplianceSettings）
	Compliance *complianceSettingsDTO `json:"compliance"`
}

// safeguardSettingsDTO 是内容安全（合规过滤）设置的可写入参。
//
// 字段用指针：未提交的项保持原值，避免前端只改一项却把其他项清空。
type safeguardSettingsDTO struct {
	SensitiveFilterEnabled *bool `json:"sensitive_filter_enabled"`
}

// complianceSettingsDTO 是合规信息（对用户公示）的可写入参。
//
// 字段用指针：未提交的项保持原值。这里允许提交空串以"清空某一项"
// （例如备案号填错了要删掉），因此不能像站点名那样把空值当作"未提供"。
type complianceSettingsDTO struct {
	OperatorName  *string `json:"operator_name"`
	ICPLicense    *string `json:"icp_license"`
	PoliceLicense *string `json:"police_license"`
	ContactEmail  *string `json:"contact_email"`
}

// mergeComplianceSettings 把提交的合规信息并入当前设置。
//
// 只做裁剪空白与长度限制（与 model.loadComplianceSettings 同一口径），
// 不做"必填"校验：站点可能正在准备备案材料，允许先留空。
func mergeComplianceSettings(target *model.ComplianceSettings, req *complianceSettingsDTO) {
	if target == nil || req == nil {
		return
	}
	apply := func(dst *string, src *string) {
		if src == nil {
			return
		}
		*dst = model.TruncateComplianceField(*src)
	}
	apply(&target.OperatorName, req.OperatorName)
	apply(&target.ICPLicense, req.ICPLicense)
	apply(&target.PoliceLicense, req.PoliceLicense)
	apply(&target.ContactEmail, req.ContactEmail)
}

// mergeSafeguardSettings 把提交的内容安全参数并入当前设置。
func mergeSafeguardSettings(target *model.SafeguardSettings, req *safeguardSettingsDTO) {
	if target == nil || req == nil {
		return
	}
	if req.SensitiveFilterEnabled != nil {
		target.SensitiveFilterEnabled = *req.SensitiveFilterEnabled
	}
}

// referralSettingsDTO 是邀请返利 / 签到设置的可写入参。
//
// 字段全用指针：未提交的项保持原值，避免前端只改一项却把其他项清空。
type referralSettingsDTO struct {
	Enabled           *bool  `json:"enabled"`
	RegisterBonus     *int64 `json:"register_bonus"`
	RechargeRatio     *int   `json:"recharge_ratio"`
	CheckinEnabled    *bool  `json:"checkin_enabled"`
	CheckinDailyQuota *int64 `json:"checkin_daily_quota"`
}

// seoSettingsDTO 是 SEO 设置的可写入参。
//
// 为什么字段用指针：未提交的项应保持原值，避免前端只改一项却把其他项清空。
// 列表型字段用 *[]string 以区分"未提供"与"提供空列表"（后者表示显式清空）。
type seoSettingsDTO struct {
	SiteURL            *string   `json:"site_url"`
	Keywords           *[]string `json:"keywords"`
	BingVerification   *string   `json:"bing_verification"`
	GoogleVerification *string   `json:"google_verification"`
	BaiduVerification  *string   `json:"baidu_verification"`
	GeoRegion          *string   `json:"geo_region"`
	GeoPlacename       *string   `json:"geo_placename"`
	GeoPosition        *string   `json:"geo_position"`
	SitemapEnabled     *bool     `json:"sitemap_enabled"`
	SitemapPaths       *[]string `json:"sitemap_paths"`
}

// handleUpdateSettings 更新系统设置。
func (s *Server) handleUpdateSettings(c *gin.Context) {
	var req settingsUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	current, err := model.LoadSiteSettings(ctx, s.deps.Settings)
	if err != nil {
		s.respondInternalError(c, "读取系统设置失败")
		return
	}

	if req.SiteName != nil && strings.TrimSpace(*req.SiteName) != "" {
		current.SiteName = strings.TrimSpace(*req.SiteName)
	}
	if req.SiteDescription != nil {
		current.SiteDescription = strings.TrimSpace(*req.SiteDescription)
	}
	if req.RegistrationEnabled != nil {
		current.RegistrationEnabled = *req.RegistrationEnabled
	}
	if req.RegistrationRequireEmailCode != nil {
		// 开启验证码校验但邮件通道未配置：明确拒绝而不是静默保存。
		// 理由：一旦保存，所有用户注册都会卡在"收不到验证码"，
		// 而管理员从界面上看不出原因，属于极难定位的运营故障。
		if *req.RegistrationRequireEmailCode && (s.deps.Mailer == nil || !s.deps.Mailer.Configured()) {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"邮件服务未配置，无法开启邮箱验证码校验（请先配置 SMTP 环境变量）",
				oai.TypeInvalidRequest, "email_service_unavailable")
			return
		}
		current.RegistrationRequireEmailCode = *req.RegistrationRequireEmailCode
	}
	if req.DefaultUserQuota != nil {
		if *req.DefaultUserQuota < model.QuotaUnlimited {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"默认额度非法（允许 -1 表示不限）", oai.TypeInvalidRequest, "invalid_quota")
			return
		}
		current.DefaultUserQuota = *req.DefaultUserQuota
	}
	if req.DefaultGroup != nil && strings.TrimSpace(*req.DefaultGroup) != "" {
		current.DefaultGroup = strings.TrimSpace(*req.DefaultGroup)
	}
	if req.Payment != nil {
		updated, err := mergePaymentSettings(current.Payment, req.Payment)
		if err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_payment_settings")
			return
		}
		current.Payment = updated
	}
	if req.SEO != nil {
		if err := mergeSEOSettings(&current.SEO, req.SEO); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_seo_settings")
			return
		}
	}
	if req.Referral != nil {
		if err := mergeReferralSettings(&current.Referral, req.Referral); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_referral_settings")
			return
		}
	}
	if req.Safeguard != nil {
		mergeSafeguardSettings(&current.Safeguard, req.Safeguard)
	}
	if req.Compliance != nil {
		mergeComplianceSettings(&current.Compliance, req.Compliance)
	}

	if err := s.deps.Settings.SetMany(ctx, current.ToMap()); err != nil {
		s.respondInternalError(c, "保存系统设置失败")
		return
	}

	// 敏感词过滤开关可能刚被改动：主动失效缓存，让新状态对下一个请求立即生效，
	// 而不是等 30 秒 TTL 到期（否则管理员会以为"开了没效果"）。
	if s.sensitiveFilter != nil {
		s.sensitiveFilter.Invalidate()
	}

	// 清空 sitemap 缓存：否则站长改完域名/路径要等到第二天才生效（日期键才失效）。
	s.invalidateSitemapCache()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SEO 相关字段的长度/数量上限。
//
// 设上限的目的：这些值会拼进 HTML/XML 并对外暴露，无限制地接受超长输入
// 既可能撑大响应体，也可能被用作"通过后台注入超大内容"的途径。
const (
	maxSEOListItems = 20  // 关键词 / 额外路径 各不超过 20 项
	maxSEOItemLen   = 64  // 单项最长 64 个字符
	maxSEOFieldLen  = 200 // 验证码等标量字段最长 200 个字符
)

// mergeSEOSettings 校验并合并 SEO 设置（只覆盖请求中出现的字段）。
func mergeSEOSettings(target *model.SEOSettings, req *seoSettingsDTO) error {
	if req.SiteURL != nil {
		normalized := model.NormalizeSiteURL(*req.SiteURL)
		// 防御性校验：NormalizeSiteURL 已会补全协议，这里再确认一次，
		// 避免将来规范化逻辑变化导致非法地址被写入并进入 sitemap。
		if normalized != "" &&
			!strings.HasPrefix(normalized, "http://") &&
			!strings.HasPrefix(normalized, "https://") {
			return fmt.Errorf("站点地址必须以 http:// 或 https:// 开头")
		}
		target.SiteURL = normalized
	}

	if req.Keywords != nil {
		items, err := normalizeSEOList("关键词", *req.Keywords)
		if err != nil {
			return err
		}
		target.Keywords = items
	}

	if req.SitemapPaths != nil {
		items, err := normalizeSEOList("额外路径", *req.SitemapPaths)
		if err != nil {
			return err
		}
		for _, item := range items {
			// 必须是站内绝对路径；用户填了完整 URL（含域名）时明确报错，
			// 否则会和 base 拼成 "https://a.comhttps://b.com/x" 这种非法链接。
			if !strings.HasPrefix(item, "/") {
				return fmt.Errorf("额外路径必须以 / 开头（例如 /pricing）")
			}
		}
		target.SitemapPaths = items
	}

	scalars := []struct {
		label string
		value *string
		dest  *string
	}{
		{"必应验证码", req.BingVerification, &target.BingVerification},
		{"Google 验证码", req.GoogleVerification, &target.GoogleVerification},
		{"百度验证码", req.BaiduVerification, &target.BaiduVerification},
		{"地域代码", req.GeoRegion, &target.GeoRegion},
		{"地名", req.GeoPlacename, &target.GeoPlacename},
	}
	for _, field := range scalars {
		if field.value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*field.value)
		if len([]rune(trimmed)) > maxSEOFieldLen {
			return fmt.Errorf("%s最长 %d 个字符", field.label, maxSEOFieldLen)
		}
		*field.dest = trimmed
	}

	if req.GeoPosition != nil {
		position := strings.TrimSpace(*req.GeoPosition)
		if err := validateGeoPosition(position); err != nil {
			return err
		}
		target.GeoPosition = position
	}

	if req.SitemapEnabled != nil {
		target.SitemapEnabled = *req.SitemapEnabled
	}

	return nil
}

// mergeReferralSettings 校验并合并邀请返利 / 签到设置（只覆盖请求中出现的字段）。
//
// 校验使用 model 层导出的校验函数，确保"后台保存"与"运行期发奖前再校验"口径一致：
// 比例限 0~100，额度限 0~1e9（越界一律拒绝保存，而不是静默截断——
// 静默截断会让站长以为自己填的值生效了，实际没有）。
func mergeReferralSettings(target *model.ReferralSettings, req *referralSettingsDTO) error {
	if req.Enabled != nil {
		target.Enabled = *req.Enabled
	}
	if req.RegisterBonus != nil {
		if err := model.ValidateReferralQuota("邀请注册奖励", *req.RegisterBonus); err != nil {
			return err
		}
		target.RegisterBonus = *req.RegisterBonus
	}
	if req.RechargeRatio != nil {
		if err := model.ValidateRechargeRatio(*req.RechargeRatio); err != nil {
			return err
		}
		target.RechargeRatio = *req.RechargeRatio
	}
	if req.CheckinEnabled != nil {
		target.CheckinEnabled = *req.CheckinEnabled
	}
	if req.CheckinDailyQuota != nil {
		if err := model.ValidateReferralQuota("每日签到额度", *req.CheckinDailyQuota); err != nil {
			return err
		}
		target.CheckinDailyQuota = *req.CheckinDailyQuota
	}
	return nil
}

// normalizeSEOList 规整列表型字段：去空白、去空项、去重，并校验数量与单项长度。
func normalizeSEOList(label string, raw []string) ([]string, error) {
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if len([]rune(trimmed)) > maxSEOItemLen {
			return nil, fmt.Errorf("%s单项最长 %d 个字符", label, maxSEOItemLen)
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	if len(result) > maxSEOListItems {
		return nil, fmt.Errorf("%s最多 %d 项", label, maxSEOListItems)
	}
	return result, nil
}

// validateGeoPosition 校验经纬度格式：非空时必须为 "纬度;经度"。
func validateGeoPosition(raw string) error {
	if raw == "" {
		return nil
	}
	lat, lng, found := strings.Cut(raw, ";")
	if !found {
		return fmt.Errorf("经纬度格式应为「纬度;经度」，例如 22.5431;114.0579")
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(lat), 64); err != nil {
		return fmt.Errorf("纬度必须是数字")
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(lng), 64); err != nil {
		return fmt.Errorf("经度必须是数字")
	}
	return nil
}

// mergePaymentSettings 校验并合并支付设置。
//
// 为什么不直接赋值：支付参数写错会造成真实资损
// （例如兑换比例填成 0 会让所有充值都不到账，填成 100000 会让 1 元换到十万额度），
// 因此必须在保存前拦住明显非法的取值。
func mergePaymentSettings(current model.PaymentSettings, req *paymentSettingsDTO) (model.PaymentSettings, error) {
	updated := current

	updated.Enabled = req.Enabled
	// 通道白名单：以支付通道注册表为准，而不是硬编码几个名字。
	//
	// 这里做两道闸门（防止"开了但用不了"）：
	//  1) 未实现的通道不允许启用（Available=false 的会明确报错）；
	//  2) 密钥未通过环境变量注入的通道不允许启用。
	// 否则用户会在充值页看到支付入口、点下去却下单失败 —— 最糟糕的体验。
	if req.Methods != nil {
		methods := make([]string, 0, len(req.Methods))
		seen := make(map[string]bool, len(req.Methods))
		for _, method := range req.Methods {
			trimmed := strings.TrimSpace(method)
			if trimmed == "" {
				continue // 忽略空项
			}
			if seen[trimmed] {
				continue // 去重，避免同一通道在界面上出现两次
			}
			channel, ok := payment.FindChannel(trimmed)
			if !ok {
				return updated, fmt.Errorf("不支持的支付通道 %q（可选：%s）",
					trimmed, strings.Join(payment.ChannelKeys(), " / "))
			}
			if err := payment.ValidateChannelEnabled(channel, os.LookupEnv); err != nil {
				return updated, err
			}
			seen[trimmed] = true
			methods = append(methods, trimmed)
		}
		updated.Methods = methods
	}

	if req.ExchangeRate > 0 {
		updated.ExchangeRate = req.ExchangeRate
	} else if req.Enabled {
		// 只在"启用充值"时强校验：关闭状态下留 0 也不会造成资损
		return updated, fmt.Errorf("兑换比例必须大于 0（表示 1 元可兑换多少额度）")
	}

	if strings.TrimSpace(req.Currency) != "" {
		updated.Currency = strings.TrimSpace(req.Currency)
	}
	if req.MinCents >= 0 {
		updated.MinCents = req.MinCents
	}
	if req.MaxCents < 0 {
		return updated, fmt.Errorf("单笔最大金额不能为负数")
	}
	updated.MaxCents = req.MaxCents
	if updated.MaxCents > 0 && updated.MaxCents < updated.MinCents {
		return updated, fmt.Errorf("单笔最大金额不能小于最小金额")
	}

	if req.OrderTTLMinutes > 0 {
		updated.OrderTTLMinutes = req.OrderTTLMinutes
	} else if req.Enabled {
		return updated, fmt.Errorf("订单有效期必须大于 0 分钟")
	}

	updated.NotifyBase = strings.TrimRight(strings.TrimSpace(req.NotifyBase), "/")

	// 通道级参数：以 Params 为唯一来源。
	params := make(map[string]string, len(updated.Params)+len(req.Params)+4)
	for key, value := range updated.Params {
		params[key] = value
	}
	for key, value := range req.Params {
		params[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	foldLegacyPaymentFields(params, req)
	// 只保留"已登记通道的已登记字段"，顺手丢掉误传的键，
	// 防止设置表被垃圾键塞满（例如通道改名后遗留的旧键）。
	updated.Params = sanitizePaymentParams(params)

	// 启用某通道却没把必要参数填全时直接拒绝：
	// 否则用户会看到一个"能下单却付不了款"的充值页，这是最令人困惑的故障。
	if updated.Enabled {
		for _, method := range updated.Methods {
			channel, ok := payment.FindChannel(method)
			if !ok {
				continue
			}
			for _, field := range channel.SettingFields() {
				if field.Required && updated.Param(method, field.Key) == "" {
					return updated, fmt.Errorf("启用「%s」需要填写「%s」", channel.Label, field.Label)
				}
			}
		}
	}

	return updated, nil
}

// foldLegacyPaymentFields 把旧版前端提交的专用字段折算进通道参数。
//
// 为什么需要它：升级后前端可能还没更新，仍会提交 epay_gateway / epay_pid 等旧字段。
// 若直接忽略，站长一保存就会把已配好的易支付参数清空 —— 属于升级事故。
// 因此这里把旧字段"翻译"成新的参数键，且不覆盖 Params 里已有的值。
func foldLegacyPaymentFields(params map[string]string, req *paymentSettingsDTO) {
	fold := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" || strings.TrimSpace(params[key]) != "" {
			return
		}
		params[key] = value
	}
	fold("epay.gateway", strings.TrimRight(req.EPayGateway, "/"))
	fold("epay.pid", req.EPayPID)
	fold("epay.types", strings.Join(req.EPayTypes, ","))
	fold("stripe.note", req.StripeNote)
}

// sanitizePaymentParams 过滤通道参数，只保留已登记通道的已登记字段。
//
// 两道过滤缺一不可：
//  1. 通道必须在注册表里（否则是拼错或已下线的通道）；
//  2. 字段必须在该通道声明过（否则是拼错或已改名的字段）。
//
// 这样即使前端被改坏，也不可能往设置表里写入任意键。
func sanitizePaymentParams(params map[string]string) map[string]string {
	result := make(map[string]string, len(params))
	for key, value := range params {
		channelKey, fieldKey, found := strings.Cut(strings.TrimSpace(key), ".")
		if !found {
			continue
		}
		channel, ok := payment.FindChannel(channelKey)
		if !ok {
			continue
		}
		declared := false
		for _, field := range channel.SettingFields() {
			if field.Key == fieldKey {
				declared = true
				break
			}
		}
		if !declared {
			continue
		}
		// 空值不落库：让"没填"与"填了空串"在库里表现一致，便于判断通道是否已配置。
		if strings.TrimSpace(value) == "" {
			continue
		}
		result[key] = strings.TrimSpace(value)
	}
	return result
}

// ---------------------------------------------------------------------------
// 共享辅助
// ---------------------------------------------------------------------------

// 渠道与分组的默认值。
const (
	defaultChannelGroup = "default"
)

// parsePagination 解析分页参数并归一化。
func parsePagination(c *gin.Context) (page, size, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ = strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(defaultPageSize)))
	if size < 1 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size, (page - 1) * size
}

// parseIDParam 解析路径中的 id 参数；失败时已写出 400 响应，返回 false。
func parseIDParam(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"资源 ID 非法", oai.TypeInvalidRequest, "invalid_id")
		return 0, false
	}
	return id, true
}

// respondInternalError 统一输出内部错误（不暴露细节）。
//
// detail 只写服务端日志、绝不回给客户端：约百处调用点靠它区分"是哪一步失败"，
// 若不留痕，一次 500 在日志里完全无法定位。
//
// 可选的 err 是真实原因（SQL/磁盘/上游错误），只会进日志、不会回给客户端。
// 做成变参而非强制参数：既有约 180 处调用点没有 err 可传（或错误已在别处记录），
// 强制补齐会把一次"补日志"的改动变成对全仓的机械改写，
// 反而容易在改写中引入笔误。关键链路（支付、备份、安装、验证码）应尽量传入。
func (s *Server) respondInternalError(c *gin.Context, detail string, errs ...error) {
	attrs := []any{"detail", detail,
		"method", c.Request.Method, "path", c.Request.URL.Path, "client_ip", c.ClientIP()}
	for _, err := range errs {
		if err != nil {
			attrs = append(attrs, "error", err)
			break
		}
	}
	slog.Error("处理请求失败", attrs...)
	oai.WriteError(c.Writer, http.StatusInternalServerError,
		"网关内部错误", oai.TypeServer, oai.CodeInternal)
}

// loadUsernames 一次性加载"用户 ID → 用户名"映射，用于避免日志/列表的 N+1 查询。
func (s *Server) loadUsernames(ctx context.Context) map[uint64]string {
	result := make(map[uint64]string)
	users, err := s.deps.Users.List(ctx, model.UserQuery{Limit: nameLookupLimit})
	if err != nil {
		// 名称解析失败不应阻断主流程：日志本身仍然有价值，最多"用户名"列为空
		slog.Warn("加载用户名映射失败，列表中用户名列将为空", "error", err)
		return result
	}
	for _, user := range users {
		result[user.ID] = user.Username
	}
	return result
}

// loadChannelNames 一次性加载"渠道 ID → 渠道名"映射。
func (s *Server) loadChannelNames(ctx context.Context) map[uint64]string {
	result := make(map[uint64]string)
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Limit: nameLookupLimit})
	if err != nil {
		return result
	}
	for _, channel := range channels {
		result[channel.ID] = channel.Name
	}
	return result
}

// defaultIfEmpty 在值为空时返回兜底值。
func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// defaultIfZero 在值为 0 时返回兜底值。
//
// 用于"前端未提交该字段"的场景：0 在这里视为"未提供"，
// 因为业务上不存在合法的零值语义（如权重 0 无意义、状态 0 非法）。
func defaultIfZero(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// truncateToDay 把时间截断到当天零点（本地时区）。
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
