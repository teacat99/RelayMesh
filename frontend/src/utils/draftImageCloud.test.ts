// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  createDraftImageCloud,
  imageKeysOf,
  serializeDraftForServer,
  unreferencedKeys,
  type CloudImageApi,
  type CloudImageState,
  type DraftImageCloud,
} from './draftImageCloud'
import { hydrateDraftImages } from './draftImages'
import {
  computeImageKey,
  createDraftPersister,
  hasUnloadedImages,
  type DraftImageLike,
  type MultiDraftLike,
} from './draftSync'

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

function img(name: string, data = name.repeat(8)): DraftImageLike {
  return { name, format: 'png', data_type: 'base64', hash: 'h-' + name, width: 10, height: 20, data, key: computeImageKey(data) }
}

function draft(images: DraftImageLike[], text = ''): MultiDraftLike {
  return { activeIndex: 0, drafts: [{ id: '1', text, presets: [], images, updated_at: 1 }] }
}

function httpError(status: number): Error {
  return Object.assign(new Error(`HTTP ${status}`), { response: { status } })
}

/** 受控的假服务器：记录每次调用，可注入失败，也可以让 put / remove 挂起直到放行。 */
class FakeApi implements CloudImageApi {
  store = new Map<string, DraftImageLike>() // `${wf}\u0001${key}` → 图片
  log: string[] = []
  putFailures: unknown[] = []
  listFailure: unknown = null
  getFailure: unknown = null
  putGate: Promise<void> | null = null
  removeGate: Promise<void> | null = null
  listGate: Promise<void> | null = null

  private id = (wf: string, key: string) => `${wf}\u0001${key}`

  async list(wf: string) {
    this.log.push(`list ${wf}`)
    const snapshot = Array.from(this.store.keys())
      .filter(k => k.startsWith(wf + '\u0001'))
      .map(k => k.split('\u0001')[1])
    if (this.listGate) await this.listGate
    if (this.listFailure) throw this.listFailure
    return snapshot
  }

  async put(wf: string, key: string, image: Parameters<CloudImageApi['put']>[2]) {
    this.log.push(`put ${wf} ${key}`)
    if (this.putGate) await this.putGate
    const failure = this.putFailures.shift()
    if (failure) throw failure
    this.store.set(this.id(wf, key), { ...image, key })
  }

  async get(wf: string, key: string) {
    this.log.push(`get ${wf} ${key}`)
    if (this.getFailure) throw this.getFailure
    return this.store.get(this.id(wf, key)) ?? null
  }

  async remove(wf: string, key: string) {
    this.log.push(`remove ${wf} ${key}`)
    if (this.removeGate) await this.removeGate
    this.store.delete(this.id(wf, key))
  }

  count(prefix: string) {
    return this.log.filter(line => line.startsWith(prefix)).length
  }
}

function gate() {
  let open!: () => void
  const promise = new Promise<void>(resolve => {
    open = resolve
  })
  return { promise, open }
}

function setup(api = new FakeApi(), extra: Partial<Parameters<typeof createDraftImageCloud>[0]> = {}) {
  const changes: string[] = []
  const errors: string[] = []
  const sleeps: number[] = []
  const cloud = createDraftImageCloud({
    api,
    retryDelaysMs: [10, 20, 30],
    sleep: async ms => {
      sleeps.push(ms)
    },
    onChange: (wf, key, state) => changes.push(`${wf}/${key}=${state}`),
    onError: (_wf, _key, _error, info) => errors.push(`${info.op}${info.final ? '!' : ''}`),
    ...extra,
  })
  return { api, cloud, changes, errors, sleeps }
}

/** 让所有已排队的微任务跑完（挂起的 put / remove 不会因此结束）。 */
async function settle() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

// ---------------------------------------------------------------------------
// 上传
// ---------------------------------------------------------------------------

