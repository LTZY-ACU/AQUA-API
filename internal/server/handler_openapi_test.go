// /openapi.json 端点的端到端测试。
//
// 意图（Why）：
//
//	这份规范是给机器和人同时看的，风险是【静默腐烂】：改了实现忘了改文档，
//	编译不报错、测试不失败，只有真实使用者按错误文档接入后才会发现。
//	本文件从 HTTP 层再钉一遍，理由是它能抓到 openapi 包内部测试抓不到的一类问题：
//
//	 1) 端点没注册（router 里漏挂）→ 规范里的路径根本访问不到；
//	 2) Content-Type / 缓存头不对 → SDK 生成器与 IDE 插件会拒绝导入；
//	 3) SPA 回退把它吞成index.html → 请求 200 但内容是 HTML，
//	    表现是"文档地址能打开但内容是网页"，排查成本极高。
//
// 流转（Flow）：
//
//	newTestServer → GET /openapi.json → 断言状态码、类型、内容与关键端点
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getOpenAPI 打一次 /openapi.json 并返回记录器与解析后的文档。
func getOpenAPI(t *testing.T, srv *Server) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

// TestOpenAPI_端点可访问且不被SPA回退吞掉 钉住最隐蔽的一类故障。
//
// Why 自己解 JSON 而不是复用测试辅助：若返回的是 index.html，
// json.Unmarshal 会失败并把 body 留空——那正好是本用例要检出的情形。
func TestOpenAPI_端点可访问且不被SPA回退吞掉(t *testing.T) {
	srv, _ := newTestServer(t)

	rec, body := getOpenAPI(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q，SDK 生成器与 IDE 插件靠它判断能否导入", ct)
	}
	if len(body) == 0 {
		t.Fatalf("响应不是 JSON（可能被 SPA 回退成了 index.html）：%.200s", rec.Body.String())
	}
	if v, _ := body["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Errorf("openapi = %v，期望 3.x", body["openapi"])
	}
	if _, ok := body["paths"].(map[string]any); !ok {
		t.Fatal("缺少 paths 字段")
	}
	if _, ok := body["components"].(map[string]any); !ok {
		t.Fatal("缺少 components 字段")
	}
}

// TestOpenAPI_无需鉴权即可访问 说明白它为什么不设防。
//
// 这条若哪天挂了，最可能的原因是有人"顺手"给它加上了鉴权中间件——
// 那样文档就只对已注册用户可见，而站长恰恰希望更多人看到自己能调什么。
func TestOpenAPI_无需鉴权即可访问(t *testing.T) {
	srv, _ := newTestServer(t)

	// 不带任何 Authorization / Cookie
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("公开端点应免鉴权，实际 %d", rec.Code)
	}
}

// TestOpenAPI_规范里的路径都真实注册 钉住"文档写了但接口不存在"。
//
// Why 用 gin 的路由表而不是手写清单：手写清单会和真实路由一起腐烂，
// 而路由表是唯一的事实来源。
func TestOpenAPI_规范里的路径都真实注册(t *testing.T) {
	srv, _ := newTestServer(t)
	_, body := getOpenAPI(t, srv)

	// 按方法分组而不是拼成 "METHOD /path" 单键：
	// 单键 map 遍历时拿不到路径本身（value 是 bool），要再 split 一次，
	// 而 split 出来的 method 与 path 又可能被路径里的空格骗到。
	registered := map[string][]string{}
	for _, route := range srv.engine.Routes() {
		registered[route.Method] = append(registered[route.Method], route.Path)
	}

	paths, _ := body["paths"].(map[string]any)
	for path, item := range paths {
		entries, _ := item.(map[string]any)
		for method := range entries {
			upper := strings.ToUpper(method)
			if routeMatches(registered, upper, path) {
				continue
			}
			t.Errorf("规范里的 %s %s 没有在路由表里注册——文档写了但接口不存在，"+
				"使用者照着接入会拿到 404", upper, path)
		}
	}
}

// routeMatches 判断规范里的一条路径是否在 gin 路由表里有对应的注册。
//
// 为什么不能直接字符串比对（这是本函数存在的全部理由）：
//
//  1. 规范用 {param}，gin 用 :param；
//  2. gin 支持通配段 *action（Gemini 那条路由就是 /v1beta/models/*action——
//     模型名与动作名都在同一段里，由适配器自己解析）。规范必须写成
//     /v1beta/models/{model}:{action} 才能让人拼出正确 URL，而它与
//     *action 不是字面相等的关系。
//
// 直接比对会把上面两种情况都报成"没注册"，一个永远失败的检查等于没有检查——
// 更糟的是它会让人养成忽略这条断言的习惯。
func routeMatches(registered map[string][]string, method, specPath string) bool {
	segments := strings.Split(strings.TrimPrefix(specPath, "/"), "/")
	for _, routePath := range registered[method] {
		if segmentsMatch(segments, strings.Split(strings.TrimPrefix(routePath, "/"), "/")) {
			return true
		}
	}
	return false
}

