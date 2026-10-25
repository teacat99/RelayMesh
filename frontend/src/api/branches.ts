// 分支会话（#236）的 REST 封装（对应 internal/api/branches.go 与 POST /workflows 的 parent_workflow_id）。
//
// 分支是一个独立的会话（自己的工作流编号、凭据与智能体），关系只是「谁是谁的主会话」加一份「向主会话开放多少」。
// 新建分支走 workflowsApi.create（带 parent_workflow_id）；这里是关系的读取、改设置与结束，以及主会话改过的参数记录与撤销（#236 P3）。
//
// 放宽授权（「改参数」档位调高、「启动」打开）要终端解锁：服务端回 403 unlock_required，调用方用 terminalStore.unlockOnDemand 包一层即可
// （请求头由 lib/terminal/unlockRetry 的 carriesUnlock 决定带不带）；终端不可用时服务端不拒绝，而是把放宽的项压回原值并在响应里带 clamped。

import { api } from './client'

/** 向主会话开放的范围（只有人能改）；与 internal/model/branch.go 的 BranchExpose 逐字段一致。 */
export interface BranchExpose {
  /** 主会话用 branch read 能看到多深的汇报。 */
  history: 'off' | 'summary' | 'full'
  decide: boolean
  /** 主会话能否给分支发消息（分支给主会话发消息恒允许）。 */
  say: boolean
  params: 'off' | 'resident' | 'resident_phase' | 'all'
  launch: boolean
  tasks: 'read' | 'assign'
}

export type BranchPreset = 'visible' | 'collab' | 'managed'
export type BranchStatus = 'active' | 'closed'

/** 一条「分支 → 主会话」的关系，外加界面要看的概况（标题、最近活动、是否在等回复、主会话未读数）。 */
export interface BranchBrief {
  id: number
  branch_workflow_id: string
  parent_workflow_id: string
  /** human | agent:<主会话工作流编号> | migrate */
  created_by: string
  expose: BranchExpose
  copy_user_notes: boolean
  status: BranchStatus
  created_at: string
  closed_at?: string
  /** 分支的标题。 */
  title: string
  parent_title?: string
  /** 开放范围与哪个预设完全一致；都不一致（自定义）时没有这个字段。 */
  preset?: BranchPreset
  last_activity_at?: string
  /** 分支有一轮汇报在等回复。 */
  waiting?: boolean
  /** 这个分支发给主会话、主会话还没读的消息数。 */
  unread_to_main?: number
  /** 这个工作流在库里已经没有任何会话行（关系已失效）。 */
  missing?: boolean
  /** 路由给主会话、还在等它定夺的请示数（#236 P2；只有活动关系才有，0 时没有这个字段）。 */
  pending_asks?: number
  /**
   * 分支的 Agent 状态（#236 P4；只有活动关系才有）：取服务端取数那一刻的状态跟踪器值（working / idle / waiting_you / … / stalled），
   * 从没接入过 hook 或没有任何会话是 unknown。面板上的状态点用状态 store 的实时数据，这个字段给没有实时数据的消费者（MCP 同口径）。
   */
  agent_state?: string
  agent_state_at?: string
  /** 归属这个分支、还没做完的任务数（按状态分，为 0 的不出现；三项都是 0 时整个字段不出现）。 */
  tasks?: BranchTasks
  /** 分支的启动方式（#236 P4；预设列表读不出来时没有这个字段）。 */
  launch?: BranchLaunch
  /** 「任务」派单的状态（#236 P5；只有活动关系才有，关系很多时服务端只给前面一批）。 */
  assign?: BranchAssign
}

/**
 * 分支的「任务」派单状态：open 是开放范围里「任务」= 派单（主会话的智能体能把它自己建的任务指派给这个分支）；
 * board_shared 是分支与主会话是否在同一张项目任务板上——派单要求同板，分支还没有会话、或会话目录没登记进主会话所在的项目时为假。
 */
export interface BranchAssign {
  open: boolean
  board_shared: boolean
}

export interface BranchTasks {
  todo?: number
  doing?: number
  review?: number
}

