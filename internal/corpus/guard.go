// Package corpus 提供「语料共建计划」的运行时判定与原文采集缓冲。
//
// 意图（Why）：
//
//	转发链路上有两个新问题，都不适合直接查库：
//	  1) "这次请求要不要采原文" —— 每个请求都要判一次，查库会把它变成热点；
//	  2) "这个用户调这个模型要不要免计费" —— 同上，而且它在鉴权/计费两个地方都要判。
//
//	因此本包把三张表的相关内容做成**内存快照**（Guard），按周期刷新；
//	判定退化为一次内存查表（读锁 + map 命中），转发链路上零数据库开销。
//	这个做法与本项目已有的敏感词过滤器完全一致，行为可预期。
//
//	另一个职责是 Recorder：一次请求的原文缓冲。它是 io.Writer，
//	可以直接挂到现有的响应回写 tee 上，对原有转发逻辑零改动。
//
// 失败时的取舍（重要）：
//
//	快照加载失败【保留旧快照】而不是清空：
//	  · 采集侧清空 = 悄悄少采；保留旧快照 = 按上一次的清单继续采，语义稳定；
//	  · 免计费侧清空 = 福利账户突然开始被扣钱（用户侧可见的事故）。
//	启动时若一次都没加载成功，快照为空：此时不采集、也不免计费（宁可保守）。
//
// 流转（Flow）：
//
//	NewGuard(repo) → Start(ctx, interval) 周期刷新
//	  ├─ ShouldCollect(model)        → relay 决定要不要建 Recorder
//	  └─ IsFree(userID, model)       → 鉴权/计费决定要不要跳过扣费
//
// 扩展（Extend）：
//
//	新增"用户可退出采集"时：在 Guard 里加一个 optOut 集合 + 一个 ShouldCollect 的重载，
//	本包的其余部分（Recorder、上下文）无需改动。
package corpus

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// defaultRefreshInterval 是内存快照的刷新周期。
//
// 取 30 秒：既能让"撤销福利资格""下架模型"在一次半分钟内生效，
// 又不至于把三张小表的查询变成高频动作。判定本身是纯内存操作，不受此影响。
const defaultRefreshInterval = 30 * time.Second

// Guard 是语料清单与福利资格的内存快照，并发安全。
type Guard struct {
	repo model.CorpusRepository

	mu     sync.RWMutex
	models map[string]struct{} // 需要采集原文的对外模型名
	free   map[uint64]map[string]struct{}
	loaded bool
}

// NewGuard 创建判定组件；repo 为 nil 时所有判定恒为"否"（便于测试与降级）。
func NewGuard(repo model.CorpusRepository) *Guard {
	return &Guard{
		repo:   repo,
		models: map[string]struct{}{},
		free:   map[uint64]map[string]struct{}{},
	}
}

// Refresh 重新加载快照。加载失败时保留旧快照并返回错误（调用方只需记日志）。
func (g *Guard) Refresh(ctx context.Context) error {
	if g == nil || g.repo == nil {
		return nil
	}

	models, err := g.repo.EnabledCorpusModels(ctx)
	if err != nil {
		return err
	}
	grants, err := g.repo.ActiveCorpusGrants(ctx)
	if err != nil {
		return err
	}

	modelSet := make(map[string]struct{}, len(models))
	for _, name := range models {
		if name != "" {
			modelSet[name] = struct{}{}
		}
	}
	freeSet := make(map[uint64]map[string]struct{}, len(grants))
	for _, grant := range grants {
		if grant == nil || grant.UserID == 0 || grant.Model == "" || !grant.FreeAccess {
			continue
		}
		item, ok := freeSet[grant.UserID]
		if !ok {
			item = map[string]struct{}{}
			freeSet[grant.UserID] = item
		}
		item[grant.Model] = struct{}{}
	}

	g.mu.Lock()
	g.models = modelSet
	g.free = freeSet
	g.loaded = true
	g.mu.Unlock()
	return nil
}

// Start 先加载一次，再按周期刷新，直到 ctx 结束。
//
// 调用方应放在 goroutine 里；本方法不 panic、不退出进程，
// 单轮失败只 warn（与项目其它后台协程保持一致）。
func (g *Guard) Start(ctx context.Context, interval time.Duration) {
	if g == nil || g.repo == nil {
		return
	}
	if interval <= 0 {
		interval = defaultRefreshInterval
	}

	if err := g.Refresh(ctx); err != nil {
		// 启动时加载失败只告警：此时快照为空（不采集、不免计费），
		// 下一轮会自动补上，不会让服务起不来。
		slog.Warn("语料共建快照首次加载失败，暂时按空清单运行", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.Refresh(ctx); err != nil {
				slog.Warn("刷新语料共建快照失败，沿用上一次快照", "error", err)
			}
		}
	}
}

// ShouldCollect 判断该对外模型是否需要采集原文。
func (g *Guard) ShouldCollect(modelName string) bool {
	if g == nil || modelName == "" {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.models[modelName]
	return ok
}

// IsFree 判断该用户调用该模型是否免计费。
func (g *Guard) IsFree(userID uint64, modelName string) bool {
	if g == nil || userID == 0 || modelName == "" {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	byModel, ok := g.free[userID]
	if !ok {
		return false
	}
	_, hit := byModel[modelName]
	return hit
}

// ModelCount 返回快照里"需要采集"的模型数量（供健康检查/后台展示）。
func (g *Guard) ModelCount() int {
	if g == nil {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.models)
}

// FreeUserCount 返回快照里享有免计费的用户数（供健康检查/后台展示）。
func (g *Guard) FreeUserCount() int {
	if g == nil {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.free)
}

// Loaded 表示快照是否成功加载过至少一次。
func (g *Guard) Loaded() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.loaded
}
