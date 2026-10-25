import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { branchesApi, type BranchBrief, type BranchFeed, type BranchFeedItem, type BranchParamEvent, type UpdateBranchRequest, type UpdateBranchResult } from '../api/branches'
import { registerConnectHook } from '../lib/connectHooks'
import { indexBranches, reuseFeedItems } from '../lib/branch'

// 分支会话（#236）：全部分支关系（侧栏的分支树、页头的芯片与分支面板用）+ 当前查看的那个会话的分支消息流（时间线用）。
//
// 关系列表在侧栏第一次挂载时取一次，之后靠实时流的 branch_changed / branch_message 校准（防抖，几条事件合并成一次重取）。
// 消息流只给「有分支关系的会话」取：没有任何关系的会话（绝大多数）切换过去不发任何额外请求。

const COLLAPSED_KEY = 'relaymesh_branch_collapsed'
const REFRESH_DEBOUNCE_MS = 300
const EMPTY_ITEMS: BranchFeedItem[] = []
const EMPTY_TITLES: Record<string, string> = {}

function loadCollapsed(): Set<string> {
  try {
    const raw = typeof localStorage === 'undefined' ? null : localStorage.getItem(COLLAPSED_KEY)
    const arr = raw ? JSON.parse(raw) : []
    return new Set(Array.isArray(arr) ? arr.filter((x): x is string => typeof x === 'string') : [])
  } catch {
    return new Set()
  }
}

export function branchErrorMessage(err: unknown, fallback: string): string {
  const anyErr = err as { response?: { data?: { error?: string } }; message?: string } | undefined
  return anyErr?.response?.data?.error || anyErr?.message || fallback
}

