import type { HeadlessPosture, HostView, LauncherRun, SessionInfo, TerminalLauncher, TerminalPrefsLimits } from '../../api/terminal'
import { classifyLauncherExec } from './launcherExec'
import { hostTitle, runLimitText } from './format'
import { canHumanTerminal } from './hosts'

// 终端启动器（会话页顶栏的「终端」入口）：一份预设 = 在哪台主机、哪个目录、会话就绪后自动输入什么命令。
// 这里是不碰界面的那一部分：预设草稿的校验（与服务端 internal/store/terminal_launchers.go 一致）、主机是否可用、
// 认出预设已经在运行的会话、要自动输入的那一行命令，以及「什么时候可以输入」的判断。

// ---------- 取值范围 ----------

export interface LauncherLimits {
  /** 预设最多几个。 */
  max: number
  /** 以下三项按字符数。 */
  name: number
  dir: number
  command: number
  /** Hub 认不认识「非交互续聊」预设（#126）：旧版 Hub 没有，界面就不提供这种预设。 */
  runSupported: boolean
  /** 「非交互续聊」的提示词 / 模型名 / 可执行文件的最大长度（按字符数）。 */
  runPrompt: number
  runModel: number
  runBin: number
  /** 提示词留空时服务端用的默认正文（%workflow 原样保留），表单里展示出来方便照着改。 */
  runPromptDefault: string
  /** 「新开对话」运行没写提示词时服务端用的默认正文（%workflow 原样保留）；空 = 这台 Hub 不认识「新开对话」（旧版，#176），表单不提供这个选项、会话页也没有「启用 Agent」。 */
  runNewPromptDefault: string
  /** 预设「最长运行时间」的上限，也是没填时的默认值（分钟，#141）；0 = 这台 Hub 不认识这个选项（旧版），表单不显示它。 */
  runMaxMinutes: number
}

/** 旧版 Hub 不返回 limits 时用这一组；与服务端的默认上限一致。 */
export const LAUNCHER_DEFAULT_LIMITS: LauncherLimits = {
  max: 20, name: 40, dir: 512, command: 1000,
  runSupported: false, runPrompt: 600, runModel: 64, runBin: 128, runPromptDefault: '', runNewPromptDefault: '', runMaxMinutes: 0,
}

/** 工作流编号的最大长度：与服务端 store.MaxGuardWorkflowRunes 一致（守护与续聊预设共用）。 */
export const RUN_WORKFLOW_MAX = 128
/** 对话编号：与服务端 agentConvRe（^[A-Za-z0-9_-]{1,64}$）一致。 */
const CONVERSATION_RE = /^[A-Za-z0-9_-]{1,64}$/
/** 可执行文件：命令名或路径，没有空白、引号、分号、变量展开。 */
const BIN_RE = /^[A-Za-z0-9_./~+-]+$/
/** 模型名（--model 的值）。 */
const MODEL_RE = /^[A-Za-z0-9][A-Za-z0-9._:/+-]*$/
/** 会改变后面命令含义的 shell 内建 / 前缀命令：不能当作「可执行文件」（与服务端 headlessPrefixCmds 一致）。 */
const PREFIX_CMDS = new Set([
  'cd', 'env', 'export', 'exec', 'source', '.', 'eval', 'set', 'sudo', 'doas', 'nohup', 'time', 'command', 'builtin', 'nice',
  'setsid', 'timeout', 'xargs', 'sh', 'bash', 'zsh', 'fish',
])

const positive = (v: number | undefined, fallback: number) => (typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : fallback)

export function launcherLimits(l?: Partial<TerminalPrefsLimits> | null): LauncherLimits {
  const d = LAUNCHER_DEFAULT_LIMITS
  return {
    max: positive(l?.launcher_max, d.max),
    name: positive(l?.launcher_name_max, d.name),
    dir: positive(l?.launcher_dir_max, d.dir),
    command: positive(l?.launcher_command_max, d.command),
    runSupported: positive(l?.launcher_run_prompt_max, 0) > 0,
    runPrompt: positive(l?.launcher_run_prompt_max, d.runPrompt),
    runModel: positive(l?.launcher_run_model_max, d.runModel),
    runBin: positive(l?.launcher_run_bin_max, d.runBin),
    runPromptDefault: typeof l?.launcher_run_prompt_default === 'string' ? l.launcher_run_prompt_default : d.runPromptDefault,
    runNewPromptDefault: typeof l?.launcher_run_new_prompt_default === 'string' ? l.launcher_run_new_prompt_default : d.runNewPromptDefault,
    runMaxMinutes: positive(l?.launcher_run_max_minutes, d.runMaxMinutes),
  }
}

