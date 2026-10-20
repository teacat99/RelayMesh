// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { ChatMessage, ChatSender } from '../api/conversations'
import type { GroupMember } from '../api/groups'
import {
  ALL_MEMBERS_IDENTITY,
  MEMBER_HUES,
  assignMemberHues,
  avatarStack,
  candidateIdentities,
  firstUnreadId,
  groupRenderKind,
  memberAvatar,
  memberHue,
  memberIdentity,
  memberKeyOf,
  senderIdentity,
  senderKeyOf,
  seqOf,
} from './groupCard'
import { MENTION_ALL, mentionCandidates } from './group'

const member = (over: Partial<GroupMember> = {}): GroupMember => ({
  kind: 'workflow', ref: 'wf-a', name: '甲', role: 'member', workflow_id: 'wf-a', push_policy: 'all',
  joined_at: '2026-10-01T00:00:00Z', read_seq: 0, ...over,
})
const msg = (over: Partial<ChatMessage> = {}): ChatMessage => ({
  id: 'm1', conversation_id: 'grp:g1', kind: 'text', sender: { kind: 'agent', id: 'wf-a', name: '甲', workflow_id: 'wf-a' },
  sent_at: '2026-10-01T00:00:00Z', cursor: 'c1', status: 'delivered', source: { type: 'group', id: 'g1' }, hops: 0, ...over,
})
const agentMsg = (id: string, seq: number, over: Partial<ChatMessage> = {}) => msg({ id, meta: { seq }, ...over })

describe('群聊成员的标识与取色（阶段 E-d）', () => {
  it('成员标识：工作流成员取工作流编号（改名不影响），没有就取 ref；发送者的标识与它对得上', () => {
    assert.equal(memberKeyOf({ ref: 'r', workflow_id: 'wf-x' }), 'wf-x')
    assert.equal(memberKeyOf({ ref: 'r', workflow_id: undefined }), 'r')
    assert.equal(memberKeyOf({ ref: 'r', workflow_id: '' }), 'r')
    assert.equal(senderKeyOf({ id: 'i', name: 'n', workflow_id: 'wf-x' }), 'wf-x')
    assert.equal(senderKeyOf({ id: 'i', name: 'n' }), 'i')
    assert.equal(senderKeyOf({ name: 'n' }), 'n')
    const m = member({ ref: 'wf-x', workflow_id: 'wf-x' })
    assert.equal(memberKeyOf(m), senderKeyOf({ name: '随便', workflow_id: 'wf-x' }))
  })

  it('色相表：8 档、互不相同、避开红 / 琥珀 / 绿这三种带含义的颜色', () => {
    assert.equal(MEMBER_HUES.length, 8)
    assert.equal(new Set(MEMBER_HUES).size, 8)
    for (const h of MEMBER_HUES) {
      assert.ok(h >= 160 && h <= 340, `色相 ${h} 落在青 / 蓝 / 紫 / 品红一带，不碰红（0）、琥珀（40）、绿（140）`)
    }
  })

  it('只凭标识取色：稳定（同一个标识永远同一个颜色），结果总在色相表里', () => {
    for (const key of ['wf-a', 'wf-b', 'relaymesh-continuation', '甲', '']) {
      assert.equal(memberHue(key), memberHue(key))
      assert.ok(MEMBER_HUES.includes(memberHue(key)), key)
    }
    assert.equal(memberHue('wf-a'), memberHue('wf-a'), '重复调用不变')
  })

  it('分配：8 个以内互不重复；先入群的颜色不受后来者影响；与输入顺序无关', () => {
    const base = Array.from({ length: 8 }, (_, i) => member({
      ref: `wf-${i}`, workflow_id: `wf-${i}`, name: `成员${i}`, joined_at: `2026-10-0${i + 1}T00:00:00Z`,
    }))
    const all = assignMemberHues(base)
    assert.equal(Object.keys(all).length, 8)
    assert.equal(new Set(Object.values(all)).size, 8, '8 位成员 8 种颜色')
    for (let n = 1; n < 8; n++) {
      const early = assignMemberHues(base.slice(0, n))
      for (const [k, v] of Object.entries(early)) assert.equal(all[k], v, `前 ${n} 位成员的颜色在后来者加入后不变：${k}`)
    }
    assert.deepEqual(assignMemberHues([...base].reverse()), all, '与输入顺序无关')
  })

  it('同时入群的按标识排序，结果仍稳定；重复的成员只算一个', () => {
    const t = '2026-10-01T00:00:00Z'
    const a = member({ ref: 'wf-1', workflow_id: 'wf-1', joined_at: t })
    const b = member({ ref: 'wf-2', workflow_id: 'wf-2', joined_at: t })
    assert.deepEqual(assignMemberHues([a, b]), assignMemberHues([b, a]))
    const twice = assignMemberHues([a, a, b])
    assert.equal(Object.keys(twice).length, 2)
    assert.deepEqual(twice, assignMemberHues([a, b]))
  })

  it('人类成员不分配颜色（您固定是绿色）；离开的成员仍占着他们的颜色', () => {
    const human = member({ kind: 'human', ref: 'me', workflow_id: undefined, joined_at: '2026-09-01T00:00:00Z' })
    const gone = member({ ref: 'wf-gone', workflow_id: 'wf-gone', left: true, joined_at: '2026-10-01T00:00:00Z' })
    const stay = member({ ref: 'wf-stay', workflow_id: 'wf-stay', joined_at: '2026-10-02T00:00:00Z' })
    const out = assignMemberHues([human, gone, stay])
    assert.equal('me' in out, false)
    assert.ok('wf-gone' in out && 'wf-stay' in out)
    assert.notEqual(out['wf-gone'], out['wf-stay'])
  })

  it('超过 8 位成员：不再要求不重复，但每个成员都有颜色、且仍稳定', () => {
    const many = Array.from({ length: 11 }, (_, i) => member({ ref: `wf-${i}`, workflow_id: `wf-${i}`, joined_at: `2026-10-${String(i + 1).padStart(2, '0')}T00:00:00Z` }))
    const out = assignMemberHues(many)
    assert.equal(Object.keys(out).length, 11)
    for (const v of Object.values(out)) assert.ok(MEMBER_HUES.includes(v))
    assert.deepEqual(assignMemberHues(many), out)
  })

  it('没有成员：空表', () => {
    assert.deepEqual(assignMemberHues([]), {})
  })
})

