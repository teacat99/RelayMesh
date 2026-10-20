import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { agentLiveIcon } from './agentLive'
import type { AgentStateView } from './agentState'

describe('agentLiveIcon', () => {
  it('unknown 不覆盖默认图标', () => {
    const v = agentLiveIcon({ state: 'unknown', reason: '需要 hook' })
    assert.equal(v.active, false)
  })

  it('quiet 只改图标状态，不再带文字后缀（避免与 hook 芯片的时间重复，#104）', () => {
    const at = new Date(Date.parse('2026-10-13T12:00:00.000Z') - 5 * 60_000).toISOString()
    const v = agentLiveIcon({ state: 'quiet', reason: '智能体应在执行', at })
    assert.equal(v.active, true)
    assert.equal(v.state, 'quiet')
    assert.equal('suffix' in v, false)
    // 完整说明留在悬停提示里
    assert.ok(v.title.includes('智能体应在执行'))
  })

  it('状态机有结果时优先于旧口径（#113）', () => {
    const view = (key: AgentStateView['key']): AgentStateView => ({
      key,
      text: key,
      detail: '',
      tone: 'muted',
      pulse: false,
      flag: '',
      flagText: '',
      title: `状态机：${key}`,
    })
    const legacyIdle = { state: 'idle', reason: '旧口径' } as const
    const table: Array<[AgentStateView['key'], string]> = [
      ['working', 'running'],
      ['waiting_you', 'running'],
      ['idle', 'idle'],
      ['ended', 'idle'],
      ['aborted', 'idle'],
      ['waiting_approval', 'quiet'],
      ['offline', 'quiet'],
      ['error', 'alert'],
      ['stalled', 'alert'],
    ]
    for (const [key, want] of table) {
      const v = agentLiveIcon(legacyIdle, view(key))
      assert.equal(v.active, true, key)
      assert.equal(v.state, want, key)
      assert.equal(v.title, `状态机：${key}`, key)
    }
  })

  it('状态机没有这个工作流（未接入）时回落到旧口径', () => {
    const unknown: AgentStateView = {
      key: 'unknown', text: '未接入', detail: '', tone: 'muted', pulse: false, flag: '', flagText: '', title: 'x',
    }
    const v = agentLiveIcon({ state: 'running', reason: '旧口径运行中' }, unknown)
    assert.equal(v.state, 'running')
    assert.equal(v.title, '旧口径运行中')
    // 两边都没有信息：不覆盖默认图标
    assert.equal(agentLiveIcon(null, unknown).active, false)
    assert.equal(agentLiveIcon(undefined, null).active, false)
  })

  it('running / idle 覆盖图标且没有后缀', () => {
    const running = agentLiveIcon({ state: 'running', reason: '运行中' })
    assert.equal(running.active, true)
    assert.equal('suffix' in running, false)
    const idle = agentLiveIcon({ state: 'idle', reason: '' })
    assert.equal(idle.state, 'idle')
    assert.ok(idle.title.length > 0)
  })
})
