// 运行：pnpm --dir frontend run test:unit
// 「待接取」数据流的接线防线（#180 B3b）：服务端 GET /work-items 带 offered → api 封装透传 → work store 采纳 → 侧栏会话卡读。
//
// 背景：第一版 e2e 里侧栏徽标始终不出现——服务端的 offered 没问题，页面也重新取了列表，却在 api 封装里被丢掉了
// （workItemsApi.list 把响应重新拼成一个固定字段的对象）。纯函数的单元测试看不到这类「中间一环漏传」，所以把每一环的接线守住。
// 行为本身见 workItems.test.ts（等待时长、快捷项匹配与计数），端到端见 scripts/e2e-terminal/e2e/work-board-notify.mjs。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'boardOffered.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const scriptOf = (src: string) => src.slice(0, src.indexOf('\n<template>'))
const templateOf = (src: string) => src.slice(src.indexOf('\n<template>'))

describe('待接取：每一环都接上了', () => {
  it('api 封装把响应里的 offered 透传出来（没带时统一成空数组）', () => {
    const api = read('api/workItems.ts')
    const list = api.slice(api.indexOf('async list(q: WorkItemListQuery'))
    const mapped = list.slice(0, list.indexOf('\n  },'))
    assert.match(mapped, /offered:\s*Array\.isArray\(data\.offered\)\s*\?\s*data\.offered\s*:\s*\[\]/, 'list() 把响应重拼成固定字段的对象，漏了 offered 侧栏徽标就永远不出现')
    assert.match(api, /offered:\s*WorkOffered\[\]/, '返回类型里要声明 offered')
  })

  it('work store：任务页加载与侧栏拉计数这两条路径都采纳 offered，且内容没变不换数组', () => {
    const store = read('stores/workItems.ts')
    assert.equal((store.match(/applyOffered\(res\.offered\)/g) || []).length, 2, 'load 与 refreshCounts 各调一次')
    const fn = store.slice(store.indexOf('function applyOffered'), store.indexOf('/** 刷新任务页数据'))
    assert.match(fn, /if \(!same\) offered\.value = list/, '没变化就不换数组，免得每次刷新都唤醒侧栏所有会话卡的依赖')
    assert.match(store, /offeredOf,/, 'offeredOf 要导出给侧栏会话卡用')
  })

  it('侧栏会话卡按工作流读 offered，并把徽标画在页脚；徽标自己读慢时钟，不当 prop 灌', () => {
    const card = read('components/sidebar/SidebarItemCard.vue')
    assert.match(scriptOf(card), /workStore\.offeredOf\(props\.item\.workflow_id\)/)
    assert.match(templateOf(card), /<SidebarOfferedBadge v-if="offered" :offered="offered" \/>/)
    const badge = read('components/sidebar/SidebarOfferedBadge.vue')
    assert.match(badge, /useCoarseNow\(\)/)
    assert.match(badge, /data-testid="sidebar-board-offered"/)
    assert.doesNotMatch(scriptOf(badge), /\bnow\b:/, '徽标不声明 now 这个 prop')
  })

  it('任务页：快捷项与行上的「已等」都读同一份 lib 逻辑，选中的快捷项没有任务时自动取消', () => {
    const page = read('components/work/WorkPage.vue')
    assert.match(page, /quickCounts\(store\.items, store\.filter\.workflowId\)/)
    assert.match(page, /store\.setQuick\(''\)/)
    const row = read('components/work/WorkTaskRow.vue')
    assert.match(row, /offerWaitView\(item\.value, clock\.value, notifyPrefs\.boardStaleMs\)/, '行上标红的时间读设置里的阈值，不是写死的 2 小时')
    assert.match(row, /data-testid="work-row-wait"/)
    const store = read('stores/workItems.ts')
    assert.match(store, /quick: filter\.quick,\s*viewer: filter\.workflowId/, 'scoped 要带上快捷筛选与 viewer')
  })
})