describe('createDraftImageCloud · 上传', () => {
  it('上传成功：uploading → uploaded，服务器上有这张图，并带上元数据', async () => {
    const { api, cloud, changes } = setup()
    const a = img('a')
    const pending = cloud.ensure('wf', a)
    assert.equal(cloud.state('wf', a.key!), 'uploading', 'ensure 返回前就已经登记为上传中，草稿落盘闸门据此等待')
    assert.equal(await pending, 'uploaded')
    assert.equal(cloud.isUploaded('wf', a.key!), true)
    assert.deepEqual(changes, [`wf/${a.key}=uploading`, `wf/${a.key}=uploaded`])
    const stored = api.store.get(`wf\u0001${a.key}`)
    assert.equal(stored?.data, a.data)
    assert.equal(stored?.hash, 'h-a')
    assert.equal(stored?.width, 10)
  })

  it('INV-C2：并发 ensure 合并成一次上传；已上传的不再重复传', async () => {
    const { api, cloud } = setup()
    const a = img('a')
    const results = await Promise.all([cloud.ensure('wf', a), cloud.ensure('wf', a), cloud.ensure('wf', { ...a })])
    assert.deepEqual(results, ['uploaded', 'uploaded', 'uploaded'])
    assert.equal(api.count('put'), 1)
    await cloud.ensure('wf', a)
    assert.equal(api.count('put'), 1)
  })

  it('INV-C1：不同工作流里的同一张图各自上传、各自登记', async () => {
    const { api, cloud } = setup()
    const a = img('a')
    await cloud.ensure('wf-1', a)
    assert.equal(cloud.isUploaded('wf-2', a.key!), false)
    await cloud.ensure('wf-2', a)
    assert.equal(api.count('put'), 2)
    await cloud.release('wf-1', [a.key!])
    assert.equal(cloud.isUploaded('wf-1', a.key!), false)
    assert.equal(cloud.isUploaded('wf-2', a.key!), true, '释放一个工作流不影响另一个')
  })

  it('没有内容键 / 没有正文 / 已确认丢失的图片不上传', async () => {
    const { api, cloud } = setup()
    await cloud.ensure('wf', { name: 'nokey', data: 'AAAA' })
    await cloud.ensure('wf', { name: 'nodata', key: 'k-1', data: '' })
    await cloud.ensure('wf', { ...img('lost'), lost: true })
    await cloud.ensure('wf', { name: 'link', key: 'k-2', url: '/x' })
    assert.equal(api.count('put'), 0)
  })

  it('瞬时失败自动重试：失败两次后成功，按配置的间隔等待，中途的失败不算最终失败', async () => {
    const api = new FakeApi()
    api.putFailures = [new Error('network'), new Error('network')]
    const { cloud, sleeps, errors } = setup(api)
    const a = img('a')
    assert.equal(await cloud.ensure('wf', a), 'uploaded')
    assert.equal(api.count('put'), 3)
    assert.deepEqual(sleeps, [10, 20])
    assert.deepEqual(errors, ['put', 'put'])
  })

  it('INV-C3：重试用尽 → failed（调用方继续把正文内联在草稿里）；之后再次 ensure 会重新尝试并可以成功', async () => {
    const api = new FakeApi()
    api.putFailures = [new Error('1'), new Error('2'), new Error('3'), new Error('4')]
    const { cloud, errors } = setup(api)
    const a = img('a')
    assert.equal(await cloud.ensure('wf', a), 'failed')
    assert.equal(cloud.state('wf', a.key!), 'failed')
    assert.equal(cloud.isUploaded('wf', a.key!), false)
    assert.equal(api.count('put'), 4, '首次 + 3 次重试')
    assert.deepEqual(errors, ['put', 'put', 'put', 'put!'])
    assert.equal(await cloud.ensure('wf', a), 'uploaded')
    assert.equal(cloud.isUploaded('wf', a.key!), true)
  })

  it('配额 / 格式类错误（HTTP 400 / 413）不重试，直接 failed', async () => {
    for (const status of [400, 401, 403, 413]) {
      const api = new FakeApi()
      api.putFailures = [httpError(status)]
      const { cloud, sleeps, errors } = setup(api)
      assert.equal(await cloud.ensure('wf', img('a')), 'failed', `HTTP ${status}`)
      assert.equal(api.count('put'), 1)
      assert.deepEqual(sleeps, [])
      assert.deepEqual(errors, ['put!'])
    }
    // 服务端 5xx 属于瞬时错误，要重试
    const api = new FakeApi()
    api.putFailures = [httpError(503)]
    const { cloud } = setup(api)
    assert.equal(await cloud.ensure('wf', img('a')), 'uploaded')
    assert.equal(api.count('put'), 2)
  })

  it('hasUploading：只有「正在上传」才算，已上传 / 失败不算', async () => {
    const api = new FakeApi()
    const g = gate()
    api.putGate = g.promise
    const { cloud } = setup(api)
    const a = img('a')
    const state = draft([a])
    const task = cloud.ensure('wf', a)
    assert.equal(cloud.hasUploading('wf', state), true)
    assert.equal(cloud.hasUploading('other', state), false)
    g.open()
    await task
    assert.equal(cloud.hasUploading('wf', state), false)
  })

  it('ensureAll：整份草稿并行上传，同一张图出现在多个卡槽只传一次', async () => {
    const { api, cloud } = setup()
    const a = img('a')
    const b = img('b')
    const state: MultiDraftLike = {
      activeIndex: 0,
      drafts: [
        { id: '1', text: '', presets: [], images: [a, b], updated_at: 1 },
        { id: '2', text: '', presets: [], images: [{ ...a }], updated_at: 1 },
      ],
    }
    await cloud.ensureAll('wf', state)
    assert.equal(api.count('put'), 2)
    assert.equal(cloud.isUploaded('wf', a.key!), true)
    assert.equal(cloud.isUploaded('wf', b.key!), true)
  })
})

