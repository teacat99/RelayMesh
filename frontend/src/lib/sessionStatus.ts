// 会话页头的「会话级状态」（#120 · D-273 阶段 D；经典视图与预览视图共用同一个页头，所以两边同时生效）。
//
// 起因：页头徽标原来直接显示「最近一轮阻塞汇报」的状态。异步对话里阻塞汇报很少——智能体用进度通知发消息、用提问请示——
// 那一轮早就答完，徽标就常驻「已完成」，与「有没有等您处理的事」「智能体还在不在干活」都无关，等于失效。
//
// 现在按整个会话（工作流）推出，从上到下取第一个命中的：
//   已归档           最近一轮汇报已归档（生命周期状态，原样）
//   等待您 · N 项    有等回复的汇报，或有未答复的提问 / 计划 / 决策（点开列清单并定位）
//   智能体进行中     没有待处理，且智能体十分钟内有动作（装了 hook 时以 hook 汇报的「工作中 / 已收尾」为准）
//   待接入           网页手动新建、智能体还没接入的会话（#174：最近一行是占位会话，由调用方翻成 AWAITING_AGENT_ROUND）
//   已取消 / 已超时  最近一轮汇报的结局（原样）；智能体还在干活时让位给「进行中」，免得一次取消把状态钉死
//   空闲             没有待处理也无近期动作，但还有任务没结束
//   已完成           最近一轮汇报已确认、任务全结束、没有待处理
// 没有汇报、没有任务、也没有任何动静的会话不显示徽标（返回 null）。
//
// 纯逻辑，界面只负责呈现。「智能体最近一次交互」的口径与收件箱芯片是同一份（lib/inbox.ts 的 agentActivity：
// 一分钟内 / 十分钟内 / 更久 / 没有记录），所以页头与芯片对「智能体是否近期有动作」的说法一致。
import { agentActivity } from './inbox'

/**
 * 传给 deriveSessionStatus 的「轮次状态」里表示「待接入」的取值（#174）：它不是服务端的会话状态，
 * 只在前端把「最近一行是网页新建的占位会话」的工作流翻译成这个值（翻译见 lib/newSession.ts 的 workflowRoundStatus）。
 */
export const AWAITING_AGENT_ROUND = 'awaiting_agent'

export type SessionStatusKey = 'archived' | 'waiting' | 'working' | 'attach' | 'cancelled' | 'timeout' | 'idle' | 'done' | 'other'

/** 徽标的变体：沿用原来页头徽标用的 Badge 变体；waiting / working 另带色调。 */
export type SessionStatusVariant = 'default' | 'secondary' | 'outline' | 'destructive'

export interface SessionStatusInput {
  /** 最近一轮阻塞汇报的状态（pending / completed / cancelled / timeout / archived）；没有汇报为空。网页新建、智能体还没接入的会话传 AWAITING_AGENT_ROUND。 */
  roundStatus?: string | null
  /** 未答复的提问 / 计划 / 决策的条数（确认项里 status = pending 的）。 */
  pendingItems: number
  /** 智能体最近一次与 RelayMesh 交互的时间（服务端在内存里记，重启后清空）。 */
  agentActivityAt?: string | null
  /** Agent 状态机（hook）里这个工作流的主状态 key；没有接入为空。 */
  agentState?: string | null
  /** 还没结束的任务数。 */
  openTasks: number
  nowMs?: number
}

export interface SessionStatusView {
  key: SessionStatusKey
  label: string
  variant: SessionStatusVariant
  /** waiting 琥珀（要您处理）/ working 天蓝（智能体在干活）/ muted 灰（空闲）；其余只用 variant。 */
  tone: 'amber' | 'sky' | 'muted' | null
  /** 前面加一个脉动小点：waiting 与 working。 */
  pulse: boolean
  /** 等您处理的项数 = 等回复的汇报 + 未答复的确认项；只有 key = waiting 时大于 0。 */
  count: number
  /** 悬停说明：写明这个状态是怎么推出来的。 */
  title: string
}

/** hook 汇报「这一轮已收尾」的状态：此时即使十分钟内有过交互，也不再算「进行中」。 */
const HOOK_FINISHED: ReadonlySet<string> = new Set(['idle', 'ended', 'aborted'])

