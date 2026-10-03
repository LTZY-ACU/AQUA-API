// 任务提交链路释放"中间件额度预留"的单元测试。
//
// 意图（Why）：
//
//	TokenAuth 中间件对所有 /v1 路由（含 POST /v1/tasks）都会先做一次
//	EstimateReserve→Reserve —— 这是一次【真实扣减】并写入 quota_reservations 在途台账。
//	同步链路在转发结束后由 settleQuota 做 Settle/Release 闭环；而任务链路不经过
//	recordUsage，若不主动释放，预留会滞留在途直到 15 分钟 TTL 才被回收，
//	期间用户可用额度被【多占用一份】。本测试锁死"提交即释放"这一行为。
//
// 流转（Flow）：
//
//	Submit 最早处（覆盖所有早退路径）释放在途预留 → 后续 ChargeOnce 独立扣真实费用。
//
// 扩展（Extend）：
//
//	若将来新增任何"绕过 usage.go 结算闭环"的请求入口，都要在此类测试里补一条
//	"预留必须被释放/结算"的回归，避免同样的额度滞留缺陷再次出现。
package relay

import (
	"context"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/reqctx"
)

// TestTaskService_Submit_释放中间件预留 验证任务提交会释放在途预留、不滞留额度。
func TestTaskService_Submit_释放中间件预留(t *testing.T) {
	ctx := context.Background()

	fake := newFakeQuotaRepo()
	billing := newTestBilling(100, 0, 0, 500).WithQuotaRepository(fake)
	// tasks 留空：让 Submit 在处理早期（释放预留之后）即返回错误，
	// 从而在不依赖上游适配器的前提下验证"释放先于业务逻辑发生"。
	svc := &TaskService{relay: &Relay{billing: billing}}

	// 模拟中间件已为本请求预留 500（真实扣减）并把 requestID 写入 context。
	if _, err := billing.Reserve(ctx, model.ReserveRequest{
		RequestID: "task-req-1", UserID: 7, TokenID: 8, Amount: 500,
	}); err != nil {
		t.Fatalf("预留失败: %v", err)
	}
	reqCtx := reqctx.WithIdentity(ctx, reqctx.Identity{UserID: 7, TokenID: 8, RequestID: "task-req-1"})

	if _, err := svc.Submit(reqCtx, 7, 8, &TaskRequest{Kind: model.TaskKindImage, Model: "test-model"}); err == nil {
		t.Fatal("未装配任务仓储时 Submit 应返回错误")
	}

	if item := fake.byRequestID["task-req-1"]; item == nil || item.Status != model.ReservationReleased {
		t.Fatalf("中间件预留应被释放，实际 %+v", item)
	}
	if pending, err := billing.PendingReserved(ctx, 7); err != nil || pending != 0 {
		t.Fatalf("在途预留应为 0（不得多占用额度），实际 (%d, %v)", pending, err)
	}
}

// TestTaskService_Submit_无预留时不误释放 验证未做预留的请求不会被误判为在途。
func TestTaskService_Submit_无预留时不误释放(t *testing.T) {
	ctx := context.Background()

	fake := newFakeQuotaRepo()
	billing := newTestBilling(100, 0, 0, 500).WithQuotaRepository(fake)
	svc := &TaskService{relay: &Relay{billing: billing}}

	// context 中无 requestID（免费模型 / 信任额度旁路 / 未启用预留）：
	// Submit 不得触碰台账，否则会误报"释放了不存在的预留"。
	if _, err := svc.Submit(ctx, 7, 8, &TaskRequest{Kind: model.TaskKindImage, Model: "test-model"}); err == nil {
		t.Fatal("未装配任务仓储时 Submit 应返回错误")
	}
	if fake.releaseCalls != 0 {
		t.Fatalf("无预留时不应调用 Release，实际 %d 次", fake.releaseCalls)
	}
}
