<script setup lang="ts">
import { computed, nextTick, ref, watch, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { useSessionStore } from '../stores/session'
import { useTaskStore } from '../stores/task'
import { useNotifyStore } from '../stores/notify'
import { useNotifyPrefsStore } from '../stores/notifyPrefs'
import { useThemeStore } from '../stores/theme'
import { useSettingsStore } from '../stores/settings'
import { useAuthStore } from '../stores/auth'
import { useTerminalStore } from '../stores/terminal'
import { useWorkItemStore } from '../stores/workItems'
import { useAgentStateStore } from '../stores/agentState'
import { useChatStore } from '../stores/chat'
import { useBranchStore } from '../stores/branches'
import { arrangeBranchTree } from '../lib/branch'
import SidebarSessions from './sidebar/SidebarSessions.vue'
import SidebarGroups from './sidebar/SidebarGroups.vue'
import SidebarProjects from './sidebar/SidebarProjects.vue'
import { summarizeCounts } from '../lib/workItems'
import { displayRounds } from '../lib/workflowRounds'
import Button from './ui/button/Button.vue'
import Badge from './ui/badge/Badge.vue'
import ThemeAutoIcon from './ThemeAutoIcon.vue'
import ProjectSwitcher from './ProjectSwitcher.vue'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem
} from './ui/dropdown-menu'
import {
  MessageSquareCode,
  Layers,
  History,
  Sun,
  Moon,
  Laptop,
  Bell,
  BellMinus,
  BellOff,
  Volume2,
  VolumeX,
  Radio,
  Settings,
  Search,
  Clock,
  FolderGit2,
  Workflow,
  Sparkles,
  Bot,
  Archive,
  Pin,
  PinOff,
  Edit2,
  Copy,
  Check,
  ChevronDown,
  X,
  PanelLeftClose,
  Hash,
  ListChecks,
  MessageSquarePlus
} from 'lucide-vue-next'
import type { FeedbackSession, TaskSummary } from '../api/types'
import SidebarItemCard from './sidebar/SidebarItemCard.vue'
import RenameSessionDialog from './sidebar/RenameSessionDialog.vue'
import NewSessionDialog from './sidebar/NewSessionDialog.vue'
import { toast } from 'vue-sonner'

export interface UnifiedItem {
  id: string
  type: 'feedback' | 'task'
  title: string
  subtitle?: string
  summary: string
  status: string
  created_at: string
  first_created_at?: string
  updated_at?: string
  project_directory?: string
  workflow_id?: string
  unread_count?: number
  revision?: number
  sequence?: number
  rounds_count?: number
  /** 该工作流最近一次带交付锚点的那轮所申报的提交哈希（从未申报过则为空）。 */
  latest_git_commit?: string
  raw: FeedbackSession | TaskSummary
}

const sessionStore = useSessionStore()
const taskStore = useTaskStore()
const notifyStore = useNotifyStore()
const notifyPrefs = useNotifyPrefsStore()
const themeStore = useThemeStore()
const settingsStore = useSettingsStore()
const authStore = useAuthStore()
const terminalStore = useTerminalStore()
const workStore = useWorkItemStore()
const agentStateStore = useAgentStateStore()
const workSummary = computed(() => summarizeCounts(workStore.globalCounts))
const router = useRouter()

// ExecMesh 模式切换器只在服务端总闸开启（已设登录密码等）时出现；登录态确认后再读取，避免未登录时触发 401。
watch(
  () => authStore.isAuthenticated,
  ok => {
    if (ok) void terminalStore.loadStatus()
  },
  { immediate: true }
)

function handleOpenSettings() {
  emit('closeMobile')
  if (window.innerWidth < 768) {
    router.push({ path: '/settings', query: { module: 'relay' } })
  } else {
    settingsStore.openSettings()
  }
}

function handleOpenWork() {
  emit('closeMobile')
  emit('openWork')
}

function handleOpenArchive() {
  emit('closeMobile')
  if (window.innerWidth < 768) {
    router.push('/archive')
  } else {
    emit('openArchive')
  }
}

const props = defineProps<{
  activeItemId?: string | null
  activeItemType?: 'feedback' | 'task' | null
  mobileOpen?: boolean
  collapsed?: boolean
  /** 右侧正在显示任务清单：此时侧栏里的会话都不应显示为选中。 */
  workActive?: boolean
  /** 右侧是群聊页：会话列表里没有选中项（任务清单入口也不高亮）。 */
  groupActive?: boolean
}>()

const emit = defineEmits<{
  (e: 'selectItem', item: UnifiedItem): void
  (e: 'openArchive'): void
  (e: 'openWork'): void
  (e: 'closeMobile'): void
  (e: 'toggleCollapse'): void
}>()

