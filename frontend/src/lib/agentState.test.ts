import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { QueuedFeedback } from '../api/types'
import {
  agentDotShown,
  applyStateEvent,
  basicFacts,
  chipSpanText,
  clientText,
  ctxText,
  dotToneClass,
  endReasonText,
  factText,
  groupByWorkflow,
  pickSession,
  screenFacts,
  sessionSpanText,
  shortConversationId,
  spanText,
  sourceNote,
  sourceText,
  stateView,
  subText,
  RUN_ENDED_TEXT,
  textToneClass,
  tokensText,
  turnSpanText,
  validTime,
  type AgentIdentity,
  type AgentSession,
} from './agentState'

const NOW = Date.parse('2026-10-14T12:00:00.000Z')
const ago = (ms: number) => new Date(NOW - ms).toISOString()
const MIN = 60_000

let nextId = 1
function mk(p: Partial<AgentSession> = {}): AgentSession {
  return {
    id: nextId++,
    identity_id: 1,
    workflow_id: 'wf-a',
    conversation_id: 'conv-0123456789',
    identity_label: 'RelayMesh',
    host_name: 'devhost',
    project_directory: '/home/user/RelayMesh',
    last_event_at: ago(5_000),
    state: 'working',
    base_state: 'working',
    since: ago(2 * MIN),
    quiet_seconds: 5,
    waiting_you: false,
    stalled: false,
    stalled_soft: false,
    approval_suspected: false,
    approval_overdue: false,
    ...p,
  }
}

function mkIdentity(p: Partial<AgentIdentity> = {}): AgentIdentity {
  return {
    id: 1,
    credential_id: 1,
    host_name: 'devhost',
    project_directory: '/home/user/RelayMesh',
    name: '',
    directory_name: 'RelayMesh',
    label: 'RelayMesh',
    label_degraded: false,
    first_seen_at: ago(60 * MIN),
    last_seen_at: ago(MIN),
    ...p,
  }
}

function mkItem(p: Partial<QueuedFeedback>): QueuedFeedback {
  return {
    id: 1,
    workflow_id: 'wf-a',
    response_text: 'x',
    status: 'pending',
    created_at: ago(10 * MIN),
    updated_at: ago(10 * MIN),
    ...p,
  }
}

describe('spanText', () => {
  const table: Array<[number, string]> = [
    [0, '0 秒'],
    [44_000, '44 秒'],
    [59_400, '59 秒'],
    [60_000, '1 分钟'],
    [59 * MIN, '59 分钟'],
    [60 * MIN, '1 小时'],
    [65 * MIN, '1 小时 5 分'],
    [47 * 60 * MIN, '47 小时'],
    [48 * 60 * MIN, '2 天'],
    [-5000, '0 秒'],
  ]
  for (const [ms, want] of table) it(`${ms}ms → ${want}`, () => assert.equal(spanText(ms), want))
})

describe('validTime', () => {
  it('Go 零值时间当作没有', () => {
    assert.equal(validTime('0001-01-01T00:00:00Z'), null)
    assert.equal(validTime(''), null)
    assert.equal(validTime(undefined), null)
    assert.equal(validTime('不是时间'), null)
  })
  it('正常时间解析成毫秒', () => {
    assert.equal(validTime('2026-10-14T12:00:00.000Z'), NOW)
  })
})

describe('subText / endReasonText', () => {
  it('子状态翻译', () => {
    assert.equal(subText('thinking'), '思考')
    assert.equal(subText('responding'), '回应')
    assert.equal(subText('tool:Shell'), '工具 Shell')
    assert.equal(subText('tool:'), '工具')
    assert.equal(subText(''), '')
    assert.equal(subText(undefined), '')
  })
  it('超长工具名被截断', () => {
    const t = subText(`tool:${'x'.repeat(60)}`)
    assert.ok(t.endsWith('…'))
    assert.ok([...t].length <= '工具 '.length + 24)
  })
  it('结束原因', () => {
    assert.equal(endReasonText('completed'), '已收尾')
    assert.equal(endReasonText('blocked'), '受阻')
    assert.equal(endReasonText('handoff'), '已移交')
    assert.equal(endReasonText('whatever'), '')
    assert.equal(endReasonText(undefined), '')
  })
})

