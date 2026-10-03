// 本文件实现「易支付」协议通道。
//
// 意图（Why）：
//
//	易支付（又称"彩虹易支付"）是国内自建网关最常见的聚合支付协议，
//	一份实现可以对接大量支付服务商，覆盖支付宝/微信等主流方式。
//	它足够简单（MD5 签名 + 表单回调），不需要引入任何 SDK。
//
// 协议要点（来自公开的易支付接口约定）：
//
//	下单：把参数拼成查询串跳转到 {gateway}/submit.php
//	  pid / type / out_trade_no / notify_url / return_url / name / money / sign / sign_type=MD5
//	回调：支付平台以 GET 或 POST 表单回调 notify_url，参数同上，
//	  外加 trade_no（第三方单号）与 trade_status=TRADE_SUCCESS；
//	  验签通过后必须原样返回字符串 success，否则平台会持续重试。
//	签名：把所有非空参数（排除 sign、sign_type）按参数名升序拼接为
//	  "k1=v1&k2=v2"，末尾直接拼接商户密钥，取 MD5 十六进制小写。
//
// 安全说明：
//
//	回调必须验签且必须比对金额。仅比对订单号是不够的——
//	攻击者可以拿一个真实存在的小额订单号，伪造"已支付"通知。
//
// 流转（Flow）：
//
//	Create：组装参数 → 签名 → 返回带查询串的 submit.php 地址（浏览器跳转）
//	ParseNotify：读表单 → 验签 → 校验 trade_status → 返回订单号与金额
//
// 扩展（Extend）：
//
//	若上游要求 POST 提交而非跳转（少数实现），在 Create 中改为返回一个
//	自提交表单页面即可，ParseNotify 无需改动。
package payment

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// 易支付协议常量。
const (
	// epaySubmitPath 是收银台地址（跳转式支付）。
	epaySubmitPath = "/submit.php"
	// epaySignType 是本实现采用的签名算法。
	epaySignType = "MD5"
	// epayTradeSuccess 是"支付成功"的状态值。
	//
	// 兼容 TRADE_SUCCESS（支付宝体系）与 SUCCESS（部分实现）两种写法，
	// 否则换一家服务商就会出现"付了钱但系统不认"。
	epayTradeSuccess = "TRADE_SUCCESS"
	// epayAckBody 是回调成功时应答的固定字符串。
	epayAckBody = "success"
)

// epayProvider 实现易支付协议。
//
// 说明：下单是"拼 URL 让浏览器跳转"，本身不需要发起 HTTP 请求；
// 但回调阶段会向网关核实订单真实性（见 verifyWithGateway），
// 因此持有一个懒加载的 HTTP 客户端（并发安全，进程内复用连接）。
type epayProvider struct {
	opts Options

	once       sync.Once
	httpClient *http.Client
}

// newEPayProvider 构造易支付通道。
func newEPayProvider(opts Options) Provider {
	return &epayProvider{opts: opts}
}

// Name 返回通道名。
func (p *epayProvider) Name() string { return model.PaymentMethodEPay }

// settings 读取当前运营参数。
func (p *epayProvider) settings(ctx context.Context) (model.PaymentSettings, error) {
	if p.opts.Settings == nil {
		return model.PaymentSettings{}, nil
	}
	return p.opts.Settings(ctx)
}

