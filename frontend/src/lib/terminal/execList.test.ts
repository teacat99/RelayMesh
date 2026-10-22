// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HistoryEntry, HostView, SessionInfo, ThreadView } from '../../api/terminal'
import {
  EXEC_FILTERS,
  archivedMatchCount,
  archivedStats,
  buildExecItems,
  dayLabel,
  filterExecItems,
  findBySid,
  findByThread,
  groupExecItems,
  historyReplayable,
  itemKey,
  runningCount,
  sortExecItems,
  threadItemKey,
} from './execList'

const NOW = new Date(2026, 9, 2, 15, 30).getTime() // 2026-10-02 15:30（本地时区）
const sec = (ms: number) => Math.floor(ms / 1000)

function sess(sid: string, over: Partial<SessionInfo> = {}): SessionInfo {
  return { sid, rows: 24, cols: 80, base: 0, end: 0, state: 'running', attached: 0, ...over }
}

function host(code: string, sessions: SessionInfo[], over: Partial<HostView> = {}): HostView {
  return {
    machine_code: code,
    remark: '',
    status: 'enabled',
    temporary: false,
    online: true,
    hostname: code.toLowerCase(),
    os: 'linux',
    arch: 'amd64',
    agent_version: '1.7.0',
    privilege: 'user',
    last_ip: '127.0.0.1',
    sessions,
    ...over,
  }
}

function past(sid: string, code: string, over: Partial<HistoryEntry> = {}): HistoryEntry {
  return { sid, machine_code: code, ...over }
}

function thread(key: string, target: string, over: Partial<ThreadView> = {}): ThreadView {
  return {
    key,
    target,
    credential_id: 7,
    credential_name: 'chatgpt-ops',
    total: 3,
    active: 0,
    awaiting: 0,
    first_at: '2026-10-02T09:00:00Z',
    last_at: '2026-10-02T10:00:00Z',
    last_run_id: 'r1',
    last_state: 'succeeded',
    last_title: '查看磁盘占用',
    ...over,
  }
}

