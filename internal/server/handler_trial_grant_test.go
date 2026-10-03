// 限时试用额发放接口（后台发放 + 门户展示）的单元测试。
//
// 意图（Why）：
//
//	这是本站唯一"一次给全体用户加钱"的接口，四条护栏必须被锁死：
//	  1) 未显式 confirm 一律拒绝（防止前端某个顺手复用的按钮把钱发出去）；
//	  2) 同一批次只能发一次（防止误点两次 = 全站双份）；
//	  3) 金额按「分 → 额度」换算（与充值同口径），换算错就是真金白银的错；
//	  4) 门户只展示"未过期且未用完"的部分（否则用户会看到一个拿不到的数字）。
//
// 流转（Flow）：
//
//	go test ./internal/server/ -run TrialGrant
//	  └─ httptest 直连 Handler，覆盖路由 → 中间件 → 处理器 → 仓储的完整链路
//
// 扩展（Extend）：
//
//	新增发放对象规则（如只发给新用户）时，在 TestTrialGrant_发放_* 补断言。
package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
	"github.com/LTZY-ACU/aqua-api/internal/store"
)

// trialFixture 是试用额接口测试的装配容器。
type trialFixture struct {
	srv      *Server
	users    model.UserRepository
	sessions model.SessionRepository
	adminTok string
	// memberIDs 是"启用且额度非不限"的普通用户，发放时会命中他们。
	memberIDs []uint64
}

// newTrialFixture 构造最小服务：1 个不限额度管理员 + 2 个启用用户 + 1 个禁用用户。
//
// 兑换比例固定为 1 元 = 1,000,000 额度（与生产一致），
// 这样 "amount_cents=10"（1 毛钱）对应 100,000 额度，断言一眼能看懂。
func newTrialFixture(t *testing.T) *trialFixture {
	t.Helper()

	st, err := store.Open("sqlite", t.TempDir()+"/trial_api_test.db")
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

	ctx := context.Background()
	users := store.NewUserRepository(st.DB())
	sessions := store.NewSessionRepository(st.DB())
	settings := store.NewSettingRepository(st.DB(), st.Dialect())

	admin := &model.User{
		Username: "trial-admin", PasswordHash: "test-hash",
		Role: model.UserRoleAdmin, Status: model.UserStatusEnabled, Quota: model.QuotaUnlimited,
	}
	if err := users.Create(ctx, admin); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	var memberIDs []uint64
	for i := 1; i <= 2; i++ {
		u := &model.User{
			Username: fmt.Sprintf("trial-member-%d", i), PasswordHash: "test-hash",
			Role: model.UserRoleUser, Status: model.UserStatusEnabled, Quota: 0,
		}
		if err := users.Create(ctx, u); err != nil {
			t.Fatalf("创建普通用户失败: %v", err)
		}
		memberIDs = append(memberIDs, u.ID)
	}
	disabled := &model.User{
		Username: "trial-disabled-user", PasswordHash: "test-hash",
		Role: model.UserRoleUser, Status: model.UserStatusDisabled, Quota: 0,
	}
	if err := users.Create(ctx, disabled); err != nil {
		t.Fatalf("创建禁用用户失败: %v", err)
	}

	if err := settings.Set(ctx, model.SettingKeyPaymentExchangeRate, "1000000"); err != nil {
		t.Fatalf("设置兑换比例失败: %v", err)
	}

	cfg := config.Default()
	cfg.Server.Mode = "test"
	cfg.Server.Listen = "127.0.0.1:0"

	srv := New(Deps{
		Config:      cfg,
		Store:       st,
		Channels:    store.NewChannelRepository(st.DB(), cipher),
		Tokens:      store.NewTokenRepository(st.DB(), cipher),
		Users:       users,
		Sessions:    sessions,
		UsageLogs:   store.NewUsageLogRepository(st.DB(), st.Dialect()),
		Settings:    settings,
		Audit:       store.NewAuditLogRepository(st.DB()),
		TrialGrants: store.NewTrialGrantRepository(st.DB()),
	})

	return &trialFixture{
		srv:       srv,
		users:     users,
		sessions:  sessions,
		memberIDs: memberIDs,
		adminTok:  mustReauthedSession(t, sessions, createTokenGroupSession(t, sessions, admin.ID)),
	}
}

