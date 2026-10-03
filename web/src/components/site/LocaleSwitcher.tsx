/** 语言切换器（React 版，等价旧 LocaleSwitcher.vue）。
 *
 * 意图（Why）：
 *   落地页顶栏与页脚提供语言切换；用「母语自称」展示（不随界面语言变化），
 *   用户在任何界面语言下都能认出自己的语言。
 */
'use client'

import { useRef, useState } from 'react'

import { AppIcon } from '@/components/AppIcon'
import { SUPPORTED_LOCALES, useI18n } from '@/i18n'

export function LocaleSwitcher({ compact = false }: { compact?: boolean }) {
  const { locale, t, setLocale } = useI18n()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  const current = SUPPORTED_LOCALES.find((item) => item.code === locale) ?? SUPPORTED_LOCALES[0]

  function select(code: string) {
    setLocale(code)
    setOpen(false)
  }

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1 rounded-md px-2 py-1.5 text-[13px] text-ink-2 transition hover:bg-ink/5 hover:text-ink"
        aria-label={t('common.language.switch')}
      >
        <AppIcon name="globe" size={15} />
        <span>{compact ? '' : current.name}</span>
        <AppIcon name="chevron-down" size={13} />
      </button>
      {open && (
        <div className="absolute right-0 z-30 mt-1 w-36 overflow-hidden rounded-md border border-line bg-card py-1 shadow-pop">
          {SUPPORTED_LOCALES.map((item) => (
            <button
              key={item.code}
              type="button"
              onClick={() => select(item.code)}
              className={`flex w-full items-center justify-between px-3 py-1.5 text-left text-[13px] transition hover:bg-surface ${
                item.code === locale ? 'text-brand' : 'text-ink-2'
              }`}
            >
              {item.name}
              {item.code === locale && <AppIcon name="check" size={14} />}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}