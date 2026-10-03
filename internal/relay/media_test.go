// 图像 / 音频类入站端点的单元测试。
//
// 意图（Why）：
//
//	媒体类转发的正确性无法靠"看代码"保证——JSON 是否逐字节透传、二进制音频
//	是否被当成 JSON 解析、multipart 的 boundary 是否被原样保留、上游 4xx 是否
//	被原样回传且不计费，都必须用真实的 HTTP 往返验证。因此本文件用 httptest
//	起假上游，断言"客户端收到什么、上游收到什么、计费落了多少"。
//
// 流转（Flow）：
//
//	go test ./internal/relay/
//	  ├─ 启动假上游（httptest.Server）
//	  ├─ 把渠道指向假上游（临时 SQLite + 加密仓储，复用 openai_test.go 的 helper）
//	  └─ 调用 Relay 的媒体处理器并断言
//
// 扩展（Extend）：
//
//	新增媒体端点时，按"JSON 透传 / 二进制透传 / multipart 透传 / 上游错误透传"
//	四类各补一条用例，保持覆盖结构一致。
package relay

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/reqctx"
)

// fakeUsageLogRepo 是内存版调用日志仓储，用于断言"计费落了多少"。
//
// 只实现被断言需要的行为：Create 记录条目，其余方法返回零值——
// 媒体链路的测试只关心写入的那一条日志（其中 Quota 即本次真实计入额度）。
type fakeUsageLogRepo struct {
	mu      sync.Mutex
	entries []*model.UsageLog
}

func (f *fakeUsageLogRepo) Create(_ context.Context, log *model.UsageLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, log)
	return nil
}

// last 返回最近写入的一条日志；没有则返回 nil。
func (f *fakeUsageLogRepo) last() *model.UsageLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		return nil
	}
	return f.entries[len(f.entries)-1]
}

func (f *fakeUsageLogRepo) List(context.Context, model.UsageLogQuery) ([]*model.UsageLog, error) {
	return nil, nil
}
func (f *fakeUsageLogRepo) Count(context.Context, model.UsageLogQuery) (int, error) { return 0, nil }
func (f *fakeUsageLogRepo) Summary(context.Context, model.UsageLogQuery) (*model.UsageSummary, error) {
	return nil, nil
}
func (f *fakeUsageLogRepo) DailySeries(context.Context, model.UsageLogQuery) ([]model.DailyUsage, error) {
	return nil, nil
}
func (f *fakeUsageLogRepo) TopModels(context.Context, model.UsageLogQuery, int) ([]model.ModelUsage, error) {
	return nil, nil
}
func (f *fakeUsageLogRepo) SumUsageByChannelKey(context.Context, uint64) ([]*model.ChannelKeyUsage, error) {
	return nil, nil
}
func (f *fakeUsageLogRepo) ModelFailureStats(context.Context, uint64, time.Time) ([]model.ModelFailureStat, error) {
	return nil, nil
}

// Leaderboard 是排行榜聚合的桩：转发链路不关心榜单，实现空即可。
// 但必须存在——接口新增方法后，缺实现的桩会让整个测试包编译失败。
func (f *fakeUsageLogRepo) Leaderboard(context.Context, model.UsageLogQuery) ([]model.LeaderboardEntry, error) {
	return nil, nil
}

