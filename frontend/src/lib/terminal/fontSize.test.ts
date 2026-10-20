// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { FONT_DEFAULT, FONT_MAX, FONT_MIN, WHEEL_STEP_PX, accumulateWheel, clampFont, isZoomResetKey, parseFont, wheelDeltaPx, zoomHint } from './fontSize'

describe('clampFont', () => {
  it('夹在合法范围内并取整', () => {
    assert.equal(clampFont(14), 14)
    assert.equal(clampFont(3), FONT_MIN)
    assert.equal(clampFont(99), FONT_MAX)
    assert.equal(clampFont(14.6), 15)
  })

  it('不是有限数字时回到默认值', () => {
    for (const bad of [Number.NaN, Infinity, -Infinity]) assert.equal(clampFont(bad), FONT_DEFAULT)
  })
})

describe('parseFont', () => {
  it('读取合法的保存值', () => {
    assert.equal(parseFont('16'), 16)
    assert.equal(parseFont(String(FONT_MIN)), FONT_MIN)
    assert.equal(parseFont(String(FONT_MAX)), FONT_MAX)
  })

  it('缺失、空白、不是数字或越界都回到默认值，而不是硬夹到边界', () => {
    for (const raw of [null, undefined, '', '  ', 'abc', '9', '25', '-3', 'NaN', 'Infinity']) {
      assert.equal(parseFont(raw), FONT_DEFAULT, String(raw))
    }
  })
})

describe('滚轮缩放', () => {
  it('不同单位折算成像素（Firefox 的鼠标滚轮按行上报）', () => {
    assert.equal(wheelDeltaPx(100, 0), 100)
    assert.equal(wheelDeltaPx(3, 1), 120)
    assert.equal(wheelDeltaPx(1, 2), 800)
  })

  it('向上滚放大、向下滚缩小，一格鼠标滚轮正好一档', () => {
    assert.deepEqual(accumulateWheel(0, -100, 0), { steps: 1, rest: 0 })
    assert.deepEqual(accumulateWheel(0, 100, 0), { steps: -1, rest: 0 })
    assert.equal(accumulateWheel(0, -3, 1).steps, 1, 'Firefox 一格 3 行 = 120px，也是一档')
  })

  it('触控板的细碎滚动累计到够一档才变化，余量带到下一次', () => {
    let acc = 0
    let total = 0
    for (let i = 0; i < 7; i++) {
      const r = accumulateWheel(acc, -30, 0)
      acc = r.rest
      total += r.steps
    }
    assert.equal(total, 2, '7 × 30 = 210 → 两档')
    assert.equal(acc, 10)
  })

  it('一次快速滚动可以连跨几档；反向滚动先抵消已有的余量', () => {
    assert.equal(accumulateWheel(0, -350, 0).steps, 3)
    const up = accumulateWheel(0, -60, 0)
    assert.deepEqual(up, { steps: 0, rest: 60 })
    const down = accumulateWheel(up.rest, 100, 0)
    assert.deepEqual(down, { steps: 0, rest: -40 }, '60 − 100 = −40，不足一档')
  })

  it('没有纵向增量不产生任何变化', () => {
    assert.deepEqual(accumulateWheel(0, 0, 0), { steps: 0, rest: 0 })
    assert.equal(WHEEL_STEP_PX, 100)
  })
})

describe('isZoomResetKey', () => {
  const key = (over: Partial<Parameters<typeof isZoomResetKey>[0]>) => ({ ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, key: '', code: '', ...over })

  it('Ctrl+0 与 ⌘+0（含小键盘）算重置', () => {
    assert.equal(isZoomResetKey(key({ ctrlKey: true, key: '0', code: 'Digit0' })), true)
    assert.equal(isZoomResetKey(key({ metaKey: true, key: '0', code: 'Digit0' })), true)
    assert.equal(isZoomResetKey(key({ ctrlKey: true, key: '0', code: 'Numpad0' })), true)
  })

  it('单独的 0、带 Alt / Shift 的组合、别的数字都不算，终端要照常收到它们', () => {
    assert.equal(isZoomResetKey(key({ key: '0', code: 'Digit0' })), false)
    assert.equal(isZoomResetKey(key({ ctrlKey: true, altKey: true, key: '0', code: 'Digit0' })), false)
    assert.equal(isZoomResetKey(key({ ctrlKey: true, shiftKey: true, key: ')', code: 'Digit0' })), false)
    assert.equal(isZoomResetKey(key({ ctrlKey: true, key: '1', code: 'Digit1' })), false)
  })
})

describe('zoomHint', () => {
  it('默认字号时标明「默认」，并提示重置快捷键', () => {
    assert.ok(zoomHint(FONT_DEFAULT).includes('（默认）'))
    assert.ok(!zoomHint(18).includes('（默认）'))
    assert.ok(zoomHint(18).includes('18') && zoomHint(18).includes('Ctrl+0'))
  })
})
