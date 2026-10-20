// 卡片摞里有哪几种类型（#160，追加 #248 / Q24 选 a；只给预览视图用，经典不碰）。
//
// 摞可以混着放：任务进展、已处理的提问、已处理的计划、都处理完的文档决策。折叠起来的摞原来只写「N 张卡片」，
// 光看摞头和右侧轨都不知道里面是什么。这里把摞里的条目按类型数出来：
//   - 摞头（事件行）给每种类型一个色标签（任务 石板 / 提问 琥珀 / 计划 红 / 决策 紫），最多画 3 个，多出的并成「+N」；
//   - 右侧轨上摞的刻度按内含类型分段着色（每种类型一段，并排）；
//   - 悬停提示写出每种类型的张数。
// 类型取卡片词汇表（lib/messageCard 的 CARD_TYPES）里的同名类型，所以颜色、标签字与卡片头部的类型标签完全一致。
// 纯函数，不依赖 Vue / DOM，组件只负责摆放。
import { CARD_TONE_CLASS, CARD_TYPES, type CardTone } from './messageCard'
import type { TimelineEntry } from './timeline'

/** 摞里可能出现的类型，按固定次序展示：只为每次顺序一致，与紧迫程度无关。 */
export const STACK_TYPE_ORDER = ['task', 'ask', 'plan', 'decision'] as const
export type StackCardType = (typeof STACK_TYPE_ORDER)[number]

export interface StackTypeCount {
  type: StackCardType
  count: number
}

export function isStackCardType(v: unknown): v is StackCardType {
  return typeof v === 'string' && (STACK_TYPE_ORDER as ReadonlyArray<string>).includes(v)
}

/** 一个条目在摞里算哪种类型；不会进摞的种类（追加、进度通知、告警、恢复胶囊）返回 null。 */
export function stackTypeOf(e: TimelineEntry): StackCardType | null {
  switch (e.kind) {
    case 'task':
      return 'task'
    case 'confirm':
      return e.item.kind === 'plan' ? 'plan' : 'ask'
    case 'decisions':
      return 'decision'
    default:
      return null
  }
}

/** 摞里每种类型各有几张，按 STACK_TYPE_ORDER 排；没有的类型不出现。 */
export function stackTypeCounts(entries: ReadonlyArray<TimelineEntry>): StackTypeCount[] {
  const n: Record<StackCardType, number> = { task: 0, ask: 0, plan: 0, decision: 0 }
  for (const e of entries) {
    const t = stackTypeOf(e)
    if (t) n[t]++
  }
  return STACK_TYPE_ORDER.filter(t => n[t] > 0).map(t => ({ type: t, count: n[t] }))
}

/** 「任务 3 · 提问 1」：悬停提示与无障碍标签里用。 */
export function stackTypesText(counts: ReadonlyArray<StackTypeCount>): string {
  return counts.map(c => `${CARD_TYPES[c.type].label} ${c.count}`).join(' · ')
}

/** 摞头最多画几个类型标签；再多就并成「+N」（悬停提示里仍写全）。 */
export const STACK_MAX_CHIPS = 3

export interface StackChip {
  type: StackCardType
  /** 标签字，与卡片头部的类型标签相同（任务 / 提问 / 计划 / 决策）。 */
  label: string
  count: number
  tone: CardTone
  /** 色调类（定义 --card-tone）：标签自己或祖先上要配它。 */
  toneClass: string
}

export interface StackChipModel {
  chips: StackChip[]
  /** 没画出来的类型个数（>0 时摞头多画一个「+N」）。 */
  more: number
  /** 悬停提示：每种类型的张数，如「任务 3 · 提问 1」。 */
  text: string
}

export function stackChipModel(counts: ReadonlyArray<StackTypeCount>, max: number = STACK_MAX_CHIPS): StackChipModel {
  const all: StackChip[] = counts.map(c => {
    const meta = CARD_TYPES[c.type]
    return { type: c.type, label: meta.label, count: c.count, tone: meta.tone, toneClass: CARD_TONE_CLASS[meta.tone] }
  })
  const shown = Math.max(0, max)
  return { chips: all.slice(0, shown), more: Math.max(0, all.length - shown), text: stackTypesText(counts) }
}

/** 右侧轨上摞刻度的一段：一种类型一段，颜色取该类型的卡片色调。 */
export interface StackRailSegment {
  type: StackCardType
  toneClass: string
}

export function stackRailSegments(parts: ReadonlyArray<StackTypeCount> | undefined): StackRailSegment[] {
  return (parts ?? []).map(p => ({ type: p.type, toneClass: CARD_TONE_CLASS[CARD_TYPES[p.type].tone] }))
}

/** parts ↔ DOM 属性：「task:3,ask:1」。 */
export function formatStackParts(parts: ReadonlyArray<StackTypeCount> | undefined): string {
  return (parts ?? []).map(p => `${p.type}:${p.count}`).join(',')
}

/** 读回 parts：不认识的类型、不是正整数的张数一律丢掉；同一种类型只认第一次，按固定次序重排；一段都没有返回 undefined。 */
export function parseStackParts(raw: string | null | undefined): StackTypeCount[] | undefined {
  if (!raw) return undefined
  const n: Partial<Record<StackCardType, number>> = {}
  for (const piece of raw.split(',')) {
    const [type, cnt] = piece.split(':')
    const count = Number(cnt)
    if (!isStackCardType(type) || n[type] !== undefined || !Number.isInteger(count) || count <= 0) continue
    n[type] = count
  }
  const out = STACK_TYPE_ORDER.filter(t => n[t] !== undefined).map(t => ({ type: t, count: n[t] as number }))
  return out.length > 0 ? out : undefined
}
