// 通知偏好的 REST 封装（对应 internal/api/notify_prefs.go）：免打扰、ntfy 预览模式、各会话的提醒策略。
// 偏好存在服务端，跨设备共享；这里只做请求与形状整理，决策规则见 lib/notifyPrefs.ts。

import { api } from './client'
import { normalizePrefs, type ConvPolicy, type NotifyPrefs, type PreviewMode } from '../lib/notifyPrefs'

/** 一次读写的结果：整理后的偏好，加上服务端时钟与本机时钟的偏差（用来判断静音是否到期）。 */
export interface PrefsResult {
  prefs: NotifyPrefs
  /** 服务端时间 − 本机时间（毫秒）。 */
  skewMs: number
}

interface PrefsEnvelope {
  prefs?: unknown
  now?: string
}

function toResult(data: PrefsEnvelope, sentAt: number): PrefsResult {
  const serverNow = data?.now ? Date.parse(data.now) : NaN
  // 请求往返期间本机时间也在走：取发出请求与收到响应的中点，偏差更准
  const localMid = (sentAt + Date.now()) / 2
  return {
    prefs: normalizePrefs(data?.prefs),
    skewMs: Number.isFinite(serverNow) ? serverNow - localMid : 0,
  }
}

export const notifyApi = {
  async getPrefs(): Promise<PrefsResult> {
    const sentAt = Date.now()
    const res = await api.get<PrefsEnvelope>('/notify/prefs')
    return toResult(res.data, sentAt)
  },

  /** 修改全局偏好；只带要改的字段（dnd / ntfy_preview）。 */
  async updatePrefs(body: { dnd?: boolean; ntfy_preview?: PreviewMode }): Promise<PrefsResult> {
    const sentAt = Date.now()
    const res = await api.put<PrefsEnvelope>('/notify/prefs', body)
    return toResult(res.data, sentAt)
  },

  /** 设置某个会话的提醒策略；muted_until 只能与 muted 一起用，且必须晚于现在。策略与默认值相同时服务端会直接去掉这条记录。 */
  async setConversation(conversation: string, policy: ConvPolicy, mutedUntil?: string): Promise<PrefsResult> {
    const sentAt = Date.now()
    const body: Record<string, unknown> = { conversation, policy }
    if (mutedUntil) body.muted_until = mutedUntil
    const res = await api.put<PrefsEnvelope>('/notify/conversations', body)
    return toResult(res.data, sentAt)
  },

  /** 恢复某个会话的默认提醒策略。 */
  async clearConversation(conversation: string): Promise<PrefsResult> {
    const sentAt = Date.now()
    const res = await api.delete<PrefsEnvelope>('/notify/conversations', { params: { conversation } })
    return toResult(res.data, sentAt)
  },
}

/** 取出接口错误里给用户看的那句话。 */
export function notifyErrorMessage(err: unknown, fallback = '操作失败，请稍后重试'): string {
  const e = err as { response?: { data?: { error?: string } }; message?: string }
  return e?.response?.data?.error || e?.message || fallback
}
