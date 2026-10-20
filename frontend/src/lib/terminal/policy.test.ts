// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { MAX_TICKET_FAILURES, SETTLE, backoff, classifyClose, classifyTicketFailure, lagDelay, settleVerdict, slowDelay } from './policy'

const noJitter = () => 0.5 // 抖动系数恰为 1

describe('backoff', () => {
  it('指数增长并在 10 秒封顶', () => {
    assert.equal(backoff(0, noJitter), 500)
    assert.equal(backoff(1, noJitter), 1000)
    assert.equal(backoff(2, noJitter), 2000)
    assert.equal(backoff(4, noJitter), 8000)
    assert.equal(backoff(5, noJitter), 10000)
    assert.equal(backoff(1000, noJitter), 10000)
  })

  it('抖动不超过 ±20%', () => {
    assert.equal(backoff(0, () => 0), 400)
    assert.equal(backoff(0, () => 1), 600)
  })

  it('负数与小数次数不会产生 NaN 或负延迟', () => {
    assert.equal(backoff(-3, noJitter), 500)
    assert.equal(backoff(1.9, noJitter), 1000)
  })

  it('慢速重试不低于 5 秒', () => {
    assert.equal(slowDelay(0, noJitter), 5000)
    assert.equal(slowDelay(5, noJitter), 10000)
  })
})

describe('classifyClose', () => {
  it('总闸关闭 / 主机停用：不再重连', () => {
    assert.equal(classifyClose(4403, 0).kind, 'fail')
    assert.equal(classifyClose(4409, 0).kind, 'fail')
  })

  it('会话结束：终止且不重连', () => {
    const a = classifyClose(4410, 0)
    assert.equal(a.kind, 'end')
  })

  it('渲染停滞 / 被取代：很快重试', () => {
    const a = classifyClose(4412, 3)
    assert.equal(a.kind, 'retry')
    assert.ok(a.kind === 'retry' && a.delayMs <= 300)
    const b = classifyClose(4413, 0)
    assert.ok(b.kind === 'retry' && b.delayMs <= 500)
  })

  it('限流 / 找不到：慢速重试', () => {
    for (const code of [4429, 4404]) {
      const a = classifyClose(code, 0, '', noJitter)
      assert.ok(a.kind === 'retry' && a.delayMs >= 5000, `code ${code}`)
    }
  })

  it('凭据失效首次立即重试，连续失败则退避', () => {
    const first = classifyClose(4401, 0, '', noJitter)
    assert.ok(first.kind === 'retry' && first.delayMs <= 200)
    const later = classifyClose(4401, 3, '', noJitter)
    assert.ok(later.kind === 'retry' && later.delayMs >= 2000)
  })

  it('网络异常断线（1006 / 1001 / 未知）：指数退避重连', () => {
    for (const code of [1006, 1001, 1011, 4999]) {
      const a = classifyClose(code, 2, '', noJitter)
      assert.ok(a.kind === 'retry' && a.delayMs === 2000, `code ${code}`)
    }
  })

  it('服务端给出的原因优先作为提示', () => {
    const a = classifyClose(4403, 0, '自定义原因')
    assert.ok(a.kind === 'fail' && a.message === '自定义原因')
  })
})

