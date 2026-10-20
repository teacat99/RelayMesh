import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import { windowTitle, windowView } from './replyWindow'

describe('replyWindow', () => {
  const now = Date.parse('2026-10-15T10:00:00Z')
  test('没有截止时间 = 空闲', () => {
    assert.deepEqual(windowView(null, now), { kind: 'none', text: '空闲', leftMs: 0 })
    assert.equal(windowView('乱写', now).kind, 'none')
  })
  test('倒计时显示 mm:ss，超过一小时带小时', () => {
    assert.equal(windowView('2026-10-15T10:05:09Z', now).text, '05:09')
    assert.equal(windowView('2026-10-15T11:01:01Z', now).text, '1:01:01')
    assert.equal(windowView('2026-10-15T10:05:00Z', now).kind, 'running')
  })
  test('到期', () => {
    assert.equal(windowView('2026-10-15T09:59:59Z', now).kind, 'expired')
    assert.equal(windowView('2026-10-15T10:00:00Z', now).text, '已到期')
  })
  test('有任务在做：窗口暂停，显示攒着的时间', () => {
    const v = windowView({ until: null, paused: true, bankSec: 300, activeTasks: 2 }, now)
    assert.deepEqual([v.kind, v.text, v.activeTasks], ['paused', '05:00', 2])
    assert.match(windowTitle(v, 5, 10), /已暂停.*2 个进行中/)
    // 必须等用户拍板的任务置「暂缓」，不挡窗口：悬停说明要把这条规则告诉用户
    assert.match(windowTitle(v, 5, 10), /「暂缓」.*不挡窗口/)
    // 没攒时间就不算暂停
    assert.equal(windowView({ paused: true, bankSec: 0 }, now).kind, 'none')
  })
  test('悬停说明随状态变化', () => {
    assert.match(windowTitle(windowView('2026-10-15T10:05:00Z', now), 5, 10), /剩余等待回复时间 05:00/)
    assert.match(windowTitle(windowView(null, now), 5, 10), /没有在等你回复/)
  })
})
