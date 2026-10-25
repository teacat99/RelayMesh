// 运行：pnpm --dir frontend run test:unit
// 预览视图「右侧轨」的纯逻辑（#120 阶段 2；#157 / 追加 #223、#225 改版）：样式表（只画非用户消息的类型）、条目映射、
// 固定间距的排布（保序回归）、可视窗口的分段线性映射、轨的跟随滚动、跳转目标。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { ConfirmItem } from '../api/confirms'
import { senderIdentity } from './groupCard'
import { CARD_TONE_CLASS, CARD_TYPES } from './messageCard'
import type { TimelineBlock, TimelineEntry } from './timeline'
import {
  RAIL_DIM_OPACITY,
  RAIL_KINDS,
  RAIL_LEN_LONG,
  RAIL_LEN_SHORT,
  RAIL_MIN_SCREENS,
  RAIL_MIN_TICKS,
  RAIL_PITCH_PX,
  followRailScroll,
  formatRailTime,
  isRailKind,
  isotonicFit,
  jumpScrollTop,
  layoutRail,
  parseRailItem,
  railDomAttrs,
  railLabel,
  railMapper,
  railMetaOfBlock,
  railMetaOfEntry,
  railMetaOfGroupMessage,
  railMetaOfReply,
  railMetaOfRound,
  railStyle,
  railViewportBox,
  shouldShowRail,
  type RailItem,
  type RailKind,
  type RailLayout,
  type RailState,
} from './timelineRail'

function item(id: string, kind: RailKind, top: number, state: RailState = 'done'): RailItem {
  return { id, kind, state, top, title: id, at: '' }
}

/** 您自己的话：不上轨。 */
const USER_KINDS: RailKind[] = ['reply', 'append']
/** 轨上画的格子（种类/状态）：非用户消息的类型 × 两个状态。 */
const DRAWN: [RailKind, RailState][] = RAIL_KINDS.filter(k => !USER_KINDS.includes(k)).flatMap(k => [[k, 'pending'] as [RailKind, RailState], [k, 'done'] as [RailKind, RailState]])

