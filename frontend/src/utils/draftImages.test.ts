// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  collectUnloadedImages,
  createDraftImageSync,
  createMemoryDraftImageStore,
  hydrateDraftImages,
  indexRemoteImages,
  isStashableImage,
  referencedImageKeys,
  type DraftImageStore,
} from './draftImages'
import {
  MAX_INLINE_IMAGE_CHARS,
  computeImageKey,
  draftSignature,
  ensureImageKeys,
  hasUnloadedImages,
  sanitizeDraftsForStorage,
  type DraftImageLike,
  type MultiDraftLike,
} from './draftSync'

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

/** 生成指定长度的假 Base64 正文，seed 不同内容就不同。 */
function body(length: number, seed: string): string {
  return (seed + '#').repeat(Math.ceil(length / (seed.length + 1))).slice(0, length)
}

const BIG = MAX_INLINE_IMAGE_CHARS + 10

function bigImage(name: string, seed = name): DraftImageLike {
  const data = body(BIG, seed)
  return { name, format: 'png', data_type: 'base64', hash: 'h-' + name, width: 800, height: 600, data, key: computeImageKey(data) }
}

/** 本地副本里的「空壳」：正文被剥掉，只剩内容键与元数据。 */
function shell(img: DraftImageLike): DraftImageLike {
  return { name: img.name, format: img.format, data_type: img.data_type, hash: img.hash, width: img.width, height: img.height, key: img.key, data: '' }
}

function draft(images: DraftImageLike[], text = ''): MultiDraftLike {
  return { activeIndex: 0, drafts: [{ id: '1', text, presets: [], images, updated_at: 1 }] }
}

class FakeNow {
  t = 1_000_000
  now = () => this.t
}

// ---------------------------------------------------------------------------
// draftSync 新增的纯函数
// ---------------------------------------------------------------------------

describe('computeImageKey / ensureImageKeys', () => {
  it('同内容得到同一个键，不同内容（哪怕首部相同）得到不同的键', () => {
    const a = body(300_000, 'A')
    // 与 a 前 64KB 完全相同，只在末尾不同：旧的「前 64KB 哈希」会把它们当成同一张图
    const b = a.slice(0, -5) + 'ZZZZZ'
    assert.equal(computeImageKey(a), computeImageKey(a))
    assert.notEqual(computeImageKey(a), computeImageKey(b))
    assert.notEqual(computeImageKey('x'.repeat(10)), computeImageKey('x'.repeat(11)))
  })

  it('只给缺键且有正文的图片补键，已有键与空壳图片不动', () => {
    const withKey: DraftImageLike = { name: 'a', data: 'AAAA', key: 'keep' }
    const noKey: DraftImageLike = { name: 'b', data: 'BBBB' }
    const emptyShell: DraftImageLike = { name: 'c', data: '' }
    const s = draft([withKey, noKey, emptyShell])
    assert.equal(ensureImageKeys(s), 1)
    assert.equal(withKey.key, 'keep')
    assert.equal(noKey.key, computeImageKey('BBBB'))
    assert.equal(emptyShell.key, undefined)
    assert.equal(ensureImageKeys(s), 0)
  })
})

describe('hasUnloadedImages', () => {
  it('空壳图片算未载入；有正文、有直链、已确认丢失都不算', () => {
    assert.equal(hasUnloadedImages(draft([{ name: 'a', data: '' }])), true)
    assert.equal(hasUnloadedImages(draft([{ name: 'a', data: 'AAAA' }])), false)
    assert.equal(hasUnloadedImages(draft([{ name: 'a', url: '/api/x' }])), false)
    assert.equal(hasUnloadedImages(draft([{ name: 'a', data: '', lost: true }])), false)
    assert.equal(hasUnloadedImages(draft([])), false)
  })
})

describe('sanitizeDraftsForStorage（#8 相关）', () => {
  it('大图剥掉正文但保留内容键与直链；小图正文保留；丢失标记不落盘', () => {
    const big = bigImage('big')
    const small: DraftImageLike = { name: 'small', format: 'png', data: 'AAAA', key: computeImageKey('AAAA') }
    const linked: DraftImageLike = { name: 'linked', url: '/api/v1/sessions/x/images/0' }
    const lost: DraftImageLike = { name: 'lost', data: '', lost: true }
    const out = sanitizeDraftsForStorage(draft([big, small, linked, lost]))
    const [b, s, l, x] = out.drafts[0].images
    assert.equal(b.data, '')
    assert.equal(b.key, big.key)
    assert.equal(s.data, 'AAAA')
    assert.equal(l.url, '/api/v1/sessions/x/images/0')
    assert.equal('lost' in x, false)
  })

  it('瘦身后重新载入的空壳不会被误判成「已载入」', () => {
    const big = bigImage('big')
    const reloaded = JSON.parse(JSON.stringify(sanitizeDraftsForStorage(draft([big])))) as MultiDraftLike
    assert.equal(hasUnloadedImages(reloaded), true)
    assert.equal(collectUnloadedImages(reloaded).length, 1)
  })
})

