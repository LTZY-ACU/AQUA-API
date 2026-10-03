// 本文件是 model.CorpusRepository 的 SQL 实现（语料共建计划三张表）。
//
// 意图（Why）：
//
//	把"采哪些模型 / 采到了什么 / 谁免计费"三件事落成 SQL。
//	本文件最重要的一条约定是**只增不改**：样本一旦落库就不再做任何原地改写，
//	因为它是训练语料的原始素材，改写会破坏"这一条到底是不是用户原话"的可追溯性。
//
// 为什么要单独一张样本表（而不塞进 usage_logs）：
//
//	usage_logs 是每天被全表扫描的统计表；正文是 KB 级大字段，
//	混进去会让"看一眼用量"这种最频繁的查询变成磁盘灾难。
//	因此列表查询刻意只取正文的**前 400 字节**做预览，要看全文必须走 GetCorpusSample
//	（接口层会对"看全文"写审计日志）——让"顺手翻用户对话"在流程上不成立。
//
// 流转（Flow）：
//
//	CreateCorpusSample ← relay 采集完成
//	ListCorpusSamples / GetCorpusSample / IterateCorpusSamples → 后台与导出
//	EnabledCorpusModels / ActiveCorpusGrants → corpus.Guard 加载内存快照
//
// 扩展（Extend）：
//
//	新增采集字段（如"上游返回的 finish_reason"）时，在迁移里加列，
//	并同步 corpusSampleColumns / scanCorpusSample / CreateCorpusSample 三处。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// corpusSamplePreviewBytes 是列表查询里正文预览的字节数。
//
// 取 400：足够接口层截出 200 字的可读预览，又不至于把一行 KB 级正文读进内存。
const corpusSamplePreviewBytes = 400

// corpusSampleColumns 集中定义查询列，顺序必须与 scanCorpusSample 的扫描顺序严格一致。
const corpusSampleColumns = `id, request_id, user_id, token_id, model, upstream_model,
	channel_id, channel_key_id, is_stream, status_code, request_body, response_body,
	request_bytes, response_bytes, truncated, incomplete, created_at`

// corpusRepository 是 model.CorpusRepository 的 SQL 实现，并发安全。
type corpusRepository struct {
	db *sql.DB
}

// NewCorpusRepository 创建语料共建计划仓储。
func NewCorpusRepository(db *sql.DB) model.CorpusRepository {
	return &corpusRepository{db: db}
}

// ---------------------------------------------------------------------------
// 一、语料模型清单
// ---------------------------------------------------------------------------

