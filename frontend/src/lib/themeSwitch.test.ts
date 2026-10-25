// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  THEME_SWITCHING_CLASS,
  THEME_SWITCH_FALLBACK_MS,
  applyDarkClass,
  browserScheduler,
  createTransitionSuppressor,
  type ClassListLike,
  type FrameScheduler,
} from './themeSwitch'

/** 最小的类名集合。 */
function fakeClassList(initial: string[] = []): ClassListLike & { names(): string[] } {
  const set = new Set(initial)
  return {
    add: n => void set.add(n),
    remove: n => void set.delete(n),
    contains: n => set.has(n),
    names: () => [...set].sort(),
  }
}

/** 手动推进的调度器：flushFrame 只跑「调用那一刻已排队」的帧回调，回调里新排的留给下一帧（与 requestAnimationFrame 一致）。 */
function fakeScheduler() {
  let frames: Array<() => void> = []
  const timers: Array<{ cb: () => void; ms: number }> = []
  const sched: FrameScheduler = {
    frame: cb => void frames.push(cb),
    timeout: (cb, ms) => void timers.push({ cb, ms }),
  }
  return {
    sched,
    flushFrame() {
      const now = frames
      frames = []
      for (const cb of now) cb()
    },
    timers,
    fireTimers() {
      for (const t of timers.splice(0)) t.cb()
    },
    pendingFrames: () => frames.length,
  }
}

describe('applyDarkClass + createTransitionSuppressor（#185 切主题时压住全局过渡）', () => {
  it('明暗变了：先加 theme-switching 再改 dark；两帧之后才去掉，dark 保留', () => {
    const cl = fakeClassList()
    const fs = fakeScheduler()
    const order: string[] = []
    const suppress = createTransitionSuppressor(cl, fs.sched)
    const changed = applyDarkClass(cl, true, () => {
      order.push(`suppress 前 dark=${cl.contains('dark')}`)
      suppress()
      order.push(`suppress 后 switching=${cl.contains(THEME_SWITCHING_CLASS)}`)
    })
    assert.equal(changed, true)
    assert.deepEqual(order, ['suppress 前 dark=false', 'suppress 后 switching=true'], '压住过渡必须发生在改类之前')
    assert.deepEqual(cl.names(), ['dark', THEME_SWITCHING_CLASS])

    fs.flushFrame()
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), true, '第一帧之后还不能放开：这一帧才做样式计算')
    fs.flushFrame()
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), false, '第二帧之后放开')
    assert.deepEqual(cl.names(), ['dark'])
  })

  it('明暗没变：不压、不排任何回调（首次应用 / 重复应用）', () => {
    const cl = fakeClassList(['dark'])
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    assert.equal(applyDarkClass(cl, true, suppress), false)
    assert.deepEqual(cl.names(), ['dark'])
    assert.equal(fs.pendingFrames(), 0)
    assert.equal(fs.timers.length, 0)

    const light = fakeClassList()
    assert.equal(applyDarkClass(light, false, suppress), false)
    assert.deepEqual(light.names(), [])
  })

  it('深色 → 浅色：同样压住，并去掉 dark', () => {
    const cl = fakeClassList(['dark'])
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    assert.equal(applyDarkClass(cl, false, suppress), true)
    assert.deepEqual(cl.names(), [THEME_SWITCHING_CLASS])
    fs.flushFrame()
    fs.flushFrame()
    assert.deepEqual(cl.names(), [])
  })

  it('没有传 suppress（首次应用）：只改类，不加 theme-switching', () => {
    const cl = fakeClassList()
    assert.equal(applyDarkClass(cl, true), true)
    assert.deepEqual(cl.names(), ['dark'])
  })

  it('连续快速切换：前一次的回调作废，类一直留到最后一次的第二帧', () => {
    const cl = fakeClassList()
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    applyDarkClass(cl, true, suppress) // 第 1 次，排了帧回调 A
    fs.flushFrame() // A 的第一帧
    applyDarkClass(cl, false, suppress) // 第 2 次，排了帧回调 B（此时 A 的第二帧已排队）
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), true)

    fs.flushFrame() // A 的第二帧（作废） + B 的第一帧
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), true, 'A 的计时不能把 B 的切换提前放开')
    fs.flushFrame() // B 的第二帧
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), false)
    assert.deepEqual(cl.names(), [])
  })

  it('兜底计时：帧回调一直不来（后台标签页）时也会去掉；晚到的旧计时不会放开新的一次', () => {
    const cl = fakeClassList()
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    applyDarkClass(cl, true, suppress)
    assert.equal(fs.timers.length, 1)
    assert.equal(fs.timers[0].ms, THEME_SWITCH_FALLBACK_MS)
    fs.fireTimers()
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), false, '计时到了就放开')
    assert.equal(cl.contains('dark'), true, '兜底只管 theme-switching，不动 dark')

    // 旧计时晚到：第 1 次的计时还没触发，第 2 次已经开始
    const cl2 = fakeClassList()
    const fs2 = fakeScheduler()
    const suppress2 = createTransitionSuppressor(cl2, fs2.sched)
    applyDarkClass(cl2, true, suppress2)
    const stale = fs2.timers.splice(0)
    applyDarkClass(cl2, false, suppress2)
    for (const t of stale) t.cb()
    assert.equal(cl2.contains(THEME_SWITCHING_CLASS), true, '旧计时作废')
    fs2.fireTimers()
    assert.equal(cl2.contains(THEME_SWITCHING_CLASS), false)
  })

  it('帧回调已经放开之后，兜底计时晚到不再写 DOM（没有多余的类名变更）', () => {
    const base = fakeClassList()
    let removes = 0
    const cl: ClassListLike = {
      add: n => base.add(n),
      remove: n => {
        removes += 1
        base.remove(n)
      },
      contains: n => base.contains(n),
    }
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    applyDarkClass(cl, true, suppress)
    fs.flushFrame()
    fs.flushFrame()
    assert.equal(removes, 1, '帧回调放开了一次')
    fs.fireTimers()
    assert.equal(removes, 1, '兜底计时到的时候类已经不在，不该再写一次')
  })

  it('放开之后再切一次：重新压住，各自独立', () => {
    const cl = fakeClassList()
    const fs = fakeScheduler()
    const suppress = createTransitionSuppressor(cl, fs.sched)
    applyDarkClass(cl, true, suppress)
    fs.flushFrame()
    fs.flushFrame()
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), false)
    applyDarkClass(cl, false, suppress)
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), true)
    fs.flushFrame()
    fs.flushFrame()
    assert.equal(cl.contains(THEME_SWITCHING_CLASS), false)
    assert.deepEqual(cl.names(), [])
  })
})

describe('browserScheduler', () => {
  it('frame 走 requestAnimationFrame，timeout 走 setTimeout，并把参数原样带过去', () => {
    const calls: string[] = []
    const sched = browserScheduler({
      requestAnimationFrame: cb => {
        calls.push('raf')
        cb(0)
        return 1
      },
      setTimeout: ((cb: () => void, ms?: number) => {
        calls.push(`timeout ${ms}`)
        cb()
        return 1
      }) as unknown as Window['setTimeout'],
    })
    let ran = 0
    sched.frame(() => void (ran += 1))
    sched.timeout(() => void (ran += 10), 1500)
    assert.deepEqual(calls, ['raf', 'timeout 1500'])
    assert.equal(ran, 11)
  })
})
