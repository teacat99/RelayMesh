// 运行：pnpm --dir frontend run test:unit（esbuild 打包后交给 node:test，无需额外依赖）
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  MAX_INLINE_IMAGE_CHARS,
  createDraftPersister,
  draftSignature,
  draftStateHash,
  hashString,
  sanitizeDraftsForStorage,
  type DraftImageLike,
  type MultiDraftLike,
} from './draftSync'

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

class FakeClock {
  t = 0
  private seq = 0
  private timers = new Map<number, { at: number; fn: () => void }>()

  now = () => this.t
  setTimer = (fn: () => void, ms: number) => {
    const id = ++this.seq
    this.timers.set(id, { at: this.t + ms, fn })
    return id
  }
  clearTimer = (handle: unknown) => {
    this.timers.delete(handle as number)
  }

  /** 按触发时间顺序执行到期的定时器，再把时钟拨到目标时刻。 */
  advance(ms: number) {
    const target = this.t + ms
    for (;;) {
      let nextId = -1
      let nextAt = Infinity
      for (const [id, timer] of this.timers) {
        if (timer.at <= target && (timer.at < nextAt || (timer.at === nextAt && id < nextId))) {
          nextId = id
          nextAt = timer.at
        }
      }
      if (nextId === -1) break
      const timer = this.timers.get(nextId)!
      this.timers.delete(nextId)
      this.t = Math.max(this.t, timer.at)
      timer.fn()
    }
    this.t = target
  }

  get pendingTimers() {
    return this.timers.size
  }
}

const tick = () => new Promise<void>(resolve => setImmediate(resolve))

function state(text: string, extra: { images?: DraftImageLike[]; presets?: string[]; updatedAt?: number; activeIndex?: number } = {}): MultiDraftLike {
  return {
    activeIndex: extra.activeIndex ?? 0,
    drafts: [{ id: '1', text, presets: extra.presets ?? [], images: extra.images ?? [], updated_at: extra.updatedAt ?? 1 }],
  }
}

function image(name: string, data: string, hash = 'h-' + name): DraftImageLike {
  return { name, format: 'png', data_type: 'base64', hash, width: 10, height: 10, data }
}

interface UploadCall {
  id: string
  activeIndex: number
  json: string
  resolve: () => void
  reject: (error: unknown) => void
}

function harness() {
  const clock = new FakeClock()
  const localWrites: Array<{ id: string; json: string; at: number }> = []
  const uploads: UploadCall[] = []
  const uploaded: Array<{ id: string; hash: string }> = []
  const errors: Array<{ kind: string; id: string }> = []
  const flags = { failLocal: false }

  const persister = createDraftPersister({
    writeLocal: (id, json) => {
      if (flags.failLocal) throw new Error('QuotaExceededError')
      localWrites.push({ id, json, at: clock.t })
    },
    upload: (id, activeIndex, json) =>
      new Promise<void>((resolve, reject) => {
        uploads.push({ id, activeIndex, json, resolve, reject })
      }),
    onUploaded: (id, hash) => uploaded.push({ id, hash }),
    onError: (kind, id) => errors.push({ kind, id }),
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  })
  return { clock, persister, localWrites, uploads, uploaded, errors, flags }
}

// ---------------------------------------------------------------------------
// 纯函数
// ---------------------------------------------------------------------------

describe('hashString', () => {
  it('相同输入得到相同结果，不同输入得到不同结果', () => {
    assert.equal(hashString('hello 世界'), hashString('hello 世界'))
    assert.notEqual(hashString('hello'), hashString('hellp'))
    assert.notEqual(hashString('ab'), hashString('ba'))
    assert.notEqual(hashString(''), hashString(' '))
  })
})

