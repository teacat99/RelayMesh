// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 B「进度通知纳入正文」（#120 · D-273，追加 #218）的源码级防线：
//   1. 预览列表用统一消息卡渲染进度通知，其余卡片仍走紧凑行；通知永远单独成块，不进「N 张卡片」摞；
//   2. 通知卡：正文全文不折叠、不限高，保留经典的 testid 与「引用」按钮，类型 / 色调来自词汇表；
//   3. 骨架卡的元素齐全（色条、头像、类型标签、标题、时间、菜单、正文、脚注）；
//   4. 经典视图与共享叶子一个字不碰：谁也不引用 preview-view 里的新组件。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import type { TimelineEntry } from './timeline'
import { groupBlocks } from './timeline'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewCard.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const PREVIEW_LIST = 'components/feedback/preview-view/PreviewMessageList.vue'
const NOTICE_CARD = 'components/feedback/preview-view/PreviewNoticeCard.vue'
const MESSAGE_CARD = 'components/feedback/preview-view/PreviewMessageCard.vue'
const CARD_MENU = 'components/feedback/preview-view/PreviewCardMenu.vue'

const notice = (id: string, at: string): TimelineEntry =>
  ({ kind: 'notice', key: `notice-${id}`, at, session: { session_id: id } as never })

describe('预览视图阶段 B：列表里的进度通知', () => {
  const src = read(PREVIEW_LIST)

  it('引入并渲染 PreviewNoticeCard，且排在紧凑行的兜底分支之前', () => {
    assert.match(src, /import PreviewNoticeCard from '\.\/PreviewNoticeCard\.vue'/)
    const branch = [...src.matchAll(/<PreviewNoticeCard\b([^>]*)>/g)]
    assert.equal(branch.length, 1, '通知卡只在一处渲染')
    assert.match(branch[0][1], /v-else-if="block\.entry\.kind === 'notice'"/)
    assert.match(branch[0][1], /:entry="block\.entry"/)
    assert.ok(!/(^|\s):?compact(\s|=|$)/.test(branch[0][1]), '通知卡不是紧凑行，不传 compact')
    assert.ok(src.indexOf('<PreviewNoticeCard') < src.indexOf('<TimelineCardEntry v-else'), '通知分支必须在兜底的紧凑行之前')
  })

  it('通知永远单独成块，不会被并进「N 张卡片」摞', () => {
    const blocks = groupBlocks([notice('a', '2026-10-17T01:00:00Z'), notice('b', '2026-10-17T01:01:00Z'), notice('c', '2026-10-17T01:02:00Z')])
    assert.deepEqual(blocks.map(b => b.type), ['single', 'single', 'single'])
  })

  it('摞与兜底分支里的 TimelineCardEntry 仍是事件行：它固定渲染事件行，已没有 compact 参数（#247）', () => {
    const entries = [...src.matchAll(/<TimelineCardEntry\b([^>]*)>/g)].map(m => m[1])
    assert.ok(entries.length >= 2)
    for (const a of entries) assert.ok(!/(^|\s):?compact(\s|=|$)/.test(a), `<TimelineCardEntry> 不再接收 compact：${a.trim()}`)
    const entry = read('components/feedback/TimelineCardEntry.vue')
    assert.match(entry, /<ConfirmTimelineCard\b[^>]*\bcompact\b/, '已处理的提问固定是事件行')
    assert.match(entry, /<DecisionGroupCard\b[^>]*\bcompact\b/, '决策汇总固定是事件行')
  })
})

