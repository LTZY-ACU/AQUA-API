// 本文件定义「渠道密钥池」领域模型与仓储接口。
//
// 意图（Why）：
//
//	一个上游通常有多把密钥（批量申请的免费额度密钥、多人共享的团队密钥）。
//	把它们放进同一个渠道的"池"里轮询使用，能同时得到两个好处：
//	  1) 单渠道即可获得远超单密钥的配额上限（请求在池内轮换）；
//	  2) 某把密钥失效时只摘除这一把，渠道整体仍然可用（故障半径最小）。
//
//	若不这样做而选择"一密钥一渠道"，渠道列表会膨胀到几百行，
//	既无法运维（改一个 base_url 要改几百次），也无法做池内负载均衡。
//
// 安全约定（与 Channel 一致）：
//
//	Key 字段在内存中是明文，仅在进程内流转；落库由 store 层加密，
//	对外输出一律使用 Masked()，绝不返回明文。
//
// 流转（Flow）：
//
//	后台导入：ChannelsView 粘贴多行密钥 → ReplaceAll 落库（加密）
//	转发路径：relay 选中渠道 → ListUsable 取池 → 随机挑一把 → MarkUsed
//	              → 失败 MarkFailure（连续失败达阈值自动摘除）
//	              → 成功 MarkSuccess（清零连续失败计数）
//
// 扩展（Extend）：
//
//	新增密钥维度的策略（如按模型区分密钥）时，在 ChannelKey 加字段并建新迁移，
//	本文件与 store/channel_key_repo.go、前端渠道表单三处同步。
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// BalanceUnknown 是"余额未知"的哨兵值。
//
// 为什么用 -1 而不是 0：
//   - 0 有明确含义——"已知余额为 0，即已用尽"；
//   - 若把"未录入"也表示为 0，则所有老数据会在升级瞬间被判定为"已用尽"
//     而集体退出调度，这是不可接受的线上事故。
//     -1 与任何真实余额都不冲突，且能自然表达"这条凭据我们不掌握它的余额"。
const BalanceUnknown int64 = -1

// KeyAutoRemoveThreshold 是密钥被自动摘除前允许的连续失败次数。
//
// 取值 3 的权衡：
//   - 过小（1 次）：一次网络抖动或上游瞬时 500 就会摘掉好密钥，
//     池子会随时间被"误杀"到没有可用密钥；
//   - 过大：失效密钥（401/额度耗尽）会反复被选中，持续浪费尝试次数与延迟。
//     3 次连续失败已能排除偶发抖动，又能快速隔离真正失效的密钥。
const KeyAutoRemoveThreshold = 3

// ChannelKeyStatus 表示密钥在池中的可用状态。
type ChannelKeyStatus int

const (
	// ChannelKeyStatusEnabled 启用：可参与轮询。
	ChannelKeyStatusEnabled ChannelKeyStatus = 1
	// ChannelKeyStatusDisabled 手动禁用：由管理员主动关闭，自动流程不会恢复。
	ChannelKeyStatusDisabled ChannelKeyStatus = 2
	// ChannelKeyStatusAutoRemoved 自动摘除：连续失败达阈值，可手动恢复。
	ChannelKeyStatusAutoRemoved ChannelKeyStatus = 3
)

// String 返回状态中文名，便于日志与界面展示。
func (s ChannelKeyStatus) String() string {
	switch s {
	case ChannelKeyStatusEnabled:
		return "启用"
	case ChannelKeyStatusDisabled:
		return "手动禁用"
	case ChannelKeyStatusAutoRemoved:
		return "已自动摘除"
	default:
		return fmt.Sprintf("未知(%d)", int(s))
	}
}

// IsValid 判断状态值是否合法（用于校验外部输入）。
func (s ChannelKeyStatus) IsValid() bool {
	switch s {
	case ChannelKeyStatusEnabled, ChannelKeyStatusDisabled, ChannelKeyStatusAutoRemoved:
		return true
	default:
		return false
	}
}

// KeyStrategy 表示渠道级凭据调度策略（落库于 channels.key_strategy）。
//
// 为什么把它做成"渠道级"而不是"凭据级"：运维关心的是
// "这个上游的一池钥匙整体怎么用"，而不是逐把钥匙配置挑选方式；
// 逐把配置既繁琐又难以解释（同一池里两种策略会互相打架）。
type KeyStrategy string

const (
	// KeyStrategySequential 顺序：永远取优先级最高、id 最小的可用凭据。
	// 适用：希望固定主用某几把钥匙、其余作为备份的场景。
	KeyStrategySequential KeyStrategy = "sequential"
	// KeyStrategyRoundRobin 轮询：环形游标依次取，游标持久化在渠道上。
	// 适用：希望把请求严格均摊到每把钥匙（如按账号均分免费额度）。
	KeyStrategyRoundRobin KeyStrategy = "round_robin"
	// KeyStrategyWeightedRandom 加权随机：按 weight 倾斜。
	// 适用：想让额度大/质量好的钥匙承担更多流量。
	KeyStrategyWeightedRandom KeyStrategy = "weighted_random"
	// KeyStrategyLeastRecent 最久未用：优先取最长时间没被用过的（从未用过最优先）。
	// 适用：让池内每把钥匙都轮流"热身"，避免长期闲置的钥匙一直不被发现失效。
	KeyStrategyLeastRecent KeyStrategy = "least_recent"
	// KeyStrategyLeastInFlight 最少在途：优先取当前在途请求数最少的（默认）。
	// 适用：高并发场景，尽量把流量分散到未被占用的钥匙上，降低单把被限流的概率。
	KeyStrategyLeastInFlight KeyStrategy = "least_in_flight"
)

// IsValid 判断策略取值是否合法（用于校验外部输入）。
func (s KeyStrategy) IsValid() bool {
	switch s {
	case KeyStrategySequential, KeyStrategyRoundRobin, KeyStrategyWeightedRandom,
		KeyStrategyLeastRecent, KeyStrategyLeastInFlight:
		return true
	default:
		return false
	}
}

// String 返回策略的中文名，便于日志与界面展示。
func (s KeyStrategy) String() string {
	switch s {
	case KeyStrategySequential:
		return "顺序"
	case KeyStrategyRoundRobin:
		return "轮询"
	case KeyStrategyWeightedRandom:
		return "加权随机"
	case KeyStrategyLeastRecent:
		return "最久未用"
	case KeyStrategyLeastInFlight:
		return "最少在途"
	default:
		return string(s)
	}
}

// NormalizeKeyStrategy 把库中读到的原始字符串归一为合法策略。
//
// 空值或未知值一律退回默认策略（least_in_flight）：
// 策略只影响"挑哪一把"，不应因为一个拼错的配置让整条渠道不可用。
func NormalizeKeyStrategy(raw string) KeyStrategy {
	s := KeyStrategy(strings.TrimSpace(raw))
	if s.IsValid() {
		return s
	}
	return KeyStrategyLeastInFlight
}

// KeyStrategyAll 返回全部合法策略，顺序与后台下拉展示一致。
//
// 单独提供这份清单（而不是让调用方各自拼列表）：
// 校验提示、接口下发与界面渲染都以它为唯一来源，避免新增策略时漏改某处。
func KeyStrategyAll() []KeyStrategy {
	return []KeyStrategy{
		KeyStrategySequential,
		KeyStrategyRoundRobin,
		KeyStrategyWeightedRandom,
		KeyStrategyLeastRecent,
		KeyStrategyLeastInFlight,
	}
}

