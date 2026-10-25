// 外壳件的「上次占位」记忆（#246）。
//
// 背景：会话页有几处「数据到了才出现」的外壳件——页头下面的「项目任务」一行（任务清单要等整张会话表让路之后才取，见 lib/secondaryGate.ts），
// 与输入区状态条里一批依赖会话的芯片（最左边的 Agent 状态芯片要等实时流连上、取到 Agent 状态概览；回复窗口、收件箱、阶段滑块 / 授权档位、
// 常驻规则要等目标会话解析出来）。生产上它们总比第一屏晚到 0.2 ～ 1.5 秒，晚到的那一刻会话列表整体被推下去 30px、状态条里的预设按钮被推右
// 一整串芯片的宽度（约 670px，有时还折成两行）；浏览器还会把同一帧里新挂载的元素（右侧时间线导轨、末尾容器）按「从原点移过来」放大，
// 版面位移分数量到 0.3 ～ 0.85，而真实的位移只值 0.01 ～ 0.03。
//
// 做法：记住「这个会话上次有没有任务、状态芯片多宽、会话相关芯片整体多宽」，下次打开时数据还在路上就先把位置占住，到了原地换成真内容。
// 与 lib/sidebarDefault.ts 的 RUN_MODE_KEY 同一个思路（刷新时第一帧就按对的布局画）。记忆按工作流存在 localStorage，
// 只有几个小数字，没有任何业务内容，最多留最近改写过的 SHELL_HINT_MAX 个工作流（内容没变不重写，也就不刷新时间）。
// 第一次来某个会话没有记忆，仍会晚到一次——这是已知的一次性位移（e2e shell-shift 第 1 步记录它的大小）。
// 记忆可能过期（任务被删光了、Agent 不再汇报）：占位在数据取到之后收起，同时改写记忆，下一次就不再占位。
//
// 深链接冷打开（点通知 / 邮件里的 ?wf=… 链接）：目标工作流要等简略会话列表回来才知道（生产约 +850ms，与第一轮内容同一帧），
// 这段时间会话页的「当前工作流」是空的。占位的键因此取 shellWorkflowKey：当前工作流 || 地址上还没处理完的深链指向的 wf，
// 这样第一帧就能按目标工作流的记忆占位（第一版只认当前工作流，深链接冷打开时画不出占位，生产复测没有可信改善）。

export const SHELL_HINT_KEY = 'relaymesh_shell_hints'
export const SHELL_HINT_MAX = 48
/** 芯片宽度的合理范围（px）：落在范围外的当脏数据丢掉。 */
const CHIP_MIN = 24
const CHIP_MAX = 640
/** 会话相关芯片整体宽度的合理范围（px）：一条状态条最宽也就一屏，超出的当脏数据丢掉。 */
const LEAD_MIN = 40
const LEAD_MAX = 1600
/**
 * 目标会话解析出来之后，多久算「芯片都到齐了」（毫秒）：这之前状态条里会话相关芯片按记忆的整体宽度预留，之后放开让真实宽度说了算，
 * 同时把量到的真实宽度记下来。生产上最晚的一枚（授权档位芯片的文字）在解析后约 +0.5 秒到。
 */
export const LEAD_SETTLE_MS = 1500

export interface ChipHint {
  /** 上次状态芯片的宽度（px，取整）。 */
  w: number
  /** 当时是否在 sm（≥640px）及以上：窄屏芯片更短（子活动文字收起），宽度不能跨断点借用。 */
  wide: boolean
}

export interface LeadHint {
  /** 上次安顿之后，状态条里会话相关芯片整体的宽度（px，取整）：第一枚芯片左缘到最后一枚右缘。 */
  w: number
  /** 当时是否在 md（≥768px）及以上：窄屏状态条是不换行的横滑条、宽屏会换行，整体宽度不能跨断点借用（断点与状态条的版式切换一致，不是 ChipHint 的 sm）。 */
  wide: boolean
}

export interface ShellHint {
  /** 上次取到的项目任务数（只记大于 0 的；没有任务就没有这个字段）。 */
  tasks?: number
  chip?: ChipHint
  lead?: LeadHint
  /** 最近一次改写的时间（毫秒），用来淘汰最久没改过的工作流。 */
  at: number
}

type HintStore = Pick<Storage, 'getItem' | 'setItem'>
type HintMap = Record<string, ShellHint>

function defaultStorage(): HintStore | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    // 隐私模式 / 被禁用：当作没有记忆，页面照常工作
    return null
  }
}

