// 会话时间线的统一排序与叠放（#52 / #105）。
//
// 时间线上除了「轮次」（智能体的一次阻塞汇报 + 您的回复）之外，还有很多别的条目：您的追加、智能体的进度通知、
// 任务进展卡、确认 / 授权卡、文档决策卡、恢复胶囊。它们以前是各自独立的几条流，每条流各自算
// 「落在哪两轮之间」，同一个间隙里再一类一类地渲染，所以会错序。
// 这里把它们合并成**一条按提交时间排序的流**再落位；连续的、不需要您马上处理的卡片叠成一摞。
// 全部是纯函数，不依赖 Vue，方便单测。

import type { FeedbackSession, QueuedFeedback } from '../api/types'
import type { ConfirmItem } from '../api/confirms'
import type { BranchFeedItem } from '../api/branches'
import type { CheckpointView } from './anchor'
import type { TaskUpdateCard } from './asyncMode'
import { branchPreview, feedItemKey } from './branch'

/** 比所有轮次都新的条目落在时间线末尾。 */
export const TIMELINE_TAIL_KEY = '__tail__'

/** 一份文档里的决策卡（确认项 kind=decision）合成的一张汇总卡：对话里不灌几十张卡，只指路到文档页作答。 */
export interface DecisionGroup {
  key: string
  docId?: number
  docPath: string
  docTitle: string
  pending: number
  answered: number
  total: number
  /** 这一组里最早一张决策卡的创建时间：汇总卡落在文档的决策卡第一次出现的位置。 */
  at: string
}

export type TimelineEntry =
  | { kind: 'inbox'; key: string; at: string; q: QueuedFeedback }
  | { kind: 'notice'; key: string; at: string; session: FeedbackSession }
  | { kind: 'task'; key: string; at: string; card: TaskUpdateCard }
  | { kind: 'confirm'; key: string; at: string; item: ConfirmItem }
  | { kind: 'decisions'; key: string; at: string; group: DecisionGroup }
  | { kind: 'capsule'; key: string; at: string; capsule: CheckpointView }
  // 分支会话（#236）：主会话 ↔ 分支之间的消息（两个方向）与「分支已建立 / 已结束」事件行
  | { kind: 'branch'; key: string; at: string; item: BranchFeedItem }

export type TimelineKind = TimelineEntry['kind']

/** 同一时刻（毫秒也相同）时的稳定次序：您的话在前，智能体的话、卡片依次在后。 */
const KIND_RANK: Record<TimelineKind, number> = {
  inbox: 0,
  notice: 1,
  // 分支会话里另一个智能体的话，与进度通知同级（同一时刻时按传入次序）
  branch: 1,
  capsule: 2,
  task: 3,
  confirm: 4,
  decisions: 5,
}

/**
 * 条目的提交时间（毫秒）。解析不了的当作最早。
 */
export function entryTime(e: Pick<TimelineEntry, 'kind' | 'at'>): number {
  const t = Date.parse(e.at)
  return Number.isFinite(t) ? t : 0
}

function roundTime(iso: string): number {
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : 0
}

function shallowSame(a: object, b: object): boolean {
  if (a === b) return true
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return false
  for (const k of ka) {
    if ((a as Record<string, unknown>)[k] !== (b as Record<string, unknown>)[k]) return false
  }
  return true
}

/**
 * 两个同键条目是不是「看起来一样」：种类、键、时间都相同，携带的数据也没变——
 * 数据是 store 里的同一个对象（会话行、收件箱条目、确认项、告警、胶囊），或是浅比较相同的小对象（任务进展卡、文档决策汇总）。
 * 用「同一个对象」而不是内容比较：store 里的行被原地改写时，卡片自己读的是响应式对象，会照常更新，不靠条目这层包装换对象；
 * 重新取数得到的新对象则一定判为不同，宁可多渲染一次，也不让旧内容留在页面上。
 */
export function sameTimelineEntry(a: TimelineEntry, b: TimelineEntry): boolean {
  if (a === b) return true
  if (a.kind !== b.kind || a.key !== b.key || a.at !== b.at) return false
  switch (a.kind) {
    case 'inbox':
      return a.q === (b as typeof a).q
    case 'notice':
      return a.session === (b as typeof a).session
    case 'task':
      return shallowSame(a.card, (b as typeof a).card)
    case 'confirm':
      return a.item === (b as typeof a).item
    case 'decisions':
      return shallowSame(a.group, (b as typeof a).group)
    case 'capsule':
      return a.capsule === (b as typeof a).capsule
    case 'branch':
      return a.item === (b as typeof a).item
  }
}

