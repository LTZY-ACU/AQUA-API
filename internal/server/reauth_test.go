// 敏感操作二次验证（step-up auth）的单元测试。
//
// 意图（Why）：
//
//	这项机制的价值在于"被劫持的会话也做不了高危动作"，而它失效的方式很隐蔽：
//	把二次验证【挂在用户上而不是会话上】就足以让它形同虚设——
//	一次输密码会放行所有设备。因此本文件最关键的一条断言是：
//	同一管理员的【另一条会话】依然被拦住。
//
// 另外两条必须钉住：
//   - 口令错误不得刷新验证时刻（否则等于用错误口令就能拿到授权）；
//   - 判断依据是"窗口内"，过期后要重新要求验证。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run Reauth
package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// reauthFixture 是二次验证用例的运行固件。
type reauthFixture struct {
	srv  *Server
	st   *store.Store
	user *model.User
	// sessionA / sessionB 是同一个管理员的两条会话：
	// 用它们验证"二次验证只在本会话生效"。
	sessionA string
	sessionB string
}

const reauthAdminPassword = "Admin-Reauth-2026"

// newReauthFixture 造一个管理员（真实 bcrypt 口令）与两条会话。
func newReauthFixture(t *testing.T) *reauthFixture {
	t.Helper()

	srv, st := newTestServer(t)
	users := store.NewUserRepository(st.DB())
	sessions := store.NewSessionRepository(st.DB())
	ctx := context.Background()

	hash, err := crypto.HashPassword(reauthAdminPassword)
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	u := &model.User{
		Username: "reauth-admin", PasswordHash: hash,
		Role: model.UserRoleAdmin, Status: model.UserStatusEnabled,
		Quota: model.QuotaUnlimited,
	}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	mk := func(tag string) string {
		token := "reauth-session-" + tag
		if err := sessions.Create(ctx, &model.Session{
			UserID:    u.ID,
			TokenHash: crypto.SHA256Hex(token),
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("创建会话失败: %v", err)
		}
		return token
	}
	return &reauthFixture{srv: srv, st: st, user: u, sessionA: mk("a"), sessionB: mk("b")}
}

// TestReauth_敏感操作先拦后放 覆盖主链路与"口令错误不算验证"。
func TestReauth_敏感操作先拦后放(t *testing.T) {
	fx := newReauthFixture(t)

	// 未二次验证：必须被拦在业务逻辑之前（403 + reauth_required）
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.sessionA,
		`{"quota":100,"expires_hours":24,"batch":"reauth-test","confirm":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未二次验证应返回 403，实际 %d，body = %v", rec.Code, body)
	}
	if errObj, _ := body["error"].(map[string]any); errObj["code"] != "reauth_required" {
		t.Errorf("错误码 = %v，期望 reauth_required（前端据此弹出重新输密码的框）", body)
	}

	// 口令错误：不得刷新验证时刻
	rec, _ = doBearerJSON(t, fx.srv, http.MethodPost, "/api/auth/reauth", fx.sessionA,
		`{"password":"definitely-wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("口令错误应返回 401，实际 %d", rec.Code)
	}
	rec, _ = doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.sessionA,
		`{"quota":100,"expires_hours":24,"batch":"reauth-test","confirm":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("错误口令后不应放行敏感操作，实际 %d", rec.Code)
	}

	// 正确口令：验证通过后该操作应放行
	// （本用例里试用额模块未注入，放行后的落点是 503；只要不再是 403 就说明已通过闸门。）
	rec, body = doBearerJSON(t, fx.srv, http.MethodPost, "/api/auth/reauth", fx.sessionA,
		`{"password":"`+reauthAdminPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确口令应验证通过，实际 %d，body = %v", rec.Code, body)
	}
	if until, _ := body["reauth_until"].(float64); until <= float64(time.Now().Unix()) {
		t.Errorf("应回传免密窗口的截止时刻，实际 %v", body["reauth_until"])
	}

	rec, _ = doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.sessionA,
		`{"quota":100,"expires_hours":24,"batch":"reauth-test","confirm":true}`)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("二次验证通过后不应再被拦：%d", rec.Code)
	}
}

// TestReauth_只对本会话生效 覆盖"挂在会话而不是用户上"这条关键设计。
func TestReauth_只对本会话生效(t *testing.T) {
	fx := newReauthFixture(t)

	// 在会话 A 上完成二次验证
	rec, _ := doBearerJSON(t, fx.srv, http.MethodPost, "/api/auth/reauth", fx.sessionA,
		`{"password":"`+reauthAdminPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("二次验证应通过，实际 %d", rec.Code)
	}

	// 会话 B（同一个管理员、另一台"设备"）必须依然被要求验证：
	// 若这条被放行，说明验证时效挂在了用户上——一次输密码就放行所有设备，等于没有二次验证。
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.sessionB,
		`{"quota":100,"expires_hours":24,"batch":"reauth-test-2","confirm":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("另一条会话应仍被拦下，实际 %d，body = %v", rec.Code, body)
	}
}

// TestReauth_窗口过期后需重新验证 覆盖时效。
func TestReauth_窗口过期后需重新验证(t *testing.T) {
	// 直接验证判定函数：真实等待 15 分钟对单元测试不可接受，
	// 而判定逻辑本身（IsReauthFresh）是本机制的时间边界，必须被覆盖到。
	now := time.Now()

	fresh := &model.Session{ReauthAt: now}
	if !fresh.IsReauthFresh(now, ReauthWindow) {
		t.Error("刚验证过的会话应处于免密窗口内")
	}
	if fresh.IsReauthFresh(now.Add(ReauthWindow+time.Minute), ReauthWindow) {
		t.Error("超出窗口后必须重新验证")
	}

	// 从未验证过的会话（零值 ReauthAt）必须要求验证：
	// 零值是一个真实状态（老会话迁移后即为该值），不能被误判成"刚刚验证过"。
	never := &model.Session{}
	if never.IsReauthFresh(now, ReauthWindow) {
		t.Error("从未验证过的会话不应处于免密窗口内")
	}
}

// mustReauthedSession 把一条新建的会话标记为"刚刚完成二次验证"，并原样返回其令牌。
//
// 用途：群发、发放试用额等受二次验证保护的接口，其既有用例测的是【业务逻辑】，
// 不是闸门本身（闸门由本文件的用例覆盖）。若让每个老固件都去走一遍验证流程，
// 会把"验证密码"这类前置动作散布到几十处，反而稀释了它们各自的断言焦点。
func mustReauthedSession(t *testing.T, sessions model.SessionRepository, rawToken string) string {
	t.Helper()

	ctx := context.Background()
	session, err := sessions.GetByTokenHash(ctx, crypto.SHA256Hex(rawToken))
	if err != nil {
		t.Fatalf("按令牌摘要查询会话失败: %v", err)
	}
	if err := sessions.UpdateReauth(ctx, session.ID, time.Now()); err != nil {
		t.Fatalf("标记会话已完成二次验证失败: %v", err)
	}
	return rawToken
}
