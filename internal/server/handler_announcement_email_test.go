// 「发布公告同步发邮件到绑定邮箱」的单元测试。
//
// 意图（Why）：
//
//	这项功能有两类失败都会造成严重后果，而且都发生得悄无声息：
//	  1) 【邮件不通却拖垮公告发布】：SMTP 没配好、群发模块没启用时，
//	     必须照常发布公告并明确告知"邮件没发出去"；若反过来报错，
//	     管理员会丢失刚写完的正文（公告本身才是业务动作）；
//	  2) 【以为发出去了其实没有】：草稿公告、定时未到点的公告都不该发信
//	     （它们此时在站点上根本看不见，提前邮出去等于泄露未上线内容）。
//	这两条规则无法靠代码阅读保证（改一处 guard 就失效），必须钉成用例。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run AnnouncementEmail
//
// 扩展（Extend）：
//
//	将来支持"定时公告到点自动发信"时，本文件的"定时发布不发信"用例
//	应当被替换为"到点由调度器触发"，而不是简单删掉它。
package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/broadcast"
	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/mailer"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// fakeMailer 记录被发出的邮件内容，替代真实 SMTP。
//
// 之所以用假实现而不是连真实邮箱：群发会跑到几十分钟，用例不能依赖外部服务；
// 而且我们要断言的是"有没有真的发出去、内容是什么"，而非 SMTP 本身是否通。
type fakeMailer struct {
	mu   sync.Mutex
	sent []string // 每封拼成 "to|subject" 便于断言与去重统计
}

func (f *fakeMailer) Send(ctx context.Context, to, subject, htmlBody string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to+"|"+subject)
	return nil
}

