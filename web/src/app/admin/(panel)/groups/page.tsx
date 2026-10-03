/** 管理后台：模型分组（/admin/groups）。
 *
 * 意图（Why）：
 *   分组是「计费倍率 + 解锁门槛」的载体（ratio 100 = 1.0 倍、150 = 1.5 倍）。
 *   列表展示引用统计（渠道数 / 价格数），删除前提示引用情况。
 *
 * 流转（Flow）：
 *   load() → listGroups() → 表格；新建/编辑走 GroupFormModal → createGroup / updateGroup；
 *   删除走 ConfirmDialog → deleteGroup（后端按引用计数拒绝）。
 *
 * 扩展（Extend）：
 *   新增分组字段：同步 types.ts 的 ModelGroup / ModelGroupPayload 与弹层表单。
 *   rpm_limit 后端尚未进入 ModelGroupPayload，这里用本地交叉类型 GroupWithRpm 承载，
 *   接口沿用现有分组创建/更新，请求体多带一个 rpm_limit（见文件内注释）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { createGroup, deleteGroup, listGroups, updateGroup } from '@/api/admin'
import type { ModelGroup, ModelGroupPayload } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { centsToYuan, formatNumber, yuanToCents } from '@/utils/format'

/**
 * 分组 + 后端新增的每分钟请求上限字段。
 *
 * 契约（后端将落地）：model_groups.rpm_limit INTEGER，0 = 不限。
 * 为什么不改 types.ts 的 ModelGroup：该共享类型被大量页面引用，本轮并行开发期间
 * 不宜改动；此处以交叉类型的方式局部承载，接口响应多出的字段天然被忽略。
 */
type GroupWithRpm = ModelGroup & { rpm_limit?: number }

