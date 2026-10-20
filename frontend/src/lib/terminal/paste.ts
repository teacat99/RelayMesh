// 终端粘贴的判断：什么时候需要「多行二次确认」，以及确认框里展示什么。
//
// 括号粘贴模式（bracketed paste）开着时，程序把整段内容当作「粘贴」处理：shell 不会逐行执行、编辑器只是插入文字，
// 不需要拦；没开时，每个换行都会被当成一次回车，多行内容会立刻被逐行执行——这才是要让用户看一眼再决定的情形。
// 内容以换行结尾的单行同理：粘贴完它会立刻执行。

/** 「多行粘贴前确认」偏好的本地存储键（只影响这个浏览器）。 */
export const PASTE_CONFIRM_KEY = 'relaymesh_terminal_paste_confirm'

export function parsePasteConfirm(raw: string | null): boolean {
  return raw !== 'false' // 默认开
}

export function hasLineBreak(text: string): boolean {
  return /[\r\n]/.test(text)
}

export interface PasteGuard {
  /** 终端当前是否开着括号粘贴模式（xterm 的 `modes.bracketedPasteMode`）。 */
  bracketed: boolean
  /** 用户是否开启了「多行粘贴前确认」。 */
  enabled: boolean
}

/** 这段内容粘贴之前要不要先让用户确认。 */
export function needsPasteConfirm(text: string, g: PasteGuard): boolean {
  return g.enabled && !g.bracketed && hasLineBreak(text)
}

export interface PasteSummary {
  /** 行数；末尾的换行不再多算一行。 */
  lines: number
  chars: number
  /** 以换行结尾：粘贴之后最后一行会立刻执行。 */
  endsWithNewline: boolean
  /** 前几行（每行截到 maxCols 个字符）。 */
  preview: string[]
  /** 预览之外还有多少行。 */
  hiddenLines: number
}

export function summarizePaste(text: string, maxLines = 8, maxCols = 100): PasteSummary {
  const endsWithNewline = /[\r\n]$/.test(text)
  const parts = text.split(/\r\n|\r|\n/)
  if (endsWithNewline) parts.pop()
  const clip = (s: string) => (s.length > maxCols ? s.slice(0, maxCols) + '…' : s)
  return {
    lines: parts.length,
    chars: text.length,
    endsWithNewline,
    preview: parts.slice(0, maxLines).map(clip),
    hiddenLines: Math.max(0, parts.length - maxLines),
  }
}

export function pasteTitle(s: PasteSummary): string {
  return s.lines > 1 ? `粘贴 ${s.lines} 行内容？` : '粘贴后会立刻执行？'
}

export function pasteDescription(s: PasteSummary): string {
  return s.lines > 1
    ? '终端没有开启括号粘贴模式：每个换行都会被当作一次回车，这些内容会被逐行立刻执行。确认无误再粘贴。'
    : '这段内容以换行结尾，粘贴之后会像按了回车一样立刻执行。确认无误再粘贴。'
}
