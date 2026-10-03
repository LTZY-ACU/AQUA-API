<div align="center">

<img src="favicon.ico" width="88" alt="LTZY-API" />

# LTZY-API

**把你的所有 AI 上游，收进一个入口。**

自托管 LLM API 网关 · AI 用量管理系统

Self-hosted LLM Gateway · OpenAI-compatible API

![License](assets/badges/license.svg)
![Go](assets/badges/go.svg)
![CGO](assets/badges/cgo.svg)
![Deploy](assets/badges/deploy.svg)
![Database](assets/badges/database.svg)
![Web](assets/badges/web.svg)
![i18n](assets/badges/i18n.svg)
![Platform](assets/badges/platform.svg)

[简体中文](README.md) · [English](README.en.md) · [Français](README.fr.md) · [Русский](README.ru.md) · [Español](README.es.md) · [العربية](README.ar.md)

> 本项目前身为 **AQUA-API**，2026-10 起更名为 **LTZY-API**；旧地址自动重定向，无需更新收藏。

</div>

---

## 官方地址

| 渠道 | 地址 |
| --- | --- |
| 官方网站（在线演示） | https://ltzy.top |
| 代码仓库 | https://github.com/LTZY-ACU/LTZY-API |
| 提交 Issue | https://github.com/LTZY-ACU/LTZY-API/issues |

> **本仓库是官方地址的唯一权威来源。** 域名如有变更，会先在这里更新，再同步到其它任何地方。
> 因此，收藏本仓库比收藏一个域名更可靠。

### 防伪提醒

- 本项目**不提供、也未授权**任何「代充」「代运营」「官方合租」服务。服务端代码完全开源
  （[MIT 许可证](LICENSE)），任何人都可以自建 —— **能跑起来 ≠ 是官方**。
- 只认上表中的地址。其它域名即使界面一模一样，也与本项目无关。
- 官方不会私聊索要账号密码、支付口令或验证码。
- 使用本项目即视为接受[《使用者须知与免责声明》](DISCLAIMER.md)；品牌边界见[《品牌与商标声明》](TRADEMARK.md)。
- 参与开发前请先读[《贡献指南》](CONTRIBUTING.md)（Fork + PR 模式，`main` 分支受保护）。

### 链接打不开？

这类站点被社交软件（QQ／微信等）误报拦截是常见现象。遇到时：

1. 换浏览器，或切换网络（移动数据 ↔ 家庭宽带）再试；
2. **分享本仓库地址而不是裸域名** —— 代码托管平台的链接被误拦的概率低得多，
   而且对方能从仓库里自行确认最新官网地址；
3. 若确认是误拦，按平台提示提交申诉即可。

> 遇到问题欢迎加入交流群：**QQ 群 1103667832**（站点首页右上角「加入群聊」入口）。

---

## 目录