// ---------------------------------------------------------------------------
// 释放
// ---------------------------------------------------------------------------

describe('createDraftImageCloud · 释放', () => {
  it('已上传的图片释放后：服务器上删除、登记清空', async () => {
    const { api, cloud } = setup()
    const a = img('a')
    await cloud.ensure('wf', a)
    await cloud.release('wf', [a.key!, a.key!])
    assert.equal(api.store.size, 0)
    assert.equal(cloud.state('wf', a.key!), null)
    assert.equal(api.count('remove'), 1, '重复的键只删一次')
  })

  it('INV-C4：上传途中被释放 → 立刻不再算「上传中」，上传结束后补一次删除，服务器上不留孤儿', async () => {
    const api = new FakeApi()
    const g = gate()
    api.putGate = g.promise
    const { cloud, changes } = setup(api)
    const a = img('a')
    const upload = cloud.ensure('wf', a)
    await settle()
    await cloud.release('wf', [a.key!])
    assert.equal(cloud.state('wf', a.key!), null)
    assert.equal(cloud.hasUploading('wf', draft([a])), false)
    assert.equal(api.count('remove'), 0, '上传还没结束，由上传任务自己收尾')
    g.open()
    assert.equal(await upload, null)
    assert.equal(api.store.size, 0, '补删之后服务器上没有这张图')
    assert.equal(api.count('remove'), 1)
    assert.equal(cloud.state('wf', a.key!), null)
    assert.deepEqual(changes, [`wf/${a.key}=uploading`, `wf/${a.key}=null`])
  })

  it('INV-C4：同一张图在被释放后马上又加回来（撤销删除）：等上一轮收尾再重新上传，最终服务器上有这张图', async () => {
    const api = new FakeApi()
    const g = gate()
    api.putGate = g.promise
    const { cloud } = setup(api)
    const a = img('a')
    const first = cloud.ensure('wf', a)
    await settle()
    await cloud.release('wf', [a.key!])
    api.putGate = null
    const second = cloud.ensure('wf', a)
    g.open()
    assert.equal(await first, null)
    assert.equal(await second, 'uploaded')
    assert.equal(api.store.has(`wf\u0001${a.key}`), true, '撤销后的图片必须还在服务器上')
    assert.equal(cloud.isUploaded('wf', a.key!), true)
  })

  it('已上传的图片释放后立刻加回来：先等删除完成再上传，不会被那次删除抹掉', async () => {
    const api = new FakeApi()
    const { cloud } = setup(api)
    const a = img('a')
    await cloud.ensure('wf', a)
    const g = gate()
    api.removeGate = g.promise
    const released = cloud.release('wf', [a.key!])
    await settle()
    const again = cloud.ensure('wf', a)
    await settle()
    assert.equal(api.count('put'), 1, '删除还没完成，不能抢先上传')
    api.removeGate = null
    g.open()
    await released
    assert.equal(await again, 'uploaded')
    assert.equal(api.store.has(`wf\u0001${a.key}`), true)
    assert.deepEqual(api.log.filter(l => !l.startsWith('list')).map(l => l.split(' ')[0]), ['put', 'remove', 'put'])
  })

  it('重试等待期间被释放：不再继续重试，登记清空', async () => {
    const api = new FakeApi()
    api.putFailures = [new Error('1'), new Error('2'), new Error('3'), new Error('4')]
    const g = gate()
    let sleepCalls = 0
    const { cloud } = setup(api, {
      sleep: async () => {
        sleepCalls++
        await g.promise
      },
    })
    const a = img('a')
    const upload = cloud.ensure('wf', a)
    await settle()
    assert.equal(sleepCalls, 1)
    await cloud.release('wf', [a.key!])
    g.open()
    assert.equal(await upload, null)
    assert.equal(api.count('put'), 1, '被释放后不再重试')
    assert.equal(cloud.state('wf', a.key!), null)
  })

  it('INV-C5：删除失败不抛错，只上报；登记仍然清空', async () => {
    const api = new FakeApi()
    api.remove = async () => {
      throw new Error('server down')
    }
    const { cloud, errors } = setup(api)
    const a = img('a')
    await cloud.ensure('wf', a)
    await assert.doesNotReject(cloud.release('wf', [a.key!]))
    assert.equal(cloud.state('wf', a.key!), null)
    assert.deepEqual(errors, ['remove!'])
  })

  it('consumed：只清掉本地登记，不发删除请求（服务端提交时已经消耗了）', async () => {
    const { api, cloud } = setup()
    const a = img('a')
    await cloud.ensure('wf', a)
    cloud.consumed('wf', [a.key!])
    assert.equal(cloud.state('wf', a.key!), null)
    assert.equal(api.count('remove'), 0)
  })
})

