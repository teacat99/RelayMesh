// 项目知识库的前端口径（#114）：只放纯逻辑，组件只负责摆放。

import type { ACLMode, AclGrant, AuditEvent, ProjectModule, KbDoc } from '../api/projects'
import type { ProjectDoc } from '../api/projectDocs'
import type { AgentIdentity } from './agentState'

export const ACL_MODES: ACLMode[] = ['off', 'audit', 'enforce']

export function aclModeLabel(mode: string): string {
  switch (mode) {
    case 'off':
      return '关闭'
    case 'audit':
      return '仅审计'
    case 'enforce':
      return '强制执行'
    default:
      return mode || '关闭'
  }
}

export function aclModeHint(mode: string): string {
  switch (mode) {
    case 'audit':
      return '不拦截任何调用；只把「若强制执行会被拒绝」的调用记进审计。建议先观察一段时间。'
    case 'enforce':
      return '真正拦截：别人负责范围的写入、没有身份声明的写入、跨项目读文档与收件箱都会被拒绝。只有你能切到这一档。'
    default:
      return '不做任何校验，行为和没有项目时一样。'
  }
}

/** 切到强制执行前要给用户看的提醒。 */
export const ENFORCE_WARNING =
  '强制执行后，没有在调用里声明身份（project_directory + host_name）的 Agent 写入会被拒绝。请先在「审计」里确认「若强制执行会被拒绝」的记录已经很少。'

// ---------------------------------------------------------------------------
// 模块树
// ---------------------------------------------------------------------------

export interface ModuleNode {
  module: ProjectModule
  depth: number
  /** 路径最后一段 */
  name: string
}

/** 按路径排序后的平铺树（父在前、子在后），带缩进深度。 */
export function flattenModules(modules: ProjectModule[]): ModuleNode[] {
  return [...modules]
    .sort((a, b) => a.path.localeCompare(b.path))
    .map(m => {
      const segs = m.path.split('/')
      return { module: m, depth: segs.length - 1, name: segs[segs.length - 1] }
    })
}

/** 新建模块时可选的父路径（空 = 顶层）。 */
export function parentCandidates(modules: ProjectModule[]): string[] {
  return ['', ...modules.map(m => m.path).sort()]
}

export function joinModulePath(parent: string, name: string): string {
  const n = name.trim().replace(/^\/+|\/+$/g, '')
  return parent ? `${parent}/${n}` : n
}

/** 模块路径只允许小写字母、数字、连字符，层级用 /（与服务端 NormalizeModulePath 一致的前端预检）。 */
export function validModuleSegment(name: string): boolean {
  return /^[a-z0-9][a-z0-9-]{0,40}$/.test(name.trim())
}

// ---------------------------------------------------------------------------
// 迁移
// ---------------------------------------------------------------------------

export function moveReasonLabel(reason: string): string {
  switch (reason) {
    case 'sheet':
      return '会话工作表归到 sessions/<工作流>/'
    case 'conflict':
      return '与其他工作流的路径撞了，改放到 legacy/<工作流>/'
    default:
      return reason
  }
}

// ---------------------------------------------------------------------------
// 授权
// ---------------------------------------------------------------------------

export function grantRoleLabel(role: string): string {
  switch (role) {
    case 'read':
      return '只读'
    case 'comment':
      return '可评论'
    case 'edit':
      return '可编辑'
    case 'manage':
      return '可管理'
    default:
      return role
  }
}

export function grantScopeLabel(kind: string, ref: string): string {
  switch (kind) {
    case 'project':
      return '整个项目'
    case 'module':
      return `模块 ${ref}`
    case 'doc':
      return `文档 ${ref}`
    case 'workflow':
      return `工作流 ${ref}`
    case 'tasks':
      return '任务管理（派任务、编辑所有任务）'
    default:
      return `${kind} ${ref}`
  }
}

export function grantExpiryLabel(expiresAt: string | null | undefined, now = Date.now()): string {
  if (!expiresAt) return '长期有效'
  const t = new Date(expiresAt).getTime()
  if (Number.isNaN(t)) return '长期有效'
  const left = t - now
  if (left <= 0) return '已过期'
  const h = Math.floor(left / 3600_000)
  if (h >= 48) return `${Math.floor(h / 24)} 天后过期`
  if (h >= 1) return `${h} 小时后过期`
  return `${Math.max(1, Math.floor(left / 60_000))} 分钟后过期`
}

export function grantActive(g: AclGrant, now = Date.now()): boolean {
  if (g.revoked_at) return false
  if (g.expires_at && new Date(g.expires_at).getTime() <= now) return false
  return true
}

// ---------------------------------------------------------------------------
// 任务管理（#175 B2c）：谁是项目的管理 Agent
// ---------------------------------------------------------------------------

