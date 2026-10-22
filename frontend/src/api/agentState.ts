// Agent 身份与状态的 REST 封装（对应 internal/api/agent_state.go）。
// 状态由 hook 汇报与 MCP 调用推进；这里只读，唯一的写入是给身份改名。

import { api } from './client'
import { workErrorMessage } from './workItems'
import type { AgentIdentity, AgentSession } from '../lib/agentState'

export interface AgentSessionQuery {
  workflow_id?: string
  identity_id?: number
  limit?: number
}

export const agentStateApi = {
  async listIdentities(): Promise<AgentIdentity[]> {
    const { data } = await api.get<{ identities?: AgentIdentity[] }>('/identities')
    return data?.identities ?? []
  },

  /** 给身份改名；空串 = 恢复用目录名。局部修改在本项目里用 PUT（CORS 只放行 GET / POST / PUT / DELETE）。 */
  async renameIdentity(id: number, name: string): Promise<AgentIdentity> {
    const { data } = await api.put<{ identity: AgentIdentity }>(`/identities/${id}`, { name })
    return data.identity
  },

  async listSessions(query: AgentSessionQuery = {}): Promise<AgentSession[]> {
    const params: Record<string, string | number> = {}
    if (query.workflow_id) params.workflow_id = query.workflow_id
    if (query.identity_id) params.identity_id = query.identity_id
    if (query.limit) params.limit = query.limit
    const { data } = await api.get<{ sessions?: AgentSession[] }>('/agent-sessions', { params })
    return data?.sessions ?? []
  },
}

/** 把接口错误翻译成可直接展示的话。 */
export function agentStateErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  return workErrorMessage(err, fallback)
}
