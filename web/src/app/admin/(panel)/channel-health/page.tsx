/** 管理后台：渠道健康看板（/admin/channel-health）。
 *
 * 意图（Why）：
 *   渠道健康此前散在三处，站长无法在一页里回答连续的问题：
 *     · 渠道列表的「延迟」列只有最近一次，看不出"它是什么时候开始变慢的"；
 *     · 成功率自动停用的判定只写进服务端日志，页面上完全看不到；
 *     · 巡检告警只在"由好转坏"那一刻发一条，之后就再也查不到了。
 *   本页把三处拼成一页：上面是成功率与延迟的汇总，下面是每个渠道的探针延迟曲线。
 *
 * 页面里几处刻意的取舍：
 *   1) 真实流量成功率与探针成功率【分两列摆开】，绝不合成一个数字。
 *      量纲不同（真实请求 vs 定时采样），合成后会把"巡检多跑了几轮"
 *      误读成"成功率掉了"。两者出现差距时，那个差距本身就是线索：
 *      探针全通但真实调用大量失败，说明问题不在"密钥能否被上游接受"这一层。
 *   2) 延迟曲线只画【成功】的探测点。失败探针的耗时语义完全不同：
 *      401 是"立刻被拒"（几毫秒），504 是"超时"（贴着超时上限）。
 *      混在一起会让 P95 稳定贴在上限上，把"偶尔很卡"显示成"一直很慢"。
 *   3) 探针历史不可用时【显式说明】而不是显示空图。
 *      空图会被读成"从没探过"，而真实原因是这个部署没开探针历史。
 *
 * 流转（Flow）：
 *   本页 → fetchChannelHealth（汇总 + 每渠道快照）
 *        → 选中某渠道 → fetchChannelProbeTimeline（该渠道时间线）→ EChart 折线
 *
 * 扩展（Extend）：
 *   后端加聚合维度时，本页只依赖 @/api/channelHealth 的类型，不依赖字段拼装。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import {
  CHANNEL_STATUS,
  fetchChannelHealth,
  fetchChannelProbeTimeline,
  type ChannelHealthItem,
  type ChannelHealthResult,
  type ChannelProbeTimeline,
} from '@/api/channelHealth'
import { Badge, EmptyState, Skeleton, StatCard } from '@/components/ui/Display'
import { EChart } from '@/components/ui/EChart'
import { DataTable, type Column } from '@/components/ui/Table'
import { useToast } from '@/lib/toast/toast-context'
import { isDarkScheme, useTheme } from '@/lib/theme/theme-context'
import { formatDateTime, formatNumber, formatPercent } from '@/utils/format'
import { areaGradient, chartStyles } from '@/utils/chart'

/** 可选统计窗口（小时）。刻意不给更细的档：探检周期是 15 分钟级，
 *  窗口比巡检周期只大一点的话样本太少，曲线全是锯齿。 */
const WINDOW_OPTIONS = [6, 24, 72, 168, 720] as const

/** 窗口标签：把小时数说成人话，避免站长去算 168 小时是几天。 */
const WINDOW_LABELS: Record<number, string> = {
  6: '6 小时',
  24: '24 小时',
  72: '3 天',
  168: '7 天',
  720: '30 天',
}

