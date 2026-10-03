/** 用户门户布局（/console/*）：鉴权守卫 + 门户导航。
 *
 * 意图（Why）：
 *   所有门户页面都需要登录。此处用 AuthProvider 的 ready/isLoggedIn 做客户端守卫，
 *   未登录跳转 /login?redirect=当前路径；已登录套 AppShell。
 */
'use client'

import { usePathname, useRouter } from 'next/navigation'
import { useEffect } from 'react'

import { AppShell, type ShellNavGroup } from '@/components/AppShell'
import { ComplianceGate } from '@/components/site/ComplianceGate'
import { useI18n } from '@/i18n'
import { useAuth } from '@/lib/auth/auth-context'

export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const { ready, isLoggedIn } = useAuth()
  const { t } = useI18n()
  const pathname = usePathname()
  const router = useRouter()

  // 导航分组随语言切换实时更新，故在组件内按 t() 构建（文案见 portal.nav.*）
  const groups: ShellNavGroup[] = [
    {
      title: t('portal.nav.groupOverview'),
      items: [
        { label: t('portal.nav.overview'), href: '/console', icon: 'home', exact: true },
        { label: t('portal.nav.models'), href: '/console/models', icon: 'grid' },
      ],
    },
    {
      title: t('portal.nav.groupAccess'),
      items: [
        { label: t('portal.nav.tokens'), href: '/console/tokens', icon: 'key' },
        { label: t('portal.nav.docs'), href: '/console/docs', icon: 'book' },
        {
          label: t('portal.nav.playground'),
          href: '/console/playground',
          icon: 'play',
          // exact：只让「游乐场」页本身高亮。否则进子页（任意门）时，
          // 前缀匹配会让父项与子项同时高亮，视觉上分不清当前所在层级。
          exact: true,
          children: [{ label: t('portal.nav.anydoor'), href: '/console/playground/anydoor', icon: 'door' }],
        },
      ],
    },
    {
      title: t('portal.nav.groupMine'),
      items: [
        { label: t('portal.nav.logs'), href: '/console/logs', icon: 'list' },
        { label: t('portal.nav.tasks'), href: '/console/tasks', icon: 'image' },
        { label: t('portal.nav.finance'), href: '/console/finance', icon: 'wallet' },
        { label: t('portal.nav.recharge'), href: '/console/recharge', icon: 'cart' },
        { label: t('portal.nav.referral'), href: '/console/referral', icon: 'users' },
      ],
    },
  ]

  useEffect(() => {
    if (ready && !isLoggedIn) {
      router.replace(`/login?redirect=${encodeURIComponent(pathname)}`)
    }
  }, [ready, isLoggedIn, pathname, router])

  if (!ready || !isLoggedIn) {
    return <div className="flex min-h-screen items-center justify-center text-[13px] text-ink-3">{t('portal.layout.entering')}</div>
  }

  return (
    <AppShell groups={groups} brand={t('components.shell.portal')}>
      {/* 首次进入控制台弹一次合规确认（自包含：内部判断路由与已确认状态） */}
      <ComplianceGate />
      {children}
    </AppShell>
  )
}