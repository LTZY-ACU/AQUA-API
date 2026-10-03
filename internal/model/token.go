// 本文件定义「访问令牌（Token）」领域模型。
//
// 意图（Why）：
//
//	令牌是网关对外的"下游凭证"：使用者拿它调用网关，网关据此判断
//	  「你是谁、能不能用、能用哪些模型、还有多少额度」。
//	把判定规则放在领域层，可以保证无论从哪条路径（后台 API、批量导入、命令行）
//	创建令牌，都遵守同一套约束。
//
// 流转（Flow）：
//
//	internal/store 实现 TokenRepository
//	  ├─ 写入：Validate → 计算 key_hash（查找用）+ key_enc（展示用）→ 落库
//	  └─ 读取：按 key_hash 或 id 取出 → 解密 key → 返回领域对象
//	internal/server 的鉴权中间件调用 GetByKey 完成校验
//
// 扩展（Extend）：
//
//	新增令牌属性（如 IP 白名单、RPM 限制）时：
//	  1) 在此结构体加字段并补 Validate 规则；
//	  2) 新建迁移脚本（如 0003_token_xxx.sql）加列——切勿修改已发布的脚本；
//	  3) 同步更新 internal/store/token_repo.go 的列清单与扫描逻辑。
package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrTokenNotFound 表示未找到指定令牌。
var ErrTokenNotFound = errors.New("model: 令牌不存在")

// 令牌 KEY 的格式约定。
const (
	// TokenKeyPrefix 是令牌 KEY 的固定前缀，便于使用者一眼识别这是网关令牌。
	TokenKeyPrefix = "sk-"
	// tokenKeyRandomBytes 是 KEY 的随机部分长度（字节）。
	// 24 字节 = 192 位熵，十六进制编码后为 48 个字符，暴力枚举不可行。
	tokenKeyRandomBytes = 24
)

// TokenStatus 表示令牌状态。
//
// 取值与数据库 tokens.status 一一对应，禁止改动已落库的数值。
type TokenStatus int

const (
	// TokenStatusEnabled 启用：可正常调用。
	TokenStatusEnabled TokenStatus = 1
	// TokenStatusDisabled 手动禁用：由管理员关闭，不因时间或额度自动恢复。
	TokenStatusDisabled TokenStatus = 2
	// TokenStatusExpired 已过期：超过 expires_at 后由系统判定。
	TokenStatusExpired TokenStatus = 3
	// TokenStatusExhausted 额度耗尽：非不限额度且剩余额度不足时由系统判定。
	TokenStatusExhausted TokenStatus = 4
)

// String 返回状态的中文名，便于日志与后台展示。
func (s TokenStatus) String() string {
	switch s {
	case TokenStatusEnabled:
		return "启用"
	case TokenStatusDisabled:
		return "手动禁用"
	case TokenStatusExpired:
		return "已过期"
	case TokenStatusExhausted:
		return "额度耗尽"
	default:
		return fmt.Sprintf("未知(%d)", int(s))
	}
}

// IsValid 判断状态取值是否合法。
func (s TokenStatus) IsValid() bool {
	switch s {
	case TokenStatusEnabled, TokenStatusDisabled, TokenStatusExpired, TokenStatusExhausted:
		return true
	default:
		return false
	}
}

// 令牌「周期预算」的周期取值。
//
// 存库为小写字符串（见迁移 0043），便于后台直接展示与做多语言文案。
// 窗口口径统一为"自窗口起点起的相对时长"（日=1 天、周=7 天、月=1 个自然月），
// 而不是自然日/自然周的零点：这样各令牌的重置时刻天然分散，
// 不会在每天零点形成全站同时重置的写尖峰，也无需处理时区与周边界。
const (
	// BudgetPeriodDaily 每日窗口（自窗口起点起 1 天）。
	BudgetPeriodDaily = "daily"
	// BudgetPeriodWeekly 每周窗口（自窗口起点起 7 天）。
	BudgetPeriodWeekly = "weekly"
	// BudgetPeriodMonthly 每月窗口（自窗口起点起 1 个自然月）。
	BudgetPeriodMonthly = "monthly"
)

