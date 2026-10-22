// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  COUNTDOWN_OPTIONS,
  MAX_CHECKS_OPTIONS,
  PROMPT_WAIT_OPTIONS,
  buildExecChip,
  checksPressure,
  countdownOptionLabel,
  effectiveCountdown,
  effectiveCountdownSeconds,
  effectiveMaxChecks,
  effectivePromptWait,
  execChipTitle,
  execStateKind,
  formatMaxChecks,
  maxChecksOptionLabel,
} from './execChip'
import type { SessionTimerResult } from './sessionTimer'

const timer = (prefix: string, text: string, isCountdown = false): SessionTimerResult => ({ prefix, text, isCountdown })

describe('formatMaxChecks', () => {
  it('缺失按默认 24，0 与负数是不限', () => {
    assert.equal(formatMaxChecks(undefined), '24')
    assert.equal(formatMaxChecks(null), '24')
    assert.equal(formatMaxChecks(0), '∞')
    assert.equal(formatMaxChecks(-1), '∞')
    assert.equal(formatMaxChecks(6), '6')
  })
  it('菜单里的写法', () => {
    assert.equal(maxChecksOptionLabel(12), '12 次')
    assert.equal(maxChecksOptionLabel(0), '∞ 不限')
  })
})

describe('生效值：会话优先，其次全局设置，最后默认', () => {
  it('倒计时：0 是有效值，不能被当成缺失', () => {
    assert.equal(effectiveCountdown({ wait_countdown_minutes: 0 }, { defaultWaitCountdownMinutes: 2 }), 0)
    assert.equal(effectiveCountdown({}, { defaultWaitCountdownMinutes: 1 }), 1)
    assert.equal(effectiveCountdown(null, null), 2)
    assert.equal(effectiveCountdown({ wait_countdown_minutes: null }, undefined), 2)
  })
  it('空回执上限：0 是「不限」，也是有效值', () => {
    assert.equal(effectiveMaxChecks({ max_no_feedback_checks: 0 }, { maxNoFeedbackChecks: 24 }), 0)
    assert.equal(effectiveMaxChecks({}, { maxNoFeedbackChecks: 12 }), 12)
    assert.equal(effectiveMaxChecks(undefined, undefined), 24)
  })
  it('提示词等待：必须为正数，0 / 缺失时往下回落', () => {
    assert.equal(effectivePromptWait({ prompt_wait_minutes: 10 }, { promptWaitMinutes: 2 }), 10)
    assert.equal(effectivePromptWait({ prompt_wait_minutes: 0 }, { promptWaitMinutes: 5 }), 5)
    assert.equal(effectivePromptWait({}, { promptWaitMinutes: 0 }), 2)
    assert.equal(effectivePromptWait(null, null), 2)
  })
})

describe('execStateKind', () => {
  it('按前缀归类', () => {
    assert.equal(execStateKind(timer('剩余', '01:00', true)), 'countdown')
    assert.equal(execStateKind(timer('剩余', '02:30')), 'countdown')
    assert.equal(execStateKind(timer('等待', '00:10')), 'waiting')
    assert.equal(execStateKind(timer('执行', '00:10')), 'executing')
    assert.equal(execStateKind(timer('', '')), 'idle')
    assert.equal(execStateKind(null), 'idle')
  })
  it('「待提取」已经取消：不会被识别成任何活动状态', () => {
    assert.equal(execStateKind(timer('待提取', '00:10')), 'idle')
  })
})

describe('checksPressure', () => {
  it('不限或还早 → ok；3/4 以上 → warn；用满 → full', () => {
    assert.equal(checksPressure(5, 0), 'ok')
    assert.equal(checksPressure(0, 6), 'ok')
    assert.equal(checksPressure(3, 6), 'ok')
    assert.equal(checksPressure(5, 6), 'warn')
    assert.equal(checksPressure(6, 6), 'full')
    assert.equal(checksPressure(9, 6), 'full')
  })
})

describe('buildExecChip：合并成一行简写', () => {
  it('例子：22:28 · 1/6 · 5m', () => {
    const v = buildExecChip({ no_feedback_checks: 1, max_no_feedback_checks: 6, prompt_wait_minutes: 5 }, {}, timer('执行', '22:28'))
    assert.equal(v.text, '22:28 · 1/6 · 5m')
    assert.equal(v.kind, 'executing')
  })

  it('不限上限显示 ∞', () => {
    const v = buildExecChip({ no_feedback_checks: 3, max_no_feedback_checks: 0, prompt_wait_minutes: 2 }, {}, timer('等待', '01:05'))
    assert.equal(v.text, '01:05 · 3/∞ · 2m')
    assert.equal(v.pressure, 'ok')
  })

  it('会话没有的字段回落到全局设置', () => {
    const v = buildExecChip({ status: 'pending' }, { maxNoFeedbackChecks: 12, promptWaitMinutes: 10 }, timer('剩余', '01:30', true))
    assert.equal(v.text, '01:30 · 0/12 · 10m')
    assert.equal(v.kind, 'countdown')
  })

  it('没有计时（会话已结束）用占位 --:--', () => {
    const v = buildExecChip({ status: 'cancelled' }, {}, timer('', ''))
    assert.equal(v.time, '--:--')
    assert.equal(v.kind, 'idle')
    assert.equal(v.text, '--:-- · 0/24 · 2m')
  })

  it('没有会话与计时也不报错', () => {
    const v = buildExecChip(null, null, null)
    assert.equal(v.text, '--:-- · 0/24 · 2m')
  })

  it('空回执次数不会是负数或小数', () => {
    assert.equal(buildExecChip({ no_feedback_checks: -3 }, {}, null).used, 0)
    assert.equal(buildExecChip({ no_feedback_checks: 2.9 }, {}, null).used, 2)
  })
})

