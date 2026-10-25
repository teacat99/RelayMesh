// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, TerminalLauncher } from '../../api/terminal'
import {
  AUTO_RESUME_OK_DEFAULT_MINUTES,
  AUTO_RESUME_OK_MAX_MINUTES,
  CURSOR_CLI_RESUME_NAME,
  LAUNCHER_DEFAULT_LIMITS,
  LEGACY_PRESET_BADGE,
  LEGACY_PRESET_HINT,
  blankRunDraft,
  charCount,
  collapseSpaces,
  dirAdvice,
  dirPlaceholder,
  draftOf,
  draftSource,
  effectiveAfterNew,
  effectiveSource,
  hasErrors,
  headlessDraft,
  isHeadlessPreset,
  isLegacyPreset,
  hostPlatformOf,
  hostReadiness,
  isMacHost,
  isWindowsAbsPath,
  isWindowsHost,
  launcherHosts,
  launcherLimits,
  maxRunFields,
  newLauncherId,
  normalizeName,
  parseAutoResumeOk,
  parseMaxRun,
  toLauncher,
  toRun,
  validateDraft,
  validHeadlessBin,
  validWindowsBin,
  validateRunOptions,
  windowsPathProblem,
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

/** 旧版交互式预设（已停用，#245）：kind 缺省、带命令。 */
const ok = (over: Partial<TerminalLauncher> = {}): TerminalLauncher => ({ id: 'l-1', name: 'Cursor CLI', host: HOST, dir: '/srv/app', command: 'agent', ...over })

describe('启动器取值范围', () => {
  it('服务端没给就用默认值，给了就用服务端的', () => {
    assert.deepEqual(launcherLimits(undefined), LAUNCHER_DEFAULT_LIMITS)
    assert.deepEqual(launcherLimits(null), LAUNCHER_DEFAULT_LIMITS)
    assert.deepEqual(launcherLimits({ launcher_max: 5, launcher_name_max: 10, launcher_dir_max: 100 }), {
      ...LAUNCHER_DEFAULT_LIMITS, max: 5, name: 10, dir: 100,
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
  it('续聊模板：名字是「Cursor CLI 续聊」，主机带上，每次都是新编号', () => {
    const a = headlessDraft(HOST)
    const b = headlessDraft(HOST)
    assert.equal(a.name, CURSOR_CLI_RESUME_NAME)
    assert.equal(a.host, HOST)
    assert.deepEqual({ ...a, id: '' }, { id: '', name: CURSOR_CLI_RESUME_NAME, host: HOST, dir: '', run: blankRunDraft() })
    assert.notEqual(a.id, b.id)
  })
  it('预设与草稿互转，缺省字段变成空串；提交时规整空白', () => {
    assert.deepEqual(draftOf({ id: 'a', name: 'x', host: HOST, kind: 'headless' }), { id: 'a', name: 'x', host: HOST, dir: '', run: blankRunDraft() })
    assert.deepEqual(toLauncher({ id: 'a', name: '  Cursor \t 续聊\n', host: ` ${HOST} `, dir: ' /srv ', run: { ...blankRunDraft(), workflow: 'wf' } }), {
      id: 'a',
      name: 'Cursor 续聊',
      host: HOST,
      dir: '/srv',
      kind: 'headless',
      run: { posture: 'allowlist', source: 'resume', workflow_id: 'wf' },
    })
    assert.equal(normalizeName('  a   b\tc  '), 'a b c')
  })
})

describe('旧版（已停用）的交互式预设（#245）', () => {
  const headless: TerminalLauncher = { id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1' } }

  it('不是「非交互续聊」的预设都算旧版（缺省的 kind 就是交互式；服务端把 terminal 规整成缺省）', () => {
    assert.equal(isLegacyPreset(ok()), true)
    assert.equal(isLegacyPreset(ok({ command: '' })), true, '只开终端、不输入命令的也是')
    assert.equal(isLegacyPreset(headless), false)
    // 与「是不是续聊」互补：任何一条预设恰好是其中一种，清单里的行不会既有「运行」又被标成旧版。
    for (const l of [ok(), ok({ command: '' }), headless]) assert.notEqual(isLegacyPreset(l), isHeadlessPreset(l))
  })

  it('标记与说明：写明已停用、只能删除、去哪里开终端', () => {
    assert.match(LEGACY_PRESET_BADGE, /已停用/)
    assert.match(LEGACY_PRESET_HINT, /已停用/)
    assert.match(LEGACY_PRESET_HINT, /只能删除/)
    assert.match(LEGACY_PRESET_HINT, /ExecMesh/)
  })
})

describe('非交互续聊预设的草稿与校验（#126）', () => {
  const runOk = (over: Partial<NonNullable<TerminalLauncher['run']>> = {}): TerminalLauncher => ({
    id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'allowlist', workflow_id: 'wf-1', ...over },
  })
  const draft = (over: Partial<TerminalLauncher> = {}, run: Partial<NonNullable<TerminalLauncher['run']>> = {}) => draftOf({ ...runOk(run), ...over })
  const check = (d = draft(), others: TerminalLauncher[] = []) => validateDraft(d, { limits, others })

  it('互转：续聊预设转草稿再转回去，选项原样回来，不带命令与守护', () => {
    assert.equal(isHeadlessPreset(ok()), false)
    assert.equal(isHeadlessPreset(runOk()), true)

    const d = draftOf(runOk({ bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', posture: 'force', conversation_id: 'chat-9', prompt: '继续' }))
    assert.deepEqual(d.run, { bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', posture: 'force', workflow: 'wf-1', conversation: 'chat-9', prompt: '继续', maxRun: '', maxRunUnit: 'hour', autoResume: false, autoResumeOk: '', source: 'resume', afterNew: 'remember', origin: '' })
    assert.deepEqual(toLauncher(d), {
      id: 'r-1', name: '续聊', host: HOST, dir: '/srv/app', kind: 'headless',
      run: { posture: 'force', source: 'resume', bin: 'cursor-agent', model: 'claude-sonnet-5-5-max', workflow_id: 'wf-1', conversation_id: 'chat-9', prompt: '继续' },
    })
  })

  it('提交时规整：空字段不带，提示词里的空白（含换行）折成一个空格', () => {
    const t = toLauncher({ ...headlessDraft(HOST), id: 'r-2', name: '  续聊 ', dir: ' /w ', run: { ...blankRunDraft(), workflow: ' wf ', model: '  ', prompt: '  继续\n  做完   %workflow  ' } })
    assert.deepEqual(t, { id: 'r-2', name: '续聊', host: HOST, dir: '/w', kind: 'headless', run: { posture: 'allowlist', source: 'resume', workflow_id: 'wf', prompt: '继续 做完 %workflow' } })
    assert.equal(collapseSpaces('  a \t b\n\nc '), 'a b c')
    assert.equal(draftOf({ ...runOk(), run: undefined, kind: 'headless' }).run?.posture, 'allowlist', '缺运行配置时给空的草稿而不是崩')
  })

  it('合法的续聊草稿没有错误', () => {
    assert.deepEqual(check(), {})
    assert.deepEqual(check(draft({}, { workflow_id: '', conversation_id: 'e33e27ef-0fbb-46e4-9446-5dee56ae61fa' })), {})
    assert.deepEqual(check(draft({}, { bin: '~/.local/bin/agent', model: 'vendor/model:tag-1.5', posture: 'force', prompt: '继续 %workflow' })), {})
  })

  it('目录必填且必须是绝对路径：/ 开头，或 Windows 的盘符 / UNC 路径（#178）；~ 与相对写法不行', () => {
    assert.match(check(draft({ dir: '' })).dir ?? '', /请填写目录/)
    for (const dir of ['~/work', 'app', './app', 'C:work', 'C:', '\\\\srv']) {
      assert.ok(check(draft({ dir })).dir, `${dir} 应被拒绝`)
    }
    assert.match(check(draft({ dir: '~/work' })).dir ?? '', /绝对路径/)
    assert.equal(check(draft({ dir: '/srv/应用' })).dir, undefined)
    for (const dir of ['C:\\work', 'D:/项目/app', 'C:\\Program Files\\应用 一', '\\\\srv\\share\\app']) {
      assert.equal(check(draft({ dir })).dir, undefined, `${dir} 应合法`)
    }
  })

  it('Windows 路径的目录里有 cmd 语法字符、控制字符或多余的冒号：拒绝并说明是哪个字符（#178）', () => {
    for (const [dir, want] of [
      ['C:\\R&D', /「&」/], ['C:\\a"b', /「"」/], ['C:\\100%', /「%」/], ['C:\\a^b', /「\^」/], ['C:\\a|b', /「\|」/],
      ['C:\\a<b', /「<」/], ['C:\\a>b', /「>」/], ['C:\\hello!', /「!」/], ['C:\\*.txt', /「\*」/], ['C:\\a?b', /「\?」/],
      ['C:\\a:b', /冒号/],
    ] as const) {
      assert.match(check(draft({ dir })).dir ?? '', want, `${dir}`)
      assert.match(check(draft({ dir })).dir ?? '', /^目录/, `${dir}：原因要接在「目录」后面`)
    }
    // POSIX 路径不受这些规则影响（它们在 shell 里是被单引号包住的）
    for (const dir of ['/srv/R&D', '/srv/a b', '/srv/100%', "/srv/it's"]) assert.equal(check(draft({ dir })).dir, undefined, `${dir}`)
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

  it('可执行文件（#178）：Windows 的命令名与盘符 / UNC 路径合法；cmd、powershell 这类 shell 与带 cmd 语法字符的路径不行', () => {
    for (const bin of ['agent.cmd', 'C:\\Users\\张三\\AppData\\Local\\cursor-agent\\agent.cmd', 'C:\\Program Files\\Cursor Agent\\agent.exe', 'C:/tools/agent.cmd', '\\\\srv\\share\\bin\\agent.cmd', '/bin/bash', '/usr/bin/zsh']) {
      assert.equal(check(draft({}, { bin })).bin, undefined, `${bin} 应合法`)
    }
    for (const bin of ['cmd', 'CMD', 'cmd.exe', 'powershell', 'PowerShell.exe', 'pwsh', 'wsl', 'C:\\Windows\\System32\\cmd.exe', 'C:\\Program Files\\Git\\bin\\bash.exe', 'C:\\R&D\\agent.cmd', 'C:\\a"b\\agent.cmd', 'C:\\bin\\', 'bin\\agent.cmd', 'C:agent.cmd']) {
      assert.ok(check(draft({}, { bin })).bin, `${bin} 应被拒绝`)
    }
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
    assert.equal(check(draft({}, { prompt: '字'.repeat(1500) })).prompt, undefined)
    assert.match(check(draft({}, { prompt: '字'.repeat(1501) })).prompt ?? '', /最长 1500 个字，现在是 1501 个字/)
    assert.equal(check(draft({}, { prompt: '字 '.repeat(750) })).prompt, undefined, '折叠后 1499 个字，没超')
    assert.match(check(draft({}, { prompt: 'a\x1bb' })).prompt ?? '', /控制字符/)
  })

  it('名字：必填、不超长、不重名（不分大小写、空白折叠后比较，且不算它自己）', () => {
    assert.equal(check(draft({ name: '   ' })).name, '请给预设起个名字')
    assert.match(check(draft({ name: '字'.repeat(41) })).name ?? '', /名字最长 40 个字，现在是 41 个字/)
    assert.equal(check(draft({ name: '字'.repeat(40) })).name, undefined)
    assert.match(check(draft({ name: 'a\x1bb' })).name ?? '', /控制字符/)
    const other = { ...runOk(), id: 'r-2', name: 'cursor resume' }
    assert.match(check(draft({ name: 'Cursor Resume' }), [other]).name ?? '', /已经有一个叫「cursor resume」的预设/)
    assert.equal(check(draft({ id: 'r-2', name: 'Cursor Resume' }), [other]).name, undefined, '改的就是这一条自己，不算重名')
    assert.match(check(draft({ name: 'CURSOR   RESUME' }), [other]).name ?? '', /已经有一个叫/)
  })

  it('主机必填', () => {
    assert.equal(check(draft({ host: '' })).host, '请选择一台主机')
  })

  it('目录：换行 / 控制字符与超长被拒绝', () => {
    assert.match(check(draft({ dir: '/a\nb' })).dir ?? '', /换行或控制字符/)
    assert.match(check(draft({ dir: '/' + 'd'.repeat(512) })).dir ?? '', /目录最长 512/)
  })

  it('用服务端给的范围校验', () => {
    const tight = { ...LAUNCHER_DEFAULT_LIMITS, runPrompt: 5, runModel: 4, runBin: 3 }
    const e = validateDraft(draft({}, { prompt: '123456', model: 'abcde', bin: 'abcd' }), { limits: tight, others: [] })
    assert.deepEqual(Object.keys(e).sort(), ['bin', 'model', 'prompt'])
    const small = { ...LAUNCHER_DEFAULT_LIMITS, name: 5, dir: 10 }
    assert.deepEqual(Object.keys(validateDraft(draft({ name: '123456', dir: '/12345678901' }), { limits: small, others: [] })).sort(), ['dir', 'name'])
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

// ---------- Windows / macOS 主机（#163） ----------
// 没有真实的 Windows / macOS 主机可用：这里只按 Agent 握手时自报的系统名构造平台信息。
const WIN = { os: 'windows' }
const MAC = { os: 'macOS 14.5' }
const LINUX = { os: 'Debian GNU/Linux 13 (trixie)' }

describe('主机平台（#163）', () => {
  it('从主机信息取平台：系统名来自 Agent 握手时的自报', () => {
    assert.deepEqual(hostPlatformOf(host({ os: 'windows' })), WIN)
    assert.deepEqual(hostPlatformOf(host({ os: 'macOS 14.5' })), MAC)
    assert.deepEqual(hostPlatformOf(undefined), { os: undefined })
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
})

describe('目录写法的提醒（#163）', () => {
  it('不知道主机系统（没选主机、旧数据）或目录为空：不说话', () => {
    assert.equal(dirAdvice('C:\\work', undefined), '')
    assert.equal(dirAdvice('C:\\work', {}), '')
    assert.equal(dirAdvice('', WIN), '')
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
    assert.match(dirAdvice('/srv/app', WIN), /盘符/)
    assert.match(dirAdvice('/srv/app', 'windows'), /盘符/, '老写法：只传系统名')
  })

  it('Windows 主机 + 盘符路径、非 Windows 主机 + / 开头的路径：不提醒；~ 本来就是错误，由校验报，这里不说话（#178）', () => {
    assert.equal(dirAdvice('C:\\work', WIN), '')
    assert.equal(dirAdvice('C:\\work', 'windows'), '')
    assert.equal(dirAdvice('/srv/app', LINUX), '')
    assert.equal(dirAdvice('~/work', WIN), '')
    assert.equal(dirAdvice('~/work', LINUX), '')
  })

  it('提醒的内容：非 Windows 要以 / 开头（不提 ~），Windows 要写盘符路径', () => {
    assert.match(dirAdvice('C:\\work', LINUX), /以 \/ 开头。/)
    assert.doesNotMatch(dirAdvice('C:\\work', LINUX), /~/)
  })

  it('目录输入框的示例跟着主机系统走', () => {
    assert.equal(dirPlaceholder(WIN), 'C:\\work\\RelayMesh')
    assert.equal(dirPlaceholder('windows'), 'C:\\work\\RelayMesh')
    assert.equal(dirPlaceholder(MAC), '/Users/你/projects/RelayMesh')
    assert.equal(dirPlaceholder(LINUX), '/home/user/RelayMesh')
    assert.equal(dirPlaceholder(undefined), '/home/user/RelayMesh')
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

describe('预设的对话来源：新会话 / 最近会话 / 指定会话（#191，沿用 #176 第三期的「新开对话」）', () => {
  const base: TerminalLauncher = { id: 'ea-1', name: '启用 Agent · x', host: HOST, dir: '/srv/app', kind: 'headless', run: { posture: 'force', workflow_id: 'wf-1' } }
  const lim = { ...LAUNCHER_DEFAULT_LIMITS, runSupported: true, runNewPromptDefault: '新开提示词' }
  const errorsOf = (run: Partial<RunDraft>) => {
    const d = draftOf(base)
    d.run = { ...blankRunDraft(), ...d.run, workflow: 'wf-1', ...run }
    return validateDraft(d, { limits: lim, others: [] })
  }
  const runOf = (run: Partial<RunDraft>) => toRun({ ...blankRunDraft(), workflow: 'wf-1', ...run })

  it('默认是指定会话：新草稿与存量预设都是 resume，选「运行之后」的默认是记下编号', () => {
    assert.equal(blankRunDraft().source, 'resume')
    assert.equal(blankRunDraft().afterNew, 'remember')
    assert.equal(blankRunDraft().origin, '')
    assert.equal(headlessDraft(HOST).run?.source, 'resume')
    assert.equal(draftOf(base).run?.source, 'resume')
    assert.equal(draftOf(base).run?.afterNew, 'remember')
    const out = toLauncher(draftOf(base)).run ?? {}
    assert.equal(out.source, 'resume', '提交时总是带明确的来源（存量预设保存一次之后就不再靠旧字段推导）')
    assert.equal('after_new' in out, false, '只有新会话才带「运行之后」')
    assert.equal('new_conversation' in out, false, '旧字段不再提交（服务端自己维护镜像）')
    assert.equal('origin' in out, false)
  })

  it('存量预设按旧字段推导：只认明确的 true 才是新会话；设了 source 就用 source', () => {
    const legacy = (v: unknown) => draftOf({ ...base, run: { ...base.run, new_conversation: v as boolean } }).run
    assert.equal(legacy(true)?.source, 'new')
    assert.equal(legacy(true)?.afterNew, 'remember')
    for (const v of [false, undefined, null, 0, 1, 'true'] as unknown[]) assert.equal(legacy(v)?.source, 'resume', String(v))
    // 旧字段是 true 但已经明确设了别的来源：以 source 为准
    assert.equal(draftOf({ ...base, run: { ...base.run, new_conversation: true, source: 'continue' } }).run?.source, 'continue')
    assert.equal(effectiveSource({ source: 'continue', new_conversation: false }), 'continue')
    assert.equal(effectiveSource({ source: 'bogus' as never, new_conversation: true }), 'new', '不认识的 source 当作没设')
    assert.equal(effectiveSource(undefined), 'resume')
  })

  it('「运行之后」：只在新会话下有意义，默认记下编号，stay / continue 原样往返', () => {
    assert.equal(effectiveAfterNew({ source: 'resume' }), '')
    assert.equal(effectiveAfterNew({ source: 'continue', after_new: 'stay' }), '', '最近会话没有「运行之后」')
    assert.equal(effectiveAfterNew({ source: 'new' }), 'remember')
    assert.equal(effectiveAfterNew({ new_conversation: true }), 'remember')
    assert.equal(effectiveAfterNew({ source: 'new', after_new: 'bogus' as never }), 'remember')
    for (const a of ['stay', 'continue'] as const) {
      const saved = toLauncher(draftOf({ ...base, run: { ...base.run, source: 'new', after_new: a } }))
      assert.equal(saved.run?.source, 'new')
      assert.equal(saved.run?.after_new, a)
      assert.equal(draftOf(saved).run?.afterNew, a)
    }
    assert.equal(runOf({ source: 'new', afterNew: undefined }).after_new, 'remember')
  })

  it('提交：对话编号只在指定会话时带，另外两种来源下藏着的输入不会悄悄生效', () => {
    assert.equal(runOf({ source: 'resume', conversation: ' chat-1 ' }).conversation_id, 'chat-1')
    for (const source of ['new', 'continue'] as const) {
      const out = runOf({ source, conversation: 'chat-1' })
      assert.equal(out.source, source)
      assert.equal('conversation_id' in out, false, source)
    }
    assert.equal('after_new' in runOf({ source: 'continue', afterNew: 'stay' }), false)
    // 草稿里没有 source（会话页「运行」）= 指定会话
    assert.equal(draftSource({}), 'resume')
    assert.equal(draftSource({ source: 'bogus' as never }), 'resume')
    assert.equal(draftSource({ source: 'continue' }), 'continue')
  })

  it('来源标记不是表单的选项，但编辑保存时必须原样带回去（否则编辑一次「启用 Agent」创建的预设就丢了标记）', () => {
    const enabled: TerminalLauncher = { ...base, run: { ...base.run, new_conversation: true, origin: 'enable_agent' } }
    const d = draftOf(enabled)
    assert.equal(d.run?.origin, 'enable_agent')
    assert.equal(d.run?.source, 'new')
    d.name = '改了个名字'
    const saved = toLauncher(d)
    assert.equal(saved.run?.origin, 'enable_agent')
    assert.equal(saved.run?.source, 'new')
    assert.equal(saved.run?.after_new, 'remember')
    // 手工建的没有标记：不凭空带
    assert.equal('origin' in (toLauncher(draftOf(base)).run ?? {}), false)
  })

  it('校验：新会话与最近会话都要填工作流编号；指定会话要工作流编号或对话编号（与服务端一致）', () => {
    assert.equal(hasErrors(errorsOf({ source: 'new' })), false)
    assert.equal(hasErrors(errorsOf({ source: 'continue' })), false)
    assert.match(errorsOf({ source: 'new', workflow: '' }).workflow ?? '', /「新会话」要填工作流编号/)
    assert.match(errorsOf({ source: 'continue', workflow: '' }).workflow ?? '', /「最近会话」要填工作流编号/)
    // 最近会话不能靠固定对话编号蒙混过关：藏着的编号不提交，所以只填编号也算缺工作流
    assert.match(errorsOf({ source: 'continue', workflow: '', conversation: 'chat-1' }).workflow ?? '', /「最近会话」要填工作流编号/)
    assert.match(errorsOf({ source: 'resume', workflow: '' }).workflow ?? '', /请填工作流编号/)
    assert.equal(errorsOf({ source: 'resume', workflow: '', conversation: 'chat-1' }).workflow, undefined, '指定会话只固定编号也行')
    assert.equal(errorsOf({ source: 'resume', conversation: 'chat-1' }).conversation, undefined)
    assert.match(errorsOf({ source: 'resume', conversation: 'a b' }).conversation ?? '', /对话编号只能含/)
    // 选了新会话 / 最近会话后，之前填的指定会话编号即使不合法也不再挡保存（它不会被提交）
    assert.equal(errorsOf({ source: 'new', conversation: 'a b' }).conversation, undefined)
    assert.equal(errorsOf({ source: 'continue', conversation: 'a b' }).conversation, undefined)
  })

  it('会话页「运行」的草稿没有来源：按指定会话校验与提交（不带 source 之外的新字段）', () => {
    const e = validateRunOptions({ bin: '', model: '', posture: 'allowlist', workflow: '', conversation: '', prompt: '' }, lim)
    assert.match(e.workflow ?? '', /请填工作流编号/)
    assert.equal(validateRunOptions({ bin: '', model: '', posture: 'allowlist', workflow: 'wf-1', conversation: '', prompt: '' }, lim).workflow, undefined)
  })

  it('旧版 Hub 不返回新会话的默认提示词时：limits 里是空串（表单不提供三选一，除非预设本来就不是指定会话）', () => {
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

// 与服务端 internal/store/headless_paths_test.go 逐条对齐的表：前后端对「什么写法合法」必须给出同一个答案，
// 否则要么前端放行、服务端拒绝（用户点保存才看到错误），要么前端拦住了服务端本来接受的写法。
describe('Windows 路径与可执行文件的写法（#178，与服务端同一套规则）', () => {
  it('isWindowsAbsPath：盘符或 UNC 才算绝对路径', () => {
    const table: Array<[string, boolean]> = [
      ['C:\\work', true], ['c:\\work\\项目', true], ['C:/work', true], ['C:\\', true], ['D:\\Program Files (x86)\\x', true],
      ['\\\\srv\\share', true], ['\\\\srv\\share\\dir', true], ['\\\\srv/share', true],
      ['\\\\srv', false], ['\\\\\\share', false], ['C:work', false], ['C:', false], ['/home/me', false], ['~/work', false],
      ['work\\sub', false], ['', false], ['1:\\x', false], ['CC:\\x', false],
    ]
    for (const [p, want] of table) assert.equal(isWindowsAbsPath(p), want, JSON.stringify(p))
  })

  it('windowsPathProblem：没问题返回空串，有问题的指出字符', () => {
    for (const p of ['C:\\work', 'C:\\Program Files (x86)\\Cursor', "C:\\Users\\O'Brien\\proj", 'C:\\用户\\项目 一', '\\\\srv\\share\\a b',
      'C:\\a.b\\c-d_e+f', 'C:\\路径\\‘弯引号’', 'D:\\#hash\\@at\\[br]\\{cb}', 'C:\\a,b;c=d', 'C:\\$HOME\\x', 'C:\\back`tick']) {
      assert.equal(windowsPathProblem(p), '', JSON.stringify(p))
    }
    const bad: Array<[string, string]> = [
      ['C:\\a"b', '「"」'], ['C:\\100%', '「%」'], ['C:\\a^b', '「^」'], ['C:\\R&D', '「&」'], ['C:\\a|b', '「|」'], ['C:\\a<b', '「<」'],
      ['C:\\a>b', '「>」'], ['C:\\hello!', '「!」'], ['C:\\*.txt', '「*」'], ['C:\\a?b', '「?」'],
      ['C:\\a\nb', '控制字符'], ['C:\\a\u0000b', '控制字符'], ['C:\\a\tb', '控制字符'], ['C:\\a:b', '冒号'], ['\\\\srv\\sh:re', '冒号'],
    ]
    for (const [p, want] of bad) assert.ok(windowsPathProblem(p).includes(want), `${JSON.stringify(p)} 应报含 ${want}，得到 ${windowsPathProblem(p)}`)
  })

  it('validHeadlessBin：预设保存时不知道主机系统，POSIX 与 Windows 两种写法都接受', () => {
    const valid = [
      'agent', 'cursor-agent', '~/.local/bin/agent', '/usr/local/bin/agent', './agent', 'a.b_c+d-e', 'agent.cmd', '/bin/bash', '/usr/bin/zsh',
      'C:\\Users\\张三\\AppData\\Local\\cursor-agent\\agent.cmd', 'C:\\Program Files\\Cursor Agent\\agent.exe', 'C:/tools/agent.cmd',
      '\\\\srv\\share\\bin\\agent.cmd', 'D:\\agent',
    ]
    for (const b of valid) assert.equal(validHeadlessBin(b), true, `${b} 应合法`)
    const invalid = [
      '', ' ', 'agent --x', 'agent;rm', 'agent&x', 'a b', '$(x)', '`x`', 'agent\n', "'agent'", '"agent"',
      'cd', 'env', 'sudo', 'bash', 'sh', 'exec', 'time', 'nohup',
      'cmd', 'CMD', 'cmd.exe', 'powershell', 'PowerShell.exe', 'pwsh', 'pwsh.exe', 'wsl', 'wsl.exe',
      'C:\\Windows\\System32\\cmd.exe', 'C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe', 'C:\\Program Files\\PowerShell\\7\\pwsh.exe',
      'C:\\Program Files\\Git\\bin\\bash.exe', 'C:\\tools\\sh.exe',
      'C:\\R&D\\agent.cmd', 'C:\\a"b\\agent.cmd', 'C:\\100%\\agent.cmd', 'C:\\a^b\\agent.cmd', 'C:\\a|b\\agent.cmd', 'C:\\hello!\\agent.cmd',
      'C:\\bin\\', 'C:/bin/', ' C:\\bin\\agent.cmd', 'C:\\bin\\agent.cmd ', 'bin\\agent.cmd', 'C:agent.cmd', '\\\\srv',
    ]
    for (const b of invalid) assert.equal(validHeadlessBin(b), false, `${JSON.stringify(b)} 应不合法`)
  })

  it('validWindowsBin：Windows 主机上只认命令名与盘符 / UNC 路径，POSIX 写法不能用', () => {
    for (const b of ['agent', 'agent.cmd', 'cursor-agent', 'C:\\x\\agent.cmd', 'C:\\Program Files\\x y\\agent.cmd', '\\\\h\\s\\agent.cmd']) {
      assert.equal(validWindowsBin(b), true, `${b} 应合法`)
    }
    for (const b of ['~/.local/bin/agent', '/usr/bin/agent', './agent', '../agent', 'a/b', 'a\\b', '', 'cmd', 'time', 'cd', 'set']) {
      assert.equal(validWindowsBin(b), false, `${JSON.stringify(b)} 应不合法`)
    }
  })
})
