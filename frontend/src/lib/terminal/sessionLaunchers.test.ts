// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { LauncherOtherRun, SessionLauncherSuggest, SessionLauncherView, SessionLaunchersView, TerminalLauncher } from '../../api/terminal'
import { RUN_POLL_ACTIVE_MS } from './launcherRun'
import {
  firstReason,
  inProject,
  normDir,
  othersInView,
  projectName,
  projectScope,
  quickRun,
  resolveTab,
  runFacts,
  SESSION_LAUNCHERS_IDLE_MS,
  sessionLaunchersPollDelay,
  suggestDraft,
  suggestLauncher,
  suggestSummary,
  tabCounts,
} from './sessionLaunchers'

const preset = (over: Partial<SessionLauncherView> = {}): SessionLauncherView => ({
  id: 'p1', name: '续聊', host: 'RM-AAAA-BBBB-CCCC-DDDD', host_title: 'build box', dir: '/work/app', conversation_id: 'e33e27ef-0fbb-46e4',
  posture: 'allowlist', max_run_seconds: 7 * 86400, command: 'agent -p --resume e33e27ef', can_run: true, busy: false, last_run: null, ...over,
})

const view = (presets: SessionLauncherView[], over: Partial<SessionLaunchersView> = {}): SessionLaunchersView => ({
  workflow_id: 'wf', hosts: [], presets, ...over,
})

/** 只比较 want 里列出的字段（node:assert 在 Node 20 里没有 partialDeepStrictEqual）。 */
function subset(actual: object, want: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const k of Object.keys(want)) out[k] = (actual as Record<string, unknown>)[k]
  return out
}

describe('quickRun：底部「运行」按钮', () => {
  it('没有视图 / 没有对应预设：不出现', () => {
    assert.deepEqual(quickRun(null, 'idle'), { mode: 'hidden' })
    assert.deepEqual(quickRun(undefined, 'idle'), { mode: 'hidden' })
    assert.deepEqual(quickRun(view([]), 'idle'), { mode: 'hidden' })
  })

  it('对话还在运行（智能体状态工作中 / 等你回答 / 等批准）：不出现，即使服务端的视图还没跟上', () => {
    for (const key of ['working', 'waiting_you', 'waiting_approval']) {
      assert.deepEqual(quickRun(view([preset()]), key), { mode: 'hidden' }, key)
    }
  })

  it('其余状态（空闲 / 结束 / 出错 / 卡住 / 离线 / 未知）都放行：续聊正是这些时候需要的', () => {
    for (const key of ['idle', 'ended', 'aborted', 'error', 'stalled', 'offline', '']) {
      assert.equal(quickRun(view([preset()]), key).mode, 'ready', key)
    }
  })

  it('每条预设都「忙」（对话在运行 / 上一次运行没结束）：不出现，而不是灰掉', () => {
    const busy = preset({ can_run: false, busy: true, issues: [{ code: 'run_active', message: '上一次运行还没结束' }] })
    assert.deepEqual(quickRun(view([busy]), 'idle'), { mode: 'hidden' })
  })

  it('可运行的预设进可选项；忙的与灰掉的不进', () => {
    const ok1 = preset({ id: 'a' })
    const ok2 = preset({ id: 'b' })
    const busy = preset({ id: 'c', can_run: false, busy: true })
    const off = preset({ id: 'd', can_run: false, issues: [{ code: 'host_offline', message: '主机离线，现在没法运行' }] })
    const q = quickRun(view([ok1, busy, off, ok2]), 'idle')
    assert.equal(q.mode, 'ready')
    assert.deepEqual(q.mode === 'ready' ? q.presets.map(p => p.id) : [], ['a', 'b'])
  })

  it('只有不能运行的原因（主机离线…）：灰掉并写明第一个原因', () => {
    const off = preset({ can_run: false, issues: [{ code: 'host_offline', message: '主机离线，现在没法运行' }, { code: 'x', message: '别的' }] })
    assert.deepEqual(quickRun(view([off]), 'idle'), { mode: 'blocked', reason: '主机离线，现在没法运行', preset: off })
    // 忙的那条不拿来当原因：它在说另一件事
    const busy = preset({ id: 'b', can_run: false, busy: true, issues: [{ code: 'run_active', message: '上一次运行还没结束' }] })
    assert.deepEqual(quickRun(view([busy, off]), 'idle'), { mode: 'blocked', reason: '主机离线，现在没法运行', preset: off })
  })

  it('不能运行却没有说明原因：给一句通用的话', () => {
    assert.equal(firstReason(preset({ can_run: false, issues: [] })), '现在不能运行')
    assert.equal(firstReason(preset({ can_run: false, issues: undefined })), '现在不能运行')
  })
})

