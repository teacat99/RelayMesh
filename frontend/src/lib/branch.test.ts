// 运行：pnpm --dir frontend run test:unit
// 分支会话（#236）的界面逻辑：预设与开放范围的文案、「改参数」的档位与放宽判断（P3）、关系索引、侧栏分支树、页头标签、
// 新建分支的限额提示、会话页上的「具名卡」署名（谁说的、发给谁、对方读了没有）、事件行与预览文案、消息流的条目复用。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { BranchBrief, BranchExpose, BranchFeedItem, BranchParamEvent } from '../api/branches'
import {
  BRANCH_PRESETS,
  BRANCH_UNLOCK_REASON,
  HISTORY_OPTIONS,
  MAX_ACTIVE_BRANCHES,
  NEW_BRANCH_UNLOCK_REASON,
  PARAMS_LEVELS,
  PARAMS_UNLOCK_NOTE,
  arrangeBranchTree,
  branchAssignStatus,
  branchCardView,
  branchChipView,
  branchEventChip,
  branchEventText,
  branchLaunchStatus,
  branchPreview,
  canCreateBranch,
  clampedNotice,
  deliveryLabel,
  exposeSummary,
  feedItemKey,
  indexBranches,
  openTaskTotal,
  openTasksText,
  paramEventKind,
  paramEventTag,
  paramsLevelHint,
  paramsLevelLabel,
  paramsRank,
  paramsSummary,
  parseEscalateMinutes,
  presetLabel,
  returnedTasksText,
  reuseFeedItems,
  widensExpose,
} from './branch'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往仓库根目录找（找不到就返回 null，对应用例跳过）。 */
function goSource(rel: string): string | null {
  for (const base of [process.cwd(), join(process.cwd(), '..')]) {
    const p = join(base, rel)
    if (existsSync(p)) return readFileSync(p, 'utf8')
  }
  return null
}

const COLLAB: BranchExpose = { history: 'full', decide: true, say: true, params: 'resident', launch: false, tasks: 'assign' }

function rel(over: Partial<BranchBrief> & Pick<BranchBrief, 'branch_workflow_id' | 'parent_workflow_id'>): BranchBrief {
  return {
    id: 1,
    created_by: 'human',
    expose: COLLAB,
    copy_user_notes: true,
    status: 'active',
    created_at: '2026-10-24T01:00:00Z',
    title: over.branch_workflow_id,
    ...over,
  }
}

const item = (id: string, workflowId?: string) => ({ id, type: 'feedback', workflow_id: workflowId ?? id })

describe('预设与开放范围的文案', () => {
  it('三个预设各有名字和说明，说明不夸大（六项开放都已生效：读历史、发消息、定夺请示、改参数、启动、任务派单）', () => {
    assert.deepEqual(BRANCH_PRESETS.map(p => p.id), ['visible', 'collab', 'managed'])
    for (const p of BRANCH_PRESETS) assert.ok(p.hint.length > 10, p.id)
    assert.match(BRANCH_PRESETS[0].hint, /不能给分支发消息/)
    assert.match(BRANCH_PRESETS[0].hint, /不替分支定夺请示/)
    assert.match(BRANCH_PRESETS[0].hint, /不能改分支的参数、给它派任务/)
    assert.match(BRANCH_PRESETS[1].hint, /替分支定夺请示/)
    assert.match(BRANCH_PRESETS[1].hint, /改分支的常驻规则/)
    assert.match(BRANCH_PRESETS[1].hint, /你可以撤销/)
    assert.match(BRANCH_PRESETS[2].hint, /阶段、标题、唤醒词/)
    assert.match(BRANCH_PRESETS[2].hint, /关掉异步对话/)
    assert.doesNotMatch(BRANCH_PRESETS[2].hint, /会话参数/, '参数已经生效，不再写成「后续版本」')
    // 启动从 P4 起生效：托管（唯一开着启动的预设）要写明要先有续聊预设，不再把「启动与唤醒」留给后续版本
    assert.match(BRANCH_PRESETS[2].hint, /续聊预设启动/)
    assert.doesNotMatch(BRANCH_PRESETS[2].hint, /启动与唤醒/, '启动已经生效，不再写成「后续版本」')
    assert.doesNotMatch(BRANCH_PRESETS[0].hint + BRANCH_PRESETS[1].hint, /启动/, '仅可见与协作不开启动，说明里不提')
    // 任务派单从 P5 起生效：协作（与托管，包含协作的全部能力）开着，写明只限自己建的任务、要同一个项目的任务板；不再有「后续版本」
    assert.match(BRANCH_PRESETS[1].hint, /把自己建的任务派给分支/)
    assert.match(BRANCH_PRESETS[1].hint, /同一个项目的任务板/)
    assert.match(BRANCH_PRESETS[2].hint, /协作的全部能力/)
    for (const p of BRANCH_PRESETS) assert.doesNotMatch(p.hint, /后续版本/, `${p.id}：六项都已生效，不再写「后续版本」`)
  })

  it('presetLabel：去掉「（推荐）」，认不出的叫「自定义」', () => {
    assert.equal(presetLabel({ preset: 'visible' }), '仅可见')
    assert.equal(presetLabel({ preset: 'collab' }), '协作')
    assert.equal(presetLabel({ preset: 'managed' }), '托管')
    assert.equal(presetLabel({ preset: undefined }), '自定义')
  })

  it('exposeSummary：六项都列（读历史、发消息、定夺、改参数、启动、任务派单），与服务端 Summary 一字不差', () => {
    // 三个预设的摘要与 internal/model/branch_test.go 里钉住的逐字相同
    const VISIBLE: BranchExpose = { history: 'summary', decide: false, say: false, params: 'off', launch: false, tasks: 'read' }
    const MANAGED: BranchExpose = { ...COLLAB, params: 'all', launch: true }
    assert.equal(exposeSummary(VISIBLE), '读历史：摘要，不可发消息，不可定夺请示，不可改参数，不可启动，不可派任务')
    assert.equal(exposeSummary(COLLAB), '读历史：全文，可发消息，可定夺请示，可改参数：常驻规则，不可启动，可派任务')
    assert.equal(exposeSummary(MANAGED), '读历史：全文，可发消息，可定夺请示，可改参数：常驻规则、阶段、标题、唤醒词、异步开关，可启动，可派任务')
    assert.equal(exposeSummary({ ...COLLAB, tasks: 'read' }), '读历史：全文，可发消息，可定夺请示，可改参数：常驻规则，不可启动，不可派任务', '任务派单已经生效，开 / 关会反映在摘要里')
    assert.notEqual(exposeSummary({ ...COLLAB, launch: true }), exposeSummary(COLLAB), '启动已经生效，开 / 关会反映在摘要里')
    assert.equal(exposeSummary({ ...COLLAB, params: 'resident_phase' }), '读历史：全文，可发消息，可定夺请示，可改参数：常驻规则与阶段，不可启动，可派任务')
    assert.equal(exposeSummary({ ...COLLAB, params: 'bogus' as BranchExpose['params'] }), '读历史：全文，可发消息，可定夺请示，不可改参数，不可启动，可派任务', '不认识的档位按不可改（宁紧勿松）')
    assert.equal(exposeSummary({ ...COLLAB, tasks: 'bogus' as BranchExpose['tasks'] }), exposeSummary({ ...COLLAB, tasks: 'read' }), '任务只认 assign，别的取值一律按不可派（宁紧勿松）')
    assert.equal(exposeSummary({ ...COLLAB, decide: false }), '读历史：全文，可发消息，不可定夺请示，可改参数：常驻规则，不可启动，可派任务')
    assert.equal(exposeSummary({ ...COLLAB, history: 'summary', say: false, decide: false }), '读历史：摘要，不可发消息，不可定夺请示，可改参数：常驻规则，不可启动，可派任务')
    assert.equal(exposeSummary({ ...COLLAB, history: 'off', decide: false }), '不可读历史，可发消息，不可定夺请示，可改参数：常驻规则，不可启动，可派任务')
  })

  it('摘要里「可启动 / 不可启动」「可派任务 / 不可派任务」与服务端 Summary 逐字一致（读 Go 源码核对，防两边悄悄漂移）', { skip: goSource('internal/model/branch.go') === null }, () => {
    const go = goSource('internal/model/branch.go') ?? ''
    assert.match(go, /launch := "不可启动"/)
    assert.match(go, /launch = "可启动"/)
    assert.match(go, /return "可派任务"/)
    assert.match(go, /return "不可派任务"/)
    assert.ok(exposeSummary({ ...COLLAB, launch: true, tasks: 'read' }).endsWith('，可启动，不可派任务'))
    assert.ok(exposeSummary({ ...COLLAB, launch: false, tasks: 'assign' }).endsWith('，不可启动，可派任务'))
  })
})

