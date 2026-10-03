/** 用户门户：邀请奖励（/console/referral）。
 *
 * 意图（Why）：
 *   邀请码/链接展示 + 签到按钮 + 返利流水，三个增长动作收敛一页。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { checkin, fetchReferral, listMyReferralRewards, type ReferralInfo } from '@/api/referral'
import type { ReferralReward } from '@/api/types'
import { Button } from '@/components/ui/Button'
import { Card, StatCard } from '@/components/ui/Display'
import { DataTable, Pagination, type Column } from '@/components/ui/Table'
import { CopyButton } from '@/components/ui/Modal'
import { Badge } from '@/components/ui/Display'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatDateTime } from '@/utils/format'
import { formatYuanFromQuota } from '@/utils/money'

const PAGE_SIZE = 20

export default function ConsoleReferralPage() {
  const [info, setInfo] = useState<ReferralInfo | null>(null)
  const [rewards, setRewards] = useState<ReferralReward[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const { t } = useI18n()
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [r, rews] = await Promise.all([
        fetchReferral(),
        listMyReferralRewards({ page, size: PAGE_SIZE }),
      ])
      setInfo(r)
      setRewards(rews.items)
      setTotal(rews.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [page])

  useEffect(() => {
    void load()
  }, [load])

  async function handleCheckin() {
    try {
      const result = await checkin()
      setInfo((prev) => (prev ? { ...prev, checkin: result } : prev))
      toast(result.daily_quota > 0 ? t('portal.referral.checkinReward', { amount: formatYuanFromQuota(result.daily_quota, quotaPerYuan) }) : t('portal.referral.checkinDone'))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('portal.referral.checkinFailed'))
    }
  }

  const inviteUrl = info ? `${typeof window !== 'undefined' ? window.location.origin : ''}${info.invite_path}` : ''

  const columns: Column<ReferralReward>[] = [
    { title: t('portal.referral.colKind'), render: (row) => <Badge tone={row.kind === 'register' ? 'info' : 'brand'}>{row.kind_text}</Badge> },
    { title: t('portal.referral.colReward'), align: 'right', render: (row) => <span className="text-ink-2">+{formatYuanFromQuota(row.quota, quotaPerYuan)}</span> },
    { title: t('portal.referral.colSource'), render: (row) => <span className="text-ink-2">{row.invitee}</span> },
    { title: t('portal.referral.colTime'), render: (row) => <span className="text-ink-2">{formatDateTime(row.created_at)}</span> },
  ]

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('portal.referral.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('portal.referral.subtitle')}</p>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <div className="text-sm font-semibold text-ink-2">{t('portal.referral.myLink')}</div>
          {info ? (
            <div className="mt-3 space-y-3">
              <div className="flex items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2.5 font-mono text-xs text-ink">
                <span className="truncate">{inviteUrl}</span>
                <CopyButton text={inviteUrl} label={t('common.action.copy')} />
              </div>
              <div className="text-[13px] text-ink-3">
                {t('portal.referral.invitedLine', { n: info.invited_count, amount: formatYuanFromQuota(info.register_bonus_quota, quotaPerYuan) })}
                {info.recharge_ratio > 0 && t('portal.referral.rechargeRatio', { ratio: info.recharge_ratio })}
              </div>
            </div>
          ) : (
            <div className="mt-3 text-[13px] text-ink-3">{t('common.state.loading')}</div>
          )}
        </Card>

        <Card>
          <div className="flex items-center justify-between">
            <div className="text-sm font-semibold text-ink-2">{t('portal.referral.dailyCheckin')}</div>
            {info?.checkin.checked_today && <Badge tone="ok">{t('portal.referral.checkedToday')}</Badge>}
          </div>
          {info ? (
            <div className="mt-3 space-y-3">
              <div className="grid grid-cols-3 gap-2 text-center">
                <div>
                  <div className="text-lg font-semibold text-ink">{info.checkin.streak_days}</div>
                  <div className="text-xs text-ink-3">{t('portal.referral.streak')}</div>
                </div>
                <div>
                  <div className="text-lg font-semibold text-ink">{info.checkin.total_days}</div>
                  <div className="text-xs text-ink-3">{t('portal.referral.totalDays')}</div>
                </div>
                <div>
                  <div className="text-lg font-semibold text-ink">{formatYuanFromQuota(info.checkin.total_quota, quotaPerYuan)}</div>
                  <div className="text-xs text-ink-3">{t('portal.referral.totalReward')}</div>
                </div>
              </div>
              {info.checkin.enabled && (
                <Button variant="primary" className="w-full" disabled={info.checkin.checked_today} onClick={handleCheckin}>
                  {info.checkin.checked_today ? t('portal.referral.comeBackTomorrow') : t('portal.referral.checkin')}
                </Button>
              )}
            </div>
          ) : (
            <div className="mt-3 text-[13px] text-ink-3">{t('common.state.loading')}</div>
          )}
        </Card>
      </div>

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label={t('portal.referral.totalReward')} value={info ? formatYuanFromQuota(info.total_reward_quota, quotaPerYuan) : '—'} />
      </div>

      <Card padding="none">
        <div className="border-b border-line px-4 py-3 text-sm font-semibold text-ink-2">{t('portal.referral.detail')}</div>
        <DataTable columns={columns} rows={loading ? null : rewards} loading={loading} rowKey={(row) => row.id} emptyTitle={t('portal.referral.empty')} />
        <div className="px-4 pb-3">
          <Pagination page={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} />
        </div>
      </Card>
    </div>
  )
}