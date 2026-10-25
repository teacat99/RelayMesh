// 切换明暗主题时，临时关掉全局的颜色过渡（#185）。
//
// 为什么：globals.css 给 body 下「所有元素」写了 background-color / border-color / color 0.15s 的过渡（悬停淡入靠它）。
// 切主题时 :root 上一整组颜色变量同时改值，每个元素的这三个颜色都变，浏览器要一次建出「元素数 × 2.6」个过渡动画：
// 生产上 2.5 万元素的长会话，同步卡 4 秒多、建出 6.4 万个动画（measure-style 实测，见 .cursor/planning/2026-10-22-185-style-recalc-plan.md）。
//
// 怎么做：切换的同一刻给 <html> 加 `theme-switching` 类（globals.css 里对应一条 `transition: none !important`），
// 等切换后的那一次渲染完成（两个动画帧之后）再去掉：这时没有任何属性在变，去掉类不会再起过渡。
// 全局过渡规则本身没动，所以悬停淡入等其余行为不受影响，只是「切主题」那一刻变成瞬间切换。
//
// 这个模块只依赖「类名集合」与「调度器」两个最小接口，不碰 document / window，单元测试可以用假的来跑。

export const THEME_SWITCHING_CLASS = 'theme-switching'

/** 兜底：渲染循环被暂停（后台标签页不跑 requestAnimationFrame）时，最迟这么久之后也要把类去掉。 */
export const THEME_SWITCH_FALLBACK_MS = 1500

export interface ClassListLike {
  add(name: string): void
  remove(name: string): void
  contains(name: string): boolean
}

export interface FrameScheduler {
  /** 在下一个动画帧里执行（回调里再调一次就是再下一帧）。 */
  frame(cb: () => void): void
  /** 普通计时器。 */
  timeout(cb: () => void, ms: number): void
}

/** 浏览器里的调度器：requestAnimationFrame + setTimeout。 */
export function browserScheduler(win: Pick<Window, 'requestAnimationFrame' | 'setTimeout'>): FrameScheduler {
  return {
    frame: cb => {
      win.requestAnimationFrame(() => cb())
    },
    timeout: (cb, ms) => {
      win.setTimeout(cb, ms)
    },
  }
}

/**
 * 造一个「压住过渡」的函数：调用时给 <html> 加上 `theme-switching`，两帧之后（或兜底计时到了）去掉。
 * 连续快速调用时只有「最近一次」的回调有资格去掉类：前一次的回调作废，类一直留到最后一次切换完成，
 * 不会出现「第一次的计时把第二次切换的过渡提前放开」。
 */
export function createTransitionSuppressor(classList: ClassListLike, sched: FrameScheduler): () => void {
  let token = 0
  return function suppress() {
    const mine = ++token
    classList.add(THEME_SWITCHING_CLASS)
    // 帧回调与兜底计时谁先到谁放开；另一个到的时候类已经不在了，不再写一次 DOM（多余的类名变更记录与样式失效）
    const release = () => {
      if (token === mine && classList.contains(THEME_SWITCHING_CLASS)) classList.remove(THEME_SWITCHING_CLASS)
    }
    sched.frame(() => sched.frame(release))
    sched.timeout(release, THEME_SWITCH_FALLBACK_MS)
  }
}

/**
 * 把明暗状态写到 <html> 的 `dark` 类上；明暗真的变了才先调用 suppress（必须在改类之前，
 * 这样切换后的第一次样式计算就已经是「没有过渡」）。没变就什么都不写（也就没有多余的类名变更记录）。
 * 返回这次是否真的变了。
 */
export function applyDarkClass(classList: ClassListLike, dark: boolean, suppress?: () => void): boolean {
  if (classList.contains('dark') === dark) return false
  suppress?.()
  if (dark) classList.add('dark')
  else classList.remove('dark')
  return true
}
