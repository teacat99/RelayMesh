// 运行：pnpm --dir frontend run test:unit
// 预览视图的消息搜索（#120 阶段 E-c1）的纯逻辑：命中认到哪张卡、怎么显示。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { ChatMessage } from '../api/conversations'
import {
  SEARCH_KINDS,
  findEntryBlock,
  hitKindLabel,
  hitSnippet,
  hitWhen,
  nearestRoundBefore,
  resolveHit,
  roundIdOfGap,
  windowToShow,
  type HitContext,
} from './previewSearch'
import type { TimelineBlock, TimelineEntry } from './timeline'

const SENT = '2026-10-17T10:00:00.000Z'
const hit = (type: string, id: string): Pick<ChatMessage, 'source' | 'sent_at'> => ({ source: { type, id }, sent_at: SENT })
const ctx = (over: Partial<HitContext> = {}): HitContext => ({
  roundIds: new Set(['sess-aaaa1111', 'sess-bbbb2222']),
  entryKeys: new Set(['notice-sess-n0000001', 'inbox-12', 'task-u345', 'confirm-7', 'decisions-doc-9']),
  confirms: new Map([['70', { seq: 7, ref: 'Q7' }]]),
  ...over,
})

describe('resolveHit：命中 → 落点（目标真实存在才选，否则往下落，最后兜底按时间）', () => {
  it('汇报 / 回复 / 结束语（来源是会话）→ 那一轮', () => {
    assert.deepEqual(resolveHit(hit('feedback_session', 'sess-aaaa1111'), ctx()), { type: 'round', sessionId: 'sess-aaaa1111' })
  })
  it('来源是会话但它不是一轮（进度通知）→ 通知条目', () => {
    assert.deepEqual(resolveHit(hit('feedback_session', 'sess-n0000001'), ctx()), { type: 'entry', key: 'notice-sess-n0000001' })
  })
  it('会话既不是轮次也不是通知（已被删除）→ 按时间兜底', () => {
    const r = resolveHit(hit('feedback_session', 'sess-gone0000'), ctx())
    assert.equal(r.type, 'time')
    assert.equal((r as { ms: number }).ms, Date.parse(SENT))
  })
  it('您的追加 → inbox 条目；条目已不在列表里 → 按时间', () => {
    assert.deepEqual(resolveHit(hit('queued_feedback', '12'), ctx()), { type: 'entry', key: 'inbox-12' })
    assert.equal(resolveHit(hit('queued_feedback', '13'), ctx()).type, 'time')
  })
  it('确认项 → 点名那张卡（按数据库编号认到序号与引用号）；列表里没有这张卡 → 按时间', () => {
    assert.deepEqual(resolveHit(hit('confirm_item', '70'), ctx()), { type: 'confirm', ref: 'Q7', key: 'confirm-7' })
    assert.equal(resolveHit(hit('confirm_item', '71'), ctx()).type, 'time')
    // 认得编号，但这张卡没进时间线（比如被收走了）
    assert.equal(resolveHit(hit('confirm_item', '70'), ctx({ entryKeys: new Set() })).type, 'time')
  })
  it('文档决策汇总（来源 doc:<文档编号>）→ 汇总卡；没有就按时间', () => {
    assert.deepEqual(resolveHit(hit('confirm_item', 'doc:9'), ctx()), { type: 'entry', key: 'decisions-doc-9' })
    assert.equal(resolveHit(hit('confirm_item', 'doc:10'), ctx()).type, 'time')
  })
  it('任务进展 → task 条目；没有就按时间', () => {
    assert.deepEqual(resolveHit(hit('work_item_update', '345'), ctx()), { type: 'entry', key: 'task-u345' })
    assert.equal(resolveHit(hit('work_item_update', '346'), ctx()).type, 'time')
  })
  it('事件 / 未知来源 / 缺来源 → 按时间', () => {
    assert.equal(resolveHit(hit('event', '77'), ctx()).type, 'time')
    assert.equal(resolveHit(hit('chat_message', '5'), ctx()).type, 'time')
    assert.equal(resolveHit({ source: undefined as unknown as ChatMessage['source'], sent_at: SENT }, ctx()).type, 'time')
  })
  it('来源编号是数字也能认（接口里是字符串，防御一下）', () => {
    assert.deepEqual(resolveHit({ source: { type: 'queued_feedback', id: 12 as unknown as string }, sent_at: SENT }, ctx()), { type: 'entry', key: 'inbox-12' })
  })
})

