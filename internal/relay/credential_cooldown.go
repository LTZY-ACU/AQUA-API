// 本文件实现「(凭据 × 模型) 级冷却表」——进程内、按组合生效的冷却。
//
// 意图（Why）：
//
//	凭据池里的失败常常是【模型维度】的：同一把密钥在模型 A 上被限流或没有授权，
//	在模型 B/C 上却完全正常。若沿用"整把密钥冷却"（channel_keys.cooldown_until），
//	模型 A 的一次失败会把整把密钥从调度里摘出去，连带影响同渠道其他模型。
//
//	因此这里提供一张键为 (keyID, model) 的进程内冷却表：
//	同一把密钥在模型 A 上失败，只影响「该密钥 × 模型 A」，模型 B/C 仍可用。
//
//	为什么先做进程内（而不是落库）：不引入迁移、重启即清，改动半径最小；
//	代价是重启后冷却丢失（可能重试一次已冷却的组合）、多实例不共享——
//	对"冷却"这种自愈语义而言代价可接受。后续若确需持久化/跨实例共享，
//	再把这张表下沉到 store 层（新增 (channel_key_id, model, cooldown_until) 表）。
//
// 流转（Flow）：
//
//	forwardChat 命中凭据级失败
//	  └─ modelCooldownsFor(r).mark(keyID, model, until)
//	resolveChatCredential 选凭据
//	  └─ filterUsableKeysForRequest(..., book)
//	       ├─ book.cooling(keyID, model)      → 命中即跳过该组合
//	       └─ book.hasLiveEntry(keyID)        → 决定整把冷却是否可被模型级记录覆盖
//
// 扩展（Extend）：
//
//	要持久化时：把 mark/cooling 的底层存储换成 store 仓储，调用点不用改。
package relay

import (
	"sync"
	"time"
)

// defaultModelCooldownCapacity 是冷却表的条目上限。
//
// 为什么要设上限：条目键由 (keyID, model) 组合而来，理论上组合数很大；
// 设一个上限并在超限时先清理过期条目、仍满则放弃写入，
// 保证内存不因异常流量（大量模型名）而无界增长——冷却属优化项，宁可不写也不能撑爆内存。
const defaultModelCooldownCapacity = 20000

// credentialModelKey 是「凭据 × 模型」冷却的键。
//
// 只用 (keyID, model)：一把凭据只属于一个渠道，因此等价于「渠道 × 模型 × 凭据」。
type credentialModelKey struct {
	keyID uint64
	model string
}

// credentialModelCooldownBook 是 (凭据, 模型) → 冷却截止时间的进程内表（并发安全）。
type credentialModelCooldownBook struct {
	mu         sync.Mutex
	entries    map[credentialModelKey]time.Time
	byKey      map[uint64]int // keyID → 该凭据当前有效的模型级冷却条目数（供 hasLiveEntry 快速判定）
	now        func() time.Time
	maxEntries int
}

// newCredentialModelCooldownBook 创建一张冷却表；容量 <= 0 时使用默认值。
func newCredentialModelCooldownBook() *credentialModelCooldownBook {
	return &credentialModelCooldownBook{
		entries:    make(map[credentialModelKey]time.Time),
		byKey:      make(map[uint64]int),
		now:        time.Now,
		maxEntries: defaultModelCooldownCapacity,
	}
}

// clock 返回当前时间（测试可注入固定时钟）。
func (b *credentialModelCooldownBook) clock() time.Time {
	if b != nil && b.now != nil {
		return b.now()
	}
	return time.Now()
}

