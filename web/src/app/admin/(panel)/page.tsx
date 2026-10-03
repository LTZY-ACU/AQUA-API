/** 管理后台：仪表盘（/admin）。
 *
 * 意图（Why）：
 *   只看不操作：汇总卡（渠道/用户/令牌/今日）+ 请求趋势 + Top 模型。
 */
'use client'

import { useEffect, useMemo, useState } from 'react'

import { fetchDashboard } from '@/api/admin'
import type { DashboardStats } from '@/api/types'
import { Card, Skeleton, StatCard } from '@/components/ui/Display'
import { EChart } from '@/components/ui/EChart'
import { useTheme, isDarkScheme } from '@/lib/theme/theme-context'
import { chartStyles } from '@/utils/chart'
import { formatNumber } from '@/utils/format'

export default function AdminDashboardPage() {
  const [stats, setStats] = useState<DashboardStats | null>(null)
  const { resolved } = useTheme()

  useEffect(() => {
    void fetchDashboard().then(setStats).catch(() => setStats(null))
  }, [])

  // 图表配色随主题切换：ECharts 用 canvas，不认 CSS 变量，必须按 isDark 显式取色
  // 用 isDarkScheme 而非 === 'dark'：深蓝也是暗色语义，漏判会让图表退化成浅色配色。
  const cs = useMemo(() => chartStyles(isDarkScheme(resolved)), [resolved])

  const trendOption = useMemo(() => {
    const days = stats?.recent_days ?? []
    return {
      tooltip: { trigger: 'axis', ...cs.tooltip },
      grid: { left: 8, right: 8, top: 28, bottom: 8, containLabel: true },
      legend: { top: 0, textStyle: { color: cs.axisLabel.color, fontSize: 11 } },
      xAxis: { type: 'category', data: days.map((d) => d.date), axisLabel: cs.axisLabel },
      yAxis: { type: 'value', axisLabel: cs.axisLabel, splitLine: cs.splitLine },
      series: [
        { name: '请求数', type: 'bar', data: days.map((d) => d.requests), itemStyle: { color: cs.palette[0] }, barWidth: '50%' },
        { name: 'Token', type: 'bar', data: days.map((d) => d.tokens), itemStyle: { color: cs.palette[1] }, barWidth: '50%' },
      ],
    }
  }, [stats, cs])

  const topModelOption = useMemo(() => {
    const items = stats?.top_models?.slice(0, 8) ?? []
    return {
      tooltip: { trigger: 'item', ...cs.tooltip },
      series: [
        {
          type: 'pie',
          radius: ['42%', '68%'],
          data: items.map((item, index) => ({
            name: item.model,
            value: item.requests,
            itemStyle: { color: cs.palette[index % cs.palette.length] },
          })),
          label: { color: cs.axisLabel.color, fontSize: 11 },
        },
      ],
    }
  }, [stats, cs])

  const today = stats?.today

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-ink">仪表盘</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">全站运行概览</p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="渠道"
          value={stats ? String(stats.channels.total) : '—'}
          hint={`${stats?.channels.enabled ?? 0} 启用 · ${stats?.channels.auto_disabled ?? 0} 自动停用`}
        />
        <StatCard label="用户" value={stats ? String(stats.users.total) : '—'} hint={`${stats?.users.active ?? 0} 活跃`} />
        <StatCard label="令牌" value={stats ? String(stats.tokens.total) : '—'} hint={`${stats?.tokens.enabled ?? 0} 启用`} />
        <StatCard
          label="今日请求"
          value={stats ? formatNumber(today?.requests ?? 0) : '—'}
          hint={today ? `成功率 ${(today.success_rate * 100).toFixed(1)}%` : undefined}
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="今日 Token" value={stats ? formatNumber(today?.tokens ?? 0) : '—'} />
        <StatCard label="平均耗时" value={today && today.avg_latency_ms ? `${today.avg_latency_ms}ms` : '—'} />
        <StatCard label="首包延迟" value={today && today.avg_first_token_ms ? `${today.avg_first_token_ms}ms` : '—'} />
        <StatCard label="输出速率" value={today && today.avg_tokens_per_second ? `${today.avg_tokens_per_second.toFixed(1)} t/s` : '—'} />
      </div>

      <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
        <Card padding="none">
          {stats ? <EChart option={trendOption} height={300} /> : <Skeleton className="m-4 h-64" />}
        </Card>
        <Card padding="none">
          {stats ? <EChart option={topModelOption} height={300} /> : <Skeleton className="m-4 h-64" />}
        </Card>
      </div>
    </div>
  )
}