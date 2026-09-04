<script setup lang="ts">
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { useSessionStore } from '../stores/session'
import { useTaskStore } from '../stores/task'
import { useNotifyStore } from '../stores/notify'
import { useThemeStore } from '../stores/theme'
import { useSettingsStore } from '../stores/settings'
import Button from './ui/button/Button.vue'
import Badge from './ui/badge/Badge.vue'
import ThemeAutoIcon from './ThemeAutoIcon.vue'
import RelayMeshLogo from './icons/RelayMeshLogo.vue'
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
  Hash
} from 'lucide-vue-next'
import type { FeedbackSession, TaskSummary } from '../api/types'
import SidebarItemCard from './sidebar/SidebarItemCard.vue'
import RenameSessionDialog from './sidebar/RenameSessionDialog.vue'
import { toast } from 'vue-sonner'

export interface UnifiedItem {
  id: string
  type: 'feedback' | 'task'
  title: string
  subtitle?: string
  summary: string
  status: string
  user_presence?: 'online' | 'away' | 'autopilot'
  created_at: string
  first_created_at?: string
  updated_at?: string
  project_directory?: string
  workflow_id?: string
  unread_count?: number
  revision?: number
  sequence?: number
  rounds_count?: number
  raw: FeedbackSession | TaskSummary
}

const sessionStore = useSessionStore()
const taskStore = useTaskStore()
const notifyStore = useNotifyStore()
const themeStore = useThemeStore()
const settingsStore = useSettingsStore()
const router = useRouter()

function handleOpenSettings() {
  if (window.innerWidth < 768) {
    emit('closeMobile')
    router.push('/settings')
  } else {
    settingsStore.openSettings()
  }
}

function handleOpenArchive() {
  if (window.innerWidth < 768) {
    emit('closeMobile')
    router.push('/archive')
  } else {
    emit('openArchive')
  }
}

const props = defineProps<{
  activeItemId?: string | null
  activeItemType?: 'feedback' | 'task' | null
  elapsedFormatted?: string
  mobileOpen?: boolean
  collapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'selectItem', item: UnifiedItem): void
  (e: 'openArchive'): void
  (e: 'closeMobile'): void
  (e: 'toggleCollapse'): void
}>()

