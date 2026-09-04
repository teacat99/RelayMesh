import axios from 'axios'
import type { FeedbackSession, TaskSummary, TaskDetail, Report, Feedback, SessionImage, WorkflowDraft, TaskStage, Segment, WaitPolicy } from './types'

export const api = axios.create({
  baseURL: '/api/v1',
  timeout: 15000,
  headers: {
    'Content-Type': 'application/json'
  }
})

export function setAuthToken(token: string) {
  if (token) {
    api.defaults.headers.common['Authorization'] = `Bearer ${token}`
    localStorage.setItem('relaymesh_token', token)
  } else {
    delete api.defaults.headers.common['Authorization']
    localStorage.removeItem('relaymesh_token')
  }
}

// Global 401 Unauthorized Interceptor Hook
let onUnauthorizedCallback: (() => void) | null = null
export function registerUnauthorizedHandler(handler: () => void) {
  onUnauthorizedCallback = handler
}

api.interceptors.response.use(
  response => response,
  error => {
    if (error.response && error.response.status === 401) {
      if (onUnauthorizedCallback) {
        onUnauthorizedCallback()
      }
    }
    return Promise.reject(error)
  }
)

// Initialize token from storage if available
const storedToken = localStorage.getItem('relaymesh_token')
if (storedToken) {
  api.defaults.headers.common['Authorization'] = `Bearer ${storedToken}`
}

