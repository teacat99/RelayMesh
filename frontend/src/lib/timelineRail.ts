// 预览视图「右侧轨」的纯逻辑（#120 阶段 2；#157 / 追加 #223 改版）。
//
// 右侧轨是会话列表右边的一条「缩略轴」。追加 #223 / #225 的要求：**点得准**（每道刻度的点击区至少 RAIL_PITCH_PX 高、互不重叠）、
// **只显示非用户消息的类型**（您自己的回复与追加不上轨）、**轨自己能用滚轮 / 触摸上下滚动**（再长也能往上找），
// 其余功能不变（悬停提示、点击跳转、可视窗口跟着会话位置走）。所以：
//   1. 只画非用户消息的类型（RAIL_STYLE 里查不到的格子就是不画：您的确认回复、您的追加；它们仍带着 data-rail-*，将来要画只改表）；
//   2. 刻度的纵向位置尽量按条目在滚动内容里的真实位置等比缩放，但相邻两道至少隔一个 pitch、不再合并：挤在一起时用保序回归
//      （PAVA，最小化与等比位置的平方偏差）把它们推开，推开的一团围着各自的等比位置居中，而不是整体往下漂；
//   3. 一屏放不下就让轨自己滚动：此时排布退化成等间距（和经典一样），轨的内容高度比可用高度大，轨跟着可视窗口走；
//   4. 当前可视范围用一块半透明窗口标出——位置用「内容位置 → 刻度位置」的分段线性映射算，推开之后窗口仍然对得上刻度。
//
// 这里只放不依赖 Vue / DOM 的部分：类型→样式表、时间线条目→轨上条目、排布、可视窗口、轨的跟随滚动、跳转目标。
// DOM 测量（ResizeObserver + rAF 批处理）在 components/feedback/preview-view/PreviewTimelineRail.vue。
//
// 数据流：预览列表给每个条目的 DOM 根节点挂 data-rail-*（railDomAttrs），轨组件扫一遍 [data-rail-kind]
// 读出位置与属性（parseRailItem），交给 layoutRail 算出刻度。「还没处理」的状态直接来自数据
// （ConfirmItem.status === 'pending'、FeedbackSession.status === 'pending'），不靠颜色或文字猜。

import type { FeedbackSession } from '../api/types'
import { firstLinePreview } from './eventRow'
import { KIND_LABEL, confirmRef } from './confirms'
import { needsAnswer, type TimelineBlock, type TimelineEntry } from './timeline'
import { STATUS_WORD } from './statusWords'
import type { CardTone } from './messageCard'
import { formatStackParts, parseStackParts, stackTypeCounts, stackTypesText, type StackTypeCount } from './stackTypes'

/** 轨上条目的种类。 */
export const RAIL_KINDS = ['plan', 'ask', 'decisions', 'round', 'reply', 'append', 'task', 'stack', 'notice', 'capsule', 'guard'] as const
export type RailKind = (typeof RAIL_KINDS)[number]

/** pending = 还要您处理（等回复 / 待答复 / 待决策 / 正在告警）；done = 其余。 */
export type RailState = 'pending' | 'done'

/**
 * 刻度的色调 = 这一类卡片的色调（lib/messageCard 的 CardTone）。追加 #265：所有刻度都按卡片类型着色——
 * 汇报 黑灰（zinc）/ 进度 蓝（sky）/ 提问 黄（amber）/ 计划 红 / 文档决策 紫 / 任务与恢复胶囊 灰蓝（slate）/ 守护告警 黄；
 * 具体颜色由刻度元素上的 CARD_TONE_CLASS[tone] 定义的 --card-tone 变量给出（亮 / 暗各一套）。
 */
export type RailTone = CardTone

