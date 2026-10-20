// 追加指令收件箱的纯逻辑（D-210）：每条追加的状态说明、智能体活动的描述、开关的文案。
// 与组件分开放，方便单元测试；不依赖 Vue。
import type { InboxConfigView, InboxState, InboxVia, QueuedFeedback } from '../api/types'

export type InboxStage = 'queued' | 'delivered' | 'acked' | 'expired'

export interface InboxStatusView {
  stage: InboxStage
  /** 勾的个数：1 = 已排队，2 = Agent 已读取，3 = Agent 已确认；0 = 已过期（没有被读到）。 */
  ticks: 0 | 1 | 2 | 3
  label: string
  /** 对话气泡标题行右侧的短徽标，与「您的确认回复」气泡的「待 AI 提取 / AI 已接收」同一套说法。 */
  badge: string
  /** 补充说明（经哪条路径、提醒了几次、什么时候）。 */
  detail: string
  tone: 'pending' | 'read' | 'done' | 'muted'
  /** 还能撤回吗：已排队与已读取未确认的可以。 */
  canRetract: boolean
  retractLabel: string
  retractTitle: string
}

/** 服务端会直接给出 state；旧服务端没有这个字段时，按 status 与时间戳推导。 */
export function stateOf(item: Pick<QueuedFeedback, 'state' | 'status' | 'delivered_at'>): InboxState {
  if (item.state) return item.state
  switch (item.status) {
    case 'consumed':
      return 'acked'
    case 'expired':
      return 'expired'
    case 'revoked':
      return item.delivered_at ? 'retracted' : 'revoked'
    default:
      return item.delivered_at ? 'delivered' : 'queued'
  }
}

/** 相对时间：刚刚 / N 秒前 / N 分钟前 / N 小时前 / N 天前；解析不了返回空串。 */
export function ago(iso: string | null | undefined, nowMs: number = Date.now()): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return ''
  const sec = Math.max(0, Math.round((nowMs - t) / 1000))
  if (sec < 5) return '刚刚'
  if (sec < 60) return `${sec} 秒前`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 48) return `${hour} 小时前`
  return `${Math.floor(hour / 24)} 天前`
}

export function viaLabel(via: InboxVia | '' | undefined): string {
  switch (via) {
    case 'hook':
      return '经编辑器 hook 的提醒读取'
    case 'receipt':
      return '经回执末尾的 inbox: 提示行读取'
    case 'tool':
      return '主动调用收件箱读取'
    case 'feedback':
      return '发起交互时随秒回带走'
    default:
      return '已读取'
  }
}

/** 已排队时的提醒情况：hook 提醒了几次、回执提示行有没有出现过。 */
export function reminderNote(item: Pick<QueuedFeedback, 'notified_count' | 'receipt_hint_at'>, nowMs: number = Date.now()): string {
  const parts: string[] = []
  const n = item.notified_count || 0
  if (n > 0) parts.push(`hook 已提醒 ${n} 次`)
  if (item.receipt_hint_at) parts.push(`回执提示行已出现过（${ago(item.receipt_hint_at, nowMs)}）`)
  return parts.join('，')
}

/** 一条追加此刻的状态说明。 */
export function inboxStatus(item: QueuedFeedback, nowMs: number = Date.now()): InboxStatusView {
  const state = stateOf(item)
  if (state === 'acked') {
    const read = item.delivered_via ? viaLabel(item.delivered_via) : '已读取'
    return {
      stage: 'acked',
      ticks: 3,
      label: 'Agent 已确认',
      badge: 'AI 已接收',
      detail: `${read}${item.acked_at ? ` · ${ago(item.acked_at, nowMs)}确认` : ''}`,
      tone: 'done',
      canRetract: false,
      retractLabel: '',
      retractTitle: '',
    }
  }
  if (state === 'expired') {
    return {
      stage: 'expired',
      ticks: 0,
      label: '已过期',
      badge: '已过期',
      detail: 'Agent 在保留期内一直没有读到这条，它不会再被送达',
      tone: 'muted',
      canRetract: false,
      retractLabel: '',
      retractTitle: '',
    }
  }
  if (state === 'delivered') {
    return {
      stage: 'delivered',
      ticks: 2,
      label: 'Agent 已读取',
      badge: 'AI 已读取',
      detail: `${viaLabel(item.delivered_via)}${item.delivered_at ? ` · ${ago(item.delivered_at, nowMs)}` : ''}，等待确认`,
      tone: 'read',
      canRetract: true,
      retractLabel: '撤回并通知 Agent',
      retractTitle: 'Agent 已经读过这条：撤回后内容回到输入框，并在它下次读取收件箱时告诉它忽略这条',
    }
  }
  const note = reminderNote(item, nowMs)
  return {
    stage: 'queued',
    ticks: 1,
    label: '已排队',
    badge: '待 AI 提取',
    detail: note || '等待 Agent 下次调用 RelayMesh',
    tone: 'pending',
    canRetract: true,
    retractLabel: '撤回指令',
    retractTitle: '撤回该追加指令并带回输入框重新编辑',
  }
}

