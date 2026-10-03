// 本文件是 model.UsageLogRepository 与 model.SettingRepository 的 SQL 实现。
//
// 意图（Why）：
//
//	调用日志层同时承担「写入」与「聚合」两类职责：
//	  - 写入：每次转发完成后落一条记录（高频，必须轻量）；
//	  - 聚合：仪表盘与用户门户所需的汇总、趋势、排行（低频但需正确）。
//	把聚合放在 SQL 层而非取全量再在内存计算，是因为日志会快速增长，
//	全量拉取会带来巨大的内存与网络开销。
//
// 流转（Flow）：
//
//	relay 转发完成 → Create
//	后台/门户 → Summary / DailySeries / TopModels / List
//
// 扩展（Extend）：
//
//	日志量继续增长后：考虑按月分表或引入独立日志库（如 ClickHouse），
//	  本文件的接口签名无需变化，仅替换实现即可。
//	新增统计维度：在 model.UsageLogQuery 加条件，并同步更新 buildUsageWhere。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// 日志列表查询的条数约束。
const (
	defaultLogListLimit = 20
	maxLogListLimit     = 100
)

// defaultSeriesDays 是聚合查询未指定起始时间时默认回溯的天数。
const defaultSeriesDays = 7

// usageLogColumns 集中定义查询列，顺序必须与 scanUsageLog 的扫描顺序严格一致。
const usageLogColumns = `id, user_id, token_id, channel_id, channel_key_id, model, upstream_model, prompt_tokens, completion_tokens,
	total_tokens, cached_tokens, reasoning_tokens, first_token_ms, tokens_per_second,
	quota, latency_ms, is_stream, status_code, error, request_id, price_version, created_at`

// usageLogRepository 是 model.UsageLogRepository 的 SQL 实现，并发安全。
type usageLogRepository struct {
	db *sql.DB
	// dialect：本仓储里「按天分桶」的表达式与数据库方言相关，故需要它。
	// 其余仓储的语句是通用 SQL，不需要方言，构造时也就不传 —— 只让真正有差异的地方拿到方言。
	dialect Dialect
}

// NewUsageLogRepository 创建调用日志仓储。
func NewUsageLogRepository(db *sql.DB, dialect Dialect) model.UsageLogRepository {
	return &usageLogRepository{db: db, dialect: dialect}
}

// Create 写入一条调用日志。
func (r *usageLogRepository) Create(ctx context.Context, log *model.UsageLog) error {
	if err := log.Validate(); err != nil {
		return fmt.Errorf("store: 调用日志非法: %w", err)
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO usage_logs
			(user_id, token_id, channel_id, channel_key_id, model, upstream_model, prompt_tokens, completion_tokens, total_tokens,
			 cached_tokens, reasoning_tokens, first_token_ms, tokens_per_second,
			 quota, latency_ms, is_stream, status_code, error, request_id, price_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.UserID, log.TokenID, log.ChannelID, log.ChannelKeyID, log.Model, log.UpstreamModel,
		log.PromptTokens, log.CompletionTokens, log.TotalTokens,
		log.CachedTokens, log.ReasoningTokens, log.FirstTokenMS, log.TokensPerSecond,
		log.Quota, log.LatencyMS, boolToInt(log.IsStream), log.StatusCode,
		log.Error, log.RequestID, log.PriceVersion, log.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: 写入调用日志失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: 读取日志 ID 失败: %w", err)
	}
	log.ID = uint64(id)
	return nil
}

// List 按条件返回日志列表（时间倒序，最新在前）。
func (r *usageLogRepository) List(ctx context.Context, q model.UsageLogQuery) ([]*model.UsageLog, error) {
	where, args := buildUsageWhere(q)

	var sb strings.Builder
	sb.WriteString("SELECT " + usageLogColumns + " FROM usage_logs")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	// 按自增主键倒序而非时间倒序：同一秒内写入多条时，时间无法区分先后，
	// 用 id 排序才能保证分页结果稳定（否则翻页可能重复或漏行）。
	sb.WriteString(" ORDER BY id DESC")

	limit := normalizeLimit(q.Limit, defaultLogListLimit, maxLogListLimit)
	offset := normalizeOffset(q.Offset)
	sb.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询调用日志失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := make([]*model.UsageLog, 0, limit)
	for rows.Next() {
		entry, err := scanUsageLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历日志结果集失败: %w", err)
	}
	return logs, nil
}

// Count 返回符合条件的日志总数。
func (r *usageLogRepository) Count(ctx context.Context, q model.UsageLogQuery) (int, error) {
	where, args := buildUsageWhere(q)

	sb := strings.Builder{}
	sb.WriteString("SELECT COUNT(1) FROM usage_logs")
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, sb.String(), args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("store: 统计日志数失败: %w", err)
	}
	return total, nil
}

