<script setup lang="ts">
import { computed } from 'vue'
import Badge from '../ui/badge/Badge.vue'
import AgentStateDot from './AgentStateDot.vue'
import {
  Workflow,
  Bot,
  Sparkles,
  Pin,
  Clock,
  CheckCircle2,
  AlertCircle,
  FolderGit2,
  X,
  Laptop
} from 'lucide-vue-next'
import type { UnifiedItem } from '../AppSidebar.vue'
import dayjs from 'dayjs'
import { useSharedNow, type SessionTimerRaw } from '../../composables/useSessionTimer'
import { stableComputed } from '../../lib/stable'
import SidebarItemTimer from './SidebarItemTimer.vue'
import { shortHash } from '../../lib/anchor'
import { useChatStore } from '../../stores/chat'
import { usePreviewView } from '../../composables/usePreviewView'
import { useSidebarSessionStatus } from '../../composables/useSidebarSessionStatus'
import { sidebarWorkingLook } from '../../lib/sidebarStatus'
import { AWAITING_AGENT_ROUND, workflowRoundStatus } from '../../lib/newSession'
import { agentDotShown, dotToneClass, stateView, textToneClass } from '../../lib/agentState'
import { useAgentStateStore } from '../../stores/agentState'
import BrailleSpinner from '../ui/spinner/BrailleSpinner.vue'
import { TONE_CLASSES } from '../work/tone'
import { cn } from '@/lib/utils'

const chatStore = useChatStore()
const previewView = usePreviewView()
const sidebarStatus = useSidebarSessionStatus()

const props = defineProps<{
  item: UnifiedItem
  isActive: boolean
  isPinned: boolean
}>()

// 「现在几点」自己读，不再由侧栏当 prop 灌下来（#215）：prop 每秒变一次，会让每张卡不管用没用到都整张重画。
// 下面凡是读 now 的推导都用 stableComputed：结果没变（绝大多数卡每一秒都没变）就不唤醒这张卡的渲染；
// 走字的计时放在 SidebarItemTimer 里自己重画。
const now = useSharedNow()

// 汇报会话卡的状态是「会话级」的（#159，Q23-b）：与页头徽标同一套推导（等待您 · N 项 / 智能体进行中 / 已取消 / 已超时 / 空闲 / 已完成），
// 不再是「最近一轮汇报」的状态词。没带工作流编号的老会话没有会话级数据可用，仍沿用下面 getStatusBadge 的本轮状态。
// 数据还没到齐时 sessionStatus 为 null：不显示徽标，免得刷新页面时先闪一个错的状态。
const usesSessionLevel = computed(() => props.item.type === 'feedback' && !!props.item.workflow_id)
// 网页新建、智能体还没接入的工作流（最近一行是占位会话，#174）：轮次状态翻成「待接入」再交给推导
const roundStatus = computed(() => workflowRoundStatus(props.item.raw as { session_id?: string }, props.item.status))
const awaitingAgent = computed(() => props.item.type === 'feedback' && roundStatus.value === AWAITING_AGENT_ROUND)
const sessionStatus = stableComputed(() =>
  usesSessionLevel.value ? sidebarStatus.statusFor(props.item.workflow_id as string, roundStatus.value, now.value) : null,
)
const statusToneClass = computed(() => {
  const tone = sessionStatus.value?.tone
  if (tone === 'amber') return TONE_CLASSES.amber.badge
  if (tone === 'sky') return TONE_CLASSES.sky.badge
  if (tone === 'muted') return 'text-muted-foreground'
  return ''
})
const statusDotClass = computed(() => (sessionStatus.value?.tone === 'sky' ? TONE_CLASSES.sky.dot : TONE_CLASSES.amber.dot))
// 「智能体进行中」在侧栏卡上画成输入区 Agent 状态芯片同款的「● ⠶ 工作中」（追加 #264）：绿色脉动点 + 盲文旋转符 + 文字，
// 点与字的颜色取芯片「工作中」那一档（tone = active）；追加 #267 去掉了外框，并且「只留一个圆点」：
// 这一行本来就可能有一个 Agent 状态小圆点（#113，hook 汇报出来的，悬停写明是哪个 Agent，不能收掉），
// 它在的时候芯片不再自带绿点（用同一条 agentDotShown 判断），没有 hook 汇报时才由芯片自带。
// 页头的会话级徽标仍写「智能体进行中」（那是另一处，没有旋转符）。
const isWorking = computed(() => sessionStatus.value?.key === 'working')
const agentState = useAgentStateStore()
const hasAgentDot = computed(
  () => props.item.type === 'feedback' && !!props.item.workflow_id && agentDotShown(agentState.entryFor(props.item.workflow_id).primary),
)
// 「工作中」这一支的颜色与文字跟页头状态徽标走同一份 Agent 状态判断（追加 #537）：原来写死成绿色，旁边的小圆点已经变橙了、
// 这里还是绿的，两处互相打架。可能卡住 → 黄色 + 「?」；疑似卡住 → 红色「疑似卡住」；没有 hook 汇报或状态正常 → 原来的绿色「工作中」。
const workingLook = stableComputed(() => {
  const wf = props.item.workflow_id
  if (props.item.type !== 'feedback' || !wf) return sidebarWorkingLook(null)
  return sidebarWorkingLook(stateView(agentState.entryFor(wf).primary, now.value))
})
const workingTitle = computed(() => [sessionStatus.value?.title ?? '', workingLook.value.hint].filter(Boolean).join('\n'))

