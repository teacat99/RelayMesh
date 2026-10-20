// 侧栏会话卡的「会话级状态」（#159，Q23 选 b；D-283）。
//
// 页头的状态徽标早已改成会话级（lib/sessionStatus.ts：等待您 · N 项 → 智能体进行中 → 已取消 / 已超时 → 空闲 → 已完成），
// 侧栏的会话卡却还在显示「最近一轮汇报」的状态（已完成 / 已提交 / 等待确认……）——同一个会话，页头说「智能体进行中」，
// 侧栏说「已完成」，互相打架。现在两处走同一个推导函数 deriveSessionStatus，只是取数的来源不同：
//   - 页头正在看的工作流：确认项 store 与收件箱轮询的实时数据（页头就是用的这一批）；
//   - 其余工作流：会话列表接口顺带给出的两个只读聚合（待处理确认项数、智能体最近动作时间），
//     待处理条数再由 confirm_updated 事件里的 pending_count 随时校准（不必为此重拉整张列表）。
// 任务数与 Agent 状态（hook）本来就是全局 store，两边一致。
//
// 这里全是纯函数（聚合怎么合并、取哪一路数据、推出什么状态），store 与界面只负责接线。
import type { AgentStateView, AgentTone } from './agentState'
import { deriveSessionStatus, type SessionStatusView } from './sessionStatus'

/**
 * 侧栏卡上「智能体进行中」的写法（追加 #264）：与输入区的 Agent 状态芯片同款——绿色脉动点 + 盲文旋转符 + 「工作中」，
 * 不再是蓝色的「智能体进行中」小标签。只改侧栏卡；页头的会话级徽标仍写「智能体进行中」（见 lib/sessionStatus.ts）。
 */
export const SIDEBAR_WORKING_TEXT = '工作中'

/** 侧栏「工作中」这一支的外观：色调、文字、是否带「?」、是否还画旋转符，以及要补进悬停提示的那一行。 */
export interface SidebarWorkingLook {
  tone: AgentTone
  text: string
  /** 「可能卡住」时在文字后带一个黄色「?」（与页头徽标同款）。 */
  flag: boolean
  /** 旋转符表示「在动」：疑似卡住时不再画，免得红字旁边还在转。 */
  spinner: boolean
  /** 补进悬停提示的说明（多行时原样保留）；没有就是空串。 */
  hint: string
}

/**
 * 侧栏「工作中」这一支该长什么样（追加 #537）：颜色跟页头状态徽标走同一份判断（stateView），不再写死成「工作中」的绿色。
 *   - 可能卡住（软标记 stalled_soft，色调 warn）→ 黄色「工作中」+「?」，悬停写明原因（与页头徽标同款）；
 *   - 疑似卡住（硬标记 stalled，色调 danger）→ 红色「疑似卡住」，不再转圈；
 *   - 其余（含这个工作流还没有 hook 汇报）→ 原来的绿色「工作中」。
 * 传 null / undefined 表示没有 hook 汇报：此时侧栏没有 Agent 状态可参考，保持原样。
 */
export function sidebarWorkingLook(view: AgentStateView | null | undefined): SidebarWorkingLook {
  if (view?.key === 'stalled') {
    return { tone: 'danger', text: view.text, flag: false, spinner: false, hint: view.title }
  }
  if (view?.key === 'working' && view.flag === 'stalled_soft') {
    return { tone: 'warn', text: SIDEBAR_WORKING_TEXT, flag: true, spinner: true, hint: `提示：${view.flagText}` }
  }
  return { tone: 'active', text: SIDEBAR_WORKING_TEXT, flag: false, spinner: true, hint: '' }
}

/** 一个工作流的聚合：会话列表里同工作流的各行取值一致，所以按工作流存一份。 */
export interface WorkflowAggregate {
  /** 待答复的确认项数；null = 服务端没给（旧版本，或聚合查询失败）。 */
  pendingConfirms: number | null
  /** 智能体最近一次与 RelayMesh 交互的时间（服务端内存里记，重启后清空）；null = 没有记录。 */
  agentActivityAt: string | null
  /** 待处理条数最近一次被 confirm_updated 事件校准的时间（毫秒）；0 = 只来自会话列表。 */
  pendingPatchedAt: number
}

export type WorkflowAggregates = Record<string, WorkflowAggregate>

/** 会话行里与聚合有关的字段（FeedbackSession 的子集，便于测试）。 */
export interface AggregateRow {
  workflow_id?: string
  pending_confirm_count?: number
  agent_last_activity_at?: string | null
}

/**
 * 两个「智能体最近动作」时间取较晚的一个（时间只会往后走，谁知道得晚谁更准）：
 * 会话列表里的聚合与收件箱轮询是两条独立的取数路径，刷新节奏不同，任何一边都可能落后，取较晚的。
 */
