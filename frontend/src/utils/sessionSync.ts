/**
 * 会话同步的纯逻辑：载荷分类、补丁合并、列表拉取合并、单飞、合并调度、离开时长判定。
 *
 * 全部不依赖 Vue / Pinia / 网络，便于脱离浏览器做单元测试（sessionSync.test.ts）。
 * 设计背景：
 *  - 服务端的会话类 SSE 事件载荷与 GET /sessions/:id 等价（含实时字段、图片只带引用），带完整载荷的事件只需补丁，不必整表重拉；
 *  - 整表校准仍然存在（重连、久别回到前台、兜底），但必须单飞合并，且不能用较旧的服务端快照覆盖期间到达的本地补丁。
 */
import type { FeedbackSession } from '../api/types'
import { isNotice } from '../lib/asyncMode'

// ---------------------------------------------------------------------------
// 载荷分类
// ---------------------------------------------------------------------------

/** full：带完整会话，直接补丁；poke：只有 session_id（MCP 侧通知），需要按 id 单条拉取；none：不含会话信息。 */
export type SessionPayloadKind = 'full' | 'poke' | 'none'

export interface ClassifiedPayload {
  kind: SessionPayloadKind
  session?: FeedbackSession
  sessionId?: string
}

/**
 * 判定 SSE / REST 返回的会话载荷形态。
 * 兼容 `{ session: {...} }` 与直接的会话对象两种包装；完整会话以 `status` 字段存在为准
 * （服务端模型的 status 永远非空，而 MCP 侧的 poke 只有 `{ session_id }`）。
 */
export function classifySessionPayload(data: unknown): ClassifiedPayload {
  if (!data || typeof data !== 'object') return { kind: 'none' }
  const root = data as Record<string, unknown>
  const inner = root.session && typeof root.session === 'object' ? (root.session as Record<string, unknown>) : root
  const id = inner.session_id
  if (typeof id !== 'string' || id === '') return { kind: 'none' }
  if (typeof inner.status === 'string' && inner.status !== '') {
    return { kind: 'full', session: inner as unknown as FeedbackSession, sessionId: id }
  }
  return { kind: 'poke', sessionId: id }
}

// ---------------------------------------------------------------------------
// 补丁合并
// ---------------------------------------------------------------------------

function parseTime(value: string | null | undefined): number {
  if (!value) return Number.NaN
  return Date.parse(value)
}

/**
 * 载荷是否比本地副本更旧。任一方缺少或无法解析 updated_at 时保守地认为「不旧」，交由合并处理。
 * 用途：撤回后迟到的旧事件（例如更早的 session_completed）不能把已经回到待反馈的会话打回已完成。
 */
export function isStaleIncoming(
  local: Pick<FeedbackSession, 'updated_at'> | null | undefined,
  incoming: Pick<FeedbackSession, 'updated_at'>
): boolean {
  if (!local) return false
  const l = parseTime(local.updated_at)
  const i = parseTime(incoming.updated_at)
  if (Number.isNaN(l) || Number.isNaN(i)) return false
  return i < l
}

/**
 * 工作流的整张会话表有没有落后于一批刚取回的会话行（#243）：行里有整表没有的，或某一行比整表里的新（updated_at 更晚）。
 * rows 由调用方按工作流筛好（一般是刚取回的侧栏会话列表里属于这个工作流的行）；整表还没有任何行时不算落后——
 * 那是「还没取到」，之后第一次取到的就是最新的。时间解析不了时不算落后（与 isStaleIncoming 一样保守）。
 * 反过来不成立：整表里有、行里没有的（更早的轮次）正常，列表只带最新一批行。
 */
export function isWorkflowTableBehind(
  table: readonly Pick<FeedbackSession, 'session_id' | 'updated_at'>[],
  rows: readonly Pick<FeedbackSession, 'session_id' | 'updated_at'>[]
): boolean {
  if (!table.length) return false
  const byId = new Map(table.map(s => [s.session_id, s]))
  for (const row of rows) {
    const cached = byId.get(row.session_id)
    if (!cached) return true
    if (isStaleIncoming(row, cached)) return true
  }
  return false
}

