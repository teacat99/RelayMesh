// 任务清单（WorkItem）的纯逻辑：状态与类型元数据、模块规划树、按规划顺序的分组与排序、进度条取值、输入规整、时间描述。
// 不依赖 Vue / DOM，便于单元测试。字段与上限须与后端 internal/model/work_item.go、work_module.go 保持一致。
//
// 注意：这里的「任务」是独立于会话的工作清单，与托管自驾的编排任务（stores/task.ts）无关。

export type WorkState = 'todo' | 'doing' | 'review' | 'done' | 'shelved'
export type WorkKind = 'bug' | 'feature' | 'improve' | 'research' | 'ops'
/**
 * 任务当前处在哪个阶段：评估 → 方案 → 开发 → 改进。
 * 它与「类型」（缺陷 / 功能…，是性质）和「状态」（待办 / 进行中…，是进展）相互独立：同一个功能任务可以先评估、再出方案、再开发，交付后还会进入改进。
 */
export type WorkStage = 'assess' | 'plan' | 'dev' | 'improve'
export type WorkActor = 'human' | 'agent'
/** 共享任务板上一条指派的接取状态（#175 B2）：待接取 / 已接取 / 已拒绝；没有指派时为空。 */
export type WorkClaimState = 'offered' | 'accepted' | 'declined'
/** 时间线上的协作事件（只有协作动作才有；普通进展为空）。 */
export type WorkEvent = 'assign' | 'reassign' | 'unassign' | 'accept' | 'decline' | 'deliver' | 'accept_delivery' | 'reject_delivery'
/** 进度条取值的来源：显式填写 / 子任务完成比例 / 由状态推导（只适合画条，不展示数字）。 */
export type ProgressFrom = 'explicit' | 'children' | 'state'

export interface WorkItemUpdate {
  id: number
  work_item_id: number
  actor_kind: WorkActor
  /** 智能体写入时记它自己的工作流（人为空）：共享板上看得出是谁写的。 */
  actor_workflow_id?: string
  /** 协作事件：指派 / 接取 / 拒绝 / 交付 / 验收 / 退回…；普通进展为空。 */
  event?: WorkEvent | ''
  session_id?: string
  from_state?: WorkState | ''
  to_state?: WorkState | ''
  text?: string
  created_at: string
}

export interface WorkItemSession {
  session_id: string
  linked_at: string
  title?: string
  status?: string
}

/** 一个会话对一个任务做了什么（仅按会话查询任务时由后端附带）。 */
export interface WorkItemSessionCtx {
  /** 任务是这个会话创建的。 */
  created: boolean
  /** 这个会话写在时间线上的条数（创建那一条也算）。 */
  updates: number
  /** 这个会话第一次与最近一次改变状态时的「原状态」与「新状态」；没改过状态时为空。 */
  from_state?: WorkState | ''
  to_state?: WorkState | ''
  /** 这个会话最近一次写时间线的时间；只是被关联（时间线上没有它的记录）时为空。 */
  last_at?: string
}

/** 「某个会话涉及某个任务」的一条关联，连同这个会话对任务做了什么（会话页按轮次展开任务用）。 */
export interface WorkSessionLink extends WorkItemSessionCtx {
  session_id: string
  work_item_id: number
}

export interface WorkItem {
  id: number
  /** 创建者所在的工作流；同一个项目的工作流共用一张任务板（board_key），#N 在板内唯一。 */
  workflow_id: string
  /** 任务所在的板：项目板 `p<项目编号>`，或工作流独立板 `w:<工作流编号>`。 */
  board_key?: string
  seq: number
  title: string
  kind: WorkKind
  /** 阶段：评估 → 方案 → 开发 → 改进；空 = 没标注（界面按类型推导一个默认值，见 stageView）。 */
  stage?: WorkStage | ''
  labels: string[]
  state: WorkState
  /** 任务内容的说明：这件事是什么、做到什么算完成。标题只是名字，进度只说做到哪；没写时为空串。 */
  description?: string
  progress: string
  conclusion: string
  refs: string[]
  parent_id?: number
  source_session_id?: string
  last_session_id?: string
  created_by: WorkActor
  closed_at?: string
  created_at: string
  updated_at: string
  updates?: WorkItemUpdate[]
  sessions?: WorkItemSession[]
  child_total?: number
  child_done?: number
  session_count?: number
  parent_title?: string
  parent_seq?: number
  /** 所属模块 / 功能的编号与路径（如「终端/历史日志」）；没有归类时为空。 */
  module_id?: number
  module?: string
  /** 显式写下的完成度（0–100）；没写过时为空。 */
  percent?: number
  /** 进度条上的取值（0–100）与来源，后端已按「已完成 > 显式 > 子任务比例 > 状态推导」算好。 */
  progress_pct?: number
  progress_from?: ProgressFrom
  /** 按会话查询时：这个会话对该任务做了什么。 */
  session_ctx?: WorkItemSessionCtx
  /** 共享任务板（#175 B2）：承接者工作流；空 = 没有指派。 */
  assignee_workflow_id?: string
  /** 派发者工作流；空且已有承接者 = 人在网页上派的。 */
  assigned_by_workflow_id?: string
  claim_state?: WorkClaimState | ''
  assigned_at?: string
  accepted_at?: string
}

export interface WorkflowStat {
  workflow_id: string
  /** 它所在的任务板；同一张板上的工作流各占一条，数字都是整张板的。 */
  board_key?: string
  /** 同一张板上有任务的工作流（只有一个时不带）；按最近更新从新到旧。 */
  workflows?: string[]
  /** 共用的项目板所属项目的名称（只有多个工作流共用时才带）；界面给共享板起名用。 */
  board_name?: string
  total: number
  active: number
  /** 进行中 / 验证中的数量（会话页头「任务」入口的角标）。 */
  doing?: number
  review?: number
  updated_at: string
}

/** 模块（或功能）下的任务统计；percent 是叶子任务（没有子任务的任务）的平均进度。 */
export interface WorkModuleStats {
  total: number
  todo: number
  doing: number
  review: number
  done: number
  shelved: number
  percent: number
}

/** 模块规划树的一个节点：depth 0 = 模块，1 = 功能。后端按树序返回扁平列表。 */
export interface WorkModule {
  id: number
  /** 创建者所在的工作流；节点属于任务板（board_key）。 */
  workflow_id: string
  board_key?: string
  parent_id?: number
  name: string
  description: string
  sort: number
  created_at: string
  updated_at: string
  /** 完整路径：模块名，或「模块名/功能名」。 */
  path: string
  depth: number
  stats: WorkModuleStats
}

export type WorkCounts = Partial<Record<WorkState, number>>

/** 与后端 model.WorkTitleMax 等常量一致。 */
export const WORK_LIMITS = {
  title: 200,
  description: 1000,
  progress: 500,
  conclusion: 400,
  note: 500,
  label: 24,
  labels: 8,
  ref: 200,
  refs: 10,
  /** 每个任务保留的最近进展条数。 */
  updates: 20,
  /** 任务关闭超过这么多天后，进展时间线被后台整理掉。 */
  compactAfterDays: 90,
  /** 「发起改进需求」的正文上限（后端 model.WorkImproveMax）：它同时写进任务时间线（备注 500 字内）并交给智能体。 */
  improve: 450,
} as const

