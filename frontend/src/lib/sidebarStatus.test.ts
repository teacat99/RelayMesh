// 侧栏会话卡会话级状态的单元测试（#159，Q23-b；D-283）：
// 聚合怎么合并 / 校准、取数优先级、推出的状态（与页头同一套口径），以及接线的源码防线。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { stateView, type AgentSession } from './agentState'
import {
  SIDEBAR_WORKING_TEXT,
  laterIso,
  mergeWorkflowAggregates,
  patchPendingCount,
  resolveSidebarSources,
  sidebarSessionStatus,
  sidebarWorkingLook,
  type WorkflowAggregates,
} from './sidebarStatus'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sidebarStatus.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const NOW = Date.parse('2026-10-17T12:00:00Z')
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString()

describe('mergeWorkflowAggregates：把会话列表里的工作流级聚合并进表', () => {
  it('同工作流的多行只取一份；没有工作流编号 / 没带 pending_confirm_count 的行跳过', () => {
    const next = mergeWorkflowAggregates(
      {},
      [
        { workflow_id: 'wf-a', pending_confirm_count: 2, agent_last_activity_at: iso(60_000) },
        { workflow_id: 'wf-a', pending_confirm_count: 2, agent_last_activity_at: iso(60_000) },
        { workflow_id: 'wf-b', pending_confirm_count: 0 },
        { workflow_id: 'wf-old' }, // 旧服务端没有这两个字段
        { pending_confirm_count: 5 }, // 没有工作流编号
      ],
      NOW,
    )
    assert.deepEqual(Object.keys(next ?? {}).sort(), ['wf-a', 'wf-b'])
    assert.equal(next!['wf-a'].pendingConfirms, 2)
    assert.equal(next!['wf-a'].agentActivityAt, iso(60_000))
    assert.equal(next!['wf-a'].pendingPatchedAt, 0)
    // 带了 0 与没带要区分：0 是「确实没有待处理」
    assert.equal(next!['wf-b'].pendingConfirms, 0)
    assert.equal(next!['wf-b'].agentActivityAt, null)
  })

  it('没有变化返回 null（调用方不必替换响应式对象）', () => {
    const prev = mergeWorkflowAggregates({}, [{ workflow_id: 'wf-a', pending_confirm_count: 1 }], NOW)!
    assert.equal(mergeWorkflowAggregates(prev, [{ workflow_id: 'wf-a', pending_confirm_count: 1 }], NOW), null)
    const changed = mergeWorkflowAggregates(prev, [{ workflow_id: 'wf-a', pending_confirm_count: 3 }], NOW)
    assert.equal(changed?.['wf-a'].pendingConfirms, 3)
    assert.equal(prev['wf-a'].pendingConfirms, 1, '不改动传入的旧表')
  })

  it('请求在途期间事件已校准过的待处理条数以事件为准；请求之前校准的以列表为准', () => {
    const patched = patchPendingCount({}, 'wf-a', 0, NOW + 500)
    // 请求在 NOW 发出，事件在 NOW + 500 到达：列表里的旧快照（2 条）不能把它打回去
    assert.equal(mergeWorkflowAggregates(patched, [{ workflow_id: 'wf-a', pending_confirm_count: 2 }], NOW), null)
    // 请求在事件之后才发出：列表是更新的快照，覆盖
    const later = mergeWorkflowAggregates(patched, [{ workflow_id: 'wf-a', pending_confirm_count: 2 }], NOW + 1000)
    assert.equal(later?.['wf-a'].pendingConfirms, 2)
    assert.equal(later?.['wf-a'].pendingPatchedAt, 0)
  })

  it('智能体最近动作只往后走：取较晚的一个', () => {
    const prev: WorkflowAggregates = { 'wf-a': { pendingConfirms: 0, agentActivityAt: iso(1_000), pendingPatchedAt: 0 } }
    assert.equal(mergeWorkflowAggregates(prev, [{ workflow_id: 'wf-a', pending_confirm_count: 0, agent_last_activity_at: iso(90_000) }], NOW), null)
    const newer = mergeWorkflowAggregates(prev, [{ workflow_id: 'wf-a', pending_confirm_count: 0, agent_last_activity_at: iso(0) }], NOW)
    assert.equal(newer?.['wf-a'].agentActivityAt, iso(0))
    // 服务端重启后清空了记录（不再带这个字段）：不把已知的时间抹掉
    assert.equal(mergeWorkflowAggregates(prev, [{ workflow_id: 'wf-a', pending_confirm_count: 0 }], NOW), null)
  })
})

