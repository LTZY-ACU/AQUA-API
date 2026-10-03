// 本文件是 model.EmailBroadcastRepository 的 SQL 实现（全站通知邮件群发）。
//
// 意图（Why）：
//
//	群发的账目必须能回答三个问题，本文件的实现取舍都围绕它们：
//	  1) "这个人发过没有？" —— 逐人一行落库（status），而不是依赖内存进度；
//	  2) "中断后能不能续？" —— NextPending 只取 status='pending' 的人，
//	     已发过的永远不会被再取到，重启续发天然幂等；
//	  3) "发了多少、谁失败了？" —— 计数由 RefreshProgress 从明细表重算（不加内存计数器），
//	     失败原因逐条落库，可查可核。
//
//	为什么 AddRecipients 用 INSERT OR IGNORE + 事务：
//	重建名单（例如重复点"发送"）不能把已存在的收件人覆盖成 pending，
//	否则会把"已投递"重置为"待发"，等于给已收到邮件的人再发一次。
//	唯一索引 (broadcast_id, user_id) 负责兜底，OR IGNORE 让它静默跳过；
//	整批放在一个事务里，避免"写了一半"留下半份名单。
//
// 流转（Flow）：
//
//	NewEmailBroadcastRepository(db)
//	  ├─ 创建：server → Create + AddRecipients
//	  ├─ 发送：broadcast 包 → NextPending + MarkRecipient + CountRecipients + UpdateProgress
//	  ├─ 重启续发：broadcast 包 → ListUnfinished
//	  └─ 后台查询：server → List + ListRecipients
//
// 扩展（Extend）：
//
//	新增字段：先建迁移加列，再同步本文件的 broadcastColumns /
//	broadcastRecipientColumns / scanEmailBroadcast / scanEmailBroadcastRecipient
//	与对应的 INSERT / UPDATE 语句。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// broadcastColumns 集中定义批次查询列，顺序必须与 scanEmailBroadcast 的扫描顺序严格一致。
//
// 说明：列表查询也会带出 body_html（正文快照）。这是刻意接受的：
// 正文只有几 KB、批次行数极少（一次运营通知一行），
// 而为它单独维护一份"不含正文"的列清单会引入"两处列清单不同步"的风险。
const broadcastColumns = `id, template, subject, body_html, status, total, sent, failed,
	created_by, created_at, updated_at, started_at, finished_at`

// broadcastRecipientColumns 集中定义收件人查询列，顺序必须与扫描顺序一致。
const broadcastRecipientColumns = `id, broadcast_id, user_id, email, status, error, sent_at, created_at`

// 批次与收件人列表的分页参数：默认 20，上限 100（与后台其他列表保持一致）。
const (
	defaultBroadcastPageSize = 20
	maxBroadcastPageSize     = 100
)

// emailBroadcastRepository 是 model.EmailBroadcastRepository 的 SQL 实现，并发安全。
type emailBroadcastRepository struct {
	db *sql.DB
}

// NewEmailBroadcastRepository 创建群发仓储。
func NewEmailBroadcastRepository(db *sql.DB) model.EmailBroadcastRepository {
	return &emailBroadcastRepository{db: db}
}

// Create 新建批次并回填 ID。
func (r *emailBroadcastRepository) Create(ctx context.Context, item *model.EmailBroadcast) error {
	if item == nil {
		return errors.New("store: 群发批次为空")
	}
	if err := item.Validate(); err != nil {
		return fmt.Errorf("store: 群发批次非法: %w", err)
	}

	now := time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = item.CreatedAt

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO email_broadcasts
			(template, subject, body_html, status, total, sent, failed,
			 created_by, created_at, updated_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Template, item.Subject, item.BodyHTML, string(item.Status),
		item.Total, item.Sent, item.Failed, item.CreatedBy,
		item.CreatedAt.Unix(), item.UpdatedAt.Unix(),
		unixOrZeroLocal(item.StartedAt), unixOrZeroLocal(item.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("store: 创建群发批次失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取新增批次 ID 失败: %w", err)
	}
	item.ID = uint64(id)
	return nil
}

// GetByID 按主键查询批次；不存在时返回 model.ErrEmailBroadcastNotFound。
func (r *emailBroadcastRepository) GetByID(ctx context.Context, id uint64) (*model.EmailBroadcast, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+broadcastColumns+" FROM email_broadcasts WHERE id = ?", id)

	item, err := scanEmailBroadcast(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrEmailBroadcastNotFound
		}
		return nil, err
	}
	return item, nil
}

