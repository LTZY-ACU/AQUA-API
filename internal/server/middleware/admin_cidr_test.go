// 本文件验证「管理面来源 IP 白名单」中间件的行为。
//
// 意图（Why）：
//
//	白名单是后台的公网边界：一旦语义写错（例如"配置为空时误拦"或"不命中却放行"），
//	后果要么是"所有人都进不来后台"，要么是"白名单形同虚设、后台裸奔"。
//	因此把四条关键语义钉死：空配置放行一切、命中放行、不命中 403、来源 IP 不可解析时拒绝。
//
// 流转（Flow）：
//
//	构造带指定 RemoteAddr / X-Real-IP 的请求 → RequireAdminCIDR → 断言状态码
package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/gin-gonic/gin"
)

// serveWithAdminCIDR 用给定白名单构造引擎并请求 /api/admin/ping，返回状态码。
func serveWithAdminCIDR(t *testing.T, prefixes []netip.Prefix, remoteAddr string, headers map[string]string) int {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequireAdminCIDR(prefixes))
	engine.GET("/api/admin/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ping", nil)
	req.RemoteAddr = remoteAddr
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Code
}

// TestRequireAdminCIDR_Semantics 表驱动覆盖白名单的核心语义。
func TestRequireAdminCIDR_Semantics(t *testing.T) {
	whitelist := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("1.2.3.4/32"),
	}

	cases := []struct {
		name       string
		prefixes   []netip.Prefix
		remoteAddr string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "空配置_放行一切",
			prefixes:   nil,
			remoteAddr: "203.0.113.9:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "命中_10网段_放行",
			prefixes:   whitelist,
			remoteAddr: "10.1.2.3:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "命中_单IP_放行",
			prefixes:   whitelist,
			remoteAddr: "1.2.3.4:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "不命中_403",
			prefixes:   whitelist,
			remoteAddr: "203.0.113.9:1234",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "可信代理_取XRealIP命中_放行",
			prefixes:   whitelist,
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Real-IP": "10.9.8.7"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "可信代理_取XRealIP不命中_403",
			prefixes:   whitelist,
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Real-IP": "203.0.113.9"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "公网直连_伪造XRealIP不被采信_403",
			prefixes:   whitelist,
			remoteAddr: "203.0.113.9:1234",
			headers:    map[string]string{"X-Real-IP": "10.1.2.3"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serveWithAdminCIDR(t, tc.prefixes, tc.remoteAddr, tc.headers)
			if got != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d", got, tc.wantStatus)
			}
		})
	}
}

// TestRequireAdminCIDR_DeniesOnUnparseableIP 验证来源 IP 不可解析时一律拒绝（fail-closed）。
func TestRequireAdminCIDR_DeniesOnUnparseableIP(t *testing.T) {
	whitelist := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

	// RemoteAddr 既不是合法 host:port 也不是合法 IP，ClientIP 会原样返回，
	// 此时必须拒绝而不是放行。
	got := serveWithAdminCIDR(t, whitelist, "not-an-ip", nil)
	if got != http.StatusForbidden {
		t.Fatalf("不可解析来源 IP 时状态码 = %d，期望 403（fail-closed）", got)
	}
}
