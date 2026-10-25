// 运行：pnpm --dir frontend run test:unit
// 预览视图「打开会话后的头几秒」（#186 C1）的源码级防线。行为用 scripts/e2e-terminal/e2e/scroll-yield.mjs 与
// scripts/e2e-terminal/ro/open-input.sh（--writes / --trace）度量，这里只守住度量查出来的那几处结构不被改回去：
//   1. 贴底只有两个出口：followBottom（内容变了、用户本来停在底部：跟随，会让位）与 scrollToBottom（显式的「到底部」：一定要到）；
//      各 watch 不再直接调 scrollToBottom（它无条件重置 isScrolledUp，晚到的滚轮会被它盖掉）；
//   2. 贴底只写 scrollTop，不再 scrollIntoView（先挪到锚点再挪回去，每次两次强制版面、没有净位移）；
//   3. 用户意图（滚轮向上 / 手指下拖 / 上翻键）一发生就记时间戳，不等晚到的 scroll 事件；
//   4. 版面预留：视图入口那一行从第一帧起占位。
// （试过的「末尾补齐按时间预算切片」没有收益，已撤销，见 D-381：不要在没有对照数据的情况下再改回去。）
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewFollow.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const LIST = 'components/feedback/preview-view/PreviewMessageList.vue'

/** 取出 function name( … ) { … } 的函数体（按花括号配对，源码里字符串与注释里没有不成对的花括号）。 */
function fnBody(src: string, signature: string): string {
  const start = src.indexOf(signature)
  assert.ok(start >= 0, `找得到 ${signature}`)
  const open = src.indexOf('{', src.indexOf(')', start))
  let depth = 0
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}' && --depth === 0) return src.slice(open + 1, i)
  }
  throw new Error(`${signature} 的花括号不成对`)
}

