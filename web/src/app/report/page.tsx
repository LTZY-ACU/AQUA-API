/** 投诉举报页（/report）。
 *
 * 意图（Why）：
 *   合规公示：接受用户对本站服务与内容的投诉举报，并给出处理路径。
 *
 * 流转（Flow）：
 *   LegalDocLayout + 四段说明；文案一律走 t('site.legal.report.*')，
 *   举报邮箱来自站点状态接口（useSite），未配置时展示占位提示。
 */
'use client'

import { LegalDocLayout } from '@/components/site/LegalDocLayout'
import { useI18n } from '@/i18n'
import { useSite } from '@/lib/site/site-context'

export default function ReportPage() {
  const { status } = useSite()
  const { t } = useI18n()
  return (
    <LegalDocLayout title={t('site.legal.report.title')} updatedAt={t('site.legal.report.updatedAt')}>
      <h2>{t('site.legal.report.s1Title')}</h2>
      <p>{t('site.legal.report.s1Body')}</p>
      <h2>{t('site.legal.report.s2Title')}</h2>
      <p>
        {t('site.legal.report.s2BodyPrefix')}
        {status?.contact_email ? (
          <a href={`mailto:${status.contact_email}`} className="font-medium text-brand hover:underline">
            {status.contact_email}
          </a>
        ) : (
          <span className="text-ink-3">{t('site.legal.report.emailNotSet')}</span>
        )}
        {t('site.legal.report.s2BodySuffix')}
      </p>
      <h2>{t('site.legal.report.s3Title')}</h2>
      <p>{t('site.legal.report.s3Body')}</p>
      <h2>{t('site.legal.report.s4Title')}</h2>
      <p>{t('site.legal.report.s4Body')}</p>
    </LegalDocLayout>
  )
}
