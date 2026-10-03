// 渠道密钥池领域逻辑的单元测试。
//
// 测试重点：
//   - 批量粘贴解析：后台允许使用者直接从记事本粘几百行密钥，
//     必须容忍空行、注释、行尾备注、逗号分隔，并自动去重；
//   - 脱敏：界面上展示的必须是遮蔽后的密钥，绝不能泄露中段；
//   - 随机挑选：池内只有一个元素时不得返回 nil。
package model

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseKeyList_多行与备注与去重(t *testing.T) {
	raw := `# 这是注释行，应被忽略

nvapi-aaa111
nvapi-bbb222  池 #002
nvapi-ccc333,池 #003
  nvapi-ddd444
nvapi-aaa111
   `

	keys, labels := ParseKeyList(raw)

	want := []string{"nvapi-aaa111", "nvapi-bbb222", "nvapi-ccc333", "nvapi-ddd444"}
	if len(keys) != len(want) {
		t.Fatalf("解析出 %d 把密钥，期望 %d 把：%v", len(keys), len(want), keys)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Errorf("第 %d 把密钥应为 %q，实际 %q", i+1, k, keys[i])
		}
	}

	// 备注应与密钥一一对应（第一把无备注）
	if len(labels) != len(keys) {
		t.Fatalf("备注数量(%d)应与密钥数量(%d)一致", len(labels), len(keys))
	}
	if labels[0] != "" {
		t.Errorf("第一把密钥没有备注，实际 %q", labels[0])
	}
	if labels[1] != "池 #002" {
		t.Errorf("第二把密钥备注应为 %q，实际 %q", "池 #002", labels[1])
	}
	if labels[2] != "池 #003" {
		t.Errorf("第三把密钥备注（逗号分隔）应为 %q，实际 %q", "池 #003", labels[2])
	}
}

func TestParseKeyList_空输入(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\n\n", "# 只有注释\n"} {
		keys, labels := ParseKeyList(raw)
		if len(keys) != 0 || len(labels) != 0 {
			t.Errorf("输入 %q 应解析为空，实际 keys=%v labels=%v", raw, keys, labels)
		}
	}
}

func TestParseKeyList_支持五百行批量粘贴(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		// 每行必须唯一：解析层会去重，若测试数据自身重复就测不出"能解析 500 行"
		fmt.Fprintf(&sb, "nvapi-key-%04d\n", i)
	}

	keys, _ := ParseKeyList(sb.String())
	if len(keys) != 500 {
		t.Fatalf("应解析出 500 把密钥，实际 %d", len(keys))
	}
	if keys[0] != "nvapi-key-0000" || keys[499] != "nvapi-key-0499" {
		t.Fatalf("解析顺序异常：首=%q 末=%q", keys[0], keys[499])
	}
}

func TestChannelKey_Masked_不泄露中段(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"真实格式", "nvapi-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"短密钥", "short"},
		{"边界长度", "nvapi-1234567890ab"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := &ChannelKey{Key: tc.key}
			masked := k.Masked()

			if masked == "" {
				t.Fatal("脱敏结果不应为空")
			}
			if masked == tc.key {
				t.Fatal("脱敏结果与原文相同，等于没有脱敏")
			}
			// 中段必须被遮蔽
			if len(tc.key) > 12 {
				middle := tc.key[8 : len(tc.key)-4]
				if len(middle) > 0 && strings.Contains(masked, middle) {
					t.Fatalf("脱敏结果 %q 泄露了中段 %q", masked, middle)
				}
			}
			if !strings.Contains(masked, "****") && len(tc.key) > 12 {
				t.Fatalf("超过 12 位的密钥应包含掩码标记，实际 %q", masked)
			}
		})
	}
}

