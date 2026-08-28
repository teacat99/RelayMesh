export interface SessionImage {
  name?: string
  format?: string
  data: string
  data_type?: string
}

export interface FeedbackSession {
  session_id: string
  workflow_id?: string
  host_name?: string
  project_directory?: string
  title?: string
  summary: string
  status: 'pending' | 'completed' | 'timeout' | 'cancelled' | 'archived'
  user_presence?: 'online' | 'away' | 'autopilot'
  response_text?: string
  user_messages?: string[]
  images?: SessionImage[]
  consumed_by_ai?: boolean
  timeout_seconds?: number
  no_feedback_checks?: number
  max_no_feedback_checks?: number
  prompt_wait_minutes?: number
  wait_countdown_minutes?: number
  is_mcp_active?: boolean
  mcp_active_at?: string
  mcp_timeout_seconds?: number
  last_keepalive_at?: string
  deadline_at?: string
  created_at: string
  updated_at: string
}

export interface QueuedFeedback {
  id: number
  workflow_id: string
  host_name?: string
  project_directory?: string
  response_text: string
  user_messages?: string[]
  images?: SessionImage[]
  status: 'pending' | 'consumed' | 'revoked'
  created_at: string
  updated_at: string
}

export interface RevokedFeedbackResult {
  session_id?: string
  workflow_id?: string
  queued_id?: number
  response_text: string
  user_messages: string[]
  images: SessionImage[]
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

export interface TaskSummary {
  task_id: string
  project_id: string
  state: 'active' | 'closed' | 'archived'
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
  body: string
  references?: PathReference[]
  created_at: string
}
