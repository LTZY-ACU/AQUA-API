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
      toast('分组已删除')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  const columns: Column<GroupWithRpm>[] = [
    { title: '标识', render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    { title: '名称', render: (row) => <span className="text-ink-2">{row.label}</span> },
    {
      title: '倍率',
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.ratio} = {formatNumber(row.ratio / 100)} 倍
        </span>
      ),
    },
    {
      title: 'RPM',
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.rpm_limit && row.rpm_limit > 0 ? `${formatNumber(row.rpm_limit)} / 分` : '不限'}
        </span>
      ),
    },
    {
      title: '描述',
      render: (row) => (
        <span className="max-w-56 truncate text-[13px] text-ink-3" title={row.description || undefined}>
          {row.description || '—'}
        </span>
      ),
    },
    {
      title: '状态',
      render: (row) => (row.enabled ? <Badge tone="ok">启用</Badge> : <Badge tone="off">停用</Badge>),
    },
    {
      title: '引用',
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.channel_count} 渠道 · {row.price_count} 价格
        </span>
      ),
    },
    {
      title: '解锁门槛',
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.unlock_min_recharge_cents > 0 ? `¥${formatNumber(centsToYuan(row.unlock_min_recharge_cents))}` : '无门槛'}
        </span>
      ),
    },
    {
      title: '批发',
      render: (row) => (row.admin_only ? <Badge tone="brand">批发价</Badge> : <span className="text-ink-3">—</span>),
    },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            编辑
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            删除
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">模型分组</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">计费倍率与解锁门槛（共 {total} 个分组）</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>新建分组</Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="还没有模型分组"
          emptyDescription="新建一个分组来定义计费倍率与解锁门槛"
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
        title="删除分组"
        message={
          deleteTarget
            ? `确认删除分组「${deleteTarget.name}」？当前被 ${deleteTarget.channel_count} 个渠道、${deleteTarget.price_count} 条计价规则引用，仍被引用时后端会拒绝删除。`
            : undefined
        }
        danger
        confirmText="删除"
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
      toastError('请填写分组标识')
      return
    }
    const ratioValue = Number(ratio)
    if (!Number.isFinite(ratioValue) || ratioValue < 0) {
      toastError('倍率必须是 ≥0 的数字（100 = 1.0 倍）')
      return
    }
    const cents = unlockYuan.trim() === '' ? 0 : yuanToCents(unlockYuan)
    if (cents === null) {
      toastError('解锁门槛必须是 ≥0 的数字（元）')
      return
    }
    // RPM 是每分钟请求上限的整数计数，0 表示不限；小数没有意义
    const rpmValue = rpmLimit.trim() === '' ? 0 : Number(rpmLimit)
    if (!Number.isInteger(rpmValue) || rpmValue < 0) {
      toastError('RPM 上限必须是 ≥0 的整数（0 = 不限）')
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
        toast('分组已更新')
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
        toast('分组已创建')
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={group ? '编辑分组' : '新建分组'} width={560}>
      <div className="space-y-4">
        <Field label="分组标识" required={!group} help={group ? '标识创建后不可修改' : '小写字母/数字/连字符，创建后不可修改'}>
          <Input value={name} onChange={(e) => setName(e.target.value)} disabled={Boolean(group)} placeholder="如 vip / free" />
        </Field>

        <Field label="展示名称" help="留空则显示分组标识">
          <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="如 VIP 会员" />
        </Field>

        <Field label="计费倍率" help="100 = 1.0 倍，150 = 1.5 倍；实际扣费 = 基础额度 × ratio / 100">
          <Input value={ratio} onChange={(e) => setRatio(e.target.value)} type="number" min={0} step="1" placeholder="100" />
        </Field>

        <Field label="RPM 上限" help="本分组每分钟允许的请求数上限；0 = 不限（默认）">
          <Input value={rpmLimit} onChange={(e) => setRpmLimit(e.target.value)} type="number" min={0} step="1" placeholder="0" />
        </Field>

        <Field label="描述">
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={3} placeholder="这个分组是给谁用的？" />
        </Field>

        <Field label="解锁门槛（元）" help="用户累计充值达到该金额才可解锁本分组；0 = 无门槛">
          <Input value={unlockYuan} onChange={(e) => setUnlockYuan(e.target.value)} type="number" min={0} step="0.01" placeholder="0" />
        </Field>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>启用分组</span>
          <Switch checked={enabled} onChange={setEnabled} label="启用分组" />
        </label>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>仅管理员分发（批发价）</span>
          <Switch checked={adminOnly} onChange={setAdminOnly} label="仅管理员分发" />
        </label>
        <div className="text-xs text-ink-3">开启后门户不下发该分组，只能由后台代建令牌时指定。</div>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>取消</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{group ? '保存' : '创建'}</Button>
      </div>
    </Modal>
  )
}
