// Package openapi 用代码描述本网关的 OpenAPI 3.1 规范。
//
// 意图（Why）：
//
//	中转站的使用者不是"打开文档照着抄"的那类人——他们要把现成的客户端
//	（Cline / Cherry Studio / Cursor / 自己的脚本）指过来。第一件事就是：
//	"这个站的 base_url 填哪、鉴权头长什么样、有哪些端点、错了会返回什么"。
//	没有这份文档，每个使用者都要靠猜 + 试错，而试错的代价是真金白银的调用。
//
// 为什么用【代码】描述规范，而不是提交一份 openapi.yaml：
//
//  1. 路径与错误码必须与实现同源。本包直接引用 oai.Code* 常量与
//     middleware.TraceHeader，改了实现忘记同步文档的概率因此显著降低；
//     手工维护的 yaml 必然随时间腐烂，且腐烂时没有任何报错。
//  2. 规范里最容易被写错的是错误响应的形状。本包复用 oai.ErrorBody 的
//     字段名（message / type / code），写错会在下面的测试里被抓住。
//
// 有意不覆盖的部分（Why 写清，避免被当成遗漏）：
//
//	后台管理接口（/api/admin/*）不进规范。它们由本站自用，暴露给文档
//	只会诱使阅读者把"后台能做的事"当成"普通用户能做的事"，反而误导。
//	用户侧门户接口（/api/user/*、/api/auth/*）同理——它们服务于浏览器
//	会话而非程序化调用，写进 API 文档会让文档被前端内部实现绑架。
//	因此本规范只描述【持有访问令牌的程序】该用的那组端点，外加两个不需要
//	令牌的运维端点（采集器与排障者要用，而他们手里没有用户凭据）。
//
// 流转（Flow）：
//
//	openapi.Spec() → internal/server/handler_openapi.go
//	  → GET /openapi.json   （机器可读：给 SDK 生成器、给 Postman 导入）
//	  → 前端 /docs 页面的数据源（人可读）
//
// 扩展（Extend）：
//
//	新增对外端点时在 paths() 里加一条，并补上对应的 schema 引用；
//	加新的错误码时同步 ErrorDetail 的枚举，否则该错误在文档里不存在，
//	使用者只能靠猜——这正是文档最大的价值所在，所以宁可啰嗦也不要漏。
package openapi

import (
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/server/middleware"
	"github.com/LTZY-ACU/ltzy-api/internal/version"
)

// specVersion 是本规范遵循的 OpenAPI 版本。
//
// 为什么是 3.1 而不是 3.0：3.1 是 JSON Schema 2020-12 的超集，
// 意味着 schema 可以直接用 if/then、type: [string, "null"] 这类表达，
// 而 3.0 的 schema 子集不支持。加上 Swagger UI / Redoc / openapi-typescript
// 都已支持 3.1，没有向下兼容的理由。
const specVersion = "3.1.0"

// basePath 是对外 API 的路径前缀。
//
// 单一常量而非散落各处的字面量：base_url 是使用者最先要填的一项，
// 而它拼错时的表现是"客户端连不上，报一个跟鉴权无关的错"，极难自查。
const basePath = "/v1"

// geminiPath 是 Gemini 原生协议的前缀。
//
// 与 OpenAI 官方一致（官方就是 /v1beta），**故意不挂在 /v1 下**：
// 统一前缀看着整齐，但会让从官方文档迁移过来的人以为写错了。
const geminiPath = "/v1beta"

// originPlaceholder 是 servers 里的占位域名。
//
// 为什么不给一个假域名而是显式写"你的域名"：使用者复制粘贴后如果忘了改，
// 会得到 DNS 解析失败——这个报错至少指向"地址不对"，
// 而一个看起来很真的假域名会让人怀疑是本站服务挂了。
const originPlaceholder = "https://你的域名"

// Document 是 OpenAPI 文档的根结构。
//
// 为什么手写结构体而不用 map[string]any：
//
//	map 版本的规范没有任何编译期保证——字段名拼错、类型写反都要等到
//	运行时序列化后才可能被发现（而序列化本身不会失败，只是产出坏 JSON）。
//	结构体把这些错误挪到了编译期。代价是新增顶层字段要改类型定义，
//	但顶层字段极少变，这个代价值得付。
type Document struct {
	OpenAPI string              `json:"openapi"`
	Info    Info                `json:"info"`
	Servers []Server            `json:"servers"`
	Paths   map[string]PathItem `json:"paths"`
	// Components 存放可复用的 schema 与安全方案。
	Components Components `json:"components"`
	// Security 是全局默认鉴权；个别公开端点在自己的 Operation 里覆盖为 nil。
	Security []map[string][]string `json:"security"`
	Tags     []Tag                 `json:"tags,omitempty"`
}

// Info 是文档元信息。
type Info struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// Server 是一个候选服务地址。
type Server struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Tag 是标签分组，用于文档侧栏归类。
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PathItem 是一个路径下的全部操作。
type PathItem struct {
	Get  *Operation `json:"get,omitempty"`
	Post *Operation `json:"post,omitempty"`
}

// Operation 是一个操作。
type Operation struct {
	Tags        []string `json:"tags"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	OperationID string   `json:"operationId"`
	// Security 覆盖全局默认；nil 表示沿用全局。
	Security    []map[string][]string `json:"security,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[string]Response   `json:"responses"`
}

// Parameter 是一个路径 / 查询参数。
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
	Example     string  `json:"example,omitempty"`
}

// RequestBody 描述请求体。
type RequestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content"`
}

// MediaType 是某个媒体类型的请求 / 响应体。
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Response 是一个响应。
type Response struct {
	Description string               `json:"description"`
	Headers     map[string]Header    `json:"headers,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Header 描述一个响应头。
type Header struct {
	Description string  `json:"description"`
	Schema      *Schema `json:"schema,omitempty"`
}

// Components 存放可复用的 schema 与安全方案。
type Components struct {
	Schemas         map[string]*Schema        `json:"schemas"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes"`
}

