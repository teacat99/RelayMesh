// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  BOARD_STALE_CHOICES,
  BOARD_STALE_FALLBACK_MINUTES,
  MUTE_PRESETS,
  PREVIEW_MODES,
  boardStaleChoices,
  boardStaleLabel,
  boardStaleMs,
  boardStaleSelected,
  defaultPolicy,
  describePolicy,
  emptyPrefs,
  formatMuteUntil,
  isMuted,
  muteUntilISO,
  mutedUntilMs,
  normalizePrefs,
  policyOf,
  workflowConversation,
} from './notifyPrefs'

// 用本地时间构造，断言「口语化的钟点」时不受运行机器的时区影响。
const NOW = new Date(2026, 9, 14, 13, 0, 0).getTime()
const at = (h: number, m = 0, dayOffset = 0) => new Date(2026, 9, 14 + dayOffset, h, m, 0).getTime()

describe('normalizePrefs', () => {
  it('空值 / 非对象 / 缺字段：给出默认偏好，不抛错', () => {
    for (const raw of [null, undefined, 42, 'x', [], {}]) {
      const p = normalizePrefs(raw)
      assert.equal(p.dnd, false)
      assert.equal(p.ntfy_preview, 'auto')
      assert.deepEqual(p.conversations, {})
    }
  })

  it('保留合法字段，丢弃不认识的取值', () => {
    const p = normalizePrefs({
      dnd: true,
      ntfy_preview: 'never',
      conversations: {
        'wf:a': { policy: 'muted', muted_until: '2026-10-14T06:00:00Z' },
        'wf:b': { policy: 'bogus' },
        'wf:c': 'oops',
        'grp:g': { policy: 'all' },
        'wf:d': { policy: 'mentions', muted_until: '2026-10-14T06:00:00Z' }, // 到期时间只属于静音
      },
    })
    assert.equal(p.dnd, true)
    assert.equal(p.ntfy_preview, 'never')
    assert.deepEqual(Object.keys(p.conversations).sort(), ['grp:g', 'wf:a', 'wf:d'])
    assert.equal(p.conversations['wf:a'].muted_until, '2026-10-14T06:00:00Z')
    assert.equal(p.conversations['wf:d'].muted_until, undefined)
  })

  it('预览模式不认识的取值回到 auto；dnd 只认布尔 true', () => {
    assert.equal(normalizePrefs({ ntfy_preview: 'sometimes' }).ntfy_preview, 'auto')
    assert.equal(normalizePrefs({ dnd: 'true' }).dnd, false)
    assert.equal(normalizePrefs({ dnd: 1 }).dnd, false)
  })
})

describe('会话键与默认策略', () => {
  it('workflowConversation：去空白；为空时没有会话键', () => {
    assert.equal(workflowConversation('relay'), 'wf:relay')
    assert.equal(workflowConversation('  relay '), 'wf:relay')
    assert.equal(workflowConversation(''), '')
    assert.equal(workflowConversation(undefined), '')
    assert.equal(workflowConversation(null), '')
  })

  it('defaultPolicy：群默认只在被 @ 时提醒，其余全部提醒', () => {
    assert.equal(defaultPolicy('wf:relay'), 'all')
    assert.equal(defaultPolicy('grp:team'), 'mentions')
    assert.equal(defaultPolicy('whatever'), 'all')
  })
})

describe('policyOf / isMuted / mutedUntilMs', () => {
  const prefs = normalizePrefs({
    conversations: {
      'wf:quiet': { policy: 'muted' },
      'wf:hour': { policy: 'muted', muted_until: new Date(NOW + 3_600_000).toISOString() },
      'wf:over': { policy: 'muted', muted_until: new Date(NOW - 1_000).toISOString() },
      'grp:loud': { policy: 'all' },
      'grp:quiet': { policy: 'muted' },
    },
  })

  it('没有记录：默认策略', () => {
    assert.equal(policyOf(prefs, 'wf:none', NOW), 'all')
    assert.equal(policyOf(prefs, 'grp:none', NOW), 'mentions')
  })

  it('一直静音与未到期的静音：静音', () => {
    assert.equal(policyOf(prefs, 'wf:quiet', NOW), 'muted')
    assert.equal(policyOf(prefs, 'wf:hour', NOW), 'muted')
    assert.equal(isMuted(prefs, 'wf:hour', NOW), true)
  })

  it('静音已到期：回到默认策略（单聊 all、群 mentions）', () => {
    assert.equal(policyOf(prefs, 'wf:over', NOW), 'all')
    assert.equal(isMuted(prefs, 'wf:over', NOW), false)
    const grp = normalizePrefs({ conversations: { 'grp:g': { policy: 'muted', muted_until: new Date(NOW - 1).toISOString() } } })
    assert.equal(policyOf(grp, 'grp:g', NOW), 'mentions')
  })

  it('群里显式设成「全部」仍然有效', () => {
    assert.equal(policyOf(prefs, 'grp:loud', NOW), 'all')
  })

  it('mutedUntilMs：只对带到期时间的静音返回时间戳', () => {
    assert.equal(mutedUntilMs(prefs, 'wf:quiet'), null)
    assert.equal(mutedUntilMs(prefs, 'wf:hour'), NOW + 3_600_000)
    assert.equal(mutedUntilMs(prefs, 'wf:none'), null)
    assert.equal(mutedUntilMs(prefs, 'grp:loud'), null)
  })

  it('到期时间解析不了：当作没有到期时间（一直静音），不抛错', () => {
    const bad = normalizePrefs({ conversations: { 'wf:x': { policy: 'muted', muted_until: '不是时间' } } })
    assert.equal(mutedUntilMs(bad, 'wf:x'), null)
    assert.equal(policyOf(bad, 'wf:x', NOW), 'muted')
  })
})

