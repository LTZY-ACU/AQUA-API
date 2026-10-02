// 本文件的职责：构造邮件的主题与正文文案。
//
// 意图（Why）：
//
//	把"用户看到的邮件长什么样"与"邮件怎么发出去"分开。
//	前者是产品文案（会频繁调整），后者是协议细节（很少变动），
//	混在一起会让每次改文案都要动传输代码，容易引入回归。
//
// 设计取舍（为什么是纯内联样式的 HTML）：
//   - 邮件客户端对 CSS 的支持极不一致，<style> 标签常被剥离，
//     因此所有样式必须内联；
//   - 不使用外部图片与字体：外链图片是垃圾邮件评分的重要负面信号；
//   - 正文同时给出"纯文本可读"的关键信息（验证码单独成行、字号大），
//     即使样式被剥离也不影响使用。
package mailer

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// emailCodeCopy 是验证码邮件的可变文案部分（不同用途只差这几句）。
//
// 抽出来的理由：注册 / 登录 / 重置口令三种验证码邮件的**结构完全一致**
// （都是"标题 + 大号验证码 + 有效期说明"），只有文案不同。
// 复制三份 HTML 意味着以后改一次样式要改三处，必然漏。
type emailCodeCopy struct {
	subjectTail string // 主题后缀，如「注册验证码」
	lead        string // 首段说明（告诉用户这封信是干什么的）
	action      string // 验证码的用途陈述
	extra       string // 额外提示；空串表示不显示该段
}

// RegisterCodeEmail 构造注册验证码邮件的主题与 HTML 正文。
//
// 参数：
//   - siteName：站点显示名（来自后台设置）；
//   - code：验证码明文；
//   - ttl：有效期。
func RegisterCodeEmail(siteName, code string, ttl time.Duration) (subject, htmlBody string) {
	return renderEmailCode(siteName, code, ttl, emailCodeCopy{
		subjectTail: "注册验证码",
		lead:        "您正在注册账号，请使用以下验证码完成验证。",
		action:      "验证码",
	})
}

// LoginCodeEmail 构造「邮箱验证码登录」邮件的主题与 HTML 正文。
func LoginCodeEmail(siteName, code string, ttl time.Duration) (subject, htmlBody string) {
	return renderEmailCode(siteName, code, ttl, emailCodeCopy{
		subjectTail: "登录验证码",
		lead:        "您正在使用邮箱验证码登录，请使用以下验证码完成验证。",
		action:      "登录验证码",
		extra:       "若非本人操作，请忽略本邮件，您的账号不会被登录。",
	})
}

// ResetPasswordCodeEmail 构造「重置密码」邮件的主题与 HTML 正文。
func ResetPasswordCodeEmail(siteName, code string, ttl time.Duration) (subject, htmlBody string) {
	return renderEmailCode(siteName, code, ttl, emailCodeCopy{
		subjectTail: "重置密码验证码",
		lead:        "您正在重置账号密码，请使用以下验证码完成验证。",
		action:      "重置验证码",
		// 必须提前告知"改完会踢下线"：否则用户改完密码发现所有设备都要重登，
		// 会以为是站点出了问题，反而来投诉。
		extra: "重置成功后，该账号此前所有登录状态都会失效，需要重新登录。",
	})
}

