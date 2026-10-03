// 「免费模型对 0 额度用户开放」这一语义的回归测试。
//
// 背景（为什么必须锁死这条语义）：
//
//	额度校验曾经排在"判断本次是否计费"之前，于是额度为 0 的用户连免费模型
//	都会被拦成 429。站长为了让免费模型可用，只能把用户额度设成"不限"（-1）
//	——用一个哨兵值掩盖了"校验顺序错了"这个真正原因。
//	本文件把正确顺序固化成用例：先判定是否计费，再决定要不要走额度墙。
//
// 流转（Flow）：
//
//	go test ./internal/server/middleware/
//	  └─ 用真实仓储（临时 SQLite）+ 可控的 fakeReserver 走完整中间件链路
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// fakeReserver 是可控的"计费判定器"。
//
// 用它替代真实的 relay.Billing，可以在不构造价格表的前提下精确控制
// "这个模型计不计费、要预留多少钱"，从而单独验证鉴权层的顺序逻辑。
type fakeReserver struct {
	// priced 为 false 表示该模型不计费（未定价 或 显式免费）
	priced bool
	amount int64

	estimateCalls int
	reserveCalls  int
}

func (f *fakeReserver) EstimateReserve(_ context.Context, _, _ string, _ int) (int64, bool) {
	f.estimateCalls++
	return f.amount, f.priced
}

func (f *fakeReserver) Reserve(_ context.Context, _ model.ReserveRequest) (*model.QuotaReservation, error) {
	f.reserveCalls++
	// 返回值会在调用处被丢弃，这里只需保证不报错
	return nil, nil
}

func (f *fakeReserver) PendingReserved(_ context.Context, _ uint64) (int64, error) {
	return 0, nil
}

func (f *fakeReserver) Release(_ context.Context, _ string) error {
	return nil
}

// createOwnerWithToken 创建"指定额度"的用户与归属它的令牌，返回令牌明文。
func createOwnerWithToken(t *testing.T, tokens model.TokenRepository,
	users model.UserRepository, name string, quota, used int64) string {
	t.Helper()

	ctx := context.Background()
	owner := &model.User{
		Username:     "owner-" + name,
		PasswordHash: "test-hash-placeholder",
		Role:         model.UserRoleUser,
		Status:       model.UserStatusEnabled,
		Quota:        quota,
		UsedQuota:    used,
	}
	if err := users.Create(ctx, owner); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	key, err := model.GenerateTokenKey()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	// 令牌自身不限额度：确保结果只由"账号级额度 + 计费判定"决定，
	// 不让令牌层的额度判定混进来干扰结论。
	token := &model.Token{
		OwnerID:        owner.ID,
		Name:           "free-model-probe-" + name,
		Key:            key,
		Status:         model.TokenStatusEnabled,
		UnlimitedQuota: true,
	}
	if err := tokens.Create(ctx, token); err != nil {
		t.Fatalf("创建令牌失败: %v", err)
	}
	return key
}

// runAuth 用给定 reserver 跑一次请求，返回状态码。
func runAuth(t *testing.T, tokens model.TokenRepository, users model.UserRepository,
	reserver QuotaReserver, tokenKey, body string) int {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if reserver != nil {
		engine.Use(TokenAuth(tokens, users, reserver))
	} else {
		engine.Use(TokenAuth(tokens, users))
	}
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenKey)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder.Code
}

// TestTokenAuth_免费模型_零额度用户必须放行 是本次修复的核心用例。
func TestTokenAuth_免费模型_零额度用户必须放行(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := createOwnerWithToken(t, tokens, users, "free", 0, 0)

	reserver := &fakeReserver{priced: false} // 不计费：未定价 或 显式免费
	code := runAuth(t, tokens, users, reserver, key,
		`{"model":"free-model","messages":[{"role":"user","content":"hi"}]}`)

	if code != http.StatusOK {
		t.Fatalf("免费模型对 0 额度用户应放行（200），实际 %d —— "+
			"若被拦成 429，站长只能把额度设成 -1 来绕开，正是本次要修掉的问题", code)
	}
	if reserver.reserveCalls != 0 {
		t.Fatalf("免费模型不应产生额度预留，实际预留 %d 次", reserver.reserveCalls)
	}
	if reserver.estimateCalls == 0 {
		t.Fatalf("应当询问过计费组件'该模型是否计费'（EstimateReserve 未被调用）")
	}
}

// TestTokenAuth_计费模型_零额度用户应被拒 保证免费豁免没有被放得太宽。
func TestTokenAuth_计费模型_零额度用户应被拒(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := createOwnerWithToken(t, tokens, users, "paid", 0, 0)

	reserver := &fakeReserver{priced: true, amount: 1000}
	code := runAuth(t, tokens, users, reserver, key,
		`{"model":"paid-model","messages":[{"role":"user","content":"hi"}]}`)

	if code != http.StatusTooManyRequests {
		t.Fatalf("计费模型对 0 额度用户应返回 429，实际 %d", code)
	}
	if reserver.reserveCalls != 0 {
		t.Fatalf("额度不足时不应写预留台账，实际预留 %d 次", reserver.reserveCalls)
	}
}

// TestTokenAuth_计费模型_额度充足放行并预留 验证常规路径没有被改坏。
func TestTokenAuth_计费模型_额度充足放行并预留(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	// 额度取小于"信任旁路阈值"的值，确保真的走预留分支
	key := createOwnerWithToken(t, tokens, users, "rich", 1000, 0)

	reserver := &fakeReserver{priced: true, amount: 300}
	code := runAuth(t, tokens, users, reserver, key,
		`{"model":"paid-model","messages":[{"role":"user","content":"hi"}]}`)

	if code != http.StatusOK {
		t.Fatalf("额度充足时应放行（200），实际 %d", code)
	}
	if reserver.reserveCalls != 1 {
		t.Fatalf("计费且额度紧张时应预留 1 次，实际 %d", reserver.reserveCalls)
	}
}

// TestTokenAuth_无法解析模型名时保守校验额度
// 验证"拿不到模型名就不豁免额度墙"：否则可以用畸形请求白嫖计费模型。
func TestTokenAuth_无法解析模型名时保守校验额度(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := createOwnerWithToken(t, tokens, users, "malformed", 0, 0)

	// 即使计费组件说"不计费"，因为拿不到模型名，也不能据此豁免
	reserver := &fakeReserver{priced: false}
	code := runAuth(t, tokens, users, reserver, key, `{这不是合法 JSON`)

	if code != http.StatusTooManyRequests {
		t.Fatalf("无法判断是否计费时应保守按额度校验（429），实际 %d", code)
	}
}

// TestTokenAuth_未启用预留时不豁免额度墙
// 说明：未注入计费组件时无从判断"是否免费"，此时必须保持旧的额度校验行为。
func TestTokenAuth_未启用预留时额度校验照旧(t *testing.T) {
	tokens, users := newTokenAndUserRepos(t)
	key := createOwnerWithToken(t, tokens, users, "nopricer", 0, 0)

	code := runAuth(t, tokens, users, nil, key,
		`{"model":"any-model","messages":[{"role":"user","content":"hi"}]}`)

	if code != http.StatusTooManyRequests {
		t.Fatalf("未注入计费组件时 0 额度应仍被拒（429），实际 %d", code)
	}
}
