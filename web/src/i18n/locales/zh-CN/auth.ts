/**
 * 认证页词条（简体中文）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/zh-CN/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以本文件为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: '登录',
    subtitle: '使用用户名或绑定邮箱登录',
    tabPassword: '密码登录',
    tabEmail: '验证码登录',
    usernameLabel: '用户名 / 邮箱',
    usernamePlaceholder: '用户名或绑定邮箱',
    passwordLabel: '密码',
    passwordPlaceholder: '密码',
    emailLabel: '邮箱',
    codeLabel: '验证码',
    codeHelp: '验证码发送到该邮箱，5 分钟内有效',
    codePlaceholder: '6 位验证码',
    submit: '登录',
    registerLink: '注册新账号',
    forgotLink: '忘记密码？',
    errUsernamePassword: '请输入用户名/邮箱与密码',
    errEmailCode: '请输入邮箱与验证码',
    errFailed: '登录失败',
    errEmail: '请先填写正确的邮箱',
    codeSent: '验证码已发送，请查收邮件',
    sendFailed: '发送失败',
    sendCode: '发送验证码',
  },
  register: {
    title: '注册',
    subtitle: '创建账号，马上接入模型',
    usernameLabel: '用户名',
    usernamePlaceholder: '2-32 位，字母数字或下划线',
    passwordLabel: '密码',
    passwordHelp: '至少 8 位',
    passwordPlaceholder: '至少 8 位',
    confirmLabel: '确认密码',
    confirmPlaceholder: '再次输入密码',
    emailLabel: '邮箱',
    emailHelp: '验证码将发送到该邮箱',
    emailHelpNotReady: '邮件服务未就绪，可能收不到验证码',
    codeLabel: '邮箱验证码',
    codePlaceholder: '6 位验证码',
    agreePrefix: '我已阅读并同意 ',
    agreeTerms: '《用户协议》',
    agreeAnd: ' 与 ',
    agreePrivacy: '《隐私政策》',
    submit: '注册',
    haveAccount: '已有账号？',
    loginLink: '去登录',
    errUsernamePassword: '用户名必填，密码至少 8 位',
    errPasswordMismatch: '两次输入的密码不一致',
    errAgree: '请先阅读并同意《用户协议》与《隐私政策》',
    errEmailRequired: '本站注册需要邮箱验证，请填写邮箱',
    errFailed: '注册失败',
    errEmail: '请先填写正确的邮箱',
    codeSent: '验证码已发送',
    sendFailed: '发送失败',
    sendCode: '发送验证码',
  },
  forgot: {
    title: '重置密码',
    subtitle: '通过已验证邮箱重置登录密码',
    emailLabel: '邮箱',
    codeLabel: '验证码',
    codePlaceholder: '6 位验证码',
    newPasswordLabel: '新密码',
    newPasswordHelp: '至少 8 位',
    newPasswordPlaceholder: '新密码',
    submit: '重置密码',
    remember: '想起密码了？',
    loginLink: '去登录',
    errEmail: '请先填写正确的邮箱',
    sendFailed: '发送失败',
    sendCode: '发送验证码',
    codeSent: '验证码已发送',
    errIncomplete: '请完整填写邮箱、验证码与新密码（至少 8 位）',
    errFailed: '重置失败',
    resetSuccess: '密码已重置，请用新密码登录',
  },
}
