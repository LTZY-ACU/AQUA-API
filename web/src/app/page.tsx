/** 落地页（App Router 首页）：开发者技术站 · 工程文档气质。
 *
 * 意图（Why）：
 *   用户先否掉「企业官网商业化」，再否掉「个人博客」——要的是一个**技术站**。
 *   因此这里改用工程文档的排版语言，而不是散文或营销卡片：
 *   - 分区带等宽编号（01 / 02 …），像规格说明书的小节；
 *   - Hero 左右分栏：左边一句结论，右边一个模拟终端窗口（请求 + 响应）；
 *   - 能力用发丝线网格矩阵（gap-px + 背景线）承载，信息密度高、无卡片浮起感；
 *   - 关键指标走「规格条」，模型走等宽表格（模型 / 分组 / 价格）。
 *
 * 视觉原则：
 *   - 字体：正文无衬线，标签与数据一律等宽（font-mono），形成「工具站」的识别度；
 *   - 强调收敛：唯一品牌色（青）只出现在编号、链接与主行动；
 *   - 装饰克制：层次靠发丝描边与网格底纹，不靠投影与大圆角。
 *
 * 流转（Flow）：
 *   SiteHeader → Hero(结论 + 终端) → 规格条 → 01 能力 → 02 接入 → 03 模型
 *   → 04 取舍与成本 → 05 FAQ → CTA → SiteFooter
 */
'use client'

import Link from 'next/link'
import { useEffect, useMemo, useState } from 'react'

import { fetchModelPlaza } from '@/api/site'
import type { ModelPlaza, PlazaModel, PlazaPrice } from '@/api/types'
import { AppIcon, type IconName } from '@/components/AppIcon'
import { Button } from '@/components/ui/Button'
import { CodeBlock } from '@/components/ui/Display'
import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { QqGroupEntry } from '@/components/site/QqGroupEntry'
import { useI18n } from '@/i18n'
import { useAuth } from '@/lib/auth/auth-context'
import { useSite } from '@/lib/site/site-context'
import { formatDiscountLabel, formatYuanPerCall, formatYuanPerMillion } from '@/utils/money'

const REPO_URL = 'https://github.com/LTZY-ACU/LTZY-API'

/* ── 小节头：等宽编号 + 标题 + 说明 ─────────────────────── */

function SectionHead({ index, title, desc, anchorId }: { index: string; title: string; desc?: string; anchorId?: string }) {
  return (
    <div id={anchorId} className="scroll-mt-20">
      <div className="flex items-baseline gap-3">
        <span className="font-mono text-[12px] font-medium text-brand">{index}</span>
        <h2 className="text-xl font-bold tracking-tight text-ink sm:text-2xl">{title}</h2>
      </div>
      {desc && <p className="mt-2.5 max-w-2xl text-[14px] leading-relaxed text-ink-2">{desc}</p>}
    </div>
  )
}

/* ── Hero：左结论 + 右终端 ─────────────────────────────── */

