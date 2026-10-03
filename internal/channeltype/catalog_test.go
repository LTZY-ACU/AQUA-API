// 本文件用不变量测试钉住「渠道目录」的几条硬约束。
//
// 意图（Why）：
//
//	catalog.go 是一份纯数据，最容易出的问题不是编译错误，而是"悄悄写错一格"：
//	漏填 Label、Key 打重、把尚未适配的类型标成 Available —— 后者尤其危险，
//	因为后台一旦把它呈现给站长，就会造成"配好了却调不通"的假象。
//	因此这里把关键约定写成断言，改目录时若破坏约定会立刻失败。
//
// 流转（Flow）：
//
//	go test ./internal/channeltype/...
//	  ├─ 逐条遍历 Types()：字段完整性、Key 唯一性
//	  ├─ 安全底线：Available=true 的类型必须走已实现的协议 + 已实现的鉴权方式
//	  ├─ 反向约束：OAuth / Cookie 一律不可用
//	  └─ 结构约束：分类覆盖、分组总数、Azure 必填额外参数
//
// 扩展（Extend）：
//
//	新增约定（例如"凡 Available 的类型必须有默认地址"）时，在此追加一条 test，
//	并在失败信息里写清"为什么这条约定重要"，方便后来人判断是改数据还是改约定。
package channeltype

import (
	"testing"
)

// supportedAuthModes 是「当前已实现的鉴权方式」白名单。
//
// 已实现：转发链路会拼 Authorization: Bearer、把密钥放进查询参数、
// 用自定义头（如 Azure 的 api-key）或 x-api-key（Anthropic 系）承载密钥、
// 对本地服务不带凭据，以及对 AWS 做 SigV4 签名（Bedrock）、用服务账号换取令牌（Vertex）。
// OAuth 订阅账号（AuthOAuth）也已实现：刷新链路见 relay/oauth.go，
// 凭据落库见 channel_keys 的 oauth 类型。
// 尚未实现：浏览器 Cookie 会话。
var supportedAuthModes = map[AuthMode]bool{
	AuthBearer:         true,
	AuthQueryKey:       true,
	AuthNone:           true,
	AuthAPIKeyHeader:   true,
	AuthXAPIKey:        true,
	AuthSigV4:          true,
	AuthServiceAccount: true,
	AuthOAuth:          true,
}

// implementedProtocols 是「当前已实现的协议适配器」白名单。
//
// Azure（部署名 + api-version 进路径与查询）、Anthropic（Messages 协议）、
// Gemini / Vertex（generateContent，模型名与动作进路径、密钥走查询参数或服务账号令牌）
// 与 Bedrock（SigV4 签名 + invoke 路径）的出站适配器已实现并通过测试；
// PaLM / Ollama / 自定义等协议尚未实现，一律不能标为可用。
var implementedProtocols = map[Protocol]bool{
	ProtocolOpenAI:    true,
	ProtocolAzure:     true,
	ProtocolAnthropic: true,
	ProtocolCodex:     true,
	ProtocolGemini:    true,
	ProtocolVertex:    true,
	ProtocolBedrock:   true,
	// CustomAsync 是模板驱动的异步任务上游（relay/async_custom.go）：
	// 协议本身已实现（提交/轮询/字段映射），具体厂商差异由渠道配置表达。
	ProtocolCustomAsync: true,
}

// TestTypes_必填字段非空 保证每条类型都具备后台展示与路由所需的最小信息。
//
// 缺 Key/Label 会让界面出现空白项；缺 Category/Protocol/AuthMode 会让分组
// 或转发逻辑拿到空值；缺 Notes 则站长无从判断"这类上游是什么"。
func TestTypes_必填字段非空(t *testing.T) {
	for i, item := range Types() {
		if item.Key == "" {
			t.Errorf("第 %d 条类型缺少 Key（无法写入渠道记录）", i)
		}
		if item.Label == "" {
			t.Errorf("类型 %q 缺少 Label（后台会显示空白）", item.Key)
		}
		if item.Category == "" {
			t.Errorf("类型 %q 缺少 Category（无法分组展示）", item.Key)
		}
		if item.Protocol == "" {
			t.Errorf("类型 %q 缺少 Protocol（转发时无法选适配器）", item.Key)
		}
		if item.AuthMode == "" {
			t.Errorf("类型 %q 缺少 AuthMode（转发时无法带凭据）", item.Key)
		}
		if item.Notes == "" {
			t.Errorf("类型 %q 缺少 Notes（站长无法理解其特点）", item.Key)
		}
	}
}

// TestTypes_Key唯一 保证类型标识不重复。
//
// Key 是渠道记录对外的主键语义：一旦重复，Find 只会命中第一条，
// 第二条将永远无法被选中，属于"登记了却用不上"的静默故障。
func TestTypes_Key唯一(t *testing.T) {
	seen := make(map[string]bool)
	for _, item := range Types() {
		if seen[item.Key] {
			t.Errorf("Key %q 重复登记（Find 只会命中第一条，后者将永远无法被选中）", item.Key)
		}
		seen[item.Key] = true
	}
}

// TestAvailable_协议必须已实现 钉住安全底线之一。
//
// 只有实现了协议适配器的类型才能标为 Available=true；否则后台会放行选用，
// 但转发时没有任何适配器能处理它，结果是"配好了却调不通"。
// 当前已实现的协议见 implementedProtocols（OpenAI 兼容 / Azure / Anthropic）。
func TestAvailable_协议必须已实现(t *testing.T) {
	for _, item := range Types() {
		if item.Available && !implementedProtocols[item.Protocol] {
			t.Errorf("类型 %q（%s）标为可用，但协议为 %q 尚未实现；"+
				"误标会导致配好后调不通",
				item.Key, item.Label, item.Protocol)
		}
	}
}

