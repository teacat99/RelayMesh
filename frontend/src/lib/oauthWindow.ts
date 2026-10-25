/**
 * OAuth「接入窗口」与「刚登记、还没授权的客户端」的纯逻辑（#239）：自动注册方式与策略字段的互相转换、
 * 窗口卡片的状态文案与可用按钮、清单里每条登记的标记与说明。与组件分开是为了能单元测试。
 *
 * 服务端口径见 internal/oauth：
 *   - dcr_window.go：窗口语义（默认 10 分钟、续期在剩余时间上累加、总剩余不超过 60 分钟、只存内存，Hub 重启即关闭），
 *     以及 effective.dcr 的字段含义；
 *   - policy.go：清单里「陌生来源 / 本机回环」标记用的就是批准页的同一套规则（SummarizeRedirects）。
 * 窗口只管自动注册（DCR）；CIMD 客户端（client_id 本身是 https 地址）不登记，不受窗口约束——页面与文案要如实说明。
 */
import type { OAuthClientRow, OAuthDcrMode, OAuthEffective, OAuthPolicy } from '../api/client'
import { formatLocalMinute, type Tone } from './oauthSetup'
import { formatCountdown, relativeTime } from './terminal/format'

// ───────────────────────── 自动注册方式 ─────────────────────────

export interface DcrModeOption {
  id: OAuthDcrMode
  label: string
  hint: string
}

/** 设置表单里「自动注册」的三种方式（顺序：关闭 / 限时窗口 / 常开）。 */
export const DCR_MODE_OPTIONS: readonly DcrModeOption[] = [
  {
    id: 'off',
    label: '关闭',
    hint: 'ChatGPT、Claude 连不上（它们靠自动注册连接）。只有客户端 ID 本身是 https 地址（CIMD）的客户端还能连。',
  },
  {
    id: 'window',
    label: '限时窗口（推荐）',
    hint: '平时关着；要添加新连接器时，在上面的「接入窗口」里开启一段时间（默认 10 分钟），窗口期间客户端才能自己登记。已经登记、已授权的客户端不受影响。',
  },
  {
    id: 'always',
    label: '常开',
    hint: '任何时候客户端都能自己登记，不用开窗口。公网上任何人都能向这台 Hub 登记客户端（登记本身不等于授权，仍要你点「批准」），不建议长期这样。',
  },
]

/** 策略里的两个布尔值对应哪种自动注册方式（dcr_always 只在 allow_dcr 为真时有意义；旧后端没有它，当作假）。 */
export function dcrModeOf(p: Pick<OAuthPolicy, 'allow_dcr' | 'dcr_always'>): OAuthDcrMode {
  if (!p.allow_dcr) return 'off'
  return p.dcr_always ? 'always' : 'window'
}

/** 选择一种自动注册方式，返回新的策略对象（不改入参）。 */
export function withDcrMode(p: OAuthPolicy, mode: OAuthDcrMode): OAuthPolicy {
  return { ...p, allow_dcr: mode !== 'off', dcr_always: mode === 'always' }
}

export function dcrModeLabel(mode: OAuthDcrMode): string {
  return DCR_MODE_OPTIONS.find(o => o.id === mode)?.label ?? mode
}

// ───────────────────────── 接入窗口卡片 ─────────────────────────

/** 剩余不到这么多秒，倒计时标成琥珀色并提示续期（与 ExecMesh 的发现窗口一致）。 */
export const WINDOW_WARN_SECONDS = 120

export type WindowKind =
  /** 总开关没开：窗口无从谈起 */
  | 'disabled'
  /** 自动注册方式是「关闭」 */
  | 'off'
  /** 自动注册方式是「常开」 */
  | 'always'
  /** 限时窗口，此刻开着 */
  | 'open'
  /** 限时窗口，此刻关着 */
  | 'closed'

export interface WindowView {
  kind: WindowKind
  tone: Tone
  title: string
  detail: string
  /** 窗口开着时的剩余秒数（本地按到期时间现算，随时间递减）；其余情况为 0 */
  remainingSeconds: number
  /** 剩余不足 WINDOW_WARN_SECONDS：卡片要提示「先续期」 */
  ending: boolean
  canOpen: boolean
  canExtend: boolean
  canClose: boolean
  /** 按钮上的分钟数（服务端给的默认时长） */
  minutes: number
}

/**
 * 窗口此刻还剩多少秒：以「收到状态那一刻」为基准本地递减，不依赖浏览器与服务器的时钟对得有多准。
 * fetchedAtMs 是收到 effective 时的本地时间戳，nowMs 是当前本地时间戳。
 */
export function remainingSecondsNow(remainingAtFetch: number, fetchedAtMs: number, nowMs: number): number {
  const left = (fetchedAtMs + Math.max(0, remainingAtFetch) * 1000 - nowMs) / 1000
  return Math.max(0, Math.ceil(left))
}

/**
 * 接入窗口卡片的视图。返回 null 表示不显示卡片：还没读到状态、被环境变量整个关闭，或者旧后端没有 effective.dcr。
 */
