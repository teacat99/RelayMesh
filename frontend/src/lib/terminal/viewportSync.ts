// xterm 缩小 scrollback（term.options.scrollback = N）时，会立刻丢掉缓冲区最前面多出来的行（内存随之释放），
// 却不通知它的滚动条 / 视口：它们仍按旧的缓冲区长度换算滚动位置。结果是之后往上滚（滚轮、scrollToTop、拖滚动条）
// 被卡在底部，要滚过「旧长度」那么多行才会动，直到下一次输出让缓冲区滚动一行才恢复。
// xterm 只在「尺寸变化」或「缓冲区滚动」时才重新同步视口，所以缩小 scrollback 之后要主动触发一次。
//
// 做法：先放大一行再还原。内容、光标位置都不变（放大时从回滚区拉回一行，还原时再推回去），行列数最终也不变；
// 连接层按尺寸去重，不会给会话发送任何尺寸变化（真实浏览器里验证过，见 scripts/e2e-terminal/e2e/long-context.mjs）。
// 全屏程序（备用屏）里不用做：退出备用屏时 xterm 会因为缓冲区切换自己同步。

export interface ResizableTerminal {
  readonly rows: number
  readonly cols: number
  readonly buffer: { readonly active: { readonly type: string } }
  resize(cols: number, rows: number): void
}

/** 返回是否真的触发了同步。 */
export function resyncViewport(t: ResizableTerminal): boolean {
  if (t.buffer.active.type !== 'normal') return false
  const rows = t.rows
  const cols = t.cols
  t.resize(cols, rows + 1)
  t.resize(cols, rows)
  return true
}
