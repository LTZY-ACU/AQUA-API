// 本文件是 model.ChannelKeyRepository 接口的 SQL 实现。
//
// 意图（Why）：
//
//	把渠道密钥池落到数据库，并承担三件事：
//	  1) 密钥加解密（明文只在内存，落库一律密文）；
//	  2) 用摘要（key_hash）做去重与存在性判断，避免解密整池密钥只为比对；
//	  3) 记录每把密钥的连续失败次数与状态，支撑"失效自动摘除"。
//
// 流转（Flow）：
//
//	main.go → NewChannelKeyRepository(db, cipher)
//	  ├─ 后台导入：ReplaceAll（事务内做差集增删）
//	  ├─ 转发路径：ListUsable → PickKey → MarkUsed / MarkSuccess / MarkFailure
//	  └─ 列表页：Summary（一次查询拿全部渠道的计数，避免 N+1）
//
// 扩展（Extend）：
//
//	新增密钥字段：先建迁移加列，再同步本文件的 channelKeyColumns / scanChannelKey /
//	insert 三处（列顺序必须与扫描顺序一致，否则会静默错位）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/crypto"
	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// channelKeyColumns 集中定义查询列，顺序必须与 scanChannelKey 的扫描顺序严格一致。
const channelKeyColumns = `id, channel_id, kind, key_enc, label, status, fail_count, last_used_at, last_error, created_at, ` +
	`refresh_token_enc, access_token_enc, expires_at, account_hint, provider, ` +
	`weight, priority, in_flight, cooldown_until, rpm_limit, window_start, window_count, ` +
	`balance, balance_updated_at, ` +
	`account_id, plan_type, quota_used_percent, quota_reset_at, quota_checked_at, ` +
	`quota_secondary_used_percent, quota_secondary_reset_at, quota_primary_window_seconds, quota_secondary_window_seconds, ` +
	`group_names, models`

// channelKeyRepository 是 model.ChannelKeyRepository 的 SQL 实现，并发安全。
type channelKeyRepository struct {
	db     *sql.DB
	cipher *crypto.Cipher
}

// NewChannelKeyRepository 创建渠道密钥池仓储。
//
// 参数 cipher 用于密钥加解密，不可为 nil —— 没有加密能力就不应允许写入密钥。
func NewChannelKeyRepository(db *sql.DB, cipher *crypto.Cipher) model.ChannelKeyRepository {
	return &channelKeyRepository{db: db, cipher: cipher}
}

// ReplaceAll 用给定 API Key 集合替换渠道的 api_key 类型凭据。
//
// 实现委托给 ReplaceCredentials：两类凭据的差集增删逻辑完全一致，
// 只是类型不同。这样避免同一套逻辑存在两份、日后改一处漏一处。
func (r *channelKeyRepository) ReplaceAll(ctx context.Context, channelID uint64, keys, labels []string) (int, int, error) {
	inputs := make([]model.CredentialInput, 0, len(keys))
	for i, key := range keys {
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		inputs = append(inputs, model.CredentialInput{
			Kind:   model.CredentialKindAPIKey,
			APIKey: key,
			Label:  label,
			// 显式写"未知"而不是依赖零值：CredentialInput.Balance 的零值是 0，
			// 而 0 表示"已用尽"，留零会让所有经此入口导入的密钥被误判为余额耗尽。
			Balance: model.BalanceUnknown,
		})
	}
	return r.ReplaceCredentials(ctx, channelID, inputs)
}

// ReplaceAllWithBalance 用给定 API Key 集合替换渠道密钥池，并录入每把密钥的余额。
//
// 与 ReplaceAll 的唯一差别是携带 balances；其余（差集增删、保留已有统计与余额）完全一致。
// balances 为 nil 或长度不足时，对应密钥余额取 BalanceUnknown（未录入）。
func (r *channelKeyRepository) ReplaceAllWithBalance(ctx context.Context, channelID uint64, keys, labels []string, balances []int64) (int, int, error) {
	inputs := make([]model.CredentialInput, 0, len(keys))
	for i, key := range keys {
		label := ""
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		balance := model.BalanceUnknown
		if i < len(balances) {
			balance = balances[i]
		}
		inputs = append(inputs, model.CredentialInput{
			Kind:    model.CredentialKindAPIKey,
			APIKey:  key,
			Label:   label,
			Balance: balance,
		})
	}
	return r.ReplaceCredentials(ctx, channelID, inputs)
}

