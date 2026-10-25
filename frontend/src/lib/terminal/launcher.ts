import type { HeadlessPosture, HostView, LauncherAfterNew, LauncherRun, LauncherSource, TerminalLauncher, TerminalPrefsLimits } from '../../api/terminal'
import { hostTitle, runLimitText } from './format'
import { canHumanTerminal } from './hosts'

// 终端启动器（会话页顶栏的「终端」入口）：一份预设 = 在哪台主机、哪个目录、接着哪个对话运行一次（「非交互续聊」，#126）。
// 这里是不碰界面的那一部分：预设草稿的校验（与服务端 internal/store/terminal_launchers.go 一致）、主机是否可用、目录的写法提醒。
// 旧版的交互式「开终端」预设已经下线（#245）：自动输入命令、认出运行中的会话、会话守护的那些代码都不在了。

// ---------- 取值范围 ----------

export interface LauncherLimits {
  /** 预设最多几个。 */
  max: number
  /** 以下两项按字符数。 */
  name: number
  dir: number
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
  max: 20, name: 40, dir: 512,
  runSupported: false, runPrompt: 1500, runModel: 64, runBin: 128, runPromptDefault: '', runNewPromptDefault: '', runMaxMinutes: 0,
}

/** 工作流编号的最大长度：与服务端 store.MaxGuardWorkflowRunes 一致。 */
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

// ---------- Windows 主机上的路径与可执行文件（#178）：规则与服务端 store/headless_paths.go 一致 ----------
//
// Windows 上的命令走 PowerShell，而 agent 往往是 agent.cmd 这样的批处理垫片：PowerShell 把参数交给 cmd.exe 时，
// 双引号、% ^ & | < > ! 会被当成 cmd 自己的语法，所以目录与可执行文件的路径里一律不许出现它们（提示词里的由服务端换成全角）。

const WIN_DRIVE_RE = /^[A-Za-z]:[\\/]/
const WIN_UNC_RE = /^\\\\[^\\/]+[\\/]+[^\\/]+/
/** Windows 上「命令名」写法的可执行文件（agent、agent.cmd）：没有路径分隔符、空白与 ~，不以点开头。 */
const WIN_BARE_BIN_RE = /^[A-Za-z0-9_+-][A-Za-z0-9_.+-]*$/
const WIN_PATH_BAD = '"%^&|<>!*?'
const WIN_SHELL_NAMES = new Set(['cmd', 'powershell', 'pwsh', 'wsl', 'bash', 'sh', 'zsh', 'fish'])
const WIN_ONLY_SHELL_NAMES = new Set(['cmd', 'powershell', 'pwsh', 'wsl'])

/** 是不是 Windows 的绝对路径：盘符（C:\work、C:/work）或 UNC（\\服务器\共享\…）。 */
export const isWindowsAbsPath = (p: string) => WIN_DRIVE_RE.test(p) || WIN_UNC_RE.test(p)

/** Windows 路径里不该出现的字符；没问题返回空串，有就返回可直接接在「目录」「可执行文件」后面的一句话。 */
export function windowsPathProblem(p: string): string {
  for (let i = 0; i < p.length; i++) {
    const ch = p[i]
    if (CONTROL_RE.test(ch)) return '里不能有换行或控制字符'
    if (WIN_PATH_BAD.includes(ch)) return `里不能有「${ch}」：这些字符（${[...WIN_PATH_BAD].join(' ')}）会被 cmd.exe 当成命令语法，或者路径里本来就不允许`
    if (ch === ':' && i !== 1) return '里的冒号只能出现在盘符后面（如 C:\\work）'
  }
  return ''
}

const binBase = (bin: string) => (bin.split(/[\\/]/).pop() ?? '').toLowerCase().replace(/\.exe$/, '')

