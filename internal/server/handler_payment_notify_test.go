// 支付回调（/api/payments/{method}/notify）的 HTTP 层回归测试。
//
// 意图（Why）：
//
//	回调是全站唯一"无需鉴权却会产生资损副作用"的接口，它的两条性质必须被锁死：
//	  1) POST 表单形式的回调（支付宝异步通知、部分易支付实现）必须能被验签通过——
//	     历史上这里用 io.ReadAll 读完 body 后再调 c.Request.ParseForm()，
//	     而 body 已被读空，导致 PostForm 恒为空、验签因"缺少 sign"必然失败，
//	     线上表现为「用户付了钱、订单永远停在待支付」；
//	  2) 同一笔回调重复投递（支付平台会重试）只能入账一次。
//
// 流转（Flow）：
//
//	构造已签名表单 → POST /api/payments/epay/notify → 断言应答、订单状态与用户额度
//
// 扩展（Extend）：
//
//	新增通道时，按同样方式补一条"该通道的回调格式能被解析并验签"的用例；
//	表单/JSON/查询串三种回调形态至少各覆盖一条。
package server

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/payment"
	"github.com/LTZY-ACU/aqua-api/internal/relay"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// notifyTestEPayKey 是测试用易支付商户密钥（非真实密钥）。
const notifyTestEPayKey = "test-epay-key-0123456789"

// TestScrubNotifyPayload 锁住"回调原文落库前去掉签名字段"的行为（安全加固 1.5）。
//
// 为什么必须有这条测试：脱敏写错有两种相反的坏结果——
//   - 删多了（把订单号/金额也删掉）：出账纠纷时拿不出证据；
//   - 删少了（sign 仍在库里）：等于把可做重放实验的材料长期留存。
//
// 两种都不会报错，只能靠断言把口径钉住。
func TestScrubNotifyPayload(t *testing.T) {
	// 表单回调：只去掉签名字段，其余一字不动
	form := "pid=1001&out_trade_no=pay20260928abc&trade_no=EP1&money=1.00&trade_status=TRADE_SUCCESS&sign=deadbeef&sign_type=MD5"
	got := scrubNotifyPayload([]byte(form))
	for _, keep := range []string{"pid=1001", "out_trade_no=pay20260928abc", "trade_no=EP1", "money=1.00", "trade_status=TRADE_SUCCESS"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("对账所需的 %q 不应被删掉，实际：%s", keep, got)
		}
	}
	for _, drop := range []string{"sign=", "sign_type="} {
		if strings.Contains(got, drop) {
			t.Fatalf("签名字段 %q 不应留在落库原文里，实际：%s", drop, got)
		}
	}

	// 大小写不敏感：上游可能写成 Sign / SIGNATURE
	if got := scrubNotifyPayload([]byte("a=1&Sign=x&SIGNATURE=y&b=2")); strings.Contains(strings.ToLower(got), "sign") {
		t.Fatalf("签名字段匹配应大小写不敏感，实际：%s", got)
	}

	// JSON 回调（签名在请求头里）：必须原样保留，不能被表单解析拆坏
	jsonBody := `{"id":"evt_1","amount":100,"status":"paid"}`
	if got := scrubNotifyPayload([]byte(jsonBody)); got != jsonBody {
		t.Fatalf("JSON 回调应原样保留，实际：%s", got)
	}

	// 空与空白：返回空串而不是拼接出垃圾
	if got := scrubNotifyPayload(nil); got != "" {
		t.Fatalf("空 body 应返回空串，实际：%q", got)
	}
}

