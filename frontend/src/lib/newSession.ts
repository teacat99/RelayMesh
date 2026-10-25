// 网页手动新建会话（#174 第一期）的纯逻辑。
//
// 流程：用户在侧栏点「新建会话」→ 填名字 → 服务端落一行占位会话（notice，已完成、已被消费）并返回启动提示词 →
// 用户把提示词发给要使用的智能体 → 智能体第一次调用 RelayMesh 时带上这个 workflow_id，就接进这个会话。
// 在智能体接入之前，这个工作流只有那一行占位会话，侧栏与页头把它标成「待接入」。
//
// 占位会话的识别：会话编号以 sess-web- 开头（与服务端 store.WebPlaceholderSessionPrefix 一致）。

import type { CreateWorkflowApplied, CreateWorkflowRequest, CreateWorkflowWarning } from '../api/client'
import type { FeedbackSession } from '../api/types'
import { AWAITING_AGENT_ROUND } from './sessionStatus'
import { RESIDENT_TEMPLATES, templateText, type PromptTemplateOverrides, type ResidentTemplateId } from './residentPrompt'

export { AWAITING_AGENT_ROUND }

export const WEB_PLACEHOLDER_SESSION_PREFIX = 'sess-web-'

/** 名字的最大字符数（按字符，不是字节；与服务端 WebWorkflowTitleMaxRunes 一致）。 */
export const SESSION_NAME_MAX = 80

type SessionLike = Pick<FeedbackSession, 'session_id'> | { session_id?: string } | null | undefined

/** 这一行是不是网页新建会话时落下的占位会话。 */
export function isWebPlaceholder(session: SessionLike): boolean {
  const id = session?.session_id
  return typeof id === 'string' && id.startsWith(WEB_PLACEHOLDER_SESSION_PREFIX)
}

/**
 * 工作流的「轮次状态」：已经是最近一行占位会话（智能体还没接入）且没有归档 / 待答复时，翻译成 AWAITING_AGENT_ROUND；
 * 其余原样返回（pending / completed / cancelled / timeout / archived …）。
 * latest 是这个工作流创建时间最新的一行；status 是侧栏卡上的轮次状态（有待答复的一轮为 pending，否则是 latest 的状态）。
 */
export function workflowRoundStatus(latest: SessionLike, status: string): string {
  return status === 'completed' && isWebPlaceholder(latest) ? AWAITING_AGENT_ROUND : status
}

/** 工作流里的一行会话：判断「待接入」只需要这几个字段。 */
type WorkflowRow = { workflow_id?: string | null; created_at: string; session_id?: string; status?: string }

/**
 * 当前会话所在工作流里创建时间最新的一行（没有工作流编号就是它自己）。
 * 「待接入」只在最新一行还是占位会话时成立：智能体接入后，页头停在占位会话那一行也不该再说待接入。
 * pool 是已经加载的会话（可以包含别的工作流的行，只看同一个工作流的）。
 */
export function latestWorkflowRow<T extends WorkflowRow>(current: T | null | undefined, pool: readonly T[]): T | null {
  if (!current) return null
  const wf = current.workflow_id
  if (!wf) return current
  let latest = current
  for (const s of pool) {
    if (s.workflow_id === wf && Date.parse(s.created_at) > Date.parse(latest.created_at)) latest = s
  }
  return latest
}

/** 这个会话现在是不是「待接入」：与页头状态徽标、侧栏会话卡同一口径（最新一行是占位会话、且没有归档 / 待答复）。 */
export function isAwaitingAgent<T extends WorkflowRow>(current: T | null | undefined, pool: readonly T[]): boolean {
  if (!current) return false
  return workflowRoundStatus(latestWorkflowRow(current, pool), current.status ?? '') === AWAITING_AGENT_ROUND
}

export type SessionNameCheck = { ok: true; name: string } | { ok: false; message: string }

/**
 * 校验并规整用户填的名字（与服务端 NormalizeWebWorkflowTitle 同一口径）：
 * 空白与控制字符折成一个空格，零宽 / 不可见格式字符丢掉，去首尾空白；规整后为空或超长则不通过。
 * 服务端仍会再校验一次，这里只是让用户在点「创建」之前就看到原因。
 */