/** 按字符（不是 UTF-16 单元）数长度：和服务端按 rune 数一致，中文与 emoji 都算一个。 */
export function charCount(s: string): number {
  return Array.from(s).length
}

// ---------- 草稿 ----------

/** 「最长运行时间」输入框旁的单位。 */
export type RunLimitUnit = 'minute' | 'hour' | 'day'

export const RUN_LIMIT_UNITS: ReadonlyArray<{ value: RunLimitUnit; label: string; minutes: number }> = [
  { value: 'minute', label: '分钟', minutes: 1 },
  { value: 'hour', label: '小时', minutes: 60 },
  { value: 'day', label: '天', minutes: 24 * 60 },
]

/** 「非交互续聊」预设的运行配置草稿（输入框里的原样文本）。 */
export interface RunDraft {
  bin: string
  model: string
  posture: HeadlessPosture
  workflow: string
  conversation: string
  prompt: string
  /** 最长运行时间（#141）：输入框里的数字（可带小数，如 1.5）与单位；留空 = 不设（用平台上限 7 天）。会话页「运行」不用它，所以可以没有。 */
  maxRun?: string
  maxRunUnit?: RunLimitUnit
  /** 异常退出时自动续跑（#151，默认关闭）。会话页「运行」不用它，所以可以没有。 */
  autoResume?: boolean
  /** 「续跑成功」的判定时长（#172）：输入框里的整数分钟；留空 = 默认 10 分钟。只在开着自动续跑时有意义；会话页「运行」不用它，所以可以没有。 */
  autoResumeOk?: string
  /** 第一次运行时新开对话（#176 第三期）：命令里没有 --resume，之后的运行接着学到的新对话。需要填工作流编号、不能固定对话编号。会话页「运行」不用它，所以可以没有。 */
  newConversation?: boolean
  /** 预设的来源标记（enable_agent = 由「启用 Agent」创建）：表单不让改，只负责在编辑保存时原样带回去（否则编辑一次标记就丢了）。 */
  origin?: string
}

export interface LauncherDraft {
  id: string
  name: string
  host: string
  dir: string
  command: string
  /** 有它就是「非交互续聊」预设（点「运行」由 Hub 运行一次，没有命令、没有守护）；终端预设没有这个字段。 */
  run?: RunDraft
}

export const blankRunDraft = (): RunDraft => ({
  bin: '', model: '', posture: 'allowlist', workflow: '', conversation: '', prompt: '', maxRun: '', maxRunUnit: 'hour', autoResume: false, autoResumeOk: '',
  newConversation: false, origin: '',
})

/** 「续跑成功」判定时长的默认值与上限（分钟，#172）：与服务端 store.DefaultAutoResumeOKMinutes / MaxAutoResumeOKMinutes 一致。 */
export const AUTO_RESUME_OK_DEFAULT_MINUTES = 10
export const AUTO_RESUME_OK_MAX_MINUTES = 24 * 60

/** 判定时长的输入 → 分钟：留空 = null（用默认）；不是纯数字 = NaN；其余是整数（可能是 0 或超限，由校验拒绝）。 */
export function parseAutoResumeOk(value: string | undefined): number | null {
  const text = (value ?? '').trim()
  if (!text) return null
  if (!/^\d+$/.test(text)) return NaN
  return Number(text)
}

/**
 * 最长运行时间的输入（数字 + 单位）→ 分钟。留空 = null（不设）；写得不对 = NaN（不是非负的十进制数，或不是整数分钟，
 * 如 0.5 分钟；1.5 小时 = 90 分钟可以）；其余是换算后的整数分钟（可能是 0，由校验拒绝）。与服务端存的整数分钟一致。
 */
export function parseMaxRun(value: string | undefined, unit: RunLimitUnit | undefined): number | null {
  const text = (value ?? '').trim()
  if (!text) return null
  if (!/^\d+(\.\d+)?$/.test(text)) return NaN
  const per = RUN_LIMIT_UNITS.find(u => u.value === unit)?.minutes ?? 60
  const minutes = Number(text) * per
  const whole = Math.round(minutes)
  return Math.abs(minutes - whole) < 1e-6 ? whole : NaN
}

