/** 品牌标识：直接使用站长提供的 ICO 图标（不再是自造水波纹）。
 *
 * 意图（Why）：
 *   用户明确要求「logo 用我给你的那个 ICO 图标，别自己造」。
 *   仓库内 favicon.ico 是唯一品牌资产（web/public/favicon.ico），
 *   本组件直接以 <img> 引用它，保证顶栏/页脚/登录页与浏览器标签是同一枚图标。
 *
 * 流转（Flow）：
 *   SiteHeader / SiteFooter / AppShell / 认证页 → <BrandLogo />
 *
 * 扩展（Extend）：
 *   换图标只替换 web/public/favicon.ico 即可，全站（含浏览器标签）同步生效。
 */

interface BrandLogoProps {
  /** 品牌文字（默认 LTZY-API） */
  name?: string
  /** 图标尺寸（px），默认 22 */
  iconSize?: number
  /** 文字颜色（Tailwind 类） */
  textClass?: string
}

/** 品牌标：ICO 图标本身 */
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    // ICO 由站长提供；favicon.ico 自带透明底与品牌图形，原样引用
    <img
      src="/favicon.ico"
      alt="LTZY-API"
      width={size}
      height={size}
      className="block"
      style={{ width: size, height: size, objectFit: 'contain' }}
    />
  )
}

/** 品牌标 + 名称的组合（顶栏/页脚常用） */
export function BrandLogo({ name = 'LTZY-API', iconSize = 22, textClass = 'text-ink' }: BrandLogoProps) {
  return (
    <span className="flex items-center gap-2">
      <BrandMark size={iconSize} />
      <span className={`font-semibold ${textClass}`}>{name}</span>
    </span>
  )
}
