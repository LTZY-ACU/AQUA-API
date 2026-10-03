// 本文件实现「渠道健康看板」接口：成功率快照 + 探针历史时间线。
//
// 意图（Why）：
//
//	渠道健康在后台此前是三块互不相干的数据：
//	  · 渠道列表里的「延迟」列——只有最近一次，且是"现在多少毫秒"；
//	  · 自动停用任务在日志里留的成功率——只写日志，页面上看不到；
//	  · 巡检告警——只在翻转的那一刻发一条通知，事后再也查不到。
//	站长要回答的却是连续的问题："哪些渠道正在变差""昨晚那次抖动是哪条渠道"。
//	本文件把这三块拼成一个可查的面板：一次请求同时给出
//	「各渠道成功率 + 状态分布」与「探针时间线」，让判断有据可依。
//
// 设计取舍（为什么这么切）：
//   - 成功率口径复用 channelHealthStats（走 usage_logs 的真实调用）：
//     探针成功率只反映"巡检探针自己"，而真实流量才是业务后果。
//     两个口径都摆出来（probe_* 与 traffic_*），差别本身就是信息——
//     探针全通但真实调用大量失败，说明问题在密钥权限之外的地方；
//   - 时间线不预聚合：行数量级（渠道数 × 每天几十到近百轮）在索引上直接查即可，
//     预计算表一旦与明细口径漂移，"看板和实际不符"比慢查询难查得多；
//   - 窗口由查询参数给定并做上限收敛：看板的时间范围是展示需求而非数据边界，
//     允许前端随意传 10 年会让一次页面刷新变成全表扫描。
//
// 流转（Flow）：
//
//	GET /admin/channels/health  → 成功率快照（真实流量）+ 探针成功率 + 状态分布
//	GET /admin/channels/:id/probes → 单渠道探针时间线（曲线数据源）
//	前端 ChannelsHealthBoard 渲染表格与 ECharts 折线
//
// 扩展（Extend）：
//	要按小时降采样：给 List 加区间聚合参数（见 channel_probe_log_repo.go 的 Extend）；
//	要跨渠道对比曲线：把明细接口的 channel_id 允许为 0（全站）即可，无需改结构。
package server

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// 看板时间窗口的默认值与上限。
const (
	// channelHealthDefaultWindowHours 是成功率统计的默认窗口（小时）。
	//
	// 取 24 而非 15 分钟：巡检间隔本身是 15 分钟级，
	// 窗口比巡检周期只大一点的话，样本量太小、曲线全是锯齿。
	channelHealthDefaultWindowHours = 24
	// channelHealthMaxWindowHours 是窗口上限，防止"一次刷新拉全表"。
	channelHealthMaxWindowHours = 24 * 30
	// channelProbeDefaultLimit 是时间线默认返回条数。
	channelProbeDefaultLimit = 200
)

// probeAggregateScanLimit 是聚合探针统计时一次最多读回的行数。
//
// 取 2000：默认 24 小时窗口下，30 个渠道 × 96 轮/天 = 2880 行，
// 2000 略低于理论上限，因此这个上限同时也是一个"静默截断"的提示信号——
// 覆盖的渠道越多、窗口越长，越可能触顶。触顶不影响看板可用性：
// 探针口径只是辅助信息，被截断时成功率仍取自 usage_logs 的真实流量。
// 之所以仍要设限：没有上限时"把窗口改成 30 天"会一次拉回 10 万行，
// 那才是真正会让进程内存抖动的操作。
const probeAggregateScanLimit = 2000

