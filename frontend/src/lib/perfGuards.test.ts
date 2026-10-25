// 运行：pnpm --dir frontend run test:unit
// 性能守卫（#198 手机唤起输入法时页面很卡）：只做源码文本扫描，防止已经量出代价的写法被改回去。
// 量出来的事实（只读探针 scripts/e2e-terminal/ro/keyboard-prod.sh，生产预览页约 1.96 万个节点）：
//   - 页面上每一个正在运行的 CSS 动画（哪怕只改 opacity / transform）都会在每一帧强制主线程重算样式；
//   - 54 个「排队加载」占位各自 animate-pulse，是视口变化时主线程开销的大头：去掉后，逐步缩放视口模拟键盘弹出的任务时间
//     248 → 176 ms（收起 375 → 168 ms）。
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, it } from 'node:test'

// 单元测试从 frontend 目录运行（见 installCommand.test.ts 里读 ../internal/... 的写法）。
const src = (rel: string) => readFileSync(resolve(process.cwd(), 'src', rel), 'utf8')

describe('消息卡片里不放持续动画的占位（#198）', () => {
  it('「排队加载」占位是静态的：一页里可能有几十个，持续动画会让每一帧都重算样式', () => {
    for (const file of ['components/feedback/preview-view/PreviewAttachments.vue']) {
      const lines = src(file).split('\n')
      let found = 0
      lines.forEach((line, i) => {
        if (!line.includes('排队加载')) return
        found++
        // 占位的外层 <div …> 在文字之前的几行内（取最近的一个）。
        const start = [lines[i - 1], lines[i - 2], lines[i - 3]].find(l => l?.includes('<div'))
        assert.ok(start, `${file}:${i + 1} 找不到「排队加载」占位的外层 <div>`)
        assert.ok(!start.includes('animate-pulse'), `${file}:${i + 1} 的「排队加载」占位不应带 animate-pulse：${start.trim()}`)
      })
      assert.ok(found >= 1, `${file} 里应至少有一处「排队加载」占位（没有了就说明这条守卫该删了）`)
    }
  })
})

describe('输入栏高度不被视口变化改写（#198）', () => {
  it('handleWindowResize 只夹实际高度，不把夹过的值写回本机记忆', () => {
    const text = src('components/feedback/FeedbackInputDock.vue')
    const m = text.match(/function handleWindowResize\(\) \{([\s\S]*?)\n\}/)
    assert.ok(m, '找不到 handleWindowResize')
    // 只看代码，不看注释（注释里会解释「以前存回 localStorage」）。
    const body = m[1]
      .split('\n')
      .filter(l => !l.trim().startsWith('//'))
      .join('\n')
    assert.ok(!body.includes('saveDockHeight') && !body.includes('localStorage'), `视口变化不应写本机记忆：${body.trim()}`)
    assert.ok(body.includes('preferredDockHeight'), '应按「想要的高度」夹，输入法收起后才能恢复')
    assert.ok(body.includes('isDraggingResize'), '拖动期间不应被改回去')
  })
})
