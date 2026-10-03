// userRepository 邮箱唯一性的单元测试。
//
// 意图（Why）：
//
//	"一个邮箱只能绑定一个账号"是站点对外的硬承诺（注册验证码、通知投递、
//	后续邮箱登录都依赖它）。这条约束的最终裁决者是数据库部分唯一索引
//	（迁移 0036），而仓库层负责把索引冲突翻译成领域错误 ErrEmailTaken。
//	本文件把三层行为都锁死：
//	  1) 重复邮箱被 Create 拒绝且返回 ErrEmailTaken（不是 ErrUsernameTaken）；
//	  2) Update 把邮箱改到他人已占用邮箱时同样返回 ErrEmailTaken；
//	  3) 空邮箱不参与约束（邮箱是可选字段），多账号留空邮箱合法。
//
// 流转（Flow）：
//
//	newTestStore → Users.Create/Update/GetByEmail → 断言错误与查询结果
//
// 扩展（Extend）：
//
//	新增"按邮箱定位用户"的功能时，先在这里补 GetByEmail 用例，再动仓库实现。
package store

import (
	"context"
	"errors"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newValidUser 构造一个能通过领域校验的合法用户，便于把焦点放在"邮箱"而非其它字段。
//
// Quota 用 0（而非默认的 QuotaUnlimited）：避免与"不限额度"语义耦合，
// 本测试只关心唯一性，不关心额度。
func newValidUser(username, email string) *model.User {
	return &model.User{
		Username:     username,
		PasswordHash: "test-hash",
		Email:        email,
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        0,
	}
}

// TestUserCreate_重复邮箱_返回ErrEmailTaken 验证邮箱唯一索引挡住重复绑定。
//
// 关键断言：必须返回 ErrEmailTaken，而不是 ErrUsernameTaken。
// 两者对使用者的提示完全不同（"换邮箱" vs "换用户名"），
// 若错误混淆，前端只能给出笼统的"注册失败"，用户无从知道该改哪一项。
func TestUserCreate_重复邮箱_返回ErrEmailTaken(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()

	first := newValidUser("alice", "alice@example.com")
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("首次创建用户失败: %v", err)
	}

	second := newValidUser("bob", "alice@example.com")
	err := repo.Create(ctx, second)
	if err == nil {
		t.Fatal("重复邮箱应被拒绝，实际创建成功")
	}
	if !errors.Is(err, model.ErrEmailTaken) {
		t.Fatalf("重复邮箱应返回 ErrEmailTaken，实际: %v", err)
	}
	if errors.Is(err, model.ErrUsernameTaken) {
		t.Fatal("重复邮箱不应被误报为 ErrUsernameTaken")
	}
}

// TestUserCreate_大小写变体邮箱_同样被拒绝 验证归一化口径贯穿入库。
//
// 归一化（去空白 + 转小写）发生在 handler 层（NormalizeEmail），
// 仓库层拿到的是已归一化的值。本用例模拟"handler 层已归一化"的场景，
// 验证只要归一化后的字符串相同，唯一索引就会生效。
func TestUserCreate_大小写变体邮箱_同样被拒绝(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()

	// 两者经 NormalizeEmail 归一化后完全相同，唯一索引必须拦住后者
	lower := newValidUser("alice", model.NormalizeEmail("Alice@Example.COM"))
	if err := repo.Create(ctx, lower); err != nil {
		t.Fatalf("首次创建用户失败: %v", err)
	}
	dup := newValidUser("bob", model.NormalizeEmail("  alice@example.com  "))
	if err := repo.Create(ctx, dup); !errors.Is(err, model.ErrEmailTaken) {
		t.Fatalf("归一化后相同的邮箱应被唯一索引拒绝，实际: %v", err)
	}
}

// TestUserUpdate_改成他人邮箱_返回ErrEmailTaken 验证更新路径同样受唯一索引约束。
func TestUserUpdate_改成他人邮箱_返回ErrEmailTaken(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()

	a := newValidUser("alice", "alice@example.com")
	b := newValidUser("bob", "bob@example.com")
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("创建 alice 失败: %v", err)
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("创建 bob 失败: %v", err)
	}

	b.Email = "alice@example.com"
	err := repo.Update(ctx, b)
	if !errors.Is(err, model.ErrEmailTaken) {
		t.Fatalf("改成他人邮箱应返回 ErrEmailTaken，实际: %v", err)
	}
}

// TestUserCreate_多个空邮箱_允许共存 验证空邮箱不参与唯一约束。
//
// 邮箱是可选字段（部分唯一索引带 WHERE email 不等于空串 的过滤），
// 大量"未绑定邮箱"的用户共存是合法且必须的，不能因空串互相冲突。
func TestUserCreate_多个空邮箱_允许共存(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()

	for i, username := range []string{"a", "b", "c"} {
		u := newValidUser(username, "")
		u.Username = "no-email-" + username
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("第 %d 个空邮箱用户应创建成功，实际失败: %v", i+1, err)
		}
	}
}

