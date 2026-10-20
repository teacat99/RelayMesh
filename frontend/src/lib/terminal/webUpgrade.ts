import type { HostView, UpgradeJobView, WebUpgradeSessions } from '../../api/terminal'

// Web 一键升级的纯逻辑：能不能点、点之前要提醒什么、请求进行到哪一步、失败了怎么向人解释。
// 升级始终是「主机自己去取」：服务端不把程序推送到主机上；主机所有者要先在本机允许（relaymesh-agent web-upgrade on），
// 这个开关只能在主机上改，服务端与界面都无法替他打开。

/** 主机所有者在本机允许 / 关闭 / 查看「从 Web 升级」的命令。 */
export const WEB_UPGRADE_ON_COMMAND = 'relaymesh-agent web-upgrade on'
export const WEB_UPGRADE_OFF_COMMAND = 'relaymesh-agent web-upgrade off'
export const WEB_UPGRADE_STATUS_COMMAND = 'relaymesh-agent web-upgrade status'

export type WebUpgradeHost = Pick<HostView, 'online' | 'web_upgrade' | 'upgrade_available' | 'upgrade_job' | 'sessions' | 'holder' | 'model_running'>

/** 这次升级是不是主机自己发起的静默自动升级（而不是管理员在 Web 上点的）。 */
export function jobIsAuto(job: Pick<UpgradeJobView, 'origin'> | null | undefined): boolean {
  return job?.origin === 'auto'
}

/** 给人看的名字：toast 与说明里用，免得把「主机自己升级了」说成「你点的一键升级」。 */
export function jobKindLabel(job: Pick<UpgradeJobView, 'origin'>): string {
  return jobIsAuto(job) ? '静默自动升级' : '一键升级'
}

/** 请求是否还在进行（已发出 / 下载中 / 已交给升级进程）。 */
export function jobActive(job: Pick<UpgradeJobView, 'state'> | null | undefined): boolean {
  return !!job && (job.state === 'requested' || job.state === 'downloading' || job.state === 'handoff')
}

export type WebUpgradeMode =
  /** 有一次升级正在进行：显示进展，不提供按钮。 */
  | 'running'
  /** 已是服务端自带的版本（或服务端不知道自带的是哪个版本）：没什么可升级的。 */
  | 'current'
  /** 主机离线：一键升级需要 Agent 在线。 */
  | 'offline'
  /** Agent 不支持（旧版本 / 前台运行 / 系统暂不受支持）：只能用升级窗口里的命令。 */
  | 'unsupported'
  /** Agent 支持，但主机所有者没有允许。 */
  | 'owner-off'
  /** 可以点。 */
  | 'ready'

export function webUpgradeMode(h: WebUpgradeHost): WebUpgradeMode {
  if (jobActive(h.upgrade_job)) return 'running'
  if (!h.upgrade_available) return 'current'
  if (!h.online) return 'offline'
  if (!h.web_upgrade) return 'unsupported'
  if (h.web_upgrade === 'off') return 'owner-off'
  return 'ready'
}

/** 用主机数据自己算一份「升级会影响到的东西」：在点击之前就提醒；服务端在真正发请求时还会再核对一次。 */
export function localSessions(h: Pick<HostView, 'sessions' | 'holder' | 'model_running'>): WebUpgradeSessions {
  let running = 0
  let attached = 0
  let alt = 0
  for (const s of h.sessions ?? []) {
    if (s.state !== 'running') continue
    running++
    if (s.attached > 0) attached++
    if (s.alt) alt++
  }
  return { running, attached, alt, model_running: h.model_running ?? 0, persistent: !!h.holder?.persistent }
}

/** 有正在运行的会话或模型运行时，必须由管理员确认才会继续。 */
export function sessionsNeedConfirm(s: WebUpgradeSessions): boolean {
  return s.running > 0 || s.model_running > 0
}

