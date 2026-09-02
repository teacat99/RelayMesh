<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import dayjs from 'dayjs'
import type { FeedbackSession } from '../../api/types'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { usePreviewStore } from '../../stores/preview'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import TimelineScrubber from '../TimelineScrubber.vue'
import Badge from '../ui/badge/Badge.vue'
import {
  Inbox,
  Sparkles,
  FolderGit2,
  User,
  AlertCircle,
  ArrowDown,
  MessageSquarePlus,
  Copy,
  Undo2,
  Clock,
  CheckCheck
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'

async function copyText(text: string, label: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  } catch (_) {
    const input = document.createElement('textarea')
    input.value = text
    document.body.appendChild(input)
    input.select()
    document.execCommand('copy')
    document.body.removeChild(input)
    toast.success(`已复制 ${label}`, {
      description: text,
      duration: 1800
    })
  }
}

// FeedbackMessageList.vue
const props = defineProps<{
  conversationRounds: FeedbackSession[]
  hasDraftImages?: boolean
}>()

const emit = defineEmits<{
  (e: 'scroll-state-change', isScrolledUp: boolean): void
  (e: 'revoke-session', sessionId: string): void
  (e: 'revoke-queued', queuedId: number): void
}>()

const sessionStore = useSessionStore()
const previewStore = usePreviewStore()

function getImageUrl(img: { data?: string; format?: string } | null | undefined): string {
  if (!img || !img.data) return ''
  if (img.data.startsWith('data:') || img.data.startsWith('http://') || img.data.startsWith('https://')) {
    return img.data
  }
  const format = img.format || 'png'
  return `data:image/${format};base64,${img.data}`
}
const settingsStore = useSettingsStore()

function formatSessionProjectDirectory(session: FeedbackSession): string {
  if (!session.project_directory) return ''
  const host = session.host_name || settingsStore.settings.hostName
  if (host && host.trim()) {
    return `${host.trim()}:${session.project_directory}`
  }
  return session.project_directory
}

const chatScrollContainer = ref<HTMLElement | null>(null)
const activeSessionIdInScrubber = ref<string | null>(null)
const highlightedSessionId = ref<string | null>(null)
let highlightTimer: any = null
const isScrolledUp = ref(false)

// 历史多轮对话无感向上滚动分页加载
const INITIAL_VISIBLE_ROUNDS = 10
const BATCH_LOAD_ROUNDS = 10
const visibleRoundsCount = ref(INITIAL_VISIBLE_ROUNDS)
const isLoadingEarlier = ref(false)
const topSentinelRef = ref<HTMLElement | null>(null)
let topObserver: IntersectionObserver | null = null
const unreadNewMessagesCount = ref(0)
const currentWorkflowId = ref<string | null>(null)
const knownSessionIds = ref<Set<string>>(new Set())

const displayedConversationRounds = computed(() => {
  const total = props.conversationRounds.length
  if (total <= visibleRoundsCount.value) {
    return props.conversationRounds
  }
  return props.conversationRounds.slice(total - visibleRoundsCount.value)
})

const hasMoreEarlierRounds = computed(() => {
  return props.conversationRounds.length > visibleRoundsCount.value
})

function loadEarlierRounds() {
  if (!hasMoreEarlierRounds.value || isLoadingEarlier.value || !chatScrollContainer.value) {
    return
  }

  isLoadingEarlier.value = true
  const container = chatScrollContainer.value
  const oldScrollHeight = container.scrollHeight
  const oldScrollTop = container.scrollTop

  visibleRoundsCount.value = Math.min(
    props.conversationRounds.length,
    visibleRoundsCount.value + BATCH_LOAD_ROUNDS
  )

  nextTick(() => {
    if (chatScrollContainer.value) {
      const newScrollHeight = chatScrollContainer.value.scrollHeight
      const heightDifference = newScrollHeight - oldScrollHeight
      // 保持用户视口相对位置绝对稳定，向上延伸无感承接
      chatScrollContainer.value.scrollTop = oldScrollTop + heightDifference
    }
    setTimeout(() => {
      isLoadingEarlier.value = false
    }, 60)
  })
}

function setupTopObserver() {
  if (topObserver) {
    topObserver.disconnect()
    topObserver = null
  }
  if (!topSentinelRef.value || !chatScrollContainer.value) return

  topObserver = new IntersectionObserver((entries) => {
    const entry = entries[0]
    if (entry && entry.isIntersecting && hasMoreEarlierRounds.value && !isLoadingEarlier.value) {
      loadEarlierRounds()
    }
  }, {
    root: chatScrollContainer.value,
    rootMargin: '250px 0px 0px 0px',
    threshold: 0.01
  })

  topObserver.observe(topSentinelRef.value)
}

