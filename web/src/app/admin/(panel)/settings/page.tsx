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
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { quotaToYuanInput, yuanToQuota } from '@/utils/money'

type TabKey = 'site' | 'seo' | 'payment' | 'speedtest' | 'compliance'

const TABS: { value: TabKey; label: string }[] = [
  { value: 'site', label: '站点' },
  { value: 'seo', label: 'SEO' },
  { value: 'payment', label: '支付' },
  { value: 'speedtest', label: '测速' },
  { value: 'compliance', label: '合规' },
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
      .catch((err) => toastError(err instanceof Error ? err.message : '设置加载失败'))
      .finally(() => setLoading(false))
  }, [toastError])

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
      toastError(err instanceof Error ? err.message : 'SMTP 状态加载失败')
    }
  }, [toastError])

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
      toast('设置已保存')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '保存失败')
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
      toast('SMTP 配置已保存')
      setSmtpPassword('')
      void loadSmtp()
    } catch (err) {
      toastError(err instanceof Error ? err.message : 'SMTP 保存失败')
    } finally {
      setSmtpSaving(false)
    }
  }

  /** 发送测试邮件（给发件地址） */
  async function handleTestSmtp() {
    setSmtpTesting(true)
    try {
      const result = await testSMTP()
      toast(`测试邮件已发送至 ${result.to}`)
    } catch (err) {
      toastError(err instanceof Error ? err.message : '测试邮件发送失败')
    } finally {
      setSmtpTesting(false)
    }
  }

  /** SMTP 配置来源 → 展示文案与徽标色 */
  function sourceBadge(): { text: string; tone: 'ok' | 'off' | 'warn' } {
    const source = smtp?.source
    if (source === 'database') return { text: '后台配置', tone: 'ok' }
    if (source === 'env') return { text: '环境变量', tone: 'warn' }
    return { text: '未配置', tone: 'off' }
  }

  const source = sourceBadge()

  if (loading) {
    return (
      <div className="space-y-5">
        <div>
          <h1 className="text-xl font-bold text-ink">系统设置</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">站点 · SEO · 支付 · 测速 · 合规 · 邮件通道</p>
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
          <h1 className="text-xl font-bold text-ink">系统设置</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">站点 · SEO · 支付 · 测速 · 合规 · 邮件通道</p>
        </div>
        <Button variant="primary" loading={saving} onClick={handleSave}>保存设置</Button>
      </div>

      <Tabs items={TABS} value={tab} onChange={setTab} />

      {/* ── 站点 ── */}
      {tab === 'site' && (
        <Card className="space-y-4">
          <Field label="站点名称">
            <Input value={siteName} onChange={(e) => setSiteName(e.target.value)} placeholder="站点名称" />
          </Field>
          <Field label="站点描述">
            <Input value={siteDescription} onChange={(e) => setSiteDescription(e.target.value)} placeholder="一句话介绍，展示在登录页等处" />
          </Field>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>开放自助注册</span>
            <Switch checked={registrationEnabled} onChange={setRegistrationEnabled} label="开放自助注册" />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>注册必须邮箱验证码<span className="ml-1 text-xs text-ink-3">（需邮件通道就绪）</span></span>
            <Switch checked={requireEmailCode} onChange={setRequireEmailCode} label="注册必须邮箱验证码" />
          </label>
          <Field label="新用户默认余额（¥）" help="-1 表示不限额度；否则填人民币金额，如 5 表示 5 元">
            <Input value={defaultUserQuota} onChange={(e) => setDefaultUserQuota(e.target.value)} type="number" placeholder="-1" />
          </Field>
          <Field label="默认分组">
            <Input value={defaultGroup} onChange={(e) => setDefaultGroup(e.target.value)} placeholder="default" />
          </Field>
        </Card>
      )}

      {/* ── SEO ── */}
      {tab === 'seo' && seo && (
        <Card className="space-y-4">
          <Field label="站点公开地址（site_url）" help="如 https://api.example.com；留空时后端按访问请求推导">
            <Input value={seo.site_url} onChange={(e) => setSeo({ ...seo, site_url: e.target.value })} placeholder="https://api.example.com" />
          </Field>
          <Field label="SEO 关键词" help="使用逗号分隔">
            <Input value={keywordsText} onChange={(e) => setKeywordsText(e.target.value)} placeholder="AI, API, 大模型" />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="必应站长验证码（msvalidate.01）">
              <Input value={seo.bing_verification} onChange={(e) => setSeo({ ...seo, bing_verification: e.target.value })} />
            </Field>
            <Field label="Google Search Console 验证码">
              <Input value={seo.google_verification} onChange={(e) => setSeo({ ...seo, google_verification: e.target.value })} />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="百度站长验证码">
              <Input value={seo.baidu_verification} onChange={(e) => setSeo({ ...seo, baidu_verification: e.target.value })} />
            </Field>
            <Field label="地域代码（geo_region）" help="如 CN-44">
              <Input value={seo.geo_region} onChange={(e) => setSeo({ ...seo, geo_region: e.target.value })} placeholder="CN-44" />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="地名（geo_placename）" help="如 Shenzhen">
              <Input value={seo.geo_placename} onChange={(e) => setSeo({ ...seo, geo_placename: e.target.value })} placeholder="Shenzhen" />
            </Field>
            <Field label="经纬度（geo_position）" help="格式「纬度;经度」，如 22.5431;114.0579">
              <Input value={seo.geo_position} onChange={(e) => setSeo({ ...seo, geo_position: e.target.value })} placeholder="22.5431;114.0579" />
            </Field>
          </div>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>输出 sitemap.xml 与 robots.txt</span>
            <Switch checked={seo.sitemap_enabled} onChange={(v) => setSeo({ ...seo, sitemap_enabled: v })} label="输出 sitemap.xml 与 robots.txt" />
          </label>
          <Field label="额外公开路径" help="逗号分隔，以 / 开头；如 /models,/docs">
            <Input value={sitemapPathsText} onChange={(e) => setSitemapPathsText(e.target.value)} placeholder="/models" />
          </Field>
          <div className="grid gap-4 rounded-md border border-line bg-surface p-3 sm:grid-cols-2">
            <div className="text-[13px]">
              <div className="text-ink-3">sitemap 地址</div>
              <div className="mt-0.5 truncate text-ink-2" title={seo.sitemap_url}>{seo.sitemap_url || '—'}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">robots 地址</div>
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
              <span>启用在线充值</span>
              <Switch checked={paymentEnabled} onChange={setPaymentEnabled} label="启用在线充值" />
            </label>
            <Field label="启用通道" help="逗号分隔的通道标识，如 epay,stripe；通道密钥需已在环境变量中配置">
              <Input value={methodsText} onChange={(e) => setMethodsText(e.target.value)} placeholder="epay, stripe" />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="法币币种（currency）">
                <Input value={currency} onChange={(e) => setCurrency(e.target.value)} placeholder="CNY" />
              </Field>
              <Field label="兑换比例（exchange_rate）" help="1 法币单位可兑换的额度数">
                <Input value={exchangeRate} onChange={(e) => setExchangeRate(e.target.value)} type="number" placeholder="100" />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="最小充值金额（分）" help="1 分 = 0.01 元；填 0 表示不限下限">
                <Input value={minCents} onChange={(e) => setMinCents(e.target.value)} type="number" placeholder="1" />
              </Field>
              <Field label="最大充值金额（分，0 不限）">
                <Input value={maxCents} onChange={(e) => setMaxCents(e.target.value)} type="number" placeholder="0" />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="订单有效期（分钟）">
                <Input value={orderTtlMinutes} onChange={(e) => setOrderTtlMinutes(e.target.value)} type="number" placeholder="30" />
              </Field>
              <Field label="回调地址前缀（notify_base）">
                <Input value={notifyBase} onChange={(e) => setNotifyBase(e.target.value)} placeholder="https://api.example.com" />
              </Field>
            </div>
            <Field label="通道参数（params）" help="每行一个 key=value，如 epay.gateway=https://pay.example.com">
              <Textarea value={paramsText} onChange={(e) => setParamsText(e.target.value)} rows={4} placeholder={'epay.gateway=https://pay.example.com'} />
            </Field>
          </Card>
          <Card>
            <h2 className="mb-3 text-sm font-semibold text-ink">支付通道状态（只读）</h2>
            <div className="space-y-2">
              {paymentChannels.map((ch) => (
                <div key={ch.key} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-line bg-surface px-3 py-2 text-[13px]">
                  <span className="font-medium text-ink-2">{ch.label}</span>
                  <span className="flex items-center gap-2">
                    {ch.enabled ? <Badge tone="ok">已启用</Badge> : <Badge tone="off">未启用</Badge>}
                    {ch.missing_env.length > 0 ? (
                      <Badge tone="warn">缺密钥：{ch.missing_env.join(', ')}</Badge>
                    ) : (
                      <Badge tone="ok">密钥就绪</Badge>
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
              启用模型测速
              <span className="ml-1 text-xs text-ink-3">（关闭后管理端测速入口拒绝、广场不再下发延迟）</span>
            </span>
            <Switch checked={speedtest.enabled} onChange={(v) => setSpeedtest({ ...speedtest, enabled: v })} label="启用模型测速" />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              在模型广场展示延迟
              <span className="ml-1 text-xs text-ink-3">（关闭后仅管理员可见，用户侧不展示）</span>
            </span>
            <Switch checked={speedtest.public} onChange={(v) => setSpeedtest({ ...speedtest, public: v })} label="在模型广场展示延迟" />
          </label>
          <label className="flex items-center justify-between text-[13px] text-ink-2">
            <span>
              自动屏蔽无权限模型
              <span className="ml-1 text-xs text-ink-3">（测速时上游明确回 403/404 的模型自动从渠道移除）</span>
            </span>
            <Switch checked={speedtest.auto_block} onChange={(v) => setSpeedtest({ ...speedtest, auto_block: v })} label="自动屏蔽无权限模型" />
          </label>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="单模型超时（秒）" help="范围 5~120；部分平台排队久可调大">
              <Input
                value={speedtest.timeout_seconds}
                onChange={(e) => setSpeedtest({ ...speedtest, timeout_seconds: Number(e.target.value) || 0 })}
                type="number"
                placeholder="20"
              />
            </Field>
            <Field label="单次测速模型数上限" help="范围 1~500；超过将拒绝并提示分批">
              <Input
                value={speedtest.max_models}
                onChange={(e) => setSpeedtest({ ...speedtest, max_models: Number(e.target.value) || 0 })}
                type="number"
                placeholder="50"
              />
            </Field>
          </div>
          <div className="rounded-md border border-line bg-surface p-3 text-xs text-ink-3">
            测速说明：逐模型发送最小请求（提示词 ping + max_tokens=1，约消耗 2~3 token），
            测量首字延迟（TTFB）并在拿到首字后立即断开。串行探测，数字不受并发干扰。
            开启自动屏蔽时，上游明确拒绝（403/404）的模型会自动从渠道清单移除；
            超时与 5xx 属暂时性故障，不会被移除。
          </div>
        </Card>
      )}

      {/* ── 合规 ── */}
      {tab === 'compliance' && (
        <Card className="space-y-4">
          <Field label="经营主体名称" help="出现在页脚与协议页；为空时回退显示站点名">
            <Input value={operatorName} onChange={(e) => setOperatorName(e.target.value)} placeholder="如 XX 科技有限公司" />
          </Field>
          <Field label="ICP 备案号" help="如 京ICP备00000000号-1；为空时不展示">
            <Input value={icpLicense} onChange={(e) => setIcpLicense(e.target.value)} placeholder="京ICP备00000000号-1" />
          </Field>
          <Field label="公安联网备案号" help="为空时不展示">
            <Input value={policeLicense} onChange={(e) => setPoliceLicense(e.target.value)} placeholder="京公网安备 00000000000000号" />
          </Field>
          <Field label="客服 / 投诉邮箱" help="为空时不展示">
            <Input value={contactEmail} onChange={(e) => setContactEmail(e.target.value)} placeholder="support@example.com" type="email" />
          </Field>
        </Card>
      )}

      {/* ── SMTP 独立卡片 ── */}
      <Card className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-ink">邮件通道（SMTP）</h2>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={source.tone}>{source.text}</Badge>
            {smtp && (smtp.ready ? <Badge tone="ok">就绪</Badge> : <Badge tone="err">未就绪</Badge>)}
          </div>
        </div>

        {smtp && smtp.effective_host && (
          <div className="grid gap-4 rounded-md border border-line bg-surface p-3 sm:grid-cols-3">
            <div className="text-[13px]">
              <div className="text-ink-3">生效主机</div>
              <div className="mt-0.5 truncate text-ink-2" title={smtp.effective_host}>{smtp.effective_host}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">生效端口</div>
              <div className="mt-0.5 text-ink-2">{smtp.effective_port || '—'}</div>
            </div>
            <div className="text-[13px]">
              <div className="text-ink-3">生效发件人</div>
              <div className="mt-0.5 truncate text-ink-2" title={smtp.effective_from}>{smtp.effective_from || '—'}</div>
            </div>
          </div>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="SMTP 主机" required>
            <Input value={smtpHost} onChange={(e) => setSmtpHost(e.target.value)} placeholder="smtp.example.com" />
          </Field>
          <Field label="端口" required>
            <Input value={smtpPort} onChange={(e) => setSmtpPort(e.target.value)} type="number" placeholder="465" />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="用户名">
            <Input value={smtpUsername} onChange={(e) => setSmtpUsername(e.target.value)} placeholder="发件账号（可为空）" />
          </Field>
          <Field label="口令" help={smtp?.password_set ? '已配置口令；留空表示沿用' : '尚未配置口令'}>
            <Input value={smtpPassword} onChange={(e) => setSmtpPassword(e.target.value)} type="password" placeholder="留空 = 沿用" />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="发件地址（from）">
            <Input value={smtpFrom} onChange={(e) => setSmtpFrom(e.target.value)} placeholder="no-reply@example.com" type="email" />
          </Field>
          <Field label="发件人名称（from_name）">
            <Input value={smtpFromName} onChange={(e) => setSmtpFromName(e.target.value)} placeholder="站点名称" />
          </Field>
        </div>
        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>启用该 SMTP 配置<span className="ml-1 text-xs text-ink-3">（启用后优先于环境变量）</span></span>
          <Switch checked={smtpEnabled} onChange={setSmtpEnabled} label="启用该 SMTP 配置" />
        </label>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" loading={smtpTesting} onClick={handleTestSmtp}>发送测试邮件</Button>
          <Button variant="primary" loading={smtpSaving} onClick={handleSaveSmtp}>保存 SMTP 配置</Button>
        </div>
      </Card>
    </div>
  )
}