describe('合成清单', () => {
  it('主机上的会话与已结束的历史合成一张清单，历史里仍在主机上的会话不重复出现', () => {
    const items = buildExecItems({
      hosts: [host('A', [sess('s1'), sess('s2', { state: 'exited' })])],
      history: { A: { sessions: [past('s2', 'A', { ended_reason: 'exit' }), past('s3', 'A', { ended_at: new Date(NOW - 3600_000).toISOString(), ended_reason: 'killed' })] } },
      pins: new Set(),
    })
    assert.deepEqual(items.map(i => i.sid).sort(), ['s1', 's2', 's3'])
    const s2 = items.find(i => i.sid === 's2')!
    assert.equal(s2.live, true)
    assert.equal(s2.running, false)
    assert.equal(s2.endedReason, 'exit', '主机列表里已退出的会话沿用历史里记录的结束原因')
    const s3 = items.find(i => i.sid === 's3')!
    assert.equal(s3.live, false)
    assert.equal(s3.endedReason, 'killed')
  })

  it('经网关打开的 SSH 会话显示目标主机的名字并注明网关；目标被删除或已结束的历史退回「SSH · 经 网关」', () => {
    const gw = host('GW', [sess('g1'), sess('x1', { shell: 'ssh', target: 'BOX', name: 'ops@box' }), sess('x2', { shell: 'ssh', target: 'GONE' })], { remark: '家里网关' })
    const box = host('BOX', [], { kind: 'ssh', remark: '构建机' })
    const items = buildExecItems({
      hosts: [gw, box],
      history: { GW: { sessions: [past('x3', 'GW', { shell: 'ssh', ended_reason: 'exit' }), past('g2', 'GW', { shell: '/bin/bash', ended_reason: 'exit' })] } },
      pins: new Set(),
    })
    const name = (sid: string) => items.find(i => i.sid === sid)?.hostName
    assert.equal(name('g1'), '家里网关', '本机 shell 照旧显示它所在的主机')
    assert.equal(name('x1'), '构建机 · 经 家里网关')
    assert.equal(name('x2'), 'SSH · 经 家里网关', '目标主机已不在列表里')
    assert.equal(name('x3'), 'SSH · 经 家里网关', '历史条目只记得它是 SSH 会话')
    assert.equal(name('g2'), '家里网关')
    // 会话仍然归属网关（附着、终止都要找网关），只是展示上写目标
    assert.equal(items.find(i => i.sid === 'x1')?.code, 'GW')
    // 搜索能按目标主机的名字命中
    assert.deepEqual(filterExecItems(items, 'all', '构建机').map(i => i.sid), ['x1'])
  })

  it('Hub 直连的 SSH 主机自己持有会话：直接显示这台主机的名字；它离线时不拿旧记录顶上（会话随连接一起结束了）', () => {
    const box = host('BOX', [sess('d1', { shell: 'ssh', name: 'ops@box' })], { kind: 'ssh', remark: '构建机' })
    const off = host('OFF', [], { kind: 'ssh', online: false, remark: '离线机' })
    const agentOff = host('AG', [], { online: false, remark: '离线 Agent' })
    const items = buildExecItems({
      hosts: [box, off, agentOff],
      history: {},
      pins: new Set(),
      lastKnown: { OFF: [sess('ghost', { shell: 'ssh' })], AG: [sess('still', {})] },
    })
    assert.equal(items.find(i => i.sid === 'd1')?.hostName, '构建机', '不是「SSH · 经 …」')
    assert.equal(items.find(i => i.sid === 'ghost'), undefined, 'SSH 主机离线后，它名下的旧会话记录不再顶上')
    assert.equal(items.find(i => i.sid === 'still')?.hostName, '离线 Agent', 'Agent 主机离线时会话仍在主机上，照旧保留')
  })

  it('只包含已启用的主机；离线主机的会话仍在清单里并标注主机离线', () => {
    const items = buildExecItems({
      hosts: [host('A', [sess('a1')], { online: false }), host('B', [sess('b1')], { status: 'disabled' })],
      history: {},
      pins: new Set(),
    })
    assert.deepEqual(items.map(i => i.sid), ['a1'])
    assert.equal(items[0].hostOnline, false)
  })

  it('最近活动时间：运行中取最后交互，已结束取结束时间，都没有则取创建时间', () => {
    const ended = new Date(NOW - 2 * 86400_000).toISOString()
    const items = buildExecItems({
      hosts: [host('A', [sess('run', { active: sec(NOW - 60_000), created: sec(NOW - 9e6) }), sess('fresh', { created: sec(NOW - 5000) }), sess('done', { state: 'exited', created: sec(NOW - 9e6) })])],
      history: { A: { sessions: [past('done', 'A', { ended_at: ended }), past('old', 'A', { started_at: sec(NOW - 5 * 86400_000) })] } },
      pins: new Set(),
    })
    const by = Object.fromEntries(items.map(i => [i.sid, i.lastActive]))
    assert.equal(by.run, sec(NOW - 60_000) * 1000)
    assert.equal(by.fresh, sec(NOW - 5000) * 1000)
    assert.equal(by.done, Date.parse(ended))
    assert.equal(by.old, sec(NOW - 5 * 86400_000) * 1000)
  })

  it('主机离线时沿用最近一次在线时的运行中会话，并标注主机离线；在线时不使用这份记录', () => {
    const lastKnown = { A: [sess('kept', { name: '长任务' })] }
    const offline = buildExecItems({ hosts: [host('A', [], { online: false })], history: {}, pins: new Set(), lastKnown })
    assert.deepEqual(offline.map(i => [i.sid, i.running, i.hostOnline]), [['kept', true, false]])
    assert.equal(offline[0].title, '长任务')
    const online = buildExecItems({ hosts: [host('A', [], { online: true })], history: {}, pins: new Set(), lastKnown })
    assert.deepEqual(online, [], '主机在线且没有会话：以主机当前报告的为准')
    const reported = buildExecItems({ hosts: [host('A', [sess('now')], { online: false })], history: {}, pins: new Set(), lastKnown })
    assert.deepEqual(reported.map(i => i.sid), ['now'], '主机自己有报告时不使用旧记录')
  })

  it('生命周期与置顶状态带进条目', () => {
    const [it1] = buildExecItems({
      hosts: [host('A', [sess('s1', { idle: 86400, idle_own: true, attached: 2, rec: 'on' })])],
      history: {},
      pins: new Set([itemKey('A', 's1')]),
    })
    assert.equal(it1.idle, 86400)
    assert.equal(it1.idleOwn, true)
    assert.equal(it1.attached, 2)
    assert.equal(it1.pinned, true)
    assert.equal(it1.rec, 'on')
  })

  it('历史条目是否可回放', () => {
    assert.equal(historyReplayable({ rec: 'on' }), true)
    assert.equal(historyReplayable({ rec: 'on', rec_purged: true }), false)
    assert.equal(historyReplayable({ rec: 'incognito' }), false)
    assert.equal(historyReplayable({ rec: 'off', rec_bytes: 10 }), true)
    assert.equal(historyReplayable({ rec: 'veto', rec_bytes: 0 }), false)
  })
})