export interface RailStyle {
  /** 刻度线长度（px）。 */
  len: number
  /** 刻度线粗细（px）：还要您处理的 4px，其余 2px。点击区高 16px，线的粗细取偶数才能上下居中、不被抗锯齿糊掉。 */
  line: number
  /** 色调：按卡片类型，不按状态（状态由长度 / 粗细 / 脉冲 / 变淡表达）。 */
  tone: RailTone
  /** 要不要脉冲：只有「还要您处理、且最紧要」的两种（待决策的计划、等回复的轮次）。 */
  pulse: boolean
  /** 变淡：已处理的（同一色调、更淡），让还要您处理的与默认的一眼分得开。 */
  dim: boolean
}

const S = (len: number, tone: RailTone, line: number, opts: { pulse?: boolean; dim?: boolean } = {}): RailStyle => ({
  len,
  tone,
  line,
  pulse: opts.pulse ?? false,
  dim: opts.dim ?? false,
})

/** 已处理的刻度变淡到这个不透明度（0~1）：还能认出是什么类型，又不抢要您处理的那几道的风头。 */
export const RAIL_DIM_OPACITY = 0.55

/**
 * 刻度线长度只有两档（追加 #248 / Q24，#160）：**需要您交互的画长的，默认画短的**。
 * 长 = 还要您处理的（待处理的计划 / 提问 / 文档决策、等回复的汇报、守护告警）；短 = 其余（已处理的、任务、摞、通知、胶囊）。
 * 长度只表达「要不要您动手」，类型靠颜色与悬停提示——摞的刻度另按内含类型分段着色（RailMeta.parts）。
 */
export const RAIL_LEN_LONG = 24
export const RAIL_LEN_SHORT = 14

/**
 * 轨上画什么、画成什么样：种类 → 状态 → 样式；**查不到的格子就是不画**。
 * 规则（追加 #225 澄清）：**只画非用户消息的类型**——您自己的话（确认回复、追加）不上轨，其余都上。
 * 颜色（追加 #265）：**一律按卡片类型**，不按状态：
 *   计划确认 红 / 智能体提问 黄 / 文档决策 紫 / AI 方案汇报轮次 黑灰 / 守护告警 黄 / 进度通知 蓝 / 任务进展与恢复胶囊 灰蓝；
 *   折叠的卡片摞按内含类型分段着色（RailMeta.parts），没有 parts 时（旧数据）退回灰蓝。
 * 状态用别的方式表达：还要您处理的 长 24px · 粗 4px（计划与等回复的汇报再加脉冲）；已处理的 短 14px · 细 2px · 同色变淡；
 * 本来就不分「待处理 / 已处理」的种类（任务、摞、通知、胶囊）两个状态用同一格，不变淡；守护告警描述的是「现在」，两个状态长度相同。
 */
export const RAIL_STYLE: { readonly [K in RailKind]?: { readonly [S in RailState]?: RailStyle } } = {
  plan: { pending: S(RAIL_LEN_LONG, 'red', 4, { pulse: true }), done: S(RAIL_LEN_SHORT, 'red', 2, { dim: true }) },
  round: { pending: S(RAIL_LEN_LONG, 'zinc', 4, { pulse: true }), done: S(RAIL_LEN_SHORT, 'zinc', 2, { dim: true }) },
  ask: { pending: S(RAIL_LEN_LONG, 'amber', 4), done: S(RAIL_LEN_SHORT, 'amber', 2, { dim: true }) },
  decisions: { pending: S(RAIL_LEN_LONG, 'violet', 4), done: S(RAIL_LEN_SHORT, 'violet', 2, { dim: true }) },
  guard: { pending: S(RAIL_LEN_LONG, 'amber', 4), done: S(RAIL_LEN_LONG, 'amber', 2) },
  task: { pending: S(RAIL_LEN_SHORT, 'slate', 2), done: S(RAIL_LEN_SHORT, 'slate', 2) },
  stack: { pending: S(RAIL_LEN_SHORT, 'slate', 2), done: S(RAIL_LEN_SHORT, 'slate', 2) },
  notice: { pending: S(RAIL_LEN_SHORT, 'sky', 2), done: S(RAIL_LEN_SHORT, 'sky', 2) },
  capsule: { pending: S(RAIL_LEN_SHORT, 'slate', 2), done: S(RAIL_LEN_SHORT, 'slate', 2) },
  // reply（您的确认回复）、append（您的追加）：您自己的话，不上轨
}

