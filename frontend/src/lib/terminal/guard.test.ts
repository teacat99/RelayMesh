// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { GuardItem, GuardState, GuardWakePlan, LauncherGuard } from '../../api/terminal'
import {
  GUARD_DEFAULT_LIMITS,
  blankGuardDraft,
  guardAttention,
  guardDraftOf,
  guardHints,
  guardLimits,
  guardNeedsAttention,
  guardStateView,
  guardSupported,
  hasGuardErrors,
  headlessButton,
  lastWakeText,
  toGuard,
  usesChatId,
  validateGuardDraft,
  wakeAdvice,
  wakeButton,
  wakeNotice,
  wakeStartLine,
  type GuardDraft,
} from './guard'

function item(over: Partial<GuardItem> & { state?: GuardState } = {}): GuardItem {
  const { state = 'stalled', ...rest } = over
  return {
    preset_id: 'l-1',
    name: 'Cursor CLI',
    host: 'RM-AAAA',
    enabled: true,
    silence_minutes: 30,
    verdict: { state, reason: '一句人话', open_work: { unread_reply: false, poll_lost: false, inbox_unread: 0, doing_tasks: 0 } },
    wakes_24h: 0,
    no_progress_streak: 0,
    wake_mode: 'type',
    chat_known: true,
    ...rest,
  }
}

function plan(over: Partial<GuardWakePlan> = {}): GuardWakePlan {
  return { mode: 'relaunch', name: 'Cursor CLI', host: 'RM-AAAA', prompt: '【系统唤醒 1/3 · 原因：会话意外结束】请继续', command: 'agent --resume abc', dir: '/srv/app', resume: true, append_prompt: true, n: 1, max: 3, over_budget: false, ...over }
}

describe('取值范围', () => {
  it('旧版 Hub 没有守护限额：用内置默认值，并认为不支持', () => {
    assert.deepEqual(guardLimits(undefined), GUARD_DEFAULT_LIMITS)
    assert.deepEqual(guardLimits({}), GUARD_DEFAULT_LIMITS)
    assert.equal(guardSupported(undefined), false)
    assert.equal(guardSupported({ launcher_max: 20 }), false)
  })
  it('服务端给了就用服务端的；非法值（0、负数、NaN）回落到默认', () => {
    const l = guardLimits({ guard_silence_min: 10, guard_silence_max: 60, guard_prompt_max: 120, guard_wake_max: 5, guard_prompt_unsafe: '$;', guard_prompt_default: '默认正文 %workflow' }, 500)
    assert.equal(l.silenceMin, 10)
    assert.equal(l.silenceMax, 60)
    assert.equal(l.promptMax, 120)
    assert.equal(l.wakeMax, 5)
    assert.equal(l.promptUnsafe, '$;')
    assert.equal(l.promptDefault, '默认正文 %workflow')
    assert.equal(l.resumeMax, 500)
    assert.equal(l.silenceDefault, GUARD_DEFAULT_LIMITS.silenceDefault)
    const bad = guardLimits({ guard_silence_min: 0, guard_prompt_max: -1, guard_wake_max: Number.NaN }, 0)
    assert.equal(bad.silenceMin, GUARD_DEFAULT_LIMITS.silenceMin)
    assert.equal(bad.promptMax, GUARD_DEFAULT_LIMITS.promptMax)
    assert.equal(bad.wakeMax, GUARD_DEFAULT_LIMITS.wakeMax)
    assert.equal(bad.resumeMax, GUARD_DEFAULT_LIMITS.resumeMax)
    assert.equal(guardSupported({ guard_prompt_max: 300 }), true)
  })
})

