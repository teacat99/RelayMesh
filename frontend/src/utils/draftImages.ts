/**
 * 草稿图片的本地大图仓库与载入链（D-201 / 反馈 #8「草稿区图片丢失」）。
 *
 * 纯逻辑：仓库、服务端草稿读取都通过参数注入，不依赖 Vue 与 DOM，可以直接在 Node 下单测。
 *
 * 丢图的真实链路（均已在代码层面确认）：
 * 1. localStorage 只有约 5MB 配额，所以落盘时把 ≥100KB 的图片正文剥掉（draftSync.sanitizeDraftsForStorage），
 *    只留一个空壳（名称 / 尺寸 / 哈希）；
 * 2. 刷新后优先读本地副本，空壳图片被原样载入输入区，缩略图是破的；旧实现不会再去服务端取回正文；
 * 3. 这时随便敲一个字，草稿就会整份（含空壳图片）上传覆盖服务端那份完整的图片，图片从此在所有设备上都找不回来。
 *
 * 修复由三层组成：
 * - 本地大图仓库（IndexedDB，容量远大于 localStorage）：图片一加入就存进去，刷新后秒级取回，不依赖网络；
 * - 载入链：内存 → 本地仓库 → 云端草稿图片（粘贴即上传的那份，跨设备 / 仓库被浏览器清理时兜底）→ 服务端草稿（旧版本内联在草稿 JSON
 *   里的兜底）→ 确认丢失（界面明确提示，而不是静默变空图）；
 * - 上传闸门（draftSync INV-D5）：还有图片正文没载入时绝不上传，载入完成后再统一冲刷。
 *
 * 不变量：
 * - INV-I1 仓库键 = (工作流, 内容键)，同一张图出现在不同工作流互不影响，清理也只作用于本工作流；
 * - INV-I2 清理只删「当前草稿没有引用」且「已超过宽限期」的记录；未载入正文的空壳图片也算引用（它的正文正躺在仓库里）；
 * - INV-I3 「确认丢失」只在仓库、云端图片存储与服务端草稿都成功查过且都没有时才下结论；任何一处暂时不可用都不算丢失，保持待载入并允许重试。
 */

import {
  computeImageKey,
  isImageUnloaded,
  MAX_INLINE_IMAGE_CHARS,
  type DraftImageLike,
  type MultiDraftLike,
} from './draftSync'

// ---------------------------------------------------------------------------
// 大图仓库
// ---------------------------------------------------------------------------

export interface DraftImageStore {
  /** 写入（同一「工作流 + 内容键」覆盖，并刷新时间戳）。 */
  put(workflowId: string, key: string, data: string): Promise<void>
  /** 批量读取；仓库里没有的键不会出现在结果里。 */
  getMany(workflowId: string, keys: string[]): Promise<Map<string, string>>
  /** 删除该工作流下不在 keep 里、且写入时间早于宽限期的记录，返回被删除的键。 */
  prune(workflowId: string, keep: ReadonlySet<string>, graceMs: number): Promise<string[]>
  /** 清理所有工作流中超过 maxAgeMs 的记录，返回删除条数。 */
  pruneOlderThan(maxAgeMs: number): Promise<number>
}

/** 内存实现：单测与「浏览器没有 IndexedDB」时的退化方案（后者等于没有本地仓库，只靠服务端兜底）。 */
export function createMemoryDraftImageStore(now: () => number = () => Date.now()): DraftImageStore & { size(): number } {
  const map = new Map<string, { workflowId: string; key: string; data: string; ts: number }>()
  const id = (workflowId: string, key: string) => `${workflowId}\u0001${key}`
  return {
    async put(workflowId, key, data) {
      map.set(id(workflowId, key), { workflowId, key, data, ts: now() })
    },
    async getMany(workflowId, keys) {
      const out = new Map<string, string>()
      for (const key of keys) {
        const hit = map.get(id(workflowId, key))
        if (hit) out.set(key, hit.data)
      }
      return out
    },
    async prune(workflowId, keep, graceMs) {
      const removed: string[] = []
      const t = now()
      for (const [k, rec] of map) {
        if (rec.workflowId !== workflowId || keep.has(rec.key)) continue
        if (t - rec.ts < graceMs) continue
        map.delete(k)
        removed.push(rec.key)
      }
      return removed
    },
    async pruneOlderThan(maxAgeMs) {
      let n = 0
      const t = now()
      for (const [k, rec] of map) {
        if (t - rec.ts >= maxAgeMs) {
          map.delete(k)
          n++
        }
      }
      return n
    },
    size: () => map.size,
  }
}

/** 需要进本地大图仓库的图片：有正文、有内容键、且大到本地副本放不下。 */
export function isStashableImage(img: DraftImageLike): img is DraftImageLike & { key: string; data: string } {
  return !!img.key && !!img.data && img.data.length >= MAX_INLINE_IMAGE_CHARS
}

