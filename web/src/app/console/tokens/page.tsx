/** 用户门户：访问令牌页（/console/tokens）。
 *
 * 意图（Why）：
 *   创建/管理访问令牌。创建后一次性明文 key 必须在弹层显著提示保存（无法二次取回）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { createMyToken, deleteMyToken, getMyTokenKey, listMyGroups, listMyTokens, updateMyToken } from '@/api/portal'
import type { AccessToken, CreateTokenPayload, CreateTokenResult, PortalGroup } from '@/api/types'
import { Button } from '@/components/ui/Button'
import { Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Field, Input, Select } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { Badge, EmptyState } from '@/components/ui/Display'
import { CopyButton } from '@/components/ui/Modal'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime } from '@/utils/format'
import { formatDiscountLabel, formatYuanFromQuota, yuanToQuota } from '@/utils/money'

const PAGE_SIZE = 20

export default function ConsoleTokensPage() {
  const { quotaPerYuan } = useSite()
  const [items, setItems] = useState<AccessToken[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [createOpen, setCreateOpen] = useState(false)
  const [created, setCreated] = useState<CreateTokenResult | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AccessToken | null>(null)
  const [groups, setGroups] = useState<PortalGroup[]>([])

  const { toast, toastError } = useToast()

  /** 明文密钥查看态：点击「查看」后调接口取回原文并展示 + 一键复制 */
  const [revealed, setRevealed] = useState<Record<number, string>>({})
  const [revealing, setRevealing] = useState<number | null>(null)

  async function handleRevealKey(token: AccessToken) {
    // 已取回的直接显示（无需重复请求）；未取回的调用取明文接口
    if (revealed[token.id]) {
      setRevealed((prev) => {
        const next = { ...prev }
        delete next[token.id] // 再次点击 = 收起（回到掩码）
        return next
      })
      return
    }
    setRevealing(token.id)
    try {
      const data = await getMyTokenKey(token.id)
      setRevealed((prev) => ({ ...prev, [token.id]: data.key }))
    } catch (err) {
      toastError(err instanceof Error ? err.message : '获取密钥失败')
    } finally {
      setRevealing(null)
    }
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listMyTokens({ page, size: PAGE_SIZE })
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

  useEffect(() => {
    void listMyGroups().then((data) => setGroups(data.items)).catch(() => setGroups([]))
  }, [])

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteMyToken(deleteTarget.id)
      toast('令牌已删除')
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  async function handleToggle(token: AccessToken) {
    try {
      await updateMyToken(token.id, { status: token.status === 1 ? 2 : 1 })
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '操作失败')
    }
  }

  const columns: Column<AccessToken>[] = [
    { title: '名称', render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    {
      title: '密钥',
      render: (row) => {
        const shown = revealed[row.id]
        return (
          <span className="flex items-center gap-2 font-mono text-xs text-ink-2">
            <span className={shown ? 'text-ink' : ''}>{shown || row.masked_key}</span>
            <button
              type="button"
              onClick={() => void handleRevealKey(row)}
              disabled={revealing === row.id}
              className="text-[13px] text-ink-3 transition hover:text-brand disabled:opacity-50"
            >
              {revealing === row.id ? '…' : shown ? '收起' : '查看原文'}
            </button>
            {shown && <CopyButton text={shown} label="复制" />}
          </span>
        )
      },
    },
    { title: '状态', render: (row) => (row.status === 1 ? <Badge tone="ok">启用</Badge> : <Badge tone="off">停用</Badge>) },
    {
      title: '剩余',
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.unlimited_quota ? '不限' : formatYuanFromQuota(row.remain_quota, quotaPerYuan)}
        </span>
      ),
    },
    {
      title: '已用',
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.used_quota, quotaPerYuan)}</span>,
    },
    {
      title: '周期预算',
      width: 'w-48',
      render: (row) => <BudgetCell token={row} quotaPerYuan={quotaPerYuan} />,
    },
    { title: '到期', render: (row) => <span className="text-ink-2">{row.expires_at ? formatDateTime(row.expires_at) : '永不过期'}</span> },
    {
      title: '操作',
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2">
          <button type="button" onClick={() => handleToggle(row)} className="text-[13px] text-ink-3 hover:text-brand">
            {row.status === 1 ? '停用' : '启用'}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-[13px] text-ink-3 hover:text-err">
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
          <h1 className="text-xl font-bold text-ink">访问令牌</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">用于调用本站 API 的密钥</p>
        </div>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          新建令牌
        </Button>
      </div>

      <Card padding="none">
        <DataTable columns={columns} rows={loading ? null : items} loading={loading} rowKey={(row) => row.id} emptyTitle="还没有令牌" />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <CreateTokenModal
        open={createOpen}
        groups={groups}
        onClose={() => setCreateOpen(false)}
        onCreated={(result) => {
          setCreated(result)
          setCreateOpen(false)
          void load()
        }}
      />

      {/* 一次性明文展示：最关键的提示 */}
      <Modal open={Boolean(created)} onClose={() => setCreated(null)} title="令牌已创建" width={480}>
        <div className="rounded-md border border-warn/30 bg-warn/8 px-3 py-2.5 text-[13px] text-warn">
          明文密钥只在此时显示一次，请立即保存。关闭后将无法再次查看。
        </div>
        <div className="mt-3 flex items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2.5 font-mono text-sm text-ink">
          <span className="break-all">{created?.key}</span>
          <CopyButton text={created?.key ?? ''} label="复制" />
        </div>
        <div className="mt-4 flex justify-end">
          <Button variant="primary" onClick={() => setCreated(null)}>
            我已保存
          </Button>
        </div>
      </Modal>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title="删除令牌"
        message={`确认删除「${deleteTarget?.name}」？删除后使用该令牌的请求将立即失效。`}
        danger
        confirmText="删除"
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function CreateTokenModal({
  open,
  groups,
  onClose,
  onCreated,
}: {
  open: boolean
  groups: PortalGroup[]
  onClose: () => void
  onCreated: (result: CreateTokenResult) => void
}) {
  const { toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const [name, setName] = useState('')
  const [group, setGroup] = useState('')
  const [unlimited, setUnlimited] = useState(false)
  const [quotaYuan, setQuotaYuan] = useState('10') // 令牌独立预算（人民币，元）
  const [expiresInDays, setExpiresInDays] = useState(0)
  const [loading, setLoading] = useState(false)

  async function handleSubmit() {
    if (!name.trim()) {
      toastError('请填写令牌名称')
      return
    }
    const q = Number(quotaYuan)
    if (!unlimited && (!Number.isFinite(q) || q < 0)) {
      toastError('请填写正确的令牌预算（元）')
      return
    }
    setLoading(true)
    try {
      const payload: CreateTokenPayload = {
        name: name.trim(),
        expires_in_days: expiresInDays,
        models: [],
        unlimited_quota: unlimited,
        remain_quota: unlimited ? 0 : (yuanToQuota(q, quotaPerYuan) ?? 0), // 人民币 → 额度（内部整数记账）
        group_name: group || undefined,
      }
      const result = await createMyToken(payload)
      onCreated(result)
      setName('')
      setGroup('')
      setUnlimited(false)
      setQuotaYuan('10')
      setExpiresInDays(0)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '创建失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="新建访问令牌" width={520}>
      <div className="space-y-4">
        <Field label="令牌名称" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：本地客户端" />
        </Field>
        <Field label="所属分组" help={groups.length ? '选择分组后按该分组路由与计费' : '加载中…'}>
          <Select value={group} onChange={(e) => setGroup(e.target.value)}>
            <option value="">默认分组</option>
            {groups.map((g) => (
              <option key={g.name} value={g.name} disabled={!g.unlocked}>
                {g.label}
                {g.is_agent ? `（代理拿货 · ${formatDiscountLabel(g.ratio)}）` : ''}
                {!g.unlocked ? '（未解锁）' : ''}
              </option>
            ))}
          </Select>
        </Field>
        {/* 分组 RPM 提示：仅当该分组下发且 >0 时显示（0/缺失 = 不限速，不显示） */}
        {(() => {
          const selected = groups.find((g) => g.name === group)
          if (!selected?.rpm_limit || selected.rpm_limit <= 0) return null
          return (
            <p className="-mt-2 text-[12px] text-ink-3">
              该分组每分钟请求上限：{selected.rpm_limit} 次/分钟
            </p>
          )
        })()}
        <Field label="有效期" help="0 表示永不过期">
          <Input type="number" min={0} value={expiresInDays} onChange={(e) => setExpiresInDays(Number(e.target.value))} />
        </Field>
        <Field label="令牌预算（¥）" help={unlimited ? '不限预算时无需填写' : '该令牌可消耗的余额上限，超出后该令牌将停止响应'}>
          <Input
            type="number"
            min={0}
            value={quotaYuan}
            onChange={(e) => setQuotaYuan(e.target.value)}
            placeholder="10"
            disabled={unlimited}
          />
        </Field>
        <label className="flex items-center gap-2 text-[13px] text-ink-2">
          <input type="checkbox" checked={unlimited} onChange={(e) => setUnlimited(e.target.checked)} className="h-4 w-4 accent-brand" />
          不限预算
        </label>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>取消</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>创建</Button>
      </div>
    </Modal>
  )
}

/* ── 周期预算展示（只读）──────────────────────────────────
 *
 * 后端令牌模型已有 budget_quota / budget_period / budget_window_start /
 * budget_window_base 四个字段（见 internal/model/token.go，迁移 0043）。
 * 本窗口已消耗 = used_quota − budget_window_base（负数按 0），与后端口径一致。
 *
 * 注意：tokenDTO 目前尚未把这四个字段下发给前端，因此此处对每行做判空：
 * 后端补齐后本列会自动显示进度；未配置/未下发时显示「未设置」。
 */

/** 预算周期标识 → 中文短标签 */
function budgetPeriodLabel(period: string): string {
  switch (period) {
    case 'daily':
      return '每日'
    case 'weekly':
      return '每周'
    case 'monthly':
      return '每月'
    default:
      return period
  }
}

/** 单行的周期预算：进度条 + 本周期已用/上限 */
function BudgetCell({ token, quotaPerYuan }: { token: AccessToken; quotaPerYuan: number }) {
  const limit = token.budget_quota ?? 0
  const period = token.budget_period ?? ''
  if (limit <= 0 || !period) {
    return <span className="text-[12px] text-ink-3">未设置</span>
  }
  const used = Math.max(0, (token.used_quota ?? 0) - (token.budget_window_base ?? 0))
  const pct = Math.min(100, Math.round((used / limit) * 100))
  const tone = pct >= 100 ? 'bg-err' : pct >= 80 ? 'bg-warn' : 'bg-brand'
  return (
    <div className="min-w-[8rem]">
      <div className="flex items-center justify-between gap-2 text-[11px] text-ink-3">
        <span>{budgetPeriodLabel(period)}</span>
        <span className="font-mono tabular-nums">
          {formatYuanFromQuota(used, quotaPerYuan)} / {formatYuanFromQuota(limit, quotaPerYuan)}
        </span>
      </div>
      <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-ink/10">
        <div className={`h-full rounded-full ${tone}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}