/**
 * 分支的启动方式：open 是开放范围里的「启动」开着没有；presets 是指向这个分支的非交互续聊预设条数（不论主机在不在线）；
 * preset / name 是按名字排序的第一条。此刻能不能运行（主机在线、上次运行结束、账本余量）不在这里——那是 preset_run list 的事。
 */
export interface BranchLaunch {
  open: boolean
  presets: number
  preset?: string
  name?: string
}

export type BranchRole = 'main' | 'branch' | 'none'

/** 消息发言者：main 主会话的智能体 / branch 分支的智能体 / human 用户（在另一个会话页里说的，被抄送过来）/ system。 */
export interface BranchActor {
  kind: 'main' | 'branch' | 'human' | 'system' | string
  workflow?: string
}

/** 会话页时间线里的一项：一条消息，或一行事件（分支建立 / 结束）。 */
export interface BranchFeedItem {
  kind: 'message' | 'event'
  id?: number
  at: string
  actor: BranchActor
  /** 发言者所在会话的标题。 */
  actor_title?: string
  text?: string
  /** message：queued 对方还没读 / delivered 已读 / acked 已确认 / expired。 */
  state?: string
  /**
   * event：created | closed | ask_routed（请示先送主会话定夺）| escalated（请示转交 / 升级给用户）| main_answered（主会话代为答复了请示）|
   * params_changed（主会话改了分支的一项参数）| params_reverted（你撤销了主会话的一次修改）。
   */
  event?: 'created' | 'closed' | 'ask_routed' | 'escalated' | 'main_answered' | 'params_changed' | 'params_reverted' | string
  /** 关联的分支工作流编号。 */
  branch?: string
  /** event：谁做的（human / agent:<wf> / migrate）。 */
  by?: string
  /** message：是本会话的智能体发出去的（收件人是 to）；否则是别人发给本会话的。 */
  outgoing?: boolean
  to?: string
  /** event（ask_routed / escalated / main_answered）：请示编号（Q12）与标题。escalated 时 by 是 agent:<wf> | timeout | system，text 是转交说明。 */
  ref?: string
  ref_title?: string
  /** event（params_changed / params_reverted）：改的是哪一项（resident / phase / wake_resume / wake_new / title / async），text 是一句话说明；id 是参数记录的编号。 */
  field?: string
  /** params_changed：这次修改已被你撤销。 */
  reverted?: boolean
  /** params_changed：现在还能撤销（它是该项最新一条未撤销的修改，当前值也仍是它写入的值）；不可撤销时省略。 */
  revertable?: boolean
  /** params_reverted：撤销的是哪一条记录。 */
  revert_of?: number
}

export interface BranchFeed {
  role: BranchRole
  /** role = branch：自己作为分支的那条关系。 */
  relation?: BranchBrief
  parent_title?: string
  /** role = main：自己的全部分支（含已结束）。 */
  branches?: BranchBrief[]
  items: BranchFeedItem[]
  /** 工作流编号 → 标题。 */
  titles?: Record<string, string>
}

export interface UpdateBranchRequest {
  expose_preset?: BranchPreset
  /** 显式开放范围必须是完整对象（缺项服务端会拒绝）。 */
  expose?: BranchExpose
  copy_user_notes?: boolean
}

/** 因为终端不可用（没设 Web 密码 / 密钥偏弱 / 被关闭）而没能放宽的项；其余设置已照常保存。 */
export type BranchClamped = 'params' | 'launch'

export interface UpdateBranchResult {
  branch: BranchBrief
  /** 被压回原值的放宽项；没有压回时是空数组。 */
  clamped: BranchClamped[]
}

/**
 * 主会话经 session_params 改过的一条参数记录（GET …/branch/params-events）。不含旧值 / 新值全文：
 * 常驻规则与唤醒词的全文在各自的弹窗里看，这里只给一句话。
 */
export interface BranchParamEvent {
  id: number
  created_at: string
  /** resident | phase | wake_resume | wake_new | title | async */
  field: string
  /** 字段的中文名（常驻规则 / 阶段 / …）。 */
  field_label: string
  /** agent:<主会话工作流编号>（主会话写入）或 human（你撤销）。 */
  by: string
  summary: string
  /** 常驻规则写入后的版本号。 */
  rev?: number
  /** 分支的回执已经把这条告诉过它。 */
  told: boolean
  reverted_at?: string
  reverted_by?: string
  /** 这是一条撤销记录，指回被撤销的那条。 */
  revert_of?: number
  /** 现在能不能撤销；不能撤销的主会话写入附带原因 revert_blocked。 */
  revertable: boolean
  revert_blocked?: string
}

