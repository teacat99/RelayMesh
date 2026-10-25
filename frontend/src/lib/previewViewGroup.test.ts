// 运行：pnpm --dir frontend run test:unit
// 群聊窗口的源码级防线（纯逻辑见 groupCard.test.ts）。预览视图阶段 E-d1～E-d3（#120）做了群聊的消息卡、成员头像与未读角标；
// #165 第二步把群聊窗口改用预览视图的组件重写，旧的聊天视图三件（ChatView / ChatBubble / ChatComposer）已删除，这里守住：
//   · 旧三件不存在、没有任何源码再引用它们；群页挂的是 GroupChatPane，且只有群编号（grp:）才会打开窗口；
//   · 群聊窗口只有一种画法（统一消息卡），不再受「预览视图」开关影响；
//   · 卡片里的发送者 = 头像字 + 名称 + 成员色（由 lib/groupCard 稳定分配），群里没有「汇报 / 回复」类型标签；
//   · 已读 / 投递 / @你的文案与 data-testid 不变；引用回复这条线已删除（群里的消息编号是 m:…，从来没有入口）；
//   · 「以下为新消息」只在打开群、取到第一页时算一次，之后不随游标与新消息重算（否则分隔线会在眼前跳）；
//   · 系统行 / 确认卡 / 决策卡 / 任务进展由事件行（GroupEventRow）画，DOM 与 data-testid 不变；
//   · 输入框只留群聊需要的（@ 补全、归档禁用、草稿、输入法守护），草稿键不变。
import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewGroup.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 去掉模板 / 脚本里的注释，避免注释里提到的词干扰断言。 */
function code(src: string): string {
  return src.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (/\.(vue|ts)$/.test(name)) out.push(full)
  }
  return out
}

const PANE = 'components/group/GroupChatPane.vue'
const EVENT_ROW = 'components/group/GroupEventRow.vue'
const COMPOSER = 'components/group/GroupComposer.vue'
const GROUP_CARD = 'components/group/GroupMessageCard.vue'
const GROUP_PAGE = 'components/group/GroupPage.vue'
const MESSAGE_CARD = 'components/feedback/preview-view/PreviewMessageCard.vue'

