import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  AWAITING_AGENT_ROUND,
  FIRST_MESSAGE_MAX,
  HOST_NAME_MAX,
  NEW_SESSION_PHASES,
  PROJECT_DIR_MAX,
  SESSION_NAME_MAX,
  WORKFLOW_ID_MAX,
  WORKFLOW_ID_MIN,
  advancedFilledCount,
  appliedLines,
  blankNewSessionDraft,
  buildCreateRequest,
  checkFirstMessage,
  checkHostName,
  checkProjectDirectory,
  checkSessionName,
  checkWorkflowId,
  collectNewSessionSuggestions,
  isAwaitingAgent,
  isWebPlaceholder,
  latestWorkflowRow,
  phaseLabel,
  warningLines,
  workflowRoundStatus,
} from './newSession'
import { deriveSessionStatus } from './sessionStatus'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'newSession.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('isWebPlaceholder：认出网页新建会话时落下的占位会话（#174）', () => {
  it('会话编号以 sess-web- 开头才算', () => {
    assert.equal(isWebPlaceholder({ session_id: 'sess-web-1a2b3c4d' }), true)
    assert.equal(isWebPlaceholder({ session_id: 'sess-1a2b3c4d' }), false)
    assert.equal(isWebPlaceholder({ session_id: 'wf-sess-web-x' }), false)
  })
  it('没有会话 / 没有编号都不算（不抛错）', () => {
    assert.equal(isWebPlaceholder(null), false)
    assert.equal(isWebPlaceholder(undefined), false)
    assert.equal(isWebPlaceholder({}), false)
  })
})

describe('workflowRoundStatus：最近一行是占位会话才翻成「待接入」', () => {
  it('占位会话 + completed → awaiting_agent', () => {
    assert.equal(workflowRoundStatus({ session_id: 'sess-web-aa' }, 'completed'), AWAITING_AGENT_ROUND)
  })
  it('有待答复的一轮 / 已归档时原样返回，不被「待接入」盖掉', () => {
    assert.equal(workflowRoundStatus({ session_id: 'sess-web-aa' }, 'pending'), 'pending')
    assert.equal(workflowRoundStatus({ session_id: 'sess-web-aa' }, 'archived'), 'archived')
  })
  it('智能体接入后（最近一行不是占位）回到原来的状态', () => {
    assert.equal(workflowRoundStatus({ session_id: 'sess-9f8e7d6c' }, 'completed'), 'completed')
    assert.equal(workflowRoundStatus(undefined, 'completed'), 'completed')
  })
})

describe('deriveSessionStatus：待接入', () => {
  const base = { pendingItems: 0, openTasks: 0, nowMs: Date.UTC(2026, 9, 18, 12, 0, 0) }

  it('awaiting_agent → 待接入（灰色、不脉动、说明写明怎么办）', () => {
    const v = deriveSessionStatus({ ...base, roundStatus: AWAITING_AGENT_ROUND })
    assert.ok(v)
    assert.equal(v.key, 'attach')
    assert.equal(v.label, '待接入')
    assert.equal(v.pulse, false)
    assert.equal(v.tone, 'muted')
    assert.match(v.title, /启动提示词/)
  })

  it('已归档优先于待接入；待回复的确认项优先于待接入', () => {
    assert.equal(deriveSessionStatus({ ...base, roundStatus: 'archived' })?.key, 'archived')
    // 占位会话还没有智能体，但用户可能已经收到别处提的确认项：等您处理更要紧
    assert.equal(deriveSessionStatus({ ...base, roundStatus: AWAITING_AGENT_ROUND, pendingItems: 2 })?.key, 'waiting')
  })

  it('智能体已经有动作（接入中）时不再说待接入', () => {
    // 占位会话已被智能体接入、但最近一行还是占位（智能体的第一次汇报还没落库）的瞬间：以智能体进行中为准
    const v = deriveSessionStatus({ ...base, roundStatus: AWAITING_AGENT_ROUND, agentState: 'working' })
    assert.equal(v?.key, 'working')
  })
})