// Summary 返回符合条件的用量汇总。
func (r *usageLogRepository) Summary(ctx context.Context, q model.UsageLogQuery) (*model.UsageSummary, error) {
	where, args := buildUsageWhere(q)

	// 用 CASE WHEN 统计成功数，避免为"成功率"再发一次查询。
	//
	// 平均延迟/首 token/速率都按"有样本才算"的口径求和：
	//   - TTFB 只在流式且确有首包时才有值，用请求总数当分母会把它拉低成无意义的数字；
	//   - 输出速率只在"有输出且时长可算"时才有值，同理单独计样本。
	sb := strings.Builder{}
	sb.WriteString(`SELECT
			COUNT(1),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(quota), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(latency_ms), 0),
			COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN first_token_ms ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_per_second > 0 THEN tokens_per_second ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_per_second > 0 THEN 1 ELSE 0 END), 0)
		FROM usage_logs`)
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}

	var summary model.UsageSummary
	if err := r.db.QueryRowContext(ctx, sb.String(), args...).Scan(
		&summary.Requests, &summary.Success, &summary.Tokens, &summary.Quota,
		&summary.PromptTokens, &summary.CompletionTokens,
		&summary.CachedTokens, &summary.ReasoningTokens,
		&summary.LatencySumMS,
		&summary.FirstTokenSumMS, &summary.FirstTokenSamples,
		&summary.TPSSum, &summary.TPSSamples); err != nil {
		return nil, fmt.Errorf("store: 汇总用量失败: %w", err)
	}
	return &summary, nil
}

// DailySeries 按天聚合用量，并补全没有请求的日期。
//
// 补零的必要性：若某天完全没有请求，SQL 的 GROUP BY 不会产生该日期的行，
// 前端折线图会出现断点——运维看到"线断了"会误判为服务中断，实际上只是没人用。
func (r *usageLogRepository) DailySeries(ctx context.Context, q model.UsageLogQuery) ([]model.DailyUsage, error) {
	since, until := normalizeSeriesRange(q)

	// 强制使用补零后的时间范围，避免与既有的 Since/Until 条件冲突
	rangeQuery := q
	rangeQuery.Since = &since
	rangeQuery.Until = &until
	where, args := buildUsageWhere(rangeQuery)

	// 按本地日期分组：使用者关心的是"我这边几号用了多少"，
	// 若按 UTC 分组，东八区凌晨的用量会被算到前一天。
	// 具体表达式由方言提供（SQLite 用 strftime，其他库各不相同）。
	sb := strings.Builder{}
	sb.WriteString(`SELECT
			` + r.dialect.DayBucket("created_at") + ` AS day,
			COUNT(1),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(quota), 0),
			COALESCE(SUM(cached_tokens), 0)
		FROM usage_logs`)
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	sb.WriteString(" GROUP BY day ORDER BY day ASC")

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 按天聚合失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// 先把查询结果放入 map，再按完整日期区间顺序输出
	byDay := make(map[string]model.DailyUsage)
	for rows.Next() {
		var item model.DailyUsage
		if err := rows.Scan(&item.Date, &item.Requests, &item.Tokens, &item.Quota, &item.CachedTokens); err != nil {
			return nil, fmt.Errorf("store: 读取按天聚合结果失败: %w", err)
		}
		byDay[item.Date] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历按天聚合结果失败: %w", err)
	}

	// 补零循环使用半开区间 [since, until)：until 是"次日零点"，
	// 若写成 !day.After(until) 会把次日也输出为一个全零数据点，
	// 前端趋势图末尾会多出一根"未来"的空柱子。
	series := make([]model.DailyUsage, 0, len(byDay)+1)
	for day := since; day.Before(until); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		if item, ok := byDay[key]; ok {
			series = append(series, item)
			continue
		}
		// 该日期无数据，补零占位
		series = append(series, model.DailyUsage{Date: key})
	}
	return series, nil
}