export interface AgentActivityView {
  text: string
  /** active：一分钟内；recent：十分钟内；idle：更久；unknown：本次服务启动以来没有记录。 */
  level: 'active' | 'recent' | 'idle' | 'unknown'
}

/** 「Agent 最近一次与 RelayMesh 交互」。服务端只在内存里记，重启后清空。 */
export function agentActivity(iso: string | null | undefined, nowMs: number = Date.now()): AgentActivityView {
  if (!iso || !Number.isFinite(Date.parse(iso))) {
    return { text: '暂无记录（服务启动以来 Agent 还没有调用过）', level: 'unknown' }
  }
  const sec = Math.max(0, Math.round((nowMs - Date.parse(iso)) / 1000))
  const level: AgentActivityView['level'] = sec < 60 ? 'active' : sec < 600 ? 'recent' : 'idle'
  return { text: ago(iso, nowMs) || '刚刚', level }
}

export interface HookStatusView {
  /**
   * active / recent / idle：hook 来问过（一分钟内 / 十分钟内 / 更久）；
   * none：hook 层开着却从没来过（多半是没装）；muted：来过，但服务端已把 hook 层关了；hidden：不用显示。
   */
  level: 'active' | 'recent' | 'idle' | 'none' | 'muted' | 'hidden'
  text: string
  /** 要不要提供「怎么装」的入口。 */
  showInstall: boolean
}

/** 「编辑器 hook 最近一次来问收件箱」：让用户一眼看出 hook 有没有装好、在不在跑。 */
export function hookStatus(
  iso: string | null | undefined,
  cfg: InboxConfigView | null | undefined,
  nowMs: number = Date.now(),
): HookStatusView {
  if (!cfg) return { level: 'hidden', text: '', showInstall: false }
  const seen = !!iso && Number.isFinite(Date.parse(iso))
  const hooksOn = cfg.effective.enabled && cfg.effective.hooks
  if (!hooksOn) {
    if (!seen) return { level: 'hidden', text: '', showInstall: false }
    return { level: 'muted', text: `编辑器 hook 最近一次查询：${ago(iso, nowMs) || '刚刚'}（hook 提醒已关闭，不会再提醒）`, showInstall: false }
  }
  if (!seen) {
    return { level: 'none', text: '编辑器 hook：还没收到过查询（项目里没装，或装了但 Agent 还没用过工具）', showInstall: true }
  }
  const sec = Math.max(0, Math.round((nowMs - Date.parse(iso as string)) / 1000))
  const level: HookStatusView['level'] = sec < 60 ? 'active' : sec < 600 ? 'recent' : 'idle'
  return { level, text: `编辑器 hook 最近一次查询：${ago(iso, nowMs) || '刚刚'}`, showInstall: false }
}

/** POSIX shell 的单引号转义：值里的单引号写成 '\''。 */
export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

export interface HookInstallCommands {
  /** 推荐：Go 二进制一键安装（Hub 自带 agent-dist 时可用）。 */
  primary: string
  /** Windows PowerShell（折叠展示）。 */
  windows?: string
  /** 无 agent-dist 时从 GitHub Release 安装（占位，由界面拼接 release 页说明）。 */
  github?: string
}

