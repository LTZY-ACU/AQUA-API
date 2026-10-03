// 本文件实现「按用量计费」：查价、算钱、扣额度。
//
// 意图（Why）：
//
//	在 M2 之前，网关只记录 token 数却从不扣费——usage_logs.quota 恒为 0，
//	令牌与用户的 used_quota 永远是 0，于是"额度耗尽"这个状态永远触发不了，
//	额度字段形同虚设。本文件把计费真正接上。
//
//	为什么单独成组件而不是塞进 relay：
//	  1) 计费规则（价格匹配 + 换算口径）需要被多处复用（后台预估、报表核算）；
//	  2) 价格读取要缓存——每次转发都查一次库会显著放大数据库压力，
//	     而缓存失效策略属于计费语义，不该散落在转发代码里。
//
// 计费口径（唯一真相在 model.ModelPrice 的文件头）：
//
//	quota = ((promptTokens − cachedTokens) × promptPrice
//	         + cachedTokens × cachePrice
//	         + completionTokens × completionPrice) / 1_000_000
//
// 流转（Flow）：
//
//	转发完成 → relay.recordUsage（entry.Group 携带本次请求分组）
//	  └─ Billing.Charge(ctx, group, userID, tokenID, model, prompt, completion, cached)
//	       ├─ priceFor(group, model)   按分组带缓存的价格匹配
//	       ├─ ratioFor(group)          按分组带缓存的倍率
//	       ├─ ComputeQuotaWithCache    换算额度（缓存命中部分单独计价）
//	       ├─ tokens.ConsumeQuota      扣令牌额度（原子）
//	       └─ users.AddUsedQuota       累加用户已用额度（原子）
//
//	放行前（鉴权阶段，见交付说明）→ Billing.BudgetExceeded(ctx, tokenID)
//	  └─ model.Token.EvaluateBudget → 需要翻篇时 tokens.ResetBudgetWindow（惰性重置）
//	  └─ 供后台巡检 → Billing.GroupSpendToday(ctx, group)（只读，不参与扣费）
//
// 扩展（Extend）：
//
//	新增计价维度（缓存命中价、按次计费、图片张数）时：
//	  在 Charge 里扩展入参并同步 model.ModelPrice 的计算函数与迁移脚本。
//	新增缓存维度时：务必保持"按分组隔离"这一前提——缓存键必须能区分分组，
//	  否则不同分组会互相串价（见 Billing.rules 上的说明）。
//	新增"按用户/按分组"的周期预算时：复用 model.Token 的窗口判定思路
//	  （相对窗口 + 惰性重置 + 基线差），并把闸门判定放到鉴权放行前调用。
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/reqctx"
)

// priceCacheTTL 是价格缓存的存活时间。
//
// 取 30 秒的权衡：
//   - 太长：管理员改价后要等很久才生效，容易被当成"改了没用"；
//   - 太短：每次转发都要查库，失去缓存意义。
//     30 秒在"改价即时感"与"数据库压力"之间比较平衡；
//     后台改价时还会主动清缓存（见 Invalidate），所以实际感知是即时的。
const priceCacheTTL = 30 * time.Second

// defaultBillingGroup 是未指定分组时使用的分组名。
const defaultBillingGroup = "default"

// ratioScale 是分组倍率的换算基数（百分比：100 = 1.0 倍）。
//
// 与 model.ModelGroup 的口径一致；单独在此定义是为了让计费公式里的
// "除以 100"这件事有一个可检索的名字，而不是散落的魔法数字。
const ratioScale int64 = 100

// estimateBytesPerToken 是由请求体字节数估算 prompt token 数的换算系数。
//
// 取 3 的理由（保守上界）：UTF-8 下中文约 3 字节/token、英文约 4 字节/token，
// 用 3 能在中文场景贴近真实值、在英文场景略微高估。预留宁可略高、不可过小——
// 过小会让额度墙失去意义，过大只是暂时多占用一点额度（结算时会退还差额）。
const estimateBytesPerToken = 3

// Billing 负责按用量计费，并发安全。
type Billing struct {
	prices model.ModelPriceRepository
	// groups 用于读取各分组的计费倍率；可为 nil（此时倍率恒为 100）。
	groups model.ModelGroupRepository
	tokens model.TokenRepository
	users  model.UserRepository
	// group 是默认分组：调用方传入空分组时使用它（见 resolveGroup）。
	group string

	// quota 是额度预留台账（可选）。
	//
	// 为 nil 时退化为"响应后扣费"（旧行为）：便于单元测试与"仅统计不限制"的部署形态。
	// 非 nil 时启用"请求前预扣 + 响应后结算"。
	quota model.QuotaRepository

	// rules 是【按分组隔离】的价格与倍率缓存：key 为分组名。
	//
	// 关键（改动前务必理解）：缓存必须按分组隔离。旧实现只存一份，
	// 免费分组请求会把自营分组的价格灌进缓存，随后自营分组读到免费价（或反之），
	// 直接造成错账 —— 这是本次改动最容易写错、后果最严重的地方。
	//
	// 同一分组内，价格与倍率放在同一份缓存里一起刷新（而不是各刷各的），
	// 以保证二者来自同一时刻的配置，避免"新价格 × 旧倍率"的中间态。
	mu    sync.RWMutex
	rules map[string]*cachedRules

	// free 判定"该用户调用该模型是否免计费"（语料共建的特殊福利账户）。
	//
	// 为 nil 时行为与引入本功能前完全一致。
	//
	// 为什么需要它、以及为什么只在 Charge/ChargeOnce 里判：
	//   Quote 的签名里没有 userID，"按人免费"在那里无从判定；
	//   但豁免请求在鉴权阶段就不会创建预留（见 middleware.TokenAuth），
	//   结算时会走"未预留 → Charge"这条路，因此 Charge 里判一道即可覆盖全链路。
	free FreeChecker

	// retryCounters 是「分组 → [上游调用次数, 计费请求次数]」的滑动计数，
	// 用于在【折扣分组】上估算真实重试率 r = 上游调用次数 / 计费请求次数。
	//
	// 为什么只统计折扣分组：全价分组的毛利本就厚，重试多吃一点也亏不到本金；
	// 真正会亏的是代理档（如 6 折），它的净利直接等于"多出来的那几次上游调用"。
	// 因此只对 ratio < 100 的分组计数，其余分组的锁竞争与内存开销不值得。
	//
	// 只存累计计数而不存时间窗：r 是"长期均值"而非瞬时值，
	// 短时抖动不该触发告警，累计到足够样本才有判断意义（见告警阈值）。
	retryMu       sync.Mutex
	retryCounters map[string]*[2]int64
}

