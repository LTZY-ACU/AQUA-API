/** 联系方式页（/contact）。
 *
 * 意图（Why）：
 *   合规公示：向访客提供可联系的客服渠道（邮箱），无邮箱时显示群入口。
 *
 * 流转（Flow）：
 *   LegalDocLayout + 邮箱/交流群/响应时段三段；文案一律走 t('site.legal.contact.*')，
 *   邮箱地址来自站点状态接口（useSite），未配置时展示群引导文案。
 */
'use client'

import { LegalDocLayout } from '@/components/site/LegalDocLayout'
import { useI18n } from '@/i18n'
import { useSite } from '@/lib/site/site-context'

export default function ContactPage() {
  const { status } = useSite()
  const { t } = useI18n()
  return (
    <LegalDocLayout title={t('site.legal.contact.title')} updatedAt={t('site.legal.contact.updatedAt')}>
      <h2>{t('site.legal.contact.emailTitle')}</h2>
      <p>
        {t('site.legal.contact.emailBody')}
        {status?.contact_email ? (
          <a href={`mailto:${status.contact_email}`} className="font-medium text-brand hover:underline">
            {status.contact_email}
          </a>
        ) : (
          <span className="text-ink-3">{t('site.legal.contact.emailNotSet')}</span>
        )}
      </p>
      <h2>{t('site.legal.contact.groupTitle')}</h2>
      <p>
        {t('site.legal.contact.groupBodyPrefix')}
        <a href="/join" className="text-brand hover:underline">{t('site.legal.contact.groupLink')}</a>
        {t('site.legal.contact.groupBodySuffix')}
      </p>
      <h2>{t('site.legal.contact.hoursTitle')}</h2>
      <p>{t('site.legal.contact.hoursBody')}</p>
    </LegalDocLayout>
  )
}
