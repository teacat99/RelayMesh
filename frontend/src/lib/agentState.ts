// Agent 身份与状态的前端口径（#113，设计稿 04 §2 / §3）。
//
// 只放纯逻辑：服务端给出的会话视图 → 状态徽标的文字 / 颜色 / 动画 / 附加标志；基础状态四项（S-1）；
// 同一工作流里有多个会话时展示哪一个；SSE 事件先就地改一下缓存（再慢慢重新取）。组件只负责摆放。
//
// 服务端（internal/agentstate/evaluate.go）已经把「离线 / 等你 / 卡住」这类随时间推导的状态算好放在
// state / stalled / stalled_soft 等字段里；这里不重复判断阈值，只翻译成人话和颜色。

import type { QueuedFeedback } from '../api/types'
import { ago, stateOf } from './inbox'

// ---------------------------------------------------------------------------
// 接口形状（对应 internal/store/agent_state.go 的 AgentIdentityView / AgentSessionView）
// ---------------------------------------------------------------------------

/** 展示态：状态机里的状态，再叠加 waiting_you / offline。 */
export type AgentStateName =
  | 'idle'
  | 'working'
  | 'waiting_approval'
  | 'aborted'
  | 'error'
  | 'ended'
  | 'waiting_you'
  | 'offline'

export interface AgentIdentity {
  id: number
  credential_id: number
  host_name: string
  project_directory: string
  /** 用户改的名字；空 = 用目录名 */
  name: string
  directory_name: string
  /** 服务端算好的显示名（改名 ?? 目录名，重名降级）；本地改名后由 identity.ts 重新算 */
  label: string
  label_degraded: boolean
  client_kind?: string
  cursor_version?: string
  first_seen_at: string
  last_seen_at: string
  last_hook_at?: string
  last_mcp_at?: string
  last_query_at?: string
}

export interface AgentSession {
  id: number
  identity_id: number
  workflow_id: string
  conversation_id: string
  client_kind?: string
  cursor_version?: string
  identity_label: string
  host_name: string
  project_directory: string
  started_at?: string
  ended_at?: string
  end_reason?: string
  last_tool?: string
  tool_started_at?: string
  turn_started_at?: string
  turn_ended_at?: string
  last_event_at: string
  last_mcp_at?: string
  last_query_at?: string
  state: AgentStateName | ''
  base_state: AgentStateName | ''
  /** 工作中的子状态：thinking / responding / tool:<名> */
  sub?: string
  /** 现在这个状态是谁说的：hook（编辑器 hook 的汇报）| tui（托管终端的屏幕）| run（Hub 读到的非交互运行输出）；旧数据没有 */
  state_source?: string
  since: string
  quiet_seconds: number
  waiting_you: boolean
  stalled: boolean
  stalled_soft: boolean
  approval_suspected: boolean
  approval_overdue: boolean
  /** quiet_soft | quiet | tool_running | tool_overrun | approval_overdue | tokens_flat | terminal_frozen */
  reason?: string
  // 托管终端的屏幕信号（M3.2）；没有可用的屏幕扫描结果时这些字段都不出现。
  /** ok = 屏幕版本已验证、信号被采信；unrecognized = 版本未验证或画面认不出，只展示不判断 */
  tui?: 'ok' | 'unrecognized'
  /** 状态行动词（Thinking / Globbing …），仅在画面还在动时有值 */
  live_word?: string
  tokens_live?: number
  /** tui */
  tokens_source?: string
  ctx_pct?: number
  screen_at?: string
}

/** SSE agent_state 事件的载荷（internal/mcp/agent_state.go 的 notifyAgentState）。 */
export interface AgentStateEvent {
  workflow_id: string
  identity_id: number
  session_id: number
  state: string
  sub?: string
  /** 现在这个状态是谁说的：hook | tui | run；旧版服务端不带 */
  source?: string
  since?: string
}

// ---------------------------------------------------------------------------
// 时间文字
// ---------------------------------------------------------------------------