export const WORK_STATES: WorkState[] = ['todo', 'doing', 'review', 'done', 'shelved']
/** 状态筛选条上的排列顺序：先放需要关注的，已搁置放最后。 */
export const WORK_FILTER_ORDER: WorkState[] = ['doing', 'review', 'todo', 'done', 'shelved']
/** 默认显示的状态：已搁置是旁支，需要时再打开。 */
export const DEFAULT_VISIBLE_STATES: WorkState[] = ['doing', 'review', 'todo', 'done']
export const WORK_KINDS: WorkKind[] = ['bug', 'feature', 'improve', 'research', 'ops']

export type Tone = 'slate' | 'sky' | 'amber' | 'emerald' | 'violet' | 'rose' | 'zinc'

export interface StateMeta {
  label: string
  /** 这一列放什么、该由谁处理的一句话说明。 */
  hint: string
  tone: Tone
}

export const WORK_STATE_META: Record<WorkState, StateMeta> = {
  todo: { label: '待办', hint: '还没开始的事', tone: 'slate' },
  doing: { label: '进行中', hint: '正在做的事', tone: 'sky' },
  review: { label: '验证中', hint: '开发完成，智能体在跑回归 / e2e / 热更冒烟；验证通过后由它直接置为已完成', tone: 'violet' },
  done: { label: '已完成', hint: '验证通过、已交付的事', tone: 'emerald' },
  shelved: { label: '已搁置', hint: '暂时放下，随时可以恢复', tone: 'zinc' },
}

export const WORK_KIND_META: Record<WorkKind, { label: string; tone: Tone }> = {
  bug: { label: '缺陷', tone: 'rose' },
  feature: { label: '功能', tone: 'sky' },
  improve: { label: '改进', tone: 'violet' },
  research: { label: '调研与方案', tone: 'amber' },
  ops: { label: '运维', tone: 'slate' },
}

export function isWorkState(v: unknown): v is WorkState {
  return typeof v === 'string' && (WORK_STATES as string[]).includes(v)
}

export function isWorkKind(v: unknown): v is WorkKind {
  return typeof v === 'string' && (WORK_KINDS as string[]).includes(v)
}

export function stateLabel(state: string): string {
  return isWorkState(state) ? WORK_STATE_META[state].label : state || '未知'
}

export function kindLabel(kind: string): string {
  return isWorkKind(kind) ? WORK_KIND_META[kind].label : kind || '未分类'
}

export function isClosedState(state: string): boolean {
  return state === 'done' || state === 'shelved'
}

/** 任务在界面与提交说明里的引用形式：#12。 */
export function workRef(item: { seq: number }): string {
  return `#${item.seq}`
}

/** 字符数（按 Unicode 码点算，与后端的 rune 计数一致）。 */
export function runeLength(s: string): number {
  let n = 0
  for (const _ of s) n++
  return n
}

export function truncateRunes(s: string, max: number): string {
  if (runeLength(s) <= max) return s
  return Array.from(s).slice(0, max).join('')
}

// ---------- 状态流转 ----------

export interface WorkAction {
  to: WorkState
  label: string
  /** 主操作（卡片上高亮的那一个）。 */
  primary?: boolean
}

/**
 * 卡片上给「人」的快捷操作：沿主线向前一步为主操作，其余是常见的回退 / 旁支。
 * 人可以把任务置为任何状态；这里只列最常用的，其余用详情里的状态选择。
 */
export function quickActions(state: WorkState): WorkAction[] {
  switch (state) {
    case 'todo':
      return [
        { to: 'doing', label: '开始', primary: true },
        { to: 'shelved', label: '搁置' },
      ]
    case 'doing':
      return [
        { to: 'review', label: '进入验证', primary: true },
        { to: 'shelved', label: '搁置' },
      ]
    case 'review':
      return [
        { to: 'done', label: '标记完成', primary: true },
        { to: 'doing', label: '退回进行中' },
      ]
    case 'done':
      return [{ to: 'doing', label: '重新打开' }]
    case 'shelved':
      return [{ to: 'todo', label: '恢复为待办', primary: true }]
    default:
      return []
  }
}

/** 一次状态变化在时间线上的说明，例如「待办 → 进行中」。 */
export function describeTransition(from?: string, to?: string): string {
  if (from && to && from !== to) return `${stateLabel(from)} → ${stateLabel(to)}`
  if (to && !from) return `置为${stateLabel(to)}`
  return ''
}

// ---------- 进度条 ----------

export function clampPercent(v: number): number {
  if (!Number.isFinite(v)) return 0
  return Math.min(100, Math.max(0, Math.round(v)))
}

/** 没有后端取值时（旧数据 / 刚创建的本地对象），按状态推导的进度。与后端 model.EffectivePercent 一致。 */
const STATE_DEFAULT_PCT: Record<WorkState, number> = { todo: 0, doing: 50, review: 90, done: 100, shelved: 0 }

export interface ProgressView {
  /** 进度条宽度，0–100。 */
  pct: number
  /** 条旁边的数字；由状态推导出来的值不展示数字，避免伪精确（「进行中 50%」其实没人量过）。 */
  label: string
  from: ProgressFrom
  /** 数值是由状态推导的：画条时淡一些。 */
  derived: boolean
  tone: Tone
}

export function progressView(item: Pick<WorkItem, 'state'> & Partial<Pick<WorkItem, 'progress_pct' | 'progress_from'>>): ProgressView {
  const state: WorkState = isWorkState(item.state) ? item.state : 'todo'
  const from: ProgressFrom = item.progress_from === 'explicit' || item.progress_from === 'children' ? item.progress_from : 'state'
  const pct = typeof item.progress_pct === 'number' ? clampPercent(item.progress_pct) : STATE_DEFAULT_PCT[state]
  return { pct, label: from === 'state' ? '' : `${pct}%`, from, derived: from === 'state', tone: WORK_STATE_META[state].tone }
}

// ---------- 阶段与双进度条 ----------
//
// 一个任务用两条并排的小进度条描述完整的追踪情况：
//   第一条 = 阶段（评估 → 方案 → 开发 → 改进）：这件事做到了哪一类工作；
//   第二条 = 状态（待办 → 进行中 → 验证中 → 已完成）：这件事走到了开发流程的哪一步。
// 单个任务画「位置」（走到第几格），一组任务画「分布」（各格有几个）。

export const WORK_STAGES: WorkStage[] = ['assess', 'plan', 'dev', 'improve']

export interface StageMeta {
  label: string
  /** 悬浮说明：这个阶段在做什么。 */
  hint: string
}

export const WORK_STAGE_META: Record<WorkStage, StageMeta> = {
  assess: { label: '评估', hint: '先弄清楚要不要做、做什么' },
  plan: { label: '方案', hint: '决定怎么做' },
  dev: { label: '开发', hint: '正在把它做出来' },
  improve: { label: '改进', hint: '交付之后的打磨与迭代' },
}

