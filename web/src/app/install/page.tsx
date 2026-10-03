/** OOBE 安装向导：把第一次使用拆成可跳步的分步流程。
 *
 * 意图（Why）：
 *   单页表单要求小白一次性理解"站点名 + 用户名 + 密码 + 确认密码"四件事，
 *   任何一项填错都要整页重来。分步向导的价值不在于"看起来更专业"，
 *   而在于把一次需要理解四件事的任务，变成四次只需要理解一件事的任务。
 *
 * 设计取舍（这些决定了它对小白是否真的友好）：
 *
 *  1) 步骤清单由【后端下发】（status.steps），前端不自己推断"还差哪一步"。
 *     两处各判一次必然不同步，用户会撞上"前端显示已完成、后端拒绝"的错位。
 *     前端只负责按 key 渲染与导航，判定规则单点维护在后端。
 *
 *  2) 管理员是唯一的硬门槛，其余步骤一律可跳过。
 *     渠道、公告、邮件都能在装完站点后从容补配；把它们做成必填只会让小白
 *     卡在"我到底该不该开"上，而站���此时其实已经可用了。
 *
 *  3) 每步都有中文标题与一句"为什么"。小白卡住往往不是不会填，
 *     而是不知道这一格填了会怎样。
 *
 *  4) 站点名那一步只写设置、不建账号，因此可以反复进出；
 *     建账号那一步是不可逆的一次性动作，放在最后作为"确认"，
 *     让用户在按下按钮前清楚知道这一步做完站点就归他管了。
 *
 * 流转（Flow）：
 *   mount → fetchInstallStatus()
 *        → installed=false → 渲染步骤条 + 当前步表单
 *        → 填完当前步 → 「下一步」/「跳过」（可跳过时）
 *        → 管理员步提交 submitInstall() → 成功后跳 result.admin_login_path
 *        → installed=true  → 转为「已完成安装」引导态
 *
 * 扩展（Extend）：
 *   加新步骤时【只改后端】buildInstallSteps（新增 key + 状态判定），
 *   前端在本文件补一个 case 即可；不要在前端硬编码"第几步该显示什么"。
 */
'use client'

import Link from 'next/link'
import { useRouter } from 'next/navigation'
import { useCallback, useEffect, useMemo, useState } from 'react'

import {
  fetchInstallStatus,
  submitInstall,
  type InstallStatus,
  type InstallStep,
} from '@/api/install'
import { BrandLogo } from '@/components/BrandMark'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Form'

/** 与后端 installStep* 常量一一对应（见 internal/server/handler_install.go）。 */
type StepKey = 'site' | 'admin' | 'access' | 'channel' | 'announce'

/** 站点名低于此长度时提示站长，长名字在浏览器标签里会被截断。 */
const MIN_SITE_NAME = 2

/** 密码强度最低位数（与后端 crypto.ValidatePasswordStrength 对齐）。 */
const MIN_PASSWORD = 8

