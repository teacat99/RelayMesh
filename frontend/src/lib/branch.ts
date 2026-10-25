// 分支会话（#236）的界面逻辑：全是纯函数，不依赖 Vue，方便单测。
//
// 分支是一个独立的会话，侧栏里收在主会话下面（一层树）；会话页里分支与主会话之间的消息用「具名卡」显示，
// 分清是谁说的——主会话的智能体、分支的智能体、用户在分支页里说的（抄送），还是系统。

import type { BranchBrief, BranchExpose, BranchFeed, BranchFeedItem, BranchParamEvent, BranchPreset, BranchTasks } from '../api/branches'

/** 一个主会话最多同时有几个活动分支（与服务端 MaxActiveBranches 一致；这里只用来提前提示，真正的校验在服务端）。 */
export const MAX_ACTIVE_BRANCHES = 8

export interface BranchPresetInfo {
  id: BranchPreset
  label: string
  /** 一句话说明这一档会让主会话能做什么。 */
  hint: string
}

/**
 * 向主会话开放多少：三个一键预设。六项开放（「读历史」「发消息」「定夺」「改参数」「启动」「任务」）都已真正生效（见 docs/branch-sessions.md），
 * 说明文字照实写每一档让主会话能做什么，不夸大。
 */
export const BRANCH_PRESETS: ReadonlyArray<BranchPresetInfo> = [
  { id: 'visible', label: '仅可见', hint: '主会话只能读到每一轮的摘要，不能给分支发消息，也不替分支定夺请示，更不能改分支的参数、给它派任务。分支仍可以随时给主会话发消息。' },
  { id: 'collab', label: '协作（推荐）', hint: '主会话能读完整的汇报，也能给分支发消息，还能替分支定夺请示（自己答复，或转交给你），并改分支的常驻规则（改动记在分支页，你可以撤销）；还能把自己建的任务派给分支（要两边在同一个项目的任务板上）；两边的智能体可以互相请示、同步进度。' },
  { id: 'managed', label: '托管', hint: '包含协作的全部能力，并能改分支的阶段、标题、唤醒词，以及关掉异步对话，还能用你建好的续聊预设启动（唤醒）这个分支的智能体。' },
]

/** 预设的显示名；开放范围和哪个预设都不完全一致时叫「自定义」。 */
export function presetLabel(b: Pick<BranchBrief, 'preset'>): string {
  const hit = BRANCH_PRESETS.find(p => p.id === b.preset)
  if (!hit) return '自定义'
  return hit.label.replace('（推荐）', '')
}

/**
 * 开放范围的一行摘要，如「读历史：全文，可发消息，可定夺请示，可改参数：常驻规则，不可启动，可派任务」。六项都列
 * （读历史、发消息、定夺、改参数、启动、任务）；任务只认 `assign`，别的取值（含不认识的）一律按不可派（宁紧勿松）。
 * 与服务端 `BranchExpose.Summary` 保持一致（逐字相同，见 branch.test.ts 里钉住的样例）。
 */
export function exposeSummary(e: BranchExpose): string {
  const history = e.history === 'off' ? '不可读历史' : e.history === 'summary' ? '读历史：摘要' : e.history === 'full' ? '读历史：全文' : `读历史：${e.history}`
  return `${history}，${e.say ? '可发消息' : '不可发消息'}，${e.decide ? '可定夺请示' : '不可定夺请示'}，${paramsSummary(e.params)}，${e.launch ? '可启动' : '不可启动'}，${e.tasks === 'assign' ? '可派任务' : '不可派任务'}`
}

// ---------- 状态：任务数与启动方式（#236 P4）----------

/** 名下还没做完的任务总数（待办 + 进行中 + 验证中）；没有这个字段按 0 算。 */
export function openTaskTotal(t: BranchTasks | undefined | null): number {
  return (t?.doing ?? 0) + (t?.review ?? 0) + (t?.todo ?? 0)
}

