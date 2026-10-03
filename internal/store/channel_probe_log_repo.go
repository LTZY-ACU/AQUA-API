// 本文件是 model.ChannelProbeLogRepository 的 SQL 实现。
//
// 意图（Why）：
//
//	探针历史是"高频追加、低频读取、会被保留期删除"的数据，
//	这三个特征决定了实现取向：写路径要极简（一次 INSERT，不做额外查询），
//	读路径要能走索引（按时间范围 + 渠道过滤 + 时间倒序），
//	删除路径要能被时间条件命中。
//
// 实现取舍（为什么这样写）：
//   - 追加不回填渠道名：渠道名会改，存快照会让历史记录与渠道对不上号。
//     读历史时按 channel_id 回查当前渠道名——用户看到的是"这个渠道历史上如何"，
//     而不是"这个渠道曾经叫什么"；
//   - List 不 JOIN channels：JOIN 会让"渠道已删除"的历史变成查不出来，
//     而渠道被删之后恰恰最需要看它最后几次探测是什么结果；
//   - 条件拼装与 alert_channel_repo 保持同一套风格（strings.Builder + 占位符），
//     不用字符串拼接用户输入，杜绝注入面。
//
// 流转（Flow）：
//
//	巡检 → Append（一次 INSERT）
//	看板 → List（时间倒序）/ Count
//	清理 → Retention.Purge 直接 DELETE（不经过本文件，见 retention.go 的理由）
//
// 扩展（Extend）：
//	需要"按小时降采样"：在本文件加一个 GroupByInterval 方法，
//	用 (channel_id, at) 索引做区间聚合；不要在调用方拉全量再在内存里分桶。
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

// channelProbeLogColumns 是 channel_probe_logs 的列清单（读写共用，避免两处手写导致错位）。
const channelProbeLogColumns = `id, channel_id, at, ok, latency_ms, status_code, model, message`

// channelProbeLogRepository 是 model.ChannelProbeLogRepository 的 SQL 实现。
//
// 并发安全：只持有 *sql.DB（自带连接池），无内部状态。
type channelProbeLogRepository struct {
	db *sql.DB
}

// NewChannelProbeLogRepository 创建探针历史仓储。
func NewChannelProbeLogRepository(db *sql.DB) model.ChannelProbeLogRepository {
	return &channelProbeLogRepository{db: db}
}

// Append 追加一条探测历史。
//
// 只做一次 INSERT 且不回填 ID：这张表的读取一律按时间排序，
// 自增 ID 在时间线里没有意义（探测可能乱序到达，ID 序不等于时间序），
// 回填它反而会诱使调用方误用 ID 排序。
func (r *channelProbeLogRepository) Append(ctx context.Context, log *model.ChannelProbeLog) error {
	if log == nil {
		return errors.New("store: 探针历史为空")
	}
	at := log.At
	if at.IsZero() {
		// 兜底成"现在"：时间为零会让这行在按时间过滤时永远查不到，
		// 而一个没有时间戳的历史记录等于没有记录。
		at = time.Now()
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO channel_probe_logs (channel_id, at, ok, latency_ms, status_code, model, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		log.ChannelID, at.Unix(), boolToInt(log.OK), log.LatencyMS, log.StatusCode,
		log.Model, log.Message); err != nil {
		return fmt.Errorf("store: 追加探针历史失败: %w", err)
	}
	return nil
}

// List 按条件返回时间线（时间倒序）。
func (r *channelProbeLogRepository) List(ctx context.Context, q model.ChannelProbeLogQuery) ([]*model.ChannelProbeLog, error) {
	where, args := buildProbeLogWhere(q)

	sb := strings.Builder{}
	sb.WriteString("SELECT " + channelProbeLogColumns + " FROM channel_probe_logs")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	// 二级排序键用 id：同一秒内可能有多条（管理员点测活与巡检撞在同一秒），
	// 只按 at 排序时它们的相对顺序由存储引擎决定，表现为"刷新一次顺序变一次"。
	sb.WriteString(" ORDER BY at DESC, id DESC LIMIT ?")
	args = append(args, probeLogLimit(q.Limit))

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询探针历史失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*model.ChannelProbeLog
	for rows.Next() {
		var (
			l       model.ChannelProbeLog
			at      int64
			ok      int
			message sql.NullString
		)
		if err := rows.Scan(&l.ID, &l.ChannelID, &at, &ok, &l.LatencyMS,
			&l.StatusCode, &l.Model, &message); err != nil {
			return nil, fmt.Errorf("store: 读取探针历史失败: %w", err)
		}
		l.At = time.Unix(at, 0)
		l.OK = ok != 0
		l.Message = message.String
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历探针历史失败: %w", err)
	}
	return out, nil
}

// Count 返回符合条件的行数。
func (r *channelProbeLogRepository) Count(ctx context.Context, q model.ChannelProbeLogQuery) (int64, error) {
	where, args := buildProbeLogWhere(q)

	sb := strings.Builder{}
	sb.WriteString("SELECT COUNT(1) FROM channel_probe_logs")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}

	var n int64
	if err := r.db.QueryRowContext(ctx, sb.String(), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: 统计探针历史数失败: %w", err)
	}
	return n, nil
}

// probeLogDefaultLimit 是未指定条数时的默认返回行数。
//
// 取 200：足够画一条有起伏的曲线，又不至于让一次看板请求拉回整个历史。
// 需要更多就该由调用方显式缩小时间范围——那比默认拉大结果集更可控。
const probeLogDefaultLimit = 200

// probeLogMaxLimit 是单次查询的硬上限（防止调用方传入超大值拖垮进程）。
const probeLogMaxLimit = 1000

// probeLogLimit 把请求里的 Limit 归一到合法范围。
//
// 上限必须存在：这张表是"只写不删"型的（受保留期约束），
// 一旦有人把保留期配成 0（永久留痕），一个 limit=0 或 limit=-1 的请求
// 就足以把整张表拉进内存。
func probeLogLimit(limit int) int {
	if limit <= 0 {
		return probeLogDefaultLimit
	}
	if limit > probeLogMaxLimit {
		return probeLogMaxLimit
	}
	return limit
}

// buildProbeLogWhere 构造时间线查询的 WHERE 子句与参数（List 与 Count 共用）。
//
// 共用的意义：若两处各写一份，迟早会出现"列表按时间过滤、计数忘了过滤"，
// 看板就会显示"共 12 次探测"而实际只列了最近 3 次。
func buildProbeLogWhere(q model.ChannelProbeLogQuery) (string, []any) {
	var (
		conditions []string
		args       []any
	)
	if q.ChannelID > 0 {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, q.ChannelID)
	}
	if !q.Since.IsZero() {
		conditions = append(conditions, "at >= ?")
		args = append(args, q.Since.Unix())
	}
	if !q.Until.IsZero() {
		conditions = append(conditions, "at <= ?")
		args = append(args, q.Until.Unix())
	}
	if q.OnlyFailures {
		conditions = append(conditions, "ok = 0")
	}
	return strings.Join(conditions, " AND "), args
}