// ---------------------------------------------------------------------------
// 载入时对账
// ---------------------------------------------------------------------------

describe('createDraftImageCloud · loadRemoteKeys', () => {
  it('把服务器上已有的图片登记为已上传（换设备 / 刷新后不重复上传）', async () => {
    const api = new FakeApi()
    const a = img('a')
    const b = img('b')
    api.store.set(`wf\u0001${a.key}`, a)
    api.store.set(`other\u0001${b.key}`, b)
    const { cloud, changes } = setup(api)
    assert.equal(await cloud.loadRemoteKeys('wf'), true)
    assert.equal(cloud.isUploaded('wf', a.key!), true)
    assert.equal(cloud.isUploaded('wf', b.key!), false)
    assert.deepEqual(changes, [`wf/${a.key}=uploaded`])
    await cloud.ensure('wf', a)
    assert.equal(api.count('put'), 0, '服务器上已有就不再上传')
  })

  it('读取失败：返回 false，登记不变（既不算有也不算没有）', async () => {
    const api = new FakeApi()
    api.listFailure = new Error('network')
    const { cloud, errors } = setup(api)
    assert.equal(await cloud.loadRemoteKeys('wf'), false)
    assert.deepEqual(errors, ['list!'])
  })

  it('上传曾经失败、但服务器上其实已经有了：对账后改为已上传', async () => {
    const api = new FakeApi()
    api.putFailures = [httpError(400)]
    const { cloud } = setup(api)
    const a = img('a')
    assert.equal(await cloud.ensure('wf', a), 'failed')
    api.store.set(`wf\u0001${a.key}`, a)
    await cloud.loadRemoteKeys('wf')
    assert.equal(cloud.isUploaded('wf', a.key!), true)
  })

  it('INV-C6：list 在途期间发生过释放的键，不按这次（已过时的）结果重新登记', async () => {
    const api = new FakeApi()
    const a = img('a')
    api.store.set(`wf\u0001${a.key}`, a)
    const g = gate()
    api.listGate = g.promise
    const { cloud } = setup(api)
    const loading = cloud.loadRemoteKeys('wf')
    await settle()
    await cloud.release('wf', [a.key!])
    g.open()
    await loading
    assert.equal(cloud.isUploaded('wf', a.key!), false, '用户刚删掉的图片不能被过时的 list 复活成「已上传」')
  })

  it('markUploaded：刚从云端取回的图片登记为已上传；正在上传的键不改，释放后可以重新传', async () => {
    const api = new FakeApi()
    const { cloud } = setup(api)
    const a = img('a')
    cloud.markUploaded('wf', a.key!)
    assert.equal(cloud.isUploaded('wf', a.key!), true)
    await cloud.ensure('wf', a)
    assert.equal(api.count('put'), 0, '已登记为已上传就不再传')

    const g = gate()
    api.putGate = g.promise
    const b = img('b')
    const upload = cloud.ensure('wf', b)
    cloud.markUploaded('wf', b.key!)
    assert.equal(cloud.state('wf', b.key!), 'uploading', '正在上传的不被覆盖')
    g.open()
    await upload
    assert.equal(cloud.isUploaded('wf', b.key!), true)
  })

  it('正在上传 / 正在删除的键不被 list 的结果覆盖', async () => {
    const api = new FakeApi()
    const g = gate()
    api.putGate = g.promise
    const { cloud } = setup(api)
    const a = img('a')
    api.store.set(`wf\u0001${a.key}`, a)
    const upload = cloud.ensure('wf', a)
    await settle()
    await cloud.loadRemoteKeys('wf')
    assert.equal(cloud.state('wf', a.key!), 'uploading')
    g.open()
    await upload
  })
})

