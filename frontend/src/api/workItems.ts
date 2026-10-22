// 任务清单的 REST 封装（对应 internal/api/work_items.go）。
// 这里的「任务」是独立于会话的工作清单（WorkItem），与托管自驾的 tasksApi 无关。

import { api } from './client'
import type {
  WorkCounts,
  WorkImproveDelivery,
  WorkItem,
  WorkKind,
  WorkModule,
  WorkModuleStats,
  WorkSessionLink,
  WorkStage,
  WorkState,
  WorkflowStat,
} from '../lib/workItems'

export interface WorkItemListQuery {
  workflow_id?: string
  state?: WorkState[]
  kind?: WorkKind[]
  /** 按阶段过滤；'none' 表示没标注阶段的任务。 */
  stage?: (WorkStage | 'none')[]
  /** 只要这个会话涉及的任务，并给每条任务附上「这个会话对它做了什么」（session_ctx）。 */
  session_id?: string
  /** 同时带回这个工作流里全部「会话 ↔ 任务」关联（links），会话页按轮次展开任务时用；需要同时指定 workflow_id。 */
  with_links?: boolean
  q?: string
  parent_id?: number
  top_level?: boolean
  /** 已关闭的任务只保留最近 N 天内关闭的。 */
  closed_days?: number
  /** 共享任务板：只要指派给这个工作流的；'none' = 没有指派的。 */
  assignee?: string
  limit?: number
}

export interface WorkItemList {
  items: WorkItem[]
  counts: WorkCounts
  workflows: WorkflowStat[]
  /** 指定了 workflow_id 时才有：该工作流的规划树（树序的扁平列表）与未归类任务的汇总。 */
  modules: WorkModule[]
  unassigned: WorkModuleStats | null
  /** 指定了 workflow_id 时才有：这个工作流所在的任务板（同一个项目的工作流共用一张板）。 */
  board_key: string
  /** 清单按最近更新取前 N 条，条数顶到上限时为 true：更早更新的可能被略去了（页面据此如实说明）。 */
  truncated: boolean
  /** 只有 with_links 时才有：这个工作流里哪个会话涉及哪个任务、做了什么。 */
  links: WorkSessionLink[]
}

export interface WorkItemCreate {
  workflow_id: string
  title: string
  kind?: WorkKind
  /** 阶段；不传 = 未标注（界面按类型推导一个默认值）。 */
  stage?: WorkStage
  state?: WorkState
  labels?: string[]
  refs?: string[]
  /** 任务内容的说明：这件事是什么、做到什么算完成（最长 1000 字）。 */
  description?: string
  progress?: string
  note?: string
  parent_id?: number
  /** 归入的模块 / 功能；不传 = 未归类。 */
  module_id?: number
  /** 显式完成度 0–100；不传 = 由子任务 / 状态推导。 */
  percent?: number
  /** 直接指派给同一张任务板上的另一个工作流；不传 = 不指派。 */
  assignee_workflow_id?: string
}

/** 缺省的字段保持不变；parent_id 传 0 表示解除父任务，module_id 传 0 表示取消归类，percent 传 -1 表示清除显式值，stage 传空串表示清除标注。 */
export interface WorkItemPatch {
  title?: string
  kind?: WorkKind
  stage?: WorkStage | ''
  state?: WorkState
  labels?: string[]
  refs?: string[]
  /** 传空串 = 清除；只改描述不算进展（不动「最近进展」时间、不占时间线）。 */
  description?: string
  progress?: string
  conclusion?: string
  parent_id?: number
  module_id?: number
  percent?: number
  note?: string
  /** 指派 / 改派 / 撤销：不传 = 不动，空串 = 撤销指派，非空 = 指派给这个工作流（共享任务板）。 */
  assignee_workflow_id?: string
  /** 协作动作；网页上人通常不用（直接改状态即可），不能和 assignee_workflow_id 同时给。 */
  claim?: 'accept' | 'decline' | 'deliver' | 'accept_delivery' | 'reject_delivery'
}

export interface WorkModuleCreate {
  workflow_id: string
  /** 0 / 不传 = 顶层模块；否则在这个模块下建功能。 */
  parent_id?: number
  name: string
  description?: string
}

export interface WorkModulePlan {
  modules: WorkModule[]
  unassigned: WorkModuleStats | null
}

export interface WorkModuleDeleted {
  path: string
  /** 回到上一级（或变成未归类）的任务数。 */
  moved_tasks: number
  /** 一起删除的功能数（删除顶层模块时）。 */
  removed_features: number
}

/** 会话时间线上的一条任务进展（智能体写的一次更新），对应 GET /workflows/:id/work-updates。 */
export interface WorkflowUpdate {
  id: number
  seq: number
  title: string
  /** 任务现在的状态（不是这次更新当时的状态，当时的看 to_state）。 */
  state: string
  from_state?: string
  to_state?: string
  text?: string
  actor_kind: string
  created_at: string
}

