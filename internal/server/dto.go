// 本文件定义对外响应的数据结构（DTO）与领域模型的转换函数。
//
// 意图（Why）：
//
//	领域模型与对外接口是两个不同的关注点，必须分开：
//	  1) 安全：领域模型里有口令哈希、渠道明文密钥、令牌明文等敏感字段，
//	     若直接把模型序列化为 JSON，一次疏忽就会把密钥泄露给前端；
//	  2) 稳定：接口字段是契约（前端依赖它），不应随内部重构而被动变化。
//	因此所有响应都经过显式转换，且敏感字段在此处统一脱敏。
//
// 流转（Flow）：
//
//	handler → toXxxDTO(领域模型) → JSON 响应
//
// 扩展（Extend）：
//
//	新增接口字段时：在此结构体补充字段与转换逻辑，
//	并在 docs/06-前后端接口契约.md 同步更新（契约与实现必须一致）。
//	新增敏感字段时：务必只输出脱敏形式（如 xxx_masked），绝不输出原文。
package server

import (
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
)

// pagedResponse 是列表接口的统一响应结构。
//
// 使用泛型而非 interface{}：让每个列表接口的 items 类型在编译期确定，
// 避免前端拿到结构不一致的数据。
type pagedResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
}

// newPagedResponse 构造分页响应。items 为 nil 时替换为空切片，
// 保证 JSON 中是 [] 而不是 null（前端可直接 .map，无需判空）。
func newPagedResponse[T any](items []T, total, page, size int) pagedResponse[T] {
	if items == nil {
		items = make([]T, 0)
	}
	return pagedResponse[T]{Items: items, Total: total, Page: page, Size: size}
}

// unixOrZero 把时间转为 Unix 秒；零值时间返回 0。
//
// 约定：接口层统一用 0 表示"未设置/永不"（如永不过期、从未测活），
// 避免前端处理 Go 零值时间那个奇怪的公元 1 年时间戳。
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ---------------------------------------------------------------------------
// 用户
// ---------------------------------------------------------------------------

