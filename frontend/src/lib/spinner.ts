/**
 * 「工作中 / 运行中」的指示符（#130）：命令行里常见的盲文旋转符（cli-spinners 的 dots）。
 *
 * 为什么不用 CSS 旋转的图标：这串字符本身就是「帧」，每帧换一个字形才是想要的样子；
 * 代价是需要一个定时器，所以把定时器管严：
 *   - 全页共用一个：同时有十个指示符在转也只有一个 setInterval，所有指示符同一帧、不会各转各的；
 *   - 有人用才启动、最后一个用完就停（空闲页面零占用）；
 *   - 页面不可见时暂停（切到别的标签页、窗口最小化），回来再接着转；
 *   - 系统开了「减少动态效果」时停在第一帧，不转。
 *
 * 这里是纯逻辑（没有 window / document / Vue 依赖），环境差异由 SpinnerEnv 注入，便于单测。
 * 浏览器侧的绑定在 useSpinner.ts，组件在 components/ui/spinner/BrailleSpinner.vue。
 */

/** 十帧，顺序与字形固定（需求原文：⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏）。 */
export const SPINNER_FRAMES = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'] as const

/** 每帧停留的毫秒数：与 cli-spinners 的 dots 一致，约 12 帧每秒。 */
export const SPINNER_INTERVAL_MS = 80

/** 第 i 帧的字形；i 可以超出范围（取模），也可以是负数。 */
export function frameAt(i: number): string {
  const n = SPINNER_FRAMES.length
  const k = Number.isFinite(i) ? Math.trunc(i) : 0
  return SPINNER_FRAMES[((k % n) + n) % n] as string
}

/** SpinnerClock 需要的环境：测试里换成假的，浏览器里是 window / document。 */
export interface SpinnerEnv {
  /** 页面当前是否不可见。 */
  hidden(): boolean
  /** 是否要求减少动态效果。 */
  reduced(): boolean
  setInterval(fn: () => void, ms: number): unknown
  clearInterval(handle: unknown): void
  /** 订阅「可见性 / 动态效果偏好」变化；返回取消订阅的函数。 */
  onChange(fn: () => void): () => void
}

/**
 * 共享的帧时钟。subscribe 返回释放函数；第一个订阅者让它启动，最后一个释放让它停止。
 * onFrame 在每次帧号变化（含重置回第一帧）时被调用，浏览器侧用它去更新一个响应式的 ref。
 */
export class SpinnerClock {
  private index = 0
  private users = 0
  private timer: unknown = undefined
  private off: (() => void) | undefined

  constructor(
    private readonly env: SpinnerEnv,
    private readonly onFrame: (index: number) => void = () => {},
  ) {}

  /** 当前帧号（0 ~ 9）。 */
  get frame(): number {
    return this.index
  }

  /** 定时器是否在跑（测试与排查用）。 */
  get running(): boolean {
    return this.timer !== undefined
  }

  /** 正在使用的指示符个数。 */
  get subscribers(): number {
    return this.users
  }

  /** 登记一个使用者；重复调用返回的释放函数是幂等的。 */
  subscribe(): () => void {
    this.users++
    if (this.users === 1) this.off = this.env.onChange(() => this.sync())
    this.sync()
    let released = false
    return () => {
      if (released) return
      released = true
      this.users--
      if (this.users === 0) {
        this.off?.()
        this.off = undefined
      }
      this.sync()
    }
  }

  private tick = (): void => {
    this.index = (this.index + 1) % SPINNER_FRAMES.length
    this.onFrame(this.index)
  }

  /** 按「有没有人在用 / 页面是否可见 / 是否减少动态效果」决定定时器开还是关。 */
  private sync(): void {
    const reduced = this.env.reduced()
    const want = this.users > 0 && !reduced && !this.env.hidden()
    if (want && this.timer === undefined) {
      this.timer = this.env.setInterval(this.tick, SPINNER_INTERVAL_MS)
    } else if (!want && this.timer !== undefined) {
      this.env.clearInterval(this.timer)
      this.timer = undefined
    }
    // 减少动态效果：固定在第一帧，不要停在某个随机的中间帧上
    if (reduced && this.index !== 0) {
      this.index = 0
      this.onFrame(0)
    }
  }
}
