// 邮件群发仓储的单元测试。
//
// 测试重点（都与"不可撤回、不能重复"有关）：
//   - 重复入队必须幂等：同一用户在同一批次里只能有一行，否则"点两次发送"会重复投递；
//   - 已发过的不会被再取到：断点续发的正确性完全依赖这一点；
//   - 失败原因必须落库：否则"某某没收到"这个问题无法回答；
//   - 进度里的开始/结束时间在传零值时不被清空：否则"开始时间"会在中途丢失。
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newTestBroadcastRepo 构造基于临时数据库的群发仓储。
func newTestBroadcastRepo(t *testing.T) model.EmailBroadcastRepository {
	t.Helper()
	st := newTestStore(t)
	return NewEmailBroadcastRepository(st.DB())
}

// newTestBroadcast 构造一个最小可用的批次对象。
func newTestBroadcast() *model.EmailBroadcast {
	return &model.EmailBroadcast{
		Template: "billing_line",
		Subject:  "【LTZY-API】测试通知",
		BodyHTML: "<p>正文</p>",
		Status:   model.BroadcastStatusPending,
	}
}

func TestEmailBroadcastRepository_创建与读取(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	bc := newTestBroadcast()
	bc.CreatedBy = 7
	if err := repo.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if bc.ID == 0 {
		t.Fatal("创建后应回填 ID")
	}

	got, err := repo.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if got.Subject != bc.Subject || got.BodyHTML != bc.BodyHTML {
		t.Fatalf("正文/主题应原样读回（快照语义）：%+v", got)
	}
	if got.Status != model.BroadcastStatusPending {
		t.Fatalf("新批次状态应为 pending，实际 %q", got.Status)
	}
	if !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() {
		t.Fatal("未开始的批次，开始/结束时间应为零值")
	}
	if !got.Status.IsActive() {
		t.Fatal("pending 应属于未完成状态（重启后需要续发）")
	}

	if _, err := repo.GetByID(ctx, 999999); !errors.Is(err, model.ErrEmailBroadcastNotFound) {
		t.Fatalf("不存在的批次应返回 ErrEmailBroadcastNotFound，实际 %v", err)
	}
}

// TestEmailBroadcastRepository_收件人入队幂等 覆盖"重复调用不重复入队"。
func TestEmailBroadcastRepository_收件人入队幂等(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	bc := newTestBroadcast()
	if err := repo.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}

	items := []*model.EmailBroadcastRecipient{
		{UserID: 1, Email: "A@Example.com"},
		{UserID: 2, Email: "b@example.com"},
	}
	added, err := repo.AddRecipients(ctx, bc.ID, items)
	if err != nil {
		t.Fatalf("写入收件人失败: %v", err)
	}
	if added != 2 {
		t.Fatalf("首次应新增 2 条，实际 %d", added)
	}

	// 重复写入同一批人：一条都不应新增（这是"点两次发送不会重复投递"的基础）
	addedAgain, err := repo.AddRecipients(ctx, bc.ID, items)
	if err != nil {
		t.Fatalf("重复写入失败: %v", err)
	}
	if addedAgain != 0 {
		t.Fatalf("重复写入应新增 0 条，实际 %d", addedAgain)
	}

	total, err := repo.CountRecipients(ctx, bc.ID, "")
	if err != nil {
		t.Fatalf("统计收件人失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("收件人总数应为 2，实际 %d", total)
	}

	// 邮箱必须被归一化为小写（否则 A@x.com 与 a@x.com 会被当成两个人）
	rows, _, err := repo.ListRecipients(ctx, bc.ID, "", 10, 0)
	if err != nil {
		t.Fatalf("查询收件人失败: %v", err)
	}
	if rows[0].Email != "a@example.com" {
		t.Fatalf("邮箱应被归一化为小写，实际 %q", rows[0].Email)
	}
}

