import { ref, type Ref } from 'vue'
import { FONT_DEFAULT, FONT_KEY, clampFont, parseFont } from '../lib/terminal/fontSize'

// 终端字号是整个浏览器共享的一份偏好：终端页（Ctrl+滚轮 / Ctrl+0）与设置里的「终端偏好」改的是同一个值，
// 所以用模块级单例；写入本地存储，并跟随其他标签页的修改。

function readStored(): number {
  try {
    return parseFont(localStorage.getItem(FONT_KEY))
  } catch {
    return FONT_DEFAULT
  }
}

const fontSize = ref(readStored())

function persist(n: number) {
  try {
    localStorage.setItem(FONT_KEY, String(n))
  } catch {
    /* 隐私模式等：只在本页生效 */
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', e => {
    if (e.key === FONT_KEY) fontSize.value = parseFont(e.newValue)
  })
}

export function setTerminalFont(n: number): number {
  const v = clampFont(n)
  if (v !== fontSize.value) {
    fontSize.value = v
    persist(v)
  }
  return v
}

export function useTerminalFont(): {
  fontSize: Readonly<Ref<number>>
  set: (n: number) => number
  /** 按档数增减（正为放大）。 */
  bump: (steps: number) => number
  reset: () => number
} {
  return {
    fontSize,
    set: setTerminalFont,
    bump: steps => setTerminalFont(fontSize.value + steps),
    reset: () => setTerminalFont(FONT_DEFAULT),
  }
}