// mark 登记一次 (凭据, 模型) 冷却；until 已过期或参数非法时不登记。
func (b *credentialModelCooldownBook) mark(keyID uint64, model string, until time.Time) {
	if b == nil || keyID == 0 || model == "" {
		return
	}
	now := b.clock()
	if !until.After(now) {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.purgeExpiredLocked(now)

	key := credentialModelKey{keyID: keyID, model: model}
	if _, exists := b.entries[key]; !exists {
		if len(b.entries) >= b.maxEntries {
			// 清理后仍满：放弃写入（冷却属优化项，宁可少写也不让内存无界增长）
			return
		}
		b.byKey[keyID]++
	}
	b.entries[key] = until
}

// cooling 判断指定 (凭据, 模型) 组合当前是否处于冷却中；命中并已过期时顺手清理。
func (b *credentialModelCooldownBook) cooling(keyID uint64, model string) bool {
	if b == nil || keyID == 0 || model == "" {
		return false
	}
	now := b.clock()

	b.mu.Lock()
	defer b.mu.Unlock()

	key := credentialModelKey{keyID: keyID, model: model}
	until, ok := b.entries[key]
	if !ok {
		return false
	}
	if !until.After(now) {
		b.removeLocked(key)
		return false
	}
	return true
}

// hasLiveEntry 判断某凭据当前是否存在任何有效的模型级冷却记录。
//
// 用途：整把密钥的 DB 冷却（cooldown_until）若可被"模型级记录"解释，
// 说明这次失败是模型维度的，不应牵连其他模型——调用方据此决定是否忽略整把冷却。
func (b *credentialModelCooldownBook) hasLiveEntry(keyID uint64) bool {
	if b == nil || keyID == 0 {
		return false
	}
	now := b.clock()

	b.mu.Lock()
	defer b.mu.Unlock()

	b.purgeExpiredLocked(now)
	return b.byKey[keyID] > 0
}

// purgeExpiredLocked 在已持锁前提下清理所有已过期的条目。
func (b *credentialModelCooldownBook) purgeExpiredLocked(now time.Time) {
	for key, until := range b.entries {
		if !until.After(now) {
			b.removeLocked(key)
		}
	}
}

// removeLocked 在已持锁前提下删除一条冷却记录并维护 byKey 计数。
func (b *credentialModelCooldownBook) removeLocked(key credentialModelKey) {
	if _, ok := b.entries[key]; !ok {
		return
	}
	delete(b.entries, key)
	if n := b.byKey[key.keyID] - 1; n <= 0 {
		delete(b.byKey, key.keyID)
	} else {
		b.byKey[key.keyID] = n
	}
}

// len 返回当前有效条目数（测试用）。
func (b *credentialModelCooldownBook) len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// relayCooldownBooks 为每个 Relay 实例旁挂一张 (凭据, 模型) 冷却表。
//
// 为什么按实例隔离、而不是包级单例：包级单例会跨实例共享状态，
// 多实例部署（以及并行测试）之间互相污染冷却，表现为"某把凭据莫名不可用"。
//
// 为什么用 sync.Map 旁挂、而不是给 Relay 加字段：本能力要求在【不修改 relay.go
// 结构定义】的前提下落地，因此用"以 *Relay 为键的旁挂表"达到同样的实例隔离效果。
// 生产环境只有一个 Relay，表里也就只有一项；测试进程结束即整体释放。
var relayCooldownBooks sync.Map // map[*Relay]*credentialModelCooldownBook

// modelCooldownsFor 返回指定 Relay 的 (凭据, 模型) 冷却表（惰性创建）。
func modelCooldownsFor(r *Relay) *credentialModelCooldownBook {
	if r == nil {
		// 没有 Relay 上下文时给一张一次性空表：所有查询都命中不到，等价于"无冷却"。
		return newCredentialModelCooldownBook()
	}
	if existing, ok := relayCooldownBooks.Load(r); ok {
		if book, ok := existing.(*credentialModelCooldownBook); ok {
			return book
		}
	}
	book := newCredentialModelCooldownBook()
	actual, _ := relayCooldownBooks.LoadOrStore(r, book)
	if typed, ok := actual.(*credentialModelCooldownBook); ok {
		return typed
	}
	return book
}

// markModelCooldown 在 (凭据, 模型) 维度登记一次冷却。
//
// cooldown <= 0 或缺少凭据/模型时不登记（无模型上下文时由整把冷却语义兜底）。
func (r *Relay) markModelCooldown(keyID uint64, model string, cooldown time.Duration) {
	if r == nil || keyID == 0 || model == "" || cooldown <= 0 {
		return
	}
	modelCooldownsFor(r).mark(keyID, model, time.Now().Add(cooldown))
}
