// Markdown 渲染管线的恶意载荷回归（S0.2）。
// 使用 jsdom 提供 DOM 环境，对真实管线（marked + 自定义渲染器 + DOMPurify 白名单）逐条验证。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import createDOMPurify from 'dompurify'
import { renderMarkdown, resetMarkdownRenderCacheForTest } from './markdownRender'
import {
  IMAGE_CLASS,
  RenderCache,
  escapeHtml,
  isTrustedClassValue,
  safeLanguage,
  type PurifyLike,
} from './markdownSafe'

const purifyWindow = new JSDOM('').window
const purify = createDOMPurify(purifyWindow as unknown as Window & typeof globalThis) as unknown as PurifyLike

function render(markdown: string): string {
  resetMarkdownRenderCacheForTest()
  return renderMarkdown(purify, markdown)
}

function parse(html: string): Document {
  return new JSDOM(`<!doctype html><body>${html}</body>`).window.document
}

const FORBIDDEN_TAGS = new Set([
  'script', 'style', 'iframe', 'frame', 'frameset', 'object', 'embed', 'form', 'link', 'meta', 'base',
  'template', 'math', 'foreignobject', 'use', 'animate', 'set', 'textarea', 'select', 'option',
])

/** 通用安全断言：输出里不能出现可执行内容，也不能出现渲染器之外的 class */
function assertSafe(html: string, label: string): void {
  const doc = parse(html)
  for (const el of Array.from(doc.body.querySelectorAll('*'))) {
    const tag = el.tagName.toLowerCase()
    assert.ok(!FORBIDDEN_TAGS.has(tag), `${label}: 出现了被禁止的标签 <${tag}>`)
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      assert.ok(!name.startsWith('on'), `${label}: 出现了事件属性 ${attr.name}`)
      assert.notEqual(name, 'style', `${label}: 出现了 style 属性`)
      assert.notEqual(name, 'srcdoc', `${label}: 出现了 srcdoc 属性`)
      assert.notEqual(name, 'id', `${label}: 出现了 id 属性（DOM clobbering）`)
      assert.notEqual(name, 'name', `${label}: 出现了 name 属性（DOM clobbering）`)
      if (name === 'href' || name === 'src' || name === 'xlink:href') {
        const normalized = attr.value.replace(/[\u0000-\u0020\u007f-\u009f]+/g, '').toLowerCase()
        assert.ok(!normalized.startsWith('javascript:'), `${label}: ${name} 含 javascript: 协议`)
        assert.ok(!normalized.startsWith('vbscript:'), `${label}: ${name} 含 vbscript: 协议`)
        assert.ok(!normalized.startsWith('data:text/html'), `${label}: ${name} 含 data:text/html`)
      }
      if (name === 'class') {
        for (const cls of [attr.value]) {
          assert.ok(isTrustedClassValue(cls), `${label}: 出现了非渲染器生成的 class="${cls}"`)
        }
      }
    }
  }
}

