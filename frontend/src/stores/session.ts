import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { AgentLiveView, AsyncModeView, FeedbackSession, InboxConfigView, InboxSummary, QueuedFeedback, SessionBody, SessionImage } from '../api/types'
import { authApi, sessionsApi, workflowsApi } from '../api/client'
import type { CreateWorkflowRequest } from '../api/client'
import { useNotifyBusStore } from './notifyBus'
import { useNotifyPrefsStore } from './notifyPrefs'
import { useChatStore } from './chat'
import { useGroupStore } from './groups'
import { useAuthStore } from './auth'
import { useTaskStore } from './task'
import { useWorkItemStore } from './workItems'
import { useConfirmStore } from './confirms'
import { useAgentStateStore } from './agentState'
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
  isWorkflowTableBehind,
  mergeFetchedList,
  mergeSessionPatch,
  shouldFollowNewPending,
  type ResolvedSyncScope,
  type SyncScope,
} from '../utils/sessionSync'
import { watchPageLifecycle } from '../lib/pageLifecycle'
import { BODY_CHUNK, applyBodies, chunk, isLightRow, lightIdsAfter, preserveFullBodies } from '../lib/lightRows'
import { createRequestAnnouncer } from '../lib/requestAnnouncer'
import { buildSessionHashIndex } from '../lib/replyRef'
import { mergeWorkflowAggregates, patchPendingCount, type WorkflowAggregates } from '../lib/sidebarStatus'
import { mergeWorkflowRounds, type WorkflowRounds } from '../lib/workflowRounds'
import { secondaryGate } from '../lib/secondaryGate'
import { runConnectHooks } from '../lib/connectHooks'

// 会话类 SSE 事件的种类；载荷形态（完整会话 / 仅 session_id）由 classifySessionPayload 判定
type SessionEventKind = 'update' | 'updated' | 'completed' | 'cancelled' | 'keepalive' | 'archived' | 'unarchived'

// 工作流完整列表的兜底 TTL：SSE 健康时缓存由事件补丁维护，只有久未整表同步才回源一次
const WORKFLOW_CACHE_TTL_MS = 5 * 60 * 1000
// 页面离开超过该时长，回到前台才做一次校准；更短的切换完全依赖 SSE 事件，不发任何请求
const AWAY_RECALIBRATE_MS = 30 * 1000
// 按需保证整表（ensureWorkflowSessions）取数失败后的最短重试间隔
const ENSURE_RETRY_MS = 30 * 1000

