<script setup lang="ts">
// 群聊里不是「谁说了什么」的四种行（#165 第二步，取代旧气泡组件里的同名分支）：
//   系统行（居中的一句话）、带卡片的提问 / 计划（沿用交流页里的确认卡，作答就在卡上）、文档决策汇总卡、任务进展（一行）。
// 哪些消息走这里由 lib/groupCard.groupRenderKind 判定（'event'）；别人说的话与您的话由 GroupMessageCard 画成统一消息卡。
// DOM 与 data-testid（chat-system / chat-card / chat-decision / chat-task-update）和 data-message-id（跳转与搜索定位用）保持原样。
import { computed } from 'vue'
import { ListChecks } from 'lucide-vue-next'
import type { ChatMessage } from '../../api/conversations'
import { clockTime } from '../../lib/chatView'
import type { DecisionGroup } from '../../lib/timeline'
import ConfirmTimelineCard from '../confirms/ConfirmTimelineCard.vue'
import DecisionGroupCard from '../confirms/DecisionGroupCard.vue'

const props = defineProps<{ message: ChatMessage }>()

const m = computed(() => props.message)
const isSystem = computed(() => m.value.sender.kind === 'system')
const isCard = computed(() => (m.value.kind === 'ask' || m.value.kind === 'plan') && !!m.value.card)
const isDecision = computed(() => m.value.kind === 'decision' && !!m.value.decision)
const isTask = computed(() => m.value.kind === 'progress' && !!m.value.task)
const timeText = computed(() => clockTime(m.value.sent_at))

const decisionGroup = computed<DecisionGroup | null>(() => {
  const d = m.value.decision
  if (!d) return null
  return {
    key: `decisions-${d.doc_id}`,
    docId: d.doc_id,
    docPath: d.doc_path ?? '',
    docTitle: d.title ?? d.doc_path ?? '',
    pending: d.pending,
    answered: d.items.length - d.pending,
    total: d.items.length,
    at: m.value.sent_at,
  }
})

const taskVerb = computed(() => {
  const t = m.value.task
  if (!t) return ''
  if (t.from_state && t.to_state && t.from_state !== t.to_state) return `${t.from_state} → ${t.to_state}`
  return t.state
})
</script>

<template>
  <!-- 系统行：居中的一句话 -->
  <div v-if="isSystem" class="flex justify-center px-3 py-1.5" data-testid="chat-system" :data-message-id="m.id">
    <span class="max-w-[85%] rounded-full bg-muted/60 px-3 py-0.5 text-center text-[11px] text-muted-foreground">
      {{ m.text }}
    </span>
  </div>

  <!-- 确认卡：沿用交流页里的卡片，作答就在卡上 -->
  <div v-else-if="isCard && m.card" class="px-3 py-1.5" data-testid="chat-card" :data-message-id="m.id">
    <div class="mx-auto max-w-[min(92%,44rem)]">
      <ConfirmTimelineCard :item="m.card" />
    </div>
  </div>

  <!-- 文档决策汇总卡 -->
  <div v-else-if="isDecision && decisionGroup" class="px-3 py-1.5" data-testid="chat-decision" :data-message-id="m.id">
    <div class="mx-auto max-w-[min(92%,44rem)]">
      <DecisionGroupCard :group="decisionGroup" />
    </div>
  </div>

  <!-- 任务进展：一行 -->
  <div v-else-if="isTask && m.task" class="px-3 py-0.5" data-testid="chat-task-update" :data-message-id="m.id" :data-seq="m.task.seq">
    <div class="mx-auto flex max-w-[min(92%,44rem)] items-start gap-2 rounded-sm border border-border/60 bg-muted/20 px-3 py-1.5 text-xs">
      <ListChecks class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
      <div class="min-w-0 flex-1">
        <span class="font-mono text-[11px] font-semibold text-foreground/70">#{{ m.task.seq }}</span>
        <span class="ml-1.5 break-words text-[13px] font-medium text-foreground">{{ m.task.title }}</span>
        <span class="ml-1.5 rounded-xs border px-1 text-[10px] text-muted-foreground">{{ taskVerb }}</span>
        <p v-if="m.text" class="mt-0.5 break-words text-[11px] text-muted-foreground">{{ m.text }}</p>
      </div>
      <span class="shrink-0 font-mono text-[10px] text-muted-foreground/70">{{ timeText }}</span>
    </div>
  </div>
</template>
