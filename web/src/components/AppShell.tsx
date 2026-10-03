/** AppShell：功能层外壳（侧边导航 + 顶栏 + 内容区），用户门户与管理后台共用。
 *
 * 意图（Why）：
 *   门户 / 后台的布局骨架相同：左侧导航（分组）、顶部用户态与登出、右侧内容区。
 *   差异只有导航项与鉴权要求，因此收敛为一个组件，两个布局传入各自的 NavGroup。
 *
 * 流转（Flow）：
 *   console/layout.tsx 或 admin/layout.tsx → <AppShell groups={...}> → 子路由内容
 */
'use client'

import Link from 'next/link'
import { usePathname, useRouter } from 'next/navigation'
import { useState } from 'react'

import { BrandLogo } from '@/components/BrandMark'
import { AppIcon, type IconName } from '@/components/AppIcon'
import { LocaleSwitcher } from '@/components/site/LocaleSwitcher'
import { ThemeToggle } from '@/components/site/ThemeToggle'
import { useI18n } from '@/i18n'
import { useAuth } from '@/lib/auth/auth-context'

export interface ShellNavItem {
  label: string
  href: string
  icon: IconName
  /** 精确匹配时高亮（默认前缀匹配） */
  exact?: boolean
  /** 子导航项（如「游乐场」下的功能入口），渲染为缩进子项 */
  children?: ShellNavItem[]
}

export interface ShellNavGroup {
  title?: string
  items: ShellNavItem[]
}

interface AppShellProps {
  groups: ShellNavGroup[]
  /** 外壳品牌名 */
  brand: string
  children: React.ReactNode
}

/** 导航项列表：渲染单层项；项含 children 时缩进渲染为子项（PC / 移动共用）。
 *
 * onNavigate：移动端点击后关闭抽屉；桌面端不传。
 */
function NavItemList({
  items,
  isActive,
  onNavigate,
}: {
  items: ShellNavItem[]
  isActive: (item: ShellNavItem) => boolean
  onNavigate?: () => void
}) {
  return (
    <div className="space-y-0.5">
      {items.map((item) => {
        const active = isActive(item)
        return (
          <div key={item.href}>
            <Link
              href={item.href}
              onClick={onNavigate}
              className={`flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] transition ${
                active ? 'bg-brand/8 font-medium text-brand' : 'text-ink-2 hover:bg-ink/5 hover:text-ink'
              }`}
            >
              <AppIcon name={item.icon} size={17} className={active ? 'text-brand' : 'text-ink-3'} />
              {item.label}
            </Link>
            {item.children && item.children.length > 0 && (
              <div className="ml-3.5 mt-0.5 space-y-0.5 border-l border-line pl-2.5">
                {item.children.map((child) => {
                  const cActive = isActive(child)
                  return (
                    <Link
                      key={child.href}
                      href={child.href}
                      onClick={onNavigate}
                      className={`flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition ${
                        cActive ? 'bg-brand/8 font-medium text-brand' : 'text-ink-2 hover:bg-ink/5 hover:text-ink'
                      }`}
                    >
                      <AppIcon name={child.icon} size={15} className={cActive ? 'text-brand' : 'text-ink-3'} />
                      {child.label}
                    </Link>
                  )
                })}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

export function AppShell({ groups, brand, children }: AppShellProps) {
  const pathname = usePathname()
  const router = useRouter()
  const { user, displayName, signOut, isAdmin } = useAuth()
  const { t } = useI18n()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  const isActive = (item: ShellNavItem) => {
    if (item.exact) return pathname === item.href
    return pathname === item.href || pathname.startsWith(item.href + '/')
  }

  async function handleSignOut() {
    await signOut()
    router.replace(isAdmin ? '/admin/login' : '/login')
  }

  return (
    <div className="flex min-h-screen">
      {/* 桌面侧边栏 */}
      <aside className="fixed inset-y-0 left-0 z-20 hidden w-56 flex-col border-r border-line bg-card lg:flex">
        <Link href="/" className="flex h-14 items-center gap-2 border-b border-line px-4">
          <BrandLogo name={brand} />
        </Link>
        <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-4">
          {groups.map((group, gi) => (
            <div key={gi}>
              {group.title && (
                <div className="px-2 pb-1.5 text-xs font-medium tracking-wider text-ink-3">{group.title}</div>
              )}
              <NavItemList items={group.items} isActive={isActive} />
            </div>
          ))}
        </nav>
      </aside>

      {/* 移动端侧边栏抽屉 */}
      {sidebarOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="fixed inset-0 bg-ink/40" onClick={() => setSidebarOpen(false)} />
          <aside className="fixed inset-y-0 left-0 z-50 flex w-64 flex-col border-r border-line bg-card">
            <div className="flex h-14 items-center justify-between border-b border-line px-4">
              <span className="font-semibold text-ink">{brand}</span>
              <button type="button" onClick={() => setSidebarOpen(false)} aria-label={t('components.shell.closeMenu')}>
                <AppIcon name="close" size={18} className="text-ink-3" />
              </button>
            </div>
            <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-4">
              {groups.map((group, gi) => (
                <div key={gi}>
                  {group.title && <div className="px-2 pb-1.5 text-xs font-medium text-ink-3">{group.title}</div>}
                  <NavItemList items={group.items} isActive={isActive} onNavigate={() => setSidebarOpen(false)} />
                </div>
              ))}
            </nav>
          </aside>
        </div>
      )}

      {/* 内容区 */}
      <div className="flex min-w-0 flex-1 flex-col lg:pl-56">
        <header className="sticky top-0 z-10 flex h-14 items-center justify-between border-b border-line bg-surface/90 px-4 backdrop-blur lg:px-6">
          <button type="button" className="rounded p-1.5 text-ink-3 hover:bg-ink/5 lg:hidden" onClick={() => setSidebarOpen(true)} aria-label={t('components.shell.openMenu')}>
            <AppIcon name="menu" size={20} />
          </button>
          <div className="hidden text-[13px] text-ink-3 lg:block">{brand}</div>
          <div className="flex items-center gap-2">
            <ThemeToggle compact />
            <LocaleSwitcher compact />
            <div className="flex items-center gap-2 rounded-md border border-line bg-card px-2.5 py-1.5 text-[13px]">
              <span className="max-w-28 truncate text-ink-2">{displayName}</span>
              {user && user.role === 10 && <span className="rounded bg-brand/10 px-1 text-xs text-brand">{t('components.shell.roleAdmin')}</span>}
            </div>
            <button
              type="button"
              onClick={handleSignOut}
              className="flex items-center gap-1 rounded-md px-2 py-1.5 text-[13px] text-ink-3 transition hover:bg-ink/5 hover:text-err"
            >
              <AppIcon name="logout" size={15} />
              {t('components.shell.signOut')}
            </button>
          </div>
        </header>
        <main className="flex-1 px-4 py-6 lg:px-6">{children}</main>
      </div>
    </div>
  )
}