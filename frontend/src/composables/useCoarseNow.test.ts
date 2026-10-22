// 运行：pnpm --dir frontend run test:unit
// 慢速共享时钟（任务页每一行的「N 分钟前」，#219）的行为：引用计数、30 秒一跳、停表期间值变旧所以第一个订阅者进来先对表、最后一个退订才停表。
import assert from 'node:assert/strict'
import { describe, it, mock } from 'node:test'
import { effectScope, type Ref } from 'vue'
import { COARSE_NOW_INTERVAL_MS, coarseNowState, useCoarseNow } from './useCoarseNow'

describe('useCoarseNow：30 秒一跳的共享慢时钟', () => {
  it('第一个订阅者进来才起表并先对一次表；多个订阅者共用一个定时器；最后一个退订才停表', () => {
    mock.timers.enable({ apis: ['setInterval', 'Date'], now: 1_000_000 })
    try {
      assert.deepEqual(coarseNowState(), { subscribers: 0, running: false })

      const a = effectScope()
      let clockA!: Readonly<Ref<number>>
      a.run(() => {
        clockA = useCoarseNow()
      })
      assert.deepEqual(coarseNowState(), { subscribers: 1, running: true })
      assert.equal(clockA.value, 1_000_000, '第一个订阅者进来先对一次表')

      // 29 秒内不变，满 30 秒走一跳
      mock.timers.tick(COARSE_NOW_INTERVAL_MS - 1000)
      assert.equal(clockA.value, 1_000_000, '没到 30 秒不该变')
      mock.timers.tick(1000)
      assert.equal(clockA.value, 1_000_000 + COARSE_NOW_INTERVAL_MS, '满 30 秒走一跳')

      // 第二个订阅者拿到的是同一个值（同一个 ref），不会再起第二个定时器
      const b = effectScope()
      let clockB!: Readonly<Ref<number>>
      b.run(() => {
        clockB = useCoarseNow()
      })
      assert.equal(clockB, clockA, '共享同一个 ref')
      assert.deepEqual(coarseNowState(), { subscribers: 2, running: true })
      mock.timers.tick(COARSE_NOW_INTERVAL_MS)
      assert.equal(clockA.value, 1_000_000 + 2 * COARSE_NOW_INTERVAL_MS, '两个订阅者时仍然只走一跳（只有一个定时器）')

      // 退订一个：表继续走；退订最后一个：停表
      a.stop()
      assert.deepEqual(coarseNowState(), { subscribers: 1, running: true })
      mock.timers.tick(COARSE_NOW_INTERVAL_MS)
      assert.equal(clockA.value, 1_000_000 + 3 * COARSE_NOW_INTERVAL_MS)
      b.stop()
      assert.deepEqual(coarseNowState(), { subscribers: 0, running: false })

      // 停表期间值不再更新（变旧）；下一个第一个订阅者进来时先对表，拿到的是此刻
      const stale = clockA.value
      mock.timers.tick(10 * 60_000)
      assert.equal(clockA.value, stale, '停表期间不更新')
      const c = effectScope()
      let clockC!: Readonly<Ref<number>>
      c.run(() => {
        clockC = useCoarseNow()
      })
      assert.equal(clockC.value, Date.now(), '重新起表时先对表，不是停表那一刻的旧值')
      assert.notEqual(clockC.value, stale)
      c.stop()
      assert.deepEqual(coarseNowState(), { subscribers: 0, running: false })
    } finally {
      mock.timers.reset()
    }
  })
})