// newNotifyTestServer 装配一个含支付注册表与订单仓储的测试服务。
func newNotifyTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	gin.DefaultWriter = io.Discard

	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "notify_test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	cipher, err := crypto.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	channels := store.NewChannelRepository(st.DB(), cipher)
	settings := store.NewSettingRepository(st.DB(), st.Dialect())

	cfg := config.Default()
	cfg.Server.Mode = "test"
	cfg.Server.Listen = "127.0.0.1:0"
	// 密钥只走环境变量注入的路径（与生产一致），这里用测试值代替
	cfg.Payment.EPayKey = notifyTestEPayKey

	// 写入易支付的运营参数（回调验签要核对商户号）。
	// gateway 故意指向不可达地址：让"向网关核实订单"这一步走"查询失败→放行"
	// 分支（签名与语义校验已在单测里独立覆盖），避免测试对真实网络产生依赖。
	notifyTestEPaySetup(t, settings)

	srv := New(Deps{
		Config:    cfg,
		Store:     st,
		Channels:  channels,
		Tokens:    store.NewTokenRepository(st.DB(), cipher),
		Users:     store.NewUserRepository(st.DB()),
		Sessions:  store.NewSessionRepository(st.DB()),
		Settings:  settings,
		Orders:    store.NewPaymentOrderRepository(st.DB()),
		Referrals: store.NewReferralRepository(st.DB()),
		Relay:     relay.New(channels, relay.Options{}),
		Payment: payment.NewRegistry(payment.Options{
			Secrets: cfg.Payment,
			Settings: func(ctx context.Context) (model.PaymentSettings, error) {
				loaded, err := model.LoadSiteSettings(ctx, settings)
				if err != nil {
					return model.PaymentSettings{}, err
				}
				return loaded.Payment, nil
			},
		}),
	})
	return srv, st
}

// notifyTestEPayPID 是测试环境的易支付商户号（回调里必须携带且与本配置一致）。
const notifyTestEPayPID = "1001"

// notifyTestEPaySetup 写入易支付通道的运营参数。
func notifyTestEPaySetup(t *testing.T, settings model.SettingRepository) {
	t.Helper()
	// Params 走单个 JSON 键 payment_params（见 model.SettingKeyPaymentParams 的说明）。
	// gateway 故意指向不可达地址：让"向网关核实订单"这一步走"查询失败→放行"
	// 分支（签名与语义校验由各用例独立覆盖），避免测试对真实网络产生依赖。
	if err := settings.SetMany(context.Background(), map[string]string{
		model.SettingKeyPaymentParams: `{"epay.pid":"` + notifyTestEPayPID + `","epay.gateway":"http://127.0.0.1:1","epay.types":"alipay"}`,
	}); err != nil {
		t.Fatalf("写入支付测试设置失败: %v", err)
	}
}

