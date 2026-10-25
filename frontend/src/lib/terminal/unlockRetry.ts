// 「是否需要二次解锁由服务端判断」的操作（例如令牌管理：只有授予 / 放宽终端能力才要求解锁）。
//
// 与 withUnlock 的区别：withUnlock 总是先要求已解锁；这里先直接执行，只有服务端明确回复 unlock_required
// 才弹出解锁，成功后重试一次。这样不涉及终端的令牌不会无故被要求输入密码。

export interface UnlockOnDemandDeps {
  /** 这个错误是不是「需要解锁」。 */
  needsUnlock: (e: unknown) => boolean
  /** 弹出解锁对话框；返回 false 表示用户取消。 */
  unlock: () => Promise<boolean>
  /** 服务端判定本地的解锁令牌无效时，清掉本地的解锁状态。 */
  stale: () => void
  /** 用户取消解锁时抛出的错误（调用方据此静默处理）。 */
  cancelled: () => Error
  /** 重试成功之后的回调（例如顺延本地的解锁空闲期）。 */
  done?: () => void
}

export async function withUnlockOnDemand<T>(fn: () => Promise<T>, deps: UnlockOnDemandDeps): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    if (!deps.needsUnlock(e)) throw e
    deps.stale()
    if (!(await deps.unlock())) throw deps.cancelled()
    const out = await fn()
    deps.done?.()
    return out
  }
}

export interface FreshUnlockDeps {
  /** 取出错误里的稳定错误码（没有则空串）。 */
  codeOf: (e: unknown) => string
  /** 强制弹出密码框（即使当前已经解锁）；返回 false 表示用户取消。 */
  reverify: () => Promise<boolean>
  /** 服务端判定本地的解锁令牌已经无效（unlock_required）时，清掉本地的解锁状态。 */
  stale: () => void
  /** 用户取消时抛出的错误（调用方据此静默处理）。 */
  cancelled: () => Error
  /** 成功之后的回调（例如顺延本地的解锁空闲期）。 */
  done?: () => void
}

/**
 * 授予「允许整个文件系统」这类高风险权限：服务端要求解锁令牌是「刚刚才用密码换来的」——
 * 已经解锁并不够，必须重新输入一次密码（错误码 unlock_fresh_required）。
 *
 * - force：调用方已经知道这次操作会授予该权限，先强制弹出密码框再发请求，不白挨一次 403；
 * - 无论是否 force，只要服务端回复 unlock_required / unlock_fresh_required，就弹出密码框并重试一次；
 *   重试仍失败则原样抛出（不循环）；用户取消时不重试，抛出取消错误。
 */
export async function withFreshUnlock<T>(fn: () => Promise<T>, deps: FreshUnlockDeps, opts: { force?: boolean } = {}): Promise<T> {
  if (opts.force && !(await deps.reverify())) throw deps.cancelled()
  try {
    const out = await fn()
    deps.done?.()
    return out
  } catch (e) {
    const code = deps.codeOf(e)
    if (code !== 'unlock_required' && code !== 'unlock_fresh_required') throw e
    if (code === 'unlock_required') deps.stale()
    if (!(await deps.reverify())) throw deps.cancelled()
    const out = await fn()
    deps.done?.()
    return out
  }
}

/**
 * 需要解锁的 API 路径前缀：终端接口，以及令牌管理（授予终端能力的令牌操作）、OAuth 的批准 / 策略，
 * 还有 OAuth 接入窗口与「刚登记的客户端」的管理（#239：开 / 续 / 关窗口、删除一条登记都要解锁）。
 * 漏掉任何一个前缀，对应的请求就不会带解锁令牌，服务端回 unlock_required、页面反复弹出密码框——新增需要解锁的接口时记得加在这里。
 */
const UNLOCK_AWARE_PREFIXES = ['/terminal/', '/credentials', '/oauth/requests', '/oauth/policy', '/oauth/window', '/oauth/clients']

/**
 * 只在「这个方法 + 这条路径」上才带解锁令牌的精确接口（不能整段加前缀，否则 /workflows/ 下所有读写请求都会带上令牌）：
 * - POST /workflows：建会话；带 parent_workflow_id 建分支时，协作 / 托管预设含「改参数」，服务端要求解锁（#236 P3）；
 *   普通建会话也会带上（服务端不看它，令牌只发往本站点）；
 * - PUT /workflows/<id>/branch：改分支向主会话开放的范围，放宽「改参数」档位或打开「启动」要解锁。
 */
const UNLOCK_AWARE_ROUTES: ReadonlyArray<{ method: string; path: RegExp }> = [
  { method: 'post', path: /^\/workflows\/?$/ },
  { method: 'put', path: /^\/workflows\/[^/]+\/branch\/?$/ },
]

/**
 * 这个请求是否应该带上解锁令牌（令牌只发往本站点自己的这几类接口）。method 不给时只按前缀判断；
 * 精确接口按「方法 + 路径」判断，路径里的查询串不参与。
 */
export function carriesUnlock(url: string | undefined, method?: string): boolean {
  const u = url ?? ''
  if (UNLOCK_AWARE_PREFIXES.some(p => u.startsWith(p))) return true
  const m = (method ?? '').toLowerCase()
  if (!m) return false
  const path = u.split('?')[0]
  return UNLOCK_AWARE_ROUTES.some(r => r.method === m && r.path.test(path))
}
