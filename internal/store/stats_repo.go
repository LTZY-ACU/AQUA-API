// 本文件实现运维监控与备份校验所需的只读统计查询。
//
// 意图（Why）：
//
//	后台需要一个「一眼看清运行状态」的入口：数据库多大、各表多少行、
//	近 24 小时与近 7 天调用是否健康、磁盘还剩多少。这些指标全部是只读聚合，
//	与业务写入无关，因此单独成文件，避免污染各业务仓储。
//	把它们放在 store 层而不是 handler 里直接拼 SQL，是为了守住分层铁律：
//	server 层只做协议转换，SQL 一律留在 store。
//
// 流转（Flow）：
//
//	handler_maintenance.go → s.deps.Store.TableRowCounts / UsageHealth /
//	  DatabaseSizeBytes / DiskUsage / InspectSQLiteBackup → 本文件的 SQL 聚合
//
// 扩展（Extend）：
//
//	新增监控指标：在本文件加一个方法返回新结构体，并在 handler 的 DTO 中透出；
//	需要统计新表：直接往 maintenanceTables 追加表名（概览与备份校验会一起生效）。
//	注意：表名来自包内常量、不接受外部输入，方可安全拼接进 SQL。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LTZY-ACU/aqua-api/internal/model"
)

// maintenanceTables 是运维概览与备份校验共同关注的核心表清单。
//
// 集中维护的原因：概览的「各表行数」与备份校验的「表行数对比」必须用同一份清单，
// 否则两边会随功能演进逐渐漂移，出现「概览里有、校验里没有」的困惑。
// 表名的命名统一按语义取自本仓库（payment_orders 而非 orders、models/channel_model_mappings
// 而非合并表），刻意保持与迁移脚本一致。
var maintenanceTables = []string{
	"users",
	"sessions",
	"tokens",
	"channels",
	"channel_keys",
	"channel_model_mappings",
	"models",
	"model_groups",
	"model_prices",
	"usage_logs",
	"tasks",
	"payment_orders",
	"redeem_codes",
	"announcements",
	"oauth_providers",
	"email_codes",
	"audit_logs",
	"settings",
}

// TableStat 描述单张表的行数。
type TableStat struct {
	Name string // 表名
	Rows int64  // 行数
}

// UsageWindow 描述一个时间窗口内的调用健康度。
type UsageWindow struct {
	Requests     int64   // 调用总次数
	Failures     int64   // 失败次数（非 2xx，含未产生状态码的 0）
	AvgLatencyMS float64 // 平均耗时（毫秒），无数据时为 0
}

// UsageHealth 汇总近 24 小时与近 7 天的调用健康度。
type UsageHealth struct {
	Last24h UsageWindow // 近 24 小时
	Last7d  UsageWindow // 近 7 天
}

// DiskUsage 描述数据目录所在分区的磁盘水位。
//
// Available 为 false 表示当前平台/驱动无法获取（例如 Windows 开发环境的本地调试、
// 或使用内存库），此时上层应省略该字段而不是展示 0。
type DiskUsage struct {
	Available  bool   // 是否成功获取
	TotalBytes uint64 // 分区总容量（字节）
	FreeBytes  uint64 // 分区可用空间（字节）
}

// BackupInspection 是一次「备份文件只读校验」的结果。
type BackupInspection struct {
	SchemaVersion  int         // schema_migrations 中的最高版本号；无该表时为 0
	HasSchemaTable bool        // 备份中是否存在 schema_migrations 表
	Tables         []TableStat // 备份中各核心表的行数（仅包含存在的表）
}

// MaintenanceTables 返回核心表清单（固定顺序的副本）。
//
// 导出是刻意的：HTTP 层需要按同一顺序组装「备份 vs 当前」的对比结果，
// 若让前端或 handler 另维护一份清单，迟早会与统计查询不一致。
func MaintenanceTables() []string {
	return append([]string(nil), maintenanceTables...)
}

// TableRowCounts 统计各核心表的行数。
//
// 不存在的表会被跳过（例如尚未迁移到最新版本的旧库），返回的切片只含真实存在的表。
func (s *Store) TableRowCounts(ctx context.Context) ([]TableStat, error) {
	result := make([]TableStat, 0, len(maintenanceTables))
	for _, name := range maintenanceTables {
		exists, err := s.tableExists(ctx, name)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}

		var rows int64
		// name 来自包内常量清单，不含用户输入，故可安全拼接（占位符不能用于表名）。
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM "+name).Scan(&rows); err != nil {
			return nil, fmt.Errorf("store: 统计表 %s 行数失败: %w", name, err)
		}
		result = append(result, TableStat{Name: name, Rows: rows})
	}
	return result, nil
}

