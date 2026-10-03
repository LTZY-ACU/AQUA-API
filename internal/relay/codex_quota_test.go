// 订阅账号额度探测的单元测试（纯函数部分）。
//
// 意图（Why）：
//
//	额度快照直接影响调度决策——"已用满就跳过、窗口重置后回池"。
//	因此解析与地址推导这两处的错值后果不是显示难看，而是
//	"账号被长期闲置"或"一直把请求派给已满账号"。
//	网络调用本身由集成验证覆盖，这里只钉住纯逻辑。
//
// 流转（Flow）：
//
//	go test ./internal/relay/ -run "CodexQuota|CodexUsage|UsedPercent"
package relay

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestNormalizeUsedPercent_边界 覆盖越界值与四舍五入。
func TestNormalizeUsedPercent_边界(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  int
	}{
		{"正常值四舍五入", 42.6, 43},
		{"恰好 100", 100, 100},
		{"超过 100 夹到 100", 100.5, 100},
		{"负数视为未探测", -1, -1},
		{"零是真实值", 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeUsedPercent(tc.value); got != tc.want {
				t.Errorf("normalizeUsedPercent(%v) = %d，期望 %d", tc.value, got, tc.want)
			}
		})
	}
}

// TestCodexUsageEndpoint_地址推导 覆盖标准地址、镜像与异常地址三种情况。
func TestCodexUsageEndpoint_地址推导(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "标准地址",
			baseURL: "https://chatgpt.com/backend-api/codex",
			want:    "https://chatgpt.com/backend-api/wham/usage",
		},
		{
			name:    "带尾斜杠",
			baseURL: "https://chatgpt.com/backend-api/codex/",
			want:    "https://chatgpt.com/backend-api/wham/usage",
		},
		{
			name:    "自建镜像：同源换主机",
			baseURL: "https://mirror.example.com/backend-api/codex",
			want:    "https://mirror.example.com/backend-api/wham/usage",
		},
		{
			name:    "非标准地址：按同源根兜底",
			baseURL: "https://odd.example.com/some/other/path",
			want:    "https://odd.example.com/backend-api/wham/usage",
		},
		{
			name:    "空地址：用官方兜底",
			baseURL: "",
			want:    "https://chatgpt.com/backend-api/wham/usage",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexUsageEndpoint(tc.baseURL); got != tc.want {
				t.Errorf("codexUsageEndpoint(%q) = %q，期望 %q", tc.baseURL, got, tc.want)
			}
		})
	}
}

// TestToCodexQuota_字段映射与缺失处理 覆盖"上游返回 null"这种常见情况。
func TestToCodexQuota_字段映射与缺失处理(t *testing.T) {
	// 正常窗口
	var payload codexUsageResponse
	if err := json.Unmarshal([]byte(`{
		"email": "user@example.com",
		"plan_type": "plus",
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {"used_percent": 37.5, "limit_window_seconds": 18000, "reset_after_seconds": 3600}
		}
	}`), &payload); err != nil {
		t.Fatalf("解析样例失败: %v", err)
	}
	quota := toCodexQuota(&payload)
	if quota.PlanType != "plus" || quota.Email != "user@example.com" {
		t.Errorf("套餐/邮箱未正确映射：%#v", quota)
	}
	if quota.UsedPercent != 38 {
		t.Errorf("used_percent = %d，期望 38（37.5 四舍五入）", quota.UsedPercent)
	}
	if quota.ResetAt.IsZero() {
		t.Error("提供 reset_after_seconds 时应推导出重置时间")
	}

	// 上游没有窗口（返回 null）：必须记为"未知"，不能记 0
	// 因为 0 表示"完全没用"，界面与调度都会据此认为额度充足。
	empty := codexUsageResponse{}
	if got := toCodexQuota(&empty); got.UsedPercent != -1 {
		t.Errorf("无窗口时应记为未知(-1)，实际 %d", got.UsedPercent)
	}
}