function validEntry(raw: unknown): ShellHint | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const out: ShellHint = { at: typeof r.at === 'number' && Number.isFinite(r.at) ? r.at : 0 }
  if (typeof r.tasks === 'number' && Number.isInteger(r.tasks) && r.tasks > 0) out.tasks = r.tasks
  const c = r.chip as Record<string, unknown> | undefined
  if (c && typeof c.w === 'number' && Number.isFinite(c.w) && c.w >= CHIP_MIN && c.w <= CHIP_MAX && typeof c.wide === 'boolean') {
    out.chip = { w: Math.round(c.w), wide: c.wide }
  }
  const l = r.lead as Record<string, unknown> | undefined
  if (l && typeof l.w === 'number' && Number.isFinite(l.w) && l.w >= LEAD_MIN && l.w <= LEAD_MAX && typeof l.wide === 'boolean') {
    out.lead = { w: Math.round(l.w), wide: l.wide }
  }
  return out.tasks === undefined && out.chip === undefined && out.lead === undefined ? null : out
}

function load(storage: HintStore | null): HintMap {
  if (!storage) return {}
  try {
    const parsed = JSON.parse(storage.getItem(SHELL_HINT_KEY) ?? '{}') as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const out: HintMap = {}
    for (const [wf, raw] of Object.entries(parsed as Record<string, unknown>)) {
      const e = validEntry(raw)
      if (e) out[wf] = e
    }
    return out
  } catch {
    return {}
  }
}

function save(storage: HintStore | null, map: HintMap) {
  if (!storage) return
  const keys = Object.keys(map)
  if (keys.length > SHELL_HINT_MAX) {
    keys.sort((a, b) => map[b].at - map[a].at)
    for (const k of keys.slice(SHELL_HINT_MAX)) delete map[k]
  }
  try {
    storage.setItem(SHELL_HINT_KEY, JSON.stringify(map))
  } catch {
    // 配额满 / 被禁用：记不住就记不住，下次晚到一次而已
  }
}

/** 读一个工作流的记忆；没有（或工作流编号为空）返回 null。 */
export function readShellHint(workflowId: string, storage: HintStore | null = defaultStorage()): ShellHint | null {
  if (!workflowId) return null
  return load(storage)[workflowId] ?? null
}

export interface ShellHintPatch {
  /** 任务数：大于 0 记下，0 / null 忘掉。 */
  tasks?: number | null
  /** 状态芯片：给宽度记下，null 忘掉。 */
  chip?: ChipHint | null
  /** 会话相关芯片整体：给宽度记下，null 忘掉。 */
  lead?: LeadHint | null
}

/**
 * 改写一个工作流的记忆（只动 patch 里出现的字段）。内容没变不写存储；字段全空时整条删掉。
 * storage / now 给测试注入。
 */
export function patchShellHint(
  workflowId: string,
  patch: ShellHintPatch,
  opts: { storage?: HintStore | null; now?: number } = {},
): void {
  if (!workflowId) return
  const storage = opts.storage === undefined ? defaultStorage() : opts.storage
  if (!storage) return
  const map = load(storage)
  const prev = map[workflowId]
  const next: ShellHint = { at: opts.now ?? Date.now() }
  if (prev?.tasks !== undefined) next.tasks = prev.tasks
  if (prev?.chip) next.chip = prev.chip
  if (prev?.lead) next.lead = prev.lead
  if (patch.tasks !== undefined) {
    if (patch.tasks && patch.tasks > 0) next.tasks = Math.floor(patch.tasks)
    else delete next.tasks
  }
  if (patch.chip !== undefined) {
    const c = patch.chip
    if (c && Number.isFinite(c.w) && c.w >= CHIP_MIN && c.w <= CHIP_MAX) next.chip = { w: Math.round(c.w), wide: c.wide }
    else delete next.chip
  }
  if (patch.lead !== undefined) {
    const l = patch.lead
    if (l && Number.isFinite(l.w) && l.w >= LEAD_MIN && l.w <= LEAD_MAX) next.lead = { w: Math.round(l.w), wide: l.wide }
    else delete next.lead
  }
  const empty = next.tasks === undefined && next.chip === undefined && next.lead === undefined
  const same =
    (prev?.tasks ?? 0) === (next.tasks ?? 0) &&
    (prev?.chip?.w ?? 0) === (next.chip?.w ?? 0) &&
    (prev?.chip?.wide ?? false) === (next.chip?.wide ?? false) &&
    (prev?.lead?.w ?? 0) === (next.lead?.w ?? 0) &&
    (prev?.lead?.wide ?? false) === (next.lead?.wide ?? false)
  if (same && (prev !== undefined || empty)) return
  if (empty) delete map[workflowId]
  else map[workflowId] = next
  save(storage, map)
}

export type StripMode = 'hidden' | 'reserved' | 'ready'

/**
 * 「项目任务」一行现在该是什么样：
 *  - ready：任务取到了且有任务，画真内容；
 *  - reserved：任务还在路上、记忆里上次有任务（且没出错），只占位（同样的盒子，里面是骨架）；
 *  - hidden：没有工作流、任务取到是空的、上次就没有任务（不为没任务的项目留空带）、或取任务出了错。
 */