const searchQuery = ref('')
const filterType = ref<'all' | 'pending' | 'feedback' | 'task' | 'completed'>('all')

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

  for (const s of allFeedbackList) {
    if (s.status === 'archived') continue
    const key = s.workflow_id || s.session_id
    if (!feedbackGroups.has(key)) {
      feedbackGroups.set(key, [])
    }
    feedbackGroups.get(key)!.push(s)
  }

  for (const [key, group] of feedbackGroups.entries()) {
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
      user_presence: activeSess.user_presence,
      created_at: latest.created_at,
      first_created_at: earliest?.created_at || latest.created_at,
      updated_at: latest.updated_at,
      project_directory: latest.project_directory,
      workflow_id: latest.workflow_id,
      rounds_count: group.length,
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

const filteredItems = computed(() => {
  return allItems.value.filter(item => {
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

function getStatusBadge(item: UnifiedItem) {
  if (item.type === 'task') {
    if (item.status === 'active') {
      if ((item.unread_count || 0) > 0) {
        return { label: `${item.unread_count} 新报告`, variant: 'default' }
      }
      return { label: '执行中', variant: 'secondary' }
    }
    return { label: item.status === 'completed' ? '已完工' : item.status, variant: 'outline' }
  }

  switch (item.status) {
    case 'completed': return { label: '已完成', variant: 'default' }
    case 'pending': return { label: '等待确认', variant: 'secondary' }
    case 'cancelled': return { label: '已取消', variant: 'outline' }
    case 'timeout': return { label: '已超时', variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: item.status, variant: 'outline' }
  }
}

function getItemPresence(item: UnifiedItem): { label: string; dotClass: string } {
  const rawSession = sessionStore.sessions.find(s => s.session_id === item.id)
  const p = item.user_presence || rawSession?.user_presence || 'online'
  if (p === 'away') {
    return { label: '暂离', dotClass: 'bg-amber-500 shadow-[0_0_4px_rgba(245,158,11,0.6)]' }
  }
  if (p === 'autopilot') {
    return { label: '托管', dotClass: 'bg-indigo-500 shadow-[0_0_4px_rgba(99,102,241,0.6)]' }
  }
  return { label: '在线', dotClass: 'bg-emerald-500 shadow-[0_0_4px_rgba(16,185,129,0.6)]' }
}

// 动态增量/分批加载 (优化大量会话/工单下的渲染性能)
const visibleCount = ref(25)
const displayedItems = computed(() => {
  return filteredItems.value.slice(0, visibleCount.value)
})

watch(() => [searchQuery.value, filterType.value], () => {
  visibleCount.value = 25
})

function handleListScroll(e: Event) {
  const el = e.target as HTMLElement
  if (!el) return
  if (el.scrollHeight - el.scrollTop - el.clientHeight < 80) {
    if (visibleCount.value < filteredItems.value.length) {
      visibleCount.value = Math.min(visibleCount.value + 20, filteredItems.value.length)
    }
  }
}

const nowRef = ref(Date.now())
let timer: number | null = null

function formatTimerDuration(seconds: number): string {
  if (seconds < 0) seconds = 0
  const ONE_DAY = 86400
  const ONE_HOUR = 3600

  if (seconds >= ONE_DAY) {
    const days = Math.floor(seconds / ONE_DAY)
    const remainSec = seconds % ONE_DAY
    const hours = Math.floor(remainSec / ONE_HOUR)
    const mins = Math.floor((remainSec % ONE_HOUR) / 60)
    return `${days}d·${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}`
  }

  if (seconds >= ONE_HOUR) {
    const hours = Math.floor(seconds / ONE_HOUR)
    const mins = Math.floor((seconds % ONE_HOUR) / 60)
    const secs = seconds % 60
    return `${String(hours).padStart(2, '0')}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }

  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

function formatElapsed(seconds: number): string {
  return formatTimerDuration(seconds)
}

function formatItemTime(dateStr?: string): string {
  if (!dateStr) return ''
  const target = dayjs(dateStr)
  if (!target.isValid()) return ''
  const now = dayjs()

  const isSameYear = target.isSame(now, 'year')
  const isSameDay = isSameYear && target.isSame(now, 'day')

  if (isSameDay) {
    // 当日：直接显示时间 HH:mm
    return target.format('HH:mm')
  } else if (isSameYear) {
    // 跨日（同年）：显示月-日·时:分 MM-DD·HH:mm
    return target.format('MM-DD·HH:mm')
  } else {
    // 跨年：显示年·月-日·时:分 YYYY·MM-DD·HH:mm
    return target.format('YYYY·MM-DD·HH:mm')
  }
}

function getItemTimerInfo(item: UnifiedItem): { text: string; isCountdown: boolean } {
  if (item.type !== 'feedback' || item.status !== 'pending') {
    return { text: '', isCountdown: false }
  }
  const start = new Date(item.created_at).getTime()
  if (isNaN(start)) return { text: '00:00', isCountdown: false }
  const elapsed = Math.max(0, Math.floor((nowRef.value - start) / 1000))

  const rawSession = sessionStore.sessions.find(s => s.session_id === item.id)
  const targetMinutes = rawSession?.wait_countdown_minutes ?? settingsStore.settings.defaultWaitCountdownMinutes ?? 2
  const targetSec = targetMinutes * 60

  if (targetSec > 0 && elapsed < targetSec) {
    const remain = targetSec - elapsed
    return {
      text: formatTimerDuration(remain),
      isCountdown: true
    }
  }

  return {
    text: formatTimerDuration(elapsed),
    isCountdown: false
  }
}

function formatItemProjectDirectory(item: UnifiedItem): string {
  const dir = item.project_directory || item.subtitle || item.workflow_id || item.id
  const host = (item.raw as FeedbackSession)?.host_name || settingsStore.settings.hostName
  if (host && host.trim() && item.project_directory) {
    return `${host.trim()}:${item.project_directory}`
  }
  return dir
}

function isItemActive(item: UnifiedItem): boolean {
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
  await Promise.all([
    sessionStore.fetchSessions(),
    sessionStore.fetchCurrentSession(),
    taskStore.fetchTasks()
  ])
  timer = window.setInterval(() => {
    nowRef.value = Date.now()
  }, 1000)
  window.addEventListener('click', closeContextMenu)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
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
      props.mobileOpen ? 'w-4/5 sm:w-72 translate-x-0 shadow-float' : 'w-4/5 sm:w-72 -translate-x-full md:translate-x-0',
      props.collapsed ? 'is-collapsed' : ''
    ]"
  >
    <!-- Fixed-width inner container ensuring zero layout reflow or wrapping during smooth collapse/expand -->
    <div class="w-72 sm:w-72 md:w-64 lg:w-80 h-full flex flex-col shrink-0">
      <!-- 1. Top Brand & App Title (高度 h-14 与主工作区顶栏严格一致) -->
      <div class="h-14 px-3.5 border-b border-border/60 flex items-center justify-between shrink-0">
        <div class="flex items-center gap-2 min-w-0">
          <div class="w-6 h-6 rounded-xs bg-primary text-primary-foreground flex items-center justify-center shadow-2xs shrink-0">
            <RelayMeshLogo class="w-3.5 h-3.5" />
          </div>
          <div class="flex flex-col min-w-0">
            <div class="flex items-center gap-1.5 min-w-0">
              <span class="font-bold text-sm tracking-tight text-foreground font-mono leading-none truncate">
                RelayMesh
              </span>
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
            </div>
            <span class="text-[10px] text-muted-foreground font-mono mt-0.5 truncate">
              Agent Relay Hub
            </span>
          </div>
        </div>

        <div class="flex items-center gap-1 shrink-0">
          <!-- PC Collapsible Toggle Button (PC 端侧边栏折叠缩进按钮) -->
          <button
            type="button"
            class="p-1 rounded-xs text-muted-foreground hover:text-foreground hover:bg-muted/80 transition-colors hidden md:flex items-center justify-center cursor-pointer"
            title="折叠侧边栏"
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
              { id: 'completed', label: '已完成' }
            ]"
            :key="ft.id"
            type="button"
            class="text-[10px] px-2 py-0.5 rounded-xs transition-all font-medium"
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
        <div
          v-if="filteredItems.length === 0"
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
          :now="nowRef"
          @select="emit('selectItem', item)"
          @contextmenu="openContextMenu"
          @cancel="id => sessionStore.cancelSession(id)"
        />

        <!-- 动态加载指示 (当列表较长时展示) -->
        <div
          v-if="filteredItems.length > 25"
          class="py-2.5 text-center text-[10px] font-mono text-muted-foreground"
        >
          <span v-if="visibleCount < filteredItems.length">
            正在动态加载更多 ({{ displayedItems.length }}/{{ filteredItems.length }})...
          </span>
          <span v-else class="opacity-60">
            已加载全部 {{ filteredItems.length }} 条记录
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
          <span>归档</span>
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
          title="系统偏好与交互设置"
          @click="handleOpenSettings"
        >
          <Settings class="w-3.5 h-3.5" />
        </Button>

        <!-- Right Utility Actions (按用户指定顺序：①通知 -> ②声音 -> ③明暗 -> ④归档) -->
        <div class="flex items-center gap-1">
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
