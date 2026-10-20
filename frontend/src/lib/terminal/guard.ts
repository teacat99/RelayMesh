import type {
  GuardItem,
  GuardState,
  GuardWakePlan,
  GuardWakeView,
  HeadlessPosture,
  LauncherGuard,
  TerminalPrefsLimits,
} from '../../api/terminal'
import { charCount, hasControlChar, shQuote, shellKindOf, startLine, type PlatformArg } from './launcher'
import { classifyLauncherExec } from './launcherExec'

// 会话守护（D-214）里不碰界面的那一部分：表单草稿与校验（与服务端 internal/store/terminal_guard.go 一致）、
// 状态怎么显示、什么时候该先问一句再唤醒、以及「重新拉起」时要输入的那一行。
//
// 守护从不自己动手：服务端只判断状态、推送通知、生成唤醒计划；真正把一句话输入到会话里的是浏览器，
// 并且要在人点了「唤醒」（刚刚解锁）之后——和你自己在键盘上敲一样。

// ---------- 取值范围 ----------

export interface GuardLimits {
  /** 停摆判定时长（分钟）的范围与默认值。 */
  silenceMin: number
  silenceMax: number
  silenceDefault: number
  /** 工作流编号最长多少字符。 */
  workflowMax: number
  /** 唤醒提示词最长多少字符，以及不允许出现的字符（在 shell 里有特殊含义）。 */
  promptMax: number
  promptUnsafe: string
  /** 没写自定义提示词时服务端用的默认正文（占位符原样保留）；旧版 Hub 不给，是空串。 */
  promptDefault: string
  /** 一天内唤醒次数的参考上限。 */
  wakeMax: number
  /** 重新拉起命令最长多少字符（与启动命令同一上限）。 */
  resumeMax: number
}

/** 旧版 Hub 不返回这些范围时用这一组；与服务端的默认值一致。 */
export const GUARD_DEFAULT_LIMITS: GuardLimits = {
  silenceMin: 5,
  silenceMax: 720,
  silenceDefault: 30,
  workflowMax: 128,
  promptMax: 300,
  promptUnsafe: '`$\\;&|<>(){}[]\'"!*?~#',
  promptDefault: '',
  wakeMax: 3,
  resumeMax: 1000,
}

const positive = (v: number | undefined, fallback: number) => (typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : fallback)

export function guardLimits(l?: Partial<TerminalPrefsLimits> | null, commandMax?: number): GuardLimits {
  const d = GUARD_DEFAULT_LIMITS
  return {
    silenceMin: positive(l?.guard_silence_min, d.silenceMin),
    silenceMax: positive(l?.guard_silence_max, d.silenceMax),
    silenceDefault: positive(l?.guard_silence_default, d.silenceDefault),
    workflowMax: positive(l?.guard_workflow_max, d.workflowMax),
    promptMax: positive(l?.guard_prompt_max, d.promptMax),
    promptUnsafe: typeof l?.guard_prompt_unsafe === 'string' && l.guard_prompt_unsafe ? l.guard_prompt_unsafe : d.promptUnsafe,
    promptDefault: typeof l?.guard_prompt_default === 'string' ? l.guard_prompt_default : d.promptDefault,
    wakeMax: positive(l?.guard_wake_max, d.wakeMax),
    resumeMax: positive(commandMax, d.resumeMax),
  }
}

/** Hub 是否认识会话守护（旧版 Hub 的限额里没有这几项）：不认识就别在界面里露出守护的入口。 */
export function guardSupported(l?: Partial<TerminalPrefsLimits> | null): boolean {
  return typeof l?.guard_prompt_max === 'number' && l.guard_prompt_max > 0
}

// ---------- 表单草稿 ----------

/** 守护配置在表单里的形态：数字也先是字符串，方便「没填」与「填了 0」分清。 */
export interface GuardDraft {
  enabled: boolean
  workflow: string
  silence: string
  resume: string
  prompt: string
  exitEndsSession: boolean
  /** 非交互唤醒（默认关闭）与它的权限姿态。 */
  headless: boolean
  posture: HeadlessPosture
}

