import test from 'node:test'
import assert from 'node:assert/strict'
import { cellWidth, displayWidth, dividerLine } from './divider'

test('字符宽度：ASCII 与箭头占 1 列，汉字与全角符号占 2 列', () => {
  assert.equal(cellWidth('a'), 1)
  assert.equal(cellWidth('↑'), 1)
  assert.equal(cellWidth('─'), 1)
  assert.equal(cellWidth('·'), 1)
  assert.equal(cellWidth('史'), 2)
  assert.equal(cellWidth('（'), 2)
  assert.equal(displayWidth('历史日志'), 8)
  assert.equal(displayWidth(' ↑ 历史 · ↓ 实时 '), 1 + 1 + 1 + 4 + 1 + 1 + 1 + 1 + 1 + 4 + 1)
})

test('分割线：刚好 cols 列，标签居中', () => {
  const label = ' ↑ 历史 · ↓ 实时 '
  for (const cols of [40, 41, 80, 120]) {
    const line = dividerLine(cols, [label])
    assert.equal(displayWidth(line), cols)
    assert.ok(line.includes(label))
    const left = line.indexOf(label)
    const right = line.length - left - label.length
    assert.ok(Math.abs(left - right) <= 1)
  }
})

test('分割线：长标签放不下就换短的，都放不下只画横线', () => {
  const long = ' ↑ 历史日志（会话记录） · ↓ 实时终端 · 运行中 '
  const short = ' ↑ 历史 · ↓ 实时 '
  assert.ok(dividerLine(100, [long, short]).includes(long))
  const narrow = dividerLine(30, [long, short])
  assert.ok(narrow.includes(short))
  assert.equal(displayWidth(narrow), 30)
  const tiny = dividerLine(10, [long, short])
  assert.equal(tiny, '─'.repeat(10))
  assert.equal(dividerLine(0, [long]), '─')
})
