// 盲文旋转符的相位对齐（#197）：让全页所有指示符的动画起点都落在文档时间线的 0 点，于是任何时刻它们显示同一帧。
// 动画本身是纯 CSS（BrailleSpinner.vue），没有定时器；这里只在「动画开始」的那一刻改一次起点，之后不再有任何脚本参与。
// 纯逻辑与测试在 spinner.ts / spinner.test.ts。
import { SPINNER_ANIMATION_NAME } from './spinner'

/** getAnimations() 返回的动画里，这里只关心名字与起点；用结构类型是为了在没有 DOM 的环境里也能测。 */
export interface SpinnerAnimationLike {
  /** CSS 动画才有这个字段（CSSAnimation.animationName）。 */
  readonly animationName?: string
  startTime: unknown
}

export interface SpinnerTargetLike {
  getAnimations?: () => SpinnerAnimationLike[]
}

/**
 * 把 target 上的转圈动画起点设为时间线 0 点，返回改了几个。
 *
 * 为什么是 0 点：动画的当前时间 = 时间线当前时间 − 起点；起点都是 0，所有指示符在同一时刻
 * 的进度就一样（一圈 800ms，对 800 取模），不管它们是什么时候挂上页面的。
 * 动画一开始（`animationstart`）才改，所以浏览器已经把样式算好了，不需要为此强制一次样式重算；
 * 起点是在动画刚开始的一两帧内改的，最多有一帧（约 16ms）与别的指示符不同相，肉眼看不出，之后始终同相。
 * 不支持 getAnimations 的环境（极老的浏览器、没有 DOM 的测试环境）什么也不做：指示符照转，只是各自的相位不同。
 */
export function alignSpinnerPhase(target: SpinnerTargetLike | null | undefined): number {
  const list = typeof target?.getAnimations === 'function' ? target.getAnimations() : []
  let n = 0
  for (const a of list) {
    if (a.animationName === SPINNER_ANIMATION_NAME) {
      a.startTime = 0
      n++
    }
  }
  return n
}
