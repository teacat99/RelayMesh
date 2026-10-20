// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  ARCHIVE_LIMIT,
  HISTORY_LIMIT,
  PRESET_SCOPES,
  archiveReasonText,
  archiveReplaced,
  clearArchive,
  deleteArchived,
  entrySignature,
  keyOf,
  normalizeVersions,
  reconcile,
  reenableArchived,
  resolveConflicts,
  uniqueId,
  type PresetConflict,
  type PresetVersions,
  type ScopeItem,
  type ScopeLists,
} from './presetVersions'

const NOW = '2026-10-08T12:00:00.000Z'

const emptyLists = (): ScopeLists => {
  const l = {} as ScopeLists
  for (const s of PRESET_SCOPES) l[s] = []
  return l
}

const preset = (id: string, content: string, extra: Record<string, unknown> = {}): ScopeItem => ({
  id,
  title: `标题-${id}`,
  content,
  allowAppend: false,
  appendTitle: `标题-${id}`,
  ...extra,
})

const phase = (id: string, prompt: string): ScopeItem => ({ id, label: `阶段-${id}`, description: '描述', prompt })

const listsOf = (patch: Partial<ScopeLists>): ScopeLists => ({ ...emptyLists(), ...patch })

describe('entrySignature', () => {
  it('同样的内容同样的签名，改了任何一个参与比较的字段就变', () => {
    const a = preset('qp-1', '继续')
    assert.equal(entrySignature('general', a), entrySignature('general', { ...a }))
    assert.notEqual(entrySignature('general', a), entrySignature('general', { ...a, content: '继续做' }))
    assert.notEqual(entrySignature('general', a), entrySignature('general', { ...a, title: '别的标题' }))
    assert.notEqual(entrySignature('general', a), entrySignature('general', { ...a, allowAppend: true }))
    assert.notEqual(entrySignature('general', a), entrySignature('general', { ...a, appendTitle: '别的' }))
    assert.notEqual(entrySignature('general', a), entrySignature('general', { ...a, isMutex: true }))
  })

  it('运行时状态（追加勾选）和字段书写顺序不算内容', () => {
    const a = preset('qp-1', '继续', { isMutex: true, mutexTargets: ['qp-3', 'qp-2'] })
    const b = { mutexTargets: ['qp-2', 'qp-3'], isMutex: true, appendTitle: '标题-qp-1', allowAppend: false, content: '继续', title: '标题-qp-1', id: 'qp-1', isAppendActive: true }
    assert.equal(entrySignature('general', a), entrySignature('general', b))
  })

  it('缺省与空值等价：没有 appendTitle / isMutex 的旧数据和显式写成假值的是同一内容', () => {
    const legacy = { id: 'x', title: 't', content: 'c', allowAppend: false } as ScopeItem
    const explicit = { id: 'x', title: 't', content: 'c', allowAppend: false, appendTitle: '', isMutex: false, mutexTargets: [] } as ScopeItem
    assert.equal(entrySignature('general', legacy), entrySignature('general', explicit))
  })

  it('阶段只看 label / description / prompt', () => {
    const p = phase('dev', '开发')
    assert.equal(entrySignature('phase', p), entrySignature('phase', { ...p, extra: 1 }))
    assert.notEqual(entrySignature('phase', p), entrySignature('phase', { ...p, prompt: '开发2' }))
  })
})