// tableExists 判断指定表是否存在（走方言，兼容未来接入的数据库）。
func (s *Store) tableExists(ctx context.Context, table string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, s.dialect.TableExistsSQL(table)).Scan(&count); err != nil {
		return false, fmt.Errorf("store: 检查表 %s 是否存在失败: %w", table, err)
	}
	return count > 0, nil
}

// UsageHealth 汇总近 24 小时与近 7 天的调用健康度。
//
// 为什么不用现有 usage_log_repo.Summary：Summary 只返回总数与成功数，
// 缺少「平均耗时」这一运维最关心的指标；本方法一次聚合出总数、失败数与平均耗时，
// 复用同一套「非 2xx 即失败」的判定（与 usage_log_repo.go 的 buildUsageWhere 保持一致）。
func (s *Store) UsageHealth(ctx context.Context, now time.Time) (*UsageHealth, error) {
	last24, err := s.usageWindow(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	last7d, err := s.usageWindow(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	return &UsageHealth{Last24h: *last24, Last7d: *last7d}, nil
}

// usageWindow 统计 [since, 现在) 内的调用次数、失败次数与平均耗时。
func (s *Store) usageWindow(ctx context.Context, since time.Time) (*UsageWindow, error) {
	const query = `SELECT
			COUNT(1),
			COALESCE(SUM(CASE WHEN status_code < 200 OR status_code >= 300 THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM usage_logs
		WHERE created_at >= ?`

	var window UsageWindow
	if err := s.db.QueryRowContext(ctx, query, since.Unix()).Scan(
		&window.Requests, &window.Failures, &window.AvgLatencyMS); err != nil {
		return nil, fmt.Errorf("store: 统计调用健康度失败: %w", err)
	}
	return &window, nil
}

// DatabaseSizeBytes 估算数据库体积（字节），ok 为 false 表示当前驱动无此概念。
//
// 为什么给出两种取法：PRAGMA page_count * page_size 反映逻辑数据库大小，
// 不受 WAL 文件与预分配空页干扰，是首选；但它依赖具体驱动，失败时退回文件 stat，
// 这样即便 PRAGMA 不可用也能给出近似值。非 SQLite 驱动返回 ok=false。
func (s *Store) DatabaseSizeBytes(ctx context.Context) (size int64, ok bool, err error) {
	if s.driver != DriverSQLite {
		return 0, false, nil
	}

	// 首选：PRAGMA 计算逻辑大小
	var pageCount, pageSize int64
	if qErr := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); qErr == nil {
		if qErr = s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); qErr == nil && pageCount > 0 && pageSize > 0 {
			return pageCount * pageSize, true, nil
		}
	}

	// 退路：直接 stat 主库文件
	path, pathErr := s.SQLiteMainFilePath(ctx)
	if pathErr != nil {
		return 0, false, fmt.Errorf("store: 获取数据库体积失败: %w", pathErr)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return 0, false, fmt.Errorf("store: 读取数据库文件大小失败: %w", statErr)
	}
	return info.Size(), true, nil
}

// SQLiteMainFilePath 返回当前连接的主数据库文件路径。
//
// 用途：磁盘水位需要知道数据目录落在哪个分区；数据库体积的退路取法也依赖它。
// 只支持 SQLite（其它驱动的数据目录由 DBA 负责管理，网关无从得知）。
func (s *Store) SQLiteMainFilePath(ctx context.Context) (string, error) {
	if s.driver != DriverSQLite {
		return "", fmt.Errorf("store: 仅 SQLite 支持读取数据文件路径（当前驱动 %s）", s.driver)
	}

	rows, err := s.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", fmt.Errorf("store: 查询数据库文件列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			seq  int
			name string
			file string
		)
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", fmt.Errorf("store: 读取数据库文件列表失败: %w", err)
		}
		if name == "main" {
			return file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("store: 遍历数据库文件列表失败: %w", err)
	}
	return "", fmt.Errorf("store: 未找到主数据库文件")
}

// DiskUsage 返回数据库文件所在分区的磁盘水位。
//
// 说明：内存库或无法定位文件时返回 Available=false 而非报错——
// 这只是监控面板上的一个可选项，不应因为它取不到就让整个概览失败。
func (s *Store) DiskUsage(ctx context.Context) (DiskUsage, error) {
	if s.driver != DriverSQLite {
		return DiskUsage{}, nil
	}

	path, err := s.SQLiteMainFilePath(ctx)
	if err != nil {
		return DiskUsage{}, err
	}
	// 内存库（:memory:）没有所在分区
	if path == "" {
		return DiskUsage{}, nil
	}

	total, free, ok := diskSpace(filepath.Dir(path))
	if !ok {
		return DiskUsage{}, nil
	}
	return DiskUsage{Available: true, TotalBytes: total, FreeBytes: free}, nil
}

