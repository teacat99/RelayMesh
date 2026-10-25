// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { browserPrefetchEnv, createPrefetcher, type ChunkLoader, type PrefetchEnv } from './prefetch'

/** 手动推进的空闲调度：每次 whenIdle 只登记回调，flush 一次放行一个。 */
function fakeEnv(opts: { saveData?: boolean } = {}) {
  const queue: Array<() => void> = []
  const env: PrefetchEnv = {
    whenIdle: cb => void queue.push(cb),
    saveData: () => opts.saveData === true,
  }
  return {
    env,
    pending: () => queue.length,
    /** 放行一个等待中的空闲回调；返回是否真的放行了。 */
    flushOne() {
      const cb = queue.shift()
      if (!cb) return false
      cb()
      return true
    },
  }
}

/** 一个可观察、可手动成功 / 失败的加载器。 */
function fakeLoader() {
  const calls: Array<{ resolve: () => void; reject: (e: Error) => void }> = []
  const loader: ChunkLoader = () =>
    new Promise<unknown>((resolve, reject) => {
      calls.push({ resolve: () => resolve({}), reject })
    })
  return { loader, calls }
}

const tick = () => new Promise<void>(r => setImmediate(r))

describe('createPrefetcher.now（悬停 / 聚焦时立刻取）', () => {
  it('未知名字：直接 false，不会调用任何加载器', async () => {
    const a = fakeLoader()
    const pf = createPrefetcher({ a: a.loader }, fakeEnv().env)
    assert.equal(await pf.now('nope'), false)
    assert.equal(a.calls.length, 0)
  })

  it('同一个块正在取时复用同一个 promise，只调用一次加载器；取到后 has 为真', async () => {
    const a = fakeLoader()
    const pf = createPrefetcher({ a: a.loader }, fakeEnv().env)
    const p1 = pf.now('a')
    const p2 = pf.now('a')
    assert.equal(a.calls.length, 1)
    assert.equal(pf.has('a'), false)
    a.calls[0].resolve()
    assert.equal(await p1, true)
    assert.equal(await p2, true)
    assert.equal(pf.has('a'), true)
  })

  it('已经取到的块再 now：不再调用加载器，直接 true', async () => {
    const a = fakeLoader()
    const pf = createPrefetcher({ a: a.loader }, fakeEnv().env)
    const p = pf.now('a')
    a.calls[0].resolve()
    await p
    assert.equal(await pf.now('a'), true)
    assert.equal(a.calls.length, 1)
  })

  it('取失败：返回 false、不抛、不记为已取到；下次 now 能重试', async () => {
    const a = fakeLoader()
    const pf = createPrefetcher({ a: a.loader }, fakeEnv().env)
    const p = pf.now('a')
    a.calls[0].reject(new Error('Failed to fetch dynamically imported module'))
    assert.equal(await p, false)
    assert.equal(pf.has('a'), false)
    const again = pf.now('a')
    assert.equal(a.calls.length, 2)
    a.calls[1].resolve()
    assert.equal(await again, true)
  })

  it('加载器同步抛错也不外泄', async () => {
    const pf = createPrefetcher(
      {
        a: () => {
          throw new Error('boom')
        },
      },
      fakeEnv().env,
    )
    assert.equal(await pf.now('a'), false)
  })
})