/** 分钟 → 输入框里的数字与单位：整天用天、整小时用小时，其余用分钟（反过来与 parseMaxRun 互逆）。 */
export function maxRunFields(minutes: number | undefined): { maxRun: string; maxRunUnit: RunLimitUnit } {
  if (typeof minutes !== 'number' || !Number.isFinite(minutes) || minutes <= 0) return { maxRun: '', maxRunUnit: 'hour' }
  if (minutes % (24 * 60) === 0) return { maxRun: String(minutes / (24 * 60)), maxRunUnit: 'day' }
  if (minutes % 60 === 0) return { maxRun: String(minutes / 60), maxRunUnit: 'hour' }
  return { maxRun: String(minutes), maxRunUnit: 'minute' }
}

/** 新预设的编号：与服务端 ^[A-Za-z0-9_-]{1,40}$ 一致。 */
export function newLauncherId(): string {
  const bytes = new Uint8Array(8)
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    crypto.getRandomValues(bytes)
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  }
  return 'l-' + Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
}

export function blankDraft(host = ''): LauncherDraft {
  return { id: newLauncherId(), name: '', host, dir: '', command: '' }
}

/** 「Cursor CLI」模板：用来在空白状态下一键起一个常用预设。命令写 agent；有的主机装的是旧名字 cursor-agent，界面上提示可以改。 */
export const CURSOR_CLI_NAME = 'Cursor CLI'
export const CURSOR_CLI_COMMAND = 'agent'

export function cursorCliDraft(host = ''): LauncherDraft {
  return { id: newLauncherId(), name: CURSOR_CLI_NAME, host, dir: '', command: CURSOR_CLI_COMMAND }
}

/** 「Cursor CLI 续聊」模板：非交互续聊的空白起点（#126）。 */
export const CURSOR_CLI_RESUME_NAME = 'Cursor CLI 续聊'

export function headlessDraft(host = ''): LauncherDraft {
  return { id: newLauncherId(), name: CURSOR_CLI_RESUME_NAME, host, dir: '', command: '', run: blankRunDraft() }
}

export function runDraftOf(r: LauncherRun | undefined): RunDraft {
  return {
    bin: r?.bin ?? '',
    model: r?.model ?? '',
    posture: r?.posture === 'force' ? 'force' : 'allowlist',
    workflow: r?.workflow_id ?? '',
    conversation: r?.conversation_id ?? '',
    prompt: r?.prompt ?? '',
    ...maxRunFields(r?.max_run_minutes),
    autoResume: r?.auto_resume === true,
    autoResumeOk: typeof r?.auto_resume_ok_minutes === 'number' && r.auto_resume_ok_minutes > 0 ? String(Math.floor(r.auto_resume_ok_minutes)) : '',
    newConversation: r?.new_conversation === true,
    origin: r?.origin ?? '',
  }
}

export function draftOf(l: TerminalLauncher): LauncherDraft {
  const d: LauncherDraft = { id: l.id, name: l.name, host: l.host, dir: l.dir ?? '', command: l.command ?? '' }
  if (l.kind === 'headless') d.run = runDraftOf(l.run)
  return d
}

/** 这条预设是不是「非交互续聊」。 */
export const isHeadlessPreset = (l: Pick<TerminalLauncher, 'kind'>) => l.kind === 'headless'

/** 名字是单行文本：各种空白折叠成一个空格。与服务端的规整一致（同时也是会话名被终端子系统规整后的样子）。 */
export function normalizeName(s: string): string {
  return s.trim().split(/\s+/).filter(Boolean).join(' ')
}

/** 提示词是单行文本：各种空白（含换行）折叠成一个空格。与服务端的规整一致。 */
export function collapseSpaces(s: string): string {
  return s.split(/\s+/).filter(Boolean).join(' ')
}

