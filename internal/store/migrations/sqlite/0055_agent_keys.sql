-- 迁移 0055：AI Agent（运维 + 客服）
--
-- 意图（Why）：
--   站长在后台遇到"渠道为什么不转发""这个报错什么意思""价格配错了"这类问题时，
--   唯一能查的地方是数据库或 SSH 里的日志。这两件事都需要技术能力，
--   而站点主人未必具备——"能问一句话就得到答案"是本项目的核心诉求之一。
--
--   两个 agent，两套边界（刻意不合并）：
--     · 运维 agent（owner 站长本人）：带工具，能读写站内数据与配置；
--     · 客服 agent（外部使用者）：无任何工具，纯问答。
--   分开的理由是权限边界不同：把工具接到面向公网的客服上，
--   等于让任何拿到客服 key 的人都能改站长的渠道与价格。
--   宁可多一次选择，也不做运行时判断——运行时判断意味着"某个分支忘了判"的风险。
--
-- 为什么 agent key 独立建表而不是复用 tokens：
--   两者权限边界完全不同（tokens 能调任意模型 API，agent key 只能问 agent）。
--   复用一张表就得在每个鉴权分支上判"这是哪种 key"，
--   漏判一次就是越权。独立表让鉴权只有一种判据：这张表的 key 只认 agent 路由。
--
-- 取值语义：
--   agent_keys.role   ops = 运维 agent（可带工具）；support = 客服（无工具）
--   agent_keys.status 1 启用；2 禁用（禁用后立即失效，不必等 key 自然过期）
--   agent_keys.key_hash SHA-256 摘要（与 tokens/sessions 一致，不存明文；
--                     明文只在生成时返回一次，站长自己保存）
--
-- 为什么不存"用了多少次/最后使用时间"：
--   那属于用量统计，已有 usage_logs 按令牌记录。重复统计两份口径必然对不上，
--   而对不上时没人说得清该信哪个。
--
-- 流转（Flow）：
--   后台生成 key → 明文仅返回一次 → 前端存 localStorage 作为 Bearer 令牌
--   外部调用 → POST /api/agent/chat（ops 走带工具循环；support 直接问答）
--   站长本人 → 后台聊天页（走同一接口，前端带管理员会话）
--
-- 扩展（Extend）：
--   要加"agent 能读某张新表"的能力：在工具白名单里加一项，
--   不要给 agent 开放通用 SQL——那等于把数据库交出去了。
CREATE TABLE IF NOT EXISTS agent_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    role       TEXT    NOT NULL DEFAULT 'support',  -- ops | support
    name       TEXT    NOT NULL DEFAULT '',         -- 站长给这把 key 的备注（如"给小程序用"）
    key_hash   TEXT    NOT NULL,                    -- SHA-256 摘要，绝不存明文
    status     INTEGER NOT NULL DEFAULT 1,          -- 1 启用 / 2 禁用
    created_at INTEGER NOT NULL DEFAULT 0,          -- Unix 秒
    expires_at INTEGER NOT NULL DEFAULT 0           -- 0 = 永不过期
);

-- 鉴权只按 key 摘要查这一个索引，不存在"先查出来再判类型"的过程。
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_keys_key_hash ON agent_keys (key_hash);
-- 后台列表按角色分组展示（ops / support 各一栏）。
CREATE INDEX IF NOT EXISTS idx_agent_keys_role ON agent_keys (role);
