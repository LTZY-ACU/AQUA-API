/**
 * 渠道健康看板接口。
 *
 * 意图（Why）：
 *   渠道健康此前散在三处：渠道列表的「延迟」列（只有最近一次）、
 *   自动停用任务写进日志的成功率（页面上看不到）、巡检告警（只在翻转那一刻发一条）。
 *   站长要回答的却是连续的问题——"哪些渠道正在变差""昨晚那次抖动是哪条渠道"。
 *   本文件对应的页面把三处拼成一页，让判断有据可依。
 *
 * 两个口径刻意分开摆（Why 这条最容易被人"顺手合并掉"）：
 *   traffic_* 来自 usage_logs 的真实调用，反映业务后果；
 *   probe_*   来自巡检采样，反映渠道自身的可达性。
 *   量纲不同（真实请求 vs 定时采样），合成一个数字会让人把
 *   "巡检多跑了几轮"误读成"成功率掉了"。两者有差别时，那个差别本身就是信息：
 *   探针全通但真实调用大量失败，说明问题不在密钥能否被接受这一层。
 *
 * 流转（Flow）：
 *   ChannelHealthPage → fetchChannelHealth / fetchChannelProbeTimeline
 *                      → /api/admin/channels/health、/api/admin/channels/{id}/probes
 *
 * 扩展（Extend）：
 *   后端新增聚合维度时在本文件补类型与函数；页面只依赖这里的类型，不依赖字段拼装。
 */
import { api } from './client'

/** 渠道状态（与后端 model.ChannelStatus 数值一一对应）。 */
export const CHANNEL_STATUS = {
  /** 启用：可参与路由 */
  ENABLED: 1,
  /** 手动禁用：管理员主动关闭，健康检查不会自动恢复 */
  DISABLED: 2,
  /** 自动禁用：熔断/健康检查判定不可用后关闭 */
  AUTO_DISABLED: 3,
} as const

/** 单个渠道的健康快照。 */
export interface ChannelHealthItem {
  channel_id: number
  name: string
  /** 状态数值，见 CHANNEL_STATUS */
  status: number
  /** 状态中文名，由后端下发（前端不自行复刻枚举表） */
  status_label: string
  group: string

  // ── 真实流量口径（usage_logs）──
  requests: number
  success: number
  failed: number
  /** 0~1；窗口内无流量时为 0 */
  success_rate: number

  // ── 探针口径（channel_probe_logs）──
  probe_total: number
  probe_ok: number
  probe_failed: number
  /** 0~1；无样本时为 0（绝不显示成 100%） */
  probe_rate: number

  /** 最近一次探针耗时（毫秒）；0 = 从未拿到过响应 */
  latency_ms: number
  /** 最近一次探针时刻（Unix 秒）；0 = 从未探针 */
  last_test_at: number
  /** 最近一次探针的上游状态码；0 = 网络层失败或从未探针 */
  last_test_code: number
  /** 后端给出的"当前是否健康"结论（保守判定：宁漏标不错标） */
  healthy: boolean
}

/** 看板顶部汇总。 */
export interface ChannelHealthSummary {
  total: number
  enabled: number
  disabled: number
  auto_disabled: number
  healthy_count: number
  unhealthy_count: number
  window_minutes: number
  /**
   * 探针历史是否可用。
   *
   * 为 false 时页面必须隐藏趋势图并说明原因，
   * 绝不能显示成"没有探测记录"——那会让站长去查一个并不存在的问题。
   */
  probe_history_enabled: boolean
}

/** GET /api/admin/channels/health 的响应体。 */
export interface ChannelHealthResult {
  summary: ChannelHealthSummary
  items: ChannelHealthItem[]
  /** 统计区间起止（Unix 秒），供图上标注，避免被误读为全时段数据 */
  window_start: number
  window_end: number
}

/** 时间线上的一个探测点。 */
export interface ChannelProbePoint {
  at: number
  ok: boolean
  latency_ms: number
  /** 上游状态码；0 = 网络层失败 */
  status_code: number
  model: string
  /** 失败原因摘要（成功时为空串） */
  message: string
}

/** 窗口内成功探针的延迟分布。 */
export interface ProbeLatencyStats {
  count: number
  min: number
  max: number
  avg: number
  p50: number
  p95: number
  /** 为 false 时上面所有数字都无意义，页面应显示「暂无数据」而不是 0 */
  valid: boolean
}

/** GET /api/admin/channels/{id}/probes 的响应体。 */
export interface ChannelProbeTimeline {
  channel_id: number
  name: string
  /** 按时间倒序（最近的在前） */
  points: ChannelProbePoint[]
  /** 窗口内探针总次数，不受 limit 截断影响 */
  total: number
  /** 为 true 表示只列了最近 N 次，页面应显式提示 */
  truncated: boolean
  latency_stats: ProbeLatencyStats
}

/**
 * GET /api/admin/channels/health：读取渠道健康看板。
 *
 * @param windowHours 统计窗口（小时）。后端会收敛到上限（720）并对非法值回退默认，
 *                    所以这里不必在前端做同样校验。
 */
export function fetchChannelHealth(windowHours = 24): Promise<ChannelHealthResult> {
  return api.get<ChannelHealthResult>('/admin/channels/health', { window_hours: windowHours })
}

/**
 * GET /api/admin/channels/{id}/probes：读取单渠道探针历史。
 *
 * @param channelId 渠道 ID
 * @param windowHours 时间窗口（小时）
 * @param limit 最多返回多少个点（后端上限 1000）
 */
export function fetchChannelProbeTimeline(
  channelId: number,
  windowHours = 24,
  limit = 200
): Promise<ChannelProbeTimeline> {
  return api.get<ChannelProbeTimeline>(`/admin/channels/${channelId}/probes`, {
    window_hours: windowHours,
    limit,
  })
}