watch(topSentinelRef, () => {
  setupTopObserver()
})

onMounted(() => {
  setupTopObserver()
  nextTick(() => {
    updateActiveSessionFromScroll()
  })
})

onBeforeUnmount(() => {
  if (topObserver) {
    topObserver.disconnect()
    topObserver = null
  }
})

function getSessionTurnNumber(session: FeedbackSession): number {
  const index = props.conversationRounds.findIndex(s => s.session_id === session.session_id)
  return index !== -1 ? index + 1 : 1
}

watch(() => props.conversationRounds, (newRounds) => {
  const targetWId = newRounds.length > 0 ? (newRounds[0].workflow_id || newRounds[0].session_id) : null

  // 1. 如果切换了不同的 Workflow 或首次载入：彻底重置未读计数，并记录已知 session_id 集合
  if (targetWId !== currentWorkflowId.value) {
    currentWorkflowId.value = targetWId
    unreadNewMessagesCount.value = 0
    visibleRoundsCount.value = INITIAL_VISIBLE_ROUNDS
    knownSessionIds.value = new Set(newRounds.map(r => r.session_id))
    nextTick(() => {
      scrollToBottom(false)
      setupTopObserver()
    })
    return
  }

  // 2. 在同一 Workflow 内，检测是否有真正新增的会话轮次 session_id
  const newlyAddedSessions = newRounds.filter(r => !knownSessionIds.value.has(r.session_id))
  if (newlyAddedSessions.length > 0) {
    for (const s of newlyAddedSessions) {
      knownSessionIds.value.add(s.session_id)
    }

    // 仅当用户向上滚动查看历史时，才展示未读新消息浮动提醒
    if (isScrolledUp.value) {
      unreadNewMessagesCount.value += newlyAddedSessions.length
    } else {
      unreadNewMessagesCount.value = 0
      nextTick(() => scrollToBottom(true))
    }
  }
}, { deep: true, immediate: true })

function handleJumpToLatest() {
  unreadNewMessagesCount.value = 0
  scrollToBottom(true)
}

function formatDateTimeCustom(dateStr?: string): string {
  if (!dateStr) return ''
  const target = dayjs(dateStr)
  if (!target.isValid()) return ''
  const now = dayjs()

  const isSameYear = target.isSame(now, 'year')
  const isSameDay = isSameYear && target.isSame(now, 'day')

  if (isSameDay) {
    return target.format('HH:mm')
  } else if (isSameYear) {
    return target.format('MM-DD·HH:mm')
  } else {
    return target.format('YYYY·MM-DD·HH:mm')
  }
}

function getStatusBadge(status: string) {
  switch (status) {
    case 'completed': return { label: '已完成', variant: 'default' }
    case 'pending': return { label: '等待确认', variant: 'secondary' }
    case 'cancelled': return { label: '已取消', variant: 'outline' }
    case 'timeout': return { label: '已超时', variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: status, variant: 'outline' }
  }
}

function scrollToSession(sessionId: string) {
  const targetIdx = props.conversationRounds.findIndex(s => s.session_id === sessionId)
  if (targetIdx === -1) return

  const total = props.conversationRounds.length
  const neededVisibleCount = total - targetIdx
  if (neededVisibleCount > visibleRoundsCount.value) {
    visibleRoundsCount.value = Math.min(total, neededVisibleCount + 5)
  }

  nextTick(() => {
    const el = document.getElementById(`session-${sessionId}`)
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' })
      activeSessionIdInScrubber.value = sessionId
      highlightedSessionId.value = sessionId
      if (highlightTimer) clearTimeout(highlightTimer)
      highlightTimer = setTimeout(() => {
        highlightedSessionId.value = null
      }, 2500)
    }
  })
}