/** 草稿 → 提交给服务端的运行配置：空字段不带。 */
export function toRun(r: RunDraft): LauncherRun {
  const out: LauncherRun = { posture: r.posture }
  const bin = r.bin.trim()
  const model = r.model.trim()
  const workflow = r.workflow.trim()
  const conversation = r.conversation.trim()
  const prompt = collapseSpaces(r.prompt)
  if (bin) out.bin = bin
  if (model) out.model = model
  if (workflow) out.workflow_id = workflow
  if (conversation) out.conversation_id = conversation
  if (prompt) out.prompt = prompt
  const maxRun = parseMaxRun(r.maxRun, r.maxRunUnit)
  if (maxRun !== null && Number.isFinite(maxRun) && maxRun >= 1) out.max_run_minutes = maxRun
  if (r.autoResume) {
    out.auto_resume = true
    // 判定时长只在开着自动续跑时才有意义；留空 = 默认，不带（服务端存下来的预设保持原样）。
    const ok = parseAutoResumeOk(r.autoResumeOk)
    if (ok !== null && Number.isFinite(ok) && ok >= 1) out.auto_resume_ok_minutes = Math.min(ok, AUTO_RESUME_OK_MAX_MINUTES)
  }
  if (r.newConversation) out.new_conversation = true
  // 来源标记不是表单的选项：原样带回去，免得编辑一次「启用 Agent」创建的预设就把标记丢了
  const origin = (r.origin ?? '').trim()
  if (origin) out.origin = origin
  return out
}

export function toLauncher(d: LauncherDraft): TerminalLauncher {
  const base = { id: d.id, name: normalizeName(d.name), host: d.host.trim(), dir: d.dir.trim() }
  // 非交互续聊没有命令、没有守护：运行的命令由 run 里的选项拼出来。
  if (d.run) return { ...base, kind: 'headless', run: toRun(d.run) }
  return { ...base, command: d.command.trim() }
}

// ---------- 校验 ----------

export type DraftField = 'name' | 'host' | 'dir' | 'command' | 'bin' | 'model' | 'workflow' | 'conversation' | 'prompt' | 'maxRun' | 'autoResumeOk'
export type DraftErrors = Partial<Record<DraftField, string>>

// eslint-disable-next-line no-control-regex
const CONTROL_RE = /[\u0000-\u001f\u007f-\u009f]/
/** 完整路径：POSIX 绝对路径、~ 开头的家目录路径，或 Windows 的盘符 / UNC 路径。 */
const DIR_RE = /^(\/|~|[A-Za-z]:[\\/]|\\\\)/

export const hasControlChar = (s: string) => CONTROL_RE.test(s)

export interface DraftContext {
  limits: LauncherLimits
  /** 已保存的其他预设（不含正在编辑的这一条），用来查重名。 */
  others: readonly TerminalLauncher[]
}

/** 「非交互续聊」预设独有的检查（运行配置 + 必须是绝对目录）；规则与服务端 store.HeadlessRun.validate 一致。 */
function validateRun(r: RunDraft, limits: LauncherLimits, err: DraftErrors) {
  const t = toRun(r)

  if (t.bin) {
    if (charCount(t.bin) > limits.runBin) err.bin = `可执行文件最长 ${limits.runBin} 个字`
    else if (!BIN_RE.test(t.bin) || PREFIX_CMDS.has(t.bin)) err.bin = '可执行文件只能是命令名或路径（字母、数字和 _ . / ~ + -），不能是 cd、env、sudo、bash 这类前缀命令或 shell'
  }
  if (t.model) {
    if (charCount(t.model) > limits.runModel) err.model = `模型名最长 ${limits.runModel} 个字`
    else if (!MODEL_RE.test(t.model)) err.model = '模型名只能含字母、数字和 . _ : / + -，并以字母或数字开头'
  }
  if (t.workflow_id) {
    const n = charCount(t.workflow_id)
    if (n > RUN_WORKFLOW_MAX) err.workflow = `工作流编号最长 ${RUN_WORKFLOW_MAX} 个字，现在是 ${n} 个字`
    else if (/\s/.test(t.workflow_id) || hasControlChar(t.workflow_id)) err.workflow = '工作流编号里不能有空格或控制字符'
  }
  if (t.conversation_id && !CONVERSATION_RE.test(t.conversation_id)) err.conversation = '对话编号只能含字母、数字、下划线和连字符（最长 64 位）'
  if (!t.workflow_id && !t.conversation_id) err.workflow ??= '请填工作流编号（用来找到智能体最近的对话），或者直接固定一个对话编号'
  // 新开对话（#176）：对话是第一次运行时才新开的，所以不能同时固定一个已有的对话；要靠工作流编号让新对话接进来（规则与服务端 HeadlessRun.validate 一致）
  if (t.new_conversation) {
    if (t.conversation_id) err.conversation = '选了「第一次运行时新开对话」就不能再固定对话编号：把对话编号留空，或者取消这个选项'
    else if (!t.workflow_id) err.workflow ??= '「新开对话」要填工作流编号：新开的智能体靠它接进这个会话'
  }
  if (t.prompt) {
    const n = charCount(t.prompt)
    if (n > limits.runPrompt) err.prompt = `提示词最长 ${limits.runPrompt} 个字，现在是 ${n} 个字`
    else if (hasControlChar(t.prompt)) err.prompt = '提示词里不能有控制字符'
  }
  // 最长运行时间（#141）：留空 = 平台上限；填了要是 1 分钟到上限之间的整数分钟。旧版 Hub 不认识它（上限为 0）：表单不显示，也就不检查。
  const maxRun = parseMaxRun(r.maxRun, r.maxRunUnit)
  if (maxRun !== null && limits.runMaxMinutes > 0) {
    const cap = runLimitText(limits.runMaxMinutes * 60)
    if (Number.isNaN(maxRun)) err.maxRun = '最长运行时间请填一个正数，并精确到分钟（例如 30 分钟、2 小时、1.5 小时）'
    else if (maxRun < 1 || maxRun > limits.runMaxMinutes) err.maxRun = `最长运行时间要在 1 分钟到 ${cap}之间（留空 = ${cap}）`
  }
  // 续跑成功的判定时长（#172）：只在开着自动续跑时检查；留空 = 默认 10 分钟；填了要是 1 分钟到 1 天的整数分钟。
  if (r.autoResume) {
    const ok = parseAutoResumeOk(r.autoResumeOk)
    if (ok !== null && (Number.isNaN(ok) || ok < 1 || ok > AUTO_RESUME_OK_MAX_MINUTES)) {
      err.autoResumeOk = `判定时长请填 1 到 ${AUTO_RESUME_OK_MAX_MINUTES} 之间的整数分钟（留空 = ${AUTO_RESUME_OK_DEFAULT_MINUTES} 分钟）`
    }
  }
}

