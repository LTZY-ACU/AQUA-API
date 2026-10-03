/**
 * 法语（fr）词条聚合入口。
 *
 * 意图（Why / 流转 / 扩展）：
 *   见 locales/zh-CN/index.ts；本文件仅聚合各域词条，
 *   并把 home / auth / legal 三个公开站子域挂到 site 命名空间下。
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
    home,
    auth,
    legal: { ...site.legal, ...legal },
  },
}
