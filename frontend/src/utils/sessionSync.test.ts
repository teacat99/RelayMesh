// 运行：pnpm --dir frontend run test:unit（esbuild 打包后交给 node:test，无需额外依赖）
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { FeedbackSession } from '../api/types'
import {
  FULL_SYNC_SCOPE,
  classifySessionPayload,
  createAwayTracker,
  createKeyedFlight,
  createSyncScheduler,
  isCacheFresh,
  isSameSessionData,
  isStaleIncoming,
  mergeFetchedList,
  mergeSessionPatch,
} from './sessionSync'

function sess(id: string, updatedAt: string, extra: Partial<FeedbackSession> = {}): FeedbackSession {
  return {
    session_id: id,
    workflow_id: 'wf-1',
    summary: `summary of ${id}`,
    status: 'completed',
    created_at: '2026-09-30T10:00:00Z',
    updated_at: updatedAt,
    ...extra,
  }
}

const T1 = '2026-09-30T10:00:01.000Z'
const T2 = '2026-09-30T10:00:02.000Z'
const T3 = '2026-09-30T10:00:03.000Z'

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>(r => {
    resolve = r
  })
  return { promise, resolve }
}

const tick = () => new Promise<void>(r => setImmediate(r))

describe('classifySessionPayload', () => {
  it('完整会话（直接或被 session 包裹）归为 full', () => {
    const direct = classifySessionPayload({ session_id: 'a', status: 'pending', summary: 's' })
    assert.equal(direct.kind, 'full')
    assert.equal(direct.session?.session_id, 'a')

    const wrapped = classifySessionPayload({ status: 'ok', session: { session_id: 'b', status: 'completed' } })
    assert.equal(wrapped.kind, 'full')
    assert.equal(wrapped.sessionId, 'b')
  })

  it('只有 session_id 的 MCP 通知归为 poke', () => {
    const r = classifySessionPayload({ session_id: 'a' })
    assert.deepEqual(r, { kind: 'poke', sessionId: 'a' })
  })

  it('没有会话信息的载荷归为 none', () => {
    for (const bad of [null, undefined, 'x', 42, {}, { action: 'credentials_updated' }, { session_id: '' }, { session_id: 7 }]) {
      assert.equal(classifySessionPayload(bad).kind, 'none', JSON.stringify(bad))
    }
  })
})

describe('isStaleIncoming', () => {
  it('仅当载荷严格早于本地副本时才算过期', () => {
    assert.equal(isStaleIncoming(sess('a', T2), sess('a', T1)), true)
    assert.equal(isStaleIncoming(sess('a', T2), sess('a', T2)), false)
    assert.equal(isStaleIncoming(sess('a', T1), sess('a', T2)), false)
  })

  it('缺少本地副本或时间不可比较时保守地不算过期', () => {
    assert.equal(isStaleIncoming(undefined, sess('a', T1)), false)
    assert.equal(isStaleIncoming(null, sess('a', T1)), false)
    assert.equal(isStaleIncoming(sess('a', 'not-a-date'), sess('a', T1)), false)
    assert.equal(isStaleIncoming(sess('a', T2), sess('a', '')), false)
  })

  it('能比较 Go 输出的纳秒精度 RFC3339 时间', () => {
    const older = sess('a', '2026-09-30T22:50:44.123456789+08:00')
    const newer = sess('a', '2026-09-30T22:50:45.000000001+08:00')
    assert.equal(isStaleIncoming(newer, older), true)
    assert.equal(isStaleIncoming(older, newer), false)
  })
})

describe('mergeSessionPatch', () => {
  it('载荷字段覆盖本地，载荷没有的字段保留', () => {
    const merged = mergeSessionPatch(sess('a', T1, { title: 'old', host_name: 'h' }), sess('a', T2, { title: 'new' }))
    assert.equal(merged.title, 'new')
    assert.equal(merged.host_name, 'h')
    assert.equal(merged.updated_at, T2)
  })

  it('空值指针字段在载荷里缺席即表示已清除', () => {
    const base = sess('a', T1, {
      consumed_at: T1,
      deadline_at: T1,
      mcp_active_at: T1,
      mcp_timeout_seconds: 300,
      last_keepalive_at: T1,
      is_mcp_active: true,
    })
    const merged = mergeSessionPatch(base, sess('a', T2, { is_mcp_active: false }))
    assert.equal(merged.is_mcp_active, false)
    for (const key of ['consumed_at', 'deadline_at', 'mcp_active_at', 'mcp_timeout_seconds', 'last_keepalive_at'] as const) {
      assert.equal(key in merged, false, `${key} 应被清除`)
    }
  })

  it('载荷里带有的可清除字段会被采用', () => {
    const merged = mergeSessionPatch(sess('a', T1), sess('a', T2, { last_keepalive_at: T2, mcp_active_at: T2 }))
    assert.equal(merged.last_keepalive_at, T2)
    assert.equal(merged.mcp_active_at, T2)
  })

  it('不修改入参', () => {
    const base = sess('a', T1, { last_keepalive_at: T1 })
    mergeSessionPatch(base, sess('a', T2))
    assert.equal(base.last_keepalive_at, T1)
  })
})

