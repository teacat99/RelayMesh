// 运行：pnpm --dir frontend run test:unit
// 预览视图的未读（#120 阶段 E-c2）的纯逻辑：哪些消息计入未读、分隔线画在哪、什么时候可以标已读。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { groupBlocks, inRoundKey, type TimelineBlock, type TimelineEntry } from './timeline'
import {
  UNREAD_KINDS,
  isAgentEntry,
  newDynamicsText,
  newSinceAway,
  newestAgentMessage,
  readVerdict,
  renderedAgentMs,
  unreadDividerKey,
  unreadItems,
  unseenNewCount,
  type NewItem,
  type UnreadItem,
} from './previewUnread'

// 造一条最小的时间线条目：只带本模块读到的字段
function entry(kind: TimelineEntry['kind'], key: string, at: string, extra: Record<string, unknown> = {}): TimelineEntry {
  const base: Record<string, unknown> = { kind, key, at }
  if (kind === 'confirm') base.item = { kind: 'ask', status: 'pending', ref: key, ...extra }
  return base as unknown as TimelineEntry
}

const T = (min: number) => `2026-10-18T10:${String(min).padStart(2, '0')}:00.000Z`
const ms = (min: number) => Date.parse(T(min))

describe('isAgentEntry：哪些条目计入未读（与服务端 CountUnreadAgentMessages 同口径）', () => {
  it('进度通知、任务进展、提问 / 计划卡计入', () => {
    assert.equal(isAgentEntry(entry('notice', 'notice-s1', T(1))), true)
    assert.equal(isAgentEntry(entry('task', 'task-u1', T(1))), true)
    assert.equal(isAgentEntry(entry('confirm', 'confirm-1', T(1), { kind: 'ask' })), true)
    assert.equal(isAgentEntry(entry('confirm', 'confirm-2', T(1), { kind: 'plan' })), true)
  })

  it('授权等其他确认卡、您的追加、决策汇总、恢复胶囊不计', () => {
    assert.equal(isAgentEntry(entry('confirm', 'confirm-3', T(1), { kind: 'grant' })), false)
    assert.equal(isAgentEntry(entry('inbox', 'inbox-1', T(1))), false)
    assert.equal(isAgentEntry(entry('decisions', 'decisions-d1', T(1))), false)
    assert.equal(isAgentEntry(entry('capsule', 'capsule-3', T(1))), false)
  })
})

describe('unreadItems：与显示顺序一致（间隙块 → 轮次 → …… → 末尾块）', () => {
  const rounds = [
    { session_id: 'r1', created_at: T(1) },
    { session_id: 'r2', created_at: T(10) },
  ]

  it('每个轮次依次是「它前面的间隙块 → 轮次本身」，最后是末尾块', () => {
    const blocks: Record<string, TimelineBlock[]> = {
      r2: groupBlocks([entry('notice', 'notice-a', T(5))]),
    }
    const tail = groupBlocks([entry('task', 'task-u9', T(20))])
    const items = unreadItems(rounds, blocks, tail)
    assert.deepEqual(
      items.map(i => i.key),
      ['r1', 'notice-a', 'r2', 'task-u9'],
    )
  })

  it('不是 Agent 发的条目（您的追加、恢复胶囊等）不带时间；轮次自带汇报时间', () => {
    const blocks: Record<string, TimelineBlock[]> = {
      r2: groupBlocks([entry('inbox', 'inbox-1', T(5)), entry('capsule', 'capsule-1', T(6))]),
    }
    const items = unreadItems(rounds, blocks, [])
    assert.deepEqual(items.find(i => i.key === 'inbox-1')?.agentAt, [])
    assert.deepEqual(items.find(i => i.key === 'r1')?.agentAt, [T(1)])
  })

  it('夹在提问与回复之间、画在卡里的 Agent 条目算进这一轮；一摞算摞里每张卡', () => {
    const blocks: Record<string, TimelineBlock[]> = {
      [inRoundKey('r1')]: groupBlocks([entry('notice', 'notice-in', T(3))]),
      r2: groupBlocks([entry('task', 'task-u1', T(5)), entry('task', 'task-u2', T(6))]),
    }
    const items = unreadItems(rounds, blocks, [])
    assert.deepEqual(items.find(i => i.key === 'r1')?.agentAt, [T(1), T(3)])
    const stack = items.find(i => i.key.startsWith('stack-'))
    assert.deepEqual(stack?.agentAt, [T(5), T(6)])
  })
})

