// 安装向导与超管入口（仅密码登录）的单元测试。
//
// 测试重点（都是"装错了会出事"的性质）：
//   - 未安装时安装向导可用；安装成功后安装接口必须【永久自锁】（409）；
//   - 超管入口只凭密码即可登录，密码错误一律 401；
//   - 未安装时超管入口也返回 401（不泄露"该站点是否已初始化"）。
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/config"
	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// newInstallTestServer 装配一个"未安装"的测试服务（库里没有任何管理员）。
func newInstallTestServer(t *testing.T) *Server {
	t.Helper()
	gin.DefaultWriter = io.Discard

	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "install_test.db"))
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

	userRepo := store.NewUserRepository(st.DB())
	cfg := config.Default()
	cfg.Server.Mode = "test"
	cfg.Server.Listen = "127.0.0.1:0"

	return New(Deps{
		Config:   cfg,
		Store:    st,
		Channels: store.NewChannelRepository(st.DB(), cipher),
		Users:    userRepo,
		Sessions: store.NewSessionRepository(st.DB()),
		Tokens:   store.NewTokenRepository(st.DB(), cipher),
		Settings: store.NewSettingRepository(st.DB(), st.Dialect()),
	})
}

// postJSON 发送一次 JSON 请求并返回状态码与解析后的响应体。
func postJSON(t *testing.T, srv *Server, method, path string, payload any) (int, map[string]any) {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	body := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

// TestInstall_安装后接口自锁 验证"装完即关闭"这一关键安全属性。
func TestInstall_安装后接口自锁(t *testing.T) {
	srv := newInstallTestServer(t)

	code, body := postJSON(t, srv, http.MethodGet, "/api/install/status", nil)
	if code != http.StatusOK {
		t.Fatalf("未安装时 status 状态码 = %d，期望 200", code)
	}
	if installed, _ := body["installed"].(bool); installed {
		t.Fatal("空库应报告未安装")
	}

	code, body = postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"username":         "admin",
		"password":         "Aqua-Install-2026",
		"confirm_password": "Aqua-Install-2026",
		"site_name":        "测试站点",
	})
	if code != http.StatusOK {
		t.Fatalf("安装状态码 = %d，期望 200（响应 %v）", code, body)
	}

	if code, body = postJSON(t, srv, http.MethodGet, "/api/install/status", nil); code != http.StatusOK {
		t.Fatalf("安装后 status 状态码 = %d", code)
	}
	if installed, _ := body["installed"].(bool); !installed {
		t.Fatal("安装后应报告已安装")
	}
	if name, _ := body["site_name"].(string); name != "测试站点" {
		t.Errorf("站点名称 = %q，期望「测试站点」", name)
	}

	// 第二次安装必须被拒绝，否则任何人都能重装并接管站点
	code, _ = postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"username": "hacker",
		"password": "Aqua-Install-2026",
	})
	if code != http.StatusConflict {
		t.Fatalf("重复安装状态码 = %d，期望 409", code)
	}
}

// TestInstall_密码强度不足被拒 验证向导复用注册的同一套口令规则。
func TestInstall_密码强度不足被拒(t *testing.T) {
	srv := newInstallTestServer(t)

	code, _ := postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"password": "",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("空密码状态码 = %d，期望 400", code)
	}

	code, _ = postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"password":         "Aqua-Install-2026",
		"confirm_password": "Aqua-Install-2027",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("两次密码不一致状态码 = %d，期望 400", code)
	}
}

// TestAdminLogin_仅密码登录 验证超管入口不需要用户名。
func TestAdminLogin_仅密码登录(t *testing.T) {
	srv := newInstallTestServer(t)

	// 未安装：必须与"密码错误"返回完全相同的状态码，不泄露站点是否已初始化
	if code, _ := postJSON(t, srv, http.MethodPost, "/api/auth/admin-login", map[string]any{"password": "whatever"}); code != http.StatusUnauthorized {
		t.Fatalf("未安装时 admin-login 状态码 = %d，期望 401", code)
	}

	if code, body := postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"password": "Aqua-Install-2026",
	}); code != http.StatusOK {
		t.Fatalf("安装失败，状态码 = %d（%v）", code, body)
	}

	code, body := postJSON(t, srv, http.MethodPost, "/api/auth/admin-login", map[string]any{
		"password": "wrong-password",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("错误密码状态码 = %d，期望 401", code)
	}

	code, body = postJSON(t, srv, http.MethodPost, "/api/auth/admin-login", map[string]any{
		"password": "Aqua-Install-2026",
	})
	if code != http.StatusOK {
		t.Fatalf("正确密码状态码 = %d，期望 200（%v）", code, body)
	}
	token, _ := body["session_token"].(string)
	if token == "" {
		t.Fatal("仅密码登录应返回会话令牌")
	}
	user, _ := body["user"].(map[string]any)
	if role, _ := user["role"].(float64); int(role) != int(model.UserRoleAdmin) {
		t.Errorf("登录身份 role = %v，期望管理员(%d)", user["role"], model.UserRoleAdmin)
	}
}

