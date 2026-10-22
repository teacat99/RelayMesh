// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { contentSignature, decideAdopt, isBlankDraft, isBlankUnsynced, parseServerTime, type AdoptInput } from './draftAdopt'
import type { DraftImageLike, MultiDraftLike } from './draftSync'

function input(overrides: Partial<AdoptInput> = {}): AdoptInput {
  return {
    serverUpdatedAt: 2000,
    syncedAt: 1000,
    localClean: true,
    noLocalRecord: false,
    sameContent: false,
    blankUnsynced: false,
    touched: false,
    ...overrides,
  }
}

function state(text: string, images: DraftImageLike[] = [], activeIndex = 0): MultiDraftLike {
  return { activeIndex, drafts: [{ id: '1', text, presets: [], images, updated_at: 1 }] }
}

describe('decideAdopt', () => {
  it('服务端没有草稿：保留本机', () => {
    assert.deepEqual(decideAdopt(input({ serverUpdatedAt: null })), { action: 'keep', reason: 'no_server_draft' })
    // 即使本机没有记录，服务端也没有可采纳的东西
    assert.equal(decideAdopt(input({ serverUpdatedAt: null, noLocalRecord: true })).action, 'keep')
  })

  it('本机没有这个工作流的记录（全新设备）：采纳服务端草稿', () => {
    assert.deepEqual(decideAdopt(input({ noLocalRecord: true, localClean: false, syncedAt: null })), { action: 'adopt', reason: 'no_local_record' })
  })

  it('决策期间用户又在输入：一律保留本机，哪怕本机没有记录', () => {
    assert.equal(decideAdopt(input({ touched: true })).action, 'keep')
    assert.deepEqual(decideAdopt(input({ touched: true, noLocalRecord: true })), { action: 'keep', reason: 'user_touched' })
  })

  it('内容相同：不用采纳（调用方只需刷新同步标记）', () => {
    assert.deepEqual(decideAdopt(input({ sameContent: true })), { action: 'keep', reason: 'same_content' })
  })

  it('本机有未同步的改动：绝不覆盖（最后写入者胜，本机之后会上传）', () => {
    assert.deepEqual(decideAdopt(input({ localClean: false })), { action: 'keep', reason: 'local_dirty' })
  })

  it('本机干净、服务端比上次同步时更新：采纳另一台设备的草稿', () => {
    assert.deepEqual(decideAdopt(input()), { action: 'adopt', reason: 'newer' })
  })

  it('服务端不比上次同步更新（包括正好相等，即本机自己刚传上去的那一份）：保留', () => {
    assert.deepEqual(decideAdopt(input({ serverUpdatedAt: 1000 })), { action: 'keep', reason: 'not_newer' })
    assert.deepEqual(decideAdopt(input({ serverUpdatedAt: 500 })), { action: 'keep', reason: 'not_newer' })
  })

  it('本机干净但没有同步时间记录（升级前留下的标记）：服务端内容不同就视为另一台设备改过，采纳', () => {
    assert.deepEqual(decideAdopt(input({ syncedAt: null })), { action: 'adopt', reason: 'newer' })
  })

  it('本机只有一份从未同步过的空白草稿（打开页面 / 刷新时自动落下的空壳）：不是用户的改动，采纳服务端草稿', () => {
    // 实测的事故：新设备在载入完成之前刷新一次，留下空壳本地记录；下一次载入把它当成「未同步的本机改动」保留，
    // 随后上传就用空白覆盖了另一台设备写在服务端的草稿
    assert.deepEqual(decideAdopt(input({ blankUnsynced: true, localClean: false, syncedAt: null })), { action: 'adopt', reason: 'blank_unsynced' })
    // 即使服务端的更新时间不比任何记录新（本机根本没有同步记录），也照样采纳
    assert.equal(decideAdopt(input({ blankUnsynced: true, localClean: false, serverUpdatedAt: 1, syncedAt: 5000 })).action, 'adopt')
  })

  it('服务端草稿同样是空白：不用采纳，只刷新标记', () => {
    assert.deepEqual(decideAdopt(input({ blankUnsynced: true, localClean: false, sameContent: true })), { action: 'keep', reason: 'same_content' })
  })

  it('空壳不能压过「用户已经在输入」：决策期间输入了字就保留本机', () => {
    assert.deepEqual(decideAdopt(input({ blankUnsynced: true, localClean: false, touched: true })), { action: 'keep', reason: 'user_touched' })
  })

  it('优先级：没有服务端草稿 > 用户已输入 > 本机无记录 > 内容相同 > 空白空壳 > 本机有改动 > 不更新', () => {
    assert.equal(decideAdopt(input({ serverUpdatedAt: null, touched: true })).reason, 'no_server_draft')
    assert.equal(decideAdopt(input({ touched: true, noLocalRecord: true, sameContent: true })).reason, 'user_touched')
    assert.equal(decideAdopt(input({ noLocalRecord: true, sameContent: true })).reason, 'no_local_record')
    assert.equal(decideAdopt(input({ sameContent: true, localClean: false, blankUnsynced: true })).reason, 'same_content')
    assert.equal(decideAdopt(input({ blankUnsynced: true, localClean: false })).reason, 'blank_unsynced')
    assert.equal(decideAdopt(input({ localClean: false, serverUpdatedAt: 1 })).reason, 'local_dirty')
  })
})

