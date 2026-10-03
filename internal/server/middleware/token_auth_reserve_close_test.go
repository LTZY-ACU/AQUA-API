// 额度预留「闭环」与「不重复扣减」的回归测试。
//
// 背景（为什么必须锁死这两条语义）：
//
//  1. 预留落台账的同时额度就已加进 used_quota，可用额度若再减一遍在途，
//     会把同一份额度扣两次——额度 100、在途 100 的账号直接被误判 429。
//  2. 预留一旦产生就必须有人闭环：正常由 relay（Settle/Release）或任务服务
//     （提交即退还）负责，但敏感词拦截、参数校验失败、任务提交前早退等路径
//     走不到结算，额度会一直占到 15 分钟 TTL 到期才被回收，
//     期间用户"有钱却用不了"。
//
// 流转（Flow）：
//
//	go test ./internal/server/middleware/
//	  └─ 用真实仓储（临时 SQLite）+ 可控的 fakeQuotaReserver 走完整中间件链路
package middleware

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/reqctx"
)

// TestTokenAuth_在途预留不重复扣减 锁死"可用额度不再减在途预留"这一修复。
//
// 旧实现：可用额度 = 总额度 − 已用 − 在途；额度 100、在途 100 → 0 → 429。
// 现在：在途已计入 used_quota，可用 = 100 − 0 = 100 → 放行，由 Reserve 原子判定。
func TestTokenAuth_在途预留不重复扣减(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := newLimitedOwnerAndToken(t, tokens, users, "pending-dedup-owner", 100, 1000)

	reserver := &fakeQuotaReserver{priced: true, estimateAmount: 50, pending: 100}
	engine := newQuotaAuthEngine(t, tokens, users, reserver, okHandler)

	rec := doAuthRequest(t, engine, map[string]string{"Authorization": "Bearer " + key},
		`{"model":"priced-model","messages":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("在途预留不应被重复扣减：可用额度 = 100 − 0 = 100，应放行，实际 %d（%s）",
			rec.Code, rec.Body.String())
	}
	if reserver.reserveCalls != 1 {
		t.Fatalf("应正常进入预留流程 1 次，实际 %d 次", reserver.reserveCalls)
	}
	// 在途预留只在"额度耗尽"的错误文案里查：热路径不该每次读库。
	if reserver.pendingCalls != 0 {
		t.Fatalf("非失败路径不应查询在途预留，实际查询 %d 次", reserver.pendingCalls)
	}
}

// TestTokenAuth_预留闭环_下游未结算时中间件兜底退还
// 覆盖下游走不到结算的路径（敏感词拦截、参数校验失败等）。
func TestTokenAuth_预留闭环_下游未结算时中间件兜底退还(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := newLimitedOwnerAndToken(t, tokens, users, "unclosed-reserve-owner", 1000, 1000)

	reserver := &fakeQuotaReserver{priced: true, estimateAmount: 10}
	engine := newQuotaAuthEngine(t, tokens, users, reserver, okHandler)

	rec := doAuthRequest(t, engine, map[string]string{"Authorization": "Bearer " + key},
		`{"model":"priced-model","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（%s）", rec.Code, rec.Body.String())
	}

	if reserver.reserveCalls != 1 {
		t.Fatalf("应预留 1 次，实际 %d 次", reserver.reserveCalls)
	}
	if reserver.releaseCalls != 1 {
		t.Fatalf("下游没有结算，中间件应兜底退还 1 次，实际 %d 次", reserver.releaseCalls)
	}
	if reserver.lastReleasedID != reserver.lastRequestID {
		t.Errorf("退还的幂等键 = %q，与预留的 %q 不一致", reserver.lastReleasedID, reserver.lastRequestID)
	}
}

// TestTokenAuth_预留闭环_已结算则不再退还 验证结算方置位后兜底跳过（热路径零额外写库）。
func TestTokenAuth_预留闭环_已结算则不再退还(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := newLimitedOwnerAndToken(t, tokens, users, "settled-reserve-owner", 1000, 1000)

	reserver := &fakeQuotaReserver{priced: true, estimateAmount: 10}
	engine := newQuotaAuthEngine(t, tokens, users, reserver, func(c *gin.Context) {
		guard, ok := reqctx.ReservationGuardFrom(c.Request.Context())
		if !ok {
			t.Fatal("做了预留就必须挂上闭环标记，否则中间件无法区分'已结算'与'没人管'")
		}
		// 等价于 relay.Billing.Settle / Release 成功后的置位动作
		guard.MarkClosed()
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rec := doAuthRequest(t, engine, map[string]string{"Authorization": "Bearer " + key},
		`{"model":"priced-model","messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（%s）", rec.Code, rec.Body.String())
	}
	if reserver.reserveCalls != 1 {
		t.Fatalf("应预留 1 次，实际 %d 次", reserver.reserveCalls)
	}
	if reserver.releaseCalls != 0 {
		t.Fatalf("已结算的预留不应再被兜底退还，实际退还 %d 次", reserver.releaseCalls)
	}
}

// TestTokenAuth_预留闭环_下游返回错误也兜底退还 与上一用例互补：
// 下游即使已经写出错误响应（上游失败 / 业务拒绝），预留同样必须闭环。
func TestTokenAuth_预留闭环_下游返回错误也兜底退还(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := newLimitedOwnerAndToken(t, tokens, users, "reject-reserve-owner", 1000, 1000)

	reserver := &fakeQuotaReserver{priced: true, estimateAmount: 10}
	engine := newQuotaAuthEngine(t, tokens, users, reserver, func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "rejected"}})
	})

	rec := doAuthRequest(t, engine, map[string]string{"Authorization": "Bearer " + key},
		`{"model":"priced-model","messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", rec.Code)
	}
	if reserver.releaseCalls != 1 {
		t.Fatalf("下游返回错误时应兜底退还 1 次，实际 %d 次", reserver.releaseCalls)
	}
}