// TestInstallStatus_未安装时下发步骤清单 验证 OOBE 向导的判定完全由服务端给出。
//
// 这条测试保护的是一个具体失败模式：若前端自己推断"还差哪一步"，
// 后端又下发一份，两处规则一旦不同步，用户就会看到"前端显示已完成、
// 后端拒绝写入"的错位。断言 steps 必须存在且每步都带 key 与中文标题。
func TestInstallStatus_未安装时下发步骤清单(t *testing.T) {
	srv := newInstallTestServer(t)

	code, body := postJSON(t, srv, http.MethodGet, "/api/install/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status 状态码 = %d，期望 200", code)
	}

	rawSteps, ok := body["steps"].([]any)
	if !ok || len(rawSteps) == 0 {
		t.Fatal("未安装时必须下发 steps 供向导渲染")
	}

	// 关键步骤必须在：缺了任一个，向导都会出现"跳过了必需步骤"的路径。
	wantKeys := map[string]bool{
		installStepSite:     false,
		installStepAdmin:    false,
		installStepAccess:   false,
		installStepChannel:  false,
		installStepAnnounce: false,
	}
	for _, raw := range rawSteps {
		step, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("步骤不是对象: %#v", raw)
		}
		key, _ := step["key"].(string)
		if _, want := wantKeys[key]; !want {
			t.Errorf("出现未预期的步骤 key = %q", key)
			continue
		}
		wantKeys[key] = true
		if title, _ := step["title"].(string); title == "" {
			t.Errorf("步骤 %q 缺少中文标题，向导会渲染出空标题", key)
		}
		if desc, _ := step["description"].(string); desc == "" {
			t.Errorf("步骤 %q 缺少说明文案，小白会不知道要填什么", key)
		}
	}
	for key, found := range wantKeys {
		if !found {
			t.Errorf("步骤清单缺少 %q", key)
		}
	}

	// 管理员那一步在未安装时必须显示为"未完成"。
	for _, raw := range rawSteps {
		step := raw.(map[string]any)
		if step["key"] == installStepAdmin {
			if done, _ := step["done"].(bool); done {
				t.Error("空库时管理员步骤不应标记为已完成")
			}
		}
	}
}

// TestInstallStatus_装完后不再下发步骤 验证步骤清单不会泄漏到已安装站点。
func TestInstallStatus_装完后不再下发步骤(t *testing.T) {
	srv := newInstallTestServer(t)

	hash, err := crypto.HashPassword("admin12345")
	if err != nil {
		t.Fatalf("计算口令哈希失败: %v", err)
	}
	if err := srv.deps.Users.Create(context.Background(), &model.User{
		Username:     "admin",
		PasswordHash: hash,
		Role:         model.UserRoleAdmin,
		Status:       model.UserStatusEnabled,
		Quota:        model.QuotaUnlimited,
	}); err != nil {
		t.Fatalf("预置管理员失败: %v", err)
	}

	code, body := postJSON(t, srv, http.MethodGet, "/api/install/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status 状态码 = %d，期望 200", code)
	}
	if _, exists := body["steps"]; exists {
		t.Error("已安装后不应再下发 steps（向导不再显示，且会暴露配置细节）")
	}
	if _, exists := body["default_admin_username"]; exists {
		t.Error("已安装后不应再下发 default_admin_username")
	}
}

