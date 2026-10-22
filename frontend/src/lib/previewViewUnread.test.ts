// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 E-c2（#120）未读的源码级防线：
//   · 分隔线 / 已读标记的逻辑只活在预览视图那几个文件里，经典列表与共享叶子一个字不碰（侧栏只多认一个开关）；
//   · 已读游标只有在「标签页可见 + 停在底部 + 那条消息已经渲染出来」时才推进，且一定带游标（不会把「现在」之前没渲染的消息标成已读）；
//   · 列表接线：换工作流重新冻结游标、滚动 / 新消息重新计时、分隔线画在日期分隔之后、紧挨着那个条目；
//   · 侧栏：预览视图开着时也关注各会话的摘要并显示未读数（聊天视图原有的开关不变）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewUnread.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (/\.(vue|ts)$/.test(name)) out.push(full)
  }
  return out
}

const PV = 'components/feedback/preview-view/'
const LIST = `${PV}PreviewMessageList.vue`
const DIVIDER = `${PV}PreviewUnreadDivider.vue`
const COMPOSABLE = 'composables/usePreviewUnread.ts'
// E-d1 / #165 第二步：群聊窗口（GroupChatPane）复用同一条「以下为新消息」分隔线组件；位置是另一套算法（lib/groupCard.firstUnreadId，按群成员序号），
// 不碰会话预览的已读逻辑（usePreviewUnread / lib/previewUnread）——下面第二条断言守住这一点。
const GROUP_VIEW = 'components/group/GroupChatPane.vue'

describe('预览视图阶段 E-c2：未读只活在预览视图里', () => {
  it('引用分隔线组件 / 已读逻辑 / 纯逻辑的文件只有预览视图那几个', () => {
    const allowed = new Set([
      'lib/previewUnread.ts',
      'lib/previewUnread.test.ts',
      'lib/previewViewUnread.test.ts',
      COMPOSABLE,
      LIST,
      DIVIDER,
      GROUP_VIEW,
      'lib/previewViewGroup.test.ts', // E-d1 的接线断言里提到分隔线组件
    ])
    const root = srcDir()
    const users = walk(root)
      .map(f => relative(root, f).split('\\').join('/'))
      .filter(rel => /PreviewUnreadDivider|usePreviewUnread|lib\/previewUnread|\.\/previewUnread/.test(readFileSync(join(root, rel), 'utf8')))
    const extra = users.filter(rel => !allowed.has(rel))
    assert.deepEqual(extra, [], `这些文件不该引用预览视图的未读逻辑：${extra.join(', ')}`)
  })

  it('群聊窗口（E-d1）只复用分隔线组件，不引用会话预览的已读逻辑', () => {
    const src = read(GROUP_VIEW)
    assert.match(src, /import PreviewUnreadDivider from '\.\.\/feedback\/preview-view\/PreviewUnreadDivider\.vue'/)
    assert.ok(!/usePreviewUnread|lib\/previewUnread|\.\/previewUnread/.test(src), '群聊不走会话预览的已读游标逻辑')
  })

  it('分隔线组件：只是一条带文字的分隔（role=separator），不带右侧轨标记、不用 v-html', () => {
    const src = read(DIVIDER)
    assert.match(src, /role="separator"/)
    assert.match(src, /data-testid="preview-unread-divider"/)
    assert.match(src, /以下为新消息/)
    assert.ok(!/data-rail-/.test(src.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\/.*$/gm, '')), '分隔线不进右侧轨')
    assert.ok(!/v-html/.test(src))
  })
})

