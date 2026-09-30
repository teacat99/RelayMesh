/**
 * 草稿落盘引擎（D-171 / P1.4）。
 *
 * 纯逻辑：定时器、本地存储、上传都通过参数注入，不依赖 Vue 与 DOM，可以直接在 Node 下单测。
 *
 * 它要解决的问题：
 * 1. 旧实现每次按键都同步 JSON.stringify + localStorage.setItem（含图草稿一次写入数十 KB～数百 KB），
 *    并且 400ms 去抖后无条件上传整份草稿；
 * 2. 旧实现在保存时用「当前」响应式状态推导写入哪个工作流，切换工作流的瞬间，旧工作流的输入内容会被写进
 *    新工作流的存储键，再被「载入」读回来，表现为草稿串到别的工作流并覆盖原有草稿。
 *
 * 不变量：
 * - INV-D1 写入的（工作流，内容）永远成对：touch 时把工作流和「读取该工作流内容」的回调绑在一起，冲刷时通过回调
 *   取回内容；回调返回 null 表示绑定已失效（组件已经切到别的工作流），此时丢弃而不是写错地方；
 * - INV-D2 不丢草稿：本地写入去抖 300ms（最长 3s），任何切换 / 隐藏 / 卸载都可以调用 flush 立即落盘；
 * - INV-D3 内容没有变化就不写、不传：以内容签名（忽略 updated_at）比较；
 * - INV-D4 同一工作流的上传串行，在途期间的新变化在其结束后再比较一次，总以最新内容为准，不会被乱序响应覆盖。
 */

export interface DraftImageLike {
  name?: string
  format?: string
  data_type?: string
  hash?: string
  width?: number
  height?: number
  data?: string
  url?: string
}

export interface DraftSlotLike {
  id: string
  text: string
  presets: string[]
  images: DraftImageLike[]
  updated_at: number
}

export interface MultiDraftLike {
  activeIndex: number
  drafts: DraftSlotLike[]
}

/** 53 位非加密字符串哈希（cyrb53），用于内容签名的紧凑比较与「已同步」标记。 */
export function hashString(input: string): string {
  let h1 = 0xdeadbeef
  let h2 = 0x41c6ce57
  for (let i = 0; i < input.length; i++) {
    const ch = input.charCodeAt(i)
    h1 = Math.imul(h1 ^ ch, 2654435761)
    h2 = Math.imul(h2 ^ ch, 1597334677)
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909)
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909)
  return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36)
}

const IMAGE_TAIL_SAMPLE = 48

/**
 * 单张图片的轻量指纹，不遍历 Base64 正文：草稿里的图片加入之后不会被就地修改，
 * 名称 + 首部哈希 + 总长度 + 尾部采样足以区分「换了一张图」。
 */
export function imageFingerprint(img: DraftImageLike): string {
  const data = img.data || ''
  return [img.name || '', img.format || '', img.hash || '', img.url || '', String(data.length), data.slice(-IMAGE_TAIL_SAMPLE)].join('\u0001')
}

/** 草稿内容签名：包含当前草稿序号、每个卡槽的文本 / 预设 / 图片指纹，不包含 updated_at。 */
export function draftSignature(state: MultiDraftLike): string {
  return JSON.stringify([
    state.activeIndex,
    state.drafts.map(slot => [slot.id, slot.text, slot.presets || [], (slot.images || []).map(imageFingerprint)]),
  ])
}

export function draftStateHash(state: MultiDraftLike): string {
  return hashString(draftSignature(state))
}

/** 小于该长度（字符数）的图片 Base64 才允许落进 localStorage，大图只在内存与服务端草稿里保留。 */
export const MAX_INLINE_IMAGE_CHARS = 100 * 1024

/** 写入 localStorage 前的瘦身：只保留必要字段，大图正文不落盘（杜绝 5MB 配额溢出与序列化卡顿）。 */
export function sanitizeDraftsForStorage(state: MultiDraftLike): MultiDraftLike {
  return {
    activeIndex: state.activeIndex,
    drafts: state.drafts.map(slot => ({
      id: slot.id,
      text: slot.text,
      presets: slot.presets,
      images: (slot.images || []).map(img => ({
        name: img.name,
        format: img.format,
        data_type: img.data_type,
        hash: img.hash,
        width: img.width,
        height: img.height,
        data: img.data && img.data.length < MAX_INLINE_IMAGE_CHARS ? img.data : '',
      })),
      updated_at: slot.updated_at,
    })),
  }
}

