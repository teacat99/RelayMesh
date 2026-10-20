// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { dropRetiredSettings, hasRetiredSettings } from './retiredSettings'

const online = [{ id: 'qp-on-1', title: 't', content: 'c' }]

describe('hasRetiredSettings', () => {
  it('干净的设置没有残留', () => {
    assert.equal(hasRetiredSettings(undefined), false)
    assert.equal(hasRetiredSettings(null), false)
    assert.equal(hasRetiredSettings('x'), false)
    assert.equal(hasRetiredSettings({}), false)
    assert.equal(
      hasRetiredSettings({
        statusPresets: { online },
        flowPrompts: { online: { waitPollPrompt: '', exhaustedPrompt: '' } },
        presetVersions: { baseline: { 'general|qp-1': 'a', 'status:online|qp-on-1': 'b', 'phase|dev': 'c' }, history: { 'general|qp-1': ['x'] }, archive: [{ uid: 'u', scope: 'phase' }] },
      }),
      false,
    )
  })

  it('默认状态、状态策略、暂离 / 托管的预设与提示词、它们的版本留存都算残留', () => {
    assert.equal(hasRetiredSettings({ userPresence: 'away' }), true)
    assert.equal(hasRetiredSettings({ statusStrategies: { away: {} } }), true)
    assert.equal(hasRetiredSettings({ statusPresets: { online, away: [] } }), true)
    assert.equal(hasRetiredSettings({ statusPresets: { online, autopilot: [] } }), true)
    assert.equal(hasRetiredSettings({ flowPrompts: { online: {}, away: { immediatePrompt: '' } } }), true)
    assert.equal(hasRetiredSettings({ presetVersions: { baseline: { 'status:away|qp-aw-1': 'a' } } }), true)
    assert.equal(hasRetiredSettings({ presetVersions: { history: { 'status:autopilot|qp-ap-1': ['a'] } } }), true)
    assert.equal(hasRetiredSettings({ presetVersions: { archive: [{ uid: 'u', scope: 'status:away' }] } }), true)
  })
})

describe('dropRetiredSettings', () => {
  it('丢掉残留字段，保留在线预设、提示词和其余设置，且不改入参', () => {
    const input = {
      hostName: 'h',
      userPresence: 'autopilot',
      statusStrategies: { away: { x: 1 } },
      statusPresets: { online, away: [{ id: 'qp-aw-1' }], autopilot: [{ id: 'qp-ap-1' }] },
      flowPrompts: { online: { waitPollPrompt: 'w', exhaustedPrompt: 'e' }, away: { immediatePrompt: 'a' }, autopilot: { immediatePrompt: 'p' } },
      presetVersions: {
        baseline: { 'status:away|qp-aw-1': 'a', 'status:online|qp-on-1': 'b' },
        history: { 'status:autopilot|qp-ap-1': ['x'] },
        archive: [{ uid: 'u', scope: 'status:away', payload: { id: 'qp-aw-1' }, kind: 'old' }],
      },
    }
    const snapshot = JSON.stringify(input)
    const out = dropRetiredSettings(input) as Record<string, any>
    assert.equal(JSON.stringify(input), snapshot)
    assert.equal(out.hostName, 'h')
    assert.equal('userPresence' in out, false)
    assert.equal('statusStrategies' in out, false)
    assert.deepEqual(out.statusPresets, { online })
    assert.deepEqual(out.flowPrompts, { online: { waitPollPrompt: 'w', exhaustedPrompt: 'e' } })
    assert.deepEqual(out.presetVersions, { baseline: { 'status:online|qp-on-1': 'b' }, history: {}, archive: [] })
    assert.equal(hasRetiredSettings(out), false)
  })

  it('没有这些字段的设置原样通过（不凭空多出字段）', () => {
    const out = dropRetiredSettings({ hostName: 'h' }) as Record<string, any>
    assert.deepEqual(out, { hostName: 'h' })
  })
})
