// 运行：pnpm --dir frontend run test:unit
// 输入法（IME）组合期间的按键判断 + 源码层面的防回退检查：
// ExecMesh 里「回车提交 / Esc 放弃」的输入框（重命名会话、主机备注、接入主机备注、记录搜索）
// 必须先判断输入法状态——否则中文名字没打完，回车确认候选词就把输入框提交掉了。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

import { isImeKey } from './ime'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'ime.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

function read(rel: string): string {
  return readFileSync(join(srcDir(), rel), 'utf8')
}

describe('isImeKey', () => {
  it('组合期间的按键（Chrome / Firefox：isComposing）', () => {
    assert.equal(isImeKey({ isComposing: true, keyCode: 13 }), true)
  })

  it('keyCode 229（Safari 在 compositionend 之后才触发确认用的回车；Windows 上的 Chrome 也是 229）', () => {
    assert.equal(isImeKey({ isComposing: false, keyCode: 229 }), true)
  })

  it('普通按键不是输入法按键', () => {
    assert.equal(isImeKey({ isComposing: false, keyCode: 13 }), false)
    assert.equal(isImeKey({ isComposing: false, keyCode: 27 }), false)
  })

  it('合成的键盘事件缺字段时按普通按键处理', () => {
    assert.equal(isImeKey({} as KeyboardEvent), false)
  })
})

// 这些文件里的回车 / Esc 处理必须经过 isImeKey；不允许退回 Vue 的 .enter / .esc 修饰符直接提交。
const GUARDED: Array<{ file: string; why: string }> = [
  { file: 'components/exec/ExecSidebar.vue', why: '重命名会话' },
  { file: 'views/exec/ExecHostsView.vue', why: '主机备注' },
  { file: 'components/terminal/DiscoveryDialog.vue', why: '接入主机时的备注' },
  { file: 'components/terminal/RecordViewer.vue', why: '记录搜索' },
]

describe('回车 / Esc 处理必须先判断输入法状态', () => {
  for (const { file, why } of GUARDED) {
    it(`${file}（${why}）`, () => {
      const src = read(file)
      assert.match(src, /from '\.\.\/\.\.\/lib\/ime'/, '应引入 isImeKey')
      assert.match(src, /isImeKey\(ev\)/, '处理函数里应调用 isImeKey')
      // 直接把 Enter / Esc 绑到「提交 / 关闭」的动作上（带 .prevent / .stop 修饰符，或直接调用提交函数）会绕过输入法判断。
      const bad = src.match(/@keydown\.(enter|esc|escape)(\.[a-z]+)*="(?!on[A-Z]\w*\(\$event)/g)
      assert.equal(bad, null, `这些绑定没有经过输入法判断：${bad?.join(' | ')}`)
    })
  }
})
