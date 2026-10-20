// 输入框里被「按钮」填入内容之后的光标处理。
//
// 现象：点快捷标签 / 预设按钮（或语音转写）往输入框末尾追加文字时，焦点还在按钮上，输入框既没有光标也不会滚动。
// 新增的内容如果正好落在可视区域下沿之外，用户根本看不出已经添加。
// 处理：把光标放到全文末尾，并把输入框滚到最底，让刚填入的内容一定可见；桌面端顺手把焦点交给输入框，接着打字就接在新内容后面。

/** 这里只用到输入框的一小部分接口，方便在没有 DOM 的环境里用假对象测试 */
export interface CaretTarget {
  readonly value: string
  readonly scrollHeight: number
  scrollTop: number
  focus(options?: { preventScroll?: boolean }): void
  setSelectionRange(start: number, end: number): void
}

export interface MoveCaretOptions {
  /** 是否把焦点交给输入框。触屏设备上聚焦会弹出软键盘，调用方应传 false */
  focus: boolean
  /** 输入框此刻是否已经拿着焦点（已有焦点就不必重复聚焦） */
  focused: boolean
}

/** 光标移到全文末尾，并把输入框滚到最底部 */
export function moveCaretToEnd(el: CaretTarget, options: MoveCaretOptions): void {
  // preventScroll：聚焦本身不要把整页滚走，滚动只发生在输入框内部
  if (options.focus && !options.focused) el.focus({ preventScroll: true })
  const end = el.value.length
  el.setSelectionRange(end, end)
  el.scrollTop = el.scrollHeight
}

/** 当前主输入方式是否为触屏（粗糙指针）：这类设备上不要主动聚焦输入框 */
export function isTouchPrimaryInput(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches
}
