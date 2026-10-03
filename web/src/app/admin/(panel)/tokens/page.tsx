/** 管理后台：令牌管理（/admin/tokens）。列表 + 新建弹层（含一次性明文 key 提示保存）+ 编辑（状态/额度/周期预算）+ 删除；数据经 api/admin.ts 读写 /api/admin/tokens。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { createTokenForUser, deleteToken, getTokenKey, listAllTokens, updateToken } from '@/api/admin'
import type { AccessToken, AdminUpdateTokenPayload, CreateTokenPayload } from '@/api/types'
import { STATUS_DISABLED, STATUS_ENABLED } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch } from '@/components/ui/Form'
import { Modal, ConfirmDialog, CopyButton } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatExpiry, parseModelList } from '@/utils/format'
import { formatYuanFromQuota, quotaToYuanInput, yuanToQuota } from '@/utils/money'

const PAGE_SIZE = 20

/**
 * 令牌 + 周期预算字段。
 *
 * 契约：后端 model.Token 已有 budget_quota / budget_period（迁移 0043），
 * 但 tokenDTO / 更新请求体尚未透出，故此处以交叉类型局部承载：
 * 响应里没有这两个字段时按“未启用预算”处理，请求体多带它们（后端落地后即生效）。
 */
type TokenWithBudget = AccessToken & {
  /** 周期预算额度（内部单位）；0 = 不限 */
  budget_quota?: number
  /** 预算周期：'' / 'daily' / 'weekly' / 'monthly' */
  budget_period?: string
}

/** 周期预算的下拉选项：值必须与后端 model.BudgetPeriod* 常量一致 */
const BUDGET_PERIOD_OPTIONS = [
  { value: '', labelKey: 'admin.tokens.form.periodDisable' },
  { value: 'daily', labelKey: 'admin.tokens.form.periodDaily' },
  { value: 'weekly', labelKey: 'admin.tokens.form.periodWeekly' },
  { value: 'monthly', labelKey: 'admin.tokens.form.periodMonthly' },
] as const

/** 周期标识 → 词条键（列表展示用） */
const BUDGET_PERIOD_LABEL: Record<string, string> = {
  daily: 'admin.tokens.period.daily',
  weekly: 'admin.tokens.period.weekly',
  monthly: 'admin.tokens.period.monthly',
}