describe('patchPendingCount：confirm_updated 事件带来的权威条数', () => {
  it('覆盖条数、记下校准时间、保留已知的最近动作；负数 / 小数规整', () => {
    const prev: WorkflowAggregates = { 'wf-a': { pendingConfirms: 4, agentActivityAt: iso(1_000), pendingPatchedAt: 0 } }
    const next = patchPendingCount(prev, 'wf-a', 1, NOW)
    assert.deepEqual(next['wf-a'], { pendingConfirms: 1, agentActivityAt: iso(1_000), pendingPatchedAt: NOW })
    assert.equal(prev['wf-a'].pendingConfirms, 4, '不改动传入的旧表')
    assert.equal(patchPendingCount(prev, 'wf-a', -3, NOW)['wf-a'].pendingConfirms, 0)
    assert.equal(patchPendingCount(prev, 'wf-a', 2.9, NOW)['wf-a'].pendingConfirms, 2)
    // 列表里还没有的工作流也记下（事件先于列表到达）
    assert.equal(patchPendingCount({}, 'wf-new', 2, NOW)['wf-new'].agentActivityAt, null)
  })
})

describe('resolveSidebarSources：实时数据（页头同一批）优先，其次列表聚合，再没有按 0 / 无记录', () => {
  const baseline = { pendingConfirms: 3, agentActivityAt: iso(30_000) }
  it('页头正在看的工作流：用实时的待答复数与收件箱里的最近动作', () => {
    assert.deepEqual(resolveSidebarSources({ live: { confirms: 1, agentActivityAt: iso(5_000) }, baseline }), {
      pendingItems: 1,
      agentActivityAt: iso(5_000),
    })
  })
  it('实时数据只到了一部分：到了的用实时，没到的退回聚合', () => {
    assert.deepEqual(resolveSidebarSources({ live: { confirms: null, agentActivityAt: iso(5_000) }, baseline }), {
      pendingItems: 3,
      agentActivityAt: iso(5_000),
    })
    assert.deepEqual(resolveSidebarSources({ live: { confirms: 0, agentActivityAt: undefined }, baseline }), {
      pendingItems: 0,
      agentActivityAt: iso(30_000),
    })
  })
  it('智能体最近动作取实时与聚合里较晚的一个：谁知道得晚谁更准，没有记录（null）不会抹掉已知的时间', () => {
    // 聚合更晚（收件箱轮询还没刷新）→ 取聚合
    assert.equal(resolveSidebarSources({ live: { confirms: 0, agentActivityAt: iso(300_000) }, baseline }).agentActivityAt, iso(30_000))
    // 实时更晚 → 取实时
    assert.equal(resolveSidebarSources({ live: { confirms: 0, agentActivityAt: iso(1_000) }, baseline }).agentActivityAt, iso(1_000))
    // 实时「取到了但没有记录」→ 沿用聚合里已知的
    assert.equal(resolveSidebarSources({ live: { confirms: 0, agentActivityAt: null }, baseline }).agentActivityAt, iso(30_000))
    assert.equal(laterIso(undefined, undefined), null)
    assert.equal(laterIso(null, iso(1_000)), iso(1_000))
    assert.equal(laterIso('不是时间', iso(1_000)), iso(1_000))
  })
  it('别的工作流只有聚合；什么都没有时按「没有待处理、没有动作记录」', () => {
    assert.deepEqual(resolveSidebarSources({ baseline }), { pendingItems: 3, agentActivityAt: iso(30_000) })
    assert.deepEqual(resolveSidebarSources({}), { pendingItems: 0, agentActivityAt: null })
    assert.deepEqual(resolveSidebarSources({ baseline: { pendingConfirms: null, agentActivityAt: null } }), {
      pendingItems: 0,
      agentActivityAt: null,
    })
  })
})

