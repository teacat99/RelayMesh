// 正文里的「引用」统一识别（#161，Q25 选 a）：把四种引用写法认成同一种可点的链接——
//
//   任务 #36（已有，lib/taskRefs.ts）· 确认项 Q23 · 文档 [[design/x.md]] / [[design/x.md#锚点]] / [[路径|显示名]] · 会话号 sess-9f02517c
//
// 判定刻意保守——误链接比漏链接更糟：每一种都要「目标确实存在」才变链接，由调用方传入 LinkRefIndex 回答；
// 索引没就绪（数据没加载完、不是当前工作流）时一律不链接，保持原样。
//   - 任务：规则全在 taskRefs.ts（只认不带前导 0 的 #N，前面不是词字符 / 路径，「追加 #5」不是任务）；
//   - 确认项：大写 Q + 不带前导 0 的 1–6 位整数，前面不是字母 / 数字 / 下划线 / 斜杠 / & / # / 点，后面不是词字符、也不是「.数字」；
//   - 文档：[[路径]]，规则与服务端 internal/docs/links.go 的 ExtractLinks 一致——先去掉 #锚点 / ?查询 与 |显示名，
//     带协议、绝对路径、非 .md 扩展名（图片 / PDF）、含「..」的都不是文档，没有 .md 后缀就补上；[[artifact:N]] 另有人处理，这里跳过；
//   - 会话号：sess- 加 6–12 位十六进制（卡片脚注里的短哈希），前后不能再贴着字母 / 数字 / 下划线 / 连字符。
//
// 纯逻辑，不依赖 Vue 与 DOM（DOM 装饰在 lib/linkRefsDom.ts，接线在 composables/useLinkRefs.ts）。
import { TASK_REF_CLASS, scanTaskRefs } from './taskRefs'

export type LinkRefKind = 'task' | 'confirm' | 'doc' | 'session'

/** 所有引用链接共用的 class；各类型再加 link-ref-<kind>（任务还带历史名 task-ref）。 */
export const LINK_REF_CLASS = 'link-ref'

/** 一个引用链接元素的 class 串：`link-ref link-ref-<kind>`，任务再加历史名 `task-ref`（样式与老 e2e 选择器都认它）。 */
export function linkRefClass(kind: LinkRefKind): string {
  return kind === 'task' ? `${LINK_REF_CLASS} ${LINK_REF_CLASS}-task ${TASK_REF_CLASS}` : `${LINK_REF_CLASS} ${LINK_REF_CLASS}-${kind}`
}

/** 一个已确认存在的引用目标。 */
export interface LinkRef {
  kind: LinkRefKind
  /** task：编号（字符串）；confirm：「Q23」；doc：规范化后的路径（.md）；session：完整会话编号。 */
  key: string
  /** doc 才有：标题锚点（不带 #）。 */
  anchor?: string
  /** 悬停里补充写的目标标题（任务 / 提问 / 文档 / 会话的标题）；没有就不写。 */
  title?: string
}

export interface LinkRefSegment {
  /** 原文。 */
  text: string
  /** 页面上显示的文字（缺省 = text）：文档的 [[路径|显示名]] 显示「显示名」，[[路径]] 去掉方括号。 */
  shown?: string
  /** 有值表示这一段是引用；没有就是普通文字。 */
  ref?: LinkRef
}

export interface LinkTarget {
  key: string
  title?: string
}

/** 「哪些目标存在」：每个函数返回目标（规范化后的键 + 标题），不存在返回 null。 */
/**
 * 一批确认项按序号建的索引，连同其中最大的序号（正文里的 Q<N> 引用靠它解析；最大序号进「存在集合变了没有」的签名）。
 * 序号相同的后面的覆盖前面的；最大序号至少为 0。性能原因与会话短哈希索引相同（#186 C2a）：只在 store 里建一份共用。
 */