// KeyStrategyOptionText 返回策略标识的顿号连接串，用于错误提示中的"可选值"。
func KeyStrategyOptionText() string {
	options := KeyStrategyAll()
	parts := make([]string, 0, len(options))
	for _, s := range options {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, " / ")
}

// Description 返回策略的一句话说明，供后台渲染帮助文案。
//
// 文字放在领域层而不是前端：新增策略时只需在此补充，界面自动跟随，
// 避免"后端加了策略、前端忘了加说明"的漏配。
func (s KeyStrategy) Description() string {
	switch s {
	case KeyStrategySequential:
		return "总是优先使用优先级最高、最早加入的凭据，其余仅作备份。"
	case KeyStrategyRoundRobin:
		return "按顺序轮流使用池内凭据，让请求严格均摊到每一把。"
	case KeyStrategyWeightedRandom:
		return "按权重随机挑选，权重越大承担越多的流量。"
	case KeyStrategyLeastRecent:
		return "优先使用最久没有被用过的凭据，让每把都有机会被发现失效。"
	case KeyStrategyLeastInFlight:
		return "优先使用当前在途请求最少的凭据，高并发下最不容易把流量压在一把上。"
	default:
		return ""
	}
}

// DefaultKeyStrategy 返回渠道未显式配置时使用的默认策略。
func DefaultKeyStrategy() KeyStrategy { return KeyStrategyLeastInFlight }

// MaxKeyCooldownSeconds 是渠道级"统一冷却时长"的上限（24 小时）。
//
// 为什么要设上限：冷却时长过长等价于"事实上的摘除"——
// 站长明明选了"只冷却不摘除"，却观察到某把密钥好几天不回来，
// 会得出"策略没生效"的错误结论。24 小时足以覆盖任何真实的上游风控窗口，
// 同时保证"密钥总会自己回来"，与"只冷却"的承诺一致。
const MaxKeyCooldownSeconds = 24 * 60 * 60

// KeyFailurePolicy 表示渠道级「凭据失败处置策略」（落库于 channels.key_failure_policy）。
//
// 为什么必须做成可配置，而不是给所有渠道一套策略：
//
//	"密钥失败之后该怎么办"高度依赖上游的性质，两类上游的正确答案是相反的：
//	  1) 密钥真的会死（付费账号被停用、密钥被吊销）：必须摘除，
//	     否则每次请求都会随机撞上这把死密钥，白等一次超时。
//	  2) 密钥不会死（如 NVIDIA NIM 的免费额度池）：失败几乎都是限流/瞬时 5xx/
//	     临时风控，密钥本身仍然有效。此时若自动摘除，池子会随时间被"误杀"到
//	     没有可用密钥——表现就是用户侧大面积 503，而站长从界面上只看到
//	     "可用 481 / 500"这种缓慢流失，极难归因。
//
//	因此策略放在渠道上，由站长按上游性质自行选择。
type KeyFailurePolicy string

const (
	// KeyFailurePolicyCooldownOnly 只冷却、永不自动摘除（默认）。
	//
	// 失败后只设置 cooldown_until，到期自动回到调度；
	// 上游即便明确说"密钥已吊销"，也只会得到一次较长冷却而不是被摘除。
	// 唯一能让密钥退出池子的动作是站长手动禁用/下架。
	KeyFailurePolicyCooldownOnly KeyFailurePolicy = "cooldown_only"
	// KeyFailurePolicyAutoRemove 保留自动摘除（连续失败达阈值或上游明确判定永久无效）。
	//
	// 适用：密钥确实会死的上游。被摘除的密钥可在密钥池明细里手动恢复。
	KeyFailurePolicyAutoRemove KeyFailurePolicy = "auto_remove"
)

// IsValid 判断策略取值是否合法（用于校验外部输入）。
func (p KeyFailurePolicy) IsValid() bool {
	return p == KeyFailurePolicyCooldownOnly || p == KeyFailurePolicyAutoRemove
}

// String 返回策略中文名，便于日志与界面展示。
func (p KeyFailurePolicy) String() string {
	switch p {
	case KeyFailurePolicyCooldownOnly:
		return "只冷却不摘除"
	case KeyFailurePolicyAutoRemove:
		return "失败自动摘除"
	default:
		return string(p)
	}
}

// Description 返回策略的一句话说明，供后台渲染帮助文案。
//
// 文字放在领域层而不是前端：新增策略时只改这里，界面自动跟随，
// 避免"后端加了策略、前端忘了加说明"的漏配。
func (p KeyFailurePolicy) Description() string {
	switch p {
	case KeyFailurePolicyCooldownOnly:
		return "密钥失败后只进入冷却池，到期自动回到调度，永不自动摘除。" +
			"适合密钥不会失效的上游（如免费额度池）：避免好密钥被瞬时故障误杀导致池子空掉。"
	case KeyFailurePolicyAutoRemove:
		return "密钥连续失败达阈值，或上游明确判定密钥永久无效时自动摘除，需人工恢复。" +
			"适合密钥真的会被吊销/停用的上游。"
	default:
		return ""
	}
}

// NormalizeKeyFailurePolicy 把库中读到的原始字符串归一为合法策略。
//
// 空值或未知值一律退回默认策略（只冷却不摘除）：
// "永不自动摘除"是更安全的默认——被误杀的好密钥会直接造成用户侧 503，
// 而一把失效密钥留在池里最坏也只是偶尔浪费一次尝试（且它会持续处于冷却）。
func NormalizeKeyFailurePolicy(raw string) KeyFailurePolicy {
	p := KeyFailurePolicy(strings.TrimSpace(raw))
	if p.IsValid() {
		return p
	}
	return DefaultKeyFailurePolicy()
}

// KeyFailurePolicyAll 返回全部合法策略，顺序与后台下拉展示一致。
func KeyFailurePolicyAll() []KeyFailurePolicy {
	return []KeyFailurePolicy{
		KeyFailurePolicyCooldownOnly,
		KeyFailurePolicyAutoRemove,
	}
}

// KeyFailurePolicyOptionText 返回策略标识的顿号连接串，用于错误提示中的"可选值"。
func KeyFailurePolicyOptionText() string {
	options := KeyFailurePolicyAll()
	parts := make([]string, 0, len(options))
	for _, p := range options {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, " / ")
}

// DefaultKeyFailurePolicy 返回渠道未显式配置时使用的默认策略。
func DefaultKeyFailurePolicy() KeyFailurePolicy { return KeyFailurePolicyCooldownOnly }

// NormalizeKeyCooldownSeconds 把库中读到的原始秒数归一到合法区间。
//
// 语义：0 = 使用内置的分级指数退避；>0 = 统一冷却该秒数。
// 越界值一律夹到 [0, MaxKeyCooldownSeconds]：
// 负数会让 cooldown_until 落到过去时刻（等价于不冷却，静默失效），
// 过大的值则等价于"事实摘除"，两者都会让站长的预期与实际行为不一致。
func NormalizeKeyCooldownSeconds(seconds int) int {
	if seconds < 0 {
		return 0
	}
	if seconds > MaxKeyCooldownSeconds {
		return MaxKeyCooldownSeconds
	}
	return seconds
}

