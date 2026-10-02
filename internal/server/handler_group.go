// 本文件实现「模型分组」的管理接口与「模型广场」的公开接口。
//
// 意图（Why）：
//
//	分组是运营抓手：渠道归属于分组、计价规则按分组区分，
//	因此"给不同人群不同的价格与不同的上游"只需改分组配置。
//	本文件提供两部分能力：
//	  1) 后台分组 CRUD（含倍率），并在删除前阻止"仍被渠道/价格引用"的分组；
//	  2) 模型广场：把手上的能力（可用模型 + 分组 + 价格）聚合成一份
//	     用户可浏览的清单——这是使用者了解"这个网关能做什么"的唯一入口。
//
// 模型广场的数据来源与组装规则（重要）：
//
//	模型清单 = 渠道声明的模型 ∪ 计价规则里的具体模型（排除通配模式）
//	  - 渠道侧的模型代表"现在真的能调"（available = true）；
//	  - 价格侧可能有尚未配置渠道的模型（提前定价很常见），
//	    这类模型也列出但标记 available = false，避免用户调用后才发现不可用。
//
//	价格按分组展示：同一模型在不同分组可以有不同价格（这正是分组的意义）。
//
// 流转（Flow）：
//
//	后台：GroupsView → GET/POST/PUT/DELETE /api/admin/groups
//	广场：模型广场页 → GET /api/models（公开）→ 分组 + 模型卡片数据
//
// 扩展（Extend）：
//
//	新增展示维度（如模型能力标签、上下文长度）时：
//	在 model 侧新增字段（渠道或价格表），在本文件的 assemble* 中补充映射。
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
	"gitee.com/xiaosu4610/aqua-api/internal/oai"
	"gitee.com/xiaosu4610/aqua-api/internal/server/middleware"
)

// ---------------------------------------------------------------------------
// 模型分组（后台）
// ---------------------------------------------------------------------------

// modelGroupDTO 是分组的对外表示。
type modelGroupDTO struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Label       string `json:"label"`
	Ratio       int64  `json:"ratio"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// UnlockMinRechargeCents 是把令牌挂到本分组所需的累计充值下限（单位：分，0 = 无门槛）。
	// 用"分"而不是"元"透出：金额一旦经过浮点就会在门槛比较上出现 99.99999 < 100 之类的
	// 假性未达标，整数分是唯一安全的表示（前端负责换算成元展示与输入）。
	UnlockMinRechargeCents int64 `json:"unlock_min_recharge_cents"`
	// AdminOnly 表示本分组只能由管理员分发（门户不下发、非管理员指定即 403）。
	// 用于批发价分组（如"代理拿货"）：这类价格一旦能被自助拿到，价格体系就塌了。
	AdminOnly bool `json:"admin_only"`
	// ChannelCount / PriceCount 是引用统计，便于管理员判断"这个分组能不能删"。
	ChannelCount int   `json:"channel_count"`
	PriceCount   int   `json:"price_count"`
	CreatedAt    int64 `json:"created_at"`
	UpdatedAt    int64 `json:"updated_at"`
}

// toModelGroupDTO 把领域模型转为对外 DTO。
func toModelGroupDTO(group *model.ModelGroup, channelCount, priceCount int) modelGroupDTO {
	if group == nil {
		return modelGroupDTO{}
	}
	return modelGroupDTO{
		ID:                     group.ID,
		Name:                   group.Name,
		DisplayName:            group.DisplayName,
		Label:                  group.Label(),
		Ratio:                  group.Ratio,
		UnlockMinRechargeCents: group.UnlockMinRechargeCents,
		AdminOnly:              group.AdminOnly,
		Description:            group.Description,
		Enabled:                group.Enabled,
		ChannelCount:           channelCount,
		PriceCount:             priceCount,
		CreatedAt:              unixOrZero(group.CreatedAt),
		UpdatedAt:              unixOrZero(group.UpdatedAt),
	}
}

// handleListGroups 处理 GET /api/admin/groups。
//
// 同时返回每个分组被多少渠道与计价规则引用：界面上据此提示
// "该分组正在被使用，删除会影响 N 个渠道"，避免误删。
func (s *Server) handleListGroups(c *gin.Context) {
	if s.deps.Groups == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"分组模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	groups, err := s.deps.Groups.List(ctx, model.ModelGroupQuery{Limit: 200})
	if err != nil {
		s.respondInternalError(c, "查询分组列表失败")
		return
	}

	channelCounts, priceCounts := s.groupReferenceCounts(ctx)

	items := make([]modelGroupDTO, 0, len(groups))
	for _, group := range groups {
		items = append(items, toModelGroupDTO(group, channelCounts[group.Name], priceCounts[group.Name]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// groupReferenceCounts 统计每个分组被渠道与计价规则引用的次数。
//
// 实现说明：两张表的数据量都很小（渠道几十个、价格几十条），
// 因此一次性全量统计即可，无需为它设计专门的聚合查询。
func (s *Server) groupReferenceCounts(ctx context.Context) (map[string]int, map[string]int) {
	channelCounts := make(map[string]int)
	priceCounts := make(map[string]int)

	if s.deps.Channels != nil {
		if channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Limit: 500}); err == nil {
			for _, channel := range channels {
				// 一个渠道可服务多个分组，逐个计入（清单恒非空）。
				// 若只记主分组，多分组渠道在"分组引用统计"里会少算，
				// 站长可能据此误以为某个分组没有渠道而重复建渠道。
				for _, name := range channel.GroupList() {
					channelCounts[name]++
				}
			}
		}
	}
	if s.deps.ModelPrices != nil {
		if prices, err := s.deps.ModelPrices.List(ctx, "", false); err == nil {
			for _, price := range prices {
				priceCounts[price.Group]++
			}
		}
	}
	return channelCounts, priceCounts
}

// modelGroupUpsertRequest 是新增/更新分组的请求体。
type modelGroupUpsertRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Ratio       *int64 `json:"ratio"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
	// UnlockMinRechargeCents 是解锁门槛（分）。用指针区分"未传"与"传 0"：
	// 未传 = 保持原值，传 0 = 明确清除门槛。
	UnlockMinRechargeCents *int64 `json:"unlock_min_recharge_cents"`
	// AdminOnly 是"仅后台可分发的分组"开关。同样用指针区分未传与显式 false：
	// 未传 = 保持原值（避免"只想改倍率"的一次 PUT 把批发价分组意外放开给所有用户）。
	AdminOnly *bool `json:"admin_only"`
}

