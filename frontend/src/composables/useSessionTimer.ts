import { ref, computed } from 'vue'

export interface SessionTimerRaw {
  status?: string
  is_mcp_active?: boolean
  mcp_active_at?: string
  wait_countdown_minutes?: number
  last_keepalive_at?: string
  created_at?: string
  updated_at?: string
}

export interface SessionTimerResult {
  text: string
  prefix: string
  isCountdown: boolean
}

export function formatTimerDuration(seconds: number): string {
  if (seconds < 0) seconds = 0
  const ONE_DAY = 86400
  const ONE_HOUR = 3600

  if (seconds >= ONE_DAY) {
    const days = Math.floor(seconds / ONE_DAY)
    const remainSec = seconds % ONE_DAY
    const hours = Math.floor(remainSec / ONE_HOUR)
    const mins = Math.floor((remainSec % ONE_HOUR) / 60)
    return `${days}d·${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}`
  }

  if (seconds >= ONE_HOUR) {
    const hours = Math.floor(seconds / ONE_HOUR)
    const mins = Math.floor((seconds % ONE_HOUR) / 60)
    const secs = seconds % 60
    return `${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }

  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

/**
 * 纯函数计算给定会话与时间戳下的展示信息（供 SidebarItemCard 等高频列表渲染使用）
 */
export function computeSessionTimer(
  session: SessionTimerRaw | null | undefined,
  now: number,
  fallbackTargetMinutes = 2
): SessionTimerResult {
  if (!session) {
    return { text: '00:00', prefix: '执行', isCountdown: false }
  }

  const isPending = session.status === 'pending'
  const isMcpActive = !!session.is_mcp_active

  // 1. 若会话处于 pending 交互中
  if (isPending) {
    if (isMcpActive) {
      // 1.1 MCP 客户端长轮询活跃挂起
      const mcpStart = session.mcp_active_at ? new Date(session.mcp_active_at).getTime() : now
      const elapsed = Math.max(0, Math.floor((now - mcpStart) / 1000))
      const targetMin = session.wait_countdown_minutes !== undefined && session.wait_countdown_minutes !== null
        ? session.wait_countdown_minutes
        : fallbackTargetMinutes
      const targetSec = targetMin * 60

      if (targetSec > 0 && elapsed < targetSec) {
        const remain = targetSec - elapsed
        return {
          text: formatTimerDuration(remain),
          prefix: '剩余',
          isCountdown: true
        }
      }
      return {
        text: formatTimerDuration(elapsed),
        prefix: '剩余',
        isCountdown: false
      }
    }

    // 1.2 挂起超时或已归零，AI sleep 盲等人类输入
    if (session.last_keepalive_at) {
      const keepaliveStart = new Date(session.last_keepalive_at).getTime()
      const keepaliveElapsed = Math.max(0, Math.floor((now - keepaliveStart) / 1000))
      return {
        text: formatTimerDuration(keepaliveElapsed),
        prefix: '等待',
        isCountdown: false
      }
    }

    return {
      text: '00:00',
      prefix: '等待',
      isCountdown: false
    }
  }

  // 2. 无交互/已完成/AI 正在自主执行任务
  if (session.status === 'completed') {
    const execStartTime = session.updated_at
      ? new Date(session.updated_at).getTime()
      : (session.created_at ? new Date(session.created_at).getTime() : now)
    const execElapsed = Math.max(0, Math.floor((now - execStartTime) / 1000))
    return {
      text: formatTimerDuration(execElapsed),
      prefix: '执行',
      isCountdown: false
    }
  }

  return { text: '', prefix: '', isCountdown: false }
}

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
