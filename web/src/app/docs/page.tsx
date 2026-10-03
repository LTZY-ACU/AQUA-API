/** 公开接口文档页（/docs）。
 *
 * 意图（Why）：
 *   中转站的使用者不是"照着文档抄参数"的那类人——他们要把现成的客户端
 *   （Cline / Cherry Studio / Cursor / 自己的脚本）指过来。因此这页的首要任务
 *   不是罗列字段，而是让人在 30 秒内明白三件事：
 *     1) base_url 填哪（三种协议同一个域名，只需改一处）；
 *     2) 鉴权头怎么写（两种都收）；
 *     3) 出错了看 error.code 而不是猜状态码。
 *
 * 数据来自服务端 `/openapi.json`（由 internal/openapi 用代码生成），
 * 不引任何 Swagger UI / Redoc：它们会给本站塞进一整套 JS 依赖，
 * 而这个页面要做的只是把 paths 渲染成可读的卡片。
 *
 * 流转（Flow）：
 *   本页 → fetchOpenAPISpec() → GET /openapi.json
 *        → 按 tag 分组渲染端点卡片 → 点"试一试"展开 curl 命令
 *
 * 扩展（Extend）：
 *   后端新增对外端点时，本页无需改动（内容全部来自规范）。
 */
'use client'

import { createContext, useContext, useEffect, useMemo, useState } from 'react'

import {
  fetchOpenAPISpec,
  type OpenAPIDocument,
  type OpenAPIOperation,
  type OpenAPIParameter,
  type OpenAPIResponse,
  type OpenAPISchema,
} from '@/api/openapi'
import { SiteFooter } from '@/components/site/SiteFooter'
import { SiteHeader } from '@/components/site/SiteHeader'
import { useToast } from '@/lib/toast/toast-context'
import { CopyButton } from '@/components/ui/Modal'

/** HTTP 方法的配色，与全站状态色保持一致。 */
const METHOD_STYLE: Record<string, string> = {
  GET: 'bg-ok/12 text-ok',
  POST: 'bg-brand/12 text-brand',
}

/** 响应状态码的配色：2xx 绿、4xx 橙、5xx 红、其他灰。 */
function statusStyle(code: string): string {
  if (code.startsWith('2')) return 'bg-ok/12 text-ok'
  if (code.startsWith('4')) return 'bg-warn/12 text-warn'
  if (code.startsWith('5')) return 'bg-err/12 text-err'
  return 'bg-ink/8 text-ink-3'
}

