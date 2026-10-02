// 安全响应头中间件与静态资源正文类型的测试。
//
// 锁住三件事：
//  1. 全局安全头（nosniff 等）在任意路由与 404 上都存在；
//  2. 静态资源按扩展名定类型，认不出的一律 octet-stream ——
//     原实现"不认识就当 HTML"，会让 .svg/未知文件被当页面渲染并执行脚本；
//  3. HTML 页面仍然拿到 text/html（SEO 注入与缓存策略以它为判据）。
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func newSecurityHeadersEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(securityHeaders())
	engine.GET("/api/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return engine
}

// TestSecurityHeaders_任意响应都带安全头 覆盖正常处理与 404 两条路径：
// 中间件若装晚了，404 与静态页就会漏掉这些头。
func TestSecurityHeaders_任意响应都带安全头(t *testing.T) {
	engine := newSecurityHeadersEngine(t)

	for _, path := range []string{"/api/ping", "/no-such-path"} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s 缺少 nosniff，实际 %q", path, got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("%s 缺少 X-Frame-Options，实际 %q", path, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
			t.Errorf("%s 缺少 Referrer-Policy，实际 %q", path, got)
		}
	}
}

// TestStaticContentType_按扩展名分类 钉死"认不出的类型不交给浏览器渲染"。
func TestStaticContentType_按扩展名分类(t *testing.T) {
	cases := map[string]string{
		"index.html":                htmlContentType,
		"console/tokens/index.html": htmlContentType,
		"main.js":                   "text/javascript; charset=utf-8",
		"style.css":                 "text/css; charset=utf-8",
		"page.txt":                  "text/plain; charset=utf-8",
		"logo.svg":                  "image/svg+xml",
		"favicon.ico":               "image/x-icon",
		"font.woff2":                "font/woff2",
		"unknown.xyz":               "application/octet-stream",
		"noextension":               "application/octet-stream",
		"MANIFEST":                  "application/octet-stream",
	}
	for path, want := range cases {
		if got := staticContentType(path); got != want {
			t.Errorf("staticContentType(%q) = %q，期望 %q", path, got, want)
		}
	}
}

// TestServeRealFile_SVG不被当HTML渲染 是上一条规则的端到端验证：
// SVG 内含 <script>，按 text/html 返回会在本站源执行脚本。
// （HTML 路径只断言类型常量，见上一用例：它的分支要读站点设置，
// 这里刻意不挂 deps，避免测试依赖仓储实现。）
func TestServeRealFile_SVG不被当HTML渲染(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dist := fstest.MapFS{
		"logo.svg": &fstest.MapFile{Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
	}

	engine := gin.New()
	engine.GET("/*path", func(c *gin.Context) {
		// SVG 分支不读站点设置，因此 &Server{} 足够
		if serveRealFile(&Server{}, c, dist, c.Request.URL.Path) {
			return
		}
		c.Status(http.StatusNotFound)
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logo.svg", nil))
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("SVG 应按 image/svg+xml 返回，实际 %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("SVG 应正常返回 200，实际 %d", rec.Code)
	}
}