describe('分支的状态：任务数与启动方式（#236 P4）', () => {
  it('openTaskTotal / openTasksText：只数没做完的三种状态，只写不为 0 的，措辞与侧栏任务入口一致', () => {
    assert.equal(openTaskTotal(undefined), 0)
    assert.equal(openTaskTotal(null), 0)
    assert.equal(openTaskTotal({}), 0)
    assert.equal(openTaskTotal({ todo: 3, doing: 2, review: 1 }), 6)
    assert.equal(openTasksText(undefined), '')
    assert.equal(openTasksText({}), '')
    assert.equal(openTasksText({ todo: 0, doing: 0, review: 0 }), '')
    assert.equal(openTasksText({ doing: 2, review: 1, todo: 3 }), '2 进行中 · 1 验证中 · 3 待办')
    assert.equal(openTasksText({ todo: 4 }), '4 待办', '侧栏在没有别的时才写待办；这里主会话要看清欠着多少，所以有就写')
    assert.equal(openTasksText({ review: 1, todo: 1 }), '1 验证中 · 1 待办')
  })

  it('branchLaunchStatus：没有 launch 字段是「未知」，不假装「没有预设」', () => {
    const st = branchLaunchStatus({ expose: COLLAB, launch: undefined })
    assert.equal(st.kind, 'unknown')
    assert.equal(st.label, '启动方式未知')
    assert.match(st.hint, /稍后再看/)
  })

  it('branchLaunchStatus：启动没开 → off（无预设时两步都写：先打开、再建预设；有预设时点名它们）', () => {
    const none = branchLaunchStatus({ expose: COLLAB, launch: { open: false, presets: 0 } })
    assert.equal(none.kind, 'off')
    assert.equal(none.label, '启动未开')
    assert.match(none.hint, /不能启动这个分支/)
    assert.match(none.hint, /「分支」面板里把「启动」打开/)
    assert.match(none.hint, /要再验证一次密码/)
    assert.match(none.hint, /点页头「运行」/, '没有预设时也要告诉用户去哪里建')
    assert.match(none.hint, /启用 Agent/)

    const some = branchLaunchStatus({ expose: COLLAB, launch: { open: false, presets: 2, preset: 'p1', name: '续聊' } })
    assert.equal(some.kind, 'off')
    assert.match(some.hint, /已有续聊预设「续聊」（共 2 条）可以唤醒这个分支/)
    assert.match(some.hint, /还不能运行它/)
    assert.doesNotMatch(some.hint, /点页头「运行」/, '已经有预设了，不再叫用户去建')
  })

  it('branchLaunchStatus：启动开了但没有预设 → no-preset，告诉用户去哪里建', () => {
    const st = branchLaunchStatus({ expose: { ...COLLAB, launch: true }, launch: { open: true, presets: 0 } })
    assert.equal(st.kind, 'no-preset')
    assert.equal(st.label, '启动已开 · 无预设')
    assert.match(st.hint, /没有可运行的东西/)
    assert.match(st.hint, /点页头「运行」/)
    assert.match(st.hint, /启用 Agent/)
  })

  it('branchLaunchStatus：启动开着也有预设 → ready；不说「此刻能运行」，只说怎么用与限制', () => {
    const one = branchLaunchStatus({ expose: { ...COLLAB, launch: true }, launch: { open: true, presets: 1, preset: 'p1', name: '续聊' } })
    assert.equal(one.kind, 'ready')
    assert.equal(one.label, '可启动')
    assert.match(one.hint, /preset_run 运行「续聊」唤醒这个分支/)
    assert.doesNotMatch(one.hint, /（共/, '只有一条预设不写条数')
    assert.match(one.hint, /主机在线、受频率与权限限制/)
    assert.match(one.hint, /关掉「启动」后，主会话下一次调用立刻被拒/)

    const many = branchLaunchStatus({ expose: { ...COLLAB, launch: true }, launch: { open: true, presets: 3, preset: 'p1', name: '甲' } })
    assert.equal(many.kind, 'ready')
    assert.equal(many.label, '可启动 · 3 条预设')
    assert.match(many.hint, /「甲」（共 3 条）/)
    // 不夸大：任何一种状态的文字都不能写成「现在就能运行」
    for (const st of [one, many]) assert.doesNotMatch(st.label + st.hint, /现在就能|马上可以|随时可以运行/)
  })

  it('预设名缺失时用「预设」代称（读不到名字也不留空洞）', () => {
    const st = branchLaunchStatus({ expose: { ...COLLAB, launch: true }, launch: { open: true, presets: 1 } })
    assert.match(st.hint, /preset_run 运行预设唤醒/)
  })
})