// IsValidBudgetPeriod 判断预算周期取值是否合法。
//
// 空串表示"不启用预算"，不算合法周期——调用方需自行区分""与非法值。
func IsValidBudgetPeriod(period string) bool {
	switch period {
	case BudgetPeriodDaily, BudgetPeriodWeekly, BudgetPeriodMonthly:
		return true
	default:
		return false
	}
}

// budgetWindowEnd 返回以 start 为起点的窗口结束时刻（右开区间）。
//
// period 非法时返回 start 本身（等价于"零长度窗口"）；调用方应先经
// IsValidBudgetPeriod 过滤，此处只为让函数在任意输入下都有确定行为。
func budgetWindowEnd(start time.Time, period string) time.Time {
	switch period {
	case BudgetPeriodDaily:
		return start.AddDate(0, 0, 1)
	case BudgetPeriodWeekly:
		return start.AddDate(0, 0, 7)
	case BudgetPeriodMonthly:
		return start.AddDate(0, 1, 0)
	default:
		return start
	}
}

// BudgetDecision 是一次「周期预算」判定的结果（纯值对象，便于测试与日志）。
type BudgetDecision struct {
	// Enabled 表示该令牌是否启用了预算闸门；为 false 时其余字段无意义，
	// 调用方应直接放行——这与引入预算能力前的行为逐字一致。
	Enabled bool
	// Exceeded 表示当前窗口已消耗是否已达到预算上限（仅在 Enabled 且非 NeedReset 时有意义）。
	Exceeded bool
	// NeedReset 表示窗口尚未锚定或已过期，调用方应把窗口重置为 WindowStart
	// 并把基线对齐到当前 used_quota；本窗口已消耗随之归零，本次判定视为未超限。
	NeedReset bool
	// WindowStart 是判定后应生效的窗口起点（NeedReset 时为 now，否则为既有起点）。
	WindowStart time.Time
	// Used 是当前窗口已消耗（= used_quota − 窗口基线，负数按 0 处理）。
	Used int64
	// Limit 是窗口预算上限（= budget_quota）。
	Limit int64
}

// Token 表示一个访问令牌。
//
// 安全约定（与 Channel 一致）：
//
//	Key 字段在内存中是【明文】，落库时由仓储加密（key_enc）并另存摘要（key_hash）。
//	对外输出必须使用 MaskedKey()，绝不直接序列化本结构体。
type Token struct {
	ID      uint64 // 主键
	OwnerID uint64 // 归属用户 ID（0 = 系统令牌，M2 尚无用户体系）
	Name    string // 名称，便于区分用途（如 "CI 构建用"）
	Key     string // 令牌明文【仅内存】，形如 sk-<48位十六进制>

	Status TokenStatus // 状态

	// ExpiresAt 为过期时间；零值表示永不过期（落库为 0）。
	ExpiresAt time.Time

	RemainQuota    int64 // 剩余额度（内部单位）
	UnlimitedQuota bool  // 是否不限额度（为 true 时忽略 RemainQuota）
	UsedQuota      int64 // 已用额度（内部单位，累计值）

	// ── 周期预算（滚动窗口，迁移 0043）────────────────────────
	//
	// 为什么需要它：RemainQuota 是"总量墙"，挡不住一把令牌在短期内把整月额度跑穿。
	// BudgetQuota 是与之【并列】的"周期墙"：每个周期内最多消耗这么多额度，
	// 周期一到自动翻篇（惰性重置，不依赖定时任务）。
	//
	// 为什么用可重置的周期窗口而不是累计上限：累计上限会让长期用户"用完即死"，
	// 一旦某个月用满就再也无法调用；周期窗口既压住短期暴冲，又不惩罚长期正常使用。

	// BudgetQuota 是周期预算额度（内部单位）；0 表示不限（默认，存量行为不变）。
	BudgetQuota int64
	// BudgetPeriod 是预算周期：daily / weekly / monthly；空串表示不启用预算。
	BudgetPeriod string
	// BudgetWindowStart 是当前窗口起点；零值表示尚未锚定（首次使用该能力时惰性写入 now）。
	BudgetWindowStart time.Time
	// BudgetWindowBase 是窗口起点时刻的 UsedQuota 快照（窗口基线）。
	//
	// 本窗口已消耗 = UsedQuota − BudgetWindowBase（负数按 0 处理）。
	// 之所以取"基线差"而不是单列一个自增计数器：令牌额度有响应后扣费、请求前预扣、
	// 结算退补三条写入路径，复用 UsedQuota 的增量可让三条路径"自动"计入本窗口消耗，
	// 无需逐一改造（否则漏改一条就会让周期预算静默失效）。
	BudgetWindowBase int64

	// Models 是允许使用的模型白名单；为空表示不限制。
	Models []string

	// GroupName 是令牌的所属分组标识（小写）。
	//
	// 语义：为空表示"使用网关默认分组"（保持历史令牌行为不变）；
	// 非空时，该令牌的请求只在"此分组的渠道"里路由，并只按"此分组的价格与倍率"计费。
	// 转发层请统一调用 EffectiveGroupName 取实际分组，不要自行判空。
	GroupName string

	CreatedAt time.Time
	UpdatedAt time.Time

	// LastUsedAt 是最近一次使用时间；零值表示从未使用。
	//
	// 用途：帮助使用者辨认"哪些 key 还在用、哪些可以清理"，也便于发现异常调用。
	LastUsedAt time.Time
}