export const workItemsApi = {
  /** 一个工作流里智能体写下的任务进展，按提交时间升序（最新的 limit 条）。 */
  async workflowUpdates(workflowId: string, opts: { since?: string; limit?: number } = {}): Promise<WorkflowUpdate[]> {
    const params: Record<string, string | number> = {}
    if (opts.since) params.since = opts.since
    if (opts.limit) params.limit = opts.limit
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/work-updates`, { params })
    return res.data?.updates || []
  },

  async list(q: WorkItemListQuery = {}): Promise<WorkItemList> {
    const params: Record<string, string | number | boolean> = {}
    if (q.workflow_id) params.workflow_id = q.workflow_id
    if (q.state?.length) params.state = q.state.join(',')
    if (q.kind?.length) params.kind = q.kind.join(',')
    if (q.stage?.length) params.stage = q.stage.join(',')
    if (q.session_id) params.session_id = q.session_id
    if (q.with_links) params.with_links = 1
    if (q.q) params.q = q.q
    if (q.parent_id) params.parent_id = q.parent_id
    if (q.top_level) params.top_level = 1
    if (q.closed_days) params.closed_days = q.closed_days
    if (q.assignee) params.assignee = q.assignee
    if (q.limit) params.limit = q.limit
    const res = await api.get('/work-items', { params })
    const data = res.data || {}
    return {
      items: data.items || [],
      counts: data.counts || {},
      workflows: data.workflows || [],
      modules: data.modules || [],
      unassigned: data.unassigned || null,
      board_key: data.board_key || '',
      truncated: data.truncated === true,
      links: data.links || [],
    }
  },

  async get(id: number): Promise<WorkItem> {
    const res = await api.get(`/work-items/${id}`)
    return res.data.item
  },

  /** 这个工作流所在的任务板上能被指派的工作流编号（项目板 = 项目里的全部工作流；独立板为空）：详情里「指派」下拉按需取。 */
  async assignees(workflowId: string): Promise<{ boardKey: string; members: string[] }> {
    const res = await api.get('/work-items/assignees', { params: { workflow_id: workflowId } })
    return { boardKey: res.data?.board_key || '', members: res.data?.members || [] }
  },

  async create(body: WorkItemCreate): Promise<WorkItem> {
    const res = await api.post('/work-items', body)
    return res.data.item
  },

  async update(id: number, patch: WorkItemPatch): Promise<{ item: WorkItem; changed: boolean }> {
    const res = await api.put(`/work-items/${id}`, patch)
    return { item: res.data.item, changed: Boolean(res.data.changed) }
  },

  async remove(id: number): Promise<void> {
    await api.delete(`/work-items/${id}`)
  },

  async linkSession(id: number, sessionId: string): Promise<WorkItem> {
    const res = await api.post(`/work-items/${id}/sessions`, { session_id: sessionId })
    return res.data.item
  },

  async unlinkSession(id: number, sessionId: string): Promise<WorkItem> {
    const res = await api.delete(`/work-items/${id}/sessions/${encodeURIComponent(sessionId)}`)
    return res.data.item
  },

  /**
   * 在任务上发起改进需求（发现问题 / 想改成别的样子）：任务回到进行中并标为「改进」阶段，
   * 同时把这段话交给工作流里的智能体。delivered 说明是作为它正在等的这一轮回复（session），还是放进了收件箱（queued）。
   */
  async improve(id: number, text: string): Promise<{ item: WorkItem; delivered: WorkImproveDelivery }> {
    const res = await api.post(`/work-items/${id}/improve`, { text })
    return { item: res.data.item, delivered: res.data.delivered === 'session' ? 'session' : 'queued' }
  },
}

/** 任务模块规划（internal/api/work_modules.go）：每个工作流一棵最多两层的树，模块 → 功能。 */
export const workModulesApi = {
  async list(workflowId: string): Promise<WorkModulePlan> {
    const res = await api.get('/work-modules', { params: { workflow_id: workflowId } })
    return { modules: res.data?.modules || [], unassigned: res.data?.unassigned || null }
  },

  async create(body: WorkModuleCreate): Promise<WorkModule> {
    const res = await api.post('/work-modules', body)
    return res.data.module
  },

  async update(id: number, patch: { name?: string; description?: string }): Promise<WorkModule> {
    const res = await api.put(`/work-modules/${id}`, patch)
    return res.data.module
  },

  /** 在同级里上移 / 下移一格，返回最新的规划树。 */
  async move(id: number, direction: 'up' | 'down'): Promise<WorkModulePlan> {
    const res = await api.post(`/work-modules/${id}/move`, { direction })
    return { modules: res.data?.modules || [], unassigned: res.data?.unassigned || null }
  },

  async remove(id: number): Promise<WorkModuleDeleted> {
    const res = await api.delete(`/work-modules/${id}`)
    return {
      path: res.data?.path || '',
      moved_tasks: res.data?.moved_tasks || 0,
      removed_features: res.data?.removed_features || 0,
    }
  },
}

/** 把接口错误翻译成可直接展示的一句话（后端对业务错误返回中文 error 字段）。 */
export function workErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  const e = err as { response?: { data?: { error?: string } }; message?: string }
  return e?.response?.data?.error || e?.message || fallback
}
