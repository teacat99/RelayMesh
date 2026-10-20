// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { LIVE_CAP_DEFAULT, clampLiveCap, evictedKeys, pruneLive, shrinkLive, touchLive } from './liveSet'

describe('clampLiveCap', () => {
  it('非法输入回默认值，越界夹到 2~8', () => {
    assert.equal(clampLiveCap(undefined), LIVE_CAP_DEFAULT)
    assert.equal(clampLiveCap('abc'), LIVE_CAP_DEFAULT)
    assert.equal(clampLiveCap(''), LIVE_CAP_DEFAULT)
    assert.equal(clampLiveCap(NaN), LIVE_CAP_DEFAULT)
    assert.equal(clampLiveCap(0), 2)
    assert.equal(clampLiveCap(1), 2)
    assert.equal(clampLiveCap(3.6), 4)
    assert.equal(clampLiveCap('6'), 6)
    assert.equal(clampLiveCap(99), 8)
  })
})

describe('touchLive', () => {
  it('使用过的移到最前', () => {
    assert.deepEqual(touchLive(['a', 'b', 'c'], 'c', new Set(['c']), 4), ['c', 'a', 'b'])
    assert.deepEqual(touchLive([], 'x', new Set(['x']), 4), ['x'])
  })

  it('超出上限时淘汰最久未使用的', () => {
    const next = touchLive(['d', 'c', 'b', 'a'], 'e', new Set(['e']), 4)
    assert.deepEqual(next, ['e', 'd', 'c', 'b'])
  })

  it('正在显示的会话不会被淘汰，即使它最久没用（并排时的左窗格）', () => {
    const next = touchLive(['d', 'c', 'b', 'a'], 'e', new Set(['e', 'a']), 4)
    assert.deepEqual(next, ['e', 'd', 'c', 'a'], '淘汰的是 b，而不是显示中的 a')
  })

  it('全部都在显示时允许暂时超出上限，而不是误杀', () => {
    const next = touchLive(['c', 'b', 'a'], 'd', new Set(['a', 'b', 'c', 'd']), 2)
    assert.deepEqual(next, ['d', 'c', 'b', 'a'])
  })

  it('不修改入参', () => {
    const order = ['a', 'b']
    touchLive(order, 'b', new Set(['b']), 4)
    assert.deepEqual(order, ['a', 'b'])
  })
})

describe('shrinkLive', () => {
  it('调小上限：从最久未使用的开始收敛，显示中的保留', () => {
    assert.deepEqual(shrinkLive(['a', 'b', 'c', 'd'], new Set(['a']), 2), ['a', 'b'])
    assert.deepEqual(shrinkLive(['a', 'b', 'c', 'd'], new Set(['d']), 2), ['a', 'd'])
    assert.deepEqual(shrinkLive(['a', 'b', 'c'], new Set(['a', 'b', 'c']), 1), ['a', 'b', 'c'])
    assert.deepEqual(shrinkLive(['a', 'b'], new Set(), 4), ['a', 'b'])
  })
})

describe('pruneLive / evictedKeys', () => {
  it('丢掉不存在的标签；没有变化时返回原数组', () => {
    const order = ['a', 'b', 'c']
    assert.deepEqual(pruneLive(order, new Set(['a', 'c'])), ['a', 'c'])
    assert.equal(pruneLive(order, new Set(['a', 'b', 'c', 'd'])), order)
  })

  it('找出被淘汰的 key', () => {
    assert.deepEqual(evictedKeys(['a', 'b', 'c'], ['c', 'a']), ['b'])
    assert.deepEqual(evictedKeys([], ['a']), [])
  })
})
