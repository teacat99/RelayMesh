// 历史日志与实时终端之间的文字分割线（纯函数，不依赖 DOM / xterm，便于单元测试）。
// 分割线只在本地写进终端缓冲区用来分隔，不会发给主机，也不会进入主机上的记录。

/** 终端里一个字符占几列：CJK 与全角符号占 2 列，其余 1 列（够用的近似，不引入完整的 wcwidth 表）。 */
export function cellWidth(ch: string): number {
  const c = ch.codePointAt(0) ?? 0
  if (c < 0x1100) return 1
  if (
    (c >= 0x1100 && c <= 0x115f) ||
    (c >= 0x2e80 && c <= 0xa4cf) ||
    (c >= 0xac00 && c <= 0xd7a3) ||
    (c >= 0xf900 && c <= 0xfaff) ||
    (c >= 0xfe30 && c <= 0xfe6f) ||
    (c >= 0xff00 && c <= 0xff60) ||
    (c >= 0xffe0 && c <= 0xffe6) ||
    (c >= 0x20000 && c <= 0x3fffd)
  ) {
    return 2
  }
  return 1
}

export function displayWidth(s: string): number {
  let n = 0
  for (const ch of s) n += cellWidth(ch)
  return n
}

const FILL = '─'
/** 标签两侧至少各留这么多根横线，才像一条分割线。 */
const MIN_SIDE = 3

/**
 * 生成一行刚好 cols 列宽的分割线，标签居中。候选标签按顺序挑第一个放得下的；都放不下就只画横线。
 */
export function dividerLine(cols: number, labels: string[]): string {
  const width = Math.max(1, Math.floor(cols))
  for (const label of labels) {
    const w = displayWidth(label)
    if (w + MIN_SIDE * 2 > width) continue
    const rest = width - w
    const left = Math.floor(rest / 2)
    return FILL.repeat(left) + label + FILL.repeat(rest - left)
  }
  return FILL.repeat(width)
}
