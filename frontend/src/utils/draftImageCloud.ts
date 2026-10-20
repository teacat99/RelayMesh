/**
 * 草稿图片的云存储登记（反馈「草稿图片也纳入云存储」）。
 *
 * 纯逻辑：网络调用与重试等待都通过参数注入，不依赖 Vue 与 DOM，可以直接在 Node 下单测。
 *
 * 要解决的问题：草稿里的图片此前只存在于三个地方——内存、本机 IndexedDB、整份草稿 JSON（里面内联着 Base64）。
 * 换一台设备、清了浏览器数据、或者草稿 JSON 因图片太大而一直传不上去，图片就找不回来；
 * 提交时也要把同一份几 MB 的正文再发一次。现在图片一加入草稿就上传到服务器的草稿图片存储（以内容键为身份、幂等），
 * 草稿 JSON 里只留引用，提交时服务端直接复用，草稿销毁时对应图片一并删除。
 *
 * 不变量：
 * - INV-C1 身份 = (工作流, 内容键)：登记、上传、删除都按这一对区分，不同工作流里的同一张图互不影响；
 * - INV-C2 上传幂等且合并：同一对键同时只有一个有效的在途上传（重复调用拿到同一个 Promise），已上传的不重复传；
 * - INV-C3 可降级：上传失败（含重试用尽）时状态是 failed，调用方据此继续把正文内联在草稿 JSON / 提交请求里，图片不会因此丢失；
 * - INV-C4 释放优先：上传途中图片被删（release），上传结束后补一次删除并清掉登记，不留孤儿；
 *   之后若同一张图又被加回来（撤销删除），要等上一轮收尾再重新上传，不会被那次补删抹掉；
 * - INV-C5 删除只是尽力而为：失败不抛错、不阻塞界面，孤儿交给服务端 24 小时的后台清理兜底；
 * - INV-C6 「服务器上有没有」的事实只来自 list / put 成功 / get 命中，不凭猜测；网络错误既不算有也不算没有；
 *   list 返回期间发生过释放 / 消耗的键，不按这次 list 的（可能已过时的）结果登记。
 */

import type { DraftImageLike, MultiDraftLike } from './draftSync'

export type CloudImageState = 'uploading' | 'uploaded' | 'failed'

export interface CloudImageMeta {
  name?: string
  format?: string
  data_type?: string
  hash?: string
  width?: number
  height?: number
}

export interface CloudImageApi {
  /** 该工作流在服务器上已有的草稿图片键。 */
  list(workflowId: string): Promise<string[]>
  /** 上传（幂等：同一个键重复上传只刷新时间）。 */
  put(workflowId: string, key: string, image: CloudImageMeta & { data: string }): Promise<void>
  /** 读取一张图片；服务器上没有（404）返回 null，暂时不可用抛错。 */
  get(workflowId: string, key: string): Promise<DraftImageLike | null>
  /** 删除（幂等：不存在也算成功）。 */
  remove(workflowId: string, key: string): Promise<void>
}

export interface DraftImageCloudOptions {
  api: CloudImageApi
  /** 上传失败后各次重试前的等待（毫秒）；长度即最多重试次数，默认 [2000, 8000, 30000]。 */
  retryDelaysMs?: readonly number[]
  /** 判断错误是否「重试也没用」（例如超配额、格式不合法），默认 HTTP 400 / 401 / 403 / 413。 */
  isPermanentError?: (error: unknown) => boolean
  sleep?: (ms: number) => Promise<void>
  /** 登记变化通知：null 表示不再登记（已释放 / 已被提交消耗）。 */
  onChange?: (workflowId: string, key: string, state: CloudImageState | null) => void
  /** 上传 / 删除 / 列表出错；final 表示上传已放弃重试（状态转为 failed）。 */
  onError?: (workflowId: string, key: string, error: unknown, info: { op: 'put' | 'remove' | 'list'; final: boolean }) => void
}

export interface DraftImageCloud {
  state(workflowId: string, key: string): CloudImageState | null
  isUploaded(workflowId: string, key: string): boolean
  /** 草稿里是否还有正在上传的图片（草稿 JSON 要等它们上传完再带引用上传，否则会把正文内联进去）。 */
  hasUploading(workflowId: string, state: MultiDraftLike): boolean
  /** 读一次服务器上已有的图片键并登记为已上传；返回 false 表示读取失败（登记不变）。 */
  loadRemoteKeys(workflowId: string): Promise<boolean>
  /** 刚从云端取回了这张图片（get 命中），即证明它在服务器上：登记为已上传。正在上传 / 删除中的键不改。 */
  markUploaded(workflowId: string, key: string): void
  /** 确保这张图片在服务器上；缺键 / 缺正文（无法上传）直接返回当前登记。 */
  ensure(workflowId: string, img: DraftImageLike): Promise<CloudImageState | null>
  /** 并行确保整份草稿里的图片都在服务器上。 */
  ensureAll(workflowId: string, state: MultiDraftLike): Promise<void>
  /** 草稿不再引用这些图片：从服务器删除并清掉登记（尽力而为）。 */
  release(workflowId: string, keys: Iterable<string>): Promise<void>
  /** 服务端已经在提交时消耗了这些图片：只清掉本地登记，不再发删除请求。 */
  consumed(workflowId: string, keys: Iterable<string>): void
  /** 取回一张图片的正文（载入链使用）。 */
  fetch(workflowId: string, key: string): Promise<DraftImageLike | null>
}

