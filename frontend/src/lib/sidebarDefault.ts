// 侧栏默认收起 / 展开（#190：stdio 直驱）。
//
// 规则：用户手动选过就一律尊重；没选过时，只有「stdio 直驱」才介入——stdio 是由 AI 客户端拉起的本机进程，
// 通常只有一个窗口（一个工作流），侧栏只会占地方，所以工作流 ≤ 1 个时默认收起；出现第二个工作流
// （例如开了第二个窗口）就自动展开。常驻服务（serve）与以前完全一样。
// 这里只算「当前该不该收起」，不落盘：自动状态一落盘就变成了「手动选择」，之后再也不会跟着工作流数量变。

/** 用户对侧栏的手动选择：true 收起 / false 展开 / null 没选过。 */
export type SidebarPref = boolean | null

export type RunMode = 'serve' | 'stdio'

/** 手动选择存在 localStorage 里的键（与旧版本相同，已有选择原样保留）。 */
export const SIDEBAR_PREF_KEY = 'relaymesh_sidebar_collapsed'

/** 最近一次从服务端看到的运行方式；用来让刷新页面时第一帧就按对的布局画，避免先展开再收起的闪动。 */
export const RUN_MODE_KEY = 'relaymesh_run_mode'

export function parseSidebarPref(raw: string | null | undefined): SidebarPref {
  if (raw === 'true') return true
  if (raw === 'false') return false
  return null
}

export function parseRunMode(raw: string | null | undefined): RunMode {
  return raw === 'stdio' ? 'stdio' : 'serve'
}

export interface SidebarSessionLike {
  session_id: string
  workflow_id?: string
  status?: string
}

/** 侧栏日常列表里的工作流个数：与侧栏一致，按 workflow_id（没有就按 session_id）聚合，已归档的不算。 */
export function countVisibleWorkflows(sessions: ReadonlyArray<SidebarSessionLike>): number {
  const keys = new Set<string>()
  for (const s of sessions) {
    if (s.status === 'archived') continue
    keys.add(s.workflow_id || s.session_id)
  }
  return keys.size
}

export interface SidebarDefaultInput {
  runMode: RunMode
  pref: SidebarPref
  workflowCount: number
}

/** 侧栏现在是否收起：手动选择优先；没选过时 stdio 在工作流 ≤ 1 个时收起，其余一律展开。 */
export function effectiveSidebarCollapsed(input: SidebarDefaultInput): boolean {
  if (input.pref !== null) return input.pref
  if (input.runMode === 'stdio') return input.workflowCount <= 1
  return false
}
