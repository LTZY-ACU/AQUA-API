// 本文件是渠道探针历史仓储与看板纯函数的单元测试。
//
// 意图（Why）：
//
//	探针历史是"只写不删"型数据里写入频率最高的一张，
//	一旦查询条件写错（时间边界反了、失败过滤漏了、limit 没兜底），
//	后果不是报错而是一条**看起来合理但其实错**的曲线——
//	站长会照着错误结论去停用或修复渠道，比没有看板更糟。
//	因此测试重心放在边界：同秒多条的排序、Until 的闭区间、
//	失败过滤、limit 上下界、分位数取法、无样本时的 Valid。
//
// 流转（Flow）：
//
//	go test ./internal/store/ → newTestStore（临时 SQLite，跑真实迁移）
//	  → ChannelProbeLogs.Append → List/Count → 断言过滤与排序
//	go test ./internal/server/ → 纯函数直调 → 断言看板判定与统计口径
//
// 扩展（Extend）：
//	新增查询条件时：在 TestChannelProbeLogRepo_List 中加一段"造数 → 断言过滤"，
//	沿用既有 execSQL/countRows 辅助，不要新起一套造数风格。
package store

import (
	"context"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// appendProbeLog 往库里写一条探针历史，返回写入时刻。
func appendProbeLog(t *testing.T, repo model.ChannelProbeLogRepository, id uint64, at time.Time, ok bool, latency int) {
	t.Helper()
	if err := repo.Append(context.Background(), &model.ChannelProbeLog{
		ChannelID: id,
		At:        at,
		OK:        ok,
		// 失败时也带上耗时：真实场景里 401 是"立刻被拒"（耗时极短），
		// 若造数时把失败一律记成 0 耗时，就测不出"失败耗时该不该进分位数"这个问题。
		LatencyMS:  latency,
		StatusCode: 200,
		Model:      "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("追加探针历史失败: %v", err)
	}
}

// TestChannelProbeLogRepo_AppendAndList 验证追加后可按时间倒序读回。
func TestChannelProbeLogRepo_AppendAndList(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	base := time.Now().Truncate(time.Second)
	appendProbeLog(t, repo, 1, base.Add(-2*time.Minute), true, 120)
	appendProbeLog(t, repo, 1, base.Add(-1*time.Minute), true, 180)
	appendProbeLog(t, repo, 1, base, true, 240)

	got, err := repo.List(ctx, model.ChannelProbeLogQuery{ChannelID: 1})
	if err != nil {
		t.Fatalf("查询探针历史失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应返回 3 条，实际 %d 条", len(got))
	}
	// 倒序：最新的在前（前端曲线按此顺序拿到的就是"从新到旧"）
	if got[0].LatencyMS != 240 || got[2].LatencyMS != 120 {
		t.Fatalf("未按时间倒序返回，实际顺序 %d/%d/%d",
			got[0].LatencyMS, got[1].LatencyMS, got[2].LatencyMS)
	}
	if !got[0].OK {
		t.Fatal("OK 字段读取后应为 true")
	}
	// 时间精度：库里存的是 Unix 秒，读回来不能带子秒余数，
	// 否则前端格式化出的时间字符串会在每次请求间跳动。
	if got[0].At.Nanosecond() != 0 {
		t.Fatalf("读回的时间带子秒精度（%v），前端展示会抖动", got[0].At)
	}
}

// TestChannelProbeLogRepo_Append_At零值兜底为现在 验证未指定时间不会写出"永远查不到"的行。
//
// 这是真实会发生的情况：调用方（或将来的新接入点）忘了填 At。
// 若原样写入 Unix 0，这行在按时间过滤的查询里永远不出现，
// 表现为"探针明明跑了但历史是空的"，且没有任何错误提示。
func TestChannelProbeLogRepo_Append_At零值兜底为现在(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	if err := repo.Append(ctx, &model.ChannelProbeLog{ChannelID: 7, OK: true, LatencyMS: 90}); err != nil {
		t.Fatalf("追加失败: %v", err)
	}

	// 不设 Since：用兜底后的时间必须能落在"最近一小时"内被查到
	since := time.Now().Add(-time.Hour)
	got, err := repo.List(ctx, model.ChannelProbeLogQuery{ChannelID: 7, Since: since})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("零值时间应被兜底为当前时间并可查到，实际查到 %d 条", len(got))
	}
}

// TestChannelProbeLogRepo_List_时间边界 验证 Since/Until 都是闭区间。
//
// 闭区间是刻意选的：探针的"窗口内"应该在边界那一轮也被计入。
// 若改成半开区间，恰好落在边界的探测会被静默漏掉——
// 而它恰恰是最值得看的那一次（窗口刚打开时的第一条 / 刚关闭前的最后一条）。
func TestChannelProbeLogRepo_List_时间边界(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	base := time.Now().Truncate(time.Second)
	appendProbeLog(t, repo, 1, base.Add(-3*time.Hour), true, 100)
	appendProbeLog(t, repo, 1, base.Add(-2*time.Hour), true, 110)
	appendProbeLog(t, repo, 1, base.Add(-1*time.Hour), true, 120)
	appendProbeLog(t, repo, 1, base, true, 130)

	// 窗口恰好卡住两条边界记录
	got, err := repo.List(ctx, model.ChannelProbeLogQuery{
		ChannelID: 1,
		Since:     base.Add(-2 * time.Hour),
		Until:     base.Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("闭区间应命中 2 条（含两端），实际 %d 条", len(got))
	}
	if got[0].LatencyMS != 120 || got[1].LatencyMS != 110 {
		t.Fatalf("边界记录内容不符：期望 120/110，实际 %d/%d", got[0].LatencyMS, got[1].LatencyMS)
	}
}

// TestChannelProbeLogRepo_List_只返回失败 验证 OnlyFailures 过滤。
func TestChannelProbeLogRepo_List_只返回失败(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	base := time.Now().Truncate(time.Second)
	appendProbeLog(t, repo, 1, base.Add(-2*time.Minute), true, 100)
	appendProbeLog(t, repo, 1, base.Add(-time.Minute), false, 0)
	appendProbeLog(t, repo, 1, base, false, 0)

	got, err := repo.List(ctx, model.ChannelProbeLogQuery{ChannelID: 1, OnlyFailures: true})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("只返回失败应得 2 条，实际 %d 条", len(got))
	}
	for _, l := range got {
		if l.OK {
			t.Fatal("OnlyFailures 结果里混进了成功记录")
		}
	}

	// 过滤后 Count 必须与 List 同口径：
	// 若两处条件写歪，看板会显示"共 5 次失败"而实际只列 2 次。
	n, err := repo.Count(ctx, model.ChannelProbeLogQuery{ChannelID: 1, OnlyFailures: true})
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("Count 与 List 口径不一致：Count=%d, List=%d", n, len(got))
	}
}

