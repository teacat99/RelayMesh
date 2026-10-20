import { defineStore } from 'pinia'
import { computed, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import type { SessionInfo } from '../api/terminal'
import {
  archivedMatchCount,
  archivedStats,
  buildExecItems,
  filterExecItems,
  groupExecItems,
  runningCount,
  sortExecItems,
  type ExecFilter,
} from '../lib/terminal/execList'
import { LIVE_CAP_DEFAULT, clampLiveCap, evictedKeys, pruneLive, shrinkLive, touchLive } from '../lib/terminal/liveSet'
import { SPLIT_OFF, SPLIT_RATIO_DEFAULT, afterPrimaryChange, clampSplitRatio, pickSecondary, type SplitState } from '../lib/terminal/split'
import { useExecRunsStore } from './execRuns'
import { useTerminalStore } from './terminal'

const PINS_KEY = 'relaymesh_exec_pins'
const THREAD_NAMES_KEY = 'relaymesh_exec_thread_names'
const COLLAPSE_KEY = 'relaymesh_exec_sidebar_collapsed'
const LIVE_CAP_KEY = 'relaymesh_exec_live_cap'
const SPLIT_RATIO_KEY = 'relaymesh_exec_split_ratio'
const MAX_PINS = 200
const MAX_THREAD_NAMES = 200

function readNumber(key: string): unknown {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeNumber(key: string, v: number) {
  try {
    localStorage.setItem(key, String(v))
  } catch {
    /* 隐私模式等：放弃持久化 */
  }
}

function readPins(): string[] {
  try {
    const raw = JSON.parse(localStorage.getItem(PINS_KEY) || '[]') as unknown
    return Array.isArray(raw) ? raw.filter((x): x is string => typeof x === 'string' && x.length <= 120).slice(-MAX_PINS) : []
  } catch {
    return []
  }
}

function readThreadNames(): Record<string, string> {
  try {
    const raw = JSON.parse(localStorage.getItem(THREAD_NAMES_KEY) || '{}') as unknown
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof v === 'string' && v.trim() && k.length <= 64) out[k] = v.slice(0, 64)
    }
    return out
  } catch {
    return {}
  }
}

/**
 * ExecMesh 的界面状态：侧栏的置顶 / 筛选 / 搜索 / 折叠，以及几个由多处触发的对话框。
 * 终端数据（主机、会话、历史、解锁）仍在 terminal store，这里只负责把它们组织成侧栏清单。
 */
