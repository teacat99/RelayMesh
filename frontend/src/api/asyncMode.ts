// 异步对话模式与授权档位的 REST 封装（对应 internal/api/async_mode.go）。
// 开关是用户的决定：智能体不能自己打开，它只能在回执里读到当前状态。

import { api } from './client'
import { workErrorMessage } from './workItems'
import type { AsyncModeView, AuthProfile, AuthProfileList } from './types'

export interface AuthProfileDraft {
  name?: string
  level?: 'L0' | 'L1'
  rules?: string
  sort?: number
}

function modeBase(workflowId: string): string {
  return `/workflows/${encodeURIComponent(workflowId)}/async-mode`
}

export const asyncModeApi = {
  async get(workflowId: string): Promise<AsyncModeView> {
    const { data } = await api.get<{ async_mode: AsyncModeView }>(modeBase(workflowId))
    return data.async_mode
  },

  /** 开关 / 选档位 / 收尾门开关：开启需要这个工作流收到过 hook 事件，否则后端返回 409 并说明怎么办。 */
  async update(
    workflowId: string,
    patch: { enabled?: boolean; profile_id?: number; strict_wrapup?: boolean },
  ): Promise<AsyncModeView> {
    const { data } = await api.put<{ async_mode: AsyncModeView }>(modeBase(workflowId), patch)
    return data.async_mode
  },

  /** 「本次放行」（#232 D2）：这一轮窗口不再被未完成任务挡住、立刻接着走；任务一概不动。没有窗口 / 门已关时 409。 */
  async releaseGate(workflowId: string): Promise<AsyncModeView> {
    const { data } = await api.post<{ async_mode: AsyncModeView }>(`${modeBase(workflowId)}/release-gate`)
    return data.async_mode
  },

  /** 回复窗口（#123）：plus = 我还在，加 1 分钟；typing = 输入心跳（窗口暂停）。 */
  async replyWindow(workflowId: string, kind: 'plus' | 'typing'): Promise<{ active: boolean; reply_window_until: string | null }> {
    const { data } = await api.post<{ active: boolean; reply_window_until?: string | null }>(
      `/workflows/${encodeURIComponent(workflowId)}/reply-window`,
      { kind },
    )
    return { active: !!data?.active, reply_window_until: data?.reply_window_until ?? null }
  },

  async listProfiles(): Promise<AuthProfileList> {
    const { data } = await api.get<AuthProfileList>('/auth-profiles')
    return {
      items: data?.items ?? [],
      default_id: data?.default_id ?? 0,
      limits: data?.limits ?? { max: 12, name_max: 24, rules_max: 240 },
    }
  },

  async createProfile(draft: AuthProfileDraft): Promise<AuthProfile> {
    const { data } = await api.post<{ item: AuthProfile }>('/auth-profiles', draft)
    return data.item
  },

  async updateProfile(id: number, draft: AuthProfileDraft): Promise<AuthProfile> {
    const { data } = await api.put<{ item: AuthProfile }>(`/auth-profiles/${id}`, draft)
    return data.item
  },

  async setDefaultProfile(id: number): Promise<AuthProfile> {
    const { data } = await api.post<{ item: AuthProfile }>(`/auth-profiles/${id}/default`)
    return data.item
  },

  async deleteProfile(id: number): Promise<void> {
    await api.delete(`/auth-profiles/${id}`)
  },
}

/** 把接口错误翻译成可直接展示的话。 */
export function asyncModeErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
