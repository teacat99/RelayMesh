// 顶栏「执行」块在 hook 汇报下的存活图标（#89）
//
// 两个来源合并成一个图标（#113，设计稿 04 §3「与现有存活图标合并」）：
//   - Agent 状态机（agentState，hook v2 的细粒度事件：工作中 / 空闲 / 中断 / 出错 / 卡住 / 离线 …）优先；
//   - 状态机里没有这个工作流（旧版 hook 只汇报 tool / stop，或还没有会话）时，回落到收件箱接口里的 agent_live 三值。
import type { AgentLiveState, AgentLiveView } from '../api/types'
import type { AgentStateView } from './agentState'

// 「无动静」不再在块里写成文字：旁边的 hook 芯片已经显示「hook 48 分钟前」，两处是同一个时间（#104），
// 这里只用图标颜色表达状态，完整说明放在悬停提示里。
/** 图标状态：旧的三值 + 状态机带来的「需要注意」（出错 / 疑似卡住）。 */
export type AgentLiveIconState = AgentLiveState | 'alert'

export interface AgentLiveIconView {
  /** 是否覆盖默认 exec 图标（unknown 时不覆盖） */
  active: boolean
  state: AgentLiveIconState
  title: string
}

/** 状态机的状态 → 图标；没接入返回 null（交给旧口径）。 */
function iconFromAgentState(a: AgentStateView): AgentLiveIconView | null {
  const title = a.title
  switch (a.key) {
    case 'working':
    case 'waiting_you':
      return { active: true, state: 'running', title }
    case 'idle':
    case 'ended':
    case 'aborted':
      return { active: true, state: 'idle', title }
    case 'waiting_approval':
    case 'offline':
      return { active: true, state: 'quiet', title }
    case 'error':
    case 'stalled':
      return { active: true, state: 'alert', title }
    default:
      return null
  }
}

export function agentLiveIcon(
  live: AgentLiveView | null | undefined,
  agent?: AgentStateView | null,
): AgentLiveIconView {
  if (agent) {
    const merged = iconFromAgentState(agent)
    if (merged) return merged
  }
  if (!live || live.state === 'unknown') {
    return { active: false, state: 'unknown', title: live?.reason || '需要安装编辑器 hook 才能显示真实运行状态' }
  }
  const base = live.reason || ''
  switch (live.state) {
    case 'running':
      return { active: true, state: 'running', title: base || '智能体正在运行（hook 汇报）' }
    case 'idle':
      return { active: true, state: 'idle', title: base || '智能体已正常收尾，处于空闲' }
    case 'quiet':
      return {
        active: true,
        state: 'quiet',
        title: `${base}${base ? '；' : ''}需要装了 hook 才有此提示`,
      }
    default:
      return { active: false, state: 'unknown', title: base }
  }
}