// channelHealthItemDTO 是单个渠道在看板上的健康快照。
type channelHealthItemDTO struct {
	ChannelID uint64 `json:"channel_id"`
	Name      string `json:"name"`
	// Status 是渠道状态的原始数值（与 channels.status 一一对应）。
	//
	// 刻意用数值而不是字符串：model.ChannelStatus 是 int，
	// 而 string(ChannelStatus) 会被 Go 转成单个 rune（"\x01"）而不是 "1"，
	// 那样前端拿到的状态就是不可见的控制字符。前端按数值与状态标签文案匹配。
	Status int `json:"status"`
	// StatusLabel 是状态的中文说明，供前端直接展示而不必在前端复刻枚举表。
	StatusLabel string `json:"status_label"`
	// Group 是渠道分组，看板上按分组聚合展示更易读。
	Group string `json:"group"`

	// ── 真实流量口径（来自 usage_logs）──
	// Requests 为窗口内经由该渠道的请求数；为 0 表示"窗口内没有流量"。
	Requests    int64   `json:"requests"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`

	// ── 探针口径（来自 channel_probe_logs）──
	// ProbeTotal/ProbeOK/ProbeFailed 是窗口内巡检次数与成败数。
	// 与上面分开摆而不合并成一个"成功率"：两者量纲不同（真实请求 vs 巡针采样），
	// 合成一个数字会让人误读成"成功率掉了"其实只是巡检多跑了几轮。
	ProbeTotal  int64   `json:"probe_total"`
	ProbeOK     int64   `json:"probe_ok"`
	ProbeFailed int64   `json:"probe_failed"`
	ProbeRate   float64 `json:"probe_rate"`

	// LatencyMS 是最近一次探针的耗时（渠道行的当前值）。
	LatencyMS int `json:"latency_ms"`
	// LastTestAt 是最近一次探针时刻；零值表示从未探针。
	LastTestAt int64 `json:"last_test_at"`
	// LastTestCode 是最近一次探针的上游状态码；0 = 未探到或网络层失败。
	LastTestCode int `json:"last_test_code"`
	// Healthy 是一次性给出的"当前是否健康"结论，供前端标色。
	//
	// 判定口径刻意保守（三个条件全满足才算健康）：任一条件不满足都返回 false，
	// 让"标绿"只出现在真的没问题的渠道上。宁可漏标，不可错标——
	// 错标成绿色的渠道会被站长忽略，那比不标更危险。
	Healthy bool `json:"healthy"`
}

// channelHealthSummaryDTO 是看板顶部的汇总。
type channelHealthSummaryDTO struct {
	Total         int `json:"total"`
	Enabled       int `json:"enabled"`
	Disabled      int `json:"disabled"`
	AutoDisabled  int `json:"auto_disabled"`
	HealthyCount  int `json:"healthy_count"`
	UnhealthyCnt  int `json:"unhealthy_count"`
	WindowMinutes int `json:"window_minutes"`
	// ProbeHistoryEnabled 为 false 表示未注入探针历史仓储，
	// 前端据此隐藏趋势图并给出说明，而不是显示一片空白让人以为"没数据"。
	ProbeHistoryEnabled bool `json:"probe_history_enabled"`
}

// channelHealthResponse 是 GET /admin/channels/health 的响应体。
type channelHealthResponse struct {
	Summary channelHealthSummaryDTO `json:"summary"`
	Items   []channelHealthItemDTO   `json:"items"`
	// WindowStart/WindowEnd 供前端在图上标注"统计区间"，
	// 避免用户把"这段时间的成功率"误解为"全时段成功率"。
	WindowStart int64 `json:"window_start"`
	WindowEnd   int64 `json:"window_end"`
}