// FreeChecker 是"按用户 + 模型判断是否免计费"的最小接口。
//
// 刻意定义在消费方（本包）而不是生产方：*corpus.Guard 天然满足它，
// 本包无需依赖语料包的其余能力；测试里也能塞一个三行的假实现。
type FreeChecker interface {
	IsFree(userID uint64, model string) bool
}

// cachedRules 是「某一个分组」的价格规则与倍率的快照。
//
// 按分组各存一份，读时互不干扰：分组 A 的缓存内容绝不会被分组 B 的请求覆盖。
type cachedRules struct {
	prices   []*model.ModelPrice
	ratio    int64
	cachedAt time.Time
}

// NewBilling 创建计费组件。
//
// group 为空时使用 default；groups / tokens / users 均允许为 nil
// （groups 为 nil 时倍率恒为 1.0；tokens/users 为 nil 时只计算不扣减，
// 便于单元测试与"仅统计不限制"的部署形态）。
func NewBilling(prices model.ModelPriceRepository, groups model.ModelGroupRepository,
	tokens model.TokenRepository, users model.UserRepository, group string) *Billing {
	if group == "" {
		group = defaultBillingGroup
	}
	return &Billing{
		prices: prices,
		groups: groups,
		tokens: tokens,
		users:  users,
		group:  group,
		rules:  make(map[string]*cachedRules),
	}
}

// WithQuotaRepository 注入额度预留台账，启用"请求前预扣 + 响应后结算"。
//
// 返回 b 本身以便链式装配（main 中一次性构造）。
// 不注入（或注入 nil）时保持旧行为：只在响应之后按实际用量扣费。
func (b *Billing) WithQuotaRepository(quota model.QuotaRepository) *Billing {
	if b != nil {
		b.quota = quota
	}
	return b
}

// WithFreeChecker 注入"特殊福利账户"的免计费判定（见 FreeChecker 的说明）。
//
// 返回 b 本身以便链式装配；不注入时全部用户照常计费。
func (b *Billing) WithFreeChecker(free FreeChecker) *Billing {
	if b != nil {
		b.free = free
	}
	return b
}

// IsFreeForUser 判断该用户在该模型上是否享有免计费福利（语料共建的特殊福利账户）。
//
// 为什么导出：鉴权中间件要用它把"这次调用不算钱"提前到额度墙之前，
// 但它又不该为此多一个构造参数（那会牵动全部调用点与测试）。
// 于是中间件对预留器做一次**可选接口断言**来取这项能力——
// 断言不到就等于"本站没有福利账户"，行为与引入本功能前完全一致。
func (b *Billing) IsFreeForUser(userID uint64, modelName string) bool {
	return b != nil && b.free != nil && b.free.IsFree(userID, modelName)
}

// DefaultGroup 返回"请求未指定分组时"本组件使用的分组。
//
// 存在意义：路由侧（relay.Options.Group）与计费侧各有一个默认分组，
// 两者必须相等，否则不带分组的令牌会"按 A 组选渠道、按 B 组查价格"——
// 而查不到价格的模型会被判为【不计费】，表现为"收费模型被免费调用"，
// 站长在账单上完全看不出异常。relay.New 会据此做一致性校验并告警。
func (b *Billing) DefaultGroup() string {
	if b == nil {
		return ""
	}
	return b.group
}

// SetDefaultGroup 覆盖"请求未指定分组时"使用的分组。
//
// 只应由装配代码（relay.New）在启动阶段调用一次：它存在的意义是保证计费的
// 默认分组与路由的默认分组一致（见 relay.New 的一致性对齐），运行期不应改动。
// 调用发生在任何请求之前，因此这里不加锁；缓存按分组隔离，
// 改这个值也不会污染已缓存的其他分组。
func (b *Billing) SetDefaultGroup(group string) {
	if b == nil || strings.TrimSpace(group) == "" {
		return
	}
	b.group = group
}

// Invalidate 清空【全部分组】的价格与倍率缓存。
//
// 调用时机：后台新增/修改/删除计价规则或分组倍率之后。
// 不清理的话，管理员改完价格会看到"新价格要等半分钟才生效"，容易误判为没生效。
//
// 直接丢弃整张缓存表（而非逐组标记过期）：实现简单且不会遗漏任何分组，
// 代价只是"改价后第一次请求各分组会各查一次库"，可以忽略。
func (b *Billing) Invalidate() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.rules = make(map[string]*cachedRules)
	b.mu.Unlock()
}