// InspectSQLiteBackup 以只读方式打开一个备份文件并读取其结构信息。
//
// 安全约束：使用 mode=ro 打开，绝不触碰备份文件内容；仅读取 sqlite_master 与
// 各表行数。文件不是合法 SQLite 时在首次查询即报错，由上层翻译成明确的中文提示。
//
// 为什么不在当前库上执行：备份文件是「另一份独立文件」，用一个临时只读连接打开
// 可以完全避免污染正在服务的数据库（例如误执行到主连接上）。
func InspectSQLiteBackup(ctx context.Context, path string) (*BackupInspection, error) {
	// 统一用斜杠，保证 Windows 路径也能被 SQLite URI 正确解析。
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开备份文件失败: %w", err)
	}
	defer func() { _ = db.Close() }()

	// 尝试读取 sqlite_master：非 SQLite 文件（或损坏文件）会在此暴露。
	var tableCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM sqlite_master").Scan(&tableCount); err != nil {
		return nil, fmt.Errorf("store: 备份文件不是有效的 SQLite 数据库: %w", err)
	}

	inspection := &BackupInspection{}

	// schema_migrations 可能不存在（任意合法 SQLite 文件都可能被上传），需先判断。
	var hasSchema int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&hasSchema); err != nil {
		return nil, fmt.Errorf("store: 检查备份 schema 表失败: %w", err)
	}
	if hasSchema > 0 {
		inspection.HasSchemaTable = true
		var version sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
			return nil, fmt.Errorf("store: 读取备份 schema 版本失败: %w", err)
		}
		if version.Valid {
			inspection.SchemaVersion = int(version.Int64)
		}
	}

	// 各核心表行数：只统计备份中确实存在的表，缺表由上层对比时体现为 InBackup=false。
	for _, name := range maintenanceTables {
		var has int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&has); err != nil {
			return nil, fmt.Errorf("store: 检查备份表 %s 失败: %w", name, err)
		}
		if has == 0 {
			continue
		}
		var rows int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM "+name).Scan(&rows); err != nil {
			return nil, fmt.Errorf("store: 统计备份表 %s 行数失败: %w", name, err)
		}
		inspection.Tables = append(inspection.Tables, TableStat{Name: name, Rows: rows})
	}

	return inspection, nil
}

// ---------------------------------------------------------------------------
// 真实成本对账（收入 − 成本 = 毛利）
// ---------------------------------------------------------------------------

