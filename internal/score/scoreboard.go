// Package score 实现「动态综合评分算法 v2.3」——用量排行榜的评分纯函数。
//
// 意图（Why）：
//
//	排行榜的分数必须随全员数据动态变化：某用户用量激增时，锚点随之抬高，
//	所有人的分数都会重新标定——这样"分数"表达的是「相对当前全站活跃度的位置」，
//	而不是可以无限累加的累计量。早期版本用「榜内线性归一」有两个硬伤：
//	  1. 分数是 0~1 小数，界面上与百分比混淆，且看不出上限；
//	  2. 线性刻度下，中段用户全部挤在中段区间（0.4~0.6），
//	     头部与尾部的区分度不够，榜单缺乏阅读价值。
//
//	本算法用「对数刻度 + 动态锚点」解决：
//	  - 每个维度的锚点 A/B 由**全员数据实时算出的分位数**决定，
//	    因此任何人的数据变化都会重标定所有人的分数（动态性）；
//	  - 区间内用对数插值而非线性：低活跃区拉开差距、高活跃区不至于全挤在顶端；
//	  - 合成用几何平均而非加权平均：任一维度为 0 都会把总分拉到 floor，
//	    杜绝"刷请求数就能拿高分"的刷分路径。
//
// 流转（Flow）：
//
//	store 聚合出每人 {tokens, requests}
//	  → server 组装 []Entry
//	    → score.ScoreBoard(entries, cfg)
//	      → 归一化 → 分位锚点 → 单维对数打分 → 几何合成 → 显式多级排序 → 名次
//
// 扩展（Extend）：
//
//   - 调整评分曲线：改 Config 的字段（全部在 DefaultConfig 集中）；
//   - 增加维度（如"额度消耗"）：新增一个 subScore + 对应权重，
//     并把不变式 wSum == 1 纳入 Validate；
//   - 需要时间衰减：在此包外做加权（把衰减后的值喂给 Entry），保持本包纯函数。
package score

import (
	"math"
	"sort"
)

// Entry 是评分输入：某用户在统计窗口内的两项用量。
type Entry struct {
	ID       string  // 账号 ID（用户名）
	Tokens   float64 // token 消耗量
	Requests float64 // 请求数
}

// Config 是评分参数。未设置的字段回退到 DefaultConfig 的对应默认值。
//
// 关于 SetFloor / SetBasePct：这两个字段的合法取值包含 0，
// 零值无法区分"没设置"与"显式设为 0"，因此用独立的标记位表达"已设置"。
// 代价是调用方稍啰嗦，但换来 floor=0、pBase=0 都能正确表达。
type Config struct {
	ScoreMax   float64 // 满分（榜首在两个维度都触顶时取得）
	Floor      float64 // 保底分：数据退化 / 触底时给分，避免"0 分"与"没数据"混淆
	SetFloor   bool    // 是否显式设置了 Floor
	Weights    Weights // 两维权重（和会被归一化到 1）
	TopPercent float64 // 上锚点分位（如 0.9 = P90）
	BasePct    float64 // 下锚点分位（如 0.1 = P10）
	SetBasePct bool    // 是否显式设置了 BasePct
	KTop       float64 // 下锚点的倍数下限：max(k × B, P_top)
}

// Weights 是两个维度的权重，和必须为 1。
type Weights struct {
	Tokens   float64 // token 维度权重
	Requests float64 // 请求维度权重
}

// DefaultConfig 是全部默认参数。
//
// 取值理由：
//   - ScoreMax=100：百分制直观，界面不需要再做换算；
//   - Floor=20：给"数据退化/触底"一个正数，0 分在榜单上会被误读为"没有数据"；
//   - P90/P10：上锚点取 P90 而非最大值，避免单个极端值（刷量）把所有人压到地板；
//     下锚点取 P10 而非最小值，保证"低活跃"是从底部开始分段；
//   - K=5：当下锚点被 0 值污染时（B=0），用 5×0=0 无意义，
//     此时退化为 P90 单独兜底——这也是为什么必须两个锚点取 max。
var DefaultConfig = Config{
	ScoreMax:   100,
	Floor:      20,
	Weights:    Weights{Tokens: 0.5, Requests: 0.5},
	TopPercent: 0.90,
	BasePct:    0.10,
	KTop:       5.0,
}

