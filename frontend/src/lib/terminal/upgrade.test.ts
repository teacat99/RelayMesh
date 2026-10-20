// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, UpgradeWindowView } from '../../api/terminal'
import {
  agentVersionNote,
  defaultUpgradeScope,
  finishedUpgrades,
  hostGOOS,
  upgradeBadge,
  upgradeBlock,
  upgradeBlockText,
  upgradePlatform,
  upgradeSummary,
  upgradeTargetOS,
  upgradeWindowOpen,
} from './upgrade'

const TICKET = '0123456789abcdef0123456789abcdef'

function win(over: Partial<UpgradeWindowView> = {}): UpgradeWindowView {
  return { open: true, ticket: TICKET, until: '2026-10-03T12:10:00Z', remaining_seconds: 540, base_version: 'v1.6.0', ...over }
}

function host(over: Partial<HostView> = {}): HostView {
  return {
    machine_code: 'RM-AAAA-BBBB-CCCC',
    remark: '',
    status: 'enabled',
    temporary: false,
    online: true,
    hostname: 'h1',
    os: 'linux',
    arch: 'amd64',
    agent_version: 'v1.6.0',
    privilege: 'root',
    last_ip: '10.0.0.1',
    sessions: [],
    ...over,
  }
}

describe('upgradeBadge', () => {
  it('没有版本差异、没有窗口就不提醒', () => {
    assert.equal(upgradeBadge(host()), null)
    assert.equal(upgradeBadge(host({ latest_agent_version: 'v1.6.0', upgrade_available: false })), null)
  })

  it('能确定更旧：可升级；无法比较：版本不同（文案如实说明无法比较新旧）', () => {
    const older = upgradeBadge(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'older' }))
    assert.equal(older?.text, '可升级')
    assert.equal(older?.tone, 'warn')
    assert.ok(older?.title.includes('v1.6.0') && older.title.includes('v1.7.0'))
    const diff = upgradeBadge(host({ agent_version: 'e14958f', latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'different' }))
    assert.equal(diff?.text, '版本不同')
    assert.ok(diff?.title.includes('无法比较新旧'))
  })

  it('窗口开着时优先显示窗口状态，并给出剩余分钟（至少 1 分钟）', () => {
    const b = upgradeBadge(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'older', upgrade_window: win({ remaining_seconds: 541 }) }))
    assert.equal(b?.text, '升级中')
    assert.equal(b?.tone, 'info')
    assert.ok(b?.title.includes('10 分钟'), b?.title)
    assert.ok(upgradeBadge(host({ upgrade_window: win({ remaining_seconds: 3 }) }))?.title.includes('1 分钟'))
  })

  it('窗口 open=false（已结束）不当作升级中', () => {
    assert.equal(upgradeBadge(host({ upgrade_window: win({ open: false }) })), null)
  })
})

describe('upgradeSummary', () => {
  it('说明三种关系：服务端没写版本、版本一致、更旧、无法比较', () => {
    assert.equal(upgradeSummary(host()), '当前 Agent v1.6.0')
    assert.ok(upgradeSummary(host({ latest_agent_version: 'v1.6.0' })).includes('一致'))
    assert.ok(upgradeSummary(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'older' })).includes('服务端自带 v1.7.0（更新）'))
    assert.ok(upgradeSummary(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'different' })).includes('无法比较新旧'))
    assert.ok(upgradeSummary(host({ agent_version: '' })).includes('未知版本'))
  })

  it('代码指纹：一致时明说「代码完全一致、不需要升级」；不同但版本号无法比较时说「代码有更新」', () => {
    const same = upgradeSummary(host({ latest_agent_version: 'v1.7.0', up_to_date_by_rev: true }))
    assert.ok(same.includes('v1.6.0') && same.includes('v1.7.0'), same)
    assert.ok(same.includes('代码完全一致') && same.includes('不需要升级'), same)
    const changed = upgradeSummary(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'changed' }))
    assert.ok(changed.includes('Agent 代码有更新') && changed.includes('无法比较新旧'), changed)
  })
})

