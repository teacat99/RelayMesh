// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { TAB_STATE_TTL_MS } from './notifyBus'
import {
  CHANNEL_NAME,
  HEARTBEAT_MS,
  LOCK_NAME,
  createCoord,
  type ChannelLike,
  type Coord,
  type CoordEnv,
  type LockManagerLike,
} from './notifyCoord'

// ———————————————— 假环境：时钟 / 频道 / 锁 ————————————————

interface Timer {
  at: number
  fn: () => void
  every?: number
}

class FakeClock {
  t = 1_000_000
  private seq = 0
  private timers = new Map<number, Timer>()
  now = () => this.t
  setTimeout = (fn: () => void, ms: number): unknown => {
    const id = ++this.seq
    this.timers.set(id, { at: this.t + ms, fn })
    return id
  }
  setInterval = (fn: () => void, ms: number): unknown => {
    const id = ++this.seq
    this.timers.set(id, { at: this.t + ms, fn, every: ms })
    return id
  }
  clearTimeout = (h: unknown) => void this.timers.delete(h as number)
  clearInterval = (h: unknown) => void this.timers.delete(h as number)
  /** 推进时间，期间到期的定时器按时间顺序依次触发。 */
  advance(ms: number) {
    const target = this.t + ms
    for (;;) {
      let next: [number, Timer] | null = null
      for (const entry of this.timers) {
        if (entry[1].at <= target && (!next || entry[1].at < next[1].at)) next = entry
      }
      if (!next) break
      const [id, timer] = next
      this.t = Math.max(this.t, timer.at)
      if (timer.every) timer.at += timer.every
      else this.timers.delete(id)
      timer.fn()
    }
    this.t = target
  }
}

class FakeChannel implements ChannelLike {
  onmessage: ((ev: { data: unknown }) => void) | null = null
  closed = false
  constructor(
    private hub: FakeHub,
    readonly name: string,
  ) {}
  postMessage(data: unknown) {
    if (this.closed) throw new Error('channel closed')
    for (const peer of this.hub.channels) {
      if (peer !== this && peer.name === this.name && !peer.closed && peer.onmessage) peer.onmessage({ data: structuredClone(data) })
    }
  }
  close() {
    this.closed = true
    this.hub.channels.delete(this)
  }
}

class FakeHub {
  channels = new Set<FakeChannel>()
  open(name: string) {
    const ch = new FakeChannel(this, name)
    this.channels.add(ch)
    return ch
  }
}

/** 一个浏览器里所有标签页共享的锁管理器；owner 标识是哪个标签页在请求。 */
class FakeLocks {
  private holder: { owner: string; release: () => void } | null = null
  private queue: Array<() => void> = []
  grants: string[] = []
  for(owner: string): LockManagerLike {
    return {
      request: (name, opts, cb) =>
        new Promise((resolve, reject) => {
          assert.equal(name, LOCK_NAME)
          assert.equal(opts.mode, 'exclusive')
          const grant = () => {
            // 先占住（同步），回调异步执行：真实浏览器里锁是异步授予的，但同一时刻只会授予一个
            this.holder = { owner, release: () => this.releaseHolder(owner) }
            queueMicrotask(async () => {
              try {
                const pending = Promise.resolve(cb())
                this.grants.push(owner)
                await pending
                resolve(undefined)
              } catch (e) {
                reject(e)
              } finally {
                this.releaseHolder(owner)
              }
            })
          }
          if (!this.holder) grant()
          else this.queue.push(grant)
          opts.signal?.addEventListener('abort', () => {
            const i = this.queue.indexOf(grant)
            if (i >= 0) this.queue.splice(i, 1)
            reject(new Error('AbortError'))
          })
        }),
    }
  }
  private releaseHolder(owner: string) {
    if (this.holder?.owner !== owner) return
    this.holder = null
    const next = this.queue.shift()
    next?.()
  }
  /** 模拟标签页崩溃：它持有的锁被浏览器强制释放。 */
  crash(owner: string) {
    this.holder?.owner === owner && this.releaseHolder(owner)
  }
}

const flush = () => new Promise<void>(resolve => setImmediate(resolve))

let idSeq = 0
/** 每个标签页打开的频道，按 tabId 记录；测试里用它模拟标签页「悄无声息地消失」（不发 bye）。 */
const channelsByTab = new Map<string, FakeChannel>()
function makeEnv(clock: FakeClock, hub: FakeHub | null, locks: LockManagerLike | null): CoordEnv {
  const id = `tab-${++idSeq}`
  return {
    now: clock.now,
    newId: () => id,
    openChannel: name => {
      const ch = hub?.open(name) ?? null
      if (ch) channelsByTab.set(id, ch)
      return ch
    },
    locks,
    setInterval: clock.setInterval,
    clearInterval: clock.clearInterval,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  }
}

