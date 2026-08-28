<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useTaskStore } from '../stores/task'
import { useSettingsStore } from '../stores/settings'
import AppSidebar, { type UnifiedItem } from '../components/AppSidebar.vue'
import ArchiveDialog from '../components/ArchiveDialog.vue'
import SessionHeader from '../components/feedback/SessionHeader.vue'
import FeedbackMessageList from '../components/feedback/FeedbackMessageList.vue'
import FeedbackInputDock from '../components/feedback/FeedbackInputDock.vue'
import TaskWorkspace from '../components/feedback/TaskWorkspace.vue'
import type { FeedbackSession, SessionImage } from '../api/types'
import { toast } from 'vue-sonner'

const sessionStore = useSessionStore()
const taskStore = useTaskStore()
const settingsStore = useSettingsStore()
const router = useRouter()

const activeItemType = ref<'feedback' | 'task'>('feedback')
const activeItemId = ref<string | null>(null)
const isArchiveOpen = ref(false)
const isMobileSidebarOpen = ref(false)
const isChatScrolledUp = ref(false)
const hasDraftImages = computed(() => {
  return (inputDockRef.value?.images?.length || 0) > 0
})

const messageListRef = ref<InstanceType<typeof FeedbackMessageList> | null>(null)
const inputDockRef = ref<InstanceType<typeof FeedbackInputDock> | null>(null)

// Elapsed & Countdown Timer management
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

const elapsedSeconds = ref(0)
const mcpActiveElapsedSeconds = ref(0)
const keepaliveElapsedSeconds = ref(0)
const executionElapsedSeconds = ref(0)
let timerId: number | null = null

function updateTimer() {
  const currentSess = sessionStore.currentSession
  const activeSess = currentSess || sessionStore.selectedSession

  if (!activeSess) {
    elapsedSeconds.value = 0
    mcpActiveElapsedSeconds.value = 0
    keepaliveElapsedSeconds.value = 0
    executionElapsedSeconds.value = 0
    return
  }

  const now = Date.now()

  // 1. 如果有活跃 pending 交互会话：
  if (currentSess && currentSess.status === 'pending') {
    const start = currentSess.created_at ? new Date(currentSess.created_at).getTime() : now
    elapsedSeconds.value = Math.max(0, Math.floor((now - start) / 1000))

    if (currentSess.is_mcp_active && currentSess.mcp_active_at) {
      const mcpStart = new Date(currentSess.mcp_active_at).getTime()
      mcpActiveElapsedSeconds.value = Math.max(0, Math.floor((now - mcpStart) / 1000))
    } else {
      mcpActiveElapsedSeconds.value = 0
    }

    if (currentSess.last_keepalive_at) {
      const keepaliveStart = new Date(currentSess.last_keepalive_at).getTime()
      keepaliveElapsedSeconds.value = Math.max(0, Math.floor((now - keepaliveStart) / 1000))
    } else {
      keepaliveElapsedSeconds.value = 0
    }
    executionElapsedSeconds.value = 0
  } else {
    // 2. 无交互/已完成状态 (AI 正在本地执行任务、编码或分析):
    // 计算自上次反馈提交/会话更新时刻以来的执行耗时
    const execStartTime = activeSess.updated_at
      ? new Date(activeSess.updated_at).getTime()
      : (activeSess.created_at ? new Date(activeSess.created_at).getTime() : now)
    executionElapsedSeconds.value = Math.max(0, Math.floor((now - execStartTime) / 1000))
    elapsedSeconds.value = 0
    mcpActiveElapsedSeconds.value = 0
    keepaliveElapsedSeconds.value = 0
  }
}

const formattedElapsed = computed(() => {
  return formatTimerDuration(elapsedSeconds.value)
})