// 网页手动新建会话（#174）：底部工具栏的「新建会话」按钮打开弹窗；建好并关闭弹窗后进入新会话
const newSessionOpen = ref(false)

const searchQuery = ref('')
const filterType = ref<'all' | 'pending' | 'feedback' | 'task' | 'completed' | 'archived'>('all')

// Pinned Session IDs (localStorage managed)
const pinnedIds = ref<string[]>(JSON.parse(localStorage.getItem('relaymesh_pinned_ids') || '[]'))

function togglePin(id: string) {
  const idx = pinnedIds.value.indexOf(id)
  if (idx >= 0) {
    pinnedIds.value.splice(idx, 1)
  } else {
    pinnedIds.value.push(id)
  }
  localStorage.setItem('relaymesh_pinned_ids', JSON.stringify(pinnedIds.value))
}

function isPinned(id: string) {
  return pinnedIds.value.includes(id)
}

// Right Click Context Menu State
const contextMenu = ref<{
  visible: boolean
  x: number
  y: number
  item: UnifiedItem | null
}>({
  visible: false,
  x: 0,
  y: 0,
  item: null
})

const copiedNotice = ref(false)

function openContextMenu(e: MouseEvent, item: UnifiedItem) {
  e.preventDefault()
  e.stopPropagation()
  
  const menuWidth = 175
  const menuHeight = 210
  let x = e.clientX
  let y = e.clientY
  
  if (typeof window !== 'undefined') {
    if (x + menuWidth > window.innerWidth) {
      x = Math.max(8, window.innerWidth - menuWidth - 8)
    }
    if (y + menuHeight > window.innerHeight) {
      y = Math.max(8, window.innerHeight - menuHeight - 8)
    }
  }
  
  contextMenu.value = {
    visible: true,
    x,
    y,
    item
  }
}

function closeContextMenu() {
  contextMenu.value.visible = false
}

function handleContextPin() {
  if (contextMenu.value.item) {
    togglePin(contextMenu.value.item.id)
  }
  closeContextMenu()
}

const renameDialogOpen = ref(false)
const renamingItem = ref<UnifiedItem | null>(null)
const copiedNoticeText = ref('Workflow ID 已复制到剪贴板')

function handleContextRename() {
  const item = contextMenu.value.item
  closeContextMenu()
  if (!item || item.type !== 'feedback') return
  renamingItem.value = item
  renameDialogOpen.value = true
}

async function handleRenameSubmit(newTitle: string) {
  if (!renamingItem.value) return
  try {
    await sessionStore.renameSession(renamingItem.value.id, newTitle)
    toast.success('工作流名称已成功修改')
  } catch (err: any) {
    toast.error(`重命名失败: ${err?.message || '未知错误'}`)
  }
}

async function handleContextArchive() {
  const item = contextMenu.value.item
  closeContextMenu()
  if (!item || item.type !== 'feedback') return
  // 在「已归档」过滤下右键同一个位置是「取消归档」：直接放回日常列表，不需要确认
  if (item.status === 'archived') {
    await sessionStore.unarchiveSession(item.id)
    return
  }
  if (confirm(`确认归档 "${item.title}"？`)) {
    await sessionStore.archiveSession(item.id)
  }
}

async function handleContextCopyId() {
  const item = contextMenu.value.item
  closeContextMenu()
  if (!item) return
  const hasWf = Boolean(item.workflow_id)
  const idToCopy = item.workflow_id || item.id
  copiedNoticeText.value = hasWf ? 'Workflow ID 已复制到剪贴板' : '会话 ID 已复制到剪贴板'
  try {
    await navigator.clipboard.writeText(idToCopy)
    copiedNotice.value = true
    setTimeout(() => { copiedNotice.value = false }, 2000)
  } catch (err) {
    console.error('Copy failed', err)
  }
}

async function handleContextCopyDirectory() {
  const item = contextMenu.value.item
  closeContextMenu()
  if (!item) return
  const dir = formatItemProjectDirectory(item)
  try {
    await navigator.clipboard.writeText(dir)
    copiedNotice.value = true
    setTimeout(() => { copiedNotice.value = false }, 2000)
  } catch (err) {
    console.error('Copy failed', err)
  }
}

