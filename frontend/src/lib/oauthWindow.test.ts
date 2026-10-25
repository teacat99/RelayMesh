// 运行：pnpm --dir frontend run test:unit
// OAuth 接入窗口与「刚登记、还没授权的客户端」的纯逻辑（#239）：自动注册方式与策略的互相转换、窗口卡片的状态与按钮、
// 清单里每条登记的标记与说明。服务端口径见 internal/oauth/dcr_window.go。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { OAuthClientRow, OAuthDcrStatus, OAuthEffective, OAuthPolicy } from '../api/client'
import {
  clientBadges,
  clientHostsText,
  clientRegisteredText,
  DCR_MODE_OPTIONS,
  dcrModeLabel,
  dcrModeOf,
  emptyClientsText,
  remainingSecondsNow,
  WINDOW_SCOPE_NOTE,
  WINDOW_WARN_SECONDS,
  windowView,
  withDcrMode,
} from './oauthWindow'

function policy(over: Partial<OAuthPolicy> = {}): OAuthPolicy {
  return {
    enabled: true,
    public_url: 'https://hub.example.com',
    allow_dcr: true,
    dcr_always: false,
    access_token_ttl_minutes: 60,
    refresh_idle_days: 30,
    refresh_absolute_days: 90,
    ...over,
  }
}

function dcr(over: Partial<OAuthDcrStatus> = {}): OAuthDcrStatus {
  return { mode: 'window', open: false, remaining_seconds: 0, default_minutes: 10, max_minutes: 60, ...over }
}

function effective(over: Partial<OAuthEffective> = {}, d: Partial<OAuthDcrStatus> = {}): OAuthEffective {
  const base = 'https://hub.example.com'
  return {
    env_off: false,
    enabled: true,
    public_base: base,
    public_base_source: 'settings',
    settings_public_url_shadowed: false,
    connector_url: `${base}/mcp`,
    resource_metadata_url: `${base}/.well-known/oauth-protected-resource/mcp`,
    authorization_server_metadata_url: `${base}/.well-known/oauth-authorization-server`,
    dcr: dcr(d),
    ...over,
  }
}

const T0 = Date.parse('2026-10-22T12:00:00Z')

describe('自动注册方式 ↔ 策略字段', () => {
  it('dcrModeOf：关闭 / 限时窗口 / 常开；dcr_always 只在自动注册打开时有意义', () => {
    assert.equal(dcrModeOf({ allow_dcr: false, dcr_always: false }), 'off')
    assert.equal(dcrModeOf({ allow_dcr: false, dcr_always: true }), 'off')
    assert.equal(dcrModeOf({ allow_dcr: true, dcr_always: false }), 'window')
    assert.equal(dcrModeOf({ allow_dcr: true, dcr_always: true }), 'always')
  })
  it('旧后端的策略没有 dcr_always：自动注册打开时按限时窗口算（与服务端的升级口径一致）', () => {
    assert.equal(dcrModeOf({ allow_dcr: true }), 'window')
    assert.equal(dcrModeOf({ allow_dcr: false }), 'off')
  })
  it('withDcrMode：返回新对象、不改入参，只动这两个字段', () => {
    const before = policy({ allow_dcr: true, dcr_always: false, public_url: 'https://keep.example.com', access_token_ttl_minutes: 240 })
    const snapshot = JSON.stringify(before)
    const always = withDcrMode(before, 'always')
    assert.equal(JSON.stringify(before), snapshot)
    assert.notEqual(always, before)
    assert.deepEqual([always.allow_dcr, always.dcr_always], [true, true])
    assert.deepEqual([withDcrMode(before, 'off').allow_dcr, withDcrMode(before, 'off').dcr_always], [false, false])
    assert.deepEqual([withDcrMode(always, 'window').allow_dcr, withDcrMode(always, 'window').dcr_always], [true, false])
    assert.equal(always.public_url, 'https://keep.example.com')
    assert.equal(always.access_token_ttl_minutes, 240)
  })
  it('三种方式互转后读回来还是原来的方式', () => {
    for (const o of DCR_MODE_OPTIONS) assert.equal(dcrModeOf(withDcrMode(policy(), o.id)), o.id)
  })
  it('三个选项：顺序是关闭 / 限时窗口 / 常开，限时窗口标成推荐，每项都有说明', () => {
    assert.deepEqual(DCR_MODE_OPTIONS.map(o => o.id), ['off', 'window', 'always'])
    assert.match(dcrModeLabel('window'), /推荐/)
    for (const o of DCR_MODE_OPTIONS) {
      assert.ok(o.label.trim().length > 0 && o.hint.trim().length > 0, o.id)
      assert.equal(o.hint.includes('**'), false, `${o.id}：文字按纯文本显示，不能带 markdown 的星号`)
    }
    assert.match(DCR_MODE_OPTIONS.find(o => o.id === 'always')!.hint, /不建议/)
  })
})

