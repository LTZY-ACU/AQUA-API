/** 用户门户：账户充值（/console/recharge）。
 *
 * 意图（Why）：
 *   单一决策点：选金额 → 选支付方式 → 下单跳收银台。
 *   支付是「离开本站再回来」的流程，因此必须处理回跳：收银台支付完会把用户带回
 *   /console/recharge?trade_no=xxx，若不确认结果，用户看到的还是那张充值表单，
 *   会以为"支付没生效"，于是反复下单（线上曾出现 21 秒内 12 笔重复订单）。
 *   因此本页在带回订单号时轮询订单真实状态，并明确告知「已到账 / 仍待支付 / 已关闭」。
 *
 * 流转（Flow）：
 *   载入 → fetchPaymentInfo()（金额下限、通道与子方式）
 *   → 若 URL 带 trade_no：轮询 getMyOrder() 直至终态或超时 → 已支付则刷新用户余额
 *   → 下单 createOrder() → 有 pay_url 则跳收银台（按钮进入「正在跳转」态，避免重复点击）
 *   → 回到本页 → 走上面的确认分支
 *   → 兑换码：RedeemCard → redeemMyCode() → POST /api/user/redeem → 刷新用户余额
 *
 * 扩展（Extend）：
 *   新增支付通道：后端 /api/payment/public 会自动多出一条 method，本页无需改动。
 */
'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import Link from 'next/link'

import { createOrder, getMyOrder, redeemMyCode } from '@/api/portal'
import { fetchPaymentInfo } from '@/api/site'
import type { PaymentOrder, PublicPaymentInfo } from '@/api/types'
import { ComplianceNotice } from '@/components/site/ComplianceNotice'
import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { useI18n } from '@/i18n'
import { useAuth } from '@/lib/auth/auth-context'
import { useSite } from '@/lib/site/site-context'
import { useToast } from '@/lib/toast/toast-context'
import { formatYuanFromQuota } from '@/utils/money'

const PRESETS = [10, 30, 50, 100, 200, 500]

/** 订单状态（与后端 PaymentStatus 对应）：1 待支付 / 2 已支付 / 3 已关闭 / 4 已退款 */
const STATUS_PENDING = 1
const STATUS_PAID = 2
const STATUS_CLOSED = 3

/** 轮询间隔与总时长：网关回调通常在 1 分钟内到达，超时后再提示用户稍后查看 */
const POLL_INTERVAL_MS = 3000
const POLL_TIMEOUT_MS = 90_000