export function blankGuardDraft(): GuardDraft {
  return { enabled: false, workflow: '', silence: '', resume: '', prompt: '', exitEndsSession: false, headless: false, posture: 'allowlist' }
}

export function guardDraftOf(g?: LauncherGuard | null): GuardDraft {
  if (!g) return blankGuardDraft()
  return {
    enabled: !!g.enabled,
    workflow: g.workflow_id ?? '',
    silence: g.silence_minutes ? String(g.silence_minutes) : '',
    resume: g.resume_command ?? '',
    prompt: g.wake_prompt ?? '',
    exitEndsSession: !!g.exit_ends_session,
    headless: !!g.headless?.enabled,
    posture: g.headless?.posture === 'force' ? 'force' : 'allowlist',
  }
}

/** 提示词是单行文本：各种空白折叠成一个空格（与服务端一致）。 */
export function normalizePrompt(s: string): string {
  return s.trim().split(/\s+/).filter(Boolean).join(' ')
}

const silenceOf = (s: string): number => {
  const t = s.trim()
  return /^\d+$/.test(t) ? Number(t) : NaN
}

/**
 * 草稿 → 要保存的守护配置。什么都没填、也没打开时返回 undefined（不必落盘；服务端同样按「没有守护」处理）。
 * 注意：停摆时长填错了（不是整数）这里按「没填」处理——保存前 validateGuardDraft 会先拦住。
 */
export function toGuard(d: GuardDraft): LauncherGuard | undefined {
  const g: LauncherGuard = { enabled: d.enabled }
  const workflow = d.workflow.trim()
  const resume = d.resume.trim()
  const prompt = normalizePrompt(d.prompt)
  const silence = silenceOf(d.silence)
  if (workflow) g.workflow_id = workflow
  if (Number.isFinite(silence) && silence > 0) g.silence_minutes = silence
  if (resume) g.resume_command = resume
  if (prompt) g.wake_prompt = prompt
  if (d.exitEndsSession) g.exit_ends_session = true
  // 关闭时也保留姿态（下次打开不用重选），但默认姿态不落盘。
  if (d.headless || d.posture === 'force') g.headless = { enabled: d.headless, ...(d.posture === 'force' ? { posture: 'force' as const } : {}) }
  const empty = !g.enabled && !g.workflow_id && !g.silence_minutes && !g.resume_command && !g.wake_prompt && !g.exit_ends_session && !g.headless
  return empty ? undefined : g
}

export type GuardField = 'workflow' | 'silence' | 'resume' | 'prompt' | 'command'
export type GuardErrors = Partial<Record<GuardField, string>>

/** 开启 exit 结束会话时校验启动命令能否 exec 包装（需传入当前命令）。 */
export function validateExitEndsCommand(command: string, exitEnds: boolean): string | undefined {
  if (!exitEnds || !command.trim()) return undefined
  const c = classifyLauncherExec(command)
  return c.ok ? undefined : c.message
}

