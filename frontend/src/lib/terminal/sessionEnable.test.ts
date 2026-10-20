// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { SessionEnableHost, SessionEnablePreview } from '../../api/terminal'
import { LAUNCHER_DEFAULT_LIMITS, launcherLimits, type LauncherLimits } from './launcher'
import {
  ENABLE_DEFAULT_MAX_RUN,
  ENABLE_DEFAULT_POSTURE,
  ENABLE_MEMORY_KEY,
  blankEnableDraft,
  enableHostChoices,
  enableNotice,
  enableSummary,
  enableSupported,
  hasEnableErrors,
  loadEnableMemory,
  pickEnableDir,
  pickEnableHost,
  postureWarning,
  previousRunNote,
  saveEnableMemory,
  toEnableRequest,
  validateEnableDraft,
} from './sessionEnable'

const LIMITS: LauncherLimits = { ...LAUNCHER_DEFAULT_LIMITS, runSupported: true, runNewPromptDefault: '新开对话提示词 %workflow', runMaxMinutes: 7 * 24 * 60 }
const WF = 'wf-web-20261019-abc123'

const host = (over: Partial<SessionEnableHost> = {}): SessionEnableHost => ({
  machine_code: 'RM-AAAA-BBBB-CCCC-DDDD', hostname: 'dev-box', os: 'linux', online: true, matched: false, ...over,
})

const filled = () => ({ ...blankEnableDraft({ host: 'RM-AAAA-BBBB-CCCC-DDDD', dir: '/work/app' }) })

class MemStorage {
  data = new Map<string, string>()
  getItem(k: string) {
    return this.data.get(k) ?? null
  }
  setItem(k: string, v: string) {
    this.data.set(k, v)
  }
}

describe('启用 Agent 的默认值（Q43 b）', () => {
  it('权限姿态默认放开全部命令、最长运行 24 小时、自动续跑关闭', () => {
    const d = blankEnableDraft()
    assert.equal(d.posture, 'force')
    assert.equal(ENABLE_DEFAULT_POSTURE, 'force')
    assert.deepEqual({ value: d.maxRun, unit: d.maxRunUnit }, ENABLE_DEFAULT_MAX_RUN)
    assert.equal(d.maxRun, '24')
    assert.equal(d.maxRunUnit, 'hour')
    assert.equal(d.autoResume, false)
    assert.equal(d.prompt, '')
  })

  it('请求里 24 小时 = 1440 分钟，权限姿态总是显式给出', () => {
    const req = toEnableRequest(WF, filled())
    assert.equal(req.max_run_minutes, 1440)
    assert.equal(req.posture, 'force')
    assert.equal(req.workflow_id, WF)
    assert.equal(req.dir, '/work/app')
    assert.equal(req.host, 'RM-AAAA-BBBB-CCCC-DDDD')
    assert.equal('auto_resume' in req, false, '默认关闭就不带')
    assert.equal('prompt' in req, false)
    assert.equal('bin' in req, false)
    assert.equal('model' in req, false)
  })

  it('改成白名单、自动续跑、自定义内容后都带进请求；提示词的换行折成空格', () => {
    const d = { ...filled(), posture: 'allowlist' as const, autoResume: true, model: ' claude-x ', bin: ' cursor-agent ', prompt: '先接入\n  再汇报 %workflow', maxRun: '1.5', maxRunUnit: 'hour' as const }
    const req = toEnableRequest(WF, d)
    assert.equal(req.posture, 'allowlist')
    assert.equal(req.auto_resume, true)
    assert.equal(req.model, 'claude-x')
    assert.equal(req.bin, 'cursor-agent')
    assert.equal(req.prompt, '先接入 再汇报 %workflow')
    assert.equal(req.max_run_minutes, 90)
  })

  it('最长运行时间留空 = 不带（用平台上限）', () => {
    const req = toEnableRequest(WF, { ...filled(), maxRun: '' })
    assert.equal('max_run_minutes' in req, false)
  })

  it('不带对话编号：「启用」永远是新开对话', () => {
    assert.equal('conversation_id' in toEnableRequest(WF, filled()), false)
  })
})