// ListCorpusModels 返回全部清单项（含已停用的），按模型名排序。
func (r *corpusRepository) ListCorpusModels(ctx context.Context) ([]*model.CorpusModel, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, model, enabled, remark, created_at, updated_at FROM corpus_models ORDER BY model")
	if err != nil {
		return nil, fmt.Errorf("store: 查询语料模型清单失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.CorpusModel, 0, 32)
	for rows.Next() {
		var (
			item                 model.CorpusModel
			enabled              int
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&item.ID, &item.Model, &enabled, &item.Remark, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: 读取语料模型清单失败: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = time.Unix(createdAt, 0)
		item.UpdatedAt = time.Unix(updatedAt, 0)
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历语料模型清单失败: %w", err)
	}
	return items, nil
}

// EnabledCorpusModels 返回启用中的模型名（供 Guard 加载快照）。
func (r *corpusRepository) EnabledCorpusModels(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT model FROM corpus_models WHERE enabled = 1 ORDER BY model")
	if err != nil {
		return nil, fmt.Errorf("store: 查询启用的语料模型失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	names := make([]string, 0, 32)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: 读取启用的语料模型失败: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历启用的语料模型失败: %w", err)
	}
	return names, nil
}

// UpsertCorpusModel 新增或更新一个清单项（按模型名匹配）。
func (r *corpusRepository) UpsertCorpusModel(ctx context.Context, item *model.CorpusModel) error {
	if item == nil {
		return errors.New("store: 语料模型清单项不能为空")
	}
	item.Normalize()
	if err := item.Validate(); err != nil {
		return err
	}
	now := time.Now()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO corpus_models (model, enabled, remark, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET enabled = excluded.enabled,
		                                 remark = excluded.remark,
		                                 updated_at = excluded.updated_at`,
		item.Model, boolToInt(item.Enabled), item.Remark, now.Unix(), now.Unix(),
	); err != nil {
		return fmt.Errorf("store: 写入语料模型 %s 失败: %w", item.Model, err)
	}
	return nil
}

// DeleteCorpusModel 从清单中移除一个模型。
//
// 只删清单项，**不删已采集的样本**：样本是历史事实，清单只是"是否继续采"。
// 误删清单不该连带毁掉已经攒下的语料。
func (r *corpusRepository) DeleteCorpusModel(ctx context.Context, name string) error {
	if _, err := r.db.ExecContext(ctx,
		"DELETE FROM corpus_models WHERE model = ?", strings.TrimSpace(name)); err != nil {
		return fmt.Errorf("store: 删除语料模型 %s 失败: %w", name, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 二、语料样本
// ---------------------------------------------------------------------------

// CreateCorpusSample 写入一条语料样本（request_id 重复时返回 ErrCorpusSampleExists）。
func (r *corpusRepository) CreateCorpusSample(ctx context.Context, sample *model.CorpusSample) error {
	if sample == nil {
		return errors.New("store: 语料样本不能为空")
	}
	if sample.CreatedAt.IsZero() {
		sample.CreatedAt = time.Now()
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO corpus_samples
			(request_id, user_id, token_id, model, upstream_model, channel_id, channel_key_id,
			 is_stream, status_code, request_body, response_body,
			 request_bytes, response_bytes, truncated, incomplete, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sample.RequestID, sample.UserID, sample.TokenID, sample.Model, sample.UpstreamModel,
		sample.ChannelID, sample.ChannelKeyID, boolToInt(sample.IsStream), sample.StatusCode,
		sample.RequestBody, sample.ResponseBody, sample.RequestBytes, sample.ResponseBytes,
		boolToInt(sample.Truncated), boolToInt(sample.Incomplete), sample.CreatedAt.Unix(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			// 幂等：同一次请求的样本只会有一条（重试/补偿不会写出两条）。
			return model.ErrCorpusSampleExists
		}
		return fmt.Errorf("store: 写入语料样本失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取语料样本 ID 失败: %w", err)
	}
	sample.ID = uint64(id)
	return nil
}

// corpusSampleFilter 把查询条件编译成 WHERE 子句与参数。
//
// 抽出来是为了让"列表 / 导出"两条路径共用同一套筛选语义——
// 否则很容易出现"列表里筛出来的和导出出来的不是同一批"这种对不上账的问题。
func corpusSampleFilter(q model.CorpusSampleQuery) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if name := strings.TrimSpace(q.Model); name != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, name)
	}
	if q.UserID > 0 {
		clauses = append(clauses, "user_id = ?")
		args = append(args, q.UserID)
	}
	if !q.From.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, q.From.Unix())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, q.To.Unix())
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// ListCorpusSamples 分页查询样本。
//
// 刻意只取正文前 corpusSamplePreviewBytes 字节：列表页只展示预览，
// 读全文是单条接口的事（那里会写审计日志）。
func (r *corpusRepository) ListCorpusSamples(ctx context.Context, q model.CorpusSampleQuery) ([]*model.CorpusSample, int, error) {
	where, args := corpusSampleFilter(q)

	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM corpus_samples"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: 统计语料样本失败: %w", err)
	}

	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	// 列清单与 scanCorpusSample 的顺序一致，只有正文两列换成预览片段。
	//
	// 参数顺序必须与 SQL 里占位符出现的顺序完全一致：
	// substr 的两个上限在【列清单】里，排在 WHERE 之前，因此先绑它们，
	// 再绑筛选条件，最后才是 LIMIT/OFFSET。顺序写错不会报错，
	// 只会静默地查出 0 行——这是本文件踩过的坑，勿改。
	params := make([]any, 0, len(args)+4)
	params = append(params, corpusSamplePreviewBytes, corpusSamplePreviewBytes)
	params = append(params, args...)
	params = append(params, limit, offset)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, request_id, user_id, token_id, model, upstream_model,
		       channel_id, channel_key_id, is_stream, status_code,
		       substr(request_body, 1, ?), substr(response_body, 1, ?),
		       request_bytes, response_bytes, truncated, incomplete, created_at
		FROM corpus_samples`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		params...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: 查询语料样本失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]*model.CorpusSample, 0, limit)
	for rows.Next() {
		item, err := scanCorpusSample(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: 遍历语料样本失败: %w", err)
	}
	return items, total, nil
}

// GetCorpusSample 取单条样本（含正文全文）。
func (r *corpusRepository) GetCorpusSample(ctx context.Context, id uint64) (*model.CorpusSample, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+corpusSampleColumns+" FROM corpus_samples WHERE id = ?", id)
	item, err := scanCorpusSample(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrCorpusSampleNotFound
		}
		return nil, err
	}
	return item, nil
}

// IterateCorpusSamples 按游标遍历样本（含正文全文），供导出使用。
//
// 用 `id > 上一批最大 id` 的游标分页而不是 OFFSET：导出期间新样本仍在写入，
// OFFSET 会因结果集变化而漏读或重复读；游标分页天然稳定。
func (r *corpusRepository) IterateCorpusSamples(ctx context.Context, q model.CorpusSampleQuery, fn func(*model.CorpusSample) error) error {
	if fn == nil {
		return nil
	}
	// 导出按时间正序（写入顺序），便于本地按会话顺序拼接。
	//
	// batch 取 20 而非 100：本查询 SELECT 的是**正文全文**
	// （request_body/response_body 各自上限 corpus.DefaultMaxBytes=16MiB），
	// 单行最坏约 32MiB。批次越大，驱动侧一次持有的结果集就越可能被放大到
	// 数百 MiB；20 行把最坏情况压到约 640MiB 量级，同时仍是主键区间扫描，
	// 分页次数对导出吞吐影响可忽略（导出本就是边读边写的后台任务）。
	const batch = 20

	lastID := uint64(0)
	for {
		where, args := corpusSampleFilter(q)
		if where == "" {
			where = " WHERE id > ?"
		} else {
			where += " AND id > ?"
		}
		args = append(args, lastID)

		rows, err := r.db.QueryContext(ctx,
			"SELECT "+corpusSampleColumns+" FROM corpus_samples"+where+" ORDER BY id LIMIT ?",
			append(args, batch)...)
		if err != nil {
			return fmt.Errorf("store: 导出语料样本失败: %w", err)
		}

		count := 0
		for rows.Next() {
			item, err := scanCorpusSample(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			count++
			lastID = item.ID
			if err := fn(item); err != nil {
				_ = rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: 遍历导出语料样本失败: %w", err)
		}
		_ = rows.Close()

		if count < batch {
			return nil
		}
	}
}

// StatCorpusSamples 返回语料库规模统计。
func (r *corpusRepository) StatCorpusSamples(ctx context.Context) (model.CorpusStat, error) {
	var (
		stat                    model.CorpusStat
		earliest, latest        sql.NullInt64
		samples, users          sql.NullInt64
		requestBytes, respBytes sql.NullInt64
	)
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(1), COUNT(DISTINCT user_id),
		       COALESCE(SUM(request_bytes), 0), COALESCE(SUM(response_bytes), 0),
		       MIN(created_at), MAX(created_at)
		FROM corpus_samples`).Scan(&samples, &users, &requestBytes, &respBytes, &earliest, &latest); err != nil {
		return model.CorpusStat{}, fmt.Errorf("store: 统计语料库失败: %w", err)
	}
	stat.Samples = samples.Int64
	stat.Users = users.Int64
	stat.RequestBytes = requestBytes.Int64
	stat.ResponseBytes = respBytes.Int64
	if earliest.Valid {
		stat.EarliestAt = time.Unix(earliest.Int64, 0)
	}
	if latest.Valid {
		stat.LatestAt = time.Unix(latest.Int64, 0)
	}
	return stat, nil
}

// DeleteCorpusSamplesBefore 删除某时间点之前的样本，返回删除条数。
//
// 用途：站长导出到本地后，按留存纪律把服务器上的原文清掉。
func (r *corpusRepository) DeleteCorpusSamplesBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM corpus_samples WHERE created_at < ?", before.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: 清理语料样本失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理影响行数失败: %w", err)
	}
	return affected, nil
}

// scanCorpusSample 从一行结果里读出语料样本。
//
// 入参用接口类型：列表（含预览片段）与单条（含全文）共用同一套列顺序，
// 避免两处各写一遍扫描逻辑而慢慢长歪。
func scanCorpusSample(row interface{ Scan(...any) error }) (*model.CorpusSample, error) {
	var (
		item                            model.CorpusSample
		isStream, truncated, incomplete int
		createdAt                       int64
	)
	if err := row.Scan(&item.ID, &item.RequestID, &item.UserID, &item.TokenID,
		&item.Model, &item.UpstreamModel, &item.ChannelID, &item.ChannelKeyID,
		&isStream, &item.StatusCode, &item.RequestBody, &item.ResponseBody,
		&item.RequestBytes, &item.ResponseBytes, &truncated, &incomplete, &createdAt); err != nil {
		// sql.ErrNoRows 需要原样向上传递，供调用方识别"不存在"。
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取语料样本失败: %w", err)
	}
	item.IsStream = isStream != 0
	item.Truncated = truncated != 0
	item.Incomplete = incomplete != 0
	item.CreatedAt = time.Unix(createdAt, 0)
	return &item, nil
}

// ---------------------------------------------------------------------------
// 三、特殊福利账户
// ---------------------------------------------------------------------------

// ListCorpusGrants 返回全部福利资格（含已撤销），按用户与模型排序。
func (r *corpusRepository) ListCorpusGrants(ctx context.Context) ([]*model.CorpusGrant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, model, free_access, status, remark, created_at, updated_at
		FROM corpus_grants ORDER BY user_id, model`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询福利资格失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanCorpusGrants(rows)
}

// ActiveCorpusGrants 返回生效中的福利资格（供 Guard 加载快照）。
func (r *corpusRepository) ActiveCorpusGrants(ctx context.Context) ([]*model.CorpusGrant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, model, free_access, status, remark, created_at, updated_at
		FROM corpus_grants WHERE status = ? ORDER BY user_id, model`,
		int(model.CorpusGrantActive))
	if err != nil {
		return nil, fmt.Errorf("store: 查询生效福利资格失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanCorpusGrants(rows)
}

// scanCorpusGrants 读取福利资格结果集（两处查询共用）。
func scanCorpusGrants(rows *sql.Rows) ([]*model.CorpusGrant, error) {
	items := make([]*model.CorpusGrant, 0, 8)
	for rows.Next() {
		var (
			item                 model.CorpusGrant
			freeAccess, status   int
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&item.ID, &item.UserID, &item.Model, &freeAccess, &status,
			&item.Remark, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: 读取福利资格失败: %w", err)
		}
		item.FreeAccess = freeAccess != 0
		item.Status = model.CorpusGrantStatus(status)
		item.CreatedAt = time.Unix(createdAt, 0)
		item.UpdatedAt = time.Unix(updatedAt, 0)
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历福利资格失败: %w", err)
	}
	return items, nil
}

// UpsertCorpusGrant 新增或更新一条福利资格（按"用户 × 模型"匹配）。
//
// 更新时会把 status 复位为生效：站长"重新发放"的语义是"恢复资格"，
// 不该因为历史行是撤销态而"发了却没用"。
func (r *corpusRepository) UpsertCorpusGrant(ctx context.Context, grant *model.CorpusGrant) error {
	if grant == nil {
		return errors.New("store: 福利资格不能为空")
	}
	name := strings.TrimSpace(grant.Model)
	if grant.UserID == 0 || name == "" {
		return errors.New("store: 福利资格需要同时指定用户与模型")
	}
	status := grant.Status
	if status == 0 {
		status = model.CorpusGrantActive
	}
	now := time.Now()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO corpus_grants (user_id, model, free_access, status, remark, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, model) DO UPDATE SET free_access = excluded.free_access,
		                                          status = excluded.status,
		                                          remark = excluded.remark,
		                                          updated_at = excluded.updated_at`,
		grant.UserID, name, boolToInt(grant.FreeAccess), int(status),
		strings.TrimSpace(grant.Remark), now.Unix(), now.Unix(),
	); err != nil {
		return fmt.Errorf("store: 写入福利资格失败: %w", err)
	}
	return nil
}

// DeleteCorpusGrant 撤销一条福利资格。
//
// 用 UPDATE 置撤销态而不是 DELETE：保留"曾经授过谁什么"的痕迹，
// 便于事后解释"他为什么一度免费"。
func (r *corpusRepository) DeleteCorpusGrant(ctx context.Context, userID uint64, name string) error {
	if _, err := r.db.ExecContext(ctx, `
		UPDATE corpus_grants SET status = ?, updated_at = ?
		WHERE user_id = ? AND model = ?`,
		int(model.CorpusGrantRevoked), time.Now().Unix(), userID, strings.TrimSpace(name),
	); err != nil {
		return fmt.Errorf("store: 撤销福利资格失败: %w", err)
	}
	return nil
}
