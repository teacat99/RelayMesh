import test from 'node:test'
import assert from 'node:assert/strict'
import { DEFAULT_GESTURE_GAP_MS, ScrollGate } from './scrollGate'

test('到顶后的第一次向上滚就加载（不再需要二次确认）', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(1000), 'fire')
})

test('同一次手势的连续事件（含惯性尾巴）只触发一次', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(1000), 'fire')
  let last = 1000
  for (let t = 1016; t < 1400; t += 16) {
    assert.equal(g.upAtTop(t), 'ignore', `t=${t}`)
    last = t
  }
  // 还没停够一个间隔的不算新手势（被忽略的输入同样把手势延长到它的时刻），停够之后的新滚动才触发
  last += DEFAULT_GESTURE_GAP_MS - 50
  assert.equal(g.upAtTop(last), 'ignore')
  assert.equal(g.upAtTop(last + DEFAULT_GESTURE_GAP_MS + 1), 'fire')
})

test('停一下再来一次新的滚动，再触发一次（例如接上一批历史之后还有更早的）', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(0), 'fire')
  assert.equal(g.upAtTop(DEFAULT_GESTURE_GAP_MS + 1), 'fire')
})

test('间隔可调', () => {
  const g = new ScrollGate({ gestureGapMs: 100 })
  assert.equal(g.upAtTop(0), 'fire')
  assert.equal(g.upAtTop(99), 'ignore')
  assert.equal(g.upAtTop(99 + 100), 'fire')
})

test('reset 之后的第一次输入就是新手势，即使间隔很短', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(1000), 'fire')
  g.reset()
  assert.equal(g.upAtTop(1100), 'fire')
})

test('newGesture 让紧接着的输入不被当作上一手势的尾巴（触屏重新按下）', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(1000), 'fire')
  g.newGesture()
  assert.equal(g.upAtTop(1050), 'fire')
})

test('hold：手势进行中遇到「不该触发」的输入（正在加载），这次手势的尾巴不会在加载结束后再触发', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(0), 'fire') // 开始加载
  // 加载期间手势仍在继续，调用方用 hold 记下这些输入
  for (let t = 100; t <= 1500; t += 100) g.hold(t)
  // 加载在 1500 之后结束，手势还有尾巴：不触发
  assert.equal(g.upAtTop(1600), 'ignore')
  assert.equal(g.upAtTop(1700), 'ignore')
  // 真正停下来之后的新滚动才触发
  assert.equal(g.upAtTop(1700 + DEFAULT_GESTURE_GAP_MS + 1), 'fire')
})

test('不 hold 的话，加载期间被忽略的输入不会延长手势（对照）', () => {
  const g = new ScrollGate()
  assert.equal(g.upAtTop(0), 'fire')
  assert.equal(g.upAtTop(1600), 'fire', '距上一次触发已超过间隔，算新手势')
})