function Hero() {
  const { status } = useSite()
  const { isLoggedIn } = useAuth()
  const { t } = useI18n()
  const sampleModel = status?.models?.[0] || 'LTZY-CALL/deepseek-v4-flash'

  const [origin, setOrigin] = useState('https://ltzy.top')
  useEffect(() => {
    if (typeof window !== 'undefined') setOrigin(window.location.origin)
  }, [])

  const terminal = `$ curl ${origin}/v1/chat/completions \\
    -H "Authorization: Bearer sk-••••••••" \\
    -d '{"model":"${sampleModel}",
         "messages":[{"role":"user","content":"${t('site.home.hero.terminalUserMessage')}"}]}

# HTTP/1.1 200 OK
{
  "id": "chatcmpl-9f3a1c",
  "object": "chat.completion",
  "model": "${sampleModel}",
  "choices": [{
    "index": 0,
    "message": { "role": "assistant", "content": "${t('site.home.hero.terminalAssistantReply')}" },
    "finish_reason": "stop"
  }],
  "usage": { "prompt_tokens": 9, "completion_tokens": 7, "total_tokens": 16 }
}`

  return (
    <section className="grid-bg border-b border-line">
      <div className="mx-auto grid max-w-6xl gap-10 px-4 py-14 sm:px-6 lg:grid-cols-[1.02fr_1fr] lg:items-center lg:py-20">
        <div>
          <div className="font-mono text-[12px] text-ink-3">
            <span className="text-brand">$</span> {t('site.home.hero.tagline')}
          </div>

          <h1 className="mt-4 text-3xl font-bold leading-[1.15] tracking-tight text-ink sm:text-[40px]">
            {t('site.home.hero.titleLine1')}
            <br />
            {t('site.home.hero.titleLine2')}
          </h1>

          <p className="mt-5 max-w-xl text-[15px] leading-[1.85] text-ink-2">{t('site.home.hero.desc')}</p>

          <div className="mt-7 flex flex-wrap gap-3">
            <Link href={isLoggedIn ? '/console' : '/register'}>
              <Button variant="primary" size="lg">
                {isLoggedIn ? t('site.home.action.console') : t('site.home.action.register')}
                <AppIcon name="chevron-right" size={16} />
              </Button>
            </Link>
            <Link href="/models">
              <Button variant="secondary" size="lg">
                {t('site.home.action.browseModels')}
              </Button>
            </Link>
            <QqGroupEntry variant="button" />
          </div>

          <div className="mt-7 inline-flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-line bg-card px-3.5 py-2.5 font-mono text-[12px] text-ink-2">
            <span className="text-ink-3">base_url</span>
            <span className="text-brand">{origin}/v1</span>
          </div>
        </div>

        {/* 终端窗口：真实请求 → 真实响应，技术站最直接的说服方式 */}
        <div className="overflow-hidden rounded-lg border border-line bg-code-bg text-code-fg shadow-pop">
          <div className="flex items-center gap-3 border-b border-white/10 px-4 py-2.5">
            <span className="term-dots inline-block h-2.5 w-8" aria-hidden />
            <span className="font-mono text-[11px] text-white/50">quickstart.sh</span>
          </div>
          <pre className="code-block overflow-x-auto p-4 text-[12.5px] leading-[1.75] text-white/90">{terminal}</pre>
        </div>
      </div>
    </section>
  )
}

/* ── 规格条：关键指标 ───────────────────────────────────── */

function SpecBar() {
  const { status } = useSite()
  const { t } = useI18n()
  const stats = useMemo(() => {
    const items: { value: string; label: string }[] = []
    items.push({ value: status?.models?.length ? String(status.models.length) : '—', label: t('site.home.stats.modelsOnline') })
    items.push({ value: '79', label: t('site.home.stats.channelTypes') })
    items.push({ value: '3', label: t('site.home.stats.protocols') })
    items.push({ value: '1', label: t('site.home.stats.deployFiles') })
    return items
  }, [status, t])

  return (
    <section className="border-b border-line bg-card">
      <div className="mx-auto flex max-w-6xl flex-col divide-y divide-line px-4 sm:px-6 lg:flex-row lg:divide-x lg:divide-y-0">
        {stats.map((s) => (
          <div key={s.label} className="flex-1 px-0 py-5 lg:px-6 lg:first:pl-0 lg:last:pr-0">
            <div className="font-mono text-2xl font-semibold tabular-nums tracking-tight text-ink">{s.value}</div>
            <div className="mt-1 text-[12px] text-ink-3">{s.label}</div>
          </div>
        ))}
      </div>
    </section>
  )
}

/* ── 01 能力矩阵 ────────────────────────────────────────── */

const FEATURES: { index: string; icon: IconName; titleKey: string; descKey: string }[] = [
  { index: '01', icon: 'layers', titleKey: 'site.home.features.f1Title', descKey: 'site.home.features.f1Desc' },
  { index: '02', icon: 'quota', titleKey: 'site.home.features.f2Title', descKey: 'site.home.features.f2Desc' },
  { index: '03', icon: 'key', titleKey: 'site.home.features.f3Title', descKey: 'site.home.features.f3Desc' },
  { index: '04', icon: 'list', titleKey: 'site.home.features.f4Title', descKey: 'site.home.features.f4Desc' },
  { index: '05', icon: 'refresh', titleKey: 'site.home.features.f5Title', descKey: 'site.home.features.f5Desc' },
  { index: '06', icon: 'server', titleKey: 'site.home.features.f6Title', descKey: 'site.home.features.f6Desc' },
]

