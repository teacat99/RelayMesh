// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { AutoCheckView, AutoGrantView, AutoUpgradeMode, UpgradeJobView } from '../../api/terminal'
import {
  AUTO_UPGRADE_ENABLE_COMMAND,
  AUTO_UPGRADE_SILENT_COMMAND,
  AUTO_UPGRADE_SWITCH_PLACE,
  type AutoUpgradeHost,
  autoUpgradeChip,
  autoUpgradeCoverage,
  autoUpgradeCoverageText,
  autoUpgradeModeText,
  autoUpgradeNote,
  windowSentence,
} from './autoUpgrade'

const NOW = Date.parse('2026-10-18T10:00:00Z')

function host(auto_upgrade?: AutoUpgradeMode, auto_check?: AutoCheckView, extra: Partial<AutoUpgradeHost> = {}): AutoUpgradeHost {
  return { auto_upgrade, auto_check, ...extra }
}

function check(status: AutoCheckView['status'], version?: string, at = '2026-10-18T09:40:00Z'): AutoCheckView {
  return { at, status, version }
}

function grant(status: AutoGrantView['status'], version?: string, at = '2026-10-18T09:50:00Z'): AutoGrantView {
  return { at, status, version }
}

function autoJob(over: Partial<UpgradeJobView> = {}): UpgradeJobView {
  return { id: 'a'.repeat(32), state: 'downloading', origin: 'auto', from: 'v1.6.0', to: 'v1.7.0', started_at: '2026-10-18T09:50:00Z', updated_at: '2026-10-18T09:50:30Z', ...over }
}

describe('autoUpgradeModeText', () => {
  it('三种取值各有说法，其余都当作关', () => {
    assert.equal(autoUpgradeModeText('notify'), '提示')
    assert.equal(autoUpgradeModeText('silent'), '静默')
    assert.equal(autoUpgradeModeText('off'), '关')
    assert.equal(autoUpgradeModeText(undefined), '关')
  })
})

