// 群发执行器的单元测试。
//
// 测试重点（每一条都对应一种"发错/发重"的真实事故）：
//   - 名单过滤：空邮箱跳过、同一邮箱只发一次、被禁用的用户不发；
//   - 逐人状态：成功的记 sent、失败的记 failed 且带原因；
//   - 绝不重发：已 sent 的人再跑一次不会再收到（断点续发的正确性）；
//   - 可停止：批次被置为 canceled 后，后续不再发送。
//
// 测试里把节流参数置 0：生产节奏是"2 秒一封 + 每 50 封停 30 秒"，
// 单测要验证的是行为正确性，不是等待时间。
package broadcast

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// fakeMailer 记录成功发送的地址，并可按地址注入失败。
type fakeMailer struct {
	sent   []string
	bodies []string
	failOn map[string]error
}

func (f *fakeMailer) Send(_ context.Context, to, _, body string) error {
	if err, ok := f.failOn[to]; ok {
		return err
	}
	f.sent = append(f.sent, to)
	f.bodies = append(f.bodies, body)
	return nil
}

// newTestSender 构造"零等待"的执行器与配套仓储。
func newTestSender(t *testing.T, mailer Mailer) (*Sender, model.EmailBroadcastRepository, model.UserRepository) {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), "broadcast_test.db")
	st, err := store.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	broadcasts := store.NewEmailBroadcastRepository(st.DB())
	users := store.NewUserRepository(st.DB())

	sender := New(broadcasts, users, mailer, Options{
		// 极小节奏：单测要验证行为正确性（含跨批、批间停顿分支），不是等待时间
		Interval:   time.Millisecond,
		BatchSize:  2,
		BatchPause: time.Millisecond,
	})
	return sender, broadcasts, users
}

// addUser 写一个测试用户。
func addUser(t *testing.T, users model.UserRepository, username, email string, status model.UserStatus) uint64 {
	t.Helper()
	user := &model.User{
		Username:     username,
		PasswordHash: "test-hash",
		Email:        email,
		Role:         model.UserRoleUser,
		Status:       status,
	}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("创建用户 %s 失败: %v", username, err)
	}
	return user.ID
}

// enabledQuery 是"只发给启用用户"的收件人筛选条件（与后台接口一致）。
func enabledQuery() model.UserQuery {
	status := model.UserStatusEnabled
	return model.UserQuery{Status: &status}
}

// TestSender_Enqueue_过滤空邮箱与重复邮箱 覆盖"名单必须先洗一遍"。
func TestSender_Enqueue_过滤空邮箱与重复邮箱(t *testing.T) {
	sender, broadcasts, users := newTestSender(t, &fakeMailer{})
	ctx := context.Background()

	addUser(t, users, "有邮箱", "alice@example.com", model.UserStatusEnabled)
	addUser(t, users, "邮箱大小写不同但同一个人", "Alice@Example.com", model.UserStatusEnabled)
	addUser(t, users, "没邮箱", "", model.UserStatusEnabled)
	addUser(t, users, "被禁用", "banned@example.com", model.UserStatusDisabled)
	addUser(t, users, "另一个有邮箱", "bob@example.com", model.UserStatusEnabled)

	bc := &model.EmailBroadcast{Template: "t", Subject: "s", BodyHTML: "<p>b</p>"}
	if err := broadcasts.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}

	total, err := sender.Enqueue(ctx, bc, enabledQuery())
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	// alice@（与 Alice@ 去重）+ bob@ = 2 人；空邮箱与被禁用的都不在名单里
	if total != 2 {
		t.Fatalf("入队人数应为 2，实际 %d", total)
	}

	// 重复调用必须幂等（点两次"发送"不会让人收到两封）
	again, err := sender.Enqueue(ctx, bc, enabledQuery())
	if err != nil {
		t.Fatalf("重复入队失败: %v", err)
	}
	if again != 0 {
		t.Fatalf("重复入队应新增 0 人，实际 %d", again)
	}

	count, err := broadcasts.CountRecipients(ctx, bc.ID, "")
	if err != nil {
		t.Fatalf("统计收件人失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("收件人明细应为 2 条，实际 %d", count)
	}
}

