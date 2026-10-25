import type { LauncherOtherRun, SessionLauncherSuggest, SessionLauncherView, SessionLaunchersView, TerminalLauncher } from '../../api/terminal'
import { runLimitText, shortCode } from './format'
import { blankRunDraft, newLauncherId, toLauncher, type LauncherDraft } from './launcher'
import { DEFAULT_RUN_LIMIT_SEC, RUN_POLL_ACTIVE_MS } from './launcherRun'

// 会话页底部的「运行」按钮与启动器的「本项目」标签（#140）：不碰界面的那一部分。
// 服务端（GET /terminal/launchers/for-session）已经按「主机 + 目录 + 跟的是这个会话」选出了对应的非交互续聊预设，
// 并按提交运行时同一份检查算好了此刻能不能运行；这里只负责把它翻译成界面要的样子：按钮显不显示、确认窗写什么、
// 启动器怎么分「本项目 / 全部」、没有对应预设时怎么给出一份预填的建议。

/** 这些状态下这个对话还在运行：不显示「运行」（状态块已经在说它正在工作，再续聊同一个对话会和它打架）。 */
const LIVE_STATE_KEYS: ReadonlySet<string> = new Set(['working', 'waiting_you', 'waiting_approval'])

/** 底部「运行」按钮此刻的样子：不出现 / 可点（一条或多条预设可选）/ 灰掉并写明原因。 */
export type QuickRun =
  | { mode: 'hidden' }
  | { mode: 'ready'; presets: SessionLauncherView[] }
  | { mode: 'blocked'; reason: string; preset: SessionLauncherView }

/** 一条预设此刻不能运行的第一个原因（也就是点了运行会被拒绝的那个）。 */
export function firstReason(p: SessionLauncherView): string {
  return p.issues?.[0]?.message || '现在不能运行'
}

/**
 * 底部「运行」按钮：没有对应预设、对话还在运行（智能体状态是工作中 / 等你回答 / 等批准）、
 * 或每条预设都「忙」（对话在运行 / 上一次运行没结束）时不出现；有可运行的预设时可点；
 * 其余原因（主机离线、对话编号未知、命令拼不出来……）灰掉并把原因放进提示，免得用户不知道为什么没有按钮可点。
 */
export function quickRun(view: SessionLaunchersView | null | undefined, agentKey: string): QuickRun {
  if (!view || !view.presets?.length || LIVE_STATE_KEYS.has(agentKey)) return { mode: 'hidden' }
  const idle = view.presets.filter(p => !p.busy)
  if (!idle.length) return { mode: 'hidden' }
  const ready = idle.filter(p => p.can_run)
  if (ready.length) return { mode: 'ready', presets: ready }
  return { mode: 'blocked', reason: firstReason(idle[0]), preset: idle[0] }
}

/** 确认窗里要展示的几行：在哪台主机的哪个目录、接着哪个对话、什么模型 / 权限 / 时限。 */
export interface RunFacts {
  target: string
  conversation: string
  options: string
}

/** 「接着对话」那一行（#191）：新会话没有可接的对话、最近会话由 Cursor CLI 决定，都不是「还不知道」。 */
function conversationFact(p: SessionLauncherView): string {
  if (p.mode === 'new') return '新开一个对话（运行后记下它的编号）'
  if (p.mode === 'continue') return '目录里最近动过的对话（由 Cursor CLI 决定，可能不是这个会话的）'
  return p.conversation_id ? `${p.conversation_id.slice(0, 8)}…` : '还不知道'
}

export function runFacts(p: SessionLauncherView): RunFacts {
  const parts = [p.model || '默认模型', p.posture === 'force' ? '放开全部命令（--force）' : '只放行白名单内的操作']
  // 设了时限才写（默认的 7 天不是用户的选择，不占位置）。
  if (p.max_run_seconds > 0 && p.max_run_seconds < DEFAULT_RUN_LIMIT_SEC) parts.push(`最长运行 ${runLimitText(p.max_run_seconds)}`)
  return {
    target: `${p.host_title || shortCode(p.host)} · ${p.dir}`,
    conversation: conversationFact(p),
    options: parts.join(' · '),
  }
}

// ---------- 启动器的「本项目 / 全部」 ----------

/** 「本项目」的范围：会话上报的项目目录，以及（主机能对上时）它所在的主机。 */
export interface ProjectScope {
  /** 已规整的绝对路径；空 = 会话没有上报项目目录，划分不出「本项目」。 */
  dir: string
  /** 名字与会话上报的主机一致的主机（机器码）；空 = 不按主机筛。 */
  hosts: string[]
}

