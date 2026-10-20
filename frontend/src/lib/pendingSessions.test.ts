import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { FeedbackSession } from '../api/types'
import { badgeText, pickPending } from './pendingSessions'

function sess(p: Partial<FeedbackSession> & { session_id: string }): FeedbackSession {
  return { summary: '', status: 'pending', created_at: '2026-10-17T00:00:00Z', updated_at: '2026-10-17T00:00:00Z', ...p }
}

describe('pickPending', () => {
  test('没有 pending 时全是空', () => {
    const r = pickPending([sess({ session_id: 's1', status: 'completed' })], null, null)
    assert.deepEqual(r.list, [])
    assert.equal(r.elsewhere, 0)
    assert.equal(r.next, null)
  })

  test('同一工作流多轮 pending 只算一个，且留最早的一轮', () => {
    const a1 = sess({ session_id: 'a1', workflow_id: 'wf-a', created_at: '2026-10-17T01:00:00Z' })
    const a2 = sess({ session_id: 'a2', workflow_id: 'wf-a', created_at: '2026-10-17T02:00:00Z' })
    const r = pickPending([a2, a1], null, null)
    assert.equal(r.list.length, 1)
    assert.equal(r.list[0].session_id, 'a1')
  })

  test('没有 workflow_id 的单个会话按 session_id 各算一个，等得最久的排最前', () => {
    const late = sess({ session_id: 'late', created_at: '2026-10-17T03:00:00Z' })
    const early = sess({ session_id: 'early', created_at: '2026-10-17T01:00:00Z' })
    const r = pickPending([late, early], null, null)
    assert.deepEqual(r.list.map(s => s.session_id), ['early', 'late'])
    assert.equal(r.next?.session_id, 'early')
    assert.equal(r.elsewhere, 2)
  })

  test('正在看的那个不算别处：数量少一个，去答复的目标跳过它', () => {
    const a = sess({ session_id: 'a1', workflow_id: 'wf-a', created_at: '2026-10-17T01:00:00Z' })
    const b = sess({ session_id: 'b1', workflow_id: 'wf-b', created_at: '2026-10-17T02:00:00Z' })
    const viewingA = pickPending([a, b], null, { workflowId: 'wf-a', sessionId: 'a1' })
    assert.equal(viewingA.elsewhere, 1)
    assert.equal(viewingA.next?.workflow_id, 'wf-b')
    const viewingOnlyOne = pickPending([a], null, { workflowId: 'wf-a', sessionId: 'a1' })
    assert.equal(viewingOnlyOne.elsewhere, 0)
    assert.equal(viewingOnlyOne.next, null)
  })

  test('不在会话页（viewing 为空）时全部都算', () => {
    const a = sess({ session_id: 'a1', workflow_id: 'wf-a' })
    const r = pickPending([a], null, null)
    assert.equal(r.elsewhere, 1)
    assert.equal(r.next?.session_id, 'a1')
  })

  test('服务端当前的 pending 会话还没进全量列表时也算；已在列表里则以它为准', () => {
    const cur = sess({ session_id: 'c1', workflow_id: 'wf-c' })
    assert.equal(pickPending([], cur, null).list.length, 1)
    const stale = sess({ session_id: 'c1', workflow_id: 'wf-c', status: 'completed' })
    // 列表里的旧副本是 completed，当前会话是 pending：以当前会话为准
    assert.equal(pickPending([stale], cur, null).list.length, 1)
  })

  test('进度通知（kind=notice）不算待答复', () => {
    const n = sess({ session_id: 'n1', workflow_id: 'wf-n', kind: 'notice' })
    assert.equal(pickPending([n], null, null).list.length, 0)
  })
})

describe('badgeText', () => {
  test('9 以内原样，更多写 9+', () => {
    assert.equal(badgeText(1), '1')
    assert.equal(badgeText(9), '9')
    assert.equal(badgeText(10), '9+')
  })
})
