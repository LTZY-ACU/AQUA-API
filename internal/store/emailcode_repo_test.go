// 邮箱验证码仓储的单元测试。
//
// 意图（Why）：
//
//	验证码是注册链路唯一的"人机/所有权证明"，它的两项性质属于安全底线：
//	  1) 单条验证码的尝试次数上限必须被【原子】守住——若"读次数"与"加次数"
//	     分成两步，并发提交 N 个猜测会全部读到同一个旧值并同时通过阈值检查，
//	     把"最多 5 次"放大成"5 + 并发数"次；
//	  2) 已被消费的验证码必须一次性失效，同一验证码不能注册两次。
//
// 流转（Flow）：
//
//	newTestStore → 建记录 → IncreaseAttemptsWithin / Consume → 断言返回值与库内状态
//
// 扩展（Extend）：
//
//	新增验证码用途（purpose）无需改本文件；新增状态字段时补对应断言。
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newTestEmailCode 插入一条验证码记录并返回其 ID。
func newTestEmailCode(t *testing.T, repo model.EmailCodeRepository) uint64 {
	t.Helper()

	record := &model.EmailCode{
		Email:     "code-test@example.com",
		Purpose:   model.EmailCodePurposeRegister,
		CodeHash:  "test-hash",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	if err := repo.Create(context.Background(), record); err != nil {
		t.Fatalf("创建验证码记录失败: %v", err)
	}
	return record.ID
}

// TestIncreaseAttemptsWithin_达到上限后拒绝计数 是本文件的核心用例。
func TestIncreaseAttemptsWithin_达到上限后拒绝计数(t *testing.T) {
	ctx := context.Background()
	repo := NewEmailCodeRepository(newTestStore(t).DB())
	id := newTestEmailCode(t, repo)

	const max = 3
	for attempt := 1; attempt <= max; attempt++ {
		ok, err := repo.IncreaseAttemptsWithin(ctx, id, max)
		if err != nil {
			t.Fatalf("第 %d 次计数失败: %v", attempt, err)
		}
		if !ok {
			t.Fatalf("第 %d 次计数被拒，期望允许（上限 %d）", attempt, max)
		}
	}

	// 第 max+1 次必须被拒——这正是"并发下也不会超限"的落地形式
	ok, err := repo.IncreaseAttemptsWithin(ctx, id, max)
	if err != nil {
		t.Fatalf("超限计数返回错误: %v", err)
	}
	if ok {
		t.Errorf("第 %d 次计数仍被允许，尝试次数上限未生效", max+1)
	}

	// 库内计数必须停在上限，不能因被拒而继续增长
	latest, err := repo.LatestActive(ctx, "code-test@example.com", model.EmailCodePurposeRegister)
	if err != nil {
		t.Fatalf("读取验证码失败: %v", err)
	}
	if latest.Attempts != max {
		t.Errorf("库内尝试次数 = %d，期望停在上限 %d", latest.Attempts, max)
	}
}

// TestIncreaseAttemptsWithin_已消费的不再计数 防止"消费后还能继续试探"。
func TestIncreaseAttemptsWithin_已消费的不再计数(t *testing.T) {
	ctx := context.Background()
	repo := NewEmailCodeRepository(newTestStore(t).DB())
	id := newTestEmailCode(t, repo)

	if err := repo.Consume(ctx, id, time.Now()); err != nil {
		t.Fatalf("消费验证码失败: %v", err)
	}

	ok, err := repo.IncreaseAttemptsWithin(ctx, id, model.EmailCodeMaxAttempts)
	if err != nil {
		t.Fatalf("已消费验证码计数返回错误: %v", err)
	}
	if ok {
		t.Error("已消费的验证码仍被计数，应为 false")
	}
}

// TestConsume_只能消费一次 覆盖"一次性"语义。
func TestConsume_只能消费一次(t *testing.T) {
	ctx := context.Background()
	repo := NewEmailCodeRepository(newTestStore(t).DB())
	id := newTestEmailCode(t, repo)

	if err := repo.Consume(ctx, id, time.Now()); err != nil {
		t.Fatalf("首次消费失败: %v", err)
	}
	if err := repo.Consume(ctx, id, time.Now()); !errors.Is(err, model.ErrEmailCodeNotFound) {
		t.Errorf("二次消费错误 = %v，期望 ErrEmailCodeNotFound", err)
	}
}
