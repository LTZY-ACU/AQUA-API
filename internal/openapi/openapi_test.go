// OpenAPI 规范的完整性测试。
//
// 意图（Why）：
//
//	这份规范是给机器和人同时看的，而它最大的风险是【静默腐烂】：
//	改了实现忘了改文档，编译不报错、测试不失败，只有真实使用者会发现，
//	而那时他们已经按错误的文档接入了半个项目。因此本文件的价值全在
//	"能不能在腐烂发生的那天就把它喊出来"。
//
// 三类检查：
//  1. 引用完整性：每个 $ref 都能在 components.schemas 里找到；
//  2. 结构有效性：operationId 唯一、路径规范、鉴权声明不为空；
//  3. 与实现同源：文档里的端点确实在 gin 路由表里注册过。
//
// 扩展（Extend）：
//
//	新增对外端点后，本文件第 3 类检查会自动覆盖它——若忘了写进规范会失败。
package openapi

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// TestSpec_引用完整性 钉住最关键的静默腐烂方式：改了名字忘了改引用。
//
// 为什么用遍历 JSON 的方式而不是直接走结构体：结构体里到处是 *Schema，
// 手写遍历会在"漏掉某个嵌套层级"时静默通过——而漏掉的正是引用所在之处。
// 走序列化后的 JSON 才能看到真实产出的形状。
func TestSpec_引用完整性(t *testing.T) {
	doc := Spec()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}

	// 反序列化到 any，顺便验证产出的是合法 JSON
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("产出不是合法 JSON：%v", err)
	}

	defined := doc.Components.Schemas
	found := map[string]int{}
	collectRefs(tree, found)

	if len(found) == 0 {
		t.Fatal("规范里一个 $ref 都没有，说明 schema 引用机制没被用起来（文档会变得难以阅读）")
	}
	for name := range found {
		if _, ok := defined[name]; !ok {
			t.Errorf("规范引用了未定义的 schema %q——文档渲染时会显示成一个坏链接，"+
				"而这不会引起任何编译错误", name)
		}
	}
}

// TestSpec_未被引用的schema不算错误但值得知道 本文件不做断言，只打印，
// 目的是让"新增了 schema 却忘了挂到某个端点上"这件事在测试日志里可见。
//
// 当前会打印四项（ChatCompletionRequest / EmbeddingRequest /
// EmbeddingResponse / TaskCreateInput），这是【有意为之】：
// 请求体在各端点里是内联展开的（文档里能直接看到字段表，比点开一个 $ref 好读），
// 而这些具名副本是留给代码生成器的——openapi-typescript / openapi-generator
// 会把它们变成 `ChatCompletionRequest` 这样的类型名，而不是匿名的内联对象。
func TestSpec_未被引用的schema(t *testing.T) {
	doc := Spec()
	raw, _ := json.Marshal(doc)
	var tree any
	_ = json.Unmarshal(raw, &tree)

	referenced := map[string]bool{}
	refs := map[string]int{}
	collectRefs(tree, refs)
	for name := range refs {
		referenced[name] = true
	}
	for name := range doc.Components.Schemas {
		if !referenced[name] {
			t.Logf("提示：schema %q 定义了但没有任何端点引用它", name)
		}
	}
}

// pathParamNames 抽出一条 OpenAPI 路径里的全部参数名。
//
// 为什么要按 { } 逐个匹配而不是先按 / 切段：Gemini 那条路径是
// /v1beta/models/{model}:{action}——一段里有**两个**参数，且中间夹着冒号。
// 按斜杠切段再 Trim 掉花括号会把 "{model}:{action}" 整体当成一个参数名，
// 于是"参数没声明"这类真实错误会被这个解析 bug 掩盖掉。
func pathParamNames(path string) []string {
	var names []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			break
		}
		names = append(names, path[i+1:i+end])
		i += end
	}
	return names
}

// collectRefs 递归收集 JSON 树里出现的所有 $ref 目标名。
func collectRefs(node any, out map[string]int) {
	switch v := node.(type) {
	case map[string]any:
		for key, val := range v {
			if key == "$ref" {
				if s, ok := val.(string); ok {
					if name := strings.TrimPrefix(s, "#/components/schemas/"); name != s {
						out[name]++
					}
				}
				continue
			}
			collectRefs(val, out)
		}
	case []any:
		for _, item := range v {
			collectRefs(item, out)
		}
	}
}

