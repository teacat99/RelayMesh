/**
 * 回复引用标记（#106）。
 *
 * 每条智能体回复（一轮汇报或一条进度通知）有一个 8 位的短哈希，就是会话编号 sess- 之后的部分。
 * 点回复旁的「引用」会在输入框插入「【回复 9f02517c】」，点恢复胶囊里的「引用」插入「【恢复 r8】」，
 * 点确认卡上的「引用」插入「【确认 Q5】」。
 * Hub 把用户的话交给智能体时，会把这些标记解析成「引用了哪一轮 / 哪个确认项、标题与开头」（见 internal/mcp/reply_refs.go），
 * 所以标记的写法必须与服务端的识别规则一致：回复 = 6~12 位十六进制，胶囊 = r + 正整数，确认项 = Q + 正整数。
 *
 * 纯逻辑，不依赖 Vue 与 DOM。
 */
import type { TextEdit } from '../utils/imageRefs'

const SESSION_ID_PATTERN = /^sess-([0-9a-f]{6,12})$/i

/** 会话编号里的短哈希（小写）；编号不是 sess-<十六进制> 的形状（旧数据、客户端自定义的编号）返回空串。 */
export function replyHash(sessionId: string): string {
  const m = SESSION_ID_PATTERN.exec((sessionId ?? '').trim())
  return m ? m[1].toLowerCase() : ''
}

/** 「【回复 9f02517c】」；会话编号没有可用的哈希时返回空串（界面据此不显示引用按钮）。 */
export function replyRefToken(sessionId: string): string {
  const hash = replyHash(sessionId)
  return hash ? `【回复 ${hash}】` : ''
}

/** 「【恢复 r8】」；修订号不是正整数时返回空串。 */
export function capsuleRefToken(revision: number): string {
  return Number.isInteger(revision) && revision > 0 ? `【恢复 r${revision}】` : ''
}

/**
 * 「【确认 Q5】」（#132）：引用一张确认卡（智能体的提问 / 计划确认 / 文档决策，已处理的也能引用）。
 * ref 可以是「Q5」「q5」「5」或数字 5；服务端按 1~6 位正整数识别，超出或不是正整数时返回空串（界面据此不显示引用按钮）。
 */
export function confirmRefToken(ref: string | number): string {
  const m = /^[Qq]?(\d{1,6})$/.exec(String(ref ?? '').trim())
  const n = m ? Number(m[1]) : 0
  return n > 0 ? `【确认 Q${n}】` : ''
}

export interface QuoteEdit extends TextEdit {
  /** false 表示文字里已经有这个标记，没有重复插入（光标被挪到它后面）。 */
  inserted: boolean
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max)
}

const isSpace = (ch: string) => ch !== '' && /\s/.test(ch)

/**
 * 在 [start, end) 处插入引用标记（有选区则替换选区），光标落在可以接着打字的位置：
 * - 标记前面有非空白文字时补一个空格，免得和前一个字粘在一起；
 * - 标记后面是文字末尾或非空白时补一个空格，光标落在这个空格之后，用户可以直接继续写；
 * - 文字里已经有同一个标记（用户重复点了引用）时不再插第二个，只把光标挪到它后面。
 */
export function insertQuoteRef(text: string, start: number, end: number, token: string): QuoteEdit {
  const existing = text.indexOf(token)
  if (existing !== -1) {
    let caret = existing + token.length
    if (text.charAt(caret) === ' ') caret += 1
    return { text, caret, inserted: false }
  }
  const s = clamp(Math.min(start, end), 0, text.length)
  const e = clamp(Math.max(start, end), 0, text.length)
  const before = text.slice(0, s)
  const after = text.slice(e)
  const lead = before === '' || isSpace(before.slice(-1)) ? '' : ' '
  const trail = isSpace(after.charAt(0)) ? '' : ' '
  const inserted = lead + token + trail
  return { text: before + inserted + after, caret: s + inserted.length, inserted: true }
}
