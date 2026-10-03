// 本文件验证 model.ModelSpeedRepository 的 SQL 实现与迁移 0045。
//
// 意图（Why）：
//
//	测速结果的正确性直接影响两个展示面（模型广场的延迟列、后台测速页），
//	而它最容易写坏的地方是 UPSERT 语义——"同一渠道 × 模型只留最近一次"
//	一旦失效（变成追加），广场会读到陈旧数字而无人察觉。
//
// 流转（Flow）：
//
//	newTestStore（真实 SQLite + 迁移）→ 仓储方法往返 → 断言
package store

import (
	"context"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestModelSpeed_UpsertKeepsLatest 验证 UPSERT 语义：同一 (渠道, 模型)
// 再次写入只覆盖不追加，且全部字段被更新。
func TestModelSpeed_UpsertKeepsLatest(t *testing.T) {
	st := newTestStore(t)
	repo := NewModelSpeedRepository(st.DB())
	ctx := context.Background()

	first := &model.ModelSpeedResult{
		ChannelID:     7,
		Model:         "gpt-4o",
		UpstreamModel: "gpt-4o-up",
		OK:            true,
		StatusCode:    200,
		TTFBMS:        800,
		TotalMS:       900,
		Message:       "",
		TestedAt:      time.Unix(1000, 0),
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	// 第二次写入同一组合：结果变差（模拟重新测速）。
	second := &model.ModelSpeedResult{
		ChannelID:     7,
		Model:         "gpt-4o",
		UpstreamModel: "gpt-4o-up2",
		OK:            false,
		StatusCode:    429,
		TTFBMS:        0,
		TotalMS:       2000,
		Message:       "限流",
		TestedAt:      time.Unix(2000, 0),
	}
	if err := repo.Upsert(ctx, second); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}

	results, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("同组合应只有一行（UPSERT 语义），实际 %d 行", len(results))
	}

	got := results[0]
	if got.OK || got.StatusCode != 429 || got.TTFBMS != 0 || got.TotalMS != 2000 {
		t.Errorf("字段未更新为最近一次结果: %+v", got)
	}
	if got.UpstreamModel != "gpt-4o-up2" || got.Message != "限流" {
		t.Errorf("upstream_model / message 未更新: %+v", got)
	}
	if got.TestedAt.Unix() != 2000 {
		t.Errorf("tested_at = %d，期望 2000", got.TestedAt.Unix())
	}
}

// TestModelSpeed_LatestAcrossChannels 验证 Latest 返回各渠道 × 模型组合，
// 广场侧据此在内存里聚合 MIN(ttfb)。
func TestModelSpeed_LatestAcrossChannels(t *testing.T) {
	st := newTestStore(t)
	repo := NewModelSpeedRepository(st.DB())
	ctx := context.Background()

	seed := []model.ModelSpeedResult{
		{ChannelID: 1, Model: "m-a", OK: true, StatusCode: 200, TTFBMS: 300, TotalMS: 350, TestedAt: time.Unix(100, 0)},
		{ChannelID: 2, Model: "m-a", OK: true, StatusCode: 200, TTFBMS: 120, TotalMS: 150, TestedAt: time.Unix(101, 0)},
		{ChannelID: 1, Model: "m-b", OK: false, StatusCode: 500, TTFBMS: 0, TotalMS: 500, Message: "上游错误", TestedAt: time.Unix(102, 0)},
	}
	for i := range seed {
		if err := repo.Upsert(ctx, &seed[i]); err != nil {
			t.Fatalf("写入种子数据失败: %v", err)
		}
	}

	results, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("应返回 3 行（2 渠道 × 2 模型组合），实际 %d", len(results))
	}

	// 广场聚合口径的最小值检查：m-a 的最快是 120ms。
	best := map[string]int{}
	for _, r := range results {
		if !r.OK || r.TTFBMS <= 0 {
			continue
		}
		if cur, ok := best[r.Model]; !ok || r.TTFBMS < cur {
			best[r.Model] = r.TTFBMS
		}
	}
	if best["m-a"] != 120 {
		t.Errorf("m-a 聚合后最小 TTFB = %d，期望 120", best["m-a"])
	}
	if _, has := best["m-b"]; has {
		t.Error("失败结果（ok=0）不应参与最小值聚合")
	}
}

// TestModelSpeed_DeleteByChannel 验证渠道删除时的级联清理。
func TestModelSpeed_DeleteByChannel(t *testing.T) {
	st := newTestStore(t)
	repo := NewModelSpeedRepository(st.DB())
	ctx := context.Background()

	seed := []model.ModelSpeedResult{
		{ChannelID: 1, Model: "m-a", OK: true, StatusCode: 200, TTFBMS: 100, TotalMS: 120, TestedAt: time.Unix(1, 0)},
		{ChannelID: 1, Model: "m-b", OK: true, StatusCode: 200, TTFBMS: 110, TotalMS: 130, TestedAt: time.Unix(1, 0)},
		{ChannelID: 2, Model: "m-a", OK: true, StatusCode: 200, TTFBMS: 140, TotalMS: 160, TestedAt: time.Unix(1, 0)},
	}
	for i := range seed {
		if err := repo.Upsert(ctx, &seed[i]); err != nil {
			t.Fatalf("写入种子数据失败: %v", err)
		}
	}

	if err := repo.DeleteByChannel(ctx, 1); err != nil {
		t.Fatalf("按渠道删除失败: %v", err)
	}

	results, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(results) != 1 || results[0].ChannelID != 2 {
		t.Fatalf("应只剩渠道 2 的一行，实际 %+v", results)
	}
}

// TestModelSpeed_RejectsInvalid 验证缺少渠道或模型名的结果在写入前被拒绝。
func TestModelSpeed_RejectsInvalid(t *testing.T) {
	st := newTestStore(t)
	repo := NewModelSpeedRepository(st.DB())

	err := repo.Upsert(context.Background(), &model.ModelSpeedResult{ChannelID: 0, Model: "m"})
	if err == nil {
		t.Error("缺少渠道 ID 应被拒绝")
	}
	err = repo.Upsert(context.Background(), &model.ModelSpeedResult{ChannelID: 1, Model: "  "})
	if err == nil {
		t.Error("缺少模型名应被拒绝")
	}
}
