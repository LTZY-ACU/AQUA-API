// Package model 的这一文件定义"本站账号 ↔ 第三方平台账号"的绑定关系。
//
// 意图（Why）：
//
//	支持第三方登录（当前为 QIU 科技账号）就必须持久化"哪个本站账号对应哪个外部账号"。
//	没有它，用户第二次用同一外部账号登录会被当成陌生人再建一个号：
//	额度、令牌、调用记录全都不见——既是体验灾难，也是数据污染。
//
// 流转（Flow）：
//
//	登录：GetByExternalID(provider, externalID) → 命中则签发该用户的会话；
//	      未命中则先建号（是否允许取决于站点注册开关）→ Bind 落绑定关系
//	绑定：已登录用户 → Bind → 之后即可用第三方账号直接登录
//
// 扩展（Extend）：
//
//	接入第二个平台：复用同一个 provider 字段，无需改结构；
//	增加"解绑"：在仓储加 Delete，注意别让用户把自己锁在门外（至少留一种登录方式）。
package model

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ExternalAccountProvider 是受支持的第三方登录平台标识。
//
// 用带类型的字符串而不是 int：它会出现在日志、导出数据与调试语句里，
// 一个看得懂的 "qiu" 比一段数字常量易用得多。
type ExternalAccountProvider string

// 受支持的第三方平台。
const (
	// ExternalProviderQIU 是 QIU 科技账号。
	ExternalProviderQIU ExternalAccountProvider = "qiu"
)

// String 返回平台标识的字符串形式。
func (p ExternalAccountProvider) String() string {
	return string(p)
}

// NormalizeExternalProvider 归一化平台标识：去空格转小写，不支持的值返回空串。
//
// 返回空串而非原样返回的意义：调用方用 err != nil 判断"不支持的平台"，
// 避免把用户传来的任意字符串一路带到 SQL 里。
func NormalizeExternalProvider(raw string) (ExternalAccountProvider, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch ExternalAccountProvider(trimmed) {
	case ExternalProviderQIU:
		return ExternalProviderQIU, nil
	default:
		return "", fmt.Errorf("不支持的第三方登录平台: %s", raw)
	}
}

// ExternalAccount 描述一条第三方账号绑定关系。
type ExternalAccount struct {
	ID               uint64                  // 主键
	UserID           uint64                  // 本站用户 id
	Provider         ExternalAccountProvider // 平台标识
	ExternalID       string                  // 外部平台的稳定用户 id
	ExternalUsername string                  // 外部用户名（快照，仅展示）
	Nickname         string                  // 外部昵称（快照，仅展示）
	CreatedAt        time.Time               // 绑定时间
}

// Validate 校验绑定关系本身的必要字段。
//
// 为什么 ExternalID 必须非空：它是唯一的匹配依据，
// 空值会让"两个不同的外部账号"在去重时塌缩成同一条，后果是串号。
func (a *ExternalAccount) Validate() error {
	if a.UserID == 0 {
		return fmt.Errorf("绑定关系缺少本站用户 id")
	}
	if a.Provider == "" {
		return fmt.Errorf("绑定关系缺少平台标识")
	}
	if strings.TrimSpace(a.ExternalID) == "" {
		return fmt.Errorf("绑定关系缺少外部账号 id")
	}
	return nil
}

// ErrExternalAccountNotFound 表示按给定组合查不到绑定关系。
var ErrExternalAccountNotFound = fmt.Errorf("第三方账号未绑定")

// ErrExternalAccountTaken 表示该外部账号已绑定到别的本站用户。
//
// 单独定义是为了让上层能给出"请先在原账号解绑"这样可操作的提示，
// 而不是把数据库的唯一约束错误直接抛给用户。
var ErrExternalAccountTaken = fmt.Errorf("该第三方账号已绑定到其它账号")

// ExternalAccountRepository 定义第三方账号绑定的持久化操作。
type ExternalAccountRepository interface {
	// Create 新增一条绑定关系，成功后回填 ID 与创建时间。
	//
	// 同一 (provider, external_id) 重复绑定时返回 ErrExternalAccountTaken：
	// 这是"一个外部账号对应多个本站账号"的最后一道防线，靠上层判断会漏。
	Create(ctx context.Context, item *ExternalAccount) error

	// GetByExternalID 按平台与外键 ID 查询绑定关系；不存在返回 ErrExternalAccountNotFound。
	//
	// 这是第三方登录的【唯一匹配入口】：昵称与用户名都可能被外部平台的用户随意修改，
	// 拿它们去匹配本地账号等同于把账号接管权交出去。
	GetByExternalID(ctx context.Context, provider ExternalAccountProvider, externalID string) (*ExternalAccount, error)

	// ListByUser 列出该本站用户绑定的所有第三方账号，供门户页展示与管理。
	ListByUser(ctx context.Context, userID uint64) ([]*ExternalAccount, error)

	// DeleteByUserAndProvider 解除该用户在该平台上的绑定。
	//
	// 返回受影响的行数：0 表示本来就没有绑定（调用方据此给出不同提示）。
	DeleteByUserAndProvider(ctx context.Context, userID uint64, provider ExternalAccountProvider) (int64, error)
}
