// 全站通知邮件（群发）模板的单元测试。
//
// 测试重点（为什么测这些）：
//   - 正文【不含任何网址/超链接】：这是运营给出的硬要求（引导去官网但不挂链接），
//     同时纯文本对垃圾邮件评分更友好；一旦有人在改文案时顺手贴了个链接，
//     本用例会立刻拦住；
//   - 关键信息必须在：计费专线、7 个模型名、三步操作、两个群号 —— 少一个，
//     用户收到的就是一封"说了等于没说"的通知，而群发发了就收不回来；
//   - 【合规红线】：不得出现「官方中转」「厂商官网价」「高速稳定」等表述 ——
//     这类措辞涉及与厂商关系的暗示与绝对化宣传，是邮件被投诉/被要求整改的高频触发点，
//     用测试把它钉死，避免以后改文案时又被加回来；
//   - 站点名必须转义：它来自后台设置，属于半可信输入。
package mailer

import (
	"strings"
	"testing"
)

// TestRenderBroadcast_模板目录与未知键 覆盖"目录里有 key、渲染器不认的键必须失败"。
func TestRenderBroadcast_模板目录与未知键(t *testing.T) {
	templates := BroadcastTemplates()
	if len(templates) == 0 {
		t.Fatal("模板目录不应为空，否则后台无从选择")
	}
	found := false
	for _, item := range templates {
		if item.Key == BroadcastTemplateBillingLine {
			found = true
		}
		if item.Label == "" {
			t.Errorf("模板 %s 缺少展示名（后台下拉会是空白项）", item.Key)
		}
	}
	if !found {
		t.Fatalf("模板目录里缺少 %s", BroadcastTemplateBillingLine)
	}

	// 未知键必须返回 ok=false：调用方据此拒绝请求，而不是发一封空邮件出去
	if _, _, ok := RenderBroadcast("not-a-template", "LTZY-API"); ok {
		t.Fatal("未知模板键应返回 ok=false")
	}
}

// TestRenderBroadcast_计费专线通知关键信息齐全且无链接 覆盖内容约束。
func TestRenderBroadcast_计费专线通知关键信息齐全且无链接(t *testing.T) {
	subject, body, ok := RenderBroadcast(BroadcastTemplateBillingLine, "LTZY-API")
	if !ok {
		t.Fatal("计费专线模板应可渲染")
	}

	if !strings.Contains(subject, "LTZY-API") || !strings.Contains(subject, "计费专线") {
		t.Errorf("主题应含站点名与核心信息（计费专线已上线），实际 %q", subject)
	}

	// 关键信息逐项核对（每一项缺失都会让通知失去意义）
	required := []string{
		"计费专线", "模型广场",
		"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4.1-flash",
		"doubao-seed-2.1-turbo", "doubao-seed-evolving",
		"glm-5.3", "glm-5.3-flash",
		"访问令牌", "所属分组",
		"免费分组",
		"1103667832", "1006740220",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少关键信息 %q", want)
		}
	}

	// 合规红线：这些措辞会暗示与厂商的关系或构成绝对化宣传，一旦出现即视为回归
	for _, banned := range []string{"官方中转", "官网价", "高速稳定", "免费公益"} {
		if strings.Contains(body, banned) {
			t.Errorf("正文不应出现 %q（合规红线：暗示官方关系或绝对化宣传）", banned)
		}
	}

	// 硬约束：不含任何网址（含本站域名），也不含 <a> 超链接
	for _, forbidden := range []string{"http://", "https://", "<a ", "ltzy.top", "www."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("正文不应出现 %q（运营要求引导去官网但不挂链接）", forbidden)
		}
	}

	// 不该出现的承诺：缓存命中折扣（上游实测 cached_tokens 恒为 0，说了兑现不了）
	if strings.Contains(body, "缓存") {
		t.Error("正文不应承诺缓存相关能力（实测上游不回报缓存命中）")
	}
}

