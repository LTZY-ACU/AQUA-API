/**
 * 展示语义辅助：状态码/状态值 → 中文文案与徽标配色。
 *
 * 意图（Why）：
 *   「启用/禁用」「成功/失败」这类判断散落在各表格里极易出现口径不一
 *   （例如渠道启用显示绿色、用户启用显示灰色）。集中在此统一口径。
 *
 * 流转（Flow）：
 *   .vue 表格 / 徽标组件 → 本文件 → 文案 + 样式类名
 *
 * 扩展（Extend）：
 *   契约若补充渠道类型枚举（当前只示例 type=1），请更新 CHANNEL_TYPE_LABEL。
 */
import { translate } from '@/i18n'

/** 状态值语义（契约：渠道/令牌 status=1 启用；用户 status：1 启用 / 2 禁用） */
export const STATUS_ENABLED = 1
export const STATUS_DISABLED = 2

/** 通用启用状态 → 文案（走 i18n；供无 status_text 字段时兜底，可传自定义文案覆盖） */
export function statusLabel(status: number | undefined, enabled?: string, disabled?: string): string {
  return status === STATUS_ENABLED
    ? (enabled ?? translate('common.state.enabled'))
    : (disabled ?? translate('common.state.disabled'))
}

/** 通用启用状态 → 徽标样式类（style.css 中定义的 .badge-*） */
export function statusBadgeClass(status: number | undefined): string {
  return status === STATUS_ENABLED ? 'badge badge-ok' : 'badge badge-off'
}

/** 渠道状态编号（与后端 model.ChannelStatus 一一对应） */
export const CHANNEL_STATUS_ENABLED = 1
export const CHANNEL_STATUS_DISABLED = 2
export const CHANNEL_STATUS_AUTO_DISABLED = 3

/**
 * 渠道状态 → 文案（三态，走 i18n）。
 *
 * 为什么必须集中在这里：渠道有三个状态（启用 / 手动停用 / 自动停用），
 * 而通用的 statusLabel 只有两态。此前各页各写一份，导致同一个渠道
 * 在渠道管理页显示"自动停用"、在模型映射页显示"停用"——
 * 后者会让站长以为是自己关的，从而找错原因。
 */
export function channelStatusLabel(status: number | undefined): string {
  switch (status) {
    case CHANNEL_STATUS_ENABLED:
      return translate('components.channelStatus.enabled')
    case CHANNEL_STATUS_DISABLED:
      return translate('components.channelStatus.manualDisabled')
    case CHANNEL_STATUS_AUTO_DISABLED:
      return translate('components.channelStatus.autoDisabled')
    default:
      return status
        ? translate('components.channelStatus.unknownCode', { code: status })
        : translate('components.channelStatus.unknown')
  }
}

/** 渠道状态 → 徽标样式类：启用绿、自动停用黄（系统判定）、手动停用灰 */
export function channelStatusBadgeClass(status: number | undefined): string {
  switch (status) {
    case CHANNEL_STATUS_ENABLED:
      return 'badge badge-ok'
    case CHANNEL_STATUS_AUTO_DISABLED:
      return 'badge badge-warn'
    default:
      return 'badge badge-off'
  }
}

/** HTTP 状态码 → 徽标样式类：2xx 绿、4xx 黄、5xx 红、其它灰 */
export function httpStatusBadgeClass(code: number | undefined): string {
  if (!code) return 'badge badge-off'
  if (code >= 200 && code < 300) return 'badge badge-ok'
  if (code >= 400 && code < 500) return 'badge badge-warn'
  if (code >= 500) return 'badge badge-err'
  return 'badge badge-off'
}

/**
 * 渠道类型编号 → 文案。
 *
 * 注意：契约只给出了示例值 type=1，未定义完整枚举。
 * 这里只对「1 = OpenAI 兼容」做确定性标注（依据契约示例中的 base_url 为 api.openai.com），
 * 其它数值一律显示为「类型 N」，避免臆造映射导致误配。
 */
export function channelTypeLabel(type: number | undefined): string {
  if (type === 1) return translate('components.channelType.openaiCompatible')
  return type ? translate('components.channelType.generic', { type }) : translate('components.channelType.unknown')
}

/** 角色编号 → 文案（契约 1.5，走 i18n） */
export function roleLabel(role: number | undefined): string {
  if (role === 10) return translate('components.userRole.admin')
  if (role === 1) return translate('components.userRole.user')
  return role ? translate('components.userRole.code', { role }) : translate('components.userRole.unknown')
}

/** 角色编号 → 徽标样式类：管理员用品牌色强调，普通用户用中性色 */
export function roleBadgeClass(role: number | undefined): string {
  return role === 10 ? 'badge badge-info' : 'badge badge-off'
}

/** 数字签名辅助：模型列表为空表示「支持全部模型」（契约 M2 过渡约定） */
export function modelsSummary(models: string[] | undefined | null): string {
  if (!models || models.length === 0) return translate('components.modelsSummary.all')
  return translate('components.modelsSummary.count', { count: models.length })
}
