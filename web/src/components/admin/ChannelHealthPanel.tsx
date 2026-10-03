/** 渠道健康面板：渠道状态分布 + 近 24h 全站调用健康度。
 *
 * 意图（Why）：
 *   渠道管理页的第一问是「上游现在健不健康」。把「渠道停用分布」与「近 24h 调用质量」
 *   聚合到列表顶部，站长不必先翻日志就能判断"是某个渠道挂了还是全站都慢"。
 *
 *   数据来源刻意只用两个【既有】管理接口：
 *     - GET /api/admin/dashboard          → 渠道三态计数 + 今日汇总（含今日消耗）；
 *     - GET /api/admin/maintenance/overview → 真正滚动 24h 窗口的请求数/失败率/平均延迟。
 *   为什么用 maintenance 而不是 dashboard 的近 24h：dashboard 的 today 口径是"今日自 00:00 起"，
 *   跨零点会突变，不能回答"最近 24 小时"；maintenance 的口径才是滚动 24h。
 *
 * 流转（Flow）：
 *   channels/page.tsx → <ChannelHealthPanel/> → fetchDashboard / fetchMaintenanceOverview
 *
 * 扩展（Extend）：
 *   需要"按渠道"聚合成功率/延迟时，后端需新增聚合接口（见本文件 report 说明）；
 *   在此之前本面板只给全站口径，逐渠道明细请以渠道列表与密钥池运行态为准。
 */
'use client'

import { useEffect, useState } from 'react'

import { fetchDashboard } from '@/api/admin'
import { fetchMaintenanceOverview, type MaintenanceOverview } from '@/api/maintenance'
import type { DashboardStats } from '@/api/types'
import { Badge, Skeleton, StatCard } from '@/components/ui/Display'
import { useI18n } from '@/i18n'
import { useSite } from '@/lib/site/site-context'
import { formatLatency, formatNumber } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

/** 成功率 → 徽标色调：≥90% 正常、70%~90% 警告、<70% 危险（与产品约定一致） */
function successTone(rate: number): 'ok' | 'warn' | 'err' {
  if (rate >= 0.9) return 'ok'
  if (rate >= 0.7) return 'warn'
  return 'err'
}

/** 成功率（0~1）→ 百分比文案 */
function formatRate(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`
}

export function ChannelHealthPanel() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()

  const [dash, setDash] = useState<DashboardStats | null>(null)
  const [overview, setOverview] = useState<MaintenanceOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError(null)
    Promise.all([fetchDashboard(), fetchMaintenanceOverview()])
      .then(([d, o]) => {
        if (!alive) return
        setDash(d)
        setOverview(o)
      })
      .catch((err) => {
        // 不静默：加载失败必须让站长看到，否则空卡片会被误读成"没有数据"
        if (alive) setError(err instanceof Error ? err.message : t('components.healthPanel.loadFailed'))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [])

  const channels = dash?.channels
  const disabled = channels ? Math.max(0, channels.total - channels.enabled - channels.auto_disabled) : 0
  const usage24h = overview?.usage.last_24h
  const successRate24h = usage24h ? 1 - usage24h.failure_rate : 0
  const today = dash?.today

  const header = (
    <div className="flex flex-wrap items-end justify-between gap-2">
      <div>
        <h2 className="text-sm font-semibold text-ink">{t('components.healthPanel.title')}</h2>
        <p className="mt-0.5 text-xs text-ink-3">{t('components.healthPanel.subtitle')}</p>
      </div>
      {error && <span className="text-xs text-err">{error}</span>}
    </div>
  )

  // 首次加载：用骨架卡占位，避免先渲染一堆「—」再跳动
  if (loading && !dash && !overview) {
    return (
      <section className="space-y-4">
        {header}
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-[92px] w-full" />
          ))}
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-[92px] w-full" />
          ))}
        </div>
      </section>
    )
  }

  return (
    <section className="space-y-4">
      {header}

      {/* 渠道三态分布 */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label={t('components.healthPanel.totalChannels')} value={channels ? formatNumber(channels.total) : '—'} />
        <StatCard label={t('common.state.enabled')} value={channels ? formatNumber(channels.enabled) : '—'} />
        <StatCard label={t('components.healthPanel.disabledManual')} value={channels ? formatNumber(disabled) : '—'} />
        <StatCard
          label={t('components.healthPanel.disabledAuto')}
          value={channels ? formatNumber(channels.auto_disabled) : '—'}
          hint={t('components.healthPanel.autoHint')}
          extra={channels && channels.auto_disabled > 0 ? <Badge tone="warn">{t('components.healthPanel.needsAttention')}</Badge> : undefined}
        />
      </div>

      {/* 近 24h 全站调用健康度 + 今日消耗 */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label={t('components.healthPanel.requests24h')}
          value={usage24h ? formatNumber(usage24h.requests) : '—'}
          hint={usage24h ? t('components.healthPanel.failuresHint', { count: formatNumber(usage24h.failures), rate: formatRate(usage24h.failure_rate) }) : undefined}
        />
        <StatCard
          label={t('components.healthPanel.success24h')}
          value={usage24h ? formatRate(successRate24h) : '—'}
          extra={
            usage24h ? (
              <Badge tone={successTone(successRate24h)}>
                {successRate24h >= 0.9 ? t('components.healthPanel.normal') : successRate24h >= 0.7 ? t('components.healthPanel.highFailure') : t('components.healthPanel.severeFailure')}
              </Badge>
            ) : undefined
          }
        />
        <StatCard
          label={t('components.healthPanel.latency24h')}
          value={usage24h && usage24h.avg_latency_ms > 0 ? formatLatency(usage24h.avg_latency_ms) : '—'}
        />
        <StatCard
          label={t('components.healthPanel.todayCost')}
          value={today ? formatYuanFromQuota(today.quota, quotaPerYuan) : '—'}
          hint={today ? t('components.healthPanel.todayRequests', { count: formatNumber(today.requests) }) : undefined}
        />
      </div>
    </section>
  )
}