/** 一个身份在项目里「管理 Agent」的状态。 */
export interface TaskManagerView {
  identityId: number
  /** 用户在「任务管理」里勾选产生的授权编号（scope=tasks）；没有就是 null */
  designatedGrantId: number | null
  /** 另有整个项目的编辑（或管理）授权：它同样让对方成为管理 Agent，不能在「任务管理」里取消 */
  inherited: boolean
  /** 现在是不是管理 Agent（任一来源都算） */
  manager: boolean
}

/** 按授权列表算某个身份的管理 Agent 状态。群授权前端不知道成员，不在这里推算。 */
export function taskManagerView(grants: AclGrant[], identityId: number, now = Date.now()): TaskManagerView {
  let designated: number | null = null
  let inherited = false
  for (const g of grants) {
    if (g.principal_kind !== 'identity' || Number(g.principal_ref) !== identityId || !grantActive(g, now)) continue
    if (g.scope_kind === 'tasks') {
      if (designated === null) designated = g.id
    } else if (g.scope_kind === 'project' && (g.role === 'edit' || g.role === 'manage')) {
      inherited = true
    }
  }
  return { identityId, designatedGrantId: designated, inherited, manager: designated !== null || inherited }
}

/**
 * 「任务管理」里列出哪些身份：目录归在这个项目里的 Agent，加上已被指定、但目录后来被移出项目的（让用户还能取消）。
 * 按显示名排序，同名按编号。
 */
export function taskManagerCandidates(
  identities: AgentIdentity[],
  dirs: { host_name: string; project_directory: string }[],
  grants: AclGrant[],
  now = Date.now(),
): AgentIdentity[] {
  const inProject = new Set(dirs.map(d => `${d.host_name}\u0000${d.project_directory}`))
  return identities
    .filter(i => inProject.has(`${i.host_name}\u0000${i.project_directory}`) || taskManagerView(grants, i.id, now).designatedGrantId !== null)
    .sort((a, b) => (a.label || '').localeCompare(b.label || '', 'zh-CN') || a.id - b.id)
}

