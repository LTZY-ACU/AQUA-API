// 本文件是 store.Retention（数据保留期清理）的单元测试。
//
// 意图（Why）：
//
//	清理是"删数据"的动作，一旦条件写错（如 cutoff 算反、状态条件遗漏），
//	后果是不可逆的数据丢失。因此测试的重心不是"能不能删"，
//	而是**边界**：还没到期的必须留、在途预留不能删、天数为 0 必须跳过、
//	失败必须上报（被吞掉的清理会让人以为磁盘还在涨）。
//
// 流转（Flow）：
//
//	go test ./internal/store/ → newTestStore（临时 SQLite）→ 直接 SQL 造数
//	  → Retention.Purge → 断言各表删除条数与剩余行
//
// 扩展（Extend）：
//
//	新增要清理的表时：造数 + 预期条数各加一段（老/新各一条是最省心的模板）。
package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// execSQL 在测试库里执行一条写语句，失败即终止用例（造数失败没有继续断言的意义）。
func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("执行测试 SQL 失败 (%s): %v", query, err)
	}
}

// countRows 统计满足条件的行数。
func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("统计行数失败 (%s): %v", query, err)
	}
	return n
}

// seedRetentionRows 往五张表各写入「一条过期 + 一条未到期」的数据，返回各自的预期删除条数。
//
// 额度预留表额外造两条边界数据：
//   - 在途（status=1）的过期记录：必须留给过期回收流程退还额度，不能被保留期删掉；
//   - expires_at=0（永不过期）的终态记录：只看 expires_at 会被当成"很久以前"，
//     由 created_at 兜底判断，这里验证它按创建时间正常过期。
func seedRetentionRows(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	old := now.AddDate(0, 0, -400).Unix() // 比所有默认保留期都久
	fresh := now.Add(-time.Hour).Unix()

	// 用量明细
	execSQL(t, db, "INSERT INTO usage_logs (created_at) VALUES (?)", old)
	execSQL(t, db, "INSERT INTO usage_logs (created_at) VALUES (?)", fresh)

	// 审计日志（method / path 无默认值）
	execSQL(t, db, "INSERT INTO audit_logs (method, path, created_at) VALUES ('POST', '/admin/x', ?)", old)
	execSQL(t, db, "INSERT INTO audit_logs (method, path, created_at) VALUES ('POST', '/admin/y', ?)", fresh)

	// 额度预留：两条可删（已结算/已释放）+ 一条永不过期的终态 + 一条在途 + 一条未到期
	execSQL(t, db, "INSERT INTO quota_reservations (request_id, status, created_at, expires_at) VALUES ('r-settled', 2, ?, ?)", old, old)
	execSQL(t, db, "INSERT INTO quota_reservations (request_id, status, created_at, expires_at) VALUES ('r-released', 3, ?, ?)", old, old)
	execSQL(t, db, "INSERT INTO quota_reservations (request_id, status, created_at, expires_at) VALUES ('r-noexp', 3, ?, 0)", old)
	execSQL(t, db, "INSERT INTO quota_reservations (request_id, status, created_at, expires_at) VALUES ('r-inflight', 1, ?, ?)", old, old)
	execSQL(t, db, "INSERT INTO quota_reservations (request_id, status, created_at, expires_at) VALUES ('r-fresh', 2, ?, ?)", fresh, fresh)

	// 语料样本（其余列均有默认值）
	execSQL(t, db, "INSERT INTO corpus_samples (created_at) VALUES (?)", old)
	execSQL(t, db, "INSERT INTO corpus_samples (created_at) VALUES (?)", fresh)

	// 群发批次与回执
	execSQL(t, db, "INSERT INTO email_broadcasts (created_at, updated_at) VALUES (?, ?)", old, old)
	execSQL(t, db, "INSERT INTO email_broadcast_recipients (broadcast_id, user_id, email, created_at) VALUES (1, 1, 'old@example.com', ?)", old)
	execSQL(t, db, "INSERT INTO email_broadcasts (created_at, updated_at) VALUES (?, ?)", fresh, fresh)
	execSQL(t, db, "INSERT INTO email_broadcast_recipients (broadcast_id, user_id, email, created_at) VALUES (2, 2, 'new@example.com', ?)", fresh)

	// 渠道探针历史：一条超期 + 一条未到期。
	// 探针历史是本组里写入最频繁的一张（每渠道每轮一行），
	// 若不接进保留期，它是唯一会随运行时间压垮统计查询的表。
	execSQL(t, db, "INSERT INTO channel_probe_logs (channel_id, at, ok, latency_ms) VALUES (1, ?, 1, 120)", old)
	execSQL(t, db, "INSERT INTO channel_probe_logs (channel_id, at, ok, latency_ms) VALUES (1, ?, 1, 130)", fresh)
}

