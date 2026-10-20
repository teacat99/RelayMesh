<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import dayjs from 'dayjs'
import type { FeedbackSession, QueuedFeedback, SessionImage } from '../../api/types'
import { useSessionStore } from '../../stores/session'
import { useSettingsStore } from '../../stores/settings'
import { usePreviewStore } from '../../stores/preview'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import LinkedText from '../work/LinkedText.vue'
import TimelineScrubber from '../TimelineScrubber.vue'
import Badge from '../ui/badge/Badge.vue'
import {
  Check,
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
  CheckCheck,
  Loader2,
  Trash2,
  GitCompareArrows
} from 'lucide-vue-next'
import AnchorChip from './AnchorChip.vue'
import CapsuleMarker from './CapsuleMarker.vue'
import SessionTasks from './SessionTasks.vue'
import { useWorkflowCheckpoints } from '../../composables/useWorkflowCheckpoints'
import {
  computeDividers,
  gitLogRangeCommand,
  shortHash
} from '../../lib/anchor'
import { useImageLoader, formatBytes } from '../../composables/useImageLoader'
import { inboxStatus, type InboxStatusView } from '../../lib/inbox'
import { useConfirmStore } from '../../stores/confirms'
import { buildTaskUpdateCards } from '../../lib/asyncMode'
import {
  TIMELINE_TAIL_KEY,
  confirmEntries,
  groupBlocks,
  guardEntries,
  placeTimeline,
  inRoundKey,
  stackKeysHoldingConfirm,
  type TimelineBlock,
  type TimelineEntry,
} from '../../lib/timeline'
import { useWorkItemStore } from '../../stores/workItems'
import CardStack from './CardStack.vue'
import TimelineCardEntry from './TimelineCardEntry.vue'
import ReplyRefButton from './ReplyRefButton.vue'
import { replyRefToken } from '../../lib/replyRef'
import { formatClock, receivedTitle, replySentAt } from '../../lib/sessionTimer'
import { createReusableTemplate } from '@vueuse/core'
import { toast, toastDestructive } from '@/components/ui/sonner'

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
  /** 真实的轮次（不含异步模式下的进度通知）；轮次编号、微缩轴都只认它。 */
  conversationRounds: FeedbackSession[]
  /** 异步模式下智能体发的进度通知（不阻塞、没有回复框），按时间穿插在轮次之间。 */
  notices?: FeedbackSession[]
  hasDraftImages?: boolean
}>()

const emit = defineEmits<{
  (e: 'scroll-state-change', isScrolledUp: boolean): void
  (e: 'revoke-session', sessionId: string): void
  (e: 'revoke-queued', queuedId: number): void
  (e: 'delete-session', sessionId: string): void
}>()

const sessionStore = useSessionStore()
const previewStore = usePreviewStore()
const imageLoader = useImageLoader()

const confirmDeleteSessionId = ref<string | null>(null)
let confirmDeleteTimer: number | null = null
let deletedSessionSnapshot: FeedbackSession | null = null

