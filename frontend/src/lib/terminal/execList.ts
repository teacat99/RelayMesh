// ExecMesh 侧栏的会话清单：把「主机上的会话」与「已结束会话的历史索引」合成一张按活动时间排列的清单（纯函数）。
// 人工终端和（之后的）模型线程都是这张清单里的普通条目，点开后右侧才显示详细内容。

import type { HistoryEntry, HostView, SessionInfo, ThreadView } from '../../api/terminal'
import { hostTitle, sessionTitle } from './format'
import { threadSubtitle, threadTitle } from './execRuns'

export type ExecKind = 'human' | 'model'

/** 模型线程条目独有的信息（人工会话没有）。 */
export interface ModelInfo {
  /** 线程键（`/exec/r/:key`）。 */
  threadKey: string
  credential: string
  thread: string
  workflowId: string
  /** 进行中（含待批准）与其中待批准的数量。 */
  active: number
  awaiting: number
  total: number
  /** 最近一次运行的标题与状态。 */
  lastTitle: string
  lastState: string
  /** 第二行文字里“线程标签 / 数量 / 最近”的拼接（不含主机）。 */
  summary: string
}

export interface ExecItem {
  /** `${机器码}:${会话 ID}`，清单内唯一。 */
  key: string
  kind: ExecKind
  code: string
  sid: string
  /** 显示名：用户起的名字，没有则是「会话 + 序号后缀」。 */
  title: string
  /** 用户起的名字原文（重命名时作为初始值）。 */
  name: string
  hostName: string
  hostOnline: boolean
  /** 仍在主机的会话列表里（含「已退出但尚未清除」的）。 */
  live: boolean
  running: boolean
  attached: number
  rec?: string
  idle: number
  idleOwn: boolean
  /** 排序与分组用的最近活动时间（毫秒）。 */
  lastActive: number
  endedReason?: string
  exitCode?: number
  pinned: boolean
  /** 已归档：默认不在侧栏显示，只出现在「已归档」筛选里（会话本身不受影响）。 */
  archived: boolean
  /** 历史条目：记录里有内容可以回放。 */
  replayable: boolean
  /** 模型线程（kind = model）才有。 */
  model?: ModelInfo
}

export type ExecFilter = 'all' | 'human' | 'model' | 'running' | 'awaiting' | 'ended' | 'archived'

export const EXEC_FILTERS: { id: ExecFilter; label: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'human', label: '人工' },
  { id: 'model', label: '模型' },
  { id: 'running', label: '运行中' },
  { id: 'awaiting', label: '待批准' },
  { id: 'ended', label: '已结束' },
  { id: 'archived', label: '已归档' },
]

export function itemKey(code: string, sid: string): string {
  return `${code}:${sid}`
}

/** 模型线程在清单里的 key（和人工会话的 `机器码:会话 ID` 不会冲突）。 */
export function threadItemKey(threadKey: string): string {
  return `r:${threadKey}`
}

function parseTime(iso: string | undefined): number {
  if (!iso) return 0
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : 0
}

/** 历史条目里是否有可回放的内容。 */
export function historyReplayable(e: Pick<HistoryEntry, 'rec' | 'rec_purged' | 'rec_bytes'>): boolean {
  if (e.rec_purged) return false
  if (e.rec === 'incognito' || e.rec === 'off' || e.rec === 'veto') return (e.rec_bytes ?? 0) > 0
  return true
}

function liveActive(s: SessionInfo, ended: HistoryEntry | undefined): number {
  if (s.state === 'running' && s.active && s.active > 0) return s.active * 1000
  if (s.state !== 'running') {
    const t = parseTime(ended?.ended_at)
    if (t) return t
  }
  if (s.active && s.active > 0) return s.active * 1000
  return (s.created ?? 0) * 1000
}

export interface BuildInput {
  hosts: HostView[]
  history: Record<string, { sessions: HistoryEntry[] } | undefined>
  pins: ReadonlySet<string>
  /** 已归档条目的 key（和 ExecItem.key 同一套）；来自 Hub，多台设备共享。 */
  archived?: ReadonlySet<string>
  /**
   * 每台主机最近一次在线时的运行中会话。主机离线时 Hub 看不到它的会话，但会话仍在主机上运行：
   * 用这份记录让它们继续留在清单里（标注「主机离线」），而不是一断线就全部消失。
   */
  lastKnown?: Record<string, SessionInfo[] | undefined>
  /** 模型线程汇总（来自 Hub）；只显示已启用主机上的线程。 */
  threads?: ThreadView[]
  /** 用户给线程起的名字（线程键 → 名称，只存在本浏览器里，不影响线程键）。 */
  threadNames?: Record<string, string | undefined>
}

