import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import { api } from '../api/client'
import {
  UNLOCK_HEADER,
  getUnlockToken,
  terminalApi,
  toApiError,
  type ExecMode,
  type ExecStatus,
  type HostDefault,
  type PauseScope,
  type RunOutputPage,
  type RunView,
  type ThreadView,
} from '../api/terminal'
import {
  SseParser,
  appendLive,
  decodeBase64,
  emptyLive,
  isActiveRun,
  mergeRun,
  mergeRuns,
  type LiveOut,
} from '../lib/terminal/execRuns'
import { watchPageLifecycle } from '../lib/pageLifecycle'
import { useTerminalStore } from './terminal'

const PAGE = 50
const EVENTS_URL = '/api/v1/terminal/exec/events'
const STATUS_POLL_MS = 15_000
const REFRESH_DEBOUNCE_MS = 400
const RECONNECT_MIN_MS = 1_000
const RECONNECT_MAX_MS = 15_000
const LIVE_RUNS_MAX = 32

export type StreamState = 'off' | 'connecting' | 'on' | 'retry'

interface EventPayload {
  type: string
  run?: RunView
  run_id?: string
  stream?: 'stdout' | 'stderr'
  off?: number
  data?: string
  paused?: boolean
}

/**
 * 模型执行（ExecMesh 的线程与卡片）的前端状态：
 * 状态 / 线程汇总来自 REST，运行与实时输出来自人类专用的事件流（fetch 流式读取，带 Bearer 与解锁令牌）。
 * 事件流只在 ExecMesh 页面打开时连接；RelayMesh 模式下只用很低的频率拉取状态，让切换器上的徽标亮起来。
 */
