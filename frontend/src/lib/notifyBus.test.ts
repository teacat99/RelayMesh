// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  AGGREGATE_WINDOW_MS,
  DEFAULT_REQUEST_BODY,
  SEEN_TTL_MS,
  TAB_STATE_TTL_MS,
  badgedTitle,
  createAggregator,
  createSeenIds,
  deepLinkTarget,
  isDeepLinkPending,
  isForeground,
  notificationTag,
  parseNotifyPayload,
  pickDeepLinkSession,
  resolveDeepLinkTarget,
  planPresentation,
  presentNotice,
  safeDeepLink,
  shouldSuppress,
  stripBadge,
  viewedConversation,
  type NotifyPayload,
  type PlanInput,
  type TabState,
} from './notifyBus'

const NOW = 1_800_000_000_000

function payload(over: Partial<NotifyPayload> = {}): NotifyPayload {
  return {
    id: 'txt:s1',
    kind: 'agent_text',
    conversation: 'wf:demo',
    workflow_id: 'demo',
    session_id: 's1',
    sender: '后端组',
    title: 'RelayMesh',
    preview: '第三步完成：构建与测试都通过了',
    deep_link: '/?wf=demo&s=s1',
    mention: false,
    created_at: '2026-10-14T05:00:00Z',
    penetrate: false,
    sound: true,
    badge: true,
    ...over,
  }
}

function tab(over: Partial<TabState> = {}): TabState {
  return { tabId: 't1', focused: true, visible: true, conversation: 'wf:demo', at: NOW, ...over }
}

describe('safeDeepLink', () => {
  it('只接受站内路径', () => {
    assert.equal(safeDeepLink('/?wf=a&s=b'), '/?wf=a&s=b')
    assert.equal(safeDeepLink('/docs/x?y=1'), '/docs/x?y=1')
    assert.equal(safeDeepLink('  /exec  '), '/exec')
  })

  it('站外、协议相对、反斜杠、空白与控制字符、超长、非字符串：一律当作没有', () => {
    for (const bad of ['https://evil.com/', '//evil.com/x', '/\\evil', '/a b', '/a\nb', '/a\u0000b', 'relative', '', '  ', 42, null, undefined, {}]) {
      assert.equal(safeDeepLink(bad), '', JSON.stringify(bad))
    }
    assert.equal(safeDeepLink('/' + 'a'.repeat(512)), '')
    assert.equal(safeDeepLink('/' + 'a'.repeat(511)).length, 512)
  })
})

describe('parseNotifyPayload', () => {
  it('完整载荷', () => {
    const p = parseNotifyPayload({ ...payload(), mention: true, penetrate: true })
    assert.deepEqual(p, { ...payload(), mention: true, penetrate: true })
  })

  it('缺 id / 种类、超长 id、非对象：无效', () => {
    assert.equal(parseNotifyPayload(null), null)
    assert.equal(parseNotifyPayload('x'), null)
    assert.equal(parseNotifyPayload({ kind: 'agent_text' }), null)
    assert.equal(parseNotifyPayload({ id: 'a' }), null)
    assert.equal(parseNotifyPayload({ id: 'a'.repeat(201), kind: 'agent_text' }), null)
  })

  it('字段类型不对：按空值 / false 处理，且深链会被净化', () => {
    const p = parseNotifyPayload({ id: 'x', kind: 'agent_text', preview: 7, penetrate: 'yes', sound: 1, badge: null, deep_link: 'https://evil.com' })
    assert.ok(p)
    assert.equal(p.preview, '')
    assert.equal(p.penetrate, false)
    assert.equal(p.sound, false)
    assert.equal(p.badge, false)
    assert.equal(p.deep_link, '')
  })
})

