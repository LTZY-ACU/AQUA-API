// 分组 RPM 限流中间件的单元测试。
//
// 意图（Why）：
//
//	RPM 上限是"套餐档位"的速率承诺，一旦计数边界写错，会出现两类严重后果：
//	  1) 少限一次（应为 429 却放行）→ 承诺失效，失控令牌照旧高频调用；
//	  2) 多限一次（未超限却 429）→ 正常用户被误伤，直接引发投诉。
//	因此用例把"第 limit 次放行、第 limit+1 次拒绝"与"0 = 不限"锁死。
//
// 流转（Flow）：
//
//	go test ./internal/server/middleware/ -run GroupRPM → 直接驱动 gin 引擎
//
// 扩展（Extend）：
//
//	新增分组限流维度（如秒级窗口、按令牌覆盖）时，参照本文件补充边界用例。
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
	"github.com/LTZY-ACU/ltzy-api/internal/reqctx"
)

// fakeGroupRPMRepo 是只实现 GetByName 的假分组仓储。
type fakeGroupRPMRepo struct {
	groups map[string]*model.ModelGroup
	err    error
}

func (f *fakeGroupRPMRepo) GetByName(_ context.Context, name string) (*model.ModelGroup, error) {
	if f.err != nil {
		return nil, f.err
	}
	group, ok := f.groups[name]
	if !ok {
		return nil, model.ErrModelGroupNotFound
	}
	return group, nil
}

// newGroupRPMEngine 构造"限流中间件 + 业务处理器"的最小 gin 引擎。
func newGroupRPMEngine(limiter *GroupRPMLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/chat/completions", limiter.Middleware(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return engine
}

// groupRPMRequest 发起一次属于指定分组的请求。
func groupRPMRequest(engine *gin.Engine, group string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(reqctx.WithGroup(req.Context(), group))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestGroupRPMLimiter_超限返回429(t *testing.T) {
	limiter := NewGroupRPMLimiter(&fakeGroupRPMRepo{groups: map[string]*model.ModelGroup{
		"vip": {Name: "vip", RpmLimit: 2, Enabled: true},
	}}, model.DefaultGroupName)
	engine := newGroupRPMEngine(limiter)

	// 第 1、2 次应放行
	for i := 1; i <= 2; i++ {
		if rec := groupRPMRequest(engine, "vip"); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际状态码 %d", i, rec.Code)
		}
	}

	// 第 3 次应被拒绝，且错误 code 为约定的语义键
	rec := groupRPMRequest(engine, "vip")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次请求应返回 429，实际 %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v，响应：%s", err, rec.Body.String())
	}
	if body.Error.Code != "quota.group_rpm_exceeded" {
		t.Fatalf("错误 code 应为 quota.group_rpm_exceeded，实际 %q", body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Fatal("错误消息应提示该分组每分钟上限")
	}
}

func TestGroupRPMLimiter_零上限不限流(t *testing.T) {
	limiter := NewGroupRPMLimiter(&fakeGroupRPMRepo{groups: map[string]*model.ModelGroup{
		"free": {Name: "free", RpmLimit: 0, Enabled: true},
	}}, model.DefaultGroupName)
	engine := newGroupRPMEngine(limiter)

	// 0 = 不限：连续多次都应放行
	for i := 0; i < 50; i++ {
		if rec := groupRPMRequest(engine, "free"); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行（0 = 不限），实际 %d", i+1, rec.Code)
		}
	}
}

func TestGroupRPMLimiter_窗口翻篇后计数重置(t *testing.T) {
	limiter := NewGroupRPMLimiter(&fakeGroupRPMRepo{groups: map[string]*model.ModelGroup{
		"vip": {Name: "vip", RpmLimit: 1, Enabled: true},
	}}, model.DefaultGroupName)

	base := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return base }
	engine := newGroupRPMEngine(limiter)

	if rec := groupRPMRequest(engine, "vip"); rec.Code != http.StatusOK {
		t.Fatalf("窗口内第 1 次应放行，实际 %d", rec.Code)
	}
	if rec := groupRPMRequest(engine, "vip"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("同窗口第 2 次应 429，实际 %d", rec.Code)
	}

	// 推进到下一分钟窗口：计数应重置
	limiter.now = func() time.Time { return base.Add(time.Minute) }
	if rec := groupRPMRequest(engine, "vip"); rec.Code != http.StatusOK {
		t.Fatalf("新窗口第 1 次应放行，实际 %d", rec.Code)
	}
}

func TestGroupRPMLimiter_不同分组互不影响(t *testing.T) {
	limiter := NewGroupRPMLimiter(&fakeGroupRPMRepo{groups: map[string]*model.ModelGroup{
		"a": {Name: "a", RpmLimit: 1, Enabled: true},
		"b": {Name: "b", RpmLimit: 1, Enabled: true},
	}}, model.DefaultGroupName)
	engine := newGroupRPMEngine(limiter)

	if rec := groupRPMRequest(engine, "a"); rec.Code != http.StatusOK {
		t.Fatalf("分组 a 第 1 次应放行，实际 %d", rec.Code)
	}
	// a 已用尽，但 b 有自己的预算
	if rec := groupRPMRequest(engine, "b"); rec.Code != http.StatusOK {
		t.Fatalf("分组 b 第 1 次应放行，实际 %d", rec.Code)
	}
	if rec := groupRPMRequest(engine, "a"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("分组 a 第 2 次应 429，实际 %d", rec.Code)
	}
}

func TestGroupRPMLimiter_未指定分组回退默认分组(t *testing.T) {
	limiter := NewGroupRPMLimiter(&fakeGroupRPMRepo{groups: map[string]*model.ModelGroup{
		model.DefaultGroupName: {Name: model.DefaultGroupName, RpmLimit: 1, Enabled: true},
	}}, model.DefaultGroupName)
	engine := newGroupRPMEngine(limiter)

	if rec := groupRPMRequest(engine, ""); rec.Code != http.StatusOK {
		t.Fatalf("默认分组第 1 次应放行，实际 %d", rec.Code)
	}
	if rec := groupRPMRequest(engine, ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("默认分组第 2 次应 429，实际 %d", rec.Code)
	}
}