/**
 * 清单第二行里的“主机”。经网关打开的 SSH 会话运行在网关主机上，但人想看到的是它连到了哪台机器：
 * 活动会话带着目标主机的机器码（`target`），显示目标的名字并注明经由哪台网关；
 * 已结束的历史条目只记得“这是 SSH 会话”（shell = ssh），或者目标主机已被删除时，退回「SSH · 经 网关」。
 * Hub 直连的 SSH 主机自己就持有会话（承载者本身是 SSH 主机，没有 target）：直接显示这台主机的名字。
 */
export function sessionHostName(
  gateway: Pick<HostView, 'machine_code' | 'remark' | 'hostname'> & { kind?: string },
  target: string | undefined,
  shell: string | undefined,
  lookup: (code: string) => Pick<HostView, 'machine_code' | 'remark' | 'hostname'> | undefined,
): string {
  const via = hostTitle(gateway)
  if (gateway.kind === 'ssh') return via
  if (target) {
    const t = lookup(target)
    return t ? `${hostTitle(t)} · 经 ${via}` : `SSH · 经 ${via}`
  }
  return shell === 'ssh' ? `SSH · 经 ${via}` : via
}

/** 合成清单（未排序）。只包含已启用的主机。 */
export function buildExecItems(input: BuildInput): ExecItem[] {
  const out: ExecItem[] = []
  const byCode = new Map(input.hosts.map(h => [h.machine_code, h]))
  const lookup = (code: string) => byCode.get(code)
  for (const h of input.hosts) {
    if (h.status !== 'enabled') continue
    const past = input.history[h.machine_code]?.sessions ?? []
    const pastBySid = new Map(past.map(e => [e.sid, e]))
    const live = new Set<string>()
    // 主机离线时用「最近一次在线时的会话」顶上，因为会话其实还在主机上运行；SSH 主机不适用——直连的会话在 Hub 里，
    // 连接断了它们就结束了，用旧记录顶上只会留下已经不存在的幽灵条目。
    const sessions = !h.online && h.sessions.length === 0 && h.kind !== 'ssh' ? (input.lastKnown?.[h.machine_code] ?? []) : h.sessions
    for (const s of sessions) {
      live.add(s.sid)
      const key = itemKey(h.machine_code, s.sid)
      out.push({
        key,
        kind: 'human',
        code: h.machine_code,
        sid: s.sid,
        title: sessionTitle(s),
        name: (s.name ?? '').trim(),
        hostName: sessionHostName(h, s.target, s.shell, lookup),
        hostOnline: h.online,
        live: true,
        running: s.state === 'running',
        attached: s.attached,
        rec: s.rec,
        idle: s.idle ?? 0,
        idleOwn: !!s.idle_own,
        lastActive: liveActive(s, pastBySid.get(s.sid)),
        endedReason: pastBySid.get(s.sid)?.ended_reason,
        exitCode: s.exit_code,
        pinned: input.pins.has(key),
        archived: input.archived?.has(key) ?? false,
        replayable: false,
      })
    }
    for (const e of past) {
      if (live.has(e.sid)) continue
      const key = itemKey(h.machine_code, e.sid)
      out.push({
        key,
        kind: 'human',
        code: h.machine_code,
        sid: e.sid,
        title: sessionTitle(e),
        name: (e.name ?? '').trim(),
        hostName: sessionHostName(h, undefined, e.shell, lookup),
        hostOnline: h.online,
        live: false,
        running: false,
        attached: 0,
        rec: e.rec,
        idle: 0,
        idleOwn: false,
        lastActive: parseTime(e.ended_at) || (e.started_at ?? 0) * 1000,
        endedReason: e.ended_reason,
        exitCode: e.exit_code,
        pinned: input.pins.has(key),
        archived: input.archived?.has(key) ?? false,
        replayable: historyReplayable(e),
      })
    }
  }
  const hostByCode = new Map(input.hosts.filter(h => h.status === 'enabled').map(h => [h.machine_code, h]))
  for (const t of input.threads ?? []) {
    const h = hostByCode.get(t.target)
    if (!h) continue
    const key = threadItemKey(t.key)
    const custom = (input.threadNames?.[t.key] ?? '').trim()
    out.push({
      key,
      kind: 'model',
      code: t.target,
      sid: '',
      title: custom || threadTitle(t, hostTitle(h)),
      name: custom,
      hostName: hostTitle(h),
      hostOnline: h.online,
      live: true,
      running: t.active > 0,
      attached: 0,
      idle: 0,
      idleOwn: false,
      lastActive: parseTime(t.last_at),
      pinned: input.pins.has(key),
      archived: input.archived?.has(key) ?? false,
      replayable: false,
      model: {
        threadKey: t.key,
        credential: t.credential_name,
        thread: t.thread ?? '',
        workflowId: t.workflow_id ?? '',
        active: t.active,
        awaiting: t.awaiting,
        total: t.total,
        lastTitle: t.last_title,
        lastState: t.last_state,
        summary: threadSubtitle(t),
      },
    })
  }
  return out
}