describe('checkSessionName：名字校验与规整（与服务端同一口径）', () => {
  it('空 / 只有空白 / 只有零宽字符不通过', () => {
    for (const raw of ['', '   ', '\n\t', '\u200b\u200d']) {
      const r = checkSessionName(raw)
      assert.equal(r.ok, false, JSON.stringify(raw))
    }
  })
  it('首尾空白去掉，内部的换行 / 制表 / 连续空白折成一个空格', () => {
    const r = checkSessionName('  支付\n重构\t二期  ')
    assert.deepEqual(r, { ok: true, name: '支付 重构 二期' })
  })
  it('零宽字符丢掉', () => {
    assert.deepEqual(checkSessionName('支\u200b付'), { ok: true, name: '支付' })
  })
  it('长度按字符数算：80 个汉字通过，81 个不通过；emoji 也算一个字符', () => {
    assert.equal(checkSessionName('字'.repeat(SESSION_NAME_MAX)).ok, true)
    const tooLong = checkSessionName('字'.repeat(SESSION_NAME_MAX + 1))
    assert.equal(tooLong.ok, false)
    assert.match(tooLong.ok ? '' : tooLong.message, /81 \/ 80/)
    assert.equal(checkSessionName('😀'.repeat(SESSION_NAME_MAX)).ok, true)
  })
})

describe('接线：侧栏按钮、弹窗与「待接入」状态', () => {
  it('侧栏底部工具栏有「新建会话」按钮：在设置齿轮之后、右侧图标组最左边（通知按钮之前）', () => {
    const sidebar = read('components/AppSidebar.vue')
    assert.match(sidebar, /<NewSessionDialog/)
    const gearAt = sidebar.indexOf('@click="handleOpenSettings"')
    const iconAt = sidebar.indexOf('data-testid="sidebar-new-session"')
    const bellAt = sidebar.indexOf('@click="notifyStore.toggleDesktop"')
    assert.ok(gearAt > 0 && iconAt > 0 && bellAt > 0, '找不到底部工具栏里的按钮')
    assert.ok(gearAt < iconAt, '新建会话按钮应在设置齿轮之后')
    assert.ok(iconAt < bellAt, '新建会话按钮应在通知按钮之前（右侧图标组最左边）')
  })
  it('侧栏卡与页头徽标都把占位会话翻成「待接入」；侧栏卡上占位会话不画「执行 / 等待」计时（它没有执行过任何东西）', () => {
    const card = read('components/sidebar/SidebarItemCard.vue')
    assert.match(card, /workflowRoundStatus/)
    // 计时小标签由 SidebarItemTimer 自己走字（#215）；占位会话（待接入）不挂它
    assert.match(card, /<SidebarItemTimer\s+v-if="item\.type === 'feedback' && item\.status === 'completed' && !awaitingAgent"\s+variant="chip"/)
    assert.match(read('components/feedback/SessionStatusBadge.vue'), /workflowRoundStatus/)
  })
})

describe('latestWorkflowRow / isAwaitingAgent：「启用 Agent」入口与「待接入」徽标同一口径（#176）', () => {
  const row = (session_id: string, created_at: string, over: Record<string, unknown> = {}) => ({ session_id, workflow_id: 'wf-1', created_at, status: 'completed', ...over })
  const placeholder = row('sess-web-aaaa1111', '2026-10-19T01:00:00Z')

  it('只看同一个工作流里创建时间最新的一行；没有工作流编号就是它自己', () => {
    const newer = row('sess-real-1', '2026-10-19T02:00:00Z')
    const other = row('sess-other', '2026-10-19T09:00:00Z', { workflow_id: 'wf-2' })
    assert.equal(latestWorkflowRow(placeholder, [placeholder, newer, other])?.session_id, 'sess-real-1')
    assert.equal(latestWorkflowRow(placeholder, [placeholder, other])?.session_id, 'sess-web-aaaa1111')
    const lone = row('sess-x', '2026-10-19T01:00:00Z', { workflow_id: '' })
    assert.equal(latestWorkflowRow(lone, [lone, newer]), lone)
    assert.equal(latestWorkflowRow(null, [newer]), null)
  })

  it('最新一行是占位会话且已完成才是待接入：智能体接入后（有更新的行）不再是', () => {
    assert.equal(isAwaitingAgent(placeholder, [placeholder]), true)
    assert.equal(isAwaitingAgent(placeholder, [placeholder, row('sess-real-1', '2026-10-19T02:00:00Z')]), false)
    assert.equal(isAwaitingAgent(row('sess-real-1', '2026-10-19T02:00:00Z'), [placeholder, row('sess-real-1', '2026-10-19T02:00:00Z')]), false)
  })

  it('归档的、有待答复的占位会话不算待接入（与徽标一致）；没有会话不抛错', () => {
    assert.equal(isAwaitingAgent({ ...placeholder, status: 'archived' }, [placeholder]), false)
    assert.equal(isAwaitingAgent({ ...placeholder, status: 'pending' }, [placeholder]), false)
    assert.equal(isAwaitingAgent(null, []), false)
    assert.equal(isAwaitingAgent(undefined, []), false)
  })
})

