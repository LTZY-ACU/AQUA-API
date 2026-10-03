// 充值订单仓储的单元测试。
//
// 测试重点（都是"错了会直接资损"的场景）：
//   - 入账幂等：重复回调只能入账一次（第一次 true，第二次 false 且额度不变）；
//   - 未支付订单不允许入账；
//   - 入账与加额度在同一事务内完成（订单标记与用户额度必须同时变化）；
//   - 不限额度账户（quota = -1）入账时不修改额度。
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newTestOrderRepo 构造订单仓储并返回用户仓储（入账需要真实用户）。
func newTestOrderRepo(t *testing.T) (model.PaymentOrderRepository, model.UserRepository, uint64) {
	t.Helper()
	st := newTestStore(t)
	orderRepo := NewPaymentOrderRepository(st.DB())
	userRepo := NewUserRepository(st.DB())

	user := newActiveUser(t, "支付测试用户")
	if err := userRepo.Create(context.Background(), user); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return orderRepo, userRepo, user.ID
}

// newActiveUser 构造一个额度为 0 的普通用户。
func newActiveUser(t *testing.T, username string) *model.User {
	t.Helper()
	return &model.User{
		Username:     username,
		PasswordHash: "test-hash",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
}

// newPendingOrder 构造一笔待支付订单。
func newPendingOrder(userID uint64, tradeNo string, amount, quota int64) *model.PaymentOrder {
	return &model.PaymentOrder{
		TradeNo:   tradeNo,
		UserID:    userID,
		Amount:    amount,
		Currency:  "CNY",
		Quota:     quota,
		Method:    model.PaymentMethodManual,
		Status:    model.PaymentStatusPending,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
}

func TestPaymentOrderRepository_创建与查询(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay20260101000000abcdef", 1000, 1000)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	if order.ID == 0 {
		t.Fatal("创建后应回填 ID")
	}

	got, err := repo.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("按订单号查询失败: %v", err)
	}
	if got.Amount != 1000 || got.Quota != 1000 {
		t.Fatalf("读回的数据不一致: %+v", got)
	}
	if got.Status != model.PaymentStatusPending {
		t.Fatalf("新订单应为待支付，实际 %v", got.Status)
	}
	if got.IsCredited() {
		t.Fatal("新订单不应处于已入账状态")
	}
}

func TestPaymentOrderRepository_查询不存在_应返回ErrOrderNotFound(t *testing.T) {
	repo, _, _ := newTestOrderRepo(t)

	_, err := repo.GetByTradeNo(context.Background(), "pay00000000000000000000")
	if !errors.Is(err, model.ErrPaymentOrderNotFound) {
		t.Fatalf("应返回 ErrPaymentOrderNotFound，实际 %v", err)
	}
}

func TestPaymentOrderRepository_CreditOrder_必须幂等(t *testing.T) {
	repo, users, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay20260101000001aaaaaa", 2000, 2000)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	if paid, err := repo.MarkPaid(ctx, order.TradeNo, "third-1", "{}", time.Now()); err != nil || !paid {
		t.Fatalf("标记支付应成功，paid=%v err=%v", paid, err)
	}

	credited, err := repo.CreditOrder(ctx, order.TradeNo, time.Now())
	if err != nil {
		t.Fatalf("首次入账失败: %v", err)
	}
	if !credited {
		t.Fatal("首次入账应返回 true")
	}

	// 第二次入账（模拟回调重复到达）必须返回 false 且额度不再增加
	credited, err = repo.CreditOrder(ctx, order.TradeNo, time.Now())
	if err != nil {
		t.Fatalf("重复入账不应报错: %v", err)
	}
	if credited {
		t.Fatal("重复入账必须返回 false（否则会重复给钱）")
	}

	user, err := users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if user.Quota != 2000 {
		t.Fatalf("额度应只被加一次（2000），实际 %d", user.Quota)
	}

	got, _ := repo.GetByTradeNo(ctx, order.TradeNo)
	if !got.IsCredited() {
		t.Fatal("订单应处于已入账状态")
	}
}

func TestPaymentOrderRepository_CreditOrder_未支付不入账(t *testing.T) {
	repo, users, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay20260101000002bbbbbb", 500, 500)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}

	credited, err := repo.CreditOrder(ctx, order.TradeNo, time.Now())
	if err != nil {
		t.Fatalf("未支付订单入账不应报错: %v", err)
	}
	if credited {
		t.Fatal("未支付订单不得入账")
	}

	user, _ := users.GetByID(ctx, userID)
	if user.Quota != 0 {
		t.Fatalf("未支付订单不应改变额度，实际 %d", user.Quota)
	}
}

