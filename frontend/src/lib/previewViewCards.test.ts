// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 C1「汇报 / 您的回复 / 您的追加换成统一消息卡」（#120 · D-273，追加 #218）的源码级防线：
//   1. 列表用三张新卡渲染这三种话，旧版式（阶段 1 的合并头部、回复气泡、追加气泡、两份附件缩略图模板）都没了；
//   2. 汇报卡 / 回复卡 / 追加卡各自的结构：类型、testid、状态槽、「⋯」菜单、脚注，信息与经典一一对应；
//   3. 您的话靠右、窄一档、5% 绿底；智能体的话通栏靠左、卡片底色（骨架的两种变体）；
//   4. 附件缩略图只在 PreviewAttachments 里定义一份，行为与阶段 1 一致；
//   5. 经典列表仍保留自己的图片函数（共享的是新增的 lib/sessionImages，只有预览用）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { CARD_TYPES } from './messageCard'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewCards.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const PV = 'components/feedback/preview-view/'
const card = (name: string) => read(`${PV}${name}.vue`)

/** 模板里某个组件所有开标签的属性文本（跨行也行；属性值里没有 ">"）。 */
function tagAttrs(src: string, tag: string): string[] {
  return [...src.matchAll(new RegExp(`<${tag}\\b([^>]*)>`, 'g'))].map(m => m[1])
}
/** 只看模板部分（脚本头注释里也会提到组件名与类名）。 */
const templateOf = (src: string) => src.slice(src.indexOf('\n<template>'))
/** 去掉 HTML 注释与整行的 // 注释：「旧写法没了」只查代码，不查解释新写法时提到旧名字的注释。 */
const withoutComments = (src: string) => src.replace(/<!--[\s\S]*?-->/g, '').replace(/^\s*\/\/.*$/gm, '')

describe('预览视图阶段 C1：列表的接线', () => {
  const list = read(`${PV}PreviewMessageList.vue`)
  const tpl = templateOf(list)

  it('引入三张新卡，每张只在一处渲染', () => {
    for (const name of ['PreviewReportCard', 'PreviewReplyCard', 'PreviewInboxCard']) {
      assert.match(list, new RegExp(`import ${name} from './${name}\\.vue'`), `${name} 没有导入`)
      assert.equal(tagAttrs(tpl, name).length, 1, `${name} 应恰好渲染一次`)
    }
  })

  it('追加卡：在「间隙」里取代旧的追加气泡，状态由列表的 10 秒时钟算好后传入，撤回转成 revoke-queued', () => {
    const [attrs] = tagAttrs(tpl, 'PreviewInboxCard')
    assert.match(attrs, /v-else-if="block\.entry\.kind === 'inbox'"/)
    assert.match(attrs, /:q="block\.entry\.q"/)
    assert.match(attrs, /:status="statusOf\(block\.entry\.q\)"/)
    assert.match(attrs, /@revoke="emit\('revoke-queued', block\.entry\.q\.id\)"/)
    assert.ok(tpl.indexOf('<PreviewInboxCard') < tpl.indexOf('<CapsuleMarker'), '追加卡在恢复胶囊之前的分支里')
  })

  it('回复卡：只在这一轮已确认时出现；外层 div 带右侧轨的 data-rail-*（按「提交回复的时刻」）；撤回转成 revoke-session', () => {
    const wrap = tpl.match(/<div v-if="session\.status === 'completed'"([^>]*)>/)
    assert.ok(wrap, '找不到回复卡的外层 div')
    assert.match(wrap[1], /v-bind="railDomAttrs\(railMetaOfReply\(session, replySentAt\(session\)\)\)"/)
    const [attrs] = tagAttrs(tpl, 'PreviewReplyCard')
    assert.match(attrs, /:session="session"/)
    assert.match(attrs, /:turn="getSessionTurnNumber\(session\)"/)
    assert.match(attrs, /@revoke="emit\('revoke-session', session\.session_id\)"/)
    // 取消 / 超时仍是紧挨着的 v-else-if 一行说明
    assert.match(tpl, /<div v-else-if="session\.status === 'cancelled' \|\| session\.status === 'timeout'"/)
    assert.match(tpl, /sessionStatusBadge\(session\.status\)\.label/)
  })

  it('汇报卡之后仍是「本轮任务」；旧版式的东西都不在列表里了', () => {
    assert.ok(tpl.indexOf('<PreviewReportCard') < tpl.indexOf('<SessionTasks :session-id="session.session_id" />'))
    const code = withoutComments(list)
    for (const gone of [
      'DefineInboxBubble', 'ReuseInboxBubble', '您的确认回复', 'AI 方案汇报', 'attachment-thumb', 'inbox-label-', 'inbox-retract-',
      '待 AI 提取', 'getStatusBadge', 'openImagePreviewModal', 'loadedImageKeys',
    ]) {
      assert.ok(!code.includes(gone), `列表里不该再有 ${gone}（已搬进卡片组件）`)
    }
  })

  it('图片地址函数来自 lib/sessionImages（下载队列的入队仍在列表里）', () => {
    assert.match(list, /import \{ getImageUrl \} from '\.\.\/\.\.\/\.\.\/lib\/sessionImages'/)
    assert.match(list, /imageLoader\.enqueue\(url\)/)
  })
})

