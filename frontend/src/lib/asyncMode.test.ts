import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import type { AsyncModeView, AuthProfile } from '../api/types'
import {
  TASK_CARD_MAX,
  buildTaskUpdateCards,
  isNotice,
  levelLabel,
  onlyRounds,
  profileChipText,
  profileChipTitle,
  profileOptionLabel,
  showPhaseSlider,
  summarizeAsyncMode,
  validateProfileDraft,
} from './asyncMode'

const profile: AuthProfile = {
  id: 1, name: '标准授权', level: 'L1', rules: '可回滚的直接做', is_default: true, sort: 0,
  created_at: '2026-10-13T00:00:00Z', updated_at: '2026-10-13T00:00:00Z',
}

function view(over: Partial<AsyncModeView> = {}): AsyncModeView {
  return {
    workflow_id: 'wf', enabled: false, ever_enabled: false, effective: false, mode: 'sync',
    can_enable: false, hook_fresh_minutes: 60, profile_id: 0, profile, ...over,
  }
}

describe('summarizeAsyncMode', () => {
  test('还没取到：加载中，开关不可点', () => {
    const s = summarizeAsyncMode(null)
    assert.equal(s.state, 'unknown')
    assert.equal(s.canToggle, false)
  })

  test('从未开启 + 没见过 hook：不能开，并告诉人先装 hook', () => {
    const s = summarizeAsyncMode(view())
    assert.equal(s.state, 'never')
    assert.equal(s.canToggle, false)
    assert.match(s.blockedReason, /hook/)
    assert.match(s.detail, /先安装/)
  })

  test('从未开启 + 见过 hook：可以开', () => {
    const s = summarizeAsyncMode(view({ can_enable: true }))
    assert.equal(s.state, 'never')
    assert.equal(s.canToggle, true)
    assert.equal(s.blockedReason, '')
  })

  test('生效：说明里写明不可逆操作要您批准才执行（发 L2 确认卡，不阻断其余工作）', () => {
    const s = summarizeAsyncMode(view({ enabled: true, ever_enabled: true, effective: true, mode: 'async', can_enable: true }))
    assert.equal(s.state, 'active')
    assert.equal(s.canToggle, true)
    assert.match(s.detail, /不可逆操作/)
    assert.match(s.detail, /L2 确认卡/)
    assert.match(s.detail, /批准前不会执行/)
    assert.match(s.detail, /不会因此停下其他工作/)
    // 旧说法「仍会停下来问您」会让人以为智能体仍在阻塞等待，不能再出现
    assert.doesNotMatch(s.detail, /停下来问您/)
  })

  test('开着却没生效：沿用服务端给的原因，开关仍能点（随时可关）', () => {
    const s = summarizeAsyncMode(view({
      enabled: true, ever_enabled: true, reason: 'hook_stale', reason_text: 'hook 已超过 60 分钟没有出现', can_enable: true,
    }))
    assert.equal(s.state, 'degraded')
    assert.equal(s.canToggle, true)
    assert.match(s.detail, /60 分钟/)
  })

  test('开着但连 can_enable 都是假（重启后 hook 记录清空）：仍然能关', () => {
    const s = summarizeAsyncMode(view({ enabled: true, ever_enabled: true, reason: 'no_hook', can_enable: false }))
    assert.equal(s.state, 'degraded')
    assert.equal(s.canToggle, true)
  })

  test('关过：已关闭；hook 还在时可再开，不在时不能再开', () => {
    assert.equal(summarizeAsyncMode(view({ ever_enabled: true, reason: 'disabled', can_enable: true })).canToggle, true)
    const gone = summarizeAsyncMode(view({ ever_enabled: true, reason: 'disabled', can_enable: false }))
    assert.equal(gone.state, 'off')
    assert.equal(gone.canToggle, false)
  })
})

describe('阶段滑块与档位标签', () => {
  test('只有异步真正生效时才隐藏阶段滑块', () => {
    assert.equal(showPhaseSlider(null), true)
    assert.equal(showPhaseSlider(view()), true)
    assert.equal(showPhaseSlider(view({ enabled: true, effective: false })), true)
    assert.equal(showPhaseSlider(view({ enabled: true, effective: true })), false)
  })

  test('档位标签与悬停说明', () => {
    assert.equal(profileChipText(view()), '授权：标准授权')
    assert.equal(profileChipText(null), '授权档位')
    const title = profileChipTitle(view())
    assert.match(title, /标准授权/)
    assert.match(title, /L1 可回滚的改动直接做/)
    assert.match(title, /可回滚的直接做/)
    assert.equal(profileChipTitle(null), '')
  })

  test('档位下拉项与等级文案', () => {
    assert.equal(profileOptionLabel(profile), '标准授权 · L1（默认）')
    assert.equal(profileOptionLabel({ name: '谨慎', level: 'L0', is_default: false }), '谨慎 · L0')
    assert.equal(levelLabel('L0'), '只读，写入先问')
    assert.equal(levelLabel('Lx'), 'Lx')
  })
})

describe('notice', () => {
  test('只有 kind=notice 才是进度气泡', () => {
    assert.equal(isNotice({ kind: 'notice' }), true)
    assert.equal(isNotice({}), false)
    assert.equal(isNotice(null), false)
    assert.deepEqual(onlyRounds([{ kind: 'notice' as const }, {}, { kind: undefined }]).length, 2)
  })
})

