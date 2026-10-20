// 侧栏会话卡的「会话级状态」取数（#159；推导规则见 lib/sidebarStatus.ts 与 lib/sessionStatus.ts）。
//
// 页头徽标取数的几路（确认项、任务计数、智能体最近动作、Agent 状态）是分几路晚到的，没到齐之前不报结论，
// 否则刷新页面时侧栏会先闪一个错的状态（「已完成」再变成「等待您」）。这里同样处理：
//   - 全局的两路（任务计数、Agent 状态）到了，且这个工作流在会话列表里已有聚合 → 立即显示；
//   - 任何一路一直到不了，3 秒后用手上有的数据照常显示（宁可晚一点出现，也不先报一个错的状态）。
// 3 秒的计时是全局一次（不是每张卡一个定时器）。
import { ref } from 'vue'
import { useSessionStore } from '../stores/session'
import { useConfirmStore } from '../stores/confirms'
import { useWorkItemStore } from '../stores/workItems'
import { useAgentStateStore } from '../stores/agentState'
import { stateView } from '../lib/agentState'
import { needsAnswer } from '../lib/timeline'
import { sidebarSessionStatus, type LiveWorkflowSources } from '../lib/sidebarStatus'
import type { SessionStatusView } from '../lib/sessionStatus'

export const SIDEBAR_STATUS_SETTLE_MS = 3000

const settled = ref(false)
let settleStarted = false

function startSettleTimer() {
  if (settleStarted || typeof window === 'undefined') return
  settleStarted = true
  window.setTimeout(() => {
    settled.value = true
  }, SIDEBAR_STATUS_SETTLE_MS)
}

export function useSidebarSessionStatus() {
  const sessionStore = useSessionStore()
  const confirmStore = useConfirmStore()
  const workStore = useWorkItemStore()
  const agentStore = useAgentStateStore()
  startSettleTimer()

  /**
   * 一个工作流此刻的会话级状态；数据还没到齐时返回 null（调用方不显示徽标）。
   * roundStatus 是会话卡上的轮次状态（有待答复的一轮为 pending，否则是最近一轮的状态）。
   */
  function statusFor(workflowId: string, roundStatus: string, nowMs: number): SessionStatusView | null {
    const baseline = sessionStore.workflowAggregates[workflowId] ?? null
    const globalsReady = workStore.countsLoaded && (agentStore.loadedAt > 0 || !agentStore.supported)
    if (!settled.value && !(globalsReady && baseline)) return null

    // 页头正在看的工作流：确认项 store 与收件箱轮询正好关注它，用与页头同一批实时数据；其余工作流用列表里的聚合
    const confirmsLive = confirmStore.loaded && confirmStore.workflowId === workflowId
    const inboxLive = sessionStore.inboxLoadedFor === workflowId
    const live: LiveWorkflowSources | null =
      confirmsLive || inboxLive
        ? {
            confirms: confirmsLive ? confirmStore.board.items.filter(needsAnswer).length : null,
            agentActivityAt: inboxLive ? sessionStore.agentLastActivityAt : undefined,
          }
        : null

    const entry = agentStore.entryFor(workflowId)
    const agentState = entry.primary ? stateView(entry.primary, nowMs).key : null
    const openTasks = workStore.workflows.find(w => w.workflow_id === workflowId)?.active ?? 0

    return sidebarSessionStatus({ roundStatus, live, baseline, agentState, openTasks, nowMs })
  }

  return { statusFor }
}