// waitForUsageLog 轮询等待调用日志写入（写库发生在响应体回传之后，存在极短的时间差）。
func waitForUsageLog(t *testing.T, logs *fakeUsageLogRepo) *model.UsageLog {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entry := logs.last(); entry != nil {
			return entry
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待调用日志写入超时")
	return nil
}

// TestServeImageGenerations_JSON透传与按次计费 覆盖场景①。
//
// 验证：请求与响应 JSON 逐字节透传、上游路径/鉴权正确、命中按次价后计费 500。
func TestServeImageGenerations_JSON透传与按次计费(t *testing.T) {
	const upstreamKey = "sk-image-upstream"

	var (
		gotPath string
		gotAuth string
		gotBody []byte
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Custom", "preserved")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://img/1.png"}]}`)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	addChannel(t, repo, upstream.URL, upstreamKey, nil, 10)

	// 按次价 500（token 单价全 0 → 自动判定为 per_call）。
	logs := &fakeUsageLogRepo{}
	rl := New(repo, Options{UsageLogs: logs, Billing: newTestBilling(100, 0, 0, 500)})

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeImageGenerations))
	defer gateway.Close()

	reqBody := `{"model":"test-model","prompt":"a cat","n":1,"size":"1024x1024"}`
	resp, err := http.Post(gateway.URL+"/v1/images/generations", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q，期望包含 application/json", ct)
	}
	if resp.Header.Get("X-Upstream-Custom") != "preserved" {
		t.Error("上游自定义响应头未被透传")
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "https://img/1.png") {
		t.Errorf("响应体未透传上游内容: %s", respBody)
	}

	// 上游收到的是渠道密钥与正确的路径、请求体。
	if gotAuth != "Bearer "+upstreamKey {
		t.Errorf("上游 Authorization = %q，期望 %q", gotAuth, "Bearer "+upstreamKey)
	}
	if gotPath != "/v1/images/generations" {
		t.Errorf("上游路径 = %q，期望 /v1/images/generations", gotPath)
	}
	if string(gotBody) != reqBody {
		t.Errorf("上游请求体被改写：\n got=%s\nwant=%s", gotBody, reqBody)
	}

	// 计费：按次价 500，且模型名与状态码正确。
	entry := waitForUsageLog(t, logs)
	if entry.Model != "test-model" {
		t.Errorf("日志模型名 = %q，期望 test-model", entry.Model)
	}
	if entry.StatusCode != http.StatusOK {
		t.Errorf("日志状态码 = %d，期望 200", entry.StatusCode)
	}
	if entry.Quota != 500 {
		t.Errorf("按次模型计费 = %d，期望 500", entry.Quota)
	}
}

// TestServeAudioSpeech_二进制透传 覆盖场景②。
//
// 验证：TTS 的二进制音频流被逐字节回传，且 Content-Type 原样保留（不解析为 JSON）。
func TestServeAudioSpeech_二进制透传(t *testing.T) {
	const upstreamKey = "sk-tts-upstream"

	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 0x01, 0x02, 0x03, 0xFE, 0x7F}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("上游路径 = %q，期望 /v1/audio/speech", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	addChannel(t, repo, upstream.URL, upstreamKey, nil, 10)
	rl := New(repo, Options{})

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeAudioSpeech))
	defer gateway.Close()

	reqBody := `{"model":"tts-1","input":"你好","voice":"alloy"}`
	resp, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q，期望 audio/mpeg", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, audio) {
		t.Errorf("音频字节未被原样透传：got=%v want=%v", got, audio)
	}
}

// TestServeAudioTranscriptions_Multipart透传 覆盖场景③。
//
// 验证：multipart 的原始 Content-Type（含 boundary）与字节流被逐字节透传给上游，
// 网关不对表单做"解析成字段再拼装"。
func TestServeAudioTranscriptions_Multipart透传(t *testing.T) {
	const upstreamKey = "sk-asr-upstream"

	// 构造一份 multipart 请求体。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", "whisper-1"); err != nil {
		t.Fatalf("写 model 字段失败: %v", err)
	}
	fileContent := []byte("FAKE-MP3-BYTES-\x00\x01\x02")
	fw, err := mw.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatalf("创建文件字段失败: %v", err)
	}
	if _, err := fw.Write(fileContent); err != nil {
		t.Fatalf("写文件内容失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	raw := buf.Bytes()
	boundary := mw.Boundary()
	contentType := "multipart/form-data; boundary=" + boundary

	var (
		gotContentType string
		gotBody        []byte
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("上游路径 = %q，期望 /v1/audio/transcriptions", r.URL.Path)
		}
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"text":"hello world"}`)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	addChannel(t, repo, upstream.URL, upstreamKey, nil, 10)
	rl := New(repo, Options{})

	gateway := httptest.NewServer(http.HandlerFunc(rl.ServeAudioTranscriptions))
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/audio/transcriptions", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "hello world") {
		t.Errorf("上游 JSON 响应未被透传: %s", respBody)
	}

	// 关键断言：上游收到的 Content-Type 含同一 boundary，且字节流逐字节一致。
	if gotContentType != contentType {
		t.Errorf("上游 Content-Type = %q，期望 %q", gotContentType, contentType)
	}
	if !bytes.Equal(gotBody, raw) {
		t.Errorf("multipart 请求体未被原样透传（boundary 或内容被改动）")
	}
}

// TestServeImageGenerations_上游4xx原样透传且不计费 覆盖场景④。
//
// 验证：上游 4xx 的错误状态码、响应头与 JSON 错误体原样回传；
// 若鉴权阶段做过额度预留，则该预留被释放（本次不计费）。
func TestServeImageGenerations_上游4xx原样透传且不计费(t *testing.T) {
	const upstreamKey = "sk-image-upstream"

	upstreamErr := `{"error":{"message":"prompt rejected","type":"invalid_request_error"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Reason", "bad-prompt")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, upstreamErr)
	}))
	defer upstream.Close()

	repo := newTestRepo(t)
	addChannel(t, repo, upstream.URL, upstreamKey, nil, 10)

	logs := &fakeUsageLogRepo{}
	quota := newFakeQuotaRepo()
	billing := newTestBilling(100, 0, 0, 500).WithQuotaRepository(quota)
	rl := New(repo, Options{UsageLogs: logs, Billing: billing})

	// 模拟鉴权阶段已为该请求预留额度（配额受限账号的真实路径）。
	const requestID = "media-4xx-req"
	if _, err := billing.Reserve(context.Background(), model.ReserveRequest{
		RequestID: requestID, UserID: 1, TokenID: 2, Amount: 500,
	}); err != nil {
		t.Fatalf("预留额度失败: %v", err)
	}

	// 注入身份（含 RequestID）与分组，等价于 TokenAuth 中间件放行后的上下文。
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := reqctx.WithIdentity(r.Context(), reqctx.Identity{
			UserID: 1, TokenID: 2, RequestID: requestID,
		})
		ctx = reqctx.WithGroup(ctx, "default")
		rl.ServeImageGenerations(w, r.WithContext(ctx))
	}))
	defer gateway.Close()

	resp, err := http.Post(gateway.URL+"/v1/images/generations",
		"application/json", strings.NewReader(`{"model":"test-model","prompt":"x"}`))
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 状态码、响应头与错误体原样透传。
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（原样透传上游错误）", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream-Reason") != "bad-prompt" {
		t.Error("上游错误响应头未被透传")
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != upstreamErr {
		t.Errorf("上游错误体被改写：\n got=%s\nwant=%s", gotBody, upstreamErr)
	}

	// 不计费：预留被释放，日志额度为 0。
	entry := waitForUsageLog(t, logs)
	if entry.StatusCode != http.StatusBadRequest {
		t.Errorf("日志状态码 = %d，期望 400", entry.StatusCode)
	}
	if entry.Quota != 0 {
		t.Errorf("上游 4xx 不应计费，实际额度 = %d", entry.Quota)
	}
	if item := quota.byRequestID[requestID]; item == nil || item.Status != model.ReservationReleased {
		t.Errorf("上游 4xx 应释放预留，实际 = %+v", item)
	}
}