export function isWorkStage(v: unknown): v is WorkStage {
  return typeof v === 'string' && (WORK_STAGES as string[]).includes(v)
}

/** 阶段的中文名；没标注 / 不认识的值显示「未标注」。 */
export function stageLabel(stage: string | undefined | null): string {
  return isWorkStage(stage) ? WORK_STAGE_META[stage].label : '未标注'
}

/** 没有标注阶段时，按类型推导一个默认值：调研 → 评估，改进 → 改进，其余（缺陷 / 功能 / 运维）→ 开发。 */
export function defaultStageForKind(kind: string | undefined): WorkStage {
  if (kind === 'research') return 'assess'
  if (kind === 'improve') return 'improve'
  return 'dev'
}

export interface StageView {
  stage: WorkStage
  /** 0–3：在 评估 → 方案 → 开发 → 改进 里的位置。 */
  index: number
  /** 没有人 / 智能体标注过，阶段是按类型推导出来的：画条时淡一些，悬浮说明里讲清楚。 */
  derived: boolean
  label: string
  hint: string
}

export function stageView(item: Pick<WorkItem, 'kind'> & { stage?: string | null }): StageView {
  const marked = isWorkStage(item.stage)
  const stage: WorkStage = marked ? (item.stage as WorkStage) : defaultStageForKind(item.kind)
  return { stage, index: WORK_STAGES.indexOf(stage), derived: !marked, label: WORK_STAGE_META[stage].label, hint: WORK_STAGE_META[stage].hint }
}

/** 第二条进度条的四格（已搁置不在这条主线上，单独画成斜纹）。 */
export const STATE_TRACK: WorkState[] = ['todo', 'doing', 'review', 'done']

/** 状态在进度条上的悬浮说明：按「走到了开发流程的哪一步」来讲。 */
export const STATE_TRACK_HINT: Record<WorkState, string> = {
  todo: '待办：还没开始，等待决策或排期',
  doing: '进行中：正在开发 / 处理',
  review: '验证中：开发完成，正在跑回归 / e2e / 热更冒烟',
  done: '已完成：验证通过，已交付',
  shelved: '已搁置：暂时放下，不在主线上',
}

export interface StateTrackView {
  state: WorkState
  /** 0–3：在 待办 → 进行中 → 验证中 → 已完成 里的位置；已搁置为 -1。 */
  index: number
  shelved: boolean
  label: string
  hint: string
  tone: Tone
}

export function stateTrack(item: Pick<WorkItem, 'state'>): StateTrackView {
  const state: WorkState = isWorkState(item.state) ? item.state : 'todo'
  return {
    state,
    index: STATE_TRACK.indexOf(state),
    shelved: state === 'shelved',
    label: WORK_STATE_META[state].label,
    hint: STATE_TRACK_HINT[state],
    tone: WORK_STATE_META[state].tone,
  }
}

/** 一格的填充程度：已经走过的与当前的填满，其余留空；index 为 -1（搁置）时全部留空。 */
export function trackFill(count: number, index: number): boolean[] {
  return Array.from({ length: count }, (_, i) => index >= 0 && i <= index)
}

/**
 * 进度条旁边要不要写数字：只有手动填写的完成度或子任务完成比例才写（由状态推导出来的值没人量过，不写）。
 * 返回 null 表示不写。
 */
export function trackPercentLabel(item: Pick<WorkItem, 'state'> & Partial<Pick<WorkItem, 'progress_pct' | 'progress_from'>>): string | null {
  const v = progressView(item)
  return v.derived ? null : v.label
}

export interface TrackSummary {
  total: number
  /** 固定按 评估 → 方案 → 开发 → 改进 的顺序给出四项，没有任务的也列出（数量为 0）。 */
  stages: { stage: WorkStage; count: number }[]
  /** 固定按 待办 → 进行中 → 验证中 → 已完成 → 已搁置 的顺序给出五项。 */
  states: { state: WorkState; count: number }[]
  /** 阶段是按类型推导出来的任务数（没人标注过）；统计里它们也按推导出的阶段计数。 */
  derivedStages: number
  done: number
  /** 整体完成度 0–100：叶子任务（没有子任务的任务）的平均进度，已搁置的不计入；没有可计入的任务时为 0。与后端模块统计口径一致。 */
  percent: number
}

/** 一组任务的分布：每个阶段、每个状态各有几个，以及整体完成度。 */
export function summarizeTracks(items: WorkItem[]): TrackSummary {
  const stageCount: Record<WorkStage, number> = { assess: 0, plan: 0, dev: 0, improve: 0 }
  const stateCount: Record<WorkState, number> = { todo: 0, doing: 0, review: 0, done: 0, shelved: 0 }
  let derivedStages = 0
  const present = new Set(items.map(i => i.id))
  const hasKids = new Set<number>()
  for (const it of items) if (it.parent_id && present.has(it.parent_id)) hasKids.add(it.parent_id)

  let pctSum = 0
  let pctN = 0
  for (const it of items) {
    const sv = stageView(it)
    stageCount[sv.stage]++
    if (sv.derived) derivedStages++
    const st: WorkState = isWorkState(it.state) ? it.state : 'todo'
    stateCount[st]++
    if (st !== 'shelved' && !hasKids.has(it.id)) {
      pctSum += progressView(it).pct
      pctN++
    }
  }
  return {
    total: items.length,
    stages: WORK_STAGES.map(stage => ({ stage, count: stageCount[stage] })),
    states: WORK_STATES.map(state => ({ state, count: stateCount[state] })),
    derivedStages,
    done: stateCount.done,
    percent: pctN ? Math.round(pctSum / pctN) : 0,
  }
}

/**
 * 这个会话对某个任务做了什么，一句话：例如「本对话创建 · 推进 3 次 · 待办 → 验证中」。
 * 只是被关联、时间线上没有记录时返回「本对话关联」。
 */
export function describeSessionCtx(ctx: WorkItemSessionCtx | undefined | null): string {
  if (!ctx) return ''
  const parts: string[] = []
  if (ctx.created) parts.push('本对话创建')
  // 创建那一条也计入 updates：创建的任务只有「创建」一条时不重复说「推进 1 次」
  const advanced = Math.max(0, ctx.updates - (ctx.created ? 1 : 0))
  if (advanced > 0) parts.push(ctx.created ? `推进 ${advanced} 次` : `本对话推进 ${advanced} 次`)
  const move = describeTransition(ctx.from_state || '', ctx.to_state || '')
  if (move) parts.push(move)
  return parts.length ? parts.join(' · ') : '本对话关联'
}

/** 一个会话（一轮对话）涉及的一条任务：任务本身 + 这一轮对它做了什么。 */
export interface SessionTask {
  item: WorkItem
  ctx: WorkItemSessionCtx
}

/**
 * 把「会话 ↔ 任务」关联按会话分组。每个会话里：本轮创建的在前，其余在后，各自按任务序号升序；
 * 关联指向的任务不在 items 里（比如已被删除）时丢弃，不让一条脏数据拖垮整页。
 */