describe('execChipTitle：悬停说明', () => {
  it('倒计时中', () => {
    const t = timer('剩余', '01:30', true)
    const s = { status: 'pending', no_feedback_checks: 1, max_no_feedback_checks: 6, prompt_wait_minutes: 5 }
    const text = execChipTitle(buildExecChip(s, {}, t), s, t)
    assert.match(text, /挂着等你回复/)
    assert.match(text, /还剩 01:30/)
    assert.match(text, /空回执 1\/6/)
    assert.match(text, /提示词等待 5 分钟/)
    assert.match(text, /右键或点击调整/)
  })

  it('倒计时走完转正计时', () => {
    const t = timer('剩余', '03:10')
    const s = { status: 'pending' }
    assert.match(execChipTitle(buildExecChip(s, {}, t), s, t), /已等 03:10/)
  })

  it('回复已提交但智能体还没来取：说明是从它最近一次空回执算起', () => {
    const t = timer('等待', '04:00')
    const s = { status: 'completed', consumed_by_ai: false }
    const text = execChipTitle(buildExecChip(s, {}, t), s, t)
    assert.match(text, /回复已提交，智能体还没来取/)
    assert.match(text, /最近一次空回执已过 04:00/)
    assert.doesNotMatch(text, /待提取/)
  })

  it('休眠、执行、已结束', () => {
    const w = timer('等待', '00:20')
    assert.match(execChipTitle(buildExecChip({ status: 'pending' }, {}, w), { status: 'pending' }, w), /两次轮询之间休眠/)
    const e = timer('执行', '10:00')
    assert.match(execChipTitle(buildExecChip({ status: 'completed', consumed_by_ai: true }, {}, e), { status: 'completed' }, e), /取走你的回复，执行了 10:00/)
    const i = timer('', '')
    assert.match(execChipTitle(buildExecChip({ status: 'cancelled' }, {}, i), { status: 'cancelled' }, i), /已经结束/)
  })

  it('执行中：回复躺了一阵才被取走时，说明间隔，并写明计时从取走算起', () => {
    const e = timer('执行', '07:29')
    const s = {
      status: 'completed',
      consumed_by_ai: true,
      responded_at: '2026-10-12T11:10:00Z',
      consumed_at: '2026-10-12T11:21:02Z',
    }
    const text = execChipTitle(buildExecChip(s, {}, e), s, e)
    assert.match(text, /执行了 07:29/)
    assert.match(text, /隔了 11:02 才来取/)
    assert.match(text, /计时从取走时算起/)
  })

  it('执行中：秒取、历史会话（没有 responded_at）不加间隔说明', () => {
    const e = timer('执行', '01:00')
    const quick = { status: 'completed', consumed_by_ai: true, responded_at: '2026-10-12T11:10:00Z', consumed_at: '2026-10-12T11:10:03Z' }
    assert.doesNotMatch(execChipTitle(buildExecChip(quick, {}, e), quick, e), /才来取/)
    const legacy = { status: 'completed', consumed_by_ai: true, consumed_at: '2026-10-12T11:21:02Z' }
    assert.doesNotMatch(execChipTitle(buildExecChip(legacy, {}, e), legacy, e), /才来取/)
  })

  it('不限上限', () => {
    const t = timer('等待', '00:20')
    const s = { status: 'pending', no_feedback_checks: 4, max_no_feedback_checks: 0 }
    const text = execChipTitle(buildExecChip(s, {}, t), s, t)
    assert.match(text, /空回执 4 次（不限上限）/)
    assert.doesNotMatch(text, /我的状态/)
  })
})

describe('菜单可选值', () => {
  it('提示词等待仍是 1 / 2 / 5 / 10 分钟（倒计时与空回执上限在 #121 改了，见下）', () => {
    assert.deepEqual([...PROMPT_WAIT_OPTIONS], [1, 2, 5, 10])
  })
})

describe('秒级挂起倒计时与新选项（#121）', () => {
  it('选项：秒回 / 30 秒 / 1 分钟；空回执上限 3 / 6 / 不限', () => {
    assert.deepEqual([...COUNTDOWN_OPTIONS], [0, 30, 60])
    assert.deepEqual([...MAX_CHECKS_OPTIONS], [3, 6, 0])
    assert.equal(countdownOptionLabel(0), '秒回')
    assert.equal(countdownOptionLabel(30), '30秒')
    assert.equal(countdownOptionLabel(60), '1分钟')
  })
  it('effectiveCountdownSeconds：会话秒 > 会话分钟 > 全局秒 > 全局分钟 > 60', () => {
    assert.equal(effectiveCountdownSeconds({ wait_countdown_seconds: 0, wait_countdown_minutes: 2 }, {}), 0)
    assert.equal(effectiveCountdownSeconds({ wait_countdown_seconds: -1, wait_countdown_minutes: 2 }, {}), 120)
    assert.equal(effectiveCountdownSeconds({}, { defaultWaitCountdownSeconds: 30, defaultWaitCountdownMinutes: 2 }), 30)
    assert.equal(effectiveCountdownSeconds({}, { defaultWaitCountdownSeconds: -1, defaultWaitCountdownMinutes: 1 }), 60)
    assert.equal(effectiveCountdownSeconds(null, undefined), 60)
  })
})