describe('筛选与搜索', () => {
  const items = buildExecItems({
    hosts: [host('RM-AAAA', [sess('aaa111', { name: '部署' }), sess('bbb222', { name: '日志', state: 'exited' })], { remark: '生产机' }), host('RM-BBBB', [sess('ccc333')], { hostname: 'devbox' })],
    history: { 'RM-AAAA': { sessions: [past('ddd444', 'RM-AAAA', { name: '旧会话', ended_reason: 'exit' })] } },
    pins: new Set(),
  })

  it('筛选条件：全部 / 人工 / 模型 / 运行中 / 待批准 / 已结束', () => {
    assert.deepEqual(EXEC_FILTERS.map(f => f.id), ['all', 'human', 'model', 'running', 'awaiting', 'ended', 'archived'])
    assert.equal(filterExecItems(items, 'all', '').length, 4)
    assert.equal(filterExecItems(items, 'human', '').length, 4)
    assert.deepEqual(filterExecItems(items, 'running', '').map(i => i.sid).sort(), ['aaa111', 'ccc333'])
    assert.deepEqual(filterExecItems(items, 'ended', '').map(i => i.sid).sort(), ['bbb222', 'ddd444'])
  })

  it('搜索匹配会话名、主机名（备注优先）、会话 ID 前缀与机器码，不区分大小写', () => {
    const find = (q: string) => filterExecItems(items, 'all', q).map(i => i.sid).sort()
    assert.deepEqual(find('部署'), ['aaa111'])
    assert.deepEqual(find('生产'), ['aaa111', 'bbb222', 'ddd444'])
    assert.deepEqual(find('DEVBOX'), ['ccc333'])
    assert.deepEqual(find('ccc3'), ['ccc333'])
    assert.deepEqual(find('rm-bbbb'), ['ccc333'])
    assert.deepEqual(find('不存在'), [])
    assert.equal(find('  ').length, 4, '只有空白的搜索词等于不搜索')
  })

  it('会话 ID 只按前缀匹配，避免在 32 位十六进制里误命中', () => {
    assert.deepEqual(filterExecItems(items, 'all', '111').map(i => i.sid), [])
  })
})

describe('排序与分组', () => {
  it('置顶在前，其余按最近活动倒序，时间相同按 key 稳定排序', () => {
    const mk = (sid: string, lastActive: number, pinned = false) =>
      ({ key: itemKey('A', sid), sid, lastActive, pinned }) as unknown as Parameters<typeof sortExecItems>[0][number]
    const sorted = sortExecItems([mk('a', 10), mk('b', 30), mk('c', 20, true), mk('d', 30), mk('e', 5, true)])
    assert.deepEqual(sorted.map(i => i.sid), ['c', 'e', 'b', 'd', 'a'])
  })

  it('dayLabel：今天 / 昨天 / 本年 / 往年 / 无时间', () => {
    assert.equal(dayLabel(NOW - 3600_000, NOW), '今天')
    assert.equal(dayLabel(new Date(2026, 9, 1, 23, 59).getTime(), NOW), '昨天')
    assert.equal(dayLabel(new Date(2026, 8, 20, 9).getTime(), NOW), '9月20日')
    assert.equal(dayLabel(new Date(2025, 11, 31, 9).getTime(), NOW), '2025年12月31日')
    assert.equal(dayLabel(0, NOW), '更早')
  })

  it('分组：先置顶，再按日期；同一天的相邻条目合并', () => {
    const items = buildExecItems({
      hosts: [
        host('A', [
          sess('p', { active: sec(NOW - 3 * 86400_000) }),
          sess('t1', { active: sec(NOW - 1000) }),
          sess('t2', { active: sec(NOW - 5000_000) }),
          sess('y', { active: sec(NOW - 86400_000) }),
        ]),
      ],
      history: {},
      pins: new Set([itemKey('A', 'p')]),
    })
    const groups = groupExecItems(sortExecItems(items), NOW)
    assert.deepEqual(groups.map(g => g.label), ['置顶', '今天', '昨天'])
    assert.deepEqual(groups[0].items.map(i => i.sid), ['p'])
    assert.deepEqual(groups[1].items.map(i => i.sid), ['t1', 't2'])
  })

  it('空清单没有分组', () => {
    assert.deepEqual(groupExecItems([], NOW), [])
  })
})

