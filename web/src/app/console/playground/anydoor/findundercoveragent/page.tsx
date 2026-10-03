/** 用户门户：任意门 → 多人推理（/console/playground/anydoor/findundercoveragent）
 *
 * 一个由本站 AI 主持的「传设备」派对推理游戏：玩家围坐一台设备轮流操作，
 * AI 在开局生成剧情与秘密身份，过程中可让 AI 推进剧情，最终投票揪出卧底特工。
 *
 * 约束（来自需求）：
 *   - 顶部必须显示站点图标 + 站点名 + 游戏名 + 返回任意门按钮；
 *   - 开始前让用户自选模型；游戏内所有 AI 调用走本站 /v1/chat/completions（OpenAI 兼容网关），
 *     鉴权用玩家自己的 sk- 访问令牌（不是登录会话令牌）；
 *   - 不登录由 /console 布局守卫自动跳转到 /login；本页只管游戏本身；
 *   - 配色自定，但保持与站点设计语言一致（复用 brand / ink / line 等 token）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import Link from 'next/link'

import { Badge, Card } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select } from '@/components/ui/Form'
import { BrandLogo } from '@/components/BrandMark'
import { fetchModelPlaza } from '@/api/site'
import { useSite } from '@/lib/site/site-context'
import { useToast } from '@/lib/toast/toast-context'

type Phase = 'setup' | 'briefing' | 'reveal' | 'discuss' | 'vote' | 'result'

interface PlayerRole {
  name: string
  role: 'undercover' | 'citizen'
  secret: string
}

interface GameSetup {
  scenario: string
  roles: PlayerRole[]
}

const GAME_TITLE = '多人推理 · 找出卧底特工'
const DISCUSS_SECONDS = 120

export default function FindUndercoverAgentPage() {
  const { siteName } = useSite()
  const { toastError } = useToast()

  // 模型与令牌（开始前自选）
  const [models, setModels] = useState<string[]>([])
  const [model, setModel] = useState('')
  const [token, setToken] = useState('')

  // 开局设置
  const [playerCount, setPlayerCount] = useState(5)
  const [nameInput, setNameInput] = useState('')

  // 运行时
  const [phase, setPhase] = useState<Phase>('setup')
  const [setup, setSetup] = useState<GameSetup | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  // 身份分发（传设备）
  const [revealIdx, setRevealIdx] = useState(0)
  const [revealShown, setRevealShown] = useState(false)

  // 讨论计时
  const [secsLeft, setSecsLeft] = useState(DISCUSS_SECONDS)
  const [timerOn, setTimerOn] = useState(false)
  const [twist, setTwist] = useState('')
  const [twistStreaming, setTwistStreaming] = useState(false)

  // 投票（传设备）
  const [votes, setVotes] = useState<Record<number, number>>({})
  const [voteIdx, setVoteIdx] = useState(0)
  const [voteTarget, setVoteTarget] = useState<number | null>(null)

  // 结算
  const [resultText, setResultText] = useState('')
  const [resultStreaming, setResultStreaming] = useState(false)

  // 模型列表：打开即加载（取自公开广场，与游乐场一致）
  useEffect(() => {
    let alive = true
    fetchModelPlaza()
      .then((p) => {
        if (!alive) return
        const names = p.items.map((i) => i.model)
        setModels(names)
        setModel((prev) => prev || names[0] || '')
      })
      .catch(() => {
        /* 加载失败静默：用户仍可看到空列表提示 */
      })
    return () => {
      alive = false
    }
  }, [])

  // 讨论倒计时
  useEffect(() => {
    if (!timerOn) return
    if (secsLeft <= 0) {
      setTimerOn(false)
      return
    }
    const t = setTimeout(() => setSecsLeft((s) => s - 1), 1000)
    return () => clearTimeout(t)
  }, [timerOn, secsLeft])

  // 调用本站 AI（/v1/chat/completions，自选模型 + sk- 令牌）
  const callAI = useCallback(
    async (system: string, user: string, onDelta?: (text: string) => void): Promise<string> => {
      const res = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token.trim()}` },
        body: JSON.stringify({
          model: model.trim(),
          messages: [
            { role: 'system', content: system },
            { role: 'user', content: user },
          ],
          stream: Boolean(onDelta),
        }),
      })
      if (!res.ok || !res.body) {
        let message = `AI 请求失败（HTTP ${res.status}）`
        try {
          const data = (await res.json()) as { error?: { message?: string } }
          message = data?.error?.message || message
        } catch {
          /* 保留默认提示 */
        }
        throw new Error(message)
      }
      if (onDelta) {
        const reader = res.body.getReader()
        const decoder = new TextDecoder()
        let buf = ''
        let full = ''
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          buf += decoder.decode(value, { stream: true })
          const lines = buf.split('\n')
          buf = lines.pop() ?? ''
          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed.startsWith('data:')) continue
            const payload = trimmed.slice(5).trim()
            if (payload === '[DONE]') continue
            try {
              const json = JSON.parse(payload) as { choices?: { delta?: { content?: string } }[] }
              const delta = json.choices?.[0]?.delta?.content
              if (delta) {
                full += delta
                onDelta(full)
              }
            } catch {
              /* 忽略不可解析块 */
            }
          }
        }
        return full
      }
      const data = (await res.json()) as { choices?: { message?: { content?: string } }[] }
      return data.choices?.[0]?.message?.content ?? ''
    },
    [token, model],
  )

  // 解析 AI 返回的开局设定（容错：缺卧底则随机指定，多卧底则只留一个）
  const parseSetup = useCallback((raw: string, names: string[]): GameSetup => {
    let text = raw.trim()
    const fence = text.match(/```(?:json)?\s*([\s\S]*?)```/i)
    if (fence) text = fence[1].trim()
    const obj = JSON.parse(text) as {
      scenario?: string
      roles?: { role?: string; secret?: string }[]
    }
    const scenario = String(obj.scenario ?? '一场神秘的事件正在发生，众人之中藏着不为人知的秘密。')
    const src = Array.isArray(obj.roles) ? obj.roles : []
    let roles: PlayerRole[] = names.map((name, i) => {
      const r = src[i] ?? {}
      const role: PlayerRole['role'] = r.role === 'undercover' ? 'undercover' : 'citizen'
      return { name, role, secret: String(r.secret ?? '') }
    })
    if (!roles.some((r) => r.role === 'undercover')) {
      const pick = Math.floor(Math.random() * roles.length)
      roles[pick] = { ...roles[pick], role: 'undercover' }
    }
    let found = false
    roles = roles.map((r) => {
      if (r.role === 'undercover') {
        if (found) return { ...r, role: 'citizen' }
        found = true
      }
      return r
    })
    return { scenario, roles }
  }, [])

  // 把「人数 + 昵称输入」解析成玩家名列表
  function resolveNames(): string[] {
    const custom = nameInput
      .split(/[\n,，、]/)
      .map((s) => s.trim())
      .filter(Boolean)
    if (custom.length >= 3) return custom.slice(0, 12)
    const n = Math.max(3, Math.min(12, playerCount || 3))
    return Array.from({ length: n }, (_, i) => `玩家${i + 1}`)
  }

  // 开局：生成剧情 + 身份
  const startGame = useCallback(async () => {
    const names = resolveNames()
    if (names.length < 3) {
      toastError('至少需要 3 名玩家')
      return
    }
    if (!model) {
      toastError('请先选择一个模型')
      return
    }
    if (!token.trim().startsWith('sk-')) {
      toastError('请填入 sk- 开头的访问令牌（在令牌页创建）')
      return
    }
    setLoading(true)
    setError('')
    try {
      const system = [
        '你是派对推理游戏「多人推理 · 找出卧底特工」的主持人（AI Host）。',
        '请为一局游戏生成设定，规则如下：',
        `1. 玩家共 ${names.length} 人；其中恰好 1 名为「卧底特工」（undercover），其余为「平民」（citizen）。`,
        '2. 平民不知道谁是卧底；卧底不知道自己是否被单独标记。',
        '3. 每位玩家获得一段「私密提示（secret）」，用于在讨论中伪装或推理，提示要有趣。',
        '4. 剧情主题轻松、适合聚会，每局不同（可校园 / 职场 / 悬疑 / 科幻等）。',
        '只输出一个 JSON 代码块，不要任何额外解释，结构如下：',
        '{"scenario":"面向全体的公开剧情（2-4 句）","roles":[{"name":"玩家名","role":"undercover 或 citizen","secret":"该玩家私密提示"}]}',
        'roles 的数量与顺序必须与下面给出的玩家名单逐一对应。',
      ].join('\n')
      const user = `玩家名单（按顺序）：${names.join('、')}。请生成设定。`
      const raw = await callAI(system, user)
      const next = parseSetup(raw, names)
      setSetup(next)
      setRevealIdx(0)
      setRevealShown(false)
      setVotes({})
      setVoteIdx(0)
      setVoteTarget(null)
      setTwist('')
      setResultText('')
      setSecsLeft(DISCUSS_SECONDS)
      setTimerOn(false)
      setPhase('briefing')
    } catch (e) {
      setError((e as Error).message || '生成失败，请检查令牌与模型后重试')
    } finally {
      setLoading(false)
    }
  }, [model, token, callAI, parseSetup, toastError, nameInput, playerCount])

  // 讨论阶段：让 AI 推进一段剧情（可选功能，同一模型）
  const generateTwist = useCallback(async () => {
    if (!setup) return
    setTwistStreaming(true)
    setTwist('')
    try {
      const system = '你是「多人推理」游戏主持人，请用一段生动、带悬念的主持人口吻推进当前剧情，制造讨论素材。不要输出 JSON，直接给正文（3-5 句）。'
      const user = `当前公开剧情：${setup.scenario}\n玩家：${setup.roles.map((r) => r.name).join('、')}\n请推进剧情。`
      await callAI(system, user, (t) => setTwist(t))
    } catch (e) {
      setTwist((e as Error).message || '剧情推进失败')
    } finally {
      setTwistStreaming(false)
    }
  }, [setup, callAI])

  // 投票：提交当前投票者选择
  const commitVote = useCallback(() => {
    if (voteTarget === null || !setup) return
    const nextVotes = { ...votes, [voteIdx]: voteTarget }
    if (voteIdx >= setup.roles.length - 1) {
      setVotes(nextVotes)
      setVoteIdx(0)
      setVoteTarget(null)
      void revealResult(nextVotes)
    } else {
      setVotes(nextVotes)
      setVoteIdx((i) => i + 1)
      setVoteTarget(null)
    }
  }, [voteTarget, votes, voteIdx, setup])

  // 结算：代码计票 + AI 复盘旁白
  const revealResult = useCallback(
    async (finalVotes: Record<number, number>) => {
      if (!setup) return
      setPhase('result')
      const n = setup.roles.length
      const tally = Array.from({ length: n }, (_, i) =>
        Object.values(finalVotes).filter((v) => v === i).length,
      )
      const undercoverIdx = setup.roles.findIndex((r) => r.role === 'undercover')
      const maxVotes = Math.max(...tally)
      const expelled = tally
        .map((c, i) => (c === maxVotes && maxVotes > 0 ? i : -1))
        .filter((i) => i >= 0)
      const undercoverCaught = expelled.includes(undercoverIdx)
      const winner = undercoverCaught ? '平民阵营' : '卧底特工'

      setResultStreaming(true)
      setResultText('')
      try {
        const system = '你是「多人推理」游戏主持人。请用生动、带悬念的主持人口吻宣读本局投票复盘，最后明确点明胜负。不要输出 JSON，直接给正文。'
        const lines = [
          `公开剧情：${setup.scenario}`,
          `各玩家身份：${setup.roles.map((r) => `${r.name}(${r.role === 'undercover' ? '卧底' : '平民'})`).join('、')}`,
          `投票情况：${setup.roles
            .map((r, i) => `${r.name} 投给 ${setup.roles[finalVotes[i]]?.name ?? '弃票'}`)
            .join('；')}`,
          `票数与结果：被投最多的是 ${expelled.map((i) => setup.roles[i].name).join('、')}（各 ${maxVotes} 票）；卧底身份是 ${setup.roles[undercoverIdx].name}；最终胜方：${winner}。`,
        ].join('\n')
        await callAI(system, lines, (t) => setResultText(t))
      } catch {
        setResultText(`本局结束：胜方为「${winner}」，卧底特工是 ${setup.roles[undercoverIdx].name}。`)
      } finally {
        setResultStreaming(false)
      }
    },
    [setup, callAI],
  )

  // 重开
  const reset = useCallback(() => {
    setSetup(null)
    setPhase('setup')
    setError('')
    setTwist('')
    setResultText('')
    setVotes({})
    setRevealIdx(0)
    setVoteIdx(0)
  }, [])

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      {/* 游戏自有顶栏：站点图标 + 站名 | 游戏名 | 返回任意门 */}
      <header className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-line bg-linear-to-r from-brand/10 to-transparent px-4 py-3">
        <div className="flex items-center gap-2">
          <BrandLogo name={siteName} />
        </div>
        <div className="order-3 w-full text-center text-[15px] font-semibold text-ink sm:order-2 sm:w-auto">
          {GAME_TITLE}
        </div>
        <Link href="/console/playground/anydoor" className="order-2 sm:order-3">
          <Button variant="secondary" size="sm">
            返回任意门
          </Button>
        </Link>
      </header>

      {error && (
        <div className="rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err">{error}</div>
      )}

      {phase === 'setup' && (
        <SetupPanel
          models={models}
          model={model}
          onModel={setModel}
          token={token}
          onToken={setToken}
          playerCount={playerCount}
          onPlayerCount={setPlayerCount}
          nameInput={nameInput}
          onNameInput={setNameInput}
          loading={loading}
          onStart={startGame}
        />
      )}

      {phase === 'briefing' && setup && (
        <Card className="space-y-4">
          <div>
            <h2 className="text-[15px] font-semibold text-ink">公开剧情</h2>
            <p className="mt-2 whitespace-pre-wrap text-[14px] leading-relaxed text-ink-2">{setup.scenario}</p>
          </div>
          <div className="rounded-md border border-line bg-surface px-3 py-2 text-[13px] text-ink-3">
            接下来请把设备交给每位玩家，依次查看自己的<b className="text-ink">秘密身份</b>。看完后进入讨论与投票。
          </div>
          <div className="flex justify-end">
            <Button
              variant="primary"
              onClick={() => {
                setRevealIdx(0)
                setRevealShown(false)
                setPhase('reveal')
              }}
            >
              开始分发身份
            </Button>
          </div>
        </Card>
      )}

      {phase === 'reveal' && setup && (
        <RevealPanel
          setup={setup}
          idx={revealIdx}
          shown={revealShown}
          onShow={() => setRevealShown(true)}
          onNext={() => {
            if (revealIdx >= setup.roles.length - 1) {
              setRevealShown(false)
              setPhase('discuss')
            } else {
              setRevealIdx((i) => i + 1)
              setRevealShown(false)
            }
          }}
        />
      )}

      {phase === 'discuss' && setup && (
        <DiscussPanel
          scenario={setup.scenario}
          twist={twist}
          twistStreaming={twistStreaming}
          secsLeft={secsLeft}
          timerOn={timerOn}
          onToggleTimer={() => setTimerOn((v) => !v)}
          onResetTimer={() => {
            setSecsLeft(DISCUSS_SECONDS)
            setTimerOn(false)
          }}
          onTwist={generateTwist}
          onVote={() => {
            setVoteIdx(0)
            setVoteTarget(null)
            setPhase('vote')
          }}
        />
      )}

      {phase === 'vote' && setup && (
        <VotePanel
          setup={setup}
          idx={voteIdx}
          target={voteTarget}
          onTarget={setVoteTarget}
          onCommit={commitVote}
        />
      )}

      {phase === 'result' && setup && (
        <ResultPanel
          setup={setup}
          votes={votes}
          text={resultText}
          streaming={resultStreaming}
          onRestart={reset}
        />
      )}
    </div>
  )
}

/* ── 子组件 ───────────────────────────────────────────── */

function SetupPanel(props: {
  models: string[]
  model: string
  onModel: (v: string) => void
  token: string
  onToken: (v: string) => void
  playerCount: number
  onPlayerCount: (v: number) => void
  nameInput: string
  onNameInput: (v: string) => void
  loading: boolean
  onStart: () => void
}) {
  const { models, model, onModel, token, onToken, playerCount, onPlayerCount, nameInput, onNameInput, loading, onStart } = props
  return (
    <Card className="space-y-4">
      <div>
        <h2 className="text-[15px] font-semibold text-ink">开局设置</h2>
        <p className="mt-1 text-[13px] text-ink-3">
          选择模型并填入你的访问令牌，AI 将为本局生成剧情与秘密身份。所有推理均由本站模型完成。
        </p>
      </div>

      <Field label="模型" required help="开始前自选一个可用模型；下方列表来自本站模型广场">
        <Select value={model} onChange={(e) => onModel(e.target.value)}>
          {models.length === 0 && <option value="">加载中…</option>}
          {models.map((m) => (
            <option key={m} value={m}>{m}</option>
          ))}
        </Select>
      </Field>

      <Field label="访问令牌" required help="sk- 开头的 API 访问令牌（不是登录密码），在令牌页创建">
        <Input
          type="password"
          value={token}
          onChange={(e) => onToken(e.target.value)}
          placeholder="填入 sk- 开头的访问令牌"
        />
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="玩家人数" help="3–12 人；若下方填写了昵称则以昵称为准">
          <Input
            type="number"
            min={3}
            max={12}
            value={playerCount}
            onChange={(e) => onPlayerCount(Number(e.target.value))}
          />
        </Field>
        <Field label="昵称（可选）" help="用逗号 / 换行分隔，至少 3 个；留空则自动命名为玩家1…">
          <Input
            value={nameInput}
            onChange={(e) => onNameInput(e.target.value)}
            placeholder="如：小明, 小红, 阿强"
          />
        </Field>
      </div>

      <div className="flex items-center gap-3 pt-1">
        <Button variant="primary" onClick={onStart} loading={loading}>
          {loading ? 'AI 生成中…' : '开始游戏'}
        </Button>
        <span className="text-xs text-ink-3">生成后将进入身份分发环节</span>
      </div>
    </Card>
  )
}

function RevealPanel(props: {
  setup: GameSetup
  idx: number
  shown: boolean
  onShow: () => void
  onNext: () => void
}) {
  const { setup, idx, shown, onShow, onNext } = props
  const player = setup.roles[idx]
  const isLast = idx >= setup.roles.length - 1
  return (
    <Card className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold text-ink">身份分发（传设备）</h2>
        <Badge tone="info">{idx + 1} / {setup.roles.length}</Badge>
      </div>

      <p className="text-[13px] text-ink-3">
        请把设备交给 <b className="text-ink">{player.name}</b>，确认只有 TA 能看到屏幕后点击查看。
      </p>

      {!shown ? (
        <div className="flex justify-center py-6">
          <Button variant="primary" onClick={onShow}>
            我是 {player.name}，查看我的身份
          </Button>
        </div>
      ) : (
        <div className="rounded-lg border border-line bg-surface px-4 py-5 text-center">
          <div className="text-[13px] text-ink-3">你的身份</div>
          <div className={`mt-1 text-2xl font-bold ${player.role === 'undercover' ? 'text-err' : 'text-ok'}`}>
            {player.role === 'undercover' ? '卧底特工' : '平民'}
          </div>
          {player.secret && (
            <p className="mt-3 text-[14px] leading-relaxed text-ink-2">{player.secret}</p>
          )}
          <p className="mt-3 text-xs text-ink-3">看完后请藏好设备，交给下一位。</p>
        </div>
      )}

      <div className="flex justify-end">
        <Button variant="secondary" onClick={onNext} disabled={!shown}>
          {isLast ? '全部看完，进入讨论' : '下一位玩家'}
        </Button>
      </div>
    </Card>
  )
}

function DiscussPanel(props: {
  scenario: string
  twist: string
  twistStreaming: boolean
  secsLeft: number
  timerOn: boolean
  onToggleTimer: () => void
  onResetTimer: () => void
  onTwist: () => void
  onVote: () => void
}) {
  const { scenario, twist, twistStreaming, secsLeft, timerOn, onToggleTimer, onResetTimer, onTwist, onVote } = props
  const mm = String(Math.floor(secsLeft / 60)).padStart(2, '0')
  const ss = String(secsLeft % 60).padStart(2, '0')
  return (
    <Card className="space-y-4">
      <h2 className="text-[15px] font-semibold text-ink">讨论阶段</h2>
      <p className="whitespace-pre-wrap text-[14px] leading-relaxed text-ink-2">{scenario}</p>

      <div className="flex flex-wrap items-center gap-3 rounded-md border border-line bg-surface px-3 py-2">
        <span className="font-mono text-lg text-ink">{mm}:{ss}</span>
        <Button variant="secondary" size="sm" onClick={onToggleTimer}>
          {timerOn ? '暂停' : '开始计时'}
        </Button>
        <Button variant="ghost" size="sm" onClick={onResetTimer}>
          重置
        </Button>
        <span className="text-xs text-ink-3">建议每人发言，找出破绽</span>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button variant="secondary" size="sm" onClick={onTwist} loading={twistStreaming}>
          {twistStreaming ? 'AI 推进中…' : '让 AI 推进剧情'}
        </Button>
      </div>
      {twist && (
        <div className="whitespace-pre-wrap rounded-md border border-line bg-card px-3 py-2 text-[14px] leading-relaxed text-ink-2">
          {twist}
        </div>
      )}

      <div className="flex justify-end">
        <Button variant="primary" onClick={onVote}>
          开始投票
        </Button>
      </div>
    </Card>
  )
}

function VotePanel(props: {
  setup: GameSetup
  idx: number
  target: number | null
  onTarget: (v: number) => void
  onCommit: () => void
}) {
  const { setup, idx, target, onTarget, onCommit } = props
  const voter = setup.roles[idx]
  const isLast = idx >= setup.roles.length - 1
  return (
    <Card className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold text-ink">投票（传设备）</h2>
        <Badge tone="info">{idx + 1} / {setup.roles.length}</Badge>
      </div>
      <p className="text-[13px] text-ink-3">
        请把设备交给 <b className="text-ink">{voter.name}</b>，TA 投票选出心目中的卧底特工（不能投自己）。
      </p>

      <div className="grid gap-2 sm:grid-cols-2">
        {setup.roles.map((r, i) => {
          if (i === idx) return null
          const active = target === i
          return (
            <button
              key={r.name}
              type="button"
              onClick={() => onTarget(i)}
              className={`rounded-lg border px-3 py-2.5 text-left text-[14px] transition ${
                active
                  ? 'border-brand bg-brand/8 font-medium text-brand'
                  : 'border-line bg-card text-ink-2 hover:border-ink-3'
              }`}
            >
              {r.name}
            </button>
          )
        })}
      </div>

      <div className="flex justify-end">
        <Button variant="primary" onClick={onCommit} disabled={target === null}>
          {isLast ? '查看结果' : '下一位投票'}
        </Button>
      </div>
    </Card>
  )
}

function ResultPanel(props: {
  setup: GameSetup
  votes: Record<number, number>
  text: string
  streaming: boolean
  onRestart: () => void
}) {
  const { setup, votes, text, streaming, onRestart } = props
  const n = setup.roles.length
  const tally = Array.from({ length: n }, (_, i) =>
    Object.values(votes).filter((v) => v === i).length,
  )
  const undercoverIdx = setup.roles.findIndex((r) => r.role === 'undercover')
  const maxVotes = Math.max(...tally)
  const expelled = tally
    .map((c, i) => (c === maxVotes && maxVotes > 0 ? i : -1))
    .filter((i) => i >= 0)
  const undercoverCaught = expelled.includes(undercoverIdx)
  const winner = undercoverCaught ? '平民阵营' : '卧底特工'

  return (
    <Card className="space-y-4">
      <h2 className="text-[15px] font-semibold text-ink">本局结果</h2>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="rounded-lg border border-line bg-surface px-3 py-2">
          <div className="text-xs text-ink-3">最终胜方</div>
          <div className="mt-0.5 text-lg font-bold text-brand">{winner}</div>
        </div>
        <div className="rounded-lg border border-line bg-surface px-3 py-2">
          <div className="text-xs text-ink-3">卧底特工</div>
          <div className="mt-0.5 text-lg font-bold text-err">{setup.roles[undercoverIdx].name}</div>
        </div>
      </div>

      <div>
        <div className="mb-1.5 text-[13px] text-ink-3">得票情况</div>
        <div className="space-y-1.5">
          {setup.roles.map((r, i) => (
            <div key={r.name} className="flex items-center gap-2 text-[13px]">
              <span className="w-20 shrink-0 text-ink-2">{r.name}</span>
              <span className="h-2.5 flex-1 overflow-hidden rounded bg-ink/10">
                <span
                  className={`block h-full ${i === undercoverIdx ? 'bg-err' : 'bg-brand'}`}
                  style={{ width: `${maxVotes ? (tally[i] / maxVotes) * 100 : 0}%` }}
                />
              </span>
              <span className="w-8 shrink-0 text-right font-mono text-ink-2">{tally[i]}</span>
            </div>
          ))}
        </div>
      </div>

      {text && (
        <div className="whitespace-pre-wrap rounded-md border border-line bg-card px-3 py-3 text-[14px] leading-relaxed text-ink-2">
          {text}
          {streaming && <span className="ml-0.5 animate-pulse text-ink-3">▍</span>}
        </div>
      )}

      <div className="flex justify-end">
        <Button variant="primary" onClick={onRestart}>
          再来一局
        </Button>
      </div>
    </Card>
  )
}
