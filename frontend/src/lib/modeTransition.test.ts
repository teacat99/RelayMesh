import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { MODE_SWITCH_MS, MODE_SWITCH_SAFETY_MS, modeSwitchDirection, modeSwitchPlan, type ModeSwitchEnv } from './modeTransition'

const ok: ModeSwitchEnv = { supported: true, enabled: true, reducedMotion: false, hidden: false }

describe('modeSwitchDirection', () => {
  it('RelayMesh → ExecMesh 向前，回来向后', () => {
    assert.equal(modeSwitchDirection('relay', 'exec'), 'forward')
    assert.equal(modeSwitchDirection('exec', 'relay'), 'back')
  })
})

describe('modeSwitchPlan', () => {
  it('跨项目的切换才播：RelayMesh → ExecMesh 向前，ExecMesh → RelayMesh 向后', () => {
    assert.equal(modeSwitchPlan('/', '/exec', ok), 'forward')
    assert.equal(modeSwitchPlan('/exec/t/abc', '/', ok), 'back')
  })

  it('RelayMesh 的各个页面（会话、任务、设置、归档）都算同一个项目', () => {
    assert.equal(modeSwitchPlan('/work/wf-1?task=3', '/exec/hosts', ok), 'forward')
    assert.equal(modeSwitchPlan('/exec/r/key', '/settings', ok), 'back')
    assert.equal(modeSwitchPlan('/archive', '/exec', ok), 'forward')
  })

  it('同一项目里换页不播', () => {
    assert.equal(modeSwitchPlan('/', '/settings', ok), null)
    assert.equal(modeSwitchPlan('/', '/work/wf-1', ok), null)
    assert.equal(modeSwitchPlan('/exec', '/exec/hosts', ok), null)
    assert.equal(modeSwitchPlan('/exec/t/a', '/exec/t/b', ok), null)
    // 只是地址里的参数变了
    assert.equal(modeSwitchPlan('/exec?x=1', '/exec?x=2', ok), null)
  })

  it('首次进入页面（没有上一页）不播', () => {
    assert.equal(modeSwitchPlan(null, '/exec', ok), null)
    assert.equal(modeSwitchPlan(null, '/', ok), null)
  })

  it('浏览器不支持、设置里关掉、要求减少动态效果、页面不可见：都不播', () => {
    assert.equal(modeSwitchPlan('/', '/exec', { ...ok, supported: false }), null)
    assert.equal(modeSwitchPlan('/', '/exec', { ...ok, enabled: false }), null)
    assert.equal(modeSwitchPlan('/', '/exec', { ...ok, reducedMotion: true }), null)
    assert.equal(modeSwitchPlan('/', '/exec', { ...ok, hidden: true }), null)
  })
})

describe('常量', () => {
  it('动画不超过 700ms，兜底时间明显长于动画本身', () => {
    assert.ok(MODE_SWITCH_MS <= 700)
    assert.ok(MODE_SWITCH_SAFETY_MS > MODE_SWITCH_MS * 3)
  })
})
