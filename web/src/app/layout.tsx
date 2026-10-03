/** 根布局：全局 Provider + 基础文档元信息。
 *
 * 意图（Why）：
 *   所有页面共享：HTML 语言方向（i18n 管理）、全局样式、Provider 树，
 *   以及「昼 / 夜」主题的首帧预设脚本（在 hydration 前设 html.dark，避免先亮后暗的闪烁）。
 *   本层不包含任何定时渲染（SSG），只做装配。
 *
 * 流转（Flow）：
 *   <head> 内联脚本（localStorage + 北京时间 → html.dark）
 *   → providers.tsx（theme/i18n/toast/auth/site）→ 各页面
 */
import type { Metadata, Viewport } from 'next'

import { Providers } from '@/lib/providers'
import { THEME_INIT_SCRIPT } from '@/lib/theme/theme-context'

import './globals.css'

export const metadata: Metadata = {
  title: 'LTZY-API · 自托管 LLM API 网关',
  description: '自托管、可私有部署的 LLM API 网关：统一多协议上游、精细计费、全量日志与审计。',
  robots: { index: true, follow: true },
  icons: {
    icon: '/favicon.ico',
  },
}

/** 视口独立导出：Next.js 16 起 viewport 不再接受放在 metadata（否则告警且不生效） */
export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        {/* 防闪烁：首帧按「用户偏好 + 北京时间」预设主题；必须内联且在 body 之前 */}
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />
      </head>
      <body className="min-h-screen">
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