/**
 * 任务数的一句话，如「2 进行中 · 1 验证中 · 3 待办」：只写不为 0 的，措辞与侧栏任务入口一致（lib/workItems 的 summarizeCounts），
 * 区别是这里三项都列（侧栏只在没有别的时才写待办）——主会话要看清分支还欠着多少。没有任务返回空串。
 */
export function openTasksText(t: BranchTasks | undefined | null): string {
  const parts: string[] = []
  if (t?.doing) parts.push(`${t.doing} 进行中`)
  if (t?.review) parts.push(`${t.review} 验证中`)
  if (t?.todo) parts.push(`${t.todo} 待办`)
  return parts.join(' · ')
}

// ---------- 状态：任务派单（#236 P5）----------

/**
 * 派单的四种状态：unknown 服务端没给、也看不出（关系很多时只给前面一批）；off「任务」是只读的；
 * unshared 打开了，但分支与主会话不在同一张任务板上；ready 打开了、也同板。
 */
export type AssignKind = 'unknown' | 'off' | 'unshared' | 'ready'

export interface AssignStatus {
  kind: AssignKind
  /** 芯片上的短文字。 */
  label: string
  /** 一句话说明与下一步（主会话行的悬停提示、分支视角的说明段落都用它）。 */
  hint: string
}

const ASSIGN_OFF_HOW = '到这个分支的会话页「分支」面板里选「协作」或「托管」预设，或在「逐项设置」里打开「主会话可以给分支派任务」（不需要再验证密码）'

/**
 * 分支的派单状态怎么说：开放范围里「任务」开着没有，加上两边是不是同一张任务板（派单要求同板）。
 * 服务端没给 assign 时，开放范围里「任务」是只读的照样说得准（不可派单），其余按未知处理，不假装可用。
 */
export function branchAssignStatus(b: Pick<BranchBrief, 'assign' | 'expose'>): AssignStatus {
  const a = b.assign
  const open = a ? a.open : b.expose?.tasks === 'assign'
  if (!open && (a || b.expose?.tasks)) {
    return { kind: 'off', label: '不可派单', hint: `主会话的智能体现在不能把任务派给这个分支：「任务」是只读的。${ASSIGN_OFF_HOW}。` }
  }
  if (!a) {
    return { kind: 'unknown', label: '派单状态未知', hint: '暂时读不到派单的状态，稍后再看。' }
  }
  if (!a.board_shared) {
    return {
      kind: 'unshared',
      label: '可派单 · 不同板',
      hint: '「任务」派单已打开，但这个分支和主会话不在同一张任务板上（派单要求同一个项目的板）：分支里的智能体还没有连上来（还没有会话），或它的会话目录没有登记进主会话所在的项目。在项目页把目录登记进去之后才能派单。',
    }
  }
  return {
    kind: 'ready',
    label: '可派单',
    hint: '主会话的智能体可以把它自己建的任务指派给这个分支，分支接取、交付后由主会话验收。派单只是放进分支的待办，不会叫醒分支的智能体——要它马上开始，主会话得给它发消息或启动它。',
  }
}

/**
 * 结束分支后退回主会话名下的任务的一句话（写进提示里）：没有退回返回空串。序号写法与任务页、MCP 回执一致（#12 #15）。
 */
export function returnedTasksText(seqs: ReadonlyArray<number> | undefined | null): string {
  const list = (seqs ?? []).filter(n => Number.isFinite(n) && n > 0)
  if (list.length === 0) return ''
  return `它没做完的 ${list.length} 个任务（${list.map(n => `#${n}`).join(' ')}）已退回主会话名下`
}

/** 启动方式的四种状态：unknown 读不出预设列表；off「启动」没打开；no-preset 打开了但还没有续聊预设；ready 打开了、也有预设。 */
export type LaunchKind = 'unknown' | 'off' | 'no-preset' | 'ready'

export interface LaunchStatus {
  kind: LaunchKind
  /** 芯片上的短文字。 */
  label: string
  /** 一句话说明与下一步（主会话行的悬停提示、分支视角的说明段落都用它；不引用面板里看不到的控件）。 */
  hint: string
}

