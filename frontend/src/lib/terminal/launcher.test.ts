// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, SessionInfo, TerminalLauncher } from '../../api/terminal'
import {
  AUTO_RESUME_OK_DEFAULT_MINUTES,
  AUTO_RESUME_OK_MAX_MINUTES,
  CURSOR_CLI_COMMAND,
  CURSOR_CLI_RESUME_NAME,
  LAUNCHER_DEFAULT_LIMITS,
  READY_MAX_WAIT_MS,
  READY_QUIET_MS,
  blankDraft,
  blankRunDraft,
  charCount,
  collapseSpaces,
  cursorCliDraft,
  dirAdvice,
  dirPlaceholder,
  draftOf,
  hasErrors,
  headlessDraft,
  isHeadlessPreset,
  hostPlatformOf,
  hostReadiness,
  isMacHost,
  isWindowsHost,
  launcherHosts,
  launcherLimits,
  maxRunFields,
  newLauncherId,
  normalizeName,
  parseAutoResumeOk,
  parseMaxRun,
  psQuote,
  quoteDir,
  readyToType,
  runningSessionsOf,
  sessionCwd,
  shQuote,
  shellKindOf,
  startLine,
  supportsExitEndsSession,
  toLauncher,
  validateDraft,
  validateRunOptions,
  windowsDir,
  type RunDraft,
} from './launcher'

const HOST = 'RM-7K2F-9XQ4-M3WD-8ABC'
const limits = LAUNCHER_DEFAULT_LIMITS

function host(over: Partial<HostView> = {}): HostView {
  return {
    machine_code: HOST,
    remark: '',
    status: 'enabled',
    temporary: false,
    online: true,
    hostname: 'devhost',
    os: 'linux',
    arch: 'amd64',
    agent_version: '1.0.0',
    privilege: 'root',
    last_ip: '10.0.0.1',
    sessions: [],
    ...over,
  } as HostView
}

function sess(sid: string, name: string, over: Partial<SessionInfo> = {}): SessionInfo {
  return { sid, name, rows: 24, cols: 80, base: 0, end: 0, state: 'running', attached: 0, ...over }
}

const ok = (over: Partial<TerminalLauncher> = {}): TerminalLauncher => ({ id: 'l-1', name: 'Cursor CLI', host: HOST, dir: '/srv/app', command: 'agent', ...over })

describe('启动器取值范围', () => {
  it('服务端没给就用默认值，给了就用服务端的', () => {
    assert.deepEqual(launcherLimits(undefined), LAUNCHER_DEFAULT_LIMITS)
    assert.deepEqual(launcherLimits(null), LAUNCHER_DEFAULT_LIMITS)
    assert.deepEqual(launcherLimits({ launcher_max: 5, launcher_name_max: 10, launcher_dir_max: 100, launcher_command_max: 200 }), {
      ...LAUNCHER_DEFAULT_LIMITS, max: 5, name: 10, dir: 100, command: 200,
    })
  })
  it('服务端给了不合理的值（0、负数、NaN）回落到默认', () => {
    assert.deepEqual(launcherLimits({ launcher_max: 0, launcher_name_max: -1, launcher_dir_max: Number.NaN }), LAUNCHER_DEFAULT_LIMITS)
  })
  it('非交互续聊的范围与默认提示词：旧版 Hub 没有就是「不认识」，有就按服务端的', () => {
    assert.equal(launcherLimits(undefined).runSupported, false, '旧版 Hub 不认识续聊预设')
    assert.equal(launcherLimits({ launcher_max: 20 }).runSupported, false)
    const l = launcherLimits({ launcher_run_prompt_max: 700, launcher_run_model_max: 40, launcher_run_bin_max: 90, launcher_run_prompt_default: '默认 %workflow' })
    assert.deepEqual(
      { s: l.runSupported, p: l.runPrompt, m: l.runModel, b: l.runBin, d: l.runPromptDefault },
      { s: true, p: 700, m: 40, b: 90, d: '默认 %workflow' },
    )
  })
  it('最长运行时间的上限（#141）：旧版 Hub 没有就是 0（表单不显示），有就按服务端的；不合理的值当没有', () => {
    assert.equal(launcherLimits(undefined).runMaxMinutes, 0)
    assert.equal(launcherLimits({ launcher_run_prompt_max: 600 }).runMaxMinutes, 0)
    assert.equal(launcherLimits({ launcher_run_max_minutes: 10080 }).runMaxMinutes, 10080)
    assert.equal(launcherLimits({ launcher_run_max_minutes: -5 }).runMaxMinutes, 0)
    assert.equal(launcherLimits({ launcher_run_max_minutes: Number.NaN }).runMaxMinutes, 0)
  })
  it('字数按字符算，与服务端按 rune 一致：中文与 emoji 各算一个', () => {
    assert.equal(charCount('终端启动器'), 5)
    assert.equal(charCount('a😀b'), 3)
    assert.equal(charCount(''), 0)
  })
})

describe('草稿', () => {
  it('新编号符合服务端的格式，且每次都不同', () => {
    const ids = new Set(Array.from({ length: 50 }, newLauncherId))
    assert.equal(ids.size, 50)
    for (const id of ids) assert.match(id, /^[A-Za-z0-9_-]{1,40}$/)
  })
  it('空白草稿与 Cursor CLI 模板', () => {
    const b = blankDraft(HOST)
    assert.deepEqual({ ...b, id: '' }, { id: '', name: '', host: HOST, dir: '', command: '' })
    const c = cursorCliDraft(HOST)
    assert.equal(c.name, 'Cursor CLI')
    assert.equal(c.command, CURSOR_CLI_COMMAND)
    assert.equal(c.host, HOST)
    assert.notEqual(c.id, b.id)
  })
  it('预设与草稿互转，缺省字段变成空串；提交时规整空白', () => {
    assert.deepEqual(draftOf({ id: 'a', name: 'x', host: HOST }), { id: 'a', name: 'x', host: HOST, dir: '', command: '' })
    assert.deepEqual(toLauncher({ id: 'a', name: '  Cursor \t CLI\n', host: ` ${HOST} `, dir: ' /srv ', command: '  FOO="a  b" agent  ' }), {
      id: 'a',
      name: 'Cursor CLI',
      host: HOST,
      dir: '/srv',
      command: 'FOO="a  b" agent',
    })
    assert.equal(normalizeName('  a   b\tc  '), 'a b c')
  })
})