/** 守护表单的校验：规则与服务端 LauncherGuard.validate 一致，错误信息直接给用户看。 */
export function validateGuardDraft(d: GuardDraft, limits: GuardLimits = GUARD_DEFAULT_LIMITS, startCommand = ''): GuardErrors {
  const err: GuardErrors = {}

  const silence = d.silence.trim()
  if (silence) {
    const n = silenceOf(silence)
    if (!Number.isFinite(n) || n < limits.silenceMin || n > limits.silenceMax) {
      err.silence = `停摆判定时长请填 ${limits.silenceMin}～${limits.silenceMax} 分钟（留空 = 默认 ${limits.silenceDefault} 分钟）`
    }
  }

  const workflow = d.workflow.trim()
  if (workflow) {
    const n = charCount(workflow)
    if (n > limits.workflowMax) err.workflow = `工作流编号最长 ${limits.workflowMax} 个字，现在是 ${n} 个字`
    else if (hasControlChar(workflow) || /\s/.test(workflow)) err.workflow = '工作流编号里不能有空格或控制字符'
  }

  const resume = d.resume.trim()
  if (resume) {
    const n = charCount(resume)
    if (n > limits.resumeMax) err.resume = `重新拉起命令最长 ${limits.resumeMax} 个字，现在是 ${n} 个字`
    else if (hasControlChar(resume)) err.resume = '重新拉起命令只能是一行，不能有换行、制表符或其他控制字符'
  }

  const prompt = normalizePrompt(d.prompt)
  if (prompt) {
    const n = charCount(prompt)
    const bad = Array.from(new Set(Array.from(prompt).filter(c => limits.promptUnsafe.includes(c)))).join(' ')
    if (n > limits.promptMax) err.prompt = `唤醒提示词最长 ${limits.promptMax} 个字，现在是 ${n} 个字`
    else if (hasControlChar(prompt)) err.prompt = '唤醒提示词只能是一行，不能有控制字符'
    else if (bad) {
      err.prompt = `唤醒提示词里不能有 ${bad} 这些字符：Cursor CLI 退出后会话会停在命令行，它们会被当成命令`
    }
  }
  const cmdMsg = validateExitEndsCommand(startCommand, d.exitEndsSession)
  if (cmdMsg) err.command = cmdMsg
  return err
}

export const hasGuardErrors = (e: GuardErrors) => Object.keys(e).length > 0

/** 不算错误、但值得在表单里提醒一句的事（开了监测却缺信息）。 */
export function guardHints(d: GuardDraft, hasStartCommand: boolean): string[] {
  const hints: string[] = []
  if (!d.enabled) return hints
  if (d.exitEndsSession) {
    hints.push('已开启「命令退出即结束会话」：启动时会用 exec 包装命令，CLI 一退出会话就结束，退出码准确。')
  } else if (hasStartCommand) {
    hints.push('未开启「命令退出即结束会话」：CLI 崩溃后外层 shell 可能还在，守护只能靠静默判断停摆（可在下方打开开关）。')
  }
  if (!d.workflow.trim()) {
    hints.push('没填工作流编号：只能发现「会话意外退出」，判断不了「还在、但是停摆了」，也不会提醒你有没读的回复。')
  }
  if (!d.resume.trim() && !hasStartCommand) {
    hints.push('预设没有启动命令，也没填重新拉起命令：会话结束后没法重新拉起，只能在会话还活着时唤醒。')
  }
  return hints
}

/** 重新拉起命令用到了 {chat_id}：只有装了编辑器 hook、汇报过对话编号之后才能接着原来的对话。 */
export const usesChatId = (resume: string) => resume.includes('{chat_id}')

// ---------- 状态的显示 ----------

export type GuardTone = 'ok' | 'info' | 'muted' | 'warn' | 'bad'

export interface GuardStateView {
  label: string
  tone: GuardTone
  /** 在干活：界面可以给一个转动的图标。 */
  busy: boolean
}

const STATE_VIEWS: Record<GuardState, GuardStateView> = {
  working: { label: '在干活', tone: 'ok', busy: true },
  waiting: { label: '在等你回复', tone: 'info', busy: false },
  present: { label: '有人连着', tone: 'muted', busy: false },
  idle: { label: '空闲', tone: 'muted', busy: false },
  ended: { label: '已结束', tone: 'muted', busy: false },
  done: { label: '已完成', tone: 'info', busy: false },
  crashed: { label: '意外结束', tone: 'bad', busy: false },
  stalled: { label: '疑似停摆', tone: 'bad', busy: false },
  offline: { label: '主机离线', tone: 'muted', busy: false },
  unknown: { label: '无法判断', tone: 'muted', busy: false },
}

export function guardStateView(state: GuardState | string | undefined): GuardStateView {
  return STATE_VIEWS[state as GuardState] ?? STATE_VIEWS.unknown
}

