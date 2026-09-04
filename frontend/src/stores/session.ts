import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { FeedbackSession, SessionImage } from '../api/types'
import { sessionsApi } from '../api/client'
import { useNotifyStore } from './notify'
import { useAuthStore } from './auth'
import { useTaskStore } from './task'
import { useSettingsStore } from './settings'

export const useSessionStore = defineStore('session', () => {
  const currentSession = ref<FeedbackSession | null>(null)
  const selectedSession = ref<FeedbackSession | null>(null)
  const sessions = ref<FeedbackSession[]>([])
  const queuedFeedbacks = ref<any[]>([])
  const loading = ref(false)
  const submitting = ref(false)
  const sseConnected = ref(false)
  const isReconnecting = ref(false)
  const lastPhaseEvent = ref<{ workflow_id: string; phase: string } | null>(null)
  let eventSource: EventSource | null = null
  let watchdogTimer: number | null = null
  let reconnectTimer: number | null = null
  let reconnectAttempt = 0
  let isManualDisconnect = false

  const notifyStore = useNotifyStore()

  const workflowSessionsCache = ref<Record<string, FeedbackSession[]>>({})
  const currentWorkflowSessions = ref<FeedbackSession[]>([])
  const loadingWorkflowSessions = ref(false)

  async function loadWorkflowSessions(workflowId: string, force = false) {
    if (!workflowId) return
    // 1. 若本地已有缓存且非强制刷新，先赋给展示层（0ms 秒开）
    if (!force && workflowSessionsCache.value[workflowId]?.length) {
      currentWorkflowSessions.value = workflowSessionsCache.value[workflowId]
    }

    try {
      loadingWorkflowSessions.value = true
      const res = await sessionsApi.getWorkflowSessions(workflowId)
      if (res && Array.isArray(res.sessions)) {
        workflowSessionsCache.value[workflowId] = res.sessions
        // 如果当前仍然停留在这个工作流，更新当前展示列表
        const activeWId = selectedSession.value?.workflow_id || selectedSession.value?.session_id
        if (activeWId === workflowId) {
          currentWorkflowSessions.value = res.sessions
        }
      }
    } catch (err) {
      console.error(`Failed to load workflow sessions for ${workflowId}`, err)
    } finally {
      loadingWorkflowSessions.value = false
    }
  }

  function selectSession(session: FeedbackSession | null) {
    selectedSession.value = session
    if (session) {
      const wId = session.workflow_id || session.session_id
      if (wId) {
        loadWorkflowSessions(wId)
      }
    } else {
      currentWorkflowSessions.value = []
    }
  }

  async function fetchCurrentSession() {
    try {
      loading.value = true
      const res = await sessionsApi.getCurrent()
      if (res.has_session && res.session) {
        if (!currentSession.value || currentSession.value.session_id !== res.session.session_id) {
          notifyStore.notify('新反馈请求', res.session.title || '收到新的 AI 反馈请求，请及时查看。')
        }
        currentSession.value = res.session
        // 当收到新的 pending 会话时，仅当未选定任何会话（初始空白态），或者当前选中的正是该同会话/同工作流时才同步更新
        if (!selectedSession.value) {
          selectedSession.value = res.session
          const wId = res.session.workflow_id || res.session.session_id
          if (wId) {
            loadWorkflowSessions(wId)
          }
        } else if (selectedSession.value.session_id === res.session.session_id ||
                   (res.session.workflow_id && selectedSession.value.workflow_id === res.session.workflow_id)) {
          // 同一会话或同一工作流下的新轮次，保持同步更新
          selectedSession.value = res.session
          const wId = res.session.workflow_id || res.session.session_id
          if (wId) {
            loadWorkflowSessions(wId)
          }
        }
        // 若用户当前正停留在其他不同工作流，严格保持 selectedSession 不动，绝不主动强切窗口！
      } else {
        const prevPendingId = currentSession.value?.session_id
        currentSession.value = null
        if (selectedSession.value && selectedSession.value.session_id === prevPendingId) {
          // If the selected session was just completed, refresh its status
          const updated = sessions.value.find(s => s.session_id === prevPendingId)
          if (updated) selectedSession.value = updated
        }
      }
    } catch (err) {
      console.error('Failed to fetch current session', err)
    } finally {
      loading.value = false
    }
  }

  let fetchSessionsSeq = 0
  let refreshTimer: any = null

  function applySessionPayload(data: any) {
    if (!data || typeof data !== 'object') return
    const sess = (data.session || data) as FeedbackSession
    if (!sess || !sess.session_id) return

    const idx = sessions.value.findIndex(s => s.session_id === sess.session_id)
    if (idx !== -1) {
      sessions.value[idx] = { ...sessions.value[idx], ...sess }
    } else {
      sessions.value.unshift(sess)
    }

    if (selectedSession.value?.session_id === sess.session_id) {
      selectedSession.value = { ...selectedSession.value, ...sess }
    }

    const wId = sess.workflow_id || sess.session_id
    if (wId && workflowSessionsCache.value[wId]) {
      const list = workflowSessionsCache.value[wId]
      const itemIdx = list.findIndex(s => s.session_id === sess.session_id)
      if (itemIdx !== -1) {
        list[itemIdx] = { ...list[itemIdx], ...sess }
      } else {
        list.push(sess)
      }
      if (selectedSession.value?.workflow_id === wId || selectedSession.value?.session_id === wId) {
        currentWorkflowSessions.value = [...list]
      }
    }

    if (currentSession.value?.session_id === sess.session_id) {
      if (sess.status === 'pending') {
        currentSession.value = { ...currentSession.value, ...sess }
      } else {
        currentSession.value = null
      }
    } else if (sess.status === 'pending' && !currentSession.value) {
      currentSession.value = sess
    }
  }

  function debouncedRefresh() {
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(() => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
      const activeWId = selectedSession.value?.workflow_id || selectedSession.value?.session_id
      if (activeWId) {
        loadWorkflowSessions(activeWId, true)
      }
    }, 120)
  }

  async function fetchSessions(status?: string) {
    const reqSeq = ++fetchSessionsSeq
    try {
      const res = await sessionsApi.list({ status, limit: 200, brief: true })
      if (reqSeq !== fetchSessionsSeq) {
        return
      }
      sessions.value = res.sessions
      if (!selectedSession.value) {
        if (currentSession.value) {
          selectSession(currentSession.value)
        } else if (res.sessions.length > 0) {
          selectSession(res.sessions[0])
        }
      } else {
        // 如果已经选中了某个会话，在 sessions 列表中找到最新对象并同步状态（例如已完成、已更新）
        const updated = res.sessions.find(s => s.session_id === selectedSession.value?.session_id)
        if (updated) {
          selectedSession.value = { ...selectedSession.value, ...updated }
        }
      }
    } catch (err) {
      console.error('Failed to list sessions', err)
    }
  }

  async function submitFeedback(sessionId: string, text: string, userMessages: string[] = [], images: SessionImage[] = []) {
    try {
      submitting.value = true
      const res = await sessionsApi.submit(sessionId, {
        response_text: text,
        user_messages: userMessages,
        images: images
      })
      if (res.session) {
        applySessionPayload(res.session)
        const wId = res.session.workflow_id || res.session.session_id
        if (wId) {
          await loadWorkflowSessions(wId, true)
        }
      }
      await fetchSessions()
      return res.session
    } finally {
      submitting.value = false
    }
  }

  async function cancelSession(sessionId: string) {
    const res = await sessionsApi.cancel(sessionId)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function keepalive(sessionId: string, extendSeconds = 300) {
    const res = await sessionsApi.keepalive(sessionId, extendSeconds)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    return res.session
  }

  async function archiveSession(idOrWorkflow: string) {
    const res = await sessionsApi.archive(idOrWorkflow)
    const targetWId = res.session.workflow_id || res.session.session_id
    if (currentSession.value && (currentSession.value.session_id === res.session.session_id || currentSession.value.workflow_id === targetWId)) {
      currentSession.value = null
    }
    if (selectedSession.value && (selectedSession.value.session_id === res.session.session_id || selectedSession.value.workflow_id === targetWId)) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function renameSession(sessionId: string, title: string) {
    const res = await sessionsApi.rename(sessionId, title)
    const targetWId = res.session?.workflow_id || sessionId
    const targetSessId = res.session?.session_id || sessionId

    // 1. 同步当前活跃会话与主选定会话
    if (currentSession.value) {
      if (currentSession.value.session_id === targetSessId || (currentSession.value.workflow_id && currentSession.value.workflow_id === targetWId)) {
        currentSession.value = { ...currentSession.value, title }
      }
    }
    if (selectedSession.value) {
      if (selectedSession.value.session_id === targetSessId || (selectedSession.value.workflow_id && selectedSession.value.workflow_id === targetWId)) {
        selectedSession.value = { ...selectedSession.value, title }
      }
    }

    // 2. 深度清除指定工作流的会话缓存，并强制重载多轮对话流
    if (targetWId && workflowSessionsCache.value[targetWId]) {
      delete workflowSessionsCache.value[targetWId]
    }
    if (targetWId) {
      await loadWorkflowSessions(targetWId, true)
    }

    // 3. 全量刷新 sessions 列表元数据
    await fetchSessions()
    return res.session
  }

  async function updatePromptWait(sessionId: string, waitMinutes: number) {
    const res = await sessionsApi.updatePromptWait(sessionId, waitMinutes)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function updateMaxChecks(sessionId: string, maxChecks: number) {
    const res = await sessionsApi.updateMaxChecks(sessionId, maxChecks)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function updateWaitCountdown(sessionId: string, countdownMinutes: number) {
    const res = await sessionsApi.updateWaitCountdown(sessionId, countdownMinutes)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function updateUserPresence(sessionId: string, userPresence: 'online' | 'away' | 'autopilot') {
    const res = await sessionsApi.updateUserPresence(sessionId, userPresence)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function unarchiveSession(idOrWorkflow: string) {
    const res = await sessionsApi.unarchive(idOrWorkflow)
    const targetWId = res.session.workflow_id || res.session.session_id
    if (selectedSession.value && (selectedSession.value.session_id === res.session.session_id || selectedSession.value.workflow_id === targetWId)) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  async function fetchQueuedFeedbacks(workflowId?: string) {
    try {
      const wId = workflowId || selectedSession.value?.workflow_id || currentSession.value?.workflow_id || ''
      if (!wId) {
        queuedFeedbacks.value = []
        return
      }
      const res = await sessionsApi.listQueued(wId)
      queuedFeedbacks.value = res.queued_feedbacks || []
    } catch (err) {
      console.error('Failed to list queued feedbacks', err)
    }
  }

  async function revokeFeedback(sessionId: string) {
    try {
      submitting.value = true
      const res = await sessionsApi.revoke(sessionId)
      if (currentSession.value?.session_id === sessionId) {
        currentSession.value = res.session
      }
      if (selectedSession.value?.session_id === sessionId) {
        selectedSession.value = res.session
      }
      const wId = res.session?.workflow_id || res.session?.session_id
      if (wId) {
        await loadWorkflowSessions(wId, true)
      }
      await fetchSessions()
      return res.revoked
    } finally {
      submitting.value = false
    }
  }

  async function appendWorkflowFeedback(workflowId: string, text: string, userMessages: string[] = [], images: SessionImage[] = [], hostName = '', projectDir = '') {
    try {
      submitting.value = true
      const res = await sessionsApi.appendWorkflowFeedback(workflowId, {
        response_text: text,
        user_messages: userMessages,
        images: images,
        host_name: hostName,
        project_directory: projectDir
      })
      if (res.session) {
        applySessionPayload(res.session)
      }
      await loadWorkflowSessions(workflowId, true)
      await fetchSessions()
      await fetchQueuedFeedbacks(workflowId)
      return res
    } finally {
      submitting.value = false
    }
  }

  async function revokeQueuedFeedback(queuedId: number, workflowId?: string) {
    try {
      submitting.value = true
      const res = await sessionsApi.revokeQueued(queuedId)
      await fetchQueuedFeedbacks(workflowId)
      return res.revoked
    } finally {
      submitting.value = false
    }
  }

  function parseSSEEventData(e: MessageEvent): any {
    if (!e.data) return null
    try {
      return JSON.parse(e.data)
    } catch (_) {
      return null
    }
  }

  function kickWatchdog() {
    if (watchdogTimer) {
      window.clearTimeout(watchdogTimer)
    }
    // 放宽至 45s（后端每 15s 发一次 ping，容忍 3 次心跳间隔），杜绝误判假死
    watchdogTimer = window.setTimeout(() => {
      console.warn('[SSE] Watchdog timeout: no events/pings for 45s. Reconnecting...')
      forceReconnect()
    }, 45000)
  }

  function scheduleReconnect() {
    if (isManualDisconnect) return
    if (reconnectTimer) return
    isReconnecting.value = true

    // 指数退避：1s -> 2s -> 4s -> 8s -> 10s (上限 10s)
    const delay = Math.min(10000, Math.pow(2, reconnectAttempt) * 1000)
    reconnectAttempt++
    console.log(`[SSE] Reconnecting in ${delay}ms (attempt #${reconnectAttempt})...`)

    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      connectSSE()
    }, delay)
  }

  function forceReconnect() {
    if (eventSource) {
      try {
        eventSource.close()
      } catch (_) {}
      eventSource = null
    }
    sseConnected.value = false
    scheduleReconnect()
  }

  function handleVisibilityChange() {
    if (document.visibilityState === 'visible') {
      if (!sseConnected.value && !isManualDisconnect) {
        console.log('[SSE] Tab became visible and SSE is disconnected. Triggering immediate reconnect...')
        if (reconnectTimer) {
          window.clearTimeout(reconnectTimer)
          reconnectTimer = null
        }
        reconnectAttempt = 0
        connectSSE()
      } else if (sseConnected.value) {
        // 唤醒自愈：切回前台瞬间主动同步最新状态，消除移动端休眠恢复后的数据滞后
        debouncedRefresh()
      }
    }
  }

  function handleWindowFocus() {
    if (sseConnected.value) {
      debouncedRefresh()
    }
  }

  if (typeof window !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('focus', handleWindowFocus)
  }

  function connectSSE() {
    if (eventSource) return
    isManualDisconnect = false

    const authStore = useAuthStore()
    const sseUrl = authStore.token 
      ? `/api/v1/events?token=${encodeURIComponent(authStore.token)}`
      : '/api/v1/events'

    eventSource = new EventSource(sseUrl)
    kickWatchdog()

    eventSource.onopen = () => {
      console.log('[SSE] Connection established successfully.')
      sseConnected.value = true
      isReconnecting.value = false
      reconnectAttempt = 0
      kickWatchdog()
      // 连接恢复后主动拉取最新数据，确保断线期间事件不丢失
      fetchCurrentSession()
      fetchSessions()
    }

    eventSource.onerror = () => {
      console.warn('[SSE] Connection error.')
      sseConnected.value = false
      if (eventSource) {
        try {
          eventSource.close()
        } catch (_) {}
        eventSource = null
      }
      scheduleReconnect()
    }

    // 监听原生 ping 心跳与通用 message 刷新看门狗
    eventSource.onmessage = () => {
      kickWatchdog()
    }

    eventSource.addEventListener('ping', () => {
      kickWatchdog()
    })

    eventSource.addEventListener('connected', () => {
      kickWatchdog()
    })
    eventSource.addEventListener('session_update', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_completed', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_cancelled', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_archived', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_unarchived', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('session_keepalive', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload) applySessionPayload(payload)
      debouncedRefresh()
    })
    eventSource.addEventListener('queued_feedback_updated', () => {
      kickWatchdog()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('queued_feedback_revoked', () => {
      kickWatchdog()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('task_update', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      const taskStore = useTaskStore()
      taskStore.debouncedRefresh(payload?.task_id)
    })
    eventSource.addEventListener('task_feedback', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      const taskStore = useTaskStore()
      taskStore.debouncedRefresh(payload?.task_id)
    })
    eventSource.addEventListener('task_ack', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      const taskStore = useTaskStore()
      taskStore.debouncedRefresh(payload?.task_id)
    })
    eventSource.addEventListener('settings_updated', () => {
      kickWatchdog()
      const settingsStore = useSettingsStore()
      settingsStore.fetchRemoteSettings()
    })
    eventSource.addEventListener('credentials_updated', () => {
      kickWatchdog()
      debouncedRefresh()
    })
    eventSource.addEventListener('phase_changed', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload?.workflow_id) {
        lastPhaseEvent.value = { workflow_id: payload.workflow_id, phase: payload.phase || '' }
      }
    })
  }

  function manualReconnect() {
    isManualDisconnect = false
    if (reconnectTimer) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    reconnectAttempt = 0
    forceReconnect()
  }

  function disconnectSSE() {
    isManualDisconnect = true
    if (watchdogTimer) {
      window.clearTimeout(watchdogTimer)
      watchdogTimer = null
    }
    if (reconnectTimer) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (eventSource) {
      eventSource.close()
      eventSource = null
      sseConnected.value = false
    }
    isReconnecting.value = false
  }

  let fallbackSyncTimer: number | null = null

  function startFallbackSync() {
    if (fallbackSyncTimer) return
    // 每 20 秒执行一次极轻量静默状态校验（仅在页面处于前台时运行）
    fallbackSyncTimer = window.setInterval(() => {
      if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
        fetchCurrentSession()
      }
    }, 20000)
  }

  function stopFallbackSync() {
    if (fallbackSyncTimer) {
      window.clearInterval(fallbackSyncTimer)
      fallbackSyncTimer = null
    }
  }

  if (typeof window !== 'undefined') {
    startFallbackSync()
  }

  return {
    currentSession,
    selectedSession,
    sessions,
    queuedFeedbacks,
    loading,
    submitting,
    sseConnected,
    isReconnecting,
    lastPhaseEvent,
    currentWorkflowSessions,
    loadingWorkflowSessions,
    loadWorkflowSessions,
    selectSession,
    fetchCurrentSession,
    fetchSessions,
    fetchQueuedFeedbacks,
    submitFeedback,
    revokeFeedback,
    appendWorkflowFeedback,
    revokeQueuedFeedback,
    cancelSession,
    keepalive,
    archiveSession,
    unarchiveSession,
    renameSession,
    updatePromptWait,
    updateMaxChecks,
    updateWaitCountdown,
    updateUserPresence,
    connectSSE,
    disconnectSSE,
    manualReconnect,
    startFallbackSync,
    stopFallbackSync
  }
})
