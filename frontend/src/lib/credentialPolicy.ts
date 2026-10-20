// 令牌「权限页」的纯逻辑：档位预设、表单与接口载荷的互转、校验。
// 不依赖 Vue / DOM，便于单元测试；界面在 components/settings/CredentialPolicyEditor.vue。

import type {
  CredentialInput,
  CredentialTier,
  MCPCredential,
  MCPPermissions,
  TerminalLimits,
  TerminalModeCap
} from '../api/client'

export const GOVERNANCE_KEYS = ['feedback', 'sessions', 'system_info', 'skills', 'configure', 'execute'] as const
export const TERMINAL_KEYS = ['terminal', 'terminal_files'] as const

/** 界面上的三个「模式」。档位在服务端由权限推导；界面模式只是权限的一种便捷预设。 */
export type PolicyMode = 'relaymesh' | 'execmesh' | 'custom'

export interface PermissionOption {
  key: keyof MCPPermissions
  label: string
  tools: string
  hint?: string
}

export const GOVERNANCE_OPTIONS: PermissionOption[] = [
  { key: 'feedback', label: '沟通反馈', tools: 'interactive_feedback, continue_feedback_session' },
  { key: 'sessions', label: '会话查询', tools: 'list_sessions, get_session_history' },
  { key: 'system_info', label: '系统信息', tools: 'get_system_info' },
  { key: 'skills', label: '规范管理', tools: 'manage_skills' },
  { key: 'configure', label: '任务指挥', tools: 'configure_task' },
  { key: 'execute', label: '自驾上报', tools: 'report_progress', hint: '自驾（autopilot）模式下上报进度，与执行命令无关' }
]

export const TERMINAL_OPTIONS: PermissionOption[] = [
  { key: 'terminal', label: '终端执行', tools: 'terminal_targets, terminal_exec, terminal_run', hint: '在被授权的主机上执行命令，等同远程执行权限' },
  { key: 'terminal_files', label: '终端文件', tools: 'terminal_targets, terminal_files', hint: '在被授权的主机上查看、拉取、写入文件，范围限定在你指定的目录内（也可以明确开启高风险的「允许整个文件系统」）；需要服务端开启文件能力' }
]

/**
 * 运行预设（#179）：项目管理 Agent 唤醒本项目里的子会话。它不是终端工具，但同样是「让一个智能体去拉起另一个智能体」的能力，
 * 所以单独成组、用琥珀色标出，授予时与终端权限一样要再次验证密码（服务端强制）。
 */
export const LAUNCHER_OPTIONS: PermissionOption[] = [
  {
    key: 'launcher',
    label: '运行预设',
    tools: 'preset_run',
    hint: '项目管理 Agent 可以运行本项目里已有的「非交互续聊」预设来唤醒子会话（只能运行：不能改预设、不能传提示词）。调用方还必须是项目的管理 Agent（项目页「任务管理」里勾选）；需要同时开启治理类工具'
  }
]

/** 终端工具的原生名字与 ExecMesh 兼容命名下的名字。 */
export const TERMINAL_NATIVE_TOOLS = ['terminal_targets', 'terminal_exec', 'terminal_run'] as const
export const TERMINAL_COMPAT_TOOLS = ['targets_list', 'target_info', 'exec', 'exec_start', 'job_status', 'job_output', 'job_cancel'] as const

/** 当前权限下终端工具的名字清单（用于权限页提示文案）。 */
export function terminalToolNames(p: MCPPermissions): readonly string[] {
  return p.terminal && p.terminal_compat ? TERMINAL_COMPAT_TOOLS : TERMINAL_NATIVE_TOOLS
}

export function relayMeshPreset(): MCPPermissions {
  return { feedback: true, sessions: true, system_info: true, skills: true, configure: true, execute: true, terminal: false, terminal_files: false, terminal_compat: false, launcher: false }
}

