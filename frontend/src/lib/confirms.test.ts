import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import type { ConfirmItem, ConfirmOption } from '../api/confirms'
import {
  adoptPreview,
  ageText,
  adoptableItems,
  answerLead,
  answerText,
  answeredByMain,
  canAdoptRecommended,
  checkSelection,
  clockText,
  customAnswerOf,
  decisionAdoptPreview,
  decisionAnswerText,
  decisionAnswerable,
  escalationText,
  groupByRisk,
  matchDecisionViews,
  pendingChipText,
  pendingChipTitle,
  recallOptions,
  recallTimes,
  recommendedIds,
  riskHint,
  riskTone,
  routedToMain,
  selectionAfterCustomToggle,
  summarizeDecisions,
  toggleSelection,
  type DocDecisionView,
} from './confirms'

const opts = (...spec: Array<[string, boolean?]>): ConfirmOption[] =>
  spec.map(([id, rec]) => ({ id, label: `选项 ${id}`, ...(rec ? { recommended: true } : {}) }))

function item(over: Partial<ConfirmItem> = {}): ConfirmItem {
  const seq = over.seq ?? 1
  return {
    id: seq,
    workflow_id: 'wf',
    seq,
    ref: `Q${seq}`,
    kind: 'ask',
    title: `问题 ${seq}`,
    options: opts(['a'], ['b', true]),
    multi: false,
    allow_note: false,
    risk: 'L1',
    status: 'pending',
    created_by: 'agent',
    created_at: '2026-10-13T08:00:00Z',
    updated_at: '2026-10-13T08:00:00Z',
    ...over,
  }
}

describe('recommendedIds', () => {
  it('单选只取第一个推荐，多选取全部，没有推荐返回空', () => {
    const o = opts(['a'], ['b', true], ['c', true])
    assert.deepEqual(recommendedIds(o, false), ['b'])
    assert.deepEqual(recommendedIds(o, true), ['b', 'c'])
    assert.deepEqual(recommendedIds(opts(['a'], ['b']), false), [])
  })
})

describe('canAdoptRecommended', () => {
  it('L0 / L1 有推荐才能一键采用；L2、无推荐、非待处理都不行', () => {
    assert.equal(canAdoptRecommended(item({ risk: 'L0' })), true)
    assert.equal(canAdoptRecommended(item({ risk: 'L1' })), true)
    assert.equal(canAdoptRecommended(item({ risk: 'L2' })), false)
    assert.equal(canAdoptRecommended(item({ options: opts(['a'], ['b']) })), false)
    assert.equal(canAdoptRecommended(item({ status: 'answered' })), false)
    assert.equal(canAdoptRecommended(item({ status: 'expired' })), false)
  })
  it('没有 status 字段的选择体（文档决策卡）也能判断', () => {
    assert.equal(canAdoptRecommended({ options: opts(['a', true], ['b']), multi: false, risk: 'L1' }), true)
    assert.equal(canAdoptRecommended({ options: opts(['a', true], ['b']), multi: false, risk: 'L2' }), false)
  })
})

describe('toggleSelection', () => {
  const o = opts(['a'], ['b'], ['c'])
  it('单选是替换，重复点同一个仍是它', () => {
    assert.deepEqual(toggleSelection(o, false, ['a'], 'b'), ['b'])
    assert.deepEqual(toggleSelection(o, false, ['b'], 'b'), ['b'])
  })
  it('多选是切换，并保持选项原顺序', () => {
    assert.deepEqual(toggleSelection(o, true, [], 'c'), ['c'])
    assert.deepEqual(toggleSelection(o, true, ['c'], 'a'), ['a', 'c'])
    assert.deepEqual(toggleSelection(o, true, ['a', 'c'], 'a'), ['c'])
  })
})

