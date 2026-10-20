// 会话的「执行状态」计时：纯函数，不依赖 Vue（便于单元测试，侧栏、输入区状态块共用）。
//
// 一个反馈会话在智能体（MCP 客户端）一侧的生命周期，对应四种显示：
//   剩余  MCP 客户端正挂着长轮询等你回复：倒计时（wait_countdown_minutes）走完后转为正计时；
//   等待  没有人在轮询：智能体发出空回执后在两次轮询之间休眠。计时从它最近一次空回执算起——
//         你在它休眠期间提交了回复也一样（回复已提交、还没被取走，仍是「等待」，不另设「待提取」），
//         这样始终能看出它离开了多久、什么时候该回来取；
//   执行  智能体已经取走你的回复，正在执行：从取走的时刻算起；
//   其余状态（已取消、已超时、已归档……）没有计时。

export interface SessionTimerRaw {
  status?: string
  is_mcp_active?: boolean
  mcp_active_at?: string
  wait_countdown_minutes?: number
  /** 秒级挂起倒计时（秒回 0 / 30 / 60）；-1 或缺失表示没设过，沿用分钟值。 */
  wait_countdown_seconds?: number
  /** 服务端这次长轮询实际挂多久（含用户「+1 分钟」/ 正在输入累计的延长）；有它就以它为准。 */
  mcp_timeout_seconds?: number
  last_keepalive_at?: string
  created_at?: string
  updated_at?: string
  consumed_by_ai?: boolean
  consumed_at?: string
}

export interface SessionTimerResult {
  text: string
  prefix: string
  isCountdown: boolean
}

