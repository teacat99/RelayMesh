// 输入框里的引用标记 → 引用条目（#118）。
// 经典视图的引用是在文字里插「【回复 9f02517c】」「【恢复 r8】」「【确认 Q5】」，Hub 按这个写法解析；标记本身保持不变，
// 这里只负责把它们读出来，在输入框上方显示成现代的引用条（带被引用内容的开头、可一键移除）。
import { replyHash } from './replyRef'

export interface QuoteChip {
  /** 文字里的原样标记，移除时用它。 */
  token: string
  kind: 'reply' | 'capsule' | 'confirm'
  /** 回复的 8 位哈希、胶囊的 rN，或确认项的 Qn（统一成小写哈希 / 小写 r / 大写 Q）。 */
  ref: string
}

// 三种写法各有各的编号形状：回复 = 6~12 位十六进制，胶囊 = r + 正整数，确认项 = Q + 正整数（Q 可省，用户手敲「【确认 5】」也认）。
const TOKEN_RE = /【(?:回复\s+([0-9a-fA-F]{6,12})|恢复\s+(r\d+)|确认\s+([Qq]?\d{1,6}))】/g

/** 按出现顺序读出文字里的引用标记，同一个标记只算一次。 */
export function parseQuoteChips(text: string): QuoteChip[] {
  const seen = new Set<string>()
  const out: QuoteChip[] = []
  for (const m of (text ?? '').matchAll(TOKEN_RE)) {
    const token = m[0]
    if (seen.has(token)) continue
    if (m[1] !== undefined) {
      out.push({ token, kind: 'reply', ref: m[1].toLowerCase() })
    } else if (m[2] !== undefined) {
      out.push({ token, kind: 'capsule', ref: m[2].toLowerCase() })
    } else {
      const n = Number(m[3].replace(/^[Qq]/, ''))
      if (!(n > 0)) continue // 「【确认 Q0】」服务端不认，这里也不显示成引用
      out.push({ token, kind: 'confirm', ref: `Q${n}` })
    }
    seen.add(token)
  }
  return out
}

/** 从文字里去掉一个标记，顺手收拢它前后多出来的空格。 */
export function removeQuoteToken(text: string, token: string): string {
  const i = text.indexOf(token)
  if (i === -1) return text
  const before = text.slice(0, i)
  let after = text.slice(i + token.length)
  if (before === '' || /\s$/.test(before)) after = after.replace(/^ /, '')
  return before + after
}

function clip(s: string, max: number): string {
  const base = (s ?? '').replace(/\s+/g, ' ').trim()
  return base.length > max ? `${base.slice(0, max)}…` : base
}

/** 在会话里找哈希对应的那一轮，返回「标题或开头」；找不到返回空串。 */
export function quoteExcerptFor(
  chip: QuoteChip,
  sessions: ReadonlyArray<{ session_id: string; title?: string; summary?: string }>,
  max = 60,
): string {
  if (chip.kind !== 'reply') return ''
  const s = sessions.find(x => replyHash(x.session_id) === chip.ref)
  if (!s) return ''
  return clip(s.title || s.summary || '', max)
}

/** 在确认项里找编号对应的那一项，返回标题；找不到（不在最近处理的范围里、或编号不存在）返回空串。 */
export function confirmExcerptFor(
  chip: QuoteChip,
  items: ReadonlyArray<{ ref?: string; seq?: number; title?: string }>,
  max = 60,
): string {
  if (chip.kind !== 'confirm') return ''
  const seq = Number(chip.ref.replace(/^Q/, ''))
  const it = items.find(x => (x.ref ? x.ref.toUpperCase() === chip.ref : x.seq === seq))
  return it ? clip(it.title ?? '', max) : ''
}
