import axios from 'axios'
import type { CheckpointView } from '../lib/anchor'
import type { FeedbackSession, SessionBody, TaskSummary, TaskDetail, Report, Feedback, SessionImage, WorkflowDraft, TaskStage, Segment, WaitPolicy, WorkflowInbox, InboxConfig, InboxConfigView } from './types'
import { perfRecordRequest } from '../utils/perf'
import { sendWithDraftImages, type WireImages } from '../utils/draftImageWire'

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
  response => {
    // D-160: 方案 A 响应头静默滑动续期 (Sliding Expiration)
    const renewedToken = response.headers?.['x-renewed-token']
    if (renewedToken) {
      setAuthToken(renewedToken)
    }
    return response
  },
  error => {
    if (error.response && error.response.status === 401) {
      if (onUnauthorizedCallback) {
        onUnauthorizedCallback()
      }
    }
    return Promise.reject(error)
  }
)

// 仅开发模式：记录每个请求的路由、耗时与解码后字节数，供 window.__rmPerf 汇总（D-171 / P0，生产构建整段树摇移除）
if (import.meta.env.DEV) {
  api.interceptors.request.use(config => {
    ;(config as any).__perfStart = performance.now()
    return config
  })
  const record = (config: any, status: number, bytes: number) => {
    const startedAt: number = config?.__perfStart ?? performance.now()
    perfRecordRequest({
      method: String(config?.method || 'get').toUpperCase(),
      url: String(config?.url || ''),
      status,
      ms: performance.now() - startedAt,
      bytes,
      startedAt,
    })
  }
  api.interceptors.response.use(
    response => {
      // responseType 为 arraybuffer / blob 时读取 responseText 会抛 InvalidStateError（终端记录是二进制响应）。
      const req = response.request as XMLHttpRequest | undefined
      const textual = !!req && (req.responseType === '' || req.responseType === 'text')
      const bytes = textual
        ? (req?.responseText.length ?? 0)
        : response.data instanceof ArrayBuffer
          ? response.data.byteLength
          : 0
      record(response.config, response.status, bytes)
      return response
    },
    error => {
      record(error?.config, error?.response?.status ?? 0, 0)
      return Promise.reject(error)
    }
  )
}

// Initialize token from storage if available
const storedToken = localStorage.getItem('relaymesh_token')
if (storedToken) {
  api.defaults.headers.common['Authorization'] = `Bearer ${storedToken}`
}

/** 把请求里的图片换成线上格式；有引用型图片（draft_key）时一并带上草稿所属的工作流。 */
function withWireImages<T extends { images?: SessionImage[] }>(data: T, wire: WireImages) {
  const { images: _images, ...rest } = data
  return wire.draftWorkflowId
    ? { ...rest, images: wire.images, draft_workflow_id: wire.draftWorkflowId }
    : { ...rest, images: wire.images }
}

