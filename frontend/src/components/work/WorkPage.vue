<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { toast } from 'vue-sonner'
import { useCoarseNow } from '../../composables/useCoarseNow'
import { ChevronDown, Clock3, ListChecks, ListTree, Plus, RefreshCw, Search, SlidersHorizontal, Users, X } from 'lucide-vue-next'
import Button from '../ui/button/Button.vue'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../ui/sheet'
import WorkModuleTree from './WorkModuleTree.vue'
import WorkTaskList from './WorkTaskList.vue'
import WorkItemDetail from './WorkItemDetail.vue'
import WorkItemCreateDialog from './WorkItemCreateDialog.vue'
import WorkTrackSummary from './WorkTrackSummary.vue'
import { CLOSED_WINDOWS, DEFAULT_CLOSED_DAYS, useWorkItemStore } from '../../stores/workItems'
import { useSessionStore } from '../../stores/session'
import { workErrorMessage } from '../../api/workItems'
import {
  ASSIGNEE_ALL,
  MODULE_ALL,
  MODULE_NONE,
  UNASSIGNED_LABEL,
  WORK_FILTER_ORDER,
  WORK_KINDS,
  WORK_KIND_META,
  WORK_QUICK_KEYS,
  WORK_QUICK_META,
  WORK_STATE_META,
  assigneeOptions,
  boardChipText,
  boardMemberList,
  buildWorkflowOptions,
  countActiveWorkFilters,
  displayModulePath,
  isWorkQuick,
  parseSeqQuery,
  quickCounts,
  summarizeCounts,
  workRef,
  type TaskRow,
  type WorkItem,
  type WorkState,
} from '../../lib/workItems'
import { backToSession } from '../../lib/sessionNav'
import { PHONE_MEDIA } from '../../lib/pendingSessions'
import { TONE_CLASSES } from './tone'
import PageHeader from '../shell/PageHeader.vue'
import PageBackButton from '../shell/PageBackButton.vue'
import PageState from '../shell/PageState.vue'

defineProps<{ sidebarCollapsed?: boolean }>()

const emit = defineEmits<{
  (e: 'open-mobile-sidebar'): void
  (e: 'expand-sidebar'): void
  (e: 'open-session', sessionId: string): void
}>()

const store = useWorkItemStore()
const sessionStore = useSessionStore()
const route = useRoute()
const router = useRouter()

/** 窄屏（< 1024px）：模块规划树收进抽屉。 */
const isNarrow = useMediaQuery('(max-width: 1023px)')
/** 手机宽度（< 768px）：筛选与总览收起来，首屏留给任务列表（#156 批 2）。 */
const isPhone = useMediaQuery(PHONE_MEDIA)

// 相对时间的时钟：30 秒一跳的共享慢时钟（composables/useCoarseNow）。页面自己只有详情抽屉要用它；
// 列表里的每一行自己读，不再由这里当 prop 灌给整列任务行（#219：200 行时每 30 秒一次整页重画，实测占主线程 28~66 ms）。
const now = useCoarseNow()

// ---------- 项目（工作流） ----------

const routeWorkflow = computed(() => {
  const p = route.params.workflowId
  return (Array.isArray(p) ? p[0] : p) || ''
})
const sessionWorkflow = computed(() => (sessionStore.selectedSession || sessionStore.currentSession)?.workflow_id || '')
const knownWorkflows = computed(() => sessionStore.sessions.map(s => ({ workflow_id: s.workflow_id || '', title: s.title })))
const currentWorkflow = computed(() => store.filter.workflowId)
// 同一个项目的工作流共用一张任务板（#175）：下拉里一张板只出一项；代表它的成员优先取正在看的、其次当前会话的
const workflowOptions = computed(() =>
  buildWorkflowOptions(store.workflows, knownWorkflows.value, {
    selected: currentWorkflow.value,
    session: sessionWorkflow.value,
    selectedBoard: store.boardKey,
  })
)
/** 当前这张板由几个工作流共用：顶条说明是谁在共用；只有一个工作流时没有这条。 */
const boardMembers = computed(() => boardMemberList(store.currentStat, knownWorkflows.value))
const boardChip = computed(() => boardChipText(store.currentStat))
const boardOpen = ref(false)
/** 承接者过滤的选项（#175 B2）：清单里一条指派都没有时为空——没有协作就不出现这个过滤，单工作流的界面不变。 */
const assigneeOpts = computed(() => assigneeOptions(store.items, knownWorkflows.value))
/**
 * 协作快捷筛选（#180 B3b）：指派给我 / 我派出的 / 待接取。「我」= 正在看的这个工作流；和承接者过滤一样，清单里没有指派时不出现。
 * 数字是还没结束的任务里各有几个（已结束的是历史）。
 */
