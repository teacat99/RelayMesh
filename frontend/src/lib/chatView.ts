// 统一消息流的纯逻辑（群聊窗口用）：游标、合并、分组、日期分隔、状态文案、引用解析。不依赖 Vue 与 DOM，便于单元测试。
// 排序与服务端（internal/chat）保持一致：（发送毫秒，来源序，主键）；游标 = base64url("ms.rank.id")。

import type { ChatMessage } from '../api/conversations'
import { dateLabel, sameDay } from './dateLabel'

export interface CursorKey {
  ms: number
  rank: number
  id: string
}

function b64urlDecode(s: string): string {
  const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4))
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + pad
  const bin = atob(b64)
  const bytes = Uint8Array.from(bin, c => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

/** 解析服务端生成的游标；认不出返回 null。 */
export function parseCursor(cursor: string | undefined | null): CursorKey | null {
  if (!cursor) return null
  let raw: string
  try {
    raw = b64urlDecode(cursor)
  } catch {
    return null
  }
  const first = raw.indexOf('.')
  const second = first < 0 ? -1 : raw.indexOf('.', first + 1)
  if (first < 0 || second < 0) return null
  const ms = Number(raw.slice(0, first))
  const rank = Number(raw.slice(first + 1, second))
  const id = raw.slice(second + 1)
  if (!Number.isFinite(ms) || !Number.isInteger(rank) || !id) return null
  return { ms, rank, id }
}

/** 键的先后：负数表示 a 在前。 */
export function compareKeys(a: CursorKey, b: CursorKey): number {
  if (a.ms !== b.ms) return a.ms < b.ms ? -1 : 1
  if (a.rank !== b.rank) return a.rank < b.rank ? -1 : 1
  if (a.id === b.id) return 0
  return a.id < b.id ? -1 : 1
}

function keyOf(m: ChatMessage): CursorKey {
  const k = parseCursor(m.cursor)
  if (k) return k
  const ms = Date.parse(m.sent_at)
  return { ms: Number.isFinite(ms) ? ms : 0, rank: 0, id: m.id }
}

/** a 是否排在 b 之前（按游标键）。 */
export function precedes(a: ChatMessage, b: ChatMessage): boolean {
  return compareKeys(keyOf(a), keyOf(b)) < 0
}

/** 把新取到的消息并进现有列表：按消息编号去重（新的覆盖旧的，状态会变），再按服务端的次序排好。 */
export function mergeMessages(existing: ReadonlyArray<ChatMessage>, incoming: ReadonlyArray<ChatMessage>): ChatMessage[] {
  const byId = new Map<string, ChatMessage>()
  for (const m of existing) byId.set(m.id, m)
  for (const m of incoming) byId.set(m.id, m)
  return [...byId.values()].sort((a, b) => compareKeys(keyOf(a), keyOf(b)))
}

/**
 * 用「最新一页」刷新列表尾部：比这一页最早一条更早的保留（已经翻出来的历史），这一页覆盖的区间以服务端为准——
 * 被删除的消息从区间里消失，状态变了的被更新，中间漏掉的（一次来了超过一页）也自然补上。
 */
export function mergeTail(existing: ReadonlyArray<ChatMessage>, page: ReadonlyArray<ChatMessage>): ChatMessage[] {
  if (page.length === 0) return [...existing]
  const first = keyOf(page[0])
  const kept = existing.filter(m => compareKeys(keyOf(m), first) < 0)
  return mergeMessages(kept, page)
}

/** 同一个人连续发的消息，间隔不超过这个时长就合并成一组（不重复显示头像与名字）。 */
export const GROUP_GAP_MS = 3 * 60 * 1000

export interface ChatRow {
  key: string
  type: 'date' | 'message'
  /** type=date：「今天」「昨天」「10月13日」…… */
  label?: string
  message?: ChatMessage
  /** 一组里的第一条：显示发送者；其余紧贴上一条。 */
  first?: boolean
  /** 一组里的最后一条：显示时间。 */
  last?: boolean
}

// 日历日的判断与日期条文字已抽到 dateLabel.ts（预览视图的日期分隔共用），这里原样转出，群聊窗口与单测的导入不变。
export { dateLabel, sameDay }

/** 卡片、系统行等「自成一块」的消息不参与分组。 */
function standalone(m: ChatMessage): boolean {
  return m.sender.kind === 'system' || m.kind === 'ask' || m.kind === 'plan' || m.kind === 'decision' || m.kind === 'alert' || m.kind === 'checkpoint'
}

function senderKey(m: ChatMessage): string {
  return `${m.sender.kind}:${m.sender.id ?? m.sender.name}`
}

/** 把消息排成可渲染的行：插入日期分隔条，并标出每一组的首尾。 */
export function buildRows(messages: ReadonlyArray<ChatMessage>, now: Date = new Date()): ChatRow[] {
  const rows: ChatRow[] = []
  let prev: ChatMessage | null = null
  let prevDay: Date | null = null
  for (const m of messages) {
    const at = new Date(m.sent_at)
    if (!prevDay || !sameDay(prevDay, at)) {
      rows.push({ key: `date-${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`, type: 'date', label: dateLabel(at, now) })
      prev = null
    }
    const continues =
      prev !== null &&
      !standalone(prev) &&
      !standalone(m) &&
      senderKey(prev) === senderKey(m) &&
      at.getTime() - new Date(prev.sent_at).getTime() <= GROUP_GAP_MS
    rows.push({ key: m.id, type: 'message', message: m, first: !continues, last: true })
    if (continues) {
      for (let i = rows.length - 2; i >= 0; i--) {
        const r = rows[i]
        if (r.type === 'message') {
          r.last = false
          break
        }
      }
    }
    prev = m
    prevDay = at
  }
  return rows
}

/** 状态的人话。Agent 发的消息只显示「待你回复」这一种需要动作的状态，其余不吵。 */
export function statusLabel(m: ChatMessage): string {
  const mine = m.sender.kind === 'human'
  switch (m.status) {
    case 'pending':
      return mine ? '' : '等你回复'
    case 'sent':
      return mine ? '已发送' : ''
    case 'delivered':
      return '已送达'
    case 'read':
      return mine ? '已读' : ''
    case 'handled':
      return '已处理'
    case 'retracted':
      return '已撤回'
    case 'expired':
      return '已过期'
    case 'answered':
      return mine ? '已答复' : '已回复'
    default:
      return ''
  }
}

export interface QuoteParts {
  hash: string
  rest: string
}

const QUOTE_RE = /^【回复 ([0-9a-fA-F]{6,12})】\s*/

/** 消息正文开头的「【回复 哈希】」标记：拆出来单独显示成引用条，正文只留后面的话。 */
export function splitQuote(text: string | undefined): QuoteParts | null {
  const m = QUOTE_RE.exec(text ?? '')
  if (!m) return null
  return { hash: m[1].toLowerCase(), rest: (text ?? '').slice(m[0].length) }
}

/** 在已加载的消息里找被引用的那条 Agent 消息（按短哈希）。 */
export function findQuoted(messages: ReadonlyArray<ChatMessage>, hash: string): ChatMessage | undefined {
  return messages.find(m => m.hash === hash && m.sender.kind === 'agent')
}

/** 引用条里显示的一行摘要。 */
export function quoteExcerpt(m: ChatMessage | undefined, max = 60): string {
  if (!m) return ''
  const base = (m.title || m.text || '').replace(/\s+/g, ' ').trim()
  return base.length > max ? `${base.slice(0, max)}…` : base
}

/** 时间的显示：当天只显示时分，其余带上日期（日期分隔条已经给了日期，这里只要时分）。 */
export function clockTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 列表里最新一条消息的游标（已读用）；没有消息返回空串。 */
export function newestCursor(messages: ReadonlyArray<ChatMessage>): string {
  return messages.length ? messages[messages.length - 1].cursor : ''
}

/** 连续的进度气泡折叠成一摞：只有「进度」类（kind=progress）且发送者相同才算连续。 */
export function isProgress(m: ChatMessage): boolean {
  return m.kind === 'progress'
}
