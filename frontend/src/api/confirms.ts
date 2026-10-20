// 确认项的 REST 封装（对应 internal/api/confirms.go）。
// 确认项是智能体的异步提问 / 计划确认 / 文档决策卡的统一载体，编号写成 Q12（与任务 #12 是两套编号）。

import { api } from './client'
import { workErrorMessage } from './workItems'

export type ConfirmKind = 'ask' | 'plan' | 'decision'
export type ConfirmRisk = 'L0' | 'L1' | 'L2'
export type ConfirmStatus = 'pending' | 'answered' | 'dismissed' | 'withdrawn' | 'expired'

export interface ConfirmOption {
  id: string
  label: string
  detail?: string
  recommended?: boolean
}

export interface ConfirmAnswer {
  /** 选中的预设选项；只写了自定义答复时是空数组。 */
  options: string[]
  note?: string
  /** 用户觉得预设选项都不合适时自己写的答复（#183）；老答复没有这个字段。 */
  custom?: string
  by: string
}

export interface ConfirmItem {
  id: number
  workflow_id: string
  seq: number
  /** 界面与收件箱里用的编号，如 Q12。 */
  ref: string
  kind: ConfirmKind
  title: string
  body?: string
  options: ConfirmOption[]
  multi: boolean
  allow_note: boolean
  default_option?: string
  risk: ConfirmRisk
  task_seq?: number
  doc_id?: number
  doc_key?: string
  doc_path?: string
  doc_title?: string
  anchor?: string
  status: ConfirmStatus
  answer?: ConfirmAnswer
  answered_at?: string
  created_by: string
  created_at: string
  updated_at: string
}

/** 「待确认」区里一条守护告警（会话崩溃 / 停摆，等你决定要不要唤醒）。 */
export interface ConfirmBoardGuard {
  preset_id: string
  name: string
  host?: string
  verdict: { state: string; reason?: string; since?: string; [k: string]: unknown }
  wake_mode?: string
}

export interface ConfirmCounts {
  pending: number
  items: number
  guard_alerts: number
  by_risk: Record<string, number>
  by_kind: Record<string, number>
}

export interface ConfirmBoard {
  items: ConfirmItem[]
  guard_alerts: ConfirmBoardGuard[]
  counts: ConfirmCounts
}

export interface ConfirmListQuery {
  status?: ConfirmStatus[]
  kind?: ConfirmKind[]
  task_seq?: number
  doc_id?: number
  limit?: number
  offset?: number
  /** asc = 旧的在前（待确认队列）；默认新的在前。 */
  order?: 'asc' | 'desc'
}

export interface ConfirmBatchEntry {
  ref: string
  options?: string[]
  note?: string
}

function base(workflowId: string): string {
  return `/workflows/${encodeURIComponent(workflowId)}/confirms`
}

export const confirmsApi = {
  async board(workflowId: string): Promise<ConfirmBoard> {
    const { data } = await api.get<ConfirmBoard>(`${base(workflowId)}/board`)
    return {
      items: data?.items ?? [],
      guard_alerts: data?.guard_alerts ?? [],
      counts: data?.counts ?? { pending: 0, items: 0, guard_alerts: 0, by_risk: {}, by_kind: {} },
    }
  },

  async list(workflowId: string, q: ConfirmListQuery = {}): Promise<ConfirmItem[]> {
    const params: Record<string, string | number> = {}
    if (q.status?.length) params.status = q.status.join(',')
    if (q.kind?.length) params.kind = q.kind.join(',')
    if (q.task_seq) params.task_seq = q.task_seq
    if (q.doc_id) params.doc_id = q.doc_id
    if (q.limit) params.limit = q.limit
    if (q.offset) params.offset = q.offset
    if (q.order === 'asc') params.order = 'asc'
    const { data } = await api.get<{ items: ConfirmItem[] }>(base(workflowId), { params })
    return data?.items ?? []
  },

  async get(workflowId: string, ref: string): Promise<ConfirmItem> {
    const { data } = await api.get<{ item: ConfirmItem }>(`${base(workflowId)}/${encodeURIComponent(ref)}`)
    return data.item
  },

  /** 单条作答：options 是选中的预设选项；custom 是「预设选项都不合适」时自己写的答复（单选时与 options 二选一，多选时可并存）。 */
  async answer(workflowId: string, ref: string, options: string[], note?: string, custom?: string): Promise<ConfirmItem> {
    const { data } = await api.post<{ item: ConfirmItem }>(`${base(workflowId)}/${encodeURIComponent(ref)}/answer`, {
      options,
      ...(note ? { note } : {}),
      ...(custom ? { custom } : {}),
    })
    return data.item
  },

  /** 批量作答：整批原子，任何一项不合格都不会保存；L2 项不接受批量。 */
  async answerBatch(
    workflowId: string,
    entries: ConfirmBatchEntry[],
    adoptRecommended = false,
  ): Promise<{ items: ConfirmItem[]; inbox_message: string }> {
    const { data } = await api.post<{ items: ConfirmItem[]; inbox_message: string }>(`${base(workflowId)}/answer-batch`, {
      items: entries,
      ...(adoptRecommended ? { adopt_recommended: true } : {}),
    })
    return { items: data?.items ?? [], inbox_message: data?.inbox_message ?? '' }
  },

  async dismiss(workflowId: string, ref: string, reason?: string): Promise<ConfirmItem> {
    const { data } = await api.post<{ item: ConfirmItem }>(
      `${base(workflowId)}/${encodeURIComponent(ref)}/dismiss`,
      reason ? { reason } : {},
    )
    return data.item
  },
}

/** 把接口错误翻译成可直接展示的话（批量失败时后端会在 error 里按行列出每一项的问题）。 */
export function confirmErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