// Create 组装收银台跳转地址。
func (p *epayProvider) Create(ctx context.Context, req *Request) (*CreateResult, error) {
	settings, err := p.settings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.MethodEnabled(model.PaymentMethodEPay) {
		return nil, ErrProviderDisabled
	}
	key := strings.TrimSpace(p.opts.Secrets.EPayKey)
	// 通道参数统一从设置表的 Params 读取（键 "epay.<字段>"）。
	// 走 Param 而不是直接读结构体字段的原因：新增支付通道时不必再改设置模型，
	// 且 Param 内部保留了"历史字段回退"，升级后旧的易支付配置仍然生效。
	gateway := settings.Param(model.PaymentMethodEPay, "gateway")
	pid := settings.Param(model.PaymentMethodEPay, "pid")
	if gateway == "" || pid == "" || key == "" {
		// 明确告诉使用者"缺什么"，而不是在跳转后由支付平台报一个含糊的错误
		return nil, fmt.Errorf("%w：易支付需要配置网关地址、商户号（后台）与商户密钥（环境变量 AQUA_EPAY_KEY）", ErrNotConfigured)
	}

	subMethod := strings.TrimSpace(req.Order.SubMethod)
	if subMethod == "" {
		subMethod = defaultSubMethod(settings.ParamList(model.PaymentMethodEPay, "types"))
	}

	params := map[string]string{
		"pid":          pid,
		"type":         subMethod,
		"out_trade_no": req.Order.TradeNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         req.Subject,
		// money 必须是"元"且两位小数
		"money":     yuanFromCents(req.Order.Amount),
		"sign_type": epaySignType,
	}
	if req.ClientIP != "" {
		params["clientip"] = req.ClientIP
	}
	params["sign"] = epaySign(params, key)

	query := url.Values{}
	for name, value := range params {
		if strings.TrimSpace(value) == "" {
			continue
		}
		query.Set(name, value)
	}

	return &CreateResult{
		PayURL: strings.TrimRight(gateway, "/") + epaySubmitPath + "?" + query.Encode(),
	}, nil
}

