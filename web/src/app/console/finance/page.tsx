/** 用户门户：财务记录（/console/finance）。
 *
 * 意图（Why）：
 *   汇总卡（余额/消费/充值/返利）+ 返利流水与订单记录，用户对账不必来回翻页面。
 *   待支付的订单额外给出「继续支付」：用户关掉收银台后仍可回到这里续付，
 *   否则一笔未付订单只能作废重下，体验上就像"支付不能用了"。
 */
'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

import { fetchFinanceSummary, listMyOrders } from '@/api/portal'
import { fetchReferral } from '@/api/referral'
import type { FinanceSummary, PaymentOrder } from '@/api/types'
import type { ReferralInfo } from '@/api/referral'
import { Badge, Card, StatCard } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { useAuth } from '@/lib/auth/auth-context'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

const PAGE_SIZE = 20

/** 订单状态（与后端 PaymentStatus 对应）：1 待支付 / 2 已支付 / 3 已关闭 / 4 已退款 */
const STATUS_PENDING = 1

export default function ConsoleFinancePage() {
  const { refreshUser } = useAuth()
  const { quotaPerYuan } = useSite()
  const [finance, setFinance] = useState<FinanceSummary | null>(null)
  const [referral, setReferral] = useState<ReferralInfo | null>(null)
  const [orders, setOrders] = useState<PaymentOrder[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [f, r, o] = await Promise.all([
        fetchFinanceSummary(),
        fetchReferral().catch(() => null),
        listMyOrders({ page, size: PAGE_SIZE }),
      ])
      setFinance(f)
      setReferral(r)
      setOrders(o.items)
      setTotal(o.total)
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
    void refreshUser()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const unlimited = finance?.balance_quota === -1

  const columns: Column<PaymentOrder>[] = [
    { title: '订单号', render: (row) => <span className="font-mono text-xs text-ink-2">{row.trade_no}</span> },
    { title: '金额', align: 'right', render: (row) => <span className="text-ink-2">¥{row.amount_text}</span> },
    {
      title: '状态',
      render: (row) => (
        <Badge tone={row.status === 2 ? 'ok' : row.status === 4 ? 'off' : 'warn'}>{row.status_text}</Badge>
      ),
    },
    { title: '时间', render: (row) => <span className="text-ink-2">{formatDateTime(row.created_at)}</span> },
    {
      title: '操作',
      align: 'right',
      render: (row) =>
        row.status === STATUS_PENDING && row.pay_url ? (
          <button
            type="button"
            onClick={() => {
              window.location.href = row.pay_url
            }}
            className="text-[13px] text-brand hover:underline"
          >
            继续支付
          </button>
        ) : (
          <span className="text-ink-3">—</span>
        ),
    },
  ]

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-ink">财务记录</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">余额、消费与充值流水</p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="当前余额" value={finance ? (unlimited ? '不限' : formatYuanFromQuota(finance.balance_quota, quotaPerYuan)) : '—'} />
        <StatCard label="累计消费" value={finance ? formatYuanFromQuota(finance.used_quota, quotaPerYuan) : '—'} />
        <StatCard label="累计充值" value={finance ? formatYuanFromQuota(finance.recharged_quota, quotaPerYuan) : '—'} hint={`${finance?.recharge_count ?? 0} 笔`} />
        <StatCard label="邀请返利" value={referral ? formatYuanFromQuota(referral.total_reward_quota, quotaPerYuan) : '—'} hint={referral ? `${referral.invited_count} 人` : undefined} />
      </div>

      <Card padding="none">
        <div className="border-b border-line px-4 py-3 text-sm font-semibold text-ink-2">充值订单</div>
        <DataTable columns={columns} rows={loading ? null : orders} loading={loading} rowKey={(row) => row.trade_no} emptyTitle="暂无充值记录" />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>
    </div>
  )
}