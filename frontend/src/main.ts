import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { useThemeStore } from './stores/theme'
import { useSettingsStore } from './stores/settings'
import { installPerfProbe } from './utils/perf'
import { installChunkFailureHandling } from './utils/chunkFailure'
import { pruneBrowserDrafts } from './lib/localDraft'
import './assets/globals.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)

// 页面开着期间热更新会换掉分块文件名：懒加载的页面 / 窗口加载失败时给出「刷新」提示，而不是点了没反应（#137）。
installChunkFailureHandling(router)

// 仅开发模式：挂载 window.__rmPerf 度量探针（D-171 / P0），生产构建整段树摇移除
if (import.meta.env.DEV) {
  installPerfProbe({ getStore: (name: string) => (pinia as any)._s.get(name) })
}

// 显式初始化主题状态，确保 DOM 类名、状态与图标绝对一致
const themeStore = useThemeStore()
themeStore.init()

// 显式从后端拉取并同步全局系统设置
const settingsStore = useSettingsStore()
settingsStore.fetchRemoteSettings()

app.mount('#app')

// 本机草稿（#144）：启动时清一次过期 / 损坏的，并把总量压在上限内；放到空闲时做，不挡首屏
setTimeout(() => {
  try {
    pruneBrowserDrafts()
  } catch {
    /* 清理失败不影响使用 */
  }
}, 2000)
