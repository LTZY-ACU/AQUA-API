/** SiteHeader：公共站顶栏——技术站式「品牌 + 版本标签 + 分区导航」。
 *
 * 意图（Why）：
 *   用户否掉了个人博客风，要的是「技术站」。技术站顶栏的信息密度更高：
 *   - 左侧：品牌标 + 一个等宽的运行标签（版本 / 协议兼容），像工具站的状态条；
 *   - 中部：分区导航锚点（能力 / 接入 / 模型 / FAQ），等宽字体，扫视快；
 *   - 右侧：主题、语言与账号入口。
 *   仍保持「不悬浮」——顶部是普通文档流的一部分，滚动时随页面离开。
 *
 * 流转（Flow）：
 *   各公共页 → <SiteHeader /> → 导航锚点回到首页分区，模型走独立路由。
 *   文案一律走 t('site.*')（词条见 locales/<lang>/site.ts），随语言切换实时更新。
 */
'use client'

import Link from 'next/link'

import { AppIcon } from '@/components/AppIcon'
import { BrandLogo } from '@/components/BrandMark'
import { useI18n } from '@/i18n'
import { useAuth } from '@/lib/auth/auth-context'
import { useSite } from '@/lib/site/site-context'

import { LocaleSwitcher } from './LocaleSwitcher'
import { ThemeToggle } from './ThemeToggle'

/** 顶栏分区导航：锚点回到首页小节，模型量级足够大故单列路由 */
const NAV = [
  { href: '/#features', key: 'site.nav.features' },
  { href: '/#quickstart', key: 'site.nav.quickstart' },
  { href: '/models', key: 'site.nav.models' },
  { href: '/#faq', key: 'site.nav.faq' },
]

export function SiteHeader({ transparent: _transparent = false }: { transparent?: boolean }) {
  const { isLoggedIn, displayName } = useAuth()
  const { status, siteName } = useSite()
  const { t } = useI18n()

  return (
    <header className="border-b border-line bg-card">
      <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4 sm:px-6">
        <Link href="/" className="shrink-0" aria-label={t('site.header.backHome')}>
          <BrandLogo name={siteName} />
        </Link>

        {/* 运行标签：等宽 + 状态点，给技术站一个「在线」的信号 */}
        <span className="hidden shrink-0 items-center gap-1.5 rounded border border-line bg-surface px-2 py-0.5 font-mono text-[11px] text-ink-3 lg:inline-flex">
          <span className="h-1.5 w-1.5 rounded-full bg-ok" />
          v{status?.version || '2'} · {t('site.header.openaiCompatible')}
        </span>

        <nav className="ml-auto hidden items-center gap-0.5 md:flex">
          {NAV.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="rounded-md px-2.5 py-1.5 font-mono text-[13px] text-ink-2 transition hover:bg-ink/5 hover:text-ink"
            >
              {t(item.key)}
            </Link>
          ))}
        </nav>

        <div className="ml-auto flex items-center gap-1.5 md:ml-3">
          <ThemeToggle compact />
          <LocaleSwitcher compact />
          {isLoggedIn ? (
            <Link
              href="/console"
              className="flex items-center gap-1 rounded-md border border-line-2 px-2.5 py-1.5 font-mono text-[13px] text-ink-2 transition hover:border-brand hover:text-brand"
            >
              {displayName}
              <AppIcon name="chevron-right" size={14} />
            </Link>
          ) : (
            <>
              <Link
                href="/login"
                className="rounded-md px-2.5 py-1.5 font-mono text-[13px] text-ink-2 transition hover:text-ink"
              >
                {t('site.header.login')}
              </Link>
              <Link
                href="/register"
                className="rounded-md bg-brand px-3 py-1.5 font-mono text-[13px] font-medium text-on-brand transition hover:bg-brand/90"
              >
                {t('site.header.register')}
              </Link>
            </>
          )}
        </div>
      </div>
    </header>
  )
}
