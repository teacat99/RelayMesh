import type { AutoCheckStatus, AutoCheckView, AutoGrantView, AutoUpgradeMode, HostView, UpgradeJobView } from '../../api/terminal'
import { relativeTime } from './format'
import { jobActive, jobIsAuto, jobNotice } from './webUpgrade'

// 「Agent 自动升级」的纯逻辑：主机卡片上的小标签与悬停说明。
// 三条原则要在文案里讲清楚，免得人误会它「会自己改我的主机」：
//   ① 开关只在主机上——服务端无法替主人打开，这里只展示主机上的选择；
//   ② 提示模式只做提醒：Agent 定期问服务端有没有新版本，不下载、不升级；
//   ③ 服务端另有一道总闸，默认关；关着时主机即使开了也不会有任何检查结果。
// 静默模式（主机所有者在本机选了 silent）是 Agent 自己发起升级：服务端有更高版本、带发布签名、主机上没有会损失的会话时，
// Agent 下载后先验签名与 SHA-256 再升级；界面只负责把「它现在处于什么状态、为什么还没升级」说清楚。

export type AutoUpgradeHost = Pick<HostView, 'auto_upgrade' | 'auto_check' | 'auto_grant' | 'auto_window' | 'upgrade_job'>

export type AutoUpgradeTone = 'info' | 'warn' | 'muted'

export interface AutoUpgradeChip {
  text: string
  title: string
  tone: AutoUpgradeTone
}

/** 在主机上开启「提示模式」的命令（界面只说明，不替主人执行）。 */
export const AUTO_UPGRADE_ENABLE_COMMAND = 'relaymesh-agent auto-upgrade enable --mode notify'

/**
 * 在主机上开启「静默模式」的命令（界面只说明，不替主人执行）。官方发布的 Agent 已内置发布公钥（主钥 + 备用钥），
 * 不需要再钉公钥；自行编译的版本没有内置公钥，才需要追加 --trust-key rmpub1:<公钥>（设置页的说明里写了）。
 */
export const AUTO_UPGRADE_SILENT_COMMAND = 'relaymesh-agent auto-upgrade enable --mode silent'

/** 服务端总闸所在的位置（设置页的路径），写进悬停说明里。 */
export const AUTO_UPGRADE_SWITCH_PLACE = '设置 › 终端偏好 › Agent 自动升级'

export function autoUpgradeModeText(mode: AutoUpgradeMode | undefined): string {
  if (mode === 'notify') return '提示'
  if (mode === 'silent') return '静默'
  return '关'
}

const WINDOW_RE = /^(\d{2}:\d{2})-(\d{2}:\d{2})$/

/** 静默模式「允许开始升级的时间段」的一句话；没设（或格式不对）就是空串。 */
export function windowSentence(window: string | undefined): string {
  const m = WINDOW_RE.exec(window ?? '')
  if (!m) return ''
  return `只在主机本地时间 ${m[1]}–${m[2]} 之间开始升级（已经开始的升级不受影响）。`
}

function behaviourText(mode: AutoUpgradeMode, window: string | undefined): string {
  if (mode === 'silent') {
    return (
      '静默模式：服务端有更高版本时，Agent 会自己发起升级——下载后先验证 SHA-256 与发布签名，再试运行、交给独立的升级进程替换并重启，新版本连不上服务端会自动还原。' +
      windowSentence(window)
    )
  }
  return '提示模式只做提醒：Agent 定期问服务端有没有新版本，不下载、不升级。'
}

function ts(iso: string | undefined): number {
  const t = Date.parse(iso ?? '')
  return Number.isNaN(t) ? 0 : t
}

type SilentEvent =
  | { kind: 'job'; job: UpgradeJobView }
  | { kind: 'grant'; grant: AutoGrantView }
  | { kind: 'check'; check: AutoCheckView }

/**
 * 静默主机上「最近发生的事」：主机自己发起的升级请求、最近一次裁决、最近一次检查，以时间最晚的为准
 * （同一时刻以信息更具体的为准：升级 > 裁决 > 检查）。这样补上签名后下一轮的新裁决会盖掉旧的「未签名」，
 * 升级完成重新上线后新一轮的检查也会盖掉「刚升级完成」。管理员在 Web 上点的升级不算——它有自己的徽标。
 */
function latestSilentEvent(h: AutoUpgradeHost): SilentEvent | null {
  const cands: Array<{ at: number; rank: number; ev: SilentEvent }> = []
  const job = h.upgrade_job
  if (job && jobIsAuto(job)) cands.push({ at: ts(job.updated_at), rank: 3, ev: { kind: 'job', job } })
  if (h.auto_grant) cands.push({ at: ts(h.auto_grant.at), rank: 2, ev: { kind: 'grant', grant: h.auto_grant } })
  if (h.auto_check) cands.push({ at: ts(h.auto_check.at), rank: 1, ev: { kind: 'check', check: h.auto_check } })
  cands.sort((a, b) => b.at - a.at || b.rank - a.rank)
  return cands[0]?.ev ?? null
}