describe('presentNotice', () => {
  it('新反馈请求沿用升级前的文案', () => {
    const p = payload({ kind: 'blocking_feedback', preview: '请确认方案' })
    assert.deepEqual(presentNotice(p), { title: '新反馈请求', body: '请确认方案' })
    assert.deepEqual(presentNotice(payload({ kind: 'blocking_feedback', preview: '  ' })), { title: '新反馈请求', body: DEFAULT_REQUEST_BODY })
  })

  it('agent_text：会话名 · 发送者；发送者缺失或与会话名相同时只写会话名', () => {
    assert.deepEqual(presentNotice(payload()), { title: 'RelayMesh · 后端组', body: '第三步完成：构建与测试都通过了' })
    assert.equal(presentNotice(payload({ sender: '' })).title, 'RelayMesh')
    assert.equal(presentNotice(payload({ sender: ' relaymesh ' })).title, 'RelayMesh')
    assert.equal(presentNotice(payload({ title: '', sender: '' })).title, 'RelayMesh')
    assert.equal(presentNotice(payload({ preview: '' })).body, '有新消息')
  })

  it('agent_text 合并：N 条新消息 + 最新一条', () => {
    assert.equal(presentNotice(payload(), 5).body, '5 条新消息\n最新：第三步完成：构建与测试都通过了')
    assert.equal(presentNotice(payload({ preview: '' }), 3).body, '3 条新消息')
    assert.equal(presentNotice(payload(), 1).body, '第三步完成：构建与测试都通过了')
  })

  it('其余种类各有标题与兜底正文', () => {
    assert.deepEqual(presentNotice(payload({ kind: 'ask_card', preview: '先做 A 还是 B？' })), { title: 'RelayMesh · 待确认', body: '先做 A 还是 B？' })
    assert.equal(presentNotice(payload({ kind: 'ask_card', preview: '' })).body, '有新的待确认提问')
    assert.deepEqual(presentNotice(payload({ kind: 'mention', preview: '' })), { title: 'RelayMesh · @你', body: '有人 @ 了你' })
    assert.deepEqual(presentNotice(payload({ kind: 'approval_waiting', preview: '' })), { title: 'RelayMesh · 待批准', body: '终端在等你批准' })
    assert.deepEqual(presentNotice(payload({ kind: 'guard_alert', title: '夜间回归', preview: '意外结束：出错中断' })), { title: '终端守护 · 夜间回归', body: '意外结束：出错中断' })
    assert.deepEqual(presentNotice(payload({ kind: 'something_new', preview: '' })), { title: 'RelayMesh', body: '有新通知' })
  })
})

describe('notificationTag / createAggregator', () => {
  it('非穿透类按会话合并；穿透类每条独立', () => {
    assert.equal(notificationTag(payload()), 'conv:wf:demo')
    assert.equal(notificationTag(payload({ id: 'txt:s2' })), 'conv:wf:demo')
    assert.equal(notificationTag(payload({ kind: 'mention', penetrate: true, id: 'm1' })), 'evt:m1')
    assert.equal(notificationTag(payload({ kind: 'blocking_feedback', penetrate: true, id: 'blk:s1' })), 'evt:blk:s1')
    assert.equal(notificationTag(payload({ conversation: '', id: 'x9' })), 'evt:x9')
  })

  it('窗口内累计，窗口外重新计数，不同标签互不影响', () => {
    const agg = createAggregator()
    assert.equal(agg.bump('conv:a', NOW), 1)
    assert.equal(agg.bump('conv:a', NOW + 1_000), 2)
    assert.equal(agg.bump('conv:b', NOW + 2_000), 1)
    assert.equal(agg.bump('conv:a', NOW + 3_000), 3)
    // 距上一条（NOW+3000）超过窗口：重新从 1 开始
    assert.equal(agg.bump('conv:a', NOW + 3_000 + AGGREGATE_WINDOW_MS + 1), 1)
  })

  it('持续有消息时窗口顺延（以最近一条计），reset 后重新计数', () => {
    const agg = createAggregator(10_000)
    assert.equal(agg.bump('t', 0), 1)
    assert.equal(agg.bump('t', 9_000), 2)
    assert.equal(agg.bump('t', 18_000), 3)
    agg.reset('t')
    assert.equal(agg.bump('t', 19_000), 1)
    agg.bump('u', 19_000)
    agg.reset()
    assert.equal(agg.size(), 0)
  })

  it('条目数有上限，不会无限增长', () => {
    const agg = createAggregator(1_000_000)
    for (let i = 0; i < 500; i++) agg.bump(`t${i}`, NOW + i)
    assert.ok(agg.size() <= 200, `size=${agg.size()}`)
  })
})

describe('createSeenIds', () => {
  it('同一个 id 只放行一次，窗口过后可再次放行', () => {
    const seen = createSeenIds()
    assert.equal(seen.claim('a', NOW), true)
    assert.equal(seen.claim('a', NOW + 1), false)
    assert.equal(seen.claim('b', NOW + 2), true)
    assert.equal(seen.claim('a', NOW + SEEN_TTL_MS + 1), true)
  })

  it('容量封顶；先淘汰过期的，再淘汰最旧的', () => {
    const seen = createSeenIds(1_000, 4)
    for (let i = 0; i < 4; i++) assert.equal(seen.claim(`k${i}`, NOW + i), true)
    assert.equal(seen.size(), 4)
    assert.equal(seen.claim('k4', NOW + 10), true)
    assert.ok(seen.size() <= 4)
    // 最旧的 k0 被淘汰，所以可以再次放行；仍在窗口里的 k4 不行
    assert.equal(seen.claim('k0', NOW + 11), true)
    assert.equal(seen.claim('k4', NOW + 12), false)
  })
})