describe('右侧轨样式表：只画非用户消息的类型（追加 #225）', () => {
  it('画的格子恰好是「除您的回复 / 追加之外的十种 × 两个状态」', () => {
    const drawn: string[] = []
    for (const k of RAIL_KINDS) for (const st of ['pending', 'done'] as const) if (railStyle(k, st)) drawn.push(`${k}/${st}`)
    assert.equal(drawn.length, 20)
    assert.deepEqual(drawn.sort(), DRAWN.map(([k, st]) => `${k}/${st}`).sort())
  })

  it('不画的：您的确认回复与您的追加（您自己的话），不论什么状态', () => {
    for (const k of USER_KINDS) {
      assert.equal(railStyle(k, 'pending'), undefined, `${k}/pending`)
      assert.equal(railStyle(k, 'done'), undefined, `${k}/done`)
    }
  })

  it('画的每一格：长度、粗细、颜色（按卡片类型）、脉冲、变淡', () => {
    // 长度只有两档（追加 #248 / Q24，#160）：要您处理的 24px（长），其余 14px（短）
    // 颜色（追加 #265）一律按卡片类型：状态靠长度 / 粗细 / 脉冲 / 变淡表达
    const want: [RailKind, RailState, number, number, string, boolean, boolean][] = [
      ['plan', 'pending', 24, 4, 'red', true, false], // 计划确认（待决策）长·红·脉冲
      ['plan', 'done', 14, 2, 'red', false, true], // 已决策 短·红·变淡
      ['ask', 'pending', 24, 4, 'amber', false, false], // 智能体提问（待答复）长·黄
      ['ask', 'done', 14, 2, 'amber', false, true], // 已答复 短·黄·变淡
      ['decisions', 'pending', 24, 4, 'violet', false, false], // 文档决策（待决策）长·紫
      ['decisions', 'done', 14, 2, 'violet', false, true], // 已处理 短·紫·变淡
      ['round', 'pending', 24, 4, 'zinc', true, false], // AI 方案汇报轮次（等回复）长·黑灰·脉冲
      ['round', 'done', 14, 2, 'zinc', false, true], // 已处理的轮次 短·黑灰·变淡
      ['task', 'done', 14, 2, 'slate', false, false], // 任务进展 短·灰蓝
      ['stack', 'done', 14, 2, 'slate', false, false], // 卡片摞 短（按内含类型分段着色，由组件按 parts 画；没有 parts 时退回灰蓝）
      ['notice', 'done', 14, 2, 'sky', false, false], // 进度通知 短·蓝（追加 #265）
      ['capsule', 'done', 14, 2, 'slate', false, false], // 恢复胶囊 短·灰蓝
      ['branch', 'pending', 14, 2, 'zinc', false, false], // 分支消息（#236）短·黑灰；不分待处理 / 已处理
      ['branch', 'done', 14, 2, 'zinc', false, false],
      ['member', 'pending', 14, 2, 'zinc', false, false], // 群聊里别的成员的发言（#241）短·黑灰；群里的话不分待处理 / 已处理
      ['member', 'done', 14, 2, 'zinc', false, false],
    ]
    for (const [k, st, len, line, tone, pulse, dim] of want) {
      const s = railStyle(k, st)
      assert.ok(s, `${k}/${st}`)
      assert.deepEqual([s.len, s.line, s.tone, s.pulse, s.dim], [len, line, tone, pulse, dim], `${k}/${st}`)
    }
  })

  it('颜色按卡片类型（追加 #265）：每种刻度的色调就是它那一类卡片的色调，待处理与已处理同色，只差长度 / 粗细 / 变淡', () => {
    // 种类 → 卡片类型（lib/messageCard 的 CARD_TYPES）
    const cardOf: Partial<Record<RailKind, keyof typeof CARD_TYPES>> = {
      round: 'report', notice: 'notice', ask: 'ask', plan: 'plan', decisions: 'decision', task: 'task', capsule: 'capsule', stack: 'task', branch: 'member', member: 'member',
    }
    for (const [k, st] of DRAWN) {
      const type = cardOf[k]
      assert.ok(type, `${k} 要有对应的卡片类型`)
      assert.equal(railStyle(k, st)!.tone, CARD_TYPES[type].tone, `${k}/${st} 的色调应是 ${type} 卡片的 ${CARD_TYPES[type].tone}`)
    }
    for (const k of RAIL_KINDS) {
      const p = railStyle(k, 'pending')
      const d = railStyle(k, 'done')
      if (p && d) assert.equal(p.tone, d.tone, `${k} 待处理与已处理同色`)
    }
    // 蓝只属于进度通知（追加 #265：进度卡的蓝色标签在轨上也得是蓝的）
    assert.deepEqual(DRAWN.filter(([k, st]) => railStyle(k, st)!.tone === 'sky').map(([k]) => k), ['notice', 'notice'])
  })

  it('变淡只给「已处理」的那几种（有待处理 / 已处理之分的：计划、提问、文档决策、汇报）；要您处理的与不分状态的都不淡', () => {
    const dimmed = DRAWN.filter(([k, st]) => railStyle(k, st)!.dim).map(([k, st]) => `${k}/${st}`)
    assert.deepEqual(dimmed.sort(), ['ask/done', 'decisions/done', 'plan/done', 'round/done'])
    assert.ok(RAIL_DIM_OPACITY > 0.3 && RAIL_DIM_OPACITY < 1, '变淡后仍能认出颜色')
  })

  it('刻度长度只有两档（#160，Q24）：需要您交互的画长（24px），其余默认画短（14px）', () => {
    assert.equal(RAIL_LEN_LONG, 24)
    assert.equal(RAIL_LEN_SHORT, 14)
    // 需要您动手的格子：待处理的计划 / 提问 / 文档决策、等回复的汇报
    const needsYou = new Set<string>(['plan/pending', 'ask/pending', 'decisions/pending', 'round/pending'])
    const lens = new Set<number>()
    for (const [k, st] of DRAWN) {
      const s = railStyle(k, st)!
      lens.add(s.len)
      assert.equal(s.len, needsYou.has(`${k}/${st}`) ? RAIL_LEN_LONG : RAIL_LEN_SHORT, `${k}/${st}`)
    }
    assert.deepEqual([...lens].sort((a, b) => a - b), [RAIL_LEN_SHORT, RAIL_LEN_LONG], '全部刻度只有这两种长度')
    // 摞的三段（最多四段）要放得下：短一档里每段至少 2px（段间留 1px 缝）
    assert.ok((RAIL_LEN_SHORT - 3) / 4 >= 2)
  })

  it('不分「待处理 / 已处理」的种类（任务、摞、通知、胶囊、分支消息、成员发言）两个状态用同一格', () => {
    for (const k of ['task', 'stack', 'notice', 'capsule', 'branch', 'member'] as const) assert.deepEqual(railStyle(k, 'pending'), railStyle(k, 'done'), k)
  })

  it('刻度线粗细：还要您处理的 4px、其余 2px；与点击区的高同奇偶，才能上下居中、不被抗锯齿糊掉', () => {
    for (const [k, st] of DRAWN) {
      const s = railStyle(k, st)!
      // 不分「待处理 / 已处理」的种类两格同样粗（2px），其余都是「待处理 4px / 已处理 2px」
      assert.equal(s.line, st === 'pending' && !['task', 'stack', 'notice', 'capsule', 'branch', 'member'].includes(k) ? 4 : 2, `${k}/${st}`)
      assert.equal((RAIL_PITCH_PX - s.line) % 2, 0, `${k}/${st} 粗细与点击区高度同奇偶`)
      assert.ok(s.len > 0 && s.len <= 28, `${k}/${st} 长度在轨宽（32px）之内`)
      assert.ok(CARD_TONE_CLASS[s.tone], `${k}/${st} 色调 ${s.tone} 没有对应的 --card-tone 类`)
    }
  })

  it('脉冲只给最紧要的两种：待处理的计划、等回复的轮次', () => {
    const pulsing = DRAWN.filter(([k, st]) => railStyle(k, st)!.pulse).map(([k, st]) => `${k}/${st}`)
    assert.deepEqual(pulsing.sort(), ['plan/pending', 'round/pending'])
  })

  it('轨上用到的每种色调在卡片色调表里都有定义（亮 / 暗两套），且互不相同', () => {
    const used = new Set<string>()
    for (const [k, st] of DRAWN) used.add(railStyle(k, st)!.tone)
    assert.deepEqual([...used].sort(), ['amber', 'red', 'sky', 'slate', 'violet', 'zinc'])
    const classes = [...used].map(t => CARD_TONE_CLASS[t as keyof typeof CARD_TONE_CLASS])
    assert.equal(new Set(classes).size, classes.length)
    for (const c of classes) assert.match(c, /\[--card-tone:#[0-9a-f]{6}\] dark:\[--card-tone:#[0-9a-f]{6}\]/)
  })

  it('点击区常量：16px 高', () => {
    assert.equal(RAIL_PITCH_PX, 16)
    assert.equal(RAIL_MIN_TICKS, 2)
  })

  it('类型名与状态名（预览状态词汇表，阶段 D2）：要您动手的都叫「待您处理」，处理完的叫「已处理」，不分状态的只有类型名', () => {
    assert.equal(railLabel('plan', 'pending'), '计划确认 · 待您处理')
    assert.equal(railLabel('plan', 'done'), '计划确认 · 已处理')
    assert.equal(railLabel('ask', 'pending'), '智能体提问 · 待您处理')
    assert.equal(railLabel('ask', 'done'), '智能体提问 · 已处理')
    assert.equal(railLabel('decisions', 'pending'), '文档决策 · 待您处理')
    assert.equal(railLabel('decisions', 'done'), '文档决策 · 已处理')
    assert.equal(railLabel('round', 'pending'), 'AI 方案汇报 · 待您处理')
    assert.equal(railLabel('round', 'done'), 'AI 方案汇报 · 已处理')
    assert.equal(railLabel('reply', 'done'), '您的确认回复')
    assert.equal(railLabel('stack', 'done'), '卡片摞')
  })

  it('摞的提示写出内含类型的张数（#160）：卡片摞 · 任务 3 · 提问 1；别的种类不受 parts 影响', () => {
    const parts = [{ type: 'task', count: 3 }, { type: 'ask', count: 1 }] as const
    assert.equal(railLabel('stack', 'done', parts), '卡片摞 · 任务 3 · 提问 1')
    assert.equal(railLabel('stack', 'done', []), '卡片摞')
    assert.equal(railLabel('stack', 'done', [{ type: 'plan', count: 2 }, { type: 'decision', count: 1 }]), '卡片摞 · 计划 2 · 决策 1')
    assert.equal(railLabel('plan', 'pending', parts), '计划确认 · 待您处理')
  })

  it('isRailKind 只认十二种', () => {
    for (const k of RAIL_KINDS) assert.ok(isRailKind(k))
    assert.equal(RAIL_KINDS.length, 12)
    assert.ok(!isRailKind('confirm'))
    assert.ok(!isRailKind(''))
    assert.ok(!isRailKind(undefined))
    assert.ok(!isRailKind(3))
  })
})

// ---------- 条目映射 ----------

const AT = '2026-10-17T05:00:00Z'
const confirm = (over: Partial<ConfirmItem>): ConfirmItem => ({ id: 1, workflow_id: 'w', seq: 7, kind: 'ask', title: '要不要采用？', options: [], status: 'pending', ...over }) as unknown as ConfirmItem

const entries: Record<TimelineEntry['kind'], TimelineEntry> = {
  inbox: { kind: 'inbox', key: 'inbox-3', at: AT, q: { id: 3, response_text: '## 请调成 debug\n第二行' } as never },
  notice: { kind: 'notice', key: 'notice-s1', at: AT, session: { session_id: 's1', title: '进度 1' } as never },
  task: { kind: 'task', key: 'task-9', at: AT, card: { key: 'task-9', seq: 9, title: '预览视图任务', state: 'doing', verb: '进行中', text: '', at: AT } },
  confirm: { kind: 'confirm', key: 'confirm-7', at: AT, item: confirm({}) },
  decisions: { kind: 'decisions', key: 'decisions-doc-1', at: AT, group: { key: 'doc-1', docPath: 'a.md', docTitle: '设计稿', pending: 2, answered: 1, total: 3, at: AT } },
  capsule: { kind: 'capsule', key: 'capsule-3', at: AT, capsule: { revision: 3, status: 'active', created_at: AT, objective: '做完右侧轨\n其它', basis: {} } as never },
  branch: { kind: 'branch', key: 'branch-m5', at: AT, item: { kind: 'message', id: 5, at: AT, text: '接口已就绪', actor: { kind: 'branch', workflow: 'wf-b' }, state: 'queued' } },
}

describe('时间线条目 → 轨上条目', () => {
  it('七种时间线条目各有归属：每种 TimelineEntry 都映射到一个合法的种类，编号 = 条目的键、时间 = 条目的提交时间', () => {
    assert.equal(Object.keys(entries).length, 7)
    for (const e of Object.values(entries)) {
      const m = railMetaOfEntry(e)
      assert.ok(isRailKind(m.kind), e.kind)
      assert.equal(m.id, e.key)
      assert.equal(m.at, e.at)
      assert.ok(m.title.length > 0, `${e.kind} 标题不该为空`)
    }
  })

  it('追加 → append（标题取第一行，去掉标记）；没有文字时用「追加」', () => {
    const m = railMetaOfEntry(entries.inbox)
    assert.deepEqual([m.kind, m.state, m.title], ['append', 'done', '请调成 debug'])
    const empty = railMetaOfEntry({ ...entries.inbox, q: { id: 4, response_text: '' } as never } as TimelineEntry)
    assert.equal(empty.title, '追加')
  })

  it('通知 / 任务进展 / 胶囊的标题与回退', () => {
    assert.deepEqual([railMetaOfEntry(entries.notice).kind, railMetaOfEntry(entries.notice).title], ['notice', '进度 1'])
    assert.equal(railMetaOfEntry({ ...entries.notice, session: { session_id: 's2' } as never } as TimelineEntry).title, '进度通知')
    assert.deepEqual([railMetaOfEntry(entries.task).kind, railMetaOfEntry(entries.task).title], ['task', '预览视图任务'])
    assert.equal(railMetaOfEntry({ ...entries.task, card: { seq: 12, title: '' } as never } as TimelineEntry).title, '任务进展 #12')
    assert.deepEqual([railMetaOfEntry(entries.capsule).kind, railMetaOfEntry(entries.capsule).title], ['capsule', '做完右侧轨'])
    assert.equal(railMetaOfEntry({ ...entries.capsule, capsule: { revision: 5, objective: '' } as never } as TimelineEntry).title, '检查点 r5')
  })

  it('分支会话（#236）：智能体之间的话与建立 / 结束事件上轨（branch，不分待处理）；您在分支里说、被抄送的话是您自己的话（归 append，不上轨）', () => {
    const msg = railMetaOfEntry(entries.branch)
    assert.deepEqual([msg.kind, msg.state, msg.title], ['branch', 'done', '接口已就绪'])
    const out = railMetaOfEntry({ ...entries.branch, item: { ...(entries.branch as { item: object }).item, outgoing: true, to: 'wf-a', text: '收到' } } as TimelineEntry)
    assert.deepEqual([out.kind, out.title], ['branch', '→ 收到'])
    const event = railMetaOfEntry({ kind: 'branch', key: 'branch-e-closed-wf-b', at: AT, item: { kind: 'event', at: AT, event: 'closed', branch: 'wf-b', actor: { kind: 'system' } } })
    assert.deepEqual([event.kind, event.title], ['branch', '分支已结束'])
    const human = railMetaOfEntry({ kind: 'branch', key: 'branch-m6', at: AT, item: { kind: 'message', id: 6, at: AT, text: '## 先做登录\n其它', actor: { kind: 'human', workflow: 'wf-b' } } })
    assert.deepEqual([human.kind, human.title], ['append', '先做登录'])
    assert.equal(railStyle(human.kind, human.state), undefined, '您自己的话不上轨')
    assert.ok(railStyle(msg.kind, msg.state), '智能体之间的话上轨')
    assert.equal(railLabel('branch', 'done'), '分支消息')
  })

  it('群聊消息（#241）：只有别的成员（智能体）说的话上轨（member，不分待处理）；您自己的话不上轨；标题把「谁说的」放在前面', () => {
    const base = { id: 'm-1', sent_at: AT }
    const agent = railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: 'Alice' }, text: '## 接口已就绪\n第二行' })
    assert.deepEqual(agent && [agent.id, agent.kind, agent.state, agent.title, agent.at], ['m-1', 'member', 'done', 'Alice：接口已就绪', AT])
    // 名字与卡片头部同一个（senderIdentity）：没有名字退到工作流编号，再没有才是「成员」；没有正文时只写名字
    assert.equal(railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: ' ', workflow_id: 'wf-x' }, text: '收到' })?.title, 'wf-x：收到')
    assert.equal(railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: ' ' }, text: '收到' })?.title, '成员：收到')
    assert.equal(railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: 'Bob' }, text: '' })?.title, 'Bob')
    assert.equal(railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: 'Alice' }, text: '收到' })?.title, `${senderIdentity({ kind: 'agent', name: 'Alice' }).name}：收到`)
    // 标题限长：长话不撑破悬停提示
    const long = railMetaOfGroupMessage({ ...base, sender: { kind: 'agent', name: 'Alice' }, text: '很长'.repeat(80) })!
    assert.ok(long.title.length <= 'Alice：'.length + 49, `标题应被截短：${long.title.length}`)
    // 您自己的话与没有发送者的行（系统行等）：不上轨
    assert.equal(railMetaOfGroupMessage({ ...base, sender: { kind: 'human', name: '您' }, text: '先做登录' }), null)
    assert.equal(railMetaOfGroupMessage({ ...base, sender: undefined as never, text: '系统消息' }), null)
    // 往返：写到 DOM 属性再读回来是同一个条目（轨靠 data-rail-* 扫描）
    const attrs = railDomAttrs(agent!)
    const parsed = parseRailItem(a => attrs[a], 100)
    assert.deepEqual(parsed && [parsed.id, parsed.kind, parsed.state, parsed.title, parsed.at, parsed.top], ['m-1', 'member', 'done', 'Alice：接口已就绪', AT, 100])
    assert.ok(railStyle('member', 'done'), '成员发言上轨')
    assert.equal(railLabel('member', 'done'), '成员发言')
  })

  it('确认项：计划 / 提问各归各的；待答复 = pending，其余（已答复、已忽略、已撤回、已过期）= done', () => {
    const plan = (status: ConfirmItem['status']) => railMetaOfEntry({ kind: 'confirm', key: 'confirm-1', at: AT, item: confirm({ kind: 'plan', status }) })
    assert.deepEqual([plan('pending').kind, plan('pending').state], ['plan', 'pending'])
    for (const st of ['answered', 'dismissed', 'withdrawn', 'expired'] as const) assert.deepEqual([plan(st).kind, plan(st).state], ['plan', 'done'], st)
    const ask = railMetaOfEntry(entries.confirm)
    assert.deepEqual([ask.kind, ask.state, ask.title], ['ask', 'pending', '要不要采用？'])
    const untitled = railMetaOfEntry({ kind: 'confirm', key: 'confirm-7', at: AT, item: confirm({ title: '' }) })
    assert.equal(untitled.title, 'Q7')
  })

  it('文档决策汇总：还有待决策项 = pending，全部处理完 = done', () => {
    assert.deepEqual([railMetaOfEntry(entries.decisions).kind, railMetaOfEntry(entries.decisions).state], ['decisions', 'pending'])
    const e = entries.decisions
    if (e.kind !== 'decisions') throw new Error('夹具')
    const done = railMetaOfEntry({ ...e, group: { ...e.group, pending: 0 } })
    assert.equal(done.state, 'done')
    assert.equal(done.title, '设计稿')
  })

  it('一摞卡片按一个条目算：编号是摞的键、标题「N 张卡片」、时间取第一张', () => {
    const t1 = entries.task
    const t2: TimelineEntry = { ...entries.task, key: 'task-10', at: '2026-10-17T06:00:00Z' } as TimelineEntry
    const block: TimelineBlock = { type: 'stack', key: 'stack-task-9', entries: [t1, t2] }
    const m = railMetaOfBlock(block)
    assert.deepEqual([m.id, m.kind, m.state, m.title, m.at], ['stack-task-9', 'stack', 'done', '2 张卡片', AT])
  })

  it('摞带内含类型的张数（#160）：任务 / 已处理的提问 / 已处理的计划 / 文档决策，按固定次序；单条的块没有 parts', () => {
    const taskB: TimelineEntry = { ...entries.task, key: 'task-10', at: '2026-10-17T06:00:00Z' } as TimelineEntry
    const doneAsk: TimelineEntry = { kind: 'confirm', key: 'confirm-8', at: AT, item: confirm({ seq: 8, status: 'answered' }) }
    const donePlan: TimelineEntry = { kind: 'confirm', key: 'confirm-9', at: AT, item: confirm({ seq: 9, kind: 'plan', status: 'answered' }) }
    const dec = entries.decisions
    if (dec.kind !== 'decisions') throw new Error('夹具')
    const doneDecisions: TimelineEntry = { ...dec, key: 'decisions-doc-2', group: { ...dec.group, pending: 0 } }
    // 乱序放进去，结果仍按 任务 → 提问 → 计划 → 决策
    const mixed = railMetaOfBlock({ type: 'stack', key: 'stack-x', entries: [doneDecisions, entries.task, donePlan, taskB, doneAsk] })
    assert.deepEqual(mixed.parts, [{ type: 'task', count: 2 }, { type: 'ask', count: 1 }, { type: 'plan', count: 1 }, { type: 'decision', count: 1 }])
    // 只有一种类型：一段
    const onlyTasks = railMetaOfBlock({ type: 'stack', key: 'stack-t', entries: [entries.task, taskB] })
    assert.deepEqual(onlyTasks.parts, [{ type: 'task', count: 2 }])
    // 单条的块不带 parts
    assert.equal(railMetaOfBlock({ type: 'single', key: 'confirm-7', entry: entries.confirm }).parts, undefined)
  })

  it('单条的块：编号用块的键（与条目的键一致）', () => {
    const m = railMetaOfBlock({ type: 'single', key: 'confirm-7', entry: entries.confirm })
    assert.deepEqual([m.id, m.kind, m.state], ['confirm-7', 'ask', 'pending'])
  })

  it('一轮 AI 汇报：等回复 = pending；已完成 / 已取消 / 已超时 = done；带会话号', () => {
    const s = (status: string) => railMetaOfRound({ session_id: 'abc', status, title: '第 1 轮', created_at: AT } as never)
    assert.deepEqual([s('pending').kind, s('pending').state, s('pending').id, s('pending').ref], ['round', 'pending', 'round:abc', 'abc'])
    for (const st of ['completed', 'cancelled', 'timeout', 'archived']) assert.equal(s(st).state, 'done', st)
    assert.equal(railMetaOfRound({ session_id: 'x', status: 'completed', title: '', created_at: AT } as never).title, '方案汇报')
    assert.equal(s('pending').at, AT)
  })

  it('您的确认回复：标题取回复第一行，空回复显示「已确认」；时间是您提交的时刻', () => {
    const m = railMetaOfReply({ session_id: 'abc', response_text: '继续第二轮\n补充' } as never, '2026-10-17T05:10:00Z')
    assert.deepEqual([m.id, m.kind, m.state, m.title, m.at, m.ref], ['reply:abc', 'reply', 'done', '继续第二轮', '2026-10-17T05:10:00Z', 'abc'])
    assert.equal(railMetaOfReply({ session_id: 'abc', response_text: '' } as never, AT).title, '已确认')
  })
})

