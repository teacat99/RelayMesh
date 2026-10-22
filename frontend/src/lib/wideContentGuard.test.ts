// 运行：pnpm --dir frontend run test:unit
// 消息列表的「宽内容」源码级防线（#167 / #283）：手机上历史里个别内容比卡片宽，把列表滚动区撑宽，右侧出现一片能横向滑动的空白。
// 行为由端到端套件 scripts/e2e-terminal/e2e/wide-content.mjs（以及 preview-view.mjs 的手机步骤）在真实页面里量；
// 这里只钉住五处「修复靠它们成立」的写法，免得有人整理样式时把其中一处顺手删掉、而 e2e 又没跑到：
//   1. MarkdownRenderer 的全局样式：文字块允许在任意处折行；表格单元格恢复成默认；手机窄屏上表格 display:block + 自己横向滚动，
//      并且这些规则不得写到「桌面也生效」的位置（桌面上的表格保持 width:100% 的整张表）；
//   2. 两个消息列表的滚动容器在手机上不许横向滚动（兜底）；
//   3. 用户消息的纯文本块（带 pre-wrap 的 LinkedText）都带 overflow-wrap:anywhere；
//   4. 预览视图折叠的摞头在手机上不画时间范围（类型标签 + 跨天的范围比一行宽，shrink-0 的箭头会被挤出卡片）；
//   5. 「分布条 + 数字」的紧凑汇总（会话页的项目任务总览、每一轮的「本轮任务」条）：两条短条能收窄、数字完整，
//      放它的容器不是 shrink-0（360px 宽的手机上一行装不下，右边会被切掉一截）。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'wideContentGuard.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const MARKDOWN = 'components/MarkdownRenderer.vue'
const CLASSIC = 'components/feedback/FeedbackMessageList.vue'
const PREVIEW = 'components/feedback/preview-view/PreviewMessageList.vue'

/** 取出 <style> 里的全部 CSS（MarkdownRenderer 只有一个全局 style 块）。 */
function styleOf(src: string): string {
  const m = src.match(/<style[^>]*>([\s\S]*?)<\/style>/)
  assert.ok(m, '找不到 <style> 块')
  return m[1]
}

/** 把 CSS 切成「顶层规则」：普通规则返回 { selector, body }；@media 返回 { media, inner }。 */
function topLevelRules(css: string): Array<{ media?: string; selector?: string; body?: string; inner?: string }> {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: Array<{ media?: string; selector?: string; body?: string; inner?: string }> = []
  let i = 0
  while (i < text.length) {
    const open = text.indexOf('{', i)
    if (open < 0) break
    const head = text.slice(i, open).trim()
    let depth = 1
    let j = open + 1
    while (j < text.length && depth > 0) {
      if (text[j] === '{') depth++
      else if (text[j] === '}') depth--
      j++
    }
    const body = text.slice(open + 1, j - 1)
    if (head.startsWith('@media')) out.push({ media: head.replace(/^@media\s*/, '').trim(), inner: body })
    else out.push({ selector: head, body })
    i = j
  }
  return out
}

describe('Markdown 全局样式：长串折行与手机上的表格（#167）', () => {
  const rules = topLevelRules(styleOf(read(MARKDOWN)))
  const plain = rules.filter(r => r.selector)
  const phone = rules.find(r => r.media === '(max-width: 767.98px)')

  it('文字块（p / li / blockquote / 标题）允许在任意位置折行，所有宽度都生效', () => {
    const r = plain.find(x => /overflow-wrap/.test(x.body ?? '') && /:where\(p, li, blockquote, h1, h2, h3, h4, h5, h6\)/.test(x.selector ?? ''))
    assert.ok(r, '缺少 .markdown-body :where(p, li, blockquote, h1…h6) { overflow-wrap: anywhere }')
    assert.match(r.body ?? '', /overflow-wrap:\s*anywhere/)
    assert.match(r.selector ?? '', /^\.markdown-body\s/)
  })

  it('表格单元格先恢复成默认折行（免得嵌在 li / blockquote 里的表格继承 anywhere、桌面的列宽分配变样）', () => {
    const r = plain.find(x => /:where\(th, td\)/.test(x.selector ?? ''))
    assert.ok(r, '缺少 .markdown-body :where(th, td) { overflow-wrap: normal }')
    assert.match(r.body ?? '', /overflow-wrap:\s*normal/)
  })

  it('桌面的表格规则不变：width:100%，没有 display:block / overflow-x', () => {
    const r = plain.find(x => (x.selector ?? '').trim() === '.markdown-body table')
    assert.ok(r, '找不到 .markdown-body table 规则')
    assert.match(r.body ?? '', /width:\s*100%/)
    assert.doesNotMatch(r.body ?? '', /display\s*:/)
    assert.doesNotMatch(r.body ?? '', /overflow/)
  })

  it('手机窄屏（与 PHONE_MEDIA 同一个断点）：表格 display:block、按内容宽度排、自己横向滚动；单元格有最小宽度并允许折行', () => {
    assert.ok(phone, '缺少 @media (max-width: 767.98px) 块')
    const inner = topLevelRules(phone.inner ?? '')
    const table = inner.find(x => (x.selector ?? '').trim() === '.markdown-body table')
    assert.ok(table, '手机块里缺少 .markdown-body table 规则')
    assert.match(table.body ?? '', /display:\s*block/)
    assert.match(table.body ?? '', /width:\s*max-content/)
    assert.match(table.body ?? '', /max-width:\s*100%/)
    assert.match(table.body ?? '', /overflow-x:\s*auto/)
    const cell = inner.find(x => /\.markdown-body td/.test(x.selector ?? ''))
    assert.ok(cell, '手机块里缺少 th / td 规则')
    assert.match(cell.body ?? '', /min-width:\s*4\.5em/)
    assert.match(cell.body ?? '', /overflow-wrap:\s*anywhere/)
  })

  it('断点与前端统一的手机判定一致（PHONE_MEDIA）', () => {
    const lib = read('lib/pendingSessions.ts')
    const m = lib.match(/PHONE_MEDIA\s*=\s*['"]([^'"]+)['"]/)
    assert.ok(m, '在 lib/pendingSessions.ts 里找不到 PHONE_MEDIA')
    assert.equal(m[1], '(max-width: 767.98px)')
  })
})