describe('校验', () => {
  const hosts = [host(), host({ machine_code: 'RM-WIN1', hostname: 'win-box', os: 'windows' })]

  it('默认草稿填好主机与目录就没有错误', () => {
    assert.deepEqual(validateEnableDraft(WF, filled(), LIMITS, hosts), {})
    assert.equal(hasEnableErrors({}), false)
  })

  it('目录必填、必须是绝对路径、不能有控制字符、不能超长', () => {
    assert.match(validateEnableDraft(WF, { ...filled(), dir: '   ' }, LIMITS, hosts).dir ?? '', /请填写目录/)
    assert.match(validateEnableDraft(WF, { ...filled(), dir: 'work/app' }, LIMITS, hosts).dir ?? '', /绝对路径/)
    assert.match(validateEnableDraft(WF, { ...filled(), dir: '~/app' }, LIMITS, hosts).dir ?? '', /绝对路径/)
    assert.match(validateEnableDraft(WF, { ...filled(), dir: '/work/a\nb' }, LIMITS, hosts).dir ?? '', /控制字符/)
    assert.match(validateEnableDraft(WF, { ...filled(), dir: '/' + 'x'.repeat(LIMITS.dir) }, LIMITS, hosts).dir ?? '', /最长/)
  })

  it('主机要选；Windows 主机选了也不行', () => {
    assert.match(validateEnableDraft(WF, { ...filled(), host: '' }, LIMITS, hosts).host ?? '', /选择/)
    assert.match(validateEnableDraft(WF, { ...filled(), host: 'RM-WIN1' }, LIMITS, hosts).host ?? '', /Windows/)
  })

  it('模型、可执行文件、提示词、最长运行时间沿用续聊预设的规则', () => {
    assert.ok(validateEnableDraft(WF, { ...filled(), model: 'bad model!' }, LIMITS, hosts).model)
    assert.ok(validateEnableDraft(WF, { ...filled(), bin: 'sudo' }, LIMITS, hosts).bin)
    assert.ok(validateEnableDraft(WF, { ...filled(), prompt: 'x'.repeat(LIMITS.runPrompt + 1) }, LIMITS, hosts).prompt)
    assert.ok(validateEnableDraft(WF, { ...filled(), maxRun: 'abc' }, LIMITS, hosts).maxRun)
    assert.ok(validateEnableDraft(WF, { ...filled(), maxRun: '8', maxRunUnit: 'day' }, LIMITS, hosts).maxRun, '超过 7 天上限')
    assert.equal(validateEnableDraft(WF, { ...filled(), maxRun: '7', maxRunUnit: 'day' }, LIMITS, hosts).maxRun, undefined)
  })

  it('工作流编号用弹窗所在的这个工作流：不会报「请填工作流编号」', () => {
    const e = validateEnableDraft(WF, filled(), LIMITS, hosts)
    assert.equal(e.workflow, undefined)
    assert.equal(e.conversation, undefined)
  })
})