describe('sidebarSessionStatus：与页头同一套推导', () => {
  const base = { agentState: null, openTasks: 0, nowMs: NOW }

  it('等回复的汇报 + 两条待答复的确认项 =「等待您 · 3 项」（侧栏不再是「等待确认」）', () => {
    const v = sidebarSessionStatus({ ...base, roundStatus: 'pending', baseline: { pendingConfirms: 2, agentActivityAt: null } })
    assert.equal(v?.key, 'waiting')
    assert.equal(v?.label, '等待您 · 3 项')
    assert.equal(v?.count, 3)
  })

  it('最近一轮已确认、但智能体十分钟内有动作 =「智能体进行中」（不再常驻「已完成」）', () => {
    const v = sidebarSessionStatus({ ...base, roundStatus: 'completed', baseline: { pendingConfirms: 0, agentActivityAt: iso(60_000) } })
    assert.equal(v?.key, 'working')
    assert.equal(v?.label, '智能体进行中')
  })

  it('装了 hook：以 hook 汇报的状态为准（工作中 → 进行中；已收尾 → 不算）', () => {
    const working = sidebarSessionStatus({ ...base, agentState: 'working', roundStatus: 'completed', baseline: { pendingConfirms: 0, agentActivityAt: null } })
    assert.equal(working?.key, 'working')
    const finished = sidebarSessionStatus({ ...base, agentState: 'idle', roundStatus: 'completed', baseline: { pendingConfirms: 0, agentActivityAt: iso(60_000) } })
    assert.equal(finished?.key, 'done')
  })

  it('没有近期动作：还有任务没结束 =「空闲」；任务都结束了 =「已完成」', () => {
    const baseline = { pendingConfirms: 0, agentActivityAt: iso(30 * 60_000) }
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'completed', openTasks: 2, baseline })?.key, 'idle')
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'completed', openTasks: 0, baseline })?.key, 'done')
  })

  it('已取消 / 已超时 / 已归档按最近一轮的结局原样显示（智能体在干活时让位给「进行中」）', () => {
    const quiet = { pendingConfirms: 0, agentActivityAt: null }
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'cancelled', baseline: quiet })?.label, '已取消')
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'timeout', baseline: quiet })?.label, '已超时')
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'archived', baseline: quiet })?.label, '已归档')
    assert.equal(
      sidebarSessionStatus({ ...base, roundStatus: 'cancelled', baseline: { pendingConfirms: 0, agentActivityAt: iso(1_000) } })?.key,
      'working',
    )
  })

  it('非选中工作流的待答复条数被事件校准到 0 后，「等待您」立刻落回', () => {
    let agg: WorkflowAggregates = mergeWorkflowAggregates({}, [{ workflow_id: 'wf-a', pending_confirm_count: 2 }], NOW)!
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'completed', baseline: agg['wf-a'] })?.key, 'waiting')
    agg = patchPendingCount(agg, 'wf-a', 0, NOW + 10)
    assert.equal(sidebarSessionStatus({ ...base, roundStatus: 'completed', baseline: agg['wf-a'] })?.key, 'done')
  })
})