describe('草稿 ⇄ 配置', () => {
  it('什么都没填、也没打开：不落盘（undefined）', () => {
    assert.equal(toGuard(blankGuardDraft()), undefined)
    assert.equal(toGuard(guardDraftOf(undefined)), undefined)
    assert.equal(toGuard({ ...blankGuardDraft(), workflow: '   ', prompt: ' \n ' }), undefined)
  })
  it('打开了就要保存，哪怕别的都没填', () => {
    assert.deepEqual(toGuard({ ...blankGuardDraft(), enabled: true }), { enabled: true })
  })
  it('规整：编号与命令去首尾空白，提示词折叠成单行，停摆时长转成数字，空字段不出现', () => {
    const d: GuardDraft = { enabled: true, workflow: '  wf-1 ', silence: ' 45 ', resume: '  agent --resume {chat_id}  ', prompt: '  第一行\n  第二行\t结束 ', exitEndsSession: false, headless: false, posture: 'allowlist' }
    assert.deepEqual(toGuard(d), {
      enabled: true,
      workflow_id: 'wf-1',
      silence_minutes: 45,
      resume_command: 'agent --resume {chat_id}',
      wake_prompt: '第一行 第二行 结束',
    })
  })
  it('关着监测但填过内容：保留（下次打开不用重填）', () => {
    const g = toGuard({ ...blankGuardDraft(), workflow: 'wf-1', resume: 'agent --resume {chat_id}' })
    assert.deepEqual(g, { enabled: false, workflow_id: 'wf-1', resume_command: 'agent --resume {chat_id}' })
  })
  it('往返一致', () => {
    const g: LauncherGuard = { enabled: true, workflow_id: 'wf', silence_minutes: 20, resume_command: 'agent --resume {chat_id}', wake_prompt: '请继续 %n' }
    assert.deepEqual(toGuard(guardDraftOf(g)), g)
  })
  it('exit_ends_session 往返', () => {
    const g: LauncherGuard = { enabled: true, exit_ends_session: true, workflow_id: 'wf' }
    assert.deepEqual(toGuard(guardDraftOf(g)), g)
  })
  it('停摆时长不是整数：按没填处理（保存前校验会先拦住）', () => {
    assert.equal(toGuard({ ...blankGuardDraft(), enabled: true, silence: '4.5' })?.silence_minutes, undefined)
    assert.equal(toGuard({ ...blankGuardDraft(), enabled: true, silence: 'abc' })?.silence_minutes, undefined)
  })
})

describe('校验（与服务端一致）', () => {
  const ok = (d: Partial<GuardDraft>) => validateGuardDraft({ ...blankGuardDraft(), ...d })

  it('全空 / 合法值没有错误', () => {
    assert.deepEqual(ok({}), {})
    assert.deepEqual(ok({ enabled: true, workflow: 'relaymesh-continuation', silence: '30', resume: 'agent --resume {chat_id}', prompt: '请继续，第 %n 次' }), {})
    assert.equal(hasGuardErrors(ok({})), false)
  })
  it('停摆时长：整数、在范围内；留空合法', () => {
    for (const s of ['4', '721', '0', '-5', '4.5', '三十', '1e2']) assert.ok(ok({ silence: s }).silence, `${s} 应报错`)
    for (const s of ['5', '30', '720', '', '  ']) assert.equal(ok({ silence: s }).silence, undefined, `${s} 应合法`)
    assert.match(ok({ silence: '1' }).silence ?? '', /5～720 分钟/)
  })
  it('工作流编号：不能有空白与控制字符，长度有限', () => {
    assert.ok(ok({ workflow: 'a b' }).workflow)
    assert.ok(ok({ workflow: 'a\tb' }).workflow)
    assert.ok(ok({ workflow: 'x'.repeat(129) }).workflow)
    assert.equal(ok({ workflow: 'x'.repeat(128) }).workflow, undefined)
    assert.equal(ok({ workflow: '工作流-一' }).workflow, undefined)
  })
  it('重新拉起命令：只能一行，长度有限', () => {
    assert.ok(ok({ resume: 'a\nb' }).resume)
    assert.ok(ok({ resume: 'x'.repeat(1001) }).resume)
    assert.equal(ok({ resume: 'agent --resume {chat_id} --yolo' }).resume, undefined)
  })
  it('提示词：不能有 shell 特殊字符，并指出是哪几个', () => {
    const e = ok({ prompt: '请 $(rm -rf) 然后 ; 继续' }).prompt ?? ''
    assert.match(e, /不能有/)
    assert.ok(e.includes('$') && e.includes('(') && e.includes(';'), e)
    for (const c of '`$\\;&|<>(){}[]\'"!*?~#') assert.ok(ok({ prompt: `请${c}继续` }).prompt, `${c} 应被拒绝`)
    // 中文标点、字母数字、下划线、%、冒号、逗号、点、连字符都没问题
    assert.equal(ok({ prompt: '第 %n/%max 次：请用 list_sessions 查看 relaymesh-continuation，然后继续。' }).prompt, undefined)
  })
  it('提示词：长度按字符数（中文算 1）', () => {
    assert.equal(ok({ prompt: '继'.repeat(300) }).prompt, undefined)
    assert.match(ok({ prompt: '继'.repeat(301) }).prompt ?? '', /最长 300 个字，现在是 301 个字/)
  })
  it('用服务端给的范围', () => {
    const l = guardLimits({ guard_silence_min: 10, guard_silence_max: 20 })
    assert.ok(validateGuardDraft({ ...blankGuardDraft(), silence: '9' }, l).silence)
    assert.equal(validateGuardDraft({ ...blankGuardDraft(), silence: '15' }, l).silence, undefined)
  })
})