// ReplaceCredentials 用给定凭据集合整体替换渠道的凭据池（差集增删，幂等）。
//
// 实现要点：
//  1. 所有增删在【单个事务】内完成。若不加事务，删一半失败会留下
//     "旧凭据已删、新凭据没进来"的空池状态，渠道会瞬间不可用；
//  2. 只增删「输入中出现的类型」的凭据，其他类型保持不变。
//     例如导入 API Key 时不会误删该渠道已有的 OAuth 订阅账号；
//     反之亦然。若一股脑全删，管理员只想补一批 Key 却把订阅账号清空，
//     会造成难以察觉的线上故障；
//  3. 去重标识统一存在 key_hash 列：api_key 用密钥摘要，
//     oauth 用 refresh_token 摘要（它才是账号的长期唯一标识）。
func (r *channelKeyRepository) ReplaceCredentials(ctx context.Context, channelID uint64, inputs []model.CredentialInput) (int, int, error) {
	if channelID == 0 {
		return 0, 0, errors.New("store: 替换凭据池时渠道 ID 不能为 0")
	}

	desired := make(map[string]model.CredentialInput, len(inputs))
	// ordered 保留管理员提交的原始顺序：下面的新增必须按它落库，
	// 新密钥的 id 升序才会与"粘贴顺序"一致。若直接 range desired（map），
	// 插入顺序每次随机，sequential 策略与 ORDER BY id ASC 的语义会不可预期，
	// 也会让"第几把密钥"这类排障判断失去依据。
	ordered := make([]model.CredentialInput, 0, len(inputs))
	touchedKinds := make(map[model.CredentialKind]struct{}, 2)
	for _, input := range inputs {
		if err := input.Validate(); err != nil {
			return 0, 0, fmt.Errorf("store: 凭据非法: %w", err)
		}
		touchedKinds[input.Kind] = struct{}{}
		hash := input.IdentityHash(crypto.SHA256Hex)
		if _, dup := desired[hash]; dup {
			continue
		}
		desired[hash] = input
		ordered = append(ordered, input)
	}

	existing, err := r.listHashByKind(ctx, channelID)
	if err != nil {
		return 0, 0, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: 开启凭据池事务失败: %w", err)
	}
	// 失败时回滚；成功后 Commit，此调用变为无操作
	defer func() { _ = tx.Rollback() }()

	// 1) 删除：仅限"本次涉及的类型"中不再出现的凭据
	removed := 0
	for kind := range touchedKinds {
		for hash := range existing[kind] {
			if _, keep := desired[hash]; keep {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM channel_keys WHERE channel_id = ? AND kind = ? AND key_hash = ?",
				channelID, string(kind), hash); err != nil {
				return 0, 0, fmt.Errorf("store: 删除陈旧凭据失败: %w", err)
			}
			removed++
		}
	}

	// 2) 按输入顺序新增目标集合中缺失的凭据；已存在的凭据保留状态与统计（并保护已录余额）
	now := time.Now().Unix()
	added := 0
	for _, input := range ordered {
		hash := input.IdentityHash(crypto.SHA256Hex)
		if _, ok := existing[input.Kind][hash]; ok {
			// 已存在：保留其状态与失败统计，不重置。
			//
			// 余额的额外保护：重新粘贴同一批密钥时，绝不把已人工录入的余额
			// 覆盖为"未知"——只有本次【显式提供了已知余额（>=0）】才更新为本次的值。
			// 这样"补一批密钥"或"原样重贴"都不会抹掉站长辛苦录入的余额。
			if input.Balance >= 0 {
				if _, err := tx.ExecContext(ctx, `
					UPDATE channel_keys SET balance = ?, balance_updated_at = ?
					WHERE channel_id = ? AND kind = ? AND key_hash = ?`,
					input.Balance, now, channelID, string(input.Kind), hash); err != nil {
					return 0, 0, fmt.Errorf("store: 更新已有凭据余额失败: %w", err)
				}
			}
			continue
		}

		keyEnc, err := r.encryptField(input.APIKey)
		if err != nil {
			return 0, 0, err
		}
		refreshEnc, err := r.encryptField(input.RefreshToken)
		if err != nil {
			return 0, 0, err
		}
		accessEnc, err := r.encryptField(input.AccessToken)
		if err != nil {
			return 0, 0, err
		}

		// 余额与更新时间：未知（负值一律归一为 BalanceUnknown）记 0 时间；
		// 已知余额则把本次视为"录入时刻"。
		balance := input.Balance
		if balance < 0 {
			balance = model.BalanceUnknown
		}
		balanceUpdatedAt := int64(0)
		if balance >= 0 {
			balanceUpdatedAt = now
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_keys
				(channel_id, kind, key_enc, key_hash, label, status, fail_count, last_used_at, last_error,
				 created_at, refresh_token_enc, access_token_enc, expires_at, account_hint, provider,
				 balance, balance_updated_at, account_id, plan_type, quota_used_percent,
				 quota_secondary_used_percent, quota_secondary_reset_at,
				 quota_primary_window_seconds, quota_secondary_window_seconds)
			VALUES (?, ?, ?, ?, ?, ?, 0, 0, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			channelID, string(input.Kind), keyEnc, hash, strings.TrimSpace(input.Label),
			int(model.ChannelKeyStatusEnabled), now,
			refreshEnc, accessEnc, unixOrZero(input.ExpiresAt),
			strings.TrimSpace(input.AccountHint), strings.TrimSpace(input.Provider),
			balance, balanceUpdatedAt,
			strings.TrimSpace(input.AccountID), strings.TrimSpace(input.PlanType),
			// 新导入的凭据额度一律"未探测"：让探测任务去填，
			// 而不是把 0（= 完全没用）当成事实写进去。
			// 两个窗口同一口径；窗口秒数未知记 0，由探测回填。
			model.QuotaUsedPercentUnknown, model.QuotaUsedPercentUnknown, 0, 0, 0); err != nil {
			return 0, 0, fmt.Errorf("store: 新增凭据失败: %w", err)
		}
		added++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("store: 提交凭据池变更失败: %w", err)
	}
	return added, removed, nil
}

// UpdateTokens 回写刷新后的 OAuth 令牌。
//
// 三个细节：
//  1. refreshToken 为空时保留原值——部分平台刷新后不回传新 refresh_token，
//     若按空值写入，等于永久丢失该账号；
//  2. 刷新成功即把失败计数清零：能刷出令牌说明该账号本身是好的，
//     之前的失败多半是令牌过期所致，不该继续累计；
//  3. 空 accessToken 视为非法（调用方应当只在刷新成功时回写）。
func (r *channelKeyRepository) UpdateTokens(ctx context.Context, id uint64, accessToken string, expiresAt time.Time, refreshToken string) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("store: 回写的 access_token 不能为空")
	}

	accessEnc, err := r.encryptField(accessToken)
	if err != nil {
		return err
	}

	var res sql.Result
	if strings.TrimSpace(refreshToken) != "" {
		refreshEnc, encErr := r.encryptField(refreshToken)
		if encErr != nil {
			return encErr
		}
		res, err = r.db.ExecContext(ctx, `
			UPDATE channel_keys SET
				access_token_enc = ?, expires_at = ?, refresh_token_enc = ?,
				fail_count = 0, last_error = ''
			WHERE id = ?`,
			accessEnc, unixOrZero(expiresAt), refreshEnc, id)
	} else {
		res, err = r.db.ExecContext(ctx, `
			UPDATE channel_keys SET
				access_token_enc = ?, expires_at = ?,
				fail_count = 0, last_error = ''
			WHERE id = ?`,
			accessEnc, unixOrZero(expiresAt), id)
	}
	if err != nil {
		return fmt.Errorf("store: 回写令牌失败: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// encryptField 加密单个字段；空值返回空串（数据库里"未使用"就是空串）。
func (r *channelKeyRepository) encryptField(plain string) (string, error) {
	if strings.TrimSpace(plain) == "" {
		return "", nil
	}
	encrypted, err := r.cipher.Encrypt(plain)
	if err != nil {
		return "", fmt.Errorf("store: 加密凭据字段失败: %w", err)
	}
	return encrypted, nil
}

// listHashByKind 返回某渠道已有凭据的摘要集合，按类型分组。
//
// 分组的必要性：替换时要"只动本次涉及的类型"，
// 因此必须先知道每一类里已有哪些凭据。
func (r *channelKeyRepository) listHashByKind(ctx context.Context, channelID uint64) (map[model.CredentialKind]map[string]struct{}, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT kind, key_hash FROM channel_keys WHERE channel_id = ?", channelID)
	if err != nil {
		return nil, fmt.Errorf("store: 查询已有凭据摘要失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[model.CredentialKind]map[string]struct{}, 2)
	for rows.Next() {
		var (
			kind string
			hash string
		)
		if err := rows.Scan(&kind, &hash); err != nil {
			return nil, fmt.Errorf("store: 读取凭据摘要失败: %w", err)
		}
		credentialKind := model.CredentialKind(kind)
		if !credentialKind.IsValid() {
			credentialKind = model.CredentialKindAPIKey
		}
		if result[credentialKind] == nil {
			result[credentialKind] = make(map[string]struct{})
		}
		result[credentialKind][hash] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历凭据摘要失败: %w", err)
	}
	return result, nil
}

// ListByChannel 列出某渠道的全部密钥（含已禁用与已摘除）。
func (r *channelKeyRepository) ListByChannel(ctx context.Context, channelID uint64) ([]*model.ChannelKey, error) {
	return r.list(ctx,
		"SELECT "+channelKeyColumns+" FROM channel_keys WHERE channel_id = ? ORDER BY id ASC",
		channelID)
}

// ListUsable 列出某渠道中可参与轮询的密钥。
func (r *channelKeyRepository) ListUsable(ctx context.Context, channelID uint64) ([]*model.ChannelKey, error) {
	return r.list(ctx,
		"SELECT "+channelKeyColumns+" FROM channel_keys WHERE channel_id = ? AND status = ? ORDER BY id ASC",
		channelID, int(model.ChannelKeyStatusEnabled))
}

// list 是列表查询的公共实现（两个入口只差 WHERE 条件）。
func (r *channelKeyRepository) list(ctx context.Context, query string, args ...any) ([]*model.ChannelKey, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询密钥列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := make([]*model.ChannelKey, 0, 32)
	for rows.Next() {
		key, err := r.scanChannelKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历密钥列表失败: %w", err)
	}
	return keys, nil
}

// Summary 批量统计多个渠道的密钥池概览。
//
// 实现方式：一条 GROUP BY 查询取回全部渠道的计数，再在内存里归类。
// 这样渠道列表页（可能几十个渠道）只需一次数据库往返。
func (r *channelKeyRepository) Summary(ctx context.Context, channelIDs []uint64) (map[uint64]model.KeyPoolSummary, error) {
	result := make(map[uint64]model.KeyPoolSummary, len(channelIDs))
	if len(channelIDs) == 0 {
		return result, nil
	}

	// 用 IN 查询限定范围；渠道数量受分页上限约束（<=1000），不会触到 SQLite 参数上限
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(channelIDs)), ",")
	args := make([]any, 0, len(channelIDs))
	for _, id := range channelIDs {
		args = append(args, id)
	}

	query := "SELECT channel_id, status, COUNT(1) FROM channel_keys WHERE channel_id IN (" +
		placeholders + ") GROUP BY channel_id, status"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 统计密钥池失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			channelID uint64
			status    int
			count     int
		)
		if err := rows.Scan(&channelID, &status, &count); err != nil {
			return nil, fmt.Errorf("store: 读取密钥池统计失败: %w", err)
		}
		summary := result[channelID]
		summary.ChannelID = channelID
		summary.Total += count
		switch model.ChannelKeyStatus(status) {
		case model.ChannelKeyStatusEnabled:
			summary.Enabled += count
		case model.ChannelKeyStatusDisabled:
			summary.Disabled += count
		case model.ChannelKeyStatusAutoRemoved:
			summary.AutoRemoved += count
		}
		result[channelID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历密钥池统计失败: %w", err)
	}
	return result, nil
}

// DeleteByChannel 删除某渠道的全部密钥（渠道删除时级联清理）。
func (r *channelKeyRepository) DeleteByChannel(ctx context.Context, channelID uint64) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM channel_keys WHERE channel_id = ?", channelID); err != nil {
		return fmt.Errorf("store: 删除渠道 %d 的密钥池失败: %w", channelID, err)
	}
	return nil
}

