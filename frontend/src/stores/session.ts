import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { FeedbackSession, SessionImage } from '../api/types'
import { sessionsApi } from '../api/client'
import { useNotifyStore } from './notify'
import { useAuthStore } from './auth'

export const useSessionStore = defineStore('session', () => {
  const currentSession = ref<FeedbackSession | null>(null)
  const selectedSession = ref<FeedbackSession | null>(null)
  const sessions = ref<FeedbackSession[]>([])
  const queuedFeedbacks = ref<any[]>([])
  const loading = ref(false)
  const submitting = ref(false)
  const sseConnected = ref(false)
  let eventSource: EventSource | null = null

  const notifyStore = useNotifyStore()

  function selectSession(session: FeedbackSession | null) {
    selectedSession.value = session
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
        // 当收到新的 pending 会话时，如果未选定会话，或者当前选中的正是该会话/该工作流，自动更新选中的会话为最新的活跃会话
        if (!selectedSession.value || 
            selectedSession.value.session_id === res.session.session_id ||
            (res.session.workflow_id && selectedSession.value.workflow_id === res.session.workflow_id)) {
          selectedSession.value = res.session
        }
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

  async function fetchSessions(status?: string) {
    try {
      const res = await sessionsApi.list({ status, limit: 200 })
      sessions.value = res.sessions
      if (!selectedSession.value) {
        if (currentSession.value) {
          selectedSession.value = currentSession.value
        } else if (res.sessions.length > 0) {
          selectedSession.value = res.sessions[0]
        }
      } else {
        // 如果已经选中了某个会话，在 sessions 列表中找到最新对象并同步状态（例如已完成、已更新）
        const updated = res.sessions.find(s => s.session_id === selectedSession.value?.session_id)
        if (updated) {
          selectedSession.value = updated
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
      if (currentSession.value?.session_id === sessionId) {
        currentSession.value = res.session
      }
      if (selectedSession.value?.session_id === sessionId) {
        selectedSession.value = res.session
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
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
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

  function connectSSE() {
    if (eventSource) return

    const authStore = useAuthStore()
    const sseUrl = authStore.token 
      ? `/api/v1/events?token=${encodeURIComponent(authStore.token)}`
      : '/api/v1/events'

    eventSource = new EventSource(sseUrl)
    eventSource.onopen = () => {
      sseConnected.value = true
    }
    eventSource.onerror = () => {
      sseConnected.value = false
    }
    eventSource.addEventListener('session_update', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_updated', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_completed', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_cancelled', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_archived', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_unarchived', () => {
      fetchCurrentSession()
      fetchSessions()
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('session_keepalive', () => {
      fetchCurrentSession()
      fetchSessions()
    })
    eventSource.addEventListener('queued_feedback_updated', () => {
      fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('queued_feedback_revoked', () => {
      fetchQueuedFeedbacks()
    })
  }

  function disconnectSSE() {
    if (eventSource) {
      eventSource.close()
      eventSource = null
      sseConnected.value = false
    }
  }

  return {
    currentSession,
    selectedSession,
    sessions,
    queuedFeedbacks,
    loading,
    submitting,
    sseConnected,
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
    disconnectSSE
  }
})
