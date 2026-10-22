// 浏览器侧的记录读取闸门。
//
// 终端记录放在主机上，每次读取都要穿过 Hub 到主机磁盘；主机同一时刻只服务很少几个读取，超出就立刻回「主机正忙」。
// 同一个浏览器里常常同时开着几个回放标签（刷新、主机重新上线时会一起重新加载），不排队就会互相挤掉。
// 这里按主机排队（每台主机最多同时放行 slots 个），遇到「主机正忙」再按退避间隔重试几次，
// 这样用户看到的是「稍慢一点」，而不是一个需要手动重试的错误。

export interface ReadGateOptions {
  /** 每台主机同时放行的读取数（应与主机端的并发上限一致）。 */
  slots?: number
  /** 「主机正忙」时的最大重试次数。 */
  maxRetries?: number
  /** 第一次重试前的等待毫秒数，之后每次翻倍。 */
  baseDelayMs?: number
  /** 退避等待的上限。 */
  maxDelayMs?: number
  /** 判断一个错误是不是「主机正忙」（可重试）。 */
  isBusy: (e: unknown) => boolean
  /** 可注入的等待函数（测试用）。 */
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>
}

export const DEFAULT_READ_SLOTS = 2

function abortError(signal?: AbortSignal): Error {
  const reason = signal?.reason
  if (reason instanceof Error) return reason
  const e = new Error('请求已取消')
  e.name = 'AbortError'
  return e
}

/** 等待 ms 毫秒；signal 触发时提前以 AbortError 结束。 */
export function sleepAbortable(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortError(signal))
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      reject(abortError(signal))
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

interface Lane {
  active: number
  waiters: Array<() => void>
}

export class ReadGate {
  private readonly lanes = new Map<string, Lane>()
  private readonly slots: number
  private readonly maxRetries: number
  private readonly baseDelayMs: number
  private readonly maxDelayMs: number
  private readonly isBusy: (e: unknown) => boolean
  private readonly sleep: (ms: number, signal?: AbortSignal) => Promise<void>

  constructor(o: ReadGateOptions) {
    this.slots = Math.max(1, o.slots ?? DEFAULT_READ_SLOTS)
    this.maxRetries = Math.max(0, o.maxRetries ?? 6)
    this.baseDelayMs = Math.max(1, o.baseDelayMs ?? 300)
    this.maxDelayMs = Math.max(this.baseDelayMs, o.maxDelayMs ?? 4000)
    this.isBusy = o.isBusy
    this.sleep = o.sleep ?? sleepAbortable
  }

  /** 某台主机上正在执行 / 排队的读取数（测试与诊断用）。 */
  load(key: string): { active: number; queued: number } {
    const l = this.lanes.get(key)
    return { active: l?.active ?? 0, queued: l?.waiters.length ?? 0 }
  }

  /**
   * 在主机 key 的队列里执行 fn。fn 因「主机正忙」失败时，释放名额、退避等待后重新排队再试；
   * 其他错误原样抛出。退避期间不占用名额，避免一个在等待的读取拖住别人。
   */
  async run<T>(key: string, fn: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    for (let attempt = 0; ; attempt++) {
      await this.acquire(key, signal)
      try {
        return await fn()
      } catch (e) {
        if (!this.isBusy(e) || attempt >= this.maxRetries || signal?.aborted) throw e
      } finally {
        this.release(key)
      }
      await this.sleep(Math.min(this.maxDelayMs, this.baseDelayMs * 2 ** attempt), signal)
    }
  }

  private acquire(key: string, signal?: AbortSignal): Promise<void> {
    if (signal?.aborted) return Promise.reject(abortError(signal))
    let lane = this.lanes.get(key)
    if (!lane) {
      lane = { active: 0, waiters: [] }
      this.lanes.set(key, lane)
    }
    if (lane.active < this.slots) {
      lane.active++
      return Promise.resolve()
    }
    const l = lane
    return new Promise<void>((resolve, reject) => {
      const wake = () => {
        signal?.removeEventListener('abort', onAbort)
        resolve()
      }
      const onAbort = () => {
        const i = l.waiters.indexOf(wake)
        if (i >= 0) l.waiters.splice(i, 1)
        reject(abortError(signal))
      }
      l.waiters.push(wake)
      signal?.addEventListener('abort', onAbort, { once: true })
    })
  }

  private release(key: string): void {
    const lane = this.lanes.get(key)
    if (!lane) return
    const next = lane.waiters.shift()
    if (next) {
      // 名额直接转交给队首，active 不变。
      next()
      return
    }
    lane.active--
    if (lane.active <= 0 && lane.waiters.length === 0) this.lanes.delete(key)
  }
}
