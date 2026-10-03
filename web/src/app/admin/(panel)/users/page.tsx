/** 管理后台：用户管理（/admin/users）。列表 + 新建/编辑弹层（角色/状态/额度）+ 删除确认
 * + 限时试用额度批量发放（全站生效，入口挂在本页因为它的作用对象就是「全部用户」）；数据经 api/admin.ts 读写。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { createUser, deleteUser, grantTrialQuota, listUsers, updateUser } from '@/api/admin'
import type { AdminUser, CreateUserPayload, UpdateUserPayload } from '@/api/types'
import { STATUS_DISABLED, STATUS_ENABLED } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { roleLabel } from '@/utils/display'
import { formatDateTime } from '@/utils/format'
import { quotaToYuanInput, yuanToQuota, formatYuanFromQuota, formatYuan } from '@/utils/money'
import { useSite } from '@/lib/site/site-context'

const PAGE_SIZE = 20

/** 用户额度 → 输入框回填值：-1 表示不限（契约），其余换算为人民币 */
function userQuotaInput(quota: number | null | undefined, quotaPerYuan: number): string {
  const q = Number(quota ?? 0)
  if (q < 0) return '-1'
  return quotaToYuanInput(q, quotaPerYuan)
}

export default function AdminUsersPage() {
  const { quotaPerYuan } = useSite()
  const [items, setItems] = useState<AdminUser[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<AdminUser | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null)
  // 限时试用发放弹层：独立于单用户编辑，因为它是一次全站批量写操作
  const [trialOpen, setTrialOpen] = useState(false)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listUsers({ page, size: PAGE_SIZE })
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
      await deleteUser(deleteTarget.id)
      toast(t('admin.users.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.users.toast.deleteFailed'))
    }
  }

  const columns: Column<AdminUser>[] = [
    { title: t('admin.users.col.id'), render: (row) => <span className="text-ink-3">#{row.id}</span> },
    { title: t('admin.users.col.username'), render: (row) => <span className="font-medium text-ink">{row.username}</span> },
    { title: t('admin.users.col.email'), render: (row) => <span className="text-ink-2">{row.email || '—'}</span> },
    {
      title: t('admin.users.col.role'),
      render: (row) => <Badge tone={row.role === 10 ? 'brand' : 'off'}>{roleLabel(row.role)}</Badge>,
    },
    {
      title: t('admin.users.col.status'),
      render: (row) => (
        <Badge tone={row.status === STATUS_ENABLED ? 'ok' : 'off'}>
          {row.status === STATUS_ENABLED ? t('admin.users.statusEnabled') : t('admin.users.statusDisabled')}
        </Badge>
      ),
    },
    {
      // 代理标记：一眼看出哪些账号在按批发档看模型广场
      title: t('admin.users.col.agent'),
      render: (row) =>
        row.agent_group ? (
          <Badge tone="warn">{row.agent_group}</Badge>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    {
      title: t('admin.users.col.balance'),
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.quota, quotaPerYuan)}</span>,
    },
    {
      title: t('admin.users.col.used'),
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.used_quota, quotaPerYuan)}</span>,
    },
    {
      title: t('admin.users.col.createdAt'),
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: t('admin.users.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.users.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.users.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.users.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.users.subtitle', { total })}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={() => setTrialOpen(true)}>
            {t('admin.users.grantTrial')}
          </Button>
          <Button variant="primary" onClick={() => setEditing('new')}>
            {t('admin.users.create')}
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.users.emptyTitle')}
          emptyDescription={t('admin.users.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <UserFormModal
        open={editing !== null}
        user={editing === 'new' ? null : editing}
        onClose={() => setEditing(null)}
        onSaved={() => {
          setEditing(null)
          void load()
        }}
      />

      <TrialGrantModal open={trialOpen} onClose={() => setTrialOpen(false)} />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.users.delete.title')}
        message={t('admin.users.delete.message', { name: deleteTarget?.username ?? '' })}
        danger
        confirmText={t('admin.users.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 用户表单弹层 ───────────────────────────────────────── */

function UserFormModal({
  open,
  user,
  onClose,
  onSaved,
}: {
  open: boolean
  user: AdminUser | null
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState(1)
  const [status, setStatus] = useState(STATUS_ENABLED)
  // 额度以人民币录入（元），提交时换算成契约额度
  const [quota, setQuota] = useState('')
  // 代理分组名：非空即该账号在模型广场按此分组的模型与折扣价展示
  const [agentGroup, setAgentGroup] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setUsername(user?.username ?? '')
    setPassword('')
    setEmail(user?.email ?? '')
    setRole(user?.role ?? 1)
    setStatus(user?.status ?? STATUS_ENABLED)
    setQuota(user ? userQuotaInput(user.quota, quotaPerYuan) : '')
    setAgentGroup(user?.agent_group ?? '')
  }, [open, user, quotaPerYuan])

  async function handleSubmit() {
    if (!username.trim()) {
      toastError(t('admin.users.error.usernameRequired'))
      return
    }
    if (!user && !password) {
      toastError(t('admin.users.error.passwordRequired'))
      return
    }
    setLoading(true)
    try {
      if (user) {
        const payload: UpdateUserPayload = {
          email: email.trim() || undefined,
          role,
          status,
          // 恒提交（含空串）：空串 = 取消代理资格，这是后台表单的明确意图
          agent_group: agentGroup.trim(),
        }
        // 额度输入留空视为不修改（避免编辑时误把额度清零）；
        // 输入为人民币，提交时换算成契约额度
        if (quota.trim() !== '') {
          const value = Number(quota)
          if (!Number.isFinite(value)) {
            toastError(t('admin.users.error.quotaNumber'))
            return
          }
          payload.quota = yuanToQuota(value, quotaPerYuan) ?? Math.round(value)
        }
        await updateUser(user.id, payload)
        toast(t('admin.users.toast.updated'))
      } else {
        const payload: CreateUserPayload = {
          username: username.trim(),
          password,
          role,
        }
        if (email.trim()) payload.email = email.trim()
        if (agentGroup.trim()) payload.agent_group = agentGroup.trim()
        await createUser(payload)
        toast(t('admin.users.toast.created'))
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.users.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={user ? t('admin.users.form.editTitle') : t('admin.users.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.users.form.username')} required>
          <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder={t('admin.users.form.usernamePlaceholder')} disabled={Boolean(user)} />
        </Field>

        {!user && (
          <Field label={t('admin.users.form.initialPassword')} required>
            <Input value={password} onChange={(e) => setPassword(e.target.value)} type="password" placeholder={t('admin.users.form.passwordPlaceholder')} autoComplete="new-password" />
          </Field>
        )}

        <Field label={t('admin.users.form.email')}>
          <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t('admin.users.form.emailPlaceholder')} />
        </Field>

        <Field label={t('admin.users.form.role')}>
          <Select value={role} onChange={(e) => setRole(Number(e.target.value))}>
            <option value={1}>{t('admin.users.form.roleUser')}</option>
            <option value={10}>{t('admin.users.form.roleAdmin')}</option>
          </Select>
        </Field>

        {user && (
          <>
            <Field label={t('admin.users.form.status')}>
              <div className="flex items-center justify-between rounded-md border border-line bg-surface px-3 py-2">
                <span className="text-[13px] text-ink-2">{status === STATUS_ENABLED ? t('admin.users.statusEnabled') : t('admin.users.statusDisabled')}</span>
                <Switch checked={status === STATUS_ENABLED} onChange={(v) => setStatus(v ? STATUS_ENABLED : STATUS_DISABLED)} />
              </div>
            </Field>

            <Field label={t('admin.users.form.balance')} help={t('admin.users.form.balanceHelp')}>
              <Input value={quota} onChange={(e) => setQuota(e.target.value)} type="number" placeholder={t('admin.users.form.balancePlaceholder')} />
            </Field>
          </>
        )}

        <Field
          label={t('admin.users.form.agentGroup')}
          help={t('admin.users.form.agentGroupHelp')}
        >
          <Input value={agentGroup} onChange={(e) => setAgentGroup(e.target.value)} placeholder={t('admin.users.form.agentGroupPlaceholder')} />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('common.action.cancel')}
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          {user ? t('admin.users.form.save') : t('admin.users.form.create')}
        </Button>
      </div>
    </Modal>
  )
}

/* ── 限时试用发放弹层 ───────────────────────────────────── */

/** 待提交参数的快照：进入二次确认后冻结表单值。
 *  确认弹层与提交请求都只读这份快照，避免「确认弹层展示的是旧值、提交的却是新值」的错位。 */
interface TrialGrantDraft {
  amountCents: number
  hours: number
  batch: string
}

/** 生成默认批次名（trial-YYYYMMDD-HHmm）。
 *  每次打开弹层重新生成；同一次会话内的重试沿用同一批次——
 *  若上次请求其实已成功（网络层报错但后端已入账），重试会收到「批次已发放」的
 *  明确冲突提示，而不是悄悄再发一份。 */
function defaultBatchName(): string {
  const now = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `trial-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`
}

function TrialGrantModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  // 金额以人民币（元）录入——站长按"发 1 毛钱"思考；提交时换算成后端契约的「分」
  const [amountYuan, setAmountYuan] = useState('')
  const [hours, setHours] = useState('24')
  const [batch, setBatch] = useState('')
  // 非空 = 处于二次确认阶段
  const [pending, setPending] = useState<TrialGrantDraft | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setAmountYuan('')
    setHours('24')
    setBatch(defaultBatchName())
    setPending(null)
  }, [open])

  // 每人额度的预估展示：与后端「分 × 兑换比例 ÷ 100」同口径（quotaPerYuan 即该比例），
  // 让站长在提交前就看到"这笔钱折合多少额度"，而不是发完再去对账；
  // 金额为空或比例未下发时退回中性提示，避免显示出"0 额度"这种误导性数字。
  const estimatedQuota =
    quotaPerYuan > 0 && Number(amountYuan) > 0 ? yuanToQuota(Number(amountYuan), quotaPerYuan) : null

  function handleSubmit() {
    const yuan = Number(amountYuan)
    if (!Number.isFinite(yuan) || yuan <= 0) {
      toastError(t('admin.users.error.amountInvalid'))
      return
    }
    const amountCents = Math.round(yuan * 100)
    if (amountCents <= 0) {
      toastError(t('admin.users.error.amountTooSmall'))
      return
    }
    const h = Number(hours)
    if (!Number.isInteger(h) || h < 1 || h > 720) {
      toastError(t('admin.users.error.hoursInvalid'))
      return
    }
    if (!batch.trim()) {
      toastError(t('admin.users.error.batchRequired'))
      return
    }
    // 冻结参数进入二次确认：发出去就进了用户余额，只能等到期才收得回，
    // 这一步的代价足够高，值得多一次明确确认。
    setPending({ amountCents, hours: h, batch: batch.trim() })
  }

  async function handleConfirm() {
    if (!pending) return
    setLoading(true)
    try {
      // confirm:true 是后端的防误触闸门——能走到这里说明站长已在弹层里看过完整摘要；
      // 其余字段由冻结的快照展开，命名按后端契约（snake_case）。
      const result = await grantTrialQuota({
        amount_cents: pending.amountCents,
        hours: pending.hours,
        batch: pending.batch,
        confirm: true,
      })
      toast(t('admin.users.toast.granted', { count: result.recipients, batch: result.batch, hours: result.hours }))
      setPending(null)
      onClose()
    } catch (err) {
      // 后端会给出具体原因（批次已发放 / 未配置兑换比例 / 无符合条件用户等），原样展示；
      // 关掉确认弹层回到表单，让站长能改批次名或金额后重试。
      toastError(err instanceof Error ? err.message : t('admin.users.toast.grantFailed'))
      setPending(null)
    } finally {
      setLoading(false)
    }
  }

  // 确认弹层里的额度预估：与表单 help 用同一换算函数，保证两处数字对得上；
  // 比例未下发时退化为空串（后端会按自己的兑换比例入账，前端不乱猜数字）。
  const confirmQuotaText =
    pending && quotaPerYuan > 0
      ? t('admin.users.trial.confirmQuota', { quota: (yuanToQuota(pending.amountCents / 100, quotaPerYuan) ?? 0).toLocaleString('zh-CN') })
      : ''

  return (
    <>
      <Modal open={open} onClose={onClose} title={t('admin.users.trial.title')} width={560}>
        <div className="space-y-4">
          {/* 先把代价说清楚再让人填数：这是全站批量加钱，不是单个用户的额度调整 */}
          <div className="rounded-md border border-warn/30 bg-warn/8 px-3 py-2 text-[13px] leading-relaxed text-ink-2">
            {t('admin.users.trial.warningPre')}
            <b>{t('admin.users.trial.warningStrong')}</b>
            {t('admin.users.trial.warningSuffix')}
          </div>

          <Field
            label={t('admin.users.trial.amount')}
            required
            help={
              estimatedQuota !== null
                ? t('admin.users.trial.amountHelpEstimated', {
                    quota: estimatedQuota.toLocaleString('zh-CN'),
                    rate: quotaPerYuan.toLocaleString('zh-CN'),
                  })
                : t('admin.users.trial.amountHelp')
            }
          >
            <Input
              value={amountYuan}
              onChange={(e) => setAmountYuan(e.target.value)}
              type="number"
              min="0.01"
              step="0.01"
              placeholder={t('admin.users.trial.amountPlaceholder')}
            />
          </Field>

          <Field label={t('admin.users.trial.hours')} required help={t('admin.users.trial.hoursHelp')}>
            <Input
              value={hours}
              onChange={(e) => setHours(e.target.value)}
              type="number"
              min="1"
              max="720"
              step="1"
            />
          </Field>

          <Field label={t('admin.users.trial.batch')} required help={t('admin.users.trial.batchHelp')}>
            <Input value={batch} onChange={(e) => setBatch(e.target.value)} placeholder={t('admin.users.trial.batchPlaceholder')} />
          </Field>
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            {t('common.action.cancel')}
          </Button>
          <Button variant="primary" onClick={handleSubmit}>
            {t('admin.users.trial.next')}
          </Button>
        </div>
      </Modal>

      <ConfirmDialog
        open={pending !== null}
        title={t('admin.users.trial.confirmTitle')}
        danger
        confirmText={t('admin.users.trial.confirmText')}
        loading={loading}
        onConfirm={handleConfirm}
        onCancel={() => setPending(null)}
        message={
          pending
            ? t('admin.users.trial.confirmMessage', {
                amount: formatYuan(pending.amountCents / 100),
                quota: confirmQuotaText,
                hours: pending.hours,
                batch: pending.batch,
              })
            : ''
        }
      />
    </>
  )
}