describe('checkSelection', () => {
  const single = { options: opts(['a'], ['b']), multi: false, allow_note: false }
  const multi = { options: opts(['a'], ['b'], ['c']), multi: true, allow_note: true }
  it('没选不能提交，单选多选各按各的规则', () => {
    assert.equal(checkSelection(single, [], '').ok, false)
    assert.equal(checkSelection(single, ['a'], '').ok, true)
    assert.equal(checkSelection(single, ['a', 'b'], '').ok, false)
    assert.equal(checkSelection(multi, ['a', 'c'], '').ok, true)
  })
  it('选项已不存在时提示刷新', () => {
    const r = checkSelection(single, ['zzz'], '')
    assert.equal(r.ok, false)
    assert.match(r.reason, /刷新/)
  })
  it('备注只在允许时可用，且有长度上限', () => {
    assert.equal(checkSelection(single, ['a'], '想说两句').ok, false)
    assert.equal(checkSelection(single, ['a'], '   ').ok, true, '纯空白备注当作没写')
    assert.equal(checkSelection(multi, ['a'], '想说两句').ok, true)
    assert.equal(checkSelection(multi, ['a'], '字'.repeat(2001)).ok, false)
    assert.equal(checkSelection(multi, ['a'], '字'.repeat(2000)).ok, true)
  })
})

describe('自定义答复（#183）：预设选项都不合适时的出路', () => {
  const single = { options: opts(['a'], ['b']), multi: false, allow_note: false }
  const multi = { options: opts(['a'], ['b'], ['c']), multi: true, allow_note: true }
  const on = (text: string) => ({ on: true, text })
  it('选中「其他（自定义）」后必须写内容；单选时只写自定义就能提交，不需要备注开关', () => {
    assert.equal(checkSelection(single, [], '', on('先做 C')).ok, true, '单选没选预设选项、写了自定义')
    assert.equal(checkSelection(single, [], '', on('   ')).ok, false, '选中了却全是空白')
    assert.match(checkSelection(single, [], '', on('')).reason, /自定义内容/)
    assert.equal(checkSelection(single, [], '', { on: false, text: '写过但没选中' }).ok, false, '没选中时文本框里残留的字不算')
    assert.match(checkSelection(single, [], '').reason, /其他（自定义）/, '没选任何东西时提示里指出还有自定义这条路')
  })
  it('单选时预设选项与自定义二选一；多选时可以并存', () => {
    assert.equal(checkSelection(single, ['a'], '', on('先做 C')).ok, false)
    assert.match(checkSelection(single, ['a'], '', on('先做 C')).reason, /二选一/)
    assert.equal(checkSelection(multi, ['a', 'c'], '', on('再加一个开关')).ok, true)
    assert.equal(checkSelection(multi, [], '', on('都不要，换个思路')).ok, true, '多选也允许只写自定义')
  })
  it('自定义有长度上限，备注规则不受影响', () => {
    assert.equal(checkSelection(multi, [], '', on('字'.repeat(2000))).ok, true)
    assert.equal(checkSelection(multi, [], '', on('字'.repeat(2001))).ok, false)
    assert.equal(checkSelection(single, [], '想说两句', on('先做 C')).ok, false, '单选项不接受备注，自定义不改变这一点')
    assert.equal(checkSelection(multi, [], '补充一句', on('先做 C')).ok, true, '备注与自定义并存')
  })
  it('点「其他（自定义）」后预设选项怎么变：单选打开时清空，其余保持', () => {
    assert.deepEqual(selectionAfterCustomToggle(false, ['a'], true), [])
    assert.deepEqual(selectionAfterCustomToggle(false, ['a'], false), ['a'], '单选关闭自定义不动预设选项')
    assert.deepEqual(selectionAfterCustomToggle(true, ['a', 'c'], true), ['a', 'c'])
    assert.deepEqual(selectionAfterCustomToggle(true, ['a', 'c'], false), ['a', 'c'])
  })
  it('答复文案：只写自定义时前面没有选项，自定义永远在最后；老答复没有 custom 字段', () => {
    const o = [{ id: 'a', label: '先做 A' }, { id: 'b', label: '先做 B' }]
    assert.equal(answerText(item({ options: o, answer: { options: [], custom: '  先做 C  ', by: 'human' } })), '自定义：先做 C')
    assert.equal(answerText(item({ options: o, answer: { options: ['a'], note: '稳一点', custom: '另外加开关', by: 'human' } })), '先做 A；备注：稳一点；自定义：另外加开关')
    assert.equal(answerText(item({ options: o, answer: { options: ['a'], by: 'human' } })), '先做 A')
    assert.equal(answerText(item({ options: o, answer: { options: [], custom: '   ', by: 'human' } })), '', '全是空白的自定义等于没写')
    assert.equal(customAnswerOf(item({ answer: { options: [], custom: ' x ', by: 'human' } })), 'x')
    assert.equal(customAnswerOf(item()), '')
  })
})