- [官方地址](#官方地址)
- [免责声明](#免责声明)
- [这是什么](#这是什么)
- [为什么选择](#为什么选择)
- [功能总览](#功能总览)
- [核心特性](#核心特性)
- [系统架构](#系统架构)
- [核心数据模型](#核心数据模型)
- [请求的完整生命周期](#请求的完整生命周期)
- [支持的协议与上游](#支持的协议与上游)
- [接口一览](#接口一览)
- [权限与角色](#权限与角色)
- [计费与账务细则](#计费与账务细则)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [配置](#配置)
- [接入示例](#接入示例)
- [运营指南](#运营指南)
- [国际化](#国际化)
- [安全](#安全)
- [部署与容量](#部署与容量)
- [常见问题](#常见问题)
- [术语表](#术语表)
- [路线图](#路线图)
- [开发](#开发)
- [参与贡献](#参与贡献)
- [许可证](#许可证)

---

## 免责声明

**使用本项目前，请先阅读[《使用者须知与免责声明》](DISCLAIMER.md)。**
本项目仅供合法的技术研究与内部管理使用；使用者须遵守所在地区法律法规
及所接入上游服务的条款。作者不对使用本项目产生的任何损失承担责任。

品牌与商标的边界见[《品牌与商标声明》](TRADEMARK.md)。

---

## 这是什么

LTZY-API 是一个**自托管的 LLM API 网关**，同时是一套 **AI 用量管理系统**。

你手上的上游通常是一团互不兼容的东西：OpenAI 官方 Key、Azure、Claude、Gemini、各家云厂商、
各类 OpenAI 兼容服务、订阅来的账号（Claude / Codex / Gemini），以及本地跑的 Ollama / vLLM。
而你的下游是各种应用：Claude Code、Codex CLI、Cursor、自研 App、脚本、插件。

LTZY-API 站在中间，把这一团整理成**一个入口、一套协议、一本清楚的账**。

```mermaid
flowchart LR
    subgraph C["下游客户端"]
        C1["Claude Code"]
        C2["Codex CLI"]
        C3["Cursor"]
        C4["自研应用 / 脚本 / 插件"]
    end

    LTZY["LTZY-API<br/>统一协议 · 智能调度 · 精确计费<br/>分组 · 凭据池 · 运营后台"]

    subgraph U["上游服务"]
        U1["OpenAI / Azure"]
        U2["Anthropic / Gemini"]
        U3["云厂商 / 兼容服务"]
        U4["订阅账号（OAuth）"]
        U5["本地 Ollama / vLLM"]
    end

    C1 --> LTZY
    C2 --> LTZY
    C3 --> LTZY
    C4 --> LTZY
    LTZY --> U1
    LTZY --> U2
    LTZY --> U3
    LTZY --> U4
    LTZY --> U5
```

它解决的核心问题只有三个词：**统一**（协议与入口）、**可靠**（故障自动避让）、**可算账**（每一分钱有据可查）。

---

## 为什么选择

同类网关不少，缺的是**能安心托付账目**的那一个。下面每一条都是踩过坑之后的设计。

| 关注点 | 常见做法 | LTZY-API |
| --- | --- | --- |
| 上游密钥 | 明文存库，后台可看回明文 | AES-256-GCM 加密落库，主密钥只从环境变量注入；后台被攻破也导不出明文 |
| 加密主密钥 | 一并写进配置文件 | 配置文件中的同名字段直接被忽略，只能走环境变量，不会随仓库泄露 |
| 上游故障 | 连续失败即永久禁用，池子越用越小 | 失败分类 + 冷却半开：限流只是临时避让、到期自动恢复；仅在上游明确声明凭据吊销时才摘除 |
| 重试策略 | 一律重试，或一律不重试 | 按失败类型分流：429 冷却换 key、5xx 换渠道、401/403 长冷却、内容审核换模型、空 200 降级，并尊重上游 `Retry-After` |
| 限流与并发 | 全局一个阈值，一限全限 | 按凭据独立计（权重 / 优先级 / 每分钟上限 / 在途数）；分组可设每分钟请求套餐 |
| 额度 | 请求前后各查一次，并发下会透支 | 预扣 → 结算 → 退还，可用额度 = 额度 − 已用 − 在途，并发也扣不出负数 |
| 周期预算 | 只有总额度，花超才知道 | 令牌级滚动窗口预算（日 / 周 / 月），窗口内超限自动熔断 |
| 流式计费 | 只读响应前若干字节，长回答计 0 费 | 增量解析 SSE，`usage` 出现在流的最后一帧也能拿到 |
| 协议 | 只做 OpenAI 兼容 | 下游 OpenAI / Anthropic / Gemini 三套协议齐备，上游按渠道类型选适配器 |
| 人群区分 | 密钥即权限，无法区分免费与付费 | 分组决定可用渠道与价格，密钥可选分组：同一份上游，免费人群与付费人群各走各的账 |
| 代理拿货 | 靠人工记折扣，账目对不上 | 代理分组：广场按代理身份展示「划线原价 + 折后价」，折扣即分组倍率，广场价与实际扣费同源 |
| 定价灵活性 | 一个模型一个价，改不动 | 价格按「模型 × 分组 × 渠道」三维可配；每条账记录定价版本快照，改价后旧账仍可复算 |
| 成本可见 | 只有收入，不知道赚没赚 | 真实成本对账报表：按分组 / 渠道 / 模型聚合收入、成本与毛利，进货价按量按次都能核算 |
| 运维 | 渠道挂了靠人盯 | 渠道健康面板 + 按成功率自动停用；管理后台支持 CIDR 白名单收紧访问 |
| 合规 | 一句免责声明了事 | 协议声明章节 + 模型徽标 + 首访确认弹窗 + 充值页提示的全站合规提示体系 |
| 部署 | 要装数据库、Redis、编译环境 | 单二进制 + SQLite，前端已内嵌，零 CGO，不需要 gcc |

---

## 功能总览

一张表看全能力边界（详细说明见后续章节）。

| 模块 | 能力 |
| --- | --- |
| 协议接入 | OpenAI 兼容 · Anthropic · Gemini · Codex / Responses，四类入站可同时开启 |
| 上游适配 | OpenAI 兼容 · Azure OpenAI · Anthropic · Gemini · Codex · 订阅账号；目录登记 80 种渠道类型，39 种已实现 |
| 渠道路由 | 分组路由 · 渠道级模型分叉 · 凭据级分组与模型分叉 · 时段规则 · 渠道级熔断跳过 |
| 凭据调度 | 顺序 / 轮询 / 加权随机 / 最久未用 / 最少在途；权重 · 优先级 · 每分钟上限 · 在途数 · 冷却截止 |
| 故障处理 | 失败分类重试 · 渠道×模型级冷却 · 指数退避 · 尊重 `Retry-After` · 会话粘性 · 半开恢复 |
| 计费 | 按量（输入 / 输出 / 缓存三价）· 按次 · 通配匹配 · 渠道专属价 · 分组倍率 · 定价版本快照 |
| 额度与风控 | 预扣 / 结算 / 退还三段式 · 令牌与用户两级额度 · 令牌周期预算（日 / 周 / 月）· 幂等请求台账 |
| 分组与代理 | 分组实体 · 计费倍率 · 解锁门槛 · 分组 RPM · 仅后台分发 · 代理拿货档与广场折后价 |
| 支付与账务 | 人工 / 易支付 / Stripe / 支付宝官方 / 微信支付官方 · 兑换码 · 订单幂等入账 · 迟到支付补记 |
| 用户体系 | 注册 · 邮箱验证码登录与重置密码 · 会话 Cookie · 限时试用额 · 邀请返利 · 每日签到 |
| 运营后台 | 模型广场 · 渠道与密钥池 · 分组 · 价格 · 令牌 · 用户 · 订单 · 兑换码 · 调用日志 · 审计 |
| 增值能力 | 异步任务 · 语料共建 · 群发邮件 · 站点公告 · 敏感词 · 模型映射 · OAuth 订阅账号 |
| 可观测 | 渠道健康面板 · 重试率告警 · 成本对账报表 · 路由响应头 · 运行维护概览与备份 |
| 安全 | 密钥加密落库 · 日志脱敏 · CIDR 白名单 · 明文取回审计 · 越权防护 |
| 国际化 | 服务端与前端各 6 种语言 · 本文档提供联合国六种官方语言版本 |
| 部署 | 单二进制 · Docker · systemd · 前端内嵌 · SQLite 免运维 |

---

## 核心特性

### 网关与转发

- **下游三协议**：OpenAI 兼容（`/v1/chat/completions`、`/v1/models`、`/v1/embeddings`）、
  Anthropic（`/v1/messages`）、Gemini（`/v1beta`）—— 三套入站协议均可直接接管对应客户端
- **上游适配器**：OpenAI 兼容、Azure OpenAI（部署名 + api-version）、Anthropic、Gemini、
  Codex / Responses，以及各类订阅账号
- **统一中间表示**：内部全部收敛到 OpenAI 协议（N×1），新增上游只写「进」，新增下游只写「出」
- **流式双向转换**：上游 Anthropic / Gemini 的 SSE 事件 ↔ OpenAI `chat.completion.chunk`，含工具调用
- **300 秒上游超时**：大模型长回答不会被掐断
- **错误原样透传**：上游真实错误（含 RFC7807 `detail`、OpenAI `error.message`）直接回传，不吞错
- **可观测路由响应头**：`X-Routed-Via`（实际命中渠道）、`X-Fallback-Attempts`（降级尝试次数）、
  `X-Upstream`（实际上游模型名），排查问题不必抓包

### 凭据池与智能调度

- **五种策略**：顺序 / 轮询 / 加权随机 / 最久未用 / 最少在途（默认），渠道级可切换
- **凭据级参数**：权重、优先级、每分钟上限、在途数、冷却截止，后台逐把可调
- **失败分类驱动重试**：
  - `429`：换密钥并把该密钥置入短冷却（指数退避），尊重上游 `Retry-After`
  - `5xx` / 超时：换渠道重试
  - `401 / 403 / 402`：长冷却（不轻易摘除，避免瞬时风控误杀好密钥）
  - 内容审核拦截：换模型
  - `200` 但内容为空：视为失败降级
- **渠道 × 模型级冷却**：失败只冷却「该渠道 × 该模型」，不牵连同渠道其它模型
- **会话粘性**：同一会话（`X-Session-Id`）固定同一把凭据以提升上游缓存命中率，目标失效自动降级
- **渠道级熔断**：某渠道全部凭据余额 / 额度耗尽时，路由主动跳过并记日志，而不是选中后再失败
- **多种录入**：单条 / 批量粘贴 / 多合一

### 计费与账务

- **口径**：`额度 = (输入 Token × 输入价 + 输出 Token × 输出价) / 1,000,000`，另支持按次计价
- **价格规则**：按模型名或通配模式匹配，可挂到分组，也可为特定渠道配专属价
- **缓存价分离**：命中上游缓存的 token 按独立单价计费（未配置时回退输入价）
- **额度安全**：预扣 + 结算 + 退还三段式，幂等台账保证「至多入账一次」
- **定价版本快照**：每条调用日志记录当时的计价规则版本，改价后旧账仍可按旧价复算
- **周期预算**：令牌可设「每周期最多花 N 额度」，窗口到期惰性重置，不依赖定时任务
- **支付通道**：人工确认 / 易支付 / Stripe / 支付宝官方（RSA2）/ 微信支付官方（APIv3 + 平台证书验签 + AES-GCM）
- **订单账务**：回调验签、幂等入账、迟到支付补记、人工补单与关单
- **兑换码**：批量生成；并发兑换是单事务原子扣减，10 个并发抢同一码只会成功一次

### 分组、定价与代理体系

- **分组是一等实体**：展示名、计费倍率、解锁门槛、每分钟请求上限
- **仅后台分发的分组**：批发价 / 代理档对普通用户完全不可见，只能由管理员指派
- **代理拿货档**：被指派到代理分组的用户，在模型广场看到的是他自己那一档的模型与价格，
  并以「原价划线 + 橙色折后价」对照展示
- **广场价 = 实际扣费**：代理广场价与计费取自同一套价格规则
- **分组引用统计**：删除分组前告知影响多少渠道与价格规则
- **公开定价试算**：`GET /api/models/quote`（无需登录），输入 token 数即返回预估费用

### 运营与后台

- **模型广场**：分面筛选 + 分面计数联动 + 搜索 + 排序 + 卡片与列表双视图 +
  详情弹窗（价格表、生效时间、可直接运行的 cURL、费用试算器、**实时 tokens/s 与连通性测试**）
- **渠道管理**：增删改、连通性测活、密钥池抽屉、一键拉取上游模型列表、上游进价核算（按量 / 按次）
- **渠道健康面板**：成功率、冷却中密钥数、剩余余额一屏尽览；支持按成功率自动停用
- **财务对账报表**：按分组 / 渠道 / 模型聚合收入、成本、毛利与毛利率
- **用量排行榜**：付费榜 / 免费榜各 Top 20（详见下文「用量排行榜」）
- **重试率告警**：按折扣分组统计 `r = 上游调用次数 / 计费请求次数`，越过保本线即告警
- **令牌**：额度 / 过期 / 模型白名单 / 所属分组 / 周期预算 / 明文受审计找回
- **用户体系**：注册、邮箱验证码登录与重置密码、限时试用额发放与到期回收
- **邀请与签到**：邀请码、注册与充值奖励台账、每日签到
- **站点公告 / 操作审计 / 敏感词 / SMTP / 异步任务 / 群发邮件 / 语料共建**
- **其余后台**：模型元数据与映射、OAuth 订阅账号、运行维护概览与数据库备份

### 用量排行榜

概览页图表下方的排行榜，回答「谁在用、用了多少」。两个榜单各取前 20 名：

- **付费榜 / 免费榜**：按是否存在已支付订单（`payment_orders.status = paid`）拆分。
  混排会让免费榜永远被付费用户占据——付费用户的使用强度通常不是一个量级。
- **综合分数** = 50% × 请求数（榜内归一） + 50% × Token 消耗（榜内归一）。
  归一化而非直接相加：一次长回答可能消耗数万 token 而请求数只有 1，
  直接相加会被 token 单方面主导，榜单失去区分度。
- **每行展示**：哈希配色的字母头像（同一用户颜色稳定）、账号 ID、请求数、Token、
  分数进度条、平均请求耗时、峰值并发。
- **自己所在行高亮**并标注「我」，未进前 20 时在榜单头部显示自己的名次。
- **管理员**可通过「完整榜单」开关查看全部名次（`?all=1`，接口不截断）。

峰值并发用差分事件扫描估算：把每条成功请求视为区间 `[created_at − latency, created_at]`，
按时间排序后从左到右累加「开始 +1 / 结束 −1」，过程中的最大值即峰值——
它反映「最忙的瞬间同时有多少请求在途」，比单纯的 RPS 更贴近真实并行压力。

口径与 `usage_logs` 完全一致（只统计成功请求），与概览页、仪表盘同源，
不存在第二套统计。

### 前端与主题

- **三套主题**：浅色 / 深色 / 深蓝色，随时切换，偏好本地持久化
- **六语言界面**：简体中文、English、Français、Русский、Español、العربية（含 RTL 布局）
- **移动端**：底部导航、表格自动降级为卡片、安全区适配、弹窗底部弹出
- **在线使用界面**（`/console/playground`）：直接调用站点当前全部可用模型，模型列表每 30 秒
  自动刷新（渠道上下线、令牌权限变化即时反映），流式输出并实时显示本轮耗时与首字延迟
- **用量排行榜**（概览页图表下方）：付费榜 / 免费榜各 Top 20；综合分数 = 请求数 50% + Token 消耗 50%
  （榜内归一化），展示平均请求时间与峰值并发；自己所在行高亮并标注「我」，管理员可切换完整榜单
- **模型详情实时指标**：输出速率（tokens/s）、平均耗时、首字延迟 TTFB 每 5 秒刷新；内置连通性
  测试——发起一次真实 agent 调用（请求发起 → 鉴权 → 流式返回），读到首个响应即断开，
  返回连通结果与首字延迟，token 消耗可忽略

### 在线使用界面

`/console/playground` 是浏览器里的模型试玩台：粘贴访问令牌即可调用站点当前**全部可用模型**。

- **模型列表实时更新**：以公开模型广场为基底，每 30 秒轮询 `GET /v1/models`
  （OpenAI 兼容端点，带当前令牌鉴权）补充「此刻真能调」的模型。
  渠道上线 / 下线、令牌模型白名单调整，都会在一个轮询周期内反映到下拉框。
- **调用状态实时可见**：请求发起后立即进入「正在生成…」状态并可随时停止；
  流式输出过程中页面底部实时显示本轮**首字延迟（TTFB）**与**总耗时**，
  完成后定格——回答「这次调用到底发生了什么」而不只是给一段文本。
- **与真实调用完全同路径**：走 `/v1/chat/completions`，因此鉴权、分组路由、
  密钥轮询、额度预扣与计费都与生产流量一致，试玩结果可信。

令牌只保存在页面内存（不写 localStorage），刷新即失效。

### 模型实时指标与连通性测试

模型详情弹窗底部有两块实时信息，帮助判断「这个模型现在能不能用、好不好用」：

| 指标 | 来源 | 刷新 |
| --- | --- | --- |
| 输出速率（tokens/s） | `usage_logs.tokens_per_second` 近 15 分钟均值 | 5 秒 |
| 平均耗时 | `usage_logs.latency_ms` 近 15 分钟均值（仅成功请求） | 5 秒 |
| 首字延迟 TTFB | `usage_logs.first_token_ms` 近 15 分钟均值 | 5 秒 |
| 可用渠道数 | 启用渠道声明该模型的数量 | 5 秒 |

**连通性测试**模拟真实 agent 调用：`POST /v1/chat/completions`（`stream: true`，
最小消息 + `max_tokens: 8`）→ 读取首个 `data:` 块即 `abort()` 断开 →
返回「是否连通」与首字延迟。刻意不引入后端专用测活接口：测试走的就是生产转发链路，
因此它验证的是「这个模型对我通不通」，而不是「渠道凭据还活着」。

边界处理：模型离线（`available = false`）时指标显示「离线」且测试按钮禁用；
未填令牌时提示先在「访问令牌」页创建；`max_tokens: 8` + 首块即断保证单次测试
几乎不产生 token 消耗，多次测试无成本压力。

---

## 系统架构

分层设计，单向依赖，`internal/` 各包之间禁止循环依赖。

```mermaid
flowchart TB
    subgraph L1["接入层 · internal/server"]
        R["路由与中间件<br/>鉴权 · 限流 · 分组 RPM · 敏感词 · 审计 · CIDR · 语言"]
        H["处理器<br/>模型广场 / 渠道 / 分组 / 令牌 / 订单 / 财务 ..."]
    end

    subgraph L2["核心域 · internal/relay"]
        RT["选路编排<br/>分组 → 渠道 → 凭据"]
        AU["协议适配<br/>OpenAI / Anthropic / Gemini / Codex"]
        BL["计费与结算<br/>预扣 · 结算 · 退还 · 预算"]
        FD["失败分类与冷却"]
    end

    subgraph L3["领域层 · internal/model"]
        M["实体与仓储接口<br/>Channel · Key · Group · Price · Token · Order · UsageLog"]
    end

    subgraph L4["持久化 · internal/store"]
        S["SQL 实现 + 版本化迁移<br/>SQLite（按方言分目录）"]
    end

    subgraph L5["支撑 · internal/*"]
        P["payment 支付通道"]
        CT["channeltype 渠道目录"]
        I18N["i18n 多语言"]
        CFG["config 配置"]
    end

    L1 --> L2
    L1 --> L3
    L2 --> L3
    L4 --> L3
    L1 -.-> L5
    L2 -.-> L5
```

| 层 | 目录 | 职责 | 不做什么 |
| --- | --- | --- | --- |
| 接入层 | `internal/server` | 路由、中间件、请求校验、DTO 转换 | 不直接写 SQL、不实现转发逻辑 |
| 核心域 | `internal/relay` | 选路、协议转换、转发、计费结算、失败处置 | 不感知 HTTP 细节，只依赖 `model` 接口 |
| 领域层 | `internal/model` | 实体、规则、仓储接口定义 | 不写 SQL、不感知 HTTP |
| 持久化 | `internal/store` | 仓储实现、迁移执行、聚合查询 | 不承载业务规则 |
| 支撑 | `payment` / `channeltype` / `i18n` / `config` | 支付适配、渠道目录、文案、配置 | 不反向依赖上层 |

> 新增上游：在 `internal/channeltype/catalog.go` 登记类型；协议不同则在 `internal/relay/` 增加适配器。
> 新增数据表：在 `internal/store/migrations/sqlite/` 新开递增编号脚本（只增不改），
> 再同步 `model` 实体与 `store` 列清单。

---

## 核心数据模型

```mermaid
erDiagram
    GROUP ||--o{ CHANNEL : "分组路由"
    CHANNEL ||--o{ CHANNEL_KEY : "一渠道多凭据"
    GROUP ||--o{ MODEL_PRICE : "分组定价"
    CHANNEL ||--o{ MODEL_PRICE : "渠道专属价"
    USER ||--o{ TOKEN : "拥有"
    TOKEN ||--o{ USAGE_LOG : "产生"
    CHANNEL ||--o{ USAGE_LOG : "实际命中"
    CHANNEL ||--o{ CHANNEL_MODEL_COST : "上游进价"
    USER ||--o{ ORDER : "充值"
    USER ||--o{ QUOTA_RESERVATION : "预扣台账"
    GROUP }o--|| USER : "agent_group 指派"
```

| 实体 | 关键字段 | 说明 |
| --- | --- | --- |
| `model_groups` | `ratio` 倍率 · `rpm_limit` 每分钟上限 · `unlock_min_recharge_cents` 门槛 · `admin_only` 仅后台分发 | 人群与代理档的载体；倍率即折扣 |
| `channels` | `group_names` 可服务分组 · `models` 支持模型 · `key_strategy` 调度策略 · 重试与冷却策略 | 「能不能走这条上游」 |
| `channel_keys` | 加密密钥 · `group_names` / `models` 分叉 · `weight` / `priority` / `rpm_limit` / `in_flight` · `cooldown_until` · 订阅额度窗口 | 「走这条上游时用哪把」 |
| `model_prices` | `model` · `group_name` · `channel_id`（0=不限渠道）· 输入 / 缓存 / 输出 / 按次价 · 计费方式 | 取价优先级：渠道专用价 → 分组默认价 |
| `tokens` | `remain_quota` / `unlimited_quota` · `group_name` · `budget_quota` / `budget_period` / `budget_window_*` | 下游凭证 + 周期预算 |
| `quota_reservations` | `request_id` 唯一索引 · `status` 状态机 · `reserved` / `settled` | 幂等闸门，保证至多入账一次 |
| `usage_logs` | 实际渠道 / 上游模型 · token 明细 · `quota` · `price_version` 定价快照 | 对账与复算的依据 |
| `channel_model_costs` | 按量 / 按次进价规则 | 成本对账的输入 |
| `payment_orders` | `trade_no` · 金额 · 状态机 | 充值订单 |
| `users` | 额度 · `agent_group` 代理分组 · 角色 | 账号与代理归属 |

---

## 请求的完整生命周期

一次 `/v1/chat/completions` 调用在网关内部经历的路径，便于排查问题与二次开发。

```mermaid
flowchart TD
    S["客户端请求"] --> P1["① 鉴权与限流"]
    P1 --> P1a["TokenAuth：校验令牌（启用 / 过期 / 模型白名单 / 所属分组）"]
    P1a --> P1b["预扣额度（预扣 → 结算 → 退还的第一步）"]
    P1b --> P1c["分组 RPM 闸门（rpm_limit = 0 时零成本放行）"]
    P1c --> P1d["敏感词前置过滤"]

    P1d --> P2["② 选路"]
    P2 --> P2a["按分组筛选可用渠道（启用状态 / 模型支持 / 时段规则）"]
    P2a --> P2b["渠道级熔断检查：凭据是否全部余额耗尽 → 跳过"]
    P2b --> P2c["渠道内选凭据：失败冷却 + 渠道×模型冷却 + 会话粘性 + 五种策略"]

    P2c --> P3["③ 转发与适配"]
    P3 --> P3a["入站协议 → 内部 OpenAI 表示 → 上游适配器"]
    P3a --> P3b["流式双向转换（SSE 帧级）"]
    P3b --> P3c["失败分类处置：429 换 key / 5xx 换渠道 / 审核换模型 / 空 200 降级"]

    P3c --> P4["④ 回写与记账"]
    P4 --> P4a["注入可观测响应头（X-Routed-Via / X-Fallback-Attempts / X-Upstream）"]
    P4a --> P4b["结算额度：成功多退少补，失败全额退还"]
    P4b --> P4c["写调用日志（实际渠道 / 上游模型 / 定价版本快照）"]
    P4c --> P4d["更新凭据运行态（最近使用 / 冷却 / 失败计数 / 余额）"]
```

---

## 支持的协议与上游

**下游（应用如何连接本站）**：OpenAI 兼容 · Anthropic · Gemini

**上游（本站如何连接他人）**：目录中已登记 **80 种**渠道类型，按 8 大类组织：

| 大类 | 说明 |
| --- | --- |
| 文本大模型 | OpenAI / Azure / Anthropic / Gemini / DeepSeek / Kimi / 智谱 / 通义 / 硅基流动 / OpenRouter / Groq / Together / Mistral / xAI / Ollama / vLLM 等 |
| 聚合服务 | 各类聚合中转 |
| 订阅账号 | Claude / Codex / Gemini 等订阅账号（OAuth 刷新） |
| 自建 | 本地与私有化部署 |
| 图像 | 图像生成类上游 |
| 视频 | 视频生成类上游 |
| 音频 | 语音类上游 |
| 嵌入 | Embedding 类上游 |

> **诚实说明**：80 种类型中，**已有 39 种完成协议适配器与鉴权实现**（`Available: true`），可直接选用；
> 其余类型在后台标为「即将支持」并禁止选中，不会让你配到一半才发现调不通。
> 已实现的协议与鉴权白名单由 `internal/channeltype/catalog_test.go` 钉住，防止误标。

---

## 接口一览

### 网关接口（下游协议）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/v1/chat/completions` | OpenAI 兼容对话（支持流式） |
| POST | `/v1/embeddings` | 向量化 |
| GET | `/v1/models` | 可用模型列表 |
| POST | `/v1/messages` | Anthropic 协议（Claude Code 直接对接） |
| POST | `/v1/responses` | OpenAI Responses / Codex 协议 |
| POST | `/v1beta/models/*action` | Gemini 协议 |
| POST | `/v1/images/generations` | 图像生成（OpenAI 兼容透传） |
| POST | `/v1/audio/speech` | 语音合成（二进制音频流透传） |
| POST | `/v1/audio/transcriptions` · `/v1/audio/translations` | 语音识别 / 翻译（multipart 透传） |
| POST | `/v1/tasks` | 提交异步生成任务 |
| GET | `/v1/tasks` · `/v1/tasks/:ref` | 任务列表与详情 |

### 公开接口（无需登录）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查（含数据库与迁移版本） |
| GET | `/api/status` | 站点信息（含额度兑换比例、合规信息） |
| GET | `/api/models` | 模型广场（带代理视图） |
| GET | `/api/models/quote` | 公开定价试算 |
| GET | `/api/announcements` | 站点公告 |
| GET | `/api/payment/public` | 公开支付参数 |
| POST/GET | `/api/payments/:method/notify` | 支付回调（按通道验签） |
| GET | `/sitemap.xml` · `/robots.txt` | SEO |

### 账号接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/auth/register` | 注册 |
| POST | `/api/auth/login` · `/api/auth/admin-login` | 密码登录 / 管理后台登录 |
| POST | `/api/auth/email-code` · `/api/auth/email-login` | 邮箱验证码与验证码登录 |
| POST | `/api/auth/password-reset` | 重置密码 |
| GET | `/api/install/status` · POST `/api/install` | 安装向导 |
| GET | `/api/auth/me` · POST `/api/auth/logout` | 当前身份 / 登出 |

### 用户门户 `/api/user`

令牌增删改查与明文找回（`/tokens`、`/tokens/:id/key`）、可选分组（`/groups`）、用量与日志
（`/usage`、`/logs`）、任务（`/tasks`）、订单（`/orders`、`/orders/:tradeNo`）、兑换（`/redeem`）、
邀请与奖励（`/referral`、`/referral/rewards`）、签到（`/checkin`）、财务概览（`/finance`）、
试用额（`/trial`）、**用量排行榜（`/leaderboard`，付费榜 / 免费榜各 Top 20，管理员 `?all=1` 看完整榜）**、
**模型实时指标（`/models/:model/stats`，tokens/s · 平均耗时 · 首字延迟 TTFB）**。

### 管理后台 `/api/admin`

| 分组 | 代表端点 |
| --- | --- |
| 概览 | `/dashboard` · `/maintenance/overview` · `/maintenance/backup` |
| 渠道 | `/channels` 增删改查 · `/channels/:id/test` 测活 · `/channels/:id/keys` 密钥池 · `/channels/:id/costs` 进价 · `/channels/:id/mappings` 模型映射 · `/fetch-models` 拉取模型 · `/channel-types` |
| 分组与定价 | `/groups` 增删改查 · `/prices` 增删改查 · `/prices/quote` 试算 |
| 令牌与用户 | `/tokens` 增删改查与明文 · `/users` 增删改查 |
| 内容与运营 | `/announcements` · `/broadcasts` 群发 · `/sensitive-words` · `/corpus/*` 语料 · `/trial-grants` |
| 账务 | `/orders` · `mark-paid` / `close` · `/redeem-codes` · `/finance/reconciliation` 成本对账 |
| 系统 | `/settings` · `/smtp` 与测试发信 · `/oauth-providers` · `/audit-logs` · `/logs` · `/tasks` |

> 完整 140+ 端点以 `internal/server/router.go` 为准；管理端点默认受会话鉴权保护，
> 可再叠加 CIDR 白名单。

---

## 权限与角色

| 角色 | 身份判定 | 可见范围 | 典型能力 |
| --- | --- | --- | --- |
| 游客 | 未登录 | 模型广场（公开价）、公开试算、公告 | 了解价格、试算费用 |
| 普通用户 | 会话 Cookie | 自己的令牌 / 用量 / 订单 / 邀请 / 签到 | 建令牌、充值、查看账单 |
| 代理用户 | 用户被指派 `agent_group` | 广场按**自己那一档**展示模型与折扣价 | 以拿货折扣调用，账目按折扣计 |
| 管理员 | 管理员角色 | 全部后台（可叠加 CIDR 白名单） | 渠道、定价、用户、订单、财务 |
| 超级管理员 | 安装向导创建 | 全部后台 + 系统设置与维护 | 站点配置、备份、SMTP、OAuth |

> 越权防护：令牌明文取回需归属校验 + 写审计；代理档仅对本人可见；
> 令牌分组切换需校验「分组存在 + 用户已达解锁门槛」。

---

## 计费与账务细则

### 额度口径

```
按量：额度 = (输入 Token × 输入价 + 缓存 Token × 缓存价 + 输出 Token × 输出价) / 1,000,000
按次：额度 = 单次价 × 次数
实际入账 = 额度 × 分组倍率 / 100
```

金额全程使用 **int64 整数「额度」**，展示层才按站点兑换比例换算成人民币，杜绝浮点漂移。
免费模型 / 未定价模型**跳过预扣**，不会被额度墙挡住。

### 三段式结算

```mermaid
sequenceDiagram
    participant C as 客户端
    participant G as 网关
    participant U as 上游
    C->>G: 请求（带令牌）
    G->>G: 预扣（写 quota_reservations，request_id 唯一）
    G->>U: 转发（可能重试 / 换渠道）
    U-->>G: 响应 + usage
    alt 成功
        G->>G: 结算（按实际用量多退少补）
    else 失败
        G->>G: 退还（全额回滚预扣）
    end
    G-->>C: 响应（含路由响应头）
```

### 定价优先级

```
渠道专用价（channel_id = 该渠道）   ← 最高
        ↓ 没有则回退
分组默认价（channel_id = 0）
```

### 成本与毛利

```
毛利 = 售价收入（用户实扣额度） − 上游成本（按渠道进价规则核算）
```

成本对账报表按分组 / 渠道 / 模型聚合；**没录进价的请求会单独标注**，
否则那部分成本按 0 计，报表会偏乐观。

---

## 技术栈

| 层 | 选型 | 说明 |
| --- | --- | --- |
| 后端 | Go 1.27 + Gin v1.12 | 单二进制，零 CGO（SQLite 采用纯 Go 的 `modernc.org/sqlite`） |
| 数据库 | SQLite | 嵌入式、免运维；迁移脚本按方言分目录，已留出扩展接缝 |
| 前端 | Next.js 16.3（静态导出）+ React 19 + Tailwind CSS v4 + TypeScript 5 | 构建产物 `web/dist` 由 `go:embed` 打进二进制 |
| 图表 | ECharts 5 | 后台统计图表 |

<div align="center">

<img src="assets/icons/go.svg" width="36" title="Go 1.27" alt="Go" />
<img src="assets/icons/nextdotjs.svg" width="36" title="Next.js 16" alt="Next.js" />
<img src="assets/icons/react.svg" width="36" title="React 19" alt="React" />
<img src="assets/icons/typescript.svg" width="36" title="TypeScript 5" alt="TypeScript" />
<img src="assets/icons/tailwindcss.svg" width="36" title="Tailwind CSS v4" alt="Tailwind CSS" />
<img src="assets/icons/sqlite.svg" width="36" title="SQLite" alt="SQLite" />
<img src="assets/icons/docker.svg" width="36" title="Docker" alt="Docker" />
<img src="assets/icons/nginx.svg" width="36" title="Nginx / Caddy 反向代理" alt="Nginx" />
<img src="assets/icons/github.svg" width="36" title="GitHub" alt="GitHub" />

</div>

> 前端采用 `output: 'export'` 静态导出，**没有独立的前端托管**：界面与 API 同源同端口，
> 部署只需一个二进制文件。

---

## 快速开始

> 本节只给最短路径。**完整部署教程**（systemd 生产部署、Nginx/Caddy 反代与 HTTPS、升级与回滚、CLI 与环境变量速查、常见问题）见 [DEPLOYMENT.md](DEPLOYMENT.md)。

### 方式一：Docker Compose（推荐）

```bash
git clone https://github.com/LTZY-ACU/LTZY-API.git && cd LTZY-API
cp .env.example .env

docker build -t ltzy-api:local .          # 首次构建（前端 + 后端 + 运行镜像）
docker run --rm ltzy-api:local -gen-key   # 打印一个主密钥，填进 .env 的 AQUA_APP_KEY

docker compose up -d
```

浏览器打开 `http://127.0.0.1:8787`。数据落在宿主机 `./data`，迁移服务器时打包该目录即可。

### 方式二：docker run（不使用 compose）

```bash
docker build -t ltzy-api:local .

docker run -d --name ltzy-api \
  -p 8787:8787 \
  -e AQUA_APP_KEY="<你的主密钥>" \
  -e AQUA_SERVER_LISTEN=0.0.0.0:8787 \
  -v "$PWD/data:/data" \
  --restart unless-stopped \
  ltzy-api:local
```

### 方式三：单二进制（Linux 服务器 / systemd）

```bash
go build -o aqua ./cmd/ltzy           # 纯 Go，零 CGO，不需要 gcc

./aqua -gen-key                        # 生成加密主密钥（只生成，不落盘）

sudo useradd -r -s /usr/sbin/nologin aqua
sudo mkdir -p /opt/aqua /etc/aqua /var/lib/aqua
sudo cp aqua /opt/aqua/aqua && sudo chown aqua:aqua /opt/aqua/aqua

sudo cp .env /etc/aqua/aqua.env        # 填入真实密钥
sudo chmod 600 /etc/aqua/aqua.env && sudo chown root:root /etc/aqua/aqua.env

sudo cp aqua-api.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now aqua-api
sudo systemctl status aqua-api
```

**升级**：替换 `/opt/aqua/aqua` 后 `sudo systemctl restart aqua-api`，数据库迁移在启动时自动执行。

> 建议保留上一版二进制（如 `aqua.bak-<时间戳>`）。回滚只需 `cp` 回旧文件再重启；
> 数据库迁移只增列、不删列，向前兼容。

### 方式四：源码直接运行（开发）

```bash
# 前端（可选：仓库中的 web/dist 为占位，正式界面需构建后才会内嵌）
cd web && npm ci && npm run build && cd ..

go build -o bin/aqua ./cmd/ltzy
export AQUA_APP_KEY="<你的主密钥>"      # Windows: $env:AQUA_APP_KEY="..."
./bin/aqua -config ./aqua.json          # 不加 -config 则使用默认值与环境变量
curl http://127.0.0.1:8787/healthz
```

> **前端是内嵌的**：`go:embed` 会把 `web/dist` 打进二进制，所以部署只需要一个文件。
> 直接 `go build` 而不构建前端时，界面会是占位页，但 API 完全可用。

### 首次使用（安装向导）

首次打开站点会进入安装向导：创建超级管理员账号 → 填写站点信息 →（可选）配置支付与邮件通道。
也可以从后台独立入口登录管理面板。

### 反向代理

对外提供服务时建议前置 Nginx 或 Caddy 并启用 HTTPS。两个容易踩的点：

```nginx
location / {
    proxy_pass http://127.0.0.1:8787;
    proxy_http_version 1.1;

    # 1) 流式响应必须关闭缓冲，否则前端要等整段生成完才逐字显示
    proxy_buffering off;
    proxy_cache off;

    # 2) 超时必须大于网关的上游超时（默认 300 秒），否则长回答会被反代先掐断
    proxy_read_timeout 600s;
    proxy_send_timeout 600s;

    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

> 若域名挂在 Cloudflare 橙云代理后面，注意 CF 回源有 100 秒硬上限，超过会返回 524。
> 想完整吃到 300 秒超时，请增加一条灰云（DNS only）记录直连源站。

---

## 配置

优先级：**默认值 < 配置文件 < 环境变量**。

### 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `AQUA_APP_KEY` | 是 | 加密主密钥，只能走环境变量（配置文件里的同名字段会被忽略）。用 `aqua -gen-key` 生成 |
| `AQUA_SERVER_LISTEN` | 否 | 监听地址，默认 `127.0.0.1:8787`；容器内须为 `0.0.0.0:8787` |
| `AQUA_SERVER_MODE` | 否 | `debug` / `release` / `test` |
| `AQUA_DATABASE_DRIVER` | 否 | 目前为 `sqlite` |
| `AQUA_DATABASE_DSN` | 否 | SQLite 文件路径，默认 `./data/aqua.db`（父目录自动创建） |
| `AQUA_RELAY_GROUP` | 否 | 网关默认分组（不带分组的令牌走哪个分组），默认 `default` |
| `AQUA_ADMIN_ALLOW_CIDRS` | 否 | 管理后台访问白名单，逗号分隔的 CIDR（如 `10.0.0.0/8,1.2.3.4/32`）。留空表示不限制 |
| `AQUA_CHANNEL_AUTO_DISABLE_MIN_REQUESTS` | 否 | 渠道自动停用的最小样本数，`0` 表示关闭自动停用（默认关闭） |
| `AQUA_CHANNEL_AUTO_DISABLE_SUCCESS_RATE` | 否 | 成功率下限（如 `0.9`），低于它且样本足够时停用该渠道 |
| `AQUA_CHANNEL_AUTO_DISABLE_WINDOW_MINUTES` | 否 | 统计窗口（分钟） |
| `AQUA_SMTP_HOST` | 否 | SMTP 服务器地址（邮件验证码与通知，也可在后台配置） |
| `AQUA_SMTP_PORT` | 否 | SMTP 端口，默认 `465` |
| `AQUA_SMTP_USERNAME` | 否 | SMTP 用户名 |
| `AQUA_SMTP_PASSWORD` | 否 | SMTP 密码，只能走环境变量 |
| `AQUA_SMTP_FROM` | 否 | 发件人地址 |
| `AQUA_SMTP_FROM_NAME` | 否 | 发件人显示名，默认 `LTZY-API` |
| `AQUA_EPAY_KEY` | 否 | 易支付商户密钥（MD5 签名） |
| `AQUA_STRIPE_SECRET_KEY` | 否 | Stripe Secret Key |
| `AQUA_STRIPE_WEBHOOK_SECRET` | 否 | Stripe Webhook 签名密钥 |
| `AQUA_ALIPAY_PRIVATE_KEY` | 否 | 支付宝应用私钥（RSA2，支持 PEM 与裸 base64） |
| `AQUA_ALIPAY_PUBLIC_KEY` | 否 | 支付宝公钥 |
| `AQUA_WECHATPAY_APIV3_KEY` | 否 | 微信支付 APIv3 密钥（32 字节） |
| `AQUA_WECHATPAY_PRIVATE_KEY` | 否 | 微信支付商户私钥（PEM） |
| `AQUA_WECHATPAY_PLATFORM_PUBLIC_KEY` | 否 | 微信支付平台证书公钥，用于校验回调签名 |
| `AQUA_LOG_LEVEL` | 否 | `debug` / `info` / `warn` / `error` |
| `AQUA_LOG_FORMAT` | 否 | `text` / `json` |

完整样例见 [`.env.example`](.env.example)。

### 配置文件

```json
{
  "server":   { "listen": "127.0.0.1:8787", "mode": "release" },
  "database": { "driver": "sqlite", "dsn": "./data/aqua.db" },
  "log":      { "level": "info", "format": "text" }
}
```

### 两条安全铁律

1. **密钥类配置一律不入库**。支付、SMTP 与加密主密钥只能从环境变量注入；只有运营参数
   （网关地址、商户号、汇率、限额、开关）进数据库、后台可改。即使数据库被完整拖走，
   也拿不到任何一把能直接用的凭据。
2. **主密钥务必单独备份**。它一旦变更，库里所有上游密钥就再也解不开，只能重新录入。

---

## 接入示例

任何 OpenAI 兼容客户端，把 Base URL 指过来、Key 换成 LTZY-API 的令牌即可。

### curl

```bash
curl https://你的域名/v1/chat/completions \
  -H "Authorization: Bearer sk-你的令牌" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "你的模型名",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

### OpenAI SDK（Python）

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://你的域名/v1",
    api_key="sk-你的令牌",
)
resp = client.chat.completions.create(
    model="你的模型名",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

### Claude Code / Anthropic 客户端

LTZY-API 原生支持 Anthropic 协议，可直接接管 Claude Code 的流量：

```bash
export ANTHROPIC_BASE_URL=https://你的域名
export ANTHROPIC_AUTH_TOKEN=sk-你的令牌
claude
```

### 其他客户端

Cursor、Codex CLI、Cherry Studio、NextChat、LobeChat、沉浸式翻译等，选择
「OpenAI 兼容 / 自定义 OpenAI 接口」，填入上面的 Base URL 与令牌即可。

### 试算费用（无需令牌）

```bash
curl "https://你的域名/api/models/quote?model=你的模型名&prompt_tokens=1000&completion_tokens=500"
```

---

## 运营指南

### 分组与渠道的关系

- **渠道**决定「这个请求能不能走这条上游」：它声称支持哪些模型、属于哪些分组；
- **凭据**（渠道下的每把 key）决定「走这条上游时用哪把」，还可进一步限定它服务哪些分组与模型；
- **令牌**可指定所属分组；不指定则落入网关默认分组（`AQUA_RELAY_GROUP`）。

> 最常见的坑：把渠道移到新分组后，若忘了同步默认分组，所有「不带分组的令牌」会立刻报
> 「无可用渠道」。

### 代理拿货档怎么配

1. 在后台「分组」里新建一个代理档（如 `agent`），把计费倍率设为拿货折扣（如 `60` 表示六折），
   并打开「仅后台分发」；
2. 在「价格」里为该代理档配置价目，也可复用同一套规则靠倍率打折；
3. 在「用户」里把代理商账号的 `agent_group` 指派为该档；
4. 代理登录后，模型广场会自动切换为他的那一档，展示「原价划线 + 折后价」，且与实际扣费同源。

### 额度与预算

- **总额度**：令牌与用户两级，`-1` 表示不限；
- **周期预算**：在令牌上设「每周期最多花 N 元」，周期可选日 / 周 / 月；窗口内超限返回 429，
  窗口到期自动重置。

### 渠道治理建议

- 单渠道密钥 ≥ 3 把，避免单点限流；
- 对易限流的上游调低单密钥每分钟上限，交给调度器换 key；
- 打开渠道健康面板关注成功率；必要时启用按成功率自动停用；
- 定期核对「未录进价」的请求，保证成本报表可信。

---

## 国际化

| 层面 | 支持 | 说明 |
| --- | --- | --- |
| 前端界面 | 简体中文 · English · Français · Русский · Español · العربية | 六种语言，含 RTL 布局 |
| 服务端文案 | 同上六种 | 错误信息按 `Accept-Language` 本地化 |
| 本文档 | 同上六种 | 见页眉语言切换 |

> 本文档覆盖**联合国六种官方语言**（中文、英文、法文、俄文、西班牙文、阿拉伯文）。
> 若你只需要其中若干种，删除对应 `README.<语言>.md` 即可，其余不受影响。

---

## 安全

- **密钥加密落库**：上游密钥 AES-256-GCM 加密，主密钥只从环境变量注入（配置文件同名字段被忽略）
- **日志脱敏**：日志只输出「是否已注入凭据」与上游主机、路径，连查询串都不输出
- **CIDR 白名单**：`AQUA_ADMIN_ALLOW_CIDRS` 限制管理后台来源，白名单外一律拒绝
- **明文取回审计**：令牌明文走独立接口并写审计日志（谁、何时、取了哪把）
- **越权防护**：代理档仅本人可见；令牌分组切换校验存在性与解锁门槛；归属不符统一返回 404
- **幂等防重**：请求台账以 `request_id` 唯一索引做闸门，重试 / 断连 / 双回调至多入账一次
- **密码与会话**：密码加盐哈希；会话使用服务端校验的签名 Cookie
- **内容安全**：敏感词前置过滤 + 词表管理
- **备份**：提供数据库备份与备份文件校验入口

---

## 部署与容量

| 场景 | 建议 |
| --- | --- |
| 本机试用 | 单二进制直接运行，SQLite 落 `./data` |
| 单机生产 | systemd 托管 + Nginx/Caddy 反代 + HTTPS；保留上一版二进制用于回滚 |
| 容器化 | 多阶段 Dockerfile；数据卷挂载 `/data`；环境变量注入密钥 |
| 备份 | 停服拷贝或用 `VACUUM INTO` 热备 `aqua.db`，**同时备份 `AQUA_APP_KEY`** |
| 容量 | 单机 SQLite 足以支撑中小规模；存储层已留方言接缝，后续可平滑接入外部数据库 |
| 扩容 | 网关无状态可多实例，但**会话粘性为进程内**，多实例下退化为尽力而为 |

---

## 常见问题

**`/healthz` 返回 503 怎么办？**
数据库不可用时就会 503。查看日志中的数据库错误；SQLite 场景优先检查数据目录权限。

**为什么后台看不到渠道密钥明文？**
这是刻意设计。密钥以 AES-256-GCM 密文落库，界面只显示掩码，后台被攻破也导不出可用凭据。
需要更换时直接覆盖写入新密钥即可。

**把渠道换到新分组后，所有令牌都报「无可用渠道」？**
这是最容易踩的坑。网关有一个默认分组（`AQUA_RELAY_GROUP`），决定「不带分组的令牌」去哪找渠道。
渠道迁到新分组后，必须同步修改这个默认分组并重启，否则老令牌会立刻失联。

**免费模型为什么还是被额度挡住？**
未命中任何价格规则的模型会跳过预扣，正常不应被挡。若被挡，检查分组下是否配了通配价格规则
（例如 `*`），那会让模型变成「有价」，从而走额度判定。

**上游返回 429 或超时频繁？**
429 属于凭据级失败：会换密钥重试并把该密钥置入短冷却（指数退避，到期自动恢复）。
若频发，通常是密钥太少或上游限速低，去渠道里加密钥或调低单密钥的每分钟上限。

**分组设了每分钟上限，用户被 429 了怎么办？**
返回体中的 `error.code` 是 `quota.group_rpm_exceeded`，提示该分组的每分钟上限。
把上限调大，或把用户移到不限速的分组即可。

**代理说「看到折扣价但扣的是原价」？**
正常情况下不会：代理广场价与计费取自同一套价格规则。请确认两点：
一是代理账号的 `agent_group` 确实指派为该代理档；二是该代理创建令牌时选择的分组是这个代理档。
两者都正确仍不一致，请提 Issue。

**数据怎么备份？**
SQLite 场景下：停服务（或用 `VACUUM INTO` 热备）→ 拷贝 `aqua.db` → 同时备份 `AQUA_APP_KEY`。
少了主密钥，备份里的上游密钥就是一堆无法解密的字节。

**支持 MySQL 或 PostgreSQL 吗？**
当前默认且仅支持 SQLite，已能覆盖自托管与中小规模场景。存储层已经留出方言接缝，
后续接入不需要重写业务层。

**怎么新增一个上游渠道类型？**
在 `internal/channeltype/catalog.go` 登记类型元数据，并确认它落在 `catalog_test.go`
的已实现白名单内。协议不同则需在 `internal/relay/` 增加适配器。

**为什么日志里看不到我配置的上游密钥？**
同样是刻意设计：日志只输出「是否已注入凭据」与上游主机和路径，连 URL 的查询串都不输出。

**怎么知道某次请求走了哪个渠道、降级了几次？**
响应头里有 `X-Routed-Via`、`X-Fallback-Attempts`、`X-Upstream`；调用日志里也记录了实际渠道与上游模型名。

---

## 术语表

| 术语 | 含义 |
| --- | --- |
| 渠道 / Channel | 一条上游服务（含 base_url、协议、鉴权与可用模型） |
| 凭据 / Channel Key | 渠道下的一把上游密钥；可独立设置权重、限速与可用范围 |
| 分组 / Group | 人群与价格的载体；决定可用渠道与计费倍率 |
| 代理档 | 仅后台分发的分组，用倍率表示拿货折扣 |
| 额度 / Quota | 站内记账单位（整数）；展示时按兑换比例换算成人民币 |
| 令牌 / Token | 发给下游使用的 API Key（`sk-` 开头） |
| 预扣 · 结算 · 退还 | 三段式额度流程，用于并发下防止透支 |
| 周期预算 | 令牌在日 / 周 / 月内的额度上限，超限熔断 |
| 冷却 / Cooldown | 凭据临时不可用状态，到期自动恢复 |
| 摘除 / Retire | 凭据永久不可用（仅在上游明确声明吊销时） |
| 熔断跳过 | 渠道整体不可用时，路由主动跳过该渠道 |
| 定价版本快照 | 记账时记录的计价规则版本，用于事后复算 |
| 进价 / Cost | 上游采购成本，用于毛利对账 |
| 路由响应头 | `X-Routed-Via` 等，用于观测实际路由与降级 |

---

## 路线图

- [x] 协议互转（OpenAI ↔ Anthropic ↔ Gemini），流式双向转换含工具调用
- [x] 凭据池五种调度策略、冷却半开、会话粘性、在途计数
- [x] 预扣 / 结算 / 退还的额度体系；流式用量增量解析
- [x] 分组与倍率、模型广场、兑换码、五种支付通道、异步任务
- [x] 单二进制 + Docker 部署，前端内嵌
- [x] 浏览器安装向导 + 超管独立入口；管理操作审计、站点公告
- [x] 邮箱验证码登录与重置密码；邀请返利与签到；限时试用额
- [x] 失败分类驱动重试、渠道 × 模型冷却、尊重上游 `Retry-After`
- [x] 令牌滚动窗口预算（日 / 周 / 月）
- [x] 分组每分钟请求套餐（RPM）、渠道级余额熔断跳过
- [x] 代理拿货档与广场折后价对照、公开定价试算接口
- [x] 真实成本对账报表（收入 − 成本 − 毛利）、定价版本快照
- [x] 渠道健康面板与按成功率自动停用、管理面 CIDR 白名单
- [x] 三套主题（浅色 / 深色 / 深蓝）、全站合规提示体系
- [x] AWS Bedrock 与 Google Vertex 签名鉴权（SigV4 / 服务账号 JWT）
- [x] 通用异步任务上游适配器（模板驱动，可接入任意图像 / 视频 / 音乐生成服务）；厂商私有适配器按需接入
- [x] 渠道专属价的录入界面（模型 × 分组 × 渠道）
- [x] 订阅账号配额窗口可视化（5 小时 / 每周双窗口）

---

## 开发

```bash
go build ./...       # 编译
go test ./...        # 测试
gofmt -w .           # 格式化

cd web && npm ci && npm run type-check && npm run build   # 前端
```

目录结构（本仓库根目录即代码目录）：

```
cmd/ltzy/              程序入口（仅装配，不含业务逻辑）
internal/config/       配置加载与校验
internal/model/        领域模型与仓储接口（不含 SQL）
internal/store/        持久化实现（SQL + 版本化迁移，按方言分目录）
internal/server/       HTTP 层（路由 / 中间件 / 处理器）
internal/relay/        协议适配与转发（核心域：选路 / 计费 / 冷却）
internal/payment/      支付通道适配
internal/channeltype/  上下游类型注册表（80 种）
internal/i18n/         服务端多语言文案
web/                   前端（Next.js，构建产物内嵌进二进制）
assets/                文档徽章与图标
Dockerfile             多阶段构建：前端 → 后端 → 极简运行镜像
aqua-api.service       systemd 单元（裸机部署）
```

### 工程约定（强制）

1. **小步提交**：每个可独立描述的小步骤完成后立刻提交，禁止攒到最后一次性提交；
   每次提交都应可编译、可回滚。完整提交时间线是项目创作过程的证据链，禁止 squash。
2. **注释为结构化文档**：每个源文件头部写清「意图 / 流转 / 扩展」三段，
   描述代码做什么、数据怎么流转、往哪儿扩展；只写技术原因，不写个人感想。
3. **密钥不入库、不落盘、不进日志**，详见上文「两条安全铁律」。
4. **原创性红线**：允许阅读、研究与学习任何公开项目（含参考实现）以理解功能与算法思想；
   但禁止逐字复制粘贴其代码、注释、常量表与命名风格。判据很简单：
   能否脱离参考项目，独立解释这套实现的设计取舍。

详见 [`AGENTS.md`](AGENTS.md) 与 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

---

## 参与贡献

- `main` 分支受保护，仅维护者可推送；外部贡献一律走 Fork + Pull Request。
- 在你的 fork 上可开任意数量的 `feature/*`、`fix/*` 分支自由开发，
  想合入主仓库时发 PR，经审查后合并（不 squash，保留提交时间线）。
- 提交规范、验证清单与 Issue / PR 模板见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 许可证

源代码采用 [MIT 许可证](LICENSE)。

> 在遵守该许可证的前提下，你可以自由使用、复制、修改、合并、发布、分发、再许可及/或销售本软件，
> 包括商业用途；重新分发时须保留版权声明与许可声明。该许可证**不授予任何商标权利**。

配套文件：

| 文件 | 作用 |
| --- | --- |
| [LICENSE](LICENSE) | 开源协议全文（MIT 许可证） |
| [DISCLAIMER.md](DISCLAIMER.md) | 使用者须知与免责声明 |
| [TRADEMARK.md](TRADEMARK.md) | 品牌与商标声明 |
| [NOTICE](NOTICE) | 版权声明、原创性时间锚点与分发义务 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献指南（Fork + PR 协作模式） |
| [AGENTS.md](AGENTS.md) | 代码导读（面向 AI 助手与开发者） |

---

<div align="center">

**如果这个项目帮你省下了对账的时间，欢迎点个 Star ⭐**

[在线演示](https://ltzy.top) · [提交 Issue](https://github.com/LTZY-ACU/LTZY-API/issues) · [GitHub](https://github.com/LTZY-ACU/LTZY-API) · [English](README.en.md)

</div>
