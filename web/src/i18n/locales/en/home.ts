/**
 * Landing page strings (English): hero / spec bar / features / quickstart / model pricing / design trade-offs / FAQ / CTA.
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
    tagline: 'Self-hosted LLM API gateway · single-binary deployment',
    titleLine1: 'One OpenAI-compatible API,',
    titleLine2: 'dozens of upstreams unified.',
    desc: 'Protocols such as OpenAI / Anthropic / Gemini are unified here into an OpenAI-compatible format. Billing, logs, key pools and automatic retries work out of the box. The whole gateway is open source — deploy your own instance any time.',
    terminalUserMessage: 'Hello',
    terminalAssistantReply: 'Hello! How can I help?',
  },
  action: {
    console: 'Open console',
    register: 'Sign up and get a token',
    browseModels: 'Browse models & pricing',
    readSource: 'Read the source',
  },
  stats: {
    modelsOnline: 'Models online',
    channelTypes: 'Upstream channel types',
    protocols: 'Compatible protocols',
    deployFiles: 'Files to deploy',
  },
  features: {
    title: 'Core capabilities',
    desc: 'Everything a gateway needs is included: protocol conversion, billing, key management, logging and fault tolerance — no third-party components to assemble.',
    f1Title: 'Unified protocols',
    f1Desc: 'Convert between inbound and outbound protocols such as OpenAI / Anthropic / Gemini, exposing only a single OpenAI-compatible format.',
    f2Title: 'Fine-grained billing',
    f2Desc: 'Per-token, per-call and free modes, with prices configured per group — every charge can be verified line by line.',
    f3Title: 'Key pools & retries',
    f3Desc: 'Rotate multiple keys per channel; when one fails, the next takes over automatically, so occasional upstream hiccups go unnoticed.',
    f4Title: 'Full logging',
    f4Desc: 'The model, token count, latency and status code of every call are recorded, so usage is always auditable.',
    f5Title: 'Failover',
    f5Desc: 'Channel-level circuit breaking with cooldown backoff: when a whole route is down, requests automatically retry on the next one.',
    f6Title: 'Single-binary self-hosting',
    f6Desc: 'One Go binary plus SQLite, with the frontend embedded inside — deployment needs just a single file.',
  },
  quickstart: {
    title: 'Get started in three steps',
    desc: 'Only an OpenAI-compatible API is exposed, so your existing SDK just needs a new base_url.',
    s1Title: 'Sign up and create a token',
    s1Desc: 'Generate an access token in the console and set its budget and allowed models along the way.',
    s2Title: 'Replace the base_url',
    s2Desc: 'In any client supporting the OpenAI SDK, point base_url at this site’s /v1 endpoint.',
    s3Title: 'Keep your existing code',
    s3Desc: 'The protocols are compatible, so request and response structures are unchanged and no business code needs editing.',
    sdkNote: '# Python / Node: change the OpenAI SDK base_url to {base}',
    tokenPlaceholder: 'your-token',
    sampleMessage: 'Hello',
  },
  models: {
    title: 'Models & pricing',
    descAgent: 'You are viewing your agent wholesale tier: struck-through prices are list prices, orange is your discounted price.',
    descPublic: 'Live data from the site; prices are shown by group, with no embellishment.',
    colModel: 'Model',
    colGroup: 'Group',
    colPrice: 'Price',
    viewAllCount: 'View all {n} models and full pricing',
    viewAll: 'View all models and full pricing',
    priceTbd: 'Pending',
    priceFree: 'Free',
    pricePerCall: 'Per call',
    pricePerToken: 'Per token',
  },
  design: {
    title: 'Design trade-offs and costs',
    desc: 'No black box: pricing rules, usage records and cost boundaries are all out in the open.',
    principlesLabel: 'Principles',
    costsLabel: 'Cost Breakdown',
    p1Title: 'Pricing made explicit',
    p1Desc: 'Each model’s unit price is listed on the model plaza, in per-call / per-token / free modes, so every bill can be checked line by line.',
    p2Title: 'You control your usage',
    p2Desc: 'The model, tokens, latency and status code of every call are recorded and always queryable by you — and unalterable by me.',
    p3Title: 'The code is open source',
    p3Desc: 'The whole gateway is open-sourced on GitHub under the MIT license; if I ever stop, you can still deploy your own instance.',
    c1Label: 'Server',
    c1Value: 'Fixed monthly · one dedicated server runs the gateway and database',
    c2Label: 'Bandwidth & traffic',
    c2Value: 'Variable · the more calls, the higher it gets',
    c3Label: 'Upstream model fees',
    c3Value: 'Usage-based · what I pay upstream is the cost baseline',
    c4Label: 'Domain & certificate',
    c4Value: 'A small amount each year',
    costNotePrefix:
      'What you pay first covers the costs, and only the surplus is my reason to keep maintaining it. If it ever truly cannot break even, I will explain in the announcements, ',
    costNoteBold: 'rather than quietly raising prices',
    costNoteSuffix: '.',
  },
  faq: {
    title: 'FAQ',
    q1: 'Will this site stay up for good?',
    a1: 'I will do my best. Its costs are manageable, I do not rely on it for profit, and there is no "run out of funding and vanish" problem. If it ever has to close, I will announce it in advance and provide a complete self-hosting guide.',
    q2: 'Why open source it?',
    a2: 'First, so you can verify that everything I say is true; second, so that if I ever stop, this project will not disappear with me. It is MIT licensed.',
    q3: 'Will models in the free group ever be charged?',
    a3: 'The free group is simply not billed — I do not play the "free first, charge once you are hooked" game. The free scope may shift with upstream prices, but I will announce any change beforehand.',
    q4: 'Will my call data be used for anything?',
    a4: 'No. Logs are used only for billing and troubleshooting and are stored on my own server. This is stated in the site’s privacy policy.',
    q5: 'What if the upstream is unstable?',
    a5: 'Key pools + automatic retries + cooldown backoff: when one key fails the next takes over, and if a whole channel fails we switch channels and retry. You still send just one request.',
    q6: 'How do I reach you?',
    a6: 'You can find me through "Contact" and "Report abuse" at the bottom of the page. Just reach out if you have anything.',
  },
  cta: {
    titleLoggedIn: 'Start with a fresh token.',
    titleGuest: 'Start with a token.',
    descLoggedIn: 'Plug it into your existing code, get one request through, then decide whether to stay.',
    descGuest: 'Signing up is free — get one request through, then decide whether to stay.',
  },
}
