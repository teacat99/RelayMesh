<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import dayjs from 'dayjs'
import type { FeedbackSession, QueuedFeedback } from '../../../api/types'
import type { ChatMessage } from '../../../api/conversations'
import { useSessionStore } from '../../../stores/session'
import { useSettingsStore } from '../../../stores/settings'
import PreviewTimelineRail from './PreviewTimelineRail.vue'
import {
  Inbox,
  Sparkles,
  AlertCircle,
  GitCompareArrows
} from 'lucide-vue-next'
import CapsuleMarker from '../CapsuleMarker.vue'
import SessionTasks from '../SessionTasks.vue'
import { useWorkflowCheckpoints } from '../../../composables/useWorkflowCheckpoints'
import {
  computeDividers,
  gitLogRangeCommand,
  shortHash
} from '../../../lib/anchor'
import { useImageLoader } from '../../../composables/useImageLoader'
import { inboxStatus, type InboxStatusView } from '../../../lib/inbox'
import { sessionStatusBadge } from '../../../lib/messageCard'
import { getImageUrl } from '../../../lib/sessionImages'
import { useConfirmStore } from '../../../stores/confirms'
import { confirmRef } from '../../../lib/confirms'
import { usePreviewSearch } from '../../../composables/usePreviewSearch'
import { findEntryBlock, nearestRoundBefore, resolveHit, roundIdOfGap, windowToShow } from '../../../lib/previewSearch'
import { usePreviewUnread } from '../../../composables/usePreviewUnread'
import { offsetInContainer, useScrollSettle } from '../../../composables/useScrollSettle'
import { holdBoundaryKeys } from '../../../lib/scrollSettle'
import { renderedAgentMs, unreadDividerKey, unreadItems } from '../../../lib/previewUnread'
import { buildTaskUpdateCards } from '../../../lib/asyncMode'
import {
  TIMELINE_TAIL_KEY,
  blockHoldsConfirm,
  confirmEntries,
  groupBlocks,
  stackKeysHoldingConfirm,
  guardEntries,
  needsAnswer,
  placeTimeline,
  inRoundKey,
  type TimelineBlock,
  type TimelineEntry,
} from '../../../lib/timeline'
import {
  jumpScrollTop,
  railDomAttrs,
  railMetaOfBlock,
  railMetaOfReply,
  railMetaOfRound,
  type RailItem,
} from '../../../lib/timelineRail'
import { daySeparators, type DayItem } from '../../../lib/daySeparators'
import { liveNoticeKeys, noticeCvClass } from '../../../lib/noticeCv'
import {
  TAIL_FILL_FALLBACK_MS,
  TAIL_FILL_IDLE_TIMEOUT_MS,
  effectiveTailStart,
  initialTailStart,
  nextTailStart,
  revealTailStart,
  shouldFillNow,
  shouldHoldFirst,
} from '../../../lib/tailWindow'
import { useWorkItemStore } from '../../../stores/workItems'
import CardStack from '../CardStack.vue'
import TimelineCardEntry from '../TimelineCardEntry.vue'
import PreviewConfirmCard from './PreviewConfirmCard.vue'
import PreviewDateSeparator from './PreviewDateSeparator.vue'
import PreviewInboxCard from './PreviewInboxCard.vue'
import PreviewNoticeCard from './PreviewNoticeCard.vue'
import PreviewReplyCard from './PreviewReplyCard.vue'
import PreviewReportCard from './PreviewReportCard.vue'
import PreviewSearchPanel from './PreviewSearchPanel.vue'
import PreviewUnreadDivider from './PreviewUnreadDivider.vue'
import { replySentAt } from '../../../lib/sessionTimer'
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

// PreviewMessageList.vue —— 「预览视图」（#120）的会话列表。
//
// 这是 FeedbackMessageList.vue 的复刻：阶段 0 内容与经典逐项一致（对等测试见 lib/previewViewParity.test.ts）；
// 之后的阶段（紧凑行、右侧轨、版式）只改这里，经典视图一个字不动。
// 经典里新增了条目或数据来源时，对等测试会提醒这里跟上。
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
const highlightedSessionId = ref<string | null>(null)
let highlightTimer: any = null
// 右侧轨点击跳转后给目标条目闪一下外框（data-rail-flash，样式见文件末尾）
let railFlashTimer: ReturnType<typeof setTimeout> | null = null
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

// 离屏跳过渲染（#158）：最近 LIVE_TAIL_ROUNDS 轮保持完整渲染——用户停留最多的位置，贴底、新消息、回到底部都要精确的几何；
// 更早的轮次（pv-cv）套 content-visibility:auto：离开视口的轮次不参与样式 / 布局 / 绘制 / 命中测试。
// 度量（bench-preview.mjs，150 轮 / 约 4.45 万个元素的长会话）：滚动的主线程开销再降约 40%（连同右侧轨拆分、惰性菜单共降约 58%）；
// 窗口缩放 / 字体加载这类整页重排在桌面上只是被推迟（右侧轨下一次量位置时会强制把跳过的内容补算一遍），手机上轨是隐藏的才是真省。
// 代价：没渲染过的轮次高度是预估值（占位 900px，靠近视口渲染后才变成真实高度），平滑滚动的终点（settle）、向上载入后的锚点（hold）都要校准。
// 轮次数不超过 LIVE_TAIL_ROUNDS 时一个轮次都不套，行为与以前完全一样。
const LIVE_TAIL_ROUNDS = 20
/** 下标小于它的轮次（displayedConversationRounds 里更早的那些）套 pv-cv。 */
const cvCutoff = computed(() => displayedConversationRounds.value.length - LIVE_TAIL_ROUNDS)
// 紧挨着锚点的几轮豁免 pv-cv：向上载入一批时，这批最下面、紧挨着原来第一轮的几轮从插入那一刻起就是真实高度（见 lib/scrollSettle.ts 的 holdBoundaryKeys）。
// 只记最近一次载入的那几轮；下一次载入时它们已经在视口下方很远，重新套上 pv-cv 不影响视口里的位置。
const boundaryRounds = ref<Set<string>>(new Set())