func (f *fakeMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// newMailReadyFixture 在公告固件的基础上补齐"邮件可用"所需的依赖。
//
// 返回群发执行器与假发送器：前者注入到 srv.deps，后者用于断言实际投递情况。
func newMailReadyFixture(t *testing.T) (*announcementFixture, *fakeMailer) {
	t.Helper()

	fx := newAnnouncementFixture(t)
	broadcasts := store.NewEmailBroadcastRepository(fx.srv.deps.Store.DB())

	// 邮件通道"已配置"的必要条件： Passthrough 检查只看 Configured()，
	// 因此这里给一套完整但不会被真正调用的参数（真正的投递走下面的 fakeMailer）。
	readyMailer := mailer.New(config.SMTPConfig{
		Host: "127.0.0.1", Port: 25, Username: "tester",
		Password: "tester-password", From: "noreply@example.com",
	})
	fx.srv.deps.Mailer = readyMailer
	fx.srv.deps.Broadcasts = broadcasts

	fake := &fakeMailer{}
	// 间隔设为极小：真实的每封间隔是 2 秒，用例等不起。
	sender := broadcast.New(broadcasts, fx.srv.deps.Users, fake, broadcast.Options{
		Interval: time.Millisecond, BatchSize: 1, BatchPause: time.Millisecond,
	})
	fx.srv.deps.Broadcast = sender

	return fx, fake
}

// seedUserWithEmail 造一个绑定了邮箱的启用用户（群发名单的构成单位）。
func seedUserWithEmail(t *testing.T, fx *announcementFixture, username, email string) {
	t.Helper()
	if err := fx.srv.deps.Users.Create(context.Background(), &model.User{
		Username: username, PasswordHash: "test-hash", Email: email,
		Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
	}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
}

// TestAnnouncementEmail_发布时同步发出邮件 覆盖主链路。
func TestAnnouncementEmail_发布时同步发出邮件(t *testing.T) {
	fx, fake := newMailReadyFixture(t)
	seedUserWithEmail(t, fx, "mail-user-1", "one@example.com")
	seedUserWithEmail(t, fx, "mail-user-2", "two@example.com")

	rec, body := doAnnouncementJSON(t, fx.srv, http.MethodPost, "/api/admin/announcements", fx.adminTok,
		`{"title":"计费专线调整","content":"本月起按量计费，详见控制台。","level":"warning","notify_email":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("发布公告应返回 200，实际 %d，body = %v", rec.Code, body)
	}
	if id, _ := body["mail_broadcast_id"].(float64); id <= 0 {
		t.Fatalf("应回传群发批次号供管理员查看进度，实际 %v", body["mail_broadcast_id"])
	}
	// 管理员 + 两个绑定邮箱的用户 = 3 位收件人（管理员那条粉丝构造函数未设邮箱，故不计）
	if got, _ := body["mail_recipients"].(float64); got != 2 {
		t.Fatalf("收件人数 = %v，期望 2（只发给绑定了邮箱的启用用户）", body["mail_recipients"])
	}

	// 群发是异步的：轮询等待投递完成（假实现毫秒级，最多等 3 秒足够）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fake.count() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("实际投递 %d 封，期望 2 封", got)
	}
	if strings.TrimSpace(fake.sent[0]) == "" || !strings.Contains(fake.sent[0], "计费专线调整") {
		t.Errorf("邮件主题应包含公告标题，实际 %q", fake.sent[0])
	}
}

// TestAnnouncementEmail_邮件未就绪时公告照常发布并告知 覆盖"不因邮件拖垮业务"。
func TestAnnouncementEmail_邮件未就绪时公告照常发布并告知(t *testing.T) {
	fx := newAnnouncementFixture(t) // 未注入 Mailer / Broadcast：邮件能力不可用

	rec, body := doAnnouncementJSON(t, fx.srv, http.MethodPost, "/api/admin/announcements", fx.adminTok,
		`{"title":"停机维护通知","content":"今晚 23:00 维护。","notify_email":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("SMTP 不可用时也必须能发布公告（邮件只是附加渠道），实际 %d，body = %v", rec.Code, body)
	}
	if body["title"] != "停机维护通知" {
		t.Errorf("公告未正确保存：%v", body)
	}
	if id, _ := body["mail_broadcast_id"].(float64); id != 0 {
		t.Errorf("邮件未发出时不该有批次号，实际 %v", id)
	}
	msg, _ := body["mail_message"].(string)
	if msg == "" {
		t.Fatal("邮件未发出必须在 mail_message 里说明原因——否则管理员会以为用户已经收到了")
	}
	if !strings.Contains(msg, "未发送") {
		t.Errorf("提示信息应明确说明邮件未发送，实际 %q", msg)
	}
}

// TestAnnouncementEmail_草稿与定时公告不发信 覆盖"不在站点显示的内容不外泄"。
func TestAnnouncementEmail_草稿与定时公告不发信(t *testing.T) {
	fx, fake := newMailReadyFixture(t)
	seedUserWithEmail(t, fx, "mail-user-3", "three@example.com")

	// 草稿（enabled=false）
	rec, body := doAnnouncementJSON(t, fx.srv, http.MethodPost, "/api/admin/announcements", fx.adminTok,
		`{"title":"草稿公告","content":"还没定稿","enabled":false,"notify_email":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存草稿应返回 200，实际 %d，body = %v", rec.Code, body)
	}
	if n, _ := body["mail_recipients"].(float64); n != 0 {
		t.Fatalf("草稿公告不应发信，实际收件人 %v", n)
	}

	// 定时到未来发布
	future := time.Now().Add(2 * time.Hour).Unix()
	rec, body = doAnnouncementJSON(t, fx.srv, http.MethodPost, "/api/admin/announcements", fx.adminTok,
		`{"title":"定时公告","content":"两小时后上线","publish_at":`+strconv.FormatInt(future, 10)+`,"notify_email":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("定时公告应返回 200，实际 %d，body = %v", rec.Code, body)
	}
	if n, _ := body["mail_recipients"].(float64); n != 0 {
		t.Fatalf("未到点的定时公告不应发信（站点上还看不见它），实际收件人 %v", n)
	}

	if got := fake.count(); got != 0 {
		t.Errorf("本用例不应发出任何邮件，实际 %d 封", got)
	}
}
