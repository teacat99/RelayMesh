// 懒加载块的预取（#186）：把「哪些块、什么时候悄悄取回来」从组件里拿出来，只依赖注入的环境，便于用假时钟单测。
//
// 两种用法：
//   - now(name)：悬停 / 聚焦入口时立刻取（用户很可能马上点），已取到或正在取就复用同一个 promise；
//   - idle(names)：首屏安静下来之后，按顺序一个一个在浏览器空闲时取（每个块各等一次空闲，不挤占首屏的带宽与主线程）。
// 取失败从不抛：预取只是加速，失败了用户真点开时再走正常的「加载失败 → 刷新」路径；失败的块下次 now() 还能重试。

/** 取一个块：就是 `() => import('…')`。 */
export type ChunkLoader = () => Promise<unknown>

export interface PrefetchEnv {
  /** 浏览器空闲时执行回调（requestIdleCallback；没有就退化成定时器）。 */
  whenIdle(cb: () => void): void
  /** 是否该省流量（Save-Data 或 2g 档）：为真时 idle() 整体跳过；悬停 / 聚焦的 now() 仍然照取。 */
  saveData(): boolean
}

export interface Prefetcher {
  /** 立刻取一个块；返回是否成功（未知名字、取失败都是 false）。 */
  now(name: string): Promise<boolean>
  /** 按顺序在空闲时取一批；返回全部处理完（成功、失败、跳过都算处理完）。 */
  idle(names: readonly string[]): Promise<void>
  /** 这个块是否已经取回。 */
  has(name: string): boolean
}

export function createPrefetcher(loaders: Readonly<Record<string, ChunkLoader>>, env: PrefetchEnv): Prefetcher {
  const done = new Set<string>()
  const inflight = new Map<string, Promise<boolean>>()

  function now(name: string): Promise<boolean> {
    const loader = loaders[name]
    if (!loader) return Promise.resolve(false)
    if (done.has(name)) return Promise.resolve(true)
    const running = inflight.get(name)
    if (running) return running
    let started: Promise<unknown>
    try {
      started = loader()
    } catch {
      return Promise.resolve(false)
    }
    const p = started.then(
      () => {
        done.add(name)
        return true
      },
      () => false,
    )
    const tracked = p.finally(() => {
      inflight.delete(name)
    })
    inflight.set(name, tracked)
    return tracked
  }

  async function idle(names: readonly string[]): Promise<void> {
    if (env.saveData()) return
    for (const name of names) {
      if (done.has(name) || !(name in loaders)) continue
      await new Promise<void>(resolve => env.whenIdle(resolve))
      await now(name)
    }
  }

  return { now, idle, has: name => done.has(name) }
}

/** 浏览器里的环境：空闲回调带 timeout（隐藏标签页 / 一直忙时最多等这么久），Save-Data 与 2g 档不做空闲预取。 */
export function browserPrefetchEnv(win: Window & typeof globalThis = window, idleTimeoutMs = 4000): PrefetchEnv {
  return {
    whenIdle(cb) {
      if (typeof win.requestIdleCallback === 'function') win.requestIdleCallback(() => cb(), { timeout: idleTimeoutMs })
      else win.setTimeout(cb, 200)
    },
    saveData() {
      const c = (win.navigator as Navigator & { connection?: { saveData?: boolean; effectiveType?: string } }).connection
      if (!c) return false
      return c.saveData === true || c.effectiveType === 'slow-2g' || c.effectiveType === '2g'
    },
  }
}
