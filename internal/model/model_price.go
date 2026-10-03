// 本文件定义「模型计费价格」领域模型与仓储接口，并给出计费计算。
//
// 意图（Why）：
//
//	网关的核心商业能力是"用量可计量、可结算"。没有价格表，
//	额度就只是一个永远不减少的数字——站长无法限制用户消耗，
//	也无法对上游成本做任何核算。
//
//	把"价格怎么匹配、额度怎么算"放在领域层而不是 relay 或 SQL 里，原因：
//	  1) 计费口径必须唯一：任何一处算错都直接造成多收/少收；
//	  2) 需要被多处复用（转发后扣费、后台预估、报表核算）。
//
// 计费口径（唯一真相，改动时必须同步迁移脚本注释与前端说明）：
//
//	quota = ((promptTokens − cachedTokens) × promptPrice
//	         + cachedTokens × cachePrice
//	         + completionTokens × completionPrice) / 1_000_000
//
//	价格字段表示「每 100 万 token 消耗的站点额度单位」，
//	与上游官方报价口径一致（如 $3 / 1M tokens），避免二次换算。
//
//	缓存命中价 cachePrice 缺省（<=0）时回退为 promptPrice，
//	此时公式退化为上面两项，与引入缓存计价之前的账目完全一致。
//
// 金额一律用 int64 整数（不用浮点）：浮点累加会产生"用了一万次之后差 0.3"这类
// 难以复现的账目问题，而额度扣减是每天发生几十万次的高频操作。
//
// 流转（Flow）：
//
//	后台维护：PricesView → ModelPriceRepository.Create/Update/Delete
//	转发计费：relay 拿到 usage → Billing.Quote(model, group, usage) → 扣减令牌/用户额度
//
// 扩展（Extend）：
//
//	新增计价维度（如按缓存命中价、按图片张数）时：
//	  1) 在本文件加字段与计算分支；
//	  2) 建新迁移加列（切勿改动已发布的 0006）；
//	  3) 同步前端表单与后台接口。
package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrModelPriceNotFound 表示不存在匹配的计价规则。
var ErrModelPriceNotFound = errors.New("model: 计价规则不存在")

// ErrModelPriceDuplicated 表示同分组下已存在同名规则。
var ErrModelPriceDuplicated = errors.New("model: 该分组下已存在同名计价规则")

// 计费方式的取值。
//
// 集中定义成常量而不是散落字符串：这些值会同时出现在数据库、后台接口与前端，
// 拼错时不会编译报错，只会静默回退到"自动判定"，属于最典型的"配了没生效"。
const (
	// BillingModeAuto 表示按价格字段自动判定（默认值，兼容全部历史数据）。
	//
	// 判定规则：只有按次价、没有 token 价 → 按次；其余按量。
	// 保留它的意义：升级不会改变既有规则的计费行为。
	BillingModeAuto = ""
	// BillingModeFree 表示显式免费：无论价格字段填了什么，一律不计费。
	//
	// 与"不配价格"的区别在于意图明确——后台能一眼看出这条是"有意免费"
	// 而不是"漏了定价"，模型广场也能据此显示「免费」标识。
	BillingModeFree = "free"
	// BillingModeToken 表示按 token 计费（输入 / 缓存 / 输出三个价）。
	BillingModeToken = "token"
	// BillingModePerCall 表示按次计费（图像 / 视频 / 异步任务类能力）。
	BillingModePerCall = "per_call"
)

// ChannelScopeAll 表示计价规则【不限渠道】，即"分组默认价"。
//
// 取值性与语义：
//   - 0 = 对所有渠道生效（历史数据与未指定渠道的规则一律是 0，升级后行为不变）；
//   - >0 = 仅对 channels.id 等于该值的渠道生效（"渠道专用价"）。
//
// 为什么用 0 而不是另设"空"值：迁移给既有列加的默认值就是 0，
// 让 0 直接表达"不限"可以让全部历史规则在升级后立即保持原语义，无需回填。
const ChannelScopeAll uint64 = 0