describe('表单里的提醒', () => {
  it('没打开监测：什么都不说', () => {
    assert.deepEqual(guardHints(blankGuardDraft(), false), [])
  })
  it('打开了却缺工作流 / 缺重新拉起手段：各提醒一句', () => {
    const hints = guardHints({ ...blankGuardDraft(), enabled: true }, false)
    assert.equal(hints.length, 2)
    assert.match(hints[0], /工作流编号/)
    assert.match(hints[1], /重新拉起/)
  })
  it('有启动命令就不再提醒重新拉起；填了工作流就不提醒工作流', () => {
    const h = guardHints({ ...blankGuardDraft(), enabled: true, workflow: 'wf' }, true)
    assert.equal(h.length, 1)
    assert.match(h[0], /未开启「命令退出即结束会话」/)
    assert.equal(guardHints({ ...blankGuardDraft(), enabled: true, workflow: 'wf', exitEndsSession: true }, true).length, 1)
    assert.equal(guardHints({ ...blankGuardDraft(), enabled: true, workflow: 'wf', resume: 'agent' }, false).length, 0)
  })
  it('usesChatId', () => {
    assert.equal(usesChatId('agent --resume {chat_id}'), true)
    assert.equal(usesChatId('agent --continue'), false)
  })
})

describe('状态的显示', () => {
  it('各状态都有中文标签；只有「在干活」是忙碌态；意外结束与停摆是红色', () => {
    const all: GuardState[] = ['working', 'waiting', 'present', 'idle', 'ended', 'done', 'crashed', 'stalled', 'offline', 'unknown']
    for (const s of all) assert.match(guardStateView(s).label, /[\u4e00-\u9fa5]/)
    assert.equal(guardStateView('working').busy, true)
    assert.equal(all.filter(s => guardStateView(s).busy).length, 1)
    assert.equal(guardStateView('crashed').tone, 'bad')
    assert.equal(guardStateView('stalled').tone, 'bad')
    assert.equal(guardStateView('working').tone, 'ok')
  })
  it('不认识的状态按「无法判断」处理（新版服务端加了状态时界面不崩）', () => {
    assert.equal(guardStateView('hibernating').label, '无法判断')
    assert.equal(guardStateView(undefined).label, '无法判断')
  })
})

