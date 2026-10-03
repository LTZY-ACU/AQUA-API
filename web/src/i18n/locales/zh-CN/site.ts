/**
 * 公共站点词条（简体中文）：页头 / 页脚 / 顶栏与页脚导航。
 *
 * 意图（Why）：
 *   公开站（未登录可见）的文案与门户、后台完全分离，单独成域便于并行维护与翻译。
 *
 * 流转（Flow）：
 *   ./site.ts → locales/zh-CN/index.ts 聚合 → i18n/index.ts 的 messages → 组件 t('site.*')
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径都要补齐（index.tsx 用类型约束强制键集合一致）。
 */
export default {
  nav: {
    features: '能力',
    quickstart: '接入',
    models: '模型与价格',
    faq: '常见问题',
  },
  header: {
    backHome: '返回首页',
    openaiCompatible: 'OpenAI 兼容',
    login: '登录',
    register: '注册',
  },
  footer: {
    description: 'OpenAI 兼容的 LLM API 网关：多协议上游统一、精细计费、全量日志，单二进制可自托管。',
    colSite: '站点',
    colCompliance: '合规',
    colOpenSource: '开源',
    features: '能力总览',
    quickstart: '接入指南',
    models: '模型与价格',
    faq: '常见问题',
    terms: '用户协议',
    privacy: '隐私政策',
    contact: '联系方式',
    report: '投诉举报',
    security: '安全致谢',
    repo: '源码仓库（GitHub）',
    license: 'MIT 许可证',
  },
}
