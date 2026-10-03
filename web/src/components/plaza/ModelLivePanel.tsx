/**
 * 模型详情增强：实时 tokens/s 指标 + 连通性测试。
 *
 * 意图（Why）：
 *   模型详情页除了价格，用户最想确认两件事：
 *     1) 这个模型现在快不快（真实调用中的输出速率 tokens/s、平均耗时、TTFB）；
 *     2) 这个模型通不通（真实 agent 调用链路：发起 → 鉴权 → 流式返回）。
 *
 * 数据流：
 *   - 实时指标：GET /api/user/models/stats?model=...（5 秒轮询，近 15 分钟窗口）；
 *   - 连通性测试：前端直接调用 /v1/chat/completions（stream=true），
 *     与 Playground 完全同路径——请求发起、Bearer 鉴权、SSE 流式读取，
 *     读到首个 data 块即断开，返回 connected + ttfb_ms。
 *
 * 边界情况：
 *   - 模型离线（available=false）：指标卡显示"离线"，测试按钮禁用并提示；
 *   - 未填令牌：测试返回"需要访问令牌"，提示到令牌页创建；
 *   - 超时：测试请求 30 秒无首个 chunk 即判定不通。
 */
'use client'

import { useCallback, useEffect, useRef, useState } from 'react'

import { fetchModelStats } from '@/api/portal'
import type { ModelStats, ModelTestResult } from '@/api/types'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Display'
import { Field, Input } from '@/components/ui/Form'
import { getSessionToken } from '@/api/client'

/** 实时指标轮询间隔（毫秒） */
const STATS_POLL_MS = 5000
/** 连通性测试超时（毫秒）——只等首个 chunk，给足排队/慢渠道 */
const TEST_TIMEOUT_MS = 30_000

interface Props {
  modelName: string
}

export function ModelLivePanel({ modelName }: Props) {
  const [stats, setStats] = useState<ModelStats | null>(null)
  const [token, setToken] = useState('')
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<ModelTestResult | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  // 预填本机会话令牌（用户可改；与 Playground 同一来源，不落库）
  useEffect(() => {
    const session = getSessionToken()
    if (session) setToken(session)
  }, [])

  const loadStats = useCallback(async () => {
    try {
      const data = await fetchModelStats(modelName)
      setStats(data)
    } catch {
      setStats(null)
    }
  }, [modelName])

  // 挂载即拉一次，随后每 5 秒轮询（切换模型时自动重拉）
  useEffect(() => {
    void loadStats()
    const timer = setInterval(loadStats, STATS_POLL_MS)
    return () => clearInterval(timer)
  }, [loadStats])

  // 卸载时中断在途测试请求
  useEffect(() => () => abortRef.current?.abort(), [])

  async function handleTest() {
    if (!modelName.trim()) return
    if (!token.trim()) {
      setResult({ connected: false, ttfb_ms: 0, model: modelName, error: '需要访问令牌：请先在「访问令牌」页创建，或粘贴 sk- 开头的令牌' })
      return
    }
    if (!stats?.available) {
      setResult({ connected: false, ttfb_ms: 0, model: modelName, error: '该模型当前无可用渠道（离线），无法测试' })
      return
    }

    const controller = new AbortController()
    abortRef.current = controller
    setTesting(true)
    setResult(null)

    const startedAt = Date.now()
    try {
      // 直连 /v1 网关（不是 /api/v1——后者是站点 API 前缀，网关端点没有 /api 这一层）
      const response = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token.trim()}` },
        body: JSON.stringify({
          model: modelName,
          messages: [{ role: 'user', content: 'hi' }],
          stream: true,
          max_tokens: 8,
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
        setResult({ connected: false, ttfb_ms: 0, model: modelName, error: message })
        return
      }

      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let received = false

      // 只读首个 data 块即断开（不等待完整响应）
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
          // 首个真实数据块到达
          received = true
          controller.abort() // 断开：上游停止生成，已产生的少量输出按实际用量入账
          break
        }
        if (received) break
      }

      if (received) {
        setResult({ connected: true, ttfb_ms: Date.now() - startedAt, model: modelName })
      } else {
        setResult({ connected: false, ttfb_ms: 0, model: modelName, error: '流式连接成功但未收到任何数据块' })
      }
    } catch (err) {
      if ((err as Error).name === 'AbortError') {
        // 主动断开或超时：若已收到首块则视为连通（上面的分支会先处理）；
        // 到这里的 Abort 是用户切页/卸载，不更新结果。
        return
      }
      setResult({ connected: false, ttfb_ms: 0, model: modelName, error: '请求出错：网关不可达或鉴权未通过' })
    } finally {
      setTesting(false)
      abortRef.current = null
    }
  }

  const offline = stats !== null && !stats.available

  return (
    <div className="mt-4 space-y-3">
      {/* ── 实时指标卡：tokens/s + 平均耗时 + TTFB ── */}
      <div className="grid grid-cols-3 gap-2">
        <MetricTile
          label="输出速率"
          value={stats && stats.avg_tokens_per_second > 0 ? `${stats.avg_tokens_per_second.toFixed(1)} t/s` : '—'}
          hint={stats && stats.requests > 0 ? '近 15 分钟' : '暂无样本'}
        />
        <MetricTile
          label="平均耗时"
          value={stats && stats.avg_latency_ms > 0 ? `${stats.avg_latency_ms.toFixed(0)} ms` : '—'}
          hint="成功请求"
        />
        <MetricTile
          label="首字延迟 TTFB"
          value={stats && stats.avg_first_token_ms > 0 ? `${stats.avg_first_token_ms.toFixed(0)} ms` : '—'}
          hint="近 15 分钟"
        />
      </div>

      {/* ── 连通性测试 ── */}
      <div className="rounded-md border border-line bg-surface/60 p-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="text-[13px] font-medium text-ink-2">连通性测试</span>
            {offline ? (
              <Badge tone="err">离线</Badge>
            ) : stats ? (
              <Badge tone="ok">{stats.channel_count} 个渠道可用</Badge>
            ) : (
              <Badge tone="off">检测中…</Badge>
            )}
          </div>
          {result && (
            <span className={`text-[12px] ${result.connected ? 'text-ok' : 'text-err'}`}>
              {result.connected ? `已连通 · 首字 ${result.ttfb_ms} ms` : '未连通'}
            </span>
          )}
        </div>

        <div className="mt-2.5 flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="sk- 访问令牌（测试用，仅本次会话内存）"
            className="flex-1"
          />
          <Button variant="primary" onClick={handleTest} loading={testing} disabled={offline} className="shrink-0">
            {testing ? '测试中…' : '开始测试'}
          </Button>
        </div>

        {result && !result.connected && result.error && (
          <div className="mt-2 rounded border border-err/25 bg-err/8 px-2.5 py-1.5 text-[12px] text-err">{result.error}</div>
        )}
        <div className="mt-1.5 text-[11px] text-ink-3">
          测试会发起一次真实调用（含鉴权与流式返回），收到首个响应即断开，几乎不消耗 token。
        </div>
      </div>
    </div>
  )
}

/** 指标小方块 */
function MetricTile({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="rounded-md border border-line bg-card px-3 py-2.5">
      <div className="text-[11px] text-ink-3">{label}</div>
      <div className="mt-0.5 font-mono text-[15px] font-semibold text-ink tabular-nums">{value}</div>
      <div className="mt-0.5 text-[10px] text-ink-3">{hint}</div>
    </div>
  )
}
