// 本文件定义「调用日志」领域模型，它是用量统计与后续计费的数据基础。
//
// 意图（Why）：
//
//	网关必须能回答运营中最常见的几个问题：
//	  「今天用了多少」「哪个模型最热」「谁用得最多」「失败率多高」。
//	这些问题的答案只能来自逐条记录的调用日志，因此每条转发（无论成功失败）
//	都应落一条日志，且记录足够完整（用户、令牌、渠道、模型、耗时、状态码）。
//
// 流转（Flow）：
//
//	relay 转发完成 → 组装 UsageLog → UsageLogRepository.Create
//	  └─ 后台仪表盘：Summary / DailySeries / TopModels 聚合查询
//	  └─ 用户门户：按 UserID 过滤的同一组聚合查询
//
// 扩展（Extend）：
//
//	接入计费后：新增 quota 的精算逻辑（当前由调用方按倍率估算后写入），
//	  并考虑把日志库独立出来（日志写入量远大于业务表）。
//	新增统计维度（如按渠道、按令牌）：在 UsageLogQuery 加条件并在仓储层实现聚合。
package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 领域错误。
var (
	// ErrUsageLogNotFound 表示未找到指定日志（通常无需对外暴露）。
	ErrUsageLogNotFound = errors.New("model: 调用日志不存在")
)

// LogStatus 是日志查询中使用的状态语义值。
//
// 为什么不直接用 HTTP 状态码：客户端筛选时想表达的是「成功/失败」这类语义，
// 而不是具体是 400 还是 500。仓储层负责把语义翻译为 SQL 条件。
const (
	// LogStatusSuccess 表示按「成功」筛选（2xx）。
	LogStatusSuccess = "success"
	// LogStatusError 表示按「失败」筛选（非 2xx，或存在错误信息）。
	LogStatusError = "error"
)

// UsageLog 表示一次模型接口调用记录。
//
// 字段设计说明：
//   - ChannelID 允许为 0（表示尚未选定渠道就失败，如无可用渠道）；
//   - Error 只记录"面向运维的简短原因"，禁止写入上游返回的原始报错全文
//     （可能包含上游地址或密钥片段）；
//   - RequestID 用于与客户端日志对账，排查"客户端说失败、服务端说成功"这类问题。
type UsageLog struct {
	ID        uint64 // 主键
	UserID    uint64 // 调用者用户 ID（0 表示未认证或系统调用）
	TokenID   uint64 // 使用的访问令牌 ID
	ChannelID uint64 // 命中的上游渠道 ID
	// ChannelKeyID 是本次实际使用的【池内密钥记录 ID】（迁移 0032）。
	//
	// 0 表示不适用或未采集：渠道使用单密钥模式、请求在选渠道前就失败、
	// 或本条是迁移之前写入的历史日志。
	// 记下它的意义：可以回答"这把密钥被用了多少"，进而结合上游进价
	// 算出"余额还剩多少"——否则余额永远只是一个静态的人工快照。
	ChannelKeyID     uint64
	Model            string // 请求的模型名（对外模型名）
	UpstreamModel    string // 实际发给上游的模型名（经渠道映射改写）；空串表示与 Model 相同
	PromptTokens     int    // 输入 token 数
	CompletionTokens int    // 输出 token 数
	TotalTokens      int    // 总 token 数
	// CachedTokens 是输入中命中上游缓存的 token 数（迁移 0026）。
	//
	// 为什么单独记：这部分通常按更低价计费，是核对账单与评估
	// "提示词前缀复用"效果的唯一依据；0 表示上游未返回该字段（不是"没有命中"）。
	CachedTokens int
	// ReasoningTokens 是输出中属于"推理过程"的 token 数（迁移 0026）。
	//
	// 它计入输出但用户看不到，出账时最容易引起争议，必须单独可查。
	ReasoningTokens int
	// FirstTokenMS 是首个响应字节的到达时间（TTFB，毫秒；0 = 未采集/非流式）。
	//
	// 只对非流式请求没有意义：那种情况响应一次性返回，不存在"首 token 延迟"。
	FirstTokenMS int
	// TokensPerSecond 是输出速率（tokens/s；0 表示无法计算）。
	//
	// 计算口径：CompletionTokens / (总耗时 − 首 token 延迟)。
	// 用总耗时会随回答变长而低估生成速度，因此必须扣除排队与首包时间。
	TokensPerSecond float64
	Quota           int64  // 本次消耗额度（内部单位）
	LatencyMS       int    // 总耗时（毫秒）
	IsStream        bool   // 是否流式请求
	StatusCode      int    // 回写给客户端的状态码
	Error           string // 失败原因（已脱敏、简短）
	RequestID       string // 请求标识
	// PriceVersion 是本次计费所依据的【定价版本快照标识】（迁移 0047）。
	//
	// 为什么需要它：定价规则是就地更新的，改价之后历史账单无法再说明"当时按什么价算"。
	// 记下当时的规则 ID 与版本时间（见 PriceSnapshotVersion），即可在事后定位到
	// "这条账走的是哪一版价格"，为"改价后旧账仍可按旧价复算"提供锚点。
	// 空串表示未采集（历史数据，或写入方尚未接线）。
	PriceVersion string
	CreatedAt    time.Time // 记录时间
}