describe('模型线程条目', () => {
  const hosts = [host('RM-AAAA', [sess('aaa111', { name: '部署' })], { remark: '生产机' }), host('RM-BBBB', [], { online: false, hostname: 'devbox' })]
  const threads = [
    thread('t1', 'RM-AAAA', { active: 2, awaiting: 1, thread: 'deploy', last_at: '2026-10-02T12:00:00Z' }),
    thread('t2', 'RM-BBBB', { credential_name: 'cursor-main', last_title: '重启服务', last_at: '2026-10-02T08:00:00Z' }),
    thread('t3', 'RM-GONE', {}),
    thread('t4', 'RM-AAAA', { workflow_id: 'wf-9', credential_name: 'claude-code' }),
  ]
  const items = buildExecItems({ hosts, history: {}, pins: new Set([threadItemKey('t2')]), threads, threadNames: { t4: '  我的线程 ' } })

  it('线程是清单里的普通条目；未知主机上的线程不显示', () => {
    assert.equal(items.filter(i => i.kind === 'model').length, 3)
    assert.equal(items.filter(i => i.kind === 'human').length, 1)
    assert.equal(findByThread(items, 't3'), undefined)
    const t1 = findByThread(items, 't1')!
    assert.equal(t1.key, 'r:t1')
    assert.equal(t1.code, 'RM-AAAA')
    assert.equal(t1.title, 'chatgpt-ops · 生产机')
    assert.equal(t1.hostName, '生产机')
    assert.equal(t1.running, true)
    assert.equal(t1.model?.awaiting, 1)
    assert.equal(t1.model?.summary, '#deploy · 1 条待批准 · 最近：查看磁盘占用')
    assert.equal(t1.lastActive, Date.parse('2026-10-02T12:00:00Z'))
  })

  it('混合令牌的线程以工作流命名；用户起的名字优先且不影响线程键', () => {
    const t4 = findByThread(items, 't4')!
    assert.equal(t4.title, '我的线程')
    assert.equal(t4.name, '我的线程')
    assert.equal(t4.model?.threadKey, 't4')
    assert.equal(t4.model?.workflowId, 'wf-9')
    const plain = buildExecItems({ hosts, history: {}, pins: new Set(), threads: [threads[3]] })
    assert.equal(plain[plain.length - 1].title, 'wf-9 · 生产机')
  })

  it('置顶状态按线程条目的 key 记录；主机离线会标出来', () => {
    assert.equal(findByThread(items, 't2')?.pinned, true)
    assert.equal(findByThread(items, 't2')?.hostOnline, false)
    assert.equal(findByThread(items, 't1')?.pinned, false)
  })

  it('筛选：模型 / 运行中 / 待批准 / 已结束', () => {
    const ids = (f: Parameters<typeof filterExecItems>[1]) => filterExecItems(items, f, '').map(i => i.model?.threadKey ?? i.sid).sort()
    assert.deepEqual(ids('model'), ['t1', 't2', 't4'])
    assert.deepEqual(ids('human'), ['aaa111'])
    assert.deepEqual(ids('running'), ['aaa111', 't1'])
    assert.deepEqual(ids('awaiting'), ['t1'])
    assert.deepEqual(ids('ended'), ['t2', 't4'])
  })

  it('搜索匹配调用方、线程标签、工作流与最近的命令', () => {
    const find = (q: string) => filterExecItems(items, 'all', q).map(i => i.model?.threadKey ?? i.sid).sort()
    assert.deepEqual(find('chatgpt'), ['t1'])
    assert.deepEqual(find('DEPLOY'), ['t1'])
    assert.deepEqual(find('wf-9'), ['t4'])
    assert.deepEqual(find('重启'), ['t2'])
    assert.deepEqual(find('devbox'), ['t2'])
    assert.deepEqual(find('磁盘'), ['t1', 't4'])
  })

  it('排序：置顶在前，其余按最近活动；人工会话的按会话 ID 查找不会命中线程', () => {
    const sorted = sortExecItems(items).map(i => i.key)
    assert.equal(sorted[0], 'r:t2')
    assert.equal(sorted[1], 'r:t1')
    assert.equal(findBySid(items, ''), undefined)
  })
})

