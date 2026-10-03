// 本文件覆盖「模板驱动的自定义异步任务上游适配器」（async_custom.go）。
//
// 意图（Why）：
//
//	该适配器的正确性完全取决于"配置驱动的字段映射"是否按预期工作——
//	点路径取不到会不会报错、未知状态会不会被误判失败、鉴权头是否按渠道注入、
//	错误文案会不会泄漏地址与密钥。这些都无法靠静态阅读确认，必须用假的
//	上游服务（httptest）跑通"提交 → 轮询"的真实链路来验证。
//
// 流转（Flow）：
//
//	httptest 上游桩 → customAsyncProvider.Submit/Poll → 断言语义
//
// 扩展（Extend）：
//
//	新增配置键时，在此追加一条用例说明它的默认与命中行为。
package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// customTestChannel 构造一个最小可用的渠道（扩展配置按需传入）。
func customTestChannel(baseURL, apiKey string, extra map[string]string) *model.Channel {
	return &model.Channel{
		Name:        "自定义异步上游",
		Type:        1,
		BaseURL:     baseURL,
		APIKey:      apiKey,
		ExtraConfig: extra,
	}
}

// TestCustomAsyncProvider_提交轮询成功_含结果URL提取 覆盖完整链路与数组下标点路径。
func TestCustomAsyncProvider_提交轮询成功_含结果URL提取(t *testing.T) {
	var (
		submitAuth string
		pollPath   string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/submit":
			submitAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":{"task_id":"abc123"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
			pollPath = r.URL.Path
			_, _ = w.Write([]byte(`{"data":{"status":"completed","progress":100,"output":{"images":[{"url":"https://cdn.example/img-1.png"}]}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	p := &customAsyncProvider{client: upstream.Client()}
	ch := customTestChannel(upstream.URL, "test-key", map[string]string{
		cfgCustomSubmitPath:    "/v1/submit",
		cfgCustomPollPath:      "/v1/tasks/{id}",
		cfgCustomIDPath:        "data.task_id",
		cfgCustomStatusPath:    "data.status",
		cfgCustomProgressPath:  "data.progress",
		cfgCustomResultURLPath: "data.output.images.0.url",
	})

	submitted, err := p.Submit(context.Background(), &TaskRequest{
		Kind:   model.TaskKindVideo,
		Model:  "some-video-model",
		Prompt: "a cat surfing",
	}, ch, "test-key")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if submitted.UpstreamID != "abc123" {
		t.Fatalf("任务号 = %q，期望 abc123", submitted.UpstreamID)
	}
	if submitted.Status != model.TaskStatusQueued {
		t.Fatalf("提交后状态 = %v，期望排队中", submitted.Status)
	}
	if submitAuth != "Bearer test-key" {
		t.Errorf("提交请求鉴权头 = %q，期望 Bearer test-key", submitAuth)
	}

	task := &model.Task{UpstreamID: "abc123", Model: "some-video-model"}
	polled, err := p.Poll(context.Background(), task, ch, "test-key")
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if polled.Status != model.TaskStatusSucceeded {
		t.Fatalf("状态 = %v，期望已完成", polled.Status)
	}
	if polled.ResultURL != "https://cdn.example/img-1.png" {
		t.Errorf("结果地址 = %q，期望从 output.images.0.url 提取", polled.ResultURL)
	}
	if polled.Progress != 100 {
		t.Errorf("进度 = %d，期望 100", polled.Progress)
	}
	if pollPath != "/v1/tasks/abc123" {
		t.Errorf("轮询路径 = %q，期望 /v1/tasks/abc123（{id} 已被替换）", pollPath)
	}
}

// TestCustomAsyncProvider_未知状态保持进行中 验证新状态词不会被误判为失败。
func TestCustomAsyncProvider_未知状态保持进行中(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"status":"hibernating_weird_state"}}`))
	}))
	defer upstream.Close()

	p := &customAsyncProvider{client: upstream.Client()}
	ch := customTestChannel(upstream.URL, "k", map[string]string{
		cfgCustomPollPath:   "/tasks/{id}",
		cfgCustomStatusPath: "data.status",
	})

	polled, err := p.Poll(context.Background(), &model.Task{UpstreamID: "1"}, ch, "k")
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	// 关键：未知状态必须保持"进行中"，绝不能是"已失败"（否则会白送额度）。
	if polled.Status != model.TaskStatusRunning {
		t.Fatalf("未知状态映射为 %v，期望进行中（保守方向）", polled.Status)
	}
}