export default function AdminChannelHealthPage() {
  const { toastError } = useToast()
  const { resolved } = useTheme()

  const [windowHours, setWindowHours] = useState<number>(24)
  const [data, setData] = useState<ChannelHealthResult | null>(null)
  const [loading, setLoading] = useState(true)
  // 当前展开趋势图的渠道 id（0 = 未选中）。
  // 一次只画一条：多渠道同图会让人无法把某次尖峰对回具体渠道。
  const [selectedId, setSelectedId] = useState(0)
  const [timeline, setTimeline] = useState<ChannelProbeTimeline | null>(null)
  const [timelineLoading, setTimelineLoading] = useState(false)

  // ECharts 用 canvas，不认 CSS 变量，必须按 isDark 显式取色。
  // 用 isDarkScheme 而非 === 'dark'：深蓝也是暗色语义，漏判会让图表退化成浅色配色。
  const cs = useMemo(() => chartStyles(isDarkScheme(resolved)), [resolved])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await fetchChannelHealth(windowHours))
    } catch (err) {
      toastError(err instanceof Error ? err.message : '渠道健康数据加载失败')
    } finally {
      setLoading(false)
    }
  }, [windowHours, toastError])

  useEffect(() => {
    void load()
  }, [load])

  // 切换窗口后如果原选中渠道已不在列表里，清掉选中：
  // 保留一个不存在的 id 会让趋势区显示上一窗口的陈旧数据，
  // 看上去像"这个渠道还在但没数据了"，比直接清空更让人困惑。
  useEffect(() => {
    if (!data || selectedId === 0) return
    if (!data.items.some((i) => i.channel_id === selectedId)) {
      setSelectedId(0)
      setTimeline(null)
    }
  }, [data, selectedId])

  useEffect(() => {
    if (selectedId === 0) {
      setTimeline(null)
      return
    }
    let cancelled = false
    setTimelineLoading(true)
    fetchChannelProbeTimeline(selectedId, windowHours)
      .then((t) => {
        if (!cancelled) setTimeline(t)
      })
      .catch((err: unknown) => {
        if (!cancelled) toastError(err instanceof Error ? err.message : '探针历史加载失败')
      })
      .finally(() => {
        if (!cancelled) setTimelineLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, windowHours, toastError])

  const columns = useMemo<Column<ChannelHealthItem>[]>(
    () => [
      {
        title: '渠道',
        render: (row) => (
          <div className="min-w-0">
            <div className="truncate font-medium text-ink">{row.name}</div>
            {row.group ? <div className="truncate text-[11px] text-ink-3">{row.group}</div> : null}
          </div>
        ),
      },
      {
        title: '状态',
        width: 'w-24',
        render: (row) => (
          <Badge
            tone={
              row.status === CHANNEL_STATUS.ENABLED
                ? row.healthy
                  ? 'ok'
                  : 'warn'
                : row.status === CHANNEL_STATUS.AUTO_DISABLED
                  ? 'err'
                  : 'off'
            }
          >
            {row.status_label}
          </Badge>
        ),
      },
      {
        title: '延迟',
        align: 'right',
        width: 'w-28',
        render: (row) => (
          <span className="tabular-nums">
            {row.latency_ms > 0 ? `${formatNumber(row.latency_ms)} ms` : '—'}
          </span>
        ),
      },
      {
        title: '最近探针',
        align: 'right',
        width: 'w-32',
        render: (row) => (
          <span className="text-[12px] text-ink-3">
            {row.last_test_at > 0 ? formatRelativeProbe(row.last_test_at) : '从未探针'}
          </span>
        ),
      },
      {
        // 真实流量口径：来自调用日志，反映业务后果。
        title: '真实成功率',
        align: 'right',
        width: 'w-32',
        render: (row) =>
          row.requests > 0 ? (
            <span className={row.success_rate < 0.95 ? 'font-medium text-err' : 'tabular-nums'}>
              {formatPercent(row.success_rate)}
              <span className="ml-1 text-[11px] text-ink-3">({row.requests})</span>
            </span>
          ) : (
            <span className="text-ink-3">无流量</span>
          ),
      },
      {
        // 探针口径：来自巡检采样，反映渠道自身可达性。与上一列分开放，不合并。
        title: '探针成功率',
        align: 'right',
        width: 'w-32',
        render: (row) =>
          row.probe_total > 0 ? (
            <span className={row.probe_failed > 0 ? 'font-medium text-err' : 'tabular-nums'}>
              {formatPercent(row.probe_rate)}
              <span className="ml-1 text-[11px] text-ink-3">({row.probe_total})</span>
            </span>
          ) : (
            <span className="text-ink-3">无样本</span>
          ),
      },
    ],
    []
  )

  const latencyOption = useMemo(() => {
    const points = timeline?.points ?? []
    if (points.length === 0) return null
    // 接口按时间倒序返回（最近的在前），绘图需要正序，否则曲线从右往左画。
    const ordered = [...points].sort((a, b) => a.at - b.at)
    return {
      tooltip: {
        trigger: 'axis',
        ...cs.tooltip,
        // 自定义 formatter：失败点要显示状态码与原因，
        // 否则图上那个"掉到 0"的点会让人以为是"延迟变成 0 ms"。
        formatter: (params: unknown) => {
          const arr = params as { dataIndex: number }[]
          if (!Array.isArray(arr) || arr.length === 0) return ''
          const p = ordered[arr[0].dataIndex]
          if (!p) return ''
          const time = formatDateTime(p.at)
          if (p.ok) return `${time}<br/>延迟 ${formatNumber(p.latency_ms)} ms`
          const code = p.status_code > 0 ? `HTTP ${p.status_code}` : '未拿到响应'
          return `${time}<br/>失败（${code}）${p.message ? `<br/>${p.message}` : ''}`
        },
      },
      grid: { left: 8, right: 12, top: 16, bottom: 8, containLabel: true },
      xAxis: {
        type: 'category',
        data: ordered.map((p) => formatDateTime(p.at)),
        axisLabel: cs.axisLabel,
        axisLine: cs.axisLine,
      },
      yAxis: {
        type: 'value',
        name: '毫秒',
        nameTextStyle: cs.axisLabel,
        axisLabel: cs.axisLabel,
        splitLine: cs.splitLine,
      },
      series: [
        {
          name: '延迟',
          type: 'line',
          // connectNulls：失败点不连线但保留在同一序列里，
          // 这样 x 轴的时间刻度不会因为剔除失败而错位——错位会让"失败发生在何时"变得不可读。
          connectNulls: false,
          smooth: false,
          areaStyle: areaGradient(cs.palette[0]),
          itemStyle: { color: cs.palette[0] },
          data: ordered.map((p) => (p.ok ? p.latency_ms : null)),
          // 失败点单独标红：它们是"这一刻渠道坏了"的锚点，
          // 隐掉它们等于把最关键的事件从图上抹去。
          markPoint: {
            symbol: 'circle',
            symbolSize: 8,
            itemStyle: { color: cs.palette[4] },
            label: { show: false },
            data: ordered
              .map((p, i) => (p.ok ? null : { coord: [i, Math.max(p.latency_ms, 0)], value: '' }))
              .filter((d): d is { coord: [number, number]; value: string } => d !== null),
          },
        },
      ],
    }
  }, [timeline, cs])

  const stats = data?.summary

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">渠道健康看板</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            各渠道的调用成功率与巡检探针延迟。点一行看该渠道的延迟时间线。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex flex-wrap gap-1">
            {WINDOW_OPTIONS.map((h) => (
              <button
                key={h}
                type="button"
                onClick={() => setWindowHours(h)}
                className={`rounded-md px-2.5 py-1 text-[12px] transition-colors ${
                  windowHours === h
                    ? 'bg-brand text-white'
                    : 'bg-surface text-ink-3 hover:text-ink'
                }`}
              >
                {WINDOW_LABELS[h]}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="渠道总数" value={stats ? String(stats.total) : '—'} hint={`${stats?.enabled ?? 0} 启用`} />
        <StatCard
          label="健康"
          value={stats ? String(stats.healthy_count) : '—'}
          hint={stats ? `${stats.unhealthy_count} 需关注` : undefined}
        />
        <StatCard
          label="自动停用"
          value={stats ? String(stats.auto_disabled) : '—'}
          hint={stats ? `${stats.disabled} 手动禁用` : undefined}
        />
        <StatCard
          label="统计窗口"
          value={WINDOW_LABELS[windowHours] ?? `${windowHours} 小时`}
          hint={stats?.probe_history_enabled ? undefined : '探针历史未启用'}
        />
      </div>

      {!stats?.probe_history_enabled && stats ? (
        <Card>
          <p className="text-[13px] text-ink-3">
            探针历史当前不可用，因此本页没有延迟曲线。这不是「没有探测记录」——
            延迟与探针成功率两列仍来自真实数据。若需要历史曲线，请确认部署已包含探针历史表并已跑过至少一轮巡检。
          </p>
        </Card>
      ) : null}

      {/* 刻意不再外包一层 Card：DataTable 自带 border-line bg-card 容器，
          外面再套一层会出现双层描边（内白外灰的"相框"效果）。 */}
      <DataTable
        columns={columns}
        rows={data?.items ?? null}
        rowKey={(row) => row.channel_id}
        loading={loading}
        emptyTitle="还没有渠道"
        emptyDescription="先到「渠道管理」里添加一个上游渠道。"
        onRowClick={(row) => setSelectedId((prev) => (prev === row.channel_id ? 0 : row.channel_id))}
      />

      {selectedId > 0 ? (
        <Card>
          <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
            <h2 className="text-[15px] font-semibold text-ink">
              {timeline?.name ?? data?.items.find((i) => i.channel_id === selectedId)?.name ?? '渠道'} · 延迟时间线
            </h2>
            {timeline?.latency_stats.valid ? (
              <span className="text-[12px] text-ink-3">
                P50 {formatNumber(timeline.latency_stats.p50)} ms · P95{' '}
                {formatNumber(timeline.latency_stats.p95)} ms · 峰值{' '}
                {formatNumber(timeline.latency_stats.max)} ms
              </span>
            ) : null}
          </div>

          {timeline?.truncated ? (
            <p className="mb-2 text-[12px] text-ink-3">
              窗口内共 {formatNumber(timeline.total)} 次探测，图中只显示最近 {timeline.points.length} 次。
            </p>
          ) : null}

          {timelineLoading ? (
            <Skeleton className="h-64" />
          ) : latencyOption ? (
            <EChart option={latencyOption} height={280} />
          ) : (
            <EmptyState
              title="窗口内没有可画的探测点"
              description="没有成功的探测，也没有失败记录——换个更长的窗口，或等下一轮巡检跑完再看。"
            />
          )}
        </Card>
      ) : null}
    </div>
  )
}

/** 把 Unix 秒格式化成"多久之前"，用于「最近探针」列。
 *
 * 刻意不用绝对时间：这一列要回答的是"这个数字还新鲜吗"，
 * 而新鲜度是个相对概念。绝对时间（10 月 3 日 14:22）需要读者自己做减法。
 */
function formatRelativeProbe(ts: number): string {
  const diffSec = Math.floor(Date.now() / 1000) - ts
  if (diffSec < 60) return '刚刚'
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} 分钟前`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)} 小时前`
  return `${Math.floor(diffSec / 86400)} 天前`
}
