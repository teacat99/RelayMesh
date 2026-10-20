import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { agentWorkingBasis, deriveSessionStatus, type SessionStatusInput } from './sessionStatus'

const NOW = Date.parse('2026-10-17T12:00:00.000Z')
const MIN = 60_000
const ago = (ms: number) => new Date(NOW - ms).toISOString()

/** 一个「什么都没发生」的基线：最近一轮已确认、没有待处理、没有任务、智能体没有记录。 */
function input(over: Partial<SessionStatusInput> = {}): SessionStatusInput {
  return { roundStatus: 'completed', pendingItems: 0, openTasks: 0, agentActivityAt: null, agentState: null, nowMs: NOW, ...over }
}

describe('agentWorkingBasis：智能体是不是在干活', () => {
  it('十分钟内有交互算「进行中」，满十分钟就不算（与收件箱芯片 active / recent / idle 同口径）', () => {
    assert.equal(agentWorkingBasis(null, ago(0), NOW), 'activity')
    assert.equal(agentWorkingBasis(null, ago(30_000), NOW), 'activity')
    assert.equal(agentWorkingBasis(null, ago(9 * MIN + 59_000), NOW), 'activity')
    assert.equal(agentWorkingBasis(null, ago(10 * MIN), NOW), '')
    assert.equal(agentWorkingBasis(null, ago(3 * 60 * MIN), NOW), '')
  })

  it('没有记录 / 时间解析不了：不算进行中', () => {
    assert.equal(agentWorkingBasis(null, null, NOW), '')
    assert.equal(agentWorkingBasis(null, undefined, NOW), '')
    assert.equal(agentWorkingBasis(null, 'not-a-time', NOW), '')
  })

  it('hook 汇报「工作中」优先：即使最近一次交互很久以前也算（长工具调用期间没有任何交互）', () => {
    assert.equal(agentWorkingBasis('working', ago(3 * 60 * MIN), NOW), 'hook')
    assert.equal(agentWorkingBasis('working', null, NOW), 'hook')
  })

  it('hook 汇报「已收尾」（空闲 / 结束 / 中断）覆盖近期交互：智能体刚发完总结就停了，不该再挂十分钟「进行中」', () => {
    for (const k of ['idle', 'ended', 'aborted']) assert.equal(agentWorkingBasis(k, ago(5_000), NOW), '', k)
  })

  it('hook 的其它状态（等批准 / 疑似卡住 / 离线 / 未知…）不改判，回落到交互时间', () => {
    for (const k of ['waiting_approval', 'waiting_you', 'stalled', 'error', 'offline', 'unknown']) {
      assert.equal(agentWorkingBasis(k, ago(2 * MIN), NOW), 'activity', k)
      assert.equal(agentWorkingBasis(k, ago(30 * MIN), NOW), '', k)
    }
  })
})

