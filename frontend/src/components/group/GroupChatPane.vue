<script setup lang="ts">
// 群聊窗口（#165 第二步，取代 chat/ChatView）：一个群的消息流 = 智能体的汇报、您的发言、确认卡、任务进展，按时间排序，
// 数据来自统一消息接口（/conversations/grp:…/messages）。画法只有一种——统一消息卡（具名发送者 + 成员色、日期分隔、「以下为新消息」），
// 不再受「预览视图」开关影响。窗口自己负责：页头（群名 / 群级状态徽标 / 头像堆 / 成员数 / 已归档 / 搜索 / 成员）、跨项目提醒、
// 已读游标、翻历史保持位置、回到最新、成员面板与输入框；搜索面板用预览视图的同一个（PreviewSearchPanel，#241），
// 这里只管「点中结果后落到哪条消息」（openResult）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDown, Loader2, RefreshCw, Search, Users } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import type { ChatMessage } from '../../api/conversations'
import { chatErrorMessage } from '../../api/conversations'
import { buildRows } from '../../lib/chatView'
import { mentionCandidates } from '../../lib/group'
import { assignMemberHues, avatarStack, candidateIdentities, firstUnreadId, groupRenderKind, memberIdentity } from '../../lib/groupCard'
import { overlayLiveState } from '../../lib/groupStatus'
import type { RailItem } from '../../lib/timelineRail'
import { useAgentStateStore } from '../../stores/agentState'
import { useChatStore } from '../../stores/chat'
import PreviewDateSeparator from '../feedback/preview-view/PreviewDateSeparator.vue'
import PreviewSearchPanel from '../feedback/preview-view/PreviewSearchPanel.vue'
import PreviewTimelineRail from '../feedback/preview-view/PreviewTimelineRail.vue'
import PreviewUnreadDivider from '../feedback/preview-view/PreviewUnreadDivider.vue'
import GroupComposer from './GroupComposer.vue'
import GroupEventRow from './GroupEventRow.vue'
import GroupMembersPanel from './GroupMembersPanel.vue'
import GroupMessageCard from './GroupMessageCard.vue'
import GroupStatusBadge from './GroupStatusBadge.vue'
import MemberAvatar from './MemberAvatar.vue'

/** workflowId = 群的会话键（grp:…），与统一消息接口、草稿键、消息卡里的引用链接装饰用的是同一个键。 */
const props = defineProps<{ workflowId: string }>()

const store = useChatStore()
const scroller = ref<HTMLElement | null>(null)
const topSentinel = ref<HTMLElement | null>(null)
const content = ref<HTMLElement | null>(null)
const composer = ref<InstanceType<typeof GroupComposer> | null>(null)
const atBottom = ref(true)
const searchOpen = ref(false)
/** 搜索请求不带 kinds（空数组）：群消息没有汇报 / 提问这些种类，服务端搜群消息也只看文字、不看 kinds，不把工作流会话的种类词表带进来 */
const SEARCH_ALL_KINDS: readonly string[] = []

const membersOpen = ref(false)
const rows = computed(() => buildRows(store.items))
const groupName = computed(() => store.group?.title || '群聊')
const candidates = computed(() => mentionCandidates(store.group?.members ?? []))
const archived = computed(() => !!store.group?.archived)
const memberCount = computed(() => (store.group?.members ?? []).filter(m => !m.left).length)

// 成员的状态以 Agent 状态仓库里的实时会话为准（lib/groupStatus.ts 的 overlayLiveState，#241 第三步）：服务端随群页带的那份只在群里有事件时
// 才重取，成员干完活变空闲、冒出一条「等你回答」不一定伴随群消息。页头徽标与成员面板读同一份，两处不会各说各话；仓库还没取到时回落到页面带的那份。
const agentStore = useAgentStateStore()
const liveMembers = computed(() => overlayLiveState(store.group?.members ?? [], wf => agentStore.entryFor(wf).primary))
const liveGroup = computed(() => (store.group ? { ...store.group, members: liveMembers.value } : null))

