import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'auto'

const STORAGE_KEY = 'relaymesh.theme'

function readPersisted(): ThemeMode {
  if (typeof localStorage === 'undefined') return 'auto'
  const v = localStorage.getItem(STORAGE_KEY)
  if (v === 'light' || v === 'dark' || v === 'auto') return v
  const old = localStorage.getItem('portpass.theme')
  if (old === 'light' || old === 'dark' || old === 'auto') return old
  return 'auto'
}

function systemPrefersDark(): boolean {
  return typeof window !== 'undefined'
    && window.matchMedia
    && window.matchMedia('(prefers-color-scheme: dark)').matches
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readPersisted())
  const systemDark = ref<boolean>(systemPrefersDark())

  // 实际生效的明暗状态 (保证 auto 状态下实时精确计算)
  const isDark = computed<boolean>(() => {
    if (mode.value === 'dark') return true
    if (mode.value === 'light') return false
    return systemDark.value
  })

  function apply() {
    if (typeof document === 'undefined') return
    const root = document.documentElement
    if (isDark.value) {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
    // 同步更新浏览器顶部地址栏/状态栏 theme-color
    document.querySelectorAll('meta[name="theme-color"]').forEach(meta => {
      meta.setAttribute('content', isDark.value ? '#0f1216' : '#f6f8fb')
    })
  }

  function setMode(next: ThemeMode) {
    mode.value = next
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, next)
    }
    apply()
  }

  // 3 态循环切换：auto -> light -> dark -> auto
  function toggle() {
    const order: ThemeMode[] = ['auto', 'light', 'dark']
    const idx = order.indexOf(mode.value)
    setMode(order[(idx + 1) % 3])
  }

  function init() {
    if (typeof window !== 'undefined' && window.matchMedia) {
      const mql = window.matchMedia('(prefers-color-scheme: dark)')
      const handler = (e: MediaQueryListEvent) => {
        systemDark.value = e.matches
        if (mode.value === 'auto') {
          apply()
        }
      }
      try {
        mql.addEventListener('change', handler)
      } catch {
        mql.addListener(handler)
      }
      systemDark.value = mql.matches
    }
    apply()
  }

  // 初始化立即应用
  init()

  watch(isDark, () => apply())

  return { mode, isDark, setMode, toggle, toggleTheme: toggle, init }
})
