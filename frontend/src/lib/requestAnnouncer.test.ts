// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { createRequestAnnouncer } from './requestAnnouncer'

const pending = (id: string) => ({ session_id: id, status: 'pending' })

describe('createRequestAnnouncer', () => {
  it('同一条待回复请求只提醒一次，不管经过几条路径', () => {
    const a = createRequestAnnouncer()
    assert.equal(a.claim(pending('s1')), true)
    assert.equal(a.claim(pending('s1')), false)
    assert.equal(a.claim(pending('s1')), false)
  })

  it('不同的请求各提醒一次', () => {
    const a = createRequestAnnouncer()
    assert.equal(a.claim(pending('s1')), true)
    assert.equal(a.claim(pending('s2')), true)
    assert.equal(a.claim(pending('s1')), false)
    assert.equal(a.claim(pending('s2')), false)
  })

  it('不是待回复的（已完成 / 取消 / 超时 / 归档）不提醒，也不占名额：之后它真的变成待回复时仍会提醒', () => {
    const a = createRequestAnnouncer()
    for (const status of ['completed', 'cancelled', 'timeout', 'archived']) {
      assert.equal(a.claim({ session_id: 's1', status }), false, status)
    }
    assert.equal(a.claim(pending('s1')), true)
  })

  it('没有 id 的不提醒', () => {
    const a = createRequestAnnouncer()
    assert.equal(a.claim({ session_id: '', status: 'pending' }), false)
  })

  it('封顶：超过上限先忘掉最早的，近期的仍然只提醒一次', () => {
    const a = createRequestAnnouncer(3)
    for (const id of ['a', 'b', 'c']) assert.equal(a.claim(pending(id)), true)
    assert.equal(a.claim(pending('d')), true) // 挤掉最早的 a
    assert.equal(a.claim(pending('b')), false)
    assert.equal(a.claim(pending('c')), false)
    assert.equal(a.claim(pending('d')), false)
    assert.equal(a.claim(pending('a')), true) // a 已被忘掉：极旧的请求会被再提醒一次，这是封顶的代价
  })
})