describe('草稿校验', () => {
  const draft = (over: Partial<TerminalLauncher> = {}) => draftOf(ok(over))
  const check = (d = draft(), others: TerminalLauncher[] = []) => validateDraft(d, { limits, others })

  it('合法的草稿没有错误；目录与命令可以留空', () => {
    assert.deepEqual(check(), {})
    assert.deepEqual(check(draft({ dir: '', command: '' })), {})
    assert.ok(!hasErrors(check()))
  })

  it('名字：必填、不超长、不重名（不分大小写，且不算它自己）', () => {
    assert.equal(check(draft({ name: '   ' })).name, '请给预设起个名字')
    assert.match(check(draft({ name: '字'.repeat(41) })).name ?? '', /名字最长 40 个字，现在是 41 个字/)
    assert.equal(check(draft({ name: '字'.repeat(40) })).name, undefined)
    assert.match(check(draft({ name: 'a\x1bb' })).name ?? '', /控制字符/)
    const other = ok({ id: 'l-2', name: 'cursor cli' })
    assert.match(check(draft(), [other]).name ?? '', /已经有一个叫「cursor cli」的预设/)
    assert.equal(check(draft({ id: 'l-2' }), [other]).name, undefined, '改的就是这一条自己，不算重名')
    // 空白折叠之后才比较：「Cursor   CLI」与「Cursor CLI」是同一个名字。
    assert.match(check(draft({ name: 'CURSOR   CLI' }), [ok({ id: 'l-2' })]).name ?? '', /已经有一个叫/)
  })

  it('主机必填', () => {
    assert.equal(check(draft({ host: '' })).host, '请选择一台主机')
  })

  it('目录：必须是完整路径；换行 / 超长被拒绝', () => {
    for (const dir of ['/', '/srv/应用', '~', '~/work', 'C:\\work', 'D:/work', '\\\\nas\\share']) {
      assert.equal(check(draft({ dir })).dir, undefined, `${dir} 应合法`)
    }
    for (const dir of ['projects/app', './app', '../app', 'app']) {
      assert.match(check(draft({ dir })).dir ?? '', /完整路径/, `${dir} 应被拒绝`)
    }
    assert.match(check(draft({ dir: '/a\nb' })).dir ?? '', /换行或控制字符/)
    assert.match(check(draft({ dir: '/' + 'd'.repeat(512) })).dir ?? '', /目录最长 512/)
  })

  it('命令：只能是一行；制表符与控制字符也不行；超长被拒绝；中间的空白原样保留', () => {
    for (const command of ['a\nb', 'a\rb', 'a\tb', 'a\x1bb', 'a\x7fb']) {
      assert.match(check(draft({ command })).command ?? '', /只能是一行/, JSON.stringify(command))
    }
    assert.match(check(draft({ command: 'x'.repeat(1001) })).command ?? '', /命令最长 1000/)
    assert.equal(check(draft({ command: 'x'.repeat(1000) })).command, undefined)
    assert.equal(check(draft({ command: 'FOO="a  b"   agent' })).command, undefined)
  })

  it('用服务端给的范围校验', () => {
    const tight = { ...LAUNCHER_DEFAULT_LIMITS, max: 3, name: 5, dir: 10, command: 8 }
    const e = validateDraft(draftOf(ok({ name: '123456', dir: '/12345678901', command: '123456789' })), { limits: tight, others: [] })
    assert.deepEqual(Object.keys(e).sort(), ['command', 'dir', 'name'])
  })
})