describe('answerText', () => {
  it('用选项文案写答复，带备注', () => {
    const it1 = item({ options: [{ id: 'a', label: '先做 A' }, { id: 'b', label: '先做 B' }], answer: { options: ['b'], by: 'human' } })
    assert.equal(answerText(it1), '先做 B')
    const it2 = item({ options: it1.options, answer: { options: ['a', 'b'], note: '都要', by: 'human' } })
    assert.equal(answerText(it2), '先做 A、先做 B；备注：都要')
  })
  it('选项已被改掉时退回 id，没有答复时为空', () => {
    assert.equal(answerText(item({ answer: { options: ['gone'], by: 'human' } })), 'gone')
    assert.equal(answerText(item()), '')
  })
})

describe('groupByRisk', () => {
  it('高风险在前，组内旧的在前，空组不出现', () => {
    const groups = groupByRisk([item({ seq: 3, risk: 'L0' }), item({ seq: 2, risk: 'L2' }), item({ seq: 1, risk: 'L1' }), item({ seq: 4, risk: 'L2' })])
    assert.deepEqual(groups.map(g => g.risk), ['L2', 'L1', 'L0'])
    assert.deepEqual(groups[0].items.map(i => i.seq), [2, 4])
    assert.deepEqual(groupByRisk([item({ risk: 'L0' })]).map(g => g.risk), ['L0'])
    assert.deepEqual(groupByRisk([]), [])
  })
  it('缺 risk 的按 L1 处理', () => {
    const noRisk = item({ seq: 1 }) as ConfirmItem
    ;(noRisk as { risk?: string }).risk = undefined
    assert.deepEqual(groupByRisk([noRisk]).map(g => g.risk), ['L1'])
  })
})

describe('adoptableItems / adoptPreview', () => {
  const list = [
    item({ seq: 3 }),
    item({ seq: 1, risk: 'L2' }),
    item({ seq: 2, options: opts(['a'], ['b']) }),
    item({ seq: 4, multi: true, options: opts(['a', true], ['b', true], ['c']) }),
    item({ seq: 5, status: 'answered' }),
  ]
  it('只含待处理、非 L2、有推荐的，按序号升序', () => {
    assert.deepEqual(adoptableItems(list).map(i => i.seq), [3, 4])
  })
  it('预览写明每条将提交什么（多选列出全部推荐）', () => {
    const rows = adoptPreview(list)
    assert.deepEqual(rows.map(r => r.ref), ['Q3', 'Q4'])
    assert.equal(rows[0].choice, '选项 b')
    assert.equal(rows[1].choice, '选项 a、选项 b')
  })
})

describe('待答复徽标 / riskTone', () => {
  it('没有待答复的提问时没有文案，否则写出条数', () => {
    assert.equal(pendingChipText(0), '')
    assert.equal(pendingChipText(3), '待答复 3')
    assert.equal(pendingChipTitle(0), '')
    assert.equal(pendingChipTitle(2), '2 项提问等你答复；点开可定位到对话里的卡片')
  })
  it('风险色调', () => {
    assert.equal(riskTone('L2'), 'danger')
    assert.equal(riskTone('L1'), 'warn')
    assert.equal(riskTone('L0'), 'muted')
    assert.equal(riskTone(undefined), 'muted')
  })
  it('风险标签的悬停说明：L2 写明批准前不会执行（#217），L0 / L1 照旧只写等级', () => {
    assert.match(riskHint('L2'), /^L2 · 不可逆 \/ 高风险：/)
    assert.match(riskHint('L2'), /在您明确批准前不会执行这一步/)
    assert.equal(riskHint('L1'), 'L1 · 可回滚的改动')
    assert.equal(riskHint('L0'), 'L0 · 只读 / 随时可回滚')
  })
})

