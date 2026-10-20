// 决策卡静态渲染 XSS 回归：与 DocDecisionCard.vue 一致，文本经 textContent（等同 Vue 插值转义）。
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { splitDocDecisions, type DecisionCard } from './docDecisions'
import { escapeHtml } from './markdownSafe'

const FORBIDDEN_TAGS = new Set([
  'script', 'style', 'iframe', 'frame', 'object', 'embed', 'form', 'link', 'meta', 'base',
  'img', 'svg', 'math',
])

function assertDecisionDOMSafe(container: HTMLElement, label: string): void {
  for (const el of Array.from(container.querySelectorAll('*'))) {
    const tag = el.tagName.toLowerCase()
    assert.ok(!FORBIDDEN_TAGS.has(tag), `${label}: 出现了标签 <${tag}>`)
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      assert.ok(!name.startsWith('on'), `${label}: 事件属性 ${attr.name}`)
      assert.notEqual(name, 'src', `${label}: src 属性`)
      assert.notEqual(name, 'href', `${label}: href 属性`)
    }
  }
  assert.equal(container.querySelector('img'), null, `${label}: 不应出现 img 元素`)
  assert.equal(container.querySelector('script'), null, `${label}: 不应出现 script 元素`)
  assert.equal(container.querySelector('a'), null, `${label}: 不应出现 a 元素（无链接渲染）`)
}

/** 模拟 DocDecisionCard：所有用户字段只写入 textContent，不用 innerHTML。 */
function mountDecisionCardDOM(doc: Document, card: DecisionCard, errors: string[]): HTMLElement {
  const root = doc.createElement('div')
  root.setAttribute('data-testid', 'doc-decision-card')

  const h4 = doc.createElement('h4')
  h4.textContent = card.title
  root.appendChild(h4)

  if (card.body) {
    const p = doc.createElement('p')
    p.textContent = card.body
    root.appendChild(p)
  }

  const ul = doc.createElement('ul')
  for (const opt of card.options) {
    const li = doc.createElement('li')
    const label = doc.createElement('span')
    label.textContent = opt.label
    li.appendChild(label)
    if (opt.detail) {
      const detail = doc.createElement('p')
      detail.textContent = opt.detail
      li.appendChild(detail)
    }
    ul.appendChild(li)
  }
  root.appendChild(ul)

  if (card.risk) {
    const risk = doc.createElement('p')
    risk.textContent = `风险：${card.risk}`
    root.appendChild(risk)
  }

  if (errors.length) {
    const errUl = doc.createElement('ul')
    for (const e of errors) {
      const li = doc.createElement('li')
      li.textContent = e
      errUl.appendChild(li)
    }
    root.appendChild(errUl)
  }

  return root
}

const xssPayloads = [
  '<img src=x onerror=alert(1)>',
  '<script>alert(1)</script>',
  '<a href="javascript:alert(1)">x</a>',
  '<svg onload=alert(1)>',
  '"><iframe src=evil>',
  'A'.repeat(8000),
  'аpple', // Cyrillic homoglyph prefix
]

function decisionFence(fields: Record<string, unknown>): string {
  const card = {
    id: 'x1',
    title: String(fields.title ?? 'Q'),
    body: fields.body,
    options: fields.options ?? [
      { id: 'a', label: 'A' },
      { id: 'b', label: 'B' },
    ],
    default: 'a',
    risk: fields.risk,
  }
  return `# Doc\n\n\`\`\`decision\n${JSON.stringify(card)}\n\`\`\`\n`
}

describe('doc decision card XSS', () => {
  const dom = new JSDOM('<!doctype html><body></body>')
  const doc = dom.window.document

  for (const payload of xssPayloads) {
    it(`title/body/label/detail 载荷安全: ${payload.slice(0, 40)}`, () => {
      const body = decisionFence({
        title: payload,
        body: payload,
        options: [
          { id: 'a', label: payload, detail: payload },
          { id: 'b', label: 'safe' },
        ],
        risk: payload,
      })
      const segs = splitDocDecisions(body)
      const dec = segs.find(s => s.kind === 'decision')
      assert.ok(dec && dec.kind === 'decision')
      const node = mountDecisionCardDOM(doc, dec.card, dec.errors)
      doc.body.appendChild(node)
      assertDecisionDOMSafe(node, 'decision-card')
      doc.body.removeChild(node)

      if (payload.includes('<')) {
        assert.ok(escapeHtml(payload).includes('&lt;'), 'escapeHtml 应转义尖括号')
      }
    })
  }

  it('超长与 Unicode 选项 id 不生成可执行节点', () => {
    const long = 'x'.repeat(12000)
    const body = decisionFence({
      title: 't',
      options: [
        { id: long, label: long, detail: long },
        { id: 'b', label: 'B' },
      ],
    })
    const dec = splitDocDecisions(body).find(s => s.kind === 'decision')
    assert.ok(dec && dec.kind === 'decision')
    const node = mountDecisionCardDOM(doc, dec.card, dec.errors)
    assertDecisionDOMSafe(node, 'long-unicode')
  })
})