describe('sidebarWorkingLook：侧栏「工作中」与页头状态徽标同口径（追加 #537）', () => {
  const T0 = Date.parse('2026-10-20T04:00:00Z')
  const agent = (p: Partial<AgentSession> = {}): AgentSession => ({
    id: 1,
    identity_id: 1,
    workflow_id: 'wf-a',
    conversation_id: 'conv-0123456789',
    identity_label: 'RelayMesh',
    host_name: 'devhost',
    project_directory: '/home/user/RelayMesh',
    last_event_at: new Date(T0 - 5_000).toISOString(),
    state: 'working',
    base_state: 'working',
    since: new Date(T0 - 120_000).toISOString(),
    quiet_seconds: 5,
    waiting_you: false,
    stalled: false,
    stalled_soft: false,
    approval_suspected: false,
    approval_overdue: false,
    ...p,
  })
  const look = (p?: Partial<AgentSession>) => sidebarWorkingLook(stateView(agent(p), T0))

  it('这个工作流还没有 hook 汇报（没有状态视图 / 状态未知）：保持原来的绿色「工作中」，没有问号', () => {
    for (const v of [null, undefined, stateView(null, T0)]) {
      assert.deepEqual(sidebarWorkingLook(v), { tone: 'active', text: SIDEBAR_WORKING_TEXT, flag: false, spinner: true, hint: '' })
    }
  })

  it('正常工作中：绿色「工作中」、转圈、没有问号，悬停不加说明', () => {
    assert.deepEqual(look(), { tone: 'active', text: '工作中', flag: false, spinner: true, hint: '' })
    // 「可能在等批准」是另一种提示，不改侧栏颜色（页头徽标也只对 stalled_soft 画问号）
    const approval = look({ sub: 'tool:Shell', approval_suspected: true, reason: 'tool_running' })
    assert.equal(approval.tone, 'active')
    assert.equal(approval.flag, false)
  })

  it('可能卡住（软标记 stalled_soft）：黄色「工作中」+ 问号，仍转圈，悬停写明原因（#537）', () => {
    const l = look({ sub: 'thinking', stalled_soft: true, reason: 'quiet_soft', quiet_seconds: 700 })
    assert.equal(l.tone, 'warn')
    assert.equal(l.text, '工作中')
    assert.equal(l.flag, true)
    assert.equal(l.spinner, true)
    assert.equal(l.hint, '提示：可能卡住')
    // token 计数停了但画面还在动：悬停里是更具体的原因
    assert.equal(look({ stalled_soft: true, reason: 'tokens_flat' }).hint, '提示：token 计数停了，但画面还在动，可能卡住')
  })

  it('疑似卡住（硬标记 stalled）：红色「疑似卡住」，不再转圈、不画问号，悬停带原因', () => {
    const l = look({ stalled: true, reason: 'quiet', quiet_seconds: 1900, last_event_at: new Date(T0 - 1_900_000).toISOString() })
    assert.equal(l.tone, 'danger')
    assert.equal(l.text, '疑似卡住')
    assert.equal(l.spinner, false)
    assert.equal(l.flag, false)
    assert.ok(l.hint.startsWith('疑似卡住：'), l.hint)
  })

  it('不是「工作中」的状态（空闲 / 等你批准……）不动：会话级状态是另一路来源，保持原样', () => {
    for (const p of [{ state: 'idle' }, { state: 'waiting_approval' }, { state: 'ended' }] as Partial<AgentSession>[]) {
      assert.deepEqual(look(p), { tone: 'active', text: '工作中', flag: false, spinner: true, hint: '' }, JSON.stringify(p))
    }
  })

  it('色调与页头徽标同源：「工作中」这一档里，侧栏色调等于 stateView 给的 tone（不会再各走各的）', () => {
    for (const p of [{}, { stalled_soft: true, reason: 'quiet_soft' }] as Partial<AgentSession>[]) {
      assert.equal(look(p).tone, stateView(agent(p), T0).tone, JSON.stringify(p))
    }
  })
})

