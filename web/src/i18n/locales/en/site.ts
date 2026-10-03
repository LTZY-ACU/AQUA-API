/**
 * Public site strings (English): header / footer / navigation.
 *
 * 意图（Why）：
 *   公开站（未登录可见）的文案与门户、后台完全分离，单独成域便于并行维护与翻译。
 *
 * 流转（Flow）：
 *   ./site.ts → locales/en/index.ts 聚合 → i18n/index.ts 的 messages → 组件 t('site.*')
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径都要补齐（index.tsx 用类型约束强制键集合一致）。
 */
export default {
  nav: {
    features: 'Features',
    quickstart: 'Quick start',
    models: 'Models & pricing',
    faq: 'FAQ',
  },
  header: {
    backHome: 'Back to home',
    openaiCompatible: 'OpenAI-compatible',
    login: 'Sign in',
    register: 'Sign up',
  },
  footer: {
    description:
      'OpenAI-compatible LLM API gateway: unified multi-protocol upstreams, metered billing, full logs — self-hostable as a single binary.',
    colSite: 'Site',
    colCompliance: 'Compliance',
    colOpenSource: 'Open source',
    features: 'Capabilities',
    quickstart: 'Quick start',
    models: 'Models & pricing',
    faq: 'FAQ',
    terms: 'Terms of Service',
    privacy: 'Privacy Policy',
    contact: 'Contact',
    report: 'Report abuse',
    security: 'Security thanks',
    repo: 'Source repository (GitHub)',
    license: 'MIT License',
  },
}
