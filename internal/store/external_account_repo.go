// 本文件是 model.ExternalAccountRepository 的 SQL 实现（第三方账号绑定）。
//
// 意图（Why）：
//
//	第三方登录的正确性几乎完全压在"绑定关系可靠"上：
//	同一份外部身份只能对应一个本站账号，第二次登录必须回到同一个号。
//	因此本实现把这条不变式交给数据库（唯一索引）而不是靠上层自觉，
//	并把唯一约束的报错翻译领域错误 ErrExternalAccountTaken，
//	让上层能给出"该账号已被绑定"这样可操作的提示。
//
// 流转（Flow）：
//
//	server.handleQIULoginStatus
//	  → GetByExternalID("qiu", externalID) —— 登录时的唯一匹配入口
//	  → Create（首次登录自动建号后绑定 / 已登录用户主动绑定）
//
// 扩展（Extend）：
//
//	新增字段：先建迁移加列，再同步本文件的 externalAccountColumns / scanExternal /
//	Create 三处列清单。
//	接入新平台：无需改动本文件——provider 只是表里的一个字符串。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
)

// externalAccountColumns 是查询列清单，顺序必须与 scanExternal 的扫描顺序一致。
const externalAccountColumns = `id, user_id, provider, external_id, external_username, nickname, created_at`

// externalAccountRepository 是第三方账号绑定的仓储实现。
type externalAccountRepository struct {
	db *sql.DB
}

// NewExternalAccountRepository 创建第三方账号绑定仓储。
func NewExternalAccountRepository(db *sql.DB) model.ExternalAccountRepository {
	return &externalAccountRepository{db: db}
}

// Create 新增一条绑定关系。
func (r *externalAccountRepository) Create(ctx context.Context, item *model.ExternalAccount) error {
	if item == nil {
		return errors.New("store: 绑定关系为空")
	}
	if err := item.Validate(); err != nil {
		return err
	}

	item.CreatedAt = time.Now()
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO user_external_accounts
			(user_id, provider, external_id, external_username, nickname, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		item.UserID, item.Provider.String(), item.ExternalID,
		item.ExternalUsername, item.Nickname, item.CreatedAt.Unix())
	if err != nil {
		// 唯一索引冲突必须翻译成领域错误：所有方言的唯一约束报错文本都不一样，
		// 用字符串匹配去判据会随数据库版本失效，因此这里再查一次做交叉确认。
		if isUniqueViolation(err) {
			return model.ErrExternalAccountTaken
		}
		return fmt.Errorf("store: 新增第三方账号绑定失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取绑定关系主键失败: %w", err)
	}
	item.ID = uint64(id)
	return nil
}

// GetByExternalID 按平台与外部账号 id 查询绑定关系。
func (r *externalAccountRepository) GetByExternalID(ctx context.Context,
	provider model.ExternalAccountProvider, externalID string) (*model.ExternalAccount, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+externalAccountColumns+" FROM user_external_accounts WHERE provider = ? AND external_id = ?",
		provider.String(), externalID)
	item, err := scanExternal(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrExternalAccountNotFound
		}
		return nil, err
	}
	return item, nil
}

// ListByUser 列出该本站用户绑定的所有第三方账号（按绑定时间升序）。
func (r *externalAccountRepository) ListByUser(ctx context.Context, userID uint64) ([]*model.ExternalAccount, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+externalAccountColumns+" FROM user_external_accounts WHERE user_id = ? ORDER BY id ASC",
		userID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询第三方账号绑定失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.ExternalAccount, 0, 4)
	for rows.Next() {
		item, err := scanExternal(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历第三方账号绑定失败: %w", err)
	}
	return items, nil
}

// DeleteByUserAndProvider 解除该用户在指定平台上的绑定，返回删除条数。
func (r *externalAccountRepository) DeleteByUserAndProvider(ctx context.Context,
	userID uint64, provider model.ExternalAccountProvider) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM user_external_accounts WHERE user_id = ? AND provider = ?",
		userID, provider.String())
	if err != nil {
		return 0, fmt.Errorf("store: 解除第三方账号绑定失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取解绑影响行数失败: %w", err)
	}
	return affected, nil
}

// scanExternal 把一行数据映射为绑定关系对象。
func scanExternal(sc rowScanner) (*model.ExternalAccount, error) {
	var (
		id               uint64
		userID           uint64
		provider         string
		externalID       string
		externalUsername string
		nickname         string
		createdAt        int64
	)
	if err := sc.Scan(&id, &userID, &provider, &externalID,
		&externalUsername, &nickname, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取第三方账号绑定字段失败: %w", err)
	}
	normalized, err := model.NormalizeExternalProvider(provider)
	if err != nil {
		// 库里出现了不认识的平台：属于数据损坏或跨版本残留，
		// 明确报错好过静默把它当成空平台继续用。
		return nil, fmt.Errorf("store: 绑定关系中的平台标识非法: %w", err)
	}
	return &model.ExternalAccount{
		ID:               id,
		UserID:           userID,
		Provider:         normalized,
		ExternalID:       externalID,
		ExternalUsername: externalUsername,
		Nickname:         nickname,
		CreatedAt:        time.Unix(createdAt, 0),
	}, nil
}
