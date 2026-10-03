/** 安全致谢页（/security）。
 *
 * 意图（Why）：
 *   公示漏洞披露渠道与致谢名单（负责任的披露文化）。
 *
 * 流转（Flow）：
 *   LegalDocLayout + 段落与致谢按钮；文案一律走 t('site.legal.security.*')，
 *   致谢名单默认折叠，收到有效报告后由「查看已致谢研究者」展开。
 */
'use client'

import { useState } from 'react'

import { LegalDocLayout } from '@/components/site/LegalDocLayout'
import { useI18n } from '@/i18n'

export default function SecurityCreditsPage() {
  const [revealed, setRevealed] = useState(false)
  const { t } = useI18n()
  return (
    <LegalDocLayout title={t('site.legal.security.title')} updatedAt={t('site.legal.security.updatedAt')}>
      <h2>{t('site.legal.security.s1Title')}</h2>
      <p>{t('site.legal.security.s1Body')}</p>
      <h2>{t('site.legal.security.s2Title')}</h2>
      <p>{t('site.legal.security.s2Body')}</p>
      <h2>{t('site.legal.security.s3Title')}</h2>
      <p>{t('site.legal.security.s3Body')}</p>
      <button type="button" onClick={() => setRevealed(true)} className="text-brand hover:underline" disabled={revealed}>
        {revealed ? t('site.legal.security.thanksRevealed') : t('site.legal.security.thanksShow')}
      </button>
    </LegalDocLayout>
  )
}
