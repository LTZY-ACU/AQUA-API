/** 管理后台：内容安全 / 敏感词（/admin/sensitive-words）。
 *
 * 意图（Why）：
 *   内容合规过滤由两块配置组成，二者缺一不可，因此放在同一页：
 *     1) 总开关（系统设置里的 safeguard.sensitive_filter_enabled，开关即持久化）；
 *     2) 词表（新建单条 / 批量导入 / 就地启停 / 删除）。
 *   命中词表的请求会被 /v1 入口直接拒绝，直接影响用户可用性，故总开关默认关闭。
 *
 * 流转（Flow）：
 *   本页 → fetchSettings() 读总开关；updateSettings({safeguard:{sensitive_filter_enabled}}) 写总开关
 *   本页 → listSensitiveWords / createSensitiveWord / importSensitiveWords
 *        / updateSensitiveWord / deleteSensitiveWord → /api/admin/sensitive-words
 *
 * 扩展（Extend）：
 *   新增词条属性：同步 types.ts 的 SensitiveWord 与后端 DTO，再在弹层与列表补字段。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { fetchSettings, updateSettings } from '@/api/admin'
import {
  createSensitiveWord,
  deleteSensitiveWord,
  importSensitiveWords,
  listSensitiveWords,
  updateSensitiveWord,
} from '@/api/safeguard'
import type { SensitiveWord, SensitiveWordPayload } from '@/api/types'
import { Badge, Card, EmptyState } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { DataTable, type Column } from '@/components/ui/Table'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

export default function AdminSensitiveWordsPage() {
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const [items, setItems] = useState<SensitiveWord[]>([])
  const [total, setTotal] = useState(0)
  const [enabledTotal, setEnabledTotal] = useState(0)
  const [loading, setLoading] = useState(true)

  // 总开关（fetchSettings().safeguard.sensitive_filter_enabled）
  const [masterEnabled, setMasterEnabled] = useState(false)
  const [masterBusy, setMasterBusy] = useState(false)

  // 弹层状态
  const [creating, setCreating] = useState(false)
  const [importing, setImporting] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<SensitiveWord | null>(null)
  // 正在就地切换启停的词条 id（0 = 无），用于给对应 Switch 置灰
  const [switchingId, setSwitchingId] = useState(0)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listSensitiveWords()
      setItems(data.items)
      setTotal(data.total)
      setEnabledTotal(data.enabled_total)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [toastError, t])

  useEffect(() => {
    void load()
  }, [load])

  // 总开关初始值来自系统设置
  useEffect(() => {
    void fetchSettings()
      .then((settings) => setMasterEnabled(settings.safeguard?.sensitive_filter_enabled ?? false))
      .catch(() => setMasterEnabled(false))
  }, [])

  /** 总开关切换：乐观更新，失败回滚 */
  async function handleMasterToggle(next: boolean) {
    setMasterEnabled(next)
    setMasterBusy(true)
    try {
      await updateSettings({ safeguard: { sensitive_filter_enabled: next } })
      toast(next ? t('admin.sensitive-words.toast.masterOn') : t('admin.sensitive-words.toast.masterOff'))
    } catch (err) {
      setMasterEnabled(!next)
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.masterFailed'))
    } finally {
      setMasterBusy(false)
    }
  }

  /** 列表内就地启停：只提交 enabled 一个字段（后端部分更新） */
  async function handleToggleWord(word: SensitiveWord) {
    setSwitchingId(word.id)
    try {
      await updateSensitiveWord(word.id, { enabled: !word.enabled })
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.toggleFailed'))
    } finally {
      setSwitchingId(0)
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteSensitiveWord(deleteTarget.id)
      toast(t('admin.sensitive-words.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.deleteFailed'))
    }
  }

  const columns: Column<SensitiveWord>[] = [
    {
      title: t('admin.sensitive-words.col.word'),
      render: (row) => <span className="text-[13px] font-medium text-ink">{row.word}</span>,
    },
    {
      title: t('admin.sensitive-words.col.category'),
      render: (row) => (row.category ? <Badge tone="info">{row.category}</Badge> : <span className="text-ink-3">—</span>),
    },
    {
      title: t('admin.sensitive-words.col.enabled'),
      align: 'center',
      render: (row) => (
        <div className="flex justify-center">
          <Switch checked={row.enabled} disabled={switchingId === row.id} onChange={() => handleToggleWord(row)} label={t('admin.sensitive-words.switchLabel', { word: row.word })} />
        </div>
      ),
    },
    {
      title: t('admin.sensitive-words.col.remark'),
      render: (row) => (
        <span className="block max-w-56 truncate text-[13px] text-ink-3" title={row.remark}>{row.remark || '—'}</span>
      ),
    },
    {
      title: t('admin.sensitive-words.col.updatedAt'),
      render: (row) => <span className="whitespace-nowrap text-[13px] text-ink-3">{formatDateTime(row.updated_at)}</span>,
    },
    {
      title: t('admin.sensitive-words.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">{t('admin.sensitive-words.delete')}</button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.sensitive-words.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">
            {t('admin.sensitive-words.subtitle', { state: masterEnabled ? t('admin.sensitive-words.stateOn') : t('admin.sensitive-words.stateOff'), count: enabledTotal })}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-[13px] text-ink-2">
            <Switch checked={masterEnabled} disabled={masterBusy} onChange={handleMasterToggle} label={t('admin.sensitive-words.masterAria')} />
            {t('admin.sensitive-words.masterSwitch')}
          </label>
          <Button variant="secondary" onClick={() => setImporting(true)}>{t('admin.sensitive-words.import')}</Button>
          <Button variant="primary" onClick={() => setCreating(true)}>{t('admin.sensitive-words.create')}</Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.sensitive-words.emptyTitle')}
          emptyDescription={t('admin.sensitive-words.emptyDescription')}
        />
      </Card>

      {/* 新建词条弹层 */}
      <CreateWordModal
        open={creating}
        onClose={() => setCreating(false)}
        onSaved={() => {
          setCreating(false)
          void load()
        }}
      />

      {/* 批量导入弹层 */}
      <ImportWordsModal
        open={importing}
        onClose={() => setImporting(false)}
        onSaved={() => {
          setImporting(false)
          void load()
        }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.sensitive-words.deleteConfirm.title')}
        message={t('admin.sensitive-words.deleteConfirm.message', { word: deleteTarget?.word ?? '' })}
        danger
        confirmText={t('admin.sensitive-words.deleteConfirm.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 新建词条弹层 ───────────────────────────────────────── */

function CreateWordModal({
  open,
  onClose,
  onSaved,
}: {
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()
  const [word, setWord] = useState('')
  const [category, setCategory] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [remark, setRemark] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setWord('')
    setCategory('')
    setEnabled(true)
    setRemark('')
  }, [open])

  async function handleSubmit() {
    if (!word.trim()) {
      toastError(t('admin.sensitive-words.error.wordRequired'))
      return
    }
    setLoading(true)
    try {
      await createSensitiveWord({
        word: word.trim(),
        category: category.trim() || undefined,
        enabled,
        remark: remark.trim() || undefined,
      } as SensitiveWordPayload)
      toast(t('admin.sensitive-words.toast.added'))
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t('admin.sensitive-words.form.title')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.sensitive-words.form.word')} required help={t('admin.sensitive-words.form.wordHelp')}>
          <Input value={word} onChange={(e) => setWord(e.target.value)} placeholder={t('admin.sensitive-words.form.wordPlaceholder')} />
        </Field>
        <Field label={t('admin.sensitive-words.form.category')} help={t('admin.sensitive-words.form.categoryHelp')}>
          <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder={t('admin.sensitive-words.form.optional')} />
        </Field>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.sensitive-words.form.enabled')}</span>
          <Switch checked={enabled} onChange={setEnabled} label={t('admin.sensitive-words.form.enabled')} />
        </label>
        <Field label={t('admin.sensitive-words.form.remark')}>
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder={t('admin.sensitive-words.form.optional')} />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('admin.sensitive-words.form.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{t('admin.sensitive-words.form.submit')}</Button>
      </div>
    </Modal>
  )
}

/* ── 批量导入弹层 ───────────────────────────────────────── */

function ImportWordsModal({
  open,
  onClose,
  onSaved,
}: {
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()
  const [text, setText] = useState('')
  const [category, setCategory] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setText('')
    setCategory('')
  }, [open])

  async function handleSubmit() {
    if (!text.trim()) {
      toastError(t('admin.sensitive-words.importError.textRequired'))
      return
    }
    setLoading(true)
    try {
      const result = await importSensitiveWords(text, category.trim())
      toast(t('admin.sensitive-words.toast.imported', {
        imported: result.imported,
        total: result.total,
        skipped: result.skipped_invalid ? t('admin.sensitive-words.toast.skipped', { count: result.skipped_invalid }) : '',
      }))
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.sensitive-words.toast.importFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t('admin.sensitive-words.importForm.title')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.sensitive-words.importForm.text')} required help={t('admin.sensitive-words.importForm.textHelp')}>
          <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={8} placeholder={t('admin.sensitive-words.importForm.textPlaceholder')} />
        </Field>
        <Field label={t('admin.sensitive-words.importForm.category')} help={t('admin.sensitive-words.importForm.categoryHelp')}>
          <Input value={category} onChange={(e) => setCategory(e.target.value)} placeholder={t('admin.sensitive-words.importForm.optional')} />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('admin.sensitive-words.importForm.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{t('admin.sensitive-words.importForm.submit')}</Button>
      </div>
    </Modal>
  )
}