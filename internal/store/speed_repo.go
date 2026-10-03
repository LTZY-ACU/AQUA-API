// 本文件是 model.ModelSpeedRepository 的 SQL 实现（模型测速结果）。
//
// 意图（Why）：
//
//	测速结果是"当前状态快照"：每个渠道 × 模型只留最近一次，读法固定为全量取回
//	（行数上界是渠道数 × 模型数，千级），因此本层不需要缓存，也不需要分页——
//	任何聚合（广场取 MIN、后台按渠道过滤）都交给调用方在内存里做，逻辑更简单。
//
// 流转（Flow）：
//
//	NewModelSpeedRepository(db)
//	  ├─ 测速接口：server.handleSpeedTestChannel → Upsert（每模型一行）
//	  ├─ 模型广场：handleModelPlaza → Latest → 内存聚合各渠道 MIN(ttfb)
//	  └─ 渠道删除：handleDeleteChannel → DeleteByChannel（清理孤儿数据）
//
// 扩展（Extend）：
//
//	新增列时：先建迁移，再同步本文件的 modelSpeedColumns 与 scanModelSpeed
//	两处列清单（缺一处即静默丢数据）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// modelSpeedColumns 集中定义查询列，顺序必须与 scanModelSpeed 的扫描顺序严格一致。
const modelSpeedColumns = `id, channel_id, model, upstream_model, ok, status_code,
	ttfb_ms, total_ms, message, tested_at`

// modelSpeedRepository 是 model.ModelSpeedRepository 的 SQL 实现，并发安全。
type modelSpeedRepository struct {
	db *sql.DB
}

// NewModelSpeedRepository 创建模型测速结果仓储。
func NewModelSpeedRepository(db *sql.DB) model.ModelSpeedRepository {
	return &modelSpeedRepository{db: db}
}

// Upsert 写入一条测速结果：同一 (channel_id, model) 只保留最近一次。
//
// 用 INSERT ... ON CONFLICT DO UPDATE 而不是"先查后写"：
// 后者在并发测速（两个管理员同时测同一渠道）时会竞态出两行或覆盖旧值失败；
// UPSERT 把唯一性约束交给数据库，天然幂等。
func (r *modelSpeedRepository) Upsert(ctx context.Context, result *model.ModelSpeedResult) error {
	if result == nil {
		return errors.New("store: 测速结果为空")
	}
	if err := result.Normalize(); err != nil {
		return fmt.Errorf("store: 测速结果非法: %w", err)
	}

	// tested_at 由调用方给定（测速发生时刻，而非写库时刻）：
	// 写库可能因重试而晚于测速，用测速时刻才能正确回答"这是多久前测的"。
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO model_speed_results
			(channel_id, model, upstream_model, ok, status_code, ttfb_ms, total_ms, message, tested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, model) DO UPDATE SET
			upstream_model = excluded.upstream_model,
			ok = excluded.ok,
			status_code = excluded.status_code,
			ttfb_ms = excluded.ttfb_ms,
			total_ms = excluded.total_ms,
			message = excluded.message,
			tested_at = excluded.tested_at`,
		result.ChannelID, result.Model, result.UpstreamModel,
		boolToInt(result.OK), result.StatusCode, result.TTFBMS, result.TotalMS,
		result.Message, result.TestedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: 写入模型测速结果失败: %w", err)
	}
	return nil
}

// Latest 返回全部「渠道 × 模型」的最近一次测速结果。
//
// 查询失败时返回错误（调用方决定降级方式：广场选择"不展示延迟"，
// 而不是让整页挂掉）。
func (r *modelSpeedRepository) Latest(ctx context.Context) ([]*model.ModelSpeedResult, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+modelSpeedColumns+" FROM model_speed_results ORDER BY model ASC, channel_id ASC")
	if err != nil {
		return nil, fmt.Errorf("store: 查询模型测速结果失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	results := make([]*model.ModelSpeedResult, 0, 64)
	for rows.Next() {
		item, err := scanModelSpeed(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历模型测速结果失败: %w", err)
	}
	return results, nil
}

// DeleteByChannel 删除某渠道的全部测速结果。
func (r *modelSpeedRepository) DeleteByChannel(ctx context.Context, channelID uint64) error {
	if channelID == 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		"DELETE FROM model_speed_results WHERE channel_id = ?", channelID); err != nil {
		return fmt.Errorf("store: 删除渠道 %d 的测速结果失败: %w", channelID, err)
	}
	return nil
}

// scanModelSpeed 把一行数据映射为测速结果对象。
func scanModelSpeed(sc rowScanner) (*model.ModelSpeedResult, error) {
	var (
		id            uint64
		channelID     uint64
		modelName     string
		upstreamModel string
		ok            int
		statusCode    int
		ttfbMS        int
		totalMS       int
		message       string
		testedAt      int64
	)

	if err := sc.Scan(&id, &channelID, &modelName, &upstreamModel, &ok, &statusCode,
		&ttfbMS, &totalMS, &message, &testedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取模型测速结果字段失败: %w", err)
	}

	return &model.ModelSpeedResult{
		ChannelID:     channelID,
		Model:         modelName,
		UpstreamModel: upstreamModel,
		OK:            ok == 1,
		StatusCode:    statusCode,
		TTFBMS:        ttfbMS,
		TotalMS:       totalMS,
		Message:       message,
		TestedAt:      time.Unix(testedAt, 0),
	}, nil
}
