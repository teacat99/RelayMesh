import { ref, type Ref } from 'vue'
import { PASTE_CONFIRM_KEY, parsePasteConfirm } from '../lib/terminal/paste'

// 「多行粘贴前确认」是整个浏览器共享的一份偏好：确认框里的「以后不再询问」与设置里的「终端偏好」改的是同一个值，
// 所以用模块级单例；写入本地存储，并跟随其他标签页的修改。

function readStored(): boolean {
  try {
    return parsePasteConfirm(localStorage.getItem(PASTE_CONFIRM_KEY))
  } catch {
    return true
  }
}

const enabled = ref(readStored())

function persist(v: boolean) {
  try {
    localStorage.setItem(PASTE_CONFIRM_KEY, String(v))
  } catch {
    /* 隐私模式等：只在本页生效 */
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', e => {
    if (e.key === PASTE_CONFIRM_KEY) enabled.value = parsePasteConfirm(e.newValue)
  })
}

export function setPasteConfirm(v: boolean): boolean {
  if (v !== enabled.value) {
    enabled.value = v
    persist(v)
  }
  return v
}

export function useTerminalPasteConfirm(): {
  enabled: Readonly<Ref<boolean>>
  set: (v: boolean) => boolean
} {
  return { enabled, set: setPasteConfirm }
}