describe('分支的状态：任务派单（#236 P5）', () => {
  it('branchAssignStatus：关着 / 开着但不同板 / 开着且同板，各给芯片文字与下一步；说明里不夸大（不说「会叫醒它」）', () => {
    const off = branchAssignStatus({ assign: { open: false, board_shared: true }, expose: { ...COLLAB, tasks: 'read' } })
    assert.equal(off.kind, 'off')
    assert.equal(off.label, '不可派单')
    assert.match(off.hint, /「任务」是只读的/)
    assert.match(off.hint, /选「协作」或「托管」预设/)
    assert.match(off.hint, /主会话可以给分支派任务/, '指到逐项设置里真有的那个开关')
    assert.match(off.hint, /不需要再验证密码/, '打开派单不要密码，说在前头')

    const unshared = branchAssignStatus({ assign: { open: true, board_shared: false }, expose: COLLAB })
    assert.equal(unshared.kind, 'unshared')
    assert.equal(unshared.label, '可派单 · 不同板')
    assert.match(unshared.hint, /不在同一张任务板上/)
    assert.match(unshared.hint, /还没有会话/, '新建的分支要等里面的智能体连上来才有会话：两种原因都写')
    assert.match(unshared.hint, /登记进主会话所在的项目/)

    const ready = branchAssignStatus({ assign: { open: true, board_shared: true }, expose: COLLAB })
    assert.equal(ready.kind, 'ready')
    assert.equal(ready.label, '可派单')
    assert.match(ready.hint, /它自己建的任务/)
    assert.match(ready.hint, /由主会话验收/)
    assert.match(ready.hint, /不会叫醒分支的智能体/)
    assert.doesNotMatch(ready.hint, /(?<!不)(会|能)(叫醒|唤醒)/, '只有「不会叫醒」，没有「会叫醒」的说法')
  })

  it('branchAssignStatus：服务端没给 assign（关系很多时只给前面一批）——「任务」只读照样说准，其余按未知，不假装可用', () => {
    assert.equal(branchAssignStatus({ expose: { ...COLLAB, tasks: 'read' } }).kind, 'off')
    assert.equal(branchAssignStatus({ expose: { ...COLLAB, tasks: 'bogus' as BranchExpose['tasks'] } }).kind, 'off', '不认识的取值按只读（宁紧勿松）')
    const unknown = branchAssignStatus({ expose: COLLAB })
    assert.equal(unknown.kind, 'unknown')
    assert.equal(unknown.label, '派单状态未知')
    assert.notEqual(unknown.kind, 'ready', '看不出同不同板就不说「可派单」')
    assert.equal(branchAssignStatus({} as Pick<BranchBrief, 'assign' | 'expose'>).kind, 'unknown', '连开放范围都没有也不报错')
  })

  it('returnedTasksText：没有退回返回空串；有就写条数与序号（#12 #15），与服务端结束通知的写法一致', () => {
    assert.equal(returnedTasksText(undefined), '')
    assert.equal(returnedTasksText(null), '')
    assert.equal(returnedTasksText([]), '')
    assert.equal(returnedTasksText([7]), '它没做完的 1 个任务（#7）已退回主会话名下')
    assert.equal(returnedTasksText([12, 15, 3]), '它没做完的 3 个任务（#12 #15 #3）已退回主会话名下', '保持服务端给的顺序')
    assert.equal(returnedTasksText([0, -1, Number.NaN]), '', '不是任务序号的值不写')
  })

  it('派单的措辞与服务端 branch list 的 how 逐项对得上（读 Go 源码核对：同板的两种原因、不叫醒）', { skip: goSource('internal/mcp/tools_branch.go') === null }, () => {
    const go = goSource('internal/mcp/tools_branch.go') ?? ''
    assert.match(go, /不会因为派单被叫醒/)
    assert.match(go, /它还没有会话/)
    assert.match(go, /登记进你所在的项目/)
  })
})