describe('静音预设', () => {
  it('三档：1 小时 / 8 小时 / 一直', () => {
    assert.deepEqual(MUTE_PRESETS.map(p => p.id), ['1h', '8h', 'forever'])
    assert.equal(MUTE_PRESETS[0].durationMs, 3_600_000)
    assert.equal(MUTE_PRESETS[1].durationMs, 8 * 3_600_000)
    assert.equal(MUTE_PRESETS[2].durationMs, null)
  })

  it('muteUntilISO：now + 时长；一直静音不带到期时间', () => {
    assert.equal(muteUntilISO(3_600_000, NOW), new Date(NOW + 3_600_000).toISOString())
    assert.equal(muteUntilISO(null, NOW), undefined)
  })

  it('预览模式选项覆盖服务端的三个取值', () => {
    assert.deepEqual(PREVIEW_MODES.map(m => m.value), ['auto', 'always', 'never'])
  })
})

describe('formatMuteUntil / describePolicy', () => {
  it('当天只写钟点，次日写「明天」，更远写月/日', () => {
    assert.equal(formatMuteUntil(at(14, 5), NOW), '14:05')
    assert.equal(formatMuteUntil(at(9, 0, 1), NOW), '明天 09:00')
    assert.equal(formatMuteUntil(at(9, 0, 3), NOW), '10/17 09:00')
  })

  it('默认策略没有说明；静音与「只在被 @ 时」有', () => {
    const prefs = normalizePrefs({
      conversations: {
        'wf:quiet': { policy: 'muted' },
        'wf:hour': { policy: 'muted', muted_until: new Date(at(14, 0)).toISOString() },
        'wf:at': { policy: 'mentions' },
      },
    })
    assert.equal(describePolicy(emptyPrefs(), 'wf:x', NOW), '')
    assert.equal(describePolicy(prefs, 'wf:quiet', NOW), '已静音（直到手动恢复）')
    assert.equal(describePolicy(prefs, 'wf:hour', NOW), '静音到 14:00')
    assert.equal(describePolicy(prefs, 'wf:at', NOW), '只在被 @ 时提醒')
    assert.equal(describePolicy(prefs, 'grp:none', NOW), '')
  })

  it('静音过期后说明消失', () => {
    const prefs = normalizePrefs({ conversations: { 'wf:hour': { policy: 'muted', muted_until: new Date(NOW - 1).toISOString() } } })
    assert.equal(describePolicy(prefs, 'wf:hour', NOW), '')
  })
})