describe('reconcile', () => {
  it('头一次对账：现在的内容就是用户的选择，只记基线，不打扰', () => {
    const shipped = listsOf({ general: [preset('qp-1', '新版')] })
    const mine = listsOf({ general: [preset('qp-1', '用户改过的')] })
    const r = reconcile({ shipped, mine, now: NOW })
    assert.equal(r.conflicts.length, 0)
    assert.equal(r.upgraded, 0)
    assert.deepEqual(r.lists.general, mine.general)
    assert.equal(r.versions.baseline[keyOf('general', 'qp-1')], entrySignature('general', shipped.general[0]))
    assert.equal(r.changed, true)
  })

  it('出厂文案没变：用户的改动原样保留，也没有任何变化需要落盘', () => {
    const shipped = listsOf({ general: [preset('qp-1', '出厂')] })
    const mine = listsOf({ general: [preset('qp-1', '用户改过的')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', shipped.general[0]) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.changed, false)
    assert.equal(r.conflicts.length, 0)
    assert.deepEqual(r.lists, mine)
  })

  it('用户没改过、出厂文案变了：自动换成新版，旧版存进留存区，运行时勾选状态保留', () => {
    const oldShipped = preset('qp-1', '旧出厂')
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const mine = listsOf({ general: [{ ...oldShipped, isAppendActive: true }] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.upgraded, 1)
    assert.equal(r.conflicts.length, 0)
    assert.equal(r.lists.general[0].content, '新出厂')
    assert.equal(r.lists.general[0].isAppendActive, true)
    assert.equal(r.versions.baseline[keyOf('general', 'qp-1')], entrySignature('general', shipped.general[0]))
    assert.equal(r.versions.archive.length, 1)
    assert.equal(r.versions.archive[0].kind, 'old')
    assert.equal(r.versions.archive[0].reason, 'auto-upgrade')
    assert.equal(r.versions.archive[0].payload.content, '旧出厂')
    assert.equal('isAppendActive' in r.versions.archive[0].payload, false)
    assert.equal(r.changed, true)
  })

  it('用户改过、出厂文案也变了：冲突，先不动任何内容，也不动基线', () => {
    const oldShipped = preset('qp-1', '旧出厂')
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const mine = listsOf({ general: [preset('qp-1', '我改的')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.conflicts.length, 1)
    const c = r.conflicts[0]
    assert.equal(c.scope, 'general')
    assert.equal(c.id, 'qp-1')
    assert.equal(c.mineText, '我改的')
    assert.equal(c.latestText, '新出厂')
    assert.deepEqual(r.lists, mine)
    assert.deepEqual(r.versions, versions)
    assert.equal(r.changed, false)
  })

  it('我的已经等于最新（比如用户点过「恢复默认」）：不是冲突，基线对齐到最新', () => {
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const mine = listsOf({ general: [preset('qp-1', '新出厂')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: 'stale-sig' }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.conflicts.length, 0)
    assert.equal(r.versions.baseline[keyOf('general', 'qp-1')], entrySignature('general', shipped.general[0]))
    assert.equal(r.versions.archive.length, 0)
  })

  it('用户自建的、出厂里已经没有的、被用户删掉的：都不归这里管', () => {
    const shipped = listsOf({ general: [preset('qp-1', '出厂'), preset('qp-2', '出厂2')] })
    const mine = listsOf({ general: [preset('custom-1', '自建'), preset('qp-gone', '出厂已删')] })
    const r = reconcile({ shipped, mine, now: NOW })
    assert.deepEqual(r.lists.general, mine.general)
    assert.deepEqual(r.versions.baseline, {})
    assert.equal(r.conflicts.length, 0)
  })

  it('三类内容各自对账，互不串扰：同一个 id 出现在不同类别里是两条独立的内容', () => {
    const shippedPhase = phase('dev', '新开发')
    const oldPhase = phase('dev', '旧开发')
    const shipped = listsOf({ phase: [shippedPhase], 'status:online': [preset('qp-1', '新')], general: [preset('qp-1', '通用不变')] })
    const mine = listsOf({ phase: [oldPhase], 'status:online': [preset('qp-1', '我改的')], general: [preset('qp-1', '通用不变')] })
    const versions: PresetVersions = {
      baseline: {
        [keyOf('phase', 'dev')]: entrySignature('phase', oldPhase),
        [keyOf('status:online', 'qp-1')]: entrySignature('status:online', preset('qp-1', '旧')),
        [keyOf('general', 'qp-1')]: entrySignature('general', preset('qp-1', '通用不变')),
      },
      history: {},
      archive: [],
    }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.upgraded, 1)
    assert.equal(r.lists.phase[0].prompt, '新开发')
    assert.deepEqual(r.conflicts.map(c => c.key), [keyOf('status:online', 'qp-1')])
    assert.equal(r.lists.general[0].content, '通用不变')
  })

  it('幂等：对账结果再对账一次，不再有任何动静', () => {
    const oldShipped = preset('qp-1', '旧出厂')
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const mine = listsOf({ general: [oldShipped] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped) }, history: {}, archive: [] }
    const first = reconcile({ shipped, mine, versions, now: NOW })
    const second = reconcile({ shipped, mine: first.lists, versions: first.versions, now: NOW })
    assert.equal(second.changed, false)
    assert.equal(second.upgraded, 0)
    assert.equal(second.conflicts.length, 0)
    assert.deepEqual(second.lists, first.lists)
  })

  it('不改传入的数据', () => {
    const shipped = listsOf({ general: [preset('qp-1', '新')] })
    const mine = listsOf({ general: [preset('qp-1', '旧')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', mine.general[0]) }, history: {}, archive: [] }
    const snapshot = JSON.stringify({ shipped, mine, versions })
    reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(JSON.stringify({ shipped, mine, versions }), snapshot)
  })

  it('同一条旧内容反复被自动升级换下来，留存区里只有一条', () => {
    const oldShipped = preset('qp-1', '旧出厂')
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const baseline = { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped) }
    const first = reconcile({ shipped, mine: listsOf({ general: [oldShipped] }), versions: { baseline, history: {}, archive: [] }, now: NOW })
    const again = reconcile({ shipped, mine: listsOf({ general: [oldShipped] }), versions: { baseline, history: {}, archive: first.versions.archive }, now: NOW })
    assert.equal(again.versions.archive.length, 1)
  })
})

describe('没刷新的旧页面（本端的出厂文案比已采用的更旧）', () => {
  const v1 = preset('qp-1', '出厂v1')
  const v2 = preset('qp-1', '出厂v2')
  const key = keyOf('general', 'qp-1')

  it('新旧两个页面轮流对账，结果稳定，不会你升我降来回写（否则经设置同步会死循环）', () => {
    let mine = listsOf({ general: [v1] })
    let versions: PresetVersions = { baseline: { [key]: entrySignature('general', v1) }, history: {}, archive: [] }
    const shippedNew = listsOf({ general: [v2] })
    const shippedOld = listsOf({ general: [v1] })

    const fromNew = reconcile({ shipped: shippedNew, mine, versions, now: NOW })
    assert.equal(fromNew.upgraded, 1)
    mine = fromNew.lists
    versions = fromNew.versions
    assert.deepEqual(versions.history[key], [entrySignature('general', v1)])

    for (let round = 0; round < 3; round++) {
      const fromOld = reconcile({ shipped: shippedOld, mine, versions, now: NOW })
      assert.equal(fromOld.changed, false, '旧页面不动任何东西')
      assert.equal(fromOld.upgraded, 0)
      assert.equal(fromOld.conflicts.length, 0)
      assert.equal(fromOld.lists.general[0].content, '出厂v2')
      const again = reconcile({ shipped: shippedNew, mine, versions, now: NOW })
      assert.equal(again.changed, false)
    }
  })

  it('用户在新页面改过的内容，旧页面也不会拿它去制造冲突', () => {
    const mine = listsOf({ general: [preset('qp-1', '我改的')] })
    const versions: PresetVersions = { baseline: { [key]: entrySignature('general', v2) }, history: { [key]: [entrySignature('general', v1)] }, archive: [] }
    const r = reconcile({ shipped: listsOf({ general: [v1] }), mine, versions, now: NOW })
    assert.equal(r.conflicts.length, 0)
    assert.equal(r.changed, false)
  })

  it('旧页面里的冲突弹框被别的页面抢先处理后，再点选择也不会改乱内容', () => {
    const old = preset('qp-1', '出厂v0')
    const shipped = listsOf({ general: [v2] })
    const mine = listsOf({ general: [preset('qp-1', '我改的')] })
    const r = reconcile({ shipped, mine, versions: { baseline: { [key]: entrySignature('general', old) }, history: {}, archive: [] }, now: NOW })
    assert.equal(r.conflicts.length, 1)

    // 另一个页面已经选了「使用最新」，这边收到同步：内容已是最新，基线也对齐了
    const other = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'latest' }, now: NOW })
    // 这边弹框里的 conflicts 还是旧的，用户又点了一次「使用最新」
    const stale = resolveConflicts({ lists: other.lists, versions: other.versions, conflicts: r.conflicts, decisions: { [key]: 'latest' }, now: NOW })
    assert.equal(stale.resolved, 0)
    assert.deepEqual(stale.lists, other.lists)
    assert.equal(stale.versions.archive.length, 1, '不会把已经是最新的内容再当作旧版存一次')
  })
})

describe('resolveConflicts', () => {
  const setup = () => {
    const oldShipped = preset('qp-1', '旧出厂', { isMutex: true, mutexTargets: ['qp-2'] })
    const latest = preset('qp-1', '新出厂', { isMutex: true, mutexTargets: ['qp-2'] })
    const mine = listsOf({ general: [{ ...preset('qp-1', '我改的'), isAppendActive: true }, preset('qp-2', '别的')] })
    const shipped = listsOf({ general: [latest, preset('qp-2', '别的')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped), [keyOf('general', 'qp-2')]: entrySignature('general', preset('qp-2', '别的')) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.conflicts.length, 1)
    return { r, latest, key: keyOf('general', 'qp-1') }
  }

  it('使用最新：换成最新出厂文案，用户原来的版本存为旧版，勾选状态保留', () => {
    const { r, latest, key } = setup()
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'latest' }, now: NOW })
    assert.equal(out.resolved, 1)
    assert.equal(out.lists.general[0].content, '新出厂')
    assert.equal(out.lists.general[0].isAppendActive, true)
    assert.equal(out.versions.baseline[key], entrySignature('general', latest))
    assert.equal(out.versions.archive.length, 1)
    assert.equal(out.versions.archive[0].kind, 'old')
    assert.equal(out.versions.archive[0].reason, 'use-latest')
    assert.equal(out.versions.archive[0].payload.content, '我改的')
    // 处理完不会再问
    const again = reconcile({ shipped: listsOf({ general: [latest, preset('qp-2', '别的')] }), mine: out.lists, versions: out.versions, now: NOW })
    assert.equal(again.conflicts.length, 0)
  })

  it('使用旧版：用户的版本不动，最新出厂文案存为新版，之后不再追问', () => {
    const { r, latest, key } = setup()
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'mine' }, now: NOW })
    assert.equal(out.lists.general[0].content, '我改的')
    assert.equal(out.versions.archive.length, 1)
    assert.equal(out.versions.archive[0].kind, 'new')
    assert.equal(out.versions.archive[0].reason, 'use-mine')
    assert.equal(out.versions.archive[0].payload.content, '新出厂')
    assert.equal(out.versions.baseline[key], entrySignature('general', latest))
    const again = reconcile({ shipped: listsOf({ general: [latest, preset('qp-2', '别的')] }), mine: out.lists, versions: out.versions, now: NOW })
    assert.equal(again.conflicts.length, 0)
    assert.equal(again.changed, false)
  })

  it('均保留：用户的版本不动，最新出厂文案作为一条新内容紧跟其后，标题带「（新版）」，id 不重复', () => {
    const { r, key } = setup()
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'both' }, now: NOW })
    assert.deepEqual(out.lists.general.map(i => i.id), ['qp-1', 'qp-1-new', 'qp-2'])
    assert.equal(out.lists.general[0].content, '我改的')
    assert.equal(out.lists.general[1].content, '新出厂')
    assert.equal(out.lists.general[1].title, '标题-qp-1（新版）')
    assert.equal(out.lists.general[1].appendTitle, '标题-qp-1（新版）')
    assert.equal(out.versions.archive.length, 0)
  })

  it('均保留两次（出厂文案又更新了一回）：新内容的 id 依次递增，不覆盖上一次保留的', () => {
    const { r, key } = setup()
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'both' }, now: NOW })
    const again = resolveConflicts({ lists: out.lists, versions: out.versions, conflicts: r.conflicts, decisions: { [key]: 'both' }, now: NOW })
    assert.deepEqual(again.lists.general.map(i => i.id), ['qp-1', 'qp-1-new2', 'qp-1-new', 'qp-2'])
  })

  it('没给出选择的冲突原样留着；多条冲突各用各的选择', () => {
    const oldA = preset('a', 'A旧')
    const oldB = preset('b', 'B旧')
    const shipped = listsOf({ general: [preset('a', 'A新'), preset('b', 'B新')] })
    const mine = listsOf({ general: [preset('a', 'A我'), preset('b', 'B我')] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'a')]: entrySignature('general', oldA), [keyOf('general', 'b')]: entrySignature('general', oldB) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    assert.equal(r.conflicts.length, 2)
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [keyOf('general', 'a')]: 'latest' }, now: NOW })
    assert.equal(out.resolved, 1)
    assert.equal(out.lists.general[0].content, 'A新')
    assert.equal(out.lists.general[1].content, 'B我')
    const next = reconcile({ shipped, mine: out.lists, versions: out.versions, now: NOW })
    assert.deepEqual(next.conflicts.map(c => c.id), ['b'])
  })

  it('弹框期间用户把这条删掉了：跳过，不凭空加回来', () => {
    const { r, key } = setup()
    const lists = { ...r.lists, general: r.lists.general.filter(i => i.id !== 'qp-1') }
    const out = resolveConflicts({ lists, versions: r.versions, conflicts: r.conflicts, decisions: { [key]: 'latest' }, now: NOW })
    assert.equal(out.resolved, 0)
    assert.deepEqual(out.lists.general.map(i => i.id), ['qp-2'])
  })

  it('阶段模板同样适用：均保留会加一个带后缀的新阶段', () => {
    const oldP = phase('dev', '旧')
    const shipped = listsOf({ phase: [phase('dev', '新')] })
    const mine = listsOf({ phase: [phase('dev', '我的')] })
    const versions: PresetVersions = { baseline: { [keyOf('phase', 'dev')]: entrySignature('phase', oldP) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    const out = resolveConflicts({ lists: r.lists, versions: r.versions, conflicts: r.conflicts, decisions: { [keyOf('phase', 'dev')]: 'both' }, now: NOW })
    assert.deepEqual(out.lists.phase.map(p => p.id), ['dev', 'dev-new'])
    assert.equal(out.lists.phase[1].label, '阶段-dev（新版）')
    assert.equal(out.lists.phase[1].prompt, '新')
  })
})

