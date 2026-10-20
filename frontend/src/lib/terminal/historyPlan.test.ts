import test from 'node:test'
import assert from 'node:assert/strict'
import { MIN_SAMPLE, concatOutput, cutItems, locateSample, outputLength, planExtend } from './historyPlan'
import type { Item } from './record'

const enc = new TextEncoder()
const dec = new TextDecoder()
const b = (s: string) => enc.encode(s)
const out = (s: string, at = 0): Item => ({ type: 'output', at, data: b(s) })
const joined = (items: Item[]) => dec.decode(concatOutput(items))

/** 生成确定性的「像日志的」文本，每行互不相同，样本不会巧合重复。 */
function lines(from: number, to: number): string {
  let s = ''
  for (let i = from; i < to; i++) s += `2026-10-03T14:${String(i % 60).padStart(2, '0')} INFO request id=${i * 7919} path=/api/v1/item/${i} ok\r\n`
  return s
}

test('定位：找到样本的位置；多处出现时取离期望最近的', () => {
  const hay = b('xxABCxxABCxxABCxx')
  assert.equal(locateSample(hay, b('ABC'), 0), 2)
  assert.equal(locateSample(hay, b('ABC'), 100), 12)
  assert.equal(locateSample(hay, b('ABC'), 8), 7)
  assert.equal(locateSample(hay, b('nope'), 0), -1)
  assert.equal(locateSample(hay, b(''), 0), -1)
  assert.equal(locateSample(b('ab'), b('abc'), 0), -1)
})

test('切分条目：横跨的输出块被截断，之后的条目丢弃，之前的标记保留', () => {
  const items: Item[] = [
    { type: 'seg', at: 0, seq: 1, rows: 24, cols: 80 },
    out('0123'),
    { type: 'resize', at: 1, rows: 30, cols: 100 },
    out('456789'),
    { type: 'gap', at: 2, reason: 'x', bytes: 5 },
    out('abc'),
    { type: 'end', at: 3, reason: 'exit', code: 0, signal: '' },
  ]
  const cut = cutItems(items, 7)
  assert.equal(joined(cut), '0123456')
  assert.deepEqual(cut.map(i => i.type), ['seg', 'output', 'resize', 'output'])
  // 恰好落在块边界：该块完整保留，之后的标记不进来
  const edge = cutItems(items, 4)
  assert.deepEqual(edge.map(i => i.type), ['seg', 'output'])
  assert.equal(outputLength(edge), 4)
  // 超过总量：全部保留
  assert.equal(cutItems(items, 1000).length, items.length)
})

test('规划：实时内容开头在记录中间 → 记录里它之前的部分就是历史', () => {
  const history = lines(0, 40)
  const live = lines(40, 60)
  const items = [out(history.slice(0, 1000)), out(history.slice(1000) + live)]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  assert.equal(joined(plan.history), history)
  assert.equal(plan.tapeHead, 0)
  assert.equal(plan.tapeCut, 0)
})

test('规划：记录比实时内容略晚或略早结束都能定位（实时内容里已有新输出）', () => {
  const history = lines(0, 30)
  const live = lines(30, 50)
  // 记录滞后：只有实时内容的前半
  const items = [out(history + live.slice(0, 800))]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  assert.equal(joined(plan.history), history)
})

test('规划：实时内容里有清屏 → 分割线放在最后一次清屏处，清屏前记录也有的部分一并归入历史', () => {
  const history = lines(0, 20)
  const live = lines(20, 30) + '\x1b[H\x1b[2J\x1b[3J' + lines(30, 33)
  const items = [out(history + live)]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  const cut = live.indexOf(lines(30, 31))
  assert.equal(plan.tapeCut, cut)
  assert.equal(plan.tapeHead, cut, '清屏前的部分记录里都有，不需要从输出带里补')
  assert.equal(joined(plan.history), history + live.slice(0, cut))
})

test('规划：记录比实时内容落后、没有覆盖到清屏位置 → 记录里有的取记录，其余由输出带补上', () => {
  const history = lines(0, 20)
  const live = lines(20, 40) + '\x1b[2J' + lines(40, 43)
  const covered = 900 // 记录只有实时内容的前 900 字节
  const items = [out(history + live.slice(0, covered))]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  assert.equal(plan.tapeHead, covered)
  assert.equal(plan.tapeCut, live.indexOf(lines(40, 41)))
  assert.ok(plan.tapeHead < plan.tapeCut)
  assert.equal(joined(plan.history), history + live.slice(0, covered))
})

test('规划：记录在清屏前的区间里有缺口（衔接处对不上）→ 不信任记录的这一段，改由输出带提供', () => {
  const history = lines(0, 20)
  const live = lines(20, 45) + '\x1b[2J' + lines(45, 47)
  // 记录在实时内容的第 700..800 字节处少了一段（录制端丢了一块）
  const items = [out(history + live.slice(0, 700) + live.slice(800))]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  assert.equal(plan.tapeHead, 0)
  assert.equal(plan.tapeCut, live.indexOf(lines(45, 46)))
  assert.equal(joined(plan.history), history)
})

test('规划：样本在窗口最开头，窗口前面还有记录 → 需要往前读', () => {
  const live = lines(0, 20)
  const items = [out(live)]
  assert.deepEqual(planExtend(items, b(live), { atBeginning: false }), { kind: 'need_earlier' })
})

test('规划：窗口就是记录的开头，实时内容从会话开头起 → 没有更早的历史；有清屏则把清屏前的内容当历史', () => {
  const live = lines(0, 20)
  assert.deepEqual(planExtend([out(live)], b(live), { atBeginning: true }), { kind: 'nothing' })
  const withClear = lines(0, 10) + '\x1b[2J' + lines(10, 15)
  const plan = planExtend([out(withClear)], b(withClear), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  const cut = withClear.indexOf(lines(10, 11))
  assert.equal(plan.tapeCut, cut)
  assert.equal(joined(plan.history), withClear.slice(0, cut), '清屏前的内容在记录里有，直接取记录')
})

test('规划：记录里没有实时内容的开头 / 实时内容太短 → 无法接续', () => {
  const items = [out(lines(0, 30))]
  assert.deepEqual(planExtend(items, b(lines(100, 110)), { atBeginning: true }), { kind: 'not_found' })
  assert.deepEqual(planExtend(items, b('x'.repeat(MIN_SAMPLE - 1)), { atBeginning: true }), { kind: 'no_sample' })
})

test('规划：样本重复出现（循环刷新的画面）时取离末尾期望位置最近的一个', () => {
  const frame = 'spinner frame ' + 'x'.repeat(120) + '\r\n'
  const history = lines(0, 10) + frame.repeat(5)
  const live = frame.repeat(3)
  const items = [out(history + live)]
  const plan = planExtend(items, b(live), { atBeginning: true })
  assert.equal(plan.kind, 'ok')
  if (plan.kind !== 'ok') return
  // 期望位置 = 总长 - 输出带长度，正好是 history 的结尾
  assert.equal(joined(plan.history), history)
})
