// 群聊窗口里消息卡片（#120 阶段 E-d）的纯逻辑：谁说的话用什么头像与颜色、哪些消息套卡片、「以下为新消息」画在哪。
// 不依赖 Vue 与 DOM，便于单元测试；组件（GroupMessageCard / GroupEventRow / GroupChatPane）只负责摆放。
//
// 原则：同一个成员在任何地方、任何一次打开都是同一个颜色与同一个字——靠「成员标识」稳定地派生，不靠出场顺序。
// 群里的成员不多（通常 2~6 个），单纯取哈希会撞色，所以在已知的成员表里按入群先后依次分配，撞了就顺延到下一个空位：
// 先入群的人颜色永远不变，后来的人不会抢走已有的颜色，8 个以内互不重复。
import type { ChatMessage, ChatSender } from '../api/conversations'
import type { GroupMember } from '../api/groups'
import { MENTION_ALL, type MentionCandidate } from './group'

/**
 * 成员的色相（HSL 度数）。避开红（要您拍板）、琥珀（要您回答 / @你）、绿（您自己）这三种带含义的颜色；
 * 数组按「相邻项相隔很远」排列（顺延到下一个空位时，新颜色与上一个颜色一眼能分开），按色相从小到大是 172 … 332，每档约 20°。
 */
export const MEMBER_HUES: readonly number[] = [172, 238, 312, 192, 264, 332, 214, 288]