// TopModels 返回用量最高的前 N 个模型（按请求数降序）。
func (r *usageLogRepository) TopModels(ctx context.Context, q model.UsageLogQuery, limit int) ([]model.ModelUsage, error) {
	where, args := buildUsageWhere(q)

	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}

	sb := strings.Builder{}
	sb.WriteString(`SELECT model, COUNT(1), COALESCE(SUM(total_tokens), 0)
		FROM usage_logs`)
	if where != "" {
		sb.WriteString(" WHERE " + where)
	}
	// 空模型名（多为解析失败的请求）不参与排行，避免占据榜单
	sb.WriteString(" GROUP BY model HAVING model != '' ORDER BY COUNT(1) DESC LIMIT ?")
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 模型排行查询失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]model.ModelUsage, 0, limit)
	for rows.Next() {
		var item model.ModelUsage
		if err := rows.Scan(&item.Model, &item.Requests, &item.Tokens); err != nil {
			return nil, fmt.Errorf("store: 读取模型排行失败: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历模型排行失败: %w", err)
	}
	return result, nil
}

// ModelFailureStats 返回某渠道在指定时间窗内的失败请求统计（按模型 × 状态码）。
//
// 用途与 SQL 约定（与接口注释口径一致）：
//   - 只统计 status_code >= 400 的行：成功的、以及不算失败的 3xx 不参与；
//   - 排除空模型名：解析失败的请求没有模型可归属，参与统计只会制造噪音；
//   - (model, status_code) 两列分组：同一模型"404 与 403"是两种不同的处置方向
//     （模型已下线 vs 凭据无授权），必须分开成行，合并会模糊处置动作。
func (r *usageLogRepository) ModelFailureStats(ctx context.Context, channelID uint64, since time.Time) ([]model.ModelFailureStat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT model, status_code, COUNT(1)
		FROM usage_logs
		WHERE channel_id = ? AND created_at >= ? AND status_code >= 400 AND model != ''
		GROUP BY model, status_code
		ORDER BY COUNT(1) DESC, model ASC`,
		channelID, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: 查询模型失败统计失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]model.ModelFailureStat, 0, 16)
	for rows.Next() {
		var stat model.ModelFailureStat
		if err := rows.Scan(&stat.Model, &stat.StatusCode, &stat.Count); err != nil {
			return nil, fmt.Errorf("store: 读取模型失败统计失败: %w", err)
		}
		result = append(result, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历模型失败统计失败: %w", err)
	}
	return result, nil
}

// Leaderboard 返回统计窗口内各用户的综合用量排行。
//
// SQL 采用「两次聚合」：
//  1. 子查询按 user_id 聚合请求数、token 总量、平均耗时，并过滤成功请求；
//  2. 外层 LEFT JOIN users（取用户名）与 payment_orders 的已支付标记（付费判定）。
//
// 峰值并发不在 SQL 里算：它需要把每条请求展开成 [开始, 结束] 区间再做
// 差分事件扫描，SQLite 表达繁琐且不易维护，因此这里只取回每用户
// 成功请求的 (created_at, latency_ms) 样本，由 Go 侧做扫描（见 peakConcurrency）。
// 样本量 = 该窗口内成功请求数，对自托管网关规模（每日数千条）完全可控。
func (r *usageLogRepository) Leaderboard(ctx context.Context, q model.UsageLogQuery) ([]model.LeaderboardEntry, error) {
	// 通用 WHERE 里的列名（created_at / user_id / token_id / model / status_code）在
	// 第一遍聚合查询（JOIN users / payment_orders）中会与关联表列名歧义，
	// 必须统一限定为 u.<col>。buildUsageWhere 生成的片段格式固定
	// （"col = ?" / "(col >= 200 AND col < 300)" 等），逐列名替换即可。
	where, args := buildUsageWhere(q)
	whereQualified := qualifyUsageWhereColumns(where)

	// 排行榜口径：统计【全部请求】（含失败），成功数另行聚合——
	// 这样"请求数"反映真实使用量，"成功率"能独立衡量稳定性，两者不互相污染。
	// 唯一硬约束是必须有归属账号（user_id > 0），否则无法归属到人。
	joinConditions := []string{whereQualified}
	if whereQualified != "" {
		joinConditions[0] = "(" + whereQualified + ")"
	}
	joinConditions = append(joinConditions, "u.user_id > 0")
	joinFilter := strings.Join(joinConditions, " AND ")

	// 第二遍：单表并发样本（无 JOIN，列名不带前缀）
	// 第二遍（并发样本）仍只取【成功】请求：区间重叠算法要求每条区间有效，
	// 失败请求没有可用的"在途时长"，计入只会让并发数虚高。
	flatConditions := []string{where}
	if where != "" {
		flatConditions[0] = "(" + where + ")"
	}
	flatConditions = append(flatConditions, "status_code >= 200", "status_code < 400", "user_id > 0")
	flatFilter := strings.Join(flatConditions, " AND ")

	rows, err := r.db.QueryContext(ctx, `
		SELECT u.user_id,
		       COALESCE(SUM(u.total_tokens), 0),
		       COALESCE(AVG(CASE WHEN u.status_code >= 200 AND u.status_code < 400 THEN u.latency_ms END), 0),
		       COUNT(1),
		       COALESCE(SUM(CASE WHEN u.status_code >= 200 AND u.status_code < 400 THEN 1 ELSE 0 END), 0),
		       COALESCE(us.username, ''),
		       EXISTS(SELECT 1 FROM payment_orders po WHERE po.user_id = u.user_id AND po.status = 2) AS paid
		FROM usage_logs u
		LEFT JOIN users us ON us.id = u.user_id
		WHERE `+joinFilter+`
		GROUP BY u.user_id, us.username, paid`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("store: 排行榜聚合查询失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// 第一遍：汇总指标 + 收集并发样本
	type rowSample struct {
		entry  model.LeaderboardEntry
		events []usageEvent
	}
	byUser := make(map[uint64]*rowSample)
	for rows.Next() {
		var (
			userID         uint64
			tokens         int64
			avgLatency     float64
			requests       int64
			successReqs    int64
			username       string
			paid           bool
		)
		if err := rows.Scan(&userID, &tokens, &avgLatency, &requests, &successReqs, &username, &paid); err != nil {
			return nil, fmt.Errorf("store: 读取排行榜聚合结果失败: %w", err)
		}
		byUser[userID] = &rowSample{
			entry: model.LeaderboardEntry{
				UserID:          userID,
				Username:        username,
				Requests:        requests,
				SuccessRequests: successReqs,
				Tokens:          tokens,
				AvgLatencyMS:    avgLatency,
				Paid:            paid,
			},
			events: make([]usageEvent, 0, int(requests)),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历排行榜聚合结果失败: %w", err)
	}

	// 第二遍：拉取每用户成功请求的 (created_at, latency_ms)，供并发扫描。
	// 单独查询而非 JOIN 进上面的大查询：避免结果集膨胀（每行带全部区间事件），
	// 且这里只需要两列，SQLite 扫描更轻。单表查询用无前缀的 flatFilter。
	eventRows, err := r.db.QueryContext(ctx, `
		SELECT user_id, created_at, latency_ms
		FROM usage_logs
		WHERE `+flatFilter+`
		ORDER BY user_id, created_at`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("store: 排行榜并发样本查询失败: %w", err)
	}
	defer func() { _ = eventRows.Close() }()

	for eventRows.Next() {
		var (
			userID   uint64
			created  int64
			latency  int
		)
		if err := eventRows.Scan(&userID, &created, &latency); err != nil {
			return nil, fmt.Errorf("store: 读取排行榜并发样本失败: %w", err)
		}
		if sample, ok := byUser[userID]; ok && latency >= 0 {
			// 区间：开始 = created_at − latency（秒），结束 = created_at。
			// latency_ms 换算成秒时向 0 截断（区间至少 1 秒），避免毫秒级请求
			// 被算成"开始晚于结束"的空区间。
			start := created - int64(latency/1000)
			if start > created {
				start = created
			}
			sample.events = append(sample.events, usageEvent{at: start, delta: 1}, usageEvent{at: created, delta: -1})
		}
	}
	if err := eventRows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历排行榜并发样本失败: %w", err)
	}

	result := make([]model.LeaderboardEntry, 0, len(byUser))
	for _, sample := range byUser {
		sample.entry.PeakConcurrency = peakConcurrency(sample.events)
		result = append(result, sample.entry)
	}
	return result, nil
}

// usageEvent 是一次差分事件：某个时间点上并发数 +1（请求开始）或 −1（请求结束）。
type usageEvent struct {
	at    int64 // 事件发生时间（Unix 秒）
	delta int   // +1 或 −1
}

// peakConcurrency 用差分事件扫描求区间集合的最大重叠数。
//
// 算法：把所有「开始 +1 / 结束 −1」事件按时间排序，同时间点的事件合并
// （先加后减——同一秒开始的请求与结束的请求重叠，计入并发），
// 然后从左到右累加 delta，过程中的最大值即峰值并发。
func peakConcurrency(events []usageEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at < events[j].at
		}
		return events[i].delta > events[j].delta // +1 排在 −1 前
	})

	var (
		current int64
		peak    int64
	)
	for _, e := range events {
		current += int64(e.delta)
		if current > peak {
			peak = current
		}
	}
	return peak
}

// SumUsageByChannelKey 按 (密钥, 模型) 汇总某渠道的用量，用于密钥余额核算。
//
// SQL 层面的三个约束与 model.UsageLogRepository 的接口注释一一对应：
//   - status_code < 400：只算成功请求（失败通常不消耗上游额度）；
//   - channel_key_id > 0：无法归属到具体凭据的行（单密钥模式 / 历史数据）不参与；
//   - channel_id = ?：一次只算一个渠道，避免把别的渠道成本混进来。
//
// 用 SUM(...) 时必须 COALESCE：SQLite 对全 NULL 列求和返回 NULL，
// 直接扫进 int64 会报错（历史上列可空，虽然新代码总是写 0）。
func (r *usageLogRepository) SumUsageByChannelKey(ctx context.Context, channelID uint64) ([]*model.ChannelKeyUsage, error) {
	if channelID == 0 {
		return []*model.ChannelKeyUsage{}, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT channel_key_id, channel_id, model, upstream_model,
		       COUNT(1),
		       COALESCE(SUM(prompt_tokens), 0),
		       COALESCE(SUM(completion_tokens), 0),
		       COALESCE(SUM(cached_tokens), 0)
		FROM usage_logs
		WHERE channel_id = ? AND channel_key_id > 0 AND status_code < 400
		GROUP BY channel_key_id, model, upstream_model
		ORDER BY channel_key_id ASC`, channelID)
	if err != nil {
		return nil, fmt.Errorf("store: 按密钥汇总用量失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]*model.ChannelKeyUsage, 0, 32)
	for rows.Next() {
		var item model.ChannelKeyUsage
		if err := rows.Scan(&item.ChannelKeyID, &item.ChannelID, &item.Model, &item.UpstreamModel,
			&item.Requests, &item.PromptTokens, &item.CompletionTokens, &item.CachedTokens); err != nil {
			return nil, fmt.Errorf("store: 读取密钥用量汇总失败: %w", err)
		}
		result = append(result, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历密钥用量汇总失败: %w", err)
	}
	return result, nil
}

// qualifyUsageWhereColumns 把 buildUsageWhere 生成的 WHERE 片段中的
// 裸列名统一加上表别名前缀（用于带 JOIN 的查询，避免歧义）。
//
// 实现说明：buildUsageWhere 产出的片段结构固定，出现的列名只有
// user_id / token_id / channel_id / model / status_code / created_at 六种，
// 且都以 "列名 + 操作符" 形式出现（= / >= / < / IN 等），逐列名做
// "完整单词边界"替换即可。此函数仅被 Leaderboard 使用（JOIN 场景），
// 其它单表查询不受影响。
func qualifyUsageWhereColumns(where string) string {
	columns := []string{"user_id", "token_id", "channel_id", "model", "status_code", "created_at"}
	qualified := where
	for _, col := range columns {
		qualified = strings.ReplaceAll(qualified, col, "u."+col)
	}
	return qualified
}

// buildUsageWhere 构造日志查询的 WHERE 子句与参数。
func buildUsageWhere(q model.UsageLogQuery) (string, []any) {
	var (
		conditions []string
		args       []any
	)

	if q.UserID != nil {
		conditions = append(conditions, "user_id = ?")
		args = append(args, *q.UserID)
	}
	if q.TokenID != nil {
		conditions = append(conditions, "token_id = ?")
		args = append(args, *q.TokenID)
	}
	if q.ChannelID != nil {
		conditions = append(conditions, "channel_id = ?")
		args = append(args, *q.ChannelID)
	}
	if modelName := strings.TrimSpace(q.Model); modelName != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, modelName)
	}

	switch q.Status {
	case model.LogStatusSuccess:
		conditions = append(conditions, "(status_code >= 200 AND status_code < 300)")
	case model.LogStatusError:
		// 把"非 2xx"统一视为失败；注意 0（未产生状态码）也属于失败
		conditions = append(conditions, "(status_code < 200 OR status_code >= 300)")
	}

	if q.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, q.Since.Unix())
	}
	if q.Until != nil {
		// 注意用 <（严格小于）并在调用方保证 Until 为"次日零点"，
		// 这样可精确覆盖整天，避免"结束当天 00:00 之后的记录被漏掉"。
		conditions = append(conditions, "created_at < ?")
		args = append(args, q.Until.Unix())
	}

	return strings.Join(conditions, " AND "), args
}