const quickChips = computed(() => {
  if (!assigneeOpts.value.length) return []
  const counts = quickCounts(store.items, store.filter.workflowId)
  return WORK_QUICK_KEYS.map(key => ({ key, ...WORK_QUICK_META[key], count: counts[key] }))
})
// 选中的快捷项已经没有任务了（待接取的都处理完了）：回到「全部」，免得芯片消失、列表却是空的
watch(quickChips, chips => {
  const sel = store.filter.quick
  if (sel && !chips.some(c => c.key === sel && c.count > 0)) store.setQuick('')
})
// 选中的承接者已经不在清单里了（任务都做完 / 撤销了）：回到「全部」，免得过滤框消失、列表却还被它筛着
watch(assigneeOpts, opts => {
  const sel = store.filter.assignee
  if (sel !== ASSIGNEE_ALL && !opts.some(o => o.value === sel)) store.setAssignee(ASSIGNEE_ALL)
})
/** 项目下拉：当前项目即使还没有任何任务也要在里面，免得下拉框显示成空白。 */
const projectSelectOptions = computed(() => {
  const list = [...workflowOptions.value]
  const cur = currentWorkflow.value
  if (cur && !list.some(o => o.id === cur)) list.unshift({ id: cur, label: cur, active: 0 })
  return list
})

/** 进入某个项目：同一个项目只是重新取一次数据，保留当前的选中项与筛选。 */
function enter(id: string) {
  if (store.filter.workflowId === id) void store.load()
  else store.setWorkflow(id)
}

/** /work 没有带项目：用上次看的，其次是当前会话所属的，最后是最近有动静的。 */
async function chooseDefaultProject() {
  if (!store.workflows.length) await store.refreshCounts()
  const stats = store.workflows
  const remembered = store.filter.workflowId
  const pick = (remembered && stats.some(w => w.workflow_id === remembered) ? remembered : '') || sessionWorkflow.value || stats[0]?.workflow_id || ''
  if (pick) await router.replace({ name: 'work', params: { workflowId: pick } })
  else store.setWorkflow('')
}

function switchProject(id: string) {
  if (!id || id === currentWorkflow.value) return
  void router.replace({ name: 'work', params: { workflowId: id } })
}

watch(routeWorkflow, id => {
  if (id) enter(id)
})

/**
 * 从会话页「本轮任务」、收尾门清单、「指派久无人接」的提醒点进来：/work/<项目>?task=<数据库编号> 直接打开这个任务的详情，
 * 随后把参数从地址栏去掉（刷新时不再重复打开）。注意是任务的数据库编号（API 里的 id），不是界面上写的 #N（板内序号，见下面的 ?seq=，#242）。
 *
 * 顺序不能反：必须先把 ?task= 从地址栏和这一条历史记录里换掉，再打开详情。详情抽屉打开时会往浏览器历史里压一条哨兵记录
 * （见 useDialogBackGesture），关闭时再用 history.back() 弹掉；如果还没来得及换掉地址就先开抽屉，带着 ?task= 的记录会留在哨兵下面，
 * 关闭时的回退会把 ?task= 又带回来，这里随即再开一次——抽屉怎么也关不掉。
 */
async function consumeTaskQuery() {
  const raw = route.query.task
  const v = Array.isArray(raw) ? raw[0] : raw
  if (!v) return
  const id = Number(v)
  const params = { ...route.params }
  await router.replace({ name: 'work', params, query: {} })
  if (Number.isInteger(id) && id > 0) void store.openDetail(id)
}
watch(() => route.query.task, consumeTaskQuery)

/**
 * 从正文里的 #N 链接（新标签页打开 / 复制链接）点进来：/work/<项目>?seq=<板内序号> 打开这个序号对应的任务详情（#242）。
 *
 * #N 是板内序号，不是上面 ?task= 认的数据库编号（两个数不同：把 #N 当 ?task= 会打开别的任务）；
 * 所以由服务端按这个项目的任务板把序号解析成任务，不依赖页面已加载的清单——新标签页刚打开时清单还没取到，
 * 已暂缓 / 已结束 / 很早以前的任务本来也不在默认清单里。
 * 与 consumeTaskQuery 同一个理由：先把参数从地址栏和这一条历史记录里换掉、再打开详情，其他参数原样保留。
 * 序号不是正整数：只把参数去掉；这张板上没有这个序号：提示一句、不弹详情。
 */
