// 消息列表「贴底」的纯逻辑（#222 / #833，预览视图用）。
//
// 问题：列表停在底部时，内容或容器大小的晚到变化——引用链接晚装饰、图片 / 字体晚加载、占位高度换成真实高度、
// 窗口或输入区高度变化——会让 scrollHeight 变了而 scrollTop 没变，最后一条就滑出底部（实测：窗口从 1440 缩到 1100 之后，列表离底 93 px）。
// 原来只有「时间线条目变了」「有新的追加」「新一轮」「切换工作流」才会重新贴底，这些晚到变化不在其中。
//
// 做法：组件用 ResizeObserver 观察滚动容器与内容列，大小一变就问这里「该不该贴回去、贴到哪」，在浏览器绘制之前改好 scrollTop，用户看不到中间那一帧。
// 判定只认「最近一次滚动之后是否仍紧贴底部」：用户向上翻过哪怕几个像素（超出容差）就不再自动贴回，绝不和用户抢滚动。

/** 距底部不超过这么多 px 就算「贴着底部」（scrollTop 会按设备像素取整，留一点余量；比 80px 的「回到底部」按钮阈值严得多）。 */
export const PIN_TOLERANCE_PX = 4

/** 已经贴着的才需要贴回去：偏离超过这个值才动手，免得亚像素抖动反复写 scrollTop。 */
export const REPIN_MIN_GAP_PX = 0.5

export interface ScrollMetrics {
  scrollHeight: number
  scrollTop: number
  clientHeight: number
}

/** 视口底边离内容底边还有多远（px）；负数（iOS 回弹等）按 0 以内算贴底。 */
export function bottomGap(m: ScrollMetrics): number {
  return m.scrollHeight - m.scrollTop - m.clientHeight
}

/** 现在是否紧贴底部（用在滚动事件里更新「最近一次是否贴底」）。 */
export function isPinnedAtBottom(m: ScrollMetrics, tolerance: number = PIN_TOLERANCE_PX): boolean {
  return bottomGap(m) <= tolerance
}

/** 上一次观察到的大小（滚动容器的 scrollHeight 与 clientHeight）。 */
export interface SizeSnapshot {
  scrollHeight: number
  clientHeight: number
}

/**
 * 「大小变化之前是不是贴着底部」：用上一次的大小和现在的 scrollTop 推算。
 * 只靠滚动事件记标记会过期（初次渲染中途的滚动事件读到的是半截版面，之后回到底部又没有新的滚动事件），
 * 而大小变化本身不会改 scrollTop，所以「上一次的大小 + 现在的 scrollTop」就是变化发生之前的位置。
 * 浏览器的滚动锚定若已经把 scrollTop 跟着往下挪了，算出来是负的离底，同样算贴底。
 */
export function wasPinnedBeforeResize(prev: SizeSnapshot | null, scrollTop: number, tolerance: number = PIN_TOLERANCE_PX): boolean {
  if (!prev) return false
  return isPinnedAtBottom({ scrollHeight: prev.scrollHeight, scrollTop, clientHeight: prev.clientHeight }, tolerance)
}

/**
 * 大小变化之后应该把 scrollTop 设成多少；null = 不动（之前没贴底，或现在已经在底部）。
 * 返回 scrollHeight 而不是 scrollHeight - clientHeight：浏览器会按最大值截断，与列表里其余的「贴底」写法一致。
 */
export function repinTarget(m: ScrollMetrics, wasPinned: boolean): number | null {
  if (!wasPinned) return null
  if (bottomGap(m) <= REPIN_MIN_GAP_PX) return null
  return m.scrollHeight
}