export function groupSessionTasks(items: WorkItem[], links: WorkSessionLink[]): Map<string, SessionTask[]> {
  const byId = new Map(items.map(i => [i.id, i]))
  const out = new Map<string, SessionTask[]>()
  for (const l of links) {
    const item = byId.get(l.work_item_id)
    if (!item || !l.session_id) continue
    const ctx: WorkItemSessionCtx = {
      created: !!l.created,
      updates: l.updates || 0,
      from_state: l.from_state,
      to_state: l.to_state,
      last_at: l.last_at,
    }
    const list = out.get(l.session_id) ?? []
    list.push({ item, ctx })
    out.set(l.session_id, list)
  }
  for (const list of out.values()) {
    list.sort((a, b) => Number(b.ctx.created) - Number(a.ctx.created) || a.item.seq - b.item.seq)
  }
  return out
}

// ---------- 模块规划树 ----------

/** 模块名 / 功能名的限制，与后端 model.WorkModule* 常量一致。 */
export const MODULE_LIMITS = { name: 40, modulesPerWorkflow: 30, featuresPerModule: 30 } as const

export const UNASSIGNED_LABEL = '未归类'

/** 左侧树的选中项：全部 / 未归类 / 某个模块或功能的编号。 */
export type ModuleSelection = 'all' | 'none' | number
export const MODULE_ALL = 'all' as const
export const MODULE_NONE = 'none' as const

export interface ModuleNode {
  module: WorkModule
  features: WorkModule[]
}

/**
 * 把模块列表整理成两层树。不依赖列表本身的顺序：同级按 sort、再按 id 排；
 * 找不到父模块的功能（理论上不会出现）当作顶层模块处理，避免整棵树因为一条脏数据丢失节点。
 */
export function buildModuleTree(modules: WorkModule[]): ModuleNode[] {
  const byId = new Map(modules.map(m => [m.id, m]))
  const bySort = (a: WorkModule, b: WorkModule) => a.sort - b.sort || a.id - b.id
  const tops = modules.filter(m => !m.parent_id || !byId.has(m.parent_id)).sort(bySort)
  return tops.map(m => ({
    module: m,
    features: modules.filter(f => f.parent_id === m.id).sort(bySort),
  }))
}

/** 树序的扁平列表：模块、它的功能、下一个模块……列表分组与下拉选项都按这个顺序。 */
export function flattenModuleTree(tree: ModuleNode[]): WorkModule[] {
  return tree.flatMap(n => [n.module, ...n.features])
}

/** 选中节点覆盖的模块编号：选中模块 = 它自己 + 它的全部功能；选中功能 = 自己。'all' / 'none' 返回 null。 */
export function selectionModuleIds(sel: ModuleSelection, modules: WorkModule[]): Set<number> | null {
  if (typeof sel !== 'number') return null
  const ids = new Set<number>([sel])
  for (const m of modules) if (m.parent_id === sel) ids.add(m.id)
  return ids
}

/** 按左侧树的选中项筛选任务；任务引用的模块已不存在时当作「未归类」。 */
export function filterBySelection(items: WorkItem[], sel: ModuleSelection, modules: WorkModule[]): WorkItem[] {
  if (sel === MODULE_ALL) return items
  const known = new Set(modules.map(m => m.id))
  if (sel === MODULE_NONE) return items.filter(i => !i.module_id || !known.has(i.module_id))
  const ids = selectionModuleIds(sel, modules)
  return items.filter(i => !!i.module_id && !!ids?.has(i.module_id))
}

export interface ModuleOption {
  id: number
  path: string
  /** 下拉里显示的文字：功能缩进，路径用「 / 」分隔。 */
  label: string
  depth: 0 | 1
}

/** 表单里「归入模块」的选项，按树序。 */
export function moduleOptions(modules: WorkModule[]): ModuleOption[] {
  return flattenModuleTree(buildModuleTree(modules)).map(m => ({
    id: m.id,
    path: m.path,
    depth: m.parent_id ? 1 : 0,
    label: m.parent_id ? `\u3000${m.name}` : m.name,
  }))
}

/** 路径在界面上的写法：终端/历史日志 → 终端 / 历史日志。 */
export function displayModulePath(path: string | undefined): string {
  return (path || '').split('/').join(' / ')
}

const MODULE_NAME_FORBIDDEN = /[/>＞／›→\r\n]/

export interface NameCheck {
  ok: boolean
  name: string
  error?: string
}

/**
 * 模块 / 功能名的前置校验（后端仍是最终裁判）：非空、不超长、不含路径分隔符、同级不重名（忽略大小写）。
 * selfId 用于改名时排除自己。
 */
export function checkModuleName(raw: string, siblings: { id?: number; name: string }[], what: '模块' | '功能' = '模块', selfId?: number): NameCheck {
  const name = raw.trim()
  if (!name) return { ok: false, name, error: `请填写${what}名称` }
  if (runeLength(name) > MODULE_LIMITS.name) return { ok: false, name, error: `${what}名称最长 ${MODULE_LIMITS.name} 个字` }
  if (MODULE_NAME_FORBIDDEN.test(name)) return { ok: false, name, error: `${what}名称里不能含 / > → 等分隔符或换行` }
  const key = name.toLowerCase()
  if (siblings.some(s => s.id !== selfId && s.name.trim().toLowerCase() === key)) return { ok: false, name, error: `已有同名的${what}` }
  return { ok: true, name }
}

// ---------- 分组与排序 ----------

function time(iso?: string): number {
  if (!iso) return 0
  const t = Date.parse(iso)
  return Number.isNaN(t) ? 0 : t
}

/** 同一个模块里的状态次序：先看正在做的，再看验证中的，然后待办，最后是搁置与已完成。 */
const STATE_RANK: Record<WorkState, number> = { doing: 0, review: 1, todo: 2, shelved: 3, done: 4 }

/** 模块内排序：状态次序，再按最近进展（更新时间）倒序。 */
export function compareInModule(a: WorkItem, b: WorkItem): number {
  const ra = STATE_RANK[a.state] ?? 9
  const rb = STATE_RANK[b.state] ?? 9
  if (ra !== rb) return ra - rb
  return time(b.updated_at) - time(a.updated_at) || b.id - a.id
}

/** 「最近进展」排序：最近更新的在前。 */
export function compareRecent(a: WorkItem, b: WorkItem): number {
  return time(b.updated_at) - time(a.updated_at) || b.id - a.id
}

export type TaskSort = 'plan' | 'recent'

export interface TaskRow {
  item: WorkItem
  /** 0 = 顶层；1 = 批次里的子任务（缩进显示在父任务下面）。 */
  depth: 0 | 1
  /** 顶层行：当前结果里它有几个子任务。 */
  childCount: number
  /** 子任务当前是否折叠（childCount > 0 时才有意义）。 */
  collapsed: boolean
}

