// 本文件验证「按成功率自动禁用渠道」的判定规则与端到端处置。
//
// 意图（Why）：
//
//	自动停用会直接影响线上可用性：判错方向时，要么"该停的没停"（持续失败拖垮体验），
//	要么"不该停的停了"（把健康渠道误杀）。因此把四条关键语义用测试钉死：
//	样本不足不停用、成功率达标不停用、低于阈值停用、已停用渠道不重复处置。
//
// 流转（Flow）：
//
//	go test ./internal/server/ → 纯判定函数表驱动 + 基于真实 SQLite 的端到端处置
package server

import (
	"context"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/config"
	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// TestShouldAutoDisable 表驱动覆盖自动停用的判定规则（纯函数，无副作用）。
func TestShouldAutoDisable(t *testing.T) {
	base := config.ChannelHealthConfig{MinRequests: 20, SuccessRate: 0.5}

	cases := []struct {
		name string
		stat channelHealthStat
		cfg  config.ChannelHealthConfig
		want bool
	}{
		{
			name: "样本不足_不停用",
			stat: channelHealthStat{Status: model.ChannelStatusEnabled, Requests: 19, SuccessRate: 0},
			cfg:  base,
			want: false,
		},
		{
			name: "成功率达标_不停用",
			stat: channelHealthStat{Status: model.ChannelStatusEnabled, Requests: 100, SuccessRate: 0.5},
			cfg:  base,
			want: false,
		},
		{
			name: "成功率高于阈值_不停用",
			stat: channelHealthStat{Status: model.ChannelStatusEnabled, Requests: 100, SuccessRate: 0.9},
			cfg:  base,
			want: false,
		},
		{
			name: "成功率低于阈值_停用",
			stat: channelHealthStat{Status: model.ChannelStatusEnabled, Requests: 100, SuccessRate: 0.2},
			cfg:  base,
			want: true,
		},
		{
			name: "已自动禁用_不重复处置",
			stat: channelHealthStat{Status: model.ChannelStatusAutoDisabled, Requests: 100, SuccessRate: 0},
			cfg:  base,
			want: false,
		},
		{
			name: "已人工禁用_不处置",
			stat: channelHealthStat{Status: model.ChannelStatusDisabled, Requests: 100, SuccessRate: 0},
			cfg:  base,
			want: false,
		},
		{
			name: "功能关闭_不停用",
			stat: channelHealthStat{Status: model.ChannelStatusEnabled, Requests: 100, SuccessRate: 0},
			cfg:  config.ChannelHealthConfig{MinRequests: 0, SuccessRate: 0.5},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoDisable(tc.stat, tc.cfg); got != tc.want {
				t.Fatalf("shouldAutoDisable = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// createHealthChannel 在测试库中建一个渠道并写入指定数量的成功/失败调用日志。
func createHealthChannel(t *testing.T, srv *Server, success, failed int, status model.ChannelStatus) *model.Channel {
	t.Helper()
	ctx := context.Background()

	ch := &model.Channel{
		Name:    "健康检查测试渠道",
		Type:    1,
		BaseURL: "https://example.com",
		APIKey:  "sk-test-not-a-real-key",
		Models:  []string{"gpt-4o"},
		Group:   model.DefaultGroupName,
		Groups:  []string{model.DefaultGroupName},
		Weight:  1,
		Status:  status,
	}
	if err := srv.deps.Channels.Create(ctx, ch); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}

	now := time.Now()
	writeLogs := func(code int, n int) {
		for i := 0; i < n; i++ {
			entry := &model.UsageLog{
				ChannelID:  ch.ID,
				Model:      "gpt-4o",
				StatusCode: code,
				CreatedAt:  now,
			}
			if err := srv.deps.UsageLogs.Create(ctx, entry); err != nil {
				t.Fatalf("写入调用日志失败: %v", err)
			}
		}
	}
	writeLogs(200, success)
	writeLogs(500, failed)
	return ch
}

// TestAutoDisableUnhealthyChannels 端到端验证：低于阈值被自动停用，其余情况保持不变。
func TestAutoDisableUnhealthyChannels(t *testing.T) {
	cases := []struct {
		name       string
		success    int
		failed     int
		status     model.ChannelStatus
		cfg        config.ChannelHealthConfig
		wantStatus model.ChannelStatus
	}{
		{
			name:       "成功率低于阈值_自动停用",
			success:    1,
			failed:     9,
			status:     model.ChannelStatusEnabled,
			cfg:        config.ChannelHealthConfig{MinRequests: 5, SuccessRate: 0.5, WindowMinutes: 15},
			wantStatus: model.ChannelStatusAutoDisabled,
		},
		{
			name:       "成功率达标_保持启用",
			success:    9,
			failed:     1,
			status:     model.ChannelStatusEnabled,
			cfg:        config.ChannelHealthConfig{MinRequests: 5, SuccessRate: 0.5, WindowMinutes: 15},
			wantStatus: model.ChannelStatusEnabled,
		},
		{
			name:       "样本不足_保持启用",
			success:    0,
			failed:     2,
			status:     model.ChannelStatusEnabled,
			cfg:        config.ChannelHealthConfig{MinRequests: 5, SuccessRate: 0.5, WindowMinutes: 15},
			wantStatus: model.ChannelStatusEnabled,
		},
		{
			name:       "已人工禁用_不重复处置",
			success:    0,
			failed:     10,
			status:     model.ChannelStatusDisabled,
			cfg:        config.ChannelHealthConfig{MinRequests: 5, SuccessRate: 0.5, WindowMinutes: 15},
			wantStatus: model.ChannelStatusDisabled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(t)
			ctx := context.Background()
			ch := createHealthChannel(t, srv, tc.success, tc.failed, tc.status)

			srv.deps.Config.ChannelHealth = tc.cfg
			srv.AutoDisableUnhealthyChannels(ctx)

			got, err := srv.deps.Channels.GetByID(ctx, ch.ID)
			if err != nil {
				t.Fatalf("重新载入渠道失败: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("渠道状态 = %d(%s)，期望 %d(%s)",
					int(got.Status), got.Status, int(tc.wantStatus), tc.wantStatus)
			}
		})
	}
}

// TestAutoDisableUnhealthyChannels_DisabledByDefault 验证默认配置（最小样本数 0）下不介入。
func TestAutoDisableUnhealthyChannels_DisabledByDefault(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// 全部失败，但保持默认配置（MinRequests=0=不启用）
	ch := createHealthChannel(t, srv, 0, 10, model.ChannelStatusEnabled)

	srv.AutoDisableUnhealthyChannels(ctx)

	got, err := srv.deps.Channels.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("重新载入渠道失败: %v", err)
	}
	if got.Status != model.ChannelStatusEnabled {
		t.Fatalf("默认配置下渠道不应被自动停用，实际状态 = %d(%s)", int(got.Status), got.Status)
	}
}