// 成员色相表算一次，逐张消息卡传下去（同一位成员在页头、成员面板、@ 选择器、消息卡里是同一个颜色）。
const hues = computed(() => assignMemberHues(store.group?.members ?? []))
// 页头的头像堆（最多 3 位 + 「+N」）：点一下开 / 关成员面板。
const stack = computed(() => avatarStack(store.group?.members ?? [], 3))
const stackTitle = computed(() =>
  (store.group?.members ?? [])
    .filter(m => m.kind === 'workflow' && !m.left)
    .map(m => m.name || m.ref)
    .join('、'),
)
const pickerIdentities = computed(() => candidateIdentities(candidates.value, store.group?.members ?? [], hues.value))
// 「以下为新消息」画在哪条前面：打开群、取到第一页时按您的已读游标算一次并记住——游标随后推进、新消息进来都不重算，分隔线才不会在眼前跳。
const unreadDividerId = ref('')
function humanReadSeq(): number {
  return store.group?.members.find(m => m.kind === 'human')?.read_seq ?? 0
}
const NEAR_BOTTOM_PX = 80

function isNearBottom(): boolean {
  const el = scroller.value
  if (!el) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX
}

function scrollToBottom(smooth = false) {
  const el = scroller.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
}

// ---- 已读游标：看得见、且停在底部约 1 秒，才把游标推到最新一条 ----
let readTimer: ReturnType<typeof setTimeout> | null = null
function scheduleRead() {
  if (readTimer) clearTimeout(readTimer)
  readTimer = setTimeout(() => {
    readTimer = null
    if (document.visibilityState === 'visible' && atBottom.value && !store.hasMoreAfter) void store.markRead()
  }, 1000)
}

function onScroll() {
  atBottom.value = isNearBottom()
  if (atBottom.value) {
    if (store.hasMoreAfter) void store.loadNewer()
    scheduleRead()
  }
}

// ---- 翻历史：顶部哨兵进入视口就加载上一页，加载完保持原来的位置不跳 ----
let observer: IntersectionObserver | null = null
let rearmTimer: ReturnType<typeof setTimeout> | null = null
async function loadOlderKeepingPosition() {
  const el = scroller.value
  if (!el || store.loadingOlder || !store.hasMoreBefore) return
  const before = el.scrollHeight
  const top = el.scrollTop
  const loadedBefore = store.items.length
  await store.loadOlder()
  await nextTick()
  el.scrollTop = top + (el.scrollHeight - before)
  // 只有真的取回了更早的消息才重新武装：失败时 items 没变，不重试，免得服务出错时每 60 毫秒打一次接口
  if (store.items.length > loadedBefore) scheduleRearmObserver()
}

/**
 * IntersectionObserver 只在哨兵「进入 / 离开」范围的那一下通知。这一页刚载完的几十毫秒里：
 * 用户（或脚本）又回到了最上面，而「离开」的通知还没送出——浏览器看来哨兵一直待在范围内，之后不会再有「进入」的通知；
 * 或者进入的那次通知撞上「正在加载」被丢掉。列表就卡在这一页：已经在最上面，再往上滚也没用，要先往下滚再回来才恢复。
 * 重新观察一次，浏览器会按「当前」状态补发一次通知（预览视图的 rearmTopObserver 是同一个办法）。
 */
function scheduleRearmObserver() {
  if (rearmTimer) clearTimeout(rearmTimer)
  rearmTimer = setTimeout(() => {
    rearmTimer = null
    const sentinel = topSentinel.value
    if (!observer || !sentinel) return
    observer.unobserve(sentinel)
    observer.observe(sentinel)
  }, 60)
}

function setupObserver() {
  observer?.disconnect()
  if (!scroller.value || !topSentinel.value || typeof IntersectionObserver === 'undefined') return
  observer = new IntersectionObserver(
    entries => {
      if (entries.some(e => e.isIntersecting)) void loadOlderKeepingPosition()
    },
    { root: scroller.value, rootMargin: '120px 0px 0px 0px' },
  )
  observer.observe(topSentinel.value)
}

// ---- 打开 / 切换群 ----
async function openConversation() {
  searchOpen.value = false
  membersOpen.value = false
  unreadDividerId.value = ''
  await store.open(props.workflowId)
  unreadDividerId.value = firstUnreadId(store.items, humanReadSeq(), store.hasMoreBefore)
  await nextTick()
  scrollToBottom()
  atBottom.value = true
  scheduleRead()
  setupObserver()
  watchContentHeight()
}
watch(() => props.workflowId, () => void openConversation())

