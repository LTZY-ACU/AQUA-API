// upstream_error_test.go 校验"上游错误 → 本站定制错误"的脱敏映射。
//
// 意图（Why）：
//
//	站长要求"绝对不暴露上游的报错码"。脱敏规则是安全边界，一旦被改错就会把上游
//	厂商名 / 账号标识 / 原始错误码泄露给下游，因此必须有断言锁定映射表。
//
// 流转（Flow）：
//
//	sanitizeUpstreamError（纯函数，本文件直接测）→ writeSanitizedUpstreamError
//	→ writeAdaptedError → 下游协议编码。
//
// 扩展（Extend）：
//
//	新增上游状态码归类时，只需在 sanitizeUpstreamError 的 switch 中补一个 case，
//	并在此表的用例中同步一行期望。
package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/oai"
)

// TestSanitizeUpstreamError_映射表 锁定三类上游失败的语义归类。
func TestSanitizeUpstreamError_映射表(t *testing.T) {
	cases := []struct {
		name           string
		upstreamStatus int
		wantStatus     int
		wantType       string
		wantCode       string
	}{
		{"上游限流", http.StatusTooManyRequests, http.StatusTooManyRequests, oai.TypeRateLimit, oai.CodeUpstreamRateLimited},
		{"上游 500", http.StatusInternalServerError, http.StatusServiceUnavailable, oai.TypeServer, oai.CodeUpstreamUnavailable},
		{"上游 502", http.StatusBadGateway, http.StatusServiceUnavailable, oai.TypeServer, oai.CodeUpstreamUnavailable},
		{"上游 503", http.StatusServiceUnavailable, http.StatusServiceUnavailable, oai.TypeServer, oai.CodeUpstreamUnavailable},
		{"上游过载 529", 529, http.StatusServiceUnavailable, oai.TypeServer, oai.CodeUpstreamUnavailable},
		{"上游 400", http.StatusBadRequest, http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed},
		{"上游 401", http.StatusUnauthorized, http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed},
		{"上游 403", http.StatusForbidden, http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed},
		{"上游 404", http.StatusNotFound, http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed},
		{"上游 422", http.StatusUnprocessableEntity, http.StatusBadGateway, oai.TypeServer, oai.CodeUpstreamRequestFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, errType, code, message := sanitizeUpstreamError(tc.upstreamStatus)
			if status != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d", status, tc.wantStatus)
			}
			if errType != tc.wantType {
				t.Fatalf("错误类型 = %q，期望 %q", errType, tc.wantType)
			}
			if code != tc.wantCode {
				t.Fatalf("错误码 = %q，期望 %q", code, tc.wantCode)
			}
			if strings.TrimSpace(message) == "" {
				t.Fatal("错误文案不能为空：下游需要一句可读的说明")
			}
			// 文案必须出自本站：出现上游标记即视为泄露。
			if strings.Contains(code, "nvidia") || strings.Contains(message, "nvidia") {
				t.Fatalf("错误码或文案含上游标识：%q / %q", code, message)
			}
		})
	}
}

// TestWriteSanitizedUpstreamError_响应体不含上游痕迹 端到端校验写出的字节。
func TestWriteSanitizedUpstreamError_响应体不含上游痕迹(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSanitizedUpstreamError(rec, nil, http.StatusUnauthorized)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", rec.Code)
	}

	raw := rec.Body.Bytes()
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v（原始：%s）", err, raw)
	}
	if payload.Error.Code != oai.CodeUpstreamRequestFailed {
		t.Fatalf("错误码 = %q，期望 %q", payload.Error.Code, oai.CodeUpstreamRequestFailed)
	}
	if payload.Error.Type != oai.TypeServer {
		t.Fatalf("错误类型 = %q，期望 %q", payload.Error.Type, oai.TypeServer)
	}
	// 响应体只应是本站文案与本站错误码，不含上游状态码数字。
	if strings.Contains(string(raw), "401") {
		t.Fatalf("响应体不应出现上游状态码，实际：%s", raw)
	}
}

// TestUpstreamErrorLogText_保留上游真实原因 确保排障证据没被一起脱敏掉。
func TestUpstreamErrorLogText_保留上游真实原因(t *testing.T) {
	got := upstreamErrorLogText(http.StatusUnauthorized, "invalid api key")
	if !strings.Contains(got, "401") || !strings.Contains(got, "invalid api key") {
		t.Fatalf("日志应保留上游真实状态码与原因，实际 %q", got)
	}

	empty := upstreamErrorLogText(http.StatusBadGateway, "   ")
	if !strings.Contains(empty, "502") || !strings.Contains(empty, "未返回") {
		t.Fatalf("上游未给出原因时应明确标注，实际 %q", empty)
	}
}
