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
  const sampleModel = status?.models?.[0] || 'LTZY-CALL/deepseek-v4-flash'

  const [origin, setOrigin] = useState('https://ltzy.top')
  useEffect(() => {
    if (typeof window !== 'undefined') setOrigin(window.location.origin)
  }, [])

  const terminal = `$ curl ${origin}/v1/chat/completions \\
    -H "Authorization: Bearer sk-••••••••" \\
    -d '{"model":"${sampleModel}",
         "messages":[{"role":"user","content":"你好"}]}'

# HTTP/1.1 200 OK
{
  "id": "chatcmpl-9f3a1c",
  "object": "chat.completion",
  "model": "${sampleModel}",
  "choices": [{
    "index": 0,
    "message": { "role": "assistant", "content": "你好！有什么可以帮你？" },
    "finish_reason": "stop"
  }],
  "usage": { "prompt_tokens": 9, "completion_tokens": 7, "total_tokens": 16 }
}`

  return (
    <section className="grid-bg border-b border-line">
      <div className="mx-auto grid max-w-6xl gap-10 px-4 py-14 sm:px-6 lg:grid-cols-[1.02fr_1fr] lg:items-center lg:py-20">
        <div>
          <div className="font-mono text-[12px] text-ink-3">
            <span className="text-brand">$</span> 自托管 LLM API 网关 · 单二进制部署
          </div>

          <h1 className="mt-4 text-3xl font-bold leading-[1.15] tracking-tight text-ink sm:text-[40px]">
            一条 OpenAI 兼容接口，
            <br />
            收拢数十家上游。
          </h1>

          <p className="mt-5 max-w-xl text-[15px] leading-[1.85] text-ink-2">
            OpenAI / Anthropic / Gemini 等协议在此统一转成 OpenAI 兼容格式。计费、日志、密钥池、
            失败重试开箱即用。整套网关开源，随时可以自己部署一套。
          </p>

          <div className="mt-7 flex flex-wrap gap-3">
            <Link href={isLoggedIn ? '/console' : '/register'}>
              <Button variant="primary" size="lg">
                {isLoggedIn ? '进入控制台' : '注册并获取令牌'}
                <AppIcon name="chevron-right" size={16} />
              </Button>
            </Link>
            <Link href="/models">
              <Button variant="secondary" size="lg">
                浏览模型与价格
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
  const stats = useMemo(() => {
    const items: { value: string; label: string }[] = []
    items.push({ value: status?.models?.length ? String(status.models.length) : '—', label: '模型在线' })
    items.push({ value: '79', label: '上游渠道类型' })
    items.push({ value: '3', label: '兼容协议' })
    items.push({ value: '1', label: '部署文件' })
    return items
  }, [status])

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

const FEATURES: { index: string; icon: IconName; title: string; desc: string }[] = [
  { index: '01', icon: 'layers', title: '协议统一', desc: 'OpenAI / Anthropic / Gemini 等入站出站协议互转，对外只暴露一种 OpenAI 兼容格式。' },
  { index: '02', icon: 'quota', title: '精细计费', desc: '按量 / 按次 / 免费三种模式，价格按分组配置，每一笔扣费都能逐条核对。' },
  { index: '03', icon: 'key', title: '密钥池与重试', desc: '同一渠道多把密钥轮转，单把失败自动换下一把，上游偶发抽风基本无感。' },
  { index: '04', icon: 'list', title: '全量日志', desc: '每次调用的模型、token 数、耗时与状态码全部留档，用量随时可查。' },
  { index: '05', icon: 'refresh', title: '失败切换', desc: '渠道级熔断与冷却退避，整条线路不可用时自动切到下一路重试。' },
  { index: '06', icon: 'server', title: '单二进制自托管', desc: '一套 Go 二进制 + SQLite，前端内嵌其中，部署只需要一个文件。' },
]

function Features() {
  return (
    <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
      <SectionHead
        anchorId="features"
        index="01"
        title="核心能力"
        desc="一个网关该有的都在里面：协议转换、计费、密钥管理、日志与容错，不需要再拼装第三方组件。"
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
            <h3 className="mt-3.5 text-[15px] font-semibold text-ink">{f.title}</h3>
            <p className="mt-1.5 text-[13px] leading-relaxed text-ink-2">{f.desc}</p>
          </div>
        ))}
      </div>
    </section>
  )
}

/* ── 02 接入 ────────────────────────────────────────────── */

const STEPS = [
  { title: '注册并创建令牌', desc: '在控制台生成一把访问令牌，顺手设好预算与可用模型。' },
  { title: '替换 base_url', desc: '任何支持 OpenAI SDK 的客户端，把 base_url 指向本站 /v1 即可。' },
  { title: '保持原有代码', desc: '协议是兼容的，请求与响应结构不变，无需改动业务代码。' },
]

