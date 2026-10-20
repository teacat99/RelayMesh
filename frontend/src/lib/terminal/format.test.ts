// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, it } from 'node:test'
import { auditEventLabel, compareAgentRev, formatCountdown, gateReasonInfo, holderBadge, holderReasonLabel, holderStaleBadge, holderVersionNote, hostTitle, lostReasonLabel, lostSummary, privilegeLabel, relativeTime, resultLabel, runLimitText, sessionTitle, shortCode } from './format'
import { idleSummary } from './lifecycle'

describe('hostTitle / sessionTitle / shortCode', () => {
  it('备注优先，其次主机名，最后机器码', () => {
    assert.equal(hostTitle({ remark: ' 生产机 ', hostname: 'web-1', machine_code: 'RM-AAAA-BBBB-CCCC-DDDD' }), '生产机')
    assert.equal(hostTitle({ remark: '', hostname: 'web-1', machine_code: 'RM-AAAA-BBBB-CCCC-DDDD' }), 'web-1')
    assert.equal(hostTitle({ machine_code: 'RM-AAAA-BBBB-CCCC-DDDD' }), 'RM-AAAA-BBBB-CCCC-DDDD')
  })

  it('会话显示名回退到 sid 前缀', () => {
    assert.equal(sessionTitle({ sid: 'abcdef123456', name: 'build' }), 'build')
    assert.equal(sessionTitle({ sid: 'abcdef123456', name: '  ' }), '会话 abcdef')
  })

  it('机器码缩写保留首尾', () => {
    assert.equal(shortCode('RM-7K2F-ABCD-EFGH-9XQ4'), 'RM-7K2F-…-9XQ4')
    assert.equal(shortCode('not-a-code'), 'not-a-code')
  })
})

describe('privilegeLabel', () => {
  it('root / admin 标记为高权限', () => {
    assert.equal(privilegeLabel('root').elevated, true)
    assert.equal(privilegeLabel('admin').elevated, true)
    assert.equal(privilegeLabel('user').elevated, false)
    assert.equal(privilegeLabel('').text, '未知')
  })
})

describe('relativeTime', () => {
  const now = Date.parse('2026-10-01T12:00:00Z')
  it('各个量级', () => {
    assert.equal(relativeTime('2026-10-01T11:59:55Z', now), '刚刚')
    assert.equal(relativeTime('2026-10-01T11:59:10Z', now), '50 秒前')
    assert.equal(relativeTime('2026-10-01T11:55:00Z', now), '5 分钟前')
    assert.equal(relativeTime('2026-10-01T09:00:00Z', now), '3 小时前')
    assert.equal(relativeTime('2026-09-28T12:00:00Z', now), '3 天前')
  })
  it('空值与非法值显示占位', () => {
    assert.equal(relativeTime(undefined, now), '—')
    assert.equal(relativeTime('garbage', now), '—')
  })
})

// 与服务端 terminal.runLimitText 一致（Go 侧同名用例 TestRunLimitText 用同一组输入）：运行时长上限的口语写法，不取整。
describe('runLimitText（运行时长上限，#141）', () => {
  it('整天、整小时、整分钟各用最大的整单位；其余写分秒；不取整', () => {
    const cases: Array<[number, string]> = [
      [30, '30 秒'], [60, '1 分钟'], [90, '1 分 30 秒'], [1800, '30 分钟'], [3600, '1 小时'], [5400, '90 分钟'],
      [2 * 3600, '2 小时'], [24 * 3600, '1 天'], [36 * 3600, '36 小时'], [7 * 24 * 3600, '7 天'],
    ]
    for (const [sec, want] of cases) assert.equal(runLimitText(sec), want, String(sec))
  })
  it('不是正数就返回空串，调用方据此省略', () => {
    for (const v of [0, -1, Number.NaN, Number.POSITIVE_INFINITY, undefined, 0.4]) assert.equal(runLimitText(v), '', String(v))
  })
})

describe('formatCountdown', () => {
  it('分秒与时分秒', () => {
    assert.equal(formatCountdown(0), '0:00')
    assert.equal(formatCountdown(65), '1:05')
    assert.equal(formatCountdown(3600), '1:00:00')
    assert.equal(formatCountdown(7325), '2:02:05')
    assert.equal(formatCountdown(-5), '0:00')
  })
})

