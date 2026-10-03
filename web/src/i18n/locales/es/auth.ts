/**
 * 认证页词条（Español）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/es/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以 zh-CN 为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: 'Iniciar sesión',
    subtitle: 'Inicia sesión con tu usuario o tu correo vinculado',
    tabPassword: 'Contraseña',
    tabEmail: 'Código por correo',
    usernameLabel: 'Usuario / Correo',
    usernamePlaceholder: 'Usuario o correo vinculado',
    passwordLabel: 'Contraseña',
    passwordPlaceholder: 'Contraseña',
    emailLabel: 'Correo',
    codeLabel: 'Código',
    codeHelp: 'El código se envía a este correo y es válido durante 5 minutos',
    codePlaceholder: 'Código de 6 dígitos',
    submit: 'Iniciar sesión',
    registerLink: 'Crear una cuenta',
    forgotLink: '¿Olvidaste tu contraseña?',
    errUsernamePassword: 'Introduce tu usuario/correo y tu contraseña',
    errEmailCode: 'Introduce tu correo y el código',
    errFailed: 'No se pudo iniciar sesión',
    errEmail: 'Introduce primero un correo válido',
    codeSent: 'Código enviado, revisa tu bandeja de entrada',
    sendFailed: 'No se pudo enviar',
    sendCode: 'Enviar código',
  },
  register: {
    title: 'Registrarse',
    subtitle: 'Crea una cuenta y empieza a usar los modelos de inmediato',
    usernameLabel: 'Usuario',
    usernamePlaceholder: 'De 2 a 32 caracteres: letras, dígitos o guiones bajos',
    passwordLabel: 'Contraseña',
    passwordHelp: 'Al menos 8 caracteres',
    passwordPlaceholder: 'Al menos 8 caracteres',
    confirmLabel: 'Confirmar contraseña',
    confirmPlaceholder: 'Vuelve a introducir la contraseña',
    emailLabel: 'Correo',
    emailHelp: 'El código de verificación se enviará a este correo',
    emailHelpNotReady: 'El servicio de correo no está listo; puede que no recibas el código',
    codeLabel: 'Código por correo',
    codePlaceholder: 'Código de 6 dígitos',
    agreePrefix: 'He leído y acepto los ',
    agreeTerms: 'Términos de servicio',
    agreeAnd: ' y la ',
    agreePrivacy: 'Política de privacidad',
    submit: 'Registrarse',
    haveAccount: '¿Ya tienes una cuenta?',
    loginLink: 'Iniciar sesión',
    errUsernamePassword: 'El usuario es obligatorio y la contraseña debe tener al menos 8 caracteres',
    errPasswordMismatch: 'Las dos contraseñas no coinciden',
    errAgree: 'Lee y acepta primero los Términos de servicio y la Política de privacidad',
    errEmailRequired: 'El registro en este sitio requiere verificación por correo; introduce tu correo',
    errFailed: 'No se pudo registrar',
    errEmail: 'Introduce primero un correo válido',
    codeSent: 'Código enviado',
    sendFailed: 'No se pudo enviar',
    sendCode: 'Enviar código',
  },
  forgot: {
    title: 'Restablecer contraseña',
    subtitle: 'Restablece tu contraseña mediante un correo verificado',
    emailLabel: 'Correo',
    codeLabel: 'Código',
    codePlaceholder: 'Código de 6 dígitos',
    newPasswordLabel: 'Nueva contraseña',
    newPasswordHelp: 'Al menos 8 caracteres',
    newPasswordPlaceholder: 'Nueva contraseña',
    submit: 'Restablecer contraseña',
    remember: '¿Recordaste tu contraseña?',
    loginLink: 'Iniciar sesión',
    errEmail: 'Introduce primero un correo válido',
    sendFailed: 'No se pudo enviar',
    sendCode: 'Enviar código',
    codeSent: 'Código enviado',
    errIncomplete: 'Completa el correo, el código y la nueva contraseña (al menos 8 caracteres)',
    errFailed: 'No se pudo restablecer',
    resetSuccess: 'Contraseña restablecida. Inicia sesión con tu nueva contraseña',
  },
}
