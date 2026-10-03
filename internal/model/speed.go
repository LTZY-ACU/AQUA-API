// 本文件定义「模型测速结果」的领域模型与仓储接口。
//
// 意图（Why）：
//
//	测速结果是"渠道 × 模型"维度的运行状态快照：广场据此向用户展示模型延迟，
//	后台据此对比各渠道快慢。它是运行数据而不是配置（不会被人手工维护），
//	因此模型层只定义形状与约束，写入全部来自测速动作本身。
//
// 流转（Flow）：
//
//	internal/relay/speedtest.go 产出 SpeedProbeResult
//	  └─ server.handleSpeedTestChannel → ModelSpeedRepository.Upsert（落库）
//	       └─ 模型广场 / 后台测速页 → Latest（读各渠道最新结果）
//
// 扩展（Extend）：
//
//	需要历史趋势（近 N 天延迟曲线）时另建追加表，不要改本接口——
//	"最新结果"与"历史序列"是两个读模型，混在一起会让两侧都变复杂。
package model

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 领域错误定义。业务层通过 errors.Is 判断，而不是比较错误字符串。
var (
	// ErrSpeedResultInvalid 表示测速结果缺少必要字段（渠道 ID 或模型名）。
	ErrSpeedResultInvalid = errors.New("model: 测速结果缺少渠道或模型")
)

// ModelSpeedResult 是一次「渠道 × 模型」测速的结果快照。
//
// 语义：每个 (ChannelID, Model) 组合只保留最近一次结果（仓储按唯一键 UPSERT）。
type ModelSpeedResult struct {
	// ChannelID 是被测渠道的主键。
	ChannelID uint64
	// Model 是对外模型名（平台模型 ID）；UpstreamModel 是实际发给上游的名字。
	//
	// 两者分开存的原因：测速失败（如 404）时，管理员第一件要确认的就是
	// "上游到底收到了哪个名字"——映射没生效与上游真没有该模型，处置完全不同。
	Model         string
	UpstreamModel string
	// OK 表示本次测速是否成功（2xx 且拿到了首字）。
	OK bool
	// StatusCode 是上游 HTTP 状态码；0 表示请求未发出（网络错误）。
	StatusCode int
	// TTFBMS 是首字延迟（毫秒），测速的主指标；失败时为 0。
	TTFBMS int
	// TotalMS 是本次测速总耗时（毫秒）。
	TotalMS int
	// Message 是给人看的结论或失败原因（已截断）。
	Message string
	// TestedAt 是测速发生时间。
	TestedAt time.Time
}

// NoPermission 判断该结果是否为「上游明确拒绝此模型」。
//
// 只认两个状态码（刻意不引入 net/http，保持 model 层不感知 HTTP 细节）：
//   - 403：账号无权访问该模型（套餐未包含 / 分组未授权）；
//   - 404：上游不存在该模型。
//
// 它们是【确定性】失败——重试不会变好，因此可以作为"测速后自动屏蔽"的依据。
// 其余失败（超时、429、5xx、网络错误）是暂时性的，绝不构成屏蔽理由：
// 一次抖动就移除健康模型，比留着无权限模型的代价更大。
func (r ModelSpeedResult) NoPermission() bool {
	return !r.OK && (r.StatusCode == 403 || r.StatusCode == 404)
}

// Normalize 清洗并校验一条测速结果，供仓储写入前调用。
//
// 导出（而非仓储私有）的原因：校验规则属于领域层——无论从哪条路径写入
// （接口、脚本、未来的定时任务）都应遵守同一套规则，与 Channel.Validate 同理。
func (r *ModelSpeedResult) Normalize() error {
	r.Model = strings.TrimSpace(r.Model)
	r.UpstreamModel = strings.TrimSpace(r.UpstreamModel)
	// message 截断到 500 字符（按字符不按字节，避免中文尾部乱码）：
	// 它来自上游响应片段，超长会撑爆后台表格与日志。
	runes := []rune(r.Message)
	if len(runes) > 500 {
		r.Message = string(runes[:500])
	}
	if r.ChannelID == 0 || r.Model == "" {
		return ErrSpeedResultInvalid
	}
	if r.TTFBMS < 0 || r.TotalMS < 0 {
		return ErrSpeedResultInvalid
	}
	return nil
}

// ModelSpeedRepository 定义模型测速结果的持久化操作。
type ModelSpeedRepository interface {
	// Upsert 写入一条测速结果：同一 (channel_id, model) 只保留最近一次。
	Upsert(ctx context.Context, result *ModelSpeedResult) error

	// Latest 返回全部「渠道 × 模型」的最近一次测速结果。
	//
	// 为什么一次全量读：表的行数上界 = 渠道数 × 每渠道模型数（千级），
	// 广场与后台各自在内存里聚合即可，没有必要为两种读法各建一套查询。
	Latest(ctx context.Context) ([]*ModelSpeedResult, error)

	// DeleteByChannel 删除某渠道的全部测速结果（渠道删除时清理孤儿数据）。
	DeleteByChannel(ctx context.Context, channelID uint64) error
}
