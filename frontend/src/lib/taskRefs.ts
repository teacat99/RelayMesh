// 正文里的任务引用规则：「#36」这样的文字在什么情况下算任务编号。
// 切片、DOM 装饰与点击动线统一在 lib/linkRefs.ts / lib/linkRefsDom.ts / composables/useLinkRefs.ts（与确认项 Q<N>、
// 文档 [[路径]]、会话号 sess-xxxxxxxx 共用一套）；点开任务编号就是任务详情弹窗。
//
// 判定刻意保守——误链接（点开一个不相干的任务）比漏链接更糟：
//   - 只认「不带前导 0 的 1–6 位整数」前面的 #；
//   - 前面紧挨着词字符 / 路径 / 实体 / 另一个 #（foo#12、/#12、&#39;、##12）都不算；中文紧贴数字（「任务#36」）算；
//   - 「追加 #5」「追加指令 #5」是收件箱里追加指令的编号，不是任务；
//   - 调用方传入判断函数，只有这个工作流里确实存在的编号才会变成链接，「第 #2 轮」「PR #123」之类保持原样。

const REF_RE = /#([1-9]\d{0,5})(?![\w#])/g
/** 紧挨在 # 前面就说明这不是任务编号的字符。 */
const BAD_BEFORE_RE = /[A-Za-z0-9_/&#]$/
/** 追加指令的编号：「追加 #5」「追加指令 #5」。 */
const APPEND_BEFORE_RE = /追加(?:指令|消息|信息)?\s*$/

/** 任务引用链接的历史 class 名（样式与老 e2e 选择器都认它）。 */
export const TASK_REF_CLASS = 'task-ref'

/** 文字里一处任务引用的位置。 */
export interface TaskRefHit {
  at: number
  length: number
  seq: number
}

/** 扫描文字里所有「判定通过且编号确实存在」的 #N，按出现顺序。 */
export function scanTaskRefs(text: string, has: (seq: number) => boolean): TaskRefHit[] {
  if (!text || !text.includes('#')) return []
  const out: TaskRefHit[] = []
  for (const m of text.matchAll(REF_RE)) {
    const at = m.index ?? 0
    const before = text.slice(Math.max(0, at - 12), at)
    if (BAD_BEFORE_RE.test(before) || APPEND_BEFORE_RE.test(before)) continue
    const seq = Number(m[1])
    if (!has(seq)) continue
    out.push({ at, length: m[0].length, seq })
  }
  return out
}
