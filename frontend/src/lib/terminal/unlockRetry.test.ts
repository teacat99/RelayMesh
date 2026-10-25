import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { carriesUnlock, withFreshUnlock, withUnlockOnDemand, type FreshUnlockDeps, type UnlockOnDemandDeps } from './unlockRetry'

class Needs extends Error {}
class Cancelled extends Error {}

function deps(over: Partial<UnlockOnDemandDeps> = {}): UnlockOnDemandDeps & { log: string[] } {
  const log: string[] = []
  return {
    log,
    needsUnlock: e => e instanceof Needs,
    unlock: async () => {
      log.push('unlock')
      return true
    },
    stale: () => log.push('stale'),
    cancelled: () => new Cancelled('cancelled'),
    done: () => log.push('done'),
    ...over,
  }
}

test('不需要解锁时只执行一次，不弹窗', async () => {
  const d = deps()
  let n = 0
  const out = await withUnlockOnDemand(async () => ++n, d)
  assert.equal(out, 1)
  assert.deepEqual(d.log, [])
})

test('服务端要求解锁：弹窗后重试一次并顺延解锁期', async () => {
  const d = deps()
  let n = 0
  const out = await withUnlockOnDemand(async () => {
    n++
    if (n === 1) throw new Needs('x')
    return 'ok'
  }, d)
  assert.equal(out, 'ok')
  assert.equal(n, 2)
  assert.deepEqual(d.log, ['stale', 'unlock', 'done'])
})

test('用户取消解锁：不重试，抛出取消错误', async () => {
  const d = deps({ unlock: async () => false })
  let n = 0
  await assert.rejects(
    withUnlockOnDemand(async () => {
      n++
      throw new Needs('x')
    }, d),
    Cancelled,
  )
  assert.equal(n, 1)
  assert.deepEqual(d.log, ['stale'])
})

test('其他错误原样抛出，不弹窗', async () => {
  const d = deps()
  const boom = new Error('boom')
  await assert.rejects(withUnlockOnDemand(async () => Promise.reject(boom), d), e => e === boom)
  assert.deepEqual(d.log, [])
})

test('重试仍然失败：错误原样抛出（不再循环）', async () => {
  const d = deps()
  let n = 0
  await assert.rejects(
    withUnlockOnDemand(async () => {
      n++
      throw new Needs(`#${n}`)
    }, d),
    Needs,
  )
  assert.equal(n, 2)
  assert.deepEqual(d.log, ['stale', 'unlock'])
})

class Coded extends Error {
  constructor(readonly code: string) {
    super(code)
  }
}

function freshDeps(over: Partial<FreshUnlockDeps> = {}): FreshUnlockDeps & { log: string[] } {
  const log: string[] = []
  return {
    log,
    codeOf: e => (e instanceof Coded ? e.code : ''),
    reverify: async () => {
      log.push('reverify')
      return true
    },
    stale: () => log.push('stale'),
    cancelled: () => new Cancelled('cancelled'),
    done: () => log.push('done'),
    ...over,
  }
}

test('刚验证过密码：先强制弹出密码框，再发请求，只发一次', async () => {
  const d = freshDeps()
  let n = 0
  const out = await withFreshUnlock(async () => {
    d.log.push('call')
    return ++n
  }, d, { force: true })
  assert.equal(out, 1)
  assert.deepEqual(d.log, ['reverify', 'call', 'done'])
})

test('强制重新验证时用户取消：请求根本不发出', async () => {
  const d = freshDeps({ reverify: async () => false })
  let n = 0
  await assert.rejects(withFreshUnlock(async () => ++n, d, { force: true }), Cancelled)
  assert.equal(n, 0)
})

test('不强制：没有被要求时不弹窗（和普通保存一样）', async () => {
  const d = freshDeps()
  assert.equal(await withFreshUnlock(async () => 'ok', d), 'ok')
  assert.deepEqual(d.log, ['done'])
})

test('服务端回复令牌不够新：弹出密码框后重试一次，且不清掉本地令牌（取消时旧令牌还可用）', async () => {
  const d = freshDeps()
  let n = 0
  const out = await withFreshUnlock(async () => {
    if (++n === 1) throw new Coded('unlock_fresh_required')
    return 'ok'
  }, d)
  assert.equal(out, 'ok')
  assert.equal(n, 2)
  assert.deepEqual(d.log, ['reverify', 'done'])
})

