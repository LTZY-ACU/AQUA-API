// 语料共建判定组件与采集缓冲的单元测试。
//
// 意图（Why）：
//
//	这一层是"每次转发都要过一遍"的热路径，也是最容易悄悄出错的层：
//	  · 判定错了 → 要么采了不该采的（隐私事故），要么该采的没采（数据缺口）；
//	  · 缓冲没有上限 → 一次异常请求就能把网关内存打爆；
//	  · 快照加载失败时若清空 → 福利账户会突然开始被扣钱（用户侧可见的事故）。
//	因此把这三条性质用测试钉死。
//
// 流转（Flow）：
//
//	go test ./internal/corpus/
//
// 扩展（Extend）：
//
//	新增"用户可退出采集"时，在 TestGuard_ShouldCollect 旁补一条"已退出者不采"。
package corpus

import (
	"context"
	"errors"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

var errFake = errors.New("fake repo failure")

// fakeCorpusRepo 是内存版仓储，只实现 Guard 需要的两个方法。
type fakeCorpusRepo struct {
	model.CorpusRepository
	models   []string
	grants   []*model.CorpusGrant
	failNext bool
}

func (f *fakeCorpusRepo) EnabledCorpusModels(_ context.Context) ([]string, error) {
	if f.failNext {
		return nil, errFake
	}
	return f.models, nil
}

func (f *fakeCorpusRepo) ActiveCorpusGrants(_ context.Context) ([]*model.CorpusGrant, error) {
	if f.failNext {
		return nil, errFake
	}
	return f.grants, nil
}

// TestGuard_ShouldCollect 覆盖"只有清单内的模型才采"。
func TestGuard_ShouldCollect(t *testing.T) {
	repo := &fakeCorpusRepo{models: []string{"AQUA-CALL/deepseek-v4.1-flash"}}
	guard := NewGuard(repo)

	if guard.ShouldCollect("AQUA-CALL/deepseek-v4.1-flash") {
		t.Fatal("快照尚未加载时不应采集（宁可保守，也不要在不知清单的情况下乱采）")
	}
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("加载快照失败: %v", err)
	}

	if !guard.ShouldCollect("AQUA-CALL/deepseek-v4.1-flash") {
		t.Error("清单内的模型应采集")
	}
	if guard.ShouldCollect("AQUA-CALL/glm-5.3") {
		t.Error("清单外的模型不应采集")
	}
	if guard.ShouldCollect("") {
		t.Error("空模型名不应采集")
	}
}

// TestGuard_IsFree 覆盖"按用户 × 模型判免计费"。
func TestGuard_IsFree(t *testing.T) {
	repo := &fakeCorpusRepo{grants: []*model.CorpusGrant{
		{UserID: 37, Model: "AQUA-CALL/deepseek-v4.1-flash", FreeAccess: true,
			Status: model.CorpusGrantActive},
		// 未开启免计费的资格（只授权采集）不应产生免费效果
		{UserID: 139, Model: "AQUA-CALL/deepseek-v4.1-flash", FreeAccess: false,
			Status: model.CorpusGrantActive},
	}}
	guard := NewGuard(repo)
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("加载快照失败: %v", err)
	}

	if !guard.IsFree(37, "AQUA-CALL/deepseek-v4.1-flash") {
		t.Error("已授权的福利账户应免计费")
	}
	if guard.IsFree(139, "AQUA-CALL/deepseek-v4.1-flash") {
		t.Error("free_access=false 的资格不应免计费")
	}
	if guard.IsFree(37, "AQUA-CALL/glm-5.3") {
		t.Error("福利只对授权的模型生效，其他模型照常计费")
	}
	if guard.IsFree(999, "AQUA-CALL/deepseek-v4.1-flash") {
		t.Error("未授权的用户不应免计费")
	}
	if guard.IsFree(0, "AQUA-CALL/deepseek-v4.1-flash") {
		t.Error("无用户身份时不应免计费")
	}
}

