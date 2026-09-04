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
import { AlertTriangle, RefreshCw, WifiOff } from 'lucide-vue-next'
import { useSessionTimer } from '../composables/useSessionTimer'

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

// 统一时钟 Composable 驱动（以用户当前选中的 selectedSession 为真源）
const activeSessionForTimer = computed(() => sessionStore.selectedSession || sessionStore.currentSession)
const { timerDisplay: timerDisplayInfo, cleanup: cleanupTimer } = useSessionTimer(
  () => activeSessionForTimer.value as any,
  () => settingsStore.settings.defaultWaitCountdownMinutes ?? 2
)

const formattedElapsed = computed(() => {
  return timerDisplayInfo.value.text
})

// Chronological feedback sessions list for current workflow (Oldest at top -> Newest at bottom)
const conversationRounds = computed(() => {
  // 优先从按需加载并带二级缓存的 currentWorkflowSessions 获取完整正文与图片
  if (sessionStore.currentWorkflowSessions && sessionStore.currentWorkflowSessions.length > 0) {
    return [...sessionStore.currentWorkflowSessions].sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  }

  // 兜底：若尚未加载完成，回退到 sessions（元数据）提供基础结构
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

async function handleSelectItem(item: UnifiedItem) {
  activeItemType.value = item.type
  activeItemId.value = item.id

  if (item.type === 'feedback') {
    const rawSess = item.raw as FeedbackSession
    sessionStore.selectSession(rawSess)
    const targetWId = item.workflow_id || item.id
    if (targetWId) {
      await sessionStore.loadWorkflowSessions(targetWId)
    }
    await nextTick()
    if (item.status === 'pending') {
      // 待回复的 Workflow：跳转到最新待确认会话卡片的开头（头部），便于第一时间阅读 AI 方案
      const pendingSessionId = rawSess?.session_id || item.id
      messageListRef.value?.scrollToSession(pendingSessionId)
    } else {
      // 已经回复的 Workflow：跳转到会话流的最末尾位置（底部）
      messageListRef.value?.scrollToBottom(false)
    }
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
  // 关键防御：严格以用户当前在主视口选中的 selectedSession 为唯一真源，杜绝后台无关 pending 会话劫持！
  const targetSession = sessionStore.selectedSession || sessionStore.currentSession
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
    const targetSession = sessionStore.selectedSession || sessionStore.currentSession
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

watch(() => sessionStore.selectedSession, (sess) => {
  const currentWId = sess?.workflow_id || sess?.session_id
  if (currentWId) {
    sessionStore.fetchQueuedFeedbacks(currentWId)
    activeItemType.value = 'feedback'
    activeItemId.value = currentWId
  }
}, { immediate: true })

watch(() => sessionStore.currentSession, (curr) => {
  // 仅在当前未选中任何会话时，跟随 currentSession 赋予默认 ID
  if (!sessionStore.selectedSession && curr) {
    const currentWId = curr.workflow_id || curr.session_id
    if (currentWId) {
      sessionStore.fetchQueuedFeedbacks(currentWId)
      activeItemType.value = 'feedback'
      activeItemId.value = currentWId
    }
  }
})

// 离线告警横条 3 秒防抖（消除短时 1~2s 微闪重连对界面的视觉干扰）
const showOfflineBanner = ref(false)
let offlineBannerTimer: number | null = null

watch(() => sessionStore.sseConnected, (connected) => {
  if (connected) {
    if (offlineBannerTimer) {
      window.clearTimeout(offlineBannerTimer)
      offlineBannerTimer = null
    }
    showOfflineBanner.value = false
  } else {
    if (!offlineBannerTimer) {
      offlineBannerTimer = window.setTimeout(() => {
        if (!sessionStore.sseConnected) {
          showOfflineBanner.value = true
        }
        offlineBannerTimer = null
      }, 3000)
    }
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
  updateSidebarCssVar()
  window.addEventListener('resize', updateSidebarCssVar)
  nextTick(() => messageListRef.value?.scrollToBottom(false))
})

onUnmounted(() => {
  cleanupTimer()
  window.removeEventListener('resize', updateSidebarCssVar)
})
</script>

<template>
  <div class="h-full w-full h-[100dvh] flex bg-background text-foreground overflow-hidden">
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
    <main class="flex-1 min-w-0 h-full flex flex-col bg-background overflow-hidden relative">
      <!-- 离线告警全局横幅 (Demand 6: SSE 离线可见性指示与主动重连) -->
      <Transition
        enter-active-class="transition-all duration-200 ease-out"
        enter-from-class="-translate-y-full opacity-0"
        enter-to-class="translate-y-0 opacity-100"
        leave-active-class="transition-all duration-150 ease-in"
        leave-from-class="translate-y-0 opacity-100"
        leave-to-class="-translate-y-full opacity-0"
      >
        <div
          v-if="showOfflineBanner"
          class="bg-amber-500/15 border-b border-amber-500/30 text-amber-600 dark:text-amber-400 px-3 py-1.5 text-xs font-mono flex items-center justify-between shrink-0 z-50 select-none backdrop-blur-xs"
        >
          <div class="flex items-center gap-2 min-w-0">
            <WifiOff class="w-3.5 h-3.5 shrink-0 animate-pulse text-amber-500" />
            <span class="truncate">
              {{ sessionStore.isReconnecting ? '实时流已断开，正在尝试自动重连...' : '实时事件流已离线，数据可能不是最新' }}
            </span>
          </div>
          <button
            type="button"
            class="px-2 py-0.5 rounded border border-amber-500/40 hover:bg-amber-500/20 text-amber-700 dark:text-amber-300 transition-colors flex items-center gap-1 cursor-pointer shrink-0"
            @click="sessionStore.manualReconnect()"
          >
            <RefreshCw class="w-3 h-3" :class="sessionStore.isReconnecting ? 'animate-spin' : ''" />
            <span>立即重连</span>
          </button>
        </div>
      </Transition>

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
          :workflow-id="sessionStore.selectedSession?.workflow_id || sessionStore.selectedSession?.session_id || 'default'"
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
