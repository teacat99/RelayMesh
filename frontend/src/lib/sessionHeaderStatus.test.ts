// 运行：pnpm --dir frontend run test:unit
// 会话页头「会话级状态」（#120 · D-273 阶段 D）的源码级防线（纯逻辑见 sessionStatus.test.ts）：
//   1. 页头只用新徽标，旧的「直接显示最近一轮汇报状态」的写法（getStatusBadge / 手写 Badge）一个不剩；宽屏与手机各一处，条件互斥；
//   2. 徽标取数的口径：确认项只在 store 关注的就是这个工作流且已加载时才算、只数 needsAnswer 的；每半分钟重算；hook 状态与任务数来自现成的 store；
//      取数的几路没到齐之前不显示（刷新页面时不闪错的中间状态），出错到不了时 3 秒后照常显示；
//   3. 待处理清单：等回复的汇报用页头事件让列表滚过去，提问用 focusRef（先清空再赋值），文档决策去文档页；testid 齐全；
//   4. 页头是经典与预览共用的，徽标不引用任何预览专属组件；FeedbackView 把 jump-session 接到列表的 scrollToSession。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sessionHeaderStatus.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 模板里某个组件所有开标签的属性文本（跨行也行；属性值里没有 ">"）。 */
function tagAttrs(src: string, tag: string): string[] {
  return [...src.matchAll(new RegExp(`<${tag}\\b([^>]*)>`, 'g'))].map(m => m[1])
}
const templateOf = (src: string) => src.slice(src.indexOf('\n<template>'))
const squash = (s: string) => s.replace(/\s+/g, ' ').trim()

describe('会话页头：只用会话级状态徽标', () => {
  const header = read('components/feedback/SessionHeader.vue')
  const tpl = templateOf(header)

  it('旧写法不剩：没有 getStatusBadge、没有手写的 Badge、没有 Badge 的 import', () => {
    assert.doesNotMatch(header, /getStatusBadge/)
    assert.doesNotMatch(tpl, /<Badge\b/)
    assert.doesNotMatch(header, /import Badge from/)
    assert.doesNotMatch(tpl, /selectedSession\.status/)
  })

  it('SessionStatusBadge 宽屏一处、手机一处，条件互斥，都带工作流号与 jump-session 转发', () => {
    assert.match(header, /import SessionStatusBadge from '\.\/SessionStatusBadge\.vue'/)
    const all = tagAttrs(tpl, 'SessionStatusBadge').map(squash)
    assert.equal(all.length, 2, '宽屏与手机各渲染一次')
    assert.ok(all.some(a => a.includes('v-if="sessionWorkflowId && !isPhone"')), '宽屏那处：!isPhone')
    assert.ok(all.some(a => a.includes('v-if="sessionWorkflowId && isPhone"')), '手机那处：isPhone')
    for (const a of all) {
      assert.match(a, /:workflow-id="sessionWorkflowId"/)
      assert.match(a, /@jump-session="emit\('jump-session', \$event\)"/)
    }
  })

  it('页头声明了 jump-session 事件', () => {
    assert.match(header, /\(e: 'jump-session', sessionId: string\): void/)
  })

  it('FeedbackView 把 jump-session 接到当前列表（经典与预览同一组方法）的 scrollToSession', () => {
    const view = read('views/FeedbackView.vue')
    assert.match(squash(view), /<SessionHeader[^>]*@jump-session="messageListRef\?\.scrollToSession\(\$event\)"/)
  })
})