describe('stateView', () => {
  it('没有会话 = 未接入', () => {
    for (const s of [null, undefined, mk({ state: '' })]) {
      const v = stateView(s, NOW)
      assert.equal(v.key, 'unknown')
      assert.equal(v.tone, 'muted')
      assert.equal(v.pulse, false)
    }
  })

  it('工作中带子活动并脉动', () => {
    const v = stateView(mk({ sub: 'tool:Shell' }), NOW)
    assert.equal(v.key, 'working')
    assert.equal(v.text, '工作中')
    assert.equal(v.detail, '工具 Shell')
    assert.equal(v.tone, 'active')
    assert.equal(v.pulse, true)
    assert.equal(v.flag, '')
    assert.ok(v.title.includes('已持续 2 分钟'))
  })

  it('软标记：可能卡住，颜色变黄但仍是工作中', () => {
    const v = stateView(mk({ sub: 'thinking', stalled_soft: true, reason: 'quiet_soft' }), NOW)
    assert.equal(v.key, 'working')
    assert.equal(v.flag, 'stalled_soft')
    assert.equal(v.flagText, '可能卡住')
    assert.equal(v.tone, 'warn')
  })

  it('疑似等批准只是提示，不改颜色', () => {
    const v = stateView(mk({ sub: 'tool:Shell', approval_suspected: true, reason: 'tool_running' }), NOW)
    assert.equal(v.key, 'working')
    assert.equal(v.flag, 'approval_suspected')
    assert.equal(v.tone, 'active')
  })

  it('硬卡住（静默）：第一眼可见，写明多久没有动静', () => {
    const v = stateView(mk({ sub: 'thinking', stalled: true, reason: 'quiet', last_event_at: ago(35 * MIN), quiet_seconds: 35 * 60 }), NOW)
    assert.equal(v.key, 'stalled')
    assert.equal(v.text, '疑似卡住')
    assert.equal(v.tone, 'danger')
    assert.equal(v.pulse, true)
    assert.ok(v.detail.includes('35 分钟'))
    assert.ok(v.title.includes('不一定真的卡住'))
  })

  it('硬卡住（工具超时）：写明工具名与运行时长', () => {
    const v = stateView(
      mk({ sub: 'tool:Shell', stalled: true, reason: 'tool_overrun', last_tool: 'Shell', tool_started_at: ago(70 * MIN) }),
      NOW,
    )
    assert.equal(v.key, 'stalled')
    assert.ok(v.detail.includes('Shell'))
    assert.ok(v.detail.includes('1 小时 10 分'))
  })

  it('空闲', () => {
    const v = stateView(mk({ state: 'idle', base_state: 'idle', sub: undefined }), NOW)
    assert.equal(v.key, 'idle')
    assert.equal(v.text, '空闲')
    assert.equal(v.tone, 'calm')
    assert.equal(v.pulse, false)
  })

  it('等你回答：提醒色并脉动（不是卡住）', () => {
    const v = stateView(mk({ state: 'waiting_you', waiting_you: true }), NOW)
    assert.equal(v.key, 'waiting_you')
    assert.equal(v.tone, 'attention')
    assert.equal(v.pulse, true)
    assert.ok(v.title.includes('不是卡住'))
  })

  it('等你批准 / 等太久', () => {
    const v = stateView(mk({ state: 'waiting_approval', base_state: 'waiting_approval' }), NOW)
    assert.equal(v.key, 'waiting_approval')
    assert.equal(v.flag, '')
    const late = stateView(mk({ state: 'waiting_approval', base_state: 'waiting_approval', approval_overdue: true }), NOW)
    assert.equal(late.flag, 'approval_overdue')
    assert.equal(late.flagText, '等批准太久了')
  })

  it('中断 / 出错', () => {
    assert.equal(stateView(mk({ state: 'aborted' }), NOW).tone, 'warn')
    const err = stateView(mk({ state: 'error' }), NOW)
    assert.equal(err.tone, 'danger')
    assert.equal(err.pulse, false)
  })

  it('已结束带原因，无需关注', () => {
    const v = stateView(mk({ state: 'ended', end_reason: 'blocked' }), NOW)
    assert.equal(v.key, 'ended')
    assert.equal(v.detail, '受阻')
    assert.equal(v.tone, 'muted')
  })

  it('离线写明多久没有信号', () => {
    const v = stateView(mk({ state: 'offline', last_event_at: ago(3 * 60 * MIN), quiet_seconds: 3 * 3600 }), NOW)
    assert.equal(v.key, 'offline')
    assert.ok(v.detail.includes('3 小时'))
  })

  it('没见过的状态名不抛错', () => {
    const v = stateView(mk({ state: 'warp' as never }), NOW)
    assert.equal(v.key, 'unknown')
    assert.equal(v.text, 'warp')
  })
})

