// 渠道密钥池仓储的单元测试。
//
// 意图（Why）：
//
//	密钥池要同时满足两个容易冲突的目标：**幂等的批量导入** 与 **精确的失效摘除**。
//	前者出错会导致每次编辑渠道都把 500 把密钥重复插一遍（池内自我竞争）；
//	后者出错会让失效密钥被反复选中（持续浪费尝试次数并拖慢响应）。
//	本文件把这两条不变量固化成测试。
//
// 流转（Flow）：
//
//	go test ./internal/store/ → 临时 SQLite 上执行真实 SQL + 真实加解密
//
// 扩展（Extend）：
//
//	新增密钥状态或统计字段时，在 TestChannelKey_MarkFailure 中补充断言。
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newTestKeyRepo 构造基于临时数据库的密钥池仓储，并返回底层连接用于安全断言。
func newTestKeyRepo(t *testing.T) (model.ChannelKeyRepository, *sql.DB) {
	t.Helper()

	st := newTestStore(t)

	cipher, err := crypto.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return NewChannelKeyRepository(st.DB(), cipher), st.DB()
}

// sampleKeys 返回一组形如真实 NVIDIA 密钥的测试密钥。
func sampleKeys(n int) []string {
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, "nvapi-test-key-"+strings.Repeat("x", i%5)+"-"+string(rune('a'+i%26))+"-"+strings.Repeat("0", 3)+string(rune('0'+i%10)))
	}
	return keys
}

// TestChannelKey_ReplaceAll_池顺序与输入一致 钉住「插入顺序决定池顺序」的约定。
//
// 背景：插入阶段曾直接遍历 map，同一批密钥每次落库的 ID 顺序都不同，
// 后台展示与轮询调度随之随机，凭据路由测试也间歇性拿到错误的钥匙。
// 本用例保证 ListByChannel（按 ID 升序）返回的顺序与导入顺序一致。
func TestChannelKey_ReplaceAll_池顺序与输入一致(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()
	const channelID uint64 = 1

	keys := []string{"nvapi-first", "nvapi-second", "nvapi-third"}
	if _, _, err := repo.ReplaceAll(ctx, channelID, keys, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	pool, err := repo.ListByChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("读取密钥池失败: %v", err)
	}
	if len(pool) != len(keys) {
		t.Fatalf("池内应有 %d 把，实际 %d", len(keys), len(pool))
	}
	for i, k := range keys {
		if pool[i].Key != k {
			t.Errorf("第 %d 把应为 %q（与导入顺序一致），实际 %q", i+1, k, pool[i].Key)
		}
	}
}

func TestChannelKey_ReplaceAll_幂等与差集增删(t *testing.T) {
	repo, db := newTestKeyRepo(t)
	ctx := context.Background()
	const channelID uint64 = 1

	keys := []string{"nvapi-a1", "nvapi-b2", "nvapi-c3"}

	// 首次导入：3 把全新密钥
	added, removed, err := repo.ReplaceAll(ctx, channelID, keys, nil)
	if err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	if added != 3 || removed != 0 {
		t.Fatalf("首次导入应为 added=3 removed=0，实际 added=%d removed=%d", added, removed)
	}

	// 幂等：重复导入同一批
	added, removed, err = repo.ReplaceAll(ctx, channelID, keys, nil)
	if err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	if added != 0 || removed != 0 {
		t.Fatalf("重复导入应无变化，实际 added=%d removed=%d（会产生重复密钥）", added, removed)
	}

	// 差集：改为只保留两把
	added, removed, err = repo.ReplaceAll(ctx, channelID, []string{"nvapi-a1", "nvapi-c3"}, nil)
	if err != nil {
		t.Fatalf("差集更新失败: %v", err)
	}
	if added != 0 || removed != 1 {
		t.Fatalf("差集更新应为 added=0 removed=1，实际 added=%d removed=%d", added, removed)
	}

	// 库内总数应为 2
	var total int
	if err := db.QueryRow("SELECT COUNT(1) FROM channel_keys WHERE channel_id = ?", channelID).Scan(&total); err != nil {
		t.Fatalf("统计密钥数失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("库内密钥数应为 2，实际 %d", total)
	}
}

func TestChannelKey_批量导入_支持大批量与去重(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	// 构造 500 把互不相同的密钥（模拟真实的 NVIDIA 密钥池规模）
	keys := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		keys = append(keys, "nvapi-"+strings.Repeat("k", i%7)+"-"+strings.Repeat("z", 8)+time.Duration(i).String()+"-"+string(rune('A'+i%26)))
	}
	// 故意塞入重复项：解析层应去重，最终落地数量应小于输入数量
	keys = append(keys, keys[0], keys[1])

	added, _, err := repo.ReplaceAll(ctx, 1, keys, nil)
	if err != nil {
		t.Fatalf("批量导入失败: %v", err)
	}
	if added != 500 {
		t.Fatalf("应导入 500 把（重复项被去重），实际 %d", added)
	}

	usable, err := repo.ListUsable(ctx, 1)
	if err != nil {
		t.Fatalf("查询可用密钥失败: %v", err)
	}
	if len(usable) != 500 {
		t.Fatalf("可用密钥应为 500，实际 %d", len(usable))
	}
}

