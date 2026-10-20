import { afterEach, beforeEach, describe, mock, test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DRAFT_DEBOUNCE_MS,
  DRAFT_PREFIX,
  DRAFT_TTL_MS,
  DraftController,
  clearDraft,
  clearLocalDraft,
  draftScopes,
  listDrafts,
  pruneDrafts,
  readDraft,
  storageKey,
  writeDraft,
  type DraftControllerOptions,
  type DraftStorage,
} from './localDraft'

/** 内存版 localStorage：可限制总字符数模拟配额满，并统计 setItem 次数。 */
class MemStorage implements DraftStorage {
  private m = new Map<string, string>()
  quota = Infinity
  sets = 0
  get length() {
    return this.m.size
  }
  key(i: number) {
    return [...this.m.keys()][i] ?? null
  }
  getItem(k: string) {
    return this.m.get(k) ?? null
  }
  setItem(k: string, v: string) {
    let total = v.length
    for (const [kk, vv] of this.m) if (kk !== k) total += vv.length
    if (total > this.quota) throw new Error('QuotaExceededError')
    this.sets++
    this.m.set(k, v)
  }
  removeItem(k: string) {
    this.m.delete(k)
  }
}

type Note = { text: string }
const isEmpty = (d: Note) => d.text.trim() === ''
const isNote = (d: unknown): d is Note => !!d && typeof d === 'object' && typeof (d as Note).text === 'string'

describe('draftScopes', () => {
  test('同一份输入永远得到同一个作用域字符串', () => {
    assert.equal(draftScopes.confirm('wf-1', 'Q9'), 'confirm:wf-1:Q9')
    assert.equal(draftScopes.workImprove(12), 'work-improve:12')
    assert.equal(draftScopes.workDesc(12), 'work-desc:12')
    assert.equal(draftScopes.docComment('wf-1', 'a/b.md'), 'doc-comment:wf-1:a/b.md')
    assert.equal(draftScopes.docEdit('wf-1', 'a/b.md'), 'doc-edit:wf-1:a/b.md')
  })
  test('存储键带版本前缀，和其它 localStorage 键不会撞', () => {
    assert.equal(storageKey('x'), `${DRAFT_PREFIX}x`)
    assert.ok(DRAFT_PREFIX.includes('relaymesh.draft.v1'))
  })
})

