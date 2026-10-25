import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { BUILD_TARGET } from './build.target'
import { precompressAssets } from './build.precompress'

// 开发服务器把 /api、/mcp 代理到后端；默认就是本机 18775，也可以用 RELAYMESH_DEV_BACKEND 指向别的实例（例如隔离的验证实例）。
const BACKEND = process.env.RELAYMESH_DEV_BACKEND || 'http://localhost:18775'

// 产物写盘之后给 assets 下的 js / css 各补一个 Brotli 预压缩文件（.br），服务端按 Accept-Encoding 直接发（见 build.precompress.ts）。
// RM_SKIP_PRECOMPRESS=1 跳过（体积报告脚本自己算 brotli，不需要落盘；CI 里只想快速验证类型 / 打包时也可用）。
function brotliPrecompress(): Plugin {
  let outDir = ''
  return {
    name: 'relaymesh-brotli-precompress',
    apply: 'build',
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir)
    },
    writeBundle() {
      if (process.env.RM_SKIP_PRECOMPRESS === '1') return
      const done = precompressAssets(outDir)
      const kept = done.filter(e => !e.skipped)
      const raw = kept.reduce((n, e) => n + e.raw, 0)
      const br = kept.reduce((n, e) => n + e.br, 0)
      console.log(`brotli 预压缩：${kept.length} 个文件，${(raw / 1024).toFixed(1)} KB → ${(br / 1024).toFixed(1)} KB`)
    },
  }
}

export default defineConfig({
  plugins: [
    vue(),
    tailwindcss(),
    brotliPrecompress()
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src')
    }
  },
  build: {
    outDir: path.resolve(__dirname, '../web/dist'),
    // 不要改低：esbuild 会把 xterm.js 的 DECRQM 处理编坏，详见 build.target.ts。
    target: BUILD_TARGET,
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 800,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-vue': ['vue', 'vue-router', 'pinia'],
          'vendor-ui': ['radix-vue', 'lucide-vue-next'],
          'vendor-misc': ['axios', 'dayjs', 'marked']
        }
      }
    }
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': {
        target: BACKEND,
        changeOrigin: true,
        // 终端使用 WebSocket。后端只接受与 Host 同源的 Origin，开发服务器的 Origin 是 5173，这里改写成后端自己的地址。
        ws: true,
        configure: proxy => {
          proxy.on('proxyReqWs', proxyReq => {
            proxyReq.setHeader('origin', BACKEND)
          })
        }
      },
      '/mcp': { target: BACKEND, changeOrigin: true }
    }
  }
})
