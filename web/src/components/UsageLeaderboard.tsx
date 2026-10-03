/**
 * 用量排行榜组件：付费榜 / 免费榜（各 Top 20）。
 *
 * 意图（Why）：
 *   概览页图表下方放一张"谁在用、用了多少"的榜单，让用户看到自己的
 *   活跃度位置，也让站长（管理员）快速感知全站使用分布。
 *   拆成付费 / 免费两个 Tab：付费用户与免费用户的使用强度差异巨大，
 *   混排会让免费榜永远被付费用户占据。
 *
 * 流转（Flow）：
 *   console/page.tsx → <UsageLeaderboard /> → fetchLeaderboard(days, all)
 *     └─ 分榜渲染：头像（用户名哈希配色）+ 账号 ID + 请求数 + token + 分数
 *         + 平均耗时 + 峰值并发；自己所在行高亮并标注"我"
 *
 * 设计说明：
 *   - 头像无真实图片源，用用户名首字符 + 哈希配色生成（与 vendor.ts 同思路，
 *     保证同一用户名颜色稳定）；不存在外部请求，离线可用。
 *   - 管理员可通过「查看完整榜单」按钮切换 all=1，全量展示（表格内滚动）。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import { fetchLeaderboard } from '@/api/portal'
import type { LeaderboardEntry, LeaderboardStats } from '@/api/types'
import { useAuth } from '@/lib/auth/auth-context'
import { StatCard } from '@/components/ui/Display'
import { formatNumber } from '@/utils/format'

/** 头像配色候选（与 utils/vendor.ts 同风格：浅底深字 + 内描边，昼夜两套） */
const AVATAR_TONES = [
  'bg-cyan-500/10 text-cyan-700 ring-cyan-500/25 dark:bg-cyan-400/15 dark:text-cyan-300 dark:ring-cyan-400/30',
  'bg-indigo-500/10 text-indigo-700 ring-indigo-500/25 dark:bg-indigo-400/15 dark:text-indigo-300 dark:ring-indigo-400/30',
  'bg-emerald-500/10 text-emerald-700 ring-emerald-500/25 dark:bg-emerald-400/15 dark:text-emerald-300 dark:ring-emerald-400/30',
  'bg-amber-500/10 text-amber-700 ring-amber-500/25 dark:bg-amber-400/15 dark:text-amber-300 dark:ring-amber-400/30',
  'bg-rose-500/10 text-rose-700 ring-rose-500/25 dark:bg-rose-400/15 dark:text-rose-300 dark:ring-rose-400/30',
  'bg-violet-500/10 text-violet-700 ring-violet-500/25 dark:bg-violet-400/15 dark:text-violet-300 dark:ring-violet-400/30',
  'bg-sky-500/10 text-sky-700 ring-sky-500/25 dark:bg-sky-400/15 dark:text-sky-300 dark:ring-sky-400/30',
  'bg-teal-500/10 text-teal-700 ring-teal-500/25 dark:bg-teal-400/15 dark:text-teal-300 dark:ring-teal-400/30',
]

/** 对用户名做稳定哈希后取色（djb2 变体，与 vendorTone 同一思路） */
function usernameTone(name: string): string {
  let hash = 5381
  for (let index = 0; index < name.length; index += 1) {
    hash = (hash * 33 + name.charCodeAt(index)) % 1_000_003
  }
  return AVATAR_TONES[hash % AVATAR_TONES.length]
}

/** 头像首字符（用户名首字符大写；空名回退 #） */
function usernameInitial(name: string): string {
  const trimmed = name.trim()
  return trimmed ? trimmed.slice(0, 1).toUpperCase() : '#'
}

/** 成功率徽标：≥95% 绿、≥80% 常规、低于 80% 橙/红一眼可辨。 */
function SuccessRateBadge({ rate }: { rate: number }) {
  const pct = (rate * 100).toFixed(1)
  const tone =
    rate >= 0.95
      ? 'bg-ok/10 text-ok border-ok/25'
      : rate >= 0.8
        ? 'text-ink-2'
        : rate >= 0.5
          ? 'bg-warn/10 text-warn border-warn/25'
          : 'bg-err/10 text-err border-err/25'
  return (
    <span className={`inline-flex items-center rounded border px-1.5 py-0.5 text-xs tabular-nums ${tone}`}>
      {pct}%
    </span>
  )
}

