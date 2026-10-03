/**
 * 管理后台页面词条（法语 / Français）。
 *
 * 意图（Why）：
 *   后台 25 个页面的文案原先硬编码在 JSX 里，切到法语时整块界面仍是中文。
 *   把文案抽到词条层之后，语言切换才真正对后台生效。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言 → 页面通过 t('admin.channels.*') 读取。
 *
 * 扩展（Extend）：
 *   键集合必须与 zh-CN/admin.ts 完全一致（index.ts 用 satisfies 在编译期强制）。
 *   本文件是译文，改文案请先改中文基准再同步，避免两种语言说法漂移。
 */
export default {
  channels: {
    // ── 页头 ────────────────────────────────────────────────
    title: 'Canaux',
    emptyTitle: 'Aucun canal pour le moment',
    subtitle: 'Canaux amont ({total})',
    create: 'Nouveau canal',

    // ── 表格列头 ────────────────────────────────────────────
    col: {
      name: 'Nom',
      type: 'Type',
      models: 'Modèles',
      keyPool: 'Pool de clés',
      status: 'Statut',
      probe: 'Latence / sonde',
      actions: 'Actions',
    },

    // ── 表格单元格 ──────────────────────────────────────────
    cell: {
      /** 单渠道（没有配置密钥池）时的说明 */
      singleKey: 'Clé unique',
      /** 密钥池异常数徽标：{count} 异常 */
      degradedCount: '{count} en échec',
      /** 模型数量：{count} 个模型 */
      modelCount: '{count} modèles',
      /** 未限定模型（模型列表为空 = 转发全部） */
      allModels: 'Tous les modèles',
    },

    // ── 行内操作 ────────────────────────────────────────────
    action: {
      speedtest: 'Vitesse',
      probe: 'Sonde',
      cost: 'Tarif',
      edit: 'Modifier',
      disable: 'Désactiver',
      enable: 'Activer',
      delete: 'Supprimer',
    },

    // ── 测活结论（ProbeCell）─────────────────────────────────
    probe: {
      /** 尚未测过时的徽标 */
      notTested: 'Non sondé',
      /** 未测过的悬停说明——把"为什么没测"讲清楚，否则管理员会以为是 bug */
      notTestedHint:
        'La sonde de fond testera ce canal à son prochain cycle ; vous pouvez aussi le sonder maintenant',
      ok: 'Opérationnel',
      fail: 'En échec',
      /** 测活时间：测于 {time}（{relative}） */
      testedAt: 'Sondé {time} ({relative})',
      testedAtUnknown: 'Sondé à un moment inconnu',
      /** 测活所用模型：模型 {model} */
      testedModel: 'Modèle {model}',
      /** 有状态码时显示 HTTP xxx；无状态码说明连状态都没拿到 */
      networkDown: 'Réseau inaccessible',
    },

    /**
     * 状态码 → 下一步该做什么。
     * 只覆盖常见且处置方式明确的几种，其余不臆测——
     * 猜错处置方向比不给提示更糟。
     */
    probeHint: {
      /** 无状态码：连接都没建立 */
      unreachable: 'Injoignable : vérifiez l’URL amont, l’accès réseau et le DNS',
      /** 401/403：密钥问题 */
      authFailed: 'Authentification refusée : clé amont invalide ou expirée, remplacez-la',
      /** 404：模型名不对 */
      modelNotFound:
        'Modèle introuvable : corrigez les noms de modèles du canal ou utilisez ceux de l’amont',
      /** 429：限流 */
      rateLimited: 'Quota atteint : réduisez la fréquence des appels ou complétez le pool de clés',
      /** 5xx：上游自己的问题 */
      upstreamDown: 'Panne amont : attendez le rétablissement du fournisseur',
    },

    // ── 测活结果弹层 ────────────────────────────────────────
    result: {
      title: 'Résultat de la sonde',
      pass: 'Réussi',
      fail: 'Échec',
      /** 耗时 {ms} 毫秒 */
      elapsed: 'Durée {ms} ms',
      upstreamBody: 'Réponse amont',
    },

    // ── 删除确认 ────────────────────────────────────────────
    deleteDialog: {
      title: 'Supprimer le canal',
      /** 确认文案：确认删除渠道「{name}」？删除后该渠道的调用将立即失败。 */
      message: 'Supprimer le canal « {name} » ? Les appels via ce canal échoueront immédiatement.',
    },

    // ── 表单（新建 / 编辑共用）─────────────────────────────
    form: {
      createTitle: 'Nouveau canal',
      editTitle: 'Modifier le canal',
      name: 'Nom du canal',
      namePlaceholder: 'Donnez un nom à cet amont',
      type: 'Type de canal',
      typeFallbackPlaceholder: 'Identifiant du type de canal',
      baseUrl: 'URL amont',
      apiKey: 'Clé amont',
      /** 编辑态：留空即不修改 */
      apiKeyHelpKeep: 'Laisser vide pour conserver la clé actuelle',
      /** 新建态：说明同时支持密钥池 */
      apiKeyHelpNew:
        'Accepte aussi un pool de clés : une clé par ligne, avec une note facultative après une espace ou une virgule',
      keyPool: 'Pool de clés (facultatif)',
      keyPoolHelp: 'Une clé par ligne — collez plusieurs clés amont en une seule fois',
      strategy: 'Stratégie d’ordonnancement',
      failurePolicy: 'Gestion des échecs',
      /** 新建时的"不选 = 用后端默认"选项 */
      followDefault: 'Suivre la valeur par défaut du système',
      models: 'Liste des modèles',
      modelsHelp: 'Un par ligne ; vide signifie que tous les modèles sont acceptés',
      fetchModelsHint:
        'Vous pouvez récupérer la liste depuis l’amont — les résultats sont fusionnés dans le champ ci-dessous',
      fetchFromUpstream: 'Récupérer',
      fetchModels: 'Récupérer les modèles depuis l’amont',
      /** 拉取时用哪套凭据的说明 */
      useSavedCredential: 'Utilise l’URL et la clé enregistrées pour ce canal',
      useTypedCredential: 'Utilise l’URL et la clé saisies ci-dessus',
      /** 拉取结果条：拉取到 {count} 个模型 */
      fetchedCount: '{count} modèles récupérés',
      fillModels: 'Insérer dans la liste des modèles',
      copyList: 'Copier la liste',
      emptyFetched: 'L’amont a renvoyé une liste vide',
      enable: 'Activer le canal',
    },

    // ── 表单校验 ────────────────────────────────────────────
    validation: {
      /** 拉取模型时既没有已存凭据也没填地址 */
      fetchNeedsUrl: 'Saisissez d’abord une URL amont',
      nameRequired: 'Saisissez un nom de canal',
      keyRequired: 'Un nouveau canal exige une clé amont',
      urlRequired: 'Un nouveau canal exige une URL amont',
    },

    // ── 操作结果提示 ────────────────────────────────────────
    toast: {
      /** 拉取成功：拉取到 {count} 个模型，确认后点「填入模型列表」 */
      fetched: '{count} modèles récupérés — cliquez sur « Insérer dans la liste des modèles »',
      fetchFailed: 'Échec de la récupération',
      /** 合并结果：已填入：新增 {added} 个，当前共 {total} 个 */
      applied: 'Insertion : {added} ajoutés, {total} au total',
      created: 'Canal créé',
      updated: 'Canal mis à jour',
      deleted: 'Canal supprimé',
      deleteFailed: 'Échec de la suppression',
      probeFailed: 'Échec de la sonde',
      saveFailed: 'Échec de l’enregistrement',
    },

    // ── 表单底部按钮 ────────────────────────────────────────
    submitCreate: 'Créer',
    submitSave: 'Enregistrer',
    cancel: 'Annuler',
  },
}