export function execMeshPreset(): MCPPermissions {
  return { feedback: false, sessions: false, system_info: false, skills: false, configure: false, execute: false, terminal: true, terminal_files: false, terminal_compat: false, launcher: false }
}

function hasTerminal(p: MCPPermissions): boolean {
  return !!p.terminal || !!p.terminal_files
}

function hasGovernance(p: MCPPermissions): boolean {
  return GOVERNANCE_KEYS.some(k => !!p[k])
}

/** 与服务端 TierForPermissions 保持一致。 */
export function tierOf(p: MCPPermissions): CredentialTier {
  if (hasTerminal(p) && !hasGovernance(p)) return 'execmesh'
  if (hasTerminal(p) && hasGovernance(p)) return 'hybrid'
  if (hasGovernance(p)) return 'relaymesh'
  return 'none'
}

export function tierLabel(tier: CredentialTier | string | undefined): string {
  switch (tier) {
    case 'relaymesh': return 'RelayMesh'
    case 'execmesh': return 'ExecMesh'
    case 'hybrid': return '混合'
    case 'none': return '无工具'
    default: return ''
  }
}

/** 权限当前对应哪个预设；不完全匹配任何预设时为 custom。 */
export function modeOf(p: MCPPermissions): PolicyMode {
  const allGovernance = GOVERNANCE_KEYS.every(k => !!p[k])
  // 带了「运行预设」就不再是任何一个预设档位（档位预设都不含它）：落到自定义，免得档位名与实际权限对不上。
  if (p.launcher) return 'custom'
  if (allGovernance && !hasTerminal(p)) return 'relaymesh'
  if (!hasGovernance(p) && !!p.terminal && !p.terminal_files) return 'execmesh'
  return 'custom'
}

export interface PolicyForm {
  permissions: MCPPermissions
  /** 选中的主机机器码；allHosts 为 true 时忽略。 */
  terminalHosts: string[]
  allHosts: boolean
  modeCap: TerminalModeCap
  maxConcurrent: string
  maxWaitSeconds: string
  maxTimeoutSeconds: string
  maxOutputBytes: string
  allowedDirsText: string
  /** 文件工具（terminal_files）允许访问的目录，每行一个；带文件权限时必填（除非开启下面的「允许整个文件系统」）。 */
  allowedPathsText: string
  /**
   * 「允许整个文件系统」：高风险开关，默认关闭。开启后文件工具不再受上面的目录限制（目录原样保留，关闭后恢复）。
   * 只对文件权限有意义；开启需要刚刚重新验证过密码（服务端强制，见 grantsWholeFs）。
   */
  allowWholeFs: boolean
  /** <input type="datetime-local"> 的值（本地时间，形如 2026-10-01T12:30）；空串表示不过期。 */
  expiresAtLocal: string
  /** 每行一条 CIDR 或单个 IP；空表示不限来源。 */
  allowedCidrsText: string
  /**
   * 能力 URL 凭据：令牌写进专用网址。只在新建时可选，建好后不可改变；
   * 只能是 ExecMesh 档（只带终端执行权限），必须逐台授权主机。
   */
  capability: boolean
}