describe('审计与总闸文案', () => {
  it('已知事件有中文名，未知事件原样显示', () => {
    assert.equal(auditEventLabel('unlock_ok'), '解锁成功')
    assert.equal(auditEventLabel('some_future_event'), 'some_future_event')
  })

  it('后端定义的每一种审计事件都有中文标签（防止新增事件只显示英文标识）', () => {
    // 事件常量分散在 terminal 包的多个文件里（audit.go、files_service.go、installer_rest.go …），所以整个包的非测试源码都要扫。
    let src = ''
    try {
      const dir = resolve(process.cwd(), '../internal/terminal')
      for (const name of readdirSync(dir)) {
        if (name.endsWith('.go') && !name.endsWith('_test.go')) src += readFileSync(resolve(dir, name), 'utf8') + '\n'
      }
    } catch {
      return // 只在完整仓库里检查
    }
    const events = [...src.matchAll(/^\s*(?:const\s+)?Ev\w+\s*=\s*"([a-z_]+)"/gm)].map(m => m[1])
    assert.ok(events.length > 20, '应当解析到后端的全部事件常量')
    const missing = events.filter(e => auditEventLabel(e) === e)
    assert.deepEqual(missing, [], '这些事件还没有中文标签')
  })

  it('结果标签', () => {
    assert.equal(resultLabel('fail').tone, 'bad')
    assert.equal(resultLabel('deny').tone, 'warn')
    assert.equal(resultLabel('').tone, 'none')
  })

  it('总闸关闭原因的说明不包含任何密钥内容', () => {
    for (const r of ['no_password', 'weak_jwt_secret', 'disabled_by_env', 'internal_error', 'whatever']) {
      const i = gateReasonInfo(r)
      assert.ok(i.title.length > 0 && i.body.length > 0)
    }
    assert.ok(gateReasonInfo('weak_jwt_secret').body.includes('RELAYMESH_JWT_SECRET'))
  })
})

describe('holderBadge', () => {
  it('离线主机不标注', () => {
    assert.equal(holderBadge({ online: false, holder: { state: 'ready', persistent: true } }), null)
  })

  it('重启不中断 / 重启会中断 / 不可用 / 旧版 Agent 各有明确标注', () => {
    assert.deepEqual(holderBadge({ online: true, holder: { state: 'ready', persistent: true } })?.tone, 'ok')
    assert.equal(holderBadge({ online: true, holder: { state: 'ready', persistent: true } })?.text, '重启不中断')
    const embedded = holderBadge({ online: true, holder: { state: 'ready', persistent: false } })
    assert.equal(embedded?.tone, 'warn')
    assert.equal(embedded?.text, '重启会中断')
    const down = holderBadge({ online: true, holder: { state: 'unavailable', persistent: false, reason: 'unreachable' } })
    assert.equal(down?.tone, 'bad')
    assert.match(down?.title ?? '', /没有运行或无法连接/)
    const old = holderBadge({ online: true })
    assert.equal(old?.tone, 'warn')
    assert.equal(old?.text, '重启会中断')
    assert.match(old?.title ?? '', /旧版 Agent/)
  })

  it('Hub 直连的 SSH 主机说的是 Hub 重启（不是“旧版 Agent”）；经网关的 SSH 主机自己不标注', () => {
    const direct = holderBadge({ online: true, kind: 'ssh', ssh: {} })
    assert.equal(direct?.tone, 'warn')
    assert.equal(direct?.text, 'Hub 重启会中断')
    assert.doesNotMatch(direct?.title ?? '', /旧版 Agent|Agent 重启/)
    assert.match(direct?.title ?? '', /经网关/)
    assert.equal(holderBadge({ online: true, kind: 'ssh', ssh: { gateway: 'RM-GW' } }), null)
    assert.equal(holderBadge({ online: false, kind: 'ssh', ssh: {} }), null)
  })

  it('两个徽标的名字不会再混淆：主机能力说「重启」，会话策略说「空闲 / 回收」', () => {
    const ok = holderBadge({ online: true, holder: { state: 'ready', persistent: true } })!
    const warn = holderBadge({ online: true, holder: { state: 'ready', persistent: false } })!
    for (const b of [ok, warn]) {
      assert.match(b.text, /重启/)
      assert.doesNotMatch(b.text, /持久|永久|保持|回收/)
    }
    assert.doesNotMatch(idleSummary(0), /持久|永久|保持/)
    assert.match(idleSummary(0), /空闲/)
  })
})

