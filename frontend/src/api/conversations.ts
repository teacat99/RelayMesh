// 统一消息流（群聊窗口、预览视图的搜索与未读共用）的 REST 封装，对应 internal/api/conversations.go。
// 会话编号：一对一是 wf:<工作流>，群聊是 grp:<编号>。消息按「发送毫秒 → 来源序 → 主键」排序，游标是不透明字符串。

import { api } from './client'
import type { ConfirmItem } from './confirms'
import type { GroupInfo } from './groups'
import type { SessionImage } from './types'

export type ChatKind = 'text' | 'progress' | 'ask' | 'plan' | 'block' | 'decision' | 'alert' | 'system' | 'checkpoint'
export type ChatSenderKind = 'human' | 'agent' | 'system'
/** 消息状态：自己发的与 Agent 发的共用一套词；确认卡直接带 pending / answered / dismissed / withdrawn / expired。 */
export type ChatStatus =
  | 'sent' | 'delivered' | 'read' | 'handled' | 'retracted' | 'expired' | 'answered'
  | 'pending' | 'dismissed' | 'withdrawn'

export interface ChatSender {
  kind: ChatSenderKind
  id?: string
  name: string
  workflow_id?: string
}

export interface ChatAttachment {
  type: 'image'
  url: string
  name?: string
  width?: number
  height?: number
}

export interface ChatDecisionGroup {
  doc_id: number
  doc_path?: string
  title?: string
  pending: number
  items: ConfirmItem[]
}

export interface ChatTaskRef {
  seq: number
  title: string
  state: string
  from_state?: string
  to_state?: string
}

export interface ChatMessage {
  id: string
  conversation_id: string
  kind: ChatKind
  sender: ChatSender
  sent_at: string
  cursor: string
  title?: string
  text?: string
  hash?: string
  reply_to?: string
  status: ChatStatus | string
  status_at?: string
  attachments?: ChatAttachment[]
  card?: ConfirmItem
  task?: ChatTaskRef
  anchor?: { commit: string; subject?: string }
  decision?: ChatDecisionGroup
  source: { type: string; id: string }
  hops: number
  meta?: Record<string, unknown>
  /** 群聊：这条消息点名了谁（工作流编号 / all / human）。 */
  mentions?: ChatMention[]
  /** 群聊：已读情况 N/M 与名单。 */
  reads?: ChatReads
  /** 群聊：被点名成员各自的投递状态。 */
  deliveries?: ChatDelivery[]
}

export interface ChatMention {
  ref: string
  name: string
}

export interface ChatReads {
  read: number
  total: number
  readers?: string[]
  unread?: string[]
}

export interface ChatDelivery {
  ref: string
  name: string
  status: string
}

export interface ChatPage {
  conversation_id: string
  items: ChatMessage[]
  before_cursor?: string
  after_cursor?: string
  has_more_before: boolean
  has_more_after: boolean
  unread: number
  read_ms: number
  agent: ChatSender
  /** 群聊才有：群信息与成员。 */
  group?: GroupInfo
  read_seq?: number
}

export interface ChatListParams {
  limit?: number
  before?: string
  after?: string
  around?: string
  q?: string
  kinds?: string[]
}

export interface ChatSummary {
  id: string
  workflow_id: string
  unread: number
  last?: ChatMessage
  preview?: string
}

export interface ChatSendBody {
  text: string
  images?: SessionImage[]
  /** 引用某条 Agent 汇报：传它的消息编号（s:sess-xxxx），服务端在正文前补「【回复 哈希】」。 */
  reply_to?: string
  /** auto（默认，沿用投递规则）| inbox（强制进收件箱，不并入当前这一轮）。 */
  target?: 'auto' | 'inbox'
  /** 群聊：点名的成员（工作流编号或 all）。 */
  mentions?: string[]
}

export interface ChatSendResult {
  message: ChatMessage
  delivered_as?: 'round_reply' | 'inbox'
  /** 群聊：这条消息投递给了几位成员。 */
  delivered_to?: number
}

/** 群聊编号是 grp:<8 位十六进制>。 */
export function isGroupId(id: string | undefined | null): boolean {
  return !!id && id.startsWith('grp:')
}

/** 会话键：一对一传工作流编号，群聊传 grp: 编号；换成服务端的会话编号。 */
export function conversationIdOf(key: string): string {
  return isGroupId(key) ? key : `wf:${key}`
}

function base(conversationId: string): string {
  return `/conversations/${encodeURIComponent(conversationId)}`
}

export const conversationsApi = {
  async list(conversationId: string, params: ChatListParams = {}): Promise<ChatPage> {
    const query: Record<string, string | number> = {}
    if (params.limit) query.limit = params.limit
    if (params.before) query.before = params.before
    if (params.after) query.after = params.after
    if (params.around) query.around = params.around
    if (params.q) query.q = params.q
    if (params.kinds?.length) query.kinds = params.kinds.join(',')
    const { data } = await api.get<ChatPage>(`${base(conversationId)}/messages`, { params: query })
    return { ...data, items: data.items ?? [] }
  },

  async markRead(conversationId: string, cursor?: string): Promise<{ read_ms: number; unread: number }> {
    const { data } = await api.post<{ read_ms: number; unread: number }>(`${base(conversationId)}/read`, cursor ? { cursor } : {})
    return data
  },

  async send(conversationId: string, body: ChatSendBody): Promise<ChatSendResult> {
    const { data } = await api.post<ChatSendResult>(`${base(conversationId)}/messages`, body)
    return data
  },

  async summaries(conversationIds: string[]): Promise<ChatSummary[]> {
    if (conversationIds.length === 0) return []
    const { data } = await api.get<{ summaries: ChatSummary[] }>('/conversation-summaries', {
      params: { ids: conversationIds.join(',') },
    })
    return data.summaries ?? []
  },
}

/** 把接口错误翻译成可直接展示的话。 */
export function chatErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  const e = err as { response?: { data?: { error?: string } }; message?: string }
  return e?.response?.data?.error || e?.message || fallback
}