// ReconcileUsage 聚合 [from, to) 内各维度（分组 / 渠道 / 模型）的请求数、收入、成本与毛利。
//
// 口径（三处必须一致，否则"对账"本身就是错的）：
//  1. 只统计成功请求（2xx/3xx）：失败请求的额度已在结算时全额退还，既不构成收入，
//     也不产生上游成本；
//  2. 收入取 usage_logs.quota —— 它是"用户实扣额度"，是全站收入的唯一来源；
//  3. 成本按 (渠道, 上游模型名) 匹配 channel_model_costs 后逐行估算，
//     按次规则走"次数 × 每次单价"（与密钥核算共用 ComputeCost），
//     因此按次计费渠道的成本不会被算成 0。
//
// 为什么放在 store 层：server 层只做协议转换，SQL 与聚合一律留在 store（分层铁律）。
// 为什么先按 (维度, 渠道, 模型) 分组再在 Go 里折叠：成本匹配需要"渠道 + 上游模型名"，
// 且要按按次/按 token 分流，无法用一条纯 SQL 表达；因此让 SQL 负责可下推的聚合，
// 匹配与折叠加在 Go 里完成——逻辑集中，且可直接喂数据单测。
func (s *Store) ReconcileUsage(ctx context.Context, from, to time.Time, dim string) (
	[]model.UsageReconciliationRow, error) {
	if !model.IsValidReconcileDim(dim) {
		return nil, fmt.Errorf("store: 不支持的对账维度 %q（可选 group / channel / model）", dim)
	}
	if !to.After(from) {
		return nil, fmt.Errorf("store: 对账时间窗非法（起点不早于终点）")
	}

	// 维度表达式：
	//   - group：usage_logs 不落分组，分组是"令牌当时的归属"，按 token_id 关联读取；
	//     令牌被删除或 group_name 为空时归入空串（展示层显示为默认分组）；
	//   - channel：渠道 ID 转字符串作为分组键；
	//   - model：直接用模型名。
	keyExpr := "u.model"
	switch dim {
	case model.ReconcileDimGroup:
		keyExpr = "COALESCE(t.group_name, '')"
	case model.ReconcileDimChannel:
		keyExpr = "CAST(u.channel_id AS TEXT)"
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+keyExpr+` AS dim_key, u.channel_id, u.model, u.upstream_model,
		       COUNT(1),
		       COALESCE(SUM(u.quota), 0),
		       COALESCE(SUM(u.prompt_tokens), 0),
		       COALESCE(SUM(u.completion_tokens), 0),
		       COALESCE(SUM(u.cached_tokens), 0)
		FROM usage_logs u
		LEFT JOIN tokens t ON t.id = u.token_id
		WHERE u.created_at >= ? AND u.created_at < ?
		  AND u.status_code >= 200 AND u.status_code < 400
		GROUP BY dim_key, u.channel_id, u.model, u.upstream_model`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: 对账聚合查询失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// 进价与渠道名都是"小表全量读"，一次读进来在内存里匹配，逻辑直观且可单测。
	costsByChannel, err := s.loadCostsByChannel(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.channelNames(ctx)
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]*model.UsageReconciliationRow)
	for rows.Next() {
		var (
			key                            string
			channelID                      uint64
			modelName, upstreamModel       string
			requests, revenue              int64
			promptTokens, completionTokens int64
			cachedTokens                   int64
		)
		if err := rows.Scan(&key, &channelID, &modelName, &upstreamModel, &requests, &revenue,
			&promptTokens, &completionTokens, &cachedTokens); err != nil {
			return nil, fmt.Errorf("store: 读取对账聚合结果失败: %w", err)
		}

		item := byKey[key]
		if item == nil {
			item = &model.UsageReconciliationRow{
				Dim:   dim,
				Key:   key,
				Label: reconcileLabel(dim, key, names),
			}
			byKey[key] = item
		}

		// 成本匹配用上游模型名（钱是上游按上游模型名收的）；为空表示未改写，回退对外名。
		costModelName := strings.TrimSpace(upstreamModel)
		if costModelName == "" {
			costModelName = modelName
		}
		item.Requests += requests
		item.RevenueQuota += revenue
		if matched := model.MatchChannelModelCost(costsByChannel[channelID], costModelName); matched != nil {
			item.CostQuota += matched.ComputeCost(promptTokens, completionTokens, cachedTokens, requests)
			item.PricedRequests += requests
		} else {
			// 未录进价：成本按 0 计，但请求数单独累计——"未知"必须与"免费"区分开。
			item.UnpricedRequests += requests
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历对账聚合结果失败: %w", err)
	}

	result := make([]model.UsageReconciliationRow, 0, len(byKey))
	for _, item := range byKey {
		result = append(result, *item)
	}
	// 收入从高到低：站长最先要看的永远是"哪一块业务在赚钱/亏钱"。
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].RevenueQuota != result[j].RevenueQuota {
			return result[i].RevenueQuota > result[j].RevenueQuota
		}
		return result[i].Key < result[j].Key
	})
	return result, nil
}

// loadCostsByChannel 读取全部上游进价并按渠道分组。
//
// 规则数量小（每个渠道几条到几十条），一次全量读取比维护缓存更简单、也更容易解释
// （与 channel_model_cost_repo.go 的取舍一致）。
func (s *Store) loadCostsByChannel(ctx context.Context) (map[uint64][]*model.ChannelModelCost, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, channel_id, model, prompt_price, cache_price, completion_price, per_call_price
		FROM channel_model_costs`)
	if err != nil {
		return nil, fmt.Errorf("store: 读取上游进价失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byChannel := make(map[uint64][]*model.ChannelModelCost)
	for rows.Next() {
		var cost model.ChannelModelCost
		if err := rows.Scan(&cost.ID, &cost.ChannelID, &cost.Model,
			&cost.PromptPrice, &cost.CachePrice, &cost.CompletionPrice, &cost.PerCallPrice); err != nil {
			return nil, fmt.Errorf("store: 读取上游进价字段失败: %w", err)
		}
		byChannel[cost.ChannelID] = append(byChannel[cost.ChannelID], &cost)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历上游进价失败: %w", err)
	}
	return byChannel, nil
}

// channelNames 返回 渠道 ID → 渠道名 的映射（对账表展示用；名字取不到不影响数字）。
func (s *Store) channelNames(ctx context.Context) (map[uint64]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name FROM channels")
	if err != nil {
		return nil, fmt.Errorf("store: 读取渠道名失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	names := make(map[uint64]string)
	for rows.Next() {
		var (
			id   uint64
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("store: 读取渠道名字段失败: %w", err)
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历渠道名失败: %w", err)
	}
	return names, nil
}

// reconcileLabel 生成某一维度分组的展示标签。
func reconcileLabel(dim, key string, names map[uint64]string) string {
	switch dim {
	case model.ReconcileDimChannel:
		if id, err := strconv.ParseUint(key, 10, 64); err == nil {
			if name := names[id]; name != "" {
				return name
			}
		}
		return key
	case model.ReconcileDimGroup:
		if key == "" {
			return "(默认分组)"
		}
		return key
	default:
		if key == "" {
			return "(未知模型)"
		}
		return key
	}
}
