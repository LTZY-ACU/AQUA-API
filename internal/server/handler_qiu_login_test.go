// 「QIU 科技账号登录」的单元测试。
//
// 意图（Why）：
//
//	第三方登录有三处出错会造成严重后果，且都不是普通测试能覆盖到的：
//	  1) 【第二次登录换了个人】：必须靠绑定关系回到同一个本站账号，
//	     否则用户第二次进来是个空号，额度与令牌全对不上；
//	  2) 【用可变信息匹配账号】：若改用昵称/用户名去猜本地账号，
//	     攻击者把昵称改成别人的用户名就能接管对方账号——本文件的"改昵称后仍要登录到
//	     同一个号"用例就是钉这条红线的；
//	  3) 【task_id 变成路径注入入口】：它直接参与拼 URL，必须严格过白名单。
//
// 流转（Flow）：
//
//	httptest 起一个假 QIU 服务 → 把配置里的 BaseURL 指向它
//	→ 走完 start / status 两步 → 断言账号归属、绑定关系与预期一致
//
// 扩展（Extend）：
//
//	新增第三个接口（如解绑）时，按同样的"正面路径 + 边界拒绝"两段式补用例。
package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// fakeQIU 是一个最小的 QIU 服务替身（只需实现 startlogin 与 readlogin 两个端点）。
//
// 为什么要它：真实第三方接口在公网且会变化，用例不能依赖它；
// 而我们要验证的是"本站这一侧的解析、匹配与建号逻辑"。
type fakeQIU struct {
	server   *httptest.Server
	status   atomic.Value // status 结果里的 status 字段
	userID   atomic.Value // user 对象里的 id
	username atomic.Value
	nickname atomic.Value
	// startCalls 记录 /startlogin 被调用的次数，用于验证"确实去建了任务"。
	startCalls atomic.Int64
}