describe('classifyTicketFailure', () => {
  it('用户取消解锁：失败并提示需要解锁', () => {
    const a = classifyTicketFailure({ cancelled: true }, 0, 1)
    assert.equal(a.kind, 'fail')
  })

  it('会话不存在：视为已结束', () => {
    assert.equal(classifyTicketFailure({ code: 'session_not_found', status: 404 }, 0, 1).kind, 'end')
  })

  it('主机离线 / 限流：慢速重试', () => {
    for (const code of ['host_offline', 'busy']) {
      const a = classifyTicketFailure({ code }, 0, 1, noJitter)
      assert.ok(a.kind === 'retry' && a.delayMs >= 5000, code)
    }
  })

  it('总闸关闭与登录失效：不重试', () => {
    assert.equal(classifyTicketFailure({ code: 'terminal_disabled' }, 0, 1).kind, 'fail')
    assert.equal(classifyTicketFailure({ status: 401 }, 0, 1).kind, 'fail')
    assert.equal(classifyTicketFailure({ code: 'auth_required', status: 401 }, 0, 1).kind, 'fail')
    assert.equal(classifyTicketFailure({ code: 'unlock_required', status: 403 }, 0, 1).kind, 'fail')
  })

  it('网络错误：退避重试，连续失败达到上限后暂停（标记为暂时性，环境好转时可自动恢复）', () => {
    const a = classifyTicketFailure({ message: 'Network Error' }, 1, 1, noJitter)
    assert.ok(a.kind === 'retry' && a.delayMs === 1000)
    // 原先 6 次就放弃，服务端重启 / 反代重载稍久一点标签页就停摆；现在能撑过几分钟的中断。
    assert.equal(classifyTicketFailure({ message: 'Network Error' }, 6, 6).kind, 'retry')
    assert.equal(classifyTicketFailure({ message: 'Network Error' }, 29, MAX_TICKET_FAILURES - 1).kind, 'retry')
    const last = classifyTicketFailure({ message: 'Network Error' }, 30, MAX_TICKET_FAILURES)
    assert.ok(last.kind === 'fail' && last.transient === true)
  })

  it('上限所覆盖的时长：退避封顶 10 秒，约 4 分半', () => {
    let total = 0
    for (let i = 0; i < MAX_TICKET_FAILURES - 1; i++) total += backoff(i, noJitter)
    assert.ok(total >= 240_000 && total <= 300_000, `累计等待 ${total}ms`)
  })

  it('非暂时性的失败不带 transient 标记（不会被网络恢复悄悄重连）', () => {
    for (const f of [{ cancelled: true }, { code: 'terminal_disabled' }, { code: 'auth_required' }, { code: 'unlock_required' }, { status: 401 }, { status: 403 }]) {
      const a = classifyTicketFailure(f, 0, 1)
      assert.ok(a.kind === 'fail' && !a.transient, JSON.stringify(f))
    }
  })

  it('5xx 同样按可恢复处理', () => {
    assert.equal(classifyTicketFailure({ status: 503 }, 0, 1).kind, 'retry')
  })
})

describe('跟不上输出（4414）', () => {
  it('lagDelay：1.5 秒起逐次翻倍，6 秒封顶，抖动不超过 ±10%', () => {
    assert.deepEqual([1, 2, 3, 4, 5, 9].map(n => lagDelay(n, noJitter)), [1500, 3000, 6000, 6000, 6000, 6000])
    assert.equal(lagDelay(1, () => 0), 1350)
    assert.equal(lagDelay(1, () => 1), 1650)
    assert.equal(lagDelay(0, noJitter), 1500)
    assert.equal(lagDelay(-4, noJitter), 1500)
  })

  it('classifyClose(4414)：重试，延迟按「连续被断开次数」算，文案说明已跳到最新输出', () => {
    const a = classifyClose(4414, 1, 'lagging', noJitter)
    assert.equal(a.kind, 'retry')
    if (a.kind === 'retry') {
      assert.equal(a.delayMs, 1500)
      assert.match(a.message, /跳到最新/)
    }
    const b = classifyClose(4414, 3, '', noJitter)
    assert.equal(b.kind === 'retry' && b.delayMs, 6000)
  })
})

describe('settleVerdict', () => {
  it('一个周期内收到的实时输出不到 12 KiB：平静，遮罩可以揭开', () => {
    assert.equal(settleVerdict(0, 100), 'calm')
    assert.equal(settleVerdict(SETTLE.busyBytes - 1, 100), 'calm')
  })

  it('达到 12 KiB 还很忙，继续盖着', () => {
    assert.equal(settleVerdict(SETTLE.busyBytes, 100), 'busy')
    assert.equal(settleVerdict(5 * 1024 * 1024, 2_900), 'busy')
  })

  it('一直很忙也有上限：满 3 秒就交给服务端的追赶保护', () => {
    assert.equal(settleVerdict(SETTLE.busyBytes, SETTLE.maxMs), 'cap')
    assert.equal(settleVerdict(SETTLE.busyBytes * 10, SETTLE.maxMs + 500), 'cap')
  })

  it('到了上限但这一段已经平静：算平静而不是封顶', () => {
    assert.equal(settleVerdict(0, SETTLE.maxMs), 'calm')
  })
})
