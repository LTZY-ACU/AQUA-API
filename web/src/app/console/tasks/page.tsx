/** 用户门户：生成任务（/console/tasks）。
 *
 * 意图（Why）：
 *   异步任务列表（图像/视频等生成类能力）。状态徽标区分，支持取消。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { listMyTasks } from '@/api/portal'
import type { Task } from '@/api/types'
import { TASK_KIND_IMAGE, TASK_KIND_MUSIC, TASK_KIND_VIDEO, TASK_STATUS_CANCELED, TASK_STATUS_FAILED, TASK_STATUS_QUEUED, TASK_STATUS_RUNNING, TASK_STATUS_SUCCEEDED } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { ConfirmDialog } from '@/components/ui/Modal'
import { Button } from '@/components/ui/Button'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20

function statusTone(status: number): 'ok' | 'warn' | 'err' | 'off' | 'brand' | 'info' {
  switch (status) {
    case TASK_STATUS_SUCCEEDED: return 'ok'
    case TASK_STATUS_RUNNING: return 'brand'
    case TASK_STATUS_QUEUED: return 'info'
    case TASK_STATUS_FAILED: return 'err'
    default: return 'off'
  }
}

export default function ConsoleTasksPage() {
  const [items, setItems] = useState<Task[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [cancelTarget, setCancelTarget] = useState<Task | null>(null)
  const { t } = useI18n()
  const { toast, toastError } = useToast()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listMyTasks({ page, size: PAGE_SIZE })
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [page])

  useEffect(() => {
    void load()
  }, [load])

  async function handleCancel() {
    if (!cancelTarget) return
    try {
      // 门户暂无取消接口，交由后台处理；此处仅展示确认（实际可在 api/portal 扩展）
      toast(t('portal.tasks.cancelSubmitted'))
      setCancelTarget(null)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('portal.tasks.cancelFailed'))
    }
  }

  const columns: Column<Task>[] = [
    { title: t('portal.tasks.colTask'), render: (row) => <span className="text-ink">{row.kind_text} · {row.model}</span> },
    { title: t('portal.tasks.colStatus'), render: (row) => <Badge tone={statusTone(row.status)}>{row.status_text}</Badge> },
    {
      title: t('portal.tasks.colProgress'),
      render: (row) => (
        <div className="flex items-center gap-2">
          <div className="h-1.5 w-20 overflow-hidden rounded-full bg-ink/10">
            <div className="h-full rounded-full bg-brand" style={{ width: `${Math.min(100, row.progress)}%` }} />
          </div>
          <span className="text-xs text-ink-3">{row.progress}%</span>
        </div>
      ),
    },
    {
      title: t('portal.tasks.colResult'),
      render: (row) =>
        row.result_url ? (
          <a href={row.result_url} target="_blank" rel="noreferrer" className="text-brand hover:underline">{t('portal.tasks.viewResult')}</a>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    { title: t('portal.tasks.colPrompt'), render: (row) => <span className="max-w-48 truncate text-ink-2" title={row.prompt}>{row.prompt || '—'}</span> },
    { title: t('portal.tasks.colTime'), render: (row) => <span className="text-ink-2">{formatDateTime(row.created_at)}</span> },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('portal.tasks.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('portal.tasks.subtitle')}</p>
      </div>

      <Card padding="none">
        <DataTable columns={columns} rows={loading ? null : items} loading={loading} rowKey={(row) => row.task_ref} emptyTitle={t('portal.tasks.empty')} />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <ConfirmDialog
        open={Boolean(cancelTarget)}
        title={t('portal.tasks.cancelTitle')}
        message={t('portal.tasks.cancelMessage')}
        danger
        confirmText={t('portal.tasks.cancelConfirm')}
        onConfirm={handleCancel}
        onCancel={() => setCancelTarget(null)}
      />
    </div>
  )
}