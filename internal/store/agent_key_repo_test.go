// agent 密钥仓储的单元测试。
//
// 测试重点（都是"配错了会出事"的性质）：
//   - 鉴权路径必须能按摘要查到密钥，且查不到时返回统一的"不存在"；
//   - 禁用 / 过期后 IsActive 必须为 false（这两条是"撤销立即生效"的保证）；
//   - 删除后再查必须是"不存在"，不能返回已删除的残留行；
//   - 未知摘要与空摘要都必须走"不存在"，不能误判成"数据库出错"。
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/crypto"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newAgentKeyRepo 构造一个已迁移的测试库 + agent 密钥仓储。
func newAgentKeyRepo(t *testing.T) model.AgentKeyRepository {
	t.Helper()
	return NewAgentKeyRepository(newTestStore(t).DB())
}

// 测试用摘要：真实链路里是 SHA256Hex(明文)，这里直接造一个等价的固定串。
const testKeyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// TestAgentKeyRepo_Create与GetByHash 验证写入后能按摘要查回，且角色不丢。
func TestAgentKeyRepo_Create与GetByHash(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	want := &model.AgentKey{
		Role: model.AgentRoleOps,
		Name: "给小程序用",
		// 摘要形态与 SHA256Hex 一致：64 位小写十六进制
		KeyHash: testKeyHash,
	}
	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if want.ID == 0 {
		t.Error("创建后应回填 ID")
	}

	got, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("按摘要查询失败: %v", err)
	}
	if got.Role != model.AgentRoleOps {
		t.Errorf("角色 = %q，期望 ops", got.Role)
	}
	if got.Name != "给小程序用" {
		t.Errorf("备注 = %q，期望原样返回", got.Name)
	}
	if got.Status != model.AgentKeyStatusEnabled {
		t.Errorf("状态 = %d，期望默认启用（%d）", got.Status, model.AgentKeyStatusEnabled)
	}
	if got.Expired() {
		t.Error("未设过期时间时不应判定为已过期")
	}
	if !got.IsActive() {
		t.Error("新建的密钥应当是可用的")
	}
}

// TestAgentKeyRepo_拒绝空摘要 验证没有摘要的行不会被写进库。
//
// 这种行永远无法通过鉴权，却会占着唯一索引的位置，
// 属于"建了但永远不能用"的脏数据——在写入处拦住比事后清理可靠。
func TestAgentKeyRepo_拒绝空摘要(t *testing.T) {
	repo := newAgentKeyRepo(t)

	err := repo.Create(context.Background(), &model.AgentKey{
		Role:    model.AgentRoleSupport,
		KeyHash: "",
	})
	if err == nil {
		t.Fatal("摘要为空时应拒绝写入")
	}
}

// TestAgentKeyRepo_摘要重复应失败 验证唯一索引真的生效。
func TestAgentKeyRepo_摘要重复应失败(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleSupport, KeyHash: testKeyHash,
	}); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleOps, KeyHash: testKeyHash,
	}); err == nil {
		t.Fatal("相同摘要应被唯一索引拒绝")
	}
}

// TestAgentKeyRepo_未知摘要与空摘要都返回不存在 验证鉴权失败时不会泄露原因。
//
// 区分"没有这把 key"与"查询出错"会给攻击者一个"这把 key 存在过"的信号。
func TestAgentKeyRepo_未知摘要与空摘要都返回不存在(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	for name, hash := range map[string]string{
		"未知摘要": crypto.SHA256Hex("never-issued"),
		"空摘要":  "",
	} {
		got, err := repo.GetByHash(ctx, hash)
		if !errors.Is(err, model.ErrAgentKeyNotFound) {
			t.Errorf("%s：err = %v，期望 ErrAgentKeyNotFound", name, err)
		}
		if got != nil {
			t.Errorf("%s：不应返回任何对象", name)
		}
	}
}

// TestAgentKeyRepo_禁用后IsActive为假 验证"撤销立即生效"。
func TestAgentKeyRepo_禁用后IsActive为假(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleOps, KeyHash: testKeyHash,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	got, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	id := got.ID

	if err := repo.SetStatus(ctx, id, model.AgentKeyStatusDisabled); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}

	// 行仍在库里（可追溯），但 IsActive 必须为 false——鉴权只看这一位。
	after, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("禁用后仍应能查到行（用于后台展示）: %v", err)
	}
	if after.IsActive() {
		t.Error("已禁用的密钥 IsActive 应为 false")
	}
	if after.IsOps() {
		// IsOps 只判角色，不代表可用；这里确认两个概念没被混为一谈。
		t.Log("角色仍为 ops，IsOps 与 IsActive 是两个独立判断（符合预期）")
	}
}

// TestAgentKeyRepo_过期后IsActive为假 验证过期判定在模型层生效。
func TestAgentKeyRepo_过期后IsActive为假(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role:      model.AgentRoleOps,
		KeyHash:   testKeyHash,
		ExpiresAt: time.Now().Add(-time.Hour), // 已经过期
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	got, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !got.Expired() {
		t.Error("过期时间已过的密钥应判定为已过期")
	}
	if got.IsActive() {
		t.Error("已过期的密钥 IsActive 应为 false")
	}
}