describe('data-rail-* 属性的往返', () => {
  it('railDomAttrs → parseRailItem 还原出同一个条目；ref 只在有会话号时才写', () => {
    const meta = railMetaOfRound({ session_id: 'abc', status: 'pending', title: '第 1 轮', created_at: AT } as never)
    const attrs = railDomAttrs(meta)
    const back = parseRailItem(a => attrs[a] ?? null, 120)
    assert.deepEqual(back, { ...meta, top: 120 })
    const noRef = railDomAttrs(railMetaOfEntry(entries.notice))
    assert.ok(!('data-rail-ref' in noRef))
    assert.equal(parseRailItem(a => noRef[a] ?? null, 0)?.ref, undefined)
  })

  it('摞的 parts 往返（#160）：data-rail-parts =「task:2,ask:1」；只有摞才读，别的种类没有这个属性', () => {
    const t2: TimelineEntry = { ...entries.task, key: 'task-10', at: '2026-10-17T06:00:00Z' } as TimelineEntry
    const doneAsk: TimelineEntry = { kind: 'confirm', key: 'confirm-8', at: AT, item: confirm({ seq: 8, status: 'answered' }) }
    const meta = railMetaOfBlock({ type: 'stack', key: 'stack-x', entries: [entries.task, t2, doneAsk] })
    const attrs = railDomAttrs(meta)
    assert.equal(attrs['data-rail-parts'], 'task:2,ask:1')
    assert.deepEqual(parseRailItem(a => attrs[a] ?? null, 5), { ...meta, ref: undefined, top: 5 })
    // 非摞：没有这个属性；就算 DOM 上多了也不读
    const notice = railDomAttrs(railMetaOfEntry(entries.notice))
    assert.ok(!('data-rail-parts' in notice))
    assert.equal(parseRailItem(a => ({ ...notice, 'data-rail-parts': 'task:2' } as Record<string, string>)[a] ?? null, 0)?.parts, undefined)
    // 摞但没有 parts（旧页面 / 异常）：照常读出，只是没有分段
    const bare: Record<string, string> = { ...attrs }
    delete bare['data-rail-parts']
    assert.equal(parseRailItem(a => bare[a] ?? null, 0)?.parts, undefined)
    // 坏值：不认识的类型、不是正整数、重复的类型都丢掉，剩下的按固定次序
    const bad: Record<string, string> = { ...attrs, 'data-rail-parts': 'plan:1,bogus:3,task:0,ask:x,task:2,plan:5' }
    assert.deepEqual(parseRailItem(a => bad[a] ?? null, 0)?.parts, [{ type: 'task', count: 2 }, { type: 'plan', count: 1 }])
    assert.equal(parseRailItem(a => ({ ...attrs, 'data-rail-parts': 'x:1,,:' } as Record<string, string>)[a] ?? null, 0)?.parts, undefined)
  })

  it('种类不认识、没有编号、位置不是有限数的丢掉；状态缺省或非法按 done', () => {
    const base: Record<string, string> = { 'data-rail-kind': 'ask', 'data-rail-state': 'pending', 'data-rail-id': 'confirm-1', 'data-rail-title': 't', 'data-rail-at': AT }
    const read = (o: Record<string, string>) => (a: string) => o[a] ?? null
    assert.ok(parseRailItem(read(base), 10))
    assert.equal(parseRailItem(read({ ...base, 'data-rail-kind': 'bogus' }), 10), null)
    assert.equal(parseRailItem(read({ ...base, 'data-rail-id': '' }), 10), null)
    assert.equal(parseRailItem(read(base), Number.NaN), null)
    assert.equal(parseRailItem(read(base), Number.POSITIVE_INFINITY), null)
    assert.equal(parseRailItem(read({ ...base, 'data-rail-state': 'weird' }), 10)?.state, 'done')
    const { 'data-rail-state': _omit, ...noState } = base
    assert.equal(parseRailItem(read(noState), 10)?.state, 'done')
  })
})

