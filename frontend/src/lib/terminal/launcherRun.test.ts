// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { HostView, LauncherRunPlan, LauncherRunView, TerminalLauncher } from '../../api/terminal'
import {
  DEFAULT_RUN_LIMIT_SEC,
  RUN_POLL_ACTIVE_MS,
  RUN_POLL_IDLE_MS,
  durationText,
  otherRunCancellable,
  otherRunHint,
  otherRunSource,
  othersSummary,
  runHint,
  runMeta,
  runNotice,
  runPollDelay,
  runReadiness,
  runStatus,
  runSummary,
  runsActive,
  autoResumeLine,
  runFailureLine,
  untilText,
} from './launcherRun'

const NOW = Date.parse('2026-10-15T08:00:00Z')
const iso = (offsetSec: number) => new Date(NOW + offsetSec * 1000).toISOString()

const view = (over: Partial<LauncherRunView> = {}): LauncherRunView => ({
  run_id: 'run-1', state: 'succeeded', active: false, created_at: iso(-60), started_at: iso(-58), ended_at: iso(-30), duration_ms: 28_000, ...over,
})

const host = (over: Partial<HostView> = {}): HostView =>
  ({ machine_code: 'RM-7K2F-9XQ4-M3WD-8ABC', status: 'enabled', online: true, os: 'linux', sessions: [], ...over }) as HostView