describe('agentDotShown（侧栏 Agent 小圆点画不画；与「工作中」芯片共用，只留一个圆点，追加 #267）', () => {
  it('没有 hook 汇报 / 状态未知 = 不画；有会话且状态已知（工作中、空闲、等你……）= 画', () => {
    for (const s of [null, undefined, mk({ state: '' }), mk({ state: 'warp' as never })]) assert.equal(agentDotShown(s, NOW), false)
    for (const state of ['working', 'idle', 'waiting_you', 'error'] as const) assert.equal(agentDotShown(mk({ state }), NOW), true, state)
  })

  it('判断与 stateView 一致：画 = 状态 key 不是 unknown', () => {
    for (const s of [null, mk(), mk({ state: 'idle' }), mk({ state: 'warp' as never })]) {
      assert.equal(agentDotShown(s, NOW), stateView(s, NOW).key !== 'unknown')
    }
  })
})

describe('状态来源（#129：非交互运行的输出）', () => {
  it('来源的短写法与解释', () => {
    assert.equal(sourceText('run'), '运行输出')
    assert.equal(sourceText('hook'), '编辑器 hook')
    assert.equal(sourceText('tui'), '终端画面')
    assert.equal(sourceText(''), '')
    assert.equal(sourceText(undefined), '')
    assert.equal(sourceText('warp'), '')
    assert.ok(sourceNote('run').includes('stream-json'))
    assert.ok(sourceNote('run').includes('不依赖编辑器 hook'))
    assert.ok(sourceNote('tui').includes('托管终端'))
    // 没有来源（旧数据）或来源是 hook：沿用原来的脚注。
    assert.ok(sourceNote('').startsWith('状态由编辑器 hook 的汇报推断'))
    assert.equal(sourceNote('hook'), sourceNote(undefined))
  })

  it('运行输出推进的空闲态写明「本次运行结束（可续聊）」，hook 来源的不变', () => {
    const run = stateView(mk({ state: 'idle', base_state: 'idle', sub: undefined, state_source: 'run' }), NOW)
    assert.equal(run.key, 'idle')
    assert.equal(run.detail, RUN_ENDED_TEXT)
    assert.equal(RUN_ENDED_TEXT, '本次运行结束（可续聊）')
    assert.ok(run.title.includes('本次非交互运行已经结束'))
    assert.ok(run.title.includes('可以接着聊'))
    const hook = stateView(mk({ state: 'idle', base_state: 'idle', sub: undefined, state_source: 'hook' }), NOW)
    assert.equal(hook.detail, '')
    assert.ok(hook.title.includes('等你的下一条指令'))
    const old = stateView(mk({ state: 'idle', base_state: 'idle', sub: undefined }), NOW)
    assert.equal(old.detail, '')
  })

  it('运行被中断 / 出错：说清是本次运行，对话没有结束', () => {
    const aborted = stateView(mk({ state: 'aborted', base_state: 'aborted', state_source: 'run' }), NOW)
    assert.equal(aborted.detail, '本次运行被中断')
    assert.ok(aborted.title.includes('对话没有结束'))
    const err = stateView(mk({ state: 'error', base_state: 'error', state_source: 'run' }), NOW)
    assert.equal(err.detail, '本次运行出错')
    assert.ok(err.title.includes('对话没有结束'))
    // hook 来源的文案不变。
    assert.equal(stateView(mk({ state: 'aborted', state_source: 'hook' }), NOW).detail, '')
    assert.equal(stateView(mk({ state: 'error', state_source: 'hook' }), NOW).detail, '')
  })

  it('工作中的悬停说明来自运行输出；子活动照旧', () => {
    const v = stateView(mk({ sub: 'tool:Shell', state_source: 'run' }), NOW)
    assert.equal(v.key, 'working')
    assert.equal(v.detail, '工具 Shell')
    assert.ok(v.title.includes('状态来自运行输出'))
    assert.ok(!stateView(mk({ sub: 'tool:Shell', state_source: 'hook' }), NOW).title.includes('运行输出'))
  })

  it('SSE 事件带来源就更新，没带就保留原来的', () => {
    const list = [mk({ id: 77, state_source: 'hook' })]
    const withSrc = applyStateEvent(list, { workflow_id: 'wf-a', identity_id: 1, session_id: 77, state: 'working', sub: 'thinking', source: 'run' }, new Date(NOW).toISOString())
    assert.equal(withSrc.list[0]?.state_source, 'run')
    const without = applyStateEvent(withSrc.list, { workflow_id: 'wf-a', identity_id: 1, session_id: 77, state: 'idle' }, new Date(NOW).toISOString())
    assert.equal(without.list[0]?.state_source, 'run')
    const fresh = applyStateEvent([mk({ id: 78 })], { workflow_id: 'wf-a', identity_id: 1, session_id: 78, state: 'idle' }, new Date(NOW).toISOString())
    assert.equal(fresh.list[0]?.state_source, undefined)
  })
})

