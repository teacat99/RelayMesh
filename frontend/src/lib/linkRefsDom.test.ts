// 引用链接的 DOM 装饰：只动文本节点、幂等、不引入可执行内容；链接是真正的 <a href>（⌘ / Ctrl 点击能在新标签页打开深链）。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { decorateLinkRefs, makeLinkRefElement } from './linkRefsDom'
import type { LinkRefIndex } from './linkRefs'

const WF = 'wf-1'

function index(spec: { tasks?: number[]; confirms?: number[]; docs?: string[]; sessions?: string[] } = {}): LinkRefIndex {
  return {
    task: seq => ((spec.tasks ?? []).includes(seq) ? { title: `任务 ${seq}` } : null),
    confirm: n => ((spec.confirms ?? []).includes(n) ? { key: `Q${n}`, title: `提问 ${n}` } : null),
    doc: path => ((spec.docs ?? []).includes(path) ? { key: path, title: `文档 ${path}` } : null),
    session: hash => ((spec.sessions ?? []).includes(hash) ? { key: `sess-${hash}` } : null),
  }
}

const ALL = index({ tasks: [36, 7, 1, 2, 3], confirms: [23, 5], docs: ['design/x.md', 'a.md'], sessions: ['9f02517c'] })

function dom(html: string): HTMLElement {
  const doc = new JSDOM(`<!doctype html><body><div id="root">${html}</div></body>`).window.document
  return doc.getElementById('root') as HTMLElement
}

function decorate(root: HTMLElement, idx: LinkRefIndex = ALL, fromPath?: string): number {
  return decorateLinkRefs(root, idx, { workflowId: WF, ...(fromPath ? { fromPath } : {}) })
}

test('decorateLinkRefs：把文本节点里的四种引用换成链接，文字内容不变', () => {
  const root = dom('<p>已完成 #36，答复 Q23，见 [[design/x.md]] 与 sess-9f02517c；另有 #99 / Q99。</p><ul><li>#7 做完了</li></ul>')
  const before = root.textContent
  const n = decorate(root)
  assert.equal(n, 5)
  // [[design/x.md]] 显示成去掉方括号的路径，其余文字原样
  assert.equal(root.textContent, before?.replace('[[design/x.md]]', 'design/x.md'))
  const links = Array.from(root.querySelectorAll('a.link-ref'))
  assert.deepEqual(
    links.map(a => [a.getAttribute('data-ref-kind'), a.getAttribute('data-ref-key'), a.textContent]),
    [
      ['task', '36', '#36'],
      ['confirm', 'Q23', 'Q23'],
      ['doc', 'design/x.md', 'design/x.md'],
      ['session', 'sess-9f02517c', 'sess-9f02517c'],
      ['task', '7', '#7'],
    ]
  )
})

test('decorateLinkRefs：链接是真正的 <a href>，地址是各自的深链；任务保留历史 class 与 data-task-seq', () => {
  const root = dom('<p>#36 Q23 [[design/x.md#8-14|设计 X]] sess-9f02517c</p>')
  decorate(root)
  const byKind = (k: string) => root.querySelector(`a[data-ref-kind="${k}"]`) as HTMLAnchorElement
  assert.equal(byKind('task').getAttribute('href'), '/work/wf-1?seq=36')
  assert.equal(byKind('confirm').getAttribute('href'), '/?wf=wf-1&q=Q23')
  assert.equal(byKind('doc').getAttribute('href'), '/docs/wf-1/design/x.md#8-14')
  assert.equal(byKind('session').getAttribute('href'), '/?wf=wf-1&s=sess-9f02517c')
  assert.equal(byKind('doc').textContent, '设计 X')
  assert.equal(byKind('doc').getAttribute('data-ref-anchor'), '8-14')
  assert.equal(byKind('task').className, 'link-ref link-ref-task task-ref')
  assert.equal(byKind('task').getAttribute('data-task-seq'), '36')
  assert.equal(byKind('confirm').className, 'link-ref link-ref-confirm')
  assert.equal(byKind('confirm').hasAttribute('data-task-seq'), false)
  assert.equal(byKind('task').getAttribute('title'), '查看任务 #36 · 任务 36')
  assert.equal(byKind('confirm').getAttribute('title'), '查看确认项 Q23 · 提问 23')
  assert.equal(byKind('doc').getAttribute('title'), '打开文档 design/x.md#8-14 · 文档 design/x.md')
  assert.equal(byKind('session').getAttribute('title'), '跳到这一轮 sess-9f02517c')
})