// newFakeQIU 起一个假的 QIU 服务，并在测试结束时关闭它。
func newFakeQIU(t *testing.T) *fakeQIU {
	t.Helper()

	f := &fakeQIU{}
	f.status.Store("pending")
	f.userID.Store(float64(10001))
	f.username.Store("qiu-tester")
	f.nickname.Store("测试者")

	mux := http.NewServeMux()
	mux.HandleFunc("/startlogin", func(w http.ResponseWriter, r *http.Request) {
		f.startCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// app 参数是调用方透传的：这里顺手回显回来，
		// 便于将来断言"站点名有没有正确传过去"。
		app := r.URL.Query().Get("app")
		_, _ = w.Write([]byte(`{"task_id":"task-abc123","url":"https://qiu.example.com/confirm?task=task-abc123&app=` +
			app + `"}`))
	})
	mux.HandleFunc("/readlogin/", func(w http.ResponseWriter, r *http.Request) {
		taskID := strings.TrimPrefix(r.URL.Path, "/readlogin/")
		if taskID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		userID := f.userID.Load()
		idJSON := toIDJSON(userID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"` + jsonStr(f.status.Load()) +
			`","user":{"id":` + idJSON + `,"username":"` + jsonStr(f.username.Load()) +
			`","nickname":"` + jsonStr(f.nickname.Load()) + `"},"token":"qiu-token"}`))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// qiuFixture 是 QIU 登录用例的运行固件。
type qiuFixture struct {
	srv *Server
	fx  *announcementFixture
	qiu *fakeQIU
}

// newQIUFixture 造一个装配了假 QIU 服务与绑定仓储的完整服务。
func newQIUFixture(t *testing.T) *qiuFixture {
	t.Helper()

	fx := newAnnouncementFixture(t)
	fx.srv.deps.ExternalAccounts = store.NewExternalAccountRepository(fx.srv.deps.Store.DB())
	qiu := newFakeQIU(t)

	cfg := fx.srv.deps.Config
	cfg.QIU.Enabled = true
	cfg.QIU.BaseURL = qiu.server.URL
	cfg.QIU.AppName = ""
	cfg.QIU.TimeoutSeconds = config.DefaultQIUTimeoutSeconds
	fx.srv.deps.Config = cfg

	return &qiuFixture{srv: fx.srv, fx: fx, qiu: qiu}
}

// TestQIULogin_二次登录必须回到同一个账号 覆盖"绑定关系才是身份依据"。
func TestQIULogin_二次登录必须回到同一个账号(t *testing.T) {
	f := newQIUFixture(t)

	// 第一次：确实去创建了任务
	rec, body := doAnnouncementJSON(t, f.srv, http.MethodPost, "/api/auth/qiu/start", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("创建 QIU 登录任务应返回 200，实际 %d，body = %v", rec.Code, body)
	}
	taskID, _ := body["task_id"].(string)
	if taskID == "" {
		t.Fatalf("响应缺少 task_id：%v", body)
	}
	if url, _ := body["url"].(string); !strings.HasPrefix(url, "https://") {
		t.Errorf("确认页地址必须是 https 绝对地址，实际 %q", url)
	}

	// 用户点了确认
	f.qiu.status.Store("ok")

	rec, body = doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/"+taskID, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("轮询应返回 200，实际 %d，body = %v", rec.Code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("第一次登录应成功，实际 %v", body)
	}
	firstToken, _ := body["session_token"].(string)
	if firstToken == "" {
		t.Fatalf("登录成功但未下发会话令牌：%v", body)
	}
	userObj, _ := body["user"].(map[string]any)
	firstUID, _ := userObj["id"].(float64)

	// 第二次：把昵称改掉再登一次。
	// 关键断言：必须还是同一个本站账号 —— 昵称是对方平台可随意修改的字段，
	// 一旦拿它当匹配依据，攻击者改个名字就能接管别人的账号。
	f.qiu.nickname.Store("换了个名字的人")
	rec, body = doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/"+taskID, "", "")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("第二次登录应成功，实际 %d / %v", rec.Code, body)
	}
	userObj2, _ := body["user"].(map[string]any)
	if uid, _ := userObj2["id"].(float64); uid != firstUID {
		t.Fatalf("二次登录落到了不同账号：%v ≠ %v（绑定关系失效会导致用户丢号）", uid, firstUID)
	}

	// 库里应当只有一条绑定关系：多次成功登录不产生重复绑定。
	items, err := f.srv.deps.ExternalAccounts.ListByUser(context.Background(), uint64(firstUID))
	if err != nil {
		t.Fatalf("查询绑定关系失败: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("绑定关系条数 = %d，期望 1（重复绑定会让后续识别出现歧义）", len(items))
	}
}

// TestQIULogin_未确认与极端任务号 覆盖边界拒绝。
func TestQIULogin_未确认与极端任务号(t *testing.T) {
	f := newQIUFixture(t)

	// 未确认：状态如实回传 pending，不得当作登录成功
	rec, body := doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/task-abc123", "", "")
	if rec.Code != http.StatusOK || body["status"] != "pending" {
		t.Fatalf("未确认应返回 pending，实际 %d / %v", rec.Code, body)
	}
	if _, ok := body["session_token"]; ok {
		t.Error("pending 状态绝不能下发会话令牌")
	}

	// 非法任务号：不得被拼进出站 URL（它会流向第三方，路径注入的入口就在这里）
	for _, bad := range []string{"../../etc/passwd", "abc/def", "abc%2f", "a\\b"} {
		rec, _ := doAnnouncementJSON(t, f.srv, http.MethodGet,
			"/api/auth/qiu/status/"+bad, "", "")
		if rec.Code == http.StatusOK {
			t.Errorf("任务号 %q 应被拒绝，实际放行（状态码 %d）", bad, rec.Code)
		}
	}
}

// TestQIULogin_未开放时返回404 覆盖关闭开关的行为。
func TestQIULogin_未开放时返回404(t *testing.T) {
	f := newQIUFixture(t)
	cfg := f.srv.deps.Config
	cfg.QIU.Enabled = false
	f.srv.deps.Config = cfg

	rec, _ := doAnnouncementJSON(t, f.srv, http.MethodPost, "/api/auth/qiu/start", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未开放第三方登录时应返回 404（表示本站点不存在该功能），实际 %d", rec.Code)
	}
	rec, _ = doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/task-abc123", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("未开放时应返回 404，实际 %d", rec.Code)
	}
}

