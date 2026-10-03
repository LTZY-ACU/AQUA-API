/**
 * 认证页词条（English）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/en/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以 zh-CN 为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: 'Sign in',
    subtitle: 'Sign in with your username or linked email',
    tabPassword: 'Password',
    tabEmail: 'Email code',
    usernameLabel: 'Username / Email',
    usernamePlaceholder: 'Username or linked email',
    passwordLabel: 'Password',
    passwordPlaceholder: 'Password',
    emailLabel: 'Email',
    codeLabel: 'Code',
    codeHelp: 'The code is sent to this email and is valid for 5 minutes',
    codePlaceholder: '6-digit code',
    submit: 'Sign in',
    registerLink: 'Create an account',
    forgotLink: 'Forgot password?',
    errUsernamePassword: 'Please enter your username/email and password',
    errEmailCode: 'Please enter your email and code',
    errFailed: 'Sign-in failed',
    errEmail: 'Please enter a valid email first',
    codeSent: 'Code sent, please check your inbox',
    sendFailed: 'Failed to send',
    sendCode: 'Send code',
  },
  register: {
    title: 'Sign up',
    subtitle: 'Create an account and start using models right away',
    usernameLabel: 'Username',
    usernamePlaceholder: '2–32 characters: letters, digits or underscores',
    passwordLabel: 'Password',
    passwordHelp: 'At least 8 characters',
    passwordPlaceholder: 'At least 8 characters',
    confirmLabel: 'Confirm password',
    confirmPlaceholder: 'Re-enter your password',
    emailLabel: 'Email',
    emailHelp: 'The verification code will be sent to this email',
    emailHelpNotReady: 'Email service is not ready; you may not receive the code',
    codeLabel: 'Email code',
    codePlaceholder: '6-digit code',
    agreePrefix: 'I have read and agree to the ',
    agreeTerms: 'Terms of Service',
    agreeAnd: ' and ',
    agreePrivacy: 'Privacy Policy',
    submit: 'Sign up',
    haveAccount: 'Already have an account?',
    loginLink: 'Sign in',
    errUsernamePassword: 'Username is required and the password must be at least 8 characters',
    errPasswordMismatch: 'The two passwords do not match',
    errAgree: 'Please read and agree to the Terms of Service and Privacy Policy first',
    errEmailRequired: 'Registration on this site requires email verification; please enter your email',
    errFailed: 'Sign-up failed',
    errEmail: 'Please enter a valid email first',
    codeSent: 'Code sent',
    sendFailed: 'Failed to send',
    sendCode: 'Send code',
  },
  forgot: {
    title: 'Reset password',
    subtitle: 'Reset your password via a verified email',
    emailLabel: 'Email',
    codeLabel: 'Code',
    codePlaceholder: '6-digit code',
    newPasswordLabel: 'New password',
    newPasswordHelp: 'At least 8 characters',
    newPasswordPlaceholder: 'New password',
    submit: 'Reset password',
    remember: 'Remembered your password?',
    loginLink: 'Sign in',
    errEmail: 'Please enter a valid email first',
    sendFailed: 'Failed to send',
    sendCode: 'Send code',
    codeSent: 'Code sent',
    errIncomplete: 'Please fill in the email, code and new password completely (at least 8 characters)',
    errFailed: 'Reset failed',
    resetSuccess: 'Password reset. Please sign in with your new password',
  },
}