function scrollToBottom(smooth = true) {
  isScrolledUp.value = false
  unreadNewMessagesCount.value = 0
  emit('scroll-state-change', false)
  if (props.conversationRounds.length > 0) {
    activeSessionIdInScrubber.value = props.conversationRounds[props.conversationRounds.length - 1].session_id
  }
  if (chatScrollContainer.value) {
    const el = chatScrollContainer.value
    if (smooth) {
      el.scrollTo({
        top: el.scrollHeight,
        behavior: 'smooth'
      })
      setTimeout(() => {
        if (el) el.scrollTop = el.scrollHeight
        updateActiveSessionFromScroll()
      }, 250)
    } else {
      // 首次加载/刷新/切换会话：瞬间即时直达底部，无滑动动画
      el.scrollTop = el.scrollHeight
      nextTick(() => {
        if (el) el.scrollTop = el.scrollHeight
        updateActiveSessionFromScroll()
      })
    }
  }
}

// 供输入框高度变化时同步滚动同样的高度增量
function scrollByDelta(delta: number) {
  if (chatScrollContainer.value) {
    chatScrollContainer.value.scrollTop += delta
  }
}

function updateActiveSessionFromScroll() {
  if (!chatScrollContainer.value || props.conversationRounds.length === 0) return

  const container = chatScrollContainer.value
  const { scrollTop, scrollHeight, clientHeight } = container

  // 1. 若接近最底部 (30px 内)，直接选定最新一轮
  if (scrollHeight - (scrollTop + clientHeight) <= 30) {
    const lastSession = props.conversationRounds[props.conversationRounds.length - 1]
    if (lastSession) {
      activeSessionIdInScrubber.value = lastSession.session_id
      return
    }
  }

  // 2. 若在最顶部 (10px 内)，选定当前展示的最早一轮
  if (scrollTop <= 10 && displayedConversationRounds.value.length > 0) {
    activeSessionIdInScrubber.value = displayedConversationRounds.value[0].session_id
    return
  }

  // 3. 计算视口中间偏上基准线 (35% 高度处作为阅读视线)
  const containerRect = container.getBoundingClientRect()
  const targetY = containerRect.top + containerRect.height * 0.35

  let bestSessionId: string | null = null
  let minDistance = Infinity

  for (const s of displayedConversationRounds.value) {
    const el = document.getElementById(`session-${s.session_id}`)
    if (el) {
      const rect = el.getBoundingClientRect()
      // 如果当前卡片正跨越视线基准点
      if (rect.top <= targetY && rect.bottom >= targetY) {
        bestSessionId = s.session_id
        break
      }
      // 否则计算卡片中心点与视线基准点的距离
      const cardCenter = (rect.top + rect.bottom) / 2
      const dist = Math.abs(cardCenter - targetY)
      if (dist < minDistance) {
        minDistance = dist
        bestSessionId = s.session_id
      }
    }
  }

  if (bestSessionId) {
    activeSessionIdInScrubber.value = bestSessionId
  }
}

function handleScroll() {
  if (!chatScrollContainer.value) return
  const { scrollTop, scrollHeight, clientHeight } = chatScrollContainer.value
  const scrolledUp = scrollHeight - (scrollTop + clientHeight) > 80
  isScrolledUp.value = scrolledUp
  emit('scroll-state-change', scrolledUp)

  // 滚回底部时自动清除未读计数
  if (!scrolledUp) {
    unreadNewMessagesCount.value = 0
  }

  // 向上滚动触顶或接近顶部 (scrollTop < 250px) 时自动无感加载更早轮次
  if (scrollTop < 250 && hasMoreEarlierRounds.value && !isLoadingEarlier.value) {
    loadEarlierRounds()
  }

  // 自动实时更新 TimelineScrubber 跟踪的活跃会话节点
  updateActiveSessionFromScroll()
}

defineExpose({
  scrollToBottom,
  scrollByDelta,
  scrollToSession
})
</script>

