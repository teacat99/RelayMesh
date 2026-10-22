// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { NtfyConfig, NtfyStatus } from '../api/ntfy'
import {
  NTFY_DEFAULT_RUN_MIN_SECONDS,
  NTFY_MAX_RUN_MIN_SECONDS,
  NTFY_RUN_MIN_OPTIONS,
  authModeOf,
  buildUpdate,
  describeCounters,
  describeLast,
  formFromConfig,
  formatRunMinSeconds,
  isDirty,
  isPublicNtfySh,
  privacyWarnings,
  randomTopic,
  runMinOptions,
  subscribeUrl,
  validateForm,
  type NtfyForm,
} from './ntfy'

const baseConfig = (over: Partial<NtfyConfig> = {}): NtfyConfig => ({
  enabled: false,
  server: 'https://ntfy.sh',
  topic: '',
  username: '',
  click_url: '',
  priority: 4,
  include_summary: false,
  delay_seconds: 0,
  run_notify: false,
  run_min_seconds: 60,
  token_set: false,
  password_set: false,
  token_from_env: false,
  ...over,
})

const formOf = (over: Partial<NtfyForm> = {}, saved: NtfyConfig = baseConfig()): NtfyForm => ({ ...formFromConfig(saved), ...over })

describe('authModeOf / formFromConfig', () => {
  it('按已保存的状态推断认证方式', () => {
    assert.equal(authModeOf(baseConfig()), 'none')
    assert.equal(authModeOf(baseConfig({ token_set: true })), 'token')
    assert.equal(authModeOf(baseConfig({ token_from_env: true })), 'token')
    assert.equal(authModeOf(baseConfig({ username: 'bob', password_set: true })), 'basic')
  })

  it('表单里的凭据输入框始终是空的：已保存的凭据不回填', () => {
    const f = formFromConfig(baseConfig({ token_set: true, username: 'bob', password_set: true, topic: 't', priority: 0 }))
    assert.equal(f.token, '')
    assert.equal(f.password, '')
    assert.equal(f.priority, 4, '优先级缺省时回到推荐值')
    assert.equal(f.topic, 't')
  })

  it('服务地址为空时回到公共 ntfy.sh', () => {
    assert.equal(formFromConfig(baseConfig({ server: '' })).server, 'https://ntfy.sh')
  })

  it('运行结束推送：默认关闭 + 60 秒；服务端没给这两项（旧版本）时也是这个默认', () => {
    const f = formFromConfig(baseConfig())
    assert.equal(f.runNotify, false)
    assert.equal(f.runMinSeconds, NTFY_DEFAULT_RUN_MIN_SECONDS)

    const legacy = baseConfig() as Partial<NtfyConfig> // 模拟没有这两个字段的旧响应
    delete legacy.run_notify
    delete legacy.run_min_seconds
    const g = formFromConfig(legacy as NtfyConfig)
    assert.equal(g.runNotify, false)
    assert.equal(g.runMinSeconds, 60)

    const on = formFromConfig(baseConfig({ run_notify: true, run_min_seconds: 300 }))
    assert.equal(on.runNotify, true)
    assert.equal(on.runMinSeconds, 300)
  })
})

describe('buildUpdate', () => {
  it('无认证：把令牌与密码都清掉，用户名置空', () => {
    const u = buildUpdate(formOf({ authMode: 'none', topic: ' t1 ', server: ' https://ntfy.sh ' }), baseConfig({ token_set: true, password_set: true }))
    assert.equal(u.clear_token, true)
    assert.equal(u.clear_password, true)
    assert.equal(u.username, '')
    assert.equal(u.token, undefined)
    assert.equal(u.topic, 't1')
    assert.equal(u.server, 'https://ntfy.sh')
  })

  it('令牌：没输入新令牌就不提交（服务端保持原值），只清掉另一种认证的密码', () => {
    const u = buildUpdate(formOf({ authMode: 'token' }), baseConfig({ token_set: true }))
    assert.equal(u.token, undefined)
    assert.equal(u.clear_token, undefined)
    assert.equal(u.clear_password, true)
    assert.equal(u.username, '')
  })

  it('令牌：输入了新令牌才替换', () => {
    const u = buildUpdate(formOf({ authMode: 'token', token: ' tk_new ' }), baseConfig({ token_set: true }))
    assert.equal(u.token, 'tk_new')
  })

  it('令牌来自环境变量：库里不必保留令牌，提交时顺手清掉', () => {
    const u = buildUpdate(formOf({ authMode: 'token' }, baseConfig({ token_from_env: true })), baseConfig({ token_from_env: true }))
    assert.equal(u.clear_token, true)
  })

  it('用户名密码：密码留空不提交，清掉令牌', () => {
    const saved = baseConfig({ username: 'bob', password_set: true })
    const u = buildUpdate(formOf({ authMode: 'basic' }, saved), saved)
    assert.equal(u.username, 'bob')
    assert.equal(u.password, undefined)
    assert.equal(u.clear_password, undefined)
    assert.equal(u.clear_token, true)

    const u2 = buildUpdate(formOf({ authMode: 'basic', username: 'alice', password: 'pw' }, saved), saved)
    assert.equal(u2.username, 'alice')
    assert.equal(u2.password, 'pw')
  })

  it('数值与开关原样带上', () => {
    const u = buildUpdate(formOf({ enabled: true, priority: 5, includeSummary: true, delaySeconds: 60, clickUrl: ' https://rm.example.com/ ' }), baseConfig())
    assert.equal(u.enabled, true)
    assert.equal(u.priority, 5)
    assert.equal(u.include_summary, true)
    assert.equal(u.delay_seconds, 60)
    assert.equal(u.click_url, 'https://rm.example.com/')
  })

  it('运行结束推送的两项原样带上（关着时也带门槛：整张表单整体替换）', () => {
    const on = buildUpdate(formOf({ runNotify: true, runMinSeconds: 600 }), baseConfig())
    assert.equal(on.run_notify, true)
    assert.equal(on.run_min_seconds, 600)
    const off = buildUpdate(formOf({ runNotify: false, runMinSeconds: 120 }), baseConfig())
    assert.equal(off.run_notify, false)
    assert.equal(off.run_min_seconds, 120)
  })
})

