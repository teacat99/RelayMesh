<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useTaskStore } from '../stores/task'
import { useSettingsStore } from '../stores/settings'
import { useAuthStore } from '../stores/auth'
import AppSidebar, { type UnifiedItem } from '../components/AppSidebar.vue'
import ArchiveDialog from '../components/ArchiveDialog.vue'
import SessionHeader from '../components/feedback/SessionHeader.vue'
import FeedbackMessageList from '../components/feedback/FeedbackMessageList.vue'
import PreviewMessageList from '../components/feedback/preview-view/PreviewMessageList.vue'
import FeedbackInputDock from '../components/feedback/FeedbackInputDock.vue'
import ViewTrialLinks from '../components/feedback/ViewTrialLinks.vue'
import GroupPage from '../components/group/GroupPage.vue'
import { usePreviewView } from '../composables/usePreviewView'
import { sessionJumpRequest } from '../composables/useSessionJump'
import TaskWorkspace from '../components/feedback/TaskWorkspace.vue'
import WorkPage from '../components/work/WorkPage.vue'
import DocsPage from '../components/docs/DocsPage.vue'
import ProjectsPage from '../components/projects/ProjectsPage.vue'
import PendingTopBar from '../components/shell/PendingTopBar.vue'
import ArtifactPreviewPane from '../components/artifact/ArtifactPreviewPane.vue'
import { useProjectDocStore } from '../stores/projectDocs'
import WorkItemDetail from '../components/work/WorkItemDetail.vue'
import WorkflowTaskStrip from '../components/feedback/WorkflowTaskStrip.vue'
import { useWorkItemStore } from '../stores/workItems'
import { useConfirmStore } from '../stores/confirms'
import { sessionsApi } from '../api/client'
import { settleDialogHistory } from '../composables/useDialogBackGesture'
import type { FeedbackSession, SessionImage } from '../api/types'
import { toast } from 'vue-sonner'
import { AlertTriangle, RefreshCw, WifiOff } from 'lucide-vue-next'
import { perfMark, perfMeasure } from '../utils/perf'
import { appendedToast, retractedToast } from '../lib/inbox'
import { isNotice, onlyRounds } from '../lib/asyncMode'
import { isDeepLinkPending } from '../lib/notifyBus'
import {
  SIDEBAR_PREF_KEY,
  countVisibleWorkflows,
  effectiveSidebarCollapsed,
  parseSidebarPref,
  type SidebarPref,
} from '../lib/sidebarDefault'

const sessionStore = useSessionStore()
const taskStore = useTaskStore()
const settingsStore = useSettingsStore()
const authStore = useAuthStore()
const workStore = useWorkItemStore()
const confirmStore = useConfirmStore()
const router = useRouter()
const route = useRoute()

// 任务清单（/work/:workflowId）与会话共用同一个壳：左侧栏不变，右侧工作区整页换成任务清单。
const isWorkView = computed(() => route.name === 'work')
const isDocsView = computed(() => route.name === 'docs')
const isGroupView = computed(() => route.name === 'group')
// 项目页与项目里的单篇文档页（project-doc，#131）同属项目区：同一个壳、同样的侧栏高亮与「点会话回首页」行为。
const isProjectsView = computed(() => route.name === 'projects' || route.name === 'project-doc')
const docStore = useProjectDocStore()

const activeItemType = ref<'feedback' | 'task'>('feedback')
const activeItemId = ref<string | null>(null)

// 会话页（时间线）是否在显示。深链接（/?wf=…&s=…）解析完之前不算：此时「默认选中的会话」只是占位，马上会被目标取代，
// 为它取整张会话表 / 任务 / 确认项都是白付的（#169）。
const onSessionPane = computed(() =>
  !isWorkView.value && !isDocsView.value && !isGroupView.value && !isProjectsView.value &&
  activeItemType.value === 'feedback' && !isDeepLinkPending(route.name, route.query),
)
// 整张会话表只在会话页显示时才顺带加载（任务 / 文档 / 群聊 / 项目等页面不需要它）；切回来时 store 先秒开缓存、再回源
watch(onSessionPane, shown => sessionStore.setTimelineActive(shown), { immediate: true })
onUnmounted(() => sessionStore.setTimelineActive(false))