// ---------------------------------------------------------------------------
// 内存仓库
// ---------------------------------------------------------------------------

describe('createMemoryDraftImageStore', () => {
  it('以 (工作流, 键) 区分记录，批量读取只返回存在的键', async () => {
    const store = createMemoryDraftImageStore()
    await store.put('wf1', 'k1', 'data-1')
    await store.put('wf2', 'k1', 'data-other')
    const got = await store.getMany('wf1', ['k1', 'k2'])
    assert.deepEqual(Array.from(got), [['k1', 'data-1']])
    assert.equal((await store.getMany('wf2', ['k1'])).get('k1'), 'data-other')
  })

  it('prune 只删本工作流里未被引用且过了宽限期的记录', async () => {
    const clock = new FakeNow()
    const store = createMemoryDraftImageStore(clock.now)
    await store.put('wf1', 'old-unref', 'a')
    await store.put('wf1', 'old-ref', 'b')
    await store.put('wf2', 'old-unref', 'c')
    clock.t += 10 * 60_000
    await store.put('wf1', 'fresh-unref', 'd')
    const removed = await store.prune('wf1', new Set(['old-ref']), 5 * 60_000)
    assert.deepEqual(removed, ['old-unref'])
    assert.equal(store.size(), 4 - 1)
    assert.equal((await store.getMany('wf2', ['old-unref'])).size, 1, '别的工作流不受影响')
    assert.equal((await store.getMany('wf1', ['fresh-unref'])).size, 1, '宽限期内的记录保留')
  })

  it('pruneOlderThan 清掉所有工作流里的陈旧记录', async () => {
    const clock = new FakeNow()
    const store = createMemoryDraftImageStore(clock.now)
    await store.put('wf1', 'a', '1')
    clock.t += 40 * 86_400_000
    await store.put('wf2', 'b', '2')
    assert.equal(await store.pruneOlderThan(30 * 86_400_000), 1)
    assert.equal(store.size(), 1)
  })
})

// ---------------------------------------------------------------------------
// 同步器
// ---------------------------------------------------------------------------

