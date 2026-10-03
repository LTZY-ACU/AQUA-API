// 「财务记录」板块接口的 HTTP 层回归测试。
//
// 意图（Why）：
//
//	财务页的钱来自三个互不相干的写入路径（支付回调入账、邀请发奖、计费扣费），
//	任一处口径写错都会让用户看到"汇总与明细对不上"——
//	这类问题用户会直接怀疑站点在偷钱，因此必须用测试把口径钉死：
//	  1) 累计充值只算「已支付」订单；
//	  2) 累计返利来自邀请台账，与充值互不串味；
//	  3) 余额与已用直接取用户行，不在服务端做任何展示换算。
//	另外锁住返利明细的归属隔离（只能看到自己的）与用户名脱敏。
//
// 流转（Flow）：
//
//	造数据（注册用户 / 造已支付订单 / 发返利）→ GET /api/user/finance
//	→ GET /api/user/referral/rewards → 断言字段
//
// 扩展（Extend）：
//
//	新增财务维度时在 TestUserFinance_汇总四个口径 里补一条断言，
//	不要另起一个只断言单个字段的用例（口径分散会再次失去"一致性"保障）。
package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newFinanceTestSite 准备一个已登录的用户，并返回其会话令牌与用户 id。
func newFinanceTestSite(t *testing.T) (*Server, string, uint64) {
	t.Helper()

	srv, st := newReferralTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyRegistrationRequireEmailCode: "false",
		// 默认额度设 0：避免"不限额度"账户吞掉加额度操作，便于断言余额口径
		model.SettingKeyDefaultUserQuota: "0",
	})

	token, user := registerUser(t, srv, "finance-buyer", "")
	id, ok := user["id"].(float64)
	if !ok {
		t.Fatalf("注册响应缺少用户 id：%v", user)
	}
	return srv, token, uint64(id)
}

// TestUserFinance_汇总四个口径 校验余额/消费/充值/返利四个数字的来源与口径。
func TestUserFinance_汇总四个口径(t *testing.T) {
	srv, token, userID := newFinanceTestSite(t)
	ctx := context.Background()

	// 1) 余额 500、已用 120：直接取用户行，不做任何换算
	if _, err := srv.deps.Users.AddQuota(ctx, userID, 500); err != nil {
		t.Fatalf("加额度失败: %v", err)
	}
	if err := srv.deps.Users.AddUsedQuota(ctx, userID, 120); err != nil {
		t.Fatalf("加已用额度失败: %v", err)
	}

	// 2) 一笔已支付订单（1000 额度）：应计入累计充值
	order := &model.PaymentOrder{
		TradeNo:  "pay20260927190000finance",
		UserID:   userID,
		Amount:   1000,
		Currency: "CNY",
		Quota:    1000,
		Method:   model.PaymentMethodManual,
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建订单失败: %v", err)
	}
	if _, err := srv.deps.Orders.MarkPaid(ctx, order.TradeNo, "", "", time.Now()); err != nil {
		t.Fatalf("标记订单已支付失败: %v", err)
	}
	// 另造一笔"待支付"订单：绝不能被计入累计充值
	pending := &model.PaymentOrder{
		TradeNo:  "pay20260927190001pending",
		UserID:   userID,
		Amount:   9900,
		Currency: "CNY",
		Quota:    9900,
		Method:   model.PaymentMethodManual,
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, pending); err != nil {
		t.Fatalf("创建待支付订单失败: %v", err)
	}

	// 3) 一笔返利 30 额度（该用户是邀请人）
	_, invitee := registerUser(t, srv, "finance-friend", "")
	inviteeID := uint64(invitee["id"].(float64))
	if _, err := srv.deps.Referrals.GrantReward(ctx, &model.ReferralReward{
		InviterID:    userID,
		InviteeID:    inviteeID,
		Kind:         model.ReferralKindRecharge,
		Quota:        30,
		OrderTradeNo: order.TradeNo,
	}); err != nil {
		t.Fatalf("发放返利失败: %v", err)
	}

	rec, body := callJSON(t, srv, http.MethodGet, "/api/user/finance", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("财务汇总应返回 200，实际 %d，响应 %v", rec.Code, body)
	}

	expect := map[string]float64{
		// 余额 = 手动加的 500 + 返利到账的 30：发奖会同步加额度，
		// 因此这条断言同时验证了"返利确实进了账户"，而不只是写进了台账
		"balance_quota":   530,
		"used_quota":      120,  // 累计消费
		"recharged_quota": 1000, // 只算已支付的那一笔（不含 9900 的待支付）
		"recharge_count":  1,
		"reward_quota":    30, // 返利与充值互不串味
	}
	for key, want := range expect {
		if got := body[key]; got != want {
			t.Errorf("%s 应为 %v，实际 %v（响应 %v）", key, want, got, body)
		}
	}

	// 登出/未登录必须拒绝：财务数据不对外公开
	rec, _ = callJSON(t, srv, http.MethodGet, "/api/user/finance", nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录访问财务汇总应返回 401，实际 %d", rec.Code)
	}
}