function Quickstart() {
  const [origin, setOrigin] = useState('https://ltzy.top')
  useEffect(() => {
    if (typeof window !== 'undefined') setOrigin(window.location.origin)
  }, [])

  const curl = `curl ${origin}/v1/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer sk-你的令牌" \\
  -d '{
    "model": "deepseek-v3",
    "messages": [{ "role": "user", "content": "你好" }]
  }'`

  return (
    <section className="border-y border-line bg-card">
      <div className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
        <SectionHead anchorId="quickstart" index="02" title="接入，三步" desc="对外只暴露 OpenAI 兼容接口，现有 SDK 改一个 base_url 就能跑通。" />
        <div className="mt-8 grid gap-10 lg:grid-cols-[0.8fr_1.2fr]">
          <ol className="space-y-6">
            {STEPS.map((s, i) => (
              <li key={s.title} className="flex gap-4">
                <span className="font-mono text-[13px] font-medium text-brand">0{i + 1}</span>
                <div>
                  <div className="text-[14px] font-medium text-ink">{s.title}</div>
                  <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{s.desc}</p>
                </div>
              </li>
            ))}
          </ol>
          <div>
            <CodeBlock code={curl} language="bash" title="bash" />
            <p className="mt-3 font-mono text-[12px] text-ink-3">
              # Python / Node：把 OpenAI SDK 的 base_url 换成 {origin}/v1
            </p>
          </div>
        </div>
      </div>
    </section>
  )
}

/* ── 03 模型表格 ────────────────────────────────────────── */

/** 价格摘要：一律换算成人民币展示（内部仍以整数额度记账） */
function priceLabel(price: PlazaPrice | undefined, quotaPerYuan: number): string {
  if (!price) return '待定价'
  if (price.is_free || price.billing_mode === 'free') return '免费'
  if (price.billing_mode === 'per_call') {
    return price.per_call_price > 0 ? formatYuanPerCall(price.per_call_price, quotaPerYuan) : '按次'
  }
  const prompt = price.prompt_price
  return prompt > 0 ? formatYuanPerMillion(prompt, quotaPerYuan) : '按量'
}

function ModelPreview() {
  const { quotaPerYuan } = useSite()
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
        title="模型与价格"
        desc={viewer ? '当前为你的代理拿货档：划线为原价，橙色为你的折后价。' : '实时来自站点信息，价格按分组展示，不做修饰。'}
      />
      <div className="mt-8 overflow-hidden rounded-lg border border-line">
        <table className="w-full text-left text-[13px]">
          <thead className="bg-surface">
            <tr className="font-mono text-[11px] uppercase tracking-wider text-ink-3">
              <th className="px-4 py-2.5 font-normal">模型</th>
              <th className="hidden px-4 py-2.5 font-normal sm:table-cell">分组</th>
              <th className="px-4 py-2.5 text-right font-normal">价格</th>
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
                              {priceLabel(m.list_price, quotaPerYuan)}
                            </span>
                          )}
                          <span className="inline-flex items-center gap-1.5 rounded border border-warn/40 bg-warn/15 px-2 py-0.5 font-mono text-[12px] font-medium text-warn">
                            {priceLabel(m.prices?.[0], quotaPerYuan)}
                            <span className="opacity-70">· {formatDiscountLabel(viewer.ratio)}</span>
                          </span>
                        </span>
                      ) : (
                        priceLabel(m.prices?.[0], quotaPerYuan)
                      )}
                    </td>
                  </tr>
                ))}
          </tbody>
        </table>
      </div>
      <Link href="/models" className="mt-4 inline-flex items-center gap-1 text-[13px] font-medium text-brand hover:underline">
        {plaza?.total ? `查看全部 ${plaza.total} 个模型与完整价格` : '查看全部模型与完整价格'}
        <AppIcon name="chevron-right" size={14} />
      </Link>
    </section>
  )
}

/* ── 04 取舍与成本 ──────────────────────────────────────── */

const PRINCIPLES: [string, string][] = [
  ['计价写得明白', '每个模型的单价摆在模型广场上，按次 / 按量 / 免费三种模式，账单可逐条核对。'],
  ['用量自己说了算', '每次调用的模型、token、耗时、状态码都留档，你随时能查，我也改不了。'],
  ['代码是开源的', '整套网关以 MIT 许可证在 GitHub 开源；哪天我不做了，你也能自己部署一套。'],
]

const COSTS: [string, string][] = [
  ['服务器', '每月固定 · 一台独服跑网关与数据库'],
  ['带宽与流量', '按量浮动 · 调用越多越高'],
  ['上游模型费用', '按用量结算 · 我向上游买的价就是成本基准'],
  ['域名与证书', '每年少量'],
]