describe('createDraftImageSync', () => {
  it('isStashableImage：必须有键、有正文、且大到本地副本放不下', () => {
    assert.equal(isStashableImage(bigImage('a')), true)
    assert.equal(isStashableImage({ ...bigImage('a'), key: undefined }), false)
    assert.equal(isStashableImage({ name: 's', data: 'AAAA', key: 'k' }), false)
    assert.equal(isStashableImage(shell(bigImage('a'))), false)
  })

  it('同一张大图只写一次仓库，不随每次落盘重复写几 MB', async () => {
    const store = createMemoryDraftImageStore()
    let puts = 0
    const counting: DraftImageStore = { ...store, put: (...args) => (puts++, store.put(...args)) }
    const sync = createDraftImageSync(counting)
    const img = bigImage('a')
    await sync.stash('wf', img)
    await sync.afterLocal('wf', draft([img]))
    await sync.afterLocal('wf', draft([img], '又敲了几个字'))
    assert.equal(puts, 1)
  })

  it('force：仓库被外部清掉后，载入链从服务端补回的图片必须强制重写，不能被「已经存过」的记忆挡住', async () => {
    const store = createMemoryDraftImageStore()
    const sync = createDraftImageSync(store)
    const img = bigImage('a')
    await sync.stash('wf', img)
    assert.equal(store.size(), 1)

    // 模拟浏览器 / 其它标签页把仓库清空：同步器的记忆已经过时
    const cleared = createMemoryDraftImageStore()
    const sync2 = createDraftImageSync(cleared)
    await sync2.stash('wf', img)
    await cleared.pruneOlderThan(0)
    assert.equal(cleared.size(), 0)
    await sync2.stash('wf', img)
    assert.equal(cleared.size(), 0, '不带 force 时被记忆挡住（正常的去重行为）')
    await sync2.stash('wf', img, { force: true })
    assert.equal(cleared.size(), 1, 'force 强制重写')
  })

  it('afterLocal 补存还没进仓库的大图（例如从服务端载入的），忽略小图与无键图片', async () => {
    const store = createMemoryDraftImageStore()
    const sync = createDraftImageSync(store)
    const big = bigImage('big')
    await sync.afterLocal('wf', draft([big, { name: 's', data: 'AAAA', key: 'ks' }, { ...bigImage('nokey'), key: undefined }]))
    assert.equal(store.size(), 1)
    assert.equal((await store.getMany('wf', [big.key!])).size, 1)
  })

  it('图片被移出草稿后，超过宽限期才清理；宽限期内保留（撤销删除的安全边际）', async () => {
    const clock = new FakeNow()
    const store = createMemoryDraftImageStore(clock.now)
    const sync = createDraftImageSync(store, { graceMs: 5 * 60_000 })
    const keep = bigImage('keep')
    const drop = bigImage('drop')
    await sync.afterLocal('wf', draft([keep, drop]))
    assert.equal(store.size(), 2)

    // 删除 drop：宽限期内不清理
    await sync.afterLocal('wf', draft([keep]))
    assert.equal(store.size(), 2)

    // 过了宽限期，下一次「引用集合变化」触发清理
    clock.t += 6 * 60_000
    await sync.afterLocal('wf', draft([keep, { name: 'x', data: 'AAAA', key: 'kx' }]))
    assert.equal(store.size(), 1)
    assert.equal((await store.getMany('wf', [keep.key!])).size, 1)
  })

  it('未载入正文的空壳图片仍算引用，它的正文不会被清理', async () => {
    const clock = new FakeNow()
    const store = createMemoryDraftImageStore(clock.now)
    const sync = createDraftImageSync(store, { graceMs: 0 })
    const img = bigImage('a')
    const other = bigImage('other')
    await sync.afterLocal('wf', draft([img, other]))
    clock.t += 1000
    // 引用集合变化（other 被移除）触发清理；img 此刻只剩空壳，但它的正文还躺在仓库里，必须保留
    await sync.afterLocal('wf', draft([shell(img)], '载入期间用户在敲字'))
    assert.equal(store.size(), 1)
    assert.equal((await store.getMany('wf', [img.key!])).size, 1)
    assert.deepEqual(Array.from(referencedImageKeys(draft([shell(img)]))), [img.key])
  })

  it('仓库写入失败：上报错误，且下一次落盘会重试而不是误以为已存', async () => {
    const store = createMemoryDraftImageStore()
    let fail = true
    const errors: unknown[] = []
    const flaky: DraftImageStore = {
      ...store,
      put: async (...args) => {
        if (fail) throw new Error('QuotaExceededError')
        return store.put(...args)
      },
    }
    const sync = createDraftImageSync(flaky, { onError: e => errors.push(e) })
    const img = bigImage('a')
    await sync.afterLocal('wf', draft([img]))
    assert.equal(errors.length, 1)
    assert.equal(store.size(), 0)
    fail = false
    await sync.afterLocal('wf', draft([img], '重试'))
    assert.equal(store.size(), 1)
  })

  it('清理失败只上报，不抛出', async () => {
    const store = createMemoryDraftImageStore()
    const errors: unknown[] = []
    const broken: DraftImageStore = {
      ...store,
      prune: async () => {
        throw new Error('boom')
      },
    }
    const sync = createDraftImageSync(broken, { onError: e => errors.push(e) })
    await sync.afterLocal('wf', draft([bigImage('a')]))
    assert.equal(errors.length, 1)
  })
})

// ---------------------------------------------------------------------------
// 载入链
// ---------------------------------------------------------------------------

describe('indexRemoteImages', () => {
  it('旧草稿没有内容键时现算，且同时建立元数据索引', () => {
    const legacy: DraftImageLike = { name: 'old', format: 'png', hash: 'h', width: 1, height: 2, data: 'AAAA' }
    const idx = indexRemoteImages(draft([legacy]))
    assert.equal(idx.byKey.get(computeImageKey('AAAA')), 'AAAA')
    assert.equal(idx.byMeta.size, 1)
  })

  it('没有正文的图片不进索引；null 得到空索引', () => {
    assert.equal(indexRemoteImages(draft([{ name: 'x', data: '' }])).byKey.size, 0)
    assert.equal(indexRemoteImages(null).byMeta.size, 0)
  })
})

