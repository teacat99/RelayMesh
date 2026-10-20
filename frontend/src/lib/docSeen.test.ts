import { test } from 'node:test'
import assert from 'node:assert/strict'
import { changedSince, latestUpdate, nextCursor, readSeenCursor, writeSeenCursor } from './docSeen'

const items = [
  { path: 'a.md', title: 'A', updated_at: '2026-10-15T01:00:00Z' },
  { path: 'b.md', title: 'B', updated_at: '2026-10-15T03:00:00Z' },
  { path: 'c.md', title: 'C', updated_at: '2026-10-15T02:00:00Z' },
]

test('游标之后更新的文档，最近的在前', () => {
  assert.deepEqual(changedSince(items, '2026-10-15T01:30:00Z').map(i => i.path), ['b.md', 'c.md'])
})

test('没有游标（首次访问）或游标非法时不提示', () => {
  assert.deepEqual(changedSince(items, ''), [])
  assert.deepEqual(changedSince(items, 'not-a-date'), [])
})

test('已经看过的文档不再算变化', () => {
  assert.deepEqual(changedSince(items, '2026-10-15T00:00:00Z', new Set(['b.md', 'a.md'])).map(i => i.path), ['c.md'])
})

test('最新更新时间与游标前进（不回退）', () => {
  assert.equal(latestUpdate(items), '2026-10-15T03:00:00Z')
  assert.equal(latestUpdate([]), '')
  assert.equal(nextCursor('', items), '2026-10-15T03:00:00Z')
  assert.equal(nextCursor('2026-10-16T00:00:00Z', items), '2026-10-16T00:00:00Z')
  assert.equal(nextCursor('2026-10-15T00:00:00Z', []), '2026-10-15T00:00:00Z')
})

test('读写游标；存储抛错时不崩', () => {
  const m = new Map<string, string>()
  const st = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
  assert.equal(readSeenCursor(st, 'wf'), '')
  writeSeenCursor(st, 'wf', '2026-10-15T03:00:00Z')
  assert.equal(readSeenCursor(st, 'wf'), '2026-10-15T03:00:00Z')
  writeSeenCursor(st, 'wf', '')
  assert.equal(readSeenCursor(st, 'wf'), '2026-10-15T03:00:00Z')
  const bad = { getItem: () => { throw new Error('x') }, setItem: () => { throw new Error('x') } }
  assert.equal(readSeenCursor(bad, 'wf'), '')
  writeSeenCursor(bad, 'wf', 'x')
})