const allItems = computed<UnifiedItem[]>(() => {
  const items: UnifiedItem[] = []

  // 1. Feedback sessions: 按 workflow_id (若无则以 session_id) 聚合展示，实现一个 Workflow 对应侧边栏一个统一条目
  const feedbackGroups = new Map<string, FeedbackSession[]>()
  
  // 合并全量 sessions 与当前 pending 的 currentSession，确保 pending 活跃轮次即使在未全量刷新时也与对应 Workflow 完美融为一体
  const allFeedbackList: FeedbackSession[] = [...sessionStore.sessions]
  if (sessionStore.currentSession) {
    const idx = allFeedbackList.findIndex(s => s.session_id === sessionStore.currentSession!.session_id)
    if (idx >= 0) {
      allFeedbackList[idx] = sessionStore.currentSession
    } else {
      allFeedbackList.unshift(sessionStore.currentSession)
    }
  }

  // 已归档的会话不混在日常列表里，只在过滤芯片「已归档」下出现（#156 批 1；整页的归档库与底部归档图标仍保留）
  const archivedGroups = new Map<string, FeedbackSession[]>()
  for (const s of allFeedbackList) {
    const bucket = s.status === 'archived' ? archivedGroups : feedbackGroups
    const key = s.workflow_id || s.session_id
    if (!bucket.has(key)) {
      bucket.set(key, [])
    }
    bucket.get(key)!.push(s)
  }

  for (const [key, group] of [...feedbackGroups.entries(), ...archivedGroups.entries()]) {
    // 找出该工作流下创建时间最新的 session 作为展示基准
    const sorted = [...group].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
    const latest = sorted[0]
    const earliest = sorted[sorted.length - 1]
    // 在已按时间倒序排列的 sorted 数组上检索最新 pending 轮次，避免未排序数组检索隐患 (D-142)
    const pendingSession = sorted.find(s => s.status === 'pending')

    // 如果有 pending 轮次，状态直接更新为 pending，且倒计时/在线状态与操作优先绑定 pendingSession
    const activeSess = pendingSession || latest

    items.push({
      id: key,
      type: 'feedback',
      title: latest.title || (latest.workflow_id ? `工作流: ${latest.workflow_id}` : '方案反馈会话'),
      subtitle: latest.workflow_id || latest.project_directory,
      summary: latest.summary || '',
      status: pendingSession ? 'pending' : latest.status,
      created_at: latest.created_at,
      first_created_at: earliest?.created_at || latest.created_at,
      updated_at: latest.updated_at,
      project_directory: latest.project_directory,
      workflow_id: latest.workflow_id,
      // 轮数取整个工作流的总数（服务端聚合）：列表只带最新一批会话行，老工作流在列表里往往只有一行（#173）
      rounds_count: displayRounds(sessionStore.workflowRounds[key], group.length),
      latest_git_commit: sorted.find(s => s.git_commit)?.git_commit || undefined,
      raw: activeSess
    })
  }

  // 2. Tasks
  for (const t of taskStore.tasks) {
    const taskCreatedAt = (t as any).created_at || t.updated_at || new Date().toISOString()
    items.push({
      id: t.task_id,
      type: 'task',
      title: t.title || t.task_id,
      subtitle: `Rev ${t.revision} · Seq ${t.report_sequence}`,
      summary: t.title ? `托管自驾工单: ${t.title}` : `托管自驾工单 (Rev ${t.revision})`,
      status: t.state,
      created_at: taskCreatedAt,
      first_created_at: taskCreatedAt,
      updated_at: t.updated_at,
      unread_count: t.unread_report_count,
      revision: t.revision,
      sequence: t.report_sequence,
      raw: t
    })
  }

  // 排序规则 (D-141)：
  // 1. 置顶 (Pinned) 绝对优先
  // 2. 当日会话位置稳定法则：在当天，出现新消息不从下向上跳转顺序，严格以初始创建时间 (first_created_at) 锁定相对排位
  return items.sort((a, b) => {
    const aPin = isPinned(a.id) ? 1 : 0
    const bPin = isPinned(b.id) ? 1 : 0
    if (aPin !== bPin) return bPin - aPin

    const aFirst = new Date(a.first_created_at || a.created_at).getTime()
    const bFirst = new Date(b.first_created_at || b.created_at).getTime()

    const aLatest = new Date(a.created_at).getTime()
    const bLatest = new Date(b.created_at).getTime()

    // 计算条目的活跃归属日 (以最新轮次为活跃归属日)
    const aDay = dayjs(a.created_at).startOf('day').valueOf()
    const bDay = dayjs(b.created_at).startOf('day').valueOf()

    // 跨自然日：较新一天的条目整体排在上方
    if (aDay !== bDay) {
      return bDay - aDay
    }

    // 同一自然日（当日内）：严格按照初次创建时间倒序排定物理排位，新消息/pending 严禁改变相对顺序（不从下向上跳转）
    if (aFirst !== bFirst) {
      return bFirst - aFirst
    }

    return bLatest - aLatest
  })
})