// TestSender_Run_逐人发送_失败落库 覆盖发送与失败记账。
func TestSender_Run_逐人发送_失败落库(t *testing.T) {
	mailer := &fakeMailer{failOn: map[string]error{
		"broken@example.com": errors.New("550 mailbox unavailable"),
	}}
	sender, broadcasts, users := newTestSender(t, mailer)
	ctx := context.Background()

	addUser(t, users, "甲", "ok1@example.com", model.UserStatusEnabled)
	addUser(t, users, "乙", "broken@example.com", model.UserStatusEnabled)
	addUser(t, users, "丙", "ok2@example.com", model.UserStatusEnabled)

	bc := &model.EmailBroadcast{Template: "t", Subject: "主题", BodyHTML: "<p>正文</p>"}
	if err := broadcasts.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if _, err := sender.Enqueue(ctx, bc, enabledQuery()); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	if err := sender.Run(ctx, bc); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// 成功 2、失败 1；失败的不重试（重试无意义且恶化发信声誉）
	if len(mailer.sent) != 2 {
		t.Fatalf("应成功发送 2 封，实际 %d（%v）", len(mailer.sent), mailer.sent)
	}
	// 发出去的内容必须与批次快照逐字一致
	for _, body := range mailer.bodies {
		if body != bc.BodyHTML {
			t.Fatalf("邮件正文应与批次快照一致，实际 %q", body)
		}
	}

	updated, err := broadcasts.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if updated.Sent != 2 || updated.Failed != 1 {
		t.Fatalf("进度应为 2 成功 / 1 失败，实际 %d/%d", updated.Sent, updated.Failed)
	}
	if updated.Status != model.BroadcastStatusDone {
		t.Fatalf("发完应置 done，实际 %q", updated.Status)
	}
	if updated.StartedAt.IsZero() || updated.FinishedAt.IsZero() {
		t.Fatal("完成时应同时具备开始与结束时间")
	}

	failedRows, _, err := broadcasts.ListRecipients(ctx, bc.ID, model.RecipientStatusFailed, 10, 0)
	if err != nil {
		t.Fatalf("查询失败明细失败: %v", err)
	}
	if len(failedRows) != 1 || !strings.Contains(failedRows[0].Error, "550") {
		t.Fatalf("失败明细应带原因，实际 %+v", failedRows)
	}
}

// TestSender_Run_已发过的不再重发 覆盖断点续发的核心保证。
func TestSender_Run_已发过的不再重发(t *testing.T) {
	mailer := &fakeMailer{}
	sender, broadcasts, users := newTestSender(t, mailer)
	ctx := context.Background()

	addUser(t, users, "甲", "a@example.com", model.UserStatusEnabled)
	addUser(t, users, "乙", "b@example.com", model.UserStatusEnabled)

	bc := &model.EmailBroadcast{Template: "t", Subject: "s", BodyHTML: "<p>b</p>"}
	if err := broadcasts.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if _, err := sender.Enqueue(ctx, bc, enabledQuery()); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	if err := sender.Run(ctx, bc); err != nil {
		t.Fatalf("首次发送失败: %v", err)
	}
	first := len(mailer.sent)
	if first != 2 {
		t.Fatalf("首次应发 2 封，实际 %d", first)
	}

	// 再跑一次（模拟"重启后 ResumeAll 又跑了同一批次"）：一封都不能多发
	reloaded, err := broadcasts.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	reloaded.Status = model.BroadcastStatusRunning // 故意伪装成"未完成"，模拟中断后重启
	if err := sender.Run(ctx, reloaded); err != nil {
		t.Fatalf("二次发送失败: %v", err)
	}
	if len(mailer.sent) != first {
		t.Fatalf("已发过的人不应再收到，实际从 %d 变为 %d", first, len(mailer.sent))
	}
}

// TestSender_Run_中途停止后不再发送 覆盖"停止按钮要真能停下来"。
func TestSender_Run_中途停止后不再发送(t *testing.T) {
	mailer := &fakeMailer{}
	sender, broadcasts, users := newTestSender(t, mailer)
	ctx := context.Background()

	addUser(t, users, "甲", "a@example.com", model.UserStatusEnabled)

	bc := &model.EmailBroadcast{Template: "t", Subject: "s", BodyHTML: "<p>b</p>"}
	if err := broadcasts.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if _, err := sender.Enqueue(ctx, bc, enabledQuery()); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	// 管理员在发送前点了"停止"
	if err := broadcasts.UpdateProgress(ctx, bc.ID, model.BroadcastStatusCanceled, 1, 0, 0, time.Time{}, time.Time{}); err != nil {
		t.Fatalf("置为已停止失败: %v", err)
	}

	stopped, err := broadcasts.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if err := sender.Run(ctx, stopped); err != nil {
		t.Fatalf("对已停止批次调用发送不应报错: %v", err)
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("已停止的批次不应再发出任何邮件，实际 %d", len(mailer.sent))
	}
}

// TestSender_同一批次不会被并发发送 覆盖"接口被连点两次"的防护。
func TestSender_同一批次不会被并发发送(t *testing.T) {
	sender := New(nil, nil, &fakeMailer{}, Options{})

	if !sender.acquire(1) {
		t.Fatal("首次占用应成功")
	}
	if sender.acquire(1) {
		t.Fatal("同一批次第二次占用应失败（否则同一批用户会各收到两封）")
	}
	sender.release(1)
	if !sender.acquire(1) {
		t.Fatal("释放后应能再次占用")
	}
}