// TestUserReferralRewards_归属隔离与脱敏 校验返利明细只看得到自己的、且用户名已脱敏。
func TestUserReferralRewards_归属隔离与脱敏(t *testing.T) {
	srv, token, inviterID := newFinanceTestSite(t)
	ctx := context.Background()

	_, invitee := registerUser(t, srv, "finance-friend", "")
	inviteeID := uint64(invitee["id"].(float64))

	// 同一被邀请人给两个邀请人各发一笔：只有本用户的应被返回
	for _, pair := range []struct {
		inviterID uint64
		quota     int64
	}{{inviterID, 30}, {inviteeID, 777}} {
		if _, err := srv.deps.Referrals.GrantReward(ctx, &model.ReferralReward{
			InviterID:    pair.inviterID,
			InviteeID:    inviteeID,
			Kind:         model.ReferralKindRecharge,
			Quota:        pair.quota,
			OrderTradeNo: "pay20260927191000shared",
		}); err != nil {
			t.Fatalf("发放返利失败: %v", err)
		}
	}

	rec, body := callJSON(t, srv, http.MethodGet, "/api/user/referral/rewards", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("返利明细应返回 200，实际 %d，响应 %v", rec.Code, body)
	}
	if total := body["total"]; total != float64(1) {
		t.Fatalf("只应看到自己的 1 条返利，实际 total=%v（响应 %v）", total, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("应返回 1 条明细，实际 %v", body["items"])
	}
	item, _ := items[0].(map[string]any)
	if item["quota"] != float64(30) {
		t.Errorf("返利额度应为 30（自己的那笔），实际 %v", item["quota"])
	}
	if item["kind_text"] != model.ReferralKindRecharge.String() {
		t.Errorf("应带出类型中文名，实际 %v", item["kind_text"])
	}
	if item["order_trade_no"] != "pay20260927191000shared" {
		t.Errorf("应带出关联订单号，实际 %v", item["order_trade_no"])
	}
	// 被邀请人用户名必须脱敏：这里是普通用户名 "finance-friend" → "fi***"
	if item["invitee"] != "fi***" {
		t.Errorf("被邀请人应脱敏为 fi***，实际 %v", item["invitee"])
	}
}

// TestMaskUsername_按形态脱敏 校验用户名脱敏的几种形态。
//
// 单独测这个纯函数的原因：站点允许邮箱作为用户名（真实数据里就有），
// 而"邮箱脱不脱敏"只能靠这个函数保证——从 HTTP 层造一个邮箱用户，
// 会受注册接口的用户名规则影响，测试会变得脆弱。
func TestMaskUsername_按形态脱敏(t *testing.T) {
	cases := map[string]string{
		"1302784641@qq.com": "13***@qq.com", // 邮箱：保留域名，去掉可定位到人的前缀
		"finance-friend":    "fi***",
		"ab":                "a***",
		"赵":                 "赵***",
		"":                  "",
		"  ":                "",
	}
	for input, want := range cases {
		if got := maskUsername(input); got != want {
			t.Errorf("maskUsername(%q) 应为 %q，实际 %q", input, want, got)
		}
	}
}