describe('标题角标', () => {
  it('badgedTitle / stripBadge', () => {
    assert.equal(badgedTitle('RelayMesh', 0), 'RelayMesh')
    assert.equal(badgedTitle('RelayMesh', 3), '(3) RelayMesh')
    assert.equal(badgedTitle('RelayMesh', 150), '(99+) RelayMesh')
    assert.equal(badgedTitle('(3) RelayMesh', 5), '(5) RelayMesh') // 不会叠加
    assert.equal(badgedTitle('(3) RelayMesh', 0), 'RelayMesh')
    assert.equal(stripBadge('(99+) RelayMesh · x'), 'RelayMesh · x')
    assert.equal(stripBadge('(不是角标) RelayMesh'), '(不是角标) RelayMesh')
  })
})

describe('前台抑制', () => {
  it('聚焦、可见、正在看这个会话：抑制非穿透类', () => {
    assert.equal(isForeground([tab()], 'wf:demo', NOW), true)
    assert.equal(shouldSuppress(payload(), [tab()], NOW), true)
  })

  it('穿透类永不抑制', () => {
    assert.equal(shouldSuppress(payload({ kind: 'mention', penetrate: true }), [tab()], NOW), false)
    assert.equal(shouldSuppress(payload({ kind: 'blocking_feedback', penetrate: true }), [tab()], NOW), false)
  })

  it('没聚焦 / 不可见 / 在看别的会话 / 状态过期 / 会话键为空：都不算前台', () => {
    assert.equal(isForeground([tab({ focused: false })], 'wf:demo', NOW), false)
    assert.equal(isForeground([tab({ visible: false })], 'wf:demo', NOW), false)
    assert.equal(isForeground([tab({ conversation: 'wf:other' })], 'wf:demo', NOW), false)
    assert.equal(isForeground([tab({ at: NOW - TAB_STATE_TTL_MS - 1 })], 'wf:demo', NOW), false)
    assert.equal(isForeground([tab({ at: NOW - TAB_STATE_TTL_MS })], 'wf:demo', NOW), true)
    assert.equal(isForeground([tab({ conversation: '' })], '', NOW), false)
    assert.equal(isForeground([], 'wf:demo', NOW), false)
  })

  it('任意一个标签页满足即可（多标签汇总）', () => {
    const states = [tab({ tabId: 'a', focused: false }), tab({ tabId: 'b', conversation: 'wf:other' }), tab({ tabId: 'c' })]
    assert.equal(isForeground(states, 'wf:demo', NOW), true)
  })
})

describe('planPresentation', () => {
  const base = (over: Partial<PlanInput> = {}): PlanInput => ({
    payload: payload(),
    isLeader: true,
    selfFocused: false,
    states: [],
    now: NOW,
    soundOn: true,
    desktopReady: true,
    ...over,
  })

  it('leader、后台标签、系统通知可用：响铃 + 系统通知 + 角标，没有页内提示', () => {
    assert.deepEqual(planPresentation(base()), { suppressed: false, badge: true, sound: true, system: true, toast: false, request: false })
  })

  it('非 leader：不响铃不弹系统通知，但角标照常', () => {
    const plan = planPresentation(base({ isLeader: false }))
    assert.deepEqual(plan, { suppressed: false, badge: true, sound: false, system: false, toast: false, request: false })
  })

  it('本机关了提示音：不响铃，系统通知照旧', () => {
    const plan = planPresentation(base({ soundOn: false }))
    assert.equal(plan.sound, false)
    assert.equal(plan.system, true)
  })

  it('服务端说不响铃 / 不计角标：照办', () => {
    const plan = planPresentation(base({ payload: payload({ sound: false, badge: false }) }))
    assert.equal(plan.sound, false)
    assert.equal(plan.badge, false)
  })

  it('聚焦的标签页不计角标；系统通知不可用时回退页内提示', () => {
    const plan = planPresentation(base({ selfFocused: true, desktopReady: false, states: [tab({ conversation: 'wf:other' })] }))
    assert.deepEqual(plan, { suppressed: false, badge: false, sound: true, system: false, toast: true, request: false })
  })

  it('聚焦但系统通知可用：不重复弹页内提示', () => {
    assert.equal(planPresentation(base({ selfFocused: true })).toast, false)
  })

  it('被前台抑制：什么都不做', () => {
    const plan = planPresentation(base({ selfFocused: true, states: [tab()] }))
    assert.deepEqual(plan, { suppressed: true, badge: false, sound: false, system: false, toast: false, request: false })
  })

  it('新反馈请求：穿透、驱动跨项目提醒，不弹页内提示（界面上本来就看得见）', () => {
    const req = payload({ kind: 'blocking_feedback', penetrate: true })
    const plan = planPresentation(base({ payload: req, selfFocused: true, desktopReady: false, states: [tab()] }))
    assert.deepEqual(plan, { suppressed: false, badge: false, sound: true, system: false, toast: false, request: true })
  })

  it('穿透类即使正在看这个会话也照常呈现', () => {
    const mention = payload({ kind: 'mention', penetrate: true })
    const plan = planPresentation(base({ payload: mention, selfFocused: true, states: [tab()] }))
    assert.equal(plan.suppressed, false)
    assert.equal(plan.sound, true)
    assert.equal(plan.system, true)
  })
})

