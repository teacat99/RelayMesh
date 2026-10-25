import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import {
  ASSIGNEE_ALL,
  ASSIGNEE_NONE,
  DEFAULT_VISIBLE_STATES,
  MODULE_ALL,
  MODULE_LIMITS,
  MODULE_NONE,
  OFFER_STALE_MS,
  WORK_QUICK_KEYS,
  WORK_QUICK_META,
  UNASSIGNED_LABEL,
  WORK_CLAIM_META,
  WORK_EVENT_META,
  WORK_FILTER_ORDER,
  STATE_TRACK,
  WORK_LIMITS,
  WORK_STAGES,
  WORK_STAGE_META,
  WORK_STATES,
  assigneeOptions,
  assigneeView,
  batchProgress,
  boardChipText,
  boardMemberList,
  buildModuleTree,
  buildTaskGroups,
  buildWorkflowOptions,
  checkModuleName,
  checkTitle,
  clampPercent,
  closedAgeDays,
  compareInModule,
  compareRecent,
  countActiveWorkFilters,
  countByState,
  defaultExpanded,
  defaultStageForKind,
  describeSessionCtx,
  claimLabel,
  describeTransition,
  eventLabel,
  eventTone,
  formatWaited,
  inBoard,
  displayModulePath,
  filterBySelection,
  filterItems,
  flattenModuleTree,
  groupSessionTasks,
  isClosedState,
  isCompacted,
  isWorkQuick,
  isWorkStage,
  kindLabel,
  isAssigned,
  matchesAssignee,
  parseSeqQuery,
  matchesQuery,
  matchesQuick,
  moduleOptions,
  offerWaitMs,
  offerWaitView,
  offeredBadgeTitle,
  parseLabelInput,
  parseRefInput,
  progressView,
  quickActions,
  quickCounts,
  relativeTime,
  runeLength,
  selectionModuleIds,
  showCreator,
  stageLabel,
  stageView,
  stateLabel,
  stateTrack,
  summarizeCounts,
  summarizeTracks,
  trackFill,
  trackPercentLabel,
  truncateRunes,
  workRef,
  workflowLabel,
  type WorkItem,
  type WorkModule,
  type WorkSessionLink,
  type WorkState,
} from './workItems'

let nextId = 1
function item(over: Partial<WorkItem> = {}): WorkItem {
  const id = over.id ?? nextId++
  return {
    id,
    workflow_id: 'wf',
    seq: id,
    title: `任务 ${id}`,
    kind: 'feature',
    labels: [],
    state: 'todo',
    progress: '',
    conclusion: '',
    refs: [],
    created_by: 'human',
    created_at: '2026-10-01T08:00:00Z',
    updated_at: '2026-10-01T08:00:00Z',
    ...over,
  }
}

describe('状态与类型元数据', () => {
  test('五个状态都有名称；筛选条先放需要关注的，默认不显示已搁置', () => {
    assert.deepEqual(WORK_STATES, ['todo', 'doing', 'review', 'done', 'shelved'])
    assert.deepEqual(WORK_FILTER_ORDER, ['doing', 'review', 'todo', 'done', 'shelved'])
    assert.deepEqual(DEFAULT_VISIBLE_STATES, ['doing', 'review', 'todo', 'done'])
    assert.deepEqual([...WORK_FILTER_ORDER].sort(), [...WORK_STATES].sort())
    assert.deepEqual(WORK_STATES.map(stateLabel), ['待办', '进行中', '验证中', '已完成', '已搁置'])
    assert.equal(stateLabel('mystery'), 'mystery')
    assert.equal(stateLabel(''), '未知')
    assert.equal(kindLabel('research'), '调研与方案')
    assert.equal(kindLabel(''), '未分类')
  })

  test('已完成与已搁置都算离开主线', () => {
    assert.deepEqual(WORK_STATES.filter(isClosedState), ['done', 'shelved'])
    assert.equal(workRef({ seq: 12 }), '#12')
  })

  test('字数上限与后端常量一致', () => {
    assert.deepEqual(WORK_LIMITS, { title: 200, description: 1000, progress: 500, conclusion: 400, note: 500, label: 24, labels: 8, ref: 200, refs: 10, updates: 20, compactAfterDays: 90, improve: 450 })
  })
})

describe('快捷操作', () => {
  test('每个状态恰好有一个主操作，且沿主线向前', () => {
    const forward: Record<WorkState, WorkState> = { todo: 'doing', doing: 'review', review: 'done', done: 'doing', shelved: 'todo' }
    for (const s of WORK_STATES) {
      const actions = quickActions(s)
      assert.ok(actions.length >= 1, s)
      const primary = actions.filter(a => a.primary)
      if (s === 'done') {
        // 已完成只提供「重新打开」，不抢眼
        assert.equal(primary.length, 0)
        assert.equal(actions[0].to, 'doing')
      } else {
        assert.equal(primary.length, 1, s)
        assert.equal(primary[0].to, forward[s], s)
      }
      assert.ok(!actions.some(a => a.to === s), `${s} 的操作不应指向自己`)
    }
  })

  test('「已完成」绑定在开发流程上：验证通过就直接标记，没有二次确认；主操作的文案跟着流程走', () => {
    const primaries = Object.fromEntries(WORK_STATES.map(s => [s, quickActions(s).find(a => a.primary)?.label ?? '']))
    assert.deepEqual(primaries, { todo: '开始', doing: '进入验证', review: '标记完成', done: '', shelved: '恢复为待办' })
    assert.ok(WORK_STATES.every(s => quickActions(s).every(a => !('confirm' in a))), '不再有需要二次确认的操作')
  })

  test('时间线上的状态说明', () => {
    assert.equal(describeTransition('todo', 'doing'), '待办 → 进行中')
    assert.equal(describeTransition('', 'todo'), '置为待办')
    assert.equal(describeTransition('doing', 'doing'), '')
    assert.equal(describeTransition(undefined, undefined), '')
  })
})

/** 构造一个模块 / 功能节点。stats 默认全 0。 */
function mod(over: Partial<WorkModule> & { id: number; name: string }): WorkModule {
  const path = over.path ?? over.name
  return {
    workflow_id: 'wf',
    description: '',
    sort: over.id,
    created_at: '2026-10-01T08:00:00Z',
    updated_at: '2026-10-01T08:00:00Z',
    path,
    depth: over.parent_id ? 1 : 0,
    stats: { total: 0, todo: 0, doing: 0, review: 0, done: 0, shelved: 0, percent: 0 },
    ...over,
  }
}

describe('进度条取值', () => {
  test('后端算好的值直接用；数字只在显式 / 子任务比例时展示，状态推导的不展示数字', () => {
    assert.deepEqual(progressView({ state: 'doing', progress_pct: 40, progress_from: 'explicit' }), { pct: 40, label: '40%', from: 'explicit', derived: false, tone: 'sky' })
    assert.deepEqual(progressView({ state: 'todo', progress_pct: 60, progress_from: 'children' }), { pct: 60, label: '60%', from: 'children', derived: false, tone: 'slate' })
    const byState = progressView({ state: 'review', progress_pct: 90, progress_from: 'state' })
    assert.equal(byState.label, '')
    assert.equal(byState.derived, true)
    assert.equal(byState.tone, 'violet')
    assert.equal(progressView({ state: 'done', progress_pct: 100, progress_from: 'state' }).tone, 'emerald')
  })

  test('旧数据没有进度字段时按状态推导，越界值被夹住', () => {
    assert.equal(progressView({ state: 'doing' }).pct, 50)
    assert.equal(progressView({ state: 'review' }).pct, 90)
    assert.equal(progressView({ state: 'done' }).pct, 100)
    assert.equal(progressView({ state: 'todo' }).pct, 0)
    assert.equal(progressView({ state: 'shelved' }).tone, 'zinc')
    assert.equal(progressView({ state: 'doing', progress_pct: 180, progress_from: 'explicit' }).pct, 100)
    assert.equal(progressView({ state: 'doing', progress_pct: -5, progress_from: 'explicit' }).pct, 0)
    assert.equal(progressView({ state: 'weird' as WorkState }).tone, 'slate')
    assert.equal(clampPercent(Number.NaN), 0)
    assert.equal(clampPercent(33.6), 34)
  })
})

