// 读取 xterm 当前屏幕状态，给「平铺」（flatten.ts）判断清屏前屏幕上有什么、全屏程序退出前最后一屏是什么用。
// 单独成文件是为了让依赖 DOM / xterm 的部分与纯逻辑分开：flatten.ts 本身只认 ScreenProbe 接口。

import type { Terminal } from '@xterm/xterm'
import type { ScreenProbe } from './flatten'

/** 没有终端可读时的占位（RecordViewer 在终端创建之前、销毁之后可能被问到）。 */
export const EMPTY_PROBE: ScreenProbe = { rows: 24, cols: 80, lines: [], cursorX: 0, cursorY: 0, alt: false, used: 0, fingerprint: '' }

export function probeScreen(t: Terminal): ScreenProbe {
  const b = t.buffer.active
  const texts: string[] = []
  for (let y = 0; y < t.rows; y++) texts.push(b.getLine(b.baseY + y)?.translateToString(true) ?? '')
  let used = 0
  for (let y = texts.length - 1; y >= 0; y--) {
    if (texts[y].trim() !== '') {
      used = y + 1
      break
    }
  }
  return {
    rows: t.rows,
    cols: t.cols,
    lines: texts,
    cursorX: b.cursorX,
    cursorY: b.cursorY,
    alt: b.type === 'alternate',
    used,
    fingerprint: texts.slice(0, used).join('\n'),
  }
}