/**
 * 重新组装出来的条目，凡是和上一次「看起来一样」的都换回上一次的那个对象；一个都没变时整个数组也换回上一次的那个。
 *
 * 为什么（#186 C2a）：时间线条目是每次重算时现拼的包装对象（`{ kind: 'notice', key, at, session }`），而每张卡片拿它当属性——
 * 任何一个来源有动静（收件箱、确认项、任务进展、胶囊、整表里随便哪一行被补丁）就重拼一遍，几百张通知卡的 `entry` 属性
 * 全是新对象，Vue 判定「属性变了」把几百张卡整个重渲染一遍；生产实测打开会话头 3 秒里有二十多次这样的渲染（每次 30 ～ 170 ms）。
 * 复用同一个对象之后，没变的卡片属性不变、整张跳过；一个条目都没变时数组也不变，下游的落位 / 分摞 / 滚轨计算连同整个列表的重渲染都不会发生。
 *
 * prev 是上一次的「键 → 条目」，返回新的这一份（下一次传回来）。纯函数，不改传入的数据。
 */
export function reuseEntries(
  prev: { byKey: ReadonlyMap<string, TimelineEntry>; list: ReadonlyArray<TimelineEntry> },
  next: ReadonlyArray<TimelineEntry>,
): { byKey: Map<string, TimelineEntry>; list: TimelineEntry[] } {
  const byKey = new Map<string, TimelineEntry>()
  const list: TimelineEntry[] = []
  let allSame = next.length === prev.list.length
  for (let i = 0; i < next.length; i++) {
    const e = next[i]
    const old = prev.byKey.get(e.key)
    const use = old && sameTimelineEntry(old, e) ? old : e
    list.push(use)
    byKey.set(use.key, use)
    if (allSame && use !== prev.list[i]) allSame = false
  }
  return allSame ? { byKey: prev.byKey as Map<string, TimelineEntry>, list: prev.list as TimelineEntry[] } : { byKey, list }
}

/** 按提交时间升序；同一时刻按种类次序、再按传入顺序，结果稳定、可重复。 */
export function sortTimeline(entries: ReadonlyArray<TimelineEntry>): TimelineEntry[] {
  return entries
    .map((e, i) => ({ e, i, t: entryTime(e) }))
    .sort((a, b) => {
      if (a.t !== b.t) return a.t < b.t ? -1 : 1
      return KIND_RANK[a.e.kind] - KIND_RANK[b.e.kind] || a.i - b.i
    })
    .map(x => x.e)
}

/**
 * 把条目放进对话时间线：先整体按提交时间排好，再落在「第一个比它晚创建的轮次」之前（键为那一轮的 session_id）——
 * 读起来就是「您追加了一句 / 智能体发了一张卡，然后才开始下一轮」；比所有轮次都新的落在末尾（键为 TIMELINE_TAIL_KEY）。
 *
 * rounds 必须是同一工作流、按时间升序排列的**全部**真实轮次（而不只是当前窗口里的那几轮），
 * 这样分页展开更早的轮次时，条目的位置不会漂移；键对应的轮次没在窗口里时它自然不渲染。
 * 与轮次同一时刻的条目落在它之后（不是之前）。
 */
export function placeTimeline(
  rounds: ReadonlyArray<{ session_id: string; created_at: string; reply_at?: string }>,
  entries: ReadonlyArray<TimelineEntry>,
): Record<string, TimelineEntry[]> {
  const out: Record<string, TimelineEntry[]> = {}
  if (entries.length === 0) return out
  // 性能（#186 C2a）：长会话里条目有几百条（进度通知为主）、轮次一百多轮，逐条从头 find 并且每比一轮就解析一次时间，
  // 是 O(条目 × 轮次) 次 Date.parse（生产实测 5～6 秒的打开过程里占 100～180 毫秒）。
  // 这里轮次的提交时间只解析一次；条目已按时间升序，「第一个比它晚创建的轮次」的下标随 t 增大只增不减
  // （比 t 晚的轮次集合随 t 增大而嵌套缩小，与轮次是否有序无关），所以接着上一条的落点往后找，整体线性。
  // 结果与逐条从头 find 逐一相同（timeline.test.ts 里有随机对照）。
  const createdMs = rounds.map(r => roundTime(r.created_at))
  let from = 0
  for (const e of sortTimeline(entries)) {
    const t = entryTime(e)
    let at = from
    while (at < rounds.length && !(createdMs[at] > t)) at++
    from = at
    const next = at < rounds.length ? rounds[at] : undefined
    // 一张轮次卡里有两个时刻：智能体提问（created_at）与你的回复（reply_at）。
    // 落在这两个时刻之间的条目要放进卡里、夹在提问和回复之间，而不是排到整张卡之后（#118：追加总显示在方案后面）。
    const prevAt = next ? at - 1 : rounds.length - 1
    const prev = rounds[prevAt]
    if (prev && prev.reply_at && t >= createdMs[prevAt] && t < roundTime(prev.reply_at)) {
      ;(out[inRoundKey(prev.session_id)] ||= []).push(e)
      continue
    }
    ;(out[next ? next.session_id : TIMELINE_TAIL_KEY] ||= []).push(e)
  }
  return out
}