describe('预览视图阶段 E-c2：已读游标什么时候推进', () => {
  const src = read(COMPOSABLE)

  it('只有「可见 + 停在底部」才计时 / 才标；请求回来之后再确认一次（这段时间可能已经滚走）', () => {
    assert.match(src, /function viewing\(\): boolean \{\s*return document\.visibilityState === 'visible' && opts\.atBottom\(\)\s*\}/)
    // touch 与 check 都先过 viewing()
    assert.match(src, /function touch\(\)[\s\S]{0,200}if \(!viewing\(\)\)/)
    assert.match(src, /async function check\(\)[\s\S]{0,200}!viewing\(\)/)
    const afterList = src.slice(src.indexOf('const page = await conversationsApi.list(conversationIdOf(wf), { limit: NEWEST_PAGE'))
    assert.match(afterList, /if \(!viewing\(\)\) return[\s\S]{0,200}conversationsApi\.markRead\(/)
  })

  it('取最新一批「计入未读的消息」（UNREAD_KINDS），按渲染情况裁决（readVerdict）；标已读一定带游标', () => {
    assert.match(src, /conversationsApi\.list\(conversationIdOf\(wf\), \{ limit: NEWEST_PAGE, kinds: \[\.\.\.UNREAD_KINDS\] \}\)/)
    assert.match(src, /readVerdict\(\{[\s\S]{0,200}renderedMs: opts\.renderedMs\(\)/)
    assert.match(src, /const cursor = newest\?\.cursor \?\? page\.items\[page\.items\.length - 1\]\?\.cursor/)
    assert.match(src, /conversationsApi\.markRead\(conversationIdOf\(wf\), cursor\)/)
    // 整个文件只有一处标已读
    assert.equal((src.match(/conversationsApi\.markRead\(/g) || []).length, 1)
  })

  it('对不上（页面一直没渲染出那条消息）时限次等待，之后兜底：数字不会一直挂着', () => {
    assert.match(src, /READ_MAX_WAITS = \d+/)
    assert.match(src, /verdict === 'wait' && \+\+waits < READ_MAX_WAITS/)
    assert.match(src, /retryLater\(\)/)
  })

  it('进入时冻结游标（只有进来时有未读才画线）；离开一阵再回来才重新冻结；切会话作废在途请求', () => {
    assert.match(src, /dividerMs\.value === null && page\.unread > 0\) dividerMs\.value = page\.read_ms/)
    assert.match(src, /async function open\(wf: string\)[\s\S]{0,300}seq\+\+[\s\S]{0,200}dividerMs\.value = null/)
    assert.match(src, /AWAY_MS = \d+/)
    assert.match(src, /Date\.now\(\) - hiddenAt >= AWAY_MS/)
    assert.equal((src.match(/if \(mine !== seq\) return/g) || []).length >= 4, true, '每个异步落点都要丢弃晚到的旧响应')
  })

  it('不碰聊天视图的 store 数据：只把数字同步给侧栏（setUnread）', () => {
    assert.match(src, /chatStore\.setUnread\(/)
    assert.ok(!/chatStore\.(items|markRead|open|refresh)\b/.test(src), '聊天视图一次只开一个会话，预览视图不能去动它的数据')
    const store = read('stores/chat.ts')
    assert.match(store, /function setUnread\(id: string, count: number\)/)
    assert.match(store, /unreadOf,\s*setUnread,/)
  })

  it('生命周期：可见性监听随组件挂载 / 卸载，卸载时作废在途请求并清计时器', () => {
    assert.match(src, /onMounted\(\(\) => document\.addEventListener\('visibilitychange', onVisibility\)\)/)
    assert.match(src, /onBeforeUnmount\(dispose\)/)
    assert.match(src, /function dispose\(\)[\s\S]{0,200}seq\+\+[\s\S]{0,200}clearTimer\(\)[\s\S]{0,200}removeEventListener\('visibilitychange', onVisibility\)/)
  })
})

describe('预览视图阶段 E-c2：列表接线', () => {
  const list = read(LIST)

  it('分隔线位置与日期分隔同一个显示顺序（间隙块 → 轮次 → 末尾块）：用 unreadItems 算，游标由 composable 冻结', () => {
    assert.match(list, /const unreadList = computed\(\(\) => unreadItems\(displayedConversationRounds\.value, timelineBlocks\.value, tailBlocks\.value\)\)/)
    assert.match(list, /const previewUnread = usePreviewUnread\(\{\s*atBottom: \(\) => !isScrolledUp\.value,\s*renderedMs: \(\) => renderedAgentMs\(unreadList\.value\),\s*\}\)/)
    assert.match(list, /readMs === null \? null : unreadDividerKey\(unreadList\.value, readMs, hasMoreEarlierRounds\.value\)/)
  })

  it('换工作流重新冻结游标；滚动、渲染出新的 Agent 消息都重新计时', () => {
    assert.match(list, /watch\(\(\) => currentWorkflowId\.value \|\| confirmStore\.workflowId \|\| '', wf => void previewUnread\.open\(wf\), \{ immediate: true \}\)/)
    assert.match(list, /watch\(\(\) => renderedAgentMs\(unreadList\.value\), \(\) => previewUnread\.touch\(\)\)/)
    const scroll = list.slice(list.indexOf('function handleScroll()'))
    assert.match(scroll.slice(0, scroll.indexOf('\n}\n')), /previewUnread\.touch\(\)/)
  })

  it('两处模板：间隙块与轮次各画一条，都紧跟在日期分隔之后（日期在上、「以下为新消息」紧挨着条目）', () => {
    const gap = list.indexOf('<PreviewUnreadDivider v-if="unreadMarkKey === block.key" />')
    const gapDate = list.indexOf('<PreviewDateSeparator v-if="dayMarks[block.key]"')
    assert.ok(gap > 0 && gapDate > 0 && gapDate < gap, '间隙块：日期分隔在前')
    const round = list.indexOf('<PreviewUnreadDivider v-if="unreadMarkKey === session.session_id" />')
    const roundDate = list.indexOf('<PreviewDateSeparator v-if="dayMarks[session.session_id]"')
    const roundCard = list.indexOf(':id="`session-${session.session_id}`"')
    assert.ok(roundDate > 0 && round > roundDate && roundCard > round, '轮次：日期分隔 → 以下为新消息 → 轮次卡')
    assert.equal((list.match(/<PreviewUnreadDivider /g) || []).length, 2)
  })
})

describe('预览视图阶段 E-c2：侧栏', () => {
  it('预览视图开着时关注各会话的摘要、显示未读数（#165：旧的聊天视图开关已删除，只剩这一个）', () => {
    const bar = read('components/AppSidebar.vue')
    assert.match(bar, /previewView\.enabled\.value \? displayedItems\.value/)
    assert.ok(!/chatPreview/.test(bar), '侧栏不应再有聊天视图的开关')
    const card = read('components/sidebar/SidebarItemCard.vue')
    assert.match(card, /v-if="previewView\.enabled\.value && item\.type === 'feedback' && chatStore\.unreadOf\(item\.workflow_id\) > 0"/)
    assert.ok(!/chatPreview/.test(card), '侧栏卡片不应再有聊天视图的开关')
    assert.match(card, /data-testid="sidebar-unread"/)
  })
})
