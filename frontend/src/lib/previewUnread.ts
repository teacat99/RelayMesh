// 预览视图的未读（#120 阶段 E-c2）：纯逻辑，不依赖 Vue 与 DOM。
//
// 口径与服务端一致（store.CountUnreadAgentMessages）：「未读」= Agent 在已读游标之后发的消息——
// 阻塞式汇报、进度通知 / 任务进展、提问 / 计划卡；您自己的话、系统行、决策汇总、守护告警、恢复胶囊都不计。
// 游标是毫秒：消息的发送毫秒 > 游标 就是未读（与服务端「created_at 严格晚于游标那一毫秒」同义）。
// 已读游标是跨设备共享的（conversation_reads，只增不减），聊天视图、侧栏与这里读写的是同一份。

import { inRoundKey, type TimelineBlock, type TimelineEntry } from './timeline'

/**
 * 计入服务端 unread 的消息分布在这几种里：汇报是 block（待答复）或 text（已答复）、进度通知与任务进展是 progress、提问 / 计划卡是 ask / plan。
 * 取「最新一条 Agent 消息」时按它过滤（您的回复 / 追加也是 text，靠发送者区分，见 newestAgentMessage）。
 * 漏掉 text 的后果：最新一条计入未读的是「已答复的汇报」时，会拿更早的通知当游标，服务端游标只增不减，未读就卡在原地。
 */
export const UNREAD_KINDS = ['text', 'block', 'progress', 'ask', 'plan'] as const

/** 向服务端取「最新一批会计入未读的消息」的条数：够越过夹在中间的、不是 Agent 发的几条。 */
export const NEWEST_PAGE = 8

type At = string | number | undefined | null

function toMs(at: At): number {
  if (typeof at === 'number') return at
  return Date.parse(String(at ?? ''))
}

/** 这条时间线条目是不是「Agent 发的、会计入未读」的消息（与服务端口径一致：只有提问 / 计划两种确认卡计入）。 */
export function isAgentEntry(e: TimelineEntry): boolean {
  switch (e.kind) {
    case 'notice':
    case 'task':
      return true
    case 'confirm':
      return e.item.kind === 'ask' || e.item.kind === 'plan'
    default:
      return false
  }
}

function blockEntries(b: TimelineBlock): ReadonlyArray<TimelineEntry> {
  return b.type === 'stack' ? b.entries : [b.entry]
}

/** 一个顶层项（一块 / 一轮）：键 + 它里面每一条「Agent 发的」消息的时间。 */
export interface UnreadItem {
  /** 与模板里 v-for 的键一致：间隙里的块用 block.key，轮次用 session_id。 */
  key: string
  agentAt: ReadonlyArray<At>
}

/**
 * 把「当前显示的顶层项（按显示顺序）」整理成 UnreadItem：每个轮次依次是「它前面的间隙块 → 轮次本身」，最后是末尾的块——
 * 与 dayMarks（日期分隔）走同一个顺序。轮次的 Agent 消息 = 汇报本身（created_at）+ 夹在提问与回复之间、画在卡里的 Agent 条目；
 * 一摞的 Agent 消息 = 摞里每一张卡。
 */
export function unreadItems(
  rounds: ReadonlyArray<{ session_id: string; created_at: string }>,
  blocks: Readonly<Record<string, ReadonlyArray<TimelineBlock>>>,
  tail: ReadonlyArray<TimelineBlock>,
): UnreadItem[] {
  const out: UnreadItem[] = []
  const pushBlocks = (list: ReadonlyArray<TimelineBlock>) => {
    for (const b of list) out.push({ key: b.key, agentAt: blockEntries(b).filter(isAgentEntry).map(e => e.at) })
  }
  for (const s of rounds) {
    pushBlocks(blocks[s.session_id] || [])
    const nested = (blocks[inRoundKey(s.session_id)] || []).flatMap(b => blockEntries(b).filter(isAgentEntry).map(e => e.at))
    out.push({ key: s.session_id, agentAt: [s.created_at, ...nested] })
  }
  pushBlocks(tail)
  return out
}

/**
 * 分隔线「以下为新消息」画在哪个顶层项前面：按显示顺序第一个含有「晚于已读游标的 Agent 消息」的项；都已读返回 null。
 * 游标之后的消息若在摞里、或在轮次的卡里，分隔线画在整摞 / 整张卡的前面（不拆开）。
 * 第一个未读项恰好是整张列表的第一项、而且上面没有被窗口藏起来的更早内容（hasEarlier = false）时也返回 null：
 * 分隔线是用来把「已读的」和「新的」隔开的，上面什么都没有就没有可隔的（新工作流的第一条汇报、从没读过的工作流都是这种情况）。
 */
export function unreadDividerKey(items: ReadonlyArray<UnreadItem>, readMs: number, hasEarlier = false): string | null {
  for (let i = 0; i < items.length; i++) {
    if (items[i].agentAt.some(a => toMs(a) > readMs)) return i === 0 && !hasEarlier ? null : items[i].key
  }
  return null
}

/** 已经渲染出来的 Agent 消息里最晚的一条（毫秒）；一条都没有为 0。 */
export function renderedAgentMs(items: ReadonlyArray<UnreadItem>): number {
  let max = 0
  for (const it of items) {
    for (const a of it.agentAt) {
      const t = toMs(a)
      if (Number.isFinite(t) && t > max) max = t
    }
  }
  return max
}

interface WireMessage {
  sender: { kind: string }
  sent_at: string
  cursor: string
}

/** 一页消息（旧 → 新）里最新的一条 Agent 消息；没有返回 null。 */
export function newestAgentMessage<T extends WireMessage>(items: ReadonlyArray<T>): T | null {
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i].sender.kind === 'agent') return items[i]
  }
  return null
}

export type ReadVerdict = 'none' | 'mark' | 'wait'

/**
 * 能不能把已读游标推到服务端最新的 Agent 消息：
 * - 服务端说没有未读 → none（不用标，只需把侧栏的数字同步成 0）；
 * - 那条消息已经渲染出来了（发送毫秒 ≤ 已渲染的最晚一条）→ mark：您确实看到了；
 * - 还没渲染出来（事件在路上 / 连接断了）→ wait：不能替您把没看过的消息标成已读。
 */
export function readVerdict(args: { unread: number; newestMs: number | null; renderedMs: number }): ReadVerdict {
  if (args.unread <= 0) return 'none'
  if (args.newestMs === null || !Number.isFinite(args.newestMs)) return 'wait'
  return args.newestMs <= args.renderedMs ? 'mark' : 'wait'
}