describe('「改参数」：档位、放宽判断与说明（#236 P3）', () => {
  it('四档由低到高，数组下标就是档位高低；不认识的值按「不可改」', () => {
    assert.deepEqual(PARAMS_LEVELS.map(l => l.id), ['off', 'resident', 'resident_phase', 'all'])
    assert.deepEqual(PARAMS_LEVELS.map(l => paramsRank(l.id)), [0, 1, 2, 3])
    assert.equal(paramsRank('bogus'), 0)
    assert.equal(paramsRank(undefined), 0)
    assert.equal(paramsLevelLabel('resident_phase'), '常驻规则 + 阶段')
    assert.equal(paramsLevelLabel('bogus'), '不可改', '认不出的档位叫「不可改」，与摘要的「宁紧勿松」一致')
    assert.equal(paramsLevelHint('resident'), PARAMS_LEVELS[1].hint)
    assert.equal(paramsLevelHint('bogus'), PARAMS_LEVELS[0].hint)
    for (const l of PARAMS_LEVELS) assert.ok(l.hint.length > 8, l.id)
    // 说明里不夸大：异步只能关、授权档位 / 凭据 / 目录 / 主机永远不开放
    const all = PARAMS_LEVELS[3].hint
    assert.match(all, /只能关，不能替你打开/)
    assert.match(all, /授权档位、凭据、工作目录与主机永远不开放/)
  })

  it('paramsSummary 与服务端 ParamsSummary 逐字一致（读 Go 源码核对，防两边悄悄漂移）', { skip: goSource('internal/model/branch.go') === null }, () => {
    const go = goSource('internal/model/branch.go') ?? ''
    for (const level of ['off', 'resident', 'resident_phase', 'all']) {
      assert.ok(go.includes(`return "${paramsSummary(level)}"`), `服务端 ParamsSummary 里应有「${paramsSummary(level)}」（${level}）`)
    }
    // 档位的取值也要和服务端常量一致
    for (const [name, value] of [['ExposeParamsOff', 'off'], ['ExposeParamsResident', 'resident'], ['ExposeParamsResidentPhase', 'resident_phase'], ['ExposeParamsAll', 'all']]) {
      assert.match(go, new RegExp(`${name}\\s*=\\s*"${value}"`), name)
    }
    assert.equal(paramsSummary('bogus'), paramsSummary('off'))
    assert.equal(paramsSummary(undefined), paramsSummary('off'))
  })

  it('widensExpose：档位调高或「启动」从关到开才算放宽；收紧、不变和无关项都不算（与服务端 ExposeWidens 一致）', () => {
    const base: BranchExpose = { ...COLLAB, params: 'resident', launch: false }
    assert.equal(widensExpose(base, base), false)
    assert.equal(widensExpose(base, { ...base, params: 'resident_phase' }), true)
    assert.equal(widensExpose(base, { ...base, params: 'all' }), true)
    assert.equal(widensExpose(base, { ...base, params: 'off' }), false, '调低是收紧')
    assert.equal(widensExpose(base, { ...base, launch: true }), true)
    assert.equal(widensExpose({ ...base, launch: true }, { ...base, launch: false }), false, '关掉启动是收紧')
    assert.equal(widensExpose({ ...base, launch: true }, { ...base, launch: true }), false)
    assert.equal(widensExpose(base, { ...base, history: 'summary', say: false, decide: false }), false, '读历史 / 发消息 / 定夺只要登录')
    assert.equal(widensExpose({ ...base, say: false, decide: false }, { ...base, say: true, decide: true }), false)
    // 一边调高一边调低：只要有一项放宽就算放宽
    assert.equal(widensExpose({ ...base, launch: true }, { ...base, params: 'all', launch: false }), true)
    // 当前取值认不出时按「不可改」，任何一个真实档位都算放宽（宁紧勿松）
    assert.equal(widensExpose({ ...base, params: 'bogus' as BranchExpose['params'] }, { ...base, params: 'resident' }), true)
    // 新建分支的基线是 off：三个预设里，协作 / 托管都要解锁，仅可见不要
    const baseline: BranchExpose = { ...base, params: 'off', launch: false }
    assert.equal(widensExpose(baseline, { ...base, params: 'off' }), false)
    assert.equal(widensExpose(baseline, { ...base, params: 'resident' }), true)
    assert.equal(widensExpose(baseline, { ...base, params: 'all', launch: true }), true)
  })

  it('clampedNotice：写明哪些项没放宽、为什么；没有压回返回空串', () => {
    assert.equal(clampedNotice(undefined), '')
    assert.equal(clampedNotice([]), '')
    assert.match(clampedNotice(['params']), /^其余设置已保存，但「改参数」没有放宽/)
    assert.match(clampedNotice(['params', 'launch']), /「改参数」、「启动」没有放宽/)
    assert.match(clampedNotice(['launch']), /但「启动」没有放宽/)
    assert.match(clampedNotice(['params']), /终端功能现在不可用（需要先设置 Web 密码并开启终端功能）/)
    assert.match(clampedNotice(['mystery']), /「?mystery」?没有放宽|mystery/, '不认识的项原样写出，不吞掉')
  })

  it('读历史三档由浅到深，取值与服务端一致', () => {
    assert.deepEqual(HISTORY_OPTIONS.map(h => h.id), ['off', 'summary', 'full'])
    for (const h of HISTORY_OPTIONS) assert.ok(h.hint.length > 8 && h.label.length > 0, h.id)
    const go = goSource('internal/model/branch.go')
    if (go !== null) {
      for (const [name, value] of [['ExposeHistoryOff', 'off'], ['ExposeHistorySummary', 'summary'], ['ExposeHistoryFull', 'full']]) {
        assert.match(go, new RegExp(`${name}\\s*=\\s*"${value}"`), name)
      }
    }
  })

  it('说明文字：解锁提示写清谁要解锁、谁不要', () => {
    assert.match(PARAMS_UNLOCK_NOTE, /创建分支或把它调高时要再输入一次密码/)
    assert.match(PARAMS_UNLOCK_NOTE, /打开「启动」同样要/)
    assert.match(PARAMS_UNLOCK_NOTE, /调低、撤销与其余设置只要登录/)
    assert.match(BRANCH_UNLOCK_REASON, /需要再次验证密码/)
    assert.match(NEW_BRANCH_UNLOCK_REASON, /新建协作 \/ 托管分支.*需要再次验证密码/)
  })

  it('参数记录的三种身份：主会话写入且生效 / 已被撤销 / 撤销记录', () => {
    assert.equal(paramEventKind({}), 'agent')
    assert.equal(paramEventKind({ reverted_at: '2026-10-24T01:00:00Z' }), 'reverted')
    assert.equal(paramEventKind({ revert_of: 3 }), 'revert')
    assert.equal(paramEventKind({ revert_of: 3, reverted_at: 'x' }), 'revert', '撤销记录优先')
    assert.equal(paramEventTag({}), '主会话改')
    assert.equal(paramEventTag({ reverted_at: 'x' }), '已撤销')
    assert.equal(paramEventTag({ revert_of: 3 }), '你撤销')
    const ev: BranchParamEvent = { id: 1, created_at: 't', field: 'phase', field_label: '阶段', by: 'agent:wf-main', summary: '阶段 讨论 → 开发', told: false, revertable: true }
    assert.equal(paramEventKind(ev), 'agent')
  })
})

