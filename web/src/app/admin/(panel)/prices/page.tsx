/** 管理后台：计价规则（/admin/prices）。
 *
 * 意图（Why）：
 *   每个模型在分组维度上的售价（每 1M token 的额度；按次计费为每次调用额度）。
 *   顶部 Tabs 按分组筛选，便于站长按「分组定价」核对是否配齐。
 *   在此基础上支持「渠道专属价」：同一「模型 + 分组」可为某个渠道单独定价，
 *   未配渠道专用价时回退该分组的默认价（优先级由后端 MatchModelPriceForChannel 决定）。
 *
 * 流转（Flow）：
 *   load() → listGroups() 构建分组筛选 + listChannels() 构建渠道筛选，
 *            再按 group / channel_id 拉列表（未选渠道只看分组默认价）；
 *   新建/编辑走 PriceFormModal → createPrice / updatePrice（含 channel_id）；
 *   删除走 ConfirmDialog → deletePrice（删除后该模型按不计费处理）；
 *   页尾费用试算走 QuoteCard → GET /admin/prices/quote（按分组维度，核对定价）。
 *
 * 扩展（Extend）：
 *   新增计费方式：同步 types.ts 的 BillingMode 与弹层下拉、展示徽标文案。
 *   渠道维度字段未写入共享 types.ts（避免与并行改动冲突），
 *   本页用本地 interface（PriceRow / PricePayloadLocal）声明。
 *   费用试算用 api.get 直连接口而非 admin.ts 的 quotePrice 封装：
 *   封装缺 group 参数，带不了「按分组试算」（各分组倍率不同正是试算要点）。
 */
'use client'

import { useCallback, useEffect, useState } from 'react'

import { api } from '@/api/client'
import { createPrice, deletePrice, listChannels, listGroups, listPrices, updatePrice } from '@/api/admin'
import type { BillingMode, ModelPrice, ModelPricePayload, QuotePreview } from '@/api/types'
import { Badge, Card, Tabs } from '@/components/ui/Display'
import { DataTable, type Column } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Field, Input, Select, Switch, Textarea } from '@/components/ui/Form'
import { Modal, ConfirmDialog } from '@/components/ui/Modal'
import { useI18n } from '@/i18n'
import { useToast } from '@/lib/toast/toast-context'
import { useSite } from '@/lib/site/site-context'
import { formatYuanFromQuota, formatYuanPerCall, formatYuanPerMillion, quotaToYuanInput, yuanToQuota } from '@/utils/money'

/** 渠道下拉选项（页面内声明，避免改动共享 types.ts）。 */
interface ChannelOption {
  id: number
  name: string
}

/** 计价规则行：在共享 ModelPrice 之上补充渠道维度字段（后端已下发）。 */
interface PriceRow extends ModelPrice {
  /** 0 = 不限渠道（分组默认价）；>0 = 仅该渠道生效的专用价 */
  channel_id?: number
  /** 渠道显示名；不限渠道或渠道已删除时为空 */
  channel_name?: string
}

/** GET /api/admin/prices 响应。 */
interface PriceListResponse {
  items: PriceRow[]
  total: number
}

/** 新增/更新请求体：在共享 ModelPricePayload 之上补充 channel_id。 */
interface PricePayloadLocal extends ModelPricePayload {
  channel_id?: number
}

/**
 * 生效计费方式 → 徽标配色与文案（free 免费 / token 按量 / per_call 按次）。
 *
 * 参数放宽为 string 而非 ModelPrice['effective_billing_mode']：
 * 试算响应的 billing_mode 可能是空串（未定价），该分支由调用方先行分流，
 * 这里只负责非空方式的配色；对既有表格列的调用是纯放宽，行为不变。
 */
function billingTone(mode: string): 'ok' | 'info' | 'brand' {
  if (mode === 'free') return 'ok'
  if (mode === 'per_call') return 'brand'
  return 'info'
}

const BILLING_LABEL: Record<string, string> = {
  free: 'admin.prices.billing.free',
  token: 'admin.prices.billing.token',
  per_call: 'admin.prices.billing.per_call',
}

