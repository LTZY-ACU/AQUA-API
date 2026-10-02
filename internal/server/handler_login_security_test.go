// 登录安全的 HTTP 层回归测试（账号级连续失败锁定）。
//
// 意图（Why）：
//
//	迁移 0043 与 store 层只保证"失败计数和锁定写得对"，真正决定安全效果的是
//	**判定发生的时机与顺序**——本文件把三条不变量钉死：
//
//	  1. 连续失败达到阈值后，再用错误口令必须得到 429（而不是继续给 401 让攻击者慢慢试）；
//	  2. 锁定期间用【正确】口令必须仍能登录成功 —— 这是"防锁定 amplification"的核心：
//	     若这条被改坏，攻击者只要知道用户名就能把受害者永久关在门外；
//	  3. 登录成功后失败计数必须清零并且来源被记录 —— 否则用户每成功一次，
//	     距离下一次误锁就少一次机会。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run LoginSecurity
//
// 扩展（Extend）：
//
//	修改 model.LoginFailureLockThreshold 时无需改本文件（用例从常量推导次数），
//	但新增"别的登录途经"时，务必为它补一条"失败要计数"的用例——
//	漏计的入口等于把整条防线绕过去。
package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/crypto"
	"gitee.com/xiaosu4610/aqua-api/internal/model"
	"gitee.com/xiaosu4610/aqua-api/internal/store"
)

// loginSecurityFixture 是登录安全用例的公共固件。
type loginSecurityFixture struct {
	srv   *Server
	st    *store.Store
	users model.UserRepository
	user  *model.User
}

// newLoginSecurityFixture 建一个启用状态的用户，并返回可直接发起请求的固件。
func newLoginSecurityFixture(t *testing.T) *loginSecurityFixture {
	t.Helper()

	srv, st := newTestServer(t)
	users := store.NewUserRepository(st.DB())
	ctx := context.Background()

	hash, err := crypto.HashPassword(correctPwd)
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	u := &model.User{
		Username:     "lock-target",
		PasswordHash: hash,
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
	}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return &loginSecurityFixture{srv: srv, st: st, users: users, user: u}
}

const (
	correctPwd = "LockTarget-Pass-2026"
	wrongPwd   = "definitely-not-the-password"
)

// loginOnce 用给定口令登录一次，返回状态码与响应体。
func (fx *loginSecurityFixture) loginOnce(t *testing.T, password string) (int, map[string]any) {
	t.Helper()
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"lock-target","password":"`+password+`"}`)
	return rec.Code, body
}

// reload 重新读取用户行，用于观察计数与来源是否落库。
func (fx *loginSecurityFixture) reload(t *testing.T) *model.User {
	t.Helper()
	u, err := fx.users.GetByID(context.Background(), fx.user.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	return u
}

// TestLoginSecurity_连续失败达到阈值后锁定 覆盖不变量 1。
func TestLoginSecurity_连续失败达到阈值后锁定(t *testing.T) {
	fx := newLoginSecurityFixture(t)

	// 达到阈值之前：每次都是 401，计数器如实递增。
	for i := 1; i <= model.LoginFailureLockThreshold; i++ {
		code, _ := fx.loginOnce(t, wrongPwd)
		if code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误口令应返回 401，实际 %d", i, code)
		}
		if got := fx.reload(t).FailedLogins; got != i {
			t.Fatalf("第 %d 次失败后计数 = %d，期望 %d", i, got, i)
		}
	}

	// 再错一次：此时应已进入锁定期，返回 429 而不是 401。
	code, body := fx.loginOnce(t, wrongPwd)
	if code != http.StatusTooManyRequests {
		t.Fatalf("锁定期内错误口令应返回 429，实际 %d，body = %v", code, body)
	}
	// code 是前端判断"该展示剩余等待时间还是换密码"的依据，必须稳定。
	// 注意它在 error 子对象里（oai.WriteError 的固定结构）。
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "login_locked" {
		t.Errorf("错误码 = %v，期望 login_locked（前端据此展示剩余时间），完整响应 = %v", errObj["code"], body)
	}
	if msg, _ := errObj["message"].(string); msg == "" {
		t.Errorf("锁定提示文案为空，用户会不知道还要等多久：%v", body)
	}
	// 锁定期内的失败不应继续累加（否则判定报告出来的一次-case 计数会远超阈值）
	if got := fx.reload(t).FailedLogins; got != model.LoginFailureLockThreshold {
		t.Errorf("锁定期间计数仍在增长 = %d，期望停在阈值 %d", got, model.LoginFailureLockThreshold)
	}
}

// TestLoginSecurity_锁定期间正确口令仍可登录 覆盖不变量 2（防止"反向锁定"攻击）。
func TestLoginSecurity_锁定期间正确口令仍可登录(t *testing.T) {
	fx := newLoginSecurityFixture(t)

	for i := 0; i < model.LoginFailureLockThreshold; i++ {
		fx.loginOnce(t, wrongPwd)
	}
	if u := fx.reload(t); !u.IsLocked(time.Now()) {
		t.Fatalf("前置条件失败：账号未进入锁定状态（failed=%d）", u.FailedLogins)
	}

	// 关键断言：账号主人拿着正确口令必须进得来。
	code, body := fx.loginOnce(t, correctPwd)
	if code != http.StatusOK {
		t.Fatalf("锁定期间正确口令应登录成功，实际 %d，body = %v", code, body)
	}
	if _, _ = body["session_token"].(string); body["session_token"] == "" {
		t.Fatalf("登录成功但未下发会话令牌：%v", body)
	}

	// 成功登录后必须解锁：否则用户改完密码还得等 15 分钟。
	if u := fx.reload(t); u.IsLocked(time.Now()) {
		t.Errorf("成功登录后仍处于锁定状态（locked_until=%v）", u.LockedUntil)
	}
}

// TestLoginSecurity_成功登录后清零并记录来源 覆盖不变量 3。
func TestLoginSecurity_成功登录后清零并记录来源(t *testing.T) {
	fx := newLoginSecurityFixture(t)

	// 先攒 3 次失败（不足阈值）
	for i := 0; i < 3; i++ {
		fx.loginOnce(t, wrongPwd)
	}
	if got := fx.reload(t).FailedLogins; got != 3 {
		t.Fatalf("前置条件失败：计数 = %d，期望 3", got)
	}

	code, _ := fx.loginOnce(t, correctPwd)
	if code != http.StatusOK {
		t.Fatalf("正确口令应登录成功，实际 %d", code)
	}

	u := fx.reload(t)
	if u.FailedLogins != 0 {
		t.Errorf("登录后失败计数未清零 = %d，期望 0", u.FailedLogins)
	}
	if u.LastLoginAt.IsZero() {
		t.Error("登录后 last_login_at 未写入（异地判定会失效）")
	}
	if u.LastLoginIP == "" {
		t.Error("登录后 last_login_ip 未写入（异地判定会失效）")
	}
}