// defaultSubMethod 在未指定支付方式时取第一个可用类型。
func defaultSubMethod(types []string) string {
	for _, item := range types {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return "alipay"
}

// ParseNotify 验签并解析回调。
//
// 安全模型（本函数是全站唯一的"零鉴权 + 资损副作用"入口，务必逐条落实）：
//
//	易支付的签名算法是对「回调携带的全部参数」做 MD5，而本站下发收银台时
//	用【同一把商户密钥、同一套算法】给请求参数签了名。这两件事叠加会产生一个
//	危险后果：攻击者把自己订单 pay_url 里的参数原样 POST 回回调地址，
//	签名天然成立——这是一次典型的签名重放（CWE-294）。
//
//	因此本函数不再"验签通过即认账"，而是要求回调必须满足【通知独有的特征】：
//	  1) 必须显式携带 trade_status（收银台请求里没有这个字段），
//	     且取值属于"支付成功"集合——绝不把"缺失"默认成成功；
//	  2) 必须携带第三方单号 trade_no（收银台请求里也没有）；
//	  3) 商户号 pid 必须与本站配置一致（防止拿别家的签名来打）；
//	  4) 金额 money 必须存在且为正（防 0 元单与金额缺失）；
//	  5) 验签通过后再交由上层比对订单金额。
//
//	这样即便攻击者原样重放自己订单的全部请求参数，也会在第 1) 步被拒。
func (p *epayProvider) ParseNotify(ctx context.Context, notify *Notify) (*NotifyResult, error) {
	if notify == nil {
		return nil, ErrSignatureInvalid
	}
	key := strings.TrimSpace(p.opts.Secrets.EPayKey)
	if key == "" {
		// 没配密钥就无法验签，此时【必须】拒绝，绝不能"放行以便调试"
		return nil, fmt.Errorf("%w：未配置商户密钥（环境变量 AQUA_EPAY_KEY）", ErrNotConfigured)
	}

	settings, err := p.settings(ctx)
	if err != nil {
		// 读不到运营参数就无法核对商户号，按"配置不可用"拒绝处理：
		// 回调路径宁可拒绝，也不要在配置未知时凭签名放款。
		return nil, fmt.Errorf("%w：读取支付配置失败: %w", ErrNotConfigured, err)
	}
	configuredPID := strings.TrimSpace(settings.Param(model.PaymentMethodEPay, "pid"))

	params := collectEPayParams(notify)
	provided := strings.TrimSpace(params["sign"])
	if provided == "" {
		return nil, fmt.Errorf("%w：回调缺少 sign 参数", ErrSignatureInvalid)
	}

	// ── 1) 先做"这是不是一条通知"的结构校验，再验签 ──────────────
	// 顺序很重要：先用协议字段把请求参数与通知区分开，避免在"明显是重放"的
	// 输入上白做一次 MD5，更避免日后有人误把验签放宽成"通过即入账"。
	status := strings.ToUpper(strings.TrimSpace(params["trade_status"]))
	if status == "" {
		// 收银台请求参数里没有 trade_status。缺失即拒绝——这正是本次漏洞的根因。
		return nil, fmt.Errorf("%w：回调缺少 trade_status（可能是请求参数被重放）", ErrSignatureInvalid)
	}
	providerTradeNo := strings.TrimSpace(params["trade_no"])
	if providerTradeNo == "" {
		return nil, fmt.Errorf("%w：回调缺少第三方单号 trade_no", ErrSignatureInvalid)
	}
	if configuredPID == "" {
		return nil, fmt.Errorf("%w：后台未配置易支付商户号", ErrNotConfigured)
	}
	if pid := strings.TrimSpace(params["pid"]); pid != configuredPID {
		// 不回显两个值：避免把本站商户号泄露给探测者
		return nil, fmt.Errorf("%w：回调商户号与本站配置不一致", ErrSignatureInvalid)
	}

	// ── 2) 验签 ─────────────────────────────────────────────
	// 用 hmac.Equal 常量时间比较，而不是 strings.EqualFold：
	// 逐字节提前返回的比较会让攻击者通过响应时间差逐位试探签名。
	// 大小写仍然不敏感（部分实现回传大写十六进制），但先把两侧统一小写，
	// 避免 EqualFold 那种"大小写变体都算通过"带来的额外猜测空间。
	expected := epaySign(params, key)
	if !hmac.Equal([]byte(strings.ToLower(provided)), []byte(expected)) {
		return nil, ErrSignatureInvalid
	}

	tradeNo := strings.TrimSpace(params["out_trade_no"])
	if tradeNo == "" {
		return nil, fmt.Errorf("%w：回调缺少 out_trade_no", ErrSignatureInvalid)
	}

	// ── 3) 金额必须存在且为正 ─────────────────────────────────
	// 旧实现允许 money 缺失（解析为 0），而上层"金额>0 才比对"的写法
	// 会让缺失金额的回调绕过金额校验。这里从源头堵住。
	amountCents := yuanToCents(params["money"])
	if amountCents <= 0 {
		return nil, fmt.Errorf("%w：回调金额缺失或非正数", ErrAmountMismatch)
	}

	paid := status == epayTradeSuccess || status == "SUCCESS"
	if paid {
		// ── 4) 向上游核实订单真实性（纵深防御）────────────────
		// 到这里签名与语义都已成立，理论上已无伪造空间。但"拿一条真实通知
		// 去重放"仍可能发生在服务商侧数据被篡改的场景，因此再向支付网关
		// 查一次这笔订单的实际状态与金额。
		//
		// 失败策略（重要）：只有网关【明确回答"未支付/金额不符"】时才拒绝；
		// 查询本身因网络/接口不可用而失败时只记日志、放行——因为此时签名与语义
		// 校验已经通过（攻击者拿不到商户密钥），而误拒会让"真付了钱的用户"
		// 拿不到额度。两害相权，取其轻。
		if err := p.verifyWithGateway(ctx, tradeNo, amountCents); err != nil {
			if errors.Is(err, errGatewaySaysUnpaid) {
				return nil, fmt.Errorf("%w：上游网关确认该订单未支付", ErrSignatureInvalid)
			}
			slog.Warn("易支付回调：向上游核实订单失败，按已验证的签名继续处理",
				"trade_no", tradeNo, "err", err)
		}
	}

	return &NotifyResult{
		TradeNo:         tradeNo,
		ProviderTradeNo: providerTradeNo,
		AmountCents:     amountCents,
		Paid:            paid,
		AckBody:         epayAckBody,
		AckContentType:  "text/plain; charset=utf-8",
	}, nil
}

// errGatewaySaysUnpaid 表示"上游网关明确回答该订单没有支付成功"。
//
// 与"查询失败"严格区分：前者必须拒绝入账，后者只记日志放行（见 ParseNotify 的说明）。
var errGatewaySaysUnpaid = errors.New("payment: 上游网关确认订单未支付")

// verifyWithGateway 向易支付网关查询订单的真实状态与金额。
//
// 接口形态（易支付公开约定）：
//
//	GET {gateway}/api.php?act=order&pid={pid}&key={key}&out_trade_no={订单号}
//	返回 JSON：{"code":1,"data":{"trade_no":"...","money":"10.00","status":1}}
//	其中 status=1 表示已支付。
//
// 兼容性处理：不同服务商的字段名与嵌套层级略有差异（有的平铺在顶层、
// 有的用 data 包裹、有的用 trade_status 而非 status），解析时几种都认；
// 完全无法识别响应结构时按"查询失败"返回（放行），而不是武断判定为未支付——
// 否则换一家服务商就会导致"付了钱不到账"。
func (p *epayProvider) verifyWithGateway(ctx context.Context, tradeNo string, expectCents int64) error {
	result, err := p.QueryOrder(ctx, tradeNo)
	if err != nil {
		return err
	}
	if !result.Recognized {
		// 无法识别的响应结构：不武断判定，交给上层的"放行"分支
		return errors.New("payment: 上游响应无法识别，跳过核实")
	}
	if !result.Paid {
		return errGatewaySaysUnpaid
	}
	if result.AmountCents > 0 && result.AmountCents != expectCents {
		return errGatewaySaysUnpaid
	}
	return nil
}

// QueryOrder 主动向易支付网关查询订单状态（实现 Querier，供对账循环使用）。
//
// 与 verifyWithGateway 的分工：本方法只返回"网关怎么说"的原始结论
// （含金额与第三方单号，是否放行由调用方决定）；verifyWithGateway 则在
// 回调路径上把它翻译成"放行/拒绝"两种语义。
func (p *epayProvider) QueryOrder(ctx context.Context, tradeNo string) (*OrderQueryResult, error) {
	settings, err := p.settings(ctx)
	if err != nil {
		return nil, err
	}
	gateway := strings.TrimSpace(settings.Param(model.PaymentMethodEPay, "gateway"))
	if gateway == "" {
		return nil, errors.New("payment: 未配置网关地址，无法查单")
	}

	query := url.Values{}
	query.Set("act", "order")
	query.Set("pid", strings.TrimSpace(settings.Param(model.PaymentMethodEPay, "pid")))
	query.Set("key", strings.TrimSpace(p.opts.Secrets.EPayKey))
	query.Set("out_trade_no", strings.TrimSpace(tradeNo))

	// 超时与回调核实共用 5 秒：查单是兜底路径，不能拖垮主流程。
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(gateway, "/") + "/api.php?" + query.Encode()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("payment: 构造查单请求失败: %w", err)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("payment: 请求上游失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 只读 64KiB：这是对外请求，不能让异常大响应拖垮内存
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("payment: 读取上游响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("payment: 上游返回 HTTP %d", resp.StatusCode)
	}

	order, recognized := parseEPayOrderQuery(raw)
	return &OrderQueryResult{
		Paid:            order.Paid,
		AmountCents:     order.AmountCents,
		ProviderTradeNo: order.ProviderTradeNo,
		Recognized:      recognized,
	}, nil
}

// epayOrderQuery 是从上游查询接口里提取出的关键信息。
type epayOrderQuery struct {
	Paid            bool
	AmountCents     int64
	ProviderTradeNo string
}

// parseEPayOrderQuery 解析上游订单查询响应。
//
// 返回 ok=false 表示"结构无法识别"（调用方据此走放行分支）。
func parseEPayOrderQuery(raw []byte) (epayOrderQuery, bool) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return epayOrderQuery{}, false
	}
	// 结果可能被 data 包裹，也可能平铺在顶层
	target := payload
	if nested, ok := payload["data"].(map[string]any); ok {
		target = nested
	}

	recognized := false
	result := epayOrderQuery{}

	// 状态：status 或 trade_status 两种写法
	switch value := target["status"].(type) {
	case float64:
		recognized = true
		result.Paid = int(value) == 1
	case string:
		recognized = true
		trimmed := strings.ToUpper(strings.TrimSpace(value))
		result.Paid = trimmed == "1" || trimmed == epayTradeSuccess || trimmed == "SUCCESS"
	}
	if rawStatus, hasTradeStatus := target["trade_status"]; hasTradeStatus {
		recognized = true
		trimmed := strings.ToUpper(strings.TrimSpace(fmt.Sprint(rawStatus)))
		result.Paid = trimmed == epayTradeSuccess || trimmed == "SUCCESS"
	}

	// 金额：money 可能是字符串 "10.00" 或数字 10
	if rawMoney, ok := target["money"]; ok {
		recognized = true
		result.AmountCents = yuanToCents(fmt.Sprint(rawMoney))
	}

	// 第三方订单号：trade_no（对账与补账落库都需要它）
	if rawTradeNo, ok := target["trade_no"]; ok && rawTradeNo != nil {
		if trimmed, ok := rawTradeNo.(string); ok {
			result.ProviderTradeNo = strings.TrimSpace(trimmed)
		}
	}

	return result, recognized
}

