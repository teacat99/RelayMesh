import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { applyDarkClass, browserScheduler, createTransitionSuppressor } from '../lib/themeSwitch'

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

  // 切主题的那一刻临时压住全局颜色过渡（#185）：不压的话每个元素都建过渡动画，2.5 万元素的长会话要同步卡 4 秒多。
  // 首次应用（刷新页面）没有旧样式可过渡，不用压；之后明暗真的变了才压（原因与做法见 lib/themeSwitch.ts）。
  let suppress: (() => void) | null = null
  let appliedOnce = false

  function apply() {
    if (typeof document === 'undefined') return
    const root = document.documentElement
    if (!suppress && typeof window !== 'undefined') {
      suppress = createTransitionSuppressor(root.classList, browserScheduler(window))
    }
    applyDarkClass(root.classList, isDark.value, appliedOnce && suppress ? suppress : undefined)
    appliedOnce = true
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
