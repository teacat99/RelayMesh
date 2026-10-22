// 手机软键盘辅助条用到的按键序列与「粘滞修饰键」处理。纯函数，便于单元测试。

export type CursorKey = 'up' | 'down' | 'left' | 'right' | 'home' | 'end'

export const ESC = '\x1b'
export const TAB = '\t'
export const PAGE_UP = '\x1b[5~'
export const PAGE_DOWN = '\x1b[6~'

const CURSOR_LETTER: Record<CursorKey, string> = { up: 'A', down: 'B', right: 'C', left: 'D', home: 'H', end: 'F' }

/** 光标键序列：应用光标模式（DECCKM，vim / less / tmux 常开）下用 SS3，否则用 CSI。 */
export function cursorKey(key: CursorKey, applicationMode: boolean): string {
  return (applicationMode ? `${ESC}O` : `${ESC}[`) + CURSOR_LETTER[key]
}

export interface Modifiers {
  ctrl: boolean
  alt: boolean
}

/**
 * 把「粘滞修饰键 + 下一次输入」翻译成终端字节：
 *   Ctrl+字母 → 0x01–0x1a，Ctrl+@ / [ \ ] ^ _ / ? 取对应控制码；Alt 则在前面加 ESC。
 * 只对单个字符生效；输入法组合、粘贴等多字符输入原样通过（applied 为 false，粘滞状态不消耗）。
 */
export function applyModifiers(data: string, m: Modifiers): { data: string; applied: boolean } {
  if ((!m.ctrl && !m.alt) || data.length !== 1) return { data, applied: false }
  let out = data
  if (m.ctrl) out = ctrlChar(data)
  if (m.alt) out = ESC + out
  return { data: out, applied: true }
}

function ctrlChar(c: string): string {
  const code = c.charCodeAt(0)
  if (code >= 0x61 && code <= 0x7a) return String.fromCharCode(code - 0x60) // a-z
  if (code >= 0x41 && code <= 0x5a) return String.fromCharCode(code - 0x40) // A-Z
  switch (c) {
    case '@':
    case ' ':
      return '\x00'
    case '[':
      return '\x1b'
    case '\\':
      return '\x1c'
    case ']':
      return '\x1d'
    case '^':
      return '\x1e'
    case '_':
      return '\x1f'
    case '?':
      return '\x7f'
    default:
      return c
  }
}
