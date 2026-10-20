// 长上下文终端的资源预算：
//   · 全新打开时只向主机要「末尾约 20 屏」，不再整体回放 1 MiB 的缓冲（tailLinesFor）；
//   · 接续历史后回滚缓冲区的内存有预算，列数越多可保留的行数越少（extScrollback / maxInlineLoaded）。
// 实测依据（headless Chrome，软件渲染）：重新打开一个 12 万行的会话，回放从 1025 KB / 35 帧降到 56 KB / 5 帧，
// 缓冲区 5044 → 921 行；接续历史时 6 万行 × 138 列的回滚缓冲区会让进程内存多出 115~270 MB，按预算只留约 3 万行。

const MiB = 1024 * 1024

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n))
const whole = (n: number, fallback: number) => (Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback)

/** xterm 每个单元格占 12 字节（3 个 Uint32：字符 / 前景 / 背景），一行的内存 = 列数 × 12。 */
export const CELL_BYTES = 12

/** 接续历史后，终端回滚缓冲区的内存预算。 */
export const EXT_BUDGET_BYTES = 48 * MiB
export const EXT_MIN_LINES = 10_000
export const EXT_MAX_LINES = 60_000

/**
 * 接续历史后的回滚行数：内存预算 ÷ 每行内存，夹在 [1 万, 6 万]。
 * 窄终端装得下更多行，宽终端少一些，缓冲区占用的内存大致恒定（实测 190 列 × 6 万行约 140 MB，是卡顿的主因）。
 */
export function extScrollback(cols: number): number {
  const c = Math.max(20, whole(cols, 80))
  return clamp(Math.floor(EXT_BUDGET_BYTES / (c * CELL_BYTES)), EXT_MIN_LINES, EXT_MAX_LINES)
}

/** 预估每行的原始字节数（带颜色的输出比纯文本长）。 */
export const EST_LINE_BYTES = 160
export const INLINE_MIN_BYTES = 3 * MiB
export const INLINE_MAX_BYTES = 12 * MiB

/** 历史窗口最多载入的原始字节：回滚行数 × 预估每行字节，夹在 [3, 12] MiB；再多的读进来也会被缓冲区丢掉。 */
export function maxInlineLoaded(scrollbackLines: number): number {
  return clamp(whole(scrollbackLines, EXT_MAX_LINES) * EST_LINE_BYTES, INLINE_MIN_BYTES, INLINE_MAX_BYTES)
}

/** 全新附着时请求主机回放的屏数：覆盖当前屏幕和足够的近期回滚，更早的按需读记录。 */
export const TAIL_SCREENS = 20
export const TAIL_MIN_LINES = 500
export const TAIL_MAX_LINES = 2500

/** 全新附着时请求主机只回放末尾多少行（主机只在缓冲很大且有记录可读时才照办，其余照旧整体回放）。 */
export function tailLinesFor(rows: number): number {
  return clamp(whole(rows, 24) * TAIL_SCREENS, TAIL_MIN_LINES, TAIL_MAX_LINES)
}