/** 小圆点 / 文字的配色类（深浅色主题都可读）。 */
export const GUARD_TONE_TEXT: Record<GuardTone, string> = {
  ok: 'text-emerald-600 dark:text-emerald-400',
  info: 'text-sky-600 dark:text-sky-400',
  muted: 'text-muted-foreground',
  warn: 'text-amber-600 dark:text-amber-400',
  bad: 'text-destructive',
}
export const GUARD_TONE_DOT: Record<GuardTone, string> = {
  ok: 'bg-emerald-500',
  info: 'bg-sky-500',
  muted: 'bg-muted-foreground/50',
  warn: 'bg-amber-500',
  bad: 'bg-destructive',
}

/** 需要你处理的状态：开了监测、并且会话意外结束或停摆。 */
export function guardNeedsAttention(item: Pick<GuardItem, 'enabled' | 'verdict'>): boolean {
  return item.enabled && (item.verdict.state === 'crashed' || item.verdict.state === 'stalled')
}

export interface GuardAttention {
  count: number
  names: string[]
  /** 鼠标悬停的说明（入口按钮上的提示点用）。 */
  title: string
}

export function guardAttention(items: readonly GuardItem[] | null | undefined): GuardAttention {
  const bad = (items ?? []).filter(guardNeedsAttention)
  const names = bad.map(i => i.name)
  const title = bad.length
    ? bad.map(i => `「${i.name}」${guardStateView(i.verdict.state).label}：${i.verdict.reason}`).join('\n')
    : ''
  return { count: bad.length, names, title }
}

// ---------- 唤醒 ----------

export interface WakeAdvice {
  /** 要不要先问一句再唤醒。 */
  confirm: boolean
  /** 为什么要问（逐条展示）。 */
  reasons: string[]
}

/** 连续几次唤醒都没有效果之后，再唤醒大概率还是白费：提醒一句（不拦截）。 */
export const NO_PROGRESS_WARN_STREAK = 2

/**
 * 点「唤醒」之前要不要先问一句：会话看起来没出问题（在干活 / 在等你回复 / 有人连着）、
 * 一天内已经唤醒超过参考上限、或者最近连着两次唤醒都没有效果。其余情况（已经意外结束、停摆）直接做。
 */
export function wakeAdvice(item: GuardItem, wakeMax = GUARD_DEFAULT_LIMITS.wakeMax): WakeAdvice {
  const reasons: string[] = []
  switch (item.verdict.state) {
    case 'working':
      reasons.push('它看起来正在干活——现在唤醒会把一句话插进它正在做的事里。')
      break
    case 'waiting':
      reasons.push('它正在 RelayMesh 里等你回复，不是停摆。')
      break
    case 'present':
      reasons.push('现在有人连着这个会话。')
      break
    case 'idle':
      reasons.push('它现在没有待处理的事。')
      break
    case 'done':
      reasons.push('已标记任务全部完成：按约定不告警；若仍要唤醒请确认。')
      break
  }
  if (item.wakes_24h >= wakeMax) {
    reasons.push(`24 小时内已经唤醒 ${item.wakes_24h} 次（参考上限 ${wakeMax} 次）。`)
  }
  if (item.no_progress_streak >= NO_PROGRESS_WARN_STREAK) {
    reasons.push(
      `最近连续 ${item.no_progress_streak} 次唤醒都没有效果：多半不是它需要被叫醒，而是 Cursor CLI 已经退出、网络不通或额度用完，再唤醒大概率还是白费。` +
        '可以先连上会话看看，或者用「重新拉起」。',
    )
  }
  return { confirm: reasons.length > 0, reasons }
}

/** 非交互唤醒按钮（只在预设开启了它时出现）：不能用时置灰并写明原因。 */
export function headlessButton(item: GuardItem): { show: boolean; disabled: boolean; title: string } {
  const h = item.headless
  if (!h?.enabled) return { show: false, disabled: true, title: '' }
  const posture = h.posture === 'force' ? '放开全部命令（--force）' : '只放行 CLI 白名单内的操作'
  if (!h.ok) return { show: true, disabled: true, title: h.note || '现在没法非交互唤醒' }
  return { show: true, disabled: false, title: `不打开终端：在主机上以「${posture}」的姿态接着原来的对话运行一次 agent -p，结束后在聊天里留一条记录` }
}