describe('非交互续聊：运行状态', () => {
  it('各状态给出文字与色调；只有「运行中」转圈，待批准是在等人', () => {
    assert.deepEqual(runStatus(view({ state: 'running', active: true })), { label: '运行中', tone: 'neutral', busy: true })
    assert.deepEqual(runStatus(view({ state: 'awaiting_approval', active: true })), { label: '待批准', tone: 'warn', busy: false })
    assert.deepEqual(runStatus(view()), { label: '已结束 · 成功', tone: 'good', busy: false })
    assert.deepEqual(runStatus(view({ silent: true })), { label: '已结束 · 空转退出', tone: 'warn', busy: false })
    assert.deepEqual(runStatus(view({ state: 'failed', exit_code: 2 })), { label: '失败（退出码 2）', tone: 'bad', busy: false })
    assert.deepEqual(runStatus(view({ state: 'failed' })), { label: '失败', tone: 'bad', busy: false })
    // 到点被主机强制结束：写「达到运行时长上限」（#141），这是用满了时限而不是命令出错，所以是提醒色而不是错误色。
    assert.deepEqual(runStatus(view({ state: 'timeout', timeout_sec: 1800 })), { label: '达到运行时长上限', tone: 'warn', busy: false })
    assert.equal(runStatus(view({ state: 'cancelled' })).tone, 'neutral')
    assert.equal(runStatus(view({ state: 'rejected' })).label, '被拒绝')
    assert.equal(runStatus(view({ state: 'denied' })).label, '被拒绝')
    assert.equal(runStatus(view({ state: 'lost' })).tone, 'warn')
    assert.equal(runStatus(view({ state: 'something-new', active: true })).busy, true, '不认识的状态：原样显示，是否转圈看 active')
  })

  it('空转只对成功的运行说；失败的运行即使 silent 也按失败显示', () => {
    assert.equal(runStatus(view({ state: 'failed', exit_code: 1, silent: true })).label, '失败（退出码 1）')
  })

  it('用时的口语写法', () => {
    assert.equal(durationText(0), '不到 1 秒')
    assert.equal(durationText(400), '不到 1 秒')
    assert.equal(durationText(12_000), '12 秒')
    assert.equal(durationText(60_000), '1 分')
    assert.equal(durationText(185_000), '3 分 5 秒')
    assert.equal(durationText(3_600_000), '1 小时')
    assert.equal(durationText(3_720_000), '1 小时 2 分')
    assert.equal(durationText(undefined), '')
    assert.equal(durationText(-5), '')
    assert.equal(durationText(Number.NaN), '')
  })

  it('状态行后半段：结束的说多久以前与用时；进行中说什么时候开始', () => {
    assert.equal(runMeta(view(), NOW), '30 秒前 · 用时 28 秒')
    assert.equal(runMeta(view({ duration_ms: undefined }), NOW), '30 秒前')
    assert.equal(runMeta(view({ state: 'running', active: true, ended_at: undefined, duration_ms: undefined }), NOW), '58 秒前开始')
    assert.equal(runMeta(view({ state: 'running', active: true, started_at: undefined, ended_at: undefined }), NOW), '1 分钟前开始', '还没开始（提交中）退回创建时间')
  })

  it('项目管理 Agent 发起的运行（#179）：状态行写明是谁，手动运行没有这一段', () => {
    assert.equal(runMeta(view({ by: '项目管理' }), NOW), '项目管理 Agent「项目管理」发起 · 30 秒前 · 用时 28 秒')
    assert.equal(runMeta(view({ by: '' }), NOW), '30 秒前 · 用时 28 秒')
    assert.equal(runMeta(view(), NOW), '30 秒前 · 用时 28 秒')
    // 与自动续跑的标记并存时发起者在最前面。
    assert.equal(
      runMeta(view({ by: 'A', auto: { enabled: true, max: 3, attempt: 1 } }), NOW),
      '项目管理 Agent「A」发起 · 自动续跑 1/3 · 30 秒前 · 用时 28 秒'
    )
  })

  it('补充说明：待批准、空转、状态丢失才有，成功与失败没有', () => {
    assert.match(runHint(view({ state: 'awaiting_approval', active: true })), /需批准.*ExecMesh/)
    assert.match(runHint(view({ silent: true })), /没有向 RelayMesh 发出任何消息/)
    assert.match(runHint(view({ state: 'lost' })), /主机重启或掉线/)
    // 到点结束：说清上限是多少、对话没有结束、想多跑一会儿去哪里调。
    assert.match(runHint(view({ state: 'timeout', timeout_sec: 1800 })), /最长运行时间（30 分钟）.*被主机强制结束.*对话本身没有结束.*「最长运行时间」调大/)
    assert.match(runHint(view({ state: 'timeout', timeout_sec: 7 * 24 * 3600 })), /（7 天）/)
    assert.doesNotMatch(runHint(view({ state: 'timeout' })), /（.*）/, '旧记录没有超时值：不写括号')
    assert.equal(runHint(view()), '')
    assert.equal(runHint(view({ state: 'failed', exit_code: 1 })), '')
    assert.equal(runHint(view({ state: 'failed', exit_code: 1, silent: true })), '', '失败的运行不提空转')
  })

  it('补充说明：被取消说清原因并告诉你对话没有结束；没有原因（旧记录）不编造', () => {
    assert.match(
      runHint(view({ state: 'cancelled', why: '终端总闸关闭（服务端的终端功能被整体停用），进行中的运行被一并取消' })),
      /^被取消：终端总闸关闭.*被一并取消。对话本身没有结束，可以再点「运行」接着聊。$/,
    )
    assert.equal(runHint(view({ state: 'cancelled' })), '')
    // 没跑起来的失败 / 被拒绝：原因就是解释；有退出码的失败仍然什么都不加。
    assert.equal(runHint(view({ state: 'failed', why: '无法连接到 SSH 主机' })), '无法连接到 SSH 主机')
    assert.equal(runHint(view({ state: 'rejected', why: '被你拒绝' })), '被你拒绝')
    assert.equal(runHint(view({ state: 'failed', exit_code: 2, why: '不该出现' })), '')
    assert.equal(runHint(view({ why: '成功的运行没有原因' })), '')
  })
})

