/**
 * 站点群发邮件（全站通知）接口。
 *
 * 意图（Why）：
 *   群发是不可逆操作，后端在 handler_broadcast.go 里设计了三道闸门——
 *   模板目录（限制能发什么）、预览发给自己（先看观感）、confirm 显式确认（防顺手误发）。
 *   本文件是这套契约的前端封装：类型逐字段对齐后端 DTO，视图层只管向导流程，
 *   不关心路径拼写与字段转换。
 *
 * 流转（Flow）：
 *   /admin/broadcast 页 → listBroadcastTemplates（向导第一步选模板）
 *                      → previewBroadcast（把渲染结果发到当前管理员邮箱）
 *                      → createBroadcast（confirm=true 才真正入队发送）
 *                      → listBroadcasts / listBroadcastRecipients（进度与失败明细）
 *                      → cancelBroadcast（停止未发部分，已发的不撤回）
 *
 * 扩展（Extend）：
 *   后端新增通知模板时无需改本文件（模板目录由后端动态下发）；
 *   批次/收件人新增字段时同步下方类型与后端 DTO（emailBroadcastDTO / broadcastRecipientDTO）。
 *   注意：路径不含 /api 前缀，前缀由 client.ts 的 baseURL 统一拼接。
 */
import { api } from './client'
import type { Paged } from './types'

/** 群发批次状态：排队中 → 发送中 → 已完成；期间可被停止（对齐后端 model.BroadcastStatus） */
export type BroadcastStatus = 'pending' | 'running' | 'done' | 'canceled'

/** 收件人投递状态：失败不重试，原因见明细的 error 字段（对齐后端 model.RecipientStatus*） */
export type RecipientStatus = 'pending' | 'sent' | 'failed'

/** 可选通知模板的目录项（后端 mailer.BroadcastTemplates 下发，前端不写死清单） */
export interface BroadcastTemplate {
  /** 模板键（创建与预览都传它） */
  key: string
  /** 展示名（给人看） */
  label: string
}

/**
 * 群发批次视图。
 *
 * 刻意不含正文：列表只关心进度与结果，正文是几 KB 的 HTML，
 * 拉一页列表顺带几十份正文只会让页面变慢（与后端 DTO 的取舍一致）。
 */
export interface EmailBroadcast {
  id: number
  template: string
  subject: string
  status: BroadcastStatus
  /** 收件人总数（创建时入队快照） */
  total: number
  sent: number
  failed: number
  pending: number
  created_by: number
  /** 以下时间均为 Unix 秒；契约约定 0 表示未发生（未开始/未结束） */
  created_at: number
  updated_at: number
  started_at: number
  finished_at: number
}

/** 收件人明细分项（核对"谁失败了、为什么"靠它） */
export interface BroadcastRecipient {
  id: number
  user_id: number
  email: string
  status: RecipientStatus
  /** 失败原因（仅 failed 状态有值） */
  error: string
  /** Unix 秒；0 表示尚未发送 */
  sent_at: number
}

/** GET /admin/broadcast-templates：可选通知模板目录（新增模板前端零改动） */
export function listBroadcastTemplates(): Promise<{ items: BroadcastTemplate[] }> {
  return api.get<{ items: BroadcastTemplate[] }>('/admin/broadcast-templates')
}

/**
 * POST /admin/broadcasts/preview：把渲染后的邮件发到【当前管理员自己的邮箱】。
 *
 * 后端刻意不支持任意收件人——那会让预览变成"用什么地址都能发的发信器"，
 * 因此这里没有收件人参数，只有模板键。
 */
export function previewBroadcast(template: string): Promise<{ subject: string; to: string }> {
  return api.post<{ subject: string; to: string }>('/admin/broadcasts/preview', { template })
}

/**
 * POST /admin/broadcasts：创建批次并立即开始后台发送。
 *
 * confirm 是后端的防误发闸门（false 直接 400）：
 * 调用方必须已经过界面上的显式二次确认才传 true，
 * 这也是 payload 保留 confirm 字段而不是封装内部写死的原因——把"已确认"表达在调用点。
 */
export function createBroadcast(payload: { template: string; confirm: boolean }): Promise<EmailBroadcast> {
  return api.post<EmailBroadcast>('/admin/broadcasts', payload)
}

/** GET /admin/broadcasts：群发批次分页列表（后端按创建时间倒序，最新批次在第 1 页） */
export function listBroadcasts(query: { page?: number; size?: number } = {}): Promise<Paged<EmailBroadcast>> {
  return api.get<Paged<EmailBroadcast>>('/admin/broadcasts', { ...query })
}

/** GET /admin/broadcasts/{id}/recipients：收件人明细分页列表（可按投递状态过滤） */
export function listBroadcastRecipients(
  id: number,
  query: { status?: RecipientStatus; page?: number; size?: number } = {},
): Promise<Paged<BroadcastRecipient>> {
  return api.get<Paged<BroadcastRecipient>>(`/admin/broadcasts/${id}/recipients`, { ...query })
}

/**
 * POST /admin/broadcasts/{id}/cancel：停止一个进行中的批次。
 *
 * 语义是"未发的人不再发"，已发出的邮件无法撤回——
 * 这正是发送前必须预览的根本原因。
 */
export function cancelBroadcast(id: number): Promise<EmailBroadcast> {
  return api.post<EmailBroadcast>(`/admin/broadcasts/${id}/cancel`)
}
