/** 管理后台：全站调用日志（/admin/logs）。
 *
 * 意图（Why）：
 *   站长需要回答「请求从哪里来、失败在哪一步」。本页提供全站调用明细，
 *   支持按模型 / 用户 / 渠道 / 状态过滤，逐条展示 token、额度、耗时与错误，
 *   是排查「哪个模型不稳、哪个用户刷爆、哪个渠道被上游拒绝」的第一现场。
 *
 * 流转（Flow）：
 *   本页 → listAllLogs({page,size,model,user,channel_id,status}) → GET /api/admin/logs
 *   筛选提交 → 页码复位为 1 → 重新拉取。
 *
 * 扩展（Extend）：
 *   新增筛选条件：在 LogQuery 加字段（后端 handler_logs.go 同步解析），
 *   再在下方筛选条补一个输入框即可，无需改动表格与分页。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { listAllLogs } from '@/api/admin'
import type { LogQuery, UsageLog } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select } from '@/components/ui/Form'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime, formatLatency, formatNumber } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

const PAGE_SIZE = 20

/**
 * HTTP 状态码 → Badge 徽标配色。
 *
 * 与 utils/display.ts 的 httpStatusBadgeClass 完全同口径（2xx 绿 / 4xx 黄 / 5xx 红），
 * 只是那套返回的是旧版 CSS 类名，而本项目的 Badge 组件按 tone 取色，故在此映射。
 */
function statusTone(code: number | undefined): 'ok' | 'warn' | 'err' | 'off' {
  if (!code) return 'off'
  if (code >= 200 && code < 300) return 'ok'
  if (code >= 400 && code < 500) return 'warn'
  if (code >= 500) return 'err'
  return 'off'
}

export default function AdminLogsPage() {
  const { quotaPerYuan } = useSite()
  const [items, setItems] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  // 筛选条件（草稿态：点「查询」才并入请求）
  const [model, setModel] = useState('')
  const [user, setUser] = useState('')
  const [channelId, setChannelId] = useState('')
  const [status, setStatus] = useState<'success' | 'error' | ''>('')

  const { toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    const query: LogQuery = {
      page,
      size: PAGE_SIZE,
      model: model.trim() || undefined,
      user: user.trim() || undefined,
      channel_id: channelId.trim() ? Number(channelId.trim()) : undefined,
      status: status || undefined,
    }
    try {
      const data = await listAllLogs(query)
      setItems(data.items)
      setTotal(data.total)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.logs.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [page, model, user, channelId, status, toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  /** 查询：页码归 1；若已在第 1 页则直接刷新（useEffect 依赖 page，页号不变不会触发） */
  function handleSearch() {
    setPage(1)
    if (page === 1) void load()
  }

  /** 重置：清空筛选并回到第 1 页 */
  function handleReset() {
    setModel('')
    setUser('')
    setChannelId('')
    setStatus('')
    setPage(1)
    if (page === 1) void load()
  }

  const columns: Column<UsageLog>[] = [
    {
      title: t('admin.logs.col.time'),
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: t('admin.logs.col.user'),
      render: (row) => <span className="text-[13px] text-ink-2">{row.username || `#${row.user_id}`}</span>,
    },
    {
      title: t('admin.logs.col.token'),
      render: (row) => (
        <span className="block max-w-36 truncate text-[13px]" title={row.token_name}>{row.token_name || '—'}</span>
      ),
    },
    {
      title: t('admin.logs.col.channel'),
      render: (row) =>
        row.channel_id ? (
          <span className="block max-w-36 truncate text-[13px]" title={row.channel_name}>
            {row.channel_name || `#${row.channel_id}`}
          </span>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    {
      title: t('admin.logs.col.model'),
      render: (row) => (
        <span className="block max-w-40 truncate text-[13px] font-medium text-ink" title={row.model}>{row.model}</span>
      ),
    },
    {
      title: t('admin.logs.col.upstreamModel'),
      render: (row) => (
        <span className="block max-w-32 truncate text-[13px] text-ink-3" title={row.upstream_model}>
          {row.upstream_model || '—'}
        </span>
      ),
    },
    {
      title: t('admin.logs.col.statusCode'),
      align: 'center',
      render: (row) => <Badge tone={statusTone(row.status_code)}>{row.status_code || '—'}</Badge>,
    },
    {
      title: t('admin.logs.col.totalTokens'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatNumber(row.total_tokens)}</span>,
    },
    {
      title: t('admin.logs.col.cost'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatYuanFromQuota(row.quota, quotaPerYuan)}</span>,
    },
    {
      title: t('admin.logs.col.latency'),
      align: 'right',
      render: (row) => <span className="text-[13px] tabular-nums text-ink-2">{formatLatency(row.latency_ms)}</span>,
    },
    {
      title: t('admin.logs.col.error'),
      render: (row) =>
        row.error ? (
          <span className="block max-w-56 truncate text-[13px] text-err" title={row.error}>{row.error}</span>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('admin.logs.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.logs.subtitle', { total: formatNumber(total) })}</p>
      </div>

      {/* 筛选条 */}
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <Field label={t('admin.logs.filter.model')}>
            <Input value={model} onChange={(e) => setModel(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder={t('admin.logs.filter.modelPlaceholder')} className="w-44" />
          </Field>
          <Field label={t('admin.logs.filter.user')}>
            <Input value={user} onChange={(e) => setUser(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder={t('admin.logs.filter.userPlaceholder')} className="w-44" />
          </Field>
          <Field label={t('admin.logs.filter.channelId')}>
            <Input value={channelId} onChange={(e) => setChannelId(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSearch()} placeholder={t('admin.logs.filter.channelPlaceholder')} type="number" className="w-32" />
          </Field>
          <Field label={t('admin.logs.filter.status')}>
            <Select value={status} onChange={(e) => setStatus(e.target.value as 'success' | 'error' | '')} className="w-32">
              <option value="">{t('admin.logs.filter.all')}</option>
              <option value="success">{t('admin.logs.filter.success')}</option>
              <option value="error">{t('admin.logs.filter.error')}</option>
            </Select>
          </Field>
          <div className="flex items-center gap-2">
            <Button variant="primary" onClick={handleSearch}>{t('admin.logs.filter.search')}</Button>
            <Button variant="secondary" onClick={handleReset}>{t('admin.logs.filter.reset')}</Button>
          </div>
        </div>
      </Card>

      {/* 列表 + 分页 */}
      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.logs.emptyTitle')}
          emptyDescription={t('admin.logs.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>
    </div>
  )
}
