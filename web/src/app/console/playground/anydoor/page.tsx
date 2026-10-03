/** 用户门户：任意门（/console/playground/anydoor）—— 游乐场下的功能入口占位页。
 *
 * 意图（Why）：
 *   在「游乐场」导航下新增「任意门」入口，作为后续功能的承载页；
 *   本期仅落地路由与导航跳转，页面主体内容待定。
 *
 * 流转（Flow）：
 *   console/layout.tsx 的「游乐场」children → 本页；右侧栏暂留空，待后续填充。
 *
 * 扩展（Extend）：
 *   后续在右侧 Card 内补充任意门的功能模块；如需新增同级入口，
 *   在 layout.tsx 的「游乐场」children 中追加 ShellNavItem 即可。
 */
'use client'

import { Card } from '@/components/ui/Display'
import { useI18n } from '@/i18n'

export default function ConsoleAnyDoorPage() {
  const { t } = useI18n()
  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-ink">{t('portal.anydoor.title')}</h1>
        <p className="mt-0.5 text-[13px] text-ink-3">{t('portal.anydoor.subtitle')}</p>
      </div>

      <div className="grid gap-5 lg:grid-cols-[1fr_300px]">
        <Card className="flex flex-col">
          <div className="flex min-h-72 items-center justify-center text-[13px] text-ink-3">
            {t('portal.anydoor.comingSoon')}
          </div>
        </Card>

        {/* 右侧栏：本期暂不添加内容，预留位置待后续填充（CardProps.children 为必填，故显式传 null） */}
        <Card className="min-h-72">{null}</Card>
      </div>
    </div>
  )
}