/**
 * 按筛选条件与搜索词过滤；搜索词匹配会话名、主机名、会话 ID 与机器码，模型线程还匹配调用方、线程标签、工作流与最近的命令（不区分大小写）。
 * 已归档的条目只出现在「已归档」筛选里，其余筛选（含「全部」「运行中」）一律不显示它们。
 */
export function filterExecItems(items: ExecItem[], filter: ExecFilter, query: string): ExecItem[] {
  const q = query.trim().toLowerCase()
  return items.filter(it => {
    if (filter === 'archived' ? !it.archived : it.archived) return false
    if (filter === 'human' && it.kind !== 'human') return false
    if (filter === 'model' && it.kind !== 'model') return false
    if (filter === 'running' && !it.running) return false
    if (filter === 'awaiting' && !(it.model && it.model.awaiting > 0)) return false
    if (filter === 'ended' && it.running) return false
    if (!q) return true
    if (it.title.toLowerCase().includes(q) || it.hostName.toLowerCase().includes(q) || it.code.toLowerCase().includes(q)) return true
    if (it.sid && it.sid.toLowerCase().startsWith(q)) return true
    const m = it.model
    return !!m && [m.credential, m.thread, m.workflowId, m.lastTitle].some(x => x.toLowerCase().includes(q))
  })
}

/** 置顶在前；其余按最近活动时间倒序；时间相同按 key 保持稳定。 */
export function sortExecItems(items: ExecItem[]): ExecItem[] {
  return [...items].sort((a, b) => Number(b.pinned) - Number(a.pinned) || b.lastActive - a.lastActive || (a.key < b.key ? -1 : 1))
}

export interface ExecGroup {
  id: string
  label: string
  items: ExecItem[]
}

function dayStart(ms: number): number {
  const d = new Date(ms)
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

/** 日期分组的标题：今天 / 昨天 / 本年 M月D日 / 往年 YYYY年M月D日；没有时间的归入「更早」。 */
export function dayLabel(ms: number, nowMs: number): string {
  if (!ms) return '更早'
  const today = dayStart(nowMs)
  const day = dayStart(ms)
  const diff = Math.round((today - day) / 86_400_000)
  if (diff <= 0) return '今天'
  if (diff === 1) return '昨天'
  const d = new Date(ms)
  const now = new Date(nowMs)
  return d.getFullYear() === now.getFullYear() ? `${d.getMonth() + 1}月${d.getDate()}日` : `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`
}

/** 分组：先「置顶」，再按活动日期分组（输入应已排序）。 */
export function groupExecItems(sorted: ExecItem[], nowMs: number): ExecGroup[] {
  const groups: ExecGroup[] = []
  const pinned = sorted.filter(i => i.pinned)
  if (pinned.length) groups.push({ id: 'pinned', label: '置顶', items: pinned })
  for (const it of sorted) {
    if (it.pinned) continue
    const label = dayLabel(it.lastActive, nowMs)
    const last = groups[groups.length - 1]
    if (last && last.id === `day:${label}`) last.items.push(it)
    else groups.push({ id: `day:${label}`, label, items: [it] })
  }
  return groups
}

/** 已归档条目的数量，以及其中仍在运行的数量（归档不会结束会话，界面要让人知道它们还在跑）。 */
export function archivedStats(items: ExecItem[]): { total: number; running: number } {
  let total = 0
  let running = 0
  for (const it of items) {
    if (!it.archived) continue
    total++
    if (it.running) running++
  }
  return { total, running }
}

/** 搜索词在「已归档」里能匹配到的条目数；当前筛选里一个都没匹配到时，用它提示「已归档里有 N 个匹配项」。 */
export function archivedMatchCount(items: ExecItem[], query: string): number {
  if (!query.trim()) return 0
  return filterExecItems(items, 'archived', query).length
}

/** 清单里正在运行的条目数（人工会话 + 有进行中命令的模型线程）。 */
export function runningCount(items: ExecItem[]): number {
  return items.reduce((n, i) => n + (i.running ? 1 : 0), 0)
}

/** 按线程键找模型线程条目（路由 `/exec/r/:key` 用）。 */
export function findByThread(items: ExecItem[], threadKey: string): ExecItem | undefined {
  return items.find(i => i.model?.threadKey === threadKey)
}

/** 在清单里按会话 ID 找条目（路由 `/exec/t/:sid` 用）；ID 冲突时优先取仍在主机上的。 */
export function findBySid(items: ExecItem[], sid: string): ExecItem | undefined {
  let found: ExecItem | undefined
  for (const it of items) {
    if (it.kind !== 'human' || it.sid !== sid) continue
    if (it.live) return it
    found ??= it
  }
  return found
}
