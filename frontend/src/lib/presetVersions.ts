// 快捷标签预设的版本留存。
//
// 设置页「快捷标签预设」里有三类内容是随应用「出厂」的：工作流阶段模板、输入区的快捷回复预设、全局通用预设。
// 它们同时是用户可以随意改的自定义内容——出厂文案更新时，不能悄悄盖掉用户改过的版本，也不能让用户永远拿不到新版。
//
// 做法是三方比较：
//   基线（用户采用这条内容时的出厂文案的签名）／ 我的（用户现在的版本）／ 最新（当前版本出厂的文案）。
//   - 我的 == 最新：什么都不用做（顺手把基线对齐到最新）；
//   - 还没有基线：当作「我的」就是用户的选择，把基线定在最新（此后出厂文案再变才会有动静）；
//   - 基线 == 最新：出厂文案没变，用户的改动原样保留；
//   - 出厂文案变了，而我的 == 基线（用户从没改过）：直接换成最新，旧版存进留存区；
//   - 出厂文案变了，而我的 != 基线（用户改过）：冲突，交给用户选「均保留 / 使用最新 / 使用旧版」。
// 被换下来的内容都存进留存区（旧版 / 新版），可以在设置里重新启用、删除或清空，不会丢。
// 另外记下每条内容采用过的出厂文案的历史：没刷新的旧页面拿着更旧的出厂文案来对账时，直接跳过，
// 否则新旧两个页面会各自把对方「升级」回去，经设置同步来回写个没完。
//
// 只有这三类自定义内容走这里；系统提示词（服务指引等）的更新不在其内。
// 纯函数，便于单元测试；落盘与界面在 stores/settings.ts 与 components/settings/PresetVersionArchive.vue。

export type PresetScope = 'phase' | 'general' | 'status:online'

export const PRESET_SCOPES: readonly PresetScope[] = ['phase', 'status:online', 'general']

export const SCOPE_NAMES: Readonly<Record<PresetScope, string>> = {
  phase: '工作流阶段模板',
  'status:online': '快捷回复预设',
  general: '全局通用预设',
}

/** 一条受管理的内容：阶段（id / label / description / prompt）或预设（id / title / content / ...）。 */
export type ScopeItem = { id: string } & Record<string, unknown>
export type ScopeLists = Record<PresetScope, ScopeItem[]>

/** 留存区里条目的种类：旧版 = 被换下来的用户内容或旧出厂文案；新版 = 没有启用的最新出厂文案。 */
export type ArchiveKind = 'old' | 'new'

/** 为什么进了留存区：自动升级（用户没改过）/ 冲突里选了「使用最新」/ 冲突里选了「使用旧版」/ 点了「恢复默认」。 */
export type ArchiveReason = 'auto-upgrade' | 'use-latest' | 'use-mine' | 'reset'

export interface ArchivedEntry {
  /** 由内容派生的编号：同一来源、同一内容、同一种类只会有一条。 */
  uid: string
  scope: PresetScope
  /** 它原来是哪条内容（出厂 id）。 */
  originId: string
  kind: ArchiveKind
  reason: ArchiveReason
  /** 列表里显示的名字。 */
  label: string
  payload: ScopeItem
  archivedAt: string
}

export interface PresetVersions {
  /** key（scope|id）→ 用户采用这条内容时的出厂文案的签名。 */
  baseline: Record<string, string>
  /** key → 以前采用过、后来被换掉的出厂文案签名（最近的在后），用来识别没刷新的旧页面。 */
  history: Record<string, string[]>
  archive: ArchivedEntry[]
}

export type Decision = 'both' | 'latest' | 'mine'

export interface PresetConflict {
  key: string
  scope: PresetScope
  id: string
  label: string
  mine: ScopeItem
  latest: ScopeItem
  /** 给对比界面用的正文（预设的内容 / 阶段的提示词）。 */
  mineText: string
  latestText: string
}

export const ARCHIVE_LIMIT = 100
export const HISTORY_LIMIT = 16

export const emptyVersions = (): PresetVersions => ({ baseline: {}, history: {}, archive: [] })

export const keyOf = (scope: PresetScope, id: string) => `${scope}|${id}`

// ---------- 签名 ----------

/** cyrb53：不需要密码学强度，只要稳定、分布好、足够短。 */
function hashString(str: string): string {
  let h1 = 0xdeadbeef
  let h2 = 0x41c6ce57
  for (let i = 0; i < str.length; i++) {
    const ch = str.charCodeAt(i)
    h1 = Math.imul(h1 ^ ch, 2654435761)
    h2 = Math.imul(h2 ^ ch, 1597334677)
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909)
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909)
  return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36)
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