/** FNV-1a 32 位哈希：简单、稳定、跨平台结果一致（不依赖 JS 引擎的字符串实现）。 */
function hash32(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

/** 成员表里一位成员的标识：工作流成员取工作流编号（改名不影响颜色），没有就取 ref。 */
export function memberKeyOf(m: Pick<GroupMember, 'ref' | 'workflow_id'>): string {
  return m.workflow_id || m.ref
}

/** 消息发送者的标识：与 memberKeyOf 对得上（发送者带工作流编号），都没有就退回名字。 */
export function senderKeyOf(s: Pick<ChatSender, 'id' | 'name' | 'workflow_id'>): string {
  return s.workflow_id || s.id || s.name
}

/** 只凭标识取色（成员表里找不到这个人时的退路，例如发送者已被移出、成员表还没取到）。 */
export function memberHue(key: string): number {
  return MEMBER_HUES[hash32(key) % MEMBER_HUES.length]
}

/**
 * 给群里的智能体成员分配色相：按入群时间先后（同时刻按标识排序，结果与输入顺序无关）依次取「哈希起点」，
 * 被占了就顺延到下一个空位；超过 8 个成员时不再要求不重复，退回单纯的哈希。人类成员不分配（您自己固定是绿色）。
 * 离开的成员仍留在成员表里（left 标记），所以他们的历史消息颜色不变。
 */
export function assignMemberHues(members: ReadonlyArray<Pick<GroupMember, 'kind' | 'ref' | 'workflow_id' | 'joined_at'>>): Record<string, number> {
  const agents = members
    .filter(m => m.kind === 'workflow')
    .map(m => ({ key: memberKeyOf(m), at: Date.parse(m.joined_at) || 0 }))
    .sort((a, b) => a.at - b.at || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
  const n = MEMBER_HUES.length
  const used = new Set<number>()
  const out: Record<string, number> = {}
  for (const { key } of agents) {
    if (key in out) continue
    let idx = hash32(key) % n
    if (used.size < n) while (used.has(idx)) idx = (idx + 1) % n
    used.add(idx)
    out[key] = MEMBER_HUES[idx]
  }
  return out
}

/** 头像字：名字的第一个可见字符（按码点取，中文 / emoji 都是一个字）；拉丁字母转大写；没有名字是「?」。 */
export function memberAvatar(name: string | undefined | null): string {
  const first = Array.from((name ?? '').trim())[0]
  if (!first) return '?'
  return Array.from(first.toUpperCase())[0] ?? '?'
}

/** 一条消息的发送者在卡片上怎么显示。hue 为 null 表示用类型自己的色调（您 = 绿，系统 = 石板）。 */
export interface SenderIdentity {
  kind: 'agent' | 'user' | 'system'
  name: string
  avatar: string
  hue: number | null
}

/** 发送者 → 头像字 / 名称 / 色相。hues 是 assignMemberHues 的结果；没有就用单纯的哈希色。 */
export function senderIdentity(sender: ChatSender, hues: Readonly<Record<string, number>> = {}): SenderIdentity {
  if (sender.kind === 'human') return { kind: 'user', name: '您', avatar: '您', hue: null }
  if (sender.kind === 'system') return { kind: 'system', name: sender.name?.trim() || '系统', avatar: '系', hue: null }
  const key = senderKeyOf(sender)
  const name = sender.name?.trim() || sender.workflow_id || sender.id || '成员'
  return { kind: 'agent', name, avatar: memberAvatar(name), hue: hues[key] ?? memberHue(key) }
}

/** 「@所有人」在头像位上的样子：它不是任何一位成员，用中性的石板色与一个「全」字。 */
export const ALL_MEMBERS_IDENTITY: Readonly<SenderIdentity> = { kind: 'system', name: '所有人', avatar: '全', hue: null }

/**
 * 成员表里的一位成员 → 头像字 / 名称 / 色相（阶段 E-d2：成员面板、页头头像堆、@ 选择器共用）。
 * 取色与 senderIdentity 走同一把钥匙（memberKeyOf == senderKeyOf），所以同一位成员在消息卡、面板、页头、选择器里永远是同一个颜色与同一个字。
 */
export function memberIdentity(m: Pick<GroupMember, 'kind' | 'ref' | 'workflow_id' | 'name'>, hues: Readonly<Record<string, number>> = {}): SenderIdentity {
  if (m.kind === 'human') return { kind: 'user', name: '您', avatar: '您', hue: null }
  const key = memberKeyOf(m)
  const name = m.name?.trim() || m.workflow_id || m.ref || '成员'
  return { kind: 'agent', name, avatar: memberAvatar(name), hue: hues[key] ?? memberHue(key) }
}

/**
 * 页头的头像堆：还在群里的智能体成员取前 max 位（成员表的顺序 = 入群先后），余下的合成「+N」。
 * 人类成员（您自己）与已离开的不进堆：堆是用来回答「群里有谁」的，您自己不必占位。
 */
export function avatarStack<T extends Pick<GroupMember, 'kind' | 'left'>>(members: ReadonlyArray<T>, max = 3): { shown: T[]; more: number } {
  const cap = Math.max(0, Math.floor(max))
  const agents = members.filter(m => m.kind === 'workflow' && !m.left)
  return { shown: agents.slice(0, cap), more: Math.max(0, agents.length - cap) }
}

/**
 * @ 候选 → 头像身份，键是候选的 ref：「所有人」固定中性色，其余按 ref 回成员表找到那位成员再取色。
 * 成员表里找不到的候选（成员表还没取到）不出现在结果里，调用方就不画头像、退回只有名字的样子。
 */
export function candidateIdentities(
  candidates: ReadonlyArray<Pick<MentionCandidate, 'ref' | 'name'>>,
  members: ReadonlyArray<Pick<GroupMember, 'kind' | 'ref' | 'workflow_id' | 'name'>>,
  hues: Readonly<Record<string, number>> = {},
): Record<string, SenderIdentity> {
  const out: Record<string, SenderIdentity> = {}
  for (const c of candidates) {
    if (c.ref === MENTION_ALL) {
      out[c.ref] = { ...ALL_MEMBERS_IDENTITY }
      continue
    }
    const m = members.find(x => x.kind === 'workflow' && x.ref === c.ref)
    if (m) out[c.ref] = memberIdentity(m, hues)
  }
  return out
}

/**
 * 这条消息在群聊窗口里怎么画：card = 统一消息卡（GroupMessageCard）；event = 事件行（GroupEventRow）：系统行 / 确认卡 / 决策卡 / 任务进展行。
 * 判断与 GroupEventRow 的分支一一对应（系统行、带卡片的提问 / 计划、决策汇总、任务进展）；
 * 这几种在群里很少出现，也不是「谁说了什么」，不值得再做一套卡片。
 */
export function groupRenderKind(m: ChatMessage): 'card' | 'event' {
  if (m.sender.kind === 'system') return 'event'
  if ((m.kind === 'ask' || m.kind === 'plan') && m.card) return 'event'
  if (m.kind === 'decision' && m.decision) return 'event'
  if (m.kind === 'progress' && m.task) return 'event'
  return 'card'
}

/** 群消息的序号（服务端放在 meta.seq）；没有就是 NaN，所有比较都为假。 */
export function seqOf(m: Pick<ChatMessage, 'meta'>): number {
  const v = m.meta?.seq
  return typeof v === 'number' && Number.isFinite(v) ? v : NaN
}

/**
 * 「以下为新消息」画在哪条消息前面：您的已读游标（群里是成员序号 read_seq）之后的第一条别人（智能体）的消息。
 * 不画的情况：从没读过（游标 ≤ 0，所有消息都是新的，分隔线只会是噪音）；游标之后没有别人的消息；
 * 它就是整段列表的第一条且前面没有更早的历史（前面没有「已读」的消息可分隔）。
 * 调用方应当在打开会话、取到第一页时算一次并记住这条消息的编号，不要随新消息或游标推进重算，否则分隔线会在眼前跳动。
 */
export function firstUnreadId(items: ReadonlyArray<ChatMessage>, readSeq: number, hasMoreBefore: boolean): string {
  if (!(readSeq > 0)) return ''
  const idx = items.findIndex(m => m.sender.kind === 'agent' && seqOf(m) > readSeq)
  if (idx < 0) return ''
  if (idx === 0 && !hasMoreBefore) return ''
  return items[idx].id
}
