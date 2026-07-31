import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

/**
 * fontsource 的 @font-face 同时声明 woff2 与 woff。
 * 技术栈只承诺支持最近两个大版本的浏览器，它们全部支持 woff2，
 * 保留 woff 回退会让内嵌进二进制的产物多出约 4MB 永远不会被下载的文件。
 */
function dropWoffFallback(): Plugin {
  return {
    name: 'opendelo:drop-woff-fallback',
    // 在 generateBundle 阶段处理：fontsource 的 CSS 是经 @import 由 postcss 内联的，
    // transform 钩子看不到它们，只有产物阶段才拿得到最终的 @font-face。
    generateBundle(_options, bundle) {
      for (const [fileName, output] of Object.entries(bundle)) {
        if (output.type === 'asset' && fileName.endsWith('.css')) {
          output.source = String(output.source).replace(/,\s*url\([^)]+\.woff\)\s*format\("woff"\)/g, '')
        }
      }
      for (const fileName of Object.keys(bundle)) {
        if (fileName.endsWith('.woff')) {
          Reflect.deleteProperty(bundle, fileName)
        }
      }
    },
  }
}

export default defineConfig({
  plugins: [react(), dropWoffFallback()],
  build: {
    // 直接产出到 go:embed 的目录，二进制里就不会是上一次构建的界面。
    // 占位文件在上一层（embedded/.gitkeep），不会被这里的清空动作删掉。
    outDir: 'embedded/dist',
    // 关掉 modulePreload polyfill，避免 Vite 注入内联 <script>：
    // Gateway 下发的 CSP 是 script-src 'self'，内联脚本会被拦截。
    modulePreload: { polyfill: false },
    // 资源一律输出为独立文件，不内联成 data: URI，同样是为了 CSP 可收紧。
    assetsInlineLimit: 0,
  },
  server: {
    proxy: {
      '/v1': {
        target: 'http://127.0.0.1:8787',
        changeOrigin: false,
      },
    },
  },
})