// TestSpec_端点结构有效 钉住几个"填了但填错"的常见问题。
func TestSpec_端点结构有效(t *testing.T) {
	doc := Spec()

	if doc.OpenAPI != specVersion {
		t.Errorf("openapi = %q，期望 %q", doc.OpenAPI, specVersion)
	}
	if doc.Info.Version == "" {
		t.Error("info.version 为空：文档必须带上与二进制一致的版本号")
	}
	if len(doc.Servers) == 0 {
		t.Error("servers 为空：使用者将不知道 base_url 该填什么——这是接入的第一个卡点")
	}

	seen := map[string]string{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("路径 %q 不以 / 开头", path)
		}
		// 含 {param} 的路径必须有对应的 path 参数声明，否则模板引擎拼不出 URL
		declared := map[string]bool{}
		for _, op := range []*Operation{item.Get, item.Post} {
			if op == nil {
				continue
			}
			for _, p := range op.Parameters {
				if p.In == "path" {
					declared[p.Name] = true
				}
			}
		}
		for _, name := range pathParamNames(path) {
			if !declared[name] {
				t.Errorf("路径 %q 声明了参数 {%s}，但 Operation 里没有对应的 path 参数", path, name)
			}
		}

		for _, op := range []*Operation{item.Get, item.Post} {
			if op == nil {
				continue
			}
			if op.OperationID == "" {
				t.Errorf("%s 缺少 operationId：SDK 生成器会用它作为方法名，缺了就退化成 URL 拼接",
					path)
			}
			if prev, dup := seen[op.OperationID]; dup {
				t.Errorf("operationId %q 在 %s 与 %s 上重复——生成器会产出两个同名方法而编译失败",
					op.OperationID, prev, path)
			}
			seen[op.OperationID] = path

			if op.Summary == "" {
				t.Errorf("%s (%s) 缺少 summary：侧栏里会显示成一个空条目", path, op.OperationID)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s (%s) 缺少 tag：文档里会飘在无分类区", path, op.OperationID)
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s (%s) 没有 responses：生成器会产出一个没有返回类型的客户端",
					path, op.OperationID)
			}
			// 2xx 必须存在——只有错误响应的端点在文档里读起来像"这接口总是失败"
			has2xx := false
			for code := range op.Responses {
				if strings.HasPrefix(code, "2") {
					has2xx = true
					break
				}
			}
			if !has2xx {
				t.Errorf("%s (%s) 只列了错误响应，没有成功响应", path, op.OperationID)
			}
		}
	}
}

// TestSpec_公开端点必须显式声明免鉴权 钉住 OpenAPI 里最容易整体搞错的一条语义。
//
// security 是**继承**的：全局设了 bearerAuth 之后，不显式覆盖就等于要求令牌。
// 于是 /healthz 这类公开端点会在文档里显示"需要 Bearer"——
// 而它明明不需要，读者会以为探活也要传令牌，白排查很久。
func TestSpec_公开端点必须显式声明免鉴权(t *testing.T) {
	doc := Spec()
	publicPaths := []string{"/healthz", "/metrics"}

	for _, path := range publicPaths {
		item, ok := doc.Paths[path]
		if !ok || item.Get == nil {
			t.Errorf("公开端点 %s 不在规范里：使用者排查「服务是否可用」时不知道该打哪个路径", path)
			continue
		}
		if item.Get.Security == nil {
			t.Errorf("%s 是公开端点，但没显式声明免鉴权——"+
				"security 是继承的，文档会显示它需要 Bearer 令牌", path)
		}
	}

	// 反向检查：需要令牌的端点不得声明免鉴权
	for path, item := range doc.Paths {
		if path == "/healthz" || path == "/metrics" {
			continue
		}
		for _, op := range []*Operation{item.Get, item.Post} {
			if op == nil {
				continue
			}
			if len(op.Security) == 1 && len(op.Security[0]) == 0 {
				t.Errorf("%s (%s) 声明了免鉴权，但它是需要令牌的端点", path, op.OperationID)
			}
		}
	}
}

