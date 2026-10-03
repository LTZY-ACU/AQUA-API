// 本文件覆盖「邮件通道（SMTP）后台配置」接口与落库加密。
//
// 测试重点（对应本功能最容易出问题的四点）：
//  1. 响应【绝不回传口令】——只回 password_set；
//  2. 口令【留空即沿用】——编辑其他字段不会把已配置的通道改坏；
//  3. 优先级【后台启用优先、未启用回退环境变量】；
//  4. 口令【密文落库】——直接读库校验不存在明文。
package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/mailer"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// baseSMTPConfig 是测试用的"环境变量兜底配置"（非真实凭据）。
func baseSMTPConfig() config.SMTPConfig {
	return config.SMTPConfig{
		Host:     "smtp.base.example.com",
		Port:     465,
		Username: "base@example.com",
		From:     "base@example.com",
		FromName: "Base Sender",
		Password: "base-secret",
	}
}

// newSMTPFixture 在模型测试夹具之上补装 SMTP 仓储与发送器。
func newSMTPFixture(t *testing.T) *modelMetaFixture {
	t.Helper()
	fx := newModelMetaFixture(t)

	cipher, err := crypto.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	base := baseSMTPConfig()
	fx.srv.deps.SMTP = store.NewSMTPRepository(fx.srv.deps.Store.DB(), cipher)
	fx.srv.deps.SMTPBase = base
	fx.srv.deps.Mailer = mailer.New(base)
	return fx
}

// TestSMTPConfig_保存即生效且不回传口令 覆盖保存、热加载、口令留空沿用与回退。
func TestSMTPConfig_保存即生效且不回传口令(t *testing.T) {
	fx := newSMTPFixture(t)
	path := "/api/admin/smtp"

	// ── 1. 初始状态：未保存过 → 生效来源是环境变量 ──────────────
	rec, body := doBearerJSON(t, fx.srv, http.MethodGet, path, fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("读取邮件通道配置失败：%d %s", rec.Code, rec.Body.String())
	}
	if body["source"] != smtpSourceEnv {
		t.Fatalf("未保存过时应回退环境变量，实际 source=%v", body["source"])
	}
	if body["ready"] != true {
		t.Fatalf("环境变量已配齐，ready 应为 true，实际 %v", body["ready"])
	}
	if body["effective_host"] != "smtp.base.example.com" {
		t.Fatalf("生效地址应为环境变量里的地址，实际 %v", body["effective_host"])
	}
	// 关键修复点：后台没保存过配置时，表单字段必须回填环境变量的值，
	// 否则站长打开页面看到一片空白，会误以为"我配好的通道丢了"。
	if body["host"] != "smtp.base.example.com" {
		t.Fatalf("host 应回填环境变量的值，实际 %v", body["host"])
	}
	if body["from"] != "base@example.com" {
		t.Fatalf("发件地址应回填环境变量的值，实际 %v", body["from"])
	}
	if body["values_from_env"] != true {
		t.Fatalf("应标记这组值来自环境变量，实际 %v", body["values_from_env"])
	}
	if body["env_password_set"] != true {
		t.Fatalf("应告知环境变量里已有口令，实际 %v", body["env_password_set"])
	}
	if _, leaked := body["password"]; leaked {
		t.Fatal("响应中不得出现 password 字段")
	}

	// ── 2. 保存后台配置 → 立即生效（无需重启） ─────────────────
	payload := `{"host":"smtpdm.aliyun.com","port":465,"username":"owner@example.com",` +
		`"from":"owner@example.com","from_name":"LTZY 站点","enabled":true,"password":"s3cret-token"}`
	rec, body = doBearerJSON(t, fx.srv, http.MethodPut, path, fx.adminTok, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存邮件通道配置失败：%d %s", rec.Code, rec.Body.String())
	}
	if body["source"] != smtpSourceDatabase {
		t.Fatalf("保存并启用后 source 应为 database，实际 %v", body["source"])
	}
	if body["password_set"] != true {
		t.Fatalf("password_set 应为 true，实际 %v", body["password_set"])
	}
	if raw := rec.Body.String(); strings.Contains(raw, "s3cret-token") {
		t.Fatal("响应体中出现了明文口令")
	}
	// 热加载：发送器应当已经切到后台那一套配置
	if host, port, _, from, _ := fx.srv.deps.Mailer.Endpoint(); host != "smtpdm.aliyun.com" || port != 465 || from != "owner@example.com" {
		t.Fatalf("发送器未热加载到后台配置：host=%s port=%d from=%s", host, port, from)
	}

	// 口令密文落库（读原始列，确认不是明文）
	var storedCipher string
	if err := fx.srv.deps.Store.DB().
		QueryRowContext(context.Background(), "SELECT password_cipher FROM smtp_settings WHERE id = 1").
		Scan(&storedCipher); err != nil {
		t.Fatalf("读取口令密文失败: %v", err)
	}
	if storedCipher == "" || strings.Contains(storedCipher, "s3cret-token") {
		t.Fatalf("口令未加密落库，实际密文=%q", storedCipher)
	}

	// ── 3. 口令留空再保存 → 沿用原口令，通道不被改坏 ────────────
	payload = `{"host":"smtpdm.aliyun.com","port":587,"username":"owner@example.com",` +
		`"from":"owner@example.com","from_name":"LTZY 站点","enabled":true,"password":""}`
	rec, body = doBearerJSON(t, fx.srv, http.MethodPut, path, fx.adminTok, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("二次保存失败：%d %s", rec.Code, rec.Body.String())
	}
	if body["password_set"] != true {
		t.Fatal("口令留空保存后，password_set 仍应为 true（应沿用已保存的口令）")
	}
	if body["ready"] != true {
		t.Fatalf("留空保存口令后通道仍应就绪，实际 ready=%v", body["ready"])
	}
	if _, port, _, _, _ := fx.srv.deps.Mailer.Endpoint(); port != 587 {
		t.Fatalf("端口未更新为 587，实际 %d", port)
	}

	// ── 4. 关闭启用 → 回退环境变量 ──────────────────────────────
	payload = `{"host":"smtpdm.aliyun.com","port":587,"username":"owner@example.com",` +
		`"from":"owner@example.com","from_name":"LTZY 站点","enabled":false,"password":""}`
	rec, body = doBearerJSON(t, fx.srv, http.MethodPut, path, fx.adminTok, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("关闭启用失败：%d %s", rec.Code, rec.Body.String())
	}
	if body["source"] != smtpSourceEnv {
		t.Fatalf("关闭启用后应回退环境变量，实际 %v", body["source"])
	}
	if body["effective_host"] != "smtp.base.example.com" {
		t.Fatalf("回退后的生效地址应为环境变量地址，实际 %v", body["effective_host"])
	}
}

