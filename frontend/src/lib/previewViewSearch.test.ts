// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 E-c1（#120）消息搜索的源码级防线：
//   · 入口「搜索消息」只在预览视图出现；面板与落点逻辑只活在预览视图那几个文件里，经典列表与共享叶子一个字不碰；
//   · 面板直接走会话接口（不依赖聊天视图的 store：那边要先打开聊天会话才能搜），命中只渲染文字（不用 v-html）；
//   · 落点按命中消息的 source 认卡：轮次 / 确认项 / 其余顶层条目 / 兜底，各走现成的机制；
//   · 面板的生命周期：换工作流、离开列表时关掉。
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewSearch.test.ts'))) return base
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
const PANEL = `${PV}PreviewSearchPanel.vue`
const ENTRY = 'components/feedback/MessageSearchLink.vue'

describe('预览视图阶段 E-c1：搜索只活在预览列表与群聊页里', () => {
  it('引用搜索面板 / 开关 / 纯逻辑的文件只有预览列表与群聊页那几个（共享叶子都不碰）', () => {
    const allowed = new Set([
      'lib/previewSearch.ts',
      'lib/previewSearch.test.ts',
      'lib/previewViewSearch.test.ts',
      // #241：群聊页用同一个面板（只管开关与落点）；群页的源码级防线里断言了它挂的是这个面板
      'components/group/GroupChatPane.vue',
      'lib/previewViewGroup.test.ts',
      'composables/usePreviewSearch.ts',
      ENTRY,
      PANEL,
      LIST,
    ])
    const root = srcDir()
    const users = walk(root)
      .map(f => relative(root, f).split('\\').join('/'))
      .filter(rel => /PreviewSearchPanel|usePreviewSearch|lib\/previewSearch|\.\/previewSearch/.test(readFileSync(join(root, rel), 'utf8')))
    const extra = users.filter(rel => !allowed.has(rel))
    assert.deepEqual(extra, [], `这些文件不该引用预览视图的搜索：${extra.join(', ')}`)
  })

  it('入口按钮「搜索消息」：点一下切换面板；入口里不再有视图切换的按钮（#166 删掉经典视图）', () => {
    const src = read(ENTRY)
    assert.match(src, /import \{ usePreviewSearch \} from '\.\.\/\.\.\/composables\/usePreviewSearch'/)
    assert.match(src, /data-testid="preview-search-toggle"/)
    assert.match(src, /@click="previewSearch\.toggle\(\)"/)
    assert.match(src, /:aria-expanded="previewSearch\.open\.value"/)
    assert.ok(!/preview-view-on|preview-view-off|usePreviewView|回到经典视图|试用预览视图/.test(src), '入口里不该再有「回到经典视图 / 试用预览视图」')
  })
})

