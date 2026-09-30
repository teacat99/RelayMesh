import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { useThemeStore } from './stores/theme'
import { useSettingsStore } from './stores/settings'
import { installPerfProbe } from './utils/perf'
import './assets/globals.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)

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
