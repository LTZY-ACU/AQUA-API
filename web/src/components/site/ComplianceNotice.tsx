/** 全站合规提示组件（ComplianceNotice）。
 *
 * 意图（Why）：
 *   站长策略：可接入订阅账号类上游，但必须把「仅供学习研究参考、遵守上游条款与
 *   当地法律」的提示铺到全站各处。本组件把提示统一成三种强度，页面按位置选用，
 *   避免各页各写一份文案导致口径漂移。
 *
 * 流转（Flow）：
 *   页面（如 /console/recharge）→ 传入 variant / message / title
 *   → 渲染 inline（行内小字）/ banner（带边框醒目条）/ card（含标题卡片）
 *
 * 扩展（Extend）：
 *   新增形态：在 ComplianceNoticeVariant 与 renderVariant 分支同步添加；
 *   调整口径：优先修改 @/lib/site/compliance 的默认文案常量。
 */
'use client'

import { type ReactNode } from 'react'

import { AppIcon } from '@/components/AppIcon'
import { DEFAULT_COMPLIANCE_MESSAGE, DEFAULT_COMPLIANCE_TITLE } from '@/lib/site/compliance'

export type ComplianceNoticeVariant = 'inline' | 'banner' | 'card'

interface ComplianceNoticeProps {
  /** 展示形态：inline 行内小字 / banner 醒目条 / card 卡片（默认 banner） */
  variant?: ComplianceNoticeVariant
  /** 正文；不传则使用默认合规文案 */
  message?: ReactNode
  /** 标题（仅 card 使用）；不传则使用默认标题 */
  title?: ReactNode
  /** 附加内容（如「查看用户协议」链接），仅 card 使用 */
  extra?: ReactNode
  className?: string
}

export function ComplianceNotice({
  variant = 'banner',
  message,
  title,
  extra,
  className,
}: ComplianceNoticeProps) {
  const text = message ?? DEFAULT_COMPLIANCE_MESSAGE
  const root = className ?? ''

  if (variant === 'inline') {
    // 行内小字：不占版面，弱化为提示级文字，语义色仅落在图标上
    return (
      <div className={`flex items-start gap-1.5 text-[12px] leading-relaxed text-ink-3 ${root}`}>
        <AppIcon name="info" size={13} className="mt-[3px] shrink-0 text-warn" />
        <span>{text}</span>
      </div>
    )
  }

  if (variant === 'card') {
    return (
      <div className={`rounded-lg border border-warn/30 bg-warn/8 p-5 ${root}`}>
        <div className="flex items-center gap-2 text-sm font-semibold text-ink">
          <AppIcon name="shield" size={16} className="text-warn" />
          <span>{title ?? DEFAULT_COMPLIANCE_TITLE}</span>
        </div>
        <div className="mt-2 text-[13px] leading-relaxed text-ink-2">{text}</div>
        {extra && <div className="mt-3">{extra}</div>}
      </div>
    )
  }

  // banner：重要位置的醒目条。全部使用设计令牌（warn/ink/line 随主题切换），
  // 三套配色（浅色 / 深色 / 深蓝）下对比度均正常，禁止硬编码色值。
  return (
    <div
      className={`flex items-start gap-2.5 rounded-md border border-warn/30 bg-warn/8 px-3.5 py-2.5 ${root}`}
    >
      <AppIcon name="alert" size={15} className="mt-[2px] shrink-0 text-warn" />
      <div className="text-[13px] leading-relaxed text-ink-2">{text}</div>
    </div>
  )
}