// TestSMTPConfig_启用时必填校验 覆盖启用状态下缺项被拒（避免"配了却不能发"）。
func TestSMTPConfig_启用时必填校验(t *testing.T) {
	fx := newSMTPFixture(t)
	path := "/api/admin/smtp"

	cases := []struct {
		name    string
		payload string
	}{
		{"缺服务器地址", `{"host":"","port":465,"username":"a@b.com","from":"a@b.com","enabled":true,"password":"x"}`},
		{"缺登录账号", `{"host":"smtp.example.com","port":465,"username":"","from":"a@b.com","enabled":true,"password":"x"}`},
		{"缺发件地址", `{"host":"smtp.example.com","port":465,"username":"a@b.com","from":"","enabled":true,"password":"x"}`},
		{"发件地址格式错误", `{"host":"smtp.example.com","port":465,"username":"a@b.com","from":"not-an-email","enabled":true,"password":"x"}`},
		{"端口越界", `{"host":"smtp.example.com","port":70000,"username":"a@b.com","from":"a@b.com","enabled":true,"password":"x"}`},
	}
	for _, tc := range cases {
		rec, body := doBearerJSON(t, fx.srv, http.MethodPut, path, fx.adminTok, tc.payload)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s：应返回 400，实际 %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if code := redeemErrorCode(body); code != "invalid_smtp_settings" {
			t.Fatalf("%s：错误码应为 invalid_smtp_settings，实际 %v", tc.name, code)
		}
	}

	// 未启用时允许留空（站长可以先粘参数再逐项核对）
	rec, _ := doBearerJSON(t, fx.srv, http.MethodPut, path, fx.adminTok,
		`{"host":"","port":465,"username":"","from":"","enabled":false,"password":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("未启用时留空应被接受，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestSMTPConfig_改用后台配置时口令必填 覆盖"环境变量有口令但不可复用"的提示。
//
// 场景：站长看到回填的环境变量参数后直接勾选启用并保存（口令框留空）。
// 此时必须给出明确指引，而不是含糊的"口令不能为空"——
// 否则他会疑惑"环境变量里明明有口令"。
func TestSMTPConfig_改用后台配置时口令必填(t *testing.T) {
	fx := newSMTPFixture(t)
	// 未保存过任何后台配置，只有环境变量兜底（口令在环境变量里）
	rec, body := doBearerJSON(t, fx.srv, http.MethodPut, "/api/admin/smtp", fx.adminTok,
		`{"host":"smtp.base.example.com","port":465,"username":"base@example.com",`+
			`"from":"base@example.com","from_name":"Base","enabled":true,"password":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未填口令且要启用，应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := redeemErrorCode(body); code != "smtp_password_required" {
		t.Fatalf("错误码应为 smtp_password_required，实际 %v", code)
	}
	if msg := redeemErrorMessage(body); !strings.Contains(msg, "环境变量") {
		t.Fatalf("错误信息应说明环境变量口令不可复用，实际 %q", msg)
	}
}

// TestSMTPConfig_测试发信未配置时明确报错 覆盖"未配置就点测试"的路径。
//
// 这里不真的连 SMTP 服务器（测试环境不应依赖外网）：
// 只验证"未配置"时返回可操作的 400，而不是抛一个底层网络错误。
func TestSMTPConfig_测试发信未配置时明确报错(t *testing.T) {
	fx := newSMTPFixture(t)
	// 把兜底配置清空，模拟"什么都没配"的站点
	fx.srv.deps.SMTPBase = config.SMTPConfig{}
	fx.srv.deps.Mailer = mailer.New(config.SMTPConfig{})

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/smtp/test",
		fx.adminTok, `{"to":"someone@example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未配置时应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := redeemErrorCode(body); code != "smtp_not_configured" {
		t.Fatalf("错误码应为 smtp_not_configured，实际 %v", code)
	}

	rec, body = doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/smtp/test",
		fx.adminTok, `{"to":"not-an-email"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("收件地址非法应返回 400，实际 %d", rec.Code)
	}
	if code := redeemErrorCode(body); code != "invalid_recipient" {
		t.Fatalf("错误码应为 invalid_recipient，实际 %v", code)
	}
}
