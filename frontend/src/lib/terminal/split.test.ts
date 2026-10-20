// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { SPLIT_OFF, afterPrimaryChange, clampSplitRatio, pickSecondary, ratioFromPointer } from './split'

describe('clampSplitRatio / ratioFromPointer', () => {
  it('夹到 25%~75%，非法值回 50%', () => {
    assert.equal(clampSplitRatio(0.1), 0.25)
    assert.equal(clampSplitRatio(0.9), 0.75)
    assert.equal(clampSplitRatio(0.4), 0.4)
    assert.equal(clampSplitRatio('0.6'), 0.6)
    assert.equal(clampSplitRatio('x'), 0.5)
    assert.equal(clampSplitRatio(undefined), 0.5)
    assert.equal(clampSplitRatio(Infinity), 0.5)
  })

  it('由指针位置换算占比', () => {
    assert.equal(ratioFromPointer(300, 100, 400), 0.5)
    assert.equal(ratioFromPointer(110, 100, 400), 0.25)
    assert.equal(ratioFromPointer(900, 100, 400), 0.75)
    assert.equal(ratioFromPointer(300, 100, 0), 0.5)
  })
})

describe('pickSecondary', () => {
  const tabs = [
    { key: 'a', replay: false },
    { key: 'b', replay: false },
    { key: 'r', replay: true },
    { key: 'c', replay: false },
  ]

  it('优先选最近使用过的另一个终端标签', () => {
    assert.equal(pickSecondary(tabs, 'a', ['a', 'c', 'b']), 'c')
  })

  it('最近使用列表里没有可选的，就取最后打开的另一个终端标签', () => {
    assert.equal(pickSecondary(tabs, 'a', ['a']), 'c')
  })

  it('回放标签不参与并排；没有可选的返回空串', () => {
    assert.equal(pickSecondary(tabs, 'a', ['r']), 'c')
    assert.equal(pickSecondary([{ key: 'a', replay: false }, { key: 'r', replay: true }], 'a', ['r']), '')
    assert.equal(pickSecondary([], '', []), '')
  })
})

describe('afterPrimaryChange', () => {
  const keys = new Set(['a', 'b', 'c'])

  it('没有开并排就原样返回', () => {
    assert.deepEqual(afterPrimaryChange(SPLIT_OFF, 'a', 'b', keys), SPLIT_OFF)
  })

  it('主窗格换成别的会话：右窗格不变', () => {
    assert.deepEqual(afterPrimaryChange({ on: true, secondary: 'b' }, 'a', 'c', keys), { on: true, secondary: 'b' })
  })

  it('点中了右窗格的会话：两边互换，不会同一个会话占两个窗格', () => {
    assert.deepEqual(afterPrimaryChange({ on: true, secondary: 'b' }, 'a', 'b', keys), { on: true, secondary: 'a' })
  })

  it('右窗格的标签没了：退出并排', () => {
    assert.deepEqual(afterPrimaryChange({ on: true, secondary: 'z' }, 'a', 'b', keys), SPLIT_OFF)
    assert.deepEqual(afterPrimaryChange({ on: true, secondary: '' }, 'a', 'b', keys), SPLIT_OFF)
  })

  it('互换时原来的主窗格已经不是终端标签（例如是回放标签）：退出并排', () => {
    assert.deepEqual(afterPrimaryChange({ on: true, secondary: 'b' }, 'replay:x', 'b', keys), SPLIT_OFF)
  })
})