// resolveGroup 把"调用方传入的分组"归一化；空字符串表示使用 Billing 的默认分组。
//
// 之所以约定"传空 = 用默认分组"：新增分组参数后，任何尚未补上分组的调用点
// 都会落到默认分组（与改动前行为一致），而不会落到一个不存在的分组导致计费错乱。
func (b *Billing) resolveGroup(group string) string {
	if g := strings.TrimSpace(group); g != "" {
		return g
	}
	return b.group
}

// rulesFor 返回指定分组（+可选渠道）的最新价格与倍率缓存；缓存过期时重新加载。
//
// 注意 group 已按 resolveGroup 归一化，因此缓存键必然是"真实分组名"，
// 不会出现 "a" 与 " a" 被当成两个分组各缓存一份的浪费。
//
// channelID 参与缓存键：渠道专用价只对特定渠道生效，若只按分组缓存，
// 渠道 A 的专用价会被渠道 B 的请求读到（串价）。channelID 为 ChannelScopeAll
// 时退化为纯分组缓存，与引入渠道专用价之前完全一致。
func (b *Billing) rulesFor(ctx context.Context, group string, channelID uint64) *cachedRules {
	group = b.resolveGroup(group)
	cacheKey := rulesCacheKey(group, channelID)

	b.mu.RLock()
	cached := b.rules[cacheKey]
	fresh := cached != nil && time.Since(cached.cachedAt) < priceCacheTTL
	b.mu.RUnlock()

	if fresh {
		return cached
	}
	return b.refresh(ctx, group, channelID, cached)
}

// rulesCacheKey 生成价格缓存的键。
//
// 渠道 0（不限渠道）直接用分组名做键，保证"普通分组"的缓存行为与旧实现逐字一致；
// 渠道专用价用 "分组\x00渠道ID" 做键，用 NUL 分隔避免与分组名拼接歧义。
func rulesCacheKey(group string, channelID uint64) string {
	if channelID == model.ChannelScopeAll {
		return group
	}
	return group + "\x00" + strconv.FormatUint(channelID, 10)
}

// refresh 重新加载【指定分组 + 渠道】的价格规则与倍率，并写回对应的缓存槽。
//
// 容错（保持旧实现语义）：读库失败时不覆盖旧值——宁可短时间沿用旧价，
// 也不要因为一次读库失败就把该分组的调用变成"未定价"（不扣费），那等于白送。
// 由于缓存按分组（+渠道）隔离，某个分组的读库失败只影响它自己，不会污染其他分组。
func (b *Billing) refresh(ctx context.Context, group string, channelID uint64, old *cachedRules) *cachedRules {
	rules := &cachedRules{ratio: ratioScale}
	if old != nil {
		// 先继承旧快照：读库失败时据此沿用，而不是清成空。
		rules.prices = old.prices
		rules.ratio = old.ratio
	}

	// coldPriceFailed：既没有旧快照可沿用、这次又没读到价。
	// 此时 rules.prices 为 nil = "未定价" = 不收费——若照常写进缓存，
	// 等于把"白送"锁死 30 秒（priceCacheTTL），且注释承诺的保护并不覆盖这条路径。
	coldPriceFailed := false
	if b.prices != nil {
		// ListForPricing 返回「分组默认价 + 本渠道专用价」，由匹配函数决定优先级。
		prices, err := b.prices.ListForPricing(ctx, group, channelID, true)
		if err != nil {
			if old == nil {
				coldPriceFailed = true
				slog.Error("读取计价规则失败且无旧缓存可沿用：本次按未定价处理且【不写缓存】",
					"error", err, "group", group)
			} else {
				slog.Warn("读取计价规则失败，本次沿用旧缓存", "error", err, "group", group)
			}
		} else {
			rules.prices = prices
		}
	}

	ratio := ratioScale
	if b.groups != nil {
		got, err := b.groups.GetByName(ctx, group)
		switch {
		case err == nil:
			if got.Ratio > 0 {
				ratio = got.Ratio
			}
		case errors.Is(err, model.ErrModelGroupNotFound):
			// 分组不存在（历史数据里的分组名从未登记，或令牌指定的分组已被删除）：
			// 按 1.0 倍处理，与本次升级前的行为一致，不产生意外扣费；并留下告警，
			// 让站长能发现"有请求打到了一个不存在的分组"。
			slog.Warn("计费分组不存在，本次按默认倍率计费", "group", group)
			ratio = ratioScale
		default:
			// 读库失败：保留旧倍率，避免"一次故障把倍率变成 1.0"导致少收钱
			slog.Warn("读取分组倍率失败，本次沿用旧倍率", "error", err, "group", group)
			ratio = rules.ratio
			if ratio <= 0 {
				ratio = ratioScale
			}
		}
	}
	rules.ratio = ratio
	rules.cachedAt = time.Now()

	if coldPriceFailed {
		// 不写缓存：下一个请求会立刻重试读库，故障恢复即恢复计费。
		// 代价只有"本次这一笔按未定价放行"——宁可少收一笔，也不能锁 30 秒白送。
		return rules
	}

	b.mu.Lock()
	if b.rules == nil {
		b.rules = make(map[string]*cachedRules)
	}
	b.rules[rulesCacheKey(group, channelID)] = rules
	b.mu.Unlock()

	return rules
}

