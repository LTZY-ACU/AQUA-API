// 语料共建计划仓储（三张表）的单元测试。
//
// 意图（Why）：
//
//	这套仓储撑着两件不能出错的事：
//	  1) 清单与福利资格的读写——它们决定"采不采"和"免不免计费"；
//	  2) 样本的落地与导出——样本一旦写坏（截断没标记、重复写入），
//	     污染的是训练数据本身，事后极难发现。
//	因此重点验证：幂等、预览不泄露全文、筛选口径在列表与导出之间一致。
//
// 流转（Flow）：
//
//	go test ./internal/store/ -run Corpus
//
// 扩展（Extend）：
//
//	新增采集字段时，在 TestCorpusRepository_样本_写入与读取 里补断言。
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newCorpusRepo 在临时库上构造语料仓储。
func newCorpusRepo(t *testing.T) model.CorpusRepository {
	t.Helper()
	st := newTestStore(t)
	return NewCorpusRepository(st.DB())
}

// TestCorpusRepository_清单_增删查 覆盖模型清单的维护路径。
func TestCorpusRepository_清单_增删查(t *testing.T) {
	ctx := context.Background()
	repo := newCorpusRepo(t)

	for _, name := range []string{"AQUA-CALL/deepseek-v4.1-flash", "z-ai/glm-5.3"} {
		if err := repo.UpsertCorpusModel(ctx, &model.CorpusModel{
			Model: name, Enabled: true, Remark: "国产大模型",
		}); err != nil {
			t.Fatalf("写入清单项失败: %v", err)
		}
	}
	// 重复写入同一模型应为更新而不是插入两条
	if err := repo.UpsertCorpusModel(ctx, &model.CorpusModel{
		Model: "z-ai/glm-5.3", Enabled: false, Remark: "暂停采集",
	}); err != nil {
		t.Fatalf("更新清单项失败: %v", err)
	}

	all, err := repo.ListCorpusModels(ctx)
	if err != nil {
		t.Fatalf("查询清单失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("清单应有 2 条（同模型不应重复），实际 %d", len(all))
	}

	enabled, err := repo.EnabledCorpusModels(ctx)
	if err != nil {
		t.Fatalf("查询启用清单失败: %v", err)
	}
	if len(enabled) != 1 || enabled[0] != "AQUA-CALL/deepseek-v4.1-flash" {
		t.Fatalf("启用清单应只剩按次专线那条，实际 %v", enabled)
	}

	if err := repo.DeleteCorpusModel(ctx, "AQUA-CALL/deepseek-v4.1-flash"); err != nil {
		t.Fatalf("移除清单项失败: %v", err)
	}
	enabled, _ = repo.EnabledCorpusModels(ctx)
	if len(enabled) != 0 {
		t.Fatalf("移除后不应还有启用项，实际 %v", enabled)
	}

	// 空模型名必须被拒（否则会在清单里留一个永远匹配不上的垃圾项）
	if err := repo.UpsertCorpusModel(ctx, &model.CorpusModel{Model: "  "}); err == nil {
		t.Error("空模型名应被拒绝")
	}
}

// TestCorpusRepository_样本_写入读取与幂等 覆盖样本落库的核心性质。
func TestCorpusRepository_样本_写入读取与幂等(t *testing.T) {
	ctx := context.Background()
	repo := newCorpusRepo(t)

	sample := &model.CorpusSample{
		RequestID:     "req-1",
		UserID:        37,
		TokenID:       5,
		Model:         "AQUA-CALL/deepseek-v4.1-flash",
		UpstreamModel: "deepseek-v4.1-flash",
		ChannelID:     4,
		ChannelKeyID:  514,
		IsStream:      true,
		StatusCode:    200,
		RequestBody:   `{"model":"m","messages":[{"role":"user","content":"你好"}]}`,
		ResponseBody:  "data: {\"choices\":[]}\n\ndata: [DONE]\n",
		RequestBytes:  52,
		ResponseBytes: 36,
	}
	if err := repo.CreateCorpusSample(ctx, sample); err != nil {
		t.Fatalf("写入样本失败: %v", err)
	}
	if sample.ID == 0 {
		t.Fatal("写入后应回填主键")
	}

	// 幂等：同一次请求重复落库应被唯一索引挡下，而不是写出两条
	dup := *sample
	dup.ID = 0
	if err := repo.CreateCorpusSample(ctx, &dup); !errors.Is(err, model.ErrCorpusSampleExists) {
		t.Fatalf("重复 request_id 应返回 ErrCorpusSampleExists，实际 %v", err)
	}

	// 单条读取：应拿到全文
	got, err := repo.GetCorpusSample(ctx, sample.ID)
	if err != nil {
		t.Fatalf("读取样本失败: %v", err)
	}
	if got.RequestBody != sample.RequestBody || got.ResponseBody != sample.ResponseBody {
		t.Error("单条读取应返回正文全文")
	}
	if !got.IsStream || got.Truncated || got.Incomplete {
		t.Errorf("标记位不正确：is_stream=%v truncated=%v incomplete=%v",
			got.IsStream, got.Truncated, got.Incomplete)
	}

	// 列表读取：正文只给预览（这是"顺手翻用户对话"在流程上不成立的关键）
	long := &model.CorpusSample{
		RequestID:    "req-2",
		UserID:       139,
		Model:        "z-ai/glm-5.3",
		StatusCode:   200,
		RequestBody:  string(make([]byte, 0)) + repeatRune('x', 2000),
		ResponseBody: repeatRune('y', 2000),
	}
	if err := repo.CreateCorpusSample(ctx, long); err != nil {
		t.Fatalf("写入长样本失败: %v", err)
	}
	items, total, err := repo.ListCorpusSamples(ctx, model.CorpusSampleQuery{Limit: 10})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("应有 2 条样本，实际 total=%d len=%d", total, len(items))
	}
	for _, item := range items {
		if len(item.RequestBody) >= 2000 || len(item.ResponseBody) >= 2000 {
			t.Error("列表查询不应返回正文全文（只给预览片段）")
		}
	}

	// 失踪样本应返回明确的"不存在"
	if _, err := repo.GetCorpusSample(ctx, 999999); !errors.Is(err, model.ErrCorpusSampleNotFound) {
		t.Fatalf("不存在的样本应返回 ErrCorpusSampleNotFound，实际 %v", err)
	}
}