const MALICIOUS_PAYLOADS: Array<[string, string]> = [
  ['script 标签', '<script>alert(1)</script>正文'],
  ['img onerror', '<img src=x onerror=alert(1)>'],
  ['img onerror 引号', '<img src="x" onerror="alert(document.cookie)">'],
  ['markdown javascript 链接', '[点我](javascript:alert(1))'],
  ['javascript 大小写混淆', '[点我](JaVaScRiPt:alert(1))'],
  ['javascript 实体编码', '[点我](&#106;avascript:alert(1))'],
  ['javascript 前置空白', '[点我]( javascript:alert(1))'],
  ['javascript 制表符', '<a href="java\tscript:alert(1)">x</a>'],
  ['html a javascript', '<a href="javascript:alert(1)">x</a>'],
  ['data html 链接', '<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>'],
  ['图片 alt 注入', '![" onerror="alert(1)](x.png)'],
  ['图片 title 注入', '![a](x.png "\\" onerror=\\"alert(1)")'],
  ['图片 href 注入', '![a](x.png" onerror="alert(1))'],
  ['围栏语言注入', '```js" onmouseover="alert(1)\nconsole.log(1)\n```'],
  ['围栏语言 class 注入', '```js fixed inset-0\nx\n```'],
  ['svg onload', '<svg onload=alert(1)></svg>'],
  ['svg script', '<svg><script>alert(1)</script></svg>'],
  ['svg use', '<svg><use href="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="/></svg>'],
  ['svg animate', '<svg><animate onbegin=alert(1) attributeName=x dur=1s></svg>'],
  ['iframe javascript', '<iframe src="javascript:alert(1)"></iframe>'],
  ['iframe srcdoc', '<iframe srcdoc="<script>alert(1)</script>"></iframe>'],
  ['object', '<object data="javascript:alert(1)"></object>'],
  ['embed', '<embed src="javascript:alert(1)">'],
  ['form action', '<form action="javascript:alert(1)"><button formaction="javascript:alert(1)">x</button></form>'],
  ['style 标签', '<style>@import url(//evil.example/x.css);</style>'],
  ['style 属性', '<p style="position:fixed;inset:0;background:#fff">覆盖</p>'],
  ['class 覆盖界面', '<div class="fixed inset-0 z-50 bg-white">假登录框</div>'],
  ['meta refresh', '<meta http-equiv="refresh" content="0;url=javascript:alert(1)">'],
  ['base 劫持', '<base href="//evil.example/">'],
  ['mXSS math', '<math><mtext><table><mglyph><style><!--</style><img title="--&gt;&lt;/mglyph&gt;&lt;img src=1 onerror=alert(1)&gt;">'],
  ['mXSS noscript', '<noscript><p title="</noscript><img src=x onerror=alert(1)>">'],
  ['id 劫持', '<a id="__proto__" name="cookie">x</a><form name="body"></form>'],
  ['input 自动聚焦', '<input type="text" onfocus=alert(1) autofocus>'],
  ['textarea', '<textarea autofocus onfocus=alert(1)></textarea>'],
  ['button formaction', '<button formaction="javascript:alert(1)">x</button>'],
  ['video poster', '<video poster=javascript:alert(1) onerror=alert(1)><source onerror=alert(1)></video>'],
  ['a target 与 rel', '<a href="https://evil.example" target="_blank">x</a>'],
  ['嵌套引号', '<img src="x"onerror="alert(1)">'],
  ['markdown 链接 title 注入', '[a](https://x.example "\\" onmouseover=\\"alert(1)")'],
]

for (const [label, payload] of MALICIOUS_PAYLOADS) {
  test(`恶意载荷被净化：${label}`, () => {
    assertSafe(render(payload), label)
  })
}

test('class 取值必须整串来自渲染器：Tailwind 工具类被剥离', () => {
  const html = render('<div class="fixed inset-0 z-50">x</div>')
  assert.ok(!html.includes('fixed'), html)
  assert.ok(!html.includes('inset-0'), html)
})

test('javascript: 链接被移除 href，文字保留', () => {
  const doc = parse(render('[点我](javascript:alert(1))'))
  const a = doc.querySelector('a')
  assert.ok(!a || !a.getAttribute('href'), '不应保留 javascript: href')
  assert.ok(doc.body.textContent?.includes('点我'))
})

test('图片属性被转义且不能逃出属性', () => {
  const doc = parse(render('![" onerror="alert(1)](x.png "t\\" onmouseover=\\"alert(2)")'))
  const img = doc.querySelector('img')
  assert.ok(img, '应保留 img')
  for (const attr of Array.from(img!.attributes)) {
    assert.ok(!attr.name.startsWith('on'), `不应出现 ${attr.name}`)
  }
})

test('围栏代码块语言名只保留安全字符', () => {
  assert.equal(safeLanguage('ts'), 'ts')
  assert.equal(safeLanguage('c++'), 'c++')
  assert.equal(safeLanguage('js" onmouseover="x'), '')
  assert.equal(safeLanguage('js fixed'), 'js')
  assert.equal(safeLanguage('a'.repeat(33)), '')
  assert.equal(safeLanguage(undefined), '')
})

test('合法 Markdown 不受影响：标题 / 列表 / 表格 / 强调 / 行内代码', () => {
  const html = render('# 标题\n\n- 项一\n- 项二\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n**粗** *斜* `code`')
  const doc = parse(html)
  assert.ok(doc.querySelector('h1'))
  assert.equal(doc.querySelectorAll('li').length, 2)
  assert.ok(doc.querySelector('table th'))
  assert.ok(doc.querySelector('strong'))
  assert.ok(doc.querySelector('em'))
  assert.ok(doc.querySelector('code'))
})