describe('validateProfileDraft', () => {
  const limits = { name_max: 24, rules_max: 240 }
  const ok = { name: '谨慎', level: 'L0', rules: '只读' }

  test('合格的草稿没有问题', () => {
    assert.equal(validateProfileDraft(ok, limits), '')
  })

  test('逐项拦截：名称、等级（没有 L2）、规则', () => {
    assert.match(validateProfileDraft({ ...ok, name: '  ' }, limits), /名称不能为空/)
    assert.match(validateProfileDraft({ ...ok, name: 'a\nb' }, limits), /不能换行/)
    assert.match(validateProfileDraft({ ...ok, name: '字'.repeat(25) }, limits), /不能超过 24/)
    assert.match(validateProfileDraft({ ...ok, level: 'L2' }, limits), /只能是 L0 或 L1/)
    assert.match(validateProfileDraft({ ...ok, rules: '' }, limits), /规则摘要不能为空/)
    assert.match(validateProfileDraft({ ...ok, rules: '字'.repeat(241) }, limits), /不能超过 240/)
  })

  test('按字数而不是字节数计：24 个汉字刚好合格', () => {
    assert.equal(validateProfileDraft({ ...ok, name: '字'.repeat(24) }, limits), '')
  })
})

describe('buildTaskUpdateCards（每次更新一张）', () => {
  const since = '2026-10-13T10:00:00Z'
  let nextId = 1
  function upd(seq: number, over: Record<string, unknown> = {}) {
    return {
      id: nextId++, seq, title: `任务${seq}`, state: 'doing',
      created_at: '2026-10-13T10:30:00Z', ...over,
    } as Parameters<typeof buildTaskUpdateCards>[0][number]
  }

  test('没开启过（没有开启时间）→ 没有卡片', () => {
    assert.deepEqual(buildTaskUpdateCards([upd(1)], undefined), [])
    assert.deepEqual(buildTaskUpdateCards([upd(1)], ''), [])
    assert.deepEqual(buildTaskUpdateCards([upd(1)], 'not-a-date'), [])
  })

  test('只要开启之后提交的更新：开启之前的历史不进时间线', () => {
    const cards = buildTaskUpdateCards([
      upd(1, { created_at: '2026-10-13T09:59:59Z' }),
      upd(2, { created_at: since }),
      upd(3, { created_at: '2026-10-13T10:00:01Z' }),
    ], since)
    assert.deepEqual(cards.map(c => c.seq), [3])
  })

  test('同一个任务的几次更新各占一张，留在各自的提交时间上；按时间升序，时间相同按更新编号', () => {
    const cards = buildTaskUpdateCards([
      upd(7, { id: 30, created_at: '2026-10-13T11:00:00Z', text: '第三次' }),
      upd(7, { id: 10, created_at: '2026-10-13T10:10:00Z', text: '第一次' }),
      upd(2, { id: 21, created_at: '2026-10-13T10:10:00Z', text: '同一时刻的另一个任务' }),
      upd(7, { id: 20, created_at: '2026-10-13T10:40:00Z', text: '第二次' }),
    ], since)
    assert.deepEqual(cards.map(c => c.text), ['第一次', '同一时刻的另一个任务', '第二次', '第三次'])
    assert.deepEqual(cards.map(c => c.key), ['task-u10', 'task-u21', 'task-u20', 'task-u30'])
    assert.equal(new Set(cards.map(c => c.key)).size, 4)
  })

  test('动词：创建那条（没有变化前的状态）→ 新建；改了状态按变化后的状态；只更新进度 / 备注 → 进展', () => {
    const cards = buildTaskUpdateCards([
      upd(1, { to_state: 'doing' }),
      upd(2, { from_state: 'todo', to_state: 'doing' }),
      upd(3, { from_state: 'doing', to_state: 'review' }),
      upd(4, { from_state: 'review', to_state: 'done' }),
      upd(5, { from_state: 'doing', to_state: 'shelved' }),
      upd(6, { from_state: 'doing', to_state: 'todo' }),
      upd(7, { state: 'doing' }),
    ], since)
    assert.deepEqual(cards.map(c => c.verb), ['新建', '进行中', '验证中', '已完成', '已搁置', '待办', '进展'])
  })

  test('徽标状态：有状态变化取变化后的，只更新进度时取任务现在的状态', () => {
    const [a, b] = buildTaskUpdateCards([
      upd(1, { from_state: 'doing', to_state: 'review', state: 'done' }),
      upd(2, { state: 'doing' }),
    ], since)
    assert.equal(a.state, 'review')
    assert.equal(b.state, 'doing')
  })

  test('正文：多余空白折叠，过长截断，没有文字时为空串', () => {
    const cards = buildTaskUpdateCards([
      upd(1, { text: '结论  已完成\n全部' }),
      upd(2, { text: '字'.repeat(300) }),
      upd(3, {}),
    ], since)
    assert.equal(cards[0].text, '结论 已完成 全部')
    assert.equal([...cards[1].text].length, 141)
    assert.ok(cards[1].text.endsWith('…'))
    assert.equal(cards[2].text, '')
  })

  test('最多保留最近的 N 张', () => {
    const many = Array.from({ length: 60 }, (_, i) =>
      upd(i + 1, { created_at: new Date(Date.parse(since) + (i + 1) * 60000).toISOString() }))
    const cards = buildTaskUpdateCards(many, since)
    assert.equal(cards.length, TASK_CARD_MAX)
    assert.equal(cards[0].seq, 60 - TASK_CARD_MAX + 1)
    assert.equal(cards[cards.length - 1].seq, 60)
    assert.deepEqual(buildTaskUpdateCards(many, since, 3).map(c => c.seq), [58, 59, 60])
    assert.deepEqual(buildTaskUpdateCards(many, since, 0), [])
  })
})