describe('预览视图贴底（#186 C1）：两个出口', () => {
  const list = read(LIST)

  it('内容变了的三处（新追加 / 新通知 / 新一轮）都走 followBottom，不再直接 scrollToBottom', () => {
    assert.match(list, /watch\(\(\) => sessionStore\.queuedFeedbacks, \(\) => \{\s*\n\s*if \(!isScrolledUp\.value\) nextTick\(followBottom\)/)
    assert.match(list, /watch\(\(\) => timelineEntries\.value\.map\(e => e\.key\)\.join\('\|'\), \(\) => \{\s*\n\s*if \(!isScrolledUp\.value\) nextTick\(followBottom\)/)
    // 新一轮：停在底部才跟到底；往上滚了不动（浮动条的数字由 newDynamics 维护，#254）
    assert.match(list, /if \(!isScrolledUp\.value\) nextTick\(followBottom\)\s*\n\s*\}\s*\n\}, \{ deep: true, immediate: true \}\)/)
  })

  it('scrollToBottom 只剩显式的三处调用：切换 / 首次载入工作流、回到底部按钮，以及对外暴露给 FeedbackView 与输入区', () => {
    const calls = [...list.matchAll(/scrollToBottom\(/g)].length
    // 定义 1 + 切换工作流 1 + handleJumpToLatest 1
    assert.equal(calls, 3, '除定义外只有两处显式调用；新增调用点请先想清楚它是不是「用户要到底部」')
    assert.match(list, /nextTick\(\(\) => \{\s*\n\s*scrollToBottom\(false\)\s*\n\s*setupTopObserver\(\)/)
    assert.match(list, /function handleJumpToLatest\(\) \{\s*\n\s*clearAway\(\)\s*\n\s*scrollToBottom\(true\)/)
    assert.match(list, /defineExpose\(\{\s*\n\s*scrollToBottom,\s*\n\s*scrollByDelta,\s*\n\s*scrollToSession\s*\n\}\)/)
  })

  it('scrollToBottom：先清让位状态再写一次；只写 scrollTop，不再 scrollIntoView；复核只在用户没有向上看时才写', () => {
    const body = fnBody(list, 'function scrollToBottom(')
    assert.ok(!/scrollIntoView/.test(body), 'scrollIntoView 会先把位置挪到锚点再被 scrollTop 挪回去')
    assert.match(body, /lastUpIntentAt = null\s*\n\s*isScrolledUp\.value = false/)
    assert.match(body, /pinOnce\(\)/)
    assert.match(body, /if \(!userHolding\(lastUpIntentAt, performance\.now\(\)\)\) pinOnce\(\)/)
    assert.match(body, /armFollowRecheck\(FOLLOW_RECHECK_FRAMES\)/)
    assert.ok(!/bottomAnchorRef/.test(list), '底部留白元素不再被当作滚动锚点引用')
    assert.ok(!/scrollIntoView\(\{ behavior: smooth/.test(list), '贴底路径上不再有 scrollIntoView')
  })

  it('pinOnce：离底部还有距离才写（repinTarget 对「已在底部」返回 null），写的是 scrollHeight', () => {
    const body = fnBody(list, 'function pinOnce(')
    assert.match(body, /repinTarget\(\{ scrollHeight: el\.scrollHeight, scrollTop: el\.scrollTop, clientHeight: el\.clientHeight \}, true\)/)
    assert.match(body, /if \(target !== null\) el\.scrollTop = target/)
  })

  it('followBottom 的让位条件全部走 mayFollow：向上看 / 用户意图宽限 / 排队的输入（只查第一次尝试）/ 滚到某一轮的校准', () => {
    const gate = fnBody(list, 'function followGateOpen(')
    assert.match(gate, /scrolledUp: isScrolledUp\.value/)
    assert.match(gate, /holding: userHolding\(lastUpIntentAt, performance\.now\(\)\)/)
    assert.match(gate, /inputPending: checkInput && isInputPending\(\)/)
    assert.match(gate, /settling: scrollJobActive\(\)/)
    const follow = fnBody(list, 'function followBottom(')
    assert.match(follow, /armFollowRecheck\(FOLLOW_RECHECK_FRAMES \+ 1, true\)/, '第一次尝试也走动画帧回调（在帧开头派发的滚轮之后），并查排队的输入')
    assert.ok(!/pinOnce\(\)/.test(follow), '内容变化的当下（微任务里）不同步写：滚轮监听还没跑，会把用户刚滚到的位置盖掉（scroll-yield 套件实测）')
    const arm = fnBody(list, 'function armFollowRecheck(')
    assert.match(arm, /const check = followCheckInput\s*\n\s*followCheckInput = false\s*\n\s*if \(followGateOpen\(check\)\) pinOnce\(\)/, '只有一次请求的第一帧查排队的输入；复核帧不查：鼠标一直在动会让它永远贴不上')
  })

  it('复核链合并：多次请求共用一个 rAF，每帧只做一次；卸载时取消', () => {
    const arm = fnBody(list, 'function armFollowRecheck(')
    assert.match(arm, /followFrames = Math\.max\(followFrames, frames\)\s*\n\s*if \(checkInput\) followCheckInput = true\s*\n\s*if \(followRaf\) return/)
    assert.match(fnBody(list, 'function cancelFollow('), /followCheckInput = false/)
    assert.match(list, /onBeforeUnmount\(\(\) => \{\s*\n\s*window\.removeEventListener\('keydown', onKeyIntent\)\s*\n\s*cancelFollow\(\)/)
  })

  it('迟到的帧不做决定：复核链让一帧（不占复核次数、每链最多一帧），大小变化的重新贴底晚一帧补（补的那次不再让）', () => {
    const arm = fnBody(list, 'function armFollowRecheck(')
    assert.match(arm, /const step = \(frameTime: number\) => \{/)
    assert.match(
      arm,
      /if \(!followStaleSkipped && currentFrameStale\(frameTime\)\) \{\s*\n\s*followStaleSkipped = true\s*\n\s*followRaf = requestAnimationFrame\(step\)\s*\n\s*return\s*\n\s*\}\s*\n\s*followFrames--/,
      '让掉的那一帧在扣复核次数之前',
    )
    assert.match(arm, /else followStaleSkipped = false/, '链结束时复位，下一条链又能让一帧')
    const repin = fnBody(list, 'function repinIfPinned(')
    assert.match(
      repin,
      /if \(!afterSkip && currentFrameStale\(\)\) \{\s*\n\s*deferredPinned = deferredPinned \|\| pinnedAtBottom \|\| wasPinnedBeforeResize\(lastSize, el\.scrollTop\)\s*\n\s*retryRepinNextFrame\(\)\s*\n\s*return\s*\n\s*\}/,
      '让位那一刻先把「变化之前是否贴底」记下来',
    )
    assert.match(fnBody(list, 'function retryRepinNextFrame('), /repinIfPinned\(true\)/)
    assert.match(list, /pinObserver = new ResizeObserver\(\(\) => repinIfPinned\(\)\)/, 'ResizeObserver 回调不能把 entries 当作 afterSkip 传进去')
    assert.match(fnBody(list, 'function currentFrameStale('), /frameTime \?\? document\.timeline\.currentTime/)
    const cancel = fnBody(list, 'function cancelFollow(')
    assert.match(cancel, /cancelAnimationFrame\(repinRetryRaf\)/)
    assert.match(cancel, /followStaleSkipped = false/)
    assert.match(cancel, /deferredPinned = false/)
  })

  it('补跑沿用让位那一刻的判定（浏览器自己调过 scrollTop 之后 pinnedAtBottom 会被下一帧的 scroll 事件改写）；用完即清，不外泄', () => {
    const repin = fnBody(list, 'function repinIfPinned(')
    assert.match(repin, /const wasPinned = pinnedAtBottom \|\| wasPinnedBeforeResize\(lastSize, now\.scrollTop\) \|\| \(afterSkip && deferredPinned\)\s*\n\s*deferredPinned = false/)
    // 沿用的判定仍然过用户意图这一关：userHolding 在 repinTarget 之前
    const iHold = repin.indexOf('userHolding(lastUpIntentAt')
    const iTarget = repin.indexOf('repinTarget(now, wasPinned)')
    assert.ok(iHold > 0 && iTarget > iHold, '用户意图的让位必须在写 scrollTop 之前')
  })

  it('大小变化后的重新贴底（repinIfPinned）也让位于刚表达的用户意图，让位期过后再看一次', () => {
    const body = fnBody(list, 'function repinIfPinned(')
    assert.match(body, /if \(scrollJobActive\(\) \|\| tailJobActive\(\)\) return/)
    assert.match(body, /if \(userHolding\(lastUpIntentAt, performance\.now\(\)\)\) \{\s*\n\s*scheduleFollowAfterHold\(\)\s*\n\s*return\s*\n\s*\}/)
  })

  it('让位不能把「该跟随」变成永远不跟随：让位期过完，没真的滚走（pinnedAtBottom 仍为真）就补上跟随；期间又有向上意图就接着等', () => {
    const body = fnBody(list, 'function scheduleFollowAfterHold(')
    assert.match(body, /if \(holdFollowTimer \|\| lastUpIntentAt === null\) return/)
    assert.match(body, /USER_SCROLL_GRACE_MS - \(performance\.now\(\) - lastUpIntentAt\)/)
    assert.match(body, /if \(userHolding\(lastUpIntentAt, performance\.now\(\)\)\) scheduleFollowAfterHold\(\)\s*\n\s*else if \(pinnedAtBottom\) followBottom\(\)/)
    // 复核链上因让位而没贴成时也排一次
    assert.match(fnBody(list, 'function armFollowRecheck('), /if \(followGateOpen\(check\)\) pinOnce\(\)\s*\n\s*else if \(userHolding\(lastUpIntentAt, performance\.now\(\)\)\) scheduleFollowAfterHold\(\)/)
    assert.match(fnBody(list, 'function cancelFollow('), /clearTimeout\(holdFollowTimer\)/)
  })
})

describe('预览视图贴底（#186 C1）：用户意图', () => {
  const list = read(LIST)

  it('滚动容器上挂滚轮 / 触摸（passive，不拦截滚动），键盘挂 window；输入框里的按键排除', () => {
    assert.match(list, /@scroll="handleScroll"\s*\n\s*@wheel\.passive="onWheelIntent"\s*\n\s*@touchstart\.passive="onTouchStartIntent"\s*\n\s*@touchmove\.passive="onTouchMoveIntent"/)
    assert.match(list, /window\.addEventListener\('keydown', onKeyIntent, \{ passive: true \}\)/)
    assert.equal([...list.matchAll(/window\.removeEventListener\('keydown', onKeyIntent\)/g)].length, 1, '加了就要摘')
    assert.match(fnBody(list, 'function onKeyIntent('), /isUpwardKey\(e\) && !isEditableTarget\(e\.target as HTMLElement \| null\)/)
    assert.match(fnBody(list, 'function onWheelIntent('), /isUpwardWheel\(e\.deltaY\)/)
  })

  it('意图只记时间戳（不改 isScrolledUp）：晚到的 scroll 事件之后仍由位置本身说了算', () => {
    assert.match(fnBody(list, 'function markUpIntent('), /lastUpIntentAt = performance\.now\(\)/)
    const wheel = fnBody(list, 'function onWheelIntent(')
    assert.ok(!/isScrolledUp/.test(wheel), '滚轮只记意图，不直接改「是否离底」')
  })
})

describe('预览视图版面预留（#186 C1）', () => {
  const view = read('views/FeedbackView.vue')

  it('搜索入口那一行从第一帧起占位，入口按钮等有了工作流才出现', () => {
    assert.match(view, /<div v-if="!isPhone" class="flex min-h-\[21px\] justify-end gap-3 px-3 pt-1">\s*\n\s*<MessageSearchLink v-if="feedWorkflowId" \/>\s*\n\s*<\/div>/)
    assert.ok(!/<div v-if="feedWorkflowId && !isPhone"/.test(view), '整行不能再等选中会话才出现（会把列表推下去 21 px）')
  })
})

describe('预览视图跳转让贴底（#186 C2a 回归：搜索命中已答复的提问后页面停在最底部）', () => {
  const list = read(LIST)

  it('beginJump：记意图（让位期）、作废「贴着底部」的旧标记与迟到帧里记下的判定', () => {
    const body = fnBody(list, 'function beginJump(')
    assert.match(body, /markUpIntent\(\)/)
    assert.match(body, /pinnedAtBottom = false/)
    assert.match(body, /deferredPinned = false/)
  })

  it('页面自己发起的跳转都先 beginJump：滚到某一轮、摆出条目（搜索命中 / 点名确认卡）、点名确认卡（含工具栏徽标与 Q 引用）', () => {
    // 滚到某一轮：找到目标之后、撑开窗口之前
    const toSession = fnBody(list, 'async function scrollToSession(')
    assert.ok(toSession.indexOf('beginJump()') > 0 && toSession.indexOf('beginJump()') < toSession.indexOf('growWindowTo('), 'scrollToSession 要在撑开窗口之前 beginJump')
    // 摆出条目：撑开窗口、展开摞之前
    const reveal = fnBody(list, 'async function revealEntry(')
    assert.ok(reveal.indexOf('beginJump()') > 0 && reveal.indexOf('beginJump()') < reveal.indexOf('growWindowTo('), 'revealEntry 要在撑开窗口之前 beginJump')
    // 任何来源的点名（工具栏徽标 / Q 引用 / 搜索命中）：卡自己平滑滚过去并做落地校准（useConfirmFocus.reveal，#253；不经过列表的校准任务）。点名发生在窗口撑开（要等网络）之后，所以让位期从点名这一刻起算
    assert.match(list, /watch\(\(\) => confirmStore\.focusRef, ref => \{\s*\n\s*if \(!ref\) return\s*\n\s*beginJump\(\)/)
    // 搜索命中确认项：先摆出来、清空、下一帧再点名（点名那一下触发上面的监听）
    const hit = fnBody(list, 'async function jumpToHit(')
    assert.match(hit, /await nextTick\(\)\s*\n\s*confirmStore\.focusRef = target\.ref/)
  })

  it('跳转之外的贴底路径仍然存在、仍按用户意图让位（beginJump 只借用 markUpIntent，不新增写 scrollTop 的地方）', () => {
    const body = fnBody(list, 'function beginJump(')
    assert.ok(!/scrollTop\s*=/.test(body) && !/scrollTo\(|scrollIntoView\(/.test(body), 'beginJump 自己不滚动')
    assert.match(fnBody(list, 'function repinIfPinned('), /userHolding\(lastUpIntentAt, performance\.now\(\)\)/)
    assert.match(fnBody(list, 'function followGateOpen('), /holding: userHolding\(lastUpIntentAt, performance\.now\(\)\)/)
  })
})

describe('向上滚时的「新动态」浮动条（#254）：数字有人维护', () => {
  const list = read(LIST)

  it('不再只数新增的轮次：旧的「+= 新增轮数」与「1 条时不带数字」都没了', () => {
    assert.ok(!/unreadNewMessagesCount\.value \+= /.test(list), '数字不能再靠累加新增轮数得到（通知 / 任务进展 / 提问卡都漏了、也不会随滚动变化）')
    assert.ok(!list.includes('收到 AI 最新方案汇报'), '只有一条时也要带数字')
    assert.match(list, /\{\{ newDynamicsText\(unreadNewMessagesCount\) \}\} · 点击直达最新 ↓/)
  })

  it('离开底部那一刻记 awayCursor（已渲染的最晚 Agent 消息；为 0 时不记），回到底部 / 点浮动条 / 切换工作流 / 显式到底部都清空', () => {
    const scroll = fnBody(list, 'function handleScroll(')
    assert.match(scroll, /if \(scrolledUp\) \{\s*\n\s*if \(awayCursor\.value === null\) \{\s*\n\s*const rendered = renderedAgentMs\(unreadList\.value\)\s*\n\s*if \(rendered > 0\) awayCursor\.value = rendered\s*\n\s*\}\s*\n\s*scheduleRecountNewBelow\(\)\s*\n\s*\} else clearAway\(\)/)
    assert.match(fnBody(list, 'function scrollToBottom('), /clearAway\(\)/)
    assert.match(fnBody(list, 'function handleJumpToLatest('), /clearAway\(\)/)
    // 切换工作流（conversationRounds 的监听里，换了 targetWId 那一支）
    assert.match(list, /previewSearch\.close\(\) \/\/ 搜索面板是按工作流搜的，换了工作流就关掉\s*\n\s*clearAway\(\)/)
    assert.match(fnBody(list, 'function clearAway('), /awayCursor\.value = null\s*\n\s*seenNewKeys\.clear\(\)\s*\n\s*unreadNewMessagesCount\.value = 0/)
  })

  it('数字 = newDynamics 里还没看到的条数：顶边在视口底边之上算看到，看过的记进 seenNewKeys，没渲染出来的按没看到算', () => {
    assert.match(list, /const newDynamics = computed\(\(\) => \(awayCursor\.value === null \? \[\] : newSinceAway\(unreadList\.value, awayCursor\.value\)\)\)/)
    const body = fnBody(list, 'function recountNewBelow(')
    assert.match(body, /const limit = container\.getBoundingClientRect\(\)\.bottom - 1/)
    assert.match(body, /unseenNewCount\(\s*\n\s*list,/)
    assert.match(body, /return !!el && el\.getBoundingClientRect\(\)\.top < limit/)
    assert.match(body, /for \(const key of seen\) seenNewKeys\.add\(key\)\s*\n\s*unreadNewMessagesCount\.value = count/)
    // 元素：间隙里的块 data-timeline-key，轮次 id = session-<session_id>
    const el = fnBody(list, 'function itemElOf(')
    assert.match(el, /data-timeline-key="\$\{CSS\.escape\(key\)\}"/)
    assert.match(el, /session-\$\{key\}/)
  })

  it('重算的时机：滚动（rAF 合并）与列表变了（DOM 更新之后）；卸载时取消，不写 scrollTop', () => {
    const sched = fnBody(list, 'function scheduleRecountNewBelow(')
    assert.match(sched, /if \(recountRaf\) return/)
    assert.match(sched, /requestAnimationFrame\(\(\) => \{\s*\n\s*recountRaf = 0\s*\n\s*recountNewBelow\(\)/)
    assert.match(list, /watch\(newDynamics, \(\) => \{\s*\n\s*if \(awayCursor\.value !== null\) scheduleRecountNewBelow\(\)\s*\n\}, \{ flush: 'post' \}\)/)
    assert.match(list, /onBeforeUnmount\(\(\) => \{[^]*?cancelRecountNewBelow\(\)/)
    for (const name of ['recountNewBelow', 'scheduleRecountNewBelow', 'clearAway']) {
      assert.ok(!/scrollTop\s*=|scrollTo\(|scrollIntoView\(/.test(fnBody(list, `function ${name}(`)), `${name} 只读几何，不写滚动位置`)
    }
  })

  it('浮动条外层不被 left-1/2 的收缩适配宽度卡成半宽：要 w-max + max-w，手机上文案一行放得下', () => {
    // 回归：absolute + left-1/2 + 宽度 auto 时，可用宽度只剩容器的一半，手机宽度下「收到 N 条新动态 · 点击直达最新 ↓」折成两行（实验室截图看到的）
    assert.match(list, /<div\s*\n\s*v-if="unreadNewMessagesCount > 0"\s*\n\s*class="absolute bottom-12 sm:bottom-14 left-1\/2 -translate-x-1\/2 w-max max-w-\[92%\] z-40 select-none pointer-events-auto"/)
  })
})

