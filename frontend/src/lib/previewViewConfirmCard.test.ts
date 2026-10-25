// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 C3「待处理的提问 / 计划确认换成统一消息卡」（#120 · D-273）的源码级防线：
//   1. 列表只把「还要您处理」的确认项交给新卡（needsAnswer），已处理的仍走 32px 事件行；新卡排在兜底的 TimelineCardEntry 之前；
//   2. 新卡的结构：统一骨架、类型 / 风险 / 「待您处理」状态、⋯ 菜单、正文不限高、作答区（与经典同一个 ConfirmChoice、同样的入参）、
//      脚注（编号 · 引用 · 两段式忽略）；经典 testid 命名一个没丢；
//   3. 作答动作、失败提示与本机草稿、聚焦高亮来自两个 composable，经典卡与新卡共用，经典卡里不再有自己的一份；
//   4. 经典的确认卡 / 时间线卡的标记不变，经典与共享叶子（含两个 composable）不引用预览专属组件。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { CARD_PANEL_CLASS, CARD_TYPES } from './messageCard'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewConfirmCard.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const PV = 'components/feedback/preview-view/'

/** 模板里某个组件所有开标签的属性文本（跨行也行；属性值里没有 ">"）。 */
function tagAttrs(src: string, tag: string): string[] {
  return [...src.matchAll(new RegExp(`<${tag}\\b([^>]*)>`, 'g'))].map(m => m[1])
}
/** 只看模板部分（脚本头注释里也会提到组件名与类名）。 */
const templateOf = (src: string) => src.slice(src.indexOf('\n<template>'))
const squash = (s: string) => s.replace(/\s+/g, ' ').trim()

describe('预览视图阶段 C3：列表的接线', () => {
  const list = read(`${PV}PreviewMessageList.vue`)
  const tpl = templateOf(list)

  it('待处理的提问 / 计划交给 PreviewConfirmCard：只一处渲染，排在兜底的事件行之前', () => {
    assert.match(list, /import PreviewConfirmCard from '\.\/PreviewConfirmCard\.vue'/)
    const all = tagAttrs(tpl, 'PreviewConfirmCard')
    assert.equal(all.length, 1, 'PreviewConfirmCard 应恰好渲染一次')
    assert.match(all[0], /v-else-if="block\.entry\.kind === 'confirm' && needsAnswer\(block\.entry\.item\)"/)
    assert.match(all[0], /:item="block\.entry\.item"/)
    assert.match(all[0], /:workflow-id="confirmStore\.workflowId"/)
    assert.ok(tpl.indexOf('<PreviewConfirmCard') < tpl.indexOf('<TimelineCardEntry v-else'), '确认卡分支必须在兜底的事件行之前')
    assert.match(list, /^\s+needsAnswer,$/m, 'needsAnswer 来自 lib/timeline')
  })

  it('已处理的确认项仍是事件行：兜底的 TimelineCardEntry 还在（它固定渲染事件行，已没有 compact 参数）', () => {
    const entries = tagAttrs(tpl, 'TimelineCardEntry')
    assert.equal(entries.length, 2)
    for (const a of entries) assert.doesNotMatch(a, /(^|\s):?compact(\s|=|$)/)
    assert.match(read('components/feedback/TimelineCardEntry.vue'), /<ConfirmTimelineCard\b[^>]*\bcompact\b/)
  })
})

