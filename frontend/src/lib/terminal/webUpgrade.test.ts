// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, SessionInfo, UpgradeJobView } from '../../api/terminal'
import {
  failureTitle,
  finishedWebUpgrades,
  jobActive,
  jobBadgeTitle,
  jobIsAuto,
  jobKindLabel,
  jobNotice,
  localSessions,
  sessionWarning,
  sessionsNeedConfirm,
  webUpgradeMode,
} from './webUpgrade'
import { upgradeBadge } from './upgrade'

function host(over: Partial<HostView> = {}): HostView {
  return {
    machine_code: 'RM-AAAA-BBBB-CCCC',
    remark: '',
    status: 'enabled',
    temporary: false,
    online: true,
    hostname: 'h1',
    os: 'Debian GNU/Linux 12 (bookworm)',
    arch: 'amd64',
    agent_version: 'v1.6.0',
    privilege: 'root',
    last_ip: '10.0.0.1',
    sessions: [],
    latest_agent_version: 'v1.7.0',
    upgrade_available: true,
    upgrade_kind: 'older',
    web_upgrade: 'on',
    ...over,
  }
}

function job(over: Partial<UpgradeJobView> = {}): UpgradeJobView {
  return { id: 'a'.repeat(32), state: 'requested', from: 'v1.6.0', to: 'v1.7.0', started_at: '2026-10-09T00:00:00Z', updated_at: '2026-10-09T00:00:00Z', ...over }
}

function session(over: Partial<SessionInfo> = {}): SessionInfo {
  return { sid: 's'.repeat(32), rows: 24, cols: 80, base: 0, end: 0, state: 'running', attached: 0, ...over }
}

describe('webUpgradeMode：这台主机现在能不能点「一键升级」', () => {
  it('在线、主人已允许、版本较旧：可以点', () => {
    assert.equal(webUpgradeMode(host()), 'ready')
  })

  it('主人没有允许：说明去主机上怎么开，而不是给一个点了必败的按钮', () => {
    assert.equal(webUpgradeMode(host({ web_upgrade: 'off' })), 'owner-off')
  })

  it('Agent 不支持（没有声明能力）：只能用升级窗口里的命令', () => {
    assert.equal(webUpgradeMode(host({ web_upgrade: undefined })), 'unsupported')
  })

  it('主机离线：需要 Agent 在线', () => {
    assert.equal(webUpgradeMode(host({ online: false })), 'offline')
  })

  it('没有可升级的版本：什么都不提供', () => {
    assert.equal(webUpgradeMode(host({ upgrade_available: false })), 'current')
    assert.equal(webUpgradeMode(host({ upgrade_available: undefined, latest_agent_version: undefined })), 'current')
  })

  it('请求进行中优先：即使 Agent 正在重启（暂时离线）也显示进展', () => {
    for (const state of ['requested', 'downloading', 'handoff'] as const) {
      assert.equal(webUpgradeMode(host({ upgrade_job: job({ state }), online: false })), 'running', state)
    }
  })

  it('已结束的请求不挡着再次发起', () => {
    assert.equal(webUpgradeMode(host({ upgrade_job: job({ state: 'failed', code: 'checksum' }) })), 'ready')
    assert.equal(webUpgradeMode(host({ upgrade_job: job({ state: 'done' }), upgrade_available: false })), 'current')
  })
})

describe('jobActive', () => {
  it('只有已发出 / 下载中 / 已交接算进行中', () => {
    assert.deepEqual(
      (['requested', 'downloading', 'handoff', 'done', 'failed'] as const).map(state => jobActive(job({ state }))),
      [true, true, true, false, false],
    )
    assert.equal(jobActive(undefined), false)
    assert.equal(jobActive(null), false)
  })
})

