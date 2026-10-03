// 本文件验证新增配置项：管理面 CIDR 白名单 与 渠道自动禁用阈值。
//
// 意图（Why）：
//
//	这两项都直接关系安全与线上稳定性，必须锁定语义：
//	  - 白名单"非法 CIDR 必须让启动失败"（fail-closed），绝不能静默放行；
//	  - 白名单"未配置=不限制"，保证存量部署升级后行为不变；
//	  - 渠道自动禁用默认关闭（最小样本数 0），避免升级后误停正常渠道。
//
// 流转（Flow）：
//
//	go test ./internal/config/ → 覆盖 ParseAdminAllowCIDRs / applyEnv / Validate
package config

import (
	"net/netip"
	"testing"
)

// clearNewEnv 清空本次新增配置项相关的环境变量，保证用例不受外部环境影响。
// 说明：与 config_test.go 的 neutralizeEnv 保持一致的做法（空值视为未设置）。
func clearNewEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AQUA_ADMIN_ALLOW_CIDRS",
		"AQUA_CHANNEL_AUTO_DISABLE_MIN_REQUESTS",
		"AQUA_CHANNEL_AUTO_DISABLE_SUCCESS_RATE",
		"AQUA_CHANNEL_AUTO_DISABLE_WINDOW_MINUTES",
	} {
		t.Setenv(k, "")
	}
}

// TestParseAdminAllowCIDRs 覆盖白名单文本解析的各类输入。
func TestParseAdminAllowCIDRs(t *testing.T) {
	t.Run("空输入返回nil表示不限制", func(t *testing.T) {
		if got, err := ParseAdminAllowCIDRs(nil); err != nil || got != nil {
			t.Fatalf("nil 输入应返回 (nil, nil)，实际 (%v, %v)", got, err)
		}
		if got, err := ParseAdminAllowCIDRs([]string{}); err != nil || got != nil {
			t.Fatalf("空切片应返回 (nil, nil)，实际 (%v, %v)", got, err)
		}
		if got, err := ParseAdminAllowCIDRs([]string{"   ", ""}); err != nil || got != nil {
			t.Fatalf("全空白项应返回 (nil, nil)，实际 (%v, %v)", got, err)
		}
	})

	t.Run("合法CIDR被解析", func(t *testing.T) {
		got, err := ParseAdminAllowCIDRs([]string{"10.0.0.0/8", " 1.2.3.4/32 "})
		if err != nil {
			t.Fatalf("合法 CIDR 不应报错: %v", err)
		}
		want := []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("1.2.3.4/32"),
		}
		if len(got) != len(want) {
			t.Fatalf("解析结果长度 = %d，期望 %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("第 %d 项 = %v，期望 %v", i, got[i], want[i])
			}
		}
	})

	t.Run("主机位非零会被Masked归一", func(t *testing.T) {
		got, err := ParseAdminAllowCIDRs([]string{"10.1.2.3/8"})
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if got[0].String() != "10.0.0.0/8" {
			t.Errorf("归一结果 = %v，期望 10.0.0.0/8", got[0])
		}
	})

	t.Run("非法CIDR报错且不静默跳过", func(t *testing.T) {
		if _, err := ParseAdminAllowCIDRs([]string{"10.0.0.0/8", "not-a-cidr"}); err == nil {
			t.Fatal("含非法条目时应返回错误，实际返回 nil（危险：静默忽略会让站长误以为白名单生效）")
		}
		if _, err := ParseAdminAllowCIDRs([]string{"192.168.0.0/33"}); err == nil {
			t.Fatal("掩码超过 32 的 CIDR 应返回错误")
		}
	})
}

// TestDefault_ChannelHealthDisabled 验证渠道自动禁用默认关闭。
func TestDefault_ChannelHealthDisabled(t *testing.T) {
	cfg := Default()

	if cfg.ChannelHealth.MinRequests != 0 {
		t.Errorf("默认最小样本数 = %d，期望 0（默认不启用，避免升级后误停渠道）", cfg.ChannelHealth.MinRequests)
	}
	if cfg.ChannelHealth.SuccessRate != DefaultChannelAutoDisableSuccessRate {
		t.Errorf("默认成功率下限 = %v，期望 %v", cfg.ChannelHealth.SuccessRate, DefaultChannelAutoDisableSuccessRate)
	}
	if cfg.ChannelHealth.WindowMinutes != DefaultChannelAutoDisableWindowMinutes {
		t.Errorf("默认窗口 = %d，期望 %d", cfg.ChannelHealth.WindowMinutes, DefaultChannelAutoDisableWindowMinutes)
	}
	if len(cfg.AdminAllowCIDRs) != 0 {
		t.Errorf("白名单默认应为空（不限制），实际 = %v", cfg.AdminAllowCIDRs)
	}
}

