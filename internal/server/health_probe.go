// 本文件实现渠道健康巡检：后台周期性对每个启用中的渠道发一次最小请求，
// 把"还在不在、快不快、哪个模型撑住了"写回渠道行——这就是「自动获取延迟更新」。
//
// 意图（Why）：
//
//	在它之前，渠道的 last_test_at/last_test_ok 只有管理员在后台手动点「测活」
//	才会更新，于是系统里根本没有"这个渠道现在多少毫秒"这个事实：
//	  · 管理员看到的延迟是上一次手点的（可能是三天前），无法据此判断现状；
//	  · 路由选择拿不到延迟依据，只能按优先级/权重挑，避不开已经变慢的渠道；
//	  · 上游"变慢"往往是宕机的前兆，没人盯着后台就发现不了，只能等用户报障。
//	巡检把这件事变成后台持续在做的日常，管理员任何时候打开后台看到的都是新鲜值。
//
// 设计取舍（为什么是这些默认值与形状）：
//   - 每个渠道只探测到一个可用模型为止：巡检的目的是"渠道可用吗、多快"，
//     不是"把清单里 30 个模型全验一遍"（那是管理员点测活时的职责，也确实那样做）；
//   - 单渠道超时远小于转发链路：挂死的上游不该拖住整轮巡检；
//   - 并发默认 2：既不把巡检变成一次针对上游的并发压测，也不让 30 个渠道串行跑几分钟；
//   - 写只用 RecordProbeResult（部分列更新）：巡检读到的渠道副本正在被读写竞争，
//     整行写回会用陈旧副本覆盖管理员刚保存的配置。
//   - 历史单独追加、失败只记日志：渠道行的当前值与时间线是两件事，
//     前者写失败会让卡片显示旧值（必须报错），后者写失败只影响趋势图（不能拖累前者）。
//
// 流转（Flow）：
//
//	cmd/aqua/main.go → go srv.StartHealthProbe(ctx, logger)
//	  → 每 interval 一轮 ProbeAllChannels
//	      → 列出启用渠道 → 并发 N → probeOneChannel（复用人工测活同一条探测链路）
//	      → Channels.RecordProbeResult（当前值）→ ChannelProbeLogs.Append（历史一行）
//	      → 只在健康状态翻转时告警
//
// 扩展（Extend）：
//
//	想让路由按延迟挑渠道：在 relay 的候选排序里读 ch.LatencyMS（已有），本文件无需改动；
//	想加"连续 N 次失败自动禁用"：在 probeChannel 之后根据 LastTestOK 的连续计数决定即可。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/notify"
)

// healthProbeConfig 是巡检运行时的已归一配置。
//
// 独立归一（而不是每次直接读 config）的目的：主协程可能在进程运行中被替换配置吗？目前不会，
// 但巡检是长期运行的协程，把参数在服务启动时算定并锁死，
// 能避免"跑一半周期变了"这种难以复现的行为。
type healthProbeConfig struct {
	Enabled     bool
	Interval    time.Duration
	Concurrency int
	Timeout     time.Duration
}

// healthProbeConfigFrom 把配置项转成运行参数，非正值一律回退到默认值。
func healthProbeConfigFrom(cfg config.HealthConfig) healthProbeConfig {
	c := healthProbeConfig{
		Enabled:     cfg.Enabled,
		Interval:    time.Duration(cfg.IntervalMinutes) * time.Minute,
		Concurrency: cfg.Concurrency,
		Timeout:     time.Duration(cfg.TimeoutSeconds) * time.Second,
	}
	if c.Interval <= 0 {
		c.Interval = time.Duration(config.DefaultHealthCheckIntervalMinutes) * time.Minute
	}
	if c.Concurrency <= 0 {
		c.Concurrency = config.DefaultHealthCheckConcurrency
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Duration(config.DefaultHealthCheckTimeoutSeconds) * time.Second
	}
	return c
}

// StartHealthProbe 启动渠道健康巡检协程，随 ctx 取消而结束。
//
// 刻意先跑一轮再进入周期等待：进程刚启动时所有渠道的延迟都是旧的，
// 若等到第一个周期之后才更新，"重启后后台显示的延迟是上一任进程的"会持续整整一个周期。
func (s *Server) StartHealthProbe(ctx context.Context, logger *slog.Logger) {
	c := healthProbeConfigFrom(s.deps.Config.Health)
	if !c.Enabled {
		logger.Info("渠道健康巡检已按配置关闭（AQUA_HEALTH_ENABLED=false），延迟不会自动刷新")
		return
	}
	if s.deps.Relay == nil {
		logger.Warn("转发引擎未就绪，跳过渠道健康巡检")
		return
	}

	if n := s.ProbeAllChannels(ctx, c); n >= 0 {
		logger.Info("已完成首轮渠道健康巡检", "channels", n, "interval", c.Interval.String())
	}

	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := s.ProbeAllChannels(ctx, c); n >= 0 {
				logger.Debug("渠道健康巡检完成一轮", "channels", n)
			}
		}
	}
}

