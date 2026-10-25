import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { GateTask } from '../api/types'
import {
  GATE_HEAVY,
  GATE_LIST_SHOWN,
  GATE_SHELVE_PROGRESS,
  gateBreakdown,
  gateHeavy,
  gateKnown,
  gateOn,
  gateReleaseState,
  gateShown,
  gateSwitchTitle,
  gateTaskTarget,
  gateTotal,
} from './wrapupGate'

const t = (seq: number, state: string): GateTask => ({ id: seq + 1000, seq, state, title: `任务${seq}` })

describe('wrapupGate', () => {
  test('老服务端没有 strict_wrapup：当作没有收尾门，界面不出现相关元素', () => {
    assert.equal(gateKnown(undefined), false)
    assert.equal(gateKnown(null), false)
    assert.equal(gateKnown({}), false)
    assert.equal(gateOn({}), false)
    assert.equal(gateTotal({ gate_total: 5 }), 0)
    assert.equal(gateKnown({ strict_wrapup: false }), true)
    assert.equal(gateOn({ strict_wrapup: false }), false)
    assert.equal(gateOn({ strict_wrapup: true }), true)
  })

  test('门关着时总数为 0（即使服务端还带着旧的名单）', () => {
    assert.equal(gateTotal({ strict_wrapup: false, gate_total: 7 }), 0)
    assert.equal(gateTotal({ strict_wrapup: true, gate_total: 7 }), 7)
    assert.equal(gateTotal({ strict_wrapup: true }), 0)
    assert.equal(gateTotal({ strict_wrapup: true, gate_total: -3 }), 0)
  })

  test('积压提醒的阈值：超过 8 个才提醒', () => {
    assert.equal(GATE_HEAVY, 8)
    assert.equal(gateHeavy(8), false)
    assert.equal(gateHeavy(9), true)
  })

  test('名单拆分：直接列出前几个，其余只给个数', () => {
    const tasks = Array.from({ length: 10 }, (_, i) => t(i + 1, 'todo'))
    const a = gateShown(tasks, 14)
    assert.equal(a.shown.length, GATE_LIST_SHOWN)
    assert.equal(a.more, 14 - GATE_LIST_SHOWN)
    assert.deepEqual(gateShown(tasks.slice(0, 2), 2), { shown: tasks.slice(0, 2), more: 0 })
    assert.deepEqual(gateShown(undefined, 0), { shown: [], more: 0 })
  })

  test('各状态个数：待办 / 进行中 / 验证中；名单不全时末尾加省略号', () => {
    const tasks = [t(1, 'todo'), t(2, 'todo'), t(3, 'doing'), t(4, 'review')]
    assert.equal(gateBreakdown(tasks, 4), '待办 2 · 进行中 1 · 验证中 1')
    assert.equal(gateBreakdown(tasks, 30), '待办 2 · 进行中 1 · 验证中 1 …')
    assert.equal(gateBreakdown([], 0), '')
    assert.equal(gateBreakdown(undefined, 0), '')
  })

  test('「本次放行」什么时候能点、不能点时为什么', () => {
    const running = { strict_wrapup: true, gate_total: 3, reply_window_until: '2026-10-22T10:00:00Z' }
    const paused = { strict_wrapup: true, gate_total: 3, reply_window_paused: true, reply_window_bank_sec: 120 }
    assert.equal(gateReleaseState(paused).can, true)
    assert.equal(gateReleaseState(running).can, true)
    assert.match(gateReleaseState(paused).why, /任务一概不动/)

    const off = gateReleaseState({ strict_wrapup: false, gate_total: 3, reply_window_until: '2026-10-22T10:00:00Z' })
    assert.deepEqual([off.can, /已关闭/.test(off.why)], [false, true])
    const done = gateReleaseState({ ...paused, gate_released: true })
    assert.deepEqual([done.can, /已经放行/.test(done.why)], [false, true])
    const none = gateReleaseState({ strict_wrapup: true, gate_total: 0, reply_window_until: '2026-10-22T10:00:00Z' })
    assert.deepEqual([none.can, /没有未完成任务/.test(none.why)], [false, true])
    const idle = gateReleaseState({ strict_wrapup: true, gate_total: 3 })
    assert.deepEqual([idle.can, /没有回复窗口在等/.test(idle.why)], [false, true])
    // 暂停但没有攒着的时间：等同没有窗口
    assert.equal(gateReleaseState({ strict_wrapup: true, gate_total: 3, reply_window_paused: true, reply_window_bank_sec: 0 }).can, false)
    assert.equal(gateReleaseState(undefined).can, false)
  })

  test('点一行任务去任务页：?task= 带的是数据库编号（id），不是界面上写的 #N（seq）；不带任务只进任务页（#242）', () => {
    const task = t(2, 'doing') // id 1002、seq 2：两个数刻意不同
    const to = gateTaskTarget('wf-a', task)
    assert.deepEqual(to, { name: 'work', params: { workflowId: 'wf-a' }, query: { task: '1002' } })
    assert.notEqual(to.query?.task, String(task.seq), '把 #N 塞进 ?task= 会打开别的任务（数据库编号恰好等于它的那个）')
    // 「另有 N 个」：不带任务，只进这个工作流的任务页，不带 query
    assert.deepEqual(gateTaskTarget('wf-a'), { name: 'work', params: { workflowId: 'wf-a' } })
    // 不合法的编号（0、负数、小数、NaN）不带参数，免得打开不相干的任务
    for (const id of [0, -3, 1.5, Number.NaN]) {
      assert.equal(gateTaskTarget('wf-a', { id }).query, undefined, `id=${id}`)
    }
  })

  test('开关悬停说明讲清两种口径；替智能体置暂缓时写的进展带「等」字（不再被「暂缓要写缘由」提醒）', () => {
    assert.match(gateSwitchTitle(true), /待办 \/ 进行中 \/ 验证中.*暂缓/)
    assert.match(gateSwitchTitle(false), /老口径.*3 小时/)
    assert.ok(GATE_SHELVE_PROGRESS.includes('等'), '服务端用「等」字判断暂缓写了缘由')
  })
})
