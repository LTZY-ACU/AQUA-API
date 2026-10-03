// 本文件实现「上游失败的语义分类」与「retry-after 解析」。
//
// 意图（Why）：
//
//	代理档是 6 折，重试率 r（= 上游调用次数 / 计费请求次数）直接决定盈亏。
//	因此"失败了要不要重试、往哪个方向重试"必须按失败语义精确分流，而不是一刀切：
//	  · 429 限流        → 只冷却该凭据，【不换渠道】（换渠道只会把限流扩散到别的上游）；
//	  · 401/403/402     → 长冷却该凭据，同样【不换渠道】（错误来自凭据本身）；
//	  · 5xx/超时/连接失败 → 属渠道级故障，换渠道；
//	  · 内容审核拦截     → 换谁都会被拦，【不重试也不换渠道】，避免白烧上游额度；
//	  · 200 但空内容     → 可降级的失败（推理模型吃满 token 预算的真实坑），走渠道级重试。
//	另外，429 若带 Retry-After，冷却时长按上游给的来，而不是固定退避。
//
// 流转（Flow）：
//
//	forwardChat 拿到上游响应
//	  └─ classifyKeyFailure（既有：判定与凭据的关系）
//	       └─ classifyUpstreamFailure（本文件：语义分类 + 响应体片段）
//	            └─ 据此决定 forwardOutcome（换密钥 / 换渠道 / 不重试）
//	429 分支额外调用 retryAfterCooldown 解析 Retry-After 覆盖默认冷却。
//
// 扩展（Extend）：
//
//	新增一类失败语义：在 upstreamFailureClass 加枚举 → 在 classifyUpstreamFailure 加判据 →
//	在 forwardChat 的 switch 补分支 → 在本文件的测试补用例，四处同步。
package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// upstreamFailureClass 表示「一次上游失败该往哪个方向重试」的语义分类。
//
// 它是 forwardChat 重试决策的唯一依据：分类错了，重试方向就错了——
// 要么把限流扩散到其他渠道，要么内容被拦还反复烧额度。
type upstreamFailureClass int

const (
	// failureClassNone 与凭据、渠道都无关（请求参数有误、授权范围问题等），不重试。
	failureClassNone upstreamFailureClass = iota
	// failureClassRateLimited 429 限流：冷却该凭据，可换同渠道其他凭据，【禁止换渠道】。
	failureClassRateLimited
	// failureClassCredential 401/403/402 鉴权/欠费：长冷却该凭据，可换同渠道其他凭据，【禁止换渠道】。
	failureClassCredential
	// failureClassChannel 5xx / 超时 / 连接失败，属渠道级故障，应换渠道。
	failureClassChannel
	// failureClassContentFilter 命中上游内容审核拦截：换密钥与换渠道都无用，不重试。
	failureClassContentFilter
)

// contentFilterMarkers 是上游「内容审核拦截」的文本特征。
//
// 为什么用文本判据：各厂商用词与状态码都不统一（OpenAI 用 content_policy_violation、
// Azure 用 content management policy / Responsible AI、通义用 data_inspection_failed），
// 只看状态码（400/403）无法与真正的参数错误、鉴权失败区分开。
//
// 选取原则：只收录足够具体的短语，避免把普通错误（如"content must be a string"）误判为审核拦截。
var contentFilterMarkers = []string{
	"content_filter",
	"content filter",
	"content_policy",
	"content policy",
	"content management policy",
	"data_inspection_failed",
	"flagged",
	"moderation",
	"safety system",
	"responsible ai",
}

