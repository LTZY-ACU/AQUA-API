/**
 * Textes du site public (français) : en-tête / pied de page / navigation.
 *
 * 意图（Why）：
 *   公开站（未登录可见）的文案与门户、后台完全分离，单独成域便于并行维护与翻译。
 *
 * 流转（Flow）：
 *   ./site.ts → locales/fr/index.ts 聚合 → i18n/index.ts 的 messages → 组件 t('site.*')
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径都要补齐（index.tsx 用类型约束强制键集合一致）。
 */
export default {
  nav: {
    features: 'Fonctionnalités',
    quickstart: 'Démarrage',
    models: 'Modèles et tarifs',
    faq: 'FAQ',
  },
  header: {
    backHome: "Retour à l'accueil",
    openaiCompatible: 'Compatible OpenAI',
    login: 'Connexion',
    register: 'Inscription',
  },
  footer: {
    description:
      'Passerelle LLM compatible OpenAI : agrégation de plusieurs fournisseurs, facturation précise, journaux complets — auto-hébergeable en un seul binaire.',
    colSite: 'Site',
    colCompliance: 'Conformité',
    colOpenSource: 'Open source',
    features: 'Aperçu des capacités',
    quickstart: 'Guide de démarrage',
    models: 'Modèles et tarifs',
    faq: 'Questions fréquentes',
    terms: "Conditions d'utilisation",
    privacy: 'Politique de confidentialité',
    contact: 'Nous contacter',
    report: 'Signaler un abus',
    security: 'Remerciements sécurité',
    repo: 'Dépôt source (GitHub)',
    license: 'Licence MIT',
  },
}
