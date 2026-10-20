import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { agentStateApi, agentStateErrorMessage } from '../api/agentState'
import {
  applyStateEvent,
  groupByWorkflow,
  pickSession,
  type AgentIdentity,
  type AgentSession,
  type AgentStateEvent,
  type PickedSession,
} from '../lib/agentState'
import { IDENTITY_NAME_MAX, computeLabels, normalizeIdentityName } from '../lib/identity'
import { createKeyedRerun } from '../lib/keyedRerun'

// Agent 身份与会话状态（#113，设计稿 04）。
//
// 数据来源与新鲜度：
//   - 全量（身份清单 + 最近 200 个会话）：页面连上事件流时、断线重连后、以及每隔一分钟（页面可见时）各取一次——
//     「离线 / 疑似卡住」这类随时间推导的状态是服务端取数时才算出来的，不重取就不会变；
//   - 事件：服务端状态变化时广播 agent_state（带会话编号与新状态），先就地改缓存，界面立刻可见；
//     再过 1.5 秒重取一次这个工作流的会话（补上卡住标志、最近工具等推导字段），连续事件合并成一次请求；
//   - 改名：提交成功后用服务端返回的那条，再按同一条规则（lib/identity.ts）重新算整批显示名，其它标签页收到
//     agent_identity_updated 后重取。
// 这个仓库只负责数据；怎么翻译成文字和颜色在 lib/agentState.ts，组件只负责摆放。

const OVERVIEW_LIMIT = 200
const WORKFLOW_LIMIT = 20
/** 事件之后重取的合并间隔：Hub 对思考心跳已经每 5 秒才推一次，这里再合并一层。 */
const EVENT_REFRESH_MS = 1500
/** 新会话 / 没见过的身份：尽快补上。 */
const NEW_ENTITY_REFRESH_MS = 250
const IDENTITY_REFRESH_MS = 300
/** 全量数据的保鲜期。 */
const STALE_MS = 60_000

export type AgentEntry = PickedSession & { identity: AgentIdentity | null }