// CredentialKind 表示凭据类型。
//
// 两类凭据的调度语义完全一致（池内轮询、失败摘除、记录最近使用），
// 差别只在"取出来之后要不要先刷新"：
//   - api_key：长期有效，取出即可用；
//   - oauth  ：access_token 短期有效，过期前需用 refresh_token 刷新。
type CredentialKind string

const (
	// CredentialKindAPIKey 静态 API Key。
	CredentialKindAPIKey CredentialKind = "api_key"
	// CredentialKindOAuth 订阅账号的 OAuth 凭据（含 refresh_token）。
	CredentialKindOAuth CredentialKind = "oauth"
)

// IsValid 判断凭据类型是否合法。
func (k CredentialKind) IsValid() bool {
	return k == CredentialKindAPIKey || k == CredentialKindOAuth
}

// refreshAheadSeconds 是"提前刷新"的窗口。
//
// 为什么要提前：若等到 access_token 真正过期才刷新，那么"过期瞬间进来的请求"
// 会先失败一次（拿 401 才发现要刷新），用户会看到偶发报错。
// 提前 60 秒刷新可以把这类失败窗口完全消除，代价只是偶尔多刷一次。
const refreshAheadSeconds = 60

// QuotaUsedPercentUnknown 表示"该凭据的额度尚未探测过"。
//
// 为什么用 -1 而不是 0：0 是一个合法且常见的真实值（一次都没用过），
// 若用 0 兼任"未知"，界面会把"没查过"显示成"额度充足"，站长据此判断会出错。
const QuotaUsedPercentUnknown = -1

// ChannelKey 表示渠道下的一条凭据（API Key 或 OAuth 账号）。
type ChannelKey struct {
	ID        uint64           // 主键
	ChannelID uint64           // 归属渠道
	Kind      CredentialKind   // 凭据类型
	Key       string           // api_key 明文【仅内存】；oauth 类型下为空
	Label     string           // 备注（便于人工定位，如"池 #001"）
	Status    ChannelKeyStatus // 可用状态
	FailCount int              // 连续失败次数（成功即清零）

	// 以下为「调度维度」字段（迁移 0013 新增）。
	//
	// 它们只影响"从池里挑哪一把"与"何时暂时不用它"，不改变凭据的持久状态：
	//   - 权重/优先级只作用于选择顺序；
	//   - 在途/限速/冷却是运行态，随时间或请求数自动变化。
	Weight        int       // 权重（weighted_random 使用；全为 0 时退化为等概率）
	Priority      int       // 优先级（sequential 使用；数值越大越优先）
	InFlight      int       // 当前在途请求数（least_in_flight 使用）
	CooldownUntil time.Time // 冷却截止时间；零值表示无冷却
	RPMLimit      int       // 每分钟请求上限；0 表示不限速
	WindowStart   time.Time // 限速窗口起点；零值表示尚未开始计数
	WindowCount   int       // 限速窗口内已用请求数

	// 以下为「余额维度」字段（迁移 0022 新增）。
	//
	// 余额是【运营数据】，由站长人工维护（网关无法得知上游实际扣费，故不自动扣减）。
	// 它不参与任何计费计算，唯一作用是"已知耗尽时不再把请求浪费在这把凭据上"：
	//   - Balance 为 BalanceUnknown(-1) 表示"未录入"，不做任何约束；
	//   - Balance >= 0 表示已知余额，0 即"已用尽"，会被调度过滤掉（见 BalanceExhausted）；
	//   - BalanceUpdatedAt 记录该数字被人工更新的时间，便于判断是否过时。
	// 注意：余额耗尽只是"本次不选它"，不改 status——
	// 站长补录成正数后它自然会重新参与调度。
	Balance          int64     // 余额；-1 表示未知（见 BalanceUnknown）
	BalanceUpdatedAt time.Time // 余额被人工更新的时间；零值表示从未录入

	// 以下字段仅 OAuth 类型使用。
	//
	// RefreshToken / AccessToken 均为明文，仅在内存中流转，落库由仓储加密。
	RefreshToken string
	AccessToken  string
	// ExpiresAt 是 access_token 的过期时间（零值表示未知）。
	ExpiresAt time.Time
	// AccountHint 是账号标识（如邮箱），用于管理员辨认"这是谁的账号"。
	AccountHint string
	// Provider 是关联的 OAuth 提供方名字（对应 oauth_providers.name）。
	Provider string

	// 以下为「订阅账号元数据」（迁移 0028 新增）。
	//
	// 这些字段只对订阅类账号（如 ChatGPT/Codex）有意义，对 API Key 型凭据一律为空/未知。
	// 它们不是运营备注而是【转发所必需】：AccountID 缺了上游会直接拒绝请求，
	// 而额度快照决定了调度该不该跳过它。
	//
	// AccountID 是上游账号标识（chatgpt_account_id），出站时写入 chatgpt-account-id 头。
	//
	// 为什么必须逐个账号存而不能用渠道级配置：同一渠道的池里是不同人的订阅账号，
	// 用错账号 ID 的后果是上游 401/403（且报错不指向根因），极难排查。
	AccountID string
	// PlanType 是套餐标识（plus / pro / team…），仅用于展示与筛选。
	PlanType string
	// QuotaUsedPercent 是主额度窗口已用百分比；QuotaUsedPercentUnknown(-1) 表示未探测。
	//
	// 与 Balance 的分工：Balance 是站长人工维护的"钱"，QuotaUsedPercent 是
	// 从上游自动探测来的"额度窗口用量"；前者能拦"余额为 0"，后者能拦"订阅额度已满"。
	QuotaUsedPercent int
	// QuotaResetAt 是额度窗口的重置时间；零值表示未知（或不存在窗口）。
	QuotaResetAt time.Time
	// QuotaCheckedAt 是上次探测额度的时刻；零值表示从未探测。
	QuotaCheckedAt time.Time

	// 以下为「订阅账号次额度窗口」（迁移 0048 新增）。
	//
	// 上游把额度拆成两条独立的线：主窗口（5 小时）与次窗口（每周）。
	// 此前只落主窗口，站长在周额度将满时毫无预警——表现是"明明没怎么用，
	// 账号却突然全满"。这里补上次窗口，供界面同时渲染两条进度条。
	//
	// 关键约束：次窗口【不】参与调度判定（QuotaExhausted 仍只看主窗口），
	// 因此对存量数据而言升级后行为逐字不变；它纯粹是"给站长看的信息"。
	//   - QuotaSecondaryUsedPercent 为 QuotaUsedPercentUnknown(-1) 表示未探测；
	//   - 窗口秒数用于把进度条正确标注成"5 小时 / 每周"，0 表示上游未提供。
	QuotaSecondaryUsedPercent   int
	QuotaSecondaryResetAt       time.Time
	QuotaPrimaryWindowSeconds   int
	QuotaSecondaryWindowSeconds int

	// 以下为「路由分叉」字段（迁移 0038 新增）。
	//
	// 用途：同一个上游接口下挂多把凭据时，它们能服务的分组与模型往往不同
	// （免费账号只有部分模型、不同账号面向不同业务线）。
	// 这两列把差异表达在【凭据】上，而不必为此多建一个渠道：
	//   - Groups 为空 = 不限分组（继承渠道级路由结果），这是默认值；
	//   - Models 为空 = 不限模型；非空时支持尾部通配符 *（如 "gpt-4*"）。
	// 判定见 MatchesScope。
	Groups []string // 本凭据可服务的分组（空 = 不限）
	Models []string // 本凭据可服务的模型（对外模型名，空 = 不限，支持尾部 *）

	// LastUsedAt 为最近被选中使用的时间；零值表示从未使用。
	LastUsedAt time.Time
	// LastError 为最近一次失败原因（已脱敏，只记状态码与简短描述）。
	LastError string
	CreatedAt time.Time
}

