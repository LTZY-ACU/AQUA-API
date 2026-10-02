// 本文件实现「安全响应头」中间件。
//
// 意图（Why）：
//
//	站点同时提供浏览器页面（Next 产物 + 管理后台）与 API 响应，
//	而 API 里还有大量来自上游、本进程无从担保的正文（见 relay.copyResponseHeaders）。
//	把最基础的几道浏览器侧防线固定成全局中间件，比在每个处理器里各写一遍可靠：
//	新增路由只要走 Gin 引擎就自动带上，不可能漏。
//
// 流转（Flow）：
//
//	请求 → bodyLimit（限读取）→ securityHeaders（写响应头）→ 各处理器/文件伺服
//	响应头在 body 之前就写好，处理器与静态伺服都无需知道它们的存在。
//
// 扩展（Extend）：
//
//	新增安全头一律加在这里的 securityHeaders 里，不要在业务代码中散落设置；
//	若将来引入 CSP，注意 Next.js 产物带内联脚本，需同时配置 nonce/hash，
//	不能简单地写一条 style/script-src 直接上线（会把整站页面打白）。
package server

import (
	"github.com/gin-gonic/gin"
)

// securityHeaders 给所有响应补上浏览器侧安全头。
//
// 这里刻意只放"加了也不会改变任何业务行为"的头：
//   - nosniff：禁止浏览器对正文类型做嗅探。本站既有 JSON 也有 HTML，
//     上游透传的正文更不可信——嗅探成 text/html 就可能在本站源执行脚本；
//   - X-Frame-Options：SAMEORIGIN 允许自家页面互相嵌（登录跳转等），
//     同时挡住外部站点把管理后台套进 iframe 做点击劫持；
//   - Referrer-Policy：跨站跳转只送 origin，避免带完整路径的链接泄露后台地址结构。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.Writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "SAMEORIGIN")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	}
}