describe('指纹相关的徽标与版本说明（#143）', () => {
  it('changed：徽标「代码有更新」，悬停说明两边代码确实不同', () => {
    const b = upgradeBadge(host({ agent_version: 'v1.7.0-dev-20261016-aaaaaaa', latest_agent_version: 'v1.7.0-dev-20261016-bbbbbbb', upgrade_available: true, upgrade_kind: 'changed' }))
    assert.equal(b?.text, '代码有更新')
    assert.equal(b?.tone, 'warn')
    assert.ok(b?.title.includes('代码确实不同') && b.title.includes('无法比较新旧'), b?.title)
  })

  it('指纹一致的主机没有升级徽标（版本号不同不再亮灯）', () => {
    assert.equal(upgradeBadge(host({ latest_agent_version: 'v1.7.0', up_to_date_by_rev: true })), null)
  })

  it('agentVersionNote：可升级给琥珀色提示（三种 kind 悬停说明不同）；代码一致给绿色说明；其余没有', () => {
    const older = agentVersionNote(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'older' }))
    assert.equal(older?.text, '（服务端自带 v1.7.0）')
    assert.equal(older?.tone, 'warn')
    assert.equal(older?.title, '服务端自带的版本更新')
    assert.match(agentVersionNote(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'changed' }))?.title ?? '', /指纹不同/)
    assert.match(agentVersionNote(host({ latest_agent_version: 'v1.7.0', upgrade_available: true, upgrade_kind: 'different' }))?.title ?? '', /无法比较新旧/)

    const same = agentVersionNote(host({ agent_rev: '72ae54bd8799', latest_agent_version: 'v1.7.0', up_to_date_by_rev: true }))
    assert.equal(same?.text, '（与服务端自带的 v1.7.0 代码一致）')
    assert.equal(same?.tone, 'ok')
    assert.ok(same?.title.includes('72ae54bd8799') && same.title.includes('不需要升级'), same?.title)
    // 老版本的服务端没有给指纹时 title 也不能出现 undefined
    assert.doesNotMatch(agentVersionNote(host({ latest_agent_version: 'v1.7.0', up_to_date_by_rev: true }))?.title ?? '', /undefined/)

    assert.equal(agentVersionNote(host()), null) // 服务端没写版本
    assert.equal(agentVersionNote(host({ latest_agent_version: 'v1.6.0' })), null) // 版本一致
  })
})

describe('目标系统与平台', () => {
  it('upgradeTargetOS：GOOS → 命令生成器的系统名，不认识的返回 null', () => {
    assert.equal(upgradeTargetOS('linux'), 'linux')
    assert.equal(upgradeTargetOS('Darwin'), 'macos')
    assert.equal(upgradeTargetOS(' windows '), 'windows')
    assert.equal(upgradeTargetOS('freebsd'), null)
    assert.equal(upgradeTargetOS(''), null)
  })

  it('upgradePlatform：os-arch 小写，信息不全返回空串', () => {
    assert.equal(upgradePlatform({ os: 'Linux', arch: 'ARM64' }), 'linux-arm64')
    assert.equal(upgradePlatform({ os: 'linux', arch: '' }), '')
    assert.equal(upgradePlatform({ os: '', arch: 'amd64' }), '')
  })

  it('hostGOOS：Agent 实际上报的是系统描述（PRETTY_NAME / macOS 版本 / windows），不是 GOOS', () => {
    // 实验环境里浏览器验证时发现：真实 Agent 报 "Ubuntu 24.04.2 LTS"，原先按 GOOS 精确匹配会把所有主机都判成「系统不受支持」
    assert.equal(hostGOOS('Ubuntu 24.04.2 LTS'), 'linux')
    assert.equal(hostGOOS('Debian GNU/Linux 12 (bookworm)'), 'linux')
    assert.equal(hostGOOS('Alpine Linux v3.20'), 'linux')
    assert.equal(hostGOOS('CentOS Linux 7 (Core)'), 'linux')
    assert.equal(hostGOOS('Linux'), 'linux')
    assert.equal(hostGOOS('macOS 14.5'), 'darwin')
    assert.equal(hostGOOS('macOS'), 'darwin')
    assert.equal(hostGOOS('Darwin'), 'darwin')
    assert.equal(hostGOOS('windows'), 'windows')
    assert.equal(hostGOOS('Windows 11 Pro'), 'windows')
    assert.equal(hostGOOS(''), null)
    assert.equal(hostGOOS('freebsd'), null)
    assert.equal(hostGOOS('FreeBSD 14.0-RELEASE'), null)
    assert.equal(hostGOOS('OpenBSD 7.5'), null)
  })

  it('真实 Agent 上报的写法也能得到升级平台与目标系统', () => {
    assert.equal(upgradePlatform({ os: 'Ubuntu 24.04.2 LTS', arch: 'amd64' }), 'linux-amd64')
    assert.equal(upgradePlatform({ os: 'macOS 14.5', arch: 'arm64' }), 'darwin-arm64')
    assert.equal(upgradePlatform({ os: 'windows', arch: 'amd64' }), 'windows-amd64')
    assert.equal(upgradeTargetOS('Ubuntu 24.04.2 LTS'), 'linux')
    assert.equal(upgradeTargetOS('macOS 14.5'), 'macos')
    const install = { available: true, platforms: ['linux-amd64'] }
    assert.equal(upgradeBlock({ os: 'Ubuntu 24.04.2 LTS', arch: 'amd64' }, install), null)
    assert.deepEqual(upgradeBlock({ os: 'macOS 14.5', arch: 'arm64' }, install), { kind: 'no-platform', platform: 'darwin-arm64' })
  })

  it('upgradeBlock：没有安装文件 / 缺平台 / 平台信息不全各有原因，能升级时返回 null', () => {
    const install = { available: true, platforms: ['linux-amd64', 'windows-amd64'] }
    assert.equal(upgradeBlock({ os: 'linux', arch: 'amd64' }, install), null)
    assert.deepEqual(upgradeBlock({ os: 'linux', arch: 'amd64' }, null), { kind: 'no-source' })
    assert.deepEqual(upgradeBlock({ os: 'linux', arch: 'amd64' }, { available: false }), { kind: 'no-source' })
    assert.deepEqual(upgradeBlock({ os: 'linux', arch: 'arm64' }, install), { kind: 'no-platform', platform: 'linux-arm64' })
    assert.deepEqual(upgradeBlock({ os: 'freebsd', arch: 'amd64' }, install), { kind: 'unknown-platform' })
    assert.deepEqual(upgradeBlock({ os: 'linux', arch: '' }, install), { kind: 'unknown-platform' })
    // 服务端没列平台清单时不据此拦截（由服务端的 404 兜底）
    assert.equal(upgradeBlock({ os: 'linux', arch: 'arm64' }, { available: true }), null)
  })

  it('upgradeBlockText：每种原因都有一句说明', () => {
    assert.ok(upgradeBlockText({ kind: 'no-source' }).includes('没有附带 Agent 安装文件'))
    assert.ok(upgradeBlockText({ kind: 'no-platform', platform: 'linux-arm64' }).includes('linux-arm64'))
    assert.ok(upgradeBlockText({ kind: 'unknown-platform' }).includes('系统与架构'))
  })
})