// SecurityScheme 描述一种鉴权方式。
type SecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	In           string `json:"in,omitempty"`
	Name         string `json:"name,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	Description  string `json:"description,omitempty"`
}

// Schema 是一个 JSON Schema 子集。
//
// 刻意只支持 OpenAPI 里实际用得到的组合（type / properties / required /
// enum / items / 附加说明），而不是引入完整的 JSON Schema 实现：
// 规范只需要能被人和工具读懂，不需要能校验任意文档。
type Schema struct {
	// Type 用 any 而非 string：OpenAPI 3.1 允许 `"type": ["string", "null"]`
	// 这种联合类型（这是 3.1 相对 3.0 的关键增益之一）。
	// 把它约束成 string 就只能靠 nullable 绕，而 nullable 在 3.1 里已废弃。
	// 合法取值：单个类型名（"string"）或类型名数组（[]string{"string","array"}）。
	Type        any                `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	// AdditionalProperties 表达"其余字段透传给上游"。
	// 布尔 true 表示允许任意键（本项目大量透传字段的写法）。
	AdditionalProperties any `json:"additionalProperties,omitempty"`
	// Example 给出一个可直接使用的样例。
	Example any `json:"example,omitempty"`
	// Ref 指向 components.schemas 中的某个名字。
	Ref string `json:"$ref,omitempty"`
}

// ref 构造一个 schema 引用。
func ref(name string) *Schema { return &Schema{Ref: "#/components/schemas/" + name} }

// obj 快捷构造一个对象 schema。
func obj(props map[string]*Schema, required ...string) *Schema {
	return &Schema{Type: "object", Properties: props, Required: required}
}

// str 快捷构造一个字符串 schema。
func str(desc string) *Schema { return &Schema{Type: "string", Description: desc} }

// integer 快捷构造一个整型 schema。
func integer(desc string) *Schema { return &Schema{Type: "integer", Description: desc} }

// number 快捷构造一个数值 schema。
func number(desc string) *Schema { return &Schema{Type: "number", Description: desc} }

// boolean 快捷构造一个布尔 schema。
func boolean(desc string) *Schema { return &Schema{Type: "boolean", Description: desc} }

// passthrough 在既有对象 schema 上追加"允许任意额外字段"。
func passthrough(s *Schema) *Schema {
	s.AdditionalProperties = true
	return s
}

// Spec 返回完整的 OpenAPI 文档。
//
// 版本号取自编译期注入的 version.Version（默认 "dev"）。
// 用它而不是手写常量：文档里的版本必须与二进制一致，否则使用者拿文档
// 对不上行为时的排查成本极高。
func Spec() Document {
	return Document{
		OpenAPI: specVersion,
		Info: Info{
			Title:   "AQUA-API 接口文档",
			Version: version.Get().Version,
			Description: "AQUA-API 是 LLM API 中转网关。持有访问令牌（API Key）的程序" +
				"把 `base_url` 指向本站 + 填入令牌，即可使用 OpenAI / Anthropic / Gemini 的原生协议。\n\n" +
				"**三种协议共用同一个域名**：客户端配置里只需改一处 base_url，" +
				"`/v1/chat/completions`、`/v1/messages`、`/v1beta/models/{model}:generateContent` " +
				"都已挂好。\n\n" +
				"所有 `/v1/*` 端点共用同一套令牌鉴权、额度扣减与失败重试，" +
				"不需要为不同协议学习不同的凭据机制。",
		},
		Servers: []Server{
			{URL: originPlaceholder, Description: "把域名换成你自己的；路径部分见下方各端点"},
			{URL: "/", Description: "同源相对路径（本站前后端同域部署时用这个）"},
		},
		Tags: []Tag{
			{Name: "对话", Description: "文本生成的主流入口，兼容 OpenAI 协议"},
			{Name: "向量", Description: "嵌入类模型（不少免费上游只提供这些端点）"},
			{Name: "多媒体", Description: "图像与语音的生成、转写、翻译"},
			{Name: "异步任务", Description: "耗时较长的生成任务：提交后凭任务号轮询取结果"},
			{Name: "其他协议", Description: "OpenAI Responses / Anthropic / Gemini 原生协议"},
			{Name: "运维", Description: "健康检查与指标，**不需要令牌**"},
		},
		Paths:      paths(),
		Components: components(),
		// 全局默认鉴权：绝大多数端点都要令牌。
		Security: []map[string][]string{{"bearerAuth": {}}},
	}
}

