import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { SECONDARY_MAX_WAIT_MS, createSecondaryGate, whenSecondaryOpen, type GateTimers } from './secondaryGate'

/** 可手动推进的假定时器。 */
function fakeTimers() {
  let now = 0
  let nextId = 1
  const pending = new Map<number, { at: number; fn: () => void }>()
  const timers: GateTimers = {
    set: (fn, ms) => {
      const id = nextId++
      pending.set(id, { at: now + ms, fn })
      return id
    },
    clear: handle => {
      pending.delete(handle as number)
    },
  }
  return {
    timers,
    get pendingCount() {
      return pending.size
    },
    advance(ms: number) {
      now += ms
      for (const [id, t] of [...pending]) {
        if (t.at <= now) {
          pending.delete(id)
          t.fn()
        }
      }
    },
  }
}

const tick = () => new Promise<void>(resolve => setImmediate(resolve))

describe('次要请求让路栅栏（#169 第三期）', () => {
  it('没立栅栏：wait() 返回 null，whenSecondaryOpen 同步执行（行为与以前一样）', () => {
    const gate = createSecondaryGate(fakeTimers().timers)
    assert.equal(gate.held, false)
    assert.equal(gate.wait(), null)
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    assert.equal(ran, 1, '同步执行，不能晚到下一个微任务')
  })

  it('立栅栏后次要请求排队，release 才放行', async () => {
    const gate = createSecondaryGate(fakeTimers().timers)
    gate.hold()
    assert.equal(gate.held, true)
    const ran: string[] = []
    whenSecondaryOpen(() => ran.push('tasks'), gate)
    whenSecondaryOpen(() => ran.push('confirms'), gate)
    await tick()
    assert.deepEqual(ran, [], '主表没到齐，次要请求不发')
    gate.release()
    assert.equal(gate.held, false)
    await tick()
    assert.deepEqual(ran, ['tasks', 'confirms'], '按排队顺序放行')
  })

  it('放行之后新来的次要请求立刻执行', async () => {
    const gate = createSecondaryGate(fakeTimers().timers)
    gate.hold()
    gate.release()
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    assert.equal(ran, 1)
  })

  it('最长等待：到点强制放行，主表很慢时次要请求不会饿死', async () => {
    const f = fakeTimers()
    const gate = createSecondaryGate(f.timers)
    gate.hold()
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    f.advance(SECONDARY_MAX_WAIT_MS - 1)
    await tick()
    assert.equal(ran, 0, '还没到点')
    assert.equal(gate.held, true)
    f.advance(1)
    await tick()
    assert.equal(ran, 1, '到点强制放行')
    assert.equal(gate.held, false)
  })

  it('超时强制放行后，迟到的 release 是空操作（不会把计数减成负数、不会误放行下一次）', async () => {
    const f = fakeTimers()
    const gate = createSecondaryGate(f.timers)
    gate.hold()
    f.advance(SECONDARY_MAX_WAIT_MS)
    gate.release()
    assert.equal(gate.held, false)
    gate.hold()
    assert.equal(gate.held, true, '新的一次加载重新立起栅栏')
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    await tick()
    assert.equal(ran, 0)
    gate.release()
    await tick()
    assert.equal(ran, 1)
  })

  it('计数：同时有两个整表加载在途，全部结束才放行；最长等待从第一次立起时开始算', async () => {
    const f = fakeTimers()
    const gate = createSecondaryGate(f.timers)
    gate.hold()
    f.advance(1000)
    gate.hold()
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    gate.release()
    await tick()
    assert.equal(ran, 0, '还有一个在途')
    assert.equal(gate.held, true)
    f.advance(500)
    await tick()
    assert.equal(ran, 1, '第一次立起后 1.5 秒到点强制放行，第二次立起不会重新计时')
  })

  it('放行会撤掉定时器，不留悬挂的计时', () => {
    const f = fakeTimers()
    const gate = createSecondaryGate(f.timers)
    gate.hold()
    assert.equal(f.pendingCount, 1)
    gate.release()
    assert.equal(f.pendingCount, 0)
  })

  it('没立栅栏时 release 是空操作', () => {
    const gate = createSecondaryGate(fakeTimers().timers)
    gate.release()
    gate.release()
    assert.equal(gate.held, false)
    assert.equal(gate.wait(), null)
  })

  it('自定义最长等待', async () => {
    const f = fakeTimers()
    const gate = createSecondaryGate(f.timers)
    gate.hold(200)
    let ran = 0
    whenSecondaryOpen(() => ran++, gate)
    f.advance(199)
    await tick()
    assert.equal(ran, 0)
    f.advance(1)
    await tick()
    assert.equal(ran, 1)
  })
})