// ---------- 排布 ----------

/** 伪随机（线性同余），测试可复现。 */
function lcg(seed: number): () => number {
  let s = seed
  return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 0xffffffff)
}

describe('isotonicFit：保序回归（PAVA）', () => {
  const near = (a: number[], b: number[]) => {
    assert.equal(a.length, b.length)
    a.forEach((v, i) => assert.ok(Math.abs(v - b[i]) < 1e-9, `第 ${i} 项 ${v} ≠ ${b[i]}`))
  }
  it('已经非降的原样返回；全降序并成一个均值；中间夹一段降序只并那一段', () => {
    near(isotonicFit([1, 2, 3]), [1, 2, 3])
    near(isotonicFit([3, 2, 1]), [2, 2, 2])
    near(isotonicFit([1, 3, 2, 4]), [1, 2.5, 2.5, 4])
    near(isotonicFit([5, 1, 1, 5]), [7 / 3, 7 / 3, 7 / 3, 5])
  })
  it('空与单个', () => {
    assert.deepEqual(isotonicFit([]), [])
    assert.deepEqual(isotonicFit([4]), [4])
  })
  it('随机 200 组：结果非降、总和不变、且比任何「只改一个数」的非降序列更接近原序列（局部最优性的抽样）', () => {
    const rnd = lcg(7)
    for (let round = 0; round < 200; round++) {
      const v = Array.from({ length: 1 + Math.floor(rnd() * 12) }, () => Math.round(rnd() * 100 - 20))
      const d = isotonicFit(v)
      for (let i = 1; i < d.length; i++) assert.ok(d[i] >= d[i - 1] - 1e-9)
      assert.ok(Math.abs(d.reduce((a, b) => a + b, 0) - v.reduce((a, b) => a + b, 0)) < 1e-6)
      const cost = (x: number[]) => x.reduce((s, xi, i) => s + (xi - v[i]) ** 2, 0)
      // 把某一项挪一点点仍保持非降时，代价不会更低
      for (let i = 0; i < d.length; i++) {
        for (const delta of [-0.5, 0.5]) {
          const e = d.slice()
          e[i] += delta
          const ok = (i === 0 || e[i] >= e[i - 1]) && (i === e.length - 1 || e[i] <= e[i + 1])
          if (ok) assert.ok(cost(e) >= cost(d) - 1e-9, `第 ${round} 组第 ${i} 项挪 ${delta} 反而更接近`)
        }
      }
    }
  })
})