// 有新消息：停在底部就跟随，否则只亮「回到最新」；您自己刚发的消息总是滚到底
watch(
  () => store.items[store.items.length - 1]?.id,
  async (id, prev) => {
    if (!id || id === prev) return
    const mine = store.items[store.items.length - 1]?.sender.kind === 'human'
    const follow = atBottom.value || mine
    await nextTick()
    if (follow && !store.hasMoreAfter) {
      scrollToBottom()
      scheduleRead()
    }
  },
)

function onVisibility() {
  if (document.visibilityState === 'visible') scheduleRead()
}

// 停在底部时内容还会变高（Markdown / 图片 / 提示条晚一拍渲染完）：高度一变就补一次贴底，否则会差一截
let resizeObserver: ResizeObserver | null = null
function watchContentHeight() {
  resizeObserver?.disconnect()
  if (!content.value || typeof ResizeObserver === 'undefined') return
  resizeObserver = new ResizeObserver(() => {
    if (atBottom.value && !store.hasMoreAfter && !store.loadingOlder) scrollToBottom()
  })
  resizeObserver.observe(content.value)
}

onMounted(() => {
  watchContentHeight()
  document.addEventListener('visibilitychange', onVisibility)
  // 页面里没有别处在拉 Agent 状态时（比如侧栏没挂载）自己拉一次；取到过就不重复（与会话页头的状态徽标同一个做法）
  agentStore.ensureFresh()
  void openConversation()
})
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', onVisibility)
  observer?.disconnect()
  resizeObserver?.disconnect()
  if (rearmTimer) clearTimeout(rearmTimer)
  if (readTimer) clearTimeout(readTimer)
  void store.open('')
})

// ---- 发送 ----
async function onSend(p: { text: string; mentions: string[] }) {
  try {
    await store.send({
      text: p.text,
      mentions: p.mentions.length ? p.mentions : undefined,
    })
    composer.value?.reset()
    await nextTick()
    atBottom.value = true
    scrollToBottom()
  } catch (e) {
    toast.error(chatErrorMessage(e, '发送失败，输入已保留'))
  }
}

// ---- 定位到某条消息（搜索命中落点）----
function scrollToMessage(id: string) {
  const el = scroller.value?.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(id)}"]`)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.add('chat-flash')
  setTimeout(() => el.classList.remove('chat-flash'), 1600)
}

// ---- 右侧缩略轴（#241）：别的成员说的话各一道刻度；轨只描述已载入的消息，点刻度直接滚到那条并闪一下 ----
function onRailJump(item: RailItem) {
  scrollToMessage(item.id)
}

// ---- 搜索：面板是预览视图的那一个，点中结果后它自己收成一行输入条（目标露出来），这里不关它 ----
async function openResult(m: ChatMessage) {
  const known = store.items.some(x => x.id === m.id)
  if (!known) await store.jumpTo(m.cursor)
  await nextTick()
  scrollToMessage(m.id)
}

