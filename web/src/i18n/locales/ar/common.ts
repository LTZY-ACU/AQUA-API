/**
 * 通用词条（阿拉伯语 / العربية，RTL）。
 *
 * 意图（Why）：
 *   「动作 / 状态 / 单位 / 提示」等跨页面复用短词；六种语言键集合必须一致。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言词条 → 组件通过 $t('common.action.save') 读取。
 *
 * 扩展（Extend）：
 *   新增词条时六种语言同步补齐（键集合由 index.ts 类型约束强制）。
 */
export default {
  action: {
    save: 'حفظ',
    cancel: 'إلغاء',
    confirm: 'تأكيد',
    close: 'إغلاق',
    delete: 'حذف',
    edit: 'تعديل',
    create: 'إنشاء',
    copy: 'نسخ',
    copied: 'تم النسخ',
    refresh: 'تحديث',
    retry: 'إعادة التحميل',
    search: 'بحث',
    reset: 'إعادة الضبط',
    submit: 'إرسال',
    back: 'رجوع',
    view: 'عرض',
    open: 'فتح',
    more: 'المزيد',
    enable: 'تفعيل',
    disable: 'تعطيل',
    restore: 'استعادة',
    addRow: 'إضافة صف',
    stop: 'إيقاف',
  },
  state: {
    loading: 'جارٍ التحميل…',
    empty: 'لا توجد بيانات',
    enabled: 'مُفعّل',
    disabled: 'مُعطّل',
    removed: 'مُزال',
    notSet: 'غير مُدخل',
    available: 'متاح',
    unavailable: 'غير متاح',
    success: 'نجح',
    failed: 'فشل',
  },
  unit: {
    items: 'عناصر',
    days: 'أيام',
  },
  toast: {
    operationFailed: 'حدث خطأ. يرجى المحاولة مرة أخرى لاحقًا.',
    loadFailed: 'فشل التحميل',
    saveFailed: 'فشل الحفظ',
  },
  language: {
    label: 'اللغة',
    switch: 'تغيير اللغة',
  },
  confirm: {
    defaultMessage: 'هل أنت متأكد من تنفيذ هذا الإجراء؟',
  },
  value: {
    neverExpires: 'لا ينتهي أبدًا',
    unlimitedQuota: 'حصة غير محدودة',
    other: 'أخرى',
  },
  money: {
    quotaUnit: 'حصة',
    originalPrice: 'السعر الأصلي',
    discount: '{ratio}% من السعر',
    perCall: '/طلب',
  },
  api: {
    timeout: 'انتهت مهلة الطلب. تحقق من الشبكة وحاول مرة أخرى.',
    network: 'تعذّر الاتصال بالخادم. تأكد من تشغيل خدمة الواجهة الخلفية.',
    http400: 'معطيات الطلب غير صحيحة',
    http401: 'انتهت صلاحية الجلسة. يُرجى تسجيل الدخول مجددًا.',
    http403: 'ليس لديك إذن لتنفيذ هذا الإجراء',
    http404: 'المورد المطلوب غير موجود',
    http409: 'تعارض: قد يكون السجل موجودًا بالفعل',
    http429: 'طلبات كثيرة جدًا أو نفاد الحصة',
    http500: 'حدث خطأ في الخادم. حاول لاحقًا.',
    http503: 'لا توجد قناة مصدرية متاحة حاليًا',
    httpGeneric: 'فشل الطلب (HTTP {status})',
    exportConnectFailed: 'تعذّر الاتصال بالخادم. فشل التصدير.',
    exportFailed: 'فشل التصدير (HTTP {status})',
    downloadFailed: 'فشل التنزيل (HTTP {status})',
  },
}
