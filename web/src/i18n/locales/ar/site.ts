/**
 * نصوص الموقع العام (العربية): الترويسة / التذييل / التنقّل.
 *
 * 意图（Why）：
 *   公开站（未登录可见）的文案与门户、后台完全分离，单独成域便于并行维护与翻译。
 *
 * 流转（Flow）：
 *   ./site.ts → locales/ar/index.ts 聚合 → i18n/index.ts 的 messages → 组件 t('site.*')
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径都要补齐（index.tsx 用类型约束强制键集合一致）。
 */
export default {
  nav: {
    features: 'الإمكانات',
    quickstart: 'البدء السريع',
    models: 'النماذج والأسعار',
    faq: 'الأسئلة الشائعة',
  },
  header: {
    backHome: 'العودة إلى الرئيسية',
    openaiCompatible: 'متوافق مع OpenAI',
    login: 'تسجيل الدخول',
    register: 'إنشاء حساب',
  },
  footer: {
    description:
      'بوابة LLM متوافقة مع OpenAI: توحيد مزوّدي خدمات متعددين، فوترة دقيقة، سجلات كاملة — قابلة للاستضافة الذاتية بملف تنفيذي واحد.',
    colSite: 'الموقع',
    colCompliance: 'الامتثال',
    colOpenSource: 'المصدر المفتوح',
    features: 'نظرة على الإمكانات',
    quickstart: 'دليل البدء',
    models: 'النماذج والأسعار',
    faq: 'الأسئلة الشائعة',
    terms: 'شروط الخدمة',
    privacy: 'سياسة الخصوصية',
    contact: 'اتصل بنا',
    report: 'الإبلاغ عن إساءة',
    security: 'شكر أمني',
    repo: 'مستودع الشيفرة (GitHub)',
    license: 'رخصة MIT',
  },
}
