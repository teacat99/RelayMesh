// live 集合：浏览器里「保持附着」的终端会话。
//
// 每个附着的会话都要占一个 WebSocket 与一个 xterm 实例（含 5000 行回滚缓冲），开了十几个标签就会很重。
// 所以只让最近用过的几个保持附着，其余的先断开——会话仍在主机上运行，再点开时增量回放，用户几乎感觉不到。
// 本文件只有纯函数，便于单元测试；状态保存在 stores/exec.ts。

export const LIVE_CAP_DEFAULT = 4
export const LIVE_CAP_MIN = 2
export const LIVE_CAP_MAX = 8

/** 把任意输入收敛成合法的上限：非数字用默认值，越界夹到 2~8。 */
export function clampLiveCap(n: unknown): number {
  const v = typeof n === 'number' ? n : typeof n === 'string' && n.trim() !== '' ? Number(n) : NaN
  if (!Number.isFinite(v)) return LIVE_CAP_DEFAULT
  return Math.min(LIVE_CAP_MAX, Math.max(LIVE_CAP_MIN, Math.round(v)))
}

/** 超出上限时从最久未使用的开始淘汰；正在显示的（protectedKeys）不淘汰，此时允许暂时超出上限。 */
export function shrinkLive(order: readonly string[], protectedKeys: ReadonlySet<string>, cap: number): string[] {
  const next = [...order]
  for (let i = next.length - 1; i >= 0 && next.length > cap; i--) {
    if (!protectedKeys.has(next[i])) next.splice(i, 1)
  }
  return next
}

/** 使用了 key：移到最前（最近使用），再按上限淘汰。刚使用的 key 本身一定保留。 */
export function touchLive(order: readonly string[], key: string, protectedKeys: ReadonlySet<string>, cap: number): string[] {
  const keep = new Set(protectedKeys).add(key)
  return shrinkLive([key, ...order.filter(k => k !== key)], keep, cap)
}

/** 丢掉已经不存在的标签。 */
export function pruneLive(order: readonly string[], existing: ReadonlySet<string>): string[] {
  const kept = order.filter(k => existing.has(k))
  return kept.length === order.length ? (order as string[]) : kept
}

/** 被淘汰的 key（用于清理对应的连接状态）。 */
export function evictedKeys(before: readonly string[], after: readonly string[]): string[] {
  const keep = new Set(after)
  return before.filter(k => !keep.has(k))
}