// Validate 校验日志的必要字段。
//
// 说明：日志的校验刻意宽松——它是可观测性数据，宁可记录一条字段不全的日志，
// 也不要因为校验失败而丢失"某次调用发生过"这一事实。
func (l *UsageLog) Validate() error {
	if l.StatusCode < 0 || l.StatusCode > 599 {
		return fmt.Errorf("状态码非法: %d", l.StatusCode)
	}
	if l.LatencyMS < 0 {
		return fmt.Errorf("耗时不能为负数: %d", l.LatencyMS)
	}
	if l.PromptTokens < 0 || l.CompletionTokens < 0 {
		return errors.New("token 数不能为负数")
	}
	return nil
}

// IsSuccess 判断本次调用是否成功（以回写给客户端的状态码为准）。
func (l *UsageLog) IsSuccess() bool {
	return l.StatusCode >= 200 && l.StatusCode < 300
}

// UsageLogQuery 描述调用日志的查询与聚合条件。
//
// 同一结构体同时用于列表查询与聚合统计，避免两套条件出现语义漂移
// （例如"列表按用户过滤、统计忘了过滤"这类难以发现的偏差）。
type UsageLogQuery struct {
	UserID    *uint64    // 按用户过滤；nil 表示不过滤
	TokenID   *uint64    // 按令牌过滤
	ChannelID *uint64    // 按渠道过滤
	Model     string     // 按模型精确匹配；空表示不过滤
	Status    string     // LogStatusSuccess / LogStatusError / 空=不过滤
	Since     *time.Time // 起始时间（含）；nil 表示不限
	Until     *time.Time // 结束时间（含）；nil 表示不限
	Limit     int        // 条数上限（仅列表查询使用）
	Offset    int        // 偏移量（仅列表查询使用）
}