/** 夹在某一轮「智能体提问」与「你的回复」之间的条目所用的键。 */
export function inRoundKey(sessionId: string): string {
  return `in:${sessionId}`
}

// ---------- 确认项 → 时间线条目 ----------

/** 一张确认项是不是「还要您回答」：只有这种卡永远摊开，不叠进摞里。 */
export function needsAnswer(item: Pick<ConfirmItem, 'status'>): boolean {
  return item.status === 'pending'
}

/**
 * 按文档把决策卡（kind=decision）合成汇总组；组按第一张决策卡出现的时间升序。
 * 文档页才是作答的地方（带批量采用推荐），对话里只留一张指路的汇总卡。
 */
export function groupDecisions(items: ReadonlyArray<ConfirmItem>): DecisionGroup[] {
  const map = new Map<string, DecisionGroup>()
  for (const it of items) {
    if (it.kind !== 'decision') continue
    const key = it.doc_id ? `doc-${it.doc_id}` : `doc-${it.doc_path || it.doc_key || 'unknown'}`
    let g = map.get(key)
    if (!g) {
      g = {
        key,
        docId: it.doc_id,
        docPath: it.doc_path || '',
        docTitle: it.doc_title || it.doc_path || '文档',
        pending: 0,
        answered: 0,
        total: 0,
        at: it.created_at,
      }
      map.set(key, g)
    }
    g.total++
    if (it.status === 'pending') g.pending++
    else if (it.status === 'answered') g.answered++
    if (roundTime(it.created_at) < roundTime(g.at)) g.at = it.created_at
  }
  return [...map.values()].sort((a, b) => roundTime(a.at) - roundTime(b.at))
}

/** 确认项 → 时间线条目：提问 / 计划各是一张卡；文档决策按文档合成一张汇总卡。 */
export function confirmEntries(items: ReadonlyArray<ConfirmItem>): TimelineEntry[] {
  const out: TimelineEntry[] = []
  for (const it of items) {
    if (it.kind === 'decision') continue
    out.push({ kind: 'confirm', key: `confirm-${it.seq}`, at: it.created_at, item: it })
  }
  for (const g of groupDecisions(items)) {
    out.push({ kind: 'decisions', key: `decisions-${g.key}`, at: g.at, group: g })
  }
  return out
}

// ---------- 分支会话 → 时间线条目 ----------

/**
 * 分支消息流（主会话 ↔ 分支的消息与建立 / 结束事件）→ 时间线条目。
 * 它们都是「话」或「事件行」，不叠进摞里（isStackable 对 branch 恒为 false）。
 */
export function branchEntries(items: ReadonlyArray<BranchFeedItem>): TimelineEntry[] {
  return items.map(item => ({ kind: 'branch', key: feedItemKey(item), at: item.at, item }))
}

// ---------- 叠放 ----------

/**
 * 能叠进摞里的卡片：任务进展、已处理的提问、都已处理完的文档决策汇总卡。
 * 不能叠的（永远单独摊开）：您和智能体的「话」（追加、进度通知——它们把前后的卡片隔成不同的摞）、
 * 还没答复的提问 / 授权（一键确认不能藏在折叠里）、还有待决策项的文档汇总卡、恢复胶囊。
 */
export function isStackable(e: TimelineEntry): boolean {
  switch (e.kind) {
    case 'task':
      return true
    case 'decisions':
      return e.group.pending === 0
    case 'confirm':
      return !needsAnswer(e.item)
    default:
      return false
  }
}

export type TimelineBlock =
  | { type: 'single'; key: string; entry: TimelineEntry }
  | { type: 'stack'; key: string; entries: TimelineEntry[] }