test('decorateLinkRefs：幂等，重复调用不会套娃，只补上新出现的', () => {
  const root = dom('<p>先 #1，后 #2，再 Q5</p>')
  assert.equal(decorate(root, index({ tasks: [1] })), 1)
  assert.equal(decorate(root, index({ tasks: [1] })), 0)
  assert.equal(decorate(root, index({ tasks: [1, 2], confirms: [5] })), 2)
  assert.equal(root.querySelectorAll('a a').length, 0)
  assert.equal(root.querySelectorAll('a.link-ref').length, 3)
})

test('decorateLinkRefs：代码块、已有链接、按钮里的引用保持原样；整段就是引用的行内代码才链接', () => {
  const root = dom(
    [
      '<pre><code>git commit -m "fix #36 Q23"</code></pre>',
      '<p>命令 <code>make #36</code> 不动</p>',
      '<p>引用 <code>#36</code> 与 <code>Q23</code> 与 <code>[[design/x.md]]</code> 要链接</p>',
      '<p><a href="https://x.test/#36">外部 #36 Q23</a></p>',
      '<button type="button">复制 #36</button>',
    ].join('')
  )
  const n = decorate(root)
  assert.equal(n, 3)
  assert.equal(root.querySelector('pre')?.querySelector('a, button'), null)
  assert.equal(root.querySelectorAll('code')[1].textContent, 'make #36')
  const inline = root.querySelectorAll('p')[1]
  assert.deepEqual(
    Array.from(inline.querySelectorAll('code a.link-ref')).map(a => a.getAttribute('data-ref-kind')),
    ['task', 'confirm', 'doc']
  )
  assert.equal(root.querySelector('a[href^="https"]')?.textContent, '外部 #36 Q23')
  assert.equal(root.querySelector('button')?.textContent, '复制 #36')
  assert.equal(root.querySelectorAll('a.link-ref').length, 3)
})

test('decorateLinkRefs：文本里长得像标签的内容仍然只是文本，生成的元素只带白名单属性', () => {
  const root = dom('<p>&lt;img src=x onerror=alert(1)&gt; 与 &lt;script&gt;#3&lt;/script&gt; #3 [[design/x.md]]</p>')
  const before = root.textContent
  decorate(root)
  assert.equal(root.textContent, before?.replace('[[design/x.md]]', 'design/x.md'))
  assert.equal(root.querySelectorAll('img, script').length, 0)
  for (const el of Array.from(root.querySelectorAll('*'))) {
    assert.ok(['P', 'A'].includes(el.tagName), `不应出现 <${el.tagName.toLowerCase()}>`)
  }
  const allowed = new Set(['class', 'href', 'title', 'data-ref-kind', 'data-ref-key', 'data-ref-anchor', 'data-task-seq'])
  for (const a of Array.from(root.querySelectorAll('a'))) {
    for (const attr of Array.from(a.attributes)) assert.ok(allowed.has(attr.name), `不应出现属性 ${attr.name}`)
  }
})

test('decorateLinkRefs：目标都不存在时不动 DOM；没有候选字符的文本节点直接跳过', () => {
  const root = dom('<p>参见 #12 Q9 [[nope.md]] sess-0000aaaa，普通文字没有引用</p>')
  const html = root.innerHTML
  assert.equal(decorate(root, index()), 0)
  assert.equal(root.innerHTML, html)
})

test('decorateLinkRefs：[[./相对写法]] 按所在文档的目录解析', () => {
  const root = dom('<p>[[./x]] 与 [[../a.md]]</p>')
  const idx = index({ docs: ['design/x.md', 'a.md'] })
  assert.equal(decorate(root, idx, 'design/b.md'), 2)
  assert.deepEqual(
    Array.from(root.querySelectorAll('a')).map(a => a.getAttribute('data-ref-key')),
    ['design/x.md', 'a.md']
  )
  const rootNoBase = dom('<p>[[./x]]</p>')
  assert.equal(decorate(rootNoBase, idx), 0, '没有所在文档时 ./x 按根目录解析，根目录里没有 x.md')
})

test('makeLinkRefElement：非引用片段直接报错（调用方不该传）', () => {
  const doc = new JSDOM('<!doctype html><body></body>').window.document
  assert.throws(() => makeLinkRefElement(doc, { text: '普通文字' }, WF))
})