describe('isSameSessionData', () => {
  it('内容一致视为相同，任一字段不同即不同', () => {
    assert.equal(isSameSessionData(sess('a', T1), sess('a', T1)), true)
    assert.equal(isSameSessionData(sess('a', T1), sess('a', T2)), false)
    assert.equal(isSameSessionData(null, sess('a', T1)), false)
    assert.equal(isSameSessionData(null, undefined), false)
    const one = sess('a', T1)
    assert.equal(isSameSessionData(one, one), true)
  })
})

describe('mergeFetchedList', () => {
  const opts = { localOnlyPosition: 'end' as const }

  it('拉取期间没有本地改动时直接采用服务端快照（同一引用）', () => {
    const fetched = [sess('a', T1), sess('b', T1)]
    assert.equal(mergeFetchedList([sess('a', T1)], fetched, new Set(), opts), fetched)
    assert.equal(mergeFetchedList(undefined, fetched, new Set(['a']), opts), fetched)
    assert.equal(mergeFetchedList([], fetched, new Set(['a']), opts), fetched)
  })

  it('被改动过的会话：本地更新则保留本地，服务端更新则采用服务端', () => {
    const localNewer = sess('a', T3, { status: 'pending' })
    const remoteOlder = sess('a', T2, { status: 'completed' })
    const out = mergeFetchedList([localNewer, sess('b', T1)], [remoteOlder, sess('b', T2, { title: 'fresh' })], new Set(['a']), opts)
    assert.equal(out[0], localNewer)
    assert.equal(out[1].title, 'fresh', '未被改动的会话以服务端为准')

    const remoteNewer = sess('a', T3, { status: 'completed' })
    const out2 = mergeFetchedList([sess('a', T2, { status: 'pending' })], [remoteNewer], new Set(['a']), opts)
    assert.equal(out2[0], remoteNewer)
  })

  it('时间相同或无法比较时偏向本地（本地刚刚打过补丁）', () => {
    const mine = sess('a', T2, { title: 'mine' })
    assert.equal(mergeFetchedList([mine], [sess('a', T2, { title: 'remote' })], new Set(['a']), opts)[0], mine)
    const weird = sess('a', 'bad', { title: 'mine' })
    assert.equal(mergeFetchedList([weird], [sess('a', T3, { title: 'remote' })], new Set(['a']), opts)[0], weird)
  })

  it('拉取期间被本地删除的会话不会因旧快照复活', () => {
    const out = mergeFetchedList([sess('b', T1)], [sess('a', T1), sess('b', T1)], new Set(['a']), opts)
    assert.deepEqual(
      out.map(s => s.session_id),
      ['b']
    )
  })

  it('本地新增而快照里还没有的会话按位置保留；未被改动的本地独有会话视为已在服务端删除', () => {
    const fresh = sess('new', T3)
    const stale = sess('gone', T1)
    const fetched = [sess('a', T2)]

    const atEnd = mergeFetchedList([sess('a', T1), fresh, stale], fetched, new Set(['new']), { localOnlyPosition: 'end' })
    assert.deepEqual(
      atEnd.map(s => s.session_id),
      ['a', 'new']
    )

    const atStart = mergeFetchedList([sess('a', T1), fresh, stale], fetched, new Set(['new']), { localOnlyPosition: 'start' })
    assert.deepEqual(
      atStart.map(s => s.session_id),
      ['new', 'a']
    )
  })

  it('结果顺序以服务端快照为准', () => {
    const out = mergeFetchedList([sess('b', T1), sess('a', T1)], [sess('a', T1), sess('b', T1), sess('c', T1)], new Set(['b']), opts)
    assert.deepEqual(
      out.map(s => s.session_id),
      ['a', 'b', 'c']
    )
  })
})

