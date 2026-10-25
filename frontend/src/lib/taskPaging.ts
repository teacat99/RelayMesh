// 托管自驾工单列表一页一页取完（#250）。
//
// 服务端 GET /api/v1/tasks 按工单编号（task- 加随机十六进制）升序、默认每页 20 条、最多 100 条，翻页走 next_cursor。
// 以前页面只取第一页（不带 limit / cursor），超过 20 个工单之后显示哪些取决于随机编号——新建的工单有一定概率不出现在侧栏「托管自驾」里。
// 这里把翻页收成一个纯函数：按页取、直到没有 next_cursor 为止；翻页数设上限（异常数据不会无限循环，游标原地不动也停）；
// 取的途中已经有更新的一次取数发出，就放弃这一轮（返回 null，调用方不要用它覆盖列表）。
//
// 翻页要拿得全，服务端的 next_cursor 也得对：它必须是「本页最后一条」的编号（下一页的条件是 id > cursor）。
// 服务端以前给的是「下一页第一条」，那条会被自己跳过，每个页界漏一条——同一处修在 internal/store/tasks.go，
// 守卫是 internal/store/tasks_list_test.go，浏览器里的整条链路由 e2e task-paging.mjs 守着。

/** 每页取多少：服务端的上限（model.MaxPageSize）。 */
export const TASK_PAGE_SIZE = 100
/** 最多翻几页（20 × 100 = 2000 个工单）：超过这个量是异常数据，宁可截断也不无限翻。 */
export const TASK_MAX_PAGES = 20

export interface PageOf<T> {
  tasks?: T[] | null
  next_cursor?: string | null
}

/**
 * 翻完所有页，按服务端给的顺序拼起来。
 * @param fetchPage 取一页；第一页的游标是空串
 * @param opts.maxPages 翻页数上限，默认 TASK_MAX_PAGES
 * @param opts.isStale 每取回一页检查一次，为真就放弃这一轮（有更新的取数在后面）
 * @returns 全部条目；被放弃时是 null
 */
export async function collectAllPages<T>(
  fetchPage: (cursor: string) => Promise<PageOf<T>>,
  opts: { maxPages?: number; isStale?: () => boolean } = {},
): Promise<T[] | null> {
  const maxPages = Math.max(1, opts.maxPages ?? TASK_MAX_PAGES)
  const out: T[] = []
  let cursor = ''
  for (let i = 0; i < maxPages; i++) {
    const page = await fetchPage(cursor)
    if (opts.isStale?.()) return null
    out.push(...(page.tasks ?? []))
    const next = page.next_cursor ?? ''
    if (!next || next === cursor) break
    cursor = next
  }
  return out
}
