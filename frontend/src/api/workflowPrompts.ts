// 会话级常驻提示词的 REST 封装（对应 internal/api/workflow_prompts.go，#232）。
// 写入口只有 Web 登录态：智能体只能在每次回执里读到它（resident: 行），改不了。

import { api } from './client'
import { workErrorMessage } from './workItems'

/** 常驻提示词的一个历史版本。 */
export interface ResidentRevision {
  id: number
  rev: number
  text: string
  actor?: string
  created_at: string
}

/** 某个工作流的常驻提示词设置。 */
export interface WorkflowPromptsView {
  workflow_id: string
  resident: string
  /** 当前版本号；0 表示从没配置过。 */
  resident_rev: number
  /** 智能体是否已经在回执里见到当前版本；从没配置过也算已见（没有什么要告诉它的）。 */
  told: boolean
  told_rev: number
  updated_at?: string
  updated_by?: string
  /** 字数上限（以服务端为准）。 */
  max: number
  /** 最近的历史版本，新的在前（服务端只留最近 10 个）。 */
  revisions: ResidentRevision[]
  /**
   * 这个会话自己的唤醒词模板（#232 D3）：续聊 / 新开对话。空串 = 没写，用全局覆盖或内置默认。
   * 旧版 Hub 没有这几项（界面据此判断要不要显示「唤醒词」页签）；它们在界面里没有历史版本（后台留着最近 10 份记录）。
   */
  wake_resume?: string
  wake_new?: string
  /** 唤醒词模板的字数上限（以服务端为准）。 */
  wake_max?: number
}

/** 保存唤醒词模板时要改的项：只带有变化的，空串 = 清除（回到全局覆盖 / 内置默认）。 */
export interface WakeTemplatePatch {
  wake_resume?: string
  wake_new?: string
}

/** 预览：智能体在回执里实际看到的样子（服务端用与回执头同一个格式化函数生成）。 */
export interface ResidentPreview {
  preview: string
  /** 整理之后文字为空：回执里不会出现 resident: 行。 */
  empty: boolean
  length: number
  max: number
  /** 保存之后会得到的版本号（文字没变就是当前版本）。 */
  rev: number
}

function base(workflowId: string): string {
  return `/workflows/${encodeURIComponent(workflowId)}/prompts`
}

export const workflowPromptsApi = {
  async get(workflowId: string): Promise<WorkflowPromptsView> {
    const { data } = await api.get<{ prompts: WorkflowPromptsView }>(base(workflowId))
    return data.prompts
  },

  /** 保存（空串 = 清除）。文字没变不涨版本（changed=false）；超过上限后端返回 400 并写明字数。 */
  async save(workflowId: string, resident: string): Promise<{ prompts: WorkflowPromptsView; changed: boolean }> {
    const { data } = await api.put<{ prompts: WorkflowPromptsView; changed: boolean }>(base(workflowId), { resident })
    return { prompts: data.prompts, changed: !!data.changed }
  },

  /** 保存这个会话的唤醒词模板（#232 D3）。多项一起提交，其中一项不合规整个请求什么也不保存（400 并写明原因）。 */
  async saveWake(workflowId: string, patch: WakeTemplatePatch): Promise<{ prompts: WorkflowPromptsView; changed: boolean }> {
    const { data } = await api.put<{ prompts: WorkflowPromptsView; changed: boolean }>(base(workflowId), patch)
    return { prompts: data.prompts, changed: !!data.changed }
  },

  async preview(workflowId: string, resident: string): Promise<ResidentPreview> {
    const { data } = await api.post<ResidentPreview>(`${base(workflowId)}/preview`, { resident })
    return data
  },

  /** 把某个历史版本的文字存成新的当前版本（版本号继续往前走，不会改写历史）。 */
  async restore(workflowId: string, revisionId: number): Promise<{ prompts: WorkflowPromptsView; changed: boolean }> {
    const { data } = await api.post<{ prompts: WorkflowPromptsView; changed: boolean }>(`${base(workflowId)}/restore`, {
      revision_id: revisionId,
    })
    return { prompts: data.prompts, changed: !!data.changed }
  },
}

/** 把接口错误翻译成可直接展示的话。 */
export function promptsErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