/** 参与比较的字段。运行时状态（例如预设当前有没有勾选追加）不算内容。 */
function signatureFields(scope: PresetScope, item: ScopeItem): unknown[] {
  if (scope === 'phase') return [str(item.label), str(item.description), str(item.prompt)]
  const targets = Array.isArray(item.mutexTargets) ? item.mutexTargets.map(String).sort() : []
  return [str(item.title), str(item.content), !!item.allowAppend, str(item.appendTitle), !!item.isMutex, targets]
}

export function entrySignature(scope: PresetScope, item: ScopeItem): string {
  return hashString(JSON.stringify(signatureFields(scope, item)))
}

/** 条目的名字与正文，给列表和对比界面显示。 */
export function describeItem(scope: PresetScope, item: ScopeItem): { label: string; text: string } {
  if (scope === 'phase') return { label: str(item.label) || item.id, text: str(item.prompt) || str(item.description) }
  return { label: str(item.title) || item.id, text: str(item.content) }
}

// ---------- 工具 ----------

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T

export function cloneLists(lists: ScopeLists): ScopeLists {
  const out = {} as ScopeLists
  for (const s of PRESET_SCOPES) out[s] = clone(lists[s] ?? [])
  return out
}

function indexById(items: ScopeItem[]): Map<string, ScopeItem> {
  const m = new Map<string, ScopeItem>()
  for (const it of items) m.set(it.id, it)
  return m
}

/** 在 existing 里找一个没被占用的 id：base、base2、base3 ... */
export function uniqueId(existing: ScopeItem[], base: string): string {
  const used = new Set(existing.map(i => i.id))
  if (!used.has(base)) return base
  for (let n = 2; ; n++) if (!used.has(`${base}${n}`)) return `${base}${n}`
}

/** 给条目的名字加个后缀（阶段改 label，预设改 title 与追加时显示的 appendTitle）；已经带这个后缀的不重复加。 */
function withSuffix(scope: PresetScope, item: ScopeItem, suffix: string): ScopeItem {
  const add = (s: unknown) => {
    const t = str(s)
    return t.endsWith(suffix) ? t : t + suffix
  }
  const out = clone(item)
  if (scope === 'phase') out.label = add(item.label)
  else {
    out.title = add(item.title)
    if (str(item.appendTitle)) out.appendTitle = add(item.appendTitle)
  }
  return out
}

function makeEntry(scope: PresetScope, item: ScopeItem, kind: ArchiveKind, reason: ArchiveReason, now: string): ArchivedEntry {
  const payload = clone(item)
  delete payload.isAppendActive
  return {
    uid: `ar-${hashString(`${scope}|${item.id}|${entrySignature(scope, item)}|${kind}`)}`,
    scope,
    originId: item.id,
    kind,
    reason,
    label: describeItem(scope, item).label,
    payload,
    archivedAt: now,
  }
}

/** 放进留存区：同一条（编号相同）不重复；超过上限时先丢最早的。 */
function pushArchive(archive: ArchivedEntry[], entry: ArchivedEntry): ArchivedEntry[] {
  if (archive.some(e => e.uid === entry.uid)) return archive
  const next = [...archive, entry]
  return next.length > ARCHIVE_LIMIT ? next.slice(next.length - ARCHIVE_LIMIT) : next
}

/** 用最新的出厂文案换掉一条内容，保留它身上的运行时状态（预设的追加勾选）。 */
function replaceItem(list: ScopeItem[], id: string, latest: ScopeItem): ScopeItem[] {
  return list.map(it => {
    if (it.id !== id) return it
    const next = clone(latest)
    if ('isAppendActive' in it) next.isAppendActive = it.isAppendActive
    return next
  })
}

// ---------- 对账 ----------

export interface ReconcileInput {
  /** 当前版本出厂的各类内容。 */
  shipped: ScopeLists
  /** 用户现在的各类内容。 */
  mine: ScopeLists
  versions?: PresetVersions
  now: string
}

export interface ReconcileResult {
  lists: ScopeLists
  versions: PresetVersions
  /** 需要用户选择的冲突。 */
  conflicts: PresetConflict[]
  /** 自动升级了几条（用户从没改过、出厂文案变了）。 */
  upgraded: number
  /** lists 或 versions 是否有变化（需要落盘）。 */
  changed: boolean
}

/** 把一条的基线换成新的签名；被换下的旧签名记进历史。 */
function setBaseline(v: PresetVersions, key: string, sig: string): void {
  const prev = v.baseline[key]
  if (prev !== undefined && prev !== sig) {
    const past = (v.history[key] ?? []).filter(s => s !== prev)
    past.push(prev)
    v.history[key] = past.length > HISTORY_LIMIT ? past.slice(past.length - HISTORY_LIMIT) : past
  }
  v.baseline[key] = sig
}