describe('draftSignature', () => {
  it('忽略 updated_at，只有内容变化才改变签名', () => {
    assert.equal(draftSignature(state('a', { updatedAt: 1 })), draftSignature(state('a', { updatedAt: 999 })))
    assert.notEqual(draftSignature(state('a')), draftSignature(state('b')))
    assert.notEqual(draftSignature(state('a')), draftSignature(state('a', { presets: ['ok'] })))
    assert.notEqual(draftSignature(state('a')), draftSignature(state('a', { activeIndex: 1 })))
  })

  it('图片的增删、调序、尾部或长度变化都会改变签名', () => {
    const a = image('a.png', 'AAAA'.repeat(50))
    const b = image('b.png', 'BBBB'.repeat(50))
    const base = draftSignature(state('t', { images: [a] }))
    assert.notEqual(base, draftSignature(state('t', { images: [a, b] })))
    assert.notEqual(draftSignature(state('t', { images: [a, b] })), draftSignature(state('t', { images: [b, a] })))
    assert.notEqual(base, draftSignature(state('t', { images: [image('a.png', 'AAAA'.repeat(50) + 'X')] })))
    // 只有尾部不同（首部哈希相同、长度相同）也能区分
    assert.notEqual(
      draftSignature(state('t', { images: [image('a.png', 'A'.repeat(100) + 'X', 'same')] })),
      draftSignature(state('t', { images: [image('a.png', 'A'.repeat(100) + 'Y', 'same')] }))
    )
    assert.equal(base, draftSignature(state('t', { images: [a] })))
  })
})

describe('sanitizeDraftsForStorage', () => {
  it('小图保留正文，大图正文不落盘，且只保留必要字段', () => {
    const small = image('small.png', 'S'.repeat(1000))
    const big = { ...image('big.png', 'B'.repeat(MAX_INLINE_IMAGE_CHARS)), url: '/api/x' }
    const out = sanitizeDraftsForStorage(state('t', { images: [small, big] }))
    const images = out.drafts[0].images
    assert.equal(images[0].data, 'S'.repeat(1000))
    assert.equal(images[1].data, '')
    assert.equal(images[1].name, 'big.png')
    assert.equal('url' in images[1], false)
  })
})

// ---------------------------------------------------------------------------
// 落盘引擎
// ---------------------------------------------------------------------------

describe('createDraftPersister · 去抖合并', () => {
  it('连续触摸只在停顿后写一次本地、上传一次', async () => {
    const { clock, persister, localWrites, uploads } = harness()
    let text = ''
    const read = () => state(text)
    for (let i = 1; i <= 10; i++) {
      text = 'x'.repeat(i)
      persister.touch('wf-1', read)
      clock.advance(50)
    }
    assert.equal(localWrites.length, 0, '停顿之前不写')
    clock.advance(300)
    assert.equal(localWrites.length, 1)
    assert.equal(JSON.parse(localWrites[0].json).drafts[0].text, 'xxxxxxxxxx')
    assert.equal(uploads.length, 0)
    clock.advance(1500)
    assert.equal(uploads.length, 1)
    assert.equal(JSON.parse(uploads[0].json).drafts[0].text, 'xxxxxxxxxx')
    uploads[0].resolve()
    await tick()
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)
  })

  it('持续输入时受最长等待约束，不会一直不落盘', () => {
    const { clock, persister, localWrites } = harness()
    let n = 0
    const read = () => state('t' + n)
    for (let i = 0; i < 70; i++) {
      n = i
      persister.touch('wf-1', read)
      clock.advance(100)
    }
    assert.ok(localWrites.length >= 2, `实际写入 ${localWrites.length} 次`)
    assert.ok(localWrites[0].at <= 3000, `首次写入发生在 ${localWrites[0].at}ms`)
  })
})

