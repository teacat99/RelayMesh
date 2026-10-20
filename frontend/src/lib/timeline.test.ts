import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { ConfirmBoardGuard, ConfirmItem } from '../api/confirms'
import {
  TIMELINE_TAIL_KEY,
  blockHoldsConfirm,
  inRoundKey,
  confirmEntries,
  entryPreview,
  groupBlocks,
  groupDecisions,
  guardEntries,
  isStackable,
  placeTimeline,
  sortTimeline,
  stackKeysHoldingConfirm,
  stackSummaryText,
  summarizeStack,
  type TimelineEntry,
} from './timeline'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 夹具在仓库根的 internal/chat/testdata 下：运行时按文件读取（构建镜像里只有 frontend/，类型检查不能依赖它存在）。单元测试总是在 frontend/ 目录下运行。
const tieFixture = JSON.parse(readFileSync(resolve(process.cwd(), '../internal/chat/testdata/tie_order.json'), 'utf8')) as {
  order: string[]
  frontend_kind: Record<string, string>
}

// ---- 造数据的小工具：只填被测代码会读的字段 ----
const inbox = (id: number, at: string): TimelineEntry =>
  ({ kind: 'inbox', key: `inbox-${id}`, at, q: { id } as never })
const notice = (id: string, at: string): TimelineEntry =>
  ({ kind: 'notice', key: `notice-${id}`, at, session: { session_id: id } as never })
const task = (seq: number, at: string, uid = seq): TimelineEntry =>
  ({ kind: 'task', key: `task-u${uid}`, at, card: { key: `task-u${uid}`, seq, title: `任务${seq}`, state: 'doing', verb: '进展', text: '', at } })
const confirmItem = (seq: number, at: string, over: Partial<ConfirmItem> = {}): ConfirmItem =>
  ({
    id: seq, workflow_id: 'wf', seq, ref: `Q${seq}`, kind: 'ask', title: `问题${seq}`, options: [], multi: false,
    allow_note: false, risk: 'L1', status: 'pending', created_by: 'agent', created_at: at, updated_at: at, ...over,
  }) as ConfirmItem
const confirm = (seq: number, at: string, status: ConfirmItem['status'] = 'pending'): TimelineEntry =>
  ({ kind: 'confirm', key: `confirm-${seq}`, at, item: confirmItem(seq, at, { status }) })
const capsule = (rev: number, at: string): TimelineEntry =>
  ({ kind: 'capsule', key: `capsule-${rev}`, at, capsule: { revision: rev } as never })

const keys = (xs: ReadonlyArray<{ key: string }>) => xs.map(x => x.key)

const rounds = [
  { session_id: 'r1', created_at: '2026-10-13T10:00:00Z' },
  { session_id: 'r2', created_at: '2026-10-13T11:00:00Z' },
]

describe('sortTimeline：所有种类按提交时间排成一条流（#52）', () => {
  test('不同种类交错时严格按时间，不再「一类一类」排', () => {
    const out = sortTimeline([
      confirm(1, '2026-10-13T10:05:00Z'),
      inbox(1, '2026-10-13T10:01:00Z'),
      task(3, '2026-10-13T10:04:00Z'),
      notice('n1', '2026-10-13T10:02:00Z'),
      capsule(2, '2026-10-13T10:03:00Z'),
    ])
    assert.deepEqual(keys(out), ['inbox-1', 'notice-n1', 'capsule-2', 'task-u3', 'confirm-1'])
  })

  test('同一时刻：您的话在前，其次智能体的话、胶囊、卡片；再同则保持传入顺序', () => {
    const t = '2026-10-13T10:30:00Z'
    const out = sortTimeline([task(1, t, 11), confirm(2, t), notice('n', t), inbox(5, t), task(2, t, 12)])
    assert.deepEqual(keys(out), ['inbox-5', 'notice-n', 'task-u11', 'task-u12', 'confirm-2'])
  })

  test('不修改传入的数组', () => {
    const input = [notice('b', '2026-10-13T10:02:00Z'), inbox(1, '2026-10-13T10:01:00Z')]
    const copy = [...input]
    sortTimeline(input)
    assert.deepEqual(input, copy)
  })

  test('守护告警没有开始时间时排在最后；其余无法解析的时间当作最早', () => {
    const guard = guardEntries([{ preset_id: 'p1', name: '守护', verdict: { state: 'stalled' } } as ConfirmBoardGuard])[0]
    const out = sortTimeline([guard, notice('late', '2026-10-13T23:00:00Z'), inbox(1, 'not-a-date')])
    assert.deepEqual(keys(out), ['inbox-1', 'notice-late', 'guard-p1'])
  })
})