/**
 * 服务端对空值指针字段使用 omitempty：字段缺席即表示已被清除（例如撤回清空 consumed_at，
 * 保活记录被删除后不再返回 last_keepalive_at）。直接展开合并会把旧值一直留着，所以这些字段缺席时要显式删除。
 * 仅用于「完整会话」载荷；简略列表行不走这里。
 * body_omitted 也在其中（#169 第二期）：完整会话载荷带着全部正文，缓存里原来是轻量行的，补丁之后就是完整行了。
 */
const CLEARABLE_KEYS = ['consumed_at', 'responded_at', 'deadline_at', 'mcp_active_at', 'mcp_timeout_seconds', 'last_keepalive_at', 'body_omitted'] as const

export function mergeSessionPatch(base: FeedbackSession, incoming: FeedbackSession): FeedbackSession {
  const merged: FeedbackSession = { ...base, ...incoming }
  for (const key of CLEARABLE_KEYS) {
    if (!(key in incoming)) delete (merged as unknown as Record<string, unknown>)[key]
  }
  return merged
}

/** 两个会话的数据是否完全一致（稳态轮询据此避免无谓地替换对象、触发下游监听）。 */
export function isSameSessionData(a: FeedbackSession | null | undefined, b: FeedbackSession | null | undefined): boolean {
  if (a === b) return true
  if (!a || !b) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

// ---------------------------------------------------------------------------
// 选中项跟随新的待答复会话
// ---------------------------------------------------------------------------

/**
 * 新的待答复会话首次成为「当前会话」时，页面上选中的会话要不要跟过去。
 * 输入区的模式（回复 / 追加反馈 / 追加指令）和页头的状态都看选中项：选中项停在上一轮（已完成）时，
 * 用户在输入框里写的话会进收件箱，而不是答复这个正在等的提问（#134）。
 *
 * - 什么都没选、或选中的就是它：跟；
 * - 选中的在别的工作流：不跟——后台别的对话的提问不能把正在看的对话抢走；
 * - 同一个工作流：选中的是最新一轮（或其后的进度通知）= 在跟进这段对话，跟到新轮次；
 *   选中的比某一轮更早 = 用户特意翻到了历史（通知深链、任务里的「关联会话」），不拽走。
 *
 * 只在「新的待答复会话首次出现」那一刻调用；稳态轮询不走这里，所以不会把有意选的旧轮次拽回来。
 * 时间解析不了时保守地认为「选中的就是最新」（默认位置就在末尾）。
 */
export function shouldFollowNewPending(
  selected: Pick<FeedbackSession, 'session_id' | 'workflow_id' | 'created_at'> | null | undefined,
  incoming: Pick<FeedbackSession, 'session_id' | 'workflow_id'>,
  workflowSessions: readonly Pick<FeedbackSession, 'session_id' | 'created_at' | 'kind'>[]
): boolean {
  if (!selected) return true
  if (selected.session_id === incoming.session_id) return true
  if (!incoming.workflow_id || selected.workflow_id !== incoming.workflow_id) return false
  const selectedAt = parseTime(selected.created_at)
  if (Number.isNaN(selectedAt)) return true
  for (const s of workflowSessions) {
    if (s.session_id === incoming.session_id || s.session_id === selected.session_id) continue
    if (isNotice(s)) continue
    const at = parseTime(s.created_at)
    if (!Number.isNaN(at) && at > selectedAt) return false
  }
  return true
}

// ---------------------------------------------------------------------------
// 列表拉取合并
// ---------------------------------------------------------------------------

export interface MergeListOptions {
  /** 本地新增、服务端快照里还没有的会话放在头部（简略列表，新的在前）还是尾部（工作流多轮，新的在后）。 */
  localOnlyPosition: 'start' | 'end'
}

/**
 * 把一次整表拉取的结果合并进本地列表。
 *
 * 拉取在途期间，SSE 补丁 / 本地操作可能已经改动了其中一部分会话（touched）。服务端快照可能早于这些改动，
 * 所以对 touched 的会话：本地有且不比快照旧则保留本地，本地已删除则保持删除，本地独有（新轮次）则保留；
 * 其余会话一律以服务端快照为准，保证最终一致。
 */
export function mergeFetchedList(
  local: FeedbackSession[] | undefined,
  fetched: FeedbackSession[],
  touched: ReadonlySet<string>,
  options: MergeListOptions
): FeedbackSession[] {
  if (!local || local.length === 0 || touched.size === 0) return fetched

  const localById = new Map<string, FeedbackSession>()
  for (const item of local) localById.set(item.session_id, item)

  const merged: FeedbackSession[] = []
  const seen = new Set<string>()
  for (const remote of fetched) {
    seen.add(remote.session_id)
    if (!touched.has(remote.session_id)) {
      merged.push(remote)
      continue
    }
    const mine = localById.get(remote.session_id)
    if (!mine) continue // 拉取期间被本地删除，不能因为旧快照而复活
    merged.push(isNotOlder(mine, remote) ? mine : remote)
  }

  const localOnly = local.filter(item => touched.has(item.session_id) && !seen.has(item.session_id))
  if (localOnly.length === 0) return merged
  return options.localOnlyPosition === 'start' ? [...localOnly, ...merged] : [...merged, ...localOnly]
}

/** 本地副本的 updated_at 不早于快照（或无法比较）时视为本地更新。 */
function isNotOlder(mine: FeedbackSession, remote: FeedbackSession): boolean {
  const m = parseTime(mine.updated_at)
  const r = parseTime(remote.updated_at)
  if (Number.isNaN(m) || Number.isNaN(r)) return true
  return m >= r
}

// ---------------------------------------------------------------------------
// 单飞
// ---------------------------------------------------------------------------

export interface KeyedFlight {
  /**
   * 请求执行一次任务。
   * fresh=false：若该 key 已有在途（或排队）执行则直接加入，不重复发起；
   * fresh=true：保证存在一次「在本次调用之后才开始」的执行——在途则排一个尾随执行，多次 fresh 请求合并为同一个尾随。
   * 返回的 Promise 永不 reject（错误交给 onError），在对应执行结束时 resolve。
   */
  request(key: string, fresh?: boolean): Promise<void>
  isBusy(key: string): boolean
}

export function createKeyedFlight(
  task: (key: string) => Promise<void>,
  onError?: (key: string, error: unknown) => void
): KeyedFlight {
  interface Flight {
    running: Promise<void>
    trailing: Promise<void> | null
  }
  const flights = new Map<string, Flight>()

  function start(key: string): Promise<void> {
    const flight: Flight = { running: Promise.resolve(), trailing: null }
    flight.running = (async () => {
      try {
        await task(key)
      } catch (error) {
        onError?.(key, error)
      } finally {
        if (flights.get(key) === flight) flights.delete(key)
      }
    })()
    flights.set(key, flight)
    return flight.running
  }

  return {
    request(key, fresh = false) {
      const flight = flights.get(key)
      if (!flight) return start(key)
      if (!fresh) return flight.trailing ?? flight.running
      if (!flight.trailing) {
        // 在途执行结束后再跑一次；若恰好有别的调用已先一步开启了新的执行，则直接加入它（它一定晚于本次请求开始）。
        flight.trailing = flight.running.then(() => flights.get(key)?.running ?? start(key))
      }
      return flight.trailing
    },
    isBusy(key) {
      return flights.has(key)
    },
  }
}

// ---------------------------------------------------------------------------
// 合并调度
// ---------------------------------------------------------------------------

/** 需要校准的数据范围：当前待反馈会话 / 简略会话列表 / 待发送队列 / 选中工作流的完整多轮列表。 */
export interface SyncScope {
  current?: boolean
  sessions?: boolean
  queued?: boolean
  workflow?: boolean
}

export type ResolvedSyncScope = Required<SyncScope>

export const FULL_SYNC_SCOPE: ResolvedSyncScope = { current: true, sessions: true, queued: true, workflow: true }

function emptyScope(): ResolvedSyncScope {
  return { current: false, sessions: false, queued: false, workflow: false }
}

function hasAnyScope(scope: ResolvedSyncScope): boolean {
  return scope.current || scope.sessions || scope.queued || scope.workflow
}

export interface SyncSchedulerOptions {
  /** 最后一次请求之后等待多久再执行，默认 120ms。 */
  delayMs?: number
  /** 从窗口内第一次请求起最久等待多久，避免持续不断的事件把校准无限推迟，默认 1000ms。 */
  maxWaitMs?: number
  now?: () => number
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (handle: unknown) => void
}

export interface SyncScheduler {
  schedule(scope: SyncScope): void
  flushNow(): void
  cancel(): void
}

/** 合并同一时间窗口内的多次校准请求：范围取并集，去抖 + 最大等待，到点只执行一次。 */
export function createSyncScheduler(flush: (scope: ResolvedSyncScope) => void, options: SyncSchedulerOptions = {}): SyncScheduler {
  const delayMs = options.delayMs ?? 120
  const maxWaitMs = options.maxWaitMs ?? 1000
  const now = options.now ?? (() => Date.now())
  const setTimer = options.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms))
  const clearTimer = options.clearTimer ?? ((handle: unknown) => clearTimeout(handle as ReturnType<typeof setTimeout>))

  let pending = emptyScope()
  let timer: unknown = null
  let firstAt: number | null = null

  function run() {
    if (timer !== null) {
      clearTimer(timer)
      timer = null
    }
    const scope = pending
    pending = emptyScope()
    firstAt = null
    if (hasAnyScope(scope)) flush(scope)
  }

  return {
    schedule(scope) {
      pending = {
        current: pending.current || !!scope.current,
        sessions: pending.sessions || !!scope.sessions,
        queued: pending.queued || !!scope.queued,
        workflow: pending.workflow || !!scope.workflow,
      }
      if (!hasAnyScope(pending)) return
      const t = now()
      if (firstAt === null) firstAt = t
      if (timer !== null) {
        clearTimer(timer)
        timer = null
      }
      if (t - firstAt >= maxWaitMs) {
        run()
        return
      }
      timer = setTimer(run, delayMs)
    },
    flushNow: run,
    cancel() {
      if (timer !== null) {
        clearTimer(timer)
        timer = null
      }
      pending = emptyScope()
      firstAt = null
    },
  }
}

