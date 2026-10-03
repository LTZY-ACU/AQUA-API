// AI Agent 的数据模型：密钥、角色与错误定义。
//
// 意图（Why）：
//
//	后台内置的 agent 有两个，边界完全不同：
//	  · 运维 agent（ops）——站长本人用，带工具，能读写站内数据与配置；
//	  · 客服 agent（support）——外部使用者用，无任何工具，纯问答。
//	角色必须落库而不是靠 key 格式区分：把边界编码进字符串（形如 "ops_xxx"）
//	会让"忘了写前缀"直接变成越权，而那正是最难发现的一类缺陷。
//
// 设计取舍：
//
//	AgentKey 不复用 model.Token。两者权限边界不同（token 能调任意模型 API，
//	agent key 只能问 agent），复用一张表就得在每个鉴权分支判"这是哪种 key"，
//	漏判一次即越权。独立表让鉴权只有一种判据。
//
// 流转（Flow）：
//
//	后台生成 → 明文仅返回一次（只存 SHA-256 摘要）
//	→ 客户端作 Bearer 令牌 → agent 鉴权按 role 分流到带工具 / 不带工具两条路径
//
// 扩展（Extend）：
//
//	要加第三种角色（如只读数据分析师），在 AgentRole 加枚举值并补 roleLabel；
//	不要靠"role 是字符串所以什么都能塞"来绕过白名单——工具授权由
//	agentToolsFor(role) 单点决定，角色只决定"选哪一组工具"。
package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
)

// AgentKeyPrefix 是 agent 密钥的前缀。
//
// 刻意与 TokenKeyPrefix（sk-）不同：两者都会出现在 Authorization 头里，
// 而它们的权限边界完全不同（token 能调任意模型 API，agent key 只能问 agent）。
// 前缀相同意味着用户把 token 贴到 agent 入口时，鉴权只能靠"查哪张表"来区分——
// 一旦查错表就是越权。前缀不同则可以在一行日志里就看出贴错了。
const AgentKeyPrefix = "ak-"

const (
	// agentKeyRandomBytes 是随机部分字节数（32 字节 = 256 位熵）。
	//
	// 与令牌同量级：agent 入口在公网可达，密钥强度不足等于把站内数据敞开。
	agentKeyRandomBytes = 32
	// agentKeyMinNameLength / agentKeyMaxNameLength 是备注名的长度边界。
	//
	// 下限为 1：备注为空时后台列表里会出现一串无法区分的密钥，
	// 站长既不知道哪把是哪把，也就无法正确地停用与轮换。
	agentKeyMinNameLength = 1
	agentKeyMaxNameLength = 64
)

// AgentRole 是 agent 的角色，决定它能拿到哪一组工具。
type AgentRole string

const (
	// AgentRoleOps 运维 agent：站长本人使用，带工具（读渠道/改状态/查日志等）。
	AgentRoleOps AgentRole = "ops"
	// AgentRoleSupport 客服 agent：面向外部使用者，无任何工具。
	//
	// 刻意不提供只读工具：客服面对的是公网输入，
	// 一旦它能读到站内数据，提示注入就能把数据套出来。
	// 「问问题」这件事本身不需要查数据库的能力。
	AgentRoleSupport AgentRole = "support"
)

// 密钥状态（与 Token/用户状态保持同一套语义，便于后台统一渲染徽标）。
const (
	AgentKeyStatusEnabled  = 1
	AgentKeyStatusDisabled = 2
)

// ErrAgentKeyNotFound 表示未找到指定 agent 密钥。
var ErrAgentKeyNotFound = errors.New("model: agent 密钥不存在")

// GenerateAgentKey 生成一个新的 agent 密钥，形如 ak-<64 位十六进制>。
//
// 使用 crypto/rand：必须用密码学安全随机源。
// 密钥强度直接等于"站内数据能否被陌生人读到"，非密码学随机源在这里是致命缺陷。
func GenerateAgentKey() (string, error) {
	buf := make([]byte, agentKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("model: 生成 agent 密钥随机数失败: %w", err)
	}
	return AgentKeyPrefix + hex.EncodeToString(buf), nil
}