describe('defaultUpgradeScope', () => {
  it('root / 管理员预选系统级，普通用户与未知预选用户级', () => {
    assert.equal(defaultUpgradeScope('root'), 'system')
    assert.equal(defaultUpgradeScope('admin'), 'system')
    assert.equal(defaultUpgradeScope('user'), 'user')
    assert.equal(defaultUpgradeScope(''), 'user')
  })
})

describe('upgradeWindowOpen', () => {
  it('open 且有票据才算开着', () => {
    assert.equal(upgradeWindowOpen(host({ upgrade_window: win() })), true)
    assert.equal(upgradeWindowOpen(host({ upgrade_window: win({ ticket: '' }) })), false)
    assert.equal(upgradeWindowOpen(host({ upgrade_window: win({ open: false }) })), false)
    assert.equal(upgradeWindowOpen(host()), false)
  })
})

describe('finishedUpgrades：升级窗口开着的主机换上了新版本', () => {
  const code = (n: number) => `RM-0000-0000-${String(n).padStart(4, '0')}`

  it('窗口消失且版本变了才算完成', () => {
    const before = [host({ machine_code: code(1), upgrade_window: win() })]
    const after = [host({ machine_code: code(1), agent_version: 'v1.7.0' })]
    assert.deepEqual(finishedUpgrades(before, after).map(h => h.machine_code), [code(1)])
  })

  it('窗口只是到期 / 被手动结束（版本没变）不算完成', () => {
    const before = [host({ machine_code: code(1), upgrade_window: win() })]
    assert.deepEqual(finishedUpgrades(before, [host({ machine_code: code(1) })]), [])
  })

  it('窗口还开着、或上一次就没有窗口，都不算', () => {
    const open = [host({ machine_code: code(1), upgrade_window: win() })]
    assert.deepEqual(finishedUpgrades(open, [host({ machine_code: code(1), agent_version: 'v1.7.0', upgrade_window: win() })]), [])
    assert.deepEqual(finishedUpgrades([host({ machine_code: code(1) })], [host({ machine_code: code(1), agent_version: 'v1.7.0' })]), [])
  })

  it('以开窗时的版本为准；新出现的主机、消失的主机都不报', () => {
    // 两次轮询之间版本变过又被别处刷新：以 base_version 为准，现在的版本仍与开窗时不同
    const before = [host({ machine_code: code(1), agent_version: 'v1.6.5', upgrade_window: win({ base_version: 'v1.6.0' }) })]
    assert.equal(finishedUpgrades(before, [host({ machine_code: code(1), agent_version: 'v1.6.5' })]).length, 1)
    assert.deepEqual(finishedUpgrades([], [host({ machine_code: code(2), agent_version: 'v1.7.0' })]), [])
    assert.deepEqual(finishedUpgrades([host({ machine_code: code(3), upgrade_window: win() })], []), [])
  })

  it('旧版服务端没有 base_version 时退回上一次看到的版本', () => {
    const before = [host({ machine_code: code(1), agent_version: 'v1.6.0', upgrade_window: win({ base_version: undefined }) })]
    assert.equal(finishedUpgrades(before, [host({ machine_code: code(1), agent_version: 'v1.7.0' })]).length, 1)
  })

  it('离线（版本为空）不算完成', () => {
    const before = [host({ machine_code: code(1), upgrade_window: win() })]
    assert.deepEqual(finishedUpgrades(before, [host({ machine_code: code(1), agent_version: '' })]), [])
  })
})
