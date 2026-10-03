// 本文件实现模型计价规则的后台管理接口。
//
// 意图（Why）：
//
//	价格是"用量 → 费用"的唯一换算依据。放在后台可维护，是因为不同部署的
//	上游采购成本差别极大（官方直采、代理、免费额度），任何硬编码的价格表
//	都必然与实际成本脱节。
//
//	开源交付考虑：本接口不预设任何厂商价格，全部由使用者自行录入，
//	既避免价格过时带来的误导，也避免把第三方的定价表当作本项目的资产分发。
//
// 流转（Flow）：
//
//	后台价格页 → GET  /api/admin/prices            列出规则（可选 ?group= 与 ?channel_id=）
//	           → POST /api/admin/prices            新增（改完清空计费缓存）
//	           → PUT  /api/admin/prices/{id}       更新（同上）
//	           → DELETE /api/admin/prices/{id}     删除（同上）
//
// 渠道专属价（本次扩展）：
//
//	同一「模型 + 分组」可以配多条价格，区别在 channel_id：
//	  - channel_id = 0：不限渠道的「分组默认价」；
//	  - channel_id > 0：仅对该渠道生效的「渠道专用价」。
//	计费时渠道专用价优先，未配专用价则回退分组默认价
//	（优先级由 model.MatchModelPriceForChannel 决定）。
//	本文件负责：渠道归属校验、渠道名回显、按渠道过滤列表，
//	以及把「(model, group_name, channel_id) 唯一」冲突翻译成可读的 400。
//
// 扩展（Extend）：
//
//	新增计价维度时：在 model.ModelPrice 加字段 → 建迁移加列 → 本文件的
//	DTO / 请求体 / 校验三处同步。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// modelPriceDTO 是计价规则的对外表示。
type modelPriceDTO struct {
	ID              uint64 `json:"id"`
	Model           string `json:"model"`
	PromptPrice     int64  `json:"prompt_price"`
	CachePrice      int64  `json:"cache_price"`
	CompletionPrice int64  `json:"completion_price"`
	PerCallPrice    int64  `json:"per_call_price"`
	// BillingMode 是站长显式选择的计费方式；空串表示"自动判定"（历史数据）。
	BillingMode string `json:"billing_mode"`
	// EffectiveBillingMode 是实际生效的计费方式（把"自动"解释成具体口径）。
	//
	// 两个字段都给前端：前者用于回显表单的选择，后者用于展示"到底按什么算"——
	// 只给一个的话，界面要么回显不出"自动"，要么显示不出真实口径。
	EffectiveBillingMode string `json:"effective_billing_mode"`
	// IsFree 是派生布尔：显式免费（与"未定价"是两回事，后者根本没有规则）。
	IsFree bool   `json:"is_free"`
	Group  string `json:"group"`
	// ChannelID 是规则适用的渠道：0 表示不限渠道（分组默认价），>0 为该渠道的专用价。
	//
	// 同一「模型 + 分组」可同时存在一条默认价与若干条渠道专用价，
	// 计费时渠道专用价优先，未配专用价回退默认价。
	ChannelID uint64 `json:"channel_id"`
	// ChannelName 是 ChannelID 对应渠道的显示名，供界面直接展示。
	//
	// 0 或渠道已被删除时为空串——界面据此回退为「渠道 #ID」，
	// 避免因渠道改名/删除而回显错误的渠道名（宁缺勿错）。
	ChannelName string `json:"channel_name"`
	Enabled     bool   `json:"enabled"`
	Remark      string `json:"remark"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// toModelPriceDTO 把领域模型转为对外 DTO。
func toModelPriceDTO(price *model.ModelPrice) modelPriceDTO {
	if price == nil {
		return modelPriceDTO{}
	}
	return modelPriceDTO{
		ID:                   price.ID,
		Model:                price.Model,
		PromptPrice:          price.PromptPrice,
		CachePrice:           price.CachePrice,
		CompletionPrice:      price.CompletionPrice,
		PerCallPrice:         price.PerCallPrice,
		BillingMode:          price.BillingMode,
		EffectiveBillingMode: price.EffectiveBillingMode(),
		IsFree:               price.IsFree(),
		Group:                price.Group,
		ChannelID:            price.ChannelID,
		Enabled:              price.Enabled,
		Remark:               price.Remark,
		CreatedAt:            unixOrZero(price.CreatedAt),
		UpdatedAt:            unixOrZero(price.UpdatedAt),
	}
}

// handleListPrices 返回计价规则列表。
//
// 支持两个可选过滤：
//   - group：按分组过滤（多业务线部署下，管理员通常只想看自己那条线的价格）；
//   - channel_id：按渠道过滤。未提供时只列「不限渠道」的分组默认价；
//     提供时列出该渠道生效的价格集合（分组默认价 + 该渠道专用价），
//     便于管理员核对"某个渠道到底按什么价计费"。
//
// 为什么默认不把各渠道专用价一并列出：仓储按"渠道"维度查询价格，
// 若把某渠道的专属折扣混进"全部"，会被误当成全组统一价展示（见 store.List 的说明）。
func (s *Server) handleListPrices(c *gin.Context) {
	if s.deps.ModelPrices == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"计费模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	group := strings.TrimSpace(c.Query("group"))

	var (
		prices []*model.ModelPrice
		err    error
	)
	if raw := strings.TrimSpace(c.Query("channel_id")); raw != "" {
		channelID, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				"channel_id 必须是数字", oai.TypeInvalidRequest, "invalid_channel_id")
			return
		}
		prices, err = s.deps.ModelPrices.ListForPricing(ctx, group, channelID, false)
	} else {
		prices, err = s.deps.ModelPrices.List(ctx, group, false)
	}
	if err != nil {
		s.respondInternalError(c, "查询计价规则失败")
		return
	}

	// 渠道名一次性解析（列表最多几百条，避免逐行查库导致的 N+1）
	var channelNames map[uint64]string
	if s.deps.Channels != nil {
		channelNames = s.loadChannelNames(ctx)
	}

	items := make([]modelPriceDTO, 0, len(prices))
	for _, price := range prices {
		dto := toModelPriceDTO(price)
		dto.ChannelName = channelNames[price.ChannelID]
		items = append(items, dto)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// modelPriceUpsertRequest 是新增/更新计价规则的请求体。
type modelPriceUpsertRequest struct {
	Model           string  `json:"model"`
	PromptPrice     *int64  `json:"prompt_price"`
	CachePrice      *int64  `json:"cache_price"`
	CompletionPrice *int64  `json:"completion_price"`
	PerCallPrice    *int64  `json:"per_call_price"`
	BillingMode     *string `json:"billing_mode"`
	Group           string  `json:"group"`
	// ChannelID 指定规则适用渠道：缺省或 0 表示不限渠道（分组默认价），
	// >0 表示仅对该渠道生效的专用价。
	//
	// 用指针是为了区分"未提交"与"显式提交 0"：更新场景下缺省表示不改动该字段。
	ChannelID *uint64 `json:"channel_id"`
	Enabled   *bool   `json:"enabled"`
	Remark    string  `json:"remark"`
}

// resolveChannelScope 校验渠道归属并返回渠道显示名。
//
// channelID 为 0（不限渠道）时直接通过并返回空名；
// >0 时必须在渠道仓储中存在，否则写出 400 并返回 ok=false。
//
// 为什么必须在入库前校验：model_prices.channel_id 没有外键约束，
// 若写入一个不存在的渠道 ID，这条规则永远不会被任何渠道命中，
// 表现为"配了价却不生效"，且从界面上看不出异常。宁可当场拦下。
func (s *Server) resolveChannelScope(c *gin.Context, channelID uint64) (string, bool) {
	if channelID == model.ChannelScopeAll {
		return "", true
	}
	if s.deps.Channels == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"渠道模块未启用，无法配置渠道专属价", oai.TypeServer, oai.CodeInternal)
		return "", false
	}
	channel, err := s.deps.Channels.GetByID(c.Request.Context(), channelID)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusBadRequest,
				fmt.Sprintf("渠道不存在：未找到 ID 为 %d 的渠道", channelID),
				oai.TypeInvalidRequest, "channel_not_found")
			return "", false
		}
		s.respondInternalError(c, "校验渠道失败")
		return "", false
	}
	return channel.Name, true
}

// priceConflictMessage 生成唯一约束冲突的可读提示。
//
// 唯一约束是 (model, group_name, channel_id)：同一模型 + 同一分组下，
// 每个渠道（含"不限渠道"）只能有一条定价。把冲突维度说清楚，
// 管理员才知道该改渠道还是改模型/分组，而不是看到一句笼统的"已存在"。
func priceConflictMessage(channelID uint64, channelName string) string {
	if channelID == model.ChannelScopeAll {
		return "该分组下该模型已有「不限渠道」的定价；同一「模型 + 分组 + 不限渠道」只能有一条"
	}
	scope := channelName
	if scope == "" {
		scope = fmt.Sprintf("渠道 #%d", channelID)
	}
	return fmt.Sprintf("该分组下该模型已为「%s」配过价；同一「模型 + 分组 + 渠道」只能有一条", scope)
}

// validateBillingMode 校验计费方式取值，非法时返回给使用者可读的原因。
//
// 单独抽出来是因为新增与更新两条路径都要校验：漏掉一处就会出现
// "能存进去但计费不认"的规则（EffectiveBillingMode 会静默回退到自动判定）。
func validateBillingMode(mode string) error {
	if mode == model.BillingModeAuto || model.IsValidBillingMode(mode) {
		return nil
	}
	return fmt.Errorf("计费方式非法：%q（可选 免费 / 按量 / 按次，或留空自动判定）", mode)
}

// handleCreatePrice 新增计价规则。
func (s *Server) handleCreatePrice(c *gin.Context) {
	if s.deps.ModelPrices == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"计费模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	var req modelPriceUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	price := &model.ModelPrice{
		Model:  strings.TrimSpace(req.Model),
		Group:  defaultIfEmpty(strings.TrimSpace(req.Group), defaultChannelGroup),
		Remark: strings.TrimSpace(req.Remark),
		// 默认启用：新增规则的目的通常就是立即生效
		Enabled: true,
	}
	if req.PromptPrice != nil {
		price.PromptPrice = *req.PromptPrice
	}
	if req.CachePrice != nil {
		price.CachePrice = *req.CachePrice
	}
	if req.CompletionPrice != nil {
		price.CompletionPrice = *req.CompletionPrice
	}
	if req.PerCallPrice != nil {
		price.PerCallPrice = *req.PerCallPrice
	}
	if req.BillingMode != nil {
		mode := strings.TrimSpace(*req.BillingMode)
		if err := validateBillingMode(mode); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_billing_mode")
			return
		}
		price.BillingMode = mode
	}
	if req.Enabled != nil {
		price.Enabled = *req.Enabled
	}
	// 渠道归属：缺省即 0（不限渠道）；指定渠道时必须在库中存在
	if req.ChannelID != nil {
		price.ChannelID = *req.ChannelID
	}
	channelName, ok := s.resolveChannelScope(c, price.ChannelID)
	if !ok {
		return
	}

	if err := s.deps.ModelPrices.Create(c.Request.Context(), price); err != nil {
		if errors.Is(err, model.ErrModelPriceDuplicated) {
			// 唯一约束冲突属于"请求的组合已存在"，用 400 给出可读原因，
			// 而不是 500/409 这类让人摸不着头脑的状态码。
			oai.WriteError(c.Writer, http.StatusBadRequest,
				priceConflictMessage(price.ChannelID, channelName),
				oai.TypeInvalidRequest, "price_duplicated")
			return
		}
		// 领域校验的错误信息（如"价格不能为负数"）对使用者有直接帮助
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_price")
		return
	}

	s.invalidatePriceCache()
	dto := toModelPriceDTO(price)
	dto.ChannelName = channelName
	c.JSON(http.StatusOK, dto)
}

// handleUpdatePrice 更新计价规则。
func (s *Server) handleUpdatePrice(c *gin.Context) {
	if s.deps.ModelPrices == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"计费模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req modelPriceUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	price, err := s.deps.ModelPrices.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrModelPriceNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "计价规则不存在", oai.TypeInvalidRequest, "price_not_found")
			return
		}
		s.respondInternalError(c, "查询计价规则失败")
		return
	}

	if strings.TrimSpace(req.Model) != "" {
		price.Model = strings.TrimSpace(req.Model)
	}
	if req.PromptPrice != nil {
		price.PromptPrice = *req.PromptPrice
	}
	if req.CachePrice != nil {
		price.CachePrice = *req.CachePrice
	}
	if req.CompletionPrice != nil {
		price.CompletionPrice = *req.CompletionPrice
	}
	if req.PerCallPrice != nil {
		price.PerCallPrice = *req.PerCallPrice
	}
	if req.BillingMode != nil {
		mode := strings.TrimSpace(*req.BillingMode)
		if err := validateBillingMode(mode); err != nil {
			oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
				oai.TypeInvalidRequest, "invalid_billing_mode")
			return
		}
		price.BillingMode = mode
	}
	if group := strings.TrimSpace(req.Group); group != "" {
		price.Group = group
	}
	if req.Enabled != nil {
		price.Enabled = *req.Enabled
	}
	if remark := strings.TrimSpace(req.Remark); remark != "" {
		price.Remark = remark
	}
	// 渠道归属：缺省（未提交）保持原值；显式提交时校验渠道存在
	if req.ChannelID != nil {
		price.ChannelID = *req.ChannelID
	}
	channelName, ok := s.resolveChannelScope(c, price.ChannelID)
	if !ok {
		return
	}

	if err := s.deps.ModelPrices.Update(ctx, price); err != nil {
		if errors.Is(err, model.ErrModelPriceDuplicated) {
			// 改到"另一个渠道已占用的 (模型 + 分组 + 渠道) 组合"时会触发唯一约束；
			// 这属于请求组合非法，用 400 + 可读提示，而不是 500。
			oai.WriteError(c.Writer, http.StatusBadRequest,
				priceConflictMessage(price.ChannelID, channelName),
				oai.TypeInvalidRequest, "price_duplicated")
			return
		}
		if errors.Is(err, model.ErrModelPriceNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "计价规则不存在", oai.TypeInvalidRequest, "price_not_found")
			return
		}
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(), oai.TypeInvalidRequest, "invalid_price")
		return
	}

	s.invalidatePriceCache()
	dto := toModelPriceDTO(price)
	dto.ChannelName = channelName
	c.JSON(http.StatusOK, dto)
}

// handleDeletePrice 删除计价规则。
//
// 删除后该模型变为"未定价"：转发仍可用，但不再扣费，日志中 quota 记 0。
// 这一语义在界面上有说明，避免管理员误以为"删了价格用户就不能用了"。
func (s *Server) handleDeletePrice(c *gin.Context) {
	if s.deps.ModelPrices == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"计费模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	if err := s.deps.ModelPrices.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, model.ErrModelPriceNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "计价规则不存在", oai.TypeInvalidRequest, "price_not_found")
			return
		}
		s.respondInternalError(c, "删除计价规则失败")
		return
	}

	s.invalidatePriceCache()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleQuotePreview 试算某模型的费用，便于管理员核对定价是否合理。
//
// 参数：model、prompt_tokens、completion_tokens（可选，默认按 1000/1000 估算）、
// cached_tokens（可选，默认 0；用于核对缓存命中价的折扣是否按预期生效）、
// group（可选，缺省用计费组件的默认分组）——价格规则按分组隔离，试算也需能指定分组。
//
// 返回值中的 priced 语义是"命中计价规则"，而不是"金额大于 0"：
// 显式免费的规则金额也是 0，若按金额判定，界面会把"免费"误显示成"未定价"。
func (s *Server) handleQuotePreview(c *gin.Context) {
	if s.deps.Billing == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"计费模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	modelName := strings.TrimSpace(c.Query("model"))
	if modelName == "" {
		oai.WriteError(c.Writer, http.StatusBadRequest, "缺少 model 参数", oai.TypeInvalidRequest, "missing_model")
		return
	}
	group := strings.TrimSpace(c.Query("group"))
	promptTokens := parseInt64Query(c, "prompt_tokens", 1000)
	completionTokens := parseInt64Query(c, "completion_tokens", 1000)
	cachedTokens := parseInt64Query(c, "cached_tokens", 0)

	ctx := c.Request.Context()
	quota := s.deps.Billing.Quote(ctx, group, modelName, promptTokens, completionTokens, cachedTokens)
	// 规则详情取自与计费同一份匹配结果，避免"试算说免费、实际在扣费"的不一致。
	price := s.deps.Billing.PriceInfo(ctx, group, modelName)

	// 未定价时把方式报成空串，前端据此显示"未定价"而不是"免费"。
	billingMode := ""
	isFree := false
	if price != nil {
		billingMode = price.EffectiveBillingMode()
		isFree = price.IsFree()
	}

	c.JSON(http.StatusOK, gin.H{
		"model":             modelName,
		"prompt_tokens":     promptTokens,
		"cached_tokens":     cachedTokens,
		"completion_tokens": completionTokens,
		"quota":             quota,
		"priced":            price != nil,
		"billing_mode":      billingMode,
		"is_free":           isFree,
	})
}

// invalidatePriceCache 清空计费价格缓存，使改价立即生效。
//
// 不做这件事的后果：管理员改完价格后，转发链路仍用旧价格计费长达 30 秒，
// 期间产生的日志会显示出"改了价但数字没变"，非常容易误判为功能失效。
func (s *Server) invalidatePriceCache() {
	if s.deps.Billing != nil {
		s.deps.Billing.Invalidate()
	}
}

// parseInt64Query 读取整型查询参数，缺省或非法时返回默认值。
func parseInt64Query(c *gin.Context, key string, fallback int64) int64 {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
