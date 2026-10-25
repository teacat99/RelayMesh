// 运行：pnpm --dir frontend run test:unit
// 开关组件的绑定防线：项目里的 Switch 基于 radix-vue 1.x，受控状态是 `checked`、事件是 `update:checked`
// （写成 `v-model:checked`、`:checked` + `@update:checked` 都行），**没有** modelValue。
// 起因（#232 D2 的 e2e 抓到）：收尾门面板的开关写成了 `:model-value` / `@update:model-value`——vue-tsc 不报错
// （多出来的属性只是被当成原生属性透传），页面上开关却永远显示为「关」、点了也不触发任何事件。
// 这里扫描所有 .vue，让这类写法在单测里就挂掉，不必等到浏览器里才发现。
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, it } from 'node:test'

function frontendDir(): string {
  for (const base of [process.cwd(), join(process.cwd(), 'frontend')]) {
    if (existsSync(join(base, 'index.html')) && existsSync(join(base, 'src'))) return base
  }
  throw new Error('找不到 frontend 目录，请在仓库根目录或 frontend 目录下运行测试')
}

function listVue(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) out.push(...listVue(full))
    else if (name.endsWith('.vue')) out.push(full)
  }
  return out
}

// 去掉模板注释，免得注释里提到这些写法时误报
const stripComments = (s: string) => s.replace(/<!--[\s\S]*?-->/g, '')

// 项目里的 <Switch ... /> 都是自闭合标签；属性值里会有箭头函数的 `>`，所以按 `/>` 截断而不是按 `>`
const SWITCH_TAG = /<Switch\b[\s\S]*?\/>/g
// modelValue 系列写法：:model-value / :modelValue / v-model（不带参数）/ @update:model-value / @update:modelValue
const WRONG_BINDING = /(?:^|\s)(?::model-?value|v-model(?![:\w.])|@update:model-?value)/i

describe('Switch 绑定', () => {
  const root = join(frontendDir(), 'src')
  const files = listVue(root).filter(f => !f.endsWith(join('components', 'ui', 'switch', 'Switch.vue')))

  it('所有 <Switch> 都用 checked / update:checked，不用 modelValue', () => {
    let seen = 0
    const bad: string[] = []
    for (const file of files) {
      const text = stripComments(readFileSync(file, 'utf8'))
      for (const m of text.matchAll(SWITCH_TAG)) {
        seen++
        if (WRONG_BINDING.test(m[0])) bad.push(`${relative(root, file)}: ${m[0].replace(/\s+/g, ' ').slice(0, 160)}`)
      }
    }
    assert.ok(seen >= 8, `应扫描到足够多的 <Switch>（现有十几处），实际只有 ${seen} 个，说明匹配规则失效了`)
    assert.deepEqual(bad, [], `radix-vue 1.x 的 Switch 没有 modelValue，请改成 :checked + @update:checked（或 v-model:checked）：\n${bad.join('\n')}`)
  })

  it('收尾门面板的开关按 checked 绑定并监听 update:checked', () => {
    const panel = readFileSync(join(root, 'components', 'feedback', 'WrapupGatePanel.vue'), 'utf8')
    const tag = panel.match(SWITCH_TAG)?.[0] ?? ''
    assert.ok(/data-testid="wrapup-gate-switch"/.test(tag), '应找到收尾门开关')
    assert.ok(/:checked="on"/.test(tag), '开关的状态要绑 :checked')
    assert.ok(/@update:checked="toggle"/.test(tag), '开关的事件要听 @update:checked')
  })

  it('匹配规则本身能认出错误写法（防止正则悄悄失效）', () => {
    const wrong = [
      '<Switch :model-value="on" @update:model-value="toggle" />',
      '<Switch\n  :modelValue="on"\n  @update:modelValue="(v: boolean) => toggle(v)"\n/>',
      '<Switch v-model="on" />',
    ]
    for (const w of wrong) {
      const tag = w.match(SWITCH_TAG)?.[0] ?? ''
      assert.ok(tag, `应能截出标签：${w}`)
      assert.ok(WRONG_BINDING.test(tag), `应判为错误写法：${w}`)
    }
    const right = [
      '<Switch :checked="on" @update:checked="(v: boolean) => toggle(v)" />',
      '<Switch v-model:checked="on" />',
    ]
    for (const r of right) {
      const tag = r.match(SWITCH_TAG)?.[0] ?? ''
      assert.ok(tag && !WRONG_BINDING.test(tag), `不应误报：${r}`)
    }
  })
})