const here = { focused: true, visible: true, conversation: 'wf:demo' }

describe('createCoord · 单标签（既没有 Web Locks 也没有 BroadcastChannel）', () => {
  it('自己就是 leader；状态里只有自己；等「已呈现」声明会超时', async () => {
    const clock = new FakeClock()
    const coord = createCoord(makeEnv(clock, null, null))
    assert.equal(coord.mode, 'solo')
    assert.equal(coord.isLeader(), true)
    coord.setSelf(here)
    const states = coord.states()
    assert.equal(states.length, 1)
    assert.deepEqual({ ...states[0], at: 0 }, { tabId: coord.tabId, ...here, at: 0 })

    const waiting = coord.waitShown('x', 1200)
    clock.advance(1200)
    assert.equal(await waiting, false)
    coord.stop()
  })
})

describe('createCoord · 心跳选举（有 BroadcastChannel、没有 Web Locks）', () => {
  function pair() {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    clock.advance(10)
    const b = createCoord(makeEnv(clock, hub, null))
    return { clock, hub, a, b }
  }

  it('启动最早的是 leader，其余不是', () => {
    const { a, b } = pair()
    assert.equal(a.mode, 'heartbeat')
    assert.equal(a.isLeader(), true)
    assert.equal(b.isLeader(), false)
  })

  it('leader 正常退出（bye）：另一个立刻接任并收到通知', () => {
    const { a, b } = pair()
    const seen: boolean[] = []
    b.onLeaderChange(v => seen.push(v))
    a.stop()
    assert.equal(b.isLeader(), true)
    assert.deepEqual(seen, [true])
  })

  it('leader 悄无声息地消失（崩溃 / 被冻结）：心跳超时后另一个接任', () => {
    const { clock, a, b } = pair()
    channelsByTab.get(a.tabId)?.close() // 不发 bye，也不再发心跳
    assert.equal(b.isLeader(), false) // 还没超时
    clock.advance(HEARTBEAT_MS)
    assert.equal(b.isLeader(), false)
    clock.advance(TAB_STATE_TTL_MS)
    assert.equal(b.isLeader(), true)
  })

  it('晚来的标签页向大家打招呼，立刻拿到已有标签页的状态', () => {
    const { clock, hub, a } = pair()
    a.setSelf(here)
    clock.advance(10)
    const c = createCoord(makeEnv(clock, hub, null))
    const stateOfA = c.states().find(s => s.tabId === a.tabId)
    assert.ok(stateOfA)
    assert.equal(stateOfA.conversation, 'wf:demo')
    assert.equal(c.isLeader(), false)
  })
})

describe('createCoord · Web Locks 选主', () => {
  it('第一个拿到锁的是 leader；它崩溃后排队的下一个接任', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const locks = new FakeLocks()
    const a = createCoord(makeEnv(clock, hub, locks.for('A')))
    const b = createCoord(makeEnv(clock, hub, locks.for('B')))
    assert.equal(a.mode, 'locks')
    assert.equal(a.isLeader(), false, '锁是异步授予的：刚创建时还不是 leader') // 锁是异步授予的
    await flush()
    assert.equal(a.isLeader(), true, '第一个请求锁的标签页拿到锁')
    assert.equal(b.isLeader(), false, `排队的标签页还不是 leader（grants=${locks.grants.join(',')}）`)

    const seen: boolean[] = []
    b.onLeaderChange(v => seen.push(v))
    locks.crash('A')
    await flush()
    assert.equal(b.isLeader(), true)
    assert.deepEqual(seen, [true])
    assert.deepEqual(locks.grants, ['A', 'B'])
  })

  it('stop() 释放锁让位给下一个', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const locks = new FakeLocks()
    const a = createCoord(makeEnv(clock, hub, locks.for('A')))
    const b = createCoord(makeEnv(clock, hub, locks.for('B')))
    await flush()
    assert.equal(a.isLeader(), true)
    a.stop()
    await flush()
    assert.equal(b.isLeader(), true)
  })
})

