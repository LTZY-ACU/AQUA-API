-- 迁移 0045：第三方账号绑定（本站账号 ↔ 外部平台账号）
--
-- 意图（Why）：
--
--   支持"用 QIU 科技账号登录"就必须记住"哪个本站账号对应哪个外部账号"，
--   否则每次登录都会新建一个号：用户第二次登录进来是个空账号，
--   额度、令牌、调用记录全都不见了——体验灾难，也是数据污染源。
--
--   为什么单独建表而不是在 users 上加 qiu_id 一列：
--     1) 一个用户将来可能绑定多个外部平台（QIU / 其它），加列意味着每接一家改一次表；
--     2) 外部身份是"一对多 belongs to"关系，外键表天然表达，且删除关系不必动用户行；
--     3) provider + external_id 的唯一约束由数据库保证，
--        比"在 users 上给每一家加一个唯一索引"干净得多。
--
-- 取值语义：
--   provider          外部平台标识（当前取值 "qiu"；小写、非空）
--   external_id       外部平台的稳定用户 id（QIU 的 user.id）——唯一身份依据
--   external_username / nickname
--                     展示用快照。刻意【只快照不依赖】：外部昵称随时可改，
--                     登录时的匹配只认 external_id，改昵称不会导致错登别人的号。
--   created_at        绑定时间（Unix 秒）
--
-- 安全约定（重要）：
--   匹配【永远只用 external_id】，绝不用昵称或外部用户名去猜本地账号。
--   否则攻击者只要把自己在 QIU 上的昵称改成别人的本站用户名，就能接管对方账号。
--
-- 流转（Flow）：
--   server.handleQIULoginStart  → 调外部 /startlogin 取 task_id
--   server.handleQIULoginStatus → 轮询外部 /readlogin/{task_id}
--     → 拿到 {id, username, nickname}
--     → GetByExternalID("qiu", id)
--         ├ 命中：签发该本地用户的会话
--         └ 未命中：按当前的注册开关决定是否新建账号 → 建号 → Bind
--   server.handleQIUBind（已登录用户主动绑定）
--
-- 扩展（Extend）：
--   接入第二个外部平台：复用本表，provider 换个标识即可，无需改表结构；
--   需要"解除绑定"：加一条 DELETE 即可（注意保留至少一个登录方式的可恢复性）。
CREATE TABLE IF NOT EXISTS user_external_accounts (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL,
    provider           TEXT    NOT NULL DEFAULT '',
    external_id        TEXT    NOT NULL DEFAULT '',
    external_username  TEXT    NOT NULL DEFAULT '',
    nickname           TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL DEFAULT 0
);

-- 同一平台同一外部 id 只能绑定一次：这是整个登录流程正确性的最后一道闸，
-- 靠应用层判断会漏，必须由数据库兜住。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_external_unique
    ON user_external_accounts (provider, external_id);

-- 按本站用户查他绑了哪些外部账号（门户页展示与解绑都需要）。
CREATE INDEX IF NOT EXISTS idx_user_external_user ON user_external_accounts (user_id);
