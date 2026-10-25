// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { NtfyConfig, NtfyKindInfo, NtfyStatus } from '../api/ntfy'
import {
  NTFY_DEFAULT_RUN_MIN_SECONDS,
  NTFY_MAX_RUN_MIN_SECONDS,
  NTFY_RUN_MIN_OPTIONS,
  authModeOf,
  buildUpdate,
  builtinPriority,
  describeCounters,
  describeKindTable,
  describeKindTest,
  describeLast,
  effectiveKind,
  formFromConfig,
  formatRunMinSeconds,
  groupKinds,
  isDirty,
  isPublicNtfySh,
  kindChoice,
  kindOptions,
  priorityLabel,
  privacyWarnings,
  randomTopic,
  runMinOptions,
  subscribeUrl,
  validateForm,
  withKindChoice,
  type KindChoice,
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

// ───────────────────── 按消息类型设置优先级 ─────────────────────

const kindInfo = (over: Partial<NtfyKindInfo> & { id: string }): NtfyKindInfo => ({
  group: 'need_you',
  label: over.id,
  desc: '说明',
  default_on: true,
  default_priority: 0,
  ...over,
})

// 与服务端目录同构的一小撮：固定默认 / 跟随默认优先级 / 跟随且封顶 / 默认关闭的异常类
const REQUEST = kindInfo({ id: 'request', label: '智能体在等你回复' })
const MENTION = kindInfo({ id: 'mention', label: '群里 @ 了你', default_priority: 4 })
const ASK_CARD = kindInfo({ id: 'ask_card', label: '新的待确认提问', default_priority: 3 })
const RUN_OK = kindInfo({ id: 'run_ok', group: 'run', label: '模型命令顺利完成', cap_normal: true })
const RUN_TROUBLE = kindInfo({ id: 'run_trouble', group: 'run', label: '模型命令出状况' })
const AGENT_ERROR = kindInfo({ id: 'agent_error', group: 'exception', label: 'Agent 出错或被中断', default_on: false, suggested_priority: 4 })
const SECURITY = kindInfo({ id: 'security_alert', group: 'exception', label: '安全告警', default_on: false, suggested_priority: 5 })
const KINDS = [REQUEST, MENTION, ASK_CARD, RUN_OK, RUN_TROUBLE, AGENT_ERROR, SECURITY]

describe('priorityLabel', () => {
  it('中文名加数字，可带备注', () => {
    assert.equal(priorityLabel(1), '最低（1）')
    assert.equal(priorityLabel(3), '默认（3）')
    assert.equal(priorityLabel(4), '较高（4）')
    assert.equal(priorityLabel(5, '建议'), '紧急（5，建议）')
  })

  it('越界或不是数字时收敛到合法范围，不会出现「undefined（9）」', () => {
    assert.equal(priorityLabel(9), '紧急（5）')
    assert.equal(priorityLabel(0), '最低（1）')
    assert.equal(priorityLabel(Number.NaN), '较高（4）', '不是数字时回到推荐值')
  })
})

describe('按消息类型的覆盖表：表单往返', () => {
  it('老服务端没有这个字段：按空表处理，提交体里是 {}（表示没有任何覆盖）', () => {
    const saved = baseConfig()
    assert.equal('priority_by_kind' in saved, false)
    const f = formFromConfig(saved)
    assert.deepEqual(f.priorityByKind, {})
    assert.deepEqual(buildUpdate(f, saved).priority_by_kind, {})
  })

  it('已保存的覆盖表带进表单，提交时整张带上；表单与配置互不牵连', () => {
    const saved = baseConfig({ priority_by_kind: { mention: 2, run_ok: 0 } })
    const f = formFromConfig(saved)
    assert.deepEqual({ ...f.priorityByKind }, { mention: 2, run_ok: 0 })
    f.priorityByKind.ask_card = 5
    assert.equal('ask_card' in (saved.priority_by_kind ?? {}), false, '改表单不能碰到已保存的配置')
    const out = buildUpdate(f, saved)
    assert.deepEqual(out.priority_by_kind, { mention: 2, run_ok: 0, ask_card: 5 })
    out.priority_by_kind!.mention = 1
    assert.equal(f.priorityByKind.mention, 2, '提交体也是副本')
  })

  it('isDirty：覆盖表变化算改动，键的顺序不同不算', () => {
    const saved = baseConfig({ priority_by_kind: { mention: 2, run_ok: 0 } })
    const base = formFromConfig(saved)
    assert.equal(isDirty(base, saved), false)
    assert.equal(isDirty({ ...base, priorityByKind: { run_ok: 0, mention: 2 } }, saved), false, '键顺序不同不算改动')
    assert.equal(isDirty({ ...base, priorityByKind: { mention: 3, run_ok: 0 } }, saved), true)
    assert.equal(isDirty({ ...base, priorityByKind: { mention: 2 } }, saved), true, '删掉一项算改动')
    assert.equal(isDirty({ ...base, priorityByKind: { mention: 2, run_ok: 0, ask_card: 1 } }, saved), true, '多一项算改动')
    assert.equal(isDirty({ ...base, priorityByKind: { mention: 2, run_ok: 1 } }, saved), true, '0 与 1 不同')
  })

  it('validateForm：覆盖表里的值必须是 0~5 的整数', () => {
    const ok = (priorityByKind: Record<string, number>) => validateForm(formOf({ topic: 't', priorityByKind }), baseConfig(), true)
    assert.equal(ok({}), '')
    assert.equal(ok({ mention: 0, run_ok: 5, ask_card: 1 }), '')
    for (const bad of [-1, 6, 2.5, Number.NaN]) {
      assert.match(ok({ mention: bad }), /mention.*不合法/, `应拒绝 ${bad}`)
    }
  })
})

describe('builtinPriority / effectiveKind（与服务端 PriorityFor 同一套规则）', () => {
  it('固定默认的类型不受默认优先级影响', () => {
    for (const dp of [1, 3, 5]) {
      assert.equal(builtinPriority(MENTION, dp), 4)
      assert.equal(builtinPriority(ASK_CARD, dp), 3)
    }
  })

  it('跟随默认优先级的类型随它走，顺利完成的命令最多按 3', () => {
    for (const dp of [1, 2, 3, 4, 5]) {
      assert.equal(builtinPriority(REQUEST, dp), dp)
      assert.equal(builtinPriority(RUN_TROUBLE, dp), dp)
      assert.equal(builtinPriority(RUN_OK, dp), Math.min(dp, 3))
    }
  })

  it('覆盖表优先于内置默认：0 = 不推送，1~5 = 指定，越界收敛', () => {
    assert.deepEqual(effectiveKind(MENTION, { mention: 2 }, 4), { on: true, priority: 2 })
    assert.deepEqual(effectiveKind(MENTION, { mention: 0 }, 4), { on: false, priority: 0 })
    assert.deepEqual(effectiveKind(MENTION, { mention: 9 }, 4), { on: true, priority: 5 })
    assert.deepEqual(effectiveKind(RUN_OK, { run_ok: 5 }, 4), { on: true, priority: 5 }, '明确指定的值不受封顶影响')
    assert.deepEqual(effectiveKind(MENTION, {}, 4), { on: true, priority: 4 })
    assert.deepEqual(effectiveKind(REQUEST, undefined, 2), { on: true, priority: 2 })
  })

  it('默认关闭的异常类：不设不推送，设了 1~5 才推', () => {
    assert.deepEqual(effectiveKind(AGENT_ERROR, {}, 4), { on: false, priority: 0 })
    assert.deepEqual(effectiveKind(AGENT_ERROR, { agent_error: 0 }, 4), { on: false, priority: 0 })
    assert.deepEqual(effectiveKind(AGENT_ERROR, { agent_error: 4 }, 4), { on: true, priority: 4 })
  })
})

describe('kindChoice / withKindChoice', () => {
  it('没设 → 默认；0 → 不推送；1~5 → 对应级别', () => {
    assert.equal(kindChoice(MENTION, undefined), 'default')
    assert.equal(kindChoice(MENTION, {}), 'default')
    assert.equal(kindChoice(MENTION, { mention: 0 }), 'off')
    assert.equal(kindChoice(MENTION, { mention: 2 }), '2')
    assert.equal(kindChoice(MENTION, { mention: 4 }), '4', '与内置默认相同的显式设置仍显示成它自己')
  })

  it('默认关闭的类型设成 0 与「默认」等价：统一显示成默认', () => {
    assert.equal(kindChoice(AGENT_ERROR, { agent_error: 0 }), 'default')
    assert.equal(kindChoice(AGENT_ERROR, { agent_error: 4 }), '4')
  })

  it('脏数据不会让下拉框悬空：越界收敛、非数字当没设', () => {
    assert.equal(kindChoice(MENTION, { mention: 9 }), '5')
    assert.equal(kindChoice(MENTION, { mention: Number.NaN }), 'default')
  })

  it('withKindChoice 返回新对象，不改原表；选默认就是删掉这一项', () => {
    const table = { mention: 2, run_ok: 0 }
    const a = withKindChoice(table, MENTION, '5')
    assert.deepEqual(a, { mention: 5, run_ok: 0 })
    assert.deepEqual(table, { mention: 2, run_ok: 0 }, '原表不变')
    assert.deepEqual(withKindChoice(table, MENTION, 'default'), { run_ok: 0 })
    assert.deepEqual(withKindChoice(table, ASK_CARD, 'off'), { mention: 2, run_ok: 0, ask_card: 0 })
    assert.deepEqual(withKindChoice(undefined, REQUEST, '1'), { request: 1 })
    assert.deepEqual(withKindChoice({}, REQUEST, 'default'), {})
  })

  it('选项 → 覆盖表 → 选项，往返一致', () => {
    const choices: KindChoice[] = ['default', 'off', '1', '2', '3', '4', '5']
    for (const info of [MENTION, RUN_OK, AGENT_ERROR]) {
      for (const c of choices) {
        const back = kindChoice(info, withKindChoice({}, info, c))
        // 默认关闭的类型「不推送」与「默认」等价，往返后统一成默认
        const want = !info.default_on && c === 'off' ? 'default' : c
        assert.equal(back, want, `${info.id} 选 ${c}`)
      }
    }
  })
})

describe('kindOptions', () => {
  const values = (o: Array<{ value: KindChoice }>) => o.map(x => x.value)

  it('默认开启的类型：默认 / 不推送 / 1~5，「默认」一项写明此刻落在几级', () => {
    const o = kindOptions(MENTION, 4)
    assert.deepEqual(values(o), ['default', 'off', '1', '2', '3', '4', '5'])
    assert.equal(o[0].label, '默认 → 较高（4）')
    assert.equal(o[1].label, '不推送')
    assert.equal(o[5].label, '较高（4）')
  })

  it('跟随默认优先级的类型：「默认」一项随默认优先级变化', () => {
    assert.equal(kindOptions(REQUEST, 2)[0].label, '跟随默认优先级 → 较低（2）')
    assert.equal(kindOptions(REQUEST, 5)[0].label, '跟随默认优先级 → 紧急（5）')
  })

  it('顺利完成的命令：默认优先级高于 3 时写明「最多按 3」，不高于 3 时不啰嗦', () => {
    assert.equal(kindOptions(RUN_OK, 4)[0].label, '跟随默认优先级（最多按 3）→ 默认（3）')
    assert.equal(kindOptions(RUN_OK, 2)[0].label, '跟随默认优先级 → 较低（2）')
  })

  it('默认关闭的异常类：「不推送（默认）」打头，没有单独的「不推送」；建议值打上标记', () => {
    const o = kindOptions(AGENT_ERROR, 4)
    assert.deepEqual(values(o), ['default', '1', '2', '3', '4', '5'])
    assert.equal(o[0].label, '不推送（默认）')
    assert.equal(o[4].label, '推送 · 较高（4，建议）')
    assert.equal(o[1].label, '推送 · 最低（1）', '非建议值不带标记')
    const sec = kindOptions(SECURITY, 4)
    assert.equal(sec[5].label, '推送 · 紧急（5，建议）')
  })

  it('每个选项的 value 都能被 kindChoice 认出来（下拉框不会悬空）', () => {
    for (const info of KINDS) {
      for (const o of kindOptions(info, 4)) {
        const table = withKindChoice({}, info, o.value)
        const shown = kindChoice(info, table)
        const ok = shown === o.value || (!info.default_on && o.value === 'off')
        assert.ok(ok, `${info.id}: ${o.value} → ${shown}`)
        assert.ok(kindOptions(info, 4).some(x => x.value === shown), `${info.id}: ${shown} 不在选项里`)
      }
    }
  })
})

describe('groupKinds', () => {
  it('按「需要你处理 / 模型运行的命令 / 异常提醒」排好，组内保持服务端给的顺序', () => {
    const shuffled = [AGENT_ERROR, MENTION, RUN_OK, REQUEST, SECURITY, RUN_TROUBLE, ASK_CARD]
    const g = groupKinds(shuffled)
    assert.deepEqual(g.map(x => x.id), ['need_you', 'run', 'exception'])
    assert.deepEqual(g[0].kinds.map(k => k.id), ['mention', 'request', 'ask_card'])
    assert.deepEqual(g[1].kinds.map(k => k.id), ['run_ok', 'run_trouble'])
    assert.deepEqual(g[2].kinds.map(k => k.id), ['agent_error', 'security_alert'])
    assert.ok(g.every(x => x.title && x.hint), '每组都有标题与说明')
  })

  it('空组不出现；没有目录（老服务端）时返回空', () => {
    assert.deepEqual(groupKinds(undefined), [])
    assert.deepEqual(groupKinds([]), [])
    assert.deepEqual(groupKinds([RUN_OK]).map(x => x.id), ['run'])
  })

  it('服务端新增了界面不认识的分组：归到「其他」，不会悄悄丢掉', () => {
    const future = kindInfo({ id: 'future_kind', group: 'brand_new' as NtfyKindInfo['group'] })
    const g = groupKinds([MENTION, future])
    assert.deepEqual(g.map(x => x.id), ['need_you', 'other'])
    assert.equal(g[1].kinds[0].id, 'future_kind')
  })
})

describe('describeKindTable', () => {
  it('没有目录时不显示小结', () => {
    assert.equal(describeKindTable(undefined, {}, 4), '')
    assert.equal(describeKindTable([], {}, 4), '')
  })

  it('全部默认：异常提醒全部关闭', () => {
    assert.equal(describeKindTable(KINDS, {}, 4), '全部使用默认设置 · 异常提醒全部关闭')
  })

  it('改过几类、关了几类、异常开了几类', () => {
    const t = { mention: 2, run_ok: 0, ask_card: 0, agent_error: 4 }
    assert.equal(describeKindTable(KINDS, t, 4), '已单独设置 4 类 · 2 类已设成不推送 · 异常提醒已开 1 / 2 类')
  })

  it('异常类设成 0（等价于默认）不算改过，也不算「已设成不推送」', () => {
    assert.equal(describeKindTable(KINDS, { agent_error: 0 }, 4), '全部使用默认设置 · 异常提醒全部关闭')
  })
})

describe('describeKindTest', () => {
  it('写明实际用了几级与是哪一类', () => {
    const text = describeKindTest(MENTION, '2', { priority: 2, took_ms: 87 })
    assert.match(text, /已按「较低（2）」试发「群里 @ 了你」/)
    assert.match(text, /87 毫秒/)
    assert.ok(!/预览/.test(text), '正在推送的类型不提示预览')
  })

  it('这一类目前不推送时提醒：这只是预览', () => {
    assert.match(describeKindTest(MENTION, 'off', { priority: 4, took_ms: 10 }), /这一类目前是「不推送」，这条只是预览/)
    assert.match(describeKindTest(SECURITY, 'default', { priority: 5, took_ms: 10 }), /这一类目前是「不推送」，这条只是预览/)
    assert.ok(!/预览/.test(describeKindTest(SECURITY, '5', { priority: 5, took_ms: 10 })), '已经打开的异常类不提示')
  })

  it('服务端没回优先级（旧版本）时不报错', () => {
    assert.match(describeKindTest(MENTION, 'default', { took_ms: 5 }), /默认优先级/)
  })
})