/** 一段时间的人话：45 秒 / 3 分钟 / 2 小时 5 分 / 3 天。 */
export function spanText(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000))
  if (sec < 60) return `${sec} 秒`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟`
  const hour = Math.floor(min / 60)
  if (hour < 48) {
    const m = min % 60
    return m > 0 ? `${hour} 小时 ${m} 分` : `${hour} 小时`
  }
  return `${Math.floor(hour / 24)} 天`
}

/** 服务端用 Go 的零值时间表示「没有」；早于 2000 年的一律当作没有。 */
export function validTime(iso: string | null | undefined): number | null {
  if (!iso) return null
  const t = Date.parse(iso)
  if (!Number.isFinite(t) || t < Date.UTC(2000, 0, 1)) return null
  return t
}

function latestIso(...values: Array<string | null | undefined>): string | null {
  let best: number | null = null
  let bestIso: string | null = null
  for (const v of values) {
    const t = validTime(v)
    if (t !== null && (best === null || t > best)) {
      best = t
      bestIso = v ?? null
    }
  }
  return bestIso
}

// ---------------------------------------------------------------------------
// 状态徽标
// ---------------------------------------------------------------------------

export type AgentStateKey =
  | 'working'
  | 'idle'
  | 'waiting_you'
  | 'waiting_approval'
  | 'aborted'
  | 'error'
  | 'ended'
  | 'offline'
  | 'stalled'
  | 'unknown'

/** 颜色语义：active 干活中、calm 正常空闲、attention 需要你、warn 留意、danger 出问题、muted 无需关注。 */
export type AgentTone = 'active' | 'calm' | 'attention' | 'warn' | 'danger' | 'muted'

export type AgentFlag = '' | 'stalled_soft' | 'approval_suspected' | 'approval_overdue'

export interface AgentStateView {
  key: AgentStateKey
  /** 徽标上的短文字：工作中 / 空闲 / 等你回答 … */
  text: string
  /** 子活动或补充：思考 / 回应 / 工具 bash / 已收尾；没有则为空 */
  detail: string
  tone: AgentTone
  pulse: boolean
  /** 附加标志（软标记 / 疑似）：显示在徽标旁，不改变主状态 */
  flag: AgentFlag
  flagText: string
  /** 悬停全文（多行） */
  title: string
}

const TOOL_NAME_MAX = 24

function clip(s: string, n: number): string {
  const chars = [...s]
  return chars.length > n ? `${chars.slice(0, n - 1).join('')}…` : s
}

/** 工作中的子状态：thinking → 思考；responding → 回应；tool:bash → 工具 bash。 */
export function subText(sub: string | undefined): string {
  const s = (sub ?? '').trim()
  if (s === '') return ''
  if (s === 'thinking') return '思考'
  if (s === 'responding') return '回应'
  if (s.startsWith('tool:')) {
    const name = s.slice('tool:'.length).trim()
    return name ? `工具 ${clip(name, TOOL_NAME_MAX)}` : '工具'
  }
  return clip(s, TOOL_NAME_MAX)
}

const END_REASON_TEXT: Record<string, string> = {
  completed: '已收尾',
  blocked: '受阻',
  aborted: '被中断',
  handoff: '已移交',
  error: '出错结束',
  closed: '已关闭',
}

export function endReasonText(reason: string | undefined): string {
  const r = (reason ?? '').trim()
  return END_REASON_TEXT[r] ?? ''
}

const UNKNOWN_VIEW: AgentStateView = {
  key: 'unknown',
  text: '未接入',
  detail: '',
  tone: 'muted',
  pulse: false,
  flag: '',
  flagText: '',
  title: '还没有收到这个工作流的 hook 汇报：装好编辑器 hook 之后才有运行状态。',
}

/** 状态来源的短写法（卡片「来源」一行用）；不认识的来源返回空串。 */
export function sourceText(source: string | undefined): string {
  switch ((source ?? '').trim()) {
    case 'run':
      return '运行输出'
    case 'hook':
      return '编辑器 hook'
    case 'tui':
      return '终端画面'
  }
  return ''
}

/** 状态来源的一句话解释（悬停与卡片脚注用）。 */
export function sourceNote(source: string | undefined): string {
  switch ((source ?? '').trim()) {
    case 'run':
      return '状态来自 RelayMesh 读到的这次非交互运行的输出（stream-json）：每个思考、工具调用、回复和结束都是确定的事件，不依赖编辑器 hook；运行期间 hook 的汇报只留记录、不改状态。'
    case 'tui':
      return '状态来自托管终端的屏幕：在 RelayMesh 启动的终端里跑 CLI 时，能确定地标出「等你批准」，并显示实时 token 与上下文占用。'
  }
  return '状态由编辑器 hook 的汇报推断；在 RelayMesh 启动的终端里跑 CLI 时，还能确定地标出「等你批准」，并显示实时 token 与上下文占用，否则只能标「疑似」。'
}

/** 非交互运行结束后空闲态的补充说明（#125 ①）：对话没有结束，还能接着聊。 */
export const RUN_ENDED_TEXT = '本次运行结束（可续聊）'

function stalledDetail(s: AgentSession, nowMs: number): string {
  if (s.reason === 'terminal_frozen') return '终端画面已冻结，没有任何输出'
  if (s.reason === 'tool_overrun') {
    const start = validTime(s.tool_started_at) ?? validTime(s.since)
    const tool = (s.last_tool ?? '').trim() || subText(s.sub).replace(/^工具\s*/, '')
    const ran = start === null ? '' : `已运行 ${spanText(nowMs - start)}`
    return [tool ? `工具 ${clip(tool, TOOL_NAME_MAX)}` : '工具', ran].filter(Boolean).join(' ')
  }
  const last = validTime(s.last_event_at)
  const quiet = last === null ? s.quiet_seconds * 1000 : Math.max(nowMs - last, s.quiet_seconds * 1000)
  return `已 ${spanText(quiet)} 没有任何动静`
}

/** 把一个会话的服务端视图翻译成徽标。session 为空 = 这个工作流还没有 hook 汇报。 */
export function stateView(s: AgentSession | null | undefined, nowMs: number = Date.now()): AgentStateView {
  if (!s || !s.state) return UNKNOWN_VIEW

  const since = validTime(s.since)
  const lasted = since === null ? '' : `已持续 ${spanText(nowMs - since)}`
  const lines = (head: string, extra: string[] = []): string => [head, ...extra].filter(Boolean).join('\n')
  const fromRun = s.state_source === 'run'

  // 硬卡住：值得第一眼看到（服务端只对 working 判断，且只有它可以触发告警）
  if (s.stalled) {
    const detail = stalledDetail(s, nowMs)
    return {
      key: 'stalled',
      text: '疑似卡住',
      detail,
      tone: 'danger',
      pulse: true,
      flag: '',
      flagText: '',
      title: lines(`疑似卡住：${detail}`, ['不一定真的卡住：长时间思考、长工具、等批准期间也会没有动静。']),
    }
  }

  switch (s.state) {
    case 'working': {
      const detail = subText(s.sub)
      let flag: AgentFlag = ''
      let flagText = ''
      if (s.stalled_soft) {
        flag = 'stalled_soft'
        flagText = s.reason === 'tokens_flat' ? 'token 计数停了，但画面还在动，可能卡住' : '可能卡住'
      } else if (s.approval_suspected) {
        flag = 'approval_suspected'
        flagText = '工具执行中，可能在等批准'
      }
      return {
        key: 'working',
        text: '工作中',
        detail,
        tone: flag === 'stalled_soft' ? 'warn' : 'active',
        pulse: true,
        flag,
        flagText,
        title: lines(`工作中${detail ? `（${detail}）` : ''}`, [
          lasted,
          flagText ? `提示：${flagText}` : '',
          fromRun ? '状态来自运行输出（stream-json）' : '',
        ]),
      }
    }
    case 'idle':
      return {
        key: 'idle',
        text: '空闲',
        detail: fromRun ? RUN_ENDED_TEXT : '',
        tone: 'calm',
        pulse: false,
        flag: '',
        flagText: '',
        title: lines(
          fromRun
            ? '空闲：本次非交互运行已经结束，对话没有结束，可以接着聊（再运行一次预设，或在编辑器里继续）'
            : '空闲：这一轮已经结束，等你的下一条指令',
          [lasted],
        ),
      }
    case 'waiting_you':
      return {
        key: 'waiting_you',
        text: '等你回答',
        detail: '',
        tone: 'attention',
        pulse: true,
        flag: '',
        flagText: '',
        title: lines('Agent 正阻塞等你回答（不是卡住）', [lasted]),
      }
    case 'waiting_approval': {
      const overdue = s.approval_overdue
      return {
        key: 'waiting_approval',
        text: '等你批准',
        detail: '',
        tone: 'attention',
        pulse: true,
        flag: overdue ? 'approval_overdue' : '',
        flagText: overdue ? '等批准太久了' : '',
        title: lines('Agent 在等你批准一个操作', [lasted, overdue ? '已经等了很久，请去处理' : '']),
      }
    }
    case 'aborted':
      return {
        key: 'aborted',
        text: '已中断',
        detail: fromRun ? '本次运行被中断' : '',
        tone: 'warn',
        pulse: false,
        flag: '',
        flagText: '',
        title: lines(fromRun ? '本次非交互运行被中断了（被取消，或进程被杀）；对话没有结束，可以接着聊' : '这一轮被中断了', [lasted]),
      }
    case 'error':
      return {
        key: 'error',
        text: '出错',
        detail: fromRun ? '本次运行出错' : '',
        tone: 'danger',
        pulse: false,
        flag: '',
        flagText: '',
        title: lines(
          fromRun ? '本次非交互运行以错误结束（输出里的 result 报错，或进程失败、超时、失联）；对话没有结束，可以接着聊' : '这一轮以错误结束',
          [lasted],
        ),
      }
    case 'ended': {
      const detail = endReasonText(s.end_reason)
      return {
        key: 'ended',
        text: '已结束',
        detail,
        tone: 'muted',
        pulse: false,
        flag: '',
        flagText: '',
        title: lines(`会话已结束${detail ? `（${detail}）` : ''}`),
      }
    }
    case 'offline': {
      const last = validTime(s.last_event_at)
      const quiet = last === null ? s.quiet_seconds * 1000 : Math.max(nowMs - last, s.quiet_seconds * 1000)
      const detail = `${spanText(quiet)}没有信号`
      return {
        key: 'offline',
        text: '离线',
        detail,
        tone: 'muted',
        pulse: false,
        flag: '',
        flagText: '',
        title: lines(`离线：${detail}`, ['Agent 可能已经关掉；它再调用 RelayMesh 或 hook 汇报时会自动恢复。']),
      }
    }
    default:
      return { ...UNKNOWN_VIEW, text: String(s.state) }
  }
}

// ---------------------------------------------------------------------------
// 颜色
// ---------------------------------------------------------------------------

/** 小圆点的 Tailwind 类（圆点本身，不含尺寸）。 */
export function dotToneClass(tone: AgentTone, pulse: boolean): string {
  const base = (() => {
    switch (tone) {
      case 'active':
        return 'bg-emerald-500'
      case 'calm':
        return 'bg-sky-500'
      case 'attention':
        return 'bg-amber-500'
      case 'warn':
        return 'bg-amber-500'
      case 'danger':
        return 'bg-destructive'
      default:
        return 'bg-muted-foreground/50'
    }
  })()
  return pulse ? `${base} motion-safe:animate-pulse` : base
}

/** 状态文字的 Tailwind 类。 */
export function textToneClass(tone: AgentTone): string {
  switch (tone) {
    case 'active':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'calm':
      return 'text-sky-600 dark:text-sky-400'
    case 'attention':
    case 'warn':
      return 'text-amber-600 dark:text-amber-400'
    case 'danger':
      return 'text-destructive'
    default:
      return 'text-muted-foreground'
  }
}

/**
 * 侧栏会话卡上会不会画「Agent 状态小圆点」（#113）：收到过这个工作流的 hook 汇报、且状态已知。
 * 小圆点组件与侧栏的「工作中」芯片共用这一条判断——有小圆点时芯片就不再自带绿点，免得两个圆点并排（追加 #267）。
 */
export function agentDotShown(primary: AgentSession | null | undefined, nowMs: number = Date.now()): boolean {
  return !!primary && stateView(primary, nowMs).key !== 'unknown'
}

// ---------------------------------------------------------------------------
// 一个工作流里展示哪个会话
// ---------------------------------------------------------------------------

export function groupByWorkflow(sessions: ReadonlyArray<AgentSession>): Map<string, AgentSession[]> {
  const out = new Map<string, AgentSession[]>()
  for (const s of sessions) {
    if (!s.workflow_id) continue
    const g = out.get(s.workflow_id)
    if (g) g.push(s)
    else out.set(s.workflow_id, [s])
  }
  for (const g of out.values()) g.sort((a, b) => (validTime(b.last_event_at) ?? 0) - (validTime(a.last_event_at) ?? 0))
  return out
}

export interface PickedSession {
  primary: AgentSession | null
  /** 同一工作流里除 primary 之外，还有几个正在活动（未结束、未离线）的会话 */
  others: number
}

const isLive = (s: AgentSession) => s.state !== 'ended' && s.state !== 'offline' && s.state !== ''

/** 展示最近有动静的活动会话；都不活动就展示未结束里最近的，再不行展示最近一个。list 已按最近动静排序。 */
export function pickSession(list: ReadonlyArray<AgentSession> | undefined): PickedSession {
  if (!list || list.length === 0) return { primary: null, others: 0 }
  const live = list.filter(isLive)
  if (live.length > 0) return { primary: live[0], others: live.length - 1 }
  const open = list.find(s => s.state !== 'ended' && s.state !== '')
  return { primary: open ?? list[0], others: 0 }
}

// ---------------------------------------------------------------------------
// SSE 事件：先就地改缓存（立刻可见），再由 store 慢慢重新取（补上卡住标志、最近工具等推导字段）
// ---------------------------------------------------------------------------

const STATES_WAITING_YOU_OVER = new Set(['working', 'idle', 'waiting_approval'])

export function applyStateEvent(
  list: ReadonlyArray<AgentSession>,
  ev: AgentStateEvent,
  nowIso: string,
): { list: AgentSession[]; found: boolean } {
  const i = list.findIndex(s => s.id === ev.session_id)
  if (i < 0) return { list: [...list], found: false }
  const cur = list[i]
  const base = ev.state as AgentStateName
  // 「等你回答」叠加在 working / idle / waiting_approval 之上：事件不会让它消失（人还没答）；会话结束则一并结束
  const keepWaiting = cur.waiting_you && STATES_WAITING_YOU_OVER.has(base)
  const next: AgentSession = {
    ...cur,
    base_state: base,
    state: keepWaiting ? 'waiting_you' : base,
    waiting_you: keepWaiting,
    sub: ev.sub || undefined,
    state_source: ev.source || cur.state_source,
    since: ev.since || nowIso,
    last_event_at: nowIso,
    quiet_seconds: 0,
    // 刚来了新事件：之前的卡住 / 疑似标志作废，等重新取到的结果再决定要不要重新标上
    stalled: false,
    stalled_soft: false,
    approval_suspected: false,
    approval_overdue: false,
    reason: undefined,
  }
  const out = [...list]
  out[i] = next
  return { list: out, found: true }
}

// ---------------------------------------------------------------------------
// 基础状态四项（S-1）：只用 Hub 已有的记录，不需要任何新信号
// ---------------------------------------------------------------------------

export interface BasicFacts {
  /** 最后交互：最近一次任何 hook 事件或 MCP 调用 */
  lastInteractionAt: string | null
  /** 最后查询：最近一次「读类」调用（收件箱、会话列表、文档读取……） */
  lastQueryAt: string | null
  /** 推送已收到：追加指令的提醒被带到 Agent 面前的最近时间（hook 提醒或回执提示行） */
  pushReceivedAt: string | null
  /** 已读：Agent 取走追加指令全文的最近时间 */
  readAt: string | null
  /** 还在排队、Agent 没读到的追加条数 */
  queued: number
}

export function basicFacts(input: {
  session: AgentSession | null | undefined
  identity: AgentIdentity | null | undefined
  items: ReadonlyArray<QueuedFeedback> | null | undefined
}): BasicFacts {
  const { session, identity } = input
  const items = input.items ?? []
  const push = latestIso(...items.flatMap(it => [it.last_notified_at, it.receipt_hint_at]))
  const read = latestIso(...items.map(it => it.delivered_at))
  const queued = items.filter(it => stateOf(it) === 'queued').length
  return {
    lastInteractionAt: latestIso(session?.last_event_at, session?.last_mcp_at, identity?.last_hook_at, identity?.last_mcp_at),
    lastQueryAt: latestIso(identity?.last_query_at, session?.last_query_at),
    pushReceivedAt: push,
    readAt: read,
    queued,
  }
}

/** 一项事实的文字：「3 分钟前」或「—」（从未发生）。 */
export function factText(iso: string | null | undefined, nowMs: number = Date.now()): string {
  if (!iso) return '—'
  return ago(iso, nowMs) || '—'
}

// ---------------------------------------------------------------------------
// 身份卡里的补充信息
// ---------------------------------------------------------------------------

/** 客户端：Cursor IDE / Cursor CLI · 版本；不确定时只给「未知」。 */
export function clientText(kind: string | undefined, version: string | undefined): string {
  const k = (kind ?? '').trim().toLowerCase()
  const v = (version ?? '').trim()
  const base = k === 'cli' ? 'Cursor CLI' : k === 'ide' ? 'Cursor IDE' : ''
  if (!base) return v ? `客户端未知 · ${v}` : '客户端未知'
  return v ? `${base} · ${v}` : base
}

/** 本轮用时：进行中从开始算到现在；已结束的是上一轮用时。 */
export function turnSpanText(s: AgentSession | null | undefined, nowMs: number = Date.now()): string {
  if (!s) return ''
  const start = validTime(s.turn_started_at)
  if (start === null) return ''
  const end = validTime(s.turn_ended_at)
  if (s.state === 'working' || s.state === 'waiting_you' || s.state === 'waiting_approval') {
    if (end !== null && end >= start) return ''
    return spanText(nowMs - start)
  }
  if (end !== null && end >= start) return `${spanText(end - start)}（上一轮）`
  return ''
}

/** 会话运行时长：开始到结束（或到现在）。 */
export function sessionSpanText(s: AgentSession | null | undefined, nowMs: number = Date.now()): string {
  if (!s) return ''
  const start = validTime(s.started_at)
  if (start === null) return ''
  const end = s.state === 'ended' ? validTime(s.ended_at) : null
  return spanText((end ?? nowMs) - start)
}

/**
 * 页头徽标上的「会话运行时长」（#535 Q63 选 c，#582 更正为「仅对 hook 启用时生效」）：
 * 每个会话各自独立判断（#601）——这个会话的状态来源是编辑器 hook（`state_source === 'hook'`，即这个会话启用了 hook）时，
 * 徽标不带时长（时间都在点开的身份卡里：agent-card-turn / agent-card-session-span）；
 * 其他来源（终端运行输出 `run`、终端画面 `tui`、旧数据没有来源）的会话维持原来的规则——有就显示、没有就不占位。
 * 注意不能用收件箱设置里的「hook 提醒」开关：它是整个 Hub 一份，不是每个会话各自的（Q65 先选了它，#601 指出与预期不符）。
 */
export function chipSpanText(s: AgentSession | null | undefined, nowMs: number = Date.now()): string {
  if (!s) return ''
  if ((s.state_source ?? '').trim() === 'hook') return ''
  return sessionSpanText(s, nowMs)
}

/** 会话编号只显示前 8 位，完整值放在悬停里。 */
export function shortConversationId(id: string | undefined): string {
  const s = (id ?? '').trim()
  if (s.length <= 8) return s
  return s.slice(0, 8)
}

// ---------------------------------------------------------------------------
// 托管终端的屏幕信号（M3.2）
// ---------------------------------------------------------------------------

/** token 数的短写法：999 → 999，1234 → 1.2k，12345 → 12k，1_500_000 → 1.5M。 */
export function tokensText(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  if (n < 1000) return String(Math.round(n))
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
}

/** 上下文占用的写法：4.4 → 4.4%，12 → 12%。 */
export function ctxText(pct: number): string {
  if (!Number.isFinite(pct) || pct < 0 || pct > 100) return ''
  return `${(Math.round(pct * 10) / 10).toString()}%`
}

export interface ScreenFacts {
  /** '' = 没有托管终端的屏幕信号 */
  tui: '' | 'ok' | 'unrecognized'
  /** 「1.2k（屏幕）」；没读到为空 */
  tokens: string
  /** 「12%」；没读到为空 */
  ctx: string
  /** 状态行动词；画面不动时为空 */
  word: string
  /** 给人看的一句说明（未识别时解释为什么没有数值） */
  note: string
}

export function screenFacts(s: AgentSession | null | undefined): ScreenFacts {
  const none: ScreenFacts = { tui: '', tokens: '', ctx: '', word: '', note: '' }
  if (!s || !s.tui) return none
  if (s.tui === 'unrecognized') {
    return { ...none, tui: 'unrecognized', note: '终端画面的版本没有验证过，已退回 hook 判断（不会产生屏幕相关的告警）。' }
  }
  const tk = typeof s.tokens_live === 'number' ? tokensText(s.tokens_live) : ''
  return {
    tui: 'ok',
    tokens: tk ? `${tk}（${s.tokens_source === 'tui' ? '屏幕' : '估算'}）` : '',
    ctx: typeof s.ctx_pct === 'number' ? ctxText(s.ctx_pct) : '',
    word: (s.live_word ?? '').trim(),
    note: '',
  }
}

// ---------------------------------------------------------------------------
// 唤醒动作（#113 M3.3，设计稿 04 §4）
// ---------------------------------------------------------------------------

/**
 * 状态卡上「唤醒」按钮的可用性。可唤醒的状态：卡住、被中断、出错、离线、以受阻结束，
 * 以及有未处理消息的空闲 / 已完成；其余（干活中、在等你、在等批准、未接入）不出现按钮——那时不需要唤醒。
 * 离线也放行是对设计稿的小补充：智能体悄悄退出时它正是离线，而这恰恰是最需要唤醒的时候；
 * 能不能真的唤醒仍由服务端按预设与主机状态裁决（wake_mode / wake_note）。
 */
export interface WakeOffer {
  show: boolean
  disabled: boolean
  /** 不可点时的原因，或可点时的一句补充 */
  note: string
}

export function wakeOffer(key: AgentStateKey, endReason: string | undefined, unhandled: number): WakeOffer {
  const yes = (note = ''): WakeOffer => ({ show: true, disabled: false, note })
  const no = (note: string): WakeOffer => ({ show: true, disabled: true, note })
  switch (key) {
    case 'stalled':
      return yes('长时间没有动静')
    case 'aborted':
      return yes('这一轮被中断了')
    case 'error':
      return yes('这一轮以错误结束')
    case 'offline':
      return yes('已经没有信号')
    case 'idle':
      return unhandled > 0 ? yes(`有 ${unhandled} 条消息还没被处理`) : no('空闲，而且没有未处理的消息')
    case 'ended': {
      const r = (endReason ?? '').trim()
      if (r === 'blocked') return yes('上次因受阻而结束')
      if (unhandled > 0) return yes(`有 ${unhandled} 条消息还没被处理`)
      return no(r === 'completed' ? '已经收尾，而且没有未处理的消息' : '已结束，而且没有未处理的消息')
    }
    default:
      return { show: false, disabled: true, note: '' }
  }
}
