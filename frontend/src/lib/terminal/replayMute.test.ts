// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { ReplayMute } from './replayMute'

function clock(start = 1000) {
  const c = { t: start, now: () => c.t }
  return c
}

describe('ReplayMute', () => {
  it('初始不压制', () => {
    assert.equal(new ReplayMute().active, false)
  })

  it('非回放写入原样返回回调，不影响压制状态', () => {
    const m = new ReplayMute()
    const done = () => {}
    assert.equal(m.wrap(false, done), done)
    assert.equal(m.active, false)
  })

  it('回放数据写入后立即压制，解析完成（回调触发）后解除，并透传完成回调', () => {
    const c = clock()
    const m = new ReplayMute(c.now)
    let called = 0
    const cb = m.wrap(true, () => called++)
    assert.equal(m.active, true)
    cb()
    assert.equal(called, 1)
    assert.equal(m.active, false)
  })

  it('多段回放：全部解析完才解除', () => {
    const c = clock()
    const m = new ReplayMute(c.now)
    const a = m.wrap(true, () => {})
    const b = m.wrap(true, () => {})
    a()
    assert.equal(m.active, true, '还有一段在排队')
    b()
    assert.equal(m.active, false)
  })

  it('实时输出夹在回放之后不会被压制（队列是先进先出，回放的回调先于实时数据的解析）', () => {
    const c = clock()
    const m = new ReplayMute(c.now)
    const replayDone = m.wrap(true, () => {})
    m.wrap(false, () => {}) // 实时数据：不登记
    replayDone() // xterm.js 先解析完回放，回调触发
    assert.equal(m.active, false, '轮到解析实时数据时已经解除，实时查询会被应答')
  })

  it('回调重复触发只计一次', () => {
    const c = clock()
    const m = new ReplayMute(c.now)
    const a = m.wrap(true, () => {})
    const b = m.wrap(true, () => {})
    a()
    a()
    assert.equal(m.active, true, '重复调用 a 不应把 b 的那份计数也减掉')
    b()
    assert.equal(m.active, false)
  })

  it('无进展超时：回调迟迟不来时自动解除，避免一直吞掉用户输入', () => {
    const c = clock()
    const m = new ReplayMute(c.now, 3000)
    m.wrap(true, () => {})
    c.t += 2999
    assert.equal(m.active, true)
    c.t += 2
    assert.equal(m.active, false)
  })

  it('有进展就续期：大回放解析慢但一直在推进，不会中途解除', () => {
    const c = clock()
    const m = new ReplayMute(c.now, 3000)
    const first = m.wrap(true, () => {})
    const rest = m.wrap(true, () => {})
    c.t += 2500
    first() // 第一段完成 = 进展
    c.t += 2500 // 距离开始已 5 秒，但距离上次进展只有 2.5 秒
    assert.equal(m.active, true)
    rest()
    assert.equal(m.active, false)
  })

  it('新的回放数据写入也会刷新超时', () => {
    const c = clock()
    const m = new ReplayMute(c.now, 3000)
    m.wrap(true, () => {})
    c.t += 2500
    m.wrap(true, () => {})
    c.t += 2500
    assert.equal(m.active, true)
  })
})