// GenerateTokenKey 生成一个新的令牌 KEY，形如 sk-<48 位十六进制>。
//
// 使用 crypto/rand：必须用密码学安全随机源，否则令牌可被预测（严重安全问题）。
func GenerateTokenKey() (string, error) {
	buf := make([]byte, tokenKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("model: 生成令牌随机数失败: %w", err)
	}
	return TokenKeyPrefix + hex.EncodeToString(buf), nil
}

// Validate 校令令牌字段合法性，供创建与更新时调用。
func (t *Token) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("令牌名称不能为空")
	}

	// KEY 校验：既接受由 GenerateTokenKey 生成的标准格式，也拒绝明显异常的输入
	if !strings.HasPrefix(t.Key, TokenKeyPrefix) {
		return fmt.Errorf("令牌 KEY 必须以 %q 开头", TokenKeyPrefix)
	}
	if len(t.Key) <= len(TokenKeyPrefix) {
		return errors.New("令牌 KEY 缺少随机部分")
	}

	if !t.Status.IsValid() {
		return fmt.Errorf("令牌状态非法: %d", int(t.Status))
	}

	// 分组校验：空表示使用默认分组；非空时必须是合法的小写分组标识
	if err := t.ValidateGroup(); err != nil {
		return err
	}

	// 额度校验：不限额度时忽略剩余额度；否则剩余额度不能为负
	if !t.UnlimitedQuota && t.RemainQuota < 0 {
		return fmt.Errorf("令牌剩余额度不能为负数: %d", t.RemainQuota)
	}
	if t.UsedQuota < 0 {
		return fmt.Errorf("令牌已用额度不能为负数: %d", t.UsedQuota)
	}

	// 周期预算校验：
	//   - 预算额度与窗口基线不能为负；
	//   - 填了周期就必须是已知取值，否则预算会"看起来配了却不生效"；
	//   - 开了预算（quota > 0）却不给周期，同样是无意义的配置，直接拒绝。
	if t.BudgetQuota < 0 {
		return fmt.Errorf("令牌周期预算额度不能为负数: %d", t.BudgetQuota)
	}
	if t.BudgetWindowBase < 0 {
		return fmt.Errorf("令牌预算窗口基线不能为负数: %d", t.BudgetWindowBase)
	}
	if t.BudgetPeriod != "" && !IsValidBudgetPeriod(t.BudgetPeriod) {
		return fmt.Errorf("令牌预算周期非法: %q（可选 daily/weekly/monthly）", t.BudgetPeriod)
	}
	if t.BudgetQuota > 0 && t.BudgetPeriod == "" {
		return errors.New("令牌启用周期预算（budget_quota > 0）时必须指定 budget_period")
	}

	return nil
}

// ValidateGroup 校验令牌所属分组标识是否合法。
//
// 规则与 ModelGroup.Validate 的口径一致（分组名会被渠道与价格表按字符串引用，
// 两处必须用同一套规则，否则会出现"能建分组却建不了指向它的令牌"）。
func (t *Token) ValidateGroup() error {
	return ValidateGroupName(t.GroupName)
}