// ProbeAllChannels 对所有启用中的渠道跑一轮巡检，返回参与的渠道数。
//
// 返回 -1 表示本轮未能开始（例如列渠道失败），调用方据此跳过"已完成"日志：
// 一轮没跑成就不该在日志里留下"巡检完成"这种误导性结论。
//
// 被设计为导出方法的原因：管理员未来可在后台点"立刻巡检一次"复用它，
// 那条路径与周期巡检必须是同一份实现。
func (s *Server) ProbeAllChannels(ctx context.Context, c healthProbeConfig) int {
	if s.deps.Relay == nil {
		return -1
	}

	enabled := model.ChannelStatusEnabled
	channels, err := s.deps.Channels.List(ctx, model.ChannelQuery{
		Status: &enabled,
		Limit:  healthProbeMaxChannels,
	})
	if err != nil {
		slog.Error("列出待巡检渠道失败（本轮巡检跳过）", "error", err)
		return -1
	}

	// 无模型的渠道跳过：探测它没有意义（无法确定发给上游哪个名字），
	// 这类渠道在转发时同样无法被选中。
	pending := make([]*model.Channel, 0, len(channels))
	for _, ch := range channels {
		if ch == nil || len(ch.Models) == 0 {
			continue
		}
		pending = append(pending, ch)
	}
	if len(pending) == 0 {
		return 0
	}

	workers := c.Concurrency
	if workers > len(pending) {
		workers = len(pending)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	queue := make(chan *model.Channel)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ch := range queue {
				// 每个渠道独立超时： ctx 是整个进程的生命周期，
				// 若把超时加在它上面，第一个慢渠道会把后面所有渠道的等待一起吃掉。
				chCtx, cancel := context.WithTimeout(ctx, c.Timeout)
				s.probeOneChannel(chCtx, ch)
				cancel()

				mu.Lock()
				done++
				mu.Unlock()
			}
		}()
	}
	for _, ch := range pending {
		queue <- ch
	}
	close(queue)
	wg.Wait()

	return done
}