// UsageSummary 是某条件下的用量汇总。
type UsageSummary struct {
	Requests int64 // 请求总数
	Success  int64 // 成功数（2xx）
	Tokens   int64 // token 总量
	Quota    int64 // 额度总量
	// PromptTokens / CompletionTokens 把总量拆成"输入"与"输出"。
	//
	// 为什么必须拆：两者的成本与优化手段完全不同（输入可缓存复用、输出受生成速度限制），
	// 只看 total 无法回答"成本涨在输入还是输出上"。
	PromptTokens     int64
	CompletionTokens int64
	// CachedTokens 是输入中命中上游缓存的 token 总量（评估缓存收益的依据）。
	CachedTokens int64
	// ReasoningTokens 是输出中的推理 token 总量（出账争议的主要来源）。
	ReasoningTokens int64
	// LatencySumMS 是总耗时之和与成功请求数，用于算平均总延迟。
	LatencySumMS int64
	// FirstTokenSumMS / FirstTokenSamples 用于算平均首 token 延迟（TTFB）。
	//
	// 单独统计样本数而不是复用 Requests：非流式请求不产生 TTFB（值为 0），
	// 若把它算进平均会把"平均首 token 延迟"拉低成没有意义的数字。
	FirstTokenSumMS   int64
	FirstTokenSamples int64
	// TPSSum / TPSSamples 用于算平均输出速率。
	//
	// 同样单独计样本：速率只在"有输出 token 且时长可算"时才存在，
	// 用请求总数做分母会得到偏低且不可解释的平均值。
	TPSSum     float64
	TPSSamples int64
}

// SuccessRate 返回成功率（0~1）；无请求时返回 0。
func (s UsageSummary) SuccessRate() float64 {
	if s.Requests <= 0 {
		return 0
	}
	return float64(s.Success) / float64(s.Requests)
}

// CacheHitRate 返回输入缓存命中率（0~1）；输入为 0 时返回 0。
//
// 口径：CachedTokens / PromptTokens。上游把"命中缓存的输入"算在 PromptTokens 内，
// 因此这个比值就是"输入里有多大比例是复用的"。
func (s UsageSummary) CacheHitRate() float64 {
	if s.PromptTokens <= 0 {
		return 0
	}
	return float64(s.CachedTokens) / float64(s.PromptTokens)
}

// AvgLatencyMS 返回平均总耗时（毫秒）；无请求时返回 0。
func (s UsageSummary) AvgLatencyMS() int64 {
	if s.Requests <= 0 {
		return 0
	}
	return s.LatencySumMS / s.Requests
}

// AvgFirstTokenMS 返回平均首 token 延迟（毫秒）；无样本时返回 0。
func (s UsageSummary) AvgFirstTokenMS() int64 {
	if s.FirstTokenSamples <= 0 {
		return 0
	}
	return s.FirstTokenSumMS / s.FirstTokenSamples
}

// AvgTokensPerSecond 返回平均输出速率（tokens/s）；无样本时返回 0。
func (s UsageSummary) AvgTokensPerSecond() float64 {
	if s.TPSSamples <= 0 {
		return 0
	}
	return s.TPSSum / float64(s.TPSSamples)
}

// DailyUsage 是单日用量，用于趋势图。
type DailyUsage struct {
	Date     string // 日期，格式 2006-01-02
	Requests int64  // 请求数
	Tokens   int64  // token 数
	Quota    int64  // 额度
	// CachedTokens 是当日输入中命中上游缓存的 token 数。
	//
	// 放进趋势而不仅仅放汇总：缓存命中率的"趋势"比单点数字更有价值——
	// 它反映提示词前缀复用是否在持续生效，掉下去往往意味着代码改动破坏了前缀。
	CachedTokens int64
}

// ModelUsage 是单个模型的用量，用于排行榜。
type ModelUsage struct {
	Model    string // 模型名
	Requests int64  // 请求数
	Tokens   int64  // token 数
}