// handleChannelHealth 返回渠道健康看板数据。
//
// GET /api/admin/channels/health?window_hours=24
func (s *Server) handleChannelHealth(c *gin.Context) {
	ctx := c.Request.Context()

	window := time.Duration(queryIntDefault(c, "window_hours", channelHealthDefaultWindowHours)) * time.Hour
	if window <= 0 {
		window = channelHealthDefaultWindowHours * time.Hour
	}
	if maxWindow := channelHealthMaxWindowHours * time.Hour; window > maxWindow {
		// 静默收敛而不是报错：站长在 URL 里手改参数时，页面上什么都不显示
		// 只会让人以为是 bug。收敛后至少能看到"这是上限内的数据"。
		window = maxWindow
	}

	// 全部渠道（不限状态）：看板必须能看到已停用的渠道——
	// "它为什么被停用"正是站在停用渠道旁边才能看清的。
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{Limit: nameLookupLimit})
	if err != nil {
		s.respondInternalError(c, "查询渠道列表失败", err)
		return
	}

	since := time.Now().Add(-window)

	// 真实流量口径：复用自动停用任务的那份统计（同一口径，不另造一套数字）。
	trafficStats, err := s.channelHealthStats(ctx, window)
	if err != nil {
		s.respondInternalError(c, "统计渠道调用成功率失败", err)
		return
	}
	trafficByID := make(map[uint64]channelHealthStat, len(trafficStats))
	for _, st := range trafficStats {
		trafficByID[st.ChannelID] = st
	}

	// 探针口径：一次查全站窗口内的历史，再在内存里按渠道归并。
	//
	// 刻意不逐渠道查询：那会是 N+1（30 个渠道 = 30 次查询），
	// 而这张表走 (channel_id, at) 索引，全站一次查回的行数就是同一批数据。
	probeByID := map[uint64]*probeAggregate{}
	probeHistoryEnabled := s.deps.ChannelProbeLogs != nil
	if probeHistoryEnabled {
		logs, err := s.deps.ChannelProbeLogs.List(ctx, model.ChannelProbeLogQuery{
			Since: since,
			Limit: probeAggregateScanLimit,
		})
		if err != nil {
			// 探针历史不可用不该让整个看板 500：成功率（真实流量）才是主信息，
			// 趋势图只是增强。降级为"没有趋势"继续返回。
			slog.Warn("读取渠道探针历史失败，看板将不含趋势数据", "error", err)
			probeHistoryEnabled = false
		} else {
			probeByID = aggregateProbes(logs)
		}
	}

	resp := channelHealthResponse{
		Items:       make([]channelHealthItemDTO, 0, len(channels)),
		WindowStart: since.Unix(),
		WindowEnd:   time.Now().Unix(),
	}

	for _, ch := range channels {
		item := channelHealthItemDTO{
			ChannelID:    ch.ID,
			Name:         ch.Name,
			Status:       int(ch.Status),
			StatusLabel:  channelStatusLabel(ch.Status),
			Group:        ch.Group,
			LatencyMS:    ch.LatencyMS,
			LastTestCode: ch.LastTestCode,
		}
		if !ch.LastTestAt.IsZero() {
			item.LastTestAt = ch.LastTestAt.Unix()
		}

		// 真实流量：只统计启用中的渠道（channelHealthStats 的既定口径），
		// 已停用渠道拿不到流量数字，留 0 比填个假数字诚实。
		if st, ok := trafficByID[ch.ID]; ok {
			item.Requests = st.Requests
			item.Success = st.Success
			item.Failed = st.Failed
			item.SuccessRate = st.SuccessRate
		}

		// 探针统计：停用渠道不再被巡检，窗口内通常没有新行，
		// 但仍保留渠道行上的"最近一次"延迟与时刻（那是停用前最后一 valuable 信息）。
		if agg, ok := probeByID[ch.ID]; ok {
			item.ProbeTotal = agg.total
			item.ProbeOK = agg.ok
			item.ProbeFailed = agg.total - agg.ok
			item.ProbeRate = agg.rate()
		}

		item.Healthy = channelIsHealthy(item)
		resp.Items = append(resp.Items, item)

		resp.Summary.Total++
		switch ch.Status {
		case model.ChannelStatusEnabled:
			resp.Summary.Enabled++
		case model.ChannelStatusAutoDisabled:
			resp.Summary.AutoDisabled++
		default:
			resp.Summary.Disabled++
		}
		if item.Healthy {
			resp.Summary.HealthyCount++
		} else {
			resp.Summary.UnhealthyCnt++
		}
	}

	resp.Summary.WindowMinutes = int(window.Minutes())
	resp.Summary.ProbeHistoryEnabled = probeHistoryEnabled

	c.JSON(http.StatusOK, resp)
}

// channelProbePointDTO 是时间线上的一个点。
type channelProbePointDTO struct {
	At         int64  `json:"at"`
	OK         bool   `json:"ok"`
	LatencyMS  int    `json:"latency_ms"`
	StatusCode int    `json:"status_code"`
	Model      string `json:"model"`
	Message    string `json:"message"`
}

// channelProbeTimelineDTO 是 GET /api/admin/channels/:id/probes 的响应体。
type channelProbeTimelineDTO struct {
	ChannelID uint64                 `json:"channel_id"`
	Name      string                 `json:"name"`
	Points    []channelProbePointDTO `json:"points"`
	// Total 是窗口内探针总次数（不受 limit 截断影响），让前端能显示"共 N 次，显示 M 次"。
	Total int64 `json:"total"`
	// Truncated 为 true 表示 Total > len(Points)，
	// 前端据此提示"仅显示最近 N 次"，避免把截断后的数据误读成全貌。
	Truncated bool `json:"truncated"`
	// LatencyStats 是窗口内成功探针的延迟分位数，给出"当前到底算快还是慢"的参照。
	LatencyStats probeLatencyStatsDTO `json:"latency_stats"`
}