// TestRetention_Purge 按保留期删除各表中过期数据，未到期的与在途记录原样保留。
func TestRetention_Purge(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	ctx := context.Background()
	now := time.Now()
	seedRetentionRows(t, db, now)

	retention := NewRetention(db, NewAuditLogRepository(db), NewCorpusRepository(db))
	counts, err := retention.Purge(ctx, RetentionPolicy{
		UsageLogDays:         180,
		AuditLogDays:         365,
		QuotaReservationDays: 7,
		CorpusSampleDays:     30,
		BroadcastDays:        90,
		ProbeLogDays:         30,
	}, now)
	if err != nil {
		t.Fatalf("Purge 返回错误: %v", err)
	}

	if counts.UsageLogs != 1 || counts.AuditLogs != 1 ||
		counts.QuotaReservations != 3 || counts.CorpusSamples != 1 || counts.Broadcasts != 1 ||
		counts.ChannelProbeLogs != 1 {
		t.Errorf("删除条数不符：%+v，期望 usage=1 audit=1 reservation=3 corpus=1 broadcast=1 probe=1", counts)
	}
	if counts.Total() != 8 {
		t.Errorf("Total() = %d，期望 8", counts.Total())
	}

	// 剩余行：每表各留一条未到期的
	if n := countRows(t, db, "SELECT COUNT(*) FROM usage_logs"); n != 1 {
		t.Errorf("usage_logs 剩余 %d 行，期望 1", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM audit_logs"); n != 1 {
		t.Errorf("audit_logs 剩余 %d 行，期望 1", n)
	}
	// 预留台账剩下：在途过期 + 未到期终态（各 1 条）
	if n := countRows(t, db, "SELECT COUNT(*) FROM quota_reservations"); n != 2 {
		t.Errorf("quota_reservations 剩余 %d 行，期望 2", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM quota_reservations WHERE request_id = 'r-inflight'"); n != 1 {
		t.Error("在途（未结算）预留被保留期清理删掉了，额度退还会失去依据")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM quota_reservations WHERE request_id = 'r-fresh'"); n != 1 {
		t.Error("未到期的终态预留被误删")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM corpus_samples"); n != 1 {
		t.Errorf("corpus_samples 剩余 %d 行，期望 1", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM email_broadcast_recipients"); n != 1 {
		t.Errorf("email_broadcast_recipients 剩余 %d 行，期望 1", n)
	}
	if n := countRows(t, db,
		"SELECT COUNT(*) FROM email_broadcast_recipients WHERE broadcast_id = 2"); n != 1 {
		t.Error("未到期批次的回执被误删")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM channel_probe_logs"); n != 1 {
		t.Errorf("channel_probe_logs 剩余 %d 行，期望 1", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM channel_probe_logs WHERE latency_ms = 130"); n != 1 {
		t.Error("未到期的探针历史被误删，趋势曲线会被截短")
	}
}

// TestRetention_ZeroPolicySkips 验证保留天数为 0 的表整表跳过（永久留痕的开关）。
func TestRetention_ZeroPolicySkips(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	ctx := context.Background()
	now := time.Now()
	seedRetentionRows(t, db, now)

	retention := NewRetention(db, NewAuditLogRepository(db), NewCorpusRepository(db))
	counts, err := retention.Purge(ctx, RetentionPolicy{}, now)
	if err != nil {
		t.Fatalf("Purge 返回错误: %v", err)
	}
	if counts.Total() != 0 {
		t.Errorf("全 0 策略删除了 %d 行，期望 0", counts.Total())
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM usage_logs"); n != 2 {
		t.Errorf("usage_logs 剩余 %d 行，期望 2（0 天 = 不清理）", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM quota_reservations"); n != 5 {
		t.Errorf("quota_reservations 剩余 %d 行，期望 5（0 天 = 不清理）", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM channel_probe_logs"); n != 2 {
		t.Errorf("channel_probe_logs 剩余 %d 行，期望 2（0 天 = 不清理）", n)
	}
}

// TestRetention_PartialPolicy 只配置部分表时，只删那部分、其余原样保留。
func TestRetention_PartialPolicy(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	ctx := context.Background()
	now := time.Now()
	seedRetentionRows(t, db, now)

	retention := NewRetention(db, NewAuditLogRepository(db), NewCorpusRepository(db))
	counts, err := retention.Purge(ctx, RetentionPolicy{UsageLogDays: 180}, now)
	if err != nil {
		t.Fatalf("Purge 返回错误: %v", err)
	}
	if counts.UsageLogs != 1 || counts.Total() != 1 {
		t.Errorf("只配置 usage_log 时期望恰好删 1 行，实际 %+v", counts)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM audit_logs"); n != 2 {
		t.Errorf("audit_logs 剩余 %d 行，期望 2（未配置 = 不清理）", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM corpus_samples"); n != 2 {
		t.Errorf("corpus_samples 剩余 %d 行，期望 2（未配置 = 不清理）", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM channel_probe_logs"); n != 2 {
		t.Errorf("channel_probe_logs 剩余 %d 行，期望 2（未配置 = 不清理）", n)
	}
}

// TestRetention_FailureReported 验证数据库故障必须以 error 上报，不能被吞掉。
//
// 清理功能的可信度完全建立在"删了没删、删了多少"的日志上，
// 静默失败会让磁盘悄悄涨回原样却无人察觉。
func TestRetention_FailureReported(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	retention := NewRetention(db, NewAuditLogRepository(db), NewCorpusRepository(db))

	// 关闭连接后执行：任何一条 DELETE 都会失败
	if err := st.Close(); err != nil {
		t.Fatalf("关闭测试库失败: %v", err)
	}

	_, err := retention.Purge(context.Background(), RetentionPolicy{
		UsageLogDays:         180,
		AuditLogDays:         365,
		QuotaReservationDays: 7,
		CorpusSampleDays:     30,
		BroadcastDays:        90,
		ProbeLogDays:         30,
	}, time.Now())
	if err == nil {
		t.Fatal("数据库已关闭时期望 Purge 返回错误，实际被吞掉")
	}
}
