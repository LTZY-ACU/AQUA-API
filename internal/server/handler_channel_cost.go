// 本文件实现「上游计费与密钥余额核算」的后台接口。
//
// 意图（Why）：
//
//	站长需要回答三个连环问题，缺任何一个都会让"成本"这件事无从下手：
//	  1) 这个渠道的每个模型，上游怎么收我的钱？   → 本文件的成本规则读写
//	  2) 我这把密钥已经用掉多少了？               → 按密钥聚合用量 × 进价
//	  3) 所以它还剩多少？                         → 录入余额 − 已消耗
//
//	第 3 步刻意【只算不写】：网关无法得知上游真实扣费（上游可能按 token、
//	按次、按模型分别计价，中间还有代理二次计费），把估算值写回余额会产生
//	与上游对不上的假数字。因此余额始终是"站长录入的快照"，
//	本接口给出的是"按当前进价估算的消耗与剩余"，供站长据此更新快照。
//	这与迁移 0022 的取舍完全一致，只是把"必须靠记忆"变成了"有据可查"。
//
// 流转（Flow）：
//
//	渠道页「上游计费」→ GET/PUT /api/admin/channels/:id/costs
//	渠道页「密钥池」  → GET     /api/admin/channels/:id/key-usage
//	  └─ 读 channel_model_costs（进价）
//	     + SumUsageByChannelKey（按密钥+模型聚合用量）
//	     + channel_keys.balance（人工录入的余额快照）
//	     → 逐模型匹配进价 → 估算已消耗 → 剩余 = 余额 − 已消耗
//
// 扩展（Extend）：
//
//	新增成本口径时：在 model.ChannelModelCost 加字段 → 建迁移加列 → 同步 store 的
//	列清单/INSERT/UPDATE/scan 四处 → 本文件 DTO 与入参补齐。
package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// channelModelCostDTO 是上游进价规则的对外表示。
type channelModelCostDTO struct {
	ID              uint64 `json:"id"`
	Model           string `json:"model"`
	PromptPrice     int64  `json:"prompt_price"`
	CachePrice      int64  `json:"cache_price"`
	CompletionPrice int64  `json:"completion_price"`
	PerCallPrice    int64  `json:"per_call_price"`
	Remark          string `json:"remark"`
	// IsFree 是派生布尔：四个价格全为 0，表示"上游免费"这一明确结论。
	//
	// 界面必须把它与"未录入成本"区分显示——前者是结论，后者是待办。
	IsFree    bool  `json:"is_free"`
	UpdatedAt int64 `json:"updated_at"`
}

// toChannelModelCostDTO 把领域模型转为对外 DTO。
func toChannelModelCostDTO(cost *model.ChannelModelCost) channelModelCostDTO {
	if cost == nil {
		return channelModelCostDTO{}
	}
	return channelModelCostDTO{
		ID:              cost.ID,
		Model:           cost.Model,
		PromptPrice:     cost.PromptPrice,
		CachePrice:      cost.CachePrice,
		CompletionPrice: cost.CompletionPrice,
		PerCallPrice:    cost.PerCallPrice,
		Remark:          cost.Remark,
		IsFree:          cost.IsFree(),
		UpdatedAt:       unixOrZero(cost.UpdatedAt),
	}
}

// handleListChannelCosts 返回某渠道的上游进价规则。
//
// 同时返回该渠道声明的模型清单（channel.Models）：让界面可以"按渠道模型预填"，
// 避免站长手工敲模型名——敲错一个字符，成本规则就会静默不生效。
func (s *Server) handleListChannelCosts(c *gin.Context) {
	if s.deps.ChannelModelCosts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"上游成本模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	channelID, ok := s.parseChannelIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	costs, err := s.deps.ChannelModelCosts.ListByChannel(ctx, channelID)
	if err != nil {
		s.respondInternalError(c, "查询上游成本失败")
		return
	}

	items := make([]channelModelCostDTO, 0, len(costs))
	for _, cost := range costs {
		items = append(items, toChannelModelCostDTO(cost))
	}

	// 渠道声明的模型清单属于"锦上添花"，取不到不影响成本编辑
	declared := []string{}
	if s.deps.Channels != nil {
		if ch, err := s.deps.Channels.GetByID(ctx, channelID); err == nil && ch != nil {
			declared = nonNilStrings(ch.Models)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"items":           items,
		"total":           len(items),
		"declared_models": declared,
	})
}

