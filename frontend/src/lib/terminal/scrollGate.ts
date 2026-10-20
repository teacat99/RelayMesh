// 「滚到顶之后继续往上滚」的手势状态机（纯逻辑，不依赖 DOM / Vue，便于单元测试）。
//
// 终端往回滚到最顶端之后，用户还在往上滚，就是想看更早的内容：这一次滚动直接去加载，不再要求「再滚一次」来确认。
// 要防住的是一次手势触发多次加载：触控板的惯性滚动在一次手势结束后还会连续送来很多滚轮事件，它们都属于同一次手势，
// 只有第一个触发，其余忽略。所以用事件之间的间隔来区分手势：间隔小于 gestureGapMs 的事件属于同一次手势。

export type GateResult = 'fire' | 'ignore'

export interface ScrollGateOptions {
  /** 事件间隔小于这个毫秒数，认为还是同一次手势（包含惯性滚动的尾巴）。 */
  gestureGapMs?: number
}

export const DEFAULT_GESTURE_GAP_MS = 450

export class ScrollGate {
  private lastAt = Number.NEGATIVE_INFINITY
  private readonly gap: number

  constructor(opts: ScrollGateOptions = {}) {
    this.gap = opts.gestureGapMs ?? DEFAULT_GESTURE_GAP_MS
  }

  /**
   * 已经在最顶端时，又有一次「向上滚」的输入。now 为当前毫秒时间戳。
   * 新手势的第一个输入返回 'fire'（去加载）；同一次手势里的后续输入（含惯性尾巴）返回 'ignore'。
   */
  upAtTop(now: number): GateResult {
    const gap = now - this.lastAt
    this.lastAt = now
    return gap < this.gap ? 'ignore' : 'fire'
  }

  /**
   * 手势还在继续，但这个输入本来就不该触发（例如正在加载）：把它记作这次手势的一部分，
   * 这样加载结束之后，这次手势剩下的尾巴不会被当成新手势、再触发一遍。
   */
  hold(now: number): void {
    this.lastAt = now
  }

  /** 一次新的手势开始（例如手指重新按下）：下一次输入一定不会被当作上一次手势的尾巴。 */
  newGesture(): void {
    this.lastAt = Number.NEGATIVE_INFINITY
  }

  /** 忘掉手势（向下滚、离开顶端、键入、提示超时都走这里）：之后的第一次输入就是新手势。 */
  reset(): void {
    this.lastAt = Number.NEGATIVE_INFINITY
  }
}