// TestLoad_AdminAllowCIDRsFromEnv 验证逗号分隔的环境变量被正确切分。
func TestLoad_AdminAllowCIDRsFromEnv(t *testing.T) {
	neutralizeEnv(t)
	clearNewEnv(t)
	setTestAppKey(t)
	// 含多余空白与空项，均应被规整掉
	t.Setenv("AQUA_ADMIN_ALLOW_CIDRS", " 10.0.0.0/8 , 1.2.3.4/32 ,, ")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	want := []string{"10.0.0.0/8", "1.2.3.4/32"}
	if len(cfg.AdminAllowCIDRs) != len(want) {
		t.Fatalf("白名单 = %v，期望 %v", cfg.AdminAllowCIDRs, want)
	}
	for i := range want {
		if cfg.AdminAllowCIDRs[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, cfg.AdminAllowCIDRs[i], want[i])
		}
	}
}

// TestLoad_ChannelHealthFromEnv 验证渠道自动禁用阈值可由环境变量注入。
func TestLoad_ChannelHealthFromEnv(t *testing.T) {
	neutralizeEnv(t)
	clearNewEnv(t)
	setTestAppKey(t)
	t.Setenv("AQUA_CHANNEL_AUTO_DISABLE_MIN_REQUESTS", "50")
	t.Setenv("AQUA_CHANNEL_AUTO_DISABLE_SUCCESS_RATE", "0.9")
	t.Setenv("AQUA_CHANNEL_AUTO_DISABLE_WINDOW_MINUTES", "30")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.ChannelHealth.MinRequests != 50 {
		t.Errorf("最小样本数 = %d，期望 50", cfg.ChannelHealth.MinRequests)
	}
	if cfg.ChannelHealth.SuccessRate != 0.9 {
		t.Errorf("成功率下限 = %v，期望 0.9", cfg.ChannelHealth.SuccessRate)
	}
	if cfg.ChannelHealth.WindowMinutes != 30 {
		t.Errorf("窗口 = %d，期望 30", cfg.ChannelHealth.WindowMinutes)
	}
}

// TestValidate_RejectsInvalidAdminCIDR 验证非法 CIDR 会让校验失败（fail-closed）。
func TestValidate_RejectsInvalidAdminCIDR(t *testing.T) {
	cfg := Default()
	cfg.Security.AppKey = testAppKey

	cfg.AdminAllowCIDRs = []string{"192.168.0.0/33"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("非法 CIDR 应导致校验失败，实际通过")
	}

	cfg.AdminAllowCIDRs = []string{"10.0.0.0/8", "1.2.3.4/32"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("合法白名单应通过校验，实际: %v", err)
	}
}

// TestValidate_ChannelHealthBounds 覆盖渠道自动禁用阈值的边界校验。
func TestValidate_ChannelHealthBounds(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"关闭状态合法", func(c *Config) { c.ChannelHealth.MinRequests = 0 }, false},
		{"正常阈值合法", func(c *Config) { c.ChannelHealth.MinRequests = 20; c.ChannelHealth.SuccessRate = 0.5 }, false},
		{"成功率下限为0合法", func(c *Config) { c.ChannelHealth.SuccessRate = 0 }, false},
		{"成功率上限为1合法", func(c *Config) { c.ChannelHealth.SuccessRate = 1 }, false},
		{"成功率小于0非法", func(c *Config) { c.ChannelHealth.SuccessRate = -0.1 }, true},
		{"成功率大于1非法", func(c *Config) { c.ChannelHealth.SuccessRate = 1.1 }, true},
		{"最小样本数为负非法", func(c *Config) { c.ChannelHealth.MinRequests = -1 }, true},
		{"窗口为负非法", func(c *Config) { c.ChannelHealth.WindowMinutes = -1 }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Security.AppKey = testAppKey
			tc.mutate(cfg)

			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("期望校验失败，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("期望校验通过，实际失败: %v", err)
			}
		})
	}
}
