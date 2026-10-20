import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { LIVE_TAIL_NOTICES, liveNoticeKeys, noticeCvClass, noticeEntryKey } from './noticeCv'
import type { TimelineBlock, TimelineEntry } from './timeline'
import type { FeedbackSession } from '../api/types'

const notice = (id: string, at: string) => ({ session_id: id, created_at: at })
const many = (n: number) => Array.from({ length: n }, (_, i) => notice(`n${i}`, new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString()))

describe('通知卡离屏跳过渲染的选择规则（#169）', () => {
  it('通知总数不超过 LIVE_TAIL_NOTICES：返回 null，一个都不套（行为与以前完全一样）', () => {
    for (const n of [0, 1, LIVE_TAIL_NOTICES - 1, LIVE_TAIL_NOTICES]) assert.equal(liveNoticeKeys(many(n)), null, `n=${n}`)
  })

  it('超过之后：最新的 LIVE_TAIL_NOTICES 条保持完整渲染，更早的才套', () => {
    const list = many(LIVE_TAIL_NOTICES + 5)
    const live = liveNoticeKeys(list)
    assert.ok(live)
    assert.equal(live.size, LIVE_TAIL_NOTICES)
    for (let i = 0; i < list.length; i++) {
      assert.equal(live.has(noticeEntryKey(list[i].session_id)), i >= 5, `第 ${i} 条`)
    }
  })

  it('不假定传入顺序：按时间取最新；时间相同按传入顺序靠后者算新；时间不可解析按最早处理', () => {
    const shuffled = [...many(30)].reverse()
    const live = liveNoticeKeys(shuffled)
    assert.ok(live)
    assert.deepEqual(
      [...live].map(k => Number(k.slice('notice-n'.length))).sort((a, b) => a - b),
      Array.from({ length: LIVE_TAIL_NOTICES }, (_, i) => 30 - LIVE_TAIL_NOTICES + i),
    )

    const same = Array.from({ length: 4 }, (_, i) => notice(`s${i}`, '2026-10-01T00:00:00Z'))
    const two = liveNoticeKeys(same, 2)
    assert.ok(two)
    assert.deepEqual([...two].sort(), [noticeEntryKey('s2'), noticeEntryKey('s3')])

    const broken = [notice('bad', 'not-a-date'), notice('a', '2026-10-01T00:00:00Z'), notice('b', '2026-10-02T00:00:00Z')]
    const one = liveNoticeKeys(broken, 2)
    assert.ok(one)
    assert.deepEqual([...one].sort(), [noticeEntryKey('a'), noticeEntryKey('b')])
  })

  it('键与时间线条目的键一致（PreviewMessageList.timelineEntries 里是 notice-<会话号>）', () => {
    assert.equal(noticeEntryKey('sess-abcdef12'), 'notice-sess-abcdef12')
  })
})

describe('noticeCvClass：只有「单张的通知卡 + 位置允许 + 不在最新的几条里」才套', () => {
  const session = { session_id: 'n1' } as unknown as FeedbackSession
  const noticeBlock: TimelineBlock = { type: 'single', key: noticeEntryKey('n1'), entry: { kind: 'notice', key: noticeEntryKey('n1'), at: 't', session } as TimelineEntry }
  const otherBlock: TimelineBlock = { type: 'single', key: 'inbox-1', entry: { kind: 'inbox', key: 'inbox-1', at: 't', q: {} } as unknown as TimelineEntry }
  const stackBlock: TimelineBlock = { type: 'stack', key: 'stack-x', entries: [] }

  it('套 / 不套', () => {
    const live = new Set<string>([noticeEntryKey('keep')])
    assert.equal(noticeCvClass(live, noticeBlock, true), 'pv-cv-notice')
    // 最新的几条
    assert.equal(noticeCvClass(new Set([noticeEntryKey('n1')]), noticeBlock, true), undefined)
    // 通知不多（live 为 null）：一个都不套
    assert.equal(noticeCvClass(null, noticeBlock, true), undefined)
    // 位置不允许（提问与回复之间 / 紧挨向上载入锚点的几轮）
    assert.equal(noticeCvClass(live, noticeBlock, false), undefined)
    assert.equal(noticeCvClass(live, noticeBlock, undefined), undefined)
    // 不是通知：事件行 / 摞 / 追加都不套
    assert.equal(noticeCvClass(live, otherBlock, true), undefined)
    assert.equal(noticeCvClass(live, stackBlock, true), undefined)
  })
})