async function consumeSeqQuery() {
  const raw = route.query.seq
  if (raw === undefined) return // 先判断再换地址：换掉之后这个监听会再触发一次，那一次什么都不用做
  const { seq: _consumed, ...rest } = route.query
  const seq = parseSeqQuery(raw)
  const target = routeWorkflow.value
  await router.replace({ name: 'work', params: { ...route.params }, query: rest })
  if (!seq || !target) return // 没带项目（稍后会选默认项目）或序号不合法：只把参数去掉
  if (!(await store.openByBoardSeq(target, seq))) toast.info(`找不到任务 #${seq}，它可能已被删除`)
}
watch(() => route.query.seq, consumeSeqQuery)

/**
 * 从「指派久无人接」的提醒点进来：/work/<项目>?quick=offered 直接套上对应的协作快捷筛选（#180 B3d）。
 *
 * 套筛选必须等这个项目的清单取到之后：快捷项由清单里的指派算出来，先套的话会被上面的 watch(quickChips) 当作「没有任务」立刻取消；
 * 换项目时 store.setWorkflow 也会把它清掉。所以用一个一次性的监听等「清单就绪」再套，套上（或判断没有可套的）就停。
 * 地址栏里的 ?quick= 先换掉再套（与 consumeTaskQuery 同一个理由：不在历史记录里留下带参数的地址），其他参数原样保留。
 * 套之前先清掉别的筛选（搜索词 / 类型 / 承接者 / 模块）：提醒承诺的是「这些待接取的指派」，被旧筛选藏住就成了空头链接。
 */
let stopQuickWait: (() => void) | null = null
function cancelQuickWait() {
  stopQuickWait?.()
  stopQuickWait = null
}
async function consumeQuickQuery() {
  const raw = route.query.quick
  const v = Array.isArray(raw) ? raw[0] : raw
  if (!v) return // 先判断再取消：下面换掉地址后这个监听会再触发一次，那一次不能取消正在等的那个
  cancelQuickWait()
  const { quick: _consumed, ...rest } = route.query
  await router.replace({ name: 'work', params: { ...route.params }, query: rest })
  const target = routeWorkflow.value
  if (!target || !isWorkQuick(v)) return // 没带项目（稍后会选默认项目）或不认识的取值：只把参数去掉
  const ready = () => store.loaded && !store.loading && store.filter.workflowId === target && routeWorkflow.value === target
  const apply = () => {
    // 清单里已经没有这类任务了（到点之后被人接走了）：不套，免得列表是空的
    if (!quickChips.value.some(c => c.key === v && c.count > 0)) return
    clearFilters()
    store.setQuick(v)
  }
  if (ready()) {
    apply()
    return
  }
  const stop = watch(ready, ok => {
    if (!ok) return
    stop()
    if (stopQuickWait === stop) stopQuickWait = null
    apply()
  })
  stopQuickWait = stop
}
watch(() => route.query.quick, consumeQuickQuery)
// 换了项目：还在等的深链属于上一个项目，作废
watch(routeWorkflow, cancelQuickWait)

onMounted(() => {
  if (routeWorkflow.value) enter(routeWorkflow.value)
  else void chooseDefaultProject()
  void consumeTaskQuery()
  void consumeSeqQuery()
  void consumeQuickQuery()
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  cancelQuickWait()
  store.closeDetail()
})

/** 回到会话页：从会话页头的「任务」进来时，浏览器的返回也是同一个去处（与项目页的「会话」共用 lib/sessionNav）。 */
function goBack() {
  backToSession(router)
}

// ---------- 列表与筛选 ----------

const summary = computed(() => summarizeCounts(store.counts))
const hasAnyTask = computed(() => store.items.length > 0)
const isProjectEmpty = computed(() => store.loaded && !store.loading && !hasAnyTask.value && !store.hasPlan)
const hasFilter = computed(
  () => !!store.filter.kind || !!store.filter.query.trim() || store.filter.assignee !== ASSIGNEE_ALL || !!store.filter.quick
)
const showClosedWindow = computed(() => store.filter.states.includes('done') || store.filter.states.includes('shelved'))

const selectionLabel = computed(() => {
  const sel = store.filter.selection
  if (sel === MODULE_ALL) return '全部任务'
  if (sel === MODULE_NONE) return UNASSIGNED_LABEL
  return displayModulePath(store.modules.find(m => m.id === sel)?.path) || '全部任务'
})

