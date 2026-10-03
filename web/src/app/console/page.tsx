/** 用户门户概览页（/console）：余额卡组 + 用量趋势 + 模型排行。
 *
 * 意图（Why）：
 *   3 秒看到「还剩多少、用了多少、主要用在哪」。四个汇总卡 + 两张图，
 *   图表配色来自 utils/chart.ts，亮色下保证可读。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import { fetchFinanceSummary } from '@/api/portal'
import type { FinanceSummary } from '@/api/types'
import { fetchMyTrial } from '@/api/portal'
import { fetchMyUsage } from '@/api/portal'
import type { UsageStats } from '@/api/types'
import { UsageLeaderboard } from '@/components/UsageLeaderboard'
import { Card, Skeleton, StatCard } from '@/components/ui/Display'
import { EChart } from '@/components/ui/EChart'
import { useAuth } from '@/lib/auth/auth-context'
import { useSite } from '@/lib/site/site-context'
import { useTheme, isDarkScheme } from '@/lib/theme/theme-context'
import { areaGradient, chartStyles } from '@/utils/chart'
import { formatYuanFromQuota } from '@/utils/money'

export default function ConsoleOverviewPage() {
  const { refreshUser } = useAuth()
  const { quotaPerYuan } = useSite()
  const { resolved } = useTheme()
  const [finance, setFinance] = useState<FinanceSummary | null>(null)
  const [usage, setUsage] = useState<UsageStats | null>(null)
  const [days, setDays] = useState(7)

  const load = useCallback(async () => {
    try {
      const [f, u] = await Promise.all([fetchFinanceSummary(), fetchMyUsage(days)])
      setFinance(f)
      setUsage(u)
    } catch {
      /* 401 由 client 统一处理 */
    }
  }, [days])

  useEffect(() => {
    void load()
  }, [load])

  // 进入页面刷新一次额度快照（余额可能刚被充值）
  useEffect(() => {
    void refreshUser()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 图表配色随主题切换：ECharts 用 canvas，不认 CSS 变量，必须显式取色。
  // 用 isDarkScheme 而非 === 'dark'：深蓝也是暗色语义，漏判会让图表退化成浅色配色。
  const cs = useMemo(() => chartStyles(isDarkScheme(resolved)), [resolved])

  const trendOption = useMemo(() => {
    const series = usage?.series ?? []
    return {
      tooltip: { trigger: 'axis', ...cs.tooltip },
      grid: { left: 8, right: 8, top: 24, bottom: 8, containLabel: true },
      xAxis: { type: 'category', data: series.map((item) => item.date), axisLabel: cs.axisLabel },
      yAxis: { type: 'value', axisLabel: cs.axisLabel, splitLine: cs.splitLine },
      series: [
        {
          name: '请求数',
          type: 'line',
          smooth: true,
          data: series.map((item) => item.requests),
          itemStyle: { color: cs.palette[0] },
          areaStyle: { color: areaGradient(cs.palette[0]) },
        },
      ],
    }
  }, [usage, cs])

  const modelOption = useMemo(() => {
    const items = (usage?.by_model ?? []).slice(0, 8)
    return {
      tooltip: { trigger: 'item', ...cs.tooltip },
      series: [
        {
          type: 'pie',
          radius: ['42%', '68%'],
          data: items.map((item, index) => ({
            name: item.model,
            value: item.tokens,
            itemStyle: { color: cs.palette[index % cs.palette.length] },
          })),
          label: { color: cs.axisLabel.color, fontSize: 11 },
        },
      ],
    }
  }, [usage, cs])

  const unlimited = finance?.balance_quota === -1

  return (
    <div className="space-y-6">
      {/* 试用额横幅（可折叠信息，不占主要注意力） */}
      <TrialBanner />

      <div>
        <h1 className="text-xl font-bold text-ink">概览</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">当前账户用量与余额</p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="当前余额"
          value={finance ? (unlimited ? '不限' : formatYuanFromQuota(finance.balance_quota, quotaPerYuan)) : '—'}
          hint={unlimited ? '余额不限制' : '可用于调用与充值抵扣'}
        />
        <StatCard label="累计消费" value={finance ? formatYuanFromQuota(finance.used_quota, quotaPerYuan) : '—'} hint="全部历史" />
        <StatCard label="累计充值" value={finance ? formatYuanFromQuota(finance.recharged_quota, quotaPerYuan) : '—'} hint={`${finance?.recharge_count ?? 0} 笔订单`} />
        <StatCard label="邀请返利" value={finance ? formatYuanFromQuota(finance.reward_quota, quotaPerYuan) : '—'} hint="注册奖 + 充值返利" />
      </div>

      <div className="flex items-center justify-between">
        <div className="text-sm font-semibold text-ink-2">近 {days} 日用量</div>
        <div className="flex gap-1 rounded-lg border border-line bg-surface p-1">
          {[7, 30].map((d) => (
            <button
              key={d}
              type="button"
              onClick={() => setDays(d)}
              className={`rounded px-2.5 py-1 text-xs transition ${days === d ? 'bg-card text-ink shadow-sm' : 'text-ink-3 hover:text-ink-2'}`}
            >
              {d} 天
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
        <Card padding="none">
          {usage ? <EChart option={trendOption} height={280} /> : <Skeleton className="m-4 h-64" />}
        </Card>
        <Card padding="none">
          {usage ? <EChart option={modelOption} height={280} /> : <Skeleton className="m-4 h-64" />}
        </Card>
      </div>

      {/* 用量排行榜：付费榜 / 免费榜（各 Top 20，自己所在行高亮并标注"我"） */}
      <UsageLeaderboard />
    </div>
  )
}

function TrialBanner() {
  const [trial, setTrial] = useState<{ active: boolean; remaining: number } | null>(null)
  const { quotaPerYuan } = useSite()
  useEffect(() => {
    void fetchMyTrial().then(setTrial).catch(() => setTrial(null))
  }, [])
  if (!trial?.active) return null
  return (
    <div className="flex items-center gap-2 rounded-lg border border-brand/20 bg-brand/5 px-4 py-2.5 text-[13px] text-ink-2">
      <span className="h-1.5 w-1.5 rounded-full bg-brand" />
      限时试用余额剩余 {formatYuanFromQuota(trial.remaining, quotaPerYuan)}，到期自动失效。
    </div>
  )
}