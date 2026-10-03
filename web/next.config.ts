/**
 * Next.js 16 配置：静态导出（SSG），产物由 go:embed 打进 Go 二进制。
 *
 * 意图（Why）：
 *   生产部署纪律是「单二进制 + go:embed web/dist」——前端不能引入独立 Node 服务。
 *   因此用 output:'export' 生成纯静态产物（无 SSR/API Routes），
 *   再经 scripts/sync-dist.mjs 把 out/ 同步到 web/dist（go:embed 的固定路径）。
 *
 * 流转（Flow）：
 *   npm run build → next build（静态导出到 out/）→ sync-dist.mjs → web/dist → go build
 *
 * 扩展（Extend）：
 *   需要调整导出目录：改 scripts/sync-dist.mjs 的 TARGET，同时保持 web/dist 不变（后端契约）。
 */
import type { NextConfig } from 'next'

const nextConfig: NextConfig = {
  // 纯静态导出：本项目所有数据都来自 Go API，无需 SSR/ISR
  output: 'export',
  // 生成 /console/tokens/index.html 式目录，便于 go:embed 直接伺服真实页面文件
  trailingSlash: true,
  // 静态导出下 next/image 不经过优化服务：一律输出原图，避免打包期报错
  images: { unoptimized: true },
  // 开发期把 /api、/v1、/v1beta 代理到本机 Go 后端（与旧 Vite 行为一致）。
  // /v1（OpenAI/Anthropic 网关）与 /v1beta（Gemini 网关）是浏览器直连路径，
  // 不代理的话 Next dev 会当成页面路由返回 404；生产静态导出不走 rewrites，无影响。
  async rewrites() {
    const target = process.env.AQUA_BACKEND || 'http://127.0.0.1:8787'
    return [
      { source: '/api/:path*', destination: `${target}/api/:path*` },
      { source: '/v1/:path*', destination: `${target}/v1/:path*` },
      { source: '/v1beta/:path*', destination: `${target}/v1beta/:path*` },
    ]
  },
}

export default nextConfig