describe('头像字（阶段 E-d）', () => {
  it('取名字的第一个可见字符：中文、emoji 都是一个字，拉丁字母转大写', () => {
    assert.equal(memberAvatar('甲方'), '甲')
    assert.equal(memberAvatar('relaymesh'), 'R')
    assert.equal(memberAvatar('  alice'), 'A')
    assert.equal(memberAvatar('🚀 发布'), '🚀')
    assert.equal(memberAvatar('ß'), 'S', '大写后变成两个字母的也只取第一个')
  })

  it('没有名字是问号', () => {
    assert.equal(memberAvatar(''), '?')
    assert.equal(memberAvatar('   '), '?')
    assert.equal(memberAvatar(undefined), '?')
    assert.equal(memberAvatar(null), '?')
  })
})

describe('发送者在卡片上怎么显示（阶段 E-d）', () => {
  const agent: ChatSender = { kind: 'agent', id: 'wf-a', name: 'Alpha', workflow_id: 'wf-a' }

  it('您：固定叫「您」、头像「您」、没有自己的色相（用类型的绿色）', () => {
    assert.deepEqual(senderIdentity({ kind: 'human', name: 'admin' }), { kind: 'user', name: '您', avatar: '您', hue: null })
  })

  it('系统：用它自己的名字，没有名字叫「系统」，头像「系」，没有色相', () => {
    assert.deepEqual(senderIdentity({ kind: 'system', name: '守护' }), { kind: 'system', name: '守护', avatar: '系', hue: null })
    assert.equal(senderIdentity({ kind: 'system', name: '  ' }).name, '系统')
  })

  it('智能体：名字、头像字、成员表里分到的色相', () => {
    const id = senderIdentity(agent, { 'wf-a': 238 })
    assert.deepEqual(id, { kind: 'agent', name: 'Alpha', avatar: 'A', hue: 238 })
  })

  it('成员表里找不到（已被移出、成员表还没取到）：退回只凭标识的哈希色，仍稳定', () => {
    const a = senderIdentity(agent, {})
    const b = senderIdentity(agent)
    assert.equal(a.hue, memberHue('wf-a'))
    assert.equal(b.hue, a.hue)
  })

  it('没有名字：依次退回工作流编号、发送者编号、「成员」', () => {
    assert.equal(senderIdentity({ kind: 'agent', name: '', workflow_id: 'wf-z' }).name, 'wf-z')
    assert.equal(senderIdentity({ kind: 'agent', name: '', id: 'i-9' }).name, 'i-9')
    assert.equal(senderIdentity({ kind: 'agent', name: '  ' }).name, '成员')
  })
})

