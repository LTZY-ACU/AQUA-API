// 本文件是 model.ChannelModelCostRepository 的 SQL 实现（上游进价）。
//
// 意图（Why）：
//
//	成本规则是"配置类"数据：量小（每个渠道几条到几十条）、读少（只在后台核算时读）、
//	写少（站长偶尔维护）。因此本层只做纯粹存取，不加缓存——
//	核算发生在后台请求内，一次全量读取比维护缓存更简单、也更容易解释。
//
//	写入采用「按模型名匹配更新 + 删除多余 + 插入新增」而不是"全删全插"：
//	后者会让每次保存都重置所有行的主键与创建时间，审计上看不出"只是改了一个价格"。
//
// 流转（Flow）：
//
//	NewChannelModelCostRepository(db)
//	  ├─ 后台编辑：server → ListByChannel（回显）+ ReplaceForChannel（整体保存）
//	  └─ 渠道删除：server → DeleteByChannel（清理孤儿数据）
//
// 扩展（Extend）：
//
//	新增成本维度时：先建迁移加列，再同步本文件的 channelModelCostColumns /
//	scanChannelModelCost / ReplaceForChannel 的 INSERT 与 UPDATE 三处列清单，
//	缺一处即静默丢数据。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// channelModelCostColumns 集中定义查询列，顺序必须与 scanChannelModelCost 的扫描顺序严格一致。
const channelModelCostColumns = `id, channel_id, model, prompt_price, cache_price, completion_price,
	per_call_price, remark, created_at, updated_at`

// channelModelCostRepository 是 model.ChannelModelCostRepository 的 SQL 实现，并发安全。
type channelModelCostRepository struct {
	db *sql.DB
}

// NewChannelModelCostRepository 创建上游进价仓储。
func NewChannelModelCostRepository(db *sql.DB) model.ChannelModelCostRepository {
	return &channelModelCostRepository{db: db}
}