// TestQIULogin_已登录用户可绑定既有账号 覆盖"不开新号也能用第三方登录"。
func TestQIULogin_已登录用户可绑定既有账号(t *testing.T) {
	f := newQIUFixture(t)
	f.qiu.status.Store("ok")
	// 换一个外部 id，避免与前面的用例互相干扰
	f.qiu.userID.Store(float64(77777))

	rec, body := doAnnouncementJSON(t, f.srv, http.MethodPost, "/api/auth/qiu/bind", f.fx.userTok,
		`{"task_id":"task-abc123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("已登录用户绑定应返回 200，实际 %d，body = %v", rec.Code, body)
	}

	bound, err := f.srv.deps.ExternalAccounts.GetByExternalID(context.Background(),
		model.ExternalProviderQIU, "77777")
	if err != nil {
		t.Fatalf("绑定关系未落库: %v", err)
	}
	// 绑定的是当前登录的那个账号，而不是新建的
	me, err := f.srv.deps.Users.GetByUsername(context.Background(), "announcement-user")
	if err != nil {
		t.Fatalf("读取占位用户失败: %v", err)
	}
	if bound.UserID != me.ID {
		t.Errorf("绑定到了账号 %d，期望当前登录账号 %d", bound.UserID, me.ID)
	}

	// 之后用同一个第三方身份登录：应当落到这个既有账号
	rec, body = doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/task-abc123", "", "")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("绑定后应能直接登录，实际 %d / %v", rec.Code, body)
	}
	userObj, _ := body["user"].(map[string]any)
	if uid, _ := userObj["id"].(float64); uint64(uid) != me.ID {
		t.Errorf("登录到了账号 %v，期望既有账号 %d", uid, me.ID)
	}
}

// TestQIULogin_已登录用户轮询不得建号发会话 钉住"绑定流程被误当成登录"这个坑。
//
// 场景：用户在控制台点「绑定第三方账号」，前端走的是同一条轮询链接。
// 若处理器认不出调用方已登录，就会为他凭空建一个新号并下发属于新号的会话——
// 用户被静默切走，而真正的绑定其实一步都没做。
func TestQIULogin_已登录用户轮询不得建号发会话(t *testing.T) {
	f := newQIUFixture(t)
	f.qiu.status.Store("ok")
	// 换一个全新的外部 id：一旦按登录流程处理，它必然凭空建号。
	f.qiu.userID.Store(float64(88888))

	rec, body := doAnnouncementJSON(t, f.srv, http.MethodGet,
		"/api/auth/qiu/status/task-abc123", f.fx.userTok, "")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("已登录用户轮询应回报已确认，实际 %d / %v", rec.Code, body)
	}
	if tok, _ := body["session_token"].(string); tok != "" {
		t.Error("已登录用户轮询绝不能下发会话令牌：一旦下发，用户就被静默切到另一个账号")
	}
	if _, err := f.srv.deps.ExternalAccounts.GetByExternalID(context.Background(),
		model.ExternalProviderQIU, "88888"); !errors.Is(err, model.ErrExternalAccountNotFound) {
		t.Errorf("轮询不应产生绑定关系（绑定只能由 /auth/qiu/bind 建立），实际 err = %v", err)
	}
	if _, ok := body["profile"]; !ok {
		t.Error("应回传对方身份的展示信息，供前端确认「绑的是这个号」")
	}

	// 未登录时仍必须是完整登录流程：本改动不得影响主路径
	f.qiu.userID.Store(float64(99999))
	rec, body = doAnnouncementJSON(t, f.srv, http.MethodGet, "/api/auth/qiu/status/task-abc123", "", "")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("未登录轮询应完成登录，实际 %d / %v", rec.Code, body)
	}
	if tok, _ := body["session_token"].(string); tok == "" {
		t.Error("未登录轮询必须下发会话令牌，否则登录流程被破坏")
	}
}

// jsonStr 取出 atomic.Value 里的字符串（空值返回空串）。
//
// 单独封装是为了让上面的响应拼装保持简短——它是测试脚手架，不是被测逻辑。
func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}

// toIDJSON 把外部用户 id 序列化成 JSON 片段（数字保持数字形态，
// 因为真实接口就是这么返回的，用例要覆盖到这条解析路径）。
func toIDJSON(v any) string {
	switch val := v.(type) {
	case float64:
		return strconv.FormatInt(int64(val), 10)
	case int:
		return strconv.Itoa(val)
	case string:
		return `"` + val + `"`
	default:
		return "0"
	}
}