export interface WakeButton {
  text: string
  title: string
  disabled: boolean
}

/** 唤醒按钮：会话还活着是「唤醒」（输入提示词），已经结束是「重新拉起」；现在做不了就置灰并写明原因。 */
export function wakeButton(item: GuardItem): WakeButton {
  if (item.wake_mode === 'none') {
    return { text: '唤醒', title: item.wake_note || '现在没法唤醒', disabled: true }
  }
  if (item.wake_mode === 'relaunch') {
    const chat = item.chat_known ? '，接着原来的对话' : '，新开一个对话（还没拿到对话编号）'
    return { text: '重新拉起', title: `会话已经结束：新开一个会话重新拉起智能体并带上提示词${chat}`, disabled: false }
  }
  return { text: '唤醒', title: '把一句提示词输入到存活的会话里（等于你在键盘上敲）', disabled: false }
}

/** 重新拉起时要输入的那一行，以及提示词是不是已经跟着命令一起带上了。 */
export function wakeStartLine(plan: GuardWakePlan, platform?: PlatformArg): { line: string; promptInline: boolean } {
  const command = (plan.command ?? '').trim()
  if (!command) return { line: '', promptInline: false }
  // 提示词用 POSIX 的单引号字面量追加；PowerShell / cmd 里单引号的含义不同（PowerShell 还把弯引号也当单引号）：Windows 主机不追加，
  // 只启动命令，提示词另行发送。
  const inline = !!plan.append_prompt && shellKindOf(platform) === 'posix' && plan.prompt.trim() !== ''
  const full = inline ? `${command} ${shQuote(plan.prompt)}` : command
  return { line: startLine({ dir: plan.dir, command: full }, platform), promptInline: inline }
}

export interface WakeNotice {
  title: string
  description: string
}

/** 唤醒之后给用户看的一句话：做了什么、接下来要不要自己做点什么。 */
export function wakeNotice(plan: GuardWakePlan, promptInline: boolean): WakeNotice {
  const title = `已唤醒「${plan.name}」（24 小时内第 ${plan.n}/${plan.max} 次）`
  const parts: string[] = []
  if (plan.mode === 'headless') {
    parts.push(`已提交非交互运行${plan.run_state === 'awaiting_approval' ? '（等你在 ExecMesh 里批准）' : ''}；结束后聊天里会有一条记录，完整输出在 ExecMesh 的运行记录里。`)
  } else if (plan.mode === 'type') {
    parts.push('提示词已经输入到会话里。过几分钟看看它有没有动静；没有的话，Cursor CLI 可能已经退出了，可以改用「重新拉起」。')
  } else if (promptInline) {
    parts.push(`已重新拉起${plan.resume ? '并接着原来的对话' : '（新对话）'}，提示词已随命令一起带上。`)
  } else {
    parts.push('已重新拉起，但这条命令没法安全地追加提示词，需要你自己把提示词发给它（见下方「复制提示词」）。')
  }
  if (plan.over_budget) parts.push('这次已超过 24 小时参考上限。')
  if (plan.warnings?.length) parts.push(...plan.warnings)
  return { title, description: parts.join(' ') }
}

/** 最近一次唤醒的结果，一个很短的说法（用来放在状态行里）。 */
export function lastWakeText(w: GuardWakeView | undefined, minutes: number): string {
  if (!w) return ''
  const how = w.mode === 'relaunch' ? '重新拉起' : w.mode === 'headless' ? '非交互唤醒' : '唤醒'
  switch (w.progress) {
    case 'progress':
      return `上次${how}：有效果`
    case 'none':
      return `上次${how}：${minutes} 分钟内没有动静`
    default:
      return `上次${how}：观察中`
  }
}
