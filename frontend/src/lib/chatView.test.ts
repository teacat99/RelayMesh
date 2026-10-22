import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { ChatMessage } from '../api/conversations'
import {
  buildRows,
  compareKeys,
  mergeMessages,
  mergeTail,
  newestCursor,
  parseCursor,
  quoteExcerpt,
} from './chatView'
import { dateLabel } from './dateLabel'

function enc(ms: number, rank: number, id: string): string {
  return Buffer.from(`${ms}.${rank}.${id}`).toString('base64url')
}

function msg(id: string, ms: number, over: Partial<ChatMessage> = {}, rank = 0): ChatMessage {
  return {
    id,
    conversation_id: 'wf:x',
    kind: 'text',
    sender: { kind: 'agent', id: 'wf:x', name: 'Agent' },
    sent_at: new Date(ms).toISOString(),
    cursor: enc(ms, rank, id),
    status: 'sent',
    source: { type: 'feedback_session', id },
    hops: 0,
    ...over,
  }
}
const human = (id: string, ms: number, over: Partial<ChatMessage> = {}): ChatMessage =>
  msg(id, ms, { sender: { kind: 'human', id: 'user', name: '你' }, ...over }, 1)

describe('游标', () => {
  test('与服务端同一格式：base64url(ms.rank.id)，主键里的点不影响解析', () => {
    assert.deepEqual(parseCursor(enc(1760000000123, 2, 'a.b.c')), { ms: 1760000000123, rank: 2, id: 'a.b.c' })
    assert.equal(parseCursor(''), null)
    assert.equal(parseCursor('###'), null)
    assert.equal(parseCursor(Buffer.from('1.2.').toString('base64url')), null, '主键为空')
  })

  test('次序：毫秒 → 来源序 → 主键', () => {
    const k = (ms: number, rank: number, id: string) => ({ ms, rank, id })
    assert.ok(compareKeys(k(1, 9, 'z'), k(2, 0, 'a')) < 0)
    assert.ok(compareKeys(k(2, 0, 'z'), k(2, 1, 'a')) < 0)
    assert.ok(compareKeys(k(2, 1, 'a'), k(2, 1, 'b')) < 0)
    assert.equal(compareKeys(k(2, 1, 'a'), k(2, 1, 'a')), 0)
  })
})

describe('mergeMessages', () => {
  test('按编号去重、新的覆盖旧的（状态会变），并按服务端次序排好', () => {
    const a = msg('s:1', 1000)
    const b = human('x:1', 1000)
    const c = msg('s:2', 3000)
    const merged = mergeMessages([c, a], [b, { ...a, status: 'read' }])
    assert.deepEqual(merged.map(m => m.id), ['s:1', 'x:1', 's:2'], '同毫秒时 Agent（来源序 0）在你（来源序 1）之前')
    assert.equal(merged[0].status, 'read')
  })

  test('重复合并同一批是幂等的', () => {
    const batch = [msg('s:1', 1), msg('s:2', 2), human('r:1', 3)]
    const once = mergeMessages([], batch)
    assert.deepEqual(mergeMessages(once, batch).map(m => m.id), once.map(m => m.id))
  })
})

describe('mergeTail：用最新一页刷新尾部', () => {
  test('保留更早的历史；这一页覆盖的区间以服务端为准（删除的消失、状态更新、漏的补上）', () => {
    const history = [msg('s:1', 1), msg('s:2', 2)]
    const stale = [msg('s:4', 4), msg('s:5', 5), msg('s:gone', 50)] // s:gone 已被服务端删除
    const page = [msg('s:4', 4, { status: 'read' }), msg('s:5', 5), msg('s:6', 6)]
    const out = mergeTail([...history, ...stale], page)
    assert.deepEqual(out.map(m => m.id), ['s:1', 's:2', 's:4', 's:5', 's:6'])
    assert.equal(out[2].status, 'read')
  })

  test('空页不动列表', () => {
    const list = [msg('s:1', 1)]
    assert.deepEqual(mergeTail(list, []).map(m => m.id), ['s:1'])
  })
})

describe('buildRows：日期分隔', () => {
  const day = (d: number, h: number, mi = 0) => new Date(2026, 9, d, h, mi).getTime()
  const now = new Date(2026, 9, 15, 12, 0)

  test('跨天插入日期条；今天 / 昨天 / 更早', () => {
    const rows = buildRows([msg('a', day(13, 9)), msg('b', day(14, 9)), msg('c', day(15, 9))], now)
    assert.deepEqual(rows.filter(r => r.type === 'date').map(r => r.label), ['10月13日', '昨天', '今天'])
    assert.equal(dateLabel(new Date(2025, 0, 2), now), '2025年1月2日')
  })

  test('同一天的消息之间不插日期条；每条消息恰好一行，键是消息编号，次序不变', () => {
    const rows = buildRows([msg('a', day(15, 9)), human('b', day(15, 9, 1)), msg('c', day(15, 11))], now)
    assert.deepEqual(rows.map(r => r.type), ['date', 'message', 'message', 'message'])
    assert.deepEqual(rows.filter(r => r.type === 'message').map(r => r.key), ['a', 'b', 'c'])
    assert.deepEqual(rows.filter(r => r.type === 'message').map(r => r.message?.id), ['a', 'b', 'c'])
  })

  test('没有消息就没有行', () => {
    assert.deepEqual(buildRows([], now), [])
  })
})

describe('摘要与已读游标', () => {
  test('quoteExcerpt：标题优先、折叠空白、超长截断；没有消息返回空串', () => {
    const target = msg('s:sess-9f02517c', 1, { hash: '9f02517c', title: '第一轮汇报', text: '长长的正文' })
    assert.equal(quoteExcerpt(target), '第一轮汇报')
    assert.equal(quoteExcerpt(msg('a', 1, { text: 'x'.repeat(100) }), 10), 'xxxxxxxxxx…')
    assert.equal(quoteExcerpt(msg('a', 1, { text: '一行\n\n  两行' })), '一行 两行')
    assert.equal(quoteExcerpt(undefined), '')
  })

  test('newestCursor', () => {
    assert.equal(newestCursor([]), '')
    const list = [msg('a', 1), msg('b', 2)]
    assert.equal(newestCursor(list), list[1].cursor)
  })
})