// 回到最新：整段内容被换成最新一页，直接贴底（不用平滑滚动），并把「在底部」先记上（与打开群时同一套做法）。
// 以前「在底部」要靠滚动事件记成真：e2e 的采样日志里有两种时序——列表清空后立刻来一个 scroll 事件（记成真，内容变高时会补贴底），
// 或者直到最新一页的 50 条渲染完才来（记成假，不补贴底）；记成假时，翻上一页（顶部哨兵）按「保持原位」把列表留在这一页的顶上，
// 平滑滚动被它打断，列表停在这一页的顶部、「回到最新」还亮着（group-window 套件第 3 步末尾因此出现过「回到底部」超时）。
async function toLatest() {
  await store.backToLatest()
  await nextTick()
  atBottom.value = true
  scrollToBottom()
}
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col bg-background" data-testid="chat-view" :data-workflow-id="workflowId">
    <!-- 页头：群名、成员头像堆、成员数、搜索、成员面板（手机上成员数让位给群名，只留头像） -->
    <header class="flex items-center gap-2 border-b border-border px-3 py-1.5">
      <Users class="size-4 shrink-0 text-muted-foreground" />
      <span class="min-w-0 flex-1 truncate text-[13px] font-medium" data-testid="chat-title">{{ groupName }}</span>
      <!-- 群级状态徽标（#241 第三步）：谁在等你 / 谁在干活；已归档的群不画（归档后没有「谁在等你」可说）；点一下开 / 关成员面板 -->
      <GroupStatusBadge v-if="!archived" :members="liveMembers" :expanded="membersOpen" @toggle="membersOpen = !membersOpen" />
      <button
        v-if="stack.shown.length"
        type="button"
        class="flex shrink-0 cursor-pointer items-center -space-x-1 rounded-full"
        :class="archived ? 'opacity-60 grayscale' : ''"
        :title="stackTitle"
        :aria-label="`群成员：${stackTitle}`"
        data-testid="group-avatar-stack"
        @click="membersOpen = !membersOpen"
      >
        <MemberAvatar v-for="m in stack.shown" :key="m.ref" :identity="memberIdentity(m, hues)" class="ring-2 ring-background" />
        <span
          v-if="stack.more"
          class="relative grid size-4 place-items-center rounded-full bg-muted text-[9px] font-medium leading-none text-muted-foreground ring-2 ring-background"
          data-testid="group-avatar-more"
        >+{{ stack.more }}</span>
      </button>
      <span v-if="memberCount" class="hidden text-[11px] text-muted-foreground sm:inline" data-testid="group-member-count">{{ memberCount }} 位成员</span>
      <span v-if="archived" class="rounded-full border border-border px-1.5 text-[10px] text-muted-foreground" data-testid="group-archived-badge">已归档</span>
      <button
        type="button"
        class="flex size-7 cursor-pointer items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        title="搜索消息"
        data-testid="chat-search-toggle"
        @click="searchOpen = !searchOpen"
      >
        <Search class="size-4" />
      </button>
      <button
        type="button"
        class="flex h-7 cursor-pointer items-center gap-1 rounded-md px-2 text-[12px] text-muted-foreground hover:bg-accent hover:text-accent-foreground"
        :class="membersOpen ? 'bg-accent text-accent-foreground' : ''"
        title="群成员与设置"
        data-testid="group-members-toggle"
        @click="membersOpen = !membersOpen"
      >
        <Users class="size-4" />成员
      </button>
    </header>

    <!-- 跨项目群的提醒：成员来自不同项目时一直挂在顶部 -->
    <div v-if="store.group?.cross_project" class="border-b border-amber-500/30 bg-amber-500/10 px-3 py-1 text-[11px] text-amber-800 dark:text-amber-300" data-testid="group-cross-banner">
      这个群的成员来自不同项目，群里的消息会被所有成员读到。
    </div>

    <div class="relative flex min-h-0 flex-1">
    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <!-- 消息流 -->
    <div class="relative min-h-0 flex-1">
      <div ref="scroller" class="chat-scroll h-full overflow-y-auto overscroll-contain pb-3" data-testid="chat-scroll" :data-at-bottom="atBottom" :data-more-after="store.hasMoreAfter" @scroll.passive="onScroll">
        <!-- md 以上右侧给缩略轴让出位置（轨 32px + 右边距 6px）：右对齐的卡不压在刻度下面；会话页的列同样从 sm 起留白（sm:pr-6） -->
        <div ref="content" class="md:pr-9">
        <div ref="topSentinel" class="h-px" />
        <div v-if="store.loadingOlder" class="flex justify-center py-2 text-muted-foreground"><Loader2 class="size-4 animate-spin" /></div>
        <div v-else-if="store.loaded && !store.hasMoreBefore && store.items.length" class="py-2 text-center text-[11px] text-muted-foreground/70" data-testid="chat-start">—— 会话开始 ——</div>

        <div v-if="store.loading && !store.loaded" class="flex justify-center py-10 text-muted-foreground" data-testid="chat-loading">
          <Loader2 class="size-5 animate-spin" />
        </div>
        <div v-else-if="store.loaded && !store.items.length" class="px-6 py-16 text-center text-[13px] text-muted-foreground" data-testid="chat-empty">
          群里还没有消息。发一条消息，用 @ 点名成员，他们才会收到。
        </div>

        <template v-for="r in rows" :key="r.key">
          <!-- 日期分隔：与预览视图同一条 -->
          <div v-if="r.type === 'date'" class="px-3 py-2"><div class="mx-auto max-w-3xl"><PreviewDateSeparator :label="r.label ?? ''" /></div></div>
          <template v-else-if="r.message">
            <div v-if="r.message.id === unreadDividerId" class="px-3 pt-2"><div class="mx-auto max-w-3xl"><PreviewUnreadDivider /></div></div>
            <!-- 别人说的话与您的话套统一消息卡；系统行 / 确认卡 / 决策卡 / 任务进展不是「谁说了什么」，由事件行画 -->
            <div v-if="groupRenderKind(r.message) === 'card'" class="px-3 py-1.5">
              <div class="mx-auto max-w-3xl">
                <GroupMessageCard :message="r.message" :workflow-id="workflowId" :hues="hues" />
              </div>
            </div>
            <GroupEventRow v-else :message="r.message" />
          </template>
        </template>
        </div>
      </div>

      <!-- 右侧缩略轴：与会话页同一条轨（别的成员说的话各一道刻度，点击落到那条消息）；输入框在列表下面、不盖列表（dock=false），下沿只让开「回到最新」 -->
      <PreviewTimelineRail
        :scroll-el="scroller"
        :has-earlier="store.hasMoreBefore"
        :loading-earlier="store.loadingOlder"
        :has-draft-images="false"
        :dock="false"
        @jump="onRailJump"
        @load-earlier="loadOlderKeepingPosition"
      />

      <!-- 出错提示（z-30：盖在缩略轴 z-20 上面，「重试」不会被轨顶的刻度挡住） -->
      <div v-if="store.error" class="absolute inset-x-3 top-2 z-30 flex items-center gap-2 rounded-md border border-destructive/40 bg-background/95 px-3 py-1.5 text-[12px] text-destructive shadow-sm" data-testid="chat-error">
        <span class="min-w-0 flex-1 truncate">{{ store.error }}</span>
        <button type="button" class="inline-flex cursor-pointer items-center gap-1 hover:underline" @click="store.refresh()"><RefreshCw class="size-3" />重试</button>
      </div>

      <!-- 回到最新 -->
      <button
        v-if="!atBottom || store.hasMoreAfter"
        type="button"
        class="absolute bottom-3 right-4 flex cursor-pointer items-center gap-1 rounded-full border border-border bg-background px-3 py-1.5 text-[12px] shadow-md hover:bg-accent"
        data-testid="chat-to-latest"
        @click="store.hasMoreAfter ? toLatest() : scrollToBottom(true)"
      >
        <ArrowDown class="size-3.5" />
        <span>{{ store.unread > 0 ? `${store.unread} 条新消息` : '回到最新' }}</span>
      </button>

      <!-- 消息搜索：与预览视图同一个面板，浮在消息流顶部；群里没有汇报 / 提问这些种类，请求不带 kinds -->
      <PreviewSearchPanel
        v-if="searchOpen"
        :workflow-id="workflowId"
        placeholder="搜索这个群的全部消息"
        :kinds="SEARCH_ALL_KINDS"
        @close="searchOpen = false"
        @pick="openResult"
      />
    </div>

    <GroupComposer
      ref="composer"
      :workflow-id="workflowId"
      :sending="store.sending"
      :candidates="candidates"
      :identities="pickerIdentities"
      :disabled="archived"
      disabled-reason="这个群已归档，不能再发消息。在成员面板里取消归档后可继续。"
      @send="onSend"
    />
    </div>

    <!-- 群成员面板：窄屏盖在消息流上，宽屏并排 -->
    <div v-if="membersOpen && liveGroup" class="absolute inset-y-0 right-0 z-20 shadow-lg md:static md:shadow-none">
      <GroupMembersPanel :info="liveGroup" :hues="hues" @close="membersOpen = false" @changed="store.refresh()" />
    </div>
    </div>
  </section>
</template>

<style scoped>
/* 跳转定位后让目标消息闪一下 */
.chat-scroll :deep(.chat-flash) {
  animation: chat-flash 1.6s ease-out;
}
@keyframes chat-flash {
  0%,
  30% {
    background-color: color-mix(in oklab, var(--primary) 18%, transparent);
  }
  100% {
    background-color: transparent;
  }
}
</style>
