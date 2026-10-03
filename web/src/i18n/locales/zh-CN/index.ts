/**
 * 简体中文（zh-CN）词条聚合入口。
 *
 * 意图（Why）：
 *   按功能域拆分为多个文件（common / components / portal / admin / site 及其子域），
 *   便于多人并行编辑不同域；本文件只做聚合，并负责把公开站的
 *   home / auth / legal 三个子域挂到 site 命名空间下。
 *
 * 流转（Flow）：
 *   ./{common,components,portal,admin,site,home,auth,legal}.ts → 本文件默认导出
 *   → i18n/index.ts 的 messages → 组件 t('site.home.*' / 'site.auth.*' / 'site.legal.*')
 *
 * 扩展（Extend）：
 *   新增语言目录时复制本文件结构并同步补齐各域词条；
 *   新增 site 子域时在下面 site 对象里追加一行挂载，六语言同步。
 */
import admin from './admin'
import auth from './auth'
import common from './common'
import components from './components'
import home from './home'
import legal from './legal'
import portal from './portal'
import site from './site'

export default {
  common,
  components,
  portal,
  admin,
  site: {
    ...site,
    // 公开站子域：home/auth/legal 顶层键即网站路径（如 site.home.hero.*）。
    // legal 需与 site.ts 里的 legal.docBreadcrumb 合并，供跨页面包屑复用。
    home,
    auth,
    legal: { ...site.legal, ...legal },
  },
}