const LAUNCH_OFF_HOW = '到这个分支的会话页「分支」面板里把「启动」打开（选「托管」预设，或在「逐项设置」里打开；要再验证一次密码）'
const LAUNCH_CREATE_HOW = '到这个分支的会话页点页头「运行」，在弹窗里为它创建一条续聊预设（待接入的会话点「启用 Agent」）'

/**
 * 分支的启动方式怎么说：开放范围里的「启动」开着没有，加上有几条指向它的续聊预设。**不说此刻能不能运行**——主机在线、
 * 上次运行有没有结束、频率余量这些要现查，由主会话的 preset_run list 回答；这里摆一个会过期的「能运行」比不摆更误导。
 * 没有 launch 字段（服务端读不出预设列表）时按未知处理，不假装「没有预设」。
 */
export function branchLaunchStatus(b: Pick<BranchBrief, 'launch' | 'expose'>): LaunchStatus {
  const l = b.launch
  if (!l) {
    return { kind: 'unknown', label: '启动方式未知', hint: '暂时读不到启动预设的列表，稍后再看。' }
  }
  const named = l.name ? `「${l.name}」` : ''
  const count = l.presets > 1 ? `（共 ${l.presets} 条）` : ''
  if (!l.open) {
    const have = l.presets > 0
      ? `已有续聊预设${named}${count}可以唤醒这个分支，但「启动」没有打开，主会话的智能体还不能运行它：${LAUNCH_OFF_HOW}。`
      : `主会话的智能体现在不能启动这个分支：「启动」没有打开，也还没有指向它的续聊预设。先${LAUNCH_OFF_HOW}；再${LAUNCH_CREATE_HOW}。`
    return { kind: 'off', label: '启动未开', hint: have }
  }
  if (l.presets === 0) {
    return {
      kind: 'no-preset',
      label: '启动已开 · 无预设',
      hint: `「启动」已打开，但还没有指向这个分支的续聊预设，主会话的智能体没有可运行的东西：${LAUNCH_CREATE_HOW}。`,
    }
  }
  return {
    kind: 'ready',
    label: l.presets > 1 ? `可启动 · ${l.presets} 条预设` : '可启动',
    hint: `主会话的智能体可以用 preset_run 运行${named || '预设'}${count}唤醒这个分支。运行仍要主机在线、受频率与权限限制；你关掉「启动」后，主会话下一次调用立刻被拒。`,
  }
}

// ---------- 「改参数」：主会话的智能体能不能改分支的参数（#236 P3）----------

export type ParamsLevel = BranchExpose['params']

export interface ParamsLevelInfo {
  id: ParamsLevel
  label: string
  /** 一句话说明这一档下主会话的智能体能改什么。 */
  hint: string
}

/**
 * 「改参数」四档，**由低到高**（与服务端 model.ExposeParams* 一致）：数组下标就是档位高低，paramsRank 靠它。
 * 档位越高，主会话的智能体能改的越多；授权档位、凭据、工作目录与主机无论哪一档都不开放。
 */
export const PARAMS_LEVELS: ReadonlyArray<ParamsLevelInfo> = [
  { id: 'off', label: '不可改', hint: '主会话的智能体改不了这个分支的任何参数。' },
  { id: 'resident', label: '常驻规则', hint: '主会话的智能体可以改这个分支的常驻规则（每一轮都会带上的那段话）。' },
  { id: 'resident_phase', label: '常驻规则 + 阶段', hint: '在常驻规则之外，还可以切换这个分支的阶段（讨论 / 方案 / 开发）。' },
  { id: 'all', label: '全部参数', hint: '再加上标题、唤醒词，以及关闭异步对话（只能关，不能替你打开）。授权档位、凭据、工作目录与主机永远不开放。' },
]

export interface HistoryOption {
  id: BranchExpose['history']
  label: string
  hint: string
}