describe('validateForm', () => {
  const ok = (f: NtfyForm, saved = baseConfig(), requireTopic = true) => validateForm(f, saved, requireTopic)

  it('合法的表单通过', () => {
    assert.equal(ok(formOf({ topic: 'relaymesh-abc' })), '')
  })

  it('服务地址：必须带协议，不能有空格和用户信息', () => {
    assert.match(ok(formOf({ topic: 't', server: 'ntfy.sh' })), /http:\/\//)
    assert.match(ok(formOf({ topic: 't', server: 'localhost:8080' })), /http:\/\//)
    assert.match(ok(formOf({ topic: 't', server: 'https://ntfy .sh' })), /空格/)
    assert.match(ok(formOf({ topic: 't', server: 'https://user:pw@ntfy.sh' })), /用户名密码/)
    assert.equal(ok(formOf({ topic: 't', server: 'http://192.168.1.5:8080/ntfy' })), '')
  })

  it('主题：必填只在需要时；字符集与长度受限', () => {
    assert.match(ok(formOf({ topic: '' })), /主题/)
    assert.equal(ok(formOf({ topic: '' }), baseConfig(), false), '')
    assert.match(ok(formOf({ topic: 'a b' })), /字母、数字/)
    assert.match(ok(formOf({ topic: 'a/b' })), /字母、数字/)
    assert.match(ok(formOf({ topic: 'x'.repeat(65) })), /64/)
    assert.equal(ok(formOf({ topic: 'x'.repeat(64) })), '')
  })

  it('令牌认证：已保存或环境变量提供就不必再填', () => {
    assert.match(ok(formOf({ topic: 't', authMode: 'token' })), /令牌/)
    assert.equal(ok(formOf({ topic: 't', authMode: 'token' }, baseConfig({ token_set: true })), baseConfig({ token_set: true })), '')
    assert.equal(ok(formOf({ topic: 't', authMode: 'token' }, baseConfig({ token_from_env: true })), baseConfig({ token_from_env: true })), '')
    assert.equal(ok(formOf({ topic: 't', authMode: 'token', token: 'tk_x' })), '')
  })

  it('用户名密码认证：用户名必填且无冒号；改了用户名必须重填密码', () => {
    const saved = baseConfig({ username: 'bob', password_set: true })
    assert.match(ok(formOf({ topic: 't', authMode: 'basic', username: '' }), saved), /用户名/)
    assert.match(ok(formOf({ topic: 't', authMode: 'basic', username: 'a:b', password: 'p' }), saved), /冒号/)
    assert.equal(ok(formOf({ topic: 't', authMode: 'basic' }, saved), saved), '', '用户名没变、已保存密码：通过')
    assert.match(ok(formOf({ topic: 't', authMode: 'basic', username: 'alice' }, saved), saved), /密码/)
    assert.equal(ok(formOf({ topic: 't', authMode: 'basic', username: 'alice', password: 'pw' }, saved), saved), '')
    assert.match(ok(formOf({ topic: 't', authMode: 'basic', username: 'bob' }), baseConfig()), /密码/, '从没保存过密码：必须填')
  })

  it('点击地址与延迟', () => {
    assert.match(ok(formOf({ topic: 't', clickUrl: 'javascript:alert(1)' })), /http/)
    assert.equal(ok(formOf({ topic: 't', clickUrl: 'https://rm.example.com/#a' })), '')
    assert.match(ok(formOf({ topic: 't', delaySeconds: -1 })), /延迟/)
    assert.match(ok(formOf({ topic: 't', delaySeconds: 901 })), /延迟/)
    assert.equal(ok(formOf({ topic: 't', delaySeconds: 900 })), '')
  })

  it('运行结束推送的门槛：1 ~ 86400 秒的整数', () => {
    assert.equal(ok(formOf({ topic: 't', runNotify: true, runMinSeconds: 1 })), '')
    assert.equal(ok(formOf({ topic: 't', runNotify: true, runMinSeconds: NTFY_MAX_RUN_MIN_SECONDS })), '')
    for (const bad of [0, -5, NTFY_MAX_RUN_MIN_SECONDS + 1, 1.5, Number.NaN]) {
      assert.match(ok(formOf({ topic: 't', runNotify: true, runMinSeconds: bad })), /最短运行时长/, `应拒绝 ${bad}`)
    }
    // 没打开时也要合法：这个值会随整张表单存进库里
    assert.match(ok(formOf({ topic: 't', runNotify: false, runMinSeconds: 0 })), /最短运行时长/)
  })
})

describe('isDirty', () => {
  const saved = baseConfig({ enabled: true, topic: 'rm-1', token_set: true, click_url: 'https://rm.example.com/' })

  it('原样的表单没有改动', () => {
    assert.equal(isDirty(formFromConfig(saved), saved), false)
  })

  it('任何一项变化都算改动，凭据输入框里有内容也算', () => {
    const base = formFromConfig(saved)
    assert.equal(isDirty({ ...base, enabled: false }, saved), true)
    assert.equal(isDirty({ ...base, topic: 'rm-2' }, saved), true)
    assert.equal(isDirty({ ...base, token: 'tk_new' }, saved), true)
    assert.equal(isDirty({ ...base, authMode: 'none' }, saved), true)
    assert.equal(isDirty({ ...base, priority: 5 }, saved), true)
    assert.equal(isDirty({ ...base, delaySeconds: 30 }, saved), true)
    assert.equal(isDirty({ ...base, includeSummary: true }, saved), true)
    assert.equal(isDirty({ ...base, clickUrl: '' }, saved), true)
    assert.equal(isDirty({ ...base, runNotify: true }, saved), true)
    assert.equal(isDirty({ ...base, runMinSeconds: 300 }, saved), true)
    assert.equal(isDirty({ ...base, topic: ' rm-1 ' }, saved), false, '首尾空白不算改动')
  })
})

describe('运行结束推送的门槛选项', () => {
  it('预设里有 1 分钟（默认值），并且按从小到大排列', () => {
    assert.ok(NTFY_RUN_MIN_OPTIONS.some(o => o.value === NTFY_DEFAULT_RUN_MIN_SECONDS), '默认值必须在预设里')
    const values = NTFY_RUN_MIN_OPTIONS.map(o => o.value)
    assert.deepEqual(values, [...values].sort((a, b) => a - b))
    assert.ok(values.every(v => v >= 1 && v <= NTFY_MAX_RUN_MIN_SECONDS))
  })

  it('当前值在预设里：选项就是预设本身', () => {
    assert.deepEqual(runMinOptions(300), [...NTFY_RUN_MIN_OPTIONS])
  })

  it('当前值不在预设里（比如经接口设成 1 秒）：补一项显示它，否则下拉框会悄悄显示成别的值', () => {
    const opts = runMinOptions(1)
    assert.equal(opts.length, NTFY_RUN_MIN_OPTIONS.length + 1)
    assert.equal(opts[0].value, 1, '自定义值按大小排进去')
    assert.match(opts[0].label, /1 秒/)
    const mid = runMinOptions(90)
    assert.ok(mid.some(o => o.value === 90 && /90 秒/.test(o.label)))
    assert.deepEqual(mid.map(o => o.value), [...mid.map(o => o.value)].sort((a, b) => a - b))
  })

  it('时长的写法', () => {
    assert.equal(formatRunMinSeconds(1), '1 秒')
    assert.equal(formatRunMinSeconds(45), '45 秒')
    assert.equal(formatRunMinSeconds(60), '1 分钟')
    assert.equal(formatRunMinSeconds(90), '90 秒')
    assert.equal(formatRunMinSeconds(120), '2 分钟')
    assert.equal(formatRunMinSeconds(3600), '1 小时')
    assert.equal(formatRunMinSeconds(5400), '90 分钟')
    assert.equal(formatRunMinSeconds(86400), '24 小时')
  })
})

describe('randomTopic', () => {
  it('以 relaymesh- 开头，主题字符集合法，长度够长', () => {
    const t = randomTopic()
    assert.match(t, /^relaymesh-[a-z2-9_]{24}$/)
    assert.ok(/^[A-Za-z0-9_-]{1,64}$/.test(t), '必须满足 ntfy 的主题规则')
  })

  it('每次都不同（来自密码学随机数）', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 200; i++) seen.add(randomTopic())
    assert.equal(seen.size, 200)
  })

  it('字母表里没有容易看错的字符', () => {
    const t = randomTopic(bytes => {
      for (let i = 0; i < bytes.length; i++) bytes[i] = i * 11
      return bytes
    })
    assert.ok(!/[01oOlLiI]/.test(t.slice('relaymesh-'.length)), t)
  })
})

