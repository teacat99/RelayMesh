import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { createKeyedRerun } from './keyedRerun'

/** 一个可以由测试决定何时结束的「请求」。 */
function deferred() {
  let resolve!: () => void
  let reject!: (err: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** 记录每次执行的起止顺序；每次执行都等测试手动放行。 */
function harness() {
  const log: string[] = []
  const pending: Array<ReturnType<typeof deferred>> = []
  let started = 0
  const request = createKeyedRerun<string>(async key => {
    const n = ++started
    log.push(`start ${key}#${n}`)
    const d = deferred()
    pending.push(d)
    try {
      await d.promise
    } finally {
      log.push(`end ${key}#${n}`)
    }
  })
  return { log, pending, request, started: () => started }
}

/** 让已经排好的微任务都跑完。 */
const flush = () => new Promise<void>(resolve => setImmediate(resolve))

describe('createKeyedRerun：进行中又来了新请求，结束后补一次，不能丢', () => {
  it('没有进行中的请求时立刻执行，结束后不再多取', async () => {
    const h = harness()
    const first = h.request('wf')
    assert.deepEqual(h.log, ['start wf#1'])
    h.pending[0].resolve()
    await first
    await flush()
    assert.deepEqual(h.log, ['start wf#1', 'end wf#1'])
    assert.equal(h.started(), 1)
  })

  it('进行中又来了请求：当前这次结束后再取一次，而且是串行的（补的那次在前一次结束之后才开始）', async () => {
    const h = harness()
    const first = h.request('wf')
    // 前一次还没回来，事件又说「变了」：这个请求不能丢
    const second = h.request('wf')
    await second // 返回的是「已记下」，不等补取结束
    assert.equal(h.started(), 1, '进行中时不会并发再发一次')
    h.pending[0].resolve()
    await first
    await flush()
    assert.deepEqual(h.log, ['start wf#1', 'end wf#1', 'start wf#2'])
    h.pending[1].resolve()
    await flush()
    assert.deepEqual(h.log, ['start wf#1', 'end wf#1', 'start wf#2', 'end wf#2'])
    assert.equal(h.started(), 2)
  })

  it('进行中连来很多次请求，只补一次', async () => {
    const h = harness()
    const first = h.request('wf')
    await Promise.all([h.request('wf'), h.request('wf'), h.request('wf'), h.request('wf')])
    h.pending[0].resolve()
    await first
    await flush()
    assert.equal(h.started(), 2)
    h.pending[1].resolve()
    await flush()
    assert.equal(h.started(), 2, '补的那次结束后，没有新请求就不再继续')
  })

  it('补取进行中又来了请求：同样适用这条规则，还会再补一次', async () => {
    const h = harness()
    const first = h.request('wf')
    await h.request('wf')
    h.pending[0].resolve()
    await first
    await flush()
    assert.equal(h.started(), 2)
    await h.request('wf') // 第二次（补取）还在进行
    assert.equal(h.started(), 2)
    h.pending[1].resolve()
    await flush()
    assert.equal(h.started(), 3)
    h.pending[2].resolve()
    await flush()
    assert.equal(h.started(), 3)
  })

  it('不同钥匙互不影响：一个在进行中，另一个照常立刻执行', async () => {
    const h = harness()
    const a = h.request('a')
    const b = h.request('b')
    assert.deepEqual(h.log, ['start a#1', 'start b#2'])
    h.pending[0].resolve()
    h.pending[1].resolve()
    await Promise.all([a, b])
    await flush()
    assert.equal(h.started(), 2, '各自没有新请求，谁也不补')
  })

  it('执行本身失败：错误照常抛给调用方，之前记下的补取仍会执行，机制不会卡死', async () => {
    const h = harness()
    const first = h.request('wf')
    await h.request('wf')
    h.pending[0].reject(new Error('网络错误'))
    await assert.rejects(first, /网络错误/)
    await flush()
    assert.equal(h.started(), 2, '失败不能吞掉记下的补取')
    h.pending[1].resolve()
    await flush()
    // 之后还能正常用
    const third = h.request('wf')
    assert.equal(h.started(), 3)
    h.pending[2].resolve()
    await third
  })

  it('补取失败不会变成未处理的拒绝', async () => {
    const h = harness()
    const unhandled: unknown[] = []
    const onUnhandled = (err: unknown) => unhandled.push(err)
    process.on('unhandledRejection', onUnhandled)
    try {
      const first = h.request('wf')
      await h.request('wf')
      h.pending[0].resolve()
      await first
      await flush()
      h.pending[1].reject(new Error('补取失败'))
      await flush()
      await flush()
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }
    assert.deepEqual(unhandled, [])
  })
})