export function checkSessionName(raw: string): SessionNameCheck {
  // 控制字符与各种空白折成空格；\u200b-\u200f \u2060 \ufeff 是零宽 / 方向标记等不可见字符，直接丢掉
  const name = raw
    .replace(/[\u200b-\u200f\u2060\ufeff]/g, '')
    .replace(/[\s\u0000-\u001f\u007f-\u009f]+/g, ' ')
    .trim()
  if (!name) return { ok: false, message: '请填写会话名称' }
  const length = Array.from(name).length
  if (length > SESSION_NAME_MAX) {
    return { ok: false, message: `名称太长了（${length} / ${SESSION_NAME_MAX} 个字符）` }
  }
  return { ok: true, name }
}

// ───────── 高级选项（#184）─────────
//
// 名字之外还有四项可选信息，放在弹窗里默认收起的「高级选项」里：自己指定的工作流编号、首条指令、起始阶段、项目目录与主机。
// 规则与服务端（internal/store/web_workflow.go 的 Normalize* 与 webWorkflowIDPattern）同一口径；服务端仍会再校验一次，
// 这里只是让用户在点「创建」之前就看到原因。填了又清空等于没填：buildRequest 只带非空的项。

/** 自己指定的工作流编号的规则（与服务端 webWorkflowIDPattern 一致）：字母或数字开头，后面是字母 / 数字 / . _ -，共 3～64 个字符。 */
export const WORKFLOW_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$/
export const WORKFLOW_ID_MIN = 3
export const WORKFLOW_ID_MAX = 64
/** 首条指令 / 项目目录 / 主机名的最大字符数（与服务端 WebWorkflowFirstMessageMaxRunes / ProjectDirMaxRunes / HostNameMaxRunes 一致）。 */
export const FIRST_MESSAGE_MAX = 20000
export const PROJECT_DIR_MAX = 1024
export const HOST_NAME_MAX = 120

export type NewSessionPhase = 'discuss' | 'plan' | 'dev'
/** '' = 不指定，沿用默认阶段。 */
export type PhaseChoice = '' | NewSessionPhase

/** 起始阶段的选项（顺序即界面顺序）。 */
export const NEW_SESSION_PHASES: ReadonlyArray<{ id: PhaseChoice; label: string; hint: string }> = [
  { id: '', label: '不指定', hint: '沿用默认阶段' },
  { id: 'discuss', label: '讨论', hint: '只读：先沟通信息，不改任何文件' },
  { id: 'plan', label: '方案', hint: '只读：先给出可拍板的方案' },
  { id: 'dev', label: '开发', hint: '按已定的方案动手实现' },
]

/** '' = 不套用常驻规则模板。 */
export type ResidentChoice = '' | ResidentTemplateId

/** 常驻规则的选项（顺序即界面顺序）：不套用 + 内置模板。 */
export const NEW_SESSION_RESIDENTS: ReadonlyArray<{ id: ResidentChoice; label: string; hint: string }> = [
  { id: '', label: '不套用', hint: '建好之后想要再在会话输入区的「常驻」里写' },
  ...RESIDENT_TEMPLATES.map(t => ({ id: t.id as ResidentChoice, label: t.label, hint: t.hint })),
]

/** 弹窗里填的全部内容（输入框里的原样文本）。 */
export interface NewSessionDraft {
  name: string
  workflowId: string
  firstMessage: string
  phase: PhaseChoice
  projectDirectory: string
  hostName: string
  resident: ResidentChoice
}

export function blankNewSessionDraft(): NewSessionDraft {
  return { name: '', workflowId: '', firstMessage: '', phase: '', projectDirectory: '', hostName: '', resident: '' }
}

export type FieldCheck = { ok: true; value: string } | { ok: false; message: string }
const okValue = (value: string): FieldCheck => ({ ok: true, value })
const bad = (message: string): FieldCheck => ({ ok: false, message })
const charLength = (s: string) => Array.from(s).length