describe('runFacts：确认窗里的几行', () => {
  it('主机名 · 目录、对话前 8 位、默认模型与白名单姿态（默认 7 天的时限不写）', () => {
    assert.deepEqual(runFacts(preset()), {
      target: 'build box · /work/app',
      conversation: 'e33e27ef…',
      options: '默认模型 · 只放行白名单内的操作',
    })
  })

  it('设了模型 / force / 时限：都写出来', () => {
    const f = runFacts(preset({ model: 'claude-sonnet-5-5-max', posture: 'force', max_run_seconds: 1800 }))
    assert.equal(f.options, 'claude-sonnet-5-5-max · 放开全部命令（--force） · 最长运行 30 分钟')
  })

  it('没有主机名用机器码；还不知道对话编号要说明', () => {
    const f = runFacts(preset({ host_title: undefined, conversation_id: undefined }))
    assert.equal(f.target, 'RM-AAAA-…-DDDD · /work/app')
    assert.equal(f.conversation, '还不知道')
  })

  it('对话来源（#191）：新会话 / 最近会话没有可接的对话编号，也不能写成「还不知道」', () => {
    const fresh = runFacts(preset({ mode: 'new', conversation_id: undefined }))
    assert.match(fresh.conversation, /新开一个对话/)
    assert.doesNotMatch(fresh.conversation, /还不知道/)
    const recent = runFacts(preset({ mode: 'continue', conversation_id: undefined }))
    assert.match(recent.conversation, /目录里最近动过的对话/)
    assert.match(recent.conversation, /由 Cursor CLI 决定/)
    assert.doesNotMatch(recent.conversation, /还不知道/)
    // 指定会话（含旧版 Hub 没有 mode 字段）照旧
    assert.equal(runFacts(preset({ mode: 'resume' })).conversation, 'e33e27ef…')
    assert.equal(runFacts(preset({ mode: undefined })).conversation, 'e33e27ef…')
  })
})

describe('项目范围：启动器的「本项目 / 全部」', () => {
  const l = (dir: string | undefined, host = 'H1'): TerminalLauncher => ({ id: 'x', name: 'n', host, dir })

  it('规整目录：重复斜杠、末尾斜杠；根目录保持', () => {
    assert.equal(normDir('/work//app/'), '/work/app')
    assert.equal(normDir('  /work/app  '), '/work/app')
    assert.equal(normDir('/'), '/')
    assert.equal(normDir(undefined), '')
  })

  it('没有视图就没有范围（启动器不分标签）', () => {
    assert.equal(projectScope(null), null)
    assert.equal(projectScope(undefined), null)
    assert.deepEqual(projectScope(view([], { dir: '/work/app/', hosts: ['H1'] })), { dir: '/work/app', hosts: ['H1'] })
    assert.deepEqual(projectScope(view([])), { dir: '', hosts: [] })
  })

  it('项目名取目录最后一段', () => {
    assert.equal(projectName({ dir: '/home/user/RelayMesh', hosts: [] }), 'RelayMesh')
    assert.equal(projectName({ dir: '', hosts: [] }), '')
    assert.equal(projectName(null), '')
  })

  it('目录相同或在项目目录下才算本项目；前缀相同的兄弟目录不算', () => {
    const scope = { dir: '/work/app', hosts: [] as string[] }
    assert.equal(inProject(l('/work/app'), scope), true)
    assert.equal(inProject(l('/work/app/'), scope), true)
    assert.equal(inProject(l('/work/app/frontend'), scope), true)
    assert.equal(inProject(l('/work/app-other'), scope), false)
    assert.equal(inProject(l('/work'), scope), false)
    assert.equal(inProject(l(undefined), scope), false)
    assert.equal(inProject(l('~/app'), scope), false)
  })

  it('会话所在主机能对上时，别的主机上的同名目录不算；对不上（hosts 为空）就不按主机筛', () => {
    assert.equal(inProject(l('/work/app', 'H1'), { dir: '/work/app', hosts: ['H1'] }), true)
    assert.equal(inProject(l('/work/app', 'H2'), { dir: '/work/app', hosts: ['H1'] }), false)
    assert.equal(inProject(l('/work/app', 'H2'), { dir: '/work/app', hosts: ['H1', 'H2'] }), true)
    assert.equal(inProject(l('/work/app', 'H2'), { dir: '/work/app', hosts: [] }), true)
  })

  it('不知道项目目录：什么都不算本项目', () => {
    assert.equal(inProject(l('/work/app'), { dir: '', hosts: [] }), false)
    assert.equal(inProject(l('/work/app'), null), false)
  })

  it('计数与默认标签：本项目有预设或有建议就默认本项目，否则全部；用户选过就听用户的', () => {
    const scope = { dir: '/work/app', hosts: [] as string[] }
    const list = [l('/work/app'), l('/work/other'), l('/work/app/sub')]
    assert.deepEqual(tabCounts(list, scope), { project: 2, all: 3 })
    assert.deepEqual(tabCounts(list, null), { project: 0, all: 3 })
    assert.equal(resolveTab('auto', { project: 2 }, false), 'project')
    assert.equal(resolveTab('auto', { project: 0 }, true), 'project')
    assert.equal(resolveTab('auto', { project: 0 }, false), 'all')
    assert.equal(resolveTab('all', { project: 2 }, true), 'all')
    assert.equal(resolveTab('project', { project: 0 }, false), 'project')
  })
})