describe('接线：会话页「启用 Agent」入口（#176 第三期）', () => {
  it('输入区状态条里，「启用 Agent」紧跟在「运行」按钮之后；两者都按工作流编号渲染', () => {
    const dock = read('components/feedback/FeedbackInputDock.vue')
    assert.match(dock, /import SessionEnableAgent from '\.\/SessionEnableAgent\.vue'/)
    const runAt = dock.indexOf('<SessionQuickRun')
    const enableAt = dock.indexOf('<SessionEnableAgent')
    assert.ok(runAt > 0 && enableAt > runAt, '「启用 Agent」应在「运行」按钮之后')
    assert.match(dock, /<SessionEnableAgent v-if="currentWorkflowId" :workflow-id="currentWorkflowId" \/>/)
  })
  it('入口只在待接入时出现，判断与页头徽标同一份纯函数；弹窗开着时不会因为状态变化被收掉', () => {
    const entry = read('components/feedback/SessionEnableAgent.vue')
    assert.match(entry, /isAwaitingAgent\(cur, pool\)/)
    assert.match(entry, /enableSupported\(terminal\.prefs\?\.limits\)/)
    assert.match(entry, /terminal\.gateOpen/)
    assert.match(entry, /<EnableAgentDialog v-if="visible \|\| open"/)
    assert.match(read('components/feedback/SessionStatusBadge.vue'), /latestWorkflowRow/)
  })
  it('弹窗：点「启用并运行」前后的护栏都在（提交期间禁用、解锁取消静默、失败就地显示原因）', () => {
    const dlg = read('components/terminal/EnableAgentDialog.vue')
    assert.match(dlg, /:disabled="!canSubmit"/)
    assert.match(dlg, /if \(submitting\.value\) return/)
    assert.match(dlg, /if \(!\(e instanceof UnlockCancelled\)\) submitError\.value = toApiError\(e\)\.message/)
    assert.match(dlg, /terminal\.enableSessionAgent\(toEnableRequest\(props\.workflowId, draft\)\)/)
    // 权限姿态默认放开全部命令，并有红字警告
    assert.match(dlg, /data-testid="enable-agent-posture-warning"/)
    assert.match(dlg, /放开全部命令（默认）/)
  })
})

// ───────── 高级选项（#184） ─────────

describe('checkWorkflowId：自己指定的工作流编号（与服务端 webWorkflowIDPattern 同一口径）', () => {
  it('留空 = 自动生成；填了先去首尾空白', () => {
    assert.deepEqual(checkWorkflowId(''), { ok: true, value: '' })
    assert.deepEqual(checkWorkflowId('   '), { ok: true, value: '' })
    assert.deepEqual(checkWorkflowId('  pay-refactor.v2 '), { ok: true, value: 'pay-refactor.v2' })
  })
  it('字母或数字开头，后面可含 . _ -；3 个字符起、64 个字符止', () => {
    assert.equal(checkWorkflowId('abc').ok, true)
    assert.equal(checkWorkflowId('A1_b.c-d').ok, true)
    assert.equal(checkWorkflowId('a'.repeat(WORKFLOW_ID_MAX)).ok, true)
    assert.equal(checkWorkflowId('a'.repeat(WORKFLOW_ID_MAX + 1)).ok, false)
    assert.equal(checkWorkflowId('a'.repeat(WORKFLOW_ID_MIN - 1)).ok, false)
  })
  it('不合规的给出具体原因：太短 / 太长 / 字符不对', () => {
    const short = checkWorkflowId('ab')
    assert.ok(!short.ok && short.message.includes('太短'))
    const long = checkWorkflowId('a'.repeat(70))
    assert.ok(!long.ok && long.message.includes('太长'))
    for (const bad of ['-abc', '.abc', '_abc', 'ab c', 'ab/cd', '支付模块重构', 'abc!']) {
      const r = checkWorkflowId(bad)
      assert.ok(!r.ok && r.message.includes('字母、数字'), bad)
    }
  })
})

describe('checkFirstMessage：首条指令', () => {
  it('留空 = 没有；填了去首尾空白，正文里的换行原样保留', () => {
    assert.deepEqual(checkFirstMessage('   \n  '), { ok: true, value: '' })
    assert.deepEqual(checkFirstMessage('  第一行\n\n第二行  '), { ok: true, value: '第一行\n\n第二行' })
  })
  it('按字符数算长度：恰好上限通过，多一个不通过；NUL 不通过', () => {
    assert.equal(checkFirstMessage('我'.repeat(FIRST_MESSAGE_MAX)).ok, true)
    assert.equal(checkFirstMessage('我'.repeat(FIRST_MESSAGE_MAX + 1)).ok, false)
    assert.equal(checkFirstMessage('a\u0000b').ok, false)
  })
})