export const useAgentStateStore = defineStore('agentState', () => {
  const identities = ref<AgentIdentity[]>([])
  const sessions = ref<AgentSession[]>([])
  const loadedAt = ref(0)
  const loading = ref(false)
  const error = ref('')
  const renaming = ref(false)
  /** 旧版 Hub 没有这些接口（404）：不再轮询，免得每分钟刷一条错误。 */
  const supported = ref(true)

  const byWorkflow = computed(() => groupByWorkflow(sessions.value))
  const identityById = computed(() => new Map(identities.value.map(i => [i.id, i])))

  /** 一个工作流里应该展示的会话、它的身份，以及还有几个其它活动会话。 */
  function entryFor(workflowId: string | null | undefined): AgentEntry {
    const picked = pickSession(workflowId ? byWorkflow.value.get(workflowId) : undefined)
    const identity = picked.primary ? (identityById.value.get(picked.primary.identity_id) ?? null) : null
    return { ...picked, identity }
  }

  function noteError(err: unknown, what: string) {
    const status = (err as { response?: { status?: number } } | undefined)?.response?.status
    if (status === 404) {
      supported.value = false
      return
    }
    error.value = agentStateErrorMessage(err, what)
  }

  // ---- 取数 ----

  let allInflight = false
  let allRerun = false

  /** 身份清单 + 最近会话。并发调用合并成「当前这次 + 结束后再来一次」。 */
  async function refreshAll(): Promise<void> {
    if (!supported.value) return
    if (allInflight) {
      allRerun = true
      return
    }
    allInflight = true
    loading.value = true
    try {
      const [ids, list] = await Promise.all([
        agentStateApi.listIdentities(),
        agentStateApi.listSessions({ limit: OVERVIEW_LIMIT }),
      ])
      identities.value = ids
      // 概览之外、之前单独取过的工作流（比如打开了一个很久以前的工作流）保留，免得被概览冲掉
      const seen = new Set(list.map(s => s.workflow_id))
      const ids2 = new Set(list.map(s => s.id))
      const keep = sessions.value.filter(s => !ids2.has(s.id) && !seen.has(s.workflow_id))
      sessions.value = [...list, ...keep]
      loadedAt.value = Date.now()
      error.value = ''
    } catch (err) {
      noteError(err, '无法加载 Agent 状态')
    } finally {
      allInflight = false
      loading.value = false
      if (allRerun) {
        allRerun = false
        void refreshAll()
      }
    }
  }

  async function refreshIdentities(): Promise<void> {
    if (!supported.value) return
    try {
      identities.value = await agentStateApi.listIdentities()
    } catch (err) {
      noteError(err, '无法加载 Agent 身份')
    }
  }

  /**
   * 只重取一个工作流的会话（事件之后用，请求很小）。
   * 进行中又来了请求时不能直接丢掉：进行中的这次可能是在新变化发生之前发出的，回来的是旧数据，
   * 丢掉就要等下一次定时校准才会纠正（比如「等你回答」一直不出现）。所以记一笔、结束后补一次——
   * 与上面 refreshAll 同一条规则，实现与单测见 lib/keyedRerun.ts。
   */
  const refreshWorkflow = createKeyedRerun(async (workflowId: string): Promise<void> => {
    if (!supported.value || !workflowId) return
    try {
      const rows = await agentStateApi.listSessions({ workflow_id: workflowId, limit: WORKFLOW_LIMIT })
      sessions.value = [...sessions.value.filter(s => s.workflow_id !== workflowId), ...rows]
      // 会话指向一个还没见过的身份（刚创建）：补一次身份清单
      if (rows.some(r => !identityById.value.has(r.identity_id))) await refreshIdentities()
    } catch (err) {
      noteError(err, '无法加载 Agent 状态')
    }
  })

  // ---- 事件 ----

  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  function schedule(key: string, ms: number, fn: () => void) {
    if (timers.has(key)) return
    timers.set(
      key,
      setTimeout(() => {
        timers.delete(key)
        fn()
      }, ms),
    )
  }

  function onServerEvent(kind: 'agent_state' | 'agent_identity_updated', payload?: unknown) {
    if (!supported.value) return
    if (kind === 'agent_identity_updated') {
      schedule('identities', IDENTITY_REFRESH_MS, () => void refreshIdentities())
      return
    }
    const ev = payload as Partial<AgentStateEvent> | undefined
    if (!ev || !ev.workflow_id || !ev.session_id) {
      schedule('all', EVENT_REFRESH_MS, () => void refreshAll())
      return
    }
    const patched = applyStateEvent(sessions.value, ev as AgentStateEvent, new Date().toISOString())
    if (patched.found) sessions.value = patched.list
    const known = patched.found && (ev.identity_id === undefined || identityById.value.has(ev.identity_id))
    const wf = ev.workflow_id
    schedule(`wf:${wf}`, known ? EVENT_REFRESH_MS : NEW_ENTITY_REFRESH_MS, () => void refreshWorkflow(wf))
  }

  /**
   * 交互请求（会话）有变化：「等你回答」是由待答复的汇报推导出来的，不会有 agent_state 事件，
   * 所以在这里补一次重取。只对已经有 Agent 会话的工作流做，免得没接 hook 的工作流白白发请求。
   */
  function onSessionEvent(workflowId: string | undefined) {
    if (!supported.value || !workflowId || !byWorkflow.value.has(workflowId)) return
    schedule(`wf:${workflowId}`, EVENT_REFRESH_MS, () => void refreshWorkflow(workflowId))
  }

  /** 事件流（重新）连上：断线期间可能漏了事件，整表校准一次。 */
  function onConnected() {
    void refreshAll()
  }

  /** 页面开着时的定时校准：数据超过保鲜期才重取（页面在后台时不取）。 */
  function ensureFresh(maxAgeMs: number = STALE_MS) {
    if (!supported.value || loading.value) return
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return
    if (Date.now() - loadedAt.value >= maxAgeMs) void refreshAll()
  }

  // ---- 改名 ----

  /** 按同一条规则重新计算整批显示名，并同步到会话上冗余的 identity_label。 */
  function relabel(list: AgentIdentity[]): AgentIdentity[] {
    const labels = computeLabels(list)
    return list.map(i => {
      const l = labels.get(i.id)
      return l ? { ...i, label: l.text, label_degraded: l.degraded } : i
    })
  }

  /** 给身份改名；空串 = 恢复用目录名。失败时抛出可直接展示的错误。 */
  async function rename(id: number, raw: string): Promise<AgentIdentity> {
    const name = normalizeIdentityName(raw)
    if (name === null) throw new Error(`名字最多 ${IDENTITY_NAME_MAX} 个字`)
    if (renaming.value) throw new Error('正在提交，请稍候')
    renaming.value = true
    try {
      let view: AgentIdentity
      try {
        view = await agentStateApi.renameIdentity(id, name)
      } catch (err) {
        throw new Error(agentStateErrorMessage(err, '改名失败'))
      }
      const merged = identities.value.some(i => i.id === id)
        ? identities.value.map(i => (i.id === id ? { ...i, ...view } : i))
        : [...identities.value, view]
      identities.value = relabel(merged)
      const byId = new Map(identities.value.map(i => [i.id, i]))
      sessions.value = sessions.value.map(s => {
        const idn = byId.get(s.identity_id)
        return idn && idn.label !== s.identity_label ? { ...s, identity_label: idn.label } : s
      })
      return byId.get(id) ?? view
    } finally {
      renaming.value = false
    }
  }

  return {
    identities,
    sessions,
    loadedAt,
    loading,
    error,
    renaming,
    supported,
    byWorkflow,
    entryFor,
    refreshAll,
    refreshIdentities,
    refreshWorkflow,
    onServerEvent,
    onSessionEvent,
    onConnected,
    ensureFresh,
    rename,
  }
})
