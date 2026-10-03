// agent 密钥的 SQLite 仓储实现。
//
// 意图（Why）：
//
//	鉴权路径在每次 agent 请求时都会走一遍 GetByHash，因此这里的索引选择
//	与"只查必要列"直接决定 agent 的响应延迟——不建索引或 SELECT * 都会被
//	高频调用放大。
//
// 流转（Flow）：
//
//	Create/List/SetStatus/Delete → agent_keys 表
//	GetByHash ← agent 鉴权中间件（每次请求一次，走 key_hash 唯一索引）
//
// 扩展（Extend）：
//
//	要加"按创建时间倒序"：给 created_at 加索引，或在 List 里改 ORDER BY；
//	这张表预期行数极小（站长手动生成，通常个位数），
//	现阶段不必为它加索引——真到需要时数据量已经到了能测量的时候。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// agentKeyRepository 是 model.AgentKeyRepository 的 SQLite 实现。
type agentKeyRepository struct {
	db *sql.DB
}

// NewAgentKeyRepository 构造 agent 密钥仓储。
func NewAgentKeyRepository(db *sql.DB) model.AgentKeyRepository {
	return &agentKeyRepository{db: db}
}

// agentKeyColumns 列出全部列，Create / scan 共用。
//
// 刻意不用 SELECT *：这张表以后若加列，SELECT * 会让扫描顺序与列顺序
// 悄悄错位，而那种 bug 表现为"字段张冠李戴"，极难定位。
const agentKeyColumns = "id, role, name, key_hash, status, created_at, expires_at"

// agentKeyDefaultLimit / agentKeyMaxLimit 是列表查询的行数边界。
const (
	agentKeyDefaultLimit = 50
	agentKeyMaxLimit     = 200
)

// agentKeyLimit 把请求里的 Limit 归一到合法范围。
//
// 上限必须存在：agent 鉴权接口是公网可达的，
// 一个 limit=-1 就能把整张表拉进内存。默认值取得小，
// 因为站长手里的密钥数量天然是个位数。
func agentKeyLimit(limit int) int {
	if limit <= 0 {
		return agentKeyDefaultLimit
	}
	if limit > agentKeyMaxLimit {
		return agentKeyMaxLimit
	}
	return limit
}

// Create 新增一条密钥。
func (r *agentKeyRepository) Create(ctx context.Context, key *model.AgentKey) error {
	if key == nil {
		return errors.New("store: agent 密钥为空")
	}
	if key.KeyHash == "" {
		// 拒绝写入没有摘要的行：这种行永远无法通过鉴权，
		// 却会占着唯一索引的位置，属于"建了但永远不能用"的脏数据。
		return errors.New("store: agent 密钥摘要为空")
	}

	now := time.Now()
	if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	if key.Status == 0 {
		key.Status = model.AgentKeyStatusEnabled
	}

	var expiresAt int64
	if !key.ExpiresAt.IsZero() {
		expiresAt = key.ExpiresAt.Unix()
	}

	res, err := r.db.ExecContext(ctx,
		"INSERT INTO agent_keys (role, name, key_hash, status, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		string(key.Role), key.Name, key.KeyHash, key.Status, key.CreatedAt.Unix(), expiresAt)
	if err != nil {
		return fmt.Errorf("store: 创建 agent 密钥失败: %w", err)
	}
	if id, idErr := res.LastInsertId(); idErr == nil {
		key.ID = uint64(id)
	}
	return nil
}

// GetByHash 按摘要查找（鉴权路径）。
func (r *agentKeyRepository) GetByHash(ctx context.Context, keyHash string) (*model.AgentKey, error) {
	if keyHash == "" {
		return nil, model.ErrAgentKeyNotFound
	}
	row := r.db.QueryRowContext(ctx,
		"SELECT "+agentKeyColumns+" FROM agent_keys WHERE key_hash = ?", keyHash)

	k, err := scanAgentKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 统一返回"不存在"，不区分"没有这把 key"与"有但被禁用"——
			// 区分开等于给攻击者一个"这把 key 存在过"的探测信号。
			return nil, model.ErrAgentKeyNotFound
		}
		return nil, err
	}
	return k, nil
}

