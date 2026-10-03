/**
 * 通用词条（简体中文）。
 *
 * 意图（Why）：
 *   「动作 / 状态 / 单位 / 提示」这类跨页面复用的短词集中在此，
 *   避免「保存」「取消」在多个组件里各写一份中文，改文案时漏改。
 *
 * 流转（Flow）：
 *   i18n/index.ts 合并六种语言词条 → 组件通过 $t('common.action.save') 读取。
 *
 * 扩展（Extend）：
 *   新增词条时六种语言的同一路径都要补齐（index.ts 用类型约束强制键集合一致）；
 *   键名规则：common.<语义组>.<语义>。
 */
export default {
  action: {
    save: '保存',
    cancel: '取消',
    confirm: '确认',
    close: '关闭',
    delete: '删除',
    edit: '编辑',
    create: '创建',
    copy: '复制',
    copied: '已复制',
    refresh: '刷新',
    retry: '重新加载',
    search: '搜索',
    reset: '重置',
    submit: '提交',
    back: '返回',
    view: '查看',
    open: '打开',
    more: '更多',
    enable: '启用',
    disable: '禁用',
    restore: '恢复',
    addRow: '新增一行',
    stop: '停止',
  },
  state: {
    loading: '加载中…',
    empty: '暂无数据',
    enabled: '启用',
    disabled: '禁用',
    removed: '已摘除',
    notSet: '未录入',
    available: '可用',
    unavailable: '不可用',
    success: '成功',
    failed: '失败',
  },
  unit: {
    items: '条',
    days: '天',
  },
  toast: {
    operationFailed: '操作失败，请稍后重试',
    loadFailed: '加载失败',
    saveFailed: '保存失败',
  },
  language: {
    label: '语言',
    switch: '切换语言',
  },
  confirm: {
    defaultMessage: '确认执行该操作？',
  },
  value: {
    neverExpires: '永不过期',
    unlimitedQuota: '不限额度',
    other: '其他',
  },
  money: {
    quotaUnit: '额度',
    originalPrice: '原价',
    discount: '{zhe}折',
    perCall: '/次',
  },
  api: {
    timeout: '请求超时，请检查网络后重试',
    network: '无法连接服务器，请确认后端服务是否已启动',
    http400: '请求参数有误',
    http401: '登录已失效，请重新登录',
    http403: '没有权限执行该操作',
    http404: '请求的资源不存在',
    http409: '操作冲突，该记录可能已存在',
    http429: '请求过于频繁或额度已耗尽',
    http500: '服务端出错了，请稍后重试',
    http503: '暂时没有可用的上游渠道',
    httpGeneric: '请求失败（HTTP {status}）',
    exportConnectFailed: '无法连接服务器，导出失败',
    exportFailed: '导出失败（HTTP {status}）',
    downloadFailed: '下载失败（HTTP {status}）',
  },
}