func TestPaymentOrderRepository_CreditOrder_不限额度账户只标记(t *testing.T) {
	repo, users, userID := newTestOrderRepo(t)
	ctx := context.Background()

	// 把用户改成不限额度
	user, err := users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	user.Quota = model.QuotaUnlimited
	if err := users.Update(ctx, user); err != nil {
		t.Fatalf("更新用户失败: %v", err)
	}

	order := newPendingOrder(userID, "pay20260101000003cccccc", 100, 100)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	if _, err := repo.MarkPaid(ctx, order.TradeNo, "", "", time.Now()); err != nil {
		t.Fatalf("标记支付失败: %v", err)
	}
	if _, err := repo.CreditOrder(ctx, order.TradeNo, time.Now()); err != nil {
		t.Fatalf("入账失败: %v", err)
	}

	updated, _ := users.GetByID(ctx, userID)
	if updated.Quota != model.QuotaUnlimited {
		t.Fatalf("不限额度账户的额度不应被改成数字，实际 %d", updated.Quota)
	}
}

func TestPaymentOrderRepository_MarkPaid_只跃迁一次(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay20260101000004dddddd", 100, 100)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}

	first, err := repo.MarkPaid(ctx, order.TradeNo, "t-1", "第一次", time.Now())
	if err != nil || !first {
		t.Fatalf("首次标记支付应成功: paid=%v err=%v", first, err)
	}
	second, err := repo.MarkPaid(ctx, order.TradeNo, "t-2", "重复回调", time.Now())
	if err != nil {
		t.Fatalf("重复标记不应报错: %v", err)
	}
	if second {
		t.Fatal("重复标记必须返回 false（状态跃迁只允许发生一次）")
	}
}

// TestPaymentOrderRepository_MarkPaid_已关闭订单的迟到支付必须补记 是资损回归。
//
// 场景：订单到期被 CloseExpired 关成"已关闭"，用户随后才真正付款。
// 此时钱已经从用户账户扣走，若 MarkPaid 拒绝补记（旧实现只允许"待支付 → 已支付"），
// 回调链路会走到 CreditOrder 却因状态不是"已支付"而静默返回，最终应答 200
// 让支付平台不再重试——用户付了钱、额度永远不到账。
func TestPaymentOrderRepository_MarkPaid_已关闭订单的迟到支付必须补记(t *testing.T) {
	repo, userRepo, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay2026010100000elateee", 700, 7_000_000)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	// 订单过期后被自动关闭
	if err := repo.UpdateStatus(ctx, order.TradeNo, model.PaymentStatusClosed); err != nil {
		t.Fatalf("关闭订单失败: %v", err)
	}

	transitioned, err := repo.MarkPaid(ctx, order.TradeNo, "third-late", "迟到回调", time.Now())
	if err != nil {
		t.Fatalf("迟到支付补记不应报错: %v", err)
	}
	if !transitioned {
		t.Fatal("已关闭订单收到支付成功回调时必须补记为已支付，否则用户付钱拿不到额度")
	}

	credited, err := repo.CreditOrder(ctx, order.TradeNo, time.Now())
	if err != nil {
		t.Fatalf("入账失败: %v", err)
	}
	if !credited {
		t.Fatal("补记后必须能入账")
	}

	user, err := userRepo.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if user.Quota != 7_000_000 {
		t.Fatalf("额度应入账 7000000，实际 %d", user.Quota)
	}
}

// TestPaymentOrderRepository_MarkPaid_已退款订单不得被改回已支付 锁住反向资损。
//
// 已退款意味着钱已经退回用户；若允许它被改回"已支付"，会对同一笔钱再入账一次。
func TestPaymentOrderRepository_MarkPaid_已退款订单不得被改回已支付(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	order := newPendingOrder(userID, "pay2026010100000frefund", 500, 5000)
	if err := repo.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	// 历史退款：直接置为已退款（4）
	if err := repo.UpdateStatus(ctx, order.TradeNo, model.PaymentStatus(4)); err != nil {
		t.Fatalf("标记退款失败: %v", err)
	}

	transitioned, err := repo.MarkPaid(ctx, order.TradeNo, "third-x", "重复回调", time.Now())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if transitioned {
		t.Fatal("已退款订单不得被改回已支付（会把退回用户的钱再入账一次）")
	}

	got, err := repo.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if got.Status != model.PaymentStatus(4) {
		t.Fatalf("状态应保持已退款，实际 %v", got.Status)
	}
}

func TestPaymentOrderRepository_ListPaidUncredited_只返回已支付未入账(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	pending := newPendingOrder(userID, "pay20260101000005eeeeee", 100, 100)
	uncredited := newPendingOrder(userID, "pay20260101000006ffffff", 100, 100)
	done := newPendingOrder(userID, "pay20260101000007gggggg", 100, 100)

	for _, order := range []*model.PaymentOrder{pending, uncredited, done} {
		if err := repo.Create(ctx, order); err != nil {
			t.Fatalf("创建订单失败: %v", err)
		}
	}

	for _, tradeNo := range []string{uncredited.TradeNo, done.TradeNo} {
		if _, err := repo.MarkPaid(ctx, tradeNo, "", "", time.Now()); err != nil {
			t.Fatalf("标记支付失败: %v", err)
		}
	}
	if _, err := repo.CreditOrder(ctx, done.TradeNo, time.Now()); err != nil {
		t.Fatalf("入账失败: %v", err)
	}

	items, err := repo.ListPaidUncredited(ctx, 10)
	if err != nil {
		t.Fatalf("查询未入账订单失败: %v", err)
	}
	if len(items) != 1 || items[0].TradeNo != uncredited.TradeNo {
		t.Fatalf("应只返回 1 笔已支付未入账订单，实际 %+v", items)
	}
}