<template>
  <!-- Middle Conversation Area -->
  <div class="flex-1 overflow-hidden relative">
    <!-- Scrollable Conversation Feed -->
    <div
      ref="chatScrollContainer"
      class="h-full overflow-y-auto px-3 sm:px-8 py-4 sm:py-6 space-y-6 sm:space-y-8 transition-[padding] duration-150 no-scrollbar scrollbar-none"
      :class="props.hasDraftImages ? 'pb-28 sm:pb-32' : 'pb-14 sm:pb-16'"
      @scroll="handleScroll"
    >
      <!-- Empty State -->
      <div
        v-if="props.conversationRounds.length === 0"
        class="flex flex-col items-center justify-center min-h-[50vh] text-center p-8 space-y-3"
      >
        <Inbox class="w-12 h-12 text-muted-foreground stroke-[1.2]" />
        <h3 class="text-sm font-semibold text-foreground">暂无交互对话</h3>
        <p class="text-xs text-muted-foreground max-w-sm">
          当 AI 客户端发起方案反馈请求时，多轮对话将在此实时呈现。
        </p>
      </div>

      <!-- Multi-turn Conversation Messages (支持无感向上滚动分页渲染) -->
      <div v-else class="max-w-4xl mx-auto space-y-8 sm:space-y-10 pr-2 sm:pr-6">
        <!-- 顶部无感加载触发哨兵 / 历史起点标识 -->
        <div
          v-if="hasMoreEarlierRounds"
          ref="topSentinelRef"
          class="h-6 w-full flex items-center justify-center py-2 text-muted-foreground/40 text-[10px] font-mono pointer-events-none"
        >
          <span v-if="isLoadingEarlier" class="animate-pulse">正在载入更早历史对话...</span>
        </div>
        <div
          v-else-if="props.conversationRounds.length > 3"
          class="flex items-center justify-center gap-2 py-3 text-[11px] font-mono text-muted-foreground/50 select-none"
        >
          <div class="h-[1px] w-12 bg-border/40"></div>
          <span>已载入全部 {{ props.conversationRounds.length }} 轮历史对话</span>
          <div class="h-[1px] w-12 bg-border/40"></div>
        </div>

        <div
          v-for="session in displayedConversationRounds"
          :key="session.session_id"
          :id="`session-${session.session_id}`"
          class="space-y-3 sm:space-y-4 pt-2 transition-all p-2 rounded-md"
          :class="highlightedSessionId === session.session_id ? 'ring-2 ring-primary bg-primary/5' : ''"
        >
          <!-- Turn Divider & Timestamp Header -->
          <div class="flex items-center gap-2 sm:gap-3 select-none">
            <div class="h-[1px] flex-1 bg-border/60"></div>
            <div class="flex items-center gap-1.5 sm:gap-2 text-[10px] sm:text-[11px] font-mono text-muted-foreground">
              <span class="font-medium text-foreground"># 第 {{ getSessionTurnNumber(session) }} 轮</span>
              <span>·</span>
              <button
                type="button"
                class="bg-muted hover:bg-accent hover:text-accent-foreground px-1.5 py-0.5 rounded text-[10px] font-mono text-muted-foreground inline-flex items-center gap-1 cursor-pointer transition-colors group"
                :title="`点击复制会话 ID: ${session.session_id}`"
                @click="copyText(session.session_id, '会话 ID')"
              >
                <Copy class="w-2.5 h-2.5 text-muted-foreground group-hover:text-primary transition-colors" />
                <span>{{ session.session_id }}</span>
              </button>
              <span>·</span>
              <span class="truncate max-w-[100px] sm:max-w-none">{{ session.title || '方案汇报' }}</span>
              <span>·</span>
              <span>{{ formatDateTimeCustom(session.created_at) }}</span>
              <Badge :variant="getStatusBadge(session.status).variant as any" class="text-[9px] sm:text-[10px] px-1 sm:px-1.5 py-0 font-normal rounded-xs">
                {{ getStatusBadge(session.status).label }}
              </Badge>
            </div>
            <div class="h-[1px] flex-1 bg-border/60"></div>
          </div>

          <!-- AI Message Turn (Proposal) -->
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-1.5 sm:gap-2 text-xs font-medium text-muted-foreground">
                <Sparkles class="w-3.5 h-3.5 text-foreground shrink-0" />
                <span class="text-foreground font-medium text-xs">AI 方案汇报</span>
                <span v-if="session.workflow_id" class="text-[10px] sm:text-[11px] font-mono text-muted-foreground">
                  ({{ session.workflow_id }})
                </span>
              </div>
              <div
                v-if="session.project_directory"
                class="text-[10px] sm:text-[11px] font-mono text-muted-foreground hidden sm:flex items-center gap-1 hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors max-w-xs md:max-w-md lg:max-w-lg truncate group"
                :title="`点击复制工作区路径: ${formatSessionProjectDirectory(session)}`"
                @click="copyText(formatSessionProjectDirectory(session), '工作区路径')"
              >
                <FolderGit2 class="w-3 h-3 shrink-0 group-hover:text-primary transition-colors" />
                <span class="truncate">{{ formatSessionProjectDirectory(session) }}</span>
              </div>
            </div>

            <div class="p-3.5 sm:p-5 md:p-6 rounded-md border border-border/70 bg-card shadow-2xs leading-relaxed text-xs sm:text-sm">
              <MarkdownRenderer :content="session.summary" />
            </div>
          </div>

          <!-- User Message Turn (Feedback / Response) -->
          <div v-if="session.status === 'completed'" class="space-y-2 pl-2 sm:pl-8">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-1.5 sm:gap-2 text-xs font-medium text-muted-foreground">
                <User class="w-3.5 h-3.5 text-[color:var(--color-status-active)]" />
                <span class="text-foreground font-medium text-xs">您的确认回复</span>
                <span class="text-[10px] sm:text-[11px] font-mono text-muted-foreground">
                  {{ formatDateTimeCustom(session.updated_at || session.created_at) }}
                </span>
                <!-- AI 消费状态指示 -->
                <Badge
                  v-if="!session.consumed_by_ai"
                  variant="secondary"
                  class="text-[9px] sm:text-[10px] px-1.5 py-0 font-normal rounded-xs bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30 flex items-center gap-1"
                >
                  <Clock class="w-2.5 h-2.5 animate-pulse" />
                  <span>待 AI 提取</span>
                </Badge>
                <Badge
                  v-else
                  variant="outline"
                  class="text-[9px] sm:text-[10px] px-1.5 py-0 font-normal rounded-xs text-muted-foreground/80 flex items-center gap-1"
                >
                  <CheckCheck class="w-2.5 h-2.5 text-emerald-500" />
                  <span>AI 已接收</span>
                </Badge>
              </div>

              <!-- 撤回按钮 (仅在 AI 尚未提取消费时可用) -->
              <button
                v-if="!session.consumed_by_ai"
                type="button"
                class="inline-flex items-center gap-1 text-[11px] font-mono text-muted-foreground hover:text-destructive hover:bg-destructive/10 px-2 py-0.5 rounded cursor-pointer transition-colors border border-border/80 hover:border-destructive/40 group select-none"
                title="点击撤回此条反馈（内容将自动退回输入框重新编辑）"
                @click="emit('revoke-session', session.session_id)"
              >
                <Undo2 class="w-3 h-3 group-hover:-translate-x-0.5 transition-transform" />
                <span>撤回反馈</span>
              </button>
            </div>

            <div class="p-3 sm:p-4 rounded-md border border-border/60 bg-muted/30 space-y-2">
              <!-- User Selected Presets Tags -->
              <div v-if="session.user_messages && session.user_messages.length > 0" class="flex flex-wrap gap-1">
                <span
                  v-for="msg in session.user_messages"
                  :key="msg"
                  class="text-[10px] sm:text-[11px] px-1.5 sm:px-2 py-0.5 rounded-xs bg-muted border border-border text-foreground font-mono"
                >
                  {{ msg }}
                </span>
              </div>

              <!-- User Text Content -->
              <p class="text-xs sm:text-sm text-foreground whitespace-pre-wrap leading-relaxed">
                {{ session.response_text || '（已确认，未附加文字）' }}
              </p>

              <!-- Attached Screenshots (支持点击放大预览) -->
              <div v-if="session.images && session.images.length > 0" class="pt-2 flex flex-wrap gap-2">
                <img
                  v-for="(img, imgIdx) in session.images"
                  :key="imgIdx"
                  :src="getImageUrl(img)"
                  class="rounded-sm border border-border max-h-24 sm:max-h-36 object-cover shadow-2xs cursor-pointer hover:opacity-90 transition-opacity"
                  :alt="img.name || `attachment-${imgIdx + 1}`"
                  @click.stop="previewStore.openImagePreview({
                    src: getImageUrl(img),
                    alt: img.name || `第 ${getSessionTurnNumber(session)} 轮用户附件-${imgIdx + 1}`
                  })"
                />
              </div>
            </div>
          </div>

          <!-- Cancelled or Expired Notice -->
          <div v-else-if="session.status === 'cancelled' || session.status === 'timeout'" class="pl-2 sm:pl-8 text-xs text-muted-foreground italic flex items-center gap-1.5 py-1">
            <AlertCircle class="w-3.5 h-3.5" />
            <span>本轮交互已关闭（{{ getStatusBadge(session.status).label }}）</span>
          </div>
        </div>

        <!-- Queued Feedbacks List (无交互期间提前追加的指令，待 AI 下次发起交互时秒回) -->
        <div v-if="sessionStore.queuedFeedbacks && sessionStore.queuedFeedbacks.length > 0" class="space-y-4 pt-4 border-t border-dashed border-border/80">
          <div
            v-for="q in sessionStore.queuedFeedbacks"
            :key="q.id"
            class="space-y-2 pl-2 sm:pl-8 p-3.5 sm:p-4 rounded-md border border-primary/40 bg-primary/5 shadow-2xs"
          >
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-1.5 sm:gap-2 text-xs font-medium text-foreground">
                <Clock class="w-3.5 h-3.5 text-primary animate-pulse shrink-0" />
                <span class="font-semibold text-primary">已暂存追加指令 (待 AI 下次交互时即时秒回)</span>
                <span class="text-[10px] sm:text-[11px] font-mono text-muted-foreground">
                  {{ formatDateTimeCustom(q.created_at) }}
                </span>
              </div>
              <button
                type="button"
                class="inline-flex items-center gap-1 text-[11px] font-mono text-destructive hover:bg-destructive/15 px-2 py-0.5 rounded cursor-pointer transition-colors border border-destructive/30 select-none group"
                title="撤回该暂存指令并带回输入框重新编辑"
                @click="emit('revoke-queued', q.id)"
              >
                <Undo2 class="w-3 h-3 group-hover:-translate-x-0.5 transition-transform" />
                <span>撤回指令</span>
              </button>
            </div>

            <div v-if="q.user_messages && q.user_messages.length > 0" class="flex flex-wrap gap-1">
              <span
                v-for="msg in q.user_messages"
                :key="msg"
                class="text-[10px] sm:text-[11px] px-1.5 sm:px-2 py-0.5 rounded-xs bg-muted border border-border text-foreground font-mono"
              >
                {{ msg }}
              </span>
            </div>

            <p class="text-xs sm:text-sm text-foreground whitespace-pre-wrap leading-relaxed font-normal">
              {{ q.response_text || '（已确认）' }}
            </p>

            <div v-if="q.images && q.images.length > 0" class="pt-2 flex flex-wrap gap-2">
              <img
                v-for="(img, imgIdx) in q.images"
                :key="imgIdx"
                :src="getImageUrl(img)"
                class="rounded-sm border border-border max-h-24 sm:max-h-36 object-cover shadow-2xs cursor-pointer hover:opacity-90 transition-opacity"
                :alt="img.name || `queued-attachment-${imgIdx + 1}`"
                @click.stop="previewStore.openImagePreview({
                  src: getImageUrl(img),
                  alt: img.name || `暂存附件-${imgIdx + 1}`
                })"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- In-Session Timeline Scrubber (Inside the conversation viewport on the right, 仅多轮时展示并严格限制垂直最大高度，避免溢出视口) -->
    <div
      v-if="props.conversationRounds.length > 1"
      class="hidden md:flex flex-col justify-center absolute right-2 sm:right-3 top-1/2 -translate-y-1/2 z-20 pointer-events-auto select-none max-h-[60vh]"
      title="时间微缩轴（悬停预览，点击跳转）"
    >
      <TimelineScrubber
        :sessions="props.conversationRounds"
        :active-session-id="activeSessionIdInScrubber"
        @jump="scrollToSession"
      />
    </div>

    <!-- Floating New Message Notification Pill (当用户在查看历史且收到新轮次时，在底部居中弹出，高度适度上浮避免压住悬浮快捷按钮) -->
    <Transition
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="opacity-0 translate-y-3 scale-95"
      enter-to-class="opacity-100 translate-y-0 scale-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="opacity-100 translate-y-0 scale-100"
      leave-to-class="opacity-0 translate-y-3 scale-95"
    >
      <div
        v-if="unreadNewMessagesCount > 0"
        class="absolute bottom-12 sm:bottom-14 left-1/2 -translate-x-1/2 z-40 select-none pointer-events-auto"
      >
        <button
          type="button"
          class="flex items-center gap-2 px-4 py-2 rounded-full bg-primary text-primary-foreground shadow-float hover:shadow-xl hover:opacity-95 font-medium text-xs font-mono cursor-pointer transition-all border border-primary-foreground/20 animate-bounce"
          @click="handleJumpToLatest"
          title="点击直达最新消息"
        >
          <Sparkles class="w-3.5 h-3.5" />
          <span>
            {{ unreadNewMessagesCount === 1 ? '收到 AI 最新方案汇报' : `收到 ${unreadNewMessagesCount} 条新动态` }} · 点击直达最新 ↓
          </span>
        </button>
      </div>
    </Transition>
  </div>
</template>