// 新建会话弹窗关闭后进入新会话：走与点侧栏卡片同一条路径（FeedbackView 的 handleSelectItem 负责回到会话页、选中、滚到底部、
// 手机上收起抽屉）。占位会话在弹窗建好时就已补丁进列表，这里等一帧让侧栏条目算出来再找它。
async function handleNewSessionOpened(session: FeedbackSession) {
  await nextTick()
  const key = session.workflow_id || session.session_id
  const item = allItems.value.find(i => i.type === 'feedback' && i.id === key)
  if (item) emit('selectItem', item)
}

const filteredItems = computed(() => {
  return allItems.value.filter(item => {
    // 已归档只在「已归档」芯片下出现；别的过滤一律不含它们（与原来「归档的不进列表」一致）
    const archived = item.type === 'feedback' && item.status === 'archived'
    if (filterType.value === 'archived') {
      if (!archived) return false
    } else if (archived) {
      return false
    }
    // Type/Status filter
    if (filterType.value === 'pending') {
      if (item.type === 'feedback' && item.status !== 'pending') return false
      if (item.type === 'task' && (item.unread_count || 0) === 0) return false
    } else if (filterType.value === 'feedback') {
      if (item.type !== 'feedback') return false
    } else if (filterType.value === 'task') {
      if (item.type !== 'task') return false
    } else if (filterType.value === 'completed') {
      if (item.type === 'feedback' && item.status !== 'completed' && item.status !== 'cancelled' && item.status !== 'timeout') return false
      if (item.type === 'task' && item.status !== 'completed') return false
    }

    // Search text filter
    if (searchQuery.value.trim()) {
      const q = searchQuery.value.toLowerCase()
      const title = item.title.toLowerCase()
      const summary = item.summary.toLowerCase()
      const dir = (item.project_directory || '').toLowerCase()
      const id = item.id.toLowerCase()
      const wf = (item.workflow_id || '').toLowerCase()
      return title.includes(q) || summary.includes(q) || dir.includes(q) || id.includes(q) || wf.includes(q)
    }

    return true
  })
})

// （条目的状态徽标与「我的状态」提示点由 SidebarItemCard 渲染，这里不再保留一份重复实现）

// 分支会话（#236）：活动分支缩进收在主会话下面（只有一层）。只在「分支和它的主会话都在过滤后的列表里」时挂靠；
// 主会话被过滤掉的分支按它自己的位置显示（仍带分支标记）；没有任何分支关系时列表一字不变。
const branchStore = useBranchStore()
const branchTree = computed(() => arrangeBranchTree(filteredItems.value, branchStore.index, branchStore.collapsed))
const listItems = computed(() => branchTree.value.items)
function isBranchItem(item: UnifiedItem): boolean {
  return item.type === 'feedback' && !!item.workflow_id && branchStore.index.byBranch.has(item.workflow_id)
}

// 动态增量/分批加载 (优化大量会话/工单下的渲染性能)
const visibleCount = ref(25)
const displayedItems = computed(() => {
  return listItems.value.slice(0, visibleCount.value)
})

watch(() => [searchQuery.value, filterType.value], () => {
  visibleCount.value = 25
})

// 侧栏要显示各会话的未读数（#120 阶段 E-c2）：关注当前列表里能看到的工作流，由 conversation_updated 事件刷新。
const chatStore = useChatStore()
watch(
  () => displayedItems.value.filter(i => i.type === 'feedback' && i.workflow_id).map(i => i.workflow_id as string).join('\n'),
  ids => chatStore.watchSummaries(ids ? ids.split('\n') : []),
  { immediate: true },
)

function handleListScroll(e: Event) {
  const el = e.target as HTMLElement
  if (!el) return
  if (el.scrollHeight - el.scrollTop - el.clientHeight < 80) {
    if (visibleCount.value < listItems.value.length) {
      visibleCount.value = Math.min(visibleCount.value + 20, listItems.value.length)
    }
  }
}

// 侧栏卡上的计时、状态都由 SidebarItemCard / SidebarItemTimer 自己订阅共享秒级时钟（#215），这里不再保留
// 一个每秒走一下、再当 prop 灌给每张卡的 now（那会让每张卡每秒都整张重画），也不再留已无人调用的旧计时 / 时间格式化函数。

function formatItemProjectDirectory(item: UnifiedItem): string {
  const dir = item.project_directory || item.subtitle || item.workflow_id || item.id
  const host = (item.raw as FeedbackSession)?.host_name || settingsStore.settings.hostName
  if (host && host.trim() && item.project_directory) {
    return `${host.trim()}:${item.project_directory}`
  }
  return dir
}