describe('remainingSecondsNow：以收到状态的那一刻为基准本地递减', () => {
  it('刚收到时等于服务端给的剩余秒数', () => {
    assert.equal(remainingSecondsNow(600, T0, T0), 600)
  })
  it('随时间递减，向上取整（到 0 之前显示的最小值是 1）', () => {
    assert.equal(remainingSecondsNow(600, T0, T0 + 1000), 599)
    assert.equal(remainingSecondsNow(600, T0, T0 + 1200), 599)
    assert.equal(remainingSecondsNow(10, T0, T0 + 9500), 1)
  })
  it('到点与过点都是 0，不会出现负数；服务端给了负数也按 0 算', () => {
    assert.equal(remainingSecondsNow(10, T0, T0 + 10_000), 0)
    assert.equal(remainingSecondsNow(10, T0, T0 + 99_000), 0)
    assert.equal(remainingSecondsNow(-5, T0, T0), 0)
  })
})

describe('windowView：接入窗口卡片', () => {
  it('没有状态（旧后端没有 effective / effective.dcr）、或被环境变量整个关闭：不显示卡片', () => {
    assert.equal(windowView(null, T0, T0), null)
    assert.equal(windowView(undefined, T0, T0), null)
    const noDcr = effective()
    delete noDcr.dcr
    assert.equal(windowView(noDcr, T0, T0), null)
    assert.equal(windowView(effective({ env_off: true, enabled: false }), T0, T0), null)
  })

  it('授权服务器没开：说明先去开总开关，不给任何按钮', () => {
    const v = windowView(effective({ enabled: false }, { mode: 'window', open: false }), T0, T0)!
    assert.equal(v.kind, 'disabled')
    assert.equal(v.tone, 'muted')
    assert.match(v.detail, /启用 OAuth 授权服务器/)
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [false, false, false])
  })

  it('总开关没开优先于自动注册方式：关闭注册且总开关关着，先提示开总开关', () => {
    assert.equal(windowView(effective({ enabled: false }, { mode: 'off' }), T0, T0)!.kind, 'disabled')
  })

  it('自动注册关闭：说明去改成限时窗口，并如实说 CIMD 客户端不受影响，不给按钮', () => {
    const v = windowView(effective({}, { mode: 'off' }), T0, T0)!
    assert.equal(v.kind, 'off')
    assert.match(v.detail, /限时窗口/)
    assert.match(v.detail, /CIMD/)
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [false, false, false])
  })

  it('常开：警告色，说明不需要窗口、也不建议长期这样，不给按钮', () => {
    const v = windowView(effective({}, { mode: 'always', open: true }), T0, T0)!
    assert.equal(v.kind, 'always')
    assert.equal(v.tone, 'warn')
    assert.match(v.detail, /不需要开窗口/)
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [false, false, false])
  })

  it('限时窗口关着：说明登记会被拒绝（403），只给「开启」', () => {
    const v = windowView(effective({}, { mode: 'window', open: false }), T0, T0)!
    assert.equal(v.kind, 'closed')
    assert.equal(v.title, '已关闭')
    assert.match(v.detail, /403/)
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [true, false, false])
    assert.equal(v.minutes, 10)
  })

  it('限时窗口开着：绿色倒计时，可续期、可关闭，不能再「开启」', () => {
    const v = windowView(effective({}, { mode: 'window', open: true, remaining_seconds: 541 }), T0, T0)!
    assert.equal(v.kind, 'open')
    assert.equal(v.tone, 'ok')
    assert.equal(v.title, '开启中 · 剩余 9:01')
    assert.equal(v.remainingSeconds, 541)
    assert.equal(v.ending, false)
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [false, true, true])
  })

  it('倒计时随本地时间推进；剩余不足 2 分钟标成琥珀色并提示续期', () => {
    const e = effective({}, { mode: 'window', open: true, remaining_seconds: 150 })
    const early = windowView(e, T0, T0 + 20_000)!
    assert.equal(early.title, '开启中 · 剩余 2:10')
    assert.equal(early.ending, false)
    const late = windowView(e, T0, T0 + 40_000)!
    assert.equal(late.title, '开启中 · 剩余 1:50')
    assert.equal(late.ending, true)
    assert.equal(late.tone, 'warn')
    assert.match(late.detail, /续期/)
    assert.equal(WINDOW_WARN_SECONDS, 120)
  })

  it('剩余时间走完：本地立刻按「已关闭」显示，不等服务端再确认', () => {
    const e = effective({}, { mode: 'window', open: true, remaining_seconds: 30 })
    assert.equal(windowView(e, T0, T0 + 29_000)!.kind, 'open')
    const v = windowView(e, T0, T0 + 30_000)!
    assert.equal(v.kind, 'closed')
    assert.deepEqual([v.canOpen, v.canExtend, v.canClose], [true, false, false])
  })

  it('已经开到上限（总剩余接近 60 分钟）：不能再续期，但还能关闭', () => {
    const v = windowView(effective({}, { mode: 'window', open: true, remaining_seconds: 3598 }), T0, T0)!
    assert.equal(v.canExtend, false)
    assert.equal(v.canClose, true)
    assert.equal(windowView(effective({}, { mode: 'window', open: true, remaining_seconds: 3000 }), T0, T0)!.canExtend, true)
  })

  it('按钮分钟数跟着服务端给的默认时长走，缺省或非法时用 10', () => {
    assert.equal(windowView(effective({}, { default_minutes: 15 }), T0, T0)!.minutes, 15)
    assert.equal(windowView(effective({}, { default_minutes: 0 }), T0, T0)!.minutes, 10)
  })

  it('所有状态的文字按纯文本显示（不带 markdown 星号），并且「窗口只管自动注册」的边界一直说明白', () => {
    const states: OAuthEffective[] = [
      effective({ enabled: false }),
      effective({}, { mode: 'off' }),
      effective({}, { mode: 'always', open: true }),
      effective({}, { mode: 'window', open: false }),
      effective({}, { mode: 'window', open: true, remaining_seconds: 300 }),
    ]
    for (const e of states) {
      const v = windowView(e, T0, T0)!
      assert.equal((v.title + v.detail).includes('**'), false, v.kind)
      assert.ok(v.title.length > 0 && v.detail.length > 0, v.kind)
    }
    assert.match(WINDOW_SCOPE_NOTE, /CIMD/)
    assert.match(WINDOW_SCOPE_NOTE, /批准/)
  })
})

