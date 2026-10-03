/**
 * 通用词条（西班牙语 / Español）。
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
    save: 'Guardar',
    cancel: 'Cancelar',
    confirm: 'Confirmar',
    close: 'Cerrar',
    delete: 'Eliminar',
    edit: 'Editar',
    create: 'Crear',
    copy: 'Copiar',
    copied: 'Copiado',
    refresh: 'Actualizar',
    retry: 'Recargar',
    search: 'Buscar',
    reset: 'Restablecer',
    submit: 'Enviar',
    back: 'Atrás',
    view: 'Ver',
    open: 'Abrir',
    more: 'Más',
    enable: 'Activar',
    disable: 'Desactivar',
    restore: 'Restaurar',
    addRow: 'Añadir fila',
    stop: 'Detener',
  },
  state: {
    loading: 'Cargando…',
    empty: 'Sin datos',
    enabled: 'Activado',
    disabled: 'Desactivado',
    removed: 'Retirado',
    notSet: 'Sin definir',
    available: 'Disponible',
    unavailable: 'No disponible',
    success: 'Correcto',
    failed: 'Fallido',
  },
  unit: {
    items: 'elementos',
    days: 'días',
  },
  toast: {
    operationFailed: 'Se produjo un error. Inténtalo de nuevo más tarde.',
    loadFailed: 'No se pudo cargar',
    saveFailed: 'No se pudo guardar',
  },
  language: {
    label: 'Idioma',
    switch: 'Cambiar idioma',
  },
  confirm: {
    defaultMessage: '¿Seguro que quieres realizar esta acción?',
  },
  value: {
    neverExpires: 'No caduca',
    unlimitedQuota: 'Cuota ilimitada',
    other: 'Otro',
  },
  money: {
    quotaUnit: 'cuota',
    originalPrice: 'Precio original',
    discount: '{ratio}% del precio',
    perCall: '/llamada',
  },
  api: {
    timeout: 'La solicitud agotó el tiempo de espera. Revisa tu red e inténtalo de nuevo.',
    network: 'No se puede conectar al servidor. Comprueba que el servicio backend esté en ejecución.',
    http400: 'Parámetros de solicitud no válidos',
    http401: 'Tu sesión ha caducado. Inicia sesión de nuevo.',
    http403: 'No tienes permiso para realizar esta acción',
    http404: 'El recurso solicitado no existe',
    http409: 'Conflicto: el registro puede que ya exista',
    http429: 'Demasiadas solicitudes o cuota agotada',
    http500: 'Error del servidor. Inténtalo de nuevo más tarde.',
    http503: 'No hay ningún canal upstream disponible por ahora',
    httpGeneric: 'Solicitud fallida (HTTP {status})',
    exportConnectFailed: 'No se puede conectar al servidor. La exportación falló.',
    exportFailed: 'La exportación falló (HTTP {status})',
    downloadFailed: 'La descarga falló (HTTP {status})',
  },
}
