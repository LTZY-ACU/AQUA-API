// 聚合支付通道"子支付方式"（支付宝 / 微信支付）的 HTTP 层回归测试。
//
// 意图（Why）：
//
//	易支付只有一个通道名（epay），但用户在充值页上真正要选的是"支付宝还是微信"。
//	历史上 /api/payment/public 只下发通道名，前端因此只渲染出一行「在线支付」——
//	用户看不出能选微信，站长也以为"配了两个通道却只显示一个"。
//	这里把三条性质锁死：
//	  1) 聚合通道必须把已开通的子方式（含中文名）下发给前端；
//	  2) 下单时子方式要落库，订单记录才看得出"这一笔是用支付宝还是微信充的"；
//	  3) 未开通的子方式必须被拒绝——type 会原样拼进收银台地址，
//	     放任用户自填会变成"跳到收银台后平台报莫名错误"的难查故障。
//
// 流转（Flow）：
//
//	写支付设置（epay + types=alipay,wxpay）→ GET /api/payment/public 断言子方式
//	→ POST /api/user/orders（带/不带/传错 sub_method）→ 断言响应与落库结果
//
// 扩展（Extend）：
//
//	新增"有子方式"的通道时，在本文件补一条用例即可（大多数断言可复用）。
package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// epaySubMethodSettings 是"易支付已开通支付宝与微信"的一套支付设置。
const epaySubMethodParams = `{"epay.gateway":"https://pay.example.com","epay.pid":"1065","epay.types":"alipay,wxpay"}`

// setupEPaySubMethodSite 写好支付设置并注册一个可下单的用户。
func setupEPaySubMethodSite(t *testing.T) (*Server, string) {
	t.Helper()

	srv, st := newNotifyTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyRegistrationRequireEmailCode: "false",
		model.SettingKeyPaymentEnabled:               "true",
		model.SettingKeyPaymentMethods:               model.PaymentMethodEPay,
		model.SettingKeyPaymentExchangeRate:          "100",
		model.SettingKeyPaymentMinCents:              "100",
		model.SettingKeyPaymentMaxCents:              "0",
		model.SettingKeyPaymentParams:                epaySubMethodParams,
	})

	token, _ := registerUser(t, srv, "submethod-buyer", "")
	return srv, token
}

// TestPublicPaymentInfo_聚合通道下发子方式 校验充值页能拿到"支付宝/微信"两项。
//
// 这是本次缺陷的直接回归：前端只能渲染后端给的东西，
// 后端不下发子方式，用户就永远只看到一行「在线支付」。
func TestPublicPaymentInfo_聚合通道下发子方式(t *testing.T) {
	srv, _ := setupEPaySubMethodSite(t)

	rec, body := callJSON(t, srv, http.MethodGet, "/api/payment/public", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("公开支付信息应返回 200，实际 %d，响应 %v", rec.Code, body)
	}

	methods, ok := body["methods"].([]any)
	if !ok || len(methods) != 1 {
		t.Fatalf("应只下发一个已启用通道（epay），实际 %v", body["methods"])
	}
	method, _ := methods[0].(map[string]any)
	if method["name"] != model.PaymentMethodEPay {
		t.Fatalf("通道名应为 epay，实际 %v", method["name"])
	}

	subs, ok := method["sub_methods"].([]any)
	if !ok || len(subs) != 2 {
		t.Fatalf("epay 应下发 2 个子方式（alipay/wxpay），实际 %v", method["sub_methods"])
	}

	// 必须带中文名：让用户看到「微信支付」而不是 «wxpay»
	got := make(map[string]string, len(subs))
	for _, item := range subs {
		entry, _ := item.(map[string]any)
		name, _ := entry["name"].(string)
		label, _ := entry["label"].(string)
		got[name] = label
	}
	if got["alipay"] != "支付宝" {
		t.Errorf("alipay 的中文名应为「支付宝」，实际 %q", got["alipay"])
	}
	if got["wxpay"] != "微信支付" {
		t.Errorf("wxpay 的中文名应为「微信支付」，实际 %q", got["wxpay"])
	}
}