describe('归档', () => {
  // A 机上有两个运行中会话和一个已退出会话，B 机只有历史；再加一个模型线程。
  function archiveFixture(archived: string[]) {
    return buildExecItems({
      hosts: [host('A', [sess('a1'), sess('a2'), sess('a3', { state: 'exited' })]), host('B', [])],
      history: { B: { sessions: [past('b1', 'B', { name: '部署回放' })] } },
      threads: [thread('0123456789abcdef01234567', 'A')],
      pins: new Set(),
      archived: new Set(archived),
    })
  }

  it('buildExecItems：人工会话、历史会话和模型线程都按 key 打上归档标记', () => {
    const items = archiveFixture(['A:a1', 'B:b1', threadItemKey('0123456789abcdef01234567')])
    const flag = (key: string) => items.find(i => i.key === key)?.archived
    assert.equal(flag('A:a1'), true)
    assert.equal(flag('A:a2'), false)
    assert.equal(flag('B:b1'), true)
    assert.equal(flag(threadItemKey('0123456789abcdef01234567')), true)
  })

  it('不传归档集合时一律视为未归档', () => {
    const items = buildExecItems({ hosts: [host('A', [sess('a1')])], history: {}, pins: new Set() })
    assert.equal(items.every(i => i.archived === false), true)
  })

  it('已归档的条目只出现在「已归档」筛选里，其余筛选（含全部、运行中）都不显示', () => {
    const items = archiveFixture(['A:a1', 'B:b1'])
    for (const f of EXEC_FILTERS) {
      const keys = filterExecItems(items, f.id, '').map(i => i.key)
      if (f.id === 'archived') {
        assert.deepEqual(keys.sort(), ['A:a1', 'B:b1'])
      } else {
        assert.equal(keys.includes('A:a1'), false, `${f.id} 不应包含已归档的 A:a1`)
        assert.equal(keys.includes('B:b1'), false, `${f.id} 不应包含已归档的 B:b1`)
      }
    }
    assert.deepEqual(
      filterExecItems(items, 'running', '').map(i => i.key).sort(),
      ['A:a2'],
    )
  })

  it('取消归档后条目回到原来的筛选里', () => {
    const before = filterExecItems(archiveFixture(['A:a1']), 'all', '').map(i => i.key)
    const after = filterExecItems(archiveFixture([]), 'all', '').map(i => i.key)
    assert.equal(before.includes('A:a1'), false)
    assert.equal(after.includes('A:a1'), true)
  })

  it('archivedStats 统计归档总数和其中仍在运行的数量', () => {
    const items = archiveFixture(['A:a1', 'A:a3', 'B:b1'])
    assert.deepEqual(archivedStats(items), { total: 3, running: 1 })
    assert.deepEqual(archivedStats(archiveFixture([])), { total: 0, running: 0 })
  })

  it('archivedMatchCount：搜索词在已归档里的命中数，空搜索词永远是 0', () => {
    const items = archiveFixture(['B:b1'])
    assert.equal(archivedMatchCount(items, '部署'), 1)
    assert.equal(archivedMatchCount(items, '不存在的词'), 0)
    assert.equal(archivedMatchCount(items, '   '), 0)
    // 搜索词只命中未归档条目时，已归档里没有匹配项。
    assert.equal(archivedMatchCount(archiveFixture([]), '部署'), 0)
  })

  it('已归档条目仍然计入运行中会话数，避免「看不见的会话还在跑」被低估', () => {
    const items = archiveFixture(['A:a1'])
    assert.equal(runningCount(items) >= 2, true)
  })
})

describe('其他', () => {
  it('runningCount 只数运行中的会话', () => {
    const items = buildExecItems({ hosts: [host('A', [sess('a'), sess('b', { state: 'exited' }), sess('c')])], history: {}, pins: new Set() })
    assert.equal(runningCount(items), 2)
  })

  it('findBySid 优先返回仍在主机上的条目', () => {
    const items = buildExecItems({
      hosts: [host('A', [sess('x')]), host('B', [])],
      history: { B: { sessions: [past('x', 'B'), past('y', 'B')] } },
      pins: new Set(),
    })
    assert.equal(findBySid(items, 'x')?.code, 'A')
    assert.equal(findBySid(items, 'y')?.code, 'B')
    assert.equal(findBySid(items, 'nope'), undefined)
  })
})