// 「指派久无人接」的开关与阈值（#180 B3d）：默认值与生效值以服务端给的为准，缺字段 / 坏值都退回顶替值，绝不出现 NaN 或 0 分钟。
describe('久无人接：偏好整理', () => {
  it('没有这些字段（旧服务端、回滚到旧版本、第一次取到之前）：提醒开、2 小时', () => {
    for (const raw of [null, undefined, {}, { dnd: true }]) {
      const p = normalizePrefs(raw)
      assert.equal(p.board_stale_off, false)
      assert.equal(p.board_stale_minutes, 0)
      assert.equal(p.board_stale_after_minutes, BOARD_STALE_FALLBACK_MINUTES)
      assert.equal(p.board_stale_default_minutes, BOARD_STALE_FALLBACK_MINUTES)
    }
    assert.equal(BOARD_STALE_FALLBACK_MINUTES, 120)
    assert.equal(boardStaleMs(emptyPrefs()), 2 * 3600_000)
  })

  it('服务端给的值原样采纳；开关只认布尔 true', () => {
    const p = normalizePrefs({ board_stale_off: true, board_stale_minutes: 90, board_stale_after_minutes: 90, board_stale_default_minutes: 120 })
    assert.equal(p.board_stale_off, true)
    assert.equal(p.board_stale_minutes, 90)
    assert.equal(p.board_stale_after_minutes, 90)
    assert.equal(boardStaleMs(p), 90 * 60_000)
    assert.equal(normalizePrefs({ board_stale_off: 'true' }).board_stale_off, false)
    assert.equal(normalizePrefs({ board_stale_off: 1 }).board_stale_off, false)
  })

  it('出厂默认以服务端为准：默认改成 180 分钟后，没改过阈值的人生效值跟着变', () => {
    const p = normalizePrefs({ board_stale_after_minutes: 180, board_stale_default_minutes: 180 })
    assert.equal(p.board_stale_minutes, 0)
    assert.equal(p.board_stale_after_minutes, 180)
    assert.equal(p.board_stale_default_minutes, 180)
  })

  it('坏值（非整数、越界、字符串、NaN）一律退回：存值 → 0，生效值 → 默认', () => {
    for (const bad of [5, 29, 1441, 99999, -30, 90.5, '90', NaN, null, {}]) {
      const p = normalizePrefs({ board_stale_minutes: bad, board_stale_after_minutes: bad, board_stale_default_minutes: bad })
      assert.equal(p.board_stale_minutes, 0, `存值 ${String(bad)}`)
      assert.equal(p.board_stale_after_minutes, BOARD_STALE_FALLBACK_MINUTES, `生效值 ${String(bad)}`)
      assert.equal(p.board_stale_default_minutes, BOARD_STALE_FALLBACK_MINUTES, `默认值 ${String(bad)}`)
    }
    // 边界合法
    assert.equal(normalizePrefs({ board_stale_minutes: 30, board_stale_after_minutes: 30 }).board_stale_after_minutes, 30)
    assert.equal(normalizePrefs({ board_stale_minutes: 1440, board_stale_after_minutes: 1440 }).board_stale_after_minutes, 1440)
  })

  it('旧服务端只给了存值、没给生效值：生效值取存值', () => {
    assert.equal(normalizePrefs({ board_stale_minutes: 240 }).board_stale_after_minutes, 240)
  })
})

describe('久无人接：阈值的写法与下拉选项', () => {
  it('boardStaleLabel：整小时写小时，其余写分钟（与服务端提醒、回执头同一种写法）', () => {
    for (const [m, want] of [[30, '30 分钟'], [45, '45 分钟'], [60, '1 小时'], [90, '90 分钟'], [120, '2 小时'], [180, '3 小时'], [1440, '24 小时']] as const) {
      assert.equal(boardStaleLabel(m), want)
    }
  })

  it('预设项 + 默认那一项标「（默认）」且值为 0；选中项与选项同一种口径', () => {
    const def = emptyPrefs()
    const choices = boardStaleChoices(def)
    assert.deepEqual(choices.map(c => c.label), ['30 分钟', '1 小时', '2 小时（默认）', '3 小时', '4 小时', '6 小时', '8 小时', '12 小时', '24 小时'])
    assert.equal(choices.filter(c => c.value === 0).length, 1)
    assert.equal(choices.find(c => c.value === 0)?.label, '2 小时（默认）')
    assert.equal(boardStaleSelected(def), 0)
    assert.ok(choices.some(c => c.value === boardStaleSelected(def)), '选中项必须在选项里')
    assert.deepEqual(BOARD_STALE_CHOICES, [30, 60, 120, 180, 240, 360, 480, 720, 1440])
  })

  it('选了预设项：选中项就是它的分钟数', () => {
    const p = normalizePrefs({ board_stale_minutes: 360, board_stale_after_minutes: 360, board_stale_default_minutes: 120 })
    assert.equal(boardStaleSelected(p), 360)
    assert.ok(boardStaleChoices(p).some(c => c.value === 360 && c.label === '6 小时'))
  })

  it('当前值不在预设里（直接调接口设的 90 分钟）：临时补进选项，下拉框不会显示成空白', () => {
    const p = normalizePrefs({ board_stale_minutes: 90, board_stale_after_minutes: 90, board_stale_default_minutes: 120 })
    const choices = boardStaleChoices(p)
    assert.ok(choices.some(c => c.value === 90 && c.label === '90 分钟'))
    assert.ok(choices.some(c => c.value === boardStaleSelected(p)))
    // 补进去的位置按大小排：在 1 小时与 2 小时之间
    const labels = choices.map(c => c.label)
    assert.ok(labels.indexOf('1 小时') < labels.indexOf('90 分钟') && labels.indexOf('90 分钟') < labels.indexOf('2 小时（默认）'))
  })

  it('默认值本身不在预设里（以后服务端把默认改成 100 分钟）：默认项照样存在且值为 0', () => {
    const p = normalizePrefs({ board_stale_after_minutes: 100, board_stale_default_minutes: 100 })
    const choices = boardStaleChoices(p)
    assert.equal(choices.find(c => c.value === 0)?.label, '100 分钟（默认）')
    assert.equal(boardStaleSelected(p), 0)
  })

  it('用户选的值恰好等于默认值：也归到「默认」那一项（存 0，以后默认调整时跟着走）', () => {
    const p = normalizePrefs({ board_stale_minutes: 120, board_stale_after_minutes: 120, board_stale_default_minutes: 120 })
    assert.equal(boardStaleSelected(p), 0)
  })
})
