// Markdown → 安全 HTML 的完整渲染管线（marked 语法转换 + 自定义渲染器 + DOMPurify 白名单净化 + LRU 缓存）。
//
// 从 MarkdownRenderer.vue 抽出成纯 TS 模块，目的是让整条管线可以在 Node（jsdom）里做恶意载荷回归测试，
// 而不必依赖 Vue 组件与浏览器。组件只负责把结果交给 v-html 以及处理点击事件。
import { marked, type Token } from 'marked'
import {
  CJK_CHAR_CLASS,
  CODE_COPY_BUTTON_CLASS,
  CODE_ICON_IDLE_CLASS,
  CODE_ICON_SUCCESS_CLASS,
  CODE_PRE_CLASS,
  CODE_WRAPPER_CLASS,
  IMAGE_CLASS,
  RenderCache,
  escapeHtml,
  safeLanguage,
  sanitizeMarkdownHtml,
  type PurifyLike,
} from './markdownSafe'
import { slugHeadingAnchor } from './docHeadings'

// 严格处理代码块中 CJK 与 ASCII 字符：为 CJK 字符包裹严格 2ch 宽度的 span，确保像素级绝对对齐
export function formatMonospaceCode(codeText: string): string {
  const lines = codeText.split('\n')
  return lines.map(line => {
    let formattedLine = ''
    for (const char of line) {
      const code = char.codePointAt(0) || 0
      // 判断是否为 CJK 全宽字符范围 (汉字、全角标点、日韩字符等)
      const isCJK = (
        (code >= 0x4E00 && code <= 0x9FFF) ||   // CJK Unified Ideographs
        (code >= 0x3400 && code <= 0x4DBF) ||   // CJK Extension A
        (code >= 0x20000 && code <= 0x2A6DF) || // CJK Extension B
        (code >= 0x3000 && code <= 0x303F) ||   // CJK Symbols and Punctuation (如 【】、。)
        (code >= 0xFF01 && code <= 0xFF60) ||   // Fullwidth Forms (如 ：！等)
        (code >= 0xFE30 && code <= 0xFE4F)      // CJK Compatibility Forms
      )

      if (isCJK) {
        formattedLine += `<span class="${CJK_CHAR_CLASS}">${char}</span>`
      } else if (char === '<') {
        formattedLine += '&lt;'
      } else if (char === '>') {
        formattedLine += '&gt;'
      } else if (char === '&') {
        formattedLine += '&amp;'
      } else if (char === '"') {
        formattedLine += '&quot;'
      } else {
        formattedLine += char
      }
    }
    return formattedLine
  }).join('\n')
}

// Custom renderer for marked to process pre / code blocks with character grid
const renderer = new marked.Renderer()
renderer.code = function (token: any, lang?: string) {
  let codeText = ''
  let language = ''
  if (typeof token === 'string') {
    codeText = token
    language = lang || ''
  } else if (token && typeof token === 'object') {
    codeText = token.text || ''
    language = token.lang || lang || ''
  }
  const formatted = formatMonospaceCode(codeText)
  // 语言名来自围栏行的自由文本，只保留安全字符后才能进入 class 属性
  const safeLang = safeLanguage(language)
  const langClass = safeLang ? ` class="language-${safeLang}"` : ''
  return `<div class="${CODE_WRAPPER_CLASS}">
    <button
      type="button"
      class="${CODE_COPY_BUTTON_CLASS}"
      title="复制代码"
    >
      <svg class="${CODE_ICON_IDLE_CLASS}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <rect width="14" height="14" x="8" y="8" rx="2" ry="2"/>
        <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>
      </svg>
      <svg class="${CODE_ICON_SUCCESS_CLASS}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M20 6 9 17l-5-5"/>
      </svg>
    </button>
    <pre class="${CODE_PRE_CLASS}"><code${langClass}>${formatted}</code></pre>
  </div>`
}

// 处理 Markdown 中的图片渲染：支持点击大图预览
renderer.image = function (token: any) {
  let href = ''
  let title = ''
  let text = ''
  if (typeof token === 'string') {
    href = token
  } else if (token && typeof token === 'object') {
    href = token.href || ''
    title = token.title || ''
    text = token.text || ''
  }
  // 三个属性值都来自不可信文本：必须转义，否则 `![" onerror="...](x)` 之类的载荷会逃出属性
  return `<img src="${escapeHtml(href)}" alt="${escapeHtml(text)}" title="${escapeHtml(title)}" class="${IMAGE_CLASS}" />`
}

// Configure marked renderer for safe and rich HTML output
marked.setOptions({
  renderer,
  breaks: true,
  gfm: true,
})

// 渲染结果按原文缓存：DOMPurify 需要完整解析并遍历 DOM，长工作流反复挂载时不重复计算
const renderCache = new RenderCache()

/** 渲染 Markdown 并返回可安全放进 v-html 的 HTML */
export function renderMarkdown(purify: PurifyLike, content: string, opts?: { headingAnchors?: boolean }): string {
  if (!content) return ''
  const cacheKey = opts?.headingAnchors ? `\x01a\x01${content}` : content
  const cached = renderCache.get(cacheKey)
  if (cached !== undefined) return cached
  let safeHtml: string
  if (opts?.headingAnchors) {
    const docRenderer = new marked.Renderer()
    docRenderer.code = renderer.code.bind(renderer)
    docRenderer.image = renderer.image.bind(renderer)
    docRenderer.heading = function (this: InstanceType<typeof marked.Renderer>, token: { depth: number; tokens: Token[]; text?: string }) {
      const depth = token.depth
      const inner = this.parser.parseInline(token.tokens)
      const plain = (token.text ?? inner.replace(/<[^>]+>/g, '')).trim()
      const id = slugHeadingAnchor(plain)
      return `<h${depth} id="${escapeHtml(id)}">${inner}</h${depth}>\n`
    }
    safeHtml = sanitizeMarkdownHtml(purify, marked.parse(content, { renderer: docRenderer }) as string, {
      allowHeadingId: true,
    })
  } else {
    safeHtml = sanitizeMarkdownHtml(purify, marked.parse(content) as string)
  }
  renderCache.set(cacheKey, safeHtml)
  return safeHtml
}

/** 仅测试使用：清空缓存，避免不同净化器实例之间相互影响 */
export function resetMarkdownRenderCacheForTest(): void {
  ;(renderCache as unknown as { map: Map<string, string> }).map.clear()
}
