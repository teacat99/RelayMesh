// 浏览器通知总线的纯逻辑（#115 M1.4）：服务端的统一通知闸经 SSE 事件 notify 把「值得打扰你」的消息推到网页，
// 这里决定「在这台设备上怎么呈现」——要不要被前台抑制、谁来响铃与弹系统通知、文案怎么写、同一会话怎么合并、标题角标怎么算。
// 服务端决定「该不该通知」（internal/notify.Decide），浏览器只决定「怎么呈现」，两边职责不重叠。
//
// 纯函数、不碰 DOM 与 Vue，便于单元测试；浏览器接口（Notification / Web Locks / BroadcastChannel）由
// stores/notifyBus.ts 与 lib/notifyCoord.ts 接上。

/** 服务端 SSE notify 事件的载荷（internal/notify/gate.go 的 Payload）。 */
export interface NotifyPayload {
  /** 事件唯一标识：同一个 id 只呈现一次（页面里有两条路径可能先后送来同一条）。 */
  id: string
  /** 种类（有效种类，@ 已提升为 mention）：blocking_feedback / mention / approval_waiting / guard_alert / agent_text / ask_card …。 */
  kind: string
  /** 会话键：wf:<工作流> / grp:<群>；用于前台抑制与同会话合并。 */
  conversation: string
  workflow_id: string
  session_id: string
  sender: string
  /** 会话名（项目名）。 */
  title: string
  /** 预览正文（服务端已折成一行并截断）。 */
  preview: string
  /** 站内深链（以 / 开头）。 */
  deep_link: string
  mention: boolean
  created_at: string
  /** 穿透类：不做前台抑制。 */
  penetrate: boolean
  /** 浏览器通道是否要响提示音。 */
  sound: boolean
  /** 是否计入标题角标的未读数。 */
  badge: boolean
}

const MAX_ID_LENGTH = 200

/** 站内深链：只接受以单个 / 开头、不含空白与控制字符的路径，其余一律当作没有（防止被诱导跳到站外）。 */
export function safeDeepLink(link: unknown): string {
  if (typeof link !== 'string') return ''
  const s = link.trim()
  if (!s.startsWith('/') || s.startsWith('//') || s.includes('\\') || s.length > 512) return ''
  for (const ch of s) {
    const code = ch.codePointAt(0) ?? 0
    if (code <= 0x20 || code === 0x7f) return ''
  }
  return s
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

/** 把 SSE 载荷整理成 NotifyPayload；缺少 id / 种类的当作无效返回 null。绝不抛错。 */
export function parseNotifyPayload(raw: unknown): NotifyPayload | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const id = str(r.id)
  const kind = str(r.kind)
  if (!id || id.length > MAX_ID_LENGTH || !kind) return null
  return {
    id,
    kind,
    conversation: str(r.conversation),
    workflow_id: str(r.workflow_id),
    session_id: str(r.session_id),
    sender: str(r.sender),
    title: str(r.title),
    preview: str(r.preview),
    deep_link: safeDeepLink(r.deep_link),
    mention: r.mention === true,
    created_at: str(r.created_at),
    penetrate: r.penetrate === true,
    sound: r.sound === true,
    badge: r.badge === true,
  }
}

// ———————————————— 文案 ————————————————

export interface NoticeText {
  title: string
  body: string
}

/** 升级前「新反馈请求」通知的正文兜底（请求没有标题时）。 */
export const DEFAULT_REQUEST_BODY = '收到新的 AI 反馈请求，请及时查看。'

function joinTitle(head: string, tail: string): string {
  return [head.trim(), tail.trim()].filter(Boolean).join(' · ') || 'RelayMesh'
}

function sameFold(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase()
}

/**
 * 通知的标题与正文。count > 1 表示这一条代表同一会话里的多条消息（窗口内合并）：正文写「N 条新消息」并附最新一条。
 * 新反馈请求沿用升级前的文案，保证老用户看到的提醒不变。
 */
