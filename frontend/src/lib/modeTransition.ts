// RelayMesh ↔ ExecMesh 切换动画的决策（纯函数，便于单元测试）。
// 动画本身是浏览器的 View Transitions：旧画面截成一张图，叠在新页面上方，用一层随时间推进的遮罩让它从一角向另一角
// 化作粒子消散（样式见 assets/globals.css 的「项目切换动画」）；路由守卫（router/modeTransition.ts）负责按这里的结论发起。

import { modeOfPath, type AppMode } from './mode'

/** 切换方向：RelayMesh → ExecMesh 是 forward（从左上扫向右下）；回来是 back（从右下扫向左上）。 */
export type ModeSwitchDirection = 'forward' | 'back'

export interface ModeSwitchEnv {
  /** 浏览器支持 View Transitions（document.startViewTransition）。 */
  supported: boolean
  /** 设置里开着「项目切换动画」。 */
  enabled: boolean
  /** 系统 / 浏览器要求减少动态效果。 */
  reducedMotion: boolean
  /** 页面不可见（后台标签页）：没人看，也省一次截图。 */
  hidden: boolean
}

/** 动画总时长（毫秒），与样式里的 animation 时长保持一致；也是守卫等待收尾的参考。 */
export const MODE_SWITCH_MS = 620

/** 守卫等不到路由收尾时的兜底（毫秒）：到点就放行，动画随之结束，不让界面一直停在截图上。 */
export const MODE_SWITCH_SAFETY_MS = 3000

export function modeSwitchDirection(from: AppMode, to: AppMode): ModeSwitchDirection {
  return from === 'relay' && to === 'exec' ? 'forward' : 'back'
}

/**
 * 这次路由切换要不要播切换动画；不播返回 null。
 * fromPath 为 null 表示这是首次进入页面（没有「上一页」）。只有跨项目的切换才播，同一项目里换页不播。
 */
export function modeSwitchPlan(fromPath: string | null, toPath: string, env: ModeSwitchEnv): ModeSwitchDirection | null {
  if (fromPath === null) return null
  if (!env.supported || !env.enabled || env.reducedMotion || env.hidden) return null
  const from = modeOfPath(fromPath)
  const to = modeOfPath(toPath)
  if (from === to) return null
  return modeSwitchDirection(from, to)
}