/** 这一格要画就返回样式，不画返回 undefined。 */
export function railStyle(kind: RailKind, state: RailState): RailStyle | undefined {
  return RAIL_STYLE[kind]?.[state]
}

export function isRailKind(v: unknown): v is RailKind {
  return typeof v === 'string' && (RAIL_KINDS as ReadonlyArray<string>).includes(v)
}

const KIND_NAME: Record<RailKind, string> = {
  plan: KIND_LABEL.plan,
  ask: KIND_LABEL.ask,
  decisions: KIND_LABEL.decision,
  round: 'AI 方案汇报',
  reply: '您的确认回复',
  append: '您的追加',
  task: '任务进展',
  stack: '卡片摞',
  notice: '进度通知',
  capsule: '恢复胶囊',
  guard: '守护告警',
}

// 状态名取预览视图的状态词汇表（lib/statusWords，阶段 D2）：要您动手的都叫「待您处理」，处理完的叫「已处理」
const STATE_NAME: Partial<Record<RailKind, Record<RailState, string>>> = {
  plan: { pending: STATUS_WORD.pending, done: STATUS_WORD.done },
  ask: { pending: STATUS_WORD.pending, done: STATUS_WORD.done },
  decisions: { pending: STATUS_WORD.pending, done: STATUS_WORD.done },
  round: { pending: STATUS_WORD.pending, done: STATUS_WORD.done },
}

/**
 * 悬停提示 / 无障碍标签里的「类型 · 状态」，如「计划确认 · 待您处理」。
 * 摞带上内含类型的张数（parts）：「卡片摞 · 任务 3 · 提问 1」——悬停时看得出里面是什么，而不是笼统的「折叠」。
 */
export function railLabel(kind: RailKind, state: RailState, parts?: ReadonlyArray<StackTypeCount>): string {
  const name = KIND_NAME[kind]
  if (kind === 'stack' && parts && parts.length > 0) return `${name} · ${stackTypesText(parts)}`
  const st = STATE_NAME[kind]?.[state]
  return st ? `${name} · ${st}` : name
}

// ---------- 条目 → 轨上条目 ----------

/** 一个条目挂在 DOM 上的信息（data-rail-*）。 */
export interface RailMeta {
  /** 轨内唯一、跨刷新稳定：轮次 round:<会话号>、回复 reply:<会话号>，其余用时间线块的键。 */
  id: string
  kind: RailKind
  state: RailState
  title: string
  /** 条目的提交时间（ISO），悬停提示里显示；解析不了就不显示。 */
  at: string
  /** 轮次 / 回复带上会话号：点击跳转时走列表里现成的「滚到某一轮」。 */
  ref?: string
  /** 只有「摞」带：摞里每种类型各几张（#160）。刻度按它分段着色，悬停提示按它写出张数。 */
  parts?: StackTypeCount[]
}

/** 轨组件量出来的条目：在 RailMeta 之外多一个「在滚动内容里的纵向位置」。 */
export interface RailItem extends RailMeta {
  /** 条目顶边到滚动内容顶部的距离（px）。 */
  top: number
}

export const RAIL_ATTR = {
  kind: 'data-rail-kind',
  state: 'data-rail-state',
  id: 'data-rail-id',
  title: 'data-rail-title',
  at: 'data-rail-at',
  ref: 'data-rail-ref',
  parts: 'data-rail-parts',
} as const

/** 轨组件监听这些属性的变化（状态翻转、标题更新、摞的构成变化要重新量）。 */
export const RAIL_WATCH_ATTRS: string[] = [RAIL_ATTR.kind, RAIL_ATTR.state, RAIL_ATTR.id, RAIL_ATTR.title, RAIL_ATTR.parts]