export function presentNotice(p: NotifyPayload, count = 1): NoticeText {
  const preview = p.preview.trim()
  switch (p.kind) {
    case 'blocking_feedback':
      return { title: '新反馈请求', body: preview || DEFAULT_REQUEST_BODY }
    case 'guard_alert':
      return { title: p.title ? `终端守护 · ${p.title}` : '终端守护', body: preview || '终端会话出现异常' }
    case 'mention':
      return { title: joinTitle(p.title, '@你'), body: preview || '有人 @ 了你' }
    case 'approval_waiting':
      return { title: joinTitle(p.title, '待批准'), body: preview || '终端在等你批准' }
    case 'ask_card':
      return { title: joinTitle(p.title, '待确认'), body: preview || '有新的待确认提问' }
    case 'agent_text': {
      const sender = p.sender && !sameFold(p.sender, p.title) ? p.sender : ''
      const title = joinTitle(p.title, sender)
      if (count > 1) return { title, body: preview ? `${count} 条新消息\n最新：${preview}` : `${count} 条新消息` }
      return { title, body: preview || '有新消息' }
    }
    default:
      return { title: p.title || 'RelayMesh', body: preview || '有新通知' }
  }
}

/** 页内提示里「查看」按钮的符号（与跨项目提醒同一套符号化按钮）。 */
export const TOAST_ACTION = '↗'

// ———————————————— 同会话合并 ————————————————

/** 同一会话的系统通知，距上一条不超过这么久就合并成「N 条新消息」。 */
export const AGGREGATE_WINDOW_MS = 60_000

/**
 * 系统通知的标签：相同标签的通知在系统里互相替换。
 * 穿透类（阻塞待答、@、审批、守护）每条独立，绝不被「N 条新消息」淹没；其余按会话合并。
 */
export function notificationTag(p: NotifyPayload): string {
  if (p.penetrate || !p.conversation) return `evt:${p.id}`
  return `conv:${p.conversation}`
}

export interface Aggregator {
  /** 登记一条并返回这个标签在窗口内的累计条数（含这一条）。 */
  bump(tag: string, now: number): number
  /** 清掉某个标签（不传则全部）：人已经看过了，下一条从 1 开始。 */
  reset(tag?: string): void
  size(): number
}

const AGGREGATE_CAP = 200

export function createAggregator(windowMs = AGGREGATE_WINDOW_MS): Aggregator {
  const entries = new Map<string, { count: number; at: number }>()
  return {
    bump(tag, now) {
      for (const [key, e] of entries) {
        if (now - e.at > windowMs) entries.delete(key)
      }
      const prev = entries.get(tag)
      const count = prev ? prev.count + 1 : 1
      entries.delete(tag) // 重新插入，让 Map 的顺序等于最近使用顺序
      entries.set(tag, { count, at: now })
      while (entries.size > AGGREGATE_CAP) {
        const oldest = entries.keys().next().value
        if (oldest === undefined) break
        entries.delete(oldest)
      }
      return count
    },
    reset(tag) {
      if (tag === undefined) entries.clear()
      else entries.delete(tag)
    },
    size: () => entries.size,
  }
}

// ———————————————— 幂等 ————————————————

export interface SeenIds {
  /** 第一次见到这个 id 返回 true 并记下；窗口内再见到返回 false。 */
  claim(id: string, now: number): boolean
  size(): number
}

export const SEEN_TTL_MS = 10 * 60_000
const SEEN_CAP = 1024

export function createSeenIds(ttlMs = SEEN_TTL_MS, cap = SEEN_CAP): SeenIds {
  const seen = new Map<string, number>()
  return {
    claim(id, now) {
      const prev = seen.get(id)
      if (prev !== undefined && now - prev <= ttlMs) return false
      if (seen.size >= cap) {
        for (const [key, at] of seen) {
          if (now - at > ttlMs) seen.delete(key)
        }
        while (seen.size >= cap) {
          const oldest = seen.keys().next().value
          if (oldest === undefined) break
          seen.delete(oldest)
        }
      }
      seen.delete(id)
      seen.set(id, now)
      return true
    },
    size: () => seen.size,
  }
}

// ———————————————— 标题角标 ————————————————

