/** 会话与会话状态管理（React Context 版，等价旧 Pinia auth store）。
 *
 * 意图（Why）：
 *   全站唯一的会话真相来源。路由守卫（console/admin 布局）在渲染前判断登录态；
 *   API 层的 401 通过 client.ts 的 setUnauthorizedHandler 触发清登录态（见 providers.tsx）。
 *
 * 流转（Flow）：
 *   启动：providers.tsx → bootstrap() → GET /api/auth/me → 校正 user
 *   登录：LoginView → signIn() → POST /api/auth/login → 保存 token + user
 *   登出：外壳退出按钮 → signOut() → POST /api/auth/logout → 清本地
 */
'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'

import { adminLogin, emailLogin, fetchMe, login, logout, register } from '@/api/auth'
import { ApiError, clearSession, getCachedUser, getSessionToken, setCachedUser, setSessionToken } from '@/api/client'
import { ROLE_ADMIN, type AuthResult, type AuthUser, type LoginPayload, type RegisterPayload } from '@/api/types'

interface AuthContextValue {
  /** 当前登录用户；null = 未登录 */
  user: AuthUser | null
  /** 是否已有会话令牌 */
  isLoggedIn: boolean
  /** 是否为管理员（role=10） */
  isAdmin: boolean
  /** 启动时会话校验是否完成（供守卫防止用旧快照误判） */
  ready: boolean
  displayName: string
  signIn: (payload: LoginPayload) => Promise<AuthUser>
  /** 直接接纳一份会话结果（第三方登录在其自己的流程里拿到了 session_token） */
  applySessionResult: (result: AuthResult) => AuthUser
  signInAsAdmin: (password: string) => Promise<AuthUser>
  signInWithEmail: (email: string, code: string) => Promise<AuthUser>
  signUp: (payload: RegisterPayload) => Promise<AuthUser>
  signOut: () => Promise<void>
  clearLocal: () => void
  bootstrap: () => Promise<void>
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(() => getCachedUser<AuthUser>())
  const [token, setToken] = useState<string>(() => getSessionToken())
  const [ready, setReady] = useState(false)

  const isLoggedIn = Boolean(token)
  const isAdmin = user?.role === ROLE_ADMIN
  const displayName = user?.username || '未登录'

  function applySession(result: AuthResult): void {
    setToken(result.session_token)
    setSessionToken(result.session_token)
    setUser(result.user)
    setCachedUser(result.user)
  }

  const clearLocal = useCallback(() => {
    setToken('')
    setUser(null)
    clearSession()
  }, [])

  async function signIn(payload: LoginPayload): Promise<AuthUser> {
    const result = await login(payload)
    applySession(result)
    return result.user
  }

  function applySessionResult(result: AuthResult): AuthUser {
    applySession(result)
    return result.user
  }

  async function signInAsAdmin(password: string): Promise<AuthUser> {
    const result = await adminLogin(password)
    applySession(result)
    return result.user
  }

  async function signInWithEmail(email: string, code: string): Promise<AuthUser> {
    const result = await emailLogin(email, code)
    applySession(result)
    return result.user
  }

  async function signUp(payload: RegisterPayload): Promise<AuthUser> {
    const result = await register(payload)
    applySession(result)
    return result.user
  }

  async function signOut(): Promise<void> {
    try {
      await logout()
    } catch {
      /* 令牌已失效时后端可能返回 401，不影响本地登出 */
    } finally {
      clearLocal()
    }
  }

  async function bootstrap(): Promise<void> {
    if (!token) {
      setReady(true)
      return
    }
    try {
      const me = await fetchMe()
      setUser(me)
      setCachedUser(me)
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) clearLocal()
    } finally {
      setReady(true)
    }
  }

  async function refreshUser(): Promise<void> {
    try {
      const me = await fetchMe()
      setUser(me)
      setCachedUser(me)
    } catch {
      /* 静默失败：页面展示上次快照，401 由 client 统一处理 */
    }
  }

  // 应用挂载时自动完成会话校正（等价旧 Vue 路由守卫的 bootstrap）：
  // 有令牌 → 调 /auth/me 校验；无令牌 → 直接标记 ready。
  useEffect(() => {
    void bootstrap()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const value = useMemo(
    () => ({
      user,
      isLoggedIn,
      isAdmin,
      ready,
      displayName,
      signIn,
      applySessionResult,
      signInAsAdmin,
      signInWithEmail,
      signUp,
      signOut,
      clearLocal,
      bootstrap,
      refreshUser,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [user, token, ready],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

/** 供 api client 在 401 时触发（由 providers.tsx 挂接） */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return ctx
}