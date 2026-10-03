-- 迁移 0053：渠道探针历史
--
-- 意图（Why）：
--   0050 只把"最近一次测活"写在渠道行上（latency_ms / last_test_*），
--   那是一个【当前值】：它能回答"这个渠道现在多少毫秒"，
--   但回答不了站长真正关心的第二个问题——"它是什么时候开始变慢的"。
--   没有历史就只能看到一条抖动的曲线里恰好采到的那一点：
--     · 上游从 300ms 劣化到 3s，看板上仍是"300ms"（直到下一次劣化完成）；
--     · 偶发一次 5s 尖峰被下一次正常值抹平，事后无从复盘；
--     · 值班交接时无法回答"这个渠道昨晚还好好的吗"。
--   本表把每次巡检的结论追加成一行，让"劣化"从一个猜测变成一条可查的时间线。
--
-- 为什么是"追加一行"而不是"更新渠道行"：
--   更新行只能存住最新一次，趋势信息必然丢失；两种写法的代价几乎相同
--   （巡检每轮每渠道一行，量级可控），所以只做加法，不动 0050 的既有语义。
--
-- 与渠道行（当前值）的关系：
--   channels.latency_ms          = 现在多少毫秒（路由与后台卡片用）
--   channel_probe_logs.latency_ms = 那一次是多少毫秒（趋势与复盘用）
--   两者不一致是正常的：巡检期间可能又写了一行，渠道行永远追着最新那次。
--
-- 取值语义：
--   channel_id  渠道主键
--   at          探测时刻（Unix 秒）；不用自增 id 排序，避免"探测乱序"时序错乱
--   ok          1 = 至少一个模型返回 2xx
--   latency_ms  本次耗时（毫秒）；失败时也可能 >0（收到了响应，只是非 2xx）
--   status_code 上游 HTTP 状态码；0 = 网络层失败（DNS/连接/超时）
--   model       实际探测命中的模型名
--   message     失败原因摘要（成功时为空串）
--
-- 为什么不存 message 的全文：
--   巡检失败时上游可能回一整页 HTML 错误页，全文入库会让这张表迅速膨胀，
--   而巡检本来就只把 message 用于生成一句处置提示。摘要足够，够用即止。
--
-- 为什么不记请求/响应体：
--   这是"渠道还活着吗"的健康信号，不是内容审计；内容审计已有 usage_logs。
--   在这张表里复制请求体会让最频繁写入的表变成最大的表。
--
-- 流转（Flow）：
--   巡检 probeOneChannel → Channels.RecordProbeResult（更新渠道行的当前值）
--                        → ChannelProbeLogs.Append（追加一行历史）
--   前端看板 → GET /admin/channels/health（聚合成功率）
--             → GET /admin/channel-probes（时间线明细/曲线）
--   保留期清理 → Retention.Purge 按 probe_log_days 删历史行
--
-- 扩展（Extend）：
--   要画"按小时平均延迟"这类降采样曲线：在本表上加 (channel_id, at) 索引后
--   按区间 GROUP BY 聚合即可，无需另建汇总表——行数量级（渠道数 × 每天 96 轮）
--   在索引上完全扛得住。
--   要接"抖动率/连续失败次数"：查询时用 SQL 窗口函数或应用层计数，
--   同样不必预计算落表——预计算表一旦与本表口径不一致，排查起来比慢查询更痛苦。
CREATE TABLE IF NOT EXISTS channel_probe_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id  INTEGER NOT NULL DEFAULT 0,
    at          INTEGER NOT NULL DEFAULT 0,
    ok          INTEGER NOT NULL DEFAULT 0,
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    model       TEXT    NOT NULL DEFAULT '',
    message     TEXT    NOT NULL DEFAULT ''
);

-- 覆盖两类访问：按渠道取时间线（chart 与详情页），以及按时间取全站时间线。
-- 单列索引不足以同时服务这两种查询方向，故建两个单列索引而非一个组合索引——
-- 组合索引 (channel_id, at) 会让"按时间取全站最近记录"退化成全表扫描，
-- 而那正是保留期清理与看板"最近失败"列表要走的路径。
CREATE INDEX IF NOT EXISTS idx_channel_probe_logs_channel_at
    ON channel_probe_logs (channel_id, at);
CREATE INDEX IF NOT EXISTS idx_channel_probe_logs_at
    ON channel_probe_logs (at);