describe('hydrateDraftImages', () => {
  it('没有缺失的图片：既不碰仓库也不请求服务端', async () => {
    let touched = false
    const store = createMemoryDraftImageStore()
    const result = await hydrateDraftImages(draft([{ name: 'a', data: 'AAAA' }, { name: 'b', url: '/x' }]), {
      workflowId: 'wf',
      store: { ...store, getMany: async () => ((touched = true), new Map()) },
      fetchRemote: async () => ((touched = true), null),
    })
    assert.deepEqual(result, { hydrated: 0, lost: 0, unavailable: 0, stale: false })
    assert.equal(touched, false)
  })

  it('本地仓库命中：秒级补回，不请求服务端', async () => {
    const store = createMemoryDraftImageStore()
    const img = bigImage('a')
    await store.put('wf', img.key!, img.data!)
    const local = draft([shell(img)])
    let remoteCalls = 0
    const result = await hydrateDraftImages(local, {
      workflowId: 'wf',
      store,
      fetchRemote: async () => (remoteCalls++, null),
    })
    assert.equal(result.hydrated, 1)
    assert.equal(remoteCalls, 0)
    assert.equal(local.drafts[0].images[0].data, img.data)
    assert.equal(hasUnloadedImages(local), false)
  })

  it('仓库没有、服务端有（跨设备 / 浏览器清了站点数据）：从服务端草稿按内容键补回，并回调 onRecovered', async () => {
    const img = bigImage('a')
    const local = draft([shell(img)], '本地文字更新')
    const remote = draft([img], '服务端旧文字')
    const recovered: DraftImageLike[] = []
    const result = await hydrateDraftImages(local, {
      workflowId: 'wf',
      store: createMemoryDraftImageStore(),
      fetchRemote: async () => remote,
      onRecovered: i => recovered.push(i),
    })
    assert.equal(result.hydrated, 1)
    assert.equal(recovered.length, 1)
    assert.equal(local.drafts[0].images[0].data, img.data)
    assert.equal(local.drafts[0].text, '本地文字更新', '只补图片正文，不覆盖用户在本地写的文字')
  })

  it('没有仓库（浏览器不支持 IndexedDB）时直接走服务端兜底', async () => {
    const img = bigImage('a')
    const local = draft([shell(img)])
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store: null, fetchRemote: async () => draft([img]) })
    assert.equal(result.hydrated, 1)
  })

  it('旧草稿的空壳没有内容键：按名称 + 哈希 + 尺寸认图，补回后顺手补上内容键', async () => {
    const data = body(BIG, 'legacy')
    const legacyShell: DraftImageLike = { name: 'paste-1.png', format: 'png', hash: 'hh', width: 10, height: 20, data: '' }
    const remoteImg: DraftImageLike = { name: 'paste-1.png', format: 'png', hash: 'hh', width: 10, height: 20, data }
    const local = draft([legacyShell])
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store: createMemoryDraftImageStore(), fetchRemote: async () => draft([remoteImg]) })
    assert.equal(result.hydrated, 1)
    assert.equal(legacyShell.data, data)
    assert.equal(legacyShell.key, computeImageKey(data))
  })

  it('同名不同尺寸的旧图不会被张冠李戴', async () => {
    const legacyShell: DraftImageLike = { name: 'a.png', format: 'png', hash: 'hh', width: 10, height: 20, data: '' }
    const other: DraftImageLike = { name: 'a.png', format: 'png', hash: 'hh', width: 99, height: 20, data: 'WRONG' }
    const result = await hydrateDraftImages(draft([legacyShell]), { workflowId: 'wf', store: null, fetchRemote: async () => draft([other]) })
    assert.equal(result.lost, 1)
    assert.equal(legacyShell.data, '')
  })

  it('仓库与服务端都成功查过且都没有：确认丢失（界面提示，且不再算「未载入」）', async () => {
    const img = bigImage('gone')
    const local = draft([shell(img)])
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store: createMemoryDraftImageStore(), fetchRemote: async () => null })
    assert.equal(result.lost, 1)
    assert.equal(local.drafts[0].images[0].lost, true)
    assert.equal(hasUnloadedImages(local), false)
  })

  it('服务端暂时不可用：不判定丢失，保持待载入（上传闸门继续挡住），可重试', async () => {
    const img = bigImage('a')
    const local = draft([shell(img)])
    const result = await hydrateDraftImages(local, {
      workflowId: 'wf',
      store: createMemoryDraftImageStore(),
      fetchRemote: async () => {
        throw new Error('network down')
      },
    })
    assert.deepEqual(result, { hydrated: 0, lost: 0, unavailable: 1, stale: false })
    assert.equal(local.drafts[0].images[0].lost, undefined)
    assert.equal(hasUnloadedImages(local), true)

    // 网络恢复后重试成功
    const retry = await hydrateDraftImages(local, { workflowId: 'wf', store: null, fetchRemote: async () => draft([img]) })
    assert.equal(retry.hydrated, 1)
    assert.equal(hasUnloadedImages(local), false)
  })

  it('仓库读取抛错按「没有」处理，继续走服务端兜底', async () => {
    const img = bigImage('a')
    const local = draft([shell(img)])
    const broken: DraftImageStore = {
      ...createMemoryDraftImageStore(),
      getMany: async () => {
        throw new Error('idb broken')
      },
    }
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store: broken, fetchRemote: async () => draft([img]) })
    assert.equal(result.hydrated, 1)
  })

  it('混合场景：一张仓库命中、一张服务端命中、一张丢失', async () => {
    const a = bigImage('a')
    const b = bigImage('b')
    const c = bigImage('c')
    const store = createMemoryDraftImageStore()
    await store.put('wf', a.key!, a.data!)
    const local = draft([shell(a), shell(b), shell(c)])
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store, fetchRemote: async () => draft([b]) })
    assert.deepEqual(result, { hydrated: 2, lost: 1, unavailable: 0, stale: false })
    const [ia, ib, ic] = local.drafts[0].images
    assert.equal(ia.data, a.data)
    assert.equal(ib.data, b.data)
    assert.equal(ic.lost, true)
  })

  it('调用方在等待期间切走：立即放弃，不再请求服务端', async () => {
    const img = bigImage('a')
    const local = draft([shell(img)])
    let remoteCalls = 0
    let stale = false
    const store = createMemoryDraftImageStore()
    const slow: DraftImageStore = {
      ...store,
      getMany: async (...args) => {
        stale = true
        return store.getMany(...args)
      },
    }
    const result = await hydrateDraftImages(local, {
      workflowId: 'wf',
      store: slow,
      fetchRemote: async () => (remoteCalls++, null),
      isStale: () => stale,
    })
    assert.equal(result.stale, true)
    assert.equal(remoteCalls, 0)
    assert.equal(local.drafts[0].images[0].lost, undefined)
  })

  it('多个草稿卡槽里的空壳图片一并补回', async () => {
    const a = bigImage('a')
    const b = bigImage('b')
    const local: MultiDraftLike = {
      activeIndex: 0,
      drafts: [
        { id: '1', text: '', presets: [], images: [shell(a)], updated_at: 1 },
        { id: '2', text: '', presets: [], images: [shell(b)], updated_at: 1 },
      ],
    }
    const result = await hydrateDraftImages(local, { workflowId: 'wf', store: null, fetchRemote: async () => draft([a, b]) })
    assert.equal(result.hydrated, 2)
    assert.equal(hasUnloadedImages(local), false)
  })

  it('已确认丢失的图片不会被再次处理', async () => {
    const lostImg: DraftImageLike = { name: 'x', data: '', lost: true }
    let calls = 0
    const result = await hydrateDraftImages(draft([lostImg]), { workflowId: 'wf', store: null, fetchRemote: async () => (calls++, null) })
    assert.equal(calls, 0)
    assert.equal(result.lost, 0)
  })
})

// ---------------------------------------------------------------------------
// 端到端：复现并堵住「刷新后敲字，服务端的图被空壳覆盖」
// ---------------------------------------------------------------------------

describe('丢图回归：本地空壳 + 载入 + 上传闸门', () => {
  it('空壳状态的签名与完整状态不同（这正是旧实现把空壳当成「有变化」上传的原因）', () => {
    const img = bigImage('a')
    assert.notEqual(draftSignature(draft([img])), draftSignature(draft([shell(img)])))
  })

  it('载入完成后，状态签名回到与上传时一致，不会多传一遍', async () => {
    const img = bigImage('a')
    const uploadedSignature = draftSignature(draft([img], 'hello'))
    const local = draft([shell(img)], 'hello')
    await hydrateDraftImages(local, { workflowId: 'wf', store: null, fetchRemote: async () => draft([img], 'hello') })
    assert.equal(draftSignature(local), uploadedSignature)
  })
})
