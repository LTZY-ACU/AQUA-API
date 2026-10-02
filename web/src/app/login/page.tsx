/** 登录页：用户名或绑定邮箱 + 密码；另提供邮箱验证码登录与重置密码入口。
 *
 * 意图（Why）：
 *   单一决策点页面（CRAP 对比：居中卡片聚焦表单）。用户名/邮箱二选一由后端统一解析。
 */
'use client'

import { useRouter, useSearchParams } from 'next/navigation'
import { Suspense, useState } from 'react'

import { BrandLogo } from '@/components/BrandMark'
import { QIOLoginButton } from '@/components/auth/QIOLoginButton'
import { sendEmailCode } from '@/api/auth'
import { SiteFooter } from '@/components/site/SiteFooter'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { useAuth } from '@/lib/auth/auth-context'
import { safeRedirect } from '@/lib/auth/safe-redirect'
import { useToast } from '@/lib/toast/toast-context'
import type { LoginPayload } from '@/api/types'

function LoginForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const redirect = searchParams.get('redirect') || null
  const { signIn, signInWithEmail, applySessionResult } = useAuth()
  const { toastError } = useToast()

  const [mode, setMode] = useState<'password' | 'email'>('password')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  function afterLogin(isAdmin: boolean) {
    // redirect 来自查询串，可被构造成站外地址（开放重定向），
    // 过滤后只允许跳回本站路径；不合法就落到默认页。
    router.replace(safeRedirect(redirect, isAdmin ? '/admin' : '/console'))
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      if (mode === 'password') {
        if (!username.trim() || !password) {
          setError('请输入用户名/邮箱与密码')
          return
        }
        const payload: LoginPayload = { username: username.trim(), password }
        const user = await signIn(payload)
        afterLogin(user.role === 10)
      } else {
        if (!email.trim() || !code.trim()) {
          setError('请输入邮箱与验证码')
          return
        }
        const user = await signInWithEmail(email.trim(), code.trim())
        afterLogin(user.role === 10)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen flex-col">
      <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
        <a href="/" className="flex items-center gap-2">
          <BrandLogo />
        </a>
      </header>

      <main className="flex flex-1 items-start justify-center px-4 py-12 sm:py-20">
        <div className="w-full max-w-sm">
          <h1 className="text-xl font-bold text-ink">登录</h1>
          <p className="mt-1 text-[13px] text-ink-3">使用用户名或绑定邮箱登录</p>

          <div className="mt-6 flex gap-1 rounded-lg border border-line bg-surface p-1">
            {(['password', 'email'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={`flex-1 rounded-md px-3 py-1.5 text-[13px] transition ${
                  mode === m ? 'bg-card text-ink shadow-sm' : 'text-ink-3 hover:text-ink-2'
                }`}
              >
                {m === 'password' ? '密码登录' : '验证码登录'}
              </button>
            ))}
          </div>

          <form onSubmit={handleSubmit} className="mt-5 space-y-4">
            {mode === 'password' ? (
              <>
                <Field label="用户名 / 邮箱">
                  <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="用户名或绑定邮箱" autoComplete="username" />
                </Field>
                <Field label="密码">
                  <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="密码" autoComplete="current-password" />
                </Field>
              </>
            ) : (
              <>
                <Field label="邮箱">
                  <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" autoComplete="email" />
                </Field>
                <Field label="验证码" help="验证码发送到该邮箱，5 分钟内有效">
                  <div className="flex gap-2">
                    <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="6 位验证码" autoComplete="one-time-code" />
                    <SendCodeButton email={email} purpose="login" />
                  </div>
                </Field>
              </>
            )}

            {error && (
              <div className="rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err">{error}</div>
            )}

            <Button type="submit" variant="primary" loading={loading} className="w-full" size="lg">
              登录
            </Button>
          </form>

          {/* 第三方登录：与本站账号互不替代——首次登录会自动建号，
              已在站上有号的用户可在控制台里绑定同一个第三方身份。 */}
          <div className="mt-5">
            <QIOLoginButton
              disabled={loading}
              onSuccess={(result) => {
                const user = applySessionResult(result)
                afterLogin(user.role === 10)
              }}
            />
          </div>

          <div className="mt-4 flex items-center justify-between text-[13px]">
            <a href="/register" className="text-brand hover:underline">注册新账号</a>
            <a href="/forgot-password" className="text-ink-3 hover:text-brand">忘记密码？</a>
          </div>
        </div>
      </main>

      <SiteFooter />
    </div>
  )
}

/** 发送验证码按钮（登录/注册/重置共用逻辑） */
function SendCodeButton({ email, purpose }: { email: string; purpose: 'login' | 'register' | 'reset' }) {
  const [countdown, setCountdown] = useState(0)
  const { toast, toastError } = useToast()

  async function handleSend() {
    if (!email.trim() || !email.includes('@')) {
      toastError('请先填写正确的邮箱')
      return
    }
    try {
      const result = await sendEmailCode(email.trim(), purpose)
      toast(result.message || '验证码已发送，请查收邮件')
      if (result.cooldown > 0) {
        setCountdown(result.cooldown)
        const timer = setInterval(() => {
          setCountdown((prev) => {
            if (prev <= 1) {
              clearInterval(timer)
              return 0
            }
            return prev - 1
          })
        }, 1000)
      }
    } catch (err) {
      toastError(err instanceof Error ? err.message : '发送失败')
    }
  }

  return (
    <Button type="button" variant="secondary" disabled={countdown > 0} onClick={handleSend} className="shrink-0 whitespace-nowrap">
      {countdown > 0 ? `${countdown}s` : '发送验证码'}
    </Button>
  )
}

export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginForm />
    </Suspense>
  )
}