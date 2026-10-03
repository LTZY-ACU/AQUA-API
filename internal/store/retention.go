// 本文件实现「按保留期批量清理只写表」的维护功能（数据保留策略）。
//
// 意图（Why）：
//
//	usage_logs / audit_logs / quota_reservations / corpus_samples /
//	email_broadcast_recipients / channel_probe_logs 这六张表只写不删，
//	站点跑得越久库越大，最终撑爆磁盘并拖慢统计查询。它们又各有留痕价值，
//	不能一删了之，因此把"留多久"交給配置（config.Retention），这里只负责执行删除。
//
//	为什么放在 store 而不是新增 model 仓储方法：清理是数据库维护动作，
//	不是领域行为；为它改 3 个仓储接口会连带改动所有测试替身，收益为零。
//	审计与语料两张表已有现成的删除方法，这里直接复用，避免同一句 SQL 写两遍。
//
// 流转（Flow）：
//
//	cmd/aqua/main.go 每小时 → Retention.Purge（按 config.Retention 计算 cutoff）
//	  → usage_logs / quota_reservations / email_broadcast_recipients 直接执行 DELETE
//	  → audit_logs → AuditLogRepository.DeleteBefore
//	  → corpus_samples → CorpusRepository.DeleteCorpusSamplesBefore
//	→ main 汇总各表删除条数写日志（slog 在调用方，本包不持有日志器）
//
// 扩展（Extend）：
//
//	新增"要按保留期清理的表"：在 RetentionPolicy 加字段 → Purge 里加一段（天数 0 跳过）
//	→ config.RetentionConfig 加对应天数（默认值/环境变量/校验三处同步）→ main 传入。
//	清理条件变更时注意走索引：usage_logs/audit_logs/corpus_samples 有 created_at 索引；
//	quota_reservations 走 (status, expires_at) 索引；群发回执按所属批次的 created_at
//	（批次表极小，全表扫可接受）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// RetentionPolicy 是一轮清理的策略：各表保留天数，0 = 该表不清理。
type RetentionPolicy struct {
	// UsageLogDays 是用量明细的保留天数。
	UsageLogDays int
	// AuditLogDays 是审计日志的保留天数。
	AuditLogDays int
	// QuotaReservationDays 是已结算/已释放预留台账的保留天数；
	// 在途记录不受影响（由过期回收流程负责释放后才进入可清理范围）。
	QuotaReservationDays int
	// CorpusSampleDays 是语料样本的保留天数。
	CorpusSampleDays int
	// BroadcastDays 是群发回执的保留天数（按所属批次的创建时间判断）。
	BroadcastDays int
	// ProbeLogDays 是渠道探针历史的保留天数。
	ProbeLogDays int
}

// RetentionCounts 是一轮清理里各表实际删除的行数（0 = 无可删数据）。
type RetentionCounts struct {
	UsageLogs         int64
	AuditLogs         int64
	QuotaReservations int64
	CorpusSamples     int64
	Broadcasts        int64 // 群发回执行数
	ChannelProbeLogs  int64 // 渠道探针历史行数
}

// Total 返回本轮删除的总行数，便于一行日志判断"有没有动静"。
func (c RetentionCounts) Total() int64 {
	return c.UsageLogs + c.AuditLogs + c.QuotaReservations + c.CorpusSamples +
		c.Broadcasts + c.ChannelProbeLogs
}

// Retention 执行保留期清理。
type Retention struct {
	db     *sql.DB
	audit  model.AuditLogRepository
	corpus model.CorpusRepository
}

// NewRetention 构造清理器。
//
// audit / corpus 必须非空：这两张表的删除 SQL 已存在于各自仓储，
// 重复实现只会让两处逻辑慢慢长歪（同类教训见 audit_repo.DeleteBefore）。
func NewRetention(db *sql.DB, audit model.AuditLogRepository, corpus model.CorpusRepository) *Retention {
	return &Retention{db: db, audit: audit, corpus: corpus}
}