export const useExecRunsStore = defineStore('execRuns', () => {
  const terminal = useTerminalStore()

  const status = ref<ExecStatus | null>(null)
  const threads = ref<ThreadView[]>([])
  const threadsLoaded = ref(false)
  const threadsError = ref('')

  /** 线程键 → 已载入的运行（按创建时间升序）。 */
  const runs = reactive<Record<string, RunView[]>>({})
  /** 线程键 → 是否还有更早的运行没载入。 */
  const hasMore = reactive<Record<string, boolean>>({})
  const loading = reactive<Record<string, boolean>>({})
  const loadError = reactive<Record<string, string>>({})
  /** 运行 ID → 实时输出（只在浏览器内存里，封顶）。 */
  const live = reactive<Record<string, LiveOut>>({})

  const stream = ref<StreamState>('off')
  /** 当前这条事件流是带着解锁令牌连接的（只有这样才会下发实时输出）。 */
  const streamUnlocked = ref(false)

  // ---------- 派生 ----------
  const available = computed(() => status.value?.available === true)
  const paused = computed(() => status.value?.paused === true)
  const awaiting = computed(() => status.value?.awaiting ?? 0)
  const activeCount = computed(() => status.value?.active ?? 0)
  /** 正在运行的（不含待批准）。 */
  const running = computed(() => Math.max(0, activeCount.value - awaiting.value))

  function threadOf(key: string): ThreadView | undefined {
    return threads.value.find(t => t.key === key)
  }

  // ---------- 拉取 ----------
  async function refreshStatus(): Promise<void> {
    try {
      status.value = await terminalApi.execStatus()
    } catch {
      /* 网络抖动：保留上一次的状态 */
    }
  }

  async function refreshThreads(): Promise<void> {
    try {
      const r = await terminalApi.execThreads({ limit: 200 })
      threads.value = r.threads ?? []
      threadsLoaded.value = true
      threadsError.value = ''
    } catch (e) {
      if (!threadsLoaded.value) threadsError.value = toApiError(e).message
    }
  }

  let refreshTimer: ReturnType<typeof setTimeout> | null = null
  /** 运行状态有变化后，合并多次变化只刷新一次状态与线程汇总。 */
  function scheduleRefresh() {
    if (refreshTimer) return
    refreshTimer = setTimeout(() => {
      refreshTimer = null
      void refreshStatus()
      void refreshThreads()
    }, REFRESH_DEBOUNCE_MS)
  }

  /** 线程键 → 已载入部分里最早一条运行的数据库游标（翻页用，由接口返回）。 */
  const cursors: Record<string, number> = {}

  /** 载入一个线程的运行。older=true 时往前翻一页；否则（重新）载入最近一页并与已有的合并。 */
  async function loadRuns(key: string, older = false): Promise<void> {
    if (loading[key]) return
    loading[key] = true
    try {
      const cur = runs[key] ?? []
      const before = older && cur.length ? (cursors[key] ?? 0) : 0
      const r = await terminalApi.execRuns({ thread: key, limit: PAGE, before: before || undefined })
      const got = r.runs ?? []
      runs[key] = mergeRuns(cur, got)
      if (older || cursors[key] === undefined) {
        cursors[key] = r.cursor || cursors[key] || 0
        hasMore[key] = got.length >= PAGE
      }
      loadError[key] = ''
    } catch (e) {
      loadError[key] = toApiError(e).message
    } finally {
      loading[key] = false
    }
  }
  // ---------- 事件处理 ----------
  function applyRun(run: RunView) {
    const key = run.thread_key
    if (runs[key]) runs[key] = mergeRun(runs[key], run)
    // 终态之后实时输出不再需要（展开时改读主机上保存的完整输出）。
    if (!isActiveRun(run.state)) {
      delete live[run.run_id]
      decoders.delete(run.run_id)
    }
    scheduleRefresh()
  }

  const decoders = new Map<string, { stdout: TextDecoder; stderr: TextDecoder }>()

  function applyOut(ev: EventPayload) {
    if (!ev.run_id || !ev.stream || !ev.data) return
    const bytes = decodeBase64(ev.data)
    if (bytes.length === 0) return
    let d = decoders.get(ev.run_id)
    if (!d) {
      d = { stdout: new TextDecoder('utf-8', { fatal: false }), stderr: new TextDecoder('utf-8', { fatal: false }) }
      decoders.set(ev.run_id, d)
    }
    const text = d[ev.stream].decode(bytes, { stream: true })
    if (!live[ev.run_id]) {
      // 只给最近的若干次运行缓存实时输出，避免终态事件丢失时无限增长。
      const ids = Object.keys(live)
      if (ids.length >= LIVE_RUNS_MAX) {
        delete live[ids[0]]
        decoders.delete(ids[0])
      }
    }
    live[ev.run_id] = appendLive(live[ev.run_id] ?? emptyLive(), ev.stream, ev.off ?? 0, text, bytes.length)
  }

  function handleEvent(name: string, raw: string) {
    let ev: EventPayload
    try {
      ev = JSON.parse(raw) as EventPayload
    } catch {
      return
    }
    switch (name) {
      case 'run':
        if (ev.run) applyRun(ev.run)
        break
      case 'out':
        applyOut(ev)
        break
      case 'pause':
        if (typeof ev.paused === 'boolean') {
          if (status.value) status.value = { ...status.value, paused: ev.paused }
          else void refreshStatus()
        }
        break
      case 'host':
        void terminal.refreshHosts()
        break
    }
  }

  // ---------- 事件流 ----------
  let users = 0
  let gen = 0
  let abort: AbortController | null = null
  /** 页面上正在看的线程：重连后需要补拉它们（断开期间的事件不会重放）。 */
  const watching = new Set<string>()

  function sleep(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise(resolve => {
      const t = setTimeout(resolve, ms)
      signal.addEventListener('abort', () => {
        clearTimeout(t)
        resolve()
      })
    })
  }

  async function afterConnect() {
    await Promise.all([refreshStatus(), refreshThreads()])
    for (const key of watching) void loadRuns(key)
  }

  async function runStream(myGen: number) {
    let delay = RECONNECT_MIN_MS
    while (myGen === gen) {
      const ctl = new AbortController()
      abort = ctl
      stream.value = 'connecting'
      try {
        const headers: Record<string, string> = { Accept: 'text/event-stream' }
        const auth = api.defaults.headers.common['Authorization']
        if (typeof auth === 'string' && auth) headers['Authorization'] = auth
        const unlock = getUnlockToken()
        if (unlock) headers[UNLOCK_HEADER] = unlock
        const res = await fetch(EVENTS_URL, { headers, signal: ctl.signal, cache: 'no-store' })
        if (res.status === 401 || res.status === 403) {
          // 未登录 / 总闸关闭：不再重试，由页面其他部分的状态检查给出提示。
          stream.value = 'off'
          return
        }
        if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`)
        streamUnlocked.value = !!unlock
        stream.value = 'on'
        delay = RECONNECT_MIN_MS
        void afterConnect()
        const reader = res.body.getReader()
        const dec = new TextDecoder()
        const parser = new SseParser()
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          for (const e of parser.push(dec.decode(value, { stream: true }))) handleEvent(e.event, e.data)
        }
      } catch {
        if (myGen !== gen) return
      }
      if (myGen !== gen) return
      stream.value = 'retry'
      await sleep(delay, ctl.signal)
      delay = Math.min(delay * 2, RECONNECT_MAX_MS)
    }
  }

  /** 进入 ExecMesh 页面时调用（可重入）：连接事件流并拉取状态与线程汇总。 */
  function start() {
    users++
    if (users > 1) return
    gen++
    void runStream(gen)
  }

  function stop() {
    users = Math.max(0, users - 1)
    if (users > 0) return
    gen++
    abort?.abort()
    abort = null
    stream.value = 'off'
    streamUnlocked.value = false
  }

  /** 解锁状态变化后重连，让服务端按新的解锁状态决定是否下发实时输出。 */
  function restartStream() {
    if (users === 0) return
    gen++
    abort?.abort()
    void runStream(gen)
  }

  // 页面离开（pagehide：进入前进 / 后退缓存，或被卸载）：把事件流连接还给浏览器，页面回来时立刻重连。
  // 冻结在 bfcache 里的旧页面会一直占着同源的连接名额（明文 HTTP/1.1 每个站点最多 6 条），见 lib/pageLifecycle.ts。
  // 断开期间的事件不会重放，所以重连成功后 afterConnect 会补拉状态与正在看的线程。
  function suspendStream() {
    if (users === 0) return
    gen++ // 让正在跑的 runStream 循环在下一次检查时退出，不再重连
    abort?.abort()
    abort = null
    stream.value = 'off'
  }
  function resumeStream() {
    if (users === 0) return
    gen++
    void runStream(gen)
  }
  if (typeof window !== 'undefined') {
    watchPageLifecycle({ suspend: suspendStream, resume: () => resumeStream() })
  }

  function watchThread(key: string) {
    watching.add(key)
  }
  function unwatchThread(key: string) {
    watching.delete(key)
  }

  // RelayMesh 模式下的低频状态拉取（切换器徽标用）。
  let pollUsers = 0
  let pollTimer: ReturnType<typeof setInterval> | null = null
  function startPolling() {
    pollUsers++
    if (pollUsers > 1) return
    void refreshStatus()
    pollTimer = setInterval(() => {
      if (!document.hidden && terminal.gateOpen && users === 0) void refreshStatus()
    }, STATUS_POLL_MS)
  }
  function stopPolling() {
    pollUsers = Math.max(0, pollUsers - 1)
    if (pollUsers > 0) return
    if (pollTimer) clearInterval(pollTimer)
    pollTimer = null
  }

  // ---------- 人的操作 ----------
  function keep(r: RunView) {
    applyRun(r)
    return r
  }

  async function cancel(run: RunView): Promise<RunView | null> {
    try {
      return keep((await terminalApi.execCancel(run.run_id)).run)
    } catch (e) {
      terminal.notifyError(e, '取消失败')
      void refreshRun(run.run_id) // 多半是状态已经变了（刚好结束 / 超时）：把卡片刷新成真实状态
      return null
    }
  }

  async function reject(run: RunView): Promise<RunView | null> {
    try {
      return keep((await terminalApi.execReject(run.run_id)).run)
    } catch (e) {
      terminal.notifyError(e, '拒绝失败')
      void refreshRun(run.run_id) // 多半是状态已经变了（刚好结束 / 超时）：把卡片刷新成真实状态
      return null
    }
  }

  async function approve(run: RunView): Promise<RunView | null> {
    try {
      const r = await terminal.withUnlock(() => terminalApi.execApprove(run.run_id), '批准命令需要再次验证密码')
      return keep(r.run)
    } catch (e) {
      terminal.notifyError(e, '批准失败')
      void refreshRun(run.run_id) // 多半是状态已经变了（刚好结束 / 超时）：把卡片刷新成真实状态
      return null
    }
  }

  async function setPaused(scope: PauseScope, machineCode: string | undefined, on: boolean): Promise<boolean> {
    try {
      if (on) status.value = await terminalApi.execPause(scope, machineCode)
      else status.value = await terminal.withUnlock(() => terminalApi.execResume(scope, machineCode), '恢复模型执行需要再次验证密码')
      await terminal.refreshHosts()
      toast.success(on ? (scope === 'all' ? '已暂停全部模型' : '已暂停这台主机的模型') : scope === 'all' ? '已恢复模型执行' : '已恢复这台主机的模型')
      return true
    } catch (e) {
      terminal.notifyError(e, on ? '暂停失败' : '恢复失败')
      return false
    }
  }

  async function setHostModel(code: string, body: { allow_model?: boolean; exec_mode?: ExecMode | string; default_for_new_models?: HostDefault }): Promise<boolean> {
    try {
      await terminal.withUnlock(() => terminalApi.setHostModel(code, body), '修改主机的模型访问设置需要再次验证密码')
      await terminal.refreshHosts()
      scheduleRefresh()
      return true
    } catch (e) {
      terminal.notifyError(e, '保存失败')
      return false
    }
  }

  /** 读完整输出的一页（需要解锁；调用方在未解锁时应先引导解锁）。 */
  async function readOutput(runId: string, streamName: 'stdout' | 'stderr', offset: number, limit?: number): Promise<RunOutputPage> {
    return terminalApi.execOutput(runId, { stream: streamName, offset, limit })
  }

  /** 一个线程里按时间升序的运行（没载入过则为空）。 */
  function runsOf(key: string): RunView[] {
    return runs[key] ?? []
  }

  /** 把线程里某条运行换成最新内容（详情页单独刷新一条时用）。 */
  async function refreshRun(runId: string): Promise<RunView | null> {
    try {
      return keep((await terminalApi.execRun(runId)).run)
    } catch {
      return null
    }
  }

  /** 清掉所有缓存（登出 / 总闸关闭时）。 */
  function reset() {
    status.value = null
    threads.value = []
    threadsLoaded.value = false
    for (const k of Object.keys(runs)) delete runs[k]
    for (const k of Object.keys(live)) delete live[k]
    decoders.clear()
  }

  return {
    status,
    threads,
    threadsLoaded,
    threadsError,
    runs,
    hasMore,
    loading,
    loadError,
    live,
    stream,
    streamUnlocked,
    available,
    paused,
    awaiting,
    activeCount,
    running,
    threadOf,
    runsOf,
    refreshStatus,
    refreshThreads,
    loadRuns,
    refreshRun,
    start,
    stop,
    restartStream,
    watchThread,
    unwatchThread,
    startPolling,
    stopPolling,
    cancel,
    reject,
    approve,
    setPaused,
    setHostModel,
    readOutput,
    reset,
  }
})
