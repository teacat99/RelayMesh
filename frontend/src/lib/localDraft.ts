// 本机浏览器草稿（#144）：表单 / 评价类输入「没提交的内容」及时落到 localStorage，
// 切换会话 / 任务 / 文档、关掉弹窗、刷新页面之后回来还在；提交成功（或用户点「丢弃」）才清掉。
//
// 边界（与 #140 的约定一致）：
// - 只存在这个浏览器里，不上传服务端；跨设备同步另议（#145 已搁置）。
// - 存的是明文：严禁用在口令 / 令牌 / SSH 密钥 / 凭据这类输入上。
// - 草稿是「尽力而为」的保险：存储不可用、配额满、超长，一律静默跳过，绝不影响正常输入与提交。
//
// 本文件是纯逻辑（存储可注入，便于单测）；Vue 侧的薄封装在 composables/useLocalDraft.ts。

export const DRAFT_PREFIX = 'relaymesh.draft.v1:'
/** 草稿保留时长：超过就当它已经过期，启动时顺手清掉。 */
export const DRAFT_TTL_MS = 14 * 24 * 60 * 60 * 1000
/** 单条草稿序列化后的字符数上限（超出不存，避免把配额吃光）。 */
export const DRAFT_MAX_CHARS = 64 * 1024
/** 全部草稿的条数 / 总字符数上限：超出按「最久没动过」淘汰。 */
export const DRAFT_MAX_ENTRIES = 200
export const DRAFT_MAX_TOTAL_CHARS = 1_500_000
/** 输入停顿多久后落盘（每次按键都写盘没必要）。 */
export const DRAFT_DEBOUNCE_MS = 300

