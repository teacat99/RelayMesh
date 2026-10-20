export interface SessionImage {
  name?: string
  format?: string
  data?: string
  data_type?: string
  hash?: string
  width?: number
  height?: number
  url?: string
  /** 仅草稿使用：整段正文的内容键（本地大图仓库 / 服务端草稿据此认图），提交时服务端会忽略。 */
  key?: string
  /** 仅草稿使用：本机与服务端都找不到这张图的正文，界面标红提示，不会被提交或上传。 */
  lost?: boolean
  /** 仅草稿使用（只在内存）：正文已经在服务器的草稿图片存储里，草稿 JSON 只带引用。 */
  synced?: boolean
  /**
   * 仅提交时使用（传输层）：引用服务器上已经上传的草稿图片。服务端据此直接复用，请求里不再带正文；
   * 服务端读出后保存的会话图片里不会有这个字段。
   */
  draft_key?: string
  /** 仅提交时使用：draft_key 所属的草稿工作流（发送前会提到请求顶层，不会出现在图片里）。 */
  draft_workflow_id?: string
}

export interface FeedbackSession {
  session_id: string
  workflow_id?: string
  host_name?: string
  project_directory?: string
  title?: string
  summary: string
  status: 'pending' | 'completed' | 'timeout' | 'cancelled' | 'archived'
  response_text?: string
  user_messages?: string[]
  images?: SessionImage[]
  consumed_by_ai?: boolean
  consumed_at?: string
  /** 人类提交这条回复的时刻（历史会话没有，界面回退到 updated_at）；与 consumed_at（智能体取走的时刻）不是一回事。 */
  responded_at?: string
  timeout_seconds?: number
  no_feedback_checks?: number
  max_no_feedback_checks?: number
  prompt_wait_minutes?: number
  wait_countdown_minutes?: number
  wait_countdown_seconds?: number
  is_mcp_active?: boolean
  mcp_active_at?: string
  mcp_timeout_seconds?: number
  last_keepalive_at?: string
  /** 工作流级只读聚合（只有会话列表与当前会话接口带，#159）：这个工作流待答复的确认项数。没带 = 旧服务端 / 没算出来；带了为 0 = 确实没有。 */
  pending_confirm_count?: number
  /** 工作流级只读聚合（同上）：智能体最近一次与 RelayMesh 交互的时间；服务端没有记录时不带。 */
  agent_last_activity_at?: string | null
  /** 工作流级只读聚合（同上，#173）：这个工作流在库里一共有多少行会话（含已归档）。列表只带最新一批行，「N 轮」徽标不能拿行数凑；没带 = 旧服务端 / 没算出来。 */
  workflow_rounds?: number
  deadline_at?: string
  /** 交付锚点：模型随本次交付申报的仓库最新提交；时间就是本轮的 created_at（服务端时间）。 */
  git_commit?: string
  git_subject?: string
  /**
   * notice = 异步对话模式下智能体不阻塞地发布的进度 / 回答气泡：没有回复框，发布即完成，
   * 不算「轮次」。常规汇报没有这个字段。
   */
  kind?: 'notice'
  /**
   * 轻量行（#169 第二期）：工作流会话表按 ?light=1 取时，较早的行 summary / response_text 只剩首行预览、user_messages / images 为空，
   * 完整正文要另取（sessionsApi.getWorkflowSessionBodies）。预览不是正文——页面不能把它当正文渲染，要显示骨架并去取正文。
   * 不是轻量行时没有这个字段。
   */
  body_omitted?: boolean
  created_at: string
  updated_at: string
}

/** GET /workflows/{id}/sessions/bodies 的一行：只有正文相关字段与定位用的 id / 版本（updated_at），用来补齐缓存里的轻量行。 */
export interface SessionBody {
  session_id: string
  updated_at: string
  summary: string
  response_text?: string
  user_messages?: string[]
  images?: SessionImage[]
}

/** 追加指令的状态：已排队 → 智能体已读取 → 智能体已确认；旁支：读取前撤回 / 读取后撤回 / 超过保留期。 */
export type InboxState = 'queued' | 'delivered' | 'acked' | 'revoked' | 'retracted' | 'expired'
/** 智能体是经哪条路径发现这条追加的。 */
export type InboxVia = 'feedback' | 'hook' | 'receipt' | 'tool'

export interface QueuedFeedback {
  id: number
  workflow_id: string
  host_name?: string
  project_directory?: string
  response_text: string
  user_messages?: string[]
  images?: SessionImage[]
  status: 'pending' | 'consumed' | 'revoked' | 'expired'
  created_at: string
  updated_at: string
  // 追加指令收件箱：服务端算好的状态与时间线（旧服务端没有这些字段）
  kind?: string
  state?: InboxState
  delivered_at?: string
  delivered_via?: InboxVia | ''
  acked_at?: string
  notified_count?: number
  last_notified_at?: string
  receipt_hint_at?: string
  retracted_at?: string
}

export interface RevokedFeedbackResult {
  session_id?: string
  workflow_id?: string
  queued_id?: number
  response_text: string
  user_messages: string[]
  images: SessionImage[]
  /** 撤回的是智能体已经读取过的追加：它下次读取收件箱时会被告知忽略。 */
  delivered?: boolean
}