describe('模块规划树', () => {
  const modules = [
    mod({ id: 3, name: '任务', sort: 2 }),
    mod({ id: 1, name: '终端', sort: 1 }),
    mod({ id: 5, name: '输入法', parent_id: 1, path: '终端/输入法', sort: 2 }),
    mod({ id: 4, name: '历史日志', parent_id: 1, path: '终端/历史日志', sort: 1 }),
  ]

  test('整理成两层树：同级按 sort，不依赖入参顺序', () => {
    const tree = buildModuleTree(modules)
    assert.deepEqual(tree.map(n => n.module.name), ['终端', '任务'])
    assert.deepEqual(tree[0].features.map(f => f.name), ['历史日志', '输入法'])
    assert.deepEqual(flattenModuleTree(tree).map(m => m.id), [1, 4, 5, 3])
  })

  test('找不到父模块的功能当作顶层，不会丢节点', () => {
    const tree = buildModuleTree([mod({ id: 9, name: '孤儿', parent_id: 404, path: '?/孤儿' })])
    assert.deepEqual(tree.map(n => n.module.id), [9])
  })

  test('选中模块覆盖它的功能，选中功能只覆盖自己', () => {
    assert.deepEqual([...(selectionModuleIds(1, modules) ?? [])].sort(), [1, 4, 5])
    assert.deepEqual([...(selectionModuleIds(4, modules) ?? [])], [4])
    assert.equal(selectionModuleIds(MODULE_ALL, modules), null)
    assert.equal(selectionModuleIds(MODULE_NONE, modules), null)
  })

  test('按选中项筛选任务；引用了已不存在的模块视同未归类', () => {
    const items = [
      item({ id: 1, module_id: 4 }),
      item({ id: 2, module_id: 5 }),
      item({ id: 3, module_id: 1 }),
      item({ id: 4, module_id: 3 }),
      item({ id: 5 }),
      item({ id: 6, module_id: 777 }),
    ]
    const ids = (sel: Parameters<typeof filterBySelection>[1]) => filterBySelection(items, sel, modules).map(i => i.id)
    assert.deepEqual(ids(MODULE_ALL), [1, 2, 3, 4, 5, 6])
    assert.deepEqual(ids(1), [1, 2, 3])
    assert.deepEqual(ids(4), [1])
    assert.deepEqual(ids(3), [4])
    assert.deepEqual(ids(MODULE_NONE), [5, 6])
    assert.deepEqual(ids(9999), [])
  })

  test('表单下拉：树序，功能缩进', () => {
    const opts = moduleOptions(modules)
    assert.deepEqual(opts.map(o => o.id), [1, 4, 5, 3])
    assert.deepEqual(opts.map(o => o.depth), [0, 1, 1, 0])
    assert.equal(opts[0].label, '终端')
    assert.ok(opts[1].label.endsWith('历史日志') && opts[1].label !== '历史日志')
    assert.equal(displayModulePath('终端/历史日志'), '终端 / 历史日志')
    assert.equal(displayModulePath(undefined), '')
  })

  test('名称校验：非空、长度、分隔符、同级不重名（忽略大小写，改名时排除自己）', () => {
    const siblings = [{ id: 1, name: '终端' }, { id: 3, name: 'Task' }]
    assert.equal(checkModuleName('  新模块  ', siblings).name, '新模块')
    assert.ok(checkModuleName('  新模块  ', siblings).ok)
    assert.match(checkModuleName('   ', siblings).error ?? '', /请填写模块名称/)
    assert.match(checkModuleName('', siblings, '功能').error ?? '', /请填写功能名称/)
    assert.match(checkModuleName('长'.repeat(MODULE_LIMITS.name + 1), siblings).error ?? '', /最长 40/)
    assert.ok(checkModuleName('长'.repeat(MODULE_LIMITS.name), siblings).ok)
    for (const bad of ['a/b', 'a>b', 'a＞b', 'a／b', 'a›b', 'a→b', '换\n行']) {
      assert.match(checkModuleName(bad, siblings).error ?? '', /分隔符/, bad)
    }
    assert.match(checkModuleName('task', siblings).error ?? '', /已有同名的模块/)
    assert.match(checkModuleName('终端', siblings, '功能').error ?? '', /已有同名的功能/)
    assert.ok(checkModuleName('终端', siblings, '模块', 1).ok, '改名时不和自己冲突')
    assert.equal(MODULE_LIMITS.modulesPerWorkflow, 30)
    assert.equal(MODULE_LIMITS.featuresPerModule, 30)
  })
})