describe('非交互续聊：能不能运行', () => {
  it('主机的原因优先：缺失 / 离线 / 未启用 / SSH', () => {
    assert.deepEqual(runReadiness(host(), undefined), { ok: true })
    assert.deepEqual(runReadiness(undefined, undefined), { ok: false, text: '主机已不在列表里（可能被删除了）' })
    assert.deepEqual(runReadiness(host({ online: false }), undefined), { ok: false, text: '主机离线' })
    assert.deepEqual(runReadiness(host({ status: 'disabled' }), undefined), { ok: false, text: '主机未启用' })
    assert.deepEqual(runReadiness(host({ kind: 'ssh' } as Partial<HostView>), undefined), { ok: false, text: '暂不支持 SSH 主机' })
  })

  it('Windows 主机暂不支持；上一次还在跑不能再点', () => {
    assert.deepEqual(runReadiness(host({ os: 'windows' }), undefined), { ok: false, text: '暂不支持 Windows 主机' })
    assert.deepEqual(runReadiness(host(), view({ state: 'running', active: true })), { ok: false, text: '上一次运行还没结束' })
    assert.deepEqual(runReadiness(host(), view({ state: 'awaiting_approval', active: true })), { ok: false, text: '上一次运行还没结束' })
    assert.deepEqual(runReadiness(host(), view()), { ok: true }, '上一次已经结束：可以再运行')
  })
})

describe('非交互续聊：清单与提示', () => {
  const l = (run: TerminalLauncher['run']): Pick<TerminalLauncher, 'run'> => ({ run })

  it('一句话说清会续哪个对话、用什么模型、什么权限姿态', () => {
    assert.equal(runSummary(l({ workflow_id: 'wf-1' })), '工作流 wf-1 里最近的对话 · 只放行白名单内的操作')
    assert.equal(
      runSummary(l({ conversation_id: 'e33e27ef-0fbb-46e4-9446-5dee56ae61fa', workflow_id: 'wf-1', model: 'claude-sonnet-5-5-max', posture: 'force' })),
      '固定对话 e33e27ef · claude-sonnet-5-5-max · 放开全部命令（--force）',
    )
    assert.equal(runSummary({}), '')
    // 设了最长运行时间才写；没设（默认 7 天）不占位置。
    assert.equal(runSummary(l({ workflow_id: 'wf-1', max_run_minutes: 30 })), '工作流 wf-1 里最近的对话 · 只放行白名单内的操作 · 最长运行 30 分钟')
    assert.equal(runSummary(l({ workflow_id: 'wf-1', posture: 'force', max_run_minutes: 120 })), '工作流 wf-1 里最近的对话 · 放开全部命令（--force） · 最长运行 2 小时')
    assert.equal(runSummary(l({ workflow_id: 'wf-1', max_run_minutes: 1440 })), '工作流 wf-1 里最近的对话 · 只放行白名单内的操作 · 最长运行 1 天')
    assert.doesNotMatch(runSummary(l({ workflow_id: 'wf-1', max_run_minutes: 0 })), /最长运行/)
  })

  it('第一次运行时新开对话的预设：写明「第一次新开，之后接着」；固定了对话编号时以固定的为准', () => {
    assert.equal(
      runSummary(l({ workflow_id: 'wf-1', new_conversation: true, posture: 'force', max_run_minutes: 1440 })),
      '第一次新开对话，之后接着工作流 wf-1 里的对话 · 放开全部命令（--force） · 最长运行 1 天',
    )
    assert.equal(runSummary(l({ workflow_id: 'wf-1', conversation_id: 'chat-12345678', new_conversation: true })), '固定对话 chat-123 · 只放行白名单内的操作')
  })

  it('点「运行」之后的提示：新开对话的运行说明它只新开这一次', () => {
    const n = runNotice({ name: '启用 Agent · x', host: 'h', run_id: 'r1', run_state: 'running', posture: 'force', conversation_id: '', new_conversation: true })
    assert.equal(n.title, '已开始运行「启用 Agent · x」')
    assert.match(n.description, /新开了一个 Cursor CLI 对话/)
    assert.match(n.description, /不会再新开/)
    assert.doesNotMatch(n.description, /接着原对话/)
  })

  it('点「运行」之后的提示：已开始 / 等批准，并带上服务端的前提提醒', () => {
    const plan = (over: Partial<LauncherRunPlan> = {}): LauncherRunPlan => ({
      name: '续聊', host: 'h', run_id: 'r1', run_state: 'running', posture: 'allowlist', conversation_id: 'chat-9', ...over,
    })
    const a = runNotice(plan())
    assert.equal(a.title, '已开始运行「续聊」')
    assert.match(a.description, /不打开终端/)
    const b = runNotice(plan({ run_state: 'awaiting_approval', warnings: ['白名单姿态：需要先放行 Mcp(...)'] }))
    assert.equal(b.title, '「续聊」等待批准')
    assert.match(b.description, /需批准/)
    assert.match(b.description, /白名单姿态/)
    // 预设设了时限才提醒（默认的 7 天不用说），并且写在前提提醒之前。
    const c = runNotice(plan({ max_run_seconds: 1800, warnings: ['白名单姿态：需要先放行 Mcp(...)'] }))
    assert.match(c.description, /这次运行最长 30 分钟，到点会被强制结束。\n白名单姿态/)
    assert.doesNotMatch(runNotice(plan({ max_run_seconds: DEFAULT_RUN_LIMIT_SEC })).description, /最长/)
    assert.doesNotMatch(runNotice(plan()).description, /最长/, '旧版 Hub 没有这个字段')
  })
})

