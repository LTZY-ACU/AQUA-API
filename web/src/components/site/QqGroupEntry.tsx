/** QQ 群入口：统一的"加入开源社区"按钮/卡片。
 *
 * 意图（Why）：
 *   用户遇到问题最快的反馈渠道是群聊，而不是翻文档或发邮件。
 *   把入群入口做成独立组件，保证它在页脚、首页 CTA 等多处是同一套视觉与文案，
 *   不会各写各的导致文案漂移。
 *
 * 流转（Flow）：
 *   SiteFooter / 首页 Cta → <QqGroupEntry /> → 跳转到群链接
 *
 * 扩展（Extend）：
 *   换群只需改下方两个常量；视觉变体用 variant 控制（inline 紧凑 / card 卡片）。
 */

import { AppIcon } from '@/components/AppIcon'
import { Button } from '@/components/ui/Button'

/** 群链接与群号：换群只改这两个常量 */
const QQ_GROUP_URL = 'https://qm.qq.com/q/hHfKssRkdi'
const QQ_GROUP_NO = '1103667832'

interface QqGroupEntryProps {
  /** inline = 紧凑按钮（页脚/头部）；button = 与大按钮同高（Hero 按钮排）；card = 带说明的卡片（首页 CTA 旁） */
  variant?: 'inline' | 'button' | 'card'
  className?: string
}

export function QqGroupEntry({ variant = 'inline', className }: QqGroupEntryProps) {
  const link = (
    <a
      href={QQ_GROUP_URL}
      target="_blank"
      rel="noreferrer"
      aria-label={`加入 QQ 群「AQUA开源社区」，群号 ${QQ_GROUP_NO}`}
      className="inline-flex items-center gap-2 rounded-md border border-line-2 bg-card px-3 py-1.5 text-[13px] text-ink-2 transition hover:border-brand hover:text-brand"
    >
      <AppIcon name="qq" size={15} />
      <span>加入群聊</span>
      <span className="font-mono text-[12px] text-ink-3">群号 {QQ_GROUP_NO}</span>
    </a>
  )

  if (variant === 'inline') {
    return <span className={className}>{link}</span>
  }

  if (variant === 'button') {
    // 与 Hero/CTA 区的大按钮同一高度（size=lg），混排时视觉对齐
    return (
      <a href={QQ_GROUP_URL} target="_blank" rel="noreferrer" className={className}>
        <Button variant="secondary" size="lg">
          <AppIcon name="qq" size={16} />
          加入群聊 · 群号 {QQ_GROUP_NO}
        </Button>
      </a>
    )
  }

  return (
    <div className={`rounded-lg border border-line bg-card p-4 ${className ?? ''}`}>
      <div className="flex items-center gap-2">
        <span className="flex h-8 w-8 items-center justify-center rounded border border-line-2 bg-surface text-brand">
          <AppIcon name="qq" size={16} />
        </span>
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-ink">AQUA 开源社区</div>
          <div className="mt-0.5 text-[12px] text-ink-3">有问题进群聊，一起反馈、吹牛、出主意</div>
        </div>
      </div>
      <div className="mt-3">
        <a href={QQ_GROUP_URL} target="_blank" rel="noreferrer" className="block">
          <Button variant="secondary" size="md" className="w-full">
            <AppIcon name="qq" size={15} />
            加入群聊 · 群号 {QQ_GROUP_NO}
          </Button>
        </a>
      </div>
    </div>
  )
}
