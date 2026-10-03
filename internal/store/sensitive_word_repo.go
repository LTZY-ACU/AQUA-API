// 本文件是 model.SensitiveWordRepository 的 SQL 实现（敏感词表）。
//
// 意图（Why）：
//
//	词表是"配置类"数据：量小（数百条以内）、读极频繁（匹配器构建时全量加载）、
//	写很少（后台手工维护）。因此本层只做纯粹存取，不做增量缓存——
//	匹配器由 server 中间件整体重建（见 middleware.SensitiveFilter），
//	比在这里维护"增量更新"简单得多，也不会出现"缓存与库不一致"的中间态。
//
//	写入前统一把词条归一化为「去首尾空白 + 小写」（model.SensitiveWord.MatchKey）：
//	这样库里的唯一索引就等于"同义词条只能有一条"，
//	不会出现 "BadWord" 与 "badword" 并存、删掉一条另一条仍在生效的诡异现象。
//
// 流转（Flow）：
//
//	NewSensitiveWordRepository(db)
//	  ├─ 后台：server → Create / CreateMany / GetByID / Update / Delete / List(false)
//	  └─ 过滤：server 中间件 → List(true) → 编译匹配器
//
// 扩展（Extend）：
//
//	新增字段：先建迁移加列，再同步本文件的 sensitiveWordColumns / scanSensitiveWord /
//	Create / Update 四处列清单，缺一处即静默丢数据。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// sensitiveWordColumns 集中定义查询列，顺序必须与 scanSensitiveWord 的扫描顺序严格一致。
const sensitiveWordColumns = `id, word, category, enabled, remark, created_at, updated_at`

// sensitiveWordRepository 是 model.SensitiveWordRepository 的 SQL 实现，并发安全。
type sensitiveWordRepository struct {
	db *sql.DB
}

// NewSensitiveWordRepository 创建敏感词仓储。
func NewSensitiveWordRepository(db *sql.DB) model.SensitiveWordRepository {
	return &sensitiveWordRepository{db: db}
}

// Create 新增词条。
func (r *sensitiveWordRepository) Create(ctx context.Context, word *model.SensitiveWord) error {
	if word == nil {
		return errors.New("store: 敏感词为空")
	}
	normalizeSensitiveWord(word)
	if err := word.Validate(); err != nil {
		return fmt.Errorf("store: 敏感词非法: %w", err)
	}

	now := time.Now()
	word.CreatedAt = now
	word.UpdatedAt = now

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO sensitive_words
			(word, category, enabled, remark, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		word.Word, word.Category, boolToInt(word.Enabled), word.Remark,
		word.CreatedAt.Unix(), word.UpdatedAt.Unix(),
	)
	if err != nil {
		// 唯一索引冲突即"该词已存在"。用错误文本判断而非预查：预查存在并发窗口。
		if isUniqueViolation(err) {
			return model.ErrSensitiveWordDuplicated
		}
		return fmt.Errorf("store: 新增敏感词失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取新增敏感词 ID 失败: %w", err)
	}
	word.ID = uint64(id)
	return nil
}

// CreateMany 批量新增词条，跳过已存在的，返回实际新增数量。
//
// 实现说明（重要）：用 INSERT OR IGNORE 而不是"逐条先查后插"——
// 后者在批量导入上千条时会发出上千次查询，且存在并发窗口；
// 单条 SQL 交给数据库用唯一索引去重，既快又准。
//
// 注意：本方法整体在【一个事务】内执行。若中途出现非"重复"类错误（如磁盘满），
// 已插入的部分会一并回滚，避免留下"导入了一半"的混乱状态。
func (r *sensitiveWordRepository) CreateMany(ctx context.Context, words []*model.SensitiveWord) (int, error) {
	if len(words) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: 开启敏感词批量导入事务失败: %w", err)
	}
	// 出错时回滚；成功提交后 Rollback 返回 ErrTxDone，可安全忽略。
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	inserted := 0

	for _, word := range words {
		if word == nil {
			continue
		}
		normalizeSensitiveWord(word)
		if err := word.Validate(); err != nil {
			return 0, fmt.Errorf("store: 敏感词非法: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO sensitive_words
				(word, category, enabled, remark, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			word.Word, word.Category, boolToInt(word.Enabled), word.Remark,
			now.Unix(), now.Unix(),
		)
		if err != nil {
			return 0, fmt.Errorf("store: 批量写入敏感词失败: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: 读取批量导入影响行数失败: %w", err)
		}
		if affected > 0 {
			inserted++
			word.CreatedAt = now
			word.UpdatedAt = now
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: 提交敏感词批量导入失败: %w", err)
	}
	return inserted, nil
}

// GetByID 按主键查询词条。
func (r *sensitiveWordRepository) GetByID(ctx context.Context, id uint64) (*model.SensitiveWord, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+sensitiveWordColumns+" FROM sensitive_words WHERE id = ?", id)

	word, err := scanSensitiveWord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrSensitiveWordNotFound
		}
		return nil, err
	}
	return word, nil
}