const timerDisplayInfo = computed(() => {
  const currentSess = sessionStore.currentSession
  const activeSess = currentSess || sessionStore.selectedSession

  if (!activeSess) {
    return {
      text: '00:00',
      prefix: '执行',
      isCountdown: false
    }
  }

  // 1. 若当前会话处于 pending 等待用户确认状态：
  if (currentSess && currentSess.status === 'pending') {
    // 1.1 如果 MCP 客户端正在保持活跃长轮询挂起 (is_mcp_active 为 true)：
    if (currentSess.is_mcp_active) {
      const targetMinutes = currentSess.wait_countdown_minutes ?? settingsStore.settings.defaultWaitCountdownMinutes ?? 2
      const targetSec = targetMinutes * 60
      const elapsed = mcpActiveElapsedSeconds.value

      if (targetSec > 0 && elapsed < targetSec) {
        const remain = targetSec - elapsed
        return {
          text: formatTimerDuration(remain),
          prefix: '剩余',
          isCountdown: true
        }
      }
      return {
        text: formatTimerDuration(elapsed),
        prefix: '剩余',
        isCountdown: false
      }
    }

    // 1.2 如果已向 AI 返回「=== 等待回执 ===」，AI 处于 sleep 盲等中：
    const waitDurationSec = currentSess.last_keepalive_at ? keepaliveElapsedSeconds.value : 0
    return {
      text: formatTimerDuration(waitDurationSec),
      prefix: '等待',
      isCountdown: false
    }
  }

  // 2. 无交互状态 (已完成/AI 正在自主执行任务)：显示「执行: MM:SS」
  return {
    text: formatTimerDuration(executionElapsedSeconds.value),
    prefix: '执行',
    isCountdown: false
  }
})

// Chronological feedback sessions list for current workflow (Oldest at top -> Newest at bottom)
const conversationRounds = computed(() => {
  let list = [...sessionStore.sessions]
  const currentSelected = sessionStore.selectedSession || sessionStore.currentSession
  if (currentSelected) {
    if (currentSelected.workflow_id) {
      const wId = currentSelected.workflow_id
      const filtered = list.filter(s => s.workflow_id === wId)
      if (filtered.length > 0) {
        list = filtered
      }
    } else {
      // 若没有 workflow_id，则按 session_id 过滤当前单一会话
      const filtered = list.filter(s => s.session_id === currentSelected.session_id)
      if (filtered.length > 0) {
        list = filtered
      }
    }
  }
  return list.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
})

function handleSelectItem(item: UnifiedItem) {
  activeItemType.value = item.type
  activeItemId.value = item.id

  if (item.type === 'feedback') {
    sessionStore.selectSession(item.raw as FeedbackSession)
    nextTick(() => {
      if (item.status === 'pending') {
        messageListRef.value?.scrollToBottom(false)
      } else {
        messageListRef.value?.scrollToSession(item.id)
      }
    })
  } else if (item.type === 'task') {
    taskStore.fetchTaskDetail(item.id)
  }
}

function handleOpenSettings() {
  if (window.innerWidth < 768) {
    router.push('/settings')
  } else {
    settingsStore.openSettings()
  }
}

function handleOpenArchive() {
  if (window.innerWidth < 768) {
    router.push('/archive')
  } else {
    isArchiveOpen.value = true
  }
}

async function handleSubmit(data: { text: string; presets: string[]; images: SessionImage[] }) {
  const targetSession = sessionStore.currentSession || sessionStore.selectedSession
  const targetWorkflowId = targetSession?.workflow_id || targetSession?.session_id || 'default'

  let finalMsg = data.text.trim()
  if (finalMsg === '' && data.presets.length > 0) {
    finalMsg = data.presets.join('；')
  }
  if (finalMsg === '' && data.images.length === 0) {
    finalMsg = '已确认'
  }

  // 1. 如果当前会话处于 pending 状态，正常提交回复
  if (targetSession && targetSession.status === 'pending') {
    inputDockRef.value?.resetForm(targetWorkflowId)
    await sessionStore.submitFeedback(
      targetSession.session_id,
      finalMsg,
      data.presets,
      data.images
    )
    toast.success('已提交反馈')
  } 
  // 2. 如果当前会话已完成但 AI 尚未提取消费，合并追加回复
  else if (targetSession && targetSession.status === 'completed' && !targetSession.consumed_by_ai) {
    inputDockRef.value?.resetForm(targetWorkflowId)
    await sessionStore.submitFeedback(
      targetSession.session_id,
      finalMsg,
      data.presets,
      data.images
    )
    toast.success('已追加反馈 (已合并至待 AI 接收消息)')
  } 
  // 3. 当前无未消费交互，向工作流暂存追加指令（AI 下次发起交互直接秒回）
  else {
    inputDockRef.value?.resetForm(targetWorkflowId)
    const hostName = targetSession?.host_name || settingsStore.settings.hostName || ''
    const projectDir = targetSession?.project_directory || ''
    await sessionStore.appendWorkflowFeedback(
      targetWorkflowId,
      finalMsg,
      data.presets,
      data.images,
      hostName,
      projectDir
    )
    toast.success('已暂存追加指令，将在 AI 下次发起交互时秒回')
  }

  nextTick(() => messageListRef.value?.scrollToBottom())
}

