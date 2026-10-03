// 注册接口「邮箱唯一」的单元测试。
//
// 意图（Why）：
//
//	"一个邮箱只能绑定一个账号"最终由数据库唯一索引兜底（迁移 0036），
//	但用户能不能得到一条【可操作】的提示，取决于注册处理器：
//	  - 邮箱冲突要在【消费验证码之前】被拦下（否则用户白白浪费一次验证码）；
//	  - 返回的必须是 auth.email_taken 而不是 auth.username_taken
//	    （两者对用户的指示不同：换邮箱 vs 换用户名）。
//	本测试把这两条不变量锁死。
//
// 流转（Flow）：
//
//	POST /api/auth/register（带邮箱）→ 断言 409 + error.code == email_taken
//
// 扩展（Extend）：
//
//	将来若把"邮箱找回/邮箱登录"做进来，注册时的占用预检逻辑可以复用，
//	但错误文案要区分场景（"该邮箱已注册，请直接登录"），届时在此补充用例。
package server

import (
	"net/http"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// registerWithEmail 发起一次带邮箱的注册，直接返回 HTTP 状态与响应体。
//
// 不复用 registerUser（它断言 200 会 t.Fatal）：本测试要验证的就是失败路径。
func registerWithEmail(t *testing.T, srv *Server, username, email string) (int, map[string]any) {
	t.Helper()
	payload := map[string]any{
		"username": username, "password": "pass-" + username,
		"email": email, "agreed_terms": true,
	}
	rec, body := callJSON(t, srv, http.MethodPost, "/api/auth/register", payload, "")
	return rec.Code, body
}

// TestHandlerRegister_重复邮箱_返回email_taken 验证同一邮箱不能绑定第二个账号。
func TestHandlerRegister_重复邮箱_返回email_taken(t *testing.T) {
	srv, st := newReferralTestServer(t)
	applySettings(t, st, map[string]string{
		// 关闭验证码，让"邮箱冲突"与"验证码失败"解耦，只验证前者
		model.SettingKeyRegistrationRequireEmailCode: "false",
	})

	// 第一次注册成功
	code, _ := registerWithEmail(t, srv, "alice", "alice@example.com")
	if code != http.StatusOK {
		t.Fatalf("首次注册应成功，实际 %d", code)
	}

	// 第二次用同邮箱注册 → 409 + email_taken
	code, body := registerWithEmail(t, srv, "bob", "alice@example.com")
	if code != http.StatusConflict {
		t.Fatalf("重复邮箱应返回 409，实际 %d，响应 %v", code, body)
	}
	errMap, _ := body["error"].(map[string]any)
	if errMap == nil {
		t.Fatalf("响应缺少 error 字段：%v", body)
	}
	if errMap["code"] != "email_taken" {
		t.Fatalf("错误码应为 email_taken，实际 %v（响应 %v）", errMap["code"], body)
	}
	// 绝不能是 username_taken —— 用户名明明没冲突，报它会把用户带偏
	if errMap["code"] == "username_taken" {
		t.Fatal("邮箱冲突被误报为 username_taken")
	}
}

// TestHandlerRegister_大小写变体邮箱_同样被拒绝 验证归一化口径贯穿注册链路。
func TestHandlerRegister_大小写变体邮箱_同样被拒绝(t *testing.T) {
	srv, st := newReferralTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyRegistrationRequireEmailCode: "false",
	})

	code, _ := registerWithEmail(t, srv, "alice", "Alice@Example.COM")
	if code != http.StatusOK {
		t.Fatalf("首次注册应成功，实际 %d", code)
	}

	// 归一化后与上一个是同一个邮箱，必须被拦下
	code, body := registerWithEmail(t, srv, "bob", "  alice@example.com  ")
	if code != http.StatusConflict {
		t.Fatalf("大小写/空白变体应返回 409，实际 %d，响应 %v", code, body)
	}
	errMap, _ := body["error"].(map[string]any)
	if errMap["code"] != "email_taken" {
		t.Fatalf("错误码应为 email_taken，实际 %v", errMap["code"])
	}
}

// TestHandlerRegister_不同邮箱_正常注册 验证修复不误伤正常路径。
func TestHandlerRegister_不同邮箱_正常注册(t *testing.T) {
	srv, st := newReferralTestServer(t)
	applySettings(t, st, map[string]string{
		model.SettingKeyRegistrationRequireEmailCode: "false",
	})

	if code, _ := registerWithEmail(t, srv, "alice", "alice@example.com"); code != http.StatusOK {
		t.Fatalf("第一个用户注册失败，实际 %d", code)
	}
	if code, _ := registerWithEmail(t, srv, "bob", "bob@example.com"); code != http.StatusOK {
		t.Fatalf("第二个用户（不同邮箱）应注册成功，实际 %d", code)
	}
}
