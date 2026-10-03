/**
 * 告警通道（管理员外发通道）接口。
 *
 * 意图（Why）：
 *   渠道熔断、自动停用、账号锁定这些事件此前只落在服务端日志里，
 *   站点出事了要靠主动翻日志才发现。本文件对应的后台页把这类通道显式管起来，
 *   让"站点挂了"这件事在几秒内出现在站长的手机上而不是第二天的日志里。
 *
 *   页面（/admin/alert-channels）只关心本文件的函数，不关心路径与字段名。
 *
 * 安全约定（Why 写在最前面，因为违反它的后果是凭据外泄）：
 *   1) 后端只回传 target_masked，永不回传明文目标；
 *   2) 新建/修改/删除/测试四个动作全部要求二次验证，调用方必须包在
 *      useReauthGuard().guard(...) 里，否则用户会看到一个无法继续的 403。
 *
 * 流转（Flow）：
 *   AlertChannelsPage → listAlertChannels / listAlertChannelKinds
 *                      / createAlertChannel / updateAlertChannel
 *                      / deleteAlertChannel / testAlertChannel
 *                      → /api/admin/alert-channels[...]
 *
 * 扩展（Extend）：
 *   后端新增一种通道类型时，本文件无需改动——类型与事件目录由
 *   GET /alert-channel-kinds 下发，页面从那里渲染下拉框。
 */
import { api } from './client'

/** 一条告警通道（target 已脱敏）。 */
export interface AlertChannel {
  id: number
  name: string
  kind: string
  kind_text: string
  /** 脱敏后的投递目标，例如 oapi.dingtalk.com/…/send?access_token=*** */
  target_masked: string
  /** 订阅的事件键；空数组 = 订阅全部 */
  events: string[]
  enabled: boolean
  created_at: number
  updated_at: number
}

/** 新建/修改的提交体。 */
export interface AlertChannelPayload {
  name: string
  kind: string
  /** 编辑时可留空表示沿用原值（界面上目标是脱敏的，抄不回原样） */
  target?: string
  /** 逗号分隔的事件键；留空 = 订阅全部 */
  events?: string
  enabled?: boolean
}

export interface AlertChannelListResult {
  items: AlertChannel[]
  total: number
}

/** 通道类型与事件目录，供页面渲染下拉框（避免前端硬编码一份类型表）。 */
export interface AlertChannelKinds {
  kinds: { value: string; text: string }[]
  events: { value: string; text: string }[]
}

/** GET /api/admin/alert-channels：读取全部通道（目标脱敏） */
export function listAlertChannels(): Promise<AlertChannelListResult> {
  return api.get<AlertChannelListResult>('/admin/alert-channels')
}

/** GET /api/admin/alert-channel-kinds：通道类型与事件目录 */
export function listAlertChannelKinds(): Promise<AlertChannelKinds> {
  return api.get<AlertChannelKinds>('/admin/alert-channel-kinds')
}

/** POST /api/admin/alert-channels：新建通道（需二次验证） */
export function createAlertChannel(payload: AlertChannelPayload): Promise<AlertChannel> {
  return api.post<AlertChannel>('/admin/alert-channels', payload)
}

/** PUT /api/admin/alert-channels/{id}：修改通道（需二次验证） */
export function updateAlertChannel(id: number, payload: AlertChannelPayload): Promise<AlertChannel> {
  return api.put<AlertChannel>(`/admin/alert-channels/${id}`, payload)
}

/** DELETE /api/admin/alert-channels/{id}：删除通道（需二次验证） */
export function deleteAlertChannel(id: number): Promise<unknown> {
  return api.delete<unknown>(`/admin/alert-channels/${id}`)
}

/**
 * POST /api/admin/alert-channels/{id}/test：发一条测试告警（需二次验证）。
 *
 * 同步返回成败：这里的目的就是让管理员当场确认地址可用，
 * 异步发送会让"点了没反应"与"地址错了"无法区分。
 */
export function testAlertChannel(id: number): Promise<{ ok: boolean; message?: string }> {
  return api.post<{ ok: boolean; message?: string }>(`/admin/alert-channels/${id}/test`, {})
}