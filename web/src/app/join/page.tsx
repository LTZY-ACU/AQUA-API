/** 加入交流群页（/join）：群卡片列表。
 *
 * 意图（Why）：
 *   落地页按钮跳到这里，访客先了解「有几个群、哪个是主群」再决定加哪个。
 */
'use client'

import { AppIcon } from '@/components/AppIcon'
import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { Button } from '@/components/ui/Button'

const GROUPS = [
  {
    name: 'LTZY-API 主群',
    desc: '讨论使用问题、渠道接入、新功能预告。',
    url: 'https://qm.qq.com/q/jH8kWrKONr',
    primary: true,
  },
]

export default function JoinGroupsPage() {
  return (
    <>
      <SiteHeader transparent={false} />
      <main className="mx-auto max-w-6xl px-4 py-14 sm:px-6">
        <div className="text-center">
          <h1 className="text-2xl font-bold tracking-tight text-ink sm:text-3xl">加入交流群</h1>
          <p className="mx-auto mt-3 max-w-xl text-[15px] text-ink-2">
            遇到问题、想提需求，或只是聊聊模型技术——都可以进群交流。
          </p>
        </div>

        <div className="mx-auto mt-10 grid max-w-2xl gap-4 sm:grid-cols-1">
          {GROUPS.map((group) => (
            <div key={group.name} className="flex items-center justify-between gap-4 rounded-lg border border-line bg-card p-5">
              <div className="flex items-center gap-3">
                <span className="flex h-11 w-11 items-center justify-center rounded-md bg-brand/8 text-brand">
                  <AppIcon name="users" size={22} />
                </span>
                <div>
                  <div className="font-semibold text-ink">{group.name}</div>
                  <div className="mt-0.5 text-[13px] text-ink-3">{group.desc}</div>
                </div>
              </div>
              <a href={group.url} target="_blank" rel="noreferrer">
                <Button variant="primary">加入 <AppIcon name="external" size={14} /></Button>
              </a>
            </div>
          ))}
        </div>
      </main>
      <SiteFooter />
    </>
  )
}