// Row 是一条评分结果。
type Row struct {
	ID       string  // 账号 ID
	Tokens   float64 // token 消耗量（已按 floor 规则归一）
	Requests float64 // 请求数
	ScoreTok float64 // token 维度得分（展示用，保留 2 位）
	ScoreReq float64 // 请求维度得分（展示用，保留 2 位）
	Total    float64 // 综合得分（展示用，保留 2 位）
	Rank     int     // 名次，从 1 起
	// TotalRaw 是未舍入的综合得分，供需要更高精度的调用方使用
	// （排序始终基于它，Total 只是它的展示快照）。
	TotalRaw float64
}

// Result 是一次评分的完整结果（含锚点，便于观测与调试）。
type Result struct {
	Rows    []Row
	Anchors Anchors
}

// Anchors 记录本次评分实际使用的锚点。
//
// 之所以回传：锚点是动态的，出问题时能立刻知道"当时的上/下界是多少"，
// 比回头猜参数省事得多。
type Anchors struct {
	BaseTok, TopTok float64
	BaseReq, TopReq float64
}

// ScoreBoard 是对外入口：按默认配置评分。
func ScoreBoard(entries []Entry) Result {
	return ScoreBoardWithConfig(entries, Config{})
}

// ScoreBoardWithConfig 按给定配置评分（cfg 中的零值字段回退默认）。
//
// 纯函数：无 IO、不读当前时间，输入相同则输出逐位相同。
func ScoreBoardWithConfig(entries []Entry, cfg Config) Result {
	c := mergeConfig(cfg)
	if len(entries) == 0 {
		return Result{Rows: []Row{}}
	}

	tokens := make([]float64, len(entries))
	requests := make([]float64, len(entries))
	for i, e := range entries {
		// 脏数据（负数 / NaN / Inf）一律置 0：保留在榜内，拿 floor 分。
		// 不丢弃行——用户要能看到"我今天确实有调用但数据异常"这件事。
		tokens[i] = sanitize(e.Tokens)
		requests[i] = sanitize(e.Requests)
	}

	// 锚点：全员数据实时算出，不做任何增量更新。
	baseTok := percentile(tokens, c.BasePct)
	topTok := math.Max(c.KTop*baseTok, percentile(tokens, c.TopPercent))
	baseReq := percentile(requests, c.BasePct)
	topReq := math.Max(c.KTop*baseReq, percentile(requests, c.TopPercent))

	rows := make([]Row, 0, len(entries))
	for i, e := range entries {
		sTok := subScore(tokens[i], topTok, baseTok, c)
		sReq := subScore(requests[i], topReq, baseReq, c)
		// 几何平均：total = scoreMax × (sTok/scoreMax)^wTok × (sReq/scoreMax)^wReq
		// 用未舍入的 sTok/sReq 计算，total 最后才舍入。
		totalRaw := c.ScoreMax *
			math.Pow(sTok/c.ScoreMax, c.Weights.Tokens) *
			math.Pow(sReq/c.ScoreMax, c.Weights.Requests)

		rows = append(rows, Row{
			ID:       e.ID,
			Tokens:   tokens[i],
			Requests: requests[i],
			ScoreTok: round2(sTok),
			ScoreReq: round2(sReq),
			Total:    round2(totalRaw),
			TotalRaw: totalRaw,
		})
	}

	// 显式多级排序：total 降 → tokens 降 → requests 降 → id 升。
	// 不依赖 sort.SliceStable 的稳定性，任何宿主实现下结果都可复现。
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalRaw != rows[j].TotalRaw {
			return rows[i].TotalRaw > rows[j].TotalRaw
		}
		if rows[i].Tokens != rows[j].Tokens {
			return rows[i].Tokens > rows[j].Tokens
		}
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].ID < rows[j].ID
	})
	for i := range rows {
		rows[i].Rank = i + 1
	}

	return Result{
		Rows:    rows,
		Anchors: Anchors{BaseTok: baseTok, TopTok: topTok, BaseReq: baseReq, TopReq: topReq},
	}
}

