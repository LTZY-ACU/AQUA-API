// 本文件验证「auto 路由」行为：model 填 auto 时按延迟选可对话模型。
//
// 意图（Why）：
//
//	对调用方而言，"记住哪个模型最快"是件麻烦事——测速数据一直在变，
//	今天最快的不代表明天还最快。auto 路由让客户端只写 auto，
//	网关用测速数据当场选出「当前分组里延迟最低且确实可对话」的模型，
//	把"选型"这件动态的事收敛到网关一侧。
//
// 流转（Flow）：
//
//	POST /v1/chat/completions {"model":"auto"} → ServeChatCompletions
//	  → resolveAutoChatModel（测速数据 + 启用渠道声明双重过滤，取最小 TTFB）
//	  → 改写请求体 model 字段 → 常规选渠道/转发链路（与原请求完全同路）
//
// 扩展（Extend）：
//
//	日后若按"分组"分别定价/限定模型，auto 的分组隔离断言在本文件补。
package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// stubSpeeds 是 model.ModelSpeedRepository 的内存实现，仅供测试注入。
type stubSpeeds struct {
	results []*model.ModelSpeedResult
}

func (s *stubSpeeds) Upsert(_ context.Context, _ *model.ModelSpeedResult) error { return nil }
func (s *stubSpeeds) Latest(_ context.Context) ([]*model.ModelSpeedResult, error) {
	return s.results, nil
}
func (s *stubSpeeds) DeleteByChannel(_ context.Context, _ uint64) error { return nil }

// modelEchoUpstream 记录收到的 model 字段并回 200。
func modelEchoUpstream(t *testing.T, got *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		var parsed map[string]any
		_ = json.Unmarshal(buf[:n], &parsed)
		m, _ := parsed["model"].(string)
		mu.Lock()
		*got = append(*got, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"auto-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
}

// TestAutoRoute_选延迟最低的可对话模型 核心行为：
// 即使慢模型挂在更高优先级渠道上，auto 也必须选延迟最低的模型。
func TestAutoRoute_选延迟最低的可对话模型(t *testing.T) {
	var (
		mu        sync.Mutex
		gotModels []string
	)
	upstream := modelEchoUpstream(t, &gotModels, &mu)
	defer upstream.Close()

	repo := newTestRepo(t)
	chFast := addChannel(t, repo, upstream.URL, "sk-fast", []string{"m-fast"}, 10)
	chSlow := addChannel(t, repo, upstream.URL, "sk-slow", []string{"m-slow"}, 100) // 优先级更高但更慢

	rl := New(repo, Options{ModelSpeeds: &stubSpeeds{results: []*model.ModelSpeedResult{
		{ChannelID: chFast.ID, Model: "m-fast", OK: true, StatusCode: 200, TTFBMS: 50},
		{ChannelID: chSlow.ID, Model: "m-slow", OK: true, StatusCode: 200, TTFBMS: 400},
	}}})

	gateway := newGateway(t, rl)
	resp := postChat(t, gateway.URL, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auto 请求应 200，实际 %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotModels) == 0 || gotModels[len(gotModels)-1] != "m-fast" {
		t.Errorf("上游应收到 m-fast（最低延迟），实际收到 %v", gotModels)
	}
}

// TestAutoRoute_无测速数据时报明确错误 验证 speeds 缺失时的失败语义：
// 返回 503 且提示先测速，而不是把 auto 当普通模型名撞 404。
func TestAutoRoute_无测速数据时报明确错误(t *testing.T) {
	repo := newTestRepo(t)
	addChannel(t, repo, "http://127.0.0.1:1", "sk-x", []string{"m-fast"}, 10)

	rl := New(repo, Options{}) // speeds 为 nil
	gateway := newGateway(t, rl)
	resp := postChat(t, gateway.URL, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("应 503，实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "测速") {
		t.Errorf("错误信息应提示去测速，实际 %s", string(body))
	}
}

// TestAutoRoute_停用渠道与未声明模型不参与 验证双重过滤：
// 测速结果只有落在「启用 + 仍声明该模型」的渠道上才算数。
func TestAutoRoute_停用渠道与未声明模型不参与(t *testing.T) {
	var (
		mu        sync.Mutex
		gotModels []string
	)
	upstream := modelEchoUpstream(t, &gotModels, &mu)
	defer upstream.Close()

	repo := newTestRepo(t)
	// 停用渠道：测速最快但渠道已停 → 不得参与
	disabled := &model.Channel{
		Name: "停用渠道", Type: 1, BaseURL: upstream.URL, APIKey: "sk-x",
		Models: []string{"m-disabled"}, Group: "default",
		Status: model.ChannelStatusDisabled, Priority: 10, Weight: 1,
	}
	if err := repo.Create(context.Background(), disabled); err != nil {
		t.Fatalf("创建停用渠道失败: %v", err)
	}
	// 启用渠道：m-removed 已被移出清单 → 不得参与；m-real 正常参与
	live := addChannel(t, repo, upstream.URL, "sk-x", []string{"m-removed", "m-real"}, 10)
	live.Models = []string{"m-real"}
	if err := repo.Update(context.Background(), live); err != nil {
		t.Fatalf("更新渠道清单失败: %v", err)
	}

	rl := New(repo, Options{ModelSpeeds: &stubSpeeds{results: []*model.ModelSpeedResult{
		{ChannelID: disabled.ID, Model: "m-disabled", OK: true, StatusCode: 200, TTFBMS: 10},
		{ChannelID: live.ID, Model: "m-removed", OK: true, StatusCode: 200, TTFBMS: 20},
		{ChannelID: live.ID, Model: "m-real", OK: true, StatusCode: 200, TTFBMS: 99},
	}}})
	gateway := newGateway(t, rl)
	resp := postChat(t, gateway.URL, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应 200（m-real 可用），实际 %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotModels) == 0 || gotModels[len(gotModels)-1] != "m-real" {
		t.Errorf("应选中 m-real，实际收到 %v", gotModels)
	}
}
