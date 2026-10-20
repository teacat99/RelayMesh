import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  UnlockCancelled,
  setUnlockToken,
  terminalApi,
  toApiError,
  type AuditLog,
  type DiscoveryView,
  type GuardItem,
  type GuardView,
  type GuardWakePlan,
  type HistoryEntry,
  type HostDefault,
  type HostGrantResult,
  type HostView,
  type LauncherOtherRun,
  type LauncherRunPlan,
  type LauncherRunView,
  type PendingView,
  type SessionEnableRequest,
  type SessionEnableResult,
  type SessionInfo,
  type SshAddBody,
  type SshConnBody,
  type SshStatus,
  type TerminalLauncher,
  type TerminalPrefsView,
  type TerminalStatus,
  type UninstallOutcome,
  type UpgradeJobView,
} from '../api/terminal'
import { withFreshUnlock, withUnlockOnDemand } from '../lib/terminal/unlockRetry'
import { hostTitle } from '../lib/terminal/format'
import { guardAttention, type GuardAttention } from '../lib/terminal/guard'
import { runPollDelay } from '../lib/terminal/launcherRun'
import { finishedUpgrades } from '../lib/terminal/upgrade'
import { failureTitle, finishedWebUpgrades, jobIsAuto, jobKindLabel } from '../lib/terminal/webUpgrade'

export interface TermTab {
  key: string
  code: string
  sid: string
  /** 只读的记录回放标签：不连接会话、不写入浏览器本地存储。 */
  replay?: boolean
}

/** 一台主机的已结束会话历史（索引来自 Hub 数据库，内容留在主机上，按需读取）。 */
export interface HistoryState {
  loaded: boolean
  loading: boolean
  /** 加载时主机是否在线（离线时仍可看索引，但读不了内容）。 */
  online: boolean
  sessions: HistoryEntry[]
  error: string
}

const TABS_KEY = 'relaymesh_term_tabs'
const HOSTS_POLL_MS = 5_000
const STATUS_POLL_MS = 15_000
/** 会话守护的状态不需要秒级：服务端一分钟才判断一次，这里半分钟读一次足够。 */
const GUARD_POLL_MS = 30_000
const KEEPALIVE_CHECK_MS = 60_000
/** 用户在终端里有过操作的这段时间内，才会替他延长解锁有效期。 */
const ACTIVITY_WINDOW_MS = 5 * 60_000

export function tabKey(code: string, sid: string): string {
  return `${code}:${sid}`
}

export function replayKey(code: string, sid: string): string {
  return `replay:${code}:${sid}`
}

/** 主机上「运行中会话集合」的指纹：变化说明有会话新建 / 结束，需要重读历史。 */
function sessionsFingerprint(h: HostView): string {
  return h.sessions.map(s => `${s.sid}:${s.state}`).join(',')
}

function readTabs(): { tabs: TermTab[]; active: string } {
  try {
    const raw = JSON.parse(localStorage.getItem(TABS_KEY) || 'null') as { tabs?: unknown; active?: unknown } | null
    const list = Array.isArray(raw?.tabs) ? raw!.tabs : []
    const tabs: TermTab[] = []
    const seen = new Set<string>()
    for (const t of list) {
      const o = t as { code?: unknown; sid?: unknown }
      if (typeof o?.code !== 'string' || typeof o?.sid !== 'string' || o.code.length > 40 || o.sid.length > 64) continue
      const key = tabKey(o.code, o.sid)
      if (seen.has(key)) continue
      seen.add(key)
      tabs.push({ key, code: o.code, sid: o.sid })
    }
    const active = typeof raw?.active === 'string' && seen.has(raw.active) ? raw.active : (tabs[0]?.key ?? '')
    return { tabs: tabs.slice(0, 16), active }
  } catch {
    return { tabs: [], active: '' }
  }
}

