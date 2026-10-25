// 异步对话的「回复窗口」显示（#123）：智能体每发一条新消息，窗口累加一段；用户一回复，窗口清零；
// 窗口走完还没有消息，智能体才收尾。状态块显示的就是「剩余的等待回复时间」。

export const WINDOW_ADD_OPTIONS = [5, 10, 15] as const
export const WAIT_SLICE_OPTIONS = [5, 10, 20] as const
export const DEFAULT_WINDOW_ADD = 5
export const DEFAULT_WAIT_SLICE = 10

export type WindowKind = 'none' | 'running' | 'paused' | 'expired'

export interface WindowView {
  kind: WindowKind
  /** 状态块上的文字：mm:ss（超过 1 小时 h:mm:ss）/ 已到期 / 空闲。 */
  text: string
  /** 剩余毫秒；none / expired 为 0；paused 是攒着的时间。 */
  leftMs: number
  /** paused 时正在做的任务数。 */
  activeTasks?: number
  /** 收尾门开着时挡着收尾的未完成任务数（#232 D2）。只在门开着且暂停（或已放行）时带；门关着 / 老服务端没有这一项。 */
  gateTotal?: number
  /** paused 是由收尾门挡的（而不是老口径的「进行中 / 验证中」）。 */
  gated?: boolean
  /** 这一轮已被「本次放行」：窗口在照常倒计时，仍有 gateTotal 个未完成任务。 */
  released?: boolean
}

export interface WindowInput {
  until?: string | null
  paused?: boolean
  bankSec?: number
  activeTasks?: number
  /** 收尾门开着（#232 D2）、挡着收尾的任务总数、这一轮是否已放行。 */
  strict?: boolean
  gateTotal?: number
  released?: boolean
}

function clock(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000))
  const p = (n: number) => String(n).padStart(2, '0')
  if (sec >= 3600) return `${Math.floor(sec / 3600)}:${p(Math.floor((sec % 3600) / 60))}:${p(sec % 60)}`
  return `${p(Math.floor(sec / 60))}:${p(sec % 60)}`
}

/** 由窗口状态与此刻算出状态块要显示的内容。截止时间为空且没有暂停 = 没有在等回复。 */
export function windowView(input: WindowInput | string | null | undefined, nowMs: number): WindowView {
  const w: WindowInput = typeof input === 'string' || input == null ? { until: input } : input
  if (w.paused && (w.bankSec ?? 0) > 0) {
    const gate = w.strict ? { gated: true, gateTotal: w.gateTotal ?? w.activeTasks ?? 0 } : {}
    return { kind: 'paused', text: clock((w.bankSec as number) * 1000), leftMs: (w.bankSec as number) * 1000, activeTasks: w.activeTasks, ...gate }
  }
  if (!w.until) return { kind: 'none', text: '空闲', leftMs: 0 }
  const end = new Date(w.until).getTime()
  if (Number.isNaN(end)) return { kind: 'none', text: '空闲', leftMs: 0 }
  // 放行了、窗口在照常走：带上「仍有几个未完成任务」，状态块上标一个放行记号
  const rel = w.strict && w.released && (w.gateTotal ?? 0) > 0 ? { released: true, gateTotal: w.gateTotal } : {}
  const left = end - nowMs
  if (left <= 0) return { kind: 'expired', text: '已到期', leftMs: 0, ...rel }
  return { kind: 'running', text: clock(left), leftMs: left, ...rel }
}

export function windowTitle(v: WindowView, addMin: number, sliceMin: number): string {
  const note = v.released && v.gateTotal ? `（你已「本次放行」收尾门：仍有 ${v.gateTotal} 个未完成任务，窗口照常倒计时）` : ''
  const head =
    v.kind === 'paused' && v.gated
      ? `回复窗口已暂停（剩余 ${v.text}）：收尾门开着，本会话还有 ${v.gateTotal ?? 0} 个未完成任务（待办 / 进行中 / 验证中）挡着收尾，窗口在它们清零前不走。做完的置完成；必须等你拍板的任务会被置成「暂缓」，它们不挡窗口。点开可以看清单、「本次放行」或关掉收尾门。`
      : v.kind === 'paused'
      ? `回复窗口已暂停（剩余 ${v.text}）：智能体还有 ${v.activeTasks ?? 0} 个进行中 / 验证中的任务，窗口只在手头没有任务时才走；任务清零后从这里接着走。必须等你拍板的任务会被置成「暂缓」，它们不挡窗口。`
      : v.kind === 'running'
      ? `剩余等待回复时间 ${v.text}。你回复后清零；智能体按它决定用计时器等多久、什么时候收尾。${note}`
      : v.kind === 'expired'
        ? `回复窗口已到期：智能体读完最后一轮收件箱后就可以收尾。${note}`
        : '智能体当前没有在等你回复。它每发一条新消息，这里就会多出一段回复窗口。'
  return `${head}\n每条新消息 +${addMin} 分钟 · 单次计时器最长等 ${sliceMin} 分钟\n点右侧「+1m」表示你还在，窗口多 1 分钟；输入时倒计时自动暂停。`
}
