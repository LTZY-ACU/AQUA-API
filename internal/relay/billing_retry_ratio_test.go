// 折扣分组重试率统计的单元测试。
//
// 意图（Why）：
//
//	6 折这类折扣档的净利薄到"重试成本"足以吞掉本金，因此必须能观测
//	「真实重试率 r = 上游调用次数 / 计费请求次数」并给出保本告警。
//	本组测试把三件事锁死：
//	  1) 只统计折扣分组（全价分组不产生计数，省锁）；
//	  2) 快照把 r 从"只在日志里出现"变成"界面上可见"（面板可达）；
//	  3) 越过保本线时 OverBreakEven 为 true（告警依据）。
//
// 流转（Flow）：
//
//	go test ./internal/relay/ -run RetryRatio → recordUpstreamCall → RetryRatioSnapshots
package relay

import (
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newRetryBilling 构造一个带指定分组倍率的计费组件（供重试率统计测试）。
func newRetryBilling(ratio int64) *Billing {
	return NewBilling(&fakePriceRepo{}, newFakeGroupRepo(ratio), nil, nil, "default")
}

// TestRetryRatio_全价分组不产生计数 锁住"只统计折扣档"的取舍。
func TestRetryRatio_全价分组不产生计数(t *testing.T) {
	b := newRetryBilling(100)
	b.recordUpstreamCall("default", true)
	b.recordUpstreamCall("default", true)
	if got := b.RetryRatioSnapshots(); len(got) != 0 {
		t.Fatalf("全价分组不应产生计数，实际 %d 条", len(got))
	}
}

// TestRetryRatio_折扣分组累计并出快照 锁住"上游调用与计费请求分开累计"。
func TestRetryRatio_折扣分组累计并出快照(t *testing.T) {
	b := newRetryBilling(60)
	// 60 次上游调用，50 次计费 → r = 1.2
	for i := 0; i < 60; i++ {
		b.recordUpstreamCall("default", false)
	}
	for i := 0; i < 50; i++ {
		b.recordUpstreamCall("default", true)
	}
	snaps := b.RetryRatioSnapshots()
	if len(snaps) != 1 {
		t.Fatalf("应有 1 条快照，实际 %d", len(snaps))
	}
	snap := snaps[0]
	if snap.UpstreamCalls != 110 {
		t.Fatalf("上游调用应为 110（60 未计费 + 50 计费），实际 %d", snap.UpstreamCalls)
	}
	if snap.ChargedRequests != 50 {
		t.Fatalf("计费请求应为 50，实际 %d", snap.ChargedRequests)
	}
	// r = 110 / 50 = 2.2（不含在保本线内：0.94 × 2.2 = 2.068 < 1.37 的判定基准不对，此处只验数值）
	if snap.RetryRatio != 2.2 {
		t.Fatalf("r 应为 2.2，实际 %v", snap.RetryRatio)
	}
}

// TestRetryRatio_越过保本线即标记 锁住告警判定。
func TestRetryRatio_越过保本线即标记(t *testing.T) {
	b := newRetryBilling(60)
	// 计费请求凑够最小样本（50），上游调用达到 r > 1.37
	for i := 0; i < 100; i++ {
		b.recordUpstreamCall("default", false)
	}
	for i := 0; i < 50; i++ {
		b.recordUpstreamCall("default", true)
	}
	snap := b.RetryRatioSnapshots()[0]
	// r = 150/50 = 3.0，远超 1.37
	if !snap.OverBreakEven {
		t.Fatalf("r=3.0 应越过保本线，实际 OverBreakEven=%v", snap.OverBreakEven)
	}
}

// TestRetryRatio_样本不足不告警 锁住"样本太少不判定"。
func TestRetryRatio_样本不足不告警(t *testing.T) {
	b := newRetryBilling(60)
	for i := 0; i < 10; i++ {
		b.recordUpstreamCall("default", false)
		b.recordUpstreamCall("default", true)
	}
	snap := b.RetryRatioSnapshots()[0]
	if snap.OverBreakEven {
		t.Fatalf("样本不足 50 时不应告警（r=2.0 但样本不够），实际 OverBreakEven=%v", snap.OverBreakEven)
	}
}

// TestRetryRatio_多分组各自累计 锁住"按分组隔离"。
func TestRetryRatio_多分组各自累计(t *testing.T) {
	groups := &fakeGroupRepo{groups: map[string]*model.ModelGroup{
		"default": {ID: 1, Name: "default", Ratio: 100, Enabled: true},
		"agent":   {ID: 2, Name: "agent", Ratio: 60, Enabled: true},
	}}
	b := NewBilling(&fakePriceRepo{}, groups, nil, nil, "default")
	b.recordUpstreamCall("default", true) // 全价：不计
	b.recordUpstreamCall("agent", true)   // 折扣：计
	snaps := b.RetryRatioSnapshots()
	if len(snaps) != 1 || snaps[0].Group != "agent" {
		t.Fatalf("应只有 agent 一条快照，实际 %+v", snaps)
	}
}