// normalizeSeriesRange 归一化趋势查询的时间范围。
//
// 约定：Until 为"次日零点"（半开区间），保证包含结束当天全天。
func normalizeSeriesRange(q model.UsageLogQuery) (time.Time, time.Time) {
	now := time.Now()

	until := now
	if q.Until != nil {
		until = *q.Until
	}

	since := until.AddDate(0, 0, -defaultSeriesDays+1)
	if q.Since != nil {
		since = *q.Since
	}

	// 归一到"天"的边界，保证补零循环覆盖完整日期
	since = truncateToDay(since)
	until = truncateToDay(until.AddDate(0, 0, 1))

	return since, until
}

// truncateToDay 把时间截断到当天零点（本地时区）。
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// scanUsageLog 把一行数据映射为日志对象。
func scanUsageLog(sc rowScanner) (*model.UsageLog, error) {
	var (
		id               uint64
		userID           uint64
		tokenID          uint64
		channelID        uint64
		channelKeyID     uint64
		modelName        string
		upstreamModel    string
		promptTokens     int
		completionTokens int
		totalTokens      int
		cachedTokens     int
		reasoningTokens  int
		firstTokenMS     int
		tokensPerSecond  float64
		quota            int64
		latencyMS        int
		isStream         int
		statusCode       int
		errMsg           string
		requestID        string
		priceVersion     string
		createdAt        int64
	)

	if err := sc.Scan(&id, &userID, &tokenID, &channelID, &channelKeyID, &modelName, &upstreamModel,
		&promptTokens, &completionTokens, &totalTokens,
		&cachedTokens, &reasoningTokens, &firstTokenMS, &tokensPerSecond,
		&quota, &latencyMS,
		&isStream, &statusCode, &errMsg, &requestID, &priceVersion, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 读取调用日志字段失败: %w", err)
	}

	return &model.UsageLog{
		ID:               id,
		UserID:           userID,
		TokenID:          tokenID,
		ChannelID:        channelID,
		ChannelKeyID:     channelKeyID,
		Model:            modelName,
		UpstreamModel:    upstreamModel,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		CachedTokens:     cachedTokens,
		ReasoningTokens:  reasoningTokens,
		FirstTokenMS:     firstTokenMS,
		TokensPerSecond:  tokensPerSecond,
		Quota:            quota,
		LatencyMS:        latencyMS,
		IsStream:         isStream != 0,
		StatusCode:       statusCode,
		Error:            errMsg,
		RequestID:        requestID,
		PriceVersion:     priceVersion,
		CreatedAt:        time.Unix(createdAt, 0),
	}, nil
}