// segmentsMatch 判断规范路径段与 gin 路由段是否逐段吻合。
func segmentsMatch(spec, route []string) bool {
	if len(spec) != len(route) {
		return false
	}
	for i := range spec {
		s, r := spec[i], route[i]
		switch {
		case r == "*" || strings.HasPrefix(r, "*"):
			// gin 通配段吞掉余下所有内容，至少要接住本段
			return true
		case strings.HasPrefix(r, ":"):
			// 路径参数：名字不必相同（{ref} 对 :ref 也算匹配）
		case r == s:
		default:
			return false
		}
	}
	return true
}

// TestOpenAPI_包含核心端点 钉住"新加了转发端点但忘了写进规范"。
//
// 与 openapi 包内的白名单检查互补：那边查代码里的 Spec()，
// 这边查真正吐给用户的 HTTP 响应——两者都过了才算真的写进去了。
func TestOpenAPI_包含核心端点(t *testing.T) {
	srv, _ := newTestServer(t)
	_, body := getOpenAPI(t, srv)

	paths, _ := body["paths"].(map[string]any)
	for _, want := range []string{
		"/v1/models",
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/messages",
		"/v1/responses",
		"/v1/tasks",
		"/v1beta/models/{model}:{action}",
		"/healthz",
		"/metrics",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("规范缺少核心端点 %s", want)
		}
	}
}

// TestOpenAPI_缓存头合理 文档随版本变，但 5 分钟窗口足以挡住重复序列化。
func TestOpenAPI_缓存头合理(t *testing.T) {
	srv, _ := newTestServer(t)
	rec, _ := getOpenAPI(t, srv)

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q，应带 max-age（文档页会反复拉取这个端点）", cc)
	}
	if strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q，这份文档不含机密且很少变，不需要禁缓存", cc)
	}
}

// TestOpenAPI_响应可被SDK生成器解析 产出必须是合法 JSON 且结构完整。
//
// Why 单独测一遍结构：openapi 包内部已经测过引用完整性，
// 这里测的是"序列化之后"仍然成立——两者之间隔着 json.Marshal，
// 而 Marshal 对 map 的键序、特殊字符都可能做出意外。
func TestOpenAPI_响应可被SDK生成器解析(t *testing.T) {
	srv, _ := newTestServer(t)
	rec, _ := getOpenAPI(t, srv)

	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title       string `json:"title"`
			Version     string `json:"version"`
			Description string `json:"description"`
		} `json:"info"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]map[string]struct {
			OperationID string              `json:"operationId"`
			Tags        []string            `json:"tags"`
			Summary     string              `json:"summary"`
			Responses   map[string]struct{} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas         map[string]json.RawMessage `json:"schemas"`
			SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
		} `json:"components"`
		Security []map[string][]string `json:"security"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("产出不符合生成器预期的结构：%v", err)
	}

	if doc.OpenAPI == "" {
		t.Error("缺少 openapi 版本号")
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Errorf("info 不完整：title=%q version=%q", doc.Info.Title, doc.Info.Version)
	}
	if doc.Info.Description == "" {
		// 说明为空会让文档页顶部只剩一个标题，使用者不知道 base_url 怎么填
		t.Error("info.description 为空：接入说明会无处可写")
	}
	if len(doc.Servers) == 0 {
		t.Error("servers 为空：生成器不知道 base_url")
	}
	if _, ok := doc.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Error("缺少 bearerAuth 安全方案：生成器产出的客户端不会带上鉴权头")
	}
	if len(doc.Security) == 0 {
		t.Error("缺少全局 security：所有端点会被生成成免鉴权")
	}
	if len(doc.Components.Schemas) == 0 {
		t.Error("components.schemas 为空")
	}

	for path, ops := range doc.Paths {
		for method, op := range ops {
			if op.OperationID == "" {
				t.Errorf("%s %s 缺少 operationId", strings.ToUpper(method), path)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s %s 缺少 tags", strings.ToUpper(method), path)
			}
			if op.Summary == "" {
				t.Errorf("%s %s 缺少 summary", strings.ToUpper(method), path)
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s %s 没有 responses", strings.ToUpper(method), path)
			}
		}
	}
}

// TestOpenAPI_不因指标关闭而消失 说明端点注册与配置开关无关。
func TestOpenAPI_不因指标关闭而消失(t *testing.T) {
	srv, _ := newTestServer(t)
	cfg := srv.deps.Config
	cfg.Metrics.Enabled = false
	srv.deps.Config = cfg

	rec, _ := getOpenAPI(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("指标关闭不应影响文档端点，实际 %d", rec.Code)
	}
}
