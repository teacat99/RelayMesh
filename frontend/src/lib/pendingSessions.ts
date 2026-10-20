// 「有几个会话在等我答复」：手机页头的「待答复 N」顶条与汉堡角标共用的纯逻辑（#156 批 1）。
//
// 口径与侧栏「待处理」过滤一致：一个工作流（没有 workflow_id 的单个会话按 session_id）只要有一轮是 pending 就算一个；
// 当前正在看的那个不算「别处」——人已经在读它了，再催一遍只会添乱。
// 排序：等得最久的在前，点「去答复」先到最早等的那一个。
import type { FeedbackSession } from '../api/types'

/** 手机宽度的媒体查询；与输入区、侧栏等处的手机判断是同一条线（Tailwind 的 md = 768px）。 */
export const PHONE_MEDIA = '(max-width: 767.98px)'

/** 此刻页面正在看的会话（只有在会话页才有；在任务清单 / 群聊 / 项目页不算「正在看」）。 */
export interface ViewedSession {
  workflowId?: string
  sessionId?: string
}

export interface PendingPick {
  /** 全部在等答复的会话（每个工作流一个，最早创建的在前）。 */
  list: FeedbackSession[]
  /** 除了正在看的那个，还有几个在等。 */
  elsewhere: number
  /** 「去答复」要去的会话：正在看的之外最早等的那个；没有则为 null。 */
  next: FeedbackSession | null
}

const keyOf = (s: { workflow_id?: string; session_id: string }) => s.workflow_id || s.session_id

/**
 * 从会话列表里挑出等着答复的。`current` 是服务端当前的 pending 会话（可能还没进全量列表），与侧栏的合并方式一致：
 * 同一 session_id 以它为准，否则放进最前面。
 */
export function pickPending(sessions: FeedbackSession[], current: FeedbackSession | null, viewing: ViewedSession | null): PendingPick {
  const all = [...sessions]
  if (current) {
    const at = all.findIndex(s => s.session_id === current.session_id)
    if (at >= 0) all[at] = current
    else all.unshift(current)
  }

  const byKey = new Map<string, FeedbackSession>()
  for (const s of all) {
    // 进度通知（kind=notice）发布即完成，没有回复框，不算「等我答复」
    if (s.status !== 'pending' || s.kind === 'notice') continue
    const key = keyOf(s)
    const kept = byKey.get(key)
    // 同一个工作流里有多轮 pending 时留最早的一轮（等得最久）
    if (!kept || new Date(s.created_at).getTime() < new Date(kept.created_at).getTime()) byKey.set(key, s)
  }
  const list = [...byKey.values()].sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())

  const viewedKey = viewing ? viewing.workflowId || viewing.sessionId || '' : ''
  const others = viewedKey ? list.filter(s => keyOf(s) !== viewedKey) : list
  return { list, elsewhere: others.length, next: others[0] ?? null }
}

/** 角标上的数字：9 以内原样，更多写 9+。 */
export function badgeText(n: number): string {
  return n > 9 ? '9+' : String(n)
}