// TestCorpusRepository_样本_筛选与导出 覆盖"列表与导出用同一套筛选语义"。
func TestCorpusRepository_样本_筛选与导出(t *testing.T) {
	ctx := context.Background()
	repo := newCorpusRepo(t)

	insert := func(requestID, name string, userID uint64) {
		t.Helper()
		if err := repo.CreateCorpusSample(ctx, &model.CorpusSample{
			RequestID: requestID, Model: name, UserID: userID, StatusCode: 200,
			RequestBody: `{"a":1}`, ResponseBody: "ok",
		}); err != nil {
			t.Fatalf("写入样本失败: %v", err)
		}
	}
	insert("r1", "AQUA-CALL/deepseek-v4.1-flash", 37)
	insert("r2", "AQUA-CALL/deepseek-v4.1-flash", 139)
	insert("r3", "z-ai/glm-5.3", 37)

	items, total, err := repo.ListCorpusSamples(ctx, model.CorpusSampleQuery{
		Model: "AQUA-CALL/deepseek-v4.1-flash", Limit: 10,
	})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("按模型筛选应命中 2 条，实际 total=%d len=%d err=%v", total, len(items), err)
	}

	items, total, err = repo.ListCorpusSamples(ctx, model.CorpusSampleQuery{UserID: 37, Limit: 10})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("按用户筛选应命中 2 条，实际 total=%d len=%d err=%v", total, len(items), err)
	}

	// 导出：应拿到全文，且筛选口径与列表一致
	exported := make([]string, 0, 3)
	if err := repo.IterateCorpusSamples(ctx, model.CorpusSampleQuery{UserID: 37},
		func(item *model.CorpusSample) error {
			exported = append(exported, item.RequestID)
			return nil
		}); err != nil {
		t.Fatalf("导出遍历失败: %v", err)
	}
	if len(exported) != 2 {
		t.Fatalf("导出应命中 2 条，实际 %v", exported)
	}
}