describe('placeTimeline：落到两轮之间', () => {
  test('落在第一个比它晚的轮次之前，比所有轮次都新的落在末尾', () => {
    const out = placeTimeline(rounds, [
      inbox(1, '2026-10-13T09:00:00Z'),
      notice('n', '2026-10-13T10:30:00Z'),
      confirm(1, '2026-10-13T12:00:00Z'),
    ])
    assert.deepEqual(keys(out['r1']), ['inbox-1'])
    assert.deepEqual(keys(out['r2']), ['notice-n'])
    assert.deepEqual(keys(out[TIMELINE_TAIL_KEY]), ['confirm-1'])
  })

  test('与轮次同一时刻：落在它之后（不是之前）', () => {
    const out = placeTimeline(rounds, [inbox(1, '2026-10-13T10:00:00Z')])
    assert.deepEqual(keys(out['r2']), ['inbox-1'])
  })

  test('回归 #52：同一个间隙里的四种条目按提交时间交错，而不是每种各成一段', () => {
    const out = placeTimeline(rounds, [
      // 故意打乱传入顺序，并让「同一种类」的条目在时间上被别的种类隔开
      task(2, '2026-10-13T10:50:00Z', 22),
      inbox(2, '2026-10-13T10:40:00Z'),
      confirm(1, '2026-10-13T10:20:00Z', 'answered'),
      notice('n1', '2026-10-13T10:10:00Z'),
      inbox(1, '2026-10-13T10:05:00Z'),
      task(1, '2026-10-13T10:30:00Z', 21),
    ])
    assert.deepEqual(keys(out['r2']), ['inbox-1', 'notice-n1', 'confirm-1', 'task-u21', 'inbox-2', 'task-u22'])
  })

  test('没有轮次时全部落在末尾，且仍按时间排；空输入得到空对象', () => {
    const out = placeTimeline([], [notice('b', '2026-10-13T10:02:00Z'), inbox(1, '2026-10-13T10:01:00Z')])
    assert.deepEqual(keys(out[TIMELINE_TAIL_KEY]), ['inbox-1', 'notice-b'])
    assert.deepEqual(placeTimeline(rounds, []), {})
  })
})

describe('confirmEntries / groupDecisions', () => {
  test('提问与计划各是一张卡；文档决策按文档合成一张汇总卡，落在最早一张出现的时间', () => {
    const items = [
      confirmItem(1, '2026-10-13T10:00:00Z'),
      confirmItem(2, '2026-10-13T10:10:00Z', { kind: 'plan' }),
      confirmItem(3, '2026-10-13T10:20:00Z', { kind: 'decision', doc_id: 9, doc_path: 'governance/auth.md', doc_title: '授权约定' }),
      confirmItem(4, '2026-10-13T10:15:00Z', { kind: 'decision', doc_id: 9, doc_path: 'governance/auth.md', doc_title: '授权约定', status: 'answered' }),
      confirmItem(5, '2026-10-13T10:30:00Z', { kind: 'decision', doc_id: 10, doc_path: 'design/x.md', doc_title: '设计' }),
    ]
    const entries = confirmEntries(items)
    assert.deepEqual(keys(entries.filter(e => e.kind === 'confirm')), ['confirm-1', 'confirm-2'])
    const groups = groupDecisions(items)
    assert.equal(groups.length, 2)
    assert.deepEqual(
      { path: groups[0].docPath, pending: groups[0].pending, answered: groups[0].answered, total: groups[0].total, at: groups[0].at },
      { path: 'governance/auth.md', pending: 1, answered: 1, total: 2, at: '2026-10-13T10:15:00Z' },
    )
    assert.equal(groups[1].docTitle, '设计')
    const decisions = entries.filter(e => e.kind === 'decisions')
    assert.deepEqual(decisions.map(e => e.at), ['2026-10-13T10:15:00Z', '2026-10-13T10:30:00Z'])
  })

  test('没有文档决策时不产生汇总卡', () => {
    assert.deepEqual(groupDecisions([confirmItem(1, '2026-10-13T10:00:00Z')]), [])
  })
})