export interface SessionWarning {
  /** 一句话：这台主机上有什么。 */
  headline: string
  /** 升级对它们意味着什么。 */
  detail: string
  /** 会话会被结束（没有独立的守护进程持有）：用更醒目的提示。 */
  severe: boolean
}

/** 点击一键升级时对「还有活动会话」的提醒。 */
export function sessionWarning(s: WebUpgradeSessions): SessionWarning {
  const parts: string[] = []
  if (s.running > 0) {
    const extra: string[] = []
    if (s.attached > 0) extra.push(`${s.attached} 个此刻有人正在查看`)
    if (s.alt > 0) extra.push(`${s.alt} 个正在运行全屏程序（如 vim）`)
    parts.push(`${s.running} 个运行中的终端会话${extra.length ? `（其中${extra.join('、')}）` : ''}`)
  }
  if (s.model_running > 0) parts.push(`${s.model_running} 个进行中的模型运行`)
  const headline = `这台主机上有 ${parts.join('，以及 ')}。`
  if (s.persistent) {
    const runs = s.model_running > 0 ? '进行中的模型运行同样由守护进程持有，Agent 重新连上后会自动对账。' : ''
    return {
      headline,
      detail: `会话由独立的守护进程持有，升级只重启 Agent：会话和里面的程序不会中断；已经打开的终端页面会短暂断开，Agent 回来后自动重连。${runs}`,
      severe: false,
    }
  }
  return {
    headline,
    detail: '这台主机没有独立的会话守护进程，会话与 Agent 同生共死：升级重启 Agent 会结束这些会话，里面没有保存的工作会丢失。建议等它们结束后再升级。',
    severe: true,
  }
}

export type NoticeTone = 'progress' | 'success' | 'error' | 'warn'

export interface JobNotice {
  tone: NoticeTone
  title: string
  detail: string
}

/** 升级失败的原因（Agent 报告的稳定代码，或 Hub 判定的 rolled_back）。 */
export function failureTitle(code: string | undefined): string {
  switch (code) {
    case 'not_supported':
      return '这台主机上的 Agent 不是以可升级的常驻服务运行'
    case 'not_allowed':
      return '主机所有者没有允许从 Web 升级'
    case 'up_to_date':
      return '目标版本不比当前版本新，没有升级'
    case 'busy':
      return '这台主机上已经有一次升级在进行'
    case 'bad_request':
      return 'Agent 认为这次升级请求不合规'
    case 'download':
      return 'Agent 没能下载新版本'
    case 'checksum':
      return '下载内容的 SHA-256 与服务端给出的不一致，已丢弃'
    case 'signature':
      return '新程序没有通过发布签名验证（签名缺失、不匹配，或签名的公钥不在这台主机信任的名单里），已丢弃'
    case 'record':
      return '无法在主机上记录这次升级尝试，所以没有下载'
    case 'verify':
      return '新程序无法在这台主机上运行，或报出的版本不是预期的版本'
    case 'handoff':
      return '无法启动独立的升级进程'
    case 'apply':
      return '升级进程没能完成替换或重启'
    case 'rolled_back':
      return '新版本没有连上服务端，已自动还原为旧版本'
    default:
      return code ? `升级没有完成（${code}）` : '升级没有完成'
  }
}

/** 静默升级失败后的后续：同一个版本最多自动尝试几次，用完之后谁来重新放行。 */
const AUTO_RETRY_NOTE = '同一个版本最多自动尝试 2 次，用完后不再自动尝试；出现另一个版本，或主机所有者在本机重新执行 relaymesh-agent auto-upgrade enable，才会重新计数。'