describe('createDraftPersister · 内容没变化就不写不传', () => {
  it('只有 updated_at 变化时不写本地也不上传', async () => {
    const { clock, persister, localWrites, uploads } = harness()
    let stamp = 1
    const read = () => state('same', { updatedAt: stamp })
    persister.touch('wf-1', read)
    clock.advance(2000)
    uploads[0].resolve()
    await tick()
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)

    stamp = 2
    persister.touch('wf-1', read)
    clock.advance(5000)
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)
  })

  it('内容改回原样同样不需要再写（与最近一次成功写入比较）', async () => {
    const { clock, persister, localWrites, uploads } = harness()
    let text = 'a'
    const read = () => state(text)
    persister.touch('wf-1', read)
    clock.advance(2000)
    uploads[0].resolve()
    await tick()
    text = 'b'
    persister.touch('wf-1', read)
    text = 'a'
    persister.touch('wf-1', read)
    clock.advance(5000)
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)
  })
})

describe('createDraftPersister · flush', () => {
  it('立即落盘并取消待执行的定时器，之后不会重复', () => {
    const { clock, persister, localWrites, uploads } = harness()
    persister.touch('wf-1', () => state('draft'))
    persister.flush('wf-1')
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)
    assert.equal(clock.pendingTimers, 0)
    clock.advance(10_000)
    assert.equal(localWrites.length, 1)
    assert.equal(uploads.length, 1)
  })

  it('不带参数的 flush 只处理有待处理变化的工作流', () => {
    const { persister, localWrites } = harness()
    persister.touch('wf-a', () => state('A'))
    persister.flush('wf-a')
    persister.touch('wf-b', () => state('B'))
    persister.flush()
    assert.deepEqual(localWrites.map(w => w.id), ['wf-a', 'wf-b'])
    persister.flush()
    assert.equal(localWrites.length, 2)
  })

  it('hasPending 反映是否还有未冲刷的变化', () => {
    const { persister } = harness()
    assert.equal(persister.hasPending(), false)
    persister.touch('wf-1', () => state('x'))
    assert.equal(persister.hasPending('wf-1'), true)
    assert.equal(persister.hasPending('wf-2'), false)
    persister.flush('wf-1')
    assert.equal(persister.hasPending('wf-1'), false)
  })
})

describe('createDraftPersister · 工作流绑定（INV-D1）', () => {
  it('写入的键与内容始终成对，切换工作流不会把旧内容写进新键', () => {
    const { persister, localWrites, uploads } = harness()
    let bound = 'wf-a'
    const contents: Record<string, MultiDraftLike> = { 'wf-a': state('内容-A'), 'wf-b': state('内容-B') }
    const readFor = (id: string) => () => (bound === id ? contents[id] : null)

    persister.touch('wf-a', readFor('wf-a'))
    // 切换：先冲刷旧工作流（此刻输入区仍属于 A），再换绑定
    persister.flush('wf-a')
    bound = 'wf-b'
    persister.touch('wf-b', readFor('wf-b'))
    persister.flush('wf-b')

    for (const write of localWrites) {
      assert.ok(write.json.includes(write.id === 'wf-a' ? '内容-A' : '内容-B'), `${write.id} 写入了别的工作流的内容`)
    }
    for (const up of uploads) {
      assert.ok(up.json.includes(up.id === 'wf-a' ? '内容-A' : '内容-B'), `${up.id} 上传了别的工作流的内容`)
    }
    assert.deepEqual(localWrites.map(w => w.id), ['wf-a', 'wf-b'])
  })

  it('绑定已失效（读取回调返回 null）时丢弃本次写入，而不是写到别处', () => {
    const { clock, persister, localWrites, uploads } = harness()
    let bound = 'wf-a'
    persister.touch('wf-a', () => (bound === 'wf-a' ? state('A') : null))
    bound = 'wf-b' // 忘了先 flush 就换了绑定
    clock.advance(20_000)
    assert.equal(localWrites.length, 0)
    assert.equal(uploads.length, 0)
  })
})