// handleCreateGroup 处理 POST /api/admin/groups。
func (s *Server) handleCreateGroup(c *gin.Context) {
	if s.deps.Groups == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"分组模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	var req modelGroupUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	group := &model.ModelGroup{
		Name:        strings.TrimSpace(req.Name),
		DisplayName: strings.TrimSpace(req.DisplayName),
		Description: strings.TrimSpace(req.Description),
		Ratio:       100, // 默认 1.0 倍
		Enabled:     true,
	}
	if req.Ratio != nil {
		group.Ratio = *req.Ratio
	}
	if req.Enabled != nil {
		group.Enabled = *req.Enabled
	}
	if req.UnlockMinRechargeCents != nil {
		group.UnlockMinRechargeCents = *req.UnlockMinRechargeCents
	}
	if req.AdminOnly != nil {
		group.AdminOnly = *req.AdminOnly
	}

	if err := s.deps.Groups.Create(c.Request.Context(), group); err != nil {
		if errors.Is(err, model.ErrModelGroupDuplicated) {
			oai.WriteError(c.Writer, http.StatusConflict,
				"该分组标识已存在", oai.TypeInvalidRequest, "group_duplicated")
			return
		}
		// 领域校验信息（如"倍率必须大于 0"）对使用者有直接帮助
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_group")
		return
	}

	c.JSON(http.StatusOK, toModelGroupDTO(group, 0, 0))
}

// handleUpdateGroup 处理 PUT /api/admin/groups/{id}。
func (s *Server) handleUpdateGroup(c *gin.Context) {
	if s.deps.Groups == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"分组模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req modelGroupUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "请求体格式错误",
			oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ctx := c.Request.Context()
	group, err := s.findGroupByID(ctx, id)
	if err != nil {
		s.respondGroupLookupError(c, err)
		return
	}

	// 标识不可改：它被渠道与价格表引用，改名等于让历史配置全部失联
	if name := strings.TrimSpace(req.Name); name != "" && name != group.Name {
		oai.WriteError(c.Writer, http.StatusBadRequest,
			"分组标识不可修改（渠道与计价规则通过它关联）；如需改名请新建分组后迁移配置",
			oai.TypeInvalidRequest, "group_name_immutable")
		return
	}
	if req.DisplayName != "" {
		group.DisplayName = strings.TrimSpace(req.DisplayName)
	}
	if req.Ratio != nil {
		group.Ratio = *req.Ratio
	}
	if req.Description != "" {
		group.Description = strings.TrimSpace(req.Description)
	}
	if req.Enabled != nil {
		group.Enabled = *req.Enabled
	}
	if req.UnlockMinRechargeCents != nil {
		group.UnlockMinRechargeCents = *req.UnlockMinRechargeCents
	}
	if req.AdminOnly != nil {
		group.AdminOnly = *req.AdminOnly
	}

	if err := s.deps.Groups.Update(ctx, group); err != nil {
		if errors.Is(err, model.ErrModelGroupNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "分组不存在",
				oai.TypeInvalidRequest, "group_not_found")
			return
		}
		oai.WriteError(c.Writer, http.StatusBadRequest, err.Error(),
			oai.TypeInvalidRequest, "invalid_group")
		return
	}

	// 倍率会影响后续所有计费，必须立即清缓存，否则管理员会看到
	// "改了倍率但扣费没变"，非常容易误判为功能失效。
	s.invalidatePriceCache()

	channelCounts, priceCounts := s.groupReferenceCounts(ctx)
	c.JSON(http.StatusOK, toModelGroupDTO(group, channelCounts[group.Name], priceCounts[group.Name]))
}

