/**
 * 认证页词条（Русский）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/ru/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以 zh-CN 为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: 'Вход',
    subtitle: 'Войдите по имени пользователя или привязанной почте',
    tabPassword: 'Пароль',
    tabEmail: 'Код из письма',
    usernameLabel: 'Имя пользователя / Почта',
    usernamePlaceholder: 'Имя пользователя или привязанная почта',
    passwordLabel: 'Пароль',
    passwordPlaceholder: 'Пароль',
    emailLabel: 'Почта',
    codeLabel: 'Код',
    codeHelp: 'Код отправляется на эту почту и действует 5 минут',
    codePlaceholder: 'Код из 6 цифр',
    submit: 'Войти',
    registerLink: 'Создать аккаунт',
    forgotLink: 'Забыли пароль?',
    errUsernamePassword: 'Введите имя пользователя/почту и пароль',
    errEmailCode: 'Введите почту и код',
    errFailed: 'Не удалось войти',
    errEmail: 'Сначала укажите корректную почту',
    codeSent: 'Код отправлен, проверьте почту',
    sendFailed: 'Не удалось отправить',
    sendCode: 'Отправить код',
  },
  register: {
    title: 'Регистрация',
    subtitle: 'Создайте аккаунт и сразу начните работать с моделями',
    usernameLabel: 'Имя пользователя',
    usernamePlaceholder: 'От 2 до 32 символов: буквы, цифры или подчёркивания',
    passwordLabel: 'Пароль',
    passwordHelp: 'Не менее 8 символов',
    passwordPlaceholder: 'Не менее 8 символов',
    confirmLabel: 'Подтвердите пароль',
    confirmPlaceholder: 'Введите пароль ещё раз',
    emailLabel: 'Почта',
    emailHelp: 'Код подтверждения будет отправлен на эту почту',
    emailHelpNotReady: 'Почтовый сервис не готов; код может не прийти',
    codeLabel: 'Код из письма',
    codePlaceholder: 'Код из 6 цифр',
    agreePrefix: 'Я прочитал и принимаю ',
    agreeTerms: 'Пользовательское соглашение',
    agreeAnd: ' и ',
    agreePrivacy: 'Политику конфиденциальности',
    submit: 'Зарегистрироваться',
    haveAccount: 'Уже есть аккаунт?',
    loginLink: 'Войти',
    errUsernamePassword: 'Имя пользователя обязательно, а пароль должен содержать не менее 8 символов',
    errPasswordMismatch: 'Пароли не совпадают',
    errAgree: 'Сначала прочитайте и примите Пользовательское соглашение и Политику конфиденциальности',
    errEmailRequired: 'Для регистрации на этом сайте требуется подтверждение почты; укажите адрес',
    errFailed: 'Не удалось зарегистрироваться',
    errEmail: 'Сначала укажите корректную почту',
    codeSent: 'Код отправлен',
    sendFailed: 'Не удалось отправить',
    sendCode: 'Отправить код',
  },
  forgot: {
    title: 'Сброс пароля',
    subtitle: 'Сбросьте пароль через подтверждённую почту',
    emailLabel: 'Почта',
    codeLabel: 'Код',
    codePlaceholder: 'Код из 6 цифр',
    newPasswordLabel: 'Новый пароль',
    newPasswordHelp: 'Не менее 8 символов',
    newPasswordPlaceholder: 'Новый пароль',
    submit: 'Сбросить пароль',
    remember: 'Вспомнили пароль?',
    loginLink: 'Войти',
    errEmail: 'Сначала укажите корректную почту',
    sendFailed: 'Не удалось отправить',
    sendCode: 'Отправить код',
    codeSent: 'Код отправлен',
    errIncomplete: 'Полностью заполните почту, код и новый пароль (не менее 8 символов)',
    errFailed: 'Не удалось сбросить',
    resetSuccess: 'Пароль сброшен. Войдите с новым паролем',
  },
}
