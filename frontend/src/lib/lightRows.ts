import type { FeedbackSession, SessionBody } from '../api/types'

// 轻量行（#169 第二期「索引优先 + 正文按需」）的纯函数。
//
// 预览视图取工作流会话表时带 ?light=1：行集合与顺序不变，较早的行只带首行预览并标 body_omitted，完整正文用
// GET /workflows/{id}/sessions/bodies 按 id 补。这里是「谁需要补、补回来怎么合、再取表时已经补好的怎么不丢」的规则，
// 不碰网络与响应式状态（store 负责），便于单测。

/** 单次取正文的 id 数（服务端上限 50，留余量）。 */
export const BODY_CHUNK = 40

/** 是不是轻量行：正文只是首行预览，页面不能当正文渲染。 */
export function isLightRow(s: Pick<FeedbackSession, 'body_omitted'> | null | undefined): boolean {
  return s?.body_omitted === true
}

export function hasLightRows(rows: readonly FeedbackSession[] | undefined): boolean {
  return !!rows && rows.some(isLightRow)
}

function timeOf(iso: string | undefined): number {
  return iso ? Date.parse(iso) : Number.NaN
}

/**
 * 刚取到的整表里的轻量行：本地已有同一行的完整正文、且版本相同（updated_at 相等）时，把本地的正文接回来。
 * 否则「再取一次表」（重连、切回标签页、强制刷新）会把已经取过的正文又丢掉，页面上已展开的轮次变回骨架。
 * 版本不同（本行在服务端已更新）就保持轻量，正文之后按需重取。非轻量的行原样。
 * 没有可接的行时返回传入的同一个数组。
 */
export function preserveFullBodies(local: readonly FeedbackSession[] | undefined, fetched: FeedbackSession[]): FeedbackSession[] {
  if (!local || local.length === 0 || !fetched.some(isLightRow)) return fetched
  const full = new Map<string, FeedbackSession>()
  for (const row of local) if (!isLightRow(row)) full.set(row.session_id, row)
  if (full.size === 0) return fetched
  let changed = false
  const out = fetched.map(row => {
    if (!isLightRow(row)) return row
    const mine = full.get(row.session_id)
    if (!mine || mine.updated_at !== row.updated_at) return row
    changed = true
    const next: FeedbackSession = {
      ...row,
      summary: mine.summary,
      response_text: mine.response_text,
      user_messages: mine.user_messages,
      images: mine.images,
    }
    delete next.body_omitted
    return next
  })
  return changed ? out : fetched
}

/**
 * 把取回的正文填进缓存里的轻量行，返回新数组；没有任何行被填时返回传入的同一个数组（不触发多余的响应式更新）。
 * 只填仍是轻量的行（已经是完整行的不动：它可能刚被 SSE 补丁更新过，比这次取回的更新）；
 * 取回的正文比缓存里这行的版本旧（updated_at 更早）也不填。取回的正文更新时一并采用它的 updated_at，
 * 否则这行会永远「版本对不上」：之后每次再取表都会把它判成需要重取。
 */
export function applyBodies(rows: readonly FeedbackSession[], bodies: readonly SessionBody[]): FeedbackSession[] {
  if (bodies.length === 0) return rows as FeedbackSession[]
  const byId = new Map<string, SessionBody>()
  for (const b of bodies) byId.set(b.session_id, b)
  let changed = false
  const out = rows.map(row => {
    const body = byId.get(row.session_id)
    if (!body || !isLightRow(row)) return row
    const have = timeOf(row.updated_at)
    const got = timeOf(body.updated_at)
    if (!Number.isNaN(have) && !Number.isNaN(got) && got < have) return row
    changed = true
    const next: FeedbackSession = {
      ...row,
      summary: body.summary,
      response_text: body.response_text ?? '',
      user_messages: body.user_messages ?? [],
      images: body.images ?? [],
      updated_at: body.updated_at || row.updated_at,
    }
    delete next.body_omitted
    return next
  })
  return changed ? out : (rows as FeedbackSession[])
}

/**
 * 某一行之后（按表里的顺序，不含它自己）所有轻量行的 id。afterId 为 null 或表里找不到它 = 从头开始。
 * 预览视图的窗口要显示「第 k 轮到最后一轮」时，要的就是「第 k-1 轮之后」的全部行——包括落在间隙里的通知。
 */
export function lightIdsAfter(rows: readonly FeedbackSession[], afterId: string | null): string[] {
  let start = 0
  if (afterId) {
    const at = rows.findIndex(r => r.session_id === afterId)
    if (at >= 0) start = at + 1
  }
  const ids: string[] = []
  for (let i = start; i < rows.length; i++) if (isLightRow(rows[i])) ids.push(rows[i].session_id)
  return ids
}

/** 把 items 切成每批不超过 size 的若干批。 */
export function chunk<T>(items: readonly T[], size: number): T[][] {
  const out: T[][] = []
  const n = Math.max(1, Math.floor(size))
  for (let i = 0; i < items.length; i += n) out.push(items.slice(i, i + n))
  return out
}