describe('分组与排序', () => {
  const modules = [
    mod({ id: 1, name: '终端' }),
    mod({ id: 4, name: '历史日志', parent_id: 1, path: '终端/历史日志' }),
    mod({ id: 3, name: '任务' }),
  ]

  test('模块内：进行中 → 验证中 → 待办 → 已搁置 → 已完成，同状态按最近进展', () => {
    const items = [
      item({ id: 1, state: 'done', updated_at: '2026-10-05T00:00:00Z' }),
      item({ id: 2, state: 'todo', updated_at: '2026-10-01T00:00:00Z' }),
      item({ id: 3, state: 'todo', updated_at: '2026-10-03T00:00:00Z' }),
      item({ id: 4, state: 'doing', updated_at: '2026-10-02T00:00:00Z' }),
      item({ id: 5, state: 'review', updated_at: '2026-10-02T00:00:00Z' }),
      item({ id: 6, state: 'shelved', updated_at: '2026-10-09T00:00:00Z' }),
    ]
    assert.deepEqual([...items].sort(compareInModule).map(i => i.id), [4, 5, 3, 2, 6, 1])
    // 最近进展：更新时间倒序；同一时刻的按编号倒序
    assert.deepEqual([...items].sort(compareRecent).map(i => i.id), [6, 1, 3, 5, 4, 2])
  })

  test('排序比较器遇到未知状态不崩溃，排到最后', () => {
    const weird = item({ id: 9, state: 'weird' as WorkState })
    const ok = item({ id: 10, state: 'done' })
    assert.deepEqual([weird, ok].sort(compareInModule).map(i => i.id), [10, 9])
  })

  test('plan：按规划树顺序一组一个模块 / 功能，模块自己的任务在它的功能之前，没归类的最后', () => {
    const items = [
      item({ id: 1, module_id: 3, state: 'todo' }),
      item({ id: 2, module_id: 4, state: 'doing' }),
      item({ id: 3, module_id: 1, state: 'todo' }),
      item({ id: 4, state: 'todo' }),
      item({ id: 5, module_id: 4, state: 'review' }),
    ]
    const groups = buildTaskGroups(items, modules, { sort: 'plan' })
    assert.deepEqual(groups.map(g => g.key), ['m:1', 'm:4', 'm:3', 'none'])
    assert.deepEqual(groups.map(g => g.path), ['终端', '终端/历史日志', '任务', UNASSIGNED_LABEL])
    assert.deepEqual(groups[1].rows.map(r => r.item.id), [2, 5], '功能组内：进行中在验证中之前')
    assert.equal(groups[1].parentName, '终端')
    assert.equal(groups[1].depth, 1)
    assert.equal(groups[0].depth, 0)
    assert.equal(groups[0].parentName, '')
    assert.equal(groups[3].moduleId, null)
  })

  test('plan：空模块不出组；引用了已删除模块的任务进「未归类」；没有任务时返回空数组', () => {
    const groups = buildTaskGroups([item({ id: 1, module_id: 555 })], modules, { sort: 'plan' })
    assert.deepEqual(groups.map(g => g.key), ['none'])
    assert.deepEqual(buildTaskGroups([], modules, { sort: 'plan' }), [])
    assert.deepEqual(buildTaskGroups([], modules, { sort: 'recent' }), [])
  })

  test('recent：不分组，最近进展在前，行里自己标出所属模块；批次不缩进', () => {
    const items = [
      item({ id: 1, module_id: 3, updated_at: '2026-10-01T00:00:00Z' }),
      item({ id: 2, module_id: 4, updated_at: '2026-10-03T00:00:00Z' }),
      item({ id: 3, parent_id: 1, updated_at: '2026-10-02T00:00:00Z' }),
    ]
    const groups = buildTaskGroups(items, modules, { sort: 'recent' })
    assert.equal(groups.length, 1)
    assert.equal(groups[0].key, 'recent')
    assert.deepEqual(groups[0].rows.map(r => r.item.id), [2, 3, 1])
    assert.ok(groups[0].rows.every(r => r.depth === 0 && r.childCount === 0))
  })

  test('批次：子任务缩进在父任务下面，按序号排；全部结束的批次默认折起来', () => {
    const batch = item({ id: 10, seq: 10, module_id: 3, state: 'doing' })
    const kids = [
      item({ id: 12, seq: 12, parent_id: 10, state: 'done' }),
      item({ id: 11, seq: 11, parent_id: 10, state: 'review' }),
    ]
    const open = buildTaskGroups([batch, ...kids], modules, { sort: 'plan' })[0].rows
    assert.deepEqual(open.map(r => [r.item.id, r.depth]), [[10, 0], [11, 1], [12, 1]])
    assert.equal(open[0].childCount, 2)
    assert.equal(open[0].collapsed, false)

    const closedKids = [item({ id: 13, seq: 13, parent_id: 10, state: 'done' }), item({ id: 14, seq: 14, parent_id: 10, state: 'shelved' })]
    const folded = buildTaskGroups([batch, ...closedKids], modules, { sort: 'plan' })[0].rows
    assert.deepEqual(folded.map(r => r.item.id), [10])
    assert.equal(folded[0].collapsed, true)
    assert.equal(folded[0].childCount, 2)
    assert.equal(defaultExpanded(closedKids), false)
    assert.equal(defaultExpanded(kids), true)

    // 手动展开 / 折叠优先于默认值
    const forced = buildTaskGroups([batch, ...closedKids], modules, { sort: 'plan', expanded: new Map([[10, true]]) })[0].rows
    assert.deepEqual(forced.map(r => r.item.id), [10, 13, 14])
    const forcedClosed = buildTaskGroups([batch, ...kids], modules, { sort: 'plan', expanded: new Map([[10, false]]) })[0].rows
    assert.deepEqual(forcedClosed.map(r => r.item.id), [10])
  })

  test('批次的父任务不在当前结果里时，子任务独立成行；子任务可以和父任务在不同模块', () => {
    const orphan = item({ id: 21, seq: 21, parent_id: 99, module_id: 4, state: 'review' })
    assert.deepEqual(buildTaskGroups([orphan], modules, { sort: 'plan' })[0].rows.map(r => [r.item.id, r.depth]), [[21, 0]])
    const batch = item({ id: 30, seq: 30, module_id: 3, state: 'doing' })
    const child = item({ id: 31, seq: 31, parent_id: 30, module_id: 4, state: 'doing' })
    const groups = buildTaskGroups([batch, child], modules, { sort: 'plan' })
    assert.deepEqual(groups.map(g => g.key), ['m:3'], '子任务跟着父任务所在的组')
    assert.deepEqual(groups[0].rows.map(r => r.item.id), [30, 31])
  })

  test('分组不改动入参', () => {
    const input = [item({ id: 1, updated_at: '2026-10-01T00:00:00Z' }), item({ id: 2, updated_at: '2026-10-02T00:00:00Z' })]
    const copy = [...input]
    buildTaskGroups(input, modules, { sort: 'plan' })
    buildTaskGroups(input, modules, { sort: 'recent' })
    assert.deepEqual(input, copy)
  })

  test('各状态计数（状态筛选条上的数字）；未知状态不计入', () => {
    const items = [item({ state: 'todo' }), item({ state: 'todo' }), item({ state: 'review' }), item({ state: 'weird' as WorkState })]
    assert.deepEqual(countByState(items), { todo: 2, doing: 0, review: 1, done: 0, shelved: 0 })
  })

  test('验证中的数量对得上账：批次里的子任务也算（列表里要看得见、点得到）', () => {
    const batch = item({ id: 30, seq: 30, module_id: 3, state: 'todo' })
    const kids = [31, 32, 33].map(id => item({ id, seq: id, parent_id: 30, module_id: 3, state: 'review' }))
    const standalone = item({ id: 34, seq: 34, module_id: 3, state: 'review' })
    const groups = buildTaskGroups([batch, ...kids, standalone], modules, { sort: 'plan' })
    const reviewRows = groups.flatMap(g => g.rows).filter(r => r.item.state === 'review')
    assert.equal(reviewRows.length, 4)
    assert.equal(summarizeCounts({ todo: 1, review: 4 }), '4 验证中')
  })
})

describe('筛选', () => {
  const a = item({ id: 1, title: '修复终端闪烁', labels: ['终端'], progress: '定位到 resize 竞态', workflow_id: 'wf-a', kind: 'bug' })
  const b = item({ id: 2, title: '整理设置页', conclusion: '按模块分组', workflow_id: 'wf-b', kind: 'improve' })

  test('关键字匹配标题、标签、进度、结论、编号与工作流，忽略大小写', () => {
    assert.ok(matchesQuery(a, '闪烁'))
    assert.ok(matchesQuery(a, '终端'))
    assert.ok(matchesQuery(a, 'RESIZE'))
    assert.ok(matchesQuery(b, '分组'))
    assert.ok(matchesQuery(a, '#1'))
    assert.ok(matchesQuery(a, 'wf-a'))
    assert.ok(!matchesQuery(a, '不存在'))
    assert.ok(matchesQuery(a, '   '))
  })

  test('关键字也匹配任务描述（标题、进度里都没有的词靠描述找到）', () => {
    const d = item({ id: 3, title: '无关的标题', description: '把冷数据归档到对象存储，保留 90 天索引' })
    assert.ok(matchesQuery(d, '对象存储'))
    assert.ok(!matchesQuery(a, '对象存储'))
    assert.ok(!matchesQuery(item({ id: 4, title: '没写描述' }), '对象存储'))
  })

  test('多个条件同时生效', () => {
    const all = [a, b]
    assert.deepEqual(filterItems(all, { workflowId: 'wf-a' }).map(i => i.id), [1])
    assert.deepEqual(filterItems(all, { kind: 'improve' }).map(i => i.id), [2])
    assert.deepEqual(filterItems(all, { query: '设置', kind: 'improve' }).map(i => i.id), [2])
    assert.deepEqual(filterItems(all, { query: '设置', kind: 'bug' }).map(i => i.id), [])
    assert.deepEqual(filterItems(all, {}).map(i => i.id), [1, 2])
  })
})

describe('批次进度与概览', () => {
  test('批次进度', () => {
    assert.equal(batchProgress({}), null)
    assert.equal(batchProgress({ child_total: 0, child_done: 0 }), null)
    assert.deepEqual(batchProgress({ child_total: 5, child_done: 2 }), { done: 2, total: 5, percent: 40 })
    assert.deepEqual(batchProgress({ child_total: 3, child_done: 9 }), { done: 3, total: 3, percent: 100 })
  })

  test('侧栏概览：进行中在前、验证中其次，都没有时才显示待办（只是进度概览，不催你确认）', () => {
    assert.equal(summarizeCounts(undefined), '')
    assert.equal(summarizeCounts({}), '')
    assert.equal(summarizeCounts({ review: 2, doing: 3, todo: 9 }), '3 进行中 · 2 验证中')
    assert.equal(summarizeCounts({ todo: 4 }), '4 待办')
    assert.equal(summarizeCounts({ done: 8 }), '')
  })
})