function isItemActive(item: UnifiedItem): boolean {
  if (props.workActive || props.groupActive) return false
  if (props.activeItemId && props.activeItemType) {
    if (props.activeItemType !== item.type) return false
    if (props.activeItemId === item.id) return true
    if (item.workflow_id && props.activeItemId === item.workflow_id) return true
    if (item.raw && (item.raw as FeedbackSession).session_id === props.activeItemId) return true
  }
  if (item.type === 'feedback') {
    // 侧边栏高亮焦点严格以 selectedSession（用户正在查看的条目）为真源，仅在未选定时回退
    const sel = sessionStore.selectedSession || sessionStore.currentSession
    if (!sel) return false
    const selKey = sel.workflow_id || sel.session_id
    return selKey === item.id || (item.workflow_id && sel.workflow_id === item.workflow_id) || sel.session_id === item.id
  }
  if (item.type === 'task') {
    return taskStore.currentTask?.task_id === item.id
  }
  return false
}

onMounted(async () => {
  // 会话卡的会话级状态（#159）要用 Agent 状态（hook）：页头 / 工具栏没挂载的页面里也得有人去取一次；已取到过不会重复请求
  agentStateStore.ensureFresh()
  // 分支关系（#236）：侧栏的分支树要用；整表一次取回，同时只有一个在途请求，之后靠实时流校准。取不到就当没有分支（列表照旧）
  void branchStore.ensureLoaded()
  await Promise.all([
    // fresh:false：与布局 / 会话页同一时刻的挂载取数合并成一次请求（#177）
    sessionStore.fetchSessions(undefined, { fresh: false }),
    sessionStore.fetchCurrentSession({ fresh: false }),
    taskStore.fetchTasks(),
    workStore.refreshCounts()
  ])
  window.addEventListener('click', closeContextMenu)
})

onUnmounted(() => {
  window.removeEventListener('click', closeContextMenu)
})
</script>

