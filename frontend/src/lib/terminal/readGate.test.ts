// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { ReadGate, sleepAbortable } from './readGate'

class Busy extends Error {
  code = 'busy'
}
const isBusy = (e: unknown) => (e as { code?: string })?.code === 'busy'

/** 手动放行的任务：测试里用它精确控制「读取何时结束」。 */
function deferred<T = void>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const tick = () => new Promise<void>(r => setImmediate(r))

describe('ReadGate 并发名额', () => {
  it('同一台主机最多同时放行 slots 个，其余按先后顺序排队', async () => {
    const gate = new ReadGate({ slots: 2, isBusy })
    const order: string[] = []
    const gates = [deferred(), deferred(), deferred(), deferred()]
    const jobs = gates.map((g, i) =>
      gate.run('h1', async () => {
        order.push(`start${i}`)
        await g.promise
        order.push(`end${i}`)
        return i
      }),
    )
    await tick()
    assert.deepEqual(order, ['start0', 'start1'])
    assert.deepEqual(gate.load('h1'), { active: 2, queued: 2 })

    gates[0].resolve()
    await tick()
    assert.deepEqual(order, ['start0', 'start1', 'end0', 'start2'])

    gates[1].resolve()
    gates[2].resolve()
    await tick()
    gates[3].resolve()
    assert.deepEqual(await Promise.all(jobs), [0, 1, 2, 3])
    assert.deepEqual(gate.load('h1'), { active: 0, queued: 0 })
  })

  it('不同主机互不影响', async () => {
    const gate = new ReadGate({ slots: 1, isBusy })
    const a = deferred()
    const started: string[] = []
    const ja = gate.run('a', async () => {
      started.push('a')
      await a.promise
    })
    const jb = gate.run('b', async () => {
      started.push('b')
    })
    await jb
    assert.deepEqual(started, ['a', 'b'])
    a.resolve()
    await ja
  })

  it('任务失败也会归还名额', async () => {
    const gate = new ReadGate({ slots: 1, isBusy })
    await assert.rejects(
      gate.run('h', async () => {
        throw new Error('boom')
      }),
      /boom/,
    )
    assert.equal(await gate.run('h', async () => 7), 7)
    assert.deepEqual(gate.load('h'), { active: 0, queued: 0 })
  })
})

describe('ReadGate 忙时重试', () => {
  it('主机正忙时退避后重试，成功就返回结果', async () => {
    const waits: number[] = []
    const gate = new ReadGate({
      slots: 2,
      isBusy,
      baseDelayMs: 100,
      maxDelayMs: 1000,
      sleep: async ms => {
        waits.push(ms)
      },
    })
    let calls = 0
    const r = await gate.run('h', async () => {
      calls++
      if (calls < 4) throw new Busy('主机正忙')
      return 'ok'
    })
    assert.equal(r, 'ok')
    assert.equal(calls, 4)
    assert.deepEqual(waits, [100, 200, 400])
  })

  it('退避间隔有上限', async () => {
    const waits: number[] = []
    const gate = new ReadGate({ isBusy, maxRetries: 5, baseDelayMs: 100, maxDelayMs: 300, sleep: async ms => void waits.push(ms) })
    await assert.rejects(
      gate.run('h', async () => {
        throw new Busy('忙')
      }),
      Busy,
    )
    assert.deepEqual(waits, [100, 200, 300, 300, 300])
  })

  it('超过重试次数后把「忙」原样抛出', async () => {
    const gate = new ReadGate({ isBusy, maxRetries: 2, sleep: async () => {} })
    let calls = 0
    await assert.rejects(
      gate.run('h', async () => {
        calls++
        throw new Busy('忙')
      }),
      Busy,
    )
    assert.equal(calls, 3)
  })

  it('不是「忙」的错误不重试', async () => {
    const gate = new ReadGate({ isBusy, sleep: async () => assert.fail('不该退避') })
    let calls = 0
    await assert.rejects(
      gate.run('h', async () => {
        calls++
        throw new Error('record_gone')
      }),
      /record_gone/,
    )
    assert.equal(calls, 1)
  })

  it('退避期间不占用名额', async () => {
    const sleeping = deferred()
    const gate = new ReadGate({
      slots: 1,
      isBusy,
      sleep: () => sleeping.promise,
    })
    let first = 0
    const j1 = gate.run('h', async () => {
      first++
      if (first === 1) throw new Busy('忙')
      return 'first-ok'
    })
    await tick()
    // j1 正在退避：名额应当空着，别的读取可以立刻执行。
    assert.deepEqual(gate.load('h'), { active: 0, queued: 0 })
    assert.equal(await gate.run('h', async () => 'other'), 'other')
    sleeping.resolve()
    assert.equal(await j1, 'first-ok')
  })
})

describe('ReadGate 取消', () => {
  it('排队中被取消：移出队列，并且不占名额', async () => {
    const gate = new ReadGate({ slots: 1, isBusy })
    const hold = deferred()
    const running = gate.run('h', () => hold.promise)
    const ac = new AbortController()
    const queued = gate.run('h', async () => 'never', ac.signal)
    await tick()
    assert.deepEqual(gate.load('h'), { active: 1, queued: 1 })
    ac.abort()
    await assert.rejects(queued, { name: 'AbortError' })
    assert.deepEqual(gate.load('h'), { active: 1, queued: 0 })
    hold.resolve()
    await running
    assert.deepEqual(gate.load('h'), { active: 0, queued: 0 })
  })

  it('已取消的信号不会开始执行', async () => {
    const gate = new ReadGate({ isBusy })
    const ac = new AbortController()
    ac.abort()
    let ran = false
    await assert.rejects(
      gate.run('h', async () => {
        ran = true
      }, ac.signal),
      { name: 'AbortError' },
    )
    assert.equal(ran, false)
  })

  it('退避等待中被取消：不再重试', async () => {
    const gate = new ReadGate({ isBusy, baseDelayMs: 60_000, maxDelayMs: 60_000 })
    const ac = new AbortController()
    let calls = 0
    const job = gate.run(
      'h',
      async () => {
        calls++
        throw new Busy('忙')
      },
      ac.signal,
    )
    await tick()
    ac.abort()
    await assert.rejects(job, { name: 'AbortError' })
    assert.equal(calls, 1)
  })
})

describe('sleepAbortable', () => {
  it('到时间正常返回', async () => {
    const t0 = Date.now()
    await sleepAbortable(20)
    assert.ok(Date.now() - t0 >= 15)
  })
})