/** 出厂文案的签名 sig 是不是「已经被后来的版本取代」的旧版本（本端是没刷新的旧页面）。 */
function isSuperseded(v: PresetVersions, key: string, sig: string): boolean {
  return v.baseline[key] !== undefined && v.baseline[key] !== sig && (v.history[key] ?? []).includes(sig)
}

function copyVersions(v: PresetVersions): PresetVersions {
  return { baseline: { ...v.baseline }, history: clone(v.history), archive: [...v.archive] }
}

export function reconcile(input: ReconcileInput): ReconcileResult {
  const before = JSON.stringify(input.versions ?? emptyVersions())
  const versions = copyVersions(input.versions ?? emptyVersions())
  const lists = cloneLists(input.mine)
  const conflicts: PresetConflict[] = []
  let upgraded = 0

  for (const scope of PRESET_SCOPES) {
    const shipped = indexById(input.shipped[scope] ?? [])
    lists[scope] = lists[scope].map(mine => {
      const latest = shipped.get(mine.id)
      if (!latest) return mine // 用户自建的，或出厂里已经没有的：不归这里管
      const key = keyOf(scope, mine.id)
      const sigMine = entrySignature(scope, mine)
      const sigLatest = entrySignature(scope, latest)
      if (isSuperseded(versions, key, sigLatest)) return mine // 本端的出厂文案比已采用的更旧（没刷新的旧页面）：不动
      const base = versions.baseline[key]
      if (sigMine === sigLatest) {
        setBaseline(versions, key, sigLatest)
        return mine
      }
      if (base === undefined) {
        setBaseline(versions, key, sigLatest) // 头一次对账：现在这样就是用户的选择
        return mine
      }
      if (base === sigLatest) return mine // 出厂文案没变：用户的改动原样保留
      if (sigMine === base) {
        // 用户从没改过这条，出厂文案变了：直接换成最新，旧版留存
        versions.archive = pushArchive(versions.archive, makeEntry(scope, mine, 'old', 'auto-upgrade', input.now))
        setBaseline(versions, key, sigLatest)
        upgraded++
        return replaceItem([mine], mine.id, latest)[0]
      }
      const d = describeItem(scope, mine)
      conflicts.push({
        key,
        scope,
        id: mine.id,
        label: d.label,
        mine: clone(mine),
        latest: clone(latest),
        mineText: d.text,
        latestText: describeItem(scope, latest).text,
      })
      return mine
    })
  }

  return { lists, versions, conflicts, upgraded, changed: upgraded > 0 || JSON.stringify(versions) !== before }
}

// ---------- 处理冲突 ----------

export interface ResolveInput {
  lists: ScopeLists
  versions: PresetVersions
  conflicts: PresetConflict[]
  /** key → 选择；没有给出选择的冲突原样留着（下次对账还会问）。 */
  decisions: Record<string, Decision>
  now: string
}

export interface ResolveResult {
  lists: ScopeLists
  versions: PresetVersions
  /** 处理了几条。 */
  resolved: number
}

export function resolveConflicts(input: ResolveInput): ResolveResult {
  const lists = cloneLists(input.lists)
  const versions = copyVersions(input.versions)
  let resolved = 0

  for (const c of input.conflicts) {
    const decision = input.decisions[c.key]
    if (!decision) continue
    const mine = lists[c.scope].find(i => i.id === c.id)
    if (!mine) continue // 在弹框期间被用户删掉了：没什么可处理的
    const sigLatest = entrySignature(c.scope, c.latest)
    if (isSuperseded(versions, c.key, sigLatest)) continue // 弹框期间别的页面已经采用了更新的版本：这条的前提不在了，交给下次对账
    if (entrySignature(c.scope, mine) === sigLatest) {
      setBaseline(versions, c.key, sigLatest) // 弹框期间别的页面已经处理过（现在就是最新）：没有可选的了，只把基线对齐
      continue
    }
    if (decision === 'latest') {
      versions.archive = pushArchive(versions.archive, makeEntry(c.scope, mine, 'old', 'use-latest', input.now))
      lists[c.scope] = replaceItem(lists[c.scope], c.id, c.latest)
    } else if (decision === 'mine') {
      versions.archive = pushArchive(versions.archive, makeEntry(c.scope, c.latest, 'new', 'use-mine', input.now))
    } else {
      const copy = withSuffix(c.scope, c.latest, '（新版）')
      copy.id = uniqueId(lists[c.scope], `${c.id}-new`)
      const at = lists[c.scope].findIndex(i => i.id === c.id)
      lists[c.scope].splice(at + 1, 0, copy)
    }
    setBaseline(versions, c.key, sigLatest)
    resolved++
  }
  return { lists, versions, resolved }
}

