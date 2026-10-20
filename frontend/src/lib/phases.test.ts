import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import { canonicalPhaseId, isLegacyPhaseId, normalizePhaseTemplate, LEGACY_PHASE_ALIAS } from './phases'
import { DEFAULT_PHASE_TEMPLATE } from './defaultPrompts.generated'

describe('出厂阶段模板', () => {
  test('是 讨论 / 方案 / 开发 三段，每段都有完整文案', () => {
    assert.deepEqual(DEFAULT_PHASE_TEMPLATE.map(p => p.id), ['discuss', 'plan', 'dev'])
    assert.deepEqual(DEFAULT_PHASE_TEMPLATE.map(p => p.label), ['讨论', '方案', '开发'])
    for (const p of DEFAULT_PHASE_TEMPLATE) {
      assert.ok(p.description.length > 0 && p.prompt.length > 0, p.id)
      assert.ok(!isLegacyPhaseId(p.id), `${p.id} 不应是旧 id`)
    }
  })
})

describe('canonicalPhaseId', () => {
  test('旧 id 映射到新 id，其余原样返回', () => {
    assert.equal(canonicalPhaseId('assess'), 'discuss')
    assert.equal(canonicalPhaseId('verify'), 'dev')
    assert.equal(canonicalPhaseId('done'), 'dev')
    assert.equal(canonicalPhaseId(' assess '), 'discuss')
    assert.equal(canonicalPhaseId('plan'), 'plan')
    assert.equal(canonicalPhaseId('phase-1730'), 'phase-1730')
    assert.equal(canonicalPhaseId(''), '')
  })

  test('toString / constructor 之类的原型属性名不会被当成旧 id', () => {
    assert.equal(isLegacyPhaseId('constructor'), false)
    assert.equal(canonicalPhaseId('toString'), 'toString')
    assert.equal(Object.keys(LEGACY_PHASE_ALIAS).length, 3)
  })
})

describe('normalizePhaseTemplate', () => {
  test('空 / 缺失时给出厂三段，并且是副本（改它不会污染出厂模板）', () => {
    const a = normalizePhaseTemplate(undefined)
    assert.deepEqual(a.map(p => p.id), ['discuss', 'plan', 'dev'])
    a[0].label = '被改过'
    assert.equal(DEFAULT_PHASE_TEMPLATE[0].label, '讨论')
    assert.deepEqual(normalizePhaseTemplate([]).map(p => p.id), ['discuss', 'plan', 'dev'])
  })

  test('不含旧 id 时原样返回（用户改过的文案与自建阶段保留）', () => {
    const items = [
      { id: 'discuss', label: '聊聊', prompt: '自己的提示词' },
      { id: 'phase-1', label: '上线' },
    ]
    assert.equal(normalizePhaseTemplate(items), items)
  })

  test('五段式缓存换成出厂三段，自建阶段接在后面', () => {
    const legacy = [
      { id: 'assess', label: '评估' },
      { id: 'plan', label: '方案' },
      { id: 'dev', label: '开发' },
      { id: 'verify', label: '验证' },
      { id: 'done', label: '完成' },
      { id: 'phase-9', label: '上线前核对' },
    ]
    const got = normalizePhaseTemplate(legacy)
    assert.deepEqual(got.map(p => p.id), ['discuss', 'plan', 'dev', 'phase-9'])
    assert.equal(got[3].label, '上线前核对')
    assert.ok(((got[0] as { prompt?: string }).prompt ?? '').includes('讨论阶段'))
  })

  test('幂等', () => {
    const once = normalizePhaseTemplate([{ id: 'assess', label: '评估' }, { id: 'done', label: '完成' }])
    assert.deepEqual(normalizePhaseTemplate(once), once)
  })
})