/** 静默主机上由「升级请求」本身产生的标签（升级进行中 / 刚完成 / 失败）。 */
function jobChip(job: UpgradeJobView, label: string): AutoUpgradeChip {
  const notice = jobNotice(job)
  if (jobActive(job)) {
    return { text: `${label} · 升级中`, tone: 'info', title: `静默自动升级进行中（${job.from || '旧版本'} → ${job.to}）：${notice.title}` }
  }
  if (job.state === 'done') {
    return { text: `${label} · 刚升级完成`, tone: 'info', title: `${notice.title}。${notice.detail}` }
  }
  return { text: `${label} · 上次升级失败`, tone: 'warn', title: `${notice.title}。${notice.detail}` }
}

/** 静默主机上由「裁决」产生的标签；与检查的回答相同的几种（总闸 / 没开启 / 已最新 / 无可用程序）交还给通用逻辑。 */
function grantChip(g: AutoGrantView, label: string, behaviour: string, when: string): AutoUpgradeChip | null {
  const version = g.version ? ` ${g.version}` : ''
  switch (g.status) {
    case 'granted':
      return { text: `${label} · 已批准`, tone: 'info', title: `${behaviour}最近一次请求（${when}）：服务端已批准升级到${version}，Agent 马上开始下载。` }
    case 'unsigned':
      return {
        text: `${label} · 未签名`,
        tone: 'warn',
        title:
          `${behaviour}最近一次请求（${when}）：服务端上的${version || '这个版本'}没有发布签名，所以不会自动升级——静默模式绝不安装没有签名的程序。` +
          '自建或社区构建默认没有签名；可以改用提示模式，再按步骤手动升级。',
      }
    case 'busy':
      return {
        text: `${label} · 稍后再试`,
        tone: 'info',
        title:
          `${behaviour}最近一次请求（${when}）：这台主机现在不适合升级——有运行中的会话（且没有独立的会话守护进程，升级会结束它们），或者已有别的升级在进行。` +
          'Agent 会隔一段时间再试，不会打断正在做的事。',
      }
    case 'stale':
      return {
        text: label,
        tone: 'muted',
        title: `${behaviour}最近一次请求（${when}）：服务端上的最新版本在 Agent 检查之后变了，Agent 会重新检查，再决定升级到哪个版本。`,
      }
    default:
      return null
  }
}

function checkChip(check: AutoCheckView | undefined, mode: AutoUpgradeMode, label: string, behaviour: string, nowMs: number): AutoUpgradeChip {
  if (!check || check.status === 'disabled') {
    return {
      text: label,
      tone: 'muted',
      title: `${behaviour}还没有可用的检查结果：Agent 被批准、连上之后几十秒内会检查一次，之后约每 6 小时一次（没得到确定答案时约每 15 分钟再问一次）。`,
    }
  }

  const when = relativeTime(check.at, nowMs)
  const latest = check.version ? `（服务端 ${check.version}）` : ''
  switch (check.status) {
    case 'available':
      return {
        text: `${label} · 有新版本`,
        tone: 'info',
        title:
          `${behaviour}最近一次检查（${when}）：服务端有新版本${check.version ? ` ${check.version}` : ''}。` +
          (mode === 'notify'
            ? '提示模式不会自动升级；点主机上的「可升级」徽标，按步骤升级。'
            : 'Agent 会自己发起升级请求；服务端的这个版本必须带发布签名，同一个版本最多自动尝试 2 次。'),
      }
    case 'up_to_date':
      return { text: label, tone: 'muted', title: `${behaviour}最近一次检查（${when}）：已是最新${latest}。` }
    case 'unavailable':
      return {
        text: `${label} · 无可用程序`,
        tone: 'warn',
        title: `${behaviour}最近一次检查（${when}）：服务端暂时没有适用于这台主机的 Agent 程序，无从比较新旧。`,
      }
    case 'not_allowed':
      return {
        text: label,
        tone: 'warn',
        title: `${behaviour}最近一次检查（${when}）：服务端认为这台主机没有开启自动升级（可能刚改过本机的设置，稍后重新握手就会一致）。`,
      }
    default:
      return { text: label, tone: 'muted', title: `${behaviour}最近一次检查：${when}。` }
  }
}

/**
 * 主机卡片徽标行里的「自动升级」小标签。只在主机所有者已在本机开启（notify / silent）时才显示——
 * 默认（关）与不支持的 Agent 不占位；这样一眼能看出哪些主机「会检查新版本」。
 * hubSwitch：服务端总闸（undefined = 这台 Hub 不认识自动升级，此时 Agent 也不会声明，通常不会走到这里）。
 */