// TestChannelProbeLogRepo_List_按渠道隔离 验证多渠道互不串数据。
func TestChannelProbeLogRepo_List_按渠道隔离(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	base := time.Now().Truncate(time.Second)
	appendProbeLog(t, repo, 1, base.Add(-time.Minute), true, 100)
	appendProbeLog(t, repo, 2, base.Add(-time.Minute), true, 200)
	appendProbeLog(t, repo, 2, base, true, 210)

	// ChannelID=0 表示全站
	all, err := repo.List(ctx, model.ChannelProbeLogQuery{})
	if err != nil {
		t.Fatalf("查询全站失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全站查询应得 3 条，实际 %d 条", len(all))
	}

	only2, err := repo.List(ctx, model.ChannelProbeLogQuery{ChannelID: 2})
	if err != nil {
		t.Fatalf("查询渠道 2 失败: %v", err)
	}
	if len(only2) != 2 {
		t.Fatalf("渠道 2 应有 2 条，实际 %d 条", len(only2))
	}
	for _, l := range only2 {
		if l.ChannelID != 2 {
			t.Fatalf("渠道 2 的结果里混进了渠道 %d", l.ChannelID)
		}
	}
}

// TestProbeLogLimit 验证 limit 的三段归一：缺省、上限、负值。
//
// 这一段是"防御一个必然发生的事"：保留期被配成 0（永久留痕）时，
// limit 传 0 或负数就等于把整张表拉进内存。
func TestProbeLogLimit(t *testing.T) {
	cases := []struct {
		name  string
		in    int
		want  int
		notes string
	}{
		{"零值走默认", 0, probeLogDefaultLimit, "不给 limit 时给一个能画曲线的量"},
		{"负值走默认", -5, probeLogDefaultLimit, "负数不是「要更多」的意思，一律回默认"},
		{"正常值透传", 50, 50, "合法值不该被改写"},
		{"超上限被收敛", probeLogMaxLimit + 1, probeLogMaxLimit, "防止一次请求拉回整张表"},
		{"恰好上限透传", probeLogMaxLimit, probeLogMaxLimit, "边界值不该被误收敛"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeLogLimit(tc.in); got != tc.want {
				t.Fatalf("probeLogLimit(%d)=%d，期望 %d（%s）", tc.in, got, tc.want, tc.notes)
			}
		})
	}
}

// TestChannelProbeLogRepo_删除渠道后历史仍可读 验证历史不随渠道删除而消失。
//
// 这是刻意不做 JOIN 的原因：渠道被删之后，恰恰最需要看它最后几次探测
// 是什么结果（"它是怎么坏的"）。若历史随渠道一起消失，
// 站长就只能靠猜——而这正是这张表存在的唯一理由。
func TestChannelProbeLogRepo_删除渠道后历史仍可读(t *testing.T) {
	st := newTestStore(t)
	repo := NewChannelProbeLogRepository(st.DB())
	ctx := context.Background()

	appendProbeLog(t, repo, 42, time.Now(), false, 0)

	// 库里并不存在 id=42 的渠道（也没有外键约束），历史仍应可读
	got, err := repo.List(ctx, model.ChannelProbeLogQuery{ChannelID: 42})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("渠道不存在时历史仍应可读，实际读到 %d 条", len(got))
	}
	if got[0].ChannelID != 42 {
		t.Fatalf("渠道 ID 应为 42，实际 %d", got[0].ChannelID)
	}
}