describe('localSessions / sessionsNeedConfirm：点击前就知道会影响什么', () => {
  it('只数运行中的会话；已结束的不算', () => {
    const s = localSessions(
      host({
        sessions: [session(), session({ attached: 2 }), session({ alt: true, attached: 1 }), session({ state: 'exited', attached: 0 })],
        model_running: 1,
        holder: { state: 'ready', persistent: true },
      }),
    )
    assert.deepEqual(s, { running: 3, attached: 2, alt: 1, model_running: 1, persistent: true })
    assert.equal(sessionsNeedConfirm(s), true)
  })

  it('没有运行中的会话也没有模型运行：不需要确认', () => {
    const s = localSessions(host({ sessions: [session({ state: 'exited' })] }))
    assert.deepEqual(s, { running: 0, attached: 0, alt: 0, model_running: 0, persistent: false })
    assert.equal(sessionsNeedConfirm(s), false)
  })

  it('只有模型运行也要确认', () => {
    assert.equal(sessionsNeedConfirm({ running: 0, attached: 0, alt: 0, model_running: 2, persistent: true }), true)
  })

  it('旧版 Agent 没有 holder 字段：按「会话与 Agent 同生共死」处理', () => {
    assert.equal(localSessions(host({ sessions: [session()], holder: undefined })).persistent, false)
  })
})

describe('sessionWarning：对活动会话的提醒', () => {
  it('会话由守护进程持有：如实说明不会中断，页面会短暂重连', () => {
    const w = sessionWarning({ running: 2, attached: 1, alt: 0, model_running: 0, persistent: true })
    assert.equal(w.severe, false)
    assert.ok(w.headline.includes('2 个运行中的终端会话') && w.headline.includes('1 个此刻有人正在查看'))
    assert.ok(w.detail.includes('不会中断') && w.detail.includes('自动重连'))
  })

  it('会话与 Agent 同生共死：明确说会被结束，并用醒目的提示', () => {
    const w = sessionWarning({ running: 1, attached: 0, alt: 1, model_running: 0, persistent: false })
    assert.equal(w.severe, true)
    assert.ok(w.headline.includes('1 个正在运行全屏程序'))
    assert.ok(w.detail.includes('结束这些会话') && w.detail.includes('丢失'))
  })

  it('同时有模型运行：一并说明', () => {
    const w = sessionWarning({ running: 1, attached: 0, alt: 0, model_running: 3, persistent: true })
    assert.ok(w.headline.includes('3 个进行中的模型运行'))
    assert.ok(w.detail.includes('模型运行') && w.detail.includes('对账'))
    assert.equal(sessionWarning({ running: 0, attached: 0, alt: 0, model_running: 1, persistent: true }).headline, '这台主机上有 1 个进行中的模型运行。')
  })
})

describe('jobNotice：请求进行到哪一步、结果是什么', () => {
  it('三个进行中的阶段各有说明，且都是进行中的语气', () => {
    const titles = (['requested', 'downloading', 'handoff'] as const).map(state => jobNotice(job({ state })))
    assert.deepEqual(titles.map(n => n.tone), ['progress', 'progress', 'progress'])
    assert.ok(titles[0].title.includes('等待 Agent 应答'))
    assert.ok(titles[1].title.includes('v1.7.0'))
    assert.ok(titles[2].detail.includes('自动还原'))
  })

  it('完成：写明从哪个版本到哪个版本', () => {
    const n = jobNotice(job({ state: 'done' }))
    assert.equal(n.tone, 'success')
    assert.ok(n.title.includes('v1.6.0') && n.title.includes('v1.7.0'))
  })

  it('停滞：不说成功也不说失败，指给人去主机上看', () => {
    const n = jobNotice(job({ state: 'handoff', stalled: true }))
    assert.equal(n.tone, 'warn')
    assert.ok(n.detail.includes('systemctl status relaymesh-agent'))
  })

  it('失败：给出原因、Agent 的说明，以及「主机现在还好吗」', () => {
    const n = jobNotice(job({ state: 'failed', code: 'checksum', detail: '期望 aaaa，实际 bbbb' }))
    assert.equal(n.tone, 'error')
    assert.ok(n.title.includes('SHA-256'))
    assert.ok(n.detail.includes('期望 aaaa') && n.detail.includes('没有被改动'))
  })

  it('已还原：说明主机已经回到旧版本', () => {
    const n = jobNotice(job({ state: 'failed', code: 'rolled_back', rolled_back: true, detail: '新版本 90 秒内没有连上服务端' }))
    assert.ok(n.title.includes('已自动还原'))
    assert.ok(n.detail.includes('已经回到旧版本'))
  })

  it('升级进程没能完成：Agent 仍是旧版本，去主机看日志', () => {
    const n = jobNotice(job({ state: 'failed', code: 'apply' }))
    assert.ok(n.detail.includes('仍在运行旧版本') && n.detail.includes('日志'))
  })

  it('每个已知失败码都有人话；不认识的码如实带出', () => {
    for (const code of ['not_supported', 'not_allowed', 'up_to_date', 'busy', 'bad_request', 'download', 'checksum', 'signature', 'record', 'verify', 'handoff', 'apply', 'rolled_back']) {
      assert.ok(!failureTitle(code).includes('升级没有完成（'), code)
    }
    assert.equal(failureTitle('weird'), '升级没有完成（weird）')
    assert.equal(failureTitle(undefined), '升级没有完成')
  })
})