/**
 * 把一个间隙里（已按时间排好）的条目分块：连续的可叠卡片（至少两张）成一摞，其余各自单独一块。
 * 摞的键取第一张卡的键——之后在末尾追加新卡不会换键，展开状态不会丢。
 */
export function groupBlocks(entries: ReadonlyArray<TimelineEntry>): TimelineBlock[] {
  const out: TimelineBlock[] = []
  let run: TimelineEntry[] = []
  const flush = () => {
    if (run.length >= 2) out.push({ type: 'stack', key: `stack-${run[0].key}`, entries: run })
    else if (run.length === 1) out.push({ type: 'single', key: run[0].key, entry: run[0] })
    run = []
  }
  for (const e of entries) {
    if (isStackable(e)) {
      run.push(e)
      continue
    }
    flush()
    out.push({ type: 'single', key: e.key, entry: e })
  }
  flush()
  return out
}

/**
 * 收着这个确认项（编号如 Q2）的摞的键。已处理的提问是事件行，连续的卡片（至少两张）默认叠在收起的摞里——
 * 行没有挂载，也就读不到 focusRef；点名这样的提问（徽标 / 正文里的 Q2 引用）时，要先把这些摞展开。
 * blocks 是按间隙分好的块（groupBlocks 的结果，键是间隙键）。
 */
export function stackKeysHoldingConfirm(blocks: Readonly<Record<string, ReadonlyArray<TimelineBlock>>>, ref: string): string[] {
  const keys: string[] = []
  for (const list of Object.values(blocks)) {
    for (const b of list) {
      if (b.type === 'stack' && b.entries.some(e => e.kind === 'confirm' && e.item.ref === ref)) keys.push(b.key)
    }
  }
  return keys
}

/** 这一块（单条或一摞）里有没有这个确认项（编号如 Q2）。 */
export function blockHoldsConfirm(block: TimelineBlock, ref: string): boolean {
  if (block.type === 'stack') return block.entries.some(e => e.kind === 'confirm' && e.item.ref === ref)
  return block.entry.kind === 'confirm' && block.entry.item.ref === ref
}

export interface StackSummary {
  total: number
  /** 任务进展的次数与涉及的任务数。 */
  taskUpdates: number
  tasks: number
  /** 已处理的提问 / 计划数。 */
  handled: number
  /** 文档决策汇总卡数。 */
  decisions: number
  firstAt: string
  lastAt: string
}

export function summarizeStack(entries: ReadonlyArray<TimelineEntry>): StackSummary {
  const seqs = new Set<number>()
  let taskUpdates = 0
  let handled = 0
  let decisions = 0
  for (const e of entries) {
    if (e.kind === 'task') {
      taskUpdates++
      seqs.add(e.card.seq)
    } else if (e.kind === 'confirm') handled++
    else if (e.kind === 'decisions') decisions++
  }
  return {
    total: entries.length,
    taskUpdates,
    tasks: seqs.size,
    handled,
    decisions,
    firstAt: entries[0]?.at ?? '',
    lastAt: entries[entries.length - 1]?.at ?? '',
  }
}

/** 一张卡片收起时的一行预览（叠起来的摞里显示最新那一张）。 */
export function entryPreview(e: TimelineEntry): string {
  switch (e.kind) {
    case 'task':
      return `#${e.card.seq} ${e.card.title} · ${e.card.verb}${e.card.text ? `：${e.card.text}` : ''}`
    case 'confirm':
      return `${e.item.ref} ${e.item.title}`
    case 'decisions':
      return `《${e.group.docTitle}》的决策卡`
    case 'notice':
      return e.session.title || '智能体进度'
    case 'capsule':
      return `恢复胶囊 r${e.capsule.revision}`
    case 'inbox':
      return '您的追加'
    case 'branch':
      return branchPreview(e.item)
  }
}

/** 摞的一句话说明，如「任务进展 4 次（3 个任务）· 已处理的提问 1 条」。 */
export function stackSummaryText(s: StackSummary): string {
  const parts: string[] = []
  if (s.taskUpdates > 0) parts.push(s.tasks > 1 ? `任务进展 ${s.taskUpdates} 次（${s.tasks} 个任务）` : `任务进展 ${s.taskUpdates} 次`)
  if (s.handled > 0) parts.push(`已处理的提问 ${s.handled} 条`)
  if (s.decisions > 0) parts.push(`文档决策 ${s.decisions} 份`)
  return parts.join(' · ')
}