/** 「主会话读历史」三档，由浅到深（与服务端 model.ExposeHistory* 一致）；分支给主会话发消息恒允许，不在这里设置。 */
export const HISTORY_OPTIONS: ReadonlyArray<HistoryOption> = [
  { id: 'off', label: '不可读', hint: '主会话读不到这个分支的任何汇报。' },
  { id: 'summary', label: '摘要', hint: '主会话只能读到每一轮汇报的摘要。' },
  { id: 'full', label: '全文', hint: '主会话能读到每一轮汇报的完整内容。' },
]

/** 提升「改参数」档位或打开「启动」属于放宽授权：服务端要求先在本机验证一次密码（解锁）。收紧、撤销与其余项只要登录。 */
export const PARAMS_UNLOCK_NOTE = '协作与托管含「改参数」，创建分支或把它调高时要再输入一次密码（已解锁则不用）；打开「启动」同样要；调低、撤销与其余设置只要登录。'

/** 改分支的开放范围时弹出解锁对话框给用户看的原因。 */
export const BRANCH_UNLOCK_REASON = '放宽主会话对分支的授权（改参数 / 启动）需要再次验证密码'

/** 新建协作 / 托管分支时弹出解锁对话框给用户看的原因。 */
export const NEW_BRANCH_UNLOCK_REASON = '新建协作 / 托管分支（含「改参数」授权）需要再次验证密码'

/** 档位的高低（0–3）；不认识的值按「不可改」算，与服务端 ExposeParamsRank 一致。 */
export function paramsRank(level: string | undefined): number {
  const i = PARAMS_LEVELS.findIndex(l => l.id === level)
  return i < 0 ? 0 : i
}

export function paramsLevelLabel(level: string | undefined): string {
  return PARAMS_LEVELS.find(l => l.id === level)?.label ?? PARAMS_LEVELS[0].label
}

/** 档位的一句话说明；不认识的值按「不可改」。 */
export function paramsLevelHint(level: string | undefined): string {
  return (PARAMS_LEVELS.find(l => l.id === level) ?? PARAMS_LEVELS[0]).hint
}

/** 「改参数」一行摘要，与服务端 `ExposeParams` 的 `ParamsSummary` 逐字一致；不认识的值按不可改。 */
export function paramsSummary(level: string | undefined): string {
  switch (level) {
    case 'resident':
      return '可改参数：常驻规则'
    case 'resident_phase':
      return '可改参数：常驻规则与阶段'
    case 'all':
      return '可改参数：常驻规则、阶段、标题、唤醒词、异步开关'
    default:
      return '不可改参数'
  }
}

/**
 * 这次改动是不是放宽了授权：「改参数」档位调高，或「启动」从关到开。放宽要终端解锁，收紧不用；
 * 与服务端 `ExposeWidens` 一致，只用来提前提示（真正的判断在服务端）。
 */
export function widensExpose(from: BranchExpose, to: BranchExpose): boolean {
  return paramsRank(to.params) > paramsRank(from.params) || (!from.launch && to.launch)
}

/** 放宽的项因为终端不可用被压回时的名字。 */
const CLAMPED_NAMES: Readonly<Record<string, string>> = { params: '「改参数」', launch: '「启动」' }

/**
 * 服务端因为终端不可用（没设 Web 密码、密钥偏弱或被关闭），没法验证密码，把放宽的项压回了原值、其余设置照常保存：
 * 给用户一句说明。没有压回返回空串。
 */
export function clampedNotice(clamped: ReadonlyArray<string> | undefined): string {
  const names = (clamped ?? []).map(c => CLAMPED_NAMES[c] ?? c)
  if (names.length === 0) return ''
  return `其余设置已保存，但${names.join('、')}没有放宽：放宽授权要先验证密码，而这台服务的终端功能现在不可用（需要先设置 Web 密码并开启终端功能）。`
}

/** 参数记录的三种身份：主会话写入且仍在生效 / 主会话写入但已被你撤销 / 这条是你的撤销记录。 */
export type ParamEventKind = 'agent' | 'reverted' | 'revert'

export function paramEventKind(ev: Pick<BranchParamEvent, 'revert_of' | 'reverted_at'>): ParamEventKind {
  if (ev.revert_of) return 'revert'
  return ev.reverted_at ? 'reverted' : 'agent'
}

