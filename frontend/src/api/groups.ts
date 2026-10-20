// 群聊（#117）的 REST 封装，对应 internal/api/groups.go。消息流本身走 conversations.ts（会话编号是 grp:<id>）。

import { api } from './client'
import type { ChatMessage } from './conversations'

export type GroupPushPolicy = 'all' | 'mention' | 'none'

export interface GroupMember {
  kind: 'human' | 'workflow'
  ref: string
  name: string
  role: string
  workflow_id?: string
  push_policy: GroupPushPolicy
  muted_until?: string
  joined_at: string
  left?: boolean
  state?: string
  sub?: string
  waiting_you?: boolean
  project_directory?: string
  host_name?: string
  read_seq: number
}

export interface GroupInfo {
  id: string
  title: string
  project_id?: string
  created_at: string
  archived: boolean
  members: GroupMember[]
  cross_project: boolean
  projects?: string[]
}

export interface GroupSummary {
  id: string
  title: string
  archived: boolean
  member_count: number
  unread: number
  mentioned: number
  last?: ChatMessage
  preview?: string
  cross_project: boolean
  updated_at: string
}

export interface GroupCandidate {
  workflow_id: string
  name: string
  project_directory?: string
  host_name?: string
  state?: string
  eligible: boolean
  reason?: string
}

function base(id: string): string {
  return `/groups/${encodeURIComponent(id)}`
}

export const groupsApi = {
  async list(archived = false): Promise<GroupSummary[]> {
    const { data } = await api.get<{ groups: GroupSummary[] }>('/groups', { params: archived ? { archived: 1 } : {} })
    return data.groups ?? []
  },
  async candidates(): Promise<GroupCandidate[]> {
    const { data } = await api.get<{ candidates: GroupCandidate[] }>('/groups/candidates')
    return data.candidates ?? []
  },
  async create(body: { title: string; project_id?: string; workflow_ids: string[] }): Promise<{ id: string; title: string }> {
    const { data } = await api.post<{ group: { id: string; title: string } }>('/groups', body)
    return data.group
  },
  async get(id: string): Promise<GroupInfo> {
    const { data } = await api.get<GroupInfo>(base(id))
    return data
  },
  async update(id: string, body: { title?: string; project_id?: string; archived?: boolean }): Promise<void> {
    await api.put(base(id), body)
  },
  async addMembers(id: string, workflowIds: string[]): Promise<void> {
    await api.post(`${base(id)}/members`, { workflow_ids: workflowIds })
  },
  async removeMember(id: string, ref: string): Promise<void> {
    await api.delete(`${base(id)}/members/${encodeURIComponent(ref)}`)
  },
  async setPolicy(id: string, ref: string, body: { push_policy?: GroupPushPolicy; muted_until?: string; clear_mute?: boolean }): Promise<void> {
    await api.put(`${base(id)}/members/${encodeURIComponent(ref)}/policy`, body)
  },
}