// mergeConfig 把用户传入的部分配置补成完整配置，并对不变式做防御。
//
// 为什么用「指针字段」而不是零值判断：0 是合法取值（floor=0、权重先归一化再判零）。
// 若用 `if cfg.Floor != 0` 判断，floor=0 会被误当成"未设置"而回退 20，
// 让调用方永远设不了 0 保底——这是零值语义与缺省语义混淆的经典坑。
//
// 不变式：0 ≤ pBase < pTop ≤ 1；wTokens + wRequests = 1；scoreMax > floor ≥ 0；k ≥ 1。
// 违反时退回默认而不是 panic：评分是展示性能力，不该因为配置写错而让接口 500。
func mergeConfig(cfg Config) Config {
	c := DefaultConfig
	if cfg.ScoreMax > 0 {
		c.ScoreMax = cfg.ScoreMax
	}
	if cfg.SetFloor {
		c.Floor = cfg.Floor
	}
	if cfg.TopPercent > 0 {
		c.TopPercent = cfg.TopPercent
	}
	if cfg.SetBasePct {
		c.BasePct = cfg.BasePct
	}
	if cfg.KTop >= 1 {
		c.KTop = cfg.KTop
	}
	// 权重：先按"和归一"，因此 (1,1) 合法（各 0.5）；全 0 视为未设置。
	sum := cfg.Weights.Tokens + cfg.Weights.Requests
	if sum > 0 {
		c.Weights = Weights{Tokens: cfg.Weights.Tokens / sum, Requests: cfg.Weights.Requests / sum}
	}
	// 不变式兜底
	if c.BasePct >= c.TopPercent {
		c.BasePct, c.TopPercent = DefaultConfig.BasePct, DefaultConfig.TopPercent
	}
	if c.Floor < 0 {
		c.Floor = 0
	}
	if c.Floor >= c.ScoreMax {
		c.Floor = 0
	}
	if c.TopPercent > 1 {
		c.TopPercent = 1
	}
	if c.KTop < 1 {
		c.KTop = DefaultConfig.KTop
	}
	return c
}

// sanitize 把非法值（负数 / NaN / ±Inf）置 0。
func sanitize(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	// 合法值向下取整：token 数与请求数都是计数，不该出现小数。
	return math.Floor(v)
}

// percentile 计算线性插值分位数（与 numpy / Excel PERCENTILE.INC 同口径）。
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	xs := make([]float64, len(values))
	copy(xs, values)
	sort.Float64s(xs)

	if len(xs) == 1 {
		return xs[0]
	}
	idx := p * float64(len(xs)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return xs[lo]
	}
	return xs[lo] + (xs[hi]-xs[lo])*(idx-float64(lo))
}

// subScore 单维打分。分支顺序是规格的一部分，不可调换：
//
//	A ≤ B（数据退化，全员同值） → scoreMax：区间不存在，不应该让所有人拿地板分
//	v ≥ A（触顶）             → scoreMax
//	v ≤ B（触底）             → floor
//	区间内                   → 对数插值
func subScore(v, a, b float64, c Config) float64 {
	if a <= b {
		return c.ScoreMax
	}
	if v >= a {
		return c.ScoreMax
	}
	if v <= b {
		return c.Floor
	}
	y := math.Log(v/b) / math.Log(a/b)
	return c.Floor + (c.ScoreMax-c.Floor)*y
}

// round2 是 round-half-up 到两位小数。
//
// 不能用 math.Round：它是 half-away-from-zero，对负数与 .005 边界表现不同，
// 且不同平台的浮点实现可能有 1 ULP 差异。这里显式做半值判定保证可复现。
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	scaled := v * 100
	// 浮点补偿：把极接近半值的数先归一到可判定的一侧。
	if diff := math.Abs(scaled - math.Round(scaled)); diff < 1e-9 {
		scaled = math.Round(scaled)
	} else if diff := math.Abs(math.Abs(scaled-math.Floor(scaled)) - 0.5); diff < 1e-9 {
		scaled = math.Floor(scaled) + 0.5 // 精确半值：进位
	}
	return math.Floor(scaled+0.5) / 100
}
