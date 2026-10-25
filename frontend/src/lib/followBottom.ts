// 「跟随新内容贴底」与「用户让位」的纯逻辑（#186 C1，预览视图用；与 lib/stickBottom.ts 的「大小变化后重新贴底」是两件事）。
//
// 背景（生产实测，实验室同样复现）：打开一个长会话后的头 2 秒里，列表代码执行了 31 次「滚到底」，第一次是 62k px 的跳转，
// 其余几乎都是原地「写了等于没写」——先 scrollIntoView 往上挪一个底部留白、紧跟着 scrollTop = scrollHeight 再挪回去，
// 每次两次强制版面、位置没有净变化。它们的触发点各自靠 scroll 事件更新的 isScrolledUp 判断「用户是不是向上翻过」；
// 而主线程被占着的时候，滚轮事件晚到、scroll 事件更晚到，标志还没更新，贴底先发生：用户的第一次滚动被晾 1～2 秒，再被贴底跳转带走。
// 另外「滚到某一轮」（侧栏点待答复 / 搜索命中 / 轨上的刻度）起跑的头几帧，还会被首次载入留下的贴底复核抢写 scrollTop，落点漂移。
//
// 做法（组件负责执行，这里只放判定）：
//   · 跟随（followBottom）只在「内容变了，而且用户本来就停在底部」时做：至多写一次，已经在底部就一个字都不写，复核也只在离底部还有距离时才写；
//   · 用户意图（向上的滚轮 / 手指下拖 / 上翻类按键）一发生就记时间戳，宽限期内所有程序化贴底让位，不等晚到的 scroll 事件；
//   · 浏览器已经收到、页面还没来得及处理的输入（navigator.scheduling.isInputPending）同样让位一帧——
//     这正是「长任务里贴底抢在滚轮之前」的那一刻；下一帧输入处理完了，上面一条的时间戳就接上了；
//   · 贴底的第一次尝试放进下一帧的动画帧回调里（帧开头先派发排队的滚轮，再跑动画帧回调），而不是在内容变化的当下同步写；
//     主线程刚被长任务占过的「迟到的帧」（isStaleFrame）不做决定、让一帧——它带的是任务开始之前的帧时间，
//     帧开头只派发到那个时间点为止的输入，占着主线程期间用户的滚轮还排在后面，scrollTop 也还没同步合成器已经滚过的位移；
//   · 正在进行的「滚到某一轮」校准（settle）让位，免得两边抢同一个 scrollTop；
//   · 显式的「到底部」（回到底部按钮 / 发送消息 / 打开会话）不受这些限制，并清掉让位状态。

/** 用户表达「向上看」之后，程序化贴底让位多久（ms）：覆盖「滚轮事件已处理、scroll 事件与 isScrolledUp 还没更新」的空档。 */
export const USER_SCROLL_GRACE_MS = 600

/** 瞬时贴底之后再复核几帧：抵抗异步 DOM 展开导致的高度时滞（D-142）；复核时只在离底部还有距离才写。 */
export const FOLLOW_RECHECK_FRAMES = 2

/** 点「回到底部」那种显式的贴底：过这么久复核一次。 */
export const FOLLOW_RECHECK_DELAY_MS = 250

/** 用户最近一次表达「向上看」是在 lastIntentAt（null = 没有），现在是否还在宽限期内。 */
export function userHolding(lastIntentAt: number | null, now: number, graceMs: number = USER_SCROLL_GRACE_MS): boolean {
  return lastIntentAt !== null && now - lastIntentAt < graceMs
}

export interface FollowGate {
  /** 离底部超过 80 px（scroll 事件更新的 isScrolledUp，会晚到）。 */
  scrolledUp: boolean
  /** 用户刚表达过「向上看」（userHolding）。 */
  holding: boolean
  /** 浏览器里有排着队、页面还没处理的输入。 */
  inputPending: boolean
  /** 「滚到某一轮」的校准正在跑，它自己在管 scrollTop。 */
  settling: boolean
}