export const useSessionStore = defineStore('session', () => {
  const currentSession = ref<FeedbackSession | null>(null)
  const selectedSession = ref<FeedbackSession | null>(null)
  const sessions = ref<FeedbackSession[]>([])
  // 各工作流的「工作流级」只读聚合（待答复的确认项数、智能体最近动作）：侧栏会话卡推「会话级状态」用（#159，见 lib/sidebarStatus.ts）。
  // 会话列表 / 当前会话接口顺带给出；confirm_updated 事件带着 pending_count 随时校准。
  // 单独存而不挂在会话行上：行会被各路事件按行补丁、被列表整体替换，聚合是工作流级的，不该跟着行的生灭。
  const workflowAggregates = ref<WorkflowAggregates>({})
  // 各工作流在库里一共有多少轮（含已归档）：侧栏「N 轮」徽标与归档库的轮数用，列表只带最新一批行，不能拿行数凑（#173，见 lib/workflowRounds.ts）。
  const workflowRounds = ref<WorkflowRounds>({})
  // 追加指令收件箱：条目（每条带状态：已排队 / Agent 已读取 / Agent 已确认）、概况、智能体最近一次交互、手动开关
  const queuedFeedbacks = ref<QueuedFeedback[]>([])
  const inboxSummary = ref<InboxSummary | null>(null)
  const agentLastActivityAt = ref<string | null>(null)
  /** 上面这些收件箱数据是哪个工作流的（还没取到 / 工作流变了而新数据没到时是空串）：页头状态徽标靠它判断「智能体最近的动作」到没到、是不是上一个工作流留下的。 */
  const inboxLoadedFor = ref('')
  const hookLastSeenAt = ref<string | null>(null)
  const agentLive = ref<AgentLiveView | null>(null)
  const inboxConfig = ref<InboxConfigView | null>(null)
  // 当前工作流的异步对话状态（随收件箱状态一起拉取；工具栏芯片与输入区据此显示）
  const asyncMode = ref<AsyncModeView | null>(null)
  const loading = ref(false)
  const submitting = ref(false)
  const sseConnected = ref(false)
  const isReconnecting = ref(false)
  const lastPhaseEvent = ref<{ workflow_id: string; phase: string } | null>(null)
  const lastSheetEvent = ref<{ workflow_id: string; timestamp: number } | null>(null)
  // 恢复胶囊被保存（MCP checkpoint_save）时的最近一次事件，时间线据此刷新胶囊标记。
  const lastCheckpointEvent = ref<{ workflow_id: string; revision: number; timestamp: number } | null>(null)
  let eventSource: EventSource | null = null
  let watchdogTimer: number | null = null
  let reconnectTimer: number | null = null
  let reconnectAttempt = 0
  let isManualDisconnect = false
  // 正在换取 SSE 票据（异步）：期间重复调用 connectSSE 必须被忽略，避免并发建立多条连接
  let sseConnecting = false
  // 页面处在 pagehide 与 pageshow 之间（进入前进 / 后退缓存，或正在被卸载）：事件流连接已经还给浏览器，不要自作主张地重连。
  // 它与「手动断开」是两回事——手动断开是用户 / 登出的意图，页面回来之后依然有效。
  let suspendedByPageHide = false
  // 离开时事件流是否「在用」（已连接 / 正在连接 / 正在等重连）：只有在用的才在页面回来时重连，没登录的页面不该因此冒出一条连接
  let resumeAfterPageShow = false

  const notifyBus = useNotifyBusStore()

  // 新反馈请求的提醒（声音 / 桌面通知 / ExecMesh 里的消息）。同一条请求会经过好几条路径进入前端：
  // 事件流里的完整载荷（applySessionPayload）、按 id 补拉、兜底的「当前会话」校准（fetchCurrentSession）。
  // 以前只有最后一条路径会提醒，而前两条路径会先把请求登记成「当前会话」，校准时就以为不是新的——
  // 结果事件流正常工作时反而不响。现在每条路径都有资格提醒，由 claim 保证同一条请求只提醒一次。
  // 呈现统一交给通知总线（stores/notifyBus.ts）：服务端的 notify 事件与这里的本地发现共用同一个 id（blk:<会话>），
  // 谁先到谁呈现，另一条被总线的幂等去重挡掉；多标签页只有一个响铃与弹系统通知。
  const requestAnnouncer = createRequestAnnouncer()
  function announceRequest(s: FeedbackSession) {
    if (!requestAnnouncer.claim(s)) return
    notifyBus.announceRequest(s)
  }

  const workflowSessionsCache = ref<Record<string, FeedbackSession[]>>({})
  const currentWorkflowSessions = ref<FeedbackSession[]>([])
  /**
   * 当前工作流的会话按短哈希（sess- 之后的部分）建的索引，正文里的会话号引用靠它解析：整个页面共用这一份。
   * 以前每段 Markdown 的 useLinkRefs 实例各扫一遍整张表，长会话里（几百段正文 × 几百行）是几万次计算，
   * 生产实测占打开过程 5～6 秒忙碌时间的约一成（#186 C2a，见 lib/replyRef.ts 的 buildSessionHashIndex）。
   */
  const sessionByHash = computed(() => buildSessionHashIndex(currentWorkflowSessions.value))
  const loadingWorkflowSessions = ref(false)
  /**
   * 「整表还没到、本地也没有缓存」的工作流 → 在途的冷加载个数（#186 Q117 a）。预览视图据此在整表到达前画骨架，
   * 而不是先画简略列表里的几个壳、整表到达后版面一下长高数万像素再贴底（一屏无关内容然后整页跳走，CLS 的主要来源）。
   * 只有「发起时本地没有任何整表行」的加载才计入；有缓存的刷新（先秒开缓存、再回源）不计，显示不受影响。
   */
  const coldLoading = ref<Record<string, number>>({})
  function markColdLoading(workflowId: string, delta: 1 | -1) {
    const next = (coldLoading.value[workflowId] ?? 0) + delta
    const copy = { ...coldLoading.value }
    if (next > 0) copy[workflowId] = next
    else delete copy[workflowId]
    coldLoading.value = copy
  }
  function isWorkflowColdLoading(workflowId: string): boolean {
    return !!workflowId && (coldLoading.value[workflowId] ?? 0) > 0
  }
  /**
   * 第一次取「简略会话列表」是否已有结果（成败都算，#186 Q117 a 的延伸）。在此之前侧栏与会话页还不知道「有没有会话」，
   * 不能画「暂无匹配 Workflow 或工单」「暂无交互对话」这类空状态——生产录像里它们先占了约 0.7～1 秒，随后内容才出来。
   * 只认不带状态过滤的那次（常规取数）；取数失败也算「有结果了」，回到原来的空状态，不会一直转。之后的刷新不再改它。
   */
  const sessionsListSettled = ref(false)

  // —— 同步引擎（D-171 / P1）：以下均为非响应式的同步元数据 ——
  // 工作流完整列表最近一次「开始拉取」的时间，用于判断缓存是否仍可信
  const workflowSyncedAt = new Map<string, number>()
  // 某个工作流的整表拉取在途期间，被本地补丁 / 本地操作改动过的会话 id；拉取返回时据此避免旧快照覆盖新补丁
  const workflowTouched = new Map<string, Set<string>>()
  // 同理，针对在途的简略列表（sessions）拉取
  const sessionsListTouched = new Set<Set<string>>()
  // 最近一次 SSE 断线的时间：此前完成的整表缓存不再可信（期间可能漏事件）
  let lastSseGapAt = 0
  // 实时流是否连上过：只有选中工作流的整表校准区分「首次 / 重连」（见 resyncAfterConnect）
  let sseOpenedBefore = false
  let activeWorkflowLoads = 0
  const awayTracker = createAwayTracker(AWAY_RECALIBRATE_MS)

  function activeWorkflowId(): string {
    return selectedSession.value?.workflow_id || selectedSession.value?.session_id || ''
  }

  // 这个工作流里本地已知的会话（完整列表缓存优先，没有就从简略列表里挑）：判断「选中的是不是最新一轮」用
  function knownWorkflowSessions(workflowId: string): readonly FeedbackSession[] {
    return workflowSessionsCache.value[workflowId] ?? sessions.value.filter(s => (s.workflow_id || s.session_id) === workflowId)
  }

  function noteLocalChange(workflowId: string | undefined, sessionId: string) {
    if (workflowId) workflowTouched.get(workflowId)?.add(sessionId)
    for (const touched of sessionsListTouched) touched.add(sessionId)
  }

  // —— 轻量行 / 正文按需（#169 第二期，纯函数规则见 lib/lightRows.ts）——
  // 取的会话表是「索引优先」的轻量表：较早的行只带首行预览（body_omitted），完整正文用 ensureSessionBodies 按需补。
  // （曾经还有不认轻量行的经典视图，要另取一份完整整表；#166 删掉经典视图后这些分支一并去掉。）

  // 正文按需：在途的行共用同一个 Promise（同一行不重复取）；自动重试的失败行 BODY_RETRY_MS 内不再试（用户手势除外）
  const BODY_RETRY_MS = 10 * 1000
  const bodyInflight = new Map<string, Promise<void>>()
  const bodyFailedAt = new Map<string, number>()
  const bodyKey = (workflowId: string, sessionId: string) => `${workflowId}\u0000${sessionId}`

  /** 把取回的正文填回缓存里仍是轻量的行；展示层、选中项里同一行的对象一并换成新的。 */
  function applyFetchedBodies(workflowId: string, bodies: readonly SessionBody[]) {
    const local = workflowSessionsCache.value[workflowId]
    if (!local || bodies.length === 0) return
    const next = applyBodies(local, bodies)
    if (next === local) return
    workflowSessionsCache.value[workflowId] = next
    if (activeWorkflowId() === workflowId) currentWorkflowSessions.value = next
    const picked = selectedSession.value
    if (picked && isLightRow(picked)) {
      const fresh = next.find(r => r.session_id === picked.session_id)
      if (fresh && !isLightRow(fresh)) selectedSession.value = fresh
    }
  }

  function fetchBodiesBatch(workflowId: string, batch: string[]): Promise<void> {
    const task = (async () => {
      try {
        const res = await sessionsApi.getWorkflowSessionBodies(workflowId, batch)
        applyFetchedBodies(workflowId, Array.isArray(res?.sessions) ? res.sessions : [])
        for (const id of batch) bodyFailedAt.delete(bodyKey(workflowId, id))
      } catch (err) {
        const now = Date.now()
        for (const id of batch) bodyFailedAt.set(bodyKey(workflowId, id), now)
        console.error(`Failed to load session bodies for ${workflowId}`, err)
      } finally {
        for (const id of batch) bodyInflight.delete(bodyKey(workflowId, id))
      }
    })()
    for (const id of batch) bodyInflight.set(bodyKey(workflowId, id), task)
    return task
  }

  /**
   * 保证这些行的正文在本地（轻量行才取，已是完整行的跳过；在途的直接等它）。取失败不抛错：行保持轻量，调用方看 isLightRow 自行判断。
   * force=true 用于用户手势（向上翻、点轨道跳转）：无视自动重试的退避。
   */
  async function ensureSessionBodies(workflowId: string, ids: readonly string[], opts: { force?: boolean } = {}): Promise<void> {
    if (!workflowId || ids.length === 0) return
    const rows = workflowSessionsCache.value[workflowId]
    if (!rows) return
    const wanted = new Set(ids)
    const now = Date.now()
    const waits: Promise<void>[] = []
    const fresh: string[] = []
    for (const row of rows) {
      if (!wanted.has(row.session_id) || !isLightRow(row)) continue
      const key = bodyKey(workflowId, row.session_id)
      const inflight = bodyInflight.get(key)
      if (inflight) {
        waits.push(inflight)
      } else if (opts.force || now - (bodyFailedAt.get(key) ?? 0) >= BODY_RETRY_MS) {
        fresh.push(row.session_id)
      }
    }
    for (const batch of chunk(fresh, BODY_CHUNK)) waits.push(fetchBodiesBatch(workflowId, batch))
    if (waits.length > 0) await Promise.all(waits)
  }

  /** 保证某一行之后（含落在间隙里的通知）所有行的正文在本地：预览窗口要显示「第 k 轮到最后一轮」时用；afterId 为 null = 从头。 */
  function ensureBodiesAfter(workflowId: string, afterId: string | null, opts: { force?: boolean } = {}): Promise<void> {
    return ensureSessionBodies(workflowId, lightIdsAfter(workflowSessionsCache.value[workflowId] ?? [], afterId), opts)
  }

  /** 保证整个工作流所有行的正文都在本地：全文搜索 / 导出这类要看每一行正文的功能先调它。 */
  function ensureAllBodies(workflowId: string, opts: { force?: boolean } = {}): Promise<void> {
    return ensureBodiesAfter(workflowId, null, opts)
  }

  // 卡片挂载时的自助取正文（轻量行渲染成骨架，再来这里补）：同一个宏任务里要的合成一批，一次请求
  const pendingBodyIds = new Map<string, Set<string>>()
  let pendingBodyTimer: ReturnType<typeof setTimeout> | null = null
  function queueSessionBody(workflowId: string, sessionId: string) {
    if (!workflowId || !sessionId) return
    let ids = pendingBodyIds.get(workflowId)
    if (!ids) pendingBodyIds.set(workflowId, (ids = new Set()))
    ids.add(sessionId)
    if (pendingBodyTimer) return
    pendingBodyTimer = setTimeout(() => {
      pendingBodyTimer = null
      const batches = [...pendingBodyIds]
      pendingBodyIds.clear()
      for (const [wId, set] of batches) void ensureSessionBodies(wId, [...set])
    }, 30)
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
        // 取「索引优先」的轻量表（较早的行只带首行预览，正文按需补，#169 第二期）
        const res = await sessionsApi.getWorkflowSessions(workflowId, { light: true })
        if (res && Array.isArray(res.sessions)) {
          const local = workflowSessionsCache.value[workflowId]
          // 再取一次表不能丢已经取过的正文：轻量行里本地有同版本完整行的，把正文接回来
          const fetched = preserveFullBodies(local, res.sessions)
          const merged = mergeFetchedList(local, fetched, touched, { localOnlyPosition: 'end' })
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
    // 次要请求让路（#169 第三期，lib/secondaryGate.ts）：打开 / 切换会话（非强制）的整表加载期间，任务 / 确认项 / 收件箱 / 恢复胶囊先不发，
    // 等整表到齐（成败都算，最长 1.5 秒）；强制刷新（SSE 触发、刚做完变更）不拦，次要请求照常即时发出。
    // 发起时本地没有任何整表行 = 冷加载：到整表到达（成败都算）为止，预览视图画骨架（isWorkflowColdLoading，#186 Q117 a）
    const cold = !cached?.length
    if (cold) markColdLoading(workflowId, 1)
    try {
      if (force) {
        await workflowFlight.request(workflowId, force)
        return
      }
      secondaryGate.hold()
      try {
        await workflowFlight.request(workflowId, force)
      } finally {
        secondaryGate.release()
      }
    } finally {
      if (cold) markColdLoading(workflowId, -1)
    }
  }

  /**
   * 加载（走单飞与缓存）并返回某个工作流的完整多轮列表。深链接点名了简略列表里没有的老轮次时用：
   * 此前这里绕过单飞直接请求，与会话页自己的加载撞成两次整表拉取（每次生产上约 600 KB，#177）。
   */
  async function loadWorkflowSessionsList(workflowId: string): Promise<readonly FeedbackSession[]> {
    await loadWorkflowSessions(workflowId)
    return workflowSessionsCache.value[workflowId] ?? []
  }

  // 会话页（时间线）是否正在显示（#169）。只有它需要工作流的整张会话表（生产上压缩后 0.6～1 MB）：
  // 任务 / 文档 / 群聊 / 项目 / 归档 / 设置等页面并不显示时间线，却会因为「默认选中了一个会话」白白把整表取一遍；
  // 深链接页还会先为占位的默认会话取一遍、再为目标取一遍。所以「顺带」的加载（选中、校准、重连）只在会话页显示时才发。
  // 显式的加载（用户点了某个会话、深链解析、引用需要它）不受此限。
  let timelineActive = false
  // 同一个状态的响应式镜像（#169 第三期）：会话列表里的次要加载（恢复胶囊等）据此在深链解析完之前不为占位会话发请求，
  // 解析完、会话页显示出来时再补发。内部逻辑仍读上面的普通变量（同步、不触发依赖收集）。
  const timelineShown = ref(false)

  /** 会话页显示 / 隐藏时由 FeedbackView 通知；显示出来时补取当前选中工作流的整表（有缓存先秒开，仍可信就不回源）。 */
  function setTimelineActive(active: boolean) {
    if (timelineActive === active) return
    timelineActive = active
    timelineShown.value = active
    if (!active) return
    const wId = activeWorkflowId()
    if (wId) void loadWorkflowSessions(wId)
  }

  /**
   * 「顺带」加载：时间线没在显示就不取。被跳过的强制刷新（校准 / 重连）记成「缓存不再可信」，
   * 切回会话页时 setTimelineActive 会先秒开缓存、再回源——整表有 ETag，没变就是 304。
   */
  function loadWorkflowSessionsIfShown(workflowId: string, force = false) {
    if (!timelineActive) {
      if (force) workflowSyncedAt.delete(workflowId)
      return
    }
    void loadWorkflowSessions(workflowId, force)
  }

  /**
   * 首次连上时核对当前工作流的整张会话表有没有落后于刚取回的会话列表（#243）。
   *
   * 会话列表（侧栏）每次连上都重取，整表却只在重连时才强制重取；页面载入到连上之间别处新增 / 更新的会话行（例如智能体刚接入、
   * 答复完一轮）于是出现在列表里、不在整表里——侧栏已经更新，会话页的对话还停在旧的。这种情形没有待答复的会话，
   * 当前会话的校准补丁与 20 秒兜底同步都不会碰整表，要等下一个事件或切换会话才补上。
   * 整表很大（生产上压缩后 0.6～1 MB），ETag 是内容哈希、304 与整表同价，所以不无条件再取，只在列表里有整表没有的行、
   * 或某一行比整表里的新时才强制取一次：常态（没有缺口）不多一个请求，load-budget.mjs 钉着的「深链接页整表恰好取 1 次」仍成立。
   * 整表还在途（深链接 / 冷加载）时先等它落地再核对：它是在实时流连上之前发出的，同样可能带着缺口之前的状态；不另发一次。
   */
  async function reconcileWorkflowTable() {
    const wId = activeWorkflowId()
    if (!wId) return
    if (workflowFlight.isBusy(wId)) await workflowFlight.request(wId)
    const table = workflowSessionsCache.value[wId]
    if (!table?.length) return
    const rows = sessions.value.filter(s => (s.workflow_id || s.session_id) === wId)
    if (isWorkflowTableBehind(table, rows)) loadWorkflowSessionsIfShown(wId, true)
  }

  /**
   * 保证某个工作流的整表在本地：非会话页上的正文引用（sess-xxxxxxxx）要靠它解析，而这些页面不再顺带加载整表。
   * 已有缓存（不管新旧）就只把它摆给展示层、不发请求；没有就走单飞取一次。
   */
  const ensureAttemptAt = new Map<string, number>()
  function ensureWorkflowSessions(workflowId: string) {
    if (!workflowId) return
    const cached = workflowSessionsCache.value[workflowId]
    if (cached?.length) {
      if (activeWorkflowId() === workflowId && currentWorkflowSessions.value !== cached) currentWorkflowSessions.value = cached
      return
    }
    // 正文每次重新装饰都会走到这里：取数失败（离线 / 服务端报错）时不能每次渲染都重试，30 秒内只试一次
    const now = Date.now()
    if (now - (ensureAttemptAt.get(workflowId) ?? 0) < ENSURE_RETRY_MS) return
    ensureAttemptAt.set(workflowId, now)
    void loadWorkflowSessions(workflowId)
  }

  function selectSession(session: FeedbackSession | null) {
    selectedSession.value = session
    if (session) {
      const wId = session.workflow_id || session.session_id
      if (wId) {
        // 展示层先对齐到目标工作流：有缓存用缓存，没有就清空，让时间线回退到简略列表里这个工作流自己的行。
        // 此前不清空，整表在途期间时间线（以及按选中工作流解析的引用）仍是上一个工作流的轮次。
        currentWorkflowSessions.value = workflowSessionsCache.value[wId] ?? []
        loadWorkflowSessionsIfShown(wId)
      }
    } else {
      currentWorkflowSessions.value = []
    }
  }

  /** 把会话行里的工作流级聚合并进 workflowAggregates（没有变化不替换对象）。 */
  function absorbWorkflowAggregates(rows: readonly FeedbackSession[], requestStartedAt: number) {
    const next = mergeWorkflowAggregates(workflowAggregates.value, rows, requestStartedAt)
    if (next) workflowAggregates.value = next
  }

  /** 把会话行里的「工作流总轮数」并进 workflowRounds（没有变化不替换对象，#173）。 */
  function absorbWorkflowRounds(rows: readonly FeedbackSession[]) {
    const next = mergeWorkflowRounds(workflowRounds.value, rows)
    if (next) workflowRounds.value = next
  }

  // 「当前会话」与简略会话列表也走单飞（#177）：页面打开时侧栏 / 布局 / 会话页 / SSE 连上各自都要拉一遍，
  // 此前各发各的，生产实测每次打开发出 4 次 /sessions/current 与 4～5 次简略列表（每次解压后 156 KB）。
  // fresh=false（页面挂载这类「只要有数据」的调用）：已有在途 / 排队的就直接加入，不重复发起；
  // fresh=true（默认，保持原语义）：保证有一次在本次调用之后才开始的拉取——变更之后要拿最新数据、SSE 刚连上要校准时用。
  const currentFlight = createKeyedFlight(() => runFetchCurrentSession())
  async function fetchCurrentSession(opts: { fresh?: boolean } = {}) {
    await currentFlight.request('current', opts.fresh ?? true)
  }

  async function runFetchCurrentSession() {
    try {
      loading.value = true
      const startedAt = Date.now()
      const res = await sessionsApi.getCurrent()
      if (res.has_session && res.session) {
        absorbWorkflowAggregates([res.session], startedAt)
        absorbWorkflowRounds([res.session])
        const incoming = res.session
        const cur = currentSession.value
        const sameSession = !!cur && cur.session_id === incoming.session_id

        // 响应生成于某个本地补丁之前（较旧的快照）：丢弃，避免把较新的本地状态打回去
        if (sameSession && isStaleIncoming(cur, incoming)) return

        const wId = incoming.workflow_id || incoming.session_id
        // 正在看这个工作流（没选任何会话、选中的就是它、或选中的是同工作流的某一轮）：它的会话列表要保持新鲜
        const viewingWorkflow = !selectedSession.value ||
          selectedSession.value.session_id === incoming.session_id ||
          (!!incoming.workflow_id && selectedSession.value.workflow_id === incoming.workflow_id)

        // 稳态（兜底轮询的绝大多数情况）：内容完全没变，不替换任何对象（避免触发下游监听与重渲染），
        // 只让缓存按新鲜度自行判断是否需要回源
        if (sameSession && isSameSessionData(cur, incoming)) {
          if (viewingWorkflow && wId) loadWorkflowSessionsIfShown(wId)
          return
        }

        announceRequest(incoming)
        currentSession.value = incoming
        // 选中项跟不跟：什么都没选 / 选中的就是它 / 停在这个工作流最新一轮的跟；翻到历史轮次的、停在别的工作流的一律不动，
        // 绝不主动强切窗口（#134：此前「同工作流就跟」，翻历史的人会在会话更新时被拽走）
        if (shouldFollowNewPending(selectedSession.value, incoming, knownWorkflowSessions(wId))) {
          selectedSession.value = incoming
        }
        // 把最新状态补丁进各级缓存（新轮次追加、已有轮次刷新实时字段），不再无条件整表重拉
        applySessionPayload(incoming)
        if (viewingWorkflow && wId) loadWorkflowSessionsIfShown(wId)
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
    // 比列表里的副本更旧的载荷（例如会话已完成之后才到达的更早的「待回复」事件）：既不覆盖，也不提醒
    const stale = idx !== -1 && isStaleIncoming(sessions.value[idx], sess)
    // 这条会话是不是「刚刚变成待答复」（新一轮到达，或撤回后重新待答复）：合并之前，本地副本里它还不是 pending
    const becamePending = !stale && sess.status === 'pending' && (idx === -1 || sessions.value[idx].status !== 'pending')
    if (idx !== -1) {
      if (!stale) {
        sessions.value[idx] = mergeSessionPatch(sessions.value[idx], sess)
      }
    } else {
      sessions.value.unshift(sess)
    }
    if (!stale) announceRequest(sess)

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

    // 新的待答复会话到达：选中项停在这个工作流最新一轮的，跟到新轮次——输入区的模式与页头状态都看选中项，不跟的话
    // 输入区会一直停在上一轮的「追加指令」，用户的回答进了收件箱而不是这个正在等的提问（#134）。
    // 不依赖 currentSession 是否空着：别的工作流已经有待答复会话时，这一条不会成为 currentSession。
    // 翻到历史轮次的、停在别的工作流的不动；稳态轮询与保活更新（不是「刚变成待答复」）不会走到这里。
    if (becamePending) {
      const picked = selectedSession.value
      if (picked && picked.session_id !== sess.session_id && shouldFollowNewPending(picked, sess, knownWorkflowSessions(wId))) {
        selectedSession.value = sess
      }
    }
  }

  // 校准请求统一走合并调度：同一窗口内范围取并集，去抖 120ms + 最长等待 1s，到点只执行一次
  function flushSync(scope: ResolvedSyncScope) {
    if (scope.current) void fetchCurrentSession()
    if (scope.sessions) void fetchSessions()
    if (scope.queued) void fetchQueuedFeedbacks()
    if (scope.workflow) {
      const wId = activeWorkflowId()
      if (wId) loadWorkflowSessionsIfShown(wId, true)
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
    // 「等你回答」是由待答复的汇报推导出来的状态，不会有 agent_state 事件：交互请求变化时让 Agent 状态跟着校准一次（#113）
    let agentWorkflow: string | undefined
    if (kind !== 'keepalive') {
      agentWorkflow =
        classified.session?.workflow_id ||
        (classified.sessionId ? sessions.value.find(s => s.session_id === classified.sessionId)?.workflow_id : undefined)
      useAgentStateStore().onSessionEvent(agentWorkflow)
    }
    if (classified.kind === 'full' && classified.session) {
      // 载荷即权威：只打补丁，不整表重拉（INV-R4）
      applySessionPayload(classified.session)
      scheduleSync(scopeForFullEvent(kind, classified.session))
    } else if (classified.kind === 'poke' && classified.sessionId) {
      const sessionId = classified.sessionId
      const flight = pokeFlight.request(sessionId, true)
      // 智能体刚发起 interactive_feedback 时，新会话的事件只带 session_id，事件到达时本地列表里还没有它，
      // 上面按 session_id 找不到工作流，Agent 状态就不会校准：芯片要等一分钟一次的定时校准才变成「等你回答」（#203）。
      // 会话取回之后列表里有了，按它自带的工作流补一次；上面已经能找到工作流的事件通知过了，不重复。
      if (kind !== 'keepalive' && !agentWorkflow) {
        void flight.then(() => useAgentStateStore().onSessionEvent(sessions.value.find(s => s.session_id === sessionId)?.workflow_id))
      }
      // 新会话创建需要触发「新反馈请求」通知并同步 currentSession，这些都在 fetchCurrentSession 里
      scheduleSync({ current: true, queued: true })
    } else {
      // 载荷里没有会话（解析失败等）：无法补丁，退回整表校准
      debouncedRefresh()
    }
  }

  // 简略列表按 status 分飞（key = status，常规调用都是空串）。同一 key 同时最多 1 个在途 + 1 个尾随，响应必然按发起顺序到达，
  // 所以不再需要「只认最后发起的那次、丢弃更早返回的」那套序号守卫——它在多个调用方同时发起时会把先到的响应白白丢掉，
  // 侧栏要等到最后发起的那次（生产上晚了约 1 秒）才有内容（#177 / #290）。
  const sessionsFlight = createKeyedFlight((statusKey: string) => runFetchSessions(statusKey || undefined))
  async function fetchSessions(status?: string, opts: { fresh?: boolean } = {}) {
    await sessionsFlight.request(status ?? '', opts.fresh ?? true)
  }

  async function runFetchSessions(status?: string) {
    const touched = new Set<string>()
    sessionsListTouched.add(touched)
    try {
      const startedAt = Date.now()
      // cover_workflows：最新 200 行之外再给每个工作流补一行，侧栏 / 归档库才不会丢掉被话痨工作流挤出窗口的老工作流（#173）
      const res = await sessionsApi.list({ status, limit: 200, brief: true, cover_workflows: true })
      absorbWorkflowAggregates(res.sessions, startedAt)
      absorbWorkflowRounds(res.sessions)
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
      // 在 try 里选中默认会话（selectSession → 整表冷加载计数 +1）之后才置位：同一段同步代码里，页面不会出现「列表有结果、却什么都没选」的一帧
      if (!status) sessionsListSettled.value = true
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

  async function updateWaitCountdownSeconds(sessionId: string, countdownSeconds: number) {
    const res = await sessionsApi.updateWaitCountdownSeconds(sessionId, countdownSeconds)
    if (currentSession.value?.session_id === sessionId) {
      currentSession.value = res.session
    }
    if (selectedSession.value?.session_id === sessionId) {
      selectedSession.value = res.session
    }
    await fetchSessions()
    return res.session
  }

  /** 把「用户还在」告诉服务端；返回服务端这次挂起的总时长（秒），没有人在挂着返回 null。 */
  async function sendPresence(sessionId: string, kind: 'plus' | 'typing'): Promise<number | null> {
    const res = await sessionsApi.presence(sessionId, kind)
    if (!res.active || typeof res.hold_seconds !== 'number') return null
    for (const s of [currentSession.value, selectedSession.value]) {
      if (s && s.session_id === sessionId) s.mcp_timeout_seconds = res.hold_seconds
    }
    return res.hold_seconds
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

  async function fetchQueuedFeedbacks(workflowId?: string, opts: { urgent?: boolean } = {}) {
    // 次要请求让路（#169 第三期）：整表在途期间收件箱先等一等（栅栏没立着时 wait() 返回 null，照旧同步往下走）；
    // 等完再算工作流——等的这段时间里用户可能已经切走了。用户刚做完动作（追加 / 撤回）的跟进请求 urgent，不让路。
    const waiting = opts.urgent ? null : secondaryGate.wait()
    if (waiting) await waiting
    try {
      const wId = workflowId || selectedSession.value?.workflow_id || currentSession.value?.workflow_id || ''
      if (!wId) {
        queuedFeedbacks.value = []
        inboxSummary.value = null
        agentLastActivityAt.value = null
        inboxLoadedFor.value = ''
        hookLastSeenAt.value = null
        agentLive.value = null
        asyncMode.value = null
        return
      }
      const res = await sessionsApi.getInbox(wId)
      // 请求在途时用户可能已经切到别的工作流：只采纳仍然对得上的结果
      const nowWf = workflowId || selectedSession.value?.workflow_id || currentSession.value?.workflow_id || ''
      if (nowWf !== wId) return
      queuedFeedbacks.value = res.items || []
      inboxSummary.value = res.summary || null
      agentLastActivityAt.value = res.agent_last_activity_at || null
      inboxLoadedFor.value = wId
      hookLastSeenAt.value = res.hook_last_seen_at || null
      agentLive.value = res.agent_live || null
      asyncMode.value = res.async_mode || null
      if (res.config) inboxConfig.value = res.config
    } catch (err) {
      console.error('Failed to load inbox', err)
    }
  }

  // 严格收尾门（#232 D2）：任务一变，挡着收尾的名单与个数就变了（窗口本身没变，服务端不会广播 workflow_mode_updated）。
  // 只在异步生效且门开着的会话里，把状态顺带重取一次（去抖，成批写入只取一次）。
  let gateRefreshTimer: ReturnType<typeof setTimeout> | null = null
  function scheduleGateRefresh() {
    const m = asyncMode.value
    if (!m?.effective || m.strict_wrapup !== true) return
    if (gateRefreshTimer) clearTimeout(gateRefreshTimer)
    gateRefreshTimer = setTimeout(() => {
      gateRefreshTimer = null
      void fetchQueuedFeedbacks()
    }, 400)
  }

  /** 开关 / 选档位提交后，直接采用服务端返回的状态（仍然只采纳对得上当前工作流的）。 */
  function setAsyncMode(view: AsyncModeView) {
    const nowWf = selectedSession.value?.workflow_id || currentSession.value?.workflow_id || ''
    if (!nowWf || view.workflow_id === nowWf) asyncMode.value = view
  }

  // 收件箱手动开关：设置页读写；其他页面通过 SSE 事件同步
  async function loadInboxConfig() {
    try {
      inboxConfig.value = await sessionsApi.getInboxConfig()
    } catch (err) {
      console.error('Failed to load inbox config', err)
    }
    return inboxConfig.value
  }

  async function updateInboxConfig(patch: Partial<InboxConfigView>) {
    const res = await sessionsApi.updateInboxConfig({
      enabled: patch.enabled,
      receipt_hint: patch.receipt_hint,
      hooks: patch.hooks,
      prompt_clause: patch.prompt_clause,
      hook_install_hint: patch.hook_install_hint,
    })
    inboxConfig.value = res
    return res
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

  /**
   * 网页手动新建会话（#174）：服务端落一行占位会话并返回启动提示词。
   * 本地先把占位会话补丁进列表（侧栏立刻出现这个「待接入」的会话），其它已打开的页面靠服务端广播的 session_updated 看到；
   * 不在这里切换选中项——要不要进入新会话由调用方（弹窗关闭时）决定。
   */
  async function createWorkflow(request: CreateWorkflowRequest) {
    const res = await workflowsApi.create(request)
    applySessionPayload(res.session)
    if (!sseConnected.value) debouncedRefresh()
    return res
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
      await fetchQueuedFeedbacks(workflowId, { urgent: true })
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
      await fetchQueuedFeedbacks(workflowId, { urgent: true })
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
    if (isManualDisconnect || suspendedByPageHide) return
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
    // 已在换票据：只需撤销可能存在的「手动断开」标记，让在途的那次继续完成即可
    if (sseConnecting) return
    sseConnecting = true
    void openEventSource()
  }

  // 解析 SSE 地址：已登录时先换取一次性票据，凭据不再以长期 token 的形式出现在 URL 上。
  // 返回 null 表示本次不应连接（未登录 / 暂时失败已安排重连）。
  async function resolveSseUrl(): Promise<string | null> {
    const authStore = useAuthStore()
    if (!authStore.token) return '/api/v1/events'
    try {
      const res = await authApi.sseTicket()
      return res.ticket
        ? `/api/v1/events?ticket=${encodeURIComponent(res.ticket)}`
        : '/api/v1/events'
    } catch (err: any) {
      const status: number | undefined = err?.response?.status
      if (status === 401) {
        // 登录态失效：全局拦截器已唤起登录；登录成功后 LoginDialog 会重新调用 connectSSE，这里不必空转重试
        isReconnecting.value = false
        return null
      }
      if (status === 404 || status === 405) {
        // 连接的是尚未支持票据的旧版服务端（例如前端开发服务器代理到旧后端）：回退到旧的 ?token= 方式
        return `/api/v1/events?token=${encodeURIComponent(authStore.token)}`
      }
      console.warn('[SSE] Failed to obtain a stream ticket, will retry.', err?.message || err)
      scheduleReconnect()
      return null
    }
  }

  /**
   * 实时流（首次或重新）连上之后，把「连上之前 / 断线期间发生、没人听见」的变化补取一次（#243）。
   *
   * 服务端的事件只发给此刻在线的订阅者、不补发。页面载入后先用 REST 取一遍各个仓库的快照、再换票据建立实时流，
   * 这中间的变化没有订阅者听见——首次连上与重连是同一种缺口，所以走同一个函数；以前这里只在重连时跑、而且漏了设置与托管自驾任务，
   * 页面载入后几百毫秒内的改动要等下一个事件才会出现（e2e 里 preset-versions 的间歇失败就是它，D-391）。
   * 每个仓库自己决定「没被用过就什么也不做」：有些在自己的 setup 里登记（只有用到它的页面才会实例化），有些自己判断。
   *
   * 区分首次与重连的只有选中工作流的整表：它的 ETag 是内容哈希，服务端要把整张表查出来、序列化完才知道能不能回 304，
   * 校准一次的服务端代价与完整下载相同（生产上整表压缩后 0.6～1 MB）。重连时缺口是整段断线，直接强制重取；
   * 首次连上的缺口只有几百毫秒、常态里什么也没发生，不无条件再取（load-budget.mjs 把「深链接页整表恰好取 1 次」钉成预算），
   * 而是等刚取回的会话列表到齐，核对整表有没有落后于它（reconcileWorkflowTable），落后了才取一次。
   * 不在这里的：Artifact 的数据 / 评论事件、阶段 / 检查点 / 工作表信号、凭据页（它们没有「快照 + 事件」的双通道，或另有校准路径）。
   */
  function resyncAfterConnect(reconnected: boolean, listsLanded: Promise<unknown>) {
    // 选中工作流的整表：重连时强制重取；首次连上等刚取回的会话列表到齐，只在整表落后于列表时才取（reconcileWorkflowTable）
    if (reconnected) {
      const wId = activeWorkflowId()
      if (wId) loadWorkflowSessionsIfShown(wId, true)
    } else {
      void listsLanded.then(reconcileWorkflowTable)
    }
    void fetchQueuedFeedbacks()
    // 每个页面都有的：全局设置、托管自驾任务（后者自己判断「没被取过就什么也不做」）
    useSettingsStore().onConnected()
    useTaskStore().onConnected()
    // 其余仓库（任务清单 / 确认项 / 项目文档 / 常驻提示词 / 异步档位）在自己的 setup 里登记补取（lib/connectHooks.ts）：
    // 只补取已经被实例化的。这里不能直接 use…Store()——那会把设置页这类从不显示它们的页面上也实例化出来，白发请求，
    // 还会改变行为（任务清单仓库一旦存在，/work 的默认项目跳转就不再等一次计数请求）；惰性加载的三个也不会因此被拉代码块
    runConnectHooks()
  }

  async function openEventSource() {
    let sseUrl: string | null = null
    try {
      sseUrl = await resolveSseUrl()
    } finally {
      sseConnecting = false
    }
    // 换票据期间用户已主动断开、页面已经离开（pagehide）或已有其他连接建立：放弃本次（页面回来时 resumeSSE 会重新连）
    if (!sseUrl || isManualDisconnect || suspendedByPageHide || eventSource) return

    eventSource = new EventSource(sseUrl)
    kickWatchdog()

    eventSource.onopen = () => {
      console.log('[SSE] Connection established successfully.')
      sseConnected.value = true
      isReconnecting.value = false
      reconnectAttempt = 0
      kickWatchdog()
      // 连接恢复后主动拉取最新数据，确保断线期间事件不丢失
      const current = fetchCurrentSession()
      const listed = fetchSessions()
      // Agent 身份与状态：首次连上取一次全量，重连后再校准一次（断线期间可能漏了 agent_state 事件）
      useAgentStateStore().onConnected()
      // 通知偏好（免打扰 / 会话静音 / 预览模式）：首次连上取一次，重连后再校准一次
      useNotifyPrefsStore().onConnected()
      // 聊天与群聊：页面载入到连上之间、断线期间的消息事件没人听见，也不会补发——打开着的会话补取最新一页，侧栏的未读数与群列表重取（#204）
      useChatStore().onConnected()
      useGroupStore().onConnected()
      const reconnected = sseOpenedBefore
      sseOpenedBefore = true
      resyncAfterConnect(reconnected, Promise.allSettled([current, listed]))
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
    eventSource.addEventListener('inbox_config_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload && typeof payload === 'object' && 'effective' in payload) inboxConfig.value = payload as InboxConfigView
    })
    // 异步对话开关 / 档位变化：本工作流的状态随收件箱状态重取；档位表有变化时设置页与工具栏的档位选择一并刷新
    eventSource.addEventListener('workflow_mode_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      const cur = selectedSession.value?.workflow_id || currentSession.value?.workflow_id || ''
      if (!payload?.workflow_id || payload.workflow_id === cur) void fetchQueuedFeedbacks()
    })
    eventSource.addEventListener('auth_profiles_updated', () => {
      kickWatchdog()
      void import('./asyncMode').then(({ useAsyncModeStore }) => useAsyncModeStore().onProfilesEvent())
      void fetchQueuedFeedbacks()
    })
    // 会话常驻规则（#232）：你在别处改了，或智能体第一次读到新版本（「已读到 / 待读」翻转）；只对已加载过的工作流重取
    eventSource.addEventListener('workflow_prompts_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      void import('./workflowPrompts').then(({ useWorkflowPromptsStore }) => useWorkflowPromptsStore().onEvent(payload?.workflow_id))
    })
    // 分支会话（#236）：关系有变化（建立 / 改设置 / 结束），或主会话与分支之间有了新消息——侧栏的分支树、页头芯片与会话页的分支消息流据此重取
    for (const evName of ['branch_changed', 'branch_message']) {
      eventSource.addEventListener(evName, (e: MessageEvent) => {
        kickWatchdog()
        const payload = parseSSEEventData(e)
        void import('./branches').then(({ useBranchStore }) => useBranchStore().onEvent(payload))
      })
    }
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
    // 任务清单（WorkItem，独立于会话）有变化：看板、详情与侧栏计数据此校准
    eventSource.addEventListener('work_item_updated', (e: MessageEvent) => {
      kickWatchdog()
      useWorkItemStore().onServerEvent(parseSSEEventData(e) || undefined)
      scheduleGateRefresh()
    })
    // 任务模块规划（树）有变化：任务页重新取一次（任务的归属、各节点的统计都随之更新）
    eventSource.addEventListener('work_module_updated', (e: MessageEvent) => {
      kickWatchdog()
      useWorkItemStore().onServerEvent(parseSSEEventData(e) || undefined)
    })
    eventSource.addEventListener('project_doc_updated', (e: MessageEvent) => {
      kickWatchdog()
      import('./projectDocs').then(({ useProjectDocStore }) => {
        useProjectDocStore().onServerEvent(parseSSEEventData(e) || undefined)
      })
    })
    // Artifact 的数据 / 评论有变化（Agent 写入或别处修改）：渲染框与评论列表经窗口事件各自校准
    for (const evName of ['artifact_data_updated', 'artifact_comment_updated']) {
      eventSource.addEventListener(evName, (e: MessageEvent) => {
        kickWatchdog()
        window.dispatchEvent(new CustomEvent('rm-artifact-event', { detail: { type: evName, ...(parseSSEEventData(e) || {}) } }))
      })
    }
    // 确认项（Q12）有变化：交流页的「待确认」区与时间线确认卡、文档页的决策卡据此校准
    eventSource.addEventListener('confirm_updated', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e) || undefined
      // 事件带着这个工作流此刻待处理的条数：侧栏里别的工作流的会话卡直接校准（#159），不必重拉整张会话列表
      const evt = payload as { workflow_id?: unknown; pending_count?: unknown } | undefined
      if (typeof evt?.workflow_id === 'string' && evt.workflow_id && typeof evt.pending_count === 'number') {
        workflowAggregates.value = patchPendingCount(workflowAggregates.value, evt.workflow_id, evt.pending_count, Date.now())
      }
      useConfirmStore().onServerEvent(payload)
      void import('./projectDocs').then(({ useProjectDocStore }) => useProjectDocStore().onConfirmEvent(payload))
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
    // Agent 状态变化（hook 汇报 / MCP 调用推进状态机）与身份改名：状态徽标、侧栏小圆点据此刷新（#113）
    eventSource.addEventListener('agent_state', (e: MessageEvent) => {
      kickWatchdog()
      useAgentStateStore().onServerEvent('agent_state', parseSSEEventData(e))
    })
    eventSource.addEventListener('agent_identity_updated', (e: MessageEvent) => {
      kickWatchdog()
      useAgentStateStore().onServerEvent('agent_identity_updated', parseSSEEventData(e))
    })
    // 统一通知闸放行的一条通知（#115）：由通知总线在这台设备上呈现（幂等、多标签选主、前台抑制、深链）
    eventSource.addEventListener('notify', (e: MessageEvent) => {
      kickWatchdog()
      notifyBus.ingest(parseSSEEventData(e))
    })
    // 通知偏好在别处（别的标签页 / 别的设备）被改了：重新取一次
    eventSource.addEventListener('notify_prefs_updated', () => {
      kickWatchdog()
      useNotifyPrefsStore().onServerEvent()
    })
    // 聊天消息流有变化（服务端已防抖合并）：群聊窗口刷新最新一页，侧栏刷新未读数
    for (const name of ['conversation_updated', 'conversation_changed']) {
      eventSource.addEventListener(name, (e: MessageEvent) => {
        kickWatchdog()
        const payload = parseSSEEventData(e)
        useChatStore().onServerEvent(payload)
        useGroupStore().onServerEvent(payload)
      })
    }
    eventSource.addEventListener('phase_changed', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload?.workflow_id) {
        lastPhaseEvent.value = { workflow_id: payload.workflow_id, phase: payload.phase || '' }
      }
    })
    eventSource.addEventListener('checkpoint_saved', (e: MessageEvent) => {
      kickWatchdog()
      const payload = parseSSEEventData(e)
      if (payload?.workflow_id) {
        lastCheckpointEvent.value = {
          workflow_id: payload.workflow_id,
          revision: Number(payload.revision) || 0,
          timestamp: Date.now()
        }
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

  // 页面离开（pagehide：进入前进 / 后退缓存，或被卸载）：把事件流连接还给浏览器。
  // 冻结在 bfcache 里的旧页面会一直占着同源的连接名额（明文 HTTP/1.1 每个站点最多 6 条），连续整页跳转几次后新页面就被卡住。
  // 与 disconnectSSE 的区别：这不是用户的意图，所以不置 isManualDisconnect，页面回来时要接着连。
  function suspendSSE() {
    suspendedByPageHide = true
    resumeAfterPageShow = !isManualDisconnect && !!(eventSource || sseConnecting || reconnectTimer)
    lastSseGapAt = Date.now() // 离开期间会漏事件：此前完成的整表缓存不再可信
    if (watchdogTimer) {
      window.clearTimeout(watchdogTimer)
      watchdogTimer = null
    }
    if (reconnectTimer) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (eventSource) {
      try {
        eventSource.close()
      } catch (_) {}
      eventSource = null
    }
    sseConnected.value = false
    isReconnecting.value = false
  }

  // 页面回来（pageshow，或重新可见）：立即重连；onopen 会整表校准一次，补上离开期间漏掉的事件。
  function resumeSSE() {
    suspendedByPageHide = false
    if (!resumeAfterPageShow || isManualDisconnect) return
    resumeAfterPageShow = false
    reconnectAttempt = 0
    connectSSE()
  }

  if (typeof window !== 'undefined') {
    watchPageLifecycle({ suspend: suspendSSE, resume: () => resumeSSE() })
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
    workflowAggregates,
    workflowRounds,
    queuedFeedbacks,
    inboxSummary,
    agentLastActivityAt,
    inboxLoadedFor,
    hookLastSeenAt,
    agentLive,
    inboxConfig,
    asyncMode,
    setAsyncMode,
    loading,
    submitting,
    sseConnected,
    isReconnecting,
    lastPhaseEvent,
    lastSheetEvent,
    lastCheckpointEvent,
    currentWorkflowSessions,
    sessionByHash,
    loadingWorkflowSessions,
    isWorkflowColdLoading,
    sessionsListSettled,
    loadWorkflowSessions,
    loadWorkflowSessionsList,
    setTimelineActive,
    timelineShown,
    ensureWorkflowSessions,
    ensureSessionBodies,
    ensureBodiesAfter,
    ensureAllBodies,
    queueSessionBody,
    selectSession,
    fetchCurrentSession,
    fetchSessions,
    fetchQueuedFeedbacks,
    loadInboxConfig,
    updateInboxConfig,
    submitFeedback,
    revokeFeedback,
    appendWorkflowFeedback,
    createWorkflow,
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
    updateWaitCountdownSeconds,
    sendPresence,
    connectSSE,
    disconnectSSE,
    manualReconnect,
    startFallbackSync,
    stopFallbackSync
  }
})