// handleDeleteGroup 处理 DELETE /api/admin/groups/{id}。
func (s *Server) handleDeleteGroup(c *gin.Context) {
	if s.deps.Groups == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"分组模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	group, err := s.findGroupByID(ctx, id)
	if err != nil {
		s.respondGroupLookupError(c, err)
		return
	}

	// 引用校验：删除仍被使用的分组会让这些渠道/价格"失去归属"，
	// 表现为渠道静默地从路由中消失（分组名再也匹配不上）。
	// 这种故障极难定位，因此在入口处直接拦住。
	channelCounts, priceCounts := s.groupReferenceCounts(ctx)
	if count := channelCounts[group.Name]; count > 0 {
		oai.WriteError(c.Writer, http.StatusConflict,
			"该分组仍被 "+strconv.Itoa(count)+" 个渠道使用，请先调整这些渠道的分组",
			oai.TypeInvalidRequest, "group_in_use")
		return
	}
	if count := priceCounts[group.Name]; count > 0 {
		oai.WriteError(c.Writer, http.StatusConflict,
			"该分组仍被 "+strconv.Itoa(count)+" 条计价规则使用，请先调整这些规则的分组",
			oai.TypeInvalidRequest, "group_in_use")
		return
	}
	if group.Name == model.DefaultGroupName {
		oai.WriteError(c.Writer, http.StatusConflict,
			"默认分组不可删除", oai.TypeInvalidRequest, "group_protected")
		return
	}

	if err := s.deps.Groups.Delete(ctx, id); err != nil {
		if errors.Is(err, model.ErrModelGroupNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "分组不存在",
				oai.TypeInvalidRequest, "group_not_found")
			return
		}
		s.respondInternalError(c, "删除分组失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// findGroupByID 按主键查找分组。
//
// 仓储只提供 GetByName（分组名才是业务主键），因此在内存中过滤——
// 分组总量最多几十个，线性查找完全可接受，避免为它再加一个仓储方法。
func (s *Server) findGroupByID(ctx context.Context, id uint64) (*model.ModelGroup, error) {
	groups, err := s.deps.Groups.List(ctx, model.ModelGroupQuery{Limit: 200})
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, nil
		}
	}
	return nil, model.ErrModelGroupNotFound
}

// respondGroupLookupError 统一处理分组查询失败。
func (s *Server) respondGroupLookupError(c *gin.Context, err error) {
	if errors.Is(err, model.ErrModelGroupNotFound) {
		oai.WriteError(c.Writer, http.StatusNotFound, "分组不存在",
			oai.TypeInvalidRequest, "group_not_found")
		return
	}
	s.respondInternalError(c, "查询分组失败")
}

// ---------------------------------------------------------------------------
// 模型清单（OpenAI 兼容：GET /v1/models）
// ---------------------------------------------------------------------------

