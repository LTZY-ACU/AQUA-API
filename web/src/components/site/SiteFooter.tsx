/** SiteFooter：公共站页脚——技术站式「品牌 + 多栏链接 + 合规条」。
 *
 * 意图（Why）：
 *   技术站的页脚承担「全站地图 + 合规公示」两件事：分区明确的链接矩阵让深层页面
 *   都能被爬到，底部合规条（备案号 / 联系邮箱）是支付与浏览器核验站点时最常看的位置。
 *   栏目用等宽小标题，与顶栏、正文的等宽标签呼应，形成统一的工程气质。
 *
 * 扩展（Extend）：
 *   新增链接：改下方 COLUMNS 数组即可；合规字段来自 /api/status，为空则自动不展示。
 */

'use client'

import Link from 'next/link'
import { useEffect, useState } from 'react'

import { AppIcon } from '@/components/AppIcon'
import { BrandLogo } from '@/components/BrandMark'
import { QqGroupEntry } from '@/components/site/QqGroupEntry'
import { useSite } from '@/lib/site/site-context'

const REPO_URL = 'https://github.com/LTZY-ACU/AQUA-API'

/** 页脚链接矩阵：三栏，标题走等宽小字 */
const COLUMNS: { title: string; links: { label: string; href: string; external?: boolean }[] }[] = [
  {
    title: '站点',
    links: [
      { label: '能力总览', href: '/#features' },
      { label: '接入指南', href: '/#quickstart' },
      { label: '模型与价格', href: '/models' },
      { label: '常见问题', href: '/#faq' },
    ],
  },
  {
    title: '合规',
    links: [
      { label: '用户协议', href: '/terms' },
      { label: '隐私政策', href: '/privacy' },
      { label: '联系方式', href: '/contact' },
      { label: '投诉举报', href: '/report' },
      { label: '安全致谢', href: '/security' },
    ],
  },
  {
    title: '开源',
    links: [{ label: '源码仓库（GitHub）', href: REPO_URL, external: true }],
  },
]

export function SiteFooter() {
  const { status, siteName } = useSite()
  // 年份在挂载后取值，避免 SSG 与浏览器时区差异触发 hydration 不一致。
  const [year, setYear] = useState(2026)
  useEffect(() => {
    setYear(new Date().getFullYear())
  }, [])

  return (
    <footer className="border-t border-line bg-card">
      <div className="mx-auto max-w-6xl px-4 py-12 sm:px-6">
        <div className="grid gap-9 sm:grid-cols-2 lg:grid-cols-4">
          {/* 品牌栏：一句话说明「这是什么」 */}
          <div className="lg:pr-6">
            <BrandLogo name={siteName} />
            <p className="mt-3 max-w-xs text-[13px] leading-relaxed text-ink-3">
              OpenAI 兼容的 LLM API 网关：多协议上游统一、精细计费、全量日志，单二进制可自托管。
            </p>
            <div className="mt-4">
              <QqGroupEntry />
            </div>
          </div>

          {COLUMNS.map((col) => (
            <div key={col.title}>
              <div className="font-mono text-[11px] uppercase tracking-wider text-ink-3">{col.title}</div>
              <ul className="mt-3 space-y-2">
                {col.links.map((link) =>
                  link.external ? (
                    <li key={link.href}>
                      <a
                        href={link.href}
                        target="_blank"
                        rel="noreferrer"
                        className="inline-flex items-center gap-1 text-[13px] text-ink-2 transition hover:text-brand"
                      >
                        {link.label}
                        <AppIcon name="external" size={13} className="text-ink-3" />
                      </a>
                    </li>
                  ) : (
                    <li key={link.href}>
                      <Link href={link.href} className="text-[13px] text-ink-2 transition hover:text-brand">
                        {link.label}
                      </Link>
                    </li>
                  ),
                )}
              </ul>
            </div>
          ))}
        </div>

        {/* 合规条：版权 + 备案 + 联系邮箱 */}
        <div className="mt-10 flex flex-col gap-2 border-t border-line pt-6 text-xs text-ink-3 sm:flex-row sm:items-center sm:justify-between">
          <span>
            © {year} {status?.operator_name || siteName} · MIT 许可证
          </span>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            {status?.icp_license && (
              <a href="https://beian.miit.gov.cn/" target="_blank" rel="noreferrer" className="transition hover:text-brand">
                {status.icp_license}
              </a>
            )}
            {status?.police_license && <span>{status.police_license}</span>}
            {status?.contact_email && (
              <a href={`mailto:${status.contact_email}`} className="transition hover:text-brand">
                {status.contact_email}
              </a>
            )}
          </div>
        </div>
      </div>
    </footer>
  )
}