// TestCreateOrder_子方式落库与校验 覆盖"选了微信""没选""选了未开通"三种情况。
func TestCreateOrder_子方式落库与校验(t *testing.T) {
	srv, token := setupEPaySubMethodSite(t)

	// 1) 明确选择微信：落库为 wxpay，收银台地址里的 type 也必须是 wxpay
	rec, body := callJSON(t, srv, http.MethodPost, "/api/user/orders", map[string]any{
		"amount_cents": 100,
		"method":       model.PaymentMethodEPay,
		"sub_method":   "wxpay",
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("下单应成功，实际 %d，响应 %v", rec.Code, body)
	}
	if body["sub_method"] != "wxpay" {
		t.Errorf("订单应记录 sub_method=wxpay，实际 %v", body["sub_method"])
	}
	if body["sub_method_label"] != "微信支付" {
		t.Errorf("订单应给出中文名「微信支付」，实际 %v", body["sub_method_label"])
	}
	payURL, _ := body["pay_url"].(string)
	if !strings.Contains(payURL, "type=wxpay") {
		t.Errorf("收银台地址应带 type=wxpay，实际 %s", payURL)
	}
	if !strings.Contains(payURL, "/submit.php?") {
		t.Errorf("收银台地址应为易支付 submit.php，实际 %s", payURL)
	}

	// 2) 不传子方式：回退到第一个已开通项（alipay），
	//    这样订单记录里不会出现"用了什么都不知道"的空白
	rec, body = callJSON(t, srv, http.MethodPost, "/api/user/orders", map[string]any{
		"amount_cents": 100,
		"method":       model.PaymentMethodEPay,
	}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("不传子方式也应能下单，实际 %d，响应 %v", rec.Code, body)
	}
	if body["sub_method"] != "alipay" {
		t.Errorf("未指定子方式时应回退为 alipay，实际 %v", body["sub_method"])
	}

	// 3) 传一个未开通的方式（bank）：必须拒绝，不能把用户放进收银台去撞运气
	rec, body = callJSON(t, srv, http.MethodPost, "/api/user/orders", map[string]any{
		"amount_cents": 100,
		"method":       model.PaymentMethodEPay,
		"sub_method":   "bank",
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未开通的子方式应被拒绝（400），实际 %d，响应 %v", rec.Code, body)
	}
	if body["error"] == nil && body["code"] == nil {
		t.Logf("拒绝响应体：%v", body)
	}
}

// TestPublicPaymentInfo_无子方式通道下发空数组 校验 Stripe/人工确认不受影响。
//
// 为什么要单独断言"空数组"：前端按 sub_methods 是否为空决定"渲染一行还是多行"，
// 若这里下发 null，前端就得写一堆防御性判空。
func TestPublicPaymentInfo_无子方式通道下发空数组(t *testing.T) {
	srv, st := newNotifyTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyPaymentEnabled: "true",
		model.SettingKeyPaymentMethods: model.PaymentMethodManual,
	})

	rec, body := callJSON(t, srv, http.MethodGet, "/api/payment/public", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("公开支付信息应返回 200，实际 %d", rec.Code)
	}
	methods, _ := body["methods"].([]any)
	if len(methods) != 1 {
		t.Fatalf("应只下发人工确认通道，实际 %v", body["methods"])
	}
	method, _ := methods[0].(map[string]any)
	subs, ok := method["sub_methods"].([]any)
	if !ok {
		t.Fatalf("sub_methods 应为空数组而不是 null，实际 %v", method["sub_methods"])
	}
	if len(subs) != 0 {
		t.Errorf("人工确认通道不应有子方式，实际 %v", subs)
	}
}

// TestCreateOrder_最低充值可到1分 是"放开最低充值"（2026-10-01）的回归测试。
//
// 背景：站长要求取消"最低 1 元"限制，允许 0.01 元小额试充。
// 实现：默认 min_cents 从 100 改为 1（1 分），并新增"0 元订单直接拒绝"的兜底校验。
// 这里锁定三个边界：
//   - 0.01 元（1 分）必须能下单（不低于上限即可通过 min 校验）；
//   - 0 元必须被拒绝（即使后台把 min_cents 配成 0，也不能出现 0 元订单）；
//   - 负数金额必须被拒绝。
func TestCreateOrder_最低充值可到1分(t *testing.T) {
	srv, st := newNotifyTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyRegistrationRequireEmailCode: "false",
		model.SettingKeyPaymentEnabled:               "true",
		model.SettingKeyPaymentMethods:               model.PaymentMethodEPay,
		model.SettingKeyPaymentExchangeRate:          "100",
		model.SettingKeyPaymentMinCents:              "1", // 最少 1 分 = 0.01 元
		model.SettingKeyPaymentMaxCents:              "0",
		model.SettingKeyPaymentParams:                epaySubMethodParams,
	})

	token, _ := registerUser(t, srv, "cent-buyer", "")

	cases := []struct {
		name   string
		cents  int64
		wantOK bool
	}{
		{"一分钱可充", 1, true},
		{"一角钱可充", 10, true},
		{"一元钱可充", 100, true},
		{"零元被拒", 0, false},
		{"负数被拒", -5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := callJSON(t, srv, http.MethodPost, "/api/user/orders", map[string]any{
				"amount_cents": tc.cents,
				"method":       model.PaymentMethodEPay,
			}, token)
			if tc.wantOK && rec.Code != http.StatusOK {
				t.Fatalf("应能下单，实际 %d，响应 %v", rec.Code, body)
			}
			if !tc.wantOK && rec.Code != http.StatusBadRequest {
				t.Fatalf("应被拒绝（400），实际 %d，响应 %v", rec.Code, body)
			}
		})
	}
}