describe('叠放：groupBlocks', () => {
  test('哪些能叠：任务进展、已处理的提问、都处理完的文档汇总；不能叠：话、待答复的提问、还有待决策项的文档汇总、告警、胶囊', () => {
    const docEntry = (status: ConfirmItem['status']) =>
      confirmEntries([confirmItem(5, '2026-10-13T10:00:00Z', { kind: 'decision', doc_id: 1, doc_path: 'a.md', doc_title: 'A', status })])[0]
    assert.equal(isStackable(docEntry('pending')), false, '还有待决策项的文档汇总卡是要您去处理的，不能藏进折叠')
    assert.equal(isStackable(docEntry('answered')), true, '都处理完的汇总卡只是留痕，可以叠')
    assert.equal(isStackable(task(1, '2026-10-13T10:00:00Z')), true)
    assert.equal(isStackable(confirm(1, '2026-10-13T10:00:00Z', 'answered')), true)
    assert.equal(isStackable(confirm(1, '2026-10-13T10:00:00Z', 'dismissed')), true)
    assert.equal(isStackable(confirm(1, '2026-10-13T10:00:00Z', 'pending')), false)
    assert.equal(isStackable(inbox(1, '2026-10-13T10:00:00Z')), false)
    assert.equal(isStackable(notice('n', '2026-10-13T10:00:00Z')), false)
    assert.equal(isStackable(capsule(1, '2026-10-13T10:00:00Z')), false)
    assert.equal(isStackable(guardEntries([{ preset_id: 'p', name: 'x', verdict: { state: 'crashed' } } as ConfirmBoardGuard])[0]), false)
  })

  test('连续两张以上的卡成一摞，单张不成摞', () => {
    const blocks = groupBlocks([
      task(1, '2026-10-13T10:01:00Z', 1),
      task(2, '2026-10-13T10:02:00Z', 2),
      task(3, '2026-10-13T10:03:00Z', 3),
    ])
    assert.equal(blocks.length, 1)
    assert.equal(blocks[0].type, 'stack')
    assert.deepEqual(keys((blocks[0] as { entries: TimelineEntry[] }).entries), ['task-u1', 'task-u2', 'task-u3'])
    assert.equal(blocks[0].key, 'stack-task-u1')

    const single = groupBlocks([task(1, '2026-10-13T10:01:00Z', 1)])
    assert.deepEqual(single.map(b => b.type), ['single'])
  })

  test('「交流」把前后的卡隔成不同的摞：话 → 摞 → 话 → 单张', () => {
    const blocks = groupBlocks([
      inbox(1, '2026-10-13T10:00:00Z'),
      task(1, '2026-10-13T10:01:00Z', 1),
      task(2, '2026-10-13T10:02:00Z', 2),
      notice('n', '2026-10-13T10:03:00Z'),
      task(3, '2026-10-13T10:04:00Z', 3),
    ])
    assert.deepEqual(blocks.map(b => b.type), ['single', 'stack', 'single', 'single'])
  })

  test('没答复的提问永远摊开：它把两边的卡隔开，自己是单独一块', () => {
    const blocks = groupBlocks([
      task(1, '2026-10-13T10:01:00Z', 1),
      task(2, '2026-10-13T10:02:00Z', 2),
      confirm(7, '2026-10-13T10:03:00Z', 'pending'),
      task(3, '2026-10-13T10:04:00Z', 3),
      confirm(8, '2026-10-13T10:05:00Z', 'answered'),
    ])
    assert.deepEqual(blocks.map(b => b.type), ['stack', 'single', 'stack'])
    assert.equal((blocks[1] as { entry: TimelineEntry }).entry.key, 'confirm-7')
  })

  test('空输入得到空数组', () => {
    assert.deepEqual(groupBlocks([]), [])
  })
})

