// 令牌「周期预算」存储与分组消耗查询的单元测试。
//
// 意图（Why）：
//
//	预算能力有两处极易写错、且出错后不报错的存储细节：
//	  1) 新加的四列必须在 tokenColumns / INSERT / UPDATE / scanToken 四处同步——
//	     漏改任一处，表现为"配了预算却不生效"或"读不回窗口状态"，都不会编译报错；
//	  2) 分组消耗聚合的"分组归属"必须与路由的分组匹配语义一致，
//	     否则会出现"能路由过去却统计不到"的诡异差异。
//	本文件在真实 SQLite 上把这两点钉死。
//
// 流转（Flow）：
//
//	go test ./internal/store/ -run Budget
//	go test ./internal/store/ -run GroupSpend
//
// 扩展（Extend）：
//
//	新增预算周期档位后，在本文件的往返用例中补一档即可。
package store

import (
	"context"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newBudgetTestRepos 在同一个临时库上构造令牌 / 渠道 / 日志三个仓储。
func newBudgetTestRepos(t *testing.T) (model.TokenRepository, model.ChannelRepository, model.UsageLogRepository) {
	t.Helper()

	st := newTestStore(t)
	cipher, err := crypto.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return NewTokenRepository(st.DB(), cipher),
		NewChannelRepository(st.DB(), cipher),
		NewUsageLogRepository(st.DB(), st.Dialect())
}

// TestTokenRepository_BudgetFields_RoundTrip 验证预算四列的写入 / 读回 / 更新。
func TestTokenRepository_BudgetFields_RoundTrip(t *testing.T) {
	tokens, _, _ := newBudgetTestRepos(t)
	ctx := context.Background()

	windowStart := time.Now().Truncate(time.Second) // 库里按秒存储
	tk := newValidToken(t)
	tk.BudgetQuota = 5000
	tk.BudgetPeriod = model.BudgetPeriodWeekly
	tk.BudgetWindowStart = windowStart
	tk.BudgetWindowBase = 1234
	if err := tokens.Create(ctx, tk); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}

	got, err := tokens.GetByID(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	if got.BudgetQuota != 5000 {
		t.Errorf("BudgetQuota = %d，期望 5000", got.BudgetQuota)
	}
	if got.BudgetPeriod != model.BudgetPeriodWeekly {
		t.Errorf("BudgetPeriod = %q，期望 weekly", got.BudgetPeriod)
	}
	if !got.BudgetWindowStart.Equal(windowStart) {
		t.Errorf("BudgetWindowStart = %v，期望 %v", got.BudgetWindowStart, windowStart)
	}
	if got.BudgetWindowBase != 1234 {
		t.Errorf("BudgetWindowBase = %d，期望 1234", got.BudgetWindowBase)
	}

	// Update 也必须带上预算四列（漏改 UPDATE 会让修改后回退到旧值）
	got.BudgetQuota = 9999
	got.BudgetPeriod = model.BudgetPeriodMonthly
	if err := tokens.Update(ctx, got); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}
	again, err := tokens.GetByID(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetByID(更新后) 失败: %v", err)
	}
	if again.BudgetQuota != 9999 || again.BudgetPeriod != model.BudgetPeriodMonthly {
		t.Errorf("更新未生效：quota=%d period=%q", again.BudgetQuota, again.BudgetPeriod)
	}

	// 存量令牌（不设预算）读回的窗口起点应为零值（0 → 零值时间），不能变成大负数时间戳
	plain := newValidToken(t)
	if err := tokens.Create(ctx, plain); err != nil {
		t.Fatalf("Create(存量形态) 失败: %v", err)
	}
	plainGot, err := tokens.GetByID(ctx, plain.ID)
	if err != nil {
		t.Fatalf("GetByID(存量形态) 失败: %v", err)
	}
	if !plainGot.BudgetWindowStart.IsZero() || plainGot.BudgetQuota != 0 || plainGot.BudgetPeriod != "" {
		t.Errorf("存量令牌预算列应全部为零值：%+v", plainGot)
	}
}

