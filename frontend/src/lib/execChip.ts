// 会话「执行状态」块（输入区第二行的矩形状态）的纯逻辑：把计时、空回执次数、提示词等待时长合并成一行简写，
// 例如 `22:28 · 1/6 · 5m`；以及右键菜单里各项的可选值。不依赖 Vue，便于单元测试。
//
// 三段含义（从左到右）：
//   22:28  计时：智能体正在等你（剩余倒计时 / 已等待），或它取走回复后执行了多久；
//   1/6    空回执：智能体已经发出的空回执次数 / 上限（∞ 表示不限）；
//   5m     提示词等待：空回执里让智能体休眠多久再回来取（分钟）。

import { formatClock, formatTimerDuration, pickupLagSeconds, type SessionTimerResult } from './sessionTimer'

export type ExecStateKind = 'countdown' | 'waiting' | 'executing' | 'idle'

/** 会话里与状态块有关的字段（均可能缺失，缺失时回落到全局设置）。 */
export interface ExecChipSession {
  status?: string
  consumed_by_ai?: boolean
  consumed_at?: string
  /** 你提交回复的时刻（悬停说明里讲清「隔了多久才被取走」用）。 */
  responded_at?: string
  wait_countdown_minutes?: number | null
  wait_countdown_seconds?: number | null
  no_feedback_checks?: number | null
  max_no_feedback_checks?: number | null
  prompt_wait_minutes?: number | null
}

/** 全局设置里与状态块有关的字段。 */
export interface ExecChipSettings {
  defaultWaitCountdownMinutes?: number | null
  defaultWaitCountdownSeconds?: number | null
  maxNoFeedbackChecks?: number | null
  promptWaitMinutes?: number | null
}

/** 挂起倒计时可选值（秒）：0 = 秒回，30 秒，1 分钟（#121）。 */
export const COUNTDOWN_OPTIONS = [0, 30, 60] as const
/** 空回执上限可选值：3 / 6 / 0（不限）。上限同时是「收尾宽限」要连续落空的轮数。 */
export const MAX_CHECKS_OPTIONS = [3, 6, 0] as const
export const PROMPT_WAIT_OPTIONS = [1, 2, 5, 10] as const

const DEFAULT_COUNTDOWN_MINUTES = 2
const DEFAULT_MAX_CHECKS = 24
const DEFAULT_PROMPT_WAIT_MINUTES = 2

const isNum = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v)

/** 倒计时分钟数：会话上的值优先（0 是有效值，表示不倒计时），其次全局默认。 */
export function effectiveCountdown(sess: ExecChipSession | null | undefined, settings: ExecChipSettings | null | undefined): number {
  if (isNum(sess?.wait_countdown_minutes)) return sess!.wait_countdown_minutes as number
  if (isNum(settings?.defaultWaitCountdownMinutes)) return settings!.defaultWaitCountdownMinutes as number
  return DEFAULT_COUNTDOWN_MINUTES
}

/** 挂起倒计时（秒）：会话上的秒级值优先，其次会话的分钟值，再其次全局的秒级 / 分钟值；都没有按 1 分钟。 */
export function effectiveCountdownSeconds(sess: ExecChipSession | null | undefined, settings: ExecChipSettings | null | undefined): number {
  if (isNum(sess?.wait_countdown_seconds) && (sess!.wait_countdown_seconds as number) >= 0) return sess!.wait_countdown_seconds as number
  if (isNum(sess?.wait_countdown_minutes)) return (sess!.wait_countdown_minutes as number) * 60
  if (isNum(settings?.defaultWaitCountdownSeconds) && (settings!.defaultWaitCountdownSeconds as number) >= 0) return settings!.defaultWaitCountdownSeconds as number
  if (isNum(settings?.defaultWaitCountdownMinutes)) return (settings!.defaultWaitCountdownMinutes as number) * 60
  return 60
}

/** 倒计时选项的写法：0 秒回、30 秒、1 分钟。 */
export function countdownOptionLabel(sec: number): string {
  if (sec <= 0) return '秒回'
  if (sec < 60) return `${sec}秒`
  return `${Math.round(sec / 60)}分钟`
}

/** 空回执上限：会话上的值优先（0 表示不限），其次全局设置。 */
export function effectiveMaxChecks(sess: ExecChipSession | null | undefined, settings: ExecChipSettings | null | undefined): number {
  if (isNum(sess?.max_no_feedback_checks)) return sess!.max_no_feedback_checks as number
  if (isNum(settings?.maxNoFeedbackChecks)) return settings!.maxNoFeedbackChecks as number
  return DEFAULT_MAX_CHECKS
}