/** 会话页「运行」（#128）用：只校验运行选项（可执行文件 / 模型 / 对话编号 / 提示词），规则与预设完全一致。 */
export function validateRunOptions(r: RunDraft, limits: LauncherLimits): DraftErrors {
  const err: DraftErrors = {}
  validateRun(r, limits, err)
  return err
}

export function validateDraft(d: LauncherDraft, ctx: DraftContext): DraftErrors {
  const err: DraftErrors = {}
  const t = toLauncher(d)

  const nameLen = charCount(t.name)
  if (!t.name) err.name = '请给预设起个名字'
  else if (nameLen > ctx.limits.name) err.name = `名字最长 ${ctx.limits.name} 个字，现在是 ${nameLen} 个字`
  else if (hasControlChar(t.name)) err.name = '名字里不能有控制字符'
  else {
    const same = ctx.others.find(o => o.id !== t.id && o.name.toLowerCase() === t.name.toLowerCase())
    if (same) err.name = `已经有一个叫「${same.name}」的预设，换个名字`
  }

  if (!t.host) err.host = '请选择一台主机'

  if (t.dir) {
    const n = charCount(t.dir)
    if (n > ctx.limits.dir) err.dir = `目录最长 ${ctx.limits.dir} 个字，现在是 ${n} 个字`
    else if (hasControlChar(t.dir)) err.dir = '目录里不能有换行或控制字符'
    else if (d.run && !t.dir.startsWith('/')) err.dir = '非交互续聊的目录必须是主机上的绝对路径（以 / 开头）'
    else if (!DIR_RE.test(t.dir)) err.dir = '目录请写完整路径（以 / 或 ~ 开头；Windows 写盘符，例如 C:\\work）'
  } else if (d.run) {
    err.dir = '请填写目录：非交互续聊要在这个目录里运行（--workspace）'
  }

  if (d.run) {
    validateRun(d.run, ctx.limits, err)
    return err
  }

  if (t.command) {
    const n = charCount(t.command)
    if (n > ctx.limits.command) err.command = `命令最长 ${ctx.limits.command} 个字，现在是 ${n} 个字`
    else if (hasControlChar(t.command)) err.command = '命令只能是一行，不能有换行、制表符或其他控制字符'
  }
  return err
}

export const hasErrors = (e: DraftErrors) => Object.keys(e).length > 0

// ---------- 主机 ----------