export type WorkingBasis = '' | 'hook' | 'activity'

/** 智能体是不是在干活：返回依据（悬停说明用），不在干活返回空串。 */
export function agentWorkingBasis(
  agentState: string | null | undefined,
  activityAt: string | null | undefined,
  nowMs: number,
): WorkingBasis {
  if (agentState === 'working') return 'hook'
  if (agentState && HOOK_FINISHED.has(agentState)) return ''
  const level = agentActivity(activityAt, nowMs).level
  return level === 'active' || level === 'recent' ? 'activity' : ''
}

function count(n: number): number {
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

/** 「等您处理」的悬停说明：拆开写明是哪几类。 */
function waitingTitle(reports: number, items: number): string {
  const parts: string[] = []
  if (reports > 0) parts.push('1 份等回复的汇报')
  if (items > 0) parts.push(`${items} 条待答复的提问 / 计划 / 决策`)
  return `等您处理 ${reports + items} 项：${parts.join('、')}。点开列出并定位`
}

export function deriveSessionStatus(input: SessionStatusInput): SessionStatusView | null {
  const nowMs = input.nowMs ?? Date.now()
  const round = input.roundStatus || ''
  const reports = round === 'pending' ? 1 : 0
  const items = count(input.pendingItems)
  const waiting = reports + items
  const tasks = count(input.openTasks)

  if (round === 'archived') {
    return { key: 'archived', label: '已归档', variant: 'outline', tone: null, pulse: false, count: 0, title: '这个会话已归档' }
  }

  if (waiting > 0) {
    return {
      key: 'waiting',
      label: `等待您 · ${waiting} 项`,
      variant: 'outline',
      tone: 'amber',
      pulse: true,
      count: waiting,
      title: waitingTitle(reports, items),
    }
  }

  const basis = agentWorkingBasis(input.agentState, input.agentActivityAt, nowMs)
  if (basis) {
    const title = basis === 'hook'
      ? '智能体正在工作（编辑器 hook 汇报）；没有等您处理的事'
      : `智能体最近一次与 RelayMesh 交互：${agentActivity(input.agentActivityAt, nowMs).text}（十分钟内都算进行中）；没有等您处理的事`
    return { key: 'working', label: '智能体进行中', variant: 'outline', tone: 'sky', pulse: true, count: 0, title }
  }

  // 网页新建、智能体还没接入：没有等您处理的事，智能体也没有动作（有动作说明它正在接入，上面已经算「进行中」）
  if (round === AWAITING_AGENT_ROUND) {
    return {
      key: 'attach',
      label: '待接入',
      variant: 'outline',
      tone: 'muted',
      pulse: false,
      count: 0,
      title: '这个会话是在网页里新建的，还没有智能体接入。把会话第一条消息里的「启动提示词」发给要使用的智能体；接入之前写下的指令会排进收件箱，等它接入后读取',
    }
  }

  if (round === 'cancelled') {
    return { key: 'cancelled', label: '已取消', variant: 'outline', tone: null, pulse: false, count: 0, title: '最近一轮汇报已被取消' }
  }
  if (round === 'timeout') {
    return { key: 'timeout', label: '已超时', variant: 'destructive', tone: null, pulse: false, count: 0, title: '最近一轮汇报等不到回复，已超时' }
  }

  if (tasks > 0) {
    const act = agentActivity(input.agentActivityAt, nowMs)
    const last = act.level === 'unknown' ? '服务启动以来没有交互记录' : `最近一次与 RelayMesh 交互：${act.text}`
    return {
      key: 'idle',
      label: '空闲',
      variant: 'outline',
      tone: 'muted',
      pulse: false,
      count: 0,
      title: `没有等您处理的事，智能体也没有近期动作（${last}）；还有 ${tasks} 个任务没结束`,
    }
  }

  if (round === 'completed') {
    return {
      key: 'done',
      label: '已完成',
      variant: 'default',
      tone: null,
      pulse: false,
      count: 0,
      title: '最近一轮汇报已确认，任务都已结束，没有等您处理的事',
    }
  }

  // 没见过的汇报状态：原样显示，免得徽标凭空消失
  if (round) return { key: 'other', label: round, variant: 'outline', tone: null, pulse: false, count: 0, title: `最近一轮汇报的状态：${round}` }
  return null
}
