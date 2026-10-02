// key_routing_test.go 校验「凭据级分组 / 模型分叉」在转发链路上的真实效果（迁移 0038）。
//
// 意图（Why）：
//
//	分叉的价值在于"请求打出去时用的到底是不是那把对的钥匙"。策略解析与判定函数
//	各自单测通过，并不保证转发层真的按它过滤——因此这里一律用 httptest 上游
//	记录【实际收到的 Authorization】，而不是断言内部变量。
//
// 流转（Flow）：
//
//	UpdateRouting（写库）→ ListUsable（读池）
//	  → filterUsableKeysForRequest（按请求的分组/模型过滤）→ 上游收到的密钥
//
// 扩展（Extend）：
//
//	新增分叉维度时，仿照本文件：建渠道 + 配两把凭据 + 数"上游收到了哪一把"。
package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// keyEchoUpstream 启动一个上游：记录每次请求携带的 Authorization，并返回 200。
//
// 用互斥锁保护切片：一次请求可能触发多次尝试（重试），读写会并发。
type keyEchoUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	auths  []string
}

// newKeyEchoUpstream 启动记录型上游。
func newKeyEchoUpstream(t *testing.T) *keyEchoUpstream {
	t.Helper()
	echo := &keyEchoUpstream{}
	echo.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echo.mu.Lock()
		echo.auths = append(echo.auths, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		echo.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(echo.server.Close)
	return echo
}

// lastAuth 返回最近一次收到的密钥（没有请求时为空串）。
func (e *keyEchoUpstream) lastAuth() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.auths) == 0 {
		return ""
	}
	return e.auths[len(e.auths)-1]
}

