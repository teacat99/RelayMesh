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
  test('收尾门开着且暂停：状态块带上挡着的任务数，悬停说明讲清楚怎么放下', () => {
    const v = windowView({ paused: true, bankSec: 300, activeTasks: 5, strict: true, gateTotal: 5 }, now)
    assert.deepEqual([v.kind, v.text, v.gated, v.gateTotal], ['paused', '05:00', true, 5])
    const t = windowTitle(v, 5, 10)
    assert.match(t, /收尾门开着.*5 个未完成任务.*挡着收尾/)
    assert.match(t, /「暂缓」.*不挡窗口/)
    assert.match(t, /本次放行/)
    // 老服务端 / 门关着：不带 gated，文案仍是老口径
    const legacy = windowView({ paused: true, bankSec: 300, activeTasks: 2 }, now)
    assert.equal(legacy.gated, undefined)
    assert.equal(legacy.gateTotal, undefined)
    assert.match(windowTitle(legacy, 5, 10), /2 个进行中 \/ 验证中/)
    // 门开着但名单总数没给：退回用 activeTasks
    assert.equal(windowView({ paused: true, bankSec: 60, activeTasks: 3, strict: true }, now).gateTotal, 3)
  })
  test('已放行：窗口照常走，状态块标放行并说明仍有几个未完成任务', () => {
    const v = windowView({ until: '2026-10-15T10:05:00Z', strict: true, released: true, gateTotal: 4 }, now)
    assert.deepEqual([v.kind, v.text, v.released, v.gateTotal], ['running', '05:00', true, 4])
    assert.match(windowTitle(v, 5, 10), /你已「本次放行」收尾门：仍有 4 个未完成任务/)
    const expired = windowView({ until: '2026-10-15T09:00:00Z', strict: true, released: true, gateTotal: 4 }, now)
    assert.equal(expired.kind, 'expired')
    assert.equal(expired.released, true)
    // 放行了但任务已清零 / 门关着：不标记
    assert.equal(windowView({ until: '2026-10-15T10:05:00Z', strict: true, released: true, gateTotal: 0 }, now).released, undefined)
    assert.equal(windowView({ until: '2026-10-15T10:05:00Z', released: true, gateTotal: 4 }, now).released, undefined)
    // 空闲状态保持原样（没有多余的键）
    assert.deepEqual(windowView({ strict: true, released: true, gateTotal: 4 }, now), { kind: 'none', text: '空闲', leftMs: 0 })
  })
})