// renderEmailCode 渲染验证码邮件（三种用途共用同一套结构与样式）。
func renderEmailCode(siteName, code string, ttl time.Duration, c emailCodeCopy) (subject, htmlBody string) {
	// 站点名来自后台设置，属于半可信输入：这里做 HTML 转义，
	// 避免管理员无意间填入的字符破坏邮件结构（或在客户端触发脚本解析）。
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}

	minutes := int(ttl.Minutes())
	if minutes <= 0 {
		minutes = 5
	}

	extraBlock := ""
	if c.extra != "" {
		extraBlock = fmt.Sprintf(`
    <p style="margin:14px 0 0;font-size:12px;color:#94a3b8;line-height:1.7;">%s</p>`, c.extra)
	}

	subject = fmt.Sprintf("【%s】%s", name, c.subjectTail)

	// 用 fmt.Sprintf 拼装而非 html/template：结构固定且只有少量变量，
	// 引入模板引擎的复杂度不值得；转义已在上方显式完成。
	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>%[1]s</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:520px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <h1 style="margin:0 0 8px;font-size:18px;font-weight:600;color:#0f172a;">%[1]s</h1>
    <p style="margin:0 0 20px;font-size:13px;color:#64748b;">%[4]s</p>

    <div style="background:#ecfeff;border:1px solid #a5f3fc;border-radius:12px;padding:18px;text-align:center;">
      <div style="font-size:12px;color:#0e7490;letter-spacing:1px;">%[5]s</div>
      <div style="margin-top:8px;font-size:32px;font-weight:700;letter-spacing:8px;color:#0891b2;font-family:'SFMono-Regular',Consolas,monospace;">%[2]s</div>
    </div>

    <p style="margin:20px 0 0;font-size:13px;color:#475569;line-height:1.7;">
      验证码 <strong>%[3]d 分钟</strong>内有效，且只能使用一次。<br>
      若非本人操作，请忽略本邮件，您的账号不会受到影响。
    </p>
%[6]s

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;">本邮件由系统自动发送，请勿直接回复。</p>
  </div>
</body>
</html>`, name, code, minutes, c.lead, c.action, extraBlock)

	return subject, htmlBody
}

// NewLocationEmail 构造「异地登录提醒」邮件的主题与 HTML 正文。
//
// 为什么必须有这封信：账号被盗的第一现场通常不是"钱没了"，而是
// "有人先在别的地方登进来了"。等到受害者发现额度被刷，损失已经发生；
// 一封即时提醒把察觉时机提前到"还能改密码"的窗口内。
//
// 参数里的 IP / UA / 时间三项刻意全部给出：受害者据此判断
// "这是不是我自己"（例如手机换网后的新出口），避免看到提醒就恐慌。
func NewLocationEmail(siteName, username, ip, userAgent string, at time.Time) (subject, htmlBody string) {
	// 三项来源信息都不是完全可信的输入：
	//   - siteName 来自后台设置；
	//   - UA 完全由客户端控制（可伪造任意长度的字符串）；
	// 二者在拼进 HTML 前都必须转义，否则一封"提醒邮件"自己就成了注入载体。
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}
	account := html.EscapeString(strings.TrimSpace(username))
	clientIP := html.EscapeString(strings.TrimSpace(ip))
	if clientIP == "" {
		clientIP = "未知"
	}
	ua := html.EscapeString(trimUserAgent(userAgent, 160))
	if ua == "" {
		ua = "未提供"
	}
	timeText := at.Format("2006-01-02 15:04:05")
	if at.IsZero() {
		timeText = "未知"
	}

	subject = fmt.Sprintf("【%s】账号异地登录提醒", name)

	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>%[1]s</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:520px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <h1 style="margin:0 0 8px;font-size:18px;font-weight:600;color:#0f172a;">检测到异地登录</h1>
    <p style="margin:0 0 20px;font-size:13px;color:#64748b;">您的账号刚刚从一个与以往不同的网络位置登录成功。</p>

    <table style="width:100%%;border-collapse:collapse;font-size:13px;color:#334155;">
      <tr><td style="padding:8px 0;color:#64748b;width:88px;">账号</td><td style="padding:8px 0;">%[2]s</td></tr>
      <tr><td style="padding:8px 0;color:#64748b;">登录 IP</td><td style="padding:8px 0;font-family:'SFMono-Regular',Consolas,monospace;">%[3]s</td></tr>
      <tr><td style="padding:8px 0;color:#64748b;">登录时间</td><td style="padding:8px 0;">%[4]s</td></tr>
      <tr><td style="padding:8px 0;color:#64748b;">设备信息</td><td style="padding:8px 0;word-break:break-all;">%[5]s</td></tr>
    </table>

    <p style="margin:20px 0 0;font-size:13px;color:#475569;line-height:1.7;">
      如果这次登录是您本人的操作（例如更换了网络、使用了新的设备），可以放心忽略本邮件。<br>
      如果不是您本人操作，请立即登录站点修改密码——修改密码会让此前的登录状态全部失效。
    </p>

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;">本邮件由系统自动发送，请勿直接回复。</p>
  </div>
</body>
</html>`, name, account, clientIP, timeText, ua)

	return subject, htmlBody
}