describe('#165 第二步：旧的聊天视图三件已删除，群页挂群聊窗口', () => {
  it('ChatView / ChatBubble / ChatComposer 与 components/chat 目录都不存在', () => {
    for (const rel of ['components/chat/ChatView.vue', 'components/chat/ChatBubble.vue', 'components/chat/ChatComposer.vue', 'components/chat']) {
      assert.ok(!existsSync(join(srcDir(), rel)), `${rel} 应已删除`)
    }
  })

  it('没有任何源码（注释除外）再引用它们，也没有 components/chat/ 路径', () => {
    const root = srcDir()
    const users = walk(root)
      .map(f => relative(root, f).split('\\').join('/'))
      .filter(rel => !rel.endsWith('.test.ts'))
      .filter(rel => /\b(ChatView|ChatBubble|ChatComposer)\b|components\/chat\//.test(code(readFileSync(join(root, rel), 'utf8'))))
    assert.deepEqual(users, [], `这些文件还在引用已删除的聊天视图组件：${users.join(', ')}`)
  })

  it('群页挂 GroupChatPane，且只有群编号（grp:）才打开窗口；别的编号指回群列表', () => {
    const page = code(read(GROUP_PAGE))
    assert.match(page, /import GroupChatPane from '\.\/GroupChatPane\.vue'/)
    assert.match(page, /<GroupChatPane v-if="groupId && isGroupId\(groupId\)" :key="groupId" :workflow-id="groupId" \/>/)
    assert.match(page, /v-else-if="groupId"[\s\S]*?data-testid="group-invalid"[\s\S]*?data-testid="group-invalid-back"/)
  })
})

describe('群聊窗口（GroupChatPane）的接线：只有一种画法', () => {
  const pane = code(read(PANE))

  it('不依赖「预览视图」开关，也没有单会话的分支', () => {
    assert.ok(!/usePreviewView|previewView/.test(pane), '群聊窗口不受预览视图开关影响')
    assert.ok(!/\bisGroup\b|\bcards\b|\bwaiting\b|chat-exit|MessagesSquare/.test(pane), '单会话 / 气泡模式的分支应已删除')
  })

  it('渲染分流：groupRenderKind 判为 card 的消息套统一消息卡，其余走事件行', () => {
    assert.match(pane, /v-if="groupRenderKind\(r\.message\) === 'card'"/)
    assert.match(pane, /<GroupMessageCard\b/)
    assert.match(pane, /<GroupEventRow v-else :message="r\.message" \/>/)
  })

  it('日期分隔与预览视图同一条（PreviewDateSeparator），没有气泡版的日期胶囊', () => {
    assert.match(pane, /<PreviewDateSeparator :label="r\.label \?\? ''" \/>/)
    assert.ok(!/chat-date/.test(pane))
  })

  it('成员色：成员表算一次（assignMemberHues），逐张卡传下去，不在每张卡里重算', () => {
    assert.match(pane, /const hues = computed\(\(\) => assignMemberHues\(store\.group\?\.members \?\? \[\]\)\)/)
    assert.match(pane, /<GroupMessageCard[\s\S]*?:hues="hues"/)
    assert.ok(!/assignMemberHues/.test(code(read(GROUP_CARD))), '卡片组件只查表，不分配颜色')
  })

  it('「以下为新消息」：打开群、取到第一页之后算一次（按您的成员已读序号）', () => {
    const calls = pane.match(/firstUnreadId\(/g) ?? []
    assert.equal(calls.length, 1, 'firstUnreadId 只在 openConversation 里调用一次')
    const open = pane.slice(pane.indexOf('async function openConversation()'), pane.indexOf('watch(() => props.workflowId'))
    assert.match(open, /unreadDividerId\.value = ''\s*\n\s*await store\.open\(props\.workflowId\)\s*\n\s*unreadDividerId\.value = firstUnreadId\(store\.items, humanReadSeq\(\), store\.hasMoreBefore\)/)
    assert.match(pane, /store\.group\?\.members\.find\(m => m\.kind === 'human'\)\?\.read_seq \?\? 0/, '游标取您这位人类成员的 read_seq')
    assert.match(pane, /v-if="r\.message\.id === unreadDividerId"[\s\S]*?<PreviewUnreadDivider \/>/)
  })

  it('卡片放在居中的一列里（与预览视图的「内容居中」一致）', () => {
    assert.match(pane, /<div class="mx-auto max-w-3xl">\s*<GroupMessageCard/)
  })

  it('已读游标 / 翻历史保持位置 / 停底部补贴底 / 发消息后滚到底：行为代码都在', () => {
    assert.match(pane, /document\.visibilityState === 'visible' && atBottom\.value && !store\.hasMoreAfter\) void store\.markRead\(\)/)
    assert.match(pane, /el\.scrollTop = top \+ \(el\.scrollHeight - before\)/)
    assert.match(pane, /if \(atBottom\.value && !store\.hasMoreAfter && !store\.loadingOlder\) scrollToBottom\(\)/)
    assert.match(pane, /composer\.value\?\.reset\(\)[\s\S]*?atBottom\.value = true\s*\n\s*scrollToBottom\(\)/)
  })

  it('发送只带文字与点名（群聊没有图片、「作为追加」与引用回复）', () => {
    assert.match(pane, /await store\.send\(\{\s*text: p\.text,\s*mentions: p\.mentions\.length \? p\.mentions : undefined,\s*\}\)/)
    // pane 是去掉注释的源码：不能拿注释当终点（找不到会变成 -1，一路切到文件末尾，把模板也算进来）；终点取下一个函数
    const send = pane.slice(pane.indexOf('async function onSend'), pane.indexOf('function scrollToMessage'))
    assert.ok(send.length > 0 && send.length < 600, 'onSend 函数体应只是一小段')
    assert.ok(send.includes('store.send') && !/images|target:|reply_to|replyTo/.test(send))
  })

  it('引用回复这条线已经删除：群里的消息编号是 m:…，从来没有「引用」入口（只有已删除的单会话聊天窗口有）', () => {
    for (const rel of [PANE, COMPOSER, GROUP_CARD]) {
      const src = code(read(rel))
      assert.ok(!/chat-reply-chip|chat-quote|replyTo|reply-to|onReply|onJump|quotedOf|findQuoted|splitQuote|canQuote|reply_to/.test(src), `${rel} 里不该再有引用回复的残留`)
    }
  })

  it('页头：群名、头像堆（点一下开 / 关成员面板，最多 3 位 + 「+N」）、成员数（手机上让位）、已归档、搜索、成员', () => {
    assert.match(pane, /const stack = computed\(\(\) => avatarStack\(store\.group\?\.members \?\? \[\], 3\)\)/)
    assert.match(pane, /v-if="stack\.shown\.length"[\s\S]*data-testid="group-avatar-stack"[\s\S]*@click="membersOpen = !membersOpen"/)
    assert.match(pane, /data-testid="group-avatar-more"/)
    assert.match(pane, /class="hidden text-\[11px\] text-muted-foreground sm:inline" data-testid="group-member-count"/)
    assert.match(pane, /:class="archived \? 'opacity-60 grayscale' : ''"/)
  })

  it('成员头像传给面板与 @ 选择器；归档的群输入框不可用并说明原因', () => {
    // 成员面板拿的是叠加了实时状态的那份（页头徽标与面板读同一份，#241 第三步），不是服务端随群页带的原始对象
    assert.match(pane, /<GroupMembersPanel :info="liveGroup" :hues="hues"/)
    assert.doesNotMatch(pane, /<GroupMembersPanel :info="store\.group"/)
    assert.match(pane, /const pickerIdentities = computed\(\(\) => candidateIdentities\(candidates\.value, store\.group\?\.members \?\? \[\], hues\.value\)\)/)
    assert.match(pane, /:identities="pickerIdentities"/)
    assert.match(pane, /:disabled="archived"/)
    assert.match(pane, /disabled-reason="这个群已归档，不能再发消息。在成员面板里取消归档后可继续。"/)
  })

  it('页头群级状态徽标（#241 第三步）：读叠加了实时状态的成员；已归档的群不画；点它开关成员面板；挂载时取一次实时状态', () => {
    assert.match(pane, /<GroupStatusBadge v-if="!archived" :members="liveMembers" :expanded="membersOpen" @toggle="membersOpen = !membersOpen" \/>/)
    assert.match(pane, /const liveMembers = computed\(\(\) => overlayLiveState\(store\.group\?\.members \?\? \[\], wf => agentStore\.entryFor\(wf\)\.primary\)\)/)
    assert.match(pane, /const liveGroup = computed\(\(\) => \(store\.group \? \{ \.\.\.store\.group, members: liveMembers\.value \} : null\)\)/)
    assert.match(pane, /agentStore\.ensureFresh\(\)/)
  })

  it('所有对外可见的 data-testid 都还在（e2e 与外部脚本靠它们定位）', () => {
    const all = [PANE, EVENT_ROW, COMPOSER, GROUP_CARD].map(rel => code(read(rel))).join('\n')
    const ids = [
      'chat-view', 'chat-title', 'chat-scroll', 'chat-composer', 'chat-input', 'chat-send', 'mention-picker', 'mention-option', 'chat-disabled',
      'chat-message', 'chat-reads', 'chat-mentions-you', 'chat-deliveries', 'chat-attachments',
      'chat-system', 'chat-card', 'chat-decision', 'chat-task-update',
      'chat-search-toggle', 'chat-to-latest', 'chat-error', 'chat-start', 'chat-empty', 'chat-loading',
      'group-avatar-stack', 'group-avatar-more', 'group-member-count', 'group-archived-badge', 'group-members-toggle', 'group-cross-banner',
    ]
    const missing = ids.filter(id => !all.includes(`data-testid="${id}"`))
    assert.deepEqual(missing, [], `缺少 data-testid：${missing.join(', ')}`)
  })

  it('搜索用预览视图的同一个面板（#241）：群页只管开关与落点，不再自带输入框 / 结果列表 / 搜索状态', () => {
    // 面板的钩子换成预览视图的那一套（preview-search / -input / -results / -hit），旧的 chat-search / -input / -results / data-result-id 不再有
    assert.match(pane, /import PreviewSearchPanel from '\.\.\/feedback\/preview-view\/PreviewSearchPanel\.vue'/)
    assert.match(
      pane,
      /<PreviewSearchPanel\s+v-if="searchOpen"\s+:workflow-id="workflowId"\s+placeholder="搜索这个群的全部消息"\s+:kinds="SEARCH_ALL_KINDS"\s+@close="searchOpen = false"\s+@pick="openResult"\s*\/>/,
    )
    // 群消息的搜索由服务端只按文字消息匹配、不看 kinds 参数：传空数组 = 不带 kinds（conversationsApi.list 对空数组省略该参数）
    assert.match(pane, /const SEARCH_ALL_KINDS: readonly string\[\] = \[\]/)
    // 点中结果后面板自己收成一行输入条（目标露出来），群页的落点函数不关它
    assert.match(pane, /async function openResult\(m: ChatMessage\) \{\s+const known = /)
    assert.ok(!/searchText|searchResults|searching|searchTimer|store\.search|chat-search-input|chat-search-results|data-result-id|data-testid="chat-search"/.test(pane), '群页里不该再有自己的搜索输入 / 结果 / 状态')
    // 切换群时仍收起面板（旧群的搜索词不带到新群）
    assert.match(pane, /async function openConversation\(\) \{\s+searchOpen\.value = false/)
  })

  it('右侧缩略轴用预览视图的同一条轨（#241 第二步）：别的成员说的话上轨，点刻度落到那条消息，输入框不盖列表所以不按「悬浮行」让位', () => {
    assert.match(pane, /import PreviewTimelineRail from '\.\.\/feedback\/preview-view\/PreviewTimelineRail\.vue'/)
    assert.match(
      pane,
      /<PreviewTimelineRail\s+:scroll-el="scroller"\s+:has-earlier="store\.hasMoreBefore"\s+:loading-earlier="store\.loadingOlder"\s+:has-draft-images="false"\s+:dock="false"\s+@jump="onRailJump"\s+@load-earlier="loadOlderKeepingPosition"\s*\/>/,
    )
    // 落点就是搜索命中用的那个函数（消息都已载入，滚过去并闪一下）；「更早」就是顶部哨兵用的翻页函数
    assert.match(pane, /function onRailJump\(item: RailItem\) \{\s+scrollToMessage\(item\.id\)\s+\}/)
    // 轨挂在消息区的相对定位容器里（与搜索面板、「回到最新」同一个容器），不是挂在页面根上
    const wrap = pane.indexOf('<div class="relative min-h-0 flex-1">')
    assert.ok(wrap > 0 && pane.indexOf('<PreviewTimelineRail') > wrap, '轨在消息区容器里面')
    // 轨占右侧 38px：内容列从 md 起右侧留白（右对齐的卡不压在刻度下面）；出错提示 z-30 盖在轨（z-20）上面，「重试」不被刻度挡住
    assert.match(pane, /<div ref="content" class="md:pr-9">/)
    assert.match(pane, /<div v-if="store\.error" class="absolute inset-x-3 top-2 z-30 /)
    // 群消息卡把「别的成员的发言」写成 data-rail-*（轨扫的就是它）；您自己的话不上轨由 railMetaOfGroupMessage 判定
    const card = code(read(GROUP_CARD))
    assert.match(card, /import \{ railDomAttrs, railMetaOfGroupMessage \} from '\.\.\/\.\.\/lib\/timelineRail'/)
    assert.match(card, /const railAttrs = computed\(\(\) => \{\s+const meta = railMetaOfGroupMessage\(m\.value\)\s+return meta \? railDomAttrs\(meta\) : \{\}\s+\}\)/)
    assert.match(card, /<PreviewMessageCard[\s\S]*?v-bind="railAttrs"[\s\S]*?data-testid="chat-message"/)
    // 共用的轨组件：dock 默认 true（会话页不变），false 时下沿只让开「回到最新」
    const rail = code(read('components/feedback/preview-view/PreviewTimelineRail.vue'))
    assert.match(rail, /dock\?: boolean/)
    assert.match(rail, /\{ dock: true \}/)
    assert.match(rail, /!dock \? 'bottom-14' : hasDraftImages \? 'bottom-44' : 'bottom-28'/)
  })

  it('「回到最新」换了一整段内容后：先把「在底部」记成真再直接贴底——不靠滚动事件的时序，也不用会被翻页打断的平滑滚动（#241 第二步里 e2e 抓到的竞态）', () => {
    assert.match(pane, /async function toLatest\(\) \{\s+await store\.backToLatest\(\)\s+await nextTick\(\)\s+atBottom\.value = true\s+scrollToBottom\(\)\s+\}/)
    // 点按钮：还有更新的一段（搜索跳转进了历史）就走 toLatest，否则才是同一页内的平滑滚动
    assert.match(pane, /@click="store\.hasMoreAfter \? toLatest\(\) : scrollToBottom\(true\)"/)
  })
})

describe('事件行（GroupEventRow）：系统行 / 确认卡 / 决策卡 / 任务进展', () => {
  const src = code(read(EVENT_ROW))

  it('四种行各有 data-testid 与 data-message-id（跳转与搜索定位靠它），分支顺序与 groupRenderKind 一致', () => {
    const order = ['chat-system', 'chat-card', 'chat-decision', 'chat-task-update'].map(id => src.indexOf(`data-testid="${id}"`))
    assert.ok(order.every(i => i > 0) && [...order].sort((a, b) => a - b).join() === order.join(), '四个分支都在，且按 系统 → 确认卡 → 决策 → 任务 的顺序')
    assert.equal((src.match(/:data-message-id="m\.id"/g) ?? []).length, 4)
    assert.match(src, /v-if="isSystem"/)
    assert.match(src, /v-else-if="isCard && m\.card"/)
    assert.match(src, /v-else-if="isDecision && decisionGroup"/)
    assert.match(src, /v-else-if="isTask && m\.task"/)
  })

  it('确认卡与决策汇总卡沿用交流页里的组件，作答就在卡上', () => {
    assert.match(src, /<ConfirmTimelineCard :item="m\.card" \/>/)
    assert.match(src, /<DecisionGroupCard :group="decisionGroup" \/>/)
  })

  it('不画「谁说了什么」：不引用头像 / 消息卡 / 成员色模块', () => {
    assert.ok(!/MemberAvatar|PreviewMessageCard|GroupMessageCard|groupCard/.test(src))
  })
})

describe('群聊消息卡（GroupMessageCard）', () => {
  const src = read(GROUP_CARD)
  const srcCode = code(src)

  it('根是统一消息卡：具名发送者（名称 / 头像字 / 色相）取自 senderIdentity，不画类型标签（进度通知除外）', () => {
    assert.match(srcCode, /<PreviewMessageCard\b/)
    for (const prop of [':sender-name="ident.name"', ':sender-avatar="ident.avatar"', ':hue="ident.hue"', ':hide-type="hideType"']) {
      assert.ok(srcCode.includes(prop), `缺少 ${prop}`)
    }
    assert.match(srcCode, /const ident = computed\(\(\) => senderIdentity\(m\.value\.sender, props\.hues\)\)/)
    assert.match(srcCode, /const type = computed<CardType>\(\(\) => \(mine\.value \? 'self' : isNotice\.value \? 'notice' : 'member'\)\)/)
    assert.match(srcCode, /const hideType = computed\(\(\) => !isNotice\.value\)/)
  })

  it('跳转 / 搜索定位靠 data-message-id，e2e 靠 chat-message / data-sender / data-mentions-you', () => {
    for (const attr of ['data-testid="chat-message"', ':data-message-id="m.id"', ':data-sender="m.sender.kind"', ':data-status="m.status"', ':data-mentions-you="mentionsYou ? \'true\' : undefined"']) {
      assert.ok(srcCode.includes(attr), `缺少 ${attr}`)
    }
  })

  it('已读 / 投递 / @你：文案取同一份函数，testid 不变', () => {
    assert.match(srcCode, /readsLabel\(m\.value\.reads\)/)
    assert.match(srcCode, /readsTitle\(m\.value\.reads\)/)
    for (const id of ['chat-reads', 'chat-mentions-you', 'chat-deliveries', 'chat-attachments']) {
      assert.ok(srcCode.includes(`data-testid="${id}"`), `缺少 ${id}`)
    }
    // 投递状态的文字表
    assert.match(srcCode, /const DELIVERY_TEXT[^=]*= \{ delivered: '已送达', read: '已读', handled: '已处理', retracted: '已撤回', expired: '已过期' \}/)
    // 「@你」：只对别人（非您）的消息、且点名了 human
    assert.match(srcCode, /const mentionsYou = computed\(\(\) => !mine\.value && !!m\.value\.mentions\?\.some\(x => x\.ref === 'human'\)\)/)
  })

  it('脚注只在有内容时才出现（#footer 槽按条件定义），避免每张卡多一圈空白', () => {
    assert.match(srcCode, /<template v-if="hasFooter" #footer>/)
    assert.match(srcCode, /const hasFooter = computed\(\(\) => !!\(reads\.value \|\| mentionsYou\.value \|\| deliveries\.value\)\)/)
  })

  it('智能体的正文走 PreviewMarkdown（Markdown + 文档引用卡），您自己的话是纯文本并先抽掉 [[artifact:N]] 记号', () => {
    assert.match(srcCode, /<PreviewMarkdown v-else-if="rawBody" :content="rawBody" :workflow-id="m\.sender\.workflow_id \|\| workflowId" \/>/)
    assert.match(srcCode, /<p v-if="mineExtracted\.text" class="whitespace-pre-wrap break-words">/)
    assert.match(srcCode, /extractArtifactRefs\(rawBody\.value\)/)
  })

  it('已读 / 投递数据只来自服务端字段，不在前端造数', () => {
    assert.ok(!/Math\.random|Date\.now\(\)/.test(srcCode))
  })
})

describe('预览视图阶段 E-d1：统一消息卡骨架的具名扩展（PreviewMessageCard）', () => {
  const src = read(MESSAGE_CARD)
  const srcCode = code(src)

  it('新增的 props 都是可选的，单会话的各种卡（不传）行为不变', () => {
    for (const p of ['senderName?: string', 'senderAvatar?: string', 'hue?: number | null', 'hideType?: boolean']) {
      assert.ok(srcCode.includes(p), `缺少可选 prop ${p}`)
    }
  })

  it('有成员色相：色调取成员色（--card-hue 内联变量），否则按类型取色调；颜色只来自词汇表', () => {
    assert.match(srcCode, /hued\.value \? CARD_HUE_TONE_CLASS : CARD_TONE_CLASS\[meta\.value\.tone\]/)
    assert.match(srcCode, /'--card-hue': String\(props\.hue\)/)
    assert.match(srcCode, /:style="hueStyle"/)
    assert.match(read('lib/messageCard.ts'), /CARD_HUE_TONE_CLASS = '\[--card-tone:hsl\(var\(--card-hue\)_55%_36%\)\] dark:\[--card-tone:hsl\(var\(--card-hue\)_70%_68%\)\]'/)
  })

  it('具名时名称手机上也显示（群里要分清是谁说的），不具名时仍只在 sm 以上显示（让位给标题）；类型标签可隐藏', () => {
    assert.match(srcCode, /props\.senderName \? '[^']*truncate[^']*' : 'hidden shrink-0 font-semibold text-foreground sm:inline'/)
    assert.ok(!/props\.senderName \? '[^']*\bhidden\b/.test(srcCode), '具名的类名里不能有 hidden')
    assert.match(srcCode, /<span v-if="!hideType" :class="CARD_CHIP_CLASS" data-testid="card-type">/)
  })
})

describe('群聊输入框（GroupComposer）：只留群聊需要的', () => {
  const composer = code(read(COMPOSER))

  it('没有图片、「作为追加」、Agent 等待文案——群聊用不到', () => {
    assert.ok(!/images|asInbox|waiting|FileReader|chat-attach|chat-as-inbox|onPaste/.test(composer))
  })

  it('草稿按群存在本机，键沿用 rm-chat-draft:<grp:…>（用户已有的草稿不丢）', () => {
    assert.ok(composer.includes('rm-chat-draft:${props.workflowId}'))
    assert.match(composer, /localStorage\.getItem\(draftKey\.value\)/)
    assert.match(composer, /localStorage\.setItem\(draftKey\.value, text\.value\)/)
  })

  it('Enter 发送、Shift+Enter 换行、输入法组字时 Enter 只是上屏', () => {
    assert.match(composer, /if \(e\.key !== 'Enter' \|\| e\.shiftKey \|\| e\.altKey\) return\s*\n\s*if \(isImeKey\(e\)\) return\s*\n\s*e\.preventDefault\(\)\s*\n\s*submit\(\)/)
    assert.match(composer, /if \(pickerOpen\.value && !isImeKey\(e\)\)/, '@ 补全打开时的选择键也要让输入法先上屏')
  })

  it('发送的内容 = 文字与点名；发送中 / 归档 / 空文字不能发；对外只暴露 reset（发送成功后清输入）', () => {
    assert.match(composer, /emit\('send', \{ text: text\.value\.trim\(\), mentions \}\)/)
    assert.match(composer, /defineExpose\(\{ reset \}\)/)
    assert.match(composer, /const canSend = computed\(\(\) => !props\.disabled && !props\.sending && text\.value\.trim\(\)\.length > 0\)/)
  })

  it('@ 选择器：候选与头像身份由窗口传入，选项前画成员头像', () => {
    assert.match(composer, /candidates: MentionCandidate\[\]/)
    assert.match(composer, /identities: Record<string, SenderIdentity>/)
    assert.match(composer, /<MemberAvatar v-if="identities\[c\.ref\]" :identity="identities\[c\.ref\]" \/>/)
    assert.match(composer, /data-testid="mention-option"/)
  })

  it('外观与会话页输入区对齐（#241 第四步）：底面与留白同一套、输入框下面一行操作行（左快捷键提示、右带字的小号发送）；只对齐外观，不加功能', () => {
    const dock = read('components/feedback/FeedbackInputDock.vue')
    // 基准：会话页输入区的底面 / 留白 / 分隔线 / 快捷键提示（它们变了，这里要跟着改，这条测试就是提醒）
    assert.ok(dock.includes('bg-background/95 backdrop-blur-xs px-3 sm:px-6'), '会话页输入区的底面与留白变了：群聊输入框要跟着对齐')
    assert.ok(dock.includes("'border-t border-border/70 transition-[height] duration-150'"), '会话页输入区的分隔线变了')
    assert.ok(dock.includes('hidden lg:inline text-muted-foreground/80 text-[11px]">按 <kbd class="px-1.5 py-0.5 rounded-xs bg-muted border border-border font-mono text-[10px]">'), '会话页输入区的快捷键提示变了')
    assert.ok(dock.includes('h-7 px-3 sm:px-3.5 text-xs rounded-sm gap-1'), '会话页输入区的发送按钮变了')

    const root = /<div\s+class="([^"]*)"\s+data-testid="chat-composer"/.exec(composer)?.[1] ?? ''
    for (const cls of ['border-t', 'border-border/70', 'bg-background/95', 'backdrop-blur-xs', 'px-3', 'sm:px-6']) {
      assert.ok(root.split(/\s+/).includes(cls), `输入框的底面应带 ${cls}：${root}`)
    }
    assert.match(composer, /data-testid="mention-picker"/)
    assert.ok(/class="absolute inset-x-3 [^"]*sm:inset-x-6"/.test(composer), '@ 补全的左右边要跟着留白走')

    // 操作行：提示在左（宽屏才显示，和会话页一样用 lg）、发送在右；发送按钮是带字的小号矩形按钮，不再是圆形图标按钮
    assert.match(composer, /<span class="hidden text-\[11px\] text-muted-foreground\/80 lg:inline" data-testid="chat-hint">/)
    assert.match(composer, /<kbd class="rounded-xs border border-border bg-muted px-1\.5 py-0\.5 font-mono text-\[10px\]">Enter<\/kbd> 发送/)
    assert.match(composer, /<kbd class="[^"]*">Shift \+ Enter<\/kbd> 换行/)
    assert.match(composer, /<Button\s+class="ml-auto h-7 shrink-0 cursor-pointer gap-1 rounded-sm px-3 text-xs font-medium sm:px-3\.5"[\s\S]*?data-testid="chat-send"/)
    assert.match(composer, /<Send class="size-3" \/>\s*<span>\{\{ sending \? '发送中…' : '发送' \}\}<\/span>/)
    assert.ok(!/SendHorizontal|rounded-full/.test(composer), '圆形图标发送按钮应已去掉')
    // 输入框占满一行（不再与按钮并排）；放在操作行之前
    assert.ok(composer.indexOf('data-testid="chat-input"') < composer.indexOf('data-testid="chat-hint"'))
    assert.ok(composer.indexOf('data-testid="chat-hint"') < composer.indexOf('data-testid="chat-send"'))
    assert.match(composer, /<textarea\s+ref="area"\s+v-model="text"\s+rows="1"\s+class="block [^"]*w-full /)
  })

  it('输入框长高时把上下边框补回去、没长到上限不出滚动条（一行高的空输入框右边不能冒出一根滚动条）', () => {
    // scrollHeight 不含边框，而盒子是 border-box：少补这两条边框，空输入框会差 2px 而冒出滚动条（#241 第四步看截图时发现，旧版同样有）
    assert.match(composer, /const MAX_HEIGHT = 168/)
    assert.match(composer, /const want = el\.scrollHeight \+ \(el\.offsetHeight - el\.clientHeight\)/)
    assert.match(composer, /el\.style\.height = `\$\{Math\.min\(want, MAX_HEIGHT\)\}px`/)
    assert.match(composer, /el\.style\.overflowY = want > MAX_HEIGHT \? 'auto' : 'hidden'/)
    assert.ok(/<textarea[^>]*class="[^"]*\bmax-h-42\b/.test(composer), '样式上限 max-h-42（168px）要与 MAX_HEIGHT 是同一个数')
  })
})