describe('静默自动升级的请求（origin = auto）：措辞要说清楚「是主机自己发起的」', () => {
  const auto = (over: Partial<UpgradeJobView> = {}) => job({ origin: 'auto', ...over })

  it('jobIsAuto / jobKindLabel：只有 origin 为 auto 才算，管理员点的与旧数据都不算', () => {
    assert.equal(jobIsAuto(auto()), true)
    assert.equal(jobIsAuto(job()), false)
    assert.equal(jobIsAuto(null), false)
    assert.equal(jobIsAuto(undefined), false)
    assert.equal(jobKindLabel(auto()), '静默自动升级')
    assert.equal(jobKindLabel(job()), '一键升级')
  })

  it('进行中：已批准 → 下载校验（含验签）→ 交接，各说各的，且不再说成「已向主机发出请求」', () => {
    const [requested, downloading, handoff] = (['requested', 'downloading', 'handoff'] as const).map(state => jobNotice(auto({ state })))
    assert.ok(requested.title.includes('服务端已批准') && !requested.title.includes('已向主机发出'))
    assert.ok(downloading.title.includes('v1.7.0') && downloading.detail.includes('发布签名') && downloading.detail.includes('静默升级'))
    assert.ok(handoff.detail.includes('自动还原'))
    assert.deepEqual([requested.tone, downloading.tone, handoff.tone], ['progress', 'progress', 'progress'])
  })

  it('完成：说明是主机自己发起的', () => {
    const n = jobNotice(auto({ state: 'done' }))
    assert.equal(n.tone, 'success')
    assert.ok(n.title.includes('已自动升级') && n.title.includes('v1.6.0') && n.title.includes('v1.7.0'))
    assert.ok(n.detail.includes('主机自己发起'))
    // 管理员点的升级措辞不变。
    assert.ok(jobNotice(job({ state: 'done' })).title.startsWith('升级完成'))
  })

  it('失败：标题带「静默自动升级没有成功」，并说明最多自动尝试 2 次、怎么重新计数', () => {
    const n = jobNotice(auto({ state: 'failed', code: 'checksum', detail: '期望 aaaa，实际 bbbb' }))
    assert.equal(n.tone, 'error')
    assert.ok(n.title.startsWith('静默自动升级没有成功：') && n.title.includes('SHA-256'))
    assert.ok(n.detail.includes('期望 aaaa') && n.detail.includes('没有被改动'))
    assert.ok(n.detail.includes('最多自动尝试 2 次') && n.detail.includes('auto-upgrade enable'))
    // 一键升级失败时不该出现「自动尝试」的说法。
    assert.ok(!jobNotice(job({ state: 'failed', code: 'checksum' })).detail.includes('自动尝试'))
  })

  it('签名没通过：说明已丢弃、主机没被改动，并指给人去核对签名与信任的公钥', () => {
    const n = jobNotice(auto({ state: 'failed', code: 'signature' }))
    assert.ok(n.title.includes('发布签名') && n.title.includes('已丢弃'))
    assert.ok(n.detail.includes('没有被改动') && n.detail.includes('auto-upgrade status'))
  })

  it('记不下尝试记录：说明因此没有下载', () => {
    assert.ok(failureTitle('record').includes('没有下载'))
  })

  it('已还原与升级进程没完成：沿用既有说明，再补上重试规则', () => {
    const rolled = jobNotice(auto({ state: 'failed', code: 'rolled_back', rolled_back: true }))
    assert.ok(rolled.detail.includes('已经回到旧版本') && rolled.detail.includes('最多自动尝试 2 次'))
    const applied = jobNotice(auto({ state: 'failed', code: 'apply' }))
    assert.ok(applied.detail.includes('仍在运行旧版本') && applied.detail.includes('最多自动尝试 2 次'))
  })

  it('徽标说明点名「静默自动升级」', () => {
    assert.ok(jobBadgeTitle(auto({ state: 'downloading' })).startsWith('静默自动升级进行中'))
    const failed = jobBadgeTitle(auto({ state: 'failed', code: 'signature' }))
    assert.ok(failed.startsWith('上一次静默自动升级没有成功') && failed.includes('发布签名'))
    const badge = upgradeBadge(host({ upgrade_job: auto({ state: 'downloading' }) }))
    assert.equal(badge?.text, '升级中')
    assert.ok(badge?.title.includes('静默自动升级'))
  })

  it('刚结束的静默升级也会被识别（页面开着时提示一次），origin 原样带出', () => {
    const before = [host({ upgrade_job: auto({ state: 'downloading' }) })]
    const after = [host({ upgrade_job: auto({ state: 'done' }) })]
    const out = finishedWebUpgrades(before, after)
    assert.equal(out.length, 1)
    assert.equal(out[0].job.origin, 'auto')
  })
})

