import type {
  HeadlessPosture,
  SessionEnableHost,
  SessionEnablePreview,
  SessionEnableRequest,
  SessionEnableResult,
  TerminalPrefsLimits,
} from '../../api/terminal'
import { hostTitle, runLimitText } from './format'
import {
  blankRunDraft,
  charCount,
  collapseSpaces,
  isWindowsHost,
  launcherLimits,
  parseMaxRun,
  validateRunOptions,
  type DraftErrors,
  type LauncherLimits,
  type RunLimitUnit,
} from './launcher'

// 会话页「启用 Agent」（#176 第三期）弹窗的纯逻辑：弹窗的草稿与默认值、校验、请求体、主机候选、成功提示，以及记住上一次填的主机与目录。
//
// 「启用 Agent」= 为一个「待接入」的网页会话，在某台主机的某个目录里无人值守地新开一个 Cursor CLI 对话（agent -p …），
// 它按提示词带着这个工作流编号接入，之后这个会话就和别的智能体会话一样了。服务端把它做成「建（或复用）一条续聊预设 + 运行它」，
// 所有闸门与防重复（已有智能体在运行、上一次运行没结束……）都在服务端；这里只负责把选项收拢、让用户在点之前看清楚会发生什么。

/**
 * 弹窗预先填的值（Q43 b）：权限姿态默认放开全部命令（--force）——无人值守的新对话里没有人能点「允许」，白名单姿态下它多半寸步难行；
 * 但这是人在弹窗里看着红字警告、亲手确认的默认，不是接口的默认（接口不填 = 白名单）。最长运行 24 小时，自动续跑默认关闭。
 */
export const ENABLE_DEFAULT_POSTURE: HeadlessPosture = 'force'
export const ENABLE_DEFAULT_MAX_RUN = { value: '24', unit: 'hour' as RunLimitUnit }

/** 弹窗里的草稿（输入框里的原样文本）。对话编号不在这里：「启用」永远是新开对话，要接着已有的对话请用预设的「固定对话编号」。 */
export interface EnableDraft {
  host: string
  dir: string
  model: string
  posture: HeadlessPosture
  maxRun: string
  maxRunUnit: RunLimitUnit
  autoResume: boolean
  /** 高级：可执行文件名（留空 = agent）与自定义提示词（留空 = 默认的「新开对话」提示词）。 */
  bin: string
  prompt: string
}

/** 记住的上一次填写：只记主机、目录、模型与可执行文件——权限姿态、时限这类带风险的选项每次都从默认值开始，让人再看一眼。 */
export interface EnableMemory {
  host?: string
  dir?: string
  model?: string
  bin?: string
}

export const ENABLE_MEMORY_KEY = 'relaymesh.enableAgent.last'

type StorageRead = Pick<Storage, 'getItem'>
type StorageWrite = Pick<Storage, 'setItem'>

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

/** 读上一次填的值；存储不可用、内容坏了都当没有。 */
export function loadEnableMemory(storage: StorageRead | null | undefined): EnableMemory {
  if (!storage) return {}
  try {
    const raw = storage.getItem(ENABLE_MEMORY_KEY)
    if (!raw) return {}
    const v = JSON.parse(raw) as Record<string, unknown> | null
    if (!v || typeof v !== 'object') return {}
    return { host: str(v.host), dir: str(v.dir), model: str(v.model), bin: str(v.bin) }
  } catch {
    return {}
  }
}

/** 启用成功之后记下这次用的主机、目录等，下次打开弹窗直接预填。存储满了或被禁用时静默放弃。 */
export function saveEnableMemory(storage: StorageWrite | null | undefined, d: EnableDraft): void {
  if (!storage) return
  const mem: EnableMemory = { host: d.host.trim(), dir: d.dir.trim(), model: d.model.trim(), bin: d.bin.trim() }
  try {
    storage.setItem(ENABLE_MEMORY_KEY, JSON.stringify(mem))
  } catch {
    /* 记不住不影响启用 */
  }
}

/** 打开弹窗时的草稿：默认值 + 记住的主机 / 目录 / 模型 / 可执行文件。 */
export function blankEnableDraft(memory?: EnableMemory | null): EnableDraft {
  return {
    host: memory?.host?.trim() ?? '',
    dir: memory?.dir?.trim() ?? '',
    model: memory?.model?.trim() ?? '',
    posture: ENABLE_DEFAULT_POSTURE,
    maxRun: ENABLE_DEFAULT_MAX_RUN.value,
    maxRunUnit: ENABLE_DEFAULT_MAX_RUN.unit,
    autoResume: false,
    bin: memory?.bin?.trim() ?? '',
    prompt: '',
  }
}

