// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 1「版式与高度」与阶段 C2「事件行」（#120）的源码级防线。会话页的预览列表里，没有正文的事件都是 32px 的事件行（见 EventRowHead）：
//   1. 卡片摞 / 恢复胶囊 / 任务进展 / TimelineCardEntry 只剩事件行一种版式，已没有 compact 参数（#247 删掉了不传 compact 的那一支）；
//      确认卡（ConfirmTimelineCard）与决策汇总卡（DecisionGroupCard）仍声明可选的 compact、且没有任何地方把它默认成开——群聊页还在用不传 compact 的原版式；
//   2. 预览列表里这些叶子固定渲染事件行，不再传 compact；
//   3. 竖图：预览里的附件缩略图（PreviewAttachments）用 bg-muted + object-contain（没有 bg-muted/40、object-cover），Markdown 内嵌图的底色是预览列表自己的作用域样式，
//      共享的 MarkdownRenderer 不改；
//   4. 汇报卡（阶段 C 起取代阶段 1 的合并头部）带着经典「轮次分隔条」与「AI 方案汇报」两行的全部内容；
//   5. 展开不限高：确认卡展开体的 max-h-72 只留给不传 compact 的群聊页（compact 时去掉）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewCompact.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const PREVIEW = 'components/feedback/preview-view/PreviewMessageList.vue'
/** 仍带 compact 参数的共享叶子（群聊页用不传 compact 的原版式，默认关闭）。 */
const SHARED_LEAVES = [
  'components/confirms/ConfirmTimelineCard.vue',
  'components/confirms/DecisionGroupCard.vue',
]
/** 只剩事件行一种版式的叶子（#247 删了不传 compact 的那一支，compact 参数随之去掉）。 */
const EVENT_ONLY_LEAVES = [
  'components/feedback/TimelineCardEntry.vue',
  'components/feedback/AsyncTimelineEntry.vue',
  'components/feedback/CardStack.vue',
  'components/feedback/CapsuleMarker.vue',
]

/** 模板里某个组件所有开标签的属性文本（跨行也行；属性值里没有 ">"）。 */
function tagAttrs(src: string, tag: string): string[] {
  return [...src.matchAll(new RegExp(`<${tag}\\b([^>]*)>`, 'g'))].map(m => m[1])
}
const hasCompactAttr = (attrs: string) => /(^|\s):?compact(\s|=|$)/.test(attrs)

describe('预览视图阶段 1：共享叶子的 compact 参数默认关闭', () => {
  it('确认卡与决策汇总卡声明了可选的 compact，没有地方把它默认成开（群聊页还在用原版式）', () => {
    for (const rel of SHARED_LEAVES) {
      const src = read(rel)
      assert.match(src, /compact\?: boolean/, `${rel} 应声明 compact?: boolean`)
      assert.ok(!/compact:\s*true/.test(src), `${rel} 不该把 compact 默认成 true`)
    }
  })

  it('卡片摞、恢复胶囊、任务进展只剩事件行一种版式：不再有 compact 参数，也没有按它切换的分支（#247）', () => {
    for (const rel of ['components/feedback/CardStack.vue', 'components/feedback/CapsuleMarker.vue', 'components/feedback/AsyncTimelineEntry.vue']) {
      assert.ok(!/(^|[^\w-])compact(?![\w-])/.test(read(rel)), `${rel} 不该再出现 compact`)
    }
  })

  it('TimelineCardEntry 没有 compact 参数：固定把 compact 交给确认卡与决策汇总卡，任务进展交给 AsyncTimelineEntry', () => {
    const src = read('components/feedback/TimelineCardEntry.vue')
    assert.ok(!/compact\?:/.test(src), 'TimelineCardEntry 不该再声明 compact')
    assert.ok(!/:compact=/.test(src), '不该再按变量递 compact')
    assert.equal(tagAttrs(src, 'ConfirmTimelineCard').filter(hasCompactAttr).length, 1, '确认卡固定传 compact')
    assert.equal(tagAttrs(src, 'DecisionGroupCard').filter(hasCompactAttr).length, 1, '决策汇总卡固定传 compact')
    assert.equal(tagAttrs(src, 'AsyncEntryItem').filter(hasCompactAttr).length, 0, '任务进展叶子已没有 compact 参数')
  })

  it('确认卡展开体的 max-h-72 仍保留给不传 compact 的群聊页，compact 时才去掉', () => {
    const src = read('components/confirms/ConfirmTimelineCard.vue')
    assert.match(src, /:class="compact \? '' : 'max-h-72 overflow-y-auto'"/)
  })
})

