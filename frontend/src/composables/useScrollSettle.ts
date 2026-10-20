// 滚动位置校准驱动（#158，预览视图用）：原理与纯函数见 lib/scrollSettle.ts。两个用法，同一时刻只有一个在跑（新的会取消旧的）。
//
// ① settle(容器, 目标条目)：平滑滚动落地后的校准。先照常发起平滑滚动（scrollIntoView / scrollTo 的 smooth），紧接着调用：
//   - 每帧看容器的 scrollTop；静止满 SETTLE_QUIET_MS 才算平滑滚动停了；
//   - 停了就量目标条目的真实位置，偏出容差就瞬时把容器放到该去的位置（最多 SETTLE_MAX_FIXES 次，补完再等静止、再量）；
//   - 用户自己滚动 / 触摸 / 按键，或条目被摘掉、组件卸载、超过 SETTLE_TIMEOUT_MS，立刻放弃。
//   - 平滑滚动自己落定（含补偿完成）时调用 onSettled：落点提示（高亮外框 / 闪一下）要在「到了」的时候亮——
//     远距离的平滑滚动要两三秒，起跑时亮的外框到落地时早灭了。被放弃（用户接手、条目没了、超时）的不调。
//   （scrollToSession 本来就会连着调两次，后一次取消前一次。）
// ② hold(容器, 锚点条目, 预期位置)：钉锚点。向上载入更早的轮次之前先用 offsetInContainer 记下锚点在视口里的位置，
//   DOM 更新之后调用：立刻把它放回原位，之后每帧复量、偏了再放回，直到连续 HOLD_QUIET_MS 没有偏差（或超过 HOLD_TIMEOUT_MS）才收手。
//   不能和用户抢滚动：滚轮 / 触摸 / 按键 / 指针按下立刻放手；还要认「惯性滑动」——手机上手指已经抬起、页面还在滑，没有任何事件，
//   所以每帧核对 scrollTop 是不是还停在我们上次放的位置：被动过且版面高度没变，只能是用户（惯性），立刻放手，绝不把滑动拉回来；
//   被动过且版面高度也变了，是浏览器的滚动锚定（它只在版面变化时调 scrollTop，而且常常只补一部分增高）——不是用户，认下它放的位置继续钉。
//   （以前把两种情况都当成用户接手而放手，恰好在锚定只补一半的那几批留下了两千像素的偏差，见 lib/scrollSettle.ts 开头。）
import { onBeforeUnmount } from 'vue'
import {
  HOLD_QUIET_MS,
  HOLD_TIMEOUT_MS,
  SETTLE_MAX_FIXES,
  SETTLE_PADDING_PX,
  SETTLE_QUIET_MS,
  SETTLE_TIMEOUT_MS,
  holdStep,
  settleTarget,
  type HoldState,
} from '../lib/scrollSettle'

/** 条目顶边相对容器顶边（视口里）的位置，px。 */
export function offsetInContainer(container: HTMLElement, el: HTMLElement): number {
  return el.getBoundingClientRect().top - container.getBoundingClientRect().top
}

export interface SettleOptions {
  padding?: number
  maxFixes?: number
  quietMs?: number
  timeoutMs?: number
  /** 平滑滚动落定（位置已在容差内，或补偿次数用完）时调用一次；被放弃的不调。 */
  onSettled?: () => void
}

export interface HoldOptions {
  quietMs?: number
  timeoutMs?: number
}