describe('留存区操作', () => {
  const archived = (): { lists: ScopeLists; versions: PresetVersions; uid: string } => {
    const oldShipped = preset('qp-1', '旧出厂')
    const shipped = listsOf({ general: [preset('qp-1', '新出厂')] })
    const mine = listsOf({ general: [oldShipped] })
    const versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', oldShipped) }, history: {}, archive: [] }
    const r = reconcile({ shipped, mine, versions, now: NOW })
    return { lists: r.lists, versions: r.versions, uid: r.versions.archive[0].uid }
  }

  it('重新启用：作为带「（旧版）」后缀的新内容加到列表末尾，从留存区移除，id 不与现有的重复', () => {
    const { lists, versions, uid } = archived()
    const out = reenableArchived(lists, versions, uid)
    assert.ok(out)
    assert.deepEqual(out.lists.general.map(i => i.id), ['qp-1', 'qp-1-old'])
    assert.equal(out.lists.general[1].content, '旧出厂')
    assert.equal(out.lists.general[1].title, '标题-qp-1（旧版）')
    assert.equal(out.lists.general[1].isAppendActive, false)
    assert.equal(out.versions.archive.length, 0)
    // 重新启用的是一条自建内容：之后出厂文案再变，不会再被它牵动
    const next = reconcile({ shipped: listsOf({ general: [preset('qp-1', '再新一版')] }), mine: out.lists, versions: out.versions, now: NOW })
    assert.equal(next.lists.general[1].content, '旧出厂')
  })

  it('重新启用「新版」带「（新版）」后缀；id 被占用时递增', () => {
    const { lists, versions, uid } = archived()
    const withNew = { baseline: versions.baseline, history: versions.history, archive: [{ ...versions.archive[0], kind: 'new' as const, uid: 'ar-new' }] }
    const taken = { ...lists, general: [...lists.general, preset('qp-1-new', '占位')] }
    const out = reenableArchived(taken, withNew, 'ar-new')
    assert.ok(out)
    assert.equal(out.lists.general[2].id, 'qp-1-new2')
    assert.equal(out.lists.general[2].title, '标题-qp-1（新版）')
    assert.ok(uid)
  })

  it('重新启用不存在的编号：返回 null，什么都不动', () => {
    const { lists, versions } = archived()
    assert.equal(reenableArchived(lists, versions, 'nope'), null)
  })

  it('删除一条 / 清空：只动留存区，不动基线', () => {
    const { versions, uid } = archived()
    const deleted = deleteArchived(versions, uid)
    assert.equal(deleted.archive.length, 0)
    assert.deepEqual(deleted.baseline, versions.baseline)
    assert.equal(clearArchive(versions).archive.length, 0)
    assert.deepEqual(clearArchive(versions).baseline, versions.baseline)
    assert.equal(versions.archive.length, 1, '不改传入的数据')
  })

  it(`留存区最多 ${ARCHIVE_LIMIT} 条，超出时丢最早的`, () => {
    const shipped = (n: number) => listsOf({ general: [preset('qp-1', `出厂${n}`)] })
    let mine = listsOf({ general: [preset('qp-1', '出厂0')] })
    let versions: PresetVersions = { baseline: { [keyOf('general', 'qp-1')]: entrySignature('general', mine.general[0]) }, history: {}, archive: [] }
    for (let i = 1; i <= ARCHIVE_LIMIT + 5; i++) {
      const r = reconcile({ shipped: shipped(i), mine, versions, now: NOW })
      mine = r.lists
      versions = r.versions
    }
    const { archive } = versions
    assert.equal(archive.length, ARCHIVE_LIMIT)
    assert.equal(archive[archive.length - 1].payload.content, `出厂${ARCHIVE_LIMIT + 4}`)
    assert.equal(archive[0].payload.content, '出厂5')
    assert.equal(versions.history[keyOf('general', 'qp-1')].length, HISTORY_LIMIT, '历史也有上限')
  })

  it('恢复默认前：只存会被换掉且和出厂不同的（改过的、自建的），和出厂一样的不存；重复恢复不重复存', () => {
    const shipped = [preset('qp-1', '出厂1'), preset('qp-2', '出厂2')]
    const current = [preset('qp-1', '出厂1'), preset('qp-2', '我改过的'), preset('custom-9', '自建的')]
    const base: PresetVersions = { baseline: { a: 'b' }, history: { a: ['c'] }, archive: [] }
    const once = archiveReplaced(base, 'general', current, shipped, NOW)
    assert.deepEqual(once.archive.map(e => e.originId), ['qp-2', 'custom-9'])
    assert.ok(once.archive.every(e => e.kind === 'old' && e.reason === 'reset' && e.scope === 'general'))
    assert.equal(once.archive[0].payload.content, '我改过的')
    assert.deepEqual(once.baseline, base.baseline)
    assert.deepEqual(once.history, base.history)
    assert.equal(base.archive.length, 0, '不改传入的数据')
    const twice = archiveReplaced(once, 'general', current, shipped, NOW)
    assert.equal(twice.archive.length, 2)
  })

  it('来源说明对四种原因都有文字', () => {
    assert.match(archiveReasonText({ kind: 'old', reason: 'auto-upgrade' }), /没改过/)
    assert.match(archiveReasonText({ kind: 'old', reason: 'use-latest' }), /使用最新/)
    assert.match(archiveReasonText({ kind: 'new', reason: 'use-mine' }), /使用旧版/)
    assert.match(archiveReasonText({ kind: 'old', reason: 'reset' }), /恢复默认/)
  })
})

