// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { SPINNER_FRAMES, SPINNER_INTERVAL_MS, SpinnerClock, frameAt, type SpinnerEnv } from './spinner'

/** 手动推进的假环境：记录定时器的创建 / 清除次数，随时切换「页面隐藏」「减少动态效果」。 */
function fakeEnv() {
  const state = { hidden: false, reduced: false, created: 0, cleared: 0, interval: 0 }
  const timers = new Map<number, () => void>()
  const listeners = new Set<() => void>()
  let next = 1
  const env: SpinnerEnv = {
    hidden: () => state.hidden,
    reduced: () => state.reduced,
    setInterval: (fn, ms) => {
      const id = next++
      state.created++
      state.interval = ms
      timers.set(id, fn)
      return id
    },
    clearInterval: h => {
      if (timers.delete(h as number)) state.cleared++
    },
    onChange: fn => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
  }
  return {
    env,
    state,
    live: () => timers.size,
    listening: () => listeners.size,
    tick: (n = 1) => {
      for (let i = 0; i < n; i++) [...timers.values()].forEach(f => f())
    },
    change: (patch: Partial<Pick<typeof state, 'hidden' | 'reduced'>>) => {
      Object.assign(state, patch)
      ;[...listeners].forEach(f => f())
    },
  }
}

describe('盲文旋转符的帧', () => {
  it('十帧，字形与顺序和需求一致', () => {
    assert.equal(SPINNER_FRAMES.join(' '), '⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏')
    assert.equal(SPINNER_FRAMES.length, 10)
    assert.equal(new Set(SPINNER_FRAMES).size, 10)
  })

  it('frameAt 循环取帧，负数与非数字不会越界', () => {
    assert.equal(frameAt(0), '⠋')
    assert.equal(frameAt(9), '⠏')
    assert.equal(frameAt(10), '⠋')
    assert.equal(frameAt(23), '⠸')
    assert.equal(frameAt(-1), '⠏')
    assert.equal(frameAt(-11), '⠏')
    assert.equal(frameAt(Number.NaN), '⠋')
    assert.equal(frameAt(2.9), '⠹')
  })

  it('每帧 80ms', () => {
    assert.equal(SPINNER_INTERVAL_MS, 80)
  })
})

describe('SpinnerClock', () => {
  it('没人用时不起定时器；第一个使用者让它启动，帧号逐帧前进并回绕', () => {
    const f = fakeEnv()
    const seen: number[] = []
    const clock = new SpinnerClock(f.env, i => seen.push(i))
    assert.equal(clock.running, false)
    assert.equal(f.state.created, 0)

    const release = clock.subscribe()
    assert.equal(clock.running, true)
    assert.equal(f.state.created, 1)
    assert.equal(f.state.interval, SPINNER_INTERVAL_MS)

    f.tick(12)
    assert.equal(clock.frame, 2)
    assert.deepEqual(seen.slice(0, 11), [1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1])
    release()
  })

  it('多个使用者共用一个定时器；最后一个释放才停，并撤掉环境监听', () => {
    const f = fakeEnv()
    const clock = new SpinnerClock(f.env)
    const a = clock.subscribe()
    const b = clock.subscribe()
    const c = clock.subscribe()
    assert.equal(f.state.created, 1, '十个指示符也只有一个定时器')
    assert.equal(f.live(), 1)
    assert.equal(f.listening(), 1)
    assert.equal(clock.subscribers, 3)

    a()
    b()
    assert.equal(clock.running, true)
    c()
    assert.equal(clock.running, false)
    assert.equal(f.live(), 0)
    assert.equal(f.listening(), 0)
    assert.equal(clock.subscribers, 0)
  })

  it('释放函数幂等：重复调用不会把别人的使用计数减掉', () => {
    const f = fakeEnv()
    const clock = new SpinnerClock(f.env)
    const a = clock.subscribe()
    const b = clock.subscribe()
    a()
    a()
    a()
    assert.equal(clock.subscribers, 1)
    assert.equal(clock.running, true)
    b()
    assert.equal(clock.running, false)
  })

  it('释放后再次使用会重新启动，帧号接着走而不是回到随机位置', () => {
    const f = fakeEnv()
    const clock = new SpinnerClock(f.env)
    const a = clock.subscribe()
    f.tick(3)
    a()
    assert.equal(clock.running, false)
    const b = clock.subscribe()
    assert.equal(clock.running, true)
    assert.equal(f.state.created, 2)
    f.tick()
    assert.equal(clock.frame, 4)
    b()
  })

  it('页面不可见时暂停，回来再接着转', () => {
    const f = fakeEnv()
    const clock = new SpinnerClock(f.env)
    const release = clock.subscribe()
    f.tick(2)
    f.change({ hidden: true })
    assert.equal(clock.running, false)
    assert.equal(f.live(), 0)
    assert.equal(f.state.cleared, 1)

    f.change({ hidden: false })
    assert.equal(clock.running, true)
    f.tick()
    assert.equal(clock.frame, 3)
    release()
  })

  it('页面一开始就不可见：先不起定时器，可见时才起', () => {
    const f = fakeEnv()
    f.state.hidden = true
    const clock = new SpinnerClock(f.env)
    const release = clock.subscribe()
    assert.equal(clock.running, false)
    assert.equal(f.state.created, 0)
    f.change({ hidden: false })
    assert.equal(clock.running, true)
    release()
  })

  it('减少动态效果：不起定时器，帧固定在第一帧；关掉偏好后恢复转动', () => {
    const f = fakeEnv()
    f.state.reduced = true
    const seen: number[] = []
    const clock = new SpinnerClock(f.env, i => seen.push(i))
    const release = clock.subscribe()
    assert.equal(clock.running, false)
    assert.equal(clock.frame, 0)
    assert.deepEqual(seen, [])

    f.change({ reduced: false })
    assert.equal(clock.running, true)
    f.tick(4)
    assert.equal(clock.frame, 4)

    // 转到一半时切到「减少动态效果」：停下，并回到第一帧（不停在中间帧上）
    f.change({ reduced: true })
    assert.equal(clock.running, false)
    assert.equal(clock.frame, 0)
    assert.equal(seen[seen.length - 1], 0)
    release()
  })

  it('环境变化通知只在有人使用期间监听', () => {
    const f = fakeEnv()
    const clock = new SpinnerClock(f.env)
    assert.equal(f.listening(), 0)
    const release = clock.subscribe()
    assert.equal(f.listening(), 1)
    release()
    assert.equal(f.listening(), 0)
    // 没人用时环境怎么变都不会误启动
    f.change({ hidden: false })
    assert.equal(clock.running, false)
  })
})