// ValidateGroupName 校验分组标识的合法性（供令牌与其它领域对象复用）。
//
// 口径（与 ModelGroup.Validate 保持一致）：
//   - 空串合法：表示"使用网关默认分组"，由 EffectiveGroupName 负责回退；
//   - 必须全小写：否则 "VIP" 与 "vip" 会被当成两个分组，排查成本极高；
//   - 不含空格、逗号、斜杠：这些字符会让"分组"在配置与日志里被拆开；
//   - 长度不超过 64 个字符。
func ValidateGroupName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil
	}
	if trimmed != strings.ToLower(trimmed) {
		return fmt.Errorf("分组标识只能使用小写字母（当前为 %q）", trimmed)
	}
	if strings.ContainsAny(trimmed, " \t\n/\\,，") {
		return fmt.Errorf("分组标识不能包含空格、逗号或斜杠（当前为 %q）", trimmed)
	}
	if len(trimmed) > 64 {
		return fmt.Errorf("分组标识最多 64 个字符，当前 %d", len(trimmed))
	}
	return nil
}

// EffectiveGroupName 返回令牌实际生效的分组名。
//
// 语义（转发层按此取值，无需自行判空）：
//   - GroupName 非空：返回它——该令牌只在"此分组的渠道"里路由，
//     并只按"此分组的价格与倍率"计费；
//   - GroupName 为空：返回 fallback（调用方通常传网关的默认分组），
//     使未设置分组的历史令牌保持原有行为。
//
// 参数 fallback 而非直接返回常量，是为了让调用方决定"默认分组"的来源
// （可能来自系统设置），避免领域层反向依赖配置。
func (t *Token) EffectiveGroupName(fallback string) string {
	if strings.TrimSpace(t.GroupName) != "" {
		return t.GroupName
	}
	return fallback
}

// IsExpired 判断令牌在给定时刻是否已过期（永不过期返回 false）。
//
// 传入 now 而非在内部取当前时间，是为了便于测试与保证同一请求内的判定一致。
func (t *Token) IsExpired(now time.Time) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return now.After(t.ExpiresAt)
}

// HasQuota 判断令牌是否还有可用额度。
//
// 说明：不限额度、或剩余额度大于 0 时视为有额度。
// 精确扣费在 M5 计费模块实现，此处只做"能否放行"的粗判。
func (t *Token) HasQuota() bool {
	if t.UnlimitedQuota {
		return true
	}
	return t.RemainQuota > 0
}

// AvailableQuota 返回令牌的可用额度；不限额度时返回 QuotaUnlimited（-1）。
//
// 为什么令牌不需要像用户那样额外减去"在途预留"：
//
//	令牌的额度扣减在【预留阶段】就已真实写入 remain_quota（见 store.QuotaRepository.Reserve），
//	因此读到的 RemainQuota 本身就已经扣除了在途预留，可用额度即当前剩余额度。
func (t *Token) AvailableQuota() int64 {
	if t.UnlimitedQuota {
		return QuotaUnlimited
	}
	return t.RemainQuota
}

// BudgetEnabled 判断该令牌是否启用了「周期预算闸门」。
//
// 启用条件（三者同时满足，否则一律视为未启用、行为与引入预算前逐字一致）：
//   - 非"不限额度"令牌（UnlimitedQuota=false）：不限额度意味着站长明确不做任何限制；
//   - BudgetQuota > 0：0 表示不限预算；
//   - BudgetPeriod 是合法周期（daily/weekly/monthly）。
func (t *Token) BudgetEnabled() bool {
	return t != nil && !t.UnlimitedQuota && t.BudgetQuota > 0 && IsValidBudgetPeriod(t.BudgetPeriod)
}

// BudgetWindowUsed 返回当前窗口已消耗的额度（内部单位）。
//
// 口径：UsedQuota − BudgetWindowBase，负数按 0 处理。
// 负数只可能来自"窗口重置后对上一窗口的退还"，此时按 0 计更保守（不会凭空放宽预算）。
func (t *Token) BudgetWindowUsed() int64 {
	used := t.UsedQuota - t.BudgetWindowBase
	if used < 0 {
		return 0
	}
	return used
}

