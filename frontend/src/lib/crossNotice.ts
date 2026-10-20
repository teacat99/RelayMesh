// 跨项目提醒：人待在 ExecMesh 里时，RelayMesh 的新反馈请求界面上看不见（侧栏、会话列表都不在场），
// 只剩切换器上的一个小圆点；这里决定「要不要、弹什么」，用一条带「查看」入口的消息补上。
// 纯函数，便于单元测试；弹出与跳转由 composables/useCrossModeNotice.ts 完成。

import type { AppMode } from './mode'

/**
 * 「新」反馈请求的时间窗。页面刚打开时已经等了很久的请求，只靠切换器上的圆点提醒，不再弹消息，
 * 否则每刷新一次页面都会被同一条请求打扰一次。
 */
export const FRESH_REQUEST_MS = 2 * 60 * 1000

/** 消息停留的时间：比普通提示长，给人一个把手头的命令敲完再去看的余地。 */
export const NOTICE_DURATION_MS = 12_000

export interface RelayRequest {
  sessionId: string
  workflowId: string
  title?: string
  summary?: string
  /** 会话创建时间（服务端时间，ISO 字符串）。 */
  createdAt?: string
}

export interface CrossNotice {
  title: string
  description: string
  /** 操作按钮上的符号（与撤销用的 ↺ 同一套「符号化」按钮）。 */
  action: string
  durationMs: number
}

/**
 * 请求是否够新。没有时间戳、时间戳解析不了、或者比本机时钟还「新」（两边时钟有偏差）时，
 * 一律当作新的：宁可多提醒一次，也不能漏掉。
 */
export function isFreshRequest(createdAt: string | undefined, now: number, windowMs = FRESH_REQUEST_MS): boolean {
  if (!createdAt) return true
  const at = Date.parse(createdAt)
  if (!Number.isFinite(at)) return true
  return now - at <= windowMs
}

const MAX_DESCRIPTION = 80

/** 消息正文：优先用会话标题，其次取摘要的第一行有字的内容，最后兜底成一句话。 */
export function describeRequest(req: Pick<RelayRequest, 'title' | 'summary'>): string {
  const title = (req.title ?? '').trim()
  if (title) return clip(title)
  const firstLine = (req.summary ?? '')
    .split(/\r?\n/)
    .map(l => l.replace(/^[#>*\-\s`]+/, '').trim())
    .find(l => l.length > 0)
  return firstLine ? clip(firstLine) : '智能体在等你回复'
}

function clip(text: string): string {
  const chars = Array.from(text)
  return chars.length > MAX_DESCRIPTION ? chars.slice(0, MAX_DESCRIPTION - 1).join('') + '…' : text
}

/**
 * 某条新反馈请求到达时，在 current 这个项目里要弹什么。
 * 只有人待在 ExecMesh 时才弹：待在 RelayMesh 里这条请求界面上本来就看得见。返回 null 表示不弹。
 */
export function relayRequestNotice(current: AppMode, req: RelayRequest, now: number): CrossNotice | null {
  if (current !== 'exec') return null
  if (!isFreshRequest(req.createdAt, now)) return null
  return {
    title: 'RelayMesh 有新的反馈请求',
    description: describeRequest(req),
    action: '↗',
    durationMs: NOTICE_DURATION_MS,
  }
}

/** 同一条请求只对应一条消息：再次到达时替换，而不是叠出两条。 */
export function relayNoticeId(sessionId: string): string {
  return `relay-request:${sessionId}`
}