export function defaultPolicyForm(): PolicyForm {
  return {
    permissions: relayMeshPreset(),
    terminalHosts: [],
    allHosts: false,
    modeCap: '',
    maxConcurrent: '',
    maxWaitSeconds: '',
    maxTimeoutSeconds: '',
    maxOutputBytes: '',
    allowedDirsText: '',
    allowedPathsText: '',
    allowWholeFs: false,
    expiresAtLocal: '',
    allowedCidrsText: '',
    capability: false
  }
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** ISO 时间 → datetime-local 控件的值（本地时区）。无法解析时返回空串。 */
export function isoToLocalInput(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** datetime-local 的值 → ISO（UTC）。空串或非法值返回 null。 */
export function localInputToIso(local: string): string | null {
  const v = local.trim()
  if (!v) return null
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return null
  return d.toISOString()
}

function numToText(n: number | undefined): string {
  return n && n > 0 ? String(n) : ''
}

export function formFromCredential(c: MCPCredential): PolicyForm {
  const hosts = c.terminal_hosts ?? []
  const limits = c.terminal_limits ?? {}
  return {
    permissions: {
      feedback: !!c.permissions.feedback,
      sessions: !!c.permissions.sessions,
      system_info: !!c.permissions.system_info,
      skills: !!c.permissions.skills,
      configure: !!c.permissions.configure,
      execute: !!c.permissions.execute,
      terminal: !!c.permissions.terminal,
      terminal_files: !!c.permissions.terminal_files,
      terminal_compat: !!c.permissions.terminal && !!c.permissions.terminal_compat,
      launcher: !!c.permissions.launcher
    },
    allHosts: hosts.includes('*'),
    terminalHosts: hosts.filter(h => h !== '*'),
    modeCap: c.terminal_mode_cap ?? '',
    maxConcurrent: numToText(limits.max_concurrent),
    maxWaitSeconds: numToText(limits.max_wait_seconds),
    maxTimeoutSeconds: numToText(limits.max_timeout_seconds),
    maxOutputBytes: numToText(limits.max_output_bytes),
    allowedDirsText: (limits.allowed_dirs ?? []).join('\n'),
    allowedPathsText: (limits.allowed_paths ?? []).join('\n'),
    allowWholeFs: !!limits.allow_whole_fs,
    expiresAtLocal: isoToLocalInput(c.expires_at),
    allowedCidrsText: (c.allowed_cidrs ?? []).join('\n'),
    capability: !!c.capability
  }
}

/** 把多行 / 逗号 / 空白分隔的文本拆成去重后的条目。 */
export function splitList(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/[\n,;\s]+/)) {
    const v = raw.trim()
    if (v && !seen.has(v)) {
      seen.add(v)
      out.push(v)
    }
  }
  return out
}

/** 路径可能含空格，只按换行拆分。 */
export function splitLines(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split('\n')) {
    const v = raw.trim()
    if (v && !seen.has(v)) {
      seen.add(v)
      out.push(v)
    }
  }
  return out
}

function parseNonNegativeInt(text: string): number | null {
  const v = text.trim()
  if (v === '') return 0
  if (!/^\d+$/.test(v)) return null
  const n = Number(v)
  return Number.isSafeInteger(n) ? n : null
}

/** 一个令牌最多可以指定的文件允许目录数（与服务端 store.CredentialMaxAllowedPaths 一致）。 */
export const MAX_ALLOWED_PATHS = 20

const MAX_PATH_BYTES = 1024

/**
 * 检查一条文件「允许目录」的写法，返回问题描述；合法返回空串。与服务端 termproto.RootProblem 逐条对应
 * （服务端仍是权威，这里只是让问题在保存之前就能看到）：
 *   合法：`~`、`~/…`（Agent 所在账户的家目录）、`/…`、`C:\…` 或 `C:/…`
 *   不接受：文件系统根、盘符根、UNC / 设备路径、相对路径、`~用户名`、含 `..` 片段、含控制字符。
 */
export function rootProblem(p: string): string {
  if (p === '') return '不能为空'
  if (new TextEncoder().encode(p).length > MAX_PATH_BYTES) return '过长'
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(p) || /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/.test(p)) {
    return '含有非法字符'
  }
  const segs = p.split(/[\\/]+/).filter(seg => seg !== '')
  let meaningful = 0
  for (const seg of segs) {
    if (seg === '..') return '路径里不能出现 .. 片段'
    if (seg !== '.') meaningful++
  }
  if (p === '~' || p.startsWith('~/') || p.startsWith('~\\')) return ''
  if (p.startsWith('~')) return '只支持 ~ 与 ~/…，不支持 ~用户名'
  if (p.startsWith('//') || p.startsWith('\\\\')) return '不支持 UNC 与设备路径'
  if (p.startsWith('/')) return meaningful === 0 ? '不能把文件系统根目录作为允许目录' : ''
  if (/^[A-Za-z]:[\\/]/.test(p)) {
    if (p.slice(2).includes(':')) return '路径里不能出现冒号'
    for (const seg of segs.slice(1)) {
      if (seg !== '.' && (seg.endsWith('.') || seg.endsWith(' '))) return '路径片段不能以点或空格结尾'
    }
    return meaningful <= 1 ? '不能把盘符根目录作为允许目录' : ''
  }
  return '必须是绝对路径：/…、~/… 或 C:\\…'
}