export function buildSeqIndex<T extends { seq: number }>(items: readonly T[]): { bySeq: Map<number, T>; maxSeq: number } {
  const bySeq = new Map<number, T>()
  let maxSeq = 0
  for (const item of items) {
    bySeq.set(item.seq, item)
    if (item.seq > maxSeq) maxSeq = item.seq
  }
  return { bySeq, maxSeq }
}

export interface LinkRefIndex {
  task(seq: number): { title?: string } | null
  /** n = 确认项编号（Q23 的 23）。 */
  confirm(n: number): LinkTarget | null
  /** path = 规范化后的文档路径（.md）。 */
  doc(path: string): LinkTarget | null
  /** hash = 小写的会话短哈希（sess- 之后的部分）。 */
  session(hash: string): LinkTarget | null
}

interface Hit {
  at: number
  length: number
  shown: string
  ref: LinkRef
}

// ---------------------------------------------------------------------------
// 确认项 Q23
// ---------------------------------------------------------------------------

const CONFIRM_RE = /Q([1-9]\d{0,5})(?!\w|\.\d)/g
/** 紧挨在 Q 前面就说明这不是确认项编号的字符。 */
const CONFIRM_BAD_BEFORE_RE = /[A-Za-z0-9_/&#.]/

function scanConfirmRefs(text: string, idx: LinkRefIndex): Hit[] {
  if (!text.includes('Q')) return []
  const out: Hit[] = []
  for (const m of text.matchAll(CONFIRM_RE)) {
    const at = m.index ?? 0
    if (at > 0 && CONFIRM_BAD_BEFORE_RE.test(text.charAt(at - 1))) continue
    const hit = idx.confirm(Number(m[1]))
    if (!hit) continue
    out.push({ at, length: m[0].length, shown: m[0], ref: { kind: 'confirm', key: hit.key, ...(hit.title ? { title: hit.title } : {}) } })
  }
  return out
}

// ---------------------------------------------------------------------------
// 会话号 sess-xxxxxxxx
// ---------------------------------------------------------------------------

const SESSION_RE = /sess-([0-9a-f]{6,12})(?![0-9A-Za-z_-])/gi
const SESSION_BAD_BEFORE_RE = /[A-Za-z0-9_-]/

function scanSessionRefs(text: string, idx: LinkRefIndex): Hit[] {
  if (!/sess-/i.test(text)) return []
  const out: Hit[] = []
  for (const m of text.matchAll(SESSION_RE)) {
    const at = m.index ?? 0
    if (at > 0 && SESSION_BAD_BEFORE_RE.test(text.charAt(at - 1))) continue
    const hit = idx.session(m[1].toLowerCase())
    if (!hit) continue
    out.push({ at, length: m[0].length, shown: m[0], ref: { kind: 'session', key: hit.key, ...(hit.title ? { title: hit.title } : {}) } })
  }
  return out
}

// ---------------------------------------------------------------------------
// 文档 [[路径]]
// ---------------------------------------------------------------------------

const WIKI_RE = /\[\[([^[\]\n]+?)\]\]/g
const SCHEME_RE = /^[A-Za-z][A-Za-z0-9+.-]*:/

/** path.Clean 的效果：丢掉空段与「.」段，「..」抵消前一段（退出根目录之外的「..」留着，由调用方拒绝）。 */
function cleanSegments(parts: string[]): string {
  const out: string[] = []
  for (const seg of parts) {
    if (seg === '' || seg === '.') continue
    if (seg === '..' && out.length > 0 && out[out.length - 1] !== '..') {
      out.pop()
      continue
    }
    out.push(seg)
  }
  return out.join('/')
}

export interface ParsedDocRef {
  /** 规范化后的路径（带 .md）。 */
  path: string
  /** 标题锚点（不带 #），没有是空串。 */
  anchor: string
  /** [[路径|显示名]] 里的显示名，没有是空串。 */
  label: string
  /** 方括号里写的路径原文（含锚点，不含「|显示名」），去掉首尾空白。 */
  written: string
}

/**
 * 解析 [[ ]] 里面的内容。规则与服务端 ExtractLinks / NormalizePath 一致；不是文档链接（Artifact 引用、外链、
 * 绝对路径、非 .md 扩展名、含「..」、空路径）返回 null。
 * fromPath 是正文所在文档的路径（文档页里渲染时才有）：只有 ./ 和 ../ 开头的相对写法才按它所在目录解析，
 * 其余仍按工作流根目录——与服务端一致；会话正文没有「当前文档」，传空串即可。
 */
export function parseDocRef(inner: string, fromPath = ''): ParsedDocRef | null {
  let raw = inner
  let label = ''
  const bar = raw.indexOf('|')
  if (bar >= 0) {
    label = raw.slice(bar + 1).trim()
    raw = raw.slice(0, bar)
  }
  const written = raw.trim()
  if (!written || /^artifact:/i.test(written)) return null

  let target = written
  let anchor = ''
  const hash = target.indexOf('#')
  if (hash >= 0) {
    anchor = target.slice(hash + 1).trim()
    target = target.slice(0, hash)
  }
  const q = target.indexOf('?')
  if (q >= 0) target = target.slice(0, q)
  target = target.trim().replace(/\\/g, '/')
  if (!target || SCHEME_RE.test(target) || target.startsWith('/')) return null

  const ext = /\.[^./]*$/.exec(target.slice(target.lastIndexOf('/') + 1))
  if (ext && ext[0].toLowerCase() !== '.md') return null // 图片、PDF 等不是文档链接

  // 只有 ./ 与 ../ 开头的相对写法才接在所在文档的目录上；其余按工作流根目录（与服务端 wiki 链接一致）
  const dirParts = fromPath && (target.startsWith('./') || target.startsWith('../')) && fromPath.includes('/') ? fromPath.slice(0, fromPath.lastIndexOf('/')).split('/') : []
  const clean = cleanSegments([...dirParts, ...target.split('/')])
  // 清理后仍含「..」（退出了根目录，或文件名里就有 ..）整体拒绝，与服务端 NormalizePath 一致
  if (!clean || clean.includes('..')) return null
  const path = clean.toLowerCase().endsWith('.md') ? clean : `${clean}.md`
  return { path, anchor, label, written }
}

function scanDocRefs(text: string, idx: LinkRefIndex, fromPath: string): Hit[] {
  if (!text.includes('[[')) return []
  const out: Hit[] = []
  for (const m of text.matchAll(WIKI_RE)) {
    const parsed = parseDocRef(m[1], fromPath)
    if (!parsed) continue
    const hit = idx.doc(parsed.path)
    if (!hit) continue
    out.push({
      at: m.index ?? 0,
      length: m[0].length,
      shown: parsed.label || parsed.written,
      ref: { kind: 'doc', key: hit.key, ...(parsed.anchor ? { anchor: parsed.anchor } : {}), ...(hit.title ? { title: hit.title } : {}) },
    })
  }
  return out
}

// ---------------------------------------------------------------------------
// 合并成片段
// ---------------------------------------------------------------------------

function taskHits(text: string, idx: LinkRefIndex): Hit[] {
  return scanTaskRefs(text, seq => idx.task(seq) !== null).map(h => {
    const found = idx.task(h.seq)
    return {
      at: h.at,
      length: h.length,
      shown: text.slice(h.at, h.at + h.length),
      ref: { kind: 'task' as const, key: String(h.seq), ...(found?.title ? { title: found.title } : {}) },
    }
  })
}

/**
 * 把一段文字切成「普通文字 / 引用」交替的片段。四种引用一起扫描，按出现位置排序，互相重叠的取先出现（同位置取更长）的那个——
 * 比如 [[design/x.md#Q3]] 整体是文档，里面的 Q3 不再单独成链接。没有可链接的引用时返回单个普通片段。
 */
export function splitLinkRefs(text: string, idx: LinkRefIndex, fromPath = ''): LinkRefSegment[] {
  if (!text) return []
  const hits = [...taskHits(text, idx), ...scanConfirmRefs(text, idx), ...scanDocRefs(text, idx, fromPath), ...scanSessionRefs(text, idx)]
  if (hits.length === 0) return [{ text }]
  hits.sort((a, b) => a.at - b.at || b.length - a.length)

  const out: LinkRefSegment[] = []
  let last = 0
  for (const h of hits) {
    if (h.at < last) continue // 被前一个引用盖住了
    if (h.at > last) out.push({ text: text.slice(last, h.at) })
    const original = text.slice(h.at, h.at + h.length)
    out.push({ text: original, ...(h.shown !== original ? { shown: h.shown } : {}), ref: h.ref })
    last = h.at + h.length
  }
  if (last < text.length) out.push({ text: text.slice(last) })
  return out
}

/** 整段文字恰好就是一个引用（行内代码 `#36` / `Q23` / `[[design/x.md]]`）时返回这个片段，否则 null。 */
export function wholeLinkRef(text: string, idx: LinkRefIndex, fromPath = ''): LinkRefSegment | null {
  const t = text.trim()
  if (!t) return null
  const segs = splitLinkRefs(t, idx, fromPath)
  return segs.length === 1 && segs[0].ref && segs[0].text === t ? segs[0] : null
}

// ---------------------------------------------------------------------------
// 地址与说明
// ---------------------------------------------------------------------------

/**
 * 每种引用的真实深链地址（站内相对路径；⌘ / Ctrl / 中键点链接在新标签页里打开的就是它）：
 *   任务 → 任务页并弹出详情（/work/<工作流>?seq=N，WorkPage 按「板内序号」向服务端解析；
 *          注意不能写成 ?task=N：那是数据库编号，#N 是板内序号，两个数不同，新标签页会打开别的任务，#242）
 *   确认项 → 会话页并点名那张卡（/?wf=<工作流>&q=Q23，useNotifyRuntime 处理）
 *   会话 → 会话页并选中那一轮（/?wf=<工作流>&s=<会话号>，通知深链同一套）
 *   文档 → 老文档地址（/docs/<工作流>/<路径>#锚点；工作流属于项目时路由守卫会替换成项目内的文档页）
 */
export function linkRefHref(ref: LinkRef, workflowId: string): string {
  const wf = encodeURIComponent(workflowId)
  switch (ref.kind) {
    case 'task':
      return `/work/${wf}?seq=${encodeURIComponent(ref.key)}`
    case 'confirm':
      return `/?wf=${wf}&q=${encodeURIComponent(ref.key)}`
    case 'session':
      return `/?wf=${wf}&s=${encodeURIComponent(ref.key)}`
    case 'doc': {
      const path = ref.key.split('/').map(encodeURIComponent).join('/')
      return `/docs/${wf}/${path}${ref.anchor ? `#${encodeURIComponent(ref.anchor)}` : ''}`
    }
  }
}

const HINT_HEAD: Record<LinkRefKind, string> = {
  task: '查看任务',
  confirm: '查看确认项',
  doc: '打开文档',
  session: '跳到这一轮',
}

const HINT_TITLE_MAX = 80

/** 悬停说明：「查看任务 #36 · 标题」；文档写规范化后的路径（含锚点）。 */
export function linkRefHint(ref: LinkRef, shown: string): string {
  const what = ref.kind === 'doc' ? `${ref.key}${ref.anchor ? `#${ref.anchor}` : ''}` : shown
  const head = `${HINT_HEAD[ref.kind]} ${what}`
  const title = (ref.title ?? '').replace(/\s+/g, ' ').trim()
  if (!title) return head
  return `${head} · ${title.length > HINT_TITLE_MAX ? `${title.slice(0, HINT_TITLE_MAX - 1)}…` : title}`
}
