/**
 * AI Agent 接口：后台配置与密钥管理、对话（流式）。
 *
 * 意图（Why）：
 *   站长要能自己完成两件事：配好"助手用什么模型、说什么话"，
 *   以及给外部人发一把"只能问客服"的钥匙。本模块把这两件事的
 *   类型定义与调用方式收敛在一处，视图层只关心渲染。
 *
 * 流转（Flow）：
 *   admin/(panel)/agent/page.tsx → 本模块 → /api/admin/agent/*
 *   对话：chatStream() → fetch + ReadableStream → 逐条 onEvent 回调
 *
 * 扩展（Extend）：
 *   新增 agent 能力时，先在服务端 agent 包加工具，再在这里加类型。
 *   不要在前端硬编码"客服有哪些工具"之类的判断——那由服务端随角色下发
 *   （见 AgentAskResult.tools），前端只负责按它渲染。
 */
import { api, getSessionToken } from './client'

// ── 配置 ────────────────────────────────────────────────────────

/** Agent 运行配置 */
export interface AgentSettings {
  /** 总开关。关闭时两个入口都不可用 */
  enabled: boolean
  /** 兜底模型名；角色未单独配置时用它 */
  default_model: string
  /** 运维助手专用模型（留空则用默认） */
  ops_model: string
  /** 在线客服专用模型（留空则用默认） */
  support_model: string
  /** 运维助手系统提示词（留空则用内置底稿） */
  ops_system_prompt: string
  /** 在线客服系统提示词（留空则用内置底稿） */
  support_system_prompt: string
  /** 是否携带历史对话 */
  history_enabled: boolean
  /**
   * 运维提示词当前是否回退到内置底稿。
   *
   * 由服务端下发而非前端自行判断：内置底稿会随版本变化，
   * 前端若自己认定"空 = 有默认"，将来后端改版就会显示一个
   * 已不存在的提示词，用户照着填半天毫无意义。
   */
  ops_prompt_is_default: boolean
  /** 同上，客服提示词 */
  support_prompt_is_default: boolean
  /** 提示词输入框的占位文案 */
  prompt_placeholder: string
}

/**
 * 配置保存请求。
 *
 * 全部为可选：未传的项在服务端保持原值。
 * 这不是"局部更新"的风格选择，而是必需的——提示词很长，
 * 若"未传即清空"，一次只改模型名的提交就会把站长写的客服话术抹掉。
 */
export interface AgentSettingsInput {
  enabled?: boolean
  default_model?: string
  ops_model?: string
  support_model?: string
  ops_system_prompt?: string
  support_system_prompt?: string
  history_enabled?: boolean
}

// ── 密钥 ────────────────────────────────────────────────────────

/** 密钥角色 */
export type AgentRole = 'ops' | 'support'

/** 密钥列表项 */
export interface AgentKeyItem {
  id: number
  role: AgentRole
  /** 角色中文名（服务端下发，前端不自行翻译） */
  role_label: string
  /** 站长备注 */
  name: string
  /** 1 启用 / 2 禁用 */
  status: number
  /** 是否已过期 */
  expired: boolean
  /** 是否当前可用（启用且未过期） */
  active: boolean
  /**
   * 过期时间（RFC3339）。
   *
   * 永不过期时服务端【不下发】此字段，而不是下发 1970 年的零值——
   * 后者会被多数日期控件当成"已过期"显示出来。
   */
  expires_at?: string
  created_at: string
}

/** 创建密钥的响应 */
export interface AgentKeyCreated extends AgentKeyItem {
  /**
   * 密钥明文，**仅本次响应返回**。
   *
   * 之后再调任何接口都拿不到它（服务端只存摘要）。
   * 前端必须明确提示用户"只显示这一次"。
   */
  key: string
  /** 服务端给的提示文案 */
  warning: string
}

// ── 对话 ────────────────────────────────────────────────────────

/** 历史里的一轮 */
export interface AgentChatTurn {
  role: 'user' | 'assistant'
  content: string
}

/** 对话请求 */
export interface AgentChatInput {
  question: string
  /** 历史（不含本轮）。服务端会丢弃其中的 system / tool 消息 */
  history?: AgentChatTurn[]
  /**
   * 指定模型。
   *
   * 【仅运维入口生效】客服入口会忽略它——否则外部人就能指定一个
   * 贵得离谱的模型让站长付账。
   */
  model?: string
}

/** 工具执行结果（结束时回传，用于展示过程） */
export interface AgentToolCall {
  name: string
  /** 是否是写操作（前端据此提示"这一步会改动配置"） */
  mutating: boolean
  ok: boolean
  /** 失败原因（面向站长） */
  error?: string
  /** 结果摘要（截断到 200 字，不含密钥等敏感内容） */
  result?: string
}

/** 对话结束时的结果 */
export interface AgentAskResult {
  answer: string
  tool_calls: AgentToolCall[]
  rounds: number
  /** 撞上轮次上限，答复可能不完整 */
  truncated: boolean
  prompt_tokens: number
  completion_tokens: number
  role: AgentRole
  /** 本次给了模型哪些工具（客服为空数组） */
  tools: { name: string }[]
}

/**
 * 对话过程中的一个事件。
 *
 * 四种类型对应流式界面的四种状态：
 *   delta → 追加正文（打字机效果）
 *   tool  → 显示"正在查 X"（写操作要额外警示）
 *   done  → 收尾，替换为完整答案
 *   error → 出错，展示可重试提示
 */
export type AgentStreamEvent =
  | { type: 'delta'; text: string }
  | { type: 'tool'; tool: { name: string; mutating: boolean } }
  | { type: 'done'; result: AgentAskResult }
  | { type: 'error'; message: string }
  | { type: 'end' }