const DEFAULT_RETRY_DELAYS_MS: readonly number[] = [2000, 8000, 30000]

function defaultIsPermanentError(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null | undefined)?.response?.status
  return status === 400 || status === 401 || status === 403 || status === 413
}

interface Running {
  task: Promise<CloudImageState | null>
  /** 这次上传开始时的 epoch；之后 epoch 变了说明它已被作废。 */
  epoch: number
}

export function createDraftImageCloud(options: DraftImageCloudOptions): DraftImageCloud {
  const api = options.api
  const delays = options.retryDelaysMs ?? DEFAULT_RETRY_DELAYS_MS
  const isPermanent = options.isPermanentError ?? defaultIsPermanentError
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms)))

  const states = new Map<string, CloudImageState>()
  const inflight = new Map<string, Running>()
  /** 在途的删除请求：同一个键重新上传之前必须等它结束。 */
  const removing = new Map<string, Promise<void>>()
  /** 每次 release / consumed 递增：在途上传与 list 结果据此发现自己已经过时。 */
  const epochs = new Map<string, number>()

  const idOf = (workflowId: string, key: string) => `${workflowId}\u0001${key}`
  const epochOf = (id: string) => epochs.get(id) ?? 0
  const bump = (id: string) => epochs.set(id, epochOf(id) + 1)

  function setState(workflowId: string, key: string, next: CloudImageState | null) {
    const id = idOf(workflowId, key)
    const prev = states.get(id) ?? null
    if (prev === next) return
    if (next === null) states.delete(id)
    else states.set(id, next)
    try {
      options.onChange?.(workflowId, key, next)
    } catch {
      // 通知方出错不影响登记本身
    }
  }

  async function removeQuietly(workflowId: string, key: string): Promise<void> {
    try {
      await api.remove(workflowId, key)
    } catch (error) {
      options.onError?.(workflowId, key, error, { op: 'remove', final: true })
    }
  }

  /** 发出删除并登记为「在途」，同一个键的删除按顺序串行。 */
  function removeTracked(workflowId: string, key: string): Promise<void> {
    const id = idOf(workflowId, key)
    const previous = removing.get(id) ?? Promise.resolve()
    const task: Promise<void> = previous
      .then(() => removeQuietly(workflowId, key))
      .finally(() => {
        if (removing.get(id) === task) removing.delete(id)
      })
    removing.set(id, task)
    return task
  }

  function upload(
    workflowId: string,
    key: string,
    image: CloudImageMeta & { data: string },
    epoch: number,
  ): Promise<CloudImageState | null> {
    const id = idOf(workflowId, key)
    setState(workflowId, key, 'uploading')
    return (async (): Promise<CloudImageState | null> => {
      for (let attempt = 0; ; attempt++) {
        try {
          await api.put(workflowId, key, image)
          if (epochOf(id) !== epoch) {
            // 上传期间图片已被释放：补一次删除，不留孤儿（INV-C4）
            await removeTracked(workflowId, key)
            setState(workflowId, key, null)
            return null
          }
          setState(workflowId, key, 'uploaded')
          return 'uploaded'
        } catch (error) {
          if (epochOf(id) !== epoch) {
            setState(workflowId, key, null)
            return null
          }
          const final = isPermanent(error) || attempt >= delays.length
          options.onError?.(workflowId, key, error, { op: 'put', final })
          if (final) {
            setState(workflowId, key, 'failed')
            return 'failed'
          }
          await sleep(delays[attempt])
          if (epochOf(id) !== epoch) {
            setState(workflowId, key, null)
            return null
          }
        }
      }
    })()
  }

  const cloud: DraftImageCloud = {
    state: (workflowId, key) => states.get(idOf(workflowId, key)) ?? null,

    isUploaded: (workflowId, key) => states.get(idOf(workflowId, key)) === 'uploaded',

    hasUploading(workflowId, state) {
      for (const slot of state.drafts) {
        for (const img of slot.images || []) {
          if (img.key && states.get(idOf(workflowId, img.key)) === 'uploading') return true
        }
      }
      return false
    },

    async loadRemoteKeys(workflowId) {
      const snapshot = new Map(epochs)
      let keys: string[]
      try {
        keys = await api.list(workflowId)
      } catch (error) {
        options.onError?.(workflowId, '', error, { op: 'list', final: true })
        return false
      }
      for (const key of keys) {
        const id = idOf(workflowId, key)
        if (epochOf(id) !== (snapshot.get(id) ?? 0)) continue // list 期间释放 / 消耗过：结果可能已过时（INV-C6）
        if (inflight.has(id) || removing.has(id)) continue
        if (states.get(id) !== 'uploaded') setState(workflowId, key, 'uploaded')
      }
      return true
    },

    markUploaded(workflowId, key) {
      const id = idOf(workflowId, key)
      if (inflight.has(id) || removing.has(id)) return
      if (states.get(id) !== 'uploaded') setState(workflowId, key, 'uploaded')
    },

    ensure(workflowId, img) {
      const key = img.key
      const data = img.data
      if (!key || !data || img.lost) return Promise.resolve(cloud.state(workflowId, key || ''))
      const id = idOf(workflowId, key)
      if (states.get(id) === 'uploaded') return Promise.resolve('uploaded')

      // 同一个键的删除还在途：等它结束再传，否则服务器上最终会被那次删除抹掉
      const removal = removing.get(id)
      if (removal) return removal.then(() => cloud.ensure(workflowId, img))

      const running = inflight.get(id)
      if (running) {
        if (running.epoch === epochOf(id)) return running.task
        // 在途的是一次已被作废的上传（期间释放过）：等它收尾（会补删），再重新上传（INV-C4）
        return running.task.then(() => cloud.ensure(workflowId, img))
      }

      const epoch = epochOf(id)
      const task: Promise<CloudImageState | null> = upload(
        workflowId,
        key,
        {
          name: img.name,
          format: img.format,
          data_type: img.data_type,
          hash: img.hash,
          width: img.width,
          height: img.height,
          data,
        },
        epoch,
      ).finally(() => {
        if (inflight.get(id)?.task === task) inflight.delete(id)
      })
      inflight.set(id, { task, epoch })
      return task
    },

    async ensureAll(workflowId, state) {
      const seen = new Set<string>()
      const jobs: Promise<unknown>[] = []
      for (const slot of state.drafts) {
        for (const img of slot.images || []) {
          if (!img.key || seen.has(img.key)) continue
          seen.add(img.key)
          jobs.push(cloud.ensure(workflowId, img))
        }
      }
      await Promise.all(jobs)
    },

    async release(workflowId, keys) {
      const unique = Array.from(new Set(keys))
      await Promise.all(
        unique.map(key => {
          const id = idOf(workflowId, key)
          bump(id)
          setState(workflowId, key, null)
          // 在途上传结束时会自己补删（INV-C4）；没有在途上传就直接删（服务器上可能有上一轮遗留的，删除幂等）
          if (inflight.has(id)) return Promise.resolve()
          return removeTracked(workflowId, key)
        }),
      )
    },

    consumed(workflowId, keys) {
      for (const key of keys) {
        bump(idOf(workflowId, key))
        setState(workflowId, key, null)
      }
    },

    fetch: (workflowId, key) => api.get(workflowId, key),
  }
  return cloud
}

