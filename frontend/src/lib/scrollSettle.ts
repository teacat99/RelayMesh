// 平滑滚动落地后的校准（#158，预览视图用）。
//
// 背景：历史轮次套了 content-visibility:auto（见 PreviewMessageList 的 pv-cv），没进过视口的轮次高度是「预估值」，
// 一旦滚到那里才按真实高度渲染。浏览器的平滑滚动（scrollIntoView / scrollTo 的 smooth）在起跑时就把终点算死了，
// 途中新渲染出来的轮次改变了总高度，落点就会偏——偏多少取决于「预估高度」与真实高度差多少、途中经过几轮。
// 所以平滑滚动结束后（scrollTop 静止一小会儿）再量一次目标条目的真实位置，偏了就瞬时补一下，最多补几次；
// 用户在这期间自己动了（滚轮 / 触摸 / 按键）就立刻放弃，绝不和用户抢滚动。
//
// 另一个同源的问题：向上「载入更早的轮次」时要把视口原来的位置钉住（不然内容会突然下沉）。
// 以前靠「载入前后 scrollHeight 之差」补偿；套了 content-visibility 之后，新载入的轮次里有的是占位高度、有的是真实高度，
// 而且离视口近的那几轮会在之后几帧里从占位变成真实——这个差既算不准，浏览器自带的滚动锚定也偶尔不补（实测约每 14 批出现一次、偏 2000px 左右）。
// 所以改成「钉锚点」：载入前记下锚点条目（原来的第一轮）在视口里的位置，载入后以及之后几帧里把它放回原位；
// 用户自己滚动（滚轮 / 触摸 / 按键，或手机的惯性滑动）就立刻放手。
//
// 真实长会话（生产 154 轮）与把 CPU 放慢 4 倍的实测说明上面还不够：紧挨着锚点的那一两轮（视口顶上露出几十像素）是新插入的占位，
// 下一帧才渲染成真实高度；浏览器的滚动锚定只补了「更上面那一轮」的增高，没补这一轮自己的增高（它选中的锚点节点就是这个占位盒子，
// 盒子顶边没动）——放慢 4 倍的实验室 14 批里有 12 批偏约 2000px，生产的真实长会话 14 批里偏 7 批（最多 1681px）；而旧的钉法一看到 scrollTop 被浏览器改过就当成用户接手而放手，
// 偏差就留下了。两处一起改：
//   · 紧挨着锚点的那几轮（holdBoundaryKeys）不套 pv-cv：插入的那一刻就是真实高度，钉锚点的第一次校正看到的已经是最终几何，之后这一带不再有「晚一帧的增高」；
//   · 钉锚点认得出「浏览器的滚动锚定调整」（foreignScroll）：scrollTop 被动过且版面高度也变了才算锚定调整，继续钉；高度没变的才是用户（惯性滑动）。
//
// 本文件只放纯函数（落点怎么算、什么时候不用动、哪几轮不套占位、scrollTop 被谁动过），驱动在 composables/useScrollSettle.ts。
import { jumpScrollTop } from './timelineRail'

/** 落点与目标相差不到这么多（px）就不再补。 */
export const SETTLE_TOLERANCE_PX = 2
/** 目标条目的顶边离容器顶边留多少（px）：与 jumpScrollTop 的默认值、轮次元素上的 scroll-mt-6（24px）一致。 */
export const SETTLE_PADDING_PX = 24
/** 最多补几次：每补一次都可能让新的轮次进入视口、再改一点高度，但很快收敛，三次足够，防止极端情况下来回震荡。 */
export const SETTLE_MAX_FIXES = 3
/** scrollTop 静止多久（毫秒）才算平滑滚动已经停了。 */
export const SETTLE_QUIET_MS = 120
/** 整个校准最多持续多久（毫秒），到点无条件放弃。 */
export const SETTLE_TIMEOUT_MS = 4000

export interface SettleMetrics {
  /** 目标条目此刻在滚动内容里的位置（px，条目顶边距内容顶部）。 */
  itemTop: number
  scrollTop: number
  scrollHeight: number
  clientHeight: number
}

/**
 * 现在该把容器放到哪：与 jumpScrollTop 同一套规则（条目顶边上方留 padding，夹在可滚范围内）。
 * 已经在目标的容差之内返回 null（不用动）。
 */
export function settleTarget(
  m: SettleMetrics,
  padding: number = SETTLE_PADDING_PX,
  tolerance: number = SETTLE_TOLERANCE_PX,
): number | null {
  if (![m.itemTop, m.scrollTop, m.scrollHeight, m.clientHeight].every(Number.isFinite)) return null
  const target = jumpScrollTop(m.itemTop, m, padding)
  return Math.abs(target - m.scrollTop) <= tolerance ? null : target
}

/** 钉锚点时，锚点偏差不超过这么多（px）就不动（scrollTop 会按设备像素取整，亚像素的差不用追）。 */
export const HOLD_TOLERANCE_PX = 1
/** 钉锚点：连续这么久（毫秒）没有偏差，就认为布局已经稳定，收手。 */
export const HOLD_QUIET_MS = 300
/** 钉锚点最长持续多久（毫秒），到点无条件放弃。 */
export const HOLD_TIMEOUT_MS = 1500

/**
 * 锚点此刻比预期位置偏了多少：正数 = 比预期更靠下，需要把 scrollTop 加上这么多才能拉回原位；负数反之。
 * 在容差之内（或数据不是有限数）返回 null，表示不用动。
 */
export function holdDelta(offsetNow: number, offsetWanted: number, tolerance: number = HOLD_TOLERANCE_PX): number | null {
  if (!Number.isFinite(offsetNow) || !Number.isFinite(offsetWanted)) return null
  const d = offsetNow - offsetWanted
  return Math.abs(d) <= tolerance ? null : d
}