// TestGuard_刷新失败保留旧快照 覆盖"加载失败不能清空"这条关键约定。
//
// 若清空：福利账户会在一次数据库抖动之后突然开始被扣钱，而账单上看不出原因。
func TestGuard_刷新失败保留旧快照(t *testing.T) {
	repo := &fakeCorpusRepo{
		models: []string{"m1"},
		grants: []*model.CorpusGrant{{UserID: 7, Model: "m1", FreeAccess: true,
			Status: model.CorpusGrantActive}},
	}
	guard := NewGuard(repo)
	if err := guard.Refresh(context.Background()); err != nil {
		t.Fatalf("首次加载失败: %v", err)
	}

	repo.failNext = true
	if err := guard.Refresh(context.Background()); err == nil {
		t.Fatal("仓储故障时 Refresh 应返回错误")
	}
	repo.failNext = false

	if !guard.ShouldCollect("m1") || !guard.IsFree(7, "m1") {
		t.Error("刷新失败后应沿用上一次的快照，而不是清空")
	}
}

// TestRecorder_上限与截断 覆盖"极端请求不会撑爆内存"。
func TestRecorder_上限与截断(t *testing.T) {
	request := []byte("0123456789")
	rec := NewRecorder(request, 4)

	body, size, truncated := rec.Request()
	if body != "0123" || size != 10 || !truncated {
		t.Errorf("请求侧应截断到 4 字节并记录原始大小 10，实际 body=%q size=%d truncated=%v",
			body, size, truncated)
	}

	// 返回侧同样受限
	if _, err := rec.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write 不应返回错误（它挂在回写链路上）: %v", err)
	}
	if _, err := rec.Write([]byte("ghij")); err != nil {
		t.Fatalf("Write 不应返回错误: %v", err)
	}
	resp, respSize, respTruncated := rec.Response()
	if resp != "abcd" || respSize != 10 || !respTruncated {
		t.Errorf("返回侧应截断到 4 字节并记录原始大小 10，实际 body=%q size=%d truncated=%v",
			resp, respSize, respTruncated)
	}

	snapshot := rec.Snapshot()
	if !snapshot.Truncated {
		t.Error("任一侧被截断时，样本整体都应标记 truncated")
	}
	if snapshot.Incomplete {
		t.Error("未标记中断时不应是 incomplete")
	}
}

// TestRecorder_正常路径与中断标记 覆盖常见的完整采集。
func TestRecorder_正常路径与中断标记(t *testing.T) {
	rec := NewRecorder([]byte(`{"model":"m"}`), DefaultMaxBytes)
	_, _ = rec.Write([]byte("data: hello\n\n"))

	snapshot := rec.Snapshot()
	if snapshot.Truncated || snapshot.Incomplete {
		t.Error("正常采集不应带截断或中断标记")
	}
	if snapshot.RequestBody != `{"model":"m"}` {
		t.Errorf("请求原文应原样保留，实际 %q", snapshot.RequestBody)
	}
	if snapshot.ResponseBody != "data: hello\n\n" {
		t.Errorf("返回正文应原样保留，实际 %q", snapshot.ResponseBody)
	}
	if snapshot.RequestBytes != 13 || snapshot.ResponseBytes != 13 {
		t.Errorf("字节数记录不正确：%d / %d", snapshot.RequestBytes, snapshot.ResponseBytes)
	}

	rec.MarkAborted()
	if !rec.Snapshot().Incomplete {
		t.Error("标记中断后应体现为 incomplete")
	}
}

// TestRecorderFrom_未采集时为 nil 覆盖"绝大多数请求走零开销路径"。
func TestRecorderFrom_未采集时为nil(t *testing.T) {
	if RecorderFrom(context.Background()) != nil {
		t.Error("未挂载采集器时应返回 nil")
	}
	rec := NewRecorder(nil, 0)
	ctx := WithRecorder(context.Background(), rec)
	if RecorderFrom(ctx) != rec {
		t.Error("挂载后应能取回同一个采集器")
	}
	// 上限为 0 时应回退到默认值，而不是"一律截断成空"
	if rec.maxBytes != DefaultMaxBytes {
		t.Errorf("非法上限应回退默认值，实际 %d", rec.maxBytes)
	}
}