// ---------------------------------------------------------------------------
// 草稿 JSON（上传服务端的格式）
// ---------------------------------------------------------------------------

/**
 * 草稿上传服务端时的 JSON：已经在图片存储里的图片只带引用（不带正文），
 * 「丢失」「已上传」这类只在内存里有意义的标记不上传。
 * 没有上传成功的图片继续内联正文，保证任何时候服务端那份草稿都能自己还原出图片（向后兼容 + 云端不可用时降级）。
 */
export function serializeDraftForServer(state: MultiDraftLike): string {
  return JSON.stringify({
    ...state,
    drafts: state.drafts.map(slot => ({
      ...slot,
      images: (slot.images || []).map(img => {
        const { synced, lost: _lost, ...rest } = img
        if (synced && img.key) {
          const { data: _data, ...ref } = rest
          return ref
        }
        return rest
      }),
    })),
  })
}

/** 「之前引用、之后不再引用」的图片键：只有它们才应该从服务器删除（同一张图可能还在别的卡槽里）。 */
export function unreferencedKeys(before: Iterable<string>, after: MultiDraftLike): string[] {
  const still = new Set<string>()
  for (const slot of after.drafts) {
    for (const img of slot.images || []) {
      if (img.key) still.add(img.key)
    }
  }
  const out: string[] = []
  const seen = new Set<string>()
  for (const key of before) {
    if (!key || still.has(key) || seen.has(key)) continue
    seen.add(key)
    out.push(key)
  }
  return out
}

/** 一组图片里的内容键（去重、保序）。 */
export function imageKeysOf(images: readonly DraftImageLike[] | undefined): string[] {
  const out: string[] = []
  for (const img of images || []) {
    if (img.key && !out.includes(img.key)) out.push(img.key)
  }
  return out
}