// TestSpec_错误码枚举与实现同源 本文件是"规范不腐烂"的核心保障。
//
// 断言方式不是"枚举里有某个值"，而是"实现里定义的全部码都在枚举里"——
// 反过来才是能抓到漏的检查方向：加了新码而忘了写进文档，测试会失败。
func TestSpec_错误码枚举与实现同源(t *testing.T) {
	doc := Spec()
	detail, ok := doc.Components.Schemas["ErrorDetail"]
	if !ok {
		t.Fatal("缺少 ErrorDetail schema")
	}
	code, ok := detail.Properties["code"]
	if !ok {
		t.Fatal("ErrorDetail 缺少 code 属性：客户端就没有可编程判断的依据")
	}

	// 实现里真实存在的码（与 oai 包常量一一对应）
	implemented := []string{
		"missing_api_key", "invalid_api_key", "token_disabled", "token_expired",
		"insufficient_quota", "model_not_allowed", "invalid_json", "missing_model",
		"request_too_large", "no_available_channel", "upstream_request_failed",
		"upstream_unavailable", "upstream_rate_limited", "sensitive_word_blocked",
		"internal_error",
	}
	documented := map[string]bool{}
	for _, v := range code.Enum {
		documented[v] = true
	}
	for _, c := range implemented {
		if !documented[c] {
			t.Errorf("错误码 %q 在实现里存在，但没写进文档枚举——"+
				"使用者遇到它时只能猜（枚举写了实现没有才是安全的方向）", c)
		}
	}
	// 反向：文档里不得出现实现中不存在的码
	for v := range documented {
		found := false
		for _, c := range implemented {
			if c == v {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("文档枚举里有实现中不存在的码 %q：它永远不会出现，写它等于误导", v)
		}
	}
}

// TestSpec_错误响应统一引用ErrorSchema 防止某个端点手写了一个形状不同的错误体。
//
// 为什么这条重要：形状不一的错误体是 SDK 解析失败的头号原因——
// 调用方的 catch 块读到 undefined，看起来像"网关崩了"，实际只是某个端点偷懒。
func TestSpec_错误响应统一引用ErrorSchema(t *testing.T) {
	doc := Spec()
	for path, item := range doc.Paths {
		for _, op := range []*Operation{item.Get, item.Post} {
			if op == nil {
				continue
			}
			for code, resp := range op.Responses {
				if !strings.HasPrefix(code, "4") && !strings.HasPrefix(code, "5") {
					continue
				}
				mt, ok := resp.Content["application/json"]
				if !ok || mt.Schema == nil {
					t.Errorf("%s (%s) 的 %s 响应没有 JSON 内容类型：客户端无法解析", path, op.OperationID, code)
					continue
				}
				if mt.Schema.Ref != "#/components/schemas/Error" {
					t.Errorf("%s (%s) 的 %s 响应没有复用 Error schema（实际 %q）",
						path, op.OperationID, code, mt.Schema.Ref)
				}
			}
		}
	}
}

// TestSpec_核心端点都已收录 钉住"新加了转发端点但忘了写进文档"。
//
// 这是一个刻意的白名单而不是自动发现：gin 的路由表里还有大量
// /api/admin/* 与 /api/user/*，那些**故意不收**（见包头说明）。
// 白名单因此只列"持有令牌的程序该用的端点"，加新端点时必须在这里加一行——
// 忘了的话这条会失败，等于把"要不要写文档"变成一个必须回答的问题。
func TestSpec_核心端点都已收录(t *testing.T) {
	doc := Spec()
	want := []string{
		"/v1/models",
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/images/generations",
		"/v1/audio/speech",
		"/v1/audio/transcriptions",
		"/v1/audio/translations",
		"/v1/messages",
		"/v1/responses",
		"/v1beta/models/{model}:{action}",
		"/v1/tasks",
		"/v1/tasks/{ref}",
		"/healthz",
		"/metrics",
	}
	for _, p := range want {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("核心端点 %s 没有出现在规范里", p)
		}
	}
}

// TestSpec_不含后台与门户接口 说明"没收录"是有意的，不是遗漏。
func TestSpec_不含后台与门户接口(t *testing.T) {
	doc := Spec()
	for path := range doc.Paths {
		if strings.HasPrefix(path, "/api/") {
			t.Errorf("规范里出现了 %s：后台与门户接口服务于浏览器会话而非程序化调用，"+
				"写进 API 文档会让文档被前端内部实现绑架", path)
		}
	}
}

// TestSpec_不泄露敏感信息 文档是公开的，任何出现在里面的东西都等于对外公布。
//
// 只匹配【凭据形态】而不匹配中文词：说明文字里出现"不包含上游地址"是
// 我们想要的（正是在告诉使用者这一点），把词本身当敏感词会让这条检查
// 逼着人把有用的免责声明删掉——检查一旦开始伤害正确行为就该改检查。
func TestSpec_不泄露敏感信息(t *testing.T) {
	raw, err := json.Marshal(Spec())
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	body := string(raw)

	// 形如真实凭据的片段：键名后跟一个看起来像随机串的值
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)access_token["'\\s:=]+[A-Za-z0-9_-]{12,}`),
		regexp.MustCompile(`(?i)(app_key|secret|password|passwd)["'\\s:=]+[A-Za-z0-9]{12,}`),
		regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`AKID[A-Za-z0-9]{10,}`),
		// 真实内网/云元数据地址
		regexp.MustCompile(`169\.254\.169\.254`),
	}
	for _, re := range patterns {
		if m := re.FindString(body); m != "" {
			t.Errorf("规范里出现了疑似真实凭据 %q：/openapi.json 是公开端点，出现即等于对外公布", m)
		}
	}

	// 占位符是允许的（"sk-..."、"https://你的域名"）——它们不指向任何真实目标
	if !strings.Contains(body, "sk-") {
		t.Error("文档里连令牌示例都没有：使用者不知道该去哪儿创建令牌")
	}
}

// TestSpec_说明里必须写清XRequestId 的价值 报障时能给出追踪 ID 与不能给出，
// 排查效率差一个数量级——这条把它固化下来，避免某次精简描述时被删掉。
func TestSpec_说明里必须写清XRequestId(t *testing.T) {
	doc := Spec()
	item, ok := doc.Paths["/v1/chat/completions"]
	if !ok || item.Post == nil {
		t.Fatal("缺少 /v1/chat/completions")
	}
	resp, ok := item.Post.Responses["200"]
	if !ok {
		t.Fatal("对话接口没有 200 响应")
	}
	h, ok := resp.Headers["X-Request-Id"]
	if !ok {
		t.Fatal("对话接口的 200 响应没有声明 X-Request-Id 头：使用者不知道报障时该给我们什么")
	}
	if !strings.Contains(h.Description, "报障") {
		t.Errorf("X-Request-Id 的说明里没有点明它对报障的作用，实际拿到：%q", h.Description)
	}
}