// openAIModelDTO 是 OpenAI 兼容的单个模型对象。
type openAIModelDTO struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// handleListModels 处理 GET /v1/models（OpenAI 兼容的模型清单）。
//
// 为什么必须提供：大量客户端（SDK、IDE 插件、Web UI）在启动时会先调
// /v1/models 来填充模型下拉框；没有这个接口时它们会显示"未获取到模型列表"，
// 使用者往往误以为网关坏了（生产日志里确实出现过这个 404）。
//
// 返回内容：
//
//	所有【启用渠道】声明模型的并集，再按令牌白名单过滤——
//	令牌看不到自己无权调用的模型，这与 OpenAI 的语义一致
//	（列出的模型应当都是该 Key 能用的）。
//
// 边界处理：若渠道都没有声明模型（过渡约定：空清单 = 支持全部模型），
// 则退回"计价规则里出现过的具体模型名"，让使用者至少能看到已定价的模型。
func (s *Server) handleListModels(c *gin.Context) {
	if s.deps.Channels == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"网关未就绪", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	enabled := model.ChannelStatusEnabled
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Status: &enabled, Limit: 500})
	if err != nil {
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	token, _ := middleware.TokenFromContext(c)

	seen := make(map[string]struct{})
	for _, channel := range channels {
		for _, name := range channel.Models {
			modelName := strings.TrimSpace(name)
			if modelName == "" {
				continue
			}
			// 令牌白名单过滤：不让令牌看到自己无权调用的模型
			if token != nil && !token.AllowsModel(modelName) {
				continue
			}
			seen[modelName] = struct{}{}
		}
	}

	// 渠道未声明模型时的兜底：用已定价的模型名（排除通配模式）
	if len(seen) == 0 && s.deps.ModelPrices != nil {
		if prices, err := s.deps.ModelPrices.List(ctx, "", true); err == nil {
			for _, price := range prices {
				modelName := strings.TrimSpace(price.Model)
				if modelName == "" || price.PatternKind() != model.PatternExact {
					continue
				}
				if token != nil && !token.AllowsModel(modelName) {
					continue
				}
				seen[modelName] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	data := make([]openAIModelDTO, 0, len(names))
	for _, name := range names {
		data = append(data, openAIModelDTO{
			ID:     name,
			Object: "model",
			// Created 填 0：模型在本网关里没有"创建时间"这一语义，
			// 客户端只把它当作可排序字段，填 0 比编造一个时间更诚实。
			Created: 0,
			// owned_by 标注为本网关，明确"这些模型是通过网关转发的"。
			OwnedBy: "aqua-api",
		})
	}

	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ---------------------------------------------------------------------------
// 分组（用户门户）
// ---------------------------------------------------------------------------

// portalGroupDTO 是门户侧（访问令牌的"所属分组"下拉）看到的分组信息。
//
// 与广场的 plazaGroupDTO 分开的理由：广场是【公开】数据（不含任何用户信息），
// 而本结构体带有"当前用户是否已解锁"的判断，必须登录后下发，
// 两者混在一起会让公开接口意外泄露用户资格信息。
type portalGroupDTO struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Ratio       int64  `json:"ratio"`
	Description string `json:"description"`
	// UnlockMinRechargeCents 是解锁本分组所需的累计充值（分）；0 表示无门槛。
	UnlockMinRechargeCents int64 `json:"unlock_min_recharge_cents"`
	// Unlocked 表示当前用户是否已解锁（无门槛时恒为 true）。
	Unlocked bool `json:"unlocked"`
	// PaidAmountCents 是当前用户的累计充值（分），前端据此显示"还差多少解锁"。
	PaidAmountCents int64 `json:"paid_amount_cents"`
}

// handleMyGroups 处理 GET /api/user/groups（当前用户可选的分组）。
//
// 为什么需要这个接口而不是直接复用公开的模型广场：广场不知道"你是谁"，
// 无法告诉前端"这个分组你还没解锁"。把门槛下发到前端只是体验（置灰 + 提示），
// 真正的闸门在服务端（见 resolveTokenGroupName）——两处都做才既好用又绕不过。
func (s *Server) handleMyGroups(c *gin.Context) {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		writeUserError(c, http.StatusUnauthorized,
			"auth.not_logged_in", oai.TypeAuthentication, oai.CodeMissingAPIKey)
		return
	}
	if s.deps.Groups == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"分组模块未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	ctx := c.Request.Context()
	groups, err := s.deps.Groups.List(ctx, model.ModelGroupQuery{EnabledOnly: true, Limit: 200})
	if err != nil {
		s.respondInternalError(c, "查询分组列表失败")
		return
	}
	served, err := s.servedGroupNames(ctx)
	if err != nil {
		s.respondInternalError(c, "查询可用分组失败")
		return
	}

	paid := int64(0)
	if s.deps.Orders != nil {
		paid, err = s.deps.Orders.SumPaidAmountCents(ctx, user.ID)
		if err != nil {
			s.respondInternalError(c, "查询累计充值金额失败")
			return
		}
	}

	items := make([]portalGroupDTO, 0, len(groups))
	for _, group := range groups {
		// 只下发"确实有启用渠道在服务"的分组：选到空分组后所有请求都会
		// 503（无可用渠道），而用户从界面上完全看不出原因。
		if !served[group.Name] {
			continue
		}
		// 仅后台可分发的分组（批发价）对普通用户直接不下发：
		// 让它出现在下拉里再置灰，等于把"存在一个更便宜的分组"明示给所有人，
		// 反而会引来"为什么我不能用"的追问；服务端的 403 是真正的闸门。
		if group.RequiresAdminGrant() {
			continue
		}
		items = append(items, portalGroupDTO{
			Name:                   group.Name,
			Label:                  group.Label(),
			Ratio:                  group.Ratio,
			Description:            group.Description,
			UnlockMinRechargeCents: group.UnlockMinRechargeCents,
			Unlocked:               paid >= group.UnlockMinRechargeCents,
			PaidAmountCents:        paid,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "paid_amount_cents": paid})
}

// servedGroupNames 返回"至少有一个启用渠道在服务"的分组集合。
//
// 判据与转发路由一致（渠道必须处于启用状态），因此集合里的分组选进去一定能路由到渠道；
// 不在集合里的分组选进去必然 503，不能让它们出现在下拉里。
func (s *Server) servedGroupNames(ctx context.Context) (map[string]bool, error) {
	served := make(map[string]bool)
	if s.deps.Channels == nil {
		return served, nil
	}
	enabled := model.ChannelStatusEnabled
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Status: &enabled, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("server: 查询启用渠道失败: %w", err)
	}
	for _, channel := range channels {
		for _, name := range channel.GroupList() {
			served[name] = true
		}
	}
	return served, nil
}

// adminOnlyGroupNames 返回"仅后台分发"的分组名集合（批发价分组）。
//
// 用途：公开的模型广场必须把这类分组**彻底隐藏**——不只是不显示卡片，
// 连"某个模型归属了哪个分组"也不能带上它。理由与门户分组下拉一致：
// 广场会展示每个分组的倍率，一旦列出「代理拿货 70%」，
// 等于向所有人公开批发折扣，代理价体系就没有意义了。
func (s *Server) adminOnlyGroupNames(ctx context.Context) map[string]bool {
	hidden := make(map[string]bool)
	if s.deps.Groups == nil {
		return hidden
	}
	groups, err := s.deps.Groups.List(ctx, model.ModelGroupQuery{EnabledOnly: true, Limit: 200})
	if err != nil {
		// 查询失败时返回空集合（= 不隐藏）：宁可多显示一个分组，
		// 也不要因为读分组表出错就把整个广场的分组信息抹掉。
		return hidden
	}
	for _, group := range groups {
		if group.RequiresAdminGrant() {
			hidden[group.Name] = true
		}
	}
	return hidden
}

// ---------------------------------------------------------------------------
// 模型广场（公开）
// ---------------------------------------------------------------------------

// plazaModelDTO 是模型广场里的一张"模型卡片"。
type plazaModelDTO struct {
	Model string `json:"model"`
	// Groups 是该模型当前可用的分组（来自渠道声明的分组）。
	Groups []string `json:"groups"`
	// Available 表示"当前至少有一个启用渠道支持它"。
	//
	// 为什么要有这个字段：价格表里可能预先建好了尚未接渠道的模型，
	// 若不加区分地展示为"可用"，用户调用后才报 503，体验很差。
	Available bool `json:"available"`
	// Prices 是按分组给出的价格（同一模型在不同分组可不同价）。
	Prices []plazaPriceDTO `json:"prices"`
	// ListPrice 仅【代理视图】下发：同一模型的原价（未打折），供前端渲染「划线原价」。
	//
	// 与 Prices[0]（代理价）取自同一条价格规则，因此两者天然同源：
	// 原价 = 规则原值（倍率按 100 计），代理价 = 规则原值 × 分组倍率 ÷ 100。
	// 这样即便日后调整折扣，划线价与折后价也不会各说各话。
	ListPrice *plazaPriceDTO `json:"list_price,omitempty"`
	// ChannelCount 是支持该模型的启用渠道数量，作为"供给充足度"的直观指标。
	ChannelCount int `json:"channel_count"`
}

// plazaViewerDTO 描述"当前查看者以什么身份看广场"。
//
// 只有被指派了代理分组的登录用户才会拿到它；其余情况不下发（omitempty），
// 前端据此决定是否渲染"代理价 / 原价划线"的对照视图。
type plazaViewerDTO struct {
	// AgentGroup 是查看者所属的代理分组名。
	AgentGroup string `json:"agent_group"`
	// Label 是该分组的展示名（如「战略代理」）。
	Label string `json:"label"`
	// Ratio 是该分组的计费倍率（百分比，100 = 不打折）。
	// 60 即「拿货 6 折」，前端据此显示折扣文案。
	Ratio int64 `json:"ratio"`
}

// plazaPriceDTO 是模型在某分组下的价格。
type plazaPriceDTO struct {
	Group           string `json:"group"`
	PromptPrice     int64  `json:"prompt_price"`
	CachePrice      int64  `json:"cache_price"`
	CompletionPrice int64  `json:"completion_price"`
	PerCallPrice    int64  `json:"per_call_price"`
	// BillingMode 是生效的计费方式（token / per_call / free）。
	BillingMode string `json:"billing_mode"`
	// IsFree 是派生布尔：显式免费。
	//
	// 广场必须能把"免费"标出来：用户最关心的就是"这个模型要不要花钱"，
	// 让他靠试算去猜是明显的体验缺口。
	IsFree bool `json:"is_free"`
	// Ratio 是该分组的计费倍率（百分比，100 = 1.0 倍），便于用户算实际价格。
	Ratio int64 `json:"ratio"`
}

// plazaGroupDTO 是广场上的分组信息。
type plazaGroupDTO struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Ratio       int64  `json:"ratio"`
	Description string `json:"description"`
	ModelCount  int    `json:"model_count"`
}

// handleModelPlaza 处理 GET /api/models（公开的模型广场数据）。
//
// 参数：
//
//	group  可选，只返回该分组下可用的模型
//	keyword 可选，按模型名模糊过滤
func (s *Server) handleModelPlaza(c *gin.Context) {
	ctx := c.Request.Context()

	if s.deps.Channels == nil {
		oai.WriteError(c.Writer, http.StatusServiceUnavailable,
			"网关未就绪", oai.TypeServer, oai.CodeInternal)
		return
	}

	// 查看者身份：被指派了代理分组的登录用户，按"自己拿货的那一档"看广场。
	// 未登录 / 非代理一律为 nil，后续全部走原有公开逻辑（行为逐字不变）。
	viewer := s.resolvePlazaViewer(ctx, c)

	// 只统计启用渠道：被禁用的渠道不代表"现在能调"
	enabled := model.ChannelStatusEnabled
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Status: &enabled, Limit: 500})
	if err != nil {
		s.respondInternalError(c, "查询渠道失败")
		return
	}

	groups, groupLabels, groupRatios := s.plazaGroups(ctx)
	// 批发价分组（仅后台分发）对公开广场完全不可见，包括模型归属信息
	hiddenGroups := s.adminOnlyGroupNames(ctx)
	// 例外：代理看自己的分组必须放行——对这个人而言批发价不是秘密。
	if viewer != nil {
		delete(hiddenGroups, viewer.AgentGroup)
	}

	groupFilter := strings.TrimSpace(c.Query("group"))
	if hiddenGroups[groupFilter] {
		// 显式按批发价分组查询时直接返回空，而不是"查不到但按价格回退放行"——
		// 价格表里确实有该分组的规则，回退逻辑会把模型放出来，等于绕过了隐藏。
		c.JSON(http.StatusOK, gin.H{
			"items":  []plazaModelDTO{},
			"groups": []plazaGroupDTO{},
			"total":  0,
		})
		return
	}

	prices := []*model.ModelPrice{}
	if s.deps.ModelPrices != nil {
		if loaded, err := s.deps.ModelPrices.List(ctx, "", true); err == nil {
			prices = loaded
		}
	}

	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	// 代理视图锁死在自己的分组上：即使有人在 URL 上手动塞 ?group=xxx，
	// 也不能让他看到别档的模型与价格（广场是公开接口，参数不可信）。
	if viewer != nil {
		groupFilter = viewer.AgentGroup
	}

	// 汇总模型 → 分组集合、渠道数
	modelGroups := make(map[string]map[string]struct{})
	modelChannelCount := make(map[string]int)
	// 模型在【各分组】下的启用渠道数：代理视图据此判断"这档现在真的能调"，
	// 而不是拿全站渠道数糊弄（某模型可能只挂在别的分组上）。
	modelGroupChannelCount := make(map[string]map[string]int)
	for _, channel := range channels {
		for _, modelName := range channel.Models {
			name := strings.TrimSpace(modelName)
			if name == "" {
				continue
			}
			if modelGroups[name] == nil {
				modelGroups[name] = make(map[string]struct{})
			}
			// 模型归属的分组 = 提供它的渠道所服务的全部分组。
			// 多分组渠道必须逐个登记，否则模型广场会漏掉"这个模型在另一个分组也能用"，
			// 使用者可能误以为在其它分组下不可用。
			for _, groupName := range channel.GroupList() {
				if hiddenGroups[groupName] {
					continue // 批发价分组不对外暴露（连归属关系也不给）
				}
				modelGroups[name][groupName] = struct{}{}
				if modelGroupChannelCount[name] == nil {
					modelGroupChannelCount[name] = make(map[string]int)
				}
				modelGroupChannelCount[name][groupName]++
			}
			modelChannelCount[name]++
		}
	}

	// 补上"有价格但还没接渠道"的模型：提前定价是常见做法，
	// 把它们也列出来但标记为不可用，使用者能据此知道"即将可用"。
	for _, price := range prices {
		if price.PatternKind() != model.PatternExact {
			continue // 通配模式不是具体模型，不构成一张卡片
		}
		if _, exists := modelGroups[price.Model]; !exists {
			modelGroups[price.Model] = make(map[string]struct{})
		}
	}

	items := make([]plazaModelDTO, 0, len(modelGroups))
	for modelName, groupSet := range modelGroups {
		if keyword != "" && !strings.Contains(strings.ToLower(modelName), keyword) {
			continue
		}

		// 代理视图：模型必须在本代理分组下有价格规则，才出现在代理的清单里。
		// 这正是"不同分组看到的模型不一样"的落点——管理员用该分组的价格行
		// 决定这一档卖哪些模型，而不是靠另建一张白名单表。
		var agentPrice, listPrice *plazaPriceDTO
		if viewer != nil {
			agentPrice, listPrice = plazaAgentPricePair(prices, modelName, viewer.AgentGroup, viewer.Ratio)
			if agentPrice == nil {
				continue
			}
		}

		groupsOfModel := make([]string, 0, len(groupSet))
		for name := range groupSet {
			groupsOfModel = append(groupsOfModel, name)
		}
		sort.Strings(groupsOfModel)

		if groupFilter != "" {
			if _, ok := groupSet[groupFilter]; !ok {
				// 也可能是"只在该分组有价格但无渠道"的模型
				if !plazaHasPriceInGroup(prices, modelName, groupFilter) {
					continue
				}
			}
		}

		item := plazaModelDTO{
			Model:        modelName,
			Groups:       groupsOfModel,
			Available:    modelChannelCount[modelName] > 0,
			Prices:       plazaPricesFor(prices, modelName, groupRatios, hiddenGroups),
			ChannelCount: modelChannelCount[modelName],
		}
		if viewer != nil {
			// 代理视图只给"本档代理价 + 划线原价"两样东西：
			// 列出其它公开分组的价只会让人比价困惑，也不该暴露他档折扣。
			item.Prices = []plazaPriceDTO{*agentPrice}
			item.ListPrice = listPrice
			item.Groups = []string{viewer.AgentGroup}
			item.Available = modelGroupChannelCount[modelName][viewer.AgentGroup] > 0
			item.ChannelCount = modelGroupChannelCount[modelName][viewer.AgentGroup]
		}
		items = append(items, item)
	}

	// 排序：可用的在前，其次按模型名升序（顺序稳定，便于前端分页与用户查找）
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Available != items[j].Available {
			return items[i].Available
		}
		return items[i].Model < items[j].Model
	})

	// 分组视图：只列出有模型的分组（空分组展示出来只会造成困惑）
	groupViews := make([]plazaGroupDTO, 0, len(groups))
	for _, name := range groups {
		if viewer != nil && name != viewer.AgentGroup {
			continue // 代理视图只保留他所属的那一档
		}
		count := 0
		for _, item := range items {
			if len(item.Groups) == 0 {
				continue
			}
			for _, group := range item.Groups {
				if group == name {
					count++
					break
				}
			}
		}
		if count == 0 && groupFilter == "" {
			continue
		}
		groupViews = append(groupViews, plazaGroupDTO{
			Name:        name,
			Label:       groupLabels[name],
			Ratio:       groupRatios[name],
			ModelCount:  count,
			Description: groupLabels[name+"#desc"],
		})
	}

	resp := gin.H{
		"items":  items,
		"groups": groupViews,
		"total":  len(items),
	}
	if viewer != nil {
		resp["viewer"] = viewer
	}
	c.JSON(http.StatusOK, resp)
}