// LeaderboardEntry 是「用量排行榜」中的一行：某个用户在统计窗口内的综合用量。
//
// 分数口径（与前端展示一致）：
//
//	score = 0.5 × (requests / 榜内最大请求数) + 0.5 × (tokens / 榜内最大 token 数)
//
// 归一化而不是直接用绝对量相加：请求数与 token 数的量纲差异极大
// （一次长回答可能消耗数万 token，而请求数只有 1），直接相加会被 token 主导，
// 让"请求少但 token 多"的用户永远霸榜，榜单失去区分度。
//
// Paid 表示该用户是否属于"付费用户"（存在已支付订单）。排行榜据此
// 拆成「付费榜 / 免费榜」两个榜单，避免免费用户与付费用户混排后
// 免费榜永远被付费用户占据（付费用户往往使用量更大）。
type LeaderboardEntry struct {
	UserID   uint64 // 用户 ID
	Username string // 用户名（榜单展示的"账号 ID"）
	// Requests 是窗口内请求总数。
	Requests int64
	// Tokens 是窗口内 token 消耗总数。
	Tokens int64
	// AvgLatencyMS 是窗口内平均请求耗时（毫秒）。
	//
	// 只统计成功请求（2xx/3xx）：失败请求的耗时往往极短（在网关层就被拒），
	// 混入平均会系统性拉低"模型快不快"的可信度。
	AvgLatencyMS float64
	// PeakConcurrency 是窗口内的峰值并发请求数估算。
	//
	// 口径：把每条成功请求视为 [开始, 结束] 的时间区间（开始 = created_at − latency），
	// 用差分事件扫描（开始 +1 / 结束 −1）求最大重叠数。该值反映"最忙的瞬间
	// 同时有多少请求在途"，是衡量该账号并行使用强度的直观指标。
	PeakConcurrency int64
	// Paid 标记该用户是否付费用户（存在 status=paid 的充值订单）。
	Paid bool
	// SuccessRequests 是窗口内该用户的成功请求数（2xx/3xx）。
	//
	// Requests 统计全部请求（含失败），SuccessRequests 只数成功——
	// 两者相除即成功率。这是"用得稳不稳"与"用得多不多"的区分：
	// 次数很高但频繁失败的用户，请求数领先却成功率低，应当被识别出来。
	SuccessRequests int64
}

// SuccessRate 返回该用户的请求成功率（0~1）；无请求时返回 0。
func (e LeaderboardEntry) SuccessRate() float64 {
	if e.Requests <= 0 {
		return 0
	}
	return float64(e.SuccessRequests) / float64(e.Requests)
}

// LeaderboardScore 返回综合使用量分数（0~100）。
//
// 两个归一化分量各占 50 分，缺失分母（榜内最大值为 0）时对应分量按 0 计。
//
// 满分封顶 100：归一化后榜首两项均为 1，分数恰为 100；分数是"相对榜内
// 标杆的刻度"，不是累计量，因此用得再久也不会超过榜首——把它做成可比较
// 的百分制刻度，比 0~1 的小数直观，也杜绝了"数字无限增长"的误读。
func (e LeaderboardEntry) LeaderboardScore(maxRequests, maxTokens int64) float64 {
	var score float64
	if maxRequests > 0 {
		score += 50 * float64(e.Requests) / float64(maxRequests)
	}
	if maxTokens > 0 {
		score += 50 * float64(e.Tokens) / float64(maxTokens)
	}
	// 浮点误差保护：归一化比值理论上不会超过 1，但除法舍入可能给出 1.0000000002。
	if score > 100 {
		return 100
	}
	return score
}

// ModelFailureStat 是"某个渠道上、某个模型、某个失败状态码"的计数。
//
// 用途：后台渠道详情页据此标记"这个模型最近一直 403/404"，
// 让站长不用翻调用日志就能看出该从渠道清单里清掉哪些失效模型。
// 三个字段组合成一条唯一记录（model + status_code 去重）。
type ModelFailureStat struct {
	Model      string `json:"model"`       // 模型名
	StatusCode int    `json:"status_code"` // 失败状态码（>= 400）
	Count      int64  `json:"count"`       // 该 (模型, 状态码) 组合的出现次数
}

// 对账维度取值（真实成本对账）。
//
// 集中定义为常量：这些字符串会同时出现在查询参数与响应里，拼错时不会编译报错，
// 只会静默回退到默认维度——属于最典型的"传了却没生效"。
const (
	// ReconcileDimGroup 按令牌归属分组聚合（usage_logs 不落分组，实现侧按 token 关联）。
	ReconcileDimGroup = "group"
	// ReconcileDimChannel 按上游渠道聚合。
	ReconcileDimChannel = "channel"
	// ReconcileDimModel 按模型聚合。
	ReconcileDimModel = "model"
)