func TestChannelKey_落库为密文(t *testing.T) {
	repo, db := newTestKeyRepo(t)
	ctx := context.Background()

	plain := "nvapi-super-secret-value-1234567890"
	if _, _, err := repo.ReplaceAll(ctx, 1, []string{plain}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	var stored string
	if err := db.QueryRow("SELECT key_enc FROM channel_keys WHERE channel_id = 1").Scan(&stored); err != nil {
		t.Fatalf("读取存储值失败: %v", err)
	}
	if stored == plain {
		t.Fatal("数据库里出现了明文密钥（严重安全问题）")
	}
	if strings.Contains(stored, "nvapi-super-secret") {
		t.Fatal("数据库中的密文仍包含明文片段")
	}

	// 反向确认能正确解密回来
	keys, err := repo.ListByChannel(ctx, 1)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(keys) != 1 || keys[0].Key != plain {
		t.Fatal("解密后的密钥与原文不一致")
	}
	// 对外展示必须是脱敏的
	if strings.Contains(keys[0].Masked(), "secret-value") {
		t.Fatalf("脱敏结果泄露了密钥中段: %s", keys[0].Masked())
	}
}

func TestChannelKey_MarkFailure_连续失败自动摘除(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-flaky"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, err := repo.ListByChannel(ctx, 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("读取密钥失败: %v", err)
	}
	id := keys[0].ID

	// 前两次失败：仍应保持启用（偶发抖动不应摘除好密钥）
	for i := 1; i < model.KeyAutoRemoveThreshold; i++ {
		if err := repo.MarkFailure(ctx, id, "429 too many requests"); err != nil {
			t.Fatalf("记录失败失败: %v", err)
		}
		got, err := repo.ListByChannel(ctx, 1)
		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if got[0].Status != model.ChannelKeyStatusEnabled {
			t.Fatalf("第 %d 次失败后不应摘除（阈值 %d）", i, model.KeyAutoRemoveThreshold)
		}
		if got[0].FailCount != i {
			t.Fatalf("失败计数应为 %d，实际 %d", i, got[0].FailCount)
		}
	}

	// 达到阈值：自动摘除
	for i := 0; i < model.KeyAutoRemoveThreshold-1; i++ {
		if err := repo.MarkFailure(ctx, id, "401 invalid api key"); err != nil {
			t.Fatalf("记录失败失败: %v", err)
		}
	}
	got, err := repo.ListByChannel(ctx, 1)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got[0].Status != model.ChannelKeyStatusAutoRemoved {
		t.Fatalf("连续失败达 %d 次后应自动摘除，实际状态 %s",
			model.KeyAutoRemoveThreshold, got[0].Status)
	}
	if got[0].LastError == "" {
		t.Fatal("应记录最近一次失败原因")
	}

	// 摘除后不再出现在可用列表中
	usable, err := repo.ListUsable(ctx, 1)
	if err != nil {
		t.Fatalf("查询可用密钥失败: %v", err)
	}
	if len(usable) != 0 {
		t.Fatalf("被摘除的密钥不应出现在可用列表，实际仍返回 %d 条", len(usable))
	}

	// 手动恢复后重新可用
	if err := repo.UpdateStatus(ctx, id, model.ChannelKeyStatusEnabled); err != nil {
		t.Fatalf("恢复密钥失败: %v", err)
	}
	usable, err = repo.ListUsable(ctx, 1)
	if err != nil {
		t.Fatalf("查询可用密钥失败: %v", err)
	}
	if len(usable) != 1 {
		t.Fatal("手动恢复后密钥应重新可用")
	}
}

