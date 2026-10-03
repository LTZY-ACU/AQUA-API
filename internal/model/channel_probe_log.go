// 本文件定义「渠道探针历史」的领域模型与仓储接口。
//
// 意图（Why）：
//
//	渠道行（channels）上只存着"最近一次测活"的当前值。这能回答
//	"这个渠道现在多少毫秒"，但回答不了站长真正会问的第二个问题：
//	"它是什么时候开始变慢的、昨晚还好好的吗"。
//	没有历史就只能看到一次采样恰好落在哪，于是：
//	  · 上游从 300ms 劣化到 3s，页面上仍是 300ms（劣化这件事被完全抹平）；
//	  · 偶发的 5s 尖峰被下一次正常值覆盖，事后无从复盘；
//	  · 值班交接时无法判断"这个抖动是新问题还是老毛病"。
//	本文件把"每次探测的结论"作为一行历史来记，让劣化从猜测变成可查的时间线。
//
// 设计取舍（为什么这样切分）：
//   - 独立表而不是给渠道行加 JSON 字段：历史要按时间范围查、要聚合、
//     要被保留期清理，这些都不是"渠道的一个属性"能承载的；
//   - 独立仓储而不是挂在 ChannelRepository 上：历史只写不读改，
//     与渠道 CRUD 的生命周期完全不同，混在一个接口里会让所有测试替身被迫实现无用方法；
//   - 不做预聚合汇总表：行数量级（渠道数 × 每天几十到近百轮）小到可以在索引上直接聚合，
//     而预计算表一旦与明细口径漂移，排查"看板和实际不符"比慢查询痛苦得多。
//
// 流转（Flow）：
//
//	巡检 probeOneChannel
//	  → Channels.RecordProbeResult（更新渠道行的当前值）
//	  → ChannelProbeLogs.Append（追加一行历史）
//	后台看板 → ChannelProbeLogs.List（时间线/曲线） + 成功率聚合
//	保留期清理 → Retention.Purge 按天数删除历史行
//
// 扩展（Extend）：
//	要按小时降采样：给查询加 (channel_id, at) 索引后 GROUP BY 区间聚合，
//	          达到百万行级别再考虑落汇总表，届时明细表保留期可相应缩短。
package model

import (
	"context"
	"time"
)

// ChannelProbeLog 是一次渠道探测的历史留痕（只写不读改）。
//
// 为什么它与 ChannelProbeResult 分成两个类型而不是复用：
//
//	ChannelProbeResult 是"一次动作的输入参数"——要写进渠道行的当前值，
//	主键是渠道 ID，语义是覆盖；
//	ChannelProbeLog 是"已经发生过的事实"——要追加进时间线，
//	没有渠道 ID 之外的唯一标识，语义是追加。
//
//	把两者合并成一个类型，会迫使写入方在同一个结构体上同时表达
//	"覆盖渠道行的这一列"和"追加历史的一整行"，而这两件事的字段子集并不相同
//	（历史额外要记 message 摘要，渠道行不记）。
type ChannelProbeLog struct {
	// ID 是自增主键，仅用于分页与去重，不承载业务含义。
	ID uint64
	// ChannelID 是被探测的渠道。
	ChannelID uint64
	// At 是探测时刻；默认用探测发起时刻而非完成时刻，
	// 避免慢探测（如 30s 超时）在时间线上被整体右移。
	At time.Time
	// OK 表示本次是否探测通过（至少一个模型返回 2xx）。
	OK bool
	// LatencyMS 是本次耗时（毫秒）。
	//
	// 注意：失败时这个值可能 > 0（收到了响应，只是非 2xx），
	// 也可能 = 0（连响应都没拿到）。因此不能用"LatencyMS > 0"反推是否成功，
	// 判定必须看 OK。
	LatencyMS int
	// StatusCode 是上游 HTTP 状态码；0 = 网络层失败（DNS/连接/超时）。
	StatusCode int
	// Model 是实际探测命中的模型名。
	Model string
	// Message 是失败原因摘要（成功时为空串）。
	//
	// 刻意存摘要而非上游响应全文：失败时上游常回一整页 HTML 错误页，
	// 全文入库会让这张最频繁写入的表迅速膨胀，而摘要已足够生成处置提示。
	Message string
}

// ChannelProbeLogQuery 是探针历史的时间线查询条件。
//
// 为什么按"时间范围 + 渠道"而不是纯分页：
//	探针历史的天然阅读方式就是"最近这段时间"，而不是"第 N 页"；
//	而没有时间下界的查询在保留期被误设为 0（永不清理）时会退化成全表扫描。
//	因此把时间范围设为必填，让"忘记限定范围"变成结构上不可能的事。
type ChannelProbeLogQuery struct {
	// ChannelID 为 0 表示不限渠道（查全站时间线）。
	ChannelID uint64
	// Since 是时间下界（含）；零值表示不设下界。
	Since time.Time
	// Until 是时间上界（含）；零值表示不设上界。
	Until time.Time
	// OnlyFailures 为 true 时只返回失败的行（看板"最近故障"列表用它）。
	OnlyFailures bool
	// Limit 是返回条数上限；<= 0 时由仓储回退到默认值。
	Limit int
}

// ChannelProbeLogRepository 是探针历史的仓储接口（只写 + 读时间线）。
type ChannelProbeLogRepository interface {
	// Append 追加一条探测历史。
	//
	// 与 RecordProbeResult 分开而不是合成一次写入：两者写的是不同的表，
	// 且失败语义不同——渠道行的当前值必须写成功（否则卡片显示旧值），
	// 历史行写失败只影响趋势分析，不该让探测结论丢失。
	// 调用方须分别处理两个错误。
	Append(ctx context.Context, log *ChannelProbeLog) error

	// List 按条件返回时间线，固定按「时间倒序」返回（最近的在前）。
	List(ctx context.Context, q ChannelProbeLogQuery) ([]*ChannelProbeLog, error)

	// Count 返回符合条件的行数，用于看板显示"共 N 次探测"。
	Count(ctx context.Context, q ChannelProbeLogQuery) (int64, error)
}
