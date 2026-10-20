import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { REVEAL_AUTO_HIDE_MS, formatLockRemaining, revealErrorInfo } from './credentialReveal'

function http(status: number, data?: Record<string, unknown>) {
  return { response: { status, data } }
}

test('密码错误（403 reveal_bad_password）：沿用服务端文案，不算锁定', () => {
  const info = revealErrorInfo(http(403, { code: 'reveal_bad_password', error: '密码错误，请重新输入' }))
  assert.equal(info.message, '密码错误，请重新输入')
  assert.equal(info.locked, false)
  assert.equal(info.gone, false)
})

test('密码错误但服务端没给文案：兜底为通用提示', () => {
  const info = revealErrorInfo(http(403, { code: 'reveal_bad_password' }))
  assert.equal(info.message, '密码错误，请重新输入')
})

test('被锁定（429）：标记 locked，并把剩余秒数换成分钟', () => {
  const info = revealErrorInfo(http(429, { code: 'reveal_locked', remaining_seconds: 840, error: 'x' }))
  assert.equal(info.locked, true)
  assert.match(info.message, /约 14 分钟/)
})

test('被锁定但没有剩余时间：退回服务端文案，仍标记 locked', () => {
  const info = revealErrorInfo(http(429, { error: '已被锁定' }))
  assert.equal(info.locked, true)
  assert.equal(info.message, '已被锁定')
})

test('令牌不存在（404）：标记 gone，让界面收起并刷新列表', () => {
  const info = revealErrorInfo(http(404, { code: 'credential_not_found' }))
  assert.equal(info.gone, true)
  assert.equal(info.locked, false)
})

test('401 与 503 有各自的提示；其他状态用服务端文案或错误消息兜底', () => {
  assert.match(revealErrorInfo(http(401)).message, /登录已过期/)
  assert.match(revealErrorInfo(http(503)).message, /暂时无法验证密码/)
  assert.equal(revealErrorInfo(http(500, { error: '内部错误' })).message, '内部错误')
  assert.equal(revealErrorInfo({ response: { status: 500 }, message: 'boom' }).message, 'boom')
  assert.equal(revealErrorInfo(http(500)).message, '查看失败')
})

test('没有响应（断网 / 超时）：提示检查网络；空值也不抛错', () => {
  assert.match(revealErrorInfo(new Error('Network Error')).message, /无法连接到服务端/)
  assert.match(revealErrorInfo(undefined).message, /无法连接到服务端/)
  assert.match(revealErrorInfo(null).message, /无法连接到服务端/)
})

test('formatLockRemaining：不足一分钟、向上取整、非法值返回空串', () => {
  assert.equal(formatLockRemaining(30), '不到 1 分钟')
  assert.equal(formatLockRemaining(60), '约 1 分钟')
  assert.equal(formatLockRemaining(61), '约 2 分钟')
  assert.equal(formatLockRemaining(0), '')
  assert.equal(formatLockRemaining(-5), '')
  assert.equal(formatLockRemaining('12'), '')
  assert.equal(formatLockRemaining(Number.NaN), '')
})

test('令牌在屏幕上停留的时长是 60 秒', () => {
  assert.equal(REVEAL_AUTO_HIDE_MS, 60_000)
})
