// 严格收尾门（#232 D2）的界面逻辑：纯函数，便于单测。
//
// 一句话：开着收尾门的异步会话，只要本会话名下还有未完成（待办 / 进行中 / 验证中）的任务，回复窗口就不走、
// 智能体也不能收尾；放下任务的正规出口是「暂缓」（等你拍板的要先发确认卡）。你可以：
//   - 看清单（哪些任务在挡着）；
//   - 「本次放行」：这一轮窗口照常倒计时，任务一概不动，你下次回复或窗口重新起算时自动复位；
//   - 关掉收尾门：这个会话回到老口径（只数 3 小时内有更新的进行中 / 验证中）；
//   - 替智能体把某个任务置「暂缓」。

import type { AsyncModeView, GateTask } from '../api/types'
import { stateLabel } from './workItems'

/** 挡着收尾的任务积压到这个数以上，弹层顶部多给一条「积压很多」的说明。 */
export const GATE_HEAVY = 8
/** 弹层里最多逐个列出的任务数，其余只给个数。 */
export const GATE_LIST_SHOWN = 6
/** 你在清单里替智能体置「暂缓」时写进进展的话：带「等」字，服务端「暂缓要写缘由」的提醒就不会再去打扰智能体。 */
export const GATE_SHELVE_PROGRESS = '等用户另行安排（用户在收尾门里置暂缓）'

type Mode = Pick<AsyncModeView, 'strict_wrapup' | 'gate_released' | 'gate_total' | 'gate_tasks' | 'reply_window_until' | 'reply_window_paused' | 'reply_window_bank_sec'>

/** 老服务端没有 strict_wrapup 字段：不知道就当没有收尾门，界面完全不出现相关元素。 */
export function gateKnown(m: Mode | null | undefined): boolean {
  return !!m && typeof m.strict_wrapup === 'boolean'
}

/** 收尾门开着（用户没关）。 */
export function gateOn(m: Mode | null | undefined): boolean {
  return gateKnown(m) && m!.strict_wrapup === true
}

/** 挡着收尾的任务总数（门关着或不知道时 0）。 */
export function gateTotal(m: Mode | null | undefined): number {
  return gateOn(m) ? Math.max(0, m!.gate_total ?? 0) : 0
}

export function gateHeavy(total: number): boolean {
  return total > GATE_HEAVY
}

/** 名单里各状态的个数，写成「待办 9 · 进行中 2 · 验证中 3」；名单只有前 20 个时（总数更大）末尾加「…」。 */
export function gateBreakdown(tasks: GateTask[] | undefined, total: number): string {
  const n: Record<string, number> = { todo: 0, doing: 0, review: 0 }
  for (const t of tasks ?? []) if (t.state in n) n[t.state]++
  const parts = (['todo', 'doing', 'review'] as const).filter(s => n[s] > 0).map(s => `${stateLabel(s)} ${n[s]}`)
  if (!parts.length) return ''
  return parts.join(' · ') + (total > (tasks?.length ?? 0) ? ' …' : '')
}

/** 名单分成「直接列出的」与「其余个数」。 */
export function gateShown(tasks: GateTask[] | undefined, total: number): { shown: GateTask[]; more: number } {
  const all = tasks ?? []
  const shown = all.slice(0, GATE_LIST_SHOWN)
  return { shown, more: Math.max(0, total - shown.length) }
}

/** 「本次放行」按钮此刻能不能点；不能点时给出原因（做成按钮的悬停说明）。 */
export function gateReleaseState(m: Mode | null | undefined): { can: boolean; why: string } {
  if (!gateOn(m)) return { can: false, why: '收尾门已关闭，不需要放行' }
  if (m!.gate_released) return { can: false, why: '这一轮已经放行了：窗口在照常倒计时，你下次回复后自动复位' }
  if (gateTotal(m) === 0) return { can: false, why: '现在没有未完成任务挡着收尾' }
  const hasWindow = !!m!.reply_window_until || (!!m!.reply_window_paused && (m!.reply_window_bank_sec ?? 0) > 0)
  if (!hasWindow) return { can: false, why: '现在没有回复窗口在等：智能体发新消息之后才有' }
  return { can: true, why: '这一轮窗口不再被任务挡住、立刻接着倒计时；任务一概不动，你下次回复或窗口重新起算时自动复位' }
}

/** 收尾门开关的悬停说明。 */
export function gateSwitchTitle(on: boolean): string {
  return on
    ? '开着：本会话名下还有未完成任务（待办 / 进行中 / 验证中）时，回复窗口不走、智能体也不能收尾；放下任务的出口是「暂缓」。关掉后回到老口径（只数 3 小时内有更新的进行中 / 验证中）。'
    : '关着：回复窗口按老口径暂停（只数 3 小时内有更新的进行中 / 验证中），待办不挡收尾。打开后按「本会话名下的全部未完成任务」算。'
}

/** 弹层里一行任务的状态标签。 */
export function gateStateLabel(state: string): string {
  return stateLabel(state)
}

/** 点收尾门里一行任务（或「另有 N 个」）后去任务页的路由；比 vue-router 的类型窄，只写这里用到的字段。 */
export interface GateTaskTarget {
  name: 'work'
  params: { workflowId: string }
  query?: { task: string }
}

/**
 * 点清单里一行任务要去的任务页地址：?task= 认的是任务的数据库编号（GateTask.id），不是界面上写的 #N（GateTask.seq，板内序号）。
 * 把 #N 塞进 ?task= 会打开别的任务（数据库编号恰好等于这个数的那个），没有这个编号时只显示「任务不存在」（#242）。
 * 不带任务（「另有 N 个」）就只进这个工作流的任务页。
 */
export function gateTaskTarget(workflowId: string, task?: Pick<GateTask, 'id'>): GateTaskTarget {
  const to: GateTaskTarget = { name: 'work', params: { workflowId } }
  if (task && Number.isInteger(task.id) && task.id > 0) to.query = { task: String(task.id) }
  return to
}