// paths 返回全部对外端点。
//
// 键必须是**完整路径**（含 /v1 前缀）：OpenAPI 的 servers 只提供 origin，
// 路径部分一律写在这里。写成 "/models" 会让工具生成出指向站点根目录的错误地址。
func paths() map[string]PathItem {
	return map[string]PathItem{
		basePath + "/models": {
			Get: op("对话", "模型清单", "listModels",
				"返回当前令牌【实际可用】的模型列表。\n\n"+
					"它不是站点宣传的全量清单，而是过滤后的结果：令牌被限制了模型白名单时，"+
					"拿不到无权调用的模型。因此用它渲染客户端的模型下拉是准确的。",
				nil, nil, listModelsResponses()),
		},
		basePath + "/chat/completions": {
			Post: op("对话", "对话补全（Chat Completions）", "createChatCompletion",
				"OpenAI 兼容的对话补全，支持流式（`stream: true`）与非流式两种模式。\n\n"+
					"本站在标准字段之外额外接受 `thinking`（深度思考）、`reasoning_effort`"+
					"（推理强度）、`web_search`（联网搜索）；命中的模型若不支持这些字段，"+
					"会原样忽略而不会报错——这与 OpenAI 官方对未知字段的宽松处理一致，"+
					"好处是同一段代码可以切模型，代价是写错字段名不会立刻被发现。",
				nil, jsonBody("请求体，字段与 OpenAI 官方一致", chatRequestSchema()),
				chatResponses()),
		},
		basePath + "/embeddings": {
			Post: op("向量", "向量嵌入（Embeddings）", "createEmbedding",
				"生成文本向量。\n\n"+
					"为什么单独提供：不少免费上游（如 NVIDIA 的 embedding / rerank / clip 模型）"+
					"只提供这个端点，对 `/v1/chat/completions` 一律返回 404。缺了它，"+
					"这些模型在清单里就等于「上架了但调不通」。",
				nil, jsonBody("请求体，字段与 OpenAI 官方一致", embeddingRequestSchema()),
				jsonResponse("向量数组，`usage` 字段给出计费信息。")),
		},
		basePath + "/images/generations": {
			Post: op("多媒体", "图像生成", "createImage",
				"文生图 / 图生图。响应含 `data[].url`：本站默认不落地图片，直接透传上游地址，"+
					"**该地址通常带有效期，请及时下载**。",
				nil, jsonBody("请求体，字段与 OpenAI 官方一致", imageRequestSchema()),
				jsonResponse("图像结果列表。")),
		},
		basePath + "/audio/speech": {
			Post: op("多媒体", "语音合成（TTS）", "createSpeech",
				"文本转语音。**响应是二进制音频流而非 JSON**，"+
					"请按响应头给出的 Content-Type 保存，不要去解析 JSON。",
				nil, jsonBody("请求体，字段与 OpenAI 官方一致", speechRequestSchema()),
				map[string]Response{
					"200": {
						Description: "音频二进制流",
						Headers:     traceHeaders(),
						Content: map[string]MediaType{
							"audio/mpeg": {Schema: &Schema{Type: "string", Description: "音频字节流"}},
						},
					},
					"401": errResponse("令牌缺失或无效", oai.CodeInvalidAPIKey),
				}),
		},
		basePath + "/audio/transcriptions": {
			Post: op("多媒体", "语音转写（ASR）", "createTranscription",
				"音频转文本。**请求体是 `multipart/form-data` 而非 JSON**——"+
					"直接把 JSON 发过来会得到 400，这是最常见的接入错误之一。",
				nil, formBody("表单数据，`file` 为音频文件，其余为 OpenAI 官方同名字段",
					transcriptionFormSchema()),
				jsonResponse("转写文本，视 `response_format` 而定（json / text / srt / vtt）。")),
		},
		basePath + "/audio/translations": {
			Post: op("多媒体", "音频翻译", "createTranslation",
				"外语音频翻译成英文文本。与转写同样使用 `multipart/form-data`。",
				nil, formBody("表单数据，`file` 为外语音频", transcriptionFormSchema()),
				jsonResponse("英文转写文本。")),
		},
		basePath + "/messages": {
			Post: op("其他协议", "Anthropic Messages", "createAnthropicMessage",
				"Anthropic 原生协议。Claude 官方 SDK、Claude Code 默认走这个端点。\n\n"+
					"网关内部把请求转成 OpenAI 格式发给上游，再把响应转回 Anthropic 格式；"+
					"**流式响应的 SSE 事件名也做了相应还原**，所以官方 SDK 可以直接用。\n\n"+
					"从 OpenAI 协议迁过来最容易错的一处：`system` 提示词在 Anthropic 协议里"+
					"位于顶层，而不在 messages 数组内。",
				nil, jsonBody("请求体，字段与 Anthropic 官方一致", anthropicRequestSchema()),
				jsonResponse("Anthropic 格式的响应；`stream: true` 时为 SSE。")),
		},
		basePath + "/responses": {
			Post: op("其他协议", "响应生成（Responses）", "createResponse",
				"OpenAI Responses 协议。Codex 系订阅账号的上游只认这个协议，"+
					"官方 Codex CLI 与新版 SDK 也以它为主入口。\n\n"+
					"客户端说 Responses 而上游也是 Responses 时，网关原样直通、不做任何转写。",
				nil, jsonBody("请求体，字段与 OpenAI 官方 Responses 协议一致", responsesRequestSchema()),
				jsonResponse("流式响应为 `text/event-stream`；非流式为完整 JSON 对象。")),
		},
		// 路径刻意写成 /models/{model}:{action} 而不是 {model}：
		// 真实路由是通配段 /v1beta/models/*action（模型名与动作名都在那一段里，
		// 由适配器自行解析）。文档里如实写出冒号形式，使用者照着拼 URL 才对得上。
		geminiPath + "/models/{model}:{action}": {
			Post: op("其他协议", "Gemini 生成（原生协议）", "geminiGenerateContent",
				"Gemini 原生协议。模型名与动作都写在路径里，实际有两处：\n\n"+
					"`POST /v1beta/models/{model}:generateContent`\n"+
					"`POST /v1beta/models/{model}:streamGenerateContent`（流式）\n\n"+
					"本站沿用官方的 `/v1beta` 前缀（**不是** `/v1`）——统一前缀看着整齐，"+
					"但会让从官方文档迁来的人以为写错了。\n\n"+
					"鉴权两种写法都接受：`Authorization: Bearer sk-xxx` 或 "+
					"`x-goog-api-key: sk-xxx`（Google SDK 的默认方式）。",
				[]Parameter{
					pathParam("model", "模型名", "gemini-2.5-pro"),
					pathParam("action", "动作名：`generateContent`（非流式）或 "+
						"`streamGenerateContent`（流式）", "generateContent"),
				}, jsonBody("Gemini 原生协议的 `GenerateContentRequest`", geminiRequestSchema()),
				jsonResponse("Gemini 原生格式的响应")),
		},
		basePath + "/tasks": {
			Post: op("异步任务", "提交任务", "createTask",
				"提交图像 / 视频 / 音乐类生成任务。与对话接口共用同一套令牌鉴权与额度体系"+
					"（**任务在提交时即扣费**，失败或取消会自动退还），因此不需要学习第二套凭据机制。\n\n"+
					"提交后用返回的 `task_ref` 轮询 `GET /v1/tasks/{task_ref}` 取结果。",
				nil, jsonBody("任务描述", taskRequestSchema()),
				jsonResponse("任务对象，含 `task_ref`。")),
			Get: op("异步任务", "任务列表", "listTasks",
				"列出当前令牌的异步任务。",
				[]Parameter{
					queryParam("status", "按状态过滤：1 排�� / 2 进行中 / 3 完成 / 4 失败 / 5 取消", "3"),
					queryParam("limit", "单页条数，默认 20", "20"),
					queryParam("offset", "偏移量，默认 0", "0"),
				}, nil, jsonResponse("任务数组。")),
		},
		basePath + "/tasks/{ref}": {
			Get: op("异步任务", "查询单个任务", "getTask",
				"凭任务号查询任务状态与结果。**建议轮询间隔不低于 3 秒**"+
					"（多数上游对高频轮询会计入限流）。",
				[]Parameter{
					pathParam("ref", "任务号（提交任务时返回的 `task_ref`）", "tsk_xxxxxxxx"),
				}, nil, map[string]Response{
					"200": {
						Description: "任务对象；`status` 为 3 时 `result_url` / `result_data` 才有值",
						Headers:     traceHeaders(),
						Content: map[string]MediaType{
							"application/json": {Schema: ref("Task")},
						},
					},
					"401": errResponse("令牌缺失或无效", oai.CodeInvalidAPIKey),
					"404": errResponse("任务不存在或不属于当前令牌", "task_not_found"),
				}),
		},

		// ── 运维端点：路径不在 /v1 下，且不需要令牌 ──
		//
		// 放进规范而不是只写在 README：使用者排查"服务是不是挂了"时的第一动作
		// 就是打这个端点，写清楚能省掉一轮"到底该打哪个路径"的问答。
		"/healthz": {
			Get: publicOp("运维", "健康检查", "healthCheck",
				"返回数据库与进程状态。**不需要令牌**——采集器不应持有用户凭据。\n\n"+
					"`status` 为 `ok` 表示可正常提供服务；`degraded` 表示某项依赖异常，"+
					"此时负载均衡器应把本站摘除流量。",
				jsonResponse("含 status / database / migration_version / version / uptime_seconds")),
		},
		"/metrics": {
			Get: publicOp("运维", "Prometheus 指标", "getMetrics",
				"Prometheus 文本格式的指标。**不需要令牌**（令牌由 `AQUA_METRICS_TOKEN` 控制，"+
					"留空表示不鉴权，便于内网采集）。\n\n"+
					"含请求量 / 延迟分位 / 在途数 / 告警投递统计，以及进程运行时长与构建信息。",
				map[string]Response{
					"200": {
						Description: "Prometheus 文本协议输出",
						Content: map[string]MediaType{
							"text/plain": {Schema: ref("MetricsEndpoint")},
						},
					},
				}),
		},
	}
}