describe('关系索引 indexBranches', () => {
  it('只索引活动的关系；同一主会话的分支按建立时间升序', () => {
    const idx = indexBranches([
      rel({ branch_workflow_id: 'b2', parent_workflow_id: 'm', created_at: '2026-10-24T03:00:00Z' }),
      rel({ branch_workflow_id: 'b1', parent_workflow_id: 'm', created_at: '2026-10-24T02:00:00Z' }),
      rel({ branch_workflow_id: 'b3', parent_workflow_id: 'm', status: 'closed' }),
    ])
    assert.deepEqual([...idx.byBranch.keys()].sort(), ['b1', 'b2'])
    assert.deepEqual(idx.byParent.get('m')?.map(r => r.branch_workflow_id), ['b1', 'b2'])
    assert.equal(idx.byBranch.has('b3'), false, '已结束的分支回到普通会话的样子')
  })

  it('空输入得到空索引', () => {
    const idx = indexBranches([])
    assert.equal(idx.byBranch.size, 0)
    assert.equal(idx.byParent.size, 0)
  })
})

describe('侧栏分支树 arrangeBranchTree', () => {
  const idx = indexBranches([
    rel({ branch_workflow_id: 'b1', parent_workflow_id: 'm', created_at: '2026-10-24T02:00:00Z' }),
    rel({ branch_workflow_id: 'b2', parent_workflow_id: 'm', created_at: '2026-10-24T03:00:00Z' }),
  ])

  it('没有任何分支关系：返回原列表的拷贝，次序一字不变，不记缩进', () => {
    const items = [item('a'), item('b'), item('c')]
    const t = arrangeBranchTree(items, indexBranches([]))
    assert.notEqual(t.items, items)
    assert.deepEqual(t.items.map(i => i.id), ['a', 'b', 'c'])
    assert.equal(t.nested.size, 0)
    assert.equal(t.childCount.size, 0)
  })

  it('分支和主会话都在列表里：分支紧跟在主会话后面缩进，哪怕它在原列表里排在主会话前面', () => {
    const items = [item('x'), item('b2'), item('y'), item('m'), item('z'), item('b1')]
    const t = arrangeBranchTree(items, idx)
    assert.deepEqual(t.items.map(i => i.id), ['x', 'y', 'm', 'b2', 'b1', 'z'], '分支保持它们在原列表里的相对次序')
    assert.deepEqual([...t.nested].sort(), ['b1', 'b2'])
    assert.equal(t.childCount.get('m'), 2)
  })

  it('不修改传入的列表', () => {
    const items = [item('b1'), item('m')]
    const before = items.map(i => i.id)
    arrangeBranchTree(items, idx)
    assert.deepEqual(items.map(i => i.id), before)
  })

  it('主会话被过滤掉了：分支按自己的位置显示，不缩进', () => {
    const t = arrangeBranchTree([item('x'), item('b1'), item('y')], idx)
    assert.deepEqual(t.items.map(i => i.id), ['x', 'b1', 'y'])
    assert.equal(t.nested.size, 0)
    assert.equal(t.childCount.size, 0)
  })

  it('折叠的主会话：不显示分支，但仍计数（界面据此写「N 个分支」）', () => {
    const t = arrangeBranchTree([item('m'), item('b1'), item('b2'), item('z')], idx, new Set(['m']))
    assert.deepEqual(t.items.map(i => i.id), ['m', 'z'])
    assert.equal(t.nested.size, 0)
    assert.equal(t.childCount.get('m'), 2)
  })

  it('已结束的分支不再缩进（索引里就没有它）', () => {
    const closed = indexBranches([rel({ branch_workflow_id: 'b1', parent_workflow_id: 'm', status: 'closed' })])
    const t = arrangeBranchTree([item('b1'), item('m')], closed)
    assert.deepEqual(t.items.map(i => i.id), ['b1', 'm'])
    assert.equal(t.nested.size, 0)
  })

  it('深度只有一层：主会话自己又是别人的活动分支时，它的分支不再嵌套（防御脏数据）', () => {
    const dirty = indexBranches([
      rel({ branch_workflow_id: 'm', parent_workflow_id: 'top' }),
      rel({ branch_workflow_id: 'b1', parent_workflow_id: 'm' }),
    ])
    const t = arrangeBranchTree([item('top'), item('m'), item('b1')], dirty)
    assert.deepEqual(t.items.map(i => i.id), ['top', 'm', 'b1'])
    assert.equal(t.nested.has('m'), true)
    assert.equal(t.nested.has('b1'), false, '第三层不缩进')
  })

  it('任务条目与没有工作流编号的条目不参与挂靠', () => {
    const t = arrangeBranchTree(
      [{ id: 'b1', type: 'task', workflow_id: 'b1' }, item('m'), { id: 'legacy', type: 'feedback' }],
      idx,
    )
    assert.deepEqual(t.items.map(i => i.id), ['b1', 'm', 'legacy'])
    assert.equal(t.nested.size, 0)
  })
})

describe('页头标签 branchChipView', () => {
  it('分支：写明主会话（点一下回到主会话）；已结束的分支另写', () => {
    const active = branchChipView(rel({ branch_workflow_id: 'b', parent_workflow_id: 'm', parent_title: '支付重构' }), 0)
    assert.equal(active?.kind, 'branch')
    assert.equal(active?.text, '分支 · 主会话「支付重构」')
    assert.match(active?.title ?? '', /点一下回到主会话/)
    const closed = branchChipView(rel({ branch_workflow_id: 'b', parent_workflow_id: 'm', parent_title: '支付重构', status: 'closed' }), 0)
    assert.equal(closed?.text, '已结束的分支 · 主会话「支付重构」')
    assert.match(closed?.title ?? '', /已结束的分支/)
  })

  it('主会话标题拿不到时退回工作流编号', () => {
    assert.equal(branchChipView(rel({ branch_workflow_id: 'b', parent_workflow_id: 'wf-main' }), 0)?.text, '分支 · 主会话「wf-main」')
  })

  it('主会话：有活动分支才出现「N 个分支」；没有或只剩已结束的不出现', () => {
    const chip = branchChipView(undefined, 3)
    assert.deepEqual([chip?.kind, chip?.text], ['main', '3 个分支'])
    assert.equal(branchChipView(undefined, 0), null)
  })
})