describe('非交互续聊：轮询节奏', () => {
  it('有运行在进行时勤一点，否则慢一点', () => {
    assert.equal(runsActive(undefined), false)
    assert.equal(runsActive(null), false)
    assert.equal(runsActive({}), false)
    assert.equal(runsActive({ a: view() }), false)
    assert.equal(runsActive({ a: view(), b: view({ state: 'running', active: true }) }), true)
    assert.equal(runPollDelay({ a: view({ state: 'running', active: true }) }), RUN_POLL_ACTIVE_MS)
    assert.equal(runPollDelay({ a: view() }), RUN_POLL_IDLE_MS)
    assert.equal(runPollDelay(null), RUN_POLL_IDLE_MS)
    assert.ok(RUN_POLL_ACTIVE_MS < RUN_POLL_IDLE_MS)
  })

  it('不属于任何预设的运行在进行时也算：没有预设、只有它在跑也要勤一点读（#147）', () => {
    const running = view({ state: 'running', active: true })
    assert.equal(runsActive({}, [running]), true)
    assert.equal(runsActive(null, [running]), true)
    assert.equal(runsActive({}, [view()]), false)
    assert.equal(runsActive({}, []), false)
    assert.equal(runsActive({}, undefined), false)
    assert.equal(runPollDelay({}, [running]), RUN_POLL_ACTIVE_MS)
    assert.equal(runPollDelay({}, [view()]), RUN_POLL_IDLE_MS)
    assert.equal(runPollDelay({ a: view() }, null), RUN_POLL_IDLE_MS)
  })
})

