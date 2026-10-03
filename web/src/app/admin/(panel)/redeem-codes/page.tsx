/** 管理后台：兑换码（/admin/redeem-codes）。列表（明文 code）+ 关键词/批次筛选 + 批量生成（CodeBlock 分发）+ 作废/删除 + 清理失效；数据经 api/admin.ts 读写 /api/admin/redeem-codes。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import {
  createRedeemCodes,
  deleteInvalidRedeemCodes,
  deleteRedeemCode,
  listRedeemCodes,
  updateRedeemCode,
} from '@/api/admin'
import type { CreateRedeemCodesResult, RedeemCode } from '@/api/types'
import { REDEEM_STATUS_UNUSED, REDEEM_STATUS_USED, REDEEM_STATUS_VOID } from '@/api/types'
import { Badge, Card, CodeBlock } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatExpiry } from '@/utils/format'
import { formatYuanFromQuota, yuanToQuota } from '@/utils/money'

const PAGE_SIZE = 20

/** 兑换码状态徽标：1 未使用 info / 2 已使用 off / 3 已作废 warn；已过期优先按 warn 展示 */
function RedeemStatusBadge({ code }: { code: RedeemCode }) {
  const { t } = useI18n()
  const tone = code.expired
    ? 'warn'
    : code.status === REDEEM_STATUS_UNUSED
      ? 'info'
      : code.status === REDEEM_STATUS_USED
        ? 'off'
        : 'warn'
  const text = code.expired && code.status === REDEEM_STATUS_UNUSED ? t('admin.redeem-codes.expired') : code.status_text
  return <Badge tone={tone}>{text}</Badge>
}