/** 失败之后主机处于什么状态：管理员最关心的是「它现在还好吗」。 */
function failureConsequence(code: string | undefined, auto: boolean): string {
  let base: string
  switch (code) {
    case 'apply':
      base = 'Agent 仍在运行旧版本；原因请到主机上查看升级进程的日志。'
      break
    case 'rolled_back':
      base = '主机已经回到旧版本并正常连接，终端会话不受影响；新版本的问题请到主机上查看日志。'
      break
    case 'signature':
      base = '已安装的 Agent 没有被改动，主机仍在运行旧版本。请核对服务端分发的发布签名与这台主机信任的公钥（在主机上执行 relaymesh-agent auto-upgrade status 可查看）。'
      break
    default:
      base = '已安装的 Agent 没有被改动，主机仍在运行旧版本。'
  }
  return auto ? `${base}${AUTO_RETRY_NOTE}` : base
}

/** 一次请求当前的进展 / 结果，换成给人看的说明。 */
export function jobNotice(job: UpgradeJobView): JobNotice {
  const auto = jobIsAuto(job)
  if (job.state === 'done') {
    return {
      tone: 'success',
      title: `${auto ? '已自动升级' : '升级完成'}：${job.from || '旧版本'} → ${job.to}`,
      detail: `${auto ? '这是主机自己发起的静默升级：' : ''}新版本已连上服务端，这台主机的升级窗口已自动关闭。`,
    }
  }
  if (job.state === 'failed') {
    const base = failureConsequence(job.code, auto)
    return {
      tone: 'error',
      title: auto ? `静默自动升级没有成功：${failureTitle(job.code)}` : failureTitle(job.code),
      detail: job.detail ? `${job.detail}。${base}` : base,
    }
  }
  if (job.stalled) {
    return {
      tone: 'warn',
      title: '已经超过 5 分钟没有任何进展',
      detail:
        'Agent 可能还在重启，也可能升级进程出了问题：到主机上运行 systemctl status relaymesh-agent 与 journalctl -u relaymesh-agent 查看（用户级安装加 --user）。' +
        '超过 10 分钟仍没有进展时，可以重新发起。',
    }
  }
  switch (job.state) {
    case 'requested':
      return auto
        ? { tone: 'progress', title: '服务端已批准这台主机的静默升级，等待 Agent 开始下载…', detail: '' }
        : { tone: 'progress', title: '已向主机发出升级请求，等待 Agent 应答…', detail: '' }
    case 'downloading':
      return {
        tone: 'progress',
        title: `Agent 正在下载并校验 ${job.to}…`,
        detail: auto
          ? '校验通过后还会验证发布签名，再试运行新程序，确认它能在这台主机上运行；这是主机自己发起的静默升级。'
          : '校验通过后还会先试运行新程序，确认它能在这台主机上运行。',
      }
    default:
      return {
        tone: 'progress',
        title: '已交给独立的升级进程，正在替换并重启 Agent…',
        detail: `${job.to} 连上服务端后升级即完成，通常不到一分钟，最长约 90 秒；连不上时会自动还原旧版本。`,
      }
  }
}

/** 主机卡片徽标用的短说明（请求进行中 / 上一次失败了）。 */
export function jobBadgeTitle(job: UpgradeJobView): string {
  const kind = jobKindLabel(job)
  if (job.state === 'failed') return `上一次${kind}没有成功：${failureTitle(job.code)}；点击查看原因`
  return `${kind}进行中（${job.to}）；点击查看进展`
}

export interface WebUpgradeOutcome {
  host: HostView
  job: UpgradeJobView
}

/**
 * 找出这次轮询里「刚刚结束」的一键升级：同一个请求上一次还在进行、这一次已经完成或失败。
 * 刷新页面后第一次看到的已结束请求不算（那是历史，不需要再弹提示）。
 */
export function finishedWebUpgrades(prev: readonly HostView[], next: readonly HostView[]): WebUpgradeOutcome[] {
  const before = new Map(prev.map(h => [h.machine_code, h]))
  const out: WebUpgradeOutcome[] = []
  for (const h of next) {
    const job = h.upgrade_job
    if (!job || (job.state !== 'done' && job.state !== 'failed')) continue
    const was = before.get(h.machine_code)?.upgrade_job
    if (was && was.id === job.id && jobActive(was)) out.push({ host: h, job })
  }
  return out
}