<template>
  <!-- Mobile Overlay Backdrop -->
  <Transition
    enter-active-class="transition-opacity ease-linear duration-200"
    enter-from-class="opacity-0"
    enter-to-class="opacity-100"
    leave-active-class="transition-opacity ease-linear duration-200"
    leave-from-class="opacity-100"
    leave-to-class="opacity-0"
  >
    <div
      v-if="props.mobileOpen"
      class="fixed inset-0 bg-black/60 backdrop-blur-xs z-40 md:hidden"
      @click="emit('closeMobile')"
    ></div>
  </Transition>

  <aside
    class="sidebar-container fixed inset-y-0 left-0 z-50 h-full h-[100dvh] flex flex-col bg-card/95 backdrop-blur-md md:static md:z-10 md:bg-muted/15 md:h-full shrink-0 select-none overflow-hidden border-r border-border/70"
    :class="[
      // 抽屉宽度：手机上占屏幕的 85%、最宽 18rem；md 以上由 globals.css 的 .sidebar-container 固定宽度接管（max-w-none 放开上限）。
      'w-[85vw] max-w-72 md:max-w-none',
      props.mobileOpen ? 'translate-x-0 shadow-float' : '-translate-x-full md:translate-x-0',
      props.collapsed ? 'is-collapsed' : ''
    ]"
  >
    <!-- 内层始终撑满侧栏自己的宽度：侧栏宽度恒定（折叠只动 margin-left），折叠 / 展开动画里内容不会重排；也避免了「内层比侧栏窄」造成的右侧空白。 -->
    <div class="w-full h-full flex flex-col shrink-0">
      <!-- 1. Top Brand & App Title (高度 h-14 与主工作区顶栏严格一致) -->
      <div class="h-14 px-3.5 border-b border-border/60 flex items-center justify-between shrink-0">
        <!-- 品牌区同时是项目切换器（RelayMesh | ExecMesh）：仅终端总闸开启时可切换，总闸关闭时与以前完全一致 -->
        <ProjectSwitcher @switched="emit('closeMobile')">
          <template #extra>
            <!-- SSE 实时流服务连接状态指示图标 (纯净指示服务本身健康度) -->
            <span
              class="inline-flex items-center justify-center cursor-help shrink-0"
              :title="`SSE 实时流服务: ${sessionStore.sseConnected ? '已连接 (LIVE)' : '未连接 (OFFLINE)'}`"
            >
              <Radio
                class="w-3 h-3 transition-colors duration-200"
                :class="[
                  sessionStore.sseConnected ? 'text-emerald-500 animate-pulse' : 'text-muted-foreground/40'
                ]"
              />
            </span>
          </template>
        </ProjectSwitcher>

        <div class="flex items-center gap-1 shrink-0">
          <!-- PC Collapsible Toggle Button (PC 端侧边栏折叠缩进按钮) -->
          <button
            type="button"
            class="p-1 rounded-xs text-muted-foreground hover:text-foreground hover:bg-muted/80 transition-colors hidden md:flex items-center justify-center cursor-pointer"
            title="折叠侧边栏"
            data-testid="sidebar-collapse"
            @click="emit('toggleCollapse')"
          >
            <PanelLeftClose class="w-4 h-4" />
          </button>

          <!-- Mobile Close Button -->
          <button
            type="button"
            class="p-1 rounded-sm text-muted-foreground hover:text-foreground md:hidden"
            @click="emit('closeMobile')"
          >
            <X class="w-4 h-4" />
          </button>
        </div>
      </div>

      <!-- 任务清单入口：独立于会话的工作清单；常驻一句进度概览（进行中 / 验证中） -->
      <!-- 导航：会话 / 任务清单 / 群聊 / 项目 四项并列，当前所在的一项高亮；手机上点哪一项都先收起抽屉（#156 批 1） -->
      <SidebarSessions @navigate="emit('closeMobile')" />

      <div class="px-2.5 pt-1.5">
        <button
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm border text-xs transition-colors cursor-pointer"
          :aria-current="props.workActive ? 'page' : undefined"
          :class="props.workActive
            ? 'bg-primary/10 border-primary/30 text-foreground'
            : 'border-border/60 bg-background/60 text-muted-foreground hover:text-foreground hover:bg-muted/60'"
          title="任务清单：独立于会话的工作清单"
          data-testid="sidebar-work-entry"
          @click="handleOpenWork"
        >
          <ListChecks class="w-3.5 h-3.5 shrink-0" />
          <span class="font-medium">任务清单</span>
          <span v-if="workSummary" class="ml-auto font-mono text-[10px] text-muted-foreground" data-testid="sidebar-work-summary">{{ workSummary }}</span>
        </button>
      </div>

      <!-- 群聊入口：总入口 + 最近有动静的几个群 -->
      <SidebarGroups @navigate="emit('closeMobile')" />
      <SidebarProjects @navigate="emit('closeMobile')" />

      <!-- 2. Search & Category Filter Bar (替代独立分页，融于统一列表) -->
      <div class="p-2.5 border-b border-border/60 space-y-2">
        <!-- Search Input -->
        <div class="relative">
          <Search class="w-3.5 h-3.5 absolute left-2.5 top-2 text-muted-foreground" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="搜索 Workflow / 工单..."
            class="w-full pl-7 pr-3 py-1 text-xs bg-background border border-border/70 rounded-sm focus:outline-none focus:ring-1 focus:ring-primary/30 placeholder:text-muted-foreground"
          />
        </div>

        <!-- Quick Filter Pills -->
        <div class="flex flex-wrap gap-1">
          <button
            v-for="ft in [
              { id: 'all', label: '全部' },
              { id: 'pending', label: '待处理' },
              { id: 'feedback', label: '工作流' },
              { id: 'task', label: '托管自驾' },
              { id: 'completed', label: '已完成' },
              { id: 'archived', label: '已归档' }
            ]"
            :key="ft.id"
            type="button"
            class="text-[10px] px-2 py-0.5 rounded-xs transition-all font-medium"
            :data-testid="`sidebar-filter-${ft.id}`"
            :class="filterType === ft.id
              ? 'bg-primary text-primary-foreground font-medium shadow-2xs'
              : 'text-muted-foreground hover:bg-muted/70'"
            @click="filterType = ft.id as any"
          >
            {{ ft.label }}
          </button>
        </div>
      </div>

      <!-- 3. Scrollable Unified Sessions & Tasks List (支持动态分批加载，待确认会话在其专属卡片右侧提供精准取消) -->
      <div
        class="flex-1 overflow-y-auto p-2 space-y-1 no-scrollbar scrollbar-none"
        @scroll="handleListScroll"
      >
        <!-- 简略列表第一次取数还没结果：还不知道有没有会话，画几张占位卡而不是「暂无匹配」（#186 Q117 a 的延伸；取数失败也算有结果，回到空状态） -->
        <div
          v-if="filteredItems.length === 0 && !sessionStore.sessionsListSettled"
          data-testid="sidebar-skeleton"
          role="status"
          aria-busy="true"
          aria-label="正在载入会话列表"
          class="space-y-1 animate-pulse motion-reduce:animate-none"
        >
          <div v-for="n in 5" :key="n" class="rounded-md border border-border/40 bg-card/30 p-3 space-y-2">
            <div class="h-3 w-2/3 rounded bg-muted/60"></div>
            <div class="h-2.5 w-11/12 rounded bg-muted/40"></div>
            <div class="h-2.5 w-1/3 rounded bg-muted/40"></div>
          </div>
        </div>
        <div
          v-else-if="filteredItems.length === 0"
          class="p-6 text-center text-xs text-muted-foreground font-mono"
        >
          暂无匹配 Workflow 或工单
        </div>

        <SidebarItemCard
          v-for="item in displayedItems"
          :key="`${item.type}-${item.id}`"
          :item="item"
          :is-active="isItemActive(item)"
          :is-pinned="isPinned(item.id)"
          :nested="branchTree.nested.has(item.id)"
          :is-branch="isBranchItem(item)"
          :branch-count="branchTree.childCount.get(item.id)"
          :branch-collapsed="!!item.workflow_id && branchStore.collapsed.has(item.workflow_id)"
          @select="emit('selectItem', item)"
          @contextmenu="openContextMenu"
          @cancel="id => sessionStore.cancelSession(id)"
          @toggle-branches="branchStore.toggleCollapsed"
        />

        <!-- 动态加载指示 (当列表较长时展示) -->
        <div
          v-if="listItems.length > 25"
          class="py-2.5 text-center text-[10px] font-mono text-muted-foreground"
        >
          <span v-if="visibleCount < listItems.length">
            正在动态加载更多 ({{ displayedItems.length }}/{{ listItems.length }})...
          </span>
          <span v-else class="opacity-60">
            已加载全部 {{ listItems.length }} 条记录
          </span>
        </div>
      </div>

    <!-- Custom Context Menu for Session Items (Teleported to body to guarantee max z-index and zero clipping) -->
    <Teleport to="body">
      <div
        v-if="contextMenu.visible && contextMenu.item"
        class="fixed z-[9999] min-w-[165px] bg-popover/95 backdrop-blur-md border border-border shadow-float rounded-md p-1 text-xs text-foreground animate-in fade-in-80"
        :style="{ top: `${contextMenu.y}px`, left: `${contextMenu.x}px` }"
        @click.stop
      >
        <!-- 1. 置顶 / 取消置顶 -->
        <button
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm hover:bg-muted text-left transition-colors cursor-pointer"
          @click="handleContextPin"
        >
          <Pin v-if="!isPinned(contextMenu.item.id)" class="w-3.5 h-3.5 text-muted-foreground" />
          <PinOff v-else class="w-3.5 h-3.5 text-muted-foreground" />
          <span>{{ isPinned(contextMenu.item.id) ? '取消置顶' : '置顶' }}</span>
        </button>

        <!-- 2. 重命名 (仅 feedback) -->
        <button
          v-if="contextMenu.item.type === 'feedback'"
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm hover:bg-muted text-left transition-colors cursor-pointer"
          @click="handleContextRename"
        >
          <Edit2 class="w-3.5 h-3.5 text-muted-foreground" />
          <span>重命名</span>
        </button>

        <!-- 3. 归档 (仅 feedback) -->
        <button
          v-if="contextMenu.item.type === 'feedback'"
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm hover:bg-muted text-left transition-colors cursor-pointer"
          @click="handleContextArchive"
        >
          <Archive class="w-3.5 h-3.5 text-muted-foreground" />
          <span>{{ contextMenu.item.status === 'archived' ? '取消归档' : '归档' }}</span>
        </button>

        <div class="h-[1px] bg-border/60 my-1"></div>

        <!-- 4. 复制 ID（有 workflow_id 则为 Workflow ID，否则为会话 ID） -->
        <button
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm hover:bg-muted text-left transition-colors cursor-pointer"
          @click="handleContextCopyId"
        >
          <Workflow v-if="contextMenu.item.workflow_id" class="w-3.5 h-3.5 text-muted-foreground" />
          <Hash v-else class="w-3.5 h-3.5 text-muted-foreground" />
          <span>{{ contextMenu.item.workflow_id ? '复制 Workflow ID' : '复制会话 ID' }}</span>
        </button>

        <!-- 5. 复制工作区路径 (若存在) -->
        <button
          v-if="contextMenu.item.project_directory"
          type="button"
          class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-sm hover:bg-muted text-left transition-colors cursor-pointer"
          @click="handleContextCopyDirectory"
        >
          <FolderGit2 class="w-3.5 h-3.5 text-muted-foreground" />
          <span>复制工作区路径</span>
        </button>
      </div>
    </Teleport>

    <!-- Copied Toast indicator -->
    <Teleport to="body">
      <div
        v-if="copiedNotice"
        class="fixed bottom-4 left-4 z-[9999] bg-foreground text-background text-xs px-3 py-1.5 rounded-sm shadow-float flex items-center gap-1.5 pointer-events-none"
      >
        <Check class="w-3.5 h-3.5" />
        <span>{{ copiedNoticeText }}</span>
      </div>
    </Teleport>

    <!-- 新建会话弹窗（#174） -->
    <NewSessionDialog v-model:open="newSessionOpen" @opened="handleNewSessionOpened" />

    <!-- Controlled Rename Dialog -->
    <RenameSessionDialog
      v-model:open="renameDialogOpen"
      :current-title="renamingItem?.title || ''"
      :item-id="renamingItem?.id || ''"
      :is-workflow="Boolean(renamingItem?.workflow_id)"
      @submit="handleRenameSubmit"
    />

      <!-- 5. Bottom Utility Bar: Settings on left, Utilities + Archive on right -->
      <div class="p-2 border-t border-border/60 flex items-center justify-between bg-card/50">
        <!-- Settings Button (调到最左边) -->
        <Button
          variant="ghost"
          size="sm"
          class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
          title="设置"
          data-prefetch="settings"
          @click="handleOpenSettings"
        >
          <Settings class="w-3.5 h-3.5" />
        </Button>

        <!-- Right Utility Actions (按用户指定顺序：①通知 -> ②声音 -> ③明暗 -> ④归档；#174 在最左边加「新建会话」) -->
        <div class="flex items-center gap-1">
          <!-- 0. 新建会话（#174）：网页里先建好会话，再把启动提示词发给要使用的智能体 -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
            data-testid="sidebar-new-session"
            title="新建会话：先在网页里建好会话，再启用对应的智能体"
            aria-label="新建会话"
            @click="newSessionOpen = true"
          >
            <MessageSquarePlus class="w-3.5 h-3.5" />
          </Button>

          <!-- 1. Desktop Notification Toggle (通知) -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
            :title="notifyStore.desktopEnabled ? '系统桌面通知已开启' : '开启系统桌面通知'"
            @click="notifyStore.toggleDesktop"
          >
            <Bell v-if="notifyStore.desktopEnabled" class="w-3.5 h-3.5 text-foreground" />
            <BellOff v-else class="w-3.5 h-3.5" />
          </Button>

          <!-- 2. Sound Toggle (声音) -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
            :title="notifyStore.soundEnabled ? '声音提醒已开启' : '声音提醒已静音'"
            @click="notifyStore.toggleSound"
          >
            <Volume2 v-if="notifyStore.soundEnabled" class="w-3.5 h-3.5 text-foreground" />
            <VolumeX v-else class="w-3.5 h-3.5" />
          </Button>

          <!-- 2.5 免打扰（#115）：一键拦下非紧急的提醒（浏览器与 ntfy 一起）；阻塞待答、@ 你、终端审批与系统告警不受影响。开关在服务端，各设备共享 -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground"
            data-testid="sidebar-dnd"
            :aria-pressed="notifyPrefs.dnd"
            :title="notifyPrefs.dnd
              ? '免打扰已开启：除阻塞待答、@ 你、终端审批与系统告警外，浏览器与手机推送都不再提醒（点击关闭）'
              : '开启免打扰：拦下非紧急的提醒，阻塞待答与 @ 你仍会通知'"
            @click="notifyPrefs.toggleDnd"
          >
            <BellMinus class="w-3.5 h-3.5" :class="notifyPrefs.dnd ? 'text-amber-500' : ''" />
          </Button>

          <!-- 3. Theme 3-State Toggle (跟随系统 auto · 浅色 light · 深色 dark) -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground cursor-pointer transition-colors"
            @click="themeStore.toggleTheme"
            :title="
              themeStore.mode === 'auto'
                ? `当前: 跟随系统 (${themeStore.isDark ? '深色' : '浅色'}) · 点击切换浅色`
                : themeStore.mode === 'light'
                  ? '当前: 浅色模式 · 点击切换深色'
                  : '当前: 深色模式 · 点击切换跟随系统'
            "
          >
            <!-- 跟随系统 auto: 明+暗+Auto 三元素融合图标 -->
            <ThemeAutoIcon v-if="themeStore.mode === 'auto'" size="w-3.5 h-3.5" />
            <!-- 浅色模式 light: 太阳图标 -->
            <Sun v-else-if="themeStore.mode === 'light'" class="w-3.5 h-3.5 text-amber-500 hover:text-amber-400" />
            <!-- 深色模式 dark: 月亮图标 -->
            <Moon v-else class="w-3.5 h-3.5 text-indigo-400 hover:text-indigo-300" />
          </Button>

          <!-- 4. Archive Dialog Trigger Button (归档) -->
          <Button
            variant="ghost"
            size="sm"
            class="w-7 h-7 p-0 text-muted-foreground hover:text-foreground cursor-pointer"
            title="已归档会话库"
            @click="handleOpenArchive"
          >
            <Archive class="w-3.5 h-3.5" />
          </Button>
        </div>
      </div>
    </div>
  </aside>
</template>