export default function ConsoleRechargePage() {
  const [info, setInfo] = useState<PublicPaymentInfo | null>(null)
  const [amount, setAmount] = useState<number>(30)
  const [custom, setCustom] = useState('')
  const [method, setMethod] = useState('')
  const [subMethod, setSubMethod] = useState('')
  const [loading, setLoading] = useState(false)
  const [redirecting, setRedirecting] = useState(false)
  const { toastError } = useToast()
  const { refreshUser } = useAuth()
  const { t } = useI18n()

  /* ── 支付回跳确认：URL 带 trade_no 时轮询订单真实状态 ── */
  const [returnedTradeNo, setReturnedTradeNo] = useState('')
  const [returnedOrder, setReturnedOrder] = useState<PaymentOrder | null>(null)
  const [pollTimedOut, setPollTimedOut] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const pollTimer = useRef<number | null>(null)

  const load = useCallback(async () => {
    try {
      const data = await fetchPaymentInfo()
      setInfo(data)
      const first = data.methods[0]
      if (first) {
        setMethod(first.name)
        // 默认选中第一个子方式（如支付宝）：否则界面上没有任何选项高亮，
        // 用户点「立即支付」时看不出到底走哪条通道（后端虽会兜底取第一个，
        // 但界面必须与后端行为一致，不能让人靠猜）。
        setSubMethod(first.sub_methods[0]?.name ?? '')
      }
    } catch {
      /* 提示状态由下方展示 */
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 读取回跳订单号：只读一次，随即从地址栏抹掉，
  // 避免用户刷新页面又进一次确认流程（确认是"一次性"的语义）。
  useEffect(() => {
    if (typeof window === 'undefined') return
    const tradeNo = new URLSearchParams(window.location.search).get('trade_no') ?? ''
    if (!tradeNo) return
    setReturnedTradeNo(tradeNo)
    window.history.replaceState(null, '', window.location.pathname)
  }, [])

  useEffect(() => {
    if (!returnedTradeNo) return
    let stopped = false
    const startedAt = Date.now()
    setConfirming(true)

    const tick = async () => {
      try {
        const order = await getMyOrder(returnedTradeNo)
        if (stopped) return
        setReturnedOrder(order)
        if (order.status === STATUS_PAID) {
          // 到账后立刻刷新用户快照：否则概览/财务页仍显示旧余额，用户会以为没到账
          void refreshUser()
          stop()
          return
        }
        if (order.status !== STATUS_PENDING) {
          stop()
          return
        }
        if (Date.now() - startedAt > POLL_TIMEOUT_MS) {
          setPollTimedOut(true)
          stop()
        }
      } catch {
        // 网络抖动不该打断确认：保持在"确认中"，下一轮继续
      }
    }

    const stop = () => {
      stopped = true
      if (pollTimer.current !== null) {
        window.clearInterval(pollTimer.current)
        pollTimer.current = null
      }
      setConfirming(false)
    }

    void tick()
    pollTimer.current = window.setInterval(() => void tick(), POLL_INTERVAL_MS)
    return stop
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [returnedTradeNo])

  const effectiveAmount = custom ? Number(custom) : amount

  async function handleOrder() {
    if (!method) {
      toastError(t('portal.recharge.selectMethod'))
      return
    }
    setLoading(true)
    setRedirecting(false)
    try {
      const order = await createOrder({
        amount_cents: Math.round(effectiveAmount * 100),
        method,
        sub_method: subMethod || undefined,
      })
      if (order.pay_url) {
        // 跳第三方收银台。先把按钮置为「正在跳转」再跳，
        // 让用户在页面离开前有明确反馈，不会以为没反应而重复下单。
        setRedirecting(true)
        window.location.href = order.pay_url
      } else {
        toastError(t('portal.recharge.noPayUrl'))
      }
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('portal.recharge.orderFailed'))
    } finally {
      setLoading(false)
    }
  }

  /** 有回跳订单号时，展示确认结果 */
  function renderResult() {
    if (!returnedTradeNo) return null
    const order = returnedOrder

    if (!order) {
      return (
        <Card className="border-line-2">
          <div className="flex items-center gap-3">
            <span className="h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-brand border-t-transparent" />
            <div>
              <div className="text-sm font-medium text-ink">{t('portal.recharge.confirming')}</div>
              <div className="mt-0.5 text-xs text-ink-3">
                {t('portal.recharge.confirmingHint', { tradeNo: returnedTradeNo })}
              </div>
            </div>
          </div>
        </Card>
      )
    }

    if (order.status === STATUS_PAID) {
      return (
        <Card className="border-ok/40 bg-ok/5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="ok">{t('portal.recharge.paidBadge')}</Badge>
            <span className="text-sm font-medium text-ink">{t('portal.recharge.paidAmount', { amount: order.amount_text })}</span>
            <span className="font-mono text-xs text-ink-3">{t('portal.recharge.orderNo', { tradeNo: order.trade_no })}</span>
          </div>
          <p className="mt-2 text-[13px] text-ink-2">
            {t('portal.recharge.paidHint')}
            {' '}
            <Link href="/console/finance" className="text-brand hover:underline">
              {t('portal.recharge.financeLink')}
            </Link>
            {t('portal.recharge.paidHintEnd')}
          </p>
        </Card>
      )
    }

    if (order.status === STATUS_CLOSED) {
      return (
        <Card className="border-warn/40 bg-warn/5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="warn">{t('portal.recharge.closedBadge')}</Badge>
            <span className="font-mono text-xs text-ink-3">{t('portal.recharge.orderNo', { tradeNo: order.trade_no })}</span>
          </div>
          <p className="mt-2 text-[13px] text-ink-2">
            {t('portal.recharge.closedHint')}
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" onClick={resetReturned}>
              {t('portal.recharge.rechargeAgain')}
            </Button>
            <Link href="/contact">
              <Button variant="ghost" size="sm">
                {t('portal.recharge.contact')}
              </Button>
            </Link>
          </div>
        </Card>
      )
    }

    // 待支付（含轮询超时）：给出继续支付入口，避免用户关掉收银台后无法续付
    return (
      <Card className="border-warn/40 bg-warn/5">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="warn">{t('portal.recharge.pendingBadge')}</Badge>
          <span className="text-sm font-medium text-ink">¥{order.amount_text}</span>
          <span className="font-mono text-xs text-ink-3">{t('portal.recharge.orderNo', { tradeNo: order.trade_no })}</span>
        </div>
        <p className="mt-2 text-[13px] text-ink-2">
          {pollTimedOut ? t('portal.recharge.pendingTimeout') : t('portal.recharge.pendingHint')}
        </p>
        <div className="mt-3 flex flex-wrap gap-2">
          {order.pay_url && (
            <Button
              variant="primary"
              size="sm"
              onClick={() => {
                setConfirming(true)
                window.location.href = order.pay_url
              }}
            >
              {t('portal.recharge.continuePay')}
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={resetReturned}>
            {t('portal.recharge.orderAgain')}
          </Button>
          <Link href="/console/finance">
            <Button variant="ghost" size="sm">
              {t('portal.recharge.viewFinance')}
            </Button>
          </Link>
        </div>
      </Card>
    )
  }

  /** 结束回跳确认态（重新下单 / 关闭订单提示后回到干净表单） */
  function resetReturned() {
    if (pollTimer.current !== null) {
      window.clearInterval(pollTimer.current)
      pollTimer.current = null
    }
    setConfirming(false)
    setReturnedTradeNo('')
    setReturnedOrder(null)
    setPollTimedOut(false)
  }

  if (!info?.enabled) {
    return (
      <div className="space-y-5">
        <h1 className="text-xl font-bold text-ink">{t('portal.recharge.title')}</h1>
        <ComplianceNotice
          variant="inline"
          className="mt-2"
          message={t('portal.recharge.compliance')}
        />
        <Card>
          <p className="text-sm text-ink-2">{t('portal.recharge.disabled')}</p>
        </Card>
        {/* 未开放在线充值时，兑换码往往是用户唯一的自助加余额途径，必须保留入口 */}
        <RedeemCard />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('portal.recharge.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">
          {t('portal.recharge.subtitle', { min: info.min_cents / 100 })}
        </p>
        <ComplianceNotice
          variant="inline"
          className="mt-2"
          message={t('portal.recharge.compliance')}
        />
      </div>

      {renderResult()}

      <Card>
        <div className="text-sm font-semibold text-ink-2">{t('portal.recharge.selectAmount')}</div>
        <div className="mt-3 grid grid-cols-3 gap-2 sm:grid-cols-6">
          {PRESETS.map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => { setAmount(value); setCustom('') }}
              className={`rounded-md border px-3 py-2.5 text-sm font-medium transition ${
                !custom && amount === value ? 'border-brand bg-brand/8 text-brand' : 'border-line-2 text-ink-2 hover:border-brand/40'
              }`}
            >
              ¥{value}
            </button>
          ))}
        </div>
        <div className="mt-3 flex items-center gap-2 text-sm">
          <span className="text-ink-3">{t('portal.recharge.custom')}</span>
          <input
            type="number"
            min={info.min_cents / 100}
            value={custom}
            onChange={(e) => setCustom(e.target.value)}
            placeholder={t('portal.recharge.customPlaceholder')}
            className="h-9 w-32 rounded-md border border-line-2 bg-card px-3 text-sm outline-none focus:border-brand"
          />
          <span className="text-xs text-ink-3">{t('portal.recharge.yuan')}</span>
        </div>
      </Card>

      {info.methods.length > 0 && (
        <Card>
          <div className="text-sm font-semibold text-ink-2">{t('portal.recharge.method')}</div>
          <div className="mt-3 space-y-2">
            {info.methods.map((item) => {
              const ready = item.ready
              return item.sub_methods.length > 0 ? (
                <div key={item.name} className="rounded-md border border-line p-3">
                  <div className="text-sm font-medium text-ink-2">{item.label}</div>
                  <div className="mt-2 flex flex-wrap gap-2">
                    {item.sub_methods.map((sub) => (
                      <button
                        key={sub.name}
                        type="button"
                        disabled={!ready}
                        onClick={() => { setMethod(item.name); setSubMethod(sub.name) }}
                        className={`rounded-md border px-3 py-1.5 text-[13px] transition disabled:opacity-40 ${
                          method === item.name && subMethod === sub.name
                            ? 'border-brand bg-brand/8 text-brand'
                            : 'border-line-2 text-ink-2 hover:border-brand/40'
                        }`}
                      >
                        {sub.label}
                      </button>
                    ))}
                  </div>
                </div>
              ) : (
                <button
                  key={item.name}
                  type="button"
                  disabled={!ready}
                  onClick={() => { setMethod(item.name); setSubMethod('') }}
                  className={`flex w-full items-center justify-start gap-2 rounded-md border px-3 py-2.5 text-sm transition disabled:opacity-40 ${
                    method === item.name ? 'border-brand bg-brand/8 text-brand' : 'border-line-2 text-ink-2 hover:border-brand/40'
                  }`}
                >
                  {item.label}
                  {!ready && <span className="text-xs text-warn">{t('portal.recharge.notReady')}</span>}
                </button>
              )
            })}
          </div>
        </Card>
      )}

      <div className="flex items-center justify-between rounded-lg border border-line bg-card p-4">
        <div>
          <div className="text-sm text-ink-3">{t('portal.recharge.payable')}</div>
          <div className="text-2xl font-bold text-ink">¥{effectiveAmount ? effectiveAmount.toFixed(2) : '0.00'}</div>
          {redirecting && (
            <div className="mt-1 text-xs text-brand">{t('portal.recharge.redirecting')}</div>
          )}
        </div>
        <Button
          variant="primary"
          size="lg"
          loading={loading || redirecting}
          disabled={!effectiveAmount || effectiveAmount <= 0 || confirming || redirecting}
          onClick={handleOrder}
        >
          {redirecting ? t('portal.recharge.redirectingShort') : t('portal.recharge.payNow')}
        </Button>
      </div>

      {/* 兑换码：与在线充值并列的另一种加余额方式，不依赖支付通道是否就绪 */}
      <RedeemCard />
    </div>
  )
}

/** 兑换码卡片：输入兑换码领取额度（POST /api/user/redeem）。 */
function RedeemCard() {
  const [code, setCode] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [gained, setGained] = useState<number | null>(null)
  const { toastError, toastSuccess } = useToast()
  const { refreshUser } = useAuth()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()

  async function handleRedeem() {
    const trimmed = code.trim()
    if (!trimmed) {
      toastError(t('portal.recharge.redeemRequired'))
      return
    }
    setSubmitting(true)
    setGained(null)
    try {
      const result = await redeemMyCode(trimmed)
      setGained(result.quota)
      setCode('')
      toastSuccess(t('portal.recharge.redeemSuccess'))
      // 额度已入账：立即刷新用户快照，让顶栏与概览页的余额同步更新
      void refreshUser()
    } catch (err) {
      // 失败原因由后端按界面语言返回精确文案（不存在/已使用/已过期/已作废），直接展示
      toastError(err instanceof Error ? err.message : t('portal.recharge.redeemFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card>
      <div className="text-sm font-semibold text-ink-2">{t('portal.recharge.redeem')}</div>
      <p className="mt-1 text-xs text-ink-3">{t('portal.recharge.redeemHint')}</p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <input
          type="text"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          onKeyDown={(e) => {
            // 回车即兑换：兑换码是"输入即提交"的场景，多一次点击只是摩擦
            if (e.key === 'Enter' && !submitting && code.trim()) void handleRedeem()
          }}
          disabled={submitting}
          placeholder={t('portal.recharge.redeemPlaceholder')}
          className="h-9 min-w-0 flex-1 rounded-md border border-line-2 bg-card px-3 font-mono text-sm outline-none focus:border-brand"
        />
        <Button
          variant="primary"
          loading={submitting}
          disabled={!code.trim()}
          onClick={handleRedeem}
        >
          {t('portal.recharge.redeemAction')}
        </Button>
      </div>
      {gained !== null && (
        <div className="mt-3 rounded-md border border-ok/40 bg-ok/5 px-3 py-2 text-[13px] text-ink-2">
          {t('portal.recharge.redeemGained')}{' '}
          <span className="font-semibold text-ok">{formatYuanFromQuota(gained, quotaPerYuan)}</span>
          {t('portal.recharge.redeemGainedMid')}{' '}
          <Link href="/console/finance" className="text-brand hover:underline">
            {t('portal.recharge.financeLink')}
          </Link>
          {t('portal.recharge.redeemGainedEnd')}
        </div>
      )}
    </Card>
  )
}