describe('findEntryBlock / roundIdOfGap / windowToShow / nearestRoundBefore', () => {
  const task = (id: number): TimelineEntry =>
    ({ kind: 'task', key: `task-u${id}`, at: '2026-10-17T00:00:00Z', card: {} as never })
  const single = (e: TimelineEntry): TimelineBlock => ({ type: 'single', key: e.key, entry: e })
  const blocks: Record<string, TimelineBlock[]> = {
    'sess-bbbb2222': [single(task(1)), { type: 'stack', key: 'stack-task-u2', entries: [task(2), task(3)] }],
    __tail__: [single(task(4))],
  }

  it('findEntryBlock：单块、摞里的条目（返回那一摞）、末尾、不存在', () => {
    assert.equal(findEntryBlock(blocks, 'task-u1')?.block.key, 'task-u1')
    const inStack = findEntryBlock(blocks, 'task-u3')
    assert.equal(inStack?.gapKey, 'sess-bbbb2222')
    assert.equal(inStack?.block.key, 'stack-task-u2')
    assert.equal(findEntryBlock(blocks, 'task-u4')?.gapKey, '__tail__')
    assert.equal(findEntryBlock(blocks, 'task-u99'), null)
    assert.equal(findEntryBlock({}, 'task-u1'), null)
  })

  it('roundIdOfGap：轮次之间的键就是后一轮；轮内取 in: 之后；末尾没有', () => {
    assert.equal(roundIdOfGap('sess-bbbb2222'), 'sess-bbbb2222')
    assert.equal(roundIdOfGap('in:sess-aaaa1111'), 'sess-aaaa1111')
    assert.equal(roundIdOfGap('__tail__'), null)
  })

  it('windowToShow：已经在窗口里不动；不在就撑到够并多留 2 轮；封顶总轮数；找不到（idx<0）不动', () => {
    assert.equal(windowToShow(100, 95, 10), 10) // 倒数第 5 轮，窗口 10 够
    assert.equal(windowToShow(100, 50, 10), 52) // 需要 50 轮 + 2
    assert.equal(windowToShow(100, 0, 10), 100) // 需要 100 + 2，封顶 100
    assert.equal(windowToShow(100, -1, 10), 10)
    assert.equal(windowToShow(100, 50, 10, 0), 50)
  })

  it('nearestRoundBefore：不晚于它的最近一轮；比所有轮次都早就落第一轮；时间坏了的跳过；没有轮次返回 null', () => {
    const rounds = [
      { session_id: 'r1', created_at: '2026-10-15T10:00:00Z' },
      { session_id: 'r3', created_at: '2026-10-17T10:00:00Z' },
      { session_id: 'r2', created_at: '2026-10-16T10:00:00Z' }, // 顺序打乱也不影响
      { session_id: 'bad', created_at: 'not-a-time' },
    ]
    assert.equal(nearestRoundBefore(rounds, Date.parse('2026-10-16T12:00:00Z')), 'r2')
    assert.equal(nearestRoundBefore(rounds, Date.parse('2026-10-17T10:00:00Z')), 'r3') // 恰好同一刻算「不晚于」
    assert.equal(nearestRoundBefore(rounds, Date.parse('2026-10-20T00:00:00Z')), 'r3')
    assert.equal(nearestRoundBefore(rounds, Date.parse('2026-10-01T00:00:00Z')), 'r1')
    assert.equal(nearestRoundBefore([], 0), null)
    assert.equal(nearestRoundBefore([{ session_id: 'bad', created_at: 'x' }], 0), null)
  })
})

