import { ref } from 'vue'
import { projectDocsApi } from '../api/projectDocs'

/**
 * 每个工作流一份「文档路径 → 标题」索引：正文里的 [[路径]] 靠它判断目标文档是否真的存在（不存在就不链接）。
 *
 * 懒加载（只有正文里出现了 [[ 才会请求）、按工作流缓存、同一工作流并发请求合并；缓存 60 秒后再用时刷新，
 * 查找没命中时最多每 15 秒补刷一次（智能体刚建好的文档能很快被链接上）。索引的内容真的变了才推进 docIndexVersion，
 * 装饰逻辑把它放进 signature，于是变化会触发一次幂等的重新装饰。
 */
interface Entry {
  /** 路径 → 标题 */
  paths: Map<string, string>
  at: number
  loading: boolean
}

const TTL_MS = 60_000
const MISS_TTL_MS = 15_000

const docs = new Map<string, Entry>()

/** 任何一个工作流的索引内容变了就加一；装饰的 signature 依赖它。 */
export const docIndexVersion = ref(0)

/** 这个工作流里有没有这篇文档（path 是规范化后的路径）；索引没加载好时一律当作没有。 */
export function lookupDoc(workflowId: string, path: string): { key: string; title?: string } | null {
  const hit = docs.get(workflowId)?.paths.get(path)
  if (hit === undefined) return null
  return hit ? { key: path, title: hit } : { key: path }
}

/** 索引是否已经有这个工作流的数据（哪怕是空的）。 */
export function docIndexReady(workflowId: string): boolean {
  const e = docs.get(workflowId)
  return !!e && e.at > 0
}

/**
 * 确保这个工作流的索引不太旧；超过 maxAgeMs 就在后台刷新（同一时刻只有一个请求）。失败也记下时间，
 * 免得每次渲染都重试；下一次过期后自然再试。
 */
export function ensureDocIndex(workflowId: string, maxAgeMs: number = TTL_MS): void {
  if (!workflowId) return
  let e = docs.get(workflowId)
  if (!e) {
    e = { paths: new Map(), at: 0, loading: false }
    docs.set(workflowId, e)
  }
  if (e.loading) return
  if (e.at > 0 && Date.now() - e.at < maxAgeMs) return
  const entry = e
  entry.loading = true
  projectDocsApi
    .list(workflowId)
    .then(res => {
      const next = new Map<string, string>()
      for (const d of res.items ?? []) next.set(d.path, d.title || '')
      const changed = next.size !== entry.paths.size || [...next.keys()].some(k => !entry.paths.has(k))
      entry.paths = next
      if (changed || entry.at === 0) docIndexVersion.value++
    })
    .catch(() => {
      /* 取不到就当作没有，下次过期后再试 */
    })
    .finally(() => {
      entry.at = Date.now()
      entry.loading = false
    })
}

/** 查找没命中时调用：索引已经有一阵子没刷新了就补刷一次（见文件头）。 */
export function noteDocMiss(workflowId: string): void {
  ensureDocIndex(workflowId, MISS_TTL_MS)
}
