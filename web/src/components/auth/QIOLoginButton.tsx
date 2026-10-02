/**
 * 「使用 QIU 科技账号登录」按钮。
 *
 * 意图（Why）：
 *   第三方登录的交互与普通登录完全不同——它是"开一个新窗口 → 等用户在对方页面点确认
 *   → 回来领结果"，而不是"填表单 → 提交"。把它单独封成组件有三个理由：
 *     1) 这段逻辑（轮询、超时、清理定时器）长约百行，塞进登录页会把表单淹没；
 *     2) 将来注册页也要放同一个按钮，复制粘贴必然导致两边行为不一致；
 *     3) 轮询一旦写错（忘了清定时器、忘了判终态）就会变成"用户关了页面还在请求"，
 *        集中在一处才好钉住这些坑。
 *
 * 流程（Flow）：
 *   onClick → startQIULogin() → window.open(url) → 每 2 秒 qiuLoginStatus(taskId)
 *     → pending：继续等（到一个最长容忍时长）
 *     → ok：onSuccess(session) —— 由父级写入登录态
 *     → denied / expired：给出明确文案（这两者用户能自行处置，不必报障）
 *
 * 扩展（Extend）：
 *   接入第二个第三方平台时，把 props 里的三个函数换成"接口注入"即可复用本组件的轮询逻辑。
 */
'use client'

import { useCallback, useEffect, useRef, useState } from 'react'

import { qiuLoginStatus, startQIULogin } from '@/api/auth'
import { Button } from '@/components/ui/Button'
import { useToast } from '@/lib/toast/toast-context'
import type { AuthResult } from '@/api/types'

/** 轮询间隔（毫秒）：与后端限流配额、第三方接口的负载共同决定，2 秒是平衡点。 */
const POLL_INTERVAL_MS = 2000

/** 最长等待时间（毫秒）：超过即放弃轮询，避免用户忘了这件事后一直在后台请求。 */
const POLL_TIMEOUT_MS = 5 * 60 * 1000

export function QIOLoginButton({
  onSuccess,
  disabled,
}: {
  /** 拿到的是完整会话结果：调用方负责写入登录态（组件不直接依赖全局 store） */
  onSuccess: (result: AuthResult) => void
  disabled?: boolean
}) {
  const { toast, toastError } = useToast()
  const [waiting, setWaiting] = useState(false)
  // 组件卸载后不得再改状态：轮询是异步的，用户完全可能在等待期间切走页面。
  const aliveRef = useRef(true)
  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  const poll = useCallback(
    async (taskId: string): Promise<AuthResult | null> => {
      const deadline = Date.now() + POLL_TIMEOUT_MS
      while (Date.now() < deadline) {
        const result = await qiuLoginStatus(taskId)
        if (!aliveRef.current) return null
        if (result.status === 'ok') {
          // 严格按 status 判断：只看"有没有 user"会把 pending 误当成登录成功。
          return {
            session_token: result.session_token,
            expires_at: result.expires_at,
            user: result.user,
          }
        }
        if (result.status === 'denied') {
          toast('你取消了这次授权，可以重新发起登录')
          return null
        }
        if (result.status === 'expired') {
          toast('登录请求已超时，请重新发起')
          return null
        }
        await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
        if (!aliveRef.current) return null
      }
      toast('等待确认超时，请重新发起登录')
      return null
    },
    [toast],
  )

  async function handleClick() {
    if (waiting) return
    setWaiting(true)
    try {
      const task = await startQIULogin()
      if (!task.task_id || !task.url) {
        toastError('第三方登录服务返回了不完整的数据')
        return
      }
      // 确认页必须开给"人"去点：由本站代跳会让用户失去
      // "我在给谁授权"的判断依据（地址栏里看不到对方的域名）。
      const win = window.open(task.url, '_blank', 'noopener,noreferrer')
      if (!win) {
        toastError('浏览器拦截了弹出窗口，请允许本站弹窗后重试')
        return
      }
      toast('已打开确认页，请在里面点击「确认登录」')
      const result = await poll(task.task_id)
      if (result && aliveRef.current) {
        onSuccess(result)
      }
    } catch (err) {
      toastError(err instanceof Error ? err.message : '第三方登录失败')
    } finally {
      if (aliveRef.current) setWaiting(false)
    }
  }

  return (
    <Button
      type="button"
      variant="secondary"
      className="w-full"
      size="lg"
      disabled={disabled}
      loading={waiting}
      onClick={handleClick}
    >
      {waiting ? '等待确认中…' : '使用 QIU 科技账号登录'}
    </Button>
  )
}