export default function AdminRedeemCodesPage() {
  const { quotaPerYuan } = useSite()
  const [items, setItems] = useState<RedeemCode[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [keyword, setKeyword] = useState('')
  const [batchNo, setBatchNo] = useState('')
  const [filters, setFilters] = useState<{ keyword?: string; batch_no?: string }>({})
  const [generateOpen, setGenerateOpen] = useState(false)
  const [voidTarget, setVoidTarget] = useState<RedeemCode | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<RedeemCode | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listRedeemCodes({ page, size: PAGE_SIZE, ...filters })
      setItems(data.items)
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [page, filters])

  useEffect(() => {
    void load()
  }, [load])

  function handleSearch() {
    setFilters({ keyword: keyword.trim() || undefined, batch_no: batchNo.trim() || undefined })
    setPage(1)
  }

  function handleReset() {
    setKeyword('')
    setBatchNo('')
    setFilters({})
    setPage(1)
  }

  async function handleVoid() {
    if (!voidTarget) return
    try {
      await updateRedeemCode(voidTarget.id, { status: REDEEM_STATUS_VOID })
      toast(t('admin.redeem-codes.toast.voided'))
      setVoidTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.redeem-codes.toast.voidFailed'))
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteRedeemCode(deleteTarget.id)
      toast(t('admin.redeem-codes.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.redeem-codes.toast.deleteFailed'))
    }
  }

  async function handleClearInvalid() {
    try {
      const { deleted } = await deleteInvalidRedeemCodes()
      toast(deleted > 0 ? t('admin.redeem-codes.toast.cleared', { count: deleted }) : t('admin.redeem-codes.toast.clearedNone'))
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.redeem-codes.toast.clearFailed'))
    }
  }

  const columns: Column<RedeemCode>[] = [
    { title: t('admin.redeem-codes.col.code'), render: (row) => <code className="font-mono text-[13px] font-medium text-ink">{row.code}</code> },
    {
      title: t('admin.redeem-codes.col.amount'),
      align: 'right',
      render: (row) => <span className="text-ink-2">{formatYuanFromQuota(row.quota, quotaPerYuan)}</span>,
    },
    { title: t('admin.redeem-codes.col.status'), render: (row) => <RedeemStatusBadge code={row} /> },
    {
      title: t('admin.redeem-codes.col.expire'),
      render: (row) => <span className="text-[13px] text-ink-3">{formatExpiry(row.expires_at)}</span>,
    },
    { title: t('admin.redeem-codes.col.batch'), render: (row) => <span className="text-[13px] text-ink-2">{row.batch_no || '—'}</span> },
    { title: t('admin.redeem-codes.col.remark'), render: (row) => <span className="text-[13px] text-ink-3">{row.remark || '—'}</span> },
    {
      title: t('admin.redeem-codes.col.usedBy'),
      render: (row) => <span className="text-ink-2">{row.used_by > 0 ? `#${row.used_by}` : '—'}</span>,
    },
    {
      title: t('admin.redeem-codes.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          {row.status === REDEEM_STATUS_UNUSED && (
            <button type="button" onClick={() => setVoidTarget(row)} className="text-ink-3 hover:text-warn">
              {t('admin.redeem-codes.action.void')}
            </button>
          )}
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.redeem-codes.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.redeem-codes.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.redeem-codes.subtitle', { total })}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={handleClearInvalid}>
            {t('admin.redeem-codes.clearInvalid')}
          </Button>
          <Button variant="primary" onClick={() => setGenerateOpen(true)}>
            {t('admin.redeem-codes.generate')}
          </Button>
        </div>
      </div>

      {/* 筛选条：关键词（码/备注）+ 批次号 */}
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-60">
          <Field label={t('admin.redeem-codes.filter.keyword')} htmlFor="rc-keyword">
            <Input
              id="rc-keyword"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              placeholder={t('admin.redeem-codes.filter.keywordPlaceholder')}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSearch()
              }}
            />
          </Field>
        </div>
        <div className="w-52">
          <Field label={t('admin.redeem-codes.filter.batch')} htmlFor="rc-batch">
            <Input
              id="rc-batch"
              value={batchNo}
              onChange={(e) => setBatchNo(e.target.value)}
              placeholder={t('admin.redeem-codes.filter.batchPlaceholder')}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSearch()
              }}
            />
          </Field>
        </div>
        <Button variant="secondary" onClick={handleSearch}>
          {t('admin.redeem-codes.filter.search')}
        </Button>
        <Button variant="ghost" onClick={handleReset}>
          {t('admin.redeem-codes.filter.reset')}
        </Button>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.redeem-codes.emptyTitle')}
          emptyDescription={t('admin.redeem-codes.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <RedeemGenerateModal
        open={generateOpen}
        onClose={() => setGenerateOpen(false)}
        onSaved={() => void load()}
      />

      <ConfirmDialog
        open={Boolean(voidTarget)}
        title={t('admin.redeem-codes.voidConfirm.title')}
        message={t('admin.redeem-codes.voidConfirm.message', { code: voidTarget?.code ?? '' })}
        danger
        confirmText={t('admin.redeem-codes.voidConfirm.confirm')}
        onConfirm={handleVoid}
        onCancel={() => setVoidTarget(null)}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.redeem-codes.deleteConfirm.title')}
        message={t('admin.redeem-codes.deleteConfirm.message', { code: deleteTarget?.code ?? '' })}
        danger
        confirmText={t('admin.redeem-codes.deleteConfirm.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 批量生成弹层：表单 → 成功后切换展示本批明文码 ───────── */

function RedeemGenerateModal({
  open,
  onClose,
  onSaved,
}: {
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [count, setCount] = useState('10')
  const [quota, setQuota] = useState('10') // 人民币（元）：每张面额
  const [expiresDays, setExpiresDays] = useState('0')
  const [remark, setRemark] = useState('')
  const [batchNo, setBatchNo] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<CreateRedeemCodesResult | null>(null)

  useEffect(() => {
    if (!open) return
    setCount('10')
    setQuota('10')
    setExpiresDays('0')
    setRemark('')
    setBatchNo('')
    setResult(null)
  }, [open])

  async function handleSubmit() {
    const n = Number(count)
    if (!Number.isInteger(n) || n < 1 || n > 500) {
      toastError(t('admin.redeem-codes.error.countInvalid'))
      return
    }
    const q = Number(quota)
    if (!Number.isFinite(q) || q < 0) {
      toastError(t('admin.redeem-codes.error.quotaInvalid'))
      return
    }
    setLoading(true)
    try {
      const data = await createRedeemCodes({
        count: n,
        quota: yuanToQuota(q, quotaPerYuan) ?? 0, // 人民币 → 额度（内部整数记账）
        expires_days: Number(expiresDays || 0),
        remark: remark.trim() || undefined,
        batch_no: batchNo.trim() || undefined,
      })
      setResult(data)
      toast(t('admin.redeem-codes.toast.generated', { count: data.count }))
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.redeem-codes.toast.generateFailed'))
    } finally {
      setLoading(false)
    }
  }

  // 生成成功：切换为结果面板（CodeBlock 供复制分发），列表已由 onSaved 刷新
  if (result) {
    return (
      <Modal open={open} onClose={onClose} title={t('admin.redeem-codes.form.resultTitle')} width={560}>
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <Badge tone="ok">{t('admin.redeem-codes.form.resultBadge')}</Badge>
            <span className="text-sm text-ink-2">
              {t('admin.redeem-codes.form.resultSummary', { batch: result.batch_no || '—', count: result.count })}
            </span>
          </div>
          <CodeBlock code={result.items.map((item) => item.code).join('\n')} title={t('admin.redeem-codes.form.copyTitle')} />
          <div className="text-xs text-ink-3">{t('admin.redeem-codes.form.resultHint')}</div>
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="primary" onClick={onClose}>
            {t('admin.redeem-codes.form.done')}
          </Button>
        </div>
      </Modal>
    )
  }

  return (
    <Modal open={open} onClose={onClose} title={t('admin.redeem-codes.form.title')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.redeem-codes.form.count')} required help={t('admin.redeem-codes.form.countHelp')}>
          <Input value={count} onChange={(e) => setCount(e.target.value)} type="number" placeholder="10" />
        </Field>

        <Field label={t('admin.redeem-codes.form.quota')} required help={t('admin.redeem-codes.form.quotaHelp')}>
          <Input value={quota} onChange={(e) => setQuota(e.target.value)} type="number" placeholder="10" />
        </Field>

        <Field label={t('admin.redeem-codes.form.expiresDays')} help={t('admin.redeem-codes.form.expiresHelp')}>
          <Input value={expiresDays} onChange={(e) => setExpiresDays(e.target.value)} type="number" placeholder="0" />
        </Field>

        <Field label={t('admin.redeem-codes.form.remark')}>
          <Input value={remark} onChange={(e) => setRemark(e.target.value)} placeholder={t('admin.redeem-codes.form.remarkPlaceholder')} />
        </Field>

        <Field label={t('admin.redeem-codes.form.batch')} help={t('admin.redeem-codes.form.batchHelp')}>
          <Input value={batchNo} onChange={(e) => setBatchNo(e.target.value)} placeholder={t('admin.redeem-codes.form.batchPlaceholder')} />
        </Field>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('admin.redeem-codes.form.cancel')}
        </Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>
          {t('admin.redeem-codes.form.submit')}
        </Button>
      </div>
    </Modal>
  )
}