// channelCostUpsertRequest 是保存上游进价的请求体（整体替换语义）。
type channelCostUpsertRequest struct {
	Items []channelCostItemPayload `json:"items"`
}

// channelCostItemPayload 是单条进价的可写入参。
type channelCostItemPayload struct {
	Model           string `json:"model"`
	PromptPrice     int64  `json:"prompt_price"`
	CachePrice      int64  `json:"cache_price"`
	CompletionPrice int64  `json:"completion_price"`
	PerCallPrice    int64  `json:"per_call_price"`
	Remark          string `json:"remark"`
}

// handleReplaceChannelCosts 整体替换某渠道的上游进价规则。
//
// 使用"整体替换"而不是逐条增删：站长在界面上就是把这一批模型的价格一次改完，
// 一次提交语义最清晰，也避免"删了一半失败"的中间状态（仓储内部有事务）。
func (s *Server) handleReplaceChannelCosts(c *gin.Context) {
	if s.deps.ChannelModelCosts == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"上游成本模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	channelID, ok := s.parseChannelIDParam(c)
	if !ok {
		return
	}

	var req channelCostUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	costs := make([]*model.ChannelModelCost, 0, len(req.Items))
	for _, item := range req.Items {
		cost := &model.ChannelModelCost{
			ChannelID:       channelID,
			Model:           strings.TrimSpace(item.Model),
			PromptPrice:     item.PromptPrice,
			CachePrice:      item.CachePrice,
			CompletionPrice: item.CompletionPrice,
			PerCallPrice:    item.PerCallPrice,
			Remark:          strings.TrimSpace(item.Remark),
		}
		// 模型名为空的行直接忽略：界面上的"新增一行"会先给一个空行，
		// 若因此整批拒绝，站长每次都要先删空行才能保存。
		if cost.Model == "" {
			continue
		}
		costs = append(costs, cost)
	}

	created, updated, err := s.deps.ChannelModelCosts.ReplaceForChannel(c.Request.Context(), channelID, costs)
	if err != nil {
		// 领域与仓储层的校验信息（哪条非法、哪个模型重复）对使用者有直接帮助
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_channel_cost")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"created": created,
		"updated": updated,
		"total":   len(costs),
	})
}

// ---------------------------------------------------------------------------
// 密钥余额核算
// ---------------------------------------------------------------------------

