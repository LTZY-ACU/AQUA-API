// 邮件群发后台接口的单元测试。
//
// 测试重点（都是"点错一下就会发给全体用户"的地方）：
//   - 必须显式 confirm：全站群发不可撤回，接口不能让误触直接触发；
//   - 未配置邮件通道时不许创建批次：否则会留下一堆失败明细，还得逐个复盘；
//   - 没有任何人填邮箱时不许"静默成功"：应明确告诉管理员"一个人都没发"；
//   - 正常路径必须真的把名单入队并开始发送（本用例用假发送器验证到实际投递）。
//
// 说明：Deps.Mailer 是具体类型（用于"通道是否就绪"的判断与预览），
// 而群发实际走的是可注入的 broadcast.Sender —— 本文件用假发送器验证投递，
// 不依赖任何真实 SMTP（预览的成功路径需要真连服务器，故只覆盖其守卫分支）。
package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/aqua-api/internal/broadcast"
	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/mailer"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// fakeBroadcastMailer 记录群发实际投递到的地址。
//
// 加锁的原因：发送跑在后台 goroutine 里，而断言在主 goroutine 读它。
type fakeBroadcastMailer struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeBroadcastMailer) Send(_ context.Context, to, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to)
	return nil
}

func (f *fakeBroadcastMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// errorCode 从错误响应里取出错误码（oai.WriteError 的固定结构）。
//
// 断言错误码而不是文案：文案随时可能改，错误码是给程序看的契约。
func errorCode(body map[string]any) string {
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

// broadcastFixture 汇总群发接口测试所需的仓储、会话与假发送器。
type broadcastFixture struct {
	srv      *Server
	st       *store.Store
	users    model.UserRepository
	bcasts   model.EmailBroadcastRepository
	fake     *fakeBroadcastMailer
	adminTok string
	adminID  uint64
}

// newBroadcastFixture 构造最小服务；adminEmail 为空表示管理员未填邮箱。
func newBroadcastFixture(t *testing.T, adminEmail string, mailerConfigured bool) *broadcastFixture {
	t.Helper()
	gin.DefaultWriter = io.Discard

	dsn := filepath.Join(t.TempDir(), "broadcast_api_test.db")
	st, err := store.Open("sqlite", dsn)
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

	cfg := config.Default()
	cfg.Server.Mode = "test"
	cfg.Server.Listen = "127.0.0.1:0"

	ctx := context.Background()
	users := store.NewUserRepository(st.DB())
	sessions := store.NewSessionRepository(st.DB())
	bcasts := store.NewEmailBroadcastRepository(st.DB())
	fake := &fakeBroadcastMailer{}

	admin := &model.User{
		Username: "broadcast-admin", PasswordHash: "test-hash", Email: adminEmail,
		Role: model.UserRoleAdmin, Status: model.UserStatusEnabled, Quota: model.QuotaUnlimited,
	}
	if err := users.Create(ctx, admin); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	// 发送节奏压到毫秒级：本用例验证的是接口与装配，不是生产间隔。
	sender := broadcast.New(bcasts, users, fake, broadcast.Options{
		Interval: time.Millisecond, BatchSize: 2, BatchPause: time.Millisecond,
	})

	// Mailer 只用于"通道是否就绪"的判断与预览；群发实际走上面的假发送器。
	smtpCfg := config.SMTPConfig{}
	if mailerConfigured {
		smtpCfg = config.SMTPConfig{Host: "smtp.example.com", Port: 465,
			Username: "no-reply@example.com", From: "no-reply@example.com", Password: "secret"}
	}

	srv := New(Deps{
		Config:     cfg,
		Store:      st,
		Channels:   store.NewChannelRepository(st.DB(), cipher),
		Tokens:     store.NewTokenRepository(st.DB(), cipher),
		Users:      users,
		Sessions:   sessions,
		UsageLogs:  store.NewUsageLogRepository(st.DB(), st.Dialect()),
		Settings:   store.NewSettingRepository(st.DB(), st.Dialect()),
		Audit:      store.NewAuditLogRepository(st.DB()),
		Mailer:     mailer.New(smtpCfg),
		Broadcasts: bcasts,
		Broadcast:  sender,
	})

	return &broadcastFixture{
		srv:      srv,
		st:       st,
		users:    users,
		bcasts:   bcasts,
		fake:     fake,
		adminID:  admin.ID,
		adminTok: mustReauthedSession(t, sessions, createTokenGroupSession(t, sessions, admin.ID)),
	}
}

// addUserWithEmail 追加一个启用用户（email 为空表示没填邮箱）。
func (f *broadcastFixture) addUserWithEmail(t *testing.T, username, email string) uint64 {
	t.Helper()
	user := &model.User{
		Username: username, PasswordHash: "test-hash", Email: email,
		Role: model.UserRoleUser, Status: model.UserStatusEnabled,
	}
	if err := f.users.Create(context.Background(), user); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return user.ID
}

// waitBroadcastDone 等待批次处理完（后台 goroutine），最多约 2 秒。
func (f *broadcastFixture) waitBroadcastDone(t *testing.T, id uint64) *model.EmailBroadcast {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bc, err := f.bcasts.GetByID(context.Background(), id)
		if err != nil {
			t.Fatalf("读取批次失败: %v", err)
		}
		if bc.IsFinished() {
			return bc
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("批次 %d 未在预期时间内完成", id)
	return nil
}

// TestBroadcast_必须显式确认才允许发送 覆盖误触防护。
func TestBroadcast_必须显式确认才允许发送(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", true)
	fx.addUserWithEmail(t, "用户甲", "user@example.com")

	// 不传 confirm：即使模板合法也必须拒绝
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts", fx.adminTok,
		`{"template":"billing_line"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未确认应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(body); code != "broadcast_confirm_required" {
		t.Fatalf("错误码应为 broadcast_confirm_required，实际 %q", code)
	}
	// 拒绝的请求不能留下任何批次
	if _, total, err := fx.bcasts.List(context.Background(), 10, 0); err != nil || total != 0 {
		t.Fatalf("被拒的请求不应创建批次，实际 total=%d err=%v", total, err)
	}
}

// TestBroadcast_未知模板被拒 覆盖"模板键必须来自目录"。
func TestBroadcast_未知模板被拒(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", true)

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts", fx.adminTok,
		`{"template":"not-a-template","confirm":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知模板应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(body); code != "broadcast_template_not_found" {
		t.Fatalf("错误码应为 broadcast_template_not_found，实际 %q", code)
	}
}

// TestBroadcast_邮件通道未配置时拒绝 覆盖"没配 SMTP 就别开始群发"。
func TestBroadcast_邮件通道未配置时拒绝(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", false)
	fx.addUserWithEmail(t, "用户甲", "user@example.com")

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts", fx.adminTok,
		`{"template":"billing_line","confirm":true}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("通道未配置应返回 503，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(body); code != "email_service_unavailable" {
		t.Fatalf("错误码应为 email_service_unavailable，实际 %q", code)
	}
	if _, total, _ := fx.bcasts.List(context.Background(), 10, 0); total != 0 {
		t.Fatal("通道未配置时不应创建批次")
	}
}

// TestBroadcast_无人填邮箱时明确报错 覆盖"静默发了 0 封"的坑。
func TestBroadcast_无人填邮箱时明确报错(t *testing.T) {
	// 管理员与用户都没填邮箱
	fx := newBroadcastFixture(t, "", true)
	fx.addUserWithEmail(t, "用户甲", "")

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts", fx.adminTok,
		`{"template":"billing_line","confirm":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无人可发时应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(body); code != "broadcast_no_recipients" {
		t.Fatalf("错误码应为 broadcast_no_recipients，实际 %q", code)
	}
}

// TestBroadcast_发送成功并逐人投递 覆盖"名单入队 + 后台发送"的完整装配。
func TestBroadcast_发送成功并逐人投递(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", true)
	fx.addUserWithEmail(t, "用户甲", "a@example.com")
	fx.addUserWithEmail(t, "用户乙", "b@example.com")
	fx.addUserWithEmail(t, "用户丙", "c@example.com")

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts", fx.adminTok,
		`{"template":"billing_line","confirm":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建群发失败：%d %s", rec.Code, rec.Body.String())
	}
	// 收件人 = 管理员 + 3 个用户（管理员也是本站用户，同样会收到通知）
	if total, _ := body["total"].(float64); int(total) != 4 {
		t.Fatalf("收件人总数应为 4，实际 %v", body["total"])
	}
	subject, _ := body["subject"].(string)
	if subject == "" {
		t.Fatal("响应应带主题，便于管理员确认发的是什么")
	}

	id := uint64(body["id"].(float64))
	done := fx.waitBroadcastDone(t, id)
	if done.Status != model.BroadcastStatusDone {
		t.Fatalf("批次应已完成，实际 %q", done.Status)
	}
	if done.Sent != 4 || done.Failed != 0 {
		t.Fatalf("应 4 封成功、0 失败，实际 %d/%d", done.Sent, done.Failed)
	}
	if fx.fake.count() != 4 {
		t.Fatalf("假发送器应收到 4 封，实际 %d（%v）", fx.fake.count(), fx.fake.sent)
	}

	// 列表与明细接口可用（后台靠它们看进度与失败原因）
	rec, list := doBearerJSON(t, fx.srv, http.MethodGet, "/api/admin/broadcasts", fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查询群发列表失败：%d %s", rec.Code, rec.Body.String())
	}
	if total, _ := list["total"].(float64); int(total) != 1 {
		t.Fatalf("列表应有 1 条记录，实际 %v", list["total"])
	}

	rec, detail := doBearerJSON(t, fx.srv, http.MethodGet,
		"/api/admin/broadcasts/"+strconv.FormatUint(id, 10)+"/recipients", fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查询收件人明细失败：%d %s", rec.Code, rec.Body.String())
	}
	if total, _ := detail["total"].(float64); int(total) != 4 {
		t.Fatalf("明细应有 4 条，实际 %v", detail["total"])
	}
}

// TestBroadcast_预览的守卫分支 覆盖"没填邮箱 / 通道没配"两种情况。
func TestBroadcast_预览的守卫分支(t *testing.T) {
	// 1) 管理员没填邮箱
	fx := newBroadcastFixture(t, "", true)
	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts/preview", fx.adminTok,
		`{"template":"billing_line"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("管理员未填邮箱应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(body); code != "broadcast_admin_email_missing" {
		t.Fatalf("错误码应为 broadcast_admin_email_missing，实际 %q", code)
	}

	// 2) 填了邮箱但邮件通道没配
	fx2 := newBroadcastFixture(t, "admin@example.com", false)
	rec2, body2 := doBearerJSON(t, fx2.srv, http.MethodPost, "/api/admin/broadcasts/preview",
		fx2.adminTok, `{"template":"billing_line"}`)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("通道未配置应返回 503，实际 %d %s", rec2.Code, rec2.Body.String())
	}
	if code := errorCode(body2); code != "email_service_unavailable" {
		t.Fatalf("错误码应为 email_service_unavailable，实际 %q", code)
	}
}

// TestBroadcast_停止与模板目录 覆盖"停止不存在的批次"与模板目录下发。
func TestBroadcast_停止与模板目录(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", true)

	rec, body := doBearerJSON(t, fx.srv, http.MethodGet, "/api/admin/broadcast-templates", fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("查询模板目录失败：%d %s", rec.Code, rec.Body.String())
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("模板目录不应为空（后台下拉需要它）")
	}
	first, _ := items[0].(map[string]any)
	if first["key"] != mailer.BroadcastTemplateBillingLine {
		t.Fatalf("目录首项应为计费专线通知，实际 %v", first["key"])
	}

	rec, _ = doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/broadcasts/999999/cancel", fx.adminTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("停止不存在的批次应返回 404，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestBroadcast_停止进行中的批次 覆盖"停止按钮真的能停下"的接口侧。
func TestBroadcast_停止进行中的批次(t *testing.T) {
	fx := newBroadcastFixture(t, "admin@example.com", true)

	// 直接造一个"进行中"的批次（模拟一次已开始的群发）
	ctx := context.Background()
	bc := &model.EmailBroadcast{
		Template: "billing_line", Subject: "主题", BodyHTML: "<p>正文</p>",
		Status: model.BroadcastStatusRunning, CreatedBy: fx.adminID,
	}
	if err := fx.bcasts.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if _, err := fx.bcasts.AddRecipients(ctx, bc.ID, []*model.EmailBroadcastRecipient{
		{UserID: 1, Email: "a@example.com"},
	}); err != nil {
		t.Fatalf("写入收件人失败: %v", err)
	}

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost,
		"/api/admin/broadcasts/"+strconv.FormatUint(bc.ID, 10)+"/cancel", fx.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("停止批次失败：%d %s", rec.Code, rec.Body.String())
	}
	if status, _ := body["status"].(string); status != string(model.BroadcastStatusCanceled) {
		t.Fatalf("状态应为 canceled，实际 %q", status)
	}

	// 已结束的批次再点停止应被拒（避免界面显示含糊）
	rec, _ = doBearerJSON(t, fx.srv, http.MethodPost,
		"/api/admin/broadcasts/"+strconv.FormatUint(bc.ID, 10)+"/cancel", fx.adminTok, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("重复停止应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}