describe('输入规整', () => {
  test('标签：中英文分隔符、去重（忽略大小写）、逐个截断、最多 8 个', () => {
    assert.deepEqual(parseLabelInput('终端, 设置，ui 、UI;发布\n  '), ['终端', '设置', 'ui', '发布'])
    assert.deepEqual(parseLabelInput(''), [])
    assert.equal(parseLabelInput('长'.repeat(40))[0].length, WORK_LIMITS.label)
    const many = parseLabelInput(Array.from({ length: 20 }, (_, i) => `t${i}`).join(','))
    assert.equal(many.length, WORK_LIMITS.labels)
  })

  test('引用：一行一条、去空行与重复、最多 10 条、逐条截断', () => {
    assert.deepEqual(parseRefInput('commit:abc1234\n\n  D-201  \ncommit:abc1234\r\ndocs/x.md'), ['commit:abc1234', 'D-201', 'docs/x.md'])
    assert.equal(parseRefInput(Array.from({ length: 15 }, (_, i) => `r${i}`).join('\n')).length, WORK_LIMITS.refs)
    assert.equal(parseRefInput('x'.repeat(300))[0].length, WORK_LIMITS.ref)
  })

  test('标题：折叠换行、去首尾空白、校验长度', () => {
    assert.deepEqual(checkTitle('  修复\n闪烁  '), { ok: true, title: '修复 闪烁' })
    assert.equal(checkTitle('   ').ok, false)
    assert.equal(checkTitle('   ').error, '请填写标题')
    assert.equal(checkTitle('长'.repeat(201)).ok, false)
    assert.equal(checkTitle('长'.repeat(200)).ok, true)
  })

  test('字数按码点计算，与后端一致', () => {
    assert.equal(runeLength('中文abc'), 5)
    assert.equal(runeLength('😀😀'), 2)
    assert.equal(truncateRunes('😀😀😀', 2), '😀😀')
    assert.equal(truncateRunes('abc', 5), 'abc')
  })
})

describe('时间', () => {
  const now = Date.parse('2026-10-02T12:00:00Z')
  test('相对时间', () => {
    assert.equal(relativeTime(undefined, now), '')
    assert.equal(relativeTime('bad', now), '')
    assert.equal(relativeTime('2026-10-02T11:59:50Z', now), '刚刚')
    assert.equal(relativeTime('2026-10-02T11:50:00Z', now), '10 分钟前')
    assert.equal(relativeTime('2026-10-02T09:00:00Z', now), '3 小时前')
    assert.equal(relativeTime('2026-09-30T12:00:00Z', now), '2 天前')
    assert.match(relativeTime('2026-07-01T12:00:00Z', now), /^2026-0[67]-\d\d$/)
    // 未来时间不出现负数
    assert.equal(relativeTime('2026-10-02T13:00:00Z', now), '刚刚')
  })

  test('关闭天数与是否已被整理压缩（超过 90 天）', () => {
    const open = { state: 'doing' as WorkState }
    assert.equal(closedAgeDays(open, now), null)
    assert.equal(closedAgeDays({ state: 'done', closed_at: undefined }, now), null)
    assert.equal(closedAgeDays({ state: 'done', closed_at: '2026-09-30T12:00:00Z' }, now), 2)
    assert.equal(isCompacted({ state: 'done', closed_at: '2026-09-30T12:00:00Z' }, now), false)
    assert.equal(isCompacted({ state: 'shelved', closed_at: '2026-06-01T00:00:00Z' }, now), true)
    assert.equal(isCompacted(open, now), false)
  })
})

describe('工作流选择项', () => {
  test('合并已有任务的工作流与已知会话，开放任务多的在前，名称优先用会话标题', () => {
    const opts = buildWorkflowOptions(
      [
        { workflow_id: 'wf-a', total: 5, active: 2, updated_at: '' },
        { workflow_id: 'wf-b', total: 1, active: 0, updated_at: '' },
      ],
      [
        { workflow_id: 'wf-a', title: '项目甲' },
        { workflow_id: 'wf-c', title: '项目丙' },
        { workflow_id: 'wf-c', title: '重复的标题不覆盖' },
        { workflow_id: '', title: '没有工作流的会话' },
        { workflow_id: 'wf-d' },
      ]
    )
    assert.deepEqual(opts.map(o => [o.id, o.label, o.active]), [
      ['wf-a', '项目甲', 2],
      ['wf-c', '项目丙', 0],
      ['wf-b', 'wf-b', 0],
      ['wf-d', 'wf-d', 0],
    ])
  })

  // 同一个项目的工作流共用一张任务板：服务端给每个成员各一条概况（数字相同），下拉里只该出一项
  const sharedStats = [
    { workflow_id: 'wf-new', board_key: 'p7', board_name: '主项目', workflows: ['wf-new', 'wf-old'], total: 9, active: 4, updated_at: '' },
    { workflow_id: 'wf-old', board_key: 'p7', board_name: '主项目', workflows: ['wf-new', 'wf-old'], total: 9, active: 4, updated_at: '' },
    { workflow_id: 'wf-solo', board_key: 'w:wf-solo', total: 2, active: 1, updated_at: '' },
  ]

  test('共用同一张板的工作流在下拉里合成一项：名字是项目名加成员数，数字是整张板的', () => {
    const opts = buildWorkflowOptions(sharedStats, [
      { workflow_id: 'wf-new', title: '会话新' },
      { workflow_id: 'wf-old', title: '会话旧' },
      { workflow_id: 'wf-solo', title: '独立的' },
    ])
    assert.deepEqual(opts.map(o => [o.id, o.label, o.active]), [
      ['wf-new', '主项目 · 2 个工作流', 4],
      ['wf-solo', '独立的', 1],
    ])
    assert.deepEqual(opts[0].members, ['wf-new', 'wf-old'])
    assert.equal(opts[1].members, undefined)
  })

  test('代表共用板的成员：正在看的 > 当前会话的 > 最近有动静的；还没有任务的成员只要目录归在这张板里也算', () => {
    const rep = (prefer: Parameters<typeof buildWorkflowOptions>[2]) =>
      buildWorkflowOptions(sharedStats, [], prefer).find(o => o.members)?.id
    assert.equal(rep({}), 'wf-new')
    assert.equal(rep({ session: 'wf-old' }), 'wf-old')
    assert.equal(rep({ selected: 'wf-old', session: 'wf-new' }), 'wf-old')
    // 在看的是别的板的成员：不影响这张板
    assert.equal(rep({ selected: 'wf-solo', session: 'wf-old' }), 'wf-old')
    // 在看的工作流自己还没有任务，但按目录已归到这张板：它代表这张板，下拉不会多出「它自己」一项
    const opts = buildWorkflowOptions(sharedStats, [{ workflow_id: 'wf-empty', title: '空的' }], { selected: 'wf-empty', selectedBoard: 'p7' })
    assert.deepEqual(opts.map(o => o.id), ['wf-empty', 'wf-solo'])
    assert.equal(opts[0].members?.length, 2)
  })

  test('没有项目名时用代表成员的会话标题；已知但没任务的成员不再单列；独立板与旧服务端（无 board_key）照旧', () => {
    const noName = sharedStats.slice(0, 2).map(s => ({ ...s, board_name: undefined }))
    const opts = buildWorkflowOptions(noName, [
      { workflow_id: 'wf-new', title: '会话新' },
      { workflow_id: 'wf-old', title: '会话旧' },
    ])
    assert.deepEqual(opts.map(o => [o.id, o.label]), [['wf-new', '会话新 · 2 个工作流']])
    // 旧服务端：没有 board_key / workflows，每个工作流各自一项
    const legacy = buildWorkflowOptions(
      [
        { workflow_id: 'a', total: 1, active: 1, updated_at: '' },
        { workflow_id: 'b', total: 1, active: 0, updated_at: '' },
      ],
      []
    )
    assert.deepEqual(legacy.map(o => o.id), ['a', 'b'])
    // 成员列表只有一个：不算共用
    const single = buildWorkflowOptions([{ workflow_id: 'a', board_key: 'p1', workflows: ['a'], total: 1, active: 1, updated_at: '' }], [])
    assert.equal(single[0].members, undefined)
    assert.equal(single[0].label, 'a')
  })

  test('共用板的成员清单与顶条文字：只有多个工作流共用时才有', () => {
    const known = [{ workflow_id: 'wf-new', title: '会话新' }]
    assert.deepEqual(boardMemberList(sharedStats[0], known), [
      { id: 'wf-new', label: '会话新' },
      { id: 'wf-old', label: 'wf-old' },
    ])
    assert.deepEqual(boardMemberList(sharedStats[2], known), [])
    assert.deepEqual(boardMemberList(null, known), [])
    assert.deepEqual(boardMemberList(undefined, known), [])
    assert.equal(boardChipText(sharedStats[0]), '共享板「主项目」 · 2 个工作流')
    assert.equal(boardChipText({ workflows: ['a', 'b', 'c'] }), '共享板 · 3 个工作流')
    assert.equal(boardChipText(sharedStats[2]), '')
    assert.equal(boardChipText(null), '')
  })
})

