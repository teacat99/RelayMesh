// Agent 会话 / 身份的「分工作流」缓存表（#215）。
//
// 原来的写法：store 里一个扁平数组 sessions，每来一个 agent_state 事件就整体换一份新数组，byWorkflow / identityById 两个 computed
// 跟着整体重算——侧栏几十张卡、底部的状态芯片、页头徽标……凡是读过其中任何一个工作流的组件都被唤醒，
// 而一个事件只涉及一个工作流。事件一秒来三四个时（Agent 在干活），整页的卡片就跟着一秒重画几百次。
//
// 这里改成按键失效：groups 是 shallowReactive 的 Map，「读某个工作流」只依赖它自己那一项；
// 写入前先与旧值比较，内容没变就不写（不通知任何人），变了只通知读这一项的组件。
// 会话 / 身份记录都是只含原始值的扁平对象，用逐字段比较即可（比较时沿用旧对象，保持「没变的就是同一个对象」）。
import { shallowReactive } from 'vue'
import { applyStateEvent, validTime, type AgentIdentity, type AgentSession, type AgentStateEvent } from './agentState'

/** 两条扁平记录是否逐字段相同（键的集合也要一致）。 */
export function sameRecord<T extends object>(a: T, b: T): boolean {
  if (a === b) return true
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return false
  for (const k of ka) {
    if (!Object.prototype.hasOwnProperty.call(b, k)) return false
    if (!Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k])) return false
  }
  return true
}

/** 最近有动静的在前（与 lib/agentState 的 groupByWorkflow 同一条规则）。 */
const byRecency = (a: AgentSession, b: AgentSession) => (validTime(b.last_event_at) ?? 0) - (validTime(a.last_event_at) ?? 0)

export type AgentGroup = ReadonlyArray<AgentSession>

export interface AgentTable {
  /** 工作流 → 它的会话（已按最近动静排序）。按键响应：读 get(wf) / has(wf) 只依赖这一个工作流。 */
  readonly groups: ReadonlyMap<string, AgentGroup>
  /** 身份编号 → 身份。按键响应。 */
  readonly identities: ReadonlyMap<number, AgentIdentity>
  /** 整批身份清单：已有的内容没变就不动，清单里没有的删掉。 */
  setIdentities(list: ReadonlyArray<AgentIdentity>): void
  /** 全量校准：清单里出现的工作流换成清单里的会话，没出现的工作流原样保留（概览有上限，不能把单独取过的冲掉）。 */
  mergeOverview(list: ReadonlyArray<AgentSession>): void
  /** 只换一个工作流的会话（事件之后的小请求）；传空数组 = 这个工作流没有会话了。 */
  replaceWorkflow(workflowId: string, rows: ReadonlyArray<AgentSession>): void
  /** 把一个 agent_state 事件就地套到缓存上；找不到这个会话返回 false（调用方安排重取）。 */
  patchEvent(ev: AgentStateEvent, nowIso: string): boolean
  /** 身份改名之后，同步会话上冗余的 identity_label；只动标签变了的那几个工作流。 */
  relabelSessions(byId: ReadonlyMap<number, AgentIdentity>): void
}

export function createAgentTable(): AgentTable {
  const groups = shallowReactive(new Map<string, AgentGroup>())
  const identities = shallowReactive(new Map<number, AgentIdentity>())

  function setGroup(workflowId: string, rows: ReadonlyArray<AgentSession>): void {
    if (rows.length === 0) {
      groups.delete(workflowId)
      return
    }
    const prev = groups.get(workflowId)
    const prevById = prev ? new Map(prev.map(s => [s.id, s])) : null
    const next = rows.map(r => {
      const old = prevById?.get(r.id)
      return old && sameRecord(old, r) ? old : r
    })
    next.sort(byRecency)
    if (prev && prev.length === next.length && prev.every((s, i) => s === next[i])) return
    groups.set(workflowId, next)
  }

  return {
    groups,
    identities,

    setIdentities(list) {
      const seen = new Set<number>()
      for (const idn of list) {
        seen.add(idn.id)
        const old = identities.get(idn.id)
        if (!old || !sameRecord(old, idn)) identities.set(idn.id, idn)
      }
      for (const id of [...identities.keys()]) if (!seen.has(id)) identities.delete(id)
    },

    mergeOverview(list) {
      const byWf = new Map<string, AgentSession[]>()
      for (const s of list) {
        if (!s.workflow_id) continue
        const g = byWf.get(s.workflow_id)
        if (g) g.push(s)
        else byWf.set(s.workflow_id, [s])
      }
      for (const [wf, rows] of byWf) setGroup(wf, rows)
    },

    replaceWorkflow(workflowId, rows) {
      setGroup(workflowId, rows)
    },

    patchEvent(ev, nowIso) {
      let wf = ev.workflow_id
      let group = groups.get(wf)
      if (!group || !group.some(s => s.id === ev.session_id)) {
        // 事件里的工作流号与缓存对不上时，按会话编号到别的工作流里找一找
        wf = ''
        for (const [k, g] of groups) {
          if (g.some(s => s.id === ev.session_id)) {
            wf = k
            group = g
            break
          }
        }
        if (!wf || !group) return false
      }
      const patched = applyStateEvent(group, ev, nowIso)
      if (!patched.found) return false
      setGroup(wf, patched.list)
      return true
    },

    relabelSessions(byId) {
      for (const [wf, g] of [...groups]) {
        let changed = false
        const next = g.map(s => {
          const idn = byId.get(s.identity_id)
          if (idn && idn.label !== s.identity_label) {
            changed = true
            return { ...s, identity_label: idn.label }
          }
          return s
        })
        if (changed) groups.set(wf, next)
      }
    },
  }
}
