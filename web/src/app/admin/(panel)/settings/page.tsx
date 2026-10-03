/** 管理后台：系统设置（/admin/settings）。
 *
 * 意图（Why）：
 *   站点 / SEO / 支付 / 合规四个分区的配置统一在「一处读取、整体保存」，
 *   避免逐字段打接口（后端 /admin/settings 本来就是整对象读写）。
 *   SMTP 走独立的 /admin/smtp 接口（口令不回显，留空沿用），因此单独成卡。
 *
 * 流转（Flow）：
 *   加载 → fetchSettings() 一次拿全部分区 → 铺平进本地 state（含文本型编辑态）
 *   保存 → updateSettings(已编辑对象)（整体提交，字段全量）
 *   SMTP → fetchSMTP() 读状态 + updateSMTP() 保存 + testSMTP() 发测试邮件
 *
 * 扩展（Extend）：
 *   新增设置项：types.ts 的 SiteSettings 加字段 + 本页面加 state 与表单 +
 *   提交 payload 里补字段，三处同步。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { fetchSettings, fetchSMTP, testSMTP, updateSettings, updateSMTP } from '@/api/admin'
import type { PaymentChannel, PaymentSettings, SeoSettings, SiteSettings, SMTPSettings, SpeedTestSettings, UpdateSiteSettingsPayload } from '@/api/types'
import { Badge, Card, SkeletonRows, Tabs } from '@/components/ui/Display'
import { Button } from '@/components/ui/Button'
import { Field, Input, Switch, Textarea } from '@/components/ui/Form'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { quotaToYuanInput, yuanToQuota } from '@/utils/money'

type TabKey = 'site' | 'seo' | 'payment' | 'speedtest' | 'compliance'

const TABS: { value: TabKey; labelKey: string }[] = [
  { value: 'site', labelKey: 'admin.settings.tab.site' },
  { value: 'seo', labelKey: 'admin.settings.tab.seo' },
  { value: 'payment', labelKey: 'admin.settings.tab.payment' },
  { value: 'speedtest', labelKey: 'admin.settings.tab.speedtest' },
  { value: 'compliance', labelKey: 'admin.settings.tab.compliance' },
]

/** 逗号/顿号/空白分隔的文本 → 数组（keywords / methods / sitemap_paths 共用） */
function splitList(text: string): string[] {
  return text.split(/[,，、\s]+/).map((s) => s.trim()).filter(Boolean)
}

/** 「key=value」每行一个的文本 → 对象（payment.params 用） */
function parseParams(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const idx = trimmed.indexOf('=')
    if (idx <= 0) continue
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim()
  }
  return out
}

/** 对象 → 文本（payment.params 的展示形式：每行 key=value） */
function stringifyParams(params: Record<string, string> | undefined): string {
  if (!params) return ''
  return Object.entries(params).map(([k, v]) => `${k}=${v}`).join('\n')
}