export const useBranchStore = defineStore('branches', () => {
  const relations = ref<BranchBrief[]>([])
  const loaded = ref(false)
  const error = ref('')
  /** 侧栏里折叠起来的主会话（它的分支不显示，只在主会话那一行写「N 个分支」）。 */
  const collapsed = ref<Set<string>>(loadCollapsed())

  /** 正在看的那个会话；feed 是它的分支消息流（feedFor 标明这份流属于哪个会话，切换时不会串）。 */
  const viewedWorkflowId = ref('')
  const feed = ref<BranchFeed | null>(null)
  const feedFor = ref('')
  /**
   * 每完成一次「重取关系 + 消息流」加一（实时流事件、自己的改动之后都会触发）。分支面板里的参数记录不进仓库，
   * 面板靠它在主会话改了参数 / 自己撤销之后重取。
   */
  const refreshTick = ref(0)

  const index = computed(() => indexBranches(relations.value))

  let relSeq = 0
  let feedSeq = 0
  let relPending: Promise<void> | null = null
  let refreshTimer: ReturnType<typeof setTimeout> | undefined

  // ---------- 关系 ----------

  async function loadRelations(): Promise<void> {
    const mine = ++relSeq
    try {
      const list = await branchesApi.list()
      if (mine !== relSeq) return
      relations.value = list
      loaded.value = true
      error.value = ''
    } catch (err) {
      if (mine !== relSeq) return
      error.value = branchErrorMessage(err, '无法读取分支关系')
    }
  }

  /** 第一次用到时取一次；同时只有一个在途请求。 */
  function ensureLoaded(): Promise<void> {
    if (loaded.value) return Promise.resolve()
    relPending ??= loadRelations().finally(() => {
      relPending = null
    })
    return relPending
  }

  /** 这条关系（刚建立 / 刚改过）直接并进本地列表，不等下一次广播。 */
  function applyRelation(brief: BranchBrief): void {
    const next = relations.value.filter(r => r.branch_workflow_id !== brief.branch_workflow_id)
    next.unshift(brief)
    relations.value = next
    loaded.value = true
  }

  /** 这个工作流作为分支的那条关系（活动或已结束；一个分支至多一条）。 */
  function asBranch(workflowId: string): BranchBrief | undefined {
    return relations.value.find(r => r.branch_workflow_id === workflowId)
  }

  /** 这个工作流作为主会话的全部关系（活动 + 已结束，建立时间升序）。 */
  function asMain(workflowId: string): BranchBrief[] {
    return relations.value
      .filter(r => r.parent_workflow_id === workflowId)
      .sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at))
  }

  /** 这个工作流在关系里出现过吗（作为主会话或分支，含已结束）。 */
  function involves(workflowId: string): boolean {
    return !!workflowId && relations.value.some(r => r.branch_workflow_id === workflowId || r.parent_workflow_id === workflowId)
  }

  function toggleCollapsed(parentWorkflowId: string): void {
    const next = new Set(collapsed.value)
    if (next.has(parentWorkflowId)) next.delete(parentWorkflowId)
    else next.add(parentWorkflowId)
    collapsed.value = next
    try {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]))
    } catch {
      // 存储不可用（隐私模式）：只在本次页面里生效
    }
  }

  // ---------- 会话页的消息流 ----------

  function clearFeed(): void {
    feedSeq++
    feed.value = null
    feedFor.value = ''
  }

  async function loadFeed(workflowId: string): Promise<void> {
    const mine = ++feedSeq
    try {
      const f = await branchesApi.feed(workflowId)
      if (mine !== feedSeq || viewedWorkflowId.value !== workflowId) return
      const prev = feedFor.value === workflowId && feed.value ? feed.value.items : EMPTY_ITEMS
      feed.value = { ...f, items: reuseFeedItems(prev, f.items) }
      feedFor.value = workflowId
    } catch {
      // 消息流取不到不影响会话本身；下一次事件 / 切换会再取
    }
  }

  /**
   * 会话页切到一个工作流：它在分支关系里就取消息流，不在就清空（不发请求）。
   * 关系列表还没到时先等它，免得对每个会话都白取一遍消息流。
   */
  async function viewWorkflow(workflowId: string): Promise<void> {
    viewedWorkflowId.value = workflowId
    if (feedFor.value !== workflowId) clearFeed()
    if (!workflowId) return
    await ensureLoaded()
    if (viewedWorkflowId.value !== workflowId) return
    if (!involves(workflowId)) {
      clearFeed()
      return
    }
    await loadFeed(workflowId)
  }

  /** 这个会话页上的分支消息（切走之后旧会话的流不会串到新会话上）。 */
  function itemsFor(workflowId: string): BranchFeedItem[] {
    return feedFor.value === workflowId && feed.value ? feed.value.items : EMPTY_ITEMS
  }

  function titlesFor(workflowId: string): Record<string, string> {
    return feedFor.value === workflowId && feed.value?.titles ? feed.value.titles : EMPTY_TITLES
  }

  // ---------- 改设置 / 结束 ----------

  /**
   * 改一个分支向主会话开放的范围（#236 P3 起放宽「改参数」/「启动」要终端解锁：调用方用 terminalStore.unlockOnDemand 包一层，
   * 取消解锁抛 UnlockCancelled）。终端不可用时放宽的项被压回原值，返回里的 clamped 列出它们，其余已保存。
   */
  async function updateSettings(workflowId: string, patch: UpdateBranchRequest): Promise<UpdateBranchResult> {
    const res = await branchesApi.update(workflowId, patch)
    applyRelation(res.branch)
    scheduleRefresh()
    return res
  }

  /** 主会话改过这个分支的哪些参数（不进仓库，每次现取；新的在前）。 */
  function loadParamEvents(workflowId: string, limit?: number): Promise<BranchParamEvent[]> {
    return branchesApi.paramEvents(workflowId, limit)
  }

  /**
   * 撤销主会话的一次修改（只要登录）。成功后触发一次重取：分支的参数（阶段、标题、常驻规则……）、消息流里的事件行与本面板的记录一起刷新。
   * 不能撤销时服务端回 409 并写明原因，错误原样抛给调用方（branchErrorMessage 取出原因）。
   */
  async function revertParamEvent(workflowId: string, eventId: number): Promise<BranchParamEvent> {
    const res = await branchesApi.revertParamEvent(workflowId, eventId)
    scheduleRefresh()
    return res.reverted
  }

  /**
   * 结束一个分支。returnedTasks 是因此退回主会话名下的任务序号（主会话派给它、还没交付的；没有就是空数组）：
   * 任务页由服务端广播的 work_item_updated 自行刷新，这里只把序号交给调用方写进提示。
   */
  async function closeBranch(workflowId: string): Promise<{ branch: BranchBrief; returnedTasks: number[] }> {
    const res = await branchesApi.close(workflowId)
    applyRelation(res.branch)
    scheduleRefresh()
    return { branch: res.branch, returnedTasks: res.returnedTasks }
  }

  // ---------- 实时流 ----------

  /** 几条事件合并成一次重取：关系列表（未读数、最近活动随之变）与正在看的消息流。 */
  function scheduleRefresh(): void {
    if (refreshTimer) return
    refreshTimer = setTimeout(() => {
      refreshTimer = undefined
      void refreshNow()
    }, REFRESH_DEBOUNCE_MS)
  }

  /** 先重取关系，再按新的关系决定要不要取正在看的会话的消息流（刚建立的第一个分支也要能带出主会话页上的消息流）。 */
  async function refreshNow(): Promise<void> {
    if (loaded.value) await loadRelations()
    const wf = viewedWorkflowId.value
    if (wf && involves(wf)) await loadFeed(wf)
    refreshTick.value++
  }

  /** branch_changed / branch_message：没用到过分支的页面不为此发请求。 */
  function onEvent(payload?: { parent?: string; branch?: string } | null): void {
    const wf = viewedWorkflowId.value
    const touchesViewed = !!wf && (payload?.parent === wf || payload?.branch === wf)
    if (!loaded.value && !touchesViewed) return
    scheduleRefresh()
  }

  /** 实时流（首次或重新）连上：断线期间的事件没人听见，已用到的数据各重取一次（#243）。 */
  function onConnected(): Promise<void> {
    return refreshNow()
  }
  // 惰性仓库：登记了就等于「有旧数据要校准」（lib/connectHooks.ts）
  registerConnectHook('branches', onConnected)

  return {
    relations,
    loaded,
    error,
    collapsed,
    index,
    viewedWorkflowId,
    feed,
    refreshTick,
    ensureLoaded,
    loadRelations,
    applyRelation,
    asBranch,
    asMain,
    involves,
    toggleCollapsed,
    viewWorkflow,
    itemsFor,
    titlesFor,
    updateSettings,
    loadParamEvents,
    revertParamEvent,
    closeBranch,
    onEvent,
    onConnected,
  }
})