describe('预览视图阶段 C3：PreviewConfirmCard 的结构', () => {
  const src = read(`${PV}PreviewConfirmCard.vue`)
  const tpl = templateOf(src)
  const classic = read('components/confirms/ConfirmCard.vue')

  it('外层是经典的定位容器标记，卡根上保留 confirm-<编号> / data-kind / data-risk', () => {
    const outer = tpl.match(/<div ref="rootEl"([^>]*)>/)
    assert.ok(outer, '找不到外层定位容器')
    assert.match(outer[1], /:data-testid="`confirm-tl-\$\{item\.ref\}`"/)
    assert.match(outer[1], /data-status="pending"/)
    const [attrs] = tagAttrs(tpl, 'PreviewMessageCard')
    assert.match(attrs, /:type="item\.kind"/)
    assert.match(attrs, /:title="item\.title"/)
    assert.match(attrs, /:title-testid="`\$\{testid\}-title`"/, '标题位沿用经典的 confirm-<编号>-title')
    assert.match(attrs, /:time="cardTimeText\(item\.created_at\)"/)
    assert.match(attrs, /:time-title="item\.created_at"/)
    assert.match(attrs, /:data-testid="testid"/)
    assert.match(attrs, /:data-kind="item\.kind"/)
    assert.match(attrs, /:data-risk="item\.risk"/)
    assert.match(src, /const testid = computed\(\(\) => `confirm-\$\{props\.item\.ref\}`\)/)
  })

  it('被工具栏点名时的高亮是 ring（不与根上的 border-border 抢颜色）', () => {
    assert.match(tpl, /:class="focused \? 'ring-2 ring-primary\/50' : ''"/)
    assert.ok(!/border-primary/.test(tpl))
  })

  it('状态槽：风险标签（共享几何）+「待您处理」脉动小点，色调随提问 / 计划；先送主会话的请示（#236 P2）换成不脉动的「待主会话」', () => {
    // 悬停说明走 riskHint：L2 多一句「按约定，智能体在您明确批准前不会执行这一步」（#217，原先只有经典卡有，#166 删经典时接过来）
    assert.match(tpl, /<span :class="\[CARD_CHIP_BASE, riskClass\]" :title="riskHint\(item\.risk\)" :data-testid="`\$\{testid\}-risk`">\{\{ RISK_SHORT\[item\.risk\] \|\| item\.risk \}\}<\/span>/)
    assert.match(tpl, /<EventStatus v-else :tone="statusTone" pulse>\{\{ STATUS_WORD\.pending \}\}<\/EventStatus>/)
    assert.match(tpl, /<EventStatus v-if="routed" tone="violet">\{\{ STATUS_WORD\.routed \}\}<\/EventStatus>/)
    assert.match(src, /const statusTone = computed\(\(\) => CARD_TYPES\[props\.item\.kind\]\.tone\)/)
    assert.equal(CARD_TYPES.ask.tone, 'amber')
    assert.equal(CARD_TYPES.plan.tone, 'red')
    // 手机让位：只留 L2，L1 / L0 让位给标题（与事件行一致）
    assert.match(src, /if \(tone === 'danger'\) return 'border-destructive\/50 text-destructive'/)
    assert.match(src, /if \(tone === 'warn'\) return 'border-amber-500\/40 text-amber-700 max-sm:hidden dark:text-amber-300'/)
    assert.match(src, /return 'border-border text-muted-foreground max-sm:hidden'/)
  })

  it('⋯ 菜单：复制正文 / 复制编号，统一走 copyWithToast，卡里不直接碰 toast / copyText', () => {
    assert.match(tpl, /<PreviewCardMenu :items="menuItems" \/>/)
    assert.match(src, /key: 'copy-body', label: '复制正文', onSelect: \(\) => void copyWithToast\(props\.item\.body \|\| props\.item\.title, '正文'\)/)
    assert.match(src, /key: 'copy-ref', label: '复制编号', onSelect: \(\) => void copyWithToast\(props\.item\.ref, '编号'\)/)
    assert.match(src, /import \{ copyWithToast \} from '\.\/cardCopy'/)
    assert.ok(!/\bcopyText\b|\btoast\b/.test(src.replace(/copyWithToast/g, '')), '卡里不该直接调 copyText / toast（提示在 composable 与 cardCopy 里）')
  })

  it('正文：关联入口 + Markdown 全文，不限高、不折叠；计划多一句只读说明', () => {
    assert.match(tpl, /<ConfirmLinks :item="item" :workflow-id="workflowId" :testid="testid" \/>/)
    const body = tpl.match(/<div v-if="item\.body" ([^>]*)>/)
    assert.ok(body, '找不到正文容器')
    assert.match(body[1], /:data-testid="`\$\{testid\}-body`"/)
    assert.ok(!/max-h-|overflow-|line-clamp/.test(body[1]), '正文不限高（经典是 max-h-56 的框内滚动）')
    assert.match(tpl, /<PreviewMarkdown :content="item\.body" :workflow-id="workflowId" \/>/) // 正文走 PreviewMarkdown（Markdown 全文 + Artifact 引用卡，阶段 E-a）
    assert.match(tpl, /<p v-if="item\.kind === 'plan'"[^>]*>\s*<FileText class="size-3" \/>批准之前，智能体只会做只读的准备工作。/)
  })

  it('作答区：同一个 ConfirmChoice、入参与经典逐项相同，套一块取自色条色调的面板', () => {
    const [attrs] = tagAttrs(tpl, 'ConfirmChoice')
    const [classicAttrs] = tagAttrs(templateOf(classic), 'ConfirmChoice')
    assert.ok(attrs && classicAttrs)
    assert.equal(squash(attrs), squash(classicAttrs), '新卡给 ConfirmChoice 的入参 / 事件必须与经典 ConfirmCard 一致')
    assert.match(tpl, /<div :class="\[CARD_PANEL_CLASS, 'p-2\.5'\]" :data-testid="`\$\{testid\}-answer`">/)
    assert.match(CARD_PANEL_CLASS, /var\(--card-tone\)/)
    assert.ok(!/#[0-9a-fA-F]{6}/.test(CARD_PANEL_CLASS) && !/#[0-9a-fA-F]{6}/.test(src), '颜色只在 lib/messageCard.ts 里定义')
  })

  it('脚注：编号 · 引用 · 两段式忽略，testid 与经典命名一致，手机上按钮撑到 44px', () => {
    assert.match(tpl, /:data-testid="`\$\{testid\}-ref`">\{\{ item\.ref \}\}<\/span>/)
    assert.match(tpl, /<ReplyRefButton :token="confirmRefToken\(item\.ref\)" title="引用这张确认卡：在输入框插入标记，智能体读到时会附上标题、选项与正文开头" \/>/)
    assert.match(tpl, /<span v-if="confirmDismiss" class="text-\[11px\] text-muted-foreground">忽略后会告诉智能体不用再等它。<\/span>/)
    assert.match(tpl, /:data-testid="`\$\{testid\}-dismiss-cancel`"\s+@click="confirmDismiss = false"/)
    assert.match(tpl, /:disabled="busy"\s+:data-testid="`\$\{testid\}-dismiss`"\s+@click="dismiss"/)
    assert.match(tpl, /\{\{ confirmDismiss \? '确认忽略' : '忽略' \}\}/)
    assert.equal((tpl.match(/max-md:min-h-11/g) ?? []).length, 2, '两个忽略按钮在手机上都要 44px 高')
  })

  it('没有把经典卡整个搬进来：不导入 ConfirmCard / ConfirmTimelineCard', () => {
    assert.ok(!/import ConfirmCard\b|import ConfirmTimelineCard\b/.test(src))
  })
})

describe('预览视图阶段 C3：作答动作与聚焦高亮只有一份（composables）', () => {
  const answer = read('composables/useConfirmAnswer.ts')
  const focus = read('composables/useConfirmFocus.ts')
  const classicCard = read('components/confirms/ConfirmCard.vue')
  const classicTimeline = read('components/confirms/ConfirmTimelineCard.vue')
  const previewCard = read(`${PV}PreviewConfirmCard.vue`)

  it('经典卡与新卡都用 useConfirmAnswer，经典卡里不再有自己的一份提交 / 采用 / 忽略', () => {
    for (const [name, src] of [['ConfirmCard', classicCard], ['PreviewConfirmCard', previewCard]] as const) {
      assert.match(src, /const \{ busy, draftScope, confirmDismiss, submit, adopt, dismiss \} = useConfirmAnswer\(\(\) => props\.item, \(\) => props\.workflowId\)/, `${name} 应使用 useConfirmAnswer`)
    }
    for (const gone of ['store.answer(', 'store.dismiss(', 'toast', 'clearLocalDraft', 'recommendedIds', 'confirmErrorMessage']) {
      assert.ok(!classicCard.includes(gone), `经典 ConfirmCard 里不该再有 ${gone}（已搬进 composables/useConfirmAnswer）`)
    }
  })

  it('useConfirmAnswer：提交 / 采用推荐 / 忽略成功后清本机草稿并提示，失败只提示原因、卡片保持原样', () => {
    assert.match(answer, /const busy = computed\(\(\) => store\.busy\.has\(toValue\(item\)\.ref\) \|\| store\.busy\.has\('\*'\)\)/)
    assert.match(answer, /const draftScope = computed\(\(\) => draftScopes\.confirm\(toValue\(workflowId\), toValue\(item\)\.ref\)\)/)
    // 第四个参数是「其他（自定义）」里写的答复（#183），没写就不带
    assert.match(answer, /const done = await store\.answer\(toValue\(item\)\.ref, options, note \|\| undefined, custom \|\| undefined\)\s+clearLocalDraft\(scope\)\s+toast\.success\(`已答复 \$\{done\.ref\}`, \{ description: answerText\(done, 80\) \|\| undefined, duration: 2500 \}\)/)
    assert.match(answer, /if \(!ids\.length\) return/)
    assert.match(answer, /const done = await store\.answer\(toValue\(item\)\.ref, ids\)\s+clearLocalDraft\(scope\)\s+toast\.success\(`已采用推荐 \$\{done\.ref\}`/)
    assert.match(answer, /const done = await store\.dismiss\(toValue\(item\)\.ref\)\s+clearLocalDraft\(scope\)\s+toast\.success\(`已忽略 \$\{done\.ref\}`, \{ description: '会告诉智能体不用再等它', duration: 2500 \}\)/)
    assert.equal((answer.match(/toast\.error\(confirmErrorMessage\(err\)\)/g) ?? []).length, 3, '三个动作失败时都提示原因')
    // 忽略是两段式：第一击只置 confirmDismiss，第二击才调接口
    assert.match(answer, /if \(!confirmDismiss\.value\) \{\s+confirmDismiss\.value = true\s+return\s+\}\s+confirmDismiss\.value = false/)
    assert.match(answer, /return \{ busy, draftScope, confirmDismiss, submit, adopt, dismiss \}/)
  })

  it('经典时间线卡与新卡都用 useConfirmFocus；高亮 2.5 秒、立即生效、结束后清 focusRef', () => {
    assert.match(classicTimeline, /const focused = useConfirmFocus\(\(\) => props\.item\.ref, rootEl\)/)
    assert.match(previewCard, /const focused = useConfirmFocus\(\(\) => props\.item\.ref, rootEl\)/)
    assert.ok(!classicTimeline.includes('focusTimer'), '经典时间线卡里不再有自己的一份定时器')
    assert.match(focus, /export const CONFIRM_FOCUS_MS = 2500/)
    assert.match(focus, /scrollIntoView\(\{ behavior: 'smooth', block: 'center' \}\)/)
    assert.match(focus, /if \(store\.focusRef === toValue\(itemRef\)\) store\.focusRef = ''/)
    assert.match(focus, /\{ immediate: true \}/)
  })

  it('点名时卡还没有根节点（点名之后才挂载）：记下来、挂载好再滚一次；点名已过期（focusRef 被清）就不滚', () => {
    // 回归：卡在 setup 里（immediate 回调）没有根节点，以前只亮外框、不滚动；它最后在不在视口里全看列表补出更早轮次时滚动位置碰巧在哪儿
    assert.match(focus, /import \{ onMounted, ref, toValue, watch,/)
    assert.match(focus, /if \(rootEl\.value\) reveal\(\)\s*\n\s*else scrollOnMount = true/)
    assert.match(focus, /onMounted\(\(\) => \{\s*\n\s*if \(!scrollOnMount\) return\s*\n\s*scrollOnMount = false\s*\n[^\n]*\n\s*if \(store\.focusRef === toValue\(itemRef\)\) reveal\(\)/)
    // 滚动只有 reveal 一处出口（不能出现第二种滚法，两边的对齐方式要一致：居中）
    assert.equal([...focus.matchAll(/scrollIntoView\(/g)].length, 1)
  })

  it('点名滚动之后要做落地校准（#253）：平滑滚动起跑 + settle(居中)，不能只剩一个裸 scrollIntoView', () => {
    // 回归：裸 scrollIntoView 的终点在起跑时算死，途中离屏占位变成真实高度、总高变了，点跳转滚到半路就停（实验室实测落在离卡数千 px 处）
    assert.match(focus, /import \{ scrollParentOf, useScrollSettle \} from '\.\/useScrollSettle'/)
    assert.match(focus, /const \{ settle \} = useScrollSettle\(\)/)
    assert.match(
      focus,
      /el\.scrollIntoView\(\{ behavior: 'smooth', block: 'center' \}\)\s*\n\s*settle\(scrollParentOf\(el\), el, \{ align: 'center' \}\)/,
    )
    // 校准驱动本身在共享 composable 里，居中对齐是它的一个选项；默认仍是顶边对齐（右侧轨 / 搜索 / 滚到某一轮不受影响）
    const settleSrc = read('composables/useScrollSettle.ts')
    assert.match(settleSrc, /const align = opts\.align \?\? 'start'/)
    assert.match(settleSrc, /itemHeight: rect\.height/)
    assert.match(settleSrc, /export function scrollParentOf\(/)
  })

  it('经典与共享叶子（含两个 composable）不引用预览专属组件', () => {
    const FILES = [
      'composables/useConfirmAnswer.ts', 'composables/useConfirmFocus.ts',
      'components/confirms/ConfirmCard.vue', 'components/confirms/ConfirmTimelineCard.vue', 'components/confirms/ConfirmChoice.vue', 'components/confirms/ConfirmLinks.vue',
    ]
    for (const rel of FILES) {
      assert.ok(!/preview-view\/|Preview[A-Z][A-Za-z]*Card|PreviewCardMenu|PreviewAttachments|cardCopy/.test(read(rel)), `${rel} 不该引用预览专属组件`)
    }
  })
})

describe('预览视图阶段 C3：经典确认卡的标记一字不动', () => {
  const classic = read('components/confirms/ConfirmCard.vue')
  const tpl = templateOf(classic)

  it('根节点、头部、正文、作答区、脚注的既有 testid 与版式都在', () => {
    assert.match(tpl, /class="rounded-sm border bg-card\/80 p-3 text-sm shadow-2xs"/)
    for (const id of ['testid', '`${testid}-ref`', '`${testid}-title`', '`${testid}-body`', '`${testid}-dismiss-cancel`', '`${testid}-dismiss`']) {
      assert.ok(tpl.includes(id), `经典 ConfirmCard 缺少 ${id}`)
    }
    assert.ok(tpl.includes(':data-kind="item.kind"') && tpl.includes(':data-risk="item.risk"'))
    assert.match(tpl, /mt-2 max-h-56 overflow-y-auto rounded-xs border border-border\/50 bg-muted\/20 px-2\.5 py-2 text-xs/, '经典正文仍是 max-h-56 的框内滚动')
    assert.ok(tpl.includes('批准之前，智能体只会做只读的准备工作。'))
    assert.match(tpl, /:class="\[focused \? 'border-primary ring-1 ring-primary\/40' : 'border-border\/70'\]"/)
  })
})