export const sessionsApi = {
  async getCurrent(projectDir?: string): Promise<{ has_session: boolean; session: FeedbackSession | null }> {
    const res = await api.get('/sessions/current', { params: { project_directory: projectDir } })
    return res.data
  },

  async list(params?: { project_directory?: string; status?: string; limit?: number; brief?: boolean; compact?: boolean }): Promise<{ sessions: FeedbackSession[] }> {
    const res = await api.get('/sessions', { params })
    return res.data
  },

  async getWorkflowSessions(workflowId: string): Promise<{ sessions: FeedbackSession[] }> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/sessions`)
    return res.data
  },

  async get(id: string): Promise<{ session: FeedbackSession }> {
    const res = await api.get(`/sessions/${encodeURIComponent(id)}`)
    return res.data
  },

  async submit(id: string, data: { response_text: string; user_messages?: string[]; images?: SessionImage[] }): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/submit`, data)
    return res.data
  },

  async cancel(id: string): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/cancel`)
    return res.data
  },

  async keepalive(id: string, extendSeconds = 300): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/keepalive`, null, { params: { extend_seconds: extendSeconds } })
    return res.data
  },

  async archive(id: string): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/archive`)
    return res.data
  },

  async unarchive(id: string): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/unarchive`)
    return res.data
  },

  async rename(id: string, title: string): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/rename`, { title })
    return res.data
  },

  async updatePromptWait(id: string, waitMinutes: number): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/prompt_wait`, { wait_minutes: waitMinutes })
    return res.data
  },

  async updateMaxChecks(id: string, maxChecks: number): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/max_checks`, { max_checks: maxChecks })
    return res.data
  },

  async updateWaitCountdown(id: string, countdownMinutes: number): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/wait_countdown`, { countdown_minutes: countdownMinutes })
    return res.data
  },

  async updateUserPresence(id: string, userPresence: 'online' | 'away' | 'autopilot'): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/user_presence`, { user_presence: userPresence })
    return res.data
  },

  async revoke(id: string): Promise<{ status: string; revoked: any; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/revoke`)
    return res.data
  },

  async appendWorkflowFeedback(workflowId: string, data: { response_text: string; user_messages?: string[]; images?: SessionImage[]; host_name?: string; project_directory?: string }): Promise<{ status: string; type: 'session' | 'queued'; session?: FeedbackSession; queued?: any }> {
    const res = await api.post(`/workflows/${encodeURIComponent(workflowId)}/append`, data)
    return res.data
  },

  async listQueued(workflowId: string): Promise<{ queued_feedbacks: any[] }> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/queued`)
    return res.data
  },

  async revokeQueued(queuedId: number): Promise<{ status: string; revoked: any }> {
    const res = await api.post(`/feedbacks/queued/${queuedId}/revoke`)
    return res.data
  }
}

export interface TranscribePayload {
  audio_base64: string
  mime_type?: string
  api_url?: string
  api_key?: string
  model?: string
  language?: string
  stream?: boolean
}

export const voiceApi = {
  async transcribe(payload: TranscribePayload): Promise<{ text?: string; [key: string]: any }> {
    const res = await api.post('/voice/transcribe', payload)
    return res.data
  }
}

export const tasksApi = {
  async list(params?: { state?: string; cursor?: string; limit?: number; updates_only?: boolean }): Promise<{ tasks: TaskSummary[]; next_cursor?: string }> {
    const res = await api.get('/tasks', { params })
    return res.data
  },

  async create(data: { task_id?: string; title?: string; mode?: string; stages?: TaskStage[]; segments?: Segment[]; wait_policy?: WaitPolicy }): Promise<{ status: string; task: TaskDetail }> {
    const res = await api.post('/tasks', data)
    return res.data
  },

  async get(id: string): Promise<{ task: TaskDetail }> {
    const res = await api.get(`/tasks/${encodeURIComponent(id)}`)
    return res.data
  },

  async updateStages(id: string, data: { expected_revision?: number; current_stage_id?: string; stages: TaskStage[] }): Promise<{ status: string; result: any }> {
    const res = await api.put(`/tasks/${encodeURIComponent(id)}/stages`, data)
    return res.data
  },

  async getReports(id: string, params?: { after_sequence?: number; limit?: number }): Promise<{ reports: Report[]; next_sequence: number; has_more: boolean }> {
    const res = await api.get(`/tasks/${encodeURIComponent(id)}/reports`, { params })
    return res.data
  },

  async getFeedbacks(id: string, params?: { after_sequence?: number; limit?: number }): Promise<{ feedbacks: Feedback[] }> {
    const res = await api.get(`/tasks/${encodeURIComponent(id)}/feedbacks`, { params })
    return res.data
  },

  async sendFeedback(id: string, data: { body: string; source?: 'human' | 'commander'; expected_revision?: number; references?: Array<{ path: string; line?: number; description?: string }> }): Promise<{ status: string; feedback: Feedback }> {
    const res = await api.post(`/tasks/${encodeURIComponent(id)}/feedbacks`, data)
    return res.data
  },

  async ackReports(id: string, throughSequence: number): Promise<{ status: string; summary: any }> {
    const res = await api.post(`/tasks/${encodeURIComponent(id)}/ack`, { through_sequence: throughSequence })
    return res.data
  }
}

export const settingsApi = {
  async get(): Promise<{ settings: Record<string, any> }> {
    const res = await api.get('/settings')
    return res.data
  },

  async update(settings: Record<string, any>): Promise<{ status: string; settings: Record<string, any> }> {
    const res = await api.put('/settings', settings)
    return res.data
  }
}

export interface UserNorm {
  name: string
  summary: string
  content: string
  is_active: boolean
  sort_order: number
  created_at: string
  updated_at: string
}

export const normsApi = {
  async list(): Promise<{ norms: UserNorm[] }> {
    const res = await api.get('/norms')
    return res.data
  },

  async get(name: string): Promise<{ norm: UserNorm }> {
    const res = await api.get(`/norms/${encodeURIComponent(name)}`)
    return res.data
  },

  async create(norm: { name: string; summary: string; content: string; is_active?: boolean }): Promise<{ status: string; norm: UserNorm }> {
    const res = await api.post('/norms', norm)
    return res.data
  },

  async update(name: string, updates: Partial<Pick<UserNorm, 'summary' | 'content' | 'is_active'>>): Promise<{ status: string; norm: UserNorm }> {
    const res = await api.put(`/norms/${encodeURIComponent(name)}`, updates)
    return res.data
  },

  async remove(name: string): Promise<{ status: string }> {
    const res = await api.delete(`/norms/${encodeURIComponent(name)}`)
    return res.data
  }
}

export interface MCPPermissions {
  feedback: boolean
  sessions: boolean
  system_info: boolean
  skills: boolean
  configure: boolean
  execute: boolean
}

export interface MCPCredential {
  id: number
  name: string
  token: string
  host_name: string
  is_active: boolean
  permissions: MCPPermissions
  note: string
  is_env?: boolean
  created_at: string
  updated_at: string
}

export interface EnvCredential {
  source: 'env'
  type: 'mcp_token' | 'web_auth'
  name: string
  token?: string
  username?: string
  note: string
}

export const credentialsApi = {
  async list(): Promise<{ credentials: MCPCredential[]; env_credentials?: EnvCredential[] }> {
    const res = await api.get('/credentials')
    return res.data
  },

  async get(id: number): Promise<{ credential: MCPCredential }> {
    const res = await api.get(`/credentials/${id}`)
    return res.data
  },

  async create(data: { name: string; host_name?: string; is_active?: boolean; permissions?: Partial<MCPPermissions>; note?: string }): Promise<{ status: string; credential: MCPCredential; token: string }> {
    const res = await api.post('/credentials', {
      ...data,
      permissions: { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true, ...(data.permissions || {}) }
    })
    return res.data
  },

  async update(id: number, updates: Partial<Pick<MCPCredential, 'name' | 'host_name' | 'is_active' | 'permissions' | 'note'>>): Promise<{ status: string; credential: MCPCredential }> {
    const res = await api.put(`/credentials/${id}`, updates)
    return res.data
  },

  async remove(id: number): Promise<{ status: string }> {
    const res = await api.delete(`/credentials/${id}`)
    return res.data
  },

  async regenerateToken(id: number): Promise<{ status: string; credential: MCPCredential; token: string }> {
    const res = await api.post(`/credentials/${id}/regenerate`)
    return res.data
  }
}

export const draftsApi = {
  async get(workflowId: string): Promise<{ draft: WorkflowDraft | null }> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/drafts`)
    return res.data
  },

  async save(workflowId: string, activeIndex: number, draftsJson: string): Promise<{ status: string; draft: WorkflowDraft }> {
    const res = await api.put(`/workflows/${encodeURIComponent(workflowId)}/drafts`, {
      active_index: activeIndex,
      drafts_json: draftsJson
    })
    return res.data
  },

  async delete(workflowId: string): Promise<{ status: string }> {
    const res = await api.delete(`/workflows/${encodeURIComponent(workflowId)}/drafts`)
    return res.data
  }
}