// resolvePlazaViewer 解析"当前查看者是不是代理、属于哪一档"。
//
// 返回 nil 表示按普通（公开）视图处理——未登录、未被指派代理分组、
// 或所指派的分组已不存在/已停用，都归入这一类。刻意不返回错误：
// 广场是公开接口，代理信息读不出来时应当降级为公开结果，而不是把请求打成 5xx。
func (s *Server) resolvePlazaViewer(ctx context.Context, c *gin.Context) *plazaViewerDTO {
	user, ok := middleware.CurrentUser(c)
	if !ok || user == nil || s.deps.Groups == nil {
		return nil
	}
	name := strings.TrimSpace(user.AgentGroup)
	if name == "" {
		return nil
	}
	group, err := s.deps.Groups.GetByName(ctx, name)
	if err != nil || group == nil || !group.Enabled {
		return nil
	}
	return &plazaViewerDTO{AgentGroup: group.Name, Label: group.Label(), Ratio: group.Ratio}
}

// plazaAgentPricePair 返回代理视图下的两档价格：代理价与原价。
//
// 两者取自【同一条价格规则】，因此天然同源，不会出现"划线价与折后价各说各话"：
//   - 原价   = 规则原值（倍率按 100 计）
//   - 代理价 = 规则原值 × 分组倍率 ÷ 100（与计费链路 applyRatio 同一口径）
//
// 该分组下没有匹配到规则时返回 (nil, nil)，调用方据此把模型排除出代理清单。
//
// 为什么把折后金额直接算进 DTO，而不是只给 ratio 让前端自己乘：
//
//	现有前端的价格格式化函数不感知倍率（公开视图展示的就是规则原值），
//	若此处也给原值，代理视图就会显示成"没打折"，与"拿货 6 折"直接矛盾。
func plazaAgentPricePair(prices []*model.ModelPrice, modelName, group string, ratio int64) (*plazaPriceDTO, *plazaPriceDTO) {
	rows := make([]*model.ModelPrice, 0, 4)
	for _, price := range prices {
		if price.Group == group {
			rows = append(rows, price)
		}
	}
	matched := model.MatchModelPrice(rows, modelName)
	if matched == nil {
		return nil, nil
	}

	list := &plazaPriceDTO{
		Group:           group,
		PromptPrice:     matched.PromptPrice,
		CachePrice:      matched.CachePrice,
		CompletionPrice: matched.CompletionPrice,
		PerCallPrice:    matched.PerCallPrice,
		BillingMode:     matched.EffectiveBillingMode(),
		IsFree:          matched.IsFree(),
		Ratio:           100,
	}
	agent := &plazaPriceDTO{
		Group:           group,
		PromptPrice:     scaleByRatio(matched.PromptPrice, ratio),
		CachePrice:      scaleByRatio(matched.CachePrice, ratio),
		CompletionPrice: scaleByRatio(matched.CompletionPrice, ratio),
		PerCallPrice:    scaleByRatio(matched.PerCallPrice, ratio),
		BillingMode:     matched.EffectiveBillingMode(),
		IsFree:          matched.IsFree(),
		Ratio:           100,
	}
	return agent, list
}