const BADGE_PATTERN = /^\((?:\d+\+?)\)\s+/

/** 去掉标题开头的未读角标「(N) 」。 */
export function stripBadge(title: string): string {
  return title.replace(BADGE_PATTERN, '')
}

/** 给基础标题加上未读角标；0 条时还原。超过 99 条显示 99+。 */
export function badgedTitle(base: string, unread: number): string {
  const clean = stripBadge(base)
  if (unread <= 0) return clean
  return `(${unread > 99 ? '99+' : unread}) ${clean}`
}

// ———————————————— 前台抑制与呈现计划 ————————————————

/** 一个标签页的前台状态（跨标签通过 BroadcastChannel 汇总）。 */
export interface TabState {
  tabId: string
  /** 窗口是否有输入焦点。 */
  focused: boolean
  /** 页面是否可见。 */
  visible: boolean
  /** 这个标签页此刻正在看的会话键；不在会话界面时为空。 */
  conversation: string
  /** 这份状态的更新时间（毫秒）。 */
  at: number
}

/** 超过这么久没有更新的状态视为过期（对应的标签页可能已关闭或被冻结）。 */
export const TAB_STATE_TTL_MS = 10_000

/** 是否有某个标签页正聚焦着并且正在看这个会话。 */
export function isForeground(states: readonly TabState[], conversation: string, now: number): boolean {
  if (!conversation) return false
  return states.some(s => s.focused && s.visible && s.conversation === conversation && now - s.at <= TAB_STATE_TTL_MS)
}

/** 前台抑制：人正在看这个会话时，非穿透类消息不再重复打扰；穿透类永远不抑制。 */
export function shouldSuppress(p: NotifyPayload, states: readonly TabState[], now: number): boolean {
  return !p.penetrate && isForeground(states, p.conversation, now)
}

export interface PlanInput {
  payload: NotifyPayload
  /** 本标签页是不是「负责响铃与弹系统通知」的那一个（多标签选主的结果）。 */
  isLeader: boolean
  /** 本标签页此刻是否聚焦。 */
  selfFocused: boolean
  /** 所有已知标签页（含自己）的前台状态。 */
  states: readonly TabState[]
  now: number
  /** 本机的提示音开关。 */
  soundOn: boolean
  /** 系统通知是否可用：本机开关已开、浏览器支持、权限已授予。 */
  desktopReady: boolean
}

export interface PresentPlan {
  /** 被前台抑制：什么都不做。 */
  suppressed: boolean
  /** 本标签页的标题角标 +1。 */
  badge: boolean
  /** 响提示音（只有 leader）。 */
  sound: boolean
  /** 弹系统通知（只有 leader）。 */
  system: boolean
  /** 在本标签页内弹一条页内提示（系统通知不可用时的回退，且只在聚焦的标签页）。 */
  toast: boolean
  /** 同时作为「新反馈请求」事件发出，驱动人在 ExecMesh 时的跨项目提醒。 */
  request: boolean
}

/** 一条通知在本标签页上该做什么。 */
export function planPresentation(i: PlanInput): PresentPlan {
  if (shouldSuppress(i.payload, i.states, i.now)) {
    return { suppressed: true, badge: false, sound: false, system: false, toast: false, request: false }
  }
  const isRequest = i.payload.kind === 'blocking_feedback'
  return {
    suppressed: false,
    badge: i.payload.badge && !i.selfFocused,
    sound: i.isLeader && i.payload.sound && i.soundOn,
    system: i.isLeader && i.desktopReady,
    // 新反馈请求在 RelayMesh 界面里本来就看得见（侧栏、会话列表），沿用升级前的做法：不额外弹页内提示
    toast: i.selfFocused && !i.desktopReady && !isRequest,
    request: isRequest,
  }
}

/** 非 leader 标签页等 leader 声明「已呈现」的宽限；超时说明 leader 不在（冻结 / 崩溃），由本标签页接手。 */
export const TAKEOVER_GRACE_MS = 1200

// ———————————————— 会话与深链 ————————————————