// isContentFiltered 判断响应体是否表明被上游内容审核拦截。
func isContentFiltered(status int, body []byte) bool {
	if status < http.StatusBadRequest || len(body) == 0 {
		return false
	}
	lowered := strings.ToLower(string(body))
	for _, marker := range contentFilterMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// classifyUpstreamFailure 依据上游响应把一次失败归入语义分类，并返回响应体片段（供日志）。
//
// 参数 snippet 是调用方已读出的响应体前缀（classifyKeyFailure 的产物，可能为空）；
// 为空且需要一个判据时才由本函数自行窥探，避免对每个失败都重复读一遍响应体。
//
// 判定顺序（important）：内容审核必须最先判——上游常用 400/403 表达它，
// 若先按状态码分流，会被误当成鉴权/参数问题而白白重试（换谁都会被拦）。
func classifyUpstreamFailure(resp *http.Response, snippet []byte) (upstreamFailureClass, []byte) {
	if resp == nil {
		return failureClassNone, nil
	}
	status := resp.StatusCode

	body := snippet
	if len(body) == 0 && status >= http.StatusBadRequest {
		if peek, err := peekBody(resp, keyLevelPeekBytes); err == nil && len(peek) > 0 {
			body = peek
		}
	}
	if isContentFiltered(status, body) {
		return failureClassContentFilter, body
	}

	switch {
	case status == http.StatusTooManyRequests:
		return failureClassRateLimited, body
	case status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusPaymentRequired:
		return failureClassCredential, body
	case status == http.StatusRequestTimeout || status >= http.StatusInternalServerError:
		// 408 与 5xx（含 529 过载）都归为渠道级故障。
		return failureClassChannel, body
	default:
		return failureClassNone, body
	}
}

// retryAfterMin 与 retryAfterMax 是采信上游 Retry-After 的区间。
//
// 太短（< 1 秒）：退避本身就够了，采信它没有收益；
// 太长（> 24 小时）：多半是上游给了异常值，照做等于把凭据长期闲置，
// 因此夹到上限，既尊重上游又防止被一个畸形值长期冻住。
const (
	retryAfterMin = time.Second
	retryAfterMax = 24 * time.Hour
)

// retryAfterCooldown 解析上游 429 响应头 Retry-After，得到冷却时长。
//
// 支持两种规范格式：
//   - 秒数（delay-seconds），如 "120"；
//   - HTTP 日期（RFC1123 等），如 "Wed, 21 Oct 2015 07:28:00 GMT"。
//
// 容错原则（重要）：缺失、非法、过去时间一律返回 0，由调用方回退到默认退避——
// 绝不允许"解析失败"导致"不冷却"（否则一把被限流的凭据会被反复推向上游）。
func retryAfterCooldown(header http.Header, now time.Time) time.Duration {
	if header == nil {
		return 0
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return clampRetryAfter(time.Duration(seconds) * time.Second)
	}

	if at, err := http.ParseTime(raw); err == nil {
		delay := at.Sub(now)
		if delay <= 0 {
			return 0
		}
		return clampRetryAfter(delay)
	}
	return 0
}

// clampRetryAfter 把 retry-after 提示夹到可信区间。
//
// 返回 0 表示"不采信"（太短），调用方据此回退到默认退避；
// 过长则夹到 retryAfterMax，避免一个畸形值把凭据长期冻住。
func clampRetryAfter(hint time.Duration) time.Duration {
	if hint < retryAfterMin {
		return 0
	}
	if hint > retryAfterMax {
		return retryAfterMax
	}
	return hint
}

// isEmptyCompletion 判断"HTTP 200 但正文为空"的可降级失败。
//
// 判据（仅针对 OpenAI 兼容的成功响应）：
//   - choices 字段缺失（embedding / Anthropic 等其他形态）→ 不算空（避免误判其他协议）；
//   - choices 为空数组 → 算空；
//   - 首个 choice 的 message.content 为空且没有 tool_calls → 算空。
//
// 为什么这算"失败"：推理模型在 token 预算被吃满时会返回 200 且 content 为空，
// 这是上游侧的一次"无效生成"，换一个渠道往往能成功，值得渠道级重试。
//
// 注意：只对非流式响应调用（流式正文是 SSE 分片，无法在不破坏逐字输出的前提下判定空内容）。
func isEmptyCompletion(resp *http.Response) bool {
	if resp == nil || resp.Body == nil {
		return false
	}
	peek, err := peekBody(resp, keyLevelPeekBytes)
	if err != nil || len(peek) == 0 {
		return false
	}

	var envelope struct {
		Choices *[]struct {
			Message *struct {
				Content   string          `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(peek, &envelope); err != nil {
		// 解析不了（例如被 4KiB 截断的大响应）：按"非空"处理，宁可不重试也不误重试。
		return false
	}
	if envelope.Choices == nil {
		return false
	}
	choices := *envelope.Choices
	if len(choices) == 0 {
		return true
	}
	first := choices[0]
	if first.Message == nil {
		return false
	}
	if strings.TrimSpace(first.Message.Content) != "" {
		return false
	}
	return !hasToolCalls(first.Message.ToolCalls)
}

// hasToolCalls 判断 tool_calls 字段是否真的有内容。
//
// 空值形态有三种：字段缺失（nil）、JSON null、空数组 []——都算"没有工具调用"。
func hasToolCalls(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}