/** 启动器能选的主机：已启用、装了 Agent（SSH 主机暂不支持）。在线的排前面，同组内按显示名排序。 */
export function launcherHosts(hosts: readonly HostView[]): HostView[] {
  return hosts
    .filter(h => h.status === 'enabled' && canHumanTerminal(h))
    .sort((a, b) => Number(b.online) - Number(a.online) || hostTitle(a).localeCompare(hostTitle(b), 'zh-Hans-CN'))
}

export type HostReadiness = { ok: true } | { ok: false; reason: 'missing' | 'unsupported' | 'disabled' | 'offline'; text: string }

/** 预设里的主机现在能不能启动会话；不能时给出一句可以直接展示的原因。 */
export function hostReadiness(host: HostView | undefined): HostReadiness {
  if (!host) return { ok: false, reason: 'missing', text: '主机已不在列表里（可能被删除了）' }
  if (!canHumanTerminal(host)) return { ok: false, reason: 'unsupported', text: '暂不支持 SSH 主机' }
  if (host.status !== 'enabled') return { ok: false, reason: 'disabled', text: '主机未启用' }
  if (!host.online) return { ok: false, reason: 'offline', text: '主机离线' }
  return { ok: true }
}

// ---------- 认出已经在运行的会话 ----------

/**
 * 这个预设在它的主机上正在运行的会话（新的在前）。靠「主机 + 会话名」认：启动时新建会话用的就是预设的名字，
 * 所以在 ExecMesh 里把它改了名，就不再被当成这个预设的会话——只是少一个「继续」入口，不会出错。
 */
export function runningSessionsOf(l: TerminalLauncher, host: HostView | undefined): SessionInfo[] {
  if (!host) return []
  const want = normalizeName(l.name)
  return host.sessions
    .filter(s => s.state === 'running' && normalizeName(s.name ?? '') === want)
    .sort((a, b) => (b.created ?? 0) - (a.created ?? 0) || b.sid.localeCompare(a.sid))
}

// ---------- 自动输入的那一行 ----------

/** 单引号包起来的 shell 字面量。 */
export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

/**
 * 给 cd 用的目录写法。~ 开头的路径让 shell 自己展开波浪号（放进引号里它就成了普通字符），其余部分照常加引号。
 * 例：`~/my work` → `~/'my work'`，`/srv/a'b` → `'/srv/a'\''b'`。
 */
export function quoteDir(dir: string): string {
  const m = /^(~[A-Za-z0-9._-]*)(\/.*)?$/.exec(dir)
  if (!m) return shQuote(dir)
  const rest = m[2] ?? ''
  if (rest.length <= 1) return m[1] + rest
  return `${m[1]}/${shQuote(rest.slice(1))}`
}

export const isWindowsHost = (os: string | undefined) => /^win/i.test(os ?? '')
export const isMacHost = (os: string | undefined) => /^(mac|darwin)/i.test(os ?? '')

// ---------- 主机平台 ----------

/**
 * 一台主机的平台信息：os 是 Agent 握手时自报的系统名（Linux 是发行版名，macOS 是「macOS 14.5」，Windows 是 windows）；
 * shell / home / sep 是会话守护进程上报的执行环境（旧版守护进程没有这三项，对应字段为空）。都是主机自报的，只当提示用。
 */
export interface HostPlatform {
  os?: string
  /** 默认 shell 的名称：bash / zsh / sh / pwsh / powershell / cmd …（Windows 上依次选 pwsh、powershell、cmd）。 */
  shell?: string
  /** 账户家目录；Windows 上用它展开 ~。 */
  home?: string
  /** 路径分隔符：/ 或 \。 */
  sep?: string
}

/** 平台参数：兼容只传系统名的老写法（字符串就是 os）。 */
export type PlatformArg = string | HostPlatform | undefined

export function platformOf(p: PlatformArg): HostPlatform {
  return typeof p === 'string' ? { os: p } : (p ?? {})
}

export function hostPlatformOf(host: Pick<HostView, 'os' | 'holder'> | undefined): HostPlatform {
  const ex = host?.holder?.exec
  return { os: host?.os, shell: ex?.shell, home: ex?.home, sep: ex?.sep }
}

/**
 * 会话 shell 的「语法族」，决定启动那一行怎么拼：
 * - posix：bash / zsh / sh 等——Linux、macOS，以及没有平台信息的主机（一直以来的写法）；
 * - powershell：Windows，守护进程上报了 pwsh / powershell（Agent 在 Windows 上优先选它们）；
 * - cmd：Windows，上报的是 cmd（机器上既没有 pwsh 也没有 powershell 时才会这样）；
 * - windows：Windows，但没上报 shell（旧版守护进程）——不知道该用哪种语法，什么都不拼。
 */