describe('autoUpgradeChip', () => {
  it('不支持的 Agent 与没有开启的主机不占位', () => {
    assert.equal(autoUpgradeChip(host(undefined), true, NOW), null)
    assert.equal(autoUpgradeChip(host('off'), true, NOW), null)
    // 即使碰巧带着检查结果（过期数据），关着就是关着。
    assert.equal(autoUpgradeChip(host('off', check('available', 'v1.7.0')), true, NOW), null)
  })

  it('开启了但还没检查过：说明什么时候会检查，并强调提示模式不下载不升级', () => {
    const c = autoUpgradeChip(host('notify'), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：提示')
    assert.equal(c.tone, 'muted')
    assert.match(c.title, /不下载、不升级/)
    assert.match(c.title, /还没有可用的检查结果/)
  })

  it('服务端总闸关着：醒目提示并告诉人去哪里打开（总闸读不到时才信 Hub 上一次的回答）', () => {
    for (const [hub, ck] of [
      [false, undefined],
      [false, check('up_to_date', 'v1.7.0')],
      [undefined, check('disabled')],
    ] as const) {
      const c = autoUpgradeChip(host('notify', ck), hub, NOW)
      assert.ok(c)
      assert.equal(c.text, '自动升级：提示 · 总闸关')
      assert.equal(c.tone, 'warn')
      assert.ok(c.title.includes(AUTO_UPGRADE_SWITCH_PLACE))
    }
  })

  it('总闸刚打开、Hub 上一次的回答还是「关着」：那是过期的，不再显示总闸关，等下一次检查', () => {
    const c = autoUpgradeChip(host('notify', check('disabled')), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：提示')
    assert.equal(c.tone, 'muted')
    assert.match(c.title, /还没有可用的检查结果/)
  })

  it('有新版本：信息色，带版本号与检查时间，并说明提示模式不会自己升级', () => {
    const c = autoUpgradeChip(host('notify', check('available', 'v1.7.0')), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：提示 · 有新版本')
    assert.equal(c.tone, 'info')
    assert.match(c.title, /v1\.7\.0/)
    assert.match(c.title, /20 分钟前/)
    assert.match(c.title, /不会自动升级/)
  })

  it('静默模式的措辞不同，且不会写「不会自动升级」', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0')), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 有新版本')
    assert.match(c.title, /自己发起升级/)
    assert.match(c.title, /SHA-256 与发布签名/)
    assert.match(c.title, /发布签名，同一个版本最多自动尝试 2 次/)
    assert.doesNotMatch(c.title, /不会自动升级/)
  })

  it('已是最新、没有可用程序、Hub 认为没开启，各有各的说法与颜色', () => {
    const latest = autoUpgradeChip(host('notify', check('up_to_date', 'v1.7.0')), true, NOW)
    assert.ok(latest)
    assert.equal(latest.text, '自动升级：提示')
    assert.equal(latest.tone, 'muted')
    assert.match(latest.title, /已是最新/)

    const none = autoUpgradeChip(host('notify', check('unavailable')), true, NOW)
    assert.ok(none)
    assert.equal(none.tone, 'warn')
    assert.match(none.text, /无可用程序/)

    const refused = autoUpgradeChip(host('notify', check('not_allowed')), true, NOW)
    assert.ok(refused)
    assert.equal(refused.tone, 'warn')
    assert.match(refused.title, /服务端认为这台主机没有开启/)
  })

  it('不认识的状态（以后新增）不会让界面出错，按「检查过了」处理', () => {
    const c = autoUpgradeChip(host('notify', check('too_soon')), true, NOW)
    assert.ok(c)
    assert.equal(c.tone, 'muted')
    assert.match(c.title, /最近一次检查/)
  })

  it('总闸是 undefined（Hub 不认识自动升级）时不当作关着', () => {
    const c = autoUpgradeChip(host('notify', check('up_to_date', 'v1.7.0')), undefined, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：提示')
  })
})

describe('autoUpgradeNote', () => {
  it('只对「支持但没开启」的 Agent 显示，并写出在主机上怎么开', () => {
    const n = autoUpgradeNote(host('off'))
    assert.ok(n)
    assert.equal(n.text, '自动升级：关')
    assert.ok(n.title.includes(AUTO_UPGRADE_ENABLE_COMMAND))
    assert.match(n.title, /服务端无法替主机开启/)
  })

  it('也点到静默模式的前提：发布签名与信任的公钥', () => {
    const n = autoUpgradeNote(host('off'))
    assert.ok(n)
    assert.match(n.title, /--mode silent/)
    assert.match(n.title, /发布签名/)
    assert.match(n.title, /信任签名公钥/)
  })

  it('不支持（旧 Agent）与已开启（由徽标表达）都不显示', () => {
    assert.equal(autoUpgradeNote(host(undefined)), null)
    assert.equal(autoUpgradeNote(host('notify')), null)
    assert.equal(autoUpgradeNote(host('silent')), null)
  })
})

describe('autoUpgradeCoverage', () => {
  it('只统计声明了这项能力的在线主机，并区分「支持」「已开启」「静默」', () => {
    const c = autoUpgradeCoverage([host(undefined), host('off'), host('notify'), host('silent'), host('off')])
    assert.deepEqual(c, { supported: 4, enabled: 2, silent: 1 })
    assert.deepEqual(autoUpgradeCoverage([]), { supported: 0, enabled: 0, silent: 0 })
  })

  it('文案按三种情况说清楚打开总闸会影响谁', () => {
    assert.match(autoUpgradeCoverageText({ supported: 0, enabled: 0, silent: 0 }, true), /没有在线主机声明支持/)
    assert.match(autoUpgradeCoverageText({ supported: 3, enabled: 0, silent: 0 }, false), /暂时不会影响任何主机/)
    const on = autoUpgradeCoverageText({ supported: 3, enabled: 2, silent: 0 }, true)
    assert.match(on, /其中 2 台的所有者已在本机开启。$/)
    assert.match(autoUpgradeCoverageText({ supported: 3, enabled: 2, silent: 0 }, false), /总闸关着，它们暂时不会检查/)
  })

  it('有静默主机时单独点明：它们会自己升级（总闸关着时说「打开后」）', () => {
    const on = autoUpgradeCoverageText({ supported: 3, enabled: 2, silent: 1 }, true)
    assert.match(on, /静默模式的 1 台会在服务端有带发布签名的新版本时自己升级。$/)
    assert.doesNotMatch(on, /总闸打开后/)
    const off = autoUpgradeCoverageText({ supported: 3, enabled: 2, silent: 2 }, false)
    assert.match(off, /静默模式的 2 台在总闸打开后会在服务端有带发布签名的新版本时自己升级。$/)
    // 没有开启的主机时不提静默（总闸暂时不影响任何主机）。
    assert.doesNotMatch(autoUpgradeCoverageText({ supported: 3, enabled: 0, silent: 0 }, true), /静默/)
  })
})

describe('在主机上开启的命令', () => {
  it('提示模式只带 --mode notify；静默模式带 --mode silent，不再要求 --trust-key（官方发布的 Agent 已内置发布公钥）', () => {
    assert.match(AUTO_UPGRADE_ENABLE_COMMAND, /auto-upgrade enable --mode notify$/)
    assert.match(AUTO_UPGRADE_SILENT_COMMAND, /auto-upgrade enable --mode silent$/)
    assert.doesNotMatch(AUTO_UPGRADE_SILENT_COMMAND, /--trust-key/)
  })
})

describe('windowSentence', () => {
  it('写出主机本地时间的时间段，并说明已经开始的升级不受影响', () => {
    const t = windowSentence('03:00-05:30')
    assert.match(t, /主机本地时间 03:00–05:30/)
    assert.match(t, /已经开始的升级不受影响/)
  })

  it('跨午夜的时间段照常显示；没设或格式不对就是空串', () => {
    assert.match(windowSentence('22:00-02:00'), /22:00–02:00/)
    for (const bad of [undefined, '', '3:00-5:00', '03:00', '03:00-05:00 ', 'abc']) assert.equal(windowSentence(bad), '', String(bad))
  })
})

describe('autoUpgradeChip · 静默模式', () => {
  it('时间段写进说明里（只对静默；提示模式不显示）', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_window: '03:00-05:00' }), true, NOW)
    assert.ok(c)
    assert.match(c.title, /主机本地时间 03:00–05:00 之间开始升级/)
    const n = autoUpgradeChip(host('notify', check('available', 'v1.7.0'), { auto_window: '03:00-05:00' }), true, NOW)
    assert.ok(n)
    assert.doesNotMatch(n.title, /03:00/)
  })

  it('升级进行中：信息色，点名「静默自动升级」与版本，并带上当前阶段', () => {
    for (const [state, phrase] of [
      ['requested', /服务端已批准/],
      ['downloading', /正在下载并校验 v1\.7\.0/],
      ['handoff', /已交给独立的升级进程/],
    ] as const) {
      const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('granted', 'v1.7.0'), upgrade_job: autoJob({ state }) }), true, NOW)
      assert.ok(c, state)
      assert.equal(c.text, '自动升级：静默 · 升级中')
      assert.equal(c.tone, 'info')
      assert.match(c.title, /静默自动升级进行中（v1\.6\.0 → v1\.7\.0）/)
      assert.match(c.title, phrase)
    }
  })

  it('刚升级完成：说明是主机自己发起的；新一轮检查（时间更晚）一到就恢复平常的显示', () => {
    const done = autoJob({ state: 'done', updated_at: '2026-10-18T09:55:00Z' })
    const justDone = autoUpgradeChip(host('silent', check('available', 'v1.7.0', '2026-10-18T09:40:00Z'), { upgrade_job: done }), true, NOW)
    assert.ok(justDone)
    assert.equal(justDone.text, '自动升级：静默 · 刚升级完成')
    assert.match(justDone.title, /主机自己发起/)

    const later = autoUpgradeChip(host('silent', check('up_to_date', 'v1.7.0', '2026-10-18T09:56:00Z'), { upgrade_job: done }), true, NOW)
    assert.ok(later)
    assert.equal(later.text, '自动升级：静默')
    assert.match(later.title, /已是最新/)
  })

  it('升级失败：警示色，写明原因与重试规则；签名失败指向核对公钥', () => {
    const failed = autoJob({ state: 'failed', code: 'signature', updated_at: '2026-10-18T09:55:00Z' })
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { upgrade_job: failed }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 上次升级失败')
    assert.equal(c.tone, 'warn')
    assert.match(c.title, /发布签名/)
    assert.match(c.title, /最多自动尝试 2 次/)
    assert.match(c.title, /auto-upgrade status/)
  })

  it('管理员点的一键升级（没有 origin）不影响静默标签：它有自己的升级徽标', () => {
    const web = autoJob({ state: 'downloading', origin: undefined })
    const c = autoUpgradeChip(host('silent', check('up_to_date', 'v1.7.0'), { upgrade_job: web }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默')
    assert.match(c.title, /已是最新/)
  })

  it('没有签名：警示色，说清「绝不安装没有签名的程序」与出路', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('unsigned', 'v1.7.0') }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 未签名')
    assert.equal(c.tone, 'warn')
    assert.match(c.title, /绝不安装没有签名的程序/)
    assert.match(c.title, /提示模式/)
    assert.match(c.title, /v1\.7\.0/)
  })

  it('主机忙：信息色，说明不会打断正在做的事，会隔一段时间再试', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('busy', 'v1.7.0') }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 稍后再试')
    assert.equal(c.tone, 'info')
    assert.match(c.title, /不会打断/)
  })

  it('请求的版本过期了：不加后缀，说明 Agent 会重新检查', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('stale', 'v1.6.5') }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默')
    assert.equal(c.tone, 'muted')
    assert.match(c.title, /重新检查/)
  })

  it('已批准、升级请求的记录还没出现（或已被清理）：显示「已批准」', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('granted', 'v1.7.0') }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 已批准')
    assert.equal(c.tone, 'info')
  })

  it('与检查回答相同的几种裁决，说法也相同', () => {
    const upToDate = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('up_to_date', 'v1.7.0') }), true, NOW)
    assert.ok(upToDate)
    assert.equal(upToDate.text, '自动升级：静默')
    assert.match(upToDate.title, /已是最新/)
    const none = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('unavailable') }), true, NOW)
    assert.ok(none)
    assert.match(none.text, /无可用程序/)
    const refused = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { auto_grant: grant('not_allowed') }), true, NOW)
    assert.ok(refused)
    assert.match(refused.title, /服务端认为这台主机没有开启/)
  })

  it('过期的 disabled 裁决（总闸刚打开）不算数，按检查结果显示', () => {
    const c = autoUpgradeChip(host('silent', check('up_to_date', 'v1.7.0'), { auto_grant: grant('disabled', undefined, '2026-10-18T09:30:00Z') }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默')
    assert.match(c.title, /已是最新/)
  })

  it('以时间最晚的事为准：补上签名后新一轮的检查盖掉旧的「未签名」；旧检查盖不掉新裁决', () => {
    const unsigned = grant('unsigned', 'v1.7.0', '2026-10-18T03:00:00Z')
    const newer = autoUpgradeChip(host('silent', check('available', 'v1.7.0', '2026-10-18T09:00:00Z'), { auto_grant: unsigned }), true, NOW)
    assert.ok(newer)
    assert.equal(newer.text, '自动升级：静默 · 有新版本')
    const older = autoUpgradeChip(host('silent', check('available', 'v1.7.0', '2026-10-18T02:00:00Z'), { auto_grant: unsigned }), true, NOW)
    assert.ok(older)
    assert.equal(older.text, '自动升级：静默 · 未签名')
  })

  it('同一时刻以更具体的为准：升级 > 裁决 > 检查', () => {
    const at = '2026-10-18T09:50:00Z'
    const withJob = autoUpgradeChip(host('silent', check('available', 'v1.7.0', at), { auto_grant: grant('granted', 'v1.7.0', at), upgrade_job: autoJob({ updated_at: at }) }), true, NOW)
    assert.equal(withJob?.text, '自动升级：静默 · 升级中')
    const withGrant = autoUpgradeChip(host('silent', check('available', 'v1.7.0', at), { auto_grant: grant('unsigned', 'v1.7.0', at) }), true, NOW)
    assert.equal(withGrant?.text, '自动升级：静默 · 未签名')
  })

  it('服务端总闸关着时仍以总闸为准（即使有进行中的升级）', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0'), { upgrade_job: autoJob() }), false, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：静默 · 总闸关')
    assert.match(c.title, /静默模式/)
  })

  it('提示模式绝不显示静默的状态（即使数据里碰巧有裁决或自动升级的请求）', () => {
    const c = autoUpgradeChip(host('notify', check('up_to_date', 'v1.7.0'), { auto_grant: grant('unsigned', 'v1.7.0'), upgrade_job: autoJob({ state: 'failed', code: 'signature' }) }), true, NOW)
    assert.ok(c)
    assert.equal(c.text, '自动升级：提示')
    assert.match(c.title, /已是最新/)
  })

  it('时间坏掉的数据不会让界面出错', () => {
    const c = autoUpgradeChip(host('silent', check('available', 'v1.7.0', 'not a time'), { auto_grant: grant('busy', 'v1.7.0', '') }), true, NOW)
    assert.ok(c)
    assert.ok(c.text.startsWith('自动升级：静默'))
  })
})