describe('预览视图阶段 E-d2：成员头像（页头头像堆 / 成员面板 / @ 选择器）', () => {
  const AVATAR = 'components/group/MemberAvatar.vue'
  const PANEL = 'components/group/GroupMembersPanel.vue'
  const avatar = code(read(AVATAR))
  const panel = code(read(PANEL))

  it('头像组件：成员按色相取色（与消息卡同一份词汇表），您固定绿、系统 / 所有人石板色；状态点只在传了 dotClass 时才画', () => {
    assert.match(avatar, /hued\.value \? CARD_HUE_TONE_CLASS : props\.identity\.kind === 'user' \? CARD_TONE_CLASS\.green : CARD_TONE_CLASS\.slate/)
    assert.match(avatar, /'--card-hue': String\(props\.identity\.hue\)/)
    assert.match(avatar, /bg-\[color:var\(--card-tone\)\]/)
    assert.match(avatar, /data-testid="member-avatar"/)
    assert.match(avatar, /v-if="dotClass"[\s\S]*data-testid="member-status-dot"/)
  })

  it('成员面板：hues 是可选的——不传就是纯状态点，传了才画头像 + 状态点，状态行缩进随之调整', () => {
    assert.match(panel, /hues\?: Record<string, number>/)
    assert.match(panel, /<MemberAvatar v-if="hues" :identity="memberIdentity\(human, hues\)" size="md" \/>\s*<span v-else class="size-2 shrink-0 rounded-full bg-primary" \/>/)
    assert.match(panel, /<MemberAvatar v-if="hues" :identity="memberIdentity\(m, hues\)" size="md" :dot-class="TONE_CLASS\[memberTone\(m\)\]" \/>\s*<span v-else class="size-2 shrink-0 rounded-full" :class="TONE_CLASS\[memberTone\(m\)\]" \/>/)
    assert.match(panel, /const indentClass = computed\(\(\) => \(props\.hues \? 'pl-7' : 'pl-4'\)\)/)
    assert.equal((panel.match(/:class="indentClass"/g) ?? []).length, 2, '状态行与推送行都用 indentClass')
    assert.ok(!/class="[^"]*\bpl-4\b[^"]*"/.test(panel.replace(/<script[\s\S]*?<\/script>/, '')), '缩进只来自 indentClass，模板里不再写死 pl-4')
  })

  it('头像组件与成员面板不依赖预览视图开关（由调用方决定画不画）', () => {
    assert.ok(!/usePreviewView|previewView/.test(avatar))
    assert.ok(!/usePreviewView|previewView/.test(panel))
  })
})