export function windowView(effective: OAuthEffective | null | undefined, fetchedAtMs: number, nowMs: number): WindowView | null {
  const dcr = effective?.dcr
  if (!effective || effective.env_off || !dcr) return null
  const minutes = dcr.default_minutes > 0 ? dcr.default_minutes : 10
  const base = { remainingSeconds: 0, ending: false, canOpen: false, canExtend: false, canClose: false, minutes }

  if (!effective.enabled) {
    return {
      ...base,
      kind: 'disabled',
      tone: 'muted',
      title: '授权服务器还没开启',
      detail: '先在下面「授权服务器设置」里勾选「启用 OAuth 授权服务器」并保存，再来开启接入窗口。',
    }
  }
  if (dcr.mode === 'off') {
    return {
      ...base,
      kind: 'off',
      tone: 'muted',
      title: '自动注册已关闭',
      detail:
        'ChatGPT、Claude 现在添加不了连接器。要接入新的客户端，在下面「授权服务器设置」里把「自动注册」改成「限时窗口」并保存。只有客户端 ID 本身是 https 地址（CIMD）的客户端不受这个限制。',
    }
  }
  if (dcr.mode === 'always') {
    return {
      ...base,
      kind: 'always',
      tone: 'warn',
      title: '常开：任何时候客户端都能自动登记',
      detail:
        '不需要开窗口。公网上任何人都能向这台 Hub 登记客户端——登记本身不等于授权，仍要你点「批准」，但「刚登记、还没授权」的清单会越积越多。想收紧的话，在下面把「自动注册」改成「限时窗口」。',
    }
  }

  const remaining = dcr.open ? remainingSecondsNow(dcr.remaining_seconds, fetchedAtMs, nowMs) : 0
  if (remaining > 0) {
    const ending = remaining <= WINDOW_WARN_SECONDS
    return {
      ...base,
      kind: 'open',
      tone: ending ? 'warn' : 'ok',
      title: `开启中 · 剩余 ${formatCountdown(remaining)}`,
      detail: ending
        ? '窗口即将关闭：还没添加完的话，先续期。'
        : '现在可以在 ChatGPT / Claude 里添加连接器。添加完回来核对下面「刚登记、还没授权」的客户端；不用了可以立即关闭。',
      remainingSeconds: remaining,
      ending,
      canExtend: remaining < dcr.max_minutes * 60 - 5,
      canClose: true,
    }
  }
  return {
    ...base,
    kind: 'closed',
    tone: 'muted',
    title: '已关闭',
    detail:
      '客户端现在自己登记会被拒绝（403）。要添加 ChatGPT / Claude 连接器，先开启窗口；已经登记、已授权的客户端不受影响。',
    canOpen: true,
  }
}

/** 接入窗口一直都要说清楚的边界：它只管自动注册，不是全局闸门。 */
export const WINDOW_SCOPE_NOTE =
  '窗口只管「客户端自动登记」。客户端 ID 本身是 https 地址（CIMD）的客户端不登记、不受窗口约束；真正的闸门是总开关，以及你在授权页点的那一下「批准」。'

// ───────────────────────── 刚登记、还没授权的客户端 ─────────────────────────

export interface ClientBadge {
  id: 'official' | 'unknown' | 'loopback'
  label: string
  tone: Tone
  title: string
}

/** 清单里每条登记的来源标记：官方（回调都在 ChatGPT / Claude 的域名下）、陌生来源、本机回环。 */
export function clientBadges(c: Pick<OAuthClientRow, 'untrusted' | 'loopback'>): ClientBadge[] {
  const out: ClientBadge[] = []
  if (c.loopback) {
    out.push({ id: 'loopback', label: '本机回环', tone: 'warn', title: '回调是本机地址（127.0.0.1 / localhost）：授权结果只会交给这台电脑上的程序，Claude Code 等本地工具是这样' })
  }
  if (c.untrusted) {
    if (!c.loopback) out.push({ id: 'unknown', label: '陌生来源', tone: 'warn', title: '回调不在 ChatGPT / Claude 的官方域名下，批准页会显示黄色提醒；不认识就删掉' })
  } else {
    out.push({ id: 'official', label: '官方来源', tone: 'ok', title: '回调在 ChatGPT / Claude 的官方域名下' })
  }
  return out
}

/** 「回调：chatgpt.com」，没有回调地址时说明。 */
export function clientHostsText(c: Pick<OAuthClientRow, 'redirect_hosts'>): string {
  const hosts = (c.redirect_hosts ?? []).filter(Boolean)
  return hosts.length ? `回调：${hosts.join('、')}` : '没有回调地址'
}

/** 「登记于 3 分钟前 · 来自 203.0.113.9」；升级前登记的没有来源地址。 */
export function clientRegisteredText(c: Pick<OAuthClientRow, 'created_at' | 'created_ip'>, nowMs: number = Date.now()): string {
  const when = relativeTime(c.created_at, nowMs)
  const ip = (c.created_ip ?? '').trim()
  return ip ? `登记于 ${when} · 来自 ${ip}` : `登记于 ${when}`
}

/** 登记时间的完整写法（悬停提示用）。 */
export function clientRegisteredTitle(c: Pick<OAuthClientRow, 'created_at'>): string {
  return formatLocalMinute(c.created_at)
}

/** 清单的空状态：按窗口此刻的样子给出下一步该做什么。 */
export function emptyClientsText(kind: WindowKind | null): string {
  switch (kind) {
    case 'open':
      return '还没有新的登记。在 ChatGPT / Claude 里添加连接器后，它会出现在这里。'
    case 'closed':
      return '没有「刚登记、还没授权」的客户端。要添加新的连接器，先开启上面的接入窗口。'
    default:
      return '没有「刚登记、还没授权」的客户端。'
  }
}