describe('摞的汇总', () => {
  test('统计任务进展次数、涉及任务数、已处理的提问与文档汇总', () => {
    const decisions = confirmEntries([
      confirmItem(5, '2026-10-13T10:06:00Z', { kind: 'decision', doc_id: 1, doc_path: 'a.md', doc_title: 'A' }),
    ])
    const entries = [
      task(1, '2026-10-13T10:01:00Z', 1),
      task(1, '2026-10-13T10:02:00Z', 2),
      task(2, '2026-10-13T10:03:00Z', 3),
      confirm(7, '2026-10-13T10:04:00Z', 'answered'),
      ...decisions,
    ]
    const s = summarizeStack(entries)
    assert.deepEqual(
      { total: s.total, taskUpdates: s.taskUpdates, tasks: s.tasks, handled: s.handled, decisions: s.decisions },
      { total: 5, taskUpdates: 3, tasks: 2, handled: 1, decisions: 1 },
    )
    assert.equal(s.firstAt, '2026-10-13T10:01:00Z')
    assert.equal(s.lastAt, '2026-10-13T10:06:00Z')
    assert.equal(stackSummaryText(s), '任务进展 3 次（2 个任务） · 已处理的提问 1 条 · 文档决策 1 份')
  })

  test('只有一个任务时不写括号', () => {
    const s = summarizeStack([task(1, '2026-10-13T10:01:00Z', 1), task(1, '2026-10-13T10:02:00Z', 2)])
    assert.equal(stackSummaryText(s), '任务进展 2 次')
  })
})

describe('entryPreview：叠起来时只露出最新一张的一行预览', () => {
  test('各种卡片各写一行人话', () => {
    const t = '2026-10-13T10:00:00Z'
    const withText: TimelineEntry = { kind: 'task', key: 'task-u9', at: t, card: { key: 'task-u9', seq: 7, title: '做时间线', state: 'doing', verb: '进展 40%', text: '合并成一条流', at: t } }
    assert.equal(entryPreview(withText), '#7 做时间线 · 进展 40%：合并成一条流')
    assert.equal(entryPreview(task(3, t)), '#3 任务3 · 进展', '没有说明文字时不留冒号')
    assert.equal(entryPreview(confirm(12, t)), 'Q12 问题12')
    const [doc] = confirmEntries([confirmItem(5, t, { kind: 'decision', doc_id: 1, doc_path: 'a.md', doc_title: '方案 A' })])
    assert.equal(entryPreview(doc), '《方案 A》的决策卡')
    const [guard] = guardEntries([{ preset_id: 'p1', name: '主机一', verdict: { state: 'crashed' } } as ConfirmBoardGuard])
    assert.equal(entryPreview(guard), '主机一 · 守护告警')
    assert.equal(entryPreview({ kind: 'notice', key: 'n', at: t, session: { title: '第一批已上线' } as never }), '第一批已上线')
    assert.equal(entryPreview(notice('x', t)), '智能体进度', '通知没有标题时兜底')
    assert.equal(entryPreview(capsule(8, t)), '恢复胶囊 r8')
    assert.equal(entryPreview(inbox(1, t)), '您的追加')
  })
})

describe('同毫秒次序与后端共用一份夹具', () => {
  // 后端（internal/chat）的聊天消息流与这里的旧时间线必须在「同一毫秒谁先谁后」上一致，两边共用 internal/chat/testdata/tie_order.json。
  test('sortTimeline 的同刻次序等于夹具里对应来源的先后', () => {
    const t = '2026-10-13T10:00:00Z'
    const make: Record<string, TimelineEntry> = {
      inbox: inbox(1, t),
      notice: notice('s1', t),
      capsule: capsule(1, t),
      task: task(1, t),
      confirm: confirm(1, t),
      decisions: confirmEntries([confirmItem(2, t, { kind: 'decision', doc_id: 1, doc_path: 'a.md', doc_title: 'A' })])[0],
      guard: { ...guardEntries([{ preset_id: 'p1', name: '主机一', verdict: { state: 'crashed' } } as ConfirmBoardGuard])[0], at: t },
    }
    const names = Object.entries(tieFixture.frontend_kind) // [后端来源名, 前端种类]
    const expected = names
      .map(([server, kind]) => ({ kind, rank: tieFixture.order.indexOf(server) }))
      .sort((a, b) => a.rank - b.rank)
      .map(x => x.kind)
    // 倒序喂进去，排出来必须回到夹具规定的次序。
    const shuffled = [...expected].reverse().map(k => make[k])
    assert.deepEqual(sortTimeline(shuffled).map(e => e.kind), expected)
  })
})