// ── 调用 ────────────────────────────────────────────────────────

/** 读取 agent 配置 */
export async function fetchAgentSettings(): Promise<AgentSettings> {
  return api.get<AgentSettings>('/admin/agent/settings')
}

/** 保存 agent 配置 */
export async function saveAgentSettings(
  input: AgentSettingsInput,
): Promise<AgentSettings> {
  return api.put<AgentSettings>('/admin/agent/settings', input)
}

/** 列出 agent 密钥 */
export async function fetchAgentKeys(role?: AgentRole): Promise<AgentKeyItem[]> {
  // 第二个参数是【查询参数】（api.get 的签名如此），不是 axios 的 config。
  const result = await api.get<{ items: AgentKeyItem[] }>(
    '/admin/agent/keys',
    role ? { role } : undefined,
  )
  return result.items
}

/** 创建一把密钥（响应含一次性明文） */
export async function createAgentKey(input: {
  role: AgentRole
  name: string
  expires_in_days?: number
}): Promise<AgentKeyCreated> {
  return api.post<AgentKeyCreated>('/admin/agent/keys', input)
}

/** 启用 / 禁用 / 改名一把密钥 */
export async function updateAgentKey(
  id: number,
  input: { status?: number; name?: string },
): Promise<AgentKeyItem> {
  return api.put<AgentKeyItem>(`/admin/agent/keys/${id}`, input)
}

/** 删除一把密钥（立即失效，不可恢复） */
export async function deleteAgentKey(id: number): Promise<void> {
  await api.delete(`/admin/agent/keys/${id}`)
}

/**
 * 与运维助手对话（后台入口，SSE 流式）。
 *
 * 为什么不能用 api.post：axios 的响应拦截器等的是完整响应体，
 * 而 SSE 需要边收边处理。必须用 fetch + ReadableStream 自己读。
 */
export function chatWithOps(
  input: AgentChatInput,
  onEvent: (event: AgentStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  return streamChat('/admin/agent/chat', input, onEvent, signal)
}

/**
 * 与在线客服对话（公开入口，需要 agent key）。
 *
 * 与运维入口的差别只有两点：路径不同、需要自行附加 Bearer 密钥。
 * 不共用一条请求函数是为了让两者的鉴权方式在代码里就分开——
 * 客服密钥是发给外部人的，绝不能因为"复用了同一个函数"而
 * 悄悄被带上站长的会话令牌。
 */
export function chatWithSupport(
  agentKey: string,
  input: AgentChatInput,
  onEvent: (event: AgentStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  return streamChat('/agent/chat', input, onEvent, signal, agentKey)
}

/**
 * streamChat 发起一次流式对话并逐条回调事件。
 *
 * 解析方式刻意手写而不是用 EventSource：EventSource 只支持 GET，
 * 而对话必须用 POST（问题内容要放请求体，且不能进 URL / 日志 / 缓存）。
 */
async function streamChat(
  path: string,
  input: AgentChatInput,
  onEvent: (event: AgentStreamEvent) => void,
  signal?: AbortSignal,
  bearer?: string,
): Promise<void> {
  const base = process.env.NEXT_PUBLIC_API_BASE || '/api'
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (bearer) {
    headers.Authorization = `Bearer ${bearer}`
  } else {
    // 走会话鉴权时复用客户端的令牌读取。
    // 不在这里自己读 localStorage：那个键由 client.ts 与 stores/auth.ts 共用，
    // 复制一份必然漂移，而漂移的表现是"登录状态随机失效"。
    const token = getSessionToken()
    if (token) headers.Authorization = `Bearer ${token}`
  }

  const res = await fetch(`${base}${path}`, {
    method: 'POST',
    headers,
    body: JSON.stringify(input),
    signal,
  })

  if (!res.ok) {
    // 非 2xx 时服务端返回的是普通 JSON 而非事件流，
    // 直接把它当成一次普通错误抛出，交给统一客户端的错误文案逻辑。
    throw await toStreamError(res)
  }
  if (!res.body) {
    throw new Error('浏览器不支持流式响应')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE 以空行分隔事件。必须留最后一段不处理：
    // 一条事件可能被 TCP 分成两块到达，
    // 见到第一个空行就消费会把半条 JSON 当成完整事件解析。
    let idx: number
    while ((idx = buffer.indexOf('\n\n')) !== -1) {
      const chunk = buffer.slice(0, idx)
      buffer = buffer.slice(idx + 2)
      const event = parseSSEChunk(chunk)
      if (event) onEvent(event)
    }
  }
}

/**
 * parseSSEChunk 解析一个 SSE 事件块。
 *
 * 只处理 data 行：以 ":" 开头的是注释（心跳），按规范忽略。
 */
function parseSSEChunk(chunk: string): AgentStreamEvent | null {
  const dataLines: string[] = []
  for (const line of chunk.split('\n')) {
    if (line.startsWith(':')) continue
    if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  if (dataLines.length === 0) return null
  try {
    return JSON.parse(dataLines.join('\n')) as AgentStreamEvent
  } catch {
    // 半条 JSON（理论上不该出现，因为按空行切分）——跳过而不是抛错，
    // 否则一条坏事件会让整段回答消失。
    return null
  }
}

/** 把非 2xx 的流式响应转成 Error，尽量沿用后端给的中文文案。 */
async function toStreamError(res: Response): Promise<Error> {
  try {
    const body = await res.json()
    const message = body?.error?.message
    if (typeof message === 'string' && message) return new Error(message)
  } catch {
    // 响应体不是 JSON（网关返回的纯文本错误页），走下面的兜底。
  }
  return new Error(`请求失败（${res.status}）`)
}