/** localStorage 的最小子集；测试里用内存实现替换。 */
export interface DraftStorage {
  readonly length: number
  key(index: number): string | null
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

interface DraftEntry {
  v: 1
  /** 最后一次写入的时间（毫秒时间戳），用来判断过期与淘汰顺序。 */
  t: number
  d: unknown
}

/** 各处草稿的作用域：同一个字符串 = 同一份草稿。集中在这里，调用方与「提交后清掉」不会各写各的。 */
export const draftScopes = {
  /** 待确认卡 / 文档决策卡：选择 + 备注（同一张确认项在两处入口共用一份）。 */
  confirm: (workflowId: string, ref: string) => `confirm:${workflowId}:${ref}`,
  /** 任务的「发起改进」输入。 */
  workImprove: (taskId: number) => `work-improve:${taskId}`,
  /** 任务描述的编辑。 */
  workDesc: (taskId: number) => `work-desc:${taskId}`,
  /** 文档页的评论框。 */
  docComment: (workflowId: string, path: string) => `doc-comment:${workflowId}:${path}`,
  /** 文档正文 / 标题的编辑。 */
  docEdit: (workflowId: string, path: string) => `doc-edit:${workflowId}:${path}`,
} as const

export function storageKey(scope: string): string {
  return DRAFT_PREFIX + scope
}

let browserStore: DraftStorage | null | undefined

/**
 * 浏览器的 localStorage；被禁用时「访问这个属性」本身就会抛 SecurityError，这时返回 null（草稿功能整体静默关闭）。
 * 不靠写一个探针来判断：存储恰好写满时探针会失败，会把本来能用的草稿功能误关掉；
 * 真写不进去由 writeDraft 兜底（淘汰旧草稿重试，仍失败就放弃这一次）。
 */
export function browserDraftStorage(): DraftStorage | null {
  if (browserStore !== undefined) return browserStore
  try {
    browserStore = typeof window !== 'undefined' && window.localStorage ? window.localStorage : null
  } catch {
    browserStore = null
  }
  return browserStore
}

function parseEntry(raw: string): DraftEntry | null {
  try {
    const o = JSON.parse(raw) as Partial<DraftEntry> | null
    if (!o || typeof o !== 'object') return null
    if (o.v !== 1 || typeof o.t !== 'number' || !Number.isFinite(o.t) || o.d === undefined) return null
    return o as DraftEntry
  } catch {
    return null
  }
}

export interface ReadOptions<T> {
  ttlMs?: number
  /** 结构校验：旧版本 / 被改坏的草稿不符合期望的形状时当作没有，并顺手删掉。 */
  validate?: (d: unknown) => d is T
}

/** 读一条草稿；不存在、过期、损坏、形状不对都返回 null（后三种会顺手删掉）。 */
export function readDraft<T>(store: DraftStorage, scope: string, now: number, opts: ReadOptions<T> = {}): T | null {
  const key = storageKey(scope)
  let raw: string | null
  try {
    raw = store.getItem(key)
  } catch {
    return null
  }
  if (raw === null) return null
  const entry = parseEntry(raw)
  const ttl = opts.ttlMs ?? DRAFT_TTL_MS
  const bad = !entry || now - entry.t > ttl || (opts.validate !== undefined && !opts.validate(entry.d))
  if (bad || !entry) {
    safeRemove(store, key)
    return null
  }
  return entry.d as T
}

export type WriteResult = 'saved' | 'too_large' | 'failed'

/** 写一条草稿。超长不写；配额满时先淘汰最旧的一批再试一次，仍失败就放弃。 */
export function writeDraft<T>(store: DraftStorage, scope: string, payload: T, now: number, maxChars: number = DRAFT_MAX_CHARS): WriteResult {
  let raw: string
  try {
    raw = JSON.stringify({ v: 1, t: now, d: payload } satisfies DraftEntry)
  } catch {
    return 'failed'
  }
  if (raw.length > maxChars) return 'too_large'
  const key = storageKey(scope)
  try {
    store.setItem(key, raw)
    return 'saved'
  } catch {
    // 多半是配额满：腾出一些再试一次
  }
  pruneDrafts(store, now, { maxEntries: 20, maxTotalChars: 200_000 })
  try {
    store.setItem(key, raw)
    return 'saved'
  } catch {
    return 'failed'
  }
}

export function clearDraft(store: DraftStorage, scope: string): void {
  safeRemove(store, storageKey(scope))
}

function safeRemove(store: DraftStorage, key: string): void {
  try {
    store.removeItem(key)
  } catch {
    /* 忽略 */
  }
}

export interface DraftInfo {
  key: string
  scope: string
  /** 损坏的条目 t 记为 0。 */
  t: number
  size: number
  valid: boolean
}

export function listDrafts(store: DraftStorage): DraftInfo[] {
  const out: DraftInfo[] = []
  try {
    // 先收集 key 再读：边遍历边删会让下标错位
    const keys: string[] = []
    for (let i = 0; i < store.length; i++) {
      const k = store.key(i)
      if (k && k.startsWith(DRAFT_PREFIX)) keys.push(k)
    }
    for (const key of keys) {
      const raw = store.getItem(key)
      if (raw === null) continue
      const entry = parseEntry(raw)
      out.push({ key, scope: key.slice(DRAFT_PREFIX.length), t: entry?.t ?? 0, size: raw.length, valid: entry !== null })
    }
  } catch {
    /* 存储读不了：当作没有 */
  }
  return out
}

export interface PruneOptions {
  ttlMs?: number
  maxEntries?: number
  maxTotalChars?: number
}

/** 清理：损坏的、过期的先删；再按「最久没动过」淘汰到条数 / 总大小的上限内。返回删掉了几条。 */
export function pruneDrafts(store: DraftStorage, now: number, opts: PruneOptions = {}): number {
  const ttl = opts.ttlMs ?? DRAFT_TTL_MS
  const maxEntries = opts.maxEntries ?? DRAFT_MAX_ENTRIES
  const maxTotal = opts.maxTotalChars ?? DRAFT_MAX_TOTAL_CHARS
  let removed = 0
  const keep: DraftInfo[] = []
  for (const info of listDrafts(store)) {
    if (!info.valid || now - info.t > ttl) {
      safeRemove(store, info.key)
      removed++
    } else {
      keep.push(info)
    }
  }
  keep.sort((a, b) => b.t - a.t)
  let total = 0
  keep.forEach((info, i) => {
    total += info.size
    if (i >= maxEntries || total > maxTotal) {
      safeRemove(store, info.key)
      removed++
    }
  })
  return removed
}

// ---- 控制器：防抖落盘 + 切换作用域时不丢 ----

export interface DraftControllerOptions<T> {
  store: DraftStorage | null
  /** 这份内容算不算「空」：空内容不落盘，并且会把已有草稿删掉。 */
  isEmpty: (payload: T) => boolean
  validate?: (d: unknown) => d is T
  debounceMs?: number
  /** 单条草稿的字符上限；文档正文这类长文本可以放宽。 */
  maxChars?: number
  now?: () => number
}

const live = new Set<DraftController<never>>()

/**
 * 一个输入区对应一个控制器：
 * - switchTo(scope)：先把旧作用域里还没落盘的写下去，再读新作用域已有的草稿并返回；
 * - update(payload)：登记最新内容，停顿 debounceMs 后落盘；内容与已落盘的一致时不重复写；
 * - flush / clear / dispose：立即落盘 / 删掉当前作用域的草稿 / 卸载时落盘。
 * 框架无关，Vue 里用 useLocalDraft 包一层。
 */
export class DraftController<T> {
  private scope: string | null = null
  private pending: { scope: string; payload: T } | null = null
  private timer: ReturnType<typeof setTimeout> | null = null
  /** 各作用域上一次落盘的内容（JSON），内容没变就不再写（也就不会无谓地续期）。 */
  private saved = new Map<string, string>()
  private readonly opts: DraftControllerOptions<T>