describe('placeTimeline：夹在提问与回复之间（#118）', () => {
  const rs = [
    { session_id: 'r1', created_at: '2026-10-13T10:00:00Z', reply_at: '2026-10-13T10:10:00Z' },
    { session_id: 'r2', created_at: '2026-10-13T10:20:00Z', reply_at: undefined },
  ]
  test('提问之后、回复之前的条目放进那一轮（in:r1），而不是排到整张卡之后', () => {
    const out = placeTimeline(rs, [inbox(1, '2026-10-13T10:05:00Z'), inbox(2, '2026-10-13T10:15:00Z')])
    assert.deepEqual((out[inRoundKey('r1')] ?? []).map(e => e.key), ['inbox-1'])
    assert.deepEqual((out['r2'] ?? []).map(e => e.key), ['inbox-2'])
  })
  test('还没回复的最后一轮：之后的条目照旧放末尾', () => {
    const out = placeTimeline(rs, [inbox(3, '2026-10-13T10:30:00Z')])
    assert.deepEqual((out[TIMELINE_TAIL_KEY] ?? []).map(e => e.key), ['inbox-3'])
  })
  test('第一轮之前的条目不受影响', () => {
    const out = placeTimeline(rs, [inbox(4, '2026-10-13T09:00:00Z')])
    assert.deepEqual((out['r1'] ?? []).map(e => e.key), ['inbox-4'])
  })
})

describe('stackKeysHoldingConfirm：点名已处理的提问时要展开的摞', () => {
  test('找出收着这个编号的摞（可能分布在多个间隙）；单张的卡不在摞里；找不到返回空', () => {
    const blocks = {
      r1: groupBlocks([confirm(2, '2026-10-13T10:01:00Z', 'answered'), confirm(3, '2026-10-13T10:02:00Z', 'answered')]),
      r2: groupBlocks([confirm(4, '2026-10-13T11:01:00Z', 'answered')]),
      [TIMELINE_TAIL_KEY]: groupBlocks([task(1, '2026-10-13T12:00:00Z'), confirm(5, '2026-10-13T12:01:00Z', 'answered')]),
    }
    assert.deepEqual(stackKeysHoldingConfirm(blocks, 'Q2'), ['stack-confirm-2'])
    assert.deepEqual(stackKeysHoldingConfirm(blocks, 'Q3'), ['stack-confirm-2'])
    assert.deepEqual(stackKeysHoldingConfirm(blocks, 'Q4'), [], '单张的卡是 single 块，不在摞里')
    assert.deepEqual(stackKeysHoldingConfirm(blocks, 'Q5'), ['stack-task-u1'], '任务进展与已处理的提问连成一摞')
    assert.deepEqual(stackKeysHoldingConfirm(blocks, 'Q99'), [])
    assert.deepEqual(stackKeysHoldingConfirm({}, 'Q2'), [])
  })
})

describe('blockHoldsConfirm：这一块里有没有某个确认项（末尾渐进渲染要把它所在的块露出来，#169 第三期）', () => {
  test('单条的确认卡、摞里的确认卡都认；别的编号、别的种类的块不认', () => {
    const single = groupBlocks([confirm(7, '2026-10-13T12:00:00Z', 'pending')])[0]
    const stack = groupBlocks([confirm(2, '2026-10-13T10:01:00Z', 'answered'), confirm(3, '2026-10-13T10:02:00Z', 'answered')])[0]
    const other = groupBlocks([notice('a', '2026-10-13T09:00:00Z')])[0]
    assert.equal(single.type, 'single')
    assert.equal(stack.type, 'stack')
    assert.equal(blockHoldsConfirm(single, 'Q7'), true)
    assert.equal(blockHoldsConfirm(single, 'Q8'), false)
    assert.equal(blockHoldsConfirm(stack, 'Q2'), true)
    assert.equal(blockHoldsConfirm(stack, 'Q3'), true)
    assert.equal(blockHoldsConfirm(stack, 'Q9'), false)
    assert.equal(blockHoldsConfirm(other, 'Q7'), false)
  })
})