export default function InstallPage() {
  const router = useRouter()

  const [status, setStatus] = useState<InstallStatus | null>(null)
  /** 状态查询失败时置为 true：宁可拦住用户，也不让人误以为还能安装。 */
  const [loadFailed, setLoadFailed] = useState(false)
  const [current, setCurrent] = useState(0)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  // 各步的表单值放在一起：切换步骤不该丢已填内容
  // （小白来回切一步就发现要重填，是最劝退的一类体验）。
  const [siteName, setSiteName] = useState('')
  const [siteDesc, setSiteDesc] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')

  const loadStatus = useCallback(async () => {
    try {
      const data: InstallStatus = await fetchInstallStatus()
      setStatus(data)
      setSiteName((prev) => prev || data.site_name || '')
      setUsername((prev) => prev || data.default_admin_username || 'admin')
    } catch {
      setLoadFailed(true)
    }
  }, [])

  useEffect(() => {
    void loadStatus()
  }, [loadStatus])

  const steps: InstallStep[] = useMemo(() => status?.steps ?? [], [status])

  const step = steps[current]
  const isLast = current === steps.length - 1
  /** 管理员那步是唯一不可跳过的：站点没有主人就没法继续。 */
  const required = step ? !step.optional : false

  function goNext() {
    setError('')
    setCurrent((i) => Math.min(i + 1, steps.length - 1))
  }

  function goBack() {
    setError('')
    setCurrent((i) => Math.max(i - 1, 0))
  }

  /** 站点那一步只做本地校验：填完直接放行，站点名在建号时一并写入。 */
  function validateSite(): boolean {
    const name = siteName.trim()
    if (name.length < MIN_SITE_NAME) {
      setError(`站点名称至少 ${MIN_SITE_NAME} 个字，不然用户根本认不出这是哪个站`)
      return false
    }
    return true
  }

  function validateAdmin(): string | null {
    if (password.length < MIN_PASSWORD) {
      return `密码至少 ${MIN_PASSWORD} 位`
    }
    if (password !== confirm) {
      return '两次输入的密码不一致'
    }
    return null
  }

  async function handleFinish(e: React.FormEvent) {
    e.preventDefault()
    const invalid = validateAdmin()
    if (invalid) {
      setError(invalid)
      return
    }
    setLoading(true)
    setError('')
    try {
      const result = await submitInstall({
        username: username.trim() || 'admin',
        password,
        confirm_password: confirm,
        site_name: siteName.trim() || undefined,
        site_description: siteDesc.trim() || undefined,
      })
      // 替换而非 push：装完再按浏览器返回键，不该回到已提交的表单上重填。
      router.replace(result.admin_login_path || '/admin/login')
    } catch (err) {
      setError(err instanceof Error ? err.message : '安装失败，请重试')
      setLoading(false)
    }
  }

  // ── 状态分支 ────────────────────────────────────────────────

  if (loadFailed) {
    return (
      <CenteredCard>
        <h1 className="text-xl font-bold text-ink">连不上服务</h1>
        <p className="mt-2 text-[13px] leading-relaxed text-ink-3">
          读取安装状态失败。通常是后端还没启动完，或数据库不可访问。
          <br />
          请确认服务在运行后刷新本页；仍然失败就查看服务端日志。
        </p>
        <div className="mt-6">
          <Button variant="primary" onClick={() => window.location.reload()}>
            重新检查
          </Button>
        </div>
      </CenteredCard>
    )
  }

  if (!status) {
    return (
      <CenteredCard>
        <p className="text-[13px] text-ink-3">正在检查安装状态…</p>
      </CenteredCard>
    )
  }

  if (status.installed) {
    return (
      <CenteredCard>
        <h1 className="text-xl font-bold text-ink">站点已就绪</h1>
        <p className="mt-2 text-[13px] leading-relaxed text-ink-3">
          {status.site_name || '本站'} 已经装好了，管理员账号已存在。
          <br />
          装完之后这个入口会永久关闭——它只在站点还没有主人时开放，
          否则任何人都能重装并接管你的站点。
        </p>
        <div className="mt-6 flex flex-col gap-2">
          <Button variant="primary" size="lg" onClick={() => router.push('/admin/login')}>
            前往后台登录
          </Button>
          <Link href="/" className="text-center text-[13px] text-ink-3 hover:text-ink-2">
            返回首页
          </Link>
        </div>
      </CenteredCard>
    )
  }

  if (steps.length === 0 || !step) {
    // 后端没给步骤清单（版本不匹配的极端情况）：宁可说清楚，也不要渲染一个空表单。
    return (
      <CenteredCard>
        <h1 className="text-xl font-bold text-ink">向导数据缺失</h1>
        <p className="mt-2 text-[13px] leading-relaxed text-ink-3">
          服务端没有返回安装步骤，可能是前后端版本不一致。
          <br />
          请确认网关与前端来自同一次构建。
        </p>
      </CenteredCard>
    )
  }

  const stepKey = step.key as StepKey

  return (
    <div className="flex min-h-screen flex-col bg-surface">
      <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
        <BrandLogo name="安装向导" />
      </header>

      <main className="mx-auto flex w-full max-w-2xl flex-1 flex-col px-4 py-8 sm:py-12">
        {/* 进度条：已完成 / 当前 / 未开始三态。
            可跳过步骤也占一格——跳过了也要让人看见"这里可以回来补"，
            直接从进度条里消失会让人怀疑它是否存在。 */}
        <ol className="flex flex-wrap gap-1.5" aria-label="安装进度">
          {steps.map((s, i) => {
            const state = i < current ? 'done' : i === current ? 'active' : 'todo'
            return (
              <li key={s.key} className="flex-1 min-w-[64px]">
                <button
                  type="button"
                  onClick={() => {
                    setError('')
                    setCurrent(i)
                  }}
                  aria-current={state === 'active' ? 'step' : undefined}
                  className={[
                    'w-full rounded-md border px-2 py-1.5 text-left text-[12px] transition-colors',
                    state === 'active'
                      ? 'border-brand bg-brand/8 text-ink'
                      : 'border-line bg-card text-ink-3 hover:border-line-2 hover:text-ink-2',
                  ].join(' ')}
                >
                  <span className="flex items-center gap-1">
                    {state === 'done' && <span className="text-brand">✓</span>}
                    <span className="truncate">{s.title}</span>
                  </span>
                </button>
              </li>
            )
          })}
        </ol>

        <div className="mt-6 flex-1 rounded-lg border border-line bg-card p-5 sm:p-6">
          <div className="flex items-baseline gap-2">
            <h1 className="text-lg font-bold text-ink">{step.title}</h1>
            {step.optional && (
              <span className="rounded border border-line-2 px-1.5 py-0.5 text-[11px] text-ink-3">
                可跳过
              </span>
            )}
          </div>
          <p className="mt-1 text-[13px] leading-relaxed text-ink-3">{step.description}</p>

          <div className="mt-5">
            {stepKey === 'site' && (
              <div className="space-y-4">
                <Field label="站点名称" required help="显示在浏览器标题、登录页与页脚">
                  <Input
                    value={siteName}
                    onChange={(e) => setSiteName(e.target.value)}
                    placeholder="例如：老王的 AI 站"
                    autoComplete="organization"
                  />
                </Field>
                <Field label="站点描述" help="一句话说明这个站是做什么的，会展示在首页与页脚">
                  <Input
                    value={siteDesc}
                    onChange={(e) => setSiteDesc(e.target.value)}
                    placeholder="例如：面向开发者的 AI 模型接口网关"
                  />
                </Field>
              </div>
            )}

            {stepKey === 'admin' && (
              <form onSubmit={handleFinish} className="space-y-4">
                <Field label="管理员用户名" help="登录后台用的账号名，忘记它只能上服务器重置">
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder="admin"
                    autoComplete="username"
                  />
                </Field>
                <Field label="管理员密码" required help={`至少 ${MIN_PASSWORD} 位`}>
                  <Input
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="new-password"
                  />
                </Field>
                <Field label="再输一次密码" required>
                  <Input
                    type="password"
                    value={confirm}
                    onChange={(e) => setConfirm(e.target.value)}
                    autoComplete="new-password"
                  />
                </Field>
                <p className="rounded-md border border-line bg-surface px-3 py-2 text-[12px] leading-relaxed text-ink-3">
                  下一步会创建这个管理员账号。创建完成后本站就归你管理，
                  站点名与描述会同时保存。
                </p>
              </form>
            )}

            {stepKey === 'access' && (
              <div className="space-y-3 text-[13px] leading-relaxed text-ink-2">
                <p>当前已经是一套能直接用的默认策略：</p>
                <ul className="ml-4 list-disc space-y-1.5 text-ink-3">
                  <li>允许注册，但注册需要邮箱验证码（防止批量注册）</li>
                  <li>新用户额度不限，站长承担上游成本</li>
                </ul>
                <p className="text-ink-3">
                  想改成"只给自己用"或"限制新用户额度"，
                  装完后到后台「站点设置」里改即可，不用在这里做选择。
                </p>
              </div>
            )}

            {stepKey === 'channel' && (
              <div className="space-y-3 text-[13px] leading-relaxed text-ink-2">
                <p>渠道是你自己的 AI 服务商（OpenAI、Claude 等），网关靠它把请求转发出去。</p>
                <p className="text-ink-3">
                  现在可以先跳过——站点已经装好、能登录后台了。
                  等你拿到密钥后到后台「渠道管理」里添加也完全来得及。
                </p>
              </div>
            )}

            {stepKey === 'announce' && (
              <div className="space-y-3 text-[13px] leading-relaxed text-ink-2">
                <p>可以发一条上线公告，并配置发信邮箱（用于邮箱验证码与通知）。</p>
                <p className="text-ink-3">
                  同样可以先跳过。没配邮箱时注册验证码发不出去，
                  但你可以先把渠道配好开始用。
                </p>
              </div>
            )}
          </div>

          {error && (
            <div
              role="alert"
              className="mt-4 rounded-md border border-err/25 bg-err/8 px-3 py-2 text-[13px] text-err"
            >
              {error}
            </div>
          )}
        </div>

        <nav className="mt-5 flex items-center gap-2">
          {current > 0 && (
            <Button variant="secondary" onClick={goBack}>
              上一步
            </Button>
          )}

          {stepKey === 'admin' ? (
            <Button
              variant="primary"
              size="lg"
              className="ml-auto"
              loading={loading}
              onClick={handleFinish}
            >
              完成安装
            </Button>
          ) : isLast ? (
            <Button variant="primary" className="ml-auto" onClick={goNext}>
              下一步
            </Button>
          ) : (
            <Button
              variant="primary"
              className="ml-auto"
              onClick={() => {
                if (stepKey === 'site' && !validateSite()) return
                goNext()
              }}
            >
              下一步
            </Button>
          )}

          {/* 可跳过且还没到最后一步时才给"跳过"：
              最后一页给"跳过"没有意义（后面没有内容了）。 */}
          {!required && !isLast && stepKey !== 'admin' && (
            <Button variant="ghost" onClick={goNext}>
              跳过
            </Button>
          )}
        </nav>

        <p className="mt-6 text-center text-[13px] text-ink-3">
          已经有账号了？<Link href="/login" className="text-brand hover:underline">去登录</Link>
        </p>
      </main>
    </div>
  )
}

/** CenteredCard 是加载态、失败态与"已安装"态共用的居中卡片。
 *
 *  这三种状态都不是用户主动进入的页面，用整页表单的样式渲染它们会让
 *  用户以为还有东西要填。
 */
function CenteredCard({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-surface">
      <header className="flex h-14 items-center border-b border-line bg-card px-4 sm:px-6">
        <BrandLogo name="安装向导" />
      </header>
      <main className="flex flex-1 items-center justify-center px-4">
        <div className="w-full max-w-sm rounded-lg border border-line bg-card p-6">
          {children}
        </div>
      </main>
    </div>
  )
}