// TestEmailBroadcastRepository_待发名单与逐人状态 覆盖"已发过的不再被取到"。
func TestEmailBroadcastRepository_待发名单与逐人状态(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	bc := newTestBroadcast()
	if err := repo.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}
	if _, err := repo.AddRecipients(ctx, bc.ID, []*model.EmailBroadcastRecipient{
		{UserID: 1, Email: "a@example.com"},
		{UserID: 2, Email: "b@example.com"},
		{UserID: 3, Email: "c@example.com"},
	}); err != nil {
		t.Fatalf("写入收件人失败: %v", err)
	}

	pending, err := repo.NextPending(ctx, bc.ID, 10)
	if err != nil {
		t.Fatalf("取待发名单失败: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("应有 3 个待发，实际 %d", len(pending))
	}
	// 顺序必须稳定（按 id 升序）：分页取人时不能漏也不能重
	if pending[0].ID >= pending[1].ID || pending[1].ID >= pending[2].ID {
		t.Fatal("待发名单应按 id 升序返回")
	}

	// 第一人成功、第二人失败
	if err := repo.MarkRecipient(ctx, pending[0].ID, model.RecipientStatusSent, "", time.Now()); err != nil {
		t.Fatalf("标记成功失败: %v", err)
	}
	if err := repo.MarkRecipient(ctx, pending[1].ID, model.RecipientStatusFailed, "550 mailbox unavailable", time.Now()); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}

	// 已处理过的不应再出现（断点续发的正确性依赖这一点）
	left, err := repo.NextPending(ctx, bc.ID, 10)
	if err != nil {
		t.Fatalf("取待发名单失败: %v", err)
	}
	if len(left) != 1 || left[0].UserID != 3 {
		t.Fatalf("只剩 1 个待发且应为 user 3，实际 %+v", left)
	}

	sent, _ := repo.CountRecipients(ctx, bc.ID, model.RecipientStatusSent)
	failed, _ := repo.CountRecipients(ctx, bc.ID, model.RecipientStatusFailed)
	if sent != 1 || failed != 1 {
		t.Fatalf("成功/失败计数应为 1/1，实际 %d/%d", sent, failed)
	}

	// 失败明细必须可查（含原因），否则"某某没收到"无法回答
	failedRows, total, err := repo.ListRecipients(ctx, bc.ID, model.RecipientStatusFailed, 10, 0)
	if err != nil {
		t.Fatalf("查询失败明细失败: %v", err)
	}
	if total != 1 || len(failedRows) != 1 {
		t.Fatalf("失败明细应为 1 条，实际 %d/%d", total, len(failedRows))
	}
	if failedRows[0].Error == "" {
		t.Fatal("失败明细必须带上失败原因")
	}

	// 非法状态必须被拒（防止把收件人改回 pending 导致重复发送）
	if err := repo.MarkRecipient(ctx, pending[0].ID, model.RecipientStatusPending, "", time.Now()); err == nil {
		t.Fatal("收件人状态只允许 sent / failed，其他取值应被拒绝")
	}
}