// ---------------------------------------------------------------------------
// 去抖器
// ---------------------------------------------------------------------------

interface Clock {
  now(): number
  setTimer(fn: () => void, ms: number): unknown
  clearTimer(handle: unknown): void
}

interface Debounced {
  poke(): void
  cancel(): void
}

/** 去抖 + 最长等待：持续不断的触发不会把执行无限推迟。 */
function createDebounced(run: () => void, delayMs: number, maxWaitMs: number, clock: Clock): Debounced {
  let timer: unknown = null
  let firstAt: number | null = null

  function fire() {
    if (timer !== null) {
      clock.clearTimer(timer)
      timer = null
    }
    firstAt = null
    run()
  }

  return {
    poke() {
      const t = clock.now()
      if (firstAt === null) firstAt = t
      if (timer !== null) {
        clock.clearTimer(timer)
        timer = null
      }
      const waited = t - firstAt
      if (waited >= maxWaitMs) {
        fire()
        return
      }
      timer = clock.setTimer(fire, Math.min(delayMs, maxWaitMs - waited))
    },
    cancel() {
      if (timer !== null) {
        clock.clearTimer(timer)
        timer = null
      }
      firstAt = null
    },
  }
}

// ---------------------------------------------------------------------------
// 落盘引擎
// ---------------------------------------------------------------------------

export interface DraftPersisterOptions {
  /** 写入本地存储（同步，允许抛错，例如配额已满）。 */
  writeLocal: (workflowId: string, json: string) => void
  /** 上传到服务端。 */
  upload: (workflowId: string, activeIndex: number, json: string) => Promise<unknown>
  /** 本地存储序列化，默认使用 sanitizeDraftsForStorage。 */
  serializeLocal?: (state: MultiDraftLike) => string
  /** 服务端序列化，默认完整 JSON。 */
  serializeRemote?: (state: MultiDraftLike) => string
  /** 上传成功后回调，携带该状态的哈希，用于持久化「已同步」标记。 */
  onUploaded?: (workflowId: string, stateHash: string) => void
  onError?: (kind: 'local' | 'remote', workflowId: string, error: unknown) => void
  localDelayMs?: number
  localMaxWaitMs?: number
  remoteDelayMs?: number
  remoteMaxWaitMs?: number
  now?: () => number
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (handle: unknown) => void
}

export interface MarkPersistedOptions {
  /** 本地存储中的内容与该状态一致（默认 true）。 */
  local?: boolean
  /** 服务端中的内容与该状态一致（例如刚从服务端载入）。 */
  remote?: boolean
  /** 之前持久化的「已同步」标记；与该状态哈希相同则视为服务端已一致。 */
  remoteSyncedHash?: string | null
}

export interface DraftPersister {
  /**
   * 标记某工作流的草稿有变化。read 在冲刷时才被调用，必须始终返回「该工作流」的最新内容；
   * 绑定失效时返回 null，本次冲刷会被丢弃（INV-D1）。
   */
  touch(workflowId: string, read: () => MultiDraftLike | null): void
  /** 立即冲刷：本地同步写入，服务端立即上传（受签名比较与串行约束）。省略 workflowId 则冲刷全部有待处理变化的工作流。 */
  flush(workflowId?: string): void
  /** 释放某工作流的条目（取消定时器，不写盘）；调用前应先 flush。 */
  release(workflowId: string): void
  /** 登记「存储中的内容已经与该状态一致」，避免载入之后多余的首次写入 / 上传。 */
  markPersisted(workflowId: string, state: MultiDraftLike, options?: MarkPersistedOptions): void
  hasPending(workflowId?: string): boolean
}

interface Entry {
  read: () => MultiDraftLike | null
  localDirty: boolean
  remoteDirty: boolean
  local: Debounced
  remote: Debounced
}