export default function DocsPage() {
  const { toastError } = useToast()
  const [doc, setDoc] = useState<OpenAPIDocument | null>(null)
  const [error, setError] = useState('')
  const [activeTag, setActiveTag] = useState('')

  useEffect(() => {
    const ctrl = new AbortController()
    fetchOpenAPISpec(ctrl.signal)
      .then(setDoc)
      .catch((err) => {
        // 主动取消不算失败：组件卸载时总会 abort
        if (ctrl.signal.aborted) return
        setError(err instanceof Error ? err.message : '文档加载失败')
      })
    return () => ctrl.abort()
  }, [])

  /**
   * 按 tag 分组端点。
   *
   * 为什么要按 tag 而不是把 paths 平铺：平铺的话 14 个端点会是一屏到底的长条，
   * 而使用者心里是分类的（"我要接对话" / "我要接图像"）。
   * 未声明 tag 的端点归到「其他」，绝不丢弃——丢一个端点等于让它在文档里消失。
   */
  const groups = useMemo(() => {
    if (!doc) return []
    const byTag = new Map<string, { tag: string; items: EndpointItem[] }>()
    for (const [path, item] of Object.entries(doc.paths)) {
      for (const method of ['get', 'post'] as const) {
        const op = item[method]
        if (!op) continue
        const tag = op.tags?.[0] ?? '其他'
        if (!byTag.has(tag)) byTag.set(tag, { tag, items: [] })
        byTag.get(tag)!.items.push({ path, method: method.toUpperCase(), op })
      }
    }
    const list = [...byTag.values()]
    // 排序：跟随规范里 tags 的声明顺序，保证"对话"总在最前，
    // 而不是取决于 map 的遍历顺序（那会随每次构建变化）
    const order = (doc.tags ?? []).map((t) => t.name)
    list.sort((a, b) => {
      const ia = order.indexOf(a.tag)
      const ib = order.indexOf(b.tag)
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
    })
    return list
  }, [doc])

  const visible = activeTag ? groups.filter((g) => g.tag === activeTag) : groups

  return (
    <>
      <SiteHeader />
      <main className="mx-auto max-w-5xl px-4 py-10 sm:px-6">
        <div className="font-mono text-[12px] text-ink-3">
          <span className="text-brand">/</span> 接口文档
        </div>
        <h1 className="mt-1.5 text-2xl font-bold tracking-tight text-ink sm:text-3xl">
          {doc?.info.title ?? '接口文档'}
        </h1>

        {error ? (
          <div className="mt-8 rounded-lg border border-err/30 bg-err/5 p-5">
            <p className="text-[13px] text-err">{error}</p>
            <p className="mt-2 text-[12px] text-ink-3">
              若持续失败，请确认后端版本不低于 v10.1；该端点随 OpenAPI 支持一同引入。
            </p>
          </div>
        ) : !doc ? (
          <div className="mt-8 space-y-3">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-24 animate-pulse rounded-lg border border-line bg-card" />
            ))}
          </div>
        ) : (
          <>
            <QuickStart />

            {/* 版本与规范下载 */}
            <div className="mt-4 flex flex-wrap items-center gap-3 text-[12px] text-ink-3">
              <span>
                规范版本 OpenAPI {doc.openapi} · 本站 {doc.info.version}
              </span>
              <a
                href="/openapi.json"
                target="_blank"
                rel="noreferrer"
                className="text-brand hover:underline"
              >
                下载 openapi.json
              </a>
              <span className="text-ink-3">
                可直接导入 Apifox / Postman，或喂给 openapi-generator 生成客户端
              </span>
            </div>

            {/* 标签筛选 */}
            <div className="mt-6 flex flex-wrap gap-2">
              <FilterChip active={!activeTag} onClick={() => setActiveTag('')}>
                全部
              </FilterChip>
              {groups.map((g) => (
                <FilterChip key={g.tag} active={activeTag === g.tag} onClick={() => setActiveTag(g.tag)}>
                  {g.tag}
                  <span className="ml-1.5 opacity-60">{g.items.length}</span>
                </FilterChip>
              ))}
            </div>

            {/* 端点列表 */}
            <RegistryContext.Provider value={doc.components.schemas}>
            <div className="mt-5 space-y-8">
              {visible.map((group) => (
                <section key={group.tag}>
                  <h2 className="text-[15px] font-semibold text-ink">{group.tag}</h2>
                  {doc.tags?.find((t) => t.name === group.tag)?.description && (
                    <p className="mt-0.5 text-[13px] text-ink-3">
                      {doc.tags.find((t) => t.name === group.tag)?.description}
                    </p>
                  )}
                  <div className="mt-3 space-y-3">
                    {group.items.map((item) => (
                      <EndpointCard key={`${item.method}-${item.path}`} {...item} />
                    ))}
                  </div>
                </section>
              ))}
            </div>

            </RegistryContext.Provider>

            <ErrorCodeTable />
          </>
        )}
      </main>
      <SiteFooter />
    </>
  )
}

/* ── 快速上手 ──────────────────────────────────────────────────────────── */

/**
 * 快速上手卡片。
 *
 * 为什么放在最前面而不是让使用者自己从 paths 里推：base_url 与鉴权头
 * 是接入的两个必经卡点，而它们在端点列表里是【重复出现】的——
 * 让人从重复里归纳出规则，比直接告诉他更慢也更易错。
 */
