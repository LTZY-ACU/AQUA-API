// 限时试用额台账（trial_grants）仓储的单元测试。
//
// 意图（Why）：
//
//	试用额是"发出去还会收回来"的额度，最容易出的两类事故都在这里钉死：
//	  1) 多发：同一批次被重复执行，全站每人拿两份（要等 24 小时才收得回）；
//	  2) 误扣：到期回收时把**用户自己充的钱**一起扣走。
//	第二条尤其隐蔽——它依赖"发放瞬间的 used_quota 快照"才能算对，
//	所以本文件把三种花法（没花 / 花一半 / 全花完）逐一验证。
//
// 流转（Flow）：
//
//	go test ./internal/store/ -run TrialGrant
//	  └─ 在真实 SQLite 上跑事务，验证记账口径（mock 证明不了余额变化）
//
// 扩展（Extend）：
//
//	新增发放维度（如只发给新用户）时，在 TestTrialGrantRepository_发放_* 补断言；
//	改动回收口径时，必须同步更新本文件的三个"花法"用例。
package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newTrialGrantFixture 在同一个临时库上构造试用额台账与用户仓储。
//
// 一并返回 *sql.DB：本文件要断言 used_baseline / reclaimed_amount 这类
// 台账内部字段，它们没有对外查询接口，只能直接读库核对。
func newTrialGrantFixture(t *testing.T) (model.TrialGrantRepository, model.UserRepository, *sql.DB) {
	t.Helper()
	st := newTestStore(t)
	return NewTrialGrantRepository(st.DB()), NewUserRepository(st.DB()), st.DB()
}

// reloadUser 读回用户当前的额度状态（断言余额变化用）。
func reloadUser(t *testing.T, users model.UserRepository, id uint64) *model.User {
	t.Helper()
	u, err := users.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("读取用户 %d 失败: %v", id, err)
	}
	return u
}

// TestTrialGrantRepository_发放_只发给启用且额度非不限的用户 覆盖发放范围与快照。
func TestTrialGrantRepository_发放_只发给启用且额度非不限的用户(t *testing.T) {
	ctx := context.Background()
	grants, users, db := newTrialGrantFixture(t)

	normal := newQuotaUser(t, users, 5_000, "trial-normal")
	disabled := newQuotaUser(t, users, 5_000, "trial-disabled")
	// 直接改库置为禁用：这里要验证的是"发放 SQL 会不会跳过禁用用户"，
	// 走仓储 Update 反而引入了与本题无关的字段校验。
	if _, err := db.ExecContext(ctx, "UPDATE users SET status = ? WHERE id = ?",
		int(model.UserStatusDisabled), disabled.ID); err != nil {
		t.Fatalf("禁用用户失败: %v", err)
	}
	unlimited := newQuotaUser(t, users, model.QuotaUnlimited, "trial-unlimited")

	// 让 normal 先花掉 2000：快照必须记下这个值，回收时才能区分"花的是谁的钱"。
	if err := users.AddUsedQuota(ctx, normal.ID, 2_000); err != nil {
		t.Fatalf("预置已用额度失败: %v", err)
	}

	res, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch:  "trial-batch-1",
		Amount: 10_000,
		TTL:    24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("发放试用额失败: %v", err)
	}
	if res.Recipients != 1 {
		t.Fatalf("发放人数应为 1（仅启用且额度非不限的用户），实际 %d", res.Recipients)
	}

	if got := reloadUser(t, users, normal.ID); got.Quota != 15_000 {
		t.Errorf("启用用户总额度应为 5000+10000=15000，实际 %d", got.Quota)
	}
	if got := reloadUser(t, users, disabled.ID); got.Quota != 5_000 {
		t.Errorf("禁用用户不应获得试用额，总额度应仍为 5000，实际 %d", got.Quota)
	}
	if got := reloadUser(t, users, unlimited.ID); got.Quota != model.QuotaUnlimited {
		t.Errorf("不限额度账号不应被动额度，实际 %d", got.Quota)
	}

	// 快照必须是"发放前"的 used_quota=2000，而不是发放后的值。
	var baseline int64
	if err := db.QueryRowContext(ctx,
		"SELECT used_baseline FROM trial_grants WHERE user_id = ?", normal.ID).Scan(&baseline); err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if baseline != 2_000 {
		t.Errorf("used_baseline 应为发放前的 2000，实际 %d", baseline)
	}
}

