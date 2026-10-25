// 「接口请求安静下来了」的判断（#186）：空闲预取要等首屏的数据请求都回来、再安静一小会儿才开始，
// 否则慢网络上预取的块会和首屏数据抢带宽（1 Mbps 下 100 KB 的块就是近 1 秒）。
//
// 只数我们自己发出的接口请求（axios 的请求 / 响应拦截器里 begin / end）；实时事件流（SSE）是常驻连接，不算。
// 纯逻辑，定时器由外部注入，便于假时钟单测。

export interface NetIdleTimers {
  setTimeout(cb: () => void, ms: number): unknown
  clearTimeout(handle: unknown): void
}

export interface NetIdle {
  /** 一个请求发出。 */
  begin(): void
  /** 一个请求结束（成功、失败、取消都要调用，且每个 begin 只调用一次）。 */
  end(): void
  /** 当前在途请求数。 */
  inflight(): number
  /**
   * 在途请求数降到 0 并持续 quietMs 之后解析为 true；
   * 期间又有请求发出就重新计时；maxWaitMs 内始终没安静下来则解析为 false（调用方一般直接放弃这次预取）。
   */
  whenQuiet(quietMs: number, maxWaitMs: number): Promise<boolean>
}

export function createNetIdle(timers: NetIdleTimers): NetIdle {
  let count = 0
  const listeners = new Set<() => void>()

  const notify = () => {
    for (const l of [...listeners]) l()
  }

  return {
    begin() {
      count++
      notify()
    },
    end() {
      // 防御：多调用的 end 不能把计数减成负数，否则之后永远「安静」
      if (count > 0) count--
      notify()
    },
    inflight: () => count,
    whenQuiet(quietMs, maxWaitMs) {
      return new Promise<boolean>(resolve => {
        let quietTimer: unknown = null
        let maxTimer: unknown = null
        let settled = false

        const finish = (ok: boolean) => {
          if (settled) return
          settled = true
          if (quietTimer !== null) timers.clearTimeout(quietTimer)
          if (maxTimer !== null) timers.clearTimeout(maxTimer)
          listeners.delete(check)
          resolve(ok)
        }

        function check() {
          if (settled) return
          if (quietTimer !== null) {
            timers.clearTimeout(quietTimer)
            quietTimer = null
          }
          if (count === 0) quietTimer = timers.setTimeout(() => finish(true), quietMs)
        }

        listeners.add(check)
        maxTimer = timers.setTimeout(() => finish(false), maxWaitMs)
        check()
      })
    },
  }
}