describe('预览视图阶段 B：进度通知卡', () => {
  const src = read(NOTICE_CARD)

  it('套统一骨架，类型是 notice', () => {
    assert.match(src, /<PreviewMessageCard\b[^>]*\btype="notice"/)
  })

  it('保留经典的 testid 与会话号标记（e2e 与对等测试按它找通知）', () => {
    assert.match(src, /data-testid="notice-bubble"/)
    assert.match(src, /:data-session-id="session\.session_id"/)
  })

  it('正文是 Markdown 全文：不折叠、不限高，没有切换按钮与展开状态', () => {
    assert.match(src, /<PreviewMarkdown :content="session\.summary" :workflow-id="session\.workflow_id" \/>/) // 阶段 E-a：Markdown 全文 + Artifact 引用卡
    assert.ok(!/notice-toggle|notice-preview|expanded|v-if="open|ChevronRight/.test(src), '不该再有折叠 / 展开的痕迹')
    assert.ok(!/max-h-|line-clamp|overflow-/.test(src), '通知卡不该给正文设高度上限')
  })

  it('保留「引用」按钮（短哈希，点一下插入「【回复 哈希】」）', () => {
    assert.match(src, /<ReplyRefButton :token="replyRefToken\(session\.session_id\)" :label="replyHash\(session\.session_id\)" \/>/)
  })

  it('标题取通知自己的标题；时间用 cardTimeText，悬停是完整时间', () => {
    assert.match(src, /:title="session\.title"/)
    assert.match(src, /:time="cardTimeText\(entry\.at\)"/)
    assert.match(src, /:time-title="entry\.at"/)
  })

  it('「⋯」菜单：复制正文、复制会话号', () => {
    assert.match(src, /key: 'copy-body'/)
    assert.match(src, /key: 'copy-id'/)
    assert.match(src, /<PreviewCardMenu :items="menuItems" \/>/)
  })

  it('没有「已读」标签：通知没有已读 / 未读数据，不画凭空的状态', () => {
    const template = src.slice(src.indexOf('<template>'))
    assert.ok(!/已读|未读/.test(template))
    assert.ok(!/#status/.test(template), '通知卡不用状态槽')
  })
})

describe('预览视图阶段 B：统一消息卡骨架', () => {
  const src = read(MESSAGE_CARD)

  it('元素齐全：色条、头像、发送方、类型标签、标题、时间、菜单槽、正文、脚注', () => {
    // 色条：智能体 / 系统的话在左边，您的话（靠右的气泡）在右边（追加 #224）；两边都取 --card-tone，根上不再写死左色条
    assert.match(src, /AGENT_CLASS = '[^']*border-l-\[3px\] border-l-\[color:var\(--card-tone\)\]'/, '智能体的话：左色条')
    assert.match(src, /USER_CLASS = '[^']*border-r-\[3px\] border-r-\[color:var\(--card-tone\)\]'/, '您的话：右色条')
    const userClass = src.match(/USER_CLASS = '([^']*)'/)?.[1] ?? ''
    const agentClass = src.match(/AGENT_CLASS = '([^']*)'/)?.[1] ?? ''
    assert.ok(!/border-l-\[3px\]/.test(userClass) && !/border-r-\[3px\]/.test(agentClass), '每个发送方只有一侧的色条')
    const articleClass = src.slice(src.indexOf('<template>')).match(/<article\s+class="([^"]*)"/)?.[1] ?? ''
    assert.ok(!/border-[lr]-\[3px\]/.test(articleClass), '根的公共类里不再有色条（按发送方分左右）')
    for (const id of ['card-head', 'card-avatar', 'card-sender', 'card-type', 'card-time', 'card-body', 'card-footer']) {
      assert.ok(src.includes(`data-testid="${id}"`), `缺少 ${id}`)
    }
    // 标题位默认是 card-title；待处理的提问 / 计划卡（阶段 C3）传 titleTestid，沿用经典的 confirm-<编号>-title
    assert.ok(src.includes(`:data-testid="titleTestid ?? 'card-title'"`), '标题位默认 card-title，可由 titleTestid 改名')
    for (const slot of ['status', 'menu']) assert.ok(src.includes(`<slot name="${slot}" />`), `缺少 ${slot} 槽`)
    assert.ok(src.includes('<slot name="footer" />'))
    assert.match(src, /<slot \/>/, '默认槽 = 正文')
  })

  it('色调、标签、发送方都来自词汇表（组件里没有写死的颜色）', () => {
    assert.match(src, /CARD_TYPES\[props\.type\]/)
    assert.match(src, /CARD_TONE_CLASS\[meta\.value\.tone\]/)
    assert.match(src, /CARD_SENDERS\[meta\.value\.sender\]/)
    assert.ok(!/#[0-9a-fA-F]{6}/.test(src), '颜色只在 lib/messageCard.ts 里定义')
  })

  it('根节点带 data-card-type / data-card-sender，调用方的 data-testid 落在根上（根上不自带 testid）', () => {
    const root = src.slice(src.indexOf('<template>')).match(/<article\b([^>]*)>/) // 只看模板：脚本头注释里也提到了 <article>
    assert.ok(root)
    assert.match(root[1], /:data-card-type="type"/)
    assert.match(root[1], /:data-card-sender="meta\.sender"/)
    assert.ok(!/data-testid/.test(root[1]))
  })

  it('正文不限高、不折叠', () => {
    const body = src.match(/<div\b([^>]*data-testid="card-body"[^>]*)>/)
    assert.ok(body)
    assert.ok(!/max-h-|line-clamp|overflow-/.test(body[1]))
  })

  it('类型标签用共享的 CARD_CHIP_CLASS（高 18px，带 leading-4，否则会把头部撑高；事件行共用同一份）', () => {
    const chip = src.match(/<span\b([^>]*data-testid="card-type"[^>]*)>/)
    assert.ok(chip)
    assert.match(chip[1], /:class="CARD_CHIP_CLASS"/)
    assert.ok(!/inline-flex h-\[18px\]/.test(src), '标签的几何只在 lib/messageCard.ts 里写一份')
  })

  it('「⋯」菜单组件有触发按钮与菜单项', () => {
    const menu = read(CARD_MENU)
    assert.match(menu, /data-testid="card-menu"/)
    assert.match(menu, /:data-testid="`card-menu-\$\{item\.key\}`"/)
    // 选中 = 调这一项的 onSelect；keepOpen 的项先拦住 radix 的默认「选中即关」（阶段 C：删除的两段式确认要菜单留着）
    assert.match(menu, /@select="\(ev: Event\) => onSelect\(item, ev\)"/)
    assert.match(menu, /if \(item\.keepOpen\) ev\.preventDefault\(\)\s*\n\s*item\.onSelect\(\)/)
  })
})

describe('预览视图阶段 B：共享叶子不反过来依赖预览专属组件', () => {
  const FILES = [
    'components/feedback/AsyncTimelineEntry.vue',
    'components/feedback/CompactTaskRow.vue',
    'components/feedback/TimelineCardEntry.vue',
    'components/feedback/CardStack.vue',
    'components/feedback/CapsuleMarker.vue',
    'components/feedback/EventRowHead.vue',
    'components/feedback/EventStatus.vue',
    'components/confirms/ConfirmCard.vue',
    'components/confirms/ConfirmTimelineCard.vue',
    'components/confirms/DecisionGroupCard.vue',
  ]
  it('共享叶子不引用 preview-view 里的任何组件（依赖方向只能是列表 → 叶子）', () => {
    for (const rel of FILES) {
      const src = read(rel)
      assert.ok(
        !/preview-view\/|Preview(Notice|Message|Report|Reply|Inbox|Confirm)Card|PreviewCardMenu|PreviewAttachments|lib\/sessionImages|cardCopy/.test(src),
        `${rel} 不该引用预览专属组件`
      )
    }
  })
})
