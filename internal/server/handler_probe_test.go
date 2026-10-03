// 本文件覆盖「渠道测活」的两块核心逻辑：
//  1. 探测凭据的选择（必须跳过冷却中与余额耗尽的密钥，且结果确定）；
//  2. 状态码 → 结论文案（管理员据此决定下一步动作）。
//
// 为什么单独测：测活给出的是"渠道能不能用"的结论，一旦结论被冷却中的密钥、
// 被随机挑中的坏密钥污染，管理员就会去改本来正确的配置——属于最贵的误导。
package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestPickProbeCredential_跳过冷却与余额耗尽 验证选密钥的规则与池内统计。
func TestPickProbeCredential_跳过冷却与余额耗尽(t *testing.T) {
	fx := newChannelStrategyFixture(t)
	ctx := context.Background()

	ch := addChannelForProbe(t, fx, []string{"probe-model"})
	// 池里放三把：第 2 把冷却中、第 3 把余额已耗尽
	if _, _, err := fx.keys.ReplaceAll(ctx, ch.ID, []string{"sk-k1", "sk-k2", "sk-k3"}, nil); err != nil {
		t.Fatalf("导入密钥失败: %v", err)
	}
	pool, err := fx.keys.ListByChannel(ctx, ch.ID)
	if err != nil {
		t.Fatalf("读取密钥失败: %v", err)
	}
	if len(pool) != 3 {
		t.Fatalf("应有 3 把密钥，实际 %d", len(pool))
	}
	// 按密钥原文定位（而非按下标）：批量导入落库后的行序不保证与输入一致，
	// 用下标会写出一个"偶发失败"的用例。
	byValue := make(map[string]*model.ChannelKey, len(pool))
	for _, k := range pool {
		byValue[k.Key] = k
	}
	if byValue["sk-k2"] == nil || byValue["sk-k3"] == nil {
		t.Fatalf("未按预期导入密钥：%v", byValue)
	}
	if err := fx.keys.SetCooldown(ctx, byValue["sk-k2"].ID, time.Now().Add(10*time.Minute), "测试：冷却中"); err != nil {
		t.Fatalf("设置冷却失败: %v", err)
	}
	if err := fx.keys.UpdateBalance(ctx, byValue["sk-k3"].ID, 0); err != nil {
		t.Fatalf("设置余额失败: %v", err)
	}

	key, cred, errMsg := fx.srv.pickProbeCredential(ctx, ch)
	if errMsg != "" {
		t.Fatalf("应当选出可用密钥，却返回错误：%s", errMsg)
	}
	// 确定性：应取 ID 最小的一把可用密钥（第 1 把）
	if key != "sk-k1" {
		t.Fatalf("应选中第一把可用密钥，实际 %q", key)
	}
	if cred.source != "pool" {
		t.Fatalf("凭据来源应为 pool，实际 %q", cred.source)
	}
	if cred.poolTotal != 3 || cred.poolAvailable != 1 || cred.poolCooling != 1 || cred.poolExhausted != 1 {
		t.Fatalf("池内统计不符：%+v", cred)
	}
	if cred.masked == "" {
		t.Fatal("应返回本次使用的凭据掩码（便于管理员对照）")
	}
}

// TestPickProbeCredential_池内无可用时给出可行动结论 验证"池空了"不被掩盖。
func TestPickProbeCredential_池内无可用时给出可行动结论(t *testing.T) {
	fx := newChannelStrategyFixture(t)
	ctx := context.Background()

	ch := addChannelForProbe(t, fx, []string{"probe-model"})
	if _, _, err := fx.keys.ReplaceAll(ctx, ch.ID, []string{"sk-a", "sk-b"}, nil); err != nil {
		t.Fatalf("导入密钥失败: %v", err)
	}
	pool, _ := fx.keys.ListByChannel(ctx, ch.ID)
	for _, k := range pool {
		if err := fx.keys.SetCooldown(ctx, k.ID, time.Now().Add(time.Hour), "测试：全部冷却"); err != nil {
			t.Fatalf("设置冷却失败: %v", err)
		}
	}

	key, cred, errMsg := fx.srv.pickProbeCredential(ctx, ch)
	if key != "" {
		t.Fatalf("没有可用密钥时不应返回密钥，实际 %q", key)
	}
	if !strings.Contains(errMsg, "没有可用密钥") {
		t.Fatalf("错误说明应指出池内无可用密钥，实际 %q", errMsg)
	}
	// 错误里必须带上分类数字，管理员才知道是"都在冷却"还是"都被摘了"
	if !strings.Contains(errMsg, "冷却中 2") {
		t.Fatalf("错误说明应包含冷却数量，实际 %q", errMsg)
	}
	if cred.poolAvailable != 0 || cred.poolCooling != 2 {
		t.Fatalf("池内统计不符：%+v", cred)
	}
}

// TestPickProbeCredential_无凭据时提示补配置 覆盖"池为空且单密钥也为空"。
func TestPickProbeCredential_无凭据时提示补配置(t *testing.T) {
	fx := newChannelStrategyFixture(t)
	ctx := context.Background()

	ch := addChannelForProbe(t, fx, []string{"probe-model"})
	// 模拟"只填了批量密钥"的渠道：单密钥为空、池也为空
	ch.APIKey = ""
	key, cred, errMsg := fx.srv.pickProbeCredential(ctx, ch)
	if key != "" || cred.source != "single" {
		t.Fatalf("无池时应回退单密钥，实际 key=%q source=%q", key, cred.source)
	}
	if !strings.Contains(errMsg, "没有任何可用凭据") {
		t.Fatalf("应提示没有任何可用凭据，实际 %q", errMsg)
	}
}

// TestDescribeProbeStatus_结论覆盖关键状态码 验证 4xx/5xx 的处置指引。
func TestDescribeProbeStatus_结论覆盖关键状态码(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"成功", http.StatusOK, `{"choices":[]}`, []string{"连通正常"}},
		{"鉴权失败", http.StatusUnauthorized, `{"error":"invalid api key"}`, []string{"鉴权失败", "invalid api key"}},
		{"限流", http.StatusTooManyRequests, `{"error":"rate limit"}`, []string{"限流"}},
		{"模型不存在", http.StatusNotFound, `{"error":"model not found"}`, []string{"404", "model not found", "BaseURL"}},
		{"上游故障", http.StatusBadGateway, `bad gateway`, []string{"502", "上游服务端异常", "bad gateway"}},
	}
	for _, tc := range cases {
		got := describeProbeStatus(tc.status, tc.body, "渠道单密钥")
		for _, needle := range tc.want {
			if !strings.Contains(got, needle) {
				t.Errorf("%s：结论应包含 %q，实际 %q", tc.name, needle, got)
			}
		}
	}
}

// addChannelForProbe 造一个启用中的渠道（供测活相关用例复用）。
func addChannelForProbe(t *testing.T, fx *channelStrategyFixture, models []string) *model.Channel {
	t.Helper()
	ch := &model.Channel{
		Name: "测活渠道", Type: 1, BaseURL: "https://upstream.example.com",
		APIKey: "sk-single", Models: models, Group: model.DefaultGroupName,
		Priority: 1, Weight: 1, Status: model.ChannelStatusEnabled,
	}
	if err := fx.channels.Create(context.Background(), ch); err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	return ch
}