// ── 操作构造 helper ──────────────────────────────────────────────────────

// op 构造一个需要令牌的操作。
func op(tag, summary, id, description string, params []Parameter, body *RequestBody,
	resp map[string]Response) *Operation {
	return &Operation{
		Tags:        []string{tag},
		Summary:     summary,
		Description: description,
		OperationID: id,
		Parameters:  params,
		RequestBody: body,
		Responses:   resp,
	}
}

// publicOp 构造一个不需要令牌的操作。
//
// 为什么要显式把 Security 置空而不是"什么都不写"：OpenAPI 里 security 是
// **继承**的，全局设了 bearerAuth 之后，不显式覆盖就等于要求令牌。
// 于是 /healthz 会在文档里显示"需要 Bearer"——而它明明是公开的，
// 这种错误会让人以为要传令牌才能探活，白排查很久。
func publicOp(tag, summary, id, description string, resp map[string]Response) *Operation {
	o := op(tag, summary, id, description, nil, nil, resp)
	o.Security = []map[string][]string{}
	return o
}

// pathParam 构造一个路径参数。
func pathParam(name, desc, example string) Parameter {
	return Parameter{Name: name, In: "path", Description: desc, Required: true,
		Schema: &Schema{Type: "string"}, Example: example}
}

// queryParam 构造一个查询参数。
func queryParam(name, desc, example string) Parameter {
	return Parameter{Name: name, In: "query", Description: desc,
		Schema: &Schema{Type: "string"}, Example: example}
}

// jsonBody 构造一个 application/json 请求体。
func jsonBody(desc string, schema *Schema) *RequestBody {
	return &RequestBody{
		Description: desc,
		Required:    true,
		Content:     map[string]MediaType{"application/json": {Schema: schema}},
	}
}

// formBody 构造一个 multipart/form-data 请求体。
func formBody(desc string, schema *Schema) *RequestBody {
	return &RequestBody{
		Description: desc,
		Required:    true,
		Content:     map[string]MediaType{"multipart/form-data": {Schema: schema}},
	}
}

// jsonResponse 构造一个 200 application/json 响应。
func jsonResponse(desc string) map[string]Response {
	return map[string]Response{
		"200": {Description: desc, Content: map[string]MediaType{
			"application/json": {Schema: &Schema{Type: "object", AdditionalProperties: true}},
		}},
	}
}

// listModelsResponses 返回模型清单的完整响应集。
func listModelsResponses() map[string]Response {
	return map[string]Response{
		"200": {
			Description: "模型清单（已按令牌白名单过滤）",
			Headers:     traceHeaders(),
			Content: map[string]MediaType{
				"application/json": {Schema: ref("ModelList")},
			},
		},
		"401": errResponse("令牌缺失或无效", oai.CodeInvalidAPIKey),
	}
}

