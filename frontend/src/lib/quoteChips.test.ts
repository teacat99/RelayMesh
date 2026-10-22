import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import { confirmExcerptFor, parseQuoteChips, quoteExcerptFor, removeQuoteToken } from './quoteChips'

describe('quoteChips', () => {
  test('读出回复与胶囊标记，去重、按出现顺序', () => {
    const chips = parseQuoteChips('看这个 【回复 9f02517c】 和【恢复 r8】，再看一次【回复 9f02517c】')
    assert.deepEqual(chips.map(c => [c.kind, c.ref]), [['reply', '9f02517c'], ['capsule', 'r8']])
  })
  test('读出确认项标记（#132）：Q 可省、大小写统一、去重', () => {
    const chips = parseQuoteChips('关于【确认 Q5】，还有【确认 7】和【确认 q7】，再说一次【确认 Q5】；【回复 9f02517c】')
    assert.deepEqual(chips.map(c => [c.kind, c.ref, c.token]), [
      ['confirm', 'Q5', '【确认 Q5】'],
      ['confirm', 'Q7', '【确认 7】'],
      ['confirm', 'Q7', '【确认 q7】'],
      ['reply', '9f02517c', '【回复 9f02517c】'],
    ])
  })
  test('服务端不认的写法不显示成引用', () => {
    assert.deepEqual(parseQuoteChips('【确认 Q0】【确认 Q1234567】【确认】【确认 Qx】【回复 r8】【恢复 9f02517c】'), [])
  })
  test('没有标记返回空', () => {
    assert.deepEqual(parseQuoteChips('普通文字'), [])
    assert.deepEqual(parseQuoteChips(''), [])
  })
  test('移除标记并收拢空格', () => {
    assert.equal(removeQuoteToken('【回复 9f02517c】 好的', '【回复 9f02517c】'), '好的')
    assert.equal(removeQuoteToken('先说 【回复 9f02517c】 后说', '【回复 9f02517c】'), '先说 后说')
  })
  test('移除确认项标记', () => {
    assert.equal(removeQuoteToken('【确认 Q5】 好的', '【确认 Q5】'), '好的')
    assert.equal(removeQuoteToken('先说 【确认 Q5】 后说', '【确认 Q5】'), '先说 后说')
  })
  test('按编号找被引用的确认项标题；找不到、或不是确认项都返回空串', () => {
    const chip = parseQuoteChips('【确认 Q5】')[0]
    const items = [
      { ref: 'Q4', seq: 4, title: '别的' },
      { ref: 'Q5', seq: 5, title: '会话顶部「文档」换成「项目」入口，旧文档位置迁移——按哪个范围做？' },
    ]
    assert.equal(confirmExcerptFor(chip, items, 12), '会话顶部「文档」换成「项…')
    assert.equal(confirmExcerptFor(chip, items.slice(0, 1)), '')
    assert.equal(confirmExcerptFor(parseQuoteChips('【回复 9f02517c】')[0], items), '')
    // 只有 seq 没有 ref 的条目也认
    assert.equal(confirmExcerptFor(chip, [{ seq: 5, title: '无 ref' }]), '无 ref')
  })
  test('按哈希找被引用那一轮的标题', () => {
    const chip = parseQuoteChips('【回复 9f02517c】')[0]
    assert.equal(quoteExcerptFor(chip, [{ session_id: 'sess-9f02517c', title: '方案汇报' }]), '方案汇报')
    assert.equal(quoteExcerptFor(chip, [{ session_id: 'sess-aaaaaaaa', title: 'x' }]), '')
  })
})