describe('主机候选', () => {
  const online = host({ machine_code: 'RM-ON', remark: '开发机' })
  const offline = host({ machine_code: 'RM-OFF', hostname: 'off-box', online: false })
  const win = host({ machine_code: 'RM-WIN', hostname: 'win-box', os: 'windows' })

  it('在线的在前，Windows 在最后并且不能选，离线的标出来但可选', () => {
    const c = enableHostChoices([win, offline, online])
    assert.deepEqual(c.map(x => x.code), ['RM-ON', 'RM-OFF', 'RM-WIN'])
    assert.deepEqual(c.map(x => x.disabled), [false, false, true])
    assert.match(c[0].label, /开发机/)
    assert.match(c[1].label, /离线/)
    assert.match(c[2].label, /Windows 暂不支持/)
  })

  it('没有候选 = 空列表', () => {
    assert.deepEqual(enableHostChoices(null), [])
    assert.deepEqual(enableHostChoices(undefined), [])
  })

  it('默认选哪台：当前已选且可选 > 上次用的 > 服务端选定的 > 留空', () => {
    const hs = [online, offline, win]
    assert.equal(pickEnableHost('RM-OFF', 'RM-ON', 'RM-ON', hs), 'RM-OFF')
    assert.equal(pickEnableHost('', 'RM-OFF', 'RM-ON', hs), 'RM-OFF')
    assert.equal(pickEnableHost('', undefined, 'RM-ON', hs), 'RM-ON')
    assert.equal(pickEnableHost('', undefined, undefined, hs), '')
  })

  it('Windows 主机与不在候选里的主机不会被默认选中', () => {
    const hs = [online, win]
    assert.equal(pickEnableHost('RM-WIN', 'RM-WIN', 'RM-WIN', hs), '')
    assert.equal(pickEnableHost('RM-GONE', 'RM-GONE', 'RM-ON', hs), 'RM-ON')
  })

  it('运行目录（#184）：会话绑定了项目目录时，用户没亲手改过就用绑定的（含「上一次记住的」别的目录），亲手填的不动，清空了回到绑定的', () => {
    // 没绑定目录：原样返回，不管有没有编辑过
    assert.equal(pickEnableDir('/work/last', false, undefined), '/work/last')
    assert.equal(pickEnableDir('/work/mine', true, ''), '/work/mine')
    assert.equal(pickEnableDir('', false, '  '), '')
    // 绑定了：没改过 → 一律用绑定的（空的、或上一次记住的别的目录）
    assert.equal(pickEnableDir('', false, '/work/app'), '/work/app')
    assert.equal(pickEnableDir('/work/last', false, '/work/app'), '/work/app')
    // 亲手填的不动：与绑定目录不一致由服务端预览报 dir_mismatch，让用户看到原因
    assert.equal(pickEnableDir('/work/mine', true, '/work/app'), '/work/mine')
    // 亲手清空了：回到绑定的目录（空 = 用默认）
    assert.equal(pickEnableDir('  ', true, '/work/app'), '/work/app')
    // 绑定值两侧的空白不算
    assert.equal(pickEnableDir('', false, ' /work/app '), '/work/app')
  })
})

describe('记住上一次填的值', () => {
  it('只记主机、目录、模型、可执行文件；权限姿态与时限每次从默认开始', () => {
    const st = new MemStorage()
    saveEnableMemory(st, { ...filled(), model: 'm1', bin: 'agent', posture: 'allowlist', maxRun: '2', maxRunUnit: 'hour', autoResume: true, prompt: '秘密提示词' })
    const raw = st.getItem(ENABLE_MEMORY_KEY) ?? ''
    assert.ok(!raw.includes('秘密提示词'), '提示词不落存储')
    assert.ok(!raw.includes('allowlist'))
    const d = blankEnableDraft(loadEnableMemory(st))
    assert.equal(d.host, 'RM-AAAA-BBBB-CCCC-DDDD')
    assert.equal(d.dir, '/work/app')
    assert.equal(d.model, 'm1')
    assert.equal(d.bin, 'agent')
    assert.equal(d.posture, 'force')
    assert.equal(d.maxRun, '24')
    assert.equal(d.autoResume, false)
  })

  it('存储不可用、内容坏了、内容不是对象都当没有', () => {
    assert.deepEqual(loadEnableMemory(null), {})
    assert.deepEqual(loadEnableMemory({ getItem: () => 'not json' }), {})
    assert.deepEqual(loadEnableMemory({ getItem: () => '"str"' }), {})
    assert.deepEqual(loadEnableMemory({ getItem: () => null }), {})
    assert.deepEqual(loadEnableMemory({ getItem: () => { throw new Error('denied') } }), {})
  })

  it('存储写入失败静默放弃', () => {
    assert.doesNotThrow(() => saveEnableMemory({ setItem: () => { throw new Error('quota') } }, filled()))
    assert.doesNotThrow(() => saveEnableMemory(null, filled()))
  })

  it('记忆里的字段类型不对就忽略', () => {
    const d = blankEnableDraft(loadEnableMemory({ getItem: () => JSON.stringify({ host: 5, dir: ['x'], model: null }) }))
    assert.equal(d.host, '')
    assert.equal(d.dir, '')
    assert.equal(d.model, '')
  })
})

