-- 迁移 0041：语料共建计划（模型清单 / 语料样本 / 特殊福利账户）
--
-- 意图（Why）：
--   团队要自研模型，需要真实对话语料。落地方式是把"指定模型"的调用在转发时
--   原文留存一份（含用户请求体与上游返回正文），供站长离线导出、脱敏后作为训练语料。
--
--   三张表各管一件事，刻意分开：
--     corpus_models  —— **采哪些模型**（与用户无关，改清单不用改代码、不用重新部署）
--     corpus_samples —— **采到的原文**（一次成功调用一行）
--     corpus_grants  —— **谁免计费**（少数的特殊福利账户，按"用户×模型"授予）
--
--   采集与计费**彻底解耦**：免费分组的模型照采、收费专线的模型照采，
--   收费的照旧扣钱；只有福利账户在指定模型上跳过计费。
--
-- 采集范围的取舍（重要，改动前务必先看懂）：
--   清单里只应放**会产生对话内容**的生成类模型。库里另有一批嵌入（embed）、
--   内容审核（nemoguard / safety-guard / content-safety）、文档解析（nemotron-parse）、
--   语音翻译（riva-translate）、图像/视频检测类模型——它们的请求体里根本没有对话，
--   收进来只是噪音与存储成本。这条规则不写进代码（代码只认清单），
--   而是靠"清单由人维护 + 备注写清理由"来落实。
--
-- 为什么不复用 usage_logs 存正文：
--   usage_logs 是每天被全表扫描的统计表，正文是 KB 级大字段，塞进去会让
--   "看统计"这种最频繁的查询变成磁盘灾难。两张表按 request_id 关联即可。
--
-- 流转（Flow）：
--   corpus_models  → corpus.Guard 内存快照 → relay 判定"这次要不要采"
--   relay 采集     → corpus_samples 落行（一次成功调用一行）
--   corpus_grants  → corpus.Guard 内存快照 → 鉴权/计费跳过扣费
--   后台导出       → 读 corpus_samples → JSONL 下载 → 站长离线脱敏
--
-- 扩展（Extend）：
--   要支持"某些用户不参与采集"（退出开关）：加一张 corpus_optouts(user_id) 表，
--   在 Guard 里多一个集合即可，本文件三张表的结构无需改动。

-- ── 一、语料模型清单 ────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS corpus_models (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- model 是【对外模型名】（用户实际调用的名字，如 LTZY-CALL/deepseek-v4.1-flash）。
    -- 用对外名而不是上游名：清单是按"用户看得见、点得着的模型"挑选的，
    -- 且同一对外名在不同渠道可能映射到不同上游名。
    model      TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    remark     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_corpus_models_model ON corpus_models (model);

-- ── 二、语料样本（原文）─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS corpus_samples (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    -- request_id 与 usage_logs.request_id 对应，便于"这笔花了多少"与"说了什么"对账。
    -- 唯一索引做成**部分索引**（仅非空值参与）：没有 request_id 的调用不该
    -- 因为"大家都是空串"而互相冲突。
    request_id     TEXT    NOT NULL DEFAULT '',
    user_id        INTEGER NOT NULL DEFAULT 0,
    token_id       INTEGER NOT NULL DEFAULT 0,
    model          TEXT    NOT NULL DEFAULT '',
    upstream_model TEXT    NOT NULL DEFAULT '',
    channel_id     INTEGER NOT NULL DEFAULT 0,
    channel_key_id INTEGER NOT NULL DEFAULT 0,
    is_stream      INTEGER NOT NULL DEFAULT 0,
    status_code    INTEGER NOT NULL DEFAULT 0,
    -- 请求原文（JSON 文本）与上游返回正文原文（非流式 JSON；流式为原始 SSE 文本）。
    -- 刻意存【原文】：脱敏与匿名化由站长离线完成，站内不做任何改写。
    request_body   TEXT    NOT NULL DEFAULT '',
    response_body  TEXT    NOT NULL DEFAULT '',
    -- 原始字节数：被截断时据此可知"丢了多少"，避免静默的不完整。
    request_bytes  INTEGER NOT NULL DEFAULT 0,
    response_bytes INTEGER NOT NULL DEFAULT 0,
    -- 1 = 超过单次上限被截断；1 = 客户端中途断开、只采到一部分。
    truncated      INTEGER NOT NULL DEFAULT 0,
    incomplete     INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_corpus_samples_request
    ON corpus_samples (request_id) WHERE request_id <> '';
-- 按模型筛选导出。
CREATE INDEX IF NOT EXISTS idx_corpus_samples_model_created
    ON corpus_samples (model, created_at);
-- 按期清理（导出后删除）与统计。
CREATE INDEX IF NOT EXISTS idx_corpus_samples_created
    ON corpus_samples (created_at);

-- ── 三、特殊福利账户（免计费资格）────────────────────────────────
CREATE TABLE IF NOT EXISTS corpus_grants (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    -- model 同样是对外模型名：资格按"用户 × 模型"授予，只免这一个模型。
    model       TEXT    NOT NULL,
    -- 1 = 该用户调用该模型不计费（不扣用户额度、不扣令牌额度）。
    free_access INTEGER NOT NULL DEFAULT 1,
    -- 1 = 生效；2 = 已撤销（保留行以便追溯"曾经授过什么"）。
    status      INTEGER NOT NULL DEFAULT 1,
    remark      TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_corpus_grants_user_model
    ON corpus_grants (user_id, model);