describe('normalizeVersions / uniqueId', () => {
  it('空值、类型错乱、缺字段的落盘数据都收敛成合法结构，不抛错', () => {
    assert.deepEqual(normalizeVersions(undefined), { baseline: {}, history: {}, archive: [] })
    assert.deepEqual(normalizeVersions('x'), { baseline: {}, history: {}, archive: [] })
    assert.deepEqual(normalizeVersions({ baseline: 1, archive: 'x' }), { baseline: {}, history: {}, archive: [] })
    const v = normalizeVersions({
      baseline: { 'general|qp-1': 'abc', bad: 3 },
      archive: [
        null,
        { uid: 'x' },
        { uid: 'y', scope: 'nope', payload: {}, kind: 'old' },
        { uid: 'z', scope: 'general', payload: { id: 'qp-1', title: 't' }, kind: 'weird' },
        { uid: 'ok', scope: 'general', payload: { id: 'qp-1', title: 't' }, kind: 'new' },
      ],
    })
    assert.deepEqual(v.baseline, { 'general|qp-1': 'abc' })
    assert.deepEqual(v.archive.map(e => e.uid), ['ok'])
    assert.equal(v.archive[0].originId, 'qp-1')
    assert.equal(v.archive[0].reason, 'auto-upgrade')
  })

  it('已经下线的范围（早先的暂离 / 托管模式预设）留下的基线、历史与留存条目都被丢掉', () => {
    const v = normalizeVersions({
      baseline: { 'status:away|qp-aw-2': 'a', 'status:autopilot|qp-ap-1': 'b', 'status:online|qp-on-1': 'c', 'general|qp-1': 'd' },
      history: { 'status:away|qp-aw-2': ['x'], 'status:online|qp-on-1': ['y'] },
      archive: [
        { uid: 'old-away', scope: 'status:away', payload: { id: 'qp-aw-2' }, kind: 'old' },
        { uid: 'keep', scope: 'status:online', payload: { id: 'qp-on-1' }, kind: 'old' },
      ],
    })
    assert.deepEqual(v.baseline, { 'status:online|qp-on-1': 'c', 'general|qp-1': 'd' })
    assert.deepEqual(v.history, { 'status:online|qp-on-1': ['y'] })
    assert.deepEqual(v.archive.map(e => e.uid), ['keep'])
  })

  it('uniqueId 递增到第一个空位', () => {
    const items: ScopeItem[] = [{ id: 'a' }, { id: 'a2' }, { id: 'a3' }]
    assert.equal(uniqueId(items, 'b'), 'b')
    assert.equal(uniqueId(items, 'a'), 'a4')
  })
})

// 防止类型被无意中改窄：冲突对象要能直接喂给界面
const _typecheck: PresetConflict | null = null
void _typecheck
