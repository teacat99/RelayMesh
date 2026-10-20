// 运行：pnpm --dir frontend run test:unit
// 预览视图的日期分隔（#120 阶段 E-b）的纯逻辑：哪个顶层条目前要画「今天 / 昨天 / 日期」。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { dateLabel, sameDay } from './dateLabel'
import { daySeparators } from './daySeparators'

const now = new Date(2026, 9, 18, 12, 0) // 2026-10-18 中午（本机时区）
const iso = (y: number, m: number, d: number, h = 9, mi = 0, s = 0) => new Date(y, m - 1, d, h, mi, s).toISOString()

describe('dateLabel / sameDay：从聊天视图抽出来的日历日判断', () => {
  it('同一天按本机日历日算，跨零点就是两天', () => {
    assert.equal(sameDay(new Date(2026, 9, 18, 0, 0, 0), new Date(2026, 9, 18, 23, 59, 59)), true)
    assert.equal(sameDay(new Date(2026, 9, 17, 23, 59, 59), new Date(2026, 9, 18, 0, 0, 0)), false)
  })
  it('今天 / 昨天 / 本年写月日 / 跨年带年份', () => {
    assert.equal(dateLabel(new Date(2026, 9, 18, 8), now), '今天')
    assert.equal(dateLabel(new Date(2026, 9, 17, 23), now), '昨天')
    assert.equal(dateLabel(new Date(2026, 9, 16, 9), now), '10月16日')
    assert.equal(dateLabel(new Date(2025, 11, 31, 9), now), '2025年12月31日')
  })
  it('月初的「昨天」跨月也对（上个月最后一天）', () => {
    const first = new Date(2026, 10, 1, 9)
    assert.equal(dateLabel(new Date(2026, 9, 31, 22), first), '昨天')
  })
  it('聊天视图仍从自己的模块转出同一组函数（导入路径不变）', async () => {
    const chat = await import('./chatView')
    assert.equal(chat.dateLabel, dateLabel)
    assert.equal(chat.sameDay, sameDay)
  })
})

describe('daySeparators：新的一天开始处才有标记', () => {
  it('没有条目、或所有条目都解析不了时间：没有标记', () => {
    assert.deepEqual(daySeparators([], now), {})
    assert.deepEqual(daySeparators([{ key: 'a', at: undefined }, { key: 'b', at: 'not a date' }, { key: 'c', at: null }], now), {})
  })

  it('同一天的所有条目：只有第一个有标记，写「今天」', () => {
    const marks = daySeparators(
      [
        { key: 'r1', at: iso(2026, 10, 18, 9) },
        { key: 'notice-1', at: iso(2026, 10, 18, 10) },
        { key: 'r2', at: iso(2026, 10, 18, 23, 59, 59) },
      ],
      now,
    )
    assert.deepEqual(marks, { r1: '今天' })
  })

  it('跨天：每一天的第一个条目有标记，标签依次是日期 / 昨天 / 今天', () => {
    const marks = daySeparators(
      [
        { key: 'r1', at: iso(2026, 10, 15, 22) },
        { key: 'r2', at: iso(2026, 10, 15, 23, 30) },
        { key: 'inbox-5', at: iso(2026, 10, 17, 0, 0, 0) },
        { key: 'r3', at: iso(2026, 10, 17, 8) },
        { key: 'r4', at: iso(2026, 10, 18, 0, 0, 1) },
      ],
      now,
    )
    assert.deepEqual(marks, { r1: '10月15日', 'inbox-5': '昨天', r4: '今天' })
  })

  it('跨年的更早内容带年份；毫秒时间戳也认', () => {
    const marks = daySeparators(
      [
        { key: 'old', at: new Date(2025, 11, 30, 9).getTime() },
        { key: 'new', at: new Date(2026, 0, 2, 9).getTime() },
      ],
      now,
    )
    assert.deepEqual(marks, { old: '2025年12月30日', new: '1月2日' })
  })

  it('解析不了时间的条目不打断前后同一天，也不会自己带标记（例如守护告警描述的是「现在」）', () => {
    const marks = daySeparators(
      [
        { key: 'bad-first', at: 'garbage' },
        { key: 'r1', at: iso(2026, 10, 18, 9) },
        { key: 'guard', at: undefined },
        { key: 'r2', at: iso(2026, 10, 18, 10) },
      ],
      now,
    )
    assert.deepEqual(marks, { r1: '今天' })
  })

  it('往上翻页载入更早的轮次：只多出更早的标记，原有的日期条不会错位', () => {
    const later = [
      { key: 'r3', at: iso(2026, 10, 17, 9) },
      { key: 'r4', at: iso(2026, 10, 18, 9) },
    ]
    const before = daySeparators(later, now)
    assert.deepEqual(before, { r3: '昨天', r4: '今天' })
    const after = daySeparators([{ key: 'r1', at: iso(2026, 10, 16, 9) }, { key: 'r2', at: iso(2026, 10, 17, 8) }, ...later], now)
    // r3 与 r2 同一天：它自己的标记让给 r2（更早载入的那个条目才是这天的第一个）
    assert.deepEqual(after, { r1: '10月16日', r2: '昨天', r4: '今天' })
  })

  it('时钟越过零点后同一份数据的标签跟着变（今天 → 昨天），标记位置不变', () => {
    const items = [{ key: 'r1', at: iso(2026, 10, 18, 9) }, { key: 'r2', at: iso(2026, 10, 18, 11) }]
    assert.deepEqual(daySeparators(items, now), { r1: '今天' })
    assert.deepEqual(daySeparators(items, new Date(2026, 9, 19, 0, 5)), { r1: '昨天' })
  })
})
