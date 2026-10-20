import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'
import { BUILD_TARGET } from './build.target'

// 开发服务器把 /api、/mcp 代理到后端；默认就是本机 18775，也可以用 RELAYMESH_DEV_BACKEND 指向别的实例（例如隔离的验证实例）。
const BACKEND = process.env.RELAYMESH_DEV_BACKEND || 'http://localhost:18775'

export default defineConfig({
  plugins: [
    vue(),
    tailwindcss()
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