// 滚动位置校准（#158）：套了 pv-cv 的轮次高度在进视口之前是预估值（进了视口才变成真实高度），
//   · settle：平滑滚动起跑时算死的终点会偏，落地后量一次补回来；
//   · hold：向上载入更早的轮次后把原来的第一轮钉在视口里的原位（占位高度会在之后几帧变成真实高度，光靠载入前后的 scrollHeight 之差补偿会偏）；
//     紧挨着锚点的几轮豁免 pv-cv（boundaryRounds），否则它们是占位、晚一帧才变高，浏览器的滚动锚定补不全（真实长会话与放慢 CPU 的实测都复现过）。
const { settle: settleScroll, hold: holdAnchor } = useScrollSettle()
// 末尾渐进渲染补齐时钉锚点用另一个实例（#169 第三期）：同一个实例里「新的取消旧的」，共用会让补齐打断一次正在进行的平滑滚动校准（反过来也一样）
const { hold: holdTailAnchor } = useScrollSettle()

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

// ── 正文按需（#169 第二期「索引优先 + 正文按需」）──
// 预览视图取的会话表里，较早的轮次只带首行预览（body_omitted）。窗口要把这些轮次放进来之前，先把它们的正文取回：
// 窗口里不出现骨架，向上载入 / 跳转时的锚点与高度校准也不会被「正文到了才变高」打乱。
// 取失败不阻塞：照常放进来，卡片自己显示骨架并隔一会儿重试（PreviewBodyGate）。取回不抛错。

/** 取回「窗口撑到 count 轮」时窗口里所有行（含轮次之间的进度通知）的正文；没有轻量行时立即返回。 */
async function ensureWindowBodies(workflowId: string | null, count: number, opts: { force?: boolean } = {}): Promise<void> {
  if (!workflowId) return
  const rounds = props.conversationRounds
  const first = Math.max(0, rounds.length - count)
  await sessionStore.ensureBodiesAfter(workflowId, first > 0 ? rounds[first - 1].session_id : null, opts)
}

/** 把窗口撑到至少 count 轮：先取回正文再增长（跳转用，用户手势，无视自动重试的退避）。等待期间换了工作流就放弃。 */
async function growWindowTo(count: number): Promise<void> {
  if (Math.min(props.conversationRounds.length, count) <= visibleRoundsCount.value) return
  const workflowId = currentWorkflowId.value
  await ensureWindowBodies(workflowId, count, { force: true })
  if (currentWorkflowId.value !== workflowId) return
  visibleRoundsCount.value = Math.max(visibleRoundsCount.value, Math.min(props.conversationRounds.length, count))
}

/** 预取下一批的正文（空闲时、不抢首屏）：用户接着往上翻时，下一次载入不用再等网络。 */
function prefetchNextBatch() {
  const workflowId = currentWorkflowId.value
  if (!workflowId || !hasMoreEarlierRounds.value) return
  void ensureWindowBodies(workflowId, visibleRoundsCount.value + BATCH_LOAD_ROUNDS)
}

// 列表变化后去抖地预取（不能晚于下面 conversationRounds 的 immediate 监听声明：它在 setup 里就会调用）
const PREFETCH_DELAY_MS = 1500
let prefetchTimer: ReturnType<typeof setTimeout> | null = null
function schedulePrefetch() {
  if (prefetchTimer) clearTimeout(prefetchTimer)
  prefetchTimer = setTimeout(() => {
    prefetchTimer = null
    prefetchNextBatch()
  }, PREFETCH_DELAY_MS)
}

async function loadEarlierRounds() {
  if (!hasMoreEarlierRounds.value || isLoadingEarlier.value || !chatScrollContainer.value) {
    return
  }

  isLoadingEarlier.value = true
  // 这一批轮次的正文先取回，再量锚点、增长窗口（量锚点必须在等待之后：等待期间用户还在滚）
  const workflowId = currentWorkflowId.value
  await ensureWindowBodies(workflowId, visibleRoundsCount.value + BATCH_LOAD_ROUNDS, { force: true })
  if (currentWorkflowId.value !== workflowId || !chatScrollContainer.value || !hasMoreEarlierRounds.value) {
    isLoadingEarlier.value = false
    return
  }
  const container = chatScrollContainer.value
  const oldScrollHeight = container.scrollHeight
  const oldScrollTop = container.scrollTop
  // 锚点：载入前第一轮里的第一条会话块（同一个 DOM 节点，按 session_id 做 key，新轮次插到它上面它也不会重建）及它在视口里的位置。
  // 钉的是这块内容而不是轮次块的顶边：轮次顶上的日期分隔条会随新轮次插到上面而消失（同一天不再重复显示），那几十像素是内容自己的变化，
  // 钉顶边会让用户正在看的内容跟着上移一截。
  const firstGroup = container.querySelector<HTMLElement>('[data-testid="turn-group"]')
  const anchor = firstGroup?.querySelector<HTMLElement>('[id^="session-"]') ?? firstGroup
  const anchorOffset = anchor ? offsetInContainer(container, anchor) : 0

  // 紧挨着锚点的几轮不套 pv-cv，要和窗口增长在同一个 tick 里改（同一次渲染里生效）
  boundaryRounds.value = new Set(
    holdBoundaryKeys(props.conversationRounds.map(r => r.session_id), visibleRoundsCount.value, BATCH_LOAD_ROUNDS),
  )
  visibleRoundsCount.value = Math.min(
    props.conversationRounds.length,
    visibleRoundsCount.value + BATCH_LOAD_ROUNDS
  )

  nextTick(() => {
    const c = chatScrollContainer.value
    if (c) {
      if (anchor?.isConnected) {
        // 保持用户视口相对位置绝对稳定，向上延伸无感承接：立刻把锚点放回原位，之后几帧里新轮次的高度（占位 → 真实）变了再放回
        holdAnchor(c, anchor, anchorOffset)
      } else {
        c.scrollTop = oldScrollTop + (c.scrollHeight - oldScrollHeight)
      }
    }
    setTimeout(() => {
      isLoadingEarlier.value = false
      prefetchNextBatch()
      rearmTopObserver()
    }, 60)
  })
}