// IsUsable 判断该凭据当前是否可参与轮询。
//
// 注意：本方法只反映持久状态（是否启用），不含冷却与限速——
// 后者随时间自动变化，需要传入当前时间，因此由调度器在挑选前单独判断。
func (k *ChannelKey) IsUsable() bool {
	return k.Status == ChannelKeyStatusEnabled
}

// CoolingDown 判断凭据是否处于冷却期（时间语义的临时停用）。
//
// 冷却到期即自动可用，无需任何后台动作；这与 status 的人工摘除语义完全不同。
func (k *ChannelKey) CoolingDown(now time.Time) bool {
	return !k.CooldownUntil.IsZero() && k.CooldownUntil.After(now)
}

// BalanceExhausted 判断凭据是否"已知且余额已用尽"。
//
// 判定规则：Balance >= 0（已知）且 Balance <= 0（用尽）——
// 即只有恰好为 0 时成立；BalanceUnknown(-1) 表示未录入，一律视为"不受约束"。
//
// 为什么余额耗尽不算"失败"、也不改 status：
//
//	余额是人工维护的运营数据，耗尽不是凭据本身坏了，而是"暂时没有额度了"。
//	把它当作永久失效摘除，会在站长补完余额后仍看到它被摘除、需要再手动恢复；
//	因此这里只作为调度过滤条件——补录成正数后它自然重新参与。
func (k *ChannelKey) BalanceExhausted() bool {
	return k.Balance >= 0 && k.Balance <= 0
}

// QuotaKnown 判断该凭据的额度是否已被探测过。
//
// 未探测（QuotaUsedPercent < 0）时不做任何额度约束——
// 宁可让它试一次再由上游拒绝，也不要因为"没查过"就把好账号判定为不可用。
func (k *ChannelKey) QuotaKnown() bool {
	return k.QuotaUsedPercent >= 0
}

// QuotaSecondaryKnown 判断该凭据的次额度窗口是否已被探测过。
//
// 与 QuotaKnown 同构：只有"未知"与"已知"两种语义。
// 单独提供它（而不是让调用方各自写 >= 0）是为了让 -1 这条规则只在领域层维护一处。
//
// 注意它【不】参与调度：次窗口未探测不影响可用性，只影响界面是否显示"未查询"。
func (k *ChannelKey) QuotaSecondaryKnown() bool {
	return k.QuotaSecondaryUsedPercent >= 0
}

// QuotaExhausted 判断该凭据的订阅额度是否已用满。
//
// 判定规则（两条同时成立才算用满）：
//  1. 已探测且已用百分比 >= 100；
//  2. 重置时间未知，或重置时间还没到。
//
// 第 2 条是必要的：额度是"窗口用量"，快照会过时。若重置时刻已过而我们还按旧的
// 100% 过滤，账号会在新窗口里被白白闲置——直到下一次探测才恢复。
// 把"重置时间已过"视为"额度已刷新"，可以让恢复时间不依赖探测任务的及时性。
func (k *ChannelKey) QuotaExhausted(now time.Time) bool {
	if !k.QuotaKnown() || k.QuotaUsedPercent < 100 {
		return false
	}
	if !k.QuotaResetAt.IsZero() && !k.QuotaResetAt.After(now) {
		// 重置时刻已过：快照过时，视为额度已恢复
		return false
	}
	return true
}

// QuotaResetPending 返回额度窗口的剩余时间；零值表示未知或无需等待。
//
// 用途：界面展示"约 2 小时后恢复"，让站长能预判账号何时回到池子里。
func (k *ChannelKey) QuotaResetPending(now time.Time) time.Duration {
	if k.QuotaResetAt.IsZero() {
		return 0
	}
	return k.QuotaResetAt.Sub(now)
}

// IsOAuth 判断是否为 OAuth 凭据。
func (k *ChannelKey) IsOAuth() bool {
	return k.Kind == CredentialKindOAuth
}

// MatchesGroup 判断该凭据能否服务指定分组的请求（迁移 0038）。
//
// 语义：Groups 为空表示"不限分组"，即继承渠道级路由结果——
// 这是默认值，"只给个别凭据限分组"是最常见的用法（例如池里大部分账号通用，
// 少数企业账号只供自营组），若要求每把凭据都必须写明分组，配置成本会高到没人愿意用。
//
// group 为空（未按分组过滤的调用路径）同样返回 true。
func (k *ChannelKey) MatchesGroup(group string) bool {
	if k == nil {
		return false
	}
	group = strings.TrimSpace(group)
	if group == "" || len(k.Groups) == 0 {
		return true
	}
	for _, name := range k.Groups {
		if strings.TrimSpace(name) == group {
			return true
		}
	}
	return false
}

// HasModel 判断该凭据是否声明支持指定模型（迁移 0038）。
//
// 语义：Models 为空表示"不限模型"（默认）。
// 非空时逐条按尾部通配符匹配（复用计价规则那套 PricePattern，语义一致：
// "gpt-4*" 命中 gpt-4o；模型名大小写敏感，因为上游把它当标识符）。
func (k *ChannelKey) HasModel(modelName string) bool {
	if k == nil {
		return false
	}
	if len(k.Models) == 0 {
		return true
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		// 调用方没给出模型名（如只按分组挑选的场景）：不因此排除任何凭据。
		return true
	}
	for _, pattern := range k.Models {
		if PricePattern(strings.TrimSpace(pattern)).Matches(modelName) {
			return true
		}
	}
	return false
}

// MatchesScope 判断该凭据是否可用于"分组 + 模型"这两个维度的本次请求。
//
// 这是凭据级路由的唯一入口：两个维度都是"留空即不限"，
// 因此未配置分叉的凭据在任何请求下都返回 true，行为与迁移前完全一致。
func (k *ChannelKey) MatchesScope(group, modelName string) bool {
	return k.MatchesGroup(group) && k.HasModel(modelName)
}