const emit = defineEmits<{
  (e: 'select', item: UnifiedItem): void
  (e: 'contextmenu', event: MouseEvent, item: UnifiedItem): void
  (e: 'cancel', id: string): void
}>()

function getStatusBadge(item: UnifiedItem) {
  if (item.type === 'task') {
    switch (item.status) {
      case 'idle': return { label: '空闲', variant: 'outline' }
      case 'working': return { label: '执行中', variant: 'secondary' }
      case 'waiting_for_feedback': return { label: '等待反馈', variant: 'secondary' }
      case 'completed': return { label: '已完成', variant: 'default' }
      case 'failed': return { label: '失败', variant: 'destructive' }
      case 'cancelled': return { label: '已取消', variant: 'outline' }
    }
    return { label: item.status === 'completed' ? '已完工' : item.status, variant: 'outline' }
  }

  switch (item.status) {
    case 'completed': {
      const raw = item.raw as any
      if (raw && !raw.consumed_by_ai) {
        // 回复已提交、智能体还没来取；计时仍显示「等待」（从它最近一次空回执算起），不再单设「待提取」
        return { label: '已提交', variant: 'secondary' }
      }
      return { label: '已完成', variant: 'default' }
    }
    case 'pending': return { label: '等待确认', variant: 'secondary' }
    case 'cancelled': return { label: '已取消', variant: 'outline' }
    case 'timeout': return { label: '已超时', variant: 'destructive' }
    case 'archived': return { label: '已归档', variant: 'outline' }
    default: return { label: item.status, variant: 'outline' }
  }
}

function formatTime(dateStr: string) {
  if (!dateStr) return ''
  const d = dayjs(dateStr)
  if (!d.isValid()) return ''
  const nowDay = dayjs()
  if (d.isSame(nowDay, 'day')) {
    return d.format('HH:mm')
  }
  if (d.isSame(nowDay, 'year')) {
    return d.format('MM-DD HH:mm')
  }
  return d.format('YYYY-MM-DD')
}

// 交付锚点：该工作流最近一次申报的提交短哈希（取最近一轮带锚点的）；从未申报过则不出现。
function itemAnchor(item: UnifiedItem): { short: string; full: string } | null {
  if (item.type !== 'feedback') return null
  const full = item.latest_git_commit
  return full ? { short: shortHash(full), full } : null
}

function formatItemProjectDirectory(item: UnifiedItem): string {
  const rawSession = item.raw as any
  let dir = item.project_directory || rawSession?.project_directory || ''
  const host = rawSession?.host_name || ''
  if (dir && host) {
    if (!dir.startsWith(`${host}:`)) {
      return `${host}:${dir}`
    }
  }
  return dir
}
</script>