// EvaluateBudget 在给定时刻判定该令牌的周期预算状态（纯函数，不触碰存储）。
//
// 返回的 BudgetDecision 供调用方决定"放行 / 拒绝 / 先重置窗口"：
//   - Enabled=false：未启用预算，直接放行；
//   - NeedReset=true：窗口尚未锚定或已过期，调用方应调用存储层把窗口起点重置为
//     decision.WindowStart 并把基线对齐到当前 used_quota，随后放行（新窗口从零开始）；
//   - 其余情况：Exceeded 表示本窗口已消耗是否达到上限。
//
// 窗口是否过期用"右开区间"判定：now >= 窗口结束时刻即视为过期，
// 恰好落在结束时刻的那次调用属于新窗口（避免边界上仍被旧窗口拒绝）。
func (t *Token) EvaluateBudget(now time.Time) BudgetDecision {
	if !t.BudgetEnabled() {
		return BudgetDecision{}
	}
	start := t.BudgetWindowStart
	if start.IsZero() || !now.Before(budgetWindowEnd(start, t.BudgetPeriod)) {
		// 尚未锚定（首次启用）或已越过窗口结束：需要惰性重置，本次放行。
		return BudgetDecision{
			Enabled:     true,
			NeedReset:   true,
			WindowStart: now,
			Limit:       t.BudgetQuota,
		}
	}
	used := t.BudgetWindowUsed()
	return BudgetDecision{
		Enabled:     true,
		Exceeded:    used >= t.BudgetQuota,
		WindowStart: start,
		Used:        used,
		Limit:       t.BudgetQuota,
	}
}

// AllowsModel 判断令牌是否被允许访问指定模型。
//
// 匹配规则：白名单为空表示不限制；否则精确匹配（大小写敏感）。
func (t *Token) AllowsModel(name string) bool {
	if len(t.Models) == 0 {
		return true
	}
	for _, m := range t.Models {
		if m == name {
			return true
		}
	}
	return false
}

// EffectiveStatus 返回"结合当前时间与额度"后的实际状态。
//
// 设计意图：数据库里的 status 只记录管理员意图（启用/手动禁用），
// 而"是否过期""额度是否耗尽"是随时间变化的事实，应在判定时实时计算。
// 这样才能做到"到期即失效"，而无需依赖定时任务去批量改状态。
//
// 判定优先级：手动禁用 > 过期 > 额度耗尽 > 启用。
// 说明：手动禁用优先，是因为管理员的显式操作应当压过自动判定。
func (t *Token) EffectiveStatus(now time.Time) TokenStatus {
	if t.Status == TokenStatusDisabled {
		return TokenStatusDisabled
	}
	if t.IsExpired(now) {
		return TokenStatusExpired
	}
	if !t.HasQuota() {
		return TokenStatusExhausted
	}
	return TokenStatusEnabled
}

// MaskedKey 返回脱敏后的令牌 KEY，供日志与后台列表展示使用。
//
// 脱敏规则：保留前缀与末 4 位，例如 "sk-abcdef12...cdef" → "sk-abc****cdef"。
// 目的：让使用者能辨认是哪把令牌，同时不足以被直接盗用。
//
// 长度下限：见 Channel.MaskedAPIKey 的同类说明——露出的字符必须在密钥里
// 只占一小部分。令牌恒由 GenerateTokenKey 生成 51 位，此下限只为防御性兜底，
// 避免有人把测试值或外部导入的短密钥塞进 Token.Key 时脱敏退化为全暴露。
func (t *Token) MaskedKey() string {
	const (
		keepSuffix = 4
		// minMaskableLen = 露出的 (前缀+3) + 4 个字符仍只占少数
		minMaskableLen = 24
	)

	key := t.Key
	if len(key) < minMaskableLen {
		return strings.Repeat("*", len(key))
	}
	return key[:len(TokenKeyPrefix)+3] + "****" + key[len(key)-keepSuffix:]
}

// TokenQuery 描述令牌列表的查询条件。
type TokenQuery struct {
	OwnerID *uint64      // 按归属人过滤；nil 表示不过滤
	Status  *TokenStatus // 按状态过滤；nil 表示不过滤
	// GroupName 按所属分组过滤；nil 表示不过滤。
	// 注意：这里按【落库的原始值】精确匹配，空串只筛出"未设置分组"的令牌，
	// 不会把空串自动展开为默认分组——筛选语义应保持字面直观。
	GroupName *string
	Limit     int // 返回条数上限；<=0 使用默认值
	Offset    int // 偏移量，用于分页
}