describe('会话状态徽标：取数口径', () => {
  const badge = read('components/feedback/SessionStatusBadge.vue')
  const script = badge.slice(0, badge.indexOf('\n<template>'))

  it('状态一律来自 deriveSessionStatus，入参取自现成的 store', () => {
    assert.match(script, /import \{ deriveSessionStatus \} from '\.\.\/\.\.\/lib\/sessionStatus'/)
    // 网页新建、智能体还没接入的会话（工作流里最新一行是占位会话）轮次状态翻成「待接入」（#174）
    assert.match(script, /roundStatus: workflowRoundStatus\(latestRow\.value, session\.value\?\.status \?\? ''\)/)
    assert.match(script, /pendingItems: items\.value\.length/)
    // 智能体最近的动作：收件箱轮询与会话列表聚合取较晚的一个（与侧栏会话卡同一口径，#159）
    assert.match(script, /agentActivityAt: agentActivityAt\.value/)
    assert.match(script, /laterIso\(sessionStore\.agentLastActivityAt, sessionStore\.workflowAggregates\[props\.workflowId\]\?\.agentActivityAt\)/)
    assert.match(script, /agentState: agentKey\.value/)
    assert.match(script, /openTasks: openTasks\.value/)
    assert.match(script, /nowMs: now\.value\.getTime\(\)/)
    assert.match(script, /agentStore\.entryFor\(props\.workflowId\)/)
    assert.match(script, /workStore\.workflows\.find\(w => w\.workflow_id === props\.workflowId\)\?\.active/)
  })

  it('确认项只在 store 关注的就是这个工作流、且已加载时才算；只数还要作答的', () => {
    assert.match(script, /confirmStore\.loaded && confirmStore\.workflowId === props\.workflowId/)
    assert.match(script, /confirmStore\.board\.items\.filter\(needsAnswer\)/)
  })

  it('每半分钟重算一次：智能体十分钟内的动作过期后不必等事件就落回空闲 / 已完成', () => {
    assert.match(script, /useNow\(\{ interval: 30_000 \}\)/)
  })

  it('数据没到齐之前不报结论：确认项 / 任务计数 / 收件箱 / Agent 状态四路都到了才显示，一路出错时 3 秒后照常显示', () => {
    assert.match(script, /workStore\.countsLoaded/)
    assert.match(script, /sessionStore\.inboxLoadedFor === props\.workflowId/)
    assert.match(script, /agentStore\.loadedAt > 0 \|\| !agentStore\.supported/)
    assert.match(squash(script), /sourcesReady\.value \|\| settled\.value \? deriveSessionStatus\(/)
    assert.match(script, /const SETTLE_MS = 3000/)
    assert.match(script, /onBeforeUnmount\(\(\) => \{\s*if \(settleTimer\) clearTimeout\(settleTimer\)/)
    // 页面里没有别处在拉的时候自己拉一次，已取到过就不重复
    assert.match(script, /agentStore\.ensureFresh\(\)/)
    assert.match(script, /if \(!workStore\.countsLoaded\) void workStore\.refreshCounts\(\)/)
  })

  it('两个 store 提供「到没到」的标记：任务计数取到过一次、收件箱数据属于哪个工作流（工作流变了、新数据没到时不算到了）', () => {
    const work = read('stores/workItems.ts')
    assert.equal((work.match(/countsLoaded\.value = true/g) ?? []).length, 2, 'load 与 refreshCounts 成功后都要置位')
    assert.match(work, /^\s+countsLoaded,$/m, '要从 store 导出')
    const sess = read('stores/session.ts')
    assert.match(sess, /inboxLoadedFor\.value = wId/)
    assert.match(sess, /inboxLoadedFor\.value = ''/)
    assert.match(sess, /^\s+inboxLoadedFor,$/m, '要从 store 导出')
  })

  it('不再「等待您」时收起面板，免得 v-model 停在 true、下次又有待处理时自己弹开', () => {
    assert.match(squash(script), /watch\( \(\) => status\.value\?\.key, key => \{ if \(key !== 'waiting'\) open\.value = false \}, \)/)
  })
})

describe('会话状态徽标：待处理清单', () => {
  const badge = read('components/feedback/SessionStatusBadge.vue')
  const script = badge.slice(0, badge.indexOf('\n<template>'))
  const tpl = templateOf(badge)

  it('只有「等待您」是可点的按钮（Popover），其余是只读 Badge；两处都带 testid / data-status-key / data-count', () => {
    assert.match(tpl, /<Popover v-if="status && status\.key === 'waiting'" v-model:open="open">/)
    assert.match(tpl, /<Badge\s+v-else-if="status"/)
    assert.equal((tpl.match(/data-testid="session-status-badge"/g) ?? []).length, 2)
    assert.equal((tpl.match(/:data-status-key="status\.key"/g) ?? []).length, 2)
    assert.equal((tpl.match(/:data-count="status\.count"/g) ?? []).length, 2)
  })

  it('清单里的条目 testid：汇报 / 提问与计划（带编号）/ 文档决策', () => {
    assert.match(tpl, /data-testid="session-status-popover"/)
    assert.match(tpl, /data-testid="session-status-report"/)
    assert.match(tpl, /:data-testid="`session-status-item-\$\{it\.ref\}`"/)
    assert.match(tpl, /:data-testid="`session-status-doc-\$\{g\.docPath \|\| g\.key\}`"/)
  })

  it('汇报条目抛 jump-session，提问条目走 focusRef（先清空再赋值，同一张卡连点两次也会再次定位），文档决策去文档页', () => {
    assert.match(script, /emit\('jump-session', id\)/)
    assert.match(script, /confirmStore\.focusRef = ''\s*\n\s*await nextTick\(\)\s*\n\s*confirmStore\.focusRef = ref/)
    assert.match(script, /navigateToDoc\(router, \{ workflowId: props\.workflowId, path: docPath \}\)/)
    assert.match(tpl, /@click="jumpReport"/)
    assert.match(tpl, /@click="jumpAsk\(it\.ref\)"/)
    assert.match(tpl, /@click="openDoc\(g\.docPath\)"/)
  })

  it('提问 / 计划与文档决策分开：决策按文档汇总，不在清单里逐条列', () => {
    assert.match(script, /items\.value\.filter\(i => i\.kind !== 'decision'\)/)
    assert.match(script, /groupDecisions\(items\.value\)/)
  })

  it('脉动小点尊重「减少动态效果」', () => {
    assert.equal((tpl.match(/motion-reduce:animate-none/g) ?? []).length, 2)
  })

  it('页头是经典与预览共用的：徽标与纯逻辑不引用预览专属组件，纯逻辑不依赖 vue', () => {
    assert.doesNotMatch(badge, /preview-view|Preview[A-Z]\w+\.vue/)
    const lib = read('lib/sessionStatus.ts')
    assert.doesNotMatch(lib, /from 'vue'|from 'pinia'/)
    assert.deepEqual([...lib.matchAll(/^import .* from '(.*)'$/gm)].map(m => m[1]), ['./inbox'])
  })
})
