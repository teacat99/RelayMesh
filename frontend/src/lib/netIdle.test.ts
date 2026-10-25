// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { createNetIdle, type NetIdleTimers } from './netIdle'

/** 手动推进时间的定时器：advance(ms) 把时间往前拨，到点的回调按时间顺序触发。 */
function fakeTimers() {
  let now = 0
  let nextId = 1
  const pending = new Map<number, { at: number; cb: () => void }>()
  const timers: NetIdleTimers = {
    setTimeout(cb, ms) {
      const id = nextId++
      pending.set(id, { at: now + ms, cb })
      return id
    },
    clearTimeout(h) {
      pending.delete(h as number)
    },
  }
  return {
    timers,
    pending: () => pending.size,
    advance(ms: number) {
      const target = now + ms
      for (;;) {
        let nextId2 = -1
        let nextAt = Infinity
        for (const [id, t] of pending) {
          if (t.at <= target && t.at < nextAt) {
            nextAt = t.at
            nextId2 = id
          }
        }
        if (nextId2 < 0) break
        const t = pending.get(nextId2)!
        pending.delete(nextId2)
        now = t.at
        t.cb()
      }
      now = target
    },
  }
}

/** 让已经入队的 promise 回调跑完。 */
const flush = () => new Promise<void>(r => setImmediate(r))

describe('createNetIdle（接口请求安静判断，#186）', () => {
  it('begin / end 计数；多调用 end 不会减成负数', () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    n.begin()
    n.begin()
    assert.equal(n.inflight(), 2)
    n.end()
    assert.equal(n.inflight(), 1)
    n.end()
    n.end()
    n.end()
    assert.equal(n.inflight(), 0)
    n.begin()
    assert.equal(n.inflight(), 1, '之前多调用的 end 没有留下负债')
  })

  it('一开始就没有在途请求：持续 quietMs 之后解析为 true', async () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    let result: boolean | null = null
    void n.whenQuiet(800, 30000).then(v => (result = v))
    ft.advance(799)
    await flush()
    assert.equal(result, null)
    ft.advance(1)
    await flush()
    assert.equal(result, true)
    assert.equal(ft.pending(), 0, '解析后不留任何定时器')
  })

  it('等待期间又有请求发出：重新计时，要等它结束后再安静 quietMs', async () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    let result: boolean | null = null
    void n.whenQuiet(800, 30000).then(v => (result = v))
    ft.advance(500)
    n.begin()
    ft.advance(5000)
    await flush()
    assert.equal(result, null, '请求没结束，不算安静')
    n.end()
    ft.advance(799)
    await flush()
    assert.equal(result, null)
    ft.advance(1)
    await flush()
    assert.equal(result, true)
  })

  it('请求在安静期内陆续出现：每次都重新计时', async () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    let result: boolean | null = null
    void n.whenQuiet(800, 30000).then(v => (result = v))
    for (let i = 0; i < 5; i++) {
      ft.advance(700)
      n.begin()
      n.end()
    }
    await flush()
    assert.equal(result, null)
    ft.advance(800)
    await flush()
    assert.equal(result, true)
  })

  it('一直不安静：maxWaitMs 到了解析为 false，并清掉所有定时器与监听', async () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    n.begin()
    let result: boolean | null = null
    void n.whenQuiet(800, 10000).then(v => (result = v))
    ft.advance(9999)
    await flush()
    assert.equal(result, null)
    ft.advance(1)
    await flush()
    assert.equal(result, false)
    assert.equal(ft.pending(), 0)
    // 解析后再有请求进出，不会再触发任何东西
    n.end()
    n.begin()
    n.end()
    ft.advance(60000)
    await flush()
    assert.equal(ft.pending(), 0)
  })

  it('多个等待者互不影响', async () => {
    const ft = fakeTimers()
    const n = createNetIdle(ft.timers)
    const results: Record<string, boolean | null> = { a: null, b: null }
    void n.whenQuiet(500, 30000).then(v => (results.a = v))
    ft.advance(300)
    void n.whenQuiet(1000, 30000).then(v => (results.b = v))
    ft.advance(200)
    await flush()
    assert.equal(results.a, true)
    assert.equal(results.b, null)
    ft.advance(800)
    await flush()
    assert.equal(results.b, true)
  })
})