// ---------------------------------------------------------------------------
// 离开时长与缓存新鲜度
// ---------------------------------------------------------------------------

export interface AwayTracker {
  /** 页面隐藏或窗口失焦时调用；重复调用保留最早的时间点。 */
  markInactive(): void
  /** 页面重新可见或窗口重新聚焦时调用；返回这次离开是否久到需要校准，并复位状态。 */
  markActive(): boolean
}

/**
 * 跟踪页面「离开」的时长。没有先离开就收到的 focus / visibilitychange（例如窗口内部焦点切换）
 * 不会被当成久别重逢，因此不会触发校准。
 */
export function createAwayTracker(thresholdMs = 30_000, now: () => number = () => Date.now()): AwayTracker {
  let inactiveSince = 0
  return {
    markInactive() {
      if (inactiveSince === 0) inactiveSince = now()
    },
    markActive() {
      const away = inactiveSince === 0 ? 0 : now() - inactiveSince
      inactiveSince = 0
      return away >= thresholdMs
    },
  }
}

/**
 * 缓存是否仍然可信：同步发生在最近一次 SSE 断线之后，且没有超过兜底 TTL。
 * syncedAt 应取「请求开始时间」——请求在途期间的 SSE 事件由 touched 合并兜住，而断线则必须让缓存失效。
 */
export function isCacheFresh(syncedAt: number | undefined, lastGapAt: number, now: number, ttlMs: number): boolean {
  if (!syncedAt) return false
  if (syncedAt <= lastGapAt) return false
  return now - syncedAt < ttlMs
}