export type ShellKind = 'posix' | 'powershell' | 'cmd' | 'windows'

export function shellKindOf(arg: PlatformArg): ShellKind {
  const p = platformOf(arg)
  if (!isWindowsHost(p.os)) return 'posix'
  const sh = (p.shell ?? '').trim().toLowerCase().replace(/\.exe$/, '')
  if (sh === 'pwsh' || sh === 'powershell') return 'powershell'
  if (sh === 'cmd') return 'cmd'
  return 'windows'
}

/** 「命令退出即结束会话」靠 POSIX shell 的 exec 实现；PowerShell / cmd 里没有 exec，输进去只会报错、命令根本不运行。 */
export const supportsExitEndsSession = (arg: PlatformArg) => shellKindOf(arg) === 'posix'

/**
 * PowerShell 里的单引号字面量。除了 ASCII 的 '，排版用的弯引号（‘ ’ ‚ ‛）也会被 PowerShell 当成单引号——
 * 目录名里有 John’s 这样的字符时，只翻倍 ' 会让字符串提前结束、命令行停在续行提示符 >> 上；四种都翻倍才对（已在 PowerShell 7 与 Windows PowerShell 5.1 上实测）。
 */
export function psQuote(s: string): string {
  return `'${s.replace(/['\u2018\u2019\u201a\u201b]/g, m => m + m)}'`
}

/**
 * Windows 主机上要进入的目录：盘符 / UNC 路径原样用；~ 开头的（~、~\x、~/x）用主机上报的家目录展开。
 * 其余写法（/ 开头、~用户名）在 Windows 上没有确定的含义，家目录也没上报时同样如此——返回空串，调用方就不拼「进入目录」那一句。
 */