describe('createKeyedFlight', () => {
  function makeFlight() {
    const gates: Array<ReturnType<typeof deferred>> = []
    const stats = { calls: 0, active: 0, maxActive: 0, errors: [] as unknown[] }
    const flight = createKeyedFlight(
      async () => {
        stats.calls++
        stats.active++
        stats.maxActive = Math.max(stats.maxActive, stats.active)
        const gate = deferred()
        gates.push(gate)
        try {
          await gate.promise
        } finally {
          stats.active--
        }
      },
      (_key, error) => stats.errors.push(error)
    )
    return { flight, gates, stats }
  }

  it('并发的普通请求共享同一次执行', async () => {
    const { flight, gates, stats } = makeFlight()
    const p1 = flight.request('wf')
    const p2 = flight.request('wf')
    const p3 = flight.request('wf')
    assert.equal(stats.calls, 1)
    assert.equal(flight.isBusy('wf'), true)
    gates[0].resolve()
    await Promise.all([p1, p2, p3])
    assert.equal(stats.calls, 1)
    assert.equal(flight.isBusy('wf'), false)
  })

  it('在途时的 fresh 请求合并为一个尾随执行，且两次执行绝不并发', async () => {
    const { flight, gates, stats } = makeFlight()
    const first = flight.request('wf')
    const trailingA = flight.request('wf', true)
    const trailingB = flight.request('wf', true)
    assert.equal(trailingA, trailingB, '多个 fresh 请求应共用同一个尾随')
    assert.equal(stats.calls, 1, '尾随必须等在途执行结束才开始')

    gates[0].resolve()
    await first
    await tick()
    assert.equal(stats.calls, 2)
    assert.equal(flight.isBusy('wf'), true)

    gates[1].resolve()
    await trailingA
    assert.equal(stats.calls, 2)
    assert.equal(stats.maxActive, 1)
    assert.equal(flight.isBusy('wf'), false)
  })

  it('尾随排队期间的普通请求加入尾随，在尾随完成时才 resolve', async () => {
    const { flight, gates } = makeFlight()
    const first = flight.request('wf')
    const trailing = flight.request('wf', true)
    const joined = flight.request('wf')
    assert.equal(joined, trailing)

    let joinedDone = false
    void joined.then(() => {
      joinedDone = true
    })
    gates[0].resolve()
    await first
    await tick()
    assert.equal(joinedDone, false)
    gates[1].resolve()
    await joined
    assert.equal(joinedDone, true)
  })

  it('执行结束后再次请求会重新发起', async () => {
    const { flight, gates, stats } = makeFlight()
    const p1 = flight.request('wf')
    gates[0].resolve()
    await p1
    const p2 = flight.request('wf')
    assert.equal(stats.calls, 2)
    gates[1].resolve()
    await p2
  })

  it('不同 key 互不影响', async () => {
    const { flight, gates, stats } = makeFlight()
    const a = flight.request('a')
    const b = flight.request('b')
    assert.equal(stats.calls, 2)
    assert.equal(stats.maxActive, 2)
    gates[0].resolve()
    gates[1].resolve()
    await Promise.all([a, b])
  })

  it('任务抛错交给 onError，返回的 Promise 不会 reject，后续请求仍可执行', async () => {
    const errors: unknown[] = []
    let calls = 0
    const flight = createKeyedFlight(
      async () => {
        calls++
        if (calls === 1) throw new Error('boom')
      },
      (_key, error) => errors.push(error)
    )
    await flight.request('wf')
    assert.equal(errors.length, 1)
    assert.equal(flight.isBusy('wf'), false)
    await flight.request('wf')
    assert.equal(calls, 2)
  })

  it('尾随执行前已有别的调用开启了新执行时，直接加入它而不是再开一个', async () => {
    const { flight, gates, stats } = makeFlight()
    const first = flight.request('wf')
    // 这个回调先于尾随的回调注册，因此在在途执行结束、尾随回调运行之前的间隙里抢先发起一次新的普通请求
    let interloper: Promise<void> = Promise.resolve()
    void first.then(() => {
      interloper = flight.request('wf')
    })
    const trailing = flight.request('wf', true)

    gates[0].resolve()
    await first
    await tick()
    assert.equal(stats.calls, 2, '只应存在一次后续执行，尾随必须加入抢先发起的那次')
    gates[1].resolve()
    await Promise.all([trailing, interloper])
    assert.equal(stats.maxActive, 1)
    assert.equal(stats.calls, 2)
  })
})

function fakeClock() {
  let t = 0
  let nextId = 1
  const timers = new Map<number, { at: number; fn: () => void }>()
  return {
    now: () => t,
    setTimer: (fn: () => void, ms: number) => {
      const id = nextId++
      timers.set(id, { at: t + ms, fn })
      return id
    },
    clearTimer: (handle: unknown) => {
      timers.delete(handle as number)
    },
    advance(ms: number) {
      const target = t + ms
      for (;;) {
        const due = [...timers.entries()].filter(([, v]) => v.at <= target).sort((a, b) => a[1].at - b[1].at)[0]
        if (!due) break
        t = due[1].at
        timers.delete(due[0])
        due[1].fn()
      }
      t = target
    },
    at(ms: number) {
      t = ms
    },
  }
}