// userDTO 是对外的用户信息。
//
// 安全约束：绝不包含 password_hash。
type userDTO struct {
	ID             uint64 `json:"id"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	Role           int    `json:"role"`
	RoleText       string `json:"role_text"`
	Status         int    `json:"status"`
	StatusText     string `json:"status_text"`
	Quota          int64  `json:"quota"`
	UsedQuota      int64  `json:"used_quota"`
	RemainingQuota int64  `json:"remaining_quota"`
	// AgentGroup 是该用户的代理分组（空串 = 普通用户）。
	//
	// 下发给前端有两个用途：后台用户列表标出"谁是代理"，以及 /auth/me
	// 让门户知道"我是不是代理"。它不是敏感信息（只是一个分组名）。
	AgentGroup string `json:"agent_group"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// toUserDTO 把用户模型转为对外 DTO。
func toUserDTO(u *model.User) userDTO {
	if u == nil {
		return userDTO{}
	}
	return userDTO{
		ID:             u.ID,
		Username:       u.Username,
		Email:          u.Email,
		Role:           int(u.Role),
		RoleText:       u.Role.String(),
		Status:         int(u.Status),
		StatusText:     u.Status.String(),
		Quota:          u.Quota,
		UsedQuota:      u.UsedQuota,
		RemainingQuota: u.RemainingQuota(),
		AgentGroup:     u.AgentGroup,
		CreatedAt:      unixOrZero(u.CreatedAt),
		UpdatedAt:      unixOrZero(u.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------
// 渠道
// ---------------------------------------------------------------------------

// channelDTO 是对外的渠道信息。
//
// 安全约束：只输出掩码后的密钥（masked_key），绝不输出明文。
type channelDTO struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
	// TypeKey 是渠道类型标识（如 azure_openai / anthropic / gemini）；
	// 空串表示历史渠道（转发时按 OpenAI 兼容处理）。
	TypeKey string `json:"type_key"`
	// ExtraConfig 是类型专属参数（如 Azure 的 deployment / api_version）。
	// 恒为非 nil（无配置时返回 {}），便于前端直接读取而不必判空。
	ExtraConfig map[string]string `json:"extra_config"`
	BaseURL     string            `json:"base_url"`
	MaskedKey   string            `json:"masked_key"`
	Models      []string          `json:"models"`
	Group       string            `json:"group"`
	// Groups 是本渠道可服务的全部分组（多选；恒非空，主分组为其第一项）。
	//
	// 前端用多选控件编辑它；路由匹配以它为准，
	// 因此"一个渠道同时服务免费组与自营组"这种常见诉求不再需要建两个渠道。
	Groups   []string `json:"groups"`
	Priority int      `json:"priority"`
	Weight   int      `json:"weight"`
	// KeyStrategy 是凭据池调度策略标识（sequential / round_robin / ...）；
	// 后端保证恒为合法值（空值已归一为默认策略）。
	KeyStrategy string `json:"key_strategy"`
	// KeyFailurePolicy 是密钥失败处置策略标识（cooldown_only / auto_remove）；
	// 后端保证恒为合法值（空值已归一为默认的「只冷却不摘除」）。
	KeyFailurePolicy string `json:"key_failure_policy"`
	// KeyCooldownSeconds 是密钥失败后的统一冷却时长（秒）；0 表示使用内置分级退避。
	KeyCooldownSeconds int `json:"key_cooldown_seconds"`
	// RetryEnabled 表示是否对该渠道的上游错误做站内重试（总开关）。
	//
	// 后端已归一：未配置的渠道下发 true，前端只需按普通开关渲染。
	RetryEnabled bool `json:"retry_enabled"`
	// RetryMaxAttempts 是渠道级重试次数上限（含首次尝试），后端保证已归一为 1..10。
	RetryMaxAttempts int `json:"retry_max_attempts"`
	// ModelRetryRules 是模型级重试覆盖规则；空数组表示全部沿用渠道级配置。
	ModelRetryRules []model.ModelRetryRule `json:"model_retry_rules"`
	Status          int                    `json:"status"`
	StatusText      string                 `json:"status_text"`
	LastTestAt      int64                  `json:"last_test_at"`
	LastTestOK      bool                   `json:"last_test_ok"`
	// LatencyMS 是最近一次测活（含后台自动巡检）的耗时；0 = 尚未拿到过响应。
	//
	// 前端必须连 LastTestAt 一起展示：它是一次测量而非均值，
	// 单独显示一个毫秒数会让人误以为是稳定的性能指标。
	LatencyMS int `json:"latency_ms"`
	// LastTestCode 是最近一次测活的上游 HTTP 状态码；0 = 网络层失败。
	//
	// 给状态码是为了可处置：401 要换密钥、404 要清模型、429 要降频，
	// 只有"失败/成功"两态会让这三条线索全部丢失。
	LastTestCode int `json:"last_test_code"`
	// LastTestModel 是最近一次实际探测命中的模型名。
	LastTestModel string `json:"last_test_model"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	// KeyPool 是密钥池概览（渠道可挂多把上游密钥并轮询使用）。
	// 零值表示该渠道没有配置密钥池，走"单密钥"模式。
	KeyPool keyPoolDTO `json:"key_pool"`

	// ModelFailures 是该渠道近 24 小时的模型失败统计（按模型 × 状态码）。
	//
	// 仅渠道【详情】接口填充；列表接口保持空数组（避免为每个渠道多跑一次聚合查询）。
	// 用途：站长一眼看出哪些模型已在上游失效（404/410）或无授权（403），
	// 从而决定从渠道模型清单里清理——这是"调用报错"最直接的体检数据。
	// 空数组表示该渠道近 24h 无失败请求。
	ModelFailures []model.ModelFailureStat `json:"model_failures"`
}

// keyPoolDTO 是密钥池的概览统计。
//
// 只给计数不给明细：一个渠道可能挂 500 把密钥，
// 若列表接口把每把密钥都带上，页面数据量会大到不可用。
// 需要明细时再单独请求该渠道的密钥列表。
type keyPoolDTO struct {
	Total       int `json:"total"`
	Enabled     int `json:"enabled"`
	Disabled    int `json:"disabled"`
	AutoRemoved int `json:"auto_removed"`
}

// channelKeyDTO 是凭据池中单条凭据的对外表示。
//
// 安全约束：默认只输出掩码（masked_key）。明文（secret）只在管理员显式
// 要求查看原文时（?reveal=1）才填充，且该次读取会被写入操作审计。
type channelKeyDTO struct {
	ID         uint64 `json:"id"`
	Kind       string `json:"kind"`
	KindText   string `json:"kind_text"`
	Label      string `json:"label"`
	MaskedKey  string `json:"masked_key"`
	Status     int    `json:"status"`
	StatusText string `json:"status_text"`
	FailCount  int    `json:"fail_count"`
	LastUsedAt int64  `json:"last_used_at"`
	LastError  string `json:"last_error"`
	CreatedAt  int64  `json:"created_at"`
	// Secret 是凭据原文，仅在显式查看原文时填充（omitempty 保证平时不出现该字段）。
	//
	// 为什么不复用 masked_key：两者语义完全不同——掩码可随手展示，
	// 原文只能在管理员明确要求时返回。用不同字段能避免"某处误用掩码字段返回了原文"。
	Secret string `json:"secret,omitempty"`

	// 调度维度字段（只读透出 + 可编辑参数）。
	//
	//   - weight / priority / rpm_limit 是可在后台编辑的调度参数；
	//   - in_flight / cooldown_until 是运行态，仅供展示（冷却剩余时间由前端按 Unix 秒计算）。
	Weight        int   `json:"weight"`
	Priority      int   `json:"priority"`
	RPMLimit      int   `json:"rpm_limit"`
	InFlight      int   `json:"in_flight"`
	CooldownUntil int64 `json:"cooldown_until"`

	// 余额维度字段（迁移 0022）。
	//
	// 安全约束：余额属于运营数据，只允许出现在管理员接口（本 DTO 仅在
	// GET /api/admin/channels/:id/keys 使用），绝不可进入公开的模型广场或 /v1/**。
	//   - balance：-1 表示未录入，>=0 为已知余额；
	//   - balance_unknown / balance_exhausted 是给前端直接判定的派生布尔，
	//     避免前端自己实现"-1 表示未知"的规则（规则只在领域层维护一处）；
	//   - balance_updated_at：余额最近一次被人工更新的时间（Unix 秒，0=未录入）。
	Balance          int64 `json:"balance"`
	BalanceUnknown   bool  `json:"balance_unknown"`
	BalanceExhausted bool  `json:"balance_exhausted"`
	BalanceUpdatedAt int64 `json:"balance_updated_at"`

	// 订阅账号维度字段（迁移 0028）。对 API Key 型凭据一律为空/未知。
	//
	//   - account_id 是上游账号标识，出站时必须随请求带上（缺了上游直接拒绝）；
	//   - plan_type 是套餐标识（plus / pro / team…），仅用于展示；
	//   - quota_used_percent / quota_reset_at / quota_checked_at 是上游额度窗口快照，
	//     由「查询额度」或转发过程中的限流反馈写入；
	//   - quota_known / quota_exhausted 是给前端直接判定的派生布尔，
	//     避免前端自己实现"-1 表示未探测""重置时间已过视为已恢复"这两条规则
	//     （规则只在领域层维护一处，见 model.ChannelKey.QuotaExhausted）。
	AccountID        string `json:"account_id"`
	PlanType         string `json:"plan_type"`
	QuotaUsedPercent int    `json:"quota_used_percent"`
	QuotaResetAt     int64  `json:"quota_reset_at"`
	QuotaCheckedAt   int64  `json:"quota_checked_at"`
	QuotaKnown       bool   `json:"quota_known"`
	QuotaExhausted   bool   `json:"quota_exhausted"`

	// 路由分叉（迁移 0038）：本凭据可服务的分组与模型。
	//
	// 恒为非 nil 空数组（空 = 不限，即继承渠道级路由），前端直接读取即可。
	Groups []string `json:"groups"`
	Models []string `json:"models"`
}

// toChannelKeyDTO 把凭据模型转为对外 DTO。
//
// 参数 reveal 为真时额外填充凭据原文（仅超管显式查看原文时使用）。
func toChannelKeyDTO(key *model.ChannelKey, reveal bool) channelKeyDTO {
	if key == nil {
		return channelKeyDTO{}
	}
	kindText := "API Key"
	if key.IsOAuth() {
		kindText = "订阅账号"
	}
	secret := ""
	if reveal {
		secret = key.RevealSecret()
	}
	return channelKeyDTO{
		ID:         key.ID,
		Kind:       string(key.Kind),
		KindText:   kindText,
		Label:      key.Label,
		MaskedKey:  key.Masked(),
		Secret:     secret,
		Status:     int(key.Status),
		StatusText: key.Status.String(),
		FailCount:  key.FailCount,
		LastUsedAt: unixOrZero(key.LastUsedAt),
		LastError:  key.LastError,
		CreatedAt:  unixOrZero(key.CreatedAt),

		Weight:        key.Weight,
		Priority:      key.Priority,
		RPMLimit:      key.RPMLimit,
		InFlight:      key.InFlight,
		CooldownUntil: unixOrZero(key.CooldownUntil),

		Balance:          key.Balance,
		BalanceUnknown:   key.Balance == model.BalanceUnknown,
		BalanceExhausted: key.BalanceExhausted(),
		BalanceUpdatedAt: unixOrZero(key.BalanceUpdatedAt),

		AccountID:        key.AccountID,
		PlanType:         key.PlanType,
		QuotaUsedPercent: key.QuotaUsedPercent,
		QuotaResetAt:     unixOrZero(key.QuotaResetAt),
		QuotaCheckedAt:   unixOrZero(key.QuotaCheckedAt),
		QuotaKnown:       key.QuotaKnown(),
		QuotaExhausted:   key.QuotaExhausted(time.Now()),

		// 路由分叉：恒以非 nil 数组下发（空数组 = 不限），前端不必判空。
		Groups: retryRulesStringsOrEmpty(key.Groups),
		Models: retryRulesStringsOrEmpty(key.Models),
	}
}

// retryRulesStringsOrEmpty 保证字符串清单以非 nil 形式下发。
//
// 与 retryRulesOrEmpty 同样的取舍：让前端直接读取即可，
// 不必为"字段可能是 null"写额外的兜底分支。
func retryRulesStringsOrEmpty(values []string) []string {
	if values == nil {
		return make([]string, 0)
	}
	return values
}

// toChannelKeyDTOList 批量转换密钥；reveal 为真时一并填充原文。
func toChannelKeyDTOList(keys []*model.ChannelKey, reveal bool) []channelKeyDTO {
	result := make([]channelKeyDTO, 0, len(keys))
	for _, key := range keys {
		result = append(result, toChannelKeyDTO(key, reveal))
	}
	return result
}

// applyKeyPool 把密钥池概览填充到渠道 DTO 上。
func applyKeyPool(dto *channelDTO, summary model.KeyPoolSummary) {
	dto.KeyPool = keyPoolDTO{
		Total:       summary.Total,
		Enabled:     summary.Enabled,
		Disabled:    summary.Disabled,
		AutoRemoved: summary.AutoRemoved,
	}
}

// toChannelDTO 把渠道模型转为对外 DTO。
func toChannelDTO(ch *model.Channel) channelDTO {
	if ch == nil {
		return channelDTO{}
	}
	models := ch.Models
	if models == nil {
		models = make([]string, 0)
	}
	// 扩展配置恒以非 nil 形式下发：前端直接读取即可，无需判空。
	extra := ch.ExtraConfig
	if extra == nil {
		extra = make(map[string]string)
	}
	return channelDTO{
		ID:          ch.ID,
		Name:        ch.Name,
		Type:        ch.Type,
		TypeKey:     ch.TypeKey,
		ExtraConfig: extra,
		BaseURL:     ch.BaseURL,
		MaskedKey:   ch.MaskedAPIKey(),
		Models:      models,
		Group:       ch.Group,
		// 恒下发非空清单：既有单分组渠道会自动得到 [group]，
		// 前端因此不必区分"单分组/多分组"两套渲染逻辑。
		Groups:   ch.GroupList(),
		Priority: ch.Priority,
		Weight:   ch.Weight,
		// 统一下发合法策略：即使库中出现空值/脏值，前端也能拿到默认策略。
		KeyStrategy:      string(model.NormalizeKeyStrategy(string(ch.KeyStrategy))),
		KeyFailurePolicy: string(model.NormalizeKeyFailurePolicy(string(ch.KeyFailurePolicy))),
		// 冷却时长同样归一（越界值会被夹到合法区间），避免前端拿到非法值。
		KeyCooldownSeconds: model.NormalizeKeyCooldownSeconds(ch.KeyCooldownSeconds),
		// 重试策略同样归一后下发：开关给出明确布尔值，次数给出 1..10 的合法值。
		RetryEnabled:     ch.RetryMode.Enabled(),
		RetryMaxAttempts: model.NormalizeRetryMaxAttempts(ch.RetryMaxAttempts),
		ModelRetryRules:  retryRulesOrEmpty(ch.ModelRetryRules),
		Status:           int(ch.Status),
		StatusText:       ch.Status.String(),
		LastTestAt:       unixOrZero(ch.LastTestAt),
		LastTestOK:       ch.LastTestOK,
		LatencyMS:        ch.LatencyMS,
		LastTestCode:     ch.LastTestCode,
		LastTestModel:    ch.LastTestModel,
		CreatedAt:        unixOrZero(ch.CreatedAt),
		UpdatedAt:        unixOrZero(ch.UpdatedAt),
		// 失败统计：列表接口恒为空数组（详情接口由处理器另行填充）。
		// 用非 nil 保证 JSON 输出 [] 而非 null，前端可直接读取。
		ModelFailures: make([]model.ModelFailureStat, 0),
	}
}

// retryRulesOrEmpty 保证模型级重试规则以非 nil 下发。
//
// 与 Models/ExtraConfig 的处理一致：让前端直接读取即可，
// 不必为"字段可能是 null"写额外的兜底分支。
func retryRulesOrEmpty(rules []model.ModelRetryRule) []model.ModelRetryRule {
	if rules == nil {
		return make([]model.ModelRetryRule, 0)
	}
	return rules
}

// toChannelDTOList 批量转换渠道。
func toChannelDTOList(channels []*model.Channel) []channelDTO {
	result := make([]channelDTO, 0, len(channels))
	for _, ch := range channels {
		result = append(result, toChannelDTO(ch))
	}
	return result
}

// ---------------------------------------------------------------------------
// 访问令牌
// ---------------------------------------------------------------------------

// tokenDTO 是对外的访问令牌信息。
//
// 安全约束：只输出 masked_key；明文仅在创建时通过一次性字段返回。
type tokenDTO struct {
	ID             uint64   `json:"id"`
	Name           string   `json:"name"`
	MaskedKey      string   `json:"masked_key"`
	Status         int      `json:"status"`
	StatusText     string   `json:"status_text"`
	ExpiresAt      int64    `json:"expires_at"`
	RemainQuota    int64    `json:"remain_quota"`
	UnlimitedQuota bool     `json:"unlimited_quota"`
	UsedQuota      int64    `json:"used_quota"`
	Models         []string `json:"models"`
	// GroupName 是令牌所属分组标识；空串表示"使用网关默认分组"。
	// 前端据此展示"这把密钥走哪个分组"，并提供按分组筛选。
	GroupName  string `json:"group_name"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	LastUsedAt int64  `json:"last_used_at"`

	// 以下字段用于管理端展示归属信息（用户门户中为空，前端会自动忽略）。
	UserID   uint64 `json:"user_id"`
	Username string `json:"username"`

	// Key 是令牌明文，【仅创建时】填充一次，其余场景因 omitempty 而不会出现在响应中。
	//
	// 设计说明：数据库只保存摘要与密文，明文无法二次取回；
	// 因此这是使用者唯一一次看到明文的机会，前端必须显著提示立即保存。
	Key string `json:"key,omitempty"`
}

// toTokenDTO 把令牌模型转为对外 DTO。
//
// 参数 status 使用"结合时间与额度后的实际状态"，因为对使用者而言
// "已过期"比数据库里记录的"启用"更有意义。
func toTokenDTO(t *model.Token, status model.TokenStatus) tokenDTO {
	if t == nil {
		return tokenDTO{}
	}
	models := t.Models
	if models == nil {
		models = make([]string, 0)
	}
	return tokenDTO{
		ID:             t.ID,
		Name:           t.Name,
		MaskedKey:      t.MaskedKey(),
		Status:         int(status),
		StatusText:     status.String(),
		ExpiresAt:      unixOrZero(t.ExpiresAt),
		RemainQuota:    t.RemainQuota,
		UnlimitedQuota: t.UnlimitedQuota,
		UsedQuota:      t.UsedQuota,
		Models:         models,
		GroupName:      t.GroupName,
		CreatedAt:      unixOrZero(t.CreatedAt),
		UpdatedAt:      unixOrZero(t.UpdatedAt),
		LastUsedAt:     unixOrZero(t.LastUsedAt),
		UserID:         t.OwnerID,
	}
}

// ---------------------------------------------------------------------------
// 调用日志
// ---------------------------------------------------------------------------

// usageLogDTO 是对外的调用日志信息。
type usageLogDTO struct {
	ID          uint64 `json:"id"`
	UserID      uint64 `json:"user_id"`
	Username    string `json:"username"`
	TokenName   string `json:"token_name"`
	ChannelID   uint64 `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Model       string `json:"model"`
	// UpstreamModel 是本次【实际发给上游】的模型名（渠道级映射改写后的名字）。
	//
	// 为什么要把它暴露出来：站长配置了"平台模型 ID → 上游模型 ID"的映射后，
	// 最关心的一件事就是"上游到底收到了哪个名字"。没有这个字段时，只能靠抓包
	// 或猜，一旦上游报"模型不存在"就无从判断是映射没生效还是上游真没有该模型。
	// 空串表示未经过映射（上游收到的与对外名一致）。
	UpstreamModel    string `json:"upstream_model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	// CachedTokens / ReasoningTokens 是上游回报的用量细节（0 = 上游未提供）。
	//
	// 它们决定"这条记录为什么扣这么多/为什么便宜"：
	//   - 缓存命中部分通常按更低价计费，是核对账单与评估提示词复用的依据；
	//   - 推理 token 计入输出但使用者看不到，是出账争议的主要来源。
	CachedTokens    int `json:"cached_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	// FirstTokenMS 是首 token 延迟（毫秒，0 = 非流式/未采集）；
	// TokensPerSecond 是输出速率（0 = 无法计算）。
	//
	// 这两个是"模型快不快"的核心指标：总耗时 30 秒可能只是回答长，
	// 首包 3 秒与 20 秒的体验完全不同，必须分开看。
	FirstTokenMS    int     `json:"first_token_ms"`
	TokensPerSecond float64 `json:"tokens_per_second"`
	Quota           int64   `json:"quota"`
	LatencyMS       int     `json:"latency_ms"`
	IsStream        bool    `json:"is_stream"`
	StatusCode      int     `json:"status_code"`
	Error           string  `json:"error"`
	CreatedAt       int64   `json:"created_at"`
}

// toUsageLogDTO 把日志模型转为对外 DTO。
//
// 参数 usernames/channelNames 为"ID → 名称"映射，由调用方批量预取。
//
// 为什么用映射而非在日志表里存名称：名称会变（渠道改名），
// 日志应记录当时的 ID 事实；展示时的名称解析放到查询侧。
// 批量预取而非逐条查询，是为了避免 N+1 查询（一页 20 条就是 40 次额外查询）。
func toUsageLogDTO(log *model.UsageLog, usernames map[uint64]string, channelNames map[uint64]string) usageLogDTO {
	if log == nil {
		return usageLogDTO{}
	}
	return usageLogDTO{
		ID:               log.ID,
		UserID:           log.UserID,
		Username:         usernames[log.UserID],
		ChannelID:        log.ChannelID,
		ChannelName:      channelNames[log.ChannelID],
		Model:            log.Model,
		UpstreamModel:    log.UpstreamModel,
		PromptTokens:     log.PromptTokens,
		CompletionTokens: log.CompletionTokens,
		TotalTokens:      log.TotalTokens,
		CachedTokens:     log.CachedTokens,
		ReasoningTokens:  log.ReasoningTokens,
		FirstTokenMS:     log.FirstTokenMS,
		TokensPerSecond:  log.TokensPerSecond,
		Quota:            log.Quota,
		LatencyMS:        log.LatencyMS,
		IsStream:         log.IsStream,
		StatusCode:       log.StatusCode,
		Error:            log.Error,
		CreatedAt:        unixOrZero(log.CreatedAt),
	}
}

// ---------------------------------------------------------------------------
// 统计
// ---------------------------------------------------------------------------

// dailyUsageDTO 是趋势图上的单个数据点。
type dailyUsageDTO struct {
	Date     string `json:"date"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Quota    int64  `json:"quota"`
	// CachedTokens 是当日命中的缓存 token（用于画缓存命中趋势）
	CachedTokens int64 `json:"cached_tokens"`
}

// toDailyUsageDTOList 批量转换趋势数据。
func toDailyUsageDTOList(items []model.DailyUsage) []dailyUsageDTO {
	result := make([]dailyUsageDTO, 0, len(items))
	for _, item := range items {
		result = append(result, dailyUsageDTO{
			Date:         item.Date,
			Requests:     item.Requests,
			Tokens:       item.Tokens,
			Quota:        item.Quota,
			CachedTokens: item.CachedTokens,
		})
	}
	return result
}

// modelUsageDTO 是模型排行中的单项。
type modelUsageDTO struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// toModelUsageDTOList 批量转换模型排行。
func toModelUsageDTOList(items []model.ModelUsage) []modelUsageDTO {
	result := make([]modelUsageDTO, 0, len(items))
	for _, item := range items {
		result = append(result, modelUsageDTO{
			Model:    item.Model,
			Requests: item.Requests,
			Tokens:   item.Tokens,
		})
	}
	return result
}