func TestPaymentOrderRepository_CloseExpired_只关闭待支付(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	expired := newPendingOrder(userID, "pay20260101000008hhhhhh", 100, 100)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	paid := newPendingOrder(userID, "pay20260101000009iiiiii", 100, 100)
	paid.ExpiresAt = time.Now().Add(-time.Minute)

	for _, order := range []*model.PaymentOrder{expired, paid} {
		if err := repo.Create(ctx, order); err != nil {
			t.Fatalf("创建订单失败: %v", err)
		}
	}
	if _, err := repo.MarkPaid(ctx, paid.TradeNo, "", "", time.Now()); err != nil {
		t.Fatalf("标记支付失败: %v", err)
	}

	closed, err := repo.CloseExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("关闭超时订单失败: %v", err)
	}
	if closed != 1 {
		t.Fatalf("应只关闭 1 笔超时订单，实际 %d", closed)
	}

	got, _ := repo.GetByTradeNo(ctx, expired.TradeNo)
	if got.Status != model.PaymentStatusClosed {
		t.Fatalf("超时订单应被关闭，实际 %v", got.Status)
	}
	gotPaid, _ := repo.GetByTradeNo(ctx, paid.TradeNo)
	if gotPaid.Status != model.PaymentStatusPaid {
		t.Fatalf("已支付订单不应被关闭，实际 %v", gotPaid.Status)
	}
}

func TestPaymentOrderRepository_SumPaidQuota(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	first := newPendingOrder(userID, "pay2026010100000ajjjjjj", 100, 150)
	second := newPendingOrder(userID, "pay2026010100000bkkkkkk", 100, 250)
	for _, order := range []*model.PaymentOrder{first, second} {
		if err := repo.Create(ctx, order); err != nil {
			t.Fatalf("创建订单失败: %v", err)
		}
		if _, err := repo.MarkPaid(ctx, order.TradeNo, "", "", time.Now()); err != nil {
			t.Fatalf("标记支付失败: %v", err)
		}
	}

	total, err := repo.SumPaidQuota(ctx, userID)
	if err != nil {
		t.Fatalf("统计充值额度失败: %v", err)
	}
	if total != 400 {
		t.Fatalf("已支付额度合计应为 400，实际 %d", total)
	}
}

// TestPaymentOrderRepository_SumPaidAmountCents 覆盖"累计充值金额"的统计口径。
//
// 这是分组解锁门槛（充值满 100 元解锁大客户分组）的判定依据，两条必须锁死：
//   - 只算【已支付】订单：待支付/已关闭订单不能算作用户充过钱；
//   - 算的是【实付金额 amount】而不是入账额度 quota：额度会随兑换比例变动，
//     且两者数值本就不同（见下一段注释），用错口径会让门槛时松时紧。
func TestPaymentOrderRepository_SumPaidAmountCents(t *testing.T) {
	repo, _, userID := newTestOrderRepo(t)
	ctx := context.Background()

	// 一笔 5000 分（50 元）已支付
	paid := newPendingOrder(userID, "pay2026010100000caaaaaa", 5000, 5_000_000)
	if err := repo.Create(ctx, paid); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	if _, err := repo.MarkPaid(ctx, paid.TradeNo, "", "", time.Now()); err != nil {
		t.Fatalf("标记支付失败: %v", err)
	}

	// 一笔 9900 分（99 元）仍待支付：落下它就能凑够 100 元，因此必须被排除在外
	pending := newPendingOrder(userID, "pay2026010100000dbbbbbb", 9900, 9_900_000)
	if err := repo.Create(ctx, pending); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}

	total, err := repo.SumPaidAmountCents(ctx, userID)
	if err != nil {
		t.Fatalf("统计累计充值金额失败: %v", err)
	}
	if total != 5000 {
		t.Fatalf("累计充值金额应为 5000 分（只算已支付），实际 %d", total)
	}

	// 额度口径与金额口径必然不同：这里 5,000,000 额度 vs 5,000 分。
	// 若哪天有人把门槛判定改成 SumPaidQuota，本断言会立刻暴露口径混用。
	quotaTotal, err := repo.SumPaidQuota(ctx, userID)
	if err != nil {
		t.Fatalf("统计充值额度失败: %v", err)
	}
	if quotaTotal == total {
		t.Fatalf("额度与金额口径不应相等（额度 %d，金额 %d 分）", quotaTotal, total)
	}

	// 无订单的用户应返回 0 而不是报错（新用户一进入分组下拉就会走这条路径）
	other, err := repo.SumPaidAmountCents(ctx, userID+100000)
	if err != nil {
		t.Fatalf("无订单用户不应报错: %v", err)
	}
	if other != 0 {
		t.Fatalf("无订单用户应为 0，实际 %d", other)
	}
}
