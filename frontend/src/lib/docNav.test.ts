// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { Router } from 'vue-router'
import {
  createOwnerResolver,
  docLocation,
  docRefFromParam,
  legacyDocRedirect,
  legacyDocsLocation,
  openDocWith,
  paramString,
  projectKbLocation,
} from './docNav'

function fakeRouter() {
  const calls: { kind: 'push' | 'replace'; loc: unknown }[] = []
  const router = {
    push: async (loc: unknown) => void calls.push({ kind: 'push', loc }),
    replace: async (loc: unknown) => void calls.push({ kind: 'replace', loc }),
  } as unknown as Router
  return { router, calls }
}

describe('路由参数', () => {
  it('paramString：字符串、数组取第一个、缺省为空串', () => {
    assert.equal(paramString('wf'), 'wf')
    assert.equal(paramString(['a', 'b']), 'a')
    assert.equal(paramString(undefined), '')
    assert.equal(paramString(null), '')
  })

  it('docRefFromParam：多段拼回路径，%2F 的单段也能还原，解不开的原样用', () => {
    assert.equal(docRefFromParam(['design', 'a.md']), 'design/a.md')
    assert.equal(docRefFromParam(['design%2Fa.md']), 'design/a.md')
    assert.equal(docRefFromParam('a%20b.md'), 'a b.md')
    assert.equal(docRefFromParam('100%.md'), '100%.md')
    assert.equal(docRefFromParam([]), '')
    assert.equal(docRefFromParam(undefined), '')
  })
})

describe('docLocation', () => {
  it('有所属项目 → 项目内文档页；锚点带不带 # 都规整成 #锚点', () => {
    assert.deepEqual(docLocation({ workflowId: 'wf', path: 'design/a.md', anchor: 'x' }, 7), {
      name: 'project-doc',
      params: { projectId: '7', workflowId: 'wf', docRef: 'design/a.md' },
      hash: '#x',
    })
    assert.equal((docLocation({ workflowId: 'wf', path: 'a.md', anchor: '#y' }, 7) as { hash?: string }).hash, '#y')
    assert.equal((docLocation({ workflowId: 'wf', path: 'a.md' }, 7) as { hash?: string }).hash, undefined)
  })

  it('没有项目（0 / 缺省）→ 老文档页', () => {
    assert.deepEqual(docLocation({ workflowId: 'wf', path: 'a.md' }, 0), { name: 'docs', params: { workflowId: 'wf', docRef: 'a.md' }, hash: undefined })
    assert.equal((docLocation({ workflowId: 'wf', path: 'a.md' }) as { name: string }).name, 'docs')
  })

  it('目标自带 projectId 时用它；显式给 0 表示没有项目', () => {
    assert.equal((docLocation({ workflowId: 'wf', path: 'a.md', projectId: 3 }) as { name: string }).name, 'project-doc')
    assert.equal((docLocation({ workflowId: 'wf', path: 'a.md', projectId: 3 }, 0) as { name: string }).name, 'docs')
  })

  it('projectKbLocation 指向项目页默认页签', () => {
    assert.deepEqual(projectKbLocation(5), { name: 'projects', params: { projectId: '5' } })
  })

  it('legacyDocsLocation 是老文档页本身（不带文档）：没有项目的工作流留的入口', () => {
    assert.deepEqual(legacyDocsLocation('wf'), { name: 'docs', params: { workflowId: 'wf' } })
  })
})

describe('legacyDocRedirect：老地址查到所属项目后的去向', () => {
  it('带文档 → 项目内文档页，参数形状、query、hash 原样带过去', () => {
    const q = { a: '1' }
    assert.deepEqual(legacyDocRedirect({ params: { workflowId: 'wf', docRef: ['design', 'a.md'] }, query: q, hash: '#h' }, 4), {
      name: 'project-doc',
      params: { projectId: '4', workflowId: 'wf', docRef: ['design', 'a.md'] },
      query: q,
      hash: '#h',
    })
    assert.equal((legacyDocRedirect({ params: { workflowId: 'wf', docRef: 'a.md' }, hash: '' }, 4) as { hash?: string }).hash, undefined)
  })

  it('没带文档 → 项目的知识库', () => {
    assert.deepEqual(legacyDocRedirect({ params: { workflowId: 'wf', docRef: [] } }, 4), projectKbLocation(4))
    assert.deepEqual(legacyDocRedirect({ params: { workflowId: 'wf' } }, 4), projectKbLocation(4))
  })
})

