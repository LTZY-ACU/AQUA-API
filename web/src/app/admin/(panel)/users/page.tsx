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
      toast('用户已删除')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  const columns: Column<AdminUser>[] = [
    { title: 'ID', render: (row) => <span className="text-ink-3">#{row.id}</span> },
    { title: '用户名', render: (row) => <span className="font-medium text-ink">{row.username}</span> },
    { title: '邮箱', render: (row) => <span className="text-ink-2">{row.email || '—'}</span> },
    {
      title: '角色',
      render: (row) => <Badge tone={row.role === 10 ? 'brand' : 'off'}>{roleLabel(row.role)}</Badge>,
    },
    {
      title: '状态',
      render: (row) => (
        <Badge tone={row.status === STATUS_ENABLED ? 'ok' : 'off'}>
          {row.status === STATUS_ENABLED ? '启用' : '停用'}
        </Badge>
      ),
    },
    {
      // 代理标记：一眼看出哪些账号在按批发档看模型广场
      title: '代理',
      render: (row) =>
        row.agent_group ? (
          <Badge tone="warn">{row.agent_group}</Badge>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
    {
      title: '余额',
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.quota, quotaPerYuan)}</span>,
    },
    {
      title: '已用',
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.used_quota, quotaPerYuan)}</span>,
    },
    {
      title: '注册时间',
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
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
          <h1 className="text-xl font-bold text-ink">用户管理</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">平台全部注册用户（{total}）</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={() => setTrialOpen(true)}>
            发放试用
          </Button>
          <Button variant="primary" onClick={() => setEditing('new')}>
            新建用户
          </Button>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle="还没有用户"
          emptyDescription="注册用户会出现在这里"
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
        title="删除用户"
        message={`确认删除用户「${deleteTarget?.username}」？该用户的令牌将一并失效。`}
        danger
        confirmText="删除"
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
      toastError('请填写用户名')
      return
    }
    if (!user && !password) {
      toastError('请设置初始密码')
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
            toastError('额度需为数字')
            return
          }
          payload.quota = yuanToQuota(value, quotaPerYuan) ?? Math.round(value)
        }
        await updateUser(user.id, payload)
        toast('用户已更新')
      } else {
        const payload: CreateUserPayload = {
          username: username.trim(),
          password,
          role,
        }
        if (email.trim()) payload.email = email.trim()
        if (agentGroup.trim()) payload.agent_group = agentGroup.trim()
        await createUser(payload)
        toast('用户已创建')
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={user ? '编辑用户' : '新建用户'} width={560}>
      <div className="space-y-4">
        <Field label="用户名" required>
          <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="登录用户名" disabled={Boolean(user)} />
        </Field>

        {!user && (
          <Field label="初始密码" required>
            <Input value={password} onChange={(e) => setPassword(e.target.value)} type="password" placeholder="设置登录密码" autoComplete="new-password" />
          </Field>
        )}

        <Field label="邮箱">
          <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="选填" />
        </Field>

        <Field label="角色">
          <Select value={role} onChange={(e) => setRole(Number(e.target.value))}>
            <option value={1}>普通用户</option>
            <option value={10}>管理员</option>
          </Select>
        </Field>

        {user && (
          <>
            <Field label="状态">
              <div className="flex items-center justify-between rounded-md border border-line bg-surface px-3 py-2">
                <span className="text-[13px] text-ink-2">{status === STATUS_ENABLED ? '启用' : '停用'}</span>
                <Switch checked={status === STATUS_ENABLED} onChange={(v) => setStatus(v ? STATUS_ENABLED : STATUS_DISABLED)} />
              </div>
            </Field>

            <Field label="余额（¥）" help="用户当前的可用余额（元）；-1 = 不限；留空表示不修改">
              <Input value={quota} onChange={(e) => setQuota(e.target.value)} type="number" placeholder="留空表示不修改" />
            </Field>
          </>
        )}

        <Field
          label="代理分组"
          help="填分组标识（如 agent）后，该账号在模型广场只看到该分组下的模型与代理折扣价；留空 = 普通用户"
        >
          <Input value={agentGroup} onChange={(e) => setAgentGroup(e.target.value)} placeholder="留空 = 普通用户" />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          取消
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          {user ? '保存' : '创建'}
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
      toastError('发放金额需为大于 0 的数字（元）')
      return
    }
    const amountCents = Math.round(yuan * 100)
    if (amountCents <= 0) {
      toastError('金额过小：每位用户最小发放 0.01 元')
      return
    }
    const h = Number(hours)
    if (!Number.isInteger(h) || h < 1 || h > 720) {
      toastError('有效时长需为 1 到 720 之间的整数小时（上限 30 天）')
      return
    }
    if (!batch.trim()) {
      toastError('请填写批次标识')
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
      toast(`已向 ${result.recipients} 位用户发放试用额（批次 ${result.batch}，${result.hours} 小时后到期）`)
      setPending(null)
      onClose()
    } catch (err) {
      // 后端会给出具体原因（批次已发放 / 未配置兑换比例 / 无符合条件用户等），原样展示；
      // 关掉确认弹层回到表单，让站长能改批次名或金额后重试。
      toastError(err instanceof Error ? err.message : '发放失败')
      setPending(null)
    } finally {
      setLoading(false)
    }
  }

  // 确认弹层里的额度预估：与表单 help 用同一换算函数，保证两处数字对得上；
  // 比例未下发时退化为空串（后端会按自己的兑换比例入账，前端不乱猜数字）。
  const confirmQuotaText =
    pending && quotaPerYuan > 0
      ? `（约 ${yuanToQuota(pending.amountCents / 100, quotaPerYuan)?.toLocaleString('zh-CN')} 额度）`
      : ''

  return (
    <>
      <Modal open={open} onClose={onClose} title="发放限时试用额度" width={560}>
        <div className="space-y-4">
          {/* 先把代价说清楚再让人填数：这是全站批量加钱，不是单个用户的额度调整 */}
          <div className="rounded-md border border-warn/30 bg-warn/8 px-3 py-2 text-[13px] leading-relaxed text-ink-2">
            这是一次<b>全站批量</b>操作：向所有「启用且额度非不限」的用户发放等额试用余额，
            发出后只能等到期（后台自动回收），无法提前撤销。
          </div>

          <Field
            label="发放金额（元 / 每位用户）"
            required
            help={
              estimatedQuota !== null
                ? `约合每人 ${estimatedQuota.toLocaleString('zh-CN')} 额度（按 1 元 = ${quotaPerYuan.toLocaleString('zh-CN')} 额度折算）`
                : '填写金额后按站点兑换比例折算成额度'
            }
          >
            <Input
              value={amountYuan}
              onChange={(e) => setAmountYuan(e.target.value)}
              type="number"
              min="0.01"
              step="0.01"
              placeholder="如 0.1"
            />
          </Field>

          <Field label="有效时长（小时）" required help="1 到 720 小时（上限 30 天）；到期后由后台自动收回余额">
            <Input
              value={hours}
              onChange={(e) => setHours(e.target.value)}
              type="number"
              min="1"
              max="720"
              step="1"
            />
          </Field>

          <Field label="批次标识" required help="同一批次只会发放一次；重复提交同名批次会被拒绝（防误发双份）">
            <Input value={batch} onChange={(e) => setBatch(e.target.value)} placeholder="如 trial-20261002-1430" />
          </Field>
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" onClick={handleSubmit}>
            下一步，核对摘要
          </Button>
        </div>
      </Modal>

      <ConfirmDialog
        open={pending !== null}
        title="确认发放试用额"
        danger
        confirmText="确认发放"
        loading={loading}
        onConfirm={handleConfirm}
        onCancel={() => setPending(null)}
        message={
          pending
            ? `即将向全站「启用且额度非不限」的用户每人发放 ${formatYuan(pending.amountCents / 100)}${confirmQuotaText}，${pending.hours} 小时后到期收回。批次「${pending.batch}」只能发放一次，确认执行？`
            : ''
        }
      />
    </>
  )
}