describe('预览视图阶段 C1：汇报卡', () => {
  const src = card('PreviewReportCard')
  const tpl = templateOf(src)

  it('套统一骨架：类型 report，根上带 report-card / 会话号 / 状态', () => {
    assert.match(tpl, /<PreviewMessageCard\b[^>]*\btype="report"/)
    assert.match(tpl, /data-testid="report-card"/)
    assert.match(tpl, /:data-session-id="session\.session_id"/)
    assert.match(tpl, /:data-status="session\.status"/)
  })

  it('标题取汇报标题（没有时用「方案汇报」），时间用 cardTimeText，悬停是完整时间', () => {
    assert.match(tpl, /:title="session\.title \|\| '方案汇报'"/)
    assert.match(tpl, /:time="cardTimeText\(session\.created_at\)"/)
    assert.match(tpl, /:time-title="session\.created_at"/)
  })

  it('状态槽：待您处理 / 已处理 / 已取消 / 已超时 / 已归档（预览状态词汇表，阶段 D2），文字与样式来自 sessionStatusBadge', () => {
    assert.match(src, /const badge = computed\(\(\) => sessionStatusBadge\(props\.session\.status\)\)/)
    assert.match(tpl, /:variant="badge\.variant"[\s\S]*?data-testid="card-status"[\s\S]*?>\{\{ badge\.label \}\}<\/Badge>/)
  })

  it('「⋯」菜单：复制正文 / 复制工作区路径（有路径才有）/ 删除此轮（两段式确认）', () => {
    assert.match(src, /key: 'copy-body', label: '复制正文'/)
    assert.match(src, /props\.projectDir\s*\n\s*\? \[\{ key: 'copy-path', label: '复制工作区路径'/)
    const del = src.slice(src.indexOf("key: 'delete'"), src.indexOf("key: 'delete'") + 400)
    assert.match(del, /label: props\.confirmingDelete \? '再点一次确认删除（3\.5 秒内）' : '删除此轮'/)
    assert.match(del, /destructive: true/)
    assert.match(del, /keepOpen: !props\.confirmingDelete/, '第一击保持菜单打开，确认那一击才关')
    assert.match(del, /onSelect: \(\) => emit\('delete'\)/)
  })

  it('正文是 Markdown 全文：不折叠、不限高', () => {
    assert.match(tpl, /<PreviewMarkdown :content="session\.summary" :workflow-id="session\.workflow_id" \/>/) // 阶段 E-a：Markdown 全文 + Artifact 引用卡
    assert.ok(!/max-h-|line-clamp|expanded|ChevronRight/.test(tpl), '汇报卡不该给正文设高度上限或折叠')
  })

  it('脚注：交付锚点（有才画）· 会话号（点一下复制）· 引用 · 工作流 · 工作区路径（手机宽度隐藏）', () => {
    assert.match(tpl, /<AnchorChip\s+v-if="session\.git_commit"[\s\S]*?:commit="session\.git_commit"[\s\S]*?:subject="session\.git_subject"[\s\S]*?@copy="copyWithToast"/)
    assert.match(tpl, /data-testid="report-session-id"/)
    assert.match(tpl, /@click="copyWithToast\(session\.session_id, '会话 ID'\)"/)
    assert.match(tpl, /<ReplyRefButton :token="replyRefToken\(session\.session_id\)" \/>/)
    assert.match(tpl, /v-if="session\.workflow_id"/)
    const pathBtn = tpl.match(/<button\s+v-if="projectDir"([\s\S]*?)>/)
    assert.ok(pathBtn, '找不到工作区路径按钮')
    assert.match(pathBtn[1], /class="[^"]*\bhidden\b[^"]*\bsm:flex\b/, '工作区路径在手机宽度隐藏、sm 起显示')
  })

  it('对外：只发 delete 事件，知道确认进行到哪一步', () => {
    assert.match(src, /defineEmits<\{ \(e: 'delete'\): void \}>\(\)/)
    assert.match(src, /confirmingDelete: boolean/)
  })
})

describe('预览视图阶段 C1：您的确认回复卡', () => {
  const src = card('PreviewReplyCard')
  const tpl = templateOf(src)

  it('套统一骨架：类型 reply，标题是「对第 N 轮的确认」，根上带 reply-card / 会话号 / 是否已取走', () => {
    assert.match(tpl, /<PreviewMessageCard\b[^>]*\btype="reply"/)
    assert.match(tpl, /:title="`对第 \$\{turn\} 轮的确认`"/)
    assert.match(tpl, /data-testid="reply-card"/)
    assert.match(tpl, /:data-session-id="session\.session_id"/)
    assert.match(tpl, /:data-consumed="session\.consumed_by_ai \? '1' : '0'"/)
  })

  it('时间是「您提交回复的时刻」（replySentAt），悬停给精确到秒的时间', () => {
    assert.match(src, /const sentAt = computed\(\(\) => replySentAt\(props\.session\)\)/)
    assert.match(tpl, /:time="cardTimeText\(sentAt\)"/)
    assert.match(tpl, /:time-title="`你提交回复的时间 \$\{formatClock\(sentAt\)\}`"/)
  })

  it('状态槽：待读取（琥珀 · 脉动时钟）/ 已读（悬停看几点取走）——预览状态词汇表，阶段 D2；两种都带 card-status', () => {
    assert.match(tpl, /v-if="!session\.consumed_by_ai"[\s\S]*?title="回复已提交，智能体还没来取"[\s\S]*?data-testid="card-status"[\s\S]*?<span>\{\{ STATUS_WORD\.unread \}\}<\/span>/)
    assert.match(tpl, /v-else[\s\S]*?:title="receivedTitle\(session\)"[\s\S]*?data-testid="card-status"[\s\S]*?<span>\{\{ STATUS_WORD\.read \}\}<\/span>/)
    assert.ok(!tpl.includes('待 AI 提取') && !tpl.includes('AI 已接收'), '回复卡不再写经典的说法')
    assert.match(tpl, /<Clock class="size-2\.5 animate-pulse" \/>/)
  })

  it('正文：预设标签 · 回复文字（可点链接）· 附件缩略图', () => {
    assert.match(src, /props\.session\.response_text \|\| '（已确认，未附加文字）'/)
    assert.match(tpl, /v-for="msg in session\.user_messages"/)
    assert.match(tpl, /<LinkedText\b[^>]*:text="bodyText"[^>]*:workflow-id="session\.workflow_id"/)
    assert.match(tpl, /<PreviewAttachments :images="session\.images" :key-prefix="session\.session_id" :fallback-name="`第 \$\{turn\} 轮用户附件`" \/>/)
  })

  it('撤回只在智能体还没取走时出现，放在脚注里，发 revoke 事件', () => {
    assert.match(tpl, /<template v-if="!session\.consumed_by_ai" #footer>/)
    assert.match(tpl, /data-testid="reply-retract"/)
    assert.match(tpl, /@click="emit\('revoke'\)"/)
    assert.match(tpl, /<span>撤回反馈<\/span>/)
    assert.match(src, /defineEmits<\{ \(e: 'revoke'\): void \}>\(\)/)
  })

  it('「⋯」菜单：复制回复', () => {
    assert.match(src, /key: 'copy-body', label: '复制回复'/)
    assert.match(tpl, /<PreviewCardMenu :items="menuItems" \/>/)
  })
})

describe('预览视图阶段 C1：您的追加卡', () => {
  const src = card('PreviewInboxCard')
  const tpl = templateOf(src)

  it('套统一骨架：类型 append，没有标题；根上保留 inbox-item-<id> 与 data-state', () => {
    const [attrs] = tagAttrs(tpl, 'PreviewMessageCard')
    assert.match(attrs, /\btype="append"/)
    assert.ok(!/(^|\s):?title=/.test(attrs), '追加卡没有标题')
    assert.match(attrs, /:data-testid="`inbox-item-\$\{q\.id\}`"/)
    assert.match(attrs, /:data-state="status\.stage"/)
    assert.match(attrs, /:time="cardTimeText\(q\.created_at\)"/)
  })

  it('状态徽标 + hook 提取详情：testid 与经典一致；宽屏放头部，窄屏挪到正文底部', () => {
    assert.match(tpl, /:data-testid="`inbox-label-\$\{q\.id\}`"/)
    assert.match(tpl, /:data-stage="status\.stage"/)
    assert.match(tpl, /:title="status\.label"/)
    const wide = tpl.match(/<span\s+class="([^"]*)"\s+:title="status\.detail"\s+:data-testid="`inbox-detail-\$\{q\.id\}`"/)
    assert.ok(wide, '找不到头部的 hook 详情')
    assert.match(wide[1], /\bhidden\b[^"]*\bsm:inline\b/)
    const narrow = tpl.match(/<p\s+class="([^"]*)"\s+:data-testid="`inbox-detail-m-\$\{q\.id\}`"/)
    assert.ok(narrow, '找不到正文底部的 hook 详情')
    assert.match(narrow[1], /\bsm:hidden\b/)
  })

  it('正文：预设标签 · 追加文字 · 附件缩略图（key 前缀 inbox-<id>）', () => {
    assert.match(tpl, /v-for="msg in q\.user_messages"/)
    assert.match(tpl, /:text="q\.response_text \|\| '（无文字内容）'" :workflow-id="q\.workflow_id"/)
    assert.match(tpl, /<PreviewAttachments :images="q\.images" :key-prefix="`inbox-\$\{q\.id\}`" fallback-name="暂存附件" \/>/)
  })

  it('撤回只在还能撤回时出现（已排队 / 已读取未确认），放在脚注里，发 revoke 事件', () => {
    assert.match(tpl, /<template v-if="status\.canRetract" #footer>/)
    assert.match(tpl, /:data-testid="`inbox-retract-\$\{q\.id\}`"/)
    assert.match(tpl, /:title="status\.retractTitle"/)
    assert.match(tpl, /<span>\{\{ status\.retractLabel \}\}<\/span>/)
    assert.match(src, /defineEmits<\{ \(e: 'revoke'\): void \}>\(\)/)
  })

  it('徽标上的字取预览状态词汇表（待读取 / 已读 / 已过期，阶段 D2）；悬停仍是全称', () => {
    assert.match(tpl, /<span>\{\{ inboxBadgeWord\(status\) \}\}<\/span>/)
    assert.match(src, /import \{ inboxBadgeWord \} from '\.\.\/\.\.\/\.\.\/lib\/statusWords'/)
    assert.ok(!tpl.includes('status.badge'), '不再直接显示经典的 status.badge（待 AI 提取 / AI 已读取 / AI 已接收）')
  })

  it('状态徽标配色与回复卡同一套：排队琥珀 / 已读取天蓝 / 其余灰', () => {
    assert.match(src, /tone === 'pending'\) return 'bg-amber-500\/15/)
    assert.match(src, /tone === 'read'\) return 'bg-sky-500\/15/)
    assert.match(src, /return 'text-muted-foreground\/80'/)
  })
})

describe('预览视图阶段 C1：您的话靠右、智能体的话靠左', () => {
  const src = card('PreviewMessageCard')

  it('您的话：左边让出 64px（手机 24px）、底色是色调的 5%、色条在右边；智能体的话：卡片底色、色条在左边', () => {
    assert.match(src, /const USER_CLASS = 'ml-6 bg-\[color:color-mix\(in_srgb,var\(--card-tone\)_5%,var\(--color-card\)\)\] sm:ml-16 border-r-\[3px\] border-r-\[color:var\(--card-tone\)\]'/)
    assert.match(src, /const AGENT_CLASS = 'bg-card border-l-\[3px\] border-l-\[color:var\(--card-tone\)\]'/)
    assert.match(src, /meta\.value\.sender === 'user' \? USER_CLASS : AGENT_CLASS/)
  })

  it('根节点的静态类里没有 bg-card（否则和 5% 色底冲突），变体类由 alignClass 追加', () => {
    const root = templateOf(src).match(/<article\b([^>]*)>/)
    assert.ok(root)
    assert.ok(!/\bbg-card\b/.test(root[1].match(/\n\s+class="([^"]*)"/)?.[1] ?? ''), '根的静态 class 不该带 bg-card')
    assert.match(root[1], /:class="\[toneClass, alignClass\]"/)
  })

  it('三张新卡的发送方：汇报是智能体（通栏），回复与追加是您（靠右）', () => {
    assert.equal(CARD_TYPES.report.sender, 'agent')
    assert.equal(CARD_TYPES.reply.sender, 'user')
    assert.equal(CARD_TYPES.append.sender, 'user')
  })
})

describe('预览视图阶段 C1：复制反馈（统一的 copyWithToast）', () => {
  const copy = read(`${PV}cardCopy.ts`)

  it('复制成功 / 失败各有提示', () => {
    assert.match(copy, /export async function copyWithToast\(text: string, label: string\)/)
    assert.match(copy, /await copyText\(text\)/)
    assert.match(copy, /toast\.success\(`已复制\$\{label\}`/)
    assert.match(copy, /toast\.error\(`复制\$\{label\}失败/)
  })

  it('四张卡都用它，自己不再直接调 copyText / toast', () => {
    for (const name of ['PreviewReportCard', 'PreviewReplyCard', 'PreviewInboxCard', 'PreviewNoticeCard']) {
      const src = card(name)
      assert.match(src, /import \{ copyWithToast \} from '\.\/cardCopy'/, `${name} 应使用 copyWithToast`)
      assert.ok(!/\bcopyText\b|\btoast\b/.test(src.replace(/copyWithToast/g, '')), `${name} 不该直接调 copyText / toast`)
    }
  })
})

describe('预览视图阶段 C1：附件缩略图（PreviewAttachments）', () => {
  const src = card('PreviewAttachments')

  it('缩略图的结构只定义在这里，函数来自 lib/sessionImages', () => {
    assert.equal((src.match(/data-testid="attachment-thumb"/g) ?? []).length, 1)
    assert.match(src, /from '\.\.\/\.\.\/\.\.\/lib\/sessionImages'/)
    for (const fn of ['getImageKey', 'getImageStyle', 'getImageUrl', 'isImageLoaded', 'markImageLoaded']) assert.ok(src.includes(fn), `缺少 ${fn}`)
  })

  it('点击放大：先让这张图优先下载，再打开预览（没有文件名用 fallbackName-序号）', () => {
    assert.match(src, /imageLoader\.prioritize\(url\)\s*\n\s*previewStore\.openImagePreview\(\{/)
    assert.match(src, /alt: img\.name \|\| `\$\{props\.fallbackName\}-\$\{index \+ 1\}`/)
  })

  it('加载中的百分比 / 下载失败重试 / 排队等待三种骨架状态都在', () => {
    assert.match(src, /\?\.status === 'loading'/)
    assert.match(src, /\?\.status === 'error'/)
    assert.match(src, /@click\.stop="imageLoader\.retry\(getImageUrl\(img\)\)"/)
    assert.match(src, /排队加载/)
    assert.match(src, /@load="markImageLoaded\(getImageKey\(img, imgIdx, keyPrefix\)\)"/)
  })
})

describe('预览视图阶段 C1：经典列表保留自己的一份图片函数', () => {
  const classic = read('components/feedback/FeedbackMessageList.vue')

  it('经典里还有自己的 getImageKey / getImageStyle / getImageUrl，没有改去用 lib/sessionImages', () => {
    for (const fn of ['getImageKey', 'getImageStyle', 'getImageUrl']) {
      assert.ok(classic.includes(`function ${fn}(`), `经典列表应保留自己的 ${fn}`)
    }
    assert.ok(!classic.includes('sessionImages'))
  })
})
