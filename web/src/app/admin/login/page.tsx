/** 超管登录页（/admin/login）：只输密码。
 *
 * 意图（Why）：
 *   刻意独立于普通登录（站长不需要回忆用户名）；与 /admin 的鉴权布局分离，
 *   避免「要登录后台才能看到登录后台的页面」死循环。
 */
'use client'

import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { Suspense, useState } from 'react'

import { BrandLogo } from '@/components/BrandMark'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { useAuth } from '@/lib/auth/auth-context'
import { safeRedirect } from '@/lib/auth/safe-redirect'

function AdminLoginForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const redirect = searchParams.get('redirect') || null
  const { signInAsAdmin } = useAuth()

  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    if (!password) {
      setError('请输入管理员密码')
      return
    }
    setLoading(true)
    try {
      const user = await signInAsAdmin(password)
      // 与普通登录同理：redirect 只放行站内地址，避免登录后被带去站外
      router.replace(safeRedirect(redirect, user.role === 10 ? '/admin' : '/console'))
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-surface">
      <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
        <Link href="/" className="flex items-center gap-2">
          <BrandLogo name="管理后台" />
        </Link>
      </header>

      <main className="flex flex-1 items-start justify-center px-4 py-16 sm:py-24">
        <div className="w-full max-w-sm">
          <h1 className="text-xl font-bold text-ink">管理员登录</h1>
          <p className="mt-1 text-[13px] text-ink-3">输入管理员密码进入后台</p>

          <form onSubmit={handleSubmit} className="mt-6 space-y-4">
            <Field label="管理员密码">
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="密码" autoComplete="current-password" />
            </Field>

            {error && <div className="rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err">{error}</div>}

            <Button type="submit" variant="primary" size="lg" className="w-full" loading={loading}>
              进入后台
            </Button>
          </form>
        </div>
      </main>
    </div>
  )
}

export default function AdminLoginPage() {
  return (
    <Suspense fallback={null}>
      <AdminLoginForm />
    </Suspense>
  )
}