function oneLine(text: string | undefined, max: number): string {
  return firstLinePreview(text ?? '', max)
}

/** 时间线条目 → 轨上条目（不含「轮次」与「您的回复」，那两个不在时间线里）。 */
export function railMetaOfEntry(e: TimelineEntry): RailMeta {
  switch (e.kind) {
    case 'inbox':
      return { id: e.key, kind: 'append', state: 'done', title: oneLine(e.q.response_text, 48) || '追加', at: e.at }
    case 'notice':
      return { id: e.key, kind: 'notice', state: 'done', title: e.session.title || '进度通知', at: e.at }
    case 'task':
      return { id: e.key, kind: 'task', state: 'done', title: e.card.title || `任务进展 #${e.card.seq}`, at: e.at }
    case 'confirm':
      return {
        id: e.key,
        kind: e.item.kind === 'plan' ? 'plan' : 'ask',
        state: needsAnswer(e.item) ? 'pending' : 'done',
        title: e.item.title || confirmRef(e.item.seq),
        at: e.at,
      }
    case 'decisions':
      return { id: e.key, kind: 'decisions', state: e.group.pending > 0 ? 'pending' : 'done', title: e.group.docTitle, at: e.at }
    case 'guard':
      return { id: e.key, kind: 'guard', state: 'pending', title: e.alert.name || e.alert.preset_id, at: e.at }
    case 'capsule':
      return { id: e.key, kind: 'capsule', state: 'done', title: oneLine(e.capsule.objective, 48) || `检查点 r${e.capsule.revision}`, at: e.at }
  }
}

/** 时间线上的一块（单条或一摞）→ 轨上条目；摞按一个条目算（它在列表里就是一行）。 */
export function railMetaOfBlock(b: TimelineBlock): RailMeta {
  if (b.type === 'stack') {
    const parts = stackTypeCounts(b.entries)
    return {
      id: b.key,
      kind: 'stack',
      state: 'done',
      title: `${b.entries.length} 张卡片`,
      at: b.entries[0]?.at ?? '',
      ...(parts.length > 0 ? { parts } : {}),
    }
  }
  return { ...railMetaOfEntry(b.entry), id: b.key }
}

/** 一轮 AI 方案汇报：等回复（status=pending）就是「还要您处理」。 */
export function railMetaOfRound(s: Pick<FeedbackSession, 'session_id' | 'status' | 'title' | 'created_at'>): RailMeta {
  return {
    id: `round:${s.session_id}`,
    kind: 'round',
    state: s.status === 'pending' ? 'pending' : 'done',
    title: s.title || '方案汇报',
    at: s.created_at,
    ref: s.session_id,
  }
}

/** 您对某一轮的确认回复；at 是您提交回复的时刻（取不到时留空，悬停提示里就不显示时间）。 */
export function railMetaOfReply(s: Pick<FeedbackSession, 'session_id' | 'response_text'>, at: string | undefined): RailMeta {
  return { id: `reply:${s.session_id}`, kind: 'reply', state: 'done', title: oneLine(s.response_text, 48) || '已确认', at: at ?? '', ref: s.session_id }
}

/** RailMeta → 要绑到 DOM 根节点上的 data-* 属性（模板里 v-bind 它）。 */
export function railDomAttrs(m: RailMeta): Record<string, string> {
  const out: Record<string, string> = {
    [RAIL_ATTR.kind]: m.kind,
    [RAIL_ATTR.state]: m.state,
    [RAIL_ATTR.id]: m.id,
    [RAIL_ATTR.title]: m.title,
    [RAIL_ATTR.at]: m.at,
  }
  if (m.ref) out[RAIL_ATTR.ref] = m.ref
  if (m.parts && m.parts.length > 0) out[RAIL_ATTR.parts] = formatStackParts(m.parts)
  return out
}