export interface TaskGroup {
  /** 稳定的键：m:<模块编号> / none / recent。 */
  key: string
  moduleId: number | null
  /** 完整路径；未归类为「未归类」；「最近进展」平铺时为空。 */
  path: string
  name: string
  /** 功能所属的模块名；顶层模块为空。 */
  parentName: string
  depth: 0 | 1
  /** 模块 / 功能的统计（服务端口径：该节点下全部任务，不受「已完成显示多久」限制）。 */
  stats: WorkModuleStats | null
  rows: TaskRow[]
}

/** 批次的子任务默认是否展开：还有没结束的就展开，全部结束了就折起来省地方。 */
export function defaultExpanded(children: WorkItem[]): boolean {
  return children.some(c => !isClosedState(c.state))
}

/**
 * 把任务整理成列表分组。
 * - plan：跟随左侧规划树的顺序，一个模块（或功能）一组，组内按状态与最近进展排；没归类的放最后。
 *   批次的子任务缩进在父任务下面（父任务不在当前结果里时才独立成行）。
 * - recent：不分组，按最近进展平铺，每行自己标出所属模块。
 */
export function buildTaskGroups(items: WorkItem[], modules: WorkModule[], opts: { sort: TaskSort; expanded?: ReadonlyMap<number, boolean> }): TaskGroup[] {
  if (opts.sort === 'recent') {
    const rows: TaskRow[] = [...items].sort(compareRecent).map(item => ({ item, depth: 0, childCount: 0, collapsed: false }))
    return rows.length ? [{ key: 'recent', moduleId: null, path: '', name: '', parentName: '', depth: 0, stats: null, rows }] : []
  }

  const ordered = flattenModuleTree(buildModuleTree(modules))
  const known = new Set(ordered.map(m => m.id))
  const nameOf = new Map(ordered.map(m => [m.id, m.name]))

  const present = new Set(items.map(i => i.id))
  const kidsOf = new Map<number, WorkItem[]>()
  const top: WorkItem[] = []
  for (const it of items) {
    if (it.parent_id && present.has(it.parent_id)) {
      const list = kidsOf.get(it.parent_id) ?? []
      list.push(it)
      kidsOf.set(it.parent_id, list)
    } else {
      top.push(it)
    }
  }
  for (const list of kidsOf.values()) list.sort((a, b) => a.seq - b.seq)

  const buckets = new Map<number | null, WorkItem[]>()
  for (const it of top) {
    const key = it.module_id && known.has(it.module_id) ? it.module_id : null
    const list = buckets.get(key) ?? []
    list.push(it)
    buckets.set(key, list)
  }

  const rowsOf = (list: WorkItem[]): TaskRow[] => {
    const rows: TaskRow[] = []
    for (const it of [...list].sort(compareInModule)) {
      const kids = kidsOf.get(it.id) ?? []
      const collapsed = kids.length > 0 && !(opts.expanded?.get(it.id) ?? defaultExpanded(kids))
      rows.push({ item: it, depth: 0, childCount: kids.length, collapsed })
      if (!collapsed) for (const k of kids) rows.push({ item: k, depth: 1, childCount: 0, collapsed: false })
    }
    return rows
  }

  const groups: TaskGroup[] = []
  for (const m of ordered) {
    const list = buckets.get(m.id)
    if (!list?.length) continue
    groups.push({
      key: `m:${m.id}`,
      moduleId: m.id,
      path: m.path,
      name: m.name,
      parentName: m.parent_id ? (nameOf.get(m.parent_id) ?? '') : '',
      depth: m.parent_id ? 1 : 0,
      stats: m.stats,
      rows: rowsOf(list),
    })
  }
  const loose = buckets.get(null)
  if (loose?.length) {
    groups.push({ key: 'none', moduleId: null, path: UNASSIGNED_LABEL, name: UNASSIGNED_LABEL, parentName: '', depth: 0, stats: null, rows: rowsOf(loose) })
  }
  return groups
}

/** 各状态的任务数（状态筛选条上的数字）。 */
export function countByState(items: WorkItem[]): Record<WorkState, number> {
  const out: Record<WorkState, number> = { todo: 0, doing: 0, review: 0, done: 0, shelved: 0 }
  for (const it of items) if (isWorkState(it.state)) out[it.state]++
  return out
}

/**
 * 手机任务清单「筛选」按钮角标上的数字：收进筛选面板里、且被改成非默认值的条件个数（#156 批 2）。
 * 类型不是「全部」、排序不是「规划顺序」、已结束任务的时间窗不是默认值（只有已完成 / 已搁置的芯片开着时这个时间窗才有意义）各算一条；
 * 搜索框与状态芯片本来就摆在面板外面，不计入。筛选条件会记在本机，下次进来还在——面板收起时靠这个数字提醒「这里有条件在生效」。
 */
export function countActiveWorkFilters(
  f: { kind: string; sort: TaskSort; closedDays: number; showClosedWindow: boolean; assignee?: string; quick?: string },
  defaultClosedDays: number
): number {
  const assignee = f.assignee && f.assignee !== ASSIGNEE_ALL ? 1 : 0
  const quick = f.quick ? 1 : 0
  return (f.kind ? 1 : 0) + assignee + quick + (f.sort !== 'plan' ? 1 : 0) + (f.showClosedWindow && f.closedDays !== defaultClosedDays ? 1 : 0)
}

export interface FilterOptions {
  workflowId?: string
  kind?: string
  query?: string
  /** 按承接者过滤：ASSIGNEE_ALL / 空 = 不过滤，ASSIGNEE_NONE = 没有指派的，其余 = 这个工作流的。 */
  assignee?: string
  /** 协作快捷筛选（#180 B3b）：空 = 不过滤；to_me / by_me 要结合 viewer（正在看的工作流）。 */
  quick?: string
  viewer?: string
}

export function matchesQuery(item: WorkItem, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const hay = [workRef(item), item.title, item.description || '', item.progress, item.conclusion, item.workflow_id, item.module || '', ...(item.labels || [])]
    .join('\n')
    .toLowerCase()
  return hay.includes(q)
}

/**
 * 任务是否属于一张清单：同一个项目的工作流共用一张任务板（#175），任务的 workflow_id 只是创建者，所以优先按板比较；
 * 板还没取到（boardKey 为空）或任务没带 board_key（旧服务端）时退回按工作流比较。
 */
export function inBoard(item: Pick<WorkItem, 'workflow_id' | 'board_key'>, boardKey: string, workflowId: string): boolean {
  if (boardKey && item.board_key) return item.board_key === boardKey
  return item.workflow_id === workflowId
}

export function filterItems(items: WorkItem[], opts: FilterOptions): WorkItem[] {
  return items.filter(it => {
    if (opts.workflowId && it.workflow_id !== opts.workflowId) return false
    if (opts.kind && it.kind !== opts.kind) return false
    if (opts.assignee && !matchesAssignee(it, opts.assignee)) return false
    if (opts.quick && !matchesQuick(it, opts.quick, opts.viewer || '')) return false
    if (opts.query && !matchesQuery(it, opts.query)) return false
    return true
  })
}

