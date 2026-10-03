/** 管理后台布局（/admin/*，route group (panel)）：鉴权守卫（管理员）+ 后台导航。
 *
 * 意图（Why）：
 *   后台全部页面需要「登录 + 管理员」双重权限。守卫在布局层统一完成，
 *   页面组件不必各自判断。导航按功能域分组（资源 / 合规 / 运维）。
 *
 * 流转（Flow）：
 *   /admin/login 放在 (panel) 之外独立渲染，不经过本守卫外壳，
 *   避免「要登录后台才能看到登录页」死循环；其余 /admin/* 全部套本布局。
 */
'use client'

import { usePathname, useRouter } from 'next/navigation'
import { useEffect } from 'react'

import { AppShell, type ShellNavGroup } from '@/components/AppShell'
import { useAuth } from '@/lib/auth/auth-context'
import { useToast } from '@/lib/toast/toast-context'

const GROUPS: ShellNavGroup[] = [
  {
    title: '总览',
    titleKey: 'components.nav.admin.overview',
    items: [
      { label: '仪表盘', href: '/admin', labelKey: 'components.nav.admin.dashboard', icon: 'home', exact: true },
      { label: '模型广场', href: '/admin/models', labelKey: 'components.nav.admin.models', icon: 'grid' },
    ],
  },
  {
    title: '资源',
    titleKey: 'components.nav.admin.resources',
    items: [
      { label: '渠道管理', href: '/admin/channels', labelKey: 'components.nav.admin.channels', icon: 'server' },
      { label: '渠道健康', href: '/admin/channel-health', labelKey: 'components.nav.admin.channelHealth', icon: 'chart' },
      { label: '模型映射', href: '/admin/model-mappings', labelKey: 'components.nav.admin.modelMappings', icon: 'layers' },
      { label: '模型分组', href: '/admin/groups', labelKey: 'components.nav.admin.groups', icon: 'tag' },
      { label: '计价规则', href: '/admin/prices', labelKey: 'components.nav.admin.prices', icon: 'quota' },
      { label: '异步任务', href: '/admin/tasks', labelKey: 'components.nav.admin.tasks', icon: 'image' },
    ],
  },
  {
    title: '业务',
    items: [
      { label: '充值订单', href: '/admin/orders', labelKey: 'components.nav.admin.orders', icon: 'cart' },
      { label: '财务对账', href: '/admin/finance', labelKey: 'components.nav.admin.finance', icon: 'wallet' },
      { label: '订阅账号', href: '/admin/oauth', labelKey: 'components.nav.admin.oauth', icon: 'globe' },
      { label: '令牌管理', href: '/admin/tokens', labelKey: 'components.nav.admin.tokens', icon: 'key' },
      { label: '用户管理', href: '/admin/users', labelKey: 'components.nav.admin.users', icon: 'users' },
      { label: '兑换码', href: '/admin/redeem-codes', labelKey: 'components.nav.admin.redeemCodes', icon: 'tag' },
    ],
  },
  {
    title: '合规与运维',
    titleKey: 'components.nav.admin.operations',
    items: [
      { label: '调用日志', href: '/admin/logs', labelKey: 'components.nav.admin.logs', icon: 'list' },
      { label: '操作审计', href: '/admin/audit-logs', labelKey: 'components.nav.admin.audit', icon: 'shield' },
      { label: '站点公告', href: '/admin/announcements', labelKey: 'components.nav.admin.announcements', icon: 'info' },
      { label: '告警通道', href: '/admin/alert-channels', labelKey: 'components.nav.admin.alertChannels', icon: 'alert' },
      { label: '内容安全', href: '/admin/sensitive-words', labelKey: 'components.nav.admin.sensitiveWords', icon: 'filter' },
      { label: '运维监控', href: '/admin/maintenance', labelKey: 'components.nav.admin.maintenanceMonitor', icon: 'trend' },
      { label: '系统设置', href: '/admin/settings', labelKey: 'components.nav.admin.settings', icon: 'sliders' },
    ],
  },
]

export default function AdminLayout({ children }: { children: React.ReactNode }) {
  const { ready, isLoggedIn, isAdmin } = useAuth()
  const pathname = usePathname()
  const router = useRouter()
  const { toastError } = useToast()

  useEffect(() => {
    if (!ready) return
    // /admin/login 在 (panel) 之外独立渲染，不受本守卫约束（结构上已隔离）
    if (!isLoggedIn) {
      router.replace(`/admin/login?redirect=${encodeURIComponent(pathname)}`)
    } else if (!isAdmin) {
      toastError('没有权限访问管理后台')
      router.replace('/console')
    }
  }, [ready, isLoggedIn, isAdmin, pathname, router, toastError])

  if (!ready || !isLoggedIn || !isAdmin) {
    return <div className="flex min-h-screen items-center justify-center text-[13px] text-ink-3">正在进入管理后台…</div>
  }

  return (
    <AppShell groups={GROUPS} brand="管理后台">
      {children}
    </AppShell>
  )
}