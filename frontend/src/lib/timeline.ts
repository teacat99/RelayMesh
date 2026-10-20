// 会话时间线的统一排序与叠放（#52 / #105）。
//
// 时间线上除了「轮次」（智能体的一次阻塞汇报 + 您的回复）之外，还有很多别的条目：您的追加、智能体的进度通知、
// 任务进展卡、确认 / 授权卡、文档决策卡、守护告警、恢复胶囊。它们以前是各自独立的几条流，每条流各自算
// 「落在哪两轮之间」，同一个间隙里再一类一类地渲染，所以会错序。
// 这里把它们合并成**一条按提交时间排序的流**再落位；连续的、不需要您马上处理的卡片叠成一摞。
// 全部是纯函数，不依赖 Vue，方便单测。

import type { FeedbackSession, QueuedFeedback } from '../api/types'
import type { ConfirmBoardGuard, ConfirmItem } from '../api/confirms'
import type { CheckpointView } from './anchor'
import type { TaskUpdateCard } from './asyncMode'

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
  | { kind: 'guard'; key: string; at: string; alert: ConfirmBoardGuard }
  | { kind: 'capsule'; key: string; at: string; capsule: CheckpointView }

export type TimelineKind = TimelineEntry['kind']

/** 同一时刻（毫秒也相同）时的稳定次序：您的话在前，智能体的话、卡片依次在后。 */
const KIND_RANK: Record<TimelineKind, number> = {
  inbox: 0,
  notice: 1,
  capsule: 2,
  task: 3,
  confirm: 4,
  decisions: 5,
  guard: 6,
}

/**
 * 条目的提交时间（毫秒）。解析不了的：守护告警没有可靠的开始时间时排在最后（它描述的是「现在」），
 * 其余当作最早。
 */
export function entryTime(e: Pick<TimelineEntry, 'kind' | 'at'>): number {
  const t = Date.parse(e.at)
  if (Number.isFinite(t)) return t
  return e.kind === 'guard' ? Number.POSITIVE_INFINITY : 0
}

function roundTime(iso: string): number {
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : 0
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
  for (const e of sortTimeline(entries)) {
    const t = entryTime(e)
    const next = rounds.find(r => roundTime(r.created_at) > t)
    // 一张轮次卡里有两个时刻：智能体提问（created_at）与你的回复（reply_at）。
    // 落在这两个时刻之间的条目要放进卡里、夹在提问和回复之间，而不是排到整张卡之后（#118：追加总显示在方案后面）。
    const prev = next ? rounds[rounds.indexOf(next) - 1] : rounds[rounds.length - 1]
    if (prev && prev.reply_at && t >= roundTime(prev.created_at) && t < roundTime(prev.reply_at)) {
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

/** 守护告警 → 时间线条目：落在告警开始的时间；没有开始时间的排在最后（描述的是「现在」）。 */
export function guardEntries(alerts: ReadonlyArray<ConfirmBoardGuard>): TimelineEntry[] {
  return alerts.map(a => ({
    kind: 'guard' as const,
    key: `guard-${a.preset_id}`,
    at: typeof a.verdict?.since === 'string' ? a.verdict.since : '',
    alert: a,
  }))
}

// ---------- 叠放 ----------

/**
 * 能叠进摞里的卡片：任务进展、已处理的提问、都已处理完的文档决策汇总卡。
 * 不能叠的（永远单独摊开）：您和智能体的「话」（追加、进度通知——它们把前后的卡片隔成不同的摞）、
 * 还没答复的提问 / 授权（一键确认不能藏在折叠里）、还有待决策项的文档汇总卡、守护告警、恢复胶囊。
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
    case 'guard':
      return `${e.alert.name} · 守护告警`
    case 'notice':
      return e.session.title || '智能体进度'
    case 'capsule':
      return `恢复胶囊 r${e.capsule.revision}`
    case 'inbox':
      return '您的追加'
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
