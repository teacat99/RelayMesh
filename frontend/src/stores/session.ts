import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { FeedbackSession, SessionImage } from '../api/types'
import { sessionsApi } from '../api/client'
import { useNotifyStore } from './notify'
import { useAuthStore } from './auth'
import { useTaskStore } from './task'
import { useSettingsStore } from './settings'
import {
  FULL_SYNC_SCOPE,
  classifySessionPayload,
  createAwayTracker,
  createKeyedFlight,
  createSyncScheduler,
  isCacheFresh,
  isSameSessionData,
  isStaleIncoming,
  mergeFetchedList,
  mergeSessionPatch,
  type ResolvedSyncScope,
  type SyncScope,
} from '../utils/sessionSync'

// 会话类 SSE 事件的种类；载荷形态（完整会话 / 仅 session_id）由 classifySessionPayload 判定
type SessionEventKind = 'update' | 'updated' | 'completed' | 'cancelled' | 'keepalive' | 'archived' | 'unarchived'

// 工作流完整列表的兜底 TTL：SSE 健康时缓存由事件补丁维护，只有久未整表同步才回源一次
const WORKFLOW_CACHE_TTL_MS = 5 * 60 * 1000
// 页面离开超过该时长，回到前台才做一次校准；更短的切换完全依赖 SSE 事件，不发任何请求
const AWAY_RECALIBRATE_MS = 30 * 1000

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
  const lastSheetEvent = ref<{ workflow_id: string; timestamp: number } | null>(null)
  let eventSource: EventSource | null = null
  let watchdogTimer: number | null = null
  let reconnectTimer: number | null = null
  let reconnectAttempt = 0
  let isManualDisconnect = false

  const notifyStore = useNotifyStore()

  const workflowSessionsCache = ref<Record<string, FeedbackSession[]>>({})
  const currentWorkflowSessions = ref<FeedbackSession[]>([])
  const loadingWorkflowSessions = ref(false)

  // —— 同步引擎（D-171 / P1）：以下均为非响应式的同步元数据 ——
  // 工作流完整列表最近一次「开始拉取」的时间，用于判断缓存是否仍可信
  const workflowSyncedAt = new Map<string, number>()
  // 某个工作流的整表拉取在途期间，被本地补丁 / 本地操作改动过的会话 id；拉取返回时据此避免旧快照覆盖新补丁
  const workflowTouched = new Map<string, Set<string>>()
  // 同理，针对在途的简略列表（sessions）拉取
  const sessionsListTouched = new Set<Set<string>>()
  // 最近一次 SSE 断线的时间：此前完成的整表缓存不再可信（期间可能漏事件）
  let lastSseGapAt = 0
  let hasConnectedOnce = false
  let activeWorkflowLoads = 0
  const awayTracker = createAwayTracker(AWAY_RECALIBRATE_MS)

  function activeWorkflowId(): string {
    return selectedSession.value?.workflow_id || selectedSession.value?.session_id || ''
  }

  function noteLocalChange(workflowId: string | undefined, sessionId: string) {
    if (workflowId) workflowTouched.get(workflowId)?.add(sessionId)
    for (const touched of sessionsListTouched) touched.add(sessionId)
  }

  // 整表拉取统一走单飞：同一工作流同时最多 1 个在途请求 + 1 个尾随，合并并发触发（INV-R3）
  const workflowFlight = createKeyedFlight(
    async (workflowId: string) => {
      const startedAt = Date.now()
      const touched = new Set<string>()
      workflowTouched.set(workflowId, touched)
      activeWorkflowLoads++
      loadingWorkflowSessions.value = true
      try {
        const res = await sessionsApi.getWorkflowSessions(workflowId)
        if (res && Array.isArray(res.sessions)) {
          const merged = mergeFetchedList(workflowSessionsCache.value[workflowId], res.sessions, touched, { localOnlyPosition: 'end' })
          workflowSessionsCache.value[workflowId] = merged
          workflowSyncedAt.set(workflowId, startedAt)
          // 如果当前仍然停留在这个工作流，更新当前展示列表
          if (activeWorkflowId() === workflowId) {
            currentWorkflowSessions.value = merged
          }
        }
      } finally {
        workflowTouched.delete(workflowId)
        activeWorkflowLoads--
        loadingWorkflowSessions.value = activeWorkflowLoads > 0
      }
    },
    (workflowId, err) => console.error(`Failed to load workflow sessions for ${workflowId}`, err)
  )

  /**
   * 加载某个工作流的完整多轮列表。
   * force=false：有缓存则先秒开展示；缓存仍可信（SSE 期间一直在线且未超过兜底 TTL）就不回源，否则回源刷新；
   * force=true：保证发起一次在本次调用之后才开始的拉取（调用方刚做过变更、要拿最新数据时使用）。
   */
  async function loadWorkflowSessions(workflowId: string, force = false) {
    if (!workflowId) return
    const cached = workflowSessionsCache.value[workflowId]
    if (!force && cached?.length) {
      // 本地已有缓存且非强制刷新，先赋给展示层（0ms 秒开）
      currentWorkflowSessions.value = cached
      if (isCacheFresh(workflowSyncedAt.get(workflowId), lastSseGapAt, Date.now(), WORKFLOW_CACHE_TTL_MS)) return
    }
    await workflowFlight.request(workflowId, force)
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
        const incoming = res.session
        const cur = currentSession.value
        const sameSession = !!cur && cur.session_id === incoming.session_id

        // 响应生成于某个本地补丁之前（较旧的快照）：丢弃，避免把较新的本地状态打回去
        if (sameSession && isStaleIncoming(cur, incoming)) return

        const wId = incoming.workflow_id || incoming.session_id
        const followsSelection = !selectedSession.value ||
          selectedSession.value.session_id === incoming.session_id ||
          (!!incoming.workflow_id && selectedSession.value.workflow_id === incoming.workflow_id)

        // 稳态（兜底轮询的绝大多数情况）：内容完全没变，不替换任何对象（避免触发下游监听与重渲染），
        // 只让缓存按新鲜度自行判断是否需要回源
        if (sameSession && isSameSessionData(cur, incoming)) {
          if (followsSelection && wId) void loadWorkflowSessions(wId)
          return
        }

        if (!sameSession) {
          notifyStore.notify('新反馈请求', incoming.title || '收到新的 AI 反馈请求，请及时查看。')
        }
        currentSession.value = incoming
        // 当收到新的 pending 会话时，仅当未选定任何会话（初始空白态），或者当前选中的正是该同会话/同工作流时才同步更新
        if (followsSelection) {
          selectedSession.value = incoming
        }
        // 把最新状态补丁进各级缓存（新轮次追加、已有轮次刷新实时字段），不再无条件整表重拉
        applySessionPayload(incoming)
        if (followsSelection && wId) void loadWorkflowSessions(wId)
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

  /**
   * 把一条完整会话补丁进 sessions / selectedSession / 工作流缓存 / currentSession。
   * 服务端的会话类事件载荷与 GET /sessions/:id 等价（含实时字段，图片只带引用），所以补丁即权威，无需再整表重拉。
   * 比本地副本更旧的载荷（例如撤回之后才到达的更早事件）按容器逐一丢弃，避免把新状态打回去。
   */
  function applySessionPayload(data: unknown) {
    const classified = classifySessionPayload(data)
    if (classified.kind !== 'full' || !classified.session) return
    const sess = classified.session

    const wId = sess.workflow_id || sess.session_id
    noteLocalChange(wId, sess.session_id)

    const idx = sessions.value.findIndex(s => s.session_id === sess.session_id)
    if (idx !== -1) {
      if (!isStaleIncoming(sessions.value[idx], sess)) {
        sessions.value[idx] = mergeSessionPatch(sessions.value[idx], sess)
      }
    } else {
      sessions.value.unshift(sess)
    }

    if (selectedSession.value?.session_id === sess.session_id && !isStaleIncoming(selectedSession.value, sess)) {
      selectedSession.value = mergeSessionPatch(selectedSession.value, sess)
    }

    if (wId && workflowSessionsCache.value[wId]) {
      const list = workflowSessionsCache.value[wId]
      const itemIdx = list.findIndex(s => s.session_id === sess.session_id)
      if (itemIdx !== -1) {
        if (!isStaleIncoming(list[itemIdx], sess)) {
          list[itemIdx] = mergeSessionPatch(list[itemIdx], sess)
        }
      } else {
        list.push(sess)
      }
      if (selectedSession.value?.workflow_id === wId || selectedSession.value?.session_id === wId) {
        currentWorkflowSessions.value = [...list]
      }
    }

    const cur = currentSession.value
    if (cur?.session_id === sess.session_id) {
      if (!isStaleIncoming(cur, sess)) {
        currentSession.value = sess.status === 'pending' ? mergeSessionPatch(cur, sess) : null
      }
    } else if (sess.status === 'pending' && !cur) {
      currentSession.value = sess
    }
  }

  // 校准请求统一走合并调度：同一窗口内范围取并集，去抖 120ms + 最长等待 1s，到点只执行一次
  function flushSync(scope: ResolvedSyncScope) {
    if (scope.current) void fetchCurrentSession()
    if (scope.sessions) void fetchSessions()
    if (scope.queued) void fetchQueuedFeedbacks()
    if (scope.workflow) {
      const wId = activeWorkflowId()
      if (wId) void loadWorkflowSessions(wId, true)
    }
  }
  const syncScheduler = createSyncScheduler(flushSync)

  function scheduleSync(scope: SyncScope) {
    syncScheduler.schedule(scope)
  }

  // 全量校准：仅用于无法补丁的场景（无载荷事件、结构性变更、重连、久别回到前台）
  function debouncedRefresh() {
    scheduleSync(FULL_SYNC_SCOPE)
  }

  // MCP 侧通知只带 session_id（等待者注册 / 注销、新会话创建等）：按 id 拉取这一条会话即可，
  // 同一会话的连续通知合并为一次在途 + 一次尾随；拉取结果自带实时字段，直接补丁
  const pokeFlight = createKeyedFlight(async (sessionId: string) => {
    try {
      const res = await sessionsApi.get(sessionId)
      if (res?.session) applySessionPayload(res.session)
    } catch (err: any) {
      // 404：会话已被删除，由 session_deleted 事件负责清理
      if (err?.response?.status !== 404) console.error(`Failed to fetch session ${sessionId}`, err)
    }
  })

  // 带完整会话的事件在补丁之后还需要校准哪些轻量数据
  function scopeForFullEvent(kind: SessionEventKind, session: FeedbackSession): SyncScope {
    switch (kind) {
      case 'archived':
      case 'unarchived':
        // 归档 / 取消归档影响整个工作流的多行状态，属于结构性变更
        return FULL_SYNC_SCOPE
      case 'update':
      case 'completed':
      case 'cancelled':
        // 完成 / 取消后可能有另一个待反馈会话接替成为「当前会话」；队列也可能随之变化
        return { current: true, queued: true }
      case 'updated':
        // 会话被 AI 消费（completed）时，排队中的反馈可能已随新轮次被吸收，队列需要校准；其他更新不影响队列
        return session.status === 'completed' ? { queued: true } : {}
      default:
        return {}
    }
  }

  function handleSessionEvent(kind: SessionEventKind, payload: unknown) {
    const classified = classifySessionPayload(payload)
    if (classified.kind === 'full' && classified.session) {
      // 载荷即权威：只打补丁，不整表重拉（INV-R4）
      applySessionPayload(classified.session)
      scheduleSync(scopeForFullEvent(kind, classified.session))
    } else if (classified.kind === 'poke' && classified.sessionId) {
      void pokeFlight.request(classified.sessionId, true)
      // 新会话创建需要触发「新反馈请求」通知并同步 currentSession，这些都在 fetchCurrentSession 里
      scheduleSync({ current: true, queued: true })
    } else {
      // 载荷里没有会话（解析失败等）：无法补丁，退回整表校准
      debouncedRefresh()
    }
  }

  async function fetchSessions(status?: string) {
    const reqSeq = ++fetchSessionsSeq
    const touched = new Set<string>()
    sessionsListTouched.add(touched)
    try {
      const res = await sessionsApi.list({ status, limit: 200, brief: true })
      if (reqSeq !== fetchSessionsSeq) {
        return
      }
      // 在途期间被本地补丁改动过的会话以本地为准，其余以服务端快照为准
      sessions.value = mergeFetchedList(sessions.value, res.sessions, touched, { localOnlyPosition: 'start' })
      if (!selectedSession.value) {
        if (currentSession.value) {
          selectSession(currentSession.value)
        } else if (res.sessions.length > 0) {
          selectSession(res.sessions[0])
        }
      } else {
        // 如果已经选中了某个会话，在 sessions 列表中找到最新对象并同步状态（例如已完成、已更新）
        const updated = res.sessions.find(s => s.session_id === selectedSession.value?.session_id)
        if (updated && !isStaleIncoming(selectedSession.value, updated)) {
          selectedSession.value = { ...selectedSession.value, ...updated }
        }
      }
    } catch (err) {
      console.error('Failed to list sessions', err)
    } finally {
      sessionsListTouched.delete(touched)
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
        // 提交接口返回该会话提交后的权威状态，本地补丁即可，界面不必等待任何整表重拉；
        // 后续的 session_completed 事件会把 currentSession 等衔接好，SSE 不在线时才退回整表校准
        applySessionPayload(res.session)
        if (!sseConnected.value) debouncedRefresh()
      } else {
        debouncedRefresh()
      }
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

  async function deleteSession(sessionId: string) {
    const res = await sessionsApi.delete(sessionId)
    // 1. 从 workflowSessionsCache 中清理
    for (const wId of Object.keys(workflowSessionsCache.value)) {
      const rounds = workflowSessionsCache.value[wId]
      const idx = rounds.findIndex(r => r.session_id === sessionId)
      if (idx !== -1) {
        rounds.splice(idx, 1)
        // 标记为本地已删除：在途的整表拉取若带回旧快照，不能让它复活
        noteLocalChange(wId, sessionId)
        if (rounds.length === 0) {
          delete workflowSessionsCache.value[wId]
        }
      }
    }
    noteLocalChange(undefined, sessionId)
    sessions.value = sessions.value.filter(s => s.session_id !== sessionId)
    // 2. 从 currentWorkflowSessions 中清理
    const curIdx = currentWorkflowSessions.value.findIndex(r => r.session_id === sessionId)
    if (curIdx !== -1) {
      currentWorkflowSessions.value.splice(curIdx, 1)
    }

    // 3. 如果删除的是当前选中的会话，切换至剩余最新轮次
    if (selectedSession.value?.session_id === sessionId) {
      if (currentWorkflowSessions.value.length > 0) {
        selectedSession.value = currentWorkflowSessions.value[currentWorkflowSessions.value.length - 1]
      } else {
        selectedSession.value = null
      }
    }
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = null
    }

    await fetchSessions()
    return res
  }

  async function restoreSession(session: FeedbackSession) {
    const res = await sessionsApi.restore(session)
    const wId = session.workflow_id
    if (wId && workflowSessionsCache.value[wId]) {
      delete workflowSessionsCache.value[wId]
    }
    if (wId) {
      await loadWorkflowSessions(wId, true)
    }
    selectedSession.value = res.session
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

  // 某个会话在本地各级缓存里已有的非空标题。
  // 自定义工作流标题由列表接口注入，撤回不会改变标题，所以撤回补丁沿用本地标题，不让接口里的原始标题把它冲掉。
  function cachedTitleOf(sessionId: string, workflowId?: string): string {
    const pools: Array<FeedbackSession | null | undefined> = [
      selectedSession.value,
      currentSession.value,
      ...(workflowId ? workflowSessionsCache.value[workflowId] ?? [] : []),
      ...sessions.value,
    ]
    return pools.find(s => !!s && s.session_id === sessionId && !!s.title)?.title || ''
  }

  async function revokeFeedback(sessionId: string) {
    try {
      submitting.value = true
      const res = await sessionsApi.revoke(sessionId)
      if (res.session) {
        // 撤回接口返回该会话撤回后的权威状态，本地补丁即可：回填输入框只取决于这一次往返，
        // 不再串行等待整条工作流与会话列表的重拉（INV-R1）。服务端随后广播的 session_updated 与此等价，重复应用是幂等的。
        const title = cachedTitleOf(sessionId, res.session.workflow_id || res.session.session_id)
        applySessionPayload(title ? { ...res.session, title } : res.session)
        // SSE 不在线时没有后续事件可依赖，退回后台整表校准
        if (!sseConnected.value) debouncedRefresh()
      } else {
        debouncedRefresh()
      }
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
      // 待发送队列可能因此变化（入队，或被进行中的会话吸收），这是一次极轻量的请求；
      // 会话本身由补丁 + SSE 事件维护，SSE 不在线时才退回整表校准
      await fetchQueuedFeedbacks(workflowId)
      if (!sseConnected.value) debouncedRefresh()
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
    lastSseGapAt = Date.now()
    scheduleReconnect()
  }

  function handleVisibilityChange() {
    if (document.visibilityState === 'hidden') {
      awayTracker.markInactive()
      return
    }
    if (document.visibilityState !== 'visible') return

    // 只有离开足够久才值得校准；短暂切换期间 SSE 持续在线，数据一直在线同步，没有任何必要发请求（INV-R5）
    const awayLongEnough = awayTracker.markActive()
    if (!sseConnected.value && !isManualDisconnect) {
      console.log('[SSE] Tab became visible and SSE is disconnected. Triggering immediate reconnect...')
      if (reconnectTimer) {
        window.clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      reconnectAttempt = 0
      connectSSE()
    } else if (sseConnected.value && awayLongEnough) {
      // 唤醒自愈：久别回到前台时主动同步最新状态，消除移动端休眠恢复后的数据滞后
      debouncedRefresh()
    }
  }

  function handleWindowBlur() {
    awayTracker.markInactive()
  }

  function handleWindowFocus() {
    const awayLongEnough = awayTracker.markActive()
    if (sseConnected.value && awayLongEnough) {
      debouncedRefresh()
    }
  }

  if (typeof window !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('blur', handleWindowBlur)
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
      if (hasConnectedOnce) {
        // 重连：断线期间可能漏掉了事件，选中工作流与队列都要整表校准一次（首次连接没有这个问题，避免重复拉取）
        const wId = activeWorkflowId()
        if (wId) void loadWorkflowSessions(wId, true)
        void fetchQueuedFeedbacks()
      }
      hasConnectedOnce = true
    }

    eventSource.onerror = () => {
      console.warn('[SSE] Connection error.')
      sseConnected.value = false
      lastSseGapAt = Date.now()
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
    const sessionEvents: Array<[string, SessionEventKind]> = [
      ['session_update', 'update'],
      ['session_updated', 'updated'],
      ['session_completed', 'completed'],
      ['session_cancelled', 'cancelled'],
      ['session_archived', 'archived'],
      ['session_unarchived', 'unarchived'],
      ['session_keepalive', 'keepalive'],
    ]
    for (const [eventName, kind] of sessionEvents) {
      eventSource.addEventListener(eventName, (e: MessageEvent) => {
        kickWatchdog()
        handleSessionEvent(kind, parseSSEEventData(e))
      })
    }
    eventSource.addEventListener('session_deleted', () => {
      kickWatchdog()
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
    eventSource.addEventListener('workflow_sheet_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload?.workflow_id) {
        lastSheetEvent.value = { workflow_id: payload.workflow_id, timestamp: Date.now() }
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
    lastSseGapAt = Date.now()
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
    lastSheetEvent,
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
    deleteSession,
    restoreSession,
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