/** 草稿里所有带内容键的图片（无论是否已载入正文）都视为被引用。 */
export function referencedImageKeys(state: MultiDraftLike): Set<string> {
  const refs = new Set<string>()
  for (const slot of state.drafts) {
    for (const img of slot.images || []) {
      if (img.key) refs.add(img.key)
    }
  }
  return refs
}

export interface DraftImageSyncOptions {
  /** 记录宽限期：刚写入的记录在此期间不会被清理（撤销删除、乱序等场景的安全边际），默认 5 分钟。 */
  graceMs?: number
  onError?: (error: unknown) => void
}

export interface DraftImageSync {
  /**
   * 图片刚加入时立即存进仓库，不等草稿去抖落盘（落盘与卸载之间的空窗里图片也不会丢）。
   * force：无视「已经存过」的记忆强制重写——载入链刚证明仓库里没有这张图（被浏览器清理 / 另一个标签页清掉了）时使用。
   */
  stash(workflowId: string, img: DraftImageLike, options?: { force?: boolean }): Promise<void>
  /** 草稿本地落盘之后调用：补存还没进仓库的大图，并在引用集合变化时清理失效记录。 */
  afterLocal(workflowId: string, state: MultiDraftLike): Promise<void>
}

/** 仓库同步器：记住「哪些键已经存进去了」，避免每次按键都重写几 MB 的图片。 */
export function createDraftImageSync(store: DraftImageStore, options: DraftImageSyncOptions = {}): DraftImageSync {
  const graceMs = options.graceMs ?? 5 * 60 * 1000
  const stored = new Map<string, Set<string>>()
  const lastRefs = new Map<string, string>()

  const storedOf = (workflowId: string) => {
    let set = stored.get(workflowId)
    if (!set) {
      set = new Set()
      stored.set(workflowId, set)
    }
    return set
  }

  async function putIfNeeded(workflowId: string, img: DraftImageLike, force = false): Promise<void> {
    if (!isStashableImage(img)) return
    const known = storedOf(workflowId)
    if (!force && known.has(img.key)) return
    // 先登记再写：并发的第二次调用不会重复写；写失败时撤销登记，下次还会重试
    known.add(img.key)
    try {
      await store.put(workflowId, img.key, img.data)
    } catch (error) {
      known.delete(img.key)
      options.onError?.(error)
    }
  }

  return {
    stash: (workflowId, img, stashOptions) => putIfNeeded(workflowId, img, stashOptions?.force === true),

    async afterLocal(workflowId, state) {
      for (const slot of state.drafts) {
        for (const img of slot.images || []) {
          await putIfNeeded(workflowId, img)
        }
      }
      const refs = referencedImageKeys(state)
      const signature = Array.from(refs).sort().join(',')
      if (lastRefs.get(workflowId) === signature) return
      try {
        const removed = await store.prune(workflowId, refs, graceMs)
        const known = storedOf(workflowId)
        for (const key of removed) known.delete(key)
        lastRefs.set(workflowId, signature)
      } catch (error) {
        options.onError?.(error)
      }
    },
  }
}

// ---------------------------------------------------------------------------
// 载入链
// ---------------------------------------------------------------------------

/** 收集草稿里所有「正文缺失」的图片对象（原对象引用，载入时就地补全）。 */
export function collectUnloadedImages(state: MultiDraftLike): DraftImageLike[] {
  const out: DraftImageLike[] = []
  for (const slot of state.drafts) {
    for (const img of slot.images || []) {
      if (isImageUnloaded(img)) out.push(img)
    }
  }
  return out
}

/** 旧草稿没有内容键：用元数据拼一个认图键（名称含毫秒时间戳，再加首部哈希与尺寸，实际唯一）。 */
function metaKey(img: DraftImageLike): string {
  return [img.name || '', img.format || '', img.hash || '', img.width || 0, img.height || 0].join('\u0001')
}

export interface RemoteImageIndex {
  byKey: Map<string, string>
  byMeta: Map<string, string>
}

/** 把服务端草稿里带正文的图片建成两个索引（内容键 / 元数据）；旧草稿没有内容键就现算一次。 */
export function indexRemoteImages(remote: MultiDraftLike | null): RemoteImageIndex {
  const index: RemoteImageIndex = { byKey: new Map(), byMeta: new Map() }
  if (!remote) return index
  for (const slot of remote.drafts || []) {
    for (const img of slot.images || []) {
      if (!img.data) continue
      index.byKey.set(img.key || computeImageKey(img.data), img.data)
      index.byMeta.set(metaKey(img), img.data)
    }
  }
  return index
}

