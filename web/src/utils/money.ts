/**
 * 额度 ↔ 人民币 换算与展示。
 *
 * 意图（Why）：
 *   站点内部一律用整数「额度」(quota) 记账——整数运算不会出现浮点误差，
 *   也不会因四舍五入把 99.99999 判成未达标（分组解锁门槛正是按金额比较的）。
 *   但「额度」是站内虚拟单位，用户看不懂。站长问的正是这件事：
 *   计费应当直接以人民币示人。因此换算只发生在**展示边界**上：
 *     人民币金额 = 额度 ÷ quotaPerYuan
 *   quotaPerYuan 由 /api/status 的 quota_per_yuan 下发（当前线上为 1_000_000，
 *   即 1 元 = 100 万额度），与后端 model.FormatCents / exchange_rate 同口径。
 *
 * 为什么比例缺失时退回显示原始额度：
 *   宁可不折算，也不能用一个假设的比例把余额显示成错误的金额——
 *   那会让用户以为钱少了。调用方据此判断。
 *
 * 流转（Flow）：
 *   页面 useSite().quotaPerYuan → formatYuanFromQuota(quota, rate) → "¥1,234.56"
 */
import { translate } from '@/i18n'

/** 额度 → 人民币（数值）。quotaPerYuan 非法时返回 null，由调用方决定如何展示。 */
export function quotaToYuan(quota: number | null | undefined, quotaPerYuan: number): number | null {
  const q = Number(quota ?? 0)
  if (!Number.isFinite(q) || !Number.isFinite(quotaPerYuan) || quotaPerYuan <= 0) return null
  return q / quotaPerYuan
}

/** 人民币（元）→ 额度（四舍五入到整数）。供价格/额度【录入】时从人民币换算回契约字段。
 *  非法比例或非正数金额返回 null（调用方负责提示，避免把空输入静默当 0）。 */
export function yuanToQuota(yuan: number | null | undefined, quotaPerYuan: number): number | null {
  const y = Number(yuan ?? 0)
  if (!Number.isFinite(y) || !Number.isFinite(quotaPerYuan) || quotaPerYuan <= 0 || y < 0) return null
  return Math.round(y * quotaPerYuan)
}

/** 额度 → 人民币数值字符串（不带 ¥，供输入框回填 / 编辑场景）。比例缺失时退回额度原文。 */
export function quotaToYuanInput(quota: number | null | undefined, quotaPerYuan: number): string {
  const yuan = quotaToYuan(quota, quotaPerYuan)
  if (yuan === null) return String(Number(quota ?? 0))
  // 用最大 6 位小数保留录入精度（如 0.002），同时去掉浮点尾巴（如 0.0100000000001）
  return yuan.toFixed(6).replace(/\.?0+$/, '')
}

/**
 * 额度 → 人民币字符串（带 ¥ 与千分位）。
 *
 * 小数位规则：金额较小时保留 4 位（否则 ¥0.0001/次 这类token单价会被显示成 ¥0.00，
 * 看起来像免费）；金额 ≥ 100 元时保留 2 位，避免长尾数字干扰阅读。
 */
export function formatYuanFromQuota(quota: number | null | undefined, quotaPerYuan: number): string {
  const yuan = quotaToYuan(quota, quotaPerYuan)
  if (yuan === null) return `${formatNumberSafe(quota)} ${translate('common.money.quotaUnit')}`
  const abs = Math.abs(yuan)
  const digits = abs > 0 && abs < 0.01 ? 4 : abs >= 100 ? 2 : 4
  return `¥${yuan.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: digits })}`
}

/** 人民币金额（数值，元）→ 显示串；用于充值金额等原本就是"元"的量 */
export function formatYuan(yuan: number | null | undefined): string {
  const v = Number(yuan ?? 0)
  if (!Number.isFinite(v)) return '¥0.00'
  const abs = Math.abs(v)
  const digits = abs > 0 && abs < 0.01 ? 4 : abs >= 100 ? 2 : 4
  return `¥${v.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: digits })}`
}

/** 单价展示：额度/次 → ¥/次（按次计费模型在模型广场与计价页使用） */
export function formatYuanPerCall(perCallQuota: number | null | undefined, quotaPerYuan: number): string {
  return `${formatYuanFromQuota(perCallQuota, quotaPerYuan)}${translate('common.money.perCall')}`
}

/** 单价展示：额度/百万 token → ¥/百万 token（按量计费模型使用） */
export function formatYuanPerMillion(pricePerMillionQuota: number | null | undefined, quotaPerYuan: number): string {
  return `${formatYuanFromQuota(pricePerMillionQuota, quotaPerYuan)}/M`
}

/**
 * 分组倍率（百分比）→ 折扣文案：60 → "6折"、95 → "9.5折"、100 及以上 → "原价"。
 *
 * 代理视图用它把「拿货多少折」直接写给人看——ratio 是内部刻度，
 * 让站长/代理自己对 60 换算成"6 折"既不直观也容易算错。
 */
export function formatDiscountLabel(ratio: number | null | undefined): string {
  const r = Number(ratio ?? 0)
  if (!Number.isFinite(r) || r <= 0 || r >= 100) return translate('common.money.originalPrice')
  const zhe = r / 10
  const zheText = Number.isInteger(zhe) ? zhe : zhe.toFixed(1)
  // 各语言模板占位符不同：中文用 {zhe}（如「6折」），其余语言用 {ratio}（如「60% of price」），故两者都传
  return translate('common.money.discount', { zhe: zheText, ratio: r })
}

/** 数字千分位（内部用，避免与 i18n 循环依赖） */
function formatNumberSafe(value: number | null | undefined): string {
  const v = Number(value ?? 0)
  return Number.isFinite(v) ? v.toLocaleString('zh-CN') : '0'
}