describe('hitKindLabel / hitWhen / SEARCH_KINDS', () => {
  const m = (type: string, kind: string, sender: 'agent' | 'human' | 'system'): Pick<ChatMessage, 'kind' | 'sender' | 'source'> => ({
    kind: kind as ChatMessage['kind'],
    sender: { kind: sender, name: 'x' },
    source: { type, id: '1' },
  })
  it('标签：回复 / 进度 / 汇报 / 追加 / 提问 / 计划 / 决策 / 答复 / 任务 / 事件', () => {
    assert.equal(hitKindLabel(m('feedback_session', 'text', 'human')), '回复')
    assert.equal(hitKindLabel(m('feedback_session', 'progress', 'agent')), '进度')
    assert.equal(hitKindLabel(m('feedback_session', 'text', 'agent')), '汇报')
    assert.equal(hitKindLabel(m('feedback_session', 'block', 'agent')), '汇报')
    assert.equal(hitKindLabel(m('queued_feedback', 'text', 'human')), '追加')
    assert.equal(hitKindLabel(m('confirm_item', 'ask', 'agent')), '提问')
    assert.equal(hitKindLabel(m('confirm_item', 'plan', 'agent')), '计划')
    assert.equal(hitKindLabel(m('confirm_item', 'decision', 'agent')), '决策')
    assert.equal(hitKindLabel(m('confirm_item', 'text', 'human')), '答复')
    assert.equal(hitKindLabel(m('work_item_update', 'progress', 'agent')), '任务')
    assert.equal(hitKindLabel(m('event', 'system', 'system')), '事件')
    assert.equal(hitKindLabel({ kind: 'text', sender: { kind: 'agent', name: 'x' }, source: undefined as unknown as ChatMessage['source'] }), '事件')
  })

  it('群聊消息（chat_message）：小标签写「谁说的」——您自己「您」（与消息卡一致）、成员写名字（空名字写「成员」）', () => {
    const g = (sender: 'agent' | 'human' | 'system', name: string) => ({
      kind: 'text' as ChatMessage['kind'],
      sender: { kind: sender, name },
      source: { type: 'chat_message', id: '7' },
    })
    assert.equal(hitKindLabel(g('human', '你')), '您')
    assert.equal(hitKindLabel(g('agent', 'RelayMesh-主线')), 'RelayMesh-主线')
    assert.equal(hitKindLabel(g('agent', '  ')), '成员')
    // 不会被工作流会话那套标签（汇报 / 提问 / 事件…）误标：来源不同，标签不同
    assert.notEqual(hitKindLabel(g('agent', 'x')), '事件')
  })

  it('时间：今天 / 昨天 / 日期 + 时分；坏时间返回空串', () => {
    const now = new Date(2026, 9, 18, 12, 0)
    const at = (d: number, h: number, mi: number) => ({ sent_at: new Date(2026, 9, d, h, mi).toISOString() })
    assert.equal(hitWhen(at(18, 9, 5), now), '今天 09:05')
    assert.equal(hitWhen(at(17, 23, 30), now), '昨天 23:30')
    assert.equal(hitWhen(at(10, 8, 0), now), '10月10日 08:00')
    assert.equal(hitWhen({ sent_at: 'nope' }, now), '')
  })

  it('搜索的消息种类：不含系统行', () => {
    assert.ok(!(SEARCH_KINDS as readonly string[]).includes('system'))
    assert.ok((SEARCH_KINDS as readonly string[]).includes('text'))
    assert.ok((SEARCH_KINDS as readonly string[]).includes('progress'))
  })
})