describe('非交互续聊预设的草稿与校验（#126）', () => {
  const runOk = (over: Partial<NonNullable<TerminalLauncher['run']>> = {}): TerminalLauncher => ({
    id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1', ...over },
  })
  const draft = (over: Partial<TerminalLauncher> = {}, run: Partial<NonNullable<TerminalLauncher['run']>> = {}) => draftOf({ ...runOk(run), ...over })
  const check = (d = draft(), others: TerminalLauncher[] = []) => validateDraft(d, { limits, others })

  it('模板与互转：终端预设没有 run 字段，续聊预设转回去不带命令与守护', () => {
    const h = headlessDraft(HOST)
    assert.equal(h.name, CURSOR_CLI_RESUME_NAME)
    assert.equal(h.host, HOST)
    assert.deepEqual(h.run, blankRunDraft())
    assert.equal('run' in blankDraft(HOST), false, '终端草稿不带 run')
    assert.equal('run' in draftOf(ok()), false)
    assert.equal(isHeadlessPreset(ok()), false)
    assert.equal(isHeadlessPreset(runOk()), true)

    const d = draftOf(runOk({ bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', posture: 'force', conversation_id: 'chat-9', prompt: '继续' }))
    assert.deepEqual(d.run, { bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', posture: 'force', workflow: 'wf-1', conversation: 'chat-9', prompt: '继续', maxRun: '', maxRunUnit: 'hour', autoResume: false, autoResumeOk: '', newConversation: false, origin: '' })
    assert.equal(d.command, '')
    assert.deepEqual(toLauncher(d), {
      id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless',
      run: { posture: 'force', bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', workflow_id: 'wf-1', conversation_id: 'chat-9', prompt: '继续' },
    })
  })

  it('提交时规整：空字段不带，提示词里的空白（含换行）折成一个空格', () => {
    const t = toLauncher({ ...blankDraft(HOST), id: 'r-2', name: '  续聊 ', dir: ' /w ', run: { ...blankRunDraft(), workflow: ' wf ', model: '  ', prompt: '  继续\n  做完   %workflow  ' } })
    assert.deepEqual(t, { id: 'r-2', name: '续聊', host: HOST, dir: '/w', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf', prompt: '继续 做完 %workflow' } })
    assert.equal(collapseSpaces('  a \t b\n\nc '), 'a b c')
    assert.equal(draftOf({ ...runOk(), run: undefined, kind: 'headless' }).run?.posture, 'allowlist', '缺运行配置时给空的草稿而不是崩')
  })

  it('合法的续聊草稿没有错误', () => {
    assert.deepEqual(check(), {})
    assert.deepEqual(check(draft({}, { workflow_id: '', conversation_id: 'e33e27ef-0fbb-46e4-9446-5dee56ae61fa' })), {})
    assert.deepEqual(check(draft({}, { bin: '~/.local/bin/agent', model: 'vendor/model:tag-1.5', posture: 'force', prompt: '继续 %workflow' })), {})
  })

  it('目录必填且必须是 / 开头的绝对路径（与终端预设不同：~ 与盘符都不行）', () => {
    assert.match(check(draft({ dir: '' })).dir ?? '', /请填写目录/)
    for (const dir of ['~/work', 'C:\\work', 'app', './app']) {
      assert.ok(check(draft({ dir })).dir, `${dir} 应被拒绝`)
    }
    assert.match(check(draft({ dir: '~/work' })).dir ?? '', /绝对路径/)
    assert.equal(check(draft({ dir: '/srv/应用' })).dir, undefined)
  })

  it('工作流编号与对话编号至少填一个；各自的格式', () => {
    assert.match(check(draft({}, { workflow_id: '', conversation_id: '' })).workflow ?? '', /至少|请填工作流编号/)
    assert.match(check(draft({}, { workflow_id: 'a b' })).workflow ?? '', /不能有空格/)
    assert.match(check(draft({}, { workflow_id: 'w'.repeat(129) })).workflow ?? '', /最长 128/)
    assert.equal(check(draft({}, { workflow_id: 'w'.repeat(128) })).workflow, undefined)
    assert.match(check(draft({}, { conversation_id: 'a b' })).conversation ?? '', /字母、数字、下划线和连字符/)
    assert.match(check(draft({}, { conversation_id: 'x'.repeat(65) })).conversation ?? '', /最长 64/)
    assert.equal(check(draft({}, { conversation_id: 'x'.repeat(64) })).conversation, undefined)
  })

  it('可执行文件：命令名或路径；前缀命令与 shell 不行；超长不行', () => {
    for (const bin of ['agent', 'cursor-agent', '/usr/local/bin/agent', '~/bin/agent', 'agent+v2']) {
      assert.equal(check(draft({}, { bin })).bin, undefined, `${bin} 应合法`)
    }
    for (const bin of ['agent --force', 'a;b', '$(x)', 'a b', "a'b", 'sudo', 'bash', 'env', 'exec', 'cd', '.']) {
      assert.ok(check(draft({}, { bin })).bin, `${bin} 应被拒绝`)
    }
    assert.match(check(draft({}, { bin: 'a'.repeat(129) })).bin ?? '', /最长 128/)
  })

  it('模型名：字母数字开头，可含 . _ : / + -', () => {
    for (const model of ['claude-sonnet-5-5-max', 'gpt-5.2', 'vendor/model:tag', 'm1']) {
      assert.equal(check(draft({}, { model })).model, undefined, `${model} 应合法`)
    }
    for (const model of ['-x', '.x', 'a b', "a'b", 'a;b', '$(x)']) {
      assert.ok(check(draft({}, { model })).model, `${model} 应被拒绝`)
    }
    assert.match(check(draft({}, { model: 'm'.repeat(65) })).model ?? '', /最长 64/)
  })

  it('提示词：按折叠空白之后的字数算上限，控制字符不行', () => {
    assert.equal(check(draft({}, { prompt: '字'.repeat(600) })).prompt, undefined)
    assert.match(check(draft({}, { prompt: '字'.repeat(601) })).prompt ?? '', /最长 600 个字，现在是 601 个字/)
    assert.equal(check(draft({}, { prompt: '字 '.repeat(300) })).prompt, undefined, '折叠后 599 个字，没超')
    assert.match(check(draft({}, { prompt: 'a\x1bb' })).prompt ?? '', /控制字符/)
  })

  it('续聊预设不校验命令；名字与主机仍然校验', () => {
    const d = draft()
    d.command = 'x'.repeat(5000)
    assert.deepEqual(check(d), {}, '命令不属于续聊预设')
    assert.equal(check(draft({ name: ' ' })).name, '请给预设起个名字')
    assert.equal(check(draft({ host: '' })).host, '请选择一台主机')
    assert.match(check(draft(), [{ ...runOk(), id: 'r-9' }]).name ?? '', /已经有一个叫/)
  })

  it('用服务端给的范围校验', () => {
    const tight = { ...LAUNCHER_DEFAULT_LIMITS, runPrompt: 5, runModel: 4, runBin: 3 }
    const e = validateDraft(draft({}, { prompt: '123456', model: 'abcde', bin: 'abcd' }), { limits: tight, others: [] })
    assert.deepEqual(Object.keys(e).sort(), ['bin', 'model', 'prompt'])
  })
})

describe('预设的最长运行时间（#141）', () => {
  const capLimits = { ...LAUNCHER_DEFAULT_LIMITS, runSupported: true, runMaxMinutes: 7 * 24 * 60 }
  const withRun = (maxRun: string, maxRunUnit: 'minute' | 'hour' | 'day' = 'hour') => {
    const d = headlessDraft(HOST)
    d.name = '续聊'
    d.dir = '/srv/app'
    d.run = { ...blankRunDraft(), workflow: 'wf-1', maxRun, maxRunUnit }
    return d
  }
  const check = (d: ReturnType<typeof withRun>, lim = capLimits) => validateDraft(d, { limits: lim, others: [] })

  it('数字加单位换算成整数分钟；留空是不设；写得不对是 NaN', () => {
    assert.equal(parseMaxRun('30', 'minute'), 30)
    assert.equal(parseMaxRun('2', 'hour'), 120)
    assert.equal(parseMaxRun('7', 'day'), 10080)
    assert.equal(parseMaxRun('1.5', 'hour'), 90, '1.5 小时正好是整数分钟')
    assert.equal(parseMaxRun(' 45 ', 'minute'), 45, '首尾空白不算')
    assert.equal(parseMaxRun('0.1', 'hour'), 6)
    assert.equal(parseMaxRun('', 'hour'), null)
    assert.equal(parseMaxRun('   ', 'day'), null)
    assert.equal(parseMaxRun(undefined, undefined), null, '会话页「运行」没有这个字段')
    for (const bad of ['abc', '-5', '1e3', '0x10', '1,5', '30分钟', '.5', '5.']) assert.ok(Number.isNaN(parseMaxRun(bad, 'minute')), `${bad} 应是 NaN`)
    assert.ok(Number.isNaN(parseMaxRun('0.5', 'minute')), '半分钟不是整数分钟')
    assert.ok(Number.isNaN(parseMaxRun('1.0001', 'hour')), '精度超过分钟的不接受')
    assert.equal(parseMaxRun('0', 'minute'), 0, '0 交给校验拒绝')
  })

  it('分钟与输入框互逆：整天用天、整小时用小时，其余用分钟', () => {
    assert.deepEqual(maxRunFields(undefined), { maxRun: '', maxRunUnit: 'hour' })
    assert.deepEqual(maxRunFields(0), { maxRun: '', maxRunUnit: 'hour' })
    assert.deepEqual(maxRunFields(30), { maxRun: '30', maxRunUnit: 'minute' })
    assert.deepEqual(maxRunFields(90), { maxRun: '90', maxRunUnit: 'minute' })
    assert.deepEqual(maxRunFields(120), { maxRun: '2', maxRunUnit: 'hour' })
    assert.deepEqual(maxRunFields(1440), { maxRun: '1', maxRunUnit: 'day' })
    assert.deepEqual(maxRunFields(10080), { maxRun: '7', maxRunUnit: 'day' })
    for (const m of [1, 30, 59, 60, 61, 90, 120, 1439, 1440, 1441, 10080]) {
      const f = maxRunFields(m)
      assert.equal(parseMaxRun(f.maxRun, f.maxRunUnit), m, `${m} 分钟往返`)
    }
  })

  it('旧预设（没有这个字段）转草稿是空；设了的转成草稿再提交，原样回到服务端', () => {
    const old: TerminalLauncher = { id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1' } }
    assert.equal(draftOf(old).run?.maxRun, '')
    assert.equal('max_run_minutes' in (toLauncher(draftOf(old)).run ?? {}), false, '没设就不带')
    for (const m of [1, 30, 90, 120, 1440, 10080]) {
      const t = toLauncher(draftOf({ ...old, run: { ...old.run, max_run_minutes: m } }))
      assert.equal(t.run?.max_run_minutes, m, `${m} 分钟往返`)
    }
  })

  it('提交：填了才带，写错的不带（由校验拦下）', () => {
    assert.equal(toLauncher(withRun('30', 'minute')).run?.max_run_minutes, 30)
    assert.equal(toLauncher(withRun('2', 'hour')).run?.max_run_minutes, 120)
    assert.equal(toLauncher(withRun('3', 'day')).run?.max_run_minutes, 4320)
    for (const v of ['', ' ', 'abc', '0', '-1']) assert.equal('max_run_minutes' in (toLauncher(withRun(v)).run ?? {}), false, `「${v}」不带`)
  })

  it('校验：留空合法；1 分钟到上限之间合法；越界与写错给出带范围的提示', () => {
    assert.equal(check(withRun('')).maxRun, undefined)
    assert.equal(check(withRun('1', 'minute')).maxRun, undefined)
    assert.equal(check(withRun('7', 'day')).maxRun, undefined, '正好上限')
    assert.equal(check(withRun('10080', 'minute')).maxRun, undefined)
    assert.equal(check(withRun('1.5', 'hour')).maxRun, undefined)
    assert.deepEqual(check(withRun('30', 'minute')), {})

    assert.match(check(withRun('0', 'minute')).maxRun ?? '', /1 分钟到 7 天/)
    assert.match(check(withRun('8', 'day')).maxRun ?? '', /1 分钟到 7 天之间（留空 = 7 天）/)
    assert.match(check(withRun('10081', 'minute')).maxRun ?? '', /1 分钟到 7 天/)
    assert.match(check(withRun('abc')).maxRun ?? '', /请填一个正数/)
    assert.match(check(withRun('0.5', 'minute')).maxRun ?? '', /精确到分钟/)
    assert.match(check(withRun('-3')).maxRun ?? '', /请填一个正数/)
  })

  it('旧版 Hub 不认识这个选项（上限为 0）：不检查，也不会因它阻止保存', () => {
    assert.equal(check(withRun('abc'), { ...capLimits, runMaxMinutes: 0 }).maxRun, undefined)
  })

  it('会话页「运行」没有这个字段：照常校验、不受影响', () => {
    const e = validateDraft({ ...withRun(''), run: { ...blankRunDraft(), workflow: 'wf-1', maxRun: undefined, maxRunUnit: undefined } }, { limits: capLimits, others: [] })
    assert.deepEqual(e, {})
  })
})

describe('可选的主机与主机状态', () => {
  it('只列已启用的 Agent 主机，在线的排前面、同组按名字排序', () => {
    const a = host({ machine_code: 'A', hostname: 'zeta', online: true })
    const b = host({ machine_code: 'B', hostname: 'alpha', online: false })
    const c = host({ machine_code: 'C', hostname: 'beta', online: true })
    const ssh = host({ machine_code: 'S', hostname: 'ssh-box', kind: 'ssh' } as Partial<HostView>)
    const off = host({ machine_code: 'D', hostname: 'disabled-box', status: 'disabled' })
    assert.deepEqual(launcherHosts([a, b, c, ssh, off]).map(h => h.machine_code), ['C', 'A', 'B'])
  })

  it('主机状态给出可以直接展示的原因', () => {
    assert.deepEqual(hostReadiness(host()), { ok: true })
    const gone = hostReadiness(undefined)
    assert.ok(!gone.ok && gone.reason === 'missing' && /不在列表里/.test(gone.text))
    const ssh = hostReadiness(host({ kind: 'ssh' } as Partial<HostView>))
    assert.ok(!ssh.ok && ssh.reason === 'unsupported')
    const disabled = hostReadiness(host({ status: 'disabled' }))
    assert.ok(!disabled.ok && disabled.reason === 'disabled')
    const offline = hostReadiness(host({ online: false }))
    assert.ok(!offline.ok && offline.reason === 'offline' && offline.text === '主机离线')
  })
})

describe('认出已经在运行的会话', () => {
  const h = host({
    sessions: [
      sess('s-old', 'Cursor CLI', { created: 100 }),
      sess('s-new', 'Cursor CLI', { created: 300 }),
      sess('s-mid', 'Cursor   CLI', { created: 200 }),
      sess('s-exited', 'Cursor CLI', { created: 400, state: 'exited' }),
      sess('s-other', '别的会话', { created: 500 }),
      sess('s-noname', '', { created: 600 }),
    ],
  })

  it('同名且在运行的会话，新的在前；已结束的和别的名字不算', () => {
    assert.deepEqual(runningSessionsOf(ok(), h).map(s => s.sid), ['s-new', 's-mid', 's-old'])
  })
  it('名字里空白不同也认得出（终端子系统会折叠会话名里的空白）', () => {
    assert.deepEqual(runningSessionsOf(ok({ name: 'Cursor   CLI' }), h).map(s => s.sid), ['s-new', 's-mid', 's-old'])
  })
  it('主机不在、或没有同名会话时是空的', () => {
    assert.deepEqual(runningSessionsOf(ok(), undefined), [])
    assert.deepEqual(runningSessionsOf(ok({ name: '没有这个' }), h), [])
  })
  it('没有创建时间的会话排在最后，不会让排序出错', () => {
    const h2 = host({ sessions: [sess('s-a', 'x'), sess('s-b', 'x', { created: 5 })] })
    assert.deepEqual(runningSessionsOf(ok({ name: 'x' }), h2).map(s => s.sid), ['s-b', 's-a'])
  })
})

describe('自动输入的那一行', () => {
  it('shell 字面量：单引号包起来，里面的单引号转义', () => {
    assert.equal(shQuote('/srv/app'), `'/srv/app'`)
    assert.equal(shQuote(`/srv/a'b`), `'/srv/a'\\''b'`)
    assert.equal(shQuote('a b'), `'a b'`)
    assert.equal(shQuote(''), `''`)
  })

  it('cd 的目录写法：~ 让 shell 自己展开，其余部分加引号', () => {
    assert.equal(quoteDir('/home/user/RelayMesh'), `'/home/user/RelayMesh'`)
    assert.equal(quoteDir('/'), `'/'`)
    assert.equal(quoteDir('~'), '~')
    assert.equal(quoteDir('~/'), '~/')
    assert.equal(quoteDir('~/work'), `~/'work'`)
    assert.equal(quoteDir('~/my work'), `~/'my work'`)
    assert.equal(quoteDir('~bob/x'), `~bob/'x'`)
    assert.equal(quoteDir(`~/o'neil`), `~/'o'\\''neil'`)
    assert.equal(quoteDir('/srv/$HOME `x`'), `'/srv/$HOME \`x\`'`, '特殊字符不被 shell 解释')
  })

  it('没有命令就什么也不输入（只开终端）', () => {
    assert.equal(startLine({ dir: '/srv', command: '' }, 'linux'), '')
    assert.equal(startLine({ dir: '/srv', command: '   ' }, 'linux'), '')
    assert.equal(startLine({}, 'linux'), '')
  })

  it('有目录就先 cd，目录不对时命令不会运行', () => {
    assert.equal(startLine({ dir: '/home/user/RelayMesh', command: 'agent' }, 'linux'), `cd -- '/home/user/RelayMesh' && agent`)
    assert.equal(startLine({ dir: '~/work', command: 'agent --resume' }, 'darwin'), `cd -- ~/'work' && agent --resume`)
    assert.equal(startLine({ dir: '/srv', command: '  agent  ' }, undefined), `cd -- '/srv' && agent`)
  })

  it('没有目录就只输入命令；Windows 主机但不知道 shell 种类（旧版守护进程）时不拼目录', () => {
    assert.equal(startLine({ dir: '', command: 'agent' }, 'linux'), 'agent')
    assert.equal(startLine({ dir: 'C:\\work', command: 'agent' }, 'windows'), 'agent')
    assert.equal(startLine({ dir: 'C:\\work', command: 'agent' }, 'Windows 11'), 'agent')
  })

  it('命令原样输入，不被改写', () => {
    assert.equal(startLine({ dir: '', command: `FOO='a  b' agent --x "y"` }, 'linux'), `FOO='a  b' agent --x "y"`)
  })

  it('开启 exit_ends_session 时用 exec 包装', () => {
    assert.equal(
      startLine({ dir: '/srv', command: 'agent --trust', guard: { enabled: true, exit_ends_session: true } }, 'linux'),
      `cd -- '/srv' && exec agent --trust`,
    )
    assert.equal(
      startLine({ dir: '', command: 'A=1 agent', guard: { enabled: true, exit_ends_session: true } }, 'linux'),
      'exec env A=1 agent',
    )
  })

  it('传给新建会话的目录：~ 开头的主机不展开，不传', () => {
    assert.equal(sessionCwd('/srv/app'), '/srv/app')
    assert.equal(sessionCwd(' /srv/app '), '/srv/app')
    assert.equal(sessionCwd('~/work'), undefined)
    assert.equal(sessionCwd('~'), undefined)
    assert.equal(sessionCwd(''), undefined)
    assert.equal(sessionCwd(undefined), undefined)
    assert.equal(sessionCwd('C:\\work'), 'C:\\work')
  })
})

// ---------- Windows / macOS 主机（#163） ----------
// 没有真实的 Windows / macOS 主机可用：这里按守护进程会上报的样子构造平台信息；PowerShell 7 上的真实行为另有实测（见 D-298）。
const WIN_PS = { os: 'windows', shell: 'pwsh', home: 'C:\\Users\\me', sep: '\\' }
const WIN_PS5 = { os: 'windows', shell: 'powershell', home: 'C:\\Users\\me', sep: '\\' }
const WIN_CMD = { os: 'windows', shell: 'cmd', home: 'C:\\Users\\me', sep: '\\' }
const WIN_OLD = { os: 'windows' } // 旧版守护进程：不上报 shell / 家目录
const MAC = { os: 'macOS 14.5', shell: 'zsh', home: '/Users/me', sep: '/' }
const LINUX = { os: 'Debian GNU/Linux 13 (trixie)', shell: 'bash', home: '/root', sep: '/' }
const EXIT_ENDS = { enabled: true, exit_ends_session: true }

describe('主机平台（#163）', () => {
  it('从主机信息取平台：系统名来自 Agent，shell / 家目录 / 分隔符来自守护进程', () => {
    const h = host({ os: 'windows', holder: { exec: { shell: 'pwsh', home: 'C:\\Users\\me', sep: '\\' } } as unknown as HostView['holder'] })
    assert.deepEqual(hostPlatformOf(h), WIN_PS)
    assert.deepEqual(hostPlatformOf(host({ os: 'windows' })), { os: 'windows', shell: undefined, home: undefined, sep: undefined })
    assert.deepEqual(hostPlatformOf(undefined), { os: undefined, shell: undefined, home: undefined, sep: undefined })
  })

  it('认出 Windows / macOS', () => {
    assert.equal(isWindowsHost('windows'), true)
    assert.equal(isWindowsHost('Windows 11'), true)
    assert.equal(isWindowsHost('macOS 14.5'), false)
    assert.equal(isWindowsHost(undefined), false)
    assert.equal(isMacHost('macOS 14.5'), true)
    assert.equal(isMacHost('macOS'), true)
    assert.equal(isMacHost('Darwin'), true)
    assert.equal(isMacHost('windows'), false)
    assert.equal(isMacHost('Debian GNU/Linux 13 (trixie)'), false)
  })

  it('shell 的语法族：非 Windows 一律 posix；Windows 看上报的 shell', () => {
    for (const p of [undefined, 'linux', LINUX, MAC, 'macOS 14.5', {}]) assert.equal(shellKindOf(p), 'posix')
    for (const sh of ['pwsh', 'powershell', 'PowerShell.exe', 'pwsh.exe', ' pwsh ']) assert.equal(shellKindOf({ os: 'windows', shell: sh }), 'powershell', sh)
    for (const sh of ['cmd', 'cmd.exe', 'CMD.EXE']) assert.equal(shellKindOf({ os: 'windows', shell: sh }), 'cmd', sh)
    assert.equal(shellKindOf(WIN_OLD), 'windows', '旧版守护进程没上报 shell')
    assert.equal(shellKindOf('windows'), 'windows', '老写法：只传系统名')
    assert.equal(shellKindOf({ os: 'windows', shell: 'bash' }), 'windows', '不认识的 shell 不猜')
  })

  it('「命令退出即结束会话」只有 POSIX shell 能做（靠 exec）', () => {
    assert.equal(supportsExitEndsSession(LINUX), true)
    assert.equal(supportsExitEndsSession(MAC), true)
    assert.equal(supportsExitEndsSession(undefined), true)
    for (const p of [WIN_PS, WIN_PS5, WIN_CMD, WIN_OLD]) assert.equal(supportsExitEndsSession(p), false)
  })
})

describe('PowerShell 的字面量与 Windows 目录（#163）', () => {
  it('psQuote：单引号翻倍，其余字符原样', () => {
    assert.equal(psQuote('C:\\work'), `'C:\\work'`)
    assert.equal(psQuote('C:\\Program Files\\x'), `'C:\\Program Files\\x'`)
    assert.equal(psQuote(`C:\\o'neil`), `'C:\\o''neil'`)
    assert.equal(psQuote('C:\\[a] $b `c`'), `'C:\\[a] $b \`c\`'`, '方括号、$、反引号在单引号里都是普通字符')
    assert.equal(psQuote('C:\\项目 一'), `'C:\\项目 一'`)
    assert.equal(psQuote(''), `''`)
  })

  it('psQuote：PowerShell 把弯引号也当单引号，四种都要翻倍（只翻倍 ASCII 的会让字符串提前结束）', () => {
    assert.equal(psQuote('C:\\J\u2019s Files'), `'C:\\J\u2019\u2019s Files'`)
    assert.equal(psQuote('\u2018x\u2019'), `'\u2018\u2018x\u2019\u2019'`)
    assert.equal(psQuote('a\u201ab\u201bc'), `'a\u201a\u201ab\u201b\u201bc'`)
  })

  it('windowsDir：盘符与 UNC 路径原样，~ 用上报的家目录展开', () => {
    assert.equal(windowsDir('C:\\work', WIN_PS), 'C:\\work')
    assert.equal(windowsDir('d:/work/app', WIN_PS), 'd:/work/app')
    assert.equal(windowsDir('\\\\srv\\share\\x', WIN_PS), '\\\\srv\\share\\x')
    assert.equal(windowsDir('~', WIN_PS), 'C:\\Users\\me')
    assert.equal(windowsDir('~\\work', WIN_PS), 'C:\\Users\\me\\work')
    assert.equal(windowsDir('~/work/app', WIN_PS), 'C:\\Users\\me\\work\\app', '~/ 里的 / 换成 \\')
    assert.equal(windowsDir('~/', WIN_PS), 'C:\\Users\\me')
    assert.equal(windowsDir('~/x', { ...WIN_PS, home: 'C:\\Users\\me\\' }), 'C:\\Users\\me\\x', '家目录末尾有 \\ 不重复')
  })

  it('windowsDir：没有确定含义的写法返回空串（调用方就不拼进入目录的那一句）', () => {
    assert.equal(windowsDir('/srv/app', WIN_PS), '', '/ 开头在 Windows 上没有确定含义')
    assert.equal(windowsDir('~bob/x', WIN_PS), '', '~用户名')
    assert.equal(windowsDir('~', WIN_OLD), '', '没上报家目录')
    assert.equal(windowsDir('~/x', { os: 'windows', home: '  ' }), '')
    assert.equal(windowsDir('work', WIN_PS), '')
  })
})

describe('Windows 主机的启动那一句（#163）', () => {
  it('PowerShell：Set-Location -LiteralPath … -ErrorAction Stop; 命令（5.1 与 7 都没有的 && 不用）', () => {
    for (const p of [WIN_PS, WIN_PS5]) {
      assert.equal(startLine({ dir: 'C:\\work\\app', command: 'agent --resume' }, p), `Set-Location -LiteralPath 'C:\\work\\app' -ErrorAction Stop; agent --resume`)
    }
    assert.equal(startLine({ dir: 'D:/work', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'D:/work' -ErrorAction Stop; agent`)
    assert.equal(startLine({ dir: '\\\\srv\\share\\x', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath '\\\\srv\\share\\x' -ErrorAction Stop; agent`)
  })

  it('PowerShell：路径里的空格、引号、弯引号、$ 和方括号都安全', () => {
    assert.equal(startLine({ dir: 'C:\\Program Files\\My App', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\Program Files\\My App' -ErrorAction Stop; agent`)
    assert.equal(startLine({ dir: `C:\\o'neil`, command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\o''neil' -ErrorAction Stop; agent`)
    assert.equal(startLine({ dir: 'C:\\J\u2019s Files', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\J\u2019\u2019s Files' -ErrorAction Stop; agent`)
    assert.equal(startLine({ dir: 'C:\\[a] $b', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\[a] $b' -ErrorAction Stop; agent`)
  })

  it('PowerShell：~ 先展开成家目录', () => {
    assert.equal(startLine({ dir: '~\\work', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\Users\\me\\work' -ErrorAction Stop; agent`)
    assert.equal(startLine({ dir: '~', command: 'agent' }, WIN_PS), `Set-Location -LiteralPath 'C:\\Users\\me' -ErrorAction Stop; agent`)
  })

  it('PowerShell：目录写法在 Windows 上没有含义（/ 开头、~用户名、没上报家目录）就不拼进入目录，命令照原样输入', () => {
    assert.equal(startLine({ dir: '/srv/app', command: 'agent' }, WIN_PS), 'agent')
    assert.equal(startLine({ dir: '~bob/x', command: 'agent' }, WIN_PS), 'agent')
    assert.equal(startLine({ dir: '~/x', command: 'agent' }, { os: 'windows', shell: 'pwsh' }), 'agent')
  })

  it('PowerShell：没有目录只输入命令；没有命令什么也不输入', () => {
    assert.equal(startLine({ dir: '', command: 'agent' }, WIN_PS), 'agent')
    assert.equal(startLine({ dir: 'C:\\work', command: '  ' }, WIN_PS), '')
  })

  it('cmd：cd /d "目录" && 命令', () => {
    assert.equal(startLine({ dir: 'C:\\work\\app', command: 'agent' }, WIN_CMD), `cd /d "C:\\work\\app" && agent`)
    assert.equal(startLine({ dir: 'C:\\Program Files\\x', command: 'agent' }, WIN_CMD), `cd /d "C:\\Program Files\\x" && agent`)
    assert.equal(startLine({ dir: '~\\work', command: 'agent' }, WIN_CMD), `cd /d "C:\\Users\\me\\work" && agent`)
  })

  it('cmd：目录含 % " ^ ! 或是 UNC 路径时不拼（cmd 会改写它们 / cd 不支持 UNC）', () => {
    for (const d of ['C:\\100%\\x', 'C:\\a"b', 'C:\\a^b', 'C:\\hi!', '\\\\srv\\share']) {
      assert.equal(startLine({ dir: d, command: 'agent' }, WIN_CMD), 'agent', d)
    }
  })

  it('Windows 但不知道 shell（旧版守护进程）：什么都不拼，和以前一样', () => {
    assert.equal(startLine({ dir: 'C:\\work', command: 'agent' }, WIN_OLD), 'agent')
    assert.equal(startLine({ dir: 'C:\\work', command: 'agent' }, 'windows'), 'agent')
    assert.equal(startLine({ dir: '~/x', command: 'agent' }, WIN_OLD), 'agent')
  })

  it('Windows 主机上 exit_ends_session 不包 exec（PowerShell / cmd 里没有 exec，包了命令根本不会运行）', () => {
    for (const p of [WIN_PS, WIN_PS5, WIN_CMD, WIN_OLD]) {
      assert.ok(!/\bexec\b/.test(startLine({ dir: '', command: 'agent --trust', guard: EXIT_ENDS }, p)), JSON.stringify(p))
    }
    assert.equal(startLine({ dir: 'C:\\work', command: 'agent --trust', guard: EXIT_ENDS }, WIN_PS), `Set-Location -LiteralPath 'C:\\work' -ErrorAction Stop; agent --trust`)
  })
})

describe('macOS / Linux 主机的启动那一句（#163）', () => {
  it('macOS：与 Linux 相同的 cd -- … && 写法，~ 交给 zsh / bash 展开', () => {
    assert.equal(startLine({ dir: '/Users/me/projects/RelayMesh', command: 'agent' }, MAC), `cd -- '/Users/me/projects/RelayMesh' && agent`)
    assert.equal(startLine({ dir: '~/projects', command: 'agent' }, MAC), `cd -- ~/'projects' && agent`)
    assert.equal(startLine({ dir: '~', command: 'agent' }, MAC), `cd -- ~ && agent`)
    assert.equal(startLine({ dir: '/Users/me/My Projects/J\u2019s', command: 'agent' }, MAC), `cd -- '/Users/me/My Projects/J\u2019s' && agent`)
    assert.equal(startLine({ dir: `/Users/me/o'neil`, command: 'agent' }, MAC), `cd -- '/Users/me/o'\\''neil' && agent`)
  })

  it('macOS / Linux：exit_ends_session 照常用 exec 包装', () => {
    for (const p of [MAC, LINUX, 'macOS 14.5', 'linux', undefined]) {
      assert.equal(startLine({ dir: '/srv', command: 'agent --trust', guard: EXIT_ENDS }, p), `cd -- '/srv' && exec agent --trust`, JSON.stringify(p))
    }
  })

  it('只传系统名的老写法与传平台对象结果一致', () => {
    assert.equal(startLine({ dir: '/srv', command: 'agent' }, 'linux'), startLine({ dir: '/srv', command: 'agent' }, LINUX))
    assert.equal(startLine({ dir: '/srv', command: 'agent' }, 'macOS 14.5'), startLine({ dir: '/srv', command: 'agent' }, MAC))
  })
})

describe('传给新建会话的目录（#163）', () => {
  it('POSIX 主机：~ 开头不传（由 cd 让 shell 展开），其余原样', () => {
    for (const p of [undefined, 'linux', LINUX, MAC]) {
      assert.equal(sessionCwd('~/work', p), undefined)
      assert.equal(sessionCwd('~', p), undefined)
      assert.equal(sessionCwd('/srv/app', p), '/srv/app')
    }
  })

  it('Windows 主机：~ 用上报的家目录换成绝对路径；没上报或写法没含义就不传', () => {
    assert.equal(sessionCwd('~', WIN_PS), 'C:\\Users\\me')
    assert.equal(sessionCwd('~\\work', WIN_PS), 'C:\\Users\\me\\work')
    assert.equal(sessionCwd('~/work', WIN_CMD), 'C:\\Users\\me\\work')
    assert.equal(sessionCwd('~/work', WIN_OLD), undefined, '旧版守护进程没上报家目录')
    assert.equal(sessionCwd('~bob/x', WIN_PS), undefined)
  })

  it('Windows 主机：盘符路径原样传', () => {
    assert.equal(sessionCwd('C:\\work', WIN_PS), 'C:\\work')
    assert.equal(sessionCwd(' C:\\work ', 'windows'), 'C:\\work')
    assert.equal(sessionCwd('', WIN_PS), undefined)
  })
})

describe('目录写法的提醒（#163）', () => {
  it('不知道主机系统（没选主机、旧数据）或目录为空：不说话', () => {
    assert.equal(dirAdvice('C:\\work', undefined), '')
    assert.equal(dirAdvice('C:\\work', {}), '')
    assert.equal(dirAdvice('', WIN_PS), '')
    assert.equal(dirAdvice('   ', LINUX), '')
  })

  it('非 Windows 主机上写了盘符 / UNC 路径：提醒要以 / 或 ~ 开头', () => {
    assert.match(dirAdvice('C:\\work', LINUX), /不是 Windows/)
    assert.match(dirAdvice('D:/work', MAC), /不是 Windows/)
    assert.match(dirAdvice('\\\\srv\\share', 'macOS 14.5'), /不是 Windows/)
    assert.equal(dirAdvice('/srv/app', LINUX), '')
    assert.equal(dirAdvice('~/work', MAC), '')
  })

  it('Windows 主机上写了 / 开头的路径：提醒写盘符路径', () => {
    for (const p of [WIN_PS, WIN_CMD, WIN_OLD]) assert.match(dirAdvice('/srv/app', p), /盘符/)
  })

  it('Windows 主机上的 ~：能展开不提醒；没上报家目录、或是 ~用户名 时说明原因', () => {
    assert.equal(dirAdvice('~', WIN_PS), '')
    assert.equal(dirAdvice('~\\work', WIN_PS), '')
    assert.match(dirAdvice('~/work', WIN_OLD), /家目录/)
    assert.match(dirAdvice('~bob/x', WIN_PS), /~用户名/)
  })

  it('Windows 主机 + 盘符路径：知道 shell 不提醒；旧版守护进程说明「不存在时悄悄回落」', () => {
    assert.equal(dirAdvice('C:\\work', WIN_PS), '')
    assert.equal(dirAdvice('C:\\work', WIN_CMD), '')
    assert.match(dirAdvice('C:\\work', WIN_OLD), /较旧/)
    assert.match(dirAdvice('C:\\work', 'windows'), /悄悄回落/)
  })

  it('目录输入框的示例跟着主机系统走', () => {
    assert.equal(dirPlaceholder(WIN_PS), 'C:\\work\\RelayMesh')
    assert.equal(dirPlaceholder('windows'), 'C:\\work\\RelayMesh')
    assert.equal(dirPlaceholder(MAC), '/Users/你/projects/RelayMesh')
    assert.equal(dirPlaceholder(LINUX), '/home/user/RelayMesh')
    assert.equal(dirPlaceholder(undefined), '/home/user/RelayMesh')
  })
})

describe('什么时候可以输入', () => {
  it('还没连上就不输入', () => {
    assert.equal(readyToType({ liveAt: null, lastOutputAt: null, now: 99999 }), false)
    assert.equal(readyToType({ liveAt: null, lastOutputAt: 100, now: 99999 }), false)
  })
  it('连上之后输出还在继续就等，安静够久就输入', () => {
    const liveAt = 1000
    assert.equal(readyToType({ liveAt, lastOutputAt: 1100, now: 1100 + READY_QUIET_MS - 1 }), false)
    assert.equal(readyToType({ liveAt, lastOutputAt: 1100, now: 1100 + READY_QUIET_MS }), true)
    // 输出又来了一段：重新计时。
    assert.equal(readyToType({ liveAt, lastOutputAt: 1300, now: 1100 + READY_QUIET_MS }), false)
  })
  it('连上之后一直没有输出，等到上限就输入', () => {
    const liveAt = 1000
    assert.equal(readyToType({ liveAt, lastOutputAt: null, now: liveAt + READY_MAX_WAIT_MS - 1 }), false)
    assert.equal(readyToType({ liveAt, lastOutputAt: null, now: liveAt + READY_MAX_WAIT_MS }), true)
  })
  it('输出持续不断也不会无限等：到上限就输入', () => {
    const liveAt = 0
    assert.equal(readyToType({ liveAt, lastOutputAt: READY_MAX_WAIT_MS, now: READY_MAX_WAIT_MS }), true)
  })
  it('输出在「已连接」之前就收完了（回放很快）：连上时已经安静，立刻输入', () => {
    assert.equal(readyToType({ liveAt: 1000, lastOutputAt: 500, now: 1000 }), true)
  })
})

describe('预设的异常退出自动续跑开关（#151）', () => {
  const old: TerminalLauncher = { id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1' } }

  it('默认关闭：新草稿是关的，旧预设（没有这个字段）转草稿也是关的，提交时不带字段', () => {
    assert.equal(blankRunDraft().autoResume, false)
    assert.equal(headlessDraft(HOST).run?.autoResume, false)
    assert.equal(draftOf(old).run?.autoResume, false)
    assert.equal('auto_resume' in (toLauncher(draftOf(old)).run ?? {}), false, '关着就不带，服务端存下来的预设保持原样')
  })

  it('打开后提交 auto_resume: true，再转成草稿原样回来；关掉后不再带', () => {
    const on = toLauncher(draftOf({ ...old, run: { ...old.run, auto_resume: true } }))
    assert.equal(on.run?.auto_resume, true)
    assert.equal(draftOf(on).run?.autoResume, true)
    const d = draftOf(on)
    d.run = { ...blankRunDraft(), ...d.run, autoResume: false }
    assert.equal('auto_resume' in (toLauncher(d).run ?? {}), false)
  })

  it('只认明确的 true：服务端给了别的值（旧版本、脏数据）都当作关', () => {
    for (const v of [false, undefined, null, 0, 1, 'true'] as unknown[]) {
      assert.equal(draftOf({ ...old, run: { ...old.run, auto_resume: v as boolean } }).run?.autoResume, false, String(v))
    }
  })

  it('会话页「运行」没有这个字段的草稿也能提交：不带', () => {
    const d = headlessDraft(HOST)
    d.name = '续聊'
    d.run = { bin: '', model: '', posture: 'allowlist', workflow: 'wf-1', conversation: '', prompt: '' }
    assert.equal('auto_resume' in (toLauncher(d).run ?? {}), false)
  })
})

describe('预设的「第一次运行时新开对话」与来源标记（#176 第三期）', () => {
  const base: TerminalLauncher = { id: 'ea-1', name: '启用 Agent · x', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'force', workflow_id: 'wf-1' } }
  const lim = { ...LAUNCHER_DEFAULT_LIMITS, runSupported: true, runNewPromptDefault: '新开提示词' }
  const errorsOf = (run: Partial<RunDraft>) => {
    const d = draftOf(base)
    d.run = { ...blankRunDraft(), ...d.run, workflow: 'wf-1', ...run }
    return validateDraft(d, { limits: lim, others: [] })
  }

  it('默认关闭：新草稿与旧预设都是关的，提交时不带字段（不改动已存的预设）', () => {
    assert.equal(blankRunDraft().newConversation, false)
    assert.equal(blankRunDraft().origin, '')
    assert.equal(headlessDraft(HOST).run?.newConversation, false)
    assert.equal(draftOf(base).run?.newConversation, false)
    const out = toLauncher(draftOf(base)).run ?? {}
    assert.equal('new_conversation' in out, false)
    assert.equal('origin' in out, false)
  })

  it('打开后提交 new_conversation: true，再转成草稿原样回来；只认明确的 true', () => {
    const on = toLauncher(draftOf({ ...base, run: { ...base.run, new_conversation: true } }))
    assert.equal(on.run?.new_conversation, true)
    assert.equal(draftOf(on).run?.newConversation, true)
    for (const v of [false, undefined, null, 0, 1, 'true'] as unknown[]) {
      assert.equal(draftOf({ ...base, run: { ...base.run, new_conversation: v as boolean } }).run?.newConversation, false, String(v))
    }
  })

  it('来源标记不是表单的选项，但编辑保存时必须原样带回去（否则编辑一次「启用 Agent」创建的预设就丢了标记）', () => {
    const enabled: TerminalLauncher = { ...base, run: { ...base.run, new_conversation: true, origin: 'enable_agent' } }
    const d = draftOf(enabled)
    assert.equal(d.run?.origin, 'enable_agent')
    d.name = '改了个名字'
    const saved = toLauncher(d)
    assert.equal(saved.run?.origin, 'enable_agent')
    assert.equal(saved.run?.new_conversation, true)
    // 手工建的没有标记：不凭空带
    assert.equal('origin' in (toLauncher(draftOf(base)).run ?? {}), false)
  })

  it('校验：新开对话要填工作流编号、不能同时固定对话编号（与服务端一致）', () => {
    assert.equal(hasErrors(errorsOf({ newConversation: true })), false)
    assert.match(errorsOf({ newConversation: true, conversation: 'chat-1' }).conversation ?? '', /不能再固定对话编号/)
    assert.match(errorsOf({ newConversation: true, workflow: '' }).workflow ?? '', /工作流编号/)
    // 没选新开对话：固定对话编号照常合法
    assert.equal(errorsOf({ conversation: 'chat-1' }).conversation, undefined)
  })

  it('旧版 Hub 不返回新开对话的默认提示词时：limits 里是空串（表单不提供这个选项）', () => {
    assert.equal(LAUNCHER_DEFAULT_LIMITS.runNewPromptDefault, '')
    assert.equal(launcherLimits({ launcher_run_prompt_max: 600 }).runNewPromptDefault, '')
    assert.equal(launcherLimits({ launcher_run_new_prompt_default: '默认 %workflow' }).runNewPromptDefault, '默认 %workflow')
  })
})

describe('续跑成功的判定时长（#172）', () => {
  const old: TerminalLauncher = { id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1', auto_resume: true } }
  const lim = { ...LAUNCHER_DEFAULT_LIMITS, runSupported: true }
  const errorsOf = (run: Partial<RunDraft>, autoResume = true) => {
    const d = draftOf(old)
    d.run = { ...blankRunDraft(), ...d.run, workflow: 'wf-1', autoResume, ...run }
    return validateDraft(d, { limits: lim, others: [] })
  }

  it('默认 10 分钟、上限 1 天（与服务端一致）', () => {
    assert.equal(AUTO_RESUME_OK_DEFAULT_MINUTES, 10)
    assert.equal(AUTO_RESUME_OK_MAX_MINUTES, 1440)
  })

  it('输入的解析：留空 = 默认；纯数字才算；0 与超限留给校验拒绝', () => {
    assert.equal(parseAutoResumeOk(undefined), null)
    assert.equal(parseAutoResumeOk(''), null)
    assert.equal(parseAutoResumeOk('   '), null)
    assert.equal(parseAutoResumeOk('10'), 10)
    assert.equal(parseAutoResumeOk(' 45 '), 45)
    assert.equal(parseAutoResumeOk('007'), 7)
    assert.equal(parseAutoResumeOk('0'), 0)
    for (const bad of ['1.5', '-3', '10分钟', '1e3', 'abc', '0x10', '+5']) assert.ok(Number.isNaN(parseAutoResumeOk(bad)), bad)
  })

  it('留空不带字段；填了转成 auto_resume_ok_minutes，转回草稿原样回来', () => {
    assert.equal(draftOf(old).run?.autoResumeOk, '', '旧预设（没有这个字段）转草稿是空')
    assert.equal(blankRunDraft().autoResumeOk, '')
    assert.equal('auto_resume_ok_minutes' in (toLauncher(draftOf(old)).run ?? {}), false, '留空不带，服务端存下来的预设保持原样')
    const set = toLauncher(draftOf({ ...old, run: { ...old.run, auto_resume_ok_minutes: 45 } }))
    assert.equal(set.run?.auto_resume_ok_minutes, 45)
    assert.equal(draftOf(set).run?.autoResumeOk, '45')
    const d = draftOf(set)
    d.run = { ...blankRunDraft(), ...d.run, autoResumeOk: '  30 ' }
    assert.equal(toLauncher(d).run?.auto_resume_ok_minutes, 30, '首尾空白不影响')
  })

  it('关着自动续跑就不带判定时长（它只在开着时才有意义）；草稿里的值保留，再打开还在', () => {
    const d = draftOf({ ...old, run: { ...old.run, auto_resume_ok_minutes: 45 } })
    d.run = { ...blankRunDraft(), ...d.run, autoResume: false }
    const out = toLauncher(d).run ?? {}
    assert.equal('auto_resume' in out, false)
    assert.equal('auto_resume_ok_minutes' in out, false)
    assert.equal(d.run.autoResumeOk, '45')
  })

  it('服务端给了不合理的值（旧版本、脏数据）：转草稿当作没设', () => {
    for (const v of [0, -5, undefined, null, 'x'] as unknown[]) {
      assert.equal(draftOf({ ...old, run: { ...old.run, auto_resume_ok_minutes: v as number } }).run?.autoResumeOk, '', String(v))
    }
    assert.equal(draftOf({ ...old, run: { ...old.run, auto_resume_ok_minutes: 12.9 } }).run?.autoResumeOk, '12', '小数向下取整')
  })

  it('校验：留空、1 到 1440 的整数合法；0、超限、小数、非数字被拒绝并说明范围', () => {
    for (const ok of ['', '1', '10', '1440']) assert.equal(errorsOf({ autoResumeOk: ok }).autoResumeOk, undefined, ok || '留空')
    for (const bad of ['0', '1441', '99999', '1.5', '-1', 'abc', '10分钟']) {
      assert.match(errorsOf({ autoResumeOk: bad }).autoResumeOk ?? '', /1 到 1440 之间的整数分钟（留空 = 10 分钟）/, bad)
    }
  })

  it('没开自动续跑时不检查判定时长（它此时被忽略）', () => {
    assert.equal(errorsOf({ autoResumeOk: 'abc' }, false).autoResumeOk, undefined)
  })

  it('会话页「运行」的草稿没有这个字段：照常校验、不带', () => {
    const d = headlessDraft(HOST)
    d.name = '续聊'
    d.run = { bin: '', model: '', posture: 'allowlist', workflow: 'wf-1', conversation: '', prompt: '' }
    assert.equal('auto_resume_ok_minutes' in (toLauncher(d).run ?? {}), false)
    assert.equal(validateRunOptions(d.run, lim).autoResumeOk, undefined)
  })
})
