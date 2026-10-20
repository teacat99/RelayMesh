import { ref, computed } from 'vue'
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
