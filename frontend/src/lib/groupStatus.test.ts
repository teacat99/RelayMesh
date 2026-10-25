import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { GroupMember } from '../api/groups'
import { deriveGroupStatus, memberBucket, overlayLiveState, type LiveGroupMember } from './groupStatus'

function member(p: Partial<GroupMember>): GroupMember {
  return { kind: 'workflow', ref: 'wf-a', name: '前端 A', role: 'member', push_policy: 'mention', joined_at: '2026-10-15T00:00:00Z', read_seq: 0, ...p }
}

/** 叠加过实时状态的成员：比群页带的多一个 stalled（硬卡住，只来自实时会话）。 */
function live(p: Partial<LiveGroupMember>): LiveGroupMember {
  return { ...member(p), stalled: p.stalled }
}

describe('memberBucket', () => {
  test('每种状态落在哪一档', () => {
    const cases: Array<[Partial<GroupMember>, string]> = [
      [{ state: 'working' }, 'working'],
      [{ state: 'idle' }, 'idle'],
      [{ state: 'aborted' }, 'idle'],
      [{ state: 'error' }, 'error'],
      [{ state: 'waiting_approval' }, 'waiting_approval'],
      [{ state: 'waiting_you' }, 'waiting_you'],
      [{ state: 'ended' }, 'away'],
      [{ state: 'offline' }, 'away'],
      [{ state: '' }, 'away'],
      [{}, 'away'],
      [{ state: 'something-new' }, 'away'],
    ]
    for (const [p, want] of cases) assert.equal(memberBucket(member(p)), want, JSON.stringify(p))
  })
  test('等你回答的标志盖过状态本身（服务端在 working / idle 之上叠加）', () => {
    assert.equal(memberBucket(member({ state: 'working', waiting_you: true })), 'waiting_you')
    assert.equal(memberBucket(member({ state: 'idle', waiting_you: true })), 'waiting_you')
  })
  test('硬卡住（stalled，只来自实时会话）单列一档，与 stateView 同口径：有 stalled 就是疑似卡住；等你回答仍排在它前面', () => {
    assert.equal(memberBucket(live({ state: 'working', stalled: true })), 'stalled')
    assert.equal(memberBucket(live({ state: 'working', stalled: false })), 'working')
    assert.equal(memberBucket(live({ state: 'working' })), 'working')
    assert.equal(memberBucket(live({ state: 'working', stalled: true, waiting_you: true })), 'waiting_you')
    // 群页带的成员里没有 stalled 这个字段：当没卡住
    assert.equal(memberBucket(member({ state: 'working' })), 'working')
  })
})