// chatResponses 返回对话接口的完整响应集。
//
// 为什么把流式与非流式分开列：两者 Content-Type 不同（后者是 text/event-stream），
// 合在一处会让 SDK 生成器产出错误的客户端代码——这是文档最容易骗人的地方。
//
// 为什么 429 与 529 分开：两者的处置动作不同。429 是"额度或速率不够"，
// 该充值或该退避；529 是"上游暂时挂了"，钱没问题，退避重试即可。
// 合并成一条等于让使用者自己猜。
func chatResponses() map[string]Response {
	return map[string]Response{
		"200": {
			Description: "`stream: false` 返回 JSON；`stream: true` 返回 SSE 流（`text/event-stream`）",
			Headers:     traceHeaders(),
			Content: map[string]MediaType{
				"application/json":  {Schema: ref("ChatCompletion")},
				"text/event-stream": {Schema: &Schema{Type: "string", Description: "SSE 事件流"}},
			},
		},
		"400": errResponse("请求体格式错误、缺少 model，或请求体超过 32 MiB", oai.CodeInvalidJSON),
		"401": errResponse("令牌缺失、无效、已禁用或已过期", oai.CodeInvalidAPIKey),
		"403": errResponse("模型不在令牌白名单内", oai.CodeModelNotAllowed),
		"413": errResponse("请求体超过 32 MiB 上限", oai.CodeRequestTooLarge),
		"429": errResponse("额度不足，或触发分组 RPM 限流", oai.CodeInsufficientQuota),
		"502": errResponse("上游请求失败（重试后仍失败）", oai.CodeUpstreamRequestFailed),
		"503": errResponse("没有可用渠道（可能全部被自动停用或达上限流）", oai.CodeNoAvailableChannel),
		"529": errResponse("上游服务不可用或过载，客户端应退避重试", oai.CodeUpstreamUnavailable),
	}
}

// errResponse 构造一个错误响应。
//
// code 参数在这里只用于让调用点的意图显式化（错误码以枚举形式集中在
// ErrorDetail 里，避免同一个码在多处文档里写法不一）。真正决定客户端
// 行为的是响应体里的 error.code，故此处不再重复写一遍。
func errResponse(desc, code string) Response {
	_ = code
	return Response{
		Description: desc,
		Content: map[string]MediaType{
			"application/json": {Schema: ref("Error")},
		},
	}
}

// traceHeaders 返回所有端点都会带上的响应头说明。
//
// 为什么要写进文档：X-Request-Id 是使用者报障时唯一能给我们的定位凭据。
// 不告诉别人它存在，报障时就只能截一张截图，我们也就无从查起。
func traceHeaders() map[string]Header {
	return map[string]Header{
		middleware.TraceHeader: {
			Description: "本次请求的追踪 ID。**报障时请一并提供**——" +
				"我们用它在校调用日志里精确定位到这一次调用（含它命中的渠道与计费明细）。",
			Schema: &Schema{Type: "string"},
		},
	}
}

// ── Schema 定义 ──────────────────────────────────────────────────────────

// components 返回可复用的 schema 与安全方案。
func components() Components {
	return Components{
		Schemas: schemas(),
		SecuritySchemes: map[string]SecurityScheme{
			"bearerAuth": {
				Type:         "http",
				Scheme:       "bearer",
				BearerFormat: "API Key",
				Description: "访问令牌。在「控制台 → 令牌管理」创建，形如 `sk-...`。\n\n" +
					"传递方式二选一：\n\n" +
					"1. `Authorization: Bearer sk-xxx` —— OpenAI SDK 的默认方式\n" +
					"2. `api-key: sk-xxx` —— Azure OpenAI SDK 的默认方式\n\n" +
					"令牌可被限制：绑定分组（决定能调哪些模型与折扣）、限定模型白名单、" +
					"设置过期时间与 RPM 上限。**令牌泄露请立刻在控制台禁用**——" +
					"它同时也是计费凭证。",
			},
			"googleApiKey": {
				Type: "apiKey",
				In:   "header",
				Name: "x-goog-api-key",
				Description: "Gemini 原生协议的另一种鉴权头。Google 官方 SDK 默认用这个，" +
					"本站与 `Authorization: Bearer` 同时接受。",
			},
		},
	}
}

