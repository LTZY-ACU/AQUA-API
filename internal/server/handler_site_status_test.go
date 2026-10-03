// GET /api/status 中"额度 ↔ 人民币 折算比例"的回归测试。
//
// 意图（Why）：
//
//	用户门户要把余额、消费、模型单价一律折算成人民币展示（额度是站内计费单位，
//	用户充了 1 元却看到「100」会被当成算错）。折算比例由本接口的 quota_per_yuan 下发，
//	前端不允许硬编码 —— 站长随时可以把兑换比例从 100 改成 97。
//	因此这里锁死两条性质：
//	  1) 下发的值必须等于后台的兑换比例设置（改设置后立即生效）；
//	  2) 未配置时下发默认比例，而不是 0（0 会让前端退回显示原始额度）。
//
// 流转（Flow）：
//
//	写设置 → GET /api/status → 断言 quota_per_yuan
//
// 扩展（Extend）：
//
//	若将来支持多币种，本用例应扩展为"按币种断言各自的折算比例"。
package server

import (
	"net/http"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// TestSiteStatus_下发额度折算比例 校验前端折算所需的 quota_per_yuan。
func TestSiteStatus_下发额度折算比例(t *testing.T) {
	srv, st := newNotifyTestServer(t)

	// 默认（未显式配置）时下发默认比例：站点设置的内置默认是 1 元 = 100 额度
	rec, body := callJSON(t, srv, http.MethodGet, "/api/status", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("站点信息应返回 200，实际 %d，响应 %v", rec.Code, body)
	}
	if got := body["quota_per_yuan"]; got != float64(model.DefaultSiteSettings().Payment.ExchangeRate) {
		t.Fatalf("默认折算比例应为 %d，实际 %v",
			model.DefaultSiteSettings().Payment.ExchangeRate, got)
	}

	// 站长改成 97（用户承担 3% 手续费的口径）后必须立即生效：
	// 否则前端会按旧比例算出"到账金额"，与真实入账对不上。
	applySettings(t, st, map[string]string{model.SettingKeyPaymentExchangeRate: "97"})
	rec, body = callJSON(t, srv, http.MethodGet, "/api/status", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("站点信息应返回 200，实际 %d", rec.Code)
	}
	if got := body["quota_per_yuan"]; got != float64(97) {
		t.Fatalf("折算比例应随设置变为 97，实际 %v", got)
	}
}