/** 去掉重复和末尾的斜杠（根目录除外）。 */
export function normDir(dir: string | undefined): string {
  const d = (dir ?? '').trim().replace(/\/{2,}/g, '/')
  return d.length > 1 ? d.replace(/\/+$/, '') : d
}

/** 没读到视图（旧版 Hub、还没读完）时是 null：启动器不分标签，保持原样。 */
export function projectScope(view: SessionLaunchersView | null | undefined): ProjectScope | null {
  if (!view) return null
  return { dir: normDir(view.dir), hosts: view.hosts ?? [] }
}

/** 项目目录最后一段，用来在标签与建议里叫出项目名。 */
export function projectName(scope: ProjectScope | null): string {
  const dir = scope?.dir ?? ''
  const name = dir.split('/').filter(Boolean).pop()
  return name || ''
}

/** 预设属于「本项目」：它的目录就是项目目录或在它下面，并且（会话所在主机能对上时）主机也一致。 */
export function inProject(l: Pick<TerminalLauncher, 'dir' | 'host'>, scope: ProjectScope | null): boolean {
  if (!scope?.dir) return false
  const d = normDir(l.dir)
  if (!d) return false
  const prefix = scope.dir.endsWith('/') ? scope.dir : scope.dir + '/'
  if (d !== scope.dir && !d.startsWith(prefix)) return false
  return scope.hosts.length === 0 || scope.hosts.includes(l.host)
}

/**
 * 当前标签下该看到哪些「其他非交互运行」（#147）：「本项目」= 运行的工作目录在会话的项目目录之内、并且（会话所在主机能对上时）主机一致，
 * 与预设归入「本项目」是同一个口径；「全部」或不分标签（不是从会话页打开）时全部都看得到。
 */
export function othersInView(
  others: readonly LauncherOtherRun[] | null | undefined,
  scope: ProjectScope | null | undefined,
  projectTab: boolean,
): LauncherOtherRun[] {
  const list = others ?? []
  if (!projectTab) return [...list]
  return list.filter(o => inProject({ dir: o.dir, host: o.host }, scope ?? null))
}

export type ListTab = 'project' | 'all'
/** 'auto' = 用户还没选过，按默认规则。 */
export type TabChoice = ListTab | 'auto'

export function tabCounts(launchers: readonly TerminalLauncher[], scope: ProjectScope | null): { project: number; all: number } {
  return { project: launchers.filter(l => inProject(l, scope)).length, all: launchers.length }
}

/** 默认看「本项目」；本项目里没有预设、也没有建议可给时，默认看「全部」（别让人对着一个空标签）。 */
export function resolveTab(choice: TabChoice, counts: { project: number }, hasSuggest: boolean): ListTab {
  if (choice !== 'auto') return choice
  return counts.project > 0 || hasSuggest ? 'project' : 'all'
}

// ---------- 没有对应预设时的建议 ----------

/** 建议 → 表单草稿：名字、目录、工作流和权限姿态已填好，主机留给调用方（建议里没有唯一主机时让人选）。 */
export function suggestDraft(s: SessionLauncherSuggest, host: string = s.host ?? ''): LauncherDraft {
  const run = blankRunDraft()
  run.workflow = s.workflow_id
  run.posture = s.posture === 'force' ? 'force' : 'allowlist'
  return { id: newLauncherId(), name: s.name, host, dir: s.dir, run }
}

/** 建议 → 可以直接保存的预设（主机必须已经唯一确定）。 */
export function suggestLauncher(s: SessionLauncherSuggest): TerminalLauncher {
  return toLauncher(suggestDraft(s))
}

/** 建议卡上的一句话：会创建什么。 */
export function suggestSummary(s: SessionLauncherSuggest): string {
  const where = s.needs_host ? `${s.dir}（主机待选）` : `${s.host_title || shortCode(s.host ?? '')} · ${s.dir}`
  return `${where} · 接着工作流 ${s.workflow_id} 里最近的对话 · ${s.posture === 'force' ? '放开全部命令' : '只放行白名单内的操作'}`
}

// ---------- 刷新节奏 ----------

/** 没有进行中的运行时多久读一次（别处点的运行、预设被改、对话状态变化都要跟上）。 */
export const SESSION_LAUNCHERS_IDLE_MS = 30_000

/** 有运行在进行时勤一点（要尽快看到它结束、按钮重新出现），否则慢一点。 */
export function sessionLaunchersPollDelay(view: SessionLaunchersView | null | undefined): number {
  return view?.presets?.some(p => p.last_run?.active) ? RUN_POLL_ACTIVE_MS : SESSION_LAUNCHERS_IDLE_MS
}
