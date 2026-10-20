// 正文里的任务引用（#36 → 可点击链接）：识别规则要保守，宁可漏链接也不能误链接。
// 规则只有 scanTaskRefs 这一份；切片、DOM 装饰、点击动线见 linkRefs.test.ts / linkRefsDom.test.ts。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { scanTaskRefs } from './taskRefs'

const all = () => true
const only = (...seqs: number[]) => (n: number) => seqs.includes(n)

function linked(text: string, has: (n: number) => boolean = all): number[] {
  return scanTaskRefs(text, has).map(h => h.seq)
}

test('scanTaskRefs：基本扫描，位置与长度对得上原文', () => {
  const text = '已完成 #36 和 #7，详见 #36。'
  const hits = scanTaskRefs(text, all)
  assert.deepEqual(
    hits.map(h => [text.slice(h.at, h.at + h.length), h.seq]),
    [
      ['#36', 36],
      ['#7', 7],
      ['#36', 36],
    ]
  )
})

test('scanTaskRefs：没有可链接的引用时返回空数组', () => {
  assert.deepEqual(scanTaskRefs('', all), [])
  assert.deepEqual(scanTaskRefs('没有引用', all), [])
  assert.deepEqual(scanTaskRefs('只有一个 # 号', all), [])
  assert.deepEqual(scanTaskRefs('参见 #12', only(99)), [])
})

test('scanTaskRefs：只链接这个工作流里存在的编号', () => {
  assert.deepEqual(linked('#1 #2 #3', only(1, 3)), [1, 3])
  assert.deepEqual(linked('第 #2 轮', () => false), [])
})

test('scanTaskRefs：中文紧贴数字、行首行尾、紧挨的多个引用都能识别', () => {
  assert.deepEqual(linked('完成任务#36已确认'), [36])
  assert.deepEqual(linked('#5'), [5])
  assert.deepEqual(linked('见 #5\n#6\n（#7）'), [5, 6, 7])
  assert.deepEqual(linked('#1 #2'), [1, 2])
  assert.deepEqual(linked('编号：#12.'), [12])
})

test('scanTaskRefs：不是任务编号的写法一律不链接', () => {
  for (const text of [
    'foo#12', // 词字符紧贴
    'issue#12',
    'a1#2',
    'http://x.test/page#12', // URL 片段
    'path/#12',
    '&#39;', // HTML 实体
    '##12', // 重复的 #
    '#012', // 前导 0
    '#0',
    '#1234567', // 超过 6 位
    '#12abc', // 数字后紧跟词字符
    '#12_3',
    '#abc',
  ]) {
    assert.deepEqual(linked(text), [], text)
  }
})

test('scanTaskRefs：「追加 #N」是追加指令的编号，不是任务', () => {
  assert.deepEqual(linked('追加 #5 已读取'), [])
  assert.deepEqual(linked('追加#5'), [])
  assert.deepEqual(linked('追加指令 #5'), [])
  assert.deepEqual(linked('追加 #5，同时完成 #6'), [6])
  assert.deepEqual(linked('这是追加内容，见 #6'), [6])
})