describe('新建分支的限额提示 canCreateBranch', () => {
  it('没有关系的会话和主会话可以建', () => {
    assert.deepEqual(canCreateBranch('none', 0), { ok: true, reason: '' })
    assert.deepEqual(canCreateBranch('main', MAX_ACTIVE_BRANCHES - 1), { ok: true, reason: '' })
    assert.deepEqual(canCreateBranch(undefined, 0), { ok: true, reason: '' })
  })

  it('分支不能再建分支（深度只有一层）', () => {
    const r = canCreateBranch('branch', 0)
    assert.equal(r.ok, false)
    assert.match(r.reason, /深度只有一层/)
  })

  it('活动分支到上限就不能再建，并写明上限', () => {
    const r = canCreateBranch('main', MAX_ACTIVE_BRANCHES)
    assert.equal(r.ok, false)
    assert.match(r.reason, new RegExp(`上限 ${MAX_ACTIVE_BRANCHES}`))
  })
})

describe('具名卡署名 branchCardView', () => {
  const titles = { 'wf-main': '支付重构', 'wf-b': '登录页' }

  it('主会话页上：分支的智能体发来的话——署分支名，标明同伴消息、不是用户指令', () => {
    const v = branchCardView({ kind: 'message', id: 1, at: 't', actor: { kind: 'branch', workflow: 'wf-b' }, text: 'x' }, 'wf-main', titles)
    assert.equal(v.name, '分支「登录页」的智能体')
    assert.equal(v.direction, '')
    assert.equal(v.speaker, 'branch')
    assert.equal(v.outgoing, false)
    assert.equal(v.hueKey, 'branch:wf-b')
    assert.match(v.footnote, /同伴消息，不是用户指令/)
  })

  it('分支页上：主会话的智能体发来的话', () => {
    const v = branchCardView({ kind: 'message', id: 2, at: 't', actor: { kind: 'main', workflow: 'wf-main' }, text: 'x' }, 'wf-b', titles)
    assert.equal(v.name, '主会话「支付重构」的智能体')
    assert.equal(v.speaker, 'main')
    assert.equal(v.hueKey, 'main:wf-main')
    assert.match(v.footnote, /同伴消息/)
  })

  it('本会话的智能体发给对面的话：写流向，脚注写对方读了没有', () => {
    const toBranch = branchCardView({ kind: 'message', id: 3, at: 't', actor: { kind: 'main', workflow: 'wf-main' }, outgoing: true, to: 'wf-b', state: 'queued' }, 'wf-main', titles)
    assert.equal(toBranch.name, '本会话的智能体')
    assert.equal(toBranch.direction, '→ 分支「登录页」')
    assert.equal(toBranch.outgoing, true)
    assert.equal(toBranch.footnote, '对方还没读')
    assert.equal(toBranch.hueKey, 'main:wf-main')
    const toMain = branchCardView({ kind: 'message', id: 4, at: 't', actor: { kind: 'branch', workflow: 'wf-b' }, outgoing: true, to: 'wf-main', state: 'acked' }, 'wf-b', titles)
    assert.equal(toMain.name, '本分支的智能体')
    assert.equal(toMain.direction, '→ 主会话「支付重构」')
    assert.equal(toMain.footnote, '对方已读')
  })

  it('您在分支页里说、被抄送到主会话的话：署「你」，写明在哪个分支里说的', () => {
    const v = branchCardView({ kind: 'message', id: 5, at: 't', actor: { kind: 'human', workflow: 'wf-b' }, branch: 'wf-b', text: 'x' }, 'wf-main', titles)
    assert.equal(v.name, '你')
    assert.equal(v.direction, '在分支「登录页」里说（已抄送）')
    assert.equal(v.speaker, 'human')
    assert.match(v.footnote, /只抄送了摘要/)
  })

  it('标题拿不到时退回消息自带的发言者标题，再退回工作流编号', () => {
    const own = branchCardView({ kind: 'message', id: 6, at: 't', actor: { kind: 'branch', workflow: 'wf-x' }, actor_title: '自带标题' }, 'wf-main', {})
    assert.equal(own.name, '分支「自带标题」的智能体')
    const bare = branchCardView({ kind: 'message', id: 7, at: 't', actor: { kind: 'branch', workflow: 'wf-x' } }, 'wf-main', {})
    assert.equal(bare.name, '分支「wf-x」的智能体')
  })

  it('系统消息：署「系统」，没有脚注', () => {
    const v = branchCardView({ kind: 'message', id: 8, at: 't', actor: { kind: 'system' } }, 'wf-main', titles)
    assert.deepEqual([v.name, v.speaker, v.footnote], ['系统', 'system', ''])
  })

  it('同一个会话的话颜色种子相同，不同会话不同', () => {
    const a = branchCardView({ kind: 'message', id: 1, at: 't', actor: { kind: 'branch', workflow: 'wf-b' } }, 'wf-main', titles)
    const a2 = branchCardView({ kind: 'message', id: 9, at: 't', actor: { kind: 'branch', workflow: 'wf-b' } }, 'wf-main', titles)
    const c = branchCardView({ kind: 'message', id: 10, at: 't', actor: { kind: 'branch', workflow: 'wf-c' } }, 'wf-main', titles)
    assert.equal(a.hueKey, a2.hueKey)
    assert.notEqual(a.hueKey, c.hueKey)
  })
})

describe('送达脚注 deliveryLabel', () => {
  it('queued 还没读；delivered / acked 已读；expired 过期；未知按没读', () => {
    assert.equal(deliveryLabel('queued'), '对方还没读')
    assert.equal(deliveryLabel('delivered'), '对方已读')
    assert.equal(deliveryLabel('acked'), '对方已读')
    assert.equal(deliveryLabel('expired'), '已过期，对方没来得及读')
    assert.equal(deliveryLabel(undefined), '对方还没读')
  })
})