export function formatTimerDuration(seconds: number): string {
  if (seconds < 0) seconds = 0
  const ONE_DAY = 86400
  const ONE_HOUR = 3600

  if (seconds >= ONE_DAY) {
    const days = Math.floor(seconds / ONE_DAY)
    const remainSec = seconds % ONE_DAY
    const hours = Math.floor(remainSec / ONE_HOUR)
    const mins = Math.floor((remainSec % ONE_HOUR) / 60)
    return `${days}d·${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}`
  }

  if (seconds >= ONE_HOUR) {
    const hours = Math.floor(seconds / ONE_HOUR)
    const mins = Math.floor((seconds % ONE_HOUR) / 60)
    const secs = seconds % 60
    return `${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }

  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

/** 解析 ISO 时间；空值或无法解析返回 null。 */
function parseTime(s?: string | null): number | null {
  if (!s) return null
  const t = new Date(s).getTime()
  return Number.isNaN(t) ? null : t
}

function elapsedSince(startMs: number, now: number): number {
  return Math.max(0, Math.floor((now - startMs) / 1000))
}

/** 与「回复时间」有关的会话字段（均可能缺失）。 */
export interface ReplyTimeRaw {
  responded_at?: string
  consumed_at?: string
  consumed_by_ai?: boolean
  updated_at?: string
  created_at?: string
}

/**
 * 「您的确认回复」气泡上显示的时间：用户提交回复的时刻。
 * 不能直接用 updated_at——智能体取走回复时它会被刷新，显示出来的就成了「被取走」的时间；
 * 历史会话没有 responded_at，才回退到 updated_at、created_at。
 */
export function replySentAt(s: ReplyTimeRaw | null | undefined): string | undefined {
  return s?.responded_at || s?.updated_at || s?.created_at || undefined
}

/**
 * 回复提交 → 被智能体取走之间隔了多少秒。只认真正的提交时间与取走时间：
 * 回退值（updated_at）与取走时间是同一刻，算出来恒为 0，反而误导；还没被取走、缺时间或时间倒挂（时钟偏差）时返回 null。
 */
export function pickupLagSeconds(s: ReplyTimeRaw | null | undefined): number | null {
  if (!s?.consumed_by_ai) return null
  const sent = parseTime(s.responded_at)
  const taken = parseTime(s.consumed_at)
  if (sent === null || taken === null) return null
  const sec = Math.floor((taken - sent) / 1000)
  return sec >= 0 ? sec : null
}

/** 本地时间的 HH:mm:ss（悬停说明里用，精确到秒才对得上「隔了多久」）。 */
export function formatClock(iso?: string | null): string {
  const t = parseTime(iso)
  if (t === null) return ''
  const d = new Date(t)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/** 「AI 已接收」徽标的悬停说明：几点取走、隔了多久；缺少时间（历史会话）时只说已取走。 */
export function receivedTitle(s: ReplyTimeRaw | null | undefined): string {
  const lag = pickupLagSeconds(s)
  const taken = formatClock(s?.consumed_at)
  if (!taken) return '智能体已取走这条回复'
  if (lag === null) return `智能体于 ${taken} 取走这条回复`
  // 5 秒以内就是「秒取」，不必特意强调间隔
  if (lag < 5) return `你提交后智能体几乎立刻取走（${taken}）`
  return `智能体于 ${taken} 取走，距你提交回复隔了 ${formatTimerDuration(lag)}`
}

/**
 * 给定会话与时间戳，计算要显示的计时（供侧栏卡片、输入区状态块等高频渲染使用）。
 */
export function computeSessionTimer(session: SessionTimerRaw | null | undefined, now: number, fallbackTargetMinutes = 2): SessionTimerResult {
  if (!session) {
    return { text: '00:00', prefix: '执行', isCountdown: false }
  }

  // 1. 会话仍在等你回复
  if (session.status === 'pending') {
    if (session.is_mcp_active) {
      // 1.1 MCP 客户端的长轮询正挂着
      const mcpStart = parseTime(session.mcp_active_at) ?? now
      const elapsed = elapsedSince(mcpStart, now)
      const targetMin = session.wait_countdown_minutes !== undefined && session.wait_countdown_minutes !== null ? session.wait_countdown_minutes : fallbackTargetMinutes
      let targetSec = targetMin * 60
      if (typeof session.wait_countdown_seconds === 'number' && session.wait_countdown_seconds >= 0) targetSec = session.wait_countdown_seconds
      // 服务端登记的这次挂起时长最准（含「+1 分钟」与输入暂停累计的延长）
      if (typeof session.mcp_timeout_seconds === 'number' && session.mcp_timeout_seconds > 0) targetSec = session.mcp_timeout_seconds

      if (targetSec > 0 && elapsed < targetSec) {
        return { text: formatTimerDuration(targetSec - elapsed), prefix: '剩余', isCountdown: true }
      }
      return { text: formatTimerDuration(elapsed), prefix: '剩余', isCountdown: false }
    }

    // 1.2 没有人在轮询：智能体发出空回执后休眠，从那一刻起正计时
    const keepalive = parseTime(session.last_keepalive_at)
    if (keepalive !== null) {
      return { text: formatTimerDuration(elapsedSince(keepalive, now)), prefix: '等待', isCountdown: false }
    }
    return { text: '00:00', prefix: '等待', isCountdown: false }
  }

  // 2. 你已经提交了回复
  if (session.status === 'completed') {
    // 2.1 智能体还没取走：它仍在两次轮询之间休眠。继续显示「等待」，计时仍从它最近一次空回执算起（不是从你提交的时刻），
    //     这样不会因为你提交了回复就看不到它离开了多久；没有空回执记录（如服务刚重启）时退回到回复提交的时间。
    if (!session.consumed_by_ai) {
      const since = parseTime(session.last_keepalive_at) ?? parseTime(session.updated_at) ?? parseTime(session.created_at) ?? now
      return { text: formatTimerDuration(elapsedSince(since, now)), prefix: '等待', isCountdown: false }
    }

    // 2.2 智能体已取走回复，进入执行阶段
    const execStart = parseTime(session.consumed_at) ?? parseTime(session.updated_at) ?? parseTime(session.created_at) ?? now
    return { text: formatTimerDuration(elapsedSince(execStart, now)), prefix: '执行', isCountdown: false }
  }

  return { text: '', prefix: '', isCountdown: false }
}