export default function AdminTokensPage() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [items, setItems] = useState<TokenWithBudget[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<TokenWithBudget | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<TokenWithBudget | null>(null)
  const { toast, toastError } = useToast()

  /** 明文密钥查看态：点击「查看」后调接口取回原文并展示 + 一键复制 */
  const [revealed, setRevealed] = useState<Record<number, string>>({})
  const [revealing, setRevealing] = useState<number | null>(null)

  async function handleRevealKey(token: AccessToken) {
    if (revealed[token.id]) {
      setRevealed((prev) => {
        const next = { ...prev }
        delete next[token.id]
        return next
      })
      return
    }
    setRevealing(token.id)
    try {
      const data = await getTokenKey(token.id)
      setRevealed((prev) => ({ ...prev, [token.id]: data.key }))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.tokens.toast.revealFailed'))
    } finally {
      setRevealing(null)
    }
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listAllTokens({ page, size: PAGE_SIZE })
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
      await deleteToken(deleteTarget.id)
      toast(t('admin.tokens.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.tokens.toast.deleteFailed'))
    }
  }

  const columns: Column<TokenWithBudget>[] = [
    { title: t('admin.tokens.col.id'), render: (row) => <span className="text-ink-3">#{row.id}</span> },
    { title: t('admin.tokens.col.name'), render: (row) => <span className="font-medium text-ink">{row.name}</span> },
    {
      title: t('admin.tokens.col.key'),
      render: (row) => {
        const shown = revealed[row.id]
        return (
          <span className="flex items-center gap-2">
            <code className="font-mono text-[13px] text-ink-2">{shown || row.masked_key}</code>
            <button
              type="button"
              onClick={() => void handleRevealKey(row)}
              disabled={revealing === row.id}
              className="text-[13px] text-ink-3 transition hover:text-brand disabled:opacity-50"
            >
              {revealing === row.id ? '…' : shown ? t('admin.tokens.collapse') : t('admin.tokens.reveal')}
            </button>
            {shown && <CopyButton text={shown} label={t('admin.tokens.copy')} />}
          </span>
        )
      },
    },
    {
      title: t('admin.tokens.col.status'),
      render: (row) => (
        <Badge tone={row.status === STATUS_ENABLED ? 'ok' : 'off'}>
          {row.status_text ?? (row.status === STATUS_ENABLED ? t('admin.tokens.statusEnabled') : t('admin.tokens.statusDisabled'))}
        </Badge>
      ),
    },
    {
      title: t('admin.tokens.col.remain'),
      align: 'right',
      render: (row) => (
        <span className="text-ink-2">
          {row.unlimited_quota ? t('admin.tokens.unlimited') : formatYuanFromQuota(row.remain_quota, quotaPerYuan)}
        </span>
      ),
    },
    {
      title: t('admin.tokens.col.used'),
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.used_quota, quotaPerYuan)}</span>,
    },
    {
      title: t('admin.tokens.col.budget'),
      align: 'right',
      render: (row) => {
        const budget = row.budget_quota ?? 0
        const period = row.budget_period ?? ''
        if (!(budget > 0) || !period) return <span className="text-ink-3">{t('admin.tokens.unlimited')}</span>
        return (
          <span className="text-ink-2">
            {t('admin.tokens.budgetValue', { amount: formatYuanFromQuota(budget, quotaPerYuan), period: BUDGET_PERIOD_LABEL[period] ? t(BUDGET_PERIOD_LABEL[period]) : period })}
          </span>
        )
      },
    },
    {
      title: t('admin.tokens.col.expire'),
      render: (row) => <span className="text-[13px] text-ink-3">{formatExpiry(row.expires_at)}</span>,
    },
    {
      title: t('admin.tokens.col.owner'),
      render: (row) => <span className="text-ink-2">{row.username ?? (row.user_id ? `#${row.user_id}` : '—')}</span>,
    },
    {
      title: t('admin.tokens.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.tokens.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.tokens.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.tokens.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.tokens.subtitle', { total })}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>
          {t('admin.tokens.create')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.tokens.emptyTitle')}
          emptyDescription={t('admin.tokens.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <TokenFormModal
        open={editing !== null}
        token={editing === 'new' ? null : editing}
        onClose={() => {
          setEditing(null)
          void load()
        }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.tokens.delete.title')}
        message={t('admin.tokens.delete.message', { name: deleteTarget?.name ?? '' })}
        danger
        confirmText={t('admin.tokens.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 令牌表单弹层（新建含一次性明文 key 展示） ──────────── */

function TokenFormModal({
  open,
  token,
  onClose,
}: {
  open: boolean
  token: TokenWithBudget | null
  onClose: () => void
}) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  // 新建字段
  const [name, setName] = useState('')
  const [expiresInDays, setExpiresInDays] = useState('0')
  const [modelsText, setModelsText] = useState('')
  const [userId, setUserId] = useState('')
  // 新建/编辑共用
  const [unlimited, setUnlimited] = useState(false)
  const [remainQuota, setRemainQuota] = useState('')
  const [status, setStatus] = useState(STATUS_ENABLED)
  // 周期预算（仅编辑已有令牌时编辑；新建接口暂不接受该字段）
  const [budgetYuan, setBudgetYuan] = useState('')
  const [budgetPeriod, setBudgetPeriod] = useState('')
  const [loading, setLoading] = useState(false)
  // 新建成功后的一次性明文密钥
  const [createdKey, setCreatedKey] = useState('')

  useEffect(() => {
    if (!open) return
    setName(token?.name ?? '')
    setExpiresInDays('0')
    setModelsText('')
    setUserId('')
    setUnlimited(token?.unlimited_quota ?? false)
    setRemainQuota(token ? quotaToYuanInput(token.remain_quota, quotaPerYuan) : '')
    setStatus(token?.status ?? STATUS_ENABLED)
    // 周期预算：额度 → 元回填；未启用（0）时留空
    setBudgetYuan(token?.budget_quota && token.budget_quota > 0 ? quotaToYuanInput(token.budget_quota, quotaPerYuan) : '')
    setBudgetPeriod(token?.budget_period ?? '')
    setCreatedKey('')
  }, [open, token, quotaPerYuan])

  async function handleSubmit() {
    if (createdKey) return // 已创建成功，等待关闭
    if (!token && !name.trim()) {
      toastError(t('admin.tokens.error.nameRequired'))
      return
    }
    if (!token && !userId.trim()) {
      toastError(t('admin.tokens.error.userIdRequired'))
      return
    }
    // 周期预算：选了周期就必须给一个 > 0 的金额；金额 <= 0 视为「不启用」。
    // 后端约束：budget_quota > 0 时必须带合法 budget_period；取消时两者都清空。
    const budgetQuota = budgetPeriod ? yuanToQuota(Number(budgetYuan || 0), quotaPerYuan) : 0
    if (budgetPeriod && (!budgetQuota || budgetQuota <= 0)) {
      toastError(t('admin.tokens.error.budgetInvalid'))
      return
    }
    setLoading(true)
    try {
      if (token) {
        const payload: AdminUpdateTokenPayload & { budget_quota: number; budget_period: string } = {
          status,
          unlimited_quota: unlimited,
          remain_quota: yuanToQuota(Number(remainQuota || 0), quotaPerYuan) ?? 0, // 人民币 → 额度
          // budget_quota/budget_period 尚未进 AdminUpdateTokenPayload，用交叉类型承载：
          // 接口复用现有 PUT /admin/tokens/{id}，请求体多带这两个字段。
          budget_quota: budgetQuota ?? 0,
          budget_period: budgetPeriod,
        }
        await updateToken(token.id, payload)
        toast(t('admin.tokens.toast.updated'))
        onClose()
      } else {
        const payload: CreateTokenPayload = {
          name: name.trim(),
          expires_in_days: Number(expiresInDays || 0),
          models: parseModelList(modelsText),
          unlimited_quota: unlimited,
          remain_quota: unlimited ? 0 : (yuanToQuota(Number(remainQuota || 0), quotaPerYuan) ?? 0),
          user_id: Number(userId.trim()),
        }
        const result = await createTokenForUser(payload)
        setCreatedKey(result.key)
        toast(t('admin.tokens.toast.created'))
      }
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.tokens.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={createdKey ? t('admin.tokens.form.createdTitle') : token ? t('admin.tokens.form.editTitle') : t('admin.tokens.form.newTitle')}
      width={560}
    >
      {createdKey ? (
        /* 一次性明文密钥：warn 强调「仅此一次」，配合 CopyButton 快速保存 */
        <div className="space-y-4">
          <div className="rounded-md border border-warn/40 bg-warn/10 p-4">
            <div className="flex items-center gap-2">
              <Badge tone="warn">{t('admin.tokens.form.oneTime')}</Badge>
              <span className="text-sm font-medium text-warn">{t('admin.tokens.form.createdHint')}</span>
            </div>
            <div className="mt-3 flex items-center justify-between gap-3 rounded-md border border-line bg-surface px-3 py-2">
              <code className="break-all font-mono text-[13px] text-ink">{createdKey}</code>
              <CopyButton text={createdKey} label={t('admin.tokens.form.copyKey')} className="shrink-0" />
            </div>
          </div>
          <div className="text-xs text-ink-3">{t('admin.tokens.form.createdNote')}</div>
        </div>
      ) : (
        <div className="space-y-4">
          {!token && (
            <>
              <Field label={t('admin.tokens.form.name')} required>
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('admin.tokens.form.namePlaceholder')} />
              </Field>

              <Field label={t('admin.tokens.form.expiresIn')} help={t('admin.tokens.form.expiresHelp')}>
                <Input value={expiresInDays} onChange={(e) => setExpiresInDays(e.target.value)} type="number" placeholder="0" />
              </Field>

              <Field label={t('admin.tokens.form.models')} help={t('admin.tokens.form.modelsHelp')}>
                <Input value={modelsText} onChange={(e) => setModelsText(e.target.value)} placeholder={t('admin.tokens.form.modelsPlaceholder')} />
              </Field>

              <Field label={t('admin.tokens.form.userId')} required help={t('admin.tokens.form.userIdHelp')}>
                <Input value={userId} onChange={(e) => setUserId(e.target.value)} type="number" placeholder={t('admin.tokens.form.userIdPlaceholder')} />
              </Field>
            </>
          )}

          {token && (
            <Field label={t('admin.tokens.form.status')}>
              <div className="flex items-center justify-between rounded-md border border-line bg-surface px-3 py-2">
                <span className="text-[13px] text-ink-2">{status === STATUS_ENABLED ? t('admin.tokens.statusEnabled') : t('admin.tokens.statusDisabled')}</span>
                <Switch checked={status === STATUS_ENABLED} onChange={(v) => setStatus(v ? STATUS_ENABLED : STATUS_DISABLED)} />
              </div>
            </Field>
          )}

          <Field label={t('admin.tokens.form.unlimited')}>
            <div className="flex items-center justify-between rounded-md border border-line bg-surface px-3 py-2">
              <span className="text-[13px] text-ink-2">{t('admin.tokens.form.unlimitedHint')}</span>
              <Switch checked={unlimited} onChange={setUnlimited} />
            </div>
          </Field>

          <Field label={t('admin.tokens.form.remain')} help={unlimited ? t('admin.tokens.form.remainHelpUnlimited') : t('admin.tokens.form.remainHelp')}>
            <Input
              value={remainQuota}
              onChange={(e) => setRemainQuota(e.target.value)}
              type="number"
              placeholder="0.00"
              disabled={unlimited}
            />
          </Field>

          {/* 周期预算：仅在编辑已有令牌时可改（新建接口暂不接受该字段）。
              留空 / 选「不启用」= 取消预算（提交 budget_quota=0、budget_period=''）。 */}
          {token && (
            <>
              <Field
                label={t('admin.tokens.form.budget')}
                help={t('admin.tokens.form.budgetHelp')}
              >
                <Input
                  value={budgetYuan}
                  onChange={(e) => setBudgetYuan(e.target.value)}
                  type="number"
                  min={0}
                  placeholder="0.00"
                  disabled={budgetPeriod === ''}
                />
              </Field>

              <Field label={t('admin.tokens.form.period')} help={t('admin.tokens.form.periodHelp')}>
                <Select value={budgetPeriod} onChange={(e) => setBudgetPeriod(e.target.value)}>
                  {BUDGET_PERIOD_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {t(option.labelKey)}
                    </option>
                  ))}
                </Select>
              </Field>
            </>
          )}
        </div>
      )}

      <div className="mt-5 flex justify-end gap-2">
        {createdKey ? (
          <Button variant="primary" onClick={onClose}>
            {t('admin.tokens.form.done')}
          </Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose}>
              {t('admin.tokens.form.cancel')}
            </Button>
            <Button variant="primary" loading={loading} onClick={handleSubmit}>
              {token ? t('admin.tokens.form.save') : t('admin.tokens.form.create')}
            </Button>
          </>
        )}
      </div>
    </Modal>
  )
}