describe('事件行与预览文案', () => {
  const titles = { 'wf-b': '登录页' }
  const event = (over: Partial<BranchFeedItem>): BranchFeedItem => ({ kind: 'event', at: 't', actor: { kind: 'system' }, event: 'created', branch: 'wf-b', ...over })

  it('branchEventText：建立写明谁建的，结束写明后果', () => {
    assert.equal(branchEventText(event({ by: 'human' }), titles), '分支「登录页」已建立（你建立）')
    assert.equal(branchEventText(event({ by: 'migrate' }), titles), '分支「登录页」已建立（由迁移命令建立）')
    assert.equal(branchEventText(event({ by: 'agent:wf-main' }), titles), '分支「登录页」已建立（由主会话的智能体建立）')
    assert.equal(branchEventText(event({}), titles), '分支「登录页」已建立')
    assert.match(branchEventText(event({ event: 'closed' }), titles), /已结束：两边不能再互发消息/)
  })

  it('branchEventText：请示的转交（主会话转 / 超时 / 分支结束）与主会话代答（#236 P2）', () => {
    const esc = (over: Partial<BranchFeedItem>) => event({ event: 'escalated', ref: 'Q7', ref_title: '要不要上线', ...over })
    assert.equal(
      branchEventText(esc({ by: 'agent:wf-main', text: '涉及预算' }), titles),
      '主会话把分支「登录页」的请示 Q7「要不要上线」 转交给你：涉及预算',
    )
    assert.equal(branchEventText(esc({ by: 'agent:wf-main' }), titles), '主会话把分支「登录页」的请示 Q7「要不要上线」 转交给你')
    assert.equal(
      branchEventText(esc({ by: 'timeout', text: '主会话 30 分钟没有答复，自动转交给你' }), titles),
      '分支「登录页」的请示 Q7「要不要上线」：主会话 30 分钟没有答复，自动转交给你',
    )
    assert.equal(
      branchEventText(esc({ by: 'system', text: '分支已结束，请示改由你定夺' }), titles),
      '分支「登录页」的请示 Q7「要不要上线」：分支已结束，请示改由你定夺',
    )
    assert.equal(branchEventText(esc({ by: 'system' }), titles), '分支「登录页」的请示 Q7「要不要上线」：已转交给你定夺')
    assert.equal(
      branchEventText(event({ event: 'main_answered', ref: 'Q8', ref_title: '选方案', by: 'agent:wf-main' }), titles),
      '主会话的智能体代为定夺了分支「登录页」的请示 Q8「选方案」（不是你本人的答复）',
    )
    // 缺编号 / 标题时不写出 undefined
    assert.equal(branchEventText(event({ event: 'main_answered' }), titles), '主会话的智能体代为定夺了分支「登录页」的请示 请示（不是你本人的答复）')
    // 请示送出：取代原先主会话收件箱里那条给智能体看的、带工具用法的请示正文
    assert.equal(
      branchEventText(event({ event: 'ask_routed', ref: 'Q9', ref_title: '先做缓存还是索引' }), titles),
      '分支「登录页」的请示 Q9「先做缓存还是索引」 先交给主会话的智能体定夺（你也可以随时直接答复）',
    )
  })

  it('branchEventText：主会话改参数与你的撤销（#236 P3，说明句由服务端写好，已撤销的补一句）', () => {
    const changed = (over: Partial<BranchFeedItem> = {}) => event({ event: 'params_changed', id: 5, field: 'phase', by: 'agent:wf-main', text: '阶段 讨论 → 开发', ...over })
    assert.equal(branchEventText(changed(), titles), '主会话的智能体改了分支「登录页」的参数：阶段 讨论 → 开发')
    assert.equal(branchEventText(changed({ reverted: true }), titles), '主会话的智能体改了分支「登录页」的参数：阶段 讨论 → 开发（已被你撤销）')
    assert.equal(branchEventText(changed({ text: '' }), titles), '主会话的智能体改了分支「登录页」的参数', '没有说明时不写出冒号和 undefined')
    assert.equal(
      branchEventText(event({ event: 'params_reverted', id: 6, by: 'human', text: '用户撤销了主会话对「阶段」的修改（阶段 讨论 → 开发）' }), titles),
      '分支「登录页」：用户撤销了主会话对「阶段」的修改（阶段 讨论 → 开发）',
    )
    assert.equal(branchEventText(event({ event: 'params_reverted' }), titles), '分支「登录页」：你撤销了主会话的一次参数修改')
  })

  it('branchEventChip：参数事件写「参数」，其余写「分支」', () => {
    assert.equal(branchEventChip({ event: 'params_changed' }), '参数')
    assert.equal(branchEventChip({ event: 'params_reverted' }), '参数')
    for (const ev of ['created', 'closed', 'ask_routed', 'escalated', 'main_answered', undefined]) assert.equal(branchEventChip({ event: ev }), '分支', String(ev))
  })

  it('branchPreview：事件、消息、发出的消息（带箭头）、过长截断、空文本回退', () => {
    assert.equal(branchPreview(event({})), '分支已建立')
    assert.equal(branchPreview(event({ event: 'closed' })), '分支已结束')
    assert.equal(branchPreview(event({ event: 'ask_routed' })), '请示交给主会话定夺')
    assert.equal(branchPreview(event({ event: 'escalated' })), '请示已转交给你')
    assert.equal(branchPreview(event({ event: 'main_answered' })), '主会话代答了一项请示')
    assert.equal(branchPreview(event({ event: 'params_changed' })), '主会话改了分支的参数')
    assert.equal(branchPreview(event({ event: 'params_reverted' })), '参数修改已撤销')
    const msg = (text: string, over: Partial<BranchFeedItem> = {}): BranchFeedItem => ({ kind: 'message', at: 't', actor: { kind: 'branch', workflow: 'wf-b' }, text, ...over })
    assert.equal(branchPreview(msg('接口  已\n就绪')), '接口 已 就绪')
    assert.equal(branchPreview(msg('收到', { outgoing: true })), '→ 收到')
    assert.equal(branchPreview(msg('')), '分支消息')
    assert.equal(branchPreview(msg('字'.repeat(80))), `${'字'.repeat(60)}…`)
  })
})

