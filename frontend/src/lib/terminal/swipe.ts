// 移动端「向右滑动露出操作条」的手势状态机。纯函数，不碰 DOM：
// 组件把指针事件翻译成 start / move / end，这里决定「是不是横向滑动」「现在偏移多少」「松手后展开还是收起」。
// 方向规则：只认向右滑（收起状态下向左滑、纵向滚动都忽略，让页面照常滚动）；展开后向左滑或点别处收起。

export interface SwipeConfig {
  /** 操作条完全展开时内容向右移动的距离（px）。 */
  width: number
  /** 手指移动超过这个距离才判定方向（px）；小于它的抖动不算手势。 */
  lockDistance: number
  /** 横向位移至少是纵向位移的多少倍，才算「横向滑动」；否则当作纵向滚动。 */
  ratio: number
}

export const SWIPE_DEFAULTS = { lockDistance: 8, ratio: 1.5 } as const

/** 松手时的横向速度（px/ms）超过它就按「甩动」处理，不看位置。 */
export const SWIPE_FLICK_SPEED = 0.45
/** 没有甩动时：从收起拖到这个比例以上就展开。 */
export const SWIPE_OPEN_RATIO = 0.4
/** 没有甩动时：从展开拖回到这个比例以下才收起（比展开的门槛高，避免误收）。 */
export const SWIPE_STAY_OPEN_RATIO = 0.6

export type SwipePhase =
  /** 没有手势。 */
  | 'idle'
  /** 手指按下，还没动够距离，方向未定。 */
  | 'pending'
  /** 判定为横向滑动，内容跟手移动。 */
  | 'dragging'
  /** 判定为纵向滚动（或不支持的方向）：本次手势到抬手为止都不再处理。 */
  | 'ignored'

export interface SwipeState {
  phase: SwipePhase
  startX: number
  startY: number
  /** 手势开始时操作条是否已展开。 */
  startOpen: boolean
  /** 内容当前的横向偏移（0 = 收起，width = 完全展开）。 */
  offset: number
}

export const SWIPE_IDLE: SwipeState = { phase: 'idle', startX: 0, startY: 0, startOpen: false, offset: 0 }

/** 偏移限制在 [0, width]；width 为 0 或非有限数时恒为 0。 */
export function clampOffset(v: number, width: number): number {
  if (!Number.isFinite(v) || !Number.isFinite(width) || width <= 0) return 0
  return Math.min(width, Math.max(0, v))
}

/** 手指按下。open 是操作条当前是否已展开。 */
export function swipeStart(x: number, y: number, open: boolean, cfg: SwipeConfig): SwipeState {
  return { phase: 'pending', startX: x, startY: y, startOpen: open, offset: open ? clampOffset(cfg.width, cfg.width) : 0 }
}

/** 手指移动。返回新的状态（不修改入参）。 */
export function swipeMove(s: SwipeState, x: number, y: number, cfg: SwipeConfig): SwipeState {
  if (s.phase === 'idle' || s.phase === 'ignored') return s
  const dx = x - s.startX
  const dy = y - s.startY
  let phase: SwipePhase = s.phase
  if (phase === 'pending') {
    if (Math.max(Math.abs(dx), Math.abs(dy)) < cfg.lockDistance) return s
    if (Math.abs(dx) < Math.abs(dy) * cfg.ratio) return { ...s, phase: 'ignored' }
    // 收起状态下向左滑没有意义（操作条在左边），交还给页面。
    if (!s.startOpen && dx < 0) return { ...s, phase: 'ignored' }
    phase = 'dragging'
  }
  const base = s.startOpen ? cfg.width : 0
  return { ...s, phase, offset: clampOffset(base + dx, cfg.width) }
}

export interface SwipeResult {
  /** 松手后操作条是否展开。 */
  open: boolean
  /** 这次手势是否真的拖动过内容；是的话，紧随其后的那次 click 要被吞掉，免得抬手就点开了会话。 */
  moved: boolean
}

/**
 * 手指抬起。velocity 是松手瞬间的横向速度（px/ms，向右为正），没有就传 0。
 * 甩动优先：向右快甩直接展开，向左快甩直接收起；否则按位置判断（展开的门槛比收起的低，见常量）。
 */
export function swipeEnd(s: SwipeState, cfg: SwipeConfig, velocity = 0): SwipeResult {
  if (s.phase !== 'dragging') return { open: s.startOpen, moved: false }
  let open: boolean
  if (velocity >= SWIPE_FLICK_SPEED) open = true
  else if (velocity <= -SWIPE_FLICK_SPEED) open = false
  else if (s.startOpen) open = s.offset >= cfg.width * SWIPE_STAY_OPEN_RATIO
  else open = s.offset >= cfg.width * SWIPE_OPEN_RATIO
  return { open, moved: true }
}

/** 手势被系统取消（例如浏览器接管了纵向滚动）：回到手势开始前的状态。 */
export function swipeCancel(s: SwipeState): SwipeResult {
  return { open: s.startOpen, moved: s.phase === 'dragging' }
}

/** 根据最近两次采样算横向速度（px/ms，向右为正）；时间差过小或过大（停顿后才抬手）都当作 0。 */
export function swipeVelocity(prevX: number, prevT: number, x: number, t: number): number {
  const dt = t - prevT
  if (dt <= 0 || dt > 120) return 0
  return (x - prevX) / dt
}

/** 内容当前应有的 translateX 值（px）。 */
export function swipeTranslate(s: SwipeState, open: boolean, cfg: SwipeConfig): number {
  if (s.phase === 'dragging') return clampOffset(s.offset, cfg.width)
  return open ? clampOffset(cfg.width, cfg.width) : 0
}