// HashAgentKey 计算密钥明文的摘要，用于落库与鉴权查询。
//
// 与令牌走同一个 SHA-256Hex：两者都不需要可逆——
// 鉴权时把请求头里的明文算一次摘要去比对即可，无需解密。
// 落库只存摘要，数据库被读走时攻击者拿到的也不是可用密钥。
func HashAgentKey(plain string) string {
	return crypto.SHA256Hex(plain)
}

// ValidateAgentKeyRole 校验角色合法性。
//
// 不给"未知角色按 ops 处理"的兜底：角色决定工具授权，
// 一个拼错的角色若被默认成 ops，就等于给了一个带工具的公网入口。
func ValidateAgentKeyRole(role AgentRole) error {
	switch role {
	case AgentRoleOps, AgentRoleSupport:
		return nil
	default:
		return fmt.Errorf("agent 角色非法: %q", string(role))
	}
}

// AgentKey 是一条 agent 访问密钥。
//
// 明文只在生成时返回一次（见 Generate 返回值），落库仅存 KeyHash。
// 这与 tokens/sessions 同一策略：数据库被读走时，攻击者拿到的也只是摘要。
type AgentKey struct {
	ID   uint64    // 主键
	Role AgentRole // ops | support
	Name string    // 站长备注，如"给小程序用"
	// KeyHash 是 SHA-256 十六进制摘要。**永远不要往结构体里放明文**：
	// 这个结构体会被日志、调试输出引用，明文一旦进来就有泄漏面。
	KeyHash string
	Status  int
	// ExpiresAt 为过期时间；零值表示永不过期（落库为 0）。
	ExpiresAt time.Time
	CreatedAt time.Time
}

// IsActive 判断密钥当前是否可用于鉴权。
//
// 过期判定放在这里而不是调用处：agent 鉴权只有一条路径，
// 在多处重复"状态检查 + 过期检查"必然有一处漏掉，而漏掉的那处
// 表现为"过期的 key 仍然能用"——这是用户最不能接受的一类缺陷。
func (k *AgentKey) IsActive() bool {
	if k == nil || k.Status != AgentKeyStatusEnabled {
		return false
	}
	return !k.Expired()
}

// Expired 判断是否已过期（ExpiresAt 为零值时永不过期）。
func (k *AgentKey) Expired() bool {
	if k.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(k.ExpiresAt)
}

