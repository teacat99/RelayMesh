import { defineStore } from 'pinia'
import { ref } from 'vue'

const MODE_SWITCH_KEY = 'relaymesh_fx_mode_switch'

function readFlag(key: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(key)
    if (v === 'true') return true
    if (v === 'false') return false
  } catch {
    // 隐私模式等读不了本地存储：用默认值
  }
  return fallback
}

/**
 * 界面效果的个人偏好（只存在这台浏览器里）。
 * 现在只有「项目切换动画」：在 RelayMesh 与 ExecMesh 之间切换时，旧画面从一角向另一角化作粒子消散，约 0.6 秒。
 * 系统要求「减少动态效果」时无论这里开不开都不会播（见 lib/modeTransition.ts）。
 */
export const useFxStore = defineStore('fx', () => {
  const modeSwitch = ref(readFlag(MODE_SWITCH_KEY, true))

  function setModeSwitch(on: boolean) {
    modeSwitch.value = on
    try {
      localStorage.setItem(MODE_SWITCH_KEY, on ? 'true' : 'false')
    } catch {
      // 存不了就只在本次页面里生效
    }
  }

  return { modeSwitch, setModeSwitch }
})
