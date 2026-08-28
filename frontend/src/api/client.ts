import axios from 'axios'
import type { FeedbackSession, TaskSummary, TaskDetail, Report, Feedback, SessionImage, WorkflowDraft } from './types'

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

  async list(params?: { project_directory?: string; status?: string; limit?: number }): Promise<{ sessions: FeedbackSession[] }> {
    const res = await api.get('/sessions', { params })
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

  async get(id: string): Promise<{ task: TaskDetail }> {
    const res = await api.get(`/tasks/${encodeURIComponent(id)}`)
    return res.data
  },

  async getReports(id: string, params?: { after_sequence?: number; limit?: number }): Promise<{ reports: Report[]; next_sequence: number; has_more: boolean }> {
    const res = await api.get(`/tasks/${encodeURIComponent(id)}/reports`, { params })
    return res.data
  },

  async sendFeedback(id: string, data: { body: string; expected_revision?: number; references?: Array<{ path: string; line?: number; description?: string }> }): Promise<{ status: string; feedback: Feedback }> {
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
