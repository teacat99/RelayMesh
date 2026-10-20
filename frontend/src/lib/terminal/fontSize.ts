// 终端字号：范围、持久化格式，以及「Ctrl + 滚轮」缩放的步进规则。纯函数，便于单元测试。
//
// 缩放沿用浏览器的习惯：按住 Ctrl 向上滚（或触控板双指张开，浏览器会把它合成为带 Ctrl 的滚轮事件）放大，
// 向下滚缩小，Ctrl+0 恢复默认。

export const FONT_KEY = 'relaymesh_term_font'
export const FONT_MIN = 10
export const FONT_MAX = 24
export const FONT_DEFAULT = 14

/** 把任意数值收敛成合法字号（整数，落在 [FONT_MIN, FONT_MAX]）；不是有限数字时回到默认值。 */
export function clampFont(n: number): number {
  if (!Number.isFinite(n)) return FONT_DEFAULT
  return Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(n)))
}

/** 解析本地存储里保存的字号；缺失、不是数字或超出范围都回到默认值（而不是被硬夹到边界，避免脏数据变成极端字号）。 */
export function parseFont(raw: string | null | undefined): number {
  if (raw == null || raw.trim() === '') return FONT_DEFAULT
  const n = Number(raw)
  return Number.isFinite(n) && n >= FONT_MIN && n <= FONT_MAX ? Math.round(n) : FONT_DEFAULT
}

/** 累计多少像素的滚动算一档。普通鼠标滚轮一格约 100，所以一格一档；触控板的细碎滚动会一直累计到够一档才变化。 */
export const WHEEL_STEP_PX = 100

/** 把一次滚轮事件的纵向增量折算成像素（deltaMode：0 像素、1 行、2 页；Firefox 的鼠标滚轮按行上报）。 */
export function wheelDeltaPx(deltaY: number, deltaMode: number): number {
  if (deltaMode === 1) return deltaY * 40
  if (deltaMode === 2) return deltaY * 800
  return deltaY
}

/**
 * 累计滚动量并换算成字号档数：向上滚（deltaY < 0）放大，向下滚缩小。
 * acc 是上一次剩下的累计量；返回本次应变化的档数（正为放大）与新的剩余量。
 */
export function accumulateWheel(acc: number, deltaY: number, deltaMode: number): { steps: number; rest: number } {
  const total = acc - wheelDeltaPx(deltaY, deltaMode)
  const steps = Math.trunc(total / WHEEL_STEP_PX) || 0 // 「|| 0」把 -0 归一成 0
  return { steps, rest: total - steps * WHEEL_STEP_PX }
}

/** 缩放时在终端角落短暂显示的提示。 */
export function zoomHint(font: number): string {
  return `字号 ${font}${font === FONT_DEFAULT ? '（默认）' : ''} · Ctrl+0 恢复默认`
}

/** 是不是「Ctrl/⌘ + 0」（含小键盘的 0）。 */
export function isZoomResetKey(ev: Pick<KeyboardEvent, 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey' | 'key' | 'code'>): boolean {
  if (!(ev.ctrlKey || ev.metaKey) || ev.altKey || ev.shiftKey) return false
  return ev.key === '0' || ev.code === 'Digit0' || ev.code === 'Numpad0'
}