describe('readDraft / writeDraft', () => {
  test('写入后能读回；没写过返回 null', () => {
    const s = new MemStorage()
    assert.equal(readDraft<Note>(s, 'a', 1000), null)
    assert.equal(writeDraft(s, 'a', { text: 'hi' }, 1000), 'saved')
    assert.deepEqual(readDraft<Note>(s, 'a', 1000), { text: 'hi' })
  })

  test('超过保留时长就当没有，并顺手删掉', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: 'old' }, 1000)
    assert.deepEqual(readDraft<Note>(s, 'a', 1000 + DRAFT_TTL_MS), { text: 'old' })
    assert.equal(readDraft<Note>(s, 'a', 1000 + DRAFT_TTL_MS + 1), null)
    assert.equal(s.getItem(storageKey('a')), null)
  })

  test('时钟被调回去（写入时间在未来）不算过期', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: 'x' }, 5_000_000)
    assert.deepEqual(readDraft<Note>(s, 'a', 1000), { text: 'x' })
  })

  test('损坏的内容、旧格式、形状不对：当作没有并删掉', () => {
    const s = new MemStorage()
    s.setItem(storageKey('bad-json'), '{not json')
    s.setItem(storageKey('bad-version'), JSON.stringify({ v: 2, t: 1000, d: { text: 'x' } }))
    s.setItem(storageKey('no-time'), JSON.stringify({ v: 1, d: { text: 'x' } }))
    s.setItem(storageKey('no-data'), JSON.stringify({ v: 1, t: 1000 }))
    writeDraft(s, 'wrong-shape', { other: 1 }, 1000)
    for (const scope of ['bad-json', 'bad-version', 'no-time', 'no-data']) {
      assert.equal(readDraft<Note>(s, scope, 1000, { validate: isNote }), null, scope)
      assert.equal(s.getItem(storageKey(scope)), null, scope)
    }
    assert.equal(readDraft<Note>(s, 'wrong-shape', 1000, { validate: isNote }), null)
    assert.equal(s.getItem(storageKey('wrong-shape')), null)
  })

  test('超长不写，已有的旧草稿保持原样', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: 'small' }, 1000)
    assert.equal(writeDraft(s, 'a', { text: 'x'.repeat(500) }, 2000, 200), 'too_large')
    assert.deepEqual(readDraft<Note>(s, 'a', 2000), { text: 'small' })
  })

  test('配额满：能腾出来的（过期的）先淘汰再重试；腾不出来就放弃，不抛错', () => {
    const s = new MemStorage()
    writeDraft(s, 'old', { text: 'o'.repeat(400) }, 1000)
    s.quota = 500
    // 旧草稿没过期、不在淘汰范围内 → 放不下，返回 failed
    assert.equal(writeDraft(s, 'new', { text: 'n'.repeat(400) }, 2000), 'failed')
    assert.equal(readDraft<Note>(s, 'new', 2000), null)
    assert.deepEqual(readDraft<Note>(s, 'old', 2000), { text: 'o'.repeat(400) })
  })

  test('配额满且旧草稿已过期：清掉过期的之后写入成功', () => {
    const s = new MemStorage()
    writeDraft(s, 'expired', { text: 'o'.repeat(400) }, 1000)
    s.quota = 500
    const later = 1000 + DRAFT_TTL_MS + 10
    assert.equal(writeDraft(s, 'new', { text: 'n'.repeat(300) }, later), 'saved')
    assert.equal(s.getItem(storageKey('expired')), null)
    assert.deepEqual(readDraft<Note>(s, 'new', later), { text: 'n'.repeat(300) })
  })

  test('clearDraft 只删这一条', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: '1' }, 1000)
    writeDraft(s, 'b', { text: '2' }, 1000)
    clearDraft(s, 'a')
    assert.equal(readDraft<Note>(s, 'a', 1000), null)
    assert.deepEqual(readDraft<Note>(s, 'b', 1000), { text: '2' })
  })
})

describe('listDrafts / pruneDrafts', () => {
  test('只列出自己前缀的键，别人的数据不碰', () => {
    const s = new MemStorage()
    s.setItem('relaymesh_multi_drafts_wf_1', '[]')
    s.setItem('theme', 'dark')
    writeDraft(s, 'a', { text: '1' }, 1000)
    assert.deepEqual(listDrafts(s).map(i => i.scope), ['a'])
    pruneDrafts(s, 1000 + DRAFT_TTL_MS * 10)
    assert.equal(s.getItem('relaymesh_multi_drafts_wf_1'), '[]')
    assert.equal(s.getItem('theme'), 'dark')
  })

  test('过期与损坏的被清掉，其余保留', () => {
    const s = new MemStorage()
    const now = DRAFT_TTL_MS * 3
    writeDraft(s, 'fresh', { text: 'f' }, now - 1000)
    writeDraft(s, 'stale', { text: 's' }, 1000)
    s.setItem(storageKey('broken'), 'xx')
    const removed = pruneDrafts(s, now)
    assert.equal(removed, 2)
    assert.deepEqual(listDrafts(s).map(i => i.scope), ['fresh'])
  })

  test('超过条数上限：保留最近动过的', () => {
    const s = new MemStorage()
    for (let i = 0; i < 6; i++) writeDraft(s, `d${i}`, { text: `${i}` }, 1000 + i)
    const removed = pruneDrafts(s, 2000, { maxEntries: 3 })
    assert.equal(removed, 3)
    assert.deepEqual(listDrafts(s).map(i => i.scope).sort(), ['d3', 'd4', 'd5'])
  })

  test('超过总字符数上限：从最久没动过的开始淘汰', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: 'a'.repeat(100) }, 1000)
    writeDraft(s, 'b', { text: 'b'.repeat(100) }, 2000)
    writeDraft(s, 'c', { text: 'c'.repeat(100) }, 3000)
    const one = listDrafts(s)[0].size
    pruneDrafts(s, 4000, { maxTotalChars: one * 2 + 10 })
    assert.deepEqual(listDrafts(s).map(i => i.scope).sort(), ['b', 'c'])
  })
})

