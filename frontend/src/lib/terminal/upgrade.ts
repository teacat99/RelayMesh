import type { HostView } from '../../api/terminal'
import type { InstallSourceStatus, TargetOS } from './installCommand'
import { jobActive, jobBadgeTitle } from './webUpgrade'

// Agent 升级提醒与升级窗口的纯逻辑：徽标文案、默认升级范围、目标平台、「升级完成」的判定。
// 服务端不会把代码推给主机：升级始终是主机自己去取——要么在主机上执行一条带票据的命令，要么（主机所有者在本机允许后）
// 由 Agent 自己取（见 webUpgrade.ts）。所以这里只负责把「该不该提醒、命令怎么选」说清楚。

export type UpgradeTone = 'warn' | 'info'

export interface UpgradeBadge {
  text: string
  title: string
  tone: UpgradeTone
}

export type UpgradeHost = Pick<
  HostView,
  'agent_version' | 'agent_rev' | 'latest_agent_version' | 'upgrade_available' | 'upgrade_kind' | 'up_to_date_by_rev' | 'upgrade_window' | 'upgrade_job'
>

/** 升级窗口的默认时长（分钟）；与服务端 terminal.DefaultUpgradeWindow 一致。 */
export const UPGRADE_WINDOW_MINUTES = 10
/** 窗口总时长上限（秒，与服务端 terminal.MaxUpgradeWindow 一致）：剩余时间已经到了这个程度就不能再延时。 */
export const UPGRADE_WINDOW_MAX_SECONDS = 60 * 60
/** 剩余时间不足这个数就提醒：要么延时，要么马上把命令跑完。 */
export const UPGRADE_WINDOW_WARN_SECONDS = 120

/** 这台主机的升级窗口是否正开着（以服务端给的状态为准）。 */
export function upgradeWindowOpen(h: Pick<HostView, 'upgrade_window'>): boolean {
  return !!h.upgrade_window?.open && !!h.upgrade_window.ticket
}

/** 主机卡片上的升级徽标；没有需要提醒的就返回 null（一键升级进行中、升级窗口开着时优先显示它们，因为那是用户正在做的事）。 */
export function upgradeBadge(h: UpgradeHost): UpgradeBadge | null {
  const job = h.upgrade_job
  if (job && jobActive(job)) return { text: '升级中', title: jobBadgeTitle(job), tone: 'info' }
  const win = h.upgrade_window
  if (win?.open) {
    const minutes = Math.max(1, Math.ceil(win.remaining_seconds / 60))
    return { text: '升级中', title: `升级窗口已开启，剩余约 ${minutes} 分钟；点击查看升级命令`, tone: 'info' }
  }
  if (!h.upgrade_available) return null
  // 上一次一键升级失败了、版本也确实还没升上去：提示失败，点开能看到原因。
  if (job?.state === 'failed') return { text: '升级失败', title: jobBadgeTitle(job), tone: 'warn' }
  const now = h.agent_version || '未知版本'
  const latest = h.latest_agent_version || '未知版本'
  if (h.upgrade_kind === 'older') {
    return { text: '可升级', title: `当前 ${now}，服务端自带 ${latest}；点击查看如何升级`, tone: 'warn' }
  }
  if (h.upgrade_kind === 'changed') {
    return {
      text: '代码有更新',
      title: `当前 ${now}，服务端自带 ${latest}。两边的 Agent 代码确实不同，但这两个版本号无法比较新旧（例如同一天的两次构建）；点击查看如何升级`,
      tone: 'warn',
    }
  }
  return {
    text: '版本不同',
    title: `当前 ${now}，服务端自带 ${latest}。这两个版本号无法比较新旧（可能是本地构建或短哈希版本号），是否升级由你决定；点击查看如何升级`,
    tone: 'warn',
  }
}

/** 一句话说明当前版本与服务端自带版本的关系（升级对话框顶部用）。 */
export function upgradeSummary(h: UpgradeHost): string {
  const now = h.agent_version || '未知版本'
  const latest = h.latest_agent_version
  if (!latest) return `当前 Agent ${now}`
  if (!h.upgrade_available) {
    return h.up_to_date_by_rev
      ? `当前 Agent ${now}，服务端自带 ${latest}：两边的 Agent 代码完全一致（版本号不同只是构建号不同），不需要升级`
      : `当前 Agent ${now}，与服务端自带的版本一致`
  }
  switch (h.upgrade_kind) {
    case 'older':
      return `当前 Agent ${now}，服务端自带 ${latest}（更新）`
    case 'changed':
      return `当前 Agent ${now}，服务端自带 ${latest}（Agent 代码有更新，两个版本号无法比较新旧）`
    default:
      return `当前 Agent ${now}，服务端自带 ${latest}（两者无法比较新旧，是否升级由你决定）`
  }
}

export interface AgentVersionNote {
  text: string
  title: string
  tone: 'ok' | 'warn'
}

/**
 * 展开的主机详情里 Agent 版本号后面的那句括号说明：
 * - 有可升级的更新：「（服务端自带 X）」（琥珀色），悬停说明是哪种更新；
 * - 两边的 Agent 代码一致、只是版本号（构建号）不同：「（与服务端自带的 X 代码一致）」，说明不需要升级——
 *   这正是「Hub 每热更一次，所有主机的版本号就都不一样」时最该让人放心的一句话；
 * - 其余情况返回 null（版本一致、服务端没有写版本、窗口 / 一键升级进行中由徽标表达）。
 */