// NormalizeForCreate 在落库前补齐与规范化字段。
//
// 【Unix 零值陷阱，勿删这段】
//
//	expires_at 落库约定是「0 = 永不过期」（见 store/agent_key_repo.go）。
//	零值 time.Time 直接取 Unix() 会得到 -62135596800（公元 1 年）而不是 0，
//	那样每一条"永不过期"的密钥一创建就是已过期状态，
//	表现为"站长刚生成的 key 立刻失效"，且日志里没有任何错误可查。
//	把这层语义在模型层固定下来，仓储层就只需照 struct 判断，
//	不必依赖每个写入点都自己记得判零值。
//
// 顺带在此把状态补成"启用"：调用方忘了设 Status 时得到一把用不了的 key，
// 比默认启用更安全（默认启用意味着"忘了设就发出去了一把可用密钥"）。
func (k *AgentKey) NormalizeForCreate(now time.Time) {
	k.Name = strings.TrimSpace(k.Name)
	if k.Status == 0 {
		k.Status = AgentKeyStatusEnabled
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
}

// ValidateName 校验备注名。
//
// 单独暴露而不是只藏在 Validate 里：改名接口只需要校验名字，
// 若为此构造一个完整的 AgentKey 去跑 Validate，就得给不相关的字段
// 填上占位值（假 role、假摘要），那些占位值随时可能让校验以
// "role 非法"这种驴唇不对马嘴的理由失败。
func (k *AgentKey) ValidateName() error {
	nameLen := len([]rune(strings.TrimSpace(k.Name)))
	if nameLen < agentKeyMinNameLength {
		return errors.New("密钥备注不能为空")
	}
	if nameLen > agentKeyMaxNameLength {
		return fmt.Errorf("密钥备注过长（最多 %d 个字）", agentKeyMaxNameLength)
	}
	return nil
}

// Validate 校验密钥字段，供创建时调用。
func (k *AgentKey) Validate() error {
	if err := k.ValidateName(); err != nil {
		return err
	}
	if err := ValidateAgentKeyRole(k.Role); err != nil {
		return err
	}
	if strings.TrimSpace(k.KeyHash) == "" {
		// 空摘要意味着"这条密钥谁都能用"——鉴权时按空摘要查库不会命中，
		// 但一旦有任何一处把它当成"跳过校验"，就是彻底的无鉴权入口。
		return errors.New("密钥摘要不能为空")
	}
	if k.Status != AgentKeyStatusEnabled && k.Status != AgentKeyStatusDisabled {
		return fmt.Errorf("密钥状态非法: %d", k.Status)
	}
	if !k.ExpiresAt.IsZero() && !k.ExpiresAt.After(k.CreatedAt) {
		// 过期时间早于创建时间 = 一创建就过期。
		// 允许它只是因为站长可能确实想要"马上作废"的语义之外的情况，
		// 但这种情况用禁用表达更清楚，因此这里直接拒绝以免误用。
		return errors.New("过期时间必须晚于创建时间")
	}
	return nil
}

// IsOps 判断是否为运维 agent（带工具的那个）。
func (k *AgentKey) IsOps() bool {
	return k != nil && k.Role == AgentRoleOps
}

// roleLabel 是角色 → 后台展示文案。
//
// 为什么在前端做而不在这里：这份文案只在后台界面出现，
// 让后端下发会为了一个静态字符串给每次响应加一个字段。
var roleLabel = map[AgentRole]string{
	AgentRoleOps:     "运维助手",
	AgentRoleSupport: "在线客服",
}

// Label 返回角色的中文展示名；未知角色回退为原值而不是空串。
func (r AgentRole) Label() string {
	if label, ok := roleLabel[r]; ok {
		return label
	}
	return string(r)
}

// AgentKeyQuery 是 agent 密钥的查询条件。
type AgentKeyQuery struct {
	// Role 为空时不限角色。
	Role AgentRole
	// OnlyEnabled 为 true 时只返回启用中的密钥（后台"可用密钥"下拉用）。
	OnlyEnabled bool
	// Limit <= 0 时由仓储层套用默认值，上限同样在仓储层夹紧——
	// 限制必须落在数据访问处，调用方漏传一个上界就等于把库拉进内存。
	Limit int
}

// AgentKeyRepository 是 agent 密钥的持久化接口。
type AgentKeyRepository interface {
	// Create 新增一条密钥。传入的 Key.KeyHash 必须是摘要而非明文。
	Create(ctx context.Context, key *AgentKey) error

	// GetByHash 按摘要查找（鉴权路径专用）。
	// 未找到时返回 ErrAgentKeyNotFound。
	GetByHash(ctx context.Context, keyHash string) (*AgentKey, error)

	// List 按条件查询，按 ID 升序返回（后台列表稳定排序）。
	List(ctx context.Context, q AgentKeyQuery) ([]*AgentKey, error)

	// SetStatus 启用 / 禁用一把密钥（禁用后立即失效，不必等过期）。
	SetStatus(ctx context.Context, id uint64, status int) error

	// UpdateName 修改备注名。
	//
	// 为什么单独一个方法而不是让 SetStatus 兼任：两件事的生命周期不同——
	// 状态影响"能不能用"（安全语义），备注只影响"站长能不能认出来"（展示语义）。
	// 合成一个方法后，调用方为了改个备注必须先读出整条记录，
	// 而顺手把它读出来的 status 又会被一起写回——一次"改备注"请求因此
	// 有了把密钥状态改掉的能力，这是典型的越权面。
	UpdateName(ctx context.Context, id uint64, name string) error

	// Delete 永久删除一把密钥。
	//
	// 不做软删除：密钥撤销后必须【立刻不可用】，
	// 留一条"已删除但记录还在"的行只会让人以为还能用。
	Delete(ctx context.Context, id uint64) error
}