describe('哪些群消息套卡片（阶段 E-d）', () => {
  it('普通消息、带回复的消息套卡片', () => {
    assert.equal(groupRenderKind(msg()), 'card')
    assert.equal(groupRenderKind(msg({ kind: 'alert' })), 'card')
    assert.equal(groupRenderKind(msg({ reply_to: 'm0' })), 'card')
    assert.equal(groupRenderKind(msg({ sender: { kind: 'human', name: 'admin' } })), 'card')
  })

  it('系统行、带卡片的提问 / 计划、决策汇总、任务进展走事件行（GroupEventRow）', () => {
    assert.equal(groupRenderKind(msg({ sender: { kind: 'system', name: '系统' } })), 'event')
    assert.equal(groupRenderKind(msg({ kind: 'ask', card: { id: 'c' } as unknown as ChatMessage['card'] })), 'event')
    assert.equal(groupRenderKind(msg({ kind: 'plan', card: { id: 'c' } as unknown as ChatMessage['card'] })), 'event')
    assert.equal(groupRenderKind(msg({ kind: 'decision', decision: { id: 'd' } as unknown as ChatMessage['decision'] })), 'event')
    assert.equal(groupRenderKind(msg({ kind: 'progress', task: { id: 1 } as unknown as ChatMessage['task'] })), 'event')
  })

  it('没有携带对应数据的提问 / 计划 / 决策 / 进展，按普通消息画（不会因为缺数据画不出来）', () => {
    assert.equal(groupRenderKind(msg({ kind: 'ask' })), 'card')
    assert.equal(groupRenderKind(msg({ kind: 'plan' })), 'card')
    assert.equal(groupRenderKind(msg({ kind: 'decision' })), 'card')
    assert.equal(groupRenderKind(msg({ kind: 'progress' })), 'card')
  })
})

describe('群消息的序号（阶段 E-d）', () => {
  it('取 meta.seq；不是有限数字或没有 meta 都是 NaN（所有比较为假）', () => {
    assert.equal(seqOf({ meta: { seq: 7 } }), 7)
    assert.equal(seqOf({ meta: { seq: 0 } }), 0)
    assert.ok(Number.isNaN(seqOf({})))
    assert.ok(Number.isNaN(seqOf({ meta: {} })))
    assert.ok(Number.isNaN(seqOf({ meta: { seq: '7' } })))
    assert.ok(Number.isNaN(seqOf({ meta: { seq: Number.POSITIVE_INFINITY } })))
    assert.ok(Number.isNaN(seqOf({ meta: { seq: Number.NaN } })))
    assert.equal(Number.NaN > 3, false)
  })
})

describe('「以下为新消息」画在哪（阶段 E-d）', () => {
  const items = [agentMsg('a', 1), agentMsg('b', 2), agentMsg('c', 3), agentMsg('d', 4)]

  it('游标之后的第一条别人的消息', () => {
    assert.equal(firstUnreadId(items, 2, false), 'c')
    assert.equal(firstUnreadId(items, 1, false), 'b')
    assert.equal(firstUnreadId(items, 3, false), 'd')
  })

  it('从没读过（游标 ≤ 0 或不是数字）：不画，所有消息都是新的，分隔线只是噪音', () => {
    assert.equal(firstUnreadId(items, 0, false), '')
    assert.equal(firstUnreadId(items, -1, false), '')
    assert.equal(firstUnreadId(items, Number.NaN, false), '')
  })

  it('游标之后没有别人的消息（已读到最新）：不画', () => {
    assert.equal(firstUnreadId(items, 4, false), '')
    assert.equal(firstUnreadId(items, 99, false), '')
    assert.equal(firstUnreadId([], 3, false), '')
  })

  it('您自己与系统的消息不算「新消息」：只有智能体的才算', () => {
    const mixed = [
      agentMsg('a', 1),
      msg({ id: 'me', sender: { kind: 'human', name: 'admin' }, meta: { seq: 2 } }),
      msg({ id: 'sys', sender: { kind: 'system', name: '系统' }, meta: { seq: 3 } }),
      agentMsg('z', 4),
    ]
    assert.equal(firstUnreadId(mixed, 1, false), 'z')
    assert.equal(firstUnreadId(mixed, 3, false), 'z')
  })

  it('它就是整段列表的第一条：前面还有更早的历史才画，没有就不画（前面没有已读的消息可分隔）', () => {
    assert.equal(firstUnreadId(items, 0.5, false), '', '第一条就是未读、前面没有历史')
    assert.equal(firstUnreadId(items, 0.5, true), 'a', '前面还有更早的历史（没加载出来）：画')
  })

  it('没有序号的消息（旧数据）不会被当成新消息', () => {
    const old = [msg({ id: 'x' }), msg({ id: 'y' })]
    assert.equal(firstUnreadId(old, 5, true), '')
  })
})