function Design() {
  return (
    <section className="border-y border-line bg-card">
      <div className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
        <SectionHead
          index="04"
          title="设计取舍与成本"
          desc="不做黑箱：定价规则、用量留档与成本边界都摆在明面上。"
        />
        <div className="mt-8 grid gap-10 lg:grid-cols-2">
          <div>
            <div className="font-mono text-[11px] uppercase tracking-wider text-ink-3">Principles</div>
            <ul className="mt-4 space-y-4">
              {PRINCIPLES.map(([title, desc]) => (
                <li key={title} className="border-l-2 border-brand/30 pl-3.5">
                  <div className="text-[14px] font-medium text-ink">{title}</div>
                  <p className="mt-1 text-[13px] leading-relaxed text-ink-2">{desc}</p>
                </li>
              ))}
            </ul>
          </div>
          <div>
            <div className="font-mono text-[11px] uppercase tracking-wider text-ink-3">Cost Breakdown</div>
            <div className="mt-4 divide-y divide-line rounded-lg border border-line">
              {COSTS.map(([k, v]) => (
                <div key={k} className="flex flex-col gap-0.5 px-4 py-2.5 sm:flex-row sm:items-center sm:justify-between">
                  <span className="text-[13px] font-medium text-ink-2">{k}</span>
                  <span className="font-mono text-[12px] text-ink-3">{v}</span>
                </div>
              ))}
            </div>
            <p className="mt-3 rounded-md border border-brand/25 bg-brand/5 p-3.5 text-[12.5px] leading-relaxed text-ink-2">
              你付的钱先覆盖成本，多出来的才是我继续维护它的理由。若哪天真入不敷出，我会在公告里说明，
              <b className="font-medium text-ink">而不是悄悄涨价</b>。
            </p>
          </div>
        </div>
      </div>
    </section>
  )
}

/* ── 05 FAQ ─────────────────────────────────────────────── */

const FAQS = [
  { q: '这站能一直开着吗？', a: '我会尽力。它的成本可控，我也不靠它赚钱，没有「融资烧完就跑」的问题。真有关停那天，我会提前公告并给出自己部署的完整方案。' },
  { q: '为什么要开源？', a: '一是让你能验证我说的都是真的；二是万一我不做了，这套东西不会跟着消失。协议是 MIT。' },
  { q: '免费分组的模型会收费吗？', a: '免费分组里就是不计费的，我不搞「先免费养熟再收费」那套。当然，免费范围会随上游价格调整，但改之前会在公告里说。' },
  { q: '我的调用数据会被拿去用吗？', a: '不会。日志只用于计费和排障，存在我自己的服务器上。站点的隐私政策里写明了这一点。' },
  { q: '上游不稳定怎么办？', a: '密钥池 + 自动重试 + 冷却退避：一把密钥失败自动换下一把，整条渠道不行就换渠道再试。你还是只发一次请求。' },
  { q: '怎么联系你？', a: '页面底部的「联系方式」和「投诉举报」都能找到我。有事直说就行。' },
]

function Faq() {
  const [open, setOpen] = useState<number | null>(0)
  return (
    <section className="mx-auto max-w-6xl px-4 py-14 sm:px-6 sm:py-16">
      <SectionHead anchorId="faq" index="05" title="常见问题" />
      <div className="mt-6 grid gap-x-10 gap-y-0 md:grid-cols-2">
        {FAQS.map((faq, index) => {
          const active = open === index
          return (
            <div key={faq.q} className="border-b border-line">
              <button
                type="button"
                onClick={() => setOpen(active ? null : index)}
                className="flex w-full items-center gap-3 py-3.5 text-left"
                aria-expanded={active}
              >
                <span className="font-mono text-[12px] text-ink-3">{String(index + 1).padStart(2, '0')}</span>
                <span className="flex-1 text-[14px] font-medium text-ink">{faq.q}</span>
                <AppIcon
                  name="chevron-down"
                  size={15}
                  className={`shrink-0 text-ink-3 transition-transform ${active ? 'rotate-180' : ''}`}
                />
              </button>
              {active && <p className="pb-4 pl-[30px] pr-6 text-[13px] leading-[1.85] text-ink-2">{faq.a}</p>}
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
  return (
    <section className="border-t border-line bg-card">
      <div className="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-14 sm:px-6 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight text-ink sm:text-3xl">
            {isLoggedIn ? '从一把新令牌开始。' : '从一把令牌开始。'}
          </h2>
          <p className="mt-2.5 max-w-xl text-[14px] leading-relaxed text-ink-2">
            {isLoggedIn
              ? '接进现有代码就行，先跑通一个请求，再决定要不要留下。'
              : '注册免费，先跑通一个请求，再决定要不要留下。'}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Link href={isLoggedIn ? '/console' : '/register'}>
            <Button variant="primary" size="lg">
              {isLoggedIn ? '进入控制台' : '注册并获取令牌'}
              <AppIcon name="chevron-right" size={16} />
            </Button>
          </Link>
          <a href={REPO_URL} target="_blank" rel="noreferrer">
            <Button variant="secondary" size="lg">
              阅读源码 <AppIcon name="external" size={15} />
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
