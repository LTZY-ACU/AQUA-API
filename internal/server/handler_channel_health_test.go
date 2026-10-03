// 本文件是渠道健康看板纯函数的单元测试。
//
// 意图（Why）：
//
//	看板的每个数字都会影响站长的处置决策（停不停用、要不要换密钥、
//	先修哪条渠道）。判定的偏差不会报错，只会让人照着错结论行动，
//	因此这里逐条钉住判定口径，尤其是三类"看起来对其实错"的情形：
//	  · 无探针样本时标成健康（"没数据"被当成"没问题"）；
//	  · 失败探针的耗时混进延迟分位数（P95 永远贴着超时上限，
//	    于是"偶尔很卡"被解释成"一直很慢"）；
//	  - 已停用渠道被标成"不健康"（站长会去修一个自己关掉的渠道）。
//
// 流转（Flow）：
//
//	go test ./internal/server/ → 直调纯函数 → 断言判定与统计口径
//	（不碰仓储与 HTTP：这些函数刻意无副作用，可单测；
//	  handler 的接线由路由核对测试覆盖）
//
// 扩展（Extend）：
//	调整健康判定口径时，请同步更新本文件与 handler_channel_health.go 的注释——
//	两处口径必须一致，注释是唯一能提醒后来者"为什么这么判"的地方。
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// TestChannelIsHealthy 钉住健康判定的三条边界。
func TestChannelIsHealthy(t *testing.T) {
	cases := []struct {
		name string
		item channelHealthItemDTO
		want bool
		why  string
	}{
		{
			name: "启用+全探针通过+有延迟=健康",
			item: channelHealthItemDTO{
				Status:    int(model.ChannelStatusEnabled),
				ProbeTotal: 8, ProbeOK: 8, LatencyMS: 220,
			},
			want: true,
			why:  "这是唯一该标绿的情形",
		},
		{
			name: "无探针样本=不健康",
			item: channelHealthItemDTO{
				Status: int(model.ChannelStatusEnabled), ProbeTotal: 0, LatencyMS: 220,
			},
			want: false,
			why:  "没采到数据不等于没问题；标绿会让从未被探过的渠道显示为完美",
		},
		{
			name: "有失败探针=不健康",
			item: channelHealthItemDTO{
				Status:    int(model.ChannelStatusEnabled),
				ProbeTotal: 8, ProbeOK: 7, LatencyMS: 220,
			},
			want: false,
			why:  "一轮失败就足以让站长想知道，不该被平均掉",
		},
		{
			name: "延迟为0=不健康",
			item: channelHealthItemDTO{
				Status:    int(model.ChannelStatusEnabled),
				ProbeTotal: 8, ProbeOK: 8, LatencyMS: 0,
			},
			want: false,
			why:  "LatencyMS=0 意味着从未拿到过响应，与「探针全通」自相矛盾",
		},
		{
			name: "手动停用=不健康（且不标「变坏」）",
			item: channelHealthItemDTO{
				Status:    int(model.ChannelStatusDisabled),
				ProbeTotal: 8, ProbeOK: 8, LatencyMS: 220,
			},
			want: false,
			why:  "停用是管理员的主动决定，看板不该把它渲染成「正在变坏」",
		},
		{
			name: "自动停用=不健康",
			item: channelHealthItemDTO{
				Status:    int(model.ChannelStatusAutoDisabled),
				ProbeTotal: 0, LatencyMS: 0,
			},
			want: false,
			why:  "自动停用后不再被巡检，无样本；结论仍是不健康",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := channelIsHealthy(tc.item); got != tc.want {
				t.Fatalf("channelIsHealthy=%v，期望 %v（%s）", got, tc.want, tc.why)
			}
		})
	}
}

// TestAggregateProbes 验证按渠道归并与空值防护。
func TestAggregateProbes(t *testing.T) {
	base := time.Now()
	logs := []*model.ChannelProbeLog{
		{ChannelID: 1, OK: true, At: base},
		{ChannelID: 1, OK: false, At: base},
		{ChannelID: 1, OK: true, At: base},
		{ChannelID: 2, OK: false, At: base},
		nil, // 脏数据：List 不该返回，但聚合层要能扛住
	}

	got := aggregateProbes(logs)

	agg1, ok := got[1]
	if !ok {
		t.Fatal("渠道 1 的聚合结果缺失")
	}
	if agg1.total != 3 || agg1.ok != 2 {
		t.Fatalf("渠道 1 应为 total=3 ok=2，实际 total=%d ok=%d", agg1.total, agg1.ok)
	}
	if r := agg1.rate(); r < 0.66 || r > 0.67 {
		t.Fatalf("渠道 1 探针成功率=%v，期望约 0.667", r)
	}

	agg2, ok := got[2]
	if !ok {
		t.Fatal("渠道 2 的聚合结果缺失")
	}
	if agg2.total != 1 || agg2.ok != 0 || agg2.rate() != 0 {
		t.Fatalf("渠道 2 应为 total=1 ok=0 rate=0，实际 %+v", agg2)
	}
}

// TestProbeAggregate_Rate_无样本不报100 验证"没数据"不会显示成"完美"。
//
// 这是最容易出错的一处：空样本除零若被写成 0/0 的默认值 1，
// 看板上会出现一条"成功率 100%"的绿色卡片，而它其实从未被探过。
func TestProbeAggregate_Rate_无样本不报100(t *testing.T) {
	var nilAgg *probeAggregate
	if nilAgg.rate() != 0 {
		t.Fatal("nil 聚合的 rate 必须为 0")
	}
	if (&probeAggregate{total: 0}).rate() != 0 {
		t.Fatal("零样本的 rate 必须为 0，不能是 1")
	}
	if (&probeAggregate{total: 4, ok: 4}).rate() != 1 {
		t.Fatal("全通过的 rate 应为 1")
	}
}

