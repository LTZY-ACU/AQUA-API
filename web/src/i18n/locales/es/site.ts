/**
 * Textos del sitio público (español): encabezado / pie de página / navegación.
 *
 * 意图（Why）：
 *   公开站（未登录可见）的文案与门户、后台完全分离，单独成域便于并行维护与翻译。
 *
 * 流转（Flow）：
 *   ./site.ts → locales/es/index.ts 聚合 → i18n/index.ts 的 messages → 组件 t('site.*')
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径都要补齐（index.tsx 用类型约束强制键集合一致）。
 */
export default {
  nav: {
    features: 'Capacidades',
    quickstart: 'Inicio rápido',
    models: 'Modelos y precios',
    faq: 'Preguntas frecuentes',
  },
  header: {
    backHome: 'Volver al inicio',
    openaiCompatible: 'Compatible con OpenAI',
    login: 'Iniciar sesión',
    register: 'Registrarse',
  },
  footer: {
    description:
      'Pasarela LLM compatible con OpenAI: múltiples proveedores unificados, facturación precisa, registros completos — autoalojable en un solo binario.',
    colSite: 'Sitio',
    colCompliance: 'Cumplimiento',
    colOpenSource: 'Código abierto',
    features: 'Resumen de capacidades',
    quickstart: 'Guía de inicio',
    models: 'Modelos y precios',
    faq: 'Preguntas frecuentes',
    terms: 'Términos de servicio',
    privacy: 'Política de privacidad',
    contact: 'Contacto',
    report: 'Reportar abuso',
    security: 'Agradecimientos de seguridad',
    repo: 'Repositorio (GitHub)',
    license: 'Licencia MIT',
  },
}
