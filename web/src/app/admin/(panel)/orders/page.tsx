/** 管理后台：充值订单（/admin/orders）。列表 + 人工入账 / 关闭订单（均带确认）；数据经 api/admin.ts 读写 /api/admin/orders。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { closeOrder, listAllOrders, markOrderPaid } from '@/api/admin'
import type { PaymentOrder } from '@/api/types'
import { ORDER_STATUS_PAID, ORDER_STATUS_PENDING } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { formatDateTime } from '@/utils/format'

const PAGE_SIZE = 20

/** 订单状态 → 徽标色调：已支付绿、待支付黄、关闭/退款灰（与任务约定一致） */
function orderTone(status: number): 'ok' | 'warn' | 'off' {
  if (status === ORDER_STATUS_PAID) return 'ok'
  if (status === ORDER_STATUS_PENDING) return 'warn'
  return 'off'
}

export default function AdminOrdersPage() {
  const [items, setItems] = useState<PaymentOrder[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [confirmAction, setConfirmAction] = useState<{ type: 'paid' | 'close'; order: PaymentOrder } | null>(null)
  const { toast, toastError } = useToast()
  const { t } = useI18n()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listAllOrders({ page, size: PAGE_SIZE })
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

  async function handleConfirm() {
    if (!confirmAction) return
    const { type, order } = confirmAction
    try {
      if (type === 'paid') {
        await markOrderPaid(order.trade_no)
        toast(t('admin.orders.toast.paid'))
      } else {
        await closeOrder(order.trade_no)
        toast(t('admin.orders.toast.closed'))
      }
      setConfirmAction(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.orders.toast.opFailed'))
    }
  }

  const columns: Column<PaymentOrder>[] = [
    { title: t('admin.orders.col.tradeNo'), render: (row) => <code className="font-mono text-[13px] text-ink">{row.trade_no}</code> },
    {
      title: t('admin.orders.col.amount'),
      align: 'right',
      render: (row) => (
        <span className="font-medium text-ink">
          {row.amount_text}
          <span className="ml-0.5 text-xs text-ink-3">{row.currency}</span>
        </span>
      ),
    },
    {
      title: t('admin.orders.col.status'),
      render: (row) => <Badge tone={orderTone(row.status)}>{row.status_text}</Badge>,
    },
    {
      title: t('admin.orders.col.method'),
      render: (row) => <span className="text-ink-2">{row.method_label ?? row.method}</span>,
    },
    {
      title: t('admin.orders.col.time'),
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: t('admin.orders.col.actions'),
      align: 'right',
      render: (row) =>
        row.status === ORDER_STATUS_PENDING ? (
          <span className="flex items-center justify-end gap-2 text-[13px]">
            <button type="button" onClick={() => setConfirmAction({ type: 'paid', order: row })} className="text-ink-3 hover:text-brand">
              {t('admin.orders.action.markPaid')}
            </button>
            <button type="button" onClick={() => setConfirmAction({ type: 'close', order: row })} className="text-ink-3 hover:text-err">
              {t('admin.orders.action.close')}
            </button>
          </span>
        ) : (
          <span className="text-ink-3/60">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.orders.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.orders.subtitle', { total })}</p>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.trade_no}
          emptyTitle={t('admin.orders.emptyTitle')}
          emptyDescription={t('admin.orders.emptyDescription')}
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <ConfirmDialog
        open={Boolean(confirmAction)}
        title={confirmAction?.type === 'paid' ? t('admin.orders.confirm.paidTitle') : t('admin.orders.confirm.closeTitle')}
        message={
          confirmAction?.type === 'paid'
            ? t('admin.orders.confirm.paidMessage', { tradeNo: confirmAction?.order.trade_no ?? '' })
            : t('admin.orders.confirm.closeMessage', { tradeNo: confirmAction?.order.trade_no ?? '' })
        }
        danger
        confirmText={confirmAction?.type === 'paid' ? t('admin.orders.confirm.paidConfirm') : t('admin.orders.confirm.closeConfirm')}
        onConfirm={handleConfirm}
        onCancel={() => setConfirmAction(null)}
      />
    </div>
  )
}