export interface PhaseItem {
  id: string
  label: string
  description?: string
  prompt?: string
}

export interface WorkflowPhaseState {
  workflow_id: string
  current_phase_id: string
  phases: PhaseItem[]
  default_prompts?: Record<string, string>
}

export const phasesApi = {
  async get(workflowId: string): Promise<WorkflowPhaseState> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/phase`)
    return res.data
  },

  async set(workflowId: string, payload: { phase_id?: string; source?: 'human' | 'ai'; phases?: PhaseItem[] }): Promise<WorkflowPhaseState> {
    const res = await api.put(`/workflows/${encodeURIComponent(workflowId)}/phase`, payload)
    return res.data
  }
}

export interface BlockedIPInfo {
  ip: string
  failed_count: number
  last_failed_at: string
  locked_until: string
  remaining_seconds: number
}

export const authApi = {
  async status(): Promise<{ auth_required: boolean; mcp_auth_required?: boolean; project_id: string; host_name?: string; current_username?: string; is_customized?: boolean }> {
    const res = await api.get('/auth/status')
    return res.data
  },

  async login(payload: { username?: string; password: string }): Promise<{ status: string; token?: string; error?: string; locked?: boolean; remaining_seconds?: number; lockout_minutes?: number }> {
    const res = await api.post('/auth/login', payload)
    return res.data
  },

  async changeCredentials(payload: { old_password?: string; new_username: string; new_password: string }): Promise<{ status: string; message: string; username: string; token: string }> {
    const res = await api.post('/auth/change_credentials', payload)
    return res.data
  },

  async resetCredentials(): Promise<{ status: string; message: string; username: string }> {
    const res = await api.post('/auth/reset_credentials')
    return res.data
  },

  async getBlockedIPs(): Promise<{ blocked_ips: BlockedIPInfo[]; total: number }> {
    const res = await api.get('/auth/blocked_ips')
    return res.data
  },

  async unblockIP(ip: string): Promise<{ status: string; message?: string }> {
    const res = await api.post('/auth/unblock_ip', { ip })
    return res.data
  },

  async clearAllBlockedIPs(): Promise<{ status: string; message?: string }> {
    const res = await api.post('/auth/clear_blocked_ips')
    return res.data
  }
}
