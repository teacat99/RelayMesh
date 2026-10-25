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
  /** 免打扰：除穿透类（阻塞待答、@ 你、终端审批、系统告警）外，浏览器与 ntfy 都不再提醒。 */
  dnd: boolean
  /** 发往 ntfy 的消息里是否带预览：auto = 1:1 显示、跨项目群隐藏。 */
  ntfy_preview: PreviewMode
  /**
   * 「指派久无人接」提醒的总开关：为真表示不再提醒（网页与 ntfy 都不发）。只管提醒，不管阈值——
   * 任务行上的「已等」标红与 MCP 回执里的 board 行仍按阈值显示。
   */
  board_stale_off: boolean
  /** 用户选的阈值（分钟，30~1440）；0 = 没改过、跟着出厂默认走。 */
  board_stale_minutes: number
  /** 生效的阈值（分钟）：服务端算好给出，任务行变红、回执头与提醒用的都是它。第一次取到之前是出厂默认。 */
  board_stale_after_minutes: number
  /** 出厂默认（分钟）：由服务端给出，界面不抄这个数（这里的 BOARD_STALE_FALLBACK_MINUTES 只在第一次取到之前顶替）。 */
  board_stale_default_minutes: number
  /** 只记录与默认策略不同的会话。键：wf:<工作流> / grp:<群>。 */
  conversations: Record<string, ConvPref>
}

export const PREVIEW_MODES: ReadonlyArray<{ value: PreviewMode; label: string; hint: string }> = [
  { value: 'auto', label: '自动', hint: '单聊显示预览，跨项目的群里隐藏' },
  { value: 'always', label: '总是显示', hint: '所有推送都带正文预览' },
  { value: 'never', label: '从不显示', hint: '推送里只写「有新消息」，正文只在网页里看' },
]

/** 「久无人接」阈值的取值范围与第一次取到服务端偏好之前的顶替值：与 internal/notify/prefs.go 的 BoardStale* 一致。 */
export const BOARD_STALE_MIN_MINUTES = 30
export const BOARD_STALE_MAX_MINUTES = 1440
export const BOARD_STALE_FALLBACK_MINUTES = 120

/** 设置里「久无人接」阈值的可选项（分钟）；已经设成别的值（例如直接调接口设的 90 分钟）时，当前值会临时补进选项里。 */
export const BOARD_STALE_CHOICES: readonly number[] = [30, 60, 120, 180, 240, 360, 480, 720, 1440]

export function emptyPrefs(): NotifyPrefs {
  return {
    dnd: false,
    ntfy_preview: 'auto',
    board_stale_off: false,
    board_stale_minutes: 0,
    board_stale_after_minutes: BOARD_STALE_FALLBACK_MINUTES,
    board_stale_default_minutes: BOARD_STALE_FALLBACK_MINUTES,
    conversations: {},
  }
}

function minutesIn(v: unknown, fallback: number, allowZero: boolean): number {
  if (typeof v !== 'number' || !Number.isInteger(v)) return fallback
  if (allowZero && v === 0) return 0
  return v >= BOARD_STALE_MIN_MINUTES && v <= BOARD_STALE_MAX_MINUTES ? v : fallback
}

/** 阈值的写法：整小时写「N 小时」，其余写「N 分钟」（与服务端提醒、MCP 回执头同一种写法）。 */
export function boardStaleLabel(minutes: number): string {
  return minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} 小时` : `${minutes} 分钟`
}

/** 生效的「久无人接」阈值（毫秒）：任务行的「已等」标红用它。 */
export function boardStaleMs(prefs: Pick<NotifyPrefs, 'board_stale_after_minutes'>): number {
  return prefs.board_stale_after_minutes * 60_000
}

export interface BoardStaleChoice {
  /** 选项的值：恰好等于出厂默认的那一项是 0（「恢复默认」，以后默认值调整时跟着走），其余是分钟数。 */
  value: number
  label: string
}

/**
 * 设置页下拉框的选项：预设项 + 当前生效值（不在预设里时补进来，免得下拉框显示成空白）；出厂默认那一项标「（默认）」。
 * 选中项的值用 boardStaleSelected 取，保持与选项的 value 同一种口径。
 */
export function boardStaleChoices(prefs: Pick<NotifyPrefs, 'board_stale_after_minutes' | 'board_stale_default_minutes'>): BoardStaleChoice[] {
  const def = prefs.board_stale_default_minutes
  const minutes = [...BOARD_STALE_CHOICES]
  if (!minutes.includes(def)) minutes.push(def)
  if (!minutes.includes(prefs.board_stale_after_minutes)) minutes.push(prefs.board_stale_after_minutes)
  minutes.sort((a, b) => a - b)
  return minutes.map(m => ({
    value: m === def ? 0 : m,
    label: m === def ? `${boardStaleLabel(m)}（默认）` : boardStaleLabel(m),
  }))
}

/** 下拉框当前选中项的值：没改过（或改成了与默认相同的值）是 0，其余是生效的分钟数。 */
export function boardStaleSelected(prefs: Pick<NotifyPrefs, 'board_stale_after_minutes' | 'board_stale_default_minutes'>): number {
  return prefs.board_stale_after_minutes === prefs.board_stale_default_minutes ? 0 : prefs.board_stale_after_minutes
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
  out.board_stale_off = r.board_stale_off === true
  // 默认值与生效值以服务端给的为准；缺字段（旧服务端、或回滚到旧版本）时退回顶替值，绝不让界面出现 NaN 或 0 分钟
  out.board_stale_default_minutes = minutesIn(r.board_stale_default_minutes, BOARD_STALE_FALLBACK_MINUTES, false)
  out.board_stale_minutes = minutesIn(r.board_stale_minutes, 0, true)
  out.board_stale_after_minutes = minutesIn(
    r.board_stale_after_minutes,
    out.board_stale_minutes || out.board_stale_default_minutes,
    false,
  )
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
