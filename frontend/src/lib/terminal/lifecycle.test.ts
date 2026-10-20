// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  IDLE_OPTIONS,
  canRebuild,
  endedNote,
  forceMajeureNote,
  formatIdle,
  idleChoiceOf,
  idleSummary,
  lifecycleBadge,
  reclaimAt,
  reclaimEta,
} from './lifecycle'

const HOUR = 3600
const DAY = 86400

describe('空闲回收取值', () => {
  it('界面提供的选项与服务端允许的范围一致，并以空闲不回收开头', () => {
    assert.equal(IDLE_OPTIONS[0].value, 0)
    assert.equal(IDLE_OPTIONS[0].label, '空闲不回收')
    for (const o of IDLE_OPTIONS.slice(1)) {
      assert.ok(o.value >= HOUR && o.value <= 30 * DAY, `${o.label} 超出 1 小时 ~ 30 天`)
    }
    assert.equal(new Set(IDLE_OPTIONS.map(o => o.value)).size, IDLE_OPTIONS.length)
  })

  it('formatIdle 取能整除的最大单位', () => {
    assert.equal(formatIdle(30 * DAY), '30 天')
    assert.equal(formatIdle(DAY), '1 天')
    assert.equal(formatIdle(36 * HOUR), '36 小时')
    assert.equal(formatIdle(HOUR), '1 小时')
    assert.equal(formatIdle(90 * 60), '90 分钟')
    assert.equal(formatIdle(45), '45 秒')
    assert.equal(formatIdle(-3), '0 秒')
  })

  it('idleSummary 区分空闲不回收与空闲回收', () => {
    assert.equal(idleSummary(0), '空闲不回收')
    assert.equal(idleSummary(DAY), '空闲 1 天后回收')
  })

  it('idleChoiceOf：单独设置过取该值，否则沿用主机默认', () => {
    assert.equal(idleChoiceOf({ idle: DAY, idle_own: true }), DAY)
    assert.equal(idleChoiceOf({ idle: 0, idle_own: true }), 0)
    assert.equal(idleChoiceOf({ idle: DAY, idle_own: false }), 'inherit')
    assert.equal(idleChoiceOf({}), 'inherit')
  })
})

describe('回收时刻', () => {
  const base = { state: 'running', idle: DAY, active: 1_000_000, attached: 0 }

  it('没人连接时：最后交互 + 空闲时长', () => {
    assert.equal(reclaimAt(base), 1_000_000 + DAY)
  })

  it('空闲不回收、已退出、有人连接、没有活动时间：都不存在回收时刻', () => {
    assert.equal(reclaimAt({ ...base, idle: 0 }), null)
    assert.equal(reclaimAt({ ...base, state: 'exited' }), null)
    assert.equal(reclaimAt({ ...base, attached: 1 }), null)
    assert.equal(reclaimAt({ ...base, active: 0 }), null)
  })

  it('reclaimEta 用口语表示剩余时间', () => {
    const at = 1_000_000 + DAY
    assert.equal(reclaimEta(base, at + 5), '随时会被回收')
    assert.equal(reclaimEta(base, at - 30), '不到 2 分钟后回收')
    assert.equal(reclaimEta(base, at - 20 * 60), '约 20 分钟后回收')
    assert.equal(reclaimEta(base, at - 5 * HOUR), '约 5 小时后回收')
    assert.equal(reclaimEta(base, at - 3 * DAY), '约 3 天后回收')
    assert.equal(reclaimEta({ ...base, idle: 0 }, at), '')
  })
})

describe('生命周期徽标', () => {
  it('默认空闲不回收：说明来源（沿用主机默认 / 单独设置）', () => {
    const inherit = lifecycleBadge({ state: 'running', idle: 0, idle_own: false }, 0)!
    assert.equal(inherit.text, '空闲不回收')
    assert.equal(inherit.tone, 'ok')
    assert.match(inherit.title, /沿用这台主机的默认设置/)
    const own = lifecycleBadge({ state: 'running', idle: 0, idle_own: true }, 0)!
    assert.match(own.title, /单独设置/)
  })

  it('设置了空闲回收：显示时长，并按是否有人连接说明计时状态', () => {
    const idle = lifecycleBadge({ state: 'running', idle: 7 * DAY, idle_own: true, attached: 0, active: 1000 }, 1000 + 2 * DAY)!
    assert.equal(idle.text, '空闲 7 天回收')
    assert.equal(idle.tone, 'info')
    assert.match(idle.title, /约 5 天后回收/)
    const watched = lifecycleBadge({ state: 'running', idle: HOUR, attached: 2, active: 5000 }, 5000)!
    assert.match(watched.title, /计时尚未开始/)
  })

  it('已退出的会话没有生命周期徽标', () => {
    assert.equal(lifecycleBadge({ state: 'exited', idle: DAY }, 0), null)
  })
})

describe('不可抗力与结束原因', () => {
  it('主机重启 / 守护进程停止 / 意外退出 / 消失都属于不可抗力，可以重建', () => {
    for (const r of ['host_reboot', 'holder_stopped', 'holder_exit', 'gone']) {
      assert.ok(forceMajeureNote(r), r)
      assert.ok(canRebuild(r), r)
    }
  })

  it('正常结束、被终止、空闲回收不是不可抗力，不提供重建', () => {
    for (const r of ['exit', 'killed', 'idle_ttl', 'policy', undefined, '']) {
      assert.equal(forceMajeureNote(r), null, String(r))
      assert.equal(canRebuild(r), false, String(r))
    }
  })

  it('endedNote 把原因说成人话', () => {
    assert.match(endedNote('host_reboot'), /主机重启/)
    assert.match(endedNote('idle_ttl'), /长时间没有人使用/)
    assert.match(endedNote('killed'), /被终止/)
    assert.match(endedNote('policy'), /总闸/)
    assert.match(endedNote('exit', 130), /退出码 130/)
    assert.equal(endedNote('exit', 0), '里面的程序已退出。')
    assert.equal(endedNote(undefined), '会话已结束。')
  })
})