export function laterIso(a: string | null | undefined, b: string | null | undefined): string | null {
  if (!a) return b || null
  if (!b) return a
  const ta = Date.parse(a)
  const tb = Date.parse(b)
  if (Number.isNaN(ta)) return b
  if (Number.isNaN(tb)) return a
  return tb >= ta ? b : a
}

/**
 * 把一批会话行里的聚合并进已有的表；没有变化返回 null（调用方就不必替换响应式对象）。
 * - 没有工作流编号、或没带 pending_confirm_count 的行（旧服务端 / 聚合失败）跳过，保留手上已有的；
 * - requestStartedAt 是这次请求发出的时间：请求在途期间 confirm_updated 事件已经校准过的待处理条数以事件为准，
 *   免得旧快照把它打回去；
 * - 智能体最近动作时间只会往后走，取较晚的一个。
 */
export function mergeWorkflowAggregates(
  prev: WorkflowAggregates,
  rows: readonly AggregateRow[],
  requestStartedAt: number,
): WorkflowAggregates | null {
  let next: WorkflowAggregates | null = null
  const seen = new Set<string>()
  for (const row of rows) {
    const wf = row.workflow_id
    if (!wf || seen.has(wf) || typeof row.pending_confirm_count !== 'number') continue
    seen.add(wf)
    const cur = (next ?? prev)[wf]
    const patchedAfterRequest = !!cur && cur.pendingPatchedAt > requestStartedAt
    const merged: WorkflowAggregate = {
      pendingConfirms: patchedAfterRequest ? cur.pendingConfirms : row.pending_confirm_count,
      agentActivityAt: laterIso(cur?.agentActivityAt ?? null, row.agent_last_activity_at ?? null),
      pendingPatchedAt: patchedAfterRequest ? cur.pendingPatchedAt : 0,
    }
    if (
      cur &&
      cur.pendingConfirms === merged.pendingConfirms &&
      cur.agentActivityAt === merged.agentActivityAt &&
      cur.pendingPatchedAt === merged.pendingPatchedAt
    ) {
      continue
    }
    if (!next) next = { ...prev }
    next[wf] = merged
  }
  return next
}

/** confirm_updated 事件带来的权威待处理条数：直接覆盖，并记下校准时间。 */
export function patchPendingCount(
  prev: WorkflowAggregates,
  workflowId: string,
  count: number,
  nowMs: number,
): WorkflowAggregates {
  const cur = prev[workflowId]
  return {
    ...prev,
    [workflowId]: {
      pendingConfirms: Math.max(0, Math.floor(count)),
      agentActivityAt: cur?.agentActivityAt ?? null,
      pendingPatchedAt: nowMs,
    },
  }
}

/** 页头正在看的工作流的实时数据（页头徽标用的就是这一批）。 */
export interface LiveWorkflowSources {
  /** 确认项 store 里待答复的条数；还没加载完（或 store 关注的是别的工作流）为 null。 */
  confirms: number | null
  /** 收件箱轮询得到的智能体最近动作；这个工作流的收件箱还没取到、或没有记录为 null / undefined。 */
  agentActivityAt: string | null | undefined
}

export interface SidebarStatusInput {
  /** 会话卡上的轮次状态：有待答复的一轮就是 pending，否则是最近一轮的状态。 */
  roundStatus: string
  live?: LiveWorkflowSources | null
  baseline?: Pick<WorkflowAggregate, 'pendingConfirms' | 'agentActivityAt'> | null
  /** Agent 状态机（hook）里这个工作流的主状态 key；没有接入为 null。 */
  agentState: string | null
  /** 这个工作流还没结束的任务数。 */
  openTasks: number
  nowMs: number
}

/**
 * 取数：待处理条数实时数据优先（与页头一致），没有就用列表里的聚合，再没有按 0 算；
 * 智能体最近动作取实时（收件箱轮询）与聚合（会话列表）里较晚的一个，都没有就是没有记录。
 */
export function resolveSidebarSources(input: Pick<SidebarStatusInput, 'live' | 'baseline'>): {
  pendingItems: number
  agentActivityAt: string | null
} {
  const { live, baseline } = input
  const pendingItems = live?.confirms ?? baseline?.pendingConfirms ?? 0
  const agentActivityAt = laterIso(live?.agentActivityAt, baseline?.agentActivityAt)
  return { pendingItems, agentActivityAt }
}

export function sidebarSessionStatus(input: SidebarStatusInput): SessionStatusView | null {
  const { pendingItems, agentActivityAt } = resolveSidebarSources(input)
  return deriveSessionStatus({
    roundStatus: input.roundStatus,
    pendingItems,
    agentActivityAt,
    agentState: input.agentState,
    openTasks: input.openTasks,
    nowMs: input.nowMs,
  })
}