describe('预览视图阶段 1：预览列表', () => {
  const preview = read(PREVIEW)

  it('每一处卡片渲染（摞里的、单独的、恢复胶囊）都是事件行：这些叶子已没有 compact 参数，列表不再传', () => {
    const entries = tagAttrs(preview, 'TimelineCardEntry')
    assert.equal(entries.length, 2, '预览列表里应有两处 <TimelineCardEntry>（摞里的与单独的）')
    for (const a of entries) assert.ok(!hasCompactAttr(a), `<TimelineCardEntry> 不再接收 compact：${a.replace(/\s+/g, ' ').trim()}`)
    const stacks = tagAttrs(preview, 'CardStack')
    assert.equal(stacks.length, 1)
    assert.ok(!hasCompactAttr(stacks[0]), '<CardStack> 不再接收 compact')
    const capsules = tagAttrs(preview, 'CapsuleMarker')
    assert.equal(capsules.length, 1)
    assert.ok(!hasCompactAttr(capsules[0]), '<CapsuleMarker> 不再接收 compact（阶段 C2：恢复胶囊固定是事件行）')
  })

  it('竖图：附件缩略图（您的回复与追加共用 PreviewAttachments）是 bg-muted + object-contain，没有 bg-muted/40 与 object-cover', () => {
    const attachments = read('components/feedback/preview-view/PreviewAttachments.vue')
    assert.ok(!preview.includes('object-cover') && !attachments.includes('object-cover'), '预览里不该再有 object-cover（竖图会被裁）')
    assert.ok(!preview.includes('attachment-thumb'), '缩略图只在 PreviewAttachments 里定义一份，列表里不再有')
    const thumbs = [...attachments.matchAll(/class="([^"]*)"\s+data-testid="attachment-thumb"/g)].map(m => m[1].split(/\s+/))
    assert.equal(thumbs.length, 1, '附件组件里应有一处缩略图容器')
    for (const tokens of thumbs) {
      assert.ok(tokens.includes('bg-muted'), `缩略图容器应是 bg-muted：${tokens.join(' ')}`)
      assert.ok(!tokens.some(t => t.startsWith('bg-muted/')), `缩略图容器底色不该带透明度：${tokens.join(' ')}`)
    }
    assert.equal((attachments.match(/size-full object-contain/g) ?? []).length, 1, '缩略图的 <img> 应是 object-contain')
    for (const card of ['PreviewReplyCard.vue', 'PreviewInboxCard.vue']) {
      assert.match(read(`components/feedback/preview-view/${card}`), /<PreviewAttachments\b/, `${card} 应通过 PreviewAttachments 渲染附件`)
    }
  })

  it('Markdown 内嵌图的底色是预览列表自己的作用域样式，共享的 MarkdownRenderer 不改', () => {
    assert.match(preview, /class="pv-list /, '预览列表的根应带 pv-list（作用域样式靠它）')
    assert.match(preview, /<style scoped>[\s\S]*\.pv-list :deep\(\.markdown-body img\)\s*\{\s*background-color:\s*var\(--muted\);/)
    // 共享的内嵌图规则（经典视图也用）不带任何底色：底色只由预览列表的作用域样式补上
    const md = read('components/MarkdownRenderer.vue')
    const rule = md.match(/\.markdown-body img\s*\{([^}]*)\}/)
    assert.ok(rule, '找不到 MarkdownRenderer 里的 .markdown-body img 规则')
    assert.ok(!/background/.test(rule[1]), '共享的 MarkdownRenderer 不该为预览视图给内嵌图加底色：' + rule[1].replace(/\s+/g, ' ').trim())
  })

  it('汇报卡：带着经典轮次分隔条与 AI 方案汇报两行的全部内容，旧的分隔条行与阶段 1 的合并头部都没了', () => {
    assert.equal((preview.match(/<PreviewReportCard\b/g) ?? []).length, 1, '每一轮只渲染一张汇报卡')
    assert.ok(!preview.includes('turn-header'), '阶段 1 的合并头部（turn-header）应已被汇报卡取代')
    assert.ok(!preview.includes('Turn Divider & Timestamp Header'), '旧的轮次分隔条行应已合并进汇报卡')
    const card = read('components/feedback/preview-view/PreviewReportCard.vue')
    for (const need of [
      'session.workflow_id',
      'session.title',
      '点击复制会话 ID',
      "copyWithToast(session.session_id, '会话 ID')",
      '<ReplyRefButton :token="replyRefToken(session.session_id)" />',
      'cardTimeText(session.created_at)',
      'sessionStatusBadge(props.session.status)',
      '<AnchorChip',
      'projectDir',
      "emit('delete')",
      '删除此轮',
    ]) {
      assert.ok(card.includes(need), `汇报卡缺少：${need}`)
    }
    // 删除的两段式确认、工作区路径仍由列表接给它
    for (const need of [':project-dir="formatSessionProjectDirectory(session)"', '@delete="handleDeleteSession(session)"']) {
      assert.ok(preview.includes(need), `列表缺少对汇报卡的接线：${need}`)
    }
  })
})

describe('预览视图阶段 C2：事件行（32px 单行）', () => {
  /** 渲染事件行的叶子：每个都用 EventRowHead + EVENT_BOX（任务行在 CompactTaskRow 里；确认卡与决策汇总卡在 compact 时）。 */
  const EVENT_LEAVES = [
    'components/feedback/CompactTaskRow.vue',
    'components/feedback/CardStack.vue',
    'components/feedback/CapsuleMarker.vue',
    'components/confirms/ConfirmTimelineCard.vue',
    'components/confirms/DecisionGroupCard.vue',
  ]

  it('每个事件行叶子都用 EventRowHead，盒子是 EVENT_BOX + 类型色调（色条取 --card-tone）', () => {
    for (const rel of EVENT_LEAVES) {
      const src = read(rel)
      assert.match(src, /import EventRowHead from/, `${rel} 应引入 EventRowHead`)
      assert.match(src, /<EventRowHead\b/, `${rel} 应渲染 EventRowHead`)
      assert.match(src, /\bEVENT_BOX\b/, `${rel} 的盒子应是 EVENT_BOX`)
      assert.match(src, /CARD_TONE_CLASS\[/, `${rel} 的色调应取自词汇表`)
    }
  })

  it('旧的紧凑行常量（COMPACT_*）和 compact 条件类名都没了：类集合回到阶段 1 之前', () => {
    for (const rel of [...EVENT_LEAVES, ...SHARED_LEAVES, ...EVENT_ONLY_LEAVES]) {
      const src = read(rel)
      assert.ok(!/COMPACT_[A-Z]+/.test(src), `${rel} 不该再引用 COMPACT_ 常量`)
      assert.ok(!/compact \? '(shrink-0|truncate|min-w-0|leading)/.test(src), `${rel} 不该再有按 compact 切换的紧凑行类名`)
      assert.ok(!/compact \? COMPACT/.test(src))
    }
    assert.ok(!existsSync(join(srcDir(), 'components/feedback/CompactNoticeRow.vue')), 'CompactNoticeRow 早已不可达（进度通知是统一消息卡），应已删除')
  })

  // 带边框的小标签若沿用默认的 18px 行高就是 20px，会把整行撑高：小标签的几何只在 lib/messageCard.ts 的 CARD_CHIP_BASE 里写一份（高 18px、leading-4）
  it('共享叶子里没有自己写的小标签几何（leading-4）：都走 CARD_CHIP_BASE / CARD_CHIP_CLASS', () => {
    for (const rel of [...EVENT_LEAVES, ...SHARED_LEAVES, ...EVENT_ONLY_LEAVES]) {
      assert.ok(!/\bleading-4\b/.test(read(rel)), `${rel} 不该自己写 leading-4（小标签几何在 lib/messageCard.ts）`)
    }
    assert.match(read('lib/messageCard.ts'), /CARD_CHIP_BASE = '[^']*\bh-\[18px\][^']*\bleading-4\b/)
    assert.match(read('components/feedback/CompactTaskRow.vue'), /:class="\[CARD_CHIP_BASE, verbClass\]"[^>]*data-testid="task-update-verb"/)
  })

  it('EventRowHead：一行固定高度、主体可整行点击、预览只在宽屏、标签用共享几何、四个槽', () => {
    const src = read('components/feedback/EventRowHead.vue')
    assert.match(src, /EVENT_LINE_PX/, '一行的高度取自 lib/eventRow')
    assert.match(src, /:class="CARD_CHIP_CLASS"/, '类型标签用共享的 CARD_CHIP_CLASS')
    assert.match(src, /hidden truncate text-\[11px\] text-muted-foreground sm:block/, '预览只在宽屏（≥ sm）出现，并且单行截断')
    assert.match(src, /min-w-0 truncate text-\[13px\] font-medium/, '标题单行截断')
    assert.match(src, /:class="\$slots\.trail \|\| phoneHideTime \? 'max-sm:hidden' : ''"/, '行末有按钮的行、传了 phoneHideTime 的行（摞头），手机上时间让位给标题')
    assert.match(src, /\$slots\.trail \? '' : 'pr-3'/, '没有行末按钮时右内边距留在主体里（整行可点）')
    assert.match(src, /:aria-expanded="interactive && expanded !== undefined \? expanded : undefined"/, '展开型的主体带 aria-expanded')
    for (const slot of ['lead', 'preview', 'status', 'trail']) assert.ok(src.includes(`<slot name="${slot}"`), `缺少 ${slot} 槽`)
    assert.ok(!/#[0-9a-fA-F]{6}/.test(src), '颜色只在 lib/messageCard.ts 里定义')
  })

  it('各叶子的既有 testid 一个没丢（e2e 与经典叶子命名一致）', () => {
    const want: Array<[string, string[]]> = [
      ['components/feedback/CompactTaskRow.vue', ['task-update-card', 'task-update-open', 'task-update-verb', 'task-update-text']],
      ['components/feedback/CardStack.vue', ['card-stack-toggle', 'card-stack-count', 'card-stack-summary', 'card-stack-preview']],
      ['components/feedback/CapsuleMarker.vue', ['capsule-marker', 'capsule-details', 'capsule-jump']],
      ['components/confirms/DecisionGroupCard.vue', ['decision-group-card', 'decision-group-state', 'decision-group-open']],
    ]
    for (const [rel, ids] of want) {
      const src = read(rel)
      for (const id of ids) assert.ok(src.includes(id), `${rel} 缺少 ${id}`)
    }
    const confirm = read('components/confirms/ConfirmTimelineCard.vue')
    for (const suffix of ['-toggle', '-state', '-detail']) assert.ok(confirm.includes('`confirm-tl-${item.ref}' + suffix + '`'), `确认卡缺少 confirm-tl-<ref>${suffix}`)
  })

  it('ConfirmTimelineCard 的事件行：色调随提问种类、状态标签「已处理」或原状态、「引用」在行末；经典的头部与详情一字未动', () => {
    const src = read('components/confirms/ConfirmTimelineCard.vue')
    assert.match(src, /CARD_TONE_CLASS\[CARD_TYPES\[props\.item\.kind\]\.tone\]/)
    assert.match(src, /props\.item\.status === 'answered' \? STATUS_WORD\.done : STATUS_LABEL\[props\.item\.status\]/)
    assert.match(src, /<template #trail>\s*<ReplyRefButton/)
    assert.match(src, /border-amber-500\/40 text-amber-700 max-sm:hidden/, '手机上中风险标签让位给标题，高风险（danger）的不让')
    // 经典头部：阶段 1 之前的写法（换行容器、两行文字、旋转的箭头）
    assert.match(src, /<div v-else class="flex items-start gap-1\.5 px-3 py-2">/)
    assert.match(src, /<span class="flex flex-wrap items-center gap-x-2 gap-y-0\.5">/)
    assert.match(src, /:class="compact \? \[EVENT_BOX, eventTone\] : 'rounded-sm border border-border\/60 bg-muted\/20 text-xs'"/)
  })
})
