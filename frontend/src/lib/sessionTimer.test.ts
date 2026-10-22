// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  computeSessionTimer,
  formatClock,
  formatTimerDuration,
  pickupLagSeconds,
  receivedTitle,
  replySentAt,
  type SessionTimerRaw,
} from './sessionTimer'

const T0 = Date.parse('2026-10-04T12:00:00Z')
const at = (offsetSec: number) => new Date(T0 + offsetSec * 1000).toISOString()

describe('formatTimerDuration', () => {
  it('分:秒 / 时:分:秒 / 天·时:分', () => {
    assert.equal(formatTimerDuration(0), '00:00')
    assert.equal(formatTimerDuration(65), '01:05')
    assert.equal(formatTimerDuration(3661), '01:01:01')
    assert.equal(formatTimerDuration(86400 + 3600 * 2 + 60 * 5), '1d·02:05')
  })
  it('负数按 0', () => {
    assert.equal(formatTimerDuration(-5), '00:00')
  })
})

describe('computeSessionTimer：等你回复（pending）', () => {
  it('MCP 长轮询挂着：倒计时走完之前是「剩余」，走完转为正计时（仍是「剩余」）', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0), wait_countdown_minutes: 2 }
    assert.deepEqual(computeSessionTimer(s, T0 + 30_000), { text: '01:30', prefix: '剩余', isCountdown: true })
    assert.deepEqual(computeSessionTimer(s, T0 + 150_000), { text: '02:30', prefix: '剩余', isCountdown: false })
  })

  it('倒计时设为 0：不倒计时，直接正计时', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0), wait_countdown_minutes: 0 }
    assert.deepEqual(computeSessionTimer(s, T0 + 45_000), { text: '00:45', prefix: '剩余', isCountdown: false })
  })

  it('会话没写倒计时分钟时用兜底值', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0) }
    assert.equal(computeSessionTimer(s, T0 + 30_000, 1).text, '00:30')
    assert.equal(computeSessionTimer(s, T0 + 30_000, 1).isCountdown, true)
  })

  it('没有人在轮询：从最近一次空回执起算「等待」', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: false, last_keepalive_at: at(-90) }
    assert.deepEqual(computeSessionTimer(s, T0), { text: '01:30', prefix: '等待', isCountdown: false })
  })

  it('没有空回执记录：等待 00:00', () => {
    assert.deepEqual(computeSessionTimer({ status: 'pending' }, T0), { text: '00:00', prefix: '等待', isCountdown: false })
  })
})

describe('computeSessionTimer：已提交回复（completed）', () => {
  it('智能体还没取走：仍是「等待」，从它最近一次空回执算起，而不是从你提交回复的时刻', () => {
    const s: SessionTimerRaw = { status: 'completed', consumed_by_ai: false, last_keepalive_at: at(-300), updated_at: at(-20) }
    const r = computeSessionTimer(s, T0)
    assert.deepEqual(r, { text: '05:00', prefix: '等待', isCountdown: false })
  })

  it('永远不再出现「待提取」', () => {
    const cases: SessionTimerRaw[] = [
      { status: 'completed', consumed_by_ai: false },
      { status: 'completed', consumed_by_ai: false, updated_at: at(-10) },
      { status: 'completed', consumed_by_ai: false, last_keepalive_at: at(-10) },
      { status: 'completed', consumed_by_ai: false, created_at: at(-10) },
    ]
    for (const s of cases) assert.notEqual(computeSessionTimer(s, T0).prefix, '待提取')
  })

  it('没有空回执记录（比如服务刚重启）：退回到回复提交的时间，再退回到创建时间', () => {
    assert.equal(computeSessionTimer({ status: 'completed', consumed_by_ai: false, updated_at: at(-42), created_at: at(-600) }, T0).text, '00:42')
    assert.equal(computeSessionTimer({ status: 'completed', consumed_by_ai: false, created_at: at(-600) }, T0).text, '10:00')
  })

  it('什么时间都没有：不报错，从 0 开始', () => {
    assert.deepEqual(computeSessionTimer({ status: 'completed', consumed_by_ai: false }, T0), { text: '00:00', prefix: '等待', isCountdown: false })
  })

  it('时间字符串无法解析时按缺失处理，不出现 NaN', () => {
    const r = computeSessionTimer({ status: 'completed', consumed_by_ai: false, last_keepalive_at: 'not-a-date', updated_at: at(-15) }, T0)
    assert.equal(r.text, '00:15')
    assert.ok(!r.text.includes('NaN'))
  })

  it('智能体取走之后是「执行」，从取走的时刻算起', () => {
    const s: SessionTimerRaw = { status: 'completed', consumed_by_ai: true, consumed_at: at(-125), updated_at: at(-600), last_keepalive_at: at(-900) }
    assert.deepEqual(computeSessionTimer(s, T0), { text: '02:05', prefix: '执行', isCountdown: false })
  })

  it('取走时刻缺失时用 updated_at 兜底', () => {
    assert.equal(computeSessionTimer({ status: 'completed', consumed_by_ai: true, updated_at: at(-30) }, T0).text, '00:30')
  })
})

