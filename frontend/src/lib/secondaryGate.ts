// 次要请求让路（#169 第三期）。
//
// 背景：打开 / 切换会话时，整张会话表（主表，生产上压缩后 0.2～0.9 MB）与几个次要请求——任务（约 101 KB）、
// 确认项（约 65 KB）、收件箱（约 33 KB）、恢复胶囊（约 20 KB）——在同一时刻一起发出，低带宽下互相抢带宽，
// 用户最想看的主表反而慢；而这几项只是页头 / 工具栏 / 时间线上的辅助信息，晚到一点点没有关系。
//
// 做法：非强制的整表加载（打开 / 切换会话）开始时「立栅栏」（hold），整表到齐（成功失败都算）时「放行」（release）；
// 次要请求发出之前问一句 wait()——栅栏没立着返回 null、立刻发（行为与以前一样，连微任务都不多等），立着就排队等放行。
// 最长等 SECONDARY_MAX_WAIT_MS：主表很慢或整表请求一直不返回时，次要请求不能饿死（到点强制放行）。
// 强制刷新（SSE 触发、刚做完变更要拿最新数据）不立栅栏，次要请求照常即时发出。
//
// 栅栏是计数的：同时有几个整表加载在途时，全部结束才放行；超时强制放行会把计数清零，之后迟到的 release 是空操作。
// 全局只有一个（模块单例）：会话页同一时刻只关心一个当前工作流，多一个工作流在途最多是少让一会儿路，不会出错。

/** 次要请求最长让路时间（毫秒）：主表再慢，次要请求也最多晚这么久。 */
export const SECONDARY_MAX_WAIT_MS = 1500

export interface SecondaryGate {
  /** 立栅栏（计数 +1）；第一次立起时开始计最长等待。 */
  hold(maxWaitMs?: number): void
  /** 放行一次（计数 -1）；计数归零时放行所有排队的次要请求。栅栏没立着时是空操作。 */
  release(): void
  /** 次要请求发出前调用：栅栏没立着返回 null（立刻发）；立着返回一个放行时兑现的 Promise。 */
  wait(): Promise<void> | null
  /** 栅栏现在是否立着。 */
  readonly held: boolean
}

export interface GateTimers {
  set: (fn: () => void, ms: number) => unknown
  clear: (handle: unknown) => void
}

const realTimers: GateTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
}

/** 创建一个栅栏（测试里可注入假定时器）。 */
export function createSecondaryGate(timers: GateTimers = realTimers): SecondaryGate {
  let depth = 0
  let timer: unknown = null
  let waiters: Array<() => void> = []

  function open() {
    depth = 0
    if (timer !== null) {
      timers.clear(timer)
      timer = null
    }
    const list = waiters
    waiters = []
    for (const resolve of list) resolve()
  }

  return {
    hold(maxWaitMs: number = SECONDARY_MAX_WAIT_MS) {
      depth++
      if (timer === null) timer = timers.set(open, maxWaitMs)
    },
    release() {
      if (depth === 0) return
      depth--
      if (depth === 0) open()
    },
    wait() {
      if (depth === 0) return null
      return new Promise<void>(resolve => waiters.push(resolve))
    },
    get held() {
      return depth > 0
    },
  }
}

/** 会话页用的全局栅栏。 */
export const secondaryGate = createSecondaryGate()

/**
 * 栅栏没立着：立刻（同步）执行 run；立着：放行之后再执行。run 里要自己核对「现在还要不要做」（等的这段时间里可能已经切走了）。
 */
export function whenSecondaryOpen(run: () => void, gate: SecondaryGate = secondaryGate): void {
  const waiting = gate.wait()
  if (waiting) void waiting.then(run)
  else run()
}
