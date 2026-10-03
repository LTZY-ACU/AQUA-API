-- 迁移 0048：订阅账号的「次额度窗口」落库（channel_keys 新增 4 列）
--
-- 背景（Why）：
--   上游（ChatGPT / Codex）对同一账号给出两条【互相独立】的额度线：
--     · primary   窗口 —— 5 小时用量（此前已落库于 quota_used_percent / quota_reset_at）；
--     · secondary 窗口 —— 每周用量（此前【只解析不落库】，探测完就被丢弃）。
--   丢掉次窗口的后果不是"少一个数字"，而是站长在周额度将满时毫无预警——
--   表现是"明明这两天没怎么用，账号却突然全满"，且无法解释原因。
--   因此本次把次窗口一并落库，供后台同时渲染"5 小时 / 每周"两条进度条。
--
-- 与既有列的对应关系（语义一一对照，勿混用）：
--   quota_used_percent            ↔ quota_secondary_used_percent   （已用百分比，-1=未探测）
--   quota_reset_at                ↔ quota_secondary_reset_at       （重置时间，Unix 秒，0=未知）
--   （新增）quota_primary_window_seconds / quota_secondary_window_seconds（窗口时长，秒，0=未知）
--
-- 兼容性（升级后行为必须逐字不变）：
--   1) 全部为 NOT NULL + 常量默认值，SQLite 允许对已有表直接 ADD COLUMN；
--   2) 存量行一次性获得：次窗口百分比 = -1（未探测）、重置时间 = 0、两个窗口秒数 = 0；
--      界面据此显示"未查询"，而不是把 0 当成"完全没用"；
--   3) 调度判定【不变】：model.ChannelKey.QuotaExhausted 仍只读主窗口
--      （quota_used_percent + quota_reset_at），次窗口纯粹是展示/观测信息，
--      不参与任何"是否跳过该账号"的决定。
--
-- 流转（Flow）：
--   relay.QueryCodexQuota（解析 primary + secondary）
--     → server.handleProbeChannelKeyQuota（组装 model.QuotaWindows 一次写入）
--       → store/channel_key_repo.go 的 UpdateQuota / scanChannelKey
--         → server.channelKeyDTO 透出 → 前端 ChannelKeyPool 双进度条
--
-- 扩展（Extend）：
--   上游若再拆出第三个额度窗口，请在本目录追加 NNNN_*.sql 加列，
--   并同步 model.QuotaWindows / 仓储列清单 / DTO / 前端四处（缺一处即静默丢数据）。

-- 次窗口已用百分比。与主窗口同口径：-1 专门表示"尚未探测"，
-- 与真实的 0%（完全没用）区分开，避免界面把"没查过"显示成"额度充足"。
ALTER TABLE channel_keys ADD COLUMN quota_secondary_used_percent INTEGER NOT NULL DEFAULT -1;

-- 次窗口重置时间（Unix 秒）；0 表示未知。
ALTER TABLE channel_keys ADD COLUMN quota_secondary_reset_at INTEGER NOT NULL DEFAULT 0;

-- 主窗口时长（秒）；0 表示上游未提供。用于界面把进度条标注成"5 小时窗口"而非硬编码文案。
ALTER TABLE channel_keys ADD COLUMN quota_primary_window_seconds INTEGER NOT NULL DEFAULT 0;

-- 次窗口时长（秒）；0 表示上游未提供。用于界面把进度条标注成"每周窗口"。
ALTER TABLE channel_keys ADD COLUMN quota_secondary_window_seconds INTEGER NOT NULL DEFAULT 0;
