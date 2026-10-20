// 浏览器终端连接的重连策略：关闭码 / 票据错误 → 下一步动作。纯逻辑，便于单元测试。

import { CLOSE } from './frames'

export type Action =
  | { kind: 'retry'; delayMs: number; message: string }
  | { kind: 'end'; message: string }
  /**
   * 需要人工处理。transient 为 true 表示只是传输层（网络 / 服务端暂时不可用）连续失败太多次而暂停重试：
   * 环境一旦好转（网络恢复、标签页重新可见）值得自动再试；总闸关闭、主机被停用、登录失效、用户取消解锁这类
   * 不是暂时性的，不会被悄悄重连。
   */
  | { kind: 'fail'; message: string; transient?: boolean }

const BASE_DELAY = 500
const MAX_DELAY = 10_000
const SLOW_DELAY = 5_000

/**
 * 连续取不到票据多少次之后暂停自动重试。退避封顶 10 秒，30 次约 4 分半：
 * 覆盖服务端重启、反代重载、手机进出信号盲区这类「几十秒到几分钟」的中断，又不会对一个长期不可用的服务端无限敲门。
 * 暂停后网络恢复 / 标签页重新可见仍会自动再试（见 TerminalConnection.resumeIfPossible），也可以手动点「重试」。
 */
export const MAX_TICKET_FAILURES = 30

/** 指数退避（500ms → 10s 封顶）加 ±20% 抖动；attempt 从 0 开始。 */
export function backoff(attempt: number, random: () => number = Math.random): number {
  const exp = Math.min(Math.max(0, Math.floor(attempt)), 10)
  const base = Math.min(MAX_DELAY, BASE_DELAY * 2 ** exp)
  const jitter = 1 + (random() * 2 - 1) * 0.2
  return Math.round(base * jitter)
}

/**
 * 因「跟不上输出」被断开之后，等多久再全新附着：给程序一点时间把这一阵的输出（常常是尺寸变化后整段对话的重画）打完，
 * 重连时拿到的就是打完之后的最新屏幕。连续被断开（程序一直在高速输出）就逐次放慢，封顶 6 秒，避免在慢链路上反复闪屏。
 * streak 是连续被断开的次数（含本次，从 1 开始）。
 */
export function lagDelay(streak: number, random: () => number = Math.random): number {
  const n = Math.min(Math.max(1, Math.floor(streak)), 6)
  const base = Math.min(6_000, 1_500 * 2 ** (n - 1))
  const jitter = 1 + (random() * 2 - 1) * 0.1
  return Math.round(base * jitter)
}

/**
 * 全新附着的回放渲染完之后，实时输出若还在高速涌来（典型：附着时的尺寸调整让程序把整段对话重画一遍，回放之后紧跟着就是重画的尾巴），
 * 载入遮罩盖上 / 继续盖着，等输出平静下来再揭开，用户就不会看着重画一路滚过屏幕。
 * 每隔 pollMs 看一次这一段里收到的实时输出：不到 busyBytes（约 120 KB/s，远高于正常的流式输出）算平静；
 * 回放刚渲染完时已经很忙就直接盖着（hold），否则先观察 watchMs：期间一旦忙起来就盖上；盖着之后最多 maxMs，再往后服务端的「跟不上」保护（lag）接手。
 */
export const SETTLE = { pollMs: 100, busyBytes: 12 * 1024, watchMs: 600, maxMs: 3_000 } as const

export function settleVerdict(deltaBytes: number, elapsedMs: number): 'busy' | 'calm' | 'cap' {
  if (deltaBytes < SETTLE.busyBytes) return 'calm'
  return elapsedMs >= SETTLE.maxMs ? 'cap' : 'busy'
}

export const LAGGING_MESSAGE = '输出速度超过了连接能力，已跳到最新输出（中间的内容可在历史里查看）'

/** 慢速重试：用于限流 / 主机离线这类「立刻重试没有意义」的情形。 */
export function slowDelay(attempt: number, random: () => number = Math.random): number {
  return Math.max(SLOW_DELAY, backoff(attempt, random))
}

