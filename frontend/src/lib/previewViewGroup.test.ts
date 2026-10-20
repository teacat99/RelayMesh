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
    const send = pane.slice(pane.indexOf('async function onSend'), pane.indexOf('// ---- 定位到某条消息'))
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
    assert.match(pane, /<GroupMembersPanel :info="store\.group" :hues="hues"/)
    assert.match(pane, /const pickerIdentities = computed\(\(\) => candidateIdentities\(candidates\.value, store\.group\?\.members \?\? \[\], hues\.value\)\)/)
    assert.match(pane, /:identities="pickerIdentities"/)
    assert.match(pane, /:disabled="archived"/)
    assert.match(pane, /disabled-reason="这个群已归档，不能再发消息。在成员面板里取消归档后可继续。"/)
  })

  it('所有对外可见的 data-testid 都还在（e2e 与外部脚本靠它们定位）', () => {
    const all = [PANE, EVENT_ROW, COMPOSER, GROUP_CARD].map(rel => code(read(rel))).join('\n')
    const ids = [
      'chat-view', 'chat-title', 'chat-scroll', 'chat-composer', 'chat-input', 'chat-send', 'mention-picker', 'mention-option', 'chat-disabled',
      'chat-message', 'chat-reads', 'chat-mentions-you', 'chat-deliveries', 'chat-attachments',
      'chat-system', 'chat-card', 'chat-decision', 'chat-task-update',
      'chat-search-toggle', 'chat-search', 'chat-search-input', 'chat-search-results', 'chat-to-latest', 'chat-error', 'chat-start', 'chat-empty', 'chat-loading',
      'group-avatar-stack', 'group-avatar-more', 'group-member-count', 'group-archived-badge', 'group-members-toggle', 'group-cross-banner',
    ]
    const missing = ids.filter(id => !all.includes(`data-testid="${id}"`))
    assert.deepEqual(missing, [], `缺少 data-testid：${missing.join(', ')}`)
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