describe('checkProjectDirectory：项目目录必须是绝对路径', () => {
  it('留空 = 不绑定；POSIX / 盘符 / UNC 绝对路径通过', () => {
    assert.deepEqual(checkProjectDirectory(''), { ok: true, value: '' })
    for (const good of ['/home/me/app', '/', 'C:\\Users\\me\\app', 'D:/work/app', '\\\\server\\share\\app']) {
      assert.equal(checkProjectDirectory(good).ok, true, good)
    }
    assert.deepEqual(checkProjectDirectory('  /home/me/app  '), { ok: true, value: '/home/me/app' })
  })
  it('相对路径、~ 开头、只有盘符、半截 UNC 不通过', () => {
    for (const bad of ['app', './app', '../app', '~/app', 'C:', 'C:app', '\\\\server', '\\\\server\\']) {
      assert.equal(checkProjectDirectory(bad).ok, false, bad)
    }
  })
  it('换行 / 制表 / 零宽字符 / 三个反引号 / 超长都不通过（它会被写进启动提示词）', () => {
    assert.equal(checkProjectDirectory('/a\nb').ok, false)
    assert.equal(checkProjectDirectory('/a\tb').ok, false)
    assert.equal(checkProjectDirectory('/a\u200bb').ok, false)
    assert.equal(checkProjectDirectory('/a```b').ok, false)
    assert.equal(checkProjectDirectory('/' + 'a'.repeat(PROJECT_DIR_MAX)).ok, false)
    assert.equal(checkProjectDirectory('/' + 'a'.repeat(PROJECT_DIR_MAX - 1)).ok, true)
  })
})

describe('checkHostName：主机名', () => {
  it('留空 = 不指定；空白折成一个空格，零宽字符丢掉', () => {
    assert.deepEqual(checkHostName(''), { ok: true, value: '' })
    assert.deepEqual(checkHostName('  my \n host\u200b '), { ok: true, value: 'my host' })
  })
  it('超过上限不通过', () => {
    assert.equal(checkHostName('h'.repeat(HOST_NAME_MAX)).ok, true)
    assert.equal(checkHostName('h'.repeat(HOST_NAME_MAX + 1)).ok, false)
  })
})

describe('buildCreateRequest：整份草稿 → 请求体', () => {
  it('只填名字：请求体里只有 title（与高级选项出现之前的请求完全一样）', () => {
    const v = buildCreateRequest({ ...blankNewSessionDraft(), name: '  支付模块重构 ' })
    assert.ok(v.ok)
    assert.deepEqual(v.request, { title: '支付模块重构' })
    assert.deepEqual(Object.keys(v.request), ['title'])
  })
  it('高级选项填了几项就带几项；填了又清空（只剩空白）等于没填', () => {
    const v = buildCreateRequest({
      name: '支付模块重构',
      workflowId: ' pay-refactor ',
      firstMessage: ' 先看一下 README ',
      phase: 'plan',
      projectDirectory: '/home/me/pay',
      hostName: 'dev-box',
    })
    assert.ok(v.ok)
    assert.deepEqual(v.request, {
      title: '支付模块重构',
      workflow_id: 'pay-refactor',
      first_message: '先看一下 README',
      phase: 'plan',
      project_directory: '/home/me/pay',
      host_name: 'dev-box',
    })
    const cleared = buildCreateRequest({ ...blankNewSessionDraft(), name: 'x', workflowId: '  ', firstMessage: '\n', hostName: ' ' })
    assert.ok(cleared.ok)
    assert.deepEqual(Object.keys(cleared.request), ['title'])
  })
  it('名字空 / 任何一项不合规都不通过，错误按字段给出（可同时有多项）', () => {
    const none = buildCreateRequest(blankNewSessionDraft())
    assert.ok(!none.ok && !!none.errors.name)
    const v = buildCreateRequest({ ...blankNewSessionDraft(), name: 'ok', workflowId: 'a b', projectDirectory: 'relative/path' })
    assert.ok(!v.ok)
    assert.equal(v.errors.name, undefined)
    assert.ok(v.errors.workflowId && v.errors.projectDirectory)
    assert.equal(v.errors.firstMessage, undefined)
  })
})