describe('computeSessionTimer：其余', () => {
  it('已取消 / 已超时 / 已归档没有计时', () => {
    for (const status of ['cancelled', 'timeout', 'archived']) {
      assert.deepEqual(computeSessionTimer({ status }, T0), { text: '', prefix: '', isCountdown: false })
    }
  })

  it('没有会话：执行 00:00（沿用原行为）', () => {
    assert.deepEqual(computeSessionTimer(null, T0), { text: '00:00', prefix: '执行', isCountdown: false })
    assert.deepEqual(computeSessionTimer(undefined, T0), { text: '00:00', prefix: '执行', isCountdown: false })
  })
})

describe('回复时间：你提交的时刻 ≠ 智能体取走的时刻', () => {
  it('replySentAt 优先 responded_at；历史会话回退 updated_at、created_at', () => {
    assert.equal(replySentAt({ responded_at: at(-700), updated_at: at(-60), created_at: at(-900) }), at(-700))
    assert.equal(replySentAt({ updated_at: at(-60), created_at: at(-900) }), at(-60))
    assert.equal(replySentAt({ created_at: at(-900) }), at(-900))
    assert.equal(replySentAt(null), undefined)
    assert.equal(replySentAt({}), undefined)
  })

  it('取走会刷新 updated_at，但显示的回复时间不受影响（这正是旧版本显示成取走时间的缘由）', () => {
    const before = { responded_at: at(-660), updated_at: at(-660), consumed_by_ai: false }
    const after = { ...before, updated_at: at(0), consumed_by_ai: true, consumed_at: at(0) }
    assert.equal(replySentAt(before), replySentAt(after))
  })

  it('pickupLagSeconds：只认 responded_at 与 consumed_at', () => {
    assert.equal(pickupLagSeconds({ consumed_by_ai: true, responded_at: at(-662), consumed_at: at(0) }), 662)
    // 还没被取走 / 缺任一时间（历史会话）/ 时间倒挂（时钟偏差）：没有间隔可说
    assert.equal(pickupLagSeconds({ consumed_by_ai: false, responded_at: at(-60), consumed_at: at(0) }), null)
    assert.equal(pickupLagSeconds({ consumed_by_ai: true, consumed_at: at(0), updated_at: at(0) }), null)
    assert.equal(pickupLagSeconds({ consumed_by_ai: true, responded_at: at(-60) }), null)
    assert.equal(pickupLagSeconds({ consumed_by_ai: true, responded_at: at(30), consumed_at: at(0) }), null)
    assert.equal(pickupLagSeconds({ consumed_by_ai: true, responded_at: 'bad', consumed_at: at(0) }), null)
    assert.equal(pickupLagSeconds(undefined), null)
  })

  it('formatClock：本地 HH:mm:ss，空值与坏值返回空串', () => {
    assert.match(formatClock(at(0)), /^\d{2}:\d{2}:\d{2}$/)
    assert.equal(formatClock(undefined), '')
    assert.equal(formatClock('nope'), '')
  })

  it('receivedTitle：隔了多久 / 几乎立刻 / 只知道已取走', () => {
    assert.match(receivedTitle({ consumed_by_ai: true, responded_at: at(-662), consumed_at: at(0) }), /距你提交回复隔了 11:02/)
    assert.match(receivedTitle({ consumed_by_ai: true, responded_at: at(-2), consumed_at: at(0) }), /几乎立刻取走/)
    assert.match(receivedTitle({ consumed_by_ai: true, consumed_at: at(0) }), /智能体于 \d{2}:\d{2}:\d{2} 取走这条回复/)
    assert.equal(receivedTitle({ consumed_by_ai: true }), '智能体已取走这条回复')
  })
})

describe('computeSessionTimer：秒级倒计时与服务端实际挂起时长（#121）', () => {
  it('wait_countdown_seconds 优先于分钟：30 秒', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0), wait_countdown_minutes: 2, wait_countdown_seconds: 30 }
    assert.deepEqual(computeSessionTimer(s, T0 + 10_000), { text: '00:20', prefix: '剩余', isCountdown: true })
    assert.equal(computeSessionTimer(s, T0 + 40_000).isCountdown, false)
  })
  it('秒回（0 秒）：直接正计时', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0), wait_countdown_seconds: 0 }
    assert.equal(computeSessionTimer(s, T0 + 5_000).isCountdown, false)
  })
  it('服务端登记的挂起时长（含「+1 分钟」延长）最准', () => {
    const s: SessionTimerRaw = { status: 'pending', is_mcp_active: true, mcp_active_at: at(0), wait_countdown_seconds: 30, mcp_timeout_seconds: 90 }
    assert.deepEqual(computeSessionTimer(s, T0 + 60_000), { text: '00:30', prefix: '剩余', isCountdown: true })
  })
})