describe('需要处理的会话', () => {
  it('只有开了监测、并且意外结束或停摆才算', () => {
    assert.equal(guardNeedsAttention(item({ state: 'crashed' })), true)
    assert.equal(guardNeedsAttention(item({ state: 'stalled' })), true)
    for (const s of ['working', 'waiting', 'present', 'idle', 'ended', 'offline', 'unknown'] as GuardState[]) {
      assert.equal(guardNeedsAttention(item({ state: s })), false, s)
    }
    assert.equal(guardNeedsAttention(item({ state: 'crashed', enabled: false })), false)
  })
  it('汇总：数量、名字、悬停说明', () => {
    const a = guardAttention([item({ state: 'crashed', name: 'A' }), item({ state: 'working', name: 'B' }), item({ state: 'stalled', name: 'C' })])
    assert.equal(a.count, 2)
    assert.deepEqual(a.names, ['A', 'C'])
    assert.match(a.title, /「A」意外结束：一句人话/)
    assert.match(a.title, /「C」疑似停摆/)
    assert.deepEqual(guardAttention(null), { count: 0, names: [], title: '' })
  })
})

describe('唤醒前要不要先问一句', () => {
  it('已经意外结束 / 停摆：直接做', () => {
    assert.deepEqual(wakeAdvice(item({ state: 'crashed' })), { confirm: false, reasons: [] })
    assert.deepEqual(wakeAdvice(item({ state: 'stalled' })), { confirm: false, reasons: [] })
  })
  it('已完成（唤醒许可）：手动唤醒需确认', () => {
    const a = wakeAdvice(item({ state: 'done' }))
    assert.equal(a.confirm, true)
    assert.match(a.reasons.join(''), /已标记任务全部完成/)
  })
  it('看起来没出问题的会话：先问（说明为什么）', () => {
    for (const [s, re] of [['working', /正在干活/], ['waiting', /等你回复/], ['present', /有人连着/], ['idle', /没有待处理/]] as const) {
      const a = wakeAdvice(item({ state: s }))
      assert.equal(a.confirm, true, s)
      assert.match(a.reasons.join(''), re)
    }
  })
  it('一天内超过参考上限：先问', () => {
    assert.equal(wakeAdvice(item({ state: 'stalled', wakes_24h: 2 })).confirm, false)
    const a = wakeAdvice(item({ state: 'stalled', wakes_24h: 3 }))
    assert.equal(a.confirm, true)
    assert.match(a.reasons.join(''), /24 小时内已经唤醒 3 次（参考上限 3 次）/)
    assert.equal(wakeAdvice(item({ state: 'stalled', wakes_24h: 4 }), 5).confirm, false)
  })
  it('连续两次唤醒都没效果：提醒别再白费力气，并指向「重新拉起」', () => {
    assert.equal(wakeAdvice(item({ state: 'stalled', no_progress_streak: 1 })).confirm, false)
    const a = wakeAdvice(item({ state: 'stalled', no_progress_streak: 2 }))
    assert.equal(a.confirm, true)
    assert.match(a.reasons.join(''), /连续 2 次唤醒都没有效果/)
    assert.match(a.reasons.join(''), /重新拉起/)
  })
  it('几种情况叠加：逐条列出', () => {
    const a = wakeAdvice(item({ state: 'working', wakes_24h: 3, no_progress_streak: 3 }))
    assert.equal(a.reasons.length, 3)
  })
  it('已标记完成：唤醒前先确认', () => {
    const a = wakeAdvice(item({ state: 'done' }))
    assert.equal(a.confirm, true)
    assert.match(a.reasons.join(''), /标记任务全部完成/)
  })
})

describe('唤醒按钮', () => {
  it('会话活着：唤醒；已结束：重新拉起（并说明能不能接着原来的对话）', () => {
    const t = wakeButton(item({ wake_mode: 'type' }))
    assert.equal(t.text, '唤醒')
    assert.equal(t.disabled, false)
    const r = wakeButton(item({ wake_mode: 'relaunch', chat_known: true }))
    assert.equal(r.text, '重新拉起')
    assert.match(r.title, /接着原来的对话/)
    assert.match(wakeButton(item({ wake_mode: 'relaunch', chat_known: false })).title, /新开一个对话/)
  })
  it('做不了：置灰，并把服务端给的原因放在悬停里', () => {
    const b = wakeButton(item({ wake_mode: 'none', wake_note: '主机离线，现在没法唤醒' }))
    assert.equal(b.disabled, true)
    assert.equal(b.title, '主机离线，现在没法唤醒')
    assert.equal(wakeButton(item({ wake_mode: 'none' })).title, '现在没法唤醒')
  })
})