describe('advancedFilledCount / phaseLabel / NEW_SESSION_PHASES', () => {
  it('统计高级选项里填了几项（只有空白不算）', () => {
    assert.equal(advancedFilledCount(blankNewSessionDraft()), 0)
    assert.equal(advancedFilledCount({ ...blankNewSessionDraft(), workflowId: ' ', firstMessage: 'x', phase: 'dev' }), 2)
  })
  it('阶段选项的顺序与名字：不指定 / 讨论 / 方案 / 开发', () => {
    assert.deepEqual(NEW_SESSION_PHASES.map(p => p.id), ['', 'discuss', 'plan', 'dev'])
    assert.equal(phaseLabel('discuss'), '讨论')
    assert.equal(phaseLabel('plan'), '方案')
    assert.equal(phaseLabel('dev'), '开发')
    assert.equal(phaseLabel('whatever'), 'whatever')
  })
})

describe('appliedLines / warningLines：建好之后告诉用户「实际生效了什么、什么没做成」', () => {
  it('没有 applied 就没有行；有几项写几行，目录那一行写明绑定的后果', () => {
    assert.deepEqual(appliedLines(undefined), [])
    assert.deepEqual(appliedLines(null), [])
    assert.deepEqual(appliedLines({}), [])
    const lines = appliedLines({ phase: 'plan', first_message: true, project_directory: '/home/me/pay', host_name: 'dev-box' })
    assert.equal(lines.length, 4)
    assert.ok(lines[0].includes('方案'))
    assert.ok(lines[1].includes('收件箱'))
    assert.ok(lines[2].includes('/home/me/pay') && lines[2].includes('拒绝'))
    assert.ok(lines[3].includes('dev-box'))
  })
  it('warnings：会话已建好、哪一项没做成、原因与补救办法都写明', () => {
    assert.deepEqual(warningLines(undefined), [])
    assert.deepEqual(warningLines([]), [])
    const w = warningLines([
      { field: 'phase', error: 'db busy' },
      { field: 'first_message', error: 'inbox full' },
      { field: 'other', error: 'x' },
    ])
    assert.equal(w.length, 3)
    assert.ok(w[0].includes('起始阶段') && w[0].includes('db busy') && w[0].includes('阶段滑块'))
    assert.ok(w[1].includes('首条指令') && w[1].includes('inbox full') && w[1].includes('输入框'))
    assert.ok(w[2].includes('other'))
  })
})

describe('collectNewSessionSuggestions：主机与目录候选取自已有会话', () => {
  it('最近用过的排前面，各自去重，空值跳过，最多 limit 个', () => {
    const rows = [
      { host_name: 'old-host', project_directory: '/old', updated_at: '2026-01-01T00:00:00Z' },
      { host_name: 'new-host', project_directory: '/new', updated_at: '2026-03-01T00:00:00Z' },
      { host_name: 'new-host', project_directory: '/new', updated_at: '2026-02-01T00:00:00Z' },
      { host_name: '', project_directory: null, updated_at: '2026-04-01T00:00:00Z' },
      { host_name: ' mid-host ', project_directory: '/mid', created_at: '2026-02-15T00:00:00Z' },
    ]
    const s = collectNewSessionSuggestions(rows)
    assert.deepEqual(s.hosts, ['new-host', 'mid-host', 'old-host'])
    assert.deepEqual(s.dirs, ['/new', '/mid', '/old'])
    assert.deepEqual(collectNewSessionSuggestions(rows, 1), { hosts: ['new-host'], dirs: ['/new'] })
    assert.deepEqual(collectNewSessionSuggestions([]), { hosts: [], dirs: [] })
  })
})

describe('接线：新建会话弹窗的高级选项（#184）', () => {
  const dialog = () => read('components/sidebar/NewSessionDialog.vue')
  it('高级选项默认收起，名称仍是唯一必填：按钮可用只取决于整份草稿校验通过', () => {
    const src = dialog()
    assert.match(src, /const advancedOpen = ref\(false\)/)
    assert.match(src, /advancedOpen\.value = false/) // 每次打开弹窗都重新收起
    assert.match(src, /data-testid="new-session-advanced-toggle"/)
    assert.match(src, /v-if="advancedOpen"[^>]*id="new-session-advanced"/)
    assert.match(src, /canSubmit = computed\(\(\) => validation\.value\.ok/)
  })
  it('请求体由 buildCreateRequest 生成（只带填了的项），409 只挂到编号输入框上', () => {
    const src = dialog()
    assert.match(src, /sessionStore\.createWorkflow\(v\.request\)/)
    assert.match(src, /status === 409 && draft\.workflowId\.trim\(\)/)
    assert.match(src, /idConflict\.value = msg/)
  })
  it('建好之后列出实际生效的设置与没做成的事', () => {
    const src = dialog()
    assert.match(src, /data-testid="new-session-applied"/)
    assert.match(src, /data-testid="new-session-warnings"/)
  })
})