export function createDraftPersister(options: DraftPersisterOptions): DraftPersister {
  const serializeLocal = options.serializeLocal ?? ((state: MultiDraftLike) => JSON.stringify(sanitizeDraftsForStorage(state)))
  const serializeRemote = options.serializeRemote ?? ((state: MultiDraftLike) => JSON.stringify(state))
  const clock: Clock = {
    now: options.now ?? (() => Date.now()),
    setTimer: options.setTimer ?? ((fn, ms) => setTimeout(fn, ms)),
    clearTimer: options.clearTimer ?? (handle => clearTimeout(handle as ReturnType<typeof setTimeout>)),
  }
  const localDelay = options.localDelayMs ?? 300
  const localMaxWait = options.localMaxWaitMs ?? 3000
  const remoteDelay = options.remoteDelayMs ?? 1500
  const remoteMaxWait = options.remoteMaxWaitMs ?? 10_000

  const entries = new Map<string, Entry>()
  /** 各工作流最近一次成功写入本地 / 上传到服务端的内容哈希。 */
  const localHashes = new Map<string, string>()
  const remoteHashes = new Map<string, string>()
  /** 正在上传的工作流；值表示在途期间是否又收到了新的上传请求（需要在结束后再比较一次）。 */
  const remoteInflight = new Map<string, boolean>()

  function runLocal(id: string) {
    const entry = entries.get(id)
    if (!entry || !entry.localDirty) return
    entry.localDirty = false
    const state = entry.read()
    if (!state) return
    const hash = draftStateHash(state)
    if (localHashes.get(id) === hash) return
    try {
      options.writeLocal(id, serializeLocal(state))
      localHashes.set(id, hash)
    } catch (error) {
      entry.localDirty = true
      options.onError?.('local', id, error)
    }
  }

  function runRemote(id: string) {
    const entry = entries.get(id)
    if (!entry || !entry.remoteDirty) return
    if (remoteInflight.has(id)) {
      remoteInflight.set(id, true)
      return
    }
    entry.remoteDirty = false
    const state = entry.read()
    if (!state) return
    const hash = draftStateHash(state)
    if (remoteHashes.get(id) === hash) return

    let json: string
    try {
      json = serializeRemote(state)
    } catch (error) {
      entry.remoteDirty = true
      options.onError?.('remote', id, error)
      return
    }
    const activeIndex = state.activeIndex

    remoteInflight.set(id, false)
    void (async () => {
      try {
        await options.upload(id, activeIndex, json)
        remoteHashes.set(id, hash)
        try {
          options.onUploaded?.(id, hash)
        } catch {
          // 「已同步」标记写入失败不影响主流程
        }
      } catch (error) {
        const current = entries.get(id)
        if (current) current.remoteDirty = true
        options.onError?.('remote', id, error)
      } finally {
        const again = remoteInflight.get(id)
        remoteInflight.delete(id)
        if (again) runRemote(id)
      }
    })()
  }

  function ensure(id: string, read: () => MultiDraftLike | null): Entry {
    let entry = entries.get(id)
    if (!entry) {
      entry = {
        read,
        localDirty: false,
        remoteDirty: false,
        local: createDebounced(() => runLocal(id), localDelay, localMaxWait, clock),
        remote: createDebounced(() => runRemote(id), remoteDelay, remoteMaxWait, clock),
      }
      entries.set(id, entry)
    }
    entry.read = read
    return entry
  }

  return {
    touch(workflowId, read) {
      const entry = ensure(workflowId, read)
      entry.localDirty = true
      entry.remoteDirty = true
      entry.local.poke()
      entry.remote.poke()
    },

    flush(workflowId) {
      const ids = workflowId === undefined ? Array.from(entries.keys()) : [workflowId]
      for (const id of ids) {
        const entry = entries.get(id)
        if (!entry) continue
        entry.local.cancel()
        entry.remote.cancel()
        runLocal(id)
        runRemote(id)
      }
    },

    release(workflowId) {
      const entry = entries.get(workflowId)
      if (!entry) return
      entry.local.cancel()
      entry.remote.cancel()
      entries.delete(workflowId)
    },

    markPersisted(workflowId, state, markOptions = {}) {
      const hash = draftStateHash(state)
      if (markOptions.local !== false) localHashes.set(workflowId, hash)
      if (markOptions.remote === true || (markOptions.remoteSyncedHash != null && markOptions.remoteSyncedHash === hash)) {
        remoteHashes.set(workflowId, hash)
      }
    },

    hasPending(workflowId) {
      if (workflowId !== undefined) {
        const entry = entries.get(workflowId)
        return !!entry && (entry.localDirty || entry.remoteDirty)
      }
      for (const entry of entries.values()) {
        if (entry.localDirty || entry.remoteDirty) return true
      }
      return false
    },
  }
}