function decision(over: Partial<DocDecisionView> = {}, risk = 'L1'): DocDecisionView {
  return {
    card: { id: 'db', title: '用哪个数据库', options: opts(['sqlite', true], ['pg']), risk },
    status: 'pending',
    confirm_ref: 'Q1',
    ...over,
  }
}

describe('文档决策卡', () => {
  it('只有围栏合法、已有确认项且待处理的才能作答；历史修订一律不能', () => {
    assert.equal(decisionAnswerable(decision()), true)
    assert.equal(decisionAnswerable(decision(), true), false)
    assert.equal(decisionAnswerable(decision({ confirm_ref: '' })), false, '确认项尚未生成')
    assert.equal(decisionAnswerable(decision({ errors: ['id 不能为空'], status: 'invalid' })), false)
    assert.equal(decisionAnswerable(decision({ status: 'answered' })), false)
    assert.equal(decisionAnswerable(decision({ status: 'expired' })), false)
    assert.equal(decisionAnswerable(undefined), false)
  })
  it('汇总：待决策数、已答复数、可一键采用的（排除 L2 与无推荐）', () => {
    const views = [
      decision({ confirm_ref: 'Q1' }),
      decision({ confirm_ref: 'Q2' }, 'L2'),
      decision({ confirm_ref: 'Q3', card: { id: 'x', title: '无推荐', options: opts(['a'], ['b']) } }),
      decision({ confirm_ref: 'Q4', status: 'answered' }),
      decision({ confirm_ref: '', status: 'pending' }),
    ]
    const s = summarizeDecisions(views)
    assert.equal(s.total, 5)
    assert.equal(s.pending, 3)
    assert.equal(s.answered, 1)
    assert.deepEqual(s.adoptable.map(v => v.confirm_ref), ['Q1'])
    assert.deepEqual(summarizeDecisions(views, true), { total: 5, pending: 0, answered: 1, adoptable: [] }, '历史修订里没有可作答的')
    assert.deepEqual(summarizeDecisions(undefined), { total: 0, pending: 0, answered: 0, adoptable: [] })
  })
  it('采用预览与已答复文案', () => {
    const rows = decisionAdoptPreview([decision({ confirm_ref: 'Q1' })])
    assert.deepEqual(rows, [{ ref: 'Q1', title: '用哪个数据库', kind: 'decision', choice: '选项 sqlite' }])
    const answered = decision({ status: 'answered', answer: { options: ['pg'], note: '团队熟悉', by: 'human' } })
    assert.equal(decisionAnswerText(answered), '选项 pg；备注：团队熟悉')
    assert.equal(decisionAnswerText(decision()), '')
  })
})

describe('ageText', () => {
  const now = Date.parse('2026-10-13T12:00:00Z')
  it('按量级写人话', () => {
    assert.equal(ageText('2026-10-13T11:59:30Z', now), '刚刚')
    assert.equal(ageText('2026-10-13T11:55:00Z', now), '5 分钟前')
    assert.equal(ageText('2026-10-13T09:00:00Z', now), '3 小时前')
    assert.equal(ageText('2026-10-10T12:00:00Z', now), '3 天前')
  })
  it('未来时间当作刚刚，无法解析为空', () => {
    assert.equal(ageText('2026-10-13T12:05:00Z', now), '刚刚')
    assert.equal(ageText('', now), '')
    assert.equal(ageText(undefined, now), '')
    assert.equal(ageText('不是时间', now), '')
  })
})

