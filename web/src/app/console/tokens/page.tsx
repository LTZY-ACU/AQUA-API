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
import { translate, useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime } from '@/utils/format'
import { formatDiscountLabel, formatYuanFromQuota, yuanToQuota } from '@/utils/money'

const PAGE_SIZE = 20

export default function ConsoleTokensPage() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
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
      toastError(err instanceof Error ? err.message : t('portal.tokens.revealFailed'))
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
      toast(t('portal.tokens.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('portal.tokens.deleteFailed'))
    }
  }

  async function handleToggle(token: AccessToken) {
    try {
      await updateMyToken(token.id, { status: token.status === 1 ? 2 : 1 })
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('portal.tokens.toggleFailed'))
    }
  }

  const columns: Column<AccessToken>[] = [
    { title: t('portal.tokens.colName'), render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    {
      title: t('portal.tokens.colKey'),
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
              {revealing === row.id ? '…' : shown ? t('portal.tokens.collapse') : t('portal.tokens.reveal')}
            </button>
            {shown && <CopyButton text={shown} label={t('common.action.copy')} />}
          </span>
        )
      },
    },
    { title: t('portal.tokens.colStatus'), render: (row) => (row.status === 1 ? <Badge tone="ok">{t('portal.tokens.enabled')}</Badge> : <Badge tone="off">{t('portal.tokens.disabled')}</Badge>) },
    {
      title: t('portal.tokens.colRemaining'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.unlimited_quota ? t('portal.overview.unlimited') : formatYuanFromQuota(row.remain_quota, quotaPerYuan)}
        </span>
      ),
    },
    {
      title: t('portal.tokens.colUsed'),
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.used_quota, quotaPerYuan)}</span>,
    },
    {
      title: t('portal.tokens.colBudget'),
      width: 'w-48',
      render: (row) => <BudgetCell token={row} quotaPerYuan={quotaPerYuan} />,
    },
    { title: t('portal.tokens.colExpires'), render: (row) => <span className="text-ink-2">{row.expires_at ? formatDateTime(row.expires_at) : t('portal.tokens.neverExpires')}</span> },
    {
      title: t('portal.tokens.colActions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2">
          <button type="button" onClick={() => handleToggle(row)} className="text-[13px] text-ink-3 hover:text-brand">
            {row.status === 1 ? t('portal.tokens.disabled') : t('portal.tokens.enabled')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-[13px] text-ink-3 hover:text-err">
            {t('common.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('portal.tokens.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('portal.tokens.subtitle')}</p>
        </div>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          {t('portal.tokens.create')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable columns={columns} rows={loading ? null : items} loading={loading} rowKey={(row) => row.id} emptyTitle={t('portal.tokens.empty')} />
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
      <Modal open={Boolean(created)} onClose={() => setCreated(null)} title={t('portal.tokens.createdTitle')} width={480}>
        <div className="rounded-md border border-warn/30 bg-warn/8 px-3 py-2.5 text-[13px] text-warn">
          {t('portal.tokens.createdWarning')}
        </div>
        <div className="mt-3 flex items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2.5 font-mono text-sm text-ink">
          <span className="break-all">{created?.key}</span>
          <CopyButton text={created?.key ?? ''} label={t('common.action.copy')} />
        </div>
        <div className="mt-4 flex justify-end">
          <Button variant="primary" onClick={() => setCreated(null)}>
            {t('portal.tokens.saved')}
          </Button>
        </div>
      </Modal>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('portal.tokens.deleteTitle')}
        message={t('portal.tokens.deleteMessage', { name: deleteTarget?.name ?? '' })}
        danger
        confirmText={t('common.action.delete')}
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
  const { t } = useI18n()
  const [name, setName] = useState('')
  const [group, setGroup] = useState('')
  const [unlimited, setUnlimited] = useState(false)
  const [quotaYuan, setQuotaYuan] = useState('10') // 令牌独立预算（人民币，元）
  const [expiresInDays, setExpiresInDays] = useState(0)
  const [loading, setLoading] = useState(false)

  async function handleSubmit() {
    if (!name.trim()) {
      toastError(t('portal.tokens.nameRequired'))
      return
    }
    const q = Number(quotaYuan)
    if (!unlimited && (!Number.isFinite(q) || q < 0)) {
      toastError(t('portal.tokens.budgetRequired'))
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
      toastError(err instanceof Error ? err.message : t('portal.tokens.createFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t('portal.tokens.createTitle')} width={520}>
      <div className="space-y-4">
        <Field label={t('portal.tokens.name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('portal.tokens.namePlaceholder')} />
        </Field>
        <Field label={t('portal.tokens.group')} help={groups.length ? t('portal.tokens.groupHelp') : t('common.state.loading')}>
          <Select value={group} onChange={(e) => setGroup(e.target.value)}>
            <option value="">{t('portal.tokens.defaultGroup')}</option>
            {groups.map((g) => (
              <option key={g.name} value={g.name} disabled={!g.unlocked}>
                {g.label}
                {g.is_agent ? t('portal.tokens.agentGroup', { label: formatDiscountLabel(g.ratio) }) : ''}
                {!g.unlocked ? t('portal.tokens.locked') : ''}
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
              {t('portal.tokens.rpmHint', { n: selected.rpm_limit })}
            </p>
          )
        })()}
        <Field label={t('portal.tokens.expires')} help={t('portal.tokens.expiresHelp')}>
          <Input type="number" min={0} value={expiresInDays} onChange={(e) => setExpiresInDays(Number(e.target.value))} />
        </Field>
        <Field label={t('portal.tokens.budget')} help={unlimited ? t('portal.tokens.budgetUnlimitedHelp') : t('portal.tokens.budgetHelp')}>
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
          {t('portal.tokens.unlimitedBudget')}
        </label>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('common.action.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{t('common.action.create')}</Button>
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

/** 预算周期标识 → 本地化短标签（纯函数，不能在模块顶层求值，否则语言被锁死） */
function budgetPeriodLabel(period: string): string {
  switch (period) {
    case 'daily':
      return translate('portal.tokens.periodDaily')
    case 'weekly':
      return translate('portal.tokens.periodWeekly')
    case 'monthly':
      return translate('portal.tokens.periodMonthly')
    default:
      return period
  }
}

/** 单行的周期预算：进度条 + 本周期已用/上限 */
function BudgetCell({ token, quotaPerYuan }: { token: AccessToken; quotaPerYuan: number }) {
  const { t } = useI18n()
  const limit = token.budget_quota ?? 0
  const period = token.budget_period ?? ''
  if (limit <= 0 || !period) {
    return <span className="text-[12px] text-ink-3">{t('portal.tokens.notSet')}</span>
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