// ratioFor 返回指定分组的计费倍率（百分比）；空分组用 Billing 的默认分组。
//
// 倍率是"分组级"属性，与具体渠道无关，因此这里固定按 ChannelScopeAll 取缓存。
func (b *Billing) ratioFor(ctx context.Context, group string) int64 {
	ratio := b.rulesFor(ctx, group, model.ChannelScopeAll).ratio
	if ratio <= 0 {
		return ratioScale
	}
	return ratio
}

// priceFor 返回适用于该模型的最优计价规则（不含渠道专用价）；无匹配时返回 nil。
//
// 参数 group 为空表示使用 Billing 的默认分组（见 resolveGroup）。
// 转发计费请用 priceForChannel，它会把"本次实际命中的渠道"纳入取值优先级。
func (b *Billing) priceFor(ctx context.Context, group, modelName string) *model.ModelPrice {
	return b.priceForChannel(ctx, group, modelName, model.ChannelScopeAll)
}

// priceForChannel 返回"该渠道 + 该分组"下适用于该模型的最优计价规则。
//
// 取值优先级（见 model.MatchModelPriceForChannel）：
//  1. 渠道专用价（ChannelID == channelID）优先；
//  2. 无专用价时回退分组默认价（ChannelID == ChannelScopeAll）。
//
// 缓存实现：按【分组 + 渠道】整表缓存（仅启用），过期后重新加载该键。
// 选择"整表"而不是"按模型缓存"，是因为规则数极少而模型名组合无限，
// 按模型缓存反而会让缓存无限膨胀。
func (b *Billing) priceForChannel(ctx context.Context, group, modelName string, channelID uint64) *model.ModelPrice {
	if b == nil || b.prices == nil {
		return nil
	}
	return model.MatchModelPriceForChannel(b.rulesFor(ctx, group, channelID).prices, modelName, channelID)
}

// PriceInfo 返回某模型在某分组下命中的计价规则（供后台展示与试算使用）。
//
// 返回 nil 表示【未定价】（该模型没有任何适用规则），调用方应把它与
// "命中规则但显式免费"区分显示：前者是待办，后者是站长的明确决定。
//
// 导出它的原因：计费链路与后台展示必须用同一份规则匹配结果，
// 后台若自行遍历价格表，就可能出现"试算说免费、实际在扣费"的不一致。
//
// 本方法按分组默认价（不含渠道专用价）匹配，供"分组层面"的展示使用；
// 需要看到某渠道专用价时用 PriceInfoForChannel。
func (b *Billing) PriceInfo(ctx context.Context, group, modelName string) *model.ModelPrice {
	return b.priceFor(ctx, group, modelName)
}

// PriceInfoForChannel 与 PriceInfo 同源，但把"渠道专用价优先"纳入匹配。
func (b *Billing) PriceInfoForChannel(ctx context.Context, group, modelName string, channelID uint64) *model.ModelPrice {
	return b.priceForChannel(ctx, group, modelName, channelID)
}

// PriceVersionForChannel 返回本次调用命中的计价规则版本标识（供账目快照）。
//
// 与 ChargeForChannel / QuoteForChannel 取价同源（渠道专用价优先、分组默认价兜底），
// 保证"账目里记的版本"就是"实际用来扣费的那条规则"，改价后旧账仍可按旧价复算。
// 未定价或未注入价格仓储时返回空串（日志里的版本列留空，不影响其它字段）。
func (b *Billing) PriceVersionForChannel(ctx context.Context, group, modelName string, channelID uint64) string {
	if b == nil {
		return ""
	}
	price := b.priceForChannel(ctx, group, modelName, channelID)
	if price == nil {
		return ""
	}
	return model.PriceSnapshotVersion(price.ID, price.UpdatedAt)
}

// Quote 计算该次用量应扣的额度（只算不扣）。
//
// cachedTokens 是输入中命中上游缓存的部分（0 表示上游未提供该维度）：
// 这部分按规则的 CachePrice 计费，未配置时自动回退输入价，因此老配置账目不变。
//
// 参数 group 为空表示使用 Billing 的默认分组；扣费请用 Charge。
//
// 本方法按分组默认价计价（不含渠道专用价）。转发链路已知实际渠道时，
// 应改用 QuoteForChannel 以享受"渠道专用价优先"的取值。
func (b *Billing) Quote(ctx context.Context, group, modelName string,
	promptTokens, completionTokens, cachedTokens int64) int64 {
	return b.QuoteForChannel(ctx, group, modelName,
		promptTokens, completionTokens, cachedTokens, model.ChannelScopeAll)
}

// QuoteForChannel 与 Quote 同口径，但按"该渠道专用价优先、默认价兜底"取价。
//
// channelID 为 0（未指定渠道）时退化为 Quote 的行为。
func (b *Billing) QuoteForChannel(ctx context.Context, group, modelName string,
	promptTokens, completionTokens, cachedTokens int64, channelID uint64) int64 {
	price := b.priceForChannel(ctx, group, modelName, channelID)
	if price == nil || price.IsFree() {
		// 两种情况都算 0，但语义不同：
		//   未定价（nil）= 站长还没给这个模型定价，日志里 quota 记 0 属于"待办"；
		//   显式免费     = 站长明确选择不收费（活动模型 / 公益分组）。
		// 二者的区分体现在展示层（广场标"免费"）与后台（价格页显示"免费"）。
		return 0
	}
	return applyRatio(
		chargeBaseForCall(price, promptTokens, completionTokens, cachedTokens),
		b.ratioFor(ctx, group))
}