// IsValidReconcileDim 判断对账维度取值是否合法。
func IsValidReconcileDim(dim string) bool {
	switch dim {
	case ReconcileDimGroup, ReconcileDimChannel, ReconcileDimModel:
		return true
	default:
		return false
	}
}

// UsageReconciliationRow 是「真实成本对账」在某一维度上的一行结果。
//
// 金额一律为整数【额度】单位（全站统一记账单位）：毛利 = 收入 − 成本，一次减法即可，
// 不使用浮点参与累加，避免"用了一万次之后差一点"的账目漂移。
type UsageReconciliationRow struct {
	Dim   string // 维度：group / channel / model
	Key   string // 维度取值（分组名 / 渠道 ID 字符串 / 模型名）
	Label string // 展示标签（渠道维度为渠道名，其余与 Key 相同）
	// Requests 是该维度下的成功请求数（口径见 store 层实现：仅 2xx/3xx）。
	Requests     int64
	RevenueQuota int64 // 收入额度（用户实扣额度合计，取自 usage_logs.quota）
	// CostQuota 是成本额度（按上游进价估算）。
	//
	// 它按 (渠道, 上游模型名) 匹配 channel_model_costs 后逐行估算：
	// 按次规则走"次数 × 每次单价"，因此按次计费渠道的成本不会被算成 0。
	CostQuota int64
	// PricedRequests / UnpricedRequests 分别是"能/不能估算成本"的请求数。
	//
	// 未录进价的请求成本按 0 计，但必须用这两个字段把"未知"显式暴露——
	// 否则对账表会看起来"全是利润"，与真实账目背离（与密钥核算同一口径）。
	PricedRequests   int64
	UnpricedRequests int64
}

// GrossProfitQuota 返回毛利额度（收入 − 成本）。
func (r UsageReconciliationRow) GrossProfitQuota() int64 {
	return r.RevenueQuota - r.CostQuota
}

// PriceSnapshotVersion 生成一条定价规则的版本标识（形如 "123@1727000000"）。
//
// 用途：写入 usage_logs.price_version，作为"这条账按哪一版价格结算"的锚点。
// 规则 ID + 规则更新时间能唯一定位到某一次改价之前/之后的规则状态；
// 二者缺一时退化为能拿到的部分，【绝不返回错误】——版本锚点是记账的附加信息，
// 不能因为它而阻断任何一次调用。
func PriceSnapshotVersion(ruleID uint64, updatedAt time.Time) string {
	if ruleID == 0 {
		return ""
	}
	if updatedAt.IsZero() {
		return strconv.FormatUint(ruleID, 10)
	}
	return strconv.FormatUint(ruleID, 10) + "@" + strconv.FormatInt(updatedAt.Unix(), 10)
}

