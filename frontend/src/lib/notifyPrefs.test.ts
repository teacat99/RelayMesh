// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import {
  MUTE_PRESETS,
  PREVIEW_MODES,
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
