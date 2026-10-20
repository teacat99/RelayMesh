import test from 'node:test'
import assert from 'node:assert/strict'
import { hasLineBreak, needsPasteConfirm, parsePasteConfirm, pasteDescription, pasteTitle, summarizePaste } from './paste'

const on = { bracketed: false, enabled: true }

test('单行内容直接粘贴，不需要确认', () => {
  assert.equal(needsPasteConfirm('ls -la', on), false)
  assert.equal(needsPasteConfirm('', on), false)
  assert.equal(needsPasteConfirm('带空格 和 制表\t符的一行', on), false)
})

test('含换行的内容需要确认：\\n、\\r、\\r\\n、结尾换行都算', () => {
  assert.equal(needsPasteConfirm('a\nb', on), true)
  assert.equal(needsPasteConfirm('a\rb', on), true)
  assert.equal(needsPasteConfirm('a\r\nb', on), true)
  assert.equal(needsPasteConfirm('rm -rf build\n', on), true)
  assert.equal(needsPasteConfirm('\n', on), true)
})

test('括号粘贴模式开着时不需要确认（程序把整段当作粘贴内容，不会逐行执行）', () => {
  assert.equal(needsPasteConfirm('a\nb\nc', { bracketed: true, enabled: true }), false)
})

test('偏好关闭时不需要确认', () => {
  assert.equal(needsPasteConfirm('a\nb', { bracketed: false, enabled: false }), false)
})

test('偏好：只有明确存成 false 才算关闭，其余（没存过、乱值）都按默认开', () => {
  assert.equal(parsePasteConfirm(null), true)
  assert.equal(parsePasteConfirm('true'), true)
  assert.equal(parsePasteConfirm('false'), false)
  assert.equal(parsePasteConfirm('0'), true)
  assert.equal(parsePasteConfirm(''), true)
})

test('hasLineBreak', () => {
  assert.equal(hasLineBreak('abc'), false)
  assert.equal(hasLineBreak('a\u2028b'), false) // 只认回车换行；Unicode 行分隔符不会被当作回车
  assert.equal(hasLineBreak('a\nb'), true)
})

test('summarizePaste：行数不把结尾换行多算一行', () => {
  assert.deepEqual(summarizePaste('a\nb\nc'), { lines: 3, chars: 5, endsWithNewline: false, preview: ['a', 'b', 'c'], hiddenLines: 0 })
  assert.deepEqual(summarizePaste('a\nb\nc\n'), { lines: 3, chars: 6, endsWithNewline: true, preview: ['a', 'b', 'c'], hiddenLines: 0 })
  assert.deepEqual(summarizePaste('only\n'), { lines: 1, chars: 5, endsWithNewline: true, preview: ['only'], hiddenLines: 0 })
  assert.equal(summarizePaste('a\r\nb\r\n').lines, 2)
  assert.equal(summarizePaste('a\rb').lines, 2)
})

test('summarizePaste：预览只取前几行、每行截断，剩下的计入 hiddenLines', () => {
  const text = Array.from({ length: 20 }, (_, i) => `line-${i + 1}`).join('\n')
  const s = summarizePaste(text, 5, 100)
  assert.equal(s.lines, 20)
  assert.deepEqual(s.preview, ['line-1', 'line-2', 'line-3', 'line-4', 'line-5'])
  assert.equal(s.hiddenLines, 15)
  const long = summarizePaste('x'.repeat(300) + '\nshort', 8, 40)
  assert.equal(long.preview[0], 'x'.repeat(40) + '…')
  assert.equal(long.preview[1], 'short')
})

test('标题与说明区分「多行」和「单行但以换行结尾」', () => {
  const multi = summarizePaste('a\nb\nc')
  assert.equal(pasteTitle(multi), '粘贴 3 行内容？')
  assert.match(pasteDescription(multi), /逐行立刻执行/)
  const single = summarizePaste('echo hi\n')
  assert.equal(pasteTitle(single), '粘贴后会立刻执行？')
  assert.match(pasteDescription(single), /以换行结尾/)
})