// TestRenderBroadcast_按次专线通知关键信息齐全且不泄露定价 覆盖内容与运营约束。
//
// 与计费专线模板的区别：本模板面向"按次计费"的 LTZY-CALL/ 专线，
// 且运营明确要求【不得出现任何具体价格/折扣/充值门槛数字】——
// 只告知"新分组已上线""上线优惠进行中"。这条一旦被改文案时破坏，
// 就等于把定价方案直接群发给全体用户，故用测试钉死。
func TestRenderBroadcast_按次专线通知关键信息齐全且不泄露定价(t *testing.T) {
	subject, body, ok := RenderBroadcast(BroadcastTemplateCallLine, "LTZY-API")
	if !ok {
		t.Fatal("按次专线模板应可渲染")
	}

	if !strings.Contains(subject, "LTZY-API") || !strings.Contains(subject, "按次") {
		t.Errorf("主题应含站点名与核心信息（按次专线已上线），实际 %q", subject)
	}

	// 关键信息逐项核对（每一项缺失都会让通知失去意义）
	required := []string{
		"按次专线", "LTZY-CALL/", "上线优惠", "模型广场",
		"deepseek-v4-flash", "deepseek-v4.1-flash", "deepseek-v4-pro",
		"glm-5.3", "glm-5.3-flash", "kimi-k3",
		"gpt-image-2-w", "gpt-image-2.5",
		"访问令牌", "所属分组",
	}
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少关键信息 %q", want)
		}
	}

	// 【运营硬约束】不得出现任何具体价格 / 折扣 / 门槛数字
	// （四档价 0.002/0.005/0.01/0.03、大客户门槛 50 元等一律不写进邮件）
	for _, leaked := range []string{"0.002", "0.005", "0.01", "0.03", "元", "折", "%"} {
		if strings.Contains(body, leaked) {
			t.Errorf("正文不应出现 %q（运营要求：不向用户披露定价）", leaked)
		}
	}
	// 只对管理员开放的批发价分组绝不能出现在面向全体用户的邮件里
	if strings.Contains(body, "代理拿货") {
		t.Error("正文不应出现仅后台可分发分组（代理拿货）")
	}

	// 合规红线：暗示官方关系或绝对化宣传的措辞
	for _, banned := range []string{"官方中转", "官网价", "高速稳定", "免费公益"} {
		if strings.Contains(body, banned) {
			t.Errorf("正文不应出现 %q（合规红线：暗示官方关系或绝对化宣传）", banned)
		}
	}

	// 硬约束：不含任何网址（含本站域名），也不含 <a> 超链接
	for _, forbidden := range []string{"http://", "https://", "<a ", "ltzy.top", "www."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("正文不应出现 %q（运营要求引导去官网但不挂链接）", forbidden)
		}
	}

	// 不该出现的承诺：缓存命中折扣（上游实测 cached_tokens 恒为 0）
	if strings.Contains(body, "缓存") {
		t.Error("正文不应承诺缓存相关能力（实测上游不回报缓存命中）")
	}
}

// TestRenderBroadcast_试用额通知写明金额与期限 覆盖本模板特有的口径要求。
//
// 与前两个模板相反：这封信**必须**写出金额（0.1 元）与期限（24 小时），
// 否则用户既不会去用、过期后还会以为额度凭空消失。
// 但模型单价仍然一个字都不能出现（写了就等于把定价方案群发出去）。
func TestRenderBroadcast_试用额通知写明金额与期限(t *testing.T) {
	subject, body, ok := RenderBroadcast(BroadcastTemplateTrialGrant, "LTZY-API")
	if !ok {
		t.Fatal("试用额模板应可渲染")
	}
	if !strings.Contains(subject, "LTZY-API") || !strings.Contains(subject, "试用额") {
		t.Errorf("主题应含站点名与核心信息（试用额已发放），实际 %q", subject)
	}

	// 必须写明的两项信息
	for _, want := range []string{"0.1 元", "24 小时", "试用额", "概览", "优先"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文应明确提到 %q（写不清用户就不会用，过期后还会以为额度丢了）", want)
		}
	}

	// 引导到按次专线的关键信息
	for _, want := range []string{"LTZY-CALL/", "按次专线", "访问令牌", "所属分组", "glm-5.3"} {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少关键信息 %q", want)
		}
	}

	// 模型单价一律不得出现（写了就是把定价方案群发出去）
	for _, leaked := range []string{"0.002", "0.005", "0.03"} {
		if strings.Contains(body, leaked) {
			t.Errorf("正文不应出现模型单价 %q", leaked)
		}
	}

	// 合规红线与硬约束（与其它模板一致）
	for _, banned := range []string{"官方中转", "官网价", "高速稳定", "免费公益"} {
		if strings.Contains(body, banned) {
			t.Errorf("正文不应出现 %q（合规红线）", banned)
		}
	}
	for _, forbidden := range []string{"http://", "https://", "<a ", "ltzy.top", "www."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("正文不应出现 %q（运营要求引导去官网但不挂链接）", forbidden)
		}
	}
	if strings.Contains(body, "缓存") {
		t.Error("正文不应承诺缓存相关能力（实测上游不回报缓存命中）")
	}
}

// TestRenderBroadcast_站点名被转义 覆盖"半可信输入不得破坏邮件结构"。
func TestRenderBroadcast_站点名被转义(t *testing.T) {
	_, body, ok := RenderBroadcast(BroadcastTemplateBillingLine, `<script>x</script>`)
	if !ok {
		t.Fatal("模板应可渲染")
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("站点名中的标签必须被转义，否则会在邮件客户端里被当作脚本解析")
	}

	// 站点名为空时回退默认名，而不是留下一处空白
	_, fallback, _ := RenderBroadcast(BroadcastTemplateBillingLine, "   ")
	if !strings.Contains(fallback, "LTZY-API") {
		t.Error("站点名为空时应回退为 LTZY-API")
	}
}