// scaleByRatio 按分组倍率折算金额（百分比整数，100 = 不折算），与计费链路同口径。
func scaleByRatio(base, ratio int64) int64 {
	if ratio <= 0 || ratio == 100 {
		return base
	}
	return base * ratio / 100
}

// plazaGroups 返回分组名列表、展示名映射与倍率映射。
//
// 与分组表解耦的必要性：历史部署里渠道可能使用了未登记的分组名，
// 此时不能因为这些名字不在分组表里就把模型藏起来。
// 因此这里对未知分组回退为"名字即展示名、倍率 1.0"。
func (s *Server) plazaGroups(ctx context.Context) ([]string, map[string]string, map[string]int64) {
	labels := make(map[string]string)
	ratios := make(map[string]int64)

	names := make([]string, 0, 8)
	if s.deps.Groups != nil {
		if groups, err := s.deps.Groups.List(ctx, model.ModelGroupQuery{EnabledOnly: true, Limit: 200}); err == nil {
			for _, group := range groups {
				names = append(names, group.Name)
				labels[group.Name] = group.Label()
				ratios[group.Name] = group.Ratio
				labels[group.Name+"#desc"] = group.Description
			}
		}
	}
	// 保证默认分组一定在列表里（即使分组表被清空，也要能展示默认分组）
	if _, ok := labels[model.DefaultGroupName]; !ok {
		names = append(names, model.DefaultGroupName)
		labels[model.DefaultGroupName] = "默认分组"
		ratios[model.DefaultGroupName] = 100
	}
	sort.Strings(names)
	return names, labels, ratios
}