describe('finishedWebUpgrades：刚结束的一键升级才提示一次', () => {
  const was = (state: UpgradeJobView['state']) => [host({ upgrade_job: job({ state }) })]

  it('上一次还在进行、这一次已完成 / 失败：提示', () => {
    const done = finishedWebUpgrades(was('handoff'), [host({ upgrade_job: job({ state: 'done' }), agent_version: 'v1.7.0' })])
    assert.equal(done.length, 1)
    assert.equal(done[0].job.state, 'done')
    const failed = finishedWebUpgrades(was('requested'), [host({ upgrade_job: job({ state: 'failed', code: 'download' }) })])
    assert.equal(failed.length, 1)
  })

  it('刷新页面后第一次就看到已结束的请求：是历史，不提示', () => {
    assert.deepEqual(finishedWebUpgrades([], [host({ upgrade_job: job({ state: 'done' }) })]), [])
    assert.deepEqual(finishedWebUpgrades([host()], [host({ upgrade_job: job({ state: 'failed' }) })]), [])
  })

  it('同一个已结束的请求不会重复提示', () => {
    assert.deepEqual(finishedWebUpgrades(was('done'), [host({ upgrade_job: job({ state: 'done' }) })]), [])
  })

  it('别的请求（编号不同）不算同一次', () => {
    assert.deepEqual(finishedWebUpgrades(was('handoff'), [host({ upgrade_job: job({ id: 'b'.repeat(32), state: 'done' }) })]), [])
  })

  it('仍在进行的不提示', () => {
    assert.deepEqual(finishedWebUpgrades(was('requested'), [host({ upgrade_job: job({ state: 'downloading' }) })]), [])
  })
})

describe('主机卡片徽标', () => {
  it('一键升级进行中：显示「升级中」，优先于「可升级」', () => {
    const b = upgradeBadge(host({ upgrade_job: job({ state: 'downloading' }) }))
    assert.equal(b?.text, '升级中')
    assert.equal(b?.tone, 'info')
    assert.ok(b?.title.includes('v1.7.0'))
  })

  it('上一次失败且仍有新版本：提示失败，点开看原因', () => {
    const b = upgradeBadge(host({ upgrade_job: job({ state: 'failed', code: 'checksum' }) }))
    assert.equal(b?.text, '升级失败')
    assert.equal(b?.tone, 'warn')
    assert.ok(b?.title.includes('SHA-256'))
  })

  it('已完成的请求（版本已更新）不再提醒', () => {
    assert.equal(upgradeBadge(host({ upgrade_job: job({ state: 'done' }), upgrade_available: false, agent_version: 'v1.7.0' })), null)
  })

  it('jobBadgeTitle', () => {
    assert.ok(jobBadgeTitle(job({ state: 'handoff' })).includes('进行中'))
    assert.ok(jobBadgeTitle(job({ state: 'failed', code: 'apply' })).includes('没有成功'))
  })
})