/** 工作流编号：空 = 自动生成（value 为空串）；否则必须符合 WORKFLOW_ID_PATTERN。 */
export function checkWorkflowId(raw: string): FieldCheck {
  const v = raw.trim()
  if (!v) return okValue('')
  if (WORKFLOW_ID_PATTERN.test(v)) return okValue(v)
  const n = charLength(v)
  if (n < WORKFLOW_ID_MIN) return bad(`编号太短了（${n} / 至少 ${WORKFLOW_ID_MIN} 个字符）`)
  if (n > WORKFLOW_ID_MAX) return bad(`编号太长了（${n} / 最多 ${WORKFLOW_ID_MAX} 个字符）`)
  return bad('编号只能用字母、数字和 . _ -，并以字母或数字开头')
}

/** 首条指令：空 = 没有；去首尾空白，正文里的换行原样保留。 */
export function checkFirstMessage(raw: string): FieldCheck {
  const v = raw.trim()
  if (!v) return okValue('')
  if (v.includes('\u0000')) return bad('内容里有不能保存的字符（NUL）')
  const n = charLength(v)
  if (n > FIRST_MESSAGE_MAX) return bad(`首条指令太长了（${n} / ${FIRST_MESSAGE_MAX} 个字符）`)
  return okValue(v)
}

/**
 * 项目目录：空 = 不绑定；否则必须是绝对路径——以 / 开头、C:\ 或 C:/ 这样的盘符路径、或 \\服务器\共享。
 * 填了它，这个会话从此绑定该目录（D-161）：智能体第一次汇报带的目录与它不一致就会被拒绝，所以界面上要把这个后果写清楚。
 */
export function checkProjectDirectory(raw: string): FieldCheck {
  const v = raw.trim()
  if (!v) return okValue('')
  const n = charLength(v)
  if (n > PROJECT_DIR_MAX) return bad(`目录太长了（${n} / ${PROJECT_DIR_MAX} 个字符）`)
  if (/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2060\ufeff]/.test(v)) return bad('目录里不能有换行、制表或不可见字符')
  if (v.includes('```')) return bad('目录里不能有连续三个反引号')
  const absolute = v.startsWith('/') || /^[A-Za-z]:[\\/]/.test(v) || /^\\\\[^\\/]+[\\/][^\\/]+/.test(v)
  if (!absolute) return bad('请填绝对路径：以 / 开头，或 C:\\ 这样的盘符路径，或 \\\\服务器\\共享')
  return okValue(v)
}

/** 主机名（会话的归属标签）：空 = 不指定；空白折成一个空格。 */
export function checkHostName(raw: string): FieldCheck {
  const v = raw
    .replace(/[\u200b-\u200f\u2060\ufeff]/g, '')
    .replace(/[\s\u0000-\u001f\u007f-\u009f]+/g, ' ')
    .trim()
  const n = charLength(v)
  if (n > HOST_NAME_MAX) return bad(`主机名太长了（${n} / ${HOST_NAME_MAX} 个字符）`)
  return okValue(v)
}

export type NewSessionErrors = Partial<Record<'name' | 'workflowId' | 'firstMessage' | 'projectDirectory' | 'hostName', string>>

export type NewSessionValidation = { ok: true; request: CreateWorkflowRequest } | { ok: false; errors: NewSessionErrors }

/**
 * 校验整份草稿，通过时给出要发给服务端的请求体：名字必填；高级选项只带非空的项（一项也没填 = 只有 title，与改动前的请求一致）。
 * 不通过时按字段给出原因（界面把它显示在对应输入框下面）。
 */
export function buildCreateRequest(draft: NewSessionDraft, templates?: PromptTemplateOverrides | null): NewSessionValidation {
  const errors: NewSessionErrors = {}
  const name = checkSessionName(draft.name)
  if (!name.ok) errors.name = name.message
  const wf = checkWorkflowId(draft.workflowId)
  if (!wf.ok) errors.workflowId = wf.message
  const first = checkFirstMessage(draft.firstMessage)
  if (!first.ok) errors.firstMessage = first.message
  const dir = checkProjectDirectory(draft.projectDirectory)
  if (!dir.ok) errors.projectDirectory = dir.message
  const host = checkHostName(draft.hostName)
  if (!host.ok) errors.hostName = host.message
  if (!name.ok || !wf.ok || !first.ok || !dir.ok || !host.ok) return { ok: false, errors }

  const request: CreateWorkflowRequest = { title: name.name }
  if (wf.value) request.workflow_id = wf.value
  if (first.value) request.first_message = first.value
  if (draft.phase) request.phase = draft.phase
  if (dir.value) request.project_directory = dir.value
  if (host.value) request.host_name = host.value
  // 常驻规则：按「当前生效的模板文字」（用户在设置里覆盖过的优先）复制进新会话，之后与模板再无关系
  if (draft.resident) request.resident = templateText(draft.resident, templates)
  return { ok: true, request }
}

