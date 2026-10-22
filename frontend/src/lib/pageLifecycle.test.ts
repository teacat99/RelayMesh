// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { createPageLifecycleGate, watchPageLifecycle } from './pageLifecycle'

const pageshow = (persisted: boolean) => Object.assign(new Event('pageshow'), { persisted })

describe('createPageLifecycleGate', () => {
  it('首次加载的 pageshow 不会触发恢复；只有先离开过才会恢复，且只恢复一次', () => {
    const g = createPageLifecycleGate()
    assert.equal(g.suspended, false)
    assert.equal(g.show(), false)
    assert.equal(g.hide(), true)
    assert.equal(g.suspended, true)
    assert.equal(g.show(), true)
    assert.equal(g.suspended, false)
    assert.equal(g.show(), false)
  })

  it('重复的 pagehide 只关闭一次', () => {
    const g = createPageLifecycleGate()
    assert.equal(g.hide(), true)
    assert.equal(g.hide(), false)
    assert.equal(g.show(), true)
    assert.equal(g.hide(), true)
  })
})

describe('watchPageLifecycle', () => {
  function setup() {
    const win = new EventTarget()
    const doc = Object.assign(new EventTarget(), { visibilityState: 'visible' })
    const calls: string[] = []
    const stop = watchPageLifecycle({ suspend: () => calls.push('suspend'), resume: p => calls.push(`resume:${p}`) }, win, doc)
    return { win, doc, calls, stop }
  }

  it('pagehide 关闭、来自前进 / 后退缓存的 pageshow 恢复（带 persisted 标记）', () => {
    const { win, calls } = setup()
    win.dispatchEvent(new Event('pagehide'))
    win.dispatchEvent(pageshow(true))
    assert.deepEqual(calls, ['suspend', 'resume:true'])
  })

  it('普通加载的 pageshow（没有先离开）什么也不做', () => {
    const { win, calls } = setup()
    win.dispatchEvent(pageshow(false))
    assert.deepEqual(calls, [])
  })

  it('pageshow 与重新可见同时到达只恢复一次，顺序任意', () => {
    const a = setup()
    a.win.dispatchEvent(new Event('pagehide'))
    a.win.dispatchEvent(pageshow(true))
    a.doc.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(a.calls, ['suspend', 'resume:true'])

    const b = setup()
    b.win.dispatchEvent(new Event('pagehide'))
    b.doc.dispatchEvent(new Event('visibilitychange'))
    b.win.dispatchEvent(pageshow(true))
    assert.deepEqual(b.calls, ['suspend', 'resume:false'])
  })

  it('兜底：恢复时没有 pageshow，页面重新可见也能接上；仍不可见时不恢复', () => {
    const { win, doc, calls } = setup()
    win.dispatchEvent(new Event('pagehide'))
    doc.visibilityState = 'hidden'
    doc.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(calls, ['suspend'])
    doc.visibilityState = 'visible'
    doc.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(calls, ['suspend', 'resume:false'])
  })

  it('没有离开过时，页面在可见 / 不可见之间切换不会动连接', () => {
    const { doc, calls } = setup()
    doc.visibilityState = 'hidden'
    doc.dispatchEvent(new Event('visibilitychange'))
    doc.visibilityState = 'visible'
    doc.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(calls, [])
  })

  it('可以多次往返；取消监听之后不再响应', () => {
    const { win, calls, stop } = setup()
    for (let i = 0; i < 3; i++) {
      win.dispatchEvent(new Event('pagehide'))
      win.dispatchEvent(pageshow(true))
    }
    assert.deepEqual(calls, ['suspend', 'resume:true', 'suspend', 'resume:true', 'suspend', 'resume:true'])
    stop()
    win.dispatchEvent(new Event('pagehide'))
    assert.equal(calls.length, 6)
  })

  it('没有 window（测试 / 服务端渲染）时是空操作', () => {
    const stop = watchPageLifecycle({ suspend: () => assert.fail(), resume: () => assert.fail() }, undefined, undefined)
    stop()
  })
})
