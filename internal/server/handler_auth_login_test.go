// 本文件的回归测试覆盖两件事：
//
//  1. 一次**线上事故**：/api/auth/login 前面挂了"账号维度限流"中间件，
//     它的 keyFunc 需要读请求体里的 username。gin 的 ShouldBindJSON 读完
//     c.Request.Body 即空、且不会自动复原 —— keyFunc 一旦吃掉 body，
//     处理器就再也绑不到参数，**所有账号都登录失败**并返回 invalid_json。
//     相关用例的断言刻意落在"响应码不是 400"上：口令错就该是 401，
//     格式错才是 400。只要这两者被混淆，就说明又有中间件在处理器之前
//     消费了请求体。
//
//  2. "用户名或邮箱 + 密码"登录：标识符先按用户名精确匹配、未命中再按
//     绑定邮箱匹配（邮箱归一化后查询）。三条关键不变量：
//     · 邮箱 + 正确口令必须能登录成功；
//     · 任何标识符 + 错误口令必须 401（绝不能 400/500）；
//     · 用户名优先于邮箱——存在"用户名长得像邮箱"的账号时，
//     该标识符必须命中用户名那个账号。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run Login
//
// 扩展（Extend）：
//
//	新增"登录前会读 body 的中间件"（风控、审计等）时，务必在
//	TestLogin_请求体不被前置中间件吃掉 里补一条断言。
package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// TestLogin_请求体不被前置中间件吃掉 覆盖"登录前有人读了 body 却没放回"。
func TestLogin_请求体不被前置中间件吃掉(t *testing.T) {
	srv, st := newTestServer(t)
	users := store.NewUserRepository(st.DB())

	hash, err := crypto.HashPassword("correct-password-123")
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	u := &model.User{
		Username: "login-regression", PasswordHash: hash,
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 口令错误：必须是 401（认证失败），绝不能是 400（请求体格式错误）。
	rec, body := doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"login-regression","password":"definitely-wrong"}`)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("口令错误被当成请求体格式错误（请求体被前置中间件吃掉了）：%v", body)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("口令错误应返回 401，实际 %d，body = %v", rec.Code, body)
	}

	// 口令正确：必须能正常登录并下发会话。
	rec, body = doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"login-regression","password":"correct-password-123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确口令应登录成功，实际 %d，body = %v", rec.Code, body)
	}
	if token, _ := body["session_token"].(string); token == "" {
		t.Fatalf("登录成功但未下发会话令牌：%v", body)
	}
}

// TestLogin_邮箱作标识符_应命中绑定该邮箱的账号 覆盖"用邮箱 + 密码登录"主路径。
func TestLogin_邮箱作标识符_应命中绑定该邮箱的账号(t *testing.T) {
	srv, st := newTestServer(t)
	users := store.NewUserRepository(st.DB())
	ctx := context.Background()

	hash, err := crypto.HashPassword("mailbox-pass-456")
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	u := &model.User{
		Username: "mailbox-user", PasswordHash: hash, Email: "someone@example.com",
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
	}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 用邮箱 + 正确口令登录：必须成功，且返回的就是这个账号。
	rec, body := doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"someone@example.com","password":"mailbox-pass-456"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("邮箱 + 正确口令应登录成功，实际 %d，body = %v", rec.Code, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["username"] != "mailbox-user" {
		t.Fatalf("邮箱登录应命中 mailbox-user，实际 %v", body)
	}

	// 邮箱大小写与前后空格不影响命中（后端做归一化）。
	rec, body = doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"  Someone@Example.COM ","password":"mailbox-pass-456"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("大小写不同的邮箱应同样命中，实际 %d，body = %v", rec.Code, body)
	}

	// 用邮箱 + 错误口令：必须是 401（认证失败），不能是别的状态码。
	rec, body = doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"someone@example.com","password":"totally-wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("邮箱 + 错误口令应返回 401，实际 %d，body = %v", rec.Code, body)
	}

	// 未绑定的邮箱：与"用户不存在"同样处理（401，不泄露账号存在性）。
	rec, _ = doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"nobody@example.com","password":"whatever"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未知邮箱应返回 401，实际 %d", rec.Code)
	}
}

// TestLogin_用户名优先于邮箱 锁死标识符的解析顺序。
//
// 场景：A 的用户名恰好长得像 B 的邮箱（用户名允许含 @）。
// 此时输入该标识符必须命中【用户名】那个账号——
// 若把邮箱查询放在前面，A 将永远登录到 B 的账号语义上（口令对不上，
// 表现为"用户名登录突然失效"），这是顺序写反时的典型症状。
func TestLogin_用户名优先于邮箱(t *testing.T) {
	srv, st := newTestServer(t)
	users := store.NewUserRepository(st.DB())
	ctx := context.Background()

	nameHash, err := crypto.HashPassword("name-side-pass")
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	// 账号一：用户名是邮箱形状。
	if err := users.Create(ctx, &model.User{
		Username: "clone@example.com", PasswordHash: nameHash,
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
	}); err != nil {
		t.Fatalf("创建用户名形状账号失败: %v", err)
	}
	// 账号二：邮箱恰好是同一个字符串。
	mailHash, err := crypto.HashPassword("email-side-pass")
	if err != nil {
		t.Fatalf("生成口令哈希失败: %v", err)
	}
	if err := users.Create(ctx, &model.User{
		Username: "email-owner", PasswordHash: mailHash, Email: "clone@example.com",
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
	}); err != nil {
		t.Fatalf("创建邮箱形状账号失败: %v", err)
	}

	// 同一标识符 + 用户名侧的口令 → 必须命中用户名那个账号。
	rec, body := doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"clone@example.com","password":"name-side-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("标识符应优先命中用户名侧账号，实际 %d，body = %v", rec.Code, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["username"] != "clone@example.com" {
		t.Fatalf("应命中用户名账号 clone@example.com，实际 %v", body)
	}

	// 邮箱侧账号仍可用【它的用户名】正常登录（未被抢走）。
	rec, _ = doBearerJSON(t, srv, http.MethodPost, "/api/auth/login", "",
		`{"username":"email-owner","password":"email-side-pass"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("邮箱侧账号应能按用户名登录，实际 %d", rec.Code)
	}
}
