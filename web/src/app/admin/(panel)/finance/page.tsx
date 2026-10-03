/** 管理后台：财务对账报表（/admin/finance）。
 *
 * 意图（Why）：
 *   回答站长最关心的一问——「这个月到底赚没赚钱」。按分组 / 渠道 / 模型聚合
 *   「售价收入 − 上游成本」的毛利，并把内部额度单位换算成人民币展示，
 *   让报表数字可以直接和账目对得上。
 *
 * 流转（Flow）：
 *   时间范围 + 维度 → fetchReconciliation() → GET /api/admin/finance/reconciliation?from=&to=&dim=*
 *   → 表格（维度 / 请求数 / 收入 / 成本 / 毛利 / 毛利率）。
 *
 * 扩展（Extend）：
 *   契约见 docs/23 的 F5「真实成本对账报表」：行结构
 *   { key, requests, revenue_quota, cost_quota, gross_quota, margin }（金额均为内部额度单位）。
 *   后端接口落地后本页即可直接用；在落地前请求会返回 404，页面会如实提示而不是静默空表。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { api } from '@/api/client'
import { Badge, Card, Tabs } from '@/components/ui/Display'
import { DataTable, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { useSite } from '@/lib/site/site-context'
import { formatNumber } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

/** 对账维度 */
type Dim = 'group' | 'channel' | 'model'

/** GET /api/admin/finance/reconciliation 的单行（金额均为内部额度单位） */
interface ReconciliationRow {
  /** 维度标识：group | channel | model */
  dim?: Dim
  /** 维度键：分组名 / 渠道 ID / 模型名 */
  key: string
  /** 维度展示名（渠道维度为渠道名；缺失时回退 key） */
  label?: string
  requests: number
  /** 售价收入（额度） */
  revenue_quota: number
  /** 上游成本（额度） */
  cost_quota: number
  /** 毛利 = 收入 − 成本（额度） */
  gross_profit_quota: number
  /** 毛利率（后端已格式化为百分比串，如 "84.5%"） */
  gross_margin: string
  /** 已录进价的请求数 / 未录进价的请求数（未录进价的行成本按 0 计，需人工留意） */
  priced_requests?: number
  unpriced_requests?: number
}

/** 汇总行：与单行同结构，但无 key/label */
type ReconciliationTotals = Omit<ReconciliationRow, 'key' | 'label' | 'dim'>

/** 响应体：{ items, total, totals }；同时容忍裸数组（兼容简化实现）。 */
interface ReconciliationResponse {
  items: ReconciliationRow[]
  total?: number
  totals?: ReconciliationTotals
}

const DIM_TABS: { value: Dim; label: string }[] = [
  { value: 'group', label: '按分组' },
  { value: 'channel', label: '按渠道' },
  { value: 'model', label: '按模型' },
]