describe('deriveSessionStatus：会话级状态按优先级取第一个命中', () => {
  it('等回复的汇报算 1 项；与未答复的确认项相加', () => {
    const only = deriveSessionStatus(input({ roundStatus: 'pending' }))
    assert.equal(only?.key, 'waiting')
    assert.equal(only?.count, 1)
    assert.equal(only?.label, '等待您 · 1 项')

    const both = deriveSessionStatus(input({ roundStatus: 'pending', pendingItems: 3 }))
    assert.equal(both?.count, 4)
    assert.equal(both?.label, '等待您 · 4 项')
    assert.match(both!.title, /1 份等回复的汇报/)
    assert.match(both!.title, /3 条待答复的提问 \/ 计划 \/ 决策/)
  })

  it('最近一轮汇报早就答完、只有未答复的提问：照样「等待您」——这正是原来常驻「已完成」的缺陷', () => {
    const v = deriveSessionStatus(input({ roundStatus: 'completed', pendingItems: 2 }))
    assert.equal(v?.key, 'waiting')
    assert.equal(v?.count, 2)
    assert.equal(v?.tone, 'amber')
    assert.equal(v?.pulse, true)
    assert.doesNotMatch(v!.title, /汇报/)
  })

  it('没有汇报的工作流（纯异步对话）有待处理的提问也亮「等待您」', () => {
    const v = deriveSessionStatus(input({ roundStatus: null, pendingItems: 1 }))
    assert.equal(v?.key, 'waiting')
    assert.equal(v?.label, '等待您 · 1 项')
  })

  it('等待您优先于智能体进行中、超时、取消、空闲、已完成', () => {
    for (const roundStatus of ['pending', 'completed', 'timeout', 'cancelled']) {
      const v = deriveSessionStatus(input({ roundStatus, pendingItems: 1, agentActivityAt: ago(1_000), openTasks: 5, agentState: 'working' }))
      assert.equal(v?.key, 'waiting', roundStatus)
    }
  })

  it('已归档是生命周期状态：压过一切（含待处理与进行中）', () => {
    const v = deriveSessionStatus(input({ roundStatus: 'archived', pendingItems: 4, agentActivityAt: ago(1_000), openTasks: 2 }))
    assert.equal(v?.key, 'archived')
    assert.equal(v?.label, '已归档')
    assert.equal(v?.count, 0)
  })

  it('没有待处理且智能体十分钟内有动作：智能体进行中（天蓝、脉动），压过已完成 / 已超时 / 已取消 / 空闲', () => {
    for (const roundStatus of ['completed', 'timeout', 'cancelled', null]) {
      const v = deriveSessionStatus(input({ roundStatus, agentActivityAt: ago(2 * MIN), openTasks: 3 }))
      assert.equal(v?.key, 'working', String(roundStatus))
      assert.equal(v?.label, '智能体进行中')
      assert.equal(v?.tone, 'sky')
      assert.equal(v?.pulse, true)
      assert.equal(v?.count, 0)
    }
  })

  it('智能体进行中的悬停说明写明依据：hook 汇报，或最近一次交互的时间', () => {
    assert.match(deriveSessionStatus(input({ agentState: 'working' }))!.title, /hook/)
    const byActivity = deriveSessionStatus(input({ agentActivityAt: ago(4 * MIN) }))!
    assert.match(byActivity.title, /最近一次与 RelayMesh 交互/)
    assert.match(byActivity.title, /十分钟内都算进行中/)
  })

  it('智能体收了尾（hook 说空闲）时，十分钟内的交互不再撑着「进行中」，落到空闲 / 已完成', () => {
    assert.equal(deriveSessionStatus(input({ agentState: 'idle', agentActivityAt: ago(10_000), openTasks: 2 }))?.key, 'idle')
    assert.equal(deriveSessionStatus(input({ agentState: 'ended', agentActivityAt: ago(10_000) }))?.key, 'done')
  })

  it('没有待处理、智能体也不在干活：已取消 / 已超时沿用原样（位于空闲与已完成之前）', () => {
    const cancelled = deriveSessionStatus(input({ roundStatus: 'cancelled', openTasks: 2 }))
    assert.equal(cancelled?.key, 'cancelled')
    assert.equal(cancelled?.label, '已取消')
    assert.equal(cancelled?.variant, 'outline')

    const timeout = deriveSessionStatus(input({ roundStatus: 'timeout', openTasks: 2 }))
    assert.equal(timeout?.key, 'timeout')
    assert.equal(timeout?.label, '已超时')
    assert.equal(timeout?.variant, 'destructive')
  })

  it('空闲 = 没有待处理也无近期动作，但还有任务没结束（无论最近一轮是已确认还是没有汇报）', () => {
    for (const roundStatus of ['completed', null, '']) {
      const v = deriveSessionStatus(input({ roundStatus, openTasks: 2, agentActivityAt: ago(30 * MIN) }))
      assert.equal(v?.key, 'idle', String(roundStatus))
      assert.equal(v?.label, '空闲')
      assert.equal(v?.tone, 'muted')
      assert.equal(v?.pulse, false)
      assert.match(v!.title, /还有 2 个任务没结束/)
    }
  })

  it('空闲的悬停说明区分「有记录但很久了」与「服务启动以来没有交互记录」', () => {
    assert.match(deriveSessionStatus(input({ openTasks: 1, agentActivityAt: ago(40 * MIN) }))!.title, /最近一次与 RelayMesh 交互/)
    assert.match(deriveSessionStatus(input({ openTasks: 1, agentActivityAt: null }))!.title, /没有交互记录/)
  })

  it('已完成 = 最近一轮已确认、任务全结束、没有待处理、智能体不在干活', () => {
    const v = deriveSessionStatus(input())
    assert.equal(v?.key, 'done')
    assert.equal(v?.label, '已完成')
    assert.equal(v?.variant, 'default')
    assert.equal(v?.tone, null)
    assert.equal(v?.pulse, false)
  })

  it('没有汇报、没有任务、没有动静：不显示徽标', () => {
    assert.equal(deriveSessionStatus(input({ roundStatus: null })), null)
    assert.equal(deriveSessionStatus(input({ roundStatus: undefined })), null)
    assert.equal(deriveSessionStatus(input({ roundStatus: '' })), null)
  })

  it('没见过的汇报状态原样显示，徽标不会凭空消失', () => {
    const v = deriveSessionStatus(input({ roundStatus: 'weird' }))
    assert.equal(v?.key, 'other')
    assert.equal(v?.label, 'weird')
  })

  it('数量字段脏数据（负数 / NaN / 小数）按 0 / 向下取整处理', () => {
    assert.equal(deriveSessionStatus(input({ pendingItems: -3 }))?.key, 'done')
    assert.equal(deriveSessionStatus(input({ pendingItems: Number.NaN }))?.key, 'done')
    assert.equal(deriveSessionStatus(input({ pendingItems: 2.9 }))?.count, 2)
    assert.equal(deriveSessionStatus(input({ openTasks: Number.NaN }))?.key, 'done')
    assert.equal(deriveSessionStatus(input({ openTasks: -1 }))?.key, 'done')
  })

  it('随时间推移：十分钟前的交互会从「进行中」落回空闲（页头每半分钟重算一次）', () => {
    const at = ago(0)
    const t0 = deriveSessionStatus(input({ agentActivityAt: at, openTasks: 1, nowMs: NOW }))
    const t9 = deriveSessionStatus(input({ agentActivityAt: at, openTasks: 1, nowMs: NOW + 9 * MIN }))
    const t11 = deriveSessionStatus(input({ agentActivityAt: at, openTasks: 1, nowMs: NOW + 11 * MIN }))
    assert.deepEqual([t0?.key, t9?.key, t11?.key], ['working', 'working', 'idle'])
  })

  it('等待您的徽标不带「已完成」之类的旧词：词汇是「等待您 · N 项」', () => {
    const v = deriveSessionStatus(input({ roundStatus: 'pending', pendingItems: 1 }))!
    assert.doesNotMatch(v.label, /等待确认|已完成/)
  })
})