// TestComputeProbeLatencyStats_只统计成功 验证失败耗时被排除。
//
// 这是本文件最关键的一条断言。若把失败耗时混进来，
// 一条 504 超时（耗时贴近超时上限）就会把 P95 拉到上限，
// 让人以为"正常请求也这么慢"，而实际上只是失败在慢。
func TestComputeProbeLatencyStats_只统计成功(t *testing.T) {
	logs := []*model.ChannelProbeLog{
		{OK: true, LatencyMS: 100},
		{OK: true, LatencyMS: 200},
		{OK: true, LatencyMS: 300},
		{OK: true, LatencyMS: 400},
		// 失败的超时：耗时贴近超时上限，绝不能污染分布
		{OK: false, LatencyMS: 30000},
		{OK: false, LatencyMS: 0},
	}

	got := computeProbeLatencyStats(logs)

	if !got.Valid {
		t.Fatal("有 4 个成功样本时 Valid 应为 true")
	}
	if got.Count != 4 {
		t.Fatalf("Count=%d，期望 4（失败样本不得计入）", got.Count)
	}
	if got.Min != 100 || got.Max != 400 || got.Avg != 250 {
		t.Fatalf("min/max/avg=%d/%d/%d，期望 100/400/250", got.Min, got.Max, got.Avg)
	}
	if got.P95 != 400 {
		t.Fatalf("P95=%d，期望 400（若把 30000ms 的失败算进来这里会变成 30000）", got.P95)
	}
}

// TestComputeProbeLatencyStats_无有效样本 验证 Valid=false 而非返回零值。
func TestComputeProbeLatencyStats_无有效样本(t *testing.T) {
	cases := []struct {
		name string
		logs []*model.ChannelProbeLog
		why  string
	}{
		{"空切片", nil, "没有任何探测"},
		{"只有失败", []*model.ChannelProbeLog{{OK: false, LatencyMS: 500}}, "全挂了，成功样本为零"},
		{"成功但耗时为0", []*model.ChannelProbeLog{{OK: true, LatencyMS: 0}}, "连通但没拿到可测的耗时不参与统计"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeProbeLatencyStats(tc.logs)
			if got.Valid {
				t.Fatal("无有效样本时 Valid 必须为 false，让前端显示「暂无数据」而不是「0 ms」")
			}
			if got.Count != 0 {
				t.Fatalf("Count=%d，期望 0", got.Count)
			}
		})
	}
}

// TestPercentileOfSorted 钉住最近秩分位数的取法。
func TestPercentileOfSorted(t *testing.T) {
	// 1..10
	values := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	cases := []struct {
		name string
		p    float64
		want int
	}{
		{"P50 取第 5 个", 0.50, 5},
		{"P95 取第 10 个", 0.95, 10},
		{"P0 取最小", 0, 1},
		{"P1 取最大", 1, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentileOfSorted(values, tc.p); got != tc.want {
				t.Fatalf("percentileOfSorted(p=%v)=%d，期望 %d", tc.p, got, tc.want)
			}
		})
	}

	// 越界防护：p 为负或大于 1 时不得 panic，也不得越界
	if got := percentileOfSorted(values, -0.5); got != 1 {
		t.Fatalf("负分位数应回退到最小值，实际 %d", got)
	}
	if got := percentileOfSorted(values, 2.0); got != 10 {
		t.Fatalf("超界分位数应回退到最大值，实际 %d", got)
	}
	if got := percentileOfSorted(nil, 0.5); got != 0 {
		t.Fatalf("空切片应返回 0，实际 %d", got)
	}
}

// TestPercentileOfSorted_单样本 验证 n=1 时不越界。
//
// 单样本是极常见的情形（新部署只有一个渠道、刚跑完第一轮巡检）。
// 若按 ceil(p*n)-1 算，n=1、p=0.5 会得到索引 0——正确；
// 但写成 ceil(p*(n-1)) 就会得到 -1 而 panic。这个用例守住它。
func TestPercentileOfSorted_单样本(t *testing.T) {
	for _, p := range []float64{0, 0.5, 0.95, 1} {
		if got := percentileOfSorted([]int{42}, p); got != 42 {
			t.Fatalf("单样本 p=%v 时应恒返回 42，实际 %d", p, got)
		}
	}
}

// TestQueryIntDefault_非法值回退默认 验证窗口参数被前端拼错时不至于让看板空白。
//
// 值得单测的原因：这些参数只影响"看多少数据"，不是业务开关。
// 前端漏传/拼错一个值就 400 的话，用户看到的是一片空白且无从判断原因。
func TestQueryIntDefault_非法值回退默认(t *testing.T) {
	cases := []struct {
		name string
		qs   map[string]string
		key  string
		def  int
		want int
	}{
		{"参数缺失用默认", nil, "window_hours", 24, 24},
		{"空串用默认", map[string]string{"window_hours": ""}, "window_hours", 24, 24},
		{"非数字用默认", map[string]string{"window_hours": "abc"}, "window_hours", 24, 24},
		{"合法值透传", map[string]string{"window_hours": "48"}, "window_hours", 24, 48},
		{"负数原样返回（由调用处归一）", map[string]string{"window_hours": "-1"}, "window_hours", 24, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			for k, v := range tc.qs {
				c.Request = httptest.NewRequest(http.MethodGet, "/?"+k+"="+v, nil)
			}
			// 缺省分支需要一个非 nil 的 Request，gin 的 Query 在其上才安全
			if c.Request == nil {
				c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			}
			if got := queryIntDefault(c, tc.key, tc.def); got != tc.want {
				t.Fatalf("queryIntDefault(%q)=%d，期望 %d", tc.key, got, tc.want)
			}
		})
	}
}
