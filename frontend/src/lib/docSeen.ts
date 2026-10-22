// 「自上次查看后的变更」：每个读者（这里是这台浏览器）在每个工作流上一个游标，
// 游标 = 上次离开文档页时已知的最新更新时间。第一次来没有游标，不把全部文档都算成「新的」。

export interface SeenItem {
  path: string
  title: string
  updated_at: string
}

const KEY_PREFIX = 'rm-docs-seen:'

export interface SeenStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

export function readSeenCursor(storage: SeenStorage | undefined, workflowId: string): string {
  try {
    return storage?.getItem(KEY_PREFIX + workflowId) || ''
  } catch {
    return ''
  }
}

export function writeSeenCursor(storage: SeenStorage | undefined, workflowId: string, cursor: string): void {
  if (!cursor) return
  try {
    storage?.setItem(KEY_PREFIX + workflowId, cursor)
  } catch {
    /* 隐私模式 / 配额：游标丢了只是少提示一次 */
  }
}

/** 这批文档里最新的更新时间（ISO 字符串，取不到返回空）。 */
export function latestUpdate(items: readonly SeenItem[]): string {
  let best = 0
  let bestRaw = ''
  for (const it of items) {
    const t = Date.parse(it.updated_at)
    if (!Number.isNaN(t) && t > best) {
      best = t
      bestRaw = it.updated_at
    }
  }
  return bestRaw
}

/** 游标之后更新过的文档，最近的在前；没有游标时返回空（首次访问不提示）。 */
export function changedSince(items: readonly SeenItem[], cursor: string, seenPaths?: ReadonlySet<string>): SeenItem[] {
  const c = Date.parse(cursor)
  if (!cursor || Number.isNaN(c)) return []
  return items
    .filter(it => Date.parse(it.updated_at) > c && !seenPaths?.has(it.path))
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
}

/** 离开页面时要写回的游标：不回退，只在有更新的时间时前进。 */
export function nextCursor(prev: string, items: readonly SeenItem[]): string {
  const latest = latestUpdate(items)
  if (!latest) return prev
  if (!prev) return latest
  return Date.parse(latest) > Date.parse(prev) ? latest : prev
}
