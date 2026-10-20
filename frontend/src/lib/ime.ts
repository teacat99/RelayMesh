/**
 * 输入法（IME）组合期间的按键不是用户对页面下达的指令：回车是在确认候选词，Esc 是在取消组合，
 * 方向键 / 空格 / 数字是在挑选候选。把它们当成「提交」或「关闭」，中文输入就会被打断。
 *
 * 各浏览器的表现并不一致，所以两个条件都要看：
 *   - Chrome / Firefox：组合期间的 keydown 带 isComposing = true；
 *   - Safari：确认候选词的那个回车，compositionend 先于它的 keydown 触发，此时 isComposing 已经是 false，
 *     但 keyCode 仍然是 229（「正在由输入法处理」）；
 *   - Windows 上的 Chrome 给这类按键的 key 是 'Process'，macOS 上则可能就是真正的 'Enter'。
 */
export function isImeKey(ev: Pick<KeyboardEvent, 'isComposing' | 'keyCode'>): boolean {
  return ev.isComposing === true || ev.keyCode === 229
}
