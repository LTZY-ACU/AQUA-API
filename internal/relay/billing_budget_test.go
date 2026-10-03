// 计费组件的「周期预算闸门」与「分组当日消耗」单元测试。
//
// 意图（Why）：
//
//	预算闸门是鉴权放行前的一道独立判定，写错会直接造成两类后果：
//	  · 该拦没拦 → 额度被短期跑穿（资损）；
//	  · 不该拦却拦 → 存量令牌突然不可用（线上事故）。
//	因此这里逐条验证：未启用一律放行、窗口内超限判定、窗口过期惰性重置、
//	不限额度令牌豁免、以及未注入仓储时的降级行为。
//
// 流转（Flow）：
//
//	go test ./internal/relay/ -run Budget
//
// 扩展（Extend）：
//
//	新增"按用户预算"后，仿照本文件补一个用户维度的 fake 与用例。
package relay

import (
	"context"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// fakeBudgetTokenRepo 是只服务于预算用例的内存版令牌仓储。
//
// 只实现本用例需要的行为：GetByID 返回预置令牌、ResetBudgetWindow 记录调用、
// GroupSpendToday 返回预置值；其余方法为空实现。
type fakeBudgetTokenRepo struct {
	tokens     map[uint64]*model.Token
	resetCalls []uint64 // 记录 ResetBudgetWindow 被调用的令牌 ID
	groupSpend int64    // GroupSpendToday 的预置返回值
}

func (f *fakeBudgetTokenRepo) Create(context.Context, *model.Token) error { return nil }

func (f *fakeBudgetTokenRepo) GetByID(_ context.Context, id uint64) (*model.Token, error) {
	tk, ok := f.tokens[id]
	if !ok {
		return nil, model.ErrTokenNotFound
	}
	return tk, nil
}

func (f *fakeBudgetTokenRepo) GetByKey(context.Context, string) (*model.Token, error) {
	return nil, model.ErrTokenNotFound
}

func (f *fakeBudgetTokenRepo) List(context.Context, model.TokenQuery) ([]*model.Token, error) {
	return nil, nil
}

func (f *fakeBudgetTokenRepo) Count(context.Context, model.TokenQuery) (int, error) { return 0, nil }

func (f *fakeBudgetTokenRepo) StatusCounts(context.Context) (map[model.TokenStatus]int, error) {
	return nil, nil
}

func (f *fakeBudgetTokenRepo) RecordUsage(context.Context, uint64, time.Time) error { return nil }

func (f *fakeBudgetTokenRepo) ConsumeQuota(context.Context, uint64, int64, time.Time) error {
	return nil
}

func (f *fakeBudgetTokenRepo) ResetBudgetWindow(_ context.Context, id uint64, _ time.Time) error {
	f.resetCalls = append(f.resetCalls, id)
	return nil
}

func (f *fakeBudgetTokenRepo) GroupSpendToday(context.Context, string, time.Time, time.Time) (int64, error) {
	return f.groupSpend, nil
}

func (f *fakeBudgetTokenRepo) Update(context.Context, *model.Token) error { return nil }

func (f *fakeBudgetTokenRepo) Delete(context.Context, uint64) error { return nil }

// TestBilling_BudgetExceeded_窗口内累计与超限 覆盖未超限 / 超限两条路径。
func TestBilling_BudgetExceeded_窗口内累计与超限(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	repo := &fakeBudgetTokenRepo{tokens: map[uint64]*model.Token{
		1: { // 窗口内已用 40 / 上限 100 → 不超限
			ID: 1, BudgetQuota: 100, BudgetPeriod: model.BudgetPeriodDaily,
			BudgetWindowStart: now.Add(-time.Hour), BudgetWindowBase: 0, UsedQuota: 40,
		},
		2: { // 窗口内已用 100 / 上限 100 → 超限
			ID: 2, BudgetQuota: 100, BudgetPeriod: model.BudgetPeriodDaily,
			BudgetWindowStart: now.Add(-time.Hour), BudgetWindowBase: 500, UsedQuota: 600,
		},
		3: { // 未启用（默认 0）→ 放行
			ID: 3, BudgetQuota: 0, BudgetPeriod: "", UsedQuota: 9999,
		},
		4: { // 不限额度令牌 → 放行
			ID: 4, UnlimitedQuota: true, BudgetQuota: 100, BudgetPeriod: model.BudgetPeriodDaily, UsedQuota: 9999,
		},
	}}
	billing := NewBilling(nil, nil, repo, nil, "default")

	if exceeded, err := billing.BudgetExceeded(ctx, 1); err != nil || exceeded {
		t.Errorf("令牌 1 不应超限，实际 (%v,%v)", exceeded, err)
	}
	if exceeded, err := billing.BudgetExceeded(ctx, 2); err != nil || !exceeded {
		t.Errorf("令牌 2 应超限，实际 (%v,%v)", exceeded, err)
	}
	if exceeded, err := billing.BudgetExceeded(ctx, 3); err != nil || exceeded {
		t.Errorf("令牌 3 未启用预算应放行，实际 (%v,%v)", exceeded, err)
	}
	if exceeded, err := billing.BudgetExceeded(ctx, 4); err != nil || exceeded {
		t.Errorf("令牌 4 不限额度应放行，实际 (%v,%v)", exceeded, err)
	}
	if len(repo.resetCalls) != 0 {
		t.Errorf("窗口未过期时不应触发重置，实际 %v", repo.resetCalls)
	}
}

// TestBilling_BudgetExceeded_窗口过期惰性重置 覆盖"过期即重置并放行"。
func TestBilling_BudgetExceeded_窗口过期惰性重置(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	repo := &fakeBudgetTokenRepo{tokens: map[uint64]*model.Token{
		7: { // 日窗口起点在两小时前 → 已过期，且"看起来"已用满
			ID: 7, BudgetQuota: 100, BudgetPeriod: model.BudgetPeriodDaily,
			BudgetWindowStart: now.AddDate(0, 0, -2), BudgetWindowBase: 0, UsedQuota: 9999,
		},
	}}
	billing := NewBilling(nil, nil, repo, nil, "default")

	exceeded, err := billing.BudgetExceeded(ctx, 7)
	if err != nil {
		t.Fatalf("窗口过期时应惰性重置并放行，实际报错: %v", err)
	}
	if exceeded {
		t.Error("窗口刚翻篇不应判超限")
	}
	if len(repo.resetCalls) != 1 || repo.resetCalls[0] != 7 {
		t.Errorf("应恰好重置一次令牌 7 的窗口，实际 %v", repo.resetCalls)
	}
}

// TestBilling_BudgetExceeded_降级路径 覆盖未注入仓储 / 令牌不存在 / tokenID=0。
func TestBilling_BudgetExceeded_降级路径(t *testing.T) {
	ctx := context.Background()

	// 未注入令牌仓储：一律放行，不报错
	plain := NewBilling(nil, nil, nil, nil, "default")
	if exceeded, err := plain.BudgetExceeded(ctx, 1); err != nil || exceeded {
		t.Errorf("未注入仓储应放行，实际 (%v,%v)", exceeded, err)
	}

	repo := &fakeBudgetTokenRepo{tokens: map[uint64]*model.Token{}}
	billing := NewBilling(nil, nil, repo, nil, "default")

	if exceeded, err := billing.BudgetExceeded(ctx, 999); err != nil || exceeded {
		t.Errorf("令牌不存在应放行，实际 (%v,%v)", exceeded, err)
	}
	if exceeded, err := billing.BudgetExceeded(ctx, 0); err != nil || exceeded {
		t.Errorf("tokenID=0 应放行，实际 (%v,%v)", exceeded, err)
	}
}

// TestBilling_GroupSpendToday 验证分组当日消耗查询的委托与降级。
func TestBilling_GroupSpendToday(t *testing.T) {
	ctx := context.Background()

	repo := &fakeBudgetTokenRepo{tokens: map[uint64]*model.Token{}, groupSpend: 4321}
	billing := NewBilling(nil, nil, repo, nil, "default")
	got, err := billing.GroupSpendToday(ctx, "vip")
	if err != nil {
		t.Fatalf("GroupSpendToday 失败: %v", err)
	}
	if got != 4321 {
		t.Errorf("GroupSpendToday = %d，期望 4321", got)
	}

	// 未注入仓储：返回 0 且不报错
	plain := NewBilling(nil, nil, nil, nil, "default")
	if n, err := plain.GroupSpendToday(ctx, "vip"); err != nil || n != 0 {
		t.Errorf("未注入仓储应返回 (0,nil)，实际 (%d,%v)", n, err)
	}
}
