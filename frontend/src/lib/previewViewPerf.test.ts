// 运行：pnpm --dir frontend run test:unit
// 预览视图渲染压力（#158）的源码级防线。渲染开销本身用 scripts/e2e-terminal/e2e/bench-preview.mjs 度量（见 README），
// 这里只守住「度量查出来的那几处结构」不被改回去：
//   1. 右侧轨：刻度列表独立成 PreviewRailTicks，会话列表每滚一帧只更新可视窗口框，不再带着几百道刻度一起重渲染；
//      排布 / 窗口框用 shallowRef（不给刻度数据套深层响应式代理）；渐隐样式只依赖两个布尔量；
//   2. 「⋯」菜单：未被碰过的卡只画朴素按钮，Reka 下拉菜单在悬停 / 点击 / ArrowDown 时才挂载（一千多张卡不再各挂一整套）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewPerf.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const RAIL = 'components/feedback/preview-view/PreviewTimelineRail.vue'
const TICKS = 'components/feedback/preview-view/PreviewRailTicks.vue'
const CARD_MENU = 'components/feedback/preview-view/PreviewCardMenu.vue'

describe('预览视图渲染压力（#158）：右侧轨', () => {
  const rail = read(RAIL)
  const ticks = read(TICKS)

  it('轨的模板里没有刻度循环：刻度交给 PreviewRailTicks，外层只剩窗口框与提示', () => {
    assert.match(rail, /import PreviewRailTicks from '\.\/PreviewRailTicks\.vue'/)
    const uses = [...rail.matchAll(/<PreviewRailTicks\b([^>]*)\/>/g)]
    assert.equal(uses.length, 1, '刻度只在一处渲染')
    assert.match(uses[0][1], /:ticks="layout\.ticks"/)
    assert.match(uses[0][1], /@jump="emit\('jump', \$event\)"/)
    assert.match(uses[0][1], /@hover="hoverId = \$event"/)
    assert.ok(!/v-for="t in layout\.ticks"/.test(rail), '外层模板不能再循环刻度，否则滚动每一帧都会重渲染几百道刻度')
    assert.ok(!/data-testid="pv-rail-tick"/.test(rail), '刻度按钮的标记只在 PreviewRailTicks 里')
  })

  it('排布与窗口框是 shallowRef（整块替换），窗口框没变不赋新对象', () => {
    assert.match(rail, /const layout = shallowRef<RailLayout>\(EMPTY_LAYOUT\)/)
    assert.match(rail, /const box = shallowRef\(\{ top: 0, height: 0 \}\)/)
    assert.ok(!/const (layout|box) = ref\b/.test(rail), '排布 / 窗口框不要用深层 ref')
    assert.match(rail, /function setBox\(next: \{ top: number; height: number \}\) \{\s*\n\s*const cur = box\.value\s*\n\s*if \(cur\.top !== next\.top \|\| cur\.height !== next\.height\) box\.value = next/)
    assert.ok(!/box\.value = railViewportBox/.test(rail), '窗口框一律经 setBox 赋值')
    assert.equal([...rail.matchAll(/setBox\(railViewportBox\(/g)].length, 2, 'measure 与 updateViewport 两处都走 setBox')
  })

  it('渐隐样式只依赖两个布尔量（railScroll 每帧都变，样式对象不跟着重算）', () => {
    assert.match(rail, /const fadeUp = computed\(\(\) => overflowing\.value && railScroll\.value > 1\)/)
    assert.match(rail, /const fadeDown = computed\(\(\) => overflowing\.value && railScroll\.value \+ trackHeight\.value < layout\.value\.height - 1\)/)
    const fade = rail.match(/const fadeStyle = computed\(\(\) => \{([\s\S]*?)\n\}\)/)
    assert.ok(fade)
    assert.ok(!/railScroll/.test(fade[1]), 'fadeStyle 本身不能直接读 railScroll')
    assert.match(fade[1], /fadeUp\.value/)
    assert.match(fade[1], /fadeDown\.value/)
  })

  it('PreviewRailTicks：只收 ticks，只往外发 jump / hover，不持有会变的状态', () => {
    assert.match(ticks, /defineProps<\{\s*[\s\S]*?ticks: readonly RailTick\[\]\s*\}>\(\)/)
    assert.match(ticks, /jump: \[item: RailItem\]/)
    assert.match(ticks, /hover: \[id: string \| null\]/)
    assert.ok(!/\bref\(|\bshallowRef\(|\breactive\(|\bcomputed\(|\bwatch\(|onMounted|onBeforeUnmount/.test(ticks.replace(/\/\/.*$/gm, '')), '刻度组件是纯展示：没有响应式状态、计算属性、监听与生命周期')
    for (const id of ['pv-rail-tick', 'pv-rail-tick-line', 'pv-rail-tick-seg']) {
      assert.ok(ticks.includes(`data-testid="${id}"`), `${id} 的标记不能丢（e2e 与脚本在用）`)
    }
    for (const attr of ['data-kind', 'data-state', 'data-id', 'data-parts']) {
      assert.ok(ticks.includes(`:${attr}=`), `${attr} 不能丢`)
    }
    assert.match(ticks, /@click="emit\('jump', t\.item\)"/)
    assert.match(ticks, /@mouseenter="emit\('hover', t\.item\.id\)"/)
    assert.match(ticks, /@mouseleave="emit\('hover', null\)"/)
    assert.match(ticks, /@focus="emit\('hover', t\.item\.id\)"/)
    assert.match(ticks, /@blur="emit\('hover', null\)"/)
  })
})

describe('预览视图渲染压力（#158）：「⋯」菜单惰性挂载', () => {
  const src = read(CARD_MENU)
  const tpl = src.slice(src.indexOf('<template>'))

  it('未被碰过只画朴素按钮；下拉菜单在 armed 之后才挂载', () => {
    assert.match(tpl, /<DropdownMenu v-if="armed" v-model:open="open">/)
    const plain = tpl.match(/<button\s+v-else\b([\s\S]*?)>\s*<MoreHorizontal/)
    assert.ok(plain, '朴素按钮是 v-else 分支')
    assert.match(plain[1], /data-testid="card-menu"/)
    assert.match(plain[1], /aria-label="更多"/)
    assert.match(plain[1], /@pointerenter="onPointerEnter"/)
    assert.match(plain[1], /@pointerdown="onPointerDown"/)
    assert.match(plain[1], /@click="onClick"/)
    assert.match(plain[1], /@keydown\.down\.prevent="arm\(true\)"/)
    assert.ok(!/@focus/.test(plain[1]), '不在 focus 上预挂载：换 DOM 节点会丢键盘焦点')
  })

  it('两种触发器长得一样（共用 BTN_CLASS），真触发器也带 card-menu 标记', () => {
    assert.equal([...tpl.matchAll(/:class="BTN_CLASS"/g)].length, 2)
    const real = tpl.match(/<DropdownMenuTrigger as-child>\s*<button\b([^>]*)>/)
    assert.ok(real)
    assert.match(real[1], /data-testid="card-menu"/)
    assert.match(real[1], /ref="realBtn"/)
  })

  it('挂载只会由 false 变 true；打开状态受控，任何一条路径挂载的都能打开', () => {
    assert.match(src, /function arm\(thenOpen: boolean\) \{\s*\n\s*if \(!armed\.value\) \{/)
    assert.match(src, /armed\.value = true/)
    assert.ok(!/armed\.value = false/.test(src), '挂上去就不再摘')
    assert.match(src, /if \(thenOpen\) open\.value = true/)
    assert.match(src, /const open = ref\(false\)/)
  })

  it('触屏的 pointerenter 不预挂载；click 与 radix 触发器同一判断（左键、没按 ctrl）；pointerdown 兜底只给鼠标 / 触控笔', () => {
    assert.match(src, /if \(ev\.pointerType === 'touch'\) return\s*\n\s*arm\(false\)/)
    assert.match(src, /if \(ev\.button === 0 && !ev\.ctrlKey\) arm\(true\)/)
    assert.match(src, /if \(ev\.pointerType === 'touch' \|\| ev\.button !== 0 \|\| ev\.ctrlKey\) return\s*\n\s*arm\(true\)/)
    assert.ok(!/detail/.test(src.replace(/\/\/.*$/gm, '')), '不再看 click 的 detail')
  })

  it('焦点在朴素按钮上时，换成真触发器后把焦点还给它', () => {
    assert.match(src, /document\.activeElement === plainBtn\.value/)
    assert.match(src, /nextTick\(\(\) => realBtn\.value\?\.focus\(\)\)/)
  })

  it('菜单项的渲染与选中语义保持不变（testid、destructive、keepOpen）', () => {
    assert.match(tpl, /:data-testid="`card-menu-\$\{item\.key\}`"/)
    assert.match(tpl, /data-testid="card-menu-content"/)
    assert.match(tpl, /item\.destructive \? 'text-destructive focus:bg-destructive\/10 focus:text-destructive' : ''/)
    assert.match(src, /if \(item\.keepOpen\) ev\.preventDefault\(\)\s*\n\s*item\.onSelect\(\)/)
  })
})

describe('预览视图渲染压力（#158）：历史轮次离屏跳过渲染', () => {
  const list = read('components/feedback/preview-view/PreviewMessageList.vue')

  it('最近 LIVE_TAIL_ROUNDS 轮不套，更早的轮次套 pv-cv；不超过 LIVE_TAIL_ROUNDS 轮时一个都不套', () => {
    assert.match(list, /const LIVE_TAIL_ROUNDS = 20/)
    assert.match(list, /const cvCutoff = computed\(\(\) => displayedConversationRounds\.value\.length - LIVE_TAIL_ROUNDS\)/)
    const group = list.match(/<div\s+v-for="\(session, gi\) in displayedConversationRounds"([\s\S]*?)data-testid="turn-group"/)
    assert.ok(group, '轮次组按 (session, gi) 循环')
    assert.match(group[1], /:key="session\.session_id"/)
    assert.match(group[1], /class="space-y-3"/)
    // 紧挨着锚点的几轮豁免（boundaryRounds，向上载入时的锚点校准用，见 scrollSettle.test.ts）：下标在 cutoff 之前、又不在豁免集里才套
    assert.match(group[1], /:class="gi < cvCutoff && !boundaryRounds\.has\(session\.session_id\) \? 'pv-cv' : undefined"/)
    // 下标 < 总数 - 20：总数 ≤ 20 时 cvCutoff ≤ 0，没有任何下标满足
    const cutoff = (n: number) => n - 20
    for (const n of [0, 1, 10, 20]) assert.ok(Array.from({ length: n }, (_, gi) => gi).every(gi => !(gi < cutoff(n))))
    assert.deepEqual(Array.from({ length: 23 }, (_, gi) => gi).filter(gi => gi < cutoff(23)), [0, 1, 2])
  })

  it('样式：content-visibility:auto + 记住真实高度 + 放宽 8px 的裁剪边，且只在两项都支持的浏览器启用', () => {
    const css = list.slice(list.indexOf('<style scoped>'))
    const rule = css.match(/@supports \(content-visibility: auto\) and \(overflow-clip-margin: 1px\) \{\s*\n\s*\.pv-cv \{([\s\S]*?)\}\s*\n\}/)
    assert.ok(rule, 'pv-cv 必须包在 @supports 里：不支持 overflow-clip-margin 的浏览器会把高亮外框削掉')
    assert.match(rule[1], /content-visibility: auto;/)
    assert.match(rule[1], /contain-intrinsic-size: auto 900px;/)
    assert.match(rule[1], /overflow-clip-margin: 8px;/)
    assert.equal([...css.matchAll(/\.pv-cv(?![-\w])/g)].length, 1, 'pv-cv 只在这一处定义（pv-cv-notice 是另一条规则）')
  })
})

describe('预览视图渲染压力（#169）：进度通知卡离屏跳过渲染', () => {
  const list = read('components/feedback/preview-view/PreviewMessageList.vue')

  it('间隙模板按 cv 决定通知卡套不套：只有时间线末尾套；轮次之间的间隙（含提问与回复之间）不套', () => {
    assert.match(list, /createReusableTemplate<\{ blocks: TimelineBlock\[\]; cv\?: boolean \}>\(\)/)
    assert.match(list, /<DefineGap v-slot="\{ blocks, cv \}">/)
    assert.match(list, /const liveNotices = computed\(\(\) => liveNoticeKeys\(props\.notices \?\? \[\]\)\)/)
    const wrapper = list.match(/<div\s+:data-timeline-key="block\.key"[^>]*:class="noticeCvClass\(liveNotices, block, cv\)"[^>]*>/)
    assert.ok(wrapper, '每一块的外层 div（带 data-timeline-key 与 data-rail-* 的那个根节点）带 noticeCvClass')
    // 三处调用：轮次前的间隙（在轮次组里面：老轮次整组已套 pv-cv，最近 20 轮要精确几何，不套）、提问与回复之间（不套）、末尾（套）。
    // 第一版把轮次前的间隙也套了，e2e/preview-longlist.mjs 实测向上载入后原来的第一轮每批都漂 197px——这条断言就是防它被加回去。
    assert.match(list, /<ReuseGap :blocks="timelineBlocks\[session\.session_id\] \|\| \[\]" \/>/)
    assert.match(list, /<ReuseGap :blocks="timelineBlocks\[inRoundKey\(session\.session_id\)\] \|\| \[\]" \/>/)
    // 必须写成 :cv="true"：ReuseGap（createReusableTemplate）没有声明属性，裸写 cv 传进去的是空串、是假值，通知卡一条都不会套
    // （e2e/preview-tail.mjs 抓到的：第一版就是裸写的）
    // 末尾渲染的是 visibleTailBlocks（#169 第三期的渐进渲染窗口，补齐完成 / 块数不多时就是 tailBlocks 本身）
    assert.match(list, /<ReuseGap :blocks="visibleTailBlocks" :cv="true" \/>/)
    assert.ok(!/<ReuseGap [^>]*\scv\s*\/?>/.test(list), 'cv 不能裸写（传进去是空串）')
    assert.equal([...list.matchAll(/<ReuseGap [^>]*\bcv\b/g)].length, 1, '只有末尾那一处传 cv')
  })

  it('样式：与 pv-cv 同样的前提（两项都支持才启用），占位高度 140px，放宽 8px 的裁剪边', () => {
    const css = list.slice(list.indexOf('<style scoped>'))
    const rule = css.match(/@supports \(content-visibility: auto\) and \(overflow-clip-margin: 1px\) \{\s*\n\s*\.pv-cv-notice \{([\s\S]*?)\}\s*\n\}/)
    assert.ok(rule, 'pv-cv-notice 必须包在 @supports 里')
    assert.match(rule[1], /content-visibility: auto;/)
    assert.match(rule[1], /contain-intrinsic-size: auto 140px;/)
    assert.match(rule[1], /overflow-clip-margin: 8px;/)
    assert.equal([...css.matchAll(/\.pv-cv-notice\b/g)].length, 1, 'pv-cv-notice 只在这一处定义')
  })

  it('不折叠、不截断：通知卡本身（PreviewNoticeCard）一个字不动，仍渲染完整正文', () => {
    const card = read('components/feedback/preview-view/PreviewNoticeCard.vue')
    assert.ok(!/pv-cv|content-visibility|noticeCv/.test(card), '离屏跳过渲染只在列表这一层套，卡片自己不管')
    assert.match(card, /data-testid="notice-bubble"/)
  })
})

describe('预览视图渲染压力（#169 第三期）：末尾通知渐进渲染 + 次要请求让路', () => {
  const list = read('components/feedback/preview-view/PreviewMessageList.vue')

  it('末尾渲染窗口：补齐完成 / 块数不多时就是 tailBlocks 本身（行为与以前完全一样）；容器带 data-tail-total / data-tail-hidden 供 e2e 等「补齐完成」', () => {
    assert.match(list, /const visibleTailBlocks = computed\(\(\) => \(tailStart\.value > 0 \? tailBlocks\.value\.slice\(tailStart\.value\) : tailBlocks\.value\)\)/)
    assert.match(list, /:data-tail-total="tailBlocks\.length"/)
    assert.match(list, /:data-tail-hidden="tailStart"/)
    // 不隐藏、不折叠：末尾容器上没有 v-show，也没有按条件整块摘掉
    const tail = list.match(/<div\s+v-if="tailBlocks\.length > 0"([\s\S]*?)>\s*<!-- cv 要写成/)
    assert.ok(tail, '找得到末尾容器')
    assert.ok(!/v-show/.test(tail[1]), '末尾容器不能用 v-show 隐藏内容')
  })

  it('起点按块的键钉住（新通知追加、晚到的条目插进中间，窗口都不滑动）；钉住的块不见了就整段渲染；钉点的 watch 不用 sync', () => {
    assert.match(list, /const tailPinnedKey = ref<string \| null>\(null\)/)
    assert.match(list, /pinned = key === '' \? 0 : Math\.max\(0, blocks\.findIndex\(b => b\.key === key\)\)/)
    const pin = list.match(/watch\(\(\) => tailBlocks\.value\.length > 0 && tailPinnedKey\.value === null,[\s\S]*?\}, \{([^}]*)\}\)/)
    assert.ok(pin, '找得到钉起点的 watch')
    assert.ok(!/flush:\s*'sync'/.test(pin[1]), 'sync 会让每次依赖变化都重算整条时间线')
  })

  it('补齐：空闲时一批一批（requestIdleCallback，Safari 退回定时器）；钉锚点用独立的 useScrollSettle 实例，不打断平滑滚动校准', () => {
    assert.match(list, /window\.requestIdleCallback\(run, \{ timeout: TAIL_FILL_IDLE_TIMEOUT_MS \}\)/)
    assert.match(list, /setTimeout\(run, TAIL_FILL_FALLBACK_MS\)/)
    assert.match(list, /const \{ hold: holdTailAnchor, cancel: cancelTailHold, isActive: tailJobActive \} = useScrollSettle\(\)/)
    assert.equal([...list.matchAll(/useScrollSettle\(\)/g)].length, 2, '补齐与原来的滚动校准各用各的实例')
    assert.match(list, /holdTailAnchor\(container, first, firstTop\)/)
    assert.match(list, /onBeforeUnmount\(cancelTailFill\)/)
  })

  it('补齐一批的视口保持（#252）：停在底部走贴底（followBottom）、不钉锚点，并撤掉前一批留下的钉锚点；没贴底才钉；用户刚向上看时按没贴底算', () => {
    const body = list.match(/async function fillTailBatch\(\) \{([\s\S]*?)\n\}\n/)?.[1] ?? ''
    assert.ok(body, '找得到 fillTailBatch')
    // 模式由纯函数定；贴底以「最近一次滚动之后是否仍紧贴底部」的标记为准，没有标记再看几何；用户刚向上看（让位期内）传 Infinity
    assert.match(body, /const mode = first === null \? 'none' : tailHoldMode\(firstTop, gap\)/)
    // 「是否贴底」与 repinIfPinned 同一口径：标记 / 上一次的大小推算 / 让掉那次的判定（#252 残留：迟到的 scroll 事件把标记翻成假，批不能因此钉锚点）
    assert.match(body, /const wasPinned = pinnedAtBottom \|\| deferredPinned \|\| wasPinnedBeforeResize\(lastSize, container\.scrollTop\)/)
    assert.match(body, /userHolding\(lastUpIntentAt, performance\.now\(\)\)\s*\n\s*\? Number\.POSITIVE_INFINITY\s*\n\s*: wasPinned\s*\n\s*\? 0\s*\n\s*: bottomGap\(/)
    // 贴底：撤掉旧的钉锚点（在改起点之前），批插入之后 followBottom；锚点只在 'anchor' 模式下钉
    assert.match(body, /if \(mode === 'bottom'\) cancelTailHold\(\)\s*\n\s*pinTailStart\(nextTailStart\(tailStart\.value\)\)\s*\n\s*await nextTick\(\)\s*\n\s*if \(mode === 'bottom'\) followBottom\(\)/)
    assert.match(body, /else if \(mode === 'anchor' && first && first\.isConnected && chatScrollContainer\.value === container\) holdTailAnchor\(container, first, firstTop\)/)
    assert.ok(!/shouldHoldFirst/.test(list), '列表里不再直接用 shouldHoldFirst（经 tailHoldMode）')
  })

  it('被点名要看的块先露出来：右侧轨 / 搜索命中走 revealEntry，「待答复」徽标 / Q 引用走 focusRef；滚动时探测用户是否在向上够', () => {
    assert.match(list, /if \(found\.gapKey === TIMELINE_TAIL_KEY\) revealTailBlock\(found\.block\.key\)/)
    assert.match(list, /revealTailIndex\(tailBlocks\.value\.findIndex\(b => blockHoldsConfirm\(b, ref\)\)\)/)
    assert.match(list, /if \(tailStart\.value > 0\) probeTailFill\(\)/)
    // 末尾容器的选择器不能在脚本里写 data-testid 字面量（对等测试的负向对照按文字找模板里的 testid）
    assert.match(list, /querySelector<HTMLElement>\('\[data-tail-total\] > \[data-timeline-key\]'\)/)
  })

  it('次要请求让路：整表非强制加载立栅栏、成败都放行；强制刷新与用户动作的跟进请求不让路；四个次要请求都走 whenSecondaryOpen / wait', () => {
    const store = read('stores/session.ts')
    assert.match(store, /if \(force\) \{\s*\n\s*await workflowFlight\.request\(workflowId, force\)\s*\n\s*return\s*\n\s*\}\s*\n\s*secondaryGate\.hold\(\)\s*\n\s*try \{\s*\n\s*await workflowFlight\.request\(workflowId, force\)\s*\n\s*\} finally \{\s*\n\s*secondaryGate\.release\(\)/)
    assert.match(store, /const waiting = opts\.urgent \? null : secondaryGate\.wait\(\)/)
    assert.equal([...store.matchAll(/fetchQueuedFeedbacks\(workflowId, \{ urgent: true \}\)/g)].length, 2, '追加 / 撤回的跟进请求要 urgent')
    for (const [rel, token] of [
      ['stores/confirms.ts', /whenSecondaryOpen\(\(\) => \{\s*\n\s*if \(token === watchSeq/],
      ['stores/workItems.ts', /whenSecondaryOpen\(\(\) => \{\s*\n\s*if \(token === feedWatchSeq/],
      ['composables/useWorkflowCheckpoints.ts', /whenSecondaryOpen\(\(\) => \{\s*\n\s*deferred = false/],
    ] as const) {
      assert.match(read(rel), token, `${rel} 的首次加载要走让路栅栏（令牌 / 合并，免得等的时候切走了还发）`)
    }
  })

  it('深链接解析完之前不为占位会话取恢复胶囊：等 sessionStore.timelineShown 变真再取（load-budget 第 3 步钉住）', () => {
    const cp = read('composables/useWorkflowCheckpoints.ts')
    assert.match(cp, /watch\(\[workflowId, \(\) => sessionStore\.timelineShown\]/)
    assert.match(cp, /if \(!shown \|\| deferred\) return/)
    const store = read('stores/session.ts')
    assert.match(store, /const timelineShown = ref\(false\)/)
    assert.match(store, /timelineActive = active\s*\n\s*timelineShown\.value = active/)
    assert.match(store, /setTimelineActive,\s*\n\s*timelineShown,/)
  })
})
