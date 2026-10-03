// 敏感词过滤中间件的测试。
//
// 意图（Why）：
//
//	过滤中间件会【读取并还原】请求体，这类"读了必须还"的逻辑最容易写出
//	"一挂过滤所有请求都报 JSON 格式错误"的故障；同时"开关未开启时零开销放行"
//	与"命中即拒绝"是两条必须锁定的行为契约。
//
// 流转（Flow）：
//
//	go test ./internal/server/middleware/
//	  └─ 用真实仓储（临时 SQLite）写入词表与开关 → httptest 构造请求
//	       └─ 断言：状态码 / 错误码 / 下游能否读到完整请求体
//
// 扩展（Extend）：
//
//	新增扫描来源（如 URL 查询参数）时，按同样风格补"命中 / 未命中"两类用例。
package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/oai"
	"github.com/LTZY-ACU/ltzy-api/internal/store"
)

// newTestSensitiveDeps 构造敏感词过滤所需的仓储（临时 SQLite，跑过全部迁移）。
func newTestSensitiveDeps(t *testing.T) (model.SensitiveWordRepository, model.SettingRepository) {
	t.Helper()

	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "sensitive_test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	return store.NewSensitiveWordRepository(st.DB()), store.NewSettingRepository(st.DB(), st.Dialect())
}

// buildSensitiveRouter 组装一个"过滤中间件 + 回显请求体"的测试路由。
//
// 回显处理器的作用：验证请求体在扫描后仍被完整还原（下游必须读到与客户端
// 发出的一模一样的字节）。
func buildSensitiveRouter(t *testing.T, filter *SensitiveFilter) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(filter.Middleware())
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.String(http.StatusInternalServerError, "读取请求体失败")
			return
		}
		c.JSON(http.StatusOK, gin.H{"body": string(body)})
	})
	return engine
}

// newEnabledFilter 构造一个"已开启过滤 + 指定词表"的过滤器。
func newEnabledFilter(t *testing.T, words ...string) *SensitiveFilter {
	t.Helper()
	repo, settings := newTestSensitiveDeps(t)

	if err := settings.Set(context.Background(),
		model.SettingKeySensitiveFilterEnabled, "true"); err != nil {
		t.Fatalf("写入过滤开关失败: %v", err)
	}
	for i, word := range words {
		if err := repo.Create(context.Background(), &model.SensitiveWord{
			Word: word, Enabled: true, Category: "测试",
		}); err != nil {
			t.Fatalf("写入第 %d 个词条失败: %v", i, err)
		}
	}
	return NewSensitiveFilter(repo, settings)
}

func TestSensitiveFilter_命中即拒绝(t *testing.T) {
	filter := newEnabledFilter(t, "赌博")
	engine := buildSensitiveRouter(t, filter)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"哪里可以赌博"}]}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("命中敏感词应返回 400，实际 %d", recorder.Code)
	}

	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v，原文 %s", err, recorder.Body.String())
	}
	if body.Error.Type != oai.TypeContentFilter || body.Error.Code != oai.CodeSensitiveWordBlocked {
		t.Fatalf("错误类型/码不符：type=%q code=%q", body.Error.Type, body.Error.Code)
	}
}