// chargeBaseForCall 按价格规则的有效计费方式，算出"一次同步调用"的基础额度。
//
// 为什么同步链路也必须识别按次（重要）：
//
//	按次模型的三个 token 单价必然全为 0（价格只填在 PerCallPrice 上），
//	若仍按 token 公式计算必然得到 0 —— 表现为"该模型调用全程免费"（漏扣）。
//	因此这里与 EstimateReserve 保持同一口径：按次 → 按"一次"计。
//
// 口径对齐说明：EstimateReserve 在按 token 算出 0 时会退化为 ComputePerCallQuota(1)，
// Quote 与 Charge 必须与之对齐，否则会出现"按次预留了、结算时却退成 0"的错账。
func chargeBaseForCall(price *model.ModelPrice, promptTokens, completionTokens, cachedTokens int64) int64 {
	if price.EffectiveBillingMode() == model.BillingModePerCall {
		// 同步链路一次请求固定按 1 次计；异步任务的多份数走 QuoteOnce/ChargeOnce。
		return price.ComputePerCallQuota(1)
	}
	return price.ComputeQuotaWithCache(promptTokens, completionTokens, cachedTokens)
}

// QuoteOnce 计算"调用一次该模型"应扣的额度（只算不扣）。
//
// 用途：异步任务在【提交时】就要扣费（见 model.Task 的文件头说明），
// 因此任务链路需要一个与 token 无关的计价入口。
// count 为本次生成的份数（如一次画 4 张图），<=0 时按 1 次处理。
// 参数 group 为空表示使用 Billing 的默认分组。
func (b *Billing) QuoteOnce(ctx context.Context, group, modelName string, count int64) int64 {
	price := b.priceFor(ctx, group, modelName)
	if price == nil || price.IsFree() {
		return 0
	}
	return applyRatio(price.ComputePerCallQuota(count), b.ratioFor(ctx, group))
}

// applyRatio 按倍率换算额度（向下取整）。
//
// 倍率为 100（即 1.0 倍）时数值完全不变，因此未配置分组的部署不受任何影响。
func applyRatio(base, ratio int64) int64 {
	if base <= 0 || ratio <= 0 || ratio == ratioScale {
		return base
	}
	return base * ratio / ratioScale
}

// ChargeOnce 按次计费并扣减额度，返回实际扣减的额度。
//
// 与 Charge 的关系：口径不同（按次 vs 按 token），扣减目标与容错策略完全一致。
// 参数 group 为空表示使用 Billing 的默认分组。
func (b *Billing) ChargeOnce(ctx context.Context, group string, userID, tokenID uint64, modelName string, count int64) int64 {
	if b == nil {
		return 0
	}

	price := b.priceFor(ctx, group, modelName)
	if price == nil {
		// 未定价：不扣费（与 token 计费保持同一语义，避免"没配价格就报错"）
		return 0
	}
	if price.IsFree() {
		// 显式免费：同样不扣费。写成独立的判断而不是并进上一个条件，
		// 是为了让"免费"这条业务规则在代码里显式可查（将来加"免费额度上限"时改这里）。
		return 0
	}
	// 语料共建的福利账户：在指定模型上免计费。
	// 放在价格判定之后：它只"免除收费"，不会把"本来就不收费"变成别的语义。
	if b.IsFreeForUser(userID, modelName) {
		return 0
	}

	quota := applyRatio(price.ComputePerCallQuota(count), b.ratioFor(ctx, group))
	if quota <= 0 {
		return 0
	}

	b.applyDelta(ctx, userID, tokenID, quota, "扣减")
	return quota
}

// Refund 退还额度（异步任务失败/取消时调用）。
//
// 为什么必须支持退还：任务在提交时就已扣费，若任务最终失败却不退，
// 用户会为"没有拿到结果"的调用付费——这是最容易被投诉的计费缺陷。
//
// 幂等性说明：本方法自身不做幂等保护，由调用方保证"每个任务最多退一次"
// （store 层的 Finish 通过 status NOT IN (终态) 条件天然实现了这一点）。
func (b *Billing) Refund(ctx context.Context, userID, tokenID uint64, amount int64) {
	if b == nil || amount <= 0 {
		return
	}
	b.applyDelta(ctx, userID, tokenID, -amount, "退还")
}

// applyDelta 对令牌与用户额度施加同一个增量（正数为扣减、负数为退还）。
//
// 抽出来的理由：扣减与退还的目标、容错策略完全相同，
// 各写一遍必然有一天会出现"退还时漏掉用户额度"的不一致。
//
// 失败按错误级记录：额度写失败意味着账实不符（usage_log 里已经记了这笔 quota，
// 但 users/tokens 的计数没动），与结算失败同属"必须被发现的资损风险"，
// 不能用 Warn 混在普通告警里被淹没。
func (b *Billing) applyDelta(ctx context.Context, userID, tokenID uint64, delta int64, action string) {
	now := time.Now()
	if tokenID > 0 && b.tokens != nil {
		if err := b.tokens.ConsumeQuota(ctx, tokenID, delta, now); err != nil {
			slog.Error(action+"令牌额度失败（账实不符，需人工核查）",
				"error", err, "token_id", tokenID, "delta", delta)
		}
	}
	if userID > 0 && b.users != nil {
		if err := b.users.AddUsedQuota(ctx, userID, delta); err != nil {
			slog.Error(action+"用户已用额度失败（账实不符，需人工核查）",
				"error", err, "user_id", userID, "delta", delta)
		}
	}
}

// ── 折扣分组的重试率告警 ──────────────────────────────────────────────────