export const useTerminalStore = defineStore('terminal', () => {
  // ---------- 总闸与状态 ----------
  const status = ref<TerminalStatus | null>(null)
  const statusError = ref('')
  const gateOpen = computed(() => status.value?.enabled === true)
  const gateReason = computed(() => status.value?.reason ?? 'ok')
  /** SSH 后端的状态；服务端没有返回（旧版本 / 闸门关闭）时为 null。 */
  const sshStatus = computed<SshStatus | null>(() => status.value?.ssh ?? null)
  /** 终端偏好（宽度保持、终端限高）：保存在服务端，所有浏览器共享；旧版服务端 / 闸门关闭时为 null。 */
  const prefs = computed<TerminalPrefsView | null>(() => status.value?.prefs ?? null)
  /** 固定列数（宽度保持）：0 表示关闭，终端宽度跟随窗口。 */
  const fixedCols = computed(() => prefs.value?.fixed_cols ?? 0)
  /** Agent 自动升级总闸；undefined = 这台 Hub 不认识自动升级（旧版本）。 */
  const agentAutoUpgrade = computed<boolean | undefined>(() => prefs.value?.agent_auto_upgrade)
  /** 终端启动器的预设（服务端保存，所有浏览器共享）。 */
  const launchers = computed<TerminalLauncher[]>(() => prefs.value?.launchers ?? [])

  // ---------- 主机 ----------
  const hosts = ref<HostView[]>([])
  const pending = ref<PendingView[]>([])
  const discovery = ref<DiscoveryView | null>(null)
  const hostsLoaded = ref(false)
  const hostsError = ref('')
  const history = ref<Record<string, HistoryState>>({})
  const fingerprints = new Map<string, string>()
  /** 已归档的侧栏条目（服务端保存，随主机列表一起下发）；key 与侧栏条目的 key 一致。 */
  const archivedKeys = ref<ReadonlySet<string>>(new Set())
  // 归档是「先改界面、再通知服务端」。期间如果一次更早发起的轮询带着旧数据回来，不能把刚归档的条目又变回去：
  // 轮询开始之后只要归档状态动过（发起或结束一次操作），这一轮的归档列表就丢弃，下一轮再对齐。
  let archiveEpoch = 0
  let archiveInflight = 0

  // ---------- 解锁（令牌只存内存，刷新页面即失效） ----------
  const unlockToken = ref('')
  const unlockedUntil = ref(0)
  let absoluteUntil = 0
  const idleSeconds = ref(900)
  const now = ref(Date.now())
  let clockTimer: ReturnType<typeof setInterval> | null = null

  const unlockRemaining = computed(() => (unlockToken.value ? Math.max(0, Math.floor((unlockedUntil.value - now.value) / 1000)) : 0))
  const isUnlocked = computed(() => unlockToken.value !== '' && unlockRemaining.value > 0)

  function startClock() {
    if (clockTimer) return
    clockTimer = setInterval(() => {
      now.value = Date.now()
      if (unlockToken.value && unlockedUntil.value <= now.value) clearUnlock()
    }, 1000)
  }
  function stopClock() {
    if (clockTimer) clearInterval(clockTimer)
    clockTimer = null
  }

  function clearUnlock() {
    unlockToken.value = ''
    unlockedUntil.value = 0
    absoluteUntil = 0
    setUnlockToken('')
    stopClock()
  }

  /** 每次成功的「需要解锁」请求都会让服务端顺延空闲期，本地估计同步顺延（不超过绝对期限）。 */
  function touchUnlock() {
    if (!unlockToken.value) return
    const idleEnd = Date.now() + idleSeconds.value * 1000
    unlockedUntil.value = absoluteUntil > 0 ? Math.min(idleEnd, absoluteUntil) : idleEnd
  }

  const unlockDialogOpen = ref(false)
  const unlockReason = ref('')
  let unlockWaiters: ((ok: boolean) => void)[] = []

  /** force：即使已经解锁也要重新输入密码（用来换一枚「刚刚验证过」的令牌）；取消时旧令牌保持有效。 */
  function requestUnlock(reason = '', force = false): Promise<boolean> {
    if (!force && isUnlocked.value) return Promise.resolve(true)
    unlockReason.value = reason
    unlockDialogOpen.value = true
    return new Promise(resolve => unlockWaiters.push(resolve))
  }

  function settleUnlock(ok: boolean) {
    unlockDialogOpen.value = false
    const waiters = unlockWaiters
    unlockWaiters = []
    for (const w of waiters) w(ok)
  }

  /** 用登录密码换取解锁令牌。失败抛 TerminalApiError（unlock_failed / unlock_locked …）。 */
  async function unlock(password: string): Promise<void> {
    const res = await terminalApi.unlock(password)
    idleSeconds.value = res.idle_seconds > 0 ? res.idle_seconds : 900
    absoluteUntil = res.expires_at ? Date.parse(res.expires_at) || 0 : 0
    unlockToken.value = res.token
    setUnlockToken(res.token)
    touchUnlock()
    now.value = Date.now()
    startClock()
    settleUnlock(true)
  }

  async function lock(): Promise<void> {
    try {
      await terminalApi.lock()
    } catch {
      /* 即使服务端没有收到，本地也要立刻丢弃令牌 */
    }
    clearUnlock()
    await loadStatus()
  }

  /**
   * 执行一个需要解锁的操作：未解锁（或服务端说令牌已失效）就弹出解锁对话框，成功后自动重试一次。
   * 用户取消时抛 UnlockCancelled，调用方应当静默处理。
   */
  async function withUnlock<T>(fn: () => Promise<T>, reason = ''): Promise<T> {
    if (!isUnlocked.value && !(await requestUnlock(reason))) throw new UnlockCancelled()
    try {
      const out = await fn()
      touchUnlock()
      return out
    } catch (e) {
      const err = toApiError(e)
      if (err.code !== 'unlock_required') throw err
      clearUnlock()
      if (!(await requestUnlock(reason || '解锁已过期，请重新输入密码'))) throw new UnlockCancelled()
      const out = await fn()
      touchUnlock()
      return out
    }
  }

  /**
   * 先直接执行；服务端回复 unlock_required 才弹出解锁并重试一次。
   * 用于「是否需要解锁由服务端判断」的操作（令牌管理：只有授予 / 放宽终端能力才要求解锁）。
   */
  function unlockOnDemand<T>(fn: () => Promise<T>, reason = ''): Promise<T> {
    return withUnlockOnDemand(fn, {
      needsUnlock: e => toApiError(e).code === 'unlock_required',
      stale: clearUnlock,
      unlock: () => requestUnlock(reason || '这个操作需要再次验证密码'),
      cancelled: () => new UnlockCancelled(),
      done: touchUnlock,
    })
  }

  /**
   * 授予「允许整个文件系统」这类高风险权限：服务端要求解锁令牌是刚刚用密码换来的（unlock_fresh_required）。
   * force=true 表示调用方已知这次会授予，先强制弹出密码框再发请求；否则只在服务端要求时才弹出，并重试一次。
   * 用户取消时抛 UnlockCancelled，调用方应当静默处理（本次修改没有保存）。
   */
  function freshUnlock<T>(fn: () => Promise<T>, reason = '', opts: { force?: boolean } = {}): Promise<T> {
    const why = reason || '这个操作需要刚刚重新输入密码确认'
    return withFreshUnlock(fn, {
      codeOf: e => toApiError(e).code,
      reverify: () => requestUnlock(why, true),
      stale: clearUnlock,
      cancelled: () => new UnlockCancelled(),
      done: touchUnlock,
    }, opts)
  }

  // ---------- 终端偏好 ----------
  function applyPrefs(p: TerminalPrefsView) {
    if (status.value) status.value = { ...status.value, prefs: p }
  }

  /** 宽度保持：0 关闭，否则固定为该列数。登录即可；所有设备共用，其他浏览器在下一次状态轮询（约 15 秒）内跟随。 */
  async function saveFixedCols(cols: number): Promise<void> {
    applyPrefs((await terminalApi.setDisplayPrefs(cols)).prefs)
  }

  /** 终端限高：0 不按行数限制；否则每个会话至少保留最近这么多行，更早的在主机上滚动丢弃。需要解锁；用户取消时抛 UnlockCancelled。 */
  async function saveRecordMaxLines(lines: number): Promise<void> {
    const r = await withUnlock(() => terminalApi.setRetentionPrefs(lines), '修改终端保留行数会让主机丢弃更早的记录，需要再次验证密码')
    applyPrefs(r.prefs)
  }

  /** Agent 自动升级总闸。需要解锁（它决定主机上的 Agent 能不能向服务端问新版本）；用户取消解锁时抛 UnlockCancelled。 */
  async function saveAgentAutoUpgrade(enabled: boolean): Promise<void> {
    const r = await withUnlock(() => terminalApi.setAutoUpgradePrefs(enabled), '修改 Agent 自动升级总闸需要再次验证密码')
    applyPrefs(r.prefs)
  }

  /**
   * 保存一条终端启动预设（新建或覆盖）。需要解锁——预设决定人点「启动」后会运行什么；用户取消解锁时抛 UnlockCancelled。
   * 服务端校验不过（重名、目录不是完整路径……）抛带可读原因的 TerminalApiError，调用方就地显示。
   */
  async function saveLauncher(l: TerminalLauncher): Promise<void> {
    const r = await withUnlock(() => terminalApi.saveLauncher(l), '保存终端启动预设需要再次验证密码（预设决定点「启动」后会运行什么）')
    applyPrefs(r.prefs)
  }

  /** 删除一条终端启动预设。它已经不在了（别处删过）也算成功：以服务端最新的清单为准。 */
  async function deleteLauncher(id: string): Promise<void> {
    try {
      const r = await withUnlock(() => terminalApi.deleteLauncher(id), '删除终端启动预设需要再次验证密码')
      applyPrefs(r.prefs)
    } catch (e) {
      if (e instanceof UnlockCancelled || toApiError(e).code !== 'launcher_not_found') throw e
      await loadStatus()
    }
  }

  // ---------- 会话守护（D-214） ----------
  // 守护的状态来自 GET /terminal/guard（登录即可读）。入口按钮上的提示点和启动器清单共用这一份；
  // 只有存在「开了监测」的预设时才轮询；旧版 Hub 没有这个接口（404）：记下来，不再请求。
  const guard = ref<GuardView | null>(null)
  const guardUnsupported = ref(false)
  const guardItems = computed<GuardItem[]>(() => guard.value?.items ?? [])
  const guardWatching = computed(() => !guardUnsupported.value && launchers.value.some(l => l.guard?.enabled))
  /** 需要你处理的会话（意外结束 / 停摆）：入口按钮上的提示点用。 */
  const guardNeeds = computed<GuardAttention>(() => guardAttention(guardItems.value))
  let guardTimer: ReturnType<typeof setInterval> | null = null
  let guardUsers = 0

  async function refreshGuard(): Promise<void> {
    if (guardUnsupported.value) return
    try {
      guard.value = await terminalApi.guard()
    } catch (e) {
      // 网络抖动：保留上一次的结果；404 说明服务端还不认识守护，之后不用再问。
      if (toApiError(e).status === 404) guardUnsupported.value = true
    }
  }

  /** 开始定时读取守护状态（可重入；没有预设开着监测时不发请求）。 */
  function startGuardPolling() {
    guardUsers++
    if (guardUsers > 1) return
    guardTimer = setInterval(() => {
      if (!document.hidden && guardWatching.value) void refreshGuard()
    }, GUARD_POLL_MS)
    if (guardWatching.value) void refreshGuard()
  }

  function stopGuardPolling() {
    guardUsers = Math.max(0, guardUsers - 1)
    if (guardUsers > 0) return
    if (guardTimer) clearInterval(guardTimer)
    guardTimer = null
  }

  /**
   * 生成并登记一次唤醒，返回要执行的计划。需要解锁（它会把一句话输入到会话里，等于人在键盘上敲）；
   * 用户取消解锁时抛 UnlockCancelled。服务端只登记，真正去输入的是调用方（浏览器）。
   */
  async function wakeGuard(presetId: string, mode?: 'type' | 'relaunch' | 'headless'): Promise<GuardWakePlan> {
    const r = await withUnlock(() => terminalApi.wakeGuard(presetId, mode), '唤醒会话会把一句话输入到会话里（等于你在键盘上敲），需要再次验证密码')
    void refreshGuard()
    return r.plan
  }

  /** 标记 / 取消「任务全部完成」（唤醒许可 0 / 1）；需要解锁。 */
  async function setGuardPermit(presetId: string, permit: 0 | 1): Promise<void> {
    await withUnlock(
      () => terminalApi.setGuardPermit(presetId, permit),
      permit === 0 ? '标记任务全部完成需要再次验证密码' : '取消标记需要再次验证密码',
    )
    void refreshGuard()
  }

  // ---------- 非交互续聊预设的运行（#126） ----------
  // 点「运行」由服务端提交一次 `agent -p --resume …`（不开终端），预设清单要看到它跑完没有、结果是什么：
  // 弹窗开着期间轮询 GET /terminal/launchers/runs——有运行在进行时勤一点，否则慢一点；
  // 旧版 Hub 没有这个接口（404）：记下来，不再请求。
  // 同一个接口还带着不属于任何预设的运行（others，#147：旧入口、非交互唤醒、预设已删除后留下的），
  // 它们没有预设可挂，所以没有续聊预设时也要读：弹窗里的「其他非交互运行」区靠它。
  const launcherRuns = ref<Record<string, LauncherRunView>>({})
  const launcherOthers = ref<LauncherOtherRun[]>([])
  const launcherRunsUnsupported = ref(false)
  let runTimer: ReturnType<typeof setTimeout> | null = null
  let runUsers = 0

  async function refreshLauncherRuns(): Promise<void> {
    if (launcherRunsUnsupported.value) return
    try {
      const r = await terminalApi.launcherRuns()
      launcherRuns.value = r.runs ?? {}
      launcherOthers.value = r.others ?? []
    } catch (e) {
      // 网络抖动：保留上一次的结果；404 说明服务端还不认识这个接口，之后不用再问。
      if (toApiError(e).status === 404) launcherRunsUnsupported.value = true
    }
  }

  function scheduleRunPoll() {
    if (runUsers === 0) return
    runTimer = setTimeout(async () => {
      runTimer = null
      if (runUsers === 0) return
      if (!document.hidden) await refreshLauncherRuns()
      scheduleRunPoll()
    }, runPollDelay(launcherRuns.value, launcherOthers.value))
  }

  /** 开始定时读取预设运行与其他非交互运行的现状（可重入；旧版 Hub 没有这个接口时只试一次）。 */
  function startLauncherRunPolling() {
    runUsers++
    if (runUsers > 1) return
    void refreshLauncherRuns()
    scheduleRunPoll()
  }

  function stopLauncherRunPolling() {
    runUsers = Math.max(0, runUsers - 1)
    if (runUsers > 0) return
    if (runTimer) clearTimeout(runTimer)
    runTimer = null
  }

  /**
   * 取消一次进行中的运行（弹窗里「其他非交互运行」区的「取消」，#147）。取消是往安全方向的操作：登录即可，不需要解锁。
   * 无论成败都马上重读现状：运行刚好结束了（竞态）服务端会把最终记录原样返回，失败多半也是状态已经变了——清单要反映真实状态。
   */
  async function cancelOtherRun(runId: string): Promise<void> {
    try {
      await terminalApi.execCancel(runId)
    } finally {
      await refreshLauncherRuns()
    }
  }

  /**
   * 取消一次排期中的自动续跑（#151）：runId 是出事的那次运行。取消只需要登录，不需要解锁。
   * 无论成败都马上重读现状：多半是它刚好开跑了或已经作废——清单要反映真实状态。
   */
  async function cancelAutoResume(runId: string): Promise<void> {
    try {
      await terminalApi.cancelAutoResume(runId)
    } finally {
      await refreshLauncherRuns()
    }
  }

  /**
   * 运行一次「非交互续聊」预设：服务端直接提交（需要刚刚解锁——它会让主机运行命令），浏览器什么都不用执行。
   * 用户取消解锁时抛 UnlockCancelled；前置条件不满足（主机离线、对话还在运行、上一次还没跑完……）抛带可读原因的错误。
   */
  async function runLauncher(presetId: string): Promise<LauncherRunPlan> {
    const r = await withUnlock(() => terminalApi.runLauncher(presetId), '运行非交互续聊预设会让主机运行一次命令（接着原对话），需要再次验证密码')
    await refreshLauncherRuns()
    // 刚提交的运行立刻在清单里亮起来：读到的现状里还没有它（极短的竞态）时，先按计划补一条。
    if (launcherRuns.value[presetId]?.run_id !== r.run.run_id) {
      launcherRuns.value = {
        ...launcherRuns.value,
        [presetId]: {
          run_id: r.run.run_id,
          state: r.run.run_state,
          active: r.run.run_state === 'running' || r.run.run_state === 'awaiting_approval',
          created_at: new Date().toISOString(),
        },
      }
    }
    return r.run
  }

  /**
   * 「启用 Agent」（#176 第三期）：为「待接入」的会话建（或复用）一条非交互续聊预设并运行——在主机上无人值守地新开一个 Cursor CLI 对话。
   * 需要刚刚解锁（它会让主机运行命令）；用户取消解锁时抛 UnlockCancelled；前置条件不满足（不是待接入、已有智能体在运行、主机离线……）
   * 抛带可读原因的错误，服务端不会留下半成品预设。成功后预设清单与运行现状马上跟上服务端。
   */
  async function enableSessionAgent(body: SessionEnableRequest): Promise<SessionEnableResult> {
    const r = await withUnlock(
      () => terminalApi.sessionEnable(body),
      '启用 Agent 会让主机无人值守地运行一个 Cursor CLI 对话，需要再次验证密码',
    )
    applyPrefs(r.prefs)
    await refreshLauncherRuns()
    // 刚提交的运行立刻在清单里亮起来（读到的现状里还没有它的极短竞态）
    if (launcherRuns.value[r.preset.id]?.run_id !== r.run.run_id) {
      launcherRuns.value = {
        ...launcherRuns.value,
        [r.preset.id]: {
          run_id: r.run.run_id,
          state: r.run.run_state,
          active: r.run.run_state === 'running' || r.run.run_state === 'awaiting_approval',
          created_at: new Date().toISOString(),
        },
      }
    }
    return r
  }

  // ---------- 加载 ----------
  async function loadStatus(): Promise<void> {
    try {
      const s = await terminalApi.status()
      status.value = s
      statusError.value = ''
      if (!s.enabled) {
        clearUnlock()
        return
      }
      if (typeof s.unlock_idle_seconds === 'number' && s.unlock_idle_seconds > 0) idleSeconds.value = s.unlock_idle_seconds
      if (unlockToken.value) {
        if (s.unlocked) {
          unlockedUntil.value = Date.now() + (s.unlock_remaining_seconds ?? 0) * 1000
        } else {
          clearUnlock()
        }
      }
      if (s.discovery) discovery.value = s.discovery
    } catch (e) {
      // 网络抖动：保留上一次状态（界面继续可用）；只有从未成功读取过时才需要向用户报错。401 由全局拦截器处理。
      if (status.value === null) statusError.value = toApiError(e).message
    }
  }

  async function refreshHosts(): Promise<void> {
    const epoch = archiveEpoch
    try {
      const r = await terminalApi.hosts()
      const before = hosts.value
      hosts.value = r.hosts ?? []
      noteUpgradesFinished(before, hosts.value)
      pending.value = r.pending ?? []
      discovery.value = r.discovery ?? discovery.value
      if (epoch === archiveEpoch && archiveInflight === 0) archivedKeys.value = new Set(r.archived ?? [])
      hostsError.value = ''
      if (!hostsLoaded.value) {
        hostsLoaded.value = true
        pruneRestoredTabs()
      }
      noteSessionChanges()
    } catch (e) {
      const err = toApiError(e)
      hostsError.value = err.message
      if (err.code === 'terminal_disabled') await loadStatus()
    }
  }

  /**
   * 升级结束的提示，不管升级对话框是否还开着：
   *   - 在 Web 上一键发起的升级、以及主机自己发起的静默自动升级：同一个请求从进行中变成完成 / 失败时提示一次（失败带上原因）；
   *   - 升级窗口开着的主机换上了新版本（服务端同时自动关闭了窗口）：提示已升级——一键升级完成时窗口也会被自动关闭，
   *     那一次已经有上面的提示，这里不再重复。
   */
  function noteUpgradesFinished(before: readonly HostView[], after: readonly HostView[]) {
    const byWeb = new Set<string>()
    for (const { host, job } of finishedWebUpgrades(before, after)) {
      byWeb.add(host.machine_code)
      if (job.state === 'done') {
        toast.success(`${hostTitle(host)} ${jobIsAuto(job) ? '已自动升级到' : '已升级到'} ${job.to}`, { description: '新版本已连上服务端。' })
      } else {
        toast.error(`${hostTitle(host)} ${jobKindLabel(job)}没有成功`, { description: failureTitle(job.code) })
      }
    }
    for (const h of finishedUpgrades(before, after)) {
      if (byWeb.has(h.machine_code)) continue
      toast.success(`${hostTitle(h)} 已升级到 ${h.agent_version}`, { description: '检测到新版本已连接，这台主机的升级窗口已自动关闭。' })
    }
  }

  /** 已经加载过历史的主机，在它的运行中会话发生变化（新建 / 结束）时重新读取一次，让刚结束的会话尽快出现在历史里。 */
  function noteSessionChanges() {
    const seen = new Set<string>()
    for (const h of hosts.value) {
      seen.add(h.machine_code)
      const fp = sessionsFingerprint(h)
      const prev = fingerprints.get(h.machine_code)
      fingerprints.set(h.machine_code, fp)
      if (prev !== undefined && prev !== fp && history.value[h.machine_code]?.loaded) void loadHistory(h.machine_code, true)
    }
    for (const code of [...fingerprints.keys()]) {
      if (!seen.has(code)) {
        fingerprints.delete(code)
        delete history.value[code]
      }
    }
  }

  async function loadHistory(code: string, force = false): Promise<void> {
    const cur = history.value[code]
    if (cur?.loading || (cur?.loaded && !force)) return
    history.value[code] = { loaded: cur?.loaded ?? false, loading: true, online: cur?.online ?? false, sessions: cur?.sessions ?? [], error: '' }
    try {
      const r = await terminalApi.history(code)
      history.value[code] = { loaded: true, loading: false, online: r.online, sessions: r.sessions ?? [], error: '' }
    } catch (e) {
      const prev = history.value[code]
      history.value[code] = { loaded: prev?.loaded ?? false, loading: false, online: prev?.online ?? false, sessions: prev?.sessions ?? [], error: toApiError(e).message }
    }
  }

  let pollUsers = 0
  let hostsTimer: ReturnType<typeof setInterval> | null = null
  let statusTimer: ReturnType<typeof setInterval> | null = null
  let keepaliveTimer: ReturnType<typeof setInterval> | null = null
  let lastActivity = 0

  function onVisible() {
    if (!document.hidden) {
      void loadStatus()
      void refreshHosts()
    }
  }

  /** 进入终端页时调用（可重入）：开始轮询主机列表、总闸状态，并在用户活跃时给解锁令牌保活。 */
  function startPolling() {
    pollUsers++
    if (pollUsers > 1) return
    void loadStatus()
    void refreshHosts()
    hostsTimer = setInterval(() => {
      if (!document.hidden) void refreshHosts()
    }, HOSTS_POLL_MS)
    statusTimer = setInterval(() => {
      if (!document.hidden) void loadStatus()
    }, STATUS_POLL_MS)
    keepaliveTimer = setInterval(() => {
      void keepaliveIfActive()
    }, KEEPALIVE_CHECK_MS)
    document.addEventListener('visibilitychange', onVisible)
  }

  function stopPolling() {
    pollUsers = Math.max(0, pollUsers - 1)
    if (pollUsers > 0) return
    for (const t of [hostsTimer, statusTimer, keepaliveTimer]) if (t) clearInterval(t)
    hostsTimer = statusTimer = keepaliveTimer = null
    document.removeEventListener('visibilitychange', onVisible)
  }

  /** 终端里有键盘输入时调用（已节流的调用方可以放心高频调用）。 */
  function noteActivity() {
    lastActivity = Date.now()
  }

  async function keepaliveIfActive() {
    if (!isUnlocked.value) return
    if (Date.now() - lastActivity > ACTIVITY_WINDOW_MS) return
    // 剩余不足一半时才续，避免无谓请求。
    if (unlockRemaining.value > idleSeconds.value / 2) return
    try {
      const r = await terminalApi.keepalive()
      unlockedUntil.value = Date.now() + r.remaining_seconds * 1000
    } catch (e) {
      if (toApiError(e).code === 'unlock_required') clearUnlock()
    }
  }

  // ---------- 通知 ----------
  function notifyError(e: unknown, prefix = '') {
    if (e instanceof UnlockCancelled) return
    const err = toApiError(e)
    toast.error(prefix ? `${prefix}：${err.message}` : err.message)
  }

  // ---------- 主机操作 ----------
  async function setRemark(code: string, remark: string) {
    await terminalApi.setRemark(code, remark)
    await refreshHosts()
  }

  /**
   * 忽略主机页「有会话已不存在」的提示（单条给会话 ID 列表，'all' 是这台主机的全部）。
   * 先在本地去掉（点下去立刻有反馈），服务端确认后再刷新；失败时还原并把错误抛给调用方。
   */
  async function dismissLost(code: string, target: string[] | 'all') {
    const before = hosts.value
    hosts.value = before.map(h => {
      if (h.machine_code !== code || !h.holder?.lost?.length) return h
      const lost = target === 'all' ? [] : h.holder.lost.filter(l => !target.includes(l.sid))
      return { ...h, holder: { ...h.holder, lost } }
    })
    try {
      await terminalApi.dismissLost(code, target === 'all' ? { all: true } : { sids: target })
    } catch (e) {
      hosts.value = before
      throw e
    }
    await refreshHosts()
  }

  /** 启用主机；defaultForNewModels 为 allow 时按默认态授予现有模型令牌，返回授予结果（没有授予时为 undefined）。 */
  async function enableHost(code: string, remark?: string, defaultForNewModels?: HostDefault): Promise<HostGrantResult | undefined> {
    const r = await withUnlock(() => terminalApi.enable(code, remark, defaultForNewModels), '启用主机需要再次验证密码')
    await refreshHosts()
    return r.grant
  }

  /** 预览把一台主机授予现有模型令牌会改动哪些令牌（只读，不需要解锁）。 */
  async function previewGrantModels(code: string): Promise<HostGrantResult> {
    return (await terminalApi.grantModelsPreview(code)).grant
  }

  /** 把一台主机写入现有模型令牌的主机名单（需要解锁）。 */
  async function grantModelsToExisting(code: string): Promise<HostGrantResult> {
    const r = await withUnlock(() => terminalApi.grantModels(code), '把主机授予现有模型需要再次验证密码')
    await refreshHosts()
    return r.grant
  }

  async function ignoreHost(code: string) {
    await withUnlock(() => terminalApi.ignore(code), '忽略主机需要再次验证密码')
    await refreshHosts()
  }

  async function unignoreHost(code: string) {
    await withUnlock(() => terminalApi.unignore(code), '取消忽略需要再次验证密码')
    await refreshHosts()
  }

  async function disableHost(code: string) {
    await withUnlock(() => terminalApi.disable(code), '停用主机需要再次验证密码')
    closeTabsOfHost(code)
    await refreshHosts()
  }

  /** 删除主机；opts.uninstall 为 true 时同时请求设备端卸载，返回设备上发生了什么（没有请求则为 undefined）。 */
  async function deleteHost(code: string, opts?: { uninstall?: boolean }): Promise<UninstallOutcome | undefined> {
    const res = await withUnlock(() => terminalApi.remove(code, opts), '删除主机需要再次验证密码')
    closeTabsOfHost(code)
    await refreshHosts()
    return res.uninstall
  }

  // ---------- SSH 主机（人工终端 + 模型执行；登记与改连接信息会让 Hub 或网关能登录远端，所以都要二次解锁） ----------
  const SSH_UNLOCK_REASON = '管理 SSH 主机需要再次验证密码'

  async function sshProbe(address: string, gateway?: string) {
    return (await withUnlock(() => terminalApi.sshProbe(address, gateway), SSH_UNLOCK_REASON)).probe
  }

  /** 接入 SSH 主机；grant 说明按默认态授予了哪些模型令牌（没有授予时为 undefined）。 */
  async function sshAdd(body: SshAddBody) {
    const r = await withUnlock(() => terminalApi.sshAdd(body), SSH_UNLOCK_REASON)
    await refreshHosts()
    return { host: r.host, grant: r.grant }
  }

  async function sshTest(code: string) {
    try {
      return (await withUnlock(() => terminalApi.sshTest(code), SSH_UNLOCK_REASON)).result
    } finally {
      // 测试结果会写进主机的健康状态；无论成败都刷新一次列表。
      void refreshHosts()
    }
  }

  async function sshProbeHost(code: string) {
    return (await withUnlock(() => terminalApi.sshProbeHost(code), SSH_UNLOCK_REASON)).probe
  }

  async function sshUpdateConnection(code: string, body: SshConnBody) {
    const r = await withUnlock(() => terminalApi.sshUpdateConnection(code, body), SSH_UNLOCK_REASON)
    await refreshHosts()
    return r.host
  }

  async function sshRekey(code: string, fingerprint: string) {
    const r = await withUnlock(() => terminalApi.sshRekey(code, fingerprint), SSH_UNLOCK_REASON)
    await refreshHosts()
    return r.host
  }

  /** 打开发现窗口；extend=true 是「续期」：在剩余时间上累加，不会让已签发的安装命令失效。 */
  async function openDiscovery(minutes: number, extend = false) {
    const r = await withUnlock(
      () => terminalApi.openDiscovery(minutes, extend),
      extend ? '续期发现窗口需要再次验证密码' : '开启发现窗口需要再次验证密码',
    )
    discovery.value = r.discovery
    // 安装票据随发现窗口而来，写在 /status 里：窗口一开就同步刷新，安装命令才能立刻生成。
    await Promise.all([refreshHosts(), loadStatus()])
  }

  async function closeDiscovery() {
    const r = await withUnlock(() => terminalApi.closeDiscovery(), '关闭发现窗口需要再次验证密码')
    discovery.value = r.discovery
    await loadStatus()
  }

  /** 开启一台主机的升级窗口；extend=true 是「延时」：在剩余时间上累加，已经生成的升级命令继续有效。 */
  async function openUpgradeWindow(code: string, minutes?: number, extend = false) {
    const r = await withUnlock(
      () => terminalApi.openUpgradeWindow(code, minutes, extend),
      extend ? '延时升级窗口需要再次验证密码' : '开启升级窗口需要再次验证密码',
    )
    await refreshHosts()
    return r.upgrade_window
  }

  /** 提前结束升级窗口：票据立刻失效。 */
  async function closeUpgradeWindow(code: string) {
    await withUnlock(() => terminalApi.closeUpgradeWindow(code), '结束升级窗口需要再次验证密码')
    await refreshHosts()
  }

  /**
   * 请这台主机的 Agent 自己升级（需要主机所有者已在本机允许）。有正在运行的会话或模型运行时服务端会先回 409 sessions_active，
   * 调用方确认后带上 confirmSessions 再调一次；其余失败原因（不在线、不支持、已是最新……）服务端都给出了可读的说明。
   */
  async function webUpgrade(code: string, confirmSessions = false): Promise<UpgradeJobView> {
    const r = await withUnlock(() => terminalApi.webUpgrade(code, confirmSessions), '一键升级需要再次验证密码')
    await refreshHosts()
    return r.upgrade_job
  }

  // ---------- 会话操作 ----------
  async function createSession(
    code: string,
    name?: string,
    size?: { rows: number; cols: number },
    opts?: { incognito?: boolean; idleSeconds?: number; cwd?: string; open?: boolean; sshTarget?: string; compact?: boolean },
  ): Promise<SessionInfo> {
    const r = await withUnlock(
      () =>
        terminalApi.openSession(code, {
          name: name || undefined,
          rows: size?.rows,
          cols: size?.cols,
          incognito: opts?.incognito || undefined,
          idle_seconds: opts?.idleSeconds,
          cwd: opts?.cwd || undefined,
          compact: opts?.compact || undefined,
          ssh_target: opts?.sshTarget || undefined,
        }),
      opts?.incognito ? '新建无痕会话需要再次验证密码' : '新建会话需要再次验证密码',
    )
    await refreshHosts()
    if (opts?.open !== false) openTab(code, r.session.sid)
    return r.session
  }

  /**
   * 为一台 SSH 主机打开人工终端，名称默认取主机备注。两条接入路径对调用方没有区别：
   *   经网关：会话运行在网关主机上（由网关出站连接目标），凭据只在 Hub → 网关这一跳短暂出现；
   *   Hub 直连：会话由 Hub 持有（Hub 出站连接目标），不做记录，所以没有「无痕」可言，incognito 被忽略。
   * 浏览器从不接触登录凭据。
   */
  async function openSshTerminal(host: HostView, opts?: { incognito?: boolean; open?: boolean }): Promise<SessionInfo> {
    if (host.kind !== 'ssh') throw new Error('这不是 SSH 主机')
    const gateway = host.ssh?.gateway
    if (!gateway) return createSession(host.machine_code, undefined, undefined, { open: opts?.open })
    return createSession(gateway, undefined, undefined, { incognito: opts?.incognito, open: opts?.open, sshTarget: host.machine_code })
  }

  /** 修改一台主机的默认生命周期（需要解锁）。 */
  async function setHostLifecycle(code: string, idleSeconds: number) {
    await withUnlock(() => terminalApi.setHostLifecycle(code, idleSeconds), '修改生命周期需要再次验证密码')
    await refreshHosts()
  }

  /** 修改一个会话自己的生命周期（需要解锁）；null = 沿用主机默认。 */
  async function setSessionLifecycle(code: string, sid: string, idleSeconds: number | null) {
    await withUnlock(() => terminalApi.setSessionLifecycle(code, sid, idleSeconds), '修改生命周期需要再次验证密码')
    // 会话清单由主机稍后推送；稍等片刻再读取，界面上的徽标才会跟上。
    setTimeout(() => void refreshHosts(), 500)
  }

  /** 开关一台主机的终端记录（服务端策略；需要解锁）。 */
  async function setRecording(code: string, off: boolean) {
    await withUnlock(() => terminalApi.setRecording(code, off), off ? '关闭终端记录需要再次验证密码' : '开启终端记录需要再次验证密码')
    await refreshHosts()
  }

  async function renameSession(code: string, sid: string, name: string) {
    await terminalApi.renameSession(code, sid, name)
    await refreshHosts()
  }

  /**
   * 归档 / 取消归档侧栏条目。界面立刻生效，失败时只回滚这次涉及的条目（不碰期间其他的改动）并提示原因。
   * 归档只影响侧栏显示，不会结束会话；不需要解锁（和重命名一样）。
   */
  async function setArchived(keys: string[], archived: boolean): Promise<boolean> {
    const list = [...new Set(keys.filter(Boolean))]
    if (list.length === 0) return true
    const before = archivedKeys.value
    const next = new Set(before)
    for (const k of list) {
      if (archived) next.add(k)
      else next.delete(k)
    }
    archivedKeys.value = next
    archiveEpoch++
    archiveInflight++
    try {
      await terminalApi.archive(list, archived)
      return true
    } catch (e) {
      const cur = new Set(archivedKeys.value)
      for (const k of list) {
        if (before.has(k)) cur.add(k)
        else cur.delete(k)
      }
      archivedKeys.value = cur
      toast.error(archived ? '归档失败' : '取消归档失败', { description: toApiError(e).message })
      return false
    } finally {
      archiveInflight--
      archiveEpoch++
      void refreshHosts()
    }
  }

  async function killSession(code: string, sid: string) {
    await withUnlock(() => terminalApi.killSession(code, sid), '终止会话需要再次验证密码')
    // kill 只是通知 Agent，会话清单稍后才会更新。
    setTimeout(() => void refreshHosts(), 700)
  }

  /** 供连接层使用：申请一次性 WebSocket 票据；需要时会弹出解锁对话框。 */
  async function fetchTicket(
    code: string,
    sid: string,
    req: { since: number; rows: number; cols: number; tailLines?: number },
  ): Promise<string> {
    const { tailLines, ...rest } = req
    const body = { ...rest, ...(tailLines ? { tail_lines: tailLines } : {}) }
    const r = await withUnlock(() => terminalApi.ticket(code, sid, body), '连接终端需要再次验证密码')
    return r.ticket
  }

  async function loadAudit(before?: number, limit = 100): Promise<AuditLog[]> {
    const r = await terminalApi.audit({ limit, before })
    return r.logs ?? []
  }

  // ---------- 标签页（= 浏览器里正在附着的会话；关闭只是分离，会话仍在主机上运行） ----------
  const restored = readTabs()
  const tabs = ref<TermTab[]>(restored.tabs)
  const activeKey = ref(restored.active)
  let tabsNeedPrune = restored.tabs.length > 0

  function persistTabs() {
    try {
      // 回放标签是临时的：不写入本地存储，刷新页面后不会恢复。
      const live = tabs.value.filter(t => !t.replay)
      const active = live.some(t => t.key === activeKey.value) ? activeKey.value : (live[0]?.key ?? '')
      localStorage.setItem(TABS_KEY, JSON.stringify({ tabs: live.map(t => ({ code: t.code, sid: t.sid })), active }))
    } catch {
      /* 隐私模式等：放弃持久化 */
    }
  }

  function openTab(code: string, sid: string) {
    const key = tabKey(code, sid)
    if (!tabs.value.some(t => t.key === key)) {
      if (tabs.value.length >= 16) tabs.value.shift()
      tabs.value.push({ key, code, sid })
    }
    activeKey.value = key
    persistTabs()
  }

  /** 只加入标签、不切换过去（并排时放到右窗格用）。返回标签 key。 */
  function openTabBackground(code: string, sid: string): string {
    const key = tabKey(code, sid)
    if (!tabs.value.some(t => t.key === key)) {
      if (tabs.value.length >= 16) tabs.value.shift()
      tabs.value.push({ key, code, sid })
      if (!tabs.value.some(t => t.key === activeKey.value)) activeKey.value = key
      persistTabs()
    }
    return key
  }

  /** 打开一个只读的记录回放标签（已结束的会话，或正在运行会话到目前为止的记录）。 */
  function openReplay(code: string, sid: string) {
    const key = replayKey(code, sid)
    if (!tabs.value.some(t => t.key === key)) {
      if (tabs.value.length >= 16) tabs.value.shift()
      tabs.value.push({ key, code, sid, replay: true })
    }
    activeKey.value = key
    persistTabs()
  }

  function activateTab(key: string) {
    if (tabs.value.some(t => t.key === key)) {
      activeKey.value = key
      persistTabs()
    }
  }

  function closeTab(key: string) {
    const idx = tabs.value.findIndex(t => t.key === key)
    if (idx < 0) return
    tabs.value.splice(idx, 1)
    if (activeKey.value === key) activeKey.value = tabs.value[Math.min(idx, tabs.value.length - 1)]?.key ?? ''
    persistTabs()
  }

  function closeTabsOfHost(code: string) {
    const keep = tabs.value.filter(t => t.code !== code)
    if (keep.length === tabs.value.length) return
    tabs.value = keep
    if (!keep.some(t => t.key === activeKey.value)) activeKey.value = keep[0]?.key ?? ''
    persistTabs()
  }

  /** 首次拿到主机列表后，丢掉指向已删除 / 已停用主机的标签；离线主机的标签保留（它们会自己等待重连）。 */
  function pruneRestoredTabs() {
    if (!tabsNeedPrune) return
    tabsNeedPrune = false
    const alive = new Set(hosts.value.filter(h => h.status === 'enabled').map(h => h.machine_code))
    const keep = tabs.value.filter(t => alive.has(t.code))
    if (keep.length !== tabs.value.length) {
      tabs.value = keep
      if (!keep.some(t => t.key === activeKey.value)) activeKey.value = keep[0]?.key ?? ''
      persistTabs()
    }
  }

  function hostByCode(code: string): HostView | undefined {
    return hosts.value.find(h => h.machine_code === code)
  }

  function sessionOf(code: string, sid: string): SessionInfo | undefined {
    return hostByCode(code)?.sessions.find(s => s.sid === sid)
  }

  return {
    // 状态
    status,
    statusError,
    gateOpen,
    gateReason,
    sshStatus,
    prefs,
    fixedCols,
    agentAutoUpgrade,
    saveFixedCols,
    saveRecordMaxLines,
    saveAgentAutoUpgrade,
    launchers,
    saveLauncher,
    deleteLauncher,
    guard,
    guardItems,
    guardWatching,
    guardNeeds,
    guardUnsupported,
    refreshGuard,
    startGuardPolling,
    stopGuardPolling,
    wakeGuard,
    setGuardPermit,
    launcherRuns,
    launcherOthers,
    cancelOtherRun,
    cancelAutoResume,
    refreshLauncherRuns,
    startLauncherRunPolling,
    stopLauncherRunPolling,
    runLauncher,
    enableSessionAgent,
    hosts,
    pending,
    discovery,
    hostsLoaded,
    hostsError,
    history,
    loadHistory,
    archivedKeys,
    setArchived,
    // 解锁
    isUnlocked,
    unlockRemaining,
    idleSeconds,
    unlockDialogOpen,
    unlockReason,
    requestUnlock,
    settleUnlock,
    unlock,
    lock,
    withUnlock,
    unlockOnDemand,
    freshUnlock,
    noteActivity,
    // 加载
    loadStatus,
    refreshHosts,
    startPolling,
    stopPolling,
    notifyError,
    // 主机 / 会话
    setRemark,
    dismissLost,
    enableHost,
    previewGrantModels,
    grantModelsToExisting,
    ignoreHost,
    unignoreHost,
    disableHost,
    deleteHost,
    sshProbe,
    sshAdd,
    sshTest,
    sshProbeHost,
    sshUpdateConnection,
    sshRekey,
    openDiscovery,
    closeDiscovery,
    openUpgradeWindow,
    closeUpgradeWindow,
    webUpgrade,
    createSession,
    openSshTerminal,
    setHostLifecycle,
    setSessionLifecycle,
    renameSession,
    setRecording,
    killSession,
    fetchTicket,
    loadAudit,
    hostByCode,
    sessionOf,
    // 标签页
    tabs,
    activeKey,
    openTab,
    openTabBackground,
    openReplay,
    activateTab,
    closeTab,
    closeTabsOfHost,
  }
})