/** scrollTop 被别人动过之后，是谁动的。 */
export type ForeignScroll = 'none' | 'layout' | 'user'

/**
 * 容器的 scrollTop 不在我们上次放的位置（ours）时，判断是谁动的：
 *   - 'none'：没人动，还在我们放的位置；
 *   - 'layout'：浏览器的滚动锚定——它只在版面变了（scrollHeight 变了）时才会调 scrollTop，所以「scrollTop 与 scrollHeight 都变了」算它；
 *     这种情况要继续钉（锚定常常只补一部分增高）；
 *   - 'user'：scrollTop 变了而版面高度没变——只能是用户（手机上手指抬起后的惯性滑动没有任何事件），立刻放手，绝不把滑动拉回来。
 * 数据不是有限数时按 'user' 处理（宁可放手）。
 */
export function foreignScroll(scrollTop: number, ours: number, heightNow: number, heightBefore: number): ForeignScroll {
  if (![scrollTop, ours, heightNow, heightBefore].every(Number.isFinite)) return 'user'
  if (scrollTop === ours) return 'none'
  return heightNow !== heightBefore ? 'layout' : 'user'
}

/** 钉锚点的状态（驱动每一帧带着它走一步）。 */
export interface HoldState {
  /** 我们自己上一次把容器放到的位置。 */
  ours: number
  /** 上一次看到的版面高度。 */
  lastHeight: number
  /** 最近一次「偏了、去补」的时刻（毫秒时间戳）；没有偏差的安静期从这里算起。 */
  quietSince: number
}

export interface HoldTick {
  now: number
  startedAt: number
  scrollTop: number
  scrollHeight: number
  /** 锚点此刻的顶边相对容器顶边（视口里）的位置。 */
  offsetNow: number
  /** 载入前记下的、锚点应该待的位置。 */
  offsetWanted: number
  quietMs?: number
  timeoutMs?: number
}

export type HoldAction =
  | { kind: 'stop'; reason: 'timeout' | 'user' | 'settled' }
  | { kind: 'fix'; delta: number }
  | { kind: 'wait' }

/**
 * 钉锚点的一步（驱动每帧调用一次；纯函数，决策都在这里，好测）：
 *   1. 超过总时限：放弃；
 *   2. scrollTop 被别人动过：版面高度没变 = 用户（惯性滑动）→ 放弃；版面也变了 = 浏览器的滚动锚定 → 认下它放的位置（state.ours），继续；
 *   3. 锚点偏出容差：返回要补的量（驱动把 scrollTop 加上它，再把读回来的 scrollTop 记成 state.ours）；
 *   4. 没偏差且已经连续 quietMs 没偏过：版面稳定，收手；否则等下一帧。
 */
export function holdStep(state: HoldState, t: HoldTick): { action: HoldAction; state: HoldState } {
  const quietMs = t.quietMs ?? HOLD_QUIET_MS
  const timeoutMs = t.timeoutMs ?? HOLD_TIMEOUT_MS
  if (t.now - t.startedAt > timeoutMs) return { action: { kind: 'stop', reason: 'timeout' }, state }
  const who = foreignScroll(t.scrollTop, state.ours, t.scrollHeight, state.lastHeight)
  if (who === 'user') return { action: { kind: 'stop', reason: 'user' }, state }
  const next: HoldState = { ...state, ours: who === 'layout' ? t.scrollTop : state.ours, lastHeight: t.scrollHeight }
  const delta = holdDelta(t.offsetNow, t.offsetWanted)
  if (delta !== null) return { action: { kind: 'fix', delta }, state: { ...next, quietSince: t.now } }
  if (t.now - state.quietSince >= quietMs) return { action: { kind: 'stop', reason: 'settled' }, state: next }
  return { action: { kind: 'wait' }, state: next }
}

/**
 * 豁免几轮：视口 900px 时 CSS 的 content-visibility:auto 在视口外约半屏（约 450px）内就会渲染，视口再高一些也是它的一半；
 * 一轮的真实高度通常上千像素，4 轮足够让第 5 轮的底边离视口顶部远超这个范围。
 */
export const HOLD_BOUNDARY_ROUNDS = 4

/**
 * 紧挨着锚点的几轮不套 pv-cv（离屏跳过渲染）：向上载入一批更早的轮次时，这批最下面的 boundary 轮（紧挨着原来的第一轮）
 * 要从插入的那一刻就是真实高度，否则它们是占位、下一帧才渲染成真实高度，会在锚点已经钉好之后把锚点顶下去（浏览器的滚动锚定补不全）。
 * 视口顶上露出的是这一带；再往上的轮次离视口远（几千像素），占位 → 真实的变化发生在视口外、浏览器的锚定补得了。
 *
 * keys：全部轮次的 key（按时间从早到晚）；visibleCount：载入前显示的轮次数（取最近的这么多）；batch：这次再多载入几轮。
 * 返回要豁免的轮次 key（可能为空：已经全部显示、或这批不够 boundary 轮）。
 */
export function holdBoundaryKeys(
  keys: readonly string[],
  visibleCount: number,
  batch: number,
  boundary: number = HOLD_BOUNDARY_ROUNDS,
): string[] {
  const total = keys.length
  const oldFirst = Math.max(0, total - Math.max(0, visibleCount))
  const newFirst = Math.max(0, total - Math.max(0, visibleCount) - Math.max(0, batch))
  const from = Math.max(newFirst, oldFirst - Math.max(0, boundary))
  return from >= oldFirst ? [] : keys.slice(from, oldFirst)
}