async function handleRevokeSession(sessionId: string) {
  try {
    const revoked = await sessionStore.revokeFeedback(sessionId)
    if (revoked) {
      inputDockRef.value?.loadSpecificContent({
        text: revoked.response_text || '',
        presets: revoked.user_messages || [],
        images: revoked.images || []
      })
      toast.success('已撤回反馈，内容已还原至输入框')
    }
  } catch (err: any) {
    toast.error('撤回失败: ' + (err.response?.data?.error || err.message || err))
  }
}

async function handleRevokeQueued(queuedId: number) {
  try {
    const targetSession = sessionStore.currentSession || sessionStore.selectedSession
    const targetWorkflowId = targetSession?.workflow_id || ''
    const revoked = await sessionStore.revokeQueuedFeedback(queuedId, targetWorkflowId)
    if (revoked) {
      inputDockRef.value?.loadSpecificContent({
        text: revoked.response_text || '',
        presets: revoked.user_messages || [],
        images: revoked.images || []
      })
      toast.success('已撤回暂存指令，内容已还原至输入框')
    }
  } catch (err: any) {
    toast.error('撤回失败: ' + (err.response?.data?.error || err.message || err))
  }
}

function handleInputDockResize(delta: number) {
  messageListRef.value?.scrollByDelta(delta)
}

const isSidebarCollapsed = ref<boolean>(localStorage.getItem('relaymesh_sidebar_collapsed') === 'true')

function updateSidebarCssVar() {
  if (typeof window === 'undefined') return
  let w = '0px'
  if (!isSidebarCollapsed.value && window.innerWidth >= 768) {
    w = window.innerWidth >= 1024 ? '20rem' : '16rem'
  }
  document.documentElement.style.setProperty('--sidebar-current-width', w)
}

function toggleSidebarCollapse() {
  isSidebarCollapsed.value = !isSidebarCollapsed.value
  try {
    localStorage.setItem('relaymesh_sidebar_collapsed', String(isSidebarCollapsed.value))
  } catch (_) {}
  updateSidebarCssVar()
}

watch(isSidebarCollapsed, () => {
  updateSidebarCssVar()
}, { immediate: true })

watch(() => [sessionStore.currentSession, sessionStore.selectedSession], () => {
  updateTimer()
  const currentWId = sessionStore.selectedSession?.workflow_id || sessionStore.currentSession?.workflow_id
  if (currentWId) {
    sessionStore.fetchQueuedFeedbacks(currentWId)
  }
  if (sessionStore.selectedSession) {
    activeItemType.value = 'feedback'
    activeItemId.value = sessionStore.selectedSession.workflow_id || sessionStore.selectedSession.session_id
  } else if (!activeItemId.value && sessionStore.currentSession) {
    activeItemType.value = 'feedback'
    activeItemId.value = sessionStore.currentSession.workflow_id || sessionStore.currentSession.session_id
  }
}, { immediate: true })