describe('matchDecisionViews', () => {
  const seg = (id: string) => ({ kind: 'decision', card: { id } })
  const md = { kind: 'markdown' }
  it('按 id 配，非决策段为 undefined', () => {
    const a = decision({ confirm_ref: 'Q1' })
    const b = decision({ confirm_ref: 'Q2', card: { id: 'cache', title: '缓存', options: opts(['x'], ['y']) } })
    const out = matchDecisionViews([md, seg('db'), md, seg('cache')], [a, b])
    assert.deepEqual(out, [undefined, a, undefined, b])
  })
  it('同一个 id 出现多次时按出现顺序一一对应，多出来的配不上', () => {
    const first = decision({ confirm_ref: 'Q1' })
    const dup = decision({ status: 'invalid', errors: ['id 重复'], confirm_ref: '' })
    const out = matchDecisionViews([seg('db'), seg('db'), seg('db')], [first, dup])
    assert.deepEqual(out, [first, dup, undefined])
  })
  it('没有视图时全部 undefined', () => {
    assert.deepEqual(matchDecisionViews([seg('db')], undefined), [undefined])
    assert.deepEqual(matchDecisionViews([], [decision()]), [])
  })
})

describe('recallOptions（#132 回看已处理的卡）', () => {
  const base = { options: opts(['a', true], ['b'], ['c']) }
  it('已答复：全部选项都列出来，选中的标出来，推荐位置不变', () => {
    const out = recallOptions({ ...base, status: 'answered', answer: { options: ['b'], note: '等周五', by: 'user' } })
    assert.deepEqual(out.map(o => [o.id, o.chosen, o.recommended, o.missing]), [
      ['a', false, true, false],
      ['b', true, false, false],
      ['c', false, false, false],
    ])
  })
  it('多选：所有选中的都标出来', () => {
    const out = recallOptions({ ...base, status: 'answered', answer: { options: ['a', 'c'], by: 'user' } })
    assert.deepEqual(out.filter(o => o.chosen).map(o => o.id), ['a', 'c'])
  })
  it('忽略 / 撤回 / 过期 / 待处理：没有选中项（即使带着旧答复）', () => {
    for (const status of ['dismissed', 'withdrawn', 'expired', 'pending'] as const) {
      const out = recallOptions({ ...base, status, answer: { options: ['b'], by: 'user' } })
      assert.equal(out.some(o => o.chosen), false, status)
      assert.equal(out.length, 3)
    }
  })
  it('答复里的 id 在选项里找不到：追加在最后并标成 missing，不假装没发生过', () => {
    const out = recallOptions({ ...base, status: 'answered', answer: { options: ['b', 'zz'], by: 'user' } })
    assert.deepEqual(out.map(o => o.id), ['a', 'b', 'c', 'zz'])
    const gone = out[3]
    assert.equal(gone.chosen, true)
    assert.equal(gone.missing, true)
    assert.equal(gone.label, 'zz')
  })
  it('选项没有标签时用 id 兜底，没有说明时不带 detail', () => {
    const out = recallOptions({
      options: [{ id: 'x', label: '' }, { id: 'y', label: '有说明', detail: '多写一句' }],
      status: 'answered',
      answer: { options: ['x'], by: 'user' },
    })
    assert.equal(out[0].label, 'x')
    assert.equal(out[0].detail, undefined)
    assert.equal(out[1].detail, '多写一句')
  })
})