describe('其他非交互运行（#147）', () => {
  it('来源给口语名与一句解释；不认识的来源按通用说法，不报错', () => {
    assert.equal(otherRunSource('session_run').label, '会话页「运行」')
    assert.equal(otherRunSource('wake').label, '非交互唤醒')
    assert.equal(otherRunSource('deleted_preset').label, '预设已删除')
    for (const s of ['session_run', 'wake', 'deleted_preset']) assert.ok(otherRunSource(s).hint.length > 10, s)
    assert.deepEqual(otherRunSource('something_new'), { label: '非交互运行', hint: '' })
  })

  it('概况：进行中与已结束各几个，没有的不写', () => {
    const run = view({ state: 'running', active: true })
    assert.equal(othersSummary([run, run, view()]), '2 个运行中 · 1 个已结束')
    assert.equal(othersSummary([run]), '1 个运行中')
    assert.equal(othersSummary([view(), view()]), '2 个已结束')
    assert.equal(othersSummary([]), '')
  })

  it('只有还没结束的（含待批准）可以取消', () => {
    assert.equal(otherRunCancellable(view({ state: 'running', active: true })), true)
    assert.equal(otherRunCancellable(view({ state: 'awaiting_approval', active: true })), true)
    for (const state of ['succeeded', 'failed', 'timeout', 'cancelled', 'rejected', 'denied', 'lost']) {
      assert.equal(otherRunCancellable(view({ state, active: false })), false, state)
    }
  })

  it('补充说明：被取消 / 用满时限不再引导去点「运行」或改预设（这些运行没有预设）', () => {
    const cancelled = otherRunHint(view({ state: 'cancelled', why: '你在 ExecMesh 里取消了它' }))
    assert.match(cancelled, /被取消：你在 ExecMesh 里取消了它/)
    assert.doesNotMatch(cancelled, /「运行」/)
    assert.equal(otherRunHint(view({ state: 'cancelled' })), '', '没有原因（旧记录）不编造')
    const timeout = otherRunHint(view({ state: 'timeout', timeout_sec: 1800 }))
    assert.match(timeout, /用满了设定的最长运行时间（30 分钟）/)
    assert.doesNotMatch(timeout, /预设/)
    assert.doesNotMatch(otherRunHint(view({ state: 'timeout' })), /（/, '旧记录没有时限就不写数字')
  })

  it('异常退出（#151）：「其他非交互运行」里把 Cursor CLI 报的原因说出来，认不出的不说，不指向预设里的开关', () => {
    const f = { class: 'retriable', code: 'resource_exhausted', text: 'Cursor CLI 报了「可重试」的错误：服务端限流（resource_exhausted）', retry: true }
    const hint = otherRunHint(view({ state: 'failed', exit_code: 1, failure: f }))
    assert.equal(hint, f.text)
    assert.doesNotMatch(hint, /预设|自动续跑|「运行」/)
    assert.equal(otherRunHint(view({ state: 'failed', exit_code: 1, failure: { class: 'unknown', text: '退出码 1，认不出原因' } })), '')
    assert.equal(otherRunHint(view({ state: 'failed', exit_code: 1 })), '', '旧版 Hub 没有 failure：不编造')
    assert.equal(otherRunHint(view({ state: 'failed', exit_code: undefined, why: '主机认为请求参数无效', failure: f })), f.text, '有失败说明就用它')
    assert.equal(otherRunHint(view({ state: 'succeeded', failure: f })), '', '成功的运行不显示失败说明')
  })

  it('其余状态沿用预设里的补充说明：待批准、空转、状态丢失、没跑起来的原因；正常结束没有', () => {
    assert.equal(otherRunHint(view({ state: 'awaiting_approval', active: true })), runHint(view({ state: 'awaiting_approval', active: true })))
    assert.match(otherRunHint(view({ silent: true })), /没有向 RelayMesh 发出任何消息/)
    assert.match(otherRunHint(view({ state: 'lost' })), /主机重启或掉线/)
    assert.equal(otherRunHint(view({ state: 'failed', exit_code: undefined, why: '主机认为请求参数无效' })), '主机认为请求参数无效')
    assert.equal(otherRunHint(view()), '')
  })
})

