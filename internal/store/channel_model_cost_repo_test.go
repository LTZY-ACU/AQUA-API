// 上游进价仓储的测试。
//
// 测试重点（为什么测这些）：
//   - 整体替换语义必须精确：站长在界面上删掉一行再保存，结果就该是这一行消失，
//     而不是"看起来删了其实还在生效"（那是成本核算最隐蔽的错误来源）；
//   - 同渠道内模型名重复必须提前报错并指出是哪个模型，而不是抛一条 SQL 约束错误；
//   - channelID 为 0 必须返回空而不是全表：漏传参数绝不能变成"拿到所有渠道的成本"。
package store

import (
	"context"
	"strings"
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// newTestChannelCostRepo 构造基于临时数据库的上游进价仓储。
func newTestChannelCostRepo(t *testing.T) model.ChannelModelCostRepository {
	t.Helper()
	st := newTestStore(t)
	return NewChannelModelCostRepository(st.DB())
}

func TestChannelModelCostRepo_整体替换的新增更新删除(t *testing.T) {
	ctx := context.Background()
	repo := newTestChannelCostRepo(t)

	// 首次保存：两条新增
	created, updated, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "qwen-long", PromptPrice: 3_000_000, CompletionPrice: 15_000_000},
		{Model: "qwen-*", PromptPrice: 1_000_000},
	})
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	if created != 2 || updated != 0 {
		t.Fatalf("首次保存应为 新增2/更新0，实际 新增%d/更新%d", created, updated)
	}

	items, err := repo.ListByChannel(ctx, 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应有 2 条成本规则，实际 %d", len(items))
	}

	// 第二次保存：改一条价格 + 删掉另一条 + 新增一条
	created, updated, err = repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "qwen-long", PromptPrice: 6_000_000, CompletionPrice: 15_000_000},
		{Model: "glm-*", PromptPrice: 500_000},
	})
	if err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}
	if created != 1 || updated != 1 {
		t.Fatalf("二次保存应为 新增1/更新1，实际 新增%d/更新%d", created, updated)
	}

	items, err = repo.ListByChannel(ctx, 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应有 2 条成本规则，实际 %d（删除未生效？）", len(items))
	}
	byModel := map[string]*model.ChannelModelCost{}
	for _, item := range items {
		byModel[item.Model] = item
	}
	if _, exists := byModel["qwen-*"]; exists {
		t.Fatalf("已从提交集合中移除的规则必须被删除，实际仍存在：%v", byModel)
	}
	if got := byModel["qwen-long"]; got == nil || got.PromptPrice != 6_000_000 {
		t.Fatalf("已有规则的改价未生效，实际 %+v", got)
	}
	if got := byModel["glm-*"]; got == nil || got.PromptPrice != 500_000 {
		t.Fatalf("新增规则未写入，实际 %+v", got)
	}

	// 其他渠道不受影响（防止"整体替换"误伤别的渠道）
	otherCreated, _, err := repo.ReplaceForChannel(ctx, 8, []*model.ChannelModelCost{
		{Model: "qwen-long", PromptPrice: 1},
	})
	if err != nil || otherCreated != 1 {
		t.Fatalf("其他渠道保存失败: created=%d err=%v", otherCreated, err)
	}
	if items, err = repo.ListByChannel(ctx, 7); err != nil || len(items) != 2 {
		t.Fatalf("渠道 7 的规则数量被其他渠道的保存影响，实际 %d，err=%v", len(items), err)
	}
}

func TestChannelModelCostRepo_同模型重复提前报错(t *testing.T) {
	ctx := context.Background()
	repo := newTestChannelCostRepo(t)

	_, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "dup", PromptPrice: 1},
		{Model: " dup ", PromptPrice: 2}, // 去空白后同名
	})
	if err == nil {
		t.Fatalf("同渠道下同模型出现两条应报错")
	}
	// 错误信息必须点出是哪个模型，否则站长无从下手
	if !strings.Contains(err.Error(), "dup") {
		t.Fatalf("错误信息应包含模型名，实际 %v", err)
	}
}

func TestChannelModelCostRepo_非法规则被拒绝(t *testing.T) {
	ctx := context.Background()
	repo := newTestChannelCostRepo(t)

	if _, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "", PromptPrice: 1},
	}); err == nil {
		t.Fatalf("空模型名应被拒绝")
	}
	if _, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "m", PromptPrice: -1},
	}); err == nil {
		t.Fatalf("负成本应被拒绝")
	}
	if _, _, err := repo.ReplaceForChannel(ctx, 0, []*model.ChannelModelCost{
		{Model: "m", PromptPrice: 1},
	}); err == nil {
		t.Fatalf("未指定渠道应被拒绝")
	}
}

func TestChannelModelCostRepo_清空与按渠道隔离(t *testing.T) {
	ctx := context.Background()
	repo := newTestChannelCostRepo(t)

	if _, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "m", PromptPrice: 1},
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 提交空集合 = 清空该渠道的成本配置
	if _, _, err := repo.ReplaceForChannel(ctx, 7, nil); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	items, err := repo.ListByChannel(ctx, 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("清空后应无规则，实际 %d", len(items))
	}

	// channelID 为 0 必须返回空：漏传参数不能变成"返回全表"
	empty, err := repo.ListByChannel(ctx, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("channelID=0 应返回空列表，实际 %d", len(empty))
	}

	// 删除渠道时清理
	if _, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "m", PromptPrice: 1},
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := repo.DeleteByChannel(ctx, 7); err != nil {
		t.Fatalf("按渠道删除失败: %v", err)
	}
	if items, err = repo.ListByChannel(ctx, 7); err != nil || len(items) != 0 {
		t.Fatalf("按渠道删除后应无规则，实际 %d，err=%v", len(items), err)
	}
}

func TestChannelModelCostRepo_免费规则可存储且被识别(t *testing.T) {
	ctx := context.Background()
	repo := newTestChannelCostRepo(t)

	// 上游免费（四个价格全 0）是一条合法且有意义的规则，
	// 必须能与"未录入"区分开：前者存得下、读得出 IsFree=true。
	if _, _, err := repo.ReplaceForChannel(ctx, 7, []*model.ChannelModelCost{
		{Model: "free-model", Remark: "免费额度池"},
	}); err != nil {
		t.Fatalf("保存免费规则失败: %v", err)
	}

	items, err := repo.ListByChannel(ctx, 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 1 || !items[0].IsFree() {
		t.Fatalf("免费规则应被保存并识别为免费，实际 %+v", items)
	}
	if items[0].Remark != "免费额度池" {
		t.Fatalf("备注未保存，实际 %q", items[0].Remark)
	}
}