/** 批次的完成进度，例如 {done: 2, total: 5, percent: 40}；不是批次时返回 null。 */
export function batchProgress(item: Pick<WorkItem, 'child_total' | 'child_done'>): { done: number; total: number; percent: number } | null {
  const total = item.child_total || 0
  if (total <= 0) return null
  const done = Math.min(item.child_done || 0, total)
  return { done, total, percent: Math.round((done / total) * 100) }
}

/** 侧栏入口上的一句话概览，例如「3 进行中 · 2 验证中」；没有任何开放任务时返回空串。任务不需要你逐条确认，所以这里只是进度概览，不催你做什么。 */
export function summarizeCounts(counts: WorkCounts | undefined | null): string {
  const parts: string[] = []
  if (counts?.doing) parts.push(`${counts.doing} 进行中`)
  if (counts?.review) parts.push(`${counts.review} 验证中`)
  if (!parts.length && counts?.todo) parts.push(`${counts.todo} 待办`)
  return parts.join(' · ')
}

// ---------- 输入规整 ----------

/** 把「a, b，c 、d」这类输入拆成标签：去空白、去重、逐个截断、最多 8 个。 */
export function parseLabelInput(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/[,\uFF0C\u3001;\uFF1B\n]+/)) {
    const t = truncateRunes(raw.trim(), WORK_LIMITS.label)
    if (!t) continue
    const key = t.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(t)
    if (out.length >= WORK_LIMITS.labels) break
  }
  return out
}

/** 引用一行一条：去空白、去重、逐条截断、最多 10 条。 */
export function parseRefInput(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/\r?\n/)) {
    const t = truncateRunes(raw.trim(), WORK_LIMITS.ref)
    if (!t || seen.has(t)) continue
    seen.add(t)
    out.push(t)
    if (out.length >= WORK_LIMITS.refs) break
  }
  return out
}

export interface TitleCheck {
  ok: boolean
  /** 规整后的标题（去首尾空白，折叠换行）。 */
  title: string
  error?: string
}

export function checkTitle(raw: string): TitleCheck {
  const title = raw.replace(/\s*[\r\n]+\s*/g, ' ').trim()
  if (!title) return { ok: false, title, error: '请填写标题' }
  if (runeLength(title) > WORK_LIMITS.title) return { ok: false, title, error: `标题最长 ${WORK_LIMITS.title} 个字` }
  return { ok: true, title }
}

/** 文本字段的剩余字数提示：超限时返回错误文案。 */
export function lengthHint(text: string, max: number): { count: number; over: boolean } {
  const count = runeLength(text)
  return { count, over: count > max }
}

