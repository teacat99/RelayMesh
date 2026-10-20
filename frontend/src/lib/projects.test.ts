import { describe, test } from 'node:test'
import assert from 'node:assert/strict'
import type { AclGrant, AuditEvent, KbDoc, ProjectModule } from '../api/projects'
import type { AgentIdentity } from './agentState'
import {
  aclModeHint,
  aclModeLabel,
  AUDIT_LEGEND,
  auditActionLabel,
  auditActorText,
  auditDetail,
  auditEventLabel,
  auditExplain,
  auditResultChip,
  auditScopeText,
  flattenModules,
  grantActive,
  grantExpiryLabel,
  grantScopeLabel,
  isIdentityRequired,
  isTasksScope,
  isWouldDeny,
  joinModulePath,
  moveReasonLabel,
  parentCandidates,
  renderLabel,
  taskManagerCandidates,
  taskManagerModeNote,
  taskManagerView,
  toTreeDocs,
  validModuleSegment,
} from './projects'

const mod = (path: string): ProjectModule => ({ id: 1, project_id: 1, path, title: path, created_at: '' })

describe('项目知识库口径', () => {
  test('档位文案齐全且 enforce 的说明提到被拒绝', () => {
    assert.equal(aclModeLabel('audit'), '仅审计')
    assert.equal(aclModeLabel(''), '关闭')
    assert.ok(String(aclModeHint('enforce')).includes('拒绝'))
    assert.ok(String(aclModeHint('audit')).includes('不拦截'))
    assert.ok(String(aclModeHint('off')).includes('不做任何校验'))
  })

  test('模块按路径排成父前子后，并带缩进深度', () => {
    const flat = flattenModules([mod('chat/groups'), mod('chat'), mod('auth')])
    assert.deepEqual(flat.map(n => n.module.path), ['auth', 'chat', 'chat/groups'])
    assert.deepEqual(flat.map(n => n.depth), [0, 0, 1])
    assert.equal(flat[2].name, 'groups')
  })

  test('父路径候选含顶层，路径拼接去掉多余斜杠', () => {
    assert.deepEqual(parentCandidates([mod('b'), mod('a')]), ['', 'a', 'b'])
    assert.equal(joinModulePath('', ' /chat/ '), 'chat')
    assert.equal(joinModulePath('chat', 'groups'), 'chat/groups')
  })

  test('模块名预检：小写字母数字连字符', () => {
    assert.equal(validModuleSegment('chat-2'), true)
    assert.equal(validModuleSegment('Chat'), false)
    assert.equal(validModuleSegment('-x'), false)
    assert.equal(validModuleSegment('a b'), false)
    assert.equal(validModuleSegment(''), false)
  })

  test('迁移原因与授权文案', () => {
    assert.ok(String(moveReasonLabel('conflict')).includes('legacy'))
    assert.ok(String(moveReasonLabel('sheet')).includes('sessions'))
    assert.equal(moveReasonLabel('x'), 'x')
    assert.equal(grantScopeLabel('module', 'chat'), '模块 chat')
    assert.equal(grantScopeLabel('project', '1'), '整个项目')
  })

  test('授权有效期文案与有效性', () => {
    const now = new Date('2026-10-15T12:00:00Z').getTime()
    assert.equal(grantExpiryLabel(null, now), '长期有效')
    assert.equal(grantExpiryLabel('2026-10-15T11:00:00Z', now), '已过期')
    assert.equal(grantExpiryLabel('2026-10-15T12:30:00Z', now), '30 分钟后过期')
    assert.equal(grantExpiryLabel('2026-10-15T15:00:00Z', now), '3 小时后过期')
    assert.equal(grantExpiryLabel('2026-10-20T12:00:00Z', now), '5 天后过期')
    const g = { revoked_at: null, expires_at: '2026-10-15T13:00:00Z' } as AclGrant
    assert.equal(grantActive(g, now), true)
    assert.equal(grantActive({ ...g, revoked_at: '2026-10-15T11:00:00Z' }, now), false)
    assert.equal(grantActive({ ...g, expires_at: '2026-10-15T11:00:00Z' }, now), false)
  })

  test('审计：动作名、详情解析、would_deny 判断、主体文字', () => {
    assert.equal(auditActionLabel('doc.write'), '写入文档')
    assert.equal(auditActionLabel('mystery'), 'mystery')
    const ev = { id: 1, at: '', actor_kind: 'identity', actor_ref: 'identity:3', actor_label: '后端', action: 'acl.denied', result: 'deny', detail: '{"would_deny":true,"rule":"R7"}' } as AuditEvent
    assert.equal(auditDetail(ev).rule, 'R7')
    assert.equal(isWouldDeny(ev), true)
    assert.equal(isWouldDeny({ ...ev, action: 'doc.write' }), false)
    assert.equal(isWouldDeny({ ...ev, detail: '{"would_deny":false}' }), false)
    assert.deepEqual(auditDetail({ ...ev, detail: 'not json' }), {})
    assert.deepEqual(auditDetail({ ...ev, detail: undefined }), {})
    assert.equal(auditActorText(ev), '后端')
    assert.equal(auditActorText({ ...ev, actor_label: '', actor_ref: 'workflow:w' }), 'workflow:w')
    assert.equal(auditActorText({ ...ev, actor_kind: 'human', actor_label: '' }), '你')
  })

  // 用户 #150 看到的那一条：core-app 的 project_docs:save 没带 project_directory，项目在审计档。
  const unasserted = (extra: Record<string, unknown> = {}, over: Partial<AuditEvent> = {}): AuditEvent => ({
    id: 41,
    at: '2026-10-16T04:51:03Z',
    actor_kind: 'identity',
    actor_ref: 'identity:3',
    actor_label: 'core-app',
    action: 'acl.denied',
    result: 'assumed',
    target: 'project_docs:save',
    detail: JSON.stringify({ mode: 'audit', reason: 'identity_required', would_deny: true, ...extra }),
    ...over,
  }) as AuditEvent

  test('审计（#150）：没声明身份的写入不再叫「权限不足」，也不画成被拒绝', () => {
    const ev = unasserted()
    assert.equal(isIdentityRequired(ev), true)
    assert.equal(auditEventLabel(ev), '未声明身份')
    assert.equal(auditActionLabel('acl.denied'), '权限不足 / 未声明身份') // 过滤下拉里同时说明两种
    const chip = auditResultChip(ev)
    assert.equal(chip.label, '已放行 · 强制执行会拒绝')
    assert.match(chip.tone, /amber/)
    assert.doesNotMatch(chip.label, /^被拒绝/)
    assert.match(chip.hint, /没有被拦下/)
    // 白话解释：为什么、有没有拦下、怎么补；并点明「授权没被查」
    const text = auditExplain(ev)
    assert.match(text, /没有带 project_directory/)
    assert.match(text, /推断调用方是「core-app」/)
    assert.match(text, /没有去查授权/)
    assert.match(text, /审计档：已放行并记录，没有拦下任何东西/)
    assert.match(text, /project_directory 与 host_name/)
  })

  test('审计（#150）：强制执行档里同样的记录才是真的被拒绝', () => {
    const ev = unasserted({ mode: 'enforce', would_deny: false })
    const chip = auditResultChip(ev)
    assert.equal(chip.label, '被拒绝')
    assert.match(chip.tone, /red/)
    const text = auditExplain(ev)
    assert.match(text, /强制执行档，所以被拒绝了/)
    assert.doesNotMatch(text, /没有拦下任何东西/)
    assert.match(text, /后重试/)
  })

  test('审计（#150）：声明了身份但角色不够，叫「权限不足」并说清缺什么', () => {
    const ev = unasserted({ reason: '需要 edit 权限，你在这里只有 read', rule: 'R7', have: 'read', need: 'edit' }, { result: 'deny', target: 'project_docs:save' })
    assert.equal(isIdentityRequired(ev), false)
    assert.equal(auditEventLabel(ev), '权限不足')
    assert.equal(auditResultChip(ev).label, '已放行 · 强制执行会拒绝')
    const text = auditExplain(ev)
    assert.match(text, /只有「只读」/)
    assert.match(text, /需要「可编辑」/)
    assert.match(text, /规则 R7/)
    assert.match(text, /给它「可编辑」授权/)
    const enforced = auditExplain(unasserted({ mode: 'enforce', would_deny: false, rule: 'R7', have: 'none', need: 'edit' }, { result: 'deny', detail: JSON.stringify({ mode: 'enforce', would_deny: false, rule: 'R7', have: 'none', need: 'edit' }) }))
    assert.match(enforced, /只有「没有权限」/)
    assert.match(enforced, /所以被拒绝了/)
  })

  test('审计（#150）：身份没认出来时不假装推断出了谁；其余记录原样', () => {
    const ghost = unasserted({}, { actor_ref: 'workflow:wf-x', actor_label: 'wf-x' })
    assert.match(auditExplain(ghost), /连调用方是哪个身份也认不出来/)
    assert.doesNotMatch(auditExplain(ghost), /推断调用方是/)
    // 带了身份的放行、被拒绝（老记录没有 would_deny）都保持原来的标签
    const allow = { id: 2, at: '', actor_kind: 'identity', actor_ref: 'identity:3', action: 'doc.write', result: 'allow', detail: '{}' } as AuditEvent
    assert.equal(auditResultChip(allow).label, '放行')
    assert.equal(auditResultChip(allow).hint, '')
    assert.equal(auditExplain(allow), '')
    assert.equal(auditEventLabel(allow), '写入文档')
    const oldDeny = { ...allow, action: 'acl.denied', result: 'deny', detail: '{"rule":"R7"}' } as AuditEvent
    assert.equal(auditResultChip(oldDeny).label, '被拒绝')
    // 写文档但没带身份（成功落盘后记的 assumed）：按推断，并说明操作已完成
    const assumedWrite = { ...allow, action: 'doc.write', result: 'assumed', actor_label: 'core-app' } as AuditEvent
    const chip = auditResultChip(assumedWrite)
    assert.equal(chip.label, '按推断')
    assert.match(chip.hint, /project_directory/)
    assert.match(auditExplain(assumedWrite), /操作本身已经完成/)
    // 图例覆盖界面上会出现的每一种标签
    const terms = AUDIT_LEGEND.map(x => x.term)
    for (const label of ['放行', '按推断', '已放行 · 强制执行会拒绝', '被拒绝', '未声明身份']) assert.ok(terms.includes(label), label)
  })

  test('知识库文档转成文档树组件的形状', () => {
    const docs: KbDoc[] = [{ id: 7, workflow_id: 'wf', path: 'a/b.md', title: 'B', kind: 'design', status: 'draft', updated_at: 't' }]
    const out = toTreeDocs(docs)
    assert.deepEqual(Object.fromEntries(Object.entries(out[0]).filter(([k]) => k in ({ id: 7, workflow_id: 'wf', path: 'a/b.md', tags: [], rev: 0 }))), { id: 7, workflow_id: 'wf', path: 'a/b.md', tags: [], rev: 0 })
    assert.equal(renderLabel('mermaid'), '图')
    assert.equal(renderLabel(undefined), 'Markdown')
  })

  // ---- 任务管理（#175 B2c）----
  const idn = (id: number, label: string, dir: string, host = 'h'): AgentIdentity => ({ id, label, host_name: host, project_directory: dir }) as AgentIdentity
  const grant = (over: Partial<AclGrant>): AclGrant => ({ id: 1, project_id: 1, principal_kind: 'identity', principal_ref: '3', scope_kind: 'tasks', scope_ref: '1', role: 'edit', granted_by_kind: 'human', granted_by_ref: 'human', created_at: '', ...over }) as AclGrant

  test('任务管理：指定、项目级编辑授权、撤销与过期各自怎么算', () => {
    const now = new Date('2026-10-19T12:00:00Z').getTime()
    assert.deepEqual(taskManagerView([], 3, now), { identityId: 3, designatedGrantId: null, inherited: false, manager: false })
    assert.deepEqual(taskManagerView([grant({ id: 7 })], 3, now), { identityId: 3, designatedGrantId: 7, inherited: false, manager: true })
    // 别的身份、群授权、只读的项目授权都不算
    assert.equal(taskManagerView([grant({ principal_ref: '4' })], 3, now).manager, false)
    assert.equal(taskManagerView([grant({ principal_kind: 'group', principal_ref: '3' })], 3, now).manager, false)
    assert.equal(taskManagerView([grant({ scope_kind: 'project', role: 'read' })], 3, now).manager, false)
    assert.equal(taskManagerView([grant({ scope_kind: 'module', scope_ref: 'chat', role: 'edit' })], 3, now).manager, false)
    // 整个项目的编辑 / 管理授权同样让对方成为管理 Agent，但那不是「任务管理」勾出来的
    assert.deepEqual(taskManagerView([grant({ scope_kind: 'project', role: 'edit' })], 3, now), { identityId: 3, designatedGrantId: null, inherited: true, manager: true })
    assert.equal(taskManagerView([grant({ scope_kind: 'project', role: 'manage' })], 3, now).inherited, true)
    // 两者并存：勾选可以取消，但取消后仍是管理 Agent
    const both = taskManagerView([grant({ id: 7 }), grant({ id: 8, scope_kind: 'project', role: 'edit' })], 3, now)
    assert.deepEqual([both.designatedGrantId, both.inherited, both.manager], [7, true, true])
    // 撤销、过期的不算
    assert.equal(taskManagerView([grant({ revoked_at: '2026-10-19T11:00:00Z' })], 3, now).manager, false)
    assert.equal(taskManagerView([grant({ expires_at: '2026-10-19T11:00:00Z' })], 3, now).manager, false)
  })

  test('任务管理：只列目录归在本项目里的身份，已被指定但目录被移出的也留着以便取消', () => {
    const ids = [idn(3, '后端', '/code/api'), idn(1, '前端', '/code/web'), idn(9, '别的项目', '/other'), idn(5, '远端同目录', '/code/api', 'remote'), idn(6, '已移出', '/old')]
    const dirs = [{ host_name: 'h', project_directory: '/code/api' }, { host_name: 'h', project_directory: '/code/web' }]
    const got = taskManagerCandidates(ids, dirs, [grant({ principal_ref: '6' })]).map(i => i.id)
    assert.deepEqual(got.sort((a, b) => a - b), [1, 3, 6]) // 9 不在项目；5 是另一台主机的同名目录
    // 没有被指定的话 6 不出现
    assert.deepEqual(taskManagerCandidates(ids, dirs, []).map(i => i.id).sort((a, b) => a - b), [1, 3])
  })

  test('任务管理：访问控制档位决定这套规则的效力', () => {
    assert.match(taskManagerModeNote('enforce'), /已生效/)
    assert.match(taskManagerModeNote('audit'), /只记录、不拦截/)
    assert.match(taskManagerModeNote('off'), /暂时不起作用/)
    assert.match(taskManagerModeNote(''), /暂时不起作用/)
    assert.equal(grantScopeLabel('tasks', '1'), '任务管理（派任务、编辑所有任务）')
  })

  test('任务管理：审计里的任务权限记录有自己的名字与白话解释', () => {
    const ev = {
      id: 90,
      at: '2026-10-19T04:00:00Z',
      actor_kind: 'identity',
      actor_ref: 'identity:4',
      actor_label: '前端',
      action: 'acl.denied',
      scope_kind: 'tasks',
      scope_ref: '1',
      target: 'workflow_context:task_update#12',
      result: 'deny',
      detail: JSON.stringify({ mode: 'audit', rule: 'tasks', reason: '这条任务不是你的工作流创建的', would_deny: true, manager: false }),
    } as AuditEvent
    assert.equal(isTasksScope(ev), true)
    assert.equal(auditEventLabel(ev), '任务权限不足')
    assert.equal(auditScopeText(ev), '共享任务板')
    assert.equal(auditScopeText({ ...ev, scope_kind: 'module', scope_ref: 'chat' }), 'module chat')
    assert.equal(auditResultChip(ev).label, '已放行 · 强制执行会拒绝')
    const soft = auditExplain(ev)
    assert.match(soft, /共享任务板上的这次写入超出了规则：这条任务不是你的工作流创建的/)
    assert.match(soft, /审计档：已放行并记录/)
    assert.match(soft, /「任务管理」里指定它为管理 Agent/)
    const hard = auditExplain({ ...ev, detail: JSON.stringify({ mode: 'enforce', rule: 'tasks', reason: 'x', would_deny: false }) })
    assert.match(hard, /强制执行档，所以被拒绝了/)
    assert.equal(auditResultChip({ ...ev, detail: JSON.stringify({ would_deny: false }) }).label, '被拒绝')
  })

  test('审计（#179）：项目管理 Agent 运行预设有自己的名字与白话解释，且不泄露提示词', () => {
    const ev = {
      id: 120,
      at: '2026-10-19T05:00:00Z',
      actor_kind: 'identity',
      actor_ref: 'identity:2',
      actor_label: '项目管理',
      action: 'launcher.run',
      scope_kind: 'tasks',
      scope_ref: '1',
      target: 'preset:3',
      result: 'allow',
      detail: JSON.stringify({ preset: '3', name: '子会话 · 前端', workflow_id: 'wf-front', run_id: 'run-9', state: 'running' }),
    } as AuditEvent
    assert.equal(auditActionLabel('launcher.run'), '运行预设')
    assert.equal(auditEventLabel(ev), '运行预设')
    assert.equal(auditResultChip(ev).label, '放行')
    const ok = auditExplain(ev)
    assert.match(ok, /「项目管理」作为项目管理 Agent 运行了预设「子会话 · 前端」，唤醒的是工作流 wf-front/)
    assert.match(ok, /提示词与命令不会记录/)
    assert.doesNotMatch(ok, /需要你在 ExecMesh 里批准|要你在 ExecMesh 里批准/)
    // 主机的执行模式是「需批准」：提醒用户去批准，否则以为它没动静。
    const pending = auditExplain({ ...ev, detail: JSON.stringify({ name: 'x', workflow_id: 'wf', state: 'awaiting_approval' }) })
    assert.match(pending, /需批准.*批准后才会开始/)
  })

  test('审计（#179）：被拒绝的运行说清是哪条规则拦下的；没见过的码原样显示', () => {
    const base = {
      id: 121,
      at: '2026-10-19T05:01:00Z',
      actor_kind: 'identity',
      actor_ref: 'identity:5',
      actor_label: '前端',
      action: 'launcher.run',
      scope_kind: 'tasks',
      scope_ref: '1',
      target: 'preset:3',
      result: 'deny',
    } as AuditEvent
    const deny = (code?: string) => auditExplain({ ...base, detail: JSON.stringify(code ? { code } : {}) })
    assert.equal(auditResultChip({ ...base, detail: JSON.stringify({ code: 'not_project_manager' }) }).label, '被拒绝')
    assert.match(deny('not_project_manager'), /「前端」想运行预设，被拒绝了：它不是项目管理 Agent（项目页「任务管理」里没有勾选它）/)
    assert.match(deny('preset_not_found'), /目标工作流不在本项目的任务板上/)
    assert.match(deny('own_workflow'), /不能自己唤醒自己/)
    assert.match(deny('agent_run_daily_cap'), /当天的运行次数到了上限/)
    assert.match(deny('agent_run_hourly'), /这一小时发起的运行次数到了上限/)
    assert.match(deny('agent_run_preset_gap'), /间隔太短/)
    assert.match(deny('agent_run_concurrency'), /同时在跑的运行太多/)
    assert.match(deny('run_active'), /上一次运行还没有结束/)
    assert.match(deny('conversation_live'), /此刻还在运行/)
    assert.match(deny('something_new'), /原因代码 something_new/)
    assert.match(deny(), /没有写明原因/)
  })
})
