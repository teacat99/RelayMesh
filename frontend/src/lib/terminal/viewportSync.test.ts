import test from 'node:test'
import assert from 'node:assert/strict'
import { resyncViewport, type ResizableTerminal } from './viewportSync'

function fake(type = 'normal', rows = 40, cols = 120) {
  const calls: Array<[number, number]> = []
  const t: ResizableTerminal = {
    rows,
    cols,
    buffer: { active: { type } },
    resize(c, r) {
      calls.push([c, r])
    },
  }
  return { t, calls }
}

test('先放大一行再还原：列数不变，最终回到原来的行数', () => {
  const { t, calls } = fake('normal', 40, 120)
  assert.equal(resyncViewport(t), true)
  assert.deepEqual(calls, [
    [120, 41],
    [120, 40],
  ])
})

test('备用屏（全屏程序）里不动尺寸', () => {
  const { t, calls } = fake('alternate')
  assert.equal(resyncViewport(t), false)
  assert.deepEqual(calls, [])
})