// ModelPrice 表示一条模型计价规则。
type ModelPrice struct {
	ID              uint64 // 主键
	Model           string // 模型名或通配模式（"gpt-4*"、"*"）
	PromptPrice     int64  // 每 1M 输入 token 的额度
	CachePrice      int64  // 每 1M「命中缓存的输入」token 的额度；0 表示按 PromptPrice 计
	CompletionPrice int64  // 每 1M 输出 token 的额度
	PerCallPrice    int64  // 每调用一次的额度（异步任务 / 图像视频类）；0 表示不计费
	// BillingMode 是显式选择的计费方式，取值见 BillingMode* 常量。
	//
	// 空串（BillingModeAuto）表示按价格字段自动判定，用于兼容历史数据；
	// 判定逻辑见 EffectiveBillingMode——计费链路一律使用它，不要直接读本字段。
	BillingMode string
	Group       string // 适用分组
	// ChannelID 是规则适用的渠道 ID：ChannelScopeAll(0) 表示不限渠道（分组默认价），
	// >0 表示仅对该渠道生效的"渠道专用价"。
	//
	// 优先级见 MatchModelPriceForChannel：命中当前渠道的专用价优先于默认价。
	ChannelID uint64
	Enabled   bool   // 是否启用（停用即视为未定价）
	Remark    string // 备注（便于说明定价依据）
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveBillingMode 返回最终生效的计费方式。
//
// 计费链路必须通过本方法取方式，而不是直接读 BillingMode：
// 后者可能为空串（自动），直接用会漏掉"只有按次价"这类既有规则的判定。
//
// 自动判定的依据是"价格字段实际表达了什么口径"：
//   - 只有按次价 → 按次（图像/视频类规则历来如此填写）；
//   - 其余（含全 0） → 按量。全 0 的按量规则结算结果为 0，
//     与迁移前的行为一致，因此不必单独判定为"免费"。
func (p *ModelPrice) EffectiveBillingMode() string {
	if p == nil {
		return BillingModeToken
	}
	switch p.BillingMode {
	case BillingModeFree, BillingModeToken, BillingModePerCall:
		return p.BillingMode
	}
	if p.PerCallPrice > 0 && p.PromptPrice == 0 && p.CachePrice == 0 && p.CompletionPrice == 0 {
		return BillingModePerCall
	}
	return BillingModeToken
}

// IsFree 返回该规则是否为"显式免费"。
//
// 注意与"未定价"的区别：本方法只对命中规则且方式为 free 的情况返回 true；
// 没命中任何规则（priceFor 返回 nil）属于"未定价"，是另一回事。
func (p *ModelPrice) IsFree() bool {
	return p != nil && p.EffectiveBillingMode() == BillingModeFree
}

// IsValidBillingMode 判断计费方式取值是否合法（供后台接口校验使用）。
func IsValidBillingMode(mode string) bool {
	switch mode {
	case BillingModeFree, BillingModeToken, BillingModePerCall:
		return true
	default:
		return false
	}
}

// Validate 校验计价规则。
func (p *ModelPrice) Validate() error {
	if strings.TrimSpace(p.Model) == "" {
		return errors.New("模型名不能为空（可用 * 表示全部模型）")
	}
	if strings.TrimSpace(p.Group) == "" {
		return errors.New("分组不能为空")
	}
	// 价格为负会变成"调用反而加额度"，必须拦住
	if p.PromptPrice < 0 || p.CachePrice < 0 || p.CompletionPrice < 0 || p.PerCallPrice < 0 {
		return fmt.Errorf("价格不能为负数（输入 %d / 缓存 %d / 输出 %d / 每次 %d）",
			p.PromptPrice, p.CachePrice, p.CompletionPrice, p.PerCallPrice)
	}
	// 计费方式只认空串（自动）与三个明确取值：拼错的值会让计费静默走自动分支，
	// 站长会看到"选了免费却还在扣费"，因此必须在入库前拦住。
	if p.BillingMode != BillingModeAuto && !IsValidBillingMode(p.BillingMode) {
		return fmt.Errorf("计费方式非法：%q（可选 free / token / per_call，留空表示自动判定）", p.BillingMode)
	}
	return nil
}

// PatternKind 返回该规则的模式类型。
//
// 实现委托给 PricePattern（见 price_pattern.go）：售价与上游进价必须用同一套
// 匹配语义，否则会出现"两条规则各自命中不同模式"的错账。
func (p *ModelPrice) PatternKind() PatternKind {
	return PricePattern(p.Model).Kind()
}

// Matches 判断该规则是否适用于给定模型名。
//
// 匹配规则见 PricePattern.Matches（精确 / 前缀通配 / 全局通配）。
func (p *ModelPrice) Matches(modelName string) bool {
	return PricePattern(p.Model).Matches(modelName)
}

// specificity 返回模式的具体程度（数值越大越优先），用于"精确 > 长前缀 > 全局"排序。
func (p *ModelPrice) specificity() int {
	return PricePattern(p.Model).Specificity()
}

// ComputeQuota 按用量计算应扣额度（不含缓存维度，等价于 cachedTokens=0）。
//
// 公式见文件头注释；采用整数运算并向下取整（不足 1 单位不计费），
// 这样小额调用不会因四舍五入而虚增费用。
//
// 溢出安全：promptTokens 与 price 的乘积上限约 10^15，远小于 int64 上限（9.2×10^18），
// 因此不需要额外的溢出保护。
func (p *ModelPrice) ComputeQuota(promptTokens, completionTokens int64) int64 {
	return p.ComputeQuotaWithCache(promptTokens, completionTokens, 0)
}

// ComputeQuotaWithCache 按用量计算应扣额度，并对「命中缓存的输入」单独计价。
//
// 为什么要单独一个函数而不是给 ComputeQuota 加参数：
//   - 多数调用点（后台试算、估算预留、无缓存概念的上游）确实只关心两个维度，
//     强制传 0 会让调用点噪声变大；
//   - 缓存计价是本文件里唯一"分两段算输入"的地方，单独命名便于检索与测试。
//
// 口径（唯一实现见 price_formula.go 的 ComputeTokenQuota，与上游进价共用）：
//
//	未命中输入 = promptTokens − cachedTokens（按 PromptPrice）
//	命中输入   = cachedTokens（按 CachePrice，未配置时回退 PromptPrice）
//	输出       = completionTokens（按 CompletionPrice）
func (p *ModelPrice) ComputeQuotaWithCache(promptTokens, completionTokens, cachedTokens int64) int64 {
	if p == nil {
		return 0
	}
	return ComputeTokenQuota(p.PromptPrice, p.CachePrice, p.CompletionPrice,
		promptTokens, completionTokens, cachedTokens)
}

// ComputePerCallQuota 按"次数"计算应扣额度（异步任务 / 图像视频类）。
//
// 公式：quota = perCallPrice × count；count <= 0 时按 1 次处理。
func (p *ModelPrice) ComputePerCallQuota(count int64) int64 {
	if p == nil {
		return 0
	}
	return ComputePerCallAmount(p.PerCallPrice, count)
}

// MatchModelPrice 从一组规则中挑出最适用的那一条。
//
// 优先级：精确匹配 → 前缀最长 → 全局通配；同类内若有多条（理论上被唯一索引拦住），
// 取 ID 最小的以保证结果稳定可复现。
//
// 返回 nil 表示没有任何规则适用（此时按"未定价"处理：不扣费但照常记录日志，
// 由站长自行决定是否为该模型补价格）。
func MatchModelPrice(prices []*ModelPrice, modelName string) *ModelPrice {
	candidates := make([]*ModelPrice, 0, len(prices))
	for _, price := range prices {
		if price == nil || !price.Enabled {
			continue
		}
		if !price.Matches(modelName) {
			continue
		}
		candidates = append(candidates, price)
	}
	return bestModelPrice(candidates)
}

// MatchModelPriceForChannel 在 MatchModelPrice 之上叠加"渠道专用价优先"。
//
// 适用场景：转发计费时已知本次实际命中的渠道 ID，需要在
// 「该渠道的专用价」与「分组默认价」之间取优先级。
//
// 优先级（顺序不可变）：
//  1. 渠道专用价（ChannelID == channelID 且匹配模型）优先；
//  2. 无渠道专用价时，回退到分组默认价（ChannelID == ChannelScopeAll）；
//  3. 其他渠道的专用价一律不参与本次匹配（那是给别的渠道用的）。
//
// channelID 为 ChannelScopeAll(0) 时退化为纯默认价匹配，与 MatchModelPrice 一致。
// 同类候选内仍按"精确 → 长前缀 → 全局通配"打分，取 ID 最小者保证可复现。
func MatchModelPriceForChannel(prices []*ModelPrice, modelName string, channelID uint64) *ModelPrice {
	if channelID == ChannelScopeAll {
		return MatchModelPrice(prices, modelName)
	}

	scoped := make([]*ModelPrice, 0, len(prices))
	fallback := make([]*ModelPrice, 0, len(prices))
	for _, price := range prices {
		if price == nil || !price.Enabled || !price.Matches(modelName) {
			continue
		}
		switch price.ChannelID {
		case channelID:
			scoped = append(scoped, price)
		case ChannelScopeAll:
			fallback = append(fallback, price)
		}
	}
	if best := bestModelPrice(scoped); best != nil {
		return best
	}
	return bestModelPrice(fallback)
}

// bestModelPrice 从"已确保启用且匹配模型"的候选里挑出最具体的一条。
//
// 抽取出来供 MatchModelPrice 与 MatchModelPriceForChannel 共用：
// 两处的"打分"口径必须完全相同，否则会出现"默认价与专用价择优不一致"的怪账。
func bestModelPrice(candidates []*ModelPrice) *ModelPrice {
	var best *ModelPrice
	bestScore := -1
	for _, price := range candidates {
		score := price.specificity()
		// 同分时取 ID 更小的，保证同一份数据每次匹配结果一致
		if best == nil || betterThan(price.ID, best.ID, score, bestScore) {
			best = price
			bestScore = score
		}
	}
	return best
}

// SortModelPrices 按"具体到笼统"的顺序排列规则，便于后台展示与人工核对。
func SortModelPrices(prices []*ModelPrice) {
	sort.SliceStable(prices, func(i, j int) bool {
		left, right := prices[i].specificity(), prices[j].specificity()
		if left != right {
			return left > right
		}
		return prices[i].ID < prices[j].ID
	})
}

// ModelPriceRepository 定义计价规则的持久化操作。
type ModelPriceRepository interface {
	// Create 新增规则，同分组同名冲突时返回 ErrModelPriceDuplicated。
	Create(ctx context.Context, price *ModelPrice) error

	// GetByID 按主键查询，不存在时返回 ErrModelPriceNotFound。
	GetByID(ctx context.Context, id uint64) (*ModelPrice, error)

	// List 查询规则；group 为空表示不过滤分组，仅启用的规则由 enabledOnly 控制。
	//
	// 注意：本方法只返回「分组默认价」（ChannelID == ChannelScopeAll），
	// 不返回渠道专用价——后台的价格广场/试算需要的是"分组层面的统一口径"，
	// 若把各渠道专用价混进来，会把某个渠道的折扣价误当成全组价格展示。
	// 转发计费请使用 ListForPricing。
	List(ctx context.Context, group string, enabledOnly bool) ([]*ModelPrice, error)

	// ListForPricing 返回该分组下「分组默认价 + 指定渠道的专用价」。
	//
	// 供转发计费读取价格使用（见 relay.Billing.priceForChannel）：
	// 返回集合里同时含 channel_id=0 的默认价与 channel_id=channelID 的专用价，
	// 由 MatchModelPriceForChannel 决定优先级。channelID 为 ChannelScopeAll 时
	// 结果与 List 相同（仅默认价）。
	ListForPricing(ctx context.Context, group string, channelID uint64, enabledOnly bool) ([]*ModelPrice, error)

	// Update 按 ID 更新，不存在时返回 ErrModelPriceNotFound。
	Update(ctx context.Context, price *ModelPrice) error

	// Delete 按 ID 删除，不存在时返回 ErrModelPriceNotFound。
	Delete(ctx context.Context, id uint64) error
}