// schemas 返回全部可复用 schema。
func schemas() map[string]*Schema {
	return map[string]*Schema{
		// ── 错误 ──
		"Error": {
			Type: "object",
			Description: "错误响应。**形状与 OpenAI 官方一致**——现成的 SDK 会解析这个结构，" +
				"若返回自定义格式，调用方只能看到「未知错误」。",
			Required: []string{"error"},
			Properties: map[string]*Schema{
				"error": ref("ErrorDetail"),
			},
		},
		"ErrorDetail": {
			Type:     "object",
			Required: []string{"message", "type"},
			Properties: map[string]*Schema{
				"message": str("面向人的可读信息。**不包含**文件路径、SQL、上游地址或密钥。"),
				"type": {
					Type:        "string",
					Description: "错误类别。客户端 SDK 按它决定是否重试。",
					Enum: []string{oai.TypeInvalidRequest, oai.TypeAuthentication, oai.TypePermission,
						oai.TypeRateLimit, oai.TypeServer, oai.TypeContentFilter},
				},
				"code": {
					Type: "string",
					Description: "本站语义化错误码，供程序化判断。**注意：这是本站自定义的，不是 OpenAI 官方码**——" +
						"它比 HTTP 状态码精确得多：同样是 502，`upstream_unavailable`（该退避重试）" +
						"与 `upstream_request_failed`（重试无用）的处置完全不同。",
					Enum: []string{
						oai.CodeMissingAPIKey, oai.CodeInvalidAPIKey, oai.CodeTokenDisabled,
						oai.CodeTokenExpired, oai.CodeInsufficientQuota, oai.CodeModelNotAllowed,
						oai.CodeInvalidJSON, oai.CodeMissingModel, oai.CodeRequestTooLarge,
						oai.CodeNoAvailableChannel, oai.CodeUpstreamRequestFailed,
						oai.CodeUpstreamUnavailable, oai.CodeUpstreamRateLimited,
						oai.CodeSensitiveWordBlocked, oai.CodeInternal,
					},
				},
			},
		},
		"Usage": {
			Type:        "object",
			Description: "计费信息。字段名与 OpenAI 官方一致。",
			Properties: map[string]*Schema{
				"prompt_tokens":     integer("输入 token 数"),
				"completion_tokens": integer("输出 token 数"),
				"total_tokens":      integer("两者之和"),
			},
		},

		// ── 模型清单 ──
		"ModelList": {
			Type:        "object",
			Description: "模型清单，形状与 OpenAI 官方一致。",
			Required:    []string{"object", "data"},
			Properties: map[string]*Schema{
				"object": {Type: "string", Enum: []string{"list"}},
				"data":   {Type: "array", Items: ref("Model")},
			},
		},
		"Model": {
			Type:     "object",
			Required: []string{"id", "object"},
			Properties: map[string]*Schema{
				"id":     str("模型名，转发时填进请求体的 `model` 字段"),
				"object": {Type: "string", Enum: []string{"model"}},
				"created": integer("恒为 0。模型在本网关里没有「创建时间」这一语义，" +
					"客户端只把它当作可排序字段——填 0 比编造一个时间更诚实。"),
				"owned_by": str("恒为 `aqua-api`，标明这些模型是通过本网关转发的"),
			},
		},

		// ── 对话 ──
		"ChatCompletionRequest": chatRequestSchema(),
		"ChatCompletion":        chatResponseSchema(),

		// ── 向量 ──
		"EmbeddingRequest": embeddingRequestSchema(),
		"EmbeddingResponse": {
			Type:     "object",
			Required: []string{"object", "data"},
			Properties: map[string]*Schema{
				"object": {Type: "string", Enum: []string{"list"}},
				"data": {Type: "array", Items: obj(map[string]*Schema{
					"object": {Type: "string", Enum: []string{"embedding"}},
					"index":  integer("在数组中的下标"),
					"embedding": {Type: "array", Items: number("单个分量"),
						Description: "向量分量数组"},
				})},
				"model": str("实际命中的模型"),
				"usage": ref("Usage"),
			},
		},

		// ── 异步任务 ──
		"Task":            taskSchema(),
		"TaskCreateInput": taskRequestSchema(),

		// ── 可观测 ──
		"MetricsEndpoint": {
			Type: "string",
			Description: "Prometheus 文本协议输出：`# HELP` / `# TYPE` 注释行 + 指标样本行。\n\n" +
				"抓取配置示例：\n\n```yaml\nscrape_configs:\n" +
				"  - job_name: aqua-api\n    metrics_path: /metrics\n" +
				"    authorization:\n      credentials: '<AQUA_METRICS_TOKEN>'\n```",
		},
	}
}

// chatRequestSchema 是对话请求的 schema。
func chatRequestSchema() *Schema {
	return obj(map[string]*Schema{
		"model": {
			Type: "string",
			Description: "模型名，取自 `GET /v1/models`。本站支持通配写法（如 `gpt-4*` 匹配全部 gpt-4 系列），" +
				"但**不建议在生产里用**——上游改模型名会让你的请求突然走到另一个模型上。",
			Example: "gpt-4o",
		},
		"messages": {
			Type: "array",
			Description: "对话消息数组。首条通常是 role=system 的指令，其后交替 user / assistant。" +
				"`content` 可以是纯字符串，也可以是内容块数组（多模态）。",
			Items: obj(map[string]*Schema{
				"role": {
					Type:        string("角色"),
					Enum:        []string{"system", "user", "assistant", "tool", "function"},
					Description: "system 只能出现在首条；tool / function 需要前面有对应的调用消息",
				},
				"content": {Description: "字符串或多模态内容块数组，形如 " +
					"`[{\"type\":\"text\",\"text\":\"...\"}," +
					"{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64,...\"}}]`"},
				"name":         str("函数调用时的函数名"),
				"tool_call_id": str("tool 消息必须带上它，指明这条回复的是哪次调用"),
			}, "role"),
		},
		"stream":      boolean("true = SSE 流式，false = 一次性返回完整 JSON"),
		"temperature": number("采样温度 0–2。0 附近最稳，越高越发散"),
		"top_p":       number("核采样。与 temperature 通常只生效其一，一般二选一"),
		"max_tokens":  integer("**输出** token 上限；输入另算。撞上后 finish_reason 会是 length"),
		"stop": {
			Type:        []string{"string", "array"},
			Description: "停止词，字符串或字符串数组，最多 4 个",
			Items:       str("单个停止词"),
		},
		"n": integer("生成几份候选回复。**计费按实际生成的份数算**，不是按 n 请求一次算一次"),

		// ── 本站扩展 ──
		"thinking": obj(map[string]*Schema{
			"type":          {Type: "string", Enum: []string{"enabled", "disabled"}},
			"budget_tokens": integer("推理预算上限；超出后模型会自行收尾"),
		}, "type"),
		"reasoning_effort": {
			Type:        string("推理强度"),
			Enum:        []string{"low", "medium", "high"},
			Description: "与 `thinking` 是两种写法，命中哪个由上游决定；都不支持时静默忽略",
		},
		"web_search": obj(map[string]*Schema{
			"enable": boolean("是否启用联网搜索"),
		}),

		"response_format": obj(map[string]*Schema{
			"type": {
				Type: string("输出格式"),
				Enum: []string{"text", "json_object"},
				Description: "json_object 会提示模型输出 JSON，但**不保证一定合法**——" +
					"严格场景仍需自己校验或改用结构化输出工具",
			},
		}, "type"),
		"tools": {
			Type:        "array",
			Description: "工具（函数）定义，供模型选择调用",
			Items: obj(map[string]*Schema{
				"type": {Type: "string", Enum: []string{"function"}},
				"function": obj(map[string]*Schema{
					"name":        str("函数名"),
					"description": str("函数说明——**模型靠它决定何时调用**，写得含糊就会乱调"),
					"parameters": {
						Type:                 "object",
						Description:          "参数的 JSON Schema",
						AdditionalProperties: true,
					},
				}, "name"),
			}),
		},
		"tool_choice": {
			Type:        []string{"string", "object"},
			Description: "工具选择策略：`auto`（模型自决）/ `none`（禁用）/ 强制指定某个函数名",
		},
		"user": str("终端用户标识。只用于本站的调用统计与风控，不参与计费"),
	}, "model", "messages")
}