/** 这个可执行文件能不能用在 Windows 主机上：命令名（agent、agent.cmd），或盘符 / UNC 绝对路径；不能是 shell，路径里没有 cmd 语法字符。 */
export function validWindowsBin(bin: string): boolean {
  if (!bin || bin !== bin.trim() || WIN_SHELL_NAMES.has(binBase(bin))) return false
  if (isWindowsAbsPath(bin)) return windowsPathProblem(bin) === '' && !/[\\/]$/.test(bin)
  return WIN_BARE_BIN_RE.test(bin) && !PREFIX_CMDS.has(bin)
}

/** 可执行文件的写法是否合法，不分主机系统（预设保存时用）：POSIX 的命令名或路径（一直以来的规则），或 Windows 的命令名、盘符 / UNC 路径。 */
export function validHeadlessBin(bin: string): boolean {
  if (BIN_RE.test(bin)) return !PREFIX_CMDS.has(bin) && !WIN_ONLY_SHELL_NAMES.has(binBase(bin))
  return validWindowsBin(bin)
}

const positive = (v: number | undefined, fallback: number) => (typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : fallback)

export function launcherLimits(l?: Partial<TerminalPrefsLimits> | null): LauncherLimits {
  const d = LAUNCHER_DEFAULT_LIMITS
  return {
    max: positive(l?.launcher_max, d.max),
    name: positive(l?.launcher_name_max, d.name),
    dir: positive(l?.launcher_dir_max, d.dir),
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
  /**
   * 对话来源（#191，三选一）：resume = 指定会话（命令带 --resume；填了 conversation 就固定接它，留空 = 工作流里最近汇报的对话）；
   * new = 新会话（命令不带 --resume，Hub 从输出里学到新对话的编号；需要填工作流编号）；
   * continue = 最近会话（命令用 --continue，接着目录里 Cursor CLI 认的最近对话；需要填工作流编号）。
   * 没有它按 resume 处理（会话页「运行」不用它）。只有 resume 会提交 conversation。
   */
  source?: LauncherSource
  /** 「新会话」运行之后的去向（#559）：remember = 记下编号、之后接着它（默认）/ stay = 不切换、每次都新开 / continue = 成功后切成最近会话。只在 source 是 new 时有意义。 */
  afterNew?: LauncherAfterNew
  /** 预设的来源标记（enable_agent = 由「启用 Agent」创建）：表单不让改，只负责在编辑保存时原样带回去（否则编辑一次标记就丢了）。 */
  origin?: string
}

export interface LauncherDraft {
  id: string
  name: string
  host: string
  dir: string
  /** 「非交互续聊」预设的运行选项（点「运行」由 Hub 运行一次，没有命令）。 */
  run: RunDraft
}

export const blankRunDraft = (): RunDraft => ({
  bin: '', model: '', posture: 'allowlist', workflow: '', conversation: '', prompt: '', maxRun: '', maxRunUnit: 'hour', autoResume: false, autoResumeOk: '',
  source: 'resume', afterNew: 'remember', origin: '',
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

/** 「Cursor CLI 续聊」模板：非交互续聊的空白起点（#126）。 */
export const CURSOR_CLI_RESUME_NAME = 'Cursor CLI 续聊'

export function headlessDraft(host = ''): LauncherDraft {
  return { id: newLauncherId(), name: CURSOR_CLI_RESUME_NAME, host, dir: '', run: blankRunDraft() }
}

/** 预设实际的对话来源（#191）：设了 source 就用它；存量预设（没有 source）按旧字段推导——勾了「新开对话」是 new，其余是 resume。与服务端 EffectiveSource 一致。 */
export function effectiveSource(r: Pick<LauncherRun, 'source' | 'new_conversation'> | undefined): LauncherSource {
  if (r?.source === 'resume' || r?.source === 'new' || r?.source === 'continue') return r.source
  return r?.new_conversation === true ? 'new' : 'resume'
}

/** 「新会话」运行之后的去向：来源不是新会话时是 ''；没设就是默认的「记下编号」。与服务端 EffectiveAfterNew 一致。 */
export function effectiveAfterNew(r: Pick<LauncherRun, 'source' | 'new_conversation' | 'after_new'> | undefined): LauncherAfterNew | '' {
  if (effectiveSource(r) !== 'new') return ''
  return r?.after_new === 'stay' || r?.after_new === 'continue' ? r.after_new : 'remember'
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
    source: effectiveSource(r),
    afterNew: effectiveAfterNew(r) || 'remember',
    origin: r?.origin ?? '',
  }
}

export function draftOf(l: TerminalLauncher): LauncherDraft {
  return { id: l.id, name: l.name, host: l.host, dir: l.dir ?? '', run: runDraftOf(l.run) }
}

/** 这条预设是不是「非交互续聊」。 */
export const isHeadlessPreset = (l: Pick<TerminalLauncher, 'kind'>) => l.kind === 'headless'

/**
 * 旧版（已停用）的交互式「开终端」预设（#245 第一期）：不是「非交互续聊」的都算。
 * 它们不能再新建、修改、启动，清单里只留删除；服务端仍把它们原样读出来（第三期才清理），所以界面要认得它们，并说清楚为什么只有删除。
 */
export const isLegacyPreset = (l: Pick<TerminalLauncher, 'kind'>) => !isHeadlessPreset(l)
export const LEGACY_PRESET_BADGE = '旧版（已停用）'
export const LEGACY_PRESET_HINT = '交互式「开终端」预设已停用，只能删除。要在这台主机上开终端，请直接用 ExecMesh 的网页终端。'

/** 名字是单行文本：各种空白折叠成一个空格。与服务端的规整一致（同时也是会话名被终端子系统规整后的样子）。 */
export function normalizeName(s: string): string {
  return s.trim().split(/\s+/).filter(Boolean).join(' ')
}

/** 提示词是单行文本：各种空白（含换行）折叠成一个空格。与服务端的规整一致。 */
export function collapseSpaces(s: string): string {
  return s.split(/\s+/).filter(Boolean).join(' ')
}

/** 草稿里选的对话来源：没有（会话页「运行」的草稿）或不认识就是指定会话。 */
export function draftSource(r: Pick<RunDraft, 'source'>): LauncherSource {
  return r.source === 'new' || r.source === 'continue' ? r.source : 'resume'
}

/** 草稿 → 提交给服务端的运行配置：空字段不带。 */
export function toRun(r: RunDraft): LauncherRun {
  const out: LauncherRun = { posture: r.posture }
  const bin = r.bin.trim()
  const model = r.model.trim()
  const workflow = r.workflow.trim()
  const source = draftSource(r)
  // 只有「指定会话」提交对话编号：另外两种来源下输入框是藏起来的，藏着的内容不该悄悄生效（所见即所存）。
  const conversation = source === 'resume' ? r.conversation.trim() : ''
  const prompt = collapseSpaces(r.prompt)
  out.source = source
  if (source === 'new') out.after_new = r.afterNew === 'stay' || r.afterNew === 'continue' ? r.afterNew : 'remember'
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
  // 来源标记不是表单的选项：原样带回去，免得编辑一次「启用 Agent」创建的预设就把标记丢了
  const origin = (r.origin ?? '').trim()
  if (origin) out.origin = origin
  return out
}

export function toLauncher(d: LauncherDraft): TerminalLauncher {
  // 非交互续聊没有命令：运行的命令由 run 里的选项拼出来。
  return { id: d.id, name: normalizeName(d.name), host: d.host.trim(), dir: d.dir.trim(), kind: 'headless', run: toRun(d.run) }
}

// ---------- 校验 ----------

export type DraftField = 'name' | 'host' | 'dir' | 'bin' | 'model' | 'workflow' | 'conversation' | 'prompt' | 'maxRun' | 'autoResumeOk'
export type DraftErrors = Partial<Record<DraftField, string>>

// eslint-disable-next-line no-control-regex
const CONTROL_RE = /[\u0000-\u001f\u007f-\u009f]/
const hasControlChar = (s: string) => CONTROL_RE.test(s)

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
    else if (!validHeadlessBin(t.bin)) err.bin = '可执行文件只能是命令名或路径（Linux / macOS：字母、数字和 _ . / ~ + -；Windows：agent 这样的命令名，或 C:\\…\\agent.cmd 这样的盘符 / UNC 路径，路径里不能有 " % ^ & | < > ! * ?），不能是 cd、env、sudo、bash 这类前缀命令，也不能是 cmd、powershell 这类 shell'
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
  // 对话来源（#191，规则与服务端 HeadlessRun.validate 一致；toRun 只在指定会话时带对话编号，所以另外两种来源不会有「又固定了编号」的冲突）：
  // 指定会话至少要有工作流编号或对话编号；新会话 / 最近会话都要靠工作流编号——新开的智能体靠它接进会话，最近会话的运行靠它归属与记系统行。
  switch (draftSource(r)) {
    case 'new':
      if (!t.workflow_id) err.workflow ??= '「新会话」要填工作流编号：新开的智能体靠它接进这个会话'
      break
    case 'continue':
      if (!t.workflow_id) err.workflow ??= '「最近会话」要填工作流编号：它用来归属这次运行、在聊天里记系统行、判断有没有向 RelayMesh 汇报'
      break
    default:
      if (!t.workflow_id && !t.conversation_id) err.workflow ??= '请填工作流编号（用来找到智能体最近的对话），或者直接固定一个对话编号'
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
    else if (!t.dir.startsWith('/') && !isWindowsAbsPath(t.dir)) err.dir = '非交互续聊的目录必须是主机上的绝对路径（Linux / macOS 以 / 开头；Windows 写盘符路径如 C:\\work，或 \\\\服务器\\共享\\… 形式）'
    else if (isWindowsAbsPath(t.dir) && windowsPathProblem(t.dir)) err.dir = `目录${windowsPathProblem(t.dir)}`
  } else {
    err.dir = '请填写目录：非交互续聊要在这个目录里运行（--workspace）'
  }

  validateRun(d.run, ctx.limits, err)
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

// ---------- 主机平台与目录写法 ----------

export const isWindowsHost = (os: string | undefined) => /^win/i.test(os ?? '')
export const isMacHost = (os: string | undefined) => /^(mac|darwin)/i.test(os ?? '')

/** 一台主机的平台信息：os 是 Agent 握手时自报的系统名（Linux 是发行版名，macOS 是「macOS 14.5」，Windows 是 windows）；主机自报的，只当提示用。 */
export interface HostPlatform {
  os?: string
}

/** 平台参数：兼容只传系统名的老写法（字符串就是 os）。 */
type PlatformArg = string | HostPlatform | undefined

export function platformOf(p: PlatformArg): HostPlatform {
  return typeof p === 'string' ? { os: p } : (p ?? {})
}

export function hostPlatformOf(host: Pick<HostView, 'os'> | undefined): HostPlatform {
  return { os: host?.os }
}

/**
 * 目录的写法与所选主机的平台对不上时给的提醒（不拦保存：主机可能刚换了系统，也可能只是我们没认出来）；没问题返回空串。
 * 只在知道主机系统时才提醒（没选主机、或旧数据里没有系统名就不说话）。续聊预设的目录必须是绝对路径、不展开 ~。
 */
export function dirAdvice(dir: string, platform?: PlatformArg): string {
  const d = dir.trim()
  const p = platformOf(platform)
  if (!d || !p.os) return ''
  const winPath = /^([A-Za-z]:[\\/]|\\\\)/.test(d)
  if (!isWindowsHost(p.os)) return winPath ? '这台主机不是 Windows：目录请以 / 开头。' : ''
  if (d.startsWith('/')) return '这台主机是 Windows：目录请写盘符路径（例如 C:\\work\\项目）；以 / 开头的路径在 Windows 上不会生效。'
  return ''
}

/** 目录输入框的示例：跟着所选主机的系统走（没选 / 认不出来用一直以来的示例）。 */
export function dirPlaceholder(platform?: PlatformArg): string {
  const p = platformOf(platform)
  if (isWindowsHost(p.os)) return 'C:\\work\\RelayMesh'
  if (isMacHost(p.os)) return '/Users/你/projects/RelayMesh'
  return '/home/user/RelayMesh'
}