// TestTrialGrantRepository_发放_重复批次被拒 覆盖"同一批次只能发一次"的护栏。
func TestTrialGrantRepository_发放_重复批次被拒(t *testing.T) {
	ctx := context.Background()
	grants, users, _ := newTrialGrantFixture(t)

	u := newQuotaUser(t, users, 0, "trial-dup")
	req := model.TrialGrantRequest{Batch: "dup-batch", Amount: 10_000, TTL: time.Hour}

	if _, err := grants.GrantAll(ctx, req); err != nil {
		t.Fatalf("首次发放应成功: %v", err)
	}
	if _, err := grants.GrantAll(ctx, req); !errors.Is(err, model.ErrTrialBatchExists) {
		t.Fatalf("重复批次应返回 ErrTrialBatchExists，实际 %v", err)
	}
	if got := reloadUser(t, users, u.ID); got.Quota != 10_000 {
		t.Errorf("重复发放不得再次加钱，总额度应为 10000，实际 %d", got.Quota)
	}
}

// TestTrialGrantRepository_回收_未使用全额收回 覆盖"没花就全额收回"。
func TestTrialGrantRepository_回收_未使用全额收回(t *testing.T) {
	ctx := context.Background()
	grants, users, _ := newTrialGrantFixture(t)

	u := newQuotaUser(t, users, 5_000, "trial-unused")
	if _, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch: "unused", Amount: 10_000, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	if got := reloadUser(t, users, u.ID); got.Quota != 15_000 {
		t.Fatalf("发放后总额度应为 15000，实际 %d", got.Quota)
	}

	// 未到期不应被回收。
	if n, err := grants.ReclaimExpired(ctx, time.Now().Add(30*time.Minute)); err != nil || n != 0 {
		t.Fatalf("未到期不应回收，实际 count=%d err=%v", n, err)
	}
	// 到期后全额收回，只剩用户自己的 5000。
	if n, err := grants.ReclaimExpired(ctx, time.Now().Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("到期应回收 1 条，实际 count=%d err=%v", n, err)
	}
	got := reloadUser(t, users, u.ID)
	if got.Quota != 5_000 || got.UsedQuota != 0 {
		t.Errorf("回收后应回到 5000/0，实际 %d/%d", got.Quota, got.UsedQuota)
	}
	// 幂等：再跑一次不应重复扣款。
	if n, err := grants.ReclaimExpired(ctx, time.Now().Add(3*time.Hour)); err != nil || n != 0 {
		t.Fatalf("重复回收应无操作，实际 count=%d err=%v", n, err)
	}
	if got := reloadUser(t, users, u.ID); got.Quota != 5_000 {
		t.Errorf("重复回收不得再次扣款，实际 %d", got.Quota)
	}
}