function QuickStart() {
  const [token, setToken] = useState('')
  const [origin, setOrigin] = useState('')

  // 页面挂载后才知道自己在哪个域上；SSR 阶段没有 window
  useEffect(() => setOrigin(window.location.origin), [])

  const curl = token
    ? `curl ${origin}/v1/chat/completions \\
  -H "Authorization: Bearer ${token}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好"}]
  }'`
    : `curl ${origin}/v1/chat/completions \\
  -H "Authorization: Bearer sk-你的令牌" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好"}]
  }'`

  return (
    <section className="mt-6 rounded-lg border border-line bg-card p-5">
      <h2 className="text-[15px] font-semibold text-ink">三步接入</h2>

      <ol className="mt-3 space-y-3">
        <Step n={1} title="在控制台创建令牌">
          「控制台 → 令牌管理」新建，形如 <code className="text-ink">sk-...</code>。
          可绑定分组（决定能调哪些模型与折扣）、限定模型白名单、设置过期时间与 RPM 上限。
        </Step>
        <Step n={2} title="把客户端的 Base URL 指向本站">
          OpenAI / Anthropic / Gemini 三种协议都在同一个域名下，
          <strong className="text-ink">只需改这一处</strong>：
          <ul className="mt-1.5 space-y-1 text-[12px] text-ink-2">
            <li>
              OpenAI 兼容：<code>{'{站点地址}'}/v1</code>
            </li>
            <li>
              Anthropic：<code>{'{站点地址}'}</code>（客户端会自动补 /v1/messages）
            </li>
            <li>
              Gemini：<code>{'{站点地址}'}/v1beta</code>
            </li>
          </ul>
        </Step>
        <Step n={3} title="填入令牌，开始调用">
          鉴权头两种写法都收：<code>Authorization: Bearer sk-xxx</code>（OpenAI SDK 默认）
          或 <code>api-key: sk-xxx</code>（Azure SDK 默认）；
          Gemini 还额外接受 <code>x-goog-api-key</code>。
        </Step>
      </ol>

      {/* 现填现试：把令牌填进去就能复制到终端直接跑，省掉"改代码才知道对不对" */}
      <div className="mt-5">
        <label htmlFor="docs-token" className="text-[13px] font-medium text-ink-2">
          试一下（令牌只留在本页面，不会上报）
        </label>
        <input
          id="docs-token"
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="sk-...（可留空，仅影响命令里的示例值）"
          autoComplete="off"
          className="mt-1.5 w-full rounded-md border border-line-2 bg-surface px-3 py-2 text-[13px] text-ink outline-none focus:border-brand"
        />
        <div className="mt-2.5 flex items-start gap-3">
          <pre className="min-w-0 flex-1 overflow-x-auto rounded-md border border-line bg-surface p-3 font-mono text-[12px] leading-relaxed text-ink-2">
            {curl}
          </pre>
          <CopyButton text={curl} />
        </div>
      </div>
    </section>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-brand/12 font-mono text-[11px] font-bold text-brand">
        {n}
      </span>
      <div className="min-w-0 text-[13px] leading-relaxed text-ink-2">
        <div className="font-medium text-ink">{title}</div>
        <div className="mt-0.5">{children}</div>
      </div>
    </li>
  )
}

/* ── 端点卡片 ──────────────────────────────────────────────────────────── */

interface EndpointItem {
  path: string
  method: string
  op: OpenAPIOperation
}

function FilterChip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`rounded-full border px-3 py-1 text-[12px] transition ${
        active
          ? 'border-brand bg-brand/10 font-medium text-brand'
          : 'border-line text-ink-3 hover:border-line-2 hover:text-ink-2'
      }`}
    >
      {children}
    </button>
  )
}

