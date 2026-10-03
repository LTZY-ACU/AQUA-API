/**
 * 模型广场价格换算与试算（纯函数，无副作用、无 DOM）。
 *
 * 意图（Why）：
 *   广场列表、详情弹层与试算器都在回答同一组问题——「这个模型怎么计费？」、
 *   「按我这个用量大概花多少？」。把口径集中在这里，保证：
 *     1) 展示（列表摘要、详情价格行）与试算结果永远一致，不会各算各的；
 *     2) 换算规则与后端计费链路【逐字同口径】——
 *        见 internal/model/price_formula.go 的 ComputeTokenQuota 与
 *        internal/relay/billing.go 的 applyRatio，避免"前端显示 ¥1、后端扣 ¥2"。
 *
 * 为什么前端自己算而不调后端试算接口：
 *   现有试算接口是 GET /api/admin/prices/quote，挂在 /admin 下需管理员权限，
 *   公开广场无权调用。故此处按后端下发的价格字段本地复算；
 *   若日后提供公开试算接口（如 GET /api/models/quote），可在此处接入并保留本地回退。
 *
 * 流转（Flow）：
 *   PlazaPrice[]（GET /api/models 下发）→ localQuote() → { quota, ... }
 *   → formatYuanFromQuota(quota, quotaPerYuan) → 人民币展示
 *
 * 扩展（Extend）：
 *   新增计费维度（按图片张数 / 音频分钟）时：在 localQuote 内追加分支，
 *   并同步后端的新换算函数，两端保持同一取整口径。
 */
import type { PlazaPrice } from '@/api/types'
import {
  formatDiscountLabel,
  formatYuanPerCall,
  formatYuanPerMillion,
} from '@/utils/money'

/** 计费方式；比后端 billing_mode 多一个 none，表示"没有任何价格规则" */
export type BillingKind = 'free' | 'per_call' | 'token' | 'none'

/** 价格口径换算基数：价格字段表示「每 100 万 token 的额度」。与后端 quotaScale 一致。 */
const QUOTA_SCALE = 1_000_000
/** 倍率换算基数（百分比，100 = 1.0 倍）。与后端 ratioScale 一致。 */
const RATIO_SCALE = 100

/** 试算输入的合理上限：仅用于夹住异常输入，避免超大数导致浮点精度失真 */
export const QUOTE_MAX_TOKENS = 10_000_000
export const QUOTE_MAX_COUNT = 1_000_000

/** 从一条价格规则判定计费方式（免费优先于其它字段） */
export function billingKindOf(price: PlazaPrice | undefined): BillingKind {
  if (!price) return 'none'
  if (price.is_free || price.billing_mode === 'free') return 'free'
  if (price.billing_mode === 'per_call') return 'per_call'
  return 'token'
}

/** 计费方式的中文短标签：免费 / 按次 / 按量 / 未定价 */
export function billingKindLabel(kind: BillingKind): string {
  switch (kind) {
    case 'free':
      return '免费'
    case 'per_call':
      return '按次'
    case 'token':
      return '按量'
    default:
      return '未定价'
  }
}

/**
 * 一行价格摘要（列表行、详情副标题用）。
 *
 * 三种模式都给出可辨识的文案：免费 / ¥X/次 / 输入 ¥X/M · 输出 ¥Y/M，
 * 让用户扫一眼就能区分计费方式，而不是只看到一个孤零零的数字。
 */
export function priceSummaryLabel(price: PlazaPrice | undefined, quotaPerYuan: number): string {
  const kind = billingKindOf(price)
  if (!price || kind === 'none') return '待定价'
  if (kind === 'free') return '免费'
  if (kind === 'per_call') {
    return price.per_call_price > 0 ? formatYuanPerCall(price.per_call_price, quotaPerYuan) : '按次计费'
  }
  return `输入 ${formatYuanPerMillion(price.prompt_price, quotaPerYuan)} · 输出 ${formatYuanPerMillion(
    price.completion_price,
    quotaPerYuan,
  )}`
}

/**
 * 缓存命中单价文案（仅按量模式下有意义）。
 *
 * 未配置（cache_price <= 0）时返回 null，调用方据此【不渲染】——
 * 显示「¥0.00/M」会让用户误以为命中缓存免费，属于误导。
 */
