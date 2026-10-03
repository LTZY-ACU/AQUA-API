/** 隐私政策页（/privacy）。
 *
 * 意图（Why）：
 *   说明数据收集范围、用途与用户权利（个人信息保护义务）。
 *
 * 流转（Flow）：
 *   LegalDocLayout + 政策段落；文案一律走 t('site.legal.privacy.*')，随语言切换实时更新。
 */
'use client'

import { LegalDocLayout } from '@/components/site/LegalDocLayout'
import { useI18n } from '@/i18n'

export default function PrivacyPage() {
  const { t } = useI18n()
  return (
    <LegalDocLayout title={t('site.legal.privacy.title')} updatedAt={t('site.legal.privacy.updatedAt')}>
      <h2>{t('site.legal.privacy.s1Title')}</h2>
      <p>{t('site.legal.privacy.s1Body')}</p>
      <h2>{t('site.legal.privacy.s2Title')}</h2>
      <p>{t('site.legal.privacy.s2Body')}</p>
      <h2>{t('site.legal.privacy.s3Title')}</h2>
      <p>{t('site.legal.privacy.s3Body')}</p>
      <h2>{t('site.legal.privacy.s4Title')}</h2>
      <p>{t('site.legal.privacy.s4Body')}</p>
      <h2>{t('site.legal.privacy.s5Title')}</h2>
      <p>{t('site.legal.privacy.s5Body')}</p>
      <h2>{t('site.legal.privacy.s6Title')}</h2>
      <p>{t('site.legal.privacy.s6Body')}</p>
    </LegalDocLayout>
  )
}
