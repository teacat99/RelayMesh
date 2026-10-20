// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { ExecItem, ModelInfo } from './execList'
import { MODEL_GROUP_ID, MODEL_GROUP_LABEL, archiveStatusText, archiveSummary, archivedItems, groupArchived } from './archivePage'

function item(key: string, over: Partial<ExecItem> = {}): ExecItem {
  const [code, sid = ''] = key.split(':')
  return {
    key,
    kind: 'human',
    code,
    sid,
    title: `会话 ${sid}`,
    name: '',
    hostName: `host-${code}`,
    hostOnline: true,
    live: true,
    running: true,
    attached: 0,
    idle: 0,
    idleOwn: false,
    lastActive: 1000,
    pinned: false,
    archived: true,
    replayable: true,
    ...over,
  }
}

function modelItem(key: string, over: Partial<ExecItem> = {}, m: Partial<ModelInfo> = {}): ExecItem {
  return item(key, {
    kind: 'model',
    code: '',
    sid: '',
    hostName: '',
    model: { threadKey: key.slice(2), credential: 'c', thread: 't', workflowId: '', active: 0, awaiting: 0, total: 4, lastTitle: '', lastState: '', summary: '', ...m },
    ...over,
  })
}

describe('archivedItems', () => {
  it('只留已归档的条目，未归档的不出现', () => {
    const list = [item('A:1'), item('A:2', { archived: false }), item('B:3')]
    assert.deepEqual(archivedItems(list).map(i => i.key), ['A:1', 'B:3'])
  })

  it('搜索规则与侧栏一致：匹配会话名与主机名，不区分大小写', () => {
    const list = [item('A:1', { title: 'Deploy Prod' }), item('B:2', { title: 'misc', hostName: 'Build-Box' })]
    assert.deepEqual(archivedItems(list, 'deploy').map(i => i.key), ['A:1'])
    assert.deepEqual(archivedItems(list, 'BUILD').map(i => i.key), ['B:2'])
    assert.deepEqual(archivedItems(list, 'nothing'), [])
  })
})

describe('groupArchived', () => {
  it('按主机分组：最近有活动的主机在前，组内最近活动在前', () => {
    const groups = groupArchived([
      item('A:1', { lastActive: 100 }),
      item('B:2', { lastActive: 500 }),
      item('A:3', { lastActive: 300 }),
    ])
    assert.deepEqual(groups.map(g => g.id), ['B', 'A'])
    assert.deepEqual(groups[1].items.map(i => i.key), ['A:3', 'A:1'])
    assert.equal(groups[0].label, 'host-B')
  })

  it('模型线程统一归在最后一组，即使它最近最活跃', () => {
    const groups = groupArchived([item('A:1', { lastActive: 10 }), modelItem('r:xyz', { lastActive: 9999 })])
    assert.deepEqual(groups.map(g => g.id), ['A', MODEL_GROUP_ID])
    assert.equal(groups[1].label, MODEL_GROUP_LABEL)
  })

  it('统计每组仍在运行的数量，并记下组内最近活动', () => {
    const [g] = groupArchived([item('A:1', { running: true, lastActive: 5 }), item('A:2', { running: false, lastActive: 9 })])
    assert.equal(g.running, 1)
    assert.equal(g.lastActive, 9)
  })

  it('时间相同按 key 保持稳定', () => {
    const groups = groupArchived([item('A:b', { lastActive: 1 }), item('A:a', { lastActive: 1 })])
    assert.deepEqual(groups[0].items.map(i => i.key), ['A:a', 'A:b'])
  })

  it('空输入得到空分组', () => {
    assert.deepEqual(groupArchived([]), [])
  })

  it('主机名为空时回落到机器码', () => {
    const [g] = groupArchived([item('RM-1:s', { hostName: '' })])
    assert.equal(g.label, 'RM-1')
  })
})

describe('archiveStatusText', () => {
  it('运行中 / 主机离线', () => {
    assert.equal(archiveStatusText(item('A:1')), '运行中')
    assert.equal(archiveStatusText(item('A:1', { hostOnline: false })), '运行中 · 主机离线')
  })

  it('已结束：仍在主机列表里是「已退出」，否则给出结束原因', () => {
    assert.equal(archiveStatusText(item('A:1', { running: false, live: true })), '已退出')
    assert.match(archiveStatusText(item('A:1', { running: false, live: false, endedReason: 'holder_stopped' })), /守护进程/)
  })

  it('模型线程：摘要，并提示待批准数量', () => {
    assert.equal(archiveStatusText(modelItem('r:a', {}, { summary: 'ops · 最近 ls' })), 'ops · 最近 ls')
    assert.equal(archiveStatusText(modelItem('r:b', {}, { summary: '', total: 7 })), '共 7 次调用')
    assert.equal(archiveStatusText(modelItem('r:c', {}, { summary: 'x', awaiting: 2 })), 'x · 2 个待批准')
  })
})

describe('archiveSummary', () => {
  it('汇总总数、运行中、人工与模型的数量', () => {
    const s = archiveSummary([item('A:1'), item('A:2', { running: false }), modelItem('r:z')])
    assert.deepEqual(s, { total: 3, running: 2, human: 2, model: 1 })
  })

  it('空列表全为 0', () => {
    assert.deepEqual(archiveSummary([]), { total: 0, running: 0, human: 0, model: 0 })
  })
})
