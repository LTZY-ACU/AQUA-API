/**
 * 语料共建管理接口（需登录且 role = 10）。
 *
 * 意图（Why）：
 *   「语料共建计划」的 10 个后端端点此前零前端调用（后端有前端无），
 *   本文件补齐这层封装：清单维护（upsert 语义，同名即更新）、样本浏览与
 *   JSONL 导出、规模统计、福利资格（免计费授权）的发放与撤销。
 *   类型只放本文件而不进共享 types.ts：语料域字段多且只被本域使用，
 *   挪进共享文件会污染其它页面的类型空间（也避免与并行改动冲突）。
 *
 * 流转（Flow）：
 *   app/admin/(panel)/corpus/page.tsx → 本文件 → /api/admin/corpus/*
 *   导出例外：返回的是 JSONL 二进制流（带 Content-Disposition 附件头），
 *   走不了统一 JSON 客户端，见 exportCorpusSamples 的说明。
 *
 * 扩展（Extend）：
 *   后端新增筛选维度（如按渠道）时：在 CorpusSampleQuery 加字段 +
 *   listCorpusSamples 与 exportCorpusSamples 的参数序列化两处同步
 *   （列表与导出在后端共用同一套筛选语义，前端也应保持一致）。
 */
import axios from 'axios'

import { api, getSessionToken, UPSTREAM_TIMEOUT_MS } from './client'
import type { Paged } from './types'

/* ── 类型：与后端 handler_corpus.go 的 DTO 逐字段对齐 ─────── */

/** 语料清单项（GET /api/admin/corpus/models 的 items 元素） */
export interface CorpusModel {
  /** 模型名（主键，含斜杠如 LTZY-CALL/deepseek-v4.1-flash，因此删除走请求体而非路径参数） */
  model: string
  /** 是否正在采集该模型的对话 */
  enabled: boolean
  remark: string
  /** 更新时间（Unix 秒） */
  updated_at: number
}

/** 福利资格项（GET /api/admin/corpus/grants 的 items 元素） */
export interface CorpusGrant {
  user_id: number
  username: string
  email: string
  model: string
  /** 是否免计费（本功能的默认福利形态） */
  free_access: boolean
  /** 是否生效中（后端按状态字段计算） */
  active: boolean
  remark: string
  updated_at: number
}

/** 样本列表项（正文只给 200 字预览；全文必须走 getCorpusSample，后端会写审计日志） */
export interface CorpusSample {
  id: number
  request_id: string
  user_id: number
  model: string
  upstream_model: string
  channel_id: number
  is_stream: boolean
  status_code: number
  request_bytes: number
  response_bytes: number
  truncated: boolean
  incomplete: boolean
  request_preview: string
  response_preview: string
  created_at: number
}

/** 样本详情（含请求/响应全文） */
export interface CorpusSampleDetail extends CorpusSample {
  request_body: string
  response_body: string
}

/** 语料库规模统计（GET /api/admin/corpus/stats） */
export interface CorpusStats {
  samples: number
  users: number
  request_bytes: number
  response_bytes: number
  /** 最早样本时间（Unix 秒；0 = 库里还没有样本） */
  earliest_at: number
  latest_at: number
  /** 内存快照里「正在采集」的模型数 */
  models: number
  /** 享有免计费的用户数 */
  free_users: number
}

/** 样本筛选条件（列表与导出共用，与后端 corpusSampleQueryFromContext 对齐） */
export interface CorpusSampleQuery {
  model?: string
  user_id?: number
  /** 起始时间（Unix 秒，含） */
  from?: number
  /** 截止时间（Unix 秒，含） */
  to?: number
}

/** 清单项写入请求（upsert：模型名已存在即整条覆盖，省略 enabled 时后端按 true 处理） */
export interface CorpusModelPayload {
  model: string
  enabled?: boolean
  remark?: string
}

/** 福利资格发放请求（email 与 user_id 二选一，后端优先邮箱并会校验其存在） */
export interface CorpusGrantPayload {
  email?: string
  user_id?: number
  model: string
  free_access?: boolean
  remark?: string
}

/* ── 语料清单 ─────────────────────────────────────────── */

/** GET /api/admin/corpus/models：语料清单（不分页，量级很小） */
export function listCorpusModels(): Promise<{ items: CorpusModel[] }> {
  return api.get<{ items: CorpusModel[] }>('/admin/corpus/models')
}

/** POST /api/admin/corpus/models：新增或覆盖式更新清单项（改动立即生效，无需重启） */
export function upsertCorpusModel(payload: CorpusModelPayload): Promise<{ ok: boolean; model: string; enabled: boolean }> {
  return api.post<{ ok: boolean; model: string; enabled: boolean }>('/admin/corpus/models', payload)
}

/**
 * POST /api/admin/corpus/models/delete：按模型名移除清单项。
 *
 * 为什么用 POST + 请求体而不是 DELETE /:name：模型名里可能带斜杠，
 * 放路径会被路由拆成多段（后端注释同此说明），前端照此约定。
 */
export function deleteCorpusModel(model: string): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>('/admin/corpus/models/delete', { model })
}

