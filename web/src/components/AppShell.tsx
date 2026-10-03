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
  /** 导航文案；labelKey 存在时以 t(labelKey) 为准 */
  label: string
  /**
   * 词条键（如 'components.nav.admin.channels'）。
   *
   * 为什么不让本组件按 href 反查词条：那样每新增一个导航项就要改两个文件
   * （layout 与本组件），漏改时的表现是「导航项存在但点进去没有对应文案」——
   * 排查成本远高于调用方多写一个字符串。
   *
   * label 刻意保留为必填：它同时是 SSG 阶段的首屏内容与词条缺失时的兜底，
   * 两者都依赖一个非空的中文字面量。
   */
  labelKey?: string
  href: string
  icon: IconName
  /** 精确匹配时高亮（默认前缀匹配） */
  exact?: boolean
}

export interface ShellNavGroup {
  /** 分组标题；titleKey 存在时以 t(titleKey) 为准 */
  title?: string
  /** 分组标题词条键；缺失时回落到 title（理由同 ShellNavItem.labelKey） */
  titleKey?: string
  items: ShellNavItem[]
}

interface AppShellProps {
  groups: ShellNavGroup[]
  /** 外壳品牌名 */
  brand: string
  children: React.ReactNode
}

export function AppShell({ groups, brand, children }: AppShellProps) {
  const pathname = usePathname()
  const router = useRouter()
  const { user, displayName, signOut, isAdmin } = useAuth()
  const { t } = useI18n()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  /**
   * 取导航项显示文案。
   *
   * 词条缺失时回落到 label（而不是显示裸键名）：导航是全站每页都在的位置，
   * 键名裸露在侧边栏比中文更糟——用户看到 'components.nav.admin.channels'
   * 完全无从判断那是什么，而中文虽然语言不对，至少还看得懂。
   */
  const navLabel = (item: ShellNavItem) => resolveLabel(item.label, item.labelKey)

  /** 分组标题取词条，缺失时回落到中文（不显示裸键名）。 */
  const groupTitle = (group: ShellNavGroup) =>
    group.title ? resolveLabel(group.title, group.titleKey) : ''

  /**
   * 词条命中则用词条，否则回落到字面量。
   *
   * 不回落到「键名本身」是刻意的：导航与分组标题是全站每页都在的位置，
   * 键名裸露在侧边栏里用户完全无从判断那是什么，而中文虽然语言不对，
   * 至少还看得懂——前者是让用户困惑，后者只是让用户别扭。
   */
  const resolveLabel = (fallback: string, key?: string) => {
    if (!key) return fallback
    const text = t(key)
    return text === key ? fallback : text
  }

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
                <div className="px-2 pb-1.5 text-xs font-medium tracking-wider text-ink-3">
                  {groupTitle(group)}
                </div>
              )}
              <div className="space-y-0.5">
                {group.items.map((item) => {
                  const active = isActive(item)
                  return (
                    <Link
                      key={item.href}
                      href={item.href}
                      className={`flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] transition ${
                        active ? 'bg-brand/8 font-medium text-brand' : 'text-ink-2 hover:bg-ink/5 hover:text-ink'
                      }`}
                    >
                      <AppIcon name={item.icon} size={17} className={active ? 'text-brand' : 'text-ink-3'} />
                      {navLabel(item)}
                    </Link>
                  )
                })}
              </div>
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
              <button
                type="button"
                onClick={() => setSidebarOpen(false)}
                aria-label={t('components.drawer.close')}
              >
                <AppIcon name="close" size={18} className="text-ink-3" />
              </button>
            </div>
            <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-4">
              {groups.map((group, gi) => (
                <div key={gi}>
                  {group.title && (
                    <div className="px-2 pb-1.5 text-xs font-medium text-ink-3">{groupTitle(group)}</div>
                  )}
                  {group.items.map((item) => (
                    <Link
                      key={item.href}
                      href={item.href}
                      onClick={() => setSidebarOpen(false)}
                      className={`flex items-center gap-2.5 rounded-md px-2.5 py-2 text-[13px] ${
                        isActive(item) ? 'bg-brand/8 font-medium text-brand' : 'text-ink-2 hover:bg-ink/5'
                      }`}
                    >
                      <AppIcon name={item.icon} size={17} />
                      {navLabel(item)}
                    </Link>
                  ))}
                </div>
              ))}
            </nav>
          </aside>
        </div>
      )}

      {/* 内容区 */}
      <div className="flex min-w-0 flex-1 flex-col lg:pl-56">
        <header className="sticky top-0 z-10 flex h-14 items-center justify-between border-b border-line bg-surface/90 px-4 backdrop-blur lg:px-6">
          <button
            type="button"
            className="rounded p-1.5 text-ink-3 hover:bg-ink/5 lg:hidden"
            onClick={() => setSidebarOpen(true)}
            aria-label={t('components.shell.expandNav')}
          >
            <AppIcon name="menu" size={20} />
          </button>
          <div className="hidden text-[13px] text-ink-3 lg:block">{brand}</div>
          <div className="flex items-center gap-2">
            <ThemeToggle compact />
            <LocaleSwitcher compact />
            <div className="flex items-center gap-2 rounded-md border border-line bg-card px-2.5 py-1.5 text-[13px]">
              <span className="max-w-28 truncate text-ink-2">{displayName}</span>
              {user && user.role === 10 && (
                <span className="rounded bg-brand/10 px-1 text-xs text-brand">
                  {t('components.shell.roleAdmin')}
                </span>
              )}
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