export default function AdminSettingsPage() {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [tab, setTab] = useState<TabKey>('site')

  /* ── 站点分区 ── */
  const [siteName, setSiteName] = useState('')
  const [siteDescription, setSiteDescription] = useState('')
  const [registrationEnabled, setRegistrationEnabled] = useState(true)
  const [requireEmailCode, setRequireEmailCode] = useState(false)
  const [defaultUserQuota, setDefaultUserQuota] = useState('')
  const [defaultGroup, setDefaultGroup] = useState('')

  /* ── SEO 分区 ── */
  const [seo, setSeo] = useState<SeoSettings | null>(null)
  const [keywordsText, setKeywordsText] = useState('')
  const [sitemapPathsText, setSitemapPathsText] = useState('')

  /* ── 支付分区 ──（不含密钥：密钥只从环境变量读取，见 payment_secrets） */
  const [payment, setPayment] = useState<PaymentSettings | null>(null)
  const [paymentChannels, setPaymentChannels] = useState<PaymentChannel[]>([])
  const [methodsText, setMethodsText] = useState('')
  const [paramsText, setParamsText] = useState('')
  const [currency, setCurrency] = useState('CNY')
  const [exchangeRate, setExchangeRate] = useState('')
  const [minCents, setMinCents] = useState('')
  const [maxCents, setMaxCents] = useState('')
  const [orderTtlMinutes, setOrderTtlMinutes] = useState('')
  const [notifyBase, setNotifyBase] = useState('')
  const [paymentEnabled, setPaymentEnabled] = useState(false)

  /* ── 合规分区 ── */
  const [operatorName, setOperatorName] = useState('')
  const [icpLicense, setIcpLicense] = useState('')
  const [policeLicense, setPoliceLicense] = useState('')
  const [contactEmail, setContactEmail] = useState('')

  /* ── 模型测速分区 ── */
  const [speedtest, setSpeedtest] = useState<SpeedTestSettings | null>(null)

  /* ── SMTP（独立卡片） ── */
  const [smtp, setSmtp] = useState<SMTPSettings | null>(null)
  const [smtpHost, setSmtpHost] = useState('')
  const [smtpPort, setSmtpPort] = useState('')
  const [smtpUsername, setSmtpUsername] = useState('')
  const [smtpFrom, setSmtpFrom] = useState('')
  const [smtpFromName, setSmtpFromName] = useState('')
  const [smtpEnabled, setSmtpEnabled] = useState(false)
  const [smtpPassword, setSmtpPassword] = useState('')
  const [smtpSaving, setSmtpSaving] = useState(false)
  const [smtpTesting, setSmtpTesting] = useState(false)

  /* ── 加载：settings 一次拿全部分区 ── */
  useEffect(() => {
    void fetchSettings()
      .then((s: SiteSettings) => {
        setSiteName(s.site_name ?? '')
        setSiteDescription(s.site_description ?? '')
        setRegistrationEnabled(s.registration_enabled ?? true)
        setRequireEmailCode(s.registration_require_email_code ?? false)
        setDefaultUserQuota(
          s.default_user_quota === undefined || s.default_user_quota === null
            ? ''
            : s.default_user_quota < 0
              ? '-1' // 不限额度：-1 保持原语义，不做人民币换算
              : quotaToYuanInput(s.default_user_quota, quotaPerYuan),
        )
        setDefaultGroup(s.default_group ?? '')

        setSeo(s.seo ?? null)
        setKeywordsText((s.seo?.keywords ?? []).join(', '))
        setSitemapPathsText((s.seo?.sitemap_paths ?? []).join(', '))

        setPayment(s.payment ?? null)
        setPaymentChannels(s.payment_channels ?? [])
        setMethodsText((s.payment?.methods ?? []).join(', '))
        setParamsText(stringifyParams(s.payment?.params))
        setCurrency(s.payment?.currency ?? 'CNY')
        setExchangeRate(s.payment?.exchange_rate === undefined || s.payment?.exchange_rate === null ? '' : String(s.payment.exchange_rate))
        setMinCents(s.payment?.min_cents === undefined || s.payment?.min_cents === null ? '' : String(s.payment.min_cents))
        setMaxCents(s.payment?.max_cents === undefined || s.payment?.max_cents === null ? '' : String(s.payment.max_cents))
        setOrderTtlMinutes(s.payment?.order_ttl_minutes === undefined || s.payment?.order_ttl_minutes === null ? '' : String(s.payment.order_ttl_minutes))
        setNotifyBase(s.payment?.notify_base ?? '')
        setPaymentEnabled(s.payment?.enabled ?? false)

        setOperatorName(s.compliance?.operator_name ?? '')
        setIcpLicense(s.compliance?.icp_license ?? '')
        setPoliceLicense(s.compliance?.police_license ?? '')
        setContactEmail(s.compliance?.contact_email ?? '')

        setSpeedtest(s.speedtest ?? null)
      })
      .catch((err) => toastError(err instanceof Error ? err.message : t('admin.settings.toast.loadFailed')))
      .finally(() => setLoading(false))
  }, [toastError, t])

  /* ── SMTP 状态与表单 ── */
  const loadSmtp = useCallback(async () => {
    try {
      const data = await fetchSMTP()
      setSmtp(data)
      setSmtpHost(data.host)
      setSmtpPort(String(data.port))
      setSmtpUsername(data.username)
      setSmtpFrom(data.from)
      setSmtpFromName(data.from_name)
      setSmtpEnabled(data.enabled)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.settings.smtp.statusFailed'))
    }
  }, [toastError, t])

  useEffect(() => {
    void loadSmtp()
  }, [loadSmtp])

  /** 整体保存四个分区（payment.params 等文本型字段先转换再提交） */
  async function handleSave() {
    setSaving(true)
    try {
      const payload: UpdateSiteSettingsPayload = {
        site_name: siteName.trim(),
        site_description: siteDescription.trim(),
        registration_enabled: registrationEnabled,
        registration_require_email_code: requireEmailCode,
        default_user_quota:
          defaultUserQuota.trim() === ''
            ? 0
            : defaultUserQuota.trim() === '-1'
              ? -1 // 不限额度：-1 保持原语义，不做人民币换算
              : (yuanToQuota(Number(defaultUserQuota), quotaPerYuan) ?? 0), // 人民币 → 额度
        default_group: defaultGroup.trim(),
        seo: seo
          ? {
              site_url: seo.site_url,
              keywords: splitList(keywordsText),
              bing_verification: seo.bing_verification,
              google_verification: seo.google_verification,
              baidu_verification: seo.baidu_verification,
              geo_region: seo.geo_region,
              geo_placename: seo.geo_placename,
              geo_position: seo.geo_position,
              sitemap_enabled: seo.sitemap_enabled,
              sitemap_paths: splitList(sitemapPathsText),
            }
          : undefined,
        payment: payment
          ? {
              enabled: paymentEnabled,
              methods: splitList(methodsText),
              exchange_rate: exchangeRate.trim() === '' ? 0 : Number(exchangeRate),
              currency: currency.trim() || 'CNY',
              min_cents: minCents.trim() === '' ? 0 : Number(minCents),
              max_cents: maxCents.trim() === '' ? 0 : Number(maxCents),
              order_ttl_minutes: orderTtlMinutes.trim() === '' ? 0 : Number(orderTtlMinutes),
              notify_base: notifyBase.trim(),
              params: parseParams(paramsText),
              // 以下四个为旧版专用字段（已废弃）：仅原样回传，避免保存其他项时被后端清空
              epay_gateway: payment.epay_gateway,
              epay_pid: payment.epay_pid,
              epay_types: payment.epay_types,
              stripe_note: payment.stripe_note,
            }
          : undefined,
        compliance: {
          operator_name: operatorName.trim(),
          icp_license: icpLicense.trim(),
          police_license: policeLicense.trim(),
          contact_email: contactEmail.trim(),
        },
        // 测速分区整体提交：数值字段在输入框层已限制为数字，这里再兜底一次
        speedtest: speedtest
          ? {
              enabled: speedtest.enabled,
              public: speedtest.public,
              auto_block: speedtest.auto_block,
              timeout_seconds: Number(speedtest.timeout_seconds) || 20,
              max_models: Number(speedtest.max_models) || 50,
            }
          : undefined,
      }
      await updateSettings(payload)
      toast(t('admin.settings.toast.saved'))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.settings.toast.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  /** SMTP 保存：password 留空 = 沿用已保存口令 */
  async function handleSaveSmtp() {
    setSmtpSaving(true)
    try {
      await updateSMTP({
        host: smtpHost.trim(),
        port: smtpPort.trim() === '' ? 25 : Number(smtpPort),
        username: smtpUsername.trim(),
        from: smtpFrom.trim(),
        from_name: smtpFromName.trim(),
        enabled: smtpEnabled,
        password: smtpPassword,
      })
      toast(t('admin.settings.smtp.saved'))
      setSmtpPassword('')
      void loadSmtp()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.settings.smtp.saveFailed'))
    } finally {
      setSmtpSaving(false)
    }
  }

  /** 发送测试邮件（给发件地址） */
  async function handleTestSmtp() {
    setSmtpTesting(true)
    try {
      const result = await testSMTP()
      toast(t('admin.settings.smtp.testSent', { to: result.to }))
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.settings.smtp.testFailed'))
    } finally {
      setSmtpTesting(false)
    }
  }

  /** SMTP 配置来源 → 展示文案与徽标色 */
  function sourceBadge(): { text: string; tone: 'ok' | 'off' | 'warn' } {
    const source = smtp?.source
    if (source === 'database') return { text: t('admin.settings.smtp.sourceDatabase'), tone: 'ok' }
    if (source === 'env') return { text: t('admin.settings.smtp.sourceEnv'), tone: 'warn' }
    return { text: t('admin.settings.smtp.sourceNone'), tone: 'off' }
  }

  const source = sourceBadge()

  if (loading) {
    return (
      <div className="space-y-5">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.settings.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.settings.subtitle')}</p>
        </div>
        <Card>
          <SkeletonRows rows={8} />
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.settings.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.settings.subtitle')}</p>
        </div>
        <Button variant="primary" loading={saving} onClick={handleSave}>{t('admin.settings.save')}</Button>
      </div>

      <Tabs items={TABS.map((item) => ({ value: item.value, label: t(item.labelKey) }))} value={tab} onChange={setTab} />

      {/* ── 站点 ── */}
      {tab === 'site' && (
        <Card className="space-y-4">
          <Field label={t('admin.settings.site.siteName')}>
            <Input value={siteName} onChange={(e) => setSiteName(e.target.value)} placeholder={t('admin.settings.site.siteNamePlaceholder')} />
          </Field>
          <Field label={t('admin.settings.site.siteDescription')}>
            <Input value={siteDescription} onChange={(e) => setSiteDescription(e.target.value)} placeholder={t('admin.settings.site.siteDescriptionPlaceholder')} />
          </Field>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>{t('admin.settings.site.registration')}</span>
            <Switch checked={registrationEnabled} onChange={setRegistrationEnabled} label={t('admin.settings.site.registration')} />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>{t('admin.settings.site.requireEmailCode')}<span className="ml-1 text-xs text-ink-3">{t('admin.settings.site.requireEmailCodeHint')}</span></span>
            <Switch checked={requireEmailCode} onChange={setRequireEmailCode} label={t('admin.settings.site.requireEmailCode')} />
          </label>
          <Field label={t('admin.settings.site.defaultQuota')} help={t('admin.settings.site.defaultQuotaHelp')}>
            <Input value={defaultUserQuota} onChange={(e) => setDefaultUserQuota(e.target.value)} type="number" placeholder={t('admin.settings.site.defaultQuotaPlaceholder')} />
          </Field>
          <Field label={t('admin.settings.site.defaultGroup')}>
            <Input value={defaultGroup} onChange={(e) => setDefaultGroup(e.target.value)} placeholder={t('admin.settings.site.defaultGroupPlaceholder')} />
          </Field>
        </Card>
      )}

      {/* ── SEO ── */}
      {tab === 'seo' && seo && (
        <Card className="space-y-4">
          <Field label={t('admin.settings.seo.siteUrl')} help={t('admin.settings.seo.siteUrlHelp')}>
            <Input value={seo.site_url} onChange={(e) => setSeo({ ...seo, site_url: e.target.value })} placeholder={t('admin.settings.seo.siteUrlPlaceholder')} />
          </Field>
          <Field label={t('admin.settings.seo.keywords')} help={t('admin.settings.seo.keywordsHelp')}>
            <Input value={keywordsText} onChange={(e) => setKeywordsText(e.target.value)} placeholder={t('admin.settings.seo.keywordsPlaceholder')} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('admin.settings.seo.bing')}>
              <Input value={seo.bing_verification} onChange={(e) => setSeo({ ...seo, bing_verification: e.target.value })} />
            </Field>
            <Field label={t('admin.settings.seo.google')}>
              <Input value={seo.google_verification} onChange={(e) => setSeo({ ...seo, google_verification: e.target.value })} />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('admin.settings.seo.baidu')}>
              <Input value={seo.baidu_verification} onChange={(e) => setSeo({ ...seo, baidu_verification: e.target.value })} />
            </Field>
            <Field label={t('admin.settings.seo.geoRegion')} help={t('admin.settings.seo.geoRegionHelp')}>
              <Input value={seo.geo_region} onChange={(e) => setSeo({ ...seo, geo_region: e.target.value })} placeholder={t('admin.settings.seo.geoRegionPlaceholder')} />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('admin.settings.seo.geoPlacename')} help={t('admin.settings.seo.geoPlacenameHelp')}>
              <Input value={seo.geo_placename} onChange={(e) => setSeo({ ...seo, geo_placename: e.target.value })} placeholder={t('admin.settings.seo.geoPlacenamePlaceholder')} />
            </Field>
            <Field label={t('admin.settings.seo.geoPosition')} help={t('admin.settings.seo.geoPositionHelp')}>
              <Input value={seo.geo_position} onChange={(e) => setSeo({ ...seo, geo_position: e.target.value })} placeholder={t('admin.settings.seo.geoPositionPlaceholder')} />
            </Field>
          </div>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>{t('admin.settings.seo.sitemap')}</span>
            <Switch checked={seo.sitemap_enabled} onChange={(v) => setSeo({ ...seo, sitemap_enabled: v })} label={t('admin.settings.seo.sitemap')} />
          </label>
          <Field label={t('admin.settings.seo.extraPaths')} help={t('admin.settings.seo.extraPathsHelp')}>
            <Input value={sitemapPathsText} onChange={(e) => setSitemapPathsText(e.target.value)} placeholder={t('admin.settings.seo.extraPathsPlaceholder')} />
          </Field>
          <div className="grid gap-4 rounded-md border border-line bg-surface p-3 sm:grid-cols-2">
            <div className="text-[13px]">
              <div className="text-ink-3">{t('admin.settings.seo.sitemapUrl')}</div>
              <div className="mt-0.5 truncate text-ink-2" title={seo.sitemap_url}>{seo.sitemap_url || '—'}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">{t('admin.settings.seo.robotsUrl')}</div>
              <div className="mt-0.5 truncate text-ink-2" title={seo.robots_url}>{seo.robots_url || '—'}</div>
            </div>
          </div>
        </Card>
      )}

      {/* ── 支付 ── */}
      {tab === 'payment' && payment && (
        <>
          <Card className="space-y-4">
            <label className="flex items-center justify-between text-[13px] text-ink-2">
              <span>{t('admin.settings.payment.enable')}</span>
              <Switch checked={paymentEnabled} onChange={setPaymentEnabled} label={t('admin.settings.payment.enable')} />
            </label>
            <Field label={t('admin.settings.payment.methods')} help={t('admin.settings.payment.methodsHelp')}>
              <Input value={methodsText} onChange={(e) => setMethodsText(e.target.value)} placeholder={t('admin.settings.payment.methodsPlaceholder')} />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('admin.settings.payment.currency')}>
                <Input value={currency} onChange={(e) => setCurrency(e.target.value)} placeholder={t('admin.settings.payment.currencyPlaceholder')} />
              </Field>
              <Field label={t('admin.settings.payment.exchangeRate')} help={t('admin.settings.payment.exchangeRateHelp')}>
                <Input value={exchangeRate} onChange={(e) => setExchangeRate(e.target.value)} type="number" placeholder={t('admin.settings.payment.exchangeRatePlaceholder')} />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('admin.settings.payment.minCents')} help={t('admin.settings.payment.minCentsHelp')}>
                <Input value={minCents} onChange={(e) => setMinCents(e.target.value)} type="number" placeholder={t('admin.settings.payment.minCentsPlaceholder')} />
              </Field>
              <Field label={t('admin.settings.payment.maxCents')}>
                <Input value={maxCents} onChange={(e) => setMaxCents(e.target.value)} type="number" placeholder={t('admin.settings.payment.maxCentsPlaceholder')} />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('admin.settings.payment.orderTtl')}>
                <Input value={orderTtlMinutes} onChange={(e) => setOrderTtlMinutes(e.target.value)} type="number" placeholder={t('admin.settings.payment.orderTtlPlaceholder')} />
              </Field>
              <Field label={t('admin.settings.payment.notifyBase')}>
                <Input value={notifyBase} onChange={(e) => setNotifyBase(e.target.value)} placeholder={t('admin.settings.payment.notifyBasePlaceholder')} />
              </Field>
            </div>
            <Field label={t('admin.settings.payment.params')} help={t('admin.settings.payment.paramsHelp')}>
              <Textarea value={paramsText} onChange={(e) => setParamsText(e.target.value)} rows={4} placeholder={t('admin.settings.payment.paramsPlaceholder')} />
            </Field>
          </Card>
          <Card>
            <h2 className="mb-3 text-sm font-semibold text-ink">{t('admin.settings.payment.channelsTitle')}</h2>
            <div className="space-y-2">
              {paymentChannels.map((ch) => (
                <div key={ch.key} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2 text-[13px]">
                  <span className="font-medium text-ink-2">{ch.label}</span>
                  <span className="flex items-center gap-2">
                    {ch.enabled ? <Badge tone="ok">{t('admin.settings.payment.channelEnabled')}</Badge> : <Badge tone="off">{t('admin.settings.payment.channelDisabled')}</Badge>}
                    {ch.missing_env.length > 0 ? (
                      <Badge tone="warn">{t('admin.settings.payment.missingEnv', { env: ch.missing_env.join(', ') })}</Badge>
                    ) : (
                      <Badge tone="ok">{t('admin.settings.payment.keysReady')}</Badge>
                    )}
                  </span>
                </div>
              ))}
            </div>
          </Card>
        </>
      )}

      {/* ── 模型测速 ── */}
      {tab === 'speedtest' && speedtest && (
        <Card className="space-y-4">
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              {t('admin.settings.speedtest.enable')}
              <span className="ml-1 text-xs text-ink-3">{t('admin.settings.speedtest.enableHint')}</span>
            </span>
            <Switch checked={speedtest.enabled} onChange={(v) => setSpeedtest({ ...speedtest, enabled: v })} label={t('admin.settings.speedtest.enable')} />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              {t('admin.settings.speedtest.public')}
              <span className="ml-1 text-xs text-ink-3">{t('admin.settings.speedtest.publicHint')}</span>
            </span>
            <Switch checked={speedtest.public} onChange={(v) => setSpeedtest({ ...speedtest, public: v })} label={t('admin.settings.speedtest.public')} />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              {t('admin.settings.speedtest.autoBlock')}
              <span className="ml-1 text-xs text-ink-3">{t('admin.settings.speedtest.autoBlockHint')}</span>
            </span>
            <Switch checked={speedtest.auto_block} onChange={(v) => setSpeedtest({ ...speedtest, auto_block: v })} label={t('admin.settings.speedtest.autoBlock')} />
          </label>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('admin.settings.speedtest.timeout')} help={t('admin.settings.speedtest.timeoutHelp')}>
              <Input
                value={speedtest.timeout_seconds}
                onChange={(e) => setSpeedtest({ ...speedtest, timeout_seconds: Number(e.target.value) || 0 })}
                type="number"
                placeholder={t('admin.settings.speedtest.timeoutPlaceholder')}
              />
            </Field>
            <Field label={t('admin.settings.speedtest.maxModels')} help={t('admin.settings.speedtest.maxModelsHelp')}>
              <Input
                value={speedtest.max_models}
                onChange={(e) => setSpeedtest({ ...speedtest, max_models: Number(e.target.value) || 0 })}
                type="number"
                placeholder={t('admin.settings.speedtest.maxModelsPlaceholder')}
              />
            </Field>
          </div>
          <div className="rounded-md border border-line bg-surface p-3 text-xs text-ink-3">
            {t('admin.settings.speedtest.explain')}
          </div>
        </Card>
      )}

      {/* ── 合规 ── */}
      {tab === 'compliance' && (
        <Card className="space-y-4">
          <Field label={t('admin.settings.compliance.operatorName')} help={t('admin.settings.compliance.operatorNameHelp')}>
            <Input value={operatorName} onChange={(e) => setOperatorName(e.target.value)} placeholder={t('admin.settings.compliance.operatorNamePlaceholder')} />
          </Field>
          <Field label={t('admin.settings.compliance.icp')} help={t('admin.settings.compliance.icpHelp')}>
            <Input value={icpLicense} onChange={(e) => setIcpLicense(e.target.value)} placeholder={t('admin.settings.compliance.icpPlaceholder')} />
          </Field>
          <Field label={t('admin.settings.compliance.police')} help={t('admin.settings.compliance.policeHelp')}>
            <Input value={policeLicense} onChange={(e) => setPoliceLicense(e.target.value)} placeholder={t('admin.settings.compliance.policePlaceholder')} />
          </Field>
          <Field label={t('admin.settings.compliance.contactEmail')} help={t('admin.settings.compliance.contactEmailHelp')}>
            <Input value={contactEmail} onChange={(e) => setContactEmail(e.target.value)} placeholder={t('admin.settings.compliance.contactEmailPlaceholder')} type="email" />
          </Field>
        </Card>
      )}

      {/* ── SMTP 独立卡片 ── */}
      <Card className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-ink">{t('admin.settings.smtp.title')}</h2>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={source.tone}>{source.text}</Badge>
            {smtp && (smtp.ready ? <Badge tone="ok">{t('admin.settings.smtp.ready')}</Badge> : <Badge tone="err">{t('admin.settings.smtp.notReady')}</Badge>)}
          </div>
        </div>

        {smtp && smtp.effective_host && (
          <div className="grid gap-4 rounded-md border border-line bg-surface p-3 sm:grid-cols-3">
            <div className="text-[13px]">
              <div className="text-ink-3">{t('admin.settings.smtp.effectiveHost')}</div>
              <div className="mt-0.5 truncate text-ink-2" title={smtp.effective_host}>{smtp.effective_host}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">{t('admin.settings.smtp.effectivePort')}</div>
              <div className="mt-0.5 text-ink-2">{smtp.effective_port || '—'}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">{t('admin.settings.smtp.effectiveFrom')}</div>
              <div className="mt-0.5 truncate text-ink-2" title={smtp.effective_from}>{smtp.effective_from || '—'}</div>
            </div>
          </div>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('admin.settings.smtp.host')} required>
            <Input value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} placeholder={t('admin.settings.smtp.hostPlaceholder')} />
          </Field>
          <Field label={t('admin.settings.smtp.port')} required>
            <Input value={smtpPort} onChange={(e) => setSmtpPort(e.target.value)} type="number" placeholder={t('admin.settings.smtp.portPlaceholder')} />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('admin.settings.smtp.username')}>
            <Input value={smtpUsername} onChange={(e) => setSmtpUsername(e.target.value)} placeholder={t('admin.settings.smtp.usernamePlaceholder')} />
          </Field>
          <Field label={t('admin.settings.smtp.password')} help={smtp?.password_set ? t('admin.settings.smtp.passwordHelpSet') : t('admin.settings.smtp.passwordHelpUnset')}>
            <Input value={smtpPassword} onChange={(e) => setSmtpPassword(e.target.value)} type="password" placeholder={t('admin.settings.smtp.passwordPlaceholder')} />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('admin.settings.smtp.from')}>
            <Input value={smtpFrom} onChange={(e) => setSmtpFrom(e.target.value)} placeholder={t('admin.settings.smtp.fromPlaceholder')} type="email" />
          </Field>
          <Field label={t('admin.settings.smtp.fromName')}>
            <Input value={smtpFromName} onChange={(e) => setSmtpFromName(e.target.value)} placeholder={t('admin.settings.smtp.fromNamePlaceholder')} />
          </Field>
        </div>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.settings.smtp.enable')}<span className="ml-1 text-xs text-ink-3">{t('admin.settings.smtp.enableHint')}</span></span>
          <Switch checked={smtpEnabled} onChange={setSmtpEnabled} label={t('admin.settings.smtp.enable')} />
        </label>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" loading={smtpTesting} onClick={handleTestSmtp}>{t('admin.settings.smtp.test')}</Button>
          <Button variant="primary" loading={smtpSaving} onClick={handleSaveSmtp}>{t('admin.settings.smtp.save')}</Button>
        </div>
      </Card>
    </div>
  )
}