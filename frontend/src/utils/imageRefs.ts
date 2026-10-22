/**
 * 输入框里的图片引用标记（D-201 / 反馈 #6）。
 *
 * 粘贴 / 选择图片时，在文字里同步插入「【图 N】」，N 是这张图在缩略图条里的序号（从 1 开始）。
 * 标记的写法与 MCP 回执列出附件时的「【图 N】(编号 #N)」完全一致，AI 读到正文里的标记就能对上下面列出的图片，
 * 用户不必再手敲「第二张截图里……」。
 *
 * 纯逻辑，不依赖 Vue 与 DOM。
 *
 * 不变量：
 * - INV-R1 标记里的序号始终等于图片当前的位置：删除一张图，指向它的标记一并删掉，后面的标记整体前移；
 *   拖拽调序，标记跟着图片走，而不是停在原来的数字上指向另一张图；
 * - INV-R2 只改标记本身，不动标记以外的任何文字；
 * - INV-R3 序号超出当前图片数的标记（用户手写的、或来自已被丢弃的内容）不会被误当成有效引用去改动语义，
 *   只按同一规则平移，不会凭空消失。
 */

const REF_PATTERN = /【图\s*(\d+)\s*】/g

export function imageRefToken(n: number): string {
  return `【图 ${n}】`
}

export interface TextEdit {
  text: string
  /** 编辑后光标应落的位置。 */
  caret: number
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max)
}

/** 在 [start, end) 处插入标记（有选区则替换选区），光标落在标记之后。 */
export function insertImageRef(text: string, start: number, end: number, n: number): TextEdit {
  const s = clamp(Math.min(start, end), 0, text.length)
  const e = clamp(Math.max(start, end), 0, text.length)
  const token = imageRefToken(n)
  return { text: text.slice(0, s) + token + text.slice(e), caret: s + token.length }
}

/** 正文里出现的全部标记序号（按出现顺序，含重复）。 */
export function listImageRefs(text: string): number[] {
  const out: number[] = []
  for (const m of text.matchAll(REF_PATTERN)) out.push(Number(m[1]))
  return out
}

/**
 * 按映射重写正文里的标记：mapper 返回新序号，或返回 null 表示删除该标记（它指向的图片已被移除）。
 * 删除标记时，若它两侧各有一个空格，顺带吞掉一个，避免留下双空格；返回值与输入相同表示没有任何改动。
 */
export function remapImageRefs(text: string, mapper: (n: number) => number | null): string {
  let out = ''
  let cursor = 0
  for (const m of text.matchAll(REF_PATTERN)) {
    const index = m.index ?? 0
    const next = mapper(Number(m[1]))
    out += text.slice(cursor, index)
    cursor = index + m[0].length
    if (next === null) {
      const before = out.slice(-1)
      const after = text.slice(cursor, cursor + 1)
      if ((before === ' ' || before === '\t') && (after === ' ' || after === '\t')) cursor += 1
      continue
    }
    out += next === Number(m[1]) ? m[0] : imageRefToken(next)
  }
  return out + text.slice(cursor)
}

/** 删除第 removedIndex 张图（0 起）时的序号映射：指向它的标记删除，之后的整体前移一位。 */
export function removalMapper(removedIndex: number): (n: number) => number | null {
  const removedNumber = removedIndex + 1
  return n => (n === removedNumber ? null : n > removedNumber ? n - 1 : n)
}

/** 把第 from 张图（0 起）拖到第 to 张的位置（先取出再插入，与 splice 语义一致）时的序号映射。 */
export function moveMapper(from: number, to: number): (n: number) => number | null {
  return n => {
    const position = n - 1
    if (position === from) return to + 1
    if (from < to && position > from && position <= to) return n - 1
    if (from > to && position >= to && position < from) return n + 1
    return n
  }
}