/** 弹窗里的错误按字段给：字段名与规则沿用续聊预设表单（host / dir / model / bin / prompt / maxRun）。 */
export type EnableErrors = DraftErrors

/**
 * 校验草稿：目录（主机上的绝对路径）、模型、可执行文件、提示词与最长运行时间，规则与续聊预设表单、服务端完全一致
 * （借用预设表单的校验，工作流编号填弹窗所在的这个工作流）。主机要选一台；Windows 主机选了也给出原因（服务端同样会拒绝）。
 */
export function validateEnableDraft(
  workflowId: string,
  d: EnableDraft,
  limits: LauncherLimits,
  hosts: readonly SessionEnableHost[] = [],
): EnableErrors {
  const err: EnableErrors = validateRunOptions(
    { ...blankRunDraft(), bin: d.bin, model: d.model, posture: d.posture, workflow: workflowId, prompt: d.prompt, maxRun: d.maxRun, maxRunUnit: d.maxRunUnit, autoResume: d.autoResume, newConversation: true },
    limits,
  )
  const dir = d.dir.trim()
  if (!dir) err.dir = '请填写目录：要在这台主机的哪个目录里运行（--workspace，主机上的绝对路径）'
  else if (/[\u0000-\u001f\u007f-\u009f]/.test(dir)) err.dir = '目录里不能有换行或控制字符'
  else if (!dir.startsWith('/')) err.dir = '目录必须是主机上的绝对路径（以 / 开头）'
  else if (charCount(dir) > limits.dir) err.dir = `目录最长 ${limits.dir} 个字，现在是 ${charCount(dir)} 个字`

  const host = d.host.trim()
  if (!host) err.host = '请选择要在哪台主机上运行'
  else {
    const chosen = hosts.find(h => h.machine_code === host)
    if (chosen && isWindowsHost(chosen.os ?? '')) err.host = '「启用 Agent」暂不支持 Windows 主机，请换一台 Linux / macOS 主机'
  }
  return err
}

export const hasEnableErrors = (e: EnableErrors) => Object.keys(e).length > 0

/** 草稿 → 提交给服务端的请求：权限姿态总是显式给出（接口不替人放宽或收紧）；空字段不带。 */
export function toEnableRequest(workflowId: string, d: EnableDraft): SessionEnableRequest {
  const req: SessionEnableRequest = { workflow_id: workflowId.trim(), dir: d.dir.trim(), posture: d.posture }
  const host = d.host.trim()
  const model = d.model.trim()
  const bin = d.bin.trim()
  const prompt = collapseSpaces(d.prompt)
  if (host) req.host = host
  if (model) req.model = model
  if (bin) req.bin = bin
  if (prompt) req.prompt = prompt
  const maxRun = parseMaxRun(d.maxRun, d.maxRunUnit)
  if (maxRun !== null && Number.isFinite(maxRun) && maxRun >= 1) req.max_run_minutes = maxRun
  if (d.autoResume) req.auto_resume = true
  return req
}

/** 「启用 Agent」的功能开关：Hub 的预设上限里带了「新开对话」默认提示词才认识它（旧版 Hub 没有）。 */
export function enableSupported(limits?: Partial<TerminalPrefsLimits> | null): boolean {
  return launcherLimits(limits).runNewPromptDefault !== ''
}

// ---------- 主机候选 ----------

export interface EnableHostChoice {
  code: string
  /** 下拉框里的文字：名字 + 系统 + 状态。 */
  label: string
  /** 不能选的原因（Windows、离线）；能选为空。离线的主机仍可选——预览会把「主机离线」写成原因——所以只有 Windows 是 disabled。 */
  disabled: boolean
  note: string
}

/** 主机候选：在线的在前，Windows 主机列出来但不能选（说明原因），离线的标出来。 */
export function enableHostChoices(hosts: readonly SessionEnableHost[] | null | undefined): EnableHostChoice[] {
  const list = [...(hosts ?? [])].sort((a, b) => Number(isWindowsHost(a.os ?? '')) - Number(isWindowsHost(b.os ?? '')) || Number(b.online) - Number(a.online))
  return list.map(h => {
    const win = isWindowsHost(h.os ?? '')
    const note = win ? 'Windows 暂不支持' : h.online ? '' : '离线'
    const os = (h.os ?? '').trim()
    return {
      code: h.machine_code,
      label: `${hostTitle(h)}${os ? ` · ${os}` : ''}${note ? `（${note}）` : ''}`,
      disabled: win,
      note,
    }
  })
}