/** 从 DOM 属性读回一个条目；种类不认识、没有编号、位置不是有限数的一律丢掉（返回 null），状态缺省 / 非法按 done。 */
export function parseRailItem(read: (attr: string) => string | null | undefined, top: number): RailItem | null {
  const kind = read(RAIL_ATTR.kind)
  const id = read(RAIL_ATTR.id)
  if (!isRailKind(kind) || !id || !Number.isFinite(top)) return null
  const parts = kind === 'stack' ? parseStackParts(read(RAIL_ATTR.parts)) : undefined
  return {
    id,
    kind,
    state: read(RAIL_ATTR.state) === 'pending' ? 'pending' : 'done',
    title: read(RAIL_ATTR.title) ?? '',
    at: read(RAIL_ATTR.at) ?? '',
    ref: read(RAIL_ATTR.ref) || undefined,
    ...(parts ? { parts } : {}),
    top,
  }
}

// ---------- 排布 ----------

/** 每道刻度占的高度（px）：点击区的高，也是相邻两道刻度中心的最小间距。点击区 16px 高 × 整条轨宽（32px），PC 上点得准。 */
export const RAIL_PITCH_PX = 16
/** 内容不足这么多屏时不显示轨。 */
export const RAIL_MIN_SCREENS = 1.5
/** 至少有这么多道刻度才显示轨：只有一道的话没有可导航的东西。 */
export const RAIL_MIN_TICKS = 2

export interface RailTick {
  /** 这道刻度对应的条目：点击跳到它；长度 / 颜色 / 脉冲 / 悬停文字都按它。 */
  item: RailItem
  /** 刻度中心在轨内容里的位置（px，距轨内容顶部），整数。 */
  y: number
  len: number
  line: number
  tone: RailTone
  pulse: boolean
  dim: boolean
}

export interface RailLayout {
  ticks: RailTick[]
  /** 轨内容的总高度（px）：放得下就等于可用高度；放不下就是排布需要的高度，轨自己滚动。 */
  height: number
  /** 排布用的间距（px）= 每道刻度点击区的高：点击区是 [y - pitch/2, y + pitch/2]。 */
  pitch: number
}

export interface RailLayoutOptions {
  /** 滚动内容的总高度（px）。 */
  scrollHeight: number
  /** 轨的可用高度（px）。 */
  trackHeight: number
  pitch?: number
}

/**
 * 保序回归（Pool Adjacent Violators）：求非降序列 d，使 Σ(d_i - v_i)² 最小，一次扫描 O(n)。
 * 排布用它实现「相邻两道刻度至少隔一个 pitch，同时离等比位置尽量近」：令 d_i = c_i - i·pitch，则约束变成 d 非降。
 */
export function isotonicFit(values: ReadonlyArray<number>): number[] {
  const sums: number[] = []
  const counts: number[] = []
  for (const v of values) {
    sums.push(v)
    counts.push(1)
    while (sums.length > 1) {
      const k = sums.length - 1
      // 前一块的均值 ≤ 后一块的均值（交叉相乘免除法）：不违反，停；否则两块并成一块
      if (sums[k - 1] * counts[k] <= sums[k] * counts[k - 1]) break
      sums[k - 1] += sums[k]
      counts[k - 1] += counts[k]
      sums.pop()
      counts.pop()
    }
  }
  const out: number[] = []
  for (let b = 0; b < sums.length; b++) {
    const mean = sums[b] / counts[b]
    for (let i = 0; i < counts[b]; i++) out.push(mean)
  }
  return out
}