// 会话页的任务数据（页头下方的「项目任务」总览、每一轮下方的「本轮任务」）：只在看会话时加载当前工作流的任务，
// 进入任务清单整页、托管自驾或离开时清掉。
const feedWorkflowId = computed(() => {
  if (!onSessionPane.value) return ''
  return (sessionStore.selectedSession || sessionStore.currentSession)?.workflow_id || ''
})
watch(feedWorkflowId, id => workStore.watchFeed(id), { immediate: true })
onUnmounted(() => workStore.watchFeed(''))
// 时间线里的确认卡与工具栏的「待答复」徽标：同样只在看会话时关注当前工作流
watch(feedWorkflowId, id => confirmStore.watchWorkflow(id), { immediate: true })
onUnmounted(() => confirmStore.watchWorkflow(''))
// 预览视图（#120，#165 起是默认视图）：只换中间的会话列表，输入区仍是经典的那一个（同一个 handleSubmit）；
// 明确选了经典的设备（本机开关 '0'）仍看经典列表。
const previewView = usePreviewView()
const showPreviewView = computed(() => previewView.enabled.value)
// 手机宽度：「回到经典视图 / 试用预览视图」入口不再单占页头下面的一行，改放进输入区上方的芯片条末尾（#155）
const isPhone = useMediaQuery('(max-width: 767.98px)')
const isArchiveOpen = ref(false)
const isMobileSidebarOpen = ref(false)
const isChatScrolledUp = ref(false)
const hasDraftImages = computed(() => {
  return (inputDockRef.value?.images?.length || 0) > 0
})

// 经典列表与预览视图的列表暴露同一组方法（scrollToSession / scrollToBottom / scrollByDelta），同一时刻只挂载其中一个。
const messageListRef = ref<InstanceType<typeof FeedbackMessageList> | InstanceType<typeof PreviewMessageList> | null>(null)

// 正文里点「会话号」（sess-xxxxxxxx）：在当前会话页里滚到那一轮。请求由 composables/useLinkRefs 经 useSessionJump 单例转交，
// 只响应正在看的这个工作流（别的页面 / 别的工作流的请求走深链，不会落到这里）
watch(sessionJumpRequest, req => {
  if (!req) return
  const sel = sessionStore.selectedSession
  if ((sel?.workflow_id || sel?.session_id || '') !== req.workflowId) return
  void nextTick(() => messageListRef.value?.scrollToSession(req.sessionId))
})
const inputDockRef = ref<InstanceType<typeof FeedbackInputDock> | null>(null)