/** 在项目根目录执行的安装命令（Go 二进制 relaymesh-hook；Python 版已移除）。 */
export function hookInstallCommands(origin: string, workflowId: string): HookInstallCommands {
  const base = origin.replace(/\/+$/, '')
  const primary = `curl -fsSL ${shQuote(base + '/hooks/cursor/install.sh')} | RELAYMESH_HOOK_INSTALL_BASE=${shQuote(base)} sh -s -- --workflow ${shQuote(workflowId)}`
  const windows = `$env:RELAYMESH_HOOK_INSTALL_BASE='${base.replace(/'/g, "''")}'; irm '${base}/hooks/cursor/install.ps1' | iex; relaymesh-hook install --workflow '${workflowId.replace(/'/g, "''")}'`
  return {
    primary,
    windows,
    github: `见 GitHub Release 资产 relaymesh-hook-<平台>，下载后执行 relaymesh-hook install --workflow ${shQuote(workflowId)}`,
  }
}

/** 只要一条命令时用：返回 primary（Go 二进制一键安装）。 */
export function hookInstallCommand(origin: string, workflowId: string): string {
  return hookInstallCommands(origin, workflowId).primary
}

export interface InboxLayersView {
  mode: 'on' | 'partial' | 'off' | 'env-off'
  text: string
}

/** 当前生效的提醒方式，一句话。 */
export function layersSummary(cfg: InboxConfigView | null | undefined): InboxLayersView {
  if (!cfg) return { mode: 'on', text: '' }
  if (cfg.env_off) {
    return { mode: 'env-off', text: '服务端环境变量 RELAYMESH_INBOX=off 已强制关闭收件箱提醒：追加只会在 Agent 下次发起交互时带走' }
  }
  const e = cfg.effective
  if (!e.enabled) {
    return { mode: 'off', text: '收件箱提醒已关闭：追加只会在 Agent 下次发起交互时带走' }
  }
  const on: string[] = []
  if (e.receipt_hint) on.push('回执提示行')
  if (e.hooks) on.push('编辑器 hook')
  if (e.prompt_clause) on.push('提示词条款')
  if (on.length === 0) {
    return { mode: 'off', text: '三种提醒方式都已关闭：追加只会在 Agent 下次发起交互时带走（它仍可主动读取收件箱）' }
  }
  return { mode: on.length === 3 ? 'on' : 'partial', text: `提醒方式：${on.join(' · ')}` }
}

/** Agent 工作期间会不会被主动提醒（提示行或 hook 至少开着一个）。 */
export function remindsAgent(cfg: InboxConfigView | null | undefined): boolean {
  return !!cfg && cfg.effective.enabled && (cfg.effective.receipt_hint || cfg.effective.hooks)
}

/** 提交追加之后的提示语。 */
export function appendedToast(cfg: InboxConfigView | null | undefined): string {
  if (remindsAgent(cfg)) {
    return '已追加：Agent 下次调用 RelayMesh 时会收到提示，也会在发起交互时一并带走'
  }
  return '已暂存追加指令，将在 AI 下次发起交互时秒回'
}

/** 输入框「当前无交互」时，提交按钮旁的说明。 */
export function appendDescription(cfg: InboxConfigView | null | undefined): string {
  if (remindsAgent(cfg)) {
    return '当前无交互：指令进入 Agent 的收件箱，它下次调用 RelayMesh 时会收到提示，或在下次发起交互时秒回'
  }
  return '当前无交互，暂存用户指令并在 AI 下次发起交互时秒回'
}

/** 输入框「当前无交互」时的占位文字。 */
export function appendPlaceholder(cfg: InboxConfigView | null | undefined): string {
  if (remindsAgent(cfg)) {
    return '追加补充指令...（当前无交互，发送后进入 Agent 的收件箱：它下次调用 RelayMesh 时会收到提示，或在下次发起交互时直接秒回）'
  }
  return '追加补充指令...（当前无交互，发送后将在 AI 下次发起交互时直接秒回）'
}

/** 撤回之后的提示语。 */
export function retractedToast(delivered: boolean | undefined): string {
  return delivered
    ? '已撤回，内容已还原至输入框；Agent 读取收件箱时会被告知忽略这条'
    : '已撤回暂存指令，内容已还原至输入框'
}