// Purge 删除各表中早于「保留期」的数据，返回各表删除行数。
//
// 行为约定：
//   - 保留天数为 0 的表整表跳过（运维显式要求永久留痕时用）；
//   - 单表失败不影响其它表：错误合并后一并返回，避免一张表出错就永远不清理别的表；
//   - 幂等：删除条件只依赖时间，多实例并发执行只是重复扫描，不会删多。
func (r *Retention) Purge(ctx context.Context, p RetentionPolicy, now time.Time) (RetentionCounts, error) {
	var (
		counts RetentionCounts
		errs   []error
	)

	counts.UsageLogs, errs = purgeWith(ctx, p.UsageLogDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		return execDelete(ctx, r.db,
			"DELETE FROM usage_logs WHERE created_at < ?", cutoff.Unix())
	})

	counts.AuditLogs, errs = purgeWith(ctx, p.AuditLogDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		return r.audit.DeleteBefore(ctx, cutoff)
	})

	counts.QuotaReservations, errs = purgeWith(ctx, p.QuotaReservationDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		// 只删终态（已结算/已释放）：在途记录必须留给过期回收流程先退还额度，
		// 提前删掉会让"额度被占用却查不到预留"变成一笔糊涂账。
		// expires_at 与 created_at 同时判断：expires_at = 0 表示"永不过期"的预留，
		// 只看它会被误判为"很久以前"，必须由 created_at 兜底。
		return execDelete(ctx, r.db,
			`DELETE FROM quota_reservations
			  WHERE status IN (?, ?) AND expires_at < ? AND created_at < ?`,
			model.ReservationSettled, model.ReservationReleased, cutoff.Unix(), cutoff.Unix())
	})

	counts.CorpusSamples, errs = purgeWith(ctx, p.CorpusSampleDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		return r.corpus.DeleteCorpusSamplesBefore(ctx, cutoff)
	})

	counts.Broadcasts, errs = purgeWith(ctx, p.BroadcastDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		// 按所属批次的创建时间整批删除：回执与批次同生命周期，
		// 批次本身是管理员手工发起的，最长几分钟，创建超过保留期的必然已结束
		// （含卡死在 running 的），其回执不再有排查价值。
		return execDelete(ctx, r.db,
			`DELETE FROM email_broadcast_recipients
			  WHERE broadcast_id IN (SELECT id FROM email_broadcasts WHERE created_at < ?)`,
			cutoff.Unix())
	})

	counts.ChannelProbeLogs, errs = purgeWith(ctx, p.ProbeLogDays, now, errs, func(ctx context.Context, cutoff time.Time) (int64, error) {
		// 探针历史是本组里写入最频繁的一张（每渠道每轮一行），
		// 不清理的话它是唯一会随站点运行时间线性压垮统计查询的表。
		// 走 at 索引（0053 建了 idx_channel_probe_logs_at），
		// 因此这里不按渠道逐个删——那会把一次全站清理变成 N 次索引扫描。
		return execDelete(ctx, r.db,
			"DELETE FROM channel_probe_logs WHERE at < ?", cutoff.Unix())
	})

	if len(errs) > 0 {
		return counts, fmt.Errorf("store: 数据保留清理部分失败: %w", errors.Join(errs...))
	}
	return counts, nil
}

// purgeWith 计算 cutoff 并执行一次清理：保留天数为 0 时直接跳过。
//
// 错误不在此处返回中断，而是追加进 errs 交由 Purge 统一返回——
// 一次清理涉及五张表，任何一张失败都不该让其余四张也停摆。
func purgeWith(ctx context.Context, days int, now time.Time, errs []error,
	run func(context.Context, time.Time) (int64, error)) (int64, []error) {
	if days <= 0 {
		return 0, errs
	}
	cutoff := now.AddDate(0, 0, -days)
	n, err := run(ctx, cutoff)
	if err != nil {
		return 0, append(errs, err)
	}
	return n, errs
}

// execDelete 执行一条 DELETE 并返回受影响行数。
//
// 统一在此包装错误：删除失败若被吞掉，运维会以为"数据还在/还在删"，
// 而实际磁盘一直在涨——保留期功能的信任完全依赖删除条数的可观测。
func execDelete(ctx context.Context, db *sql.DB, query string, args ...any) (int64, error) {
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("store: 执行保留期清理失败 (%s): %w", query, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取清理影响行数失败: %w", err)
	}
	return affected, nil
}