describe('会话与深链', () => {
  it('viewedConversation：只有在 home 路由且选中了工作流才有', () => {
    assert.equal(viewedConversation('home', 'demo'), 'wf:demo')
    assert.equal(viewedConversation('home', ' demo '), 'wf:demo')
    assert.equal(viewedConversation('home', ''), '')
    assert.equal(viewedConversation('home', undefined), '')
    assert.equal(viewedConversation('work', 'demo'), '')
    assert.equal(viewedConversation('exec', 'demo'), '')
    assert.equal(viewedConversation(undefined, 'demo'), '')
    assert.equal(viewedConversation(Symbol('x'), 'demo'), '')
    // 群聊页：看的是群，不是侧栏里选中的工作流
    assert.equal(viewedConversation('group', 'demo', 'grp:ab12cd34'), 'grp:ab12cd34')
    assert.equal(viewedConversation('group', 'demo', ['grp:ab12cd34']), 'grp:ab12cd34')
    assert.equal(viewedConversation('group', 'demo', undefined), '')
    assert.equal(viewedConversation('group', 'demo', 'wf:demo'), '')
  })

  it('deepLinkTarget：取 wf / s / q，数组取第一个，缺失为空串；q 只认 Q+整数', () => {
    assert.deepEqual(deepLinkTarget({ wf: 'demo', s: 's1' }), { wf: 'demo', sid: 's1', q: '' })
    assert.deepEqual(deepLinkTarget({ wf: [' demo ', 'x'], s: ['s1'] }), { wf: 'demo', sid: 's1', q: '' })
    assert.deepEqual(deepLinkTarget({ wf: null, s: 3 }), { wf: '', sid: '', q: '' })
    assert.deepEqual(deepLinkTarget({}), { wf: '', sid: '', q: '' })
    assert.deepEqual(deepLinkTarget(null), { wf: '', sid: '', q: '' })
    assert.deepEqual(deepLinkTarget({ wf: 'demo', q: 'Q23' }), { wf: 'demo', sid: '', q: 'Q23' })
    assert.deepEqual(deepLinkTarget({ wf: 'demo', q: [' Q7 ', 'Q8'] }), { wf: 'demo', sid: '', q: 'Q7' })
    for (const bad of ['q23', 'Q0', 'Q05', 'Q', 'Q1234567', 'Q2x', '23', '<script>', 3, null]) {
      assert.equal(deepLinkTarget({ wf: 'demo', q: bad }).q, '', String(bad))
    }
  })

  it('isDeepLinkPending：只有 home 路由且带 wf 或 s 才算「深链还没处理完」（#169）', () => {
    assert.equal(isDeepLinkPending('home', { wf: 'demo' }), true)
    assert.equal(isDeepLinkPending('home', { s: 'sess-1' }), true)
    assert.equal(isDeepLinkPending('home', { wf: 'demo', s: 'sess-1', q: 'Q23' }), true)
    assert.equal(isDeepLinkPending('home', { wf: [' demo '] }), true)
    // 没有目标、或只有 q（没有工作流 / 会话可打开，openDeepLink 同样直接返回）
    assert.equal(isDeepLinkPending('home', {}), false)
    assert.equal(isDeepLinkPending('home', { q: 'Q23' }), false)
    assert.equal(isDeepLinkPending('home', { wf: '  ', s: '' }), false)
    assert.equal(isDeepLinkPending('home', null), false)
    // 别的页面带着同名参数不是深链（openDeepLink 只在 home 路由处理）
    for (const name of ['work', 'docs', 'group', 'projects', 'settings', undefined, null, Symbol('x')]) {
      assert.equal(isDeepLinkPending(name, { wf: 'demo' }), false, String(name))
    }
  })

  it('pickDeepLinkSession：精确会话优先；否则取该工作流里最新的一条', () => {
    const list = [
      { session_id: 's1', workflow_id: 'demo', created_at: '2026-10-14T01:00:00Z' },
      { session_id: 's2', workflow_id: 'demo', created_at: '2026-10-14T03:00:00Z' },
      { session_id: 's3', workflow_id: 'demo', created_at: '2026-10-14T02:00:00Z', updated_at: '2026-10-14T04:00:00Z' },
      { session_id: 'solo', created_at: '2026-10-14T05:00:00Z' }, // 没有工作流编号：以自己的编号作为工作流
      { session_id: 'o1', workflow_id: 'other', created_at: '2026-10-14T06:00:00Z' },
    ]
    assert.equal(pickDeepLinkSession(list, 'demo', 's1')?.session_id, 's1')
    assert.equal(pickDeepLinkSession(list, 'demo', '')?.session_id, 's3') // 更新时间最新
    assert.equal(pickDeepLinkSession(list, 'demo', 'gone')?.session_id, 's3') // 会话不在了，退到该工作流最新
    assert.equal(pickDeepLinkSession(list, '', 'o1')?.session_id, 'o1')
    assert.equal(pickDeepLinkSession(list, 'solo', '')?.session_id, 'solo')
    assert.equal(pickDeepLinkSession(list, 'nope', 'nope'), null)
    assert.equal(pickDeepLinkSession(list, '', ''), null)
    assert.equal(pickDeepLinkSession([], 'demo', 's1'), null)
  })

  describe('resolveDeepLinkTarget：由便宜到贵逐级兜底，每级最多发生一次（#177）', () => {
    const S = (id: string, wf: string, at: string) => ({ session_id: id, workflow_id: wf, created_at: at })
    const fixture = (initial: ReturnType<typeof S>[], afterRefresh: ReturnType<typeof S>[], full: Record<string, ReturnType<typeof S>[]> = {}) => {
      let list = initial
      const calls = { refresh: 0, load: [] as string[] }
      return {
        calls,
        loaders: {
          list: () => list,
          refreshList: async () => {
            calls.refresh++
            list = afterRefresh
          },
          loadWorkflow: async (wf: string) => {
            calls.load.push(wf)
            return full[wf] ?? []
          },
        },
      }
    }

    it('已有列表里就有：不刷新、不取整表', async () => {
      const f = fixture([S('a1', 'wf-a', '2026-10-14T01:00:00Z')], [])
      const t = await resolveDeepLinkTarget(f.loaders, 'wf-a', '')
      assert.equal(t?.session_id, 'a1')
      assert.deepEqual(f.calls, { refresh: 0, load: [] })
    })

    it('刷新简略列表后找到：只刷新一次，不取整表', async () => {
      const f = fixture([], [S('a1', 'wf-a', '2026-10-14T01:00:00Z')])
      const t = await resolveDeepLinkTarget(f.loaders, 'wf-a', '')
      assert.equal(t?.session_id, 'a1')
      assert.deepEqual(f.calls, { refresh: 1, load: [] })
    })

    it('简略列表里始终没有该工作流：才取这个工作流的完整列表，且只取一次、走注入的加载器（不直连接口）', async () => {
      const f = fixture([], [S('o1', 'wf-other', '2026-10-14T01:00:00Z')], {
        'wf-old': [S('old1', 'wf-old', '2026-09-01T01:00:00Z'), S('old2', 'wf-old', '2026-09-02T01:00:00Z')],
      })
      const t = await resolveDeepLinkTarget(f.loaders, 'wf-old', 'old1')
      assert.equal(t?.session_id, 'old1')
      assert.deepEqual(f.calls, { refresh: 1, load: ['wf-old'] })
    })

    it('没点名工作流（只有会话号）且列表里找不到：不会去取任何整表', async () => {
      const f = fixture([], [S('o1', 'wf-other', '2026-10-14T01:00:00Z')])
      const t = await resolveDeepLinkTarget(f.loaders, '', 'ghost')
      assert.equal(t, null)
      assert.deepEqual(f.calls, { refresh: 1, load: [] })
    })

    it('整表里也没有：返回 null，不会抛错', async () => {
      const f = fixture([], [], { 'wf-gone': [] })
      assert.equal(await resolveDeepLinkTarget(f.loaders, 'wf-gone', 'x'), null)
      assert.deepEqual(f.calls, { refresh: 1, load: ['wf-gone'] })
    })
  })
})
