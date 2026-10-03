-- 迁移 0049：模型测速结果表
--
-- 意图（Why）：
--   「模型测速」（见 internal/relay/speedtest.go）逐渠道 × 模型测首字延迟。
--   结果需要落库而不是只回显一次：模型广场要向用户展示"这个模型现在多快"，
--   后台测速页要能看到"上次测的结果"——这些都要在测速请求结束后继续存在。
--
-- 设计说明：
--   1) 每个渠道 × 模型只保留【最近一次】结果（UNIQUE 约束 + UPSERT）：
--      测速是"当前状态快照"，历史趋势属于另一个需求，不在这里囤积行；
--   2) 建模为"渠道 × 模型"而不是"模型"：同一模型在不同渠道的延迟可能
--      差一个数量级，渠道维度丢失后广场只能显示一个来历不明的数字；
--   3) ok=0 的行也保留（带 message）：它们回答"这个模型在这个渠道上测不了，
--      原因是什么"，与"还没测过"（无行）语义不同；
--   4) 时间戳沿用本库惯例存 Unix 秒。
--
-- 流转（Flow）：
--   POST /api/admin/channels/:id/speedtest → store.UpsertSpeedResult（每模型一行）
--     → 模型广场（handleModelPlaza）按模型聚合各渠道最新结果的 MIN(ttfb)
--     → 后台测速页（/admin/speedtest）展示上次结果
--
-- 扩展（Extend）：
--   若将来要保留历史趋势（如近 7 天延迟曲线），另建新表按时间追加，
--   不要放开本表的 UNIQUE 约束（那会把"最新结果"的读取路径全部破坏）。

CREATE TABLE IF NOT EXISTS model_speed_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id INTEGER NOT NULL,
    -- model 是对外模型名（平台模型 ID）；upstream_model 是实际发给上游的名字（可能被映射改写）。
    model TEXT NOT NULL,
    upstream_model TEXT NOT NULL DEFAULT '',
    ok INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    ttfb_ms INTEGER NOT NULL DEFAULT 0,
    total_ms INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    tested_at INTEGER NOT NULL,
    UNIQUE(channel_id, model)
);

CREATE INDEX IF NOT EXISTS idx_model_speed_results_model
    ON model_speed_results (model);
