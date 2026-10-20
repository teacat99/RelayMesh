// 文档跳转的纯逻辑（#131）：路由地址怎么拼、工作流所属项目怎么缓存。不依赖 api / 组件，能在 node:test 里直接跑；
// 真正发请求的那一层在 docNavClient.ts（所有入口都从那里拿 openDoc）。
//
// 单篇文档的阅读 / 编辑页搬进了项目区：工作流属于某个项目时，文档在 `/projects/:项目/docs/:工作流/:文档` 里打开
// （项目壳、「知识库」页签高亮、能返回知识库）；还没归入项目的工作流仍用老的 `/docs/:工作流/:文档` 整页。
// 所有入口（会话顶部、确认卡、决策卡、产物预览、知识库、模块页、文档页内部）都走同一个 openDoc，
// 不再各自写 `name: 'docs'`——「点文档跳到哪」只在这里改。
//
// 老链接不失效：路由上还有一道全局守卫（router/index.ts），`/docs/...` 一旦查到所属项目就替换成新地址。
import type { LocationQuery, RouteLocationRaw, Router } from 'vue-router'

export interface DocTarget {
  workflowId: string
  path: string
  /** 标题锚点（带不带 # 都行）。 */
  anchor?: string
  /** 已知所属项目时直接给（在项目页里点文档就是这种）；不给就按工作流查一次。 */
  projectId?: number
}

/** 路由参数取单个字符串（`:workflowId?` 可能是数组或 undefined）。 */
export function paramString(p: string | string[] | undefined | null): string {
  return (Array.isArray(p) ? p[0] : p) || ''
}

/** `:docRef*` / `:docRef+` 是可重复参数：数组按段拼回路径；每段做一次解码（解不开就原样用）。 */
export function docRefFromParam(p: string | string[] | undefined | null): string {
  const one = (x: string) => {
    try {
      return decodeURIComponent(x)
    } catch {
      return x
    }
  }
  if (Array.isArray(p)) return p.map(x => one(String(x))).join('/')
  return p ? one(String(p)) : ''
}

