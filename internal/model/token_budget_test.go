// 令牌「周期预算」判定逻辑的单元测试。
//
// 意图（Why）：
//
//	预算闸门写错的方向只有两种，且都很糟：
//	  · 判宽了 → 该拦的没拦住（额度被跑穿，站长真金白银的损失）；
//	  · 判窄了 → 不该拦的被拦（用户投诉，且极难解释）。
//	因此把"启用条件、窗口累计、超限边界、过期重置、默认不限"逐条钉死。
//
// 流转（Flow）：
//
//	go test ./internal/model/ -run Budget
//
// 扩展（Extend）：
//
//	新增周期档位（如 quarterly）时，在 TestToken_EvaluateBudget_窗口过期需重置 里补一档。
package model

import (
	"testing"
	"time"
)

// TestIsValidBudgetPeriod 验证周期取值的合法性判定。
func TestIsValidBudgetPeriod(t *testing.T) {
	valid := []string{BudgetPeriodDaily, BudgetPeriodWeekly, BudgetPeriodMonthly}
	for _, p := range valid {
		if !IsValidBudgetPeriod(p) {
			t.Errorf("周期 %q 应合法", p)
		}
	}
	for _, p := range []string{"", "yearly", "DAY", "每天"} {
		if IsValidBudgetPeriod(p) {
			t.Errorf("周期 %q 不应合法", p)
		}
	}
}

// TestToken_EvaluateBudget_未启用时一律放行 覆盖"默认 0 / 周期非法 / 不限额度"的降级路径。
//
// 这是最重要的兼容性断言：引入预算能力后，存量令牌（四列均为默认值）
// 必须与改动前逐字一致地放行。
func TestToken_EvaluateBudget_未启用时一律放行(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		tk   *Token
	}{
		{"零值令牌", &Token{}},
		{"预算为0", &Token{BudgetQuota: 0, BudgetPeriod: BudgetPeriodDaily, UsedQuota: 9999}},
		{"周期为空", &Token{BudgetQuota: 100, BudgetPeriod: "", UsedQuota: 9999}},
		{"周期非法", &Token{BudgetQuota: 100, BudgetPeriod: "yearly", UsedQuota: 9999}},
		{"不限额度令牌不受预算影响", &Token{UnlimitedQuota: true, BudgetQuota: 100, BudgetPeriod: BudgetPeriodDaily, UsedQuota: 9999}},
		{"nil 令牌", nil},
	}
	for _, tc := range cases {
		got := tc.tk.EvaluateBudget(now)
		if got.Enabled || got.Exceeded || got.NeedReset {
			t.Errorf("%s：应完全未启用，实际 %+v", tc.name, got)
		}
	}
}

// TestToken_EvaluateBudget_窗口内累计与超限 验证消耗累计口径与超限边界（>= 即超限）。
func TestToken_EvaluateBudget_窗口内累计与超限(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
	windowStart := now.Add(-time.Hour)

	tk := &Token{
		BudgetQuota:       100,
		BudgetPeriod:      BudgetPeriodDaily,
		BudgetWindowStart: windowStart,
		BudgetWindowBase:  500, // 窗口起点时的 used_quota 快照
		UsedQuota:         560, // 本窗口已用 = 560 - 500 = 60
	}
	got := tk.EvaluateBudget(now)
	if !got.Enabled || got.NeedReset {
		t.Fatalf("窗口内不应需要重置：%+v", got)
	}
	if got.Used != 60 {
		t.Errorf("窗口已用 = %d，期望 60", got.Used)
	}
	if got.Exceeded {
		t.Errorf("60 < 100 不应超限：%+v", got)
	}
	if !got.WindowStart.Equal(windowStart) {
		t.Errorf("窗口起点被意外改动：%v", got.WindowStart)
	}

	// 恰好达到上限即算超限（边界取 >=，宁可早拦一刻，也不放过最后一次超额调用）
	tk.UsedQuota = 600
	if got := tk.EvaluateBudget(now); !got.Exceeded || got.Used != 100 {
		t.Errorf("达到上限应判超限且已用=100，实际 %+v", got)
	}

	// 基线差为负时按 0 处理（窗口重置后对上一窗口的退还可能造成负数）
	tk.UsedQuota = 400
	if used := tk.BudgetWindowUsed(); used != 0 {
		t.Errorf("基线差为负应按 0，实际 %d", used)
	}
}