describe('holderStaleBadge', () => {
  const ready = (caps?: string[]) => ({ state: 'ready', caps })

  it('没有声明 charset 能力的就绪守护进程：标「守护进程较旧」，并说明原因与做法', () => {
    const b = holderStaleBadge({ online: true, os: 'linux', holder: ready(['pty', 'policy', 'drain', 'lost', 'exec']) })
    assert.equal(b?.text, '守护进程较旧')
    assert.match(b?.title ?? '', /holder restart/)
    assert.match(b?.title ?? '', /--drain/)
    assert.match(b?.title ?? '', /中文/)
    // 照抄命令不一定能用：悬停说明里要带上「找不到命令写完整路径 / 系统级安装加 sudo」的使用要点
    assert.match(b?.title ?? '', /完整路径/)
    assert.match(b?.title ?? '', /sudo/)
    // caps 字段整个缺失（更早的守护进程）也一样
    assert.equal(holderStaleBadge({ online: true, os: 'darwin', holder: ready() })?.text, '守护进程较旧')
  })

  it('声明了 charset 能力就不提醒（重启之后徽标自动消失）', () => {
    assert.equal(holderStaleBadge({ online: true, os: 'linux', holder: ready(['pty', 'charset']) }), null)
  })

  it('不适用的主机不提醒：离线、没有守护进程、守护进程不可用、Windows、SSH 主机', () => {
    assert.equal(holderStaleBadge({ online: false, os: 'linux', holder: ready([]) }), null)
    assert.equal(holderStaleBadge({ online: true, os: 'linux' }), null) // 旧版 Agent：holderBadge 已经说明
    assert.equal(holderStaleBadge({ online: true, os: 'linux', holder: { state: 'unavailable' } }), null)
    assert.equal(holderStaleBadge({ online: true, os: 'Windows 11', holder: ready([]) }), null)
    assert.equal(holderStaleBadge({ online: true, os: 'linux', kind: 'ssh', holder: ready([]) }), null)
  })

  // 代码指纹（#143）：两边都带着指纹时只看指纹，不看版本号，也不看能力位。
  const REV_A = '72ae54bd8799'
  const REV_B = '0123456789ab'

  it('指纹一致：守护进程跑的就是现在这份 Agent 代码，哪怕它没声明 charset 能力也不提醒', () => {
    assert.equal(holderStaleBadge({ online: true, os: 'linux', agent_rev: REV_A, holder: { state: 'ready', caps: [], rev: REV_A } }), null)
  })

  it('指纹不同：确实是旧一版，提醒，并写明两个指纹与重启做法；即使声明了 charset 能力也提醒', () => {
    const b = holderStaleBadge({ online: true, os: 'linux', agent_rev: REV_A, holder: { state: 'ready', caps: ['charset'], rev: REV_B } })
    assert.equal(b?.text, '守护进程较旧')
    assert.match(b?.title ?? '', new RegExp(REV_A))
    assert.match(b?.title ?? '', new RegExp(REV_B))
    assert.match(b?.title ?? '', /holder restart/)
    assert.match(b?.title ?? '', /--drain/)
    assert.match(b?.title ?? '', /完整路径/)
    assert.match(b?.title ?? '', /sudo/)
    // 这条提醒与 locale 无关，不该再把「中文输入」当作影响说出来
    assert.doesNotMatch(b?.title ?? '', /中文|locale/)
  })

  it('指纹不同对 Windows 同样适用（locale 的例外只属于能力位那条旧路径）', () => {
    assert.equal(holderStaleBadge({ online: true, os: 'Windows 11', agent_rev: REV_A, holder: { state: 'ready', rev: REV_B } })?.text, '守护进程较旧')
  })

  it('任何一边没有（或写法不合规的）指纹：回退到能力位，行为与引入指纹之前一致', () => {
    // 守护进程是旧版本（没有指纹）：由能力位判断
    assert.equal(holderStaleBadge({ online: true, os: 'linux', agent_rev: REV_A, holder: ready(['pty']) })?.text, '守护进程较旧')
    assert.equal(holderStaleBadge({ online: true, os: 'linux', agent_rev: REV_A, holder: ready(['pty', 'charset']) }), null)
    // Agent 没有指纹（非发布构建）
    assert.equal(holderStaleBadge({ online: true, os: 'linux', holder: { state: 'ready', caps: [], rev: REV_A } })?.text, '守护进程较旧')
    // 不合规的指纹等同于没有
    assert.equal(holderStaleBadge({ online: true, os: 'linux', agent_rev: 'NOT-HEX', holder: { state: 'ready', caps: ['charset'], rev: 'NOT-HEX' } }), null)
    assert.equal(holderStaleBadge({ online: true, os: 'Windows 11', agent_rev: REV_A, holder: ready([]) }), null)
  })
})

