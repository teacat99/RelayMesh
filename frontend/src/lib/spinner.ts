/**
 * 「工作中 / 运行中」的指示符（#130 / #197）：命令行里常见的盲文旋转符（cli-spinners 的 dots）。
 *
 * 实现方式（#197）：整个动画交给浏览器的合成器线程。BrailleSpinner.vue 里是一条十行字形的竖条，
 * 在固定大小的裁剪盒里用 `transform: translateY(-100%)` + `steps(10)` 每 80ms 上移一行。
 * 动画期间没有 JS 定时器、不写 DOM、不触发样式重算与布局。
 *
 * 为什么不再用「共享 setInterval + 响应式帧号」：那种做法每 80ms 改一次所有指示符的文字
 * （每秒约 125 次 DOM 变更），在 1.8 万个节点的真实页面上让空闲主线程占用 20%～30% 个核，
 * 样式重算与布局每秒十几次，是手机输入法卡顿（#198）的主要嫌疑（D-329、D-345）。
 *
 * 保留下来的约定：
 *   - 全页所有指示符同一帧、不各转各的：每个指示符的动画一开始，就把起点对齐到文档时间线的 0 点（spinnerPhase.ts）；
 *   - 页面不可见时浏览器自己不再出帧，回来接着转，不需要脚本管；
 *   - 系统开了「减少动态效果」时停在第一帧：CSS 媒体查询里不挂动画（BrailleSpinner.vue）。
 *
 * 这里是纯逻辑（没有 window / document / Vue 依赖），便于单测；样式在 BrailleSpinner.vue，
 * 浏览器侧的相位对齐在 spinnerPhase.ts。
 */

/** 十帧，顺序与字形固定（需求原文：⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏）。 */
export const SPINNER_FRAMES = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'] as const

/** 每帧停留的毫秒数：与 cli-spinners 的 dots 一致，约 12 帧每秒。 */
export const SPINNER_INTERVAL_MS = 80

/** 转一圈的毫秒数（CSS 动画的时长，必须等于 帧数 × 每帧毫秒数）。 */
export const SPINNER_CYCLE_MS = SPINNER_FRAMES.length * SPINNER_INTERVAL_MS

/** CSS 关键帧的名字（BrailleSpinner.vue 的 @keyframes 与这里必须一致，相位对齐靠它认出动画）。 */
export const SPINNER_ANIMATION_NAME = 'braille-spin'

/** 第 i 帧的字形；i 可以超出范围（取模），也可以是负数。 */
export function frameAt(i: number): string {
  const n = SPINNER_FRAMES.length
  const k = Number.isFinite(i) ? Math.trunc(i) : 0
  return SPINNER_FRAMES[((k % n) + n) % n] as string
}

/**
 * 文档时间线走到 timeMs 毫秒时，所有指示符应该显示第几帧（0 ~ 9）。
 * 动画起点对齐到时间线 0 点之后，这就是「同一时刻所有指示符显示的帧」；测试与排查用它换算，
 * 页面本身不需要这个值（动画由浏览器自己推进）。
 */
export function frameIndexAt(timeMs: number): number {
  const t = Number.isFinite(timeMs) ? timeMs : 0
  const inCycle = ((t % SPINNER_CYCLE_MS) + SPINNER_CYCLE_MS) % SPINNER_CYCLE_MS
  return Math.min(Math.floor(inCycle / SPINNER_INTERVAL_MS), SPINNER_FRAMES.length - 1)
}
