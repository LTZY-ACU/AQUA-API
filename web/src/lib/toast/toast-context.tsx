/** Toast 通知系统（React Context 版，等价旧 composables/useToast + ToastHost.vue）。
 *
 * 意图（Why）：
 *   轻量级全局提示：成功/失败/信息三类，自动消失、可点击关闭。
 *   不引入第三方库（antd 等），保持单二进制前端体积可控。
 */
'use client'

import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'

import { useI18n } from '@/i18n'

type ToastKind = 'success' | 'error' | 'info'

interface ToastItem {
  id: number
  kind: ToastKind
  message: string
}

interface ToastContextValue {
  toast: (message: string, kind?: ToastKind) => void
  toastSuccess: (message: string) => void
  toastError: (message: string) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

/** 去抖合并：同一消息在 800ms 内重复出现时不追加新 toast（避免表单连点刷屏） */
const DEDUP_MS = 800
const AUTO_DISMISS_MS = 3500

export function ToastProvider({ children }: { children: ReactNode }) {
  // 关闭按钮的无障碍标签：此前只有一个「×」符号，读屏软件只能念出「乘号」，
  // 用户完全不知道按下会发生什么。图标按钮必须带 aria-label，这不是可选项。
  const { t } = useI18n()
  const [items, setItems] = useState<ToastItem[]>([])
  const seq = useRef(0)
  const lastKey = useRef<string>('')
  const lastAt = useRef(0)

  const dismiss = useCallback((id: number) => {
    setItems((prev) => prev.filter((item) => item.id !== id))
  }, [])

  const toast = useCallback((message: string, kind: ToastKind = 'info') => {
    const now = Date.now()
    if (message === lastKey.current && now - lastAt.current < DEDUP_MS) return
    lastKey.current = message
    lastAt.current = now

    const id = ++seq.current
    setItems((prev) => [...prev.slice(-3), { id, kind, message }])
    // 自动消失；用 setTimeout 而非依赖组件卸载，简化生命周期
    setTimeout(() => dismiss(id), AUTO_DISMISS_MS)
  }, [dismiss])

  const value = useMemo(
    () => ({
      toast,
      toastSuccess: (message: string) => toast(message, 'success'),
      toastError: (message: string) => toast(message, 'error'),
    }),
    [toast],
  )

  return (
    <ToastContext.Provider value={value}>
      {children}
      {/* Toast 容器：右上角堆叠，不遮挡主要内容 */}
      <div aria-live="polite" className="fixed right-4 top-4 z-50 flex w-[min(92vw,360px)] flex-col gap-2">
        {items.map((item) => (
          <button
            key={item.id}
            type="button"
            onClick={() => dismiss(item.id)}
            aria-label={t('components.toast.dismiss')}
            className={`flex items-start gap-2 rounded-md border px-3 py-2.5 text-left text-sm shadow-pop backdrop-blur transition ${
              item.kind === 'success'
                ? 'border-ok/30 bg-card text-ink'
                : item.kind === 'error'
                  ? 'border-err/30 bg-card text-ink'
                  : 'border-line-2 bg-card text-ink'
            }`}
          >
            <span
              className={`mt-0.5 h-2 w-2 shrink-0 rounded-full ${
                item.kind === 'success' ? 'bg-ok' : item.kind === 'error' ? 'bg-err' : 'bg-brand'
              }`}
            />
            <span className="flex-1 break-words">{item.message}</span>
            <span className="text-ink-3">×</span>
          </button>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast 必须在 ToastProvider 内使用')
  return ctx
}