describe('说明与提示', () => {
  it('放开全部命令给红字警告，白名单没有', () => {
    assert.match(postureWarning('force'), /--force/)
    assert.match(postureWarning('force'), /任何命令/)
    assert.equal(postureWarning('allowlist'), '')
  })

  it('一句话总结', () => {
    assert.equal(enableSummary({ posture: 'force', maxRun: '24', maxRunUnit: 'hour', autoResume: false }, '开发机'), '在「开发机」上新开一个 Cursor CLI 对话 · 放开全部命令 · 最长运行 1 天')
    assert.equal(enableSummary({ posture: 'allowlist', maxRun: '', maxRunUnit: 'hour', autoResume: true }, ''), '在所选主机上新开一个 Cursor CLI 对话 · 只放行白名单内的操作 · 异常退出自动续跑')
  })

  it('上一次启用的提醒：还在运行 / 已结束 / 没启用过', () => {
    const preset = { id: 'ea-1', name: '启用 Agent · x' }
    const lr = (active: boolean) => ({ run_id: 'r1', state: active ? 'running' : 'succeeded', active, created_at: '2026-10-19T00:00:00Z' })
    assert.equal(previousRunNote(null), '')
    assert.equal(previousRunNote({}), '')
    assert.match(previousRunNote({ preset, last_run: lr(true) }), /还在进行/)
    assert.match(previousRunNote({ preset, last_run: lr(false) }), /复用这条预设/)
    assert.match(previousRunNote({ preset }), /复用这条预设/)
  })

  it('成功提示：新开 / 需批准 / 接着已有对话', () => {
    const preset = { id: 'ea-1', name: '启用 Agent · x' }
    const run = (over: Partial<Parameters<typeof enableNotice>[0]['run']> = {}) => ({
      name: preset.name, host: 'RM-A', run_id: 'r1', run_state: 'running', posture: 'force' as const, conversation_id: '', ...over,
    })
    const fresh = enableNotice({ preset, run: run({ new_conversation: true }) })
    assert.equal(fresh.title, '已启用 Agent')
    assert.match(fresh.description, /新开 Cursor CLI 对话/)
    assert.match(fresh.description, /不会再新开/)
    const approve = enableNotice({ preset, run: run({ run_state: 'awaiting_approval', new_conversation: true }) })
    assert.equal(approve.title, '启用 Agent：等待批准')
    assert.match(approve.description, /需批准/)
    const resumed = enableNotice({ preset, run: run({ conversation_id: 'chat-1', warnings: ['白名单提醒'] }) })
    assert.match(resumed.description, /接着它运行，不是新开/)
    assert.match(resumed.description, /白名单提醒/)
  })

  it('旧版 Hub（预设上限里没有新开对话默认提示词）不认识「启用 Agent」', () => {
    assert.equal(enableSupported(null), false)
    assert.equal(enableSupported({ launcher_run_prompt_max: 600 }), false)
    assert.equal(enableSupported({ launcher_run_prompt_max: 600, launcher_run_new_prompt_default: '默认 %workflow' }), true)
    assert.equal(launcherLimits({ launcher_run_new_prompt_default: 'x' }).runNewPromptDefault, 'x')
  })
})

// 类型层面保证：预览类型里该有的字段都在（编译期检查，运行期无断言）
const _pv: SessionEnablePreview = {
  workflow_id: WF, pending: true, host: '', hosts: [], dir: '', bin: 'agent', model: '', posture: 'force', new_conversation: true, conversation_id: '',
  prompt: '', default_prompt: '', command: '', max_run_minutes: 1440, auto_resume: false, warnings: [], issues: [], can_run: false,
}
void _pv