describe('layoutRail：固定间距的缩略轴', () => {
  const opt = { scrollHeight: 4000, trackHeight: 400 }
  const ys = (l: RailLayout) => l.ticks.map(t => t.y)

  it('稀疏时位置按「条目位置 / 内容总高 × 轨高」等比（第一道的点击区不越出轨顶，故最少 8px）', () => {
    const l = layoutRail([item('a', 'round', 0), item('b', 'round', 1000), item('c', 'round', 2000), item('d', 'round', 2200)], opt)
    assert.deepEqual(ys(l), [8, 100, 200, 220])
    assert.deepEqual(l.ticks.map(t => t.item.id), ['a', 'b', 'c', 'd'])
    assert.equal(l.height, 400)
    assert.equal(l.pitch, RAIL_PITCH_PX)
  })

  it('您自己的话（确认回复 / 追加）被滤掉、不占刻度；其余种类都各占一道', () => {
    const mine = [item('reply', 'reply', 500), item('append', 'append', 600), item('reply2', 'reply', 2500, 'pending')]
    const others = [
      item('r1', 'round', 0),
      item('task', 'task', 700),
      item('stack', 'stack', 800),
      item('notice', 'notice', 900),
      item('capsule', 'capsule', 1000),
      item('plan-done', 'plan', 1100),
      item('ask-done', 'ask', 1200),
      item('dec-done', 'decisions', 1300),
      item('p', 'plan', 2000, 'pending'),
      item('r2', 'round', 3000),
    ]
    const l = layoutRail([...mine, ...others], opt)
    assert.deepEqual(l.ticks.map(t => t.item.id), others.map(o => o.id))
  })

  it('挤在一起时按间距推开，相邻中心至少隔 16px；一团围着各自的等比位置居中（不是整体往下漂）', () => {
    // 三个条目想去 200 / 201 / 202：均值 201 → 中心 185 / 201 / 217
    const l = layoutRail([item('a', 'round', 2000), item('b', 'plan', 2010, 'pending'), item('c', 'round', 2020)], opt)
    assert.deepEqual(ys(l), [185, 201, 217])
    for (let i = 1; i < l.ticks.length; i++) assert.ok(l.ticks[i].y - l.ticks[i - 1].y >= RAIL_PITCH_PX)
  })

  it('推开时不越出轨顶：想去顶上的一团从 8px 开始往下排', () => {
    const l = layoutRail([item('a', 'round', 0), item('b', 'round', 10), item('c', 'round', 20)], opt)
    assert.deepEqual(ys(l), [8, 24, 40])
  })

  it('放得下时也不越出轨底：想去底上的一团向上收，最后一道点击区正好贴着轨底', () => {
    const l = layoutRail([item('a', 'round', 3980), item('b', 'round', 3990), item('c', 'round', 4000)], opt)
    assert.deepEqual(ys(l), [360, 376, 392])
    assert.equal(l.ticks[2].y + RAIL_PITCH_PX / 2, opt.trackHeight)
    assert.equal(l.height, opt.trackHeight)
  })

  it('放不下时（n × 16 > 轨高）退化成等间距，轨内容高 = 最后一道的下沿，比轨高大，轨自己滚动', () => {
    const items = Array.from({ length: 10 }, (_, i) => item(`r${i}`, 'round', i * 900))
    const l = layoutRail(items, { scrollHeight: 10000, trackHeight: 100 })
    assert.deepEqual(ys(l), [8, 24, 40, 56, 72, 88, 104, 120, 136, 152])
    assert.equal(l.height, 160)
    assert.ok(l.height > 100)
  })

  it('刚好放得下（n × 16 = 轨高）时是等间距，轨内容高 = 轨高', () => {
    const items = Array.from({ length: 5 }, (_, i) => item(`r${i}`, 'round', i * 100))
    const l = layoutRail(items, { scrollHeight: 4000, trackHeight: 80 })
    assert.deepEqual(ys(l), [8, 24, 40, 56, 72])
    assert.equal(l.height, 80)
  })

  it('没有合并：每个可画的条目一道刻度，刻度带着它自己的条目，样式按条目的种类与状态', () => {
    const items = [item('a', 'round', 1000, 'pending'), item('b', 'ask', 1001, 'pending'), item('c', 'plan', 1002, 'pending')]
    const l = layoutRail(items, opt)
    assert.equal(l.ticks.length, 3)
    assert.deepEqual(l.ticks.map(t => [t.item.id, t.len, t.line, t.tone, t.pulse, t.dim]), [
      ['a', 24, 4, 'zinc', true, false],
      ['b', 24, 4, 'amber', false, false],
      ['c', 24, 4, 'red', true, false],
    ])
  })

  it('入参乱序也得到同样的结果，且不修改入参；位置相同的保持入参顺序', () => {
    const list = Object.freeze([item('c', 'round', 2000), item('a', 'round', 0), item('b', 'round', 1000)].map(i => Object.freeze(i)))
    const l = layoutRail(list, opt)
    assert.deepEqual(l.ticks.map(t => t.item.id), ['a', 'b', 'c'])
    assert.deepEqual(list.map(i => i.id), ['c', 'a', 'b'])
    const same = layoutRail([item('x', 'round', 500), item('y', 'plan', 500, 'pending'), item('z', 'round', 500)], opt)
    assert.deepEqual(same.ticks.map(t => t.item.id), ['x', 'y', 'z'])
  })

  it('越界与异常输入：位置夹在轨内；位置非有限数的跳过；内容或轨高为 0 时没有刻度', () => {
    const edge = layoutRail([item('top', 'round', -50), item('bottom', 'round', 4000), item('way', 'round', 99999)], opt)
    assert.equal(edge.ticks[0].y, 8)
    assert.ok(edge.ticks[edge.ticks.length - 1].y + RAIL_PITCH_PX / 2 <= opt.trackHeight)
    assert.equal(layoutRail([item('x', 'round', Number.NaN), item('y', 'round', Number.POSITIVE_INFINITY)], opt).ticks.length, 0)
    assert.deepEqual(layoutRail([item('a', 'round', 10)], { scrollHeight: 0, trackHeight: 400 }), { ticks: [], height: 400, pitch: 16 })
    assert.deepEqual(layoutRail([item('a', 'round', 10)], { scrollHeight: 4000, trackHeight: 0 }), { ticks: [], height: 0, pitch: 16 })
    assert.deepEqual(layoutRail([], opt), { ticks: [], height: 400, pitch: 16 })
  })

  it('pitch 可调：点击区高度与间距一起变', () => {
    const l = layoutRail([item('a', 'round', 0), item('b', 'round', 10)], { ...opt, pitch: 20 })
    assert.deepEqual(ys(l), [10, 30])
    assert.equal(l.pitch, 20)
  })

  it('伪随机 500 个（含各种种类）：数量 = 可画的条目数；间距 ≥ 16；位置是整数；点击区都在轨内容之内；放得下时轨内容高 = 轨高', () => {
    const rnd = lcg(12345)
    for (const trackHeight of [120, 400, 900, 3000]) {
      const items = Array.from({ length: 500 }, (_, i) => {
        const k = RAIL_KINDS[Math.floor(rnd() * RAIL_KINDS.length)]
        return item(`r${i}`, k, Math.floor(rnd() * 20000), rnd() < 0.3 ? 'pending' : 'done')
      })
      const drawable = items.filter(i => railStyle(i.kind, i.state)).length
      const l = layoutRail(items, { scrollHeight: 20000, trackHeight })
      assert.equal(l.ticks.length, drawable)
      for (let i = 0; i < l.ticks.length; i++) {
        const t = l.ticks[i]
        assert.ok(Number.isInteger(t.y), `y 应是整数：${t.y}`)
        assert.ok(t.y - RAIL_PITCH_PX / 2 >= 0, '点击区不越出上沿')
        assert.ok(t.y + RAIL_PITCH_PX / 2 <= l.height, `点击区不越出下沿：${t.y} + 8 > ${l.height}`)
        if (i > 0) assert.ok(t.y - l.ticks[i - 1].y >= RAIL_PITCH_PX, `第 ${i} 道离上一道只有 ${t.y - l.ticks[i - 1].y}px`)
      }
      if (drawable * RAIL_PITCH_PX <= trackHeight) assert.equal(l.height, trackHeight)
      else assert.equal(l.height, l.ticks[l.ticks.length - 1].y + RAIL_PITCH_PX / 2)
    }
  })
})