test('服务端回复未解锁：先清掉本地的失效令牌，再弹出密码框并重试', async () => {
  const d = freshDeps()
  let n = 0
  await withFreshUnlock(async () => {
    if (++n === 1) throw new Coded('unlock_required')
  }, d)
  assert.deepEqual(d.log, ['stale', 'reverify', 'done'])
})

test('强制之后服务端仍说不够新：只再验证并重试一次，不循环', async () => {
  const d = freshDeps()
  let n = 0
  await assert.rejects(
    withFreshUnlock(async () => {
      n++
      throw new Coded('unlock_fresh_required')
    }, d, { force: true }),
    Coded,
  )
  assert.equal(n, 2)
  assert.deepEqual(d.log, ['reverify', 'reverify'])
})

test('服务端要求重新验证而用户取消：不重试', async () => {
  const d = freshDeps({ reverify: async () => false })
  let n = 0
  await assert.rejects(
    withFreshUnlock(async () => {
      n++
      throw new Coded('unlock_fresh_required')
    }, d),
    Cancelled,
  )
  assert.equal(n, 1)
})

test('其他错误（含 403 以外的业务错误）原样抛出，不弹窗', async () => {
  const d = freshDeps()
  const boom = new Coded('bad_request')
  await assert.rejects(withFreshUnlock(async () => Promise.reject(boom), d), e => e === boom)
  assert.deepEqual(d.log, [])
})

test('哪些请求路径会带上解锁令牌', () => {
  assert.equal(carriesUnlock('/terminal/hosts'), true)
  assert.equal(carriesUnlock('/credentials'), true)
  assert.equal(carriesUnlock('/credentials/12'), true)
  assert.equal(carriesUnlock('/credentials/12/regenerate'), true)
  assert.equal(carriesUnlock('/oauth/requests/abc/decision'), true)
  assert.equal(carriesUnlock('/oauth/policy'), true)
  // #239：接入窗口的开 / 续 / 关与删除一条登记都要解锁；漏掉前缀会让页面反复弹出密码框
  assert.equal(carriesUnlock('/oauth/window'), true)
  assert.equal(carriesUnlock('/oauth/clients'), true)
  assert.equal(carriesUnlock('/oauth/clients/ab12cd34'), true)
  assert.equal(carriesUnlock('/oauth/grants'), false)
  assert.equal(carriesUnlock('/sessions'), false)
  assert.equal(carriesUnlock('/settings'), false)
  assert.equal(carriesUnlock(undefined), false)
})

test('分支的两个放宽入口按「方法 + 精确路径」带解锁令牌，/workflows/ 下其它请求不带（#236 P3）', () => {
  // 建会话（带 parent_workflow_id 就是建分支）与改分支的开放范围
  assert.equal(carriesUnlock('/workflows', 'post'), true)
  assert.equal(carriesUnlock('/workflows', 'POST'), true, '方法大小写不敏感')
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch', 'put'), true)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch?x=1', 'PUT'), true, '查询串不参与判断')
  // 同一路径换方法不带；没给方法时精确接口不带（只按前缀）
  assert.equal(carriesUnlock('/workflows', 'get'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch', 'get'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch', 'post'), false)
  assert.equal(carriesUnlock('/workflows', undefined), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch'), false)
  // 撤销与读列表只要登录，不带令牌；别的 /workflows/ 请求更不带
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch/params-events', 'get'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch/params-events/3/revert', 'post'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/branch/close', 'post'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/sessions', 'get'), false)
  assert.equal(carriesUnlock('/workflows/wf-web-1/prompts', 'put'), false)
  assert.equal(carriesUnlock('/workflows/a/b/branch', 'put'), false, '路径里只能有一段工作流编号')
  // 原有前缀不受 method 影响
  assert.equal(carriesUnlock('/terminal/hosts', 'get'), true)
  assert.equal(carriesUnlock('/credentials/12', 'delete'), true)
})