function Features() {
  const { t } = useI18n()
  return (
    <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
      <SectionHead
        anchorId="features"
        index="01"
        title={t('site.home.features.title')}
        desc={t('site.home.features.desc')}
      />
      {/* 发丝线网格：gap-px 露出底色形成 1px 分隔，比卡片投影更像工程图谱 */}
      <div className="mt-8 grid gap-px overflow-hidden rounded-lg border border-line bg-line sm:grid-cols-2 lg:grid-cols-3">
        {FEATURES.map((f) => (
          <div key={f.index} className="bg-card p-5">
            <div className="flex items-center justify-between">
              <span className="flex h-8 w-8 items-center justify-center rounded border border-line-2 bg-surface text-brand">
                <AppIcon name={f.icon} size={16} />
              </span>
              <span className="font-mono text-[11px] text-ink-3">{f.index}</span>
            </div>
            <h3 className="mt-3.5 text-[15px] font-semibold text-ink">{t(f.titleKey)}</h3>
            <p className="mt-1.5 text-[13px] leading-relaxed text-ink-2">{t(f.descKey)}</p>
          </div>
        ))}
      </div>
    </section>
  )
}

/* ── 02 接入 ────────────────────────────────────────────── */

const STEPS = [
  { titleKey: 'site.home.quickstart.s1Title', descKey: 'site.home.quickstart.s1Desc' },
  { titleKey: 'site.home.quickstart.s2Title', descKey: 'site.home.quickstart.s2Desc' },
  { titleKey: 'site.home.quickstart.s3Title', descKey: 'site.home.quickstart.s3Desc' },
]

function Quickstart() {
  const { t } = useI18n()
  const [origin, setOrigin] = useState('https://ltzy.top')
  useEffect(() => {
    if (typeof window !== 'undefined') setOrigin(window.location.origin)
  }, [])

  const curl = `curl ${origin}/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer sk-${t('site.home.quickstart.tokenPlaceholder')}" \\
  -d '{
    "model": "deepseek-v3",
    "messages": [{ "role": "user", "content": "${t('site.home.quickstart.sampleMessage')}" }]
  }'`

  return (
    <section className="border-y border-line bg-card">
      <div className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
        <SectionHead
          anchorId="quickstart"
          index="02"
          title={t('site.home.quickstart.title')}
          desc={t('site.home.quickstart.desc')}
        />
        <div className="mt-8 grid gap-10 lg:grid-cols-[0.8fr_1.2fr]">
          <ol className="space-y-6">
            {STEPS.map((s, i) => (
              <li key={s.titleKey} className="flex gap-4">
                <span className="font-mono text-[13px] font-medium text-brand">0{i + 1}</span>
                <div>
                  <div className="text-[14px] font-medium text-ink">{t(s.titleKey)}</div>
                  <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{t(s.descKey)}</p>
                </div>
              </li>
            ))}
          </ol>
          <div>
            <CodeBlock code={curl} language="bash" title="bash" />
            <p className="mt-3 font-mono text-[12px] text-ink-3">
              {t('site.home.quickstart.sdkNote', { base: `${origin}/v1` })}
            </p>
          </div>
        </div>
      </div>
    </section>
  )
}

/* ── 03 模型表格 ────────────────────────────────────────── */

/** 翻译函数签名（组件内 useI18n().t），供组件外的小工具函数接收使用 */
type TranslateFn = (key: string, vars?: Record<string, string | number>) => string

/** 价格摘要：一律换算成人民币展示（内部仍以整数额度记账） */
function priceLabel(price: PlazaPrice | undefined, quotaPerYuan: number, t: TranslateFn): string {
  if (!price) return t('site.home.models.priceTbd')
  if (price.is_free || price.billing_mode === 'free') return t('site.home.models.priceFree')
  if (price.billing_mode === 'per_call') {
    return price.per_call_price > 0
      ? formatYuanPerCall(price.per_call_price, quotaPerYuan)
      : t('site.home.models.pricePerCall')
  }
  const prompt = price.prompt_price
  return prompt > 0 ? formatYuanPerMillion(prompt, quotaPerYuan) : t('site.home.models.pricePerToken')
}

