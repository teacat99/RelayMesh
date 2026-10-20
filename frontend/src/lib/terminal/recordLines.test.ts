// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { RECORD_LINES_DEFAULT, RECORD_LINES_PRESETS, coverageText, fmtLines, parseRecordLines, tightensRecordLines } from './recordLines'

const lim = { min: 5000, max: 10_000_000 }

describe('parseRecordLines', () => {
  it('接受范围内的整数，容忍千分位逗号与空格', () => {
    assert.deepEqual(parseRecordLines('100000', lim), { ok: true, value: 100000 })
    assert.deepEqual(parseRecordLines(' 100,000 ', lim), { ok: true, value: 100000 })
    assert.deepEqual(parseRecordLines('1 000 000', lim), { ok: true, value: 1_000_000 })
    assert.deepEqual(parseRecordLines('5000', lim), { ok: true, value: 5000 })
    assert.deepEqual(parseRecordLines('10000000', lim), { ok: true, value: 10_000_000 })
  })

  it('拒绝越界、小数、负数、空与非数字', () => {
    for (const bad of ['4999', '10000001', '5000.5', '-6000', '', '   ', 'abc', '1e5', '0']) {
      assert.equal(parseRecordLines(bad, lim).ok, false, JSON.stringify(bad))
    }
    const r = parseRecordLines('99999999999999999999', lim)
    assert.equal(r.ok, false)
    const r2 = parseRecordLines('3', lim)
    assert.equal(r2.ok, false)
    if (!r2.ok) assert.ok(r2.message.includes('5,000–10,000,000'), r2.message)
  })

  it('快捷值与默认值都在范围内', () => {
    for (const n of [...RECORD_LINES_PRESETS, RECORD_LINES_DEFAULT]) assert.equal(parseRecordLines(String(n), lim).ok, true, String(n))
  })
})

describe('tightensRecordLines', () => {
  it('不限制 → 有上限：会丢内容，需要确认', () => assert.equal(tightensRecordLines(0, 100_000), true))
  it('调小上限：会丢内容', () => assert.equal(tightensRecordLines(200_000, 100_000), true))
  it('调大上限 / 取消上限 / 不变：不丢内容', () => {
    assert.equal(tightensRecordLines(100_000, 200_000), false)
    assert.equal(tightensRecordLines(100_000, 0), false)
    assert.equal(tightensRecordLines(100_000, 100_000), false)
    assert.equal(tightensRecordLines(0, 0), false)
  })
})

describe('coverageText / fmtLines', () => {
  it('没有数据：不说话', () => {
    assert.equal(coverageText(null), null)
    assert.equal(coverageText(undefined), null)
  })
  it('没有在线主机', () => {
    const c = coverageText({ online: 0, unsupported: 0 })
    assert.equal(c?.warn, false)
    assert.ok(c?.text.includes('没有在线主机'))
  })
  it('都支持：正常提示', () => {
    const c = coverageText({ online: 3, unsupported: 0 })
    assert.equal(c?.warn, false)
    assert.ok(c?.text.includes('3 台在线主机'))
  })
  it('有主机不支持：警示并说明怎么办', () => {
    const c = coverageText({ online: 3, unsupported: 2 })
    assert.equal(c?.warn, true)
    assert.ok(c?.text.includes('2 台'))
    assert.ok(c?.text.includes('另外 1 台已生效'))
    assert.ok(c?.text.includes('holder restart'))
    assert.ok(!coverageText({ online: 2, unsupported: 2 })?.text.includes('已生效'))
  })
  it('千分位', () => {
    assert.equal(fmtLines(100000), '100,000')
    assert.equal(fmtLines(5000), '5,000')
  })
})