// NeedsRefresh 判断 OAuth 凭据是否需要在本次使用前刷新。
//
// 判定规则：
//   - 非 OAuth 凭据永不刷新；
//   - 没有 refresh_token 时不刷新（见下方说明）；
//   - access_token 为空 → 必须刷新；
//   - 剩余有效期不足 refreshAheadSeconds → 提前刷新。
func (k *ChannelKey) NeedsRefresh(now time.Time) bool {
	if !k.IsOAuth() {
		return false
	}
	// 没有 refresh_token 就"没得刷"：这类凭据（例如只导入了短期 access_token）
	// 只能用当前令牌去试，被上游拒绝后走正常的凭据失败流程（冷却而不是摘除）。
	//
	// 为什么不能在这里返回 true：刷新必然失败，调用方会把它记成
	// "刷新令牌失败"并累计失败次数，一个本来只是过期需要重导的账号
	// 会被当成坏凭据处理——那是误伤。
	if strings.TrimSpace(k.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(k.AccessToken) == "" {
		return true
	}
	if k.ExpiresAt.IsZero() {
		// 过期时间未知：保守起见按"需要刷新"处理，
		// 否则一旦令牌已失效就会持续 401（而池内其他凭据被白白浪费）
		return true
	}
	return now.Add(refreshAheadSeconds * time.Second).After(k.ExpiresAt)
}

// CredentialValue 返回"当前可直接用于上游鉴权"的凭据值。
//
// api_key 型返回 Key；oauth 型返回 AccessToken。
// 这样转发链路无需区分类型，统一取一个字符串即可。
func (k *ChannelKey) CredentialValue() string {
	if k.IsOAuth() {
		return k.AccessToken
	}
	return k.Key
}

// RevealSecret 返回该凭据的敏感原文，供超管在后台显式查看（如复制到另一台实例）。
//
// 为什么单独开一个方法而不是直接暴露字段：
//   - 两类凭据的"秘密"不是同一个字段：API Key 是 Key，订阅账号是 RefreshToken
//     （AccessToken 是短期票据，没有搬运价值）；
//   - 把"取原文"这件事集中在一个可被搜索到、可被审计的方法里，
//     比让各处随手读 k.Key 更容易发现误用。
//
// 调用方必须保证：只在管理员显式要求查看原文时使用，且不得写入日志。
func (k *ChannelKey) RevealSecret() string {
	if k == nil {
		return ""
	}
	if k.IsOAuth() {
		return k.RefreshToken
	}
	return k.Key
}

// Masked 返回脱敏后的凭据，供界面与日志展示。
//
// 脱敏规则：保留前 8 位与后 4 位。相比渠道密钥多留 2 位前缀，
// 是因为密钥池里往往有几百把同前缀（如 nvapi-）的密钥，
// 只保留 6 位前缀在界面上几乎无法区分。
//
// OAuth 凭据返回"账号标识 + 类型"而不是令牌片段：
// 令牌片段对管理员没有任何辨识价值，而账号标识能立刻告诉他是谁。
func (k *ChannelKey) Masked() string {
	if k.IsOAuth() {
		hint := strings.TrimSpace(k.AccountHint)
		if hint == "" {
			hint = "未命名账号"
		}
		return "[OAuth] " + hint
	}

	key := k.Key
	if key == "" {
		return ""
	}
	const (
		keepPrefix = 8
		keepSuffix = 4
		// minMaskableLen 见 Channel.MaskedAPIKey 的同类说明：
		// 露出的 8+4=12 个字符必须在密钥里只占一小部分，否则脱敏等于没做。
		minMaskableLen = 24
	)
	if len(key) < minMaskableLen {
		return strings.Repeat("*", len(key))
	}
	return key[:keepPrefix] + "****" + key[len(key)-keepSuffix:]
}

// KeyPoolSummary 是密钥池的概览统计（列表页展示用）。
//
// 为什么需要它：渠道列表若把每个渠道的全部密钥（可能几百把）都查出来，
// 页面数据量会大到不可用。这里只给计数，需要细节时再单独查该渠道的密钥列表。
type KeyPoolSummary struct {
	ChannelID   uint64 `json:"channel_id"`
	Total       int    `json:"total"`
	Enabled     int    `json:"enabled"`
	Disabled    int    `json:"disabled"`
	AutoRemoved int    `json:"auto_removed"`
}

// Available 返回可用密钥数。
func (s KeyPoolSummary) Available() int {
	return s.Enabled
}

// CredentialInput 描述一条待导入的凭据。
//
// 支持两种形态：
//   - API Key：填 APIKey；
//   - OAuth  ：填 RefreshToken（可选同时给 AccessToken 与 ExpiresAt）。
//
// 之所以用"一个结构体装两类"，而不是两个方法：导入路径（后台表单、批量粘贴、
// CLI）对两类凭据的处理流程完全一致，分成两个方法只会让调用点多一层判断。
type CredentialInput struct {
	Kind         CredentialKind // 必填：api_key / oauth
	APIKey       string         // kind=api_key 时必填
	RefreshToken string         // kind=oauth 时必填
	AccessToken  string         // 可选：导入时若已持有访问令牌则一并保存，省一次刷新
	ExpiresAt    time.Time      // 可选：access_token 的过期时间
	AccountHint  string         // 可选：账号标识（邮箱等）
	Provider     string         // kind=oauth 时建议填写：关联的 OAuth 提供方
	AccountID    string         // 可选：上游账号 ID（订阅类账号必需，如 chatgpt_account_id）
	PlanType     string         // 可选：套餐标识（plus / pro / team…），仅用于展示
	Label        string         // 可选：备注
	// Balance 是人工录入的余额（迁移 0022）；BalanceUnknown(-1) 表示未知。
	//
	// 注意零值陷阱：Go 的零值是 0，而 0 在余额语义里表示"已知且已用尽"。
	// 因此调用方【必须显式赋值】——没有余额信息时写 BalanceUnknown，切勿留 0，
	// 否则新导入的凭据会被误判为"余额耗尽"而立即退出调度。
	Balance int64
}

// IdentityHash 返回该凭据的去重标识。
//
// 规则：API Key 用密钥本身；OAuth 优先用 refresh_token（它是账号的长期唯一标识，
// access_token 每次刷新都变），没有 refresh_token 时退化为用 access_token。
//
// 退化分支不可省略：只导入了短期令牌的账号若都按"空 refresh_token"计算摘要，
// 会被整体判为重复，导入 N 个只留下 1 个。
func (c CredentialInput) IdentityHash(sha256Hex func(string) string) string {
	if c.Kind == CredentialKindOAuth {
		if token := strings.TrimSpace(c.RefreshToken); token != "" {
			return sha256Hex(token)
		}
		return sha256Hex("access:" + strings.TrimSpace(c.AccessToken))
	}
	return sha256Hex(strings.TrimSpace(c.APIKey))
}

// Validate 校验凭据是否可用。
func (c CredentialInput) Validate() error {
	if !c.Kind.IsValid() {
		return fmt.Errorf("凭据类型非法: %q", c.Kind)
	}
	switch c.Kind {
	case CredentialKindAPIKey:
		if strings.TrimSpace(c.APIKey) == "" {
			return errors.New("API Key 不能为空")
		}
	case CredentialKindOAuth:
		// 两种 OAuth 形态都接受：带 refresh_token（可自动续期）
		// 或只带 access_token（短期有效，过期后需重导）。
		if strings.TrimSpace(c.RefreshToken) == "" && strings.TrimSpace(c.AccessToken) == "" {
			return errors.New("OAuth 凭据必须提供 refresh_token 或 access_token")
		}
	}
	return nil
}

// ParseCredentialList 解析批量粘贴的订阅账号凭据文本。
//
// 支持两种形态（可只用其中一种）：
//
//	形态一：JSON —— Codex CLI 导出的 auth.json、JSON 数组、或每行一个 JSON 对象。
//	  例：{"tokens":{"access_token":"...","refresh_token":"...","account_id":"..."}}
//	  字段名兼容驼峰与下划线两种写法，账号标识与套餐会从令牌里自动补齐，
//	  因此使用者只需粘贴文件内容，不必手工拆字段。
//
//	形态二：纯文本行 —— 每行 "refresh_token [账号标识]"。
//	  支持空格、制表符或逗号分隔；以 # 开头的行视为注释；空行忽略；重复项自动去重。
//
// 为什么不只留纯文本：站长手上有的是 Codex CLI 导出的 auth.json，
// 要求他先把 refresh_token 抽出来再粘贴，既费事又极易粘错一个字符——
// 而粘错的表现是"账号莫名 401"，排查成本极高。
//
// provider 会写入每条凭据，便于刷新时找到对应的 OAuth 提供方配置。
func ParseCredentialList(raw, provider string) []CredentialInput {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	if inputs, ok := parseCodexJSONCredentials(trimmed, provider); ok && len(inputs) > 0 {
		return inputs
	}

	tokens, labels := ParseKeyList(raw)
	inputs := make([]CredentialInput, 0, len(tokens))
	for i, token := range tokens {
		label := ""
		if i < len(labels) {
			label = labels[i]
		}
		inputs = append(inputs, CredentialInput{
			Kind:         CredentialKindOAuth,
			RefreshToken: token,
			// 备注同时也是账号标识：使用者粘贴时写的通常就是账号邮箱，
			// 直接当作 AccountHint 可以让池列表立刻可读，省去手工再填一遍。
			AccountHint: label,
			Label:       label,
			Provider:    provider,
			// OAuth 账号的余额是上游订阅额度，网关无法得知，统一按"未知"处理。
			Balance: BalanceUnknown,
		})
	}
	return inputs
}

// parseCodexJSONCredentials 解析 JSON 形态的订阅账号凭据。
//
// 第二个返回值为 false 表示"这段文本不是 JSON"（调用方应回退到按行解析）。
// 之所以用 json.Decoder 循环解码而不是一次 Unmarshal：
// 实际文件里常见"多个 JSON 对象首尾相连"与 JSONL（每行一个对象）两种写法，
// 循环解码能把它们一并读出来。
func parseCodexJSONCredentials(raw, provider string) ([]CredentialInput, bool) {
	if !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
		return nil, false
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	inputs := make([]CredentialInput, 0, 4)
	decodedAny := false

	for {
		var value any
		if err := decoder.Decode(&value); err != nil {
			break
		}
		decodedAny = true
		for _, entry := range flattenCredentialEntries(value) {
			if input, ok := credentialFromJSONEntry(entry, provider); ok {
				inputs = append(inputs, input)
			}
		}
	}

	if !decodedAny {
		return nil, false
	}
	return inputs, true
}

// flattenCredentialEntries 把"一个 JSON 值"摊平成若干账号对象。
//
// 支持形态：单个对象、对象数组、以及 { "accounts": [...] } 这类包一层的写法。
func flattenCredentialEntries(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		// 常见包装：把账号清单放在 accounts / items / data 里
		for _, key := range []string{"accounts", "items", "data"} {
			if list, ok := typed[key].([]any); ok {
				return flattenCredentialEntries(list)
			}
		}
		return []map[string]any{typed}
	case []any:
		entries := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				entries = append(entries, object)
			}
		}
		return entries
	default:
		return nil
	}
}