// List 查询词条（按创建时间升序，保证后台展示顺序稳定）。
func (r *sensitiveWordRepository) List(ctx context.Context, enabledOnly bool) ([]*model.SensitiveWord, error) {
	query := "SELECT " + sensitiveWordColumns + " FROM sensitive_words"
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	// id 升序作为兜底排序键：同一秒内导入的批量词条也必须顺序稳定，
	// 否则后台每次刷新看到的顺序都在变，无法核对。
	query += " ORDER BY created_at ASC, id ASC"

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: 查询敏感词失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	words := make([]*model.SensitiveWord, 0, 64)
	for rows.Next() {
		word, err := scanSensitiveWord(rows)
		if err != nil {
			return nil, err
		}
		words = append(words, word)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历敏感词失败: %w", err)
	}
	return words, nil
}

// Update 按 ID 更新词条。
func (r *sensitiveWordRepository) Update(ctx context.Context, word *model.SensitiveWord) error {
	if word == nil {
		return errors.New("store: 敏感词为空")
	}
	if word.ID == 0 {
		return errors.New("store: 更新敏感词时 ID 不能为 0")
	}
	normalizeSensitiveWord(word)
	if err := word.Validate(); err != nil {
		return fmt.Errorf("store: 敏感词非法: %w", err)
	}

	// 刻意不更新 created_at：创建时间应保持不可变，便于审计。
	res, err := r.db.ExecContext(ctx, `
		UPDATE sensitive_words
		SET word = ?, category = ?, enabled = ?, remark = ?, updated_at = ?
		WHERE id = ?`,
		word.Word, word.Category, boolToInt(word.Enabled), word.Remark,
		time.Now().Unix(), word.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return model.ErrSensitiveWordDuplicated
		}
		return fmt.Errorf("store: 更新敏感词 %d 失败: %w", word.ID, err)
	}
	return sensitiveWordAffectedOrNotFound(res)
}

// Delete 按 ID 删除词条。
func (r *sensitiveWordRepository) Delete(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM sensitive_words WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: 删除敏感词 %d 失败: %w", id, err)
	}
	return sensitiveWordAffectedOrNotFound(res)
}

// scanSensitiveWord 把一行数据映射为词条对象。
func scanSensitiveWord(sc rowScanner) (*model.SensitiveWord, error) {
	var (
		id        uint64
		word      string
		category  string
		enabled   int
		remark    string
		createdAt int64
		updatedAt int64
	)

	if err := sc.Scan(&id, &word, &category, &enabled, &remark, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取敏感词字段失败: %w", err)
	}

	return &model.SensitiveWord{
		ID:        id,
		Word:      word,
		Category:  category,
		Enabled:   enabled != 0,
		Remark:    remark,
		CreatedAt: time.Unix(createdAt, 0),
		UpdatedAt: time.Unix(updatedAt, 0),
	}, nil
}

// normalizeSensitiveWord 归一化词条的写入字段。
//
// 只归一化 Word（去空白 + 小写）：它是唯一索引与匹配的唯一依据。
// Category / Remark 允许站长随意填写（含中文与空格），
// 只在两端去空白，避免"看起来没填但其实是空格"的记录。
func normalizeSensitiveWord(word *model.SensitiveWord) {
	word.Word = word.MatchKey()
	word.Category = strings.TrimSpace(word.Category)
	word.Remark = strings.TrimSpace(word.Remark)
}

// sensitiveWordAffectedOrNotFound 依据受影响行数判断操作是否命中记录。
func sensitiveWordAffectedOrNotFound(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrSensitiveWordNotFound
	}
	return nil
}