// TestToken_EvaluateBudget_窗口过期需重置 验证各类周期在过期时都要求惰性重置。
func TestToken_EvaluateBudget_窗口过期需重置(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)

	cases := []struct {
		name      string
		period    string
		startedAt time.Time
		wantReset bool
	}{
		{"尚未锚定（零值起点）", BudgetPeriodDaily, time.Time{}, true},
		{"日窗口已过期", BudgetPeriodDaily, now.AddDate(0, 0, -1), true},
		{"周窗口已过期", BudgetPeriodWeekly, now.AddDate(0, 0, -7), true},
		{"月窗口已过期", BudgetPeriodMonthly, now.AddDate(0, -1, 0), true},
		{"日窗口仍在期内", BudgetPeriodDaily, now.Add(-23 * time.Hour), false},
		{"周窗口仍在期内", BudgetPeriodWeekly, now.AddDate(0, 0, -6), false},
	}
	for _, tc := range cases {
		tk := &Token{
			BudgetQuota:       100,
			BudgetPeriod:      tc.period,
			BudgetWindowStart: tc.startedAt,
			UsedQuota:         999, // 即便"看起来"已用很多，过期后也应要求重置
		}
		got := tk.EvaluateBudget(now)
		if !got.Enabled {
			t.Fatalf("%s：应启用预算", tc.name)
		}
		if got.NeedReset != tc.wantReset {
			t.Errorf("%s：NeedReset = %v，期望 %v", tc.name, got.NeedReset, tc.wantReset)
		}
		if tc.wantReset {
			// 需要重置时，本次一律视为未超限，且窗口起点前移、已用归零。
			if got.Exceeded {
				t.Errorf("%s：需要重置时不应同时判超限", tc.name)
			}
			if !got.WindowStart.Equal(now) {
				t.Errorf("%s：重置后的窗口起点应为 now，实际 %v", tc.name, got.WindowStart)
			}
			if got.Used != 0 {
				t.Errorf("%s：重置后窗口已用应为 0，实际 %d", tc.name, got.Used)
			}
		} else {
			// 仍在期内：已用 999 ≥ 上限 100，应判超限。
			if !got.Exceeded {
				t.Errorf("%s：窗口内已用超上限应判超限", tc.name)
			}
		}
	}
}

// TestToken_Validate_周期预算 验证预算相关字段的领域校验。
func TestToken_Validate_周期预算(t *testing.T) {
	newKey := func(t *testing.T) string {
		t.Helper()
		key, err := GenerateTokenKey()
		if err != nil {
			t.Fatalf("生成 KEY 失败: %v", err)
		}
		return key
	}
	base := func(t *testing.T) *Token {
		return &Token{Name: "预算令牌", Key: newKey(t), Status: TokenStatusEnabled}
	}

	// 合法：默认（不启用预算）
	if err := base(t).Validate(); err != nil {
		t.Errorf("默认令牌应合法，实际: %v", err)
	}
	// 合法：启用日预算
	tk := base(t)
	tk.BudgetQuota = 100
	tk.BudgetPeriod = BudgetPeriodDaily
	if err := tk.Validate(); err != nil {
		t.Errorf("启用日预算应合法，实际: %v", err)
	}

	reject := []struct {
		name   string
		mutate func(*Token)
	}{
		{"预算额度为负", func(tk *Token) { tk.BudgetQuota = -1; tk.BudgetPeriod = BudgetPeriodDaily }},
		{"窗口基线为负", func(tk *Token) { tk.BudgetQuota = 1; tk.BudgetPeriod = BudgetPeriodDaily; tk.BudgetWindowBase = -5 }},
		{"周期非法", func(tk *Token) { tk.BudgetQuota = 1; tk.BudgetPeriod = "yearly" }},
		{"开了预算却不给周期", func(tk *Token) { tk.BudgetQuota = 1; tk.BudgetPeriod = "" }},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			tk := base(t)
			tc.mutate(tk)
			if err := tk.Validate(); err == nil {
				t.Error("非法预算配置应被拒绝，实际通过")
			}
		})
	}

	// 空周期 + 零预算合法（存量令牌形态）
	tk = base(t)
	tk.BudgetQuota = 0
	tk.BudgetPeriod = ""
	if err := tk.Validate(); err != nil {
		t.Errorf("零预算且空周期应合法，实际: %v", err)
	}
}
