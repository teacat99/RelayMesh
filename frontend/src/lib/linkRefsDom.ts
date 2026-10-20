// 引用链接的 DOM 装饰（#161）：在已经净化过的 HTML 容器里，把文本里存在的 #N / Q<N> / [[路径]] / sess-xxxxxxxx 换成链接。
// 识别规则与深链地址见 lib/linkRefs.ts；这里只管「怎么安全地改 DOM」。
//
// 只替换文本节点、用 DOM API 构造元素，不碰既有标签与属性，所以不可能引入新的可执行内容（净化管线的结论不变）。
// 链接是真正的 <a href>：普通左键点击由调用方拦下来做站内动作（弹窗 / 滚动 / 站内跳转），
// ⌘ / Ctrl / Shift 点击与中键则走浏览器默认行为，在新标签页里打开同一个深链。
import { linkRefClass, linkRefHint, linkRefHref, splitLinkRefs, wholeLinkRef, type LinkRefIndex, type LinkRefSegment } from './linkRefs'

export interface DecorateLinkRefsOptions {
  /** 正文所属的工作流（深链地址里要带）。 */
  workflowId: string
  /** 正文所在文档的路径（文档页才有）：解析 [[./x]] / [[../x]] 相对写法。 */
  fromPath?: string
}

/** 不需要链接的元素：已经是链接 / 按钮的（包括我们自己生成的）、代码块、脚本样式、表单控件、图标。 */
const SKIP_TAGS = new Set(['A', 'BUTTON', 'PRE', 'SCRIPT', 'STYLE', 'TEXTAREA', 'SVG', 'IMG'])

/** 文本里出现这些才可能有引用；绝大多数文本节点在这一步就被筛掉，不用跑完整扫描。 */
const MAYBE_REF_RE = /#\d|Q\d|\[\[|sess-/i

/** 用片段造一个引用链接（装饰与测试共用）；片段必须带 ref。 */
export function makeLinkRefElement(doc: Document, seg: LinkRefSegment, workflowId: string): HTMLAnchorElement {
  const ref = seg.ref
  if (!ref) throw new Error('makeLinkRefElement：片段不是引用')
  const shown = seg.shown ?? seg.text
  const a = doc.createElement('a')
  a.className = linkRefClass(ref.kind)
  a.setAttribute('href', linkRefHref(ref, workflowId))
  a.setAttribute('data-ref-kind', ref.kind)
  a.setAttribute('data-ref-key', ref.key)
  if (ref.anchor) a.setAttribute('data-ref-anchor', ref.anchor)
  if (ref.kind === 'task') a.setAttribute('data-task-seq', ref.key)
  a.setAttribute('title', linkRefHint(ref, shown))
  a.textContent = shown
  return a
}

/**
 * 装饰一个容器，返回新增的链接数。幂等：已经在链接 / 按钮里的文字不再处理，重复调用只会补上新出现的；
 * 行内代码只在整段就是一个引用（`#36`、`Q23`、`[[design/x.md]]`）时才链接。
 */
export function decorateLinkRefs(root: HTMLElement, idx: LinkRefIndex, opts: DecorateLinkRefsOptions): number {
  const doc = root.ownerDocument
  const fromPath = opts.fromPath ?? ''
  const walker = doc.createTreeWalker(root, 4 /* NodeFilter.SHOW_TEXT */)
  const nodes: Text[] = []
  for (let n = walker.nextNode(); n; n = walker.nextNode()) nodes.push(n as Text)

  let added = 0
  for (const node of nodes) {
    const text = node.data
    if (!MAYBE_REF_RE.test(text)) continue

    let skip = false
    let inInlineCode = false
    for (let el = node.parentElement; el && el !== root; el = el.parentElement) {
      const tag = el.tagName.toUpperCase()
      if (tag === 'CODE') {
        inInlineCode = true
        continue
      }
      if (SKIP_TAGS.has(tag)) {
        skip = true
        break
      }
    }
    if (skip) continue

    if (inInlineCode) {
      // `#36` 这样整段就是一个引用的行内代码才链接；一般的代码片段保持原样
      const whole = wholeLinkRef(text, idx, fromPath)
      if (!whole) continue
      node.parentNode?.replaceChild(makeLinkRefElement(doc, whole, opts.workflowId), node)
      added++
      continue
    }

    const parts = splitLinkRefs(text, idx, fromPath)
    if (!parts.some(p => p.ref)) continue
    const frag = doc.createDocumentFragment()
    for (const p of parts) {
      if (p.ref) {
        frag.appendChild(makeLinkRefElement(doc, p, opts.workflowId))
        added++
      } else {
        frag.appendChild(doc.createTextNode(p.text))
      }
    }
    node.parentNode?.replaceChild(frag, node)
  }
  return added
}