// probeLatencyStatsDTO 描述窗口内延迟的分布。
//
// 为什么给分位数而不是只有平均值：延迟分布通常是长尾的，
// 平均 300ms 可能意味着"多数 200ms + 偶发 5s 尖峰"，而后者在体感上就是"有时候很卡"。
// 只有一个平均值时，这种卡顿在图表上完全看不出来。
type probeLatencyStatsDTO struct {
	Count int   `json:"count"`
	Min   int   `json:"min"`
	Max   int   `json:"max"`
	Avg   int   `json:"avg"`
	P50   int   `json:"p50"`
	P95   int   `json:"p95"`
	Valid bool  `json:"valid"`
}

// handleChannelProbeTimeline 返回单个渠道的探针历史时间线。
//
// GET /api/admin/channels/:id/probes?window_hours=24&limit=200
func (s *Server) handleChannelProbeTimeline(c *gin.Context) {
	ctx := c.Request.Context()

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		oai.WriteError(c.Writer, http.StatusBadRequest, "渠道 ID 不合法", oai.TypeInvalidRequest, oai.CodeInvalidJSON)
		return
	}

	ch, err := s.deps.Channels.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrChannelNotFound) {
			oai.WriteError(c.Writer, http.StatusNotFound, "渠道不存在", oai.TypeInvalidRequest, oai.CodeInternal)
			return
		}
		s.respondInternalError(c, "载入渠道失败", err)
		return
	}

	// 探针历史未注入：明确回 501 而不是回一个空列表。
	//
	// 理由：空列表在前端表现为"这个渠道从来没有探针记录"，
	// 而真实原因是"这个部署没开探针历史"。两者含义完全不同，
	// 让站长去查一个并不存在的问题是最差的体验。
	if s.deps.ChannelProbeLogs == nil {
		oai.WriteError(c.Writer, http.StatusNotImplemented,
			"探针历史未启用", oai.TypeServer, oai.CodeInternal)
		return
	}

	window := time.Duration(queryIntDefault(c, "window_hours", channelHealthDefaultWindowHours)) * time.Hour
	if window <= 0 {
		window = channelHealthDefaultWindowHours * time.Hour
	}
	if maxWindow := channelHealthMaxWindowHours * time.Hour; window > maxWindow {
		window = maxWindow
	}

	query := model.ChannelProbeLogQuery{
		ChannelID: ch.ID,
		Since:     time.Now().Add(-window),
		Limit:     queryIntDefault(c, "limit", channelProbeDefaultLimit),
	}
	logs, err := s.deps.ChannelProbeLogs.List(ctx, query)
	if err != nil {
		s.respondInternalError(c, "查询渠道探针历史失败", err)
		return
	}
	total, err := s.deps.ChannelProbeLogs.Count(ctx, model.ChannelProbeLogQuery{
		ChannelID: ch.ID,
		Since:     query.Since,
	})
	if err != nil {
		// 计数只影响"是否提示截断"，失败不该让已经取到的时间线作废。
		slog.Warn("统计渠道探针历史数失败，截断提示将不可用", "error", err, "channel_id", ch.ID)
	}

	points := make([]channelProbePointDTO, 0, len(logs))
	for _, l := range logs {
		points = append(points, channelProbePointDTO{
			At:         l.At.Unix(),
			OK:         l.OK,
			LatencyMS:  l.LatencyMS,
			StatusCode: l.StatusCode,
			Model:      l.Model,
			Message:    l.Message,
		})
	}

	// 时间线是倒序返回的（最近的在前，符合 SQL 侧约定），
	// 而延迟分位数与图表都按时间正序更自然，故先在统计里排一次正序。
	returned := make([]*model.ChannelProbeLog, 0, len(logs))
	for i := len(logs) - 1; i >= 0; i-- {
		returned = append(returned, logs[i])
	}

	c.JSON(http.StatusOK, channelProbeTimelineDTO{
		ChannelID:    ch.ID,
		Name:         ch.Name,
		Points:       points,
		Total:        total,
		Truncated:    total > int64(len(points)),
		LatencyStats: computeProbeLatencyStats(returned),
	})
}

// ---------------------------------------------------------------------------
// 纯函数辅助（便于单测，不依赖任何仓储）
// ---------------------------------------------------------------------------

// probeAggregate 是某渠道在窗口内的探针成败计数。
type probeAggregate struct {
	total int64
	ok    int64
}

// rate 返回探针成功率；无探针时返回 0。
//
// 无样本时返回 0 而不是 1：把"没数据"显示成 100% 成功，
// 会让看板出现一条绿色的"完美渠道"卡片，而它其实从未被探过。
func (a *probeAggregate) rate() float64 {
	if a == nil || a.total <= 0 {
		return 0
	}
	return float64(a.ok) / float64(a.total)
}