describe('clockText / recallTimes', () => {
  // 用本地时间构造，断言在任何时区都成立
  const at = (h: number, m: number) => new Date(2026, 9, 15, h, m).toISOString()
  it('clockText：月-日 时:分（本地时间，补零）', () => {
    assert.equal(clockText(at(7, 5)), '10-15 07:05')
    assert.equal(clockText(new Date(2026, 0, 2, 23, 59).toISOString()), '01-02 23:59')
  })
  it('clockText：空或无法解析为空串', () => {
    assert.equal(clockText(''), '')
    assert.equal(clockText(undefined), '')
    assert.equal(clockText('不是时间'), '')
  })
  it('recallTimes：提出于 · 答复于（已答复用 answered_at）', () => {
    assert.equal(
      recallTimes({ status: 'answered', created_at: at(17, 20), answered_at: at(17, 32), updated_at: at(17, 33) }),
      '提出于 10-15 17:20 · 答复于 10-15 17:32',
    )
  })
  it('recallTimes：忽略 / 撤回 / 过期用 updated_at，动词各不相同', () => {
    const times = { created_at: at(9, 0), updated_at: at(9, 30) }
    assert.equal(recallTimes({ status: 'dismissed', ...times }), '提出于 10-15 09:00 · 忽略于 10-15 09:30')
    assert.equal(recallTimes({ status: 'withdrawn', ...times }), '提出于 10-15 09:00 · 撤回于 10-15 09:30')
    assert.equal(recallTimes({ status: 'expired', ...times }), '提出于 10-15 09:00 · 过期于 10-15 09:30')
  })
  it('recallTimes：待处理只有前半句；答复时间缺失时退回 updated_at', () => {
    assert.equal(recallTimes({ status: 'pending', created_at: at(9, 0), updated_at: at(9, 0) }), '提出于 10-15 09:00')
    assert.equal(
      recallTimes({ status: 'answered', created_at: at(9, 0), updated_at: at(9, 45) }),
      '提出于 10-15 09:00 · 答复于 10-15 09:45',
    )
  })
})

describe('决策路由（#236 P2）：请示先送主会话时的判断与文案', () => {
  it('routedToMain：只有「待处理 + addressee 是 wf:<主会话>」才算在等主会话；转交后 addressee 清空、作答后保留都不算', () => {
    assert.equal(routedToMain(item({ addressee: 'wf:wf-main' })), true)
    assert.equal(routedToMain(item({})), false, '普通请示')
    assert.equal(routedToMain(item({ addressee: '' })), false)
    assert.equal(routedToMain(item({ addressee: 'wf:' })), false, '没有主会话编号')
    assert.equal(routedToMain(item({ addressee: 'user:x' })), false, '不是路由给主会话的')
    assert.equal(routedToMain(item({ addressee: 'wf:wf-main', status: 'answered' })), false, '答复后保留 addressee，但不再「在等」')
    assert.equal(routedToMain(item({ addressee: 'wf:wf-main', status: 'dismissed' })), false)
  })

  it('answeredByMain / answerLead：答复者是 agent:<主会话> 才写「主会话代答」；用户自己答 / 老答复没有 by 都是「已答复」', () => {
    const answered = (by?: string) => item({ status: 'answered', answer: { options: ['a'], by: by as string } })
    assert.equal(answeredByMain(answered('agent:wf-main')), true)
    assert.equal(answerLead(answered('agent:wf-main')), '主会话代答')
    for (const by of ['human', 'doc', '', undefined]) {
      assert.equal(answeredByMain(answered(by)), false, String(by))
      assert.equal(answerLead(answered(by)), '已答复', String(by))
    }
    assert.equal(answeredByMain(item({ status: 'pending', answer: { options: [], by: 'agent:wf-main' } })), false, '没答复完不算')
    assert.equal(answerLead(item({ status: 'answered' })), '已答复', '没有 answer 字段也不报错')
  })

  it('escalationText：主会话转的带它写的原因；超时 / 系统的说明本身是整句；缺说明时给兜底句；没转交过为空', () => {
    assert.equal(escalationText(item({})), '')
    assert.equal(escalationText(item({ escalation: { by: 'agent:wf-main', note: ' 涉及预算 ' } })), '主会话转交给您：涉及预算')
    assert.equal(escalationText(item({ escalation: { by: 'agent:wf-main' } })), '主会话转交给您定夺')
    assert.equal(escalationText(item({ escalation: { by: 'timeout', note: '主会话 30 分钟没有答复，自动转交给你' } })), '主会话 30 分钟没有答复，自动转交给你')
    assert.equal(escalationText(item({ escalation: { by: 'system', note: '分支已结束，请示改由你定夺' } })), '分支已结束，请示改由你定夺')
    assert.equal(escalationText(item({ escalation: { by: 'timeout' } })), '主会话一直没有答复，已转交给您')
    assert.equal(escalationText(item({ escalation: { by: 'system' } })), '已转交给您定夺')
  })
})