export function windowsDir(dir: string, p: HostPlatform): string {
  const d = dir.trim()
  if (/^[A-Za-z]:[\\/]/.test(d) || d.startsWith('\\\\')) return d
  const m = /^~(?:[\\/](.*))?$/.exec(d)
  const home = (p.home ?? '').trim()
  if (!m || !home) return ''
  const rest = (m[1] ?? '').replace(/\//g, '\\')
  return rest ? `${home.replace(/[\\/]+$/, '')}\\${rest}` : home
}

/** cmd 里放进双引号也会被改写的字符：% 会按环境变量展开，" 会提前结束引号，^ ! 在个别设置下也有特殊含义。 */
const CMD_UNSAFE_RE = /[%"^!]/

/**
 * 会话就绪后要输入的那一行（不含回车）；预设没有命令时是空串（只打开终端，什么也不输入）。
 *
 * 有目录时命令前面带一句「先进入目录」：新建会话时目录也会传给主机，但主机上找不到这个目录（或者是 ~ 开头的路径）
 * 会静默回落到默认目录——对 Cursor CLI 这类会在当前目录里改文件的程序，悄悄跑到错误的位置比失败更糟。
 * 带上这一句之后，目录不对就是一句看得见的报错，命令不会运行。写法按主机的 shell 来：
 * - POSIX（Linux / macOS）：`cd -- <目录> && <命令>`；
 * - PowerShell：`Set-Location -LiteralPath '<目录>' -ErrorAction Stop; <命令>`——Windows PowerShell 5.1 没有 &&，
 *   用分号加 -ErrorAction Stop：进不去目录是终止性错误，同一行里后面的命令不会运行（PowerShell 7 与 Windows PowerShell 5.1 上实测；
 *   5.1 里写 && 是语法错误，所以不能用它）；
 * - cmd：`cd /d "<目录>" && <命令>`（目录含 % " ^ ! 时不拼，免得被 cmd 改写；其余特殊字符的目录名在 Windows 实机上实测都能进入）；
 * - Windows 但不知道 shell（旧版守护进程）：不拼，和以前一样。
 */
export function startLine(l: Pick<TerminalLauncher, 'dir' | 'command' | 'guard'>, platform?: PlatformArg): string {
  const p = platformOf(platform)
  const kind = shellKindOf(p)
  let command = (l.command ?? '').trim()
  if (!command) return ''
  if (l.guard?.exit_ends_session && kind === 'posix') {
    const c = classifyLauncherExec(command)
    if (c.ok && c.exec) command = c.exec
  }
  const dir = (l.dir ?? '').trim()
  if (!dir) return command
  switch (kind) {
    case 'posix':
      return `cd -- ${quoteDir(dir)} && ${command}`
    case 'powershell': {
      const target = windowsDir(dir, p)
      return target ? `Set-Location -LiteralPath ${psQuote(target)} -ErrorAction Stop; ${command}` : command
    }
    case 'cmd': {
      const target = windowsDir(dir, p)
      return target && !target.startsWith('\\\\') && !CMD_UNSAFE_RE.test(target) ? `cd /d "${target}" && ${command}` : command
    }
    default:
      return command
  }
}

/**
 * 传给「新建会话」的启动目录。~ 开头的路径：POSIX 主机不展开，不传（由 startLine 里的 cd 让 shell 展开）；
 * Windows 主机没有这种展开，用它上报的家目录换成绝对路径（没上报就不传）。
 */
export function sessionCwd(dir: string | undefined, platform?: PlatformArg): string | undefined {
  const d = (dir ?? '').trim()
  if (!d) return undefined
  if (!d.startsWith('~')) return d
  const p = platformOf(platform)
  return shellKindOf(p) === 'posix' ? undefined : windowsDir(d, p) || undefined
}

/**
 * 目录的写法与所选主机的平台对不上时给的提醒（不拦保存：主机可能刚换了系统，也可能只是我们没认出来）；没问题返回空串。
 * 只在知道主机系统时才提醒（没选主机、或旧数据里没有系统名就不说话）。
 */
export function dirAdvice(dir: string, platform?: PlatformArg): string {
  const d = dir.trim()
  const p = platformOf(platform)
  if (!d || !p.os) return ''
  const kind = shellKindOf(p)
  const winPath = /^([A-Za-z]:[\\/]|\\\\)/.test(d)
  if (kind === 'posix') return winPath ? '这台主机不是 Windows：目录请以 / 或 ~ 开头。' : ''
  if (d.startsWith('/')) return '这台主机是 Windows：目录请写盘符路径（例如 C:\\work\\项目）；以 / 开头的路径在 Windows 上不会生效。'
  if (d.startsWith('~') && !windowsDir(d, p)) {
    return (p.home ?? '').trim()
      ? '这台主机是 Windows：~ 后面要直接跟路径（~\\work），不支持 ~用户名；也可以直接写盘符路径。'
      : '这台主机是 Windows，还没有上报家目录，~ 开头的目录不会生效：请写盘符路径（例如 C:\\Users\\你\\work），或把这台主机的 Agent 升到最新版。'
  }
  if (kind === 'windows' && winPath) {
    return '这台主机的会话守护进程较旧，没有上报 shell 种类：目录只在新建会话时交给主机，不存在时会悄悄回落到默认目录、不会报错。升级 Agent 后会像其他主机一样先进入目录再运行。'
  }
  return ''
}

/** 目录输入框的示例：跟着所选主机的系统走（没选 / 认不出来用一直以来的示例）。 */
export function dirPlaceholder(platform?: PlatformArg): string {
  const p = platformOf(platform)
  if (isWindowsHost(p.os)) return 'C:\\work\\RelayMesh'
  if (isMacHost(p.os)) return '/Users/你/projects/RelayMesh'
  return '/home/user/RelayMesh'
}

// ---------- 什么时候可以输入 ----------

/** 会话刚连上时 shell 还在读配置、打印提示符；这时输入的字符会被终端驱动先回显一遍，提示符出来后又重画一遍，看起来是重复的。 */
export const READY_QUIET_MS = 350
/** shell 迟迟没有输出（极简 shell、提示符为空）时，连上之后最多等这么久就输入。 */
export const READY_MAX_WAIT_MS = 4000

export interface ReadyClock {
  /** 连接变为「已连接」的时刻；还没连上是 null。 */
  liveAt: number | null
  /** 最近一次收到输出的时刻；还没有输出是 null。 */
  lastOutputAt: number | null
  now: number
}

/** 连上之后，输出安静了一小会儿（提示符已经出来）就可以输入；等太久也直接输入。 */
export function readyToType(c: ReadyClock): boolean {
  if (c.liveAt === null) return false
  if (c.now - c.liveAt >= READY_MAX_WAIT_MS) return true
  return c.lastOutputAt !== null && c.now - c.lastOutputAt >= READY_QUIET_MS
}
