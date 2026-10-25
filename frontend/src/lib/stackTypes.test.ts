// 运行：pnpm --dir frontend run test:unit
// 卡片摞里有哪几种类型（#160，追加 #248 / Q24 选 a）：类型怎么数、摞头的类型标签、右侧轨摞刻度的分段、属性往返，
// 以及源码级防线（折叠的摞头是事件行、在里面画标签，展开后的头部不画）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { ConfirmItem } from '../api/confirms'
import { CARD_TONE_CLASS, CARD_TYPES } from './messageCard'
import type { TimelineEntry } from './timeline'
import {
  STACK_MAX_CHIPS,
  STACK_TYPE_ORDER,
  formatStackParts,
  isStackCardType,
  parseStackParts,
  stackChipModel,
  stackRailSegments,
  stackTypeCounts,
  stackTypeOf,
  stackTypesText,
} from './stackTypes'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'stackTypes.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const AT = '2026-10-17T05:00:00Z'
const confirm = (over: Partial<ConfirmItem>): ConfirmItem => ({ id: 1, workflow_id: 'w', seq: 7, kind: 'ask', title: '要不要采用？', options: [], status: 'answered', ...over }) as unknown as ConfirmItem
const task = (seq: number): TimelineEntry => ({ kind: 'task', key: `task-${seq}`, at: AT, card: { key: `task-${seq}`, seq, title: `任务 ${seq}`, state: 'doing', verb: '进行中', text: '', at: AT } })
const ask = (seq: number): TimelineEntry => ({ kind: 'confirm', key: `confirm-${seq}`, at: AT, item: confirm({ seq }) })
const plan = (seq: number): TimelineEntry => ({ kind: 'confirm', key: `confirm-${seq}`, at: AT, item: confirm({ seq, kind: 'plan' }) })
const decisions = (n: number): TimelineEntry => ({ kind: 'decisions', key: `decisions-doc-${n}`, at: AT, group: { key: `doc-${n}`, docPath: 'a.md', docTitle: '设计稿', pending: 0, answered: 2, total: 2, at: AT } })
const notice: TimelineEntry = { kind: 'notice', key: 'notice-s1', at: AT, session: { session_id: 's1', title: '进度' } as never }

describe('stackTypeOf / stackTypeCounts：摞里每种类型各几张', () => {
  it('任务 → task；确认项按 kind 分 ask / plan；文档决策汇总 → decision；不会进摞的种类是 null', () => {
    assert.equal(stackTypeOf(task(1)), 'task')
    assert.equal(stackTypeOf(ask(2)), 'ask')
    assert.equal(stackTypeOf(plan(3)), 'plan')
    assert.equal(stackTypeOf(decisions(1)), 'decision')
    assert.equal(stackTypeOf(notice), null)
  })

  it('按固定次序（任务 → 提问 → 计划 → 决策）排，与条目顺序无关；没有的类型不出现', () => {
    const counts = stackTypeCounts([decisions(1), plan(3), task(1), ask(2), task(4), ask(5), task(6)])
    assert.deepEqual(counts, [{ type: 'task', count: 3 }, { type: 'ask', count: 2 }, { type: 'plan', count: 1 }, { type: 'decision', count: 1 }])
    assert.deepEqual(stackTypeCounts([task(1), task(2)]), [{ type: 'task', count: 2 }])
    assert.deepEqual(stackTypeCounts([]), [])
  })

  it('不属于摞的条目（进度通知）不计数', () => {
    assert.deepEqual(stackTypeCounts([notice, task(1)]), [{ type: 'task', count: 1 }])
    assert.deepEqual(stackTypeCounts([notice]), [])
  })

  it('次序表与类型守卫：四种，且都是卡片词汇表里的类型', () => {
    assert.deepEqual([...STACK_TYPE_ORDER], ['task', 'ask', 'plan', 'decision'])
    for (const t of STACK_TYPE_ORDER) {
      assert.ok(isStackCardType(t))
      assert.ok(CARD_TYPES[t], `${t} 在卡片词汇表里`)
    }
    for (const bad of ['notice', 'report', '', undefined, 3]) assert.ok(!isStackCardType(bad), String(bad))
  })
})

describe('stackTypesText / stackChipModel：摞头的类型标签', () => {
  it('悬停文字：「任务 3 · 提问 1」，字取卡片头部的类型标签', () => {
    assert.equal(stackTypesText([{ type: 'task', count: 3 }, { type: 'ask', count: 1 }]), '任务 3 · 提问 1')
    assert.equal(stackTypesText([{ type: 'plan', count: 2 }, { type: 'decision', count: 1 }]), '计划 2 · 决策 1')
    assert.equal(stackTypesText([]), '')
  })

  it('一种类型一个标签：字与色调都取自卡片词汇表（任务 石板 / 提问 琥珀 / 计划 红 / 决策 紫）', () => {
    const m = stackChipModel(stackTypeCounts([task(1), ask(2)]))
    assert.deepEqual(m.chips.map(c => [c.type, c.label, c.tone, c.count]), [['task', '任务', 'slate', 1], ['ask', '提问', 'amber', 1]])
    assert.equal(m.more, 0)
    assert.equal(m.text, '任务 1 · 提问 1')
    for (const c of m.chips) assert.equal(c.toneClass, CARD_TONE_CLASS[CARD_TYPES[c.type].tone])
    assert.deepEqual(stackChipModel(stackTypeCounts([plan(1)])).chips.map(c => [c.label, c.tone]), [['计划', 'red']])
    assert.deepEqual(stackChipModel(stackTypeCounts([decisions(1)])).chips.map(c => [c.label, c.tone]), [['决策', 'violet']])
  })

  it('最多画 3 个，多出的并成「+N」；悬停文字仍写全', () => {
    assert.equal(STACK_MAX_CHIPS, 3)
    const all = stackTypeCounts([task(1), ask(2), plan(3), decisions(1)])
    const m = stackChipModel(all)
    assert.deepEqual(m.chips.map(c => c.type), ['task', 'ask', 'plan'])
    assert.equal(m.more, 1)
    assert.equal(m.text, '任务 1 · 提问 1 · 计划 1 · 决策 1')
    const wide = stackChipModel(all, 4)
    assert.equal(wide.chips.length, 4)
    assert.equal(wide.more, 0)
    assert.equal(stackChipModel(all, 0).more, 4)
  })

  it('没有任何类型（空摞）：没有标签、没有「+N」', () => {
    const m = stackChipModel([])
    assert.deepEqual([m.chips.length, m.more, m.text], [0, 0, ''])
  })
})