export const sessionsApi = {
  async getCurrent(projectDir?: string): Promise<{ has_session: boolean; session: FeedbackSession | null }> {
    const res = await api.get('/sessions/current', { params: { project_directory: projectDir } })
    return res.data
  },

  /**
   * cover_workflows（只对 brief 生效）：在最新 limit 行之外，再给每个工作流补上它最新的一行（#173）。
   * 侧栏与归档库要的是「每个工作流一条」，只拿最新 N 行会话时，话痨的工作流会把较早的工作流挤出窗口。
   */
  async list(params?: { project_directory?: string; status?: string; limit?: number; brief?: boolean; compact?: boolean; cover_workflows?: boolean }): Promise<{ sessions: FeedbackSession[] }> {
    const res = await api.get('/sessions', { params })
    return res.data
  },

  /**
   * 工作流的会话整表。light（#169 第二期）：较早的行只带首行预览并标 body_omitted，正文用 getWorkflowSessionBodies 按需补；
   * 不传时与以前一致（每一行都带完整正文，经典视图用）。tail 只给测试 / 特殊调用方用：保留正文的最近轮数，缺省由服务端按字节预算定。
   */
  async getWorkflowSessions(workflowId: string, opts: { light?: boolean; tail?: number } = {}): Promise<{ sessions: FeedbackSession[] }> {
    const params: Record<string, string | number> = {}
    if (opts.light) params.light = 1
    if (opts.light && opts.tail && opts.tail > 0) params.tail = opts.tail
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/sessions`, { params })
    return res.data
  },

  /** 按 id 取一批会话的完整正文（单次最多 50 个，服务端限定在这个工作流里；不存在的 id 略过）。 */
  async getWorkflowSessionBodies(workflowId: string, ids: readonly string[]): Promise<{ sessions: SessionBody[] }> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/sessions/bodies`, { params: { ids: ids.join(',') } })
    return res.data
  },

  async get(id: string): Promise<{ session: FeedbackSession }> {
    const res = await api.get(`/sessions/${encodeURIComponent(id)}`)
    return res.data
  },

  async submit(id: string, data: { response_text: string; user_messages?: string[]; images?: SessionImage[] }): Promise<{ status: string; session: FeedbackSession }> {
    // 草稿里已经上传到服务器的图片只发引用，服务端直接复用；引用的图片不在了就用内存里的正文重发一次
    return sendWithDraftImages(data.images, async wire => {
      const res = await api.post(`/sessions/${encodeURIComponent(id)}/submit`, withWireImages(data, wire))
      return res.data
    })
  },

  async cancel(id: string): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/cancel`)
    return res.data
  },

  async delete(id: string): Promise<{ status: string; deleted_session_id: string }> {
    const res = await api.delete(`/sessions/${encodeURIComponent(id)}`)
    return res.data
  },

  async restore(session: FeedbackSession): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post('/sessions/restore', session)
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

  /** 按秒设置挂起倒计时（0 = 秒回）。 */
  async updateWaitCountdownSeconds(id: string, countdownSeconds: number): Promise<{ status: string; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/wait_countdown`, { countdown_seconds: countdownSeconds })
    return res.data
  },

  /** 告诉服务端「用户还在」：plus = 点了 +1 分钟；typing = 输入心跳。没有智能体在挂着时 active=false。 */
  async presence(id: string, kind: 'plus' | 'typing'): Promise<{ status: string; active: boolean; hold_seconds?: number }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/presence`, { kind })
    return res.data
  },

  async revoke(id: string): Promise<{ status: string; revoked: any; session: FeedbackSession }> {
    const res = await api.post(`/sessions/${encodeURIComponent(id)}/revoke`)
    return res.data
  },

  async appendWorkflowFeedback(workflowId: string, data: { response_text: string; user_messages?: string[]; images?: SessionImage[]; host_name?: string; project_directory?: string }): Promise<{ status: string; type: 'session' | 'queued'; session?: FeedbackSession; queued?: any }> {
    return sendWithDraftImages(data.images, async wire => {
      const res = await api.post(`/workflows/${encodeURIComponent(workflowId)}/append`, withWireImages(data, wire))
      return res.data
    })
  },

  async listQueued(workflowId: string): Promise<{ queued_feedbacks: any[] }> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/queued`)
    return res.data
  },

  async revokeQueued(queuedId: number): Promise<{ status: string; revoked: any }> {
    const res = await api.post(`/feedbacks/queued/${queuedId}/revoke`)
    return res.data
  },

  /** 追加指令收件箱：每条追加的状态、智能体最近一次交互、手动开关。 */
  async getInbox(workflowId: string): Promise<WorkflowInbox> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/inbox`)
    return res.data
  },

  async getInboxConfig(): Promise<InboxConfigView> {
    const res = await api.get('/inbox/config')
    return res.data
  },

  async updateInboxConfig(patch: Partial<InboxConfig>): Promise<InboxConfigView> {
    const res = await api.put('/inbox/config', patch)
    return res.data
  }
}

/**
 * 网页手动新建会话（#174）的请求体。title 必填；其余是「高级选项」（#184），全部可选，不填就和以前完全一样：
 * workflow_id 自己指定的编号；first_message 首条指令（排进收件箱）；phase 起始阶段；
 * project_directory 把这个会话绑定到某个项目目录（D-161：智能体第一次汇报必须带同一个目录）；host_name 会话的归属主机标签。
 */
export interface CreateWorkflowRequest {
  title: string
  workflow_id?: string
  first_message?: string
  phase?: 'discuss' | 'plan' | 'dev'
  project_directory?: string
  host_name?: string
}

/** 服务端实际生效的高级选项（只写用户填了的那些项；一项也没填时整个字段不存在）。 */
export interface CreateWorkflowApplied {
  phase?: string
  first_message?: boolean
  project_directory?: string
  host_name?: string
}

/** 会话建好之后没做成的事（阶段 / 首条指令）：会话本身一定已经建好，原因在 error 里。 */
export interface CreateWorkflowWarning {
  field: string
  error: string
}

/** 网页手动新建会话（#174）的返回：占位会话 + 交给用户转发给智能体的启动提示词（+ #184：applied / warnings）。 */
export interface CreateWorkflowResult {
  status: string
  workflow_id: string
  session: FeedbackSession
  start_prompt: string
  applied?: CreateWorkflowApplied
  warnings?: CreateWorkflowWarning[]
}

export const workflowsApi = {
  /**
   * 网页手动新建会话：服务端落一行占位会话（notice，已完成、已被消费），名字固定为用户填的名字。
   * workflow_id 可不填（自动生成）；给定时必须合规且未被占用，否则 400 / 409。高级选项见 CreateWorkflowRequest。
   */
  async create(data: CreateWorkflowRequest): Promise<CreateWorkflowResult> {
    const res = await api.post('/workflows', data)
    return res.data
  },
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
  /** 自驾（autopilot）上报：对应 report_progress，与「执行命令」无关。 */
  execute: boolean
  /** 终端执行：terminal_targets / terminal_exec / terminal_run。缺省关闭，需人在权限页显式授予。 */
  terminal?: boolean
  /** 终端文件类工具（P2）。缺省关闭。 */
  terminal_files?: boolean
  /**
   * ExecMesh 兼容命名：终端工具以 ExecMesh 的名字（targets_list / exec / exec_start / job_* …）与返回形状暴露，
   * 方便既有 ExecMesh 客户端与提示词迁移。只改名字，不增加能力；没有终端执行权限时无意义。
   */
  terminal_compat?: boolean
  /**
   * 运行预设（#179）：preset_run 工具——项目管理 Agent 唤醒本项目里的子会话（运行已有的「非交互续聊」预设）。缺省关闭，需人在权限页显式授予；
   * 调用方还必须是所在项目的管理 Agent（项目页「任务管理」里勾选）。授予它与授予终端权限一样要再次验证密码。
   */
  launcher?: boolean
}

/** 令牌档位，由权限推导：relaymesh 只有治理工具 / execmesh 只有终端工具 / hybrid 两类都有 / none 什么都没有。 */
export type CredentialTier = 'relaymesh' | 'execmesh' | 'hybrid' | 'none'

/** 令牌的执行模式上限：空串跟随主机设置，只能比主机更严。 */
export type TerminalModeCap = '' | 'auto' | 'approve' | 'readonly'

/** 令牌级终端限额。全部缺省/为 0 表示不设限（默认）。 */
export interface TerminalLimits {
  max_concurrent?: number
  max_wait_seconds?: number
  max_timeout_seconds?: number
  max_output_bytes?: number
  allowed_dirs?: string[]
  /** 文件工具（terminal_files）允许访问的目录；带文件权限的令牌必须非空，除非明确开启下面的 allow_whole_fs。 */
  allowed_paths?: string[]
  /**
   * 「允许整个文件系统」：高风险开关，默认关闭。开启后文件工具不再受 allowed_paths 限制（目录设置原样保留，关闭后恢复）。
   * 只有刚刚重新验证过密码才能开启（服务端强制）；开启 / 关闭与每次文件操作（scope=whole_fs）都会写入审计。
   */
  allow_whole_fs?: boolean
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

  tier?: CredentialTier
  /** 令牌强度不足的原因；空串表示合格。带终端权限的令牌必须合格。 */
  token_weakness?: string
  terminal_hosts?: string[]
  terminal_mode_cap?: TerminalModeCap
  terminal_limits?: TerminalLimits
  expires_at?: string | null
  allowed_cidrs?: string[]
  last_used_at?: string | null
  last_used_ip?: string
  /**
   * 能力 URL 凭据：令牌直接写在专用网址 /mcp/c/<令牌> 里，给只能填一个网址的连接器使用。
   * 只能是 ExecMesh 档、必须逐台授权主机；创建后不可改成普通凭据，反之亦然。
   */
  capability?: boolean
}

/** 创建 / 更新凭据时可提交的字段（令牌本身由服务端生成，不可提交）。 */
export interface CredentialInput {
  name?: string
  host_name?: string
  is_active?: boolean
  permissions?: Partial<MCPPermissions>
  note?: string
  terminal_hosts?: string[]
  terminal_mode_cap?: TerminalModeCap
  terminal_limits?: TerminalLimits
  expires_at?: string | null
  allowed_cidrs?: string[]
  /** 仅创建时有效：创建能力 URL 凭据。更新时服务端会忽略该字段。 */
  capability?: boolean
}

export interface CredentialPreview {
  tier: CredentialTier
  tools: { name: string; summary: string }[]
  /** 该令牌 initialize 时收到的指引版本。 */
  instructions: 'governance' | 'terminal' | 'governance+terminal' | 'none'
  /** 混合令牌调用终端执行时必须带 workflow_id 与 description。 */
  terminal_requires_workflow: boolean
  /** 是否使用 ExecMesh 兼容命名（此时 tools 里是 ExecMesh 的工具名）。 */
  terminal_compat?: boolean
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

  async create(data: CredentialInput & { name: string }): Promise<{ status: string; credential: MCPCredential; token: string; capability_path?: string }> {
    const res = await api.post('/credentials', {
      ...data,
      // 新令牌默认是 RelayMesh 档：全部治理工具、不含终端；终端权限必须由调用方显式传入。
      permissions: { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true, terminal: false, terminal_files: false, terminal_compat: false, launcher: false, ...(data.permissions || {}) }
    })
    return res.data
  },

  async update(id: number, updates: CredentialInput): Promise<{ status: string; credential: MCPCredential }> {
    const res = await api.put(`/credentials/${id}`, updates)
    return res.data
  },

  /** 预览具有给定权限的令牌将暴露的工具与指引版本（由服务端按真实逻辑计算）。 */
  async preview(permissions: Partial<MCPPermissions>): Promise<CredentialPreview> {
    const res = await api.post('/credentials/preview', { permissions })
    return res.data
  },

  async remove(id: number): Promise<{ status: string }> {
    const res = await api.delete(`/credentials/${id}`)
    return res.data
  },

  async regenerateToken(id: number): Promise<{ status: string; credential: MCPCredential; token: string; capability_path?: string }> {
    const res = await api.post(`/credentials/${id}/regenerate`)
    return res.data
  },

  /**
   * 验证 Web 登录密码后读出某个令牌的完整明文（能力 URL 凭据同时返回网址路径）。
   * 密码错误是 403、被锁定是 429、令牌不存在是 404；开放模式（未设置登录密码）传空串即可。
   */
  async revealToken(id: number, password: string): Promise<{ status: string; token: string; capability_path?: string }> {
    const res = await api.post(`/credentials/${id}/reveal`, { password })
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

/**
 * 草稿图片云存储：图片一加入草稿就上传，草稿 JSON 只留引用，提交时服务端直接复用，草稿销毁时一并删除。
 * 单张图片可达数十 MB，传输超时比普通请求长得多。
 */
const DRAFT_IMAGE_TIMEOUT_MS = 120_000

export const draftImagesApi = {
  /** 该工作流在服务器上已有的草稿图片键（只含元数据，不含正文）。 */
  async list(workflowId: string): Promise<string[]> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/draft-images`)
    const list: Array<{ key?: string }> = Array.isArray(res.data?.images) ? res.data.images : []
    return list.map(item => item.key).filter((key): key is string => !!key)
  },

  /** 上传（幂等）。 */
  async put(
    workflowId: string,
    key: string,
    image: { name?: string; format?: string; data_type?: string; hash?: string; width?: number; height?: number; data: string }
  ): Promise<void> {
    await api.put(
      `/workflows/${encodeURIComponent(workflowId)}/draft-images/${encodeURIComponent(key)}`,
      image,
      { timeout: DRAFT_IMAGE_TIMEOUT_MS }
    )
  },

  /** 取回一张图片（含正文）；服务器上没有（404）返回 null。 */
  async get(workflowId: string, key: string): Promise<SessionImage | null> {
    try {
      const res = await api.get(
        `/workflows/${encodeURIComponent(workflowId)}/draft-images/${encodeURIComponent(key)}`,
        { timeout: DRAFT_IMAGE_TIMEOUT_MS }
      )
      const image = res.data?.image
      if (!image || !image.data) return null
      // 只取草稿图片该有的字段（服务端记录里的工作流 / 时间等不应混进草稿与提交内容）
      return {
        key,
        name: image.name,
        format: image.format,
        data_type: image.data_type,
        hash: image.hash,
        width: image.width,
        height: image.height,
        data: image.data
      }
    } catch (error) {
      if ((error as { response?: { status?: number } })?.response?.status === 404) return null
      throw error
    }
  },

  /** 删除（幂等：不存在也算成功）。 */
  async remove(workflowId: string, key: string): Promise<void> {
    await api.delete(`/workflows/${encodeURIComponent(workflowId)}/draft-images/${encodeURIComponent(key)}`)
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

export interface WorkflowSheetData {
  workflow_id: string
  content: string
  updated_at?: string
}

export const workflowSheetApi = {
  async get(workflowId: string): Promise<WorkflowSheetData> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/sheet`)
    return res.data
  },

  async save(workflowId: string, content: string): Promise<{ status: string; workflow_id: string }> {
    const res = await api.put(`/workflows/${encodeURIComponent(workflowId)}/sheet`, { content })
    return res.data
  }
}

/** 恢复胶囊的只读视图（Web 时间线用来在交付之间标出胶囊）。 */
export const workflowCheckpointsApi = {
  async list(workflowId: string, limit = 30): Promise<CheckpointView[]> {
    const res = await api.get(`/workflows/${encodeURIComponent(workflowId)}/checkpoints`, { params: { limit } })
    return Array.isArray(res.data?.checkpoints) ? res.data.checkpoints : []
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

  // SSE 一次性票据：EventSource 无法自定义请求头，过去把 7 天有效的 JWT 拼进 URL 会进入反代日志；
  // 现在先用 JWT 换取 30 秒、单次有效的票据再连接。
  async sseTicket(): Promise<{ auth_required: boolean; ticket: string; expires_in: number }> {
    const res = await api.post('/auth/sse_ticket', undefined, { timeout: 8000 })
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

export interface OAuthGrantRow {
  id: number
  client_name: string
  terminal_hosts?: string[]
  terminal_mode_cap?: string
  revoked: boolean
  last_used_at?: string | null
  last_used_ip?: string
  created_at: string
}

export interface OAuthPolicy {
  enabled: boolean
  public_url: string
  allow_dcr: boolean
  access_token_ttl_minutes: number
  refresh_idle_days: number
  refresh_absolute_days: number
  client_host_allowlist: string[]
  allow_chatgpt: boolean
  allow_claude: boolean
  allow_loopback: boolean
}

export const oauthApi = {
  async getRequest(id: string) {
    const res = await api.get(`/oauth/requests/${encodeURIComponent(id)}`)
    return res.data as { request: { client_name: string; redirect_host: string; scopes: string[]; warn_untrusted: boolean; warn_loopback: boolean } }
  },
  async decision(id: string, body: { approve: boolean; nonce: string; terminal_hosts?: string[]; terminal_mode_cap?: string }) {
    const res = await api.post(`/oauth/requests/${encodeURIComponent(id)}/decision`, body)
    return res.data as { redirect_url: string }
  },
  async listGrants() {
    const res = await api.get('/oauth/grants')
    return res.data as { grants: OAuthGrantRow[] }
  },
  async revokeGrant(id: number) {
    await api.delete(`/oauth/grants/${id}`)
  },
  async getPolicy() {
    const res = await api.get('/oauth/policy')
    return res.data as { policy: OAuthPolicy }
  },
  async savePolicy(policy: OAuthPolicy) {
    await api.put('/oauth/policy', { policy })
  },
}
