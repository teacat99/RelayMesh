// 运行：pnpm --dir frontend run test:unit
// 预览视图「事件行」的纯逻辑（#120 阶段 1 的一行预览提取，阶段 C2 起的行高常量）。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { EVENT_BOX, EVENT_LINE_PX, EVENT_ROW_MAX_PX, EVENT_ROW_PX, firstLinePreview } from './eventRow'

describe('firstLinePreview：把 Markdown 压成一行预览', () => {
  it('取第一行有内容的文字，去掉粗体与列表标记', () => {
    assert.equal(firstLinePreview('**正在做**：第 1 步（abc）\n\n- 读代码\n- 改实现'), '正在做：第 1 步（abc）')
    assert.equal(firstLinePreview('\n\n- 先做这个\n- 再做那个'), '先做这个')
    assert.equal(firstLinePreview('3. 第三项'), '第三项')
    assert.equal(firstLinePreview('- [x] 已完成的事'), '已完成的事')
  })

  it('标题与引用的标记也去掉', () => {
    assert.equal(firstLinePreview('## 第 2 轮方案\n正文'), '第 2 轮方案')
    assert.equal(firstLinePreview('> 引用的一句话'), '引用的一句话')
  })

  it('链接留文字、图片留说明、行内代码去反引号', () => {
    assert.equal(firstLinePreview('见 [设计文档](design/a.md) 和 `go test ./...`'), '见 设计文档 和 go test ./...')
    assert.equal(firstLinePreview('![截图说明](/a.png) 之后的文字'), '截图说明 之后的文字')
    assert.equal(firstLinePreview('~~作废~~ 的内容'), '作废 的内容')
  })

  it('不误伤下划线的标识符与星号乘法', () => {
    assert.equal(firstLinePreview('字段 snake_case_name 与 a * b * c'), '字段 snake_case_name 与 a * b * c')
  })

  it('代码围栏的开关行与分隔线、表格对齐行跳过', () => {
    assert.equal(firstLinePreview('```go\nfunc Sum() {}\n```'), 'func Sum() {}')
    assert.equal(firstLinePreview('---\n正文'), '正文')
    assert.equal(firstLinePreview('|---|---|\n| a | b |'), '| a | b |')
  })

  it('没有可显示的文字返回空串', () => {
    assert.equal(firstLinePreview(''), '')
    assert.equal(firstLinePreview('   \n\n  '), '')
    assert.equal(firstLinePreview('---\n***\n```\n```'), '')
  })

  it('过长的内容用省略号截断，且不超过 max 个字符', () => {
    const out = firstLinePreview('长'.repeat(500), 40)
    assert.equal(out.length, 40)
    assert.ok(out.endsWith('…'))
    assert.equal(firstLinePreview('短', 40), '短')
  })

  it('Windows 换行也能处理', () => {
    assert.equal(firstLinePreview('第一行\r\n第二行'), '第一行')
  })
})

describe('事件行的版式常量', () => {
  it('行高是写死的：32px = 上下边框各 1px + 一行 30px，上限只留 4px 余量', () => {
    assert.equal(EVENT_ROW_PX, 32)
    assert.equal(EVENT_LINE_PX, 30)
    assert.equal(EVENT_LINE_PX + 2, EVENT_ROW_PX)
    assert.ok(EVENT_ROW_MAX_PX > EVENT_ROW_PX && EVENT_ROW_MAX_PX <= EVENT_ROW_PX + 6, `上限 ${EVENT_ROW_MAX_PX}px 应略大于基线 ${EVENT_ROW_PX}px`)
  })

  it('盒子 = 1px 描边 + 3px 左色条（取 --card-tone）+ 卡片底色，没有任何内边距（高度全由里面那一行决定）', () => {
    const tokens = EVENT_BOX.split(/\s+/)
    for (const need of ['border', 'border-l-[3px]', 'border-l-[color:var(--card-tone)]', 'bg-card', 'rounded-lg']) {
      assert.ok(tokens.includes(need), `事件行的盒子缺少 ${need}：${EVENT_BOX}`)
    }
    assert.ok(!tokens.some(t => /^p[xytblr]?-/.test(t)), `事件行的盒子不该带内边距（会撑高）：${EVENT_BOX}`)
  })
})