// ---------- 时间 ----------

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前；超过 30 天显示日期。 */
export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  const t = time(iso)
  if (!t) return ''
  const sec = Math.max(0, Math.floor((now - t) / 1000))
  if (sec < 45) return '刚刚'
  const min = Math.floor(sec / 60)
  if (min < 60) return `${Math.max(1, min)} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  const day = Math.floor(hour / 24)
  if (day <= 30) return `${day} 天前`
  const d = new Date(t)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** 关闭至今的天数；未关闭返回 null。 */
export function closedAgeDays(item: Pick<WorkItem, 'state' | 'closed_at'>, now: number = Date.now()): number | null {
  if (!isClosedState(item.state) || !item.closed_at) return null
  const t = time(item.closed_at)
  if (!t) return null
  return Math.max(0, Math.floor((now - t) / 86400000))
}

/** 关闭超过 90 天的任务，进展时间线已被后台整理压缩；详情里据此提示。 */
export function isCompacted(item: Pick<WorkItem, 'state' | 'closed_at'>, now: number = Date.now()): boolean {
  const days = closedAgeDays(item, now)
  return days !== null && days > WORK_LIMITS.compactAfterDays
}

// ---------- 工作流 ----------

export interface WorkflowOption {
  id: string
  /** 展示名：优先用会话标题，退回到工作流 id；多个工作流共用一张板时是「项目名 · N 个工作流」。 */
  label: string
  /** 该工作流下的开放任务数（共用板时是整张板的）。 */
  active: number
  /** 共用这张任务板的工作流（只有多个时才有）；这一项代表整张板，id 是其中一个成员。 */
  members?: string[]
}

/**
 * 选哪个成员代表共用板：正在看的 > 当前会话的 > 最近有动静的（服务端已按最近更新排好）。
 * selectedBoard 是正在看的工作流所在的板：它自己还没有任务（概况的成员里没有它）、但目录已经归到这个项目时，
 * 也算这张板的一员，免得下拉里同时出现「这张共用板」和「它自己」两项。
 */
export interface BoardPrefer {
  selected?: string
  session?: string
  selectedBoard?: string
}

/**
 * 工作流选择项：并上「已有任务的工作流」与「当前会话里出现过的工作流」，
 * 这样第一次给某个工作流建任务时也能在下拉里选到它。
 *
 * 同一个项目的工作流共用一张任务板（#175）：服务端给每个成员各一条概况（数字都是整张板的），
 * 这里按板合成一项——它们看的是同一份清单，下拉里列出来只会是几个一模一样的选项。
 */
export function buildWorkflowOptions(
  stats: WorkflowStat[],
  known: { workflow_id: string; title?: string }[],
  prefer: BoardPrefer = {}
): WorkflowOption[] {
  const titleOf = new Map<string, string>()
  for (const k of known) {
    if (k.workflow_id && k.title && !titleOf.has(k.workflow_id)) titleOf.set(k.workflow_id, k.title)
  }
  const byId = new Map<string, WorkflowOption>()
  const sharedBoards = new Set<string>()
  const sharedMembers = new Set<string>()
  for (const s of stats) {
    const members = s.workflows && s.workflows.length > 1 && s.board_key ? s.workflows : null
    if (!members || !s.board_key) {
      byId.set(s.workflow_id, { id: s.workflow_id, label: titleOf.get(s.workflow_id) || s.workflow_id, active: s.active })
      continue
    }
    members.forEach(m => sharedMembers.add(m))
    // 正在看的工作流还没有任务、但目录归在这张板的项目里：它也是这张板的一员
    const selectedHere = !!prefer.selected && (members.includes(prefer.selected) || prefer.selectedBoard === s.board_key)
    if (selectedHere && prefer.selected) sharedMembers.add(prefer.selected)
    if (sharedBoards.has(s.board_key)) continue
    sharedBoards.add(s.board_key)
    const rep =
      (selectedHere ? prefer.selected : '') || (prefer.session && members.includes(prefer.session) ? prefer.session : '') || members[0]
    const name = s.board_name || titleOf.get(rep) || rep
    byId.set(rep, { id: rep, label: `${name} · ${members.length} 个工作流`, active: s.active, members: [...members] })
  }
  for (const k of known) {
    if (!k.workflow_id || byId.has(k.workflow_id) || sharedMembers.has(k.workflow_id)) continue
    byId.set(k.workflow_id, { id: k.workflow_id, label: titleOf.get(k.workflow_id) || k.workflow_id, active: 0 })
  }
  // 名称按中文界面的习惯排序，并固定 locale：不跟随浏览器 / 进程的语言环境（否则同一批工作流，
  // 在 LANG=zh_CN 与 en-US 的机器上顺序不同，汉字名称一会儿排在字母前面、一会儿排在后面）。
  return [...byId.values()].sort((a, b) => b.active - a.active || a.label.localeCompare(b.label, 'zh-CN'))
}

/** 共用板上的成员（工作流编号 + 展示名）；没有多个工作流共用时返回空数组——没什么可「共用」的。 */
export function boardMemberList(
  stat: Pick<WorkflowStat, 'workflows'> | null | undefined,
  known: { workflow_id: string; title?: string }[]
): { id: string; label: string }[] {
  const ids = stat?.workflows
  if (!ids || ids.length < 2) return []
  const titleOf = new Map<string, string>()
  for (const k of known) {
    if (k.workflow_id && k.title && !titleOf.has(k.workflow_id)) titleOf.set(k.workflow_id, k.title)
  }
  return ids.map(id => ({ id, label: titleOf.get(id) || id }))
}

/** 任务页顶条上共享板的一句话：「共享板「项目名」· N 个工作流」；没有项目名时省略名字。 */
export function boardChipText(stat: Pick<WorkflowStat, 'workflows' | 'board_name'> | null | undefined): string {
  const n = stat?.workflows?.length ?? 0
  if (n < 2) return ''
  return `共享板${stat?.board_name ? `「${stat.board_name}」` : ''} · ${n} 个工作流`
}

// ---------- 共享板的协作：承接者、接取状态、协作事件（#175 B2） ----------

/** 承接者过滤的取值：全部 / 没有指派的 / 某个工作流（其余字符串就是工作流编号）。 */
export const ASSIGNEE_ALL = 'all'
export const ASSIGNEE_NONE = 'none'

export const WORK_CLAIM_META: Record<WorkClaimState, { label: string; tone: Tone; hint: string }> = {
  offered: { label: '待接取', tone: 'amber', hint: '已派出，承接者还没有答复' },
  accepted: { label: '已接取', tone: 'emerald', hint: '承接者已接下这条任务' },
  declined: { label: '已拒绝', tone: 'rose', hint: '承接者拒绝了这次指派，需要改派或撤销' },
}

export const WORK_EVENT_META: Record<WorkEvent, { label: string; tone: Tone }> = {
  assign: { label: '指派', tone: 'sky' },
  reassign: { label: '改派', tone: 'sky' },
  unassign: { label: '撤销指派', tone: 'zinc' },
  accept: { label: '接取', tone: 'emerald' },
  decline: { label: '拒绝', tone: 'rose' },
  deliver: { label: '交付', tone: 'violet' },
  accept_delivery: { label: '验收通过', tone: 'emerald' },
  reject_delivery: { label: '退回', tone: 'amber' },
}

export function isClaimState(v: unknown): v is WorkClaimState {
  return v === 'offered' || v === 'accepted' || v === 'declined'
}

export function claimLabel(state: string | undefined | null): string {
  return isClaimState(state) ? WORK_CLAIM_META[state].label : ''
}

/** 时间线协作事件的色调；不是协作事件返回 slate。 */
export function eventTone(event: string | undefined | null): Tone {
  return event && event in WORK_EVENT_META ? WORK_EVENT_META[event as WorkEvent].tone : 'slate'
}

/** 时间线事件的中文名；不是协作事件（普通进展）返回空串。 */
export function eventLabel(event: string | undefined | null): string {
  return event && event in WORK_EVENT_META ? WORK_EVENT_META[event as WorkEvent].label : ''
}

export function isAssigned(item: Pick<WorkItem, 'assignee_workflow_id'>): boolean {
  return !!item.assignee_workflow_id
}

/** 工作流的展示名：优先用会话标题，退回到工作流编号。 */
export function workflowLabel(id: string | undefined | null, known: { workflow_id: string; title?: string }[]): string {
  if (!id) return ''
  return known.find(k => k.workflow_id === id && k.title)?.title || id
}

export interface AssigneeView {
  id: string
  label: string
  claim: WorkClaimState | ''
  claimLabel: string
  tone: Tone
  hint: string
  /** 谁派的：工作流的展示名；人在网页上派的写「网页」。 */
  byLabel: string
  /** 协作任务：派发者不是承接者本人（自己派给自己的就是自己的任务）。 */
  collaborative: boolean
}

/** 行内承接者标签与详情「指派」区块的取值；没有指派返回 null（单工作流的板上永远是 null，界面不变）。 */
export function assigneeView(
  item: Pick<WorkItem, 'assignee_workflow_id' | 'assigned_by_workflow_id' | 'claim_state'>,
  known: { workflow_id: string; title?: string }[]
): AssigneeView | null {
  const id = item.assignee_workflow_id
  if (!id) return null
  const claim: WorkClaimState | '' = isClaimState(item.claim_state) ? item.claim_state : ''
  const meta = claim ? WORK_CLAIM_META[claim] : null
  const by = item.assigned_by_workflow_id || ''
  return {
    id,
    label: workflowLabel(id, known),
    claim,
    claimLabel: meta?.label || '',
    tone: meta?.tone || 'zinc',
    hint: meta?.hint || '',
    byLabel: by ? workflowLabel(by, known) : '网页',
    collaborative: by !== id,
  }
}

/** 是否匹配承接者过滤（ASSIGNEE_ALL / 空 = 都匹配）。 */
export function matchesAssignee(item: Pick<WorkItem, 'assignee_workflow_id'>, selection: string): boolean {
  if (!selection || selection === ASSIGNEE_ALL) return true
  if (selection === ASSIGNEE_NONE) return !item.assignee_workflow_id
  return item.assignee_workflow_id === selection
}

export interface AssigneeOption {
  value: string
  label: string
  count: number
}

/**
 * 「承接者」过滤的选项：全部 / 未指派 / 清单里出现过的每个承接者（按任务数从多到少）。
 * 清单里一条指派都没有时返回空数组——没有协作就不出现这个过滤，单工作流的界面不变。
 */
export function assigneeOptions(items: Pick<WorkItem, 'assignee_workflow_id'>[], known: { workflow_id: string; title?: string }[]): AssigneeOption[] {
  const counts = new Map<string, number>()
  let none = 0
  for (const it of items) {
    if (it.assignee_workflow_id) counts.set(it.assignee_workflow_id, (counts.get(it.assignee_workflow_id) || 0) + 1)
    else none++
  }
  if (counts.size === 0) return []
  const people = [...counts.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0], 'zh-CN'))
    .map(([id, count]) => ({ value: id, label: workflowLabel(id, known), count }))
  return [{ value: ASSIGNEE_ALL, label: '全部承接者', count: items.length }, { value: ASSIGNEE_NONE, label: '未指派', count: none }, ...people]
}

/**
 * 共享板的行上是否标出创建者：任务不是「正在看的这个工作流」创建的才标（和 MCP 回执的 workflow 字段同一个口径）；
 * 只有一个工作流的板、独立板上永远不标。
 */
export function showCreator(item: Pick<WorkItem, 'workflow_id'>, viewer: string, sharedBoard: boolean): boolean {
  return sharedBoard && !!viewer && !!item.workflow_id && item.workflow_id !== viewer
}

// ---------- 待接取与协作快捷筛选（#180 B3b） ----------

/**
 * 指派超过这么久还没人接，行上的「已等」标红：这是出厂默认（2 小时）。#180 B3d 起阈值可以在设置里改，
 * 实际用的是服务端给出的生效值（stores/notifyPrefs 的 boardStaleMs，与回执头 board: 行、久无人接的提醒同一个数）；
 * 这里的常量只在偏好还没取到、或没传阈值时顶替。
 */
export const OFFER_STALE_MS = 2 * 60 * 60 * 1000

/** 服务端 GET /work-items 的 offered：某个工作流手上待接取的协作任务个数与最早派出时间（侧栏徽标用）。 */
export interface WorkOffered {
  workflow_id: string
  count: number
  oldest_at?: string
}

/** 等待时长的人话：不到 1 分钟 / 45 分钟 / 3 小时 20 分 / 2 天 3 小时（48 小时以内按小时 + 分钟，再久按天 + 小时）。 */
export function formatWaited(ms: number): string {
  const min = Math.floor(Math.max(0, ms) / 60_000)
  if (min < 1) return '不到 1 分钟'
  if (min < 60) return `${min} 分钟`
  const hours = Math.floor(min / 60)
  if (hours < 48) {
    const rest = min % 60
    return rest ? `${hours} 小时 ${rest} 分` : `${hours} 小时`
  }
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours ? `${days} 天 ${restHours} 小时` : `${days} 天`
}

type OfferFields = Pick<WorkItem, 'state' | 'claim_state' | 'assignee_workflow_id' | 'assigned_by_workflow_id' | 'assigned_at'>

/** 待接取的协作任务已经等了多久（毫秒）；不是「协作任务、待接取、还没结束」或没有派出时间时返回 null。 */
export function offerWaitMs(item: OfferFields, now: number = Date.now()): number | null {
  if (item.claim_state !== 'offered' || !item.assignee_workflow_id || isClosedState(item.state)) return null
  if ((item.assigned_by_workflow_id || '') === item.assignee_workflow_id) return null
  const at = item.assigned_at ? Date.parse(item.assigned_at) : NaN
  if (Number.isNaN(at)) return null
  return Math.max(0, now - at)
}

export interface OfferWaitView {
  /** 「已等 3 小时 20 分」；还不到 1 分钟写「刚派出」（「已等 不到 1 分钟」读着别扭）。 */
  label: string
  /** 等到了阈值（设置里的「久无人接」时长，默认 2 小时）：标红，提醒派发者去催或改派。 */
  stale: boolean
}

/** 行上「已等 X」的取值；不是待接取的协作任务返回 null（行与以前一样）。staleMs 是「久无人接」的阈值（毫秒），等于即算。 */
export function offerWaitView(item: OfferFields, now: number = Date.now(), staleMs: number = OFFER_STALE_MS): OfferWaitView | null {
  const ms = offerWaitMs(item, now)
  if (ms === null) return null
  return { label: ms < 60_000 ? '刚派出' : `已等 ${formatWaited(ms)}`, stale: ms >= staleMs }
}

/** 侧栏徽标的悬停说明：「3 项指派在等这个工作流接取，最久已等 47 分钟」；最早的一项也还不到 1 分钟时写「刚派出」。 */
export function offeredBadgeTitle(o: WorkOffered, now: number = Date.now()): string {
  const at = o.oldest_at ? Date.parse(o.oldest_at) : NaN
  let tail = ''
  if (!Number.isNaN(at)) tail = now - at < 60_000 ? '，刚派出' : `，最久已等 ${formatWaited(now - at)}`
  return `${o.count} 项指派在等这个工作流接取${tail}`
}

/** 任务页的协作快捷筛选：指派给我 / 我派出的 / 待接取。「我」= 任务页正在看的工作流。 */
export const WORK_QUICK_KEYS = ['to_me', 'by_me', 'offered'] as const
export type WorkQuickKey = (typeof WORK_QUICK_KEYS)[number]

export const WORK_QUICK_META: Record<WorkQuickKey, { label: string; hint: string }> = {
  to_me: { label: '指派给我', hint: '别人（或你在网页上）派给当前工作流的任务，不含自己派给自己的' },
  by_me: { label: '我派出的', hint: '当前工作流派给别的工作流的任务' },
  offered: { label: '待接取', hint: '已派出、承接者还没有答复的任务' },
}

export function isWorkQuick(v: unknown): v is WorkQuickKey {
  return typeof v === 'string' && (WORK_QUICK_KEYS as readonly string[]).includes(v)
}

/**
 * 任务页地址里 ?seq=N 的取值（板内序号 #N，#242）。只认正整数的十进制写法，多值取第一个；
 * 空、abc、0、-1、1.5、1e3、0x10、前导零之类一律 null（调用方只把参数去掉、不打开任何任务）。
 *
 * 它和 ?task=（任务的数据库编号）是两回事：#N 是每张板各自从 1 数起的序号，写进 ?task= 会打开别的任务。
 */
export function parseSeqQuery(raw: unknown): number | null {
  const v = (Array.isArray(raw) ? raw[0] : raw) as unknown
  if (typeof v !== 'string') return null
  const s = v.trim()
  return /^[1-9]\d{0,8}$/.test(s) ? Number(s) : null
}

type QuickFields = Pick<WorkItem, 'state' | 'claim_state' | 'assignee_workflow_id' | 'assigned_by_workflow_id'>

/** 是否匹配协作快捷筛选（空串 / 不认识的值 = 都匹配）。to_me / by_me 没有 viewer 时什么都不匹配。 */
export function matchesQuick(item: QuickFields, quick: string, viewer: string): boolean {
  if (!isWorkQuick(quick)) return true
  const assignee = item.assignee_workflow_id || ''
  const by = item.assigned_by_workflow_id || ''
  switch (quick) {
    case 'to_me':
      return !!viewer && assignee === viewer && by !== viewer
    case 'by_me':
      return !!viewer && by === viewer && !!assignee && assignee !== viewer
    case 'offered':
      return item.claim_state === 'offered' && !isClosedState(item.state)
  }
}

/** 三个快捷项上的数字：还没结束的任务里各有几个（已结束的是历史，不算「待办」）。 */
export function quickCounts(items: QuickFields[], viewer: string): Record<WorkQuickKey, number> {
  const out: Record<WorkQuickKey, number> = { to_me: 0, by_me: 0, offered: 0 }
  for (const it of items) {
    if (isClosedState(it.state)) continue
    for (const k of WORK_QUICK_KEYS) if (matchesQuick(it, k, viewer)) out[k]++
  }
  return out
}

// ---------- 发起改进需求 ----------

/**
 * 发起改进需求时这段话交给智能体的方式：
 * session = 智能体正在等回复，这段话就是这一轮的回复；queued = 放进收件箱，它下次交互时读到。
 */
export type WorkImproveDelivery = 'session' | 'queued'