// probeOneChannel 巡检单个渠道：探测一次 → 落库 → 必要时告警。
func (s *Server) probeOneChannel(ctx context.Context, ch *model.Channel) {
	// 复用"管理员点测活"的那条链路：两者的结论必须可比，
	// 否则后台会出现"巡检说通、手动点一下说不讲理"的自相矛盾。
	result := s.probeChannel(ctx, ch)

	// 上一次的可用状态用于识别翻转（只在这时候告警，避免每 15 分钟刷一条失败日志）。
	wasOK := ch.LastTestOK

	record := model.ChannelProbeResult{
		ID:         ch.ID,
		At:         time.Now(),
		OK:         result.OK,
		LatencyMS:  result.LatencyMS,
		StatusCode: result.StatusCode,
		Model:      result.Model,
	}
	if err := s.deps.Channels.RecordProbeResult(ctx, record); err != nil {
		slog.Error("记录渠道巡检结果失败", "error", err, "channel_id", ch.ID, "channel", ch.Name)
		return
	}

	// 历史留痕：与上面的"当前值"是两次独立写入，且刻意不因失败而 return。
	//
	// 为什么不共用一次事务：渠道行的当前值是巡检的产出（丢了会显示旧延迟），
	// 历史行只是趋势分析的原料（丢了只是曲线上少一个点）。
	// 把它们绑在一个事务里，等于让"趋势数据暂不可写"也能拖垮"当前延迟不更新"——
	// 用一条本可丢失的数据换一个必然丢失的展示，是明确的坏交易。
	if s.deps.ChannelProbeLogs != nil {
		histErr := s.deps.ChannelProbeLogs.Append(ctx, &model.ChannelProbeLog{
			ChannelID:  ch.ID,
			At:         record.At,
			OK:         result.OK,
			LatencyMS:  result.LatencyMS,
			StatusCode: result.StatusCode,
			Model:      result.Model,
			Message:    result.Message,
		})
		if histErr != nil {
			// 记 Warn 而不 Error：它不影响本轮探测的结论，只影响趋势完整度。
			slog.Warn("追加渠道探针历史失败（本轮延迟已记录，趋势图将缺一个点）",
				"error", histErr, "channel_id", ch.ID, "channel", ch.Name)
		}
	}

	switch {
	case result.OK && !wasOK:
		slog.Info("渠道巡检恢复可用", "channel_id", ch.ID, "channel", ch.Name,
			"latency_ms", result.LatencyMS, "model", result.Model)
		s.deps.Notifier.Alert(notify.Alert{
			Key:      model.EventChannelRecovered,
			Level:    notify.LevelInfo,
			Title:    fmt.Sprintf("渠道「%s」已恢复可用", ch.Name),
			Detail:   "此前不可用的渠道在最近一次巡检中探测成功，流量可正常承接。",
			DedupKey: fmt.Sprintf("channel/%d", ch.ID),
			Fields: []notify.Field{
				{Label: "渠道", Value: fmt.Sprintf("%s（#%d）", ch.Name, ch.ID)},
				{Label: "本次耗时", Value: fmt.Sprintf("%d ms", result.LatencyMS)},
				{Label: "探测模型", Value: result.Model},
			},
		})
	case !result.OK && wasOK:
		// 由好转坏必须显式告警：这是"用户马上要开始报错"的最早信号，
		// 比等用户来投诉便宜得多。
		slog.Warn("渠道巡检由可用转为不可用", "channel_id", ch.ID, "channel", ch.Name,
			"status_code", result.StatusCode, "message", result.Message)
		s.deps.Notifier.Alert(notify.Alert{
			Key:      model.EventChannelUnhealthy,
			Level:    notify.LevelWarning,
			Title:    fmt.Sprintf("渠道「%s」探测失败", ch.Name),
			Detail:   probeAlertDetail(result),
			DedupKey: fmt.Sprintf("channel/%d", ch.ID),
			Fields: []notify.Field{
				{Label: "渠道", Value: fmt.Sprintf("%s（#%d）", ch.Name, ch.ID)},
				{Label: "上游状态码", Value: probeAlertStatus(result)},
				{Label: "失败说明", Value: notify.ShortTitle(result.Message, 120)},
			},
		})
	case !result.OK:
		slog.Warn("渠道巡检仍不可用", "channel_id", ch.ID, "channel", ch.Name,
			"status_code", result.StatusCode, "message", result.Message)
		// 持续不可用【不】重复外发：告警的目标是"第一次知道"，
		// 每 15 分钟发一次只会训练站长忽略通知。去重窗口由 notify 统一控制。
	}
}

// probeAlertStatus 把探测结果的状态码转成人能看懂的一句。
//
// 状态码为 0 说明请求根本没拿到响应（DNS/连接/超时），
// 这时给一个空状态码会让站长误以为是上游返回了 0。
func probeAlertStatus(result channelTestResponse) string {
	if result.StatusCode == 0 {
		return "未拿到响应（连接失败或超时）"
	}
	return fmt.Sprintf("HTTP %d", result.StatusCode)
}

// probeAlertDetail 给出可据以行动的处置提示。
func probeAlertDetail(result channelTestResponse) string {
	switch {
	case result.StatusCode == 401 || result.StatusCode == 403:
		return "上游拒绝了凭据，请检查渠道密钥是否失效或已被撤销。"
	case result.StatusCode == 404:
		return "上游返回「模型不存在」，请检查渠道里的模型名是否与上游一致。"
	case result.StatusCode == 429:
		return "上游触发了限流，请降低调用频率或为该渠道补充密钥。"
	case result.StatusCode >= 500:
		return "上游返回服务端错误，通常是对方故障，稍后巡检会自动重试。"
	case result.StatusCode > 0:
		return "上游返回了非预期的状态码，建议手动点一次「测活」查看完整响应。"
	default:
		return "请求未能送达上游，请检查渠道地址、网络与 DNS。"
	}
}

// healthProbeMaxChannels 是单轮巡检的渠道数量上限。
//
// 取 500：自托管场景的渠道量远小于此；设置上限是为了让"极端情况下列表查到几千行"
// 不会让巡检变成一次全表扫描 + 几百个并发请求。超出部分本轮不巡检，下轮也不会轮到，
// 属于明确的取舍（宁可少巡检，也不能把自己变成上游的一次流量事故）。
const healthProbeMaxChannels = 500
