// 本文件集中实现「上游错误 → 本站定制错误」的脱敏映射。
//
// 意图（Why）：
//
//	站点绝不能让下游看到上游的错误码与错误原文。上游错误体里常带有上游厂商名、
//	账号标识、账号额度状态等信息，透传出去等于把"我们用了哪家上游、渠道怎么组织"
//	暴露给调用方，既不符合产品定位，也可能被用来探测渠道结构。
//	因此：上游的真实原因【只】读进本站调用日志（供站长排障），
//	给下游的一律是本站语义化错误码与本站文案，上游信息不出网关。
//
// 流转（Flow）：
//
//	上游状态码 ──sanitizeUpstreamError──▶ (本站状态码, 错误类型, 本站错误码, 本站文案)
//	                                            └─ writeSanitizedUpstreamError ─▶ 下游
//	上游响应体 ──extractUpstreamErrorMessage──▶ upstreamErrorLogText ─▶ 本站调用日志
//
// 扩展（Extend）：
//
//	新增映射档位：在 sanitizeUpstreamError 的 switch 里加分支，并到 oai 包补对应
//	  错误码常量；调整文案：只改本文件，勿在调用处内联字符串
//	  （内联会让同一种错误在不同路径出现多套说辞，用户以为遇到了不同问题）。
package relay

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// sanitizeUpstreamError 把上游失败映射为本站定制错误（状态码 / 类型 / 错误码 / 文案）。
//
// 映射策略（语义化，站长已确认）：
//   - 429        → 429 限流：下游可据此退避重试（与 OpenAI 约定一致，保持兼容）；
//   - 5xx / 529  → 503 上游不可用：下游稍后重试可能成功；
//   - 其余（4xx 等）→ 502 上游请求失败：统一归类，不再细分上游语义，避免泄露。
//
// 关键约束：入参 upstreamStatus 仅用于本站内部判定；返回值里【不含】任何上游标识，
// 消息文本也全部由本站撰写。
func sanitizeUpstreamError(upstreamStatus int) (status int, errType, code, message string) {
	switch {
	case upstreamStatus == http.StatusTooManyRequests:
		return http.StatusTooManyRequests, oai.TypeRateLimit, oai.CodeUpstreamRateLimited,
			"上游服务当前限流，请稍后重试"
	case upstreamStatus >= http.StatusInternalServerError:
		// 529（上游过载，厂商约定）也落在这里：>= 500。
		return http.StatusServiceUnavailable, oai.TypeServer, oai.CodeUpstreamUnavailable,
			"上游服务暂时不可用，请稍后重试"
	default:
		// 含 400/401/403/404/408/422 等一切其余上游失败。
		return http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed,
			"上游服务未能处理本次请求，请稍后重试"
	}
}

// writeSanitizedUpstreamError 按下游协议写出脱敏后的上游错误。
//
// adapter 为 nil 时走 OpenAI 直通格式（oai.WriteError），非 nil 时由适配器改写为
// Anthropic / Gemini 等格式；两条路径写出的都是本站错误码与本站文案，不含上游信息。
//
// 复用 writeAdaptedError 而不是自己分叉：它已统一处理"OpenAI 错误体 → 下游协议"的
// 结构映射，在这里再实现一遍只会产生两套不一致的错误语义。
func writeSanitizedUpstreamError(w http.ResponseWriter, adapter Adapter, upstreamStatus int) {
	status, errType, code, message := sanitizeUpstreamError(upstreamStatus)
	writeAdaptedError(w, adapter, status, message, errType, code)
}

// upstreamErrorLogText 生成写进本站调用日志的上游错误摘要。
//
// 为什么日志保留上游原文而下游看不到：日志只有站长能看，保留上游真实原因
// 是排障的关键（否则"到底是模型没权限、账号没额度，还是上游挂了"永远说不清）；
// 下游响应则必须脱敏（见 sanitizeUpstreamError）。摘要里标注"已脱敏"，
// 避免日后有人误以为下游也看到了这段原文。
func upstreamErrorLogText(upstreamStatus int, upstreamMessage string) string {
	if strings.TrimSpace(upstreamMessage) == "" {
		return fmt.Sprintf("上游返回 HTTP %d（原因未返回，已脱敏回下游）", upstreamStatus)
	}
	return fmt.Sprintf("上游返回 HTTP %d：%s（已脱敏回下游）", upstreamStatus, upstreamMessage)
}