// AnnouncementEmail 构造「站点公告」邮件的主题与 HTML 正文。
//
// 为什么单独一个模板而不是复用群发模板目录：群发模板是站方反复使用的固定文案
// （计费专线等），由 RenderBroadcast 按 key 渲染；而公告标题与正文是站长这一次
// 现写的内容，必须先落成字符串快照存进批次表，"预览看到的就是发出去的"
// 这条约定才不会被后续的模板改动破坏。
//
// 参数里的 levelText 是公告的语气（普通/喜报/警告/故障）：
// 它决定邮件的强调色，让收件人在收件箱里就能分辨"这是通知"还是"这是故障"。
func AnnouncementEmail(siteName, title, content, levelText string) (subject, htmlBody string) {
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}
	heading := html.EscapeString(strings.TrimSpace(title))
	if heading == "" {
		heading = "站点公告"
	}
	levelLabel := html.EscapeString(strings.TrimSpace(levelText))
	if levelLabel == "" {
		levelLabel = "普通"
	}
	// 正文按行处理：公告通常是多行短文。空行分段、其余各成一行，
	// 避免管理员精心排的段落被压成一坨看不出结构。
	bodyHTML := ""
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyHTML += "\n"
			continue
		}
		if bodyHTML != "" {
			bodyHTML += "\n      "
		}
		bodyHTML += fmt.Sprintf(`<p style="margin:0 0 10px;font-size:14px;color:#334155;line-height:1.75;">%s</p>`,
			html.EscapeString(trimmed))
	}
	if strings.TrimSpace(bodyHTML) == "" {
		bodyHTML = `<p style="margin:0;font-size:14px;color:#64748b;">（公告正文为空）</p>`
	}

	// 语气→强调色：只覆盖四种公告级别，未命中一律走中性蓝。
	accent, bg := "#0891b2", "#ecfeff"
	switch levelText {
	case "喜报":
		accent, bg = "#059669", "#ecfdf5"
	case "警告":
		accent, bg = "#d97706", "#fffbeb"
	case "故障":
		accent, bg = "#dc2626", "#fef2f2"
	}

	subject = fmt.Sprintf("【%s】%s", name, heading)

	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>%[1]s</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <div style="display:inline-block;padding:3px 10px;border-radius:999px;font-size:12px;color:%[4]s;background:%[5]s;">%[3]s</div>
    <h1 style="margin:12px 0 18px;font-size:18px;font-weight:600;color:#0f172a;">%[2]s</h1>

    <div style="font-size:14px;color:#334155;">
      %[6]s
    </div>

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;line-height:1.7;">
      您收到这封信是因为在本站绑定了邮箱。本邮件由系统自动发送，请勿直接回复。
    </p>
  </div>