describe('createCoord · 前台状态汇总', () => {
  function trio(): { clock: FakeClock; a: Coord; b: Coord } {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    return { clock, a, b }
  }

  it('一个标签页的变化立刻出现在其他标签页的汇总里', () => {
    const { a, b } = trio()
    a.setSelf(here)
    const seenByB = b.states().find(s => s.tabId === a.tabId)
    assert.ok(seenByB)
    assert.deepEqual({ focused: seenByB.focused, visible: seenByB.visible, conversation: seenByB.conversation }, here)

    a.setSelf({ focused: false, visible: true, conversation: 'wf:demo' })
    assert.equal(b.states().find(s => s.tabId === a.tabId)?.focused, false)
  })

  it('汇总总是包含自己，且自己的状态是最新的', () => {
    const { b } = trio()
    b.setSelf({ focused: true, visible: true, conversation: 'wf:other' })
    const mine = b.states().find(s => s.tabId === b.tabId)
    assert.equal(mine?.conversation, 'wf:other')
  })

  it('心跳让对端一直保持新鲜；正常退出（bye）后立即消失', () => {
    const { clock, a, b } = trio()
    a.setSelf(here)
    clock.advance(TAB_STATE_TTL_MS * 3) // 期间 a 的心跳持续刷新
    assert.ok(b.states().some(s => s.tabId === a.tabId))
    a.stop()
    assert.equal(b.states().some(s => s.tabId === a.tabId), false)
  })

  it('对端失联超过 TTL：不再出现在汇总里（避免把已关闭的标签页当成「正在看」）', () => {
    const { clock, a, b } = trio()
    a.setSelf(here)
    channelsByTab.get(a.tabId)?.close()
    clock.advance(TAB_STATE_TTL_MS - 1_000)
    assert.ok(b.states().some(s => s.tabId === a.tabId))
    clock.advance(2_000)
    assert.equal(b.states().some(s => s.tabId === a.tabId), false)
  })

  it('变化时才广播：重复设置同样的状态不会刷屏', () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const spy = hub.open(CHANNEL_NAME)
    let states = 0
    spy.onmessage = ev => {
      if ((ev.data as { k?: string }).k === 'state') states++
    }
    a.setSelf(here)
    assert.equal(states, 1)
    a.setSelf({ ...here })
    assert.equal(states, 1)
    a.setSelf({ ...here, focused: false })
    assert.equal(states, 2)
  })
})

describe('createCoord · 「已呈现」声明与接手', () => {
  it('leader 声明之后，等待方立刻得到 true，不接手', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const waiting = b.waitShown('txt:s1', 1200)
    clock.advance(300)
    a.announceShown('txt:s1')
    assert.equal(await waiting, true)
    assert.equal(b.hasShown('txt:s1'), true)
    assert.equal(a.hasShown('txt:s1'), true)
    // 已经声明过的：再等直接得到 true
    assert.equal(await b.waitShown('txt:s1', 1200), true)
  })

  it('宽限内没人声明：返回 false，由等待方自己接手', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const waiting = b.waitShown('txt:s2', 1200)
    clock.advance(1199)
    clock.advance(1)
    assert.equal(await waiting, false)
    assert.equal(b.hasShown('txt:s2'), false)
  })

  it('超时之后迟到的声明不会再改变结果，也不会残留等待者', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const waiting = b.waitShown('txt:s3', 100)
    clock.advance(100)
    assert.equal(await waiting, false)
    a.announceShown('txt:s3')
    assert.equal(b.hasShown('txt:s3'), true)
    assert.equal(await b.waitShown('txt:s3', 100), true)
  })

  it('stop()：未决的等待立即得到 false，之后的消息被忽略', async () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const waiting = b.waitShown('txt:s4', 5000)
    b.stop()
    assert.equal(await waiting, false)
    a.announceShown('txt:s4')
    assert.equal(b.hasShown('txt:s4'), false)
    b.stop() // 幂等
  })

  it('「已呈现」记录有上限，旧的会被淘汰', () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    for (let i = 0; i < 600; i++) a.announceShown(`id-${i}`)
    assert.equal(a.hasShown('id-0'), false)
    assert.equal(a.hasShown('id-599'), true)
  })
})

describe('createCoord · 容错', () => {
  it('收到格式不对的消息：忽略，不抛错', () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const rogue = hub.open(CHANNEL_NAME)
    for (const bad of [null, 'x', 42, {}, { k: 'state' }, { k: 'state', tabId: 'z' }, { k: 'shown', tabId: 'z' }, { k: 'unknown', tabId: 'z' }]) {
      rogue.postMessage(bad)
    }
    assert.equal(a.states().length, 1)
    assert.equal(a.hasShown('anything'), false)
  })

  it('订阅者抛错不影响其他订阅者与协调本身', () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const seen: boolean[] = []
    b.onLeaderChange(() => {
      throw new Error('订阅者出错')
    })
    b.onLeaderChange(v => seen.push(v))
    a.stop()
    assert.deepEqual(seen, [true])
  })

  it('取消订阅之后不再收到通知', () => {
    const clock = new FakeClock()
    const hub = new FakeHub()
    const a = createCoord(makeEnv(clock, hub, null))
    const b = createCoord(makeEnv(clock, hub, null))
    const seen: boolean[] = []
    const off = b.onLeaderChange(v => seen.push(v))
    off()
    a.stop()
    assert.deepEqual(seen, [])
  })
})