/** 项目的访问控制档位决定「任务管理」这套规则现在有多大的效力。 */
export function taskManagerModeNote(mode: string): string {
  switch (mode) {
    case 'enforce':
      return '强制执行档：规则已生效，只有任务的创建者、承接者和管理 Agent 能改对应的任务。'
    case 'audit':
      return '仅审计档：规则只记录、不拦截——超出规则的写入照常执行，并在审计页标「强制执行会拒绝」。'
    default:
      return '访问控制已关闭：任务写入不做任何校验，指定管理 Agent 暂时不起作用。'
  }
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

const ACTION_LABELS: Record<string, string> = {
  'doc.write': '写入文档',
  'doc.delete': '删除文档',
  'doc.read.cross': '跨范围读取',
  'write.cross': '跨工作流写入',
  grant: '授权',
  revoke: '撤销授权',
  // 同一个动作名下有两种事：角色不够，和调用没声明身份；列表里按每条记录的原因分开叫（见 auditEventLabel）。
  'acl.denied': '权限不足 / 未声明身份',
  'artifact.data_write': '写入 Artifact 数据',
  'group.create': '建群',
  'group.member_add': '群加人',
  'group.member_remove': '群移人',
  'group.cooling': '群冷却',
  'wake.manual': '手动唤醒',
  'launcher.run': '运行预设',
  'identity.rename': '身份改名',
  'identity.disable': '停用身份',
  'project.create': '创建项目',
  'project.update': '修改项目',
  'project.dir_add': '归入目录',
  'project.dir_remove': '移出目录',
  'project.migrate': '迁移文档',
  'module.write': '修改模块',
}

export function auditActionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

export const AUDIT_ACTION_OPTIONS = Object.keys(ACTION_LABELS).filter(a => !a.startsWith('group.') && a !== 'wake.manual')

export function auditResultLabel(result: string): string {
  switch (result) {
    case 'allow':
      return '放行'
    case 'deny':
      return '被拒绝'
    case 'assumed':
      return '按推断'
    default:
      return result
  }
}

export function auditResultTone(result: string): string {
  switch (result) {
    case 'deny':
      return 'bg-red-500/15 text-red-700 dark:text-red-300'
    case 'assumed':
      return 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
    default:
      return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
  }
}

/** 审计详情是 JSON 字符串；解析失败返回空对象。 */
export function auditDetail(ev: AuditEvent): Record<string, unknown> {
  if (!ev.detail) return {}
  try {
    const v = JSON.parse(ev.detail)
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

/** 「audit 档里若启用 enforce 会被拒绝」的记录（用来判断能不能切到强制执行）。 */
export function isWouldDeny(ev: AuditEvent): boolean {
  if (ev.action !== 'acl.denied') return false
  return auditDetail(ev).would_deny === true
}

export function auditActorText(ev: AuditEvent): string {
  const label = ev.actor_label?.trim()
  if (ev.actor_kind === 'human') return label || '你'
  return label ? `${label}` : ev.actor_ref
}

/** 「调用没声明身份」：写入没带 project_directory（acl.denied，原因 identity_required）。它与角色高低无关，授权没被查。 */
export function isIdentityRequired(ev: AuditEvent): boolean {
  return ev.action === 'acl.denied' && auditDetail(ev).reason === 'identity_required'
}

/**
 * 列表里这条记录叫什么。同一个 acl.denied 有两种事：「角色不够」和「调用没声明身份」，
 * 后者不是权限问题（在审计档里也没拦下任何东西），不该跟着叫「权限不足」。
 */
export function auditEventLabel(ev: AuditEvent): string {
  if (ev.action === 'acl.denied') return isIdentityRequired(ev) ? '未声明身份' : isTasksScope(ev) ? '任务权限不足' : '权限不足'
  return auditActionLabel(ev.action)
}

/** 共享任务板上的权限记录（#175 B2c）：范围是 tasks，范围编号是项目。 */
export function isTasksScope(ev: AuditEvent): boolean {
  return ev.scope_kind === 'tasks'
}

/** 「范围」一栏怎么写：任务板的权限记录不必给用户看一串项目编号。 */
export function auditScopeText(ev: AuditEvent): string {
  if (isTasksScope(ev)) return '共享任务板'
  return `${ev.scope_kind ?? ''} ${ev.scope_ref ?? ''}`.trim()
}

export interface AuditChip {
  label: string
  /** 样式类（与 auditResultTone 同一套） */
  tone: string
  /** 悬停说明；没有就是空串 */
  hint: string
}

const TONE_AMBER = 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
const TONE_RED = 'bg-red-500/15 text-red-700 dark:text-red-300'

export const ASSUMED_HINT = '按推断：这次调用没带 project_directory，调用方不是自己声明的，身份是系统按工作流绑定的目录推断出来的。'

/**
 * 结果标签。审计档里的 acl.denied 并没有拦下任何东西，只是记下「若强制执行会被拒绝」，
 * 所以不能和强制执行档里「真的被拒绝」画成同一个样子（以前都显示「被拒绝」，容易看成保存被拦下了）。
 * 详情里没有 would_deny 的老记录仍按原来的结果显示。
 */
export function auditResultChip(ev: AuditEvent): AuditChip {
  if (ev.action === 'acl.denied') {
    const wd = auditDetail(ev).would_deny
    if (wd === true) {
      return { label: '已放行 · 强制执行会拒绝', tone: TONE_AMBER, hint: '项目在审计档：这次没有被拦下，只是记下「若切到强制执行会被拒绝」。' }
    }
    if (wd === false) {
      return { label: '被拒绝', tone: TONE_RED, hint: '项目在强制执行档：这次被拦下了。' }
    }
  }
  return {
    label: auditResultLabel(ev.result),
    tone: auditResultTone(ev.result),
    hint: ev.result === 'assumed' ? ASSUMED_HINT : '',
  }
}

function auditRoleText(role: unknown): string {
  const r = typeof role === 'string' ? role : ''
  if (!r || r === 'none') return '没有权限'
  return grantRoleLabel(r)
}

/** 系统对「调用方是谁」的说法：有身份编号就是推断出的那个身份，否则连它也认不出。 */
function inferredWho(ev: AuditEvent): string {
  return ev.actor_ref.startsWith('identity:')
    ? `只能按工作流绑定的目录推断调用方是「${auditActorText(ev)}」`
    : '连调用方是哪个身份也认不出来'
}

/** 项目管理 Agent 运行预设被拒绝时（launcher.run，result=deny）详情里的 code 对应的白话原因（#179）。 */
const LAUNCHER_DENY_REASONS: Record<string, string> = {
  not_project_manager: '它不是项目管理 Agent（项目页「任务管理」里没有勾选它）',
  preset_not_found: '没有这个预设，或者它的目标工作流不在本项目的任务板上',
  own_workflow: '目标是它自己所在的工作流（不能自己唤醒自己）',
  run_active: '这个预设上一次运行还没有结束',
  conversation_live: '目标对话此刻还在运行',
  agent_run_daily_cap: '所有项目管理 Agent 当天的运行次数到了上限（防止互相唤醒形成循环）',
  agent_run_hourly: '它这一小时发起的运行次数到了上限',
  agent_run_preset_gap: '同一个预设两次运行之间的间隔太短',
  agent_run_concurrency: '它同时在跑的运行太多',
}

/**
 * 一条审计记录的白话解释（展开后显示在原始字段上面）。没有需要解释的返回空串。
 * 说清三件事：发生了什么、有没有真的拦下、怎么补。
 */
export function auditExplain(ev: AuditEvent): string {
  const d = auditDetail(ev)
  if (ev.action === 'acl.denied') {
    const enforced = d.would_deny === false || d.mode === 'enforce'
    const verdict = enforced ? '项目是强制执行档，所以被拒绝了。' : '项目现在是审计档：已放行并记录，没有拦下任何东西；切到强制执行后，这类调用会被拒绝。'
    if (d.reason === 'identity_required') {
      const fix = '让这个 Agent 在调用里带上 project_directory 与 host_name'
      if (enforced) return `这次写入没有带 project_directory（调用方没声明自己是谁），${verdict}${fix} 后重试。`
      return `这次写入没有带 project_directory（调用方没声明自己是谁），系统${inferredWho(ev)}，所以没有去查授权。${verdict}${fix} 即可，带上之后才会按你给它的授权来判定。`
    }
    if (isTasksScope(ev)) {
      const why = typeof d.reason === 'string' && d.reason ? `：${d.reason}` : ''
      const fix = '要让它能做，请在本项目「授权」页的「任务管理」里指定它为管理 Agent；或只改它自己创建或承接的任务。'
      if (enforced) return `「${auditActorText(ev)}」在共享任务板上的这次写入超出了规则${why}，${verdict}${fix}`
      return `「${auditActorText(ev)}」在共享任务板上的这次写入超出了规则${why}。${verdict}${fix}`
    }
    const have = auditRoleText(d.have)
    const need = auditRoleText(d.need)
    const rule = typeof d.rule === 'string' && d.rule ? `（规则 ${d.rule}）` : ''
    const fix = `要让它能做，给它「${need}」授权，或让负责人处理。`
    if (enforced) return `「${auditActorText(ev)}」在这里只有「${have}」，这个操作需要「${need}」${rule}，所以被拒绝了。${fix}`
    return `「${auditActorText(ev)}」在这里只有「${have}」，这个操作需要「${need}」${rule}。${verdict}${fix}`
  }
  if (ev.action === 'launcher.run') {
    const who = `「${auditActorText(ev)}」`
    if (ev.result === 'allow') {
      const name = typeof d.name === 'string' && d.name ? `「${d.name}」` : ''
      const wf = typeof d.workflow_id === 'string' && d.workflow_id ? `，唤醒的是工作流 ${d.workflow_id}` : ''
      const approve = d.state === 'awaiting_approval' ? '这台主机的执行模式是「需批准」，要你在 ExecMesh 里批准后才会开始。' : ''
      return `${who}作为项目管理 Agent 运行了预设${name}${wf}。预设里的提示词与命令不会记录；运行本身在 ExecMesh 的运行记录里。${approve}`
    }
    const code = typeof d.code === 'string' ? d.code : ''
    return `${who}想运行预设，被拒绝了：${LAUNCHER_DENY_REASONS[code] ?? (code ? `原因代码 ${code}` : '没有写明原因')}。`
  }
  if (ev.result === 'assumed') {
    return `这次调用没带 project_directory，系统${inferredWho(ev)}；操作本身已经完成。`
  }
  return ''
}

/** 审计页的图例：每个结果标签是什么意思。 */
export const AUDIT_LEGEND: { term: string; text: string }[] = [
  { term: '放行', text: '带了身份、角色够，判定通过。' },
  { term: '按推断', text: '调用没带 project_directory，身份不是调用方自己声明的，而是系统按工作流绑定的目录推断的；操作照常完成，只是记下「没声明身份」。' },
  { term: '已放行 · 强制执行会拒绝', text: '项目在审计档：这次没有被拦下，只是记下「若切到强制执行会被拒绝」——原因要么是没声明身份，要么是角色不够，展开看白话说明。' },
  { term: '被拒绝', text: '项目在强制执行档：这次真的被拦下了。' },
  { term: '未声明身份', text: '与角色高低无关：没声明身份的写入不会去查授权，所以你给它的「可编辑」这时不起作用；让 Agent 在调用里带上 project_directory 与 host_name 就好。' },
]

// ---------------------------------------------------------------------------
// 知识库
// ---------------------------------------------------------------------------

/** 把项目文档元数据转成现有文档树组件吃的形状。 */
export function toTreeDocs(items: KbDoc[]): ProjectDoc[] {
  return items.map(d => ({
    id: d.id,
    workflow_id: d.workflow_id,
    seq: 0,
    path: d.path,
    title: d.title,
    kind: d.kind,
    status: d.status,
    tags: d.tags ?? [],
    rev: 0,
    hash: '',
    updated_at: d.updated_at,
  }))
}

/** 渲染方式的显示名。 */
export function renderLabel(render: string | undefined): string {
  switch (render) {
    case 'mermaid':
      return '图'
    case 'html':
      return '页面'
    default:
      return 'Markdown'
  }
}