</body>
</html>`, name, heading, levelLabel, accent, bg, bodyHTML)

	return subject, htmlBody
}

// trimUserAgent 截断用于展示的 UA。
//
// UA 完全由客户端控制，可以有几千个字符；原样放进表格会把邮件撑成一屏看不全的噪声，
// 因此超过 max 的部分以省略号收尾（仍保留头部，那里才是浏览器/系统的标识区）。
func trimUserAgent(ua string, max int) string {
	ua = strings.TrimSpace(ua)
	if len(ua) <= max {
		return ua
	}
	return strings.TrimSpace(ua[:max]) + "…"
}

// ---------------------------------------------------------------------------
// 全站通知邮件（群发）模板目录
// ---------------------------------------------------------------------------

// BroadcastTemplate 描述一个可在后台选用的全站通知模板。
type BroadcastTemplate struct {
	Key   string // 模板键（落库到 EmailBroadcast.Template，仅作分类与审计用）
	Label string // 后台下拉里的展示名
}

// BroadcastTemplateBillingLine 是「计费专线上线 + 官方交流群」通知的模板键。
const BroadcastTemplateBillingLine = "billing_line"

// BroadcastTemplateCallLine 是「按次计费专线上线 + 上线优惠」通知的模板键。
//
// 与 billing_line 的区别：billing_line 讲的是**按量计费**的 AQUA/ 专线，
// 本模板讲的是**按次计费**的 AQUA-CALL/ 专线，两者是并列的两条线，不可混用。
const BroadcastTemplateCallLine = "call_line"

// BroadcastTemplateTrialGrant 是「限时试用额已发放」通知的模板键。
//
// 注意：本模板里的金额与时长是**写死的**（0.1 元 / 24 小时），
// 因为渲染入口只接收站点名，没有"每次发送时传参"的通道。
// 因此后台的模板名里也标注了口径；若将来要发别的金额/时长，
// 应给 RenderBroadcast 加参数而不是改这里的文案（否则历史邮件与文案会对不上）。
const BroadcastTemplateTrialGrant = "trial_grant"

// BroadcastTemplates 返回可用的通知模板清单。
//
// 为什么做成目录而不是让调用方直接调具体函数：后台下拉据此渲染，
// 新增一个通知模板（如"价格调整""维护窗口"）只需在下面加一条 + 写一个渲染函数，
// 前后端都不必改动（与 channel-types / key-strategies 的做法一致）。
func BroadcastTemplates() []BroadcastTemplate {
	return []BroadcastTemplate{
		{Key: BroadcastTemplateBillingLine, Label: "计费专线上线 + 官方交流群"},
		{Key: BroadcastTemplateCallLine, Label: "按次计费专线上线 + 上线优惠"},
		{Key: BroadcastTemplateTrialGrant, Label: "限时试用额已发放（0.1 元 / 24 小时）"},
	}
}

// RenderBroadcast 按模板键渲染通知邮件的主题与 HTML 正文。
//
// 返回 ok=false 表示模板键未知。调用方【必须】据此拒绝请求，
// 而不是降级发一封空邮件出去 —— 群发无法撤回，宁可报错也不发错。
func RenderBroadcast(templateKey, siteName string) (subject, htmlBody string, ok bool) {
	switch templateKey {
	case BroadcastTemplateBillingLine:
		return billingLineEmail(siteName)
	case BroadcastTemplateCallLine:
		return callLineEmail(siteName)
	case BroadcastTemplateTrialGrant:
		return trialGrantEmail(siteName)
	default:
		return "", "", false
	}
}

// billingLineEmail 构造「计费专线上线 + 交流群」通知的主题与正文。
//
// 三条硬约束（改动文案时必须守住）：
//  1. 正文【不出现任何网址/超链接】，包括本站域名 —— 运营要求"引导用户前往官网"
//     但不挂链接；同时纯文本对垃圾邮件评分更友好；
//  2. 不承诺缓存命中折扣（上游实测 cached_tokens 恒为 0），
//     也不提上游会注入系统提示等实现细节：说了却兑现不了、或把用户劝退，都是负收益；
//  3. 不使用「官方中转」「厂商官网价 N 折」「高速稳定」等表述 —— 这类措辞涉及
//     与厂商关系的暗示、绝对化宣传，是邮件与页面被投诉/被要求整改的高频触发点。
//     价格一律表述为"低于标准档位，以模型广场公示为准"。
func billingLineEmail(siteName string) (subject, htmlBody string, ok bool) {
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}

	subject = fmt.Sprintf("【%s】计费专线已上线", name)

	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>计费专线已上线</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <h1 style="margin:0 0 8px;font-size:18px;font-weight:600;color:#0f172a;">%[1]s</h1>
    <p style="margin:0 0 22px;font-size:13px;color:#64748b;">这是一封站点通知，有两件新东西想告诉你。</p>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">一、计费专线已上线</h2>
    <p style="margin:0 0 12px;font-size:13px;color:#475569;line-height:1.8;">
      计费专线由第三方模型接口服务提供，按量计费，适合生产环境使用。
    </p>
    <ul style="margin:0 0 12px;padding-left:20px;font-size:13px;color:#475569;line-height:1.9;">
      <li>单价低于标准档位，具体以「模型广场」公示的价格为准</li>
      <li>首期覆盖 7 个主力模型，对外名称统一带 <code style="font-family:Consolas,monospace;">AQUA/</code> 前缀：
        deepseek-v4-flash、deepseek-v4-pro、deepseek-v4.1-flash、doubao-seed-2.1-turbo、
        doubao-seed-evolving、glm-5.3、glm-5.3-flash</li>
      <li>按服务端返回的实际用量结算：输入与输出分别计价，每一笔都能在「调用日志」里
        查到输入 / 输出 token 与扣费金额</li>
      <li>原有免费分组保持不变、照常可用；计费专线是多出来的一个选择，不影响你现在的调用</li>
    </ul>
    <div style="background:#f8fafc;border:1px solid #e2e8f0;border-radius:12px;padding:16px;margin:0 0 8px;">
      <div style="font-size:12px;color:#64748b;letter-spacing:.5px;margin-bottom:8px;">怎么开始用（在站点操作，三步）</div>
      <ol style="margin:0;padding-left:20px;font-size:13px;color:#0f172a;line-height:1.9;">
        <li>进入「访问令牌」页，新建一个访问令牌</li>
        <li>把新令牌的「所属分组」选为「计费专线」</li>
        <li>调用时模型名填 <code style="font-family:Consolas,monospace;">AQUA/</code> 开头的名称（例如 <code style="font-family:Consolas,monospace;">AQUA/glm-5.3</code>）</li>
      </ol>
    </div>
    <p style="margin:8px 0 22px;font-size:12px;color:#94a3b8;line-height:1.7;">
      建议新建令牌而不是改现有的那一个：免费与计费并存，方便对比，也方便随时切回去。
      各模型的详细价格与全部可用模型，请在「模型广场」查看。
    </p>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">二、交流群已开通</h2>
    <p style="margin:0 0 10px;font-size:13px;color:#475569;line-height:1.8;">
      站点首页新增了「加入交流群」入口，点进去可以看到两个 QQ 群的群号与入群入口。
    </p>
    <ul style="margin:0 0 10px;padding-left:20px;font-size:13px;color:#475569;line-height:1.9;">
      <li>主群（AQUA开源项目交流群）：<strong style="font-family:Consolas,monospace;">1103667832</strong></li>
      <li>备用群（AQUA开源项目交流二群）：<strong style="font-family:Consolas,monospace;">1006740220</strong></li>
    </ul>
    <p style="margin:0 0 22px;font-size:13px;color:#475569;line-height:1.8;">
      建议先加主群，主群满员了再加备用群。使用中遇到的问题、想要的模型、对计费的疑问，
      都可以直接在群里问，比等邮件回复快得多，其他用户也能一起参考。
    </p>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">三、用量较大的用户，欢迎对接专属方案</h2>
    <p style="margin:0 0 22px;font-size:13px;color:#475569;line-height:1.8;">
      如果你的用量较大，或者有接入、定制方面的需求，欢迎加入交流群后私信管理员对接。
      我们会按你的实际用量谈专属方案，量大从优。
    </p>

    <p style="margin:0;font-size:13px;color:#475569;">感谢你的使用。<br>%[1]s</p>

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;">本邮件由系统自动发送，请勿直接回复。</p>
  </div>
</body>
</html>`, name)

	return subject, htmlBody, true
}