describe('阶段与双进度条', () => {
  test('四个阶段按 评估 → 方案 → 开发 → 改进 排列，都有名称与说明；不认识的值是「未标注」', () => {
    assert.deepEqual(WORK_STAGES, ['assess', 'plan', 'dev', 'improve'])
    assert.deepEqual(WORK_STAGES.map(stageLabel), ['评估', '方案', '开发', '改进'])
    for (const s of WORK_STAGES) assert.ok(WORK_STAGE_META[s].hint.length > 0)
    assert.equal(stageLabel(''), '未标注')
    assert.equal(stageLabel(undefined), '未标注')
    assert.equal(stageLabel('上线'), '未标注')
    assert.equal(isWorkStage('dev'), true)
    assert.equal(isWorkStage('Dev'), false)
    assert.equal(isWorkStage(''), false)
  })

  test('没标注阶段时按类型推导：调研 → 评估，改进 → 改进，其余 → 开发；推导出来的要标明', () => {
    assert.equal(defaultStageForKind('research'), 'assess')
    assert.equal(defaultStageForKind('improve'), 'improve')
    for (const k of ['bug', 'feature', 'ops', '', undefined]) assert.equal(defaultStageForKind(k), 'dev')
    const derived = stageView(item({ kind: 'research' }))
    assert.deepEqual([derived.stage, derived.index, derived.derived, derived.label], ['assess', 0, true, '评估'])
    const marked = stageView(item({ kind: 'research', stage: 'plan' }))
    assert.deepEqual([marked.stage, marked.index, marked.derived], ['plan', 1, false])
    // 阶段值写坏了：当作没标注
    assert.equal(stageView({ kind: 'bug', stage: 'nope' }).derived, true)
    assert.equal(stageView(item({ stage: '' })).derived, true)
  })

  test('状态条：待办 → 进行中 → 验证中 → 已完成 四格，已搁置不在主线上', () => {
    assert.deepEqual(STATE_TRACK, ['todo', 'doing', 'review', 'done'])
    assert.deepEqual(STATE_TRACK.map(s => stateTrack({ state: s }).index), [0, 1, 2, 3])
    const shelved = stateTrack({ state: 'shelved' })
    assert.deepEqual([shelved.index, shelved.shelved, shelved.label], [-1, true, '已搁置'])
    // 未知状态按待办处理，不崩
    assert.equal(stateTrack({ state: 'weird' as WorkState }).state, 'todo')
    assert.ok(stateTrack({ state: 'todo' }).hint.includes('等待决策'))
    assert.ok(stateTrack({ state: 'review' }).hint.includes('回归'))
    assert.ok(stateTrack({ state: 'done' }).hint.includes('验证通过'))
  })

  test('格子填充：走过的与当前的填满，搁置（-1）全空', () => {
    assert.deepEqual(trackFill(4, 0), [true, false, false, false])
    assert.deepEqual(trackFill(4, 2), [true, true, true, false])
    assert.deepEqual(trackFill(4, 3), [true, true, true, true])
    assert.deepEqual(trackFill(4, -1), [false, false, false, false])
  })

  test('进度条旁边的数字：只有手动填写或子任务比例才写，由状态推导的不写', () => {
    assert.equal(trackPercentLabel({ state: 'doing' }), null)
    assert.equal(trackPercentLabel({ state: 'doing', progress_pct: 50, progress_from: 'state' }), null)
    assert.equal(trackPercentLabel({ state: 'doing', progress_pct: 60, progress_from: 'explicit' }), '60%')
    assert.equal(trackPercentLabel({ state: 'doing', progress_pct: 33, progress_from: 'children' }), '33%')
  })

  test('分布统计：四个阶段与五个状态固定顺序，没有任务的也列出；推导的阶段计数并标出数量', () => {
    const items = [
      item({ id: 1, kind: 'research' }), // 推导 → 评估
      item({ id: 2, stage: 'plan', state: 'doing', progress_pct: 40, progress_from: 'explicit' }),
      item({ id: 3, stage: 'dev', state: 'review' }),
      item({ id: 4, stage: 'dev', state: 'done' }),
      item({ id: 5, stage: 'improve', state: 'shelved' }),
    ]
    const sum = summarizeTracks(items)
    assert.equal(sum.total, 5)
    assert.deepEqual(sum.stages, [
      { stage: 'assess', count: 1 },
      { stage: 'plan', count: 1 },
      { stage: 'dev', count: 2 },
      { stage: 'improve', count: 1 },
    ])
    assert.deepEqual(sum.states.map(s => [s.state, s.count]), [['todo', 1], ['doing', 1], ['review', 1], ['done', 1], ['shelved', 1]])
    assert.equal(sum.derivedStages, 1)
    assert.equal(sum.done, 1)
    // 整体完成度：已搁置的不计入；#1 待办 0、#2 显式 40、#3 验证中 90、#4 已完成 100 → 平均 57.5 → 58
    assert.equal(sum.percent, 58)
  })

  test('整体完成度只数叶子任务：批次本身不重复计入，批次的父任务不在结果里时按自己算', () => {
    const batch = item({ id: 10, state: 'doing', progress_pct: 50, progress_from: 'children' })
    const kidA = item({ id: 11, parent_id: 10, state: 'done' })
    const kidB = item({ id: 12, parent_id: 10, state: 'todo' })
    // 父 + 两个子：只数两个子 → (100 + 0) / 2
    assert.equal(summarizeTracks([batch, kidA, kidB]).percent, 50)
    // 父任务不在结果里：子任务各算各的
    assert.equal(summarizeTracks([kidA, kidB]).percent, 50)
    // 只有批次本身（子任务被筛掉了）：按它自己的进度
    assert.equal(summarizeTracks([batch]).percent, 50)
    // 空列表与全部搁置：0，不出现 NaN
    assert.equal(summarizeTracks([]).percent, 0)
    assert.equal(summarizeTracks([item({ state: 'shelved' })]).percent, 0)
    assert.equal(summarizeTracks([]).total, 0)
  })

  test('汇总不改动入参', () => {
    const items = [item({ id: 20, stage: 'dev' }), item({ id: 21 })]
    const snapshot = JSON.stringify(items)
    summarizeTracks(items)
    assert.equal(JSON.stringify(items), snapshot)
  })

  test('本对话对任务做了什么：创建 / 推进次数 / 状态变化；只是被关联的也要有话说', () => {
    assert.equal(describeSessionCtx(undefined), '')
    assert.equal(describeSessionCtx(null), '')
    assert.equal(describeSessionCtx({ created: true, updates: 1 }), '本对话创建')
    assert.equal(describeSessionCtx({ created: true, updates: 3, from_state: 'todo', to_state: 'review' }), '本对话创建 · 推进 2 次 · 待办 → 验证中')
    assert.equal(describeSessionCtx({ created: false, updates: 2, from_state: 'doing', to_state: 'review' }), '本对话推进 2 次 · 进行中 → 验证中')
    assert.equal(describeSessionCtx({ created: false, updates: 1 }), '本对话推进 1 次')
    assert.equal(describeSessionCtx({ created: false, updates: 0 }), '本对话关联')
  })
})