describe('deriveGroupStatus', () => {
  test('群里没有在职的 Agent 成员：不画徽标', () => {
    assert.equal(deriveGroupStatus([]), null)
    assert.equal(deriveGroupStatus([member({ kind: 'human', ref: 'user', name: '你' })]), null)
    assert.equal(deriveGroupStatus([member({ state: 'working', left: true })]), null)
  })

  test('有人在等你：排在最前，数的是在等你的人数，其余状态的人只在悬停说明里', () => {
    const s = deriveGroupStatus([
      member({ ref: 'a', name: '前端 A', state: 'working', waiting_you: true }),
      member({ ref: 'b', name: '后端 B', state: 'idle', waiting_you: true }),
      member({ ref: 'c', name: '测试 C', state: 'working' }),
      member({ ref: 'd', name: '运维 D', state: 'offline' }),
    ])
    assert.ok(s)
    assert.equal(s.key, 'waiting_you')
    assert.equal(s.tone, 'attention')
    assert.equal(s.count, 2)
    assert.equal(s.label, '2 位在等你')
    assert.equal(s.shortLabel, '等你 2')
    assert.equal(s.pulse, true)
    assert.equal(s.title, '在等你回答：前端 A、后端 B\n工作中：测试 C\n不在线：运维 D')
  })

  test('等授权排在工作中前面、也算「需要你」的琥珀色', () => {
    const s = deriveGroupStatus([member({ ref: 'a', state: 'waiting_approval' }), member({ ref: 'b', state: 'working' })])
    assert.ok(s)
    assert.equal(s.key, 'waiting_approval')
    assert.equal(s.tone, 'attention')
    assert.equal(s.label, '1 位等授权')
    assert.equal(s.shortLabel, '授权 1')
  })

  test('疑似卡住排在工作中前面、等授权后面：红色、点跳动；没卡住的人只在悬停说明里（与侧栏「疑似卡住」同口径）', () => {
    const s = deriveGroupStatus([
      live({ ref: 'a', name: '前端 A', state: 'working', stalled: true }),
      live({ ref: 'b', name: '后端 B', state: 'working' }),
      live({ ref: 'c', name: '测试 C', state: 'working', stalled: true }),
    ])
    assert.ok(s)
    assert.equal(s.key, 'stalled')
    assert.equal(s.tone, 'danger')
    assert.equal(s.count, 2)
    assert.equal(s.label, '2 位疑似卡住')
    assert.equal(s.shortLabel, '卡住 2')
    assert.equal(s.pulse, true)
    assert.equal(s.title, '疑似卡住：前端 A、测试 C\n工作中：后端 B')
    // 有人等你 / 等授权时，它们仍排在疑似卡住前面
    assert.equal(deriveGroupStatus([live({ ref: 'a', state: 'working', stalled: true }), live({ ref: 'b', state: 'working', waiting_you: true })])?.key, 'waiting_you')
    assert.equal(deriveGroupStatus([live({ ref: 'a', state: 'working', stalled: true }), live({ ref: 'b', state: 'waiting_approval' })])?.key, 'waiting_approval')
  })

  test('没人等你时，在干活的排最前（跳动的绿点）', () => {
    const s = deriveGroupStatus([
      member({ ref: 'a', state: 'working' }),
      member({ ref: 'b', state: 'working' }),
      member({ ref: 'c', state: 'working' }),
      member({ ref: 'd', state: 'idle' }),
    ])
    assert.ok(s)
    assert.equal(s.key, 'working')
    assert.equal(s.tone, 'active')
    assert.equal(s.count, 3)
    assert.equal(s.label, '3 位工作中')
    assert.equal(s.shortLabel, '工作 3')
    assert.equal(s.pulse, true)
  })

  test('出错排在工作中后面（与会话页徽标同一顺序：干活的优先于出问题的），没人干活时才说出错', () => {
    const busy = deriveGroupStatus([member({ ref: 'a', state: 'error' }), member({ ref: 'b', state: 'working' })])
    assert.equal(busy?.key, 'working')
    const failed = deriveGroupStatus([member({ ref: 'a', state: 'error' }), member({ ref: 'b', state: 'idle' })])
    assert.ok(failed)
    assert.equal(failed.key, 'error')
    assert.equal(failed.tone, 'danger')
    assert.equal(failed.label, '1 位出错')
    assert.equal(failed.pulse, false)
  })

  test('只剩空闲 / 已中断：说「空闲」（不写数字）；数量仍在 count 里', () => {
    const s = deriveGroupStatus([member({ ref: 'a', state: 'idle' }), member({ ref: 'b', state: 'aborted' }), member({ ref: 'c', state: 'ended' })])
    assert.ok(s)
    assert.equal(s.key, 'idle')
    assert.equal(s.tone, 'muted')
    assert.equal(s.count, 2)
    assert.equal(s.label, '空闲')
    assert.equal(s.shortLabel, '空闲')
    assert.equal(s.pulse, false)
  })

  test('全都不在线 / 还没接入：说「都不在线」', () => {
    const s = deriveGroupStatus([member({ ref: 'a', state: 'offline' }), member({ ref: 'b', state: 'ended' }), member({ ref: 'c' })])
    assert.ok(s)
    assert.equal(s.key, 'away')
    assert.equal(s.count, 3)
    assert.equal(s.label, '都不在线')
    assert.equal(s.shortLabel, '离线')
    assert.equal(s.tone, 'muted')
  })

  test('已离开的成员与您自己不参与；名字空着时用编号', () => {
    const s = deriveGroupStatus([
      member({ kind: 'human', ref: 'user', name: '你', state: 'working' }),
      member({ ref: 'gone', name: '走了的', state: 'working', left: true }),
      member({ ref: 'wf-x', name: '  ', state: 'idle' }),
    ])
    assert.ok(s)
    assert.equal(s.key, 'idle')
    assert.equal(s.count, 1)
    assert.equal(s.title, '空闲：wf-x')
  })

  test('悬停说明一行一个状态，按「越要紧越靠前」排', () => {
    const s = deriveGroupStatus([
      member({ ref: 'a', name: 'A', state: 'offline' }),
      member({ ref: 'b', name: 'B', state: 'idle' }),
      member({ ref: 'c', name: 'C', state: 'error' }),
      member({ ref: 'd', name: 'D', state: 'working' }),
      member({ ref: 'e', name: 'E', state: 'waiting_approval' }),
      member({ ref: 'f', name: 'F', state: 'working', waiting_you: true }),
      live({ ref: 'g', name: 'G', state: 'working', stalled: true }),
    ])
    assert.equal(s?.title, ['在等你回答：F', '等授权：E', '疑似卡住：G', '工作中：D', '出错：C', '空闲：B', '不在线：A'].join('\n'))
  })
})