/** 单行渲染（表格行）。highlight 表示当前登录用户所在行。 */
function LeaderboardRow({ entry, highlight }: { entry: LeaderboardEntry; highlight: boolean }) {
  return (
    <tr
      className={`border-b border-line/70 transition last:border-0 ${
        highlight ? 'bg-brand/10 font-medium text-ink' : 'hover:bg-surface/40 text-ink-2'
      }`}
    >
      {/* 名次 */}
      <td className="px-3 py-2.5">
        <div className="flex items-center gap-2">
          <span
            className={`inline-flex h-6 w-6 items-center justify-center rounded text-xs font-semibold tabular-nums ${
              entry.rank <= 3
                ? 'bg-brand/15 text-brand'
                : 'bg-ink/5 text-ink-3'
            }`}
          >
            {entry.rank}
          </span>
          {highlight && (
            <span className="rounded bg-brand px-1.5 py-0.5 text-[11px] font-semibold text-on-brand">我</span>
          )}
        </div>
      </td>
      {/* 头像 + 账号 ID */}
      <td className="px-3 py-2.5">
        <div className="flex items-center gap-2.5">
          <span
            className={`inline-flex h-8 w-8 items-center justify-center rounded-full text-sm font-semibold ring-1 ${usernameTone(
              entry.username,
            )}`}
          >
            {usernameInitial(entry.username)}
          </span>
          <span className="max-w-[140px] truncate text-[13px]">{entry.username || `#${entry.user_id}`}</span>
        </div>
      </td>
      {/* 请求数 / token */}
      <td className="px-3 py-2.5 text-right text-[13px] tabular-nums">{formatNumber(entry.requests)}</td>
      <td className="px-3 py-2.5 text-right text-[13px] tabular-nums">{formatNumber(entry.tokens)}</td>
      {/* 成功率：独立成列，与"综合分数"区分开——
          分数衡量"用得多不多"，成功率衡量"用得稳不稳" */}
      <td className="px-3 py-2.5 text-right">
        <SuccessRateBadge rate={entry.success_rate} />
      </td>
      {/* 综合分数：0~100 分制（榜首封顶 100） */}
      <td className="px-3 py-2.5 text-right text-[13px] font-semibold tabular-nums text-ink-2">
        {entry.score.toFixed(1)}
      </td>
      {/* 平均请求时间 */}
      <td className="px-3 py-2.5 text-right text-[13px] tabular-nums">{entry.avg_latency_ms.toFixed(0)} ms</td>
      {/* 峰值并发 */}
      <td className="px-3 py-2.5 text-right text-[13px] tabular-nums">{entry.peak_concurrency}</td>
    </tr>
  )
}