/** 决策路由的全局偏好（GET / PUT /branch/prefs）。 */
export interface BranchPrefs {
  /** 高风险（L2）请示不让主会话代答，只能转交给用户。 */
  l2_guard: boolean
  /** 存的值：0 = 用默认。 */
  escalate_minutes: number
  /** 实际生效的时限（分钟）。 */
  escalate_after_minutes: number
  default_escalate_minutes: number
  min_escalate_minutes: number
  max_escalate_minutes: number
}

export interface UpdateBranchPrefsRequest {
  l2_guard?: boolean
  /** 0 = 恢复默认；其余必须在 [min, max] 内。 */
  escalate_minutes?: number
}

export const branchesApi = {
  /** 全部分支关系（活动 + 已结束，最新在前，最多 500 条）。 */
  async list(): Promise<BranchBrief[]> {
    const res = await api.get('/branches')
    return (res.data?.branches ?? []) as BranchBrief[]
  },

  /** 一个会话页的分支消息流：自己的角色、关系概况、两个方向的消息与建立 / 结束事件。 */
  async feed(workflowId: string, limit = 100): Promise<BranchFeed> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/branch-feed`, { params: { limit } })
    return res.data.branch_feed as BranchFeed
  },

  /**
   * 改一个分支向主会话开放的范围与「抄送」开关（workflowId 是分支自己的编号）。
   * 放宽「改参数」档位或打开「启动」要终端解锁（403 unlock_required）；终端不可用时放宽的项被压回原值，见返回的 clamped。
   */
  async update(workflowId: string, patch: UpdateBranchRequest): Promise<UpdateBranchResult> {
    const res = await api.put(`/workflows/${encodeURIComponent(workflowId)}/branch`, patch)
    return { branch: res.data.branch as BranchBrief, clamped: (res.data.clamped ?? []) as BranchClamped[] }
  },

  /** 主会话改过这个分支的哪些参数（新的在前；默认 50 条，最多 200 条），每条带「现在能不能撤销」。 */
  async paramEvents(workflowId: string, limit = 50): Promise<BranchParamEvent[]> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/branch/params-events`, { params: { limit } })
    return (res.data?.events ?? []) as BranchParamEvent[]
  },

  /** 撤销主会话的一次修改（只要登录，不需要解锁）；不能撤销时服务端回 409 并写明原因。 */
  async revertParamEvent(workflowId: string, eventId: number): Promise<{ reverted: BranchParamEvent; event: BranchParamEvent }> {
    const res = await api.post(`/workflows/${encodeURIComponent(workflowId)}/branch/params-events/${eventId}/revert`)
    return { reverted: res.data.reverted as BranchParamEvent, event: res.data.event as BranchParamEvent }
  },

  /** 决策路由偏好：主会话没答复多久后转交给用户、L2 请示是否禁止主会话代答。 */
  async prefs(): Promise<BranchPrefs> {
    const res = await api.get('/branch/prefs')
    return res.data.prefs as BranchPrefs
  },

  async updatePrefs(patch: UpdateBranchPrefsRequest): Promise<BranchPrefs> {
    const res = await api.put('/branch/prefs', patch)
    return res.data.prefs as BranchPrefs
  },

  /**
   * 结束一个分支（幂等；关系与历史保留）。returnedTasks 是因此退回主会话名下的任务序号：主会话派给它、还没交付的待办 / 进行中任务（#236 P5）；
   * 没有就是空数组。
   */
  async close(workflowId: string): Promise<{ changed: boolean; branch: BranchBrief; returnedTasks: number[] }> {
    const res = await api.post(`/workflows/${encodeURIComponent(workflowId)}/branch/close`)
    return { changed: !!res.data.changed, branch: res.data.branch as BranchBrief, returnedTasks: (res.data.returned_tasks ?? []) as number[] }
  },
}