describe('unreadDividerKey：分隔线画在哪', () => {
  const items: UnreadItem[] = [
    { key: 'r1', agentAt: [T(1)] },
    { key: 'inbox-1', agentAt: [] },
    { key: 'notice-a', agentAt: [T(5)] },
    { key: 'r2', agentAt: [T(10)] },
  ]

  it('第一个含有「晚于游标的 Agent 消息」的顶层项（严格晚于才算未读）', () => {
    assert.equal(unreadDividerKey(items, ms(1)), 'notice-a') // 游标恰在 r1 那一毫秒：r1 已读
    assert.equal(unreadDividerKey(items, ms(5) - 1), 'notice-a')
    assert.equal(unreadDividerKey(items, ms(5)), 'r2')
  })

  it('第一个未读项就是整张列表的第一项：上面没有已读的内容，不画（窗口藏着更早内容时才画）', () => {
    assert.equal(unreadDividerKey(items, 0), null)
    assert.equal(unreadDividerKey(items, 0, true), 'r1')
    assert.equal(unreadDividerKey([{ key: 'k', agentAt: [1000] }], 999), null)
    assert.equal(unreadDividerKey([{ key: 'k', agentAt: [1000] }], 999, true), 'k')
  })

  it('上面只有您自己的话（没有 Agent 消息的项）也算「上面有东西」：画在第一条未读 Agent 消息前面', () => {
    const list: UnreadItem[] = [
      { key: 'inbox-1', agentAt: [] },
      { key: 'r1', agentAt: [T(1)] },
    ]
    assert.equal(unreadDividerKey(list, 0), 'r1')
  })

  it('全都已读、没有 Agent 消息、时间解析不了：都没有分隔线', () => {
    assert.equal(unreadDividerKey(items, ms(10)), null)
    assert.equal(unreadDividerKey([], 0), null)
    assert.equal(unreadDividerKey([{ key: 'x', agentAt: ['not-a-time', undefined, null] }], 0), null)
  })

  it('摞里 / 卡里有新消息时画在整摞 / 整张卡前面，且只取第一个', () => {
    const list: UnreadItem[] = [
      { key: 'r0', agentAt: [T(0)] },
      { key: 'stack-a', agentAt: [T(2), T(8)] },
      { key: 'r2', agentAt: [T(10), T(12)] },
    ]
    assert.equal(unreadDividerKey(list, ms(1)), 'stack-a')
    assert.equal(unreadDividerKey(list, ms(5)), 'stack-a')
    assert.equal(unreadDividerKey(list, ms(9)), 'r2')
  })

  it('支持毫秒数作为时间', () => {
    assert.equal(unreadDividerKey([{ key: 'k', agentAt: [1000] }], 999, true), 'k')
    assert.equal(unreadDividerKey([{ key: 'k', agentAt: [1000] }], 1000, true), null)
  })
})

describe('renderedAgentMs：已经渲染出来的最晚一条 Agent 消息', () => {
  it('取所有项里最大的毫秒；没有为 0；跳过解析不了的', () => {
    assert.equal(renderedAgentMs([]), 0)
    assert.equal(renderedAgentMs([{ key: 'a', agentAt: [] }]), 0)
    assert.equal(
      renderedAgentMs([
        { key: 'a', agentAt: [T(3), 'bad'] },
        { key: 'b', agentAt: [T(9), T(7)] },
      ]),
      ms(9),
    )
  })
})

describe('UNREAD_KINDS：取「最新一条 Agent 消息」时要覆盖计入服务端未读的所有种类', () => {
  it('已答复的汇报是文字类（text）：漏掉它，最新一条计入未读的是已答复的汇报时会拿更早的通知当游标，未读卡住不动', () => {
    assert.deepEqual([...UNREAD_KINDS].sort(), ['ask', 'block', 'plan', 'progress', 'text'])
  })

  it('文字类里也有您的回复 / 追加：靠发送者挑出 Agent 发的那条（已答复的汇报在您的回复前面）', () => {
    const page = [
      { sender: { kind: 'agent' }, sent_at: T(1), cursor: 'notice' },
      { sender: { kind: 'agent' }, sent_at: T(5), cursor: 'report' },
      { sender: { kind: 'human' }, sent_at: T(6), cursor: 'reply' },
    ]
    assert.equal(newestAgentMessage(page)?.cursor, 'report')
  })
})

describe('newestAgentMessage：一页（旧 → 新）里最新的 Agent 消息', () => {
  const m = (kind: string, id: string) => ({ sender: { kind }, sent_at: T(1), cursor: id })

  it('从后往前找第一个 agent 发的', () => {
    assert.equal(newestAgentMessage([m('agent', 'a'), m('agent', 'b'), m('human', 'c')])?.cursor, 'b')
    assert.equal(newestAgentMessage([m('human', 'a')]), null)
    assert.equal(newestAgentMessage([]), null)
  })
})

