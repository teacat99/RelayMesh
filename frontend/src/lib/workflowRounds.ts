// 各工作流「一共有多少轮」（#173）。
//
// 会话列表接口只带最新一批会话行，每个工作流在列表里往往只有几行、甚至只有一行（较早的工作流）；
// 侧栏会话卡的「N 轮」徽标与归档库的「共 N 轮交互」若拿列表里的行数凑，就会写得比实际少很多。
// 所以服务端在每一行上顺带给出整个工作流的总行数（workflow_rounds，含已归档），这里按工作流存一份。
//
// 单独存而不挂在会话行上，理由与 workflowAggregates 相同：行会被各路事件按行补丁、被列表整体替换，
// 总轮数是工作流级的，不该跟着行的生灭。这里全是纯函数，store 与界面只负责接线。

/** 键是工作流编号，值是该工作流在库里的总行数。 */
export type WorkflowRounds = Record<string, number>

/** 会话行里与总轮数有关的字段（FeedbackSession 的子集，便于测试）。 */
export interface RoundsRow {
  workflow_id?: string
  workflow_rounds?: number
}

/**
 * 把一批会话行里的总轮数并进已有的表；没有变化返回 null（调用方就不必替换响应式对象）。
 * - 没有工作流编号、或没带 workflow_rounds 的行（旧服务端 / 聚合查询失败 / 事件补丁出来的行）跳过，保留手上已有的；
 * - 带了就以服务端为准直接覆盖（总数可能因清理而变小，不能只增不减）；同一批里同一个工作流的各行取值一致，取第一行即可。
 */
export function mergeWorkflowRounds(prev: WorkflowRounds, rows: readonly RoundsRow[]): WorkflowRounds | null {
  let next: WorkflowRounds | null = null
  const seen = new Set<string>()
  for (const row of rows) {
    const wf = row.workflow_id
    const n = row.workflow_rounds
    if (!wf || seen.has(wf) || typeof n !== 'number' || !Number.isFinite(n) || n < 0) continue
    seen.add(wf)
    const rounded = Math.floor(n)
    if ((next ?? prev)[wf] === rounded) continue
    if (!next) next = { ...prev }
    next[wf] = rounded
  }
  return next
}

/**
 * 一个工作流要显示的轮数：服务端给的总数与列表里实际带出的行数取较大者。
 * 总数不会比已经在列表里的行数少（少了说明总数是更早的快照，刚发生的新一轮还没进去）；
 * 没有总数（旧服务端 / 聚合失败 / 没有工作流编号的老会话）就按列表里的行数显示，与以前一致。
 */
export function displayRounds(total: number | undefined, listed: number): number {
  if (typeof total !== 'number' || !Number.isFinite(total)) return listed
  return Math.max(Math.floor(total), listed)
}