// UsageLogRepository 定义调用日志的持久化与聚合操作。
type UsageLogRepository interface {
	// Create 写入一条调用日志。
	Create(ctx context.Context, log *UsageLog) error

	// List 按条件返回日志列表，按时间倒序（最新在前）。
	List(ctx context.Context, q UsageLogQuery) ([]*UsageLog, error)

	// Count 返回符合条件的日志总数，用于分页。
	Count(ctx context.Context, q UsageLogQuery) (int, error)

	// Summary 返回符合条件的用量汇总。
	Summary(ctx context.Context, q UsageLogQuery) (*UsageSummary, error)

	// DailySeries 按天聚合，返回按日期升序的趋势数据。
	//
	// 实现要求：必须补全"没有请求的日期"（补 0），否则前端折线图会出现断点，
	// 让人误以为那些天服务中断了。补零逻辑放在实现层，避免每个调用方各自处理。
	DailySeries(ctx context.Context, q UsageLogQuery) ([]DailyUsage, error)

	// TopModels 返回用量最高的前 N 个模型，按请求数降序。
	TopModels(ctx context.Context, q UsageLogQuery, limit int) ([]ModelUsage, error)

	// SumUsageByChannelKey 按 (密钥, 模型) 汇总某渠道的用量，用于密钥余额核算。
	//
	// 口径（重要）：
	//   - 只统计 status_code < 400 的【成功】请求：失败请求通常不消耗上游额度，
	//     把它们算进成本会让余额看起来比实际掉得更快；
	//   - 只统计 channel_key_id > 0 的行：单密钥模式与历史数据无法归属到具体凭据；
	//   - 统计范围为【全部历史】（余额是累计量，不能只看某个时间窗）。
	SumUsageByChannelKey(ctx context.Context, channelID uint64) ([]*ChannelKeyUsage, error)

	// ModelFailureStats 返回某渠道近 since 时间内的失败请求统计（按模型 × 状态码）。
	//
	// 用途：渠道详情页的"失效模型体检"——404/410 意味着该模型在上游已不存在，
	// 403 意味着凭据对该模型无授权，站长据此清理渠道模型清单。
	// 只统计 status_code >= 400 的行，且 model 非空。
	ModelFailureStats(ctx context.Context, channelID uint64, since time.Time) ([]ModelFailureStat, error)

	// Leaderboard 返回统计窗口内各用户的综合用量排行（按请求数降序）。
	//
	// 用途：门户概览页的「用量排行榜」——付费榜 / 免费榜各取前 N 名。
	// 实现要点（与 store 层契约一致）：
	//   - 只统计成功请求（2xx/3xx）：失败请求不消耗 token、耗时不可信，
	//     混入会同时污染"分数"与"平均耗时"两个口径；
	//   - 只统计 user_id > 0 的行：未认证/系统调用无账号可归属；
	//   - 平均耗时只按成功请求计算；峰值并发用差分事件扫描估算；
	//   - 返回结果无序，由调用方决定排序与截断（付费/免费分榜在此之上做）。
	//
	// 窗口由 q.Since（含）到 q.Until（不含，nil 表示现在）限定；
	// 参考 TopModels 的 limit 语义，本方法不设内部条数上限（全量返回，
	// 供调用方一次性分完两个榜单，避免"付费榜要前 20、免费榜也要前 20"
	// 这种需求拆成两次全量查询）。
	Leaderboard(ctx context.Context, q UsageLogQuery) ([]LeaderboardEntry, error)
}

// ChannelKeyUsage 是一把密钥在某个模型上的用量汇总（密钥余额核算的输入）。
//
// 按 (密钥, 模型) 而不是只按密钥分组，是因为不同模型的进价差别极大，
// 必须逐模型匹配进价后再累加，否则算出的成本会明显失真。
type ChannelKeyUsage struct {
	ChannelKeyID uint64 // 池内密钥记录 ID（> 0）
	ChannelID    uint64 // 所属渠道
	Model        string // 请求的对外模型名
	// UpstreamModel 是实际发给上游的模型名（映射改写后的名字）；空串表示与 Model 相同。
	//
	// 匹配上游进价时必须用它：钱是上游按上游模型名收的。
	UpstreamModel    string
	Requests         int64 // 成功请求数
	PromptTokens     int64 // 输入 token 合计
	CompletionTokens int64 // 输出 token 合计
	CachedTokens     int64 // 其中命中缓存合计
}

// CostModelName 返回用于匹配上游进价的模型名。
//
// 优先用上游模型名（成本由上游按该名字决定），为空时回退对外名——
// 与 usage_logs.upstream_model 的空值语义一致（空 = 映射未改写，两者相同）。
func (u *ChannelKeyUsage) CostModelName() string {
	if u == nil {
		return ""
	}
	if name := strings.TrimSpace(u.UpstreamModel); name != "" {
		return name
	}
	return u.Model
}
