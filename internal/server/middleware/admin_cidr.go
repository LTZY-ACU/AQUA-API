// 本文件实现「管理面来源 IP 白名单」中间件。
//
// 意图（Why）：
//
//	管理后台是公网可达的高价值入口。仅靠口令/会话鉴权时，一旦凭据泄露或出现
//	鉴权绕过，攻击者即可直达后台。再叠一层网络边界（只允许已知网段访问
//	/api/admin/**），可把攻击面从"任何地方"收敛到"白名单网段"，显著降低
//	口令爆破与未知漏洞的影响面。
//
// 流转（Flow）：
//
//	config.Load → AdminAllowCIDRs([]string) + Validate（非法 CIDR 直接启动失败）
//	  → server.registerRoutes → config.ParseAdminAllowCIDRs → []netip.Prefix
//	    → middleware.RequireAdminCIDR(prefixes) 挂在 /admin 分组【最前】
//	      → ClientIP(c) 取可信来源 IP → netip.Prefix.Contains 判定 → 放行 / 403
//
// 扩展（Extend）：
//
//	需要按路径细分（如只保护写操作）时：把本中间件挂到更细的分组上，
//	而不是在处理器里写判断。需要调整"可信代理"的判定时，只改 ClientIP
//	（它也是限流与审计的唯一来源），避免出现两套互相矛盾的来源 IP 口径。
package middleware

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/oai"
)

// adminAllowCIDREnv 是白名单环境变量名，仅用于 403 文案提示，便于管理员自助排查。
const adminAllowCIDREnv = "AQUA_ADMIN_ALLOW_CIDRS"

// RequireAdminCIDR 返回校验来源 IP 是否落在白名单内的中间件。
//
// 参数 prefixes 为空（nil 或长度 0）时【不做任何限制】——这是刻意的语义：
// 让"未配置白名单"的存量部署升级后行为逐字不变。
//
// IP 取值一律走 ClientIP(c)，绝不自己解析 X-Forwarded-For：
// 那些头可由客户端伪造，直接采信等于把白名单变成摆设（详见 ratelimit.go 的说明）。
//
// fail-closed：来源 IP 无法解析或不在任何前缀内，一律 403，
// 绝不在"判定不了"时放行。
func RequireAdminCIDR(prefixes []netip.Prefix) gin.HandlerFunc {
	if len(prefixes) == 0 {
		// 未配置：直接放行，保持既有行为不变
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if !ipWithinPrefixes(prefixes, ClientIP(c)) {
			// 文案明确告知"这是网络边界拦截"，避免管理员误以为账号或权限出了问题。
			// 不回显白名单内容，避免向未授权来源泄露内网网段划分。
			abortWithError(c, http.StatusForbidden,
				"来源 IP 不在管理后台访问白名单内（如需放行，请将该 IP/CIDR 加入环境变量 "+adminAllowCIDREnv+" 后重启）",
				oai.TypePermission, "ip_not_allowed")
			return
		}
		c.Next()
	}
}

// ipWithinPrefixes 判断给定 IP 文本是否落在任一 CIDR 前缀内。
//
// 取不到合法 IP 时返回 false（fail-closed）。IPv4-mapped IPv6 先 Unmap 归一，
// 避免 ::ffff:1.2.3.4 因表示形式不同而漏判。
func ipWithinPrefixes(prefixes []netip.Prefix, raw string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