// TestKeyRouting_按模型选到匹配的凭据 验证"一把凭据只服务某些模型"时会选到对的那把。
func TestKeyRouting_按模型选到匹配的凭据(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	echo := newKeyEchoUpstream(t)
	ch := addChannel(t, channels, echo.server.URL, "", []string{"model-a", "model-b"}, 10)
	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"sk-for-a", "sk-for-b"}, nil); err != nil {
		t.Fatalf("导入密钥池失败: %v", err)
	}

	// 两把凭据各自限定一个模型
	pool, err := keys.ListByChannel(ctx, ch.ID)
	if err != nil {
		t.Fatalf("读取密钥池失败: %v", err)
	}
	if len(pool) != 2 {
		t.Fatalf("池内应有 2 把凭据，实际 %d", len(pool))
	}
	// 按明文识别两把凭据，再各自限定一个模型。
	// 不能用 pool[0]/pool[1] 这类下标：池的 id 顺序属于存储细节，
	// 测试要钉住的是"凭据声明与请求模型是否匹配"，与落库顺序无关。
	byKey := make(map[string]uint64, len(pool))
	for _, k := range pool {
		byKey[k.Key] = k.ID
	}
	idA, idB := byKey["sk-for-a"], byKey["sk-for-b"]
	if idA == 0 || idB == 0 {
		t.Fatalf("池内应包含 sk-for-a 与 sk-for-b，实际 %#v", byKey)
	}
	if err := keys.UpdateRouting(ctx, idA, nil, []string{"model-a"}); err != nil {
		t.Fatalf("配置第一把凭据失败: %v", err)
	}
	if err := keys.UpdateRouting(ctx, idB, nil, []string{"model-b"}); err != nil {
		t.Fatalf("配置第二把凭据失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{Keys: keys}))

	// 请求 model-a：无论池内怎么轮换，都必须用"声明支持 model-a"的那把
	for i := 0; i < 6; i++ {
		resp := postChat(t, gateway.URL, `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 次请求应为 200，实际 %d", i+1, resp.StatusCode)
		}
		if got := echo.lastAuth(); got != "sk-for-a" {
			t.Fatalf("请求 model-a 时应用 sk-for-a，实际用了 %q", got)
		}
	}

	// 请求 model-b：同理应切到另一把
	resp := postChat(t, gateway.URL, `{"model":"model-b","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()
	if got := echo.lastAuth(); got != "sk-for-b" {
		t.Fatalf("请求 model-b 时应用 sk-for-b，实际用了 %q", got)
	}
}

// TestKeyRouting_无凭据服务该模型时换渠道 验证"池里没有一把能服务该模型"时的收敛行为。
//
// 期望：该渠道被放弃（而不是硬用一把不匹配的凭据），请求落到能服务的渠道上。
func TestKeyRouting_无凭据服务该模型时换渠道(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	// 高优先级渠道：池内只有"服务 model-a"的凭据
	limited := newKeyEchoUpstream(t)
	limitedChannel := addChannel(t, channels, limited.server.URL, "", []string{"model-a", "model-b"}, 100)
	if _, _, err := keys.ReplaceAll(ctx, limitedChannel.ID, []string{"sk-only-a"}, nil); err != nil {
		t.Fatalf("导入密钥池失败: %v", err)
	}
	pool, _ := keys.ListByChannel(ctx, limitedChannel.ID)
	if err := keys.UpdateRouting(ctx, pool[0].ID, nil, []string{"model-a"}); err != nil {
		t.Fatalf("配置凭据失败: %v", err)
	}

	// 低优先级渠道：单密钥模式，不限模型
	fallback := newKeyEchoUpstream(t)
	addChannel(t, channels, fallback.server.URL, "sk-fallback", []string{"model-a", "model-b"}, 10)

	gateway := newGateway(t, New(channels, Options{Keys: keys}))
	resp := postChat(t, gateway.URL, `{"model":"model-b","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应回退到能服务的渠道并返回 200，实际 %d", resp.StatusCode)
	}
	if got := fallback.lastAuth(); got != "sk-fallback" {
		t.Fatalf("应由备用渠道的密钥处理，实际用了 %q", got)
	}
	if got := limited.lastAuth(); got != "" {
		t.Fatalf("限定 model-a 的渠道不该被请求，实际用了 %q", got)
	}
}

// TestKeyRouting_按分组选到匹配的凭据 验证分组维度的分叉。
//
// 场景：渠道同时服务 free 与 vip。池里一把凭据只供 vip，另一把不限分组。
// 期望：free 分组的请求必须落到"不限分组"的那把，绝不落到 vip 那把。
func TestKeyRouting_按分组选到匹配的凭据(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	echo := newKeyEchoUpstream(t)
	ch := addChannelInGroup(t, channels, echo.server.URL, "free", []string{"model-a"}, 10)
	// 让同一渠道也服务 vip：路由按分组命中渠道，凭据再按分组细分
	ch.Groups = []string{"free", "vip"}
	ch.Group = "free"
	if err := channels.Update(ctx, ch); err != nil {
		t.Fatalf("更新渠道分组失败: %v", err)
	}

	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"sk-vip-only", "sk-shared"}, nil); err != nil {
		t.Fatalf("导入密钥池失败: %v", err)
	}
	pool, _ := keys.ListByChannel(ctx, ch.ID)
	// 同样按明文定位，避免依赖池内顺序
	var vipKeyID uint64
	for _, k := range pool {
		if k.Key == "sk-vip-only" {
			vipKeyID = k.ID
		}
	}
	if vipKeyID == 0 {
		t.Fatalf("池内未找到 sk-vip-only，实际 %d 把", len(pool))
	}
	if err := keys.UpdateRouting(ctx, vipKeyID, []string{"vip"}, nil); err != nil {
		t.Fatalf("配置凭据分组失败: %v", err)
	}

	relayInstance := New(channels, Options{Keys: keys})

	// free 分组：只能落到"不限分组"的那把
	for i := 0; i < 6; i++ {
		rec := serveWithGroup(relayInstance, "free", `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次 free 分组请求应为 200，实际 %d：%s", i+1, rec.Code, rec.Body.String())
		}
		if got := echo.lastAuth(); got != "sk-shared" {
			t.Fatalf("free 分组不该用只服务 vip 的凭据，实际用了 %q", got)
		}
	}

	// vip 分组：两把都可能被选中，但绝不能选到"只服务别的分组"的凭据——
	// 这里只断言请求成功（池内确实有 vip 可用凭据）
	rec := serveWithGroup(relayInstance, "vip", `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("vip 分组请求应为 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := echo.lastAuth(); got != "sk-vip-only" && got != "sk-shared" {
		t.Fatalf("vip 分组应使用池内凭据，实际用了 %q", got)
	}
}

// TestKeyRouting_未配置分叉时行为不变 保护升级前的既有调度行为。
//
// 这是本次改动最重要的回归点：两列默认空串（= 不限），
// 因此未做任何配置的渠道必须与迁移前完全一致地"池内随机用哪把都行"。
func TestKeyRouting_未配置分叉时行为不变(t *testing.T) {
	channels, keys := newTestRepos(t)
	ctx := context.Background()

	echo := newKeyEchoUpstream(t)
	ch := addChannel(t, channels, echo.server.URL, "", []string{"model-a"}, 10)
	if _, _, err := keys.ReplaceAll(ctx, ch.ID, []string{"sk-1", "sk-2"}, nil); err != nil {
		t.Fatalf("导入密钥池失败: %v", err)
	}

	gateway := newGateway(t, New(channels, Options{Keys: keys}))
	for i := 0; i < 5; i++ {
		resp := postChat(t, gateway.URL, `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 次请求应为 200，实际 %d", i+1, resp.StatusCode)
		}
	}

	// 两把都应至少被用过一次（不强制，但必须都在可选集合内：
	// 这里只断言"没有因为空配置而被整体排除"）
	echo.mu.Lock()
	seen := map[string]bool{}
	for _, auth := range echo.auths {
		seen[auth] = true
	}
	echo.mu.Unlock()
	if !seen["sk-1"] && !seen["sk-2"] {
		t.Fatal("未配置分叉时池内凭据都应可用，实际一次都没用上")
	}
}