describe('重新拉起时输入的那一行', () => {
  it('把提示词放进单引号、追加在命令后面；有目录先 cd', () => {
    const r = wakeStartLine(plan(), 'linux')
    assert.equal(r.promptInline, true)
    assert.equal(r.line, "cd -- '/srv/app' && agent --resume abc '【系统唤醒 1/3 · 原因：会话意外结束】请继续'")
  })
  it('提示词里有单引号也安全（虽然校验不允许，这是最后一道保险）', () => {
    const r = wakeStartLine(plan({ prompt: "它说'好'" , dir: '' }), 'linux')
    assert.equal(r.line, `agent --resume abc '它说'\\''好'\\'''`)
  })
  it('服务端说不能追加（管道、重定向……）：只启动命令，提示词另行发送', () => {
    const r = wakeStartLine(plan({ append_prompt: false, command: 'agent | tee log' }), 'linux')
    assert.equal(r.promptInline, false)
    assert.equal(r.line, "cd -- '/srv/app' && agent | tee log")
  })
  it('Windows 主机不追加（shell 种类不定），也不拼 cd', () => {
    const r = wakeStartLine(plan(), 'windows')
    assert.equal(r.promptInline, false)
    assert.equal(r.line, 'agent --resume abc')
  })
  it('Windows 主机 + PowerShell：先 Set-Location，提示词仍不追加（另行发送）', () => {
    const r = wakeStartLine(plan({ dir: 'C:\\work\\app' }), { os: 'windows', shell: 'pwsh', home: 'C:\\Users\\me', sep: '\\' })
    assert.equal(r.promptInline, false)
    assert.equal(r.line, "Set-Location -LiteralPath 'C:\\work\\app' -ErrorAction Stop; agent --resume abc")
  })
  it('macOS 主机与 Linux 一样追加提示词（zsh / bash 都认单引号字面量）', () => {
    const r = wakeStartLine(plan(), { os: 'macOS 14.5', shell: 'zsh', home: '/Users/me', sep: '/' })
    assert.equal(r.promptInline, true)
    assert.equal(r.line, "cd -- '/srv/app' && agent --resume abc '【系统唤醒 1/3 · 原因：会话意外结束】请继续'")
  })
  it('没有命令：什么也不输入', () => {
    assert.deepEqual(wakeStartLine(plan({ command: '  ' }), 'linux'), { line: '', promptInline: false })
    assert.deepEqual(wakeStartLine(plan({ command: undefined }), 'linux'), { line: '', promptInline: false })
  })
  it('提示词为空时不追加一个空引号', () => {
    const r = wakeStartLine(plan({ prompt: '  ', dir: '' }), 'linux')
    assert.equal(r.promptInline, false)
    assert.equal(r.line, 'agent --resume abc')
  })
})

describe('唤醒之后的提示', () => {
  it('type：说清楚输入了什么，以及没动静时怎么办', () => {
    const n = wakeNotice(plan({ mode: 'type' }), false)
    assert.match(n.title, /已唤醒「Cursor CLI」（24 小时内第 1\/3 次）/)
    assert.match(n.description, /提示词已经输入到会话里/)
    assert.match(n.description, /重新拉起/)
  })
  it('relaunch：随命令带上了提示词 / 没带上（要自己发）', () => {
    assert.match(wakeNotice(plan(), true).description, /接着原来的对话.*随命令一起带上/)
    assert.match(wakeNotice(plan({ resume: false }), true).description, /新对话/)
    assert.match(wakeNotice(plan(), false).description, /需要你自己把提示词发给它/)
  })
  it('超过参考上限、服务端的提醒都带上', () => {
    const n = wakeNotice(plan({ over_budget: true, warnings: ['还不知道对话编号'] }), true)
    assert.match(n.description, /超过 24 小时参考上限/)
    assert.match(n.description, /还不知道对话编号/)
  })
})

