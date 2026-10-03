// 本文件提供 OpenAPI 规范端点。
//
// 意图（Why）：
//
//	中转站的使用者要把现成客户端指过来，第一件事是问"base_url 填哪、
//	鉴权头长什么样、错了会返回什么"。这份信息散落在各处的后果是
//	每个新使用者都要靠试错，而试错的代价是真金白银的调用。
//
// 为什么规范要由服务端吐出、而不是放一个静态文件：
//
//	静态 openapi.json 一定会腐烂——加了端点忘了改文档，没有任何东西会报错，
//	而读者会照着一份过期的文档去接入，排查成本极高。
//	从代码生成则能保证"文档里有的接口一定存在"，且错误码枚举直接引用
//	oai 常量，改实现时编译期就会提醒（见 internal/openapi）。
//
// 流转（Flow）：
//
//	GET /openapi.json → openapi.Spec() → JSON
//	  → 机器：SDK 生成器 / Postman / Apifox 导入
//	  → 人：前端 /docs 页面拉取并渲染
//
// 扩展（Extend）：
//
//	新增对外端点：在 internal/openapi/paths() 里加一条，
//	本文件不需要改（除了新增一种鉴权方式时才要动 components）。
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/openapi"
)

// openapiCache 缓存已序列化的规范。
//
// 为什么缓存：Spec() 会构造整棵文档树（含几十个子 schema），
// 而这个端点可能被文档页的每次刷新、以及多个客户端探测反复拉取。
// 文档在进程生命周期内是常量（错误码枚举来自编译期常量），构造一次即可。
var (
	openapiOnce  sync.Once
	openapiBody  []byte
	openapiErr   error
	openapiMedia string // Content-Type，构造成功后固定
)

// buildOpenAPIDocument 构造规范正文（只执行一次）。
//
// 刻意关掉 HTML 转义：正文里大量中文说明与 Markdown 记号，
// 默认的 \u003c 转义会让 JSON 变成一大串难以人工检查的十六进制——
// 而这份文档的核心使用者恰恰是人。
func buildOpenAPIDocument() {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ") // 缩进：这份 JSON 经常被人直接翻开来读
	if err := enc.Encode(openapi.Spec()); err != nil {
		openapiErr = err
		return
	}
	openapiBody = buf.Bytes()
	openapiMedia = "application/json; charset=utf-8"
}

// handleOpenAPI 输出 OpenAPI 规范。
//
// 无需鉴权（Why 不做成需要令牌）：
//
//	这份文档里没有任何机密——端点路径、字段名、错误码都是使用者本来
//	就该知道的东西，鉴权只会让"想接入的人"先得注册账号，
//	而站长恰恰希望更多人看到自己能调什么。
//	反过来，它不包含任何令牌、上游地址或价格策略，因此公开无害。
func (s *Server) handleOpenAPI(c *gin.Context) {
	openapiOnce.Do(buildOpenAPIDocument)
	if openapiErr != nil {
		s.respondInternalError(c, "生成接口文档失败", openapiErr)
		return
	}

	// 短缓存：文档随版本变，但 5 分钟的窗口足以挡住"文档页反复刷新"
	// 造成的重复序列化，同时保证发版后不需要等很久才生效。
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, openapiMedia, openapiBody)
}