// TestBuildInstallSteps_站点名等于默认值时视为未完成 验证占位默认名不会冒充"已填"。
//
// 默认名是系统给的白牌占位（"LTZY-API"），站长没主动填过。
// 若把它算作已完成，向导第一步会直接打勾，小白会以为站点名已经定好了。
func TestBuildInstallSteps_站点名等于默认值时视为未完成(t *testing.T) {
	srv := newInstallTestServer(t)

	defaults := model.DefaultSiteSettings()
	steps := srv.buildInstallSteps(defaults, false)

	for _, step := range steps {
		if step.Key == installStepSite {
			if step.Done {
				t.Errorf("站点名仍是默认名 %q 时不应判定为完成", defaults.SiteName)
			}
			return
		}
	}
	t.Fatal("步骤清单缺少站点步骤")
}

// TestBuildInstallSteps_填过站点名后判为完成 验证站长填了名字就会打勾。
func TestBuildInstallSteps_填过站点名后判为完成(t *testing.T) {
	srv := newInstallTestServer(t)

	settings := model.DefaultSiteSettings()
	settings.SiteName = "老王的 AI 站"
	steps := srv.buildInstallSteps(settings, true)

	byKey := make(map[string]installStepState, len(steps))
	for _, step := range steps {
		byKey[step.Key] = step
	}
	if site, ok := byKey[installStepSite]; !ok || !site.Done {
		t.Error("填过站点名后该步骤应判定为完成")
	}
	if admin, ok := byKey[installStepAdmin]; !ok || !admin.Done {
		t.Error("installed=true 时管理员步骤应判定为完成")
	}
	// 渠道与公告始终可跳过：装完站点再补配完全来得及，不该拦住安装完成。
	for _, key := range []string{installStepChannel, installStepAnnounce} {
		step, ok := byKey[key]
		if !ok {
			t.Fatalf("步骤清单缺少 %q", key)
		}
		if !step.Optional {
			t.Errorf("步骤 %q 应标记为可跳过，否则小白会以为必须配渠道才能用", key)
		}
	}
}

// TestInstall_同时写入站点名与描述 验证 OOBE 同一步收集的两项都会落库。
//
// 单测只覆盖 site_name 时，site_description 会被静默丢弃：JSON 反序列化不认的
// 字段不会报错，前端却已经提示"填好了"，这类"填了没生效"最难自查。
func TestInstall_同时写入站点名与描述(t *testing.T) {
	srv := newInstallTestServer(t)

	code, _ := postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"username":         "admin",
		"password":         "admin12345",
		"confirm_password": "admin12345",
		"site_name":        "老王的 AI 站",
		"site_description": "面向开发者的模型网关",
	})
	if code != http.StatusOK {
		t.Fatalf("安装状态码 = %d，期望 200", code)
	}

	settings, err := model.LoadSiteSettings(context.Background(), srv.deps.Settings)
	if err != nil {
		t.Fatalf("读取站点设置失败: %v", err)
	}
	if settings.SiteName != "老王的 AI 站" {
		t.Errorf("站点名未写入，实际 = %q", settings.SiteName)
	}
	if settings.SiteDescription != "面向开发者的模型网关" {
		t.Errorf("站点描述未写入，实际 = %q", settings.SiteDescription)
	}

	// 写完之后步骤清单里站点那一步应判定为完成。
	steps := srv.buildInstallSteps(settings, true)
	for _, s := range steps {
		if s.Key == installStepSite && !s.Done {
			t.Error("站点名已写入后该步骤仍判为未完成，向导会让人重复填一遍")
		}
	}
}

// TestInstall_描述留空时保持默认值 验证可选字段留空不会把描述写成空串。
func TestInstall_描述留空时保持默认值(t *testing.T) {
	srv := newInstallTestServer(t)

	code, _ := postJSON(t, srv, http.MethodPost, "/api/install", map[string]any{
		"username":         "admin",
		"password":         "admin12345",
		"confirm_password": "admin12345",
		"site_name":        "老王的 AI 站",
		// site_description 故意不填
	})
	if code != http.StatusOK {
		t.Fatalf("安装状态码 = %d，期望 200", code)
	}

	settings, err := model.LoadSiteSettings(context.Background(), srv.deps.Settings)
	if err != nil {
		t.Fatalf("读取站点设置失败: %v", err)
	}
	if settings.SiteDescription == "" {
		t.Error("描述留空时应回退到默认值，而不是被写成空串")
	}
}
