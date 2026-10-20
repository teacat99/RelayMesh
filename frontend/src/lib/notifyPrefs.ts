// 通知偏好（服务端保存，各设备共享）的类型与纯函数。对应 internal/notify/prefs.go 与 internal/api/notify_prefs.go。
//
// 「这条消息该不该打扰你」的最终裁决在服务端（internal/notify 的 Decide）；
// 这里只负责：把接口返回的偏好整理成界面用的形状、推断某个会话当前的策略（控件的选中态与提示文案）、
// 以及生成静音的到期时间。纯函数，便于单元测试。

export type ConvPolicy = 'all' | 'mentions' | 'muted'
export type PreviewMode = 'auto' | 'always' | 'never'

export interface ConvPref {
  policy: ConvPolicy
  /** 静音到期时间（ISO 字符串）；只在 policy=muted 时可能有，没有表示一直静音。 */
  muted_until?: string
}

export interface NotifyPrefs {
  /** 免打扰：除穿透类（阻塞待答、@ 你、终端审批、守护告警）外，浏览器与 ntfy 都不再提醒。 */
  dnd: boolean
  /** 发往 ntfy 的消息里是否带预览：auto = 1:1 显示、跨项目群隐藏。 */
  ntfy_preview: PreviewMode
  /** 只记录与默认策略不同的会话。键：wf:<工作流> / grp:<群>。 */
  conversations: Record<string, ConvPref>
}

export const PREVIEW_MODES: ReadonlyArray<{ value: PreviewMode; label: string; hint: string }> = [
  { value: 'auto', label: '自动', hint: '单聊显示预览，跨项目的群里隐藏' },
  { value: 'always', label: '总是显示', hint: '所有推送都带正文预览' },
  { value: 'never', label: '从不显示', hint: '推送里只写「有新消息」，正文只在网页里看' },
]

export function emptyPrefs(): NotifyPrefs {
  return { dnd: false, ntfy_preview: 'auto', conversations: {} }
}

const POLICIES: readonly string[] = ['all', 'mentions', 'muted']
const PREVIEWS: readonly string[] = ['auto', 'always', 'never']

/** 把接口返回的偏好整理成界面用的形状：缺字段补默认，不认识的取值丢弃，绝不抛错。 */
export function normalizePrefs(raw: unknown): NotifyPrefs {
  const out = emptyPrefs()
  if (!raw || typeof raw !== 'object') return out
  const r = raw as Record<string, unknown>
  out.dnd = r.dnd === true
  if (typeof r.ntfy_preview === 'string' && PREVIEWS.includes(r.ntfy_preview)) out.ntfy_preview = r.ntfy_preview as PreviewMode
  const convs = r.conversations
  if (convs && typeof convs === 'object') {
    for (const [key, v] of Object.entries(convs as Record<string, unknown>)) {
      if (!v || typeof v !== 'object') continue
      const p = (v as Record<string, unknown>).policy
      if (typeof p !== 'string' || !POLICIES.includes(p)) continue
      const pref: ConvPref = { policy: p as ConvPolicy }
      const until = (v as Record<string, unknown>).muted_until
      if (p === 'muted' && typeof until === 'string' && until) pref.muted_until = until
      out.conversations[key] = pref
    }
  }
  return out
}

/** 一对一会话（按工作流）的会话键；工作流为空时没有会话键。 */
export function workflowConversation(workflowId: string | undefined | null): string {
  const id = (workflowId ?? '').trim()
  return id ? `wf:${id}` : ''
}

/** 没有单独设置时的默认策略：群默认只在被 @ 时提醒，其余（一对一）全部提醒。 */
export function defaultPolicy(conversation: string): ConvPolicy {
  return conversation.startsWith('grp:') ? 'mentions' : 'all'
}

/** 静音到期的时刻（毫秒时间戳）；没有设置到期时间（一直静音）或不是静音时返回 null。 */
export function mutedUntilMs(prefs: NotifyPrefs, conversation: string): number | null {
  const pref = prefs.conversations[conversation]
  if (!pref || pref.policy !== 'muted' || !pref.muted_until) return null
  const at = Date.parse(pref.muted_until)
  return Number.isFinite(at) ? at : null
}

/**
 * 某个会话此刻生效的策略：静音已到期的按默认策略算（与服务端 PolicyFor 一致）。
 * now 是「服务端认为的现在」（本机时钟加上与服务端的偏差）。
 */
export function policyOf(prefs: NotifyPrefs, conversation: string, now: number): ConvPolicy {
  const pref = prefs.conversations[conversation]
  if (!pref) return defaultPolicy(conversation)
  if (pref.policy === 'muted') {
    const until = mutedUntilMs(prefs, conversation)
    if (until !== null && until <= now) return defaultPolicy(conversation)
  }
  return pref.policy
}

export function isMuted(prefs: NotifyPrefs, conversation: string, now: number): boolean {
  return policyOf(prefs, conversation, now) === 'muted'
}

export interface MutePreset {
  id: string
  label: string
  /** 静音多久（毫秒）；null = 一直静音，直到手动恢复。 */
  durationMs: number | null
}

export const MUTE_PRESETS: readonly MutePreset[] = [
  { id: '1h', label: '静音 1 小时', durationMs: 60 * 60 * 1000 },
  { id: '8h', label: '静音 8 小时', durationMs: 8 * 60 * 60 * 1000 },
  { id: 'forever', label: '一直静音', durationMs: null },
]

/** 静音到期时间的 ISO 字符串（now + 时长）；一直静音返回 undefined（接口里不带 muted_until）。 */
export function muteUntilISO(durationMs: number | null, now: number): string | undefined {
  if (durationMs === null) return undefined
  return new Date(now + durationMs).toISOString()
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n)
}

/** 到期时刻的口语化写法：当天只写钟点，次日写「明天」，更远的写月/日。按本机时区。 */
export function formatMuteUntil(untilMs: number, now: number): string {
  const until = new Date(untilMs)
  const clock = `${pad2(until.getHours())}:${pad2(until.getMinutes())}`
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  const days = Math.round((startOfDay(until) - startOfDay(new Date(now))) / 86_400_000)
  if (days <= 0) return clock
  if (days === 1) return `明天 ${clock}`
  return `${until.getMonth() + 1}/${until.getDate()} ${clock}`
}

/** 会话策略的一句话说明，给控件的提示用；默认策略返回空串。 */
export function describePolicy(prefs: NotifyPrefs, conversation: string, now: number): string {
  const policy = policyOf(prefs, conversation, now)
  if (policy === defaultPolicy(conversation)) return ''
  if (policy === 'mentions') return '只在被 @ 时提醒'
  if (policy === 'muted') {
    const until = mutedUntilMs(prefs, conversation)
    return until === null ? '已静音（直到手动恢复）' : `静音到 ${formatMuteUntil(until, now)}`
  }
  return ''
}