// TestToCodexQuota_双窗口解析 覆盖"两个都有 / 只有主窗口 / 都缺失"三种形态。
//
// 为什么必须钉住：此前只读取主窗口、把次窗口（每周）丢弃，
// 结果是周额度将满时没有任何预警（"明明没怎么用，账号却突然全满"）。
// 同时要保证缺失的窗口记为"未知"(-1)，而不是被 0 冒充成"完全没用"。
func TestToCodexQuota_双窗口解析(t *testing.T) {
	t.Run("两个窗口都有", func(t *testing.T) {
		var payload codexUsageResponse
		if err := json.Unmarshal([]byte(`{
			"plan_type": "pro",
			"rate_limit": {
				"primary_window": {"used_percent": 20.4, "limit_window_seconds": 18000, "reset_after_seconds": 3600},
				"secondary_window": {"used_percent": 88.0, "limit_window_seconds": 604800, "reset_after_seconds": 7200}
			}
		}`), &payload); err != nil {
			t.Fatalf("解析样例失败: %v", err)
		}
		quota := toCodexQuota(&payload)
		if quota.UsedPercent != 20 || quota.PrimaryWindowSeconds != 18000 {
			t.Errorf("主窗口解析不符: used=%d win=%d", quota.UsedPercent, quota.PrimaryWindowSeconds)
		}
		if quota.SecondaryUsedPercent != 88 || quota.SecondaryWindowSeconds != 604800 {
			t.Errorf("次窗口解析不符: used=%d win=%d", quota.SecondaryUsedPercent, quota.SecondaryWindowSeconds)
		}
		if quota.ResetAt.IsZero() || quota.SecondaryResetAt.IsZero() {
			t.Error("两个窗口都应能从 reset_after_seconds 推导出重置时间")
		}
	})

	t.Run("只有主窗口", func(t *testing.T) {
		var payload codexUsageResponse
		if err := json.Unmarshal([]byte(`{
			"rate_limit": {"primary_window": {"used_percent": 50, "reset_at": 1893456000}}
		}`), &payload); err != nil {
			t.Fatalf("解析样例失败: %v", err)
		}
		quota := toCodexQuota(&payload)
		if quota.UsedPercent != 50 {
			t.Errorf("主窗口应为 50，实际 %d", quota.UsedPercent)
		}
		if quota.ResetAt.Unix() != 1893456000 {
			t.Errorf("主窗口重置时间应优先取绝对时间 reset_at，实际 %v", quota.ResetAt)
		}
		if quota.SecondaryUsedPercent != -1 {
			t.Errorf("缺失的次窗口应记为未知(-1)，实际 %d", quota.SecondaryUsedPercent)
		}
		if !quota.SecondaryResetAt.IsZero() {
			t.Errorf("缺失的次窗口重置时间应为零值，实际 %v", quota.SecondaryResetAt)
		}
	})

	t.Run("两个都缺失", func(t *testing.T) {
		quota := toCodexQuota(&codexUsageResponse{})
		if quota.UsedPercent != -1 || quota.SecondaryUsedPercent != -1 {
			t.Errorf("两个窗口都应记为未知(-1)，实际 primary=%d secondary=%d",
				quota.UsedPercent, quota.SecondaryUsedPercent)
		}
	})
}

// TestToCodexQuota_触顶优先 覆盖"上游说触顶但百分比未到 100"的窗口切换瞬间。
func TestToCodexQuota_触顶优先(t *testing.T) {
	var payload codexUsageResponse
	if err := json.Unmarshal([]byte(`{
		"plan_type": "pro",
		"rate_limit": {"limit_reached": true, "primary_window": {"used_percent": 12.0}}
	}`), &payload); err != nil {
		t.Fatalf("解析样例失败: %v", err)
	}
	quota := toCodexQuota(&payload)
	if quota.UsedPercent != 100 {
		t.Errorf("上游明确触顶时应记为 100，实际 %d（否则会继续把请求派给它并立刻失败）", quota.UsedPercent)
	}
}

// TestCodexQuotaResetHint_解析与夹取 覆盖限流恢复提示。
func TestCodexQuotaResetHint_解析与夹取(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration // 0 表示不采信
	}{
		{
			name: "配额类错误 + resets_in_seconds",
			body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":7200}}`,
			want: 2 * time.Hour,
		},
		{
			name: "普通限流不采信（退避更合适）",
			body: `{"error":{"type":"rate_limit_error","resets_in_seconds":7200}}`,
			want: 0,
		},
		{
			name: "过短的提示不采信",
			body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":5}}`,
			want: 0,
		},
		{
			name: "过长的提示不采信（可能是异常值）",
			body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":604800}}`,
			want: 0,
		},
		{
			name: "非 JSON",
			body: `plain text error`,
			want: 0,
		},
		{
			name: "空体",
			body: ``,
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexQuotaResetHint([]byte(tc.body)); got != tc.want {
				t.Errorf("codexQuotaResetHint = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestCodexQuotaResetHint_绝对时间 覆盖上游给 resets_at 的形态。
func TestCodexQuotaResetHint_绝对时间(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Unix()
	body := `{"error":{"type":"usage_limit_reached","resets_at":` +
		strconv.FormatInt(resetAt, 10) + `}}`

	got := codexQuotaResetHint([]byte(body))
	if got <= 0 {
		t.Fatalf("应采信绝对时间提示，实际 %v", got)
	}
	// 允许若干秒误差（测试执行耗时）
	if got < 88*time.Minute || got > 90*time.Minute+2*time.Second {
		t.Errorf("推导出的恢复时间偏差过大：%v", got)
	}
}