describe('颜色类', () => {
  it('脉动只在要求时加', () => {
    assert.ok(dotToneClass('active', true).includes('animate-pulse'))
    assert.ok(!dotToneClass('active', false).includes('animate-pulse'))
  })
  it('每种语义都有颜色', () => {
    for (const tone of ['active', 'calm', 'attention', 'warn', 'danger', 'muted'] as const) {
      assert.ok(dotToneClass(tone, false).startsWith('bg-'), tone)
      assert.ok(textToneClass(tone).startsWith('text-'), tone)
    }
  })
})

describe('groupByWorkflow / pickSession', () => {
  it('按工作流分组并按最近动静排序，没有工作流的会话被丢弃', () => {
    const a1 = mk({ workflow_id: 'a', last_event_at: ago(10 * MIN) })
    const a2 = mk({ workflow_id: 'a', last_event_at: ago(MIN) })
    const b1 = mk({ workflow_id: 'b' })
    const none = mk({ workflow_id: '' })
    const g = groupByWorkflow([a1, none, b1, a2])
    assert.deepEqual(g.get('a')?.map(s => s.id), [a2.id, a1.id])
    assert.equal(g.get('b')?.length, 1)
    assert.equal(g.size, 2)
  })

  it('没有会话', () => {
    assert.deepEqual(pickSession(undefined), { primary: null, others: 0 })
    assert.deepEqual(pickSession([]), { primary: null, others: 0 })
  })

  it('优先展示最近有动静的活动会话，并数出其余活动会话', () => {
    const ended = mk({ state: 'ended', last_event_at: ago(MIN) })
    const live1 = mk({ state: 'working', last_event_at: ago(2 * MIN) })
    const live2 = mk({ state: 'idle', last_event_at: ago(5 * MIN) })
    const gone = mk({ state: 'offline', last_event_at: ago(300 * MIN) })
    const got = pickSession([ended, live1, live2, gone])
    assert.equal(got.primary?.id, live1.id)
    assert.equal(got.others, 1)
  })

  it('没有活动会话：未结束（离线）优先于已结束', () => {
    const ended = mk({ state: 'ended', last_event_at: ago(MIN) })
    const gone = mk({ state: 'offline', last_event_at: ago(300 * MIN) })
    const got = pickSession([ended, gone])
    assert.equal(got.primary?.id, gone.id)
    assert.equal(got.others, 0)
  })

  it('全部已结束：取最近的', () => {
    const e1 = mk({ state: 'ended', last_event_at: ago(MIN) })
    const e2 = mk({ state: 'ended', last_event_at: ago(9 * MIN) })
    assert.equal(pickSession([e1, e2]).primary?.id, e1.id)
  })
})

