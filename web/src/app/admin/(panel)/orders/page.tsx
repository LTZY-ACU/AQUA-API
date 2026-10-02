/** 管理后台：充值订单（/admin/orders）。列表 + 人工入账 / 关闭订单（均带确认）；数据经 api/admin.ts 读写 /api/admin/orders。 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { closeOrder, listAllOrders, markOrderPaid } from '@/api/admin'
import { useReauthGuard } from '@/components/auth/ReauthGuard'
import type { PaymentOrder } from '@/api/types'
import { ORDER_STATUS_PAID, ORDER_STATUS_PENDING } from '@/api/types'
import { Badge, Card } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/Modal'
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

  // 人工入账属于"凭一句话产生资产"的操作，后端要求二次验证；
  // guard 会在被拦时弹窗要密码，验证通过后自动重试，调用方无需感知。
  const { guard, dialog } = useReauthGuard()

  async function handleConfirm() {
    if (!confirmAction) return
    const { type, order } = confirmAction
    try {
      if (type === 'paid') {
        await guard(() => markOrderPaid(order.trade_no))
        toast('订单已人工入账')
      } else {
        await closeOrder(order.trade_no)
        toast('订单已关闭')
      }
      setConfirmAction(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : '操作失败')
    }
  }

  const columns: Column<PaymentOrder>[] = [
    { title: '订单号', render: (row) => <code className="font-mono text-[13px] text-ink">{row.trade_no}</code> },
    {
      title: '金额',
      align: 'right',
      render: (row) => (
        <span className="font-medium text-ink">
          {row.amount_text}
          <span className="ml-0.5 text-xs text-ink-3">{row.currency}</span>
        </span>
      ),
    },
    {
      title: '状态',
      render: (row) => <Badge tone={orderTone(row.status)}>{row.status_text}</Badge>,
    },
    {
      title: '通道',
      render: (row) => <span className="text-ink-2">{row.method_label ?? row.method}</span>,
    },
    {
      title: '时间',
      render: (row) => <span className="text-[13px] text-ink-3">{formatDateTime(row.created_at)}</span>,
    },
    {
      title: '操作',
      align: 'right',
      render: (row) =>
        row.status === ORDER_STATUS_PENDING ? (
          <span className="flex items-center justify-end gap-2 text-[13px]">
            <button type="button" onClick={() => setConfirmAction({ type: 'paid', order: row })} className="text-ink-3 hover:text-brand">
              人工入账
            </button>
            <button type="button" onClick={() => setConfirmAction({ type: 'close', order: row })} className="text-ink-3 hover:text-err">
              关闭
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
          <h1 className="text-xl font-bold text-ink">充值订单</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">全站充值订单，可人工入账或关闭（{total}）</p>
        </div>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.trade_no}
          emptyTitle="还没有订单"
          emptyDescription="用户发起充值的订单会出现在这里"
        />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>

      <ConfirmDialog
        open={Boolean(confirmAction)}
        title={confirmAction?.type === 'paid' ? '人工入账' : '关闭订单'}
        message={
          confirmAction?.type === 'paid'
            ? `确认将订单「${confirmAction?.order.trade_no}」标记为已支付并给用户入账额度？后端幂等，重复执行不会重复加额。`
            : `确认关闭订单「${confirmAction?.order.trade_no}」？关闭后用户无法继续支付。`
        }
        danger
        confirmText={confirmAction?.type === 'paid' ? '确认入账' : '确认关闭'}
        onConfirm={handleConfirm}
        onCancel={() => setConfirmAction(null)}
      />

      {/* 二次验证弹窗（被闸门拦下时才显示） */}
      {dialog}
    </div>
  )
}