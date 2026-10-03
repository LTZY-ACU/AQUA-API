/**
 * 公开的 OpenAPI 规范类型（与 internal/openapi 的 Document 对应）。
 *
 * 意图（Why）：
 *   `/docs` 页面的数据源是服务端吐出的 `/openapi.json`，而这份 JSON 由
 *   Go 结构体生成。前端若自己再写一份类型，两边就会各自漂移——
 *   后端加了字段，前端类型没跟上，页面渲染成 undefined 却没有任何报错。
 *   因此这里只声明【渲染实际需要的那部分】，其余字段一律允许额外键。
 *
 * 为什么不全量声明：规范里绝大部分字段（examples、tags、callbacks…）
 * 这个页面根本不读。全量声明等于把后端的结构变更全部转成前端编译错误，
 * 而多数变更并不影响渲染——那是纯粹的摩擦。
 */
export interface OpenAPIDocument {
  openapi: string
  info: {
    title: string
    description: string
    version: string
  }
  servers: { url: string; description?: string }[]
  paths: Record<string, OpenAPIPathItem>
  components: {
    schemas: Record<string, OpenAPISchema>
    securitySchemes: Record<string, unknown>
  }
  security?: Record<string, string[]>[]
  tags?: { name: string; description?: string }[]
}

/** 一个路径下的可用操作。 */
export interface OpenAPIPathItem {
  get?: OpenAPIOperation
  post?: OpenAPIOperation
}

/** 一个操作。 */
export interface OpenAPIOperation {
  tags: string[]
  summary: string
  description?: string
  operationId: string
  /** 存在且为空数组 = 该端点免鉴权。 */
  security?: Record<string, string[]>[]
  parameters?: OpenAPIParameter[]
  requestBody?: OpenAPIRequestBody
  responses: Record<string, OpenAPIResponse>
}

/** 一个参数。 */
export interface OpenAPIParameter {
  name: string
  in: string
  description?: string
  required?: boolean
  example?: string
  schema?: OpenAPISchema
}

/** 请求体。 */
export interface OpenAPIRequestBody {
  description?: string
  required?: boolean
  content: Record<string, { schema?: OpenAPISchema }>
}

/** 一个响应。 */
export interface OpenAPIResponse {
  description: string
  headers?: Record<string, { description: string }>
  content?: Record<string, { schema?: OpenAPISchema }>
}

/** 一个 schema（本页面只渲染类型、必填、枚举与说明）。 */
export interface OpenAPISchema {
  type?: string | string[]
  description?: string
  properties?: Record<string, OpenAPISchema>
  required?: string[]
  items?: OpenAPISchema
  enum?: string[]
  additionalProperties?: unknown
  $ref?: string
}

/**
 * 拉取 OpenAPI 规范。
 *
 * 为什么不用 `api.get`：那个客户端会自动带会话 Cookie 并在 401 时跳登录，
 * 而 `/openapi.json` 是公开端点。用它反而会在未登录时把文档页变成一次重定向，
 * 让访客以为"文档需要登录才能看"。
 */
export async function fetchOpenAPISpec(signal?: AbortSignal): Promise<OpenAPIDocument> {
  const res = await fetch('/openapi.json', {
    signal,
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`加载接口文档失败（HTTP ${res.status}）`)
  }
  return (await res.json()) as OpenAPIDocument
}