describe('applyStateEvent', () => {
  const NOW_ISO = new Date(NOW).toISOString()

  it('找不到会话：不改缓存，让调用方重新取', () => {
    const list = [mk()]
    const out = applyStateEvent(list, { workflow_id: 'wf-a', identity_id: 1, session_id: 9999, state: 'idle' }, NOW_ISO)
    assert.equal(out.found, false)
    assert.deepEqual(out.list.map(s => s.id), list.map(s => s.id))
  })

  it('就地更新状态、子状态、起始时间，并清掉旧的卡住标志', () => {
    const s = mk({ stalled_soft: true, approval_suspected: true, reason: 'quiet_soft', quiet_seconds: 700 })
    const out = applyStateEvent([s], { workflow_id: 'wf-a', identity_id: 1, session_id: s.id, state: 'working', sub: 'tool:Read', since: ago(1000) }, NOW_ISO)
    assert.equal(out.found, true)
    const n = out.list[0]
    assert.equal(n.state, 'working')
    assert.equal(n.sub, 'tool:Read')
    assert.equal(n.since, ago(1000))
    assert.equal(n.last_event_at, NOW_ISO)
    assert.equal(n.quiet_seconds, 0)
    assert.equal(n.stalled_soft, false)
    assert.equal(n.approval_suspected, false)
    assert.equal(n.reason, undefined)
    // 不改动传入的数组与对象
    assert.equal(s.stalled_soft, true)
  })

  it('事件没带 since 就用收到的时间', () => {
    const s = mk()
    const n = applyStateEvent([s], { workflow_id: 'wf-a', identity_id: 1, session_id: s.id, state: 'idle' }, NOW_ISO).list[0]
    assert.equal(n.since, NOW_ISO)
    assert.equal(n.sub, undefined)
  })

  it('人还没答复时，新事件不会让「等你回答」消失', () => {
    const s = mk({ state: 'waiting_you', waiting_you: true })
    const n = applyStateEvent([s], { workflow_id: 'wf-a', identity_id: 1, session_id: s.id, state: 'working', sub: 'thinking' }, NOW_ISO).list[0]
    assert.equal(n.state, 'waiting_you')
    assert.equal(n.waiting_you, true)
    assert.equal(n.base_state, 'working')
  })

  it('会话结束时「等你回答」随之结束；中断 / 出错也不叠加', () => {
    const s = mk({ state: 'waiting_you', waiting_you: true })
    for (const st of ['ended', 'aborted', 'error']) {
      const n = applyStateEvent([s], { workflow_id: 'wf-a', identity_id: 1, session_id: s.id, state: st }, NOW_ISO).list[0]
      assert.equal(n.state, st)
      assert.equal(n.waiting_you, false)
    }
  })
})

describe('basicFacts（S-1 四项）', () => {
  it('最后交互取各来源里最近的一个', () => {
    const f = basicFacts({
      session: mk({ last_event_at: ago(10 * MIN), last_mcp_at: ago(3 * MIN) }),
      identity: mkIdentity({ last_hook_at: ago(7 * MIN), last_mcp_at: ago(20 * MIN) }),
      items: [],
    })
    assert.equal(f.lastInteractionAt, ago(3 * MIN))
  })

  it('最后查询来自身份，会话上的也算', () => {
    assert.equal(basicFacts({ session: mk(), identity: mkIdentity({ last_query_at: ago(4 * MIN) }), items: [] }).lastQueryAt, ago(4 * MIN))
    assert.equal(basicFacts({ session: mk({ last_query_at: ago(2 * MIN) }), identity: null, items: [] }).lastQueryAt, ago(2 * MIN))
    assert.equal(basicFacts({ session: mk(), identity: null, items: [] }).lastQueryAt, null)
  })

  it('推送已收到 = 提醒与回执提示行里最近的；已读 = 取走全文的最近时间', () => {
    const items = [
      mkItem({ id: 1, last_notified_at: ago(9 * MIN), delivered_at: ago(8 * MIN), status: 'consumed', state: 'delivered' }),
      mkItem({ id: 2, receipt_hint_at: ago(2 * MIN), last_notified_at: ago(5 * MIN) }),
    ]
    const f = basicFacts({ session: null, identity: null, items })
    assert.equal(f.pushReceivedAt, ago(2 * MIN))
    assert.equal(f.readAt, ago(8 * MIN))
    assert.equal(f.queued, 1)
  })

  it('什么都没有时全部为空，不抛错', () => {
    const f = basicFacts({ session: null, identity: null, items: null })
    assert.deepEqual(f, { lastInteractionAt: null, lastQueryAt: null, pushReceivedAt: null, readAt: null, queued: 0 })
  })

  it('零值时间不算数', () => {
    const f = basicFacts({ session: mk({ last_event_at: '0001-01-01T00:00:00Z' }), identity: null, items: [] })
    assert.equal(f.lastInteractionAt, null)
  })
})

