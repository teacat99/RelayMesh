// 预览视图的消息搜索（#120 阶段 E-c1，Q26 选 a）：纯函数——命中怎么显示、点了落到哪张卡。
// 搜索本身走已有的服务端接口 GET /conversations/wf:<工作流>/messages?q=（子串匹配，覆盖整段历史）；
// 每条命中的消息自带 source{type,id}，据此在预览列表里找到对应的卡：不靠消息编号的前缀，也不靠文字猜。
import type { ChatMessage } from '../api/conversations'
import { clockTime } from './chatView'
import { dateLabel } from './dateLabel'
import { TIMELINE_TAIL_KEY, type TimelineBlock } from './timeline'

/** 搜索只看列表里有对应卡片的消息：系统行（撤回提示等）没有可落的位置，不搜。 */
export const SEARCH_KINDS = ['text', 'progress', 'ask', 'plan', 'block', 'decision', 'alert', 'checkpoint'] as const

/** 一次最多取多少条命中（服务端上限更高，面板里取最新的这些）。 */
export const SEARCH_LIMIT = 30

export type HitTarget =
  /** 某一轮（汇报 / 回复 / 结束语）：走「滚到某一轮」，含展开更早的轮次与高亮 */
  | { type: 'round'; sessionId: string }
  /** 一张确认项（提问 / 计划及其答复）：走「点名确认项」，含展开摞与高亮 */
  | { type: 'confirm'; ref: string; key: string }
  /** 时间线上的某个条目（进度通知 / 您的追加 / 任务进展 / 文档决策汇总）：撑开窗口、展开摞、滚到那一块并闪一下 */
  | { type: 'entry'; key: string }
  /** 找不到对应的卡（已被收走 / 列表里不单独成卡）：落到「发生那一刻之前最近的一轮」 */
  | { type: 'time'; ms: number }

export interface HitContext {
  /** 当前工作流所有轮次的会话号（不只是窗口里显示的） */
  roundIds: ReadonlySet<string>
  /** 时间线条目的键（inbox-<编号> / notice-<会话号> / task-u<编号> / confirm-<序号> / decisions-doc-<文档编号>…） */
  entryKeys: ReadonlySet<string>
  /** 确认项：数据库编号 → 序号与引用号（Q12），用来把「确认项来源的命中」认到那张卡 */
  confirms: ReadonlyMap<string, { seq: number; ref: string }>
}

/** 命中 → 落点。每一档都要「目标真实存在」才选，不存在就往下落，最后兜底按时间。 */
export function resolveHit(hit: Pick<ChatMessage, 'source' | 'sent_at'>, ctx: HitContext): HitTarget {
  const type = hit.source?.type ?? ''
  const id = String(hit.source?.id ?? '')
  switch (type) {
    case 'feedback_session':
      if (ctx.roundIds.has(id)) return { type: 'round', sessionId: id }
      if (ctx.entryKeys.has(`notice-${id}`)) return { type: 'entry', key: `notice-${id}` }
      break
    case 'queued_feedback':
      if (ctx.entryKeys.has(`inbox-${id}`)) return { type: 'entry', key: `inbox-${id}` }
      break
    case 'confirm_item': {
      if (id.startsWith('doc:')) {
        const key = `decisions-doc-${id.slice(4)}`
        if (ctx.entryKeys.has(key)) return { type: 'entry', key }
        break
      }
      const c = ctx.confirms.get(id)
      if (c && ctx.entryKeys.has(`confirm-${c.seq}`)) return { type: 'confirm', ref: c.ref, key: `confirm-${c.seq}` }
      break
    }
    case 'work_item_update':
      if (ctx.entryKeys.has(`task-u${id}`)) return { type: 'entry', key: `task-u${id}` }
      break
  }
  return { type: 'time', ms: Date.parse(hit.sent_at) }
}

/** 条目在哪个间隙的哪一块里（摞里的条目返回那一摞）；找不到返回 null。 */
export function findEntryBlock(
  blocksByGap: Readonly<Record<string, ReadonlyArray<TimelineBlock>>>,
  entryKey: string,
): { gapKey: string; block: TimelineBlock } | null {
  for (const [gapKey, blocks] of Object.entries(blocksByGap)) {
    for (const block of blocks) {
      const has = block.type === 'single' ? block.entry.key === entryKey : block.entries.some(e => e.key === entryKey)
      if (has) return { gapKey, block }
    }
  }
  return null
}

/** 间隙键 → 它前面的那一轮的会话号：轮次之间的间隙键就是后一轮的会话号，轮内的是 in:<会话号>，末尾没有。 */
export function roundIdOfGap(gapKey: string): string | null {
  if (gapKey === TIMELINE_TAIL_KEY) return null
  return gapKey.startsWith('in:') ? gapKey.slice(3) : gapKey
}