// TestEmailBroadcastRepository_进度更新保留开始时间 覆盖"零值不清空已有时间"。
func TestEmailBroadcastRepository_进度更新保留开始时间(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	bc := newTestBroadcast()
	if err := repo.Create(ctx, bc); err != nil {
		t.Fatalf("创建批次失败: %v", err)
	}

	started := time.Now().Add(-time.Minute)
	if err := repo.UpdateProgress(ctx, bc.ID, model.BroadcastStatusRunning, 10, 3, 1, started, time.Time{}); err != nil {
		t.Fatalf("更新进度失败: %v", err)
	}
	got, err := repo.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("开始时间应被写入")
	}
	if !got.FinishedAt.IsZero() {
		t.Fatal("未结束时结束时间应保持零值")
	}

	// 后续更新传零值：开始时间必须保持不变（否则中途会丢失"什么时候开始的"）
	if err := repo.UpdateProgress(ctx, bc.ID, model.BroadcastStatusRunning, 10, 8, 1, time.Time{}, time.Time{}); err != nil {
		t.Fatalf("更新进度失败: %v", err)
	}
	again, err := repo.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if again.StartedAt.IsZero() {
		t.Fatal("传零值时开始时间不应被清空")
	}
	if again.Sent != 8 || again.Failed != 1 {
		t.Fatalf("进度应更新为 8/1，实际 %d/%d", again.Sent, again.Failed)
	}

	// 结束：写入完成时间
	if err := repo.UpdateProgress(ctx, bc.ID, model.BroadcastStatusDone, 10, 9, 1, time.Time{}, time.Now()); err != nil {
		t.Fatalf("更新进度失败: %v", err)
	}
	done, err := repo.GetByID(ctx, bc.ID)
	if err != nil {
		t.Fatalf("读取批次失败: %v", err)
	}
	if done.FinishedAt.IsZero() || !done.IsFinished() {
		t.Fatalf("done 批次应带结束时间且判定为已结束：%+v", done)
	}
}

// TestEmailBroadcastRepository_未完成批次查询 覆盖重启续发的入口。
func TestEmailBroadcastRepository_未完成批次查询(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	mk := func(status model.BroadcastStatus) *model.EmailBroadcast {
		bc := newTestBroadcast()
		if err := repo.Create(ctx, bc); err != nil {
			t.Fatalf("创建批次失败: %v", err)
		}
		if err := repo.UpdateProgress(ctx, bc.ID, status, 1, 0, 0, time.Time{}, time.Time{}); err != nil {
			t.Fatalf("更新进度失败: %v", err)
		}
		return bc
	}

	pending := mk(model.BroadcastStatusPending)
	running := mk(model.BroadcastStatusRunning)
	mk(model.BroadcastStatusDone)
	mk(model.BroadcastStatusCanceled)

	items, err := repo.ListUnfinished(ctx)
	if err != nil {
		t.Fatalf("查询未完成批次失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("未完成批次应为 2（pending + running），实际 %d", len(items))
	}
	// 按 id 升序：先创建的先续发
	if items[0].ID != pending.ID || items[1].ID != running.ID {
		t.Fatalf("未完成批次应按 id 升序返回，实际 %d, %d", items[0].ID, items[1].ID)
	}
}

// TestEmailBroadcastRepository_列表分页与校验 覆盖后台历史列表与非法输入。
func TestEmailBroadcastRepository_列表分页与校验(t *testing.T) {
	repo := newTestBroadcastRepo(t)
	ctx := context.Background()

	// 主题或正文为空的批次必须被拒：发出去只会被标为垃圾邮件，而群发不可撤回
	if err := repo.Create(ctx, &model.EmailBroadcast{Subject: "", BodyHTML: "<p>x</p>"}); err == nil {
		t.Fatal("主题为空应被拒绝")
	}
	if err := repo.Create(ctx, &model.EmailBroadcast{Subject: "s", BodyHTML: "  "}); err == nil {
		t.Fatal("正文为空应被拒绝")
	}

	for i := 0; i < 3; i++ {
		bc := newTestBroadcast()
		bc.Subject = "主题" + string(rune('A'+i))
		if err := repo.Create(ctx, bc); err != nil {
			t.Fatalf("创建批次失败: %v", err)
		}
		// 创建时间都取当前秒，验证"同秒创建也能稳定分页"（次级排序用 id）
		time.Sleep(time.Millisecond)
	}

	page, total, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("查询批次列表失败: %v", err)
	}
	if total != 3 || len(page) != 2 {
		t.Fatalf("总数应为 3、首页应为 2 条，实际 %d/%d", total, len(page))
	}

	rest, _, err := repo.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("第二页应为 1 条，实际 %d", len(rest))
	}
	if rest[0].ID == page[0].ID || rest[0].ID == page[1].ID {
		t.Fatal("分页结果不应重复（同秒创建的批次也要按 id 稳定排序）")
	}
}