const hiddenShelved = computed(() => (store.filter.states.includes('shelved') ? 0 : store.stateCounts.shelved))

// 手机宽度的折叠（#156 批 2）：类型 / 已结束的时间窗 / 排序收进「筛选」，阶段与状态分布收成一行；桌面（≥md）仍是常驻的工具条与完整总览。
// 筛选条件会记在本机、下次进来还在，所以收起时按钮上的数字要如实告诉你「有几条非默认的条件在生效」。
const filtersOpen = ref(false)
const overviewOpen = ref(false)
const activeFilters = computed(() =>
  countActiveWorkFilters(
    {
      kind: store.filter.kind,
      assignee: store.filter.assignee,
      quick: store.filter.quick,
      sort: store.filter.sort,
      closedDays: store.filter.closedDays,
      showClosedWindow: showClosedWindow.value,
    },
    DEFAULT_CLOSED_DAYS
  )
)

/** 总览条的范围说明：已结束的任务只加载了最近这段时间内关闭的，统计与下方列表一致。 */
const overviewScopeNote = computed(() => {
  const days = store.filter.closedDays
  if (!days) return ''
  const label = CLOSED_WINDOWS.find(w => w.days === days)?.label || `近 ${days} 天`
  return `已完成 / 已搁置的任务只统计${label}内关闭的，与下方列表一致`
})

const searchInput = ref(store.filter.query)
let searchTimer: ReturnType<typeof setTimeout> | null = null
function onSearch() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => (store.filter.query = searchInput.value), 120)
}
// 切换项目会清空搜索条件：输入框里的字要跟着清掉，免得框里有字而列表没有被筛选
watch(
  () => store.filter.query,
  query => {
    if (query !== searchInput.value) searchInput.value = query
  }
)

function clearFilters() {
  searchInput.value = ''
  store.filter.query = ''
  store.setKind('')
  store.setAssignee(ASSIGNEE_ALL)
  store.setQuick('')
  store.select(MODULE_ALL)
}

// ---------- 操作 ----------

const busyId = ref<number | null>(null)

async function handleAction(item: WorkItem, to: WorkState) {
  busyId.value = item.id
  try {
    const updated = await store.setState(item, to)
    if (to === 'done') toast.success(`已标记完成 ${workRef(updated)}`)
  } catch (err) {
    toast.error(workErrorMessage(err))
  } finally {
    busyId.value = null
  }
}

const detailOpen = computed({
  get: () => store.selectedId !== null,
  set: v => {
    if (!v) store.closeDetail()
  },
})

function openItem(id: number) {
  void store.openDetail(id)
}

function toggleBatch(row: TaskRow) {
  store.toggleBatch(row)
}

const createOpen = ref(false)
const presetParent = ref<Pick<WorkItem, 'id' | 'workflow_id' | 'seq' | 'title'> | null>(null)
const createDefaultWorkflow = computed(() => currentWorkflow.value || sessionWorkflow.value || workflowOptions.value[0]?.id || '')
const createDefaultModule = computed(() => (typeof store.filter.selection === 'number' ? store.filter.selection : 0))

function openCreate(parent: WorkItem | null = null) {
  presetParent.value = parent ? { id: parent.id, workflow_id: parent.workflow_id, seq: parent.seq, title: parent.title } : null
  createOpen.value = true
}

function onCreated(item: WorkItem) {
  // 新建的任务直接打开，方便继续补充细节
  void store.openDetail(item.id)
}

function openSession(sessionId: string) {
  store.closeDetail()
  emit('open-session', sessionId)
}

// ---------- 窄屏：规划树抽屉 ----------

const treeDrawerOpen = ref(false)
watch(isNarrow, narrow => {
  if (!narrow) treeDrawerOpen.value = false
})

function stateChipClass(s: WorkState): string {
  return store.filter.states.includes(s)
    ? TONE_CLASSES[WORK_STATE_META[s].tone].badge + ' font-medium'
    : 'border-border/70 text-muted-foreground/70 hover:bg-muted'
}
</script>

