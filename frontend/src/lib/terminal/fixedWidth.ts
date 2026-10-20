// 终端「宽度保持」：让终端的列数固定为一个值，不随浏览器窗口变化——窗口放不下就缩小字号去适配。
//
// 为什么需要：终端的列数是主机上伪终端的真实宽度，换行、全屏程序的排版、会话记录里的每一行都由它决定。
// 以前列数跟着当前正在看的那个浏览器窗口走，在手机上看一眼，电脑上回来看到的历史就是按另一个宽度排的。
// 开启后所有设备都把终端报告为同一个列数，字号按窗口宽度自动调整（宽窗口保持用户设置的字号，窄窗口缩小）。
//
// 这里只放纯函数：真正量「单元格有多宽」的是 xterm（按设备像素取整），调用方把「这个字号放不放得下」包成回调传进来。

/** 字号的下限（CSS px）：再小就算在高分屏上也看不清了。缩到这里还放不下时，由调用方改用横向滚动。 */
export const FIXED_FONT_MIN = 4
/** 求解字号的步进（CSS px）。xterm 的单元格宽度按设备像素取整，更细的步进只会重复探测同一个宽度。 */
export const FIXED_FONT_STEP = 0.5
/** 低于它就提醒「字号偏小」（高分屏手机上 6~7 px 仍可勉强辨认）。 */
export const FIXED_FONT_LEGIBLE = 7

/** 设置里的「常用宽度」快捷值：只是常见的终端宽度，不是推荐值（推荐值要按 Cursor / Claude / Codex 等的实际排版另行评估）。 */
export const FIXED_COLS_PRESETS = [80, 100, 120, 132, 160, 200] as const
/** 第一次打开开关时填入的列数（暂定）。 */
export const FIXED_COLS_DEFAULT = 120

export interface FixedFontResult {
  /** 求得的字号（CSS px）。 */
  font: number
  /** 在这个字号下 fits 为真；缩到下限仍放不下时为 false（此时 font 就是下限）。 */
  fits: boolean
  /** 调用 fits 的次数（每次都会让 xterm 重新量一次单元格，所以要尽量少）。 */
  probes: number
}

const round2 = (n: number) => Math.round(n * 100) / 100

/**
 * 在 [min, base] 里找出「放得下」的最大字号。
 * fits(f) 必须随字号单调：字号越小越容易放下（单元格宽度随字号不减，这一点成立）。
 * 先试 base（窗口够宽时只探测一次）；放不下再试下限；之后在两者之间二分，约 6 次探测收敛。
 */
export function solveFixedFont(
  base: number,
  fits: (font: number) => boolean,
  opts: { min?: number; step?: number } = {},
): FixedFontResult {
  const step = opts.step && opts.step > 0 ? opts.step : FIXED_FONT_STEP
  const min = Math.min(opts.min ?? FIXED_FONT_MIN, base)
  let probes = 0
  const test = (f: number) => {
    probes++
    return fits(f)
  }
  if (test(base)) return { font: base, fits: true, probes }
  if (base <= min) return { font: base, fits: false, probes }
  if (!test(min)) return { font: min, fits: false, probes }

  // font(i) = min + i * step，i ∈ [0, n]；lo 放得下，hi（开区间）放不下——base 已确认放不下，所以 n + 1 可以当哨兵。
  const n = Math.floor((base - min) / step)
  const fontAt = (i: number) => round2(min + i * step)
  let lo = 0
  let hi = n + 1
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (test(fontAt(mid))) lo = mid
    else hi = mid
  }
  return { font: fontAt(lo), fits: true, probes }
}

/** 字号文字：整数不带小数，其余保留一位。 */
export function fmtFont(font: number): string {
  return Number.isInteger(font) ? String(font) : font.toFixed(1)
}

export interface FixedWidthState {
  cols: number
  /** 实际使用的字号。 */
  font: number
  /** 用户设置的字号（上限）。 */
  base: number
  /** 缩到下限后是否仍放得下。 */
  fits: boolean
}

export interface FixedWidthHint {
  text: string
  title: string
  tone: 'info' | 'warn'
}

/** 终端角落的提示：字号被缩小、或窗口窄到放不下时才有；字号没变（窗口够宽）时不打扰。 */
export function describeFixedWidth(s: FixedWidthState): FixedWidthHint | null {
  if (!s.fits) {
    return {
      text: `固定 ${s.cols} 列 · 窗口过窄`,
      title: `终端宽度固定为 ${s.cols} 列，字号已缩到最小的 ${fmtFont(s.font)}px 仍放不下，右侧内容需要横向滑动查看。可以在 设置 → 终端偏好 里调低固定列数。`,
      tone: 'warn',
    }
  }
  if (s.font >= s.base) return null
  const small = s.font < FIXED_FONT_LEGIBLE
  return {
    text: `固定 ${s.cols} 列 · ${fmtFont(s.font)}px`,
    title:
      `终端宽度固定为 ${s.cols} 列：当前窗口放不下，字号已从 ${fmtFont(s.base)}px 缩小到 ${fmtFont(s.font)}px。` +
      (small ? '字号偏小，可以在 设置 → 终端偏好 里调低固定列数。' : ''),
    tone: small ? 'warn' : 'info',
  }
}

/** 设置里输入的固定列数：必须是落在范围内的整数。 */
export function parseFixedCols(
  raw: string,
  limits: { min: number; max: number },
): { ok: true; value: number } | { ok: false; message: string } {
  const text = raw.trim()
  if (!/^\d+$/.test(text)) return { ok: false, message: '请输入整数' }
  const value = Number(text)
  if (value < limits.min || value > limits.max) return { ok: false, message: `应在 ${limits.min}–${limits.max} 之间` }
  return { ok: true, value }
}
