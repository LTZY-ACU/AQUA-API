/**
 * 通用词条（法语 / Français）。
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
    save: 'Enregistrer',
    cancel: 'Annuler',
    confirm: 'Confirmer',
    close: 'Fermer',
    delete: 'Supprimer',
    edit: 'Modifier',
    create: 'Créer',
    copy: 'Copier',
    copied: 'Copié',
    refresh: 'Actualiser',
    retry: 'Recharger',
    search: 'Rechercher',
    reset: 'Réinitialiser',
    submit: 'Envoyer',
    back: 'Retour',
    view: 'Voir',
    open: 'Ouvrir',
    more: 'Plus',
    enable: 'Activer',
    disable: 'Désactiver',
    restore: 'Restaurer',
    addRow: 'Ajouter une ligne',
    stop: 'Arrêter',
  },
  state: {
    loading: 'Chargement…',
    empty: 'Aucune donnée',
    enabled: 'Activé',
    disabled: 'Désactivé',
    removed: 'Retiré',
    notSet: 'Non renseigné',
    available: 'Disponible',
    unavailable: 'Indisponible',
    success: 'Réussi',
    failed: 'Échec',
  },
  unit: {
    items: 'éléments',
    days: 'jours',
  },
  toast: {
    operationFailed: 'Une erreur est survenue. Veuillez réessayer plus tard.',
    loadFailed: 'Échec du chargement',
    saveFailed: 'Échec de l’enregistrement',
  },
  language: {
    label: 'Langue',
    switch: 'Changer de langue',
  },
  confirm: {
    defaultMessage: 'Voulez-vous vraiment effectuer cette action ?',
  },
  value: {
    neverExpires: 'N’expire jamais',
    unlimitedQuota: 'Quota illimité',
    other: 'Autre',
  },
  money: {
    quotaUnit: 'crédits',
    originalPrice: 'Prix initial',
    discount: '{ratio} % du prix',
    perCall: '/appel',
  },
  api: {
    timeout: 'Délai de requête dépassé. Vérifiez votre réseau et réessayez.',
    network: 'Impossible de joindre le serveur. Vérifiez que le service backend est démarré.',
    http400: 'Paramètres de requête invalides',
    http401: 'Votre session a expiré. Veuillez vous reconnecter.',
    http403: 'Vous n’avez pas la permission d’effectuer cette action',
    http404: 'La ressource demandée n’existe pas',
    http409: 'Conflit : l’enregistrement existe peut-être déjà',
    http429: 'Trop de requêtes, ou votre quota est épuisé',
    http500: 'Erreur du serveur. Réessayez plus tard.',
    http503: 'Aucun canal en amont n’est disponible pour le moment',
    httpGeneric: 'Échec de la requête (HTTP {status})',
    exportConnectFailed: 'Impossible de joindre le serveur. Échec de l’export.',
    exportFailed: 'Échec de l’export (HTTP {status})',
    downloadFailed: 'Échec du téléchargement (HTTP {status})',
  },
}