func TestChannelKey_MarkSuccess_清零失败计数(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-ok"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	id := keys[0].ID

	if err := repo.MarkFailure(ctx, id, "500 server error"); err != nil {
		t.Fatalf("记录失败失败: %v", err)
	}
	if err := repo.MarkSuccess(ctx, id); err != nil {
		t.Fatalf("记录成功失败: %v", err)
	}

	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].FailCount != 0 {
		t.Fatalf("成功后连续失败计数应清零，实际 %d", got[0].FailCount)
	}
	if got[0].LastError != "" {
		t.Fatalf("成功后应清空最近错误，实际 %q", got[0].LastError)
	}
}

func TestChannelKey_Summary_按渠道统计(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"k1", "k2", "k3"}, nil); err != nil {
		t.Fatalf("导入渠道 1 失败: %v", err)
	}
	if _, _, err := repo.ReplaceAll(ctx, 2, []string{"k4"}, nil); err != nil {
		t.Fatalf("导入渠道 2 失败: %v", err)
	}

	// 把渠道 1 的一把密钥置为禁用、一把置为自动摘除
	keys, _ := repo.ListByChannel(ctx, 1)
	if err := repo.UpdateStatus(ctx, keys[0].ID, model.ChannelKeyStatusDisabled); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	if err := repo.UpdateStatus(ctx, keys[1].ID, model.ChannelKeyStatusAutoRemoved); err != nil {
		t.Fatalf("摘除失败: %v", err)
	}

	summaries, err := repo.Summary(ctx, []uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}

	s1 := summaries[1]
	if s1.Total != 3 || s1.Enabled != 1 || s1.Disabled != 1 || s1.AutoRemoved != 1 {
		t.Fatalf("渠道 1 统计错误: %+v", s1)
	}
	s2 := summaries[2]
	if s2.Total != 1 || s2.Enabled != 1 {
		t.Fatalf("渠道 2 统计错误: %+v", s2)
	}
	// 无密钥的渠道不应出现在 map 中（调用方按零值处理即可）
	if _, ok := summaries[3]; ok {
		t.Fatal("无密钥的渠道不应出现在统计结果中")
	}
}

func TestChannelKey_MarkUsed_记录使用时间(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-used"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	if !keys[0].LastUsedAt.IsZero() {
		t.Fatal("新导入的密钥不应有使用时间")
	}

	at := time.Now().Truncate(time.Second)
	if err := repo.MarkUsed(ctx, keys[0].ID, at); err != nil {
		t.Fatalf("记录使用失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].LastUsedAt.Unix() != at.Unix() {
		t.Fatalf("使用时间应为 %v，实际 %v", at, got[0].LastUsedAt)
	}
}

func TestChannelKey_DeleteByChannel(t *testing.T) {
	repo, db := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 7, []string{"a", "b"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if err := repo.DeleteByChannel(ctx, 7); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	var total int
	if err := db.QueryRow("SELECT COUNT(1) FROM channel_keys WHERE channel_id = 7").Scan(&total); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("删除后应为 0 条，实际 %d", total)
	}
}