export default function AdminPricesPage() {
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [items, setItems] = useState<PriceRow[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [groups, setGroups] = useState<string[]>([])
  const [group, setGroup] = useState('')
  const [channels, setChannels] = useState<ChannelOption[]>([])
  const [channelFilter, setChannelFilter] = useState('')
  const [editing, setEditing] = useState<PriceRow | null | 'new'>(null)
  const [deleteTarget, setDeleteTarget] = useState<PriceRow | null>(null)
  const { toast, toastError } = useToast()

  // 分组列表用于构建筛选 Tabs；失败时只剩「全部」，不影响列表本身
  useEffect(() => {
    void listGroups()
      .then((data) => setGroups(data.items.map((g) => g.name)))
      .catch(() => setGroups([]))
  }, [])

  // 渠道列表供「按渠道筛选」与弹层下拉使用；失败时退化为只有「不限渠道」
  useEffect(() => {
    void listChannels({ size: 500 })
      .then((data) => setChannels(data.items.map((c) => ({ id: c.id, name: c.name }))))
      .catch(() => setChannels([]))
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      // 未选渠道 → 只列分组默认价（不限渠道）；
      // 选中渠道 → 列该渠道生效的价格（分组默认价 + 该渠道专用价）。
      const data =
        channelFilter === ''
          ? await listPrices(group)
          : await api.get<PriceListResponse>('/admin/prices', {
              group,
              channel_id: Number(channelFilter),
            })
      setItems(data.items as PriceRow[])
      setTotal(data.total)
    } catch {
      /* 401 统一处理 */
    } finally {
      setLoading(false)
    }
  }, [group, channelFilter])

  useEffect(() => {
    void load()
  }, [load])

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deletePrice(deleteTarget.id)
      toast(t('admin.prices.toast.deleted'))
      setDeleteTarget(null)
      void load()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.prices.toast.deleteFailed'))
    }
  }

  const columns: Column<PriceRow>[] = [
    { title: t('admin.prices.col.model'), render: (row) => <span className="font-medium text-ink">{row.model}</span> },
    { title: t('admin.prices.col.group'), render: (row) => <span className="text-ink-2">{row.group || t('admin.prices.groupDefault')}</span> },
    {
      title: t('admin.prices.col.channel'),
      render: (row) =>
        row.channel_id ? (
          <span className="text-ink-2" title={t('admin.prices.channelTitle', { id: row.channel_id })}>
            {row.channel_name || t('admin.prices.channelValue', { id: row.channel_id })}
          </span>
        ) : (
          <span className="text-ink-3">{t('admin.prices.channelUnlimited')}</span>
        ),
    },
    {
      title: t('admin.prices.col.billing'),
      render: (row) => <Badge tone={billingTone(row.effective_billing_mode)}>{BILLING_LABEL[row.effective_billing_mode] ? t(BILLING_LABEL[row.effective_billing_mode]) : row.effective_billing_mode}</Badge>,
    },
    { title: t('admin.prices.col.promptPrice'), align: 'right', render: (row) => <span className="text-ink-2">{formatYuanPerMillion(row.prompt_price, quotaPerYuan)}</span> },
    { title: t('admin.prices.col.cachePrice'), align: 'right', render: (row) => <span className="text-ink-2">{formatYuanPerMillion(row.cache_price, quotaPerYuan)}</span> },
    { title: t('admin.prices.col.completionPrice'), align: 'right', render: (row) => <span className="text-ink-2">{formatYuanPerMillion(row.completion_price, quotaPerYuan)}</span> },
    { title: t('admin.prices.col.perCallPrice'), align: 'right', render: (row) => <span className="text-ink-2">{formatYuanPerCall(row.per_call_price, quotaPerYuan)}</span> },
    {
      title: t('admin.prices.col.status'),
      render: (row) => (row.enabled ? <Badge tone="ok">{t('admin.prices.statusEnabled')}</Badge> : <Badge tone="off">{t('admin.prices.statusDisabled')}</Badge>),
    },
    {
      title: t('admin.prices.col.remark'),
      render: (row) => (
        <span className="max-w-44 truncate text-[13px] text-ink-3" title={row.remark || undefined}>
          {row.remark || '—'}
        </span>
      ),
    },
    {
      title: t('admin.prices.col.actions'),
      align: 'right',
      render: (row) => (
        <span className="flex items-center justify-end gap-2 text-[13px]">
          <button type="button" onClick={() => setEditing(row)} className="text-ink-3 hover:text-brand">
            {t('admin.prices.action.edit')}
          </button>
          <button type="button" onClick={() => setDeleteTarget(row)} className="text-ink-3 hover:text-err">
            {t('admin.prices.action.delete')}
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-ink">{t('admin.prices.title')}</h1>
          <p className="mt-0.5 text-[13px] text-ink-3">{t('admin.prices.subtitle', { total })}</p>
        </div>
        <Button variant="primary" onClick={() => setEditing('new')}>{t('admin.prices.create')}</Button>
      </div>

      {/* 分组筛选：空串 = 全部 */}
      <Tabs
        items={[{ value: '', label: t('admin.prices.all') }, ...groups.map((g) => ({ value: g, label: g }))]}
        value={group}
        onChange={setGroup}
      />

      {/* 渠道筛选：不选 = 只看分组默认价；选中渠道 = 该渠道生效的价格集合 */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="w-64 shrink-0">
          <Select value={channelFilter} onChange={(e) => setChannelFilter(e.target.value)} aria-label={t('admin.prices.channelFilterAria')}>
            <option value="">{t('admin.prices.channelFilterAll')}</option>
            {channels.map((c) => (
              <option key={c.id} value={String(c.id)}>
                {t('admin.prices.channelFilterOption', { name: c.name })}
              </option>
            ))}
          </Select>
        </div>
        <p className="text-[12px] text-ink-3">
          {t('admin.prices.channelFilterHint')}
        </p>
      </div>

      <Card padding="none">
        <DataTable
          columns={columns}
          rows={loading ? null : items}
          loading={loading}
          rowKey={(row) => row.id}
          emptyTitle={t('admin.prices.emptyTitle')}
          emptyDescription={t('admin.prices.emptyDescription')}
        />
      </Card>

      {/* 费用试算：放在页尾，不打断上方「筛选 → 管理规则」的主流程 */}
      <QuoteCard groups={groups} />

      <PriceFormModal
        open={editing !== null}
        price={editing === 'new' ? null : editing}
        channels={channels}
        onClose={() => setEditing(null)}
        onSaved={() => { setEditing(null); void load() }}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={t('admin.prices.delete.title')}
        message={t('admin.prices.delete.message', { model: deleteTarget?.model ?? '' })}
        danger
        confirmText={t('admin.prices.delete.confirm')}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/* ── 计价规则表单弹层 ─────────────────────────────────── */

function PriceFormModal({
  open,
  price,
  channels,
  onClose,
  onSaved,
}: {
  open: boolean
  price: PriceRow | null
  channels: ChannelOption[]
  onClose: () => void
  onSaved: () => void
}) {
  const { toast, toastError } = useToast()
  const { quotaPerYuan } = useSite()
  const { t } = useI18n()
  const [model, setModel] = useState('')
  const [groupName, setGroupName] = useState('')
  // 适用渠道：'0' = 不限渠道（分组默认价），其他为渠道 ID 字符串
  const [channelID, setChannelID] = useState('0')
  const [billingMode, setBillingMode] = useState<BillingMode | ''>('')
  const [enabled, setEnabled] = useState(true)
  // 以下四个价格字段一律以【人民币】录入与展示（元 / 1M token，按次为 元/次），
  // 提交时经 yuanToQuota 换算成契约的整数额度。比例缺失时原样提交（由后端照常处理）。
  const [promptPrice, setPromptPrice] = useState('0')
  const [cachePrice, setCachePrice] = useState('0')
  const [completionPrice, setCompletionPrice] = useState('0')
  const [perCallPrice, setPerCallPrice] = useState('0')
  const [remark, setRemark] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setModel(price?.model ?? '')
    setGroupName(price?.group ?? '')
    setChannelID(String(price?.channel_id ?? 0))
    setBillingMode(price?.billing_mode ?? '')
    setEnabled(price?.enabled ?? true)
    setPromptPrice(quotaToYuanInput(price?.prompt_price, quotaPerYuan))
    setCachePrice(quotaToYuanInput(price?.cache_price, quotaPerYuan))
    setCompletionPrice(quotaToYuanInput(price?.completion_price, quotaPerYuan))
    setPerCallPrice(quotaToYuanInput(price?.per_call_price, quotaPerYuan))
    setRemark(price?.remark ?? '')
  }, [open, price, quotaPerYuan])

  /** 人民币输入 → 非负数字；空串按 0 处理 */
  function parseYuan(raw: string): number | null {
    if (raw.trim() === '') return 0
    const value = Number(raw)
    return Number.isFinite(value) && value >= 0 ? value : null
  }

  /** 人民币输入 → 契约额度；比例缺失时原样当作额度（历史兼容） */
  function toQuota(yuan: number | null): number {
    if (yuan === null) return 0
    return yuanToQuota(yuan, quotaPerYuan) ?? Math.round(yuan)
  }

  async function handleSubmit() {
    if (!model.trim()) {
      toastError(t('admin.prices.error.modelRequired'))
      return
    }
    const prompt = parseYuan(promptPrice)
    const cache = parseYuan(cachePrice)
    const completion = parseYuan(completionPrice)
    const perCall = parseYuan(perCallPrice)
    if (prompt === null || cache === null || completion === null || perCall === null) {
      toastError(t('admin.prices.error.priceInvalid'))
      return
    }
    setLoading(true)
    try {
      const payload: PricePayloadLocal = {
        model: model.trim(),
        group: groupName.trim(),
        channel_id: Number(channelID),
        billing_mode: billingMode,
        enabled,
        prompt_price: toQuota(prompt),
        cache_price: toQuota(cache),
        completion_price: toQuota(completion),
        per_call_price: toQuota(perCall),
        remark: remark.trim(),
      }
      if (price) {
        await updatePrice(price.id, payload)
        toast(t('admin.prices.toast.updated'))
      } else {
        await createPrice(payload)
        toast(t('admin.prices.toast.created'))
      }
      onSaved()
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.prices.toast.saveFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={price ? t('admin.prices.form.editTitle') : t('admin.prices.form.newTitle')} width={560}>
      <div className="space-y-4">
        <Field label={t('admin.prices.form.model')} required help={t('admin.prices.form.modelHelp')}>
          <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder={t('admin.prices.form.modelPlaceholder')} />
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <Field label={t('admin.prices.form.group')} help={t('admin.prices.form.groupHelp')}>
            <Input value={groupName} onChange={(e) => setGroupName(e.target.value)} placeholder={t('admin.prices.form.groupPlaceholder')} />
          </Field>

          <Field label={t('admin.prices.form.billing')} help={t('admin.prices.form.billingHelp')}>
            <Select value={billingMode} onChange={(e) => setBillingMode(e.target.value as BillingMode | '')}>
              <option value="">{t('admin.prices.form.billingAuto')}</option>
              <option value="free">{t('admin.prices.form.billingFree')}</option>
              <option value="token">{t('admin.prices.form.billingToken')}</option>
              <option value="per_call">{t('admin.prices.form.billingPerCall')}</option>
            </Select>
          </Field>
        </div>

        <Field
          label={t('admin.prices.form.channel')}
          help={t('admin.prices.form.channelHelp')}
        >
          <Select value={channelID} onChange={(e) => setChannelID(e.target.value)}>
            <option value="0">{t('admin.prices.form.channelUnlimitedOption')}</option>
            {channels.map((c) => (
              <option key={c.id} value={String(c.id)}>
                {c.name}
              </option>
            ))}
          </Select>
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <Field label={t('admin.prices.form.promptPrice')} help={t('admin.prices.form.promptHelp')}>
            <Input value={promptPrice} onChange={(e) => setPromptPrice(e.target.value)} type="number" min={0} step="0.000001" placeholder="0" />
          </Field>
          <Field label={t('admin.prices.form.cachePrice')} help={t('admin.prices.form.cacheHelp')}>
            <Input value={cachePrice} onChange={(e) => setCachePrice(e.target.value)} type="number" min={0} step="0.000001" placeholder="0" />
          </Field>
          <Field label={t('admin.prices.form.completionPrice')} help={t('admin.prices.form.completionHelp')}>
            <Input value={completionPrice} onChange={(e) => setCompletionPrice(e.target.value)} type="number" min={0} step="0.000001" placeholder="0" />
          </Field>
          <Field label={t('admin.prices.form.perCallPrice')} help={t('admin.prices.form.perCallHelp')}>
            <Input value={perCallPrice} onChange={(e) => setPerCallPrice(e.target.value)} type="number" min={0} step="0.000001" placeholder="0" />
          </Field>
        </div>

        <Field label={t('admin.prices.form.remark')}>
          <Textarea value={remark} onChange={(e) => setRemark(e.target.value)} rows={2} placeholder={t('admin.prices.form.optional')} />
        </Field>

        <label className="flex items-center justify-between text-[13px] text-ink-2">
          <span>{t('admin.prices.form.enable')}</span>
          <Switch checked={enabled} onChange={setEnabled} label={t('admin.prices.form.enable')} />
        </label>
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>{t('admin.prices.form.cancel')}</Button>
        <Button variant="primary" loading={loading} onClick={handleSubmit}>{price ? t('admin.prices.form.save') : t('admin.prices.form.create')}</Button>
      </div>
    </Modal>
  )
}

/* ── 费用试算卡片 ─────────────────────────────────────── */

/**
 * 管理员费用试算：按「分组 × 模型 × 用量」预估一次调用的应扣额度。
 *
 * 为什么调后端接口而不是本地复算（与用户侧广场试算器的差别）：
 *   广场试算器服务无管理权限的普通用户，只能按下发的价格字段本地复算；
 *   管理端有权直接调 GET /api/admin/prices/quote —— 它与计费链路用同一份
 *   规则匹配结果（分组倍率已计入），口径最权威，且能区分「免费」
 *   （命中显式免费规则）与「未定价」（根本没配规则）这两种同为 0 额度的语义。
 *
 * 为什么用 api.get 直连而非 admin.ts 的 quotePrice 封装：
 *   该封装不带 group 参数，而「按分组试算」正是本接口的核心能力
 *   （各分组倍率不同，换个分组结果就变）。本页对渠道筛选列表已有
 *   api.get 直连的先例，此处同法调用同一端点并补上 group 查询参数。
 */
function QuoteCard({ groups }: { groups: string[] }) {
  const { quotaPerYuan } = useSite()
  const { toastError } = useToast()
  const { t } = useI18n()
  // 空串 = 计费默认分组：与「令牌未指定分组」的真实计费路径一致
  const [group, setGroup] = useState('')
  const [model, setModel] = useState('')
  // 三个用量输入的初始值与后端 parseInt64Query 的缺省回退值一致，避免「界面默认」与「接口默认」两套数
  const [prompt, setPrompt] = useState('1000')
  const [completion, setCompletion] = useState('1000')
  const [cached, setCached] = useState('0')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<QuotePreview | null>(null)

  /**
   * 用量输入 → 非负整数；空串/非法/负数回退后端缺省值
   * （与后端 parseInt64Query 的回退语义对齐，保证「试的是什么就算什么」）。
   * 取整用 floor 而非 round：token 是天然整数单位，后端整数除法
   * 也是向下截断（「不足 1 额度不计」），与公开试算器保持同一口径。
   */
  function toTokens(raw: string, fallback: number): number {
    if (raw.trim() === '') return fallback
    const v = Math.floor(Number(raw))
    return Number.isFinite(v) && v >= 0 ? v : fallback
  }

  async function handleQuote() {
    const name = model.trim()
    if (!name) {
      toastError(t('admin.prices.quote.modelRequired'))
      return
    }
    setLoading(true)
    try {
      const data = await api.get<QuotePreview>('/admin/prices/quote', {
        model: name,
        // group 为空串时被客户端 cleanParams 剔除，后端据此落到计费默认分组
        group,
        prompt_tokens: toTokens(prompt, 1000),
        completion_tokens: toTokens(completion, 1000),
        cached_tokens: toTokens(cached, 0),
      })
      setResult(data)
    } catch (err) {
      toastError(err instanceof Error ? err.message : t('admin.prices.quote.failed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h2 className="text-[15px] font-semibold text-ink">{t('admin.prices.quote.title')}</h2>
        <span className="text-[12px] text-ink-3">
          {t('admin.prices.quote.subtitle')}
        </span>
      </div>

      <div className="mt-3 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Field label={t('admin.prices.quote.group')} help={t('admin.prices.quote.groupHelp')}>
          <Select value={group} onChange={(e) => setGroup(e.target.value)} aria-label={t('admin.prices.quote.groupAria')}>
            <option value="">{t('admin.prices.quote.groupDefault')}</option>
            {groups.map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
          </Select>
        </Field>

        <Field label={t('admin.prices.quote.model')} required help={t('admin.prices.quote.modelHelp')}>
          <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder={t('admin.prices.quote.modelPlaceholder')} />
        </Field>

        <Field label={t('admin.prices.quote.promptTokens')} help={t('admin.prices.quote.promptHelp')}>
          <Input type="number" min={0} step={1} value={prompt} onChange={(e) => setPrompt(e.target.value)} />
        </Field>

        <Field label={t('admin.prices.quote.completionTokens')} help={t('admin.prices.quote.completionHelp')}>
          <Input type="number" min={0} step={1} value={completion} onChange={(e) => setCompletion(e.target.value)} />
        </Field>

        <Field label={t('admin.prices.quote.cachedTokens')} help={t('admin.prices.quote.cachedHelp')}>
          <Input type="number" min={0} step={1} value={cached} onChange={(e) => setCached(e.target.value)} />
        </Field>

        <div className="flex items-end">
          <Button variant="primary" loading={loading} onClick={handleQuote}>
            {t('admin.prices.quote.submit')}
          </Button>
        </div>
      </div>

      {result && (
        <div className="mt-4 flex flex-wrap items-end justify-between gap-3 rounded-md border border-line bg-surface/50 px-3.5 py-3">
          <div>
            <div className="text-[12px] text-ink-3">{t('admin.prices.quote.estimated')}</div>
            <div className="mt-0.5 font-mono text-xl font-semibold tabular-nums text-ink">
              {formatYuanFromQuota(result.quota, quotaPerYuan)}
            </div>
            <div className="mt-0.5 font-mono text-[11px] text-ink-3">
              {t('admin.prices.quote.quotaEq', { quota: result.quota.toLocaleString('zh-CN') })}
            </div>
          </div>
          <div className="flex flex-col items-end gap-1">
            {result.priced ? (
              result.is_free ? (
                <Badge tone="ok">{t('admin.prices.quote.freeBadge')}</Badge>
              ) : (
                <Badge tone={billingTone(result.billing_mode)}>
                  {BILLING_LABEL[result.billing_mode] ? t(BILLING_LABEL[result.billing_mode]) : result.billing_mode}
                </Badge>
              )
            ) : (
              <Badge tone="warn">{t('admin.prices.quote.unpricedBadge')}</Badge>
            )}
            <span className="max-w-[28rem] text-right text-[11px] text-ink-3">{quoteHint(result, group, t)}</span>
          </div>
        </div>
      )}
    </Card>
  )
}

/**
 * 组装试算结果的说明文案：未定价与免费各给明确语义（二者额度同为 0
 * 但性质完全不同，绝不能混为一谈——前者是待办，后者是站长的决定）；
 * 按量模式回显用量明细，让「这个金额由哪些量算出来的」可核对。
 */
function quoteHint(result: QuotePreview, group: string, t: (key: string, vars?: Record<string, string | number>) => string): string {
  const label = group || t('admin.prices.quote.groupDefault')
  if (!result.priced) {
    return t('admin.prices.quote.hintUnpriced', { group: label })
  }
  if (result.is_free) {
    return t('admin.prices.quote.hintFree', { group: label })
  }
  if (result.billing_mode === 'per_call') {
    // 按次模型 token 单价全为 0，改用量数字价格也不会动——提前说明，避免误判试算失灵
    return t('admin.prices.quote.hintPerCall', { group: label })
  }
  return t('admin.prices.quote.hintToken', {
    group: label,
    prompt: result.prompt_tokens.toLocaleString('zh-CN'),
    cached: result.cached_tokens.toLocaleString('zh-CN'),
    completion: result.completion_tokens.toLocaleString('zh-CN'),
  })
}