// client 返回支付通道专用的 HTTP 客户端（懒加载，进程内复用连接）。
func (p *epayProvider) client() *http.Client {
	p.once.Do(func() { p.httpClient = newHTTPClient() })
	return p.httpClient
}

// collectEPayParams 合并 GET 查询与 POST 表单中的回调参数。
//
// 为什么两者都看：不同实现分别用 GET 与 POST 回调，
// 若只读一种，换服务商时会出现"回调收不到"的诡异现象。
func collectEPayParams(notify *Notify) map[string]string {
	params := make(map[string]string, 16)
	for name, values := range notify.Query {
		if len(values) > 0 {
			params[name] = values[0]
		}
	}
	for name, values := range notify.Form {
		if len(values) > 0 {
			params[name] = values[0]
		}
	}
	return params
}

// epaySign 计算易支付签名。
//
// 算法：过滤空值与 sign/sign_type → 按参数名 ASCII 升序 →
// 拼成 "k=v&k=v" → 末尾直接拼商户密钥 → MD5 小写十六进制。
func epaySign(params map[string]string, key string) string {
	names := make([]string, 0, len(params))
	for name := range params {
		if name == "sign" || name == "sign_type" {
			continue
		}
		if strings.TrimSpace(params[name]) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for index, name := range names {
		if index > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString(name)
		builder.WriteByte('=')
		builder.WriteString(params[name])
	}
	builder.WriteString(key)

	sum := md5.Sum([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// yuanToCents 把"元"字符串解析为"分"。
//
// 手工解析而不走 strconv.ParseFloat：浮点会把 10.01 解析成 10.009999…，
// 转成分为 1000 或 1001 取决于实现细节，属于典型的资损隐患。
func yuanToCents(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	negative := false
	if strings.HasPrefix(raw, "-") {
		negative = true
		raw = raw[1:]
	}

	// 按小数点切成整数部分与小数部分
	intPart, fracPart, _ := strings.Cut(raw, ".")
	// 小数部分补齐/截断到两位
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	fracPart = fracPart[:2]

	whole, err := strconv.ParseInt(strings.TrimSpace(intPart), 10, 64)
	if err != nil {
		return 0
	}
	frac, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return 0
	}

	cents := whole*100 + frac
	if negative {
		return -cents
	}
	return cents
}