// insertTestChannel 插入一条测试渠道（用于游标/策略相关断言）。
//
// 显式指定 id：游标与策略按 channel_id 读写，固定 id 便于断言。
func insertTestChannel(t *testing.T, db *sql.DB, id uint64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO channels
			(id, name, type, base_url, api_key_enc, models, group_name, priority, weight, status, created_at, updated_at)
		VALUES (?, '测试渠道', 1, 'https://example.com', '', 'm', 'default', 0, 1, 1, 0, 0)`, id); err != nil {
		t.Fatalf("写入测试渠道失败: %v", err)
	}
}

// TestChannelKey_调度列_默认值与读写 验证迁移 0013 新增列的默认值与更新。
func TestChannelKey_调度列_默认值与读写(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-sched"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, err := repo.ListByChannel(ctx, 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("读取密钥失败: %v", err)
	}
	k := keys[0]

	// 默认值：weight=1，其余为 0 / 零值
	if k.Weight != 1 || k.Priority != 0 || k.InFlight != 0 || k.RPMLimit != 0 || k.WindowCount != 0 {
		t.Fatalf("调度列默认值不符: %+v", k)
	}
	if !k.CooldownUntil.IsZero() || !k.WindowStart.IsZero() {
		t.Fatalf("时间类调度列默认应为零值: cooldown=%v window=%v", k.CooldownUntil, k.WindowStart)
	}

	if err := repo.UpdateScheduling(ctx, k.ID, 7, 3, 120); err != nil {
		t.Fatalf("更新调度参数失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].Weight != 7 || got[0].Priority != 3 || got[0].RPMLimit != 120 {
		t.Fatalf("调度参数未正确写回: %+v", got[0])
	}
}

// TestChannelKey_在途计数_增减与重复释放 验证 Acquire/Release 的计数语义。
//
// 重点：Release 必须可安全重复调用，且计数不会降到 0 以下
// （否则 least_in_flight 会长期偏好负计数的凭据，造成流量倾斜）。
func TestChannelKey_在途计数_增减与重复释放(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-inflight"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	id := keys[0].ID

	for i := 0; i < 2; i++ {
		if err := repo.Acquire(ctx, id); err != nil {
			t.Fatalf("Acquire 失败: %v", err)
		}
	}
	if got, _ := repo.ListByChannel(ctx, 1); got[0].InFlight != 2 {
		t.Fatalf("两次 Acquire 后在途应为 2，实际 %d", got[0].InFlight)
	}

	if err := repo.Release(ctx, id); err != nil {
		t.Fatalf("Release 失败: %v", err)
	}
	if got, _ := repo.ListByChannel(ctx, 1); got[0].InFlight != 1 {
		t.Fatalf("Release 一次后在途应为 1，实际 %d", got[0].InFlight)
	}

	// 重复释放：不应降到负数
	for i := 0; i < 3; i++ {
		if err := repo.Release(ctx, id); err != nil {
			t.Fatalf("重复 Release 失败: %v", err)
		}
	}
	if got, _ := repo.ListByChannel(ctx, 1); got[0].InFlight != 0 {
		t.Fatalf("重复释放后在途应恒为 0，实际 %d", got[0].InFlight)
	}
}

// TestChannelKey_冷却_读写与成功清除 验证冷却截止时间的读写与移除。
func TestChannelKey_冷却_读写与成功清除(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-cooldown"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	id := keys[0].ID

	until := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	if err := repo.SetCooldown(ctx, id, until, "429 too many requests"); err != nil {
		t.Fatalf("设置冷却失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].CooldownUntil.Unix() != until.Unix() {
		t.Fatalf("冷却截止时间应为 %v，实际 %v", until, got[0].CooldownUntil)
	}
	if got[0].FailCount != 1 {
		t.Fatalf("冷却应累加一次失败计数，实际 %d", got[0].FailCount)
	}
	if got[0].LastError == "" {
		t.Fatal("冷却应记录失败原因")
	}

	// 成功即解除冷却
	if err := repo.MarkSuccess(ctx, id); err != nil {
		t.Fatalf("MarkSuccess 失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if !got[0].CooldownUntil.IsZero() || got[0].FailCount != 0 {
		t.Fatalf("成功后应清除冷却与失败计数: %+v", got[0])
	}

	// 显式清除（until 为零值）
	if err := repo.SetCooldown(ctx, id, time.Time{}, ""); err != nil {
		t.Fatalf("清除冷却失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if !got[0].CooldownUntil.IsZero() {
		t.Fatal("until 为零值时应清除冷却")
	}
}

// TestChannelKey_轮询游标与策略_持久化 验证渠道级策略与游标的读写。
func TestChannelKey_轮询游标与策略_持久化(t *testing.T) {
	repo, db := newTestKeyRepo(t)
	ctx := context.Background()
	insertTestChannel(t, db, 1)

	// 默认：最少在途 + 游标 0
	strategy, cursor, err := repo.ChannelKeyStrategy(ctx, 1)
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if strategy != model.DefaultKeyStrategy() || cursor != 0 {
		t.Fatalf("默认策略/游标不符: strategy=%s cursor=%d", strategy, cursor)
	}

	if err := repo.SetChannelStrategy(ctx, 1, model.KeyStrategyRoundRobin); err != nil {
		t.Fatalf("设置策略失败: %v", err)
	}
	if err := repo.AdvanceKeyCursor(ctx, 1, 5); err != nil {
		t.Fatalf("推进游标失败: %v", err)
	}
	strategy, cursor, _ = repo.ChannelKeyStrategy(ctx, 1)
	if strategy != model.KeyStrategyRoundRobin || cursor != 5 {
		t.Fatalf("策略/游标未持久化: strategy=%s cursor=%d", strategy, cursor)
	}

	// 非法策略应被拒绝
	if err := repo.SetChannelStrategy(ctx, 1, model.KeyStrategy("nope")); err == nil {
		t.Fatal("非法策略应被拒绝")
	}

	// 渠道不存在：返回默认策略与游标 0，不报错
	strategy, cursor, err = repo.ChannelKeyStrategy(ctx, 999)
	if err != nil || strategy != model.DefaultKeyStrategy() || cursor != 0 {
		t.Fatalf("渠道不存在时应返回默认策略: strategy=%s cursor=%d err=%v", strategy, cursor, err)
	}
}

// TestChannelKey_ReplaceAllWithBalance_录入与不足补未知 验证余额随导入落库，
// 以及 balances 比 keys 短时缺失项取"未知"。
func TestChannelKey_ReplaceAllWithBalance_录入与不足补未知(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	keys := []string{"k-1", "k-2", "k-3"}
	// balances 故意比 keys 短：第三把应落为"未知"
	if _, _, err := repo.ReplaceAllWithBalance(ctx, 1, keys, nil, []int64{10, 0}); err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	byKey := map[string]*model.ChannelKey{}
	got, err := repo.ListByChannel(ctx, 1)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	for _, k := range got {
		byKey[k.Key] = k
	}

	if byKey["k-1"].Balance != 10 {
		t.Fatalf("k-1 余额应为 10，实际 %d", byKey["k-1"].Balance)
	}
	if !byKey["k-2"].BalanceExhausted() {
		t.Fatalf("k-2 余额为 0，应判定为已耗尽，实际 %d", byKey["k-2"].Balance)
	}
	if byKey["k-3"].Balance != model.BalanceUnknown {
		t.Fatalf("k-3 未提供余额，应为未知(%d)，实际 %d", model.BalanceUnknown, byKey["k-3"].Balance)
	}
	// 已知余额应记录更新时间；未知的保持零值
	if byKey["k-1"].BalanceUpdatedAt.IsZero() {
		t.Fatal("已知余额应记录更新时间")
	}
	if !byKey["k-3"].BalanceUpdatedAt.IsZero() {
		t.Fatal("未知余额不应记录更新时间")
	}
}

// TestChannelKey_重贴密钥_不覆盖已录余额 是本任务最关键的不变量：
// ReplaceAll 系列是"差集增删 + 保留已有统计"语义，重新粘贴同一批密钥时
// 绝不能把人工录入的余额覆盖为"未知"。
func TestChannelKey_重贴密钥_不覆盖已录余额(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAllWithBalance(ctx, 1, []string{"keep-me"}, nil, nil); err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	keys, err := repo.ListByChannel(ctx, 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("读取密钥失败: %v", err)
	}
	id := keys[0].ID
	if keys[0].Balance != model.BalanceUnknown {
		t.Fatalf("未提供余额应为未知(%d)，实际 %d", model.BalanceUnknown, keys[0].Balance)
	}

	// 人工录入余额 50
	if err := repo.UpdateBalance(ctx, id, 50); err != nil {
		t.Fatalf("更新余额失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].Balance != 50 {
		t.Fatalf("余额应为 50，实际 %d", got[0].Balance)
	}
	if got[0].BalanceUpdatedAt.IsZero() {
		t.Fatal("更新余额应记录更新时间")
	}

	// 重贴同一批密钥且未提供余额 → 已录余额必须保留
	if _, _, err := repo.ReplaceAllWithBalance(ctx, 1, []string{"keep-me"}, nil, nil); err != nil {
		t.Fatalf("重贴失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if got[0].Balance != 50 {
		t.Fatalf("重贴未提供余额时不应覆盖已录余额，期望 50，实际 %d", got[0].Balance)
	}

	// 走旧入口 ReplaceAll 重贴 → 同样不能把余额重置为未知
	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"keep-me"}, nil); err != nil {
		t.Fatalf("ReplaceAll 重贴失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if got[0].Balance != 50 {
		t.Fatalf("ReplaceAll 重贴不应把余额重置，期望 50，实际 %d", got[0].Balance)
	}

	// 本次显式提供已知余额 → 允许更新
	if _, _, err := repo.ReplaceAllWithBalance(ctx, 1, []string{"keep-me"}, nil, []int64{70}); err != nil {
		t.Fatalf("显式更新余额失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if got[0].Balance != 70 {
		t.Fatalf("显式提供余额时应更新为 70，实际 %d", got[0].Balance)
	}
}

// TestChannelKey_UpdateBalance_边界与不存在 验证余额的取值边界与"找不到"语义。
func TestChannelKey_UpdateBalance_边界与不存在(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"b1"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	id := keys[0].ID

	// 非法值（小于 -1）应被拒绝
	if err := repo.UpdateBalance(ctx, id, model.BalanceUnknown-1); err == nil {
		t.Fatal("余额小于 -1 应被拒绝")
	}

	// 置为未知：既不耗尽，也不带更新时间语义上的"已知"
	if err := repo.UpdateBalance(ctx, id, model.BalanceUnknown); err != nil {
		t.Fatalf("置为未知失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	if got[0].Balance != model.BalanceUnknown || got[0].BalanceExhausted() {
		t.Fatalf("置为未知后应为未知且不耗尽: %+v", got[0])
	}

	// 更新不存在的凭据 → 返回 not found
	if err := repo.UpdateBalance(ctx, 999999, 10); !errors.Is(err, model.ErrChannelKeyNotFound) {
		t.Fatalf("更新不存在的凭据应返回 ErrChannelKeyNotFound，实际 %v", err)
	}
}

// TestChannelKey_UpdateRouting_往返与清空 验证凭据级分组/模型的落库与读回（迁移 0038）。
//
// 两个关键点：
//  1. 落库后必须能原样读回（CSV 编解码两侧对称，否则调度会按错误的分叉过滤）；
//  2. 提交空清单必须能真正清空限制（= 不限）——否则站长改不回"不限分组"，
//     只能靠手工改库，这属于不可完成的操作。
func TestChannelKey_UpdateRouting_往返与清空(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"r1"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, _ := repo.ListByChannel(ctx, 1)
	id := keys[0].ID

	// 新导入的凭据默认不限（空清单），保证升级后既有行为不变
	if len(keys[0].Groups) != 0 || len(keys[0].Models) != 0 {
		t.Fatalf("默认应为不限分组/模型，实际 groups=%v models=%v", keys[0].Groups, keys[0].Models)
	}

	// 写入分组与模型（含通配符）；重复项与空白应被归一
	if err := repo.UpdateRouting(ctx, id, []string{"vip", " vip ", "svip"}, []string{"gpt-4o", "gpt-4*"}); err != nil {
		t.Fatalf("更新分叉失败: %v", err)
	}
	got, _ := repo.ListByChannel(ctx, 1)
	key := got[0]
	if len(key.Groups) != 2 || key.Groups[0] != "vip" || key.Groups[1] != "svip" {
		t.Fatalf("分组应去重并保持顺序，实际 %v", key.Groups)
	}
	if len(key.Models) != 2 || key.Models[1] != "gpt-4*" {
		t.Fatalf("模型清单读回不符，实际 %v", key.Models)
	}
	// 读回后的判定必须与写入一致（否则分叉会在调度层静默失效）
	if !key.MatchesScope("svip", "gpt-4o-mini") {
		t.Fatalf("读回后应命中 svip 分组下的 gpt-4* 模型，实际不命中：%+v", key)
	}

	// 清空：空清单 = 不限，必须能真正生效
	if err := repo.UpdateRouting(ctx, id, nil, nil); err != nil {
		t.Fatalf("清空分叉失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if len(got[0].Groups) != 0 || len(got[0].Models) != 0 {
		t.Fatalf("清空后应为不限，实际 groups=%v models=%v", got[0].Groups, got[0].Models)
	}
	if !got[0].MatchesScope("任意分组", "任意模型") {
		t.Fatal("清空限制后该凭据应对所有分组与模型可用")
	}

	// 更新不存在的凭据 → 返回 not found
	if err := repo.UpdateRouting(ctx, 999999, []string{"vip"}, nil); !errors.Is(err, model.ErrChannelKeyNotFound) {
		t.Fatalf("更新不存在的凭据应返回 ErrChannelKeyNotFound，实际 %v", err)
	}
}

// TestChannelKey_额度窗口_双窗口落库往返 验证迁移 0048 新增列（主/次窗口 + 窗口时长）的读写。
//
// 重点：
//  1. 新导入的凭据两个窗口都应落成"未探测"(-1)，而不是 0%（否则界面会显示"额度充足"）；
//  2. 写入的 7 个字段必须能原样读回（列清单与扫描顺序一致，否则会静默错位）；
//  3. 置回未知时重置时间一并清空，避免依据过期数据判定"是否恢复"。
func TestChannelKey_额度窗口_双窗口落库往返(t *testing.T) {
	repo, _ := newTestKeyRepo(t)
	ctx := context.Background()

	if _, _, err := repo.ReplaceAll(ctx, 1, []string{"nvapi-quota"}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	keys, err := repo.ListByChannel(ctx, 1)
	if err != nil || len(keys) != 1 {
		t.Fatalf("读取密钥失败: %v", err)
	}
	if keys[0].QuotaUsedPercent != model.QuotaUsedPercentUnknown ||
		keys[0].QuotaSecondaryUsedPercent != model.QuotaUsedPercentUnknown {
		t.Fatalf("新导入的额度应为未知(-1): primary=%d secondary=%d",
			keys[0].QuotaUsedPercent, keys[0].QuotaSecondaryUsedPercent)
	}
	if keys[0].QuotaKnown() || keys[0].QuotaSecondaryKnown() {
		t.Fatal("新导入的凭据不应被判定为已探测")
	}
	id := keys[0].ID

	primaryReset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	secondaryReset := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	if err := repo.UpdateQuota(ctx, id, model.QuotaWindows{
		PrimaryUsedPercent:     70,
		PrimaryResetAt:         primaryReset,
		PrimaryWindowSeconds:   18000,
		SecondaryUsedPercent:   88,
		SecondaryResetAt:       secondaryReset,
		SecondaryWindowSeconds: 604800,
	}); err != nil {
		t.Fatalf("写入额度快照失败: %v", err)
	}

	got, _ := repo.ListByChannel(ctx, 1)
	k := got[0]
	if k.QuotaUsedPercent != 70 || k.QuotaResetAt.Unix() != primaryReset.Unix() || k.QuotaPrimaryWindowSeconds != 18000 {
		t.Fatalf("主窗口未正确落地: %+v", k)
	}
	if k.QuotaSecondaryUsedPercent != 88 || k.QuotaSecondaryResetAt.Unix() != secondaryReset.Unix() || k.QuotaSecondaryWindowSeconds != 604800 {
		t.Fatalf("次窗口未正确落地: %+v", k)
	}
	if !k.QuotaKnown() || !k.QuotaSecondaryKnown() {
		t.Fatal("写入后应判定为已探测")
	}

	// 置回未知：两个窗口一起恢复"未探测"，窗口时长与重置时间清零
	if err := repo.UpdateQuota(ctx, id, model.QuotaWindows{
		PrimaryUsedPercent:   model.QuotaUsedPercentUnknown,
		SecondaryUsedPercent: model.QuotaUsedPercentUnknown,
	}); err != nil {
		t.Fatalf("置回未知失败: %v", err)
	}
	got, _ = repo.ListByChannel(ctx, 1)
	if got[0].QuotaKnown() || got[0].QuotaSecondaryKnown() {
		t.Fatalf("置回未知后不应判定为已探测: %+v", got[0])
	}
	if !got[0].QuotaResetAt.IsZero() || !got[0].QuotaSecondaryResetAt.IsZero() {
		t.Fatal("置回未知时应一并清空重置时间（避免依据过期数据判定是否恢复）")
	}
	if got[0].QuotaPrimaryWindowSeconds != 0 || got[0].QuotaSecondaryWindowSeconds != 0 {
		t.Fatal("未提供窗口时长时应落成 0")
	}

	// 更新不存在的凭据 → 返回 not found
	if err := repo.UpdateQuota(ctx, 999999, model.QuotaWindows{PrimaryUsedPercent: 1}); !errors.Is(err, model.ErrChannelKeyNotFound) {
		t.Fatalf("更新不存在的凭据应返回 ErrChannelKeyNotFound，实际 %v", err)
	}
}