// TestAvailable_鉴权必须是已实现方式 钉住安全底线之二。
//
// 即便协议已实现，只要鉴权方式还没实现（OAuth 续期、Cookie 会话），
// 类型同样不能标为可用。
func TestAvailable_鉴权必须是已实现方式(t *testing.T) {
	for _, item := range Types() {
		if item.Available && !supportedAuthModes[item.AuthMode] {
			t.Errorf("类型 %q（%s）标为可用，但鉴权方式 %q 尚未实现；"+
				"误标会导致配好后鉴权失败", item.Key, item.Label, item.AuthMode)
		}
	}
}

// TestUnavailable_未接入的订阅账号一律不可用 钉住订阅类渠道的开放边界。
//
// 为什么不能按鉴权方式一刀切：订阅账号的刷新链路骨架已实现（AuthOAuth 已在
// supportedAuthModes 里），但"能刷新令牌"不等于"能转发请求"——每个平台的端点、
// 必需请求头与请求协议都不同（ChatGPT 要转 Responses 协议，Claude / Gemini
// 各有自己的格式）。因此边界必须逐个类型显式声明。
//
// 本测试用白名单把"已接入"钉死；其余订阅类型一旦被标为可用就报错，
// 避免出现"后台里配得好好的、一调就 4xx"的假象。
func TestUnavailable_未接入的订阅账号一律不可用(t *testing.T) {
	// wiredSubscriptionTypes 是"适配器与凭据链路都已打通"的订阅类型白名单。
	//
	// 新增订阅类型时：先把出站适配器（含请求/响应转换）与测试补完，
	// 再把类型 key 加到这里——顺序反了就等于把未完成的功能暴露给站长。
	wiredSubscriptionTypes := map[string]bool{
		"openai_codex_subscription": true,
	}

	for _, item := range Types() {
		if item.Category != CategorySubscription {
			continue
		}
		if item.Available && !wiredSubscriptionTypes[item.Key] {
			t.Errorf("订阅类型 %q（%s）被标为可用，但它尚未接入："+
				"请先补齐出站适配器并加入 wiredSubscriptionTypes 白名单",
				item.Key, item.Label)
		}
	}
}

// TestTypes_数量下限 防止目录被误删导致静默少上游。
//
// 这类事故很隐蔽：删掉几条数据不会编译失败，站长只会发现"某个上游
// 在后台找不到了"，却无从判断是被删了还是从未登记。因此设一个下限。
func TestTypes_数量下限(t *testing.T) {
	const minCount = 70
	if got := len(Types()); got < minCount {
		t.Fatalf("渠道目录仅 %d 条，少于下限 %d 条；"+
			"目录被误删会静默少上游（后台看不到、站长无从察觉），请核对 catalog.go",
			got, minCount)
	}
}

// TestTypesByCategory_覆盖全部分类且总数一致 校验分组逻辑与目录是否自洽。
//
// 两个关注点：
//  1. 八个大类都必须有类型，否则后台某个分组会长期空白（说明目录漏登记）；
//  2. 各组条目数之和必须等于 len(Types())，否则说明 TypesByCategory 丢项。
func TestTypesByCategory_覆盖全部分类且总数一致(t *testing.T) {
	allCategories := []Category{
		CategoryText, CategoryImage, CategoryVideo, CategoryAudio,
		CategoryEmbedding, CategoryAggregator, CategorySelfHosted, CategorySubscription,
	}

	grouped := TypesByCategory()
	total := 0
	for _, category := range allCategories {
		items := grouped[category]
		if len(items) == 0 {
			t.Errorf("分类 %q（%s）没有任何类型，后台该分组会长期空白，疑似漏登记",
				category, CategoryLabel(category))
		}
		total += len(items)
	}
	if total != len(Types()) {
		t.Errorf("分组条目数之和 %d 与目录总数 %d 不一致，说明 TypesByCategory 丢项",
			total, len(Types()))
	}
}

// TestAzure_必须声明部署名与API版本 验证「类型专属必填参数」机制确实生效。
//
// Azure 与标准 OpenAI 最大的不同就在于这两个参数：没有部署名请求会打到
// 不存在的路径，没有 api-version 会被直接拒绝。它们必须以 Required 形式
// 出现在 ExtraFields 里，后台才会在选中 Azure 时强制站长填写。
func TestAzure_必须声明部署名与API版本(t *testing.T) {
	azure, ok := Find("azure_openai")
	if !ok {
		t.Fatal("未登记 azure_openai 类型")
	}

	required := make(map[string]bool)
	for _, field := range azure.RequiredExtraFields() {
		required[field.Key] = true
	}
	for _, key := range []string{"deployment", "api_version"} {
		if !required[key] {
			t.Errorf("azure_openai 的必填额外参数缺少 %q；"+
				"缺少它后台就不会强制填写，线上会以 400/404 的形式暴露", key)
		}
	}

	// api_version 应带默认值，减少站长填写负担。
	for _, field := range azure.ExtraFields {
		if field.Key == "api_version" && field.Default == "" {
			t.Error("azure_openai 的 api_version 应有默认值，否则站长每次都要查文档")
		}
	}
}