describe('shouldShowRail：内容不足 1.5 屏或刻度不足 2 道时不显示', () => {
  const m = { scrollHeight: 1500, clientHeight: 1000, trackHeight: 600, tickCount: 5 }
  it('正好 1.5 屏显示，差一点不显示', () => {
    assert.equal(RAIL_MIN_SCREENS, 1.5)
    assert.equal(shouldShowRail(m), true)
    assert.equal(shouldShowRail({ ...m, scrollHeight: 1499 }), false)
    assert.equal(shouldShowRail({ ...m, scrollHeight: 9000 }), true)
  })
  it('刻度不足 2 道（只有一道没有可导航的东西）、没有轨高、可视高度为 0 都不显示', () => {
    assert.equal(shouldShowRail({ ...m, tickCount: 2 }), true)
    assert.equal(shouldShowRail({ ...m, tickCount: 1 }), false)
    assert.equal(shouldShowRail({ ...m, tickCount: 0 }), false)
    assert.equal(shouldShowRail({ ...m, trackHeight: 0 }), false)
    assert.equal(shouldShowRail({ ...m, clientHeight: 0 }), false)
  })
})

describe('railMapper / railViewportBox：当前可视范围在轨内容里的窗口', () => {
  // 稀疏的夹具：4 个轮次，刻度中心 8 / 100 / 200 / 220，点击区上沿 0 / 92 / 192 / 212
  const sparse = layoutRail([item('a', 'round', 0), item('b', 'round', 1000), item('c', 'round', 2000), item('d', 'round', 2200)], { scrollHeight: 4000, trackHeight: 400 })

  it('锚点：滚到某个条目顶上时映射到它的点击区上沿；两端是 (0, 0) 与 (内容总高, 轨内容总高)', () => {
    const f = railMapper(sparse, 4000)
    assert.equal(f(0), 0)
    assert.equal(f(1000), 92)
    assert.equal(f(2000), 192)
    assert.equal(f(2200), 212)
    assert.equal(f(4000), 400)
  })

  it('锚点之间线性插值；越界夹到两端；NaN 当 0', () => {
    const f = railMapper(sparse, 4000)
    assert.equal(f(500), 46)
    assert.equal(f(3100), 306) // (2200→212)~(4000→400) 的中点 3100 → 306
    assert.equal(f(-30), 0)
    assert.equal(f(99999), 400)
    assert.equal(f(Number.NaN), 0)
  })

  it('映射单调不减（含被推开的密集排布）：伪随机 300 个点', () => {
    const rnd = lcg(99)
    const items = Array.from({ length: 80 }, (_, i) => item(`r${i}`, 'round', Math.floor(rnd() * 8000)))
    const l = layoutRail(items, { scrollHeight: 8000, trackHeight: 500 })
    const f = railMapper(l, 8000)
    let prev = -1
    for (let x = 0; x <= 8000; x += 8000 / 300) {
      const y = f(x)
      assert.ok(y >= prev - 1e-9, `x=${x} 映射 ${y} 小于前一个 ${prev}`)
      prev = y
    }
    assert.equal(f(8000), l.height)
  })

  it('稀疏排布下窗口 = 条目位置的等比换算：位置 = 映射(scrollTop)，高度 = 映射(scrollTop + clientHeight) − 位置', () => {
    const base = { scrollTop: 0, clientHeight: 1000, scrollHeight: 4000 }
    assert.deepEqual(railViewportBox(sparse, base), { top: 0, height: 92 })
    assert.deepEqual(railViewportBox(sparse, { ...base, scrollTop: 1000 }), { top: 92, height: 100 })
    // 滚到底：f(3000) 落在 (2200→212)~(4000→400) 这一段上，位置 = 212 + 800 × 188 / 1800；下沿 = f(4000) = 400
    const bottom = railViewportBox(sparse, { ...base, scrollTop: 3000 })
    assert.ok(Math.abs(bottom.top - (212 + (800 * 188) / 1800)) < 1e-9, `top ${bottom.top}`)
    assert.ok(Math.abs(bottom.top + bottom.height - 400) < 1e-9, `bottom ${bottom.top + bottom.height}`)
  })

  it('滚到底：窗口贴着轨内容的底边，不越界；橡皮筋回弹（负的 scrollTop）夹到 0', () => {
    const base = { clientHeight: 1000, scrollHeight: 4000 }
    const bottom = railViewportBox(sparse, { ...base, scrollTop: 3000 })
    assert.equal(bottom.top + bottom.height, 400)
    const over = railViewportBox(sparse, { ...base, scrollTop: 99999 })
    assert.ok(over.top + over.height <= 400 + 1e-9)
    assert.equal(railViewportBox(sparse, { ...base, scrollTop: -40 }).top, 0)
  })

  it('窗口至少 8px 高', () => {
    const tiny = railViewportBox(sparse, { scrollTop: 100, clientHeight: 2, scrollHeight: 4000 })
    assert.equal(tiny.height, 8)
  })

  it('刻度被推开时窗口仍对得上刻度：视口里是第 3 ~ 5 个条目，窗口恰好罩住它们的三道点击区', () => {
    // 20 个条目挤在内容前 400px（每个 20px），轨高 500：必然被推开成 16px 间距
    const items = Array.from({ length: 20 }, (_, i) => item(`r${i}`, 'round', i * 20))
    const l = layoutRail(items, { scrollHeight: 4000, trackHeight: 500 })
    for (let i = 1; i < 20; i++) assert.ok(l.ticks[i].y - l.ticks[i - 1].y >= 16)
    // 视口从第 3 个条目顶（top=40）开始、高度 60 → 恰好含第 3、4、5 个条目（top 40 / 60 / 80），下沿在第 6 个条目顶（top 100）
    const b = railViewportBox(l, { scrollTop: 40, clientHeight: 60, scrollHeight: 4000 })
    assert.equal(b.top, l.ticks[2].y - 8)
    assert.equal(b.top + b.height, l.ticks[5].y - 8)
  })

  it('没有刻度 / 没有内容高度 / 没有轨高：不崩', () => {
    const empty: RailLayout = { ticks: [], height: 400, pitch: 16 }
    assert.deepEqual(railViewportBox(empty, { scrollTop: 0, clientHeight: 100, scrollHeight: 1000 }), { top: 0, height: 40 })
    assert.deepEqual(railViewportBox(sparse, { scrollTop: 0, clientHeight: 100, scrollHeight: 0 }), { top: 0, height: 400 })
    assert.deepEqual(railViewportBox({ ticks: [], height: 0, pitch: 16 }, { scrollTop: 0, clientHeight: 100, scrollHeight: 1000 }), { top: 0, height: 0 })
  })
})