describe('其他非交互运行：按标签筛（#147）', () => {
  const other = (id: string, dir: string | undefined, host = 'H1'): LauncherOtherRun => ({
    run_id: id, state: 'running', active: true, created_at: '2026-10-16T08:00:00Z', source: 'session_run', host, dir,
  })
  const list = [other('a', '/work/app'), other('b', '/work/app/sub'), other('c', '/work/other'), other('d', undefined), other('e', '/work/app', 'H2')]

  it('「本项目」标签与预设同一个口径：目录在项目目录里；会话所在主机能对上时主机也要一致', () => {
    const ids = (xs: LauncherOtherRun[]) => xs.map(o => o.run_id)
    assert.deepEqual(ids(othersInView(list, { dir: '/work/app', hosts: [] }, true)), ['a', 'b', 'e'])
    assert.deepEqual(ids(othersInView(list, { dir: '/work/app', hosts: ['H1'] }, true)), ['a', 'b'])
  })

  it('「全部」或不分标签：全部都看得到；不知道项目目录时「本项目」里什么都没有', () => {
    assert.equal(othersInView(list, { dir: '/work/app', hosts: [] }, false).length, 5)
    assert.equal(othersInView(list, null, false).length, 5)
    assert.equal(othersInView(list, { dir: '', hosts: [] }, true).length, 0)
    assert.equal(othersInView(list, null, true).length, 0)
  })

  it('旧版 Hub 没有这个字段：当作没有，也不改动原数组', () => {
    assert.deepEqual(othersInView(undefined, { dir: '/work/app', hosts: [] }, true), [])
    assert.deepEqual(othersInView(null, null, false), [])
    const copy = othersInView(list, null, false)
    copy.pop()
    assert.equal(list.length, 5)
  })
})

describe('建议：为这个会话创建续聊预设', () => {
  const suggest = (over: Partial<SessionLauncherSuggest> = {}): SessionLauncherSuggest => ({
    name: '续聊 · app', host: 'RM-AAAA-BBBB-CCCC-DDDD', host_title: 'build box', dir: '/work/app', workflow_id: 'wf-1', posture: 'allowlist', ...over,
  })

  it('草稿：名字、目录、工作流与姿态预填，主机默认取建议里的', () => {
    const d = suggestDraft(suggest())
    assert.deepEqual(subset(d, { name: 1, host: 1, dir: 1 }), { name: '续聊 · app', host: 'RM-AAAA-BBBB-CCCC-DDDD', dir: '/work/app' })
    assert.deepEqual(subset(d.run ?? {}, { workflow: 1, posture: 1, conversation: 1, prompt: 1 }), { workflow: 'wf-1', posture: 'allowlist', conversation: '', prompt: '' })
    assert.match(d.id, /^l-[0-9a-f]{16}$/)
    // 主机待选：留空，由人选
    assert.equal(suggestDraft(suggest({ host: undefined, needs_host: true })).host, '')
    // 主机由调用方指定（人选了一台）
    assert.equal(suggestDraft(suggest({ host: undefined, needs_host: true }), 'RM-1111-2222-3333-4444').host, 'RM-1111-2222-3333-4444')
  })

  it('一键创建的预设：非交互续聊，只带工作流、姿态与对话来源（指定会话），服务端用默认的可执行文件 / 模型 / 提示词 / 时限', () => {
    const l = suggestLauncher(suggest())
    assert.deepEqual(subset(l, { name: 1, host: 1, dir: 1, kind: 1 }), { name: '续聊 · app', host: 'RM-AAAA-BBBB-CCCC-DDDD', dir: '/work/app', kind: 'headless' })
    assert.deepEqual(l.run, { posture: 'allowlist', source: 'resume', workflow_id: 'wf-1' })
    assert.equal(l.command, undefined)
    assert.equal(suggestLauncher(suggest({ posture: 'force' })).run?.posture, 'force')
  })

  it('一句话摘要：说明在哪里、接着什么、什么权限', () => {
    assert.equal(suggestSummary(suggest()), 'build box · /work/app · 接着工作流 wf-1 里最近的对话 · 只放行白名单内的操作')
    assert.equal(
      suggestSummary(suggest({ host: undefined, host_title: undefined, needs_host: true, posture: 'force' })),
      '/work/app（主机待选） · 接着工作流 wf-1 里最近的对话 · 放开全部命令',
    )
  })
})

describe('刷新节奏', () => {
  it('有进行中的运行时勤一点，否则慢一点', () => {
    const active = preset({ last_run: { run_id: 'r1', state: 'running', active: true, created_at: '2026-10-16T00:00:00Z' } })
    const done = preset({ last_run: { run_id: 'r1', state: 'succeeded', active: false, created_at: '2026-10-16T00:00:00Z' } })
    assert.equal(sessionLaunchersPollDelay(view([active])), RUN_POLL_ACTIVE_MS)
    assert.equal(sessionLaunchersPollDelay(view([done])), SESSION_LAUNCHERS_IDLE_MS)
    assert.equal(sessionLaunchersPollDelay(view([])), SESSION_LAUNCHERS_IDLE_MS)
    assert.equal(sessionLaunchersPollDelay(null), SESSION_LAUNCHERS_IDLE_MS)
  })
})