function EndpointCard({ path, method, op }: EndpointItem) {
  const [open, setOpen] = useState(false)
  const public_ = op.security?.length === 0
  const sample = op.requestBody
    ? Object.entries(op.requestBody.content)[0]
    : undefined

  return (
    <article className="overflow-hidden rounded-lg border border-line bg-card">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-3 px-4 py-3 text-left transition hover:bg-surface"
      >
        <span
          className={`shrink-0 rounded px-1.5 py-0.5 font-mono text-[11px] font-bold ${METHOD_STYLE[method] ?? 'bg-ink/8 text-ink-3'}`}
        >
          {method}
        </span>
        <code className="min-w-0 flex-1 truncate font-mono text-[13px] text-ink">{path}</code>
        {public_ && (
          <span className="shrink-0 rounded bg-ok/12 px-1.5 py-0.5 text-[11px] text-ok">
            免鉴权
          </span>
        )}
        <span className="shrink-0 text-[13px] text-ink-3">{op.summary}</span>
      </button>

      {open && (
        <div className="space-y-5 border-t border-line px-4 py-4">
          {op.description && (
            <Markdown text={op.description} />
          )}

          {op.parameters && op.parameters.length > 0 && (
            <Section title="路径 / 查询参数">
              <table className="w-full text-[12px]">
                <thead>
                  <tr className="text-left text-ink-3">
                    <th className="w-40 pb-1.5 font-medium">名称</th>
                    <th className="w-24 pb-1.5 font-medium">位置</th>
                    <th className="pb-1.5 font-medium">说明</th>
                  </tr>
                </thead>
                <tbody>
                  {op.parameters.map((p) => (
                    <tr key={p.name} className="border-t border-line align-top">
                      <td className="py-1.5">
                        <code className="text-ink">{p.name}</code>
                        {p.required && <span className="ml-1 text-err">*</span>}
                      </td>
                      <td className="py-1.5 text-ink-3">{p.in}</td>
                      <td className="py-1.5 text-ink-2">
                        {p.description ?? '—'}
                        {p.example && (
                          <div className="mt-0.5 font-mono text-ink-3">例：{p.example}</div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Section>
          )}

          {op.requestBody && sample && (
            <Section title={`请求体（${sample[0]}）${op.requestBody.required ? ' · 必填' : ''}`}>
              <SchemaTable schema={sample[1].schema} depth={0} />
            </Section>
          )}

          <Section title="响应">
            <div className="space-y-2.5">
              {Object.entries(op.responses)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([code, resp]) => (
                  <ResponseRow key={code} code={code} resp={resp} />
                ))}
            </div>
            {/* 把 X-Request-Id 单独拎出来：它不在 responses 的 content 里，
                却是报障时最有价值的一条信息，混在 headers 里容易被跳过 */}
            {Object.values(op.responses).some((r) => r.headers?.['X-Request-Id']) && (
              <p className="rounded border border-brand/20 bg-brand/5 p-2.5 text-[12px] leading-relaxed text-ink-2">
                每个响应都带 <code className="text-brand">X-Request-Id</code> 追踪 ID。
                <strong className="text-ink">报障时请一并提供</strong>
                —— 我们用它在校调用日志里精确定位到这一次调用（含命中的渠道与计费明细），
                没有它只能靠时间戳在几十行日志里猜。
              </p>
            )}
          </Section>
        </div>
      )}
    </article>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h4 className="mb-1.5 text-[12px] font-semibold uppercase tracking-wide text-ink-3">
        {title}
      </h4>
      {children}
    </div>
  )
}

function ResponseRow({ code, resp }: { code: string; resp: OpenAPIResponse }) {
  const [show, setShow] = useState(false)
  const hasSchema = Object.keys(resp.content ?? {}).length > 0
  return (
    <div className="rounded border border-line">
      <button
        type="button"
        onClick={() => setShow((v) => !v)}
        disabled={!hasSchema}
        className="flex w-full items-center gap-2 px-2.5 py-2 text-left text-[12px] disabled:cursor-default"
      >
        <span className={`shrink-0 rounded px-1.5 py-0.5 font-mono font-bold ${statusStyle(code)}`}>
          {code}
        </span>
        <span className="min-w-0 flex-1 text-ink-2">{resp.description}</span>
        {hasSchema && <span className="shrink-0 text-ink-3">{show ? '收起' : '展开'}</span>}
      </button>
      {show && hasSchema && (
        <div className="border-t border-line px-2.5 py-2">
          {Object.entries(resp.content ?? {}).map(([ct, media]) => (
            <div key={ct}>
              <div className="mb-1 font-mono text-[11px] text-ink-3">{ct}</div>
              <SchemaTable schema={media.schema} depth={0} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

/* ── Schema 渲染 ───────────────────────────────────────────────────────── */

/**
 * 共享 schema 表的上下文。
 *
 * 为什么用 context 而不是模块级变量：模块级可变全局在静态导出（SSG）
 * 下会跨请求/跨页面实例串味——A 站点的 schema 泄漏到 B 站点的文档页。
 * 本站是单站点部署，但这个坑一旦踩上极难定位，不值得省那几行 prop。
 */
const RegistryContext = createContext<Record<string, OpenAPISchema>>({})

function useRegistry() {
  return useContext(RegistryContext)
}

/** 嵌套深度上限：再深下去（对话的 content 块之类）读起来比看原始 JSON 更累。 */
const MAX_DEPTH = 3

function SchemaTable({ schema, depth }: { schema?: OpenAPISchema; depth: number }) {
  const registry = useRegistry()
  if (!schema) return <p className="text-[12px] text-ink-3">（无结构定义）</p>

  // $ref 就地展开一层：读者要的是字段表，不是"点开一个链接"
  if (schema.$ref) {
    return (
      <RefExpander schema={schema} depth={depth} />
    )
  }

  if (schema.properties && Object.keys(schema.properties).length > 0) {
    const required = new Set(schema.required ?? [])
    return (
      <div className="overflow-hidden rounded border border-line">
        <table className="w-full text-[12px]">
          <tbody>
            {Object.entries(schema.properties).map(([name, sub]) => (
              <tr key={name} className="border-b border-line last:border-b-0 align-top">
                <td className="w-44 px-2.5 py-1.5">
                  <code className="text-ink">{name}</code>
                  {required.has(name) && <span className="ml-1 text-err">*</span>}
                  <div className="mt-0.5 font-mono text-[11px] text-ink-3">
                    {typeLabel(sub)}
                  </div>
                </td>
                <td className="px-2.5 py-1.5 text-ink-2">
                  {sub.description ? <Markdown text={sub.description} inline /> : '—'}
                  {sub.enum && (
                    <div className="mt-1 flex flex-wrap gap-1">
                      {sub.enum.map((v) => (
                        <code key={v} className="rounded bg-surface px-1 py-0.5 text-[11px] text-ink-3">
                          {v}
                        </code>
                      ))}
                    </div>
                  )}
                  {depth < MAX_DEPTH && (sub.properties || sub.items) && (
                    <details className="mt-1">
                      <summary className="cursor-pointer text-[11px] text-brand">
                        展开子结构
                      </summary>
                      <div className="mt-1.5">
                        <SchemaTable
                          schema={sub.properties ? sub : sub.items}
                          depth={depth + 1}
                         
                        />
                      </div>
                    </details>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    )
  }

  if (schema.items) {
    return (
      <div className="pl-1">
        <div className="mb-1 font-mono text-[11px] text-ink-3">数组元素：</div>
        <SchemaTable schema={schema.items} depth={depth} />
      </div>
    )
  }

  return (
    <p className="text-[12px] text-ink-3">
      {typeLabel(schema)}
      {schema.description ? ` · ${schema.description}` : ''}
    </p>
  )
}

/** 就地展开 $ref（最多一层，避免无限递归）。 */
function RefExpander({ schema, depth }: { schema: OpenAPISchema; depth: number }) {
  return (
    <p className="text-[12px] text-ink-3">
      结构见下方共享定义：
      <code className="ml-1 text-brand">{schema.$ref?.split('/').pop()}</code>
      （{typeLabel(schema)}）
      {depth < MAX_DEPTH && <RefContents target={schema.$ref ?? ''} depth={depth} />}
    </p>
  )
}

function RefContents({ target, depth }: { target: string; depth: number }) {
  return (
    <details className="mt-1">
      <summary className="cursor-pointer text-[11px] text-brand">展开</summary>
      <div className="mt-1.5">
        {/* 由页面顶层把 components 传下来的占位见 DocsRenderer 的 SchemaContext */}
        <SchemaFromRegistry name={target.split('/').pop() ?? ''} depth={depth + 1} />
      </div>
    </details>
  )
}

function SchemaFromRegistry({ name, depth }: { name: string; depth: number }) {
  const found = useRegistry()[name]
  if (!found) {
    return <p className="text-[12px] text-ink-3">（共享定义 {name} 未找到）</p>
  }
  return <SchemaTable schema={found} depth={depth} />
}

/** typeLabel 把 schema 的 type 渲染成一句人话。 */
function typeLabel(schema: OpenAPISchema): string {
  if (schema.enum && !schema.type) return '枚举'
  const t = schema.type
  if (Array.isArray(t)) return t.join(' 或 ')
  if (t === 'array') return '数组'
  if (t) return t
  if (schema.$ref) return '对象'
  if (schema.additionalProperties) return '任意字段'
  return '任意'
}

/* ── Markdown ──────────────────────────────────────────────────────────── */

/**
 * 极简 Markdown 渲染。
 *
 * Why 不用 react-markdown：规范里的正文只用到加粗、行内代码、换行与列表，
 * 引一个完整的 Markdown 解析器（及其安全清洗依赖）来支持这四种语法不划算，
 * 而且它会把 dangerouslySetInnerHTML 引入到一个公开页面里——风险与收益不成比例。
 *
 * 支持的语法：`**粗体**`、`` `等宽` ``、`- ` 列表、空行分段。
 */
function Markdown({ text, inline = false }: { text: string; inline?: boolean }) {
  const blocks = text.split('\n\n')

  if (inline) {
    return <span>{renderInline(blocks.join('\n\n'))}</span>
  }

  return (
    <div className="space-y-2.5">
      {blocks.map((block, i) => {
        const lines = block.split('\n')
        if (lines.every((l) => l.trimStart().startsWith('- ') || l.trimStart().startsWith('> '))) {
          return (
            <ul key={i} className="space-y-1">
              {lines.map((l, j) => (
                <li key={j} className="flex gap-2 text-[13px] leading-relaxed text-ink-2">
                  <span className="mt-1.5 h-1 w-1 shrink-0 rounded-full bg-ink-3" />
                  <span>{renderInline(l.replace(/^\s*[->]\s*/, ''))}</span>
                </li>
              ))}
            </ul>
          )
        }
        return (
          <p key={i} className="text-[13px] leading-relaxed text-ink-2">
            {block.split('\n').map((l, j) => (
              <span key={j}>
                {renderInline(l)}
                {j < block.split('\n').length - 1 && <br />}
              </span>
            ))}
          </p>
        )
      })}
    </div>
  )
}

/** 行内语法：**粗体** 与 `等宽`。 */
function renderInline(text: string): React.ReactNode[] {
  const out: React.ReactNode[] = []
  // 一次扫描同时处理两种记号，避免先替换 ** 再替换 ` 时把前者的产物切坏
  const re = /(\*\*[^*]+\*\*|`[^`]+`)/g
  let last = 0
  let key = 0
  for (const m of text.matchAll(re)) {
    const idx = m.index ?? 0
    if (idx > last) out.push(text.slice(last, idx))
    const tok = m[0]
    if (tok.startsWith('**')) {
      out.push(
        <strong key={key++} className="font-semibold text-ink">
          {tok.slice(2, -2)}
        </strong>,
      )
    } else {
      out.push(
        <code key={key++} className="rounded bg-surface px-1 py-0.5 font-mono text-[12px] text-brand">
          {tok.slice(1, -1)}
        </code>,
      )
    }
    last = idx + tok.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

/* ── 错误码表 ──────────────────────────────────────────────────────────── */

/**
 * 错误码速查。
 *
 * 为什么单列一张表而不是散在各端点的响应里：使用者排查时的动作是
 * "拿到一个 error.code，反查它意味着什么、下一步做什么"，
 * 这是一个跨端点的查询动作，需要一个跨端点的视图。
 */
function ErrorCodeTable() {
  const rows: { code: string; meaning: string; action: string }[] = [
    { code: 'invalid_api_key', meaning: '令牌不存在或已删除', action: '检查 base_url 与令牌是否抄错' },
    { code: 'token_disabled', meaning: '令牌被手动停用', action: '去控制台确认令牌状态' },
    { code: 'token_expired', meaning: '令牌已过期', action: '新建一个令牌' },
    { code: 'insufficient_quota', meaning: '额度不足', action: '充值，或检查令牌的额度限制' },
    { code: 'model_not_allowed', meaning: '模型不在令牌白名单内', action: '改令牌白名单，或换一个该令牌可用的模型' },
    { code: 'no_available_channel', meaning: '没有可用渠道', action: '稍后重试；持续出现说明该模型无可用上游' },
    { code: 'upstream_unavailable', meaning: '上游服务故障或过载（HTTP 529）', action: '退避重试。**重试通常有效**' },
    { code: 'upstream_rate_limited', meaning: '上游限流或额度受限（HTTP 429）', action: '退避重试并降低并发' },
    { code: 'upstream_request_failed', meaning: '上游拒绝了请求（HTTP 502）', action: '重试无用。检查参数与模型名是否被上游接受' },
    { code: 'request_too_large', meaning: '请求体超过 32 MiB', action: '压缩图片或减少单次输入长度' },
    { code: 'sensitive_word_blocked', meaning: '内容命中站点敏感词策略', action: '改写命中的措辞' },
    { code: 'invalid_json', meaning: '请求体不是合法 JSON', action: '检查引号与逗号；音频接口须用 multipart/form-data' },
    { code: 'missing_model', meaning: '缺少 model 字段', action: '填入 model，或先调 /v1/models 看可用模型' },
  ]

  return (
    <section className="mt-10">
      <h2 className="text-[15px] font-semibold text-ink">错误码速查</h2>
      <p className="mt-0.5 text-[13px] text-ink-3">
        报错时先看 <code className="text-brand">error.code</code> 而不是 HTTP 状态码——
        同样是 502，退避重试与放弃重试的处置完全相反。
      </p>
      <div className="mt-3 overflow-x-auto rounded-lg border border-line bg-card">
        <table className="w-full min-w-[560px] text-[12px]">
          <thead>
            <tr className="border-b border-line text-left text-ink-3">
              <th className="px-3 py-2 font-medium">error.code</th>
              <th className="px-3 py-2 font-medium">含义</th>
              <th className="px-3 py-2 font-medium">下一步</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.code} className="border-b border-line last:border-b-0 align-top">
                <td className="px-3 py-2">
                  <code className="text-ink">{r.code}</code>
                </td>
                <td className="px-3 py-2 text-ink-2">{r.meaning}</td>
                <td className="px-3 py-2 text-ink-2">
                  <Markdown text={r.action} inline />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}