export default function AdminGroupsPage() {
  const [items, setItems] = useState<GroupWithRpm[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<GroupWithRpm | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<GroupWithRpm | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  // 分组数量极少，后端不分页，一次拉全量
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listGroups()
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteGroup(deleteTarget.id)
      toast(t('admin.groups.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.groups.toast.deleteFailed'))
    }
  }

  const columns: Column<GroupWithRpm>[] = [
    { title: t('admin.groups.col.name'), render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    { title: t('admin.groups.col.label'), render: (row) => <span className="text-ink-2">{row.label}</span> },
    {
      title: t('admin.groups.col.ratio'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {t('admin.groups.ratioValue', { ratio: row.ratio, times: formatNumber(row.ratio / 100) })}
        </span>
      ),
    },
    {
      title: t('admin.groups.col.rpm'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.rpm_limit && row.rpm_limit > 0 ? t('admin.groups.rpmValue', { count: formatNumber(row.rpm_limit) }) : t('admin.groups.rpmUnlimited')}
        </span>
      ),
    },
    {
      title: t('admin.groups.col.description'),
      render: (row) => (
        <span className="max-w-56 truncate text-[13px] text-ink-3" title={row.description || undefined}>
          {row.description || '—'}
        </span>
      ),
    },
    {
      title: t('admin.groups.col.status'),
      render: (row) => (row.enabled ? <Badge tone="ok">{t('admin.groups.statusEnabled')}</Badge> : <Badge tone="off">{t('admin.groups.statusDisabled')}</Badge>),
    },
    {
      title: t('admin.groups.col.refs'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {t('admin.groups.refsValue', { channels: row.channel_count, prices: row.price_count })}
        </span>
      ),
    },
    {
      title: t('admin.groups.col.unlock'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.unlock_min_recharge_cents > 0 ? t('admin.groups.unlockValue', { amount: formatNumber(centsToYuan(row.unlock_min_recharge_cents)) }) : t('admin.groups.unlockNone')}
        </span>
      ),
    },
    {
      title: t('admin.groups.col.wholesale'),
      render: (row) => (row.admin_only ? <Badge tone="brand">{t('admin.groups.wholesaleBadge')}</Badge> : <span className="text-ink-3">—</span>),
    },
    {
      title: t('admin.groups.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.groups.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.groups.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.groups.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.groups.subtitle', { total })}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>{t('admin.groups.create')}</Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.groups.emptyTitle')}
          emptyDescription={t('admin.groups.emptyDescription')}
        />
      </Card>

      <GroupFormModal
        open={editing !== null}
        group={editing === 'new' ? null : editing}
        onClose={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.groups.delete.title')}
        message={
          deleteTarget
            ? t('admin.groups.delete.message', { name: deleteTarget.name, channels: deleteTarget.channel_count, prices: deleteTarget.price_count })
            : undefined
        }
        danger
        confirmText={t('admin.groups.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 分组表单弹层 ─────────────────────────────────────── */

function GroupFormModal({
  open,
  group,
  onClose,
  onSaved,
}: {
  open: boolean
  group: GroupWithRpm | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [ratio, setRatio] = useState('100')
  const [description, setDescription] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [unlockYuan, setUnlockYuan] = useState('0')
  const [rpmLimit, setRpmLimit] = useState('0')
  const [adminOnly, setAdminOnly] = useState(false)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setName(group?.name ?? '')
    setDisplayName(group?.display_name ?? '')
    setRatio(String(group?.ratio ?? 100))
    setDescription(group?.description ?? '')
    setEnabled(group?.enabled ?? true)
    // 解锁门槛以「分」存取，表单按「元」输入，在边界上换算
    setUnlockYuan(String(centsToYuan(group?.unlock_min_recharge_cents ?? 0)))
    setRpmLimit(String(group?.rpm_limit ?? 0))
    setAdminOnly(group?.admin_only ?? false)
  }, [open, group])

  async function handleSubmit() {
    const nameValue = name.trim()
    if (!group && !nameValue) {
      toastError(t('admin.groups.error.nameRequired'))
      return
    }
    const ratioValue = Number(ratio)
    if (!Number.isFinite(ratioValue) || ratioValue < 0) {
      toastError(t('admin.groups.error.ratioInvalid'))
      return
    }
    const cents = unlockYuan.trim() === '' ? 0 : yuanToCents(unlockYuan)
    if (cents === null) {
      toastError(t('admin.groups.error.unlockInvalid'))
      return
    }
    // RPM 是每分钟请求上限的整数计数，0 表示不限；小数没有意义
    const rpmValue = rpmLimit.trim() === '' ? 0 : Number(rpmLimit)
    if (!Number.isInteger(rpmValue) || rpmValue < 0) {
      toastError(t('admin.groups.error.rpmInvalid'))
      return
    }
    setLoading(true)
    try {
      if (group) {
        // 标识创建后不可修改；其余字段用户显式选择，全量提交
        // rpm_limit 不在 ModelGroupPayload 中，故用交叉类型承载：接口复用现有更新接口，
        // 请求体多带一个 rpm_limit（后端落地后即生效；未落地时被忽略）。
        const payload: Partial<ModelGroupPayload> & { rpm_limit: number } = {
          display_name: displayName.trim(),
          ratio: ratioValue,
          description: description.trim(),
          enabled,
          unlock_min_recharge_cents: cents,
          admin_only: adminOnly,
          rpm_limit: rpmValue,
        }
        await updateGroup(group.id, payload)
        toast(t('admin.groups.toast.updated'))
      } else {
        const payload: ModelGroupPayload & { rpm_limit: number } = {
          name: nameValue.toLowerCase(),
          display_name: displayName.trim(),
          ratio: ratioValue,
          description: description.trim(),
          enabled,
          unlock_min_recharge_cents: cents,
          admin_only: adminOnly,
          rpm_limit: rpmValue,
        }
        await createGroup(payload)
        toast(t('admin.groups.toast.created'))
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.groups.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={group ? t('admin.groups.form.editTitle') : t('admin.groups.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.groups.form.name')} required={!group} help={group ? t('admin.groups.form.nameHelpEdit') : t('admin.groups.form.nameHelpNew')}>
          <Input value={name} onChange={(e) => setName(e.target.value)} disabled={Boolean(group)} placeholder={t('admin.groups.form.namePlaceholder')} />
        </Field>

        <Field label={t('admin.groups.form.displayName')} help={t('admin.groups.form.displayNameHelp')}>
          <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder={t('admin.groups.form.displayNamePlaceholder')} />
        </Field>

        <Field label={t('admin.groups.form.ratio')} help={t('admin.groups.form.ratioHelp')}>
          <Input value={ratio} onChange={(e) => setRatio(e.target.value)} type="number" min={0} step="1" placeholder="100" />
        </Field>

        <Field label={t('admin.groups.form.rpm')} help={t('admin.groups.form.rpmHelp')}>
          <Input value={rpmLimit} onChange={(e) => setRpmLimit(e.target.value)} type="number" min={0} step="1" placeholder="0" />
        </Field>

        <Field label={t('admin.groups.form.description')}>
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={3} placeholder={t('admin.groups.form.descriptionPlaceholder')} />
        </Field>

        <Field label={t('admin.groups.form.unlock')} help={t('admin.groups.form.unlockHelp')}>
          <Input value={unlockYuan} onChange={(e) => setUnlockYuan(e.target.value)} type="number" min={0} step="0.01" placeholder="0" />
        </Field>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.groups.form.enable')}</span>
          <Switch checked={enabled} onChange={setEnabled} label={t('admin.groups.form.enable')} />
        </label>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.groups.form.adminOnly')}</span>
          <Switch checked={adminOnly} onChange={setAdminOnly} label={t('admin.groups.form.adminOnlySwitch')} />
        </label>
        <div className="text-xs text-ink-3">{t('admin.groups.form.adminOnlyHint')}</div>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('admin.groups.form.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{group ? t('admin.groups.form.save') : t('admin.groups.form.create')}</Button>
      </div>
    </Modal>
  )
}