<template>
  <div
    data-testid="sidebar-card"
    :data-workflow-id="item.type === 'feedback' ? item.workflow_id : undefined"
    class="p-2.5 rounded-xs border transition-all cursor-pointer relative group"
    :class="isActive
      ? 'border-border/90 bg-muted/85 dark:bg-muted/65 shadow-inner ring-1 ring-primary/30 border-l-[3.5px] border-l-primary'
      : (item.type === 'feedback' && item.status === 'pending'
          ? 'border-border/70 bg-muted/20 hover:border-border hover:bg-muted/35'
          : 'border-transparent hover:border-border/60 hover:bg-muted/30')"
    @click="emit('select', item)"
    @contextmenu="emit('contextmenu', $event, item)"
  >
    <!-- 待确认专属小顶栏 -->
    <div
      v-if="item.type === 'feedback' && item.status === 'pending'"
      class="flex items-center justify-between pb-1.5 mb-1.5 border-b border-border/50 text-[10px] font-mono"
    >
      <div class="flex-1 flex items-center justify-center text-center px-1">
        <SidebarItemTimer variant="bar" :raw="item.raw as SessionTimerRaw" :is-active="isActive" />
      </div>

      <div class="shrink-0 flex items-center">
        <button
          type="button"
          class="px-1.5 py-0.5 rounded-2xs text-[9px] font-mono border border-destructive/30 text-destructive bg-destructive/10 hover:bg-destructive hover:text-destructive-foreground transition-colors cursor-pointer flex items-center gap-0.5"
          :title="`取消工作流: ${item.title}`"
          @click.stop="emit('cancel', item.id)"
        >
          <X class="w-2.5 h-2.5" />
          <span>取消</span>
        </button>
      </div>
    </div>

    <!-- Title Row -->
    <div class="flex items-center justify-between gap-1.5 mb-1">
      <div class="flex items-center gap-1.5 min-w-0">
        <Pin v-if="isPinned" class="w-3 h-3 text-primary shrink-0 -rotate-45 fill-primary/30" />
        <Workflow v-else-if="item.type === 'feedback'" class="w-3.5 h-3.5 shrink-0" :class="isActive ? 'text-primary' : 'text-foreground'" />
        <Sparkles v-else class="w-3.5 h-3.5 text-primary shrink-0" />
        <span data-testid="sidebar-title" class="text-xs truncate font-mono" :class="isActive ? 'font-bold text-foreground' : 'font-medium text-foreground/85 group-hover:text-foreground'">
          {{ item.title }}
        </span>
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <SidebarItemTimer
          v-if="item.type === 'feedback' && item.status === 'completed' && !awaitingAgent"
          variant="chip"
          :raw="item.raw as SessionTimerRaw"
        />
        <span
          v-if="item.type === 'feedback' && item.rounds_count && item.rounds_count > 1"
          data-testid="sidebar-rounds"
          class="text-[9px] px-1 py-0 rounded-2xs bg-muted/80 text-muted-foreground font-mono"
          :title="`该工作流包含 ${item.rounds_count} 轮交互历史`"
        >
          {{ item.rounds_count }}轮
        </span>
        <!-- Agent 状态小圆点（#113）：这个工作流的 Agent 正在干活 / 在等你 / 出了问题；没收到过 hook 汇报时不显示 -->
        <AgentStateDot v-if="item.type === 'feedback' && item.workflow_id" :workflow-id="item.workflow_id" />
        <!-- 预览视图开启时：这个会话里 Agent 发的、你还没读的消息数 -->
        <span
          v-if="previewView.enabled.value && item.type === 'feedback' && chatStore.unreadOf(item.workflow_id) > 0"
          class="min-w-4 rounded-full bg-primary px-1 text-center text-[9px] font-mono leading-4 text-primary-foreground"
          data-testid="sidebar-unread"
          :title="`${chatStore.unreadOf(item.workflow_id)} 条未读消息`"
        >
          {{ chatStore.unreadOf(item.workflow_id) > 99 ? '99+' : chatStore.unreadOf(item.workflow_id) }}
        </span>
        <!-- 汇报会话：会话级状态（与页头同一套口径）；数据没到齐前不显示 -->
        <template v-if="usesSessionLevel">
          <!-- 工作中（追加 #264 / #267）：与输入区的 Agent 状态芯片同款 —— 绿色脉动点 + 盲文旋转符 + 「工作中」，没有外框；
               这一行已经有 Agent 状态小圆点时，芯片不再自带绿点（只留一个圆点）。
               颜色与文字跟随 Agent 状态（追加 #537）：可能卡住 → 黄色 + 「?」，疑似卡住 → 红色（见 workingLook） -->
          <span
            v-if="sessionStatus && isWorking"
            class="inline-flex shrink-0 items-center gap-1 font-mono text-[9px] leading-3 text-foreground"
            :title="workingTitle"
            data-testid="sidebar-status"
            data-status="working"
            :data-tone="workingLook.tone"
          >
            <i
              v-if="!hasAgentDot"
              class="size-1.5 shrink-0 rounded-full motion-reduce:animate-none"
              :class="dotToneClass(workingLook.tone, true)"
              aria-hidden="true"
              data-testid="sidebar-status-dot"
            />
            <BrailleSpinner v-if="workingLook.spinner" size="sm" class="text-primary" data-testid="sidebar-status-spinner" />
            <span class="font-medium" :class="textToneClass(workingLook.tone)" data-testid="sidebar-status-text">{{ workingLook.text }}</span>
            <span v-if="workingLook.flag" class="shrink-0 text-amber-600 dark:text-amber-400" data-testid="sidebar-status-flag">?</span>
          </span>
          <Badge
            v-else-if="sessionStatus"
            :variant="sessionStatus.variant"
            :class="cn('text-[9px] px-1 py-0 shrink-0 font-normal rounded-2xs gap-1', statusToneClass)"
            :title="sessionStatus.title"
            data-testid="sidebar-status"
            :data-status="sessionStatus.key"
          >
            <i
              v-if="sessionStatus.pulse"
              class="size-1.5 shrink-0 animate-pulse rounded-full motion-reduce:animate-none"
              :class="statusDotClass"
              aria-hidden="true"
            />
            {{ sessionStatus.label }}
          </Badge>
        </template>
        <Badge v-else :variant="getStatusBadge(item).variant as any" class="text-[9px] px-1 py-0 shrink-0 font-normal rounded-2xs">
          {{ getStatusBadge(item).label }}
        </Badge>
      </div>
    </div>

    <!-- Summary / Subtitle Preview -->
    <p v-if="item.type === 'feedback'" class="text-[11px] text-muted-foreground line-clamp-1 leading-snug mb-1">
      {{ item.summary.replace(/[#*`]/g, '') || '暂无内容' }}
    </p>
    <p v-else class="text-[11px] text-muted-foreground line-clamp-1 font-mono mb-1">
      {{ item.subtitle }}
    </p>

    <!-- Footer Metadata Row -->
    <div class="flex items-center justify-between text-[10px] text-muted-foreground font-mono pt-1 border-t border-border/30">
      <span class="truncate max-w-[130px] flex items-center gap-1" :title="formatItemProjectDirectory(item)">
        <FolderGit2 class="w-2.5 h-2.5 shrink-0" />
        <span class="truncate">{{ formatItemProjectDirectory(item) ? formatItemProjectDirectory(item).split('/').pop() : '工作区' }}</span>
      </span>
      <span class="flex items-center gap-1.5 shrink-0">
        <span
          v-if="itemAnchor(item)"
          class="px-1 rounded-2xs bg-muted/60 text-foreground/70"
          :title="`最近一次交付锚点 ${itemAnchor(item)!.full}`"
          data-testid="sidebar-anchor"
        >{{ itemAnchor(item)!.short }}</span>
        <span>{{ formatTime(item.created_at) }}</span>
      </span>
    </div>
  </div>
</template>