describe('factText', () => {
  it('从未发生写破折号', () => {
    assert.equal(factText(null, NOW), '—')
    assert.equal(factText('不是时间', NOW), '—')
  })
  it('发生过写多久以前', () => {
    assert.equal(factText(ago(3 * MIN), NOW), '3 分钟前')
    assert.equal(factText(ago(1000), NOW), '刚刚')
  })
})

describe('身份卡补充信息', () => {
  it('客户端', () => {
    assert.equal(clientText('cli', '2026.07.01'), 'Cursor CLI · 2026.07.01')
    assert.equal(clientText('ide', ''), 'Cursor IDE')
    assert.equal(clientText('unknown', undefined), '客户端未知')
    assert.equal(clientText('', '1.2'), '客户端未知 · 1.2')
  })

  it('本轮用时：进行中算到现在，结束后写上一轮', () => {
    const working = mk({ state: 'working', turn_started_at: ago(3 * MIN) })
    assert.equal(turnSpanText(working, NOW), '3 分钟')
    const idle = mk({ state: 'idle', turn_started_at: ago(10 * MIN), turn_ended_at: ago(4 * MIN) })
    assert.equal(turnSpanText(idle, NOW), '6 分钟（上一轮）')
    assert.equal(turnSpanText(mk({ state: 'idle' }), NOW), '')
    assert.equal(turnSpanText(null, NOW), '')
  })

  it('会话时长：已结束的算到结束时刻', () => {
    assert.equal(sessionSpanText(mk({ started_at: ago(90 * MIN) }), NOW), '1 小时 30 分')
    assert.equal(sessionSpanText(mk({ state: 'ended', started_at: ago(90 * MIN), ended_at: ago(30 * MIN) }), NOW), '1 小时')
    assert.equal(sessionSpanText(mk(), NOW), '')
  })

  it('徽标上的会话时长（Q63 / #582 / #601）：每个会话各自判断——状态来源是 hook 的不带，其他来源有就显示、没有就不占位', () => {
    const started = { started_at: ago(90 * MIN) }
    // 这个会话启用了 hook（状态来源是编辑器 hook）：徽标不带时长（时间都在身份卡里）
    assert.equal(chipSpanText(mk({ ...started, state_source: 'hook' }), NOW), '')
    assert.equal(chipSpanText(mk({ ...started, state_source: ' hook ' }), NOW), '', '来源前后的空白不影响判断')
    // 其他来源（终端运行输出 / 终端画面 / 旧数据没有来源）：维持原规则
    assert.equal(chipSpanText(mk({ ...started, state_source: 'run' }), NOW), '1 小时 30 分')
    assert.equal(chipSpanText(mk({ ...started, state_source: 'tui' }), NOW), '1 小时 30 分')
    assert.equal(chipSpanText(mk({ ...started, state_source: '' }), NOW), '1 小时 30 分')
    assert.equal(chipSpanText(mk({ ...started }), NOW), '1 小时 30 分')
    // 两个会话互不影响：同一时刻一个 hook 来源、一个终端来源，各按各的
    const a = mk({ ...started, state_source: 'hook' })
    const b = mk({ ...started, state_source: 'run' })
    assert.deepEqual([chipSpanText(a, NOW), chipSpanText(b, NOW)], ['', '1 小时 30 分'])
    // 非 hook 来源但没有开始时间：不占位；没有会话：不占位
    assert.equal(chipSpanText(mk({ state_source: 'run' }), NOW), '')
    assert.equal(chipSpanText(null, NOW), '')
    assert.equal(chipSpanText(undefined, NOW), '')
    // 已结束的非 hook 会话算到结束时刻（与 sessionSpanText 一致）
    assert.equal(chipSpanText(mk({ state: 'ended', started_at: ago(90 * MIN), ended_at: ago(30 * MIN), state_source: 'run' }), NOW), '1 小时')
  })

  it('会话编号只显示前 8 位', () => {
    assert.equal(shortConversationId('conv-0123456789'), 'conv-012')
    assert.equal(shortConversationId('abc'), 'abc')
    assert.equal(shortConversationId(undefined), '')
  })
})