describe('subscribeUrl / isPublicNtfySh', () => {
  it('拼出订阅地址，去掉重复斜杠；没有主题时为空', () => {
    assert.equal(subscribeUrl('https://ntfy.sh/', ' abc '), 'https://ntfy.sh/abc')
    assert.equal(subscribeUrl('', 'abc'), 'https://ntfy.sh/abc')
    assert.equal(subscribeUrl('http://nas:8080/ntfy', 'abc'), 'http://nas:8080/ntfy/abc')
    assert.equal(subscribeUrl('https://ntfy.sh', ' '), '')
  })

  it('识别公共服务', () => {
    assert.equal(isPublicNtfySh('https://ntfy.sh'), true)
    assert.equal(isPublicNtfySh('https://NTFY.SH/'), true)
    assert.equal(isPublicNtfySh(''), true, '留空就是用公共服务')
    assert.equal(isPublicNtfySh('https://ntfy.example.com'), false)
    assert.equal(isPublicNtfySh('not a url'), false)
  })
})

describe('privacyWarnings', () => {
  it('公共服务提醒主题等同口令；主题太短、附带摘要还会各加一条', () => {
    const f = formOf({ topic: 'short', includeSummary: true })
    const w = privacyWarnings(f)
    assert.equal(w.length, 3)
    assert.match(w[0], /等同于口令/)
    assert.match(w[1], /偏短/)
    assert.match(w[2], /第三方/)
  })

  it('长随机主题且只发标题：只剩口令提醒', () => {
    assert.equal(privacyWarnings(formOf({ topic: randomTopic() })).length, 1)
  })

  it('附带摘要的提醒也要说明：运行结束的推送里它是命令的第一行', () => {
    const w = privacyWarnings(formOf({ topic: randomTopic(), includeSummary: true }))
    assert.equal(w.length, 2)
    assert.match(w[1], /命令的第一行/)
    assert.match(w[1], /第三方/)
  })

  it('自建服务不提醒公共服务的事；明文 http 带凭据到公网地址要警告，内网不用', () => {
    assert.deepEqual(privacyWarnings(formOf({ server: 'https://ntfy.example.com', topic: 'x' })), [])
    const lan = privacyWarnings(formOf({ server: 'http://192.168.1.5:8080', topic: 'x', enabled: true, authMode: 'token' }))
    assert.deepEqual(lan, [])
    const wan = privacyWarnings(formOf({ server: 'http://ntfy.example.com', topic: 'x', enabled: true, authMode: 'basic' }))
    assert.equal(wan.length, 1)
    assert.match(wan[0], /明文/)
  })
})