export function cachePriceLabel(price: PlazaPrice | undefined, quotaPerYuan: number): string | null {
  if (!price || price.cache_price <= 0) return null
  return formatYuanPerMillion(price.cache_price, quotaPerYuan)
}

/**
 * 倍率说明文案：60 → 「倍率 60%（6折）」、100 → 「倍率 100%（原价）」。
 *
 * 同一模型在不同分组价格不同，根因就是分组倍率不同；把 ratio 直接写出来，
 * 用户才能理解"为什么换个分组就便宜了"。
 */
export function ratioLabel(ratio: number): string {
  const r = Number(ratio ?? 0)
  if (!Number.isFinite(r) || r <= 0) return '倍率 100%（原价）'
  if (r >= 100) return `倍率 ${r}%`
  return `倍率 ${r}%（${formatDiscountLabel(r)}）`
}

/** 折叠成非负整数并夹到 [0, max]，非数字/空值按 0 处理 */
function toSafeInt(value: number, max: number): number {
  const v = Math.floor(Number(value))
  if (!Number.isFinite(v) || v <= 0) return 0
  return v > max ? max : v
}

/** 按分组倍率折算额度（向下取整）。与后端 relay.applyRatio 同口径。 */
function applyRatio(base: number, ratio: number): number {
  if (base <= 0 || ratio <= 0 || ratio === RATIO_SCALE) return base
  return Math.floor((base * ratio) / RATIO_SCALE)
}

/** 按 token 用量换算额度（向下取整）。与后端 model.ComputeTokenQuota 同口径。 */
function computeTokenQuota(price: PlazaPrice, input: QuoteInput): number {
  const prompt = toSafeInt(input.promptTokens, QUOTE_MAX_TOKENS)
  const completion = toSafeInt(input.completionTokens, QUOTE_MAX_TOKENS)
  // 命中缓存的输入不可能超过输入总量：夹到 prompt，防止"输入被算两次"
  const cached = Math.min(toSafeInt(input.cachedTokens, QUOTE_MAX_TOKENS), prompt)
  // cache_price <= 0 表示未配置缓存价，命中部分回退按 promptPrice 计（与后端一致）
  const cachePrice = price.cache_price > 0 ? price.cache_price : price.prompt_price
  const uncached = prompt - cached
  const raw = uncached * price.prompt_price + cached * cachePrice + completion * price.completion_price
  return Math.floor(raw / QUOTA_SCALE)
}

/** 试算输入 */
export interface QuoteInput {
  /** 输入（prompt）token 数 */
  promptTokens: number
  /** 输出（completion）token 数 */
  completionTokens: number
  /** 其中命中缓存的输入 token 数（按量且配置了缓存价时才有意义） */
  cachedTokens: number
  /** 调用次数（按次模式生效） */
  count: number
}

/** 试算结果 */
export interface QuoteResult {
  kind: BillingKind
  /** 是否免费（显式免费规则） */
  isFree: boolean
  /** 应扣额度（整数，与后端一致） */
  quota: number
}

/**
 * 本地试算：按后端下发的价格字段复算「一次调用」的应扣额度。
 *
 * 为什么用 Math.floor 而不是四舍五入：
 *   后端 ComputeTokenQuota 与 applyRatio 都采用整数除法，Go 对非负整数是向下截断，
 *   语义是"不足 1 额度不计费"。前端若用 Math.round，小额调用会凭空多算一档，
 *   出现"试算 ¥0.01、实际扣 ¥0.00"的口径偏差，因此这里严格用 Math.floor。
 *
 * 返回 null 表示该分组没有任何价格规则（未定价），调用方展示"不可试算"。
 */
export function localQuote(price: PlazaPrice | undefined, input: QuoteInput): QuoteResult | null {
  const kind = billingKindOf(price)
  if (!price || kind === 'none') return null
  if (kind === 'free') {
    return { kind, isFree: true, quota: 0 }
  }
  if (kind === 'per_call') {
    // 次数缺省按 1 次（与后端 ComputePerCallAmount 的 count<=0 → 1 一致）
    const count = toSafeInt(input.count, QUOTE_MAX_COUNT) || 1
    const base = price.per_call_price > 0 ? price.per_call_price * count : 0
    return { kind, isFree: false, quota: applyRatio(base, price.ratio) }
  }
  return { kind, isFree: false, quota: applyRatio(computeTokenQuota(price, input), price.ratio) }
}