/**
 * 把量出来的条目排成刻度：先滤掉「不画」的格子（railStyle 查不到）与位置非有限数的条目，按位置排序（同位置保持入参顺序），
 * 再求每道刻度的中心位置 c_i：
 *   - 理想位置 = 条目位置 / 内容总高 × 轨高（等比的缩略轴）；
 *   - 约束：相邻两道至少隔 pitch；第一道的点击区不越出轨顶（c ≥ pitch/2）；
 *   - 放得下（n·pitch ≤ 轨高）时最后一道的点击区也不越出轨底，轨内容高 = 轨高；
 *     放不下时没有下界约束，排布自然退化成等间距（和经典一样），轨内容高 = 最后一道的下沿，轨自己滚动。
 *   - 在约束下离理想位置的平方偏差最小（保序回归）：挤在一起的一团围着各自的理想位置居中，而不是整体往下漂。
 * 不修改入参；入参乱序、有位置相同的都能处理。
 */
export function layoutRail(items: ReadonlyArray<RailItem>, opts: RailLayoutOptions): RailLayout {
  const { scrollHeight, trackHeight } = opts
  const pitch = opts.pitch ?? RAIL_PITCH_PX
  if (!(scrollHeight > 0) || !(trackHeight > 0)) return { ticks: [], height: trackHeight > 0 ? trackHeight : 0, pitch }
  const placed = items
    .map((item, index) => ({ item, index, style: railStyle(item.kind, item.state) }))
    .filter((p): p is typeof p & { style: RailStyle } => p.style !== undefined && Number.isFinite(p.item.top))
    .sort((a, b) => a.item.top - b.item.top || a.index - b.index)
  const n = placed.length
  if (n === 0) return { ticks: [], height: trackHeight, pitch }

  // 先乘后除：整数输入时结果是精确的（先除会带出 220.00000000000003 这种浮点尾巴）
  const want = placed.map(p => Math.min(trackHeight, Math.max(0, (p.item.top * trackHeight) / scrollHeight)))
  const fit = isotonicFit(want.map((w, i) => w - i * pitch))
  const lo = pitch / 2
  const fits = n * pitch <= trackHeight
  const hi = fits ? trackHeight - pitch / 2 - (n - 1) * pitch : Number.POSITIVE_INFINITY

  const ticks: RailTick[] = []
  for (let i = 0; i < n; i++) {
    const { item, style } = placed[i]
    let y = Math.round(Math.min(hi, Math.max(lo, fit[i])) + i * pitch)
    // 取整对「间距 ≥ pitch」是安全的（整数 pitch 下 round 保序），这里只防浮点尾巴把两道的间距吃掉 1px
    if (i > 0 && y < ticks[i - 1].y + pitch) y = ticks[i - 1].y + pitch
    ticks.push({ item, y, len: style.len, line: style.line, tone: style.tone, pulse: style.pulse, dim: style.dim })
  }
  return { ticks, height: fits ? trackHeight : ticks[n - 1].y + pitch / 2, pitch }
}

export interface RailMetrics {
  scrollHeight: number
  clientHeight: number
  trackHeight: number
  /** 排出来的刻度数（不是列表里的条目数：不画的条目不算）。 */
  tickCount: number
}

/** 轨要不要出现：至少 RAIL_MIN_TICKS 道刻度、有可用高度，且内容至少 1.5 屏。 */
export function shouldShowRail(m: RailMetrics): boolean {
  return m.tickCount >= RAIL_MIN_TICKS && m.trackHeight > 0 && m.clientHeight > 0 && m.scrollHeight >= m.clientHeight * RAIL_MIN_SCREENS
}

/**
 * 滚动内容里的位置 → 轨内容里的位置：分段线性，锚点是 (0, 0)、每道刻度的（条目位置, 点击区上沿）、（内容总高, 轨内容总高）。
 * 刻度被推开之后，可视窗口靠它仍然对得上刻度：滚到某个条目顶上时，窗口恰好从它的点击区上沿开始，
 * 窗口下沿落在「视口下方第一个条目」的点击区上沿——窗口罩住的正好是视口里条目的整道刻度。
 * 同一位置有多个锚点时取最后一个；位置越界按两端处理。
 */