function hashOf(anchor?: string): string | undefined {
  const a = (anchor ?? '').replace(/^#/, '')
  return a ? `#${a}` : undefined
}

/** 纯函数：给定所属项目（> 0）走项目内的文档页，否则走老文档页。显式给 0 表示「没有项目」。 */
export function docLocation(t: DocTarget, projectId?: number): RouteLocationRaw {
  const pid = projectId ?? t.projectId ?? 0
  const hash = hashOf(t.anchor)
  if (pid > 0) {
    return { name: 'project-doc', params: { projectId: String(pid), workflowId: t.workflowId, docRef: t.path }, hash }
  }
  return { name: 'docs', params: { workflowId: t.workflowId, docRef: t.path }, hash }
}

/** 项目的知识库页（项目页默认页签）。 */
export function projectKbLocation(projectId: number): RouteLocationRaw {
  return { name: 'projects', params: { projectId: String(projectId) } }
}

/** 老文档页本身（不指定文档）：给还没归入项目的工作流留的入口，会话页头「项目」在没有项目时落到这里。 */
export function legacyDocsLocation(workflowId: string): RouteLocationRaw {
  return { name: 'docs', params: { workflowId } }
}

/**
 * 老地址 `/docs/:工作流/:文档*` 查到所属项目后的去向：带文档 → 项目内文档页（参数形状原样带过去，
 * 老链接里 `%2F` 与多段两种写法都照旧能用）；没带文档 → 项目的知识库。
 */
export function legacyDocRedirect(
  to: { params: Record<string, string | string[]>; query?: LocationQuery; hash?: string },
  projectId: number,
): RouteLocationRaw {
  const ref = to.params.docRef
  const hasDoc = Array.isArray(ref) ? ref.length > 0 : !!ref
  if (!hasDoc) return projectKbLocation(projectId)
  return {
    name: 'project-doc',
    params: { projectId: String(projectId), workflowId: paramString(to.params.workflowId), docRef: ref },
    query: to.query,
    hash: to.hash || undefined,
  }
}

// ---- 工作流 → 所属项目（0 = 没有归属） ----

export interface OwnerResolver {
  /** 所属项目编号；没有归属、查不到、接口不可用都是 0（退回老文档页，入口不会因此消失）。 */
  resolve(workflowId: string): Promise<number>
  /** resolve + 超时：接口迟迟不回就按「没有归属」处理，导航不会被它卡住。 */
  within(workflowId: string, ms?: number): Promise<number>
  /** 缓存里有、且没过期的归属；没有返回 undefined。 */
  peek(workflowId: string, now?: number): number | undefined
  /** 项目页改了目录归属 / 做了迁移之后调用：下一次重新查。 */
  invalidate(): void
}

/**
 * 短时缓存：同一个工作流连点几篇文档不重复请求，并发的请求合并成一次。
 * fetchOwner 返回项目编号（没有归属返回 0）；抛错按 0 处理且不进缓存，下一次还会再试。
 */
export function createOwnerResolver(fetchOwner: (workflowId: string) => Promise<number>, ttlMs = 30_000): OwnerResolver {
  const cache = new Map<string, { at: number; projectId: number }>()
  const inflight = new Map<string, Promise<number>>()

  function peek(workflowId: string, now = Date.now()): number | undefined {
    const hit = cache.get(workflowId.trim())
    return hit && now - hit.at < ttlMs ? hit.projectId : undefined
  }

  function resolve(workflowId: string): Promise<number> {
    const wf = workflowId.trim()
    if (!wf) return Promise.resolve(0)
    const cached = peek(wf)
    if (cached !== undefined) return Promise.resolve(cached)
    const pending = inflight.get(wf)
    if (pending) return pending
    const run = (async () => {
      try {
        const id = await fetchOwner(wf)
        const pid = Number.isFinite(id) && id > 0 ? id : 0
        cache.set(wf, { at: Date.now(), projectId: pid })
        return pid
      } catch {
        return 0
      } finally {
        inflight.delete(wf)
      }
    })()
    inflight.set(wf, run)
    return run
  }

  function within(workflowId: string, ms = 3000): Promise<number> {
    let timer: ReturnType<typeof setTimeout> | undefined
    const timeout = new Promise<number>(r => {
      timer = setTimeout(() => r(0), ms)
    })
    return Promise.race([resolve(workflowId), timeout]).finally(() => clearTimeout(timer))
  }

  return { resolve, within, peek, invalidate: () => cache.clear() }
}

// ---- 失败后按退避补查（#208） ----

/** 页头补查的间隔：前几次快，之后放慢；全部用完约 2 分钟，仍不通就放弃（点「项目」时还会再查一次）。 */
export const PATIENT_RETRY_DELAYS_MS: readonly number[] = [2_000, 5_000, 15_000, 30_000, 60_000]

export interface PatientOptions {
  /** 每次重试之间的等待（毫秒）；默认 PATIENT_RETRY_DELAYS_MS，数组长度 = 最多重试几次。 */
  delaysMs?: readonly number[]
  /** 等待之后、重试之前调用：返回 false 就不再试了（页头已切到别的工作流 / 已卸载）。 */
  stillWanted?: () => boolean
  /** 等待的实现，测试里换成立即返回。 */
  sleep?: (ms: number) => Promise<void>
}

/**
 * 先试一次，失败（返回 false 或抛错）就按间隔再试，直到成功、被放弃或用完次数。
 * 返回最后一次是否成功。浏览器在网络瞬断时会把在途请求一起掐掉，只试一次的入口就会一直缺着。
 */
export async function retryWithBackoff(attempt: () => Promise<boolean>, opts: PatientOptions = {}): Promise<boolean> {
  const delays = opts.delaysMs ?? PATIENT_RETRY_DELAYS_MS
  const wanted = opts.stillWanted ?? (() => true)
  const sleep = opts.sleep ?? ((ms: number) => new Promise<void>(r => setTimeout(r, ms)))
  const once = async () => {
    try {
      return await attempt()
    } catch {
      return false
    }
  }
  if (await once()) return true
  for (const ms of delays) {
    await sleep(ms)
    if (!wanted()) return false
    if (await once()) return true
  }
  return false
}

/**
 * 查工作流的所属项目，接口失败就补查。
 * resolve 在「没有归属」时返回 0 并进缓存、在「接口失败」时也返回 0 但不进缓存——两者只能靠 peek 区分：
 * 缓存里有值才算查到了（包括查到的是 0），没有就是失败，需要再试。用完次数仍失败返回 0。
 */
export async function resolveOwnerPatiently(owners: OwnerResolver, workflowId: string, opts: PatientOptions = {}): Promise<number> {
  const wf = workflowId.trim()
  if (!wf) return 0
  await retryWithBackoff(async () => {
    await owners.resolve(wf)
    return owners.peek(wf) !== undefined
  }, opts)
  return owners.peek(wf) ?? 0
}

/** 打开一篇文档（所有入口共用；入口们拿到的是 docNavClient 里绑好真实接口的 openDoc）。 */
export async function openDocWith(router: Router, owners: OwnerResolver, t: DocTarget, opts: { replace?: boolean } = {}): Promise<void> {
  const pid = t.projectId && t.projectId > 0 ? t.projectId : await owners.within(t.workflowId)
  const loc = docLocation(t, pid)
  if (opts.replace) await router.replace(loc)
  else await router.push(loc)
}