// credentialFromJSONEntry 从一个 JSON 对象里抽取一条订阅账号凭据。
//
// 字段名兼容多种写法（tokens.access_token / access_token / accessToken 等）：
// 这些都是社区工具实际导出过的形态，只认一种会让一半人的文件导不进来。
// 账号标识与套餐在缺失时会从 id_token / access_token 的 JWT 里补齐。
func credentialFromJSONEntry(entry map[string]any, provider string) (CredentialInput, bool) {
	tokens, _ := entry["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}

	accessToken := firstNonEmptyString(
		stringValue(tokens, "access_token"), stringValue(tokens, "accessToken"),
		stringValue(entry, "access_token"), stringValue(entry, "accessToken"),
		stringValue(entry, "token"),
	)
	refreshToken := firstNonEmptyString(
		stringValue(tokens, "refresh_token"), stringValue(tokens, "refreshToken"),
		stringValue(entry, "refresh_token"), stringValue(entry, "refreshToken"),
	)
	idToken := firstNonEmptyString(
		stringValue(tokens, "id_token"), stringValue(tokens, "idToken"),
		stringValue(entry, "id_token"), stringValue(entry, "idToken"),
	)
	if accessToken == "" && refreshToken == "" {
		// 既没有访问令牌也没有刷新令牌：这条不是账号，跳过而不是报错——
		// 导出文件里常混着无关字段（时间戳、版本号），报错会让整批导入失败。
		return CredentialInput{}, false
	}

	accountID := firstNonEmptyString(
		stringValue(entry, "chatgpt_account_id"), stringValue(entry, "chatgptAccountId"),
		stringValue(entry, "account_id"), stringValue(entry, "accountId"),
		stringValue(tokens, "account_id"), stringValue(tokens, "accountId"),
	)
	planType := firstNonEmptyString(
		stringValue(entry, "plan_type"), stringValue(entry, "planType"),
		stringValue(tokens, "plan_type"), stringValue(tokens, "planType"),
	)
	if nested, ok := entry["account"].(map[string]any); ok {
		accountID = firstNonEmptyString(
			accountID,
			stringValue(nested, "id"), stringValue(nested, "account_id"), stringValue(nested, "chatgpt_account_id"),
		)
	}

	// 从令牌里补齐账号标识、套餐与过期时间：让使用者不必手工查找这些字段。
	for _, token := range []string{idToken, accessToken} {
		if token == "" {
			continue
		}
		tokenAccountID, tokenPlanType, ok := DecodeCodexTokenClaims(token)
		if !ok {
			continue
		}
		if accountID == "" {
			accountID = tokenAccountID
		}
		if planType == "" {
			planType = tokenPlanType
		}
	}

	accountHint := firstNonEmptyString(
		stringValue(entry, "email"), stringValue(entry, "label"),
		stringValue(entry, "name"), stringValue(entry, "account_hint"),
	)
	label := firstNonEmptyString(stringValue(entry, "label"), accountHint)

	input := CredentialInput{
		Kind:         CredentialKindOAuth,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		AccountHint:  accountHint,
		AccountID:    accountID,
		PlanType:     planType,
		Label:        label,
		Provider:     provider,
		Balance:      BalanceUnknown,
	}
	// 令牌自带过期时间时用它，省去一次"导入即过期"的误判
	if expires := CodexTokenExpiry(accessToken); expires > 0 {
		input.ExpiresAt = time.Unix(expires, 0)
	}
	return input, true
}

// stringValue 读取对象里的字符串字段（非字符串一律视为不存在）。
func stringValue(entry map[string]any, key string) string {
	value, ok := entry[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// QuotaWindows 是一次额度探测的完整结果（主 / 次两个窗口 + 探测时刻）。
//
// 为什么用一个结构体承载，而不是把参数排成一长串：
//
//	探测结果有 7 个值，且「已用百分比(int) / 重置时间(time.Time) / 窗口秒数(int)」
//	的类型在按位置传参时极易串位（编译能过、语义出错，且这种错很难在评审时看出来）。
//	结构体让调用点自带字段名，落库实现也能逐字段对齐，不会静默把主窗口的值写进次窗口。
type QuotaWindows struct {
	// PrimaryUsedPercent 主窗口已用百分比；QuotaUsedPercentUnknown 表示未探测。
	PrimaryUsedPercent int
	// PrimaryResetAt 主窗口重置时间；零值表示未知。
	PrimaryResetAt time.Time
	// PrimaryWindowSeconds 主窗口时长（秒）；0 表示上游未提供。
	PrimaryWindowSeconds int
	// SecondaryUsedPercent 次窗口已用百分比；QuotaUsedPercentUnknown 表示未探测。
	SecondaryUsedPercent int
	// SecondaryResetAt 次窗口重置时间；零值表示未知。
	SecondaryResetAt time.Time
	// SecondaryWindowSeconds 次窗口时长（秒）；0 表示上游未提供。
	SecondaryWindowSeconds int
	// CheckedAt 本次探测时刻；零值时由落库实现补当前时间。
	CheckedAt time.Time
}

// ChannelKeyRepository 定义密钥池的持久化操作。
//
// 约定：所有方法的实现都必须保证密钥落库加密、读取解密；
// 未找到时返回 ErrChannelKeyNotFound。
type ChannelKeyRepository interface {
	// ReplaceAll 用给定密钥集合整体替换某渠道的密钥池。
	//
	// 语义（幂等）：已存在的密钥（按摘要匹配）保留其状态与统计，只新增缺失的、
	// 删除不在集合中的。因此"编辑渠道时重新粘贴同一批密钥"不会重置统计数据，
	// 也不会产生重复记录。
	//
	// labels 与 keys 一一对应（可为 nil，此时备注留空）。
	// 返回 added（新增数）、removed（删除数）。
	ReplaceAll(ctx context.Context, channelID uint64, keys, labels []string) (added, removed int, err error)

	// ReplaceAllWithBalance 与 ReplaceAll 语义一致，额外为每把密钥录入余额。
	//
	// balances 与 keys 一一对应；为 nil 或长度不足时的对应项取 BalanceUnknown。
	//
	// 余额的更新规则（防误覆盖，务必保持）：
	//   - 新增的密钥：写入本次提供的余额（未知则写 BalanceUnknown）；
	//   - 已存在的密钥：默认保留其人工录入的余额——重新粘贴同一批密钥
	//     不会把已知余额覆盖为"未知"；仅当本次【显式提供了已知余额（>=0）】
	//     时才更新为本次的值。
	ReplaceAllWithBalance(ctx context.Context, channelID uint64, keys, labels []string, balances []int64) (added, removed int, err error)

	// ReplaceCredentials 用给定凭据集合整体替换某渠道的凭据池。
	//
	// 与 ReplaceAll 的关系：ReplaceAll 是"纯 API Key 池"的便捷入口，
	// 本方法支持两类凭据混装（API Key + OAuth 订阅账号）。
	// 去重标识：api_key 用密钥摘要，oauth 用 refresh_token 摘要
	// （refresh_token 是 OAuth 凭据的长期唯一标识）。
	ReplaceCredentials(ctx context.Context, channelID uint64, inputs []CredentialInput) (added, removed int, err error)

	// UpdateTokens 回写刷新后的 OAuth 令牌。
	//
	// refreshToken 为空时保留原值：部分平台的刷新响应不回传新的 refresh_token，
	// 此时不应把它清空（清空等于永久丢失该账号）。
	UpdateTokens(ctx context.Context, id uint64, accessToken string, expiresAt time.Time, refreshToken string) error

	// ListByChannel 列出某渠道的全部密钥（含已摘除），按 ID 升序。
	ListByChannel(ctx context.Context, channelID uint64) ([]*ChannelKey, error)

	// ListUsable 列出某渠道中状态为"启用"的密钥，供转发时轮询选择。
	ListUsable(ctx context.Context, channelID uint64) ([]*ChannelKey, error)

	// Summary 批量返回多个渠道的密钥池概览，避免列表页出现 N+1 查询。
	Summary(ctx context.Context, channelIDs []uint64) (map[uint64]KeyPoolSummary, error)

	// DeleteByChannel 删除某渠道的全部密钥（渠道被删除时调用）。
	DeleteByChannel(ctx context.Context, channelID uint64) error

	// MarkUsed 记录密钥被选中使用（用于展示"最近使用时间"）。
	MarkUsed(ctx context.Context, id uint64, at time.Time) error

	// MarkSuccess 记录一次成功：清零连续失败计数。
	MarkSuccess(ctx context.Context, id uint64) error

	// MarkFailure 记录一次失败：累加连续失败计数，达到 KeyAutoRemoveThreshold 时自动摘除。
	MarkFailure(ctx context.Context, id uint64, reason string) error

	// UpdateStatus 手动修改密钥状态（启用 / 禁用 / 重新启用被摘除的密钥）。
	UpdateStatus(ctx context.Context, id uint64, status ChannelKeyStatus) error

	// ---- 以下为调度维度（迁移 0013）的方法 ----

	// ChannelKeyStrategy 读取渠道的凭据调度策略与轮询游标。
	//
	// 渠道不存在时返回默认策略与游标 0（不报错）：策略只影响"挑哪一把"，
	// 读不到就退化为默认，不应成为转发链路的单点故障。
	ChannelKeyStrategy(ctx context.Context, channelID uint64) (KeyStrategy, int64, error)

	// SetChannelStrategy 设置渠道的凭据调度策略（校验失败返回错误）。
	SetChannelStrategy(ctx context.Context, channelID uint64, strategy KeyStrategy) error

	// AdvanceKeyCursor 持久化轮询游标（round_robin 每次选中后自增写回）。
	//
	// 持久化而非只放内存：进程重启后轮询位置不归零，
	// 否则每次重启都会反复压在前几把凭据上。
	AdvanceKeyCursor(ctx context.Context, channelID uint64, cursor int64) error

	// Acquire 记录一次在途占用（in_flight + 1）。
	Acquire(ctx context.Context, id uint64) error

	// Release 释放在途占用（in_flight - 1）。
	//
	// 必须可【安全重复调用】：调用方在重试与异常分支上可能重复释放，
	// 实现需保证计数不会被减到负数。
	Release(ctx context.Context, id uint64) error

	// SetCooldown 设置冷却截止时间与失败原因，并累加连续失败计数。
	//
	// until 为零值时表示清除冷却。冷却到期即自动恢复，无需额外动作。
	SetCooldown(ctx context.Context, id uint64, until time.Time, reason string) error

	// MarkPermanentFailure 记录一次"凭据被明确判定为永久无效"：置为自动摘除状态。
	//
	// 只有在上游明确表示该凭据永久不可用时才调用，不要用它表达临时失败。
	MarkPermanentFailure(ctx context.Context, id uint64, reason string) error

	// RecordRequest 记录一次请求，用于 RPM 固定窗口计数（window 为窗口时长）。
	RecordRequest(ctx context.Context, id uint64, at time.Time, window time.Duration) error

	// UpdateScheduling 更新凭据的调度参数（权重 / 优先级 / RPM 上限）。
	UpdateScheduling(ctx context.Context, id uint64, weight, priority, rpmLimit int) error

	// ---- 以下为凭据级路由分叉（迁移 0038）的方法 ----

	// UpdateRouting 更新凭据可服务的分组与模型（空切片 = 不限，即继承渠道配置）。
	//
	// 之所以整组替换而不是增量增删：后台是"一把凭据一个表单"的编辑方式，
	// 整组替换能保证"界面所见 = 落库结果"，也不会因并发编辑留下孤儿配置。
	UpdateRouting(ctx context.Context, id uint64, groups, models []string) error

	// UpdateBalance 人工更新某把凭据的余额，并记录更新时间（balance_updated_at）。
	//
	// balance 取值：BalanceUnknown(-1) 表示置为"未知"；>=0 设为该值（0 即视为已用尽）。
	// 余额是运营数据、不参与计费，只影响"是否还把这把凭据纳入调度"。
	UpdateBalance(ctx context.Context, id uint64, balance int64) error

	// ---- 以下为订阅账号元数据（迁移 0028）的方法 ----

	// UpdateAccountMeta 写入账号级元数据（account_id / plan_type）。
	//
	// 空串表示"本次没有新信息"，对应列保持原值——刷新响应常常不回传 plan_type，
	// 无条件写入会把已知套餐抹空。
	UpdateAccountMeta(ctx context.Context, id uint64, accountID, planType string) error

	// UpdateQuota 写入额度探测结果（主 / 次两个窗口一起写）；某个窗口的已用百分比传
	// QuotaUsedPercentUnknown 表示"该窗口置回未知"。
	//
	// 为什么两个窗口必须一次写入：它们来自同一次探测，分开写会在两次写入之间
	// 留下"主窗口已更新、次窗口还是旧值"的不一致快照，界面会显示自相矛盾的数据。
	// resetAt 与 checkedAt 一并写入（不做"空值保留"）：额度窗口本身会滚动，
	// 保留旧的重置时间会让"额度是否已恢复"的判断依据过期数据。
	//
	// 注意：本方法只落库，不改变调度判定——QuotaExhausted 仍以主窗口为准。
	UpdateQuota(ctx context.Context, id uint64, windows QuotaWindows) error
}

// ErrChannelKeyNotFound 表示密钥不存在。
var ErrChannelKeyNotFound = errors.New("model: 渠道密钥不存在")

// ParseKeyList 把多行文本解析为密钥列表。
//
// 支持的输入形式（后台"批量粘贴"入口使用）：
//   - 每行一把密钥；
//   - 允许行内带备注，用空白或逗号分隔："nvapi-xxx 池#1" 或 "nvapi-xxx,池#1"；
//   - 忽略空行与以 # 开头的注释行；
//   - 自动去重（保留首次出现的顺序）。
//
// 返回 keys 为密钥正文，labels 与之一一对应（无备注时为空字符串）。
//
// 为什么把解析放在领域层：后台导入、CLI 导入、测试都走同一套解析规则，
// 避免"页面上能导入、脚本里导入格式却不一致"这类割裂。
func ParseKeyList(raw string) (keys []string, labels []string) {
	seen := make(map[string]struct{})

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 分隔符：优先按逗号切分（明确），否则按空白切分（宽松）
		key, label := splitKeyAndLabel(line)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		labels = append(labels, label)
	}
	return keys, labels
}

// splitKeyAndLabel 从一行文本中拆出密钥与备注。
//
// 规则：逗号优先级高于空白（因为密钥本身不含逗号，而备注常带空格）。
func splitKeyAndLabel(line string) (key, label string) {
	if idx := strings.IndexAny(line, ",，\t"); idx >= 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
	}
	if idx := strings.Index(line, " "); idx >= 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
	}
	return line, ""
}