// channelKeyModelUsageDTO 是一把密钥在某个模型上的用量与成本。
type channelKeyModelUsageDTO struct {
	Model            string `json:"model"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	// Cost 是本次按上游进价估算的成本（额度单位）。
	Cost int64 `json:"cost"`
	// Priced 表示该模型是否匹配到了进价规则。
	//
	// false 时必须让界面明确提示"未录进价"，否则站长会以为"这个模型不花钱"——
	// 而真相是"我们还不知道它花多少钱"。
	Priced bool `json:"priced"`
}

// channelKeyUsageDTO 是一把密钥的用量与余额核算结果。
type channelKeyUsageDTO struct {
	ChannelKeyID     uint64 `json:"channel_key_id"`
	Label            string `json:"label"`
	Status           int    `json:"status"`
	AccountHint      string `json:"account_hint"`
	Balance          int64  `json:"balance"`
	BalanceUnknown   bool   `json:"balance_unknown"`
	BalanceExhausted bool   `json:"balance_exhausted"`
	BalanceUpdatedAt int64  `json:"balance_updated_at"`

	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	// EstimatedCost 是按上游进价估算的累计消耗（额度单位）。
	//
	// 它只统计"能匹配到进价规则"的模型；未匹配的部分通过
	// UnpricedModels / PricedRequests 显式暴露，避免把"未知"当成"零成本"。
	EstimatedCost int64 `json:"estimated_cost"`
	// PricedRequests / UnpricedRequests 分别是能/不能估算成本的请求数。
	PricedRequests   int64 `json:"priced_requests"`
	UnpricedRequests int64 `json:"unpriced_requests"`
	// Remaining 是估算剩余（录入余额 − 估算消耗）；仅在 BalanceKnown 为 true 时有意义。
	//
	// 允许为负：负值表示"按当前进价估算，这把密钥已经用超了"，
	// 这正是站长最需要看到的信号（比显示 0 更有信息量）。
	Remaining int64 `json:"remaining"`
	// BalanceKnown 表示站长是否录入过余额（即 Balance != -1）。
	BalanceKnown bool `json:"balance_known"`

	Models         []channelKeyModelUsageDTO `json:"models"`
	UnpricedModels []string                  `json:"unpriced_models"`
}

// handleChannelKeyUsage 返回某渠道下各把密钥的用量、估算消耗与剩余余额。
//
// 参数 from_keys_pool：是否只统计"仍在密钥池里"的凭据（默认 true）。
// 之所以默认只算池内：已删除的密钥在日志里仍有历史用量，但列表里已经看不到它，
// 显示出来只会让站长困惑（那些行没有"剩余"可言）。
func (s *Server) handleChannelKeyUsage(c *gin.Context) {
	if s.deps.UsageLogs == nil || s.deps.ChannelKeys == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"核算模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	channelID, ok := s.parseChannelIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	usageRows, err := s.deps.UsageLogs.SumUsageByChannelKey(ctx, channelID)
	if err != nil {
		s.respondInternalError(c, "汇总密钥用量失败")
		return
	}

	keys, err := s.deps.ChannelKeys.ListByChannel(ctx, channelID)
	if err != nil {
		s.respondInternalError(c, "查询渠道密钥失败")
		return
	}

	costs := []*model.ChannelModelCost{}
	if s.deps.ChannelModelCosts != nil {
		loaded, err := s.deps.ChannelModelCosts.ListByChannel(ctx, channelID)
		if err != nil {
			// 进价为"锦上添花"：取不到时仍返回用量，只是全部标记为"未录进价"。
			// 这样站长至少能看到"用了多少"，而不是整页打不开。
			costs = nil
		} else {
			costs = loaded
		}
	}

	items := buildChannelKeyUsage(keyListToMap(keys), usageRows, costs)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// buildChannelKeyUsage 把「密钥列表 + 用量汇总 + 进价规则」合成为核算结果。
//
// 抽成独立函数的原因：这段逻辑是纯计算、没有 I/O，单测可以直接喂数据验证
// （"未录进价不能按 0 成本算"这类规则必须锁住）。
func buildChannelKeyUsage(keys map[uint64]*model.ChannelKey,
	usageRows []*model.ChannelKeyUsage, costs []*model.ChannelModelCost) []channelKeyUsageDTO {
	byKey := make(map[uint64]*channelKeyUsageDTO, len(keys))

	// 先按"仍在池内"的密钥建骨架：没有用量的密钥也要出现在列表里，
	// 且要显示"已用 0 / 剩余 = 录入余额"，否则站长会以为余额显示丢了。
	for id, key := range keys {
		byKey[id] = newChannelKeyUsageDTO(key)
	}

	for _, row := range usageRows {
		if row == nil || row.ChannelKeyID == 0 {
			continue
		}
		item, exists := byKey[row.ChannelKeyID]
		if !exists {
			// 该密钥已被删除（日志里仍有历史用量）：不展示，
			// 因为它没有"剩余"可言，混在列表里只会让人困惑。
			continue
		}

		item.Requests += row.Requests
		item.PromptTokens += row.PromptTokens
		item.CompletionTokens += row.CompletionTokens
		item.CachedTokens += row.CachedTokens

		detail := channelKeyModelUsageDTO{
			Model:            row.Model,
			Requests:         row.Requests,
			PromptTokens:     row.PromptTokens,
			CompletionTokens: row.CompletionTokens,
			CachedTokens:     row.CachedTokens,
		}

		if matched := model.MatchChannelModelCost(costs, row.CostModelName()); matched != nil {
			detail.Priced = true
			// 用 ComputeCost 而不是 ComputeTokenCost：按次计费的进价规则
			// （只填了 PerCallPrice）必须按"次数 × 每次单价"算，否则这里恒为 0，
			// 表现为"密钥余额不减、毛利虚高"（详见 model.ChannelModelCost.ComputeCost）。
			detail.Cost = matched.ComputeCost(row.PromptTokens, row.CompletionTokens, row.CachedTokens, row.Requests)
			item.PricedRequests += row.Requests
			item.EstimatedCost += detail.Cost
		} else {
			// 未录进价：请求数照实累计，但成本保持 0 并单独标记。
			// 关键：绝不能把"未知成本"当成 0 元混进总额——
			// 那会让站长以为这个模型不花钱。
			item.UnpricedRequests += row.Requests
			item.UnpricedModels = append(item.UnpricedModels, row.Model)
		}

		item.Models = append(item.Models, detail)
	}

	result := make([]channelKeyUsageDTO, 0, len(byKey))
	for _, item := range byKey {
		sort.SliceStable(item.Models, func(i, j int) bool {
			return item.Models[i].Cost > item.Models[j].Cost
		})
		item.UnpricedModels = dedupeStrings(item.UnpricedModels)
		if item.BalanceKnown {
			item.Remaining = item.Balance - item.EstimatedCost
		}
		result = append(result, *item)
	}
	// 按"剩余最少"排序：余额快见底/已超支的密钥排在前面，这才是站长要处理的对象。
	// 未录入余额的排在最后（没有剩余可言）。
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		switch {
		case left.BalanceKnown && !right.BalanceKnown:
			return true
		case !left.BalanceKnown && right.BalanceKnown:
			return false
		case !left.BalanceKnown && !right.BalanceKnown:
			return left.ChannelKeyID < right.ChannelKeyID
		default:
			if left.Remaining != right.Remaining {
				return left.Remaining < right.Remaining
			}
			return left.ChannelKeyID < right.ChannelKeyID
		}
	})
	return result
}

// newChannelKeyUsageDTO 由密钥记录生成核算结果的初始骨架。
func newChannelKeyUsageDTO(key *model.ChannelKey) *channelKeyUsageDTO {
	item := &channelKeyUsageDTO{
		ChannelKeyID: key.ID,
		Label:        key.Label,
		Status:       int(key.Status),
		AccountHint:  key.AccountHint,
		Balance:      key.Balance,
		// BalanceUnknown(-1) 是"未录入"的哨兵值，这里换算成对前端更友好的布尔
		BalanceUnknown:   key.Balance == model.BalanceUnknown,
		BalanceExhausted: key.BalanceExhausted(),
		BalanceUpdatedAt: unixOrZero(key.BalanceUpdatedAt),
		Models:           []channelKeyModelUsageDTO{},
		UnpricedModels:   []string{},
	}
	item.BalanceKnown = !item.BalanceUnknown
	return item
}

// keyListToMap 把密钥切片转为 map（核算时需要按 ID 快速定位）。
func keyListToMap(keys []*model.ChannelKey) map[uint64]*model.ChannelKey {
	result := make(map[uint64]*model.ChannelKey, len(keys))
	for _, key := range keys {
		if key != nil && key.ID != 0 {
			result[key.ID] = key
		}
	}
	return result
}

// dedupeStrings 去重并保持原顺序（未录进价的模型列表里同一模型可能出现多次）。
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// parseChannelIDParam 解析路径中的渠道 ID。
//
// 与 parseIDParam 的区别：额外校验渠道是否真实存在，避免对不存在的渠道
// 写入一批"孤儿成本规则"（它们永远不会被任何渠道读到，只会污染数据）。
func (s *Server) parseChannelIDParam(c *gin.Context) (uint64, bool) {
	id, ok := parseIDParam(c)
	if !ok {
		return 0, false
	}
	if s.deps.Channels != nil {
		if _, err := s.deps.Channels.GetByID(c.Request.Context(), id); err != nil {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, "channel_not_found")
			return 0, false
		}
	}
	return id, true
}