// TokenRepository 定义令牌的持久化操作。
//
// 实现约定：
//   - 写入时必须保存 key_hash（摘要，供查找）与 key_enc（密文，供展示）；
//   - GetByKey 必须走摘要索引查找，禁止全表解密比对；
//   - 未找到时返回 ErrTokenNotFound。
type TokenRepository interface {
	// Create 新增令牌，成功后回填 ID、CreatedAt、UpdatedAt。
	Create(ctx context.Context, t *Token) error

	// GetByID 按主键查询令牌，不存在时返回 ErrTokenNotFound。
	GetByID(ctx context.Context, id uint64) (*Token, error)

	// GetByKey 按令牌明文查询（内部通过摘要索引匹配），不存在时返回 ErrTokenNotFound。
	// 这是鉴权热路径，实现必须高效。
	GetByKey(ctx context.Context, key string) (*Token, error)

	// List 按条件查询令牌列表，按 ID 升序返回。
	List(ctx context.Context, q TokenQuery) ([]*Token, error)

	// Count 返回符合条件的令牌总数，用于分页。
	Count(ctx context.Context, q TokenQuery) (int, error)

	// StatusCounts 按状态分组统计令牌数量，用于仪表盘概览。
	StatusCounts(ctx context.Context) (map[TokenStatus]int, error)

	// RecordUsage 记录令牌最近一次使用时间。
	//
	// 说明：只更新一个字段，避免把整行写回造成并发覆盖。
	RecordUsage(ctx context.Context, id uint64, at time.Time) error

	// ConsumeQuota 调整令牌额度：amount 为正表示扣减，为负表示退还。
	//
	// 关键实现要求：必须在【单条 SQL】内完成自增与自减。
	// 若写成"先读出来、在内存里加减、再写回"，并发请求会互相覆盖，
	// 表现为"用于统计的已用额度明显偏小"——这是计费类系统最典型的漏计缺陷。
	//
	// 不限额度（unlimited_quota）的令牌只累加已用额度、不动剩余额度，
	// 但用量仍然照常记录，便于站长核算上游成本。
	// 剩余额度扣到 0 为止（不允许为负），避免出现"倒欠额度"的怪异状态。
	//
	// 退还（amount < 0）用于异步任务失败时回滚提交阶段已扣的额度。
	ConsumeQuota(ctx context.Context, id uint64, amount int64, at time.Time) error

	// ResetBudgetWindow 惰性重置令牌的周期预算窗口：把窗口起点设为 windowStart，
	// 并把窗口基线对齐到当前 used_quota（等价于"本窗口已消耗归零"）。
	//
	// 为什么需要它：周期预算的"翻篇"不依赖定时任务——只有当某次判定发现窗口已过期时，
	// 才在这一次调用里顺手重置（见 model.Token.EvaluateBudget 的 NeedReset）。
	// 基线必须在【同一条 SQL】里取 used_quota，避免"先读后写"被并发扣费插队。
	//
	// 实现须容忍令牌已被删除（受影响 0 行不报错），与 RecordUsage 的容错口径一致。
	ResetBudgetWindow(ctx context.Context, id uint64, windowStart time.Time) error

	// GroupSpendToday 聚合某分组在 [since, until) 区间内消耗的额度（只读）。
	//
	// 分组归属按"请求实际命中的渠道所属分组"判定（与路由分组匹配语义一致），
	// 只统计成功请求。用于后台展示"某分组今天烧了多少"，不参与扣费。
	//
	// 放在 TokenRepository 上而非另开仓储：计费组件已持有本仓储，
	// 无需为一项只读统计再牵动装配（该查询本身也不需要令牌上下文）。
	GroupSpendToday(ctx context.Context, group string, since, until time.Time) (int64, error)

	// Update 按 ID 更新令牌（不修改创建时间），不存在时返回 ErrTokenNotFound。
	Update(ctx context.Context, t *Token) error

	// Delete 按 ID 物理删除令牌，不存在时返回 ErrTokenNotFound。
	Delete(ctx context.Context, id uint64) error
}
