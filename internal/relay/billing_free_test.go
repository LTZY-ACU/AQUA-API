// 语料共建「特殊福利账户」在计费侧的单元测试。
//
// 意图（Why）：
//
//	免计费一旦写错，方向只有两种、且都很糟：
//	  · 判宽了 → 不该免费的人被免费（直接资损）；
//	  · 判窄了 → 该免费的人被扣钱（用户侧可见的投诉）。
//	因此把"谁能免、免到什么程度"逐条钉死。
//
//	另一个必须验证的点：豁免**只针对指定模型**，不能顺带把该用户的其他调用也放过。
//
// 流转（Flow）：
//
//	go test ./internal/relay/ -run 福利
//
// 扩展（Extend）：
//
//	新增"按分组免计费"时，在本文件补一条与"按用户免计费"并存的用例。
package relay

import (
	"context"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// stubFreeChecker 是内存版免计费判定（对应 corpus.Guard 的最小能力集）。
type stubFreeChecker struct {
	// byUser: 用户 ID → 免计费的模型集合
	byUser map[uint64]map[string]bool
}

func (s stubFreeChecker) IsFree(userID uint64, model string) bool {
	models, ok := s.byUser[userID]
	if !ok {
		return false
	}
	return models[model]
}

// TestBilling_福利账户_按用户与模型免计费 覆盖 Charge 的豁免口径。
func TestBilling_福利账户_按用户与模型免计费(t *testing.T) {
	ctx := context.Background()
	free := stubFreeChecker{byUser: map[uint64]map[string]bool{
		37: {"test-model": true},
	}}

	// 两个都定价的模型：用于验证"豁免只对白名单里的那一个生效"。
	prices := &fakePriceRepo{prices: []*model.ModelPrice{
		{ID: 1, Model: "test-model", PromptPrice: 1_000, CompletionPrice: 2_000, Group: "default", Enabled: true},
		{ID: 2, Model: "other-model", PromptPrice: 1_000, CompletionPrice: 2_000, Group: "default", Enabled: true},
	}}
	billing := NewBilling(prices, newFakeGroupRepo(100), nil, nil, "default").WithFreeChecker(free)

	// 基准：普通用户照常计费（用于确认"豁免不是把所有人都放过了"）
	if got := billing.Charge(ctx, "default", 1, 0, "test-model", 1_000, 1_000, 0); got <= 0 {
		t.Fatalf("普通用户应正常计费，实际 %d", got)
	}
	// 福利账户：不扣
	if got := billing.Charge(ctx, "default", 37, 0, "test-model", 1_000, 1_000, 0); got != 0 {
		t.Errorf("福利账户应免计费，实际扣了 %d", got)
	}
	// 同一用户的其他模型照常计费：豁免只对白名单里的模型生效
	if got := billing.Charge(ctx, "default", 37, 0, "other-model", 1_000, 1_000, 0); got <= 0 {
		t.Errorf("福利账户调用白名单外的模型应正常计费，实际 %d", got)
	}

	// 未配置判定组件时行为与引入本功能前一致
	plain := NewBilling(prices, newFakeGroupRepo(100), nil, nil, "default")
	if got := plain.Charge(ctx, "default", 37, 0, "test-model", 1_000, 1_000, 0); got <= 0 {
		t.Errorf("未注入福利判定时应照常计费，实际 %d", got)
	}
}

// TestBilling_福利账户_按次计费同样豁免 覆盖 ChargeOnce 的豁免口径。
//
// 必须单独测：异步任务（图像/视频等）走的是按次扣费的另一条路径，
// 只在 Charge 里加判定会让"按次计费的任务"漏掉豁免。
func TestBilling_福利账户_按次计费同样豁免(t *testing.T) {
	ctx := context.Background()
	free := stubFreeChecker{byUser: map[uint64]map[string]bool{
		139: {"test-model": true},
	}}

	billing := newTestBilling(100, 0, 0, 2_000).WithFreeChecker(free)

	if got := billing.ChargeOnce(ctx, "default", 1, 0, "test-model", 1); got <= 0 {
		t.Fatalf("普通用户按次调用应正常计费，实际 %d", got)
	}
	if got := billing.ChargeOnce(ctx, "default", 139, 0, "test-model", 1); got != 0 {
		t.Errorf("福利账户按次调用应免计费，实际扣了 %d", got)
	}
}

// TestBilling_福利判定为假实现时不影响其它用户 覆盖"判定组件为空"的降级路径。
func TestBilling_福利判定为空时不影响其它用户(t *testing.T) {
	ctx := context.Background()
	billing := newTestBilling(100, 1_000, 2_000, 0).WithFreeChecker(stubFreeChecker{})

	if got := billing.Charge(ctx, "default", 37, 0, "test-model", 1_000, 1_000, 0); got <= 0 {
		t.Errorf("判定恒为否时应照常计费，实际 %d", got)
	}
}