function clipText(s: string, max = 40): string {
  const chars = Array.from(s)
  return chars.length > max ? chars.slice(0, max).join('') + '…' : s
}

/**
 * 文件系统根 / 盘符根这类「整盘」写法被拒绝时给出的改法。
 * 目录框里的根目录永远是被拒绝的（防止手误）；确实要整个文件系统，只能用专门的高风险开关（与服务端 termproto.RootProblem 的提示一致）。
 */
export const ROOT_FIX_HINT = '请改填具体目录，例如 ~/（Agent 账户的家目录）、/home/用户名 或 /srv/应用；确实需要整个文件系统时，请使用「允许整个文件系统」开关'

/** 一条不合法的「允许访问的目录」。 */
export interface PathIssue {
  path: string
  why: string
}

/** 逐行检查「允许访问的目录」，返回所有不合法的条目（空数组表示都合法）。保存之前就能看到问题所在。 */
export function pathIssues(text: string): PathIssue[] {
  const out: PathIssue[] = []
  for (const path of splitLines(text)) {
    const why = rootProblem(path)
    if (why) out.push({ path, why })
  }
  return out
}

/** 一条不合法目录的完整说明；整盘写法会附上改法。 */
export function pathIssueText(i: PathIssue): string {
  const wholeDisk = i.why.startsWith('不能把文件系统根目录') || i.why.startsWith('不能把盘符根目录')
  return `允许访问的目录「${clipText(i.path)}」不合法：${i.why}${wholeDisk ? `（${ROOT_FIX_HINT}）` : ''}`
}

/** 校验表单；返回错误文案，合法时返回空串。服务端仍会再校验一遍。 */
export function validatePolicyForm(f: PolicyForm): string {
  if (tierOf(f.permissions) === 'none') {
    return '请至少选择一项工具权限'
  }
  if (f.permissions.launcher && f.capability) {
    // 网址一旦泄露就等于口令泄露：不能让拿到网址的人去唤醒别的智能体（服务端同样拒绝）。
    return '能力 URL 凭据只能是 ExecMesh 档，不能带「运行预设」'
  }
  if (f.permissions.launcher && !hasGovernance(f.permissions)) {
    // 项目管理 Agent 要靠治理类工具认出自己、指派任务、向你汇报；单独一个「运行预设」没有用，也不该被当成一个完整的令牌。
    return '「运行预设」需要同时开启治理类工具（例如沟通反馈、会话查询）：项目管理 Agent 要靠它们指派任务、向你汇报'
  }
  if (f.capability) {
    // 网址一旦泄露就等于口令泄露，所以能力 URL 凭据的授权面必须尽量窄；服务端会再校验一遍。
    if (hasGovernance(f.permissions) || !f.permissions.terminal) {
      return '能力 URL 凭据只能是 ExecMesh 档：只带终端执行权限，不能带 RelayMesh 治理工具'
    }
    if (f.allHosts) return '能力 URL 凭据不支持「全部主机」，请逐台选择'
    if (f.terminalHosts.length === 0) return '能力 URL 凭据必须逐台选择可用主机'
  }
  if (hasTerminal(f.permissions) && !f.allHosts && f.terminalHosts.length === 0) {
    return '已开启终端权限：请选择可用主机，或选择「全部主机」'
  }
  if (f.permissions.terminal_files) {
    // 文件权限没有「默认全部可访问」：必须明确列出允许的目录，或者明确打开「允许整个文件系统」。
    const paths = splitLines(f.allowedPathsText)
    if (paths.length === 0 && !f.allowWholeFs) {
      return '已开启终端文件权限：请填写允许访问的目录（每行一个），或明确开启「允许整个文件系统」'
    }
    // 开关开着时目录并不生效，但会被原样保留（关闭开关后恢复），所以仍要保证它们是合法的。
    if (paths.length > MAX_ALLOWED_PATHS) return `允许访问的目录最多 ${MAX_ALLOWED_PATHS} 个`
    const issues = pathIssues(f.allowedPathsText)
    if (issues.length > 0) return pathIssueText(issues[0])
  }
  const numeric: [string, string][] = [
    [f.maxConcurrent, '最大并发'],
    [f.maxWaitSeconds, '单次等待上限'],
    [f.maxTimeoutSeconds, '单次运行超时上限'],
    [f.maxOutputBytes, '单次输出上限']
  ]
  for (const [text, label] of numeric) {
    if (parseNonNegativeInt(text) === null) return `${label}必须是不小于 0 的整数（留空表示不限）`
  }
  if (f.expiresAtLocal.trim() && localInputToIso(f.expiresAtLocal) === null) {
    return '到期时间格式不正确'
  }
  return ''
}

