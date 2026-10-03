-- 迁移 0046：模型计价规则支持「渠道专用价」
--
-- 意图（Why）：
--   此前的定价只有「分组 × 模型」一个维度（model_prices.group_name × model），
--   同一分组下的同一模型，无论请求最终打到哪条上游渠道，都按同一条规则计费。
--   但真实的多上游运营里，各上游的进价并不相同：
--     · 同一个模型，A 渠道是官方直连、B 渠道是折扣中转，给用户应报不同的价；
--     · 某个渠道进价高，需要对它单独加价，而对其他渠道维持基准价。
--   只按分组定价无法表达这种差异，只能把渠道拆进不同分组——那会连带改变路由
--   （分组决定候选渠道集合），代价过大。本迁移引入「渠道专用价」，把定价的
--   渠道维度与路由的分组维度解耦。
--
-- 语义（务必与 model/model_price.go、store/model_price_repo.go 保持一致）：
--   channel_id = 0（ChannelScopeAll）—— 不限渠道的「分组默认价」，对所有渠道生效；
--   channel_id > 0             —— 仅对该渠道生效的「渠道专用价」。
--   取值优先级：渠道专用价（channel_id 命中当前渠道）优先，无专用价时回退默认价。
--
-- 设计说明：
--   1) 用整数 channel_id 而非渠道名：渠道名可改、可重复风险由人工承担，
--      而 ID 是稳定主键（见 channels.id），做外键语义的关联更可靠。
--   2) 默认 0 且不加外键约束：0 是合法取值（不限渠道），且渠道删除不应连带
--      删除价格规则（价格是运营数据，渠道临时下架后规则应保留）。
--   3) 原唯一索引 (model, group_name) 必须替换为含 channel_id 的三列唯一索引：
--      否则「同一分组、同一模型、不同渠道」的多条专用价会互相冲突而插不进去。
--      升级后默认价（channel_id=0）的"同分组同模型唯一"约束保持不变。
--
-- 流转（Flow）：
--   model_prices.channel_id
--     → store/model_price_repo.go 读写（List 只返回默认价 / ListForPricing 返回默认价+本渠道专用价）
--       → relay.Billing.priceForChannel → model.MatchModelPriceForChannel
--         （先取渠道专用价，取不到再回退 channel_id=0 的默认价）
--
-- 扩展（Extend）：
--   若将来需要「按令牌 / 按用户」再分层定价，请另行追加 NNNN_*.sql，
--   不要修改本文件（历史迁移禁止改动）。

ALTER TABLE model_prices ADD COLUMN channel_id INTEGER NOT NULL DEFAULT 0;

-- 旧的「同分组同模型唯一」约束无法容纳渠道维度，替换为含 channel_id 的三列唯一索引。
DROP INDEX IF EXISTS idx_model_prices_model_group;

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_prices_model_group_channel
    ON model_prices (model, group_name, channel_id);

-- 计费热路径：按分组 + 模型取「默认价 + 指定渠道专用价」。
CREATE INDEX IF NOT EXISTS idx_model_prices_group_model_channel
    ON model_prices (group_name, model, channel_id);