/** 榜单主体（一个 Tab 的表格） */
function LeaderboardTable({ section }: { section: LeaderboardEntry[] }) {
  if (!section || section.length === 0) {
    return <div className="px-4 py-8 text-center text-[13px] text-ink-3">暂无数据</div>
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-line bg-surface/70 text-[12px] text-ink-3">
            <th className="px-3 py-2 text-left font-medium">名次</th>
            <th className="px-3 py-2 text-left font-medium">账号</th>
            <th className="px-3 py-2 text-right font-medium">请求数</th>
            <th className="px-3 py-2 text-right font-medium">Token</th>
            <th className="px-3 py-2 text-right font-medium">成功率</th>
            <th className="px-3 py-2 text-right font-medium">综合分数</th>
            <th className="px-3 py-2 text-right font-medium">平均耗时</th>
            <th className="px-3 py-2 text-right font-medium">峰值并发</th>
          </tr>
        </thead>
        <tbody>
          {section.map((entry) => (
            <LeaderboardRow key={entry.user_id} entry={entry} highlight={entry.is_me} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

type TabKey = 'paid' | 'free'

export function UsageLeaderboard() {
  const { isAdmin } = useAuth()
  const [stats, setStats] = useState<LeaderboardStats | null>(null)
  const [tab, setTab] = useState<TabKey>('paid')
  const [days, setDays] = useState(30)
  const [showAll, setShowAll] = useState(false)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await fetchLeaderboard(days, showAll)
      setStats(data)
    } catch {
      setStats(null)
    } finally {
      setLoading(false)
    }
  }, [days, showAll])

  useEffect(() => {
    void load()
  }, [load])

  const section = useMemo(() => (stats ? stats[tab]?.items ?? [] : []), [stats, tab])
  const myRank = stats?.[tab]?.my_rank ?? 0

  return (
    <div className="space-y-4">
      {/* ── 全站汇总：与主站概览页同款 StatCard（复用组件，深浅色自动适配） ── */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <StatCard
          label="总请求"
          value={stats ? formatNumber(stats.totals.requests) : '—'}
          hint={`近 ${days} 天 · ${stats ? stats.totals.users : 0} 位用户`}
        />
        <StatCard
          label="总 Token"
          value={stats ? formatNumber(stats.totals.tokens) : '—'}
          hint="输入 + 输出"
        />
        <StatCard
          label="总成功率"
          value={stats ? `${(stats.totals.success_rate * 100).toFixed(1)}%` : '—'}
          hint={`近 ${days} 天`}
        />
      </div>

      <div className="rounded-lg border border-line bg-card">
      {/* 头部：标题 + 窗口切换 + 管理员完整榜单开关 */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
        <div>
          <h2 className="text-[15px] font-semibold text-ink">用量排行榜</h2>
          <p className="mt-0.5 text-[12px] text-ink-3">综合分数 = 请求数 50% + Token 消耗 50%，按用户归一化</p>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex gap-1 rounded-lg border border-line bg-surface p-1">
            {[7, 30, 90].map((d) => (
              <button
                key={d}
                type="button"
                onClick={() => setDays(d)}
                className={`rounded px-2 py-1 text-xs transition ${
                  days === d ? 'bg-card text-ink shadow-sm' : 'text-ink-3 hover:text-ink-2'
                }`}
              >
                {d} 天
              </button>
            ))}
          </div>
          {isAdmin && (
            <button
              type="button"
              onClick={() => setShowAll((v) => !v)}
              className={`rounded-lg border px-2.5 py-1 text-xs transition ${
                showAll
                  ? 'border-brand bg-brand/10 text-brand'
                  : 'border-line text-ink-3 hover:border-brand/40 hover:text-brand'
              }`}
            >
              {showAll ? '完整榜单 ✓' : '完整榜单'}
            </button>
          )}
        </div>
      </div>

      {/* Tab：付费榜 / 免费榜 */}
      <div className="flex gap-1 border-b border-line px-4 pt-2">
        {(
          [
            { key: 'paid', label: '付费榜' },
            { key: 'free', label: '免费榜' },
          ] as { key: TabKey; label: string }[]
        ).map((item) => (
          <button
            key={item.key}
            type="button"
            onClick={() => setTab(item.key)}
            className={`rounded-t px-3 py-2 text-[13px] transition ${
              tab === item.key
                ? 'border-b-2 border-brand font-medium text-brand'
                : 'text-ink-3 hover:text-ink'
            }`}
          >
            {item.label}
          </button>
        ))}
        {/* 我的名次（未上榜不显示） */}
        {myRank > 0 && (
          <div className="ml-auto self-center pb-1 text-[12px] text-ink-3">
            我的名次：<span className="font-semibold text-ink">{myRank}</span>
          </div>
        )}
      </div>

      {/* 榜单主体 */}
      <div className="p-1">
        {loading ? (
          <div className="px-4 py-8 text-center text-[13px] text-ink-3">加载中…</div>
        ) : (
          <LeaderboardTable section={section} />
        )}
      </div>
      </div>
    </div>
  )
}