function client(over: Partial<OAuthClientRow> = {}): OAuthClientRow {
  return {
    id: '0123456789abcdef0123456789abcdef',
    client_name: 'ChatGPT',
    redirect_hosts: ['chatgpt.com'],
    untrusted: false,
    loopback: false,
    created_at: '2026-10-22T11:57:00Z',
    created_ip: '203.0.113.9',
    ...over,
  }
}

describe('clientBadges：来源标记', () => {
  const ids = (c: Partial<OAuthClientRow>) => clientBadges(client(c)).map(b => b.id)
  it('回调在官方域名下：官方来源（绿色）', () => {
    const b = clientBadges(client())
    assert.deepEqual(b.map(x => x.id), ['official'])
    assert.equal(b[0].tone, 'ok')
  })
  it('陌生域名：陌生来源（琥珀色），没有官方标记', () => {
    const b = clientBadges(client({ untrusted: true, redirect_hosts: ['tools.example.com'] }))
    assert.deepEqual(b.map(x => x.id), ['unknown'])
    assert.equal(b[0].tone, 'warn')
  })
  it('本机回环：只显示「本机回环」，不再重复显示「陌生来源」（回环按批准页的规则一定算陌生，但这样更清楚）', () => {
    assert.deepEqual(ids({ untrusted: true, loopback: true, redirect_hosts: ['127.0.0.1'] }), ['loopback'])
  })
  it('每个标记都有悬停说明', () => {
    for (const c of [{}, { untrusted: true }, { untrusted: true, loopback: true }]) {
      for (const b of clientBadges(client(c))) assert.ok(b.title.length > 0 && b.label.length > 0, b.id)
    }
  })
})

describe('清单里每条登记的文字', () => {
  it('回调主机：多个用顿号连接，没有时说明', () => {
    assert.equal(clientHostsText(client({ redirect_hosts: ['claude.ai', 'claude.com'] })), '回调：claude.ai、claude.com')
    assert.equal(clientHostsText(client({ redirect_hosts: [] })), '没有回调地址')
    assert.equal(clientHostsText({ redirect_hosts: undefined as unknown as string[] }), '没有回调地址')
  })
  it('登记时间与来源地址；升级前登记的没有来源地址', () => {
    const now = Date.parse('2026-10-22T12:00:00Z')
    assert.equal(clientRegisteredText(client(), now), '登记于 3 分钟前 · 来自 203.0.113.9')
    assert.equal(clientRegisteredText(client({ created_ip: '' }), now), '登记于 3 分钟前')
    assert.equal(clientRegisteredText(client({ created_ip: '  ' }), now), '登记于 3 分钟前')
    assert.equal(clientRegisteredText(client({ created_at: '2026-10-22T11:59:58Z' }), now), '登记于 刚刚 · 来自 203.0.113.9')
  })
  it('空状态按窗口此刻的样子给出下一步', () => {
    assert.match(emptyClientsText('open'), /添加连接器/)
    assert.match(emptyClientsText('closed'), /开启上面的接入窗口/)
    for (const k of ['always', 'off', 'disabled', null] as const) assert.match(emptyClientsText(k), /没有/)
  })
})