describe('createOwnerResolver：工作流 → 所属项目', () => {
  it('查到就返回编号，并缓存（同一个工作流连点不重复请求）', async () => {
    let n = 0
    const r = createOwnerResolver(async () => (n++, 9))
    assert.equal(await r.resolve('wf'), 9)
    assert.equal(await r.resolve('wf'), 9)
    assert.equal(n, 1)
    assert.equal(r.peek('wf'), 9)
  })

  it('没有归属（0）也缓存；非法值按 0 处理', async () => {
    const seen: string[] = []
    const r = createOwnerResolver(async wf => {
      seen.push(wf)
      return wf === 'neg' ? -3 : wf === 'nan' ? Number.NaN : 0
    })
    assert.equal(await r.resolve('a'), 0)
    assert.equal(await r.resolve('a'), 0)
    assert.equal(await r.resolve('neg'), 0)
    assert.equal(await r.resolve('nan'), 0)
    assert.deepEqual(seen, ['a', 'neg', 'nan'])
  })

  it('接口失败 → 0（退回老文档页），失败不进缓存，下次还会再试', async () => {
    let n = 0
    const r = createOwnerResolver(async () => {
      if (n++ === 0) throw new Error('net')
      return 2
    })
    assert.equal(await r.resolve('wf'), 0)
    assert.equal(r.peek('wf'), undefined)
    assert.equal(await r.resolve('wf'), 2)
  })

  it('invalidate 之后重新查；缓存过期也重新查', async () => {
    let n = 0
    const r = createOwnerResolver(async () => (n++, 1), 30_000)
    await r.resolve('wf')
    r.invalidate()
    assert.equal(r.peek('wf'), undefined)
    await r.resolve('wf')
    assert.equal(n, 2)
    assert.equal(r.peek('wf', Date.now() + 31_000), undefined)
    assert.equal(r.peek('wf', Date.now() + 1_000), 1)
  })

  it('并发的两次查询只发一次请求；空工作流不发请求', async () => {
    let n = 0
    let release: (v: number) => void = () => {}
    const r = createOwnerResolver(() => {
      n++
      return new Promise<number>(res => (release = res))
    })
    const p1 = r.resolve('wf')
    const p2 = r.resolve('wf')
    release(5)
    assert.deepEqual(await Promise.all([p1, p2]), [5, 5])
    assert.equal(n, 1)
    assert.equal(await r.resolve('  '), 0)
    assert.equal(n, 1)
  })

  it('within：接口迟迟不回，超时后按没有归属处理，导航不被卡住', async () => {
    const r = createOwnerResolver(() => new Promise<number>(() => {}))
    const t0 = Date.now()
    assert.equal(await r.within('wf', 30), 0)
    assert.ok(Date.now() - t0 < 1000)
  })

  it('within：正常返回时不等超时', async () => {
    const r = createOwnerResolver(async () => 4)
    const t0 = Date.now()
    assert.equal(await r.within('wf', 5000), 4)
    assert.ok(Date.now() - t0 < 1000)
  })
})

describe('openDocWith', () => {
  it('目标自带项目：不查接口，直接进项目内文档页', async () => {
    const { router, calls } = fakeRouter()
    let asked = 0
    const owners = createOwnerResolver(async () => (asked++, 1))
    await openDocWith(router, owners, { workflowId: 'wf', path: 'a.md', projectId: 6 })
    assert.equal(asked, 0)
    assert.deepEqual(calls, [{ kind: 'push', loc: { name: 'project-doc', params: { projectId: '6', workflowId: 'wf', docRef: 'a.md' }, hash: undefined } }])
  })

  it('不知道项目：按工作流查；查到进项目内，没有就老页', async () => {
    const { router, calls } = fakeRouter()
    const owners = createOwnerResolver(async wf => (wf === 'wf' ? 8 : 0))
    await openDocWith(router, owners, { workflowId: 'wf', path: 'a.md', anchor: 'sec' })
    await openDocWith(router, owners, { workflowId: 'other', path: 'b.md' })
    assert.deepEqual(calls[0].loc, { name: 'project-doc', params: { projectId: '8', workflowId: 'wf', docRef: 'a.md' }, hash: '#sec' })
    assert.deepEqual(calls[1].loc, { name: 'docs', params: { workflowId: 'other', docRef: 'b.md' }, hash: undefined })
  })

  it('接口失败：退回老文档页，入口不会因此消失', async () => {
    const { router, calls } = fakeRouter()
    const owners = createOwnerResolver(async () => {
      throw new Error('down')
    })
    await openDocWith(router, owners, { workflowId: 'wf', path: 'a.md' })
    assert.equal((calls[0].loc as { name: string }).name, 'docs')
  })

  it('replace 选项走 router.replace', async () => {
    const { router, calls } = fakeRouter()
    const owners = createOwnerResolver(async () => 1)
    await openDocWith(router, owners, { workflowId: 'wf', path: 'a.md', projectId: 1 }, { replace: true })
    assert.deepEqual(calls.map(c => c.kind), ['replace'])
  })
})