describe('预览视图阶段 E-c1：搜索面板', () => {
  const panel = read(PANEL)

  it('直接走会话接口：默认只搜文字类消息（SEARCH_KINDS；宿主传空数组 = 不过滤，群聊就是这样）、取最新一批（SEARCH_LIMIT），不依赖聊天视图的 store', () => {
    assert.match(panel, /conversationsApi\.list\(conversationIdOf\(props\.workflowId\), \{ limit: SEARCH_LIMIT, q, kinds: \[\.\.\.props\.kinds\] \}\)/)
    assert.match(panel, /kinds\?: readonly string\[\]/)
    assert.match(panel, /kinds: \(\) => SEARCH_KINDS/)
    assert.ok(!/stores\/chat['"]/.test(panel), '聊天视图的 store 要先打开聊天会话才能搜，预览视图不能依赖它')
  })

  it('输入框占位文字可由宿主给（群聊写「这个群」），默认仍是工作流会话的说法', () => {
    assert.match(panel, /placeholder\?: string/)
    assert.match(panel, /placeholder: '搜索这个工作流的全部消息（汇报 \/ 回复 \/ 追加 \/ 提问 \/ 任务进展）'/)
    assert.match(panel, /:placeholder="props\.workflowId \? props\.placeholder : '这个会话没有工作流编号，无法搜索'"/)
  })

  it('输入防抖 300 毫秒；晚到的旧响应丢弃；面板关闭后在路上的响应不再落地', () => {
    assert.match(panel, /setTimeout\(async \(\) => \{[\s\S]*?\}, 300\)/)
    assert.match(panel, /if \(mine !== seq\) return/)
    assert.match(panel, /onBeforeUnmount\([\s\S]*?seq\+\+/)
  })

  it('只渲染文字：命中用 <mark> 分段，绝不用 v-html', () => {
    assert.ok(!/v-html/.test(panel))
    assert.match(panel, /<mark v-if="part\.hit"/)
    assert.match(panel, /hitSnippet\(m, resultQuery\)/)
  })

  it('键盘：↑ ↓ 选、回车跳、Esc 关；输入法组字时的回车 / 方向键不算', () => {
    assert.match(panel, /e\.isComposing \|\| e\.keyCode === 229/)
    assert.match(panel, /e\.key === 'ArrowDown'/)
    assert.match(panel, /e\.key === 'ArrowUp'/)
    assert.match(panel, /e\.key === 'Enter'/)
    assert.match(panel, /e\.key === 'Escape'/)
  })

  it('点中结果后收起列表只留输入条，并收起手机软键盘；再点输入条 / 改关键词重新展开', () => {
    assert.match(panel, /function pick\(hit: ChatMessage\) \{\s*collapsed\.value = true\s*inputEl\.value\?\.blur\(\)/)
    assert.match(panel, /v-if="trimmed && !collapsed"/)
    assert.match(panel, /@click="collapsed = false"/)
    assert.match(panel, /watch\(trimmed, q => \{[\s\S]{0,120}collapsed\.value = false/)
  })

  it('没有工作流编号的会话：输入框禁用并说明原因', () => {
    assert.match(panel, /:disabled="!props\.workflowId"/)
    assert.match(panel, /这个会话没有工作流编号，无法搜索/)
  })

  it('结果行的测试钩子齐全（e2e 靠它们）', () => {
    for (const id of ['preview-search', 'preview-search-input', 'preview-search-results', 'preview-search-hit', 'preview-search-close', 'preview-search-empty', 'preview-search-more', 'preview-search-error']) {
      assert.ok(panel.includes(`data-testid="${id}"`), `缺少 ${id}`)
    }
    assert.match(panel, /:data-hit-id="m\.id"/)
    assert.match(panel, /:data-hit-source="m\.source\?\.type"/)
  })
})

describe('预览视图阶段 E-c1：落点（PreviewMessageList）', () => {
  const list = read(LIST)

  it('按命中消息的 source 认卡（resolveHit），上下文来自当前已有的轮次 / 条目 / 确认项', () => {
    assert.match(list, /const target = resolveHit\(hit, \{/)
    assert.match(list, /roundIds: new Set\(props\.conversationRounds\.map\(s => s\.session_id\)\)/)
    assert.match(list, /entryKeys: new Set\(timelineEntries\.value\.map\(e => e\.key\)\)/)
    assert.match(list, /confirms\.set\(String\(item\.id\), \{ seq: item\.seq, ref: confirmRef\(item\.seq\) \}\)/)
  })

  it('轮次走现成的 scrollToSession；确认项先摆出来、等卡挂载，再走 focusRef（先清空、下一帧再点名，同一张卡连搜两次也触发）', () => {
    assert.match(list, /case 'round':\s*scrollToSession\(target\.sessionId\)/)
    assert.match(
      list,
      /case 'confirm':[\s\S]*?revealEntry\(target\.key\)\s*confirmStore\.focusRef = ''\s*await nextTick\(\)\s*confirmStore\.focusRef = target\.ref/,
      '必须先 revealEntry 再清空 / 点名：卡在点名之后才挂载就不会滚动（useConfirmFocus 在 setup 里没有根节点）',
    )
  })

  it('其余顶层条目：扩窗到所在间隙、展开所在的摞、滚到那一块并闪一下；认不出的落到时间上最近的上一轮', () => {
    assert.match(list, /function revealEntry\(entryKey: string\)/)
    assert.match(list, /windowToShow\(props\.conversationRounds\.length, idx, visibleRoundsCount\.value\)/)
    assert.match(list, /expandedStacks\.value = new Set\(\[\.\.\.expandedStacks\.value, found\.block\.key\]\)/)
    // 摆出来要先取回新进窗口的轮次的正文（#169 第二期），所以是异步的：等摆好再去滚
    assert.match(list, /async function scrollToEntry\(entryKey: string\): Promise<boolean> \{\s*const found = await revealEntry\(entryKey\)/)
    assert.match(list, /flashElement\(container, el\)/)
    assert.match(list, /const roundId = nearestRoundBefore\(props\.conversationRounds, ms\)/)
  })

  it('闪烁动画与右侧轨共用同一个 flashElement，没有第二份', () => {
    assert.equal((list.match(/function flashElement\(/g) ?? []).length, 1)
    assert.equal((list.match(/setAttribute\('data-rail-flash', ''\)/g) ?? []).length, 1)
    assert.match(list, /function onRailJump[\s\S]*?flashElement\(container, el\)/)
  })

  it('面板的生命周期：换工作流、离开列表时关掉；面板挂在列表根节点里（绝对定位浮层）', () => {
    assert.match(list, /currentWorkflowId\.value = targetWId\s*previewSearch\.close\(\)/)
    assert.match(list, /onBeforeUnmount\(\(\) => \{[\s\S]*?previewSearch\.close\(\)\s*\}\)/)
    assert.match(list, /<PreviewSearchPanel\s+v-if="previewSearch\.open\.value"/)
    assert.match(list, /@pick="jumpToHit"/)
  })
})