async function handleDeleteSession(session: FeedbackSession) {
  const sessionId = session.session_id
  // 第一击：进入激活确认状态（3.5 秒倒计时自动还原）
  if (confirmDeleteSessionId.value !== sessionId) {
    confirmDeleteSessionId.value = sessionId
    if (confirmDeleteTimer) window.clearTimeout(confirmDeleteTimer)
    confirmDeleteTimer = window.setTimeout(() => {
      if (confirmDeleteSessionId.value === sessionId) {
        confirmDeleteSessionId.value = null
      }
    }, 3500)
    return
  }

  // 第二击：确认执行删除
  if (confirmDeleteTimer) {
    window.clearTimeout(confirmDeleteTimer)
    confirmDeleteTimer = null
  }
  confirmDeleteSessionId.value = null

  // 保存可逆快照备份（Demand 5: 高风险操作通知提供撤销操作，保留 7000ms）
  deletedSessionSnapshot = { ...session }
  const turnNum = getSessionTurnNumber(session)

  try {
    await sessionStore.deleteSession(sessionId)
    toastDestructive(`已删除第 ${turnNum} 轮会话`, () => restoreDeletedSession())
  } catch (e: any) {
    toast.error('删除会话失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

async function restoreDeletedSession() {
  if (!deletedSessionSnapshot) return
  const snapshot = deletedSessionSnapshot
  deletedSessionSnapshot = null
  try {
    await sessionStore.restoreSession(snapshot)
    toast.success('已成功恢复该轮会话')
  } catch (e: any) {
    toast.error('恢复会话失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

function openImagePreviewModal(img: SessionImage, alt: string) {
  const url = getImageUrl(img)
  imageLoader.prioritize(url)
  previewStore.openImagePreview({
    src: url,
    alt: alt,
    width: img.width,
    height: img.height
  })
}

// 自动将当前会话流中的图片按从上到下、组内从左到右的阅读顺序加入串行下载队列
watch(() => props.conversationRounds, (rounds) => {
  if (!rounds || rounds.length === 0) return
  for (const r of rounds) {
    if (r.images && r.images.length > 0) {
      for (const img of r.images) {
        const url = getImageUrl(img)
        if (url && !url.startsWith('data:')) {
          imageLoader.enqueue(url)
        }
      }
    }
  }
}, { immediate: true, deep: true })

// 追加信息里已确认 / 已过期的条目服务端不再内联 base64，只给按需取图的直链：
// 和会话图片一样加入串行下载队列（带鉴权取图），否则 <img> 会不带令牌直接请求直链而 401，缩略图就「消失」了。
watch(() => sessionStore.queuedFeedbacks, (items) => {
  for (const q of items ?? []) {
    for (const img of q.images ?? []) {
      const url = getImageUrl(img)
      if (url && !url.startsWith('data:')) {
        imageLoader.enqueue(url)
      }
    }
  }
}, { immediate: true, deep: true })

const loadedImageKeys = ref<Set<string>>(new Set())

function getImageKey(img: SessionImage | null | undefined, index: number, prefix: string): string {
  if (!img) return `${prefix}_${index}`
  if (img.hash) return `${prefix}_${img.hash}`
  if (img.url) return `${prefix}_${img.url}`
  return `${prefix}_${index}`
}

function isImageLoaded(key: string): boolean {
  return loadedImageKeys.value.has(key)
}

function handleImageLoad(key: string) {
  loadedImageKeys.value.add(key)
}

function getImageStyle(img: SessionImage | null | undefined) {
  if (img && img.width && img.height && img.width > 0 && img.height > 0) {
    return {
      aspectRatio: `${img.width} / ${img.height}`
    }
  }
  return {
    aspectRatio: '16 / 10'
  }
}

function getImageUrl(img: SessionImage | null | undefined): string {
  if (!img) return ''
  if (img.url) return img.url
  if (img.data) {
    if (img.data.startsWith('data:') || img.data.startsWith('http://') || img.data.startsWith('https://')) {
      return img.data
    }
    const format = img.format || 'png'
    return `data:image/${format};base64,${img.data}`
  }
  return ''
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
const bottomAnchorRef = ref<HTMLElement | null>(null)
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

// ---- 交付锚点与恢复胶囊（只读标记，缺省时界面与以前一致）----
// 相邻两轮提交哈希不同时，在两轮之间出现「期间有新提交」分隔条（键为后一轮的 session_id）。
const anchorDividers = computed(() => computeDividers(props.conversationRounds))

// 恢复胶囊按创建时间落在对应两次交付之间；时间线上只做只读展示。
const capsuleWorkflowId = computed(() => {
  const rounds = props.conversationRounds
  return rounds.length > 0 ? rounds[rounds.length - 1].workflow_id : undefined
})
const { checkpoints: workflowCapsules } = useWorkflowCheckpoints(capsuleWorkflowId)
const expandedCapsules = ref<Set<number>>(new Set())

function toggleCapsule(revision: number) {
  const next = new Set(expandedCapsules.value)
  if (next.has(revision)) next.delete(revision)
  else next.add(revision)
  expandedCapsules.value = next
}

function turnNumberOfSession(sessionId?: string): number | null {
  if (!sessionId) return null
  const idx = props.conversationRounds.findIndex(s => s.session_id === sessionId)
  return idx === -1 ? null : idx + 1
}

function copyCommand(command: string, label: string) {
  if (command) void copyText(command, label)
}

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

// ---- 追加信息（无交互期间您追加的指令）----
// 它们是对话的一部分，不再是对话底下一条独立的状态带：按创建时间落在对应两轮之间，
// 格式与「您的确认回复」一致（标题行右侧是 hook 提取状态，下面是内容）。
// Agent 最近活动、提醒方式、hook 状态与安装命令搬到了工具栏的「收件箱状态块」（InboxStatusChip），
// 重新拉取也由它负责；这里只需要一个低频时钟来推动「3 分钟前确认」这类相对时间。
const inboxNow = ref(Date.now())
let inboxClock: number | undefined

// 追加信息的气泡在「轮次之间」和「末尾」两处渲染，用可复用模板只写一份
const [DefineInboxBubble, ReuseInboxBubble] = createReusableTemplate<{ q: QueuedFeedback }>()

// ---- 轮次之外的所有条目：合并成一条按提交时间排序的流（#52 / #105）----
// 您的追加、智能体的进度通知、任务进展卡、确认 / 授权卡、文档决策汇总卡、守护告警、恢复胶囊，
// 以前是各自独立的几条流，每条流各自落位、同一个间隙里再一类一类渲染，所以会错序。
// 现在先整体按提交时间排好，再落在「第一个比它晚创建的轮次」之前，比所有轮次都新的落在末尾；
// 用全部轮次计算而不是当前窗口，分页展开更早的轮次时位置不会漂移。
const confirmStore = useConfirmStore()
// 任务进展卡只在这个工作流开启过异步模式时出现（同步工作流的时间线保持原样）。
const workStore = useWorkItemStore()
const timelineEntries = computed<TimelineEntry[]>(() => {
  const entries: TimelineEntry[] = []
  for (const q of sessionStore.queuedFeedbacks ?? []) {
    entries.push({ kind: 'inbox', key: `inbox-${q.id}`, at: q.created_at, q })
  }
  for (const n of props.notices ?? []) {
    entries.push({ kind: 'notice', key: `notice-${n.session_id}`, at: n.created_at, session: n })
  }
  entries.push(...confirmEntries(confirmStore.timelineItems))
  entries.push(...guardEntries(confirmStore.board.guard_alerts))
  for (const c of workflowCapsules.value) {
    entries.push({ kind: 'capsule', key: `capsule-${c.revision}`, at: c.created_at, capsule: c })
  }
  const mode = sessionStore.asyncMode
  if (mode?.ever_enabled && workStore.feed.loaded && workStore.feed.workflowId === mode.workflow_id) {
    for (const card of buildTaskUpdateCards(workStore.feed.updates, mode.enabled_at)) {
      entries.push({ kind: 'task', key: card.key, at: card.at, card })
    }
  }
  return entries
})
// 每一轮带上「你的回复」的时刻：夹在提问与回复之间的条目放进卡里（见 placeTimeline）
const timelinePlacement = computed(() =>
  placeTimeline(
    props.conversationRounds.map(s => ({
      session_id: s.session_id,
      created_at: s.created_at,
      reply_at: s.status === 'completed' ? replySentAt(s) : undefined,
    })),
    timelineEntries.value,
  ),
)
// 间隙里连续的卡片（没有「交流」隔开）叠成一摞；还没答复的提问永远摊开
const timelineBlocks = computed(() => {
  const out: Record<string, TimelineBlock[]> = {}
  for (const [key, list] of Object.entries(timelinePlacement.value)) out[key] = groupBlocks(list)
  return out
})
const tailBlocks = computed(() => timelineBlocks.value[TIMELINE_TAIL_KEY] || [])
// 间隙里的条目渲染（轮次之间与末尾两处共用，只写一份）
const [DefineGap, ReuseGap] = createReusableTemplate<{ blocks: TimelineBlock[] }>()

// 工具栏「待答复」徽标点一下、或点正文里的 Q23 引用，会把要看的编号放进 confirmStore.focusRef：那张卡可能落在还没展开的更早轮次里，
// 先把窗口撑到它所在的间隙；已处理的提问收在默认收起的摞里（卡没有挂载，也就读不到 focusRef），所以也要先把那一摞展开；
// 卡挂载时自己读取 focusRef 完成滚动与高亮（见 ConfirmTimelineCard）。
watch(() => confirmStore.focusRef, ref => {
  if (!ref) return
  const closed = stackKeysHoldingConfirm(timelineBlocks.value, ref).filter(key => !expandedStacks.value.has(key))
  if (closed.length) expandedStacks.value = new Set([...expandedStacks.value, ...closed])
  const placement = timelinePlacement.value
  const gapKey = Object.keys(placement).find(
    key => key !== TIMELINE_TAIL_KEY && placement[key].some(e => e.kind === 'confirm' && e.item.ref === ref),
  )
  if (!gapKey) return
  const total = props.conversationRounds.length
  const idx = props.conversationRounds.findIndex(s => s.session_id === gapKey)
  if (idx === -1) return
  const needed = total - idx
  if (needed > visibleRoundsCount.value) visibleRoundsCount.value = Math.min(total, needed + 2)
})

// 摞默认收起；展开状态按摞的键记（键取第一张卡的键，末尾追加新卡不会换键）
const expandedStacks = ref<Set<string>>(new Set())
function toggleStack(key: string) {
  const next = new Set(expandedStacks.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedStacks.value = next
}

// 出现新的追加、或状态变化撑高了气泡时：用户本来就停在底部就跟着滚到底部（否则最新一条会被输入区盖住），
// 正在向上翻历史则不打扰。
watch(() => sessionStore.queuedFeedbacks, () => {
  if (!isScrolledUp.value) nextTick(() => scrollToBottom(false))
})
// 新的进度通知 / 卡片同理
watch(() => timelineEntries.value.map(e => e.key).join('|'), () => {
  if (!isScrolledUp.value) nextTick(() => scrollToBottom(false))
})
function statusOf(q: QueuedFeedback): InboxStatusView {
  return inboxStatus(q, inboxNow.value)
}
/** 标题行右侧的状态徽标配色：与「您的确认回复」气泡的「待 AI 提取 / AI 已接收」同一套。 */
function inboxBadgeClass(tone: InboxStatusView['tone']): string {
  if (tone === 'pending') return 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30'
  if (tone === 'read') return 'bg-sky-500/15 text-sky-600 dark:text-sky-400 border border-sky-500/30'
  return 'text-muted-foreground/80'
}

function tickInboxClock() {
  inboxNow.value = Date.now()
}

onMounted(() => {
  setupTopObserver()
  nextTick(() => {
    updateActiveSessionFromScroll()
  })
  inboxClock = window.setInterval(tickInboxClock, 10_000)
})

onBeforeUnmount(() => {
  if (topObserver) {
    topObserver.disconnect()
    topObserver = null
  }
  if (inboxClock !== undefined) {
    window.clearInterval(inboxClock)
    inboxClock = undefined
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

  const attemptScroll = () => {
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
  }

  nextTick(() => {
    attemptScroll()
    requestAnimationFrame(() => {
      attemptScroll()
    })
  })
}

function scrollToBottom(smooth = true) {
  isScrolledUp.value = false
  unreadNewMessagesCount.value = 0
  emit('scroll-state-change', false)
  if (props.conversationRounds.length > 0) {
    activeSessionIdInScrubber.value = props.conversationRounds[props.conversationRounds.length - 1].session_id
  }

  const doScroll = () => {
    if (bottomAnchorRef.value) {
      bottomAnchorRef.value.scrollIntoView({ behavior: smooth ? 'smooth' : 'auto', block: 'end' })
    }
    if (chatScrollContainer.value) {
      chatScrollContainer.value.scrollTop = chatScrollContainer.value.scrollHeight
    }
  }

  doScroll()

  if (smooth) {
    setTimeout(() => {
      doScroll()
      updateActiveSessionFromScroll()
    }, 250)
  } else {
    // 首次加载/刷新/切换会话：双帧 rAF 校准，抵抗异步 DOM 展开导致的高度时滞 (D-142)
    requestAnimationFrame(() => {
      doScroll()
      requestAnimationFrame(() => {
        doScroll()
        updateActiveSessionFromScroll()
      })
    })
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
    <!--
      追加信息气泡（D-212）：无交互期间您追加的指令，按创建时间落在对话里，格式与「您的确认回复」一致——
      左边是「您的追加信息 + 时间 + 状态徽标」，右边是 hook 提取状态（经哪条路径读到、什么时候）与撤回按钮。
      在「轮次之间」与「末尾」两处用 <ReuseInboxBubble> 渲染，这里只定义一次。
    -->
    <DefineInboxBubble v-slot="{ q }">
      <div
        class="space-y-2 pl-2 sm:pl-8"
        :data-testid="`inbox-item-${q.id}`"
        :data-state="statusOf(q).stage"
      >
        <!-- 窄屏（360px）上「您的追加信息 · 时间 · AI 已读取」加上「撤回并通知 Agent」放不下一行，旧写法里撤回按钮会压在徽标上（生产只读核对发现）：
             手机宽度允许换行，右侧那组落到下一行靠右；sm 以上仍是单行，右侧的 hook 状态文字按原样截断。 -->
        <div class="flex flex-wrap sm:flex-nowrap items-center justify-between gap-x-2 gap-y-1">
          <div class="flex items-center gap-1.5 sm:gap-2 text-xs font-medium text-muted-foreground min-w-0">
            <User class="w-3.5 h-3.5 shrink-0 text-[color:var(--color-status-active)]" />
            <span class="text-foreground font-medium text-xs shrink-0">您的追加信息</span>
            <span class="text-[10px] sm:text-[11px] font-mono text-muted-foreground shrink-0">
              {{ formatDateTimeCustom(q.created_at) }}
            </span>
            <Badge
              :variant="statusOf(q).tone === 'pending' || statusOf(q).tone === 'read' ? 'secondary' : 'outline'"
              class="text-[9px] sm:text-[10px] px-1.5 py-0 font-normal rounded-xs flex items-center gap-1 shrink-0"
              :class="inboxBadgeClass(statusOf(q).tone)"
              :title="statusOf(q).label"
              :data-testid="`inbox-label-${q.id}`"
              :data-stage="statusOf(q).stage"
            >
              <Clock v-if="statusOf(q).stage === 'queued'" class="w-2.5 h-2.5 animate-pulse" />
              <CheckCheck v-else-if="statusOf(q).stage === 'delivered'" class="w-2.5 h-2.5" />
              <CheckCheck v-else-if="statusOf(q).stage === 'acked'" class="w-2.5 h-2.5 text-emerald-500" />
              <Clock v-else class="w-2.5 h-2.5" />
              <span>{{ statusOf(q).badge }}</span>
            </Badge>
          </div>

          <!-- 右侧：hook 提取状态 + 撤回 -->
          <div class="flex items-center justify-end gap-1.5 sm:gap-2 min-w-0 ml-auto">
            <span
              class="hidden sm:inline truncate text-[10px] sm:text-[11px] font-mono text-muted-foreground"
              :title="statusOf(q).detail"
              :data-testid="`inbox-detail-${q.id}`"
            >{{ statusOf(q).detail }}</span>
            <button
              v-if="statusOf(q).canRetract"
              type="button"
              class="inline-flex items-center gap-1 text-[11px] font-mono text-muted-foreground hover:text-destructive hover:bg-destructive/10 px-2 py-0.5 rounded cursor-pointer transition-colors border border-border/80 hover:border-destructive/40 group select-none shrink-0"
              :title="statusOf(q).retractTitle"
              :data-testid="`inbox-retract-${q.id}`"
              @click="emit('revoke-queued', q.id)"
            >
              <Undo2 class="w-3 h-3 group-hover:-translate-x-0.5 transition-transform" />
              <span>{{ statusOf(q).retractLabel }}</span>
            </button>
          </div>
        </div>

        <div class="p-3 sm:p-4 rounded-md border border-border/60 bg-muted/30 space-y-2">
          <div v-if="q.user_messages && q.user_messages.length > 0" class="flex flex-wrap gap-1">
            <span
              v-for="msg in q.user_messages"
              :key="msg"
              class="text-[10px] sm:text-[11px] px-1.5 sm:px-2 py-0.5 rounded-xs bg-muted border border-border text-foreground font-mono"
            >
              {{ msg }}
            </span>
          </div>

          <LinkedText
            class="text-xs sm:text-sm text-foreground whitespace-pre-wrap [overflow-wrap:anywhere] leading-relaxed"
            :text="q.response_text || '（无文字内容）'"
            :workflow-id="q.workflow_id"
          />

          <!--
            Attached Screenshots (支持点击放大预览、阅读顺序流式进度条与固定宽高比骨架防抖)。
            所有状态都显示缩略图：已确认 / 已过期的条目服务端不再内联 base64，改给按需取图的直链（img.url），
            由 imageLoader 带鉴权按需加载；用户回看历史追加时需要核对当时附的截图，不能因为智能体读过就消失。
          -->
          <div v-if="q.images && q.images.length > 0" class="pt-2 flex flex-wrap gap-2 max-w-full overflow-hidden">
            <div
              v-for="(img, imgIdx) in q.images"
              :key="img.hash || imgIdx"
              class="relative group rounded-xs overflow-hidden border border-border/80 bg-muted/40 shadow-2xs cursor-pointer hover:border-primary/60 transition-all shrink-0 h-24 sm:h-36 max-w-full"
              :style="getImageStyle(img)"
              @click.stop="openImagePreviewModal(img, img.name || `暂存附件-${imgIdx + 1}`)"
            >
              <!-- 骨架占位防抖与实时流式下载进度条 (CLS = 0) -->
              <div
                v-if="!isImageLoaded(getImageKey(img, imgIdx, `inbox-${q.id}`))"
                class="absolute inset-0 flex flex-col items-center justify-center bg-muted/70 select-none p-2 z-5"
              >
                <!-- 正在流式下载中：展示百分比与已传大小 -->
                <div
                  v-if="imageLoader.getTask(getImageUrl(img))?.status === 'loading'"
                  class="flex flex-col items-center gap-1.5 w-full max-w-[130px]"
                >
                  <div class="flex items-center justify-between w-full text-[10px] font-mono text-muted-foreground font-semibold">
                    <span class="text-primary flex items-center gap-1">
                      <Loader2 class="w-3 h-3 animate-spin" />
                      {{ imageLoader.getTask(getImageUrl(img))?.percent }}%
                    </span>
                    <span>{{ formatBytes(imageLoader.getTask(getImageUrl(img))?.receivedBytes || 0) }}</span>
                  </div>
                  <div class="w-full h-1.5 rounded-full bg-muted/90 overflow-hidden border border-border/60">
                    <div
                      class="h-full bg-primary transition-all duration-150 rounded-full"
                      :style="{ width: `${imageLoader.getTask(getImageUrl(img))?.percent || 0}%` }"
                    ></div>
                  </div>
                </div>

                <!-- 下载失败重试 -->
                <div
                  v-else-if="imageLoader.getTask(getImageUrl(img))?.status === 'error'"
                  class="flex flex-col items-center gap-1 text-[10px] font-mono text-destructive cursor-pointer hover:underline"
                  title="点击重试下载"
                  @click.stop="imageLoader.retry(getImageUrl(img))"
                >
                  <AlertCircle class="w-3.5 h-3.5" />
                  <span>重试</span>
                </div>

                <!-- 队列排队等待中 -->
                <div v-else class="px-2 py-1 rounded-xs border border-muted-foreground/20 flex items-center gap-1 text-muted-foreground/60 animate-pulse">
                  <span class="text-[9px] font-mono">排队加载</span>
                </div>
              </div>

              <!-- 实际图片：懒加载 + 异步解码 + 就绪平滑淡入 -->
              <img
                :src="imageLoader.getDisplaySrc(getImageUrl(img))"
                loading="lazy"
                decoding="async"
                class="w-full h-full object-cover pointer-events-none block transition-opacity duration-300"
                :class="isImageLoaded(getImageKey(img, imgIdx, `inbox-${q.id}`)) ? 'opacity-100' : 'opacity-0'"
                :alt="img.name || `queued-attachment-${imgIdx + 1}`"
                @load="handleImageLoad(getImageKey(img, imgIdx, `inbox-${q.id}`))"
              />
              <!-- 底部一体化分区标题栏：左侧嵌入编号，右侧嵌入文件名 -->
              <div class="absolute bottom-0 inset-x-0 bg-black/80 backdrop-blur-xs text-white text-[9px] font-mono flex items-stretch h-4.5 select-none pointer-events-none overflow-hidden z-10">
                <div class="px-1.5 bg-primary text-primary-foreground font-bold flex items-center justify-center shrink-0 border-r border-white/20">
                  #{{ imgIdx + 1 }}
                </div>
                <div class="flex-1 truncate px-1.5 text-center self-center text-white/90">
                  {{ img.name || `img-${imgIdx + 1}` }}
                </div>
              </div>
            </div>
          </div>

          <!-- 窄屏标题行放不下 hook 提取状态：挪到气泡底部 -->
          <p
            class="sm:hidden text-[10px] font-mono text-muted-foreground leading-relaxed"
            :data-testid="`inbox-detail-m-${q.id}`"
          >{{ statusOf(q).detail }}</p>
        </div>
      </div>
    </DefineInboxBubble>

    <!--
      一个间隙里的条目：按 groupBlocks 分好的块依次渲染。连续的可叠卡片成一摞（默认收起，点开看全部），
      其余各自单独一块——您的话、智能体的话、还没答复的提问 / 授权、守护告警、恢复胶囊永远摊开。
      在「轮次之间」与「末尾」两处用 <ReuseGap> 渲染，这里只定义一次。
    -->
    <DefineGap v-slot="{ blocks }">
      <!-- 每一块外包一层只带标记的 div（key / 种类 / 提交时间）：e2e 与排障据此核对「整条流是不是按提交时间排的」；摞取第一张卡的时间 -->
      <div
        v-for="block in blocks"
        :key="block.key"
        :data-timeline-key="block.key"
        :data-timeline-kind="block.type === 'stack' ? 'stack' : block.entry.kind"
        :data-timeline-at="block.type === 'stack' ? block.entries[0].at : block.entry.at"
      >
        <CardStack
          v-if="block.type === 'stack'"
          :entries="block.entries"
          :expanded="expandedStacks.has(block.key)"
          @toggle="toggleStack(block.key)"
        >
          <template #card="{ entry }">
            <TimelineCardEntry :entry="entry" />
          </template>
        </CardStack>
        <ReuseInboxBubble v-else-if="block.entry.kind === 'inbox'" :q="block.entry.q" />
        <CapsuleMarker
          v-else-if="block.entry.kind === 'capsule'"
          :capsule="block.entry.capsule"
          :expanded="expandedCapsules.has(block.entry.capsule.revision)"
          :time-text="formatDateTimeCustom(block.entry.capsule.created_at)"
          :anchor-turn="turnNumberOfSession(block.entry.capsule.basis?.anchor_session_id)"
          @toggle="toggleCapsule(block.entry.capsule.revision)"
          @jump="scrollToSession(block.entry.capsule.basis?.anchor_session_id || '')"
          @copy="copyText"
        />
        <TimelineCardEntry v-else :entry="block.entry" />
      </div>
    </DefineGap>

    <!-- Scrollable Conversation Feed -->
    <div
      ref="chatScrollContainer"
      class="h-full overflow-y-auto max-md:overflow-x-hidden px-3 sm:px-8 py-4 sm:py-6 space-y-6 sm:space-y-8 transition-[padding] duration-150 no-scrollbar scrollbar-none"
      :class="props.hasDraftImages ? 'pb-28 sm:pb-32' : 'pb-14 sm:pb-16'"
      @scroll="handleScroll"
    >
      <!-- Empty State -->
      <div
        v-if="props.conversationRounds.length === 0 && timelineEntries.length === 0"
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

        <!--
          轮次组：本轮卡片 + 落在它之前的恢复胶囊 / 「期间有新提交」分隔条。
          组内间距由 space-y-3 统一给出，组与组之间由外层 space-y-8/10 给出。
          不要再用负外边距去「拉近」它们：Tailwind v4 的 space-y 写的是 margin-block-end，
          负的 mb 会把它覆盖掉，结果就是胶囊压在下一轮的标题上。
        -->
        <div
          v-for="session in displayedConversationRounds"
          :key="session.session_id"
          class="space-y-3"
          data-testid="turn-group"
        >
        <!--
          轮次之间的所有条目（您的追加 / 智能体的进度通知 / 任务进展卡 / 确认与授权卡 / 文档决策汇总 / 守护告警 / 恢复胶囊）：
          合并成一条按提交时间排序的流再渲染；连续的卡片叠成一摞（#52 / #105）。
        -->
        <ReuseGap :blocks="timelineBlocks[session.session_id] || []" />
        <!-- 期间有新提交：相邻两轮都带哈希且不同才出现 -->
        <div
          v-if="anchorDividers[session.session_id]"
          class="flex items-center gap-2 select-none"
          data-testid="anchor-divider"
        >
          <div class="h-px flex-1 bg-border/60"></div>
          <button
            type="button"
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-border/70 bg-muted/40 hover:bg-muted text-[10px] sm:text-[11px] font-mono text-muted-foreground hover:text-foreground cursor-pointer transition-colors"
            :title="`点击复制 ${gitLogRangeCommand(anchorDividers[session.session_id].from, anchorDividers[session.session_id].to)}`"
            @click="copyCommand(gitLogRangeCommand(anchorDividers[session.session_id].from, anchorDividers[session.session_id].to), 'git log 命令')"
          >
            <GitCompareArrows class="w-3 h-3 shrink-0" />
            <span>期间有新提交</span>
            <span class="text-foreground/80">{{ shortHash(anchorDividers[session.session_id].from) }} → {{ shortHash(anchorDividers[session.session_id].to) }}</span>
          </button>
          <div class="h-px flex-1 bg-border/60"></div>
        </div>
        <div
          :id="`session-${session.session_id}`"
          class="space-y-3 sm:space-y-4 pt-2 transition-all p-2 rounded-md scroll-mt-6"
          :class="highlightedSessionId === session.session_id ? 'ring-2 ring-primary bg-primary/5' : ''"
        >
          <!-- Turn Divider & Timestamp Header -->
          <div class="flex items-center gap-2 sm:gap-3 select-none">
            <div class="h-[1px] flex-1 bg-border/60"></div>
            <div class="flex min-w-0 flex-wrap items-center justify-center gap-x-1.5 gap-y-1 sm:gap-x-2 text-[10px] sm:text-[11px] font-mono text-muted-foreground">
              <button
                type="button"
                class="bg-muted hover:bg-accent hover:text-accent-foreground px-1.5 py-0.5 rounded text-[10px] font-mono text-muted-foreground inline-flex shrink-0 items-center gap-1 whitespace-nowrap cursor-pointer transition-colors group"
                :title="`点击复制会话 ID: ${session.session_id}`"
                @click="copyText(session.session_id, '会话 ID')"
              >
                <Copy class="w-2.5 h-2.5 text-muted-foreground group-hover:text-primary transition-colors" />
                <!-- 手机宽度下只留复制图标：编号就是引用标记里的哈希，点「引用」后输入框里看得到 -->
                <span class="hidden sm:inline">{{ session.session_id }}</span>
              </button>
              <!-- 引用这一轮：在输入框插入「【回复 哈希】」（#106） -->
              <ReplyRefButton :token="replyRefToken(session.session_id)" />
              <span>·</span>
              <span class="truncate max-w-[100px] sm:max-w-none">{{ session.title || '方案汇报' }}</span>
              <span>·</span>
              <span class="whitespace-nowrap">{{ formatDateTimeCustom(session.created_at) }}</span>
              <Badge :variant="getStatusBadge(session.status).variant as any" class="text-[9px] sm:text-[10px] px-1 sm:px-1.5 py-0 font-normal rounded-xs whitespace-nowrap shrink-0">
                {{ getStatusBadge(session.status).label }}
              </Badge>
            </div>
            <div class="h-[1px] flex-1 bg-border/60"></div>
          </div>

          <!-- 交付锚点：模型随本次交付申报的仓库最新提交（没有锚点的轮次不出现任何新元素） -->
          <AnchorChip
            v-if="session.git_commit"
            :commit="session.git_commit"
            :subject="session.git_subject"
            @copy="copyText"
          />

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
              <div class="flex items-center gap-1.5 sm:gap-2">
                <div
                  v-if="session.project_directory"
                  class="text-[10px] sm:text-[11px] font-mono text-muted-foreground hidden sm:flex items-center gap-1 hover:text-foreground hover:bg-muted/70 px-1.5 py-0.5 rounded cursor-pointer transition-colors max-w-xs md:max-w-md lg:max-w-lg truncate group"
                  :title="`点击复制工作区路径: ${formatSessionProjectDirectory(session)}`"
                  @click="copyText(formatSessionProjectDirectory(session), '工作区路径')"
                >
                  <FolderGit2 class="w-3 h-3 shrink-0 group-hover:text-primary transition-colors" />
                  <span class="truncate">{{ formatSessionProjectDirectory(session) }}</span>
                </div>
                <!-- 删除此轮按钮 (带两段式确认与快照撤销 D-159) -->
                <button
                  type="button"
                  class="inline-flex items-center gap-1 text-[10px] sm:text-[11px] font-mono px-1.5 py-0.5 rounded cursor-pointer transition-all select-none border"
                  :class="confirmDeleteSessionId === session.session_id 
                    ? 'bg-destructive text-destructive-foreground border-destructive shadow-xs' 
                    : 'text-muted-foreground/80 hover:text-destructive hover:bg-destructive/10 border-border/60 hover:border-destructive/40'"
                  :title="confirmDeleteSessionId === session.session_id ? '再次点击确认删除（3.5秒内有效）' : '删除本轮会话（清理模型发错的脏数据）'"
                  @click.stop="handleDeleteSession(session)"
                >
                  <Trash2 class="w-3 h-3" />
                  <span>{{ confirmDeleteSessionId === session.session_id ? '确定删除？' : '删除此轮' }}</span>
                </button>
              </div>
            </div>

            <div class="p-3.5 sm:p-5 md:p-6 rounded-md border border-border/70 bg-card shadow-2xs leading-relaxed text-xs sm:text-sm">
              <MarkdownRenderer :content="session.summary" :workflow-id="session.workflow_id" />
            </div>
            <!-- 本轮任务：这一轮创建 / 推进过的任务，可展开；这一轮没碰过任务时不出现 -->
            <SessionTasks :session-id="session.session_id" />
          </div>

          <!-- 提问与回复之间发生的追加 / 通知 / 卡片：按时间夹在中间，不再统一排到整张卡之后（#118） -->
          <div v-if="(timelineBlocks[inRoundKey(session.session_id)] || []).length" class="space-y-3" data-testid="in-round-gap">
            <ReuseGap :blocks="timelineBlocks[inRoundKey(session.session_id)] || []" />
          </div>

          <!-- User Message Turn (Feedback / Response) -->
          <div v-if="session.status === 'completed'" class="space-y-2 pl-2 sm:pl-8">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-1.5 sm:gap-2 text-xs font-medium text-muted-foreground">
                <User class="w-3.5 h-3.5 text-[color:var(--color-status-active)]" />
                <span class="text-foreground font-medium text-xs">您的确认回复</span>
                <!-- 这里是你提交回复的时刻（不是智能体取走它的时刻）；悬停看精确到秒的时间 -->
                <span
                  class="text-[10px] sm:text-[11px] font-mono text-muted-foreground"
                  :title="`你提交回复的时间 ${formatClock(replySentAt(session))}`"
                >
                  {{ formatDateTimeCustom(replySentAt(session)) }}
                </span>
                <!-- AI 消费状态指示 -->
                <Badge
                  v-if="!session.consumed_by_ai"
                  variant="secondary"
                  title="回复已提交，智能体还没来取"
                  class="text-[9px] sm:text-[10px] px-1.5 py-0 font-normal rounded-xs bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30 flex items-center gap-1"
                >
                  <Clock class="w-2.5 h-2.5 animate-pulse" />
                  <span>待 AI 提取</span>
                </Badge>
                <Badge
                  v-else
                  variant="outline"
                  :title="receivedTitle(session)"
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
              <LinkedText
                class="text-xs sm:text-sm text-foreground whitespace-pre-wrap [overflow-wrap:anywhere] leading-relaxed"
                :text="session.response_text || '（已确认，未附加文字）'"
                :workflow-id="session.workflow_id"
              />

              <!-- Attached Screenshots (支持点击放大预览、阅读顺序流式进度条与固定宽高比骨架防抖) -->
              <div v-if="session.images && session.images.length > 0" class="pt-2 flex flex-wrap gap-2 max-w-full overflow-hidden">
                <div
                  v-for="(img, imgIdx) in session.images"
                  :key="img.hash || imgIdx"
                  class="relative group rounded-xs overflow-hidden border border-border/80 bg-muted/40 shadow-2xs cursor-pointer hover:border-primary/60 transition-all shrink-0 h-24 sm:h-36 max-w-full"
                  :style="getImageStyle(img)"
                  @click.stop="openImagePreviewModal(img, img.name || `第 ${getSessionTurnNumber(session)} 轮用户附件-${imgIdx + 1}`)"
                >
                  <!-- 骨架占位防抖与实时流式下载进度条 (CLS = 0) -->
                  <div
                    v-if="!isImageLoaded(getImageKey(img, imgIdx, session.session_id))"
                    class="absolute inset-0 flex flex-col items-center justify-center bg-muted/70 select-none p-2 z-5"
                  >
                    <!-- 正在流式下载中：展示百分比与已传大小 -->
                    <div
                      v-if="imageLoader.getTask(getImageUrl(img))?.status === 'loading'"
                      class="flex flex-col items-center gap-1.5 w-full max-w-[130px]"
                    >
                      <div class="flex items-center justify-between w-full text-[10px] font-mono text-muted-foreground font-semibold">
                        <span class="text-primary flex items-center gap-1">
                          <Loader2 class="w-3 h-3 animate-spin" />
                          {{ imageLoader.getTask(getImageUrl(img))?.percent }}%
                        </span>
                        <span>{{ formatBytes(imageLoader.getTask(getImageUrl(img))?.receivedBytes || 0) }}</span>
                      </div>
                      <div class="w-full h-1.5 rounded-full bg-muted/90 overflow-hidden border border-border/60">
                        <div
                          class="h-full bg-primary transition-all duration-150 rounded-full"
                          :style="{ width: `${imageLoader.getTask(getImageUrl(img))?.percent || 0}%` }"
                        ></div>
                      </div>
                    </div>

                    <!-- 下载失败重试 -->
                    <div
                      v-else-if="imageLoader.getTask(getImageUrl(img))?.status === 'error'"
                      class="flex flex-col items-center gap-1 text-[10px] font-mono text-destructive cursor-pointer hover:underline"
                      @click.stop="imageLoader.retry(getImageUrl(img))"
                      title="点击重试下载"
                    >
                      <AlertCircle class="w-3.5 h-3.5" />
                      <span>重试</span>
                    </div>

                    <!-- 队列排队等待中 -->
                    <div v-else class="px-2 py-1 rounded-xs border border-muted-foreground/20 flex items-center gap-1 text-muted-foreground/60 animate-pulse">
                      <span class="text-[9px] font-mono">排队加载</span>
                    </div>
                  </div>

                  <!-- 实际图片：懒加载 + 异步解码 + 就绪平滑淡入 -->
                  <img
                    :src="imageLoader.getDisplaySrc(getImageUrl(img))"
                    loading="lazy"
                    decoding="async"
                    class="w-full h-full object-cover pointer-events-none block transition-opacity duration-300"
                    :class="isImageLoaded(getImageKey(img, imgIdx, session.session_id)) ? 'opacity-100' : 'opacity-0'"
                    :alt="img.name || `attachment-${imgIdx + 1}`"
                    @load="handleImageLoad(getImageKey(img, imgIdx, session.session_id))"
                  />
                  <!-- 底部一体化分区标题栏：左侧嵌入编号，右侧嵌入文件名 -->
                  <div class="absolute bottom-0 inset-x-0 bg-black/80 backdrop-blur-xs text-white text-[9px] font-mono flex items-stretch h-4.5 select-none pointer-events-none overflow-hidden z-10">
                    <div class="px-1.5 bg-primary text-primary-foreground font-bold flex items-center justify-center shrink-0 border-r border-white/20">
                      #{{ imgIdx + 1 }}
                    </div>
                    <div class="flex-1 truncate px-1.5 text-center self-center text-white/90">
                      {{ img.name || `img-${imgIdx + 1}` }}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <!-- Cancelled or Expired Notice -->
          <div v-else-if="session.status === 'cancelled' || session.status === 'timeout'" class="pl-2 sm:pl-8 text-xs text-muted-foreground italic flex items-center gap-1.5 py-1">
            <AlertCircle class="w-3.5 h-3.5" />
            <span>本轮交互已关闭（{{ getStatusBadge(session.status).label }}）</span>
          </div>
        </div>
        </div>

        <!-- 比所有轮次都新的条目落在时间线末尾（同样是一条按提交时间排序的流） -->
        <div
          v-if="tailBlocks.length > 0"
          class="space-y-3"
          data-testid="timeline-tail"
        >
          <ReuseGap :blocks="tailBlocks" />
        </div>

        <!-- 底部物理定位锚点 (D-142: 确保 100% 直达会话流最新最底部，消灭虚拟高度塌陷和高度时滞导致的半途卡顿) -->
        <div ref="bottomAnchorRef" class="h-0 w-full pointer-events-none opacity-0"></div>
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
