<script setup lang="ts">
// 「您的确认回复」卡（#120 · D-273 阶段 C，只给预览视图用）。
//
// 您对某一轮汇报的回复：靠右、窄一档、5% 绿色底；头部是「您」· 「回复」标签 · 「对第 N 轮的确认」· 提交时间 · 智能体取走了没有
// （待读取 / 已读〔预览视图的状态词汇表 lib/statusWords，经典仍是「待 AI 提取 / AI 已接收」〕，悬停看几点取走、隔了多久）· ⋯；正文依次是预设标签、回复文字、附件缩略图。
// 智能体还没取走的回复可以撤回（内容退回输入框重新编辑）：按钮放在脚注，取走后消失。
// 提交时间取「您提交回复的时刻」（replySentAt），不是智能体取走它的时刻。
import { computed } from 'vue'
import { CheckCheck, Clock, Undo2 } from 'lucide-vue-next'
import type { FeedbackSession } from '../../../api/types'
import { cardTimeText, type CardMenuItem } from '../../../lib/messageCard'
import { formatClock, receivedTitle, replySentAt } from '../../../lib/sessionTimer'
import { STATUS_WORD } from '../../../lib/statusWords'
import LinkedText from '../../work/LinkedText.vue'
import Badge from '../../ui/badge/Badge.vue'
import { copyWithToast } from './cardCopy'
import PreviewAttachments from './PreviewAttachments.vue'
import PreviewBodyGate from './PreviewBodyGate.vue'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'

const props = defineProps<{
  session: FeedbackSession
  /** 这是第几轮（从 1 数起），用在标题「对第 N 轮的确认」和附件的预览名里。 */
  turn: number
}>()

const emit = defineEmits<{ (e: 'revoke'): void }>()

const sentAt = computed(() => replySentAt(props.session))
const bodyText = computed(() => props.session.response_text || '（已确认，未附加文字）')

// 轻量行（正文还没取回，见 PreviewBodyGate）手里只有首行预览，不能复制「回复」：取回之前先不给这一项
const menuItems = computed<CardMenuItem[]>(() => props.session.body_omitted
  ? []
  : [{ key: 'copy-body', label: '复制回复', onSelect: () => void copyWithToast(props.session.response_text || '', '回复') }])
</script>

<template>
  <PreviewMessageCard
    type="reply"
    :title="`对第 ${turn} 轮的确认`"
    :time="cardTimeText(sentAt)"
    :time-title="`你提交回复的时间 ${formatClock(sentAt)}`"
    data-testid="reply-card"
    :data-session-id="session.session_id"
    :data-consumed="session.consumed_by_ai ? '1' : '0'"
  >
    <template #status>
      <!-- 智能体消费状态：还没来取 / 已取走 -->
      <Badge
        v-if="!session.consumed_by_ai"
        variant="secondary"
        title="回复已提交，智能体还没来取"
        class="flex shrink-0 items-center gap-1 whitespace-nowrap rounded-xs border border-amber-500/30 bg-amber-500/15 px-1.5 py-0 text-[10px] font-normal text-amber-600 dark:text-amber-400"
        data-testid="card-status"
      >
        <Clock class="size-2.5 animate-pulse" />
        <span>{{ STATUS_WORD.unread }}</span>
      </Badge>
      <Badge
        v-else
        variant="outline"
        :title="receivedTitle(session)"
        class="flex shrink-0 items-center gap-1 whitespace-nowrap rounded-xs px-1.5 py-0 text-[10px] font-normal text-muted-foreground/80"
        data-testid="card-status"
      >
        <CheckCheck class="size-2.5 text-emerald-500" />
        <span>{{ STATUS_WORD.read }}</span>
      </Badge>
    </template>
    <template #menu>
      <PreviewCardMenu :items="menuItems" />
    </template>

    <PreviewBodyGate :session="session" :lines="2">
      <div class="space-y-2">
        <!-- 您选的预设标签 -->
        <div v-if="session.user_messages && session.user_messages.length > 0" class="flex flex-wrap gap-1">
          <span
            v-for="msg in session.user_messages"
            :key="msg"
            class="rounded-xs border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px] text-foreground sm:px-2 sm:text-[11px]"
          >{{ msg }}</span>
        </div>
        <LinkedText class="whitespace-pre-wrap [overflow-wrap:anywhere] leading-relaxed text-foreground" :text="bodyText" :workflow-id="session.workflow_id" />
        <PreviewAttachments :images="session.images" :key-prefix="session.session_id" :fallback-name="`第 ${turn} 轮用户附件`" />
      </div>
    </PreviewBodyGate>

    <!-- 撤回：仅在智能体尚未取走时可用，内容会自动退回输入框重新编辑 -->
    <template v-if="!session.consumed_by_ai" #footer>
      <button
        type="button"
        class="group inline-flex shrink-0 cursor-pointer select-none items-center gap-1 rounded border border-border/80 px-2 py-0.5 font-mono text-[11px] text-muted-foreground transition-colors hover:border-destructive/40 hover:bg-destructive/10 hover:text-destructive"
        title="点击撤回此条反馈（内容将自动退回输入框重新编辑）"
        data-testid="reply-retract"
        @click="emit('revoke')"
      >
        <Undo2 class="size-3 transition-transform group-hover:-translate-x-0.5" />
        <span>撤回反馈</span>
      </button>
    </template>
  </PreviewMessageCard>
</template>
