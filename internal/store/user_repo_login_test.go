// 登录安全字段（失败锁定 / 登录来源 / 会话来源与二次验证）的仓储测试。
//
// 意图（Why）：
//
//	这三类字段是"账号被撞库时最后的防线"，行为一旦写歪不会报错、
//	只会在真的被撞时静默失效。因此本文件把边界锁死：
//	  1) 未到阈值不锁定（正常人忘密码试几次不能被关小黑屋）；
//	  2) 达到阈值必须锁定且锁期来自 model 常量；
//	  3) 成功登录必须清零计数与锁定，并记下来源 IP；
//	  4) 资料更新（整行写回）不得覆盖登录安全字段——这是 Update 的老陷阱；
//	  5) 会话要能记下 IP/UA（超长 UA 截断）并刷新二次验证时刻。
//
// 流转（Flow）：
//
//	newTestStore → NewUserRepository/NewSessionRepository → RecordLoginSuccess /
//	RegisterLoginFailure / Create / UpdateReauth → 断言字段与锁定判定
//
// 扩展（Extend）：
//
//	改锁定策略（阈值/时长）时：先改 model 常量，再回来核对本文件的断言
//	是否仍与新常量一致（这里刻意引用 model 常量而非写死数字）。
package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// lockTestUser 建一个用户并返回 id，供各用例复用。
func lockTestUser(t *testing.T, repo model.UserRepository) uint64 {
	t.Helper()
	u := newValidUser("lock-user", "lock@example.com")
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u.ID
}

// TestRegisterLoginFailure_未达阈值不锁定 验证"忘密码多试几次"不会被锁。
func TestRegisterLoginFailure_未达阈值不锁定(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, repo)
	now := time.Now()

	for i := 1; i < model.LoginFailureLockThreshold; i++ {
		failed, lockedUntil, err := repo.RegisterLoginFailure(ctx, id, now)
		if err != nil {
			t.Fatalf("第 %d 次记录失败出错: %v", i, err)
		}
		if failed != i {
			t.Fatalf("失败计数 = %d，期望 %d", failed, i)
		}
		if lockedUntil.After(now) {
			t.Fatalf("第 %d 次失败就锁定了（阈值 %d）", i, model.LoginFailureLockThreshold)
		}
	}

	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if u.IsLocked(now) {
		t.Error("未达阈值却处于锁定状态")
	}
}

// TestRegisterLoginFailure_达到阈值锁定 验证第 N 次失败即锁、锁期取自 model 常量。
func TestRegisterLoginFailure_达到阈值锁定(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, repo)
	now := time.Now()

	var failed int
	var lockedUntil time.Time
	var err error
	for i := 0; i < model.LoginFailureLockThreshold; i++ {
		failed, lockedUntil, err = repo.RegisterLoginFailure(ctx, id, now)
		if err != nil {
			t.Fatalf("第 %d 次记录失败出错: %v", i+1, err)
		}
	}

	if failed != model.LoginFailureLockThreshold {
		t.Fatalf("失败计数 = %d，期望 %d", failed, model.LoginFailureLockThreshold)
	}
	if !lockedUntil.After(now) {
		t.Fatal("达到阈值后应处于锁定中")
	}
	if remain := lockedUntil.Sub(now); remain < model.LoginFailureLockDuration-time.Minute ||
		remain > model.LoginFailureLockDuration {
		t.Errorf("剩余锁定时长 = %v，期望约 %v", remain, model.LoginFailureLockDuration)
	}

	// 锁定在用户行上可被读到（登录路径就是读它来判定的）
	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if !u.IsLocked(now) {
		t.Error("GetByID 读到的用户未反映锁定状态")
	}
	if u.LockRemain(now) <= 0 {
		t.Error("LockRemain 应为正数")
	}
}

// TestRecordLoginSuccess_清零并记录来源 验证成功登录同时重置锁定与计数。
//
// 这是"用户被锁后改用正确密码/等锁到期"的自愈路径：
// 计数不清零会让下一次正常输错直接续上旧计数，等同于变相延长锁定。
func TestRecordLoginSuccess_清零并记录来源(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, repo)
	now := time.Now()

	for i := 0; i < model.LoginFailureLockThreshold; i++ {
		if _, _, err := repo.RegisterLoginFailure(ctx, id, now); err != nil {
			t.Fatalf("记录失败出错: %v", err)
		}
	}
	if err := repo.RecordLoginSuccess(ctx, id, "203.0.113.9", now); err != nil {
		t.Fatalf("记录成功登录失败: %v", err)
	}

	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if u.FailedLogins != 0 {
		t.Errorf("失败计数 = %d，期望清零", u.FailedLogins)
	}
	if u.IsLocked(now) {
		t.Error("成功登录后仍处于锁定")
	}
	if u.LastLoginIP != "203.0.113.9" {
		t.Errorf("最近登录 IP = %q，期望记录来源", u.LastLoginIP)
	}
	if !u.LastLoginAt.Equal(now.Truncate(time.Second)) {
		t.Errorf("最近登录时间 = %v，期望 %v", u.LastLoginAt, now.Truncate(time.Second))
	}
}