/* ── 福利资格 ─────────────────────────────────────────── */

/** GET /api/admin/corpus/grants：福利资格列表（后端已联查用户名与邮箱） */
export function listCorpusGrants(): Promise<{ items: CorpusGrant[] }> {
  return api.get<{ items: CorpusGrant[] }>('/admin/corpus/grants')
}

/** POST /api/admin/corpus/grants：发放/续授福利资格（后端一律置为生效中） */
export function upsertCorpusGrant(payload: CorpusGrantPayload): Promise<{ ok: boolean; user_id: number; model: string }> {
  return api.post<{ ok: boolean; user_id: number; model: string }>('/admin/corpus/grants', payload)
}

/** POST /api/admin/corpus/grants/delete：撤销福利资格（后端撤销后立刻刷新内存快照） */
export function deleteCorpusGrant(userId: number, model: string): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>('/admin/corpus/grants/delete', { user_id: userId, model })
}

/* ── 样本查看 ─────────────────────────────────────────── */

/** GET /api/admin/corpus/samples：样本列表（分页，正文只有预览） */
export function listCorpusSamples(
  query: CorpusSampleQuery & { page?: number; size?: number },
): Promise<Paged<CorpusSample>> {
  // 展开成普通对象再传：接口类型没有字符串索引签名，直接把 query 交给
  // api.get 会过不了它对 Record<string, unknown> 的参数约束
  const params: Record<string, unknown> = { ...query }
  return api.get<Paged<CorpusSample>>('/admin/corpus/samples', params)
}

/**
 * GET /api/admin/corpus/samples/{id}：样本全文。
 *
 * 这是「看用户对话原文」的唯一入口，后端会写一条查询审计
 * （谁、何时、看了哪条），界面层应把这一点明示给管理员。
 */
export function getCorpusSample(id: number): Promise<CorpusSampleDetail> {
  return api.get<CorpusSampleDetail>(`/admin/corpus/samples/${id}`)
}

/**
 * GET /api/admin/corpus/export：按当前筛选条件导出 JSONL 并触发浏览器下载。
 *
 * 为什么不走统一的 api 客户端：后端返回的不是 JSON 而是逐行流式写出的
 * JSONL 附件（Content-Disposition: attachment），统一客户端按 JSON 解析
 * 会拿到乱码；这里单独用 axios 以 blob 接收，再经 ObjectURL 落盘。
 *
 * 超时取 UPSTREAM_TIMEOUT_MS（320 秒）而不是默认 30 秒：后端是边读库边
 * 写响应、不在内存攒整批，几十万条样本导出耗时可能远超普通管理接口，
 * 前端比后端先放弃会让用户误判为「导出失败」。
 *
 * 错误提示也需特判：blob 模式下错误体同样是 Blob 而非已解析的 JSON，
 * 需要异步读出文本再解出 error.message，否则用户只会看到兜底文案。
 */
export async function exportCorpusSamples(query: CorpusSampleQuery): Promise<void> {
  const params: Record<string, unknown> = {}
  if (query.model) params.model = query.model
  if (query.user_id) params.user_id = query.user_id
  if (query.from) params.from = query.from
  if (query.to) params.to = query.to

  let blob: Blob
  let filename = `aqua-corpus-${Date.now()}.jsonl`
  try {
    const response = await axios.request<Blob>({
      url: '/admin/corpus/export',
      method: 'GET',
      baseURL: process.env.NEXT_PUBLIC_API_BASE || '/api',
      params,
      responseType: 'blob',
      timeout: UPSTREAM_TIMEOUT_MS,
      headers: { Authorization: `Bearer ${getSessionToken()}` },
    })
    blob = response.data
    // 优先用后端起的带时间戳的文件名；解析失败（如经代理剥头）再退化为本地名
    const disposition = String(response.headers?.['content-disposition'] ?? '')
    const matched = /filename="([^"]+)"/.exec(disposition)
    if (matched?.[1]) filename = matched[1]
  } catch (error) {
    throw await blobErrorToApiError(error)
  }

  // 用 <a download> 触发保存：这是纯前端落盘，无需任何后端配合
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

/** 把 blob 模式下的失败响应还原成带后端 message 的 ApiError（见 exportCorpusSamples 的说明） */
async function blobErrorToApiError(error: unknown): Promise<Error> {
  const axiosError = error as { response?: { status?: number; data?: unknown } }
  const data = axiosError?.response?.data
  if (data instanceof Blob) {
    try {
      const parsed = JSON.parse(await data.text()) as { error?: { message?: string } }
      if (parsed.error?.message) return new Error(parsed.error.message)
    } catch {
      /* 解析不出结构化错误就落到下面的通用文案 */
    }
  }
  if (!axiosError?.response) return new Error('无法连接服务器，导出失败')
  return new Error(`导出失败（HTTP ${axiosError.response.status ?? 0}）`)
}

/* ── 统计 ─────────────────────────────────────────────── */

/** GET /api/admin/corpus/stats：全库规模统计（后端未提供按模型分列的统计） */
export function fetchCorpusStats(): Promise<CorpusStats> {
  return api.get<CorpusStats>('/admin/corpus/stats')
}