/** 还有没有「需要继续观察」的条目（已确认、已过期的不会再变；没有活跃条目时可以放慢轮询）。 */
export function hasLiveItems(items: ReadonlyArray<QueuedFeedback>): boolean {
  return items.some(i => {
    const s = stateOf(i)
    return s !== 'acked' && s !== 'expired'
  })
}

export interface InboxChipView {
  /** 指示灯：active / recent / idle 为 hook 来问过（一分钟 / 十分钟 / 更久）；none 为从没来过；muted 为已被关闭；off 为提醒整体关闭或不适用。 */
  level: 'active' | 'recent' | 'idle' | 'none' | 'muted' | 'off'
  /** 按钮上的短文字：「hook 刚刚」「hook 未装」「收件箱」「提醒已关」。 */
  text: string
  /** 排队中（还没被 Agent 读到）的条数。 */
  queued: number
  /** 已被读取、等待确认的条数。 */
  delivered: number
  /** 悬停提示：把关键状态说成一句话。 */
  title: string
}

/**
 * 工具栏「收件箱状态块」的摘要。配置还没取到时返回 null（不显示）。
 * 完整信息（Agent 最近活动、提醒方式、hook 状态与安装命令）放在点开后的浮层里，这里只给一眼能看懂的结论。
 */
export function inboxChipView(args: {
  cfg: InboxConfigView | null | undefined
  hookSeenAt: string | null | undefined
  activityAt: string | null | undefined
  items: ReadonlyArray<Pick<QueuedFeedback, 'state' | 'status' | 'delivered_at'>>
  nowMs?: number
}): InboxChipView | null {
  const { cfg, hookSeenAt, activityAt, items } = args
  if (!cfg) return null
  const nowMs = args.nowMs ?? Date.now()
  let queued = 0
  let delivered = 0
  for (const it of items) {
    const s = stateOf(it)
    if (s === 'queued') queued++
    else if (s === 'delivered') delivered++
  }
  const layers = layersSummary(cfg)
  const hook = hookStatus(hookSeenAt, cfg, nowMs)
  const activity = agentActivity(activityAt, nowMs)

  let level: InboxChipView['level']
  let text: string
  if (layers.mode === 'off' || layers.mode === 'env-off') {
    level = 'off'
    text = '提醒已关'
  } else if (hook.level === 'active' || hook.level === 'recent' || hook.level === 'idle') {
    level = hook.level
    text = `hook ${ago(hookSeenAt, nowMs) || '刚刚'}`
  } else if (hook.level === 'none') {
    level = 'none'
    text = 'hook 未装'
  } else {
    // hook 层关了（来过的显示已静音，没来过的不提 hook）：提醒走别的层，状态块只当收件箱入口
    level = hook.level === 'muted' ? 'muted' : 'off'
    text = '收件箱'
  }

  const lines = [
    activity.level === 'unknown' ? 'Agent：暂无交互记录' : `Agent 最近一次与 RelayMesh 交互：${activity.text}`,
    layers.text,
    hook.text,
  ].filter(Boolean)
  if (queued || delivered) lines.push(`排队中 ${queued} 条，已读取待确认 ${delivered} 条`)
  return { level, text, queued, delivered, title: lines.join('\n') }
}

/**
 * 状态块上的文字是不是「会随时间自己变」的那种：hook 来问过时写成「hook 刚刚 / 12 秒前 / 3 分钟前 / 5 小时前」。
 * 这种文字每隔几秒就换一次、宽度跟着变，后面的芯片会被一次次推着走（手机上看起来就是整排在抖，#197）；
 * 所以要给它预留固定宽度。其余几种（提醒已关 / hook 未装 / 收件箱）只在状态变化时才换，不用预留。
 */
export function inboxChipTextIsTimed(level: InboxChipView['level']): boolean {
  return level === 'active' || level === 'recent' || level === 'idle'
}

/**
 * 给「随时间变」的状态块文字预留的最小宽度（单位 em，随字号走）。
 * 最宽的写法是「hook 59 分钟前」「hook 47 小时前」：ASCII 约 0.6em、汉字约 1em，合计约 7.8em，再留一点余量。
 * 模板里写的是字面量类名 `min-w-[8em]`（Tailwind 只认源码里完整出现的类名），由单测保证它与这个常量一致。
 */
export const INBOX_CHIP_TIMED_TEXT_MIN_EM = 8
