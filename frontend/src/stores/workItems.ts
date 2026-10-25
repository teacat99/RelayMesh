import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import {
  workItemsApi,
  workModulesApi,
  workErrorMessage,
  type WorkItemCreate,
  type WorkItemPatch,
  type WorkModuleDeleted,
  type WorkModulePlan,
  type WorkflowUpdate,
} from '../api/workItems'
import {
  DEFAULT_VISIBLE_STATES,
  ASSIGNEE_ALL,
  MODULE_ALL,
  buildModuleTree,
  buildTaskGroups,
  countByState,
  filterBySelection,
  filterItems,
  groupSessionTasks,
  inBoard,
  isWorkState,
  summarizeTracks,
  type ModuleSelection,
  type TaskRow,
  type TaskSort,
  type WorkCounts,
  type WorkImproveDelivery,
  type WorkItem,
  type WorkModule,
  type WorkModuleStats,
  type WorkOffered,
  type WorkQuickKey,
  type WorkSessionLink,
  type WorkState,
  type WorkflowStat,
} from '../lib/workItems'
import { registerConnectHook } from '../lib/connectHooks'
import { whenSecondaryOpen } from '../lib/secondaryGate'

const FILTER_KEY = 'relaymesh_work_filter'

/** 已结束任务的时间窗默认值（天）：手机「筛选」角标据此判断这一项有没有被改动。 */
export const DEFAULT_CLOSED_DAYS = 30

/** 已完成 / 已搁置的任务只展示最近这么多天内关闭的；0 = 不限。 */
export const CLOSED_WINDOWS = [
  { days: 7, label: '近 7 天' },
  { days: 30, label: '近 30 天' },
  { days: 90, label: '近 90 天' },
  { days: 0, label: '全部' },
] as const

interface SavedFilter {
  workflowId?: string
  kind?: string
  closedDays?: number
  sort?: TaskSort
  states?: WorkState[]
}

function readSavedFilter(): SavedFilter {
  try {
    const raw = localStorage.getItem(FILTER_KEY)
    return raw ? (JSON.parse(raw) as SavedFilter) : {}
  } catch {
    return {}
  }
}

/** 更新接口只回传有值的字段：这些「可清除」的字段缺省就表示已被清除，合并时不能沿用旧值。 */
const CLEARABLE_FIELDS = [
  'module_id',
  'module',
  'percent',
  'parent_id',
  'stage',
  // 撤销指派后服务端不再回传这些字段（#175 B2）
  'assignee_workflow_id',
  'assigned_by_workflow_id',
  'claim_state',
  'assigned_at',
  'accepted_at',
] as const

function mergeItem(old: WorkItem, next: WorkItem): WorkItem {
  const merged: WorkItem = { ...old, ...next }
  for (const k of CLEARABLE_FIELDS) {
    if (!(k in next)) delete merged[k]
  }
  return merged
}

/**
 * 任务清单（WorkItem）的前端状态。
 *
 * 任务页按「项目（工作流）」组织：服务端一次返回该项目的任务与规划树（模块 → 功能），
 * 左侧树的选中项、状态 / 类型 / 关键字筛选都在已加载的结果里本地完成。
 * 侧栏入口与会话页头「任务」入口的计数不依赖任务页是否打开过：refreshCounts 只取一条记录，换来全局计数与各项目概况。
 */
/** 会话时间线一次最多取多少条任务进展（更早的去任务页看）。 */
const FEED_UPDATES_LIMIT = 60