// ParseKeyListWithBalance 解析"带余额标记"的批量密钥文本。
//
// 相比 ParseKeyList，它额外识别「余额标记行」，让站长可以把
// "余额不同的一批密钥"按段粘贴，而无需在每把密钥上重复写余额。
//
// 支持的实际粘贴格式（这是本函数的核心易用性目标）：
//
//	49余额
//	sk-aaaaaaaa
//	62余额
//	sk-bbbbbbbb
//	55余额
//	sk-cccccccc
//	sk-dddddddd
//
// 规则：
//  1. 单独一行若形如「<整数>余额」「余额<整数>」「余额: <整数>」「余额 <整数>」
//     （允许前后空白与全角冒号「：」），则该行是"余额标记"，为【其后】的密钥
//     设定余额；同一标记持续生效，直到遇到下一个标记。
//  2. 其它非空、非注释行按 ParseKeyList 的规则解析（密钥 + 可选备注，
//     备注用逗号 / 制表符 / 空白分隔）。
//  3. 任何余额标记之前出现的密钥（没有任何标记生效）余额为 BalanceUnknown。
//  4. 忽略空行与以 # 开头的注释行；重复密钥自动去重（保留首次出现的余额）。
//
// 返回 keys / labels / balances 三者一一对应。
//
// 关于"行内直接带余额"（形如 `sk-xxx 49`）的取舍——刻意【不支持】：
//
//	这种写法与"密钥 + 数字备注"（如 `sk-xxx 49` 表示第 49 号密钥）
//	在文本上完全无法区分，任何猜测都可能把一条合法的数字备注误当成余额，
//	从而静默改变已录数据的语义。既然①的余额标记写法已经足够清晰且无歧义，
//	就让"对号入座"这件事交给更明确的标记形式，避免歧义。
func ParseKeyListWithBalance(raw string) (keys []string, labels []string, balances []int64) {
	seen := make(map[string]struct{})
	// current 是当前生效的余额标记；初始为"未知"，即第一个标记之前的密钥不受约束。
	current := BalanceUnknown

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 余额标记行：只更新"其后密钥的余额"，本身不是密钥。
		if value, ok := parseBalanceMarker(line); ok {
			current = value
			continue
		}

		key, label := splitKeyAndLabel(line)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		labels = append(labels, label)
		balances = append(balances, current)
	}
	return keys, labels, balances
}