export function railMapper(layout: RailLayout, scrollHeight: number): (x: number) => number {
  const xs: number[] = [0]
  const ys: number[] = [0]
  for (const t of layout.ticks) {
    // 刻度已按条目位置排好序；这里只防越界与浮点，保证两列都非降
    xs.push(Math.min(Math.max(scrollHeight, 0), Math.max(xs[xs.length - 1], t.item.top)))
    ys.push(Math.max(ys[ys.length - 1], t.y - layout.pitch / 2))
  }
  xs.push(Math.max(scrollHeight, xs[xs.length - 1]))
  ys.push(Math.max(layout.height, ys[ys.length - 1]))
  const last = xs.length - 1
  return (x: number): number => {
    if (!(x > 0)) return 0
    if (x >= xs[last]) return ys[last]
    // 二分：最后一个 xs[i] ≤ x 的 i（i < last，因为 x < xs[last]），区间 [xs[i], xs[i+1]) 宽度必大于 0
    let a = 0
    let b = last
    while (a < b) {
      const mid = (a + b + 1) >> 1
      if (xs[mid] <= x) a = mid
      else b = mid - 1
    }
    return ys[a] + ((x - xs[a]) * (ys[a + 1] - ys[a])) / (xs[a + 1] - xs[a])
  }
}

/** 当前可视范围在轨内容里的窗口（px）：至少 minPx 高，且不越出轨内容。 */
export function railViewportBox(
  layout: RailLayout,
  m: { scrollTop: number; clientHeight: number; scrollHeight: number },
  minPx = 8,
): { top: number; height: number } {
  const total = layout.height
  if (!(total > 0)) return { top: 0, height: 0 }
  if (!(m.scrollHeight > 0)) return { top: 0, height: total }
  const f = railMapper(layout, m.scrollHeight)
  const top = f(m.scrollTop)
  const bottom = f(m.scrollTop + m.clientHeight)
  const height = Math.min(total, Math.max(minPx, bottom - top))
  return { top: Math.min(total - height, Math.max(0, top)), height }
}

/**
 * 轨自己该滚到哪，才能让可视窗口露出来：窗口已经在可见范围里（上下各留 margin）就不动；
 * 窗口比可见范围还高时对齐窗口顶。轨放得下（内容高 ≤ 可用高）时恒为 0；结果夹在 [0, 内容高 - 可用高]。
 */
export function followRailScroll(
  box: { top: number; height: number },
  m: { scrollTop: number; trackHeight: number; height: number },
  margin = RAIL_PITCH_PX,
): number {
  const max = Math.max(0, m.height - m.trackHeight)
  if (max <= 0) return 0
  let next = m.scrollTop
  const bottom = box.top + box.height
  if (box.height > m.trackHeight - 2 * margin || box.top < next + margin) next = box.top - margin
  else if (bottom > next + m.trackHeight - margin) next = bottom - m.trackHeight + margin
  return Math.min(max, Math.max(0, next))
}

/** 点击刻度后列表该滚到哪：条目顶边上方留 padding，且不超出可滚范围。 */
export function jumpScrollTop(itemTop: number, m: { scrollHeight: number; clientHeight: number }, padding = 24): number {
  const max = Math.max(0, m.scrollHeight - m.clientHeight)
  return Math.max(0, Math.min(max, itemTop - padding))
}

/** 悬停提示里的时间：今天只显示时分，今年显示月-日 时:分，更早带年份；解析不了返回空串。 */
export function formatRailTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const n = new Date(now)
  const p2 = (v: number) => String(v).padStart(2, '0')
  const hm = `${p2(t.getHours())}:${p2(t.getMinutes())}`
  if (t.getFullYear() !== n.getFullYear()) return `${t.getFullYear()}-${p2(t.getMonth() + 1)}-${p2(t.getDate())} ${hm}`
  if (t.getMonth() === n.getMonth() && t.getDate() === n.getDate()) return hm
  return `${p2(t.getMonth() + 1)}-${p2(t.getDate())} ${hm}`
}
