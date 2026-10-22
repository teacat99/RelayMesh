import { ref, computed, onScopeDispose, type Ref } from 'vue'
import { computeSessionTimer, type SessionTimerRaw, type SessionTimerResult } from '../lib/sessionTimer'

// 计时的纯逻辑在 lib/sessionTimer.ts（可单元测试）；这里保留原来的导出，现有的引用不用改。
export { computeSessionTimer, formatTimerDuration } from '../lib/sessionTimer'
export type { SessionTimerRaw, SessionTimerResult } from '../lib/sessionTimer'

/**
 * 响应式全局秒级时钟单例驱动
 */
const sharedNow = ref(Date.now())
let sharedTimerInterval: number | null = null
let sharedRefSubscribers = 0

function startSharedClock() {
  if (typeof window === 'undefined') return
  if (!sharedTimerInterval) {
    sharedTimerInterval = window.setInterval(() => {
      sharedNow.value = Date.now()
    }, 1000)
  }
}

function stopSharedClock() {
  if (sharedTimerInterval && sharedRefSubscribers <= 0) {
    window.clearInterval(sharedTimerInterval)
    sharedTimerInterval = null
  }
}

/**
 * 只要「现在几点」、不要计时结果的组件用这个（侧栏会话卡的状态推导）：返回共享的秒级时钟，所在的作用域结束（组件卸载）时自动退订。
 *
 * 不要把这个值当 prop 从父组件往一整批子组件里灌：prop 一变，子组件不管模板里用没用到它都会被整个重画一遍——
 * 侧栏几十张卡每秒各重画一次就是这么来的（#215）。谁要用就自己在 computed 里读，并让 computed 的结果经 stableComputed
 * （lib/stable.ts）过一遍，显示没变的卡就不会被唤醒；走字的那几个字放进自己的小组件（SidebarItemTimer）里单独重画。
 */
export function useSharedNow(): Readonly<Ref<number>> {
  startSharedClock()
  sharedRefSubscribers++
  onScopeDispose(() => {
    sharedRefSubscribers--
    if (sharedRefSubscribers <= 0) stopSharedClock()
  })
  return sharedNow
}

export function useSessionTimer(
  sessionGetter: () => SessionTimerRaw | null | undefined,
  fallbackTargetMinutesGetter?: () => number
) {
  startSharedClock()
  sharedRefSubscribers++

  const timerDisplay = computed<SessionTimerResult>(() => {
    const sess = sessionGetter()
    const targetMin = fallbackTargetMinutesGetter ? fallbackTargetMinutesGetter() : 2
    return computeSessionTimer(sess, sharedNow.value, targetMin)
  })

  function cleanup() {
    sharedRefSubscribers--
    if (sharedRefSubscribers <= 0) {
      stopSharedClock()
    }
  }

  return {
    now: sharedNow,
    timerDisplay,
    cleanup
  }
}