/** 参数记录行左边的小标签。 */
export function paramEventTag(ev: Pick<BranchParamEvent, 'revert_of' | 'reverted_at'>): string {
  const k = paramEventKind(ev)
  return k === 'revert' ? '你撤销' : k === 'reverted' ? '已撤销' : '主会话改'
}

// ---------- 关系索引与侧栏分支树 ----------

export interface BranchIndex {
  /** 分支的工作流编号 → 它的活动关系。 */
  byBranch: ReadonlyMap<string, BranchBrief>
  /** 主会话的工作流编号 → 它的活动分支（按建立时间升序）。 */
  byParent: ReadonlyMap<string, ReadonlyArray<BranchBrief>>
}

/** 只索引活动的关系：已结束的分支回到普通会话的样子，不再缩进挂在主会话下面。 */
export function indexBranches(rels: ReadonlyArray<BranchBrief>): BranchIndex {
  const byBranch = new Map<string, BranchBrief>()
  const byParent = new Map<string, BranchBrief[]>()
  for (const r of rels) {
    if (r.status !== 'active') continue
    byBranch.set(r.branch_workflow_id, r)
    const list = byParent.get(r.parent_workflow_id)
    if (list) list.push(r)
    else byParent.set(r.parent_workflow_id, [r])
  }
  for (const list of byParent.values()) list.sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at))
  return { byBranch, byParent }
}

export interface TreeItem {
  id: string
  type: string
  workflow_id?: string
}

export interface BranchTree<T extends TreeItem> {
  /** 重排后的列表：每个活动分支紧跟在它的主会话后面（只在两者都在列表里时）。 */
  items: T[]
  /** 缩进挂在主会话下面的分支条目 id（没有出现在这里的条目都是顶层）。 */
  nested: ReadonlySet<string>
  /** 主会话条目 id → 它在这份列表里的活动分支数（含被折叠起来的）。 */
  childCount: ReadonlyMap<string, number>
}

/**
 * 把活动分支收到主会话下面。规则：
 * - 只有「分支和它的主会话都在这份（已过滤的）列表里」才缩进挂靠；主会话被过滤掉了，分支就按它自己的位置显示（仍带分支标记）；
 * - 同一主会话的几个分支保持它们在原列表里的相对次序；
 * - 折叠的主会话不显示它的分支（childCount 仍计数，界面据此显示「N 个分支」）；
 * - 没有任何分支关系时返回原列表的一份拷贝，次序一字不变。
 */
export function arrangeBranchTree<T extends TreeItem>(
  items: ReadonlyArray<T>,
  index: BranchIndex,
  collapsed: ReadonlySet<string> = new Set(),
): BranchTree<T> {
  const nested = new Set<string>()
  const childCount = new Map<string, number>()
  if (index.byBranch.size === 0) return { items: [...items], nested, childCount }

  const present = new Map<string, T>() // 工作流编号 → 列表里的条目
  for (const it of items) {
    if (it.type === 'feedback' && it.workflow_id) present.set(it.workflow_id, it)
  }
  const nestedUnder = (it: T): string | null => {
    if (it.type !== 'feedback' || !it.workflow_id) return null
    const rel = index.byBranch.get(it.workflow_id)
    if (!rel || rel.parent_workflow_id === it.workflow_id) return null
    // 深度只有一层：主会话自己又是别人的活动分支时不再嵌套（服务端也不允许，这里防御一下脏数据）
    if (index.byBranch.has(rel.parent_workflow_id)) return null
    return present.has(rel.parent_workflow_id) ? rel.parent_workflow_id : null
  }

  const children = new Map<string, T[]>() // 主会话工作流编号 → 列表里的分支条目（原列表次序）
  for (const it of items) {
    const parent = nestedUnder(it)
    if (!parent) continue
    const list = children.get(parent)
    if (list) list.push(it)
    else children.set(parent, [it])
  }

  const out: T[] = []
  for (const it of items) {
    if (nestedUnder(it)) continue // 由主会话带出
    out.push(it)
    const kids = it.workflow_id ? children.get(it.workflow_id) : undefined
    if (!kids?.length) continue
    childCount.set(it.id, kids.length)
    if (collapsed.has(it.workflow_id as string)) continue
    for (const kid of kids) {
      out.push(kid)
      nested.add(kid.id)
    }
  }
  return { items: out, nested, childCount }
}