describe('DraftController', () => {
  beforeEach(() => {
    mock.timers.enable({ apis: ['setTimeout'] })
  })
  afterEach(() => {
    mock.timers.reset()
  })

  function make(store: DraftStorage | null, extra: Partial<DraftControllerOptions<Note>> = {}) {
    return new DraftController<Note>({ store, isEmpty, validate: isNote, now: () => 1_000_000, ...extra })
  }

  test('停顿后才落盘（防抖），连续输入只写一次', () => {
    const s = new MemStorage()
    const c = make(s)
    c.switchTo('a')
    c.update({ text: 'h' })
    c.update({ text: 'he' })
    c.update({ text: 'hel' })
    mock.timers.tick(DRAFT_DEBOUNCE_MS - 1)
    assert.equal(s.sets, 0)
    mock.timers.tick(1)
    assert.equal(s.sets, 1)
    assert.deepEqual(readDraft<Note>(s, 'a', 1_000_000), { text: 'hel' })
    c.dispose()
  })

  test('切换作用域时先把旧作用域没落盘的内容写下去，并返回新作用域的草稿', () => {
    const s = new MemStorage()
    writeDraft(s, 'b', { text: 'draft-b' }, 1_000_000)
    const c = make(s)
    assert.equal(c.switchTo('a'), null)
    c.update({ text: 'typing-in-a' })
    assert.equal(s.getItem(storageKey('a')), null)
    assert.deepEqual(c.switchTo('b'), { text: 'draft-b' })
    assert.deepEqual(readDraft<Note>(s, 'a', 1_000_000), { text: 'typing-in-a' })
    // 之后的输入属于 b，不会写进 a
    c.update({ text: 'edited-b' })
    c.switchTo('a')
    assert.deepEqual(readDraft<Note>(s, 'b', 1_000_000), { text: 'edited-b' })
    assert.deepEqual(readDraft<Note>(s, 'a', 1_000_000), { text: 'typing-in-a' })
    c.dispose()
  })

  test('旧作用域里把内容清空：草稿被删掉，不会留一份空的', () => {
    const s = new MemStorage()
    const c = make(s)
    c.switchTo('a')
    c.update({ text: 'x' })
    c.flush()
    assert.notEqual(s.getItem(storageKey('a')), null)
    c.update({ text: '   ' })
    c.flush()
    assert.equal(s.getItem(storageKey('a')), null)
    c.dispose()
  })

  test('内容和已落盘的一致就不再写（不会无谓续期）', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: 'same' }, 1_000_000)
    const before = s.sets
    const c = make(s)
    assert.deepEqual(c.switchTo('a'), { text: 'same' })
    c.update({ text: 'same' })
    c.flush()
    assert.equal(s.sets, before)
    c.update({ text: 'changed' })
    c.flush()
    assert.equal(s.sets, before + 1)
    c.dispose()
  })

  test('读到的草稿是空内容：当作没有并删掉', () => {
    const s = new MemStorage()
    writeDraft(s, 'a', { text: '  ' }, 1_000_000)
    const c = make(s)
    assert.equal(c.switchTo('a'), null)
    assert.equal(s.getItem(storageKey('a')), null)
    c.dispose()
  })

  test('clear 删掉草稿，并丢弃还没落盘的内容', () => {
    const s = new MemStorage()
    const c = make(s)
    c.switchTo('a')
    c.update({ text: 'x' })
    c.flush()
    c.update({ text: 'xy' })
    c.clear()
    mock.timers.tick(DRAFT_DEBOUNCE_MS * 3)
    assert.equal(s.getItem(storageKey('a')), null)
    c.dispose()
    assert.equal(s.getItem(storageKey('a')), null)
  })

  test('dispose（卸载）时把没落盘的内容写下去', () => {
    const s = new MemStorage()
    const c = make(s)
    c.switchTo('a')
    c.update({ text: 'about-to-unmount' })
    c.dispose()
    assert.deepEqual(readDraft<Note>(s, 'a', 1_000_000), { text: 'about-to-unmount' })
  })

  test('没有作用域时 update 不写任何东西', () => {
    const s = new MemStorage()
    const c = make(s)
    c.switchTo(null)
    c.update({ text: 'orphan' })
    c.flush()
    assert.deepEqual(listDrafts(s), [])
    c.dispose()
  })

  test('存储不可用（null）时全部静默，不抛错', () => {
    const c = make(null)
    assert.equal(c.switchTo('a'), null)
    assert.doesNotThrow(() => {
      c.update({ text: 'x' })
      c.flush()
      c.clear()
      c.dispose()
    })
  })

  test('存储读写都抛错：静默放弃，不影响调用方', () => {
    const broken: DraftStorage = {
      length: 0,
      key: () => null,
      getItem: () => {
        throw new Error('SecurityError')
      },
      setItem: () => {
        throw new Error('QuotaExceededError')
      },
      removeItem: () => {
        throw new Error('SecurityError')
      },
    }
    const c = make(broken)
    assert.equal(c.switchTo('a'), null)
    assert.doesNotThrow(() => {
      c.update({ text: 'x' })
      c.flush()
      c.clear()
      c.dispose()
    })
  })

  test('maxChars 放宽后，长文本也能存', () => {
    const s = new MemStorage()
    const big = 'z'.repeat(100_000)
    const small = make(s)
    small.switchTo('a')
    small.update({ text: big })
    small.flush()
    assert.equal(readDraft<Note>(s, 'a', 1_000_000), null)
    small.dispose()
    const wide = make(s, { maxChars: 400_000 })
    wide.switchTo('a')
    wide.update({ text: big })
    wide.flush()
    assert.deepEqual(readDraft<Note>(s, 'a', 1_000_000), { text: big })
    wide.dispose()
  })
})