export interface HydrateDeps {
  workflowId: string
  /** 本地大图仓库；浏览器没有 IndexedDB 时为 null。 */
  store: DraftImageStore | null
  /** 读取服务端草稿；服务端没有草稿返回 null；暂时不可用抛错（不能据此判定丢失，INV-I3）。 */
  fetchRemote: () => Promise<MultiDraftLike | null>
  /**
   * 读取服务器草稿图片存储里的一张图片（粘贴即上传的那份）；没有（404）返回 null，暂时不可用抛错。
   * 不提供则跳过这一步。命中的图片会被标记为 synced（正文已在云端，草稿 JSON 只需带引用）。
   */
  fetchCloud?: (key: string) => Promise<DraftImageLike | null>
  /** 每次 await 之后检查：返回 true 表示调用方已经切走，立即放弃。 */
  isStale?: () => boolean
  /** 云端 / 服务端草稿兜底成功补回正文的图片，回写本地仓库让下次直接命中。 */
  onRecovered?: (img: DraftImageLike) => void
}

export interface HydrateResult {
  /** 补回正文的图片数（仓库 + 服务端）。 */
  hydrated: number
  /** 确认丢失的图片数。 */
  lost: number
  /** 因服务端暂时不可用而仍待载入的图片数（可重试）。 */
  unavailable: number
  /** 调用方已切走，结果未应用。 */
  stale: boolean
}

/** 载入草稿里正文缺失的图片：本地仓库 → 服务端草稿 → 确认丢失。就地修改传入的图片对象。 */
export async function hydrateDraftImages(state: MultiDraftLike, deps: HydrateDeps): Promise<HydrateResult> {
  const result: HydrateResult = { hydrated: 0, lost: 0, unavailable: 0, stale: false }
  let missing = collectUnloadedImages(state)
  if (missing.length === 0) return result
  const stale = () => !!deps.isStale?.()

  // 1. 本地仓库
  if (deps.store) {
    const keys = Array.from(new Set(missing.map(img => img.key).filter((k): k is string => !!k)))
    if (keys.length > 0) {
      let found = new Map<string, string>()
      try {
        found = await deps.store.getMany(deps.workflowId, keys)
      } catch {
        // 仓库读取失败按「没有」处理，继续走服务端兜底
      }
      if (stale()) return { ...result, stale: true }
      for (const img of missing) {
        const data = img.key ? found.get(img.key) : undefined
        if (data) {
          img.data = data
          result.hydrated++
        }
      }
      missing = missing.filter(isImageUnloaded)
    }
  }
  if (missing.length === 0) return result

  // 2. 服务器草稿图片存储（粘贴即上传的那份：换设备、浏览器清了本地数据之后都靠它找回）
  //    云端暂时不可用的图片记下来：后面服务端草稿里也没有时只能算「待重试」，不能下「丢失」的结论（INV-I3）
  const cloudFailed = new Set<DraftImageLike>()
  if (deps.fetchCloud) {
    const fetchCloud = deps.fetchCloud
    const byKey = new Map<string, DraftImageLike[]>()
    for (const img of missing) {
      if (!img.key) continue
      const list = byKey.get(img.key)
      if (list) list.push(img)
      else byKey.set(img.key, [img])
    }
    await Promise.all(
      Array.from(byKey.entries()).map(async ([key, imgs]) => {
        try {
          const got = await fetchCloud(key)
          if (stale()) return
          if (got?.data) {
            for (const img of imgs) {
              img.data = got.data
              img.synced = true
              result.hydrated++
              deps.onRecovered?.(img)
            }
          }
        } catch {
          for (const img of imgs) cloudFailed.add(img)
        }
      }),
    )
    if (stale()) return { ...result, stale: true }
    missing = missing.filter(isImageUnloaded)
    if (missing.length === 0) return result
  }

  // 3. 服务端草稿（旧版本把正文内联在草稿 JSON 里的兜底）
  let remote: MultiDraftLike | null
  try {
    remote = await deps.fetchRemote()
  } catch {
    if (stale()) return { ...result, stale: true }
    result.unavailable = missing.length
    return result
  }
  if (stale()) return { ...result, stale: true }
  const index = indexRemoteImages(remote)
  for (const img of missing) {
    const data = img.key ? index.byKey.get(img.key) : index.byMeta.get(metaKey(img))
    if (data) {
      img.data = data
      if (!img.key) img.key = computeImageKey(data)
      result.hydrated++
      deps.onRecovered?.(img)
    } else if (cloudFailed.has(img)) {
      // 云端暂时读不到、服务端草稿里也没有：不能断定丢失，保持待载入并允许重试
      result.unavailable++
    } else {
      // 仓库、云端与服务端都成功查过且都没有：确认丢失
      img.lost = true
      result.lost++
    }
  }
  return result
}