// ---------------------------------------------------------------------------
// 草稿 JSON 与引用计算
// ---------------------------------------------------------------------------

describe('serializeDraftForServer', () => {
  it('已在云端的图片只带引用（没有正文），其余图片继续内联正文；内存标记不上传', () => {
    const synced = { ...img('s'), synced: true }
    const plain = img('p')
    const lost = { ...img('l'), lost: true, data: '' }
    const state = draft([synced, plain, lost], '文字')
    const parsed = JSON.parse(serializeDraftForServer(state))
    const [s, p, l] = parsed.drafts[0].images
    assert.equal(s.key, synced.key)
    assert.equal('data' in s, false, '已上传的图片不带正文')
    assert.equal(s.name, 's')
    assert.equal(s.hash, 'h-s')
    assert.equal(p.data, plain.data, '没有上传成功的图片仍然内联正文')
    assert.equal('lost' in l, false)
    assert.equal('synced' in s, false)
    assert.equal(parsed.drafts[0].text, '文字')
    assert.equal(parsed.activeIndex, 0)
    // 序列化不能改动内存里的对象
    assert.equal(synced.data, 's'.repeat(8))
    assert.equal(lost.lost, true)
  })

  it('标记为已上传但没有内容键的图片无法引用，必须内联', () => {
    const odd: DraftImageLike = { name: 'odd', data: 'ZZZZ', synced: true }
    const parsed = JSON.parse(serializeDraftForServer(draft([odd])))
    assert.equal(parsed.drafts[0].images[0].data, 'ZZZZ')
  })

  it('保留卡槽与草稿层面的其它字段（兼容服务端与旧版本读取）', () => {
    const state: MultiDraftLike = {
      activeIndex: 1,
      drafts: [
        { id: 'a', text: 't1', presets: ['ok'], images: [], updated_at: 11 },
        { id: 'b', text: 't2', presets: [], images: [{ ...img('x'), synced: true }], updated_at: 22 },
      ],
    }
    const parsed = JSON.parse(serializeDraftForServer(state))
    assert.equal(parsed.activeIndex, 1)
    assert.deepEqual(
      parsed.drafts.map((d: { id: string; updated_at: number; presets: string[] }) => [d.id, d.updated_at, d.presets]),
      [['a', 11, ['ok']], ['b', 22, []]],
    )
  })
})

describe('unreferencedKeys / imageKeysOf', () => {
  it('只返回草稿里确实不再引用的键；同一张图还在别的卡槽里就保留', () => {
    const a = img('a')
    const b = img('b')
    const after: MultiDraftLike = {
      activeIndex: 0,
      drafts: [
        { id: '1', text: '', presets: [], images: [{ ...a }], updated_at: 1 },
        { id: '2', text: '', presets: [], images: [], updated_at: 1 },
      ],
    }
    assert.deepEqual(unreferencedKeys([a.key!, b.key!], after), [b.key])
    assert.deepEqual(unreferencedKeys([b.key!, b.key!, '', b.key!], after), [b.key], '去重并忽略空键')
    assert.deepEqual(unreferencedKeys([], after), [])
  })

  it('imageKeysOf：去重、保序、忽略没有键的图片', () => {
    const a = img('a')
    const b = img('b')
    assert.deepEqual(imageKeysOf([a, { name: 'nokey', data: 'x' }, b, { ...a }]), [a.key, b.key])
    assert.deepEqual(imageKeysOf(undefined), [])
  })
})

// ---------------------------------------------------------------------------
// 载入链：云端这一步
// ---------------------------------------------------------------------------

function cloudBackedDeps(api: FakeApi, extra: Partial<Parameters<typeof hydrateDraftImages>[1]> = {}) {
  return {
    workflowId: 'wf',
    store: null,
    fetchRemote: async () => null as MultiDraftLike | null,
    fetchCloud: (key: string) => api.get('wf', key),
    ...extra,
  }
}

