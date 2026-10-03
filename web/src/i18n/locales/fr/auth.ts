/**
 * 认证页词条（Français）：登录 / 注册 / 重置密码。
 *
 * 意图（Why）：
 *   登录、注册、忘记密码三页的用户可见文案独立成域，与站点公共文案（site.ts）
 *   分离，便于并行维护与翻译；挂载后键前缀统一为 site.auth.*。
 *
 * 流转（Flow）：
 *   ./auth.ts → locales/fr/index.ts 聚合（挂到 site.auth 命名空间）→
 *   i18n/index.ts 的 messages → 组件 t('site.auth.login.xxx') 等
 *
 * 扩展（Extend）：
 *   新增键时六种语言的 auth.ts 同一路径都要补齐（键集合以 zh-CN 为基准，
 *   i18n/index.tsx 用类型约束强制一致）。
 */
export default {
  login: {
    title: 'Connexion',
    subtitle: 'Connectez-vous avec votre nom d’utilisateur ou votre e-mail associé',
    tabPassword: 'Mot de passe',
    tabEmail: 'Code e-mail',
    usernameLabel: 'Nom d’utilisateur / E-mail',
    usernamePlaceholder: 'Nom d’utilisateur ou e-mail associé',
    passwordLabel: 'Mot de passe',
    passwordPlaceholder: 'Mot de passe',
    emailLabel: 'E-mail',
    codeLabel: 'Code',
    codeHelp: 'Le code est envoyé à cette adresse et valable 5 minutes',
    codePlaceholder: 'Code à 6 chiffres',
    submit: 'Se connecter',
    registerLink: 'Créer un compte',
    forgotLink: 'Mot de passe oublié ?',
    errUsernamePassword: 'Veuillez saisir votre nom d’utilisateur/e-mail et votre mot de passe',
    errEmailCode: 'Veuillez saisir votre e-mail et le code',
    errFailed: 'Échec de la connexion',
    errEmail: 'Veuillez d’abord saisir une adresse e-mail valide',
    codeSent: 'Code envoyé, veuillez consulter votre boîte de réception',
    sendFailed: 'Échec de l’envoi',
    sendCode: 'Envoyer le code',
  },
  register: {
    title: 'Inscription',
    subtitle: 'Créez un compte et commencez à utiliser les modèles immédiatement',
    usernameLabel: 'Nom d’utilisateur',
    usernamePlaceholder: '2 à 32 caractères : lettres, chiffres ou tirets bas',
    passwordLabel: 'Mot de passe',
    passwordHelp: 'Au moins 8 caractères',
    passwordPlaceholder: 'Au moins 8 caractères',
    confirmLabel: 'Confirmer le mot de passe',
    confirmPlaceholder: 'Saisissez à nouveau le mot de passe',
    emailLabel: 'E-mail',
    emailHelp: 'Le code de vérification sera envoyé à cette adresse',
    emailHelpNotReady: 'Le service e-mail n’est pas prêt ; vous risquez de ne pas recevoir le code',
    codeLabel: 'Code e-mail',
    codePlaceholder: 'Code à 6 chiffres',
    agreePrefix: 'J’ai lu et j’accepte les ',
    agreeTerms: 'Conditions d’utilisation',
    agreeAnd: ' et la ',
    agreePrivacy: 'Politique de confidentialité',
    submit: 'S’inscrire',
    haveAccount: 'Vous avez déjà un compte ?',
    loginLink: 'Se connecter',
    errUsernamePassword: 'Le nom d’utilisateur est requis et le mot de passe doit comporter au moins 8 caractères',
    errPasswordMismatch: 'Les deux mots de passe ne correspondent pas',
    errAgree: 'Veuillez d’abord lire et accepter les Conditions d’utilisation et la Politique de confidentialité',
    errEmailRequired: 'L’inscription sur ce site exige une vérification par e-mail ; veuillez saisir votre adresse',
    errFailed: 'Échec de l’inscription',
    errEmail: 'Veuillez d’abord saisir une adresse e-mail valide',
    codeSent: 'Code envoyé',
    sendFailed: 'Échec de l’envoi',
    sendCode: 'Envoyer le code',
  },
  forgot: {
    title: 'Réinitialiser le mot de passe',
    subtitle: 'Réinitialisez votre mot de passe via une adresse e-mail vérifiée',
    emailLabel: 'E-mail',
    codeLabel: 'Code',
    codePlaceholder: 'Code à 6 chiffres',
    newPasswordLabel: 'Nouveau mot de passe',
    newPasswordHelp: 'Au moins 8 caractères',
    newPasswordPlaceholder: 'Nouveau mot de passe',
    submit: 'Réinitialiser le mot de passe',
    remember: 'Vous vous souvenez de votre mot de passe ?',
    loginLink: 'Se connecter',
    errEmail: 'Veuillez d’abord saisir une adresse e-mail valide',
    sendFailed: 'Échec de l’envoi',
    sendCode: 'Envoyer le code',
    codeSent: 'Code envoyé',
    errIncomplete: 'Veuillez renseigner l’e-mail, le code et le nouveau mot de passe (au moins 8 caractères)',
    errFailed: 'Échec de la réinitialisation',
    resetSuccess: 'Mot de passe réinitialisé. Veuillez vous connecter avec votre nouveau mot de passe',
  },
}
