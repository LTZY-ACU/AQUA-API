// 运维统计与备份校验查询的单元测试。
//
// 意图（Why）：
//
//	运维概览的真实性依赖这些聚合查询：一旦「各表行数」「失败率」「备份校验」
//	出错，站长会据此做出错误判断（例如以为备份可用而实际不可恢复）。
//	因此用真实 SQLite 库验证「行数正确」「空库不 panic」「非法文件被识别」。
//
// 流转（Flow）：
//
//	go test ./internal/store/ → 临时库写入样本 → 断言聚合结果
//
// 扩展（Extend）：
//
//	新增统计维度时，在此补充对应断言。
package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// TestTableRowCounts_ReturnsExistingTables 验证行数统计覆盖核心表且只返回存在的表。
func TestTableRowCounts_ReturnsExistingTables(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.DB().ExecContext(ctx,
		"INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"stats-user", "hash", time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("写入样本用户失败: %v", err)
	}

	stats, err := st.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("统计表行数失败: %v", err)
	}

	byName := make(map[string]int64, len(stats))
	for _, item := range stats {
		byName[item.Name] = item.Rows
	}
	if rows, ok := byName["users"]; !ok || rows != 1 {
		t.Fatalf("users 行数应为 1，实际 ok=%v rows=%d", ok, rows)
	}
	if _, ok := byName["usage_logs"]; !ok {
		t.Fatalf("统计结果应包含 usage_logs 表：%v", byName)
	}
}