function shellOf(image: DraftImageLike): DraftImageLike {
  return { name: image.name, format: image.format, hash: image.hash, width: image.width, height: image.height, key: image.key, data: '' }
}

describe('hydrateDraftImages · 云端草稿图片', () => {
  it('本地没有、云端有（换设备 / 清了浏览器数据）：从云端取回，标记为已上传，不再请求服务端草稿', async () => {
    const api = new FakeApi()
    const a = img('a')
    api.store.set(`wf\u0001${a.key}`, a)
    const local = draft([shellOf(a)])
    let remoteCalls = 0
    const recovered: DraftImageLike[] = []
    const result = await hydrateDraftImages(local, {
      ...cloudBackedDeps(api),
      fetchRemote: async () => (remoteCalls++, null),
      onRecovered: i => recovered.push(i),
    })
    assert.deepEqual(result, { hydrated: 1, lost: 0, unavailable: 0, stale: false })
    assert.equal(local.drafts[0].images[0].data, a.data)
    assert.equal(local.drafts[0].images[0].synced, true)
    assert.equal(remoteCalls, 0)
    assert.equal(recovered.length, 1, '回写本地仓库，下次直接命中')
    assert.equal(hasUnloadedImages(local), false)
  })

  it('同一张图出现多次只取一次，全部补回', async () => {
    const api = new FakeApi()
    const a = img('a')
    api.store.set(`wf\u0001${a.key}`, a)
    const local: MultiDraftLike = {
      activeIndex: 0,
      drafts: [
        { id: '1', text: '', presets: [], images: [shellOf(a)], updated_at: 1 },
        { id: '2', text: '', presets: [], images: [shellOf(a)], updated_at: 1 },
      ],
    }
    const result = await hydrateDraftImages(local, cloudBackedDeps(api))
    assert.equal(result.hydrated, 2)
    assert.equal(api.count('get'), 1)
  })

  it('云端 404 但旧版本的服务端草稿里内联着正文：用服务端草稿兜底（向后兼容）', async () => {
    const api = new FakeApi()
    const a = img('a')
    const local = draft([shellOf(a)])
    const result = await hydrateDraftImages(local, { ...cloudBackedDeps(api), fetchRemote: async () => draft([a]) })
    assert.equal(result.hydrated, 1)
    assert.equal(local.drafts[0].images[0].data, a.data)
    assert.equal(local.drafts[0].images[0].synced, undefined, '来自旧草稿 JSON，云端并没有这张图')
  })

  it('INV-I3：云端暂时读不到、服务端草稿里也没有 → 不判定丢失，保持待载入', async () => {
    const api = new FakeApi()
    api.getFailure = new Error('network')
    const a = img('a')
    const local = draft([shellOf(a)])
    const result = await hydrateDraftImages(local, cloudBackedDeps(api))
    assert.deepEqual(result, { hydrated: 0, lost: 0, unavailable: 1, stale: false })
    assert.equal(local.drafts[0].images[0].lost, undefined)
    assert.equal(hasUnloadedImages(local), true, '上传闸门继续挡住，不会拿空壳覆盖服务端')
  })

  it('云端暂时读不到，但服务端草稿里有正文：照样补回', async () => {
    const api = new FakeApi()
    api.getFailure = new Error('network')
    const a = img('a')
    const local = draft([shellOf(a)])
    const result = await hydrateDraftImages(local, { ...cloudBackedDeps(api), fetchRemote: async () => draft([a]) })
    assert.equal(result.hydrated, 1)
    assert.equal(result.unavailable, 0)
  })

  it('仓库、云端、服务端草稿都成功查过且都没有：确认丢失', async () => {
    const api = new FakeApi()
    const a = img('a')
    const local = draft([shellOf(a)])
    const result = await hydrateDraftImages(local, cloudBackedDeps(api))
    assert.deepEqual(result, { hydrated: 0, lost: 1, unavailable: 0, stale: false })
    assert.equal(local.drafts[0].images[0].lost, true)
  })

  it('混合：一张云端命中、一张云端读取失败、一张都没有', async () => {
    const api = new FakeApi()
    const a = img('a')
    const b = img('b')
    const c = img('c')
    api.store.set(`wf\u0001${a.key}`, a)
    api.get = async (_wf, key) => {
      if (key === b.key) throw new Error('flaky')
      return api.store.get(`wf\u0001${key}`) ?? null
    }
    const local = draft([shellOf(a), shellOf(b), shellOf(c)])
    const result = await hydrateDraftImages(local, cloudBackedDeps(api))
    assert.deepEqual(result, { hydrated: 1, lost: 1, unavailable: 1, stale: false })
    const [ia, ib, ic] = local.drafts[0].images
    assert.equal(ia.data, a.data)
    assert.equal(ib.lost, undefined)
    assert.equal(ic.lost, true)
  })

  it('没有内容键的旧空壳不会请求云端（按元数据走服务端草稿）', async () => {
    const api = new FakeApi()
    const legacy: DraftImageLike = { name: 'old.png', format: 'png', hash: 'hh', width: 1, height: 2, data: '' }
    const withData: DraftImageLike = { ...legacy, data: 'LEGACYDATA' }
    const local = draft([legacy])
    const result = await hydrateDraftImages(local, { ...cloudBackedDeps(api), fetchRemote: async () => draft([withData]) })
    assert.equal(result.hydrated, 1)
    assert.equal(api.count('get'), 0)
  })

  it('等待期间调用方已切走：放弃，不应用结果', async () => {
    const api = new FakeApi()
    const a = img('a')
    api.store.set(`wf\u0001${a.key}`, a)
    const local = draft([shellOf(a)])
    let stale = false
    const origGet = api.get.bind(api)
    api.get = async (wf, key) => {
      stale = true
      return origGet(wf, key)
    }
    const result = await hydrateDraftImages(local, { ...cloudBackedDeps(api), isStale: () => stale })
    assert.equal(result.stale, true)
    assert.equal(local.drafts[0].images[0].data, '', '切走之后不能把图片写进别的工作流的状态')
  })
})

