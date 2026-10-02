// 本文件是 model.ChannelRepository 接口的 SQL 实现。
//
// 意图（Why）：
//
//	把渠道数据真正落到 SQLite（后续可扩展 PostgreSQL）。此层的核心职责有三：
//	  1) 屏蔽 SQL 细节，让业务代码只面对领域模型；
//	  2) 承担【密钥加解密】——领域模型里是明文，落库必须是密文；
//	  3) 把数据库错误翻译成领域错误（如 ErrChannelNotFound）。
//
// 流转（Flow）：
//
//	main.go 装配 → NewChannelRepository(db, cipher)
//	  └─ server / relay 调用接口方法
//	       ├─ Create/Update：Validate 校验 → cipher.Encrypt 加密 → 写库
//	       └─ GetByID/List ：读库 → cipher.Decrypt 解密 → 返回领域对象
//
// 扩展（Extend）：
//
//	新增查询条件：在 model.ChannelQuery 加字段 → 在 List 中拼 WHERE 与参数（务必用占位符，
//	             禁止字符串拼接用户输入，避免 SQL 注入）。
//	新增字段：同步改 schema.sql（追加迁移）+ 本文件的 channelColumns / scanChannel / insert / update。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitee.com/xiaosu4610/aqua-api/internal/crypto"
	"gitee.com/xiaosu4610/aqua-api/internal/model"
)

// 列表查询的条数约束。
//
// 为什么要设上限：防止调用方传 0 或超大值导致一次性拉全表，
// 在渠道数量增长后会成为内存与慢查询风险。
const (
	defaultChannelListLimit = 100  // Limit 未指定时的默认条数
	maxChannelListLimit     = 1000 // 单次查询允许的最大条数
)

// channelColumns 集中定义查询列，避免各处手写列名导致顺序错乱。
//
// 注意：列顺序必须与 scanChannel 的 Scan 参数顺序严格一致。
const channelColumns = `id, name, type, type_key, extra_config, base_url, api_key_enc, models, group_name, group_names, priority, weight, status, created_at, updated_at, last_test_at, last_test_ok, latency_ms, last_test_code, last_test_model, key_strategy, key_failure_policy, key_cooldown_seconds, retry_enabled, retry_max_attempts, model_retry_rules`

// channelRepository 是 model.ChannelRepository 的 SQL 实现。
//
// 并发安全：内部只持有 *sql.DB（自带连接池）与 *crypto.Cipher（无状态），可被多 goroutine 共享。
type channelRepository struct {
	db     *sql.DB
	cipher *crypto.Cipher
}

// NewChannelRepository 创建渠道仓储。
//
// 参数 cipher 用于密钥加解密，不可为 nil —— 没有加密能力就不应该允许写入密钥。
func NewChannelRepository(db *sql.DB, cipher *crypto.Cipher) model.ChannelRepository {
	return &channelRepository{db: db, cipher: cipher}
}

// Create 新增渠道并回填数据库生成的 ID 与时间戳。
func (r *channelRepository) Create(ctx context.Context, ch *model.Channel) error {
	// 领域校验前置：保证任何入口写入的数据都符合规则
	if err := ch.Validate(); err != nil {
		return fmt.Errorf("store: 渠道数据非法: %w", err)
	}

	// 密钥加密：领域模型持有明文，落库一律转为密文
	encryptedKey, err := r.cipher.Encrypt(ch.APIKey)
	if err != nil {
		return fmt.Errorf("store: 加密渠道密钥失败: %w", err)
	}

	now := time.Now()
	ch.CreatedAt = now
	ch.UpdatedAt = now

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO channels
			(name, type, type_key, extra_config, base_url, api_key_enc, models, group_name, group_names, priority, weight, status, created_at, updated_at, key_strategy, key_failure_policy, key_cooldown_seconds, retry_enabled, retry_max_attempts, model_retry_rules)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Name, ch.Type, ch.TypeKey, encodeExtraConfig(ch.ExtraConfig), ch.BaseURL, encryptedKey, encodeModels(ch.Models),
		// 主分组与分组清单一并写入：group_name 用于展示与统计，group_names 是路由匹配依据。
		// encodeModels 的行为（去空白、去重、CSV）与分组清单所需完全一致，故直接复用。
		ch.Group, encodeModels(ch.Groups), ch.Priority, ch.Weight, int(ch.Status),
		ch.CreatedAt.Unix(), ch.UpdatedAt.Unix(),
		string(model.NormalizeKeyStrategy(string(ch.KeyStrategy))),
		string(model.NormalizeKeyFailurePolicy(string(ch.KeyFailurePolicy))),
		model.NormalizeKeyCooldownSeconds(ch.KeyCooldownSeconds),
		encodeRetryMode(ch.RetryMode),
		encodeRetryMaxAttempts(ch.RetryMaxAttempts),
		encodeModelRetryRules(ch.ModelRetryRules),
	)
	if err != nil {
		return fmt.Errorf("store: 新增渠道失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取新增渠道的自增 ID 失败: %w", err)
	}
	ch.ID = uint64(id)
	return nil
}

