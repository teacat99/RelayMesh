import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { GroupMember } from '../api/groups'
import {
  extractMentions,
  filterCandidates,
  groupPreviewLine,
  groupTotals,
  insertMention,
  memberStatusText,
  memberTone,
  mentionCandidates,
  mentionQuery,
  readsLabel,
  readsTitle,
} from './group'

function member(p: Partial<GroupMember>): GroupMember {
  return { kind: 'workflow', ref: 'wf-a', name: '前端 A', role: 'member', push_policy: 'mention', joined_at: '2026-10-15T00:00:00Z', read_seq: 0, ...p }
}

describe('mentionCandidates', () => {
  test('只含在职 Agent 与「所有人」，不含你自己和已离开的', () => {
    const list = mentionCandidates([
      member({ kind: 'human', ref: 'user', name: '你' }),
      member({ ref: 'wf-a', name: '前端 A' }),
      member({ ref: 'wf-b', name: '后端 B', left: true }),
      member({ ref: 'wf-c', name: '' }),
    ])
    assert.deepEqual(list.map(c => c.ref), ['wf-a', 'wf-c', 'all'])
    assert.equal(list[1].name, 'wf-c')
  })
  test('没有 Agent 成员时不给「所有人」', () => {
    assert.deepEqual(mentionCandidates([member({ kind: 'human', ref: 'user', name: '你' })]), [])
  })
})

describe('extractMentions', () => {
  const cands = [
    { ref: 'wf-a', name: '前端' },
    { ref: 'wf-b', name: '前端 B' },
    { ref: 'wf-c', name: 'api' },
    { ref: 'all', name: '所有人' },
  ]
  test('按名字解析，长名优先，去重并保持顺序', () => {
    assert.deepEqual(extractMentions('@前端 B 看下，@api 也看；@前端 B 再确认', cands), ['wf-b', 'wf-c'])
  })
  test('@all 与 @所有人 都是所有人', () => {
    assert.deepEqual(extractMentions('@all 请注意', cands), ['all'])
    assert.deepEqual(extractMentions('大家好 @所有人', cands), ['all'])
  })
  test('名字后必须是边界：@前端Bx 不算 @前端', () => {
    assert.deepEqual(extractMentions('@前端Bx 你好', cands), [])
    assert.deepEqual(extractMentions('@前端，你好', cands), ['wf-a'])
  })
  test('邮箱里的 @ 不算', () => {
    assert.deepEqual(extractMentions('发到 me@api 吧', cands), [])
  })
  test('没有候选名时没有结果', () => {
    assert.deepEqual(extractMentions('@谁 在吗', cands), [])
  })
})

describe('mentionQuery / filterCandidates / insertMention', () => {
  test('光标前正在输入 @ 查询', () => {
    assert.deepEqual(mentionQuery('你好 @fr', 6), { start: 3, query: 'fr' })
    assert.deepEqual(mentionQuery('@', 1), { start: 0, query: '' })
  })
  test('已经输入过空白、或 @ 不在词首，就不是查询', () => {
    assert.equal(mentionQuery('@前端 看', 5), null)
    assert.equal(mentionQuery('a@b', 3), null)
    assert.equal(mentionQuery('没有', 2), null)
  })
  test('过滤：名字或编号，不区分大小写', () => {
    const c = [{ ref: 'wf-A1', name: '前端' }, { ref: 'wf-b', name: 'Backend' }]
    assert.deepEqual(filterCandidates(c, 'back').map(x => x.ref), ['wf-b'])
    assert.deepEqual(filterCandidates(c, 'a1').map(x => x.ref), ['wf-A1'])
    assert.equal(filterCandidates(c, '').length, 2)
  })
  test('插入：替换光标前的 @查询，光标落在空格后', () => {
    const r = insertMention('你好 @fr 再说', 6, 3, '前端')
    assert.equal(r.text, '你好 @前端  再说')
    assert.equal(r.caret, 3 + '@前端 '.length)
  })
})

describe('reads', () => {
  test('文案', () => {
    assert.equal(readsLabel(undefined), '')
    assert.equal(readsLabel({ read: 0, total: 0 }), '')
    assert.equal(readsLabel({ read: 1, total: 3 }), '已读 1/3')
    assert.equal(readsLabel({ read: 3, total: 3 }), '全部已读')
  })
  test('悬停提示分已读与未读', () => {
    assert.equal(readsTitle({ read: 1, total: 2, readers: ['A'], unread: ['B'] }), '已读：A\n未读：B')
    assert.equal(readsTitle({ read: 2, total: 2, readers: ['A', 'B'] }), '已读：A、B')
  })
})

describe('member status', () => {
  test('文案与颜色', () => {
    assert.equal(memberStatusText(member({ state: 'working', sub: 'thinking' })), '工作中 · 思考')
    assert.equal(memberStatusText(member({ state: 'idle' })), '空闲')
    assert.equal(memberStatusText(member({ state: 'working', waiting_you: true })), '在等你回答')
    assert.equal(memberStatusText(member({ kind: 'human', state: 'idle' })), '')
    assert.equal(memberStatusText(member({ state: 'weird' })), '')
    assert.equal(memberTone({ state: 'working' }), 'active')
    assert.equal(memberTone({ state: 'idle', waiting_you: true }), 'attention')
    assert.equal(memberTone({ state: 'error' }), 'danger')
    assert.equal(memberTone({ state: 'offline' }), 'muted')
  })
  test('服务端判出的硬卡住（stalled）写「疑似卡住」、红色；等你回答仍优先；没有这个标志时一切照旧', () => {
    assert.equal(memberStatusText({ ...member({ state: 'working', sub: 'thinking' }), stalled: true }), '疑似卡住')
    assert.equal(memberStatusText({ ...member({ state: 'working', sub: 'thinking' }), stalled: false }), '工作中 · 思考')
    assert.equal(memberStatusText({ ...member({ state: 'working', waiting_you: true }), stalled: true }), '在等你回答')
    assert.equal(memberStatusText({ ...member({ kind: 'human', state: 'working' }), stalled: true }), '')
    assert.equal(memberTone({ state: 'working', stalled: true }), 'danger')
    assert.equal(memberTone({ state: 'working', stalled: false }), 'active')
    assert.equal(memberTone({ state: 'working', waiting_you: true, stalled: true }), 'attention')
  })
})

describe('summaries', () => {
  test('合计不含已归档的群', () => {
    assert.deepEqual(
      groupTotals([
        { unread: 2, mentioned: 1, archived: false },
        { unread: 5, mentioned: 2, archived: true },
        { unread: 1, mentioned: 0, archived: false },
      ]),
      { unread: 3, mentioned: 1 },
    )
  })
  test('预览行带发送者', () => {
    assert.equal(groupPreviewLine({ preview: '' }), '还没有消息')
    assert.equal(groupPreviewLine({ preview: '好了' }), '好了')
    assert.equal(
      groupPreviewLine({ preview: '好了', last: { sender: { kind: 'agent', name: '后端 B' } } as never }),
      '后端 B：好了',
    )
    assert.equal(groupPreviewLine({ preview: '收到', last: { sender: { kind: 'human', name: '你' } } as never }), '你：收到')
  })
})
