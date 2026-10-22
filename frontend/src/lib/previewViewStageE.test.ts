// 运行：pnpm --dir frontend run test:unit
// 预览视图阶段 E-a / E-b（#120）的源码级防线：
//   E-a Artifact 引用卡：智能体写的正文（汇报 / 进度通知 / 提问与计划）统一走 PreviewMarkdown——记号 [[artifact:N]] 从文字里去掉、
//       每个编号一张引用卡；您自己写的话、经典视图与共享叶子一个字不碰；
//   E-b 日期分隔：列表按「间隙里的块 → 轮次 → …… → 末尾的块」的顺序算每个日历日的第一个条目，只在顶层画，不进右侧轨。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { extractArtifactRefs } from './artifactRef'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'previewViewStageE.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const PV = 'components/feedback/preview-view/'
const LIST = `${PV}PreviewMessageList.vue`
const MARKDOWN = `${PV}PreviewMarkdown.vue`
const SEPARATOR = `${PV}PreviewDateSeparator.vue`
const AGENT_CARDS = ['PreviewReportCard.vue', 'PreviewNoticeCard.vue', 'PreviewConfirmCard.vue']

describe('预览视图阶段 E-a：智能体正文里的 Artifact 引用卡', () => {
  const md = read(MARKDOWN)

  it('PreviewMarkdown：先抽出记号，把去掉记号的正文交给 MarkdownRenderer，再按编号各画一张引用卡', () => {
    assert.match(md, /import \{ extractArtifactRefs \} from '\.\.\/\.\.\/\.\.\/lib\/artifactRef'/)
    assert.match(md, /const extracted = computed\(\(\) => extractArtifactRefs\(props\.content\)\)/)
    assert.match(md, /<MarkdownRenderer :content="extracted\.text" :workflow-id="workflowId" \/>/)
    assert.match(md, /<ArtifactRefCard v-for="id in extracted\.ids" :key="id" :doc-id="id" \/>/)
    assert.match(md, /v-if="extracted\.ids\.length"/, '没有记号时不多渲染任何节点（与直接用 MarkdownRenderer 完全一样）')
    assert.ok(!/v-html/.test(md), '自己不渲染 HTML：正文一律交给 MarkdownRenderer（DOMPurify 白名单）')
  })

  it('三张智能体卡（汇报 / 进度通知 / 提问与计划）的正文都走 PreviewMarkdown，不再直接用 MarkdownRenderer', () => {
    for (const f of AGENT_CARDS) {
      const src = read(PV + f)
      assert.match(src, /import PreviewMarkdown from '\.\/PreviewMarkdown\.vue'/, `${f} 没引入 PreviewMarkdown`)
      assert.equal((src.match(/<PreviewMarkdown\b/g) ?? []).length, 1, `${f} 应恰好渲染一次 PreviewMarkdown`)
      assert.ok(!/MarkdownRenderer/.test(src), `${f} 不该再直接用 MarkdownRenderer（记号会当成普通文字露出来）`)
    }
  })

  it('您自己写的话（回复 / 追加）不抽取记号；经典视图与共享叶子不引用预览目录里的新组件', () => {
    for (const f of ['PreviewReplyCard.vue', 'PreviewInboxCard.vue']) {
      assert.ok(!/PreviewMarkdown|extractArtifactRefs/.test(read(PV + f)), `${f} 不该抽取 Artifact 记号`)
    }
    for (const rel of ['components/feedback/FeedbackMessageList.vue', 'components/confirms/ConfirmCard.vue', 'components/confirms/ConfirmTimelineCard.vue', 'components/MarkdownRenderer.vue']) {
      const src = read(rel)
      assert.ok(!/PreviewMarkdown|PreviewDateSeparator/.test(src), `${rel} 不该引用预览视图的新组件`)
    }
  })

  it('抽取规则本身（聊天视图沿用）：去掉记号、去重、保序，正文里别的方括号不动', () => {
    const r = extractArtifactRefs('先看看看板\n\n[[artifact:12]]\n\n再看 [[artifact:7]] 和 [[artifact:12]]，另见 [[design/x.md]]')
    assert.deepEqual(r.ids, [12, 7])
    assert.ok(!r.text.includes('artifact:'))
    assert.ok(r.text.includes('[[design/x.md]]'))
    assert.deepEqual(extractArtifactRefs('没有记号').ids, [])
    assert.equal(extractArtifactRefs('没有记号').text, '没有记号')
  })
})