// ---------- 页头标签 ----------

export interface BranchChipView {
  kind: 'branch' | 'main'
  text: string
  title: string
}

/**
 * 页头标题行上的分支标记：分支显示「分支 · 主会话「X」」（点一下回到主会话）；主会话有活动分支时显示「N 个分支」（点一下打开分支面板）；
 * 没有关系、或只剩已结束的分支时不出现——没用到分支的会话页头上不多任何标签。
 */
export function branchChipView(asBranch: BranchBrief | undefined, activeBranches: number): BranchChipView | null {
  if (asBranch) {
    const parent = quoted(asBranch.parent_title || '', asBranch.parent_workflow_id)
    const closed = asBranch.status === 'closed'
    return {
      kind: 'branch',
      text: `${closed ? '已结束的分支' : '分支'} · 主会话${parent}`,
      title: `这是主会话${parent}的${closed ? '一个已结束的' : ''}分支：点一下回到主会话`,
    }
  }
  if (activeBranches > 0) {
    return { kind: 'main', text: `${activeBranches} 个分支`, title: `这个会话有 ${activeBranches} 个进行中的分支：点一下查看` }
  }
  return null
}

// ---------- 会话页的分支消息流 ----------

/** 这个会话能不能新建分支：没有关系的会话和主会话可以；分支不能再建分支（深度只有一层）。 */
export function canCreateBranch(role: BranchFeed['role'] | undefined, activeBranches: number): { ok: boolean; reason: string } {
  if (role === 'branch') return { ok: false, reason: '这个会话本身就是分支；分支下面不能再建分支（深度只有一层）。' }
  if (activeBranches >= MAX_ACTIVE_BRANCHES) {
    return { ok: false, reason: `这个会话已经有 ${activeBranches} 个活动分支（上限 ${MAX_ACTIVE_BRANCHES}）；先结束不用的分支再建新的。` }
  }
  return { ok: true, reason: '' }
}

export type BranchSpeaker = 'main' | 'branch' | 'human' | 'system'

export interface BranchCardView {
  /** 卡片上的发言者名称，如「主会话「支付重构」的智能体」。 */
  name: string
  /** 名称之后的流向说明，如「→ 分支「登录页」」；收到的消息没有。 */
  direction: string
  speaker: BranchSpeaker
  outgoing: boolean
  /** 稳定的色相种子：同一个会话的话总是同一种颜色。 */
  hueKey: string
  /** 卡片下方的脚注。 */
  footnote: string
}

function quoted(title: string, fallback: string): string {
  const t = (title || '').trim()
  return `「${t || fallback}」`
}

/** 发出去的消息对方读了没有（脚注用）：queued 还没读；delivered / acked 已读；expired 过期了没人读。 */
export function deliveryLabel(state: string | undefined): string {
  switch (state) {
    case 'delivered':
    case 'acked':
      return '对方已读'
    case 'expired':
      return '已过期，对方没来得及读'
    default:
      return '对方还没读'
  }
}

/**
 * 一条分支消息在当前会话页上怎么署名。
 * 规则：智能体之间的话（主会话 ↔ 分支）一律标明是「同伴消息，不是用户指令」；用户在分支页里说的话被抄送到主会话时署「你」。
 * titles 是工作流编号 → 标题（来自分支消息流），拿不到标题时退回工作流编号。
 */