describe('followRailScroll：轨放不下时跟着可视窗口走', () => {
  const m = { scrollTop: 0, trackHeight: 400, height: 1000 }
  it('放得下时恒为 0', () => {
    assert.equal(followRailScroll({ top: 300, height: 50 }, { scrollTop: 0, trackHeight: 400, height: 400 }), 0)
    assert.equal(followRailScroll({ top: 300, height: 50 }, { scrollTop: 77, trackHeight: 400, height: 300 }), 0)
  })
  it('窗口在可见范围（上下各留 16px）里就不动', () => {
    assert.equal(followRailScroll({ top: 100, height: 50 }, { ...m, scrollTop: 50 }), 50)
  })
  it('窗口在下面：滚到窗口下沿留 16px 的位置', () => {
    assert.equal(followRailScroll({ top: 600, height: 50 }, m), 650 - 400 + 16)
  })
  it('窗口在上面：滚到窗口上沿留 16px 的位置，不为负', () => {
    assert.equal(followRailScroll({ top: 200, height: 50 }, { ...m, scrollTop: 400 }), 184)
    assert.equal(followRailScroll({ top: 10, height: 50 }, { ...m, scrollTop: 200 }), 0)
  })
  it('夹在 [0, 内容高 − 可用高]', () => {
    assert.equal(followRailScroll({ top: 960, height: 40 }, m), 600)
    assert.equal(followRailScroll({ top: 0, height: 20 }, { ...m, scrollTop: 9999 }), 0)
  })
  it('窗口比可见范围还高：对齐窗口上沿', () => {
    assert.equal(followRailScroll({ top: 100, height: 400 }, { ...m, scrollTop: 300 }), 84)
  })
})

describe('jumpScrollTop：点击刻度后列表滚到哪', () => {
  const m = { scrollHeight: 5000, clientHeight: 800 }
  it('条目顶边上方留 24px', () => {
    assert.equal(jumpScrollTop(1000, m), 976)
    assert.equal(jumpScrollTop(1000, m, 60), 940)
  })
  it('靠近顶部不为负，靠近底部不超过可滚范围', () => {
    assert.equal(jumpScrollTop(10, m), 0)
    assert.equal(jumpScrollTop(4900, m), 4200)
  })
  it('内容比可视区矮时恒为 0', () => {
    assert.equal(jumpScrollTop(300, { scrollHeight: 500, clientHeight: 800 }), 0)
  })
})

describe('formatRailTime：悬停提示里的时间', () => {
  const now = new Date(2026, 9, 17, 13, 0).getTime()
  it('今天 → 时:分；今年 → 月-日 时:分；更早 → 带年份', () => {
    assert.equal(formatRailTime(new Date(2026, 9, 17, 9, 5).toISOString(), now), '09:05')
    assert.equal(formatRailTime(new Date(2026, 8, 3, 21, 7).toISOString(), now), '09-03 21:07')
    assert.equal(formatRailTime(new Date(2025, 11, 31, 23, 59).toISOString(), now), '2025-12-31 23:59')
  })
  it('空与解析不了返回空串', () => {
    assert.equal(formatRailTime('', now), '')
    assert.equal(formatRailTime(undefined, now), '')
    assert.equal(formatRailTime('not-a-date', now), '')
  })
})

// ---------- 接入预览列表（源码级）----------

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'timelineRail.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('预览列表接入右侧轨（源码级防线）', () => {
  const list = read('components/feedback/preview-view/PreviewMessageList.vue')

  it('会话列表用右侧轨（PreviewTimelineRail）；旧的 TimelineScrubber 已删除（#166）', () => {
    assert.match(list, /<PreviewTimelineRail\b/)
    assert.doesNotMatch(list, /<TimelineScrubber\b/)
    assert.ok(!existsSync(join(srcDir(), 'components', 'TimelineScrubber.vue')), 'TimelineScrubber.vue 应已删除')
  })

  it('三类条目都挂了 data-rail-*：时间线的每一块、每一轮汇报、每条确认回复', () => {
    assert.match(list, /v-bind="railDomAttrs\(railMetaOfBlock\(block\)\)"/)
    assert.match(list, /v-bind="railDomAttrs\(railMetaOfRound\(session\)\)"/)
    assert.match(list, /v-bind="railDomAttrs\(railMetaOfReply\(session,/)
  })

  it('点击刻度走列表里现成的滚动：轮次用 scrollToSession，载入更早走 loadEarlierRounds', () => {
    assert.match(list, /@jump="onRailJump"/)
    assert.match(list, /@load-earlier="loadEarlierRounds"/)
    assert.match(list, /scrollToSession\(item\.ref\)/)
  })

  it('共享叶子组件都没有 data-rail-*（轨只在列表里挂属性）', () => {
    for (const rel of ['components/feedback/CardStack.vue', 'components/feedback/TimelineCardEntry.vue', 'components/feedback/AsyncTimelineEntry.vue', 'components/confirms/ConfirmTimelineCard.vue', 'components/confirms/ConfirmCard.vue']) {
      assert.doesNotMatch(read(rel), /data-rail-/, rel)
    }
  })
})

