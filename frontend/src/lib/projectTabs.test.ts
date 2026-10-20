import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import { DEFAULT_MANAGE_SECTION, isManageSection, MANAGE_SECTIONS, moveManageSection, resolveProjectTab } from './projectTabs'

describe('resolveProjectTab（?tab= → 页签 + 管理分区；旧链接一个都不能断）', () => {
  test('没有 tab / 认不得的取值 → 知识库', () => {
    for (const raw of [undefined, null, '', 'kb', 'nope', 42, {}]) {
      assert.deepEqual(resolveProjectTab(raw), { tab: 'kb', section: DEFAULT_MANAGE_SECTION })
    }
  })

  test('模块与单个模块页都落在「模块」页签', () => {
    assert.equal(resolveProjectTab('modules').tab, 'modules')
    assert.equal(resolveProjectTab('module').tab, 'module')
  })

  test('设置 / 迁移 / 授权 / 审计 四个旧取值统归「管理」，并指明分区', () => {
    for (const s of ['settings', 'migration', 'grants', 'audit'] as const) {
      assert.deepEqual(resolveProjectTab(s), { tab: 'manage', section: s })
    }
  })

  test('手输的 manage 落在默认分区（设置）', () => {
    assert.deepEqual(resolveProjectTab('manage'), { tab: 'manage', section: 'settings' })
  })

  test('同名参数出现多次时（数组）取第一个', () => {
    assert.deepEqual(resolveProjectTab(['audit', 'kb']), { tab: 'manage', section: 'audit' })
    assert.deepEqual(resolveProjectTab([]), { tab: 'kb', section: 'settings' })
  })
})

describe('isManageSection', () => {
  test('只认四个分区', () => {
    assert.deepEqual(MANAGE_SECTIONS.map(s => s.id), ['settings', 'migration', 'grants', 'audit'])
    assert.equal(isManageSection('grants'), true)
    assert.equal(isManageSection('kb'), false)
    assert.equal(isManageSection(undefined), false)
  })
})

describe('moveManageSection（键盘在分区间移动）', () => {
  test('上 / 左往前，下 / 右往后，到头不循环', () => {
    assert.equal(moveManageSection('migration', 'ArrowDown'), 'grants')
    assert.equal(moveManageSection('migration', 'ArrowRight'), 'grants')
    assert.equal(moveManageSection('migration', 'ArrowUp'), 'settings')
    assert.equal(moveManageSection('migration', 'ArrowLeft'), 'settings')
    assert.equal(moveManageSection('settings', 'ArrowUp'), 'settings')
    assert.equal(moveManageSection('audit', 'ArrowDown'), 'audit')
  })

  test('Home / End 跳首尾；别的键不管', () => {
    assert.equal(moveManageSection('grants', 'Home'), 'settings')
    assert.equal(moveManageSection('grants', 'End'), 'audit')
    assert.equal(moveManageSection('grants', 'Enter'), null)
    assert.equal(moveManageSection('grants', 'a'), null)
  })
})