// TestRecordLoginSuccess_空IP记为unknown 验证拿不到来源时不会写入空串。
//
// 空串是"未知"的哨兵值，若把它当正常值写进去，
// 异地提醒就会把"上次未知"当成"和上次一样"而漏报。
func TestRecordLoginSuccess_空IP记为unknown(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, repo)

	if err := repo.RecordLoginSuccess(ctx, id, "", time.Now()); err != nil {
		t.Fatalf("记录成功登录失败: %v", err)
	}
	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if u.LastLoginIP == "" {
		t.Error("空 IP 应记为 unknown，实际是空串（会被误判成'与上次相同'）")
	}
}

// TestUserUpdate_不覆盖登录安全字段 验证资料整行写回不会动登录计数。
//
// Update 是"读到什么写回什么"，若把登录字段也塞进 UPDATE 列表，
// 管理员改个昵称就能把并发中的失败计数清零——等于给撞库开了后门。
func TestUserUpdate_不覆盖登录安全字段(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, repo)
	now := time.Now()

	// 先取一份"锁定之前"的副本，模拟真实竞态（登录路径持旧读、管理员同时改资料）
	stale, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}

	for i := 0; i < model.LoginFailureLockThreshold; i++ {
		if _, _, err := repo.RegisterLoginFailure(ctx, id, now); err != nil {
			t.Fatalf("记录失败出错: %v", err)
		}
	}

	stale.Username = "lock-user-renamed"
	if err := repo.Update(ctx, stale); err != nil {
		t.Fatalf("更新资料失败: %v", err)
	}

	u, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if u.Username != "lock-user-renamed" {
		t.Errorf("资料未更新，实际 %q", u.Username)
	}
	if !u.IsLocked(now) {
		t.Error("资料更新把锁定状态冲掉了")
	}
	if u.FailedLogins != model.LoginFailureLockThreshold {
		t.Errorf("失败计数 = %d，期望仍为 %d", u.FailedLogins, model.LoginFailureLockThreshold)
	}
}

// TestSession_记录来源与二次验证 会话要记住签发来源，并能刷新验证时刻。
func TestSession_记录来源与二次验证(t *testing.T) {
	st := newTestStore(t)
	users := NewUserRepository(st.DB())
	sessions := NewSessionRepository(st.DB())
	ctx := context.Background()
	id := lockTestUser(t, users)

	// 超长 UA：必须被截断，否则客户端能用一个头把行撑大
	longUA := strings.Repeat("x", model.UserAgentMaxLength*2)
	sess := &model.Session{
		UserID:    id,
		TokenHash: "hash-来源测试",
		ExpiresAt: time.Now().Add(time.Hour),
		IP:        "198.51.100.7",
		UserAgent: longUA,
	}
	if err := sessions.Create(ctx, sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	got, err := sessions.GetByTokenHash(ctx, sess.TokenHash)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got.IP != "198.51.100.7" {
		t.Errorf("会话 IP = %q，期望记录签发来源", got.IP)
	}
	if len(got.UserAgent) != model.UserAgentMaxLength {
		t.Errorf("会话 UA 长度 = %d，期望截断到 %d", len(got.UserAgent), model.UserAgentMaxLength)
	}
	if !got.ReauthAt.IsZero() {
		t.Error("新会话的二次验证时刻应为零值")
	}
	if got.IsReauthFresh(time.Now(), time.Minute) {
		t.Error("从未验证过的会话不应被视为已验证")
	}

	// 刷新二次验证时刻
	if err := sessions.UpdateReauth(ctx, got.ID, time.Now()); err != nil {
		t.Fatalf("刷新二次验证失败: %v", err)
	}
	got, err = sessions.GetByTokenHash(ctx, sess.TokenHash)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if !got.IsReauthFresh(time.Now(), time.Minute) {
		t.Error("刚验证过的会话应在窗口内视为已验证")
	}
	if got.IsReauthFresh(time.Now().Add(2*time.Minute), time.Minute) {
		t.Error("超出窗口后不应再视为已验证")
	}
}