// TestAgentKeyRepo_永不过期 验证 expires_at=0 被当作"不过期"而非"1970 年就过期了"。
//
// 这是从 Unix 时间戳落库时最常见的一类 bug：零值被当成真实时间戳，
// 于是"永不过期"变成"一创建就过期"。
func TestAgentKeyRepo_永不过期(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleSupport, KeyHash: testKeyHash,
		// ExpiresAt 留零值
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	got, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.Expired() {
		t.Error("expires_at=0 应表示永不过期")
	}
	if !got.IsActive() {
		t.Error("未设过期时间的密钥应当可用")
	}
}

// TestAgentKeyRepo_删除后不可用 验证撤销是"立刻"而非"等过期"。
func TestAgentKeyRepo_删除后不可用(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleOps, KeyHash: testKeyHash,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	got, _ := repo.GetByHash(ctx, testKeyHash)

	if err := repo.Delete(ctx, got.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := repo.GetByHash(ctx, testKeyHash); !errors.Is(err, model.ErrAgentKeyNotFound) {
		t.Errorf("删除后查询 err = %v，期望 ErrAgentKeyNotFound", err)
	}
}

// TestAgentKeyRepo_操作不存在的ID应报错 验证"密钥已被删除"不会被静默当成成功。
//
// 静默成功是最难查的一类问题：站长点了删除、界面提示成功，实际什么都没发生。
func TestAgentKeyRepo_操作不存在的ID应报错(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	const missingID = uint64(999999)
	if err := repo.SetStatus(ctx, missingID, model.AgentKeyStatusDisabled); !errors.Is(err, model.ErrAgentKeyNotFound) {
		t.Errorf("禁用不存在的密钥 err = %v，期望 ErrAgentKeyNotFound", err)
	}
	if err := repo.Delete(ctx, missingID); !errors.Is(err, model.ErrAgentKeyNotFound) {
		t.Errorf("删除不存在的密钥 err = %v，期望 ErrAgentKeyNotFound", err)
	}
}

// TestAgentKeyRepo_List按角色过滤 验证后台"运维 / 客服"分组能正确取数。
func TestAgentKeyRepo_List按角色过滤(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	opsHash := crypto.SHA256Hex("ops-key")
	supportHash := crypto.SHA256Hex("support-key")
	if err := repo.Create(ctx, &model.AgentKey{Role: model.AgentRoleOps, KeyHash: opsHash}); err != nil {
		t.Fatalf("创建 ops 失败: %v", err)
	}
	if err := repo.Create(ctx, &model.AgentKey{Role: model.AgentRoleSupport, KeyHash: supportHash}); err != nil {
		t.Fatalf("创建 support 失败: %v", err)
	}

	opsList, err := repo.List(ctx, model.AgentKeyQuery{Role: model.AgentRoleOps})
	if err != nil {
		t.Fatalf("按角色查询失败: %v", err)
	}
	if len(opsList) != 1 {
		t.Fatalf("ops 角色应有 1 条，实际 %d 条", len(opsList))
	}
	if opsList[0].Role != model.AgentRoleOps {
		t.Errorf("角色过滤失效，返回了 %q", opsList[0].Role)
	}

	all, err := repo.List(ctx, model.AgentKeyQuery{})
	if err != nil {
		t.Fatalf("全量查询失败: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("不限角色时应返回 2 条，实际 %d 条", len(all))
	}

	// OnlyEnabled 过滤：先禁用 support 那条，再验证它被排除
	all, _ = repo.List(ctx, model.AgentKeyQuery{})
	for _, k := range all {
		if k.Role == model.AgentRoleSupport {
			if err := repo.SetStatus(ctx, k.ID, model.AgentKeyStatusDisabled); err != nil {
				t.Fatalf("禁用失败: %v", err)
			}
		}
	}
	enabled, err := repo.List(ctx, model.AgentKeyQuery{OnlyEnabled: true})
	if err != nil {
		t.Fatalf("按启用状态查询失败: %v", err)
	}
	for _, k := range enabled {
		if k.Role == model.AgentRoleSupport {
			t.Error("已禁用的密钥不应出现在 OnlyEnabled 结果里")
		}
	}
	if len(enabled) != 1 {
		t.Errorf("启用中的密钥应为 1 条，实际 %d 条", len(enabled))
	}
}

// TestAgentKeyRepo_List限制返回行数 验证 limit 被夹紧，防止公网接口把表拉进内存。
func TestAgentKeyRepo_List限制返回行数(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := repo.Create(ctx, &model.AgentKey{
			Role:    model.AgentRoleSupport,
			KeyHash: crypto.SHA256Hex(string(rune('a' + i))),
		}); err != nil {
			t.Fatalf("创建第 %d 条失败: %v", i, err)
		}
	}

	got, err := repo.List(ctx, model.AgentKeyQuery{Limit: 2})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit=2 应返回 2 条，实际 %d 条", len(got))
	}

	// limit<=0 走默认值（50），不是"不限制"
	got, err = repo.List(ctx, model.AgentKeyQuery{Limit: -1})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit=-1 应回退到默认值并返回全部 3 条，实际 %d 条", len(got))
	}
}

// TestAgentKeyRepo_备注为空也能读回 验证可空字段不会让扫描失败。
func TestAgentKeyRepo_备注为空也能读回(t *testing.T) {
	repo := newAgentKeyRepo(t)
	ctx := context.Background()

	if err := repo.Create(ctx, &model.AgentKey{
		Role: model.AgentRoleSupport, KeyHash: testKeyHash, // Name 留空
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	got, err := repo.GetByHash(ctx, testKeyHash)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.Name != "" {
		t.Errorf("备注应为空字符串，实际 %q", got.Name)
	}
}