// GetByID 按主键查询渠道。不存在时返回 model.ErrChannelNotFound。
func (r *channelRepository) GetByID(ctx context.Context, id uint64) (*model.Channel, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+channelColumns+" FROM channels WHERE id = ?", id)

	ch, err := r.scanChannel(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrChannelNotFound
		}
		return nil, err
	}
	return ch, nil
}

// List 按条件查询渠道列表。
//
// 返回顺序固定为「优先级降序 → 权重降序 → ID 升序」，即路由选取候选时的推荐顺序，
// 上层可直接按序遍历做渠道选择，无需二次排序。
func (r *channelRepository) List(ctx context.Context, q model.ChannelQuery) ([]*model.Channel, error) {
	// 动态拼接 WHERE：所有值都通过占位符传入，杜绝 SQL 注入
	where, args := buildChannelWhere(q)

	var sb strings.Builder
	sb.WriteString("SELECT " + channelColumns + " FROM channels")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	sb.WriteString(" ORDER BY priority DESC, weight DESC, id ASC")

	// 分页保护：归一化到合法区间
	limit := q.Limit
	if limit <= 0 {
		limit = defaultChannelListLimit
	}
	if limit > maxChannelListLimit {
		limit = maxChannelListLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询渠道列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// 预分配容量，减少切片扩容
	channels := make([]*model.Channel, 0, limit)
	for rows.Next() {
		ch, err := r.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	// 注意：必须检查迭代过程中的错误，否则可能静默返回不完整结果
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历渠道结果集失败: %w", err)
	}
	return channels, nil
}

// Update 按 ID 更新渠道。不存在时返回 model.ErrChannelNotFound。
func (r *channelRepository) Update(ctx context.Context, ch *model.Channel) error {
	if ch.ID == 0 {
		return errors.New("store: 更新渠道时 ID 不能为 0")
	}
	if err := ch.Validate(); err != nil {
		return fmt.Errorf("store: 渠道数据非法: %w", err)
	}

	encryptedKey, err := r.cipher.Encrypt(ch.APIKey)
	if err != nil {
		return fmt.Errorf("store: 加密渠道密钥失败: %w", err)
	}

	ch.UpdatedAt = time.Now()

	// 刻意不更新 created_at：创建时间应保持不可变，便于审计
	res, err := r.db.ExecContext(ctx, `
		UPDATE channels SET
			name = ?, type = ?, type_key = ?, extra_config = ?, base_url = ?, api_key_enc = ?, models = ?,
			group_name = ?, group_names = ?, priority = ?, weight = ?, status = ?, updated_at = ?, key_strategy = ?,
			key_failure_policy = ?, key_cooldown_seconds = ?,
			retry_enabled = ?, retry_max_attempts = ?, model_retry_rules = ?
		WHERE id = ?`,
		ch.Name, ch.Type, ch.TypeKey, encodeExtraConfig(ch.ExtraConfig), ch.BaseURL, encryptedKey, encodeModels(ch.Models),
		ch.Group, encodeModels(ch.Groups), ch.Priority, ch.Weight, int(ch.Status),
		ch.UpdatedAt.Unix(), string(model.NormalizeKeyStrategy(string(ch.KeyStrategy))),
		string(model.NormalizeKeyFailurePolicy(string(ch.KeyFailurePolicy))),
		model.NormalizeKeyCooldownSeconds(ch.KeyCooldownSeconds),
		encodeRetryMode(ch.RetryMode),
		encodeRetryMaxAttempts(ch.RetryMaxAttempts),
		encodeModelRetryRules(ch.ModelRetryRules),
		ch.ID,
	)
	if err != nil {
		return fmt.Errorf("store: 更新渠道 %d 失败: %w", ch.ID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取更新影响行数失败: %w", err)
	}
	if affected == 0 {
		// 影响 0 行说明 ID 不存在（而不是"内容没变化"——SQLite 对相同值更新仍计入行数）
		return model.ErrChannelNotFound
	}
	return nil
}

// Delete 按 ID 物理删除渠道。不存在时返回 model.ErrChannelNotFound。
func (r *channelRepository) Delete(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM channels WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: 删除渠道 %d 失败: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取删除影响行数失败: %w", err)
	}
	if affected == 0 {
		return model.ErrChannelNotFound
	}
	return nil
}

// buildChannelWhere 构造渠道查询的 WHERE 子句与参数。
//
// 抽成独立函数是为了让"列表查询"与"计数查询"共用同一份筛选逻辑——
// 若各写一份，很容易出现"列表按分组过滤、计数忘了过滤"导致分页总页数错误。
func buildChannelWhere(q model.ChannelQuery) (string, []any) {
	var (
		conditions []string
		args       []any
	)
	if q.Group != "" {
		// 多分组匹配：请求分组命中「主分组」或「分组清单」中的任意一项即可路由。
		//
		// 为什么要同时判两处：group_names 为空表示"未显式配置多分组"，
		// 此时必须回退到 group_name（既有数据与旧版前端都只写这一列），
		// 否则升级后所有渠道都会因清单为空而匹配不上，直接全站 503。
		//
		// 清单匹配用「前后补逗号 + LIKE」实现：它把 CSV 变成 "…,free,aqua,…" 形式，
		// 从而避免 free 命中 freebies 这类子串误判；SQLite 与 MySQL 都支持，无需方言分支。
		//
		// 分组名做 LIKE 转义：分组标识里若含下划线（如 my_group），不转义时 "_"
		// 会被当作"任意单字符"通配符，既可能匹配到无关分组（路由错），
		// 也让查询串里的 % 变成全表扫描模式。
		conditions = append(conditions, "(group_name = ? OR (',' || group_names || ',') LIKE ? ESCAPE '\\')")
		args = append(args, q.Group, "%,"+escapeLike(q.Group)+",%")
	}
	if q.Status != nil {
		conditions = append(conditions, "status = ?")
		args = append(args, int(*q.Status))
	}
	return strings.Join(conditions, " AND "), args
}

// Count 返回符合条件的渠道总数。
func (r *channelRepository) Count(ctx context.Context, q model.ChannelQuery) (int, error) {
	where, args := buildChannelWhere(q)

	sb := strings.Builder{}
	sb.WriteString("SELECT COUNT(1) FROM channels")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, sb.String(), args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 统计渠道数失败: %w", err)
	}
	return total, nil
}

