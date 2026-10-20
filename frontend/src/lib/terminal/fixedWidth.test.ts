// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { FIXED_FONT_LEGIBLE, FIXED_FONT_MIN, describeFixedWidth, fmtFont, parseFixedCols, solveFixedFont } from './fixedWidth'

// 模拟 xterm：单元格宽度按设备像素取整（dpr = 1 时是整数 px），宽度随字号不减。
function cellWidth(font: number, dpr = 1): number {
  return Math.max(1, Math.round(font * 0.6 * dpr)) / dpr
}
function fitsIn(avail: number, cols: number, dpr = 1) {
  return (font: number) => Math.floor(avail / cellWidth(font, dpr)) >= cols
}

describe('solveFixedFont', () => {
  it('窗口够宽：直接用基础字号，只探测一次', () => {
    assert.deepEqual(solveFixedFont(14, fitsIn(1200, 120)), { font: 14, fits: true, probes: 1 })
  })

  it('窗口放不下：缩小到放得下的最大字号', () => {
    const avail = 730
    const r = solveFixedFont(14, fitsIn(avail, 120))
    assert.equal(r.fits, true)
    assert.ok(r.font < 14, `字号应被缩小：${r.font}`)
    assert.equal(fitsIn(avail, 120)(r.font), true)
    // 再大半个像素就放不下了
    assert.equal(fitsIn(avail, 120)(r.font + 0.5), false)
  })

  it('结果总是「放得下的最大字号」：对一批宽度、列数与像素比逐个验证', () => {
    for (const dpr of [1, 2, 3]) {
      for (const avail of [320, 360, 390, 540, 744, 980, 1400]) {
        for (const cols of [60, 80, 100, 120, 132, 160, 200]) {
          const fits = fitsIn(avail, cols, dpr)
          const r = solveFixedFont(16, fits)
          if (r.fits) {
            assert.equal(fits(r.font), true, `dpr=${dpr} avail=${avail} cols=${cols} font=${r.font}`)
            if (r.font < 16) assert.equal(fits(Math.round((r.font + 0.5) * 100) / 100), false, `dpr=${dpr} avail=${avail} cols=${cols} font=${r.font}`)
          } else {
            assert.equal(r.font, FIXED_FONT_MIN)
            assert.equal(fits(FIXED_FONT_MIN), false)
          }
        }
      }
    }
  })

  it('探测次数很少（每次探测都会让 xterm 重新量单元格）', () => {
    const r = solveFixedFont(24, fitsIn(300, 120))
    assert.ok(r.probes <= 9, `探测了 ${r.probes} 次`)
  })

  it('缩到下限仍放不下：返回下限并标明放不下', () => {
    const r = solveFixedFont(14, fitsIn(200, 400))
    assert.equal(r.font, FIXED_FONT_MIN)
    assert.equal(r.fits, false)
  })

  it('基础字号本身不大于下限且放不下：不再往下找', () => {
    assert.deepEqual(solveFixedFont(4, () => false), { font: 4, fits: false, probes: 1 })
  })

  it('下限刚好放得下', () => {
    const r = solveFixedFont(14, f => f <= FIXED_FONT_MIN)
    assert.equal(r.font, FIXED_FONT_MIN)
    assert.equal(r.fits, true)
  })

  it('步进可调，且结果落在步进网格上', () => {
    const r = solveFixedFont(14, f => f <= 9.3, { min: 5, step: 1 })
    assert.equal(r.font, 9)
    assert.equal(r.fits, true)
  })
})

describe('describeFixedWidth', () => {
  it('窗口够宽、字号没变：不提示', () => {
    assert.equal(describeFixedWidth({ cols: 120, font: 14, base: 14, fits: true }), null)
  })

  it('字号被缩小：提示列数与字号', () => {
    const h = describeFixedWidth({ cols: 120, font: 9.5, base: 14, fits: true })
    assert.equal(h?.text, '固定 120 列 · 9.5px')
    assert.equal(h?.tone, 'info')
    assert.ok(h?.title.includes('14px'))
  })

  it('字号低于可辨认下限：警示色并给出建议', () => {
    const h = describeFixedWidth({ cols: 120, font: FIXED_FONT_LEGIBLE - 1, base: 14, fits: true })
    assert.equal(h?.tone, 'warn')
    assert.ok(h?.title.includes('调低固定列数'))
  })

  it('放不下：提示窗口过窄与横向滑动', () => {
    const h = describeFixedWidth({ cols: 400, font: 4, base: 14, fits: false })
    assert.equal(h?.text, '固定 400 列 · 窗口过窄')
    assert.equal(h?.tone, 'warn')
    assert.ok(h?.title.includes('横向滑动'))
  })
})

describe('parseFixedCols / fmtFont', () => {
  const lim = { min: 40, max: 400 }

  it('接受范围内的整数（含首尾空白）', () => {
    assert.deepEqual(parseFixedCols(' 120 ', lim), { ok: true, value: 120 })
    assert.deepEqual(parseFixedCols('40', lim), { ok: true, value: 40 })
    assert.deepEqual(parseFixedCols('400', lim), { ok: true, value: 400 })
  })

  it('拒绝越界、小数、负数与非数字', () => {
    for (const bad of ['39', '401', '12.5', '-80', 'abc', '', '  ', '1e2', '0x50']) {
      assert.equal(parseFixedCols(bad, lim).ok, false, bad)
    }
    const r = parseFixedCols('1000', lim)
    assert.equal(r.ok, false)
    if (!r.ok) assert.ok(r.message.includes('40–400'))
  })

  it('字号文字：整数不带小数', () => {
    assert.equal(fmtFont(14), '14')
    assert.equal(fmtFont(9.5), '9.5')
    assert.equal(fmtFont(6.25), '6.3')
  })
})
