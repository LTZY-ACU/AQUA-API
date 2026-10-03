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
	"errors"
	"time"
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

	// Delete 永久删除一把密钥。
	//
	// 不做软删除：密钥撤销后必须【立刻不可用】，
	// 留一条"已删除但记录还在"的行只会让人以为还能用。
	Delete(ctx context.Context, id uint64) error
}
