// 本文件实现前端静态资源的托管与 SPA 路由回退。
//
// 意图（Why）：
//
//	本项目把前端构建产物嵌入二进制并由 Go 直接提供，好处是部署只有一个文件、
//	不存在"静态资源与后端版本不匹配"的问题。
//	前端是 Next.js 静态导出（output:export + trailingSlash）产物，其形态与旧 Vite SPA 不同：
//	  1) 每个页面是真实 HTML 文件（如 console/tokens/index.html），不是"全靠一个 index.html 回退"；
//	  2) 静态资源前缀是 /_next/*（JS/CSS/字体），而非旧版的 /assets/*；
//	  3) 客户端路由导航会请求 RSC 加载态文件（如 __next.__PAGE__.txt）。
//	因此伺服逻辑必须支持：先按路径找真实文件 → 找不到再做 SPA 回退到 index.html。
//
// 流转（Flow）：
//
//	GET /_next/* 或 /assets/* → 直接返回嵌入文件（带一年长缓存，文件名带内容哈希）
//	GET /console/tokens       → 尝试真实文件 console/tokens/index.html → 命中即返回
//	GET /unknown-path         → SPA 回退：返回 index.html（Next 客户端接管路由）
//	GET /api/*、/v1/*         → 不回退，仍返回 404/405（避免接口 404 变成 HTML）
//
// 扩展（Extend）：
//
//	新增静态资源前缀时：在 staticPrefixes 中加入该前缀（享用长缓存），
//	其余路径一律走"真实文件 → SPA 回退"两级查找。
package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"gitee.com/xiaosu4610/aqua-api/internal/model"
	"gitee.com/xiaosu4610/aqua-api/internal/oai"
)

// 静态资源缓存时长。
//
// 为什么可以设一年：前端产物文件名带内容哈希（如 index-BSZ0bE39.js），
// 内容变化必然导致文件名变化，因此可以放心地让浏览器长期缓存。
// index.html 本身不缓存（见下），保证发版后用户能立即拿到新的资源引用。
const assetCacheControl = "public, max-age=31536000, immutable"

// distRoot 是嵌入文件系统内前端产物的根目录名。
const distRoot = "web/dist"

// indexFileName 是 SPA 回退入口文件名。
const indexFileName = "index.html"

// staticPrefixes 是需要长期缓存的静态资源前缀（产物文件名带哈希）。
var staticPrefixes = []string{"/_next/", "/assets/"}

