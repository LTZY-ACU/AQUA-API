// 本文件实现「按分组维度」的 RPM（每分钟请求数）限流中间件。
//
// 意图（Why）：
//
//	额度墙管的是【总量】，挡不住【速率】——一个失控令牌在几分钟内高频调用，
//	足以在结算前吃掉整月的毛利，而"累计额度"此时还没到顶。RPM 上限补的正是
//	这一维度：让"某档套餐每分钟最多打多少"成为可配置、可强制的承诺。
//
//	限流维度取【分组】而非令牌/IP：分组代表套餐档位（免费档 / 标准档 / 代理档），
//	RPM 是套餐的一部分；按分组计数使"同档令牌共享一份速率预算"，
//	管理员调档一次即对该档全部令牌生效，与倍率、门槛的归属维度一致。
//
//	计数为【进程内固定 1 分钟窗口】（map + mutex + 惰性清理）：
//	   - 不引入 Redis/外部依赖，改动半径最小，重启即清零；
//	   - 代价：多实例部署时各实例各算一份，实际全局速率约为"上限 × 实例数"。
//	     对"套餐档位"这种粗粒度承诺可以接受；日后确需跨实例共享时，
//	     只需把 allow/limitFor 的底层存储换掉，中间件与路由无需改动。
//
// 分流（Flow）：
//
//	/v1 与 /v1beta 转发路由链 → GroupRPMLimiter.Middleware()
//	  ├─ resolveGroup   从 reqctx 取当前令牌分组（TokenAuth 写入），空则回退默认分组
//	  ├─ limitFor       查分组 rpm_limit（带 30s 进程内缓存）；0/读库失败 → 放行
//	  └─ allow          固定 1 分钟窗口计数；超限 → 429（code quota.group_rpm_exceeded）
//
// 顺序与"零成本"：
//
//	rpm_limit=0（默认，也是全部历史分组）时，取到 0 即直接 c.Next()，
//	既不查库也不入计数表——对现有部署的请求路径几乎零开销。
//
// 扩展（Extend）：
//
//	需要秒级限流 / 按令牌覆盖 / 跨实例共享时，另建限流器或替换本文件底层存储，
//	不要改动 TokenAuth（分组来源约定保持不变）。
package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/reqctx"
)

const (
	// rpmWindow 是限流窗口长度：固定 1 分钟（即 RPM 的分子）。
	rpmWindow = time.Minute

	// rpmLimitCacheTTL 是"分组 → rpm_limit"的进程内缓存存活时间。
	//
	// 为什么要缓存：转发链路是 /v1 的热路径，若每个请求都查一次分组表，
	// 会把"分组配置几乎不变"的事实白白变成持续的数据库读压力。
	// 30 秒与计费价格缓存同量级：管理员改完限流最多半分钟生效，感知足够即时。
	rpmLimitCacheTTL = 30 * time.Second

	// rpmCleanupInterval 是惰性清理的间隔，避免 map 随分组/窗口数无界增长。
	rpmCleanupInterval = 10 * time.Minute

	// rpmMaxGroups 是计数表与限流缓存的条目上限。
	//
	// 键是分组名，正常部署只有个位数；设上限只为防御"分组名被异常写坏"这类意外，
	// 达到上限时放弃写入（宁可少限流，也不让内存无界增长）。
	rpmMaxGroups = 10000
)

// GroupRPMLimitSource 提供「按分组名查分组」的最小能力（由 model.ModelGroupRepository 实现）。
//
// 在消费方定义接口而非直接依赖仓储：便于单元测试注入假实现，
// 也让本包只依赖"能拿到分组"这一件事。
type GroupRPMLimitSource interface {
	GetByName(ctx context.Context, name string) (*model.ModelGroup, error)
}

// rpmWindowState 是某分组在当前 1 分钟窗口内的计数。
type rpmWindowState struct {
	windowStart time.Time
	count       int64
}

// rpmLimitCacheEntry 是"分组 → rpm_limit"的缓存项。
type rpmLimitCacheEntry struct {
	limit    int
	cachedAt time.Time
}

// GroupRPMLimiter 是按分组做固定 1 分钟窗口计数的限流器，并发安全。
type GroupRPMLimiter struct {
	source       GroupRPMLimitSource
	defaultGroup string
	window       time.Duration
	limitTTL     time.Duration

	// now 便于测试注入固定时钟（生产使用 time.Now）。
	now func() time.Time

	mu          sync.Mutex
	windows     map[string]*rpmWindowState
	limitCache  map[string]rpmLimitCacheEntry
	lastCleanup time.Time
}

// NewGroupRPMLimiter 创建分组 RPM 限流器。
//
// defaultGroup 为"请求未指定分组时"使用的分组（应与计费默认分组一致）；
// 为空时回退 model.DefaultGroupName。source 为 nil 时中间件恒放行。
func NewGroupRPMLimiter(source GroupRPMLimitSource, defaultGroup string) *GroupRPMLimiter {
	if strings.TrimSpace(defaultGroup) == "" {
		defaultGroup = model.DefaultGroupName
	}
	return &GroupRPMLimiter{
		source:       source,
		defaultGroup: defaultGroup,
		window:       rpmWindow,
		limitTTL:     rpmLimitCacheTTL,
		now:          time.Now,
		windows:      make(map[string]*rpmWindowState),
		limitCache:   make(map[string]rpmLimitCacheEntry),
	}
}