// ---------------------------------------------------------------------------
// 与落盘引擎配合：草稿 JSON 等图片上传完再带引用上传
// ---------------------------------------------------------------------------

describe('草稿落盘引擎 + 云端图片', () => {
  function persisterFor(cloud: DraftImageCloud, uploads: string[]) {
    return createDraftPersister({
      writeLocal: () => {},
      upload: async (_wf, _active, json) => {
        uploads.push(json)
        return { draft: { updated_at: '2026-10-03T00:00:00Z' } }
      },
      serializeRemote: serializeDraftForServer,
      canUpload: (state, wf) => !hasUnloadedImages(state) && !cloud.hasUploading(wf, state),
      setTimer: () => 0,
      clearTimer: () => {},
    })
  }

  it('图片还在上传时草稿 JSON 不上传；上传完成并标记 synced 后，上传的 JSON 只带引用', async () => {
    const api = new FakeApi()
    const g = gate()
    api.putGate = g.promise
    const { cloud } = setup(api)
    const uploads: string[] = []
    const persister = persisterFor(cloud, uploads)
    const a = img('a')
    const state = draft([a], '带图的草稿')

    const upload = cloud.ensure('wf', a)
    persister.touch('wf', () => state)
    persister.flush('wf')
    assert.equal(uploads.length, 0, '图片还没传完，闸门挡住草稿 JSON')
    assert.equal(persister.hasPending('wf'), true)

    g.open()
    await upload
    a.synced = cloud.isUploaded('wf', a.key!)
    persister.flush('wf')
    await settle()
    assert.equal(uploads.length, 1)
    const parsed = JSON.parse(uploads[0])
    assert.equal(parsed.drafts[0].images[0].key, a.key)
    assert.equal('data' in parsed.drafts[0].images[0], false, '草稿 JSON 里不再内联几 MB 的正文')
    assert.equal(parsed.drafts[0].text, '带图的草稿')
  })

  it('图片上传失败：草稿 JSON 照常上传并继续内联正文（降级，不丢图）', async () => {
    const api = new FakeApi()
    api.putFailures = [httpError(413)]
    const { cloud } = setup(api)
    const uploads: string[] = []
    const persister = persisterFor(cloud, uploads)
    const a = img('a')
    const state = draft([a])
    assert.equal(await cloud.ensure('wf', a), 'failed')
    a.synced = cloud.isUploaded('wf', a.key!)
    persister.touch('wf', () => state)
    persister.flush('wf')
    await settle()
    assert.equal(uploads.length, 1)
    assert.equal(JSON.parse(uploads[0]).drafts[0].images[0].data, a.data)
  })
})