func TestPickKey_池内选择(t *testing.T) {
	// 空池返回 nil
	if got := PickKey(nil); got != nil {
		t.Fatalf("空池应返回 nil，实际 %v", got)
	}

	// 单元素直接返回该元素（这是最常见的情况：渠道只配了一把密钥）
	single := []*ChannelKey{{ID: 1, Key: "only"}}
	if got := PickKey(single); got == nil || got.ID != 1 {
		t.Fatalf("单元素池应返回该元素，实际 %v", got)
	}

	// 多元素：只返回池内元素（随机性不在此断言，只验证取值范围与不越界）
	pool := []*ChannelKey{{ID: 1}, {ID: 2}, {ID: 3}}
	seen := make(map[uint64]struct{})
	for i := 0; i < 300; i++ {
		got := PickKey(pool)
		if got == nil {
			t.Fatal("多元素池不应返回 nil")
		}
		seen[got.ID] = struct{}{}
	}
	// 300 次随机若只命中 1 个元素，说明选择逻辑退化为固定取第一个
	if len(seen) < 2 {
		t.Fatalf("随机挑选应在池内分散，实际只命中了 %d 个元素", len(seen))
	}
}

// TestParseKeyListWithBalance_按余额标记分段 覆盖站长实际粘贴的核心格式：
// 一个"49余额"标记之后的所有密钥都记 49，直到出现下一个标记。
func TestParseKeyListWithBalance_按余额标记分段(t *testing.T) {
	raw := `49余额
sk-aaaaaaaa
62余额
sk-bbbbbbbb
55余额
sk-cccccccc
sk-dddddddd`

	keys, labels, balances := ParseKeyListWithBalance(raw)

	wantKeys := []string{"sk-aaaaaaaa", "sk-bbbbbbbb", "sk-cccccccc", "sk-dddddddd"}
	wantBalances := []int64{49, 62, 55, 55}
	if len(keys) != len(wantKeys) {
		t.Fatalf("解析出 %d 把密钥，期望 %d 把：%v", len(keys), len(wantKeys), keys)
	}
	for i, k := range wantKeys {
		if keys[i] != k {
			t.Errorf("第 %d 把密钥应为 %q，实际 %q", i+1, k, keys[i])
		}
		if balances[i] != wantBalances[i] {
			t.Errorf("第 %d 把密钥余额应为 %d，实际 %d", i+1, wantBalances[i], balances[i])
		}
	}
	// labels 必须与 keys 一一对应
	if len(labels) != len(keys) {
		t.Fatalf("备注数量(%d)应与密钥数量(%d)一致", len(labels), len(keys))
	}
	// 余额标记行本身不能变成密钥
	for _, k := range keys {
		if strings.Contains(k, "余额") {
			t.Fatalf("余额标记行被误当成密钥：%q", k)
		}
	}
}

// TestParseKeyListWithBalance_标记写法与注释与去重 覆盖各种余额标记写法、
// 注释忽略、无标记为未知、以及去重时"保留首次出现的余额"。
func TestParseKeyListWithBalance_标记写法与注释与去重(t *testing.T) {
	raw := `sk-before
余额 30
余额49
# 这一行是注释，不该影响余额
sk-a
余额: 62
sk-b
余额：95
sk-c
sk-a`

	keys, _, balances := ParseKeyListWithBalance(raw)

	wantKeys := []string{"sk-before", "sk-a", "sk-b", "sk-c"}
	wantBalances := []int64{BalanceUnknown, 49, 62, 95}
	if len(keys) != len(wantKeys) {
		t.Fatalf("解析出 %d 把密钥，期望 %d 把：%v", len(keys), len(wantKeys), keys)
	}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Errorf("第 %d 把密钥应为 %q，实际 %q", i+1, wantKeys[i], keys[i])
		}
		if balances[i] != wantBalances[i] {
			t.Errorf("第 %d 把密钥余额应为 %d，实际 %d", i+1, wantBalances[i], balances[i])
		}
	}
}

