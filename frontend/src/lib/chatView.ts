// 统一消息流的纯逻辑（群聊窗口用）：游标、合并、日期分隔、时间与摘要文案。不依赖 Vue 与 DOM，便于单元测试。
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

export interface ChatRow {
  key: string
  type: 'date' | 'message'
  /** type=date：「今天」「昨天」「10月13日」…… */
  label?: string
  message?: ChatMessage
}

/** 把消息排成可渲染的行：跨天处插入日期分隔条（日历日的判断与日期条文字在 dateLabel.ts，预览视图的日期分隔共用）。 */
export function buildRows(messages: ReadonlyArray<ChatMessage>, now: Date = new Date()): ChatRow[] {
  const rows: ChatRow[] = []
  let prevDay: Date | null = null
  for (const m of messages) {
    const at = new Date(m.sent_at)
    if (!prevDay || !sameDay(prevDay, at)) {
      rows.push({ key: `date-${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`, type: 'date', label: dateLabel(at, now) })
    }
    rows.push({ key: m.id, type: 'message', message: m })
    prevDay = at
  }
  return rows
}

/** 搜索结果等处显示的一行摘要。 */
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