// chatResponseSchema 是对话响应的 schema。
func chatResponseSchema() *Schema {
	return obj(map[string]*Schema{
		"id":      str("本次响应的编号"),
		"object":  {Type: "string", Enum: []string{"chat.completion"}},
		"created": integer("创建时刻（Unix 秒）"),
		"model":   str("实际命中的模型。与请求里的 model 可能不同（本站做了通配路由），这是预期的"),
		"choices": {
			Type: "array",
			Items: obj(map[string]*Schema{
				"index": integer("候选下标"),
				"message": obj(map[string]*Schema{
					"role":    {Type: "string"},
					"content": str("回复正文"),
					"tool_calls": {
						Type: "array",
						Items: obj(map[string]*Schema{
							"id":   str("调用编号，tool 消息要回传它"),
							"type": {Type: "string", Enum: []string{"function"}},
							"function": obj(map[string]*Schema{
								"name":      str("函数名"),
								"arguments": str("参数，**JSON 字符串**（不是对象）——直接 parse 会踩坑"),
							}),
						}),
					},
					"reasoning_content": str("推理过程。命中支持深度思考的模型时才有；" +
						"**部分上游把思考内容放在这里而不是 content**，只读 content 会拿到空的"),
				}),
				"finish_reason": {
					Type: string("结束原因"),
					Enum: []string{"stop", "length", "content_filter", "tool_calls"},
					Description: "`length` 表示撞上 max_tokens 被截断——内容不完整，重试时调大它；" +
						"`content_filter` 表示命中站点内容安全策略",
				},
			}),
		},
		"usage": ref("Usage"),
	})
}

// embeddingRequestSchema 是嵌入请求的 schema。
func embeddingRequestSchema() *Schema {
	return obj(map[string]*Schema{
		"model":           {Type: "string", Description: "嵌入模型名", Example: "text-embedding-3-small"},
		"input":           {Description: "字符串或字符串数组。**传数组比逐条调用省一个数量级的费用**"},
		"encoding_format": {Type: "string", Enum: []string{"float", "base64"}, Description: "默认 float"},
		"dimensions":      integer("输出维度。部分模型支持降到原维度以省存储，代价是精度下降"),
		"user":            str("终端用户标识，仅用于统计"),
	}, "model", "input")
}

// imageRequestSchema 是图像生成请求的 schema。
func imageRequestSchema() *Schema {
	return obj(map[string]*Schema{
		"model":           str("图像模型名"),
		"prompt":          {Type: "string", Description: "提示词。**只有它与 model 是必填的**"},
		"n":               integer("生成张数 1–10。**计费按张数算**"),
		"size":            str("尺寸，如 `1024x1024` / `1792x1024` / `auto`"),
		"quality":         {Type: "string", Enum: []string{"standard", "hd"}, Description: "hd 更贵也更慢"},
		"style":           {Type: "string", Enum: []string{"vivid", "natural"}, Description: "仅 dall-e-3 支持"},
		"response_format": {Type: "string", Enum: []string{"url", "b64_json"}},
		"user":            str("终端用户标识"),
	}, "model", "prompt")
}

// speechRequestSchema 是语音合成请求的 schema。
func speechRequestSchema() *Schema {
	return obj(map[string]*Schema{
		"model":           str("TTS 模型名"),
		"input":           str("要合成的文本"),
		"voice":           {Type: "string", Description: "音色名，如 `alloy` / `echo` / `fable` / `nova`"},
		"response_format": {Type: "string", Enum: []string{"mp3", "opus", "aac", "flac", "wav", "pcm"}},
		"speed":           number("语速 0.25–4.0，默认 1.0"),
	}, "model", "input", "voice")
}

// transcriptionFormSchema 是语音转写表单的 schema。
func transcriptionFormSchema() *Schema {
	return obj(map[string]*Schema{
		"file":            str("音频文件，**上限 25 MiB**；支持 mp3 / mp4 / mpeg / mpga / m4a / wav / webm"),
		"model":           str("转写模型名"),
		"language":        str("ISO-639-1 语言码，如 `zh`。指定后准确率明显提升"),
		"prompt":          str("引导词，用于影响标点与专有名词"),
		"response_format": {Type: "string", Enum: []string{"json", "text", "srt", "verbose_json", "vtt"}},
		"temperature":     number("0–1，默认 0。提高会增加幻听概率，一般不动"),
	}, "file", "model")
}

