// 群聊的纯函数：@ 提及的解析与补全、已读文案、成员状态文案、侧栏摘要。不依赖 Vue，便于单测。

import type { ChatReads } from '../api/conversations'
import type { GroupMember, GroupSummary } from '../api/groups'
import { subText } from './agentState'

/** @ 候选：ref 是工作流编号或 all（人自己不出现在候选里）。 */
export interface MentionCandidate {
  ref: string
  name: string
}

export const MENTION_ALL = 'all'
export const MENTION_ALL_NAME = '所有人'

/** 群里还在的 Agent 成员 + 「所有人」，作为 @ 候选。 */
export function mentionCandidates(members: ReadonlyArray<GroupMember>): MentionCandidate[] {
  const out: MentionCandidate[] = []
  for (const m of members) {
    if (m.kind !== 'workflow' || m.left) continue
    out.push({ ref: m.ref, name: m.name || m.ref })
  }
  if (out.length > 0) out.push({ ref: MENTION_ALL, name: MENTION_ALL_NAME })
  return out
}

/** 一个 @ 之后可以紧跟的字符：空白、结尾或常见标点，避免 `@A` 误命中 `@Ab`。 */
function boundaryAfter(text: string, end: number): boolean {
  if (end >= text.length) return true
  return /[\s,，.。;；:：!！?？、)）\]】]/.test(text[end])
}

/** 从文本里解析被 @ 的成员：按名字长度从长到短匹配；`@all` 与 `@所有人` 都是所有人。去重，保持出现顺序。 */
export function extractMentions(text: string, candidates: ReadonlyArray<MentionCandidate>): string[] {
  const names: Array<{ token: string; ref: string }> = []
  for (const c of candidates) names.push({ token: c.name, ref: c.ref })
  if (candidates.some(c => c.ref === MENTION_ALL)) names.push({ token: 'all', ref: MENTION_ALL })
  names.sort((a, b) => b.token.length - a.token.length)

  const found: Array<{ at: number; ref: string }> = []
  for (let i = 0; i < text.length; i++) {
    if (text[i] !== '@') continue
    // 前一个字符必须是开头或空白/标点（邮箱里的 @ 不算）
    if (i > 0 && !/[\s,，.。;；:：!！?？、(（\[【]/.test(text[i - 1])) continue
    for (const n of names) {
      if (n.token && text.startsWith(n.token, i + 1) && boundaryAfter(text, i + 1 + n.token.length)) {
        found.push({ at: i, ref: n.ref })
        i += n.token.length
        break
      }
    }
  }
  const seen = new Set<string>()
  const refs: string[] = []
  for (const f of found) {
    if (!seen.has(f.ref)) {
      seen.add(f.ref)
      refs.push(f.ref)
    }
  }
  return refs
}

/** 光标前正在输入的 @ 查询：`@` 后面没有空白的那一段；不在输入 @ 时返回 null。 */
export function mentionQuery(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret)
  const at = before.lastIndexOf('@')
  if (at < 0) return null
  if (at > 0 && !/[\s,，.。;；:：!！?？、(（\[【]/.test(before[at - 1])) return null
  const query = before.slice(at + 1)
  if (/\s/.test(query)) return null
  return { start: at, query }
}

/** 按输入过滤候选：名字或编号包含查询（不区分大小写）；空查询返回全部。 */
export function filterCandidates(candidates: ReadonlyArray<MentionCandidate>, query: string): MentionCandidate[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...candidates]
  return candidates.filter(c => c.name.toLowerCase().includes(q) || c.ref.toLowerCase().includes(q))
}

/** 把光标前的 `@查询` 换成 `@名字 `，返回新文本与新光标位置。 */
export function insertMention(text: string, caret: number, start: number, name: string): { text: string; caret: number } {
  const insert = `@${name} `
  const next = text.slice(0, start) + insert + text.slice(caret)
  return { text: next, caret: start + insert.length }
}

/** 你发出的群消息的已读文案：全部已读 / 已读 N/M / 空（没有其他成员）。 */
export function readsLabel(reads: ChatReads | undefined): string {
  if (!reads || reads.total <= 0) return ''
  if (reads.read >= reads.total) return '全部已读'
  return `已读 ${reads.read}/${reads.total}`
}

/** 已读详情（悬停提示）：谁读了、谁还没读。 */
export function readsTitle(reads: ChatReads | undefined): string {
  if (!reads || reads.total <= 0) return ''
  const lines: string[] = []
  if (reads.readers?.length) lines.push(`已读：${reads.readers.join('、')}`)
  if (reads.unread?.length) lines.push(`未读：${reads.unread.join('、')}`)
  return lines.join('\n')
}

const STATE_TEXT: Record<string, string> = {
  working: '工作中',
  idle: '空闲',
  waiting_approval: '等授权',
  aborted: '已中断',
  error: '出错',
  ended: '已结束',
  offline: '离线',
}

/** 成员状态的一行文字：工作中 · 思考；等你回答时提示「在等你」。 */
export function memberStatusText(m: Pick<GroupMember, 'kind' | 'state' | 'sub' | 'waiting_you'>): string {
  if (m.kind !== 'workflow') return ''
  if (m.waiting_you) return '在等你回答'
  const base = STATE_TEXT[m.state ?? ''] ?? ''
  if (!base) return ''
  const detail = m.state === 'working' ? subText(m.sub) : ''
  return detail ? `${base} · ${detail}` : base
}

/** 状态点的颜色：工作中绿、等你/等授权琥珀、出错红、其余灰。 */
export function memberTone(m: Pick<GroupMember, 'state' | 'waiting_you'>): 'active' | 'attention' | 'danger' | 'muted' {
  if (m.waiting_you || m.state === 'waiting_approval') return 'attention'
  if (m.state === 'working') return 'active'
  if (m.state === 'error') return 'danger'
  return 'muted'
}

/** 推送策略的人话。 */
export function policyLabel(policy: string): string {
  switch (policy) {
    case 'all':
      return '收到群里所有消息'
    case 'none':
      return '不推送（需要时自己读）'
    default:
      return '只在被 @ 时推送'
  }
}

/** 侧栏里所有群的未读合计与被点名合计。 */
export function groupTotals(list: ReadonlyArray<Pick<GroupSummary, 'unread' | 'mentioned' | 'archived'>>): { unread: number; mentioned: number } {
  let unread = 0
  let mentioned = 0
  for (const g of list) {
    if (g.archived) continue
    unread += g.unread
    mentioned += g.mentioned
  }
  return { unread, mentioned }
}

/** 群摘要的一行预览：「发送者：内容」；没有消息时提示。 */
export function groupPreviewLine(g: Pick<GroupSummary, 'preview' | 'last'>): string {
  const body = (g.preview ?? '').trim()
  if (!body) return '还没有消息'
  const who = g.last?.sender
  if (!who) return body
  const name = who.kind === 'human' ? '你' : who.name
  return `${name}：${body}`
}
