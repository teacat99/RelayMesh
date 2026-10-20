// 运行：pnpm --dir frontend run test:unit
// 会话流排版的源码级防回退检查。
//
// 背景：Tailwind v4 的 space-y-* 写的是 margin-block-end（挂在除最后一个之外的每个子元素上），
// 子元素上再写一个负的 mb-* 并不是「在间距上再减一点」，而是直接把间距整个覆盖成负数，
// 于是恢复胶囊、「期间有新提交」分隔条会压在下一轮的标题上（用户截图 2、3、4）。
// 正确做法是：胶囊 / 分隔条 / 本轮卡片放进同一个「轮次组」，组内用 space-y-3，组间交给外层。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'feedLayout.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

function read(rel: string): string {
  return readFileSync(join(srcDir(), rel), 'utf8')
}

// 取出所有 class="..." 中的字面量类名片段
function classLiterals(source: string): string[] {
  const out: string[] = []
  const re = /\bclass="([^"]*)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source)) !== null) out.push(m[1])
  return out
}

const NEGATIVE_VERTICAL_MARGIN = /(?:^|\s)-m[tby]-/
const ANY_VERTICAL_MARGIN = /(?:^|\s)-?m[tby]-/

describe('会话流排版', () => {
  const list = read('components/feedback/FeedbackMessageList.vue')
  const capsule = read('components/feedback/CapsuleMarker.vue')

  it('会话流与恢复胶囊都不使用负的纵向外边距', () => {
    for (const [name, source] of [['FeedbackMessageList.vue', list], ['CapsuleMarker.vue', capsule]] as const) {
      for (const cls of classLiterals(source)) {
        assert.ok(!NEGATIVE_VERTICAL_MARGIN.test(cls), `${name} 里出现负纵向外边距：${cls}`)
      }
    }
  })

  it('每一轮都包在「轮次组」里，组内间距由 space-y-3 给出', () => {
    const group = /<div\s+v-for="session in displayedConversationRounds"[^>]*?class="space-y-3"[^>]*?data-testid="turn-group"/s
    assert.match(list, group, '缺少 data-testid="turn-group" 的轮次组容器')
  })

  it('时间线末尾的条目同样成组', () => {
    assert.match(list, /data-testid="timeline-tail"/)
  })

  it('轮次组里同时有间隙条目（含恢复胶囊）、分隔条和本轮卡片', () => {
    const start = list.indexOf('data-testid="turn-group"')
    const end = list.indexOf('data-testid="timeline-tail"')
    assert.ok(start > 0 && end > start, '轮次组与末尾组的顺序不对')
    const body = list.slice(start, end)
    // 间隙里的条目（您的追加 / 进度通知 / 任务进展卡 / 确认卡 / 恢复胶囊……）合并成一条流，只经由同一个可复用模板渲染
    assert.match(body, /<ReuseGap\s[^>]*?timelineBlocks\[session\.session_id\]/s)
    assert.match(body, /data-testid="anchor-divider"/)
    assert.match(body, /:id="`session-\$\{session\.session_id\}`"/)
    // 恢复胶囊在间隙模板里渲染
    const gap = list.slice(list.indexOf('<DefineGap'), list.indexOf('</DefineGap>'))
    assert.match(gap, /<CapsuleMarker/)
  })

  it('轮次之外的条目只有一条合并后的流：不再各类各自一份独立落位', () => {
    for (const stale of ['inboxPlacement', 'confirmPlacement', 'extraPlacement', 'capsulePlacement']) {
      assert.ok(!list.includes(stale), `FeedbackMessageList.vue 里不应再出现独立的落位流 ${stale}`)
    }
  })

  it('轮次组的直接子元素（胶囊、分隔条）不带自己的纵向外边距，免得覆盖 space-y', () => {
    const divider = list.match(/<div\s[^>]*data-testid="anchor-divider"[^>]*>/s)
    assert.ok(divider, '找不到分隔条')
    const cls = divider[0].match(/\bclass="([^"]*)"/)
    assert.ok(cls, '分隔条没有 class')
    assert.ok(!ANY_VERTICAL_MARGIN.test(cls[1]), `分隔条不应带纵向外边距：${cls[1]}`)

    const markerTag = list.match(/<CapsuleMarker\s[^>]*?\/>/s)
    assert.ok(markerTag, '找不到间隙里的胶囊')
    assert.ok(!/\sclass=/.test(markerTag[0]), '胶囊不应再通过 class 传入外边距')
  })
})
