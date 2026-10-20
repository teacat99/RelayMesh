<script setup lang="ts">
// 「您的追加信息」卡（#120 · D-273 阶段 C，只给预览视图用）。
//
// 无交互期间您追加的指令（D-212 的追加指令收件箱），按创建时间落在对话里：靠右、窄一档、5% 绿色底；
// 头部是「您」· 「追加」标签 · 创建时间 · 状态徽标（待读取 / 已读 / 已过期〔预览视图的状态词汇表 lib/statusWords〕，悬停看全称：已排队 / Agent 已读取 / Agent 已确认）· hook 提取详情（经哪条路径读到、什么时候）· ⋯；
// 正文依次是预设标签、追加文字、附件缩略图；还能撤回的（已排队、已读取未确认）在脚注有「撤回」按钮。
// 状态由列表按 10 秒一跳的时钟算好（inboxStatus）后传进来，这里只负责摆放。
// 根节点保留 data-testid="inbox-item-<id>" 与 data-state，状态徽标 / 详情 / 撤回的 testid 也与经典一致。
import { computed } from 'vue'
import { CheckCheck, Clock, Undo2 } from 'lucide-vue-next'
import type { QueuedFeedback } from '../../../api/types'
import type { InboxStatusView } from '../../../lib/inbox'
import { cardTimeText, type CardMenuItem } from '../../../lib/messageCard'
import { inboxBadgeWord } from '../../../lib/statusWords'
import LinkedText from '../../work/LinkedText.vue'
import Badge from '../../ui/badge/Badge.vue'
import { copyWithToast } from './cardCopy'
import PreviewAttachments from './PreviewAttachments.vue'
import PreviewCardMenu from './PreviewCardMenu.vue'
import PreviewMessageCard from './PreviewMessageCard.vue'

const props = defineProps<{
  q: QueuedFeedback
  status: InboxStatusView
}>()

const emit = defineEmits<{ (e: 'revoke'): void }>()

/** 状态徽标配色：与「您的确认回复」卡的「待读取 / 已读」同一套。 */
const badgeClass = computed(() => {
  const tone = props.status.tone
  if (tone === 'pending') return 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border border-amber-500/30'
  if (tone === 'read') return 'bg-sky-500/15 text-sky-600 dark:text-sky-400 border border-sky-500/30'
  return 'text-muted-foreground/80'
})

const menuItems = computed<CardMenuItem[]>(() => [
  { key: 'copy-body', label: '复制追加内容', onSelect: () => void copyWithToast(props.q.response_text || '', '追加内容') }
])
</script>

<template>
  <PreviewMessageCard
    type="append"
    :time="cardTimeText(q.created_at)"
    :time-title="q.created_at"
    :data-testid="`inbox-item-${q.id}`"
    :data-state="status.stage"
  >
    <template #status>
      <Badge
        :variant="status.tone === 'pending' || status.tone === 'read' ? 'secondary' : 'outline'"
        class="flex shrink-0 items-center gap-1 whitespace-nowrap rounded-xs px-1.5 py-0 text-[10px] font-normal"
        :class="badgeClass"
        :title="status.label"
        :data-testid="`inbox-label-${q.id}`"
        :data-stage="status.stage"
      >
        <Clock v-if="status.stage === 'queued'" class="size-2.5 animate-pulse" />
        <CheckCheck v-else-if="status.stage === 'delivered'" class="size-2.5" />
        <CheckCheck v-else-if="status.stage === 'acked'" class="size-2.5 text-emerald-500" />
        <Clock v-else class="size-2.5" />
        <span>{{ inboxBadgeWord(status) }}</span>
      </Badge>
      <!-- hook 提取详情：宽屏放头部，窄屏放不下，挪到正文底部 -->
      <span
        class="hidden min-w-0 max-w-[12rem] truncate font-mono text-[10px] text-muted-foreground sm:inline"
        :title="status.detail"
        :data-testid="`inbox-detail-${q.id}`"
      >{{ status.detail }}</span>
    </template>
    <template #menu>
      <PreviewCardMenu :items="menuItems" />
    </template>

    <div class="space-y-2">
      <div v-if="q.user_messages && q.user_messages.length > 0" class="flex flex-wrap gap-1">
        <span
          v-for="msg in q.user_messages"
          :key="msg"
          class="rounded-xs border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px] text-foreground sm:px-2 sm:text-[11px]"
        >{{ msg }}</span>
      </div>
      <LinkedText class="whitespace-pre-wrap [overflow-wrap:anywhere] leading-relaxed text-foreground" :text="q.response_text || '（无文字内容）'" :workflow-id="q.workflow_id" />
      <!-- 已确认 / 已过期的条目服务端不再内联 base64，改给按需取图的直链，由 imageLoader 带鉴权加载；回看历史追加时仍要看得到当时附的截图 -->
      <PreviewAttachments :images="q.images" :key-prefix="`inbox-${q.id}`" fallback-name="暂存附件" />
      <p
        class="font-mono text-[10px] leading-relaxed text-muted-foreground sm:hidden"
        :data-testid="`inbox-detail-m-${q.id}`"
      >{{ status.detail }}</p>
    </div>

    <template v-if="status.canRetract" #footer>
      <button
        type="button"
        class="group inline-flex shrink-0 cursor-pointer select-none items-center gap-1 rounded border border-border/80 px-2 py-0.5 font-mono text-[11px] text-muted-foreground transition-colors hover:border-destructive/40 hover:bg-destructive/10 hover:text-destructive"
        :title="status.retractTitle"
        :data-testid="`inbox-retract-${q.id}`"
        @click="emit('revoke')"
      >
        <Undo2 class="size-3 transition-transform group-hover:-translate-x-0.5" />
        <span>{{ status.retractLabel }}</span>
      </button>
    </template>
  </PreviewMessageCard>
</template>