describe('成员头像：面板 / 页头头像堆 / @ 选择器与消息卡同一份身份（阶段 E-d2）', () => {
  const a = member({ ref: 'wf-a', workflow_id: 'wf-a', name: '甲', joined_at: '2026-10-01T00:00:00Z' })
  const b = member({ ref: 'wf-b', workflow_id: 'wf-b', name: 'Beta', joined_at: '2026-10-01T00:01:00Z' })
  const c = member({ ref: 'wf-c', workflow_id: 'wf-c', name: '丙', joined_at: '2026-10-01T00:02:00Z' })
  const d = member({ ref: 'wf-d', workflow_id: 'wf-d', name: '丁', joined_at: '2026-10-01T00:03:00Z' })
  const gone = member({ ref: 'wf-x', workflow_id: 'wf-x', name: '已走', left: true, joined_at: '2026-10-01T00:00:30Z' })
  const you = member({ kind: 'human', ref: 'user', name: '你', workflow_id: undefined, joined_at: '2026-10-01T00:00:00Z' })
  const all = [you, a, gone, b, c, d]
  const hues = assignMemberHues(all)

  it('成员 → 身份：同一位成员的颜色与字，与他发的消息卡上的发送者完全一致（同一把钥匙）', () => {
    for (const m of [a, b, c, d]) {
      const fromTable = memberIdentity(m, hues)
      const fromMessage = senderIdentity({ kind: 'agent', id: m.ref, name: m.name, workflow_id: m.workflow_id }, hues)
      assert.deepEqual(fromTable, fromMessage, `${m.name}：成员面板与消息卡必须同色同字`)
    }
    assert.equal(memberIdentity(b, hues).avatar, 'B', '拉丁字母转大写')
    assert.equal(memberIdentity(a, hues).avatar, '甲')
  })

  it('成员 → 身份：人类是「您」+ 无色相（固定绿）；没有名字退回工作流编号 / ref；没有色相表时按标识取稳定哈希色', () => {
    assert.deepEqual(memberIdentity(you, hues), { kind: 'user', name: '您', avatar: '您', hue: null })
    const nameless = member({ ref: 'wf-n', workflow_id: 'wf-n', name: '  ' })
    assert.equal(memberIdentity(nameless, hues).name, 'wf-n')
    const bare = member({ ref: 'only-ref', workflow_id: undefined, name: '' })
    assert.equal(memberIdentity(bare).name, 'only-ref')
    assert.equal(memberIdentity(a).hue, memberHue('wf-a'), '没传色相表：退回单纯的哈希色')
  })

  it('头像堆：只含还在群里的智能体（您自己与已离开的不占位），按成员表顺序取前 max 位，其余合成 +N', () => {
    const s3 = avatarStack(all, 3)
    assert.deepEqual(s3.shown.map(m => m.ref), ['wf-a', 'wf-b', 'wf-c'])
    assert.equal(s3.more, 1)
    const s9 = avatarStack(all, 9)
    assert.equal(s9.shown.length, 4)
    assert.equal(s9.more, 0)
    const s0 = avatarStack(all, 0)
    assert.deepEqual(s0, { shown: [], more: 4 })
    assert.deepEqual(avatarStack([you, gone], 3), { shown: [], more: 0 })
    assert.deepEqual(avatarStack([], 3), { shown: [], more: 0 })
  })

  it('头像堆：max 取非负整数（负数当 0、小数向下取整），不改传入的数组', () => {
    assert.deepEqual(avatarStack(all, -2), { shown: [], more: 4 })
    assert.equal(avatarStack(all, 2.9).shown.length, 2)
    const before = all.map(m => m.ref).join(',')
    avatarStack(all, 3)
    assert.equal(all.map(m => m.ref).join(','), before)
  })

  it('@ 候选 → 头像：成员按 ref 回成员表取同一份身份，「所有人」固定中性色，找不到成员的候选不出现', () => {
    const cands = mentionCandidates(all)
    const ids = candidateIdentities(cands, all, hues)
    assert.deepEqual(Object.keys(ids).sort(), ['all', 'wf-a', 'wf-b', 'wf-c', 'wf-d'], '已离开的成员本来就不在候选里')
    assert.deepEqual(ids['wf-a'], memberIdentity(a, hues))
    assert.deepEqual(ids[MENTION_ALL], ALL_MEMBERS_IDENTITY)
    assert.equal(ids[MENTION_ALL].hue, null)
    assert.deepEqual(candidateIdentities([{ ref: 'wf-zzz', name: '陌生' }], all, hues), {}, '成员表里没有这个人：不造假身份')
    assert.deepEqual(candidateIdentities([], all, hues), {})
  })

  it('「所有人」的身份是只读副本：改结果不会污染常量', () => {
    const ids = candidateIdentities([{ ref: MENTION_ALL, name: '所有人' }], all, hues)
    ids[MENTION_ALL].avatar = '改'
    assert.equal(ALL_MEMBERS_IDENTITY.avatar, '全')
  })
})
