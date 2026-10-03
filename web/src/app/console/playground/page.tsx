/** 用户门户：游乐场（/console/playground）—— 在线使用界面。
 *
 * 意图（Why）：
 *   用访问令牌直接调用 /v1/chat/completions，让用户在浏览器确认「令牌通不通、上游答不答得上」。
 *   令牌只粘贴一次、只存内存（不落 localStorage），避免明文泄漏风险。
 *
 * 增强（本轮）：
 *   1) 模型列表实时更新：除加载公开广场外，每 30 秒轮询 /v1/models（OpenAI 兼容端点），
 *      自动反映「当前可用模型」——渠道上线/下线、令牌权限变化都会同步；
 *   2) 调用状态实时显示：请求中（「正在生成…」+ 停止按钮）→ 流式输出 → 完成后展示
 *      本轮耗时与 TTFB（首字延迟），让用户直观感知"这次调用发生了什么"。
 */
'use client'

import { useCallback, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/Button'
import { Card } from '@/components/ui/Display'
import { Field, Input, Select } from '@/components/ui/Form'
import { fetchModelPlaza } from '@/api/site'
import { useToast } from '@/lib/toast/toast-context'
import Link from 'next/link'

interface Message {
  role: 'user' | 'assistant'
  content: string
}

/** 模型列表轮询间隔（毫秒）：实时反映可用模型变化 */
const MODELS_POLL_MS = 30_000

/** 单轮调用的时序状态（用于"调用状态"展示） */
interface CallStat {
  ttfb_ms: number | null // 首字延迟（收到首个数据块的时间）
  total_ms: number // 本轮总耗时（发起 → 流结束/断开）
}

export default function ConsolePlaygroundPage() {
  const [models, setModels] = useState<string[]>([])
  const [model, setModel] = useState('')
  const [token, setToken] = useState('')
  const [prompt, setPrompt] = useState('')
  const [messages, setMessages] = useState<Message[]>([])
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const [stat, setStat] = useState<CallStat | null>(null)
  const [modelsUpdatedAt, setModelsUpdatedAt] = useState<number | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const { toastError } = useToast()

  /** 加载模型列表：公开广场（含可用性）为基底，/v1/models 提供实时可用性补充。
   *
   * 令牌说明（重要）：/v1 网关认的是「访问令牌」（sk- 开头，存于 tokens 表），
   * 登录会话令牌走的是另一套鉴权，填进去只会 401。因此这里【不预填】会话令牌，
   * 且只在用户填入的令牌形如 sk- 时才去探 /v1/models（否则静默退化为广场清单）。
   */
  const loadModels = useCallback(async (accessToken: string) => {
    try {
      // 1) 公开广场：完整模型清单（含分组/可用性）
      const plaza = await fetchModelPlaza()
      const plazaNames = plaza.items.map((item) => item.model)
      // 2) /v1/models：带访问令牌鉴权的"实时可用"模型（无令牌/非 sk- 令牌时跳过）
      let liveNames: string[] = []
      const tok = accessToken.trim()
      if (tok.startsWith('sk-')) {
        try {
          const live = await fetch('/v1/models', {
            headers: { Authorization: `Bearer ${tok}` },
          })
          if (live.ok) {
            const data = (await live.json()) as { data?: { id: string }[] }
            liveNames = (data.data ?? []).map((m) => m.id)
          }
        } catch {
          /* 网络错误：忽略，保留广场清单 */
        }
      }
      // 3) 并集：广场模型在前（保证首次就有选择），/v1 模型在后（实时补充）
      const merged = [...new Set([...plazaNames, ...liveNames])]
      setModels(merged)
      setModelsUpdatedAt(Date.now())
      setModel((prev) => prev || merged[0] || '')
    } catch {
      setModels([])
    }
  }, [])

  // 首次加载 + 每 30 秒轮询（令牌变化时重建轮询，保证 /v1/models 用最新令牌）
  useEffect(() => {
    void loadModels(token)
    const timer = setInterval(() => void loadModels(token), MODELS_POLL_MS)
    return () => clearInterval(timer)
  }, [loadModels, token])

  async function handleSend() {
    if (!token.trim() || !model.trim() || !prompt.trim()) {
      toastError('请填写令牌、模型与问题')
      return
    }
    if (streaming) {
      abortRef.current?.abort()
      setStreaming(false)
      return
    }

    const userMsg: Message = { role: 'user', content: prompt }
    setMessages((prev) => [...prev, userMsg])
    setPrompt('')
    setError('')
    setStat(null)

    // 流式读取：SSE 逐块渲染（等价旧版 playLLM 逻辑）
    const controller = new AbortController()
    abortRef.current = controller
    setStreaming(true)
    setMessages((prev) => [...prev, { role: 'assistant', content: '' }])

    const startedAt = Date.now()
    let ttfb: number | null = null

    try {
      // 直连 /v1 网关（相对路径）：生产环境前端与 API 同源由 go:embed 伺服，
      // 开发环境由 next.config.ts 的 rewrites 代理到本机后端。
      // 不能走 /api 前缀——网关路由只注册在 /v1 下，多一层前缀会 404。
      const response = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token.trim()}` },
        body: JSON.stringify({
          model: model.trim(),
          messages: [...messages, userMsg].map((m) => ({ role: m.role, content: m.content })),
          stream: true,
        }),
        signal: controller.signal,
      })

      if (!response.ok || !response.body) {
        let message = `请求失败（HTTP ${response.status}）`
        try {
          const data = await response.json()
          message = data?.error?.message || message
        } catch {
          /* 保留默认提示 */
        }
        setError(message)
        setMessages((prev) => prev.slice(0, -1))
        return
      }

      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() ?? ''

        for (const line of lines) {
          const trimmed = line.trim()
          if (!trimmed.startsWith('data:')) continue
          const payload = trimmed.slice(5).trim()
          if (payload === '[DONE]') continue
          // 记录首字延迟（首个真实数据块到达）
          if (ttfb === null) ttfb = Date.now() - startedAt
          try {
            const json = JSON.parse(payload) as { choices?: { delta?: { content?: string } }[] }
            const delta = json.choices?.[0]?.delta?.content
            if (delta) {
              setMessages((prev) => {
                const next = [...prev]
                const last = next[next.length - 1]
                if (last?.role === 'assistant') next[next.length - 1] = { role: 'assistant', content: last.content + delta }
                return next
              })
            }
          } catch {
            /* 忽略无法解析的块 */
          }
        }
      }
    } catch (err) {
      if ((err as Error).name === 'AbortError') {
        // 用户主动停止：保留已生成内容
      } else {
        setError('请求出错了，请检查令牌是否有效')
      }
    } finally {
      setStat({ ttfb_ms: ttfb, total_ms: Date.now() - startedAt })
      setStreaming(false)
      abortRef.current = null
    }
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">游乐场</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">在线使用界面：调用全部可用模型，实时观察调用状态</p>
      </div>

      <div className="grid gap-5 lg:grid-cols-[1fr_300px]">
        <Card className="flex flex-col">
          <div className="min-h-72 space-y-3">
            {messages.length === 0 ? (
              <div className="flex h-72 items-center justify-center text-[13px] text-ink-3">
                在上方或下方输入问题开始对话
              </div>
            ) : (
              messages.map((msg, index) => (
                <div key={index} className={`flex ${msg.role === 'user' ? 'justify-end' : 'justify-start'}`}>
                  <div
                    className={`max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm ${
                      msg.role === 'user' ? 'bg-brand text-on-brand' : 'border border-line bg-surface text-ink-2'
                    }`}
                  >
                    {msg.content || (streaming && index === messages.length - 1 ? '正在生成…' : '')}
                  </div>
                </div>
              ))
            )}
            {error && <div className="rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err">{error}</div>}
          </div>

          {/* 调用状态条：本轮耗时 / 首字延迟 / 模型列表更新时间 */}
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-line pt-3 text-[12px] text-ink-3">
            {stat && (
              <span>
                本轮耗时 <span className="font-mono text-ink-2">{stat.total_ms} ms</span>
                {stat.ttfb_ms !== null && (
                  <>
                    {' · '}首字延迟 <span className="font-mono text-ink-2">{stat.ttfb_ms} ms</span>
                  </>
                )}
              </span>
            )}
            {modelsUpdatedAt && (
              <span className="ml-auto">
                模型列表更新于 <span className="font-mono">{new Date(modelsUpdatedAt).toLocaleTimeString()}</span>
                {' · '}共 <span className="font-mono">{models.length}</span> 个
              </span>
            )}
          </div>

          <div className="mt-4 flex items-end gap-2">
            <textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  void handleSend()
                }
              }}
              placeholder="输入问题，Enter 发送，Shift+Enter 换行"
              rows={2}
              className="flex-1 rounded-md border border-line-2 bg-card px-3 py-2 text-sm outline-none focus:border-brand"
            />
            <Button variant="primary" onClick={handleSend} className="shrink-0">
              {streaming ? '停止' : '发送'}
            </Button>
          </div>
        </Card>

        <Card>
          <div className="space-y-3">
            <Field label="访问令牌" help="sk- 开头的 API 访问令牌，不是登录密码">
              <Input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="填入 sk- 开头的 API 访问令牌（在令牌页创建）"
              />
            </Field>
            <Field label="模型">
              <Select value={model} onChange={(e) => setModel(e.target.value)}>
                {models.length === 0 && <option value="">加载中…</option>}
                {models.map((name) => (
                  <option key={name} value={name}>{name}</option>
                ))}
              </Select>
            </Field>
            <div className="text-xs leading-relaxed text-ink-3">
              令牌只保存在本页内存，刷新即消失。调用走 <code className="rounded bg-ink/5 px-1">/v1</code> 网关；
              模型列表每 30 秒自动刷新，反映当前可用模型。
              <br />
              还没有令牌？
              <Link href="/console/tokens" className="text-brand hover:underline">
                去令牌页创建
              </Link>
            </div>
          </div>
        </Card>
      </div>
    </div>
  )
}