/** 表单 → 接口载荷。没有任何终端权限时，终端相关配置一律清空，避免残留配置误导。 */
export function payloadFromForm(f: PolicyForm, opts: { creating?: boolean } = {}): Pick<
  CredentialInput,
  'permissions' | 'terminal_hosts' | 'terminal_mode_cap' | 'terminal_limits' | 'expires_at' | 'allowed_cidrs' | 'capability'
> {
  const terminal = hasTerminal(f.permissions)
  const limits: TerminalLimits = {}
  if (terminal) {
    const set = (key: 'max_concurrent' | 'max_wait_seconds' | 'max_timeout_seconds' | 'max_output_bytes', text: string) => {
      const n = parseNonNegativeInt(text)
      if (n && n > 0) limits[key] = n
    }
    set('max_concurrent', f.maxConcurrent)
    set('max_wait_seconds', f.maxWaitSeconds)
    set('max_timeout_seconds', f.maxTimeoutSeconds)
    set('max_output_bytes', f.maxOutputBytes)
    const dirs = splitLines(f.allowedDirsText)
    if (dirs.length > 0) limits.allowed_dirs = dirs
    // 允许目录与「允许整个文件系统」只对文件权限有意义；没有文件权限时不发送，避免残留配置误导。
    // 开关开着时目录也照常发送并保存：关闭开关后回到的就是管理员原本明确列出的目录，而不是更宽的范围。
    const files = !!f.permissions.terminal_files
    const paths = files ? splitLines(f.allowedPathsText) : []
    if (paths.length > 0) limits.allowed_paths = paths
    if (files && f.allowWholeFs) limits.allow_whole_fs = true
  }
  return {
    // 兼容命名只在有终端执行权限时有意义；服务端也会归一化，这里同样不发送残留的开关。
    permissions: { ...f.permissions, terminal_compat: !!f.permissions.terminal && !!f.permissions.terminal_compat },
    terminal_hosts: terminal ? (f.allHosts ? ['*'] : [...f.terminalHosts]) : [],
    terminal_mode_cap: terminal ? f.modeCap : '',
    terminal_limits: limits,
    expires_at: localInputToIso(f.expiresAtLocal),
    allowed_cidrs: splitList(f.allowedCidrsText),
    // 凭据类型只在创建时决定，之后不可更改：更新载荷里不带该字段。
    ...(opts.creating && f.capability ? { capability: true } : {})
  }
}

/**
 * 这次保存是否「授予」整个文件系统：保存之前没有生效、保存之后生效。
 * 与服务端的判断一致，比较的是「有效状态」（有文件权限且开关打开），而不是只看开关本身——
 * 所以「先关掉文件权限、再单独把文件权限打开」这类绕法在界面上同样会触发重新验证密码。
 * before 为 null 表示新建。
 */