// trialGrantEmail 构造「限时试用额已发放」通知的主题与正文。
//
// 与另外两个模板共用同一套硬约束（改文案时必须一并守住）：
//  1. 正文【不出现任何网址/超链接】，包括本站域名；
//  2. 不承诺缓存命中折扣，也不提上游实现细节；
//  3. 不使用"厂商官网价 N 折""高速稳定"等绝对化/攀附性表述。
//
// 本模板与它们唯一的区别：**必须明确写出金额与有效期**。
// 原因：这封信的全部意义就是让用户知道"有 0.1 元、只有 24 小时"，
// 不写金额用户不会去用，不写期限用户会以为额度丢失。这也是它
// 与"上线通知"（刻意不披露定价）在此处的口径差异。
//
// 金额与时长是写死的（渲染入口只接收站点名），因此后台模板名里也标注了口径；
// 将来若要换金额，应给 RenderBroadcast 加参数，而不是改这里的文案。
func trialGrantEmail(siteName string) (subject, htmlBody string, ok bool) {
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}

	subject = fmt.Sprintf("【%s】限时试用额已发放", name)

	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>限时试用额已发放</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <h1 style="margin:0 0 8px;font-size:18px;font-weight:600;color:#0f172a;">%[1]s</h1>
    <p style="margin:0 0 22px;font-size:13px;color:#64748b;">这是一封站点通知，有一笔额度已经送到你的账户。</p>

    <div style="background:#ecfeff;border:1px solid #a5f3fc;border-radius:12px;padding:18px;text-align:center;margin:0 0 20px;">
      <div style="font-size:12px;color:#0e7490;letter-spacing:1px;">限时试用额</div>
      <div style="margin-top:8px;font-size:30px;font-weight:700;color:#0891b2;font-family:'SFMono-Regular',Consolas,monospace;">0.1 元</div>
      <div style="margin-top:8px;font-size:12px;color:#0e7490;">自发放起 <strong>24 小时</strong>内有效，到期未用完的部分自动收回</div>
    </div>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">一、这笔额度怎么用</h2>
    <ul style="margin:0 0 12px;padding-left:20px;font-size:13px;color:#475569;line-height:1.9;">
      <li>已经直接充入你的账户，<strong>不需要领取、也不需要兑换码</strong></li>
      <li>扣费时<strong>优先抵扣试用额</strong>，它与你自己充值的余额可以一起使用</li>
      <li>在「概览」页可以看到这笔额度的剩余金额与倒计时</li>
      <li>有效期内用掉的部分就是你的；过期后<strong>未用完的部分会被收回</strong>，请尽快体验</li>
    </ul>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">二、推荐用来体验按次计费专线</h2>
    <p style="margin:0 0 10px;font-size:13px;color:#475569;line-height:1.8;">
      按次专线<strong>按调用次数计费</strong>：一个请求一个价，提示词再长也不额外加价。
      首期覆盖 8 个模型，对外名称统一带 <code style="font-family:Consolas,monospace;">AQUA-CALL/</code> 前缀：
      deepseek-v4-flash、deepseek-v4.1-flash、deepseek-v4-pro、glm-5.3、glm-5.3-flash、
      kimi-k3、gpt-image-2-w、gpt-image-2.5。
    </p>

    <div style="background:#f8fafc;border:1px solid #e2e8f0;border-radius:12px;padding:16px;margin:0 0 8px;">
      <div style="font-size:12px;color:#64748b;letter-spacing:.5px;margin-bottom:8px;">怎么开始用（在站点操作，三步）</div>
      <ol style="margin:0;padding-left:20px;font-size:13px;color:#0f172a;line-height:1.9;">
        <li>进入「访问令牌」页，新建一个访问令牌</li>
        <li>把新令牌的「所属分组」选为「按次专线」</li>
        <li>调用时模型名填 <code style="font-family:Consolas,monospace;">AQUA-CALL/</code> 开头的名称（例如 <code style="font-family:Consolas,monospace;">AQUA-CALL/glm-5.3</code>）</li>
      </ol>
    </div>
    <p style="margin:8px 0 22px;font-size:12px;color:#94a3b8;line-height:1.7;">
      已经有令牌的话，也可以直接新建一个"按次专线"分组的令牌来体验，
      原有令牌与免费分组不受影响。
    </p>

    <p style="margin:0;font-size:13px;color:#475569;">感谢你的使用。<br>%[1]s</p>

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;">本邮件由系统自动发送，请勿直接回复。</p>
  </div>