// 「指派久无人接」提醒的前端接线（#180 B3d）：设置卡 → 偏好 store → 服务端；任务行 / 回执口径读同一个阈值；提醒点开后的深链。
// 行为见 notifyPrefs.test.ts / workItems.test.ts / notifyBus.test.ts，端到端见 scripts/e2e-terminal/e2e/work-board-remind.mjs。
describe('久无人接提醒：设置与深链', () => {
  it('设置卡：开关与时长都走偏好 store，选项来自服务端给的默认值，不在界面里抄一份', () => {
    const card = read('components/settings/AppearanceSettings.vue')
    const script = scriptOf(card)
    assert.match(script, /boardStaleChoices\(notifyPrefs\.prefs\)/)
    assert.match(script, /boardStaleSelected\(notifyPrefs\.prefs\)/)
    assert.match(script, /notifyPrefs\.setBoardStaleMinutes\(Number\(/)
    const tpl = templateOf(card)
    assert.match(tpl, /data-testid="settings-board-stale"/)
    assert.match(tpl, /data-testid="settings-board-stale-toggle"/)
    assert.match(tpl, /data-testid="settings-board-stale-after"/)
    assert.match(tpl, /notifyPrefs\.setBoardStaleOff\(!notifyPrefs\.boardStaleOff\)/)
    assert.match(tpl, /:aria-pressed="!notifyPrefs\.boardStaleOff"/)
    // 开关按钮里不能再嵌可交互元素（下拉框在按钮外面）
    const toggle = tpl.slice(tpl.indexOf('data-testid="settings-board-stale-toggle"'))
    const toggleBlock = toggle.slice(0, toggle.indexOf('</button>'))
    assert.doesNotMatch(toggleBlock, /<select|<input|<button/)
  })

  it('偏好 store 的两个写入都乐观更新、失败回滚并提示；接口封装只带这四个可写字段', () => {
    const store = read('stores/notifyPrefs.ts')
    assert.match(store, /function setBoardStaleOff\(off: boolean\)/)
    assert.match(store, /function setBoardStaleMinutes\(minutes: number\)/)
    assert.match(store, /设置久无人接提醒失败/)
    assert.match(store, /设置久无人接的时长失败/)
    const api = read('api/notify.ts')
    assert.match(api, /board_stale_off\?:\s*boolean/)
    assert.match(api, /board_stale_minutes\?:\s*number/)
  })

  it('任务页深链：?quick= 只认已知的快捷项，等清单就绪再套，先去掉地址里的参数，换项目 / 离开页面就作废', () => {
    const page = scriptOf(read('components/work/WorkPage.vue'))
    assert.match(page, /async function consumeQuickQuery\(\)/)
    assert.match(page, /isWorkQuick\(v\)/, '不认识的取值只去掉参数')
    assert.match(page, /store\.loaded && !store\.loading && store\.filter\.workflowId === target/, '清单没取到之前套筛选会被 watch(quickChips) 立刻取消')
    assert.match(page, /const \{ quick: _consumed, \.\.\.rest \} = route\.query/, '只去掉 quick，其他参数原样保留')
    assert.match(page, /await router\.replace\(\{ name: 'work', params: \{ \.\.\.route\.params \}, query: rest \}\)/)
    assert.match(page, /watch\(\(\) => route\.query\.quick, consumeQuickQuery\)/)
    assert.match(page, /watch\(routeWorkflow, cancelQuickWait\)/)
    assert.match(page, /void consumeQuickQuery\(\)/, '直接打开地址（刷新 / 粘贴链接）也要处理')
    assert.match(page, /onBeforeUnmount\(\(\) => \{[^}]*cancelQuickWait\(\)/)
    // 没有可套的（已被人接走）就不套；套之前先清掉别的筛选
    assert.match(page, /c\.key === v && c\.count > 0\)\) return\s*\n\s*clearFilters\(\)\s*\n\s*store\.setQuick\(v\)/)
    // 取消的先判断再做：换地址后监听会再触发一次，那一次不能把正在等的取消掉
    const fn = page.slice(page.indexOf('async function consumeQuickQuery'))
    assert.ok(fn.indexOf('if (!v) return') < fn.indexOf('cancelQuickWait()'), '必须先判断没有 ?quick= 就返回，再取消旧的等待')
  })

  it('通知：board_stale 有自己的文案，深链走站内安全检查', () => {
    const bus = read('lib/notifyBus.ts')
    assert.match(bus, /case 'board_stale':/)
    assert.match(bus, /指派久无人接/)
  })
})
