// 本文件实现进程内 TTL 缓存，用于降低高频只读接口的数据库压力。
//
// 意图（Why）：
//
//	日志列表每次请求都要把「用户 ID → 用户名」「渠道 ID → 渠道名」全表加载
//	（nameLookupLimit=1000），dashboard 的统计聚合每次实时计算。
//	这两类数据变化频率远低于查询频率，加一层短 TTL 缓存能显著减少
//	数据库往返，且不需要引入外部缓存组件（自托管单进程足够）。
//
// 设计取舍：
//   - 只做「读缓存」：写入路径不主动失效，靠 TTL 自然过期。
//     调用方若需要"改完立即生效"的语义，请使用 Invalidate 主动失效。
//   - TTL 很短（5~60s 量级）：即使数据变化，最多滞后一个 TTL，
//     对名称映射、统计汇总这类展示数据完全可接受。
//   - 并发安全：内部用 sync.Mutex 保护，足够满足单进程网关的并发量。
package server

import (
	"sync"
	"time"
)

// ttlCacheEntry 是缓存中的一个条目。
type ttlCacheEntry struct {
	value     any
	expiresAt time.Time
}

// ttlCache 是简单的进程内 TTL 缓存。
type ttlCache struct {
	mu    sync.Mutex
	items map[string]ttlCacheEntry
	ttl   time.Duration
}

// newTTLCache 创建 TTL 缓存，ttl 为条目的默认存活时间。
func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{
		items: make(map[string]ttlCacheEntry),
		ttl:   ttl,
	}
}

// Get 读取缓存；命中且未过期时返回 true 与值，否则返回 false。
//
// 惰性清理：读路径发现已过期条目会顺手删除（避免条目堆积），
// 但不做主动后台清扫——条目数量有限（映射表、统计键），
// 惰性回收足够，不引入额外的 goroutine 生命周期管理。
func (c *ttlCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.items, key)
		return nil, false
	}
	return entry.value, true
}

// Set 写入缓存条目，使用默认 TTL。
func (c *ttlCache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = ttlCacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Invalidate 主动删除一个键（供"改完立即生效"的场景使用）。
func (c *ttlCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Len 返回当前缓存的条目数（调试/观测用）。
func (c *ttlCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}