  constructor(opts: DraftControllerOptions<T>) {
    this.opts = opts
    live.add(this as unknown as DraftController<never>)
  }

  get currentScope(): string | null {
    return this.scope
  }

  private now(): number {
    return this.opts.now ? this.opts.now() : Date.now()
  }

  switchTo(scope: string | null): T | null {
    this.flush()
    this.scope = scope
    const { store } = this.opts
    if (!scope || !store) return null
    const saved = readDraft<T>(store, scope, this.now(), { validate: this.opts.validate })
    if (saved === null) return null
    if (this.opts.isEmpty(saved)) {
      clearDraft(store, scope)
      this.saved.delete(scope)
      return null
    }
    this.saved.set(scope, JSON.stringify(saved))
    return saved
  }

  update(payload: T): void {
    if (!this.scope || !this.opts.store) return
    this.pending = { scope: this.scope, payload }
    if (this.timer !== null) clearTimeout(this.timer)
    this.timer = setTimeout(() => {
      this.timer = null
      this.flush()
    }, this.opts.debounceMs ?? DRAFT_DEBOUNCE_MS)
  }

  flush(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
    const p = this.pending
    this.pending = null
    const { store } = this.opts
    if (!p || !store) return
    if (this.opts.isEmpty(p.payload)) {
      clearDraft(store, p.scope)
      this.saved.delete(p.scope)
      return
    }
    let json: string
    try {
      json = JSON.stringify(p.payload)
    } catch {
      return
    }
    if (this.saved.get(p.scope) === json) return
    if (writeDraft(store, p.scope, p.payload, this.now(), this.opts.maxChars) === 'saved') this.saved.set(p.scope, json)
  }

  /** 删掉当前作用域的草稿，并丢弃还没落盘的内容（提交成功 / 用户点「丢弃」）。 */
  clear(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
    this.pending = null
    const { store } = this.opts
    if (this.scope && store) {
      clearDraft(store, this.scope)
      this.saved.delete(this.scope)
    }
  }

  dispose(): void {
    this.flush()
    this.scope = null
    live.delete(this as unknown as DraftController<never>)
  }
}

/**
 * 提交成功后按作用域清掉草稿：不要求调用方还握着那个输入控件，
 * 且会同时丢弃仍活着的控制器里还没落盘的内容，避免「刚清掉又被防抖写回来」。
 */
export function clearLocalDraft(scope: string, store: DraftStorage | null = browserDraftStorage()): void {
  for (const c of live) {
    if (c.currentScope === scope) c.clear()
  }
  if (store) clearDraft(store, scope)
}

/** 应用启动时调用一次：清掉过期 / 损坏的草稿，并把总量压在上限内。 */
export function pruneBrowserDrafts(now: number = Date.now()): number {
  const store = browserDraftStorage()
  return store ? pruneDrafts(store, now) : 0
}
