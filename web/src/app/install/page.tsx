/** 安装向导页：创建第一个管理员账号。
 *
 * 意图（Why）：
 *   新部署实例里没有账号，需要「登录之前就能访问」的入口；
 *   已安装时后端会返回 409，页面转为「去登录」引导态。
 *
 * 流转（Flow）：
 *   install 状态 API → 三态渲染（检查中 / 已安装 / 向导表单）；
 *   全部文案一律走 t('site.legal.install.*')，站点名与邮箱等值仍来自后端。
 */
'use client'

import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { useCallback, useEffect, useState } from 'react'

import { BrandLogo } from '@/components/BrandMark'
import { fetchInstallStatus, submitInstall, type InstallStatus } from '@/api/install'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'

export default function InstallPage() {
  const router = useRouter()
  const { toastError } = useToast()
  const { t } = useI18n()

  const [installed, setInstalled] = useState<boolean | null>(null)
  const [siteName, setSiteName] = useState('')
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const loadStatus = useCallback(async () => {
    try {
      const status: InstallStatus = await fetchInstallStatus()
      setInstalled(status.installed)
      setSiteName(status.site_name)
    } catch {
      setInstalled(true) // 状态查询失败按已安装处理，避免误导用户重复安装
    }
  }, [])

  useEffect(() => {
    void loadStatus()
  }, [loadStatus])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    if (password.length < 8) {
      setError(t('site.legal.install.errPasswordShort'))
      return
    }
    if (password !== confirm) {
      setError(t('site.legal.install.errPasswordMismatch'))
      return
    }
    setLoading(true)
    try {
      const result = await submitInstall({
        username: username.trim() || 'admin',
        password,
        confirm_password: confirm,
        site_name: siteName.trim() || undefined,
      })
      router.replace(result.admin_login_path || '/admin/login')
    } catch (err) {
      setError(err instanceof Error ? err.message : t('site.legal.install.errFailed'))
    } finally {
      setLoading(false)
    }
  }

  if (installed === null) {
    return (
      <div className="flex min-h-screen items-center justify-center text-ink-3">
        {t('site.legal.install.checking')}
      </div>
    )
  }

  if (installed) {
    return (
      <div className="flex min-h-screen flex-col bg-surface">
        <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
          <div className="flex items-center gap-2">
            <BrandLogo name={siteName || 'LTZY-API'} />
          </div>
        </header>
        <main className="flex flex-1 items-center justify-center px-4">
          <div className="w-full max-w-sm text-center">
            <h1 className="text-xl font-bold text-ink">{t('site.legal.install.doneTitle')}</h1>
            <p className="mt-2 text-[13px] text-ink-3">{t('site.legal.install.doneDesc')}</p>
            <div className="mt-6">
              <Button variant="primary" size="lg" onClick={() => router.push('/admin/login')}>
                {t('site.legal.install.doneButton')}
              </Button>
            </div>
          </div>
        </main>
      </div>
    )
  }

  return (
    <div className="flex min-h-screen flex-col bg-surface">
      <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
        <div className="flex items-center gap-2">
          <BrandLogo name={t('site.legal.install.wizardBrand')} />
        </div>
      </header>

      <main className="flex flex-1 items-start justify-center px-4 py-16">
        <div className="w-full max-w-sm">
          <h1 className="text-xl font-bold text-ink">{t('site.legal.install.title')}</h1>
          <p className="mt-1 text-[13px] text-ink-3">{t('site.legal.install.subtitle')}</p>

          <form onSubmit={handleSubmit} className="mt-6 space-y-4">
            <Field label={t('site.legal.install.siteNameLabel')} help={t('site.legal.install.siteNameHelp')}>
              <Input value={siteName} onChange={(e) => setSiteName(e.target.value)} placeholder={t('site.legal.install.siteNamePlaceholder')} />
            </Field>
            <Field label={t('site.legal.install.usernameLabel')}>
              <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="admin" autoComplete="username" />
            </Field>
            <Field label={t('site.legal.install.passwordLabel')} required help={t('site.legal.install.passwordHelp')}>
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={t('site.legal.install.passwordPlaceholder')} autoComplete="new-password" />
            </Field>
            <Field label={t('site.legal.install.confirmLabel')} required>
              <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={t('site.legal.install.confirmPlaceholder')} autoComplete="new-password" />
            </Field>

            {error && <div className="rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err">{error}</div>}

            <Button type="submit" variant="primary" size="lg" className="w-full" loading={loading}>
              {t('site.legal.install.submit')}
            </Button>
          </form>

          <div className="mt-4 text-center text-[13px]">
            {t('site.legal.install.haveAccount')}<Link href="/login" className="text-brand hover:underline">{t('site.legal.install.loginLink')}</Link>
          </div>
        </div>
      </main>
    </div>
  )
}
