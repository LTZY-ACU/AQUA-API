/**
 * 敏感操作的二次验证守卫（React 侧）。
 *
 * 意图（Why）：
 *   后端对"人工确认入账 / 全站群发 / 发放试用额"这类不可撤回的操作加了二次验证闸门，
 *   未验证时返回 403 + reauth_required。若前端只是把这句中文弹出来，
 *   管理员会看到一个"无法继续"的死胡同——因为它必须去某个地方输入密码再重试。
 *   本组件把"被拦 → 弹窗要密码 → 验证 → 自动重试原操作"整条链路收敛到一处：
 *     1) 调用方只写一行 await guard(() => doSomething())，不被闸门细节污染；
 *     2) 重试逻辑只有一份，不会出现"某个页面重试了、另一个页面忘了"；
 *     3) 密码只在弹窗内流转，不会经过调用方，减少它出现在日志里的机会。
 *
 * 流转（Flow）：
 *   guard(fn) → 403/reauth_required → askPassword()（弹窗）
 *     → POST /api/auth/reauth → 重新执行 fn
 *
 * 扩展（Extend）：
 *   将来接入"短信/邮箱验证码二次验证"时，只改弹窗内部与 reauth 的调用，
 *   guard 的形状与调用方代码都不用动。
 */
'use client'

import { useCallback, useRef, useState } from 'react'

import { reauth } from '@/api/auth'
import { ApiError } from '@/api/client'
import { Button } from '@/components/ui/Button'
import { Modal } from '@/components/ui/Modal'

export function useReauthGuard() {
  const [open, setOpen] = useState(false)
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  // 弹窗是异步的：用 ref 挂住 promise 的 resolve，等用户点确认或取消再落地。
  const resolverRef = useRef<((value: string | null) => void) | null>(null)

  const askPassword = useCallback((): Promise<string | null> => {
    setPassword('')
    setError('')
    setOpen(true)
    return new Promise<string | null>((resolve) => {
      resolverRef.current = resolve
    })
  }, [])

  function closeWith(value: string | null) {
    setOpen(false)
    resolverRef.current?.(value)
    resolverRef.current = null
  }

  async function handleSubmit() {
    if (!password) {
      setError('请输入登录密码')
      return
    }
    setSubmitting(true)
    try {
      await reauth(password)
      closeWith(password)
    } catch (err) {
      setError(err instanceof Error ? err.message : '验证失败')
    } finally {
      setSubmitting(false)
    }
  }

  /**
   * 执行一个可能被二次验证闸门拦下的操作。
   *
   * 只对 403 + reauth_required 做重试：其它错误（权限不足、参数不对、网络错误）
   * 一律原样抛出——盲目重试只会把真正的故障掩盖成"点了两下还是不行"。
   */
  const guard = useCallback(
    async function run<T>(fn: () => Promise<T>): Promise<T> {
      try {
        return await fn()
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 403 || err.code !== 'reauth_required') {
          throw err
        }
        const pw = await askPassword()
        if (!pw) {
          throw new Error('已取消：该操作需要重新验证密码后才能继续')
        }
        // 验证已经成功（handleSubmit 里完成），直接重试原操作即可
        return await fn()
      }
    },
    [askPassword],
  )

  const dialog = (
    <Modal
      open={open}
      onClose={() => closeWith(null)}
      title="需要重新验证密码"
      width={420}
      footer={
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => closeWith(null)}>
            取消
          </Button>
          <Button type="button" variant="primary" loading={submitting} onClick={handleSubmit}>
            验证并继续
          </Button>
        </div>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">
        该操作涉及资产或不可撤回，请重新输入您的登录密码。验证后 15 分钟内无需再次输入。
      </p>
      <div className="mt-4">
        <input
          type="password"
          value={password}
          autoComplete="current-password"
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void handleSubmit()
          }}
          placeholder="登录密码"
          className="w-full rounded-md border border-line bg-surface px-3 py-2 text-[13px] text-ink outline-none focus:border-brand"
        />
      </div>
      {error && <p className="mt-2 text-[13px] text-err">{error}</p>}
    </Modal>
  )

  return { guard, dialog }
}