// epayTestSign 复算易支付签名，用于在测试里构造"合法回调"。
//
// 与 internal/payment/epay.go 的 epaySign 保持一致：过滤空值与 sign/sign_type →
// 按参数名升序拼 "k=v&k=v" → 末尾拼商户密钥 → MD5 小写十六进制。
func epayTestSign(params map[string]string, key string) string {
	names := make([]string, 0, len(params))
	for name, value := range params {
		if name == "sign" || name == "sign_type" {
			continue
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+params[name])
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

// TestPaymentNotify_表单回调_验签通过并幂等入账 覆盖"表单能解析 + 只入账一次"。
func TestPaymentNotify_表单回调_验签通过并幂等入账(t *testing.T) {
	srv, _ := newNotifyTestServer(t)
	ctx := context.Background()

	buyer := &model.User{
		Username:     "notify-buyer",
		PasswordHash: "test-hash",
		Email:        "buyer@example.com",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
	if err := srv.deps.Users.Create(ctx, buyer); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	const orderQuota = int64(10000)
	order := &model.PaymentOrder{
		TradeNo:  "pay20260101000001abcdef",
		UserID:   buyer.ID,
		Amount:   1000, // 10.00 元
		Currency: "CNY",
		Quota:    orderQuota,
		Method:   "epay",
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建测试订单失败: %v", err)
	}

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": order.TradeNo,
		"trade_no":     "T2026010100001",
		"name":         "充值",
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = epayTestSign(params, notifyTestEPayKey)
	params["sign_type"] = "MD5"

	form := url.Values{}
	for name, value := range params {
		form.Set(name, value)
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/payments/epay/notify",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 第一次回调：必须验签通过（若表单未被解析，这里会因缺少 sign 返回 400）
	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("表单回调状态码 = %d，期望 200（响应体: %s）", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "success" {
		t.Errorf("回调应答 = %q，期望 %q（易支付要求原样返回 success）", got, "success")
	}

	paid, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if paid.Status != model.PaymentStatusPaid {
		t.Errorf("订单状态 = %v，期望已支付", paid.Status)
	}
	if !paid.IsCredited() {
		t.Error("订单未被标记入账")
	}

	afterFirst, err := srv.deps.Users.GetByID(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if afterFirst.Quota != orderQuota {
		t.Fatalf("首次入账后额度 = %d，期望 %d", afterFirst.Quota, orderQuota)
	}

	// 第二次回调（支付平台重试）：必须幂等，额度不再增加
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("重复回调状态码 = %d，期望 200", rec.Code)
	}
	afterSecond, err := srv.deps.Users.GetByID(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if afterSecond.Quota != orderQuota {
		t.Errorf("重复回调后额度 = %d，期望仍为 %d（重复入账）", afterSecond.Quota, orderQuota)
	}
}

// TestPaymentNotify_签名错误_拒绝且不入账 覆盖"验签失败即拒绝"。
//
// 用错误密钥签出的回调必须被拒，且不得改变订单状态与用户额度。
func TestPaymentNotify_签名错误_拒绝且不入账(t *testing.T) {
	srv, _ := newNotifyTestServer(t)
	ctx := context.Background()

	buyer := &model.User{
		Username:     "notify-buyer-2",
		PasswordHash: "test-hash",
		Email:        "buyer2@example.com",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
	if err := srv.deps.Users.Create(ctx, buyer); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	order := &model.PaymentOrder{
		TradeNo:  "pay20260101000002abcdef",
		UserID:   buyer.ID,
		Amount:   1000,
		Currency: "CNY",
		Quota:    10000,
		Method:   "epay",
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建测试订单失败: %v", err)
	}

	params := map[string]string{
		"pid":          "1001",
		"out_trade_no": order.TradeNo,
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
	}
	// 刻意用错误密钥签名
	params["sign"] = epayTestSign(params, "wrong-key")

	form := url.Values{}
	for name, value := range params {
		form.Set(name, value)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/payments/epay/notify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("错误签名状态码 = %d，期望 400", rec.Code)
	}

	stored, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if stored.Status != model.PaymentStatusPending {
		t.Errorf("订单状态被错误签名改变为 %v", stored.Status)
	}
	after, err := srv.deps.Users.GetByID(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if after.Quota != 0 {
		t.Errorf("错误签名回调后额度 = %d，期望 0", after.Quota)
	}
}

// TestPaymentNotify_重放下单签名_拒绝且不入账 锁死 AQUA-SEC-2026-0928-01 的根因。
//
// 攻击链：下单响应的 pay_url 携带服务端用【同一把商户密钥】签名的请求参数，
// 把这套参数（不含 trade_status / trade_no）原样 POST 到回调地址。
// 旧实现"验签通过即认账 + trade_status 缺失默认成功"，导致零付款任意入账。
//
// 修复后该请求必须在验签之前就被"通知结构校验"拦下：
//   - 缺 trade_status → 拒绝（本用例的场景）；
//   - 即使补了字段，参数集变了签名也会随之失配（攻击者无密钥，无法重签）。
func TestPaymentNotify_重放下单签名_拒绝且不入账(t *testing.T) {
	srv, _ := newNotifyTestServer(t)
	ctx := context.Background()

	buyer := &model.User{
		Username:     "notify-replay-attacker",
		PasswordHash: "test-hash",
		Email:        "replay@example.com",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
	if err := srv.deps.Users.Create(ctx, buyer); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	order := &model.PaymentOrder{
		TradeNo:  "pay20260928145812b35a8a",
		UserID:   buyer.ID,
		Amount:   50000, // 500.00 元（复现报告里的实测金额）
		Currency: "CNY",
		Quota:    500000000,
		Method:   "epay",
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建测试订单失败: %v", err)
	}

	// 完整复刻「下单响应 pay_url 里的参数」：服务端签过名、但属于请求而非通知。
	// 签名用【真实密钥】计算——这正是漏洞报告里"签名天然成立"的原因。
	replay := map[string]string{
		"pid":          notifyTestEPayPID,
		"type":         "alipay",
		"out_trade_no": order.TradeNo,
		"notify_url":   "https://example.com/api/payments/epay/notify",
		"return_url":   "https://example.com/console/recharge?trade_no=" + order.TradeNo,
		"name":         "充值 500.00 CNY（测试）",
		"money":        "500.00",
		"sign_type":    "MD5",
	}
	replay["sign"] = epayTestSign(replay, notifyTestEPayKey)

	form := url.Values{}
	for name, value := range replay {
		form.Set(name, value)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/payments/epay/notify",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重放下单签名状态码 = %d，期望 400（漏洞回归！）响应体: %s", rec.Code, rec.Body.String())
	}

	stored, err := srv.deps.Orders.GetByTradeNo(ctx, order.TradeNo)
	if err != nil {
		t.Fatalf("读取订单失败: %v", err)
	}
	if stored.Status != model.PaymentStatusPending {
		t.Errorf("订单状态被重放攻击改变为 %v（漏洞回归！）", stored.Status)
	}
	if stored.IsCredited() {
		t.Error("重放攻击居然入账了（漏洞回归！）")
	}
	after, err := srv.deps.Users.GetByID(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if after.Quota != 0 {
		t.Errorf("重放攻击后额度 = %d，期望 0（漏洞回归！）", after.Quota)
	}
}

// TestPaymentNotify_伪造通知结构仍需有效签名 验证"补全字段"绕不过验签。
//
// 攻击者若给重放参数补上 trade_status / trade_no，参数集改变会使原签名失配；
// 没有商户密钥就无法重新签名。本用例锁死这一点。
func TestPaymentNotify_伪造通知结构仍需有效签名(t *testing.T) {
	srv, _ := newNotifyTestServer(t)
	ctx := context.Background()

	buyer := &model.User{
		Username:     "notify-replay-forged",
		PasswordHash: "test-hash",
		Email:        "forged@example.com",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
	if err := srv.deps.Users.Create(ctx, buyer); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	order := &model.PaymentOrder{
		TradeNo:  "pay20260928150000forged01",
		UserID:   buyer.ID,
		Amount:   100,
		Currency: "CNY",
		Quota:    10000,
		Method:   "epay",
		Status:   model.PaymentStatusPending,
	}
	if err := srv.deps.Orders.Create(ctx, order); err != nil {
		t.Fatalf("创建测试订单失败: %v", err)
	}

	// 在重放参数基础上补上通知独有字段——签名（用旧参数集算的）必然失配
	forged := map[string]string{
		"pid":          notifyTestEPayPID,
		"type":         "alipay",
		"out_trade_no": order.TradeNo,
		"notify_url":   "https://example.com/api/payments/epay/notify",
		"return_url":   "https://example.com/console/recharge",
		"name":         "充值",
		"money":        "1.00",
		"trade_status": "TRADE_SUCCESS",
		"trade_no":     "T-FORGED-1",
		"sign_type":    "MD5",
	}
	forged["sign"] = epayTestSign(map[string]string{
		"pid":          notifyTestEPayPID,
		"type":         "alipay",
		"out_trade_no": order.TradeNo,
		"notify_url":   "https://example.com/api/payments/epay/notify",
		"return_url":   "https://example.com/console/recharge",
		"name":         "充值",
		"money":        "1.00",
	}, notifyTestEPayKey) // 用"少了两个字段"的参数集签名：模拟补字段后的旧签名

	form := url.Values{}
	for name, value := range forged {
		form.Set(name, value)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/payments/epay/notify",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("补字段的伪造回调状态码 = %d，期望 400（签名失配）", rec.Code)
	}
	after, err := srv.deps.Users.GetByID(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if after.Quota != 0 {
		t.Errorf("伪造回调后额度 = %d，期望 0", after.Quota)
	}
}