// TestUsageHealth_CountsFailureAndLatency 验证窗口统计的次数、失败数与平均耗时。
func TestUsageHealth_CountsFailureAndLatency(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// 三条近 24 小时记录：两条成功（200/201，耗时 100/300），一条失败（500，耗时 200）；
	// 再加一条 3 天前的失败记录（只应计入 7 天窗口，不计入 24 小时）。
	insertLog := func(status, latency int, age time.Duration) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO usage_logs (status_code, latency_ms, created_at) VALUES (?, ?, ?)`,
			status, latency, now.Add(-age).Unix()); err != nil {
			t.Fatalf("写入样本日志失败: %v", err)
		}
	}
	insertLog(200, 100, time.Hour)
	insertLog(201, 300, 2*time.Hour)
	insertLog(500, 200, 3*time.Hour)
	insertLog(500, 400, 3*24*time.Hour)

	health, err := st.UsageHealth(ctx, now)
	if err != nil {
		t.Fatalf("统计调用健康度失败: %v", err)
	}

	if health.Last24h.Requests != 3 || health.Last24h.Failures != 1 {
		t.Errorf("近 24 小时 requests=%d failures=%d，期望 3/1",
			health.Last24h.Requests, health.Last24h.Failures)
	}
	if got := health.Last24h.AvgLatencyMS; got < 199 || got > 201 {
		t.Errorf("近 24 小时平均耗时 = %v，期望约 200", got)
	}
	if health.Last7d.Requests != 4 || health.Last7d.Failures != 2 {
		t.Errorf("近 7 天 requests=%d failures=%d，期望 4/2",
			health.Last7d.Requests, health.Last7d.Failures)
	}
}

// TestUsageHealth_EmptyDatabase 验证空库统计返回零值而非报错。
func TestUsageHealth_EmptyDatabase(t *testing.T) {
	st := newTestStore(t)

	health, err := st.UsageHealth(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("空库统计不应报错: %v", err)
	}
	if health.Last24h.Requests != 0 || health.Last7d.Requests != 0 {
		t.Errorf("空库应返回 0 次调用，实际 %+v", health)
	}
}

// TestDatabaseSizeBytes_SQLite 验证 SQLite 能给出正数的数据库体积。
func TestDatabaseSizeBytes_SQLite(t *testing.T) {
	st := newTestStore(t)

	size, ok, err := st.DatabaseSizeBytes(context.Background())
	if err != nil {
		t.Fatalf("读取数据库体积失败: %v", err)
	}
	if !ok || size <= 0 {
		t.Fatalf("SQLite 应返回正数体积，实际 ok=%v size=%d", ok, size)
	}
}

// TestDiskUsage_NoError 验证磁盘水位查询在任何平台都不报错。
//
// 说明：Windows 开发环境下返回 Available=false（见 stats_disk_windows.go），
// 因此不断言 Available 的具体取值，只要求「不报错」且「可用时数据自洽」。
func TestDiskUsage_NoError(t *testing.T) {
	st := newTestStore(t)

	disk, err := st.DiskUsage(context.Background())
	if err != nil {
		t.Fatalf("磁盘水位查询不应报错: %v", err)
	}
	if disk.Available && disk.FreeBytes > disk.TotalBytes {
		t.Errorf("可用空间不应大于总空间: %+v", disk)
	}
}

// TestInspectSQLiteBackup_RoundTrip 验证对一个真实快照的只读校验。
func TestInspectSQLiteBackup_RoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.DB().ExecContext(ctx,
		"INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"backup-user", "hash", time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("写入样本用户失败: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := st.DB().ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		t.Fatalf("生成快照失败: %v", err)
	}

	inspection, err := InspectSQLiteBackup(ctx, backupPath)
	if err != nil {
		t.Fatalf("校验快照失败: %v", err)
	}
	if !inspection.HasSchemaTable {
		t.Error("快照应包含 schema_migrations 表")
	}
	if inspection.SchemaVersion != SupportedSchemaVersion() {
		t.Errorf("快照 schema 版本 = %d，期望 %d", inspection.SchemaVersion, SupportedSchemaVersion())
	}

	var userRows int64 = -1
	for _, item := range inspection.Tables {
		if item.Name == "users" {
			userRows = item.Rows
		}
	}
	if userRows != 1 {
		t.Errorf("快照中 users 行数 = %d，期望 1", userRows)
	}
}

// TestInspectSQLiteBackup_RejectsNonSQLite 验证非 SQLite 文件被明确拒绝。
func TestInspectSQLiteBackup_RejectsNonSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-db.db")
	if err := os.WriteFile(path, []byte("this is definitely not a sqlite database"), 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	if _, err := InspectSQLiteBackup(context.Background(), path); err == nil {
		t.Fatal("非 SQLite 文件应返回错误，实际返回 nil")
	}
}

// TestReconcileUsage_收入成本毛利勾稽 验证对账聚合的收入、成本、毛利三者自洽。
//
// 测试重点（为什么测这些）：
//   - 成本必须覆盖【按次计费】渠道：只填 per_call_price 的进价规则若被算成 0，
//     对账表会显示"全是利润"，与真实账目背离（docs/17 缺口 1 的对账侧表现）；
//   - 未录进价的请求成本按 0 计，但必须计入 unpriced_requests，不能混进"免费"；
//   - 失败请求（status >= 400）不构成收入也不产生成本，必须被排除。
func TestReconcileUsage_收入成本毛利勾稽(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// 渠道 1（用于渠道维度的展示标签）
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO channels (id, name, type, created_at, updated_at) VALUES (1, '测试渠道', 1, ?, ?)`,
		now.Unix(), now.Unix()); err != nil {
		t.Fatalf("写入样本渠道失败: %v", err)
	}
	// 令牌 7 归属分组 vip（usage_logs 不落分组，分组维度靠 token 关联）
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO tokens (id, name, key_hash, key_enc, group_name, created_at, updated_at)
		VALUES (7, '样本令牌', 'hash-7', 'enc', 'vip', ?, ?)`,
		now.Unix(), now.Unix()); err != nil {
		t.Fatalf("写入样本令牌失败: %v", err)
	}
	// 进价：img 为纯按次（只填 per_call_price）；chat 为按量（每 1M 输入 100 额度）
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO channel_model_costs
			(channel_id, model, prompt_price, cache_price, completion_price, per_call_price, created_at, updated_at)
		VALUES (1, 'img', 0, 0, 0, 2000, ?, ?), (1, 'chat', 100, 0, 0, 0, ?, ?)`,
		now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatalf("写入样本进价失败: %v", err)
	}

	insertUsage := func(modelName, upstream string, quota, prompt, completion int64, status int) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO usage_logs
				(token_id, channel_id, model, upstream_model, prompt_tokens, completion_tokens, total_tokens, quota, status_code, created_at)
			VALUES (7, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
			modelName, upstream, prompt, completion, prompt+completion, quota, status, now.Unix()); err != nil {
			t.Fatalf("写入样本日志失败: %v", err)
		}
	}
	// 3 次按次请求：收入 30000，成本 2000 × 3 = 6000
	for i := 0; i < 3; i++ {
		insertUsage("img", "img", 10_000, 0, 0, 200)
	}
	// 2 次按量请求：收入 10000，成本 2 × 1M × 100 / 1M = 200
	for i := 0; i < 2; i++ {
		insertUsage("chat", "", 5_000, 1_000_000, 0, 200)
	}
	// 1 次未录进价：收入 100，成本 0，但必须计入 unpriced
	insertUsage("mystery", "", 100, 0, 0, 200)
	// 1 次失败请求：不得计入收入与成本
	insertUsage("img", "img", 999, 0, 0, 500)

	from, to := now.Add(-24*time.Hour), now.Add(time.Hour)

	// ── 渠道维度 ────────────────────────────────────────────────
	rows, err := st.ReconcileUsage(ctx, from, to, "channel")
	if err != nil {
		t.Fatalf("渠道维度对账失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("渠道维度应只有 1 行，实际 %d（%+v）", len(rows), rows)
	}
	got := rows[0]
	if got.Key != "1" || got.Label != "测试渠道" {
		t.Fatalf("渠道行键/标签异常：key=%q label=%q", got.Key, got.Label)
	}
	if got.Requests != 6 {
		t.Fatalf("成功请求数应为 6（失败请求应排除），实际 %d", got.Requests)
	}
	if got.RevenueQuota != 40_100 {
		t.Fatalf("收入应为 30000+10000+100=40100，实际 %d", got.RevenueQuota)
	}
	if got.CostQuota != 6_200 {
		t.Fatalf("成本应为按次 6000 + 按量 200 = 6200（按次不能是 0），实际 %d", got.CostQuota)
	}
	if got.PricedRequests != 5 || got.UnpricedRequests != 1 {
		t.Fatalf("可/不可估成本请求数应为 5/1，实际 %d/%d", got.PricedRequests, got.UnpricedRequests)
	}
	if got.GrossProfitQuota() != got.RevenueQuota-got.CostQuota {
		t.Fatalf("毛利必须等于收入−成本：%d", got.GrossProfitQuota())
	}

	// ── 分组维度（按 token 关联）────────────────────────────────
	groupRows, err := st.ReconcileUsage(ctx, from, to, "group")
	if err != nil {
		t.Fatalf("分组维度对账失败: %v", err)
	}
	if len(groupRows) != 1 || groupRows[0].Key != "vip" {
		t.Fatalf("分组维度应聚合到 vip，实际 %+v", groupRows)
	}
	if groupRows[0].RevenueQuota != 40_100 || groupRows[0].CostQuota != 6_200 {
		t.Fatalf("分组维度金额应与渠道维度一致，实际 收入 %d / 成本 %d",
			groupRows[0].RevenueQuota, groupRows[0].CostQuota)
	}

	// ── 模型维度（逐模型核对）──────────────────────────────────
	modelRows, err := st.ReconcileUsage(ctx, from, to, "model")
	if err != nil {
		t.Fatalf("模型维度对账失败: %v", err)
	}
	byModel := make(map[string]model.UsageReconciliationRow, len(modelRows))
	for _, row := range modelRows {
		byModel[row.Key] = row
	}
	if img := byModel["img"]; img.Requests != 3 || img.RevenueQuota != 30_000 || img.CostQuota != 6_000 {
		t.Fatalf("img 应按次算得成本 6000，实际 %+v", img)
	}
	if chat := byModel["chat"]; chat.CostQuota != 200 {
		t.Fatalf("chat 成本应为 200，实际 %+v", chat)
	}
	if mystery := byModel["mystery"]; mystery.CostQuota != 0 || mystery.UnpricedRequests != 1 {
		t.Fatalf("未录进价的模型应成本 0 且计入 unpriced，实际 %+v", mystery)
	}

	// 非法参数与空区间
	if _, err := st.ReconcileUsage(ctx, from, to, "unknown"); err == nil {
		t.Fatal("非法维度应报错")
	}
	if _, err := st.ReconcileUsage(ctx, to, from, "channel"); err == nil {
		t.Fatal("起点不早于终点应报错")
	}
	empty, err := st.ReconcileUsage(ctx, now.Add(24*time.Hour), now.Add(48*time.Hour), "channel")
	if err != nil {
		t.Fatalf("空区间对账不应报错: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空区间应返回空数组，实际 %#v", empty)
	}
}

// TestSQLiteMainFilePath_ReturnsFile 验证能定位主库文件路径。
func TestSQLiteMainFilePath_ReturnsFile(t *testing.T) {
	st := newTestStore(t)

	path, err := st.SQLiteMainFilePath(context.Background())
	if err != nil {
		t.Fatalf("读取主库文件路径失败: %v", err)
	}
	if path == "" {
		t.Fatal("主库文件路径不应为空")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("主库文件应存在: %v", err)
	}
}