// ---------------------------------------------------------------------------
// 系统设置仓储
// ---------------------------------------------------------------------------

// settingRepository 是 model.SettingRepository 的 SQL 实现。
type settingRepository struct {
	db *sql.DB
	// dialect：设置表的 UPSERT 语法各方言不同（ON CONFLICT / ON DUPLICATE KEY），故需要它。
	dialect Dialect
}

// NewSettingRepository 创建设置仓储。
func NewSettingRepository(db *sql.DB, dialect Dialect) model.SettingRepository {
	return &settingRepository{db: db, dialect: dialect}
}

// Get 读取单个设置；不存在时返回空字符串与 nil。
func (r *settingRepository) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 未配置不是错误：调用方会回退到默认值
			return "", nil
		}
		return "", fmt.Errorf("store: 读取设置 %s 失败: %w", key, err)
	}
	return value, nil
}

// GetAll 读取全部设置。
func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, fmt.Errorf("store: 读取全部设置失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	values := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: 读取设置项失败: %w", err)
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历设置失败: %w", err)
	}
	return values, nil
}

// Set 写入单个设置（存在则覆盖）。
func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	return r.SetMany(ctx, map[string]string{key: value})
}

// SetMany 在单个事务内批量写入设置。
func (r *settingRepository) SetMany(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启设置事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	upsert := r.dialect.UpsertSettingSQL()
	for key, value := range values {
		// UPSERT：一条语句同时覆盖「插入」与「已存在则更新」，
		// 避免"先查再写"带来的并发窗口（两个请求同时插同一个键会有一个失败）。
		if _, err := tx.ExecContext(ctx, upsert, key, value, now); err != nil {
			return fmt.Errorf("store: 写入设置 %s 失败: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交设置失败: %w", err)
	}
	return nil
}