describe('createPrefetcher.idle（首屏安静后按顺序空闲预取）', () => {
  it('每个块各等一次空闲，按给定顺序一个一个取，不并发', async () => {
    const a = fakeLoader()
    const b = fakeLoader()
    const e = fakeEnv()
    const pf = createPrefetcher({ a: a.loader, b: b.loader }, e.env)
    const all = pf.idle(['a', 'b'])
    await tick()
    assert.equal(e.pending(), 1, '先等第一次空闲')
    assert.equal(a.calls.length, 0, '空闲没来之前不取')
    e.flushOne()
    await tick()
    assert.equal(a.calls.length, 1)
    assert.equal(b.calls.length, 0, 'a 没取完不会开始 b')
    assert.equal(e.pending(), 0, 'a 取完之前也不登记第二次空闲')
    a.calls[0].resolve()
    await tick()
    assert.equal(e.pending(), 1, 'a 取完后才为 b 等下一次空闲')
    e.flushOne()
    await tick()
    assert.equal(b.calls.length, 1)
    b.calls[0].resolve()
    await all
    assert.equal(pf.has('a'), true)
    assert.equal(pf.has('b'), true)
  })

  it('已经取到的块与未知名字直接跳过，不占空闲', async () => {
    const a = fakeLoader()
    const b = fakeLoader()
    const e = fakeEnv()
    const pf = createPrefetcher({ a: a.loader, b: b.loader }, e.env)
    const first = pf.now('a')
    a.calls[0].resolve()
    await first
    const all = pf.idle(['a', 'ghost', 'b'])
    await tick()
    assert.equal(e.pending(), 1, '只为 b 等一次空闲')
    e.flushOne()
    await tick()
    b.calls[0].resolve()
    await all
    assert.equal(a.calls.length, 1)
  })

  it('某个块取失败：继续取后面的，不整体中断', async () => {
    const a = fakeLoader()
    const b = fakeLoader()
    const e = fakeEnv()
    const pf = createPrefetcher({ a: a.loader, b: b.loader }, e.env)
    const all = pf.idle(['a', 'b'])
    await tick()
    e.flushOne()
    await tick()
    a.calls[0].reject(new Error('net'))
    await tick()
    e.flushOne()
    await tick()
    assert.equal(b.calls.length, 1)
    b.calls[0].resolve()
    await all
    assert.equal(pf.has('a'), false)
    assert.equal(pf.has('b'), true)
  })

  it('省流量（Save-Data / 2g）：idle 整体跳过、什么都不取；悬停的 now 仍然照取', async () => {
    const a = fakeLoader()
    const e = fakeEnv({ saveData: true })
    const pf = createPrefetcher({ a: a.loader }, e.env)
    await pf.idle(['a'])
    assert.equal(a.calls.length, 0)
    assert.equal(e.pending(), 0)
    const p = pf.now('a')
    assert.equal(a.calls.length, 1)
    a.calls[0].resolve()
    assert.equal(await p, true)
  })

  it('idle 排队期间用户已经悬停取过同一个块：不会重复取', async () => {
    const a = fakeLoader()
    const e = fakeEnv()
    const pf = createPrefetcher({ a: a.loader }, e.env)
    const all = pf.idle(['a'])
    await tick()
    const hover = pf.now('a')
    assert.equal(a.calls.length, 1)
    e.flushOne()
    await tick()
    assert.equal(a.calls.length, 1, '空闲到来时复用正在取的那一次')
    a.calls[0].resolve()
    await hover
    await all
  })
})

describe('browserPrefetchEnv', () => {
  function fakeWin(opts: { idle?: boolean; connection?: { saveData?: boolean; effectiveType?: string } }) {
    const calls: Array<{ kind: string; ms?: number }> = []
    const win = {
      navigator: opts.connection ? { connection: opts.connection } : {},
      setTimeout: (cb: () => void, ms: number) => {
        calls.push({ kind: 'timeout', ms })
        cb()
        return 0
      },
    } as unknown as Window & typeof globalThis
    if (opts.idle) {
      ;(win as unknown as { requestIdleCallback: unknown }).requestIdleCallback = (cb: () => void, o?: { timeout?: number }) => {
        calls.push({ kind: 'idle', ms: o?.timeout })
        cb()
        return 1
      }
    }
    return { win, calls }
  }

  it('有 requestIdleCallback 就用它，并带上超时；没有就退化成定时器', () => {
    const a = fakeWin({ idle: true })
    let ran = 0
    browserPrefetchEnv(a.win, 1234).whenIdle(() => void ran++)
    assert.deepEqual(a.calls, [{ kind: 'idle', ms: 1234 }])
    assert.equal(ran, 1)

    const b = fakeWin({ idle: false })
    browserPrefetchEnv(b.win).whenIdle(() => void ran++)
    assert.deepEqual(b.calls, [{ kind: 'timeout', ms: 200 }])
    assert.equal(ran, 2)
  })

  it('saveData：Save-Data、slow-2g、2g 为真；4g 与没有 connection（Safari / Firefox）为假', () => {
    assert.equal(browserPrefetchEnv(fakeWin({ connection: { saveData: true, effectiveType: '4g' } }).win).saveData(), true)
    assert.equal(browserPrefetchEnv(fakeWin({ connection: { effectiveType: 'slow-2g' } }).win).saveData(), true)
    assert.equal(browserPrefetchEnv(fakeWin({ connection: { effectiveType: '2g' } }).win).saveData(), true)
    assert.equal(browserPrefetchEnv(fakeWin({ connection: { effectiveType: '3g' } }).win).saveData(), false)
    assert.equal(browserPrefetchEnv(fakeWin({ connection: { effectiveType: '4g' } }).win).saveData(), false)
    assert.equal(browserPrefetchEnv(fakeWin({}).win).saveData(), false)
  })
})