describe('异常退出的说明与自动续跑（#151）', () => {
  const failure = (cls: string, over: Partial<NonNullable<LauncherRunView['failure']>> = {}): NonNullable<LauncherRunView['failure']> => ({
    class: cls, text: `Cursor CLI 报了「${cls}」的错误`, retry: cls === 'retriable', ...over,
  })
  const failed = (over: Partial<LauncherRunView> = {}): LauncherRunView => view({ state: 'failed', exit_code: 1, ...over })
  const auto = (over: Partial<NonNullable<LauncherRunView['auto']>> = {}): NonNullable<LauncherRunView['auto']> => ({ enabled: true, max: 3, ...over })

  it('清单摘要：开着自动续跑才写，关着（默认）不占位置', () => {
    const base: Pick<TerminalLauncher, 'run'> = { run: { posture: 'allowlist', workflow_id: 'wf-1' } }
    assert.doesNotMatch(runSummary(base), /自动续跑/)
    assert.doesNotMatch(runSummary({ run: { ...base.run!, auto_resume: false } }), /自动续跑/)
    assert.match(runSummary({ run: { ...base.run!, auto_resume: true } }), /只放行白名单内的操作 · 异常退出自动续跑$/)
    assert.match(runSummary({ run: { ...base.run!, auto_resume: true, max_run_minutes: 30 } }), /最长运行 30 分钟 · 异常退出自动续跑$/)
  })

  it('清单摘要：判定时长是自定义的才写，默认值不占位置；没开自动续跑时不写（#172）', () => {
    const base: Pick<TerminalLauncher, 'run'> = { run: { posture: 'allowlist', workflow_id: 'wf-1', auto_resume: true } }
    assert.match(runSummary({ run: { ...base.run!, auto_resume_ok_minutes: 30 } }), /异常退出自动续跑（跑满 30 分钟算成功）$/)
    assert.match(runSummary({ run: { ...base.run!, auto_resume_ok_minutes: 60 } }), /（跑满 1 小时算成功）$/)
    assert.match(runSummary({ run: { ...base.run!, auto_resume_ok_minutes: 1440 } }), /（跑满 1 天算成功）$/)
    assert.doesNotMatch(runSummary({ run: { ...base.run!, auto_resume_ok_minutes: 0 } }), /算成功/, '0 = 没设')
    assert.doesNotMatch(runSummary(base), /算成功/)
    assert.doesNotMatch(runSummary({ run: { ...base.run!, auto_resume: false, auto_resume_ok_minutes: 30 } }), /自动续跑|算成功/)
  })

  it('续跑成功（#172）：状态行标「已成功」，说明行写清判定时长与次数已清零；排期、放弃等更要紧的说明优先', () => {
    const ok = (over: Partial<NonNullable<LauncherRunView['auto']>> = {}) => auto({ attempt: 2, settled: true, ok_minutes: 10, ...over })
    assert.match(runMeta(view({ state: 'running', active: true, auto: ok() }), NOW), /^自动续跑 2\/3（已成功） · /)
    assert.match(runMeta(view({ auto: auto({ attempt: 2 }) }), NOW), /^自动续跑 2\/3 · /, '还没认定成功的不带')
    assert.deepEqual(autoResumeLine(view({ state: 'running', active: true, auto: ok() }), NOW), {
      text: '续跑成功：这次续跑连续跑满了 10 分钟，24 小时内累计的续跑次数已清零', tone: 'good', cancellable: false,
    })
    assert.match(autoResumeLine(view({ auto: ok({ ok_minutes: 90 }) }), NOW)?.text ?? '', /连续跑满了 90 分钟/)
    assert.match(autoResumeLine(view({ auto: ok({ ok_minutes: undefined }) }), NOW)?.text ?? '', /已经稳定运行/, '旧版 Hub 没写判定时长：不编造数字')
    assert.equal(autoResumeLine(view({ auto: auto({ attempt: 2 }) }), NOW), null, '没认定成功、也没有安排：不说话')
    // 认定成功之后它又失败了：下一步怎么安排才是要紧的，成功那句不抢位置
    assert.equal(autoResumeLine(failed({ failure: failure('retriable'), auto: ok({ state: 'pending', next_attempt: 1, due: iso(300) }) }), NOW)?.text, '将在 5 分钟后自动续跑（第 1/3 次）')
    assert.match(autoResumeLine(failed({ failure: failure('action_required'), auto: ok() }), NOW)?.text ?? '', /没有自动续跑：这类失败重试没有用/)
  })

  it('倒计时的口语写法：到了或不到半分钟写「马上」；时间不合法是空串', () => {
    assert.equal(untilText(iso(300), NOW), '5 分钟后')
    assert.equal(untilText(iso(250), NOW), '5 分钟后', '分钟向上取整：还有 4 分多钟不说「4 分钟后」')
    assert.equal(untilText(iso(1200), NOW), '20 分钟后')
    assert.equal(untilText(iso(3600), NOW), '1 小时后')
    assert.equal(untilText(iso(3900), NOW), '1 小时 5 分钟后')
    assert.equal(untilText(iso(45), NOW), '45 秒后')
    assert.equal(untilText(iso(89), NOW), '89 秒后')
    assert.equal(untilText(iso(90), NOW), '2 分钟后')
    assert.equal(untilText(iso(30), NOW), '30 秒后')
    assert.equal(untilText(iso(29), NOW), '马上')
    assert.equal(untilText(iso(20), NOW), '马上')
    assert.equal(untilText(iso(0), NOW), '马上')
    assert.equal(untilText(iso(-90), NOW), '马上', '已经过了（等下一次检查）也是马上，不写负数')
    assert.equal(untilText(undefined, NOW), '')
    assert.equal(untilText('不是时间', NOW), '')
  })

  it('没有失败说明的运行（成功、取消、旧版 Hub）没有失败行；认不出原因的也不写，退出码已经在状态里', () => {
    assert.equal(runFailureLine(view()), null)
    assert.equal(runFailureLine(view({ state: 'cancelled' })), null)
    assert.equal(runFailureLine(failed({ failure: failure('unknown') })), null)
    assert.equal(runFailureLine(failed({ failure: { class: 'retriable', text: '' } })), null, '没有文字就没有可显示的')
  })

  it('失败行 = 服务端的一句话 + 你可以怎么办；每一类的建议不同', () => {
    const retriable = runFailureLine(failed({ failure: failure('retriable', { text: '服务端限流（resource_exhausted）' }) }))
    assert.equal(retriable?.text, '服务端限流（resource_exhausted）')
    assert.match(retriable?.hint ?? '', /再点「运行」/)
    assert.match(retriable?.hint ?? '', /异常退出时自动续跑/, '没开自动续跑时指给它')
    assert.match(runFailureLine(failed({ failure: failure('action_required') }))?.hint ?? '', /需要你处理.*重试没有用/)
    assert.match(runFailureLine(failed({ failure: failure('non_retriable') }))?.hint ?? '', /重试没有用/)
    assert.match(runFailureLine(failed({ failure: failure('crashed', { retry: true }) }))?.hint ?? '', /崩溃/)
    assert.equal(runFailureLine(failed({ failure: failure('crashed', { retry: false }) }))?.hint, '', '不重试的崩溃不乱建议')
    assert.equal(runFailureLine(failed({ failure: failure('something-new') }))?.hint, '', '不认识的类别：只显示服务端的话，不编造建议')
  })

  it('开着自动续跑时，可重试的失败不再叫你手动点「运行」（接下来怎样由自动续跑那一行说明）', () => {
    const v = failed({ failure: failure('retriable'), auto: auto() })
    assert.equal(runFailureLine(v)?.hint, '')
    assert.match(runFailureLine({ ...v, failure: failure('action_required') })?.hint ?? '', /需要你处理/, '要你处理的照常提醒')
    assert.equal(runFailureLine(failed({ failure: failure('crashed', { retry: true }), auto: auto() }))?.hint, '')
  })

  it('排期中：写出多久以后与第几次，可以取消；到点前后措辞不同', () => {
    const v = failed({ failure: failure('retriable'), auto: auto({ state: 'pending', next_attempt: 1, due: iso(300) }) })
    assert.deepEqual(autoResumeLine(v, NOW), { text: '将在 5 分钟后自动续跑（第 1/3 次）', tone: 'warn', cancellable: true })
    assert.deepEqual(
      autoResumeLine(failed({ auto: auto({ state: 'pending', next_attempt: 2, due: iso(5) }) }), NOW),
      { text: '即将自动续跑（第 2/3 次）', tone: 'warn', cancellable: true },
    )
    assert.equal(autoResumeLine(failed({ auto: auto({ state: 'pending', due: iso(300) }) }), NOW)?.text, '将在 5 分钟后自动续跑', '没有次数就不写次数')
  })

  it('其余安排：已续跑 / 你取消了 / 作废 / 放弃，只有排期中的可以取消', () => {
    const line = (a: NonNullable<LauncherRunView['auto']>) => autoResumeLine(failed({ auto: a }), NOW)
    assert.deepEqual(line(auto({ state: 'started', next_attempt: 1 })), { text: '已自动续跑（第 1/3 次），最新一次运行在上面', tone: 'neutral', cancellable: false })
    assert.deepEqual(line(auto({ state: 'stopped' })), { text: '你取消了这次自动续跑', tone: 'neutral', cancellable: false })
    assert.deepEqual(line(auto({ state: 'skipped', why: '对话或上一次运行还在进行（有人接手了），不去打架' })), {
      text: '没有自动续跑：对话或上一次运行还在进行（有人接手了），不去打架', tone: 'neutral', cancellable: false,
    })
    const gave = line(auto({ state: 'gaveup', why: '24 小时内已经自动续跑了 3 次' }))
    assert.equal(gave?.tone, 'bad')
    assert.equal(gave?.cancellable, false)
    assert.match(gave?.text ?? '', /不再自动续跑：24 小时内已经自动续跑了 3 次。请你看看原因再手动运行/)
    assert.equal(line(auto({ state: 'skipped' }))?.text, '没有自动续跑：条件变了', '没有原因也不留空')
    assert.equal(line(auto({ state: 'something-new' })), null, '不认识的状态：不显示，也不当作可取消')
  })

  it('开着自动续跑、失败又是重试没有用的：说明为什么没有续跑；其余没有安排的不写', () => {
    assert.equal(autoResumeLine(failed({ failure: failure('action_required'), auto: auto() }), NOW)?.text, '没有自动续跑：这类失败重试没有用')
    assert.equal(autoResumeLine(failed({ failure: failure('non_retriable'), auto: auto() }), NOW)?.cancellable, false)
    assert.equal(autoResumeLine(failed({ failure: failure('retriable'), auto: auto() }), NOW), null, '可重试但还没登记（刚结束）：不说话')
    assert.equal(autoResumeLine(failed({ failure: failure('unknown'), auto: auto() }), NOW), null)
    assert.equal(autoResumeLine(failed({ failure: failure('action_required'), auto: auto({ enabled: false }) }), NOW), null, '没开自动续跑就不解释它')
    assert.equal(autoResumeLine(view({ auto: auto() }), NOW), null, '成功的运行没有自动续跑可说')
    assert.equal(autoResumeLine(failed({ failure: failure('retriable') }), NOW), null, '旧版 Hub 没有 auto 字段')
  })

  it('自动续跑出来的运行：状态行里标出是第几次；手动运行的不标', () => {
    assert.match(runMeta(view({ auto: auto({ attempt: 2 }) }), NOW), /^自动续跑 2\/3 · /)
    assert.match(runMeta(view({ state: 'running', active: true, auto: auto({ attempt: 1 }) }), NOW), /^自动续跑 1\/3 · /)
    assert.doesNotMatch(runMeta(view({ auto: auto() }), NOW), /自动续跑/)
    assert.doesNotMatch(runMeta(view({ auto: auto({ attempt: 0 }) }), NOW), /自动续跑/)
    assert.doesNotMatch(runMeta(view(), NOW), /自动续跑/)
  })
})