describe('最近一次唤醒', () => {
  it('没有记录：空串', () => {
    assert.equal(lastWakeText(undefined, 10), '')
  })
  it('有效果 / 没动静 / 观察中', () => {
    const at = '2026-10-12T10:00:00Z'
    assert.equal(lastWakeText({ at, mode: 'type', reason: 'x', progress: 'progress' }, 10), '上次唤醒：有效果')
    assert.equal(lastWakeText({ at, mode: 'relaunch', reason: 'x', progress: 'none' }, 10), '上次重新拉起：10 分钟内没有动静')
    assert.equal(lastWakeText({ at, mode: 'type', reason: 'x', progress: 'pending' }, 10), '上次唤醒：观察中')
  })
})

describe('非交互唤醒（M3.4）', () => {
  it('草稿 ⇄ 配置：默认关闭且不落盘；打开后带姿态；关闭时保留非默认姿态；全空仍然是 undefined', () => {
    assert.equal(toGuard(blankGuardDraft()), undefined)
    assert.deepEqual(toGuard({ ...blankGuardDraft(), enabled: true }), { enabled: true })
    assert.deepEqual(toGuard({ ...blankGuardDraft(), enabled: true, headless: true }), { enabled: true, headless: { enabled: true } })
    assert.deepEqual(toGuard({ ...blankGuardDraft(), enabled: true, headless: true, posture: 'force' }), {
      enabled: true,
      headless: { enabled: true, posture: 'force' },
    })
    assert.deepEqual(toGuard({ ...blankGuardDraft(), enabled: true, headless: false, posture: 'force' }), {
      enabled: true,
      headless: { enabled: false, posture: 'force' },
    })
  })

  it('配置 → 草稿：没有配置是关闭 + allowlist；force 原样还原', () => {
    const d0 = guardDraftOf({ enabled: true })
    assert.equal(d0.headless, false)
    assert.equal(d0.posture, 'allowlist')
    const d1 = guardDraftOf({ enabled: true, headless: { enabled: true, posture: 'force' } })
    assert.equal(d1.headless, true)
    assert.equal(d1.posture, 'force')
    assert.equal(guardDraftOf({ enabled: true, headless: { enabled: true } }).posture, 'allowlist')
  })

  it('按钮：没开启不出现；不可用时置灰并写明原因；可用时说明姿态', () => {
    assert.equal(headlessButton(item()).show, false)
    const off = headlessButton(item({ headless: { enabled: true, posture: 'allowlist', ok: false, note: '会话还活着：请用「唤醒」' } }))
    assert.deepEqual([off.show, off.disabled], [true, true])
    assert.match(off.title, /会话还活着/)
    const on = headlessButton(item({ headless: { enabled: true, posture: 'allowlist', ok: true } }))
    assert.deepEqual([on.show, on.disabled], [true, false])
    assert.match(on.title, /白名单/)
    const forced = headlessButton(item({ headless: { enabled: true, posture: 'force', ok: true } }))
    assert.match(forced.title, /--force/)
  })

  it('提示与状态行：提交后说明去哪里看；等批准时点明；最近一次叫「非交互唤醒」', () => {
    const plan: GuardWakePlan = { mode: 'headless', name: 'Cursor CLI', host: 'RM-1', prompt: '', n: 1, max: 3, over_budget: false, run_id: 'r1', run_state: 'running', posture: 'allowlist' }
    const n = wakeNotice(plan, false)
    assert.match(n.title, /1\/3/)
    assert.match(n.description, /ExecMesh 的运行记录/)
    assert.doesNotMatch(n.description, /等你在 ExecMesh 里批准/)
    assert.match(wakeNotice({ ...plan, run_state: 'awaiting_approval' }, false).description, /等你在 ExecMesh 里批准/)
    assert.equal(lastWakeText({ at: '2026-10-15T00:00:00Z', mode: 'headless', reason: '手动唤醒', progress: 'pending' }, 10), '上次非交互唤醒：观察中')
  })
})