// TestTokenRepository_ResetBudgetWindow 验证惰性重置把基线对齐到当前已用额度。
func TestTokenRepository_ResetBudgetWindow(t *testing.T) {
	tokens, _, _ := newBudgetTestRepos(t)
	ctx := context.Background()

	tk := newValidToken(t)
	tk.BudgetQuota = 1000
	tk.BudgetPeriod = model.BudgetPeriodDaily
	tk.UsedQuota = 300 // 模拟"已产生消耗但窗口尚未锚定"
	if err := tokens.Create(ctx, tk); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}

	// 重置前：基线为 0，窗口已用 = used_quota
	before, err := tokens.GetByID(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	if used := before.BudgetWindowUsed(); used != 300 {
		t.Fatalf("重置前窗口已用 = %d，期望 300", used)
	}

	newStart := time.Now().Truncate(time.Second)
	if err := tokens.ResetBudgetWindow(ctx, tk.ID, newStart); err != nil {
		t.Fatalf("ResetBudgetWindow 失败: %v", err)
	}

	after, err := tokens.GetByID(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetByID(重置后) 失败: %v", err)
	}
	if used := after.BudgetWindowUsed(); used != 0 {
		t.Errorf("重置后窗口已用 = %d，期望 0（基线应对齐到 used_quota）", used)
	}
	if !after.BudgetWindowStart.Equal(newStart) {
		t.Errorf("重置后窗口起点 = %v，期望 %v", after.BudgetWindowStart, newStart)
	}
	if after.BudgetWindowBase != after.UsedQuota {
		t.Errorf("基线 %d 应等于当前 used_quota %d", after.BudgetWindowBase, after.UsedQuota)
	}
}

// TestTokenRepository_GroupSpendToday 验证按分组聚合当日消耗的口径。
func TestTokenRepository_GroupSpendToday(t *testing.T) {
	tokens, channels, logs := newBudgetTestRepos(t)
	ctx := context.Background()

	mkChannel := func(name, primary string, groups []string) *model.Channel {
		t.Helper()
		ch := newValidChannel()
		ch.Name = name
		ch.Group = primary
		ch.Groups = groups
		if err := channels.Create(ctx, ch); err != nil {
			t.Fatalf("创建渠道 %s 失败: %v", name, err)
		}
		return ch
	}
	vipCh := mkChannel("vip渠道", "vip", []string{"vip"})
	otherCh := mkChannel("其它渠道", "other", []string{"other"})
	multiCh := mkChannel("多分组渠道", "promo", []string{"promo", "vip"}) // 分组清单含 vip

	day := truncateToDayLocal(time.Now())
	mkLog := func(ch uint64, quota int64, status int, at time.Time) {
		t.Helper()
		entry := &model.UsageLog{
			UserID: 1, ChannelID: ch, Model: "gpt-4o",
			Quota: quota, StatusCode: status, LatencyMS: 1, CreatedAt: at,
		}
		if err := logs.Create(ctx, entry); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	mkLog(vipCh.ID, 100, 200, day.Add(1*time.Hour))                   // 计：vip 主分组命中
	mkLog(vipCh.ID, 50, 200, day.Add(2*time.Hour))                    // 计
	mkLog(vipCh.ID, 999, 500, day.Add(3*time.Hour))                   // 不计：失败请求
	mkLog(otherCh.ID, 777, 200, day.Add(4*time.Hour))                 // 不计：别的分组
	mkLog(multiCh.ID, 30, 200, day.Add(5*time.Hour))                  // 计：分组清单命中 vip
	mkLog(vipCh.ID, 888, 200, day.AddDate(0, 0, -1).Add(6*time.Hour)) // 不计：区间外（昨天）

	since, until := day, day.AddDate(0, 0, 1)
	total, err := tokens.GroupSpendToday(ctx, "vip", since, until)
	if err != nil {
		t.Fatalf("GroupSpendToday 失败: %v", err)
	}
	if want := int64(100 + 50 + 30); total != want {
		t.Errorf("vip 分组当日消耗 = %d，期望 %d", total, want)
	}

	// 其它分组只计自己那条
	other, err := tokens.GroupSpendToday(ctx, "other", since, until)
	if err != nil {
		t.Fatalf("GroupSpendToday(other) 失败: %v", err)
	}
	if other != 777 {
		t.Errorf("other 分组当日消耗 = %d，期望 777", other)
	}

	// 不存在 / 空分组：返回 0 而非报错
	if n, err := tokens.GroupSpendToday(ctx, "nonexistent", since, until); err != nil || n != 0 {
		t.Errorf("不存在的分组应返回 (0,nil)，实际 (%d,%v)", n, err)
	}
	if n, err := tokens.GroupSpendToday(ctx, "  ", since, until); err != nil || n != 0 {
		t.Errorf("空分组应返回 (0,nil)，实际 (%d,%v)", n, err)
	}
}