// ListByChannel 返回某渠道的全部成本规则。
func (r *channelModelCostRepository) ListByChannel(ctx context.Context, channelID uint64) ([]*model.ChannelModelCost, error) {
	// channelID 为 0 时返回空：漏传参数绝不能变成"返回全部渠道的成本"，
	// 那会把 A 渠道的成本算到 B 渠道头上，属于静默错账。
	if channelID == 0 {
		return []*model.ChannelModelCost{}, nil
	}

	rows, err := r.db.QueryContext(ctx,
		"SELECT "+channelModelCostColumns+" FROM channel_model_costs WHERE channel_id = ? ORDER BY id ASC",
		channelID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询上游成本失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	costs := make([]*model.ChannelModelCost, 0, 16)
	for rows.Next() {
		cost, err := scanChannelModelCost(rows)
		if err != nil {
			return nil, err
		}
		costs = append(costs, cost)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历上游成本失败: %w", err)
	}
	return costs, nil
}

// ReplaceForChannel 用给定集合整体替换某渠道的成本规则。
//
// 全程在【一个事务】内完成，因此不存在"删了旧的、新的没写进去"的中间状态。
// 返回实际新增与更新的条数（未变化的行也会被更新一次，实现简单且结果一致）。
func (r *channelModelCostRepository) ReplaceForChannel(ctx context.Context, channelID uint64,
	costs []*model.ChannelModelCost) (int, int, error) {
	if channelID == 0 {
		return 0, 0, errors.New("store: 保存上游成本时必须指定渠道")
	}

	// 先做一遍校验与去重：同一渠道下模型名重复会在唯一索引处报错，
	// 提前在 Go 层给出"哪条重复了"的明确信息，比让站长面对一条 SQL 错误好得多。
	existing, err := r.ListByChannel(ctx, channelID)
	if err != nil {
		return 0, 0, err
	}
	byModel := make(map[string]uint64, len(existing))
	for _, item := range existing {
		byModel[item.Model] = item.ID
	}

	seen := make(map[string]struct{}, len(costs))
	for _, cost := range costs {
		if cost == nil {
			return 0, 0, errors.New("store: 上游成本规则为空")
		}
		cost.ChannelID = channelID
		cost.Model = strings.TrimSpace(cost.Model)
		cost.Remark = strings.TrimSpace(cost.Remark)
		if err := cost.Validate(); err != nil {
			return 0, 0, fmt.Errorf("store: 上游成本规则非法: %w", err)
		}
		if _, duplicated := seen[cost.Model]; duplicated {
			return 0, 0, fmt.Errorf("store: 模型 %q 出现了多条成本规则（同一渠道下只能有一条）", cost.Model)
		}
		seen[cost.Model] = struct{}{}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: 开启上游成本保存事务失败: %w", err)
	}
	// 出错时回滚；成功提交后 Rollback 返回 ErrTxDone，可安全忽略。
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	created, updated := 0, 0

	for _, cost := range costs {
		if id, ok := byModel[cost.Model]; ok {
			_, err := tx.ExecContext(ctx, `
				UPDATE channel_model_costs
				SET prompt_price = ?, cache_price = ?, completion_price = ?, per_call_price = ?,
				    remark = ?, updated_at = ?
				WHERE id = ?`,
				cost.PromptPrice, cost.CachePrice, cost.CompletionPrice, cost.PerCallPrice,
				cost.Remark, now.Unix(), id)
			if err != nil {
				return 0, 0, fmt.Errorf("store: 更新上游成本 %q 失败: %w", cost.Model, err)
			}
			cost.ID = id
			cost.UpdatedAt = now
			updated++
			continue
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO channel_model_costs
				(channel_id, model, prompt_price, cache_price, completion_price, per_call_price,
				 remark, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			channelID, cost.Model, cost.PromptPrice, cost.CachePrice, cost.CompletionPrice,
			cost.PerCallPrice, cost.Remark, now.Unix(), now.Unix())
		if err != nil {
			return 0, 0, fmt.Errorf("store: 新增上游成本 %q 失败: %w", cost.Model, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, 0, fmt.Errorf("store: 读取新增上游成本 ID 失败: %w", err)
		}
		cost.ID = uint64(id)
		cost.CreatedAt = now
		cost.UpdatedAt = now
		created++
	}

	// 删除本次未提交的规则：删除动作必须显式且可预期——站长在界面上删掉一行再保存，
	// 结果就是这一行消失，而不是"看起来删了其实还在生效"。
	for _, item := range existing {
		if _, kept := seen[item.Model]; kept {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM channel_model_costs WHERE id = ?", item.ID); err != nil {
			return 0, 0, fmt.Errorf("store: 删除上游成本 %q 失败: %w", item.Model, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("store: 提交上游成本保存失败: %w", err)
	}
	return created, updated, nil
}

// DeleteByChannel 删除某渠道的全部成本规则。
func (r *channelModelCostRepository) DeleteByChannel(ctx context.Context, channelID uint64) error {
	if channelID == 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		"DELETE FROM channel_model_costs WHERE channel_id = ?", channelID); err != nil {
		return fmt.Errorf("store: 删除渠道 %d 的上游成本失败: %w", channelID, err)
	}
	return nil
}

// scanChannelModelCost 把一行数据映射为成本规则对象。
func scanChannelModelCost(sc rowScanner) (*model.ChannelModelCost, error) {
	var (
		id              uint64
		channelID       uint64
		modelName       string
		promptPrice     int64
		cachePrice      int64
		completionPrice int64
		perCallPrice    int64
		remark          string
		createdAt       int64
		updatedAt       int64
	)

	if err := sc.Scan(&id, &channelID, &modelName, &promptPrice, &cachePrice, &completionPrice,
		&perCallPrice, &remark, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取上游成本字段失败: %w", err)
	}

	return &model.ChannelModelCost{
		ID:              id,
		ChannelID:       channelID,
		Model:           modelName,
		PromptPrice:     promptPrice,
		CachePrice:      cachePrice,
		CompletionPrice: completionPrice,
		PerCallPrice:    perCallPrice,
		Remark:          remark,
		CreatedAt:       time.Unix(createdAt, 0),
		UpdatedAt:       time.Unix(updatedAt, 0),
	}, nil
}
