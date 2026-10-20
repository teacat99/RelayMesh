import test from 'node:test'
import assert from 'node:assert/strict'
import {
  CELL_BYTES,
  EXT_BUDGET_BYTES,
  EXT_MAX_LINES,
  EXT_MIN_LINES,
  INLINE_MAX_BYTES,
  INLINE_MIN_BYTES,
  TAIL_MAX_LINES,
  TAIL_MIN_LINES,
  extScrollback,
  maxInlineLoaded,
  tailLinesFor,
} from './historyBudget'

test('接续历史后的回滚行数：列数越多行数越少，内存大致恒定', () => {
  const n80 = extScrollback(80)
  const n190 = extScrollback(190)
  assert.ok(n80 > n190, `80 列应比 190 列装得下更多行: ${n80} vs ${n190}`)
  assert.equal(n190, Math.floor(EXT_BUDGET_BYTES / (190 * CELL_BYTES)))
  // 内存估算（行数 × 列数 × 12）不超过预算
  for (const cols of [40, 80, 120, 190, 300]) {
    const lines = extScrollback(cols)
    if (lines > EXT_MIN_LINES) assert.ok(lines * cols * CELL_BYTES <= EXT_BUDGET_BYTES, `${cols} 列超预算`)
  }
})

test('回滚行数夹在上下限之间', () => {
  assert.equal(extScrollback(20), EXT_MAX_LINES)
  assert.equal(extScrollback(1), extScrollback(20)) // 列数下限 20
  assert.equal(extScrollback(2000), EXT_MIN_LINES)
  assert.equal(extScrollback(Math.ceil(EXT_BUDGET_BYTES / (EXT_MIN_LINES * CELL_BYTES))), EXT_MIN_LINES) // 恰好到下限的那一列数
})

test('非法的列数退回 80 列', () => {
  assert.equal(extScrollback(Number.NaN), extScrollback(80))
  assert.equal(extScrollback(0), extScrollback(80))
  assert.equal(extScrollback(-5), extScrollback(80))
  assert.equal(extScrollback(Number.POSITIVE_INFINITY), extScrollback(80))
})

test('历史窗口的载入上限随回滚行数缩放，夹在 3–12 MiB', () => {
  assert.equal(maxInlineLoaded(EXT_MAX_LINES), EXT_MAX_LINES * 160) // 6 万行 × 160 字节，约 9.2 MiB，在上下限之间
  assert.equal(maxInlineLoaded(1), INLINE_MIN_BYTES)
  assert.equal(maxInlineLoaded(10_000_000), INLINE_MAX_BYTES)
  const a = maxInlineLoaded(extScrollback(190))
  const b = maxInlineLoaded(extScrollback(80))
  assert.ok(a <= b)
  assert.ok(a >= INLINE_MIN_BYTES && b <= INLINE_MAX_BYTES)
})

test('全新附着请求的回放行数：约 20 屏，夹在 500–2500', () => {
  assert.equal(tailLinesFor(40), 800)
  assert.equal(tailLinesFor(24), TAIL_MIN_LINES) // 480 → 夹到下限
  assert.equal(tailLinesFor(10), TAIL_MIN_LINES)
  assert.equal(tailLinesFor(60), 1200)
  assert.equal(tailLinesFor(500), TAIL_MAX_LINES)
})

test('非法的行数按 24 行算', () => {
  assert.equal(tailLinesFor(Number.NaN), tailLinesFor(24))
  assert.equal(tailLinesFor(0), tailLinesFor(24))
  assert.equal(tailLinesFor(-3), tailLinesFor(24))
})