describe('两个消息列表的滚动容器：手机上不许横向滚动（兜底）', () => {
  for (const [name, file] of [['经典', CLASSIC], ['预览', PREVIEW]] as const) {
    it(`${name}列表：h-full overflow-y-auto 的滚动容器带 max-md:overflow-x-hidden`, () => {
      const src = read(file)
      const m = src.match(/class="(h-full overflow-y-auto[^"]*)"/)
      assert.ok(m, '找不到消息列表滚动容器')
      assert.match(m[1], /(^|\s)max-md:overflow-x-hidden(\s|$)/)
    })
  }
})

describe('用户消息的纯文本块：很长的不可断字符串（链接等）要能折行', () => {
  const cases: Array<[string, string, number]> = [
    ['经典列表（确认卡 + 单独回复）', CLASSIC, 2],
    ['预览：回复卡', 'components/feedback/preview-view/PreviewReplyCard.vue', 1],
    ['预览：追加指令卡', 'components/feedback/preview-view/PreviewInboxCard.vue', 1],
  ]
  for (const [name, file, expected] of cases) {
    it(`${name}：${expected} 处 pre-wrap 的 LinkedText 都带 [overflow-wrap:anywhere]`, () => {
      const src = read(file)
      const blocks = [...src.matchAll(/<LinkedText\b[^>]*?class="([^"]*whitespace-pre-wrap[^"]*)"/g)].map(m => m[1])
      assert.equal(blocks.length, expected, `${file} 里 pre-wrap 的 LinkedText 数量变了：${blocks.length}`)
      for (const cls of blocks) assert.match(cls, /\[overflow-wrap:anywhere\]/)
    })
  }
})

describe('预览视图折叠的摞头：手机上不画跨天的时间范围', () => {
  it('EventRowHead 有 phoneHideTime，且 <time> 在它为真时带 max-sm:hidden', () => {
    const src = read('components/feedback/EventRowHead.vue')
    assert.match(src, /phoneHideTime\?:\s*boolean/)
    const tag = src.match(/<time\b[^>]*>/)
    assert.ok(tag, '找不到 <time> 标签')
    assert.match(tag[0], /\$slots\.trail\s*\|\|\s*phoneHideTime\s*\?\s*'max-sm:hidden'/)
  })

  it('CardStack 的 compact 事件行传 phone-hide-time', () => {
    const src = read('components/feedback/CardStack.vue')
    const tag = src.match(/<EventRowHead\b[^>]*>/)
    assert.ok(tag, '找不到 compact 分支的 <EventRowHead>')
    assert.match(tag[0], /(^|\s)phone-hide-time(\s|$)/)
    assert.match(tag[0], /:time="range"/)
  })
})

describe('紧凑汇总（分布条 + 数字）：窄屏上短条先让位，数字不被切掉', () => {
  it('WorkTrackSummary 紧凑版的两条短条可收窄（shrink + min-w-4），340px 以下不画，不再是 shrink-0', () => {
    const src = read('components/work/WorkTrackSummary.vue')
    const bars = [...src.matchAll(/class="(flex h-1\.5 w-16[^"]*)"/g)].map(m => m[1])
    assert.equal(bars.length, 2, '紧凑版应有两条 64px 的短条（阶段、状态）')
    for (const cls of bars) {
      assert.match(cls, /(^|\s)shrink(\s|$)/)
      assert.match(cls, /(^|\s)min-w-4(\s|$)/)
      assert.match(cls, /(^|\s)max-\[340px\]:hidden(\s|$)/)
      assert.doesNotMatch(cls, /shrink-0/)
    }
    // 数字一直完整：不折行、不收缩到比文字窄
    assert.match(src, /whitespace-nowrap font-mono text-\[10px\] tabular-nums[^"]*"[^>]*data-testid="work-track-summary-overall"/)
  })

  it('每一轮的「本轮任务」条：放汇总的容器是 ml-auto min-w-0（不是 shrink-0）', () => {
    const src = read('components/feedback/SessionTasks.vue')
    const m = src.match(/<span class="(ml-auto[^"]*)"><WorkTrackSummary\b/)
    assert.ok(m, '找不到放汇总的容器')
    assert.match(m[1], /(^|\s)min-w-0(\s|$)/)
    assert.doesNotMatch(m[1], /shrink-0/)
  })

  it('项目任务总览：放汇总的容器是 min-w-0（可收窄）', () => {
    const src = read('components/feedback/WorkflowTaskStrip.vue')
    const m = src.match(/<span class="([^"]*)"><WorkTrackSummary\b/)
    assert.ok(m, '找不到放汇总的容器')
    assert.match(m[1], /(^|\s)min-w-0(\s|$)/)
    assert.doesNotMatch(m[1], /shrink-0/)
  })
})