export function agentVersionNote(h: UpgradeHost): AgentVersionNote | null {
  const latest = h.latest_agent_version
  if (!latest) return null
  if (h.upgrade_available) {
    const titles: Record<string, string> = {
      older: '服务端自带的版本更新',
      changed: '两边的 Agent 代码不同（指纹不同），但两个版本号无法比较新旧',
    }
    return { text: `（服务端自带 ${latest}）`, tone: 'warn', title: titles[h.upgrade_kind ?? ''] ?? '两个版本号无法比较新旧' }
  }
  if (h.up_to_date_by_rev) {
    return {
      text: `（与服务端自带的 ${latest} 代码一致）`,
      tone: 'ok',
      title: `Agent 侧代码指纹相同${h.agent_rev ? `（${h.agent_rev}）` : ''}：版本号不同只是构建号不同，Agent 代码完全一致，不需要升级`,
    }
  }
  return null
}

/** Agent 能运行的三种系统（Go 的 GOOS）。 */
export type HostGOOS = 'linux' | 'darwin' | 'windows'

/**
 * 从主机上报的系统描述推出 GOOS。
 *
 * Agent 握手时报的 os 是给人看的描述，不是 GOOS：Linux 上是 /etc/os-release 的 PRETTY_NAME（"Ubuntu 24.04.2 LTS"、
 * "Debian GNU/Linux 12 (bookworm)"），macOS 是 "macOS 14.5"，Windows 是 "windows"。已经装好的旧 Agent 不会再多报一个 GOOS，
 * 所以只能从描述归一：认出 Windows / macOS 的写法，单个词又不是这三种的（freebsd 之类）视为不认识，其余描述（多个词的发行版名）按 Linux。
 */
export function hostGOOS(os: string): HostGOOS | null {
  const s = (os || '').trim().toLowerCase()
  if (!s) return null
  if (s === 'linux' || s === 'darwin' || s === 'windows') return s
  if (/\bwindows\b/.test(s)) return 'windows'
  if (/\b(macos|mac os x?|os x|darwin)\b/.test(s)) return 'darwin'
  if (!/\s/.test(s)) return null // 单个词、又不是上面三种：不认识的系统
  if (/bsd|solaris|illumos|\baix\b/.test(s)) return null // Agent 没有这些平台的构建
  return 'linux'
}

/** 主机上报的系统换成命令生成器认识的目标系统；不认识的返回 null（不提供升级命令）。 */
export function upgradeTargetOS(os: string): TargetOS | null {
  switch (hostGOOS(os)) {
    case 'linux':
      return 'linux'
    case 'darwin':
      return 'macos'
    case 'windows':
      return 'windows'
    default:
      return null
  }
}

/** 这台主机需要的 Agent 平台名，与服务端 /status 里 install.platforms 的写法一致（如 linux-amd64）；信息不全或不认识返回空串。 */
export function upgradePlatform(h: Pick<HostView, 'os' | 'arch'>): string {
  const goos = hostGOOS(h.os)
  const arch = (h.arch || '').trim().toLowerCase()
  return goos && arch ? `${goos}-${arch}` : ''
}

export type UpgradeBlock =
  /** 服务端没有附带 Agent 安装文件。 */
  | { kind: 'no-source' }
  /** 服务端附带了安装文件，但没有这台主机的平台。 */
  | { kind: 'no-platform'; platform: string }
  /** 主机上报的系统 / 架构不全或不认识。 */
  | { kind: 'unknown-platform' }

/** 能不能为这台主机提供升级：不能时返回原因（界面据此说明，而不是给一条跑不通的命令）。 */
export function upgradeBlock(h: Pick<HostView, 'os' | 'arch'>, install: InstallSourceStatus | null | undefined): UpgradeBlock | null {
  if (!install?.available) return { kind: 'no-source' }
  const platform = upgradePlatform(h)
  if (!platform || !upgradeTargetOS(h.os)) return { kind: 'unknown-platform' }
  if (install.platforms && !install.platforms.includes(platform)) return { kind: 'no-platform', platform }
  return null
}

export function upgradeBlockText(b: UpgradeBlock): string {
  switch (b.kind) {
    case 'no-source':
      return '这台服务端没有附带 Agent 安装文件，无法提供升级链接。可以改用 GitHub Release 的安装命令在主机上重新安装，或使用附带了 Agent 的服务端镜像。'
    case 'no-platform':
      return `这台服务端没有附带 ${b.platform} 的 Agent 安装文件，无法为这台主机提供升级链接。`
    case 'unknown-platform':
      return '这台主机没有上报完整的系统与架构信息（或系统不受支持），无法生成升级命令。'
  }
}

/**
 * 默认的升级范围：root / 管理员权限运行的 Agent 多半是系统级服务，普通用户权限的是用户级服务。
 * 这只是预选（对话框里可以改）；就算选错，命令里的 `agent upgrade` 也会自己找到已安装的那一份，找不到时给出明确提示。
 */
export function defaultUpgradeScope(privilege: string): 'system' | 'user' {
  return privilege === 'root' || privilege === 'admin' ? 'system' : 'user'
}

/**
 * 找出这次轮询里「升级完成」的主机：上一次它的升级窗口还开着，这一次窗口没了，并且 Agent 版本变了。
 * 窗口只是到期或被手动结束（版本没变）不算完成。
 */
export function finishedUpgrades(prev: readonly HostView[], next: readonly HostView[]): HostView[] {
  const before = new Map(prev.map(h => [h.machine_code, h]))
  const done: HostView[] = []
  for (const h of next) {
    const p = before.get(h.machine_code)
    if (!p?.upgrade_window?.open || h.upgrade_window?.open) continue
    // 以开窗时的版本为准（服务端在版本变化时关窗）；老版本的服务端没有 base_version 时退回上一次看到的版本。
    const from = p.upgrade_window.base_version || p.agent_version
    if (h.agent_version && h.agent_version !== from) done.push(h)
  }
  return done
}