describe('contentSignature', () => {
  const a: DraftImageLike = { name: 'a.png', key: 'k-a', data: 'AAAA' }

  it('忽略 updated_at 与图片正文是否载入：同一张图（同内容键）无论内联还是只有引用都是同一份内容', () => {
    const inline = state('hi', [{ ...a }])
    const reference = state('hi', [{ name: 'a.png', key: 'k-a', data: '' }])
    const synced = state('hi', [{ ...a, synced: true }])
    inline.drafts[0].updated_at = 999
    assert.equal(contentSignature(inline), contentSignature(reference))
    assert.equal(contentSignature(inline), contentSignature(synced))
  })

  it('文字、预设、图片、当前序号、卡槽变化都会改变签名', () => {
    const base = contentSignature(state('hi', [a]))
    assert.notEqual(base, contentSignature(state('hi!', [a])))
    assert.notEqual(base, contentSignature(state('hi', [])))
    assert.notEqual(base, contentSignature(state('hi', [{ ...a, key: 'k-b' }])))
    assert.notEqual(base, contentSignature(state('hi', [a], 1)))
    const withPreset = state('hi', [a])
    withPreset.drafts[0].presets = ['ok']
    assert.notEqual(base, contentSignature(withPreset))
    const twoSlots = state('hi', [a])
    twoSlots.drafts.push({ id: '2', text: '', presets: [], images: [], updated_at: 1 })
    assert.notEqual(base, contentSignature(twoSlots))
  })

  it('旧草稿的图片没有内容键：用名称 / 哈希 / 正文长度与尾部区分', () => {
    const x: DraftImageLike = { name: 'x.png', hash: 'h', data: 'XXXXYYYY' }
    const y: DraftImageLike = { name: 'x.png', hash: 'h', data: 'XXXXZZZZ' }
    assert.notEqual(contentSignature(state('', [x])), contentSignature(state('', [y])))
    assert.equal(contentSignature(state('', [x])), contentSignature(state('', [{ ...x }])))
  })
})

describe('isBlankDraft / parseServerTime', () => {
  it('只有一个卡槽且没有文字、预设、图片才算空白', () => {
    assert.equal(isBlankDraft(state('')), true)
    assert.equal(isBlankDraft(state(' ')), false)
    assert.equal(isBlankDraft(state('', [{ key: 'k' }])), false)
    const two = state('')
    two.drafts.push({ id: '2', text: '', presets: [], images: [], updated_at: 1 })
    assert.equal(isBlankDraft(two), false)
    const preset = state('')
    preset.drafts[0].presets = ['ok']
    assert.equal(isBlankDraft(preset), false)
  })

  it('从未同步过的空白草稿：空白且没有已同步标记才算；有标记（确实同步过再清空）或有内容都不算', () => {
    assert.equal(isBlankUnsynced(state(''), null), true)
    // 同步过再清空：用户的真实意图（清空），要照常上传
    assert.equal(isBlankUnsynced(state(''), 'abc123'), false)
    assert.equal(isBlankUnsynced(state('在离线时写的'), null), false)
    assert.equal(isBlankUnsynced(state('', [{ key: 'k' }]), null), false)
  })

  it('服务端时间解析：合法 ISO → 毫秒；空 / 非法 → null', () => {
    assert.equal(parseServerTime('2026-10-03T00:00:00Z'), Date.parse('2026-10-03T00:00:00Z'))
    // 服务端（Go）返回纳秒精度的 RFC3339 时间，浏览器必须能解析
    const nano = parseServerTime('2026-10-03T23:40:07.067054564+08:00')
    assert.ok(nano !== null && Math.abs(nano - Date.parse('2026-10-03T23:40:07.067+08:00')) < 5)
    assert.equal(parseServerTime(''), null)
    assert.equal(parseServerTime(undefined), null)
    assert.equal(parseServerTime('not a date'), null)
  })
})
