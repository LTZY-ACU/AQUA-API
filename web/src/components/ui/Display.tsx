/** 展示类基础组件：Card / Badge / StatCard / Skeleton / EmptyState / CodeBlock / Tabs。
 *
 * 意图（Why）：
 *   后台与门户的「信息展示」高度复用这些基础件；集中定义保证视觉重复性与一致性，
 *   避免每个页面各自拼样式类。
 */
'use client'

import { useState, type ReactNode } from 'react'

import { useI18n } from '@/i18n'

/* ── Card ─────────────────────────────────────────────── */

interface CardProps {
  children: ReactNode
  className?: string
  /** 内部纵向间距（默认 p-5，紧凑场景用 p-4） */
  padding?: 'md' | 'lg' | 'none'
}

export function Card({ children, className, padding = 'md' }: CardProps) {
  const pad = padding === 'lg' ? 'p-6' : padding === 'none' ? 'p-0' : 'p-5'
  return (
    <div className={`rounded-lg border border-line bg-card ${pad} ${className ?? ''}`}>
      {children}
    </div>
  )
}

/* ── Badge ─────────────────────────────────────────────── */

type BadgeTone = 'ok' | 'warn' | 'err' | 'info' | 'off' | 'brand'

export function Badge({ tone = 'off', children }: { tone?: BadgeTone; children: ReactNode }) {
  const toneClass: Record<BadgeTone, string> = {
    ok: 'bg-ok/10 text-ok border-ok/25',
    warn: 'bg-warn/10 text-warn border-warn/25',
    err: 'bg-err/10 text-err border-err/25',
    info: 'bg-info/10 text-info border-info/25',
    off: 'bg-ink/5 text-ink-2 border-ink/10',
    brand: 'bg-brand/10 text-brand border-brand/25',
  }
  return (
    <span className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-xs ${toneClass[tone]}`}>
      {children}
    </span>
  )
}

/* ── StatCard：仪表盘/概览的汇总卡 ──────────────────────── */

interface StatCardProps {
  label: string
  value: string
  /** 辅助说明（如单位/对比） */
  hint?: string
  /** 卡片内的小徽标或趋势 */
  extra?: ReactNode
}

export function StatCard({ label, value, hint, extra }: StatCardProps) {
  // 超长数字（如管理员配额 10,000,000,000,000）在 22px 下会撑破卡片，
  // 按字符数降一档字号并允许折行，保证任何量级都不溢出。
  const compact = value.length > 10
  return (
    <div className="rounded-lg border border-line bg-card p-5">
      <div className="text-[13px] text-ink-3">{label}</div>
      <div
        className={`mt-1.5 font-semibold tracking-tight text-ink tabular-nums ${
          compact ? 'break-all text-[17px] leading-snug' : 'text-[22px]'
        }`}
      >
        {value}
      </div>
      {hint && <div className="mt-1 text-xs text-ink-3">{hint}</div>}
      {extra && <div className="mt-2">{extra}</div>}
    </div>
  )
}

/* ── Skeleton：数据加载占位 ─────────────────────────────── */

export function Skeleton({ className = '' }: { className?: string }) {
  return <div className={`animate-pulse rounded-md bg-ink/8 ${className}`} />
}

export function SkeletonRows({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  )
}

/* ── EmptyState：空数据占位 ────────────────────────────── */

interface EmptyStateProps {
  title: string
  description?: string
  action?: ReactNode
}

export function EmptyState({ title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-14 text-center">
      <div className="text-sm font-medium text-ink-2">{title}</div>
      {description && <div className="max-w-md text-[13px] text-ink-3">{description}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

/* ── CodeBlock：只读代码展示（含一键复制） ──────────────── */

import { copyText } from '@/utils/clipboard'

interface CodeBlockProps {
  code: string
  language?: string
  title?: string
}

export function CodeBlock({ code, language, title }: CodeBlockProps) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  async function handleCopy() {
    const ok = await copyText(code)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }
  return (
    // 代码块用专用 code-bg/code-fg 令牌：两套主题下都保持深底浅字，
    // 不复用 ink/surface（它们在暗色下会互换，导致代码块反相成浅底深字）。
    <div className="overflow-hidden rounded-lg border border-line bg-code-bg text-code-fg">
      <div className="flex items-center justify-between border-b border-white/10 px-4 py-2 text-xs">
        <span className="text-white/60">{title || language || 'code'}</span>
        <button type="button" onClick={handleCopy} className="flex items-center gap-1 text-white/60 hover:text-white">
          {copied ? t('components.copy.copied') : t('components.copy.copy')}
        </button>
      </div>
      <pre className="code-block overflow-x-auto p-4 text-white/90">{code}</pre>
    </div>
  )
}

/* ── Tabs：分段式切换（用于广场分组 / 设置分区） ─────────── */

interface TabItem<T extends string> {
  value: T
  label: string
  count?: number
}

interface TabsProps<T extends string> {
  items: TabItem<T>[]
  value: T
  onChange: (value: T) => void
}

export function Tabs<T extends string>({ items, value, onChange }: TabsProps<T>) {
  return (
    <div className="flex flex-wrap gap-1 rounded-lg border border-line bg-surface p-1" role="tablist">
      {items.map((item) => {
        const active = item.value === value
        return (
          <button
            key={item.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(item.value)}
            className={`flex items-center gap-1.5 rounded-md px-3 py-1.5 text-[13px] transition ${
              active ? 'bg-card text-ink shadow-sm border border-line-2' : 'text-ink-3 hover:text-ink-2'
            }`}
          >
            {item.label}
            {item.count !== undefined && (
              <span className={`rounded bg-ink/5 px-1.5 text-xs ${active ? 'text-brand' : 'text-ink-3'}`}>
                {item.count}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}