// ---------- 留存区 ----------

/** 把留存区里的一条重新启用：作为一条新内容加到对应列表末尾（带「旧版 / 新版」后缀，id 不与现有的重复）。 */
export function reenableArchived(lists: ScopeLists, versions: PresetVersions, uid: string): { lists: ScopeLists; versions: PresetVersions } | null {
  const entry = versions.archive.find(e => e.uid === uid)
  if (!entry) return null
  const next = cloneLists(lists)
  const item = withSuffix(entry.scope, entry.payload, entry.kind === 'old' ? '（旧版）' : '（新版）')
  item.id = uniqueId(next[entry.scope], `${entry.originId}-${entry.kind}`)
  if (entry.scope !== 'phase') item.isAppendActive = false
  next[entry.scope].push(item)
  return { lists: next, versions: deleteArchived(versions, uid) }
}

/**
 * 点「恢复默认」之前，把会被换掉的内容存进留存区：和出厂文案不同的（用户改过的、用户自建的）。
 * 和出厂文案一模一样的不用存——恢复默认之后还是它。
 */
export function archiveReplaced(versions: PresetVersions, scope: PresetScope, current: ScopeItem[], shipped: ScopeItem[], now: string): PresetVersions {
  const next = copyVersions(versions)
  const byId = indexById(shipped)
  for (const item of current) {
    const d = byId.get(item.id)
    if (d && entrySignature(scope, d) === entrySignature(scope, item)) continue
    next.archive = pushArchive(next.archive, makeEntry(scope, item, 'old', 'reset', now))
  }
  return next
}

export function deleteArchived(versions: PresetVersions, uid: string): PresetVersions {
  return { ...copyVersions(versions), archive: versions.archive.filter(e => e.uid !== uid) }
}

export function clearArchive(versions: PresetVersions): PresetVersions {
  return { ...copyVersions(versions), archive: [] }
}

/** 留存区里一条的来源说明，给列表显示。 */
export function archiveReasonText(e: Pick<ArchivedEntry, 'kind' | 'reason'>): string {
  switch (e.reason) {
    case 'auto-upgrade':
      return '出厂文案更新，你没改过，已自动换成新版'
    case 'use-latest':
      return '出厂文案更新，你选了「使用最新」，你原来的版本存在这里'
    case 'use-mine':
      return '出厂文案更新，你选了「使用旧版」，最新的出厂文案存在这里'
    case 'reset':
      return '点「恢复默认」前的版本（你改过或自建的内容）'
  }
}

/** 兼容读取：落盘的数据可能缺字段或类型不对（手改过、旧版本写的），统一收敛成合法结构。 */
export function normalizeVersions(raw: unknown): PresetVersions {
  const out = emptyVersions()
  if (!raw || typeof raw !== 'object') return out
  const r = raw as { baseline?: unknown; history?: unknown; archive?: unknown }
  // 已经下线的范围（早先的「暂离 / 托管」模式预设）留下的记录直接丢掉
  const known = (k: string) => (PRESET_SCOPES as readonly string[]).includes(k.split('|')[0])
  if (r.baseline && typeof r.baseline === 'object') {
    for (const [k, v] of Object.entries(r.baseline as Record<string, unknown>)) if (typeof v === 'string' && known(k)) out.baseline[k] = v
  }
  if (r.history && typeof r.history === 'object') {
    for (const [k, v] of Object.entries(r.history as Record<string, unknown>)) {
      if (Array.isArray(v) && known(k)) out.history[k] = v.filter((s): s is string => typeof s === 'string').slice(-HISTORY_LIMIT)
    }
  }
  if (Array.isArray(r.archive)) {
    for (const e of r.archive as Array<Partial<ArchivedEntry> | null>) {
      if (!e || typeof e !== 'object') continue
      if (!e.uid || !e.scope || !PRESET_SCOPES.includes(e.scope) || !e.payload || typeof e.payload !== 'object') continue
      if (e.kind !== 'old' && e.kind !== 'new') continue
      out.archive.push({
        uid: String(e.uid),
        scope: e.scope,
        originId: String(e.originId ?? e.payload.id ?? ''),
        kind: e.kind,
        reason: e.reason === 'use-latest' || e.reason === 'use-mine' || e.reason === 'reset' ? e.reason : 'auto-upgrade',
        label: String(e.label ?? ''),
        payload: e.payload as ScopeItem,
        archivedAt: String(e.archivedAt ?? ''),
      })
    }
  }
  return out
}
