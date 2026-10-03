/**
 * 模型实体管理接口（需管理员会话）。
 *
 * 意图（Why）：
 *   模型实体把"模型"从渠道上的一个字符串升级为一等实体（展示名/厂商/上下文
 *   长度/能力标签/启停状态），本文件封装其增删改查与「删除前引用统计」。
 *   引用统计单独立接口而非塞进列表的原因：模型名被令牌白名单、渠道清单、
 *   渠道映射与计价规则引用，盲目删除会让线上请求静默 404——后端 DELETE
 *   刻意不做引用拦截（是否强行删由管理员决定），因此"先看影响面再动手"
 *   的防线必须建在前端调用方，本文件把这条约定固化成函数注释。
 *   类型定义在本文件而非 types.ts：模型实体是独立域，类型与封装同文件交付，
 *   也避免与其他模块对 types.ts 的并行改动互相冲突。
 *
 * 流转（Flow）：
 *   admin/(panel)/models 页 → 本文件 → /api/admin/models*（见各函数注释的路径）
 *
 * 扩展（Extend）：
 *   后端模型实体新增字段（如模态、最大输出长度）时：在 ModelEntity 与
 *   ModelEntityPayload 同步补字段，并在 models/page.tsx 的表单弹层加输入项。
 */
import { api } from './client'

/** 模型实体（对应后端 modelMetaDTO） */
export interface ModelEntity {
  id: number
  /** 对外模型名（全局唯一；创建后不可修改——令牌/渠道/映射通过它关联） */
  name: string
  /** 展示名（留空时界面回退显示 name） */
  display_name: string
  /** 展示名兜底结果：有 display_name 时等于它，否则等于 name */
  label: string
  /** 厂商标识（如 deepseek、openai），可空 */
  vendor: string
  /** 模型说明，可空 */
  description: string
  /** 上下文长度（token）；0 表示未登记 */
  context_length: number
  /** 是否启用 */
  enabled: boolean
  /** 能力标签（如 chat/stream/tools）；后端保证序列化为 [] 而非 null */
  capabilities: string[]
  /** 创建时间（Unix 秒） */
  created_at: number
  /** 更新时间（Unix 秒） */
  updated_at: number
}

/** 列表查询条件（全部可选，与后端 GET /api/admin/models 的 query 一一对应） */
export interface ModelEntityQuery {
  page?: number
  size?: number
  /** 按模型名/展示名模糊匹配 */
  keyword?: string
  /** 按厂商精确过滤 */
  vendor?: string
  /** 启用状态过滤；不传 = 不过滤（后端仅在显式给出 true/false 时生效） */
  enabled?: boolean
}

/** 列表响应 */
export interface ModelEntityListResult {
  items: ModelEntity[]
  total: number
}

/**
 * 新建/更新请求体。
 *
 * 更新（PUT）语义与后端对齐，避免"改一项清空其他项"：
 *   - name：仅新建时提交；更新时后端禁止改名，编辑弹层不要带该字段；
 *   - display_name/vendor/description：空字符串视为「不修改」，保留原值；
 *   - context_length/enabled：显式提交才覆盖（context_length=0 表示登记为"未登记"）；
 *   - capabilities：传 [] 可清空全部能力标签（与"不传 = 不修改"区分）。
 */
export interface ModelEntityPayload {
  name?: string
  display_name?: string
  vendor?: string
  description?: string
  /** 上下文长度（token）；0 表示未登记 */
  context_length?: number
  enabled?: boolean
  /** 能力标签；更新时传 [] 表示清空 */
  capabilities?: string[]
}

/** 删除前引用统计（GET /api/admin/models/{name}/references） */
export interface ModelReferenceStats {
  /** 被统计的模型名（回显） */
  model: string
  /** 令牌白名单包含该模型的令牌数（白名单为空的令牌不算引用） */
  token_count: number
  /** 渠道模型清单包含该模型的渠道数 */
  channel_count: number
  /** 计价规则中精确指向该模型的分组数 */
  group_count: number
  /** 渠道映射中涉及该模型（对外名或上游名命中）的映射条数 */
  mapping_count: number
  /** 后端口径：token + channel + mapping（不含 group），供快速判断影响面 */
  total: number
}

/** GET /api/admin/models：模型实体列表（分页 + keyword/vendor/enabled 筛选） */
export function listModelEntities(query: ModelEntityQuery = {}): Promise<ModelEntityListResult> {
  // 展开成普通对象再传：client.ts 的 params 形参是 Record<string, unknown>，
  // interface 没有隐式索引签名，直接传类型化对象会编译报错（与 admin.ts
  // 的 listAllLogs 用 {...query} 是同一口径）
  return api.get<ModelEntityListResult>('/admin/models', { ...query })
}

/** POST /api/admin/models：新建模型实体（模型名重复时后端返回 409） */
export function createModelEntity(payload: ModelEntityPayload): Promise<ModelEntity> {
  return api.post<ModelEntity>('/admin/models', payload)
}

/** PUT /api/admin/models/{id}：更新模型实体（只提交要改的字段；不要提交 name） */
export function updateModelEntity(id: number, payload: ModelEntityPayload): Promise<ModelEntity> {
  return api.put<ModelEntity>(`/admin/models/${id}`, payload)
}

/** DELETE /api/admin/models/{id}：删除模型实体（后端不拦引用，调用前必须先展示引用统计） */
export function deleteModelEntity(id: number): Promise<unknown> {
  return api.delete<unknown>(`/admin/models/${id}`)
}

/**
 * GET /api/admin/models/{name}/references：删除前的引用统计（只读）。
 *
 * 用 encodeURIComponent 编码模型名：站点模型名统一是「厂商/模型」形式
 * （如 nvidia/nemotron-…），不编码的话斜杠会被当作路径分隔符截断，
 * 导致统计打到了一个不存在的模型名上（返回全 0，漏报影响面）。
 */
export function fetchModelReferences(name: string): Promise<ModelReferenceStats> {
  return api.get<ModelReferenceStats>(`/admin/models/${encodeURIComponent(name)}/references`)
}
