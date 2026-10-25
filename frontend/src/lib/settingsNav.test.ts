import { test, describe } from 'node:test'
import assert from 'node:assert/strict'
import { NAV_KEYS, moduleSegments, moveCategory, navCategoryIds, navGroups } from './settingsNav'
import { categoriesOwnedBy, jumpLabel } from './settingsRegistry'

const ids = (module: 'relay' | 'exec') => navCategoryIds(module) as string[]

describe('navGroups：左栏 / 选择条的分组', () => {
  test('RelayMesh：先是本模块的 7 个分类，再是通用的 5 个，组名带模块名', () => {
    const g = navGroups('relay')
    assert.deepEqual(g.map(x => x.key), ['own', 'common'])
    assert.deepEqual(g.map(x => x.title), ['RelayMesh 设置', '通用设置'])
    assert.deepEqual(g[0].items.map(c => c.id), ['waiting', 'prompts', 'speech', 'presets', 'authProfiles', 'sessionPrompts', 'sessionData'])
    assert.deepEqual(g[1].items.map(c => c.id), ['appearance', 'security', 'oauth', 'backup', 'about'])
  })

  test('ExecMesh：本模块只有 3 个分类，另一个模块的分类不出现', () => {
    const g = navGroups('exec')
    assert.deepEqual(g.map(x => x.title), ['ExecMesh 设置', '通用设置'])
    assert.deepEqual(g[0].items.map(c => c.id), ['termPrefs', 'modelAccess', 'retention'])
    const all = ids('exec')
    for (const c of categoriesOwnedBy('relay')) assert.ok(!all.includes(c.id), `${c.id} 不该出现在 ExecMesh 的导航里`)
  })

  test('导航的全部分类 = 分组顺序展开，没有重复', () => {
    for (const m of ['relay', 'exec'] as const) {
      const flat = navGroups(m).flatMap(g => g.items.map(c => c.id))
      assert.deepEqual(navCategoryIds(m), flat)
      assert.equal(new Set(flat).size, flat.length, m)
    }
    assert.equal(ids('relay').length, 12)
    assert.equal(ids('exec').length, 8)
  })
})

describe('moveCategory：左栏的键盘操作', () => {
  test('向下 / 向上走一格，顺序与导航一致（跨过「本模块 → 通用」的分界）', () => {
    assert.equal(moveCategory('relay', 'waiting', 'ArrowDown'), 'prompts')
    assert.equal(moveCategory('relay', 'sessionData', 'ArrowDown'), 'appearance')
    assert.equal(moveCategory('relay', 'appearance', 'ArrowUp'), 'sessionData')
    assert.equal(moveCategory('exec', 'retention', 'ArrowDown'), 'appearance')
    assert.equal(moveCategory('exec', 'appearance', 'ArrowUp'), 'retention')
  })

  test('在首尾处循环', () => {
    assert.equal(moveCategory('relay', 'about', 'ArrowDown'), 'waiting')
    assert.equal(moveCategory('relay', 'waiting', 'ArrowUp'), 'about')
    assert.equal(moveCategory('exec', 'about', 'ArrowDown'), 'termPrefs')
    assert.equal(moveCategory('exec', 'termPrefs', 'ArrowUp'), 'about')
  })

  test('Home / End 到首尾，与当前位置无关', () => {
    for (const cur of ids('relay')) {
      assert.equal(moveCategory('relay', cur, 'Home'), 'waiting', cur)
      assert.equal(moveCategory('relay', cur, 'End'), 'about', cur)
    }
  })

  test('不是导航键就返回 null，调用方不拦截浏览器原有行为', () => {
    for (const k of ['Enter', ' ', 'Tab', 'ArrowLeft', 'ArrowRight', 'a', 'Escape', '']) {
      assert.equal(moveCategory('relay', 'waiting', k), null, JSON.stringify(k))
    }
  })

  test('当前分类不在这个模块的导航里（刚跨模块、未知 id）：向下 / Home 落第一项，向上 / End 落最后一项', () => {
    assert.equal(moveCategory('relay', 'termPrefs', 'ArrowDown'), 'waiting')
    assert.equal(moveCategory('relay', 'termPrefs', 'ArrowUp'), 'about')
    assert.equal(moveCategory('exec', '', 'ArrowDown'), 'termPrefs')
    assert.equal(moveCategory('exec', 'nope', 'ArrowUp'), 'about')
  })

  test('每个导航键都有对应结果；NAV_KEYS 与 switch 保持一致', () => {
    for (const k of NAV_KEYS) assert.notEqual(moveCategory('relay', 'waiting', k), null, k)
  })

  test('沿着向下键走一圈，恰好经过每个分类一次再回到起点', () => {
    for (const m of ['relay', 'exec'] as const) {
      const all = ids(m)
      const seen: string[] = []
      let cur = all[0]
      for (let i = 0; i < all.length; i++) {
        seen.push(cur)
        cur = moveCategory(m, cur, 'ArrowDown')!
      }
      assert.deepEqual(seen, all)
      assert.equal(cur, all[0])
    }
  })
})

describe('moduleSegments：顶部分段切换', () => {
  test('两个模块固定顺序（RelayMesh 在前），恰好一个是当前', () => {
    for (const m of ['relay', 'exec'] as const) {
      const s = moduleSegments(m)
      assert.deepEqual(s.map(x => x.id), ['relay', 'exec'])
      assert.deepEqual(s.map(x => x.name), ['RelayMesh', 'ExecMesh'])
      assert.equal(s.filter(x => x.current).length, 1)
      assert.equal(s.find(x => x.current)!.id, m)
    }
  })

  test('另一个模块的说明就是「去 … 的设置」，当前模块的说明写明现在看的是谁', () => {
    const s = moduleSegments('relay')
    assert.equal(s[1].title, jumpLabel('relay'))
    assert.equal(s[1].title, '去 ExecMesh 的设置')
    assert.match(s[0].title, /RelayMesh/)
    assert.equal(moduleSegments('exec')[0].title, '去 RelayMesh 的设置')
  })
})
