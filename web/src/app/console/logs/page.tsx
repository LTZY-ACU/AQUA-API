/** 用户门户：调用日志（/console/logs）。
 *
 * 意图（Why）：
 *   筛选（模型/状态）→ 表格 → 分页的三动作闭环。状态用徽标区分，
 *   用量与耗时右对齐保证数字列可对齐（CRAP 对齐原则）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { listMyLogs } from '@/api/portal'
import type { LogQuery, UsageLog } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Input, Select } from '@/components/ui/Form'
import { Button } from '@/components/ui/Button'
import { useI18n } from '@/i18n'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime, formatNumber } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

const PAGE_SIZE = 20

export default function ConsoleLogsPage() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [items, setItems] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [model, setModel] = useState('')
  const [status, setStatus] = useState<'' | 'success' | 'error'>('')

  const load = useCallback(async () => {
    setLoading(true)
    const query: LogQuery = { page, size: PAGE_SIZE, model: model || undefined, status: status || undefined }
    try {
      const data = await listMyLogs(query)
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [page, model, status])

  useEffect(() => {
    void load()
  }, [load])

  const columns: Column<UsageLog>[] = [
    { title: t('portal.logs.colTime'), render: (row) => <span className="text-ink-2">{formatDateTime(row.created_at)}</span> },
    {
      title: t('portal.logs.colModel'),
      render: (row) => (
        <div>
          <div className="text-ink">{row.model}</div>
          {row.upstream_model && row.upstream_model !== row.model && (
            <div className="text-xs text-ink-3">{t('portal.logs.upstream', { model: row.upstream_model })}</div>
          )}
        </div>
      ),
    },
    {
      title: t('portal.logs.colStatus'),
      render: (row) =>
        row.status_code >= 200 && row.status_code < 300 ? (
          <Badge tone="ok">{row.status_code}</Badge>
        ) : (
          <Badge tone="err">{row.status_code}</Badge>
        ),
    },
    { title: t('portal.logs.colTokens'), align: 'right', render: (row) => <span className="text-ink-2">{formatNumber(row.total_tokens)}</span> },
    { title: t('portal.logs.colUsage'), align: 'right', render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.quota, quotaPerYuan)}</span> },
    { title: t('portal.logs.colLatency'), align: 'right', render: (row) => <span className="text-ink-2">{row.latency_ms}ms</span> },
    {
      title: t('portal.logs.colError'),
      render: (row) => (row.error ? <span className="max-w-52 truncate text-xs text-err" title={row.error}>{row.error}</span> : <span className="text-ink-3">—</span>),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('portal.logs.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('portal.logs.subtitle')}</p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Input value={model} onChange={(e) => { setModel(e.target.value); setPage(1) }} placeholder={t('portal.logs.filterModel')} className="max-w-52" />
        <Select value={status} onChange={(e) => { setStatus(e.target.value as '' | 'success' | 'error'); setPage(1) }} className="max-w-36">
          <option value="">{t('portal.logs.allStatus')}</option>
          <option value="success">{t('common.state.success')}</option>
          <option value="error">{t('common.state.failed')}</option>
        </Select>
        <Button variant="secondary" onClick={() => { setModel(''); setStatus(''); setPage(1) }}>{t('common.action.reset')}</Button>
      </div>

      <Card padding="none">
        <DataTable columns={columns} rows={loading ? null : items} loading={loading} rowKey={(row) => row.id} emptyTitle={t('portal.logs.empty')} />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>
    </div>
  )
}