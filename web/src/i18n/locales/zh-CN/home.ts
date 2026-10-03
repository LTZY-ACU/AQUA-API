/**
 * 首页词条（简体中文）：Hero / 规格条 / 能力矩阵 / 接入 / 模型价格 / 设计取舍 / FAQ / CTA。
 *
 * 意图（Why）：
 *   首页文案量大、改版频繁，从 site.ts 拆出独立文件，便于单独迭代与六语翻译对齐。
 *
 * 流转（Flow）：
 *   ./home.ts → locales/<lang>/index.ts 聚合进 site 命名空间 → i18n/index.ts 的 messages
 *   → app/page.tsx 以 t('site.home.*') 取用（本文件只含 site.home. 之下的层级）。
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径必须同步补齐（六文件键结构完全一致）；
 *   专有名词保留英文：OpenAI / LLM / API / base_url / token / GitHub / MIT。
 */
export default {
  hero: {
    tagline: '自托管 LLM API 网关 · 单二进制部署',
    titleLine1: '一条 OpenAI 兼容接口，',
    titleLine2: '收拢数十家上游。',
    desc: 'OpenAI / Anthropic / Gemini 等协议在此统一转成 OpenAI 兼容格式。计费、日志、密钥池、失败重试开箱即用。整套网关开源，随时可以自己部署一套。',
    terminalUserMessage: '你好',
    terminalAssistantReply: '你好！有什么可以帮你？',
  },
  action: {
    console: '进入控制台',
    register: '注册并获取令牌',
    browseModels: '浏览模型与价格',
    readSource: '阅读源码',
  },
  stats: {
    modelsOnline: '模型在线',
    channelTypes: '上游渠道类型',
    protocols: '兼容协议',
    deployFiles: '部署文件',
  },
  features: {
    title: '核心能力',
    desc: '一个网关该有的都在里面：协议转换、计费、密钥管理、日志与容错，不需要再拼装第三方组件。',
    f1Title: '协议统一',
    f1Desc: 'OpenAI / Anthropic / Gemini 等入站出站协议互转，对外只暴露一种 OpenAI 兼容格式。',
    f2Title: '精细计费',
    f2Desc: '按量 / 按次 / 免费三种模式，价格按分组配置，每一笔扣费都能逐条核对。',
    f3Title: '密钥池与重试',
    f3Desc: '同一渠道多把密钥轮转，单把失败自动换下一把，上游偶发抽风基本无感。',
    f4Title: '全量日志',
    f4Desc: '每次调用的模型、token 数、耗时与状态码全部留档，用量随时可查。',
    f5Title: '失败切换',
    f5Desc: '渠道级熔断与冷却退避，整条线路不可用时自动切到下一路重试。',
    f6Title: '单二进制自托管',
    f6Desc: '一套 Go 二进制 + SQLite，前端内嵌其中，部署只需要一个文件。',
  },
  quickstart: {
    title: '接入，三步',
    desc: '对外只暴露 OpenAI 兼容接口，现有 SDK 改一个 base_url 就能跑通。',
    s1Title: '注册并创建令牌',
    s1Desc: '在控制台生成一把访问令牌，顺手设好预算与可用模型。',
    s2Title: '替换 base_url',
    s2Desc: '任何支持 OpenAI SDK 的客户端，把 base_url 指向本站 /v1 即可。',
    s3Title: '保持原有代码',
    s3Desc: '协议是兼容的，请求与响应结构不变，无需改动业务代码。',
    sdkNote: '# Python / Node：把 OpenAI SDK 的 base_url 换成 {base}',
    tokenPlaceholder: '你的令牌',
    sampleMessage: '你好',
  },
  models: {
    title: '模型与价格',
    descAgent: '当前为你的代理拿货档：划线为原价，橙色为你的折后价。',
    descPublic: '实时来自站点信息，价格按分组展示，不做修饰。',
    colModel: '模型',
    colGroup: '分组',
    colPrice: '价格',
    viewAllCount: '查看全部 {n} 个模型与完整价格',
    viewAll: '查看全部模型与完整价格',
    priceTbd: '待定价',
    priceFree: '免费',
    pricePerCall: '按次',
    pricePerToken: '按量',
  },
  design: {
    title: '设计取舍与成本',
    desc: '不做黑箱：定价规则、用量留档与成本边界都摆在明面上。',
    principlesLabel: 'Principles',
    costsLabel: 'Cost Breakdown',
    p1Title: '计价写得明白',
    p1Desc: '每个模型的单价摆在模型广场上，按次 / 按量 / 免费三种模式，账单可逐条核对。',
    p2Title: '用量自己说了算',
    p2Desc: '每次调用的模型、token、耗时、状态码都留档，你随时能查，我也改不了。',
    p3Title: '代码是开源的',
    p3Desc: '整套网关以 MIT 许可证在 GitHub 开源；哪天我不做了，你也能自己部署一套。',
    c1Label: '服务器',
    c1Value: '每月固定 · 一台独服跑网关与数据库',
    c2Label: '带宽与流量',
    c2Value: '按量浮动 · 调用越多越高',
    c3Label: '上游模型费用',
    c3Value: '按用量结算 · 我向上游买的价就是成本基准',
    c4Label: '域名与证书',
    c4Value: '每年少量',
    costNotePrefix: '你付的钱先覆盖成本，多出来的才是我继续维护它的理由。若哪天真入不敷出，我会在公告里说明，',
    costNoteBold: '而不是悄悄涨价',
    costNoteSuffix: '。',
  },
  faq: {
    title: '常见问题',
    q1: '这站能一直开着吗？',
    a1: '我会尽力。它的成本可控，我也不靠它赚钱，没有「融资烧完就跑」的问题。真有关停那天，我会提前公告并给出自己部署的完整方案。',
    q2: '为什么要开源？',
    a2: '一是让你能验证我说的都是真的；二是万一我不做了，这套东西不会跟着消失。协议是 MIT。',
    q3: '免费分组的模型会收费吗？',
    a3: '免费分组里就是不计费的，我不搞「先免费养熟再收费」那套。当然，免费范围会随上游价格调整，但改之前会在公告里说。',
    q4: '我的调用数据会被拿去用吗？',
    a4: '不会。日志只用于计费和排障，存在我自己的服务器上。站点的隐私政策里写明了这一点。',
    q5: '上游不稳定怎么办？',
    a5: '密钥池 + 自动重试 + 冷却退避：一把密钥失败自动换下一把，整条渠道不行就换渠道再试。你还是只发一次请求。',
    q6: '怎么联系你？',
    a6: '页面底部的「联系方式」和「投诉举报」都能找到我。有事直说就行。',
  },
  cta: {
    titleLoggedIn: '从一把新令牌开始。',
    titleGuest: '从一把令牌开始。',
    descLoggedIn: '接进现有代码就行，先跑通一个请求，再决定要不要留下。',
    descGuest: '注册免费，先跑通一个请求，再决定要不要留下。',
  },
}
