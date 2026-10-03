/** 全站合规提示的文案与本地确认键（合规提示体系的数据源）。
 *
 * 意图（Why）：
 *   站长的策略是「功能可以接入订阅账号类上游，但必须把正规化使用、仅供学习参考的
 *   提示铺到全站」。文案若各页各写一份，改一处必然漏一片；这里集中定义默认文案与
 *   首次确认的存储键，组件与页面统一引用，保证口径一致、可一处调整。
 *
 * 流转（Flow）：
 *   ComplianceNotice（页面内展示） / ComplianceGate（控制台首次确认）
 *   → 读取本文件的文案常量与 COMPLIANCE_ACK_KEY
 *
 * 扩展（Extend）：
 *   调整对外口径只改本文件常量；新增提示位置直接复用组件，勿再各写一份文案。
 */

/** 首次进入控制台确认「使用须知」后写入 localStorage 的键 */
export const COMPLIANCE_ACK_KEY = 'aqua.compliance_ack'

/** 行内 / 横幅提示的默认正文 */
export const DEFAULT_COMPLIANCE_MESSAGE =
  '本站服务仅供学习、研究与技术验证参考，请遵守各上游服务商的服务条款与当地法律法规，勿用于商业转售、批量抓取或绕过上游限制等用途。'

/** 卡片形态提示的默认标题 */
export const DEFAULT_COMPLIANCE_TITLE = '合规提示'

/** 首次确认弹窗的标题 */
export const COMPLIANCE_GATE_TITLE = '使用须知'

/** 首次确认弹窗的正文（进入控制台前的一次性告知，与用户协议口径保持一致） */
export const COMPLIANCE_GATE_MESSAGE =
  '本站服务仅供学习、研究与技术验证参考，不构成任何商业承诺。请你确认：使用本站时遵守各上游服务商的服务条款与所在地法律法规，不用于商业转售、批量抓取或绕过上游限制等违规用途；因违规使用产生的一切后果由使用者自行承担。'