// anthropicRequestSchema 是 Anthropic Messages 请求的 schema。
func anthropicRequestSchema() *Schema {
	return obj(map[string]*Schema{
		"model": str("模型名（不带 `anthropic/` 前缀也能识别）"),
		"messages": {
			Type: "array",
			Items: obj(map[string]*Schema{
				"role":    {Type: "string", Enum: []string{"user", "assistant"}},
				"content": {Description: "字符串或内容块数组（文本 / 图片块）"},
			}, "role"),
		},
		"system": {Description: "系统提示词。**Anthropic 把它放在顶层而不是 messages 里**——" +
			"从 OpenAI 协议迁过来时最容易错的就是这一处"},
		// 必填性已由外层 obj(..., "max_tokens") 声明，这里不再重复。
		"max_tokens":  integer("**Anthropic 协议里必填**，没有默认值"),
		"stream":      boolean("true = SSE 流式"),
		"temperature": number("0–1（**上限是 1**，与 OpenAI 的 2 不同）"),
		"top_p":       number("核采样"),
		"top_k":       integer("Anthropic 独有：候选 token 数"),
		"stop_sequences": {
			Type: "array", Items: str("停止序列"),
			Description: "最多 4 个",
		},
		"tools": {
			Type: "array",
			Items: obj(map[string]*Schema{
				"name":        str("工具名"),
				"description": str("工具说明"),
				"input_schema": {
					Type: "object", Description: "参数的 JSON Schema",
					AdditionalProperties: true,
				},
			}),
		},
	}, "model", "messages", "max_tokens")
}

// responsesRequestSchema 是 Responses 请求的 schema。
//
// 为什么用 additionalProperties 兜底而不是逐字段列：Responses 协议仍在演进，
// 官方每个版本都在加字段；列全了很快就会过期，而漏字段比模糊更糟——
// 漏字段的表现是使用者照文档写了但服务端说没这参数。
func responsesRequestSchema() *Schema {
	return passthrough(obj(map[string]*Schema{
		"model":        str("模型名"),
		"input":        {Description: "字符串，或输入项数组（含消息、函数调用结果等）"},
		"instructions": str("系统指令（对应 OpenAI 协议的 system 消息）"),
		"stream":       boolean("true = SSE 流式"),
		"tools": {Type: "array", Description: "工具定义",
			Items: &Schema{Type: "object", AdditionalProperties: true}},
		"reasoning": obj(map[string]*Schema{
			"effort": {Type: "string", Enum: []string{"minimal", "low", "medium", "high"}},
		}),
	}, "model", "input"))
}

// geminiRequestSchema 是 Gemini 原生协议请求的 schema。
func geminiRequestSchema() *Schema {
	return passthrough(obj(map[string]*Schema{
		"contents": {
			Type: "array",
			Description: "对话内容。`role` 为 `user` / `model`（**注意不是 assistant**），" +
				"`parts` 内放 text / inlineData（图片）等块",
			Items: obj(map[string]*Schema{
				"role":  {Type: "string", Enum: []string{"user", "model"}},
				"parts": {Type: "array", Items: &Schema{Type: "object", AdditionalProperties: true}},
			}, "role", "parts"),
		},
		"systemInstruction": {
			Type:        "object",
			Description: "系统指令（对应另两个协议的 system 消息）",
		},
		"generationConfig": {
			Type:        "object",
			Description: "生成参数：`temperature` / `topP` / `topK` / `maxOutputTokens` / `stopSequences`",
		},
		"safetySettings": {
			Type: "array", Description: "安全阈值设置",
			Items: &Schema{Type: "object", AdditionalProperties: true},
		},
	}, "contents"))
}

// taskRequestSchema 是异步任务提交的 schema。
func taskRequestSchema() *Schema {
	// 其余字段（aspect_ratio / duration / n / seed 等）按类别的差异很大，
	// 一律透传给上游，故用 additionalProperties 表达"还有更多参数"。
	return passthrough(obj(map[string]*Schema{
		"kind": {
			Type: "string", Enum: []string{"image", "video", "music"},
			Description: "任务类别。**不填会按 model 名推断**（如 `imagine-*` 归入 image），" +
				"但显式填更稳妥",
		},
		"model": {Type: "string", Description: "模型名，或该类模型的动作名（如 imagine / blend）"},
		"prompt": {Type: "string", Description: "任务描述。图像类也可用 `image` 字段传参考图" +
			"（URL 或 Base64 数据 URI）"},
		"provider": str("指定上游提供方；留空则由路由自动选择"),
	}, "kind", "model"))
}

// taskSchema 是任务对象的 schema。
func taskSchema() *Schema {
	return obj(map[string]*Schema{
		"task_ref":  {Type: "string", Description: "任务号，轮询时用它", Example: "tsk_1a2b3c4d"},
		"kind":      {Type: "string", Enum: []string{"image", "video", "music"}},
		"kind_text": str("类别的中文名"),
		"provider":  str("实际命中的上游提供方"),
		"model":     str("实际命中的模型"),
		"prompt":    str("提示词"),
		"params":    str("其余参数，**JSON 字符串**"),
		"status": {
			Type: "integer",
			Enum: []string{"1", "2", "3", "4", "5"},
			Description: "1 排队 / 2 进行中 / 3 完成 / 4 失败 / 5 取消。" +
				"**3、4、5 是终态**，其余状态继续轮询",
		},
		"status_text": str("状态的中文描述"),
		"progress":    integer("进度百分比 0–100"),
		"result_url":  str("结果地址，**通常带有效期，请及时下载**；仅 status=3 时有值"),
		"result_data": str("上游返回的完整结果（**JSON 字符串**）；仅 status=3 时有值"),
		"error":       str("失败原因；仅 status=4 时有值"),
		"quota":       integer("该任务扣除的额度（站内单位，1 额度 = 1 微元）"),
		"channel_id":  integer("命中的渠道编号，排障时提供它能直接定位到具体上游"),
		"created_at":  integer("创建时刻（Unix 秒）"),
		"updated_at":  integer("最近更新时刻"),
		"finished_at": integer("完成时刻；未完成为 0"),
	})
}
