import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import {
  SETTINGS_CATEGORIES,
  categoryById,
  categoriesOwnedBy,
  visibleCategories,
  firstCategory,
  resolveCategory,
  settingsTitle,
  jumpLabel,
} from './settingsRegistry'

describe('分类注册表', () => {
  test('每个分类都有合法的归属、名称和说明，id 不重复', () => {
    const owners = new Set(['common', 'relay', 'exec'])
    const ids = new Set<string>()
    for (const c of SETTINGS_CATEGORIES) {
      assert.ok(owners.has(c.owner), `${c.id} 的归属 ${c.owner} 不合法`)
      assert.ok(c.label.trim().length > 0, `${c.id} 缺名称`)
      assert.ok(c.hint.trim().length > 0, `${c.id} 缺说明`)
      assert.ok(!ids.has(c.id), `${c.id} 重复`)
      ids.add(c.id)
    }
  })

  test('通用、RelayMesh、ExecMesh 各自至少有一个分类', () => {
    for (const owner of ['common', 'relay', 'exec'] as const) {
      assert.ok(categoriesOwnedBy(owner).length > 0, owner)
    }
  })

  test('预期的归属：通用 4 个、RelayMesh 6 个、ExecMesh 3 个', () => {
    assert.deepEqual(categoriesOwnedBy('common').map(c => c.id), ['appearance', 'security', 'backup', 'about'])
    assert.deepEqual(categoriesOwnedBy('relay').map(c => c.id), ['waiting', 'prompts', 'speech', 'presets', 'authProfiles', 'sessionData'])
    assert.deepEqual(categoriesOwnedBy('exec').map(c => c.id), ['termPrefs', 'modelAccess', 'retention'])
  })
})

describe('visibleCategories', () => {
  test('ExecMesh 窗口里看不到 RelayMesh 专属的分类，反之亦然', () => {
    const exec = visibleCategories('exec')
    assert.ok(exec.own.every(c => c.owner === 'exec'))
    assert.ok(exec.common.every(c => c.owner === 'common'))
    const relay = visibleCategories('relay')
    assert.ok(relay.own.every(c => c.owner === 'relay'))
    const execIds = new Set([...exec.own, ...exec.common].map(c => c.id))
    for (const c of relay.own) assert.ok(!execIds.has(c.id), `${c.id} 不应出现在 ExecMesh 窗口`)
  })

  test('第一个分类是本模块自己的，而不是通用的', () => {
    assert.equal(firstCategory('relay'), 'waiting')
    assert.equal(firstCategory('exec'), 'termPrefs')
  })
})

describe('resolveCategory', () => {
  test('本模块的分类与通用分类原样返回', () => {
    assert.equal(resolveCategory('exec', 'modelAccess'), 'modelAccess')
    assert.equal(resolveCategory('exec', 'security'), 'security')
    assert.equal(resolveCategory('relay', 'prompts'), 'prompts')
  })

  test('另一个模块的分类、未知 id、空值都回到本模块的第一个分类', () => {
    assert.equal(resolveCategory('exec', 'waiting'), 'termPrefs')
    assert.equal(resolveCategory('relay', 'modelAccess'), 'waiting')
    assert.equal(resolveCategory('relay', 'nope'), 'waiting')
    assert.equal(resolveCategory('exec', ''), 'termPrefs')
    assert.equal(resolveCategory('exec', undefined), 'termPrefs')
    assert.equal(resolveCategory('exec', 'constructor'), 'termPrefs')
  })

  test('categoryById 不会把原型属性当成分类', () => {
    assert.equal(categoryById('toString'), undefined)
    assert.equal(categoryById('about')?.owner, 'common')
  })
})

describe('标题与跳转按钮', () => {
  test('窗口标题带当前模块名', () => {
    assert.equal(settingsTitle('relay'), '设置 · RelayMesh')
    assert.equal(settingsTitle('exec'), '设置 · ExecMesh')
  })

  test('跳转按钮指向对方模块', () => {
    assert.equal(jumpLabel('relay'), '去 ExecMesh 的设置')
    assert.equal(jumpLabel('exec'), '去 RelayMesh 的设置')
  })
})
