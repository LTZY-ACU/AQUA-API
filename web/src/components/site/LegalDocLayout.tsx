/** 法律文档布局（React 版，等价旧 LegalDocLayout.vue）。
 *
 * 意图（Why）：
 *   用户协议 / 隐私政策 / 联系方式 / 投诉举报 / 安全致谢五类合规页共用
 *   同一排版：标题 + 内容 + 页脚。目录锚点随文档内容可选。
 */
'use client'

import { type ReactNode } from 'react'

import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { useI18n } from '@/i18n'

interface LegalDocLayoutProps {
  title: string
  /** 最后更新说明（可选） */
  updatedAt?: string
  children: ReactNode
}

export function LegalDocLayout({ title, updatedAt, children }: LegalDocLayoutProps) {
  const { t } = useI18n()
  return (
    <>
      <SiteHeader />
      <main className="mx-auto max-w-3xl px-4 py-12 sm:px-6">
        <div className="font-mono text-[12px] text-ink-3">
          <span className="text-brand">/</span> {t('site.legal.docBreadcrumb')}
        </div>
        <h1 className="mt-1.5 text-2xl font-bold tracking-tight text-ink sm:text-3xl">{title}</h1>
        {updatedAt && <div className="mt-2 font-mono text-[12px] text-ink-3">{updatedAt}</div>}
        <article className="prose-sm mt-8 rounded-lg border border-line bg-card p-6 sm:p-8">{children}</article>
      </main>
      <SiteFooter />
    </>
  )
}