// parseBalanceMarker 判断一行文本是否为余额标记，并返回其中的余额。
//
// 接受四种写法（允许前后空白；"余额"与数字之间允许半角或全角冒号）：
//
//	49余额 / 余额49 / 余额: 49 / 余额 49
//
// 数字部分必须是纯数字（不允许正负号、小数、千分位），避免把密钥或备注误判为标记。
func parseBalanceMarker(line string) (int64, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0, false
	}

	// 形式一：余额在前 —— 余额49 / 余额: 49 / 余额：49 / 余额 49
	if rest, ok := strings.CutPrefix(trimmed, "余额"); ok {
		rest = strings.TrimSpace(rest)
		rest = strings.TrimLeft(rest, ":：")
		rest = strings.TrimSpace(rest)
		if value, ok := parseNonNegativeInt(rest); ok {
			return value, true
		}
		return 0, false
	}

	// 形式二：余额在后 —— 49余额
	if rest, ok := strings.CutSuffix(trimmed, "余额"); ok {
		rest = strings.TrimSpace(rest)
		if value, ok := parseNonNegativeInt(rest); ok {
			return value, true
		}
	}

	return 0, false
}

// parseNonNegativeInt 解析一个非负整数；含任何非数字字符即视为不成立。
//
// 之所以不用 strconv.Atoi 直接判断：Atoi 会接受 "+5" / "-3" 这类带符号写法，
// 而余额标记里出现符号说明它更可能是别的东西（如密钥片段），应当让它走普通解析。
func parseNonNegativeInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// PickKey 从可用密钥中挑一把（随机）。
//
// 为什么用随机而不是"严格轮询"（按 last_used_at 排序取最早）：
//   - 并发场景下"读时间 → 排序 → 写时间"存在竞态，多个请求会选中同一把；
//   - 随机选择在请求量较大时同样接近均匀分布，且无锁、无状态、实现简单。
//
// 池内只有一把时直接返回，省去随机数开销（这是最常见的情况）。
func PickKey(keys []*ChannelKey) *ChannelKey {
	switch len(keys) {
	case 0:
		return nil
	case 1:
		return keys[0]
	}
	return keys[rand.IntN(len(keys))]
}