// TestCustomAsyncProvider_状态映射配置生效 验证 async_status_map 的命中与优先级。
func TestCustomAsyncProvider_状态映射配置生效(t *testing.T) {
	cases := []struct {
		name      string
		response  string
		statusMap string
		want      model.TaskStatus
	}{
		{
			name:      "自定义词命中映射",
			response:  `{"status":"FANCY_OK"}`,
			statusMap: "fancy_ok=success,running=running",
			want:      model.TaskStatusSucceeded,
		},
		{
			name:      "映射优先于内置词表",
			response:  `{"status":"running"}`,
			statusMap: "running=success",
			want:      model.TaskStatusSucceeded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer upstream.Close()

			p := &customAsyncProvider{client: upstream.Client()}
			ch := customTestChannel(upstream.URL, "k", map[string]string{
				cfgCustomPollPath:   "/tasks/{id}",
				cfgCustomStatusPath: "status",
				cfgCustomStatusMap:  tc.statusMap,
			})

			polled, err := p.Poll(context.Background(), &model.Task{UpstreamID: "1"}, ch, "k")
			if err != nil {
				t.Fatalf("轮询失败: %v", err)
			}
			if polled.Status != tc.want {
				t.Fatalf("状态 = %v，期望 %v", polled.Status, tc.want)
			}
		})
	}
}

// TestCustomAsyncProvider_提交失败与上游5xx错误路径 覆盖错误分支与脱敏。
func TestCustomAsyncProvider_提交失败与上游5xx错误路径(t *testing.T) {
	const secretKey = "sk-secret-key-should-not-leak"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		// 故意回显地址与密钥：断言适配器返回前已把它们抹掉。
		_, _ = fmt.Fprintf(w, `{"error":{"message":"boom at http://%s with key %s"}}`, r.Host, secretKey)
	}))
	defer upstream.Close()

	p := &customAsyncProvider{client: upstream.Client()}
	ch := customTestChannel(upstream.URL, secretKey, map[string]string{
		cfgCustomSubmitPath: "/v1/submit",
		cfgCustomPollPath:   "/tasks/{id}",
	})

	_, err := p.Submit(context.Background(), &TaskRequest{
		Kind: model.TaskKindImage, Model: "m", Prompt: "p",
	}, ch, secretKey)
	if err == nil {
		t.Fatal("上游 500 时提交应返回错误")
	}
	if strings.Contains(err.Error(), upstream.URL) {
		t.Errorf("提交错误泄漏了上游地址: %q", err.Error())
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Errorf("提交错误泄漏了密钥: %q", err.Error())
	}

	// 轮询遇到 5xx 同样应返回错误（由编排层当作"暂时查不到"处理）。
	if _, err := p.Poll(context.Background(), &model.Task{UpstreamID: "1"}, ch, secretKey); err == nil {
		t.Fatal("上游 500 时轮询应返回错误")
	}

	// 未配置提交路径：应给出明确错误而非发出错误请求。
	noPath := customTestChannel(upstream.URL, secretKey, nil)
	if _, err := p.Submit(context.Background(), &TaskRequest{
		Kind: model.TaskKindImage, Model: "m", Prompt: "p",
	}, noPath, secretKey); err == nil {
		t.Fatal("缺少 async_submit_path 时应返回错误")
	}
}

// TestCustomAsyncProvider_失败结果脱敏 验证失败任务的 Error 字段不含地址与密钥。
func TestCustomAsyncProvider_失败结果脱敏(t *testing.T) {
	const secretKey = "sk-secret-key-should-not-leak"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"status":"failed","reason":"failed calling http://%s with key %s"}`, r.Host, secretKey)
	}))
	defer upstream.Close()

	p := &customAsyncProvider{client: upstream.Client()}
	ch := customTestChannel(upstream.URL, secretKey, map[string]string{
		cfgCustomPollPath:   "/tasks/{id}",
		cfgCustomStatusPath: "status",
		cfgCustomErrorPath:  "reason",
	})

	polled, err := p.Poll(context.Background(), &model.Task{UpstreamID: "1"}, ch, secretKey)
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if polled.Status != model.TaskStatusFailed {
		t.Fatalf("状态 = %v，期望已失败", polled.Status)
	}
	if strings.Contains(polled.Error, upstream.URL) {
		t.Errorf("失败原因泄漏了上游地址: %q", polled.Error)
	}
	if strings.Contains(polled.Error, secretKey) {
		t.Errorf("失败原因泄漏了密钥: %q", polled.Error)
	}
}

// TestLookupDotPath 覆盖点路径提取器的对象键、数组下标与取不到的情形。
func TestLookupDotPath(t *testing.T) {
	root, ok := decodeJSONAny([]byte(`{"data":{"items":[{"url":"u0"},{"url":"u1"}],"id":9007199254740993},"flag":true}`))
	if !ok {
		t.Fatal("测试数据应为合法 JSON")
	}

	cases := []struct {
		path    string
		want    string
		wantGot bool
	}{
		{"data.items.0.url", "u0", true},
		{"data.items.1.url", "u1", true},
		{"flag", "true", true},
		{"data.id", "9007199254740993", true}, // 大整数不被科学计数法破坏
		{"data.missing", "", false},
		{"data.items.9.url", "", false}, // 下标越界
		{"data.items.url", "", false},   // 对数组用了非数字键
		{"", "", false},                 // 空路径
	}

	for _, tc := range cases {
		got, found := lookupDotPath(root, tc.path)
		if found != tc.wantGot || got != tc.want {
			t.Errorf("lookupDotPath(%q) = (%q, %v)，期望 (%q, %v)",
				tc.path, got, found, tc.want, tc.wantGot)
		}
	}
}