// 当前工作流的全部会话记录（含异步模式下的进度通知），按时间升序（最早在上、最新在下）
const workflowTimeline = computed(() => {
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
// 真实的轮次：轮次编号、微缩轴、「第 N 轮」都只认它
const conversationRounds = computed(() => onlyRounds(workflowTimeline.value))
// 进度通知（异步模式下 wait:false 发出的话）：穿插在轮次之间，不算轮次
const conversationNotices = computed(() => workflowTimeline.value.filter(isNotice))

async function handleSelectItem(item: UnifiedItem) {
  // 在任务清单 / 群聊里点了侧栏的会话：先回到会话页，等右侧的会话视图挂载好再选中、滚动
  if (isWorkView.value || isGroupView.value || isProjectsView.value) {
    await router.push('/')
    await nextTick()
  }
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

// 手机顶条「待答复 N」：直达等得最久的那个会话（与侧栏里点它是同一条路径，从任务清单 / 群聊 / 项目页来也会先回会话页）
async function openPendingSession(s: FeedbackSession) {
  isMobileSidebarOpen.value = false
  await handleSelectItem({
    id: s.workflow_id || s.session_id,
    type: 'feedback',
    title: s.title || '',
    subtitle: s.workflow_id || s.project_directory,
    summary: s.summary || '',
    status: 'pending',
    created_at: s.created_at,
    updated_at: s.updated_at,
    project_directory: s.project_directory,
    workflow_id: s.workflow_id,
    raw: s,
  })
}

function handleOpenWork() {
  isMobileSidebarOpen.value = false
  if (isWorkView.value) return
  // 侧栏的「任务清单」：优先进入正在看的会话所属的项目；没有当前会话时由任务页自己挑一个项目
  const workflowId = (sessionStore.selectedSession || sessionStore.currentSession)?.workflow_id
  void router.push(workflowId ? { name: 'work', params: { workflowId } } : { name: 'work' })
}

// 任务详情里点了「关联会话」：回到会话页并定位到那一轮
async function openSessionFromWork(sessionId: string) {
  let target = sessionStore.sessions.find(s => s.session_id === sessionId) || null
  if (!target) {
    try {
      target = (await sessionsApi.get(sessionId)).session
    } catch {
      toast.info('找不到这个会话，它可能已被删除')
      return
    }
  }
  // 详情抽屉刚刚关闭：它在浏览器历史里压过一条哨兵记录，要等关闭时的回退走完再跳转，否则回退会把新页面又退回 /work
  await settleDialogHistory()
  await router.push('/')
  await nextTick()
  const workflowId = target.workflow_id || target.session_id
  activeItemType.value = 'feedback'
  activeItemId.value = workflowId
  sessionStore.selectSession(target)
  await sessionStore.loadWorkflowSessions(workflowId)
  await nextTick()
  messageListRef.value?.scrollToSession(target.session_id)
}

// 任务详情弹窗（会话页版）：点「本轮任务」里的任务行、点正文里的 #N 都就地打开，不离开对话。
// 任务清单页自己挂着一份（WorkPage），两者绑定同一个任务 store 的选中项，所以切换视图时先关掉，免得两边各开一个。
const taskPreviewOpen = computed({
  get: () => !isWorkView.value && workStore.selectedId !== null,
  set: v => {
    if (!v) workStore.closeDetail()
  },
})
const taskPreviewNow = ref(Date.now())
watch(taskPreviewOpen, open => {
  if (open) taskPreviewNow.value = Date.now()
})
watch(isWorkView, () => workStore.closeDetail())

async function openSessionFromPreview(sessionId: string) {
  workStore.closeDetail()
  await openSessionFromWork(sessionId)
}

function handleOpenSettings() {
  if (window.innerWidth < 768) {
    router.push({ path: '/settings', query: { module: 'relay' } })
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
    toast.success(appendedToast(sessionStore.inboxConfig))
  }

  nextTick(() => messageListRef.value?.scrollToBottom())
}

async function handleRevokeSession(sessionId: string) {
  if (import.meta.env.DEV) perfMark('revoke:click')
  try {
    const revoked = await sessionStore.revokeFeedback(sessionId)
    if (revoked) {
      inputDockRef.value?.loadSpecificContent({
        text: revoked.response_text || '',
        presets: revoked.user_messages || [],
        images: revoked.images || []
      })
      if (import.meta.env.DEV) nextTick(() => perfMeasure('revoke:click→filled', 'revoke:click'))
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
      toast.success(retractedToast(revoked.delivered))
    }
  } catch (err: any) {
    toast.error('撤回失败: ' + (err.response?.data?.error || err.message || err))
  }
}

function handleInputDockResize(delta: number) {
  messageListRef.value?.scrollByDelta(delta)
}

// 侧栏收起状态（#190）：用户手动切换过就以他的选择为准（存在 localStorage，键与旧版本相同）；没选过时，
// stdio 直驱在只有一个工作流（单窗口）的情况下默认收起，出现第二个工作流就自动展开，常驻服务一律展开。
// 自动状态只是算出来的、不落盘，否则它就会变成「手动选择」。
const sidebarPref = ref<SidebarPref>(parseSidebarPref(localStorage.getItem(SIDEBAR_PREF_KEY)))
const visibleWorkflowCount = computed(() =>
  countVisibleWorkflows(sessionStore.currentSession ? [...sessionStore.sessions, sessionStore.currentSession] : sessionStore.sessions),
)
const isSidebarCollapsed = computed<boolean>(() =>
  effectiveSidebarCollapsed({ runMode: authStore.runMode, pref: sidebarPref.value, workflowCount: visibleWorkflowCount.value }),
)

function updateSidebarCssVar() {
  if (typeof window === 'undefined') return
  let w = '0px'
  if (!isSidebarCollapsed.value && window.innerWidth >= 768) {
    w = window.innerWidth >= 1024 ? '20rem' : '16rem'
  }
  document.documentElement.style.setProperty('--sidebar-current-width', w)
}

function toggleSidebarCollapse() {
  const next = !isSidebarCollapsed.value
  sidebarPref.value = next
  try {
    localStorage.setItem(SIDEBAR_PREF_KEY, String(next))
  } catch (_) {}
  updateSidebarCssVar()
}

watch(isSidebarCollapsed, () => {
  updateSidebarCssVar()
}, { immediate: true })

// 按工作流 id 监听而不是按会话对象监听：SSE 补丁、保活、轮询都会替换 selectedSession / currentSession 对象，
// 对象身份一变就重拉待发送队列，一次撤回曾因此多出 5 次无意义的请求（D-171 / P1）。
watch(() => sessionStore.selectedSession?.workflow_id || sessionStore.selectedSession?.session_id || '', (currentWId) => {
  if (currentWId) {
    sessionStore.fetchQueuedFeedbacks(currentWId)
    activeItemType.value = 'feedback'
    activeItemId.value = currentWId
  }
}, { immediate: true })

watch(() => sessionStore.currentSession?.workflow_id || sessionStore.currentSession?.session_id || '', (currentWId) => {
  // 仅在当前未选中任何会话时，跟随 currentSession 赋予默认 ID
  if (!sessionStore.selectedSession && currentWId) {
    sessionStore.fetchQueuedFeedbacks(currentWId)
    activeItemType.value = 'feedback'
    activeItemId.value = currentWId
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
    // fresh:false：与布局 / 侧栏同一时刻的挂载取数合并成一次请求（#177）
    sessionStore.fetchCurrentSession({ fresh: false }),
    sessionStore.fetchSessions(undefined, { fresh: false }),
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
  window.removeEventListener('resize', updateSidebarCssVar)
})
</script>

<template>
  <div class="h-full w-full h-[100dvh] flex bg-background text-foreground overflow-hidden">
    <!-- 1. Top-to-Bottom Piercing Left Sidebar -->
    <AppSidebar
      :active-item-id="activeItemId"
      :active-item-type="activeItemType"
      :mobile-open="isMobileSidebarOpen"
      :collapsed="isSidebarCollapsed"
      :work-active="isWorkView"
      :group-active="isGroupView || isProjectsView"
      @open-work="handleOpenWork"
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
        if (isWorkView || isGroupView || isProjectsView) router.push('/')
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

      <!-- 手机：别处有会话在等你答复时的顶条（点一下直达等得最久的那个；PC 侧栏常驻，不需要） -->
      <PendingTopBar @open="openPendingSession" />

      <!-- ========================================== -->
      <!-- VIEW W: 任务清单（独立于会话的工作清单）    -->
      <!-- ========================================== -->
      <WorkPage
        v-if="isWorkView"
        :sidebar-collapsed="isSidebarCollapsed"
        @open-mobile-sidebar="isMobileSidebarOpen = true"
        @expand-sidebar="toggleSidebarCollapse"
        @open-session="openSessionFromWork"
      />

      <GroupPage
        v-else-if="isGroupView"
        :sidebar-collapsed="isSidebarCollapsed"
        @open-mobile-sidebar="isMobileSidebarOpen = true"
        @expand-sidebar="toggleSidebarCollapse"
      />

      <ProjectsPage
        v-else-if="isProjectsView"
        :sidebar-collapsed="isSidebarCollapsed"
        @open-mobile-sidebar="isMobileSidebarOpen = true"
        @expand-sidebar="toggleSidebarCollapse"
      />

      <DocsPage
        v-else-if="isDocsView"
        :sidebar-collapsed="isSidebarCollapsed"
        @open-mobile-sidebar="isMobileSidebarOpen = true"
        @expand-sidebar="toggleSidebarCollapse"
      />

      <!-- ========================================== -->
      <!-- VIEW A: FEEDBACK INTERACTION CONVERSATION  -->
      <!-- ========================================== -->
      <template v-else-if="activeItemType === 'feedback'">
        <!-- Top Workspace Header -->
        <SessionHeader
          :sidebar-collapsed="isSidebarCollapsed"
          @open-mobile-sidebar="isMobileSidebarOpen = true"
          @expand-sidebar="toggleSidebarCollapse"
          @jump-session="messageListRef?.scrollToSession($event)"
        />

        <!-- 项目任务总览：一行，点一下进入任务清单；这个项目没有任务时不出现 -->
        <WorkflowTaskStrip />

        <!-- 视图入口（搜索消息 / 回到经典视图 / 试用预览视图）：一行小字，不占地方（手机宽度改放进输入区上方的芯片条，见 FeedbackInputDock） -->
        <div v-if="feedWorkflowId && !isPhone" class="flex justify-end gap-3 px-3 pt-1">
          <ViewTrialLinks />
        </div>

        <!-- Middle Scrollable Conversation Feed：预览视图开着时换成预览列表（经典一个字不动） -->
        <PreviewMessageList
          v-if="showPreviewView"
          ref="messageListRef"
          data-testid="preview-view"
          :conversation-rounds="conversationRounds"
          :notices="conversationNotices"
          :has-draft-images="hasDraftImages"
          @scroll-state-change="(scrolled) => isChatScrolledUp = scrolled"
          @revoke-session="handleRevokeSession"
          @revoke-queued="handleRevokeQueued"
        />
        <FeedbackMessageList
          v-else
          ref="messageListRef"
          :conversation-rounds="conversationRounds"
          :notices="conversationNotices"
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

        <!-- 任务详情弹窗：会话里点任务行 / 正文里的 #N 就地打开（桌面是弹窗，手机是抽屉） -->
        <WorkItemDetail
          v-model:open="taskPreviewOpen"
          :sessions="sessionStore.sessions"
          :now="taskPreviewNow"
          :allow-add-child="false"
          @open-session="openSessionFromPreview"
          @open-item="workStore.openDetail"
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

    <!-- 聊天里点开 Artifact / 文档引用后的右侧预览（手机上盖满屏幕） -->
    <ArtifactPreviewPane />
  </div>
</template>
