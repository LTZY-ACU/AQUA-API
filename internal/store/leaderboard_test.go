// 排行榜聚合与峰值并发算法的测试。
//
// 意图（Why）：
//   排行榜的分数口径、付费/免费分榜、峰值并发估算都直接呈现给用户，
//   出错可见性强。这里重点锁定：
//     1) peakConcurrency 的差分扫描算法（区间重叠边界最容易算错）；
//     2) Leaderboard 的成功请求过滤（失败请求不得混入分数与耗时）；
//     3) 付费标记（EXISTS payment_orders status=2）正确归属分组。
//
// 流转（Flow）：
//   go test ./internal/store/ -run Leaderboard → 内存 SQLite 写入样本后核验
package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

func TestPeakConcurrency_Basic(t *testing.T) {
	cases := []struct {
		name   string
		events []usageEvent
		want   int64
	}{
		{name: "空输入", events: nil, want: 0},
		{
			name: "两个区间重叠一部分",
			// 请求 A: [100, 130]，请求 B: [120, 150] → 120~130 重叠，峰值 2
			events: []usageEvent{
				{at: 100, delta: 1}, {at: 130, delta: -1},
				{at: 120, delta: 1}, {at: 150, delta: -1},
			},
			want: 2,
		},
		{
			name: "三个区间全重叠于一点",
			events: []usageEvent{
				{at: 10, delta: 1}, {at: 20, delta: -1},
				{at: 15, delta: 1}, {at: 25, delta: -1},
				{at: 12, delta: 1}, {at: 22, delta: -1},
			},
			want: 3,
		},
		{
			name: "同一秒开始与结束算重叠",
			// 请求 A 结束于 50，请求 B 开始于 50 → 同时刻 +1 在前，峰值 2
			events: []usageEvent{
				{at: 40, delta: 1}, {at: 50, delta: -1},
				{at: 50, delta: 1}, {at: 60, delta: -1},
			},
			want: 2,
		},
		{
			name: "乱序输入仍求对峰值",
			events: []usageEvent{
				{at: 200, delta: -1},
				{at: 100, delta: 1},
				{at: 150, delta: 1},
				{at: 180, delta: -1},
			},
			want: 2, // 100~180 与 150~200 重叠于 150~180
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peakConcurrency(tc.events); got != tc.want {
				t.Fatalf("peakConcurrency = %d，期望 %d", got, tc.want)
			}
		})
	}
}

