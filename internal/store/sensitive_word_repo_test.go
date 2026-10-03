// 敏感词仓储的测试。
//
// 意图（Why）：
//
//	词表是"匹配器的输入"，这里的三个行为直接决定过滤是否可靠：
//	  1) 归一化——大小写与首尾空白不同的同一词条必须只存一条，
//	     否则删掉一条另一条仍在生效，站长会认为"删了没用"；
//	  2) 批量导入跳过重复——站长粘贴的词表里混有已加过的词是常态，
//	     整批失败会让人反复试错；
//	  3) 空词/超长词被拒绝——空词入库会让匹配器命中一切（等于拦全站）。
//
// 流转（Flow）：
//
//	go test ./internal/store/ → 真实 SQLite 上跑完整迁移后校验读写行为
//
// 扩展（Extend）：
//
//	新增词条属性后，仿照本文件补"写入 → 读回一致"的断言。
package store

import (
	"context"
	"errors"
	"testing"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// newTestSensitiveWordRepo 构造基于临时数据库的敏感词仓储。
func newTestSensitiveWordRepo(t *testing.T) model.SensitiveWordRepository {
	t.Helper()
	st := newTestStore(t)
	return NewSensitiveWordRepository(st.DB())
}

func TestSensitiveWordRepo_归一化与重复拒绝(t *testing.T) {
	ctx := context.Background()
	repo := newTestSensitiveWordRepo(t)

	if err := repo.Create(ctx, &model.SensitiveWord{Word: "  BadWord  ", Enabled: true}); err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	items, err := repo.List(ctx, false)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 条词条，实际 %d", len(items))
	}
	// 落库时已归一化为小写去空白
	if items[0].Word != "badword" {
		t.Fatalf("词条未归一化，实际 %q", items[0].Word)
	}

	// 大小写不同视为同一个词 → 必须报重复
	err = repo.Create(ctx, &model.SensitiveWord{Word: "BADWORD", Enabled: true})
	if !errors.Is(err, model.ErrSensitiveWordDuplicated) {
		t.Fatalf("大小写不同的同名词条应报重复，实际 %v", err)
	}
}

func TestSensitiveWordRepo_空词与超长词被拒绝(t *testing.T) {
	ctx := context.Background()
	repo := newTestSensitiveWordRepo(t)

	if err := repo.Create(ctx, &model.SensitiveWord{Word: "   ", Enabled: true}); err == nil {
		t.Fatalf("空词条应被拒绝（空词会让匹配器命中一切）")
	}

	long := make([]rune, model.MaxSensitiveWordRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := repo.Create(ctx, &model.SensitiveWord{Word: string(long), Enabled: true}); err == nil {
		t.Fatalf("超长词条应被拒绝")
	}
}

func TestSensitiveWordRepo_批量导入跳过重复(t *testing.T) {
	ctx := context.Background()
	repo := newTestSensitiveWordRepo(t)

	if err := repo.Create(ctx, &model.SensitiveWord{Word: "已存在", Enabled: true}); err != nil {
		t.Fatalf("预置词条失败: %v", err)
	}

	imported, err := repo.CreateMany(ctx, []*model.SensitiveWord{
		{Word: "新词一", Enabled: true},
		{Word: "已存在", Enabled: true}, // 重复：应被跳过而不是整批失败
		{Word: "NEWWORD", Enabled: true},
	})
	if err != nil {
		t.Fatalf("批量导入失败: %v", err)
	}
	if imported != 2 {
		t.Fatalf("应新增 2 条，实际 %d", imported)
	}

	items, err := repo.List(ctx, false)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("应有 3 条词条，实际 %d", len(items))
	}
}

func TestSensitiveWordRepo_按启用状态过滤与增删改(t *testing.T) {
	ctx := context.Background()
	repo := newTestSensitiveWordRepo(t)

	enabled := &model.SensitiveWord{Word: "启用词", Enabled: true, Category: "测试"}
	disabled := &model.SensitiveWord{Word: "停用词", Enabled: false}
	for _, word := range []*model.SensitiveWord{enabled, disabled} {
		if err := repo.Create(ctx, word); err != nil {
			t.Fatalf("新增失败: %v", err)
		}
	}

	onlyEnabled, err := repo.List(ctx, true)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(onlyEnabled) != 1 || onlyEnabled[0].Word != "启用词" {
		t.Fatalf("只应返回启用词条，实际 %+v", onlyEnabled)
	}

	// 更新：把启用词改为停用
	enabled.Enabled = false
	enabled.Remark = "临时停用"
	if err := repo.Update(ctx, enabled); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	onlyEnabled, err = repo.List(ctx, true)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(onlyEnabled) != 0 {
		t.Fatalf("更新后不应有启用词条，实际 %+v", onlyEnabled)
	}

	// 删除不存在：应返回 NotFound 而不是静默成功
	if err := repo.Delete(ctx, 999999); !errors.Is(err, model.ErrSensitiveWordNotFound) {
		t.Fatalf("删除不存在的词条应返回 NotFound，实际 %v", err)
	}
	// 删除存在
	if err := repo.Delete(ctx, disabled.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := repo.GetByID(ctx, disabled.ID); !errors.Is(err, model.ErrSensitiveWordNotFound) {
		t.Fatalf("删除后查询应返回 NotFound，实际 %v", err)
	}
}