// 告警阈值常量：与 docs/17 的定价假设一一对应。
const (
	// retryWarnRatio 判定"该分组是折扣档"的边界：倍率低于它才监控。
	// 100 = 全价，100 以下（如 60 = 6 折）才会被"重试成本"压到接近亏本。
	retryWarnRatio = 100
	// retryWarnMinSamples 触发告警所需的最小计费请求数：样本太少时 r 没有意义。
	// 取 50 是因为 6 折档下即便 50 次请求也能把 r 的抖动控制在可判读范围。
	retryWarnMinSamples = 50
	// retryWarnThreshold 告警线（r × 100）：1.37 ≈ 6 折档的保本重试率
	// （净利率 0.94 × 1.37 ≈ 1.28，略低于上游成本系数 1.3，留 0.03 提前量）。
	// 超过即意味着"这一档正在亏本或即将亏本"。
	retryWarnThreshold = 137
)

// recordUpstreamCall 在每次真实上游调用后累加计数（含重试），用于估算折扣分组的重试率。
//
// 调用方是转发链路（openai.go 的 forwardChat），它在每次真实打到上游后调用一次。
// 参数 charged 表示"本次调用是否产生了对用户计费"——同一请求的重试多次调用只算一次计费，
// 因此 r = 上游调用次数 / 计费请求次数 才是真实成本放大率。
func (b *Billing) recordUpstreamCall(group string, charged bool) {
	if b == nil {
		return
	}
	group = b.resolveGroup(group)
	// 只统计折扣分组：全价分组的毛利足够厚，重试成本不值得监控（也省锁）。
	if b.ratioFor(context.Background(), group) >= retryWarnRatio {
		return
	}
	b.retryMu.Lock()
	if b.retryCounters == nil {
		b.retryCounters = make(map[string]*[2]int64)
	}
	c := b.retryCounters[group]
	if c == nil {
		c = &[2]int64{}
		b.retryCounters[group] = c
	}
	c[0]++ // 上游调用次数
	if charged {
		c[1]++ // 计费请求次数
	}
	ratio := b.ratioFor(context.Background(), group)
	r := float64(c[0]) / float64(c[1])
	if c[1] >= retryWarnMinSamples && ratio > 0 && int64(r*100) > retryWarnThreshold {
		slog.Error("折扣分组重试率已越过保本线，正在亏本：请降低该渠道 max_attempts 或上调价格",
			"group", group, "upstream_calls", c[0], "charged_requests", c[1],
			"retry_ratio", fmt.Sprintf("%.3f", r), "discount_ratio", ratio)
	}
	b.retryMu.Unlock()
}

// RetryRatioSnapshot 是「折扣分组的重试率」读数，供后台运维面板展示。
//
// 把 r 从"只在日志里出现"变成"界面上可见"：资损告警若只写日志，
// 站长不上服务器就永远看不到——这与"面板可达"原则直接冲突，因此必须能查。
type RetryRatioSnapshot struct {
	// Group 是分组名。
	Group string
	// Ratio 是该分组的计费倍率（百分比，<100 即折扣档）。
	Ratio int64
	// UpstreamCalls 是累计上游调用次数（含重试）。
	UpstreamCalls int64
	// ChargedRequests 是累计产生了计费的请求数。
	ChargedRequests int64
	// RetryRatio 是 r = 上游调用次数 / 计费请求次数（无计费请求时为 0）。
	RetryRatio float64
	// OverBreakEven 表示 r 已越过该折扣档的保本线（正在亏本）。
	OverBreakEven bool
}

