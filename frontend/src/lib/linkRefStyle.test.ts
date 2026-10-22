// 运行：pnpm --dir frontend run test:unit
// 守护「正文里的引用链接装饰不改变版面」（#222 / #833，Q72 选 a）。
//
// 背景：#N / Q<N> / [[路径]] / sess-xxxxxxxx 是正文渲染完、等任务 / 确认项 / 文档 / 会话数据就绪之后才装饰成 <a class="link-ref"> 的
// （MarkdownRenderer.decorateRefs，数据晚到就晚装饰）。如果链接的外观会改变宽度或行高（等宽字体、加粗、内边距、行内图标……），
// 每一次晚到的装饰都会让整段正文位移。生产实测：离底部最近的 60 张含引用的正文，装饰前后 50 张高度不同，合计 341.6 px，单张最多 50 px；
// 改成只涂色（颜色 / 点状下划线 / 底色 / box-shadow）之后同样 60 张逐张相等。
//
// 做法：用「白名单」钉住 .link-ref 规则里允许出现的声明——只有不影响布局的绘制属性；想加别的属性，测试会失败并提醒你先想清楚会不会再让正文位移。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { linkRefClass } from './linkRefs'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'linkRefStyle.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

/** 不影响布局的绘制属性（颜色、下划线、底色、阴影、描边）；display 另行检查必须是 inline。 */
const PAINT_ONLY = new Set([
  'display',
  'box-sizing',
  'cursor',
  'border-radius',
  'color',
  'text-decoration',
  'text-decoration-style',
  'text-underline-offset',
  'background-color',
  'box-shadow',
  'outline',
  'outline-offset',
])

/** 去掉注释，免得注释里的括号和冒号干扰解析。 */
const stripComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '')

interface Rule {
  selector: string
  decls: Array<[string, string]>
}

/** 抽出选择器以 prefix 开头的所有规则（这份 CSS 里这几条规则都没有嵌套花括号，按最近的 } 截断就够）。 */
function rulesStartingWith(css: string, prefix: string): Rule[] {
  const out: Rule[] = []
  const text = stripComments(css)
  const re = /([^{}]+)\{([^{}]*)\}/g
  for (let m = re.exec(text); m; m = re.exec(text)) {
    const selector = m[1]!.trim()
    if (!selector.startsWith(prefix)) continue
    const decls: Array<[string, string]> = []
    for (const part of m[2]!.split(';')) {
      const i = part.indexOf(':')
      if (i < 0) continue
      const prop = part.slice(0, i).trim().toLowerCase()
      if (prop) decls.push([prop, part.slice(i + 1).trim()])
    }
    out.push({ selector, decls })
  }
  return out
}

describe('引用链接外观：版面中性（装饰晚到不能让正文位移）', () => {
  const css = read('assets/globals.css')

  it('.link-ref 的规则只有三条：基础、悬停、键盘聚焦；都在 globals.css 里', () => {
    const rules = rulesStartingWith(css, '.link-ref')
    const selectors = rules.map(r => r.selector).sort()
    assert.deepEqual(selectors, ['.link-ref.link-ref', '.link-ref.link-ref:focus-visible', '.link-ref.link-ref:hover'])
  })

  it('每条规则里只出现不影响布局的绘制属性（白名单）', () => {
    for (const r of rulesStartingWith(css, '.link-ref')) {
      for (const [prop, value] of r.decls) {
        assert.ok(PAINT_ONLY.has(prop), `${r.selector} 里的 ${prop}: ${value} 可能改变宽度或行高——装饰晚到会让正文位移；只能用颜色 / 下划线 / 底色 / 阴影这类不占位的绘制属性`)
      }
    }
  })

  it('基础规则：行内（display:inline）、没有任何占位声明，也没有 padding / margin / font / 图标', () => {
    const base = rulesStartingWith(css, '.link-ref').find(r => r.selector === '.link-ref.link-ref')
    assert.ok(base, '找不到 .link-ref.link-ref 规则')
    const props = new Map(base!.decls)
    assert.equal(props.get('display'), 'inline', 'display 必须是 inline（行内块 / flex 会改变行盒）')
    for (const banned of ['padding', 'margin', 'font-family', 'font-size', 'font-weight', 'font', 'letter-spacing', 'line-height', 'width', 'height', 'border', 'vertical-align']) {
      assert.ok(!props.has(banned), `基础规则里不能有 ${banned}`)
    }
  })

  it('没有任何 .link-ref 的伪元素规则（::before / ::after 的图标或内容都会占位）', () => {
    assert.doesNotMatch(stripComments(css), /\.link-ref[^{}]*:{1,2}(before|after)/i)
  })

  it('正文里通用的链接样式 .markdown-body a 也只涂色（它同样作用在 a.link-ref 上）', () => {
    const vue = read('components/MarkdownRenderer.vue')
    const style = vue.slice(vue.indexOf('<style'))
    const rules = rulesStartingWith(style, '.markdown-body a')
    assert.ok(rules.length >= 1, '找不到 .markdown-body a 规则')
    const allowed = new Set(['color', 'text-decoration', 'text-underline-offset'])
    for (const r of rules) {
      for (const [prop, value] of r.decls) {
        assert.ok(allowed.has(prop), `${r.selector} 里的 ${prop}: ${value} 会同时作用在引用链接上，可能改变版面`)
      }
    }
  })

  it('类名仍是 link-ref + link-ref-<kind>（任务多带历史名 task-ref）：样式靠 .link-ref，不再按类型分图标', () => {
    assert.equal(linkRefClass('task'), 'link-ref link-ref-task task-ref')
    assert.equal(linkRefClass('confirm'), 'link-ref link-ref-confirm')
    assert.equal(linkRefClass('doc'), 'link-ref link-ref-doc')
    assert.equal(linkRefClass('session'), 'link-ref link-ref-session')
  })
})
