/** 用户协议页（/terms）。
 *
 * 意图（Why）：
 *   经营性服务必须公示的条款：账号、额度、服务可用性与责任边界。
 *
 * 流转（Flow）：
 *   LegalDocLayout + 条款段落；文案一律走 t('site.legal.terms.*')，随语言切换实时更新。
 */
'use client'

import { LegalDocLayout } from '@/components/site/LegalDocLayout'
import { useI18n } from '@/i18n'

export default function TermsPage() {
  const { t } = useI18n()
  return (
    <LegalDocLayout title={t('site.legal.terms.title')} updatedAt={t('site.legal.terms.updatedAt')}>
      <h2>{t('site.legal.terms.s1Title')}</h2>
      <p>{t('site.legal.terms.s1Body')}</p>
      <h2>{t('site.legal.terms.s2Title')}</h2>
      <p>{t('site.legal.terms.s2Body')}</p>
      <h2>{t('site.legal.terms.s3Title')}</h2>
      <p>{t('site.legal.terms.s3Body')}</p>
      <h2>{t('site.legal.terms.s4Title')}</h2>
      <p>{t('site.legal.terms.s4Body1')}</p>
      <p>{t('site.legal.terms.s4Body2')}</p>
      <p>{t('site.legal.terms.s4Body3')}</p>
      <p>{t('site.legal.terms.s4Body4')}</p>
      <h2>{t('site.legal.terms.s5Title')}</h2>
      <p>{t('site.legal.terms.s5Body')}</p>
      <h2>{t('site.legal.terms.s6Title')}</h2>
      <p>{t('site.legal.terms.s6Body')}</p>
      <h2>{t('site.legal.terms.s7Title')}</h2>
      <p>{t('site.legal.terms.s7Body')}</p>
      <h2>{t('site.legal.terms.s8Title')}</h2>
      <p>{t('site.legal.terms.s8Body')}</p>
    </LegalDocLayout>
  )
}
