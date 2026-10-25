import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { BranchFeedItem } from '../api/branches'
import type { ConfirmItem } from '../api/confirms'
import {
  TIMELINE_TAIL_KEY,
  blockHoldsConfirm,
  branchEntries,
  inRoundKey,
  confirmEntries,
  entryPreview,
  groupBlocks,
  groupDecisions,
  isStackable,
  placeTimeline,
  reuseEntries,
  sameTimelineEntry,
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

  test('无法解析的时间当作最早', () => {
    const out = sortTimeline([notice('late', '2026-10-13T23:00:00Z'), inbox(1, 'not-a-date')])
    assert.deepEqual(keys(out), ['inbox-1', 'notice-late'])
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

// ---- #186 C2a：placeTimeline 从「逐条从头 find、每比一轮解析一次时间」改成「轮次时间只解析一次 + 指针只进不退」----
// 下面是改写之前的原实现（逐字照搬），用来做随机对照：不管轮次是否有序、有没有重复时刻、时间解析不了，结果都必须与它逐一相同。
function placeTimelineReference(
  roundList: ReadonlyArray<{ session_id: string; created_at: string; reply_at?: string }>,
  entries: ReadonlyArray<TimelineEntry>,
): Record<string, TimelineEntry[]> {
  const parse = (iso: string) => {
    const t = Date.parse(iso)
    return Number.isFinite(t) ? t : 0
  }
  const out: Record<string, TimelineEntry[]> = {}
  if (entries.length === 0) return out
  for (const e of sortTimeline(entries)) {
    const t = Number.isFinite(Date.parse(e.at)) ? Date.parse(e.at) : 0
    const next = roundList.find(r => parse(r.created_at) > t)
    const prev = next ? roundList[roundList.indexOf(next) - 1] : roundList[roundList.length - 1]
    if (prev && prev.reply_at && t >= parse(prev.created_at) && t < parse(prev.reply_at)) {
      ;(out[inRoundKey(prev.session_id)] ||= []).push(e)
      continue
    }
    ;(out[next ? next.session_id : TIMELINE_TAIL_KEY] ||= []).push(e)
  }
  return out
}

/** 带种子的伪随机（mulberry32）：失败时能用同一个种子复现。 */
function seeded(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

describe('placeTimeline：与改写前的逐条查找逐一相同（#186 C2a 随机对照）', () => {
  const BASE = Date.parse('2026-10-13T00:00:00Z')
  const stamp = (rand: () => number, spanMin: number) => {
    const roll = rand()
    if (roll < 0.04) return 'not-a-date' // 解析不了
    if (roll < 0.06) return ''
    return new Date(BASE + Math.floor(rand() * spanMin) * 60_000).toISOString() // 分钟粒度：故意制造大量「同一时刻」
  }

  test('随机轮次（可乱序、可重复时刻、可缺回复）× 随机条目（含时间解析不了的）：落位与原实现完全一致', () => {
    for (let seed = 1; seed <= 400; seed++) {
      const rand = seeded(seed)
      const roundCount = Math.floor(rand() * 26)
      const roundList = Array.from({ length: roundCount }, (_, i) => ({
        session_id: `r${i}`,
        created_at: stamp(rand, 120),
        ...(rand() < 0.6 ? { reply_at: stamp(rand, 160) } : {}),
      }))
      if (rand() < 0.5) roundList.sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || 0) // 一半的用例按时间升序（真实情形）
      const makers: Array<(i: number, at: string) => TimelineEntry> = [
        (i, at) => inbox(i, at),
        (i, at) => notice(`n${i}`, at),
        (i, at) => task(i, at, i),
        (i, at) => confirm(i, at, i % 2 ? 'answered' : 'pending'),
        (i, at) => capsule(i, at),
      ]
      const entryList: TimelineEntry[] = Array.from({ length: Math.floor(rand() * 45) }, (_, i) =>
        makers[Math.floor(rand() * makers.length)](i, stamp(rand, 160)),
      )
      const want = placeTimelineReference(roundList, entryList)
      const got = placeTimeline(roundList, entryList)
      assert.deepEqual(got, want, `种子 ${seed}：落位与原实现不同`)
      assert.deepEqual(Object.keys(got).sort(), Object.keys(want).sort(), `种子 ${seed}：键集合不同`)
    }
  })

  test('性能形状：解析时间的次数随「条目 + 轮次」线性增长，而不是「条目 × 轮次」', () => {
    const E = 300
    const R = 150
    const roundList = Array.from({ length: R }, (_, i) => ({
      session_id: `r${i}`,
      created_at: new Date(BASE + i * 600_000).toISOString(),
      reply_at: new Date(BASE + i * 600_000 + 300_000).toISOString(),
    }))
    const entryList = Array.from({ length: E }, (_, i) => notice(`n${i}`, new Date(BASE + i * 290_000 + 7_000).toISOString()))
    const realParse = Date.parse
    let calls = 0
    Date.parse = (s: string) => {
      calls++
      return realParse(s)
    }
    try {
      placeTimeline(roundList, entryList)
    } finally {
      Date.parse = realParse
    }
    // 轮次 R 次 + 排序里每条一次 + 落位里每条一次 + 最多每条一次回复时刻；原实现在这个形状下是几万次
    assert.ok(calls <= R + 3 * E, `解析了 ${calls} 次，应不超过 ${R + 3 * E}`)
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

// ---- #186 C2a：条目对象复用——没变的条目换回上一次的对象，卡片属性不变，几百张通知卡不会因为别处有动静就整个重渲染 ----
describe('reuseEntries / sameTimelineEntry', () => {
  const row = (id: string) => ({ session_id: id }) as never
  const card = (seq: number, state = 'doing') => ({ key: `task-u${seq}`, seq, title: `任务${seq}`, state, verb: '进展', text: '', at: '2026-10-13T10:00:00Z' })
  const taskEntry = (seq: number, state = 'doing'): TimelineEntry => ({ kind: 'task', key: `task-u${seq}`, at: '2026-10-13T10:00:00Z', card: card(seq, state) })
  const noticeOf = (r: ReturnType<typeof row>, at = '2026-10-13T10:01:00Z'): TimelineEntry => ({ kind: 'notice', key: `notice-${(r as { session_id: string }).session_id}`, at, session: r })
  const start = () => ({ byKey: new Map<string, TimelineEntry>(), list: [] as TimelineEntry[] })

  test('第一次：原样返回（没有上一次可复用）', () => {
    const a = noticeOf(row('a'))
    const out = reuseEntries(start(), [a])
    assert.equal(out.list[0], a)
    assert.equal(out.byKey.get('notice-a'), a)
  })

  test('什么都没变：数组与映射都换回上一次的那一份（下游整条链不会重算）', () => {
    const ra = row('a')
    const rb = row('b')
    const first = reuseEntries(start(), [noticeOf(ra), noticeOf(rb), taskEntry(1)])
    const again = reuseEntries(first, [noticeOf(ra), noticeOf(rb), taskEntry(1)]) // 全是新拼的包装对象，内容与上次相同
    assert.equal(again.list, first.list, '数组本身换回上一次的')
    assert.equal(again.byKey, first.byKey)
    assert.deepEqual(again.list.map(e => e.key), ['notice-a', 'notice-b', 'task-u1'])
  })

  test('只有一行换成了新对象：只有它是新的，其余条目沿用上一次的对象，数组是新的', () => {
    const ra = row('a')
    const rb = row('b')
    const first = reuseEntries(start(), [noticeOf(ra), noticeOf(rb)])
    const rb2 = row('b') // 重新取数后同一个会话号的新对象
    const second = reuseEntries(first, [noticeOf(ra), noticeOf(rb2)])
    assert.notEqual(second.list, first.list)
    assert.equal(second.list[0], first.list[0], '没变的沿用')
    assert.notEqual(second.list[1], first.list[1])
    assert.equal((second.list[1] as { session: unknown }).session, rb2, '变了的用新数据，不能把旧内容留在页面上')
  })

  test('时间变了、种类相同键相同：算不同；新增与删除各自处理；顺序变了数组变但对象沿用', () => {
    const ra = row('a')
    const rb = row('b')
    const first = reuseEntries(start(), [noticeOf(ra), noticeOf(rb)])
    const moved = reuseEntries(first, [noticeOf(rb), noticeOf(ra)])
    assert.notEqual(moved.list, first.list, '顺序变了，数组是新的')
    assert.equal(moved.list[0], first.list[1])
    assert.equal(moved.list[1], first.list[0])
    const later = reuseEntries(first, [noticeOf(ra, '2026-10-13T11:00:00Z'), noticeOf(rb)])
    assert.notEqual(later.list[0], first.list[0], 'at 变了就是新条目')
    assert.equal(later.list[1], first.list[1])
    const removed = reuseEntries(first, [noticeOf(ra)])
    assert.equal(removed.list.length, 1)
    assert.equal(removed.list[0], first.list[0])
    assert.equal(removed.byKey.has('notice-b'), false)
    const added = reuseEntries(first, [noticeOf(ra), noticeOf(rb), noticeOf(row('c'))])
    assert.equal(added.list.length, 3)
    assert.equal(added.list[0], first.list[0])
    assert.equal(added.list[1], first.list[1])
  })

  test('任务进展卡每次重算都是新对象：字段全相同就沿用，哪个字段变了就算新的', () => {
    const first = reuseEntries(start(), [taskEntry(7)])
    assert.equal(reuseEntries(first, [taskEntry(7)]).list[0], first.list[0])
    const changed = reuseEntries(first, [taskEntry(7, 'done')])
    assert.notEqual(changed.list[0], first.list[0])
    assert.equal((changed.list[0] as { card: { state: string } }).card.state, 'done')
  })

  test('各种类的「同一个数据对象」判定：收件箱 / 确认项 / 胶囊按对象本身，不同对象即使内容一样也算不同', () => {
    const q = { id: 1 } as never
    const item = confirmItem(3, '2026-10-13T10:00:00Z')
    const cap = { revision: 2 } as never
    const make = (): TimelineEntry[] => [
      { kind: 'inbox', key: 'inbox-1', at: 't1', q },
      { kind: 'confirm', key: 'confirm-3', at: 't2', item },
      { kind: 'capsule', key: 'capsule-2', at: 't4', capsule: cap },
    ]
    const first = reuseEntries(start(), make())
    const same = reuseEntries(first, make())
    assert.equal(same.list, first.list)
    const other = reuseEntries(first, [
      { kind: 'inbox', key: 'inbox-1', at: 't1', q: { id: 1 } as never },
      ...make().slice(1),
    ])
    assert.notEqual(other.list[0], first.list[0], '换了一个内容相同的新对象：宁可多渲染一次')
    assert.equal(other.list[1], first.list[1])
    assert.ok(sameTimelineEntry(first.list[1], first.list[1]))
    assert.ok(!sameTimelineEntry(first.list[1], first.list[2]), '种类不同')
  })

  test('随机对照：复用只改变对象的身份，不改变内容（逐项 deepEqual）', () => {
    const rand = seeded(7)
    const pool = Array.from({ length: 12 }, (_, i) => row(`r${i}`))
    let prev = start()
    for (let step = 0; step < 200; step++) {
      const next: TimelineEntry[] = []
      for (const r of pool) if (rand() < 0.7) next.push(noticeOf(rand() < 0.15 ? row((r as { session_id: string }).session_id) : r, rand() < 0.1 ? `2026-10-13T1${Math.floor(rand() * 9)}:00:00Z` : '2026-10-13T10:01:00Z'))
      if (rand() < 0.5) next.push(taskEntry(Math.floor(rand() * 3), rand() < 0.5 ? 'doing' : 'done'))
      if (rand() < 0.3) next.reverse()
      const out = reuseEntries(prev, next)
      assert.deepEqual(out.list, next, `第 ${step} 步：内容必须与新拼的逐项相同`)
      assert.equal(out.byKey.size, new Set(next.map(e => e.key)).size)
      prev = out
    }
  })

  test('结构守卫：预览列表的 timelineEntries 经过 reuseEntries，条目不再每次重算都换一批新对象', () => {
    const src = readFileSync(resolve(process.cwd(), 'src/components/feedback/preview-view/PreviewMessageList.vue'), 'utf8')
    assert.match(src, /entryMemo = reuseEntries\(entryMemo, entries\)\s*\n\s*return entryMemo\.list as TimelineEntry\[\]/)
  })
})

// ---- 分支会话（#236）：主会话 ↔ 分支的消息与建立 / 结束事件，是时间线上的一种条目 ----
describe('分支会话条目 branch', () => {
  const msg = (id: number, at: string, text = '话', over: Partial<BranchFeedItem> = {}): BranchFeedItem =>
    ({ kind: 'message', id, at, actor: { kind: 'branch', workflow: 'wf-b' }, text, state: 'queued', ...over })
  const event = (at: string, ev: 'created' | 'closed' = 'created'): BranchFeedItem =>
    ({ kind: 'event', at, actor: { kind: 'system' }, event: ev, branch: 'wf-b' })

  test('branchEntries：消息按行号起键，事件按（种类，分支）起键，时间取条目自己的时间', () => {
    const out = branchEntries([event('2026-10-24T01:00:00Z'), msg(5, '2026-10-24T02:00:00Z'), event('2026-10-24T03:00:00Z', 'closed')])
    assert.deepEqual(keys(out), ['branch-e-created-wf-b', 'branch-m5', 'branch-e-closed-wf-b'])
    assert.deepEqual(out.map(e => e.at), ['2026-10-24T01:00:00Z', '2026-10-24T02:00:00Z', '2026-10-24T03:00:00Z'])
    assert.deepEqual(out.map(e => e.kind), ['branch', 'branch', 'branch'])
    assert.deepEqual(branchEntries([]), [])
  })

  test('永远不叠进摞里：相邻的分支消息 / 事件各自单独一块', () => {
    const entries = branchEntries([msg(1, '2026-10-24T01:00:00Z'), msg(2, '2026-10-24T01:01:00Z'), event('2026-10-24T01:02:00Z', 'closed')])
    for (const e of entries) assert.equal(isStackable(e), false)
    const blocks = groupBlocks(entries)
    assert.deepEqual(blocks.map(b => b.type), ['single', 'single', 'single'])
    // 夹在可叠的卡片之间时，把前后的卡片隔成不同的摞（和「话」同理）
    const mixed = groupBlocks([task(1, '2026-10-24T01:00:00Z'), task(2, '2026-10-24T01:01:00Z'), ...branchEntries([msg(9, '2026-10-24T01:02:00Z')]), task(3, '2026-10-24T01:03:00Z'), task(4, '2026-10-24T01:04:00Z')])
    assert.deepEqual(mixed.map(b => b.type), ['stack', 'single', 'stack'])
  })

  test('同一时刻：和进度通知同级（您的话之后、胶囊与卡片之前），同级按传入次序', () => {
    const t = '2026-10-24T05:00:00Z'
    const [b] = branchEntries([msg(1, t)])
    const out = sortTimeline([capsule(1, t), confirm(1, t), notice('n', t), b, inbox(1, t)])
    assert.deepEqual(keys(out), ['inbox-1', 'notice-n', 'branch-m1', 'capsule-1', 'confirm-1'])
  })

  test('落位：按时间落在两轮之间，和别的条目一起排成一条流', () => {
    const [b] = branchEntries([msg(1, '2026-10-13T10:30:00Z')])
    const placed = placeTimeline(rounds, [b, notice('n', '2026-10-13T11:30:00Z')])
    assert.deepEqual(Object.fromEntries(Object.entries(placed).map(([k, v]) => [k, keys(v)])), {
      r2: ['branch-m1'],
      [TIMELINE_TAIL_KEY]: ['notice-n'],
    })
  })

  test('条目复用：同一个数据对象沿用上一次的条目，换了对象（送达状态翻转）才算新的', () => {
    const item = msg(1, '2026-10-24T01:00:00Z')
    const first = reuseEntries({ byKey: new Map(), list: [] }, branchEntries([item]))
    const same = reuseEntries(first, branchEntries([item]))
    assert.equal(same.list, first.list, '数据对象没换：数组都沿用')
    const flipped = reuseEntries(first, branchEntries([{ ...item, state: 'delivered' }]))
    assert.notEqual(flipped.list[0], first.list[0])
    assert.equal((flipped.list[0] as { item: BranchFeedItem }).item.state, 'delivered', '变了的用新数据')
  })

  test('预览文案：事件写建立 / 结束，发出去的话带箭头', () => {
    const [created, said, closed] = branchEntries([event('t'), msg(2, 't', '收到', { outgoing: true, to: 'wf-a' }), event('t', 'closed')])
    assert.equal(entryPreview(created), '分支已建立')
    assert.equal(entryPreview(said), '→ 收到')
    assert.equal(entryPreview(closed), '分支已结束')
  })
})
