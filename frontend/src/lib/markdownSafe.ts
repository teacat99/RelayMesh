// Markdown 输出安全层（S0.2）。
//
// 威胁模型：会话摘要、用户回复、任务报告都来自 AI 客户端或远端调用方，属于**不可信输入**。
// marked 不做任何净化：原始 HTML 会原样透传，`[x](javascript:...)` 与图片属性里的引号也能注入脚本。
// 一旦执行，攻击者就以管理员身份操作整个 Web 控制台（包括终端），因此采用三层防线：
//   1) 渲染层：图片/代码块的属性一律转义，语言名只保留安全字符；
//   2) 净化层：DOMPurify 白名单（标签、属性、URL 协议、class 取值），`style`/`on*`/`javascript:` 一律剔除；
//   3) 浏览器层：服务端下发的 CSP（script-src 'self'）兜底，即使前两层被绕过也不能执行内联脚本。
//
// 本模块只依赖纯 TS，DOMPurify 实例由调用方注入，便于在 Node（jsdom）里做恶意载荷回归测试。

/** HTML 文本 / 属性值转义（包含单引号，可安全放进双引号或单引号属性） */
export function escapeHtml(input: unknown): string {
  return String(input ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/** 围栏代码块的语言名只允许安全字符，避免 "``` x" onmouseover=..." 之类的 class 属性注入 */
export function safeLanguage(lang: unknown): string {
  const text = String(lang ?? '').trim().split(/\s+/)[0] ?? ''
  return /^[A-Za-z0-9_+.#-]{1,32}$/.test(text) ? text : ''
}

// ---- 渲染器使用的静态 class（也是净化时 class 的白名单，唯一真源） ----

export const CODE_WRAPPER_CLASS =
  'code-block-wrapper my-3 rounded-xs border border-border/80 bg-card overflow-hidden shadow-2xs relative group'
export const CODE_COPY_BUTTON_CLASS =
  'copy-code-button absolute top-2 right-2 p-1.5 rounded-sm bg-muted/70 hover:bg-muted text-muted-foreground hover:text-foreground cursor-pointer transition-all border border-border/60 shadow-2xs opacity-75 hover:opacity-100 group-hover:opacity-100 z-10'
export const CODE_ICON_IDLE_CLASS = 'copy-icon-idle w-3.5 h-3.5'
export const CODE_ICON_SUCCESS_CLASS = 'copy-icon-success w-3.5 h-3.5 hidden text-green-600 dark:text-green-400'
export const CODE_PRE_CLASS = 'p-3 m-0 overflow-x-auto'
export const IMAGE_CLASS =
  'markdown-previewable-image rounded-sm border border-border my-2 max-h-96 object-contain cursor-pointer hover:opacity-95 transition-all shadow-xs'
export const CJK_CHAR_CLASS = 'cjk-char'

const TRUSTED_CLASS_VALUES: ReadonlySet<string> = new Set([
  CODE_WRAPPER_CLASS,
  CODE_COPY_BUTTON_CLASS,
  CODE_ICON_IDLE_CLASS,
  CODE_ICON_SUCCESS_CLASS,
  CODE_PRE_CLASS,
  IMAGE_CLASS,
  CJK_CHAR_CLASS,
])

const LANGUAGE_CLASS_RE = /^language-[A-Za-z0-9_+.#-]{1,32}$/

/** class 取值是否来自渲染器本身：整串相等才放行，攻击者无法借此叠加 Tailwind 工具类做界面覆盖 */
export function isTrustedClassValue(value: string): boolean {
  return TRUSTED_CLASS_VALUES.has(value) || LANGUAGE_CLASS_RE.test(value)
}

// ---- DOMPurify 白名单 ----

const ALLOWED_TAGS = [
  'a', 'abbr', 'b', 'blockquote', 'br', 'button', 'code', 'del', 'details', 'div', 'em',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'i', 'img', 'input', 'kbd', 'li', 'ol', 'p', 'pre',
  's', 'small', 'span', 'strong', 'sub', 'summary', 'sup',
  'table', 'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'u', 'ul',
  // 代码块复制按钮的图标
  'svg', 'rect', 'path',
]

const ALLOWED_ATTR = [
  'href', 'src', 'alt', 'title', 'class', 'type', 'disabled', 'checked', 'start', 'open',
  'colspan', 'rowspan', 'align',
  'viewBox', 'fill', 'stroke', 'stroke-width', 'stroke-linecap', 'stroke-linejoin',
  'x', 'y', 'width', 'height', 'rx', 'ry', 'd', 'xmlns',
]

const FORBID_TAGS = ['script', 'style', 'iframe', 'frame', 'frameset', 'object', 'embed', 'form', 'link', 'meta', 'base', 'template', 'math', 'foreignObject']
const FORBID_ATTR = ['style', 'srcset', 'formaction', 'action', 'name', 'id', 'xlink:href']

// 与 DOMPurify 默认值相比去掉了 ftp / callto / sms / cid / xmpp / matrix，只保留 web 场景真正需要的协议；
// 首字符不是字母的（相对路径、锚点、查询串、协议相对 URL）与无协议的相对地址仍然放行。
const ALLOWED_URI_REGEXP = /^(?:(?:https?|mailto|tel):|[^a-z]|[a-z+.\-]+(?:[^a-z+.\-:]|$))/i

/** 仅需要 DOMPurify 的这一小部分接口，便于测试时注入 jsdom 版本 */
export interface PurifyLike {
  sanitize(dirty: string, config?: Record<string, unknown>): string | { toString(): string }
  addHook(entryPoint: string, hook: (node: any, data: any, config: any) => void): void
}

const configured = new WeakSet<object>()

function installHooks(purify: PurifyLike): void {
  if (configured.has(purify)) return
  configured.add(purify)

  purify.addHook('uponSanitizeAttribute', (node: any, data: any) => {
    const tag = String(node?.nodeName ?? '').toLowerCase()
    const name = String(data?.attrName ?? '').toLowerCase()
    const value = String(data?.attrValue ?? '')

    if (name === 'class') {
      if (!isTrustedClassValue(value)) data.keepAttr = false
      return
    }
    if (name === 'type') {
      // 只有 GFM 任务列表的复选框与我们自己的按钮需要 type
      const ok = (tag === 'input' && value === 'checkbox') || (tag === 'button' && value === 'button')
      if (!ok) data.keepAttr = false
      return
    }
    if (name === 'width' || name === 'height' || name === 'x' || name === 'y' || name === 'rx' || name === 'ry') {
      // 尺寸类属性只给图标 SVG 用，禁止 <img width=99999> 之类的版面滥用
      if (tag !== 'svg' && tag !== 'rect') data.keepAttr = false
      return
    }
    if (name === 'disabled' || name === 'checked') {
      if (tag !== 'input') data.keepAttr = false
    }
  })

  purify.addHook('afterSanitizeAttributes', (node: any) => {
    const tag = String(node?.nodeName ?? '').toLowerCase()
    if (tag === 'a' && node.hasAttribute?.('href')) {
      node.setAttribute('rel', 'noopener noreferrer')
    }
    // input 只为 GFM 任务列表的只读复选框保留；其它类型（文本框等）可被用来伪造表单，直接移除
    if (tag === 'input' && node.getAttribute?.('type') !== 'checkbox') {
      node.parentNode?.removeChild(node)
    }
  })
}

/** 净化 marked 生成的 HTML。任何不在白名单内的标签 / 属性 / 协议都会被剔除。 */
export function sanitizeMarkdownHtml(
  purify: PurifyLike,
  html: string,
  opts?: { allowHeadingId?: boolean },
): string {
  installHooks(purify)
  const attrs = opts?.allowHeadingId ? [...ALLOWED_ATTR, 'id'] : ALLOWED_ATTR
  const forbidAttr = opts?.allowHeadingId ? FORBID_ATTR.filter(a => a !== 'id') : FORBID_ATTR
  const clean = purify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR: attrs,
    FORBID_TAGS,
    FORBID_ATTR: forbidAttr,
    ALLOWED_URI_REGEXP,
    ALLOW_DATA_ATTR: false,
    ALLOW_UNKNOWN_PROTOCOLS: false,
    SANITIZE_DOM: true,
    RETURN_TRUSTED_TYPE: false,
  })
  return String(clean)
}

// ---- 渲染结果缓存 ----
//
// DOMPurify 需要完整解析并遍历 DOM，长工作流（上百轮、上百 KB 的摘要）重复渲染时代价可观。
// 相同文本的渲染结果完全确定，用容量受限的 LRU 缓存，切换工作流 / 列表补丁后的重挂载不会重复计算。

export class RenderCache {
  private readonly map = new Map<string, string>()
  constructor(private readonly capacity = 400, private readonly maxKeyLength = 200_000) {}

  get(key: string): string | undefined {
    const hit = this.map.get(key)
    if (hit === undefined) return undefined
    // 触碰：移到队尾
    this.map.delete(key)
    this.map.set(key, hit)
    return hit
  }

  set(key: string, value: string): void {
    // 超大文本不缓存，避免缓存本身占用大量内存
    if (key.length > this.maxKeyLength) return
    if (this.map.has(key)) this.map.delete(key)
    this.map.set(key, value)
    while (this.map.size > this.capacity) {
      const oldest = this.map.keys().next().value
      if (oldest === undefined) break
      this.map.delete(oldest)
    }
  }

  get size(): number {
    return this.map.size
  }
}