// StatusCounts 按状态分组统计渠道数量。
func (r *channelRepository) StatusCounts(ctx context.Context) (map[model.ChannelStatus]int, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT status, COUNT(1) FROM channels GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("store: 统计渠道状态失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := make(map[model.ChannelStatus]int, 3)
	for rows.Next() {
		var (
			status int
			count  int
		)
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("store: 读取渠道状态统计失败: %w", err)
		}
		counts[model.ChannelStatus(status)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历渠道状态统计失败: %w", err)
	}
	return counts, nil
}

// RecordProbeResult 记录一次渠道测活的完整结论。
//
// 实现要点：只更新测活相关的几个字段，不像 Update 那样把整行配置写回——
// 巡检是后台高频行为，若整行写回，会用巡检开始时读到的副本
// 覆盖管理员在这几毫秒里刚保存的配置（例如刚改好的 base_url）。
//
// 手动点「测活」与后台巡检共用此实现，两者的结论落在同一行同一组字段上，
// 页面才不会出现"巡检说 200ms、手动测活说 1800ms"的自相矛盾。
func (r *channelRepository) RecordProbeResult(ctx context.Context, result model.ChannelProbeResult) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE channels SET last_test_at = ?, last_test_ok = ?,
			latency_ms = ?, last_test_code = ?, last_test_model = ?
		WHERE id = ?`,
		result.At.Unix(), boolToInt(result.OK),
		result.LatencyMS, result.StatusCode, result.Model, result.ID)
	if err != nil {
		return fmt.Errorf("store: 记录渠道 %d 测活结果失败: %w", result.ID, err)
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

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的共同能力，使扫描逻辑只需写一份。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanChannel 把一行数据映射为领域对象，并完成密钥解密。
//
// 错误处理说明：
//   - sql.ErrNoRows 原样返回，由调用方翻译成 model.ErrChannelNotFound；
//   - 解密失败会包装为明确错误——通常意味着 AQUA_APP_KEY 被更换或数据被篡改。
func (r *channelRepository) scanChannel(sc rowScanner) (*model.Channel, error) {
	var (
		id         uint64
		name       string
		channelTy  int
		typeKey    string
		extraJSON  string
		baseURL    string
		encoded    string
		modelsCSV  string
		group      string
		groupNames string
		priority   int
		weight     int
		status     int
		createdAt  int64
		updatedAt  int64
		lastTestAt int64
		lastTestOK int
		// 健康巡检（迁移 0044）：最近一次测活的耗时、状态码与命中的模型名
		latencyMS     int
		lastTestCode  int
		lastTestModel string
		keyStrategy   string
		// 密钥失败策略（迁移 0024）：策略标识 + 统一冷却秒数
		keyFailurePolicy   string
		keyCooldownSeconds int
		// 重试策略（迁移 0037）：总开关 + 次数上限 + 模型级规则（JSON）
		retryEnabled     int
		retryMaxAttempts int
		modelRetryRules  string
	)

	if err := sc.Scan(&id, &name, &channelTy, &typeKey, &extraJSON, &baseURL, &encoded, &modelsCSV,
		&group, &groupNames, &priority, &weight, &status, &createdAt, &updatedAt,
		&lastTestAt, &lastTestOK, &latencyMS, &lastTestCode, &lastTestModel, &keyStrategy,
		&keyFailurePolicy, &keyCooldownSeconds,
		&retryEnabled, &retryMaxAttempts, &modelRetryRules); err != nil {
		// sql.ErrNoRows 属于正常控制流，不额外包装，便于调用方用 errors.Is 判断
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取渠道字段失败: %w", err)
	}

	apiKey, err := r.cipher.Decrypt(encoded)
	if err != nil {
		return nil, fmt.Errorf("store: 解密渠道 %d 的密钥失败（AQUA_APP_KEY 是否变更？）: %w", id, err)
	}

	return &model.Channel{
		ID:            id,
		Name:          name,
		Type:          channelTy,
		TypeKey:       typeKey,
		ExtraConfig:   decodeExtraConfig(extraJSON),
		BaseURL:       baseURL,
		APIKey:        apiKey,
		Models:        decodeModels(modelsCSV),
		Group:         group,
		Groups:        decodeModels(groupNames), // 与模型清单同为 CSV，解码规则一致故复用
		Priority:      priority,
		Weight:        weight,
		Status:        model.ChannelStatus(status),
		CreatedAt:     time.Unix(createdAt, 0),
		UpdatedAt:     time.Unix(updatedAt, 0),
		LastTestAt:    unixToExpiresAt(lastTestAt), // 复用"0 表示零值时间"的转换
		LastTestOK:    lastTestOK != 0,
		LatencyMS:     latencyMS,
		LastTestCode:  lastTestCode,
		LastTestModel: lastTestModel,

		KeyStrategy: model.NormalizeKeyStrategy(keyStrategy),

		KeyFailurePolicy:   model.NormalizeKeyFailurePolicy(keyFailurePolicy),
		KeyCooldownSeconds: model.NormalizeKeyCooldownSeconds(keyCooldownSeconds),

		RetryMode:        decodeRetryMode(retryEnabled),
		RetryMaxAttempts: model.NormalizeRetryMaxAttempts(retryMaxAttempts),
		ModelRetryRules:  decodeModelRetryRules(modelRetryRules),
	}, nil
}

// encodeRetryMode 把重试总开关编码为落库整数。
//
// 约定：关闭写 2，其余（未配置/明确开启）一律写 1。
// 为什么"未配置"要落成 1 而不是 0：列上只有 1/2 两种有意义的取值，
// 把语义在写入时定死，读出来就不必再猜——历史行由列默认值 1 覆盖，行为与旧版一致。
func encodeRetryMode(mode model.RetryMode) int {
	if mode == model.RetryModeOff {
		return int(model.RetryModeOff)
	}
	return int(model.RetryModeOn)
}

// decodeRetryMode 把落库整数还原为领域取值。
//
// 容错：0（列默认值被手工写成 0）或任何非法值一律按"未配置"处理，
// 由 RetryMode.Enabled() 得出"允许重试"——宁可多试一次，也不因脏数据静默关掉重试。
func decodeRetryMode(value int) model.RetryMode {
	mode := model.RetryMode(value)
	if !mode.IsValid() {
		return model.RetryModeUnset
	}
	return mode
}

// encodeRetryMaxAttempts 归一化重试次数后落库（0 保留为 0，表示"用默认值"）。
//
// 刻意不把 0 直接写成默认值 3：这样"站长把次数改回默认"与"从未配置过"
// 在库里是同一个值，将来若调整默认次数，所有未显式配置的渠道会一起受益。
func encodeRetryMaxAttempts(attempts int) int {
	if attempts <= 0 {
		return 0
	}
	if attempts > model.MaxRetryMaxAttempts {
		return model.MaxRetryMaxAttempts
	}
	return attempts
}

// encodeModelRetryRules 把模型级重试规则序列化为 JSON 数组字符串。
//
// 归一化后再序列化：保证落库的 model 字段无空白、无空项、无重复（读写一致，
// 不会出现"存进去两条一样、显示成两条"的困惑）。
func encodeModelRetryRules(rules []model.ModelRetryRule) string {
	normalized := model.NormalizeModelRetryRules(rules)
	if len(normalized) == 0 {
		// 与迁移默认值保持一致，避免 NULL 与 '[]' 两种空值并存
		return "[]"
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		// 不可达：结构体字段均为可序列化的基础类型。
		// 真出现时返回空数组而不是 panic——配置读不出来不该让整个渠道列表挂掉。
		return "[]"
	}
	return string(encoded)
}

// decodeModelRetryRules 解析模型级重试规则 JSON。
//
// 容错：字段为空串/非法 JSON/类型不符时一律返回 nil（= 无模型级规则），
// 调用方据此自然回退到渠道级策略；不因一行脏数据阻塞转发。
func decodeModelRetryRules(raw string) []model.ModelRetryRule {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil
	}
	var rules []model.ModelRetryRule
	if err := json.Unmarshal([]byte(trimmed), &rules); err != nil {
		return nil
	}
	return model.NormalizeModelRetryRules(rules)
}

// encodeModels 把模型列表编码为逗号分隔字符串（落库格式）。
//
// 处理细则：去除首尾空白、丢弃空项、去重（保持首次出现顺序）。
// 去重的意义：避免因重复配置导致路由时对同一模型重复候选。
func encodeModels(models []string) string {
	if len(models) == 0 {
		return ""
	}

	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, m := range models {
		name := strings.TrimSpace(m)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return strings.Join(result, ",")
}

// decodeModels 把逗号分隔字符串还原为模型列表（读取格式）。
func decodeModels(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			result = append(result, name)
		}
	}
	return result
}

// encodeExtraConfig 把类型专属参数序列化为 JSON 对象字符串（落库格式）。
//
// 空值一律落成 "{}"：让"没有扩展配置"在库里只有一种表示（空 JSON 对象），
// 避免 NULL 与空串并存导致读取端要处理两种"没有值"的情形。
// 序列化失败（理论上不会：键值均为字符串）时同样回退为 "{}"，绝不让一次
// 保存因扩展配置而整体失败——扩展配置只是锦上添花，不应阻断主流程。
func encodeExtraConfig(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// decodeExtraConfig 把 JSON 对象字符串还原为键值映射（读取格式）。
//
// 返回 nil 表示"无扩展配置"（空串 / "{}" / 非法 JSON）：
// 协议适配器对 nil 映射会自动回退到类型默认值（见 relay 的 extraValue）。
// 非法 JSON 不报错而是视为空：历史脏数据不应让整条渠道无法读取。
func decodeExtraConfig(raw string) map[string]string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