/**
 * 弹窗打开（或候选更新）时默认选哪台主机：当前已选且仍可选就不动；其次是上一次用的；其次是服务端自动选定的（只有一台可选）；
 * 否则留空让用户自己选，不替他猜。
 */
export function pickEnableHost(current: string, remembered: string | undefined, serverPick: string | undefined, hosts: readonly SessionEnableHost[] | null | undefined): string {
  const usable = new Set(enableHostChoices(hosts).filter(c => !c.disabled).map(c => c.code))
  for (const c of [current, remembered ?? '', serverPick ?? '']) {
    const code = c.trim()
    if (code && usable.has(code)) return code
  }
  return ''
}

/**
 * 运行目录该显示什么（#184）：会话在网页里绑定了项目目录（preview.bound_dir）时，智能体第一次汇报必须带这个目录（D-161），
 * 所以用户没有亲手改过目录输入框、或把它清空了，就用绑定的目录——不管框里原来是空的还是「上一次记住的」别的目录；
 * 用户亲手填的不动（与绑定目录不一致时，由服务端预览报 dir_mismatch，让用户看到原因）。没绑定目录时原样返回当前值。
 */
export function pickEnableDir(current: string, edited: boolean, bound: string | undefined): string {
  const b = (bound ?? '').trim()
  if (b && (!edited || current.trim() === '')) return b
  return current
}

// ---------- 弹窗里的说明与提示 ----------

/** 权限姿态的风险说明：放开全部命令时给红字警告；白名单没有额外警告（服务端预览的 warnings 会说明它的前提）。 */
export function postureWarning(p: HeadlessPosture): string {
  return p === 'force'
    ? '放开全部命令（--force）：这个无人值守的智能体可以不经询问地在这台主机上运行任何命令、改任意文件，没有人会在运行时点「允许」。只在你完全信任这个目录与这台主机时使用；想收紧就改选白名单。'
    : ''
}

/** 一句话说清这次会做什么（弹窗顶部与确认前的总结）。 */
export function enableSummary(d: Pick<EnableDraft, 'posture' | 'maxRun' | 'maxRunUnit' | 'autoResume'>, hostLabel: string): string {
  const maxMin = parseMaxRun(d.maxRun, d.maxRunUnit)
  const limit = maxMin !== null && Number.isFinite(maxMin) && maxMin >= 1 ? runLimitText(maxMin * 60) : ''
  const parts = [
    hostLabel ? `在「${hostLabel}」上新开一个 Cursor CLI 对话` : '在所选主机上新开一个 Cursor CLI 对话',
    d.posture === 'force' ? '放开全部命令' : '只放行白名单内的操作',
    limit ? `最长运行 ${limit}` : '',
    d.autoResume ? '异常退出自动续跑' : '',
  ].filter(Boolean)
  return parts.join(' · ')
}

/** 预览里「上一次启用」的一句话：只在之前启用过、且那次运行还在进行时提醒（防重复由服务端拒绝，这里让人看懂为什么）。 */
export function previousRunNote(pv: Pick<SessionEnablePreview, 'preset' | 'last_run'> | null | undefined): string {
  if (!pv?.preset) return ''
  if (pv.last_run?.active) return `之前已经为这个会话启用过（预设「${pv.preset.name}」），那次运行还在进行，要等它结束才能再启用。`
  return `之前已经为这个会话启用过（预设「${pv.preset.name}」）：再次启用会复用这条预设，不会再多建一条；对话已经建立的话，这次是接着它而不是再新开。`
}

/** 启用成功之后弹出的提示。 */
export function enableNotice(r: Pick<SessionEnableResult, 'run' | 'preset'>): { title: string; description: string } {
  const lines: string[] = []
  if (r.run.run_state === 'awaiting_approval') {
    lines.push('这台主机的执行模式是「需批准」：到 ExecMesh 的运行记录里批准后才会开始。')
  } else if (r.run.new_conversation) {
    lines.push('在主机上新开 Cursor CLI 对话中：智能体接入后，这个会话会从「待接入」变成它的会话；之后点「运行」都是接着这个对话，不会再新开。')
  } else {
    lines.push('主机上已有这个会话的对话：这次是接着它运行，不是新开。')
  }
  lines.push(`预设「${r.preset.name}」可以在启动器里查看运行结果。`)
  for (const w of r.run.warnings ?? []) lines.push(w)
  return {
    title: r.run.run_state === 'awaiting_approval' ? '启用 Agent：等待批准' : '已启用 Agent',
    description: lines.join('\n'),
  }
}