export const useExecStore = defineStore('exec', () => {
  const terminal = useTerminalStore()
  const execRuns = useExecRunsStore()

  const pins = ref(new Set<string>(readPins()))
  /** 线程的自定义名称（只存在本浏览器里，线程键不变）。 */
  const threadNames = ref<Record<string, string>>(readThreadNames())
  const filter = ref<ExecFilter>('all')
  const query = ref('')
  const sidebarCollapsed = ref(localStorage.getItem(COLLAPSE_KEY) === 'true')
  const mobileSidebarOpen = ref(false)
  const discoveryOpen = ref(false)
  const auditOpen = ref(false)

  // 分组标题（今天 / 昨天）与相对时间随时间推移而变，半分钟刷新一次即可。
  const now = ref(Date.now())
  let clock: ReturnType<typeof setInterval> | null = null
  function startClock() {
    if (clock) return
    clock = setInterval(() => {
      now.value = Date.now()
    }, 30_000)
  }
  function stopClock() {
    if (clock) clearInterval(clock)
    clock = null
  }

  // 每台主机最近一次在线时的运行中会话：主机离线期间继续显示它们（会话其实仍在主机上运行）。
  const lastKnown = ref<Record<string, SessionInfo[]>>({})
  watch(
    () => terminal.hosts,
    hosts => {
      const next = { ...lastKnown.value }
      for (const h of hosts) {
        if (h.status !== 'enabled') delete next[h.machine_code]
        else if (h.online) next[h.machine_code] = h.sessions.filter(s => s.state === 'running')
      }
      lastKnown.value = next
    },
    { immediate: true },
  )

  /** 全部条目（未过滤）。 */
  const items = computed(() =>
    buildExecItems({
      hosts: terminal.hosts,
      history: terminal.history,
      pins: pins.value,
      archived: terminal.archivedKeys,
      lastKnown: lastKnown.value,
      threads: execRuns.threads,
      threadNames: threadNames.value,
    }),
  )
  /** 过滤、排序、按日期分组之后给侧栏显示的清单。 */
  const groups = computed(() => groupExecItems(sortExecItems(filterExecItems(items.value, filter.value, query.value)), now.value))
  const visibleCount = computed(() => groups.value.reduce((n, g) => n + g.items.length, 0))
  const running = computed(() => runningCount(items.value))
  /** 已归档条目的数量，以及其中仍在运行的数量。 */
  const archivedInfo = computed(() => archivedStats(items.value))
  /** 当前搜索词在「已归档」里的命中数：当前筛选里搜不到时，提示用户去已归档里找。 */
  const archivedMatches = computed(() => archivedMatchCount(items.value, query.value))

  // 最后一个已归档条目被取消归档后，「已归档」筛选就没有内容了：自动退回「全部」，避免停在一个选不回来的空筛选上。
  watch(
    () => archivedInfo.value.total,
    n => {
      if (n === 0 && filter.value === 'archived') filter.value = 'all'
    },
  )

  /** 归档一批条目（只影响侧栏显示，会话照常运行），并给出带「撤销」的提示。 */
  async function archiveItems(targets: { key: string; title: string }[]) {
    if (targets.length === 0) return
    const keys = targets.map(t => t.key)
    if (!(await terminal.setArchived(keys, true))) return
    const label = targets.length === 1 ? `已归档「${targets[0].title.slice(0, 24)}」` : `已归档 ${targets.length} 个条目`
    toast(label, {
      description: '在侧栏顶部的「已归档」里可以找回',
      action: { label: '撤销', onClick: () => void terminal.setArchived(keys, false) },
    })
  }

  /** 取消归档，条目回到侧栏原来的位置。 */
  function unarchiveItems(keys: string[]) {
    return terminal.setArchived(keys, false)
  }

  /** 给线程起名（空串 = 恢复默认名称）。 */
  function renameThread(threadKey: string, name: string) {
    const next = { ...threadNames.value }
    const v = name.trim().slice(0, 64)
    if (v) next[threadKey] = v
    else delete next[threadKey]
    const keys = Object.keys(next)
    if (keys.length > MAX_THREAD_NAMES) for (const k of keys.slice(0, keys.length - MAX_THREAD_NAMES)) delete next[k]
    threadNames.value = next
    try {
      localStorage.setItem(THREAD_NAMES_KEY, JSON.stringify(next))
    } catch {
      /* 隐私模式等：放弃持久化 */
    }
  }

  function persistPins() {
    try {
      localStorage.setItem(PINS_KEY, JSON.stringify([...pins.value]))
    } catch {
      /* 隐私模式等：放弃持久化 */
    }
  }

  // ---------- live 集合：保持附着的会话 ----------
  // 只有这里的会话才会挂载终端面板（占 WebSocket 与 xterm）；其余标签先断开，会话仍在主机上运行。
  const liveCap = ref(readNumber(LIVE_CAP_KEY) === null ? LIVE_CAP_DEFAULT : clampLiveCap(readNumber(LIVE_CAP_KEY)))
  const liveOrder = ref<string[]>([])
  /** 后台会话有了新输出、还没看过。键是标签 key（主机码:会话 ID）。 */
  const unread = reactive<Record<string, boolean>>({})

  function setLiveCap(n: number) {
    liveCap.value = clampLiveCap(n)
    writeNumber(LIVE_CAP_KEY, liveCap.value)
    // 调小后立刻收敛（正在显示的不动）。
    applyLive(shrinkLive(liveOrder.value, new Set(visibleKeys.value), liveCap.value))
  }

  /** 更新 live 集合；返回被淘汰的 key，由调用方清理它们的连接状态。 */
  function applyLive(next: string[]): string[] {
    const gone = evictedKeys(liveOrder.value, next)
    liveOrder.value = next
    return gone
  }

  // ---------- 并排（桌面） ----------
  const split = ref<SplitState>({ ...SPLIT_OFF })
  const splitRatio = ref(readNumber(SPLIT_RATIO_KEY) === null ? SPLIT_RATIO_DEFAULT : clampSplitRatio(readNumber(SPLIT_RATIO_KEY)))
  /** 侧栏菜单「在右侧打开」发来的请求，由终端页消费。 */
  const splitRequest = ref('')

  function setSplitRatio(v: number, persist = true) {
    splitRatio.value = clampSplitRatio(v)
    if (persist) writeNumber(SPLIT_RATIO_KEY, splitRatio.value)
  }

  /** 正在显示的标签：主窗格，加上并排时的右窗格。 */
  const visibleKeys = computed<string[]>(() => {
    const keys: string[] = []
    const a = terminal.activeKey
    const activeTab = terminal.tabs.find(t => t.key === a)
    if (a) keys.push(a)
    if (split.value.on && split.value.secondary && activeTab && !activeTab.replay) keys.push(split.value.secondary)
    return keys
  })

  /** 并排是否真的在显示（主窗格是终端标签，右窗格存在）。 */
  const splitShown = computed(() => visibleKeys.value.length === 2)

  function openSplit() {
    const primary = terminal.activeKey
    const secondary = pickSecondary(terminal.tabs, primary, liveOrder.value)
    if (!primary || !secondary) return false
    split.value = { on: true, secondary }
    return true
  }

  function closeSplit() {
    split.value = { ...SPLIT_OFF }
  }

  function toggleSplit() {
    if (split.value.on) {
      closeSplit()
      return true
    }
    return openSplit()
  }

  /** 把某个标签放到右窗格（它必须是终端标签）。 */
  function setSecondary(key: string) {
    const tab = terminal.tabs.find(t => t.key === key)
    if (!tab || tab.replay || key === terminal.activeKey) return false
    split.value = { on: true, secondary: key }
    return true
  }

  /** 关掉一个窗格：关右窗格就退出并排；关主窗格则右窗格升为主窗格。 */
  function closeSplitPane(key: string) {
    if (key === split.value.secondary) {
      closeSplit()
      return
    }
    const next = split.value.secondary
    closeSplit()
    if (next) terminal.activateTab(next)
  }

  // 主窗格变了（点标签、点侧栏、关标签……）：必要时互换或退出并排。
  watch(
    () => terminal.activeKey,
    (next, prev) => {
      const keys = new Set(terminal.tabs.filter(t => !t.replay).map(t => t.key))
      split.value = afterPrimaryChange(split.value, prev, next, keys)
    },
  )
  // 右窗格的标签被关掉了。
  watch(
    () => terminal.tabs.map(t => t.key).join('|'),
    () => {
      const keys = new Set(terminal.tabs.map(t => t.key))
      if (split.value.on && !keys.has(split.value.secondary)) closeSplit()
      const pruned = pruneLive(liveOrder.value, keys)
      if (pruned !== liveOrder.value) liveOrder.value = pruned
      for (const k of Object.keys(unread)) if (!keys.has(k)) delete unread[k]
    },
  )

  // 显示出来的会话进入 live 集合（并清掉「有新输出」标记）。
  watch(
    visibleKeys,
    keys => {
      const protectedKeys = new Set(keys)
      let order = liveOrder.value
      // 先放次要的，主窗格最后放，让它排在最前。
      for (const k of [...keys].reverse()) {
        order = touchLive(order, k, protectedKeys, liveCap.value)
        delete unread[k]
      }
      if (order.join('|') !== liveOrder.value.join('|')) liveOrder.value = order
    },
    { immediate: true },
  )

  function markUnread(key: string) {
    if (!visibleKeys.value.includes(key)) unread[key] = true
  }

  function togglePin(key: string) {
    const next = new Set(pins.value)
    if (!next.delete(key)) {
      next.add(key)
      while (next.size > MAX_PINS) next.delete(next.values().next().value as string)
    }
    pins.value = next
    persistPins()
  }

  function setSidebarCollapsed(v: boolean) {
    sidebarCollapsed.value = v
    try {
      localStorage.setItem(COLLAPSE_KEY, String(v))
    } catch {
      /* 同上 */
    }
  }

  function toggleSidebar() {
    setSidebarCollapsed(!sidebarCollapsed.value)
  }

  /** 把已启用主机的历史索引都读进来（索引在 Hub 数据库里，很轻；已读过的不会重复请求）。 */
  function ensureHistory() {
    for (const h of terminal.hosts) {
      if (h.status === 'enabled') void terminal.loadHistory(h.machine_code)
    }
  }

  return {
    pins,
    filter,
    query,
    sidebarCollapsed,
    mobileSidebarOpen,
    discoveryOpen,
    auditOpen,
    now,
    startClock,
    stopClock,
    items,
    groups,
    visibleCount,
    running,
    archivedInfo,
    archivedMatches,
    archiveItems,
    unarchiveItems,
    togglePin,
    threadNames,
    renameThread,
    setSidebarCollapsed,
    toggleSidebar,
    ensureHistory,
    liveCap,
    liveOrder,
    unread,
    setLiveCap,
    applyLive,
    markUnread,
    split,
    splitRatio,
    splitRequest,
    splitShown,
    visibleKeys,
    setSplitRatio,
    openSplit,
    closeSplit,
    toggleSplit,
    setSecondary,
    closeSplitPane,
  }
})