describe('消息流的条目复用', () => {
  const m = (id: number, text: string, over: Partial<BranchFeedItem> = {}): BranchFeedItem => ({ kind: 'message', id, at: `2026-10-24T0${id}:00:00Z`, actor: { kind: 'branch', workflow: 'wf-b' }, text, state: 'queued', ...over })

  it('feedItemKey：消息按行号，事件按（种类，分支）', () => {
    assert.equal(feedItemKey(m(3, 'x')), 'branch-m3')
    assert.equal(feedItemKey({ kind: 'event', at: 't', actor: { kind: 'system' }, event: 'closed', branch: 'wf-b' }), 'branch-e-closed-wf-b')
  })

  it('请示的转交 / 代答事件按（种类，分支，请示编号）分键：同一个分支的几条不会互相顶掉', () => {
    const e = (ref: string, text: string): BranchFeedItem => ({ kind: 'event', at: 't', actor: { kind: 'system' }, event: 'escalated', branch: 'wf-b', ref, text })
    assert.equal(feedItemKey(e('Q1', 'a')), 'branch-e-escalated-wf-b-Q1')
    assert.notEqual(feedItemKey(e('Q1', 'a')), feedItemKey(e('Q2', 'a')))
    const prev = [e('Q1', 'a'), e('Q2', 'b')]
    const next = reuseFeedItems(prev, [e('Q1', 'a'), e('Q2', 'b')])
    assert.equal(next, prev, '内容没变时整体复用')
    const changed = reuseFeedItems(prev, [e('Q1', 'a'), { ...e('Q2', 'b'), ref_title: '改了标题' }])
    assert.equal(changed[0], prev[0])
    assert.notEqual(changed[1], prev[1], '标题变了要换成新对象')
  })

  it('参数事件按（种类，分支，记录编号）分键：同一个分支改很多次也不会互相顶掉；撤销状态翻转时换成新对象（#236 P3）', () => {
    const pe = (id: number, over: Partial<BranchFeedItem> = {}): BranchFeedItem => ({
      kind: 'event', id, at: `2026-10-24T0${id}:00:00Z`, actor: { kind: 'system' }, event: 'params_changed', branch: 'wf-b', by: 'agent:wf-main', field: 'phase', text: '阶段 讨论 → 开发', revertable: true, ...over,
    })
    assert.equal(feedItemKey(pe(4)), 'branch-e-params_changed-wf-b-p4')
    assert.notEqual(feedItemKey(pe(4)), feedItemKey(pe(5)))
    assert.notEqual(feedItemKey(pe(4)), feedItemKey(pe(4, { event: 'params_reverted' })), '改与撤销是两种事件')
    const prev = [pe(4), pe(5)]
    assert.equal(reuseFeedItems(prev, [pe(4), pe(5)]), prev, '内容没变时整体复用')
    const afterRevert = reuseFeedItems(prev, [pe(4), pe(5, { reverted: true, revertable: false })])
    assert.equal(afterRevert[0], prev[0])
    assert.notEqual(afterRevert[1], prev[1], '已撤销的状态变了要换成新对象')
    const lost = reuseFeedItems(prev, [pe(4, { revertable: false }), pe(5)])
    assert.notEqual(lost[0], prev[0], '「还能不能撤销」变了也要换成新对象')
    const revertRow = reuseFeedItems(prev, [pe(4), pe(5, { field: 'title' })])
    assert.notEqual(revertRow[1], prev[1], '改的字段不同也不是同一条')
    const other = reuseFeedItems([pe(7, { event: 'params_reverted', revertable: false })], [pe(7, { event: 'params_reverted', revertable: false, revert_of: 5 })])
    assert.equal(other[0].revert_of, 5, '指向被撤销记录的编号变了要换成新对象')
  })

  it('一条都没变：数组也换回上一次的', () => {
    const prev = [m(1, 'a'), m(2, 'b')]
    const next = reuseFeedItems(prev, [m(1, 'a'), m(2, 'b')])
    assert.equal(next, prev)
  })

  it('没变的条目沿用旧对象，变了的（送达状态翻转）换成新的，新增的追加', () => {
    const prev = [m(1, 'a'), m(2, 'b')]
    const fresh = [m(1, 'a'), m(2, 'b', { state: 'delivered' }), m(3, 'c')]
    const next = reuseFeedItems(prev, fresh)
    assert.notEqual(next, prev)
    assert.equal(next[0], prev[0], '没变的沿用旧对象')
    assert.equal(next[1], fresh[1], '状态变了换成新对象')
    assert.equal(next[2], fresh[2])
    assert.equal(next.length, 3)
  })

  it('条数变少（被清理）时不返回旧数组', () => {
    const prev = [m(1, 'a'), m(2, 'b')]
    const next = reuseFeedItems(prev, [m(1, 'a')])
    assert.notEqual(next, prev)
    assert.equal(next.length, 1)
    assert.equal(next[0], prev[0])
  })
})

describe('决策路由偏好：超时转交分钟数的解析', () => {
  it('留空 = 恢复默认（0）；范围内的整数原样给出，首尾空白忽略', () => {
    assert.deepEqual(parseEscalateMinutes('', 5, 1440), { ok: true, value: 0 })
    assert.deepEqual(parseEscalateMinutes('   ', 5, 1440), { ok: true, value: 0 })
    assert.deepEqual(parseEscalateMinutes(' 45 ', 5, 1440), { ok: true, value: 45 })
    assert.deepEqual(parseEscalateMinutes('5', 5, 1440), { ok: true, value: 5 })
    assert.deepEqual(parseEscalateMinutes('1440', 5, 1440), { ok: true, value: 1440 })
  })

  it('越界与非整数给出能看懂的原因，不提交', () => {
    assert.deepEqual(parseEscalateMinutes('4', 5, 1440), { ok: false, reason: '要在 5–1440 分钟之间' })
    assert.deepEqual(parseEscalateMinutes('1441', 5, 1440), { ok: false, reason: '要在 5–1440 分钟之间' })
    assert.deepEqual(parseEscalateMinutes('0', 5, 1440), { ok: false, reason: '要在 5–1440 分钟之间' })
    for (const bad of ['abc', '1.5', '-5', '3e2', '１２']) {
      assert.deepEqual(parseEscalateMinutes(bad, 5, 1440), { ok: false, reason: '请填整数分钟数' }, bad)
    }
  })
})
