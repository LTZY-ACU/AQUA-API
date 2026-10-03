/** 控制台首次使用确认弹窗（ComplianceGate）。
 *
 * 意图（Why）：
 *   合规提示不能只写进协议页——用户可能在未读协议的情况下直接进入控制台。
 *   本组件在首次进入 /console/* 时弹一次「使用须知」，用户点「我已知悉」后写入
 *   localStorage，此后不再打扰；既完成一次性告知，又不影响日常使用。
 *
 * 流转（Flow）：
 *   挂载于控制台 → usePathname 判断当前是否为 /console/*
 *   → 读取 localStorage[COMPLIANCE_ACK_KEY]，未确认则显示 Modal
 *   → 点「我已知悉」（或关闭弹窗）→ 写入标记 → 本次及以后不再弹出
 *
 * 扩展（Extend）：
 *   需要对全体用户重新告知时，改 @/lib/site/compliance 的 COMPLIANCE_ACK_KEY
 *   键名即可（所有客户端视为未确认）。
 *   本组件自包含（自行判断路由与已确认状态），无需在每个页面单独接线。
 */
'use client'

import { useEffect, useState } from 'react'
import Link from 'next/link'
import { usePathname } from 'next/navigation'

import { Button } from '@/components/ui/Button'
import { Modal } from '@/components/ui/Modal'
import {
  COMPLIANCE_ACK_KEY,
  COMPLIANCE_GATE_MESSAGE,
  COMPLIANCE_GATE_TITLE,
} from '@/lib/site/compliance'

export function ComplianceGate() {
  const pathname = usePathname()
  const [open, setOpen] = useState(false)

  // 仅在控制台路由下检查；localStorage 不可用（隐私模式等）时静默跳过：
  // 宁可少弹一次，也不阻断用户正常使用。
  useEffect(() => {
    if (!pathname.startsWith('/console')) return
    try {
      if (window.localStorage.getItem(COMPLIANCE_ACK_KEY) === '1') return
    } catch {
      return
    }
    setOpen(true)
  }, [pathname])

  function acknowledge() {
    try {
      window.localStorage.setItem(COMPLIANCE_ACK_KEY, '1')
    } catch {
      /* 写入失败不阻断：本次关闭即可，下次仍会提示 */
    }
    setOpen(false)
  }

  if (!open) return null

  return (
    <Modal
      open={open}
      onClose={acknowledge}
      title={COMPLIANCE_GATE_TITLE}
      width={480}
      footer={
        <>
          <Link href="/terms" className="mr-auto self-center text-[13px] text-brand hover:underline">
            查看用户协议
          </Link>
          <Button variant="primary" onClick={acknowledge}>
            我已知悉
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-relaxed text-ink-2">{COMPLIANCE_GATE_MESSAGE}</p>
    </Modal>
  )
}