export function useScrollSettle() {
  let stopCurrent: (() => void) | null = null

  function cancel() {
    stopCurrent?.()
    stopCurrent = null
  }

  function settle(container: HTMLElement | null | undefined, el: HTMLElement | null | undefined, opts: SettleOptions = {}) {
    cancel()
    if (!container || !el) return
    const padding = opts.padding ?? SETTLE_PADDING_PX
    const maxFixes = opts.maxFixes ?? SETTLE_MAX_FIXES
    const quietMs = opts.quietMs ?? SETTLE_QUIET_MS
    const timeoutMs = opts.timeoutMs ?? SETTLE_TIMEOUT_MS

    const startedAt = performance.now()
    let lastTop = container.scrollTop
    let stableSince = startedAt
    let fixes = 0
    let raf = 0
    let done = false

    const userEvents: Array<[EventTarget, string]> = [
      [container, 'wheel'],
      [container, 'touchstart'],
      [container, 'pointerdown'],
      [document, 'keydown'],
    ]
    const stop = () => {
      if (done) return
      done = true
      cancelAnimationFrame(raf)
      for (const [target, type] of userEvents) target.removeEventListener(type, stop)
      if (stopCurrent === stop) stopCurrent = null
    }
    for (const [target, type] of userEvents) target.addEventListener(type, stop, { passive: true })

    const tick = () => {
      if (done) return
      if (!el.isConnected || !container.isConnected) return stop()
      const now = performance.now()
      if (now - startedAt > timeoutMs) return stop()
      if (container.scrollTop !== lastTop) {
        lastTop = container.scrollTop
        stableSince = now
      }
      if (now - stableSince >= quietMs) {
        const itemTop = el.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop
        const target = settleTarget(
          { itemTop, scrollTop: container.scrollTop, scrollHeight: container.scrollHeight, clientHeight: container.clientHeight },
          padding,
        )
        if (target === null || fixes >= maxFixes) {
          stop()
          opts.onSettled?.()
          return
        }
        fixes++
        container.scrollTop = target
        lastTop = container.scrollTop
        stableSince = now
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    stopCurrent = stop
  }

  function hold(
    container: HTMLElement | null | undefined,
    el: HTMLElement | null | undefined,
    wantedOffset: number,
    opts: HoldOptions = {},
  ) {
    cancel()
    if (!container || !el) return
    const quietMs = opts.quietMs ?? HOLD_QUIET_MS
    const timeoutMs = opts.timeoutMs ?? HOLD_TIMEOUT_MS

    const startedAt = performance.now()
    // 决策都在纯函数 holdStep 里（lib/scrollSettle.ts）：我们上一次放的位置 / 当时的版面高度 / 安静期起点，每帧带着走一步
    let state: HoldState = { ours: container.scrollTop, lastHeight: container.scrollHeight, quietSince: startedAt }
    let raf = 0
    let done = false

    const userEvents: Array<[EventTarget, string]> = [
      [container, 'wheel'],
      [container, 'touchstart'],
      [container, 'pointerdown'],
      [document, 'keydown'],
    ]
    const stop = () => {
      if (done) return
      done = true
      cancelAnimationFrame(raf)
      for (const [target, type] of userEvents) target.removeEventListener(type, stop)
      if (stopCurrent === stop) stopCurrent = null
    }
    for (const [target, type] of userEvents) target.addEventListener(type, stop, { passive: true })

    const tick = () => {
      if (done) return
      if (!el.isConnected || !container.isConnected) return stop()
      const step = holdStep(state, {
        now: performance.now(),
        startedAt,
        scrollTop: container.scrollTop,
        scrollHeight: container.scrollHeight,
        offsetNow: offsetInContainer(container, el),
        offsetWanted: wantedOffset,
        quietMs,
        timeoutMs,
      })
      state = step.state
      if (step.action.kind === 'stop') return stop()
      if (step.action.kind === 'fix') {
        container.scrollTop += step.action.delta
        // 读回来的才是实际放到的位置（scrollTop 会按设备像素取整）
        state = { ...state, ours: container.scrollTop }
      }
      raf = requestAnimationFrame(tick)
    }
    stopCurrent = stop
    // 调用方在 nextTick 之后调用，DOM 已更新：第一次校正不等下一帧，与原来的「载入后立刻补偿」同一时刻
    tick()
  }

  onBeforeUnmount(cancel)
  return { settle, hold, cancel }
}
