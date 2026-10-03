/**
 * 展示格式化工具（兼容转发层）。
 *
 * 意图（Why）：
 *   契约里所有时间字段都是 Unix 秒、所有数量字段都是裸数字，
 *   若各页面各写一套格式化，会出现「有的带千分位、有的不带」的不一致。
 *   现在「时间 / 数字」的本地化统一交给 src/i18n/format.ts（基于 Intl，跟随当前语言），
 *   本文件保留原有导入路径以免大范围改动调用方，只做转发与少量仍属「换算/解析」的工具。
 *
 * 流转（Flow）：
 *   .vue / LogTable 等组件 → 本文件（转发）→ i18n/format.ts → Intl.* → 已本地化字符串
 *
 * 扩展（Extend）：
 *   新增「本地化展示」格式化请加在 i18n/format.ts（那里能拿到当前语言）；
 *   本文件只保留与语言无关的换算与解析（formatLatency / parseModelList 等）。
 */
import { translate } from '@/i18n'
import { EMPTY, formatDateTime, formatNumber } from '@/i18n/format'

// 时间 / 数字的本地化格式化统一从 i18n/format.ts 转发，保持既有导入路径可用
export { EMPTY, formatCompact, formatCurrency, formatDate, formatDateTime, formatNumber, formatPercent, formatRelative } from '@/i18n/format'

/**
 * 毫秒耗时：>1s 用秒表示，便于快速判断慢请求。
 * 单位（ms / s）跨语言通用，故不做本地化，留在本文件。
 */
export function formatLatency(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return EMPTY
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

/**
 * 输出速率（tokens/秒）：保留一位小数。
 *
 * 为什么 0 不展示成 "0.0 t/s"：0 是"无法计算"（非流式、上游未回报用量、
 * 或耗时太短没有有效生成区间），若原样显示会被误读为"模型慢到不产出"。
 */
export function formatRate(tps: number | null | undefined): string {
  if (tps === null || tps === undefined || Number.isNaN(tps) || tps <= 0) return EMPTY
  return `${tps.toFixed(1)} t/s`
}

/**
 * 到期时间展示：0 表示永不过期（契约约定）。
 */
export function formatExpiry(ts: number | null | undefined): string {
  if (!ts) return translate('common.value.neverExpires')
  return formatDateTime(ts)
}

/**
 * 剩余额度展示：unlimited_quota 为 true 时忽略 remain_quota（契约明确该字段无意义）。
 */
export function formatQuota(remain: number | null | undefined, unlimited?: boolean): string {
  if (unlimited) return translate('common.value.unlimitedQuota')
  if (remain === null || remain === undefined) return EMPTY
  // 负数在契约里未被定义为「无限」，这里如实展示，避免误读为可用
  return formatNumber(remain)
}

/**
 * 「分」→「元」（数值），配合 formatCurrency 展示。
 *
 * 为什么金额用整数「分」在前后端之间传递：浮点比较会出现
 * 99.99999 < 100 这类假性未达标（分组解锁门槛正是按金额比较的），
 * 因此换算只在展示/输入的边界上做，中间一律保持整数分。与后端
 * model.FormatCents 同口径。
 */
export function centsToYuan(cents: number | null | undefined): number {
  const value = Number(cents ?? 0)
  return Number.isFinite(value) ? value / 100 : 0
}

/** 「元」→ 整数「分」；非法或负值返回 null（由调用方提示，不静默当 0）。 */
export function yuanToCents(raw: string | number): number | null {
  const value = Number(String(raw).trim())
  if (!Number.isFinite(value) || value < 0) return null
  return Math.round(value * 100)
}

/** 解析输入框里的模型列表：支持中英文逗号、空格、换行分隔 */
export function parseModelList(input: string): string[] {
  return input
    .split(/[,，\s]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

/** 模型数组 → 输入框文本（parseModelList 的反向操作） */
export function joinModelList(models: string[] | undefined | null): string {
  return Array.isArray(models) ? models.join(', ') : ''
}