describe('clearLocalDraft', () => {
  beforeEach(() => {
    mock.timers.enable({ apis: ['setTimeout'] })
  })
  afterEach(() => {
    mock.timers.reset()
  })

  test('提交成功后清掉：存储里的删掉，活着的控制器里还没落盘的也不会被写回来', () => {
    const s = new MemStorage()
    const c = new DraftController<Note>({ store: s, isEmpty, now: () => 1_000_000 })
    c.switchTo('confirm:wf:Q1')
    c.update({ text: 'note' })
    c.flush()
    c.update({ text: 'note, last words' }) // 还在防抖里
    clearLocalDraft('confirm:wf:Q1', s)
    mock.timers.tick(DRAFT_DEBOUNCE_MS * 3)
    c.dispose()
    assert.equal(s.getItem(storageKey('confirm:wf:Q1')), null)
  })

  test('只影响同一个作用域的控制器', () => {
    const s = new MemStorage()
    const a = new DraftController<Note>({ store: s, isEmpty, now: () => 1_000_000 })
    const b = new DraftController<Note>({ store: s, isEmpty, now: () => 1_000_000 })
    a.switchTo('x')
    b.switchTo('y')
    a.update({ text: 'ax' })
    b.update({ text: 'by' })
    clearLocalDraft('x', s)
    a.dispose()
    b.dispose()
    assert.equal(readDraft<Note>(s, 'x', 1_000_000), null)
    assert.deepEqual(readDraft<Note>(s, 'y', 1_000_000), { text: 'by' })
  })

  test('没有存储时不抛错', () => {
    assert.doesNotThrow(() => clearLocalDraft('x', null))
  })
})