describe('会话页按轮次分组任务', () => {
  const link = (session_id: string, work_item_id: number, over: Partial<WorkSessionLink> = {}): WorkSessionLink => ({
    session_id,
    work_item_id,
    created: false,
    updates: 1,
    ...over,
  })

  test('按会话分组；本轮创建的在前，其余在后，各自按任务序号升序', () => {
    const a = item({ id: 101, seq: 1 })
    const b = item({ id: 102, seq: 2 })
    const c = item({ id: 103, seq: 3 })
    const d = item({ id: 104, seq: 4 })
    const groups = groupSessionTasks(
      [d, c, b, a],
      [
        link('s1', 104),
        link('s1', 102, { created: true, updates: 3, from_state: 'todo', to_state: 'review' }),
        link('s1', 101),
        link('s1', 103, { created: true }),
        link('s2', 101, { created: true }),
      ]
    )
    assert.deepEqual([...groups.keys()].sort(), ['s1', 's2'])
    assert.deepEqual(groups.get('s1')!.map(t => t.item.seq), [2, 3, 1, 4]) // 先创建的 #2 #3，再 #1 #4
    assert.deepEqual(groups.get('s2')!.map(t => t.item.seq), [1])
    const second = groups.get('s1')![0]
    assert.deepEqual([second.ctx.created, second.ctx.updates, second.ctx.from_state, second.ctx.to_state], [true, 3, 'todo', 'review'])
  })

  test('关联指向不存在的任务（已删除）或没有会话编号时丢弃；没有关联的会话不出现；空输入返回空表', () => {
    const only = item({ id: 201, seq: 1 })
    const groups = groupSessionTasks([only], [link('s1', 999), link('', 201), link('s1', 201)])
    assert.equal(groups.get('s1')!.length, 1)
    assert.equal(groups.size, 1)
    assert.equal(groupSessionTasks([], []).size, 0)
    assert.equal(groupSessionTasks([only], []).size, 0)
  })

  test('不改动入参，也不把关联对象本身塞进结果（结果里的 ctx 是拷贝）', () => {
    const it = item({ id: 301, seq: 1 })
    const links = [link('s1', 301, { created: true })]
    const snapshot = JSON.stringify([[it], links])
    const g = groupSessionTasks([it], links)
    assert.equal(JSON.stringify([[it], links]), snapshot)
    assert.notEqual(g.get('s1')![0].ctx, links[0] as unknown)
  })
})


describe('countActiveWorkFilters（手机「筛选」按钮角标）', () => {
  const base = { kind: '', sort: 'plan' as const, closedDays: 30, showClosedWindow: false }

  test('全是默认值时为 0', () => {
    assert.equal(countActiveWorkFilters(base, 30), 0)
    assert.equal(countActiveWorkFilters({ ...base, showClosedWindow: true }, 30), 0)
  })

  test('类型、排序、已结束时间窗各算一条，互不影响', () => {
    assert.equal(countActiveWorkFilters({ ...base, kind: 'bug' }, 30), 1)
    assert.equal(countActiveWorkFilters({ ...base, sort: 'recent' }, 30), 1)
    assert.equal(countActiveWorkFilters({ ...base, closedDays: 7, showClosedWindow: true }, 30), 1)
    assert.equal(countActiveWorkFilters({ kind: 'ops', sort: 'recent', closedDays: 0, showClosedWindow: true }, 30), 3)
  })

  test('已完成 / 已搁置的芯片都没开时，时间窗不起作用，改了也不计数', () => {
    assert.equal(countActiveWorkFilters({ ...base, closedDays: 7, showClosedWindow: false }, 30), 0)
    assert.equal(countActiveWorkFilters({ ...base, closedDays: 0, kind: 'bug', showClosedWindow: false }, 30), 1)
  })
})

describe('inBoard（任务属不属于当前清单：同一个项目的工作流共用一张任务板）', () => {
  const item = (workflow_id: string, board_key?: string) => ({ workflow_id, board_key })

  test('带 board_key 时按板比较：别的工作流创建、同一张板上的任务也属于当前清单', () => {
    assert.equal(inBoard(item('wf-b', 'p1'), 'p1', 'wf-a'), true)
    assert.equal(inBoard(item('wf-a', 'p2'), 'p1', 'wf-a'), false, '创建者相同但在另一张板上，不属于')
  })

  test('板还没取到时退回按工作流比较', () => {
    assert.equal(inBoard(item('wf-a', 'p1'), '', 'wf-a'), true)
    assert.equal(inBoard(item('wf-b', 'p1'), '', 'wf-a'), false)
  })

  test('旧服务端的任务没有 board_key 时退回按工作流比较', () => {
    assert.equal(inBoard(item('wf-a'), 'p1', 'wf-a'), true)
    assert.equal(inBoard(item('wf-b'), 'p1', 'wf-a'), false)
  })
})