describe('describeLast / describeCounters', () => {
  const status = (over: Partial<NtfyStatus> = {}): NtfyStatus => ({ last_ok: false, last_test: false, sent: 0, failed: 0, dropped: 0, ...over })

  it('没有记录', () => {
    assert.equal(describeLast(null).tone, 'none')
    assert.equal(describeLast(status()).tone, 'none')
  })

  it('成功与失败；测试推送单独标注', () => {
    const at = new Date(2026, 9, 9, 14, 3, 5).toISOString()
    const ok = describeLast(status({ last_attempt_at: at, last_ok: true }))
    assert.equal(ok.tone, 'ok')
    assert.match(ok.text, /推送成功 · 14:03:05/)
    const test = describeLast(status({ last_attempt_at: at, last_ok: true, last_test: true }))
    assert.match(test.text, /测试推送成功/)
    const fail = describeLast(status({ last_attempt_at: at, last_error: '认证失败（HTTP 403）' }))
    assert.equal(fail.tone, 'fail')
    assert.match(fail.text, /失败 · 14:03:05：认证失败（HTTP 403）/)
    assert.match(describeLast(status({ last_attempt_at: at })).text, /原因未知/)
  })

  it('累计数字', () => {
    assert.equal(describeCounters(null), '')
    assert.equal(describeCounters(status()), '')
    assert.equal(describeCounters(status({ sent: 3, failed: 1, dropped: 2 })), '已推送 3 条 · 失败 1 条 · 被限流合并 / 丢弃 2 条')
  })
})