// TestParseKeyListWithBalance_行内数字备注不当作余额 固化"刻意不支持 sk-xxx 49"的取舍：
// 末尾的纯数字仍按备注处理，余额保持未知，避免误伤合法的数字备注。
func TestParseKeyListWithBalance_行内数字备注不当作余额(t *testing.T) {
	keys, labels, balances := ParseKeyListWithBalance("sk-aaa 49")

	if len(keys) != 1 || keys[0] != "sk-aaa" {
		t.Fatalf("密钥解析错误：keys=%v", keys)
	}
	if labels[0] != "49" {
		t.Fatalf("末尾数字应被当作备注 %q，实际 %q", "49", labels[0])
	}
	if balances[0] != BalanceUnknown {
		t.Fatalf("行内数字不应被当作余额，期望未知(%d)，实际 %d", BalanceUnknown, balances[0])
	}
}

// TestChannelKey_BalanceExhausted_语义 固化余额"未知/已知/耗尽"的判定边界。
func TestChannelKey_BalanceExhausted_语义(t *testing.T) {
	cases := []struct {
		name    string
		balance int64
		want    bool
	}{
		{"未知(-1)", BalanceUnknown, false},
		{"余额为0(已用尽)", 0, true},
		{"余额为正", 55, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := &ChannelKey{Balance: tc.balance}
			if got := k.BalanceExhausted(); got != tc.want {
				t.Fatalf("Balance=%d 时 BalanceExhausted 应为 %v，实际 %v", tc.balance, tc.want, got)
			}
		})
	}
}

// TestChannelKey_QuotaExhausted_仍以主窗口为准 是迁移 0048 的回归保护。
//
// 引入次窗口（每周）后调度判定必须【逐字不变】：只有主窗口已满才算耗尽，
// 次窗口无论多满都不影响可用性——否则存量账号会在升级瞬间被错误跳过。
func TestChannelKey_QuotaExhausted_仍以主窗口为准(t *testing.T) {
	now := time.Now()
	future := now.Add(2 * time.Hour)

	// 次窗口已满、主窗口未满 → 不应判定耗尽（这是本次改动最关键的回归点）
	k := &ChannelKey{
		QuotaUsedPercent:          10,
		QuotaResetAt:              future,
		QuotaSecondaryUsedPercent: 100,
		QuotaSecondaryResetAt:     future,
	}
	if k.QuotaExhausted(now) {
		t.Fatal("次窗口已满不应导致账号被跳过（调度只看主窗口）")
	}

	// 主窗口已满且未到重置 → 耗尽
	k = &ChannelKey{QuotaUsedPercent: 100, QuotaResetAt: future, QuotaSecondaryUsedPercent: 10}
	if !k.QuotaExhausted(now) {
		t.Fatal("主窗口已满且未到重置时应判定耗尽")
	}

	// 主窗口已满但重置时刻已过 → 视为已恢复（快照过时）
	k = &ChannelKey{QuotaUsedPercent: 100, QuotaResetAt: now.Add(-time.Minute)}
	if k.QuotaExhausted(now) {
		t.Fatal("主窗口重置时刻已过应视为额度已恢复")
	}

	// 主窗口未探测 → 不施加任何额度约束（哪怕次窗口是 100）
	k = &ChannelKey{QuotaUsedPercent: QuotaUsedPercentUnknown, QuotaSecondaryUsedPercent: 100}
	if k.QuotaExhausted(now) {
		t.Fatal("主窗口未探测时不应判定耗尽")
	}
}

// TestChannelKey_QuotaSecondaryKnown_语义 验证次窗口"未知"判定与主窗口同构。
func TestChannelKey_QuotaSecondaryKnown_语义(t *testing.T) {
	if (&ChannelKey{QuotaSecondaryUsedPercent: QuotaUsedPercentUnknown}).QuotaSecondaryKnown() {
		t.Fatal("-1 应表示次窗口未探测")
	}
	if !(&ChannelKey{QuotaSecondaryUsedPercent: 0}).QuotaSecondaryKnown() {
		t.Fatal("已探测到的 0%（完全没用）应视为已知")
	}
}
