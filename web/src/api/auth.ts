/**
 * 认证接口：登录、注册、当前用户、退出。
 *
 * 意图（Why）：
 *   把「会话令牌的获取与吊销」收敛到一处，供 stores/auth.ts 调用；
 *   视图层不直接触碰令牌，只操作 store 的语义化方法。
 *
 * 流转（Flow）：
 *   stores/auth.ts → login()/register() → POST /api/auth/* → 返回 session_token
 *                 → client.setSessionToken() 落盘
 *   /auth/me 在每次应用启动时调用一次，用于校正本地用户快照（额度/角色可能已变）。
 *
 * 扩展（Extend）：
 *   新增认证方式（如 OAuth）时在此加函数，并在 stores/auth.ts 暴露对应 action。
 */
import { api } from './client'
import type { AuthResult, AuthUser, EmailCodeResult, LoginPayload, RegisterPayload } from './types'

/** POST /api/auth/login：用户名 + 密码登录 */
export function login(payload: LoginPayload): Promise<AuthResult> {
  return api.post<AuthResult>('/auth/login', payload)
}

/**
 * POST /api/auth/admin-login：超管入口登录（只提交密码，不提交用户名）。
 *
 * 为什么单独一个接口而不是把用户名也填成隐藏值：后端需要按"仅密码"的语义
 * 枚举管理员，并对管理员数量过多的情况给出明确拒绝理由（见后端注释）。
 */
export function adminLogin(password: string): Promise<AuthResult> {
  return api.post<AuthResult>('/auth/admin-login', { password })
}

/**
 * POST /api/auth/register：受站点 registration_enabled 开关控制（由调用方先行校验）。
 *
 * 站点开启"注册必须邮箱验证码"时，后端会强制校验 email + code；
 * 未开启时二者可选（留空则不发送，避免后端把空字符串当成显式空值）。
 *
 * invite_code 为可选邀请码（来自邀请链接 ?invite=CODE）：
 * 用交叉类型在本函数局部扩展，避免改动共享的 RegisterPayload 定义；
 * 后端对非法邀请码会忽略并照常注册，因此这里只在有值时透传。
 */
export function register(payload: RegisterPayload & { invite_code?: string }): Promise<AuthResult> {
  const body: RegisterPayload & { invite_code?: string } = {
    username: payload.username,
    password: payload.password,
    // 协议同意必须有值：后端强制校验，缺失会被直接拒绝
    agreed_terms: payload.agreed_terms,
  }
  if (payload.email) body.email = payload.email
  if (payload.code) body.code = payload.code
  if (payload.invite_code) body.invite_code = payload.invite_code
  return api.post<AuthResult>('/auth/register', body)
}

/**
 * 验证码用途。
 *
 * 三种用途在后端是**严格区分**的：验证码按用途哈希与查找，
 * 一条注册码无法拿去登录，一条登录码也无法改密。
 */
export type EmailCodePurpose = 'register' | 'login' | 'reset'

/**
 * POST /api/auth/email-code：申请邮箱验证码。
 *
 * 后端返回 cooldown（冷却秒数）与 expires_in（有效期秒数），
 * 前端据此驱动倒计时，避免把这两个数值硬编码在页面里。
 *
 * 对 login / reset 用途，后端**不会**因为"该邮箱未注册"而报错
 * （否则接口会变成邮箱枚举器），因此前端不要依据此接口判断账号是否存在。
 */
export function sendEmailCode(
  email: string,
  purpose: EmailCodePurpose = 'register',
): Promise<EmailCodeResult> {
  return api.post<EmailCodeResult>('/auth/email-code', { email, purpose })
}

/**
 * POST /api/auth/email-login：邮箱验证码登录。
 *
 * 用户不必记得当初注册的用户名，用邮箱收码即可进入控制台。
 */
export function emailLogin(email: string, code: string): Promise<AuthResult> {
  return api.post<AuthResult>('/auth/email-login', { email, code })
}

/**
 * POST /api/auth/password-reset：邮箱验证码重置密码。
 *
 * 成功后后端会吊销该账号的全部会话，因此调用方应清除本地登录态并引导重新登录。
 */
export function resetPassword(
  email: string,
  code: string,
  password: string,
): Promise<{ ok: boolean; message: string }> {
  return api.post<{ ok: boolean; message: string }>('/auth/password-reset', {
    email,
    code,
    password,
  })
}

/**
 * GET /api/auth/me：读取当前登录用户。
 *
 * 契约只写了「当前登录用户信息」，未明确响应是 { ...user } 还是 { user: {...} }，
 * 这里做兼容解析（优先取 user 字段），待后端定稿后可精简。
 */
export async function fetchMe(): Promise<AuthUser> {
  const data = await api.get<AuthUser & { user?: AuthUser }>('/auth/me')
  return (data?.user ?? data) as AuthUser
}

/** POST /api/auth/logout：吊销当前会话 */
export function logout(): Promise<unknown> {
  return api.post<unknown>('/auth/logout')
}

/* ── QIU 科技账号登录 ──────────────────────────────────────────────────────
 *
 * 三段式：
 *   ① startQIULogin()  → 拿到 task_id 与「对方确认页」的 url
 *   ② 把 url 开给用户（window.open），由用户在 QIU 侧点确认
 *   ③ qiuLoginStatus() 轮询该 task_id，直到拿到状态机的终态
 *
 * 为什么由前端驱动轮询而不是后端自己转：等待多久取决于用户何时点确认，
 * 只有浏览器这一侧知道用户还在不在页面上；后端转轮询会变成一堆没人认领的任务。
 */

/** QIU 登录任务状态机的取值。 */
export type QIULoginStatus = 'pending' | 'ok' | 'denied' | 'expired'

/** POST /api/auth/qiu/start：创建一次第三方登录任务。 */
export function startQIULogin(): Promise<{ task_id: string; url: string }> {
  return api.post<{ task_id: string; url: string }>('/auth/qiu/start')
}

/**
 * GET /api/auth/qiu/status/{taskId}：查询任务结论。
 *
 * 只有 status === 'ok' 时才带 session_token / user —— 调用方必须严格按此判断，
 * 不能只看"有没有 user"，否则 pending 会被误当成登录成功。
 */
export function qiuLoginStatus(
  taskId: string,
): Promise<{ status: QIULoginStatus; session_token: string; expires_at: number; user: AuthUser }> {
  return api.get<{ status: QIULoginStatus; session_token: string; expires_at: number; user: AuthUser }>(
    `/auth/qiu/status/${encodeURIComponent(taskId)}`,
  )
}

/** POST /api/auth/qiu/bind：已登录用户把当前账号绑定到某个 QIU 身份。 */
export function bindQIUAccount(taskId: string): Promise<{ ok: boolean; username: string }> {
  return api.post<{ ok: boolean; username: string }>('/auth/qiu/bind', { task_id: taskId })
}
