// 支付对账循环的单元测试。
//
// 覆盖三个"错了会直接资损"的场景：
//   - 回调丢失但用户已支付 → 查单后自动补账（本功能存在的意义）；
//   - 网关明确未支付且订单已过期 → 才允许关闭（防误关正准备付款的订单）；
//   - 网关金额与本地订单不一致 → 拒绝入账（与回调路径同一标准）。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newReconcileTestEnv 装配对账测试环境，网关为可编程的 httptest 服务。
//
// 返回 srv 与「设置网关应答」的函数：对账循环会真实发起 HTTP 查单，
// 必须用可控的假网关，绝不依赖外部网络。
func newReconcileTestEnv(t *testing.T) (*Server, func(body string)) {
	t.Helper()
	srv, _ := newNotifyTestServer(t)

	var responseBody string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "order" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(gateway.Close)

	// 覆盖 notifyTestEPaySetup 写入的不可达网关，指向本地假网关（商户号保持一致）。
	// 同时补上充值总开关：新库默认关闭，不开对账循环会在首轮就短路返回。
	if err := srv.deps.Settings.SetMany(context.Background(), map[string]string{
		model.SettingKeyPaymentEnabled: "true",
		model.SettingKeyPaymentParams:  `{"epay.pid":"` + notifyTestEPayPID + `","epay.gateway":"` + gateway.URL + `","epay.types":"alipay"}`,
	}); err != nil {
		t.Fatalf("覆盖支付网关设置失败: %v", err)
	}

	setGatewayBody := func(body string) { responseBody = body }
	return srv, setGatewayBody
}