describe('托管终端屏幕信号（M3.2）', () => {
  it('token 数的短写法', () => {
    assert.equal(tokensText(0), '0')
    assert.equal(tokensText(999), '999')
    assert.equal(tokensText(1000), '1k')
    assert.equal(tokensText(1234), '1.2k')
    assert.equal(tokensText(12_345), '12k')
    assert.equal(tokensText(1_500_000), '1.5M')
    assert.equal(tokensText(-1), '')
    assert.equal(tokensText(Number.NaN), '')
  })

  it('上下文占用的写法', () => {
    assert.equal(ctxText(4.4), '4.4%')
    assert.equal(ctxText(12), '12%')
    assert.equal(ctxText(12.04), '12%')
    assert.equal(ctxText(0), '0%')
    assert.equal(ctxText(100), '100%')
    assert.equal(ctxText(-1), '')
    assert.equal(ctxText(101), '')
  })

  it('没有信号 / 未识别 / 已识别', () => {
    assert.deepEqual(screenFacts(null), { tui: '', tokens: '', ctx: '', word: '', note: '' })
    assert.equal(screenFacts(mk()).tui, '')

    const un = screenFacts(mk({ tui: 'unrecognized' }))
    assert.equal(un.tui, 'unrecognized')
    assert.equal(un.tokens, '')
    assert.match(un.note, /没有验证过/)

    const ok = screenFacts(mk({ tui: 'ok', tokens_live: 1234, tokens_source: 'tui', ctx_pct: 8.25, live_word: 'Globbing' }))
    assert.deepEqual(ok, { tui: 'ok', tokens: '1.2k（屏幕）', ctx: '8.3%', word: 'Globbing', note: '' })

    // 只读到页脚、没有状态行：没有 token，仍然有上下文占用
    const footer = screenFacts(mk({ tui: 'ok', ctx_pct: 30 }))
    assert.equal(footer.tokens, '')
    assert.equal(footer.ctx, '30%')
  })

  it('终端冻结：硬卡住，文案说清楚是终端画面', () => {
    const v = stateView(mk({ stalled: true, reason: 'terminal_frozen', tui: 'ok' }), NOW)
    assert.equal(v.key, 'stalled')
    assert.equal(v.detail, '终端画面已冻结，没有任何输出')
  })

  it('token 计数停了但画面还在动：软标记，文案带理由', () => {
    const v = stateView(mk({ stalled_soft: true, reason: 'tokens_flat', tui: 'ok' }), NOW)
    assert.equal(v.key, 'working')
    assert.equal(v.flag, 'stalled_soft')
    assert.match(v.flagText, /token 计数停了/)
    assert.equal(v.tone, 'warn')
    // 普通的软标记文案不变
    assert.equal(stateView(mk({ stalled_soft: true, reason: 'quiet_soft' }), NOW).flagText, '可能卡住')
  })

  it('屏幕确认的审批等待沿用已有文案', () => {
    const v = stateView(mk({ state: 'waiting_approval', base_state: 'waiting_approval', tui: 'ok' }), NOW)
    assert.equal(v.key, 'waiting_approval')
    assert.equal(v.text, '等你批准')
  })
})