// TestUserGetByEmail_按邮箱定位与空串拒查 覆盖 GetByEmail 的两条路径。
func TestUserGetByEmail_按邮箱定位与空串拒查(t *testing.T) {
	st := newTestStore(t)
	repo := NewUserRepository(st.DB())
	ctx := context.Background()

	created := newValidUser("alice", "alice@example.com")
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 命中：大小写变体也应能找到（GetByEmail 内部归一化）
	got, err := repo.GetByEmail(ctx, "Alice@Example.COM")
	if err != nil {
		t.Fatalf("按邮箱（大小写变体）查询失败: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetByEmail 命中 ID = %d，期望 %d", got.ID, created.ID)
	}

	// 空邮箱一律返回 ErrUserNotFound（空串在库中大量共存，按空串查询无意义）
	if _, err := repo.GetByEmail(ctx, ""); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("空邮箱应返回 ErrUserNotFound，实际: %v", err)
	}
	// 不存在的邮箱同样返回 ErrUserNotFound
	if _, err := repo.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Errorf("不存在的邮箱应返回 ErrUserNotFound，实际: %v", err)
	}
}

// TestMigrate0036_清理重复邮箱并建索引 用"手工插入重复邮箱"模拟老数据，
// 验证迁移 0036 的去重与部分唯一索引真正生效。
//
// 为什么不用全量 Migrate：本测试手工建的"老表"已经带上了 0021 的
// invite_code/inviter_id 列，若执行全量迁移会在 0021 报"duplicate column"。
// 因此这里直接执行 0036 的 SQL 语句（去重 + 建索引），只验证这一个迁移的效果。
func TestMigrate0036_清理重复邮箱并建索引(t *testing.T) {
	dsn := t.TempDir() + "/legacy.db"
	st, err := Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// 手工建"0036 之前"的 users 表（带 0021 的邀请列，但不带 0036 的索引）
	if _, err := st.DB().ExecContext(ctx, `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			role INTEGER NOT NULL DEFAULT 1,
			status INTEGER NOT NULL DEFAULT 1,
			quota INTEGER NOT NULL DEFAULT 0,
			used_quota INTEGER NOT NULL DEFAULT 0,
			invite_code TEXT NOT NULL DEFAULT '',
			inviter_id INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		t.Fatalf("建模拟老表失败: %v", err)
	}
	// 三个账号绑同一个邮箱（id 依次增大），另有一个大小写变体、一个空邮箱
	legacy := []struct{ username, email string }{
		{"u1", "dup@example.com"},     // id=1，应保留
		{"u2", "dup@example.com"},     // id=2，应清空
		{"u3", "  DUP@EXAMPLE.COM  "}, // id=3，归一化后与上面同，应清空
		{"u4", "solo@example.com"},    // id=4，唯一，应保留
		{"u5", ""},                    // id=5，空邮箱，不参与约束，应保留
	}
	for _, it := range legacy {
		if _, err := st.DB().ExecContext(ctx,
			"INSERT INTO users (username, password_hash, email) VALUES (?, 'h', ?)",
			it.username, it.email); err != nil {
			t.Fatalf("插入模拟数据失败: %v", err)
		}
	}

	// 执行 0036 的 SQL（归一化 → 去重 → 建部分唯一索引）
	migration0036 := `
		UPDATE users SET email = LOWER(TRIM(email)) WHERE email <> '';
		UPDATE users SET email = ''
		WHERE email <> ''
		  AND id NOT IN (SELECT MIN(id) FROM users WHERE email <> '' GROUP BY email);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users (email) WHERE email <> '';
	`
	if _, err := st.DB().ExecContext(ctx, migration0036); err != nil {
		t.Fatalf("执行迁移 0036 失败: %v", err)
	}

	// 断言去重结果
	rows := map[string]string{}
	rs, err := st.DB().QueryContext(ctx, "SELECT username, email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var u, e string
		if err := rs.Scan(&u, &e); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		rows[u] = e
	}
	if rows["u1"] != "dup@example.com" {
		t.Errorf("u1 应保留 dup@example.com，实际 %q", rows["u1"])
	}
	if rows["u2"] != "" {
		t.Errorf("u2 的邮箱应被清空，实际 %q", rows["u2"])
	}
	if rows["u3"] != "" {
		t.Errorf("u3（大小写变体）的邮箱应被清空，实际 %q", rows["u3"])
	}
	if rows["u4"] != "solo@example.com" {
		t.Errorf("u4 的邮箱应原样保留，实际 %q", rows["u4"])
	}
	if rows["u5"] != "" {
		t.Errorf("u5 的空邮箱应保留，实际 %q", rows["u5"])
	}

	// 索引生效：再插入同邮箱应被拒绝
	if _, err := st.DB().ExecContext(ctx,
		"INSERT INTO users (username, password_hash, email) VALUES ('u6', 'h', 'dup@example.com')"); err == nil {
		t.Error("唯一索引应拒绝重复邮箱插入，实际成功")
	}
	// 但空邮箱仍可共存
	if _, err := st.DB().ExecContext(ctx,
		"INSERT INTO users (username, password_hash, email) VALUES ('u7', 'h', '')"); err != nil {
		t.Errorf("空邮箱应允许多个共存，实际报错: %v", err)
	}
}