// scanAgentKey 扫描一行并组装成 model.AgentKey。
//
// 复用包内已有的 rowScanner（见 channel_repo.go），不重复定义同一接口。
func scanAgentKey(row rowScanner) (*model.AgentKey, error) {
	var (
		k         model.AgentKey
		role      string
		name      sql.NullString
		createdAt int64
		expiresAt int64
	)
	if err := row.Scan(&k.ID, &role, &name, &k.KeyHash, &k.Status, &createdAt, &expiresAt); err != nil {
		return nil, err
	}
	k.Role = model.AgentRole(role)
	k.Name = name.String
	k.CreatedAt = time.Unix(createdAt, 0)
	if expiresAt > 0 {
		k.ExpiresAt = time.Unix(expiresAt, 0)
	}
	return &k, nil
}

// List 按条件查询密钥列表。
func (r *agentKeyRepository) List(ctx context.Context, q model.AgentKeyQuery) ([]*model.AgentKey, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString("SELECT " + agentKeyColumns + " FROM agent_keys")
	if q.Role != "" {
		sb.WriteString(" WHERE role = ?")
		args = append(args, string(q.Role))
	}
	if q.OnlyEnabled {
		if q.Role != "" {
			sb.WriteString(" AND")
		} else {
			sb.WriteString(" WHERE")
		}
		sb.WriteString(" status = ?")
		args = append(args, model.AgentKeyStatusEnabled)
	}
	// 按 ID 升序：让后台列表的顺序稳定，否则每次刷新行会跳动。
	sb.WriteString(" ORDER BY id ASC LIMIT ?")
	args = append(args, agentKeyLimit(q.Limit))

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询 agent 密钥失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*model.AgentKey
	for rows.Next() {
		k, err := scanAgentKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 读取 agent 密钥失败: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历 agent 密钥失败: %w", err)
	}
	return out, nil
}

// SetStatus 启用 / 禁用一把密钥。
//
// 影响已过期的密钥时不做特殊处理：过期密钥本来就进不来，
// 再判一次"是否过期"只会让"禁用一把已过期密钥"变成无意义操作。
func (r *agentKeyRepository) SetStatus(ctx context.Context, id uint64, status int) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE agent_keys SET status = ? WHERE id = ?", status, id)
	if err != nil {
		return fmt.Errorf("store: 更新 agent 密钥状态失败: %w", err)
	}
	// 影响行数为 0 说明 id 不存在：显式返回错误，
	// 让"密钥已被删除"不会被静默当成"设置成功"。
	if n, nErr := res.RowsAffected(); nErr == nil && n == 0 {
		return model.ErrAgentKeyNotFound
	}
	return nil
}

// UpdateName 修改密钥的备注名。
//
// 刻意【只写 name 一列】：若顺手把整行读出来再写回，
// 一次"改备注"的请求就会连带把 status、role 一起写回，
// 于是它有了改权限的能力。SQL UPDATE 只列出真正要改的列是这里的安全边界。
func (r *agentKeyRepository) UpdateName(ctx context.Context, id uint64, name string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE agent_keys SET name = ? WHERE id = ?", strings.TrimSpace(name), id)
	if err != nil {
		return fmt.Errorf("store: 更新 agent 密钥备注失败: %w", err)
	}
	if n, nErr := res.RowsAffected(); nErr == nil && n == 0 {
		return model.ErrAgentKeyNotFound
	}
	return nil
}

// Delete 永久删除一把密钥。
func (r *agentKeyRepository) Delete(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM agent_keys WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: 删除 agent 密钥失败: %w", err)
	}
	if n, nErr := res.RowsAffected(); nErr == nil && n == 0 {
		return model.ErrAgentKeyNotFound
	}
	return nil
}
