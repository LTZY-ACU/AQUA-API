// 密钥余额核算的测试。
//
// 测试重点（为什么测这些）：
//   - "未录进价"绝不能按 0 成本算：那会让站长以为某模型不花钱，
//     进而以为余额还很充足——这是资损级的误导；
//   - 剩余 = 录入余额 − 估算消耗，且允许为负（超支必须看得见）；
//   - 未录入余额的密钥不能显示"剩余 0"，而应显示"未录入"；
//   - 已从池中删除的密钥不展示（它没有"剩余"可言）。
package server

import (
	"testing"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

func TestBuildChannelKeyUsage_未录进价不按零成本算(t *testing.T) {
	keys := map[uint64]*model.ChannelKey{
		1: {ID: 1, ChannelID: 9, Label: "池 #001", Status: model.ChannelKeyStatusEnabled,
			Balance: 100},
	}
	usage := []*model.ChannelKeyUsage{
		{ChannelKeyID: 1, ChannelID: 9, Model: "有进价的模型", Requests: 10,
			PromptTokens: 1_000_000, CompletionTokens: 0},
		{ChannelKeyID: 1, ChannelID: 9, Model: "没进价的模型", Requests: 5,
			PromptTokens: 1_000_000, CompletionTokens: 0},
	}
	costs := []*model.ChannelModelCost{
		{ID: 1, ChannelID: 9, Model: "有进价的模型", PromptPrice: 10_000_000},
	}

	items := buildChannelKeyUsage(keys, usage, costs)
	if len(items) != 1 {
		t.Fatalf("应返回 1 把密钥，实际 %d", len(items))
	}
	item := items[0]

	// 只有"有进价的模型"计入成本：1M × 10_000_000 / 1M = 10_000_000
	if item.EstimatedCost != 10_000_000 {
		t.Fatalf("估算消耗应为 10_000_000，实际 %d", item.EstimatedCost)
	}
	if item.PricedRequests != 10 || item.UnpricedRequests != 5 {
		t.Fatalf("能/不能估算的请求数应为 10/5，实际 %d/%d", item.PricedRequests, item.UnpricedRequests)
	}
	if len(item.UnpricedModels) != 1 || item.UnpricedModels[0] != "没进价的模型" {
		t.Fatalf("应列出未录进价的模型，实际 %v", item.UnpricedModels)
	}

	// 剩余 = 100 − 10_000_000，允许为负（表示按进价估算已远超录入余额）
	if !item.BalanceKnown {
		t.Fatalf("录入了余额应标记 BalanceKnown")
	}
	if want := int64(100 - 10_000_000); item.Remaining != want {
		t.Fatalf("剩余应为 %d，实际 %d", want, item.Remaining)
	}
}

func TestBuildChannelKeyUsage_未录余额不显示剩余(t *testing.T) {
	keys := map[uint64]*model.ChannelKey{
		2: {ID: 2, ChannelID: 9, Balance: model.BalanceUnknown},
	}
	usage := []*model.ChannelKeyUsage{
		{ChannelKeyID: 2, ChannelID: 9, Model: "m", Requests: 1, PromptTokens: 1_000_000},
	}
	costs := []*model.ChannelModelCost{{ID: 1, ChannelID: 9, Model: "m", PromptPrice: 1_000_000}}

	items := buildChannelKeyUsage(keys, usage, costs)
	if len(items) != 1 {
		t.Fatalf("应返回 1 把密钥，实际 %d", len(items))
	}
	item := items[0]
	if item.BalanceKnown || !item.BalanceUnknown {
		t.Fatalf("未录入余额应标记为未知，实际 known=%v unknown=%v", item.BalanceKnown, item.BalanceUnknown)
	}
	if item.Remaining != 0 {
		t.Fatalf("未录入余额时剩余应为 0（无意义值），实际 %d", item.Remaining)
	}
	// 消耗仍然要算出来：站长正是靠它决定该给这把密钥补多少额度
	if item.EstimatedCost != 1_000_000 {
		t.Fatalf("即使未录余额，也应算出估算消耗，实际 %d", item.EstimatedCost)
	}
}

func TestBuildChannelKeyUsage_池内空密钥也出现(t *testing.T) {
	// 没有任何用量的密钥必须出现在列表里并显示"已用 0 / 剩余 = 录入余额"，
	// 否则站长会以为"余额显示丢了"。
	keys := map[uint64]*model.ChannelKey{
		3: {ID: 3, ChannelID: 9, Balance: 49},
	}

	items := buildChannelKeyUsage(keys, nil, nil)
	if len(items) != 1 {
		t.Fatalf("无用量的密钥也应返回，实际 %d", len(items))
	}
	if items[0].Requests != 0 || items[0].EstimatedCost != 0 || items[0].Remaining != 49 {
		t.Fatalf("无用量的密钥应显示 已用0/消耗0/剩余49，实际 %+v", items[0])
	}
}

func TestBuildChannelKeyUsage_已删除的密钥不展示(t *testing.T) {
	// 日志里仍有历史用量，但密钥已不在池中：它没有"剩余"可言，不应混进列表
	keys := map[uint64]*model.ChannelKey{
		1: {ID: 1, ChannelID: 9, Balance: 10},
	}
	usage := []*model.ChannelKeyUsage{
		{ChannelKeyID: 1, ChannelID: 9, Model: "m", Requests: 1},
		{ChannelKeyID: 99, ChannelID: 9, Model: "m", Requests: 100}, // 已删除
	}

	items := buildChannelKeyUsage(keys, usage, nil)
	if len(items) != 1 || items[0].ChannelKeyID != 1 {
		t.Fatalf("只应返回仍在池中的密钥，实际 %+v", items)
	}
}

func TestBuildChannelKeyUsage_按剩余升序把该处理的排前面(t *testing.T) {
	keys := map[uint64]*model.ChannelKey{
		1: {ID: 1, ChannelID: 9, Balance: 1000},                 // 剩余 1000
		2: {ID: 2, ChannelID: 9, Balance: 100},                  // 剩余 100
		3: {ID: 3, ChannelID: 9, Balance: model.BalanceUnknown}, // 未录入 → 排最后
	}
	items := buildChannelKeyUsage(keys, nil, nil)

	if len(items) != 3 {
		t.Fatalf("应返回 3 把密钥，实际 %d", len(items))
	}
	if items[0].ChannelKeyID != 2 {
		t.Fatalf("剩余最少的应排第一，实际首个为 %d（剩余 %d）", items[0].ChannelKeyID, items[0].Remaining)
	}
	if items[1].ChannelKeyID != 1 {
		t.Fatalf("剩余次少的应排第二，实际 %d", items[1].ChannelKeyID)
	}
	if items[2].ChannelKeyID != 3 {
		t.Fatalf("未录入余额的应排最后，实际 %d", items[2].ChannelKeyID)
	}
}

func TestBuildChannelKeyUsage_全免费进价成本为零(t *testing.T) {
	// 上游免费是明确结论（如免费额度池）：成本确实为 0，但必须"有规则"才算数，
	// 否则会与"未录进价"混为一谈。
	keys := map[uint64]*model.ChannelKey{
		1: {ID: 1, ChannelID: 9, Balance: 0},
	}
	usage := []*model.ChannelKeyUsage{
		{ChannelKeyID: 1, ChannelID: 9, Model: "免费模型", Requests: 3, PromptTokens: 5_000_000},
	}
	costs := []*model.ChannelModelCost{{ID: 1, ChannelID: 9, Model: "免费模型"}}

	items := buildChannelKeyUsage(keys, usage, costs)
	item := items[0]
	if item.EstimatedCost != 0 {
		t.Fatalf("免费模型成本应为 0，实际 %d", item.EstimatedCost)
	}
	if item.PricedRequests != 3 || item.UnpricedRequests != 0 {
		t.Fatalf("免费模型应算作'已录进价'，实际 priced=%d unpriced=%d",
			item.PricedRequests, item.UnpricedRequests)
	}
	if len(item.UnpricedModels) != 0 {
		t.Fatalf("免费模型不应出现在未录进价清单里，实际 %v", item.UnpricedModels)
	}
}

// TestBuildChannelKeyUsage_按次进价成本非零 锁住"按次计费渠道的成本不再是 0"。
//
// 背景（docs/17 缺口 1）：核算路径此前只按 token 公式算成本，对"只填了 PerCallPrice"
// 的进价规则结果恒为 0 —— 不报错，但会让密钥余额永不减少、毛利虚高、重试率也算不出来。
// 这条测试要求：纯按次规则下，成本 = 每次单价 × 成功请求数，且这些请求计入"已录进价"。
func TestBuildChannelKeyUsage_按次进价成本非零(t *testing.T) {
	keys := map[uint64]*model.ChannelKey{
		1: {ID: 1, ChannelID: 9, Balance: 1_000_000},
	}
	usage := []*model.ChannelKeyUsage{
		{ChannelKeyID: 1, ChannelID: 9, UpstreamModel: "glm-5.3", Requests: 4},
	}
	costs := []*model.ChannelModelCost{
		{ID: 1, ChannelID: 9, Model: "glm-5.3", PerCallPrice: 2200},
	}

	items := buildChannelKeyUsage(keys, usage, costs)
	if len(items) != 1 {
		t.Fatalf("应返回 1 把密钥，实际 %d", len(items))
	}
	item := items[0]
	if item.EstimatedCost != 8800 {
		t.Fatalf("按次成本应为 2200 × 4 = 8800（不能是 0），实际 %d", item.EstimatedCost)
	}
	if item.PricedRequests != 4 || item.UnpricedRequests != 0 {
		t.Fatalf("按次规则应算作已录进价，实际 priced=%d unpriced=%d",
			item.PricedRequests, item.UnpricedRequests)
	}
	if want := int64(1_000_000 - 8800); item.Remaining != want {
		t.Fatalf("剩余应为 %d，实际 %d", want, item.Remaining)
	}
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("去重应保持原顺序，实际 %v", got)
	}
}
