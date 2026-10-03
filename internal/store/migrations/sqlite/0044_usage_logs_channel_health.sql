-- 迁移 0044：为「按成功率自动禁用渠道」的统计查询补一个复合索引
--
-- 意图（Why）：
--   本迁移配套「渠道健康检查」后台任务（见 internal/server/channel_health.go）。
--   该任务按固定间隔执行一条聚合查询，口径是"某渠道在最近 N 分钟内"的成功/失败数：
--       WHERE channel_id = ? AND created_at >= ?
--   既有索引里，idx_usage_logs_channel_key 的前缀虽然也是 channel_id，但其第二列是
--   channel_key_id，无法用于 created_at 的时间范围过滤——结果会退化成"把该渠道的
--   全部历史日志都扫一遍再筛时间"，日志表越大越慢，而它是高频写入、持续增长的表。
--   本迁移补一个 (channel_id, created_at) 复合索引，让该查询只扫目标时间窗内的行。
--
-- 设计说明：
--   1) 只加索引、不改任何列与数据，历史数据语义完全不变；
--   2) 用 IF NOT EXISTS：迁移器允许重跑，重复执行必须安全；
--   3) 为什么值得为后台任务建索引：usage_logs 会随调用量线性增长，若每次健康检查
--      都全量扫某渠道的历史日志，几分钟一次的开销会随数据量持续放大；一个精准的
--      复合索引把这个成本压回"只扫窗口内的少量行"。
--
-- 流转（Flow）：
--   渠道健康检查 → UsageLogs.Summary{ChannelID, Since}
--     → SELECT COUNT(...) ... WHERE channel_id = ? AND created_at >= ?
--       → 命中 idx_usage_logs_channel_created（channel_id 等值 + created_at 范围）
--
-- 扩展（Extend）：
--   若将来判定口径变为"按模型"或"按密钥"，为对应列组合另建索引即可，
--   不要修改本文件（历史迁移禁止改动）。

CREATE INDEX IF NOT EXISTS idx_usage_logs_channel_created
    ON usage_logs (channel_id, created_at);
