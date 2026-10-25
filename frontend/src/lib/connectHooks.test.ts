// 运行：pnpm --dir frontend run test:unit
// 惰性仓库的「连上后补取」登记处（lib/connectHooks.ts，#243）：登记 / 覆盖 / 注销、执行顺序、单个失败不拖累其余。
import assert from 'node:assert/strict'
import { afterEach, describe, it } from 'node:test'
import { connectHookNames, registerConnectHook, runConnectHooks, unregisterConnectHook } from './connectHooks'

afterEach(() => {
  for (const name of connectHookNames()) unregisterConnectHook(name)
})

/** 等已经排进微任务队列的 then 回调都执行完。 */
const flush = () => new Promise<void>(resolve => setImmediate(resolve))

describe('connectHooks', () => {
  it('按登记顺序执行每一个补取', () => {
    const calls: string[] = []
    registerConnectHook('a', () => void calls.push('a'))
    registerConnectHook('b', () => void calls.push('b'))
    registerConnectHook('c', () => void calls.push('c'))
    runConnectHooks()
    assert.deepEqual(calls, ['a', 'b', 'c'])
  })

  it('每次连上都执行（首次与重连一样），不是只跑一次', () => {
    let n = 0
    registerConnectHook('a', () => void n++)
    runConnectHooks()
    runConnectHooks()
    assert.equal(n, 2)
  })

  it('同名重复登记以后一次为准：仓库被重建也不会补取两次', () => {
    const calls: string[] = []
    registerConnectHook('docs', () => void calls.push('old'))
    registerConnectHook('docs', () => void calls.push('new'))
    runConnectHooks()
    assert.deepEqual(calls, ['new'])
    assert.deepEqual(connectHookNames(), ['docs'])
  })

  it('注销之后不再执行', () => {
    const calls: string[] = []
    registerConnectHook('a', () => void calls.push('a'))
    registerConnectHook('b', () => void calls.push('b'))
    unregisterConnectHook('a')
    runConnectHooks()
    assert.deepEqual(calls, ['b'])
  })

  it('没有登记任何仓库时什么也不做', () => {
    assert.doesNotThrow(() => runConnectHooks())
  })

  it('一个补取同步抛错：只报告它的名字，其余照常执行', () => {
    const calls: string[] = []
    const reported: Array<[string, unknown]> = []
    const boom = new Error('boom')
    registerConnectHook('first', () => void calls.push('first'))
    registerConnectHook('bad', () => {
      throw boom
    })
    registerConnectHook('last', () => void calls.push('last'))
    runConnectHooks((name, err) => reported.push([name, err]))
    assert.deepEqual(calls, ['first', 'last'])
    assert.deepEqual(reported, [['bad', boom]])
  })

  it('一个补取异步拒绝：同样只报告、不产生未处理的拒绝，其余照常执行', async () => {
    const calls: string[] = []
    const reported: Array<[string, unknown]> = []
    const boom = new Error('async boom')
    registerConnectHook('bad', () => Promise.reject(boom))
    registerConnectHook('ok', async () => void calls.push('ok'))
    runConnectHooks((name, err) => reported.push([name, err]))
    await flush()
    assert.deepEqual(calls, ['ok'])
    assert.deepEqual(reported, [['bad', boom]])
  })

  it('补取期间新登记的仓库不在这一轮里（执行的是登记表的快照）', () => {
    const calls: string[] = []
    registerConnectHook('a', () => {
      calls.push('a')
      registerConnectHook('late', () => void calls.push('late'))
    })
    runConnectHooks()
    assert.deepEqual(calls, ['a'])
    runConnectHooks()
    assert.deepEqual(calls, ['a', 'a', 'late'], '下一轮才轮到它')
  })

  it('没传 report 时失败写 console.warn（带仓库名），不抛出', () => {
    const warned: unknown[][] = []
    const original = console.warn
    console.warn = (...args: unknown[]) => void warned.push(args)
    try {
      registerConnectHook('named', () => {
        throw new Error('x')
      })
      assert.doesNotThrow(() => runConnectHooks())
    } finally {
      console.warn = original
    }
    assert.equal(warned.length, 1)
    assert.match(String(warned[0][0]), /named/)
  })
})
