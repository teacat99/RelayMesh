// 运行：pnpm --dir frontend run test:unit
// 守护输入区两处可被调用方覆盖的文案（#249）：发送按钮文字（buttonText）与输入框占位提示（placeholder）。
// 事故：这两个属性声明了、托管自驾页也传了，组件里却没有任何地方读它们，「发送最高指令」从未显示过。
// 纯函数给行为断言；组件本身没有 DOM 测试，所以另用源码断言钉住「组件确实经过这两个函数读属性、任务页确实传、会话页不传」。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { pickPlaceholder, withButtonText, type SubmitButtonInfo } from './dockText'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'dockText.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

const base: SubmitButtonInfo = {
  label: '追加指令',
  submittingLabel: '暂存中...',
  description: '当前无交互：指令进入 Agent 的收件箱',
}

describe('withButtonText：按钮文字', () => {
  it('没传（undefined / 空串 / 纯空白）：原样返回，会话页的文案不受影响', () => {
    for (const custom of [undefined, '', '   ', '\n\t']) {
      assert.equal(withButtonText(base, custom), base)
    }
  })
  it('传了：覆盖主文案并去掉首尾空白；提交中的文案保留；按会话状态写的悬停提示清空', () => {
    const got = withButtonText(base, '  发送最高指令 ')
    assert.equal(got.label, '发送最高指令')
    assert.equal(got.submittingLabel, '暂存中...')
    assert.equal(got.description, '')
  })
  it('不改入参（computed 里每次都拿同一个对象）', () => {
    const snapshot = { ...base }
    withButtonText(base, '发送最高指令')
    assert.deepEqual(base, snapshot)
  })
})

describe('pickPlaceholder：输入框占位提示', () => {
  const fallback = '追加补充指令...（当前无交互）'
  it('没传（undefined / 空串 / 纯空白）：用会话状态推出来的那一句', () => {
    for (const custom of [undefined, '', '   ']) {
      assert.equal(pickPlaceholder(custom, fallback), fallback)
    }
  })
  it('传了：用调用方的（去掉首尾空白）', () => {
    assert.equal(pickPlaceholder(' 向指挥端投递人工最高指令 ', fallback), '向指挥端投递人工最高指令')
  })
})

describe('接线（源码断言）', () => {
  const dock = read('components/feedback/FeedbackInputDock.vue')
  /** 取出某个 <FeedbackInputDock ... /> 标签的开头到第一个 "/>" 为止。 */
  const tagOf = (src: string) => {
    const start = src.indexOf('<FeedbackInputDock')
    assert.ok(start >= 0, '找不到 <FeedbackInputDock')
    const end = src.indexOf('/>', start)
    assert.ok(end > start, '找不到标签结尾')
    return src.slice(start, end)
  }

  it('输入区经过 withButtonText / pickPlaceholder 读 buttonText / placeholder 两个属性', () => {
    assert.match(dock, /withButtonText\(\s*[\w.]+\s*,\s*props\.buttonText\s*\)/, 'buttonText 没有被读')
    assert.match(dock, /pickPlaceholder\(\s*props\.placeholder\s*,/, 'placeholder 没有被读')
  })
  it('发送按钮渲染的是 submitButtonInfo（覆盖后的那一份），不是会话状态推出来的原始值', () => {
    assert.match(dock, /submitButtonInfo\.label/)
    assert.match(dock, /submitButtonInfo\.description/)
    assert.doesNotMatch(dock, /sessionSubmitButtonInfo\.(label|description|submittingLabel)/)
  })
  it('任务页传这两个属性；会话页都不传（继续显示由会话状态推出来的文案）', () => {
    const task = tagOf(read('components/feedback/TaskWorkspace.vue'))
    assert.match(task, /:button-text=/)
    assert.match(task, /:placeholder=/)
    const session = tagOf(read('views/FeedbackView.vue'))
    assert.doesNotMatch(session, /button-text/)
    assert.doesNotMatch(session, /placeholder/)
  })
})