describe('接线的源码防线（侧栏卡用会话级状态，聚合由 store 维护）', () => {
  it('SidebarItemCard 用 useSidebarSessionStatus 渲染带 data-testid 的会话级徽标；老会话（无工作流编号）才走本轮状态词', () => {
    const card = read('components/sidebar/SidebarItemCard.vue')
    assert.match(card, /useSidebarSessionStatus/)
    assert.match(card, /data-testid="sidebar-status"/)
    assert.match(card, /:data-status="sessionStatus\.key"/)
    assert.match(card, /usesSessionLevel/)
    assert.match(card, /<Badge v-else :variant="getStatusBadge\(item\)/)
  })

  it('「智能体进行中」在侧栏卡上画成输入区芯片同款的「● ⠶ 工作中」（追加 #264）：绿点 + 盲文旋转符 + 文字，没有外框、整行只留一个圆点（追加 #267）；Agent 状态小圆点（#113）照旧；页头徽标不动', () => {
    assert.equal(SIDEBAR_WORKING_TEXT, '工作中')
    const card = read('components/sidebar/SidebarItemCard.vue')
    assert.match(card, /const isWorking = computed\(\(\) => sessionStatus\.value\?\.key === 'working'\)/)
    // 工作中这一支：同一个 data-testid / data-status，点与字的色调取 workingLook（正常是芯片的 active 档：绿色脉动；#537 起可能卡住变黄、疑似卡住变红），
    // 旋转符与芯片同一个组件，文字取 workingLook.text（正常就是 SIDEBAR_WORKING_TEXT）
    assert.match(card, /v-if="sessionStatus && isWorking"/)
    assert.match(card, /data-status="working"/)
    assert.match(card, /dotToneClass\(workingLook\.tone, true\)/)
    assert.match(card, /<BrailleSpinner v-if="workingLook\.spinner" size="sm" class="text-primary"/)
    assert.match(card, /textToneClass\(workingLook\.tone\)/)
    assert.match(card, /\{\{ workingLook\.text \}\}/)
    assert.doesNotMatch(card, /textToneClass\('active'\)/, '颜色不能再写死成「工作中」的绿色（#537）')
    // 追加 #267：没有外框（不再是带边框 / 底色的小芯片）；这一行有 Agent 状态小圆点时芯片不再自带绿点（只留一个圆点），两处共用 agentDotShown
    const chipBlock = card.slice(card.indexOf('v-if="sessionStatus && isWorking"'), card.indexOf('v-else-if="sessionStatus"'))
    assert.ok(chipBlock.includes('data-testid="sidebar-status"'), '应截到「工作中」这一支')
    assert.doesNotMatch(chipBlock, /\bborder\b|bg-card|rounded-2xs|\bpx-1\b/)
    assert.match(chipBlock, /v-if="!hasAgentDot"/)
    assert.match(card, /agentDotShown\(agentState\.entryFor\(props\.item\.workflow_id\)\.primary\)/)
    assert.match(read('components/sidebar/AgentStateDot.vue'), /agentDotShown\(entry\.value\.primary\)/)
    // 其余状态仍是原来的徽标（v-else-if）；Agent 状态小圆点（#113，悬停写明是哪个 Agent）不因为工作中而收掉
    assert.match(card, /<Badge\s+v-else-if="sessionStatus"/)
    assert.match(card, /<AgentStateDot v-if="item\.type === 'feedback' && item\.workflow_id" :workflow-id="item\.workflow_id" \/>/)
    // 芯片本身（AgentStatusChip）的写法：点 + 旋转符 + 文字，用的是同一组函数
    const chip = read('components/feedback/AgentStatusChip.vue')
    assert.match(chip, /dotToneClass\(view\.tone, view\.pulse\)/)
    assert.match(chip, /return \{ icon: BrailleSpinner, cls: 'text-primary' \}/)
    assert.match(chip, /textToneClass\(view\.tone\)/)
    // 追加 #535（Q63 选 c）+ #582（仅对 hook 启用时生效）+ #601（每个会话各自独立）：这个会话的状态来源是 hook 时，芯片上不带时长
    // （有的会话有会话时长、有的没有，样式看着不一致）；本轮用时 / 会话时长只在点开的身份卡里。其他来源的会话维持
    // 「有就显示、没有就不占位」，判断集中在 chipSpanText，只看会话自己，不看整个 Hub 一份的收件箱设置
    assert.match(chip, /const chipSpan = computed\(\(\) => chipSpanText\(session\.value, now\.value\)\)/)
    assert.doesNotMatch(chip, /inboxConfig|hooksEnabled/, '芯片的时长判断不该依赖整个 Hub 一份的收件箱设置')
    assert.match(chip, /data-testid="agent-status-span"/)
    assert.match(chip, /data-testid="agent-card-turn"/)
    assert.match(chip, /data-testid="agent-card-session-span"/)
    // 页头徽标与会话级推导仍叫「智能体进行中」
    assert.match(read('lib/sessionStatus.ts'), /label: '智能体进行中'/)
  })

  it('取数的组合函数走 sidebarSessionStatus，并且数据没到齐前不显示（3 秒兜底全局只计一次时）', () => {
    const composable = read('composables/useSidebarSessionStatus.ts')
    assert.match(composable, /sidebarSessionStatus\(/)
    assert.match(composable, /SIDEBAR_STATUS_SETTLE_MS = 3000/)
    assert.match(composable, /if \(!settled\.value && !\(globalsReady && baseline\)\) return null/)
    assert.match(composable, /settleStarted/)
  })

  it('session store 在拉取列表 / 当前会话时并入聚合，confirm_updated 事件带 pending_count 时校准', () => {
    const store = read('stores/session.ts')
    assert.match(store, /absorbWorkflowAggregates\(res\.sessions, startedAt\)/)
    assert.match(store, /absorbWorkflowAggregates\(\[res\.session\], startedAt\)/)
    assert.match(store, /patchPendingCount\(workflowAggregates\.value, evt\.workflow_id, evt\.pending_count, Date\.now\(\)\)/)
    assert.match(store, /\n\s+workflowAggregates,\n/)
  })

  it('页头徽标与侧栏卡用同一个「较晚的最近动作」口径（两处不会各说各话）', () => {
    const header = read('components/feedback/SessionStatusBadge.vue')
    assert.match(header, /laterIso\(sessionStore\.agentLastActivityAt, sessionStore\.workflowAggregates\[props\.workflowId\]\?\.agentActivityAt\)/)
    assert.match(header, /agentActivityAt: agentActivityAt\.value/)
  })

  it('AppSidebar 挂载时让 Agent 状态保持新鲜（页头 / 工具栏没挂载的页面里也要有人取一次）', () => {
    const sidebar = read('components/AppSidebar.vue')
    assert.match(sidebar, /agentStateStore\.ensureFresh\(\)/)
  })
})