describe('右侧轨摞刻度的分段：stackRailSegments / formatStackParts / parseStackParts', () => {
  it('一种类型一段，颜色取该类型的卡片色调', () => {
    const segs = stackRailSegments([{ type: 'task', count: 2 }, { type: 'ask', count: 1 }, { type: 'plan', count: 1 }])
    assert.deepEqual(segs.map(s => s.type), ['task', 'ask', 'plan'])
    assert.deepEqual(segs.map(s => s.toneClass), [CARD_TONE_CLASS.slate, CARD_TONE_CLASS.amber, CARD_TONE_CLASS.red])
    assert.deepEqual(stackRailSegments(undefined), [])
    assert.deepEqual(stackRailSegments([]), [])
  })

  it('属性往返：format → parse 还原；空与 undefined 都得到 undefined / 空串', () => {
    const parts = [{ type: 'task', count: 12 }, { type: 'decision', count: 1 }] as const
    assert.equal(formatStackParts(parts), 'task:12,decision:1')
    assert.deepEqual(parseStackParts(formatStackParts(parts)), [...parts])
    assert.equal(formatStackParts(undefined), '')
    assert.equal(formatStackParts([]), '')
    assert.equal(parseStackParts(''), undefined)
    assert.equal(parseStackParts(null), undefined)
    assert.equal(parseStackParts(undefined), undefined)
  })

  it('读回时：坏值丢弃（不认识的类型、非正整数、小数、重复），剩下的按固定次序重排；一段都不剩得到 undefined', () => {
    assert.deepEqual(parseStackParts('decision:1,task:2'), [{ type: 'task', count: 2 }, { type: 'decision', count: 1 }])
    assert.deepEqual(parseStackParts('task:2.5,ask:-1,plan:0,notice:3,task:3'), [{ type: 'task', count: 3 }])
    assert.deepEqual(parseStackParts('task:1,task:9'), [{ type: 'task', count: 1 }])
    assert.equal(parseStackParts('x:1,,:,task'), undefined)
  })
})

describe('源码防线：折叠的摞头（事件行）画类型标签，展开后的头部不画', () => {
  const stackSrc = read('components/feedback/CardStack.vue')
  const railSrc = read('components/feedback/preview-view/PreviewTimelineRail.vue')
  // 刻度列表从 #158 起拆到 PreviewRailTicks：刻度的分段与无障碍标签在它里面，悬停提示与排布指纹仍在轨本体里
  const ticksSrc = read('components/feedback/preview-view/PreviewRailTicks.vue')
  const lib = read('lib/timelineRail.ts')

  it('折叠的事件行：lead 槽里画标签（带 data-type 与 testid），用 stackChipModel 取数', () => {
    assert.match(stackSrc, /stackChipModel\(stackTypeCounts\(props\.entries\)\)/)
    assert.match(stackSrc, /data-testid="card-stack-types"/)
    assert.match(stackSrc, /data-testid="card-stack-type"/)
    assert.match(stackSrc, /data-testid="card-stack-type-more"/)
    assert.match(stackSrc, /CARD_CHIP_CLASS, c\.toneClass/)
  })

  it('折叠态只有事件行一支（v-else 的整行按钮在 #247 删掉了）；展开后的头部没有类型标签', () => {
    assert.doesNotMatch(stackSrc, /<button\n\s+v-else/, '折叠态不该再有 v-else 的整行按钮')
    const expanded = stackSrc.slice(stackSrc.indexOf('<template v-else>'))
    assert.ok(expanded.length > 100, '找到了展开态')
    assert.doesNotMatch(expanded, /card-stack-type/)
    assert.doesNotMatch(expanded, /chipModel/)
  })

  it('右侧轨：摞的刻度按 parts 分段画（每段一个类型色调），悬停提示 / 无障碍标签带上张数，排布指纹含 parts', () => {
    assert.match(ticksSrc, /stackRailSegments\(t\.item\.parts\)/)
    assert.match(ticksSrc, /data-testid="pv-rail-tick-seg"/)
    assert.match(railSrc, /railLabel\(hoverTick\.item\.kind, hoverTick\.item\.state, hoverTick\.item\.parts\)/)
    assert.match(ticksSrc, /railLabel\(item\.kind, item\.state, item\.parts\)/)
    assert.match(railSrc, /formatStackParts\(t\.item\.parts\)\}\|\$\{t\.y\}/)
  })

  it('轨条目：摞带 parts、DOM 属性可往返、轨监听该属性', () => {
    assert.match(lib, /stackTypeCounts\(b\.entries\)/)
    assert.match(lib, /parts: 'data-rail-parts'/)
    assert.match(lib, /RAIL_WATCH_ATTRS: string\[\] = \[[^\]]*RAIL_ATTR\.parts\]/)
  })
})