<template>
  <!-- 进入任务页：整页淡入并从下方升起一点（一次性、300ms、只动 opacity / transform；「减少动态效果」下关闭） -->
  <div
    class="flex min-h-0 flex-1 animate-in flex-col bg-background fade-in-0 slide-in-from-bottom-1 duration-300 ease-out motion-reduce:animate-none"
    data-testid="work-page"
  >
    <!-- 顶栏：与会话页、群聊 / 项目 / 文档页共用同一个页头骨架（PageHeader：同高同底线，汉堡与返回在左、主操作在右） -->
    <PageHeader :sidebar-collapsed="sidebarCollapsed" @open-mobile-sidebar="emit('open-mobile-sidebar')" @expand-sidebar="emit('expand-sidebar')">
      <template #leading>
        <PageBackButton label="会话" title="返回会话" data-testid="work-back" @click="goBack" />
      </template>

      <ListChecks class="hidden size-4 shrink-0 text-foreground sm:inline-block" />
      <h2 class="hidden shrink-0 font-mono text-sm font-semibold tracking-tight text-foreground sm:block">任务清单</h2>
      <select
        :value="currentWorkflow"
        class="h-8 min-w-0 max-w-[12rem] flex-1 cursor-pointer rounded-xs border border-border/80 bg-background px-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none sm:max-w-[18rem] sm:flex-none"
        aria-label="切换项目"
        data-testid="work-project"
        @change="(e: Event) => switchProject((e.target as HTMLSelectElement).value)"
      >
        <option v-if="!currentWorkflow" value="">还没有项目</option>
        <option v-for="w in projectSelectOptions" :key="w.id" :value="w.id">{{ w.label }}{{ w.active ? `（${w.active}）` : '' }}</option>
      </select>
      <span v-if="summary" class="hidden truncate rounded-xs bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground md:inline" data-testid="work-summary">{{ summary }}</span>

      <template #actions>
        <Button size="sm" class="h-8 shrink-0 gap-1 text-xs" data-testid="work-new" @click="openCreate()">
          <Plus class="size-3.5" /><span class="hidden sm:inline">新建任务</span><span class="sm:hidden">新建</span>
        </Button>
      </template>
    </PageHeader>

    <!-- 共享板（#175）：同一个项目里的几个工作流共用这张任务板，序号与规划树是一套；只有一个工作流时不出现。点开看是哪几个工作流在共用 -->
    <div v-if="boardMembers.length" class="shrink-0 border-b border-border/60 bg-muted/10 px-3 py-1.5 sm:px-6" data-testid="work-board">
      <button
        type="button"
        class="inline-flex h-7 max-w-full cursor-pointer items-center gap-1.5 rounded-xs px-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        :aria-expanded="boardOpen"
        title="同一个项目里的几个工作流共用这张任务板：任务编号是一套，规划树是一棵"
        data-testid="work-board-chip"
        @click="boardOpen = !boardOpen"
      >
        <Users class="size-3.5 shrink-0" aria-hidden="true" />
        <span class="truncate">{{ boardChip }}</span>
        <ChevronDown class="size-3 shrink-0 transition-transform" :class="boardOpen ? 'rotate-180' : ''" aria-hidden="true" />
      </button>
      <ul v-if="boardOpen" class="mt-1 flex flex-wrap gap-1.5 pb-0.5" data-testid="work-board-members">
        <li
          v-for="m in boardMembers"
          :key="m.id"
          class="max-w-full truncate rounded-xs bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground"
          :class="m.id === currentWorkflow ? 'ring-1 ring-primary/50' : ''"
          :title="m.id"
          :data-testid="`work-board-member-${m.id}`"
        >
          {{ m.label }}
        </li>
      </ul>
    </div>

    <!-- 工具条：模块（窄屏）/ 搜索 / 状态 / 类型 / 排序 -->
    <div class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border/60 px-3 py-2 sm:px-6">
      <button
        v-if="isNarrow"
        type="button"
        class="inline-flex h-7 max-w-[11rem] cursor-pointer items-center gap-1 rounded-xs border border-border/80 px-2 text-xs text-foreground hover:bg-muted"
        data-testid="work-tree-open"
        @click="treeDrawerOpen = true"
      >
        <ListTree class="size-3.5 shrink-0 text-muted-foreground" /><span class="truncate">{{ selectionLabel }}</span>
      </button>

      <div class="relative min-w-0 flex-1 basis-28 sm:flex-none sm:basis-auto">
        <Search class="absolute left-2 top-2 size-3.5 text-muted-foreground" />
        <input
          v-model="searchInput"
          type="search"
          placeholder="搜索标题、模块、进度"
          class="h-7 w-full rounded-xs border border-border/70 bg-background pl-7 pr-2 text-xs placeholder:text-muted-foreground focus:border-primary focus:outline-none sm:w-56"
          data-testid="work-search"
          @input="onSearch"
        />
      </div>

      <!-- 手机宽度：类型 / 已结束的时间窗 / 排序收成这一个按钮（与项目知识库页同一个做法；右上角的数字是改成了非默认值的条数），点开才展开；桌面（≥md）不出现 -->
      <button
        type="button"
        class="relative inline-flex h-7 shrink-0 cursor-pointer items-center gap-1 rounded-xs border border-border/80 px-2 text-xs text-foreground hover:bg-muted md:hidden"
        :class="filtersOpen || activeFilters ? 'border-primary/60 text-primary' : ''"
        :aria-expanded="filtersOpen"
        data-testid="work-filter-toggle"
        @click="filtersOpen = !filtersOpen"
      >
        <SlidersHorizontal class="size-3.5" aria-hidden="true" />
        <span>筛选</span>
        <span v-if="activeFilters" class="rounded-full bg-primary px-1.5 text-[11px] leading-4 text-primary-foreground" data-testid="work-filter-count">{{ activeFilters }}</span>
      </button>

      <div class="flex min-w-0 flex-wrap items-center gap-1 max-md:basis-full" role="group" aria-label="按状态筛选">
        <button
          v-for="s in WORK_FILTER_ORDER"
          :key="s"
          type="button"
          class="inline-flex cursor-pointer items-center gap-1 rounded-xs border px-1.5 py-0.5 text-[11px] transition-colors"
          :class="stateChipClass(s)"
          :aria-pressed="store.filter.states.includes(s)"
          :data-testid="`work-chip-${s}`"
          @click="store.toggleState(s)"
        >
          <span class="size-1.5 rounded-full" :class="TONE_CLASSES[WORK_STATE_META[s].tone].dot" />{{ WORK_STATE_META[s].label }}
          <span class="font-mono text-[10px] tabular-nums opacity-80" :data-testid="`work-chip-count-${s}`">{{ store.stateCounts[s] }}</span>
        </button>
      </div>

      <!-- 类型 / 时间窗 / 排序 / 刷新：桌面（≥md）是工具条里的散件（md:contents 让它们直接成为工具条的子项）；手机收在这里，点「筛选」展开成独立的一行 -->
      <div
        class="md:contents"
        :class="filtersOpen ? 'max-md:flex max-md:basis-full max-md:flex-wrap max-md:items-center max-md:gap-2' : 'max-md:hidden'"
        data-testid="work-filters"
      >
        <select
          :value="store.filter.kind"
          class="h-7 cursor-pointer rounded-xs border border-border/70 bg-background px-1.5 text-[11px] text-foreground focus:border-primary focus:outline-none"
          aria-label="按类型筛选"
          data-testid="work-kind-filter"
          @change="(e: Event) => store.setKind((e.target as HTMLSelectElement).value)"
        >
          <option value="">全部类型</option>
          <option v-for="k in WORK_KINDS" :key="k" :value="k">{{ WORK_KIND_META[k].label }}</option>
        </select>

        <!-- 协作快捷项（共享任务板）：指派给我 / 我派出的 / 待接取；再点一次取消，数字是还没结束的任务数 -->
        <div v-if="quickChips.length" class="inline-flex items-center gap-1" role="group" aria-label="协作快捷筛选" data-testid="work-quick">
          <button
            v-for="c in quickChips"
            :key="c.key"
            type="button"
            class="inline-flex h-7 cursor-pointer items-center gap-1 rounded-xs border px-1.5 text-[11px] transition-colors"
            :class="store.filter.quick === c.key ? 'border-primary/50 bg-primary/10 font-medium text-primary' : 'border-border/70 text-muted-foreground hover:bg-muted'"
            :aria-pressed="store.filter.quick === c.key"
            :title="c.hint"
            :data-testid="`work-quick-${c.key}`"
            @click="store.setQuick(c.key)"
          >
            {{ c.label }}
            <span class="font-mono text-[10px] tabular-nums opacity-80" :data-testid="`work-quick-count-${c.key}`">{{ c.count }}</span>
          </button>
        </div>

        <!-- 承接者（共享任务板）：只有清单里出现了指派才有；选项后面的数字是这个承接者手上的任务数 -->
        <select
          v-if="assigneeOpts.length"
          :value="store.filter.assignee"
          class="h-7 max-w-[10rem] cursor-pointer rounded-xs border border-border/70 bg-background px-1.5 text-[11px] text-foreground focus:border-primary focus:outline-none"
          aria-label="按承接者筛选"
          data-testid="work-assignee-filter"
          @change="(e: Event) => store.setAssignee((e.target as HTMLSelectElement).value)"
        >
          <option v-for="o in assigneeOpts" :key="o.value" :value="o.value">{{ o.label }}（{{ o.count }}）</option>
        </select>

        <select
          v-if="showClosedWindow"
          :value="store.filter.closedDays"
          class="h-7 cursor-pointer rounded-xs border border-border/70 bg-background px-1.5 text-[11px] text-muted-foreground focus:border-primary focus:outline-none"
          aria-label="已完成与已搁置的任务显示多久以内的"
          title="已完成 / 已搁置的任务只显示最近这段时间内关闭的"
          data-testid="work-closed-days"
          @change="(e: Event) => store.setClosedDays(Number((e.target as HTMLSelectElement).value))"
        >
          <option v-for="w in CLOSED_WINDOWS" :key="w.days" :value="w.days">已结束 · {{ w.label }}</option>
        </select>

        <div class="ml-auto flex items-center gap-2 max-md:ml-0">
          <div class="inline-flex overflow-hidden rounded-xs border border-border/70" role="group" aria-label="排序方式">
            <button
              type="button"
              class="inline-flex h-7 cursor-pointer items-center gap-1 px-2 text-[11px] transition-colors"
              :class="store.filter.sort === 'plan' ? 'bg-primary/10 font-medium text-primary' : 'text-muted-foreground hover:bg-muted'"
              :aria-pressed="store.filter.sort === 'plan'"
              title="跟随左侧规划树的顺序，每个模块里先看正在做的"
              data-testid="work-sort-plan"
              @click="store.setSort('plan')"
            ><ListTree class="size-3" />规划顺序</button>
            <button
              type="button"
              class="inline-flex h-7 cursor-pointer items-center gap-1 border-l border-border/70 px-2 text-[11px] transition-colors"
              :class="store.filter.sort === 'recent' ? 'bg-primary/10 font-medium text-primary' : 'text-muted-foreground hover:bg-muted'"
              :aria-pressed="store.filter.sort === 'recent'"
              title="不分组，最近有进展的排在最前"
              data-testid="work-sort-recent"
              @click="store.setSort('recent')"
            ><Clock3 class="size-3" />最近进展</button>
          </div>
          <button
            type="button"
            class="hidden cursor-pointer items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground md:inline-flex"
            title="重新加载"
            @click="store.load()"
          >
            <RefreshCw class="size-3" :class="store.loading ? 'animate-spin' : ''" />刷新
          </button>
        </div>
      </div>
    </div>

    <!-- 整体进度：当前范围（模块 / 类型 / 关键字筛选之后，不受状态筛选影响）里任务的阶段与状态分布。
         手机宽度收成一行（两条分布短条 + 整体完成度），点开才展开阶段 / 状态的图例；桌面（≥md）一直是完整版 -->
    <div v-if="store.loaded && store.scoped.length" class="shrink-0 border-b border-border/60 bg-muted/10 px-3 py-2 sm:px-6" data-testid="work-overview">
      <button
        v-if="isPhone"
        type="button"
        class="flex min-h-6 w-full cursor-pointer items-center gap-2 text-left"
        :aria-expanded="overviewOpen"
        data-testid="work-overview-toggle"
        @click="overviewOpen = !overviewOpen"
      >
        <span class="shrink-0 text-[10px] text-muted-foreground">整体进度</span>
        <!-- 展开后下面就是完整版，这一行里的短条与数字不再重复 -->
        <WorkTrackSummary v-if="!overviewOpen" :items="store.scoped" :scope-note="overviewScopeNote" compact />
        <ChevronDown
          class="ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform duration-200 motion-reduce:transition-none"
          :class="overviewOpen ? 'rotate-180' : ''"
          aria-hidden="true"
        />
      </button>
      <WorkTrackSummary v-if="!isPhone || overviewOpen" :items="store.scoped" :scope-note="overviewScopeNote" :class="isPhone ? 'mt-2' : ''" />
    </div>

    <p v-if="store.error" class="mx-3 mt-3 flex items-center justify-between gap-3 rounded-xs border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive sm:mx-6" role="alert">
      <span>{{ store.error }}</span>
      <button type="button" class="shrink-0 cursor-pointer underline" @click="store.load()">重试</button>
    </p>

    <!-- 清单被截断：最近更新的任务取满了上限，更早更新的没有显示；如实说明，免得人以为「就这么多」（项目下拉里括号中的数字是整张板的开放任务数） -->
    <p
      v-if="store.truncated"
      class="mx-3 mt-3 rounded-xs border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-700 sm:mx-6 dark:text-amber-300"
      role="status"
      data-testid="work-truncated"
    >
      任务太多，只显示了最近更新的 {{ store.items.length }} 条，更早更新的被略去了；用上面的状态、类型或搜索缩小范围，可以看到它们。
    </p>

    <div class="flex min-h-0 flex-1">
      <!-- 宽屏：规划树常驻左侧 -->
      <aside v-if="!isNarrow" class="flex w-72 shrink-0 flex-col border-r border-border/60 bg-muted/10" data-testid="work-sidebar-tree">
        <WorkModuleTree />
      </aside>

      <section class="relative min-h-0 min-w-0 flex-1 overflow-y-auto" data-testid="work-scroll">
        <!-- 这个项目还没有任何任务和规划：说明它是什么，并给出两条出路 -->
        <PageState
          v-if="isProjectEmpty"
          kind="empty"
          :icon="ListChecks"
          :title="currentWorkflow ? '这个项目还没有任务清单' : '还没有任务'"
          class="min-h-full animate-in fade-in duration-300 motion-reduce:animate-none"
          data-testid="work-empty"
        >
          <p>
            任务清单独立于会话，用来记住「还有哪些事要做、做到哪了」。智能体会先用
            <code class="rounded-xs bg-muted px-1 font-mono">task_board</code> 规划模块、成批建任务，再随工作推进更新进度；
            每件事随开发流程流转：待办 → 进行中 → 验证中 → 已完成；验证通过后由智能体直接置为「已完成」并写下结论。
          </p>
          <p v-if="currentWorkflow">你也可以在左侧「编辑规划」里先把模块建好，智能体之后会按这些模块标注任务。</p>
          <template #actions>
            <Button size="sm" class="h-8 gap-1 text-xs" @click="openCreate()"><Plus class="size-3.5" />新建第一个任务</Button>
          </template>
        </PageState>

        <template v-else>
          <PageState v-if="!store.loaded && store.loading" kind="loading" title="任务加载中…" data-testid="work-loading" />

          <WorkTaskList
            v-else-if="store.groups.length"
            :groups="store.groups"
            :sort="store.filter.sort"
            :unassigned="store.unassigned"
            :selected-id="store.selectedId"
            :busy-id="busyId"
            :known="knownWorkflows"
            :viewer="currentWorkflow"
            :shared-board="boardMembers.length > 0"
            @open="openItem"
            @action="handleAction"
            @toggle="toggleBatch"
          />

          <PageState
            v-else-if="store.loaded"
            kind="empty"
            :title="`「${selectionLabel}」里没有符合当前筛选的任务`"
            class="animate-in fade-in duration-300 motion-reduce:animate-none"
            data-testid="work-no-match"
          >
            <p v-if="hiddenShelved">另有 {{ hiddenShelved }} 个已搁置的任务被隐藏，点上面的「已搁置」可以显示。</p>
            <template v-if="hasFilter || store.filter.selection !== MODULE_ALL" #actions>
              <button type="button" class="inline-flex cursor-pointer items-center gap-0.5 text-xs text-primary hover:underline" @click="clearFilters">
                <X class="size-3" />清除筛选，回到全部任务
              </button>
            </template>
          </PageState>
        </template>
      </section>
    </div>

    <!-- 窄屏：规划树抽屉 -->
    <Sheet v-model:open="treeDrawerOpen">
      <SheetContent side="left" class="flex w-[88vw] max-w-sm flex-col gap-0 p-0" data-testid="work-tree-drawer">
        <SheetHeader class="sr-only">
          <SheetTitle>模块规划</SheetTitle>
          <SheetDescription>按模块或功能筛选任务，也可以编辑规划。</SheetDescription>
        </SheetHeader>
        <WorkModuleTree drawer @picked="treeDrawerOpen = false" />
      </SheetContent>
    </Sheet>

    <WorkItemDetail
      v-model:open="detailOpen"
      :sessions="sessionStore.sessions"
      :now="now"
      @open-session="openSession"
      @open-item="openItem"
      @add-child="openCreate"
    />

    <WorkItemCreateDialog
      v-model:open="createOpen"
      :workflow-options="workflowOptions"
      :default-workflow-id="createDefaultWorkflow"
      :default-module-id="createDefaultModule"
      :preset-parent="presetParent"
      @created="onCreated"
    />
  </div>
</template>
