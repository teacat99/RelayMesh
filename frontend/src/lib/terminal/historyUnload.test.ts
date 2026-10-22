import test from 'node:test'
import assert from 'node:assert/strict'
import { UNLOAD_AT_BOTTOM_MS, UNLOAD_AWAY_MS, UnloadClock } from './historyUnload'

const up = { atBottom: false, away: false }
const bottom = { atBottom: true, away: false }

test('停在历史里翻看的时候，永远不卸载', () => {
  const c = new UnloadClock()
  for (let t = 0; t <= 60 * 60_000; t += 1000) assert.equal(c.sample(t, up), false)
})

test('回到实时底部满时限才卸载', () => {
  const c = new UnloadClock()
  assert.equal(c.sample(1000, bottom), false)
  assert.equal(c.sample(1000 + UNLOAD_AT_BOTTOM_MS - 1, bottom), false)
  assert.equal(c.sample(1000 + UNLOAD_AT_BOTTOM_MS, bottom), true)
})

test('回到底部后又往上翻：重新计时', () => {
  const c = new UnloadClock()
  c.sample(0, bottom)
  assert.equal(c.sample(UNLOAD_AT_BOTTOM_MS - 1000, bottom), false)
  c.sample(UNLOAD_AT_BOTTOM_MS - 500, up) // 翻上去了
  assert.equal(c.sample(UNLOAD_AT_BOTTOM_MS + 5000, bottom), false) // 重新从这里开始数
  assert.equal(c.sample(UNLOAD_AT_BOTTOM_MS + 5000 + UNLOAD_AT_BOTTOM_MS, bottom), true)
})

test('面板被切走：即使停在历史里，满时限也卸载；中途回来则重新计时', () => {
  const c = new UnloadClock()
  const away = { atBottom: false, away: true }
  assert.equal(c.sample(0, away), false)
  assert.equal(c.sample(UNLOAD_AWAY_MS - 1, away), false)
  assert.equal(c.sample(UNLOAD_AWAY_MS - 1, up), false) // 回来了
  assert.equal(c.sample(UNLOAD_AWAY_MS + 10_000, away), false) // 重新计时
  assert.equal(c.sample(UNLOAD_AWAY_MS + 10_000 + UNLOAD_AWAY_MS, away), true)
})

test('reset 清掉计时', () => {
  const c = new UnloadClock()
  c.sample(0, bottom)
  c.reset()
  assert.equal(c.sample(UNLOAD_AT_BOTTOM_MS + 1, bottom), false)
})

test('时限可以注入', () => {
  const c = new UnloadClock(100, 1000)
  c.sample(0, bottom)
  assert.equal(c.sample(100, bottom), true)
})