describe('createSyncScheduler', () => {
  it('同一窗口内的多次请求合并为一次，范围取并集', () => {
    const clock = fakeClock()
    const flushed: unknown[] = []
    const s = createSyncScheduler(scope => flushed.push(scope), clock)
    s.schedule({ queued: true })
    clock.advance(50)
    s.schedule({ current: true })
    clock.advance(119)
    assert.equal(flushed.length, 0)
    clock.advance(1)
    assert.deepEqual(flushed, [{ current: true, sessions: false, queued: true, workflow: false }])
  })

  it('去抖：新请求会把执行时间往后推', () => {
    const clock = fakeClock()
    let flushedAt = -1
    const s = createSyncScheduler(() => (flushedAt = clock.now()), clock)
    s.schedule({ current: true })
    clock.advance(100)
    s.schedule({ current: true })
    clock.advance(119)
    assert.equal(flushedAt, -1)
    clock.advance(1)
    assert.equal(flushedAt, 220)
  })

  it('最大等待：持续不断的请求也会在上限处被强制执行', () => {
    const clock = fakeClock()
    const flushedAt: number[] = []
    const s = createSyncScheduler(() => flushedAt.push(clock.now()), { ...clock, delayMs: 120, maxWaitMs: 1000 })
    for (let i = 0; i <= 12; i++) {
      s.schedule({ workflow: true })
      clock.advance(100)
    }
    assert.ok(flushedAt.length >= 1, '应至少强制执行一次')
    assert.equal(flushedAt[0], 1000)
  })

  it('空范围不会触发执行，也不会占用窗口', () => {
    const clock = fakeClock()
    let count = 0
    const s = createSyncScheduler(() => count++, clock)
    s.schedule({})
    clock.advance(10_000)
    assert.equal(count, 0)
  })

  it('flushNow 立即执行，cancel 丢弃待处理请求', () => {
    const clock = fakeClock()
    const flushed: unknown[] = []
    const s = createSyncScheduler(scope => flushed.push(scope), clock)
    s.schedule(FULL_SYNC_SCOPE)
    s.flushNow()
    assert.deepEqual(flushed, [FULL_SYNC_SCOPE])
    s.schedule({ sessions: true })
    s.cancel()
    clock.advance(10_000)
    assert.equal(flushed.length, 1)
  })
})

describe('createAwayTracker', () => {
  it('离开不足阈值不校准，达到阈值才校准，并且每次复位', () => {
    const clock = fakeClock()
    const tracker = createAwayTracker(30_000, clock.now)
    clock.at(1000)
    tracker.markInactive()
    clock.at(1000 + 29_999)
    assert.equal(tracker.markActive(), false)

    clock.at(100_000)
    tracker.markInactive()
    clock.at(100_000 + 30_000)
    assert.equal(tracker.markActive(), true)
    assert.equal(tracker.markActive(), false, '复位后再次激活不应重复触发')
  })

  it('没有先离开的激活事件不触发校准', () => {
    const clock = fakeClock()
    const tracker = createAwayTracker(30_000, clock.now)
    clock.at(10 * 60_000)
    assert.equal(tracker.markActive(), false)
  })

  it('重复的离开事件保留最早时间点', () => {
    const clock = fakeClock()
    const tracker = createAwayTracker(30_000, clock.now)
    clock.at(1000)
    tracker.markInactive()
    clock.at(20_000)
    tracker.markInactive()
    clock.at(31_000)
    assert.equal(tracker.markActive(), true)
  })
})

describe('isCacheFresh', () => {
  const ttl = 5 * 60_000
  it('未同步过、断线后未重新同步、超过 TTL 都不新鲜', () => {
    assert.equal(isCacheFresh(undefined, 0, 1000, ttl), false)
    assert.equal(isCacheFresh(1000, 1000, 2000, ttl), false)
    assert.equal(isCacheFresh(1000, 2000, 3000, ttl), false)
    assert.equal(isCacheFresh(1000, 0, 1000 + ttl, ttl), false)
  })

  it('断线之后同步且在 TTL 内才新鲜', () => {
    assert.equal(isCacheFresh(2000, 1000, 2000 + ttl - 1, ttl), true)
    assert.equal(isCacheFresh(5, 0, 6, ttl), true)
  })
})