// MarkUsed 记录密钥被选中使用的时间。
func (r *channelKeyRepository) MarkUsed(ctx context.Context, id uint64, at time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET last_used_at = ? WHERE id = ?", at.Unix(), id); err != nil {
		return fmt.Errorf("store: 记录密钥使用时间失败: %w", err)
	}
	return nil
}

// MarkSuccess 记录一次成功：清零连续失败计数并解除冷却。
//
// 注意这里【不】改状态：被手动禁用的密钥不应因一次"恰好被选中并成功"而启用
// （实际上它根本不会被选中）。保持状态与统计分离，语义更清晰。
//
// 冷却必须一并清除：借这次成功已经证明"这把钥匙当前是好的"，
// 再保留冷却只会让它在冷却期内被白白排除在池外。
func (r *channelKeyRepository) MarkSuccess(ctx context.Context, id uint64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET fail_count = 0, last_error = '', cooldown_until = 0 WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: 重置密钥失败计数失败: %w", err)
	}
	return nil
}

// MarkFailure 记录一次失败：累加连续失败计数，达到阈值时自动摘除。
//
// 用 SQL 表达式在一条语句内完成"自增 + 判断阈值 + 改状态"，
// 避免"先读再判断再写"在并发下的计数丢失。
func (r *channelKeyRepository) MarkFailure(ctx context.Context, id uint64, reason string) error {
	// 截断错误描述：上游可能返回很长的响应体，直接入库会撑大数据库
	if len(reason) > 200 {
		reason = reason[:200]
	}

	if _, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET
			fail_count = fail_count + 1,
			last_error = ?,
			status = CASE WHEN fail_count + 1 >= ? THEN ? ELSE status END
		WHERE id = ?`,
		reason, model.KeyAutoRemoveThreshold, int(model.ChannelKeyStatusAutoRemoved), id); err != nil {
		return fmt.Errorf("store: 记录密钥失败失败: %w", err)
	}
	return nil
}

// UpdateStatus 手动修改密钥状态。
//
// 典型用法：把误杀（连续失败被自动摘除）的密钥重新启用，
// 或临时禁用一个正在被上游限流的密钥。
func (r *channelKeyRepository) UpdateStatus(ctx context.Context, id uint64, status model.ChannelKeyStatus) error {
	if !status.IsValid() {
		return fmt.Errorf("store: 密钥状态非法: %d", int(status))
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET status = ? WHERE id = ?", int(status), id)
	if err != nil {
		return fmt.Errorf("store: 更新密钥状态失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// ChannelKeyStrategy 读取渠道的凭据调度策略与轮询游标。
//
// 渠道不存在时返回默认策略与游标 0：策略属于"优化项"而非"必需项"，
// 读不到就退化为默认，避免因一条渠道记录缺失阻断整条转发链路。
func (r *channelKeyRepository) ChannelKeyStrategy(ctx context.Context, channelID uint64) (model.KeyStrategy, int64, error) {
	var (
		raw    string
		cursor int64
	)
	err := r.db.QueryRowContext(ctx,
		"SELECT key_strategy, key_cursor FROM channels WHERE id = ?", channelID).Scan(&raw, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DefaultKeyStrategy(), 0, nil
	}
	if err != nil {
		return model.DefaultKeyStrategy(), 0, fmt.Errorf("store: 读取渠道 %d 的凭据策略失败: %w", channelID, err)
	}
	return model.NormalizeKeyStrategy(raw), cursor, nil
}

// SetChannelStrategy 设置渠道的凭据调度策略。
func (r *channelKeyRepository) SetChannelStrategy(ctx context.Context, channelID uint64, strategy model.KeyStrategy) error {
	if !strategy.IsValid() {
		return fmt.Errorf("store: 凭据调度策略非法: %q", string(strategy))
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE channels SET key_strategy = ? WHERE id = ?", string(strategy), channelID)
	if err != nil {
		return fmt.Errorf("store: 更新渠道 %d 的凭据策略失败: %w", channelID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelNotFound
	}
	return nil
}

// AdvanceKeyCursor 持久化轮询游标。
func (r *channelKeyRepository) AdvanceKeyCursor(ctx context.Context, channelID uint64, cursor int64) error {
	if cursor < 0 {
		cursor = 0
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE channels SET key_cursor = ? WHERE id = ?", cursor, channelID)
	if err != nil {
		return fmt.Errorf("store: 更新渠道 %d 的轮询游标失败: %w", channelID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelNotFound
	}
	return nil
}

// Acquire 记录一次在途占用（in_flight + 1）。
//
// 用 SQL 自增而非"先读后写"，避免并发下计数丢失。
func (r *channelKeyRepository) Acquire(ctx context.Context, id uint64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET in_flight = in_flight + 1 WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: 增加凭据 %d 的在途计数失败: %w", id, err)
	}
	return nil
}

// Release 释放在途占用（in_flight - 1）。
//
// 关键点：计数【不会降到 0 以下】。进程崩溃后 in_flight 可能是残留值，
// 而调用方在重试/异常分支上可能重复释放；若简单自减会出现负数，
// 进而让 least_in_flight 策略长期偏好这把凭据（负数最小），造成流量倾斜。
func (r *channelKeyRepository) Release(ctx context.Context, id uint64) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET in_flight = CASE WHEN in_flight > 0 THEN in_flight - 1 ELSE 0 END WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: 减少凭据 %d 的在途计数失败: %w", id, err)
	}
	return nil
}

// SetCooldown 设置冷却截止时间与失败原因，并累加连续失败计数。
//
// until 为零值时仅清除冷却而不计失败（用于人工/成功路径）。
func (r *channelKeyRepository) SetCooldown(ctx context.Context, id uint64, until time.Time, reason string) error {
	if len(reason) > 200 {
		reason = reason[:200]
	}

	var res sql.Result
	var err error
	if until.IsZero() {
		res, err = r.db.ExecContext(ctx,
			"UPDATE channel_keys SET cooldown_until = 0, last_error = ? WHERE id = ?", reason, id)
	} else {
		res, err = r.db.ExecContext(ctx, `
			UPDATE channel_keys SET
				cooldown_until = ?, fail_count = fail_count + 1, last_error = ?
			WHERE id = ?`,
			until.Unix(), reason, id)
	}
	if err != nil {
		return fmt.Errorf("store: 设置凭据 %d 冷却失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// MarkPermanentFailure 把凭据置为自动摘除状态（永久语义）。
//
// 与 SetCooldown 的区别：冷却会到期自动恢复，摘除不会。
// 仅在上游明确表示该凭据永久无效（如密钥被吊销）时调用。
func (r *channelKeyRepository) MarkPermanentFailure(ctx context.Context, id uint64, reason string) error {
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if _, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET
			status = ?, fail_count = fail_count + 1, last_error = ?
		WHERE id = ?`,
		int(model.ChannelKeyStatusAutoRemoved), reason, id); err != nil {
		return fmt.Errorf("store: 摘除凭据 %d 失败: %w", id, err)
	}
	return nil
}