// registerStaticRoutes 注册前端静态资源与 SPA 回退。
//
// 参数 fsys 为整个嵌入文件系统（通常是根包的 WebDist）；
// 若为 nil 或其中不含前端产物，则只在访问页面时给出明确提示，不影响接口可用。
func (s *Server) registerStaticRoutes(fsys fs.FS) {
	dist, err := fs.Sub(fsys, distRoot)
	if err != nil {
		// 嵌入路径异常属于构建配置问题，记录后不注册静态路由：
		// 接口仍可用，便于在浏览器之外排查。
		slog.Error("嵌入前端产物不可用，静态路由未注册（接口不受影响）",
			"error", err, "root", distRoot)
		return
	}

	fileServer := http.FileServer(http.FS(dist))

	// 静态资源（/_next/*、/assets/*）：交给标准库文件服务器，并补上长缓存头
	for _, prefix := range staticPrefixes {
		route := prefix
		s.engine.GET(route+"*filepath", func(c *gin.Context) {
			c.Header("Cache-Control", assetCacheControl)
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}
	s.engine.GET("/favicon.ico", gin.WrapH(fileServer))

	// SPA 回退：先按路径找真实文件（Next 多页产物），找不到再回退 index.html
	s.engine.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// 接口路径不做回退：否则前端请求打错地址时会收到 HTML，
		// 报错信息会变成"Unexpected token < in JSON"这类令人困惑的解析错误。
		if isAPIPath(path) {
			if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodPost {
				oai.WriteError(c.Writer, http.StatusNotFound,
					"接口不存在", oai.TypeInvalidRequest, "endpoint_not_found")
				return
			}
			c.Status(http.StatusMethodNotAllowed)
			return
		}

		// 非 GET 的页面请求无意义（前端页面只通过 GET 访问）
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusMethodNotAllowed)
			return
		}

		// 第一级：真实文件（Next 每个路由一个目录，如 console/tokens/index.html）
		if served := serveRealFile(s, c, dist, path); served {
			return
		}

		// 第二级：SPA 回退 —— 返回 index.html，由 Next 客户端接管路由
		index, err := fs.ReadFile(dist, indexFileName)
		if err != nil {
			// 前端未构建：给出可操作的提示，而不是空白页或 500
			c.String(http.StatusServiceUnavailable,
				"前端尚未构建。请先执行：cd web && npm install && npm run build，然后重新编译后端。")
			return
		}

		// 注入 SEO 元信息（关键词、站长验证码、canonical 等）。
		// 读设置失败时跳过注入、照常返回页面：SEO 是增强项，不应让页面打不开。
		if settings, loadErr := model.LoadSiteSettings(c.Request.Context(), s.deps.Settings); loadErr == nil {
			base := resolveBaseURL(settings, c)
			index = injectSEOMeta(index, settings, base, path)
		}

		// index.html 自身不做缓存：它引用的是带哈希的资源文件名，
		// 若缓存了旧版 index.html，发版后用户会继续请求已不存在的旧资源。
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

// serveRealFile 尝试按请求路径伺服嵌入文件系统中的真实文件。
//
// Next 多页产物的路径规则：console/tokens → 目录 console/tokens/ 下的 index.html。
// 同时兼容「无尾斜杠访问目录」「直接访问某文件」两种形态。
// 返回 true 表示已由本函数写出响应（含 404 后的回退决定由调用方继续）。
func serveRealFile(s *Server, c *gin.Context, dist fs.FS, path string) bool {
	rel := strings.TrimPrefix(path, "/")
	if rel == "" {
		return false // 首页交给 SPA 回退统一处理（含 SEO 注入）
	}

	candidates := []string{rel}
	// Next 目录形态：console/tokens/ 需要通过 index.html 访问
	dirCandidate := strings.TrimSuffix(rel, "/") + "/" + indexFileName
	if dirCandidate != rel {
		candidates = append(candidates, dirCandidate)
	}
	// 旧 Vite SPA 单文件形态：路径即为文件名（如 /about 对 about.html）
	if !strings.HasSuffix(rel, "/") {
		candidates = append(candidates, rel+".html")
	}

	for _, candidate := range candidates {
		if _, err := fs.Stat(dist, candidate); err != nil {
			continue
		}
		// 命中真实文件：按文件类型返回，HTML 额外注入 SEO
		data, err := fs.ReadFile(dist, candidate)
		if err != nil {
			continue
		}
		contentType := "text/html; charset=utf-8"
		if strings.HasSuffix(candidate, ".txt") {
			contentType = "text/plain; charset=utf-8"
		} else if strings.HasSuffix(candidate, ".js") {
			contentType = "text/javascript; charset=utf-8"
		} else if strings.HasSuffix(candidate, ".css") {
			contentType = "text/css; charset=utf-8"
		}

		if strings.Contains(contentType, "text/html") {
			if settings, loadErr := model.LoadSiteSettings(c.Request.Context(), s.deps.Settings); loadErr == nil {
				base := resolveBaseURL(settings, c)
				data = injectSEOMeta(data, settings, base, "/"+strings.TrimSuffix(rel, indexFileName))
			}
			c.Header("Cache-Control", "no-cache")
		} else {
			c.Header("Cache-Control", assetCacheControl)
		}
		c.Data(http.StatusOK, contentType, data)
		return true
	}
	return false
}

// isAPIPath 判断路径是否属于后端接口（这些路径不应回退到前端页面）。
func isAPIPath(path string) bool {
	prefixes := []string{"/api/", "/v1/", "/v1beta/", "/healthz", "/readyz"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// 精确匹配接口根路径（如 /api 本身）
	return path == "/api" || path == "/v1"
}
