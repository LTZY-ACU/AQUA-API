/** 管理后台：异步任务（/admin/tasks）。
 *
 * 意图（Why）：
 *   全站图像/视频/音乐等异步生成任务的调度面板：筛选 + 进度 + 结果 + 取消。
 *   任务在执行中状态会变化，因此用 setInterval 每 5 秒静默轮询刷新，
 *   卸载时清除定时器，避免后台空转。
 *
 * 流转（Flow）：
 *   初次/筛选变化 → load()（带 loading）；轮询 → load(true)（静默刷新，不闪骨架屏）；
 *   取消 → ConfirmDialog → cancelTask(task_ref) → 重新拉列表。
 *
 * 扩展（Extend）：
 *   新增任务类别：同步 types.ts 的 TASK_KIND_* 与本页筛选下拉的选项。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { cancelTask, listAllTasks } from '@/api/admin'
import type { Task } from '@/api/types'
import { TASK_STATUS_CANCELED, TASK_STATUS_FAILED, TASK_STATUS_QUEUED, TASK_STATUS_RUNNING, TASK_STATUS_SUCCEEDED } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Select } from '@/components/ui/Form'
import { ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20
/** 轮询间隔（毫秒） */
const POLL_INTERVAL_MS = 5000

function statusTone(status: number): 'ok' | 'warn' | 'err' | 'off' | 'brand' | 'info' {
  switch (status) {
    case TASK_STATUS_SUCCEEDED: return 'ok'
    case TASK_STATUS_RUNNING: return 'brand'
    case TASK_STATUS_QUEUED: return 'info'
    case TASK_STATUS_FAILED: return 'err'
    case TASK_STATUS_CANCELED: return 'warn'
    default: return 'off'
  }
}

/** 终态（成功/失败/取消）不可再取消 */
function isFinal(status: number): boolean {
  return status === TASK_STATUS_SUCCEEDED || status === TASK_STATUS_FAILED || status === TASK_STATUS_CANCELED
}

export default function AdminTasksPage() {
  const [items, setItems] = useState<Task[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [kind, setKind] = useState('')
  const [status, setStatus] = useState(0)
  const [loading, setLoading] = useState(true)
  const [cancelTarget, setCancelTarget] = useState<Task | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true)
      try {
        const data = await listAllTasks({
          page,
          size: PAGE_SIZE,
          kind,
          status: status === 0 ? undefined : status,
        })
        setItems(data.items)
        setTotal(data.total)
      } catch {
        /* 401 统一处理 */
      } finally {
        if (!silent) setLoading(false)
      }
    },
    [page, kind, status],
  )

  useEffect(() => {
    void load()
  }, [load])

  // 任务在执行中状态会变：每 5 秒静默轮询一次，卸载时清除
  useEffect(() => {
    const timer = setInterval(() => {
      void load(true)
    }, POLL_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [load])

  /** 筛选变化时回到第一页，避免停留在超出结果集的页码 */
  function handleKindChange(value: string) {
    setKind(value)
    setPage(1)
  }

  function handleStatusChange(value: string) {
    setStatus(Number(value))
    setPage(1)
  }

  async function handleCancel() {
    if (!cancelTarget) return
    try {
      await cancelTask(cancelTarget.task_ref)
      toast(t('admin.tasks.toast.canceled'))
      setCancelTarget(null)
      void load(true)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.tasks.toast.cancelFailed'))
    }
  }

  const columns: Column<Task>[] = [
    { title: t('admin.tasks.col.task'), render: (row) => <span className="max-w-44 truncate text-[13px] text-ink-2" title={row.task_ref}>{row.task_ref}</span> },
    { title: t('admin.tasks.col.user'), align: 'right', render: (row) => <span className="text-ink-2">#{row.user_id}</span> },
    { title: t('admin.tasks.col.kind'), render: (row) => <span className="text-ink-2">{row.kind_text}</span> },
    { title: t('admin.tasks.col.model'), render: (row) => <span className="text-ink-2">{row.model || '—'}</span> },
    { title: t('admin.tasks.col.status'), render: (row) => <Badge tone={statusTone(row.status)}>{row.status_text}</Badge> },
    {
      title: t('admin.tasks.col.progress'),
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
      title: t('admin.tasks.col.result'),
      render: (row) =>
        row.result_url ? (
          <a href={row.result_url} target="_blank" rel="noreferrer" className="text-brand hover:underline">{t('admin.tasks.viewResult')}</a>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    { title: t('admin.tasks.col.createdAt'), render: (row) => <span className="text-ink-2">{formatDateTime(row.created_at)}</span> },
    {
      title: t('admin.tasks.col.actions'),
      align: 'right',
      render: (row) =>
        isFinal(row.status) ? (
          <span className="text-ink-3/50">—</span>
        ) : (
          <button type="button" onClick={() => setCancelTarget(row)} className="text-[13px] text-ink-3 hover:text-err">
            {t('admin.tasks.action.cancel')}
          </button>
        ),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('admin.tasks.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.tasks.subtitle')}</p>
      </div>

      {/* 筛选条：类别 + 状态 */}
      <div className="flex flex-wrap items-center gap-3">
        <Select
          value={kind}
          onChange={(e) => handleKindChange(e.target.value)}
          className="w-36"
          aria-label={t('admin.tasks.filter.kindAria')}
        >
          <option value="">{t('admin.tasks.filter.kindAll')}</option>
          <option value="image">{t('admin.tasks.filter.kindImage')}</option>
          <option value="video">{t('admin.tasks.filter.kindVideo')}</option>
          <option value="music">{t('admin.tasks.filter.kindMusic')}</option>
        </Select>
        <Select
          value={status}
          onChange={(e) => handleStatusChange(e.target.value)}
          className="w-36"
          aria-label={t('admin.tasks.filter.statusAria')}
        >
          <option value={0}>{t('admin.tasks.filter.statusAll')}</option>
          <option value={1}>{t('admin.tasks.filter.statusQueued')}</option>
          <option value={2}>{t('admin.tasks.filter.statusRunning')}</option>
          <option value={3}>{t('admin.tasks.filter.statusSucceeded')}</option>
          <option value={4}>{t('admin.tasks.filter.statusFailed')}</option>
          <option value={5}>{t('admin.tasks.filter.statusCanceled')}</option>
        </Select>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.task_ref}
          emptyTitle={t('admin.tasks.emptyTitle')}
          emptyDescription={t('admin.tasks.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <ConfirmDialog
        open={Boolean(cancelTarget)}
        title={t('admin.tasks.confirm.title')}
        message={t('admin.tasks.confirm.message', { ref: cancelTarget?.task_ref ?? '' })}
        danger
        confirmText={t('admin.tasks.confirm.confirmText')}
        onConfirm={handleCancel}
        onCancel={() => setCancelTarget(null)}
      />
    </div>
  )
}
