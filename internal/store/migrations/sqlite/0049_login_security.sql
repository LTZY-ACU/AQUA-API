-- 迁移 0049：登录安全字段（连续失败锁定 + 最近登录来源 + 会话来源与二次验证）
--
-- 意图（Why）：
--   此前登录安全只有"进程内 IP/账号限流"三道闸：重启清零、不落库、不跨实例，
--   撞库脚本换一个账号就能继续试，也无从回答"谁在什么时候从哪个 IP 登进来"。
--   本迁移把三类事实落到库里：
--     1) users.failed_logins / locked_until —— 连续失败计数与锁定截止（账号级，落库）
--     2) users.last_login_at / last_login_ip —— 最近一次成功登录的来源（异地提醒的比较基准）
--     3) sessions.ip / user_agent —— 会话签发时的来源（请求级异常识别）
--     4) sessions.reauth_at —— 本会话最近一次"重新输密码"的时刻（敏感操作二次验证）
--
-- 取值语义：
--   failed_logins  0 = 无连续失败；成功登录即清零
--   locked_until   0 = 未锁定；> now = 处于锁定中（到点自动解锁，无需人工干预）
--   last_login_at  0 = 从未登录过（此时不发异地提醒，避免首登骚扰）
--   last_login_ip  '' = 未知（同上）
--   sessions.ip    '' = 未知（老会话迁移后即为该值，比对时按"未知=不判异地"处理）
--   reauth_at      0 = 本会话从未验证过密码
--
-- 为什么放 users/sessions 而不是独立表：
--   三者都是"用户/会话的属性"，与主体同生命周期，拆表只会多一次连接；
--   锁定查询发生在登录路径上（按 id 取用户后立刻判），与用户行同行最省一次读。
--
-- 为什么锁定时间用绝对截止而不是"失败次数 + 重置标记"：
--   解锁判定只需 `locked_until <= now` 一个比较，天然自愈、无定时任务；
--   而"到点后手动清零"需要额外任务兜底，漏一次就等于永久锁死用户。
--
-- 流转（Flow）：
--   users.failed_logins/locked_until
--     → server.handleLogin / handleAdminLogin / handleEmailLogin（失败+1、到阈值锁定、成功清零）
--   users.last_login_at/last_login_ip
--     → server.issueSession（成功时写入；与上次不同即"异地/新设备"）
--   sessions.ip/user_agent
--     → server.issueSession 写入 → middleware.sessionAuth 读取比对（只标记不阻断）
--   sessions.reauth_at
--     → server 二次验证接口写入 → 敏感操作中间件按窗口判定
--
-- 扩展（Extend）：
--   调整锁定阈值/时长：改 model 中的常量（不改表结构）；
--   要"锁定期间也通知站长"：在 handleLogin 锁定分支挂邮件/审计，不动本迁移。
--   新增会话级安全字段时：本文件再加一条 ALTER + 同步 model.Session / sessionColumns /
--   scanSession / sessionRepository.Create 四处（见 user_repo.go 头部约定）。

-- 用户侧：登录失败计数与锁定截止
--
-- 序号说明（合并时重排）：
--   本脚本原为 0043 号，与另一条开发线上已发布的同名序号撞车。
--   迁移版本号必须全局唯一（store.loadMigrations 发现重复会直接让进程启动失败），
--   且已发布的序号不得改动（线上库的 schema_migrations 已经记过号），
--   因此【保留对方已发布的 0043–0048，把本条顺延到 0049 之后】。

ALTER TABLE users ADD COLUMN failed_logins  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until   INTEGER NOT NULL DEFAULT 0;
-- 用户侧：最近一次成功登录的来源（异地提醒基准）
ALTER TABLE users ADD COLUMN last_login_at  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN last_login_ip  TEXT    NOT NULL DEFAULT '';

-- 会话侧：签发时的来源（IP + UA，UA 截断后入库）
ALTER TABLE sessions ADD COLUMN ip         TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN user_agent TEXT    NOT NULL DEFAULT '';
-- 会话侧：最近一次"重新验证密码"的时刻（敏感操作二次验证的时效基准）
ALTER TABLE sessions ADD COLUMN reauth_at  INTEGER NOT NULL DEFAULT 0;
