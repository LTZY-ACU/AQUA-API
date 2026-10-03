/**
 * 通用词条（英语 / English）。
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
    save: 'Save',
    cancel: 'Cancel',
    confirm: 'Confirm',
    close: 'Close',
    delete: 'Delete',
    edit: 'Edit',
    create: 'Create',
    copy: 'Copy',
    copied: 'Copied',
    refresh: 'Refresh',
    retry: 'Reload',
    search: 'Search',
    reset: 'Reset',
    submit: 'Submit',
    back: 'Back',
    view: 'View',
    open: 'Open',
    more: 'More',
    enable: 'Enable',
    disable: 'Disable',
    restore: 'Restore',
    addRow: 'Add row',
    stop: 'Stop',
  },
  state: {
    loading: 'Loading…',
    empty: 'No data',
    enabled: 'Enabled',
    disabled: 'Disabled',
    removed: 'Removed',
    notSet: 'Not set',
    available: 'Available',
    unavailable: 'Unavailable',
    success: 'Success',
    failed: 'Failed',
  },
  unit: {
    items: 'items',
    days: 'days',
  },
  toast: {
    operationFailed: 'Something went wrong. Please try again later.',
    loadFailed: 'Failed to load',
    saveFailed: 'Failed to save',
  },
  language: {
    label: 'Language',
    switch: 'Switch language',
  },
  confirm: {
    defaultMessage: 'Are you sure you want to proceed?',
  },
  value: {
    neverExpires: 'Never expires',
    unlimitedQuota: 'Unlimited quota',
    other: 'Other',
  },
  money: {
    quotaUnit: 'quota',
    originalPrice: 'List price',
    discount: '{ratio}% of price',
    perCall: '/call',
  },
  api: {
    timeout: 'Request timed out. Check your network and try again.',
    network: 'Cannot reach the server. Please make sure the backend service is running.',
    http400: 'Invalid request parameters',
    http401: 'Your session has expired. Please sign in again.',
    http403: 'You do not have permission to perform this action',
    http404: 'The requested resource does not exist',
    http409: 'Conflict: the record may already exist',
    http429: 'Too many requests, or your quota is exhausted',
    http500: 'Server error. Please try again later.',
    http503: 'No upstream channel is currently available',
    httpGeneric: 'Request failed (HTTP {status})',
    exportConnectFailed: 'Cannot reach the server. Export failed.',
    exportFailed: 'Export failed (HTTP {status})',
    downloadFailed: 'Download failed (HTTP {status})',
  },
}