export function stripMode(s: { workflowId: string; loaded: boolean; error: string; total: number; hinted: number }): StripMode {
  if (!s.workflowId) return 'hidden'
  if (s.loaded) return s.total > 0 ? 'ready' : 'hidden'
  return s.hinted > 0 && !s.error ? 'reserved' : 'hidden'
}

/**
 * 状态芯片的占位宽度（px）；0 = 不占位。
 * 只在这个工作流有记忆、记忆是同一断点下量的、芯片自己还没出现、Agent 状态概览也还没取到（取到了而没有会话 = 这个工作流现在没有芯片）、
 * 概览没有取失败（失败时不让骨架一直挂着）时占位。
 */
export function chipReservation(s: {
  hint: ShellHint | null
  wide: boolean
  hasSession: boolean
  overviewLoaded: boolean
  overviewFailed: boolean
  supported: boolean
}): number {
  if (s.hasSession || s.overviewLoaded || s.overviewFailed || !s.supported) return 0
  const c = s.hint?.chip
  if (!c || c.wide !== s.wide) return 0
  return c.w
}

// ───────────── 侧栏「群聊」列表的行数（不分工作流，全站一份） ─────────────
// 侧栏里「群聊」入口下面最多列 SIDEBAR_GROUP_ROWS_MAX 个最近有动静的群，群列表要等 GET /api/v1/groups 回来才有。
// 生产上它与会话页第一轮内容差不多同时到：列表一出来，下面的搜索框、筛选、会话列表整体被推下去（4 个群约 114px），
// 正好和会话页的其它位移落在同一帧里（#246）。记住上次列了几行，没回来之前先用同样高的骨架行占位，到了原地换成真行。
export const SIDEBAR_GROUP_ROWS_KEY = 'relaymesh_sidebar_group_rows'
export const SIDEBAR_GROUP_ROWS_MAX = 5

/** 上次侧栏「群聊」列表列了几行（0 = 没有记忆 / 上次没有群 / 数据不合法）。 */
export function readSidebarGroupRows(storage: HintStore | null = defaultStorage()): number {
  if (!storage) return 0
  try {
    const n = Number(storage.getItem(SIDEBAR_GROUP_ROWS_KEY))
    return Number.isInteger(n) && n > 0 && n <= SIDEBAR_GROUP_ROWS_MAX ? n : 0
  } catch {
    return 0
  }
}

/** 记下这次侧栏「群聊」列表列了几行（超出范围按边界取）；没有变化不写存储，没有群记成 0（读出来也是「没有记忆」）。 */
export function writeSidebarGroupRows(rows: number, storage: HintStore | null = defaultStorage()): void {
  if (!storage) return
  const n = Number.isInteger(rows) ? Math.min(Math.max(rows, 0), SIDEBAR_GROUP_ROWS_MAX) : 0
  if (readSidebarGroupRows(storage) === n) return
  try {
    storage.setItem(SIDEBAR_GROUP_ROWS_KEY, String(n))
  } catch {
    // 配额满 / 被禁用：记不住就记不住，下次晚到一次而已
  }
}

/**
 * 占位记忆的键：当前工作流，没有就用地址上还没处理完的深链指向的 wf（深链接冷打开时，目标要等简略会话列表回来才解析出来，
 * 那之前「当前工作流」是空的，见 FeedbackView 的 feedWorkflowId）。都没有返回空串（不占位）。
 */
export function shellWorkflowKey(currentWorkflowId: string, pendingDeepLinkWf: string): string {
  return currentWorkflowId || pendingDeepLinkWf || ''
}

/**
 * 状态条里「会话相关芯片整体」要预留的最小宽度（px）；0 = 不预留。
 * 有记忆、记忆是同一断点下量的、芯片还没到齐（settled 为假）时预留；到齐之后放开，让真实宽度说了算
 * （记忆过期、这次芯片比上次少时，预设按钮左移一次，同时记忆改写成这次的宽度）。
 */
export function leadReservation(s: { hint: ShellHint | null; wide: boolean; settled: boolean }): number {
  if (s.settled) return 0
  const l = s.hint?.lead
  if (!l || l.wide !== s.wide) return 0
  return l.w
}

/**
 * 一组芯片占的整体宽度（px，取整）：最左的左缘到最右的右缘；宽度为 0 的（没有渲染内容的占位节点）不算；没有芯片返回 0。
 * 只对「同一行」有意义：调用方在芯片折成多行时不记。
 */
export function leadWidthOf(rects: readonly { left: number; right: number }[]): number {
  let left = Infinity
  let right = -Infinity
  for (const r of rects) {
    if (!(r.right > r.left)) continue
    if (r.left < left) left = r.left
    if (r.right > right) right = r.right
  }
  return right > left ? Math.round(right - left) : 0
}