export const useWorkItemStore = defineStore('workItems', () => {
  const saved = readSavedFilter()

  const items = ref<WorkItem[]>([])
  /** 当前项目下各状态的任务数（含所有已关闭的）。 */
  const counts = ref<WorkCounts>({})
  /** 全部项目下各状态的任务数，侧栏徽标用。 */
  const globalCounts = ref<WorkCounts>({})
  const workflows = ref<WorkflowStat[]>([])
  /**
   * 各工作流手上待接取的协作任务（全局口径，服务端 GET /work-items 的 offered）：侧栏会话卡的「N 项待接取」徽标读它（#180 B3b）。
   * 每次拿到列表（任务页加载 / 侧栏拉计数 / SSE 触发的后台刷新）都对一次，所以指派、接取、拒绝后徽标跟着变，不需要额外的订阅。
   */
  const offered = ref<WorkOffered[]>([])
  /** 当前项目的规划树（树序的扁平列表）与未归类任务的汇总。 */
  const modules = ref<WorkModule[]>([])
  const unassigned = ref<WorkModuleStats | null>(null)
  /** 当前项目（任务页筛选的工作流）所在的任务板；同一个项目的工作流共用一张板，判断「任务属不属于当前清单」要看它。 */
  const boardKey = ref('')
  /** 清单被截断了（最近更新的取满了上限，更早更新的没显示）；页面据此如实说明。 */
  const truncated = ref(false)
  const loaded = ref(false)
  /** 各项目的任务计数（workflows / globalCounts）是否至少取到过一次——任务页没打开过、只靠侧栏拉计数时 loaded 不会变，页头状态徽标看这个。 */
  const countsLoaded = ref(false)
  const loading = ref(false)
  const error = ref('')

  const filter = reactive({
    workflowId: saved.workflowId || '',
    kind: saved.kind || '',
    query: '',
    closedDays: typeof saved.closedDays === 'number' ? saved.closedDays : DEFAULT_CLOSED_DAYS,
    sort: (saved.sort === 'recent' ? 'recent' : 'plan') as TaskSort,
    /** 承接者过滤（共享任务板）：只在本次打开页面期间有效，不记在本机——换了项目 / 工作流，旧编号就没意义了。 */
    assignee: ASSIGNEE_ALL as string,
    /** 协作快捷筛选（指派给我 / 我派出的 / 待接取）：同样只在本次打开页面期间有效；「我」= filter.workflowId。 */
    quick: '' as '' | WorkQuickKey,
    states: (Array.isArray(saved.states) && saved.states.filter(isWorkState).length
      ? saved.states.filter(isWorkState)
      : [...DEFAULT_VISIBLE_STATES]) as WorkState[],
    /** 左侧树的选中项：全部 / 未归类 / 某个模块或功能。 */
    selection: MODULE_ALL as ModuleSelection,
  })

  /** 批次（有子任务的任务）的展开 / 折叠：用户手动切换过的才记录，其余按默认（还有没结束的子任务就展开）。 */
  const batchExpanded = reactive(new Map<number, boolean>())

  const selectedId = ref<number | null>(null)
  const detail = ref<WorkItem | null>(null)
  const children = ref<WorkItem[]>([])
  const detailLoading = ref(false)
  /**
   * 详情所属工作流的规划树。`modules` 只对应任务页当前筛选的工作流；从会话页（别的工作流）点开任务详情时，
   * 详情里的「所属模块」要用任务自己那个工作流的规划树，所以单独取一份。
   */
  const detailForeignModules = ref<{ workflowId: string; modules: WorkModule[] } | null>(null)

  /**
   * 会话页的任务数据：当前工作流的全部任务，加上「哪个会话涉及哪个任务、做了什么」。
   * 会话页按轮次（每个会话）展开本轮涉及的任务，页头下方的总览用的也是这一份。
   * 与任务页的列表互不影响；服务端事件到达时一起刷新，所以智能体在对话里写任务，这里会即时多出一行。
   */
  const feed = reactive({
    workflowId: '',
    items: [] as WorkItem[],
    links: [] as WorkSessionLink[],
    /** 智能体写下的任务进展（每次更新一条，按提交时间升序）：会话时间线的进展卡每次更新一张，靠它排进对话。 */
    updates: [] as WorkflowUpdate[],
    loading: false,
    loaded: false,
    error: '',
  })

  let loadSeq = 0
  let detailSeq = 0
  let feedSeq = 0
  /** watchFeed 的令牌：被放行延后的首次加载只有令牌仍是最新的才真的发。 */
  let feedWatchSeq = 0
  let refreshTimer: ReturnType<typeof setTimeout> | null = null

  function persistFilter() {
    try {
      localStorage.setItem(
        FILTER_KEY,
        JSON.stringify({
          workflowId: filter.workflowId,
          kind: filter.kind,
          closedDays: filter.closedDays,
          sort: filter.sort,
          states: filter.states,
        })
      )
    } catch {
      // 隐私模式等场景下写不进去，不影响使用
    }
  }

  /** 任务是否属于当前清单（按任务板比较，见 inBoard）。 */
  function inCurrentBoard(item: Pick<WorkItem, 'workflow_id' | 'board_key'>): boolean {
    return inBoard(item, boardKey.value, filter.workflowId)
  }

  // ---------- 派生 ----------

  const tree = computed(() => buildModuleTree(modules.value))
  /** 左侧树选中项 + 类型 + 关键字筛选之后的任务（还没按状态筛）。 */
  const scoped = computed(() =>
    filterBySelection(
      filterItems(items.value, {
        kind: filter.kind,
        query: filter.query,
        assignee: filter.assignee,
        quick: filter.quick,
        viewer: filter.workflowId,
      }),
      filter.selection,
      modules.value
    )
  )
  const offeredByWorkflow = computed(() => new Map(offered.value.map(o => [o.workflow_id, o] as const)))
  /** 某个工作流手上待接取的协作任务（没有则 undefined）。 */
  function offeredOf(workflowId: string | undefined | null): WorkOffered | undefined {
    return workflowId ? offeredByWorkflow.value.get(workflowId) : undefined
  }
  /** 状态筛选条上的数字：在其它筛选条件下各状态有几个。 */
  const stateCounts = computed(() => countByState(scoped.value))
  const visible = computed(() => scoped.value.filter(i => filter.states.includes(i.state)))
  const groups = computed(() => buildTaskGroups(visible.value, modules.value, { sort: filter.sort, expanded: batchExpanded }))
  const openTotal = computed(
    () => (globalCounts.value.todo || 0) + (globalCounts.value.doing || 0) + (globalCounts.value.review || 0)
  )
  /** 当前项目的概况（来自工作流列表）。 */
  const currentStat = computed(() => workflows.value.find(w => w.workflow_id === filter.workflowId) || null)
  const hasPlan = computed(() => modules.value.length > 0)
  /** 会话页：每个会话（轮次）涉及的任务。 */
  const feedBySession = computed(() => groupSessionTasks(feed.items, feed.links))
  /** 会话页：当前工作流全部任务的分布与整体完成度。 */
  const feedSummary = computed(() => summarizeTracks(feed.items))
  /**
   * 会话页当前工作流里存在的任务编号：正文里的 #N 只有在这里能找到时才会变成链接（见 lib/taskRefs.ts）。
   * 签名是排好序的编号串：它只在编号集合真的变了时才变，正文里成百上千处引用据此决定要不要重新装饰，
   * 而不是每次任务进度刷新都重扫一遍。
   */
  const feedSeqSet = computed(() => new Set(feed.items.map(i => i.seq)))
  /** 编号 → 标题：正文里 #N 链接的悬停说明用（标题改了不触发重新装饰，下次装饰时才更新）。 */
  const feedTitleBySeq = computed(() => new Map(feed.items.map(i => [i.seq, i.title] as const)))
  const feedSeqSignature = computed(() =>
    feed.items
      .map(i => i.seq)
      .sort((a, b) => a - b)
      .join(',')
  )
  /** 详情里「所属模块」可选的规划树：任务属于任务页当前的工作流时用 modules，否则用单独取来的那一份。 */
  const detailModules = computed<WorkModule[]>(() => {
    const d = detail.value
    if (!d || inCurrentBoard(d)) return modules.value
    const f = detailForeignModules.value
    return f && f.workflowId === d.workflow_id ? f.modules : []
  })

  // ---------- 读取 ----------

  function applyPlan(plan: WorkModulePlan) {
    modules.value = plan.modules
    unassigned.value = plan.unassigned
    // 选中的节点被删（可能是别处删的）：回到「全部」
    if (typeof filter.selection === 'number' && !plan.modules.some(m => m.id === filter.selection)) {
      filter.selection = MODULE_ALL
    }
  }

  /** 采纳服务端的 offered：内容没变就不换数组，免得每次刷新都把侧栏所有会话卡的依赖唤醒一遍。 */
  function applyOffered(next: WorkOffered[] | undefined) {
    const list = next ?? []
    const cur = offered.value
    const same = list.length === cur.length && list.every((o, i) => o.workflow_id === cur[i].workflow_id && o.count === cur[i].count && o.oldest_at === cur[i].oldest_at)
    if (!same) offered.value = list
  }

  /** 刷新任务页数据。silent=true 时不触发加载态（SSE 事件触发的后台刷新不应让界面闪动）。 */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const res = await workItemsApi.list({
        workflow_id: filter.workflowId || undefined,
        closed_days: filter.closedDays || undefined,
      })
      if (seq !== loadSeq) return
      items.value = res.items
      counts.value = res.counts
      workflows.value = res.workflows
      applyOffered(res.offered)
      applyPlan({ modules: res.modules, unassigned: res.unassigned })
      boardKey.value = filter.workflowId ? res.board_key : ''
      truncated.value = res.truncated
      if (!filter.workflowId) globalCounts.value = res.counts
      error.value = ''
      loaded.value = true
      countsLoaded.value = true
      if (filter.workflowId) await refreshCounts()
    } catch (err) {
      if (seq !== loadSeq) return
      error.value = workErrorMessage(err, '无法加载任务清单')
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  /** 只更新全局计数与项目概况（取一条记录，开销很小）。 */
  async function refreshCounts() {
    try {
      const res = await workItemsApi.list({ limit: 1 })
      globalCounts.value = res.counts
      workflows.value = res.workflows
      applyOffered(res.offered)
      countsLoaded.value = true
    } catch {
      // 计数拿不到不影响主流程
    }
  }

  async function openDetail(id: number, silent = false) {
    const seq = ++detailSeq
    selectedId.value = id
    if (!silent) detailLoading.value = true
    try {
      const [item, kids] = await Promise.all([workItemsApi.get(id), workItemsApi.list({ parent_id: id })])
      if (seq !== detailSeq) return
      detail.value = item
      children.value = kids.items
      // 任务不属于任务页当前的工作流（从会话页打开的）：另取它自己那个工作流的规划树，给「所属模块」用
      if (!inCurrentBoard(item) && detailForeignModules.value?.workflowId !== item.workflow_id) {
        try {
          const plan = await workItemsApi.list({ workflow_id: item.workflow_id, limit: 1 })
          if (seq === detailSeq) detailForeignModules.value = { workflowId: item.workflow_id, modules: plan.modules }
        } catch {
          // 取不到规划树只影响「所属模块」下拉框，不影响查看与编辑其它内容
        }
      }
    } catch (err) {
      if (seq !== detailSeq) return
      error.value = workErrorMessage(err)
      closeDetail()
    } finally {
      if (seq === detailSeq) detailLoading.value = false
    }
  }

  function closeDetail() {
    detailSeq++
    selectedId.value = null
    detail.value = null
    children.value = []
    detailLoading.value = false
    detailForeignModules.value = null
  }

  /**
   * 按编号打开任务详情：编号来自正文里的 #N，只在会话页当前的工作流里解析。
   * 找不到（任务已被删除、或不是这个工作流的编号）时返回 false，由调用方提示。
   */
  async function openBySeq(workflowId: string, seq: number): Promise<boolean> {
    const hit = feed.workflowId === workflowId ? feed.items.find(i => i.seq === seq) : undefined
    if (!hit) return false
    await openDetail(hit.id)
    return true
  }

  /**
   * 按「板内序号 #N」打开任务详情（任务页地址里的 ?seq= 深链，#242）：由服务端按工作流所在的任务板解析，
   * 不依赖页面已加载的清单——已结束 / 已暂缓 / 很早以前的任务、被筛选藏起来的任务一样能开（新标签页打开时清单还没加载）。
   * 这张板上没有这个序号返回 false，由调用方提示；其它错误（网络等）像 openDetail 一样记进 error 并返回 true（已处理，不再提示「找不到」）。
   */
  async function openByBoardSeq(workflowId: string, seq: number): Promise<boolean> {
    let item: WorkItem | null
    try {
      item = await workItemsApi.getBySeq(workflowId, seq)
    } catch (err) {
      error.value = workErrorMessage(err)
      return true
    }
    if (!item) return false
    await openDetail(item.id)
    return true
  }

  // ---------- 会话页的任务 ----------

  /** 读取一个工作流的全部任务与「会话 ↔ 任务」关联。silent=true（服务端事件触发的刷新）不显示加载态，也不因暂时的网络错误清空已有内容。 */
  async function loadFeed(workflowId: string, silent = false) {
    const seq = ++feedSeq
    if (!silent) feed.loading = true
    try {
      // 进展流失败不影响任务列表：旧服务端没有这个接口时保持原样，时间线只是没有进展卡
      const [res, updates] = await Promise.all([
        workItemsApi.list({ workflow_id: workflowId, with_links: true }),
        workItemsApi.workflowUpdates(workflowId, { limit: FEED_UPDATES_LIMIT }).catch(() => null),
      ])
      if (seq !== feedSeq || feed.workflowId !== workflowId) return
      feed.items = res.items
      feed.links = res.links
      if (updates) feed.updates = updates
      feed.error = ''
      feed.loaded = true
    } catch (err) {
      if (seq !== feedSeq || feed.workflowId !== workflowId) return
      if (!silent) feed.error = workErrorMessage(err, '无法加载这个项目的任务')
    } finally {
      if (seq === feedSeq) feed.loading = false
    }
  }

  /** 会话页切换到另一个工作流（或离开会话页，传空串）：清掉上一个工作流的数据，免得新页面下闪现旧内容。 */
  function watchFeed(workflowId: string) {
    if (feed.workflowId === workflowId) return
    feedSeq++
    feed.workflowId = workflowId
    feed.items = []
    feed.links = []
    feed.updates = []
    feed.loaded = false
    feed.loading = false
    feed.error = ''
    if (workflowId) {
      // 次要请求让路（#169 第三期，lib/secondaryGate.ts）：整表在途时先等一等，放行后再取；等的这段时间里又切走了就不发
      // （令牌对不上）。栅栏没立着时 whenSecondaryOpen 同步执行，与以前一样。
      const token = ++feedWatchSeq
      whenSecondaryOpen(() => {
        if (token === feedWatchSeq && feed.workflowId === workflowId) void loadFeed(workflowId)
      })
    } else {
      feedWatchSeq++
    }
  }

  // ---------- 筛选 ----------

  /** 切换项目：清掉上一个项目的任务与规划树，免得新标题下闪现旧内容。 */
  function setWorkflow(id: string) {
    if (filter.workflowId === id) return
    filter.workflowId = id
    boardKey.value = ''
    truncated.value = false
    filter.selection = MODULE_ALL
    filter.query = ''
    filter.assignee = ASSIGNEE_ALL
    filter.quick = ''
    items.value = []
    modules.value = []
    unassigned.value = null
    counts.value = {}
    loaded.value = false
    batchExpanded.clear()
    closeDetail()
    persistFilter()
    void load()
  }

  /** 按类型筛选；空串 = 全部类型。 */
  function setKind(kind: string) {
    if (filter.kind === kind) return
    filter.kind = kind
    persistFilter()
  }

  /** 按承接者筛选：ASSIGNEE_ALL = 全部，ASSIGNEE_NONE = 未指派，其余 = 工作流编号（不记在本机）。 */
  function setAssignee(assignee: string) {
    filter.assignee = assignee || ASSIGNEE_ALL
  }

  /** 协作快捷筛选：传空串 = 取消；传已选中的那一项也等于取消（点两次回到全部）。 */
  function setQuick(quick: '' | WorkQuickKey) {
    filter.quick = filter.quick === quick ? '' : quick
  }

  function setClosedDays(days: number) {
    if (filter.closedDays === days) return
    filter.closedDays = days
    persistFilter()
    void load()
  }

  function setSort(sort: TaskSort) {
    if (filter.sort === sort) return
    filter.sort = sort
    persistFilter()
  }

  function toggleState(state: WorkState) {
    const i = filter.states.indexOf(state)
    if (i >= 0) filter.states.splice(i, 1)
    else filter.states.push(state)
    persistFilter()
  }

  function select(sel: ModuleSelection) {
    filter.selection = sel
  }

  /** 批次的展开 / 折叠：row 是当前列表里的这一行。 */
  function toggleBatch(row: Pick<TaskRow, 'item' | 'collapsed'>) {
    batchExpanded.set(row.item.id, row.collapsed)
  }

  // ---------- 任务写入 ----------

  /** 把服务端返回的任务合并进本地列表与详情，避免等下一次整表刷新才看到变化。 */
  function applyServerItem(item: WorkItem) {
    const idx = items.value.findIndex(i => i.id === item.id)
    const inScope = !filter.workflowId || inCurrentBoard(item)
    if (idx >= 0) {
      if (inScope) items.value.splice(idx, 1, mergeItem(items.value[idx], item))
      else items.value.splice(idx, 1)
    } else if (inScope) {
      items.value.unshift(item)
    }
    if (detail.value?.id === item.id) detail.value = mergeItem(detail.value, item)
    const fi = feed.items.findIndex(i => i.id === item.id)
    if (fi >= 0) feed.items.splice(fi, 1, mergeItem(feed.items[fi], item))
  }

  async function create(body: WorkItemCreate): Promise<WorkItem> {
    const item = await workItemsApi.create(body)
    applyServerItem(item)
    void refreshAfterWrite()
    return item
  }

  async function patch(id: number, p: WorkItemPatch): Promise<WorkItem> {
    const res = await workItemsApi.update(id, p)
    applyServerItem(res.item)
    if (res.changed) void refreshAfterWrite()
    return res.item
  }

  function setState(item: Pick<WorkItem, 'id'>, state: WorkState, note?: string) {
    return patch(item.id, { state, note })
  }

  /** 指派 / 改派（assignee 是工作流编号）；空串 = 撤销指派。只有人（网页）和管理 Agent 能指派。 */
  function assign(item: Pick<WorkItem, 'id'>, assignee: string, note?: string) {
    return patch(item.id, { assignee_workflow_id: assignee, note })
  }

  /** 在任务上发起改进需求；返回最新的任务与这段话交给智能体的方式。 */
  async function improve(id: number, text: string): Promise<{ item: WorkItem; delivered: WorkImproveDelivery }> {
    try {
      const res = await workItemsApi.improve(id, text)
      applyServerItem(res.item)
      void refreshAfterWrite()
      return res
    } catch (err) {
      // 服务端可能已经把任务改回进行中、只是没能交给智能体（502）：无论如何以服务端为准重新取一次
      void refreshAfterWrite()
      throw err
    }
  }

  async function remove(id: number) {
    await workItemsApi.remove(id)
    items.value = items.value.filter(i => i.id !== id)
    if (selectedId.value === id) closeDetail()
    void refreshAfterWrite()
  }

  async function linkSession(id: number, sessionId: string) {
    const item = await workItemsApi.linkSession(id, sessionId)
    applyServerItem(item)
    return item
  }

  async function unlinkSession(id: number, sessionId: string) {
    const item = await workItemsApi.unlinkSession(id, sessionId)
    applyServerItem(item)
    return item
  }

  /** 写入之后的校准：本地已经按返回值更新过，这里取一次服务端的计数、规划树统计与批次进度。 */
  async function refreshAfterWrite() {
    await load(true)
    if (selectedId.value) await openDetail(selectedId.value, true)
  }

  // ---------- 规划树写入 ----------

  /** 新建模块（parentId 为 0）或功能。 */
  async function createModule(name: string, parentId = 0, description = ''): Promise<WorkModule> {
    const m = await workModulesApi.create({
      workflow_id: filter.workflowId,
      parent_id: parentId || undefined,
      name,
      description: description || undefined,
    })
    await load(true)
    return m
  }

  async function renameModule(id: number, name: string): Promise<WorkModule> {
    const m = await workModulesApi.update(id, { name })
    await refreshAfterWrite()
    return m
  }

  /** 同级里上移 / 下移一格：服务端直接回传最新的树，不必再整表刷新。 */
  async function moveModule(id: number, direction: 'up' | 'down') {
    applyPlan(await workModulesApi.move(id, direction))
  }

  /** 删除模块（顶层模块连同它的功能）；任务不会被删，回到上一级或变成未归类。 */
  async function removeModule(id: number): Promise<WorkModuleDeleted> {
    const res = await workModulesApi.remove(id)
    await refreshAfterWrite()
    return res
  }

  // ---------- 服务端事件 ----------

  /**
   * 收到 work_item_updated / work_module_updated（或 SSE 重连）后的刷新。短时间内的多次事件合并成一次请求；
   * 任务页没打开过时只更新侧栏计数。MCP 的成批写入广播时不带具体编号（bulk），此时整表刷新、已打开的详情重取。
   */
  function onServerEvent(payload?: { id?: number; deleted?: boolean; workflow_id?: string; bulk?: boolean }) {
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(() => {
      refreshTimer = null
      if (loaded.value) void load(true)
      else void refreshCounts()
      if (feed.workflowId) void loadFeed(feed.workflowId, true)
      const open = selectedId.value
      if (open) {
        if (payload?.deleted && payload.id === open) closeDetail()
        else if (!payload?.id || payload.id === open) void openDetail(open, true)
      }
    }, 150)
  }

  // 实时流连上之后补取一遍（#243）：连上之前发生的变化没人听见。登记写在这里而不是由会话仓库直接调用，
  // 是为了「只有用到过这个仓库的页面才补取」——设置页这类从不显示任务清单的页面不会实例化它，也就不会为它多发请求。
  registerConnectHook('workItems', () => onServerEvent())

  return {
    items,
    counts,
    globalCounts,
    workflows,
    offered,
    offeredOf,
    modules,
    unassigned,
    boardKey,
    truncated,
    loaded,
    countsLoaded,
    loading,
    error,
    filter,
    batchExpanded,
    selectedId,
    detail,
    children,
    detailLoading,
    feed,
    feedBySession,
    feedSummary,
    feedSeqSet,
    feedTitleBySeq,
    feedSeqSignature,
    detailModules,
    tree,
    scoped,
    stateCounts,
    visible,
    groups,
    openTotal,
    currentStat,
    hasPlan,
    load,
    refreshCounts,
    openDetail,
    openBySeq,
    openByBoardSeq,
    closeDetail,
    loadFeed,
    watchFeed,
    setWorkflow,
    setKind,
    setAssignee,
    setQuick,
    setClosedDays,
    setSort,
    toggleState,
    select,
    toggleBatch,
    create,
    patch,
    setState,
    assign,
    improve,
    remove,
    linkSession,
    unlinkSession,
    createModule,
    renameModule,
    moveModule,
    removeModule,
    onServerEvent,
  }
})