/**
 * 要看到第 idx 轮（0 起，共 total 轮），窗口（只显示最新的 current 轮）至少要撑到多大；
 * 够了就原样返回，不够就多撑 slack 轮，免得目标贴在窗口边缘又被分页挤动。
 */
export function windowToShow(total: number, idx: number, current: number, slack = 2): number {
  if (idx < 0) return current
  const needed = total - idx
  return needed > current ? Math.min(total, needed + slack) : current
}

/** 发生在 ms 这一刻、却找不到卡的命中：落到「不晚于它的最近一轮」；它比所有轮次都早就落到第一轮。 */
export function nearestRoundBefore(rounds: ReadonlyArray<{ session_id: string; created_at: string }>, ms: number): string | null {
  let best: string | null = null
  let bestAt = -Infinity
  let first: { id: string; at: number } | null = null
  for (const r of rounds) {
    const at = Date.parse(r.created_at)
    if (!Number.isFinite(at)) continue
    if (!first || at < first.at) first = { id: r.session_id, at }
    if (at <= ms && at >= bestAt) {
      best = r.session_id
      bestAt = at
    }
  }
  return best ?? first?.id ?? null
}

/** 命中是哪一类消息（给结果行的小标签）。 */
export function hitKindLabel(m: Pick<ChatMessage, 'kind' | 'sender' | 'source'>): string {
  const human = m.sender?.kind === 'human'
  switch (m.source?.type) {
    case 'feedback_session':
      return human ? '回复' : m.kind === 'progress' ? '进度' : '汇报'
    case 'queued_feedback':
      return '追加'
    case 'confirm_item':
      if (human) return '答复'
      return m.kind === 'plan' ? '计划' : m.kind === 'decision' ? '决策' : '提问'
    case 'work_item_update':
      return '任务'
    default:
      return '事件'
  }
}

/** 结果行上的时间：今天 / 昨天 / 日期 + 时分，与日期分隔条同一套说法。 */
export function hitWhen(m: Pick<ChatMessage, 'sent_at'>, now: Date): string {
  const at = new Date(m.sent_at)
  if (Number.isNaN(at.getTime())) return ''
  return `${dateLabel(at, now)} ${clockTime(m.sent_at)}`
}

export interface SnippetPart {
  text: string
  hit: boolean
}

// 搜索命中的是 Markdown 原文。先只做「不改变能匹配到的字」的轻处理（去 artifact 记号、图片 / 链接语法留文字、折叠空白），
// 在这个文本里定位关键词；显示时再把两侧的 Markdown 标记去掉，关键词那一段保持原样。
function flat(src: string): string {
  return src
    .replace(/\[\[artifact:\d{1,9}\]\]/g, '')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/\s+/g, ' ')
    .trim()
}

function tidy(s: string): string {
  return s.replace(/[`*>|]+/g, '').replace(/(^|\s)#{1,6}(?=\s)/g, '$1').replace(/\s{2,}/g, ' ')
}

/**
 * 命中的那一小段文字，关键词处标出（渲染时按 part 分段，不用 v-html）。
 * 先在正文里找，找不到再在标题里找（服务端两处都匹配）；都没有就取正文开头。
 * 子串匹配不分大小写，关键词按字面处理（不当正则）。
 */
export function hitSnippet(m: Pick<ChatMessage, 'title' | 'text'>, query: string, width = 40): SnippetPart[] {
  const q = query.trim().toLowerCase()
  const bodies = [flat(m.text ?? ''), flat(m.title ?? '')]
  for (const body of bodies) {
    if (!body || !q) continue
    const low = body.toLowerCase()
    if (low.length !== body.length) continue // 个别字符转小写会变长度，位置对不上，放弃精确标注
    const at = low.indexOf(q)
    if (at < 0) continue
    const from = Math.max(0, at - Math.floor(width / 2))
    const to = Math.min(body.length, at + q.length + width)
    const parts: SnippetPart[] = []
    const head = (from > 0 ? '…' : '') + tidy(body.slice(from, at))
    if (head) parts.push({ text: head, hit: false })
    parts.push({ text: body.slice(at, at + q.length), hit: true })
    const tail = tidy(body.slice(at + q.length, to)) + (to < body.length ? '…' : '')
    if (tail) parts.push({ text: tail, hit: false })
    return parts
  }
  const fallback = tidy(bodies.find(Boolean) ?? '')
  if (!fallback) return []
  return [{ text: fallback.length > width * 2 ? fallback.slice(0, width * 2) + '…' : fallback, hit: false }]
}