/** 追加指令收件箱的手动开关：总开关 + 三层发现机制各自的开关。 */
export interface InboxConfig {
  enabled: boolean
  receipt_hint: boolean
  hooks: boolean
  prompt_clause: boolean
  hook_install_hint?: boolean
}

export type AgentLiveState = 'running' | 'idle' | 'quiet' | 'unknown'

export interface AgentLiveView {
  state: AgentLiveState
  reason?: string
  at?: string | null
}

export interface InboxConfigView extends InboxConfig {
  /** 服务端用环境变量 RELAYMESH_INBOX=off 强制关闭了总开关。 */
  env_off: boolean
  /** 叠加环境变量之后真正生效的开关。 */
  effective: InboxConfig
}

export interface InboxSummary {
  unread: number
  delivered: number
  retract_notices: number
  oldest_unread_at?: string
  latest_unread_at?: string
  latest_preview?: string
}

export interface WorkflowInbox {
  workflow_id: string
  items: QueuedFeedback[]
  summary: InboxSummary
  /** 智能体最近一次调用 RelayMesh 的时间（只在服务端内存里，重启后清空）。 */
  agent_last_activity_at: string | null
  /** 编辑器 hook 最近一次来问收件箱的时间（同样只在内存里）；null = 没来过，多半是还没装。 */
  hook_last_seen_at: string | null
  agent_live?: AgentLiveView
  config: InboxConfigView
  /** 这个工作流的异步对话状态（与 hook 芯片同源，旧服务端没有这个字段）。 */
  async_mode?: AsyncModeView
}

/** 授权档位：异步对话模式下取代「讨论 / 方案 / 开发」阶段提示词，告诉智能体哪些事可以直接做。 */
export interface AuthProfile {
  id: number
  name: string
  /** L0 只读（写入先问）/ L1 可回滚的改动直接做；不可逆操作在任何档位下都要确认，所以没有 L2。 */
  level: 'L0' | 'L1'
  rules: string
  is_default: boolean
  sort: number
  created_at: string
  updated_at: string
}

export interface AuthProfileLimits {
  max: number
  name_max: number
  rules_max: number
}

export interface AuthProfileList {
  items: AuthProfile[]
  default_id: number
  limits: AuthProfileLimits
}

/** 某个工作流此刻的异步对话状态（对应 internal/api/async_mode.go 的 asyncModeView）。 */
export interface AsyncModeView {
  workflow_id: string
  /** 用户的开关。真正生效看 effective（开关开着 且 编辑器 hook 在线）。 */
  enabled: boolean
  ever_enabled: boolean
  effective: boolean
  mode: 'async' | 'sync'
  /** 没生效的原因：disabled 用户已关 / no_hook 没见过 hook / hook_stale hook 太久没出现。 */
  reason?: 'disabled' | 'no_hook' | 'hook_stale'
  reason_text?: string
  /** 这个工作流至少收到过一次 hook 事件；没收到过就不能开启（编辑器里还没装 hook）。 */
  can_enable: boolean
  hook_last_seen_at?: string
  hook_fresh_minutes: number
  enabled_at?: string
  /** 工作流选用的档位（0 = 跟随默认）与实际解析出的档位。 */
  profile_id: number
  profile?: AuthProfile
  /** 回复窗口截止时间（#123）：智能体每发新消息累加，用户回复清零；空 = 当前没有在等回复。 */
  reply_window_until?: string | null
  /** 智能体手头有进行中 / 验证中的任务：窗口暂停（用户 2026-10-15：倒计时只在没有任务时触发）；此时 bank_sec 是攒着的剩余秒数。 */
  reply_window_paused?: boolean
  reply_window_bank_sec?: number
  active_tasks?: number
  /** 每条新消息给回复窗口加多少分钟、智能体单次计时器最长等多少分钟。 */
  window_add_minutes?: number
  wait_slice_minutes?: number
}

export interface WorkflowDraft {
  workflow_id: string
  active_index: number
  drafts_json: string
  updated_at: string
}

export interface PathReference {
  path: string
  line?: number
  description?: string
}

export interface Segment {
  name: string
  content: string
  position?: number
  updated_revision?: number
}

export interface WaitPolicy {
  after_minutes: number
  max_no_feedback_checks: number
  wait_instruction?: string
  exhausted_instruction?: string
}

export interface TaskStage {
  id: string
  name: string
  status: 'pending' | 'in_progress' | 'completed' | 'blocked'
  summary?: string
  evidence?: string
  updated_at?: string
}

export interface TaskSummary {
  task_id: string
  project_id: string
  title?: string
  mode?: string
  state: 'active' | 'closed' | 'archived' | 'paused'
  current_stage_id?: string
  stages?: TaskStage[]
  commander_session_id?: string
  executor_session_id?: string
  revision: number
  report_sequence: number
  feedback_sequence: number
  acknowledged_report_sequence: number
  unread_report_count: number
  updated_at: string
}

export interface TaskDetail extends TaskSummary {
  wait_policy: WaitPolicy
  segments: Segment[]
  created_at: string
}

export interface Report {
  task_id: string
  sequence: number
  acknowledged_task_revision: number
  kind: 'progress' | 'stage' | 'evidence' | 'question' | 'completion'
  body: string
  references?: PathReference[]
  created_at: string
}

export interface Feedback {
  task_id: string
  sequence: number
  task_revision: number
  source?: 'human' | 'commander'
  body: string
  references?: PathReference[]
  created_at: string
}