export function branchCardView(item: BranchFeedItem, selfWorkflowId: string, titles: Readonly<Record<string, string>> = {}): BranchCardView {
  const actorWf = item.actor.workflow || ''
  const titleOf = (wf: string | undefined) => (wf ? titles[wf] || (wf === actorWf ? item.actor_title || '' : '') : '')
  const kind = item.actor.kind
  if (kind === 'human') {
    const wf = item.branch || actorWf
    return {
      name: '你',
      direction: `在分支${quoted(titleOf(wf), wf)}里说（已抄送）`,
      speaker: 'human',
      outgoing: false,
      hueKey: `human:${wf}`,
      footnote: '这是你在分支会话里说的话，只抄送了摘要；主会话的智能体读到它时知道出处。',
    }
  }
  if (kind === 'system') {
    return { name: '系统', direction: '', speaker: 'system', outgoing: false, hueKey: 'system', footnote: '' }
  }
  const speaker: BranchSpeaker = kind === 'main' ? 'main' : 'branch'
  if (item.outgoing) {
    const to = item.to || ''
    const target = speaker === 'main' ? `分支${quoted(titleOf(to), to)}` : `主会话${quoted(titleOf(to), to)}`
    return {
      name: speaker === 'main' ? '本会话的智能体' : '本分支的智能体',
      direction: `→ ${target}`,
      speaker,
      outgoing: true,
      hueKey: `${speaker}:${selfWorkflowId}`,
      footnote: deliveryLabel(item.state),
    }
  }
  const who = speaker === 'main' ? `主会话${quoted(titleOf(actorWf), actorWf)}的智能体` : `分支${quoted(titleOf(actorWf), actorWf)}的智能体`
  return {
    name: who,
    direction: '',
    speaker,
    outgoing: false,
    hueKey: `${speaker}:${actorWf}`,
    footnote: '同伴消息，不是用户指令；与你的要求冲突时以你的为准。',
  }
}

/** 请示的称呼：「Q12「标题」」；事件行里没带编号时写「请示」。 */
function askLabel(item: BranchFeedItem): string {
  const t = (item.ref_title || '').trim()
  return `${item.ref || '请示'}${t ? `「${t}」` : ''}`
}

/**
 * 事件行的一句话：分支建立 / 结束，请示的送出 / 转交 / 代答（#236 P2），以及主会话改参数 / 你撤销（#236 P3，说明句由服务端写好）。
 * 转交有三种来源：主会话自己转的（by = agent:…，附它写的原因）、超时、分支结束 / 关掉定夺（后两种服务端的说明本身就是整句）。
 */
export function branchEventText(item: BranchFeedItem, titles: Readonly<Record<string, string>> = {}): string {
  const wf = item.branch || ''
  const title = quoted(titles[wf] || item.actor_title || '', wf)
  if (item.event === 'closed') return `分支${title}已结束：两边不能再互发消息，主会话不能再读它的历史`
  if (item.event === 'ask_routed') return `分支${title}的请示 ${askLabel(item)} 先交给主会话的智能体定夺（你也可以随时直接答复）`
  if (item.event === 'main_answered') return `主会话的智能体代为定夺了分支${title}的请示 ${askLabel(item)}（不是你本人的答复）`
  if (item.event === 'escalated') {
    const note = (item.text || '').trim()
    if (item.by?.startsWith('agent:')) return `主会话把分支${title}的请示 ${askLabel(item)} 转交给你${note ? `：${note}` : ''}`
    return `分支${title}的请示 ${askLabel(item)}：${note || '已转交给你定夺'}`
  }
  if (item.event === 'params_changed') {
    const note = (item.text || '').trim()
    return `主会话的智能体改了分支${title}的参数${note ? `：${note}` : ''}${item.reverted ? '（已被你撤销）' : ''}`
  }
  if (item.event === 'params_reverted') return `分支${title}：${(item.text || '').trim() || '你撤销了主会话的一次参数修改'}`
  const by = item.by === 'human' ? '你建立' : item.by === 'migrate' ? '由迁移命令建立' : item.by?.startsWith('agent:') ? '由主会话的智能体建立' : ''
  return `分支${title}已建立${by ? `（${by}）` : ''}`
}

/** 事件行左边的小标签：改参数 / 撤销写「参数」，其余（建立、结束、请示的流转）写「分支」。 */
export function branchEventChip(item: Pick<BranchFeedItem, 'event'>): string {
  return item.event === 'params_changed' || item.event === 'params_reverted' ? '参数' : '分支'
}