onMounted(async () => {
  await Promise.all([
    sessionStore.fetchCurrentSession(),
    sessionStore.fetchSessions(),
    taskStore.fetchTasks()
  ])
  if (sessionStore.selectedSession) {
    activeItemType.value = 'feedback'
    activeItemId.value = sessionStore.selectedSession.workflow_id || sessionStore.selectedSession.session_id
  } else if (sessionStore.currentSession) {
    activeItemType.value = 'feedback'
    activeItemId.value = sessionStore.currentSession.workflow_id || sessionStore.currentSession.session_id
  } else if (taskStore.tasks.length > 0) {
    activeItemType.value = 'task'
    activeItemId.value = taskStore.tasks[0].task_id
    taskStore.fetchTaskDetail(taskStore.tasks[0].task_id)
  }
  timerId = window.setInterval(updateTimer, 1000)
  updateSidebarCssVar()
  window.addEventListener('resize', updateSidebarCssVar)
  nextTick(() => messageListRef.value?.scrollToBottom(false))
})

onUnmounted(() => {
  if (timerId) clearInterval(timerId)
  window.removeEventListener('resize', updateSidebarCssVar)
})
</script>

<template>
  <div class="h-screen w-screen flex bg-background text-foreground overflow-hidden">
    <!-- 1. Top-to-Bottom Piercing Left Sidebar -->
    <AppSidebar
      :active-item-id="activeItemId"
      :active-item-type="activeItemType"
      :elapsed-formatted="formattedElapsed"
      :mobile-open="isMobileSidebarOpen"
      :collapsed="isSidebarCollapsed"
      @select-item="(item) => {
        handleSelectItem(item)
        isMobileSidebarOpen = false
      }"
      @open-archive="handleOpenArchive"
      @close-mobile="isMobileSidebarOpen = false"
      @toggle-collapse="toggleSidebarCollapse"
    />

    <!-- Archive Modal Dialog -->
    <ArchiveDialog
      v-model:open="isArchiveOpen"
      @select-session="(s) => {
        sessionStore.selectSession(s)
        activeItemType = 'feedback'
        activeItemId = s.workflow_id || s.session_id
      }"
    />

    <!-- 2. Right Main Workspace -->
    <main class="flex-1 min-w-0 h-screen flex flex-col bg-background overflow-hidden relative">
      <!-- ========================================== -->
      <!-- VIEW A: FEEDBACK INTERACTION CONVERSATION  -->
      <!-- ========================================== -->
      <template v-if="activeItemType === 'feedback'">
        <!-- Top Workspace Header -->
        <SessionHeader
          :timer-display-info="timerDisplayInfo"
          :sidebar-collapsed="isSidebarCollapsed"
          @open-mobile-sidebar="isMobileSidebarOpen = true"
          @expand-sidebar="toggleSidebarCollapse"
        />

        <!-- Middle Scrollable Conversation Feed -->
        <FeedbackMessageList
          ref="messageListRef"
          :conversation-rounds="conversationRounds"
          :has-draft-images="hasDraftImages"
          @scroll-state-change="(scrolled) => isChatScrolledUp = scrolled"
          @revoke-session="handleRevokeSession"
          @revoke-queued="handleRevokeQueued"
        />

        <!-- Bottom Feedback Submission Dock (常驻底部面板) -->
        <FeedbackInputDock
          ref="inputDockRef"
          :is-scrolled-up="isChatScrolledUp"
          @submit="handleSubmit"
          @scroll-to-bottom="messageListRef?.scrollToBottom()"
          @open-settings="handleOpenSettings"
          @resize-delta="handleInputDockResize"
        />
      </template>

      <!-- ========================================== -->
      <!-- VIEW B: AUTOMATION TASK ORCHESTRATION      -->
      <!-- ========================================== -->
      <template v-else-if="activeItemType === 'task'">
        <TaskWorkspace
          :active-item-id="activeItemId"
          :sidebar-collapsed="isSidebarCollapsed"
          @open-mobile-sidebar="isMobileSidebarOpen = true"
          @expand-sidebar="toggleSidebarCollapse"
        />
      </template>
    </main>
  </div>
</template>
