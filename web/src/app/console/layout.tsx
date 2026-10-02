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
import { useAuth } from '@/lib/auth/auth-context'

const GROUPS: ShellNavGroup[] = [
  {
    title: '总览',
    items: [
      { label: '概览', href: '/console', icon: 'home', exact: true },
      { label: '模型广场', href: '/console/models', icon: 'grid' },
    ],
  },
  {
    title: '接入',
    items: [
      { label: '访问令牌', href: '/console/tokens', icon: 'key' },
      { label: '接入示例', href: '/#quickstart', icon: 'book' },
      { label: '游乐场', href: '/console/playground', icon: 'play' },
    ],
  },
  {
    title: '我的',
    items: [
      { label: '调用日志', href: '/console/logs', icon: 'list' },
      { label: '生成任务', href: '/console/tasks', icon: 'image' },
      { label: '财务记录', href: '/console/finance', icon: 'wallet' },
      { label: '账户充值', href: '/console/recharge', icon: 'cart' },
      { label: '邀请奖励', href: '/console/referral', icon: 'users' },
      { label: '第三方账号', href: '/console/accounts', icon: 'external' },
    ],
  },
]

export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const { ready, isLoggedIn } = useAuth()
  const pathname = usePathname()
  const router = useRouter()

  useEffect(() => {
    if (ready && !isLoggedIn) {
      router.replace(`/login?redirect=${encodeURIComponent(pathname)}`)
    }
  }, [ready, isLoggedIn, pathname, router])

  if (!ready || !isLoggedIn) {
    return <div className="flex min-h-screen items-center justify-center text-[13px] text-ink-3">正在进入门户…</div>
  }

  return (
    <AppShell groups={GROUPS} brand="用户门户">
      {children}
    </AppShell>
  )
}