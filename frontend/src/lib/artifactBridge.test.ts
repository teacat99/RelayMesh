import { test } from 'node:test'
import assert from 'node:assert/strict'
import { WriteLimiter, bridgeReply, clampFrameHeight, dataItemForPage, parseBridgeMessage } from './artifactBridge'

test('只接受带 rm:1 的合规消息', () => {
  assert.equal(parseBridgeMessage(null), null)
  assert.equal(parseBridgeMessage('x'), null)
  assert.equal(parseBridgeMessage({ type: 'data.list', id: 1 }), null)
  assert.equal(parseBridgeMessage({ rm: 2, type: 'data.list', id: 1 }), null)
  assert.equal(parseBridgeMessage({ rm: 1, type: 'chat.send', id: 1, value: 'x' }), null)
  assert.deepEqual(parseBridgeMessage({ rm: 1, type: 'data.list', id: 3 }), { kind: 'data.list', id: 3 })
})

test('数据键名校验，拒绝路径穿越与空白', () => {
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1, key: '../x' }), null)
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1, key: 'a b' }), null)
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1, key: '' }), null)
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1, key: 'a'.repeat(129) }), null)
  assert.deepEqual(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1, key: 'board:1.x-y_z' }), { kind: 'data.get', id: 1, key: 'board:1.x-y_z' })
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 0, key: 'k' }), null)
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.get', id: 1.5, key: 'k' }), null)
})

test('data.set 带期望版本与任意 JSON 值', () => {
  assert.deepEqual(parseBridgeMessage({ rm: 1, type: 'data.set', id: 2, key: 'k', value: { a: [1] }, expected: 0 }), {
    kind: 'data.set', id: 2, key: 'k', value: { a: [1] }, expected: 0,
  })
  assert.equal(parseBridgeMessage({ rm: 1, type: 'data.set', id: 2, key: 'k', value: 1, expected: 'x' }), null)
  const m = parseBridgeMessage({ rm: 1, type: 'data.set', id: 2, key: 'k', value: null })
  assert.ok(m && m.kind === 'data.set' && m.expected === undefined)
})

test('评论：文本必填，锚点截断', () => {
  assert.equal(parseBridgeMessage({ rm: 1, type: 'comment', id: 1, value: '  ' }), null)
  const m = parseBridgeMessage({ rm: 1, type: 'comment', id: 1, key: 'x'.repeat(300), value: ' 改一下 ' })
  assert.ok(m && m.kind === 'comment' && m.text === '改一下' && m.anchor.length === 128)
})

test('resize 夹在合理范围内', () => {
  assert.equal(clampFrameHeight(10), 160)
  assert.equal(clampFrameHeight(99999), 5000)
  assert.equal(clampFrameHeight(400.2), 401)
  assert.deepEqual(parseBridgeMessage({ rm: 1, type: 'resize', height: 3 }), { kind: 'resize', height: 160 })
  assert.equal(parseBridgeMessage({ rm: 1, type: 'resize', height: 'abc' }), null)
})

test('写入限频：窗口内超过上限被拒，过窗口恢复', () => {
  const l = new WriteLimiter(3, 1000)
  assert.ok(l.allow(0) && l.allow(10) && l.allow(20))
  assert.equal(l.allow(30), false)
  assert.ok(l.allow(1100))
})

test('应答与数据项还原', () => {
  assert.deepEqual(bridgeReply(5, true, { a: 1 }), { rm: 1, id: 5, ok: true, value: { a: 1 } })
  assert.deepEqual(bridgeReply(5, false, '出错了'), { rm: 1, id: 5, ok: false, error: '出错了' })
  assert.deepEqual(dataItemForPage({ key: 'k', value: '{"x":[1,2]}', version: 3 }), { key: 'k', value: { x: [1, 2] }, version: 3 })
  assert.deepEqual(dataItemForPage({ key: 'k', value: '{坏', version: 1 }), { key: 'k', value: null, version: 1 })
})