describe('readVerdict：能不能推进已读游标', () => {
  it('没有未读：none（只同步数字，不发请求）', () => {
    assert.equal(readVerdict({ unread: 0, newestMs: ms(5), renderedMs: ms(9) }), 'none')
  })

  it('服务端最新的 Agent 消息已经渲染出来：mark', () => {
    assert.equal(readVerdict({ unread: 2, newestMs: ms(5), renderedMs: ms(5) }), 'mark')
    assert.equal(readVerdict({ unread: 2, newestMs: ms(5), renderedMs: ms(9) }), 'mark')
  })

  it('还没渲染出来（页面数据落后于服务端）：wait，不替您标成已读', () => {
    assert.equal(readVerdict({ unread: 1, newestMs: ms(9), renderedMs: ms(5) }), 'wait')
    assert.equal(readVerdict({ unread: 1, newestMs: ms(9), renderedMs: 0 }), 'wait')
  })

  it('有未读却找不到最新的 Agent 消息（数据对不上）：wait，由调用方限次后兜底', () => {
    assert.equal(readVerdict({ unread: 1, newestMs: null, renderedMs: ms(9) }), 'wait')
    assert.equal(readVerdict({ unread: 1, newestMs: Number.NaN, renderedMs: ms(9) }), 'wait')
  })
})

describe('离开底部之后的新动态（#254）：哪些算新的、数字怎么跟着滚动变', () => {
  const item = (key: string, ...mins: number[]): UnreadItem => ({ key, agentAt: mins.map(T) })

  it('newSinceAway：只列「严格晚于」离开那一刻的 Agent 消息，按项汇总条数，保持显示顺序', () => {
    const items = [item('s1', 1), item('notice-a', 2, 3), item('s2', 4, 5, 6), item('s3', 7)]
    assert.deepEqual(newSinceAway(items, ms(3)), [
      { key: 's2', count: 3 },
      { key: 's3', count: 1 },
    ])
    // 整数毫秒相等不算新的（与已读游标同一条规则）
    assert.deepEqual(newSinceAway(items, ms(7)), [])
    assert.deepEqual(newSinceAway(items, ms(0)).map(n => n.key), ['s1', 'notice-a', 's2', 's3'])
  })

  it('newSinceAway：轮次里夹的卡也算（一项里多条各计一条）；没有 Agent 消息的项（如只有您的话）不出现', () => {
    const items: UnreadItem[] = [item('s1', 1, 9, 10), { key: 'mine', agentAt: [] }]
    assert.deepEqual(newSinceAway(items, ms(5)), [{ key: 's1', count: 2 }])
  })

  it('newSinceAway：向上载入的更早轮次时间不晚于离开那一刻，不会被算成新的；时间解析不了的不计', () => {
    const items: UnreadItem[] = [item('older', 0), item('s1', 1), { key: 'bad', agentAt: ['not-a-time', undefined, null] }]
    assert.deepEqual(newSinceAway(items, ms(1)), [])
  })

  it('newSinceAway：renderedAgentMs 当离开时刻 → 之后追加的一条通知 / 一轮汇报各是一条新动态', () => {
    const before = [item('s1', 1), item('s2', 2)]
    const away = renderedAgentMs(before)
    assert.deepEqual(newSinceAway(before, away), [])
    const after = [...before, item('notice-x', 3), item('s3', 4)]
    assert.deepEqual(newSinceAway(after, away), [{ key: 'notice-x', count: 1 }, { key: 's3', count: 1 }])
  })

  const LIST: NewItem[] = [
    { key: 'a', count: 1 },
    { key: 'b', count: 2 },
    { key: 'c', count: 1 },
  ]

  it('unseenNewCount：顶边进入视口的项不再计；数字 = 还在视口下方的条数', () => {
    const seen = (visible: string[]) => (key: string) => visible.includes(key)
    assert.deepEqual(unseenNewCount(LIST, seen([])), { count: 4, seen: [] })
    assert.deepEqual(unseenNewCount(LIST, seen(['a'])), { count: 3, seen: ['a'] })
    assert.deepEqual(unseenNewCount(LIST, seen(['a', 'b'])), { count: 1, seen: ['a', 'b'] })
    assert.deepEqual(unseenNewCount(LIST, seen(['a', 'b', 'c'])), { count: 0, seen: ['a', 'b', 'c'] })
  })

  it('unseenNewCount：看过的项记进 seenKeys 之后，往上滚、它又落到视口下方也不会重新计', () => {
    const seenKeys = new Set(['a', 'b'])
    // a、b 现在又在视口下方了（用户往上滚去看历史），但它们已经看过
    assert.deepEqual(unseenNewCount(LIST, () => false, seenKeys), { count: 1, seen: [] })
    assert.deepEqual(unseenNewCount(LIST, () => true, seenKeys), { count: 0, seen: ['c'] })
  })

  it('unseenNewCount：还没渲染出来的项（调用方的 seen 回答 false）按没看到算；空列表是 0', () => {
    assert.deepEqual(unseenNewCount(LIST, () => false), { count: 4, seen: [] })
    assert.deepEqual(unseenNewCount([], () => true), { count: 0, seen: [] })
  })

  it('newDynamicsText：任何条数都带数字（不再有「1 条时没有数字」的特例）', () => {
    assert.equal(newDynamicsText(1), '收到 1 条新动态')
    assert.equal(newDynamicsText(3), '收到 3 条新动态')
    assert.equal(newDynamicsText(12), '收到 12 条新动态')
  })
})