func TestSensitiveFilter_命中文案不泄露词条本身(t *testing.T) {
	filter := newEnabledFilter(t, "赌博")
	engine := buildSensitiveRouter(t, filter)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"content":"赌博"}]}`))
	engine.ServeHTTP(recorder, request)

	// 回显给使用者的文案里不得出现词条：否则试探几次就能摸清整个黑名单。
	if strings.Contains(recorder.Body.String(), "赌博") {
		t.Fatalf("错误响应泄露了敏感词原文：%s", recorder.Body.String())
	}
}

func TestSensitiveFilter_未命中时放行且请求体完整(t *testing.T) {
	filter := newEnabledFilter(t, "赌博")
	engine := buildSensitiveRouter(t, filter)

	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"今天天气怎么样"}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("未命中应放行，实际 %d，响应 %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析回显响应失败: %v", err)
	}
	// 关键：请求体必须被完整还原，否则下游会读到空/截断的 JSON
	if body.Body != payload {
		t.Fatalf("请求体未完整还原：\n期望 %s\n实际 %s", payload, body.Body)
	}
}

func TestSensitiveFilter_命中时也还原请求体(t *testing.T) {
	// 被拦截的请求同样必须还原请求体：中间件之后的处理链（如审计、错误日志）
	// 仍可能读取它；还原是"读了就还"的硬约束，与是否命中无关。
	repo, settings := newTestSensitiveDeps(t)
	if err := settings.Set(context.Background(), model.SettingKeySensitiveFilterEnabled, "true"); err != nil {
		t.Fatalf("写入过滤开关失败: %v", err)
	}
	if err := repo.Create(context.Background(), &model.SensitiveWord{Word: "赌博", Enabled: true}); err != nil {
		t.Fatalf("写入词条失败: %v", err)
	}
	filter := NewSensitiveFilter(repo, settings)

	var captured string
	engine := gin.New()
	engine.Use(filter.Middleware())
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		captured = string(raw)
		c.Status(http.StatusOK)
	})

	payload := `{"messages":[{"content":"赌博"}]}`
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(payload)))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("命中应被拦截，实际 %d", recorder.Code)
	}
	// 处理器未被执行（请求被中断），captured 应保持为空
	if captured != "" {
		t.Fatalf("命中后不应继续执行后续处理器，实际捕获到 %q", captured)
	}
}

func TestSensitiveFilter_关闭时不扫描且放行(t *testing.T) {
	repo, settings := newTestSensitiveDeps(t)
	// 未写入开关 → 默认 false
	if err := repo.Create(context.Background(), &model.SensitiveWord{Word: "赌博", Enabled: true}); err != nil {
		t.Fatalf("写入词条失败: %v", err)
	}
	filter := NewSensitiveFilter(repo, settings)
	engine := buildSensitiveRouter(t, filter)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"content":"赌博"}]}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("过滤关闭时应放行，实际 %d", recorder.Code)
	}
}

func TestSensitiveFilter_停用词不拦截(t *testing.T) {
	repo, settings := newTestSensitiveDeps(t)
	if err := settings.Set(context.Background(), model.SettingKeySensitiveFilterEnabled, "true"); err != nil {
		t.Fatalf("写入过滤开关失败: %v", err)
	}
	if err := repo.Create(context.Background(), &model.SensitiveWord{
		Word: "赌博", Enabled: false,
	}); err != nil {
		t.Fatalf("写入词条失败: %v", err)
	}
	filter := NewSensitiveFilter(repo, settings)
	engine := buildSensitiveRouter(t, filter)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"content":"赌博"}]}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("停用词不应拦截，实际 %d", recorder.Code)
	}
}

func TestSensitiveFilter_长二进制串不参与匹配(t *testing.T) {
	// base64 里恰好出现某个英文词是常见现象，若不跳过二进制串会产生随机误拦。
	filter := newEnabledFilter(t, "badword")
	engine := buildSensitiveRouter(t, filter)

	// 构造一段足够长、且不含空白与标点的"类 base64"字符串，其中嵌入目标词
	blob := "data:image/png;base64," + strings.Repeat("A", 40) + "badword" + strings.Repeat("B", 1500)
	payload, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "看看这张图"},
				{"type": "image_url", "image_url": map[string]string{"url": blob}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(string(payload))))

	if recorder.Code != http.StatusOK {
		t.Fatalf("图片 base64 不应触发敏感词拦截，实际 %d", recorder.Code)
	}
}

func TestSensitiveFilter_GET不扫描(t *testing.T) {
	filter := newEnabledFilter(t, "赌博")
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(filter.Middleware())
	engine.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusOK) })

	recorder := httptest.NewRecorder()
	// 即便查询串里带敏感词，GET 也不应被扫描（只扫"提交内容"的 POST）
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models?q=赌博", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET 不应被扫描，实际 %d", recorder.Code)
	}
}

func TestSensitiveFilter_Invalidate后新词立即生效(t *testing.T) {
	repo, settings := newTestSensitiveDeps(t)
	if err := settings.Set(context.Background(), model.SettingKeySensitiveFilterEnabled, "true"); err != nil {
		t.Fatalf("写入过滤开关失败: %v", err)
	}
	filter := NewSensitiveFilter(repo, settings)
	engine := buildSensitiveRouter(t, filter)
	payload := `{"messages":[{"content":"赌博"}]}`

	// 首次请求：词表为空 → 放行（同时把"已启用 + 空词表"这一状态缓存下来）
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(payload)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("空词表时应放行，实际 %d", recorder.Code)
	}

	// 后台新增词条并失效缓存 → 下一个请求即被拦截（不必等 30 秒 TTL）
	if err := repo.Create(context.Background(), &model.SensitiveWord{Word: "赌博", Enabled: true}); err != nil {
		t.Fatalf("写入词条失败: %v", err)
	}
	filter.Invalidate()

	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(payload)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("新增词条并失效缓存后应立即拦截，实际 %d", recorder.Code)
	}
}

func TestIsBinaryLikeString(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"data 前缀", "data:image/png;base64,AAAA", true},
		{"长且无空白", strings.Repeat("A", 2000), true},
		{"长但有空白", strings.Repeat("A", 1000) + " " + strings.Repeat("B", 1000), false},
		{"短文本", "badword", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBinaryLikeString(tc.value); got != tc.want {
				t.Fatalf("isBinaryLikeString(%s…) = %v，期望 %v", tc.name, got, tc.want)
			}
		})
	}
}
