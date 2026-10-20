// ExecMesh「归档」页的纯逻辑：把已归档的条目按主机分组、给出状态文字与汇总（第六批 #29）。
//
// 归档只决定侧栏默认显示不显示：不结束会话、不动记录与审计（见 model.TerminalArchive）。
// 侧栏里只有一个「已归档」筛选，且没有归档条目时它不出现，所以用户找不到归档的东西；
// 这个页面是常驻入口的落点：随时能进来，没有归档时也有说明，而不是一片空白。

import { endReasonLabel } from './record'
import { filterExecItems, type ExecItem } from './execList'

export interface ArchiveGroup {
  /** 机器码；模型线程统一归在 'model'。 */
  id: string
  label: string
  items: ExecItem[]
  /** 其中仍在运行的数量（归档不会结束会话，要让人知道它们还在跑）。 */
  running: number
  /** 这一组里最近的活动时间，用来给分组排序。 */
  lastActive: number
}

export const MODEL_GROUP_ID = 'model'
export const MODEL_GROUP_LABEL = '模型线程'

/** 已归档的条目（可按搜索词过滤；搜索规则与侧栏一致）。 */
export function archivedItems(items: ExecItem[], query = ''): ExecItem[] {
  return filterExecItems(items, 'archived', query)
}

/**
 * 按主机分组：最近有活动的主机在前，模型线程固定排最后；组内最近活动在前，时间相同按 key 保持稳定。
 * 同一台主机的人工会话归一组，主机名取条目自带的显示名。
 */
export function groupArchived(items: ExecItem[]): ArchiveGroup[] {
  const map = new Map<string, ArchiveGroup>()
  for (const it of items) {
    const id = it.model ? MODEL_GROUP_ID : it.code
    let g = map.get(id)
    if (!g) {
      g = { id, label: it.model ? MODEL_GROUP_LABEL : it.hostName || it.code, items: [], running: 0, lastActive: 0 }
      map.set(id, g)
    }
    g.items.push(it)
    if (it.running) g.running++
    if (it.lastActive > g.lastActive) g.lastActive = it.lastActive
  }
  const groups = [...map.values()]
  for (const g of groups) g.items.sort((a, b) => b.lastActive - a.lastActive || (a.key < b.key ? -1 : 1))
  groups.sort((a, b) => {
    const am = a.id === MODEL_GROUP_ID
    const bm = b.id === MODEL_GROUP_ID
    if (am !== bm) return am ? 1 : -1
    return b.lastActive - a.lastActive || (a.id < b.id ? -1 : 1)
  })
  return groups
}

/** 一条归档条目的状态文字：运行中（含主机离线）/ 已退出 / 结束原因 / 模型线程的摘要。 */
export function archiveStatusText(it: ExecItem): string {
  if (it.model) {
    const parts = [it.model.summary || `共 ${it.model.total} 次调用`]
    if (it.model.awaiting > 0) parts.push(`${it.model.awaiting} 个待批准`)
    return parts.join(' · ')
  }
  if (it.running) return it.hostOnline ? '运行中' : '运行中 · 主机离线'
  return it.live ? '已退出' : endReasonLabel(it.endedReason)
}

export interface ArchiveSummary {
  total: number
  running: number
  human: number
  model: number
}

export function archiveSummary(items: ExecItem[]): ArchiveSummary {
  const s: ArchiveSummary = { total: 0, running: 0, human: 0, model: 0 }
  for (const it of items) {
    s.total++
    if (it.running) s.running++
    if (it.model) s.model++
    else s.human++
  }
  return s
}