function ModelPreview() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [plaza, setPlaza] = useState<ModelPlaza | null>(null)

  useEffect(() => {
    void fetchModelPlaza().then(setPlaza).catch(() => setPlaza(null))
  }, [])

  const rows: PlazaModel[] = plaza?.items?.slice(0, 8) ?? []
  const viewer = plaza?.viewer

  return (
    <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
      <SectionHead
        anchorId="models"
        index="03"
        title={t('site.home.models.title')}
        desc={viewer ? t('site.home.models.descAgent') : t('site.home.models.descPublic')}
      />
      <div className="mt-8 overflow-hidden rounded-lg border border-line">
        <table className="w-full text-left text-[13px]">
          <thead className="bg-surface">
            <tr className="font-mono text-[11px] uppercase tracking-wider text-ink-3">
              <th className="px-4 py-2.5 font-normal">{t('site.home.models.colModel')}</th>
              <th className="hidden px-4 py-2.5 font-normal sm:table-cell">{t('site.home.models.colGroup')}</th>
              <th className="px-4 py-2.5 text-right font-normal">{t('site.home.models.colPrice')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {rows.length === 0
              ? Array.from({ length: 5 }).map((_, i) => (
                  <tr key={i} className="bg-card">
                    <td colSpan={3} className="px-4 py-3">
                      <span className="block h-3.5 w-52 animate-pulse rounded bg-ink/8" />
                    </td>
                  </tr>
                ))
              : rows.map((m) => (
                  <tr key={m.model} className="bg-card transition hover:bg-surface/60">
                    <td className="px-4 py-2.5 font-mono text-ink">{m.model}</td>
                    <td className="hidden px-4 py-2.5 font-mono text-ink-3 sm:table-cell">
                      {m.groups?.join(' / ') || '—'}
                    </td>
                    <td className="px-4 py-2.5 text-right text-ink-2">
                      {viewer ? (
                        <span className="inline-flex items-center justify-end gap-2">
                          {m.list_price && (
                            <span className="text-[12px] text-ink-3 line-through decoration-ink-3/70">
                              {priceLabel(m.list_price, quotaPerYuan, t)}
                            </span>
                          )}
                          <span className="inline-flex items-center gap-1.5 rounded border border-warn/40 bg-warn/15 px-2 py-0.5 font-mono text-[12px] font-medium text-warn">
                            {priceLabel(m.prices?.[0], quotaPerYuan, t)}
                            <span className="opacity-70">· {formatDiscountLabel(viewer.ratio)}</span>
                          </span>
                        </span>
                      ) : (
                        priceLabel(m.prices?.[0], quotaPerYuan, t)
                      )}
                    </td>
                  </tr>
                ))}
          </tbody>
        </table>
      </div>
      <Link href="/models" className="mt-4 inline-flex items-center gap-1 text-[13px] font-medium text-brand hover:underline">
        {plaza?.total
          ? t('site.home.models.viewAllCount', { n: plaza.total })
          : t('site.home.models.viewAll')}
        <AppIcon name="chevron-right" size={14} />
      </Link>
    </section>
  )
}

/* ── 04 取舍与成本 ──────────────────────────────────────── */

const PRINCIPLES: [string, string][] = [
  ['site.home.design.p1Title', 'site.home.design.p1Desc'],
  ['site.home.design.p2Title', 'site.home.design.p2Desc'],
  ['site.home.design.p3Title', 'site.home.design.p3Desc'],
]

const COSTS: [string, string][] = [
  ['site.home.design.c1Label', 'site.home.design.c1Value'],
  ['site.home.design.c2Label', 'site.home.design.c2Value'],
  ['site.home.design.c3Label', 'site.home.design.c3Value'],
  ['site.home.design.c4Label', 'site.home.design.c4Value'],
]

function Design() {
  const { t } = useI18n()
  return (
    <section className="border-y border-line bg-card">
      <div className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
        <SectionHead
          index="04"
          title={t('site.home.design.title')}
          desc={t('site.home.design.desc')}
        />
        <div className="mt-8 grid gap-10 lg:grid-cols-2">
          <div>
            <div className="font-mono text-[11px] uppercase tracking-wider text-ink-3">{t('site.home.design.principlesLabel')}</div>
            <ul className="mt-4 space-y-4">
              {PRINCIPLES.map(([title, desc]) => (
                <li key={title} className="border-l-2 border-brand/30 pl-3.5">
                  <div className="text-[14px] font-medium text-ink">{t(title)}</div>
                  <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{t(desc)}</p>
                </li>
              ))}
            </ul>
          </div>
          <div>
            <div className="font-mono text-[11px] uppercase tracking-wider text-ink-3">{t('site.home.design.costsLabel')}</div>
            <div className="mt-4 divide-y divide-line rounded-lg border border-line">
              {COSTS.map(([k, v]) => (
                <div key={k} className="flex flex-col gap-0.5 px-4 py-2.5 sm:flex-row sm:items-center sm:justify-between">
                  <span className="text-[13px] font-medium text-ink-2">{t(k)}</span>
                  <span className="font-mono text-[12px] text-ink-3">{t(v)}</span>
                </div>
              ))}
            </div>
            <p className="mt-3 rounded-md border border-brand/25 bg-brand/5 p-3.5 text-[12.5px] leading-relaxed text-ink-2">
              {t('site.home.design.costNotePrefix')}
              <b className="font-medium text-ink">{t('site.home.design.costNoteBold')}</b>
              {t('site.home.design.costNoteSuffix')}
            </p>
          </div>
        </div>
      </div>
    </section>
  )
}

