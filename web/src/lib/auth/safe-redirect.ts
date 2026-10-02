/** 登录/后台跳转的 redirect 参数白名单（只放行站内地址）
 *
 * 意图（Why）：
 *   redirect 取自查询串，任何人都能拼成 `https://evil.example` 或 `//evil.example`，
 *   直接喂给 router.replace 就会把刚登录的用户送到站外（登录后钓鱼的经典手法），
 *   管理员登录尤其危险——跳转目标是攻击者指定的页面，紧接着的伪造后台更易得手。
 *   校验放在读取侧（而非写入侧）：写入侧只是 pathname，改不了别人构造的 URL。
 *
 * 判据（为什么是这几条）：
 *   - 必须以单个 `/` 开头：一次挡掉绝对 URL、协议相对地址（//host）与空值；
 *   - 不得含反斜杠：反斜杠开头的串会被部分浏览器规范化成 //host；
 *   - 不得含控制字符：换行、制表等字符出现在地址里会绕过浏览器的地址归一化。
 *   以上任一不满足就回落到默认地址——跳错页远好于跳出站。
 *
 * 扩展（Extend）：
 *   新增任何"跳转到用户指定地址"的地方（退出登录回跳、SSO 回调等）
 *   都必须走本函数，不要各自再写一遍 startsWith 判断。
 */
export function safeRedirect(raw: string | null | undefined, fallback: string): string {
  if (!raw) return fallback
  if (!raw.startsWith('/') || raw.startsWith('//')) return fallback
  if (raw.includes('\\')) return fallback
  // 控制字符（C0 与 DEL）出现即拒绝；用 charCodeAt 判断而非正则字面量，
  // 免得在源码里写入不可见字符
  for (let i = 0; i < raw.length; i++) {
    const code = raw.charCodeAt(i)
    if (code < 0x20 || code === 0x7f) return fallback
  }
  return raw
}