/** 本标签页此刻「正在看」的会话键：在会话界面（home 路由）选中了某个工作流是 wf:<id>；打开了某个群（group 路由）是 grp:<id>。 */
export function viewedConversation(routeName: unknown, workflowId: string | undefined | null, groupId?: unknown): string {
  if (routeName === 'group') {
    const g = (Array.isArray(groupId) ? groupId[0] : groupId)
    return typeof g === 'string' && g.trim().startsWith('grp:') ? g.trim() : ''
  }
  if (routeName !== 'home') return ''
  const id = (workflowId ?? '').trim()
  return id ? `wf:${id}` : ''
}

/**
 * 从地址的查询参数里取出深链目标（wf=工作流、s=会话、q=要点名的确认项编号，如 Q23）；值可能是数组（重复参数），取第一个。
 * q 只认「Q + 不带前导 0 的整数」的写法，其余当作没有。
 */
export function deepLinkTarget(query: Record<string, unknown> | null | undefined): { wf: string; sid: string; q: string } {
  const first = (v: unknown): string => {
    const x = Array.isArray(v) ? v[0] : v
    return typeof x === 'string' ? x.trim() : ''
  }
  const q = first(query?.q)
  return { wf: first(query?.wf), sid: first(query?.s), q: /^Q[1-9]\d{0,5}$/.test(q) ? q : '' }
}

/**
 * 地址上有没有还没处理完的深链：home 路由且带了 wf 或 s。处理它的是 useNotifyRuntime.openDeepLink——解析目标、选中、再把参数去掉。
 * 在它完成之前，「默认选中的会话」只是占位、马上会被目标取代，会话页据此不为占位会话取整表 / 任务 / 确认项（#169）。
 */
export function isDeepLinkPending(routeName: unknown, query: Record<string, unknown> | null | undefined): boolean {
  if (routeName !== 'home') return false
  const { wf, sid } = deepLinkTarget(query)
  return !!(wf || sid)
}

export interface PickableSession {
  session_id: string
  workflow_id?: string
  created_at?: string
  updated_at?: string
}

function stamp(s: PickableSession): number {
  const t = Date.parse(s.updated_at || s.created_at || '')
  return Number.isFinite(t) ? t : 0
}

/** 深链要打开哪一条：优先精确的会话编号，其次是该工作流里最新的一条。 */
export function pickDeepLinkSession<T extends PickableSession>(list: readonly T[], wf: string, sid: string): T | null {
  if (sid) {
    const hit = list.find(s => s.session_id === sid)
    if (hit) return hit
  }
  if (!wf) return null
  let best: T | null = null
  for (const s of list) {
    if ((s.workflow_id || s.session_id) !== wf) continue
    if (!best || stamp(s) >= stamp(best)) best = s
  }
  return best
}

export interface DeepLinkLoaders<T extends PickableSession> {
  /** 此刻已有的简略会话列表（每次调用都取最新值）。 */
  list: () => readonly T[]
  /** 刷新简略列表；应加入在途的取数而不是另发一次（页面挂载时侧栏 / 布局 / 会话页已经在拉了）。 */
  refreshList: () => Promise<unknown>
  /** 取某个工作流的完整多轮列表；必须走 store 的单飞与缓存，不能绕过去直接请求（#177）。 */
  loadWorkflow: (wf: string) => Promise<readonly T[]>
}

/**
 * 解析深链目标，由便宜到贵逐级兜底：已有的简略列表 → 刷新后的简略列表 → （点名了工作流才）该工作流的完整列表。
 * 越往后越贵（完整列表在长工作流上解压后可达 1 MB 以上），所以前一级找到就不往下走，且每一级都只发生一次。
 */
export async function resolveDeepLinkTarget<T extends PickableSession>(loaders: DeepLinkLoaders<T>, wf: string, sid: string): Promise<T | null> {
  let target = pickDeepLinkSession(loaders.list(), wf, sid)
  if (target) return target
  await loaders.refreshList()
  target = pickDeepLinkSession(loaders.list(), wf, sid)
  if (target || !wf) return target
  return pickDeepLinkSession(await loaders.loadWorkflow(wf), wf, sid)
}
