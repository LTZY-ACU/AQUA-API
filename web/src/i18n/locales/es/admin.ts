/**
 * 管理后台页面词条（西班牙语 / Español）。
 *
 * 意图（Why）：
 *   后台 25 个页面的文案原先硬编码在 JSX 里，切到西班牙语时整块界面仍是中文。
 *   把文案抽到词条层之后，语言切换才真正对后台生效。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言 → 页面通过 t('admin.channels.*') 读取。
 *
 * 扩展（Extend）：
 *   键集合必须与 zh-CN/admin.ts 完全一致（index.ts 用 satisfies 在编译期强制）。
 *   本文件是译文，改文案请先改中文基准再同步，避免两种语言说法漂移。
 *
 * 排版（Typography）：
 *   数字与名词之间用窄不换行空格（U+202F），如 «{count}» + «modelos»，
 *   避免数字与单位被拆到两行；其余位置用普通空格。
 */
export default {
  channels: {
    // ── 页头 ────────────────────────────────────────────────
    title: 'Gestión de canales',
    emptyTitle: 'Aún no hay canales',
    subtitle: 'Canales upstream ({total})',
    create: 'Nuevo canal',

    // ── 表格列头 ────────────────────────────────────────────
    col: {
      name: 'Nombre',
      type: 'Tipo',
      models: 'Modelos',
      keyPool: 'Pool de claves',
      status: 'Estado',
      probe: 'Latencia / sonda',
      actions: 'Acciones',
    },

    // ── 表格单元格 ──────────────────────────────────────────
    cell: {
      /** 单渠道（没有配置密钥池）时的说明 */
      singleKey: 'Clave única',
      /** 密钥池异常数徽标：{count} 异常 */
      degradedCount: '{count} con errores',
      /** 模型数量：{count} 个模型 */
      modelCount: '{count} modelos',
      /** 未限定模型（模型列表为空 = 转发全部） */
      allModels: 'Todos los modelos',
    },

    // ── 行内操作 ────────────────────────────────────────────
    action: {
      speedtest: 'Velocidad',
      probe: 'Sonda',
      cost: 'Coste',
      edit: 'Editar',
      disable: 'Desactivar',
      enable: 'Activar',
      delete: 'Eliminar',
    },

    // ── 测活结论（ProbeCell）─────────────────────────────────
    probe: {
      /** 尚未测过时的徽标 */
      notTested: 'Sin probar',
      /** 未测过的悬停说明——把"为什么没测"讲清楚，否则管理员会以为是 bug */
      notTestedHint:
        'La supervisión en segundo plano sondeará el canal en su próximo ciclo; también puedes sondarlo ahora',
      ok: 'Correcto',
      fail: 'Con errores',
      /** 测活时间：测于 {time}（{relative}） */
      testedAt: 'Sondeado {time} ({relative})',
      testedAtUnknown: 'Sondeado en un momento desconocido',
      /** 测活所用模型：模型 {model} */
      testedModel: 'Modelo {model}',
      /** 有状态码时显示 HTTP xxx；无状态码说明连状态都没拿到 */
      networkDown: 'Red no accesible',
    },

    /**
     * 状态码 → 下一步该做什么。
     * 只覆盖常见且处置方式明确的几种，其余不臆测——
     * 猜错处置方向比不给提示更糟。
     */
    probeHint: {
      /** 无状态码：连接都没建立 */
      unreachable: 'No se puede conectar: revisa la URL upstream, el acceso a Internet y el DNS',
      /** 401/403：密钥问题 */
      authFailed: 'Error de autenticación: la clave upstream no es válida o ha caducado; cámbiala',
      /** 404：模型名不对 */
      modelNotFound:
        'El modelo no existe: corrige los nombres de modelo del canal o usa los del upstream',
      /** 429：限流 */
      rateLimited: 'Límite de peticiones: reduce la frecuencia de llamada o amplía el pool de claves',
      /** 5xx：上游自己的问题 */
      upstreamDown: 'Upstream caído: espera a que se recupere',
    },

    // ── 测活结果弹层 ────────────────────────────────────────
    result: {
      title: 'Resultado de la sonda del canal',
      pass: 'Aprobada',
      fail: 'Fallida',
      /** 耗时 {ms} 毫秒 */
      elapsed: 'Duración: {ms} ms',
      upstreamBody: 'Respuesta del upstream',
    },

    // ── 删除确认 ────────────────────────────────────────────
    deleteDialog: {
      title: 'Eliminar canal',
      /** 确认文案：确认删除渠道「{name}」？删除后该渠道的调用将立即失败。 */
      message: '¿Eliminar el canal «{name}»? Las llamadas a través de este canal fallarán de inmediato.',
    },

    // ── 表单（新建 / 编辑共用）─────────────────────────────
    form: {
      createTitle: 'Nuevo canal',
      editTitle: 'Editar canal',
      name: 'Nombre del canal',
      namePlaceholder: 'Dale un nombre a este upstream',
      type: 'Tipo de canal',
      typeFallbackPlaceholder: 'ID del tipo de canal',
      baseUrl: 'URL del upstream',
      apiKey: 'Clave del upstream',
      /** 编辑态：留空即不修改 */
      apiKeyHelpKeep: 'Déjalo vacío para no modificarla',
      /** 新建态：说明同时支持密钥池 */
      apiKeyHelpNew:
        'También acepta un pool de claves: una por línea, con una nota opcional detrás de un espacio o una coma',
      keyPool: 'Pool de claves (opcional)',
      keyPoolHelp: 'Una clave por línea; pega varias claves upstream a la vez',
      strategy: 'Estrategia de programación',
      failurePolicy: 'Política de fallos',
      /** 新建时的"不选 = 用后端默认"选项 */
      followDefault: 'Seguir el valor predeterminado del sistema',
      models: 'Lista de modelos',
      modelsHelp: 'Uno por línea; si se deja vacío, se admiten todos los modelos',
      fetchModelsHint:
        'Puedes obtener la lista del upstream; los resultados se combinan con el campo de abajo',
      fetchFromUpstream: 'Obtener del upstream',
      fetchModels: 'Obtener modelos del upstream',
      /** 拉取时用哪套凭据的说明 */
      useSavedCredential: 'Usa la URL y la clave guardadas de este canal',
      useTypedCredential: 'Usa la URL y la clave escritas arriba',
      /** 拉取结果条：拉取到 {count} 个模型 */
      fetchedCount: 'Se obtuvieron {count} modelos',
      fillModels: 'Insertar en la lista de modelos',
      copyList: 'Copiar lista',
      emptyFetched: 'El upstream devolvió una lista vacía',
      enable: 'Activar el canal',
    },

    // ── 表单校验 ────────────────────────────────────────────
    validation: {
      /** 拉取模型时既没有已存凭据也没填地址 */
      fetchNeedsUrl: 'Introduce primero la URL del upstream',
      nameRequired: 'Introduce un nombre de canal',
      keyRequired: 'Un canal nuevo necesita una clave de upstream',
      urlRequired: 'Un canal nuevo necesita una URL de upstream',
    },

    // ── 操作结果提示 ────────────────────────────────────────
    toast: {
      /** 拉取成功：拉取到 {count} 个模型，确认后点「填入模型列表」 */
      fetched: 'Se obtuvieron {count} modelos; pulsa «Insertar en la lista de modelos» para aplicarlo',
      fetchFailed: 'No se pudieron obtener los modelos',
      /** 合并结果：已填入：新增 {added} 个，当前共 {total} 个 */
      applied: 'Insertados: {added} añadidos, {total} en total',
      created: 'Canal creado',
      updated: 'Canal actualizado',
      deleted: 'Canal eliminado',
      deleteFailed: 'No se pudo eliminar',
      probeFailed: 'La sonda falló',
      saveFailed: 'No se pudo guardar',
    },

    // ── 表单底部按钮 ────────────────────────────────────────
    submitCreate: 'Crear',
    submitSave: 'Guardar',
    cancel: 'Cancelar',
  },
}
