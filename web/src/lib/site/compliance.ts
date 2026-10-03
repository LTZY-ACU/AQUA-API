/** 全站合规提示的本地确认键（合规提示体系的最小数据源）。
 *
 * 意图（Why）：
 *   站长的策略是「功能可以接入订阅账号类上游，但必须把正规化使用、仅供学习参考的
 *   提示铺到全站」。确认键集中定义，组件统一引用，避免各页各写一份导致改一处漏一片。
 *
 * 流转（Flow）：
 *   ComplianceGate（控制台首次确认）→ 读取 / 写入 localStorage[COMPLIANCE_ACK_KEY]
 *
 * 扩展（Extend）：
 *   需要对全体用户重新告知时，改这里的键名即可让所有人重新确认一次。
 *   提示文案本身已迁入 i18n（components.compliance.*），勿在本文件再写中文文案——
 *   否则它会脱离语言切换（模块加载时求值一次，之后不再变化）。
 */

/** 首次进入控制台确认「使用须知」后写入 localStorage 的键 */
export const COMPLIANCE_ACK_KEY = 'ltzy.compliance_ack'