// List 按创建时间倒序返回批次（含总数）。
func (r *emailBroadcastRepository) List(ctx context.Context, limit, offset int) ([]*model.EmailBroadcast, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM email_broadcasts").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计群发批次数量失败: %w", err)
	}

	limit = normalizeLimit(limit, defaultBroadcastPageSize, maxBroadcastPageSize)
	offset = normalizeOffset(offset)

	// 次级排序用 id DESC：同一秒内创建的批次也能有稳定顺序，分页不会错行。
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+broadcastColumns+" FROM email_broadcasts ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?",
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询群发批次列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.EmailBroadcast, 0, limit)
	for rows.Next() {
		item, err := scanEmailBroadcast(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历群发批次失败: %w", err)
	}
	return items, total, nil
}

// ListUnfinished 返回所有尚未处理完的批次（按 id 升序，先创建的先续发）。
func (r *emailBroadcastRepository) ListUnfinished(ctx context.Context) ([]*model.EmailBroadcast, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+broadcastColumns+" FROM email_broadcasts WHERE status IN (?, ?) ORDER BY id ASC",
		string(model.BroadcastStatusPending), string(model.BroadcastStatusRunning))
	if err != nil {
		return nil, fmt.Errorf("store: 查询未完成群发批次失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.EmailBroadcast, 0, 4)
	for rows.Next() {
		item, err := scanEmailBroadcast(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历未完成批次失败: %w", err)
	}
	return items, nil
}

// AddRecipients 批量写入收件人，返回实际新增条数；已存在的 (broadcast_id, user_id) 跳过。
func (r *emailBroadcastRepository) AddRecipients(ctx context.Context, broadcastID uint64, items []*model.EmailBroadcastRecipient) (int, error) {
	if broadcastID == 0 {
		return 0, errors.New("store: 写入收件人时批次 ID 不能为 0")
	}
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: 开启收件人写入事务失败: %w", err)
	}
	// 出错路径统一回滚；成功路径在末尾显式 Commit，重复 Commit 是安全的无操作。
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO email_broadcast_recipients
			(broadcast_id, user_id, email, status, error, sent_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("store: 准备收件人写入语句失败: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().Unix()
	added := 0
	for _, item := range items {
		if item == nil || item.UserID == 0 {
			continue
		}
		email := model.NormalizeEmail(item.Email)
		if email == "" {
			continue
		}
		res, err := stmt.ExecContext(ctx,
			broadcastID, item.UserID, email,
			model.RecipientStatusPending, "", 0, now)
		if err != nil {
			return added, fmt.Errorf("store: 写入收件人失败: %w", err)
		}
		if affected, err := res.RowsAffected(); err == nil {
			added += int(affected)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: 提交收件人名单失败: %w", err)
	}
	return added, nil
}

// NextPending 取该批次最前面的 N 个待发收件人。
func (r *emailBroadcastRepository) NextPending(ctx context.Context, broadcastID uint64, limit int) ([]*model.EmailBroadcastRecipient, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+broadcastRecipientColumns+`
		FROM email_broadcast_recipients
		WHERE broadcast_id = ? AND status = ?
		ORDER BY id ASC LIMIT ?`,
		broadcastID, model.RecipientStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询待发收件人失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.EmailBroadcastRecipient, 0, limit)
	for rows.Next() {
		item, err := scanEmailBroadcastRecipient(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历待发收件人失败: %w", err)
	}
	return items, nil
}

// MarkRecipient 更新单个收件人的最终状态。
//
// 注意：即使状态是"待发"也会被写入 —— 发送器只在两种最终状态间选择，
// 不做"待发→待发"的无意义更新；这里不做额外校验，保持写入路径最简单。
func (r *emailBroadcastRepository) MarkRecipient(ctx context.Context, id uint64, status, errMsg string, at time.Time) error {
	if status != model.RecipientStatusSent && status != model.RecipientStatusFailed {
		return fmt.Errorf("store: 收件人状态非法：%q", status)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE email_broadcast_recipients
		SET status = ?, error = ?, sent_at = ?
		WHERE id = ?`,
		status, errMsg, unixOrZeroLocal(at), id)
	if err != nil {
		return fmt.Errorf("store: 更新收件人状态失败: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("store: 收件人 %d 不存在", id)
	}
	return nil
}

// UpdateProgress 落库批次进度；startedAt / finishedAt 为零值时保持原值不变。
func (r *emailBroadcastRepository) UpdateProgress(ctx context.Context, id uint64, status model.BroadcastStatus,
	total, sent, failed int, startedAt, finishedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE email_broadcasts SET
			status = ?, total = ?, sent = ?, failed = ?, updated_at = ?,
			started_at = CASE WHEN ? > 0 THEN ? ELSE started_at END,
			finished_at = CASE WHEN ? > 0 THEN ? ELSE finished_at END
		WHERE id = ?`,
		string(status), total, sent, failed, time.Now().Unix(),
		unixOrZeroLocal(startedAt), unixOrZeroLocal(startedAt),
		unixOrZeroLocal(finishedAt), unixOrZeroLocal(finishedAt),
		id)
	if err != nil {
		return fmt.Errorf("store: 更新群发进度失败: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return model.ErrEmailBroadcastNotFound
	}
	return nil
}

// CountRecipients 统计该批次下指定状态的收件人数；status 为空表示全部。
func (r *emailBroadcastRepository) CountRecipients(ctx context.Context, broadcastID uint64, status string) (int, error) {
	sqlText := "SELECT COUNT(1) FROM email_broadcast_recipients WHERE broadcast_id = ?"
	args := []any{broadcastID}
	if status != "" {
		sqlText += " AND status = ?"
		args = append(args, status)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, sqlText, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 统计收件人数量失败: %w", err)
	}
	return total, nil
}

// ListRecipients 分页返回收件人明细（按 id 升序，与发送顺序一致）。
func (r *emailBroadcastRepository) ListRecipients(ctx context.Context, broadcastID uint64, status string,
	limit, offset int) ([]*model.EmailBroadcastRecipient, int, error) {
	where := " WHERE broadcast_id = ?"
	args := []any{broadcastID}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}

	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM email_broadcast_recipients"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计收件人数量失败: %w", err)
	}

	limit = normalizeLimit(limit, defaultBroadcastPageSize, maxBroadcastPageSize)
	offset = normalizeOffset(offset)

	rows, err := r.db.QueryContext(ctx,
		"SELECT "+broadcastRecipientColumns+" FROM email_broadcast_recipients"+where+
			" ORDER BY id ASC LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询收件人明细失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.EmailBroadcastRecipient, 0, limit)
	for rows.Next() {
		item, err := scanEmailBroadcastRecipient(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历收件人明细失败: %w", err)
	}
	return items, total, nil
}

// scanEmailBroadcast 把一行数据映射为批次对象。
func scanEmailBroadcast(sc rowScanner) (*model.EmailBroadcast, error) {
	var (
		id         uint64
		template   string
		subject    string
		bodyHTML   string
		status     string
		total      int
		sent       int
		failed     int
		createdBy  uint64
		createdAt  int64
		updatedAt  int64
		startedAt  int64
		finishedAt int64
	)

	if err := sc.Scan(&id, &template, &subject, &bodyHTML, &status, &total, &sent, &failed,
		&createdBy, &createdAt, &updatedAt, &startedAt, &finishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取群发批次字段失败: %w", err)
	}

	return &model.EmailBroadcast{
		ID:        id,
		Template:  template,
		Subject:   subject,
		BodyHTML:  bodyHTML,
		Status:    model.BroadcastStatus(status),
		Total:     total,
		Sent:      sent,
		Failed:    failed,
		CreatedBy: createdBy,
		CreatedAt: time.Unix(createdAt, 0),
		UpdatedAt: time.Unix(updatedAt, 0),
		// 0 表示"未开始 / 未结束"：转成零值时间，便于领域层用 IsZero 判断
		StartedAt:  unixToTimeOrZero(startedAt),
		FinishedAt: unixToTimeOrZero(finishedAt),
	}, nil
}

// scanEmailBroadcastRecipient 把一行数据映射为收件人对象。
func scanEmailBroadcastRecipient(sc rowScanner) (*model.EmailBroadcastRecipient, error) {
	var (
		id          uint64
		broadcastID uint64
		userID      uint64
		email       string
		status      string
		errMsg      string
		sentAt      int64
		createdAt   int64
	)

	if err := sc.Scan(&id, &broadcastID, &userID, &email, &status, &errMsg, &sentAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取收件人字段失败: %w", err)
	}

	return &model.EmailBroadcastRecipient{
		ID:          id,
		BroadcastID: broadcastID,
		UserID:      userID,
		Email:       email,
		Status:      status,
		Error:       errMsg,
		SentAt:      unixToTimeOrZero(sentAt),
		CreatedAt:   time.Unix(createdAt, 0),
	}, nil
}