describe('overlayLiveState', () => {
  test('实时会话盖掉页面带的状态：这就是「不靠群里的消息事件也能跟上」', () => {
    const members = [member({ ref: 'wf-a', state: 'working', sub: 'thinking' })]
    const out = overlayLiveState(members, () => ({ state: 'idle', sub: undefined, waiting_you: false }))
    assert.equal(out[0].state, 'idle')
    assert.equal(out[0].sub, undefined)
    assert.equal(out[0].waiting_you, false)
    // 盖完再汇总，徽标跟着实时的走
    assert.equal(deriveGroupStatus(members)?.key, 'working')
    assert.equal(deriveGroupStatus(out)?.key, 'idle')
  })

  test('实时会话说「在等你回答」：页面带的是 working 也变成等你', () => {
    const out = overlayLiveState([member({ ref: 'wf-a', state: 'working' })], () => ({ state: 'waiting_you', sub: undefined, waiting_you: true }))
    assert.equal(deriveGroupStatus(out)?.key, 'waiting_you')
  })

  test('实时会话的硬卡住标志跟着盖上来（群页带的成员里没有它）；没有标志就是没卡住', () => {
    const members = [member({ ref: 'wf-a', state: 'working' }), member({ ref: 'wf-b', state: 'working' })]
    const out = overlayLiveState(members, ref => ({ state: 'working', sub: undefined, waiting_you: false, stalled: ref === 'wf-a' }))
    assert.equal(out[0].stalled, true)
    assert.equal(out[1].stalled, false)
    assert.equal(deriveGroupStatus(members)?.key, 'working')
    assert.equal(deriveGroupStatus(out)?.key, 'stalled')
    // 实时源里没有 stalled 字段：按没卡住算，不是 undefined 漏过去
    assert.equal(overlayLiveState(members, () => ({ state: 'working', sub: undefined, waiting_you: false }))[0].stalled, false)
  })

  test('没有实时会话、或实时会话没有状态：原样返回这位成员（同一个对象）', () => {
    const a = member({ ref: 'wf-a', state: 'working' })
    const b = member({ ref: 'wf-b', state: 'idle' })
    const out = overlayLiveState([a, b], ref => (ref === 'wf-a' ? null : { state: '', sub: undefined, waiting_you: false }))
    assert.equal(out[0], a)
    assert.equal(out[1], b)
    assert.equal(overlayLiveState([a], () => undefined)[0], a)
  })

  test('您自己与已离开的成员不查实时、原样返回', () => {
    const asked: string[] = []
    const human = member({ kind: 'human', ref: 'user', name: '你' })
    const gone = member({ ref: 'wf-gone', state: 'idle', left: true })
    const out = overlayLiveState([human, gone], ref => {
      asked.push(ref)
      return { state: 'working', sub: undefined, waiting_you: false }
    })
    assert.deepEqual(asked, [])
    assert.equal(out[0], human)
    assert.equal(out[1], gone)
  })

  test('按工作流编号查实时（workflow_id 优先，没有就用 ref）；不改传入的成员', () => {
    const asked: string[] = []
    const a = member({ ref: 'ref-a', workflow_id: 'wf-a', state: 'idle' })
    const b = member({ ref: 'wf-b', state: 'idle' })
    const out = overlayLiveState([a, b], id => {
      asked.push(id)
      return { state: 'working', sub: 'tool:x', waiting_you: false }
    })
    assert.deepEqual(asked, ['wf-a', 'wf-b'])
    assert.equal(out[0].state, 'working')
    assert.equal(out[0].sub, 'tool:x')
    assert.equal(a.state, 'idle')
    assert.equal(a.sub, undefined)
    assert.equal(b.state, 'idle')
  })
})