/** 一次「跟随贴底」现在能不能动手——所有让位条件的唯一出口。 */
export function mayFollow(g: FollowGate): boolean {
  return !g.scrolledUp && !g.holding && !g.inputPending && !g.settling
}

/**
 * 帧时间落后「现在」超过这么多毫秒，就算迟到的帧。
 * 60 Hz 的一帧 16.7 ms；帧开头派发输入、样式与版面占掉几十毫秒也属正常，再长就是主线程刚被长任务占过
 * （实测：忙了 1.5 s 的页面，恢复后的第一个动画帧回调拿到的时间戳比 performance.now() 落后 1.3 ～ 1.5 s；紧跟着的那一帧只落后 10 ms 上下）。
 */
export const STALE_FRAME_MS = 48

/**
 * 这一帧是不是迟到的帧：主线程被长任务占着时，合成器排队的第一个「开始主线程帧」带的还是任务开始之前的帧时间。
 * 帧开头只派发时间戳不晚于帧时间的输入，占着主线程期间用户的滚轮 / 手指还排在后面；scrollTop 也还没同步合成器已经滚过的位移。
 * 在这样的帧里做「要不要贴底」的决定，会把用户刚做的事盖掉；让一帧，下一帧输入派发完、位置同步完了再决定。
 * frameTime：动画帧回调的时间戳，或 ResizeObserver 回调里的 document.timeline.currentTime（二者实测相等）；
 * 不是有限数字（缺省 / null）一律当作不迟到——不要因为取不到时间戳让贴底永远做不成。
 * 只对「帧内」的回调有意义：微任务 / 定时器里 currentTime 是上一帧的时间，页面空闲时可以任意旧。
 */
export function isStaleFrame(frameTime: number | null | undefined, now: number, thresholdMs: number = STALE_FRAME_MS): boolean {
  if (typeof frameTime !== 'number' || !Number.isFinite(frameTime)) return false
  return now - frameTime > thresholdMs
}

/** 滚轮：deltaY < 0 是向上（看更早的内容）。 */
export function isUpwardWheel(deltaY: number): boolean {
  return deltaY < 0
}

/** 向上翻的按键：方向键上 / PageUp / Home / Shift+空格。 */
export function isUpwardKey(e: { key: string; shiftKey?: boolean }): boolean {
  return e.key === 'ArrowUp' || e.key === 'PageUp' || e.key === 'Home' || (e.key === ' ' && e.shiftKey === true)
}

/** 手指向下拖 = 内容向上走 = 在看更早的内容（不足 minPx 的抖动不算）。 */
export function isUpwardTouchMove(prevY: number, y: number, minPx: number = 2): boolean {
  return y - prevY >= minPx
}

/** 键盘事件落在输入控件里不算滚动意图：在输入框里按方向键 / Home 是移动光标。 */
export function isEditableTarget(t: { tagName?: string; isContentEditable?: boolean } | null | undefined): boolean {
  if (!t) return false
  if (t.isContentEditable) return true
  const tag = (t.tagName || '').toUpperCase()
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

/** navigator.scheduling.isInputPending 的最小形状（只有 Chromium 有；别的浏览器一律当作没有排队的输入）。 */
export interface InputPendingNavigator {
  scheduling?: { isInputPending?: (opts?: { includeContinuous?: boolean }) => boolean }
}

/**
 * 有没有排着队的输入（含滚轮这类连续事件）。
 * 只用在「一次跟随请求的第一次尝试」上：鼠标移动也算连续事件，复核帧上再查会因为鼠标一直在动而永远不贴底。
 */
export function isInputPending(nav: InputPendingNavigator | undefined = typeof navigator === 'undefined' ? undefined : (navigator as InputPendingNavigator)): boolean {
  try {
    return nav?.scheduling?.isInputPending?.({ includeContinuous: true }) === true
  } catch {
    return false
  }
}