// newReconcileBuyerAndOrder 建一个零额度用户与一笔易支付待支付订单。
func newReconcileBuyerAndOrder(t *testing.T, srv *Server, tradeNo string, amountCents, quota int64, expiresSoon bool) *model.PaymentOrder {
	t.Helper()
	ctx := context.Background()
	buyer := &model.User{
		Username:     "reconcile-" + tradeNo,
		PasswordHash: "test-hash",
		Email:        tradeNo + "@example.com",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
	if err := srv.deps.Users.Create(ctx, buyer); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	order := &model.PaymentOrder{
		TradeNo: tradeNo,
		UserID:  buyer.ID,
		Amount:  amountCents,
		Quota:   quota,
		Method:  "epay",
		Status:  model.PaymentStatusPending,
	}
	if expiresSoon {
		// 用正常的过去时间（真实订单的到期时间落库后是正数 unix；
		// 若用 time.Time{}.Add() 会得到负数，被 scanPaymentOrder 的
		// expiresAt > 0 守卫丢弃，测不到目标分支）。
		order.ExpiresAt = time.Now().Add(-time.Hour)
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建测试订单失败: %v", err)
	}
	return order
}

// TestPaymentReconcile_回调丢失_查单自动补账 是本功能的立身之本：
// 用户已支付、回调永远没到（迁移/配置错误），对账循环必须让钱到账。
func TestPaymentReconcile_回调丢失_查单自动补账(t *testing.T) {
	srv, setGateway := newReconcileTestEnv(t)
	ctx := context.Background()

	const quota = int64(10000)
	order := newReconcileBuyerAndOrder(t, srv, "pay20260102000001recon01", 1000, quota, false)

	// 网关回答：已支付 10.00 元（金额与本地订单一致）
	setGateway(`{"code":1,"data":{"trade_no":"GW20260102000001","money":"10.00","status":1}}`)

	srv.reconcilePaymentsOnce(ctx)

	paid, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if paid.Status != model.PaymentStatusPaid {
		t.Fatalf("订单状态 = %v，期望已支付（回调丢失应由查单补账）", paid.Status)
	}
	if !paid.IsCredited() {
		t.Fatal("订单未被标记入账")
	}
	if paid.ProviderTradeNo != "GW20260102000001" {
		t.Errorf("第三方单号 = %q，期望查单结果回填", paid.ProviderTradeNo)
	}

	buyer, err := srv.deps.Users.GetByID(ctx, order.UserID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if buyer.Quota != quota {
		t.Fatalf("补账后额度 = %d，期望 %d", buyer.Quota, quota)
	}

	// 再跑一轮：已支付订单不应对账（也不会重复入账）
	srv.reconcilePaymentsOnce(ctx)
	again, _ := srv.deps.Users.GetByID(ctx, order.UserID)
	if again.Quota != quota {
		t.Fatalf("重复对账后额度 = %d，期望仍为 %d（重复入账）", again.Quota, quota)
	}
}

// TestPaymentReconcile_迟到支付_已关闭订单补记 覆盖最恶劣的场景：
// 订单已因超时被关闭，但用户其实付了钱（回调丢了）——MarkPaid 允许
// 已关闭 → 已支付的补记，对账必须走通这条路。
func TestPaymentReconcile_迟到支付_已关闭订单补记(t *testing.T) {
	srv, setGateway := newReconcileTestEnv(t)
	ctx := context.Background()

	order := newReconcileBuyerAndOrder(t, srv, "pay20260102000002recon02", 2000, 20000, true)
	if err := srv.deps.Orders.UpdateStatus(ctx, order.TradeNo, model.PaymentStatusClosed); err != nil {
		t.Fatalf("预置已关闭状态失败: %v", err)
	}

	setGateway(`{"code":1,"data":{"trade_no":"GW20260102000002","money":"20.00","status":1}}`)
	srv.reconcilePaymentsOnce(ctx)

	paid, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if paid.Status != model.PaymentStatusPaid {
		t.Fatalf("订单状态 = %v，期望已支付（已关闭订单的迟到支付应补记）", paid.Status)
	}
	if !paid.IsCredited() {
		t.Fatal("补记后未入账")
	}
}

// TestPaymentReconcile_网关明确未支付_到期才关闭 锁死关单安全性：
// 未到期绝不关（用户可能正在付款），到期且网关说没付才关。
func TestPaymentReconcile_网关明确未支付_到期才关闭(t *testing.T) {
	srv, setGateway := newReconcileTestEnv(t)
	ctx := context.Background()

	fresh := newReconcileBuyerAndOrder(t, srv, "pay20260102000003recon03", 100, 1000, false)
	expired := newReconcileBuyerAndOrder(t, srv, "pay20260102000004recon04", 100, 1000, true)

	setGateway(`{"code":1,"data":{"money":"1.00","status":0}}`)
	srv.reconcilePaymentsOnce(ctx)

	gotFresh, _ := srv.deps.Orders.GetByTradeNo(ctx, fresh.TradeNo)
	if gotFresh.Status != model.PaymentStatusPending {
		t.Fatalf("未到期订单不应被关闭，实际 %v", gotFresh.Status)
	}
	gotExpired, _ := srv.deps.Orders.GetByTradeNo(ctx, expired.TradeNo)
	if gotExpired.Status != model.PaymentStatusClosed {
		t.Fatalf("已到期且网关明确未支付的订单应被关闭，实际 %v", gotExpired.Status)
	}
}

// TestPaymentReconcile_金额不一致_拒绝入账 与回调路径同标准：
// 网关说付了 0.01 元、本地订单是 10 元，绝不能按订单金额入账。
func TestPaymentReconcile_金额不一致_拒绝入账(t *testing.T) {
	srv, setGateway := newReconcileTestEnv(t)
	ctx := context.Background()

	order := newReconcileBuyerAndOrder(t, srv, "pay20260102000005recon05", 1000, 10000, false)

	setGateway(`{"code":1,"data":{"trade_no":"GW20260102000005","money":"0.01","status":1}}`)
	srv.reconcilePaymentsOnce(ctx)

	got, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if got.Status != model.PaymentStatusPending {
		t.Fatalf("金额不一致的订单不应入账，实际状态 %v", got.Status)
	}
	buyer, _ := srv.deps.Users.GetByID(ctx, order.UserID)
	if buyer.Quota != 0 {
		t.Fatalf("金额不一致时额度不应变化，实际 %d", buyer.Quota)
	}
}