// TestCorpusRepository_样本_统计与按期清理 覆盖规模统计与留存纪律。
func TestCorpusRepository_样本_统计与按期清理(t *testing.T) {
	ctx := context.Background()
	repo := newCorpusRepo(t)

	old := time.Now().Add(-48 * time.Hour)
	for i, at := range []time.Time{old, time.Now()} {
		if err := repo.CreateCorpusSample(ctx, &model.CorpusSample{
			RequestID: "req-" + string(rune('a'+i)), Model: "m", UserID: uint64(i + 1),
			StatusCode: 200, RequestBody: "req", ResponseBody: "resp",
			RequestBytes: 3, ResponseBytes: 4, CreatedAt: at,
		}); err != nil {
			t.Fatalf("写入样本失败: %v", err)
		}
	}

	stat, err := repo.StatCorpusSamples(ctx)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if stat.Samples != 2 || stat.Users != 2 || stat.RequestBytes != 6 || stat.ResponseBytes != 8 {
		t.Errorf("统计不正确：%+v", stat)
	}
	if stat.EarliestAt.IsZero() || stat.LatestAt.IsZero() {
		t.Error("应记录最早与最新时间")
	}

	// 清理 24 小时之前的：应只删掉那条旧的
	removed, err := repo.DeleteCorpusSamplesBefore(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("应清理 1 条，实际 removed=%d err=%v", removed, err)
	}
	stat, _ = repo.StatCorpusSamples(ctx)
	if stat.Samples != 1 {
		t.Errorf("清理后应剩 1 条，实际 %d", stat.Samples)
	}
}

// TestCorpusRepository_福利资格_生效与撤销 覆盖免计费名单的读写。
func TestCorpusRepository_福利资格_生效与撤销(t *testing.T) {
	ctx := context.Background()
	repo := newCorpusRepo(t)

	grant := &model.CorpusGrant{
		UserID: 37, Model: "AQUA-CALL/deepseek-v4.1-flash",
		FreeAccess: true, Remark: "第一批报名用户",
	}
	if err := repo.UpsertCorpusGrant(ctx, grant); err != nil {
		t.Fatalf("写入福利资格失败: %v", err)
	}

	active, err := repo.ActiveCorpusGrants(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("生效资格应有 1 条，实际 %d err=%v", len(active), err)
	}
	if active[0].Status != model.CorpusGrantActive || !active[0].FreeAccess {
		t.Errorf("资格状态不正确：%+v", active[0])
	}

	// 重复发放应为更新而非新增（同一用户同一模型只有一条）
	grant.Remark = "改个备注"
	if err := repo.UpsertCorpusGrant(ctx, grant); err != nil {
		t.Fatalf("重复发放失败: %v", err)
	}
	all, _ := repo.ListCorpusGrants(ctx)
	if len(all) != 1 {
		t.Fatalf("同一用户同一模型应只有一条资格，实际 %d", len(all))
	}

	// 撤销后不应再出现在"生效"名单里（Guard 据此停止免计费）
	if err := repo.DeleteCorpusGrant(ctx, 37, grant.Model); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	active, _ = repo.ActiveCorpusGrants(ctx)
	if len(active) != 0 {
		t.Fatalf("撤销后生效名单应为空，实际 %d", len(active))
	}
	// 但行要保留下来，便于事后解释"他为什么一度免费"
	all, _ = repo.ListCorpusGrants(ctx)
	if len(all) != 1 || all[0].Status != model.CorpusGrantRevoked {
		t.Errorf("撤销应保留行并置为撤销态，实际 %+v", all)
	}

	// 重新发放应把状态复位为生效
	grant.Status = model.CorpusGrantActive
	if err := repo.UpsertCorpusGrant(ctx, grant); err != nil {
		t.Fatalf("重新发放失败: %v", err)
	}
	active, _ = repo.ActiveCorpusGrants(ctx)
	if len(active) != 1 {
		t.Errorf("重新发放后应恢复生效，实际 %d", len(active))
	}
}

// repeatRune 生成 n 个字符的字符串（构造"超长正文"用）。
func repeatRune(ch rune, n int) string {
	buf := make([]rune, n)
	for i := range buf {
		buf[i] = ch
	}
	return string(buf)
}