describe('共享板的协作（#175 B2：承接者 / 接取状态 / 时间线事件）', () => {
  const known = [
    { workflow_id: 'wf-a', title: '主线开发' },
    { workflow_id: 'wf-b', title: '' },
    { workflow_id: 'wf-c', title: '体验打磨' },
  ]

  test('接取状态与协作事件的名称、色调齐全；不认识的值返回空串', () => {
    assert.deepEqual(['offered', 'accepted', 'declined'].map(claimLabel), ['待接取', '已接取', '已拒绝'])
    assert.equal(claimLabel(''), '')
    assert.equal(claimLabel(undefined), '')
    assert.equal(claimLabel('mystery'), '')
    assert.deepEqual(
      ['assign', 'reassign', 'unassign', 'accept', 'decline', 'deliver', 'accept_delivery', 'reject_delivery'].map(eventLabel),
      ['指派', '改派', '撤销指派', '接取', '拒绝', '交付', '验收通过', '退回']
    )
    assert.equal(eventLabel('progress'), '', '普通进展没有事件标签')
    assert.equal(eventLabel(''), '')
    assert.equal(eventTone('decline'), 'rose')
    assert.equal(eventTone('accept'), 'emerald')
    assert.equal(eventTone('mystery'), 'slate')
    assert.equal(Object.keys(WORK_CLAIM_META).length, 3)
    assert.equal(Object.keys(WORK_EVENT_META).length, 8)
  })

  test('工作流的展示名：优先会话标题，没有标题退回编号', () => {
    assert.equal(workflowLabel('wf-a', known), '主线开发')
    assert.equal(workflowLabel('wf-b', known), 'wf-b')
    assert.equal(workflowLabel('wf-x', known), 'wf-x')
    assert.equal(workflowLabel('', known), '')
    assert.equal(workflowLabel(undefined, known), '')
  })

  test('没有指派的任务没有承接者视图——单工作流的板上界面不变', () => {
    assert.equal(assigneeView(item(), known), null)
    assert.equal(isAssigned(item()), false)
    assert.equal(isAssigned(item({ assignee_workflow_id: 'wf-b' })), true)
  })

  test('承接者视图：待接取 / 已接取 / 已拒绝各有标签与色调，派发者是人时写「网页」', () => {
    const offered = assigneeView(item({ assignee_workflow_id: 'wf-b', assigned_by_workflow_id: '', claim_state: 'offered' }), known)!
    assert.equal(offered.id, 'wf-b')
    assert.equal(offered.label, 'wf-b')
    assert.equal(offered.claimLabel, '待接取')
    assert.equal(offered.tone, 'amber')
    assert.equal(offered.byLabel, '网页')
    assert.equal(offered.collaborative, true)

    const accepted = assigneeView(item({ assignee_workflow_id: 'wf-c', assigned_by_workflow_id: 'wf-a', claim_state: 'accepted' }), known)!
    assert.equal(accepted.label, '体验打磨')
    assert.equal(accepted.claimLabel, '已接取')
    assert.equal(accepted.tone, 'emerald')
    assert.equal(accepted.byLabel, '主线开发')

    const declined = assigneeView(item({ assignee_workflow_id: 'wf-c', assigned_by_workflow_id: 'wf-a', claim_state: 'declined' }), known)!
    assert.equal(declined.claimLabel, '已拒绝')
    assert.equal(declined.tone, 'rose')
    assert.ok(declined.hint.includes('改派'))
  })

  test('自己派给自己的任务不算协作；没有接取状态时没有状态标签', () => {
    const self = assigneeView(item({ assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-a' }), known)!
    assert.equal(self.collaborative, false)
    assert.equal(self.claim, '')
    assert.equal(self.claimLabel, '')
    assert.equal(self.tone, 'zinc')
  })

  test('承接者过滤：全部 / 未指派 / 某个工作流；与类型、搜索叠加', () => {
    const a = item({ id: 101, kind: 'bug', assignee_workflow_id: 'wf-b', claim_state: 'offered' })
    const b = item({ id: 102, kind: 'feature', assignee_workflow_id: 'wf-c', claim_state: 'accepted' })
    const c = item({ id: 103, kind: 'bug' })
    const all = [a, b, c]
    assert.deepEqual(filterItems(all, { assignee: ASSIGNEE_ALL }).map(i => i.id), [101, 102, 103])
    assert.deepEqual(filterItems(all, {}).map(i => i.id), [101, 102, 103])
    assert.deepEqual(filterItems(all, { assignee: ASSIGNEE_NONE }).map(i => i.id), [103])
    assert.deepEqual(filterItems(all, { assignee: 'wf-b' }).map(i => i.id), [101])
    assert.deepEqual(filterItems(all, { assignee: 'wf-c', kind: 'bug' }).map(i => i.id), [])
    assert.deepEqual(filterItems(all, { assignee: ASSIGNEE_NONE, kind: 'bug' }).map(i => i.id), [103])
    assert.equal(matchesAssignee(a, ''), true)
    assert.equal(matchesAssignee(a, 'wf-x'), false)
  })

  test('承接者选项：清单里没有任何指派时不出现；有就按任务数从多到少，带「全部」「未指派」', () => {
    assert.deepEqual(assigneeOptions([item(), item()], known), [])
    const items = [
      item({ assignee_workflow_id: 'wf-c' }),
      item({ assignee_workflow_id: 'wf-b' }),
      item({ assignee_workflow_id: 'wf-b' }),
      item(),
    ]
    const opts = assigneeOptions(items, known)
    assert.deepEqual(
      opts.map(o => [o.value, o.label, o.count]),
      [
        [ASSIGNEE_ALL, '全部承接者', 4],
        [ASSIGNEE_NONE, '未指派', 1],
        ['wf-b', 'wf-b', 2],
        ['wf-c', '体验打磨', 1],
      ]
    )
  })

  test('承接者过滤也计入手机「筛选」角标；选「全部」不计', () => {
    const base = { kind: '', sort: 'plan' as const, closedDays: 30, showClosedWindow: false }
    assert.equal(countActiveWorkFilters({ ...base, assignee: ASSIGNEE_ALL }, 30), 0)
    assert.equal(countActiveWorkFilters({ ...base, assignee: '' }, 30), 0)
    assert.equal(countActiveWorkFilters({ ...base, assignee: 'wf-b' }, 30), 1)
    assert.equal(countActiveWorkFilters({ ...base, assignee: ASSIGNEE_NONE, kind: 'bug' }, 30), 2)
  })

  test('行上的创建者标记：只在共享板上、任务不是正在看的这个工作流创建的才标', () => {
    assert.equal(showCreator({ workflow_id: 'wf-b' }, 'wf-a', true), true)
    assert.equal(showCreator({ workflow_id: 'wf-a' }, 'wf-a', true), false, '自己创建的不标')
    assert.equal(showCreator({ workflow_id: 'wf-b' }, 'wf-a', false), false, '单工作流的板永远不标')
    assert.equal(showCreator({ workflow_id: 'wf-b' }, '', true), false, '没选工作流时不标')
    assert.equal(showCreator({ workflow_id: '' }, 'wf-a', true), false)
  })
})

describe('待接取与协作快捷筛选（#180 B3b）', () => {
  const NOW = Date.parse('2026-10-22T12:00:00Z')
  const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString()
  const offered = (over: Partial<WorkItem> = {}) =>
    item({ assignee_workflow_id: 'wf-b', assigned_by_workflow_id: 'wf-a', claim_state: 'offered', assigned_at: minutesAgo(30), ...over })

  test('等待时长的人话：不到 1 分钟 / 分钟 / 小时加分 / 天加小时', () => {
    assert.equal(formatWaited(0), '不到 1 分钟')
    assert.equal(formatWaited(59_999), '不到 1 分钟')
    assert.equal(formatWaited(-5_000), '不到 1 分钟', '时钟偏差导致的负数按 0 处理')
    assert.equal(formatWaited(60_000), '1 分钟')
    assert.equal(formatWaited(47 * 60_000), '47 分钟')
    assert.equal(formatWaited(60 * 60_000), '1 小时')
    assert.equal(formatWaited(200 * 60_000), '3 小时 20 分')
    assert.equal(formatWaited(47 * 3600_000 + 59 * 60_000), '47 小时 59 分', '48 小时以内仍按小时 + 分钟')
    assert.equal(formatWaited(48 * 3600_000), '2 天')
    assert.equal(formatWaited(51 * 3600_000 + 30 * 60_000), '2 天 3 小时')
  })

  test('只有「协作、待接取、没结束」的任务才有等待时长，其余一律 null', () => {
    assert.equal(offerWaitMs(offered(), NOW), 30 * 60_000)
    assert.equal(offerWaitMs(offered({ claim_state: 'accepted' }), NOW), null, '已接取不算在等')
    assert.equal(offerWaitMs(offered({ claim_state: 'declined' }), NOW), null)
    assert.equal(offerWaitMs(offered({ claim_state: undefined }), NOW), null)
    assert.equal(offerWaitMs(offered({ state: 'done' }), NOW), null, '已完成的不算')
    assert.equal(offerWaitMs(offered({ state: 'shelved' }), NOW), null, '已搁置的不算')
    assert.equal(offerWaitMs(offered({ assigned_by_workflow_id: 'wf-b' }), NOW), null, '自己派给自己的不是协作')
    assert.equal(offerWaitMs(offered({ assignee_workflow_id: undefined }), NOW), null)
    assert.equal(offerWaitMs(offered({ assigned_at: undefined }), NOW), null, '没有派出时间无从算起')
    assert.equal(offerWaitMs(offered({ assigned_at: 'not-a-date' }), NOW), null)
    assert.equal(offerWaitMs(offered({ assigned_by_workflow_id: '' }), NOW), 30 * 60_000, '用户在网页上派的（派发者为空）也算')
    assert.equal(offerWaitMs(offered({ assigned_at: new Date(NOW + 60_000).toISOString() }), NOW), 0, '派出时间在「现在」之后（时钟偏差）按 0')
  })

  test('「已等 X」与超时标红：恰好 2 小时算超时，差一毫秒不算', () => {
    assert.deepEqual(offerWaitView(offered({ assigned_at: minutesAgo(47) }), NOW), { label: '已等 47 分钟', stale: false })
    assert.deepEqual(offerWaitView(offered({ assigned_at: new Date(NOW - 20_000).toISOString() }), NOW), { label: '刚派出', stale: false }, '不到 1 分钟写「刚派出」')
    assert.deepEqual(offerWaitView(offered({ assigned_at: minutesAgo(1) }), NOW), { label: '已等 1 分钟', stale: false })
    assert.deepEqual(offerWaitView(offered({ assigned_at: minutesAgo(200) }), NOW), { label: '已等 3 小时 20 分', stale: true })
    assert.equal(offerWaitView(offered({ assigned_at: new Date(NOW - OFFER_STALE_MS).toISOString() }), NOW)!.stale, true)
    assert.equal(offerWaitView(offered({ assigned_at: new Date(NOW - OFFER_STALE_MS + 1).toISOString() }), NOW)!.stale, false)
    assert.equal(offerWaitView(offered({ claim_state: 'accepted' }), NOW), null)
    assert.equal(OFFER_STALE_MS, 2 * 3600_000, '与回执头 board: 行的默认阈值同为 2 小时')
  })

  test('阈值可以在设置里改（#180 B3d）：标红跟着传入的阈值走，标签不变；等于即算', () => {
    const min = (m: number) => m * 60_000
    // 阈值调到 30 分钟：等了 45 分钟就标红；调到 4 小时：等了 200 分钟还不标红
    assert.deepEqual(offerWaitView(offered({ assigned_at: minutesAgo(45) }), NOW, min(30)), { label: '已等 45 分钟', stale: true })
    assert.deepEqual(offerWaitView(offered({ assigned_at: minutesAgo(200) }), NOW, min(240)), { label: '已等 3 小时 20 分', stale: false })
    // 边界：恰好等于阈值算超时，差一毫秒不算（与服务端「等于即算」同口径）
    assert.equal(offerWaitView(offered({ assigned_at: new Date(NOW - min(90)).toISOString() }), NOW, min(90))!.stale, true)
    assert.equal(offerWaitView(offered({ assigned_at: new Date(NOW - min(90) + 1).toISOString() }), NOW, min(90))!.stale, false)
    // 不传阈值就是出厂默认 2 小时；不是待接取的协作任务仍是 null
    assert.equal(offerWaitView(offered({ assigned_at: minutesAgo(119) }), NOW)!.stale, false)
    assert.equal(offerWaitView(offered({ assigned_at: minutesAgo(121) }), NOW)!.stale, true)
    assert.equal(offerWaitView(offered({ claim_state: 'accepted' }), NOW, min(30)), null)
  })

  test('侧栏徽标的悬停说明：带个数与最久已等；没有派出时间就只说个数', () => {
    assert.equal(
      offeredBadgeTitle({ workflow_id: 'wf-b', count: 3, oldest_at: minutesAgo(47) }, NOW),
      '3 项指派在等这个工作流接取，最久已等 47 分钟'
    )
    assert.equal(
      offeredBadgeTitle({ workflow_id: 'wf-b', count: 2, oldest_at: new Date(NOW - 10_000).toISOString() }, NOW),
      '2 项指派在等这个工作流接取，刚派出'
    )
    assert.equal(offeredBadgeTitle({ workflow_id: 'wf-b', count: 1 }, NOW), '1 项指派在等这个工作流接取')
    assert.equal(offeredBadgeTitle({ workflow_id: 'wf-b', count: 1, oldest_at: 'bad' }, NOW), '1 项指派在等这个工作流接取')
  })

  test('快捷项的取值与说明齐全，isWorkQuick 只认这三个', () => {
    assert.deepEqual([...WORK_QUICK_KEYS], ['to_me', 'by_me', 'offered'])
    assert.deepEqual(WORK_QUICK_KEYS.map(k => WORK_QUICK_META[k].label), ['指派给我', '我派出的', '待接取'])
    for (const k of WORK_QUICK_KEYS) assert.ok(WORK_QUICK_META[k].hint.length > 0)
    assert.equal(isWorkQuick('to_me'), true)
    assert.equal(isWorkQuick(''), false)
    assert.equal(isWorkQuick('mine'), false)
    assert.equal(isWorkQuick(undefined), false)
  })

  test('任务页地址 ?seq=（板内序号 #N，#242）：只认正整数的十进制写法，多值取第一个，其余一律当没带', () => {
    assert.equal(parseSeqQuery('36'), 36)
    assert.equal(parseSeqQuery(' 7 '), 7, '两端空白忽略')
    assert.equal(parseSeqQuery(['12', '99']), 12, '同名参数出现多次取第一个（vue-router 给数组）')
    assert.equal(parseSeqQuery('999999999'), 999999999)
    for (const bad of ['', ' ', 'abc', '0', '-1', '1.5', '1e3', '0x10', '007', '+3', '1 2', '1000000000', null, undefined, 12, [], [''], [null], {}]) {
      assert.equal(parseSeqQuery(bad), null, `应判为不合法：${JSON.stringify(bad)}`)
    }
  })

  test('指派给我 / 我派出的：以正在看的工作流为准，自己派给自己的都不算', () => {
    const toMe = offered({ assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-b' })
    const byMe = offered({ assignee_workflow_id: 'wf-b', assigned_by_workflow_id: 'wf-a' })
    const human = offered({ assignee_workflow_id: 'wf-a', assigned_by_workflow_id: '' })
    const self = offered({ assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-a' })
    const plain = item()
    assert.equal(matchesQuick(toMe, 'to_me', 'wf-a'), true)
    assert.equal(matchesQuick(human, 'to_me', 'wf-a'), true, '用户在网页上派给我的也算')
    assert.equal(matchesQuick(self, 'to_me', 'wf-a'), false)
    assert.equal(matchesQuick(byMe, 'to_me', 'wf-a'), false)
    assert.equal(matchesQuick(byMe, 'by_me', 'wf-a'), true)
    assert.equal(matchesQuick(self, 'by_me', 'wf-a'), false)
    assert.equal(matchesQuick(toMe, 'by_me', 'wf-a'), false)
    assert.equal(matchesQuick(plain, 'by_me', 'wf-a'), false)
    assert.equal(matchesQuick(toMe, 'to_me', ''), false, '没有 viewer 时「我」不存在，什么都不匹配')
    assert.equal(matchesQuick(byMe, 'by_me', ''), false)
  })

  test('待接取：不分承接者，已结束的不算；空串与不认识的值不过滤', () => {
    assert.equal(matchesQuick(offered(), 'offered', 'wf-a'), true)
    assert.equal(matchesQuick(offered({ assignee_workflow_id: 'wf-z' }), 'offered', ''), true, '不依赖 viewer')
    assert.equal(matchesQuick(offered({ claim_state: 'accepted' }), 'offered', 'wf-a'), false)
    assert.equal(matchesQuick(offered({ state: 'done' }), 'offered', 'wf-a'), false)
    assert.equal(matchesQuick(item(), '', 'wf-a'), true)
    assert.equal(matchesQuick(item(), 'whatever', 'wf-a'), true)
  })

  test('filterItems 带 quick：与类型、承接者叠加；不带 viewer 的 to_me 得到空', () => {
    const a = offered({ id: 201, kind: 'bug' })
    const b = offered({ id: 202, kind: 'feature', assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-b', claim_state: 'accepted' })
    const c = item({ id: 203, kind: 'bug' })
    const all = [a, b, c]
    assert.deepEqual(filterItems(all, { quick: 'offered', viewer: 'wf-a' }).map(i => i.id), [201])
    assert.deepEqual(filterItems(all, { quick: 'to_me', viewer: 'wf-a' }).map(i => i.id), [202])
    assert.deepEqual(filterItems(all, { quick: 'by_me', viewer: 'wf-a' }).map(i => i.id), [201])
    assert.deepEqual(filterItems(all, { quick: 'by_me', viewer: 'wf-a', kind: 'feature' }).map(i => i.id), [])
    assert.deepEqual(filterItems(all, { quick: 'by_me', viewer: 'wf-a', assignee: 'wf-b' }).map(i => i.id), [201])
    assert.deepEqual(filterItems(all, { quick: 'to_me' }).map(i => i.id), [])
    assert.deepEqual(filterItems(all, { quick: '' }).map(i => i.id), [201, 202, 203])
  })

  test('快捷项上的数字：只数还没结束的任务', () => {
    const items = [
      offered({ id: 301 }),
      offered({ id: 302, assignee_workflow_id: 'wf-c' }),
      offered({ id: 303, assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-b' }),
      offered({ id: 304, assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-b', claim_state: 'accepted', state: 'doing' }),
      offered({ id: 305, assignee_workflow_id: 'wf-a', assigned_by_workflow_id: 'wf-b', claim_state: 'accepted', state: 'done' }),
      offered({ id: 306, state: 'shelved' }),
      item({ id: 307 }),
    ]
    assert.deepEqual(quickCounts(items, 'wf-a'), { to_me: 2, by_me: 2, offered: 3 })
    // wf-b：301 是派给它的；303 / 304 是它派给 wf-a 的
    assert.deepEqual(quickCounts(items, 'wf-b'), { to_me: 1, by_me: 2, offered: 3 }, '换成别的工作流，「我」跟着变，待接取不变')
    assert.deepEqual(quickCounts(items, ''), { to_me: 0, by_me: 0, offered: 3 })
    assert.deepEqual(quickCounts([], 'wf-a'), { to_me: 0, by_me: 0, offered: 0 })
  })

  test('快捷筛选也计入手机「筛选」角标', () => {
    const base = { kind: '', sort: 'plan' as const, closedDays: 30, showClosedWindow: false }
    assert.equal(countActiveWorkFilters({ ...base, quick: '' }, 30), 0)
    assert.equal(countActiveWorkFilters({ ...base, quick: 'offered' }, 30), 1)
    assert.equal(countActiveWorkFilters({ ...base, quick: 'to_me', assignee: 'wf-b', kind: 'bug' }, 30), 3)
  })
})