/**
 * WebSocket 被关闭后该怎么办。reason 是服务端随关闭帧给出的文字（若有）。
 * 4414（跟不上输出）时 attempt 传「连续被断开的次数」，不是普通的重连次数（见 lagDelay）。
 */
export function classifyClose(code: number, attempt: number, reason = '', random: () => number = Math.random): Action {
  switch (code) {
    case CLOSE.lagging:
      return { kind: 'retry', delayMs: lagDelay(attempt, random), message: LAGGING_MESSAGE }
    case CLOSE.gateClosed:
      return { kind: 'fail', message: reason || '终端总闸已关闭（凭据被修改或服务端停用了终端功能）' }
    case CLOSE.disabled:
      return { kind: 'fail', message: reason || '该主机已被停用' }
    case CLOSE.sessionEnded:
      return { kind: 'end', message: reason || '会话已结束' }
    case CLOSE.stalled:
      return { kind: 'retry', delayMs: 200, message: reason || '渲染跟不上输出，已断开，正在重连' }
    case CLOSE.superseded:
      return { kind: 'retry', delayMs: 300, message: reason || '主机重新连接，正在重连' }
    case CLOSE.rateLimited:
      return { kind: 'retry', delayMs: slowDelay(attempt, random), message: reason || '连接过于频繁，稍后重试' }
    case CLOSE.notFound:
      return { kind: 'retry', delayMs: slowDelay(attempt, random), message: reason || '主机不在线或会话不存在，稍后重试' }
    case CLOSE.invalidHandshake:
      // 票据过期 / 已被使用：换一张新票据立刻重试即可，但连续失败要退避，避免打满服务端。
      return { kind: 'retry', delayMs: attempt === 0 ? 100 : backoff(attempt, random), message: reason || '连接凭据失效，正在重连' }
    default:
      return { kind: 'retry', delayMs: backoff(attempt, random), message: reason || '连接中断，正在重连' }
  }
}

export interface TicketFailure {
  /** Hub 返回的 code 字段（若有）。 */
  code?: string
  /** HTTP 状态码；0 / undefined 表示没有拿到响应（网络错误）。 */
  status?: number
  /** 用户在解锁对话框里取消了。 */
  cancelled?: boolean
  message?: string
}

/** 申请票据失败后该怎么办；consecutive 是连续失败次数（含本次）。 */
export function classifyTicketFailure(f: TicketFailure, attempt: number, consecutive: number, random: () => number = Math.random): Action {
  if (f.cancelled) return { kind: 'fail', message: '需要先解锁终端才能连接' }
  switch (f.code) {
    case 'session_not_found':
      return { kind: 'end', message: '会话已不存在（可能已退出被清理，或主机重启过）' }
    case 'host_offline':
      return { kind: 'retry', delayMs: slowDelay(attempt, random), message: '主机当前不在线，等待其重新连接' }
    case 'busy':
    case 'too_many':
      return { kind: 'retry', delayMs: slowDelay(attempt, random), message: '操作过于频繁，稍后重试' }
    case 'terminal_disabled':
      return { kind: 'fail', message: f.message || '终端功能当前不可用' }
    case 'auth_required':
      return { kind: 'fail', message: '登录已失效，请重新登录' }
    case 'unlock_required':
      return { kind: 'fail', message: '需要先解锁终端才能连接' }
    case 'host_not_found':
    case 'bad_machine_code':
    case 'bad_session_id':
      return { kind: 'fail', message: f.message || '主机或会话不存在' }
  }
  if (f.status === 401) return { kind: 'fail', message: '登录已失效，请重新登录' }
  if (f.status === 403) return { kind: 'fail', message: f.message || '没有权限连接该会话' }
  // 其余（网络错误、5xx、未知代码）：退避重试，连续失败太多次就暂停，不再打扰服务端。
  if (consecutive >= MAX_TICKET_FAILURES) return { kind: 'fail', message: f.message || '多次尝试仍无法建立连接', transient: true }
  return { kind: 'retry', delayMs: backoff(attempt, random), message: f.message || '无法建立连接，正在重试' }
}