// RecordRequest 记录一次请求以统计 RPM 固定窗口。
//
// 窗口语义：window_start 起算，超过 window 即开新窗口（计数重置为 1），
// 否则窗口内计数自增。用固定窗口而非滑动窗口，是为了能用一条 UPDATE
// 完成"判断 + 重置 + 自增"，避免并发下的计数错乱。
func (r *channelKeyRepository) RecordRequest(ctx context.Context, id uint64, at time.Time, window time.Duration) error {
	seconds := int64(window.Seconds())
	if seconds <= 0 {
		seconds = 60
	}
	now := at.Unix()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET
			window_count = CASE
				WHEN window_start = 0 OR ? - window_start >= ? THEN 1
				ELSE window_count + 1 END,
			window_start = CASE
				WHEN window_start = 0 OR ? - window_start >= ? THEN ?
				ELSE window_start END
		WHERE id = ?`,
		now, seconds, now, seconds, now, id); err != nil {
		return fmt.Errorf("store: 记录凭据 %d 的请求计数失败: %w", id, err)
	}
	return nil
}

// UpdateScheduling 更新凭据的调度参数。
//
// 负值一律归一为 0（weight 为 0 在加权随机里等价于"不选它"，语义自洽）。
func (r *channelKeyRepository) UpdateScheduling(ctx context.Context, id uint64, weight, priority, rpmLimit int) error {
	if weight < 0 {
		weight = 0
	}
	if priority < 0 {
		priority = 0
	}
	if rpmLimit < 0 {
		rpmLimit = 0
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET weight = ?, priority = ?, rpm_limit = ? WHERE id = ?`,
		weight, priority, rpmLimit, id)
	if err != nil {
		return fmt.Errorf("store: 更新凭据 %d 的调度参数失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// UpdateRouting 更新凭据可服务的分组与模型（迁移 0038）。
//
// 空切片一律落成空串（= 不限），因此"清空分组/模型限制"是一个明确且可完成的动作。
//
// 为什么整组替换：后台按"一把凭据一个表单"编辑，整组替换保证界面所见即落库结果，
// 也不会因两次并发编辑拼出一个谁都没提交过的组合。
func (r *channelKeyRepository) UpdateRouting(ctx context.Context, id uint64, groups, models []string) error {
	// 复用 channel_repo.go 的 encodeModels：去空白、去重、CSV，
	// 与渠道级 group_names/models 的落库格式完全一致（读取侧也用同一解码函数）。
	res, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET group_names = ?, models = ? WHERE id = ?",
		encodeModels(groups), encodeModels(models), id)
	if err != nil {
		return fmt.Errorf("store: 更新凭据 %d 的分组/模型失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// UpdateBalance 人工更新凭据余额，并记录更新时间。
//
// balance 允许 BalanceUnknown(-1)（置回"未录入"）与任意 >=0 的整数；
// 小于 -1 的值无意义（余额不可能比"未知"更负），直接拒绝而不是静默修正，
// 免得把一次前端 bug 变成一个看不出异常的数字。
//
// 同时刷新 balance_updated_at：即便数值没变，也把"这次核对来自何时"记下来，
// 方便后台展示这个余额数字的新鲜度。
func (r *channelKeyRepository) UpdateBalance(ctx context.Context, id uint64, balance int64) error {
	if balance < model.BalanceUnknown {
		return fmt.Errorf("store: 凭据余额非法: %d（最小为 %d，表示未知）", balance, model.BalanceUnknown)
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE channel_keys SET balance = ?, balance_updated_at = ? WHERE id = ?",
		balance, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: 更新凭据 %d 的余额失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// UpdateAccountMeta 写入订阅账号的账号级元数据（account_id / plan_type）。
//
// 空值语义：空串表示"本次没有新信息"，对应的列保持原值不变。
// 这与"显式清空"不同——刷新响应里常常不回传 plan_type，若无条件写入会把
// 原本已知的套餐抹成空，反而丢信息。
func (r *channelKeyRepository) UpdateAccountMeta(ctx context.Context, id uint64, accountID, planType string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET
			account_id = CASE WHEN ? = '' THEN account_id ELSE ? END,
			plan_type  = CASE WHEN ? = '' THEN plan_type  ELSE ? END
		WHERE id = ?`,
		strings.TrimSpace(accountID), strings.TrimSpace(accountID),
		strings.TrimSpace(planType), strings.TrimSpace(planType), id)
	if err != nil {
		return fmt.Errorf("store: 更新凭据 %d 的账号元数据失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// UpdateQuota 写入额度探测结果（主 / 次两个窗口一次性落库）。
//
// 某个窗口的已用百分比传 model.QuotaUsedPercentUnknown 表示"该窗口置回未知"，
// 此时把快照置回未知（并刷新 checked_at），避免界面继续展示一个越来越过时的旧值。
//
// resetAt 为零值时保留原重置时间？——不保留：额度窗口是会变的，
// 保留旧的"重置时间"会让 QuotaExhausted 依据过期数据做判断。因此一并写入本次探测值。
//
// 两个窗口在同一条 UPDATE 内完成：它们来自同一次探测，拆成两条语句会在中间
// 留下"主窗口已更新、次窗口还是旧值"的不一致快照。
func (r *channelKeyRepository) UpdateQuota(ctx context.Context, id uint64, windows model.QuotaWindows) error {
	primary := windows.PrimaryUsedPercent
	if primary < model.QuotaUsedPercentUnknown {
		primary = model.QuotaUsedPercentUnknown
	}
	secondary := windows.SecondaryUsedPercent
	if secondary < model.QuotaUsedPercentUnknown {
		secondary = model.QuotaUsedPercentUnknown
	}
	checkedAt := windows.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE channel_keys SET
			quota_used_percent = ?,
			quota_reset_at = ?,
			quota_checked_at = ?,
			quota_secondary_used_percent = ?,
			quota_secondary_reset_at = ?,
			quota_primary_window_seconds = ?,
			quota_secondary_window_seconds = ?
		WHERE id = ?`,
		primary, unixOrZero(windows.PrimaryResetAt), checkedAt.Unix(),
		secondary, unixOrZero(windows.SecondaryResetAt),
		normalizeWindowSeconds(windows.PrimaryWindowSeconds),
		normalizeWindowSeconds(windows.SecondaryWindowSeconds),
		id)
	if err != nil {
		return fmt.Errorf("store: 更新凭据 %d 的额度快照失败: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelKeyNotFound
	}
	return nil
}

// normalizeWindowSeconds 把窗口时长收敛为非负数；负数（上游异常值）归零表示"未提供"。
//
// 归零而非夹到某个正数：0 在界面上有明确定义（退回默认文案），
// 而一个凭空捏造的正数会被当成"上游真的这么说的"，反而更误导。
func normalizeWindowSeconds(seconds int) int {
	if seconds < 0 {
		return 0
	}
	return seconds
}

// scanChannelKey 把一行数据映射为凭据对象，并完成解密。
func (r *channelKeyRepository) scanChannelKey(sc rowScanner) (*model.ChannelKey, error) {
	var (
		id           uint64
		channelID    uint64
		kind         string
		encryptedKey string
		label        string
		status       int
		failCount    int
		lastUsedAt   int64
		lastError    string
		createdAt    int64
		refreshEnc   string
		accessEnc    string
		expiresAt    int64
		accountHint  string
		provider     string
		weight       int
		priority     int
		inFlight     int
		cooldownEnd  int64
		rpmLimit     int
		windowStart  int64
		windowCount  int
		balance      int64
		balanceAt    int64
		accountID    string
		planType     string
		quotaUsed    int
		quotaReset   int64
		quotaChecked int64
		// 次额度窗口与窗口时长（迁移 0048）
		quotaSecondaryUsed    int
		quotaSecondaryReset   int64
		quotaPrimaryWindowSec int
		quotaSecondaryWinSec  int
		// 凭据级路由分叉（迁移 0038）：可服务的分组与模型（CSV，空 = 不限）
		groupNames string
		modelsCSV  string
	)

	if err := sc.Scan(&id, &channelID, &kind, &encryptedKey, &label, &status,
		&failCount, &lastUsedAt, &lastError, &createdAt,
		&refreshEnc, &accessEnc, &expiresAt, &accountHint, &provider,
		&weight, &priority, &inFlight, &cooldownEnd, &rpmLimit, &windowStart, &windowCount,
		&balance, &balanceAt,
		&accountID, &planType, &quotaUsed, &quotaReset, &quotaChecked,
		&quotaSecondaryUsed, &quotaSecondaryReset, &quotaPrimaryWindowSec, &quotaSecondaryWinSec,
		&groupNames, &modelsCSV); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取凭据字段失败: %w", err)
	}

	credentialKind := model.CredentialKind(kind)
	if !credentialKind.IsValid() {
		// 数据库里出现未知类型：不阻断读取（管理员仍应能在界面上看到这条异常数据），
		// 但按 api_key 处理并保留原始 kind 字符串用于排查
		credentialKind = model.CredentialKindAPIKey
	}

	plainKey, err := r.decryptField(encryptedKey, id, "密钥")
	if err != nil {
		return nil, err
	}
	refreshToken, err := r.decryptField(refreshEnc, id, "refresh_token")
	if err != nil {
		return nil, err
	}
	accessToken, err := r.decryptField(accessEnc, id, "access_token")
	if err != nil {
		return nil, err
	}

	return &model.ChannelKey{
		ID:           id,
		ChannelID:    channelID,
		Kind:         credentialKind,
		Key:          plainKey,
		Label:        label,
		Status:       model.ChannelKeyStatus(status),
		FailCount:    failCount,
		RefreshToken: refreshToken,
		AccessToken:  accessToken,
		ExpiresAt:    unixToExpiresAt(expiresAt),
		AccountHint:  accountHint,
		Provider:     provider,
		LastUsedAt:   unixToExpiresAt(lastUsedAt),
		LastError:    lastError,
		CreatedAt:    time.Unix(createdAt, 0),

		Weight:        weight,
		Priority:      priority,
		InFlight:      inFlight,
		CooldownUntil: unixToExpiresAt(cooldownEnd),
		RPMLimit:      rpmLimit,
		WindowStart:   unixToExpiresAt(windowStart),
		WindowCount:   windowCount,

		Balance:          balance,
		BalanceUpdatedAt: unixToExpiresAt(balanceAt),

		AccountID:        accountID,
		PlanType:         planType,
		QuotaUsedPercent: quotaUsed,
		QuotaResetAt:     unixToExpiresAt(quotaReset),
		QuotaCheckedAt:   unixToExpiresAt(quotaChecked),

		QuotaSecondaryUsedPercent:   quotaSecondaryUsed,
		QuotaSecondaryResetAt:       unixToExpiresAt(quotaSecondaryReset),
		QuotaPrimaryWindowSeconds:   quotaPrimaryWindowSec,
		QuotaSecondaryWindowSeconds: quotaSecondaryWinSec,

		// 路由分叉：与渠道级同格式（CSV），解码规则一致故复用 encodeModels/decodeModels。
		Groups: decodeModels(groupNames),
		Models: decodeModels(modelsCSV),
	}, nil
}

// decryptField 解密单个字段。
//
// 空字符串直接返回空：数据库里未使用的字段是空串，对它调用解密会报错，
// 而"空"本身是合法状态（例如 api_key 类型的凭据没有 refresh_token）。
func (r *channelKeyRepository) decryptField(encoded string, id uint64, fieldName string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	plain, err := r.cipher.Decrypt(encoded)
	if err != nil {
		return "", fmt.Errorf("store: 解密凭据 %d 的 %s 失败（AQUA_APP_KEY 是否变更？）: %w", id, fieldName, err)
	}
	return plain, nil
}