// aggregateProbes 把探针历史按渠道归并成成败计数。
func aggregateProbes(logs []*model.ChannelProbeLog) map[uint64]*probeAggregate {
	out := make(map[uint64]*probeAggregate, len(logs))
	for _, l := range logs {
		if l == nil {
			continue
		}
		agg, ok := out[l.ChannelID]
		if !ok {
			agg = &probeAggregate{}
			out[l.ChannelID] = agg
		}
		agg.total++
		if l.OK {
			agg.ok++
		}
	}
	return out
}

// channelIsHealthy 判定渠道在看板上的"当前是否健康"。
//
// 三条全满足才算健康（保守判定，宁可漏标不可错标）：
//  1. 状态为「启用」——已停用的渠道不需要被判为"不健康"，
//     它已经是"不可用"这个更准确的状态了，混为一谈会让停用渠道看起来像"正在变坏"；
//  2. 探针有样本且成功率为 100%——无样本不算健康（没采到数据不等于没问题），
//     但也不因此判定"有故障"，两者在看板上由 LatencyMS/ProbeTotal 自行呈现；
//  3. 最近一次探针有延迟记录（>0）——LatencyMS=0 意味着从未拿到过响应。
//
// 真实流量的成功率刻意【不】纳入判定：
// 它是窗口内的滚动值，会因为"半小时内没流量"而变成噪声，
// 而探针是定时固定采样的，更适合作为"此刻状态"的依据。
func channelIsHealthy(item channelHealthItemDTO) bool {
	if item.Status != int(model.ChannelStatusEnabled) {
		return false
	}
	if item.ProbeTotal <= 0 || item.ProbeOK != item.ProbeTotal {
		return false
	}
	return item.LatencyMS > 0
}

// channelStatusLabel 返回渠道状态的中文名。
//
// 复用 model.ChannelStatus.String() 而不在前端复刻一份映射表：
// 状态枚举在 Go 侧改动时（如将来加"限额停用"），
// 后端文案会立刻跟上，而前端那份副本会静默退化成"未知"。
func channelStatusLabel(s model.ChannelStatus) string {
	return s.String()
}

// queryIntDefault 读取整型查询参数，缺省或非法时返回 def。
//
// 为什么不报错：这些参数只影响"看多少数据"，不是业务开关。
// 前端拼错一个值就让整个看板 400，用户看到的是一片空白且不知原因；
// 回退到默认窗口至少还能看到内容。真正的边界（上限）在调用处另行收敛。
func queryIntDefault(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// computeProbeLatencyStats 计算窗口内【成功】探针的延迟分布。
//
// 为什么只统计成功的：失败探针的耗时语义完全不同——
// 401 是"上游立刻拒绝"（耗时极短），504 是"上游超时"（耗时接近超时上限）。
// 把它们混进延迟分布，P95 会稳定地贴在超时上限上，
// 让人以为"正常请求也这么慢"，而实际上只是失败在慢。
func computeProbeLatencyStats(logs []*model.ChannelProbeLog) probeLatencyStatsDTO {
	var values []int
	for _, l := range logs {
		if l == nil || !l.OK || l.LatencyMS <= 0 {
			continue
		}
		values = append(values, l.LatencyMS)
	}
	if len(values) == 0 {
		// Valid=false 让前端区分"没有成功样本"与"延迟为 0"——
		// 前者该显示"暂无数据"，后者该显示一个真实的 0。
		return probeLatencyStatsDTO{}
	}

	sort.Ints(values)
	sum := 0
	for _, v := range values {
		sum += v
	}
	return probeLatencyStatsDTO{
		Count: len(values),
		Min:   values[0],
		Max:   values[len(values)-1],
		Avg:   sum / len(values),
		P50:   percentileOfSorted(values, 0.50),
		P95:   percentileOfSorted(values, 0.95),
		Valid: true,
	}
}

// percentileOfSorted 在已升序排序的切片上取分位数（最近秩法）。
//
// 为什么用最近秩而不是插值：延迟是离散毫秒，且样本量通常不大（几十到几百）。
// 插值会造出一个"从未真实测到过"的中间值（如 347ms），
// 让人无法把页面上任何一个数字对回某次具体探测；最近秩返回的一定是真实观测值。
//
// 输入必须已排序且非空（空切片返回 0，调用方需先判 Valid）。
func percentileOfSorted(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// idx 用浮点算再取整，等价于 ceil(p*n) - 1，且 p=1 时正好落在末位。
	idx := int(float64(len(sorted))*p + 0.9999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