describe('预览视图阶段 E-b：日期分隔', () => {
  const list = read(LIST)
  const sep = read(SEPARATOR)
  const script = list.slice(0, list.indexOf('</script>'))
  const tpl = list.slice(list.indexOf('<template>'))

  it('分隔条本身：只是一条带标签的分隔，没有交互，也不带右侧轨的 data-rail-*', () => {
    const sepTpl = sep.slice(sep.indexOf('<template>')) // 只看模板：脚本里的注释会提到 data-rail-*
    assert.match(sepTpl, /data-testid="date-separator"/)
    assert.match(sepTpl, /role="separator"/)
    assert.ok(!/data-rail|railDomAttrs|@click|<button/.test(sepTpl), '日期分隔不进右侧轨、也没有交互')
  })

  it('列表里恰好两处画分隔：间隙里的块之前、轮次之前；轮次那一处在「期间有新提交」分隔条之前', () => {
    assert.match(list, /import PreviewDateSeparator from '\.\/PreviewDateSeparator\.vue'/)
    assert.match(list, /import \{ daySeparators, type DayItem \} from '\.\.\/\.\.\/\.\.\/lib\/daySeparators'/)
    const uses = [...tpl.matchAll(/<PreviewDateSeparator\b[^>]*>/g)].map(m => m[0])
    assert.equal(uses.length, 2, '间隙里的块、轮次各一处')
    assert.ok(uses.some(u => /v-if="dayMarks\[block\.key\]"/.test(u)), '间隙里的块按 block.key 取标记')
    assert.ok(uses.some(u => /v-if="dayMarks\[session\.session_id\]"/.test(u)), '轮次按 session_id 取标记')
    const atRound = tpl.indexOf('v-if="dayMarks[session.session_id]"')
    assert.ok(tpl.indexOf('<ReuseGap :blocks="timelineBlocks[session.session_id]') < atRound, '轮次的分隔排在它前面的间隙块之后')
    assert.ok(atRound < tpl.indexOf('data-testid="anchor-divider"'), '轮次的分隔排在「期间有新提交」分隔条之前')
    assert.ok(atRound < tpl.indexOf(':id="`session-${session.session_id}`"'), '轮次的分隔排在轮次本体之前')
  })

  it('间隙里的块外面包一层带键的 <template v-for>：分隔条与块并列，块根节点仍带 data-rail-*', () => {
    assert.match(tpl, /<template v-for="block in blocks" :key="block\.key">/)
    const block = tpl.match(/<div\s+:data-timeline-key="block\.key"[\s\S]*?>/)
    assert.ok(block, '找不到块的根节点')
    assert.match(block[0], /v-bind="railDomAttrs\(railMetaOfBlock\(block\)\)"/)
  })

  it('dayMarks 按显示顺序收集顶层条目：每个间隙的块 → 该轮次 →（下一个间隙……）→ 末尾的块；轮次里面的块不参与', () => {
    const start = script.indexOf('const dayMarks = stableComputed')
    assert.ok(start > 0, '找不到 dayMarks（要用 stableComputed：内容没变就不换对象，#215）')
    const body = script.slice(start, script.indexOf('\n})\n', start))
    const gap = body.indexOf('timelineBlocks.value[s.session_id]')
    const round = body.indexOf('items.push({ key: s.session_id, at: s.created_at })')
    const tail = body.indexOf('pushBlocks(tailBlocks.value)')
    assert.ok(gap > 0 && round > gap && tail > round, '顺序应为 间隙块 → 轮次 → 末尾块')
    assert.ok(!/inRoundKey/.test(body), '轮次里面的块（提问与回复之间）不是顶层条目，不能参与日期分隔')
    assert.match(body, /b\.type === 'stack' \? b\.entries\[0\]\.at : b\.entry\.at/, '摞取第一张卡的时间，与 data-timeline-at 一致')
    // 「今天 / 昨天」过零点会自己变，但不能直接读每 10 秒走一下的 inboxNow：那样每 10 秒 dayMarks 就是一个新对象，整条时间线跟着重画（#215）。
    // 只依赖 todayKey（「今天是几号」这个字符串，由低频时钟推出），日期标签取当下时间
    assert.match(body, /void todayKey\.value/, '依赖 todayKey')
    assert.match(body, /daySeparators\(items, new Date\(\)\)/, '日期标签用当下时间')
    assert.ok(!/inboxNow/.test(body), 'dayMarks 里不能直接读 inboxNow')
    assert.match(script, /const todayKey = computed\(\(\) => \{\s*const d = new Date\(inboxNow\.value\)\s*return `\$\{d\.getFullYear\(\)\}-\$\{d\.getMonth\(\)\}-\$\{d\.getDate\(\)\}`\s*\}\)/, 'todayKey 由低频时钟推出日历日')
  })

  it('经典列表没有日期分隔（只改预览）', () => {
    assert.ok(!/date-separator|daySeparators/.test(read('components/feedback/FeedbackMessageList.vue')))
  })
})