// TestTrialGrant_未确认一律拒绝 覆盖"批量加钱必须显式确认"这条护栏。
func TestTrialGrant_未确认一律拒绝(t *testing.T) {
	fx := newTrialFixture(t)

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.adminTok,
		`{"amount_cents":10,"hours":24,"batch":"no-confirm"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未确认时状态码 = %d，期望 400", rec.Code)
	}
	if got := errorCode(body); got != "trial_grant_confirm_required" {
		t.Errorf("错误码 = %q，期望 trial_grant_confirm_required", got)
	}
	// 关键：拒绝必须是"什么都没发生"，不能出现"报错但钱已发出"。
	if got := fx.userQuota(t, fx.memberIDs[0]); got != 0 {
		t.Errorf("被拒绝的请求不得改动余额，实际 %d", got)
	}
}

// TestTrialGrant_发放成功_存入余额且门户可见 覆盖正常路径与分→额度换算。
func TestTrialGrant_发放成功_存入余额且门户可见(t *testing.T) {
	fx := newTrialFixture(t)
	ctx := context.Background()

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.adminTok,
		`{"amount_cents":10,"hours":24,"batch":"welcome-1","confirm":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("发放失败：状态码 = %d，body = %v", rec.Code, body)
	}
	// 10 分 × (1,000,000 / 100) = 100,000 额度
	const wantQuota int64 = 100_000
	if got := body["amount_quota"]; got != float64(wantQuota) {
		t.Errorf("amount_quota = %v，期望 %d（10 分按 1 元=1000000 额度折算）", got, wantQuota)
	}
	// 两个启用用户命中；管理员（不限额度）与禁用用户都应被排除。
	if got := body["recipients"]; got != float64(len(fx.memberIDs)) {
		t.Errorf("recipients = %v，期望 %d（不限额度管理员与禁用用户不应命中）", got, len(fx.memberIDs))
	}
	for _, id := range fx.memberIDs {
		if got := fx.userQuota(t, id); got != wantQuota {
			t.Errorf("用户 %d 余额 = %d，期望 %d", id, got, wantQuota)
		}
	}

	// 门户接口应能读到这笔试用额，并给出到期时间。
	memberTok := createTokenGroupSession(t, fx.sessions, fx.memberIDs[0])
	rec, trial := doBearerJSON(t, fx.srv, http.MethodGet, "/api/user/trial", memberTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("门户查询失败：状态码 = %d", rec.Code)
	}
	if trial["active"] != true {
		t.Fatalf("试用额应处于生效状态，实际 %v", trial)
	}
	if got := trial["remaining"]; got != float64(wantQuota) {
		t.Errorf("remaining = %v，期望 %d", got, wantQuota)
	}
	if until, ok := trial["expires_in_seconds"].(float64); !ok || until <= 0 || until > 24*3600+60 {
		t.Errorf("expires_in_seconds = %v，期望略小于 24 小时", trial["expires_in_seconds"])
	}

	// 台账应收敛为 1 条 pending，且记录下发放前的 used_quota 快照（此处为 0）。
	var count int
	if err := fx.srv.deps.Store.DB().QueryRowContext(ctx,
		"SELECT COUNT(1) FROM trial_grants WHERE user_id = ? AND status = ?",
		fx.memberIDs[0], string(model.TrialGrantPending)).Scan(&count); err != nil {
		t.Fatalf("查询台账失败: %v", err)
	}
	if count != 1 {
		t.Errorf("台账应有 1 条在效期记录，实际 %d", count)
	}
}

// TestTrialGrant_同一批次只能发一次 覆盖防重复发放的唯一闸门。
func TestTrialGrant_同一批次只能发一次(t *testing.T) {
	fx := newTrialFixture(t)

	payload := `{"amount_cents":10,"hours":24,"batch":"welcome-dup","confirm":true}`
	rec, _ := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.adminTok, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次发放应成功，实际 %d", rec.Code)
	}

	rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.adminTok, payload)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复批次应返回 409，实际 %d，body = %v", rec.Code, body)
	}
	if got := errorCode(body); got != "trial_grant_batch_exists" {
		t.Errorf("错误码 = %q，期望 trial_grant_batch_exists", got)
	}
	// 余额必须是"只发了一次"的数额。
	if got := fx.userQuota(t, fx.memberIDs[0]); got != 100_000 {
		t.Errorf("重复发放不得再加钱，余额 = %d，期望 100000", got)
	}
}

// TestTrialGrant_参数校验 覆盖批次缺失、时长越界与金额过小。
func TestTrialGrant_参数校验(t *testing.T) {
	fx := newTrialFixture(t)

	cases := []struct {
		name string
		body string
		code string
	}{
		{"批次为空", `{"amount_cents":10,"hours":24,"batch":"  ","confirm":true}`, "trial_grant_batch_required"},
		{"时长为零", `{"amount_cents":10,"hours":0,"batch":"bad-hours","confirm":true}`, "trial_grant_invalid_hours"},
		{"时长过大", `{"amount_cents":10,"hours":100000,"batch":"bad-hours2","confirm":true}`, "trial_grant_invalid_hours"},
		{"金额为零", `{"amount_cents":0,"hours":24,"batch":"bad-amount","confirm":true}`, "trial_grant_amount_too_small"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := doBearerJSON(t, fx.srv, http.MethodPost, "/api/admin/trial-grants", fx.adminTok, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d，期望 400，body = %v", rec.Code, body)
			}
			if got := errorCode(body); got != tc.code {
				t.Errorf("错误码 = %q，期望 %q", got, tc.code)
			}
		})
	}
}

// TestTrialGrant_门户_无试用额时返回未生效 覆盖"没有就老实说没有"。
//
// 前端据 active 决定是否渲染横幅，因此这里必须返回 200 + active=false，
// 而不是 404 或错误（那会让概览页多出一处无谓的错误态）。
func TestTrialGrant_门户_无试用额时返回未生效(t *testing.T) {
	fx := newTrialFixture(t)

	tok := createTokenGroupSession(t, fx.sessions, fx.memberIDs[0])
	rec, body := doBearerJSON(t, fx.srv, http.MethodGet, "/api/user/trial", tok, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if body["active"] != false {
		t.Errorf("无试用额时 active 应为 false，实际 %v", body["active"])
	}
	if got := body["remaining"]; got != float64(0) {
		t.Errorf("remaining 应为 0，实际 %v", got)
	}
}

// userQuota 读取用户当前总额度。
func (f *trialFixture) userQuota(t *testing.T, id uint64) int64 {
	t.Helper()
	u, err := f.users.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("读取用户 %d 失败: %v", id, err)
	}
	return u.Quota
}