export function grantsWholeFs(before: MCPCredential | null | undefined, after: PolicyForm): boolean {
  const was = !!before && !!before.permissions.terminal_files && !!before.terminal_limits?.allow_whole_fs
  const will = !!after.permissions.terminal_files && after.allowWholeFs
  return will && !was
}

/** 令牌当前是否对主机上的所有文件开放（有文件权限且开关打开）。 */
export function hasWholeFs(c: Pick<MCPCredential, 'permissions' | 'terminal_limits'>): boolean {
  return !!c.permissions.terminal_files && !!c.terminal_limits?.allow_whole_fs
}

/** 令牌列表里「文件范围」的一句话摘要；没有文件权限时返回空串。 */
export function fileScopeSummary(c: Pick<MCPCredential, 'permissions' | 'terminal_limits'>): string {
  if (!c.permissions.terminal_files) return ''
  if (hasWholeFs(c)) return '文件：整个文件系统'
  return `文件目录 ${c.terminal_limits?.allowed_paths?.length ?? 0} 个`
}

/** 应用某个预设到表单（保留已填的访问控制与终端配置，只改权限位）。 */
export function applyMode(f: PolicyForm, mode: PolicyMode): PolicyForm {
  if (mode === 'relaymesh') return { ...f, permissions: relayMeshPreset() }
  if (mode === 'execmesh') {
    // 已经带终端权限时保留「兼容命名」的选择，避免切换预设把它悄悄关掉。
    const keepCompat = !!f.permissions.terminal && !!f.permissions.terminal_compat
    return { ...f, permissions: { ...execMeshPreset(), terminal_compat: keepCompat } }
  }
  return { ...f }
}

/** 能力 URL 凭据的默认有效期（天）：网址等同口令，默认就带一个到期时间，需要长期使用时由人主动清除。 */
export const CAPABILITY_DEFAULT_DAYS = 30

/** 默认到期时间（datetime-local 的值，本地时区）。 */
export function defaultCapabilityExpiry(now: Date = new Date(), days: number = CAPABILITY_DEFAULT_DAYS): string {
  return isoToLocalInput(new Date(now.getTime() + days * 86_400_000).toISOString())
}

/**
 * 打开 / 关闭「能力 URL」接入方式。
 * 打开：权限收窄为 ExecMesh 档、取消「全部主机」、没填到期时间时给默认值；关闭：只取消标记，其余保持。
 */
export function applyCapability(f: PolicyForm, on: boolean, now: Date = new Date()): PolicyForm {
  if (!on) return { ...f, capability: false }
  const base = applyMode(f, 'execmesh')
  return {
    ...base,
    capability: true,
    allHosts: false,
    expiresAtLocal: base.expiresAtLocal.trim() ? base.expiresAtLocal : defaultCapabilityExpiry(now)
  }
}

/** 能力 URL 的完整地址：源（协议 + 主机 + 端口）加服务端返回的路径。 */
export function capabilityUrl(origin: string, path: string): string {
  const base = origin.replace(/\/+$/, '')
  return base + (path.startsWith('/') ? path : '/' + path)
}

export const MODE_CAP_OPTIONS: { value: TerminalModeCap; label: string; hint: string }[] = [
  { value: '', label: '跟随主机', hint: '使用每台主机自己设置的执行模式' },
  { value: 'auto', label: '自动执行', hint: '令牌最多按「自动」执行（主机更严时仍以主机为准）' },
  { value: 'approve', label: '需人工批准', hint: '每条命令都要在 Web 上批准后才会执行' },
  { value: 'readonly', label: '只读', hint: '只能查看主机信息与已有运行，不能执行新命令' }
]

export function modeCapLabel(v: TerminalModeCap | undefined): string {
  return MODE_CAP_OPTIONS.find(o => o.value === (v ?? ''))?.label ?? '跟随主机'
}