// clock 返回当前时间（测试可注入固定时钟）。
func (l *GroupRPMLimiter) clock() time.Time {
	if l != nil && l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Middleware 返回挂到转发路由链上的 gin 中间件。
//
// 必须在 TokenAuth 之后使用：分组来自 TokenAuth 写入 request context 的值。
func (l *GroupRPMLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 未配置分组仓储（或未启用本能力）：完全放行，行为与引入前一致。
		if l == nil || l.source == nil {
			c.Next()
			return
		}

		group := l.resolveGroup(c)
		limit := l.limitFor(c.Request.Context(), group)
		if limit <= 0 {
			// 0 = 不限：直接放行（零成本，不入计数表）。
			c.Next()
			return
		}

		if !l.allow(group, limit) {
			abortWithError(c, http.StatusTooManyRequests,
				fmt.Sprintf("分组 %s 每分钟最多 %d 次请求，请稍后重试", group, limit),
				oai.TypeRateLimit, "quota.group_rpm_exceeded")
			return
		}
		c.Next()
	}
}

// resolveGroup 取本次请求所属分组。
//
// 来源优先取 reqctx 中的令牌分组（由 TokenAuth 写入）；为空表示令牌未指定分组，
// 回退到默认分组——与转发层、计费层"空分组 = 默认分组"的约定保持一致，
// 避免同一个请求"按 A 组选渠道、按 B 组限流"。
func (l *GroupRPMLimiter) resolveGroup(c *gin.Context) string {
	group := strings.TrimSpace(reqctx.Group(c.Request.Context()))
	if group != "" {
		return group
	}
	if l.defaultGroup != "" {
		return l.defaultGroup
	}
	return model.DefaultGroupName
}

// limitFor 返回分组的 RPM 上限（0 = 不限）。
//
// 读库失败时返回 0（fail-open）并留下告警：限流是保护性能力，
// 不应因一次分组表读故障就把该档全部调用打成 429——那属于"防护比风险更凶"。
func (l *GroupRPMLimiter) limitFor(ctx context.Context, group string) int {
	now := l.clock()

	l.mu.Lock()
	if entry, ok := l.limitCache[group]; ok && now.Sub(entry.cachedAt) < l.limitTTL {
		l.mu.Unlock()
		return entry.limit
	}
	l.mu.Unlock()

	limit := 0
	if l.source != nil {
		got, err := l.source.GetByName(ctx, group)
		switch {
		case err == nil && got != nil:
			if got.RpmLimit > 0 {
				limit = got.RpmLimit
			}
		case errors.Is(err, model.ErrModelGroupNotFound):
			// 分组不存在（历史数据里的分组名未登记）：按不限处理（与计费侧一致）
		default:
			slog.Warn("读取分组 RPM 上限失败，本次按不限流处理", "error", err, "group", group)
		}
	}

	l.mu.Lock()
	if len(l.limitCache) < rpmMaxGroups {
		l.limitCache[group] = rpmLimitCacheEntry{limit: limit, cachedAt: now}
	}
	l.mu.Unlock()
	return limit
}

// allow 在固定 1 分钟窗口内为分组计数；返回 false 表示本窗口内已超限。
func (l *GroupRPMLimiter) allow(group string, limit int) bool {
	if l == nil || limit <= 0 || group == "" {
		return true
	}
	now := l.clock()
	windowStart := now.Truncate(l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanupLocked(now, windowStart)

	state := l.windows[group]
	if state == nil || !state.windowStart.Equal(windowStart) {
		// 新窗口（或首次出现该分组）：重新开窗计数。
		if state == nil && len(l.windows) >= rpmMaxGroups {
			// 条目已满（异常情况）：放弃计数而非无限增长，等价于"该请求放行"。
			return true
		}
		state = &rpmWindowState{windowStart: windowStart}
		l.windows[group] = state
	}

	if state.count >= int64(limit) {
		return false
	}
	state.count++
	return true
}

// cleanupLocked 在已持锁前提下惰性清理过期窗口与陈旧上限缓存。
//
// 清理频率由 rpmCleanupInterval 控制（与登录限流器同一思路）：
// 多数请求只做一次时间比较，不产生遍历开销。
func (l *GroupRPMLimiter) cleanupLocked(now, currentWindow time.Time) {
	if now.Sub(l.lastCleanup) < rpmCleanupInterval {
		return
	}
	for name, state := range l.windows {
		if state.windowStart.Before(currentWindow) {
			delete(l.windows, name)
		}
	}
	staleAfter := l.limitTTL * 10
	for name, entry := range l.limitCache {
		if now.Sub(entry.cachedAt) > staleAfter {
			delete(l.limitCache, name)
		}
	}
	l.lastCleanup = now
}
