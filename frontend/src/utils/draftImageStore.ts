/**
 * 草稿大图仓库的 IndexedDB 实现（接口与载入链见 draftImages.ts）。
 *
 * 只做最薄的一层封装：每个操作一个事务，主键 (工作流, 内容键)，另有按工作流与时间的索引用于清理。
 * 浏览器没有 IndexedDB（隐私模式的个别实现、非常老的环境）时 createIdbDraftImageStore 返回 null，
 * 调用方退化为「只靠服务端草稿兜底」。
 */
import { createDraftImageSync, type DraftImageStore, type DraftImageSync } from './draftImages'

const DB_NAME = 'relaymesh-draft-images'
const DB_VERSION = 1
const STORE = 'images'

interface StoredImage {
  wf: string
  key: string
  data: string
  ts: number
}

function requestToPromise<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

export function createIdbDraftImageStore(factory: IDBFactory | undefined = globalThis.indexedDB): DraftImageStore | null {
  if (!factory) return null
  let dbPromise: Promise<IDBDatabase> | null = null

  function open(): Promise<IDBDatabase> {
    if (!dbPromise) {
      dbPromise = new Promise<IDBDatabase>((resolve, reject) => {
        const req = factory!.open(DB_NAME, DB_VERSION)
        req.onupgradeneeded = () => {
          const db = req.result
          if (!db.objectStoreNames.contains(STORE)) {
            const os = db.createObjectStore(STORE, { keyPath: ['wf', 'key'] })
            os.createIndex('wf', 'wf')
            os.createIndex('ts', 'ts')
          }
        }
        req.onsuccess = () => {
          const db = req.result
          // 其它标签页升级版本或浏览器回收连接时丢弃缓存的句柄，下次操作重新打开
          db.onversionchange = () => {
            db.close()
            dbPromise = null
          }
          db.onclose = () => {
            dbPromise = null
          }
          resolve(db)
        }
        req.onerror = () => reject(req.error)
        req.onblocked = () => reject(new Error('IndexedDB 被其它标签页阻塞'))
      }).catch(error => {
        dbPromise = null
        throw error
      })
    }
    return dbPromise
  }

  /** 在一个事务里执行 fn，事务提交成功后 resolve。 */
  async function run<T>(mode: IDBTransactionMode, fn: (os: IDBObjectStore) => Promise<T> | T): Promise<T> {
    const db = await open()
    return new Promise<T>((resolve, reject) => {
      let outcome: T
      const tx = db.transaction(STORE, mode)
      tx.oncomplete = () => resolve(outcome)
      tx.onerror = () => reject(tx.error)
      tx.onabort = () => reject(tx.error ?? new Error('IndexedDB 事务中止'))
      Promise.resolve(fn(tx.objectStore(STORE))).then(
        value => {
          outcome = value
        },
        error => {
          reject(error)
          try {
            tx.abort()
          } catch {
            // 事务可能已经结束
          }
        }
      )
    })
  }

  return {
    async put(workflowId, key, data) {
      const record: StoredImage = { wf: workflowId, key, data, ts: Date.now() }
      await run('readwrite', os => requestToPromise(os.put(record)))
    },

    async getMany(workflowId, keys) {
      const out = new Map<string, string>()
      if (keys.length === 0) return out
      await run('readonly', async os => {
        await Promise.all(
          keys.map(async key => {
            const rec = (await requestToPromise(os.get([workflowId, key]))) as StoredImage | undefined
            if (rec && typeof rec.data === 'string' && rec.data) out.set(key, rec.data)
          })
        )
      })
      return out
    },

    async prune(workflowId, keep, graceMs) {
      const removed: string[] = []
      const threshold = Date.now() - graceMs
      await run('readwrite', os =>
        new Promise<void>((resolve, reject) => {
          const req = os.index('wf').openCursor(IDBKeyRange.only(workflowId))
          req.onerror = () => reject(req.error)
          req.onsuccess = () => {
            const cursor = req.result
            if (!cursor) {
              resolve()
              return
            }
            const rec = cursor.value as StoredImage
            if (!keep.has(rec.key) && rec.ts <= threshold) {
              removed.push(rec.key)
              cursor.delete()
            }
            cursor.continue()
          }
        })
      )
      return removed
    },

    async pruneOlderThan(maxAgeMs) {
      let removed = 0
      const threshold = Date.now() - maxAgeMs
      await run('readwrite', os =>
        new Promise<void>((resolve, reject) => {
          const req = os.index('ts').openCursor(IDBKeyRange.upperBound(threshold))
          req.onerror = () => reject(req.error)
          req.onsuccess = () => {
            const cursor = req.result
            if (!cursor) {
              resolve()
              return
            }
            cursor.delete()
            removed++
            cursor.continue()
          }
        })
      )
      return removed
    },
  }
}

/** 超过这个时长没有被写入过的记录视为遗弃（工作流早已不用、草稿早已清空）。 */
const STALE_AFTER_MS = 30 * 24 * 60 * 60 * 1000

let shared: { store: DraftImageStore | null; sync: DraftImageSync | null } | null = null

/**
 * 整个页面共用一份仓库与同步器（同步器记着「哪些大图已经存进去」，多个输入区各建一份会重复写）。
 * 首次取用时顺手清理一次陈旧记录；浏览器没有 IndexedDB 时 store / sync 都是 null，调用方退化为只靠服务端草稿兜底。
 */
export function sharedDraftImages(): { store: DraftImageStore | null; sync: DraftImageSync | null } {
  if (!shared) {
    const store = createIdbDraftImageStore()
    shared = {
      store,
      sync: store ? createDraftImageSync(store, { onError: error => console.warn('草稿大图仓库异常：', error) }) : null,
    }
    store?.pruneOlderThan(STALE_AFTER_MS).catch(() => {})
  }
  return shared
}