/** 提示词等待分钟数：必须为正数；会话上没有就用全局设置。 */
export function effectivePromptWait(sess: ExecChipSession | null | undefined, settings: ExecChipSettings | null | undefined): number {
  if (isNum(sess?.prompt_wait_minutes) && (sess!.prompt_wait_minutes as number) > 0) return sess!.prompt_wait_minutes as number
  if (isNum(settings?.promptWaitMinutes) && (settings!.promptWaitMinutes as number) > 0) return settings!.promptWaitMinutes as number
  return DEFAULT_PROMPT_WAIT_MINUTES
}

/** 空回执上限的显示：0 / 负数是不限，缺失按默认的 24。 */
export function formatMaxChecks(val?: number | null): string {
  if (val === undefined || val === null) return String(DEFAULT_MAX_CHECKS)
  if (val <= 0) return '∞'
  return String(val)
}

/** 空回执上限在菜单里的写法。 */
export function maxChecksOptionLabel(v: number): string {
  return v <= 0 ? '∞ 不限' : `${v} 次`
}

/** 计时对应的状态类别，决定图标与动画。 */
export function execStateKind(timer: Pick<SessionTimerResult, 'prefix'> | null | undefined): ExecStateKind {
  switch (timer?.prefix) {
    case '剩余': return 'countdown'
    case '等待': return 'waiting'
    case '执行': return 'executing'
    default: return 'idle'
  }
}

/** 空回执额度的紧张程度：不限或还早是 ok，用到 3/4 以上是 warn，用满是 full。 */
export function checksPressure(used: number, max: number): 'ok' | 'warn' | 'full' {
  if (max <= 0 || used <= 0) return 'ok'
  if (used >= max) return 'full'
  return used / max >= 0.75 ? 'warn' : 'ok'
}

export interface ExecChipView {
  kind: ExecStateKind
  /** 计时文字；没有计时（会话已结束）时是占位的 --:-- */
  time: string
  used: number
  max: number
  maxText: string
  wait: number
  /** 合并后的简写：22:28 · 1/6 · 5m */
  text: string
  pressure: 'ok' | 'warn' | 'full'
}

export function buildExecChip(
  sess: ExecChipSession | null | undefined,
  settings: ExecChipSettings | null | undefined,
  timer: SessionTimerResult | null | undefined,
): ExecChipView {
  const kind = execStateKind(timer)
  const time = timer?.text ? timer.text : '--:--'
  const used = Math.max(0, Math.floor(sess?.no_feedback_checks ?? 0))
  const max = effectiveMaxChecks(sess, settings)
  const maxText = formatMaxChecks(max)
  const wait = effectivePromptWait(sess, settings)
  return {
    kind,
    time,
    used,
    max,
    maxText,
    wait,
    text: `${time} · ${used}/${maxText} · ${wait}m`,
    pressure: checksPressure(used, max),
  }
}

/** 鼠标悬停的完整说明：把简写三段和当前状态讲清楚。 */
export function execChipTitle(view: ExecChipView, sess: ExecChipSession | null | undefined, timer: SessionTimerResult | null | undefined): string {
  let state: string
  switch (view.kind) {
    case 'countdown':
      state = timer?.isCountdown
        ? `智能体正挂着等你回复，距这一轮结束（发出空回执）还剩 ${view.time}`
        : `智能体正挂着等你回复，已等 ${view.time}`
      break
    case 'waiting':
      state = sess?.status === 'completed'
        ? `你的回复已提交，智能体还没来取；距它最近一次空回执已过 ${view.time}`
        : `智能体在两次轮询之间休眠，距它最近一次空回执已过 ${view.time}`
      break
    case 'executing': {
      state = `智能体已取走你的回复，执行了 ${view.time}`
      // 计时从「取走」那一刻算起；它在两次轮询之间休眠时回复会先躺一会儿，这段不算执行——
      // 在这里把间隔写明白，免得对着「才 7 分钟」和自己记得的提交时间对不上。
      const lag = pickupLagSeconds(sess)
      if (lag !== null && lag >= 5) {
        state += `\n你 ${formatClock(sess?.responded_at)} 提交回复，它隔了 ${formatTimerDuration(lag)} 才来取；计时从取走时算起`
      }
      break
    }
    default:
      state = '这个会话已经结束，没有计时'
  }
  const checks = view.max <= 0 ? `空回执 ${view.used} 次（不限上限）` : `空回执 ${view.used}/${view.max}`
  return `${state}\n${checks} · 提示词等待 ${view.wait} 分钟\n右键或点击调整`
}