describe('hitSnippet：命中的那一小段，关键词处标出', () => {
  const join = (parts: { text: string }[]) => parts.map(p => p.text).join('')
  const hitText = (parts: { text: string; hit: boolean }[]) => parts.filter(p => p.hit).map(p => p.text)

  it('关键词在正文里：标出命中那一段，两侧保留上下文', () => {
    const parts = hitSnippet({ text: '今天把导出做完了，缓存失效策略还在评审中。', title: '进度' }, '缓存失效')
    assert.deepEqual(hitText(parts), ['缓存失效'])
    assert.ok(join(parts).includes('导出做完了'))
    assert.ok(join(parts).includes('还在评审中'))
  })

  it('不分大小写，标出的是原文里的写法', () => {
    const parts = hitSnippet({ text: 'Deploy finished: DEPLOY ok' }, 'deploy')
    assert.deepEqual(hitText(parts), ['Deploy'])
  })

  it('关键词按字面处理，不当正则', () => {
    const parts = hitSnippet({ text: '匹配 a.b* 这个写法，不是正则' }, 'a.b*')
    assert.deepEqual(hitText(parts), ['a.b*'])
    assert.deepEqual(hitText(hitSnippet({ text: 'axb' }, 'a.b')), []) // 点不是通配
  })

  it('正文没有、标题里有：标题里标出来', () => {
    const parts = hitSnippet({ title: '日志级别', text: '无关的正文' }, '日志')
    assert.deepEqual(hitText(parts), ['日志'])
    assert.equal(join(parts).startsWith('日志级别'), true)
  })

  it('两侧超长会截断并加省略号；命中靠近开头时左边没有省略号', () => {
    const long = '前'.repeat(80) + '目标词' + '后'.repeat(80)
    const parts = hitSnippet({ text: long }, '目标词')
    assert.ok(parts[0].text.startsWith('…'))
    assert.ok(parts[parts.length - 1].text.endsWith('…'))
    assert.ok(join(parts).length < long.length)
    const near = hitSnippet({ text: '目标词' + '后'.repeat(80) }, '目标词')
    assert.equal(near[0].hit, true) // 第一段就是命中，没有左省略
  })

  it('Markdown：artifact 记号去掉，链接 / 图片语法留文字，两侧的强调与代码标记去掉，命中那段保持原样', () => {
    const text = '看板 [[artifact:12]] 在 **这里**，详见 [设计稿](https://x.test/a) 与 `代码` 目标词 结束'
    const parts = hitSnippet({ text }, '目标词')
    const all = join(parts)
    assert.ok(!all.includes('[[artifact'))
    assert.ok(!all.includes('https://x.test'))
    assert.ok(!all.includes('**'))
    assert.ok(all.includes('设计稿'))
    assert.deepEqual(hitText(parts), ['目标词'])
    // 关键词本身带标记时按原样标出
    assert.deepEqual(hitText(hitSnippet({ text: '改了 `Q1` 的写法' }, '`Q1`')), ['`Q1`'])
  })

  it('没有命中位置（关键词只出现在链接地址里 / 空关键词）：取正文开头，不标', () => {
    const parts = hitSnippet({ text: '见 [文档](https://example.test/needle)' }, 'needle')
    assert.deepEqual(hitText(parts), [])
    assert.ok(join(parts).startsWith('见 文档'))
    assert.deepEqual(hitText(hitSnippet({ text: 'abc' }, '   ')), [])
  })

  it('正文与标题都是空：返回空数组；很长的开头截断', () => {
    assert.deepEqual(hitSnippet({}, 'x'), [])
    const parts = hitSnippet({ text: 'x'.repeat(300) }, 'zzz')
    assert.equal(parts.length, 1)
    assert.ok(parts[0].text.endsWith('…'))
    assert.ok(parts[0].text.length <= 82)
  })

  it('转小写会改变长度的字符（İ）：放弃精确标注，不抛错、不错位', () => {
    const parts = hitSnippet({ text: 'İstanbul 目标词' }, '目标词')
    assert.ok(join(parts).includes('目标词'))
  })
})