/** 本地日期 → 'YYYY-MM-DD'（避免 toISOString 的 UTC 偏移把「今天」算错） */
function localDate(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

/** 已格式化的百分比串 → 数值（用于配色判定）；无法解析时返回 NaN */
function marginPercent(margin: string | undefined): number {
  const n = Number.parseFloat(String(margin ?? '').replace('%', ''))
  return Number.isFinite(n) ? n : Number.NaN
}

/**
 * 拉取对账报表。
 *
 * 契约（docs/23 F5）：GET /api/admin/finance/reconciliation?from=&to=&dim=group|channel|model
 * 支持 from/to 为 YYYY-MM-DD（后端亦接受 Unix 秒）。未落地时后端返回 404，调用方展示错误提示。
 */
async function fetchReconciliation(params: {
  from: string
  to: string
  dim: Dim
}): Promise<{ rows: ReconciliationRow[]; totals?: ReconciliationTotals }> {
  const data = await api.get<ReconciliationResponse | ReconciliationRow[]>(
    '/admin/finance/reconciliation',
    params,
  )
  if (Array.isArray(data)) return { rows: data }
  return { rows: data.items ?? [], totals: data.totals }
}

export default function AdminFinancePage() {
  const { quotaPerYuan } = useSite()

  const today = new Date()
  const monthAgo = new Date(today.getTime() - 30 * 24 * 3600 * 1000)

  const [from, setFrom] = useState(localDate(monthAgo))
  const [to, setTo] = useState(localDate(today))
  const [dim, setDim] = useState<Dim>('group')

  const [rows, setRows] = useState<ReconciliationRow[]>([])
  const [totals, setTotals] = useState<ReconciliationTotals | undefined>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!from || !to) return // 日期未填全时不发请求，避免后端按空范围报错
    setLoading(true)
    setError(null)
    try {
      const data = await fetchReconciliation({ from, to, dim })
      setRows(data.rows)
      setTotals(data.totals)
    } catch (err) {
      setRows([])
      setTotals(undefined)
      setError(err instanceof Error ? err.message : '对账数据加载失败')
    } finally {
      setLoading(false)
    }
  }, [from, to, dim])

  useEffect(() => {
    void load()
  }, [load])

  /** 额度 → 「¥x.xx」；换算比例缺失时退回显示原始额度（绝不假设比例） */
  function formatMoney(quota: number): string {
    return formatYuanFromQuota(quota, quotaPerYuan)
  }

  const columns: Column<ReconciliationRow>[] = [
    { title: '维度', render: (row) => <span className="font-medium text-ink">{row.label || row.key}</span> },
    {
      title: '请求数',
      align: 'right',
      render: (row) => (
        <span className="tabular-nums text-ink-2">
          {formatNumber(row.requests)}
          {row.unpriced_requests ? (
            <span className="ml-1 text-[11px] text-warn" title="其中有请求未录上游进价，成本按 0 计">
              未计价 {formatNumber(row.unpriced_requests)}
            </span>
          ) : null}
        </span>
      ),
    },
    {
      title: '收入',
      align: 'right',
      render: (row) => <span className="tabular-nums text-ink-2">{formatMoney(row.revenue_quota)}</span>,
    },
    {
      title: '成本',
      align: 'right',
      render: (row) => <span className="tabular-nums text-ink-2">{formatMoney(row.cost_quota)}</span>,
    },
    {
      title: '毛利',
      align: 'right',
      render: (row) => (
        <span className={`tabular-nums font-medium ${row.gross_profit_quota < 0 ? 'text-err' : 'text-ink'}`}>
          {formatMoney(row.gross_profit_quota)}
        </span>
      ),
    },
    {
      title: '毛利率',
      align: 'right',
      render: (row) => {
        const pct = marginPercent(row.gross_margin)
        return (
          <Badge tone={pct < 0 ? 'err' : pct < 15 ? 'warn' : 'ok'}>
            {row.gross_margin || '—'}
          </Badge>
        )
      },
    },
  ]

  const summary = totals ? (
    <span className="text-xs text-ink-3">
      合计：收入 {formatMoney(totals.revenue_quota)} · 成本 {formatMoney(totals.cost_quota)} · 毛利{' '}
      <span className={totals.gross_profit_quota < 0 ? 'text-err' : 'text-ok'}>
        {formatMoney(totals.gross_profit_quota)}
      </span>{' '}
      ({totals.gross_margin || '—'})
    </span>
  ) : null

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">财务对账</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">按维度聚合「售价收入 − 上游成本」毛利，金额已换算为人民币</p>
      </div>

      <Card>
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div className="flex flex-wrap items-end gap-3">
            <Field label="开始日期">
              <Input type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} />
            </Field>
            <Field label="结束日期">
              <Input type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} />
            </Field>
            <Button variant="primary" loading={loading} onClick={() => void load()}>
              查询
            </Button>
          </div>
          <Tabs items={DIM_TABS} value={dim} onChange={setDim} />
        </div>
      </Card>

      <Card padding="none">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold text-ink">对账明细</h2>
          <span className="text-xs text-ink-3">
            {from && to ? `${from} ~ ${to}` : '请选择时间范围'} · 共 {rows.length} 行
          </span>
        </div>
        {summary ? <div className="border-b border-line px-4 py-2">{summary}</div> : null}
        {error ? (
          <div className="flex items-center gap-3 px-4 py-6 text-[13px] text-err">
            <span>{error}</span>
            <Button variant="secondary" size="sm" onClick={() => void load()}>
              重试
            </Button>
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={loading ? null : rows}
            loading={loading}
            rowKey={(row) => row.key}
            emptyTitle="该时间段没有对账数据"
            emptyDescription="换个时间范围或维度再试；若接口尚未上线，请联系后端确认 /api/admin/finance/reconciliation。"
          />
        )}
      </Card>
    </div>
  )
}
