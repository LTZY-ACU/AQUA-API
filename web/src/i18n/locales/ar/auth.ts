/**
 * 认证页词条（العربية）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/ar/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以 zh-CN 为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: 'تسجيل الدخول',
    subtitle: 'سجّل الدخول باسم المستخدم أو البريد المرتبط',
    tabPassword: 'كلمة المرور',
    tabEmail: 'رمز البريد',
    usernameLabel: 'اسم المستخدم / البريد',
    usernamePlaceholder: 'اسم المستخدم أو البريد المرتبط',
    passwordLabel: 'كلمة المرور',
    passwordPlaceholder: 'كلمة المرور',
    emailLabel: 'البريد',
    codeLabel: 'الرمز',
    codeHelp: 'يُرسَل الرمز إلى هذا البريد وهو صالح 5 دقائق',
    codePlaceholder: 'رمز من 6 أرقام',
    submit: 'تسجيل الدخول',
    registerLink: 'إنشاء حساب جديد',
    forgotLink: 'نسيت كلمة المرور؟',
    errUsernamePassword: 'يُرجى إدخال اسم المستخدم/البريد وكلمة المرور',
    errEmailCode: 'يُرجى إدخال البريد والرمز',
    errFailed: 'تعذّر تسجيل الدخول',
    errEmail: 'يُرجى إدخال بريد صحيح أولًا',
    codeSent: 'أُرسل الرمز، يُرجى مراجعة بريدك',
    sendFailed: 'تعذّر الإرسال',
    sendCode: 'إرسال الرمز',
  },
  register: {
    title: 'إنشاء حساب',
    subtitle: 'أنشئ حسابًا وابدأ استخدام النماذج فورًا',
    usernameLabel: 'اسم المستخدم',
    usernamePlaceholder: 'من 2 إلى 32 محرفًا: حروف أو أرقام أو شرطات سفلية',
    passwordLabel: 'كلمة المرور',
    passwordHelp: '8 محارف على الأقل',
    passwordPlaceholder: '8 محارف على الأقل',
    confirmLabel: 'تأكيد كلمة المرور',
    confirmPlaceholder: 'أعد إدخال كلمة المرور',
    emailLabel: 'البريد',
    emailHelp: 'سيُرسَل رمز التحقق إلى هذا البريد',
    emailHelpNotReady: 'خدمة البريد غير جاهزة؛ قد لا يصلك الرمز',
    codeLabel: 'رمز البريد',
    codePlaceholder: 'رمز من 6 أرقام',
    agreePrefix: 'قرأتُ وأوافق على ',
    agreeTerms: 'شروط الخدمة',
    agreeAnd: ' و ',
    agreePrivacy: 'سياسة الخصوصية',
    submit: 'إنشاء الحساب',
    haveAccount: 'لديك حساب بالفعل؟',
    loginLink: 'تسجيل الدخول',
    errUsernamePassword: 'اسم المستخدم مطلوب وكلمة المرور 8 محارف على الأقل',
    errPasswordMismatch: 'كلمتا المرور غير متطابقتين',
    errAgree: 'يُرجى قراءة شروط الخدمة وسياسة الخصوصية والموافقة عليهما أولًا',
    errEmailRequired: 'يتطلب التسجيل في هذا الموقع تحققًا بالبريد؛ يُرجى إدخال بريدك',
    errFailed: 'تعذّر إنشاء الحساب',
    errEmail: 'يُرجى إدخال بريد صحيح أولًا',
    codeSent: 'أُرسل الرمز',
    sendFailed: 'تعذّر الإرسال',
    sendCode: 'إرسال الرمز',
  },
  forgot: {
    title: 'إعادة تعيين كلمة المرور',
    subtitle: 'أعد تعيين كلمة مرورك عبر بريد موثّق',
    emailLabel: 'البريد',
    codeLabel: 'الرمز',
    codePlaceholder: 'رمز من 6 أرقام',
    newPasswordLabel: 'كلمة المرور الجديدة',
    newPasswordHelp: '8 محارف على الأقل',
    newPasswordPlaceholder: 'كلمة المرور الجديدة',
    submit: 'إعادة تعيين كلمة المرور',
    remember: 'تذكّرت كلمة المرور؟',
    loginLink: 'تسجيل الدخول',
    errEmail: 'يُرجى إدخال بريد صحيح أولًا',
    sendFailed: 'تعذّر الإرسال',
    sendCode: 'إرسال الرمز',
    codeSent: 'أُرسل الرمز',
    errIncomplete: 'يُرجى إكمال البريد والرمز وكلمة المرور الجديدة (8 محارف على الأقل)',
    errFailed: 'تعذّرت إعادة التعيين',
    resetSuccess: 'تمت إعادة تعيين كلمة المرور. سجّل الدخول بكلمة المرور الجديدة',
  },
}