</body>
</html>`, name)

	return subject, htmlBody, true
}

// callLineEmail 构造「按次计费专线上线 + 上线优惠」通知的主题与正文。
//
// 与 billingLineEmail 共用同一套硬约束（改文案时必须一并守住）：
//  1. 正文【不出现任何网址/超链接】，包括本站域名；
//  2. 不承诺缓存命中折扣，也不提上游会注入系统提示等实现细节；
//  3. 不使用"厂商官网价 N 折""高速稳定"等绝对化/攀附性表述，
//     价格一律表述为"低于标准档位，以模型广场公示为准"。
//
// 本模板额外的一条（运营明确要求）：
//  4. 【不出现任何具体价格与门槛数字】—— 只说明"新分组已上线""上线优惠进行中"，
//     金额、折扣、充值门槛一律让用户去站点自行查看。
//     因此价格档位（0.002/0.005/0.01/0.03）、大客户门槛（50 元）、
//     以及只对管理员开放的批发价分组，都刻意不出现在正文里。
func callLineEmail(siteName string) (subject, htmlBody string, ok bool) {
	name := html.EscapeString(strings.TrimSpace(siteName))
	if name == "" {
		name = "AQUA-API"
	}

	subject = fmt.Sprintf("【%s】按次计费专线已上线", name)

	htmlBody = fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="UTF-8"><title>按次计费专线已上线</title></head>
<body style="margin:0;padding:24px;background:#f1f5f9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;color:#0f172a;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:14px;padding:28px;">
    <h1 style="margin:0 0 8px;font-size:18px;font-weight:600;color:#0f172a;">%[1]s</h1>
    <p style="margin:0 0 22px;font-size:13px;color:#64748b;">这是一封站点通知，有两件新东西想告诉你。</p>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">一、新分组：按次计费专线</h2>
    <p style="margin:0 0 12px;font-size:13px;color:#475569;line-height:1.8;">
      按次专线由第三方模型接口服务提供，<strong>按调用次数计费</strong>：
      一个请求一个价，<strong>提示词再长也不额外加价</strong>。
      适合长提示词、批量任务，以及调用量比较好预估的场景。
    </p>
    <ul style="margin:0 0 12px;padding-left:20px;font-size:13px;color:#475569;line-height:1.9;">
      <li>首期覆盖 8 个模型，对外名称统一带 <code style="font-family:Consolas,monospace;">AQUA-CALL/</code> 前缀：
        deepseek-v4-flash、deepseek-v4.1-flash、deepseek-v4-pro、glm-5.3、glm-5.3-flash、
        kimi-k3、gpt-image-2-w、gpt-image-2.5</li>
      <li>上线即开放两个分组：
        <strong>「按次专线」对所有用户直接可选</strong>；
        用量较大的用户还可使用<strong>「按次专线（大客户）」</strong>优惠档</li>
      <li>每次调用都在「调用日志」里留痕：可以查到所用模型与本次扣费</li>
      <li>原有分组保持不变、照常可用；按次专线是多出来的一个选择，不影响你现在的调用</li>
    </ul>

    <h2 style="margin:0 0 10px;font-size:15px;font-weight:600;color:#0f172a;">二、上线优惠</h2>
    <p style="margin:0 0 12px;font-size:13px;color:#475569;line-height:1.8;">
      适逢新线上线，按次专线推出<strong>上线优惠</strong>：专线价格低于标准档位，
      活动期间的力度还会更大。具体价格与参与方式，以「模型广场」公示为准。
    </p>
    <p style="margin:0 0 22px;font-size:13px;color:#475569;line-height:1.8;">
      如果你用量较大，或者有接入、定制方面的需求，欢迎在站点首页进入「加入交流群」
      后私信管理员对接，我们会按你的实际用量谈专属方案，量大从优。
    </p>

    <div style="background:#f8fafc;border:1px solid #e2e8f0;border-radius:12px;padding:16px;margin:0 0 8px;">
      <div style="font-size:12px;color:#64748b;letter-spacing:.5px;margin-bottom:8px;">怎么开始用（在站点操作，三步）</div>
      <ol style="margin:0;padding-left:20px;font-size:13px;color:#0f172a;line-height:1.9;">
        <li>进入「访问令牌」页，新建一个访问令牌</li>
        <li>把新令牌的「所属分组」选为「按次专线」（用量较大的用户可选「按次专线（大客户）」）</li>
        <li>调用时模型名填 <code style="font-family:Consolas,monospace;">AQUA-CALL/</code> 开头的名称（例如 <code style="font-family:Consolas,monospace;">AQUA-CALL/glm-5.3</code>）</li>
      </ol>
    </div>
    <p style="margin:8px 0 22px;font-size:12px;color:#94a3b8;line-height:1.7;">
      建议新建令牌而不是改现有的那一个：免费与计费并存，方便对比，也方便随时切回去。
      各模型的详细价格与全部可用模型，请在「模型广场」查看。
    </p>

    <p style="margin:0;font-size:13px;color:#475569;">感谢你的使用。<br>%[1]s</p>

    <hr style="margin:22px 0 14px;border:none;border-top:1px solid #e2e8f0;">
    <p style="margin:0;font-size:12px;color:#94a3b8;">本邮件由系统自动发送，请勿直接回复。</p>
  </div>
</body>
</html>`, name)

	return subject, htmlBody, true
}