export function autoUpgradeChip(h: AutoUpgradeHost, hubSwitch: boolean | undefined, nowMs: number = Date.now()): AutoUpgradeChip | null {
  const mode = h.auto_upgrade
  if (mode !== 'notify' && mode !== 'silent') return null
  const label = `自动升级：${autoUpgradeModeText(mode)}`
  const behaviour = behaviourText(mode, h.auto_window)
  const check = h.auto_check

  // 总闸以最新读到的为准；读不到时（undefined）才信 Hub 上一次的回答。
  // 总闸已经打开、但 Hub 上一次的回答还是「关着」：那是打开之前问的，已经过期，等下一次检查即可。
  if (hubSwitch === false || (hubSwitch === undefined && check?.status === 'disabled')) {
    return {
      text: `${label} · 总闸关`,
      tone: 'warn',
      title: `主机所有者已在本机开启自动升级（${autoUpgradeModeText(mode)}模式），但服务端的「Agent 自动升级」总闸是关着的，所以不会有任何检查或升级。总闸在「${AUTO_UPGRADE_SWITCH_PLACE}」里打开。`,
    }
  }

  if (mode === 'silent') {
    const ev = latestSilentEvent(h)
    if (ev?.kind === 'job') return jobChip(ev.job, label)
    if (ev?.kind === 'grant') {
      const chip = grantChip(ev.grant, label, behaviour, relativeTime(ev.grant.at, nowMs))
      if (chip) return chip
      // 与检查的回答相同的几种：按「这一刻的回答」交给通用逻辑；总闸已在上面单独处理（disabled 的旧回答不算数）。
      if (ev.grant.status === 'disabled' || ev.grant.status === 'too_soon') return checkChip(check, mode, label, behaviour, nowMs)
      return checkChip({ at: ev.grant.at, status: ev.grant.status as AutoCheckStatus, version: ev.grant.version }, mode, label, behaviour, nowMs)
    }
  }
  return checkChip(check, mode, label, behaviour, nowMs)
}

export interface AutoUpgradeNote {
  text: string
  title: string
}

/**
 * 展开的主机详情里的「自动升级：关」。只对声明了这项能力、但主机所有者没有开启的 Agent 显示——
 * 让人知道这件事存在、以及要在哪里打开（开关只在主机上）；已开启的由徽标行里的标签表达，不重复。
 */
export function autoUpgradeNote(h: Pick<HostView, 'auto_upgrade'>): AutoUpgradeNote | null {
  if (h.auto_upgrade !== 'off') return null
  return {
    text: '自动升级：关',
    title:
      `主机所有者没有在本机开启自动升级。要开启请在这台主机上执行：${AUTO_UPGRADE_ENABLE_COMMAND}（只提示、不升级）；` +
      `需要无人值守升级时用 --mode silent（要求服务端分发的程序带发布签名，且主机信任签名公钥，详见「${AUTO_UPGRADE_SWITCH_PLACE}」的说明）。` +
      '服务端的总闸也要打开。服务端无法替主机开启。',
  }
}

export interface AutoUpgradeCoverage {
  /** 声明支持自动升级的在线主机数（含主机所有者没有开启的）。 */
  supported: number
  /** 其中主机所有者已在本机开启（提示 / 静默）的数量。 */
  enabled: number
  /** 其中选了静默模式的数量（会自己升级的那些）。 */
  silent: number
}

/** 统计在线主机里有多少声明支持、多少已开启、多少是静默模式（离线主机不带这项声明，不计入）。 */
export function autoUpgradeCoverage(hosts: ReadonlyArray<Pick<HostView, 'auto_upgrade'>>): AutoUpgradeCoverage {
  let supported = 0
  let enabled = 0
  let silent = 0
  for (const h of hosts) {
    if (h.auto_upgrade === undefined) continue
    supported++
    if (h.auto_upgrade === 'notify' || h.auto_upgrade === 'silent') enabled++
    if (h.auto_upgrade === 'silent') silent++
  }
  return { supported, enabled, silent }
}

/** 设置页里总闸下方的一句话：打开总闸现在会影响几台主机，免得人以为它会改动所有主机。 */
export function autoUpgradeCoverageText(c: AutoUpgradeCoverage, hubSwitch: boolean | undefined): string {
  if (c.supported === 0) return '目前没有在线主机声明支持自动升级（需要较新版本、以系统服务方式运行的 Agent）。'
  if (c.enabled === 0) return `${c.supported} 台在线主机支持自动升级，但它们的所有者都还没有在本机开启，所以打开总闸暂时不会影响任何主机。`
  const tail = hubSwitch === false ? '；总闸关着，它们暂时不会检查。' : '。'
  const base = `${c.supported} 台在线主机支持自动升级，其中 ${c.enabled} 台的所有者已在本机开启${tail}`
  if (c.silent === 0) return base
  return `${base}静默模式的 ${c.silent} 台${hubSwitch === false ? '在总闸打开后' : ''}会在服务端有带发布签名的新版本时自己升级。`
}