// TestTrialGrantRepository_回收_只收回未用完的部分_不吃自有余额 覆盖最关键的记账口径。
//
// 场景：用户自有 5000，发放 10000，花掉 10000。
// 按"试用额先花"的约定，这 10000 全部来自试用额，用户自己的 5000 一分未动，
// 因此到期时应**回收 0**，余额保持 5000。
// 若实现漏了 used_baseline 快照，这里会误扣 5000（把用户自己充的钱收走）。
func TestTrialGrantRepository_回收_只收回未用完的部分_不吃自有余额(t *testing.T) {
	ctx := context.Background()
	grants, users, db := newTrialGrantFixture(t)

	u := newQuotaUser(t, users, 5_000, "trial-partial")
	if _, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch: "partial", Amount: 10_000, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	if err := users.AddUsedQuota(ctx, u.ID, 10_000); err != nil {
		t.Fatalf("模拟消费失败: %v", err)
	}

	n, err := grants.ReclaimExpired(ctx, time.Now().Add(2*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("到期应回收 1 条，实际 count=%d err=%v", n, err)
	}
	got := reloadUser(t, users, u.ID)
	if got.Quota != 15_000 || got.UsedQuota != 10_000 {
		t.Errorf("试用额已花完，不应动余额：期望 15000/10000，实际 %d/%d", got.Quota, got.UsedQuota)
	}
	if got.RemainingQuota() != 5_000 {
		t.Errorf("用户自有余额应仍为 5000，实际 %d", got.RemainingQuota())
	}

	// 台账里应记下"实际回收 0"，便于事后对账。
	var reclaimed int64
	if err := db.QueryRowContext(ctx,
		"SELECT reclaimed_amount FROM trial_grants WHERE user_id = ?", u.ID).Scan(&reclaimed); err != nil {
		t.Fatalf("读取回收额失败: %v", err)
	}
	if reclaimed != 0 {
		t.Errorf("已用完的批次回收额应为 0，实际 %d", reclaimed)
	}
}

// TestTrialGrantRepository_回收_花一半只收回另一半 覆盖部分消费的记账口径。
func TestTrialGrantRepository_回收_花一半只收回另一半(t *testing.T) {
	ctx := context.Background()
	grants, users, _ := newTrialGrantFixture(t)

	u := newQuotaUser(t, users, 0, "trial-half")
	if _, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch: "half", Amount: 10_000, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	if err := users.AddUsedQuota(ctx, u.ID, 4_000); err != nil {
		t.Fatalf("模拟消费失败: %v", err)
	}

	if n, err := grants.ReclaimExpired(ctx, time.Now().Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("到期应回收 1 条，实际 count=%d err=%v", n, err)
	}
	got := reloadUser(t, users, u.ID)
	// 发了 10000、花了 4000 → 收回 6000；总额度 10000-6000=4000，已用 4000，余额 0。
	if got.Quota != 4_000 || got.UsedQuota != 4_000 || got.RemainingQuota() != 0 {
		t.Errorf("期望 4000/4000（余额 0），实际 %d/%d（余额 %d）",
			got.Quota, got.UsedQuota, got.RemainingQuota())
	}
}

// TestTrialGrantRepository_ActiveFor_展示剩余与最早到期 覆盖门户展示口径。
func TestTrialGrantRepository_ActiveFor_展示剩余与最早到期(t *testing.T) {
	ctx := context.Background()
	grants, users, _ := newTrialGrantFixture(t)

	u := newQuotaUser(t, users, 0, "trial-active")
	now := time.Now()
	if _, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch: "active-a", Amount: 10_000, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("发放失败: %v", err)
	}
	// 再叠一批更晚到期的，验证"合计剩余"与"最早到期时间"两个口径。
	if _, err := grants.GrantAll(ctx, model.TrialGrantRequest{
		Batch: "active-b", Amount: 3_000, TTL: 5 * time.Hour,
	}); err != nil {
		t.Fatalf("第二批发放失败: %v", err)
	}

	active, err := grants.ActiveFor(ctx, u.ID, now)
	if err != nil {
		t.Fatalf("查询生效中的试用额失败: %v", err)
	}
	if active.Remaining != 13_000 {
		t.Errorf("两批合计应为 13000，实际 %d", active.Remaining)
	}
	// 最早到期的是第一批（1 小时），而不是第二批（5 小时）。
	if until := time.Until(active.ExpiresAt); until > time.Hour+time.Minute || until < 50*time.Minute {
		t.Errorf("到期时间应约为 1 小时后，实际 %s 后", until)
	}

	// 消费 12_000 后只剩 1000。
	if err := users.AddUsedQuota(ctx, u.ID, 12_000); err != nil {
		t.Fatalf("模拟消费失败: %v", err)
	}
	active, err = grants.ActiveFor(ctx, u.ID, now)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if active.Remaining != 1_000 {
		t.Errorf("消费 12000 后应剩 1000，实际 %d", active.Remaining)
	}

	// 全部到期后不再展示（此时回收协程即使还没跑，界面也不该再显示）。
	active, err = grants.ActiveFor(ctx, u.ID, now.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if active.IsActive() {
		t.Errorf("全部到期后不应再展示试用额，实际 %+v", active)
	}
}