// plazaPricesFor 返回某模型在各分组下的价格。
//
// 匹配规则复用计费链路的 MatchModelPrice（精确 → 前缀 → 通配），
// 保证"广场上看到的价格"与"实际扣费的价格"永远一致——
// 两处各写一套匹配逻辑必然有一天会漂移，那是用户投诉的源头。
//
// hiddenGroups 里的分组（仅后台分发的批发价档次）会被整体跳过：
// 只把它们从分组列表里藏起来是不够的——逐模型的价格数组同样会暴露
// "存在一个 7 折的档位"，等于把批发价明示给所有人。
func plazaPricesFor(prices []*model.ModelPrice, modelName string,
	groupRatios map[string]int64, hiddenGroups map[string]bool) []plazaPriceDTO {

	byGroup := make(map[string][]*model.ModelPrice)
	for _, price := range prices {
		if hiddenGroups[price.Group] {
			continue
		}
		byGroup[price.Group] = append(byGroup[price.Group], price)
	}

	result := make([]plazaPriceDTO, 0, len(byGroup))
	groups := make([]string, 0, len(byGroup))
	for group := range byGroup {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	for _, group := range groups {
		matched := model.MatchModelPrice(byGroup[group], modelName)
		if matched == nil {
			continue
		}
		result = append(result, plazaPriceDTO{
			Group:           group,
			PromptPrice:     matched.PromptPrice,
			CachePrice:      matched.CachePrice,
			CompletionPrice: matched.CompletionPrice,
			PerCallPrice:    matched.PerCallPrice,
			BillingMode:     matched.EffectiveBillingMode(),
			IsFree:          matched.IsFree(),
			Ratio:           groupRatioOrDefault(groupRatios, group),
		})
	}
	return result
}

// plazaHasPriceInGroup 判断某模型在指定分组下是否有价格规则。
func plazaHasPriceInGroup(prices []*model.ModelPrice, modelName, group string) bool {
	for _, price := range prices {
		if price.Group != group {
			continue
		}
		if price.Matches(modelName) {
			return true
		}
	}
	return false
}

// groupRatioOrDefault 返回分组倍率，未知分组按 1.0 倍处理。
func groupRatioOrDefault(ratios map[string]int64, group string) int64 {
	if ratio, ok := ratios[group]; ok && ratio > 0 {
		return ratio
	}
	return 100
}