// RetryRatioSnapshots 返回全部折扣分组的重试率快照（按分组名排序，供面板展示）。
//
// 读的是 recordUpstreamCall 维护的内存计数；进程重启后从零累计——
// 这是刻意取舍：r 是"当前运行期的健康度"，历史均值见调用日志，两者不混。
func (b *Billing) RetryRatioSnapshots() []RetryRatioSnapshot {
	if b == nil {
		return nil
	}
	b.retryMu.Lock()
	defer b.retryMu.Unlock()
	if len(b.retryCounters) == 0 {
		return nil
	}
	out := make([]RetryRatioSnapshot, 0, len(b.retryCounters))
	for group, c := range b.retryCounters {
		ratio := b.ratioFor(context.Background(), group)
		var r float64
		if c[1] > 0 {
			r = float64(c[0]) / float64(c[1])
		}
		out = append(out, RetryRatioSnapshot{
			Group:           group,
			Ratio:           ratio,
			UpstreamCalls:   c[0],
			ChargedRequests: c[1],
			RetryRatio:      r,
			OverBreakEven:   c[1] >= retryWarnMinSamples && ratio > 0 && int64(r*100) > retryWarnThreshold,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

// Charge 按用量计费并扣减额度，返回实际扣减的额度。
//
// 安全约束（重要）：本方法绝不能影响客户端响应。
// 它通常在响应已完整回传之后执行，因此内部对错误只记录、不向上返回——
// 用户不该因为"记账失败"而收到一个报错。
//
// 扣减范围：
//   - 令牌额度（remain_quota / used_quota）；
//   - 用户额度（used_quota）。
//
// 两者都扣的原因：令牌是"发给某个用户的凭据"，用户额度是账号级上限。
// 只扣令牌会让用户通过"多建几个令牌"绕过总量限制。
//
// 参数 group 为空表示使用 Billing 的默认分组。
//
// 本方法按分组默认价计价（不含渠道专用价）。转发链路已知实际渠道时，
// 应改用 ChargeForChannel 以享受"渠道专用价优先"的取值。
func (b *Billing) Charge(ctx context.Context, group string, userID, tokenID uint64, modelName string,
	promptTokens, completionTokens, cachedTokens int64) int64 {
	return b.ChargeForChannel(ctx, group, userID, tokenID, modelName,
		promptTokens, completionTokens, cachedTokens, model.ChannelScopeAll)
}

// ChargeForChannel 与 Charge 完全同口径，但按"该渠道专用价优先、默认价兜底"取价。
//
// 为什么单开一个方法而不改 Charge 的签名：Charge 被鉴权/结算等既有调用点使用，
// 改签名会牵动所有调用点；新增方法则让"有渠道上下文"的转发链路按需选择，
// 其余调用点行为逐字不变（channelID 为 0 时二者等价）。
func (b *Billing) ChargeForChannel(ctx context.Context, group string, userID, tokenID uint64, modelName string,
	promptTokens, completionTokens, cachedTokens int64, channelID uint64) int64 {
	if b == nil {
		return 0
	}

	price := b.priceForChannel(ctx, group, modelName, channelID)
	if price == nil {
		// 未定价：不扣费但照常记录日志（quota=0）。
		// 这样站长能从日志看出"哪些模型还没定价"，而不是被静默拦住。
		return 0
	}
	if price.IsFree() {
		// 显式免费：价格字段即使填了也不生效——免费是站长的明确决定，
		// 不能被"顺手填过的价格"覆盖，否则会出现"选了免费还在扣费"的投诉。
		return 0
	}
	// 语料共建的福利账户：在指定模型上免计费（与 ChargeOnce 同一口径）。
	if b.IsFreeForUser(userID, modelName) {
		return 0
	}

	quota := applyRatio(
		chargeBaseForCall(price, promptTokens, completionTokens, cachedTokens),
		b.ratioFor(ctx, group))
	if quota <= 0 {
		return 0
	}

	b.applyDelta(ctx, userID, tokenID, quota, "扣减")
	return quota
}

// ---------------------------------------------------------------------------
// 额度预扣 / 结算 / 退还
// ---------------------------------------------------------------------------
//
// 为什么把这三件事放在计费组件上（而不是让中间件直接操作仓储）：
//   - "该估多少、该怎么收"属于计费语义，只有计费组件知道价格与倍率；
//   - 中间件只需表达"我要预留多少、什么时候结算"，无需理解计价规则。
//
// 三个方法都对 b == nil / 未注入台账 保持零值安全，便于测试与降级部署。

// EstimateReserve 估算一次调用应预留的额度。
//
// 返回 priced=false 表示该模型【未命中任何计价规则】（调用不计费），
// 此时鉴权层必须跳过预留——否则免费模型会被额度墙挡住（这正是线上事故的根因）。
//
// 估算口径（重要：估算只影响"预留"，最终一律以结算为准）：
//
//	按请求体字节数保守估算 prompt token（见 estimateBytesPerToken），
//	并假设输出与输入同量级；对"按次定价"的模型退化为按一次计。
//
// 因此它可能高于真实用量（结算时会把差额退还），也可能低于真实用量
// （结算时补扣），但绝不会把有价格的调用算成免费。
//
// 参数 group 为空表示使用 Billing 的默认分组；预留与最终结算必须用同一分组，
// 否则会出现"按 A 分组预扣、按 B 分组结算"的错账。
func (b *Billing) EstimateReserve(ctx context.Context, group, modelName string, promptBytes int) (int64, bool) {
	if b == nil {
		return 0, false
	}

	price := b.priceFor(ctx, group, modelName)
	if price == nil {
		return 0, false
	}
	if price.IsFree() {
		// 显式免费的模型必须【跳过预留】：否则免费模型会被额度墙挡住，
		// 而"免费却不能用"正是历史上那次线上事故的表现形式。
		return 0, false
	}

	promptTokens := estimatePromptTokens(promptBytes)
	base := price.ComputeQuota(promptTokens, promptTokens)
	if base <= 0 {
		// 未配 token 单价但配了按次单价的模型：退化为按一次预留。
		base = price.ComputePerCallQuota(1)
	}
	amount := applyRatio(base, b.ratioFor(ctx, group))
	if amount <= 0 {
		// 命中计价规则但算得极小（如极低单价）：至少预留 1，让额度墙真正生效。
		amount = 1
	}
	return amount, true
}

// Reserve 预扣额度（幂等）。
//
// 未注入台账时返回 (nil, nil)：表示"不做预留"，调用方应退化为响应后扣费。
func (b *Billing) Reserve(ctx context.Context, req model.ReserveRequest) (*model.QuotaReservation, error) {
	if b == nil || b.quota == nil {
		return nil, nil
	}
	return b.quota.Reserve(ctx, req)
}

// Settle 结算第 requestID 号预留（幂等）：按实际用量多退少补。
//
// actualQuota 为 model.QuotaUnknown 时按预留量收取。
// 返回的预留记录中 Settled 是真实入账额度，调用方据此识别"补扣受限"的缺口。
//
// 成功后把 ctx 中的预留闭环标记置位（reqctx.ReservationGuard）：
// 鉴权中间件据此知道"这笔预留已有人负责"，从而跳过它的兜底退还。
// 失败时不置位——中间件会再试一次 Release（幂等），失败则等 TTL 回收。
func (b *Billing) Settle(ctx context.Context, requestID string, actualQuota int64) (*model.QuotaReservation, error) {
	if b == nil || b.quota == nil {
		return nil, nil
	}
	res, err := b.quota.Settle(ctx, requestID, actualQuota)
	if err == nil {
		if guard, ok := reqctx.ReservationGuardFrom(ctx); ok {
			guard.MarkClosed()
		}
	}
	return res, err
}

// Release 全额退还第 requestID 号预留（幂等，请求失败时调用）。
//
// 与 Settle 一样在成功后置位闭环标记：异步任务在 Submit 开头即退还预留，
// 置位后中间件不会在 handler 返回时再退还一次（多一次无谓的写库）。
func (b *Billing) Release(ctx context.Context, requestID string) error {
	if b == nil || b.quota == nil {
		return nil
	}
	err := b.quota.Release(ctx, requestID)
	if err == nil {
		if guard, ok := reqctx.ReservationGuardFrom(ctx); ok {
			guard.MarkClosed()
		}
	}
	return err
}

// PendingReserved 返回某用户在途预留的合计额度。
//
// 只用于"额度耗尽"类错误文案的诊断数字，不参与可用额度计算
// （在途预留已计入 used_quota，再减一遍会重复扣减）。
func (b *Billing) PendingReserved(ctx context.Context, userID uint64) (int64, error) {
	if b == nil || b.quota == nil {
		return 0, nil
	}
	return b.quota.PendingAmount(ctx, userID)
}

// ---------------------------------------------------------------------------
// 周期预算闸门（令牌级）
// ---------------------------------------------------------------------------
//
// 与"预扣/结算"的分工（重要）：
//   - 预算是否超限必须【在放行之前】就知道，否则请求已经打到上游再后悔没有意义；
//     因此它被做成可提前查询的 BudgetExceeded，由鉴权层在放行前调用。
//   - Charge 系列"不能影响客户端响应"（错误只记录不返回），因此本方法【不在
//     Charge 内部做拦截】，只负责判定与惰性重置窗口；扣费记账仍由 Charge/ConsumeQuota 完成。
//
// 为什么窗口重置放在这里（惰性重置）而不放定时任务：
//   定时任务要额外引入调度器、持久化"上次重置时间"、还要处理进程重启漏跑；
//   而"每次判定顺手检查是否过期"几乎零成本，且永远不会有"整点没跑"的窗口。
//   代价只是"某令牌在窗口过期后第一次被调用时才翻篇"——这正是我们想要的语义。

// BudgetExceeded 判断某令牌的周期预算是否已用尽。
//
// 返回值语义：
//   - (false, nil)：未启用预算（默认 0 / 不限额度 / 周期非法）、令牌不存在、
//     或窗口刚翻篇 —— 均应放行；
//   - (true, nil)：当前窗口已消耗达到 budget_quota，鉴权层应拒绝本次请求；
//   - (_, err)：读取令牌或重置窗口失败。由调用方决定如何处理（本项目约定：
//     不要把存储故障当成"超限"直接拒绝用户，也不静默忽略——如实上报）。
//
// 参数 tokenID 为 0 或未注入令牌仓储时返回 (false, nil)，保证降级部署与测试场景行为不变。
func (b *Billing) BudgetExceeded(ctx context.Context, tokenID uint64) (bool, error) {
	if b == nil || b.tokens == nil || tokenID == 0 {
		return false, nil
	}

	tk, err := b.tokens.GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, model.ErrTokenNotFound) {
			// 令牌已被删除：交给鉴权层按"令牌不存在"处理，不在此处误判为预算超限。
			return false, nil
		}
		return false, fmt.Errorf("relay: 读取令牌 %d 的预算状态失败: %w", tokenID, err)
	}

	decision := tk.EvaluateBudget(time.Now())
	if !decision.Enabled {
		// 未启用预算：与引入本能力前逐字一致地放行（存量令牌走的就是这条路）。
		return false, nil
	}
	if decision.NeedReset {
		// 窗口尚未锚定或已过期：先惰性重置（起点=now、基线对齐当前 used_quota），
		// 新窗口从零开始，本次放行。重置失败如实上报，绝不静默吞错。
		if err := b.tokens.ResetBudgetWindow(ctx, tokenID, decision.WindowStart); err != nil {
			return false, fmt.Errorf("relay: 重置令牌 %d 的预算窗口失败: %w", tokenID, err)
		}
		return false, nil
	}
	return decision.Exceeded, nil
}

// GroupSpendToday 返回某分组在【今天】已消耗的额度（只读，供后台展示/巡检）。
//
// 口径：以本地时区的自然日为区间 [今日零点, 明日零点)，按请求命中的渠道所属分组聚合
// （判定语义与路由一致，详见 model.TokenRepository.GroupSpendToday）。
// 只读，不参与扣费，也不做任何自动熔断——自动禁用渠道由渠道治理链路负责。
//
// 未注入令牌仓储时返回 0，不报错。
func (b *Billing) GroupSpendToday(ctx context.Context, group string) (int64, error) {
	if b == nil || b.tokens == nil {
		return 0, nil
	}
	start := startOfDay(time.Now())
	return b.tokens.GroupSpendToday(ctx, group, start, start.AddDate(0, 0, 1))
}

// startOfDay 返回 t 所在自然日的零点（保持 t 的时区）。
//
// 刻意按本地时区切分：站长看的是"我这边今天烧了多少"，
// 若按 UTC 切分，东八区凌晨的消耗会被算到前一天，与直觉不符。
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// estimatePromptTokens 由请求体字节数估算 prompt token 数（保守上界）。
func estimatePromptTokens(promptBytes int) int64 {
	if promptBytes <= 0 {
		return 0
	}
	return int64((promptBytes + estimateBytesPerToken - 1) / estimateBytesPerToken)
}