describe('createDraftPersister · 上传串行（INV-D4）', () => {
  it('在途期间的新变化等待其结束后再上传，且内容是最新的', async () => {
    const { persister, uploads } = harness()
    let text = 'v1'
    const read = () => state(text)
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    assert.equal(uploads.length, 1)

    text = 'v2'
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    assert.equal(uploads.length, 1, '第一次上传未结束前不并发发起第二次')

    uploads[0].resolve()
    await tick()
    assert.equal(uploads.length, 2)
    assert.equal(JSON.parse(uploads[1].json).drafts[0].text, 'v2')
    uploads[1].resolve()
    await tick()
    assert.equal(uploads.length, 2)
  })

  it('在途期间内容又改回上传中的版本，结束后不再多传', async () => {
    const { persister, uploads } = harness()
    let text = 'v1'
    const read = () => state(text)
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    text = 'v2'
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    text = 'v1'
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    uploads[0].resolve()
    await tick()
    assert.equal(uploads.length, 1)
  })

  it('上传失败不会标记为已同步，下一次冲刷会重试；成功后不再重复', async () => {
    const { persister, uploads, errors, uploaded } = harness()
    const read = () => state('need-sync')
    persister.touch('wf-1', read)
    persister.flush('wf-1')
    uploads[0].reject(new Error('network'))
    await tick()
    assert.deepEqual(errors, [{ kind: 'remote', id: 'wf-1' }])
    assert.equal(uploaded.length, 0)

    persister.flush('wf-1')
    assert.equal(uploads.length, 2)
    uploads[1].resolve()
    await tick()
    assert.equal(uploaded.length, 1)
    assert.equal(uploaded[0].hash, draftStateHash(state('need-sync')))

    persister.flush('wf-1')
    assert.equal(uploads.length, 2)
  })
})

describe('createDraftPersister · 载入登记与异常', () => {
  it('markPersisted 之后同样的内容不会再写本地', () => {
    const { persister, localWrites } = harness()
    persister.markPersisted('wf-1', state('loaded'))
    persister.touch('wf-1', () => state('loaded'))
    persister.flush('wf-1')
    assert.equal(localWrites.length, 0)
    persister.touch('wf-1', () => state('edited'))
    persister.flush('wf-1')
    assert.equal(localWrites.length, 1)
  })

  it('远端一致性只在显式声明或已同步标记匹配时才登记', () => {
    const { persister, uploads } = harness()
    const s = state('loaded')

    persister.markPersisted('wf-1', s)
    persister.touch('wf-1', () => s)
    persister.flush('wf-1')
    assert.equal(uploads.length, 1, '仅本地一致时，服务端仍需要同步一次')

    const t = state('with-marker')
    persister.markPersisted('wf-2', t, { remoteSyncedHash: draftStateHash(t) })
    persister.touch('wf-2', () => t)
    persister.flush('wf-2')
    assert.equal(uploads.length, 1, '标记匹配则跳过上传')

    const u = state('stale-marker')
    persister.markPersisted('wf-3', u, { remoteSyncedHash: 'not-the-same' })
    persister.touch('wf-3', () => u)
    persister.flush('wf-3')
    assert.equal(uploads.length, 2, '标记不匹配则上传')

    const v = state('from-server')
    persister.markPersisted('wf-4', v, { remote: true })
    persister.touch('wf-4', () => v)
    persister.flush('wf-4')
    assert.equal(uploads.length, 2)
  })

  it('本地存储抛错（例如配额已满）时不抛出，下次冲刷重试', () => {
    const { persister, localWrites, errors, flags } = harness()
    flags.failLocal = true
    persister.touch('wf-1', () => state('quota'))
    persister.flush('wf-1')
    assert.deepEqual(errors, [{ kind: 'local', id: 'wf-1' }])
    assert.equal(localWrites.length, 0)
    assert.equal(persister.hasPending('wf-1'), true)

    flags.failLocal = false
    persister.flush('wf-1')
    assert.equal(localWrites.length, 1)
  })

  it('release 取消待执行的定时器且不写盘', () => {
    const { clock, persister, localWrites, uploads } = harness()
    persister.touch('wf-1', () => state('gone'))
    persister.release('wf-1')
    assert.equal(clock.pendingTimers, 0)
    clock.advance(20_000)
    assert.equal(localWrites.length, 0)
    assert.equal(uploads.length, 0)
  })
})
