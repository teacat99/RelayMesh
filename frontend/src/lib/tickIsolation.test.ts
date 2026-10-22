// 运行：pnpm --dir frontend run test:unit
// 「高频变化的模块各管各的刷新」的源码级防线（#215）。
//
// 背景：侧栏有几十张会话卡，原来 AppSidebar 每秒走一下 nowRef、当 prop 灌给每张卡——prop 一变，卡不管用没用到都整张重画；
// Agent 一秒来三四个事件，store 每次整体换掉会话数组，所有读它的卡又各重画一遍。实验 Hub 上量到：65 张卡时空闲每秒 65 次卡片更新，
// Agent 干活时每秒 350 次（任务耗时 55 → 205 ms/s）。改法：时钟由读它的小块自己订阅、走字的计时放进小组件，推导结果经 stableComputed，
// Agent 状态按工作流各自失效（行为见 stable.test.ts / agentTable.test.ts，这里只守住接线，防止回退到旧写法）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { approvalRemaining, runElapsed } from './terminal/execRuns'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'tickIsolation.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const scriptOf = (src: string) => src.slice(0, src.indexOf('\n<template>'))
const templateOf = (src: string) => src.slice(src.indexOf('\n<template>'))

describe('侧栏：每秒变化的时钟不再当 prop 灌给每张卡', () => {
  const bar = read('components/AppSidebar.vue')
  const card = read('components/sidebar/SidebarItemCard.vue')

  it('AppSidebar 没有自己的每秒定时器，也不给卡片传 now', () => {
    assert.doesNotMatch(bar, /nowRef/)
    assert.doesNotMatch(bar, /setInterval/)
    const tag = templateOf(bar).match(/<SidebarItemCard\b[\s\S]*?\/>/)
    assert.ok(tag, '找不到 SidebarItemCard 的用法')
    assert.doesNotMatch(tag[0], /:now\b|\bnow=/, '给每张卡灌一个每秒变的 prop，会让每张卡每秒整张重画')
  })

  it('SidebarItemCard 不声明 now 这个 prop，时钟自己读（共享的秒级时钟，卸载自动退订）', () => {
    const props = scriptOf(card).match(/defineProps<\{([\s\S]*?)\}>\(\)/)
    assert.ok(props, '找不到 defineProps')
    assert.doesNotMatch(props[1], /\bnow\b/)
    assert.doesNotMatch(card, /props\.now/)
    assert.match(card, /const now = useSharedNow\(\)/)
  })

  it('读时钟与 Agent 状态的推导（会话级状态、「工作中」外观）都经 stableComputed：结果没变就不唤醒这张卡', () => {
    const script = scriptOf(card)
    assert.match(script, /const sessionStatus = stableComputed\(/)
    assert.match(script, /const workingLook = stableComputed\(/)
    // 它们读的是 now.value，不是别处传进来的值
    assert.match(script, /sidebarStatus\.statusFor\(props\.item\.workflow_id as string, roundStatus\.value, now\.value\)/)
    assert.match(script, /stateView\(agentState\.entryFor\(wf\)\.primary, now\.value\)/)
  })

  it('走字的计时放进自己的小组件（SidebarItemTimer）：待答复的计时条与已提交的小标签各一处，卡片本身不再调用计时函数', () => {
    const tpl = templateOf(card)
    assert.match(tpl, /<SidebarItemTimer variant="bar"/)
    assert.match(tpl, /<SidebarItemTimer\s+v-if="[^"]*"\s+variant="chip"/)
    assert.doesNotMatch(card, /computeSessionTimer|getItemTimerInfo/)
    const timer = read('components/sidebar/SidebarItemTimer.vue')
    assert.match(timer, /useSessionTimer\(/)
    assert.match(timer, /onBeforeUnmount\(cleanup\)/)
    assert.match(timer, /const info = stableComputed\(/)
  })

  it('共享时钟：useSharedNow 登记订阅、作用域结束时退订，最后一个退订时停表', () => {
    const src = read('composables/useSessionTimer.ts')
    const fn = src.slice(src.indexOf('export function useSharedNow'), src.indexOf('export function useSessionTimer'))
    assert.match(fn, /startSharedClock\(\)/)
    assert.match(fn, /sharedRefSubscribers\+\+/)
    assert.match(fn, /onScopeDispose\(/)
    assert.match(fn, /sharedRefSubscribers--/)
    assert.match(fn, /stopSharedClock\(\)/)
  })
})

describe('Agent 状态 store：按工作流各自失效', () => {
  const store = read('stores/agentState.ts')

  it('会话放在按键响应的表里（lib/agentTable.ts），没有整张替换的扁平数组，也没有整体重算的 byWorkflow / identityById', () => {
    assert.match(store, /createAgentTable\(\)/)
    assert.doesNotMatch(store, /const sessions = ref/)
    assert.doesNotMatch(store, /sessions\.value\s*=/)
    assert.doesNotMatch(store, /groupByWorkflow|identityById/)
  })

  it('事件、重取、全量校准都走表的增量接口', () => {
    assert.match(store, /table\.patchEvent\(/)
    assert.match(store, /table\.replaceWorkflow\(/)
    assert.match(store, /table\.mergeOverview\(/)
    assert.match(store, /table\.relabelSessions\(/)
  })

  it('身份清单内容没变就不换数组；「是否取到过」用布尔 computed（loadedAt 每分钟刷新，不该唤醒读它的组件）', () => {
    assert.match(store, /if \(!deepEqual\(identities\.value, list\)\) identities\.value = list/)
    assert.match(store, /const loaded = computed\(\(\) => loadedAt\.value > 0\)/)
    for (const rel of ['composables/useSidebarSessionStatus.ts', 'components/feedback/SessionStatusBadge.vue']) {
      const src = read(rel)
      assert.match(src, /agentStore\.loaded\b/, `${rel} 读 loaded`)
      assert.doesNotMatch(src, /agentStore\.loadedAt/, `${rel} 不该直接读 loadedAt`)
    }
  })

  it('组件只通过 entryFor(自己的工作流) 读状态，不去碰整张表', () => {
    for (const rel of [
      'components/sidebar/SidebarItemCard.vue',
      'components/sidebar/AgentStateDot.vue',
      'components/feedback/AgentStatusChip.vue',
      'components/feedback/SessionStatusBadge.vue',
      'components/feedback/SessionExecChip.vue',
      'components/feedback/SessionQuickRun.vue',
      'composables/useSidebarSessionStatus.ts',
    ]) {
      const src = read(rel)
      assert.doesNotMatch(src, /\.byWorkflow\b|\.groups\b|agent[A-Za-z]*\.sessions\b/, `${rel} 不该直接读整张表`)
      assert.match(src, /entryFor\(/, `${rel} 应该用 entryFor 读`)
    }
  })
})

// #219：模型线程页（ExecMesh /exec/r/:key）。原来 ExecThreadView 每秒走一下 now、当 prop 灌给每张 RunCard：
// 一页 50 张卡（其中没有一条在跑）实验 Hub 上量到每秒 50 次整卡重画，只为了其中 0 ~ 2 张真的在走字。
describe('模型线程页：每秒的时钟不再当 prop 灌给每张命令卡', () => {
  const view = read('views/exec/ExecThreadView.vue')
  const card = read('components/exec/RunCard.vue')

  it('ExecThreadView 没有自己的每秒定时器，也不给 RunCard 传 now（页头的「N 分钟前起」读共享时钟）', () => {
    assert.doesNotMatch(scriptOf(view), /setInterval|clearInterval/)
    const tag = templateOf(view).match(/<RunCard\b[\s\S]*?\/>/)
    assert.ok(tag, '找不到 RunCard 的用法')
    assert.doesNotMatch(tag[0], /:now\b|\bnow=/, '给每张卡灌一个每秒变的 prop，会让每张卡每秒整张重画')
    assert.match(scriptOf(view), /const now = useSharedNow\(\)/)
  })

  it('RunCard 不声明 now 这个 prop；只有运行中 / 待批准的卡片才读共享时钟，已结束的卡片的计算属性里不读它', () => {
    const props = scriptOf(card).match(/defineProps<\{([\s\S]*?)\}>\(\)/)
    assert.ok(props, '找不到 defineProps')
    assert.doesNotMatch(props[1], /\bnow\b/)
    assert.doesNotMatch(card, /props\.now/)
    assert.match(card, /const sharedNow = useSharedNow\(\)/)
    assert.match(card, /const clockMs = \(\) => \(active\.value \? sharedNow\.value : 0\)/)
    assert.match(card, /const elapsed = computed\(\(\) => runElapsed\(props\.run, clockMs\(\)\)\)/)
    assert.match(card, /const remaining = computed\(\(\) => approvalRemaining\(props\.run, clockMs\(\)\)\)/)
  })

  it('已结束的运行，用时与时钟无关；不在待批准的运行没有倒计时——所以已结束的卡片不读时钟也不会显示错', () => {
    const started = '2026-10-21T10:00:00+08:00'
    for (const state of ['succeeded', 'failed', 'cancelled', 'timeout', 'lost']) {
      const r = { state, started_at: started, duration_ms: 1234, created_at: started }
      assert.equal(runElapsed(r, 0), 1234, `${state}：用时来自 duration_ms`)
      assert.equal(runElapsed(r, Date.now()), 1234, `${state}：与时钟无关`)
      assert.equal(approvalRemaining(r, 0), null, `${state}：没有倒计时`)
    }
    // 对照：运行中的用时随时钟走、待批准的倒计时随时钟走——这两种卡片必须读时钟
    const t0 = Date.parse(started)
    assert.equal(runElapsed({ state: 'running', started_at: started, duration_ms: -1 }, t0 + 5000), 5000)
    assert.equal(runElapsed({ state: 'running', started_at: started, duration_ms: -1 }, t0 + 9000), 9000)
    const w = { state: 'awaiting_approval', created_at: started }
    const a = approvalRemaining(w, t0 + 1000)
    const b = approvalRemaining(w, t0 + 6000)
    assert.ok(a !== null && b !== null && a - b === 5000, '待批准的倒计时随时钟递减')
  })
})

// #219：任务页（/work）。原来 WorkPage 每 30 秒走一下 now、当 prop 灌给 WorkTaskList，再灌给每一行 WorkTaskRow：
// 200 行时每 30 秒一次整页重画（列表组件本身 + 203 个组件在同一次刷新里更新），实验 Hub 上量到一次占主线程 28 ~ 66 ms。
describe('任务页：30 秒的时钟不再当 prop 灌给每一行任务', () => {
  const page = read('components/work/WorkPage.vue')
  const list = read('components/work/WorkTaskList.vue')
  const row = read('components/work/WorkTaskRow.vue')

  it('WorkPage 没有自己的定时器，不给 WorkTaskList 传 now；页面的 now 来自共享慢时钟（只给详情抽屉）', () => {
    assert.doesNotMatch(scriptOf(page), /setInterval|clearInterval/)
    const tag = templateOf(page).match(/<WorkTaskList\b[\s\S]*?\/>/)
    assert.ok(tag, '找不到 WorkTaskList 的用法')
    assert.doesNotMatch(tag[0], /:now\b|\bnow=/, '给整列任务行灌一个定时变的 prop，会让每一行在同一次刷新里整行重画')
    assert.match(scriptOf(page), /const now = useCoarseNow\(\)/)
  })

  it('WorkTaskList / WorkTaskRow 不声明 now 这个 prop，列表不再往行里传', () => {
    for (const [name, src] of [['WorkTaskList', list], ['WorkTaskRow', row]] as const) {
      const props = scriptOf(src).match(/defineProps<\{([\s\S]*?)\}>\(\)/)
      assert.ok(props, `${name}：找不到 defineProps`)
      assert.doesNotMatch(props[1], /\bnow\b/, `${name} 不该有 now`)
      assert.doesNotMatch(src, /props\.now/, `${name} 不该读 props.now`)
    }
    assert.doesNotMatch(templateOf(list), /:now\b|\bnow=/)
  })

  it('每一行自己读慢时钟，把「N 分钟前」放进 computed（标签没变就不唤醒这一行）；模板里不再直接算', () => {
    assert.match(scriptOf(row), /const clock = useCoarseNow\(\)/)
    assert.match(scriptOf(row), /const updatedLabel = computed\(\(\) => relativeTime\(item\.value\.updated_at, clock\.value\)\)/)
    assert.match(templateOf(row), /\{\{ updatedLabel \}\}/)
    assert.doesNotMatch(templateOf(row), /relativeTime\(/)
  })

  it('慢时钟的接线：共享、引用计数、卸载自动退订（行为见 composables/useCoarseNow.test.ts）', () => {
    const src = read('composables/useCoarseNow.ts')
    assert.match(src, /export const COARSE_NOW_INTERVAL_MS = 30_000/)
    assert.match(src, /onScopeDispose\(/)
    assert.match(src, /subscribers--/)
    assert.match(src, /clearInterval\(timer\)/)
  })
})