describe('预览视图阶段 E-d3：群未读与「@我」角标（侧栏与群列表）', () => {
  const sidebar = code(read('components/sidebar/SidebarGroups.vue'))
  const list = code(read(GROUP_PAGE))

  it('侧栏每个群：有人点名您显示「@我 N」（99+ 封顶），否则显示未读数；两种角标都有 testid', () => {
    assert.match(sidebar, /v-if="g\.mentioned > 0"[^>]*data-testid="sidebar-group-item-mentioned">@我 \{\{ g\.mentioned > 99 \? '99\+' : g\.mentioned \}\}<\/span>\s*<span v-else-if="g\.unread > 0"[^>]*data-testid="sidebar-group-item-unread">\{\{ g\.unread > 99 \? '99\+' : g\.unread \}\}<\/span>/)
  })

  it('侧栏总入口：被点名的总数优先于未读总数（沿用）', () => {
    assert.match(sidebar, /v-if="groups\.totals\.mentioned > 0"[^>]*data-testid="sidebar-group-mentioned">@我 \{\{ groups\.totals\.mentioned \}\}/)
    assert.match(sidebar, /v-else-if="badge > 0"[^>]*data-testid="sidebar-group-unread"/)
  })

  it('群列表每一行：同口径（「@我 N」优先、否则未读数 99+ 封顶），角标数据只来自服务端摘要', () => {
    assert.match(list, /v-if="g\.mentioned > 0"[^>]*data-testid="group-item-mentioned">@我 \{\{ g\.mentioned \}\}/)
    assert.match(list, /v-else-if="g\.unread > 0"[^>]*data-testid="group-item-unread">\{\{ g\.unread > 99 \? '99\+' : g\.unread \}\}/)
    assert.ok(!/Math\.random|Date\.now\(\)/.test(sidebar + list), '角标不在前端造数')
  })
})
