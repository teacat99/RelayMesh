// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { backOrSession, backToSession, cameFromSession, hasInAppBack } from './sessionNav'

function fakeRouter() {
  const calls: string[] = []
  const router = {
    back: () => void calls.push('back'),
    push: async (loc: unknown) => void calls.push(`push:${String(loc)}`),
  }
  return { router, calls }
}

describe('cameFromSession：上一步是不是会话页', () => {
  it('back 是 / 时算是，带查询串或锚点也算', () => {
    assert.equal(cameFromSession({ back: '/' }), true)
    assert.equal(cameFromSession({ back: '/?wf=relaymesh-continuation' }), true)
    assert.equal(cameFromSession({ back: '/#top' }), true)
  })
  it('back 指向别的页面、缺失或类型不对时都不算', () => {
    assert.equal(cameFromSession({ back: '/projects/12' }), false)
    assert.equal(cameFromSession({ back: '/work/relaymesh-continuation' }), false)
    assert.equal(cameFromSession({ back: '/settings?from=/' }), false)
    assert.equal(cameFromSession({ back: null }), false)
    assert.equal(cameFromSession({ back: 3 }), false)
    assert.equal(cameFromSession({}), false)
    assert.equal(cameFromSession(null), false)
    assert.equal(cameFromSession(undefined), false)
    assert.equal(cameFromSession('/'), false)
  })
})

describe('backToSession', () => {
  it('从会话页进来：用浏览器的返回，不另外压一层历史', () => {
    const { router, calls } = fakeRouter()
    backToSession(router, { back: '/' })
    assert.deepEqual(calls, ['back'])
  })
  it('直接打开的链接（没有上一步）：前往会话页', () => {
    const { router, calls } = fakeRouter()
    backToSession(router, null)
    assert.deepEqual(calls, ['push:/'])
  })
  it('上一步是别的页面（例如项目列表 → 项目）：前往会话页，而不是退回那一页', () => {
    const { router, calls } = fakeRouter()
    backToSession(router, { back: '/projects' })
    assert.deepEqual(calls, ['push:/'])
  })
})

describe('hasInAppBack：上一步还在不在本应用里', () => {
  it('back 是路径字符串（任何页面）时算在', () => {
    assert.equal(hasInAppBack({ back: '/' }), true)
    assert.equal(hasInAppBack({ back: '/archive' }), true)
    assert.equal(hasInAppBack({ back: '/projects/3?tab=audit' }), true)
  })
  it('back 为空、缺失或类型不对时都不算（直接打开的链接、新标签页、外站跳来）', () => {
    assert.equal(hasInAppBack({ back: null }), false)
    assert.equal(hasInAppBack({ back: '' }), false)
    assert.equal(hasInAppBack({ back: 1 }), false)
    assert.equal(hasInAppBack({}), false)
    assert.equal(hasInAppBack(null), false)
    assert.equal(hasInAppBack(undefined), false)
    assert.equal(hasInAppBack('/'), false)
  })
})

describe('backOrSession：没有固定上一页的整页（归档库）的返回', () => {
  it('上一步在本应用里：用浏览器的返回，回到来的那一页（不一定是会话页）', () => {
    const { router, calls } = fakeRouter()
    backOrSession(router, { back: '/work/relaymesh-continuation' })
    assert.deepEqual(calls, ['back'])
  })
  it('直接打开的链接（即便浏览器历史里有别的条目）：前往会话页，不退到应用外面', () => {
    const { router, calls } = fakeRouter()
    backOrSession(router, { back: null, current: '/archive' })
    assert.deepEqual(calls, ['push:/'])
  })
  it('没有 history.state 时同样前往会话页', () => {
    const { router, calls } = fakeRouter()
    backOrSession(router, null)
    assert.deepEqual(calls, ['push:/'])
  })
})