/** 一条分支消息的预览（右侧轨 / 摞里的一行）。 */
export function branchPreview(item: BranchFeedItem): string {
  if (item.kind === 'event') {
    if (item.event === 'closed') return '分支已结束'
    if (item.event === 'ask_routed') return '请示交给主会话定夺'
    if (item.event === 'escalated') return '请示已转交给你'
    if (item.event === 'main_answered') return '主会话代答了一项请示'
    if (item.event === 'params_changed') return '主会话改了分支的参数'
    if (item.event === 'params_reverted') return '参数修改已撤销'
    return '分支已建立'
  }
  const text = (item.text || '').replace(/\s+/g, ' ').trim()
  const head = item.outgoing ? '→ ' : ''
  return head + (text.length > 60 ? `${text.slice(0, 60)}…` : text || '分支消息')
}

// ---------- 条目复用 ----------

function sameFeedItem(a: BranchFeedItem, b: BranchFeedItem): boolean {
  return (
    a.kind === b.kind &&
    a.id === b.id &&
    a.at === b.at &&
    a.text === b.text &&
    a.state === b.state &&
    a.event === b.event &&
    a.branch === b.branch &&
    a.by === b.by &&
    a.ref === b.ref &&
    a.ref_title === b.ref_title &&
    a.outgoing === b.outgoing &&
    a.to === b.to &&
    a.field === b.field &&
    a.reverted === b.reverted &&
    a.revertable === b.revertable &&
    a.revert_of === b.revert_of &&
    a.actor_title === b.actor_title &&
    a.actor.kind === b.actor.kind &&
    a.actor.workflow === b.actor.workflow
  )
}

/**
 * 一条消息 / 事件在时间线里的键：消息按行号，事件按（种类，分支）；请示的转交 / 代答事件再加请示编号（同一个分支会有好几条），
 * 参数事件再加记录编号（同一个分支会改很多次）。
 */
export function feedItemKey(item: BranchFeedItem): string {
  if (item.kind === 'message') return `branch-m${item.id ?? item.at}`
  const tail = item.ref ? `-${item.ref}` : item.event?.startsWith('params_') && item.id != null ? `-p${item.id}` : ''
  return `branch-e-${item.event ?? ''}-${item.branch ?? ''}${tail}`
}

// ---------- 决策路由偏好（设置页）----------

/**
 * 解析设置页里填的「主会话多久没答复就转交给你」（分钟）：留空 = 恢复默认（0）；其余必须是 [min, max] 内的整数。
 * 范围以服务端回的 min / max 为准（GET /branch/prefs），这里只做提交前的就地提示，真正的校验在服务端。
 */
export function parseEscalateMinutes(text: string, min: number, max: number): { ok: true; value: number } | { ok: false; reason: string } {
  const t = text.trim()
  if (t === '') return { ok: true, value: 0 }
  if (!/^\d+$/.test(t)) return { ok: false, reason: '请填整数分钟数' }
  const n = Number(t)
  if (n < min || n > max) return { ok: false, reason: `要在 ${min}–${max} 分钟之间` }
  return { ok: true, value: n }
}

/**
 * 重新取到的消息流里，凡是和上一次内容完全相同的条目都换回上一次的那个对象（时间线靠对象同一性跳过不必要的重渲染，
 * 原理见 lib/timeline.ts 的 reuseEntries）。一条都没变时数组也换回上一次的。
 */
export function reuseFeedItems(prev: ReadonlyArray<BranchFeedItem>, next: ReadonlyArray<BranchFeedItem>): BranchFeedItem[] {
  const old = new Map(prev.map(p => [feedItemKey(p), p]))
  let allSame = prev.length === next.length
  const out = next.map((n, i) => {
    const o = old.get(feedItemKey(n))
    const use = o && sameFeedItem(o, n) ? o : n
    if (allSame && use !== prev[i]) allSame = false
    return use
  })
  return allSame ? (prev as BranchFeedItem[]) : out
}
