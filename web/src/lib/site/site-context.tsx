/** 站点信息 Provider：名称/描述/模型列表/合规信息（React Context 版，等价旧 Pinia site store）。
 *
 * 意图（Why）：
 *   落地页、登录页、页脚等多处需要站点公开信息；60s 缓存避免路由切换重复请求。
 *   登录后的门户/后台还需要「当前用户」信息，但那是 AuthProvider 的职责，本 Provider 只管站点公开数据。
 */
'use client'

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

import { fetchSiteStatus, peekSiteName } from '@/api/site'
import type { SiteStatus } from '@/api/types'

interface SiteContextValue {
  /** 站点信息；null = 尚未加载成功 */
  status: SiteStatus | null
  /** 是否已尝试过加载（失败后仍为 true，供页面显示重试条） */
  loaded: boolean
  /** 站点名（未加载时回退默认值，不触发请求） */
  siteName: string
  /**
   * 额度 → 人民币的折算基数（1 元 = 多少额度）。
   *
   * 本站内部用整数「额度」记账（避免浮点误差），但对外一律以人民币示人，
   * 因此各页面用 formatYuanFromQuota(quota, quotaPerYuan) 做展示边界上的换算。
   * 0 表示未取到比例——此时格式化函数会退回显示原始额度，绝不假设比例。
   */
  quotaPerYuan: number
  /** 强制刷新（落地页「重试」按钮使用） */
  refresh: () => Promise<void>
}

const SiteContext = createContext<SiteContextValue | null>(null)

export function SiteProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SiteStatus | null>(null)
  const [loaded, setLoaded] = useState(false)
  const inflight = useRef(false)

  const refresh = useCallback(async () => {
    if (inflight.current) return
    inflight.current = true
    try {
      const data = await fetchSiteStatus(true)
      setStatus(data)
    } catch {
      /* 失败时保留旧值；loaded 置 true 供页面渲染重试条 */
    } finally {
      setLoaded(true)
      inflight.current = false
    }
  }, [])

  // 启动加载一次（用非强制缓存：多次 Provider 挂载不重复请求）
  useEffect(() => {
    void (async () => {
      try {
        const data = await fetchSiteStatus(false)
        setStatus(data)
      } catch {
        /* 静默 */
      } finally {
        setLoaded(true)
      }
    })()
  }, [])

  const siteName = status?.name || peekSiteName() || 'LTZY-API'
  const quotaPerYuan = status?.quota_per_yuan ?? 0

  const value = useMemo(
    () => ({ status, loaded, siteName, quotaPerYuan, refresh }),
    [status, loaded, siteName, quotaPerYuan, refresh],
  )

  return <SiteContext.Provider value={value}>{children}</SiteContext.Provider>
}

export function useSite(): SiteContextValue {
  const ctx = useContext(SiteContext)
  if (!ctx) throw new Error('useSite 必须在 SiteProvider 内使用')
  return ctx
}