/**
 * 忙碌期结束后让顶部哨兵按「当前」状态再通知一次。
 * IntersectionObserver 只在哨兵「进入 / 离开」范围的那一下通知：这一批刚载完的几十毫秒里（锚点校准完、忙碌标志还没放开）
 * 用户又滚回了最上面的话，哨兵进入范围的那次通知撞上忙碌标志被丢掉，之后哨兵一直待在范围内、不会再有通知，
 * 列表就卡在这一批——再往上滚也没用（已经在最上面了），要先往下滚再回来才恢复。重新观察一次会补发一次当前状态的通知。
 */
function rearmTopObserver() {
  const sentinel = topSentinelRef.value
  if (!topObserver || !sentinel) return
  topObserver.unobserve(sentinel)
  topObserver.observe(sentinel)
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

// ── 末尾通知渐进渲染（#169 第三期，规则与取舍见 lib/tailWindow.ts）──
// 末尾先只渲染最新的一批块，更早的在浏览器空闲时一批一批补齐；用户向上够到插入点附近就不等空闲、立刻补齐；
// 被点名要看的块（右侧轨 / 搜索命中 / 「待答复」徽标 / Q 引用）先露出来再滚动。内容一个不少，只是晚几百毫秒出现。
// 起点按「块的键」钉住：新通知追加在尾部、晚到的确认项 / 收件箱条目按时间插进中间，窗口都不会滑动；
// 钉住的那一块不见了（重新分摞）就整段渲染——宁可多渲染一点，也不让已经出现的内容消失。
const tailPinnedKey = ref<string | null>(null) // null = 还没钉；'' = 全部渲染
const tailStart = computed(() => {
  const blocks = tailBlocks.value
  const key = tailPinnedKey.value
  let pinned: number | null = null
  if (key !== null) pinned = key === '' ? 0 : Math.max(0, blocks.findIndex(b => b.key === key))
  return effectiveTailStart(pinned, blocks.length)
})
const visibleTailBlocks = computed(() => (tailStart.value > 0 ? tailBlocks.value.slice(tailStart.value) : tailBlocks.value))

function pinTailStart(index: number) {
  const blocks = tailBlocks.value
  tailPinnedKey.value = index <= 0 || index >= blocks.length ? '' : blocks[index].key
}
// 第一次看到末尾有块（或换了工作流、重置之后）：把起点钉住。用默认的 pre 时机（先于这一次渲染）；
// 不用 sync：每次依赖变化都要重算整条时间线，得不偿失。
watch(() => tailBlocks.value.length > 0 && tailPinnedKey.value === null, needPin => {
  if (needPin) pinTailStart(initialTailStart(tailBlocks.value.length))
}, { immediate: true })

/** 让下标 index 的末尾块渲染出来（起点只会往前退）。 */
function revealTailIndex(index: number) {
  const next = revealTailStart(tailStart.value, index)
  if (next !== tailStart.value) pinTailStart(next)
}
function revealTailBlock(blockKey: string) {
  revealTailIndex(tailBlocks.value.findIndex(b => b.key === blockKey))
}

type TailFillHandle = { kind: 'idle'; id: number } | { kind: 'timer'; id: ReturnType<typeof setTimeout> }
let tailFillHandle: TailFillHandle | null = null
let tailFilling = false
let tailProbeFrame = 0

/** 撤掉已排好的空闲补齐（用户够到了、或要立刻补一批时）。 */
function cancelTailIdle() {
  if (!tailFillHandle) return
  if (tailFillHandle.kind === 'idle') window.cancelIdleCallback?.(tailFillHandle.id)
  else clearTimeout(tailFillHandle.id)
  tailFillHandle = null
}

/** 撤掉所有排着的补齐与滚动探测（换工作流、卸载时）。 */
function cancelTailFill() {
  cancelTailIdle()
  if (tailProbeFrame) {
    cancelAnimationFrame(tailProbeFrame)
    tailProbeFrame = 0
  }
}

/** 空闲时补下一批；浏览器一直忙也最多等 TAIL_FILL_IDLE_TIMEOUT_MS。不支持 requestIdleCallback 的浏览器（Safari）退回定时器。 */
function scheduleTailFill() {
  if (tailFillHandle || tailFilling || tailStart.value <= 0) return
  const run = () => {
    tailFillHandle = null
    void fillTailBatch()
  }
  tailFillHandle = typeof window.requestIdleCallback === 'function'
    ? { kind: 'idle', id: window.requestIdleCallback(run, { timeout: TAIL_FILL_IDLE_TIMEOUT_MS }) }
    : { kind: 'timer', id: setTimeout(run, TAIL_FILL_FALLBACK_MS) }
}

/**
 * 末尾已渲染的第一块（补齐总是发生在它的上面）。末尾容器用 data-tail-total 认（只有它带这个属性），
 * 不写 data-testid 的字面量——对等测试的负向对照按文字找模板里的 testid，脚本里多一处字面量会让它找错地方。
 */
function firstTailBlockEl(): HTMLElement | null {
  return chatScrollContainer.value?.querySelector<HTMLElement>('[data-tail-total] > [data-timeline-key]') ?? null
}

/**
 * 补齐一批。插入点在已渲染的第一块之上：视口在它或它下面（用户停在底部的常见情形）时，把这一块钉在原位，视口里的内容纹丝不动；
 * 用户正在看插入点上面（轮次）时不钉，免得把视口推走。钉锚点用独立的实例，不打断正在进行的平滑滚动校准。
 */
async function fillTailBatch() {
  const container = chatScrollContainer.value
  if (!container || tailFilling || tailStart.value <= 0) return
  tailFilling = true
  try {
    const first = firstTailBlockEl()
    const firstTop = first ? offsetInContainer(container, first) : 0
    const hold = first !== null && shouldHoldFirst(firstTop)
    pinTailStart(nextTailStart(tailStart.value))
    await nextTick()
    if (hold && first && first.isConnected && chatScrollContainer.value === container) holdTailAnchor(container, first, firstTop)
  } finally {
    tailFilling = false
  }
  scheduleTailFill()
}

/** 滚动时（rAF 去抖）：已渲染的第一块的顶边离视口顶边不到 TAIL_PROBE_PX = 用户正在向上够它，不等空闲、立刻补一批。 */
function probeTailFill() {
  if (tailProbeFrame || tailFilling || tailStart.value <= 0) return
  tailProbeFrame = requestAnimationFrame(() => {
    tailProbeFrame = 0
    const container = chatScrollContainer.value
    const first = firstTailBlockEl()
    if (!container || !first || tailFilling || tailStart.value <= 0) return
    if (shouldFillNow(offsetInContainer(container, first))) {
      cancelTailIdle()
      void fillTailBatch()
    }
  })
}

// 起点首次钉在 0 之后的位置（有块被延后了）：渲染完成后开始空闲补齐。换工作流重置时先取消上一个工作流的补齐。
watch(tailStart, start => {
  if (start > 0) scheduleTailFill()
}, { immediate: true, flush: 'post' })
onMounted(scheduleTailFill)
onBeforeUnmount(cancelTailFill)
// 日期分隔（#120 阶段 E-b）：按「当前显示的顶层条目」的顺序（间隙里的块 → 轮次 → …… → 末尾的块）算出每个日历日的第一个条目，
// 在它前面画一条「今天 / 昨天 / 日期」小条。轮次里面（提问与回复之间）的块不是顶层条目，不参与；
// 键与模板里的 v-for 键一致：块用 block.key，轮次用 session_id。标签的「今天 / 昨天」跟着低频时钟走，过了零点会自己变。
const dayMarks = computed<Record<string, string>>(() => {
  const items: DayItem[] = []
  const pushBlocks = (blocks: ReadonlyArray<TimelineBlock>) => {
    for (const b of blocks) items.push({ key: b.key, at: b.type === 'stack' ? b.entries[0].at : b.entry.at })
  }
  for (const s of displayedConversationRounds.value) {
    pushBlocks(timelineBlocks.value[s.session_id] || [])
    items.push({ key: s.session_id, at: s.created_at })
  }
  pushBlocks(tailBlocks.value)
  return daySeparators(items, new Date(inboxNow.value))
})
// 未读（#120 阶段 E-c2）：进入这个会话时冻结一份已读游标，第一条晚于它的 Agent 消息前面画「以下为新消息」；
// 停在底部、标签页可见约 2 秒之后，把共享已读游标推到最新（侧栏的未读数同步清零）。分隔线的位置与 dayMarks 走同一个显示顺序（lib/previewUnread）。
// 分隔线只在「进入时有未读」才出现，之后一直留到离开这个会话——标完已读它也不消失；进来时没有未读、边看边来的新消息不画（向上翻时由「新消息」浮动提醒管）。
const unreadList = computed(() => unreadItems(displayedConversationRounds.value, timelineBlocks.value, tailBlocks.value))
const previewUnread = usePreviewUnread({
  atBottom: () => !isScrolledUp.value,
  renderedMs: () => renderedAgentMs(unreadList.value),
})
const unreadMarkKey = computed(() => {
  const readMs = previewUnread.dividerMs.value
  return readMs === null ? null : unreadDividerKey(unreadList.value, readMs, hasMoreEarlierRounds.value)
})
watch(() => currentWorkflowId.value || confirmStore.workflowId || '', wf => void previewUnread.open(wf), { immediate: true })
// 渲染出新的 Agent 消息（SSE 刷新数据之后）：停在底部就重新计时
watch(() => renderedAgentMs(unreadList.value), () => previewUnread.touch())
// 间隙里的条目渲染（轮次之间与末尾两处共用，只写一份）。
// cv：这个间隙里的进度通知卡允许套离屏跳过渲染（pv-cv-notice，#169）。只有「末尾」那一处传它：
// 轮次之间的间隙在轮次组（turn-group）里面，更早的轮次已经整组套了 pv-cv，最近 20 轮要精确的几何（贴底 / 向上载入的锚点），
// 在这里再套一层，e2e/preview-longlist.mjs 实测向上载入后原来的第一轮会漂 197px，所以不套；末尾的通知流不在任何轮次组里，才是 pv-cv 管不到的。
const [DefineGap, ReuseGap] = createReusableTemplate<{ blocks: TimelineBlock[]; cv?: boolean }>()
// 进度通知卡的离屏跳过渲染（#169，规则见 lib/noticeCv.ts）：最新 LIVE_TAIL_NOTICES 条保持完整渲染，更早的才套；通知不多时一个都不套。
const liveNotices = computed(() => liveNoticeKeys(props.notices ?? []))

// 工具栏「待答复」徽标点一下、或点正文里的 Q23 引用，会把要看的编号放进 confirmStore.focusRef：那张卡可能落在还没展开的更早轮次里，
// 先把窗口撑到它所在的间隙；已处理的提问是事件行，收在默认收起的摞里（行没有挂载，也就读不到 focusRef），所以也要先把那一摞展开；
// 卡挂载时自己读取 focusRef 完成滚动与高亮（见 ConfirmTimelineCard）。
watch(() => confirmStore.focusRef, ref => {
  if (!ref) return
  const closed = stackKeysHoldingConfirm(timelineBlocks.value, ref).filter(key => !expandedStacks.value.has(key))
  if (closed.length) expandedStacks.value = new Set([...expandedStacks.value, ...closed])
  const placement = timelinePlacement.value
  const gapKey = Object.keys(placement).find(
    key => key !== TIMELINE_TAIL_KEY && placement[key].some(e => e.kind === 'confirm' && e.item.ref === ref),
  )
  if (!gapKey) {
    // 在末尾：它所在的块可能还没渲染（渐进渲染，#169 第三期），先让它露出来；卡挂载时自己读取 focusRef 完成滚动与高亮
    revealTailIndex(tailBlocks.value.findIndex(b => blockHoldsConfirm(b, ref)))
    return
  }
  const total = props.conversationRounds.length
  const idx = props.conversationRounds.findIndex(s => s.session_id === gapKey)
  if (idx === -1) return
  const needed = total - idx
  // 先取回要放进窗口的轮次的正文再撑开（#169 第二期）；focusRef 在卡处理之前一直保留，晚一点挂载也能接上
  if (needed > visibleRoundsCount.value) void growWindowTo(needed + 2)
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
function tickInboxClock() {
  inboxNow.value = Date.now()
}

onMounted(() => {
  setupTopObserver()
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
  if (railFlashTimer) {
    clearTimeout(railFlashTimer)
    railFlashTimer = null
  }
  if (prefetchTimer) {
    clearTimeout(prefetchTimer)
    prefetchTimer = null
  }
  previewSearch.close()
})

function getSessionTurnNumber(session: FeedbackSession): number {
  const index = props.conversationRounds.findIndex(s => s.session_id === session.session_id)
  return index !== -1 ? index + 1 : 1
}

watch(() => props.conversationRounds, (newRounds) => {
  const targetWId = newRounds.length > 0 ? (newRounds[0].workflow_id || newRounds[0].session_id) : null
  // 列表变了（切换工作流 / 整表到了 / 事件补丁）：稍后在空闲时预取下一批轮次的正文（#169 第二期，去抖，没有轻量行时什么也不做）
  schedulePrefetch()

  // 1. 如果切换了不同的 Workflow 或首次载入：彻底重置未读计数，并记录已知 session_id 集合
  if (targetWId !== currentWorkflowId.value) {
    currentWorkflowId.value = targetWId
    previewSearch.close() // 搜索面板是按工作流搜的，换了工作流就关掉
    unreadNewMessagesCount.value = 0
    visibleRoundsCount.value = INITIAL_VISIBLE_ROUNDS
    boundaryRounds.value = new Set()
    // 末尾渐进渲染也从头来：撤掉上一个工作流还没补完的批次，起点重新钉（见 watch(() => tailBlocks…) 与 lib/tailWindow）
    cancelTailFill()
    tailPinnedKey.value = null
    knownSessionIds.value = new Set(newRounds.map(r => r.session_id))
    nextTick(() => {
      scrollToBottom(false)
      setupTopObserver()
      scheduleTailFill() // 新工作流的起点已钉好（pre 时机）；起点数字与上一个工作流恰好相同时 watch(tailStart) 不会响，这里兜底
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

async function scrollToSession(sessionId: string) {
  const targetIdx = props.conversationRounds.findIndex(s => s.session_id === sessionId)
  if (targetIdx === -1) return

  const total = props.conversationRounds.length
  const neededVisibleCount = total - targetIdx
  if (neededVisibleCount > visibleRoundsCount.value) {
    // 目标不在窗口里：先取回「目标到最新一轮」所有行的正文再撑开窗口（#169 第二期），落地时卡是真实高度，平滑滚动的终点不会漂
    const workflowId = currentWorkflowId.value
    await growWindowTo(neededVisibleCount + 5)
    // 等待期间换了工作流 / 这一轮没了：不再滚
    if (currentWorkflowId.value !== workflowId || !props.conversationRounds.some(s => s.session_id === sessionId)) return
  }

  // 外框起跑时亮一次、落地时再亮满 2.5 秒：远距离的平滑滚动要两三秒，只在起跑时亮的话，到了它早灭了
  const armHighlight = () => {
    highlightedSessionId.value = sessionId
    if (highlightTimer) clearTimeout(highlightTimer)
    highlightTimer = setTimeout(() => {
      highlightedSessionId.value = null
    }, 2500)
  }
  const attemptScroll = () => {
    const el = document.getElementById(`session-${sessionId}`)
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' })
      settleScroll(chatScrollContainer.value, el, { onSettled: armHighlight })
      armHighlight()
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
    }, 250)
  } else {
    // 首次加载/刷新/切换会话：双帧 rAF 校准，抵抗异步 DOM 展开导致的高度时滞 (D-142)
    requestAnimationFrame(() => {
      doScroll()
      requestAnimationFrame(() => {
        doScroll()
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

// 点击右侧轨上的刻度（#120 阶段 2）。轮次走上面现成的「滚到某一轮」（含分页扩窗与高亮）；
// 其余条目都是已经渲染出来的（轨只描述已载入的条目），直接滚到它上方并闪一下外框。
function onRailJump(item: RailItem) {
  if (item.kind === 'round' && item.ref) {
    scrollToSession(item.ref)
    return
  }
  const container = chatScrollContainer.value
  if (!container) return
  const el = Array.from(container.querySelectorAll<HTMLElement>('[data-rail-id]')).find(n => n.getAttribute('data-rail-id') === item.id)
  if (!el) return
  const top = el.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop
  container.scrollTo({ top: jumpScrollTop(top, container), behavior: 'smooth' })
  // 起跑时闪一下，落地时再闪一下（远距离的平滑滚动要两三秒，起跑时的那一下到落地早没了）
  settleScroll(container, el, { onSettled: () => flashElement(container, el) })
  flashElement(container, el)
}

// 给一个条目闪一下外框（右侧轨跳转与搜索命中落点共用；样式见文件末尾的 data-rail-flash）
function flashElement(container: HTMLElement, el: HTMLElement) {
  // 重新触发闪烁动画：先摘掉再（强制回流后）加上
  container.querySelectorAll('[data-rail-flash]').forEach(n => n.removeAttribute('data-rail-flash'))
  void el.offsetWidth
  el.setAttribute('data-rail-flash', '')
  if (railFlashTimer) clearTimeout(railFlashTimer)
  railFlashTimer = setTimeout(() => {
    el.removeAttribute('data-rail-flash')
    railFlashTimer = null
  }, 1800)
}

// ── 消息搜索（#120 阶段 E-c1）：点命中 → 落到那张卡 ──
// 搜索面板只把命中的那条聊天消息交出来；这里按它的 source 认卡（lib/previewSearch.resolveHit），再按卡的种类落点：
//   · 轮次（汇报 / 回复）→ 现成的 scrollToSession（含分页扩窗与 ring 高亮）；
//   · 确认项 → 先把它「摆出来」（扩窗、展开所在的摞）、等卡挂载，再走 confirmStore.focusRef（与「待答复」徽标 / Q 引用同一条路：滚到卡并亮边框）；
//   · 其余顶层条目（追加 / 通知 / 任务进展 / 决策汇总）→ 摆出来后滚到那一块并闪一下；
//   · 认不出卡的（事件、已被收走的条目）→ 落到时间上最近的上一轮。
const previewSearch = usePreviewSearch()

/** 把条目所在的位置「摆出来」：窗口撑到它所在的间隙、它所在的摞展开（只改状态，不滚动）。返回它所在的块；找不到返回 null。 */
async function revealEntry(entryKey: string) {
  let found = findEntryBlock(timelineBlocks.value, entryKey)
  if (!found) return null
  // 在末尾的块可能还没渲染（渐进渲染，#169 第三期）：先让它露出来
  if (found.gapKey === TIMELINE_TAIL_KEY) revealTailBlock(found.block.key)
  const roundId = roundIdOfGap(found.gapKey)
  if (roundId) {
    const idx = props.conversationRounds.findIndex(s => s.session_id === roundId)
    const count = windowToShow(props.conversationRounds.length, idx, visibleRoundsCount.value)
    if (count > visibleRoundsCount.value) {
      // 窗口要扩：先取回新进窗口的轮次的正文（#169 第二期）；等待期间列表可能变了，重新找一遍
      await growWindowTo(count)
      found = findEntryBlock(timelineBlocks.value, entryKey)
      if (!found) return null
    }
  }
  if (found.block.type === 'stack' && !expandedStacks.value.has(found.block.key)) {
    expandedStacks.value = new Set([...expandedStacks.value, found.block.key])
  }
  return found
}

async function scrollToEntry(entryKey: string): Promise<boolean> {
  const found = await revealEntry(entryKey)
  if (!found) return false
  const blockKey = found.block.key
  const attempt = (): boolean => {
    const container = chatScrollContainer.value
    if (!container) return false
    const el = Array.from(container.querySelectorAll<HTMLElement>('[data-timeline-key]')).find(n => n.getAttribute('data-timeline-key') === blockKey)
    if (!el) return false
    const top = el.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop
    container.scrollTo({ top: jumpScrollTop(top, container), behavior: 'smooth' })
    settleScroll(container, el, { onSettled: () => flashElement(container, el) })
    flashElement(container, el)
    return true
  }
  // 窗口扩开 / 摞展开后下一帧才有 DOM；再给一帧兜底
  void nextTick(() => {
    if (!attempt()) requestAnimationFrame(() => void attempt())
  })
  return true
}

async function jumpToHit(hit: ChatMessage) {
  const confirms = new Map<string, { seq: number; ref: string }>()
  for (const item of confirmStore.timelineItems) confirms.set(String(item.id), { seq: item.seq, ref: confirmRef(item.seq) })
  const target = resolveHit(hit, {
    roundIds: new Set(props.conversationRounds.map(s => s.session_id)),
    entryKeys: new Set(timelineEntries.value.map(e => e.key)),
    confirms,
  })
  switch (target.type) {
    case 'round':
      scrollToSession(target.sessionId)
      return
    case 'confirm':
      // 卡是「被点名时」才滚动的：它若在点名之后才挂载，useConfirmFocus 在 setup 里读到 focusRef 时还没有根节点，只会亮边框、不会滚动。
      // 所以先摆出来、等它挂载好，再点名；先清空再点名，同一张卡连着搜到两次也各触发一次
      await revealEntry(target.key)
      confirmStore.focusRef = ''
      await nextTick()
      confirmStore.focusRef = target.ref
      return
    case 'entry':
      if (await scrollToEntry(target.key)) return
      break
  }
  const ms = target.type === 'time' ? target.ms : Date.parse(hit.sent_at)
  const roundId = nearestRoundBefore(props.conversationRounds, ms)
  if (roundId) scrollToSession(roundId)
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

  // 停在底部才计「看过了」的时间；离开底部就取消（E-c2）
  previewUnread.touch()

  // 末尾还有没渲染的块：用户向上够到插入点附近就不等空闲、立刻补齐（#169 第三期；补齐完成后 tailStart 为 0，这里一次比较就返回）
  if (tailStart.value > 0) probeTailFill()
}

defineExpose({
  scrollToBottom,
  scrollByDelta,
  scrollToSession
})
</script>

<template>
  <!-- Middle Conversation Area（pv-list：下面的作用域样式靠它给 Markdown 内嵌图补底色） -->
  <div class="pv-list flex-1 overflow-hidden relative">
    <!--
      一个间隙里的条目：按 groupBlocks 分好的块依次渲染。连续的可叠卡片成一摞（默认收起，点开看全部），
      其余各自单独一块——您的话、智能体的话、还没答复的提问 / 授权、守护告警、恢复胶囊永远摊开。
      在「轮次之间」与「末尾」两处用 <ReuseGap> 渲染，这里只定义一次。
    -->
    <DefineGap v-slot="{ blocks, cv }">
      <!-- 每一块外包一层只带标记的 div（key / 种类 / 提交时间）：e2e 与排障据此核对「整条流是不是按提交时间排的」；摞取第一张卡的时间 -->
      <!-- 同一个根节点上再挂 data-rail-*（种类 / 状态 / 标题 / 时间）：右侧轨据此量位置、画刻度（#120 阶段 2） -->
      <template v-for="block in blocks" :key="block.key">
      <!-- 日期分隔（E-b）：新的一天的第一个条目前面；轮次里面的块不在 dayMarks 里，所以不会出现 -->
      <PreviewDateSeparator v-if="dayMarks[block.key]" :label="dayMarks[block.key]" />
      <!-- 以下为新消息（E-c2）：这一块（或这一摞）里有第一条晚于进入时已读游标的 Agent 消息 -->
      <PreviewUnreadDivider v-if="unreadMarkKey === block.key" />
      <div
        :data-timeline-key="block.key"
        :data-timeline-kind="block.type === 'stack' ? 'stack' : block.entry.kind"
        :data-timeline-at="block.type === 'stack' ? block.entries[0].at : block.entry.at"
        v-bind="railDomAttrs(railMetaOfBlock(block))"
        :class="noticeCvClass(liveNotices, block, cv)"
      >
        <!-- compact：没有正文的事件（任务进展 / 已处理的提问与决策 / 告警 / 恢复胶囊 / 卡片摞）都是 32px 的事件行（#120 · D-273 阶段 C2；经典列表不传它） -->
        <CardStack
          v-if="block.type === 'stack'"
          :entries="block.entries"
          :expanded="expandedStacks.has(block.key)"
          compact
          @toggle="toggleStack(block.key)"
        >
          <template #card="{ entry }">
            <TimelineCardEntry :entry="entry" compact />
          </template>
        </CardStack>
        <!-- 您的追加（D-212 的追加指令收件箱）：靠右的统一消息卡，状态由这里的 10 秒时钟算好后传进去（#120 · D-273 阶段 C） -->
        <PreviewInboxCard
          v-else-if="block.entry.kind === 'inbox'"
          :q="block.entry.q"
          :status="statusOf(block.entry.q)"
          @revoke="emit('revoke-queued', block.entry.q.id)"
        />
        <CapsuleMarker
          v-else-if="block.entry.kind === 'capsule'"
          compact
          :capsule="block.entry.capsule"
          :expanded="expandedCapsules.has(block.entry.capsule.revision)"
          :time-text="formatDateTimeCustom(block.entry.capsule.created_at)"
          :anchor-turn="turnNumberOfSession(block.entry.capsule.basis?.anchor_session_id)"
          @toggle="toggleCapsule(block.entry.capsule.revision)"
          @jump="scrollToSession(block.entry.capsule.basis?.anchor_session_id || '')"
          @copy="copyText"
        />
        <!-- 进度通知是正文，不折叠：套统一消息卡（#120 · D-273 阶段 B）；其余没有正文的卡片走事件行（compact） -->
        <PreviewNoticeCard v-else-if="block.entry.kind === 'notice'" :entry="block.entry" />
        <!-- 还要您处理的提问 / 计划确认：统一消息卡，作答区在卡里（#120 · D-273 阶段 C3）；答复 / 忽略后条目变成已处理，下一帧起走下面的事件行 -->
        <PreviewConfirmCard
          v-else-if="block.entry.kind === 'confirm' && needsAnswer(block.entry.item)"
          :item="block.entry.item"
          :workflow-id="confirmStore.workflowId"
        />
        <TimelineCardEntry v-else :entry="block.entry" compact />
      </div>
      </template>
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
          v-for="(session, gi) in displayedConversationRounds"
          :key="session.session_id"
          class="space-y-3"
          :class="gi < cvCutoff && !boundaryRounds.has(session.session_id) ? 'pv-cv' : undefined"
          data-testid="turn-group"
        >
        <!--
          轮次之间的所有条目（您的追加 / 智能体的进度通知 / 任务进展卡 / 确认与授权卡 / 文档决策汇总 / 守护告警 / 恢复胶囊）：
          合并成一条按提交时间排序的流再渲染；连续的卡片叠成一摞（#52 / #105）。
        -->
        <ReuseGap :blocks="timelineBlocks[session.session_id] || []" />
        <!-- 日期分隔（E-b）：这一轮是新的一天的第一个条目时，画在它（以及它的「期间有新提交」分隔条）前面 -->
        <PreviewDateSeparator v-if="dayMarks[session.session_id]" :label="dayMarks[session.session_id]" />
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
        <!-- 以下为新消息（E-c2）：这一轮（汇报本身，或夹在提问与回复之间的 Agent 条目）是第一条晚于进入时已读游标的 Agent 消息 -->
        <PreviewUnreadDivider v-if="unreadMarkKey === session.session_id" />
        <div
          :id="`session-${session.session_id}`"
          class="space-y-3 sm:space-y-4 py-2 transition-all rounded-md scroll-mt-6"
          :class="highlightedSessionId === session.session_id ? 'ring-2 ring-primary bg-primary/5' : ''"
          v-bind="railDomAttrs(railMetaOfRound(session))"
        >
          <!-- AI Message Turn (Proposal) -->
          <div class="space-y-2">
            <!--
              汇报卡（#120 · D-273 阶段 C）：经典的「轮次分隔条」与「AI 方案汇报」两段合成一张统一消息卡——
              头部：头像 · 汇报 · 标题 · 时间 · 状态 · ⋯（复制 / 删除此轮）；正文：Markdown 全文；脚注：交付锚点 · 会话号 · 引用 · 工作流 · 工作区路径。
              「删除此轮」的两段式确认（3.5 秒倒计时）与可撤销快照仍由这里执行，卡片只发 delete 事件、并知道确认进行到哪一步。
            -->
            <PreviewReportCard
              :session="session"
              :project-dir="formatSessionProjectDirectory(session)"
              :confirming-delete="confirmDeleteSessionId === session.session_id"
              @delete="handleDeleteSession(session)"
            />
            <!-- 本轮任务：这一轮创建 / 推进过的任务，可展开；这一轮没碰过任务时不出现 -->
            <SessionTasks :session-id="session.session_id" />
          </div>

          <!-- 提问与回复之间发生的追加 / 通知 / 卡片：按时间夹在中间，不再统一排到整张卡之后（#118） -->
          <div v-if="(timelineBlocks[inRoundKey(session.session_id)] || []).length" class="space-y-3" data-testid="in-round-gap">
            <ReuseGap :blocks="timelineBlocks[inRoundKey(session.session_id)] || []" />
          </div>

          <!--
            您的确认回复（#120 · D-273 阶段 C）：靠右的统一消息卡（绿色「回复」），只在这一轮已被确认时出现；
            撤回只在智能体还没取走时可点（卡片脚注里的按钮），撤回动作由这里转成 revoke-session 事件。
            外面包一层带 data-rail-* 的 div：右侧轨据此给它画刻度，位置按「您提交回复的时刻」。
          -->
          <div v-if="session.status === 'completed'" v-bind="railDomAttrs(railMetaOfReply(session, replySentAt(session)))">
            <PreviewReplyCard
              :session="session"
              :turn="getSessionTurnNumber(session)"
              @revoke="emit('revoke-session', session.session_id)"
            />
          </div>

          <!-- Cancelled or Expired Notice -->
          <div v-else-if="session.status === 'cancelled' || session.status === 'timeout'" class="pl-2 sm:pl-8 text-xs text-muted-foreground italic flex items-center gap-1.5 py-1">
            <AlertCircle class="w-3.5 h-3.5" />
            <span>本轮交互已关闭（{{ sessionStatusBadge(session.status).label }}）</span>
          </div>
        </div>
        </div>

        <!-- 比所有轮次都新的条目落在时间线末尾（同样是一条按提交时间排序的流） -->
        <!--
          渐进渲染（#169 第三期）：data-tail-total = 末尾一共多少块，data-tail-hidden = 还有多少块（最早的那些）没渲染，补齐完成为 0；
          e2e 与基准脚本据此等「补齐完成」，不要在这里用 v-if / v-show 隐藏内容。
        -->
        <div
          v-if="tailBlocks.length > 0"
          class="space-y-3"
          data-testid="timeline-tail"
          :data-tail-total="tailBlocks.length"
          :data-tail-hidden="tailStart"
        >
          <!-- cv 要写成 :cv="true"：createReusableTemplate 的 ReuseGap 没有声明属性，裸写 cv 传进去的是空串（假值），通知卡一条都不会套 -->
          <ReuseGap :blocks="visibleTailBlocks" :cv="true" />
        </div>

        <!-- 底部物理定位锚点 (D-142: 确保 100% 直达会话流最新最底部，消灭虚拟高度塌陷和高度时滞导致的半途卡顿) -->
        <div ref="bottomAnchorRef" class="h-0 w-full pointer-events-none opacity-0"></div>
      </div>
    </div>

    <!--
      右侧轨（#120 阶段 2）：取代经典的 TimelineScrubber。列表里每个带 data-rail-* 的条目在轨上都有一道刻度，
      位置按真实位置等比缩放、长度与颜色按类型 / 状态；点击跳转、悬停看标题；内容不足 1.5 屏时自己隐藏（桌面宽度才有）。
    -->
    <PreviewTimelineRail
      v-if="props.conversationRounds.length > 0 || timelineEntries.length > 0"
      :scroll-el="chatScrollContainer"
      :has-earlier="hasMoreEarlierRounds"
      :loading-earlier="isLoadingEarlier"
      :has-draft-images="props.hasDraftImages"
      @jump="onRailJump"
      @load-earlier="loadEarlierRounds"
    />

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

    <!-- 消息搜索（E-c1）：入口是会话区上方的「搜索消息」（ViewTrialLinks，只在预览视图出现）；点中结果后面板收成一行输入条，目标卡露出来 -->
    <PreviewSearchPanel
      v-if="previewSearch.open.value"
      :workflow-id="confirmStore.workflowId || currentWorkflowId || ''"
      @close="previewSearch.close()"
      @pick="jumpToHit"
    />
  </div>
</template>

<style scoped>
/*
  竖图底色统一（#120 阶段 1）：附件缩略图用 bg-muted，Markdown 内嵌图在这里补同一个底色（--muted 就是 bg-muted 的值，暗色主题自动跟着换）。
  作用域只限预览列表：不改共享的 markdownSafe / MarkdownRenderer，经典视图里的内嵌图保持原样。
*/
.pv-list :deep(.markdown-body img) {
  background-color: var(--muted);
}

/*
  历史轮次离屏跳过渲染（#158）：content-visibility:auto 让离开视口的轮次不参与样式 / 布局 / 绘制 / 命中测试；
  contain-intrinsic-size 的 auto 让浏览器记住每一轮渲染过的真实高度，没渲染过的先按 900px 占位。
  占位要等轮次真的靠近视口（约半屏以内）渲染了才会变成真实高度；右侧轨量位置时的强制布局只是临时算一遍，并不会让浏览器记住。
  几个要点：
  - content-visibility:auto 隐含 contain:layout style paint（渲染着的时候也一样）；其中 paint 会把画到轮次盒子外面的东西
    （高亮外框 ring-2、焦点环、阴影）削掉，overflow-clip-margin 把裁剪边放宽 8px，足够容下它们（Chrome 实测有效）；
  - 浏览器自带的页内查找、Tab 焦点、文字选择都不受影响（auto 跳过的内容仍可被找到 / 聚焦）；
  - 占位 → 真实的高度变化发生在视口上方时，靠浏览器的滚动锚定（overflow-anchor）补回去：向上滚动、向上载入、点击跳转都依赖它，
    useScrollSettle 的 settle / hold 补它补不了的几种情形。补不了的一种：锚定会选中「视口顶上露出一小截的占位盒子」做锚点，
    那个盒子自己晚一帧变高它是不补的——所以向上载入时紧挨着锚点的几轮豁免 pv-cv（boundaryRounds）、插入时就是真实高度。
    只在 Chrome 上验证过（e2e/preview-longlist.mjs 把 CPU 放慢 4 倍逐帧记录；ro/verify-preview-list.sh 用真实长会话再核对一遍）；
  - 只有同时支持 content-visibility 与 overflow-clip-margin 的浏览器才启用：不支持后者的会削掉外框，宁可不启用。
*/
@supports (content-visibility: auto) and (overflow-clip-margin: 1px) {
  .pv-cv {
    content-visibility: auto;
    contain-intrinsic-size: auto 900px;
    overflow-clip-margin: 8px;
  }
}

/*
  进度通知卡的离屏跳过渲染（#169，规则与取舍见 lib/noticeCv.ts）：同样的 content-visibility:auto，占位高度按一张通知卡的典型高度取 140px
  （渲染过的由浏览器记住真实高度）。轮次的 pv-cv 管不到末尾的通知流，而生产上一个工作流的末尾就有上百张通知卡。
  前提与 pv-cv 相同：要同时支持 content-visibility 与 overflow-clip-margin（右侧轨点击后的落点外框会被 paint 包含削掉，要放宽 8px）。
*/
@supports (content-visibility: auto) and (overflow-clip-margin: 1px) {
  .pv-cv-notice {
    content-visibility: auto;
    contain-intrinsic-size: auto 140px;
    overflow-clip-margin: 8px;
  }
}

/*
  右侧轨点击跳转后的落点提示（#120 阶段 2）：给目标条目的根节点短暂套一圈外框再淡出。
  轮次走经典的 ring 高亮（highlightedSessionId），这里只管其余条目（data-rail-flash 由 onRailJump 加上 / 去掉）。
*/
.pv-list :deep([data-rail-flash]) {
  animation: pv-rail-flash 1.6s ease-out 1;
  border-radius: 0.5rem;
}
@keyframes pv-rail-flash {
  0% {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
    background-color: color-mix(in srgb, var(--ring) 10%, transparent);
  }
  100% {
    outline: 2px solid transparent;
    outline-offset: 3px;
    background-color: transparent;
  }
}
@media (prefers-reduced-motion: reduce) {
  .pv-list :deep([data-rail-flash]) {
    animation-duration: 0.01s;
  }
}
</style>