/** 高级选项里有没有填任何东西（决定「高级选项」折叠条上要不要提示「已填 N 项」）。 */
export function advancedFilledCount(draft: NewSessionDraft): number {
  return [draft.workflowId, draft.firstMessage, draft.phase, draft.projectDirectory, draft.hostName, draft.resident].filter(v => v.trim() !== '').length
}

/** 阶段编号翻成界面上的名字（认不出的原样返回）。 */
export function phaseLabel(id: string): string {
  return NEW_SESSION_PHASES.find(p => p.id === id)?.label ?? id
}

/** 建好之后给用户看的「实际生效了什么」：服务端 applied 里有几项就几行，一项也没有就是空数组。 */
export function appliedLines(applied: CreateWorkflowApplied | undefined | null): string[] {
  if (!applied) return []
  const lines: string[] = []
  if (applied.phase) lines.push(`起始阶段：${phaseLabel(applied.phase)}`)
  if (applied.first_message) lines.push('首条指令已排进收件箱，智能体接入后会读到')
  if (applied.project_directory) lines.push(`项目目录：${applied.project_directory}（已绑定：智能体第一次汇报必须带这个目录，换目录会被拒绝）`)
  if (applied.host_name) lines.push(`主机：${applied.host_name}`)
  if (applied.resident) lines.push(`常驻规则：已套用（${applied.resident} 字），智能体每次回执都会带上它；可在会话输入区的「常驻」里修改`)
  return lines
}

const WARNING_FIELD_TEXT: Record<string, string> = {
  phase: '起始阶段没有设上',
  first_message: '首条指令没有发出去',
  resident: '常驻规则没有存上',
}
const WARNING_FIELD_FIX: Record<string, string> = {
  phase: '可以在会话页里用阶段滑块再选一次',
  first_message: '可以在会话页下方的输入框里重新写',
  resident: '可以在会话页输入区的「常驻」里再套用一次',
}

/** 会话已经建好、但有事没做成时的提示：每条写明哪一项、为什么、怎么补。 */
export function warningLines(warnings: readonly CreateWorkflowWarning[] | undefined | null): string[] {
  if (!warnings?.length) return []
  return warnings.map(w => {
    const what = WARNING_FIELD_TEXT[w.field] ?? `「${w.field}」没有生效`
    const fix = WARNING_FIELD_FIX[w.field]
    return `${what}：${w.error}${fix ? `。${fix}。` : ''}`
  })
}

type SuggestRow = { host_name?: string | null; project_directory?: string | null; updated_at?: string | null; created_at?: string | null }

/**
 * 主机与项目目录的候选：取自已经加载的会话里出现过的值，最近用过的排前面，各自去重、最多 limit 个。
 * 纯前端推导，只是让用户少打几个字（输入框仍然可以填任何值）。
 */
export function collectNewSessionSuggestions(rows: readonly SuggestRow[], limit = 12): { hosts: string[]; dirs: string[] } {
  const stamp = (r: SuggestRow) => Date.parse(r.updated_at || r.created_at || '') || 0
  const sorted = [...rows].sort((a, b) => stamp(b) - stamp(a))
  const hosts = new Set<string>()
  const dirs = new Set<string>()
  for (const r of sorted) {
    const h = (r.host_name ?? '').trim()
    if (h) hosts.add(h)
    const d = (r.project_directory ?? '').trim()
    if (d) dirs.add(d)
    if (hosts.size >= limit && dirs.size >= limit) break
  }
  return { hosts: [...hosts].slice(0, limit), dirs: [...dirs].slice(0, limit) }
}