describe('compareAgentRev', () => {
  it('两边都有才比较；写法不合规或缺失一律 unknown', () => {
    assert.equal(compareAgentRev('72ae54bd8799', '72ae54bd8799'), 'same')
    assert.equal(compareAgentRev('72ae54bd8799', '0123456789ab'), 'differs')
    for (const [a, b] of [
      [undefined, '72ae54bd8799'],
      ['72ae54bd8799', undefined],
      ['', ''],
      ['72AE54BD8799', '72AE54BD8799'], // 大写不是合规写法
      ['abc12', 'abc12'], // 过短
      ['a'.repeat(41), 'a'.repeat(41)], // 过长
      ['../../etc', '../../etc'],
    ] as [string | undefined, string | undefined][]) {
      assert.equal(compareAgentRev(a, b), 'unknown', `${a} vs ${b}`)
    }
  })
})

describe('holderVersionNote', () => {
  const base = { online: true, agent_version: 'v1.6.3-dev-20261016-c26a452', agent_rev: '72ae54bd8799' }
  const holder = (over: { version?: string; rev?: string; state?: string } = {}) => ({
    state: 'ready',
    version: 'v1.6.3-dev-20261015-95b0679',
    rev: '72ae54bd8799',
    ...over,
  })

  it('版本号不同但代码指纹一致：标「代码一致」，说明不需要重启（这就是 #124 的现场）', () => {
    const n = holderVersionNote({ ...base, holder: holder() })
    assert.equal(n?.text, '守护进程 v1.6.3-dev-20261015-95b0679（代码一致）')
    assert.equal(n?.tone, 'muted')
    assert.match(n?.title ?? '', /不需要重启守护进程/)
  })

  it('指纹不同：标「代码较旧」并用琥珀色，说明怎么换上新的', () => {
    const n = holderVersionNote({ ...base, holder: holder({ rev: '0123456789ab' }) })
    assert.equal(n?.text, '守护进程 v1.6.3-dev-20261015-95b0679（代码较旧）')
    assert.equal(n?.tone, 'warn')
    assert.match(n?.title ?? '', /holder restart/)
  })

  it('没有指纹（旧版本）：只显示版本号，并如实说明无法判断', () => {
    const n = holderVersionNote({ ...base, holder: holder({ rev: undefined }) })
    assert.equal(n?.text, '守护进程 v1.6.3-dev-20261015-95b0679')
    assert.match(n?.title ?? '', /无法判断/)
    assert.equal(holderVersionNote({ ...base, agent_rev: undefined, holder: holder() })?.text, '守护进程 v1.6.3-dev-20261015-95b0679')
  })

  it('没有多余信息时不显示：版本号相同、离线、没有守护进程、守护进程不可用、没有版本、SSH 主机', () => {
    assert.equal(holderVersionNote({ ...base, holder: holder({ version: base.agent_version }) }), null)
    assert.equal(holderVersionNote({ ...base, online: false, holder: holder() }), null)
    assert.equal(holderVersionNote({ ...base }), null)
    assert.equal(holderVersionNote({ ...base, holder: holder({ state: 'unavailable' }) }), null)
    assert.equal(holderVersionNote({ ...base, holder: holder({ version: '' }) }), null)
    assert.equal(holderVersionNote({ ...base, kind: 'ssh', holder: holder() }), null)
  })
})

describe('守护进程原因文案', () => {
  it('丢失原因逐一翻译，未知原因原样保留', () => {
    assert.equal(lostReasonLabel('host_reboot'), '主机重启')
    assert.equal(lostReasonLabel('holder_stopped'), '守护进程被停止')
    assert.equal(lostReasonLabel('holder_exit'), '守护进程意外退出')
    assert.equal(lostReasonLabel('weird'), 'weird')
    assert.equal(lostReasonLabel(''), '原因未知')
  })

  it('不可用原因覆盖四种取值与兜底', () => {
    for (const r of ['connecting', 'superseded', 'version', 'unreachable']) {
      assert.notEqual(holderReasonLabel(r), '守护进程不可用')
    }
    assert.equal(holderReasonLabel(undefined), '守护进程不可用')
  })

  it('丢失会话汇总：按原因计数', () => {
    assert.equal(lostSummary(undefined), '')
    assert.equal(lostSummary([]), '')
    assert.equal(
      lostSummary([
        { sid: 'a', reason: 'holder_stopped' },
        { sid: 'b', name: 'x', reason: 'holder_stopped' },
        { sid: 'c', reason: 'host_reboot' },
      ]),
      '有会话已不存在：2 个因守护进程被停止、1 个因主机重启',
    )
  })
})