// TestLeaderboard_SuccessOnlyAndPaidSplit 验证排行榜的三大口径：
//  1. 只统计成功请求（500 的失败请求不出现在任何榜单）；
//  2. 付费用户（有已支付订单）进付费榜，否则进免费榜；
//  3. 请求数 / token 聚合正确。
func TestLeaderboard_SuccessOnlyAndPaidSplit(t *testing.T) {
	st := newTestStore(t)
	repo := NewUsageLogRepository(st.DB(), st.Dialect())
	ctx := context.Background()
	now := time.Now()
	since := now.AddDate(0, 0, -30)
	until := now.Add(time.Hour)

	userID := uint64(42)
	paidUserID := uint64(7)

	// 免费用户：2 次成功 + 1 次失败
	createLog(t, repo, userID, now.Add(-time.Hour), 200, 100, 80)  // 成功 token=100
	createLog(t, repo, userID, now.Add(-2*time.Hour), 200, 200, 120)
	createLog(t, repo, userID, now.Add(-3*time.Hour), 500, 999, 10) // 失败：请求数计入、token 计入、成功率扣分

	// 付费用户：1 次成功
	createLog(t, repo, paidUserID, now.Add(-time.Hour), 200, 300, 60)

	// 标记付费：写入一条已支付订单（status=2, credited=1）
	createPaidOrder(t, st, paidUserID)

	entries, err := repo.Leaderboard(ctx, model.UsageLogQuery{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("Leaderboard 失败: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("榜单条目数 = %d，期望 2（两个用户各一条）", len(entries))
	}

	// 免费用户校验
	var freeEntry *model.LeaderboardEntry
	var paidEntry *model.LeaderboardEntry
	for i := range entries {
		switch entries[i].UserID {
		case userID:
			freeEntry = &entries[i]
		case paidUserID:
			paidEntry = &entries[i]
		}
	}
	if freeEntry == nil {
		t.Fatal("未找到免费用户的榜单条目")
	}
	if freeEntry.Paid {
		t.Error("免费用户被错误标记为付费")
	}
	// 新口径：请求数统计全部请求（含失败），成功率单独反映稳定性
	if freeEntry.Requests != 3 {
		t.Errorf("免费用户请求数 = %d，期望 3（含失败请求）", freeEntry.Requests)
	}
	if freeEntry.SuccessRequests != 2 {
		t.Errorf("免费用户成功请求数 = %d，期望 2", freeEntry.SuccessRequests)
	}
	if rate := freeEntry.SuccessRate(); rate < 0.66 || rate > 0.67 {
		t.Errorf("免费用户成功率 = %v，期望约 0.667（2/3）", rate)
	}
	if freeEntry.Tokens != 1299 {
		t.Errorf("免费用户 token = %d，期望 1299（全部请求累计）", freeEntry.Tokens)
	}
	// 平均耗时只按成功请求计算（失败请求耗时不可信）
	if freeEntry.AvgLatencyMS != 100 {
		// (80+120)/2 = 100
		t.Errorf("免费用户平均耗时 = %v，期望 100（仅成功请求）", freeEntry.AvgLatencyMS)
	}

	// 分数封顶 100：榜首（各项最大）应为 100 分
	score := freeEntry.LeaderboardScore(freeEntry.Requests, freeEntry.Tokens)
	if score != 100 {
		t.Errorf("榜首分数 = %v，期望 100（0~100 封顶）", score)
	}

	// 付费用户 1 次成功：请求 1、成功率 100%
	if paidEntry.Requests != 1 || paidEntry.SuccessRequests != 1 {
		t.Errorf("付费用户聚合错误: requests=%d success=%d", paidEntry.Requests, paidEntry.SuccessRequests)
	}
	if rate := paidEntry.SuccessRate(); rate != 1 {
		t.Errorf("付费用户成功率 = %v，期望 1（全成功）", rate)
	}
	if !paidEntry.Paid {
		t.Error("有已支付订单的用户应标记为付费")
	}
	if paidEntry.Tokens != 300 {
		t.Errorf("付费用户 token = %d，期望 300", paidEntry.Tokens)
	}
}

// TestLeaderboard_PeakConcurrency 验证同名用户在窗口内的峰值并发估算。
func TestLeaderboard_PeakConcurrency(t *testing.T) {
	st := newTestStore(t)
	repo := NewUsageLogRepository(st.DB(), st.Dialect())
	ctx := context.Background()
	now := time.Now()
	since := now.AddDate(0, 0, -7)
	until := now.Add(time.Hour)
	userID := uint64(99)

	// 构造三个重叠区间：base=now-500s
	//  A: [T,   T+20]   => created_at = T+20, latency = 20000ms
	//  B: [T+10,T+40]  => created_at = T+40, latency = 30000ms
	//  C: [T+30,T+50]  => created_at = T+50, latency = 20000ms
	//  A 与 B 重叠（T+10~T+20），B 与 C 重叠（T+30~T+40），峰值 2。
	base := now.Add(-500 * time.Second).Unix()
	logs := []*model.UsageLog{
		{UserID: userID, ChannelID: 1, Model: "m", TotalTokens: 1, StatusCode: 200, LatencyMS: 20000, CreatedAt: time.Unix(base+20, 0)},
		{UserID: userID, ChannelID: 1, Model: "m", TotalTokens: 1, StatusCode: 200, LatencyMS: 30000, CreatedAt: time.Unix(base+40, 0)},
		{UserID: userID, ChannelID: 1, Model: "m", TotalTokens: 1, StatusCode: 200, LatencyMS: 20000, CreatedAt: time.Unix(base+50, 0)},
	}
	for _, l := range logs {
		if err := repo.Create(ctx, l); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	entries, err := repo.Leaderboard(ctx, model.UsageLogQuery{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("Leaderboard 失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(entries))
	}
	if entries[0].PeakConcurrency != 2 {
		t.Errorf("峰值并发 = %d，期望 2", entries[0].PeakConcurrency)
	}
}

// createLog 写入一条指定时间/状态/token/耗时的日志。
func createLog(t *testing.T, repo model.UsageLogRepository, userID uint64, at time.Time, status int, tokens int, latency int) {
	t.Helper()
	entry := &model.UsageLog{
		UserID:      userID,
		ChannelID:   1,
		Model:       "gpt-4o",
		TotalTokens: tokens,
		StatusCode:  status,
		LatencyMS:   latency,
		CreatedAt:   at,
	}
	if err := repo.Create(context.Background(), entry); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}
}

// createPaidOrder 写入一条已支付订单（status=2），用于付费判定测试。
func createPaidOrder(t *testing.T, st *Store, userID uint64) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := st.DB().Exec(`INSERT INTO payment_orders
		(trade_no, user_id, amount, currency, quota, method, status, credited, created_at, updated_at)
		VALUES (?, ?, 100, 'CNY', 10000, 'manual', 2, 1, ?, ?)`,
		"test-paid-"+strconv.FormatUint(userID, 10), userID, now, now); err != nil {
		t.Fatalf("写入已支付订单失败: %v", err)
	}
}