describe('右侧轨组件（源码级防线，#223 点得准 / 不太密 / 能滚动）', () => {
  const rail = read('components/feedback/preview-view/PreviewTimelineRail.vue')
  // 刻度列表从 #158 起拆到 PreviewRailTicks（会话列表每滚一帧不再带着几百道刻度重渲染）：刻度本身的模板断言读它
  const ticks = read('components/feedback/preview-view/PreviewRailTicks.vue')

  it('每道刻度的点击区高 = 排布间距（RAIL_PITCH_PX），横向铺满整条轨；不再有 6px 的小点击区与「合并」', () => {
    assert.match(ticks, /height: `\$\{RAIL_PITCH_PX\}px`/)
    assert.match(ticks, /top: `\$\{t\.y - RAIL_PITCH_PX \/ 2\}px`/)
    assert.match(ticks, /absolute inset-x-0 flex cursor-pointer/)
    assert.doesNotMatch(rail + ticks, /h-\[6px\]|data-count|data-first|members|\.rep\b|\.first\b/)
  })

  it('单个刻度的颜色按卡片类型（追加 #265）：--card-tone 变量 + CARD_TONE_CLASS[t.tone]，已处理的变淡，带 data-tone 方便核对；不再有按状态的色类', () => {
    assert.match(ticks, /import \{ CARD_TONE_CLASS \} from '\.\.\/\.\.\/\.\.\/lib\/messageCard'/)
    assert.match(ticks, /block shrink-0 rounded-full bg-\[color:var\(--card-tone\)\]/)
    assert.match(ticks, /CARD_TONE_CLASS\[t\.tone\]/)
    assert.match(ticks, /opacity: RAIL_DIM_OPACITY/)
    assert.match(ticks, /:data-tone="t\.tone"/)
    assert.match(ticks, /:data-dim="t\.dim \? '1' : undefined"/)
    assert.doesNotMatch(rail + ticks, /RAIL_TONE_CLASS|bg-destructive|color-status-pending|color-status-active/)
  })

  it('轨自己是滚动容器：overflow-y-auto + 不出滚动条 + 滚到头不带动页面；放得下时对鼠标透明、放不下才接管滚轮', () => {
    assert.match(rail, /no-scrollbar absolute inset-0 overflow-y-auto overscroll-contain/)
    assert.match(rail, /:class="overflowing \? 'pointer-events-auto' : ''"/)
    assert.match(rail, /pointer-events-none absolute right-1\.5 top-3 z-20 hidden w-8/)
  })

  it('轨的下沿让开输入区的悬浮行与「回到底部」：固定让开 112px（有待发图片 176px），不再贴着容器下缘（bottom-3）', () => {
    const list = read('components/feedback/preview-view/PreviewMessageList.vue')
    // 两行芯片 62 + 间隙 8 + 「回到底部」浮在上面 31 ≈ 101px，留 11px 余量 = 112px（bottom-28）；有待发图片再加一排缩略图 ≈ 70px → 176px（bottom-44）
    assert.match(rail, /hasDraftImages \? 'bottom-44' : 'bottom-28'/)
    assert.doesNotMatch(rail, /top-3 bottom-3/)
    // 列表在有待发图片时同样多留白（pb-28 / sm:pb-32），轨靠这个属性跟着变
    assert.match(list, /props\.hasDraftImages \? 'pb-28 sm:pb-32' : 'pb-14 sm:pb-16'/)
    assert.match(list, /:has-draft-images="props\.hasDraftImages"/)
  })

  it('跟随可视窗口用 followRailScroll，用户在轨上滚轮后暂停跟随；窗口位置用 railViewportBox（分段线性）而不是等比', () => {
    assert.match(rail, /followRailScroll\(/)
    assert.match(rail, /FOLLOW_PAUSE_MS/)
    // 滚轮与触摸（touchstart / touchmove）都算用户在拨轨
    assert.match(rail, /@wheel\.passive="onRailUserScroll"/)
    assert.match(rail, /@touchstart\.passive="onRailUserScroll"/)
    assert.match(rail, /@touchmove\.passive="onRailUserScroll"/)
    assert.match(rail, /railViewportBox\(/)
    assert.doesNotMatch(rail, /\bviewportBox\(/)
  })

  it('用户正在用轨时轨不自己动（鼠标指针停在轨上、或刚拨过轨）；被拦下的跟随拦截一结束就补一次，会话没动就不补（轨不会自己弹回去）', () => {
    assert.match(rail, /@pointerenter="onRailPointerEnter"/)
    assert.match(rail, /@pointerleave="onRailPointerLeave"/)
    // 触摸点不算「指针停在轨上」（触屏上的 :hover 会粘住，触摸靠 touchstart / touchmove 记时间）
    assert.match(rail, /ev\.pointerType !== 'touch'/)
    assert.match(rail, /ev\.pointerType === 'touch' \|\| !pointerInside/)
    // 保险：漏掉 pointerleave（轨在指针下面被藏起来）时，按真实的悬停状态自愈
    assert.match(rail, /scrollerEl\.value\?\.matches\(':hover'\)/)
    // 被拦下的跟随记在 followHeld、排一次补跟；指针离开后再安静一个暂停期才补；没被拦下就不补
    assert.match(rail, /followHeld = true\s+scheduleCatchUp\(hold\)/)
    assert.match(rail, /if \(followHeld\) scheduleCatchUp\(FOLLOW_PAUSE_MS\)/)
    assert.match(rail, /if \(!Number\.isFinite\(afterMs\)\) return/)
    // 轨藏起来 / 卸载时清掉状态与定时器，免得「指针在轨上」粘住、或卸载后还有定时器在跑
    assert.match(rail, /function resetFollowHold\(\)/)
    const clear = rail.slice(rail.indexOf('function clearTicks()'), rail.indexOf('function measure()'))
    assert.match(clear, /resetFollowHold\(\)/)
    const unmount = rail.slice(rail.indexOf('onBeforeUnmount('))
    assert.match(unmount.slice(0, 400), /resetFollowHold\(\)/)
  })

  it('刻度数用「画出来的」刻度判断是否显示（shouldShowRail 的 tickCount），不是列表里的条目数', () => {
    assert.match(rail, /tickCount: next\.ticks\.length/)
    assert.doesNotMatch(rail, /itemCount/)
  })

  it('提示框在滚动容器外面（不被 overflow 削掉），位置要减掉轨自己的滚动', () => {
    const scroller = rail.indexOf('data-testid="pv-rail-scroller"')
    const content = rail.indexOf('data-testid="pv-rail-content"')
    const tip = rail.indexOf('data-testid="pv-rail-tip"')
    assert.ok(scroller > 0 && content > scroller && tip > content, '提示框在滚动容器之后')
    assert.match(rail, /t\.y - railScroll\.value/)
    // 滚动容器的右括号必须在提示框之前：数 <div / </div> 的平衡太脆，这里只验证提示框前面有「放在滚动容器外面」的说明
    assert.match(rail, /放在滚动容器外面/)
  })
})
