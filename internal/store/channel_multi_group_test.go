// 本文件覆盖渠道「多分组」能力（迁移 0025）。
//
// 为什么单独测：多分组是路由匹配的依据，写错会直接表现为
// "用户调用某分组下的模型时 0ms 返回 503（没有任何候选渠道）"——
// 这种故障在日志里看不到渠道名，定位成本极高，必须由测试兜住。
package store

import (
	"context"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// TestChannelRepository_多分组_创建读回与匹配 覆盖清单落库与按分组查询。
func TestChannelRepository_多分组_创建读回与匹配(t *testing.T) {
	repo, _ := newTestChannelRepo(t)
	ctx := context.Background()

	ch := newValidChannel()
	ch.Name = "多分组渠道"
	ch.Group = "free"                    // 主分组 = 清单首项
	ch.Groups = []string{"free", "aqua"} // 同时服务免费组与自营组
	if err := repo.Create(ctx, ch); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}

	// 读回：清单完整且顺序保持（首项即主分组）
	got, err := repo.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "free" || got.Groups[1] != "aqua" {
		t.Fatalf("分组清单读回不一致：%v", got.Groups)
	}
	if got.Group != "free" {
		t.Fatalf("主分组应为 free，实际 %q", got.Group)
	}

	// 两个分组都能查到该渠道
	enabled := model.ChannelStatusEnabled
	for _, group := range []string{"free", "aqua"} {
		list, err := repo.List(ctx, model.ChannelQuery{Group: group, Status: &enabled})
		if err != nil {
			t.Fatalf("按分组 %s 查询失败: %v", group, err)
		}
		if len(list) != 1 || list[0].ID != ch.ID {
			t.Fatalf("分组 %s 应能查到该渠道，实际 %d 条", group, len(list))
		}
	}

	// 未列出的分组不应查到（避免"分组写错却仍然命中"的隐性错误）
	list, err := repo.List(ctx, model.ChannelQuery{Group: "not-in-list", Status: &enabled})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("未列出的分组不应命中，实际 %d 条", len(list))
	}

	// 子串误判防线：清单里有 free，但 requests 分组是 "fr" / "freebies" 都不得命中
	for _, group := range []string{"fr", "freebies"} {
		list, err := repo.List(ctx, model.ChannelQuery{Group: group, Status: &enabled})
		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("分组 %q 是清单项的前缀/扩展，不应命中，实际 %d 条", group, len(list))
		}
	}
}

// TestChannelRepository_单分组渠道_兼容既有行为 覆盖"未配置清单时按 group_name 匹配"。
func TestChannelRepository_单分组渠道_兼容既有行为(t *testing.T) {
	repo, _ := newTestChannelRepo(t)
	ctx := context.Background()

	ch := newValidChannel()
	ch.Group = "legacy" // 只设单分组，Groups 留空（模拟升级前的既有数据）
	if err := repo.Create(ctx, ch); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}

	got, err := repo.GetByID(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	// 清单为空 → 回退为主分组；这是升级后行为不变的关键
	if list := got.GroupList(); len(list) != 1 || list[0] != "legacy" {
		t.Fatalf("单分组渠道的清单应回退为 [legacy]，实际 %v", list)
	}

	enabled := model.ChannelStatusEnabled
	list, err := repo.List(ctx, model.ChannelQuery{Group: "legacy", Status: &enabled})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("单分组渠道应能按 group_name 命中，实际 %d 条", len(list))
	}
}