test('代码块保留复制按钮、语言 class 与 CJK 定宽 span', () => {
  const doc = parse(render('```ts\nconst 名字 = "值"\n```'))
  assert.ok(doc.querySelector('.code-block-wrapper'), '应有代码块容器')
  assert.ok(doc.querySelector('button.copy-code-button[type="button"]'), '应保留复制按钮')
  assert.ok(doc.querySelector('.copy-icon-idle'), '应保留复制图标')
  assert.ok(doc.querySelector('.copy-icon-success'), '应保留成功图标')
  assert.ok(doc.querySelector('code.language-ts'), '应保留语言 class')
  assert.ok(doc.querySelectorAll('.cjk-char').length >= 3, 'CJK 字符应被包裹')
})

test('正常 https 链接与图片得以保留', () => {
  const doc = parse(render('[文档](https://example.com/a?b=1) ![图](https://example.com/x.png "说明")'))
  const a = doc.querySelector('a')
  assert.equal(a?.getAttribute('href'), 'https://example.com/a?b=1')
  assert.equal(a?.getAttribute('rel'), 'noopener noreferrer')
  const img = doc.querySelector('img')
  assert.equal(img?.getAttribute('src'), 'https://example.com/x.png')
  assert.equal(img?.getAttribute('title'), '说明')
  assert.equal(img?.getAttribute('class'), IMAGE_CLASS)
})

test('data:image 内嵌图片与相对路径图片可用', () => {
  const png = 'data:image/png;base64,iVBORw0KGgo='
  const doc = parse(render(`![内嵌](${png}) ![相对](/api/v1/sessions/s1/images/0?hash=abc)`))
  const srcs = Array.from(doc.querySelectorAll('img')).map(i => i.getAttribute('src'))
  assert.deepEqual(srcs, [png, '/api/v1/sessions/s1/images/0?hash=abc'])
})

test('GFM 任务列表复选框保留，其它类型的 input 被移除', () => {
  const doc = parse(render('- [x] 完成\n- [ ] 待办\n\n<input type="text" value="x">'))
  const boxes = Array.from(doc.querySelectorAll('input'))
  assert.equal(boxes.length, 2)
  for (const box of boxes) assert.equal(box.getAttribute('type'), 'checkbox')
})

test('无害的内联 HTML 得以保留：details / summary / kbd / br', () => {
  const doc = parse(render('<details><summary>展开</summary>内容</details> 按 <kbd>Ctrl</kbd>+<kbd>C</kbd><br>换行'))
  assert.ok(doc.querySelector('details > summary'))
  assert.equal(doc.querySelectorAll('kbd').length, 2)
  assert.ok(doc.querySelector('br'))
})

test('空内容与缓存命中', () => {
  assert.equal(renderMarkdown(purify, ''), '')
  resetMarkdownRenderCacheForTest()
  const first = renderMarkdown(purify, '**a**')
  const second = renderMarkdown(purify, '**a**')
  assert.equal(first, second)
})

test('escapeHtml 覆盖五类特殊字符', () => {
  assert.equal(escapeHtml(`<a href="x" title='y'>&</a>`), '&lt;a href=&quot;x&quot; title=&#39;y&#39;&gt;&amp;&lt;/a&gt;')
  assert.equal(escapeHtml(undefined), '')
  assert.equal(escapeHtml(null), '')
})

test('文档页标题锚点（headingAnchors）', () => {
  resetMarkdownRenderCacheForTest()
  const html = renderMarkdown(purify, '# 端到端文档\n\n## 第一节\n', { headingAnchors: true })
  assert.match(html, /id="端到端文档"/)
  assert.match(html, /id="第一节"/)
})

test('RenderCache 按 LRU 淘汰，超大文本不入缓存', () => {
  const cache = new RenderCache(2, 10)
  cache.set('a', '1')
  cache.set('b', '2')
  assert.equal(cache.get('a'), '1') // a 变为最近使用
  cache.set('c', '3') // 淘汰 b
  assert.equal(cache.get('b'), undefined)
  assert.equal(cache.get('a'), '1')
  assert.equal(cache.get('c'), '3')
  cache.set('x'.repeat(11), 'big')
  assert.equal(cache.get('x'.repeat(11)), undefined)
  assert.equal(cache.size, 2)
})
