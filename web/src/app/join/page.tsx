/** 加入交流群页（/join）：群卡片列表。
 *
 * 意图（Why）：
 *   落地页按钮跳到这里，访客先了解「有几个群、哪个是主群」再决定加哪个。
 *
 * 流转（Flow）：
 *   SiteHeader/Footer + 群卡片；卡片文案一律走 t('site.legal.join.*')，
 *   群数据只保留加群链接与词条键，新增群时补一条数据 + 两个词条键即可。
 */
'use client'

import { AppIcon } from '@/components/AppIcon'
import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { Button } from '@/components/ui/Button'
import { useI18n } from '@/i18n'

const GROUPS = [
  {
    nameKey: 'site.legal.join.groupMainName',
    descKey: 'site.legal.join.groupMainDesc',
    url: 'https://qm.qq.com/q/jH8kWrKONr',
    primary: true,
  },
]

export default function JoinGroupsPage() {
  const { t } = useI18n()
  return (
    <>
      <SiteHeader transparent={false} />
      <main className="mx-auto max-w-6xl px-4 py-14 sm:px-6">
        <div className="text-center">
          <h1 className="text-2xl font-bold tracking-tight text-ink sm:text-3xl">{t('site.legal.join.title')}</h1>
          <p className="mx-auto mt-3 max-w-xl text-[15px] text-ink-2">{t('site.legal.join.subtitle')}</p>
        </div>

        <div className="mx-auto mt-10 grid max-w-2xl gap-4 sm:grid-cols-1">
          {GROUPS.map((group) => (
            <div key={group.url} className="flex items-center justify-between gap-4 rounded-lg border border-line bg-card p-5">
              <div className="flex items-center gap-3">
                <span className="flex h-11 w-11 items-center justify-center rounded-md bg-brand/8 text-brand">
                  <AppIcon name="users" size={22} />
                </span>
                <div>
                  <div className="font-semibold text-ink">{t(group.nameKey)}</div>
                  <div className="mt-0.5 text-[13px] text-ink-3">{t(group.descKey)}</div>
                </div>
              </div>
              <a href={group.url} target="_blank" rel="noreferrer">
                <Button variant="primary">{t('site.legal.join.joinButton')} <AppIcon name="external" size={14} /></Button>
              </a>
            </div>
          ))}
        </div>
      </main>
      <SiteFooter />
    </>
  )
}
