/** 管理后台：站点公告（/admin/announcements）。
 *
 * 意图（Why）：
 *   后台维护公告的完整生命周期：标题/内容/语气/置顶/启停/发布窗口。
 *   公开端只读「当前可见」的公告（fetchPublicAnnouncements），后台可见全部含停用与过期。
 *
 * 流转（Flow）：
 *   load() → listAnnouncements({page, size}) → 表格 + 分页；
 *   新建/编辑走 AnnouncementFormModal → createAnnouncement / updateAnnouncement；
 *   删除走 ConfirmDialog → deleteAnnouncement。
 *
 * 扩展（Extend）：
 *   新增语气等级：同步 announcement.ts 的 AnnouncementLevel、本页徽标 tone 与下拉选项。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  createAnnouncement,
  deleteAnnouncement,
  listAnnouncements,
  updateAnnouncement,
  type Announcement,
  type AnnouncementLevel,
  type AnnouncementPayload,
} from '@/api/announcement'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20

/** 公告语气 → 徽标配色：info 蓝 / success 绿 / warning 橙 / danger 红 */
function levelTone(level: AnnouncementLevel): 'info' | 'ok' | 'warn' | 'err' {
  switch (level) {
    case 'info': return 'info'
    case 'success': return 'ok'
    case 'warning': return 'warn'
    case 'danger': return 'err'
    default: return 'info'
  }
}

const LEVEL_LABEL_KEYS: Record<AnnouncementLevel, string> = {
  info: 'admin.announcements.level.info',
  success: 'admin.announcements.level.success',
  warning: 'admin.announcements.level.warning',
  danger: 'admin.announcements.level.danger',
}

export default function AdminAnnouncementsPage() {
  const [items, setItems] = useState<Announcement[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<Announcement | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<Announcement | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listAnnouncements({ page, size: PAGE_SIZE })
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

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteAnnouncement(deleteTarget.id)
      toast(t('admin.announcements.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.announcements.toast.deleteFailed'))
    }
  }

  const columns: Column<Announcement>[] = [
    {
      title: t('admin.announcements.col.title'),
      render: (row) => (
        <span className="flex items-center gap-1.5">
          {row.pinned && <span className="text-[13px] leading-none text-brand" title={t('admin.announcements.pinned')}>★</span>}
          <span className="font-medium text-ink">{row.title}</span>
        </span>
      ),
    },
    { title: t('admin.announcements.col.level'), render: (row) => <Badge tone={levelTone(row.level)}>{t(LEVEL_LABEL_KEYS[row.level])}</Badge> },
    {
      title: t('admin.announcements.col.status'),
      render: (row) => (row.enabled ? <Badge tone="ok">{t('admin.announcements.statusEnabled')}</Badge> : <Badge tone="off">{t('admin.announcements.statusDisabled')}</Badge>),
    },
    {
      title: t('admin.announcements.col.publishStart'),
      render: (row) => (
        <span className="text-ink-2">{row.publish_at > 0 ? formatDateTime(row.publish_at) : t('admin.announcements.publishNow')}</span>
      ),
    },
    {
      title: t('admin.announcements.col.expire'),
      render: (row) => (
        <span className="text-ink-2">{row.expire_at > 0 ? formatDateTime(row.expire_at) : t('admin.announcements.neverExpire')}</span>
      ),
    },
    { title: t('admin.announcements.col.updatedAt'), render: (row) => <span className="text-ink-2">{formatDateTime(row.updated_at)}</span> },
    {
      title: t('admin.announcements.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.announcements.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.announcements.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.announcements.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.announcements.subtitle', { total })}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>{t('admin.announcements.create')}</Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.announcements.emptyTitle')}
          emptyDescription={t('admin.announcements.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <AnnouncementFormModal
        open={editing !== null}
        announcement={editing === 'new' ? null : editing}
        onClose={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.announcements.delete.title')}
        message={t('admin.announcements.delete.message', { title: deleteTarget?.title ?? '' })}
        danger
        confirmText={t('admin.announcements.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 公告表单弹层 ─────────────────────────────────────── */

function AnnouncementFormModal({
  open,
  announcement,
  onClose,
  onSaved,
}: {
  open: boolean
  announcement: Announcement | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [level, setLevel] = useState<AnnouncementLevel>('info')
  const [pinned, setPinned] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setTitle(announcement?.title ?? '')
    setContent(announcement?.content ?? '')
    setLevel(announcement?.level ?? 'info')
    setPinned(announcement?.pinned ?? false)
    setEnabled(announcement?.enabled ?? true)
  }, [open, announcement])

  async function handleSubmit() {
    if (!title.trim()) {
      toastError(t('admin.announcements.error.titleRequired'))
      return
    }
    if (!content.trim()) {
      toastError(t('admin.announcements.error.contentRequired'))
      return
    }
    setLoading(true)
    try {
      const payload: AnnouncementPayload = {
        title: title.trim(),
        content: content.trim(),
        level,
        pinned,
        enabled,
      }
      if (announcement) {
        await updateAnnouncement(announcement.id, payload)
        toast(t('admin.announcements.toast.updated'))
      } else {
        await createAnnouncement(payload)
        toast(t('admin.announcements.toast.created'))
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.announcements.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={announcement ? t('admin.announcements.form.editTitle') : t('admin.announcements.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.announcements.form.title')} required>
          <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t('admin.announcements.form.titlePlaceholder')} />
        </Field>

        <Field label={t('admin.announcements.form.content')} required>
          <Textarea value={content} onChange={(e) => setContent(e.target.value)} rows={5} placeholder={t('admin.announcements.form.contentPlaceholder')} />
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <Field label={t('admin.announcements.form.level')}>
            <Select value={level} onChange={(e) => setLevel(e.target.value as AnnouncementLevel)}>
              <option value="info">{t('admin.announcements.levelOption.info')}</option>
              <option value="success">{t('admin.announcements.levelOption.success')}</option>
              <option value="warning">{t('admin.announcements.levelOption.warning')}</option>
              <option value="danger">{t('admin.announcements.levelOption.danger')}</option>
            </Select>
          </Field>

          <div className="flex flex-col gap-3 pt-1">
            <label className="flex items-center justify-between text-[13px] text-ink-2">
              <span>{t('admin.announcements.form.pinned')}</span>
              <Switch checked={pinned} onChange={setPinned} label={t('admin.announcements.form.pinnedSwitch')} />
            </label>
            <label className="flex items-center justify-between text-[13px] text-ink-2">
              <span>{t('admin.announcements.form.enabled')}</span>
              <Switch checked={enabled} onChange={setEnabled} label={t('admin.announcements.form.enabledSwitch')} />
            </label>
          </div>
        </div>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('common.action.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{announcement ? t('admin.announcements.form.save') : t('admin.announcements.form.create')}</Button>
      </div>
    </Modal>
  )
}