/* ── 05 FAQ ─────────────────────────────────────────────── */

const FAQS = [
  { qKey: 'site.home.faq.q1', aKey: 'site.home.faq.a1' },
  { qKey: 'site.home.faq.q2', aKey: 'site.home.faq.a2' },
  { qKey: 'site.home.faq.q3', aKey: 'site.home.faq.a3' },
  { qKey: 'site.home.faq.q4', aKey: 'site.home.faq.a4' },
  { qKey: 'site.home.faq.q5', aKey: 'site.home.faq.a5' },
  { qKey: 'site.home.faq.q6', aKey: 'site.home.faq.a6' },
]

function Faq() {
  const { t } = useI18n()
  const [open, setOpen] = useState<number | null>(0)
  return (
    <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
      <SectionHead anchorId="faq" index="05" title={t('site.home.faq.title')} />
      <div className="mt-6 grid gap-x-10 gap-y-0 md:grid-cols-2">
        {FAQS.map((faq, index) => {
          const active = open === index
          return (
            <div key={faq.qKey} className="border-b border-line">
              <button
                type="button"
                onClick={() => setOpen(active ? null : index)}
                className="flex w-full items-center gap-3 py-3.5 text-left"
                aria-expanded={active}
              >
                <span className="font-mono text-[12px] text-ink-3">{String(index + 1).padStart(2, '0')}</span>
                <span className="flex-1 text-[14px] font-medium text-ink">{t(faq.qKey)}</span>
                <AppIcon
                  name="chevron-down"
                  size={15}
                  className={`shrink-0 text-ink-3 transition-transform ${active ? 'rotate-180' : ''}`}
                />
              </button>
              {active && <p className="pb-4 pl-[30px] pr-6 text-[13px] leading-[1.85] text-ink-2">{t(faq.aKey)}</p>}
            </div>
          )
        })}
      </div>
    </section>
  )
}

/* ── CTA ────────────────────────────────────────────────── */

function Cta() {
  const { isLoggedIn } = useAuth()
  const { t } = useI18n()
  return (
    <section className="border-t border-line bg-card">
      <div className="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-14 sm:px-6 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight text-ink sm:text-3xl">
            {isLoggedIn ? t('site.home.cta.titleLoggedIn') : t('site.home.cta.titleGuest')}
          </h2>
          <p className="mt-2.5 max-w-xl text-[14px] leading-relaxed text-ink-2">
            {isLoggedIn ? t('site.home.cta.descLoggedIn') : t('site.home.cta.descGuest')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Link href={isLoggedIn ? '/console' : '/register'}>
            <Button variant="primary" size="lg">
              {isLoggedIn ? t('site.home.action.console') : t('site.home.action.register')}
              <AppIcon name="chevron-right" size={16} />
            </Button>
          </Link>
          <a href={REPO_URL} target="_blank" rel="noreferrer">
            <Button variant="secondary" size="lg">
              {t('site.home.action.readSource')} <AppIcon name="external" size={15} />
            </Button>
          </a>
          <QqGroupEntry variant="card" className="w-full lg:w-72" />
        </div>
      </div>
    </section>
  )
}

/* ── 页面装配 ───────────────────────────────────────────── */

export default function LandingPage() {
  return (
    <>
      <SiteHeader />
      <main>
        <Hero />
        <SpecBar />
        <Features />
        <Quickstart />
        <ModelPreview />
        <Design />
        <Faq />
        <Cta />
      </main>
